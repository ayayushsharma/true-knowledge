package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/ayayushsharma/true-knowledge/internal/cbmexec"
	"github.com/ayayushsharma/true-knowledge/internal/memory"
	"github.com/ayayushsharma/true-knowledge/internal/zoekttext"
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

func responseText(t *testing.T, resp map[string]any) string {
	t.Helper()
	result, ok := resp["result"].(map[string]any)
	if !ok {
		t.Fatalf("expected result, got %v", resp)
	}
	return result["content"].([]any)[0].(map[string]any)["text"].(string)
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
		{"analysis", 15, []string{"validate", "query_graph", "manage_adr", "get_graph_schema"}, nil},
		{"minimal", 3, []string{"check_index_coverage", "search_graph", "get_code_snippet"}, []string{"source_search", "detect_changes"}},
		{"memory", 22, []string{"mem_save", "mem_recall", "mem_review", "note_save", "note_search", "note_toc", "note_reindex", "note_review", "ledger_update", "ledger_get", "ledger_history"}, []string{"validate"}},
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
	// data is the structuredContent the fake claims to have. Empty means
	// "this engine predates format:json", which is the fallback path.
	data string
}

func (r *fakeRunner) RunJSON(ctx context.Context, tool string, payload map[string]any) (string, error) {
	return "mock-out", nil
}

func (r *fakeRunner) RunStructured(ctx context.Context, tool string, payload map[string]any) (cbmexec.Result, error) {
	if r.data == "" {
		return cbmexec.Result{Text: "mock-out"}, nil
	}
	return cbmexec.Result{Text: "mock-out", Data: json.RawMessage(r.data)}, nil
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

// TestToolsListSchemad verifies every advertised tool carries the schema MCP
// requires (inputSchema = object with properties/required).
func TestToolsListSchemas(t *testing.T) {
	for _, tc := range []struct {
		profile string
		want    int
	}{
		{"", 11},
		{"analysis", 15},
		{"minimal", 3},
		{"memory", 22},
	} {
		resp := serveOne(t, &Server{Profile: tc.profile}, `{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`)
		tools := resp["result"].(map[string]any)["tools"].([]any)
		if len(tools) != tc.want {
			t.Fatalf("[%q] tools = %d, want %d", tc.profile, len(tools), tc.want)
		}
		for _, tool := range tools {
			td := tool.(map[string]any)
			name, _ := td["name"].(string)
			schema, ok := td["inputSchema"].(map[string]any)
			if !ok {
				t.Fatalf("[%q] %s: missing inputSchema", tc.profile, name)
			}
			if ty, _ := schema["type"].(string); ty != "object" {
				t.Fatalf("[%q] %s: inputSchema.type = %q, want object", tc.profile, name, ty)
			}
			if _, ok := schema["properties"].(map[string]any); !ok {
				t.Fatalf("[%q] %s: inputSchema.properties missing", tc.profile, name)
			}
		}
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

// RunStructured records the call the same way, and reports no payload: the
// spy stands in for an engine that predates format:"json", so the tests that
// assert on the text block keep exercising the fallback.
func (r *spyRunner) RunStructured(ctx context.Context, tool string, payload map[string]any) (cbmexec.Result, error) {
	r.tool = tool
	r.payload = payload
	return cbmexec.Result{Text: "mock-out"}, nil
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
	if err := zoekttext.IndexDir(context.Background(), shards, dir, "p", nil); err != nil {
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

// openTestMemStore wires a real memory.Store on a temp TK_HOME tree.
func openTestMemStore(t *testing.T) *memory.Store {
	t.Helper()
	root := filepath.Join(t.TempDir(), "data")
	facts, err := memory.OpenFacts(context.Background(), filepath.Join(root, "mem"))
	if err != nil {
		t.Fatal(err)
	}
	notes, err := memory.OpenNotes(context.Background(), filepath.Join(root, "notes"))
	if err != nil {
		t.Fatal(err)
	}
	ldg, err := memory.OpenLedger(filepath.Join(root, "ledger"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = notes.Close() })
	return &memory.Store{Facts: facts, Notes: notes, Ledger: ldg, LedgerEnabled: true, LedgerBudget: 1500, NotesTocBudget: 700}
}

// TestMemoryToolsWorkWithoutCBM: memory-profile tools must succeed even when
// the graph backend is absent.
func TestMemoryToolsWorkWithoutCBM(t *testing.T) {
	s := &Server{Profile: ProfileMemory, Budget: 6000, Mem: openTestMemStore(t)}

	resp := serveOne(t, s, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"mem_save","arguments":{"topic":"db","value":"postgres","scope":"project","project":"demo","provenance":"e2e"}}}`)
	if text := responseText(t, resp); !strings.Contains(text, "saved fact") {
		t.Fatalf("mem_save = %q", text)
	}
	resp = serveOne(t, s, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"mem_recall","arguments":{"topic":"db","project":"demo"}}}`)
	if text := responseText(t, resp); !strings.Contains(text, "postgres") {
		t.Fatalf("mem_recall = %q", text)
	}
	resp = serveOne(t, s, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"note_save","arguments":{"title":"tls","text":"certs rotate monthly","project":"demo"}}}`)
	if text := responseText(t, resp); !strings.Contains(text, "for review") {
		t.Fatalf("note_save = %q", text)
	}
	resp = serveOne(t, s, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"note_review","arguments":{"action":"list"}}}`)
	if text := responseText(t, resp); !strings.Contains(text, "tls") {
		t.Fatalf("note_review list = %q", text)
	}
	id := ""
	for _, line := range strings.Split(responseText(t, resp), "\n") {
		if strings.Contains(line, "tls") {
			id = strings.Fields(line)[0]
		}
	}
	if id == "" {
		t.Fatal("no note review id")
	}
	resp = serveOne(t, s, `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"note_review","arguments":{"action":"approve","id":"`+id+`"}}}`)
	if text := responseText(t, resp); !strings.Contains(text, "approved note") {
		t.Fatalf("note_review approve = %q", text)
	}
	resp = serveOne(t, s, `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"note_search","arguments":{"query":"certs","project":"demo"}}}`)
	if text := responseText(t, resp); !strings.Contains(text, "certs rotate monthly") {
		t.Fatalf("note_search = %q", text)
	}
	resp = serveOne(t, s, `{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"ledger_update","arguments":{"project":"demo","key":"goal","text":"demo serves the API"}}}`)
	if text := responseText(t, resp); !strings.Contains(text, "appended ledger demo/goal") {
		t.Fatalf("ledger_update = %q", text)
	}
	resp = serveOne(t, s, `{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"ledger_update","arguments":{"project":"demo","key":"goal","text":"demo serves the API and owns tls"}}}`)
	resp = serveOne(t, s, `{"jsonrpc":"2.0","id":6,"method":"tools/call","params":{"name":"ledger_get","arguments":{"project":"demo"}}}`)
	if text := responseText(t, resp); !strings.Contains(text, "demo serves the API and owns tls") {
		t.Fatalf("ledger_get winners = %q", text)
	}
	resp = serveOne(t, s, `{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"ledger_history","arguments":{"project":"demo"}}}`)
	text := responseText(t, resp)
	// history is complete: the overwritten winner's line is still its own
	// entry (ends at EOL) and the newer goal entry is present alongside it.
	if !strings.Contains(text, "goal  demo serves the API and owns tls") || !strings.Contains(text, "goal  demo serves the API\n") {
		t.Fatalf("ledger_history must contain every entry: %q", text)
	}
	resp = serveOne(t, s, `{"jsonrpc":"2.0","id":8,"method":"tools/call","params":{"name":"ledger_update","arguments":{"project":"demo","key":"bogus","text":"x"}}}`)
	if resp["error"] == nil {
		t.Fatalf("unknown key must be rejected")
	}
}

// TestLedgerEnabledGateMatchesCLI: the MCP write path must honor
// ledger.enabled exactly like `tk ledger update` — writes fail, reads stay
// open.
func TestLedgerEnabledGateMatchesCLI(t *testing.T) {
	s := &Server{Profile: ProfileMemory, Budget: 6000, Mem: openTestMemStore(t)}
	s.Mem.LedgerEnabled = false
	resp := serveOne(t, s, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"ledger_update","arguments":{"project":"demo","key":"goal","text":"must be rejected"}}}`)
	errObj, ok := resp["error"].(map[string]any)
	if !ok {
		t.Fatalf("expected gate error, got %v", resp)
	}
	if msg := errObj["message"].(string); msg != memory.ErrLedgerDisabled.Error() {
		t.Fatalf("message = %q, want %q", msg, memory.ErrLedgerDisabled)
	}
	resp = serveOne(t, s, `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"ledger_get","arguments":{"project":"demo"}}}`)
	if text := responseText(t, resp); !strings.Contains(text, "is empty") {
		t.Fatalf("ledger_get must stay open when disabled: %q", text)
	}
}

// TestMemorySecretsMasked: secret-looking saved values must be masked, not
// echoed, in memory tool output.
func TestMemorySecretsMasked(t *testing.T) {
	s := &Server{Profile: ProfileMemory, Budget: 6000, Mem: openTestMemStore(t)}
	resp := serveOne(t, s, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"mem_save","arguments":{"topic":"keys","value":"sk-abcdefghijklmnopqrstuvwxyz123456","scope":"project","project":"demo"}}}`)
	text := responseText(t, resp)
	if strings.Contains(text, "sk-abcdefghijklmnopqrstuvwxyz123456") {
		t.Fatalf("secret leaked into output: %q", text)
	}
	resp = serveOne(t, s, `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"mem_review","arguments":{"action":"list"}}}`)
	text = responseText(t, resp)
	if strings.Contains(text, "sk-abcdefghijklmnopqrstuvwxyz123456") {
		t.Fatalf("secret leaked into review list: %q", text)
	}
}

