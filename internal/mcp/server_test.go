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
	resp := serveOne(t, &Server{}, `{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`)
	result := resp["result"].(map[string]any)
	tools := result["tools"].([]any)
	if len(tools) != 11 {
		t.Fatalf("tools = %d, want 11", len(tools))
	}
	names := map[string]bool{}
	for _, tool := range tools {
		names[tool.(map[string]any)["name"].(string)] = true
	}
	for _, want := range []string{"search_graph", "source_search", "get_file_outline", "detect_changes", "check_index_coverage"} {
		if !names[want] {
			t.Fatalf("missing tool %q", want)
		}
	}
}

func TestParseError(t *testing.T) {
	resp := serveOne(t, &Server{}, `{oops`)
	if resp["error"] == nil {
		t.Fatal("expected parse error")
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
