//go:build e2ereal

package e2ereal

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"
)

// scoutTools is the documented MCP contract for the default profile. The test
// pins the names, not just the count: a profile that quietly swaps a tool keeps
// the count and breaks every agent that called the old name.
var scoutTools = []string{
	"check_index_coverage", "detect_changes", "get_architecture", "get_code_snippet",
	"get_file_outline", "index_status", "list_projects", "search_code", "search_graph",
	"source_search", "trace_path",
}

type result struct {
	stdout, stderr string
	code           int
}

func (r result) String() string {
	return fmt.Sprintf("exit=%d\nstdout:\n%s\nstderr:\n%s", r.code, r.stdout, r.stderr)
}

// tk drives the built binary as a black box. Nothing here imports tk's
// packages: an end-to-end test that shares the code under test cannot catch
// that code being wrong.
type tk struct {
	bin     string
	home    string
	repo    string
	project string
	head    string
	// cbmBin is the CBM every invocation is pinned to. Empty until
	// installCBM resolves it, so the two pre-install calls cannot use an
	// engine the test has not version-checked yet.
	cbmBin string
}

// childEnv builds the environment for every tk invocation. The test process
// environment is stripped of the keys that would let state outside this
// TK_HOME redirect the engine: an inherited TK_CBM_BIN silently swaps the
// binary, and inherited CBM_CACHE_DIR / CBM_RUNTIME_DIR / CBM_ALLOWED_ROOT
// point the store, the daemon namespace, and the sandbox at someone else's
// home. What this test means to use is added back explicitly, so the gate
// cannot be moved by whatever the shell happens to export.
func childEnv(h tk) []string {
	drop := map[string]bool{
		"TK_CBM_BIN":       true,
		"CBM_CACHE_DIR":    true,
		"CBM_RUNTIME_DIR":  true,
		"CBM_ALLOWED_ROOT": true,
	}
	env := make([]string, 0, len(os.Environ())+2)
	for _, kv := range os.Environ() {
		if k, _, ok := strings.Cut(kv, "="); ok && drop[k] {
			continue
		}
		env = append(env, kv)
	}
	env = append(env, "TK_HOME="+h.home)
	if h.cbmBin != "" {
		env = append(env, "TK_CBM_BIN="+h.cbmBin)
	}
	return env
}

func (h tk) run(t *testing.T, args ...string) result {
	t.Helper()
	return h.runWithin(t, 10*time.Minute, args...)
}

func (h tk) runWithin(t *testing.T, limit time.Duration, args ...string) result {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), limit)
	defer cancel()
	cmd := exec.CommandContext(ctx, h.bin, args...)
	cmd.Env = childEnv(h)
	var out, errb strings.Builder
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	code := 0
	if err != nil {
		var exit *exec.ExitError
		if ok := asExit(err, &exit); ok {
			code = exit.ExitCode()
		} else {
			t.Fatalf("tk %s: %v", strings.Join(args, " "), err)
		}
	}
	return result{out.String(), errb.String(), code}
}

func asExit(err error, target **exec.ExitError) bool {
	e, ok := err.(*exec.ExitError)
	if ok {
		*target = e
	}
	return ok
}

// jsonData runs tk with --json and returns the envelope's `data` object. It
// fails when the envelope or that object is absent, so a caller-side crash can
// never be mistaken for an empty result. Every CBM-backed verb nests there.
func (h tk) jsonData(t *testing.T, args ...string) map[string]any {
	t.Helper()
	env := h.jsonEnv(t, args...)
	data, ok := env["data"].(map[string]any)
	if !ok {
		t.Fatalf("tk %s: envelope has no data object: %v", strings.Join(args, " "), env)
	}
	return data
}

// jsonPayload returns `data` when the envelope nests it and the envelope itself
// otherwise. The tk-owned verbs (source-search, status, config) report flat,
// the CBM-backed ones nest, and 00-INDEX promises neither shape — keys may
// change in any release — so the assertions read the fields they need instead
// of pinning a layout.
func (h tk) jsonPayload(t *testing.T, args ...string) map[string]any {
	t.Helper()
	env := h.jsonEnv(t, args...)
	if data, ok := env["data"].(map[string]any); ok {
		return data
	}
	return env
}

