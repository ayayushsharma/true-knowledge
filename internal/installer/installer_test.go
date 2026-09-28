package installer

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ayayushsharma/true-knowledge/internal/backends"
)

// fakeBinary is a shell script that reports a version, standing in for the real
// 300 MB CBM image. The swap is about paths and versions, not size.
const fakeBinary = "#!/bin/sh\necho codebase-memory-mcp %s\n"

func cbmBackend() backends.Backend { return backends.CBM() }

func contains(hay, needle string) bool { return strings.Contains(hay, needle) }

func repeat64(s string) string { return strings.Repeat(s, 64) }

func versionedScript(v string) []byte { return []byte(fmt.Sprintf(fakeBinary, v)) }

// releaseServer serves a tar.gz holding one versioned binary plus a matching
// checksums.txt, at the same tag-scoped paths the real release uses.
func releaseServer(t *testing.T, version string) *httptest.Server {
	t.Helper()
	tgz := tarGz(t, "codebase-memory-mcp", versionedScript(version))
	sum := sha256.Sum256(tgz)
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "checksums.txt"):
			fmt.Fprintf(w, "%s  codebase-memory-mcp-linux-amd64.tar.gz\n", hex.EncodeToString(sum[:]))
		case strings.HasSuffix(r.URL.Path, ".tar.gz"):
			w.Write(tgz)
		default:
			http.NotFound(w, r)
		}
	})
	s := httptest.NewServer(mux)
	t.Cleanup(s.Close)
	return s
}

func tarGz(t *testing.T, name string, body []byte) []byte {
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
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func zipArchive(t *testing.T, name string, body []byte) []byte {
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
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func withReleaseBase(t *testing.T, s *httptest.Server) {
	t.Setenv("TK_RELEASE_BASE_URL", s.URL)
}

func TestInstallFetchesVerifiesAndPublishes(t *testing.T) {
	s := releaseServer(t, "0.11.0")
	withReleaseBase(t, s)
	cache := t.TempDir()
	plan, err := Install(context.Background(), cache, cbmBackend(), "0.11.0", "linux", "amd64")
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	if got := DetectVersion(context.Background(), plan.Dest); got != "0.11.0" {
		t.Fatalf("published binary reports %q, want 0.11.0", got)
	}
	// A published install leaves no staging debris behind.
	for _, suffix := range []string{".new", ".prev"} {
		if _, err := os.Stat(plan.Dest + suffix); !os.IsNotExist(err) {
			t.Fatalf("%s should not survive a clean install", suffix)
		}
	}
}

func TestInstallIsIdempotent(t *testing.T) {
	s := releaseServer(t, "0.11.0")
	withReleaseBase(t, s)
	cache := t.TempDir()
	first, err := Install(context.Background(), cache, cbmBackend(), "0.11.0", "linux", "amd64")
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	second, err := Install(context.Background(), cache, cbmBackend(), "0.11.0", "linux", "amd64")
	if err != nil {
		t.Fatalf("second: %v", err)
	}
	if first.Dest != second.Dest {
		t.Fatalf("dest moved: %s vs %s", first.Dest, second.Dest)
	}
	if _, err := os.Stat(second.Dest + ".prev"); !os.IsNotExist(err) {
		t.Fatal("an identical reinstall must not retain a generation")
	}
}

func TestInstallWritesNothingOnChecksumMismatch(t *testing.T) {
	// The bytes are the ones a mirror or proxy would substitute: the archive
	// resolves, but the manifest does not vouch for it.
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "checksums.txt") {
			fmt.Fprintf(w, "%s  codebase-memory-mcp-linux-amd64.tar.gz\n", repeat64("0"))
			return
		}
		w.Write(tarGz(t, "codebase-memory-mcp", versionedScript("0.11.0")))
	})
	s := httptest.NewServer(mux)
	t.Cleanup(s.Close)
	withReleaseBase(t, s)

	cache := t.TempDir()
	_, err := Install(context.Background(), cache, cbmBackend(), "0.11.0", "linux", "amd64")
	if err == nil {
		t.Fatal("a checksum mismatch must fail the install")
	}
	var ie *Error
	if !errors.As(err, &ie) || !contains(ie.Error(), "checksum mismatch") {
		t.Fatalf("want a checksum Error, got %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(cache, "bin", "codebase-memory-mcp")); !os.IsNotExist(statErr) {
		t.Fatal("nothing may be published when the checksum fails")
	}
}

