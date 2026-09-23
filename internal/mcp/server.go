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
	"os"

	"github.com/true-knowledge/tk/internal/cbmexec"
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

// Tools exposed by tk mcp (scout-7 + snippet + source_search).
func tools() []toolDef {
	return []toolDef{
		{"list_projects", "List indexed projects with node/edge counts"},
		{"index_status", "Indexing status of a project"},
		{"check_index_coverage", "Whether exact paths/scope are indexed and fresh (clean = no recorded gap, not proof)"},
		{"search_graph", "Structural/semantic node search (name_pattern, label, semantic_query, limit/offset)"},
		{"trace_path", "BFS callers/callees (function_name, direction, depth 1-5)"},
		{"search_code", "Grep-like text search within indexed files (CBM)"},
		{"source_search", "Trigram text search via zoekt (pattern, project, files?, limit?)"},
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
	"get_architecture":     "get_architecture",
	"get_code_snippet":     "get_code_snippet",
}

// Server proxies tool calls to `cbm cli`, except source_search (zoekt library).
type Server struct {
	Run    *cbmexec.Runner
	Budget int
	Out    *os.File
	// ShardsFor maps project -> zoekt shard dir.
	ShardsFor func(project string) string
}

// Serve loops on stdin NDJSON; EOF exits 0 immediately.
func (s *Server) Serve(ctx context.Context) int {
	in := bufio.NewScanner(os.Stdin)
	in.Buffer(make([]byte, 1024*1024), 1024*1024)
	enc := json.NewEncoder(os.Stdout)
	for in.Scan() {
		line := in.Bytes()
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
		if p.Name == "source_search" {
			return s.callSourceSearch(ctx, id, p.Arguments)
		}
		cbmTool, ok := toolToCBM[p.Name]
		if !ok {
			return rpcResp{JSONRPC: "2.0", ID: id, Error: &rpcErr{-32601, fmt.Sprintf("unknown tool %q (tk exposes scout + snippet + source_search)", p.Name)}}
		}
		if s.Run == nil {
			return rpcResp{JSONRPC: "2.0", ID: id, Error: &rpcErr{-32000, "cbm not installed; run `tk install` (fail-open: continue without graph)"}}
		}
		if p.Arguments == nil {
			p.Arguments = map[string]any{}
		}
		out, err := s.Run.Run(ctx, cbmTool, p.Arguments)
		if err != nil {
			return rpcResp{JSONRPC: "2.0", ID: id, Error: &rpcErr{-32000, err.Error()}}
		}
		out = cbmexec.Truncate(out, s.Budget)
		return rpcResp{JSONRPC: "2.0", ID: id, Result: map[string]any{
			"content": []map[string]any{{"type": "text", "text": out}},
		}}
	case "ping":
		return rpcResp{JSONRPC: "2.0", ID: id, Result: map[string]any{}}
	default:
		return rpcResp{JSONRPC: "2.0", ID: id, Error: &rpcErr{-32601, "method not found: " + req.Method}}
	}
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
