package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

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
		{"analysis", 14, []string{"validate", "query_graph", "manage_adr"}, nil},
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
		{"analysis", 14},
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
		EnsureIndex: func(string) error { indexed++; return nil },
		Staleness:   func(string) string { return "[source-search: 1 modified, 0 untracked in worktree not indexed]\n" },
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
		EnsureIndex: func(string) error { return errors.New("reindex boom") },
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
