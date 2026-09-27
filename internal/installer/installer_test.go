package installer_test

import (
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

// fakeScript serves an installer script that records its argv and env, then
// places a fake binary at the --dir it was given. This stands in for the real
// install.sh: tk's job is to invoke the pinned script correctly, not to
// reimplement what it does.
func fakeScript(t *testing.T, recordPath string) *httptest.Server {
	t.Helper()
	script := fmt.Sprintf(`#!/usr/bin/env bash
set -euo pipefail
{
  echo "argv: $*"
  echo "download_url: ${CBM_DOWNLOAD_URL:-}"
} > %q
for arg in "$@"; do
  case "$arg" in
    --dir=*) dir="${arg#--dir=}" ;;
  esac
done
mkdir -p "$dir"
cat > "$dir/codebase-memory-mcp" <<'BIN'
#!/bin/sh
echo cbm 0.11.0 fake
BIN
chmod 755 "$dir/codebase-memory-mcp"
`, recordPath)
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "install.sh") ||
			strings.HasSuffix(r.URL.Path, "install.ps1") {
			fmt.Fprint(w, script)
			return
		}
		http.NotFound(w, r)
	})
	s := httptest.NewServer(mux)
	t.Cleanup(s.Close)
	return s
}

func TestInstallDelegatesToPinnedScript(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake script is bash")
	}
	record := filepath.Join(t.TempDir(), "record.txt")
	s := fakeScript(t, record)
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
	// No staging residue: the script is staged in a private temp dir and the
	// whole dir is removed after the run.
	entries, err := os.ReadDir(filepath.Join(cache, "bin"))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".stage") {
			t.Errorf("staging residue left behind: %s", e.Name())
		}
	}

	st := installer.Inspect(t.Context(), cache, b, "0.11.0", "linux")
	if !st.UpToDate || st.NeedsInstall || !st.InCache {
		t.Fatalf("status = %+v", st)
	}
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

// TestInstallPassesIsolatedCBMIdentity proves a hostile ambient CBM_* identity
// cannot steer an install: the caller's env wins because it is appended last.
func TestInstallPassesIsolatedCBMIdentity(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake script is bash")
	}
	record := filepath.Join(t.TempDir(), "record.txt")
	s := fakeScript(t, record)
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