// TestMemoryProfileGateKept: memory tools are not exposed outside the profile.
func TestMemoryProfileGateKept(t *testing.T) {
	resp := serveOne(t, &Server{Profile: ProfileScout, Mem: openTestMemStore(t)},
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"mem_save","arguments":{}}}`)
	errObj := resp["error"].(map[string]any)
	if errObj["code"].(float64) != -32601 {
		t.Fatalf("code = %v", errObj["code"])
	}
}

// TestSourceSearchHiddenInMinimal: profile enforcement is the first gate, so
// source_search — special-cased in dispatch — is still unknown to minimal.
func TestSourceSearchHiddenInMinimal(t *testing.T) {
	s := &Server{Profile: ProfileMinimal, ShardsFor: func(string) string { return t.TempDir() }}
	resp := serveOne(t, s, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"source_search","arguments":{"pattern":"x","project":"p"}}}`)
	errObj, ok := resp["error"].(map[string]any)
	if !ok {
		t.Fatalf("source_search must be hidden in minimal, got %v", resp)
	}
	if errObj["code"].(float64) != -32601 {
		t.Fatalf("code = %v", errObj["code"])
	}
}

// TestMCPLogSecretRedaction: string params that look secret are whole-masked
// in tk.log records; output/error get the span mask. The raw value never
// lands on disk even though mem_save routes it to review.
func TestMCPLogSecretRedaction(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "tk.log")
	secret := "API_SECRET_KEY = superSekritValue123456789"
	s := &Server{Profile: ProfileMemory, LogPath: logPath, Mem: openTestMemStore(t)}
	req := fmt.Sprintf(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"mem_save","arguments":{"topic":"t","scope":"global","value":%q}}}`, secret)
	resp := serveOne(t, s, req)
	if resp["error"] != nil {
		t.Fatalf("mem_save failed: %v", resp)
	}
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "superSekritValue123456789") {
		t.Fatalf("secret leaked into MCP log: %s", data)
	}
	if !strings.Contains(string(data), "[REDACTED]") {
		t.Fatalf("expected a redaction marker in the MCP log: %s", data)
	}
}

// TestSourceSearchHooks: EnsureIndex runs before the search, live snippets
// come from the worktree, and a Staleness note is prepended.
func TestSourceSearchHooks(t *testing.T) {
	dir, shards := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "w.go"), []byte("func Widget() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := zoekttext.IndexDir(context.Background(), shards, dir, "p", nil); err != nil {
		t.Fatal(err)
	}
	// Dirty the worktree after indexing: shards hold the stale line.
	if err := os.WriteFile(filepath.Join(dir, "w.go"), []byte("func Widget() {}  // edited\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	indexed := 0
	s := &Server{
		Budget:      6000,
		ShardsFor:   func(string) string { return shards },
		EnsureIndex: func(context.Context, string) error { indexed++; return nil },
		Staleness:   func(string, bool) string { return "[source-search: 1 modified, 0 untracked in worktree not indexed]\n" },
		ProjectRoot: func(string) string { return dir },
	}
	resp := serveOne(t, s, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"source_search","arguments":{"pattern":"Widget","project":"p"}}}`)
	text := responseText(t, resp)
	if indexed != 1 {
		t.Fatalf("EnsureIndex ran %d times, want 1", indexed)
	}
	if !strings.Contains(text, "// edited") {
		t.Fatalf("live snippet missing, got %q", text)
	}
	if !strings.Contains(text, "[source-search: 1 modified") {
		t.Fatalf("staleness note missing, got %q", text)
	}
}

