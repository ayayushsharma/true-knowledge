package cli

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/true-knowledge/tk/internal/store"
)

func testCtx(reg store.Registry) *Ctx {
	return &Ctx{Reg: reg}
}

func TestRequireProject(t *testing.T) {
	reg := store.Registry{
		"demo":  {Path: "/a"},
		"other": {Path: "/b"},
	}
	c := testCtx(reg)
	if p, err := requireProject(c, "", []string{"other"}); err != nil || p != "other" {
		t.Fatalf("positional = %q %v", p, err)
	}
	if p, err := requireProject(c, "demo", []string{"other"}); err != nil || p != "demo" {
		t.Fatalf("flag = %q %v", p, err)
	}
	if _, err := requireProject(c, "", []string{"query words here"}); err == nil {
		t.Fatal("expected ambiguity error with 2 projects and no match")
	}
	single := testCtx(store.Registry{"solo": {Path: "/s"}})
	if p, err := requireProject(single, "", []string{"anything"}); err != nil || p != "solo" {
		t.Fatalf("single = %q %v", p, err)
	}
}

func TestKeywords(t *testing.T) {
	got := keywords(`  process an order, retry!  `)
	want := []string{"process", "an", "order", "retry"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %q", got)
	}
	if got := keywords("   "); len(got) != 0 {
		t.Fatalf("blank = %q", got)
	}
}

func TestFingerprintStableAndSensitive(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "a.txt")
	if err := os.WriteFile(f, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	a, err := store.Fingerprint(dir)
	if err != nil || a == "" {
		t.Fatalf("fp = %q %v", a, err)
	}
	b, err := store.Fingerprint(dir)
	if err != nil || a != b {
		t.Fatal("fingerprint not stable")
	}
	if err := os.WriteFile(f, []byte("xy"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Same-second mtime granularity can collide; count change still trips it.
	if err := os.WriteFile(filepath.Join(dir, "b.txt"), []byte("y"), 0o644); err != nil {
		t.Fatal(err)
	}
	c, _ := store.Fingerprint(dir)
	if c == a {
		t.Fatal("new file did not change fingerprint")
	}
}

func TestRepoRelative(t *testing.T) {
	c := testCtx(store.Registry{"demo": {Path: "/repo"}})
	if got := repoRelative(c, "demo", "/repo/src/a.go"); got != "src/a.go" {
		t.Fatalf("got %q", got)
	}
	if got := repoRelative(c, "demo", "rel/b.go"); got != "rel/b.go" {
		t.Fatalf("relative passthrough = %q", got)
	}
	if got := repoRelative(c, "demo", "/elsewhere/a.go"); got != "/elsewhere/a.go" {
		t.Fatalf("outside root = %q", got)
	}
}
