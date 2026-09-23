package zoekttext_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/true-knowledge/tk/internal/zoekttext"
)

// makeGitRepo creates a tiny committed repo for indexing tests.
func makeGitRepo(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t",
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q")
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	run("add", "-A")
	run("commit", "-qm", "init")
	return dir
}

func TestIndexRepoAndSearch(t *testing.T) {
	repo := makeGitRepo(t, map[string]string{
		"orders.go": "package main\n\nfunc ProcessOrder(id string) string {\n\treturn \"ok:\" + id\n}\n",
	})
	shards := t.TempDir()

	updated, err := zoekttext.IndexRepo(shards, repo, "demo")
	if err != nil {
		t.Fatal(err)
	}
	if !updated {
		t.Fatal("first index should report updated=true")
	}

	// Incremental no-op: SHA already covered.
	updated, err = zoekttext.IndexRepo(shards, repo, "demo")
	if err != nil {
		t.Fatal(err)
	}
	if updated {
		t.Fatal("second index should report updated=false (incremental no-op)")
	}

	matches, err := zoekttext.Search(shards, "ProcessOrder", "", 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) == 0 {
		t.Fatal("expected at least one match")
	}
	if matches[0].File != "orders.go" || matches[0].Line != 3 {
		t.Fatalf("match = %+v", matches[0])
	}

	// Limit bounds output.
	matches, err = zoekttext.Search(shards, "return", "", 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 1 {
		t.Fatalf("limit=1 gave %d matches", len(matches))
	}

	// No matches is empty, not an error.
	matches, err = zoekttext.Search(shards, "nomatch_xyz_123", "", 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 0 {
		t.Fatalf("expected zero matches, got %d", len(matches))
	}
}

func TestIndexDirPlain(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "note.txt"), []byte("hello world\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	shards := t.TempDir()
	if err := zoekttext.IndexDir(shards, dir, "plain"); err != nil {
		t.Fatal(err)
	}
	matches, err := zoekttext.Search(shards, "hello", "", 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) == 0 {
		t.Fatal("expected a match in plain dir index")
	}
}

func TestSearchMissingShards(t *testing.T) {
	_, err := zoekttext.Search(filepath.Join(t.TempDir(), "noshards"), "x", "", 20)
	if err == nil {
		t.Fatal("expected error for missing shards")
	}
}