// TestSourceSearchRefreshFailOpen: a failed refresh still searches the
// shards it has, annotated as stale — it never blocks the tool.
func TestSourceSearchRefreshFailOpen(t *testing.T) {
	dir, shards := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "w.go"), []byte("func Alpha() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := zoekttext.IndexDir(context.Background(), shards, dir, "p", nil); err != nil {
		t.Fatal(err)
	}
	s := &Server{
		Budget:      6000,
		ShardsFor:   func(string) string { return shards },
		EnsureIndex: func(context.Context, string) error { return errors.New("reindex boom") },
		ProjectRoot: func(string) string { return dir },
	}
	resp := serveOne(t, s, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"source_search","arguments":{"pattern":"Alpha","project":"p"}}}`)
	text := responseText(t, resp)
	if !strings.Contains(text, "index refresh failed: reindex boom") {
		t.Fatalf("fail-open note missing, got %q", text)
	}
	if !strings.Contains(text, "Alpha") {
		t.Fatalf("search should still run on stale shards, got %q", text)
	}
}

// seqRunner answers per-tool with canned output/error and records call order.
type seqRunner struct {
	outs  map[string]string
	errs  map[string]error
	order []string
}

func (r *seqRunner) RunJSON(_ context.Context, tool string, _ map[string]any) (string, error) {
	r.order = append(r.order, tool)
	if err := r.errs[tool]; err != nil {
		return "", err
	}
	return r.outs[tool], nil
}

// RunStructured replays the same sequence and reports no payload, so these
// tests keep covering the text path on an engine without format support.
func (r *seqRunner) RunStructured(ctx context.Context, tool string, payload map[string]any) (cbmexec.Result, error) {
	out, err := r.RunJSON(ctx, tool, payload)
	return cbmexec.Result{Text: out}, err
}

const cleanCoverage = "generation_matches: true\nhash_records_complete: true\nrecording_status: complete\n"

// TestEmptySearchAnnotatedClean: an empty search_graph result must trigger a
// whole-project coverage probe and carry the clean verdict — never bare absence.
func TestEmptySearchAnnotatedClean(t *testing.T) {
	run := &seqRunner{outs: map[string]string{
		"search_graph":         "",
		"check_index_coverage": cleanCoverage,
	}}
	resp := serveOne(t, &Server{Profile: ProfileScout, Budget: 1024, Run: run},
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"search_graph","arguments":{"name_pattern":"Nope","project":"p"}}}`)
	if resp["error"] != nil {
		t.Fatalf("search failed: %v", resp)
	}
	if len(run.order) != 2 || run.order[1] != "check_index_coverage" {
		t.Fatalf("call order = %v, want [search_graph check_index_coverage]", run.order)
	}
	text := responseText(t, resp)
	if !strings.Contains(text, "(coverage: clean") {
		t.Fatalf("clean verdict missing, got %q", text)
	}
}

// TestEmptySearchAnnotatedGap: a coverage gap marks absence unverified (same
// suffix string as the CLI).
func TestEmptySearchAnnotatedGap(t *testing.T) {
	run := &seqRunner{outs: map[string]string{
		"search_graph":         "",
		"check_index_coverage": "generation_matches: false\n",
	}}
	resp := serveOne(t, &Server{Profile: ProfileScout, Budget: 1024, Run: run},
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"search_graph","arguments":{"name_pattern":"Nope","project":"p"}}}`)
	if resp["error"] != nil {
		t.Fatalf("search failed: %v", resp)
	}
	text := responseText(t, resp)
	if !strings.Contains(text, "; absence unverified)") {
		t.Fatalf("gap suffix missing, got %q", text)
	}
}

// TestEmptySearchCoverageFailureHardError: probe failure on empty results is a
// hard error — never silent absence (CLI doctrine mirrored).
func TestEmptySearchCoverageFailureHardError(t *testing.T) {
	run := &seqRunner{outs: map[string]string{"search_graph": ""},
		errs: map[string]error{"check_index_coverage": errors.New("cbm down")}}
	resp := serveOne(t, &Server{Profile: ProfileScout, Budget: 1024, Run: run},
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"search_graph","arguments":{"name_pattern":"Nope","project":"p"}}}`)
	errObj, ok := resp["error"].(map[string]any)
	if !ok {
		t.Fatalf("expected hard error, got %v", resp)
	}
	if errObj["code"].(float64) != -32000 {
		t.Fatalf("code = %v", errObj["code"])
	}
	if !strings.Contains(errObj["message"].(string), "absence unverified") {
		t.Fatalf("message = %q", errObj["message"])
	}
}

// TestNonEmptySearchSkipsProbe: results mean no probe and no annotation.
func TestNonEmptySearchSkipsProbe(t *testing.T) {
	run := &seqRunner{outs: map[string]string{"search_graph": "mock-out"}}
	resp := serveOne(t, &Server{Profile: ProfileScout, Budget: 1024, Run: run},
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"search_graph","arguments":{"name_pattern":"Demo","project":"p"}}}`)
	if len(run.order) != 1 || run.order[0] != "search_graph" {
		t.Fatalf("call order = %v, want [search_graph]", run.order)
	}
	if text := responseText(t, resp); text != "mock-out" {
		t.Fatalf("text = %q, want unannotated mock-out", text)
	}
}

// TestSearchCodeEmptyAnnotated: grep's search_code gets the same treatment.
func TestSearchCodeEmptyAnnotated(t *testing.T) {
	run := &seqRunner{outs: map[string]string{
		"search_code":          "",
		"check_index_coverage": cleanCoverage,
	}}
	resp := serveOne(t, &Server{Profile: ProfileScout, Budget: 1024, Run: run},
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"search_code","arguments":{"pattern":"zzz","project":"p"}}}`)
	if resp["error"] != nil {
		t.Fatalf("search failed: %v", resp)
	}
	if len(run.order) != 2 || run.order[1] != "check_index_coverage" {
		t.Fatalf("call order = %v, want [search_code check_index_coverage]", run.order)
	}
	if text := responseText(t, resp); !strings.Contains(text, "(coverage: clean") {
		t.Fatalf("verdict missing, got %q", text)
	}
}

