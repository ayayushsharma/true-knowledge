package cbmexec_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/true-knowledge/tk/internal/cbmexec"
	"github.com/true-knowledge/tk/internal/config"
	"github.com/true-knowledge/tk/internal/paths"
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