func (h tk) jsonEnv(t *testing.T, args ...string) map[string]any {
	t.Helper()
	all := append(append([]string{}, args...), "--json")
	r := h.run(t, all...)
	if r.code != 0 {
		t.Fatalf("tk %s exited %d\n%s", strings.Join(all, " "), r.code, r)
	}
	var env map[string]any
	if err := json.Unmarshal([]byte(r.stdout), &env); err != nil {
		t.Fatalf("tk %s: stdout is not a JSON envelope: %v\n%s", strings.Join(all, " "), err, r)
	}
	if env["ok"] != true {
		t.Fatalf("tk %s: envelope ok=%v\n%s", strings.Join(all, " "), env["ok"], r)
	}
	return env
}

func TestRealCBMAgainstRealRepo(t *testing.T) {
	h := setup(t)
	probe := probeSymbol(t, h)

	t.Run("index_is_clean_and_current", func(t *testing.T) {
		r := h.run(t, "status", "--json")
		if r.code != 0 {
			t.Fatalf("status: %s", r)
		}
		var env struct {
			Projects []struct {
				Project   string `json:"project"`
				State     string `json:"state"`
				Mode      string `json:"mode"`
				Head      string `json:"head"`
				ZoektHead string `json:"zoekt_head"`
			} `json:"projects"`
		}
		if err := json.Unmarshal([]byte(r.stdout), &env); err != nil {
			t.Fatalf("status: %v\n%s", err, r)
		}
		if len(env.Projects) != 1 {
			t.Fatalf("want exactly 1 registered project, got %d", len(env.Projects))
		}
		p := env.Projects[0]
		if p.State != "clean" {
			t.Errorf("state = %q, want clean", p.State)
		}
		if p.Head != h.head {
			t.Errorf("head = %q, want the indexed repo HEAD %q", p.Head, h.head)
		}
		if p.ZoektHead != h.head {
			t.Errorf("zoekt_head = %q, want %q — the text index is behind the graph", p.ZoektHead, h.head)
		}
		if p.Mode != env2(t, "TK_E2E_MODE", "moderate") {
			t.Errorf("mode = %q, want the requested mode", p.Mode)
		}
	})

	t.Run("graph_knows_a_symbol_read_from_the_repo", func(t *testing.T) {
		r := h.run(t, "find", "--project", h.project, "--query", probe)
		if r.code != 0 {
			t.Fatalf("find %s: %s", probe, r)
		}
		if !strings.Contains(r.stdout, probe) {
			t.Errorf("find %s returned no row naming it; the graph does not know this repo's own code", probe)
		}
	})

	t.Run("find_json_qn_reconstructs_and_explains", func(t *testing.T) {
		// The chain under test: search_graph returns a grouped payload, the
		// documented qn_rule rebuilds a qualified name from prefix + name, and
		// that name is what get_code_snippet accepts. A renderer that changes
		// the rule silently breaks every caller that copies a name out of a
		// result, so the rule is asserted, not assumed.
		data := h.jsonData(t, "find", "--project", h.project, "--query", probe)
		qn := firstQN(t, data)
		if qn == "" {
			t.Fatalf("no qualified name in payload for %s: %v", probe, data)
		}
		definition := h.jsonData(t, "explain", "--project", h.project, "--symbol", qn)["definition"]
		if definition == nil {
			t.Fatalf("explain %s: no definition block: %v", qn, data)
		}
		def, _ := definition.(map[string]any)
		if got, _ := def["qualified_name"].(string); got != qn {
			t.Errorf("definition.qualified_name = %q, want %q", got, qn)
		}
		if src, _ := def["source"].(string); strings.TrimSpace(src) == "" {
			t.Errorf("definition.source is empty for %s — a resolved name with no code is a lie", qn)
		}
		if path, _ := def["file_path"].(string); !strings.HasPrefix(path, h.repo) {
			t.Errorf("definition.file_path = %q, want a path under %s", path, h.repo)
		}
		if line, _ := def["start_line"].(float64); line < 1 {
			t.Errorf("definition.start_line = %v, want >= 1", def["start_line"])
		}
	})

	t.Run("absence_is_annotated_never_bare", func(t *testing.T) {
		// House rule: no negative claim without the coverage verdict. The name
		// is the probe symbol with a suffix, so it cannot resolve but is close
		// enough to offer near misses — the shape a real typo produces.
		bogus := probe + "NotAThing"
		data := h.jsonData(t, "validate", "--project", h.project, "--symbol", bogus)
		coverage, _ := data["coverage"].(map[string]any)
		if coverage == nil {
			t.Fatalf("validate %s: no coverage verdict: %v", bogus, data)
		}
		meta, _ := coverage["metadata"].(map[string]any)
		if meta["generation_matches"] != true {
			t.Errorf("coverage.metadata.generation_matches = %v, want true", meta["generation_matches"])
		}
		if meta["hash_records_complete"] != true {
			t.Errorf("coverage.metadata.hash_records_complete = %v, want true", meta["hash_records_complete"])
		}
		if meta["recording_status"] != "complete" {
			t.Errorf("coverage.metadata.recording_status = %v, want complete", meta["recording_status"])
		}
		if m, ok := data["match"].(map[string]any); ok && m != nil {
			t.Errorf("validate %s reported a match; the probe name must not resolve", bogus)
		}
		// near_miss is best-effort — a name with no similarity yields none — so
		// its absence is not a failure. Its presence must still be counted.
		if near, ok := data["near_miss"].(map[string]any); ok && near != nil {
			if total, counted := near["total"].(float64); !counted {
				t.Errorf("near_miss carries no total: %v", near)
			} else if total < 1 {
				t.Errorf("near_miss total = %v for a near-miss name, want >= 1", total)
			}
		}
	})

	t.Run("unresolvable_symbol_exits_nonzero_with_empty_stdout", func(t *testing.T) {
		// Both faces must fail the same way. A --json caller that gets exit 0
		// and an empty stdout cannot tell "absent" from "tk crashed".
		r := h.run(t, "explain", "--project", h.project, "--symbol", "definitely.not.a.symbol")
		if r.code == 0 {
			t.Errorf("explain on a bogus symbol exited 0: %s", r)
		}
		if strings.TrimSpace(r.stdout) != "" {
			t.Errorf("explain on a bogus symbol wrote to stdout: %q", r.stdout)
		}
		if !strings.Contains(r.stderr, "search_graph") {
			t.Errorf("stderr does not name the recovery path: %q", r.stderr)
		}
	})

	t.Run("source_search_paths_exist_on_disk", func(t *testing.T) {
		// Zoekt, not the graph. The cap is explicit (--limit), so a small limit
		// plus a repo-specific term makes the hit deterministic; every path the
		// index names must exist in the worktree, or the text index is serving
		// files that are not there.
		data := h.jsonPayload(t, "source-search", "--project", h.project, "--pattern", probe, "--limit", "5")
		if data["backend"] != "zoekt" {
			t.Errorf("backend = %v, want zoekt", data["backend"])
		}
		if data["zoekt_fresh"] != true {
			t.Errorf("zoekt_fresh = %v, want true", data["zoekt_fresh"])
		}
		if _, ok := data["worktree_modified"]; !ok {
			t.Errorf("envelope omits worktree_modified; unindexed edits must be visible: %v", data)
		}
		text, _ := data["text"].(string)
		lines := strings.Split(strings.TrimSpace(text), "\n")
		if len(lines) == 0 || lines[0] == "" {
			t.Fatalf("source-search %s returned no matches: %v", probe, data)
		}
		seen := 0
		for _, line := range lines {
			path, _, ok := strings.Cut(line, ":")
			if !ok || path == "" || strings.HasPrefix(line, "[") {
				continue
			}
			seen++
			if _, err := os.Stat(filepath.Join(h.repo, path)); err != nil {
				t.Errorf("index names a file that is not in the worktree: %s", path)
			}
		}
		if seen == 0 {
			t.Errorf("no parsable path:line rows in %q", text)
		}
	})

	t.Run("sync_on_clean_head_is_a_noop", func(t *testing.T) {
		r := h.run(t, "sync", h.project)
		if r.code != 0 {
			t.Fatalf("sync: %s", r)
		}
		if !strings.Contains(r.stdout, "no-op") {
			t.Errorf("sync on a clean HEAD re-indexed instead of no-op: %s", r)
		}
	})

	t.Run("mcp_profile_and_structured_payload", func(t *testing.T) {
		lines := h.mcp(t,
			map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": map[string]any{
				"protocolVersion": "2025-06-18", "capabilities": map[string]any{},
				"clientInfo": map[string]any{"name": "e2ereal", "version": "1"}}},
			map[string]any{"jsonrpc": "2.0", "id": 2, "method": "tools/list"},
			map[string]any{"jsonrpc": "2.0", "id": 3, "method": "tools/call", "params": map[string]any{
				"name": "search_graph", "arguments": map[string]any{
					"project": h.project, "name_pattern": probe, "limit": 5}}},
		)
		var names []string
		for _, line := range lines {
			if id, _ := line["id"].(float64); id == 2 {
				tools, _ := line["result"].(map[string]any)["tools"].([]any)
				for _, tool := range tools {
					names = append(names, tool.(map[string]any)["name"].(string))
				}
			}
		}
		sort.Strings(names)
		want := append([]string{}, scoutTools...)
		sort.Strings(want)
		if strings.Join(names, ",") != strings.Join(want, ",") {
			t.Errorf("scout tools = %v\nwant %v", names, want)
		}
		for _, line := range lines {
			if id, _ := line["id"].(float64); id != 3 {
				continue
			}
			result, _ := line["result"].(map[string]any)
			sc, _ := result["structuredContent"].(map[string]any)
			if sc == nil {
				t.Fatalf("search_graph returned no structuredContent: %v", result)
			}
			if total, _ := sc["total"].(float64); total < 1 {
				t.Errorf("search_graph total = %v, want >= 1 for %s", sc["total"], probe)
			}
		}
	})

	t.Run("trace_log_is_jsonl", func(t *testing.T) {
		path := filepath.Join(h.home, "state", "logs", "tk.log")
		f, err := os.Open(path)
		if err != nil {
			t.Fatalf("trace log: %v", err)
		}
		defer f.Close()
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 0, 1<<20), 1<<22)
		n, withArgv := 0, 0
		for sc.Scan() {
			var entry map[string]any
			if err := json.Unmarshal(sc.Bytes(), &entry); err != nil {
				t.Fatalf("trace log line %d is not JSON: %v", n+1, err)
			}
			if argv, _ := entry["argv"].([]any); len(argv) > 0 {
				withArgv++
			}
			n++
		}
		if err := sc.Err(); err != nil {
			t.Fatalf("trace log: %v", err)
		}
		if n == 0 || withArgv == 0 {
			t.Errorf("trace log has %d entries, %d with argv; want a populated log", n, withArgv)
		}
	})
}

