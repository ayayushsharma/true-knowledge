package memory

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func openTestLedger(t *testing.T) *Ledger {
	t.Helper()
	l, err := OpenLedger(filepath.Join(t.TempDir(), "ledger"))
	if err != nil {
		t.Fatal(err)
	}
	return l
}

func TestLedgerRoundTrip(t *testing.T) {
	l := openTestLedger(t)
	text, _ := l.Get("demo")
	if text != "" {
		t.Fatal("fresh ledger should be empty")
	}

	got, err := l.Update("demo", "demo serves the public API; deploys weekly.", 1500)
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if got != "demo serves the public API; deploys weekly." {
		t.Fatalf("unexpected stored text: %q", got)
	}
	if text, _ := l.Get("demo"); text != got {
		t.Fatalf("Get mismatch: %q", text)
	}
	projects, _ := l.Projects()
	if len(projects) != 1 || projects[0] != "demo" {
		t.Fatalf("projects = %v", projects)
	}
	all, _ := l.All()
	if len(all) != 1 || !strings.Contains(all[0].Text, "public API") {
		t.Fatalf("All = %+v", all)
	}
}

func TestLedgerBudgetTruncation(t *testing.T) {
	l := openTestLedger(t)
	got, err := l.Update("demo", strings.Repeat("x ", 1000), 32)
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if len(got) > 32 {
		t.Fatalf("text not truncated: %d chars", len(got))
	}
	if !strings.HasPrefix(got, "x x") {
		t.Fatalf("unexpected truncation result %q", got)
	}
}

func TestLedgerDisabledRemovesFile(t *testing.T) {
	l := openTestLedger(t)
	if _, err := l.Update("demo", "some truth", 1500); err != nil {
		t.Fatal(err)
	}
	if _, err := l.Update("demo", "", 1500); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(l.dir, "demo.json")); !os.IsNotExist(err) {
		t.Fatal("empty ledger should remove the file")
	}
}

func TestLedgerProjectTraversalGuard(t *testing.T) {
	l := openTestLedger(t)
	for _, bad := range []string{"../evil", "a/b", "/abs", "a\\b"} {
		if _, err := l.Update(bad, "x", 1500); err == nil {
			t.Fatalf("Update(%q) should reject traversal", bad)
		}
		if _, err := l.Get(bad); err == nil {
			t.Fatalf("Get(%q) should reject traversal", bad)
		}
	}
}

func TestLedgerFileMode(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("chmod assertions are meaningless as root")
	}
	l := openTestLedger(t)
	if _, err := l.Update("demo", "secret-ish truth", 1500); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(filepath.Join(l.dir, "demo.json"))
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("ledger file mode = %o, want 600", fi.Mode().Perm())
	}
}
