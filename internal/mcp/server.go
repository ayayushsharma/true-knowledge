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
	"time"

	"github.com/true-knowledge/tk/internal/cbmexec"
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
	Name        string `json:"name"`
	Description string `json:"description"`
}

// Tools exposed by tk mcp (scout profile + snippet + text).
func tools() []toolDef {
	return []toolDef{
		{"list_projects", "List indexed projects with node/edge counts"},
		{"index_status", "Indexing status of a project"},
		{"check_index_coverage", "Whether exact paths/scope are indexed and fresh (clean = no recorded gap, not proof)"},
		{"search_graph", "Structural/semantic node search (name_pattern, label, semantic_query, limit/offset)"},
		{"trace_path", "BFS callers/callees (function_name, direction, depth 1-5)"},
		{"search_code", "Grep-like text search within indexed files (CBM)"},
		{"source_search", "Trigram text search via zoekt (pattern, project, files?, limit?)"},
		{"get_file_outline", "Declarations in one file, in source order (cheap read alternative)"},
		{"detect_changes", "Working-tree diff mapped to affected symbols + blast radius"},
		{"get_architecture", "Languages, packages, routes, hotspots overview"},
		{"get_code_snippet", "Source snippet by qualified name"},
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

// Server proxies tool calls to `cbm cli`, except source_search (zoekt library).
type Server struct {
	Run    *cbmexec.Runner
	Budget int
	// ShardsFor maps project -> zoekt shard dir.
	ShardsFor func(project string) string
	// In/Out override stdio (tests). Nil = os.Stdin/os.Stdout.
	In   io.Reader
	OutW io.Writer
	// LogPath is <state>/logs/tk.log ("": no per-call records).
	LogPath string
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

func (s *Server) handle(ctx context.Context, req rpcReq) rpcResp {
	id := req.ID
	switch req.Method {
	case "initialize":
		return rpcResp{JSONRPC: "2.0", ID: id, Result: map[string]any{
			"protocolVersion": "2024-11-05",
			"serverInfo":      map[string]any{"name": "tk", "version": "0.1.0"},
			"capabilities":    map[string]any{"tools": map[string]any{}},
		}}
	case "notifications/initialized":
		return rpcResp{JSONRPC: "2.0", ID: id, Result: map[string]any{}}
	case "tools/list":
		names := make([]map[string]any, 0, len(tools()))
		for _, t := range tools() {
			names = append(names, map[string]any{"name": t.Name, "description": t.Description})
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
	cbmTool, ok := toolToCBM[name]
	if !ok {
		return rpcResp{JSONRPC: "2.0", ID: id, Error: &rpcErr{-32601, fmt.Sprintf("unknown tool %q (tk exposes scout + snippet + source_search)", name)}}
	}
	if s.Run == nil {
		return rpcResp{JSONRPC: "2.0", ID: id, Error: &rpcErr{-32000, "cbm not installed; run `tk install` (fail-open: continue without graph)"}}
	}
	if args == nil {
		args = map[string]any{}
	}
	out, err := s.Run.Run(ctx, cbmTool, args)
	if err != nil {
		return rpcResp{JSONRPC: "2.0", ID: id, Error: &rpcErr{-32000, err.Error()}}
	}
	out = cbmexec.Truncate(out, s.Budget)
	return rpcResp{JSONRPC: "2.0", ID: id, Result: map[string]any{
		"content": []map[string]any{{"type": "text", "text": out}},
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