func setup(t *testing.T) tk {
	t.Helper()
	if os.Getenv("TK_E2E_REAL") != "1" {
		t.Skip("set TK_E2E_REAL=1 to run against a real engine and a real repo")
	}
	repo := os.Getenv("TK_E2E_REPO")
	if repo == "" {
		t.Skip("set TK_E2E_REPO=/path/to/repo (a git work tree with real source in it)")
	}
	abs, err := filepath.Abs(repo)
	if err != nil {
		t.Fatalf("TK_E2E_REPO: %v", err)
	}
	repo = abs
	head := strings.TrimSpace(git(t, repo, "rev-parse", "HEAD"))
	if head == "" {
		t.Fatalf("%s is not a git work tree", repo)
	}
	name := env2(t, "TK_E2E_NAME", filepath.Base(repo))

	home := t.TempDir()
	bin := filepath.Join(t.TempDir(), "tk")
	buildTk(t, bin)

	h := tk{bin: bin, home: home, repo: repo, project: name, head: head}
	if r := h.run(t, "init"); r.code != 0 {
		t.Fatalf("init: %s", r)
	}
	// CBM sandboxes reads to this root; without it a repo outside the tk
	// checkout is invisible to the engine and every assertion below fails for
	// the wrong reason.
	if r := h.run(t, "config", "set", "allowed_root", filepath.Dir(repo)); r.code != 0 {
		t.Fatalf("config set allowed_root: %s", r)
	}
	// Pin resolution before anything graph-backed runs. After this line every
	// tk invocation names the same engine, whatever PATH or the shell says.
	h.cbmBin = installCBM(t, h)
	if r := h.run(t, "register", repo, "--name", name); r.code != 0 {
		t.Fatalf("register: %s", r)
	}

	// CBM stops on last committed client disconnect, asynchronously. t.TempDir
	// removes the home underneath that, and the next run inherits a runtime
	// dir whose endpoint and cohort locks are already gone. Cleanups run LIFO,
	// so this stops the daemon before the directory is removed.
	t.Cleanup(func() { h.run(t, "daemon", "stop") })

	mode := env2(t, "TK_E2E_MODE", "moderate")
	t.Logf("indexing %s (%s, HEAD %s) with real cbm — this is the slow part", repo, mode, head[:12])
	budget := 45 * time.Minute
	if v := os.Getenv("TK_E2E_TIMEOUT"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			t.Fatalf("TK_E2E_TIMEOUT: %v", err)
		}
		budget = d
	}
	if r := h.runWithin(t, budget, "index", name, "--mode", mode); r.code != 0 {
		t.Fatalf("index %s --mode %s: %s", name, mode, r)
	}
	return h
}

