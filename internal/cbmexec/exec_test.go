package cbmexec_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ayayushsharma/true-knowledge/internal/cbmexec"
	"github.com/ayayushsharma/true-knowledge/internal/config"
	"github.com/ayayushsharma/true-knowledge/internal/paths"
)

func TestTruncateWholeLines(t *testing.T) {
	s := "line1\nline2\nline3 is long and should be cut"
	out := cbmexec.Truncate(s, 12)
	if !strings.HasSuffix(out, "...truncated") {
		t.Fatalf("got %q", out)
	}
	if strings.Contains(out, "line3") {
		t.Fatalf("should prefer whole records, got %q", out)
	}
	if got := cbmexec.Truncate("short", 100); got != "short" {
		t.Fatalf("no-op failed: %q", got)
	}
}

func TestUnwrapEnvelopeContent(t *testing.T) {
	out := `{"jsonrpc":"2.0","id":1,"result":{"content":[{"type":"text","text":"hello graph"}]}}`
	text, isErr, _ := cbmexec.UnwrapEnvelope(out)
	if isErr || text != "hello graph" {
		t.Fatalf("got %q err=%v", text, isErr)
	}
}

func TestUnwrapEnvelopeStringResult(t *testing.T) {
	out := `{"jsonrpc":"2.0","id":1,"result":"plain payload"}`
	text, isErr, _ := cbmexec.UnwrapEnvelope(out)
	if isErr || text != "plain payload" {
		t.Fatalf("got %q err=%v", text, isErr)
	}
}

func TestUnwrapEnvelopeError(t *testing.T) {
	out := `{"jsonrpc":"2.0","id":1,"error":{"code":-32000,"message":"boom line1\nline2"}}`
	_, isErr, msg := cbmexec.UnwrapEnvelope(out)
	if !isErr || msg != "boom line1" {
		t.Fatalf("got %q err=%v", msg, isErr)
	}
}

func TestUnwrapEnvelopeTreeFallback(t *testing.T) {
	for _, out := range []string{"", "Function Demo\n  src/main.go:3", `{"ok":true}`} {
		if text, isErr, _ := cbmexec.UnwrapEnvelope(out); isErr || text != "" {
			t.Fatalf("tree %q must fall back, got %q", out, text)
		}
	}
}

