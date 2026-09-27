package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// failingScriptServer serves a backend installer that fails the way a real one
// does: progress on stdout, the cause on stderr, non-zero exit.
func failingScriptServer(t *testing.T) *httptest.Server {
	t.Helper()
	body := "#!/usr/bin/env bash\n" +
		"echo 'Downloading codebase-memory-mcp-linux-amd64-portable.tar.gz...'\n" +
		"echo 'tar: install.sh: Cannot write: Disk quota exceeded' >&2\n" +
		"exit 2\n"
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "install.sh") || strings.HasSuffix(r.URL.Path, "install.ps1") {
			fmt.Fprint(w, body)
			return
		}
		http.NotFound(w, r)
	})
	s := httptest.NewServer(mux)
	t.Cleanup(s.Close)
	return s
}

func runInstall(t *testing.T, home string, asJSON bool) (string, error) {
	t.Helper()
	out := &bytes.Buffer{}
	g := &Globals{Home: home, JSON: asJSON}
	cmd := cmdInstall(g)
	// The root silences both, so a bare subcommand must be judged the way the
	// real command runs: the error on the return value, not reprinted with the
	// usage block.
	cmd.SilenceUsage = true
	cmd.SilenceErrors = true
	cmd.SetOut(out)
	cmd.SetErr(out)
	cmd.SetArgs([]string{"cbm"})
	err := cmd.Execute()
	return out.String(), err
}

// TestInstallFailureExitsNonZero: `tk install` used to print FAILED, continue,
// and exit 0. A caller that checks $? — the real e2e gate does exactly that —
// was told the install worked, and went looking for the missing binary
// somewhere else entirely.
func TestInstallFailureExitsNonZero(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake script is bash")
	}
	s := failingScriptServer(t)
	t.Setenv("TK_SCRIPT_BASE_URL", s.URL)

	out, err := runInstall(t, t.TempDir(), false)
	if err == nil {
		t.Fatalf("a failed backend install must exit non-zero; output was:\n%s", out)
	}
	if !strings.Contains(out, "FAILED") {
		t.Errorf("human face lost the failure line:\n%s", out)
	}
	// The vendor's own words are the diagnosis; a bare exit status is not.
	if !strings.Contains(out, "Disk quota exceeded") {
		t.Errorf("human face lost the backend's diagnostic:\n%s", out)
	}
}

// The two faces must agree. --json is how a machine learns what happened, so an
// envelope that says ok:true next to a non-zero exit is a lie one of the two
// callers always believes.
func TestInstallFailureJSONFaceSaysNotOK(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake script is bash")
	}
	s := failingScriptServer(t)
	t.Setenv("TK_SCRIPT_BASE_URL", s.URL)

	out, err := runInstall(t, t.TempDir(), true)
	if err == nil {
		t.Fatalf("a failed backend install must exit non-zero; output was:\n%s", out)
	}
	var env struct {
		OK       bool `json:"ok"`
		Text     string
		Backends []struct {
			Backend string `json:"backend"`
			Status  string `json:"status"`
			Detail  string `json:"detail"`
		} `json:"backends"`
	}
	if jerr := json.Unmarshal([]byte(out), &env); jerr != nil {
		t.Fatalf("envelope is not JSON: %v\n%s", jerr, out)
	}
	if env.OK {
		t.Errorf("envelope claims ok:true on a failed install:\n%s", out)
	}
	if len(env.Backends) != 1 || env.Backends[0].Status != "failed" {
		t.Fatalf("backends = %+v", env.Backends)
	}
	if !strings.Contains(env.Backends[0].Detail, "Disk quota exceeded") {
		t.Errorf("row lost the backend's diagnostic: %q", env.Backends[0].Detail)
	}
}

// TestInstallDoesNotPinOnFailure: a failed install that still wrote the pin
// would make the next run report "up-to-date" against a binary that is not
// there, and the pin would outlive the failure that caused it.
func TestInstallDoesNotPinOnFailure(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake script is bash")
	}
	s := failingScriptServer(t)
	t.Setenv("TK_SCRIPT_BASE_URL", s.URL)
	home := t.TempDir()

	if _, err := runInstall(t, home, false); err == nil {
		t.Fatal("expected a non-zero exit")
	}
	if _, err := os.Stat(filepath.Join(home, "config", "config.json")); err == nil {
		raw, rerr := os.ReadFile(filepath.Join(home, "config", "config.json"))
		if rerr == nil && strings.Contains(string(raw), "cbm_version_pin") {
			t.Errorf("a failed install pinned a version:\n%s", raw)
		}
	}
}

// A successful install is the control for both faces above: if ok:true and
// exit 0 only ever came from the failure path, the assertions would pass with a
// command that cannot install anything.
func TestInstallSuccessKeepsExitZero(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake script is bash")
	}
	body := "#!/usr/bin/env bash\n" +
		"set -euo pipefail\n" +
		"for arg in \"$@\"; do case \"$arg\" in --dir=*) dir=\"${arg#--dir=}\" ;; esac; done\n" +
		"mkdir -p \"$dir\"\n" +
		"printf '#!/bin/sh\\necho cbm 0.11.0 fake\\n' > \"$dir/codebase-memory-mcp\"\n" +
		"chmod 755 \"$dir/codebase-memory-mcp\"\n"
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, body)
	})
	s := httptest.NewServer(mux)
	t.Cleanup(s.Close)
	t.Setenv("TK_SCRIPT_BASE_URL", s.URL)
	home := t.TempDir()

	out, err := runInstall(t, home, false)
	if err != nil {
		t.Fatalf("a successful install must exit 0: %v\n%s", err, out)
	}
	if !strings.Contains(out, "installed") {
		t.Errorf("human face lost the installed line:\n%s", out)
	}
	if _, err := os.Stat(filepath.Join(home, "cache", "bin", "codebase-memory-mcp")); err != nil {
		t.Errorf("binary not installed into the cache: %v", err)
	}
}
