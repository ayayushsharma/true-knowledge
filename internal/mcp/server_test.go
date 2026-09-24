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