// TestEmptyTraceAnnotatedClean: an empty trace_path is the negative claim
// "nothing calls this" (usually a name-resolution miss), so it earns the
// same coverage proof as the search tools.
func TestEmptyTraceAnnotatedClean(t *testing.T) {
	run := &seqRunner{outs: map[string]string{
		"trace_path":           "",
		"check_index_coverage": cleanCoverage,
	}}
	resp := serveOne(t, &Server{Profile: ProfileScout, Budget: 1024, Run: run},
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"trace_path","arguments":{"function_name":"Nope","project":"p"}}}`)
	if resp["error"] != nil {
		t.Fatalf("trace failed: %v", resp)
	}
	if len(run.order) != 2 || run.order[1] != "check_index_coverage" {
		t.Fatalf("call order = %v, want [trace_path check_index_coverage]", run.order)
	}
	if text := responseText(t, resp); !strings.Contains(text, "(coverage: clean") {
		t.Fatalf("verdict missing, got %q", text)
	}
}

// TestEmptyTraceAnnotatedGap + probe failure: the traversal path keeps the
// same gap wording and hard-error rule as the search tools.
func TestEmptyTraceAnnotatedGap(t *testing.T) {
	run := &seqRunner{outs: map[string]string{
		"trace_path":           "",
		"check_index_coverage": "generation_matches: false\n",
	}}
	resp := serveOne(t, &Server{Profile: ProfileScout, Budget: 1024, Run: run},
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"trace_path","arguments":{"function_name":"Nope","project":"p"}}}`)
	if resp["error"] != nil {
		t.Fatalf("trace failed: %v", resp)
	}
	if text := responseText(t, resp); !strings.Contains(text, "; absence unverified)") {
		t.Fatalf("gap suffix missing, got %q", text)
	}
}

func TestEmptyTraceCoverageFailureHardError(t *testing.T) {
	run := &seqRunner{outs: map[string]string{"trace_path": ""},
		errs: map[string]error{"check_index_coverage": errors.New("cbm down")}}
	resp := serveOne(t, &Server{Profile: ProfileScout, Budget: 1024, Run: run},
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"trace_path","arguments":{"function_name":"Nope","project":"p"}}}`)
	errObj, ok := resp["error"].(map[string]any)
	if !ok {
		t.Fatalf("expected hard error, got %v", resp)
	}
	if errObj["code"].(float64) != -32000 || !strings.Contains(errObj["message"].(string), "absence unverified") {
		t.Fatalf("error = %v", errObj)
	}
}

// TestNonEmptyTraceSkipsProbe: a real traversal needs no probe and stays bare.
func TestNonEmptyTraceSkipsProbe(t *testing.T) {
	run := &seqRunner{outs: map[string]string{"trace_path": "mock-trace"}}
	resp := serveOne(t, &Server{Profile: ProfileScout, Budget: 1024, Run: run},
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"trace_path","arguments":{"function_name":"Demo","project":"p"}}}`)
	if len(run.order) != 1 || run.order[0] != "trace_path" {
		t.Fatalf("call order = %v, want [trace_path]", run.order)
	}
	if text := responseText(t, resp); text != "mock-trace" {
		t.Fatalf("text = %q, want unannotated mock-trace", text)
	}
}

// TestInventoryEmptyNotAnnotated: list_projects is not an absence-capable
// search tool — empty means no probe.
func TestInventoryEmptyNotAnnotated(t *testing.T) {
	run := &seqRunner{outs: map[string]string{"list_projects": ""}}
	resp := serveOne(t, &Server{Profile: ProfileScout, Budget: 1024, Run: run},
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"list_projects","arguments":{}}}`)
	if resp["error"] != nil {
		t.Fatalf("list_projects failed: %v", resp)
	}
	if len(run.order) != 1 || run.order[0] != "list_projects" {
		t.Fatalf("call order = %v, want [list_projects]", run.order)
	}
}

// TestEmptySearchNoProjectSkipsProbe: without a project there is nothing to
// probe — the empty result passes through untouched.
func TestEmptySearchNoProjectSkipsProbe(t *testing.T) {
	run := &seqRunner{outs: map[string]string{"search_graph": ""}}
	serveOne(t, &Server{Profile: ProfileScout, Budget: 1024, Run: run},
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"search_graph","arguments":{"name_pattern":"Nope"}}}`)
	if len(run.order) != 1 || run.order[0] != "search_graph" {
		t.Fatalf("call order = %v, want [search_graph]", run.order)
	}
}

// ---------------------------------------------------------------------------
// Structured tool results
// ---------------------------------------------------------------------------

// oneSession drives a full handshake + one call through the real loop, so the
// negotiated revision and the reply are produced by the same code path a
// client sees rather than by a struct literal set up by the test.
func oneSession(t *testing.T, s *Server, initVer, call string) map[string]any {
	t.Helper()
	var out bytes.Buffer
	s.In = strings.NewReader(
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"` + initVer + `"}}` + "\n" +
			call + "\n")
	s.OutW = &out
	if code := s.Serve(context.Background()); code != 0 {
		t.Fatalf("serve exit = %d", code)
	}
	var lines []map[string]any
	dec := json.NewDecoder(bytes.NewReader(out.Bytes()))
	for dec.More() {
		var m map[string]any
		if err := dec.Decode(&m); err != nil {
			t.Fatalf("bad stream %q: %v", out.String(), err)
		}
		lines = append(lines, m)
	}
	if len(lines) != 2 {
		t.Fatalf("want initialize + call, got %d replies: %s", len(lines), out.String())
	}
	return lines[1]
}

const hitSearchPayload = `{"cols":["name","label"],"groups":[{"qn_prefix":"demo","rows":[["Level2","Function"]]}],"total":1,"returned":1,"count":1,"has_more":false,"truncated":false}`

// TestStructuredContentOnModernClient: a 2025-06-18 client gets the payload
// as a field and the same bytes in the text block.
func TestStructuredContentOnModernClient(t *testing.T) {
	resp := oneSession(t, &Server{Profile: ProfileScout, Budget: 4096, Run: &fakeRunner{data: hitSearchPayload}},
		"2025-06-18",
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"search_graph","arguments":{"name_pattern":"Level2","project":"p"}}}`)
	result, ok := resp["result"].(map[string]any)
	if !ok {
		t.Fatalf("no result: %v", resp)
	}
	sc, ok := result["structuredContent"].(map[string]any)
	if !ok {
		t.Fatalf("structuredContent missing on a 2025-06-18 session: %v", result)
	}
	if sc["total"] != float64(1) {
		t.Errorf("structuredContent lost the payload: %v", sc)
	}
	text := responseText(t, resp)
	if !strings.Contains(text, `"total":1`) {
		t.Errorf("text block is not the same answer: %q", text)
	}
	if strings.Contains(text, "\n  ") {
		t.Errorf("text block must be compacted for a client that only reads it: %q", text)
	}
}

