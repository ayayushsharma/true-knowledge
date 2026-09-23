package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/true-knowledge/tk/internal/zoekttext"
)

// serveOne runs a Server over a single NDJSON request line.
func serveOne(t *testing.T, s *Server, line string) map[string]any {
	t.Helper()
	var out bytes.Buffer
	s.In = strings.NewReader(line + "\n")
	s.OutW = &out
	if code := s.Serve(context.Background()); code != 0 {
		t.Fatalf("serve exit = %d", code)
	}
	var resp map[string]any
	if err := json.Unmarshal(out.Bytes(), &resp); err != nil {
		t.Fatalf("bad response %q: %v", out.String(), err)
	}
	return resp
}

func TestToolsListCount(t *testing.T) {
	tests := []struct {
		profile string
		want    int
		check   []string
		absent  []string
	}{
		{"", 11, []string{"search_graph", "source_search", "get_file_outline", "detect_changes", "check_index_coverage"}, []string{"validate", "query_graph", "manage_adr"}},
		{"scout", 11, nil, nil},
		{"analysis", 14, []string{"validate", "query_graph", "manage_adr"}, nil},
		{"minimal", 3, []string{"check_index_coverage", "search_graph", "get_code_snippet"}, []string{"source_search", "detect_changes"}},
	}
	for _, tc := range tests {
		resp := serveOne(t, &Server{Profile: tc.profile}, `{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`)
		result := resp["result"].(map[string]any)
		tools := result["tools"].([]any)
		if len(tools) != tc.want {
			t.Fatalf("[%q] tools = %d, want %d", tc.profile, len(tools), tc.want)
		}
		names := map[string]bool{}
		for _, tool := range tools {
			names[tool.(map[string]any)["name"].(string)] = true
		}
		for _, want := range tc.check {
			if !names[want] {
				t.Fatalf("[%q] missing tool %q", tc.profile, want)
			}
		}
		for _, gone := range tc.absent {
			if names[gone] {
				t.Fatalf("[%q] tool %q must be absent", tc.profile, gone)
			}
		}
	}
}

// fakeRunner satisfies the cbmexec.Runner surface for MCP tests.
type fakeRunner struct {
	bin string
}

func (r *fakeRunner) RunJSON(ctx context.Context, tool string, payload map[string]any) (string, error) {
	return "mock-out", nil
}

func TestValidateToolInAnalysisOnly(t *testing.T) {
	resp := serveOne(t, &Server{
		Profile: ProfileAnalysis,
		Run:     &fakeRunner{},
	}, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"validate","arguments":{"symbol":"Demo","project":"p"}}}`)
	if resp["error"] != nil {
		t.Fatalf("validate failed in analysis: %v", resp)
	}
}

func TestParseError(t *testing.T) {
	resp := serveOne(t, &Server{}, `{oops`)
	if resp["error"] == nil {
		t.Fatal("expected parse error")
	}
}

// TestScoutDoesNotExposeValidate checks the gate holds for calls too.
func TestScoutDoesNotExposeValidate(t *testing.T) {
	resp := serveOne(t, &Server{
		Profile: ProfileScout,
		Run:     &fakeRunner{},
	}, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"validate","arguments":{"symbol":"X","project":"p"}}}`)
	errObj := resp["error"].(map[string]any)
	if errObj["code"].(float64) != -32601 {
		t.Fatalf("code = %v", errObj["code"])
	}
}

