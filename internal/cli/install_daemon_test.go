package cli

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/ayayushsharma/true-knowledge/internal/backends"
	"github.com/ayayushsharma/true-knowledge/internal/config"
)

// fakeDaemon is a script standing in for CBM, installed as the managed binary
// so cbmresolve finds it exactly as it finds the real one. It models the parts
// of the daemon contract this code depends on: status exits 1 when stopped,
// stop is idempotent, and stop REFUSES while a client is committed.
type fakeDaemon struct {
	dir    string // the managed bin dir, holding the daemon's state files
	log    string // one line per invocation, in order
	mu     sync.Mutex
	calls  []string
	busy   bool // stop refuses, naming this pid
	active bool // status reports running
}

// image is the fake binary: it answers --version AND speaks the daemon
// protocol, because the real one does both and a replacement runs the new
// image to bring the daemon back.
func (d *fakeDaemon) image(version string) []byte {
	return []byte(d.script(version))
}

func (d *fakeDaemon) script(version string) string {
	busyBody := `echo "daemon: refusing to stop; committed clients: 4242 5150" >&2
        exit 1`
	idleBody := fmt.Sprintf(`rm -f %q
        echo "daemon stopped"`, d.running())
	body := idleBody
	if d.busy {
		// The refusal tk must not override: it names the committed clients
		// and changes nothing.
		body = busyBody
	}
	return fmt.Sprintf(`#!/bin/sh
echo "$*" >> %q
case "$1 $2" in
  "--version ") echo "codebase-memory-mcp %s"; exit 0 ;;
esac
case "$1" in
  daemon)
    case "$2" in
      status)
        if [ -f %q ]; then
          echo "daemon: active (permanent)"
          echo "  pid: 4242"
          echo "  build: %s (a831cdcafaed...)"
          echo "  committed clients: 0"
          exit 0
        fi
        echo "daemon: not running"; exit 1 ;;
      stop)
        if [ -f %q ]; then
        %s
        fi ;;
      start) touch %q; echo "daemon started"; exit 0 ;;
    esac ;;
esac
exit 0
`, d.log, version, d.running(), version, d.running(), body, d.running())
}

func (d *fakeDaemon) running() string { return filepath.Join(d.dir, "daemon.running") }

// install puts the fake at the managed path tk will be replacing.
func (d *fakeDaemon) install(t *testing.T, home string) {
	t.Helper()
	d.dir = filepath.Join(home, "cache", "bin")
	if err := os.MkdirAll(d.dir, 0o700); err != nil {
		t.Fatal(err)
	}
	d.log = filepath.Join(t.TempDir(), "daemon.log")
	if err := os.WriteFile(d.dest(home), d.image(config.DefaultCBMPin), 0o755); err != nil {
		t.Fatal(err)
	}
	if d.active {
		if err := os.WriteFile(d.running(), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func (d *fakeDaemon) dest(home string) string {
	return filepath.Join(d.dir, backends.CBM().BinaryName(backends.HostGOOS()))
}

func (d *fakeDaemon) invoked() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	raw, _ := os.ReadFile(d.log)
	var out []string
	for _, l := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		if l != "" {
			out = append(out, l)
		}
	}
	return out
}

func (d *fakeDaemon) called(want string) bool {
	for _, c := range d.invoked() {
		if strings.Contains(c, want) {
			return true
		}
	}
	return false
}

func (d *fakeDaemon) isRunning() bool {
	_, err := os.Stat(d.running())
	return err == nil
}

// servingRelease publishes a release whose handler records whether the daemon
// was still up at the moment bytes were handed over. That is the property the
// whole quiesce sequence exists to guarantee, so it is asserted at the only
// point that cannot be faked afterwards.
func servingRelease(t *testing.T, d *fakeDaemon, whenFetched func()) {
	t.Helper()
	goos, goarch := backends.HostGOOS(), backends.HostGOARCH()
	archive, err := backends.CBM().Archive(goos, goarch)
	if err != nil {
		t.Skipf("no fake release for %s/%s: %v", goos, goarch, err)
	}
	body := d.image(config.DefaultCBMPin)
	var blob []byte
	if strings.HasSuffix(archive, ".zip") {
		blob = zipBytes(t, backends.CBM().BinaryName(goos), body)
	} else {
		blob = tarBytes(t, backends.CBM().BinaryName(goos), body)
	}
	sum := sha256.Sum256(blob)
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "checksums.txt"):
			fmt.Fprintf(w, "%s  %s\n", hex.EncodeToString(sum[:]), archive)
		case strings.HasSuffix(r.URL.Path, archive):
			whenFetched()
			w.Write(blob)
		default:
			http.NotFound(w, r)
		}
	})
	s := httptest.NewServer(mux)
	t.Cleanup(s.Close)
	t.Setenv("TK_RELEASE_BASE_URL", s.URL)
}

