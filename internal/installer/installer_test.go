package installer_test

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/ayayushsharma/true-knowledge/internal/backends"
	"github.com/ayayushsharma/true-knowledge/internal/installer"
)

// scriptServer serves body as the installer script for any install.sh request.
func scriptServer(t *testing.T, body string) *httptest.Server {
	t.Helper()
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

// placingScript records argv and env, then places a binary that reports
// version at the --dir it was given. This stands in for the real install.sh:
// tk's job is to invoke the pinned script correctly and to notice what it did,
// not to reimplement what it does.
func placingScript(recordPath, version string) string {
	return fmt.Sprintf(`#!/usr/bin/env bash
set -euo pipefail
{
  echo "argv: $*"
  echo "download_url: ${CBM_DOWNLOAD_URL:-}"
  echo "tmpdir: ${TMPDIR:-}"
} > %q
for arg in "$@"; do
  case "$arg" in
    --dir=*) dir="${arg#--dir=}" ;;
  esac
done
mkdir -p "$dir"
cat > "$dir/codebase-memory-mcp" <<'BIN'
#!/bin/sh
echo cbm %s fake
BIN
chmod 755 "$dir/codebase-memory-mcp"
`, recordPath, version)
}

func TestInstallDelegatesToPinnedScript(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake script is bash")
	}
	record := filepath.Join(t.TempDir(), "record.txt")
	s := scriptServer(t, placingScript(record, "0.11.0"))
	t.Setenv("TK_SCRIPT_BASE_URL", s.URL)
	t.Setenv("TK_RELEASE_BASE_URL", "https://example.invalid/dl")
	cache := t.TempDir()
	b := backends.CBM()

	plan, err := installer.Install(t.Context(), cache, b, "0.11.0", "linux", "amd64", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(plan.Dest); err != nil {
		t.Fatalf("dest missing: %v", err)
	}
	raw, err := os.ReadFile(record)
	if err != nil {
		t.Fatal(err)
	}
	got := string(raw)
	// The pin must reach the script as a tag-scoped download base, never latest.
	if !strings.Contains(got, "https://example.invalid/dl/v0.11.0") {
		t.Errorf("pin not passed to script:\n%s", got)
	}
	// Agent config is tk's job; CBM must not write any client mcp.json.
	if !strings.Contains(got, "--skip-config") {
		t.Errorf("--skip-config not passed:\n%s", got)
	}
	// The binary must land in tk's cache, not $HOME/.local/bin.
	if !strings.Contains(got, "--dir="+filepath.Join(cache, "bin")) {
		t.Errorf("--dir not passed:\n%s", got)
	}
	// TMPDIR must not be the system temp dir: the install needs ~340 MB
	// transient, and the script unpacks it wherever TMPDIR points.
	tmp := tmpdirOf(got)
	if tmp == "" {
		t.Fatalf("TMPDIR not set for the script:\n%s", got)
	}
	if !strings.HasPrefix(tmp, cache) {
		t.Errorf("TMPDIR %q is outside the cache %q", tmp, cache)
	}
	// No staging residue: the private work dir is removed after the run.
	entries, err := os.ReadDir(cache)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".install-") {
			t.Errorf("staging residue left behind: %s", e.Name())
		}
	}

	st := installer.Inspect(t.Context(), cache, b, "0.11.0", "linux")
	if !st.UpToDate || st.NeedsInstall || !st.InCache {
		t.Fatalf("status = %+v", st)
	}
}

func tmpdirOf(record string) string {
	for _, line := range strings.Split(record, "\n") {
		if v, ok := strings.CutPrefix(line, "tmpdir: "); ok {
			return v
		}
	}
	return ""
}

func TestInstallRejectsMissingScript(t *testing.T) {
	s := httptest.NewServer(http.NotFoundHandler())
	t.Cleanup(s.Close)
	t.Setenv("TK_SCRIPT_BASE_URL", s.URL)
	_, err := installer.Install(t.Context(), t.TempDir(), backends.CBM(), "0.11.0", "linux", "amd64", nil)
	if err == nil {
		t.Fatal("expected an error when the installer script is absent")
	}
}

// TestInstallCarriesTheVendorDiagnostic: the exit status alone cannot tell a
// full disk from a corrupt archive from a refused activation. tk must carry the
// backend's own words, or the reader is left with "exit status 1".
func TestInstallCarriesTheVendorDiagnostic(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake script is bash")
	}
	body := "#!/usr/bin/env bash\n" +
		"echo 'Downloading codebase-memory-mcp-linux-amd64-portable.tar.gz...'\n" +
		"echo 'tar: install.sh: Cannot write: Disk quota exceeded' >&2\n" +
		"exit 2\n"
	s := scriptServer(t, body)
	t.Setenv("TK_SCRIPT_BASE_URL", s.URL)

	_, err := installer.Install(t.Context(), t.TempDir(), backends.CBM(), "0.11.0", "linux", "amd64", nil)
	if err == nil {
		t.Fatal("a failed script must be an error")
	}
	var ie *installer.Error
	if !errors.As(err, &ie) {
		t.Fatalf("want *installer.Error, got %T: %v", err, err)
	}
	if ie.Pin != "0.11.0" || ie.Backend != "cbm" {
		t.Errorf("error lost its identity: %+v", ie)
	}
	if !strings.Contains(err.Error(), "Disk quota exceeded") {
		t.Errorf("vendor diagnostic dropped:\n%v", err)
	}
	// A disk-full is not a daemon problem, so it must not borrow that hint.
	if ie.Hint != "" {
		t.Errorf("unrelated hint on a disk failure: %q", ie.Hint)
	}
}