// TestQueryGraphPassthrough verifies analysis-only tools reach cbm under
// their own name (they are not in toolToCBM).
func TestQueryGraphPassthrough(t *testing.T) {
	run := &spyRunner{}
	resp := serveOne(t, &Server{
		Profile: ProfileAnalysis,
		Run:     run,
	}, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"query_graph","arguments":{"query":"MATCH (f) RETURN f LIMIT 1","project":"p"}}}`)
	if resp["error"] != nil {
		t.Fatalf("query_graph failed: %v", resp)
	}
	if run.tool != "query_graph" {
		t.Fatalf("spawned %q, want query_graph", run.tool)
	}
}

type spyRunner struct {
	tool    string
	payload map[string]any
}

func (r *spyRunner) RunJSON(ctx context.Context, tool string, payload map[string]any) (string, error) {
	r.tool = tool
	r.payload = payload
	return "mock-out", nil
}

// TestCoverageScopesDefault verifies check_index_coverage without explicit
// paths/scopes probes the whole project.
func TestCoverageScopesDefault(t *testing.T) {
	run := &spyRunner{}
	resp := serveOne(t, &Server{
		Profile: ProfileAnalysis,
		Budget:  1024,
		Run:     run,
	}, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"check_index_coverage","arguments":{"project":"p"}}}`)
	if resp["error"] != nil {
		t.Fatalf("coverage call failed: %v", resp)
	}
	got, ok := run.payload["scopes"].([]string)
	if !ok || len(got) != 1 || got[0] != "." {
		t.Fatalf("scopes = %#v, want [.]", run.payload["scopes"])
	}
}

// TestCoverageScopesRespected skips the default when explicit scopes given.
func TestCoverageScopesRespected(t *testing.T) {
	run := &spyRunner{}
	resp := serveOne(t, &Server{
		Profile: ProfileAnalysis,
		Budget:  1024,
		Run:     run,
	}, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"check_index_coverage","arguments":{"project":"p","scopes":["src"]}}}`)
	if resp["error"] != nil {
		t.Fatalf("coverage call failed: %v", resp)
	}
	got, ok := run.payload["scopes"].([]any)
	if !ok || len(got) != 1 || got[0] != "src" {
		t.Fatalf("scopes = %#v, want [src] untouched", run.payload["scopes"])
	}
}

func TestUnknownTool(t *testing.T) {
	resp := serveOne(t, &Server{}, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"nope","arguments":{}}}`)
	errObj := resp["error"].(map[string]any)
	if errObj["code"].(float64) != -32601 {
		t.Fatalf("code = %v", errObj["code"])
	}
}

func TestSourceSearchValidation(t *testing.T) {
	resp := serveOne(t, &Server{ShardsFor: func(string) string { return t.TempDir() }},
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"source_search","arguments":{"pattern":"x"}}}`)
	errObj := resp["error"].(map[string]any)
	if errObj["code"].(float64) != -32602 {
		t.Fatalf("code = %v (missing project should be invalid params)", errObj["code"])
	}
}

func TestSourceSearchRoundTrip(t *testing.T) {
	dir := t.TempDir()
	shards := t.TempDir()
	content := "package main\n\nfunc Widget() {}\n"
	if err := os.WriteFile(filepath.Join(dir, "w.go"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := zoekttext.IndexDir(shards, dir, "p"); err != nil {
		t.Fatal(err)
	}
	s := &Server{Budget: 6000, ShardsFor: func(string) string { return shards }}
	resp := serveOne(t, s,
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"source_search","arguments":{"pattern":"Widget","project":"p"}}}`)
	result, ok := resp["result"].(map[string]any)
	if !ok {
		t.Fatalf("expected result, got %v", resp)
	}
	text := result["content"].([]any)[0].(map[string]any)["text"].(string)
	if !strings.Contains(text, "w.go") || !strings.Contains(text, "Widget") {
		t.Fatalf("text = %q", text)
	}
}

func TestSourceSearchPanicContained(t *testing.T) {
	s := &Server{ShardsFor: func(string) string { panic("shard boom") }}
	resp := serveOne(t, s,
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"source_search","arguments":{"pattern":"x","project":"p"}}}`)
	errObj, ok := resp["error"].(map[string]any)
	if !ok {
		t.Fatalf("expected contained panic error, got %v", resp)
	}
	if errObj["code"].(float64) != -32000 {
		t.Fatalf("code = %v", errObj["code"])
	}
	if !strings.Contains(errObj["message"].(string), "shard boom") {
		t.Fatalf("message = %q", errObj["message"])
	}
}
