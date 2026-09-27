package docs

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("no go.mod above %s", dir)
		}
		dir = parent
	}
}

// TestDocsLint is the doc gate promised by the docs law: front-matter, unique
// ids, resolvable references, no stale pre-consolidation path, an immutable
// history, a manifest that matches disk, and a size budget per file.
func TestDocsLint(t *testing.T) {
	root := repoRoot(t)
	problems := Check(root)
	if len(problems) != 0 {
		t.Errorf("%d doc problem(s):", len(problems))
		for _, p := range problems {
			t.Errorf("  %s", p)
		}
	}
}

func TestSplitRefs(t *testing.T) {
	cases := []struct {
		in   string
		want int
	}{
		{"", 0},
		{"null", 0},
		{"[]", 0},
		{"[a.md]", 1},
		{"[a.md, b.md]", 2},
		{"[a.md (x, y), b.md]", 2},
		{"[internal/config/config.go, AGENTS.md]", 2},
	}
	for _, c := range cases {
		if got := len(splitRefs(c.in)); got != c.want {
			t.Errorf("splitRefs(%q) = %d refs, want %d", c.in, got, c.want)
		}
	}
}

func TestCleanRef(t *testing.T) {
	cases := map[string]string{
		"AGENT_DOCS/history/ROADMAP.md §MVP1":    "AGENT_DOCS/history/ROADMAP.md",
		"compatible-implementation-spec.md §5.2": "compatible-implementation-spec.md",
		"compatible-implementation-spec.md":      "compatible-implementation-spec.md",
		"internal/config/config.go":              "internal/config/config.go",
	}
	for in, want := range cases {
		if got := cleanRef(in); got != want {
			t.Errorf("cleanRef(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestReadFrontMatterRejectsMissingFence(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.md")
	if err := os.WriteFile(path, []byte("# no front-matter\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := readFrontMatter(path); err == nil {
		t.Fatal("want an error for a file with no front-matter fence")
	}
}

// TestHistoryImmutableFailsOnEdit proves the immutability gate is real, in a
// throwaway repo, so a clean run of TestDocsLint means the check has teeth once
// the tree is committed.
func TestHistoryImmutableFailsOnEdit(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	root := t.TempDir()
	hist := filepath.Join(root, HistoryDir, "DECISIONS")
	if err := os.MkdirAll(hist, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	adr := filepath.Join(hist, "2026-01-01-example.md")
	original := "---\ntitle: Example\nstatus: authoritative\n---\n\nfrozen\n"
	if err := os.WriteFile(adr, []byte(original), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q")
	run("config", "user.email", "lint@example.invalid")
	run("config", "user.name", "lint")
	run("add", ".")
	run("commit", "-q", "-m", "seed")

	if problems := checkHistoryImmutable(root); len(problems) != 0 {
		t.Fatalf("clean history reported %v", problems)
	}
	if err := os.WriteFile(adr, []byte(original+"\nedited\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	problems := checkHistoryImmutable(root)
	if len(problems) != 1 {
		t.Fatalf("edited history file: got %d problems, want 1: %v", len(problems), problems)
	}
	if !strings.Contains(problems[0], "immutable") {
		t.Errorf("problem does not name the rule: %q", problems[0])
	}
}

func TestAuthoredFilesStayUnderBudget(t *testing.T) {
	root := repoRoot(t)
	paths, err := filepath.Glob(filepath.Join(root, DocsDir, "*.md"))
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	if len(paths) == 0 {
		t.Fatal("no authored docs found")
	}
	for _, p := range paths {
		data, err := os.ReadFile(p)
		if err != nil {
			t.Fatalf("read %s: %v", p, err)
		}
		if n := countLines(data); n > MaxLines {
			t.Errorf("%s: %d lines, over the %d-line budget", filepath.Base(p), n, MaxLines)
		}
		if !strings.HasSuffix(strings.ToLower(filepath.Base(p)), ".md") {
			t.Errorf("%s: authored docs are markdown", filepath.Base(p))
		}
	}
}
