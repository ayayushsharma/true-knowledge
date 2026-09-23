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
