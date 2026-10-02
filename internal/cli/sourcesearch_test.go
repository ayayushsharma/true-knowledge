package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/ayayushsharma/true-knowledge/internal/cbmexec"
	"github.com/ayayushsharma/true-knowledge/internal/config"
	"github.com/ayayushsharma/true-knowledge/internal/mcp"
	"github.com/ayayushsharma/true-knowledge/internal/paths"
	"github.com/ayayushsharma/true-knowledge/internal/store"
	"github.com/ayayushsharma/true-knowledge/internal/zoekttext"
	"github.com/spf13/cobra"
)

// fleetCtx wires a Ctx against an isolated TK_HOME with n real git repos
// registered under the names alpha, beta, gamma, delta. Each holds two
// ProcessOrder functions, so a --limit 1 search cannot finish inside one
// project and the fleet's limit arithmetic is exercised.
func fleetCtx(t *testing.T, n int) *Ctx {
	t.Helper()
	home := t.TempDir()
	p := paths.Resolve(home)
	if err := p.Ensure(); err != nil {
		t.Fatal(err)
	}
	cfg := config.Defaults()
	cfg.Budgets.DefaultChars = 8192
	cfg.Budgets.ArchitectureChars = 8192
	c := &Ctx{G: Globals{}, Paths: p, Cfg: cfg, Reg: map[string]store.Project{}}
	for i := 0; i < n; i++ {
		name := []string{"alpha", "beta", "gamma", "delta"}[i]
		dir := filepath.Join(t.TempDir(), name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		initRepoWithFile(t, dir, "svc.go", "package main\n\nfunc ProcessOrder() {}\n\nfunc ProcessOrderAgain() {}\n")
		c.Reg[name] = store.Project{Path: dir, Mode: "moderate"}
	}
	return c
}

func initRepoWithFile(t *testing.T, dir, file, body string) {
	t.Helper()
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	git("init", "-q")
	git("config", "user.email", "t@t")
	git("config", "user.name", "t")
	if err := os.WriteFile(filepath.Join(dir, file), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	git("add", "-A")
	git("commit", "-qm", "init")
}

// fleetSearch runs the fleet path directly and returns what a human reads plus
// the --json fields it paired that text with.
func fleetSearch(t *testing.T, c *Ctx, limit int) (string, map[string]any) {
	t.Helper()
	out := &bytes.Buffer{}
	cmd := &cobra.Command{}
	cmd.SetOut(out)
	cmd.SetErr(out)
	// Execute() is what normally installs a context on a cobra command; these
	// tests drive the handler directly, so install one explicitly.
	cmd.SetContext(context.Background())
	var got map[string]any
	if err := runSourceSearchFleet(cmd, c, "ProcessOrder", "", limit, func(fields map[string]any) {
		got = fields
	}); err != nil {
		t.Fatal(err)
	}
	return out.String(), got
}

// TestSourceSearchFleetCLI: --all-projects answers for every registered repo
// from one invocation, with the repo prefix and the scope line.
func TestSourceSearchFleetCLI(t *testing.T) {
	c := fleetCtx(t, 3)
	text, _ := fleetSearch(t, c, 20)
	for _, name := range []string{"alpha", "beta", "gamma"} {
		if !strings.Contains(text, name+":svc.go") {
			t.Fatalf("no hit from %s: %q", name, text)
		}
	}
	if !strings.Contains(text, "[source-search: ") || !strings.Contains(text, "across 3 projects") {
		t.Fatalf("scope line missing: %q", text)
	}
	if strings.Contains(text, "\nsvc.go:") {
		t.Fatalf("fleet hits must be repo-prefixed: %q", text)
	}
}

// TestSourceSearchFleetJSONAgreesWithText: the two faces must not disagree
// about what was searched or how much came back.
func TestSourceSearchFleetJSONAgreesWithText(t *testing.T) {
	c := fleetCtx(t, 3)
	text, fields := fleetSearch(t, c, 20)
	if fields["scope"] != "all-projects" {
		t.Fatalf("envelope must declare the fleet scope: %v", fields["scope"])
	}
	searched, _ := fields["projects_searched"].([]string)
	if len(searched) != 3 {
		t.Fatalf("projects_searched = %v, want 3", searched)
	}
	total, _ := fields["matches_total"].(int)
	returned, _ := fields["matches_returned"].(int)
	if total < returned || returned == 0 {
		t.Fatalf("counts wrong: total=%d returned=%d", total, returned)
	}
	// The same facts must be readable from the JSON alone, with no prose.
	b, err := json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range []string{`"matches_total"`, `"projects_searched"`, `"truncated"`} {
		if !strings.Contains(string(b), w) {
			t.Fatalf("envelope missing %s: %s", w, b)
		}
	}
	if !strings.Contains(text, "across 3 projects") {
		t.Fatalf("human face lost the scope the JSON face carries: %q", text)
	}
}

// TestSourceSearchFleetReportsNotSearched: when the limit runs out, the
// untouched projects are named rather than silently absent.
func TestSourceSearchFleetReportsNotSearched(t *testing.T) {
	c := fleetCtx(t, 3)
	text, fields := fleetSearch(t, c, 1)
	if !strings.Contains(text, "truncated") {
		t.Fatalf("truncation must be stated: %q", text)
	}
	if !strings.Contains(text, "not searched:") {
		t.Fatalf("the unsearched tail must be named: %q", text)
	}
	notSearched, _ := fields["not_searched"].([]string)
	if len(notSearched) == 0 {
		t.Fatalf("--json must carry the unsearched tail too: %v", fields)
	}
	// Nothing may be claimed about a project that was never opened.
	if tr, _ := fields["truncated"].(bool); !tr {
		t.Fatalf("truncated must be true when the walk stopped early: %v", fields)
	}
}

// TestSourceSearchFleetOrderIsRegistryOrder: the walk follows sorted registry
// order, which is what makes a future cursor's (project, rank) sequence
// deterministic.
func TestSourceSearchFleetOrderIsRegistryOrder(t *testing.T) {
	c := fleetCtx(t, 3)
	text, _ := fleetSearch(t, c, 0)
	ai := strings.Index(text, "alpha:svc.go")
	bi := strings.Index(text, "beta:svc.go")
	gi := strings.Index(text, "gamma:svc.go")
	if ai < 0 || bi < 0 || gi < 0 {
		t.Fatalf("all three must be searched with --limit 0: %q", text)
	}
	if !(ai < bi && bi < gi) {
		t.Fatalf("walk order must be registry order: alpha@%d beta@%d gamma@%d", ai, bi, gi)
	}
	if strings.Contains(text, "not searched:") {
		t.Fatalf("--limit 0 must search the whole fleet: %q", text)
	}
}

// TestResolveSourceScopeIsExplicit: no scope is an error naming all three
// routes. Inference is what this command must not do, and this is the only
// project-resolving command that behaves so.
func TestResolveSourceScopeIsExplicit(t *testing.T) {
	c := fleetCtx(t, 3)
	if _, err := resolveSourceScope(c, "", false, false); err == nil {
		t.Fatal("a source-search with no scope must fail")
	} else {
		msg := err.Error()
		for _, w := range []string{"--project", "--select", "--all-projects"} {
			if !strings.Contains(msg, w) {
				t.Fatalf("error must name %s: %q", w, msg)
			}
		}
	}
	if _, err := resolveSourceScope(c, "alpha", false, true); err == nil {
		t.Fatal("--project with --all-projects must fail")
	}
	if got, err := resolveSourceScope(c, "alpha", false, false); err != nil || got != "alpha" {
		t.Fatalf("--project must resolve: %q %v", got, err)
	}
	if _, err := resolveSourceScope(c, "", false, true); err != nil {
		t.Fatalf("--all-projects must resolve with projects registered: %v", err)
	}
	if _, err := resolveSourceScope(c, "nope", false, false); err == nil {
		t.Fatal("an unknown --project must fail")
	} else if !strings.Contains(err.Error(), "nope") {
		t.Fatalf("the bad name must be echoed: %v", err)
	}
}

// TestResolveSourceScopeEmptyRegistry: --all-projects with nothing registered
// is a scope error naming the fix, never an empty result.
func TestResolveSourceScopeEmptyRegistry(t *testing.T) {
	c := fleetCtx(t, 0)
	if _, err := resolveSourceScope(c, "", false, true); err == nil || !strings.Contains(err.Error(), "tk register") {
		t.Fatalf("empty registry must name the fix: %v", err)
	}
}

// TestSourceSearchFleetNoFdOrGoroutineLeak: a fleet search opens one zoekt
// searcher per project, and each starts a directory watcher holding an fsnotify
// descriptor. A defer written in the loop body would run at function exit
// instead of per iteration, so this walks the fleet repeatedly and holds the
// process steady. The resident (`tk mcp --detach`) is long-lived, so a leak
// here accumulates for the life of a session, not the life of a command.
func TestSourceSearchFleetNoFdOrGoroutineLeak(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fd counting is Unix-specific")
	}
	fdDir := "/proc/self/fd"
	if _, err := os.Stat(fdDir); err != nil {
		t.Skip("no /proc/self/fd on this platform")
	}
	c := fleetCtx(t, 3)
	countFDs := func() int { d, _ := os.ReadDir(fdDir); return len(d) }
	countGoroutines := func() int { return runtime.NumGoroutine() }

	fleetSearch(t, c, 20) // warm the page cache and the incremental index
	time.Sleep(50 * time.Millisecond)
	fdBefore, grBefore := countFDs(), countGoroutines()
	for i := 0; i < 5; i++ {
		fleetSearch(t, c, 20)
	}
	time.Sleep(200 * time.Millisecond)
	fdAfter, grAfter := countFDs(), countGoroutines()

	// Three leaked watchers per run would be +15 fds; the tolerance is slack
	// for the one-off descriptors the harness itself opens.
	if fdAfter > fdBefore+2 {
		t.Errorf("fds grew across repeated fleet searches: %d -> %d (watchers not closed per project?)", fdBefore, fdAfter)
	}
	if grAfter > grBefore+2 {
		t.Errorf("goroutines grew across repeated fleet searches: %d -> %d", grBefore, grAfter)
	}
}

// TestSourceSearchFleetFailOpen: one project whose refresh fails is named and
// the rest of the fleet still answers. A refresh failure must never read as a
// project with no matches.
func TestSourceSearchFleetFailOpen(t *testing.T) {
	c := fleetCtx(t, 2)
	// Put a regular file where alpha's shard directory must go, so its refresh
	// fails for a reason that is about alpha alone. An empty or unreadable
	// project path would not do: a plain dir indexes successfully and returns
	// no hits, which is a different case entirely.
	if err := os.MkdirAll(c.Paths.ZoektDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(c.Paths.ZoektShards("alpha"), []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	text, fields := fleetSearch(t, c, 20)
	if !strings.Contains(text, "beta:svc.go") {
		t.Fatalf("the healthy project must still answer: %q", text)
	}
	if strings.Contains(text, "alpha:svc.go") {
		t.Fatalf("a project that never refreshed must not report hits: %q", text)
	}
	searched, _ := fields["projects_searched"].([]string)
	for _, s := range searched {
		if s == "alpha" {
			t.Fatalf("alpha must not appear in projects_searched: %v", searched)
		}
	}
}

var _ = cbmexec.Truncate

// TestSourceSearchSingleProjectFaceIsByteStable pins the rendering that existed
// before the fleet: `file:line: text`, no repo prefix, no per-file header, and
// no scope annotation when nothing was withheld. The pre-release law protects
// the human face, and grouping is deliberately fleet-only, so this is the
// assertion that keeps both true at once.
func TestSourceSearchSingleProjectFaceIsByteStable(t *testing.T) {
	c := fleetCtx(t, 3)
	// Index alpha only, then read the exact bytes the single-project path emits.
	if _, err := ensureZoektIndex(context.Background(), c, "alpha", c.Reg["alpha"].Path); err != nil {
		t.Fatal(err)
	}
	text, err := mcp.QueryZoektLive(context.Background(), c.Paths.ZoektShards("alpha"), c.Reg["alpha"].Path, "ProcessOrder", "", 20)
	if err != nil {
		t.Fatal(err)
	}
	want := "svc.go:3: func ProcessOrder() {}\nsvc.go:5: func ProcessOrderAgain() {}"
	if text != want {
		t.Fatalf("single-project rendering changed:\n got %q\nwant %q", text, want)
	}
	// The grouped renderer must differ from it, or grouping would be invisible.
	grouped := mcp.RenderMatches([]zoekttext.Match{
		{File: "svc.go", Line: 3, Text: "func ProcessOrder() {}", Repo: "alpha"},
		{File: "svc.go", Line: 5, Text: "func ProcessOrderAgain() {}", Repo: "alpha"},
	}, true)
	if !strings.Contains(grouped, "alpha:svc.go") || !strings.Contains(grouped, "(2 matches)") {
		t.Fatalf("grouped face must prefix and count: %q", grouped)
	}
}