// TestNoStructuredContentOnOldClient: the field arrived in 2025-06-18, so a
// 2024-11-05 client is served the same answer without it rather than with an
// unknown member it would have to guess at.
func TestNoStructuredContentOnOldClient(t *testing.T) {
	resp := oneSession(t, &Server{Profile: ProfileScout, Budget: 4096, Run: &fakeRunner{data: hitSearchPayload}},
		"2024-11-05",
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"search_graph","arguments":{"name_pattern":"Level2","project":"p"}}}`)
	result := resp["result"].(map[string]any)
	if _, present := result["structuredContent"]; present {
		t.Errorf("structuredContent sent to a 2024-11-05 client: %v", result)
	}
	if !strings.Contains(responseText(t, resp), `"total":1`) {
		t.Error("an old client must still get the payload, just not as a field")
	}
}

// TestNoStructuredContentBeforeInitialize: a client that calls a tool without
// a handshake is served the oldest dialect.
func TestNoStructuredContentBeforeInitialize(t *testing.T) {
	resp := serveOne(t, &Server{Profile: ProfileScout, Budget: 4096, Run: &fakeRunner{data: hitSearchPayload}},
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"search_graph","arguments":{"name_pattern":"Level2","project":"p"}}}`)
	if _, present := resp["result"].(map[string]any)["structuredContent"]; present {
		t.Error("structuredContent sent before a handshake negotiated it")
	}
}

// TestNegotiateProtocol pins the handshake table.
func TestNegotiateProtocol(t *testing.T) {
	cases := map[string]string{
		"2025-06-18":    "2025-06-18",
		"2025-03-26":    "2025-03-26",
		"2024-11-05":    "2024-11-05",
		"1999-01-01":    "2025-06-18", // older than tk knows: answer with tk's newest
		"2099-12-31":    "2025-06-18", // newer than tk knows: still a session
		"":              "2025-06-18",
		"not-a-version": "2025-06-18",
	}
	for req, want := range cases {
		if got := negotiateProtocol(req); got != want {
			t.Errorf("negotiateProtocol(%q) = %q, want %q", req, got, want)
		}
	}
}

// TestStructuredForVersion: the cut-off is the revision that introduced the
// field, and it stays that way if a newer revision is ever added.
func TestStructuredForVersion(t *testing.T) {
	if !structuredFor("2025-06-18") {
		t.Error("2025-06-18 reads structuredContent")
	}
	for _, v := range []string{"2025-03-26", "2024-11-05", "", "bogus"} {
		if structuredFor(v) {
			t.Errorf("%q must not read structuredContent", v)
		}
	}
}

// TestInitializeEchoesNegotiatedVersion: the client asked for a revision, so
// the reply names it rather than a hardcoded one.
func TestInitializeEchoesNegotiatedVersion(t *testing.T) {
	for _, v := range []string{"2025-06-18", "2025-03-26", "2024-11-05"} {
		resp := serveOne(t, &Server{Profile: ProfileScout, Run: &fakeRunner{}},
			`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"`+v+`"}}`)
		got := resp["result"].(map[string]any)["protocolVersion"]
		if got != v {
			t.Errorf("initialize(%s) answered %v", v, got)
		}
	}
}

// TestOutputSchemaNotDeclared: tk does not declare outputSchema, because the
// payload is the engine's schema and CBM owns it. A declared schema tk does
// not enforce would be a promise it cannot keep.
func TestOutputSchemaNotDeclared(t *testing.T) {
	resp := serveOne(t, &Server{Profile: ProfileAnalysis, Run: &fakeRunner{}},
		`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	for _, raw := range resp["result"].(map[string]any)["tools"].([]any) {
		tool := raw.(map[string]any)
		if _, present := tool["outputSchema"]; present {
			t.Errorf("%v declares an outputSchema", tool["name"])
		}
	}
}

// TestStructuredAbsenceCarriesCoverage: an empty structured search is a claim
// about absence, so it ships the coverage that backs it. The evidence is a
// sibling of content, never a member of the engine's payload.
func TestStructuredAbsenceCarriesCoverage(t *testing.T) {
	resp := oneSession(t, &Server{Profile: ProfileScout, Budget: 4096, Run: &fakeRunner{data: `{"cols":["name"],"groups":[],"total":0,"returned":0,"count":0,"has_more":false,"truncated":false}`}},
		"2025-06-18",
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"search_graph","arguments":{"name_pattern":"Nope","project":"p"}}}`)
	result := resp["result"].(map[string]any)
	if _, present := result["coverage"]; !present {
		t.Errorf("an empty structured search was reported without coverage evidence: %v", result)
	}
	sc := result["structuredContent"].(map[string]any)
	if _, polluted := sc["coverage"]; polluted {
		t.Errorf("the engine payload must be passed through untouched: %v", sc)
	}
}

// TestManageADRModesMatchEngine: the schema must not offer a mode the engine
// rejects. `list` and `delete` were advertised and are not modes.
func TestManageADRModesMatchEngine(t *testing.T) {
	resp := serveOne(t, &Server{Profile: ProfileAnalysis, Run: &fakeRunner{}},
		`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	for _, raw := range resp["result"].(map[string]any)["tools"].([]any) {
		tool := raw.(map[string]any)
		if tool["name"] != "manage_adr" {
			continue
		}
		props := tool["inputSchema"].(map[string]any)["properties"].(map[string]any)
		var got []string
		for _, v := range props["mode"].(map[string]any)["enum"].([]any) {
			got = append(got, v.(string))
		}
		want := []string{"outline", "get", "sections", "set_sections", "update"}
		if len(got) != len(want) {
			t.Fatalf("manage_adr modes = %q, want %q", got, want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("manage_adr modes = %q, want %q", got, want)
			}
		}
		return
	}
	t.Fatal("manage_adr not in the analysis profile")
}

// Cancellation and notifications
// ---------------------------------------------------------------------------

// TestServeEOFExitsZero: stdin close is still the instant clean exit AGENTS.md
// 6 promises, and it stays distinguishable from a signal shutdown.
func TestServeEOFExitsZero(t *testing.T) {
	var out bytes.Buffer
	s := &Server{In: strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"ping"}` + "\n"), OutW: &out}
	if code := s.Serve(context.Background()); code != 0 {
		t.Fatalf("EOF exit = %d, want 0", code)
	}
	if !strings.Contains(out.String(), `"result"`) {
		t.Fatalf("ping unanswered: %q", out.String())
	}
}

// TestServeCancelExits: a client that keeps the pipe open and then goes away
// (Ctrl-C, SIGTERM, parent killed) must not leave tk running. Before this,
// Serve only learned about EOF, so a cancelled context ended nothing: the
// scanner sat in a blocking read on a pipe that would never be closed.
func TestServeCancelExits(t *testing.T) {
	pr, pw := io.Pipe() // never written to, never closed by the test
	defer pw.Close()
	var out bytes.Buffer
	s := &Server{In: pr, OutW: &out}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan int, 1)
	go func() { done <- s.Serve(ctx) }()
	cancel()
	select {
	case code := <-done:
		if code != 1 {
			t.Fatalf("cancel exit = %d, want 1", code)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Serve did not return after its context was cancelled")
	}
}