func installCmd(t *testing.T, home string, args ...string) (string, error) {
	t.Helper()
	out := &bytes.Buffer{}
	cmd := cmdInstall(&Globals{Home: home})
	cmd.SilenceUsage = true
	cmd.SilenceErrors = true
	cmd.SetOut(out)
	cmd.SetErr(out)
	cmd.SetArgs(args)
	err := cmd.Execute()
	return out.String(), err
}

// A replacement with a live daemon: stop first, then fetch, then start again.
// A tk that swapped first would leave the new binary unadmitted by the old
// daemon, so the stop is asserted to have happened before any bytes moved.
func TestInstallQuiescesALiveDaemonBeforeReplacing(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake daemon is a sh script")
	}
	home := t.TempDir()
	d := &fakeDaemon{active: true}
	d.install(t, home)

	var runningAtFetch bool
	servingRelease(t, d, func() { runningAtFetch = d.isRunning() })
	// The image the fake reports is the pin, so the replacement is a real
	// rewrite of the same version -- the daemon must still be quiesced.
	out, err := installCmd(t, home, "cbm", "--update")
	if err != nil {
		t.Fatalf("install: %v\n%s", err, out)
	}
	if runningAtFetch {
		t.Error("archive bytes were fetched while the daemon still held the binary")
	}
	if !d.called("daemon stop") {
		t.Errorf("a replacement must stop the daemon; calls: %v", d.invoked())
	}
	if !d.called("daemon start") {
		t.Errorf("a daemon that was running must come back; calls: %v\n%s", d.invoked(), out)
	}
	if !d.isRunning() {
		t.Error("the daemon should be running again on the new image")
	}
}

// The refusal is the whole safety property. Swapping past it would leave a
// binary the old daemon refuses to admit, so tk must not fetch, must not
// write, and must exit non-zero naming the clients.
func TestInstallRefusesWhenTheDaemonWillNotStop(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake daemon is a sh script")
	}
	home := t.TempDir()
	d := &fakeDaemon{active: true, busy: true}
	d.install(t, home)

	fetched := false
	servingRelease(t, d, func() { fetched = true })
	before, err := os.ReadFile(d.dest(home))
	if err != nil {
		t.Fatal(err)
	}
	out, err := installCmd(t, home, "cbm", "--update")
	if err == nil {
		t.Fatalf("a refused stop must fail the install; output:\n%s", out)
	}
	if fetched {
		t.Error("tk fetched a replacement past a daemon that refused to stop")
	}
	if !d.isRunning() {
		t.Error("the refused stop must leave the daemon running")
	}
	after, err := os.ReadFile(d.dest(home))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Error("the managed binary was modified despite the refusal")
	}
	if !strings.Contains(out, "FAILED") {
		t.Errorf("human face lost the failure line:\n%s", out)
	}
	// The pids CBM printed are the fix, so they have to reach the reader.
	if !strings.Contains(out, "4242") {
		t.Errorf("the committed client pids must survive to the output:\n%s", out)
	}
	if d.called("daemon start") {
		t.Errorf("a refused stop must not be followed by a start: %v", d.invoked())
	}
}