// TestInstallSuggestsStoppingADaemon: when the backend refused because a daemon
// holds its coordination lock, the reader's next move is a command they can
// type. The backend cannot drain a daemon that predates its drain protocol, so
// this is the one install failure with a known fix.
func TestInstallSuggestsStoppingADaemon(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake script is bash")
	}
	body := "#!/usr/bin/env bash\n" +
		"echo 'Stopping active CBM sessions and operations for install...'\n" +
		"echo 'error: activation could not reserve exclusive access; no activation was committed.' >&2\n" +
		"exit 1\n"
	s := scriptServer(t, body)
	t.Setenv("TK_SCRIPT_BASE_URL", s.URL)

	_, err := installer.Install(t.Context(), t.TempDir(), backends.CBM(), "0.11.0", "linux", "amd64", nil)
	if err == nil {
		t.Fatal("a refused activation must be an error")
	}
	if !strings.Contains(err.Error(), "tk daemon stop") {
		t.Errorf("no remediation for a refused activation:\n%v", err)
	}
	if !strings.Contains(err.Error(), "could not reserve exclusive access") {
		t.Errorf("refusal text dropped:\n%v", err)
	}
}

// TestInstallFailsWhenNothingWasPublished: CBM exits 0 and leaves a binary
// owned by mise/Homebrew/nix alone, and a config-only install publishes no
// binary at all. Trusting the exit status there reports an install that has no
// binary behind it — which is what "installed, but the version reads -" is.
func TestInstallFailsWhenNothingWasPublished(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake script is bash")
	}
	body := "#!/usr/bin/env bash\n" +
		"echo 'Binary is managed elsewhere by mise:'\n" +
		"echo 'Leaving it and your PATH untouched; configuring agents only.'\n" +
		"exit 0\n"
	s := scriptServer(t, body)
	t.Setenv("TK_SCRIPT_BASE_URL", s.URL)

	_, err := installer.Install(t.Context(), t.TempDir(), backends.CBM(), "0.11.0", "linux", "amd64", nil)
	if err == nil {
		t.Fatal("exit 0 without a binary must still be a failure")
	}
	if !strings.Contains(err.Error(), "no binary at") {
		t.Errorf("want the missing-binary reason, got:\n%v", err)
	}
}

// TestInstallFailsOnTheWrongVersion: the pin is the one thing tk owns, so a
// binary that reports anything else did not satisfy it, exit status or not.
func TestInstallFailsOnTheWrongVersion(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake script is bash")
	}
	record := filepath.Join(t.TempDir(), "record.txt")
	s := scriptServer(t, placingScript(record, "0.10.8"))
	t.Setenv("TK_SCRIPT_BASE_URL", s.URL)

	_, err := installer.Install(t.Context(), t.TempDir(), backends.CBM(), "0.11.0", "linux", "amd64", nil)
	if err == nil {
		t.Fatal("a binary at the wrong version must not satisfy the pin")
	}
	if !strings.Contains(err.Error(), "0.10.8") || !strings.Contains(err.Error(), "0.11.0") {
		t.Errorf("want both versions in the reason, got:\n%v", err)
	}
}

// TestInstallPassesIsolatedCBMIdentity proves a hostile ambient CBM_* identity
// cannot steer an install: the caller's env wins because it is appended last.
func TestInstallPassesIsolatedCBMIdentity(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake script is bash")
	}
	record := filepath.Join(t.TempDir(), "record.txt")
	s := scriptServer(t, placingScript(record, "0.11.0"))
	t.Setenv("TK_SCRIPT_BASE_URL", s.URL)
	t.Setenv("CBM_CACHE_DIR", "/tmp/hostile-cache")
	t.Setenv("CBM_RUNTIME_DIR", "/tmp/hostile-runtime")
	cache := t.TempDir()

	_, err := installer.Install(t.Context(), cache, backends.CBM(), "0.11.0", "linux", "amd64",
		[]string{"CBM_CACHE_DIR=" + cache})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(cache, "bin", "codebase-memory-mcp")); err != nil {
		t.Fatalf("binary not placed in tk cache: %v", err)
	}
}

func TestInspectMissing(t *testing.T) {
	st := installer.Inspect(t.Context(), t.TempDir(), backends.CBM(), "0.11.0", "linux")
	if !st.NeedsInstall || st.UpToDate {
		t.Fatalf("status = %+v", st)
	}
}