// TestServeCancelMidRequest: cancellation reaches work already in flight, not
// just the idle wait for the next line.
func TestServeCancelMidRequest(t *testing.T) {
	entered := make(chan struct{})
	s := &Server{
		Budget: 4096,
		In:     strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"search_graph","arguments":{"name_pattern":"x","project":"p"}}}` + "\n"),
		Run:    &blockingRunner{entered: entered},
	}
	ctx, cancel := context.WithCancel(context.Background())
	var out bytes.Buffer
	s.OutW = &out
	done := make(chan int, 1)
	go func() { done <- s.Serve(ctx) }()
	select {
	case <-entered:
	case <-time.After(10 * time.Second):
		t.Fatal("the graph tool call never reached the runner")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Serve did not return; the in-flight tool call ignored its context")
	}
}

// blockingRunner records that a call started, then waits for its context.
type blockingRunner struct{ entered chan struct{} }

func (b *blockingRunner) RunJSON(ctx context.Context, _ string, _ map[string]any) (string, error) {
	close(b.entered)
	<-ctx.Done()
	return "", ctx.Err()
}

func (b *blockingRunner) RunStructured(ctx context.Context, _ string, _ map[string]any) (cbmexec.Result, error) {
	close(b.entered)
	<-ctx.Done()
	return cbmexec.Result{}, ctx.Err()
}

// TestNotificationsGetNoReply: a notification carries no id, so answering it
// produces a frame the client never asked for. tk used to reply to
// notifications/initialized with a result and to any other notifications/*
// with a null-id "method not found", which a strict client rejects as a
// response to a message it marked as a notification.
func TestNotificationsGetNoReply(t *testing.T) {
	var out bytes.Buffer
	s := &Server{
		In: strings.NewReader(
			`{"jsonrpc":"2.0","method":"notifications/initialized"}` + "\n" +
				`{"jsonrpc":"2.0","method":"notifications/cancelled","params":{"requestId":1}}` + "\n" +
				`{"jsonrpc":"2.0","method":"notifications/somethingUnknown"}` + "\n" +
				`{"jsonrpc":"2.0","id":7,"method":"ping"}` + "\n"),
		OutW: &out,
	}
	if code := s.Serve(context.Background()); code != 0 {
		t.Fatalf("serve exit = %d", code)
	}
	dec := json.NewDecoder(bytes.NewReader(out.Bytes()))
	var frames []map[string]any
	for dec.More() {
		var m map[string]any
		if err := dec.Decode(&m); err != nil {
			t.Fatalf("bad stream %q: %v", out.String(), err)
		}
		frames = append(frames, m)
	}
	if len(frames) != 1 {
		t.Fatalf("want exactly one reply to the ping, got %d: %s", len(frames), out.String())
	}
	if id, _ := frames[0]["id"].(float64); id != 7 {
		t.Fatalf("reply id = %v, want 7", frames[0]["id"])
	}
}

// TestRequestContextReachesHooks: the context a hook receives is the request's,
// so cancelling a request unblocks the refresh it started.
func TestRequestContextReachesHooks(t *testing.T) {
	dir, shards := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "w.go"), []byte("func Gamma() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := zoekttext.IndexDir(context.Background(), shards, dir, "p", nil); err != nil {
		t.Fatal(err)
	}
	// Inspected inside the hook: handle cancels the request context on return,
	// so anything sampled afterwards is dead by construction.
	var ran, hasDeadline bool
	var errAtEntry error
	s := &Server{
		Budget:    6000,
		ShardsFor: func(string) string { return shards },
		EnsureIndex: func(c context.Context, _ string) error {
			ran = true
			errAtEntry = c.Err()
			_, hasDeadline = c.Deadline()
			return nil
		},
		ProjectRoot: func(string) string { return dir },
	}
	serveOne(t, s, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"source_search","arguments":{"pattern":"Gamma","project":"p"}}}`)
	if !ran {
		t.Fatal("EnsureIndex never ran")
	}
	if errAtEntry != nil {
		t.Errorf("hook context already dead on entry: %v", errAtEntry)
	}
	if !hasDeadline {
		t.Error("hook context has no deadline; a wedged index refresh would hold the loop open")
	}
}

// indexPlain indexes a one-file plain dir under <root>/<name> and returns its
// shard dir. Mirrors tk's real layout: <cache>/zoekt/<project>/, one level
// below the cache root, NOT flat inside it.
func indexPlain(t *testing.T, root, name, body string) (dir, shards string) {
	t.Helper()
	dir, shards = t.TempDir(), filepath.Join(root, name)
	if err := os.WriteFile(filepath.Join(dir, "w.go"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := zoekttext.IndexDir(context.Background(), shards, dir, name, nil); err != nil {
		t.Fatal(err)
	}
	return dir, shards
}

// TestSourceSearchFleetSpansProjects is the test the layout makes necessary.
// tk keeps shards at <cache>/zoekt/<project>/, while a zoekt directory
// searcher globs <dir>/*.zoekt — flat, non-recursive. So a searcher opened on
// the cache root loads zero shards and answers empty with no error at all. An
// implementation that searched the parent directory would pass every
// single-project test and return "(no matches)" for every fleet query, forever.
func TestSourceSearchFleetSpansProjects(t *testing.T) {
	zoektRoot := t.TempDir()
	dirA, shardsA := indexPlain(t, zoektRoot, "alpha", "func ProcessOrder() {}\n")
	_, shardsB := indexPlain(t, zoektRoot, "beta", "func ProcessOrderLater() {}\n")

	// Precondition: the cache root itself is NOT a searchable directory.
	if res, err := zoekttext.Search(context.Background(), zoektRoot, "ProcessOrder", "", 20); err == nil {
		if len(res.Matches) != 0 {
			t.Fatalf("precondition broke: parent dir returned %d matches; the layout under test requires 0", len(res.Matches))
		}
	} else {
		t.Logf("parent dir errors outright (also acceptable for the layout): %v", err)
	}

	s := &Server{
		Budget:      6000,
		ShardsFor:   func(p string) string { return map[string]string{"alpha": shardsA, "beta": shardsB}[p] },
		Projects:    func() []string { return []string{"alpha", "beta"} },
		ProjectRoot: func(p string) string { return map[string]string{"alpha": dirA}[p] },
	}
	resp := serveOne(t, s, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"source_search","arguments":{"pattern":"ProcessOrder","all_projects":true}}}`)
	if resp["error"] != nil {
		t.Fatalf("fleet search failed: %v", resp)
	}
	text := responseText(t, resp)
	if !strings.Contains(text, "alpha:w.go") {
		t.Fatalf("hit from alpha missing — a parent-directory searcher returns nothing at all: %q", text)
	}
	if !strings.Contains(text, "beta:w.go") {
		t.Fatalf("hit from beta missing: %q", text)
	}
	if !strings.Contains(text, "2 matches") || !strings.Contains(text, "across 2 projects (alpha, beta)") {
		t.Fatalf("completeness line missing or wrong: %q", text)
	}
	// Hits are repo-prefixed under a fleet so the shape reveals the scope.
	if strings.Contains(text, "\nw.go:1:") {
		t.Fatalf("fleet hits must be repo-prefixed: %q", text)
	}
}

// TestSourceSearchFleetRefreshFailOpen: one project's refresh failure is
// annotated and named; the rest of the fleet still answers.
func TestSourceSearchFleetRefreshFailOpen(t *testing.T) {
	zoektRoot := t.TempDir()
	_, shardsA := indexPlain(t, zoektRoot, "alpha", "func ProcessOrder() {}\n")
	_, shardsB := indexPlain(t, zoektRoot, "beta", "func ProcessOrderLater() {}\n")
	s := &Server{
		Budget:    6000,
		ShardsFor: func(p string) string { return map[string]string{"alpha": shardsA, "beta": shardsB}[p] },
		Projects:  func() []string { return []string{"alpha", "beta"} },
		EnsureIndex: func(_ context.Context, p string) error {
			if p == "alpha" {
				return errors.New("reindex boom")
			}
			return nil
		},
	}
	resp := serveOne(t, s, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"source_search","arguments":{"pattern":"ProcessOrder","all_projects":true}}}`)
	text := responseText(t, resp)
	if !strings.Contains(text, "alpha: index refresh failed: reindex boom") {
		t.Fatalf("failed refresh must name its project: %q", text)
	}
	if !strings.Contains(text, "beta:w.go") {
		t.Fatalf("fleet must still answer from the projects that refreshed: %q", text)
	}
	if strings.Contains(text, "alpha:w.go") {
		t.Fatalf("a project that failed to refresh must not be reported as searched: %q", text)
	}
}

