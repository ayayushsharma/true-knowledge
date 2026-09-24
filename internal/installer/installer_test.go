package installer_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/ayayushsharma/true-knowledge/internal/backends"
	"github.com/ayayushsharma/true-knowledge/internal/installer"
)

// fakeRelease serves checksums.txt + a tar.gz containing one executable script.
func fakeRelease(t *testing.T, binary, body string) *httptest.Server {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	content := []byte(body)
	_ = tw.WriteHeader(&tar.Header{Name: "some-dir/" + binary, Mode: 0o755, Size: int64(len(content))})
	if _, err := tw.Write(content); err != nil {
		t.Fatal(err)
	}
	_ = tw.Close()
	_ = gz.Close()
	blob := buf.Bytes()
	sum := sha256.Sum256(blob)
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		switch filepath.Base(r.URL.Path) {
		case "checksums.txt":
			archive, _ := backends.CBM().Archive("linux", "amd64")
			fmt.Fprintf(w, "%s  %s\n", hex.EncodeToString(sum[:]), archive)
		default:
			w.Write(blob)
		}
	})
	s := httptest.NewServer(mux)
	t.Cleanup(s.Close)
	return s
}

func TestInstallVerifiesAndCaches(t *testing.T) {
	s := fakeRelease(t, "codebase-memory-mcp", "#!/bin/sh\necho cbm 0.11.0 fake\n")
	t.Setenv("TK_RELEASE_BASE_URL", s.URL)
	cache := t.TempDir()
	ctx := t.Context()
	b := backends.CBM()

	plan, err := installer.Install(ctx, cache, b, "0.11.0", "linux", "amd64")
	if err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(plan.Dest)
	if err != nil || fi.IsDir() {
		t.Fatalf("dest missing: %v", err)
	}
	st := installer.Inspect(ctx, cache, b, "0.11.0", "linux")
	if !st.UpToDate || st.NeedsInstall || !st.InCache {
		t.Fatalf("status = %+v", st)
	}
}

// TestInstallLargeBinary guards the extraction path against size caps:
// backend binaries can exceed hundreds of MB (CBM is ~268MB). A 260MiB
// zero-filled entry compresses tiny but must extract byte-complete.
func TestInstallLargeBinary(t *testing.T) {
	const size = 260 << 20
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	_ = tw.WriteHeader(&tar.Header{Name: "codebase-memory-mcp", Mode: 0o755, Size: size})
	chunk := make([]byte, 1<<20)
	for written := int64(0); written < size; written += int64(len(chunk)) {
		if _, err := tw.Write(chunk); err != nil {
			t.Fatal(err)
		}
	}
	_ = tw.Close()
	_ = gz.Close()
	blob := buf.Bytes()
	sum := sha256.Sum256(blob)
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if filepath.Base(r.URL.Path) == "checksums.txt" {
			archive, _ := backends.CBM().Archive("linux", "amd64")
			fmt.Fprintf(w, "%s  %s\n", hex.EncodeToString(sum[:]), archive)
			return
		}
		w.Write(blob)
	})
	s := httptest.NewServer(mux)
	t.Cleanup(s.Close)
	t.Setenv("TK_RELEASE_BASE_URL", s.URL)

	cache := t.TempDir()
	plan, err := installer.Install(t.Context(), cache, backends.CBM(), "0.11.0", "linux", "amd64")
	if err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(plan.Dest)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Size() != size {
		t.Fatalf("extracted size = %d, want %d (truncated?)", fi.Size(), size)
	}
}

func TestInstallRejectsBadChecksum(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if filepath.Base(r.URL.Path) == "checksums.txt" {
			archive, _ := backends.CBM().Archive("linux", "amd64")
			fmt.Fprintf(w, "%064x  %s\n", [32]byte{}, archive)
			return
		}
		w.Write([]byte("tampered"))
	})
	s := httptest.NewServer(mux)
	t.Cleanup(s.Close)
	t.Setenv("TK_RELEASE_BASE_URL", s.URL)

	err := func() error {
		_, err := installer.Install(t.Context(), t.TempDir(), backends.CBM(), "0.11.0", "linux", "amd64")
		return err
	}()
	if err == nil {
		t.Fatal("expected checksum mismatch error")
	}
}

func TestInspectMissing(t *testing.T) {
	st := installer.Inspect(t.Context(), t.TempDir(), backends.CBM(), "0.11.0", "linux")
	if !st.NeedsInstall || st.UpToDate {
		t.Fatalf("status = %+v", st)
	}
}
