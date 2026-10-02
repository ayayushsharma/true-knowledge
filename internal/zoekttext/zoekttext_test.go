package zoekttext_test

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/ayayushsharma/true-knowledge/internal/zoekttext"
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

	res, err := zoekttext.Search(context.Background(), shards, "ProcessOrder", "", 20)
	if err != nil {
		t.Fatal(err)
	}
	matches := res.Matches
	if len(matches) == 0 {
		t.Fatal("expected at least one match")
	}
	if res.Matches[0].File != "orders.go" || res.Matches[0].Line != 3 {
		t.Fatalf("match = %+v", matches[0])
	}

	// Limit bounds output, and the bounded Result still reports the true total.
	// "o" appears many times in the fixture, so limit=1 genuinely truncates.
	lim, err := zoekttext.Search(context.Background(), shards, "o", "", 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(lim.Matches) != 1 {
		t.Fatalf("limit=1 gave %d matches", len(lim.Matches))
	}
	if !lim.HasMore {
		t.Fatal("limit=1 over a multi-hit file must report HasMore")
	}
	if lim.Total <= len(lim.Matches) {
		t.Fatalf("Total=%d must exceed the %d returned matches", lim.Total, len(lim.Matches))
	}
	if lim.Files < 1 {
		t.Fatalf("Files=%d must count the file holding the hits", lim.Files)
	}

	// Unbounded: HasMore stays false and Total equals what was returned.
	all, err := zoekttext.Search(context.Background(), shards, "o", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if all.HasMore {
		t.Fatal("limit=0 must not report truncation")
	}

	// No matches is empty, not an error.
	res, err = zoekttext.Search(context.Background(), shards, "nomatch_xyz_123", "", 20)
	if err != nil {
		t.Fatal(err)
	}
	matches = res.Matches
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
	if err := zoekttext.IndexDir(context.Background(), shards, dir, "plain", nil); err != nil {
		t.Fatal(err)
	}
	res, err := zoekttext.Search(context.Background(), shards, "hello", "", 20)
	if err != nil {
		t.Fatal(err)
	}
	matches := res.Matches
	if len(matches) == 0 {
		t.Fatal("expected a match in plain dir index")
	}
}

// TestIndexDirSkipsCoreAndIgnore verifies the plain-dir filter: core
// dependency dirs always go, global ignore rules prune both files and whole
// subtrees, and lockfiles stay indexable.
func TestIndexDirSkipsCoreAndIgnore(t *testing.T) {
	dir := t.TempDir()
	files := map[string]string{
		"go.mod":                  "module needsLockfileTesting\n\ngo 1.26\n",
		"sub/used.txt":            "keepme\n",
		"vendor/dep.go":           "dropme\n",
		"sub/__pycache__/gen.pyc": "dropme\n",
	}
	for name, content := range files {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, name)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	// No ignore file: core skips only (vendor + __pycache__), used.txt stays.
	shards := t.TempDir()
	if err := zoekttext.IndexDir(context.Background(), shards, dir, "plain", nil); err != nil {
		t.Fatal(err)
	}
	for _, term := range []string{"keepme", "needsLockfileTesting"} {
		if mr, _ := zoekttext.Search(context.Background(), shards, term, "", 20); len(mr.Matches) == 0 {
			t.Fatalf("term %q must be indexed without ignores", term)
		}
	}
	if mr, _ := zoekttext.Search(context.Background(), shards, "dropme", "", 20); len(mr.Matches) != 0 {
		t.Fatalf("core dependency dirs must be skipped, got %d hits", len(mr.Matches))
	}

	// Ignore "used.txt": the file is pruned, others unaffected.
	shards2 := t.TempDir()
	if err := zoekttext.IndexDir(context.Background(), shards2, dir, "plain", []string{"used.txt"}); err != nil {
		t.Fatal(err)
	}
	if mr, _ := zoekttext.Search(context.Background(), shards2, "keepme", "", 20); len(mr.Matches) != 0 {
		t.Fatalf("ignored file must be pruned, got %d hits", len(mr.Matches))
	}
	if mr, _ := zoekttext.Search(context.Background(), shards2, "needsLockfileTesting", "", 20); len(mr.Matches) == 0 {
		t.Fatal("go.mod must stay after ignore (lockfiles are useful)")
	}
}

// TestIndexDirSkipsNonRegularAndOversize pins the two guards the walk owns
// itself, as opposed to filter policy (which is CBM's). A FIFO would block
// os.ReadFile forever and the pre-read ctx check cannot interrupt it, so the
// non-regular guard is what keeps `tk index` from hanging. An oversized file
// must be skipped without being buffered: zoekt's own SizeMax check happens
// after Add receives the bytes, so a pre-read is what bounds RAM.
func TestIndexDirSkipsNonRegularAndOversize(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "small.txt"), []byte("keepme in the index\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// A FIFO with no writer: reading it blocks until a writer appears, forever.
	fifo := filepath.Join(dir, "pipe")
	if err := syscall.Mkfifo(fifo, 0o644); err != nil {
		t.Skipf("mkfifo unavailable: %v", err)
	}
	// 3 MiB: past zoekt's 2 MiB SizeMax. The probe term is in the content and
	// not in the filename, so a hit can only mean the bytes were indexed.
	big := append([]byte("oversizemarker "), bytes.Repeat([]byte("a"), 3<<20)...)
	if err := os.WriteFile(filepath.Join(dir, "big.txt"), big, 0o644); err != nil {
		t.Fatal(err)
	}
	// A symlink to a real file: skipped, never indexed by the target's bytes.
	if err := os.Symlink(filepath.Join(dir, "small.txt"), filepath.Join(dir, "link.txt")); err != nil {
		t.Fatal(err)
	}

	shards := t.TempDir()
	done := make(chan error, 1)
	go func() { done <- zoekttext.IndexDir(context.Background(), shards, dir, "plain", nil) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(60 * time.Second):
		t.Fatal("IndexDir hung: a non-regular file was read (FIFO blocks forever)")
	}

	if mr, _ := zoekttext.Search(context.Background(), shards, "keepme", "", 20); len(mr.Matches) == 0 {
		t.Error("the regular file must still be indexed")
	}
	if mr, _ := zoekttext.Search(context.Background(), shards, "pipe", "", 20); len(mr.Matches) != 0 {
		t.Errorf("the FIFO must not be indexed, got %d hits", len(mr.Matches))
	}
	if mr, _ := zoekttext.Search(context.Background(), shards, "oversizemarker", "", 20); len(mr.Matches) != 0 {
		t.Errorf("an oversized file must be skipped, got %d hits", len(mr.Matches))
	}
}

// A .gitignore in a plain dir is NOT applied - CBM owns filter policy, and Zoekt
// reads only .sourcegraph/ignore. This test exists to fail loudly if someone
// later "fixes" that in tk and thereby reimplements CBM's chain inside tk
// (AGENTS.md 1: delegate, never reimplement). <config>/ignore is the supported
// plain-dir knob.
func TestIndexDirDoesNotReadGitignore(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("secret.txt\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "secret.txt"), []byte("ignoreme by gitignore\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	shards := t.TempDir()
	if err := zoekttext.IndexDir(context.Background(), shards, dir, "plain", nil); err != nil {
		t.Fatal(err)
	}
	if mr, _ := zoekttext.Search(context.Background(), shards, "ignoreme", "", 20); len(mr.Matches) == 0 {
		t.Error("plain-dir indexing must not start honoring .gitignore; use <config>/ignore")
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
	if err := zoekttext.IndexDir(ctx, shards, t.TempDir(), "plain", nil); err == nil {
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

	shardRes, err := zoekttext.Search(context.Background(), shards, "hello", "", 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(shardRes.Matches) == 0 || shardRes.Matches[0].Text != "line1 hello" {
		t.Fatalf("shard bytes should stay old, got %+v", shardRes.Matches)
	}

	liveRes, err := zoekttext.SearchLive(context.Background(), shards, repo, "hello", "", 20)
	if err != nil {
		t.Fatal(err)
	}
	live := liveRes.Matches
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
	goneRes, err := zoekttext.SearchLive(context.Background(), shards, repo, "hello", "", 20)
	if err != nil {
		t.Fatal(err)
	}
	gone := goneRes.Matches
	if len(gone) == 0 || gone[0].Text[:len("(worktree-missing)")] != "(worktree-missing)" {
		t.Fatalf("missing file should be tagged, got %+v", gone)
	}
}