// TestRunJSONFallback uses a fake that ignores --json (old binary shape):
// RunJSON must still return the legacy output, never fail on the flag.
func TestRunJSONFallback(t *testing.T) {
	dir := t.TempDir()
	fake := filepath.Join(dir, "fake-cbm")
	script := "#!/bin/sh\necho '{\"ok\":true,\"results\":[\"fake-hit\"]}'\n"
	if err := os.WriteFile(fake, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	p := paths.Resolve(filepath.Join(dir, "home"))
	r := &cbmexec.Runner{Bin: fake, Paths: p, Cfg: config.Config{}}
	out, err := r.RunJSON(context.Background(), "search_graph", map[string]any{"project": "demo"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "fake-hit") {
		t.Fatalf("fallback lost output: %q", out)
	}
}

// TestRunJSONEnvelope uses a fake emitting a real MCP envelope.
func TestCoverageVerdictClean(t *testing.T) {
	out := "project: demo\ngeneration_matches: true\nhash_records_complete: true\nrecording_status: complete\n"
	v := cbmexec.CoverageVerdict(out)
	if v[:15] != "coverage: clean" {
		t.Fatalf("got %q", v)
	}
}

func TestCoverageVerdictGap(t *testing.T) {
	cases := map[string]string{
		"stale generation": "generation_matches: false\nrecording_status: complete",
		"incomplete":       "generation_matches: true\nrecording_status: incomplete",
		"no projects":      "No projects indexed.",
	}
	for name, out := range cases {
		if v := cbmexec.CoverageVerdict(out); v[:13] != "coverage: GAP" && v[:15] != "coverage: GAP" {
			t.Fatalf("%s: got %q, want GAP", name, v)
		}
	}
}

func TestLooksEmptyAndTokens(t *testing.T) {
	if !cbmexec.LooksEmpty("") || !cbmexec.LooksEmpty("No results found") {
		t.Fatal("empty markers not detected")
	}
	if cbmexec.LooksEmpty("results: 2 (cols: qn)") {
		t.Fatal("non-empty output flagged empty")
	}
	got := cbmexec.NearMissTokens("doProcessOrder")
	want := []string{"process", "order"}
	if len(got) != len(want) {
		t.Fatalf("tokens = %q", got)
	}
}

// The fixtures below are verbatim CBM 0.11.0 output captured from a live
// `cbm cli --json <tool> --args-file` call. They are the reason the
// emptiness check and the envelope unwrap cannot be prose-only: the engine
// reports absence in counters, and its `cli --json` reply is a bare MCP
// result object with no `result` wrapper.
const realEmptyTrace = `function: Handler
direction: inbound
callers_total: 0
callers_total_relation: eq
callers: 0  (cols: qn hop)
`

const realEmptySearch = `results: 0  (cols: qn label file lines in out)
hint: "No nodes match; check spelling or broaden the regex."
total: 0
returned: 0
has_more: false
truncated: false
`

// TestLooksEmptyEngineCounters: a real zero-caller trace and a real search
// miss contain no prose marker at all, so marker matching alone would ship
// every genuine absence claim unproven.
func TestLooksEmptyEngineCounters(t *testing.T) {
	if !cbmexec.LooksEmpty(realEmptyTrace) {
		t.Error("real zero-caller trace must count as empty")
	}
	if !cbmexec.LooksEmpty(realEmptySearch) {
		t.Error("real zero-result search must count as empty")
	}
}

// TestNotEmptyMixedCounts: one non-zero counter is a hit. `direction=both`
// on a function with 2 callers and 0 callees must not be mistaken for
// absence.
func TestNotEmptyMixedCounts(t *testing.T) {
	out := `function: Handler
direction: both
callees_total: 1
callees_total_relation: eq
callees: 1  (cols: qn hop)
  live.ProcessOrder 1
callers_total: 0
callers_total_relation: eq
callers: 0  (cols: qn hop)
`
	if cbmexec.LooksEmpty(out) {
		t.Error("a traversal with one callee is a hit, not an absence")
	}
	if cbmexec.LooksEmpty("callers_total: 2\n  live.main 1\n") {
		t.Error("a populated trace is not empty")
	}
	// Metadata zeros and non-counter lines must not manufacture emptiness.
	if cbmexec.LooksEmpty("project: demo\nhas_more: false\ntruncated: false\nelapsed_ms: 93\n") {
		t.Error("no evidence counters present: never guess")
	}
}

// TestUnwrapEnvelopeBareResult: CBM 0.11.0 emits the MCP result object
// directly. Unwrapping it avoids a pointless legacy re-spawn.
func TestUnwrapEnvelopeBareResult(t *testing.T) {
	out := `{"content":[{"type":"text","text":"function: charge\ncallers_total: 1\n"}],"isError":false}`
	text, isErr, _ := cbmexec.UnwrapEnvelope(out)
	if isErr || text != "function: charge\ncallers_total: 1\n" {
		t.Fatalf("got %q err=%v", text, isErr)
	}
}

// TestUnwrapEnvelopeToolErrorHint: a tool that fails sets isError and puts
// its diagnosis in the content. The engine's own hint is the remediation,
// so it must reach tk's error instead of a raw JSON blob.
func TestUnwrapEnvelopeToolErrorHint(t *testing.T) {
	out := `{"content":[{"type":"text","text":"{\"error\":\"function not found\",\"function_name\":\"TotalMiss\",\"hint\":\"Use search_graph(name_pattern=...) to find the exact qualified name, then pass it to trace_path.\"}"}],"isError":true}`
	text, isErr, msg := cbmexec.UnwrapEnvelope(out)
	if !isErr {
		t.Fatalf("isError:true must surface as an error, got text %q", text)
	}
	if !strings.HasPrefix(msg, "function not found") {
		t.Errorf("msg = %q", msg)
	}
	if !strings.Contains(msg, "search_graph") {
		t.Errorf("engine hint lost: %q", msg)
	}
	if strings.Contains(msg, "{\\") || strings.HasPrefix(msg, "{") {
		t.Errorf("raw JSON leaked into the message: %q", msg)
	}
}

// A bare object with no content (the old `{"ok":true}` shape) must still
// fall through to the legacy re-spawn rather than being read as a result.
func TestUnwrapEnvelopeToolErrorNoHint(t *testing.T) {
	out := `{"content":[{"type":"text","text":"index is stale; run sync"}],"isError":true}`
	_, isErr, msg := cbmexec.UnwrapEnvelope(out)
	if !isErr || msg != "index is stale; run sync" {
		t.Fatalf("got %q err=%v", msg, isErr)
	}
	if text, isErr, _ := cbmexec.UnwrapEnvelope(`{"ok":true}`); isErr || text != "" {
		t.Fatalf("no content must fall back, got %q err=%v", text, isErr)
	}
}

func TestRunJSONEnvelope(t *testing.T) {
	dir := t.TempDir()
	fake := filepath.Join(dir, "fake-cbm")
	script := "#!/bin/sh\necho '{\"jsonrpc\":\"2.0\",\"id\":1,\"result\":{\"content\":[{\"type\":\"text\",\"text\":\"unwrapped hit\"}]}}'\n"
	if err := os.WriteFile(fake, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	p := paths.Resolve(filepath.Join(dir, "home"))
	r := &cbmexec.Runner{Bin: fake, Paths: p, Cfg: config.Config{}}
	out, err := r.RunJSON(context.Background(), "search_graph", map[string]any{"project": "demo"})
	if err != nil {
		t.Fatal(err)
	}
	if out != "unwrapped hit" {
		t.Fatalf("got %q", out)
	}
}

// ---------------------------------------------------------------------------
// Structured payloads (format:"json")
//
// The fixtures below are verbatim `structuredContent` from CBM 0.11.0,
// captured from live `cbm cli --json <tool> --args-file` calls. They pin the
// engine's own counter vocabulary, because LooksEmptyData has to agree with
// LooksEmpty on when a reply proves absence — and because the two look
// nothing alike: one scans `key: 0` lines, the other JSON keys.
// ---------------------------------------------------------------------------

// search_graph with no match. The engine states emptiness in counters
// (total/returned/count) plus an empty `groups`, with the remediation in
// `hint`. There is no prose marker anywhere.
const jsonEmptySearch = `{
  "qn_rule": "qn = qn_prefix == \"\" ? name : qn_prefix + \".\" + name",
  "cols": ["name", "label", "lines", "in", "out"],
  "groups": [],
  "hint": "No nodes match; check spelling or broaden the regex.",
  "total": 0, "returned": 0, "count": 0, "has_more": false, "truncated": false
}`

// search_graph with hits, grouped by qn_prefix + file. Rows are positional
// against `cols` and the qualified name is reconstructed from qn_prefix.
const jsonSearchHit = `{
  "qn_rule": "qn = qn_prefix == \"\" ? name : qn_prefix + \".\" + name",
  "cols": ["name", "label", "lines", "in", "out"],
  "groups": [
    {"qn_prefix": "sweep", "file": "misc.go", "rows": [["Deep", "Function", "9-14", 1, 0]]},
    {"qn_prefix": "sweep", "file": "chain.go", "rows": [["EntryA", "Function", "16-16", 0, 1], ["EntryB", "Function", "17-16", 0, 1]]}
  ],
  "total": 18, "returned": 3, "count": 3, "has_more": true, "next_offset": 3,
  "truncated": true, "truncation_reason": "page_limit"
}`

// trace_path, direction=both, on a function with 1 callee and 0 callers.
// One non-zero counter disqualifies emptiness — this is a hit.
const jsonMixedTrace = `{
  "function": "sweep.Level2", "direction": "both",
  "callees_total": 1, "callees_total_relation": "eq",
  "callees": {"qn_rule": "…", "cols": ["name", "hop"], "groups": [{"qn_prefix": "sweep", "rows": [["Level1", 1]]}]},
  "callers_total": 0, "callers_total_relation": "eq",
  "callers": {"qn_rule": "…", "cols": ["name", "hop"], "groups": []}
}`

// trace_path, direction=inbound, on a leaf: nothing calls it. The negative
// claim coverage-before-absence exists to prove.
const jsonEmptyTrace = `{
  "function": "sweep.Level0", "direction": "inbound",
  "callers_total": 0, "callers_total_relation": "eq",
  "callers": {"qn_rule": "…", "cols": ["name", "hop"], "groups": []}
}`

// search_code, three results. Two independent row tables plus a `directories`
// map, and `matches` is a real [13] array per row (the tree renders "13").
// The per-row `matches` array is why the trimmer must not descend into rows.
const jsonSearchCode = `{
  "cols": ["qn", "label", "file", "lines", "matches", "matches_omitted", "in", "out"],
  "rows": [
    ["demo.Level3", "Function", "chain.go", "13-13", [13], 0, 2, 1],
    ["demo.Level0", "Function", "chain.go", "4-4", [4], 0, 1, 0],
    ["demo.Level1", "Function", "chain.go", "7-7", [7], 0, 1, 1]
  ],
  "raw_matches": {
    "cols": ["file", "line", "content", "content_start_byte"],
    "rows": [
      ["chain.go", 3, "// Level0 is a leaf: nothing calls it.", 0],
      ["chain.go", 6, "// Level1 is called only by Level2.", 0],
      ["chain.go", 9, "// Level2 is called by Level3 and also by Orphan caller.", 0]
    ]
  },
  "directories": {"chain.go": 6},
  "total_grep_matches": 11, "total_results": 6, "raw_match_count": 3,
  "total_relation": "eq", "result_offset": 0, "results_returned": 3,
  "has_more": true, "next_offset": 3, "raw_returned": 3, "raw_has_more": false,
  "truncated": true, "elapsed_ms": 94, "dedup_ratio": "1.0x"
}`

// query_graph with zero rows. `columns` is a header list that is ALWAYS
// populated; if it counted as evidence this reply would read as a hit and
// the absence gate would never fire on a raw graph query.
const jsonEmptyQuery = `{"columns":["f.name"],"rows":[],"returned":0,"total":0,"total_relation":"eq","has_more":false,"truncated":false}`

// check_index_coverage, whole project, clean.
const jsonCoverageClean = `{
  "project": "sweep", "signal": "best_effort",
  "indexed_at": "2026-09-25T20:49:31Z",
  "metadata": {"generation": "2026-09-25T20:49:31Z", "index_mode": "moderate",
    "recording_status": "complete", "ignored_files_stored": 0, "ignored_files_total": 0,
    "hash_records_complete": true, "coverage_version": 3, "generation_matches": true},
  "paths": [], "path_total": 0, "path_returned": 0, "path_has_more": false,
  "scopes": [{"requested_scope": "whole-project", "scope": "whole-project",
    "total": 0, "returned": 0, "truncated": false, "entries": [], "status": "no_recorded_issue"}],
  "scope_total": 0, "scope_returned": 0, "scope_truncated": false, "has_more": false,
  "caveat": "Best-effort signal only."
}`

// TestLooksEmptyDataEngineCounters is the structured half of the pair with
// TestLooksEmptyEngineCounters: the same two replies, expressed the way
// format:"json" expresses them, must reach the same verdicts. If the two
// ever disagree, one surface silently stops proving absence.
func TestLooksEmptyDataEngineCounters(t *testing.T) {
	if !cbmexec.LooksEmptyData(json.RawMessage(jsonEmptySearch)) {
		t.Error("a zero-result structured search must count as empty")
	}
	if !cbmexec.LooksEmptyData(json.RawMessage(jsonEmptyTrace)) {
		t.Error("a zero-caller structured trace must count as empty")
	}
	if !cbmexec.LooksEmptyData(json.RawMessage(jsonEmptyQuery)) {
		t.Error("a zero-row query_graph must count as empty (columns is not evidence)")
	}
	if cbmexec.LooksEmptyData(json.RawMessage(jsonSearchHit)) {
		t.Error("a populated search is not empty")
	}
	if cbmexec.LooksEmptyData(json.RawMessage(jsonSearchCode)) {
		t.Error("a populated search_code is not empty")
	}
}

// TestNotEmptyMixedCountsData ports the mixed-counts guard to the structured
// path: one non-zero counter is a hit, not an absence.
func TestNotEmptyMixedCountsData(t *testing.T) {
	if cbmexec.LooksEmptyData(json.RawMessage(jsonMixedTrace)) {
		t.Error("a traversal with one callee is a hit, not an absence")
	}
}

// TestLooksEmptyDataNoGuess: a payload that states no counters is never
// called empty — absence is never inferred from silence.
func TestLooksEmptyDataNoGuess(t *testing.T) {
	snippet := `{"name":"Level2","qualified_name":"sweep.Level2","label":"Function","file_path":"/x/chain.go","start_line":10,"end_line":10,"source":"…","callers":1,"callees":1}`
	if cbmexec.LooksEmptyData(json.RawMessage(snippet)) {
		t.Error("get_code_snippet states no evidence counter; never guess")
	}
	if cbmexec.LooksEmptyData(nil) {
		t.Error("a nil payload is not empty; the text path owns that verdict")
	}
	if cbmexec.LooksEmptyData(json.RawMessage("not json")) {
		t.Error("an unreadable payload is not empty")
	}
}

// TestBudgetResultDropsRows: the budget is spent on whole records, never on
// bytes. Counters survive (they are the absence evidence) and the marker
// says what was spent.
func TestBudgetResultDropsRows(t *testing.T) {
	out := cbmexec.BudgetResult(json.RawMessage(jsonSearchCode), 600)
	if len(out) > 600 {
		t.Fatalf("still over budget: %d chars", len(out))
	}
	var got map[string]any
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("budgeted payload must stay valid JSON: %v", err)
	}
	if got["total_grep_matches"] == nil || got["total_results"] == nil || got["raw_match_count"] == nil {
		t.Error("counters must survive the trim; they are the absence evidence")
	}
	if got["budget_truncated"] != true {
		t.Error("budget_truncated marker missing")
	}
	if d, _ := got["rows_dropped"].(float64); d != 4 {
		t.Errorf("rows_dropped = %v, want 4 (1 result row + 3 raw_match rows)", got["rows_dropped"])
	}
	if got["truncated"] != true {
		t.Error("the engine's own truncated flag must keep its own meaning")
	}
	if got["budget_exceeded"] == true {
		t.Error("this budget is satisfiable; must not claim otherwise")
	}
	if _, ok := got["directories"]; !ok {
		t.Error("scalar maps are never dropped")
	}
	if cols, _ := got["raw_matches"].(map[string]any)["cols"].([]any); len(cols) != 4 {
		t.Error("raw_matches.cols is a header list, not a row table; it must survive whole")
	}
	// The result rows left behind must still carry their own `matches`
	// array: had the walk descended into a kept row, that nested array would
	// be a droppable container and would have been spent first.
	rows, _ := got["rows"].([]any)
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want 2 kept", len(rows))
	}
	first, _ := rows[0].([]any)
	matches, _ := first[4].([]any)
	if len(matches) != 1 {
		t.Errorf("a kept row lost its matches array: %v", first)
	}
}

// TestBudgetResultNestedOrder pins the escalation order: a group's rows are
// spent before the group itself, and only then do whole records go.
func TestBudgetResultNestedOrder(t *testing.T) {
	for _, tc := range []struct {
		budget      int
		wantGroups  int
		wantDropped float64
	}{
		{400, 2, 3}, // every row drained, both groups still worth keeping
		{350, 1, 4}, // rows gone; now the first whole record goes
		{320, 0, 5}, // still over: records go too
	} {
		out := cbmexec.BudgetResult(json.RawMessage(jsonSearchHit), tc.budget)
		if len(out) > tc.budget {
			t.Errorf("budget %d: over budget at %d chars", tc.budget, len(out))
		}
		var got map[string]any
		if err := json.Unmarshal(out, &got); err != nil {
			t.Fatalf("budget %d: invalid JSON: %v", tc.budget, err)
		}
		groups, _ := got["groups"].([]any)
		if len(groups) != tc.wantGroups {
			t.Errorf("budget %d: groups = %d, want %d", tc.budget, len(groups), tc.wantGroups)
		}
		for _, g := range groups {
			gm, _ := g.(map[string]any)
			if rows, _ := gm["rows"].([]any); len(rows) != 0 {
				t.Errorf("budget %d: a record survived with %d undrained rows", tc.budget, len(rows))
			}
		}
		if d, _ := got["rows_dropped"].(float64); d != tc.wantDropped {
			t.Errorf("budget %d: rows_dropped = %v, want %v", tc.budget, got["rows_dropped"], tc.wantDropped)
		}
		if got["total"] == nil {
			t.Errorf("budget %d: counters must survive", tc.budget)
		}
	}
}

// TestBudgetResultNeverCorrupts: once nothing droppable is left, the payload
// is returned whole and flagged, never cut. Under-trimming is honest; a
// caller can see the answer is complete and the budget simply did not fit.
func TestBudgetResultNeverCorrupts(t *testing.T) {
	// jsonSearchCode floors at 507 chars with every row gone: the header
	// lists and counters are not droppable, by design.
	out := cbmexec.BudgetResult(json.RawMessage(jsonSearchCode), 300)
	if len(out) > 600 {
		t.Fatalf("expected a whole payload, got %d chars", len(out))
	}
	var got map[string]any
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("must stay valid JSON: %v", err)
	}
	if got["budget_exceeded"] != true {
		t.Errorf("budget_exceeded marker missing, got %v", got["budget_exceeded"])
	}
	if got["total_grep_matches"] == nil {
		t.Error("counters survive even an unsatisfiable budget")
	}
	if rows, _ := got["rows"].([]any); len(rows) != 0 {
		t.Errorf("every droppable row must still be spent, got %d", len(rows))
	}

	// A payload with nothing droppable at all comes back untouched.
	in := json.RawMessage(`{"note":"aaaaaaaaaaaaaaaaaaaa","count":1}`)
	small := cbmexec.BudgetResult(in, 10)
	if err := json.Unmarshal(small, &got); err != nil {
		t.Fatalf("must stay valid JSON: %v", err)
	}
	if got["budget_exceeded"] != true {
		t.Errorf("budget_exceeded marker missing, got %v", got["budget_exceeded"])
	}
	if got["count"] != float64(1) {
		t.Error("scalars survive an unsatisfiable budget")
	}
}