// A first install has no image to replace, so it must not start a permanent
// daemon that nothing asked for.
func TestInstallFirstRunStartsNoDaemon(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake daemon is a sh script")
	}
	home := t.TempDir()
	d := &fakeDaemon{}
	d.dir = filepath.Join(home, "cache", "bin")
	if err := os.MkdirAll(d.dir, 0o700); err != nil {
		t.Fatal(err)
	}
	d.log = filepath.Join(t.TempDir(), "daemon.log")
	servingRelease(t, d, func() {})

	if _, err := installCmd(t, home, "cbm"); err != nil {
		t.Fatalf("first install: %v", err)
	}
	if d.called("daemon") {
		t.Errorf("a first install has nothing to quiesce; calls: %v", d.invoked())
	}
}

// An already-current binary is a no-op, so a running daemon keeps running.
func TestInstallUpToDateLeavesADaemonAlone(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake daemon is a sh script")
	}
	home := t.TempDir()
	d := &fakeDaemon{active: true}
	d.install(t, home)
	servingRelease(t, d, func() {})

	out, err := installCmd(t, home, "cbm")
	if err != nil {
		t.Fatalf("an up-to-date install must exit 0: %v\n%s", err, out)
	}
	if !strings.Contains(out, "up-to-date") {
		t.Errorf("want an up-to-date no-op:\n%s", out)
	}
	if d.called("daemon stop") {
		t.Errorf("an up-to-date no-op must not stop anything; calls: %v", d.invoked())
	}
	if !d.isRunning() {
		t.Error("the daemon must be left running")
	}
}

// --check is a report. It must answer without touching the daemon.
func TestInstallCheckTouchesNoDaemon(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake daemon is a sh script")
	}
	home := t.TempDir()
	d := &fakeDaemon{active: true}
	d.install(t, home)
	servingRelease(t, d, func() {})

	if _, err := installCmd(t, home, "--check"); err != nil {
		t.Fatalf("--check must exit 0: %v", err)
	}
	if d.called("daemon") {
		t.Errorf("--check must not talk to the daemon; calls: %v", d.invoked())
	}
}

// The real `daemon status` output, captured from CBM v0.11.0. tk parses prose
// rather than an exit code, so the shape is a contract: if a CBM release
// rewords it, this test is where that shows up instead of in a field report
// where an install silently stops no daemon.
func TestDaemonStatusProse(t *testing.T) {
	const live = "daemon: active (permanent)\n" +
		"  pid: 13482\n" +
		"  build: 0.11.0 (a831cdcafaed...)\n" +
		"  committed clients: 0\n" +
		"  ui: configured at http://127.0.0.1:9749 (readiness not checked)\n"
	const down = "daemon: not running\n" +
		"hint: `codebase-memory-mcp daemon start` keeps a daemon warm so CLI commands and hooks skip the per-command startup cost.\n"

	if !daemonActive(live) {
		t.Error("a live daemon must read as active")
	}
	if daemonActive(down) {
		t.Error(`"daemon: not running" must not read as active`)
	}
	if got := daemonPid(live); got != "13482" {
		t.Errorf("pid = %q, want 13482 — the announcement names the wrong process", got)
	}
	if got := daemonPid(down); got != "" {
		t.Errorf("pid = %q, want empty when nothing is running", got)
	}
	// An unrecognised shape must still yield a usable result: refusing to stop
	// because tk could not parse a pid would be a worse failure than stopping
	// without one.
	if got := daemonPid("daemon: active (something new)"); got != "" {
		t.Errorf("pid = %q, want empty rather than a wrong number", got)
	}
}

func TestQuiesceNoteNamesThePid(t *testing.T) {
	if got := (Quiesce{Was: true, Pid: "4242"}).Note(); !strings.Contains(got, "4242") {
		t.Errorf("note must name the pid it stopped: %q", got)
	}
	if got := (Quiesce{Was: true}).Note(); strings.Contains(got, "pid") {
		t.Errorf("no pid parsed must not print an empty one: %q", got)
	}
}