// installCBM returns the CBM the whole gate is pinned to. TK_E2E_CBM_BIN
// reuses a local binary for offline runs, and it is still version-checked
// against the pin: an unversioned engine is how this gate ends up reporting
// two schema failures that read like tk defects. The versions go in the
// failure message, because "0.10.8 does not match 0.11.0" is the whole
// diagnosis and the caller otherwise has no way to see which engine ran.
func installCBM(t *testing.T, h tk) string {
	t.Helper()
	bin := os.Getenv("TK_E2E_CBM_BIN")
	if bin == "" {
		if r := h.run(t, "install", "cbm"); r.code != 0 {
			t.Fatalf("install cbm: %s", r)
		}
		bin = filepath.Join(h.home, "cache", "bin", "codebase-memory-mcp")
	}
	pin := strings.TrimSpace(h.run(t, "config", "get", "cbm_version_pin").stdout)
	got := cbmVersion(t, h, bin)
	if got != pin {
		t.Fatalf("cbm %s at %s does not match config pin %s.\n"+
			"This gate asserts behavior of the pinned engine, so it refuses to run on "+
			"another build: two versions fail differently and the differences look "+
			"like tk bugs.\nUnset TK_E2E_CBM_BIN to install the pin, or point it at a %s build.",
			got, bin, pin, pin)
	}
	t.Logf("cbm %s at %s, matching pin %s (installer verified the checksum)", got, bin, pin)
	return bin
}

