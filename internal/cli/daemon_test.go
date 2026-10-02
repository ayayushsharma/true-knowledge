package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/ayayushsharma/true-knowledge/internal/backends"
)

// daemonVerbScript is a CBM stand-in whose control-plane answers are placed by
// the caller: `busy` decides whether stop refuses, `where` picks the stream a
// refusal lands on, and `running` decides what status reports. All three are
// load-bearing, because the bug this file pins was that tk decided from stdout
// alone and so could not tell a refusal from an answer no matter which stream
// carried it.
func daemonVerbScript(log, marker string, running, busy, refuseOnStdout bool) string {
	refusal := "daemon: refusing to stop; committed clients: 4242 5150"
	redir := ">&2"
	if refuseOnStdout {
		redir = ""
	}
	statusBody := `echo "daemon: not running"; exit 1`
	if running {
		statusBody = `echo "daemon: active (permanent)"; echo "  pid: 13482"; echo "  committed clients: 0"`
	}
	stopBody := `rm -f "` + marker + `.running"; echo "daemon stopped"`
	if busy {
		stopBody = `echo "` + refusal + `" ` + redir + `; exit 1`
	}
	return `#!/bin/sh
echo "$*" >> "` + log + `"
case "$1" in
  --version) echo "codebase-memory-mcp probe"; exit 0 ;;
  daemon)
    case "$2" in
      status) ` + statusBody + ` ;;
      stop) ` + stopBody + ` ;;
    esac ;;
esac
exit 0
`
}

// installDaemonVerb places the script where cbmresolve looks for the managed
// binary, so the verb under test goes through the real spawn wrapper rather than
// a stubbed one.
func installDaemonVerb(t *testing.T, script string) string {
	t.Helper()
	home := t.TempDir()
	bin := filepath.Join(home, "cache", "bin")
	if err := os.MkdirAll(bin, 0o700); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(bin, backends.CBM().BinaryName(backends.HostGOOS()))
	if err := os.WriteFile(dest, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return home
}

func daemonCmd(t *testing.T, home string, args ...string) (string, error) {
	t.Helper()
	out := &bytes.Buffer{}
	cmd := cmdDaemon(&Globals{Home: home})
	cmd.SilenceUsage = true
	cmd.SilenceErrors = true
	cmd.SetOut(out)
	cmd.SetErr(out)
	cmd.SetArgs(args)
	err := cmd.Execute()
	return out.String(), err
}

// A daemon that is not running answers the question. `daemon status` exits 1
// when stopped, so the reply arrives on a nonzero exit and must still exit 0.
func TestDaemonStatusNotRunningIsAnAnswer(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake daemon is a sh script")
	}
	marker := filepath.Join(t.TempDir(), "cbm")
	home := installDaemonVerb(t, daemonVerbScript(filepath.Join(t.TempDir(), "log"), marker, false, false, false))

	out, err := daemonCmd(t, home, "status")
	if err != nil {
		t.Fatalf(`"daemon: not running" is a reply, not a fault: %v\n%s`, err, out)
	}
	if !strings.Contains(out, "not running") {
		t.Errorf("the engine's own wording must reach the reader:\n%s", out)
	}
}

// A refusal must exit nonzero, whichever stream carries it. Before the fix the
// stdout variant exited 0, so a caller scripted on `tk daemon stop` saw a
// success while the daemon was still holding clients.
func TestDaemonStopRefusalFailsOnEitherStream(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake daemon is a sh script")
	}
	for _, onStdout := range []bool{true, false} {
		name := "stderr"
		if onStdout {
			name = "stdout"
		}
		t.Run(name, func(t *testing.T) {
			marker := filepath.Join(t.TempDir(), "cbm")
			if err := os.WriteFile(marker+".running", nil, 0o600); err != nil {
				t.Fatal(err)
			}
			home := installDaemonVerb(t, daemonVerbScript(filepath.Join(t.TempDir(), "log"), marker, true, true, onStdout))

			out, err := daemonCmd(t, home, "stop")
			if err == nil {
				t.Fatalf("a refused stop must exit nonzero; output:\n%s", out)
			}
			// The pids are the fix, so they must survive to the reader intact.
			if !strings.Contains(out, "4242") || !strings.Contains(out, "5150") {
				t.Errorf("the committed client pids must reach stdout:\n%s", out)
			}
			if _, serr := os.Stat(marker + ".running"); serr != nil {
				t.Error("a refused stop must leave the daemon running")
			}
		})
	}
}

// A shape tk does not recognise fails closed. Treating "not the answer" as
// "the answer" is how a reworded refusal turns into a silent success.
func TestDaemonStopUnrecognisedShapeFailsClosed(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake daemon is a sh script")
	}
	home := installDaemonVerb(t, `#!/bin/sh
case "$1" in
  --version) echo "codebase-memory-mcp probe"; exit 0 ;;
  daemon)
    case "$2" in
      stop) echo "daemon: busy, see the dashboard" ; exit 1 ;;
    esac ;;
esac
exit 0
`)
	out, err := daemonCmd(t, home, "stop")
	if err == nil {
		t.Fatalf("a nonzero exit tk cannot classify must not read as success:\n%s", out)
	}
}

// --json must not claim ok:true on a refusal. A machine reading the envelope
// and a shell reading $? are the two ways a caller learns the command failed.
func TestDaemonStopRefusalJSONEnvelopeIsNotOK(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake daemon is a sh script")
	}
	marker := filepath.Join(t.TempDir(), "cbm")
	if err := os.WriteFile(marker+".running", nil, 0o600); err != nil {
		t.Fatal(err)
	}
	home := installDaemonVerb(t, daemonVerbScript(filepath.Join(t.TempDir(), "log"), marker, true, true, true))

	out := &bytes.Buffer{}
	cmd := cmdDaemon(&Globals{Home: home, JSON: true})
	cmd.SilenceUsage = true
	cmd.SilenceErrors = true
	cmd.SetOut(out)
	cmd.SetErr(out)
	cmd.SetArgs([]string{"stop"})
	if err := cmd.Execute(); err == nil {
		t.Fatalf("a refused stop must exit nonzero; output:\n%s", out)
	}
	var env struct {
		OK   bool   `json:"ok"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(out.Bytes(), &env); err != nil {
		t.Fatalf("envelope: %v\n%s", err, out)
	}
	if env.OK {
		t.Error("ok:true on a refusal tells a machine the stop worked:\n" + out.String())
	}
	if !strings.Contains(env.Text, "4242") {
		t.Errorf("the refusal text must reach the envelope:\n%s", out)
	}
}

// The two happy paths still exit 0.
func TestDaemonStopSuccessExitsZero(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake daemon is a sh script")
	}
	marker := filepath.Join(t.TempDir(), "cbm")
	if err := os.WriteFile(marker+".running", nil, 0o600); err != nil {
		t.Fatal(err)
	}
	home := installDaemonVerb(t, daemonVerbScript(filepath.Join(t.TempDir(), "log"), marker, true, false, false))

	out, err := daemonCmd(t, home, "stop")
	if err != nil {
		t.Fatalf("a clean stop must exit 0: %v\n%s", err, out)
	}
	if _, serr := os.Stat(marker + ".running"); !os.IsNotExist(serr) {
		t.Error("the daemon should be stopped")
	}

	out, err = daemonCmd(t, home, "status")
	if err != nil {
		t.Fatalf("status on a live daemon must exit 0: %v\n%s", err, out)
	}
	if !strings.Contains(out, "active") {
		t.Errorf("a live daemon must read as active:\n%s", out)
	}
}
