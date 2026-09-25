package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/ayayushsharma/true-knowledge/internal/cbmexec"
	"github.com/ayayushsharma/true-knowledge/internal/config"
	"github.com/ayayushsharma/true-knowledge/internal/gitx"
	"github.com/ayayushsharma/true-knowledge/internal/mcp"
	"github.com/ayayushsharma/true-knowledge/internal/paths"
	"github.com/ayayushsharma/true-knowledge/internal/store"
	"github.com/spf13/cobra"
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

// A bad --direction/--depth is a hard error, never a silently empty
// traversal: `trace` validates both before the CBM gate so the failure
// never depends on a spawn (or on a backend being installed at all).
func TestTraceArgValidation(t *testing.T) {
	for _, d := range []string{"inbound", "outbound", "both"} {
		if err := traceDirection(d); err != nil {
			t.Errorf("traceDirection(%q) = %v, want nil", d, err)
		}
	}
	for _, d := range []string{"", "in", "inbound ", "INBOUND", "sideways"} {
		err := traceDirection(d)
		if err == nil {
			t.Errorf("traceDirection(%q) = nil, want error", d)
			continue
		}
		if !strings.Contains(err.Error(), "inbound|outbound|both") {
			t.Errorf("traceDirection(%q) error must name the allowlist, got %v", d, err)
		}
	}

	for _, n := range []int{1, 2, 3, 4, 5} {
		if err := traceDepth(n); err != nil {
			t.Errorf("traceDepth(%d) = %v, want nil", n, err)
		}
	}
	for _, n := range []int{0, -1, 6, 100} {
		err := traceDepth(n)
		if err == nil {
			t.Errorf("traceDepth(%d) = nil, want error", n)
			continue
		}
		if !strings.Contains(err.Error(), "1-5") {
			t.Errorf("traceDepth(%d) error must name the range, got %v", n, err)
		}
	}
	// The engine's own vocabulary is the single source: flag help, the
	// validator allowlist, and the MCP schema enum must not drift apart.
	if len(traceDirections) != 3 || traceDepthMin != 1 || traceDepthMax != 5 {
		t.Fatalf("trace bounds drifted: %v depth %d-%d", traceDirections, traceDepthMin, traceDepthMax)
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

// ---------------------------------------------------------------------------
// The dual path: humans get the engine's table, --json gets the payload
//
// These drive cbmRead against a fake engine, so the contract is pinned
// without a daemon, an index, or a real repository behind it.
// ---------------------------------------------------------------------------

// shellQuote renders s as one single-quoted shell word. The fake scripts
// embed JSON in it, with quotes, newlines and backslashes, so this cannot be
// a hand-rolled escape.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'"'"'`) + "'"
}

// fakeEngine writes a format-aware fake engine and a Ctx wired to it, so the
// dual path can be pinned without a daemon, an index, or a real repo.
//
// Being format-aware is the point. The engine renders the text block
// according to the format it was asked for, so requesting format:"json"
// replaces a person's table with the JSON encoding of the same rows. A fake
// that ignored the argument would answer both requests identically and could
// not see that regression — which is the one that changes what a human reads.
//
// replies maps a tool name to its rendered text and its structuredContent. A
// tool that is absent gets a fixed reply, and a tool whose payload is empty
// answers with text only: the shape of a binary that predates the argument,
// which is the fallback path.
func fakeEngine(t *testing.T, replies map[string][2]string) (*Ctx, *cobra.Command, *bytes.Buffer) {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "fake-cbm")
	// argv is walked rather than indexed, so the script answers both the tree
	// call (`cli <tool> --args-file <path>`) and the structured one
	// (`cli --json <tool> --args-file <path>`).
	script := "#!/bin/sh\n" +
		"tool=\"\"; f=\"\"\n" +
		"while [ $# -gt 0 ]; do\n" +
		"  case \"$1\" in\n" +
		"    --args-file) f=\"$2\"; shift ;;\n" +
		"    cli|--json) ;;\n" +
		"    *) tool=\"$1\" ;;\n" +
		"  esac\n" +
		"  shift\n" +
		"done\n" +
		"json=no\n" +
		"if [ -n \"$f\" ] && grep -q format \"$f\" 2>/dev/null; then json=yes; fi\n" +
		"case \"$tool\" in\n"
	for tool, pair := range replies {
		treeText, err := json.Marshal(pair[0])
		if err != nil {
			t.Fatal(err)
		}
		field := ""
		textWhenJSON := string(treeText)
		if pair[1] != "" {
			// The text block holds a string, so the payload is quoted there;
			// structuredContent holds the object, so the payload is raw there.
			// Quoting both is the bug this comment exists to prevent.
			quoted, err := json.Marshal(pair[1])
			if err != nil {
				t.Fatal(err)
			}
			field = `,"structuredContent":` + pair[1]
			textWhenJSON = string(quoted)
		}
		script += tool + ")\n" +
			"  if [ \"$json\" = yes ]; then\n" +
			"    printf '%s\\n' " + shellQuote(`{"content":[{"type":"text","text":`+textWhenJSON+`}]`+field+`}`) + "\n" +
			"  else\n" +
			"    printf '%s\\n' " + shellQuote(`{"content":[{"type":"text","text":`+string(treeText)+`}]`+field+`}`) + "\n" +
			"  fi\n;;\n"
	}
	script += "*) printf '%s\\n' '{\"content\":[{\"type\":\"text\",\"text\":\"fallback tree\"}]}' ;;\nesac\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(dir, "home")
	p := paths.Resolve(home)
	if err := p.Ensure(); err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{Budgets: config.Budgets{DefaultChars: 8192, ArchitectureChars: 8192}}
	c := &Ctx{
		G:     Globals{},
		Paths: p,
		Cfg:   cfg,
		Reg:   map[string]store.Project{},
		Run:   &cbmexec.Runner{Bin: bin, Paths: p, Cfg: cfg},
		CBMOK: true,
	}
	out := &bytes.Buffer{}
	cmd := &cobra.Command{}
	cmd.SetOut(out)
	cmd.SetErr(out)
	return c, cmd, out
}

