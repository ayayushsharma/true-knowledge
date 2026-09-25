package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/ayayushsharma/true-knowledge/internal/gitx"
	"github.com/ayayushsharma/true-knowledge/internal/mcp"
	"github.com/ayayushsharma/true-knowledge/internal/store"
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
	if _, err := requireProject(c, "", false); err == nil {
		t.Fatal("expected ambiguity error with 2 registered and no --project")
	}
	if p, err := requireProject(c, "demo", false); err != nil || p != "demo" {
		t.Fatalf("flag = %q %v", p, err)
	}
	single := testCtx(store.Registry{"solo": {Path: "/s"}})
	if p, err := requireProject(single, "", false); err != nil || p != "solo" {
		t.Fatalf("single = %q %v", p, err)
	}
}

// --select forces the picker open instead of auto-defaulting. A zero-value
// Ctx has ui.picker off and no TTY, so the forced picker is unreachable in
// tests: --select must hard-fail rather than silently resolve to the single
// registered project, and must never yield "" to CBM. --project wins outright.
func TestRequireProjectSelect(t *testing.T) {
	single := testCtx(store.Registry{"solo": {Path: "/s"}})
	// --select does not auto-default even with exactly one project.
	if p := resolveProject(single, "", true); p != "" {
		t.Fatalf("--select must not auto-default, got %q", p)
	}
	if p, err := requireProject(single, "", true); err == nil {
		t.Fatalf("--select with an unreachable picker must fail, got %q", p)
	} else if p != "" {
		t.Fatalf("--select must never resolve a project off-TTY, got %q", p)
	} else if !strings.Contains(err.Error(), "pass --project") {
		t.Fatalf("want routing hint in error, got %v", err)
	}
	// --project takes precedence: --select is ignored, no error.
	if p, err := requireProject(single, "solo", true); err != nil || p != "solo" {
		t.Fatalf("--project must win over --select, got %q %v", p, err)
	}
	// Nothing registered: a distinct error pointing at register.
	empty := testCtx(store.Registry{})
	if _, err := requireProject(empty, "", true); err == nil {
		t.Fatal("--select with an empty registry must fail")
	} else if !strings.Contains(err.Error(), "tk register") {
		t.Fatalf("want register hint for empty registry, got %v", err)
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

func TestResolveProfile(t *testing.T) {
	cases := []struct {
		name string
		flag bool
		fl   string
		env  string
		cfg  string
		want string
		err  bool
	}{
		{"defaults to scout", false, "", "", "", mcp.ProfileScout, false},
		{"flag wins over env and config", true, "memory", "analysis", "minimal", "memory", false},
		{"env beats config", false, "", "analysis", "minimal", "analysis", false},
		{"config fallback", false, "", "", "minimal", "minimal", false},
		{"invalid flag errors", true, "bogus", "memory", "", "", true},
		{"invalid env errors", false, "", "bogus", "memory", "", true},
		{"invalid config errors", false, "", "", "bogus", "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := resolveProfile(tc.flag, tc.fl, tc.env, tc.cfg)
			if tc.err {
				if err == nil {
					t.Fatalf("expected error, got %q", got)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Fatalf("got %q err %v (want %q)", got, err, tc.want)
			}
		})
	}
}

// syncFresh must require both backends fresh: clean claims never outrun the
// zoekt text index (git: ZoektHead == HEAD; plain dirs: built "files").
func TestSyncFresh(t *testing.T) {
	git := t.TempDir()
	head := initRepo(t, git)

	cases := []struct {
		name string
		p    store.Project
		want bool
	}{
		{"git fresh: HEAD + zoekt cover", store.Project{Path: git, Head: head, ZoektHead: head}, true},
		{"git dirty: HEAD moved", store.Project{Path: git, Head: "old"}, false},
		{"git dirty: zoekt stale", store.Project{Path: git, Head: head, ZoektHead: ""}, false},
		{"git dirty: zoekt older", store.Project{Path: git, Head: head, ZoektHead: "olderhead"}, false},
		{"git dirty: never indexed", store.Project{Path: git}, false},
	}
	for _, tc := range cases {
		if got := syncFresh(tc.p); got != tc.want {
			t.Errorf("%s: syncFresh = %v, want %v", tc.name, got, tc.want)
		}
	}

	// Plain dir path: fingerprint must match AND zoekt must be built ("files").
	plain := t.TempDir()
	fp, err := store.Fingerprint(plain)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		p    store.Project
		want bool
	}{
		{"plain fresh: fp + zoekt files", store.Project{Path: plain, Fingerprint: fp, ZoektHead: "files"}, true},
		{"plain dirty: zoekt never built", store.Project{Path: plain, Fingerprint: fp, ZoektHead: ""}, false},
		{"plain dirty: tree changed", store.Project{Path: plain, Fingerprint: "9-9", ZoektHead: "files"}, false},
		{"plain dirty: never indexed", store.Project{Path: plain}, false},
	} {
		if got := syncFresh(tc.p); got != tc.want {
			t.Errorf("%s: syncFresh = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// initRepo creates a git repo in dir with one commit and returns its HEAD.
func initRepo(t *testing.T, dir string) string {
	t.Helper()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q")
	run("config", "user.email", "t@t")
	run("config", "user.name", "t")
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", "-A")
	run("commit", "-qm", "init")
	return gitx.Head(dir)
}
