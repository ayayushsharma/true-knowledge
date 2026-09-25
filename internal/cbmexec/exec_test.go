package cbmexec_test

import (
	"context"
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