// The engine's own summary lines, verbatim, so the human face is asserted
// against what a person actually reads.
const fakeTree = "results: 1  (cols: qn label file lines in out)\n  sweep.Level2 Function chain.go 10-10 2 1\ntotal: 1\nreturned: 1\n"

// Verbatim structuredContent: cols/rows tables and integer counters, exactly
// as the engine sends them.
const fakeSearchData = `{"qn_rule":"qn = qn_prefix == \"\" ? name : qn_prefix + \".\" + name","cols":["name","label","lines","in","out"],"groups":[{"qn_prefix":"sweep","file":"chain.go","rows":[["Level2","Function","10-10",2,1]]}],"total":1,"returned":1,"count":1,"has_more":false,"truncated":false}`

const fakeCoverageData = `{"project":"sweep","signal":"best_effort","metadata":{"recording_status":"complete","generation_matches":true},"scopes":[{"total":0,"returned":0,"entries":[]}],"total":0}`

const fakeEmptyData = `{"cols":["name"],"groups":[],"total":0,"returned":0,"count":0,"has_more":false,"truncated":false}`

const fakeEmptyTree = "results: 0  (cols: qn label file lines in out)\ntotal: 0\nreturned: 0\n"

const fakeCoverageTree = "recording_status: complete\ngeneration_matches: true\n"

// engine answers search_graph with a hit and check_index_coverage with a
// clean whole-project verdict.
func engine() map[string][2]string {
	return map[string][2]string{
		"search_graph":         {fakeTree, fakeSearchData},
		"check_index_coverage": {fakeCoverageTree, fakeCoverageData},
	}
}

// emptyEngine answers every search with nothing. It is the reply that must
// never be reported as bare absence.
func emptyEngine() map[string][2]string {
	return map[string][2]string{
		"search_graph":         {fakeEmptyTree, fakeEmptyData},
		"check_index_coverage": {fakeCoverageTree, fakeCoverageData},
	}
}

// TestCBMCADHumanFaceIsTheTree: with no --json, the reply is the engine's
// rendered table and nothing else. This is the whole reason the two faces are
// separate calls: asking for format:"json" here would replace the table a
// person reads with the JSON encoding of the same rows.
func TestCBMReadHumanFaceIsTheTree(t *testing.T) {
	c, cmd, out := fakeEngine(t, engine())
	if err := c.cbmRead(cmd, cbmReadArgs{
		proj: "sweep", tool: "search_graph", gate: true,
		payload: map[string]any{"name_pattern": "Level2", "project": "sweep"},
	}); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	if !strings.Contains(got, "sweep.Level2 Function chain.go 10-10 2 1") {
		t.Errorf("human face lost the rendered table:\n%s", got)
	}
	if strings.Contains(got, `"structuredContent"`) || strings.Contains(got, `"qn_rule"`) {
		t.Errorf("human face leaked the payload:\n%s", got)
	}
}

