package gitx

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func clearDirtyCache() {
	dirtyMu.Lock()
	defer dirtyMu.Unlock()
	dirtyCache = map[string]dirtyDirty{}
}

func gitInit(t *testing.T, dir string, files map[string]string) {
	t.Helper()
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
	for name := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(files[name]), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	run("add", "-A")
	run("commit", "-qm", "init")
}

func TestHead(t *testing.T) {
	dir := t.TempDir()
	gitInit(t, dir, map[string]string{"a.txt": "one\n"})
	if h := Head(dir); len(h) != 40 {
		t.Fatalf("Head = %q", h)
	}
	if Head(t.TempDir()) != "" {
		t.Fatal("non-git dir should give empty HEAD")
	}
}

func TestDirty(t *testing.T) {
	dir := t.TempDir()
	gitInit(t, dir, map[string]string{"a.txt": "one\n"})

	if m, u, err := Dirty(dir); err != nil || m != 0 || u != 0 {
		t.Fatalf("clean repo: m=%d u=%d err=%v", m, u, err)
	}

	// Modified tracked file + brand-new untracked file.
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("two\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "new.txt"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	clearDirtyCache()
	if m, u, err := Dirty(dir); err != nil || m != 1 || u != 1 {
		t.Fatalf("dirty repo: m=%d u=%d err=%v", m, u, err)
	}

	// Staged edits still count as modified.
	cmd := exec.Command("git", "add", "a.txt")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git add: %v\n%s", err, out)
	}
	clearDirtyCache()
	if m, u, _ := Dirty(dir); m != 1 || u != 1 {
		t.Fatalf("staged: m=%d u=%d", m, u)
	}

	// Non-git dir: git errors, Dirty reports it.
	clearDirtyCache()
	if _, _, err := Dirty(t.TempDir()); err == nil {
		t.Fatal("expected error for non-git dir")
	}
}

func TestDirtyCached(t *testing.T) {
	old := dirtyCacheTTL
	dirtyCacheTTL = 100 * time.Millisecond
	defer func() {
		dirtyCacheTTL = old
		clearDirtyCache()
	}()

	dir := t.TempDir()
	gitInit(t, dir, map[string]string{"a.txt": "one\n"})
	clearDirtyCache()

	// First call computes and caches.
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("two\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if m, u, err := Dirty(dir); err != nil || m != 1 || u != 0 {
		t.Fatalf("first call: m=%d u=%d err=%v", m, u, err)
	}

	// Edit again within the TTL: the boasted annotation may lag up to 2s by
	// design when several agents share one process.
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("three\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "b.txt"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if m, u, _ := Dirty(dir); m != 1 || u != 0 {
		t.Fatalf("within TTL should serve cache (m=%d u=%d)", m, u)
	}

	time.Sleep(150 * time.Millisecond)
	if m, u, err := Dirty(dir); err != nil || m != 1 || u != 1 {
		t.Fatalf("after TTL expiry: m=%d u=%d err=%v", m, u, err)
	}
}
