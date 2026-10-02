package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/ayayushsharma/true-knowledge/internal/config"
	"github.com/ayayushsharma/true-knowledge/internal/logx"
	"github.com/ayayushsharma/true-knowledge/internal/paths"
)

// captureStderr points the process's stderr — the real one, which is what
// internal/logx and internal/progress resolve at run time — at a pipe for the
// duration of one call.
//
// The alternative is injecting a writer, and that would test a wiring that
// production never takes: the whole claim is that the diagnostics land on
// os.Stderr and nowhere else, so the test has to use os.Stderr.
func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	orig := os.Stderr
	os.Stderr = w
	logx.SetOutput(w)
	logx.SetLevel(logx.DefaultLevel)
	// currentSinks is deliberately left alone: NewRoot already built it, and it
	// is the buffer finalize writes the diag tee into. Replacing it here would
	// send the progress trace to a buffer nobody reads.
	t.Cleanup(func() {
		os.Stderr = orig
		logx.SetOutput(orig)
	})

	fn()

	os.Stderr = orig
	logx.SetOutput(orig)
	_ = w.Close()
	out, _ := io.ReadAll(r)
	_ = r.Close()
	return string(out)
}

// runInstallSplit runs `tk install cbm` and returns its two channels separately.
// Merging them into one buffer cannot tell a correct command from one that only
// works because both streams land in the same place.
//
// The global flags go through root's real flag parsing rather than being
// pre-set on a Globals: NewRoot rebinds every persistent flag to the struct it
// is handed, so a pre-set Home is overwritten by the flag default and the command
// installs into the developer's real ~/.cache. Passing --home as an argument is
// both the faithful path and the only safe one.
func runInstallSplit(t *testing.T, home string, flags ...string) (stdout, stderr string, err error) {
	t.Helper()
	out := &bytes.Buffer{}
	root := NewRoot(&Globals{})
	// Replace the root's stdout tee after construction: the point is to read
	// what the command actually writes, not what NewRoot pre-wired.
	root.SetOut(out)
	root.SetErr(out)
	root.SetArgs(append(append([]string{"--home", home}, flags...), "install", "cbm"))
	stderr = captureStderr(t, func() { err = root.Execute() })
	// Asserted after the run, not before: the flag is only bound once cobra has
	// parsed it. If this fails the command just ran against the developer's real
	// ~/.cache, which is the whole hazard.
	if got := root.PersistentFlags().Lookup("home").Value.String(); got != home {
		t.Fatalf("isolation lost: --home bound to %q, want %q", got, home)
	}
	return out.String(), stderr, err
}

// The whole point: stdout carries the answer and nothing else. Every stage of a
// 340 MB install is on stderr, so `--json | jq` yields a parseable envelope and
// a redirected stdout is never a half-written progress bar.
func TestInstallKeepsProgressOffStdout(t *testing.T) {
	fakeRelease(t, config.DefaultCBMPin, false)
	stdout, stderr, err := runInstallSplit(t, t.TempDir(), "--json")
	if err != nil {
		t.Fatalf("install: %v\n%s", err, stderr)
	}
	var env map[string]any
	if jerr := json.Unmarshal([]byte(stdout), &env); jerr != nil {
		t.Fatalf("stdout is not a JSON envelope: %v\nstdout:\n%s\nstderr:\n%s", jerr, stdout, stderr)
	}
	if env["ok"] != true {
		t.Fatalf("envelope lost the verdict: %s", stdout)
	}
	// And the stages must actually be on the other channel, or the rule is
	// satisfied by an install that says nothing at all.
	for _, want := range []string{"resolve", "checksum manifest", "download", "verify sha256", "extract", "validate", "publish"} {
		if !strings.Contains(stderr, want) {
			t.Fatalf("progress step %q never reached stderr:\n%s", want, stderr)
		}
	}
}