// TestSourceSearchFleetReportsNotSearched: when the limit runs out mid-walk,
// the untouched tail is named. A project never opened says nothing about its
// content, and "truncated" alone would read as "nothing else matched".
func TestSourceSearchFleetReportsNotSearched(t *testing.T) {
	zoektRoot := t.TempDir()
	_, a := indexPlain(t, zoektRoot, "alpha", "package main\nfunc ProcessOrder() {}\nfunc ProcessOrder2() {}\nfunc ProcessOrder3() {}\n")
	_, b := indexPlain(t, zoektRoot, "beta", "package main\nfunc ProcessOrder() {}\nfunc ProcessOrder2() {}\n")
	_, c := indexPlain(t, zoektRoot, "gamma", "package main\nfunc ProcessOrder() {}\n")
	s := &Server{
		Budget:    6000,
		ShardsFor: func(p string) string { return map[string]string{"alpha": a, "beta": b, "gamma": c}[p] },
		Projects:  func() []string { return []string{"alpha", "beta", "gamma"} },
	}
	resp := serveOne(t, s, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"source_search","arguments":{"pattern":"ProcessOrder","all_projects":true,"limit":1}}}`)
	text := responseText(t, resp)
	if !strings.Contains(text, "truncated") {
		t.Fatalf("truncation must be stated: %q", text)
	}
	// alpha alone filled the limit, so beta and gamma were both never opened.
	if !strings.Contains(text, "not searched: beta, gamma (limit)") {
		t.Fatalf("the whole unsearched tail must be named: %q", text)
	}
	// Totals cover searched projects only: nothing is claimed about content
	// tk never looked at.
	if !strings.Contains(text, "in 1 file in alpha") {
		t.Fatalf("counts must cover the searched project alone: %q", text)
	}
}

// TestSourceSearchScopeIsExplicit: neither route, or both, is an error. Omission
// must never widen the search by default.
func TestSourceSearchScopeIsExplicit(t *testing.T) {
	dir, shards := t.TempDir(), t.TempDir()
	if err := zoekttext.IndexDir(context.Background(), shards, dir, "p", nil); err != nil {
		t.Fatal(err)
	}
	s := &Server{Budget: 6000, ShardsFor: func(string) string { return shards }, Projects: func() []string { return []string{"p"} }}

	resp := serveOne(t, s, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"source_search","arguments":{"pattern":"x"}}}`)
	if resp["error"] == nil {
		t.Fatal("omitting both project and all_projects must be an error, not a fleet search")
	}
	if msg, _ := resp["error"].(map[string]any)["message"].(string); !strings.Contains(msg, "all_projects") {
		t.Fatalf("the error must name the fleet route: %q", msg)
	}

	resp = serveOne(t, s, `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"source_search","arguments":{"pattern":"x","project":"p","all_projects":true}}}`)
	if resp["error"] == nil {
		t.Fatal("project and all_projects together must be an error")
	}
}

