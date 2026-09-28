package cli

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ayayushsharma/true-knowledge/internal/backends"
	"github.com/ayayushsharma/true-knowledge/internal/config"
)

// fakeRelease serves a CBM release the way GitHub does — a tag-scoped
// checksums.txt plus one archive — so the CLI is exercised over the same
// network path a real install uses. badSum makes the manifest vouch for bytes
// that are not the bytes served, which is what a mirror does.
func fakeRelease(t *testing.T, version string, badSum bool) string {
	t.Helper()
	goos, goarch := backends.HostGOOS(), backends.HostGOARCH()
	archive, err := backends.CBM().Archive(goos, goarch)
	if err != nil {
		t.Skipf("no fake release for %s/%s: %v", goos, goarch, err)
	}
	body := fakeImage(version)
	var blob []byte
	if strings.HasSuffix(archive, ".zip") {
		blob = zipBytes(t, backends.CBM().BinaryName(goos), body)
	} else {
		blob = tarBytes(t, backends.CBM().BinaryName(goos), body)
	}
	sum := sha256.Sum256(blob)
	digest := hex.EncodeToString(sum[:])
	if badSum {
		digest = strings.Repeat("0", 64)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "checksums.txt"):
			fmt.Fprintf(w, "%s  %s\n", digest, archive)
		case strings.HasSuffix(r.URL.Path, archive):
			w.Write(blob)
		default:
			http.NotFound(w, r)
		}
	})
	s := httptest.NewServer(mux)
	t.Cleanup(s.Close)
	t.Setenv("TK_RELEASE_BASE_URL", s.URL)
	return s.URL
}

func fakeImage(version string) []byte {
	return []byte(fmt.Sprintf("#!/bin/sh\necho codebase-memory-mcp %s\n", version))
}

func tarBytes(t *testing.T, name string, body []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{
		Name: name, Mode: 0o755, Size: int64(len(body)), Typeflag: tar.TypeReg,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(body); err != nil {
		t.Fatal(err)
	}
	tw.Close()
	gz.Close()
	return buf.Bytes()
}

func zipBytes(t *testing.T, name string, body []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.Create(name)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write(body); err != nil {
		t.Fatal(err)
	}
	zw.Close()
	return buf.Bytes()
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
	fakeRelease(t, config.DefaultCBMPin, true)
	out, err := runInstall(t, t.TempDir(), false)
	if err == nil {
		t.Fatalf("a failed backend install must exit non-zero; output was:\n%s", out)
	}
	if !strings.Contains(out, "FAILED") {
		t.Errorf("human face lost the failure line:\n%s", out)
	}
	// tk writes no vendor diagnostic any more, so the cause is tk's own. An
	// exit status alone is not a diagnosis.
	if !strings.Contains(out, "checksum mismatch") {
		t.Errorf("human face lost the cause:\n%s", out)
	}
}

// The two faces must agree. --json is how a machine learns what happened, so an
// envelope that says ok:true next to a non-zero exit is a lie one of the two
// callers always believes.
func TestInstallFailureJSONFaceSaysNotOK(t *testing.T) {
	fakeRelease(t, config.DefaultCBMPin, true)
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
	if !strings.Contains(env.Backends[0].Detail, "checksum mismatch") {
		t.Errorf("row lost the cause: %q", env.Backends[0].Detail)
	}
}

// TestInstallDoesNotPinOnFailure: a failed install that still wrote the pin
// would make the next run report "up-to-date" against a binary that is not
// there, and the pin would outlive the failure that caused it.
func TestInstallDoesNotPinOnFailure(t *testing.T) {
	fakeRelease(t, config.DefaultCBMPin, true)
	home := t.TempDir()

	if _, err := runInstall(t, home, false); err == nil {
		t.Fatal("expected a non-zero exit")
	}
	if _, err := os.Stat(filepath.Join(home, "cache", "bin", backends.CBM().BinaryName(backends.HostGOOS()))); err == nil {
		t.Error("a failed install published a binary")
	}
	raw, rerr := os.ReadFile(filepath.Join(home, "config", "config.json"))
	if rerr == nil && strings.Contains(string(raw), "cbm_version_pin") {
		t.Errorf("a failed install pinned a version:\n%s", raw)
	}
}

// A successful install is the control for both faces above: if ok:true and
// exit 0 only ever came from the failure path, the assertions would pass with a
// command that cannot install anything.
func TestInstallSuccessKeepsExitZero(t *testing.T) {
	fakeRelease(t, config.DefaultCBMPin, false)
	home := t.TempDir()
	out, err := runInstall(t, home, false)
	if err != nil {
		t.Fatalf("a successful install must exit 0: %v\n%s", err, out)
	}
	if !strings.Contains(out, "installed") {
		t.Errorf("human face lost the installed line:\n%s", out)
	}
	dest := filepath.Join(home, "cache", "bin", backends.CBM().BinaryName(backends.HostGOOS()))
	if _, err := os.Stat(dest); err != nil {
		t.Errorf("binary not installed into the cache: %v", err)
	}
	// Second run must recognize its own work and change nothing.
	again, err := runInstall(t, home, false)
	if err != nil {
		t.Fatalf("re-run must stay green: %v\n%s", err, again)
	}
	if !strings.Contains(again, "up-to-date") {
		t.Errorf("a reinstall of the same pin must be a no-op:\n%s", again)
	}
}