// --json does not silence the narrative. These are two channels; muting the
// story on the machine path would leave the run that most needs diagnosing — an
// automated install — with the least evidence.
func TestProgressSurvivesJSONMode(t *testing.T) {
	fakeRelease(t, config.DefaultCBMPin, false)
	stdout, stderr, err := runInstallSplit(t, t.TempDir(), "--json")
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	if !strings.Contains(stderr, "resolve") {
		t.Fatalf("--json must not mute the diagnostic channel:\n%s", stderr)
	}
	if strings.Contains(stdout, "resolve") {
		t.Fatalf("progress leaked onto stdout:\n%s", stdout)
	}
}

// Every stage of an install is a stage that can fail, and a reader deciding
// where to look needs the list complete: resolve, manifest, download, verify,
// extract, staged validation, swap, published validation.
func TestInstallReportsEveryStage(t *testing.T) {
	fakeRelease(t, config.DefaultCBMPin, false)
	_, stderr, err := runInstallSplit(t, t.TempDir())
	if err != nil {
		t.Fatalf("install: %v\n%s", err, stderr)
	}
	for _, want := range []string{
		"resolve codebase-memory-mcp",
		"fetch checksum manifest",
		"download codebase-memory-mcp",
		"verify sha256",
		"extract codebase-memory-mcp",
		"validate staged candidate",
		"publish ",
		"validate published binary",
	} {
		if !strings.Contains(stderr, want) {
			t.Fatalf("missing stage %q in the install trace:\n%s", want, stderr)
		}
	}
}

// A first install holds nothing open, and saying so is what stops an operator
// wondering whether a daemon was missed. Silence here is indistinguishable from
// a stage that was skipped.
func TestInstallSaysWhenNothingIsQuiesced(t *testing.T) {
	fakeRelease(t, config.DefaultCBMPin, false)
	_, stderr, err := runInstallSplit(t, t.TempDir())
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	if !strings.Contains(stderr, "nothing holding the old image") {
		t.Fatalf("a first install must report that no quiesce was needed:\n%s", stderr)
	}
}

// A failed install is exactly the run where the stages matter. The trace has to
// survive the failure — the table is the diagnosis, and the trace says where it
// stopped.
func TestFailureStillReportsStages(t *testing.T) {
	fakeRelease(t, config.DefaultCBMPin, true) // corrupt checksum
	_, stderr, err := runInstallSplit(t, t.TempDir())
	if err == nil {
		t.Fatal("a failed install must exit non-zero")
	}
	if !strings.Contains(stderr, "download") {
		t.Fatalf("a failed install lost its progress trace:\n%s", stderr)
	}
	// Nothing may be published after a checksum refusal, and the trace must not
	// claim otherwise.
	if strings.Contains(stderr, "publish ") {
		t.Fatalf("nothing may be published after a checksum mismatch:\n%s", stderr)
	}
}

// --quiet is the escape hatch for the one caller that cannot tolerate a
// diagnostic: a script whose stderr is another program's stdin. It reduces the
// channel to errors and silences the step reporter — and it must not touch what
// lands on stdout, because it is a statement about the diagnostic channel only.
func TestQuietSilencesProgress(t *testing.T) {
	fakeRelease(t, config.DefaultCBMPin, false)
	stdout, stderr, err := runInstallSplit(t, t.TempDir(), "--quiet")
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	if strings.Contains(stderr, "resolve") || strings.Contains(stderr, "download") {
		t.Fatalf("--quiet must silence the step reporter:\n%s", stderr)
	}
	if !strings.Contains(stdout, "installed") {
		t.Fatalf("--quiet must not change the answer:\n%s", stdout)
	}
}

// The flags have to exist on the real command tree and both sides have to be
// wired: --verbose raises the level, --quiet lowers it, and load() reads both
// fields. A flag dropped from root is a silent feature.
func TestDiagnosticFlagsAreRegistered(t *testing.T) {
	root := NewRoot(&Globals{Home: t.TempDir()})
	for _, name := range []string{"verbose", "quiet"} {
		if root.PersistentFlags().Lookup(name) == nil {
			t.Fatalf("--%s is not a persistent flag; load() reads Globals.%s", name, name)
		}
	}
}

