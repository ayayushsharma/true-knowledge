package zoekttext_test

import (
	"context"
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

	updated, err := zoekttext.IndexRepo(context.Background(), shards, repo, "demo")
	if err != nil {
		t.Fatal(err)
	}
	if !updated {
		t.Fatal("first index should report updated=true")
	}

	// Incremental no-op: SHA already covered.
	updated, err = zoekttext.IndexRepo(context.Background(), shards, repo, "demo")
	if err != nil {
		t.Fatal(err)
	}
	if updated {
		t.Fatal("second index should report updated=false (incremental no-op)")
	}

	matches, err := zoekttext.Search(context.Background(), shards, "ProcessOrder", "", 20)
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
	matches, err = zoekttext.Search(context.Background(), shards, "return", "", 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 1 {
		t.Fatalf("limit=1 gave %d matches", len(matches))
	}

	// No matches is empty, not an error.
	matches, err = zoekttext.Search(context.Background(), shards, "nomatch_xyz_123", "", 20)
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
	if err := zoekttext.IndexDir(context.Background(), shards, dir, "plain"); err != nil {
		t.Fatal(err)
	}
	matches, err := zoekttext.Search(context.Background(), shards, "hello", "", 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) == 0 {
		t.Fatal("expected a match in plain dir index")
	}
}

func TestSearchMissingShards(t *testing.T) {
	_, err := zoekttext.Search(context.Background(), filepath.Join(t.TempDir(), "noshards"), "x", "", 20)
	if err == nil {
		t.Fatal("expected error for missing shards")
	}
}

// TestCancellationHonored proves every entry point honors the caller ctx:
// a cancelled caller is refused at the boundary (index) and stops a query
// that already has shards open (search). gitindex at this pin has no ctx, so
// IndexRepo's mid-build window is bounded by the build itself — documented.
func TestCancellationHonored(t *testing.T) {
	repo := makeGitRepo(t, map[string]string{"a.txt": "hello world\n"})
	shards := t.TempDir()
	if _, err := zoekttext.IndexRepo(context.Background(), shards, repo, "demo"); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := zoekttext.IndexRepo(ctx, shards, repo, "demo"); err == nil {
		t.Fatal("IndexRepo must refuse a cancelled ctx")
	}
	if err := zoekttext.IndexDir(ctx, shards, t.TempDir(), "plain"); err == nil {
		t.Fatal("IndexDir must refuse a cancelled ctx")
	}
	if _, err := zoekttext.Search(ctx, shards, "hello", "", 20); err == nil {
		t.Fatal("Search must stop for a cancelled ctx")
	}
	if _, err := zoekttext.SearchLive(ctx, shards, repo, "hello", "", 20); err == nil {
		t.Fatal("SearchLive must stop for a cancelled ctx")
	}
}

func TestSearchLive(t *testing.T) {
	repo := makeGitRepo(t, map[string]string{"a.txt": "line1 hello\nline2 world\n"})
	shards := t.TempDir()
	if _, err := zoekttext.IndexRepo(context.Background(), shards, repo, "demo"); err != nil {
		t.Fatal(err)
	}

	// Dirty the worktree: index still covers the old COMMIT, disk is newer.
	liveFile := filepath.Join(repo, "a.txt")
	if err := os.WriteFile(liveFile, []byte("line1 hello EDITED\nline2 world\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	shard, err := zoekttext.Search(context.Background(), shards, "hello", "", 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(shard) == 0 || shard[0].Text != "line1 hello" {
		t.Fatalf("shard bytes should stay old, got %+v", shard)
	}

	live, err := zoekttext.SearchLive(context.Background(), shards, repo, "hello", "", 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(live) == 0 || live[0].Text != "line1 hello EDITED" {
		t.Fatalf("live text = %+v", live)
	}
	if live[0].File != "a.txt" || live[0].Line != 1 {
		t.Fatalf("live match = %+v", live[0])
	}

	// Deleted-on-disk file stays, tagged.
	if err := os.Remove(liveFile); err != nil {
		t.Fatal(err)
	}
	gone, err := zoekttext.SearchLive(context.Background(), shards, repo, "hello", "", 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(gone) == 0 || gone[0].Text[:len("(worktree-missing)")] != "(worktree-missing)" {
		t.Fatalf("missing file should be tagged, got %+v", gone)
	}
}