func TestInstallRejectsACandidateThatIsNotThePin(t *testing.T) {
	// Served bytes that match the manifest but report the wrong version: a
	// release asset that does not match the pin tk was built with.
	s := releaseServer(t, "0.10.0")
	withReleaseBase(t, s)
	cache := t.TempDir()
	_, err := Install(context.Background(), cache, cbmBackend(), "0.11.0", "linux", "amd64")
	if err == nil {
		t.Fatal("a binary that does not report the pin must fail the install")
	}
	if !errors.Is(err, errNoPin) {
		t.Fatalf("want errNoPin so the cause is machine-checkable, got %v", err)
	}
	// Validated before publication: the existing binary is untouched.
	dest := filepath.Join(cache, "bin", "codebase-memory-mcp")
	if _, statErr := os.Stat(dest); !os.IsNotExist(statErr) {
		t.Fatal("a rejected candidate must never reach the published path")
	}
	if _, statErr := os.Stat(dest + ".new"); !os.IsNotExist(statErr) {
		t.Fatal("a rejected candidate must not leave a stage behind")
	}
}

func TestInstallRetainsAndRestoresThePriorGeneration(t *testing.T) {
	cache := t.TempDir()
	dest := filepath.Join(cache, "bin", "codebase-memory-mcp")
	if err := os.MkdirAll(filepath.Dir(dest), 0o700); err != nil {
		t.Fatal(err)
	}
	good := versionedScript("0.10.8")
	if err := os.WriteFile(dest, good, 0o755); err != nil {
		t.Fatal(err)
	}
	// Publish a candidate that vanishes between staging and the final probe is
	// the hard case; simulate the rollback directly instead, which is the only
	// branch that can restore.
	staged := dest + ".new"
	if err := os.WriteFile(staged, versionedScript("0.11.0"), 0o755); err != nil {
		t.Fatal(err)
	}
	swap(dest)
	if _, err := os.Stat(dest + ".prev"); err != nil {
		t.Fatalf("swap must retain the prior generation: %v", err)
	}
	rollback(dest)
	if got := DetectVersion(context.Background(), dest); got != "0.10.8" {
		t.Fatalf("rollback restored %q, want the retained 0.10.8", got)
	}
	if _, err := os.Stat(staged); !os.IsNotExist(err) {
		t.Fatal("rollback leaves the staged candidate consumed, not dangling")
	}
}