// A long-lived process must not install the invocation record tee. finalize
// drains that buffer at exit, and `tk mcp` never exits while the client is
// connected — so a tee would grow for the length of the session with nothing
// reading it. The MCP server writes its own per-call record to tk.log instead.
func TestLongLivedInstallsNoRecordTee(t *testing.T) {
	var sink bytes.Buffer
	logx.SetOutput(&sink)
	logx.SetRecord(nil)
	logx.SetLevel(logx.LevelDebug)
	t.Cleanup(func() {
		logx.SetOutput(io.Discard)
		logx.SetRecord(nil)
		logx.SetLevel(logx.DefaultLevel)
	})

	// The failure this guards against is silent, so it is asserted where the
	// decision is made rather than through `tk mcp` end to end.
	g := Globals{Home: t.TempDir(), LongLived: true}
	old := currentSinks
	currentSinks = &sinks{out: &bytes.Buffer{}, diag: &bytes.Buffer{}}
	t.Cleanup(func() { currentSinks = old })

	if _, err := load(g); err != nil {
		t.Fatalf("load: %v", err)
	}
	if got := logx.RecordSet(); got {
		t.Fatal("a long-lived process installed the invocation record tee; the buffer grows for the whole session")
	}
	// It still narrates on stderr — that is what --verbose is for.
	logx.Debugf("mcp call served")
	if !strings.Contains(sink.String(), "mcp call served") {
		t.Fatalf("stderr narration must survive: %q", sink.String())
	}
}

// The mirror image: an invocation-scoped command does install the tee.
func TestInvocationInstallsTheRecordTee(t *testing.T) {
	logx.SetOutput(io.Discard)
	logx.SetLevel(logx.DefaultLevel)
	old := currentSinks
	currentSinks = &sinks{out: &bytes.Buffer{}, diag: &bytes.Buffer{}}
	t.Cleanup(func() {
		currentSinks = old
		logx.SetRecord(nil)
	})

	if _, err := load(Globals{Home: t.TempDir()}); err != nil {
		t.Fatalf("load: %v", err)
	}
	if !logx.RecordSet() {
		t.Fatal("an invocation-scoped command lost its progress trace in tk.log")
	}
}

// The trace is teed into tk.log, so an install whose narration scrolled past is
// still answerable afterwards. Without it tk.log and the terminal disagree about
// what happened.
func TestProgressReachesTkLog(t *testing.T) {
	fakeRelease(t, config.DefaultCBMPin, false)
	home := t.TempDir()
	_, stderr, err := runInstallSplit(t, home)
	if err != nil {
		t.Fatalf("install: %v\n%s", err, stderr)
	}
	raw, rerr := os.ReadFile(paths.Resolve(home).LogFile())
	if rerr != nil {
		t.Fatalf("tk.log missing: %v", rerr)
	}
	for _, want := range []string{"fetch checksum manifest", "validate published binary"} {
		if !strings.Contains(string(raw), want) {
			t.Fatalf("tk.log lost the progress trace %q:\n%s", want, raw)
		}
	}
}

// No numbered step may ever exceed its declared total. `[10/9]` is the specific
// failure a step reporter with an environment-dependent header produces, and it
// is worse than printing no position at all: a reader trusts the denominator.
func TestInstallStepCountIsExact(t *testing.T) {
	fakeRelease(t, config.DefaultCBMPin, false)
	_, stderr, err := runInstallSplit(t, t.TempDir())
	if err != nil {
		t.Fatalf("install: %v\n%s", err, stderr)
	}
	n, total := 0, 0
	for _, line := range strings.Split(stderr, "\n") {
		if !strings.HasPrefix(line, "[") {
			continue
		}
		close := strings.Index(line, "]")
		if close < 0 {
			continue
		}
		haveN, haveTotal := 0, 0
		if _, err := fmt.Sscanf(line[1:close], "%d/%d", &haveN, &haveTotal); err != nil {
			continue
		}
		n, total = haveN, haveTotal
	}
	if total == 0 {
		t.Fatalf("no numbered steps at all:\n%s", stderr)
	}
	if n != total {
		t.Fatalf("steps end at %d of %d:\n%s", n, total, stderr)
	}
}
