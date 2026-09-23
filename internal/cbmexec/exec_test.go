package cbmexec_test

import (
	"strings"
	"testing"

	"github.com/true-knowledge/tk/internal/cbmexec"
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