// TestCBMReadJSONFaceIsThePayload: with --json, the answer is the engine's
// data under "data", and the rendered table is not sent beside it. Shipping
// both means shipping every result twice and leaving a machine to parse box
// drawing to find the value it was handed as JSON.
func TestCBMReadJSONFaceIsThePayload(t *testing.T) {
	c, cmd, out := fakeEngine(t, engine())
	c.G.JSON = true
	if err := c.cbmRead(cmd, cbmReadArgs{
		proj: "sweep", tool: "search_graph", gate: true,
		payload: map[string]any{"name_pattern": "Level2", "project": "sweep"},
		fields:  map[string]any{"route": "search_graph"},
	}); err != nil {
		t.Fatal(err)
	}
	var env map[string]any
	if err := json.Unmarshal(out.Bytes(), &env); err != nil {
		t.Fatalf("envelope is not JSON: %v\n%s", err, out.String())
	}
	if env["ok"] != true {
		t.Errorf("ok = %v", env["ok"])
	}
	data, ok := env["data"].(map[string]any)
	if !ok {
		t.Fatalf("no data: %s", out.String())
	}
	if data["total"] != float64(1) {
		t.Errorf("data lost the engine payload: %v", data)
	}
	if _, present := env["text"]; present {
		t.Errorf("text must not be sent beside data: %s", out.String())
	}
	if env["empty"] != false {
		t.Errorf("empty = %v, want false", env["empty"])
	}
	if env["route"] != "search_graph" {
		t.Errorf("caller fields lost: %v", env)
	}
	// The project is envelope metadata, not payload, so it has to be added
	// identically on both paths. A --json caller has no argv to fall back on,
	// and payloads like search_code's do not name the project themselves.
	if env["project"] != "sweep" {
		t.Errorf("project = %v, want sweep (the text face carries it)", env["project"])
	}
}

// TestCBMReadEmptyCarriesCoverage: an empty reply is a claim about absence,
// and on the JSON face there is no text to append a verdict to — so the
// evidence rides beside the payload instead.
func TestCBMReadEmptyCarriesCoverage(t *testing.T) {
	c, cmd, out := fakeEngine(t, emptyEngine())
	c.G.JSON = true
	if err := c.cbmRead(cmd, cbmReadArgs{
		proj: "sweep", tool: "search_graph", gate: true,
		payload: map[string]any{"name_pattern": "Nope", "project": "sweep"},
	}); err != nil {
		t.Fatal(err)
	}
	var env map[string]any
	if err := json.Unmarshal(out.Bytes(), &env); err != nil {
		t.Fatalf("envelope is not JSON: %v\n%s", err, out.String())
	}
	if env["empty"] != true {
		t.Errorf("empty = %v, want true", env["empty"])
	}
	cov, ok := env["coverage"].(map[string]any)
	if !ok {
		t.Fatalf("absence reported without coverage evidence: %s", out.String())
	}
	if cov["project"] != "sweep" {
		t.Errorf("coverage is not the engine's payload: %v", cov)
	}
	if _, polluted := env["data"].(map[string]any)["coverage"]; polluted {
		t.Error("the engine payload must be passed through untouched")
	}
}

// TestCBMReadHitHasNoCoverage: the coverage probe exists to back an absence.
// A hit needs no such evidence, so asking for it would spend a call to say
// nothing.
func TestCBMReadHitHasNoCoverage(t *testing.T) {
	c, cmd, out := fakeEngine(t, engine())
	c.G.JSON = true
	if err := c.cbmRead(cmd, cbmReadArgs{
		proj: "sweep", tool: "search_graph", gate: true,
		payload: map[string]any{"name_pattern": "Level2", "project": "sweep"},
	}); err != nil {
		t.Fatal(err)
	}
	var env map[string]any
	if err := json.Unmarshal(out.Bytes(), &env); err != nil {
		t.Fatal(err)
	}
	if _, present := env["coverage"]; present {
		t.Errorf("a hit was made to justify itself: %s", out.String())
	}
}

// TestCBMReadUngatedNeverProbes: a tool that cannot answer "nothing here"
// never claims absence, so it must not spend a coverage call either way.
func TestCBMReadUngatedNeverProbes(t *testing.T) {
	c, cmd, out := fakeEngine(t, emptyEngine())
	c.G.JSON = true
	if err := c.cbmRead(cmd, cbmReadArgs{
		proj: "sweep", tool: "get_architecture",
		payload: map[string]any{"project": "sweep"},
	}); err != nil {
		t.Fatal(err)
	}
	var env map[string]any
	if err := json.Unmarshal(out.Bytes(), &env); err != nil {
		t.Fatal(err)
	}
	if _, present := env["coverage"]; present {
		t.Errorf("an ungated tool probed coverage: %s", out.String())
	}
}

// TestCBMReadLegacyEngine: an engine that ignores format:"json" sends no
// structuredContent, and that absence is the capability probe. The reply
// degrades to the text it always sent rather than to a hole.
func TestCBMReadLegacyEngine(t *testing.T) {
	c, cmd, out := fakeEngine(t, nil)
	c.G.JSON = true
	if err := c.cbmRead(cmd, cbmReadArgs{
		proj: "sweep", tool: "get_architecture",
		payload: map[string]any{"project": "sweep"},
	}); err != nil {
		t.Fatal(err)
	}
	var env map[string]any
	if err := json.Unmarshal(out.Bytes(), &env); err != nil {
		t.Fatalf("envelope is not JSON: %v\n%s", err, out.String())
	}
	if env["text"] != "fallback tree" {
		t.Errorf("legacy text lost: %v", env)
	}
	if _, present := env["data"]; present {
		t.Errorf("data fabricated with no payload behind it: %s", out.String())
	}
	if env["structured"] != false {
		t.Errorf("structured = %v, want false", env["structured"])
	}
}
