// Package mcp serves a minimal MCP stdio proxy over the CBM CLI.
// Tools: list_projects, index_status, check_index_coverage, search_graph,
// trace_path, search_code, get_architecture, get_code_snippet.
// stdin EOF = instant exit (no flush/stop). Stdout is pure JSON-RPC.
package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/true-knowledge/tk/internal/cbmexec"
	"github.com/true-knowledge/tk/internal/memory"
	"github.com/true-knowledge/tk/internal/trace"
)

type rpcReq struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      any             `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

type rpcResp struct {
	JSONRPC string  `json:"jsonrpc"`
	ID      any     `json:"id"`
	Result  any     `json:"result,omitempty"`
	Error   *rpcErr `json:"error,omitempty"`
}

type rpcErr struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type toolDef struct {
	Name        string
	Description string
	// Schema is the JSON Schema advertised for this tool in tools/list.
	// MCP requires every tool to carry one (type: object + properties).
	Schema map[string]any
}

// obj builds the outer JSON-Schema body for a tool's parameters.
func obj(required []string, properties map[string]any) map[string]any {
	s := map[string]any{"type": "object", "properties": properties}
	if len(required) > 0 {
		s["required"] = required
	}
	return s
}

func strProp(desc string) map[string]any {
	return map[string]any{"type": "string", "description": desc}
}

func intProp(desc string) map[string]any {
	return map[string]any{"type": "integer", "description": desc}
}

func boolProp(desc string) map[string]any {
	return map[string]any{"type": "boolean", "description": desc}
}

func enumProp(vals []string, desc string) map[string]any {
	return map[string]any{"type": "string", "enum": vals, "description": desc}
}

func strSliceProp(desc string) map[string]any {
	return map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": desc}
}

// projectProp is the shared routing parameter most graph tools require.
func projectProp() map[string]any {
	return strProp("Registered project name (see list_projects / `tk status`)")
}

// auditProp describes the shared list|approve|reject review action + id pair.
func auditProps() map[string]any {
	return map[string]any{
		"action": enumProp([]string{"list", "approve", "reject"}, "list (default) shows the queue; approve/reject resolve by id"),
		"id":     strProp("Review entry id; required when action is approve|reject"),
	}
}

// Profile names. scout (11) is default; analysis adds demo/power tools;
// minimal keeps the fast filter trio for <8B models; memory joins the scout
// surface with tk's 9 in-process memory tools (20 total) and works without CBM.
const (
	ProfileScout    = "scout"
	ProfileAnalysis = "analysis"
	ProfileMinimal  = "minimal"
	ProfileMemory   = "memory"
)

// memoryTools are the tk-owned, in-process memory tools (no CBM needed).
var memoryTools = []toolDef{
	{"mem_save", "Store (or queue for review) a durable fact: topic, value, scope project|global, project, provenance", obj([]string{"topic", "value"}, map[string]any{
		"topic":      strProp("Fact topic/key"),
		"value":      strProp("Fact value (secret-looking values queue for review instead)"),
		"scope":      enumProp([]string{"project", "global"}, "Where the fact applies (default project)"),
		"project":    strProp("Required when scope=project"),
		"provenance": strProp("Where the fact came from (free text)"),
	})},
	{"mem_recall", "Recall facts by topic: project-scoped first, then global", obj([]string{"topic"}, map[string]any{
		"topic":   strProp("Fact topic to recall"),
		"project": strProp("Prefer facts for this project before falling back to global"),
	})},
	{"mem_review", "Facts review queue: action=list | approve|reject with id", obj([]string{"action"}, auditProps())},
	{"note_save", "Capture a note (title, text, project) into the review queue", obj([]string{"title", "text"}, map[string]any{
		"title":   strProp("Note title"),
		"text":    strProp("Note body"),
		"project": strProp("Optional project to attach the note to"),
	})},
	{"note_search", "Search approved notes (BM25 + optional embedding fusion)", obj([]string{"query"}, map[string]any{
		"query":   strProp("Search phrase"),
		"project": strProp("Restrict to this project"),
		"limit":   intProp("Max hits (default 10)"),
	})},
	{"note_toc", "Approved note titles only, budgeted, newest first", obj(nil, map[string]any{
		"project": strProp("Restrict to this project"),
	})},
	{"note_reindex", "Rebuild the FTS index from the markdown sources of truth", obj(nil, map[string]any{
		"project": strProp("Restrict to this project"),
	})},
	{"note_review", "Notes review queue: action=list | approve|reject with id", obj([]string{"action"}, auditProps())},
	{"ledger_update", "Replace a project's working-truth ledger (bounded full-text)", obj([]string{"project", "text"}, map[string]any{
		"project": projectProp(),
		"text":    strProp("New ledger content (bounded full-text)"),
	})},
}

// tools returns the profile-dependent surface.
func tools(profile string) []toolDef {
	base := []toolDef{
		{"list_projects", "List indexed projects with node/edge counts", obj(nil, map[string]any{})},
		{"index_status", "Indexing status of a project", obj([]string{"project"}, map[string]any{
			"project": projectProp(),
		})},
		{"check_index_coverage", "Whether exact paths/scope are indexed and fresh (clean = no recorded gap, not proof)", obj([]string{"project"}, map[string]any{
			"project": projectProp(),
			"paths":   strSliceProp("Exact repo-relative paths to probe (default whole project)"),
			"scopes":  strSliceProp("Scope markers to probe (default [\".\"])"),
		})},
		{"search_graph", "Structural/semantic node search (name_pattern, label, semantic_query, limit/offset)", obj([]string{"project"}, map[string]any{
			"project":        projectProp(),
			"name_pattern":   strProp("Regex/glob node-name pattern (give this OR semantic_query)"),
			"label":          strProp("Node-label filter, e.g. Function"),
			"semantic_query": strSliceProp("Keyword array for semantic search (give this OR name_pattern)"),
			"limit":          intProp("Max results (default 20)"),
			"offset":         intProp("Pagination offset"),
		})},
		{"trace_path", "BFS callers/callees (function_name, direction, depth 1-5)", obj([]string{"project", "function_name"}, map[string]any{
			"project":       projectProp(),
			"function_name": strProp("Function to trace"),
			"direction":     enumProp([]string{"inbound", "outbound", "both"}, "inbound=callers, outbound=callees (default inbound)"),
			"depth":         intProp("Traversal depth, 1-5"),
		})},
		{"search_code", "Grep-like text search within indexed files (CBM)", obj([]string{"project", "pattern"}, map[string]any{
			"project":      projectProp(),
			"pattern":      strProp("Text or regex pattern"),
			"file_pattern": strProp("File glob filter"),
			"limit":        intProp("Max results (default 20)"),
			"regex":        boolProp("Treat pattern as a regex"),
		})},
		{"source_search", "Trigram text search via zoekt (pattern, project, files?, limit?)", obj([]string{"project", "pattern"}, map[string]any{
			"project": projectProp(),
			"pattern": strProp("Trigram search pattern"),
			"files":   strProp("Optional file glob filter"),
			"limit":   intProp("Max results (default 20)"),
		})},
		{"get_file_outline", "Declarations in one file, in source order (cheap read alternative)", obj([]string{"project", "file_path"}, map[string]any{
			"project":   projectProp(),
			"file_path": strProp("Repo-relative path to the file"),
			"labels":    strSliceProp("Node-label filter, e.g. Function,Class"),
			"limit":     intProp("Max declarations (default 100)"),
		})},
		{"detect_changes", "Working-tree diff mapped to affected symbols + blast radius", obj([]string{"project"}, map[string]any{
			"project":   projectProp(),
			"direction": enumProp([]string{"inbound", "outbound", "both"}, "Impact direction (default inbound)"),
			"depth":     intProp("Traversal depth (default 2)"),
			"limit":     intProp("Max rows (default 50)"),
		})},
		{"get_architecture", "Languages, packages, routes, hotspots overview", obj([]string{"project"}, map[string]any{
			"project": projectProp(),
		})},
		{"get_code_snippet", "Source snippet by qualified name", obj([]string{"project", "qualified_name"}, map[string]any{
			"project":        projectProp(),
			"qualified_name": strProp("Qualified symbol name, e.g. pkg.Func"),
		})},
	}
	switch profile {
	case ProfileMinimal:
		return []toolDef{
			base[2],  // check_index_coverage
			base[3],  // search_graph
			base[10], // get_code_snippet
		}
	case ProfileMemory:
		return append(base, memoryTools...)
	case ProfileAnalysis:
		return append(base,
			toolDef{"query_graph", "Read-only Cypher-style graph query (max_rows guardrail)", obj([]string{"project", "query"}, map[string]any{
				"project":  projectProp(),
				"query":    strProp("Cypher query (read-only; must carry LIMIT for safety)"),
				"max_rows": intProp("Row limit guardrail"),
			})},
			toolDef{"manage_adr", "Persist architectural decisions alongside the graph (passthrough)", obj(nil, map[string]any{
				"project": projectProp(),
				"mode":    enumProp([]string{"get", "update", "set_sections", "sections", "list", "delete"}, "ADR operation"),
			})},
			toolDef{"validate", "Symbol existence + near-miss candidates, coverage-annotated", obj([]string{"project", "symbol"}, map[string]any{
				"project": projectProp(),
				"symbol":  strProp("Exact symbol name to validate"),
				"limit":   intProp("Near-miss candidate limit (default 5)"),
			})},
		)
	default:
		return base
	}
}

var toolToCBM = map[string]string{
	"list_projects":        "list_projects",
	"index_status":         "index_status",
	"check_index_coverage": "check_index_coverage",
	"search_graph":         "search_graph",
	"trace_path":           "trace_path",
	"search_code":          "search_code",
	"get_file_outline":     "get_file_outline",
	"detect_changes":       "detect_changes",
	"get_architecture":     "get_architecture",
	"get_code_snippet":     "get_code_snippet",
}

// cbmRunner is the graph backend surface mcp needs. Concretely
// *cbmexec.Runner; interface keeps tests free of a real binary.
type cbmRunner interface {
	RunJSON(ctx context.Context, tool string, payload map[string]any) (string, error)
}

// Server proxies tool calls to `cbm cli`, except source_search (zoekt library).
type Server struct {
	Run    cbmRunner
	Budget int
	// Profile selects the tool surface: scout (11) | analysis (14) | minimal (3).
	Profile string
	// ShardsFor maps project -> zoekt shard dir.
	ShardsFor func(project string) string
	// In/Out override stdio (tests). Nil = os.Stdin/os.Stdout.
	In   io.Reader
	OutW io.Writer
	// LogPath is <state>/logs/tk.log ("": no per-call records).
	LogPath string
	// Mem is the in-process memory backend (facts/notes/ledger). Nil means
	// the memory profile tools error out; graph tools never need it.
	Mem *memory.Store
}

// Serve loops on stdin NDJSON; EOF exits 0 immediately.
func (s *Server) Serve(ctx context.Context) int {
	in := s.In
	if in == nil {
		in = os.Stdin
	}
	out := s.OutW
	if out == nil {
		out = os.Stdout
	}
	sc := bufio.NewScanner(in)
	sc.Buffer(make([]byte, 1024*1024), 1024*1024)
	enc := json.NewEncoder(out)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var req rpcReq
		if err := json.Unmarshal(line, &req); err != nil {
			_ = enc.Encode(rpcResp{JSONRPC: "2.0", ID: nil, Error: &rpcErr{-32700, "parse error"}})
			continue
		}
		resp := s.handle(ctx, req)
		_ = enc.Encode(resp)
	}
	return 0
}

// profile returns the normalized profile (default scout).
func (s *Server) profile() string {
	switch s.Profile {
	case ProfileAnalysis, ProfileMinimal, ProfileMemory:
		return s.Profile
	default:
		return ProfileScout
	}
}

func (s *Server) handle(ctx context.Context, req rpcReq) rpcResp {
	id := req.ID
	switch req.Method {
	case "initialize":
		return rpcResp{JSONRPC: "2.0", ID: id, Result: map[string]any{
			"protocolVersion": "2024-11-05",
			"serverInfo":      map[string]any{"name": "tk", "version": "0.1.0", "profile": s.profile()},
			"capabilities":    map[string]any{"tools": map[string]any{}},
		}}
	case "notifications/initialized":
		return rpcResp{JSONRPC: "2.0", ID: id, Result: map[string]any{}}
	case "tools/list":
		tl := tools(s.profile())
		names := make([]map[string]any, 0, len(tl))
		for _, t := range tl {
			names = append(names, map[string]any{"name": t.Name, "description": t.Description, "inputSchema": t.Schema})
		}
		return rpcResp{JSONRPC: "2.0", ID: id, Result: map[string]any{"tools": names}}
	case "tools/call":
		var p struct {
			Name      string         `json:"name"`
			Arguments map[string]any `json:"arguments"`
		}
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return rpcResp{JSONRPC: "2.0", ID: id, Error: &rpcErr{-32602, "invalid params"}}
		}
		t0 := time.Now()
		resp := s.callTool(ctx, id, p.Name, p.Arguments)
		s.logCall(req.Method, p.Name, p.Arguments, t0, resp)
		return resp
	case "ping":
		return rpcResp{JSONRPC: "2.0", ID: id, Result: map[string]any{}}
	default:
		return rpcResp{JSONRPC: "2.0", ID: id, Error: &rpcErr{-32601, "method not found: " + req.Method}}
	}
}

// callTool dispatches one tool invocation (extracted for timing/records).
func (s *Server) callTool(ctx context.Context, id any, name string, args map[string]any) rpcResp {
	if name == "source_search" {
		return s.callSourceSearch(ctx, id, args)
	}
	if !s.hasTool(name) {
		return rpcResp{JSONRPC: "2.0", ID: id, Error: &rpcErr{-32601, fmt.Sprintf("unknown tool %q (profile %s: %s)", name, s.profile(), strings.Join(s.toolNames(), ", "))}}
	}
	if args == nil {
		args = map[string]any{}
	}
	if isMemoryTool(name) {
		return s.callMemory(ctx, id, name, args)
	}
	if s.Run == nil {
		return rpcResp{JSONRPC: "2.0", ID: id, Error: &rpcErr{-32000, "cbm not installed; run `tk install` (fail-open: continue without graph)"}}
	}
	if name == "validate" {
		return s.callValidate(ctx, id, args)
	}
	cbmTool := toolToCBM[name]
	if cbmTool == "" {
		cbmTool = name // query_graph / manage_adr passthrough
	}
	if cbmTool == "check_index_coverage" {
		_, hasPaths := args["paths"].([]any)
		_, hasScopes := args["scopes"].([]any)
		if !hasPaths && !hasScopes {
			args["scopes"] = []string{"."} // whole-project probe
		}
	}
	out, err := s.Run.RunJSON(ctx, cbmTool, args)
	if err != nil {
		return rpcResp{JSONRPC: "2.0", ID: id, Error: &rpcErr{-32000, err.Error()}}
	}
	out = cbmexec.Truncate(out, s.Budget)
	return rpcResp{JSONRPC: "2.0", ID: id, Result: map[string]any{
		"content": []map[string]any{{"type": "text", "text": out}},
	}}
}

// hasTool reports whether name is exposed in the active profile.
func (s *Server) hasTool(name string) bool {
	for _, t := range tools(s.profile()) {
		if t.Name == name {
			return true
		}
	}
	return false
}

func (s *Server) toolNames() []string {
	tl := tools(s.profile())
	names := make([]string, len(tl))
	for i, t := range tl {
		names[i] = t.Name
	}
	return names
}

// callValidate implements the analysis-profile validate tool: exact symbol
// lookup via search_graph, near-miss candidates on failed token match, and a
// mandatory check_index_coverage annotation for absence claims.
func (s *Server) callValidate(ctx context.Context, id any, args map[string]any) rpcResp {
	str := func(k string) string {
		if v, ok := args[k].(string); ok {
			return v
		}
		return ""
	}
	sym, project := str("symbol"), str("project")
	if sym == "" || project == "" {
		return rpcResp{JSONRPC: "2.0", ID: id, Error: &rpcErr{-32602, "validate needs symbol + project"}}
	}
	limit := 5
	if v, ok := args["limit"].(float64); ok && v > 0 {
		limit = int(v)
	}
	hit, err := s.Run.RunJSON(ctx, "search_graph", map[string]any{"name_pattern": sym, "project": project, "limit": limit})
	if err != nil {
		goto coverage
	}
	if !cbmexec.LooksEmpty(hit) && strings.Contains(strings.ToLower(hit), strings.ToLower(sym)) {
		return rpcResp{JSONRPC: "2.0", ID: id, Result: map[string]any{
			"content": []map[string]any{{"type": "text", "text": cbmexec.Truncate("valid: "+sym+" found\n"+hit, s.Budget)}},
		}}
	}
coverage:
	out, cerr := s.Run.RunJSON(ctx, "check_index_coverage", map[string]any{"project": project, "scopes": []string{"."}})
	if cerr != nil {
		return rpcResp{JSONRPC: "2.0", ID: id, Error: &rpcErr{-32000, "no exact hit and coverage check failed: " + cerr.Error() + " — absence unverified"}}
	}
	verdict := cbmexec.CoverageVerdict(out)
	cands := "(no candidates)"
	if toks := cbmexec.NearMissTokens(sym); len(toks) > 0 {
		if near, nerr := s.Run.RunJSON(ctx, "search_graph", map[string]any{"name_pattern": toks[0], "project": project, "limit": limit}); nerr == nil && !cbmexec.LooksEmpty(near) {
			cands = near
		}
	}
	text := fmt.Sprintf("invalid: %q not found in %s\n== near-miss ==\n%s\n(%s)", sym, project, cands, verdict)
	return rpcResp{JSONRPC: "2.0", ID: id, Result: map[string]any{
		"content": []map[string]any{{"type": "text", "text": cbmexec.Truncate(text, s.Budget)}},
	}}
}

// logCall appends one MCP record to tk.log (best-effort, never fails calls).
// Coarser than CLI records by design: tool + timing + outcome, no backend
// breakdown (the single CBM/zoekt call per tool is unambiguous).
func (s *Server) logCall(method, tool string, args map[string]any, t0 time.Time, resp rpcResp) {
	if s.LogPath == "" {
		return
	}
	rec := map[string]any{
		"v":      1,
		"ts":     t0.Unix(),
		"dur_ms": time.Since(t0).Milliseconds(),
		"mcp":    map[string]any{"method": method, "tool": tool, "params": args},
		"exit":   0,
	}
	if resp.Error != nil {
		rec["exit"] = 1
		rec["error"] = trace.Redact(resp.Error.Message)
	} else if text := resultText(resp.Result); text != "" {
		text = trace.Redact(text)
		rec["output"] = map[string]any{"chars": len(text), "text": text}
	}
	trace.Append(s.LogPath, rec)
}

func resultText(result any) string {
	m, ok := result.(map[string]any)
	if !ok {
		return ""
	}
	content, ok := m["content"].([]map[string]any)
	if !ok || len(content) == 0 {
		return ""
	}
	text, _ := content[0]["text"].(string)
	return text
}

// callSourceSearch serves the zoekt-backed tool (in-process library —
// always present; a missing index surfaces as a `tk index` hint).
func (s *Server) callSourceSearch(ctx context.Context, id any, args map[string]any) (resp rpcResp) {
	defer func() {
		if r := recover(); r != nil {
			resp = rpcResp{JSONRPC: "2.0", ID: id, Error: &rpcErr{-32000, fmt.Sprintf("source_search panic contained: %v", r)}}
		}
	}()
	str := func(k string) string {
		if v, ok := args[k].(string); ok {
			return v
		}
		return ""
	}
	limit := 20
	if v, ok := args["limit"].(float64); ok && v > 0 {
		limit = int(v)
	}
	pattern, project := str("pattern"), str("project")
	if pattern == "" || project == "" {
		return rpcResp{JSONRPC: "2.0", ID: id, Error: &rpcErr{-32602, "source_search needs pattern + project"}}
	}
	if s.ShardsFor == nil {
		return rpcResp{JSONRPC: "2.0", ID: id, Error: &rpcErr{-32000, "text index not configured in this server"}}
	}
	text, err := QueryZoekt(ctx, s.ShardsFor(project), pattern, str("files"), limit)
	if err != nil {
		return rpcResp{JSONRPC: "2.0", ID: id, Error: &rpcErr{-32000, err.Error() + " (shards missing? run `tk index`)"}}
	}
	return rpcResp{JSONRPC: "2.0", ID: id, Result: map[string]any{
		"content": []map[string]any{{"type": "text", "text": cbmexec.Truncate(text, s.Budget)}},
	}}
}