// TestBudgetResultNoOp: a payload inside budget is returned byte-identical,
// so the common case never pays for a decode/re-encode.
func TestBudgetResultNoOp(t *testing.T) {
	in := json.RawMessage(jsonSearchCode)
	if got := cbmexec.BudgetResult(in, 0); string(got) != string(in) {
		t.Error("budget<=0 must be a no-op")
	}
	if got := cbmexec.BudgetResult(in, 1<<20); string(got) != string(in) {
		t.Error("an in-budget payload must be returned verbatim")
	}
}

// TestBudgetResultDeterministic: the same payload trims to the same bytes,
// which is what makes a golden-output test possible.
func TestBudgetResultDeterministic(t *testing.T) {
	a := cbmexec.BudgetResult(json.RawMessage(jsonSearchCode), 400)
	b := cbmexec.BudgetResult(json.RawMessage(jsonSearchCode), 400)
	if string(a) != string(b) {
		t.Errorf("trim is not deterministic:\n%s\n%s", a, b)
	}
}

// TestBudgetResultHonoursBudget is the invariant, swept over every budget:
// the returned bytes either fit the budget or say they do not. The markers
// are part of the payload, so a trim measured against the bare tree lands
// over by the size of the marker on every call — the failure this pins.
func TestBudgetResultHonoursBudget(t *testing.T) {
	for _, name := range []string{jsonSearchCode, jsonSearchHit, jsonEmptySearch, jsonEmptyQuery, jsonCoverageClean, jsonMixedTrace} {
		for budget := 1; budget <= 900; budget += 7 {
			out := cbmexec.BudgetResult(json.RawMessage(name), budget)
			var got map[string]any
			if err := json.Unmarshal(out, &got); err != nil {
				t.Fatalf("budget %d: invalid JSON: %v", budget, err)
			}
			if len(out) <= budget {
				continue
			}
			if got["budget_exceeded"] != true {
				t.Fatalf("budget %d: %d bytes over budget with no budget_exceeded marker", budget, len(out)-budget)
			}
		}
	}
}