// A swap that cannot move the file must be an error, not a success report over
// an unchanged binary.
//
// The reachable failure is an occupied `.prev`: a non-empty directory there
// cannot be removed, and on Windows it also cannot be renamed over. This is the
// shape a half-finished earlier install leaves behind, so it is the one worth
// proving is reported rather than swallowed.
func TestSwapReportsAFailedPublish(t *testing.T) {
	dir := t.TempDir()
	dest := filepath.Join(dir, "target")
	if err := os.WriteFile(dest, versionedScript("0.10.8"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(dest+".prev", 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dest+".prev", "in-the-way"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dest+".new", versionedScript("0.11.0"), 0o755); err != nil {
		t.Fatal(err)
	}
	err := swap(dest)
	if err == nil {
		t.Fatal("a swap that cannot publish must return an error, not report success")
	}
	if got := DetectVersion(context.Background(), dest); got != "0.10.8" {
		t.Fatalf("a failed swap changed the target to %q; it must be left alone", got)
	}
	if _, statErr := os.Stat(dest + ".new"); statErr != nil {
		t.Fatalf("a failed swap must leave the staged candidate in place: %v", statErr)
	}
}

// The retained generation is the rollback path, and a re-install must not
// accumulate them.
func TestSwapRetainsExactlyOneGeneration(t *testing.T) {
	dir := t.TempDir()
	dest := filepath.Join(dir, "target")
	if err := os.WriteFile(dest, versionedScript("0.10.8"), 0o755); err != nil {
		t.Fatal(err)
	}
	for i, v := range []string{"0.10.8", "0.11.0"} {
		if err := os.WriteFile(dest+".new", versionedScript(v), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := swap(dest); err != nil {
			t.Fatalf("swap %d: %v", i, err)
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Fatalf("exactly one retained generation is the contract, got %v", names)
	}
	if got := DetectVersion(context.Background(), dest); got != "0.11.0" {
		t.Fatalf("published %q, want 0.11.0", got)
	}
}

func TestExtractBinaryIgnoresEntryPathsForWrites(t *testing.T) {
	// A traversal entry name must never become a write path. The name is
	// matched by base only, and the content lands at dest and nowhere else.
	const escaped = "../../../../tmp/tk-traversal-probe"
	evil := tarGz(t, escaped, versionedScript("0.11.0"))
	blob := filepath.Join(t.TempDir(), "evil.tar.gz")
	if err := os.WriteFile(blob, evil, 0o600); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(t.TempDir(), "out")
	err := extractBinary(blob, "evil.tar.gz", "tk-traversal-probe", dest)
	if err != nil {
		t.Fatalf("entry content should still extract: %v", err)
	}
	if _, statErr := os.Stat("/tmp/tk-traversal-probe"); statErr == nil {
		t.Fatal("a traversal entry escaped the destination")
	}
	if _, statErr := os.Stat(dest); statErr != nil {
		t.Fatalf("content must land at dest: %v", statErr)
	}
}

func TestExtractBinaryRejectsAnArchiveWithoutTheBinary(t *testing.T) {
	blob := filepath.Join(t.TempDir(), "a.tar.gz")
	if err := os.WriteFile(blob, tarGz(t, "some-other-tool", versionedScript("1.0.0")), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := extractBinary(blob, "a.tar.gz", "codebase-memory-mcp", filepath.Join(t.TempDir(), "o")); err == nil {
		t.Fatal("an archive missing the binary must fail, not publish nothing quietly")
	}
}

func TestExtractBinaryReadsZip(t *testing.T) {
	blob := filepath.Join(t.TempDir(), "a.zip")
	if err := os.WriteFile(blob, zipArchive(t, "codebase-memory-mcp.exe", versionedScript("0.11.0")), 0o600); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(t.TempDir(), "o.exe")
	if err := extractBinary(blob, "a.zip", "codebase-memory-mcp.exe", dest); err != nil {
		t.Fatalf("zip extract: %v", err)
	}
	if got := DetectVersion(context.Background(), dest); got != "0.11.0" {
		t.Fatalf("zip binary reports %q", got)
	}
}

func TestChecksumForReadsBothManifestSpellings(t *testing.T) {
	manifest := []byte(hex.EncodeToString(make([]byte, 32)) + "  a.tar.gz\n" +
		repeat64("c") + " *b.tar.gz\n")
	for _, archive := range []string{"a.tar.gz", "b.tar.gz"} {
		if _, err := checksumFor(manifest, archive); err != nil {
			t.Fatalf("%s should be found in either spelling: %v", archive, err)
		}
	}
	if _, err := checksumFor(manifest, "missing.tar.gz"); err == nil {
		t.Fatal("an unlisted archive must be an error, not a silent pass")
	}
}

func TestDetectVersionIgnoresAnAmbientCBMIdentity(t *testing.T) {
	// A PATH copy must not be able to redirect its own store by being asked
	// its version, so the probe runs with the CBM identity scrubbed.
	dir := t.TempDir()
	probe := filepath.Join(dir, "probe.sh")
	body := `#!/bin/sh
if [ -n "${CBM_CACHE_DIR:-}" ] || [ -n "${CBM_RUNTIME_DIR:-}" ]; then
  echo "identity leaked: $CBM_CACHE_DIR" >&2
  exit 9
fi
echo codebase-memory-mcp 0.11.0
`
	if err := os.WriteFile(probe, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CBM_CACHE_DIR", "/should/not/reach/the/probe")
	t.Setenv("CBM_RUNTIME_DIR", "/should/not/reach/the/probe")
	if got := DetectVersion(context.Background(), probe); got != "0.11.0" {
		t.Fatalf("probe reported %q; the CBM identity must be scrubbed", got)
	}
}

func TestInspectMissing(t *testing.T) {
	st := Inspect(context.Background(), t.TempDir(), cbmBackend(), "0.11.0", "nosuchgoos")
	if !st.NeedsInstall {
		t.Fatal("a missing binary needs an install")
	}
	if st.UpToDate {
		t.Fatal("nothing installed is never up to date")
	}
}

func TestResolvePlanRejectsAnUnsupportedPlatform(t *testing.T) {
	if _, err := ResolvePlan(t.TempDir(), cbmBackend(), "0.11.0", "plan9", "amd64"); err == nil {
		t.Fatal("an unsupported platform must be an error, not a bad URL")
	}
}
