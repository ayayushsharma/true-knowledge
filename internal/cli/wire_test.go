package cli

import (
	"bytes"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/ayayushsharma/true-knowledge/internal/paths"
	"github.com/ayayushsharma/true-knowledge/internal/trace"
)

// TestTraceWiring pins the facade-verb surface of `trace` / `kg_trace`:
// the legacy alias, the strict flag-only grammar, the payload flag, and the
// engine-matching defaults. Metadata-only, so it holds with no backend.
func TestTraceWiring(t *testing.T) {
	root := NewRoot(&Globals{Home: t.TempDir()})
	cmd, _, err := root.Find([]string{"trace"})
	if err != nil || cmd.Name() != "trace" {
		t.Fatalf("trace not registered: %v", err)
	}

	var hasAlias bool
	for _, a := range cmd.Aliases {
		if a == "kg_trace" {
			hasAlias = true
		}
	}
	if !hasAlias {
		t.Fatalf("kg_trace compat alias missing, got %v", cmd.Aliases)
	}

	// NoArgs: a stray positional is a hard error, never a project sniff.
	if err := cmd.Args(cmd, []string{"ProcessOrder"}); err == nil {
		t.Fatal("trace must reject positional args")
	}
	if err := cmd.Args(cmd, nil); err != nil {
		t.Fatalf("trace with no positionals must pass Args: %v", err)
	}

	if f := cmd.Flags().Lookup("symbol"); f == nil || cmd.Flags().Lookup("project") == nil ||
		cmd.Flags().Lookup("direction") == nil || cmd.Flags().Lookup("depth") == nil ||
		cmd.Flags().Lookup("select") == nil {
		t.Fatal("trace flag set incomplete: want symbol, project, direction, depth, select")
	}
	if got := cmd.Flags().Lookup("direction").DefValue; got != "inbound" {
		t.Errorf("--direction default = %q, want inbound (engine default)", got)
	}
	if got := cmd.Flags().Lookup("depth").DefValue; got != "1" {
		t.Errorf("--depth default = %q, want 1 (engine default)", got)
	}
	// Resolution through the legacy name must land on the same command.
	if via, _, err := root.Find([]string{"kg_trace"}); err != nil || via != cmd {
		t.Fatalf("kg_trace must resolve to the trace command: %v", err)
	}
}

// TestFinalizeMasksEventSecrets: backend event Detail/Error strings reach
// tk.log through finalize and must carry the same redaction as argv/output.
func TestFinalizeMasksEventSecrets(t *testing.T) {
	home := t.TempDir()
	c := &Ctx{
		Paths: paths.Resolve(home),
		Events: []trace.Event{
			{Backend: "zoekt", Op: "search", Detail: "match sk-abcdefghijklmnopqrst", Error: "password: hunter2secret"},
		},
	}
	old := currentCtx
	currentCtx = c
	defer func() { currentCtx = old }()

	var buf bytes.Buffer
	buf.WriteString("clean output\n")
	finalize(&buf, time.Now(), nil)

	data, err := os.ReadFile(c.Paths.LogFile())
	if err != nil {
		t.Fatal(err)
	}
	s := string(data)
	for _, secret := range []string{"sk-abcdefghijklmnopqrst", "hunter2secret"} {
		if strings.Contains(s, secret) {
			t.Fatalf("secret %q leaked into event record: %s", secret, s)
		}
	}
	if !strings.Contains(s, "[REDACTED]") {
		t.Fatalf("expected a redaction marker: %s", s)
	}
	if !strings.Contains(s, "clean output") {
		t.Fatalf("legit output must survive: %s", s)
	}
}