// ---------------------------------------------------------------------------
// RunStructured: the format:"json" contract
// ---------------------------------------------------------------------------

// recordingCBM is a fake engine that appends its argv and the contents of
// the --args-file it was handed to a log, then replies with whatever reply
// it was given. It is how the tests assert what tk actually asks the engine
// for, rather than inferring it from a reply.
func recordingCBM(t *testing.T, dir, reply string) (*cbmexec.Runner, func() []string) {
	t.Helper()
	log := filepath.Join(dir, "calls.log")
	fake := filepath.Join(dir, "fake-cbm")
	script := "#!/bin/sh\n" +
		"printf '%s\\n' \"$@\" >> " + log + "\n" +
		"for a in \"$@\"; do case \"$a\" in --args-file) next=1;; *) if [ \"$next\" = 1 ]; then cat \"$a\" >> " + log + "; next=0; fi;; esac; done\n" +
		"cat <<'REPLY'\n" + reply + "\nREPLY\n"
	if err := os.WriteFile(fake, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	p := paths.Resolve(filepath.Join(dir, "home"))
	r := &cbmexec.Runner{Bin: fake, Paths: p, Cfg: config.Config{}}
	return r, func() []string {
		raw, err := os.ReadFile(log)
		if err != nil {
			return nil
		}
		return strings.Split(strings.TrimRight(string(raw), "\n"), "\n")
	}
}

const structuredReply = `{"jsonrpc":"2.0","id":1,"result":{"content":[{"type":"text","text":"╭─ demo ╮\n│ Level2 │\n╰───────╯"}],"structuredContent":{"cols":["name","label"],"groups":[{"qn_prefix":"demo","rows":[["Level2","Function"]]}],"total":1,"returned":1,"count":1,"has_more":false,"truncated":false}}}`

// TestRunStructuredAsksForJSON: the argument is what buys the payload, so
// the call is asserted at the argv level, not just by its result.
func TestRunStructuredAsksForJSON(t *testing.T) {
	dir := t.TempDir()
	r, calls := recordingCBM(t, dir, structuredReply)
	res, err := r.RunStructured(context.Background(), "search_graph", map[string]any{"project": "demo"})
	if err != nil {
		t.Fatal(err)
	}
	log := strings.Join(calls(), "\n")
	if !strings.Contains(log, "search_graph") {
		t.Errorf("tool name not passed: %q", log)
	}
	var args map[string]any
	raw := log[strings.Index(log, "{"):]
	if err := json.Unmarshal([]byte(raw), &args); err != nil {
		t.Fatalf("args file not JSON (%v): %q", err, log)
	}
	if args["format"] != "json" {
		t.Errorf("format = %v, want json", args["format"])
	}
	if args["project"] != "demo" {
		t.Errorf("caller payload was not forwarded: %v", args)
	}
	if res.Data == nil {
		t.Fatal("Data empty despite structuredContent")
	}
	var got map[string]any
	if err := json.Unmarshal(res.Data, &got); err != nil {
		t.Fatalf("Data is not JSON: %v", err)
	}
	if got["total"] == nil {
		t.Error("Data did not carry the engine payload")
	}
	if !strings.Contains(res.Text, "Level2") {
		t.Error("the text block is dropped: an agent needs the field, a human does not")
	}
}

// TestRunStructuredFallsBackOnce: an engine that ignores format:"json" and
// sends no structuredContent is an older binary. tk must degrade to the
// text path, and must not keep paying for the probe.
func TestRunStructuredFallsBackOnce(t *testing.T) {
	dir := t.TempDir()
	legacy := `{"jsonrpc":"2.0","id":1,"result":{"content":[{"type":"text","text":"demo Project {} - 0 0"}]}}`
	r, calls := recordingCBM(t, dir, legacy)

	res, err := r.RunStructured(context.Background(), "search_graph", map[string]any{"project": "demo"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Data != nil {
		t.Errorf("no structuredContent must not become a fabricated Data: %s", res.Data)
	}
	if !strings.Contains(res.Text, "Project") {
		t.Errorf("text lost on fallback: %q", res.Text)
	}
	if n := strings.Count(strings.Join(calls(), "\n"), `"format"`); n != 1 {
		t.Errorf("format was sent %d times, want 1 (probed, then remembered)", n)
	}
	if _, err := r.RunStructured(context.Background(), "search_graph", map[string]any{"project": "demo"}); err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(strings.Join(calls(), "\n"), `"format"`); n != 1 {
		t.Errorf("a probed binary was re-probed: %d sends", n)
	}
}

// TestRunJSONStaysTree: the human path must not ask for JSON. Sending
// format:"json" there would change every rendered tree in the product.
func TestRunJSONStaysTree(t *testing.T) {
	dir := t.TempDir()
	r, calls := recordingCBM(t, dir, structuredReply)
	if _, err := r.RunJSON(context.Background(), "search_graph", map[string]any{"project": "demo"}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.Join(calls(), "\n"), `"format"`) {
		t.Error("RunJSON asked for json; the human tree path must be untouched")
	}
}

// TestRunStructuredForcesFormat: a caller-supplied format is not an
// override. RunStructured exists to get Data; a caller that wanted a table
// calls RunJSON.
func TestRunStructuredForcesFormat(t *testing.T) {
	dir := t.TempDir()
	r, calls := recordingCBM(t, dir, structuredReply)
	if _, err := r.RunStructured(context.Background(), "query_graph", map[string]any{"project": "demo", "format": "table"}); err != nil {
		t.Fatal(err)
	}
	var args map[string]any
	log := strings.Join(calls(), "\n")
	raw := log[strings.Index(log, "{"):]
	if err := json.Unmarshal([]byte(raw), &args); err != nil {
		t.Fatal(err)
	}
	if args["format"] != "json" {
		t.Errorf("format = %v, want json", args["format"])
	}
}

// TestQualifiedNames pins the reassembly rule against a live capture.
//
// The row is ["Level2","Function","10-10",2,1] under qn_prefix "sweep", so
// the qualified name is "sweep.Level2" — a string that appears nowhere in the
// payload. Anyone reading the reply without reassembling it concludes the
// engine did not return the symbol it plainly returned.
func TestQualifiedNames(t *testing.T) {
	got := cbmexec.QualifiedNames(json.RawMessage(`{
		"qn_rule": "qn = qn_prefix == \"\" ? name : qn_prefix + \".\" + name",
		"cols": ["name", "label", "lines", "in", "out"],
		"groups": [
			{"qn_prefix": "sweep", "file": "chain.go", "rows": [["Level2", "Function", "10-10", 2, 1]]},
			{"qn_prefix": "", "file": "main.go", "rows": [["main", "Function", "3-3", 0, 0]]}
		]
	}`))
	want := []string{"sweep.Level2", "main"}
	if len(got) != len(want) {
		t.Fatalf("QualifiedNames = %q, want %q", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("QualifiedNames = %q, want %q", got, want)
		}
	}
}

// TestQualifiedNamesGuards: a payload with no `name` column, or none at all,
// yields nothing rather than a guess. The answer to "is this the symbol" must
// never be manufactured out of an unrelated column.
func TestQualifiedNamesGuards(t *testing.T) {
	for _, in := range []string{
		`{"cols":["qn","label"],"rows":[["sweep.Level2","Function"]]}`,
		`{"cols":["name"],"groups":[{"rows":[[]]}]}`,                  // short row
		`{"cols":["name"],"groups":[{"qn_prefix":"p","rows":[[7]]}]}`, // non-string leaf
		`{}`, `not json`, ``,
	} {
		if got := cbmexec.QualifiedNames(json.RawMessage(in)); len(got) != 0 {
			t.Errorf("QualifiedNames(%s) = %q, want none", in, got)
		}
	}
}

// TestLeafName: the engine matches name_pattern against the leaf, so the
// lookup splits there. Searching for the whole qualified name returns nothing
// — verified against CBM 0.11.0, where name_pattern "sweep.Level2" gave
// total 0 and "Level2" gave the one row.
func TestLeafName(t *testing.T) {
	for in, want := range map[string]string{
		"sweep.Level2": "Level2",
		"Level2":       "Level2",
		"a.b.c":        "c",
		"":             "",
		".":            "",
		"sweep.":       "",
	} {
		if got := cbmexec.LeafName(in); got != want {
			t.Errorf("LeafName(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestMatchSymbol pins the exactness rule, including the two cases the old
// substring test got wrong.
func TestMatchSymbol(t *testing.T) {
	// Verbatim from the live tree: one real hit, and the project row that a
	// search renders for the project itself.
	tree := "results: 1  (cols: qn label file lines in out)\n  sweep.Level2 Function chain.go 10-10 2 1\ntotal: 1\n"
	names := cbmexec.QualifiedNamesFromText(tree)
	if got := cbmexec.MatchSymbol(names, "sweep.Level2"); got != "sweep.Level2" {
		t.Errorf("qualified hit = %q, want sweep.Level2", got)
	}
	if got := cbmexec.MatchSymbol(names, "Level2"); got != "sweep.Level2" {
		t.Errorf("bare leaf = %q, want the full qualified name", got)
	}
	if got := cbmexec.MatchSymbol(names, "sweep.Level3"); got != "" {
		t.Errorf("a different symbol matched: %q", got)
	}
	if got := cbmexec.MatchSymbol(names, "Level3"); got != "" {
		t.Errorf("a different leaf matched: %q", got)
	}

	// The project row is the false positive the substring test walked into:
	// "sweep Project {} - 0 0" mentions the project, not the symbol.
	proj := cbmexec.QualifiedNamesFromText("results: 1  (cols: qn label file lines in out)\n  sweep Project {} - 0 0\ntotal: 1\n")
	if got := cbmexec.MatchSymbol(proj, "sweep.Level2"); got != "" {
		t.Errorf("the project row matched a qualified symbol: %q", got)
	}

	// A dotted symbol must match whole: a.b.x is not a.b.c.
	sibs := cbmexec.QualifiedNamesFromText("x\n  a.b.x Function f.go 1-1 0 0\n")
	if got := cbmexec.MatchSymbol(sibs, "a.b.c"); got != "" {
		t.Errorf("a near-miss qualifier matched: %q", got)
	}

	// Nothing in, nothing out.
	if got := cbmexec.MatchSymbol(nil, "sweep.Level2"); got != "" {
		t.Errorf("empty result matched: %q", got)
	}
}

// TestQualifiedNamesFromTextIgnoresNonRows: headers, counters and hints are not
// indented, so they cannot be mistaken for a result row.
func TestQualifiedNamesFromTextIgnoresNonRows(t *testing.T) {
	out := "results: 0  (cols: qn label file lines in out)\nhint: \"No nodes match; check spelling or broaden the regex.\"\ntotal: 0\nreturned: 0\n"
	if got := cbmexec.QualifiedNamesFromText(out); len(got) != 0 {
		t.Errorf("QualifiedNamesFromText = %q, want none", got)
	}
}