// cbmVersion asks a binary for its version under this test's isolated
// environment. A bare probe would inherit the shell's CBM_CACHE_DIR and read
// whatever store it points at instead of this home's.
func cbmVersion(t *testing.T, h tk, bin string) string {
	t.Helper()
	cmd := exec.Command(bin, "--version")
	cmd.Env = childEnv(h)
	b, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s --version: %v", bin, err)
	}
	got := strings.TrimSpace(string(b))
	return strings.TrimSpace(strings.TrimPrefix(got, "codebase-memory-mcp"))
}

func buildTk(t *testing.T, out string) {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatalf("module root: %v", err)
	}
	cmd := exec.Command("go", "build", "-o", out, "./cmd/tk")
	cmd.Dir = root
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go build tk: %v\n%s", err, b)
	}
}

// defPatterns pull one identifier out of a definition line, per language. A
// probe read from the repo is what makes this test reproducible on any
// checkout: no hardcoded symbol list to rot when a file is renamed.
var defPatterns = map[string]*regexp.Regexp{
	".go":   regexp.MustCompile(`^func\s+([A-Z]\w{5,})\s*\(`),
	".py":   regexp.MustCompile(`^def\s+([a-z]\w{5,})\s*\(`),
	".cc":   regexp.MustCompile(`^[A-Za-z_][\w:<>,\s*&]*\s([A-Z]\w{5,})\([^;]*\)\s*(const\s*)?\{`),
	".h":    regexp.MustCompile(`^[A-Za-z_][\w:<>,\s*&]*\s([A-Z]\w{5,})\([^;]*\)\s*(const\s*)?\{`),
	".cpp":  regexp.MustCompile(`^[A-Za-z_][\w:<>,\s*&]*\s([A-Z]\w{5,})\([^;]*\)\s*(const\s*)?\{`),
	".rs":   regexp.MustCompile(`^\s*(?:pub\s+)?fn\s+([a-z]\w{5,})\s*[(<]`),
	".ts":   regexp.MustCompile(`^(?:export\s+)?(?:async\s+)?function\s+([a-z]\w{5,})\s*\(`),
	".js":   regexp.MustCompile(`^(?:export\s+)?(?:async\s+)?function\s+([a-z]\w{5,})\s*\(`),
	".java": regexp.MustCompile(`^\s*(?:public|private|protected)[\w\s<>\[\]]*\s([a-z]\w{5,})\s*\(`),
}

