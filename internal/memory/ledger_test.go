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

func TestLedgerAppendAndFold(t *testing.T) {
	l := openTestLedger(t)
	fold, err := l.Get("demo", 1500)
	if err != nil {
		t.Fatal(err)
	}
	if len(fold) != 0 {
		t.Fatal("fresh ledger should be empty")
	}

	e, err := l.Append("demo", "goal", "build a codebase-cognizant agent")
	if err != nil {
		t.Fatalf("Append: %v", err)
	}
	if e.Seq != 1 || e.Key != "goal" || e.Value != "build a codebase-cognizant agent" {
		t.Fatalf("unexpected entry: %+v", e)
	}
	if _, err := l.Append("demo", "next", "scout coverage-before-absence"); err != nil {
		t.Fatalf("Append: %v", err)
	}
	// Last write per key wins: goal is replaced, next joins.
	if _, err := l.Append("demo", "goal", "build the best codebase agent"); err != nil {
		t.Fatalf("Append: %v", err)
	}
	fold, err = l.Get("demo", 1500)
	if err != nil {
		t.Fatal(err)
	}
	if fold["goal"].Value != "build the best codebase agent" {
		t.Fatalf("goal winner = %q", fold["goal"].Value)
	}
	if fold["next"].Value != "scout coverage-before-absence" {
		t.Fatalf("next winner = %q", fold["next"].Value)
	}
	seqs := []int{fold["goal"].Seq, fold["next"].Seq}
	if seqs[0] != 3 || seqs[1] != 2 {
		t.Fatalf("winners seqs = %v", seqs)
	}
	if len(fold) != 2 {
		t.Fatalf("fold keys = %d", len(fold))
	}
}

func TestLedgerHistoryCompleteAndUncapped(t *testing.T) {
	l := openTestLedger(t)
	long := strings.Repeat("x ", 1000)
	for _, kv := range [][2]string{{"goal", "g"}, {"done", long}, {"done", "d2"}} {
		if _, err := l.Append("demo", kv[0], kv[1]); err != nil {
			t.Fatal(err)
		}
	}
	hist, err := l.History("demo")
	if err != nil {
		t.Fatal(err)
	}
	if len(hist) != 3 {
		t.Fatalf("history len = %d, want 3", len(hist))
	}
	// History is never capped: the long value comes back whole.
	if hist[1].Value != long || hist[2].Value != "d2" {
		t.Fatalf("history values must be complete, got %d/%s chars", len(hist[1].Value), hist[2].Value)
	}
	// Old winner state survives in history while get folds latest only.
	fold, _ := l.Get("demo", 1500)
	if fold["done"].Value != "d2" {
		t.Fatalf("done winner = %q", fold["done"].Value)
	}
}

func TestLedgerRetrievalCap(t *testing.T) {
	l := openTestLedger(t)
	if _, err := l.Append("demo", "done", strings.Repeat("x ", 1000)); err != nil {
		t.Fatal(err)
	}
	fold, err := l.Get("demo", 32)
	if err != nil {
		t.Fatal(err)
	}
	if len(fold["done"].Value) > 32 {
		t.Fatalf("value not capped: %d chars", len(fold["done"].Value))
	}
	if !strings.HasSuffix(fold["done"].Value, "...truncated") {
		t.Fatalf("missing truncation marker: %q", fold["done"].Value)
	}
	// The stored log is untouched by the cap.
	hist, _ := l.History("demo")
	if len(hist[0].Value) != 2000 {
		t.Fatalf("stored value mutated by cap: %d chars", len(hist[0].Value))
	}
}

func TestLedgerValidation(t *testing.T) {
	l := openTestLedger(t)
	for _, bad := range [][2]string{
		{"bogus_key", "x"},
		{"goal", "   "},
	} {
		if _, err := l.Append("demo", bad[0], bad[1]); err == nil {
			t.Fatalf("Append(%q, %q) should fail", bad[0], bad[1])
		}
	}
	for _, p := range []string{"../evil", "a/b", "/abs", "a\\b"} {
		if _, err := l.Append(p, "goal", "x"); err == nil {
			t.Fatalf("Append(%q) should reject traversal", p)
		}
		if _, err := l.Get(p, 1500); err == nil {
			t.Fatalf("Get(%q) should reject traversal", p)
		}
		if err := l.Prune(p); err == nil {
			t.Fatalf("Prune(%q) should reject traversal", p)
		}
	}
	// Empty isn't allowed; one valid key is.
	if _, err := l.Append("demo", "open_questions", "is fleet stable?"); err != nil {
		t.Fatalf("valid key rejected: %v", err)
	}
}

func TestLedgerTrailingPartialLineTolerated(t *testing.T) {
	l := openTestLedger(t)
	if _, err := l.Append("demo", "goal", "g"); err != nil {
		t.Fatal(err)
	}
	// Simulate a crash mid-append: a partial JSON line at EOF.
	p := filepath.Join(l.dir, "demo.jsonl")
	f, err := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(`{"seq":2,"ts":"x","ke`); err != nil {
		t.Fatal(err)
	}
	f.Close()

	hist, err := l.History("demo")
	if err != nil {
		t.Fatalf("trailing partial line must not fail reads: %v", err)
	}
	if len(hist) != 1 {
		t.Fatalf("history = %d entries, want 1", len(hist))
	}
	// Next append continues after the last valid seq.
	e, err := l.Append("demo", "next", "n")
	if err != nil {
		t.Fatal(err)
	}
	if e.Seq != 2 {
		t.Fatalf("append seq = %d, want 2", e.Seq)
	}
}

func TestLedgerPrune(t *testing.T) {
	l := openTestLedger(t)
	if _, err := l.Append("demo", "goal", "g"); err != nil {
		t.Fatal(err)
	}
	// Legacy v1 blob should be removed too.
	if err := os.WriteFile(filepath.Join(l.dir, "demo.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := l.Prune("demo"); err != nil {
		t.Fatal(err)
	}
	fold, err := l.Get("demo", 1500)
	if err != nil {
		t.Fatal(err)
	}
	if len(fold) != 0 {
		t.Fatalf("after prune fold = %v", fold)
	}
	projs, err := l.Projects()
	if err != nil {
		t.Fatal(err)
	}
	if len(projs) != 0 {
		t.Fatalf("projects after prune = %v", projs)
	}
}

func TestLedgerFileMode(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("chmod assertions are meaningless as root")
	}
	l := openTestLedger(t)
	if _, err := l.Append("demo", "goal", "secret-ish truth"); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(filepath.Join(l.dir, "demo.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("ledger file mode = %o, want 600", fi.Mode().Perm())
	}
}

func TestLedgerProjects(t *testing.T) {
	l := openTestLedger(t)
	if _, err := l.Append("demo", "goal", "g"); err != nil {
		t.Fatal(err)
	}
	if _, err := l.Append("other", "done", "d"); err != nil {
		t.Fatal(err)
	}
	projs, err := l.Projects()
	if err != nil {
		t.Fatal(err)
	}
	if len(projs) != 2 || projs[0] != "demo" || projs[1] != "other" {
		t.Fatalf("projects = %v", projs)
	}
}