// TestSourceSearchFleetEmptyRegistry: an empty registry is a scope error naming
// the fix, never an empty result that reads as an absence.
func TestSourceSearchFleetEmptyRegistry(t *testing.T) {
	s := &Server{Budget: 6000, ShardsFor: func(string) string { return t.TempDir() }, Projects: func() []string { return nil }}
	resp := serveOne(t, s, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"source_search","arguments":{"pattern":"x","all_projects":true}}}`)
	if resp["error"] == nil {
		t.Fatal("an empty registry must be an error")
	}
	if msg, _ := resp["error"].(map[string]any)["message"].(string); !strings.Contains(msg, "tk register") {
		t.Fatalf("the error must name the fix: %q", msg)
	}
}

// TestRenderMatchesGrouped: the fleet face groups by file with a per-file count
// and prefixes the repo; the single-project face stays byte-identical.
func TestRenderMatchesGrouped(t *testing.T) {
	mk := func() []zoekttext.Match {
		return []zoekttext.Match{
			{File: "a.go", Line: 1, Text: "one", Repo: "alpha"},
			{File: "a.go", Line: 9, Text: "two", Repo: "alpha"},
			{File: "b.go", Line: 3, Text: "three", Repo: "beta"},
		}
	}
	flat := RenderMatches(mk(), false)
	want := "a.go:1: one\na.go:9: two\nb.go:3: three"
	if flat != want {
		t.Fatalf("single-project face changed:\n got %q\nwant %q", flat, want)
	}
	grouped := RenderMatches(mk(), true)
	for _, w := range []string{"a.go (2 matches)", "b.go (1 match)", "alpha:a.go:1: one", "beta:b.go:3: three"} {
		if !strings.Contains(grouped, w) {
			t.Fatalf("grouped face missing %q:\n%s", w, grouped)
		}
	}
	if RenderMatches(nil, true) != "(no matches)" {
		t.Fatal("empty result must keep the (no matches) sentinel")
	}
}

// TestQueryZoektFleetOrderAndAccounting: walk order is the given order, the
// limit is fleet-wide rather than per project, and the untouched tail is named.
func TestQueryZoektFleetOrderAndAccounting(t *testing.T) {
	root := t.TempDir()
	_, a := indexPlain(t, root, "alpha", "package main\nfunc Needle() {}\nfunc Needle2() {}\n")
	_, b := indexPlain(t, root, "beta", "package main\nfunc Needle() {}\n")
	members := []FleetMember{{Name: "alpha", Shards: a}, {Name: "beta", Shards: b}}

	got, err := QueryZoektFleet(context.Background(), members, "Needle", "", 1)
	if err != nil {
		t.Fatal(err)
	}
	if got.MatchesShown != 1 {
		t.Fatalf("limit is fleet-wide: shown=%d want 1", got.MatchesShown)
	}
	if len(got.NotSearched) != 1 || got.NotSearched[0] != "beta" {
		t.Fatalf("unsearched tail wrong: %v", got.NotSearched)
	}
	if got.MatchesTotal <= got.MatchesShown {
		t.Fatalf("total must exceed shown even when truncated: total=%d shown=%d", got.MatchesTotal, got.MatchesShown)
	}
	if !strings.Contains(got.Text, "alpha:") {
		t.Fatalf("first member must own the returned hit: %q", got.Text)
	}

	// Members must be joined with a newline. RenderMatches trims its own
	// trailing newline, so plain concatenation runs the last hit of one
	// project into the first header of the next — which looks like one file
	// with a garbled line and passes any assertion that only counts prefixes.
	if i := strings.Index(full0Text(t, members), "alpha:"); i >= 0 {
		body := full0Text(t, members)[i:]
		if strings.Contains(body, "file(svc.go (") || strings.Contains(body, "second svc.go") {
			t.Fatalf("members were concatenated without a newline: %q", body)
		}
	}

	full, err := QueryZoektFleet(context.Background(), members, "Needle", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(full.NotSearched) != 0 {
		t.Fatalf("limit=0 must search everything: %v", full.NotSearched)
	}
	if full.Truncated {
		t.Fatal("limit=0 over a small fleet is not truncated")
	}
	ai := strings.Index(full.Text, "alpha:")
	bi := strings.Index(full.Text, "beta:")
	if ai < 0 || bi < 0 || ai > bi {
		t.Fatalf("walk order must follow the member list, got alpha@%d beta@%d", ai, bi)
	}
}

// TestCompletenessLineShapes pins the wording a small model has to act on.
func TestCompletenessLineShapes(t *testing.T) {
	full := FleetResult{Searched: []string{"demo"}, MatchesTotal: 20, FilesTotal: 1, MatchesShown: 20, FilesShown: 1}
	if got := full.Completeness(false); got != "[source-search: 20 matches in 1 file in demo]\n" {
		t.Fatalf("complete single-project line = %q", got)
	}
	trunc := FleetResult{Searched: []string{"demo"}, MatchesTotal: 214, FilesTotal: 12, MatchesShown: 20, FilesShown: 1, Truncated: true}
	want := "[source-search: 214 matches in 12 files in demo; returned 20 matches from 1 file; truncated — raise --limit or narrow --files]\n"
	if got := trunc.Completeness(false); got != want {
		t.Fatalf("truncated line =\n got %q\nwant %q", got, want)
	}
	fleet := FleetResult{
		Searched: []string{"alpha", "beta"}, Skipped: []string{"delta (no text index)"}, NotSearched: []string{"gamma"},
		MatchesTotal: 214, FilesTotal: 87, MatchesShown: 20, FilesShown: 12, Truncated: true,
	}
	line := fleet.Completeness(false)
	for _, w := range []string{"214 matches in 87 files", "across 2 projects (alpha, beta)", "truncated",
		"skipped: delta (no text index)", "not searched: gamma (limit)"} {
		if !strings.Contains(line, w) {
			t.Fatalf("fleet line missing %q: %s", w, line)
		}
	}
	empty := FleetResult{Searched: []string{"demo"}}
	if got := empty.Completeness(false); got != "[source-search: no matches in demo]\n" {
		t.Fatalf("empty line = %q", got)
	}
	// The budget marker must only appear when the budget actually cut.
	if got := full.Completeness(true); !strings.Contains(got, "budget-truncated") {
		t.Fatalf("budget cut must be stated: %q", got)
	}
}

// full0Text renders an unbounded fleet search and fails the test on error, for
// the join assertion above.
func full0Text(t *testing.T, members []FleetMember) string {
	t.Helper()
	r, err := QueryZoektFleet(context.Background(), members, "Needle", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	return r.Text
}

// get_graph_schema's arguments were measured against CBM 0.11.0, not guessed,
// and the measurement is the only thing standing between the schema and the
// manage_adr defect: an enum that advertised modes the engine rejects. The
// engine ignores arguments it does not know rather than erroring, so an
// overstated schema would not fail at the boundary — it would quietly do
// nothing and read as a broken tool. Pin the exact surface.
func TestGetGraphSchemaArgumentSurface(t *testing.T) {
	tools := serveOne(t, &Server{Profile: ProfileAnalysis},
		`{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`)["result"].(map[string]any)["tools"].([]any)

	var schema map[string]any
	for _, tl := range tools {
		td := tl.(map[string]any)
		if td["name"] == "get_graph_schema" {
			schema = td["inputSchema"].(map[string]any)
		}
	}
	if schema == nil {
		t.Fatal("analysis does not expose get_graph_schema")
	}

	props := schema["properties"].(map[string]any)
	got := make([]string, 0, len(props))
	for k := range props {
		got = append(got, k)
	}
	sort.Strings(got)
	if want := []string{"limit", "offset", "project"}; !reflect.DeepEqual(got, want) {
		t.Errorf("properties = %v, want exactly %v — an argument CBM ignores must not be advertised", got, want)
	}

	req := schema["required"].([]any)
	if len(req) != 1 || req[0] != "project" {
		t.Errorf("required = %v, want [project] — the only argument CBM 0.11.0 rejects when missing", req)
	}
}

// It reaches the engine under its own name, like query_graph: no toolToCBM
// entry, so the name passes through.
func TestGetGraphSchemaPassthrough(t *testing.T) {
	run := &spyRunner{}
	resp := serveOne(t, &Server{
		Profile: ProfileAnalysis,
		Run:     run,
	}, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"get_graph_schema","arguments":{"project":"p","limit":3}}}`)
	if resp["error"] != nil {
		t.Fatalf("get_graph_schema failed: %v", resp)
	}
	if run.tool != "get_graph_schema" {
		t.Errorf("spawned %q, want get_graph_schema", run.tool)
	}
	// format:"json" is added by the cbmexec runner, not here, so what this
	// layer owes the engine is the caller's arguments untouched.
	if run.payload["project"] != "p" || run.payload["limit"] != float64(3) {
		t.Errorf("arguments did not reach the engine intact: %v", run.payload)
	}
}
