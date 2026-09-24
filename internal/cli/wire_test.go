package cli

import (
	"bytes"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/true-knowledge/tk/internal/paths"
	"github.com/true-knowledge/tk/internal/trace"
)

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