// probeSymbol walks the repo for a definition the engine should have indexed.
// The first candidate that the graph confirms wins, so an unsupported language
// costs one skipped file rather than the whole run.
func probeSymbol(t *testing.T, h tk) string {
	t.Helper()
	tried := map[string]bool{}
	for _, rel := range candidateFiles(h.repo, 4000) {
		pattern, ok := defPatterns[strings.ToLower(filepath.Ext(rel))]
		if !ok {
			continue
		}
		f, err := os.Open(filepath.Join(h.repo, rel))
		if err != nil {
			continue
		}
		var symbol string
		sc := bufio.NewScanner(f)
		for sc.Scan() && symbol == "" {
			if m := pattern.FindStringSubmatch(sc.Text()); m != nil {
				symbol = m[1]
			}
		}
		f.Close()
		if symbol == "" || tried[symbol] {
			continue
		}
		tried[symbol] = true
		r := h.run(t, "find", "--project", h.project, "--query", symbol)
		if r.code == 0 && strings.Contains(r.stdout, symbol) {
			t.Logf("probe symbol %s from %s", symbol, rel)
			return symbol
		}
	}
	t.Fatalf("no symbol read out of %s is present in the index (%d candidates tried); "+
		"the graph does not cover this repo's language", h.repo, len(tried))
	return ""
}

func candidateFiles(root string, limit int) []string {
	var out []string
	skip := map[string]bool{
		".git": true, "node_modules": true, "vendor": true, "third_party": true,
		"bazel-out": true, "dist": true, "build": true,
	}
	_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || len(out) >= limit {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		if d.IsDir() {
			if skip[d.Name()] || strings.HasPrefix(d.Name(), ".") && d.Name() != "." {
				return filepath.SkipDir
			}
			return nil
		}
		base := d.Name()
		if strings.HasSuffix(base, "_test.go") || strings.HasSuffix(base, ".pb.go") ||
			strings.HasSuffix(base, "_test.py") || strings.HasSuffix(base, ".pb.h") ||
			strings.HasSuffix(base, ".inc") {
			return nil
		}
		if info, err := d.Info(); err == nil && info.Size() < 512*1024 {
			out = append(out, rel)
		}
		return nil
	})
	return out
}

func firstQN(t *testing.T, data map[string]any) string {
	t.Helper()
	rule, _ := data["qn_rule"].(string)
	if !strings.Contains(rule, "qn_prefix") {
		t.Fatalf("payload has no qn_rule, so a caller cannot rebuild a name from it: %v", data)
	}
	groups, _ := data["groups"].([]any)
	for _, g := range groups {
		group, _ := g.(map[string]any)
		prefix, _ := group["qn_prefix"].(string)
		rows, _ := group["rows"].([]any)
		for _, r := range rows {
			row, _ := r.([]any)
			name, _ := row[0].(string)
			if strings.TrimSpace(name) == "" {
				continue
			}
			if prefix == "" {
				return name
			}
			return prefix + "." + name
		}
	}
	return ""
}

func (h tk) mcp(t *testing.T, requests ...map[string]any) []map[string]any {
	t.Helper()
	var stdin strings.Builder
	for _, req := range requests {
		b, err := json.Marshal(req)
		if err != nil {
			t.Fatalf("marshal request: %v", err)
		}
		stdin.Write(b)
		stdin.WriteByte('\n')
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, h.bin, "mcp")
	cmd.Env = childEnv(h)
	cmd.Stdin = strings.NewReader(stdin.String())
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("tk mcp: %v", err)
	}
	var replies []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var reply map[string]any
		if err := json.Unmarshal([]byte(line), &reply); err != nil {
			t.Fatalf("mcp reply is not JSON: %v\n%s", err, line)
		}
		if e, ok := reply["error"]; ok {
			t.Fatalf("mcp error reply: %v", e)
		}
		replies = append(replies, reply)
	}
	if len(replies) != len(requests) {
		t.Fatalf("got %d mcp replies for %d requests", len(replies), len(requests))
	}
	return replies
}

func out(t *testing.T, dir, name string, args ...string) string {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	b, _ := cmd.Output()
	return string(b)
}

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	return out(t, dir, "git", append([]string{"-C", dir}, args...)...)
}

func env2(t *testing.T, key, fallback string) string {
	t.Helper()
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
