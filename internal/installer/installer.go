// Package installer fetches, verifies, and atomically installs backend binaries.
//
// Design: stdlib only (net/http, crypto/sha256, archive/tar+zip), checksum
// verification mandatory before any write, atomic rename into <cache>/bin,
// installed versions tracked in <cache>/bin/.tk-versions.json.
// A backend is added by extending internal/backends — this package is generic.
package installer

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/ayayushsharma/true-knowledge/internal/backends"
)

var httpClient = &http.Client{Timeout: 5 * time.Minute}

var semverRe = regexp.MustCompile(`\d+\.\d+\.\d+`)

// maxArchiveBytes caps a downloaded archive. Kept for checksum-manifest
// fetches and as the streaming download bound below.
const maxArchiveBytes = 512 << 20

// Status describes one backend's install state.
type Status struct {
	Backend          backends.Backend
	Pin              string
	Path             string // resolved binary (PATH or cache/bin) or ""
	InCache          bool   // resolved path is the tk-managed one
	InstalledVersion string // "" when unknown
	UpToDate         bool   // installed version == pin
	NeedsInstall     bool
}

// Plan describes the concrete download for one backend.
type Plan struct {
	Backend      backends.Backend
	Pin          string
	Archive      string
	ArchiveURL   string
	ChecksumsURL string
	Dest         string // <cache>/bin/<binary>
}

// ResolvePlan builds the download plan for the host (or given) platform.
func ResolvePlan(cacheDir string, b backends.Backend, pin, goos, goarch string) (Plan, error) {
	archive, err := b.Archive(goos, goarch)
	if err != nil {
		return Plan{}, err
	}
	tag := b.Tag(pin)
	base := b.ReleaseBase(tag)
	return Plan{
		Backend:      b,
		Pin:          pin,
		Archive:      archive,
		ArchiveURL:   base + "/" + archive,
		ChecksumsURL: base + "/" + b.Checksums,
		Dest:         filepath.Join(cacheDir, "bin", b.BinaryName(goos)),
	}, nil
}

// binDir is <cache>/bin.
func binDir(cacheDir string) string { return filepath.Join(cacheDir, "bin") }

// versionsPath tracks what tk installed (vs. user PATH copies).
func versionsPath(cacheDir string) string {
	return filepath.Join(binDir(cacheDir), ".tk-versions.json")
}

func readVersions(cacheDir string) map[string]string {
	m := map[string]string{}
	raw, err := os.ReadFile(versionsPath(cacheDir))
	if err != nil {
		return m
	}
	_ = json.Unmarshal(raw, &m)
	return m
}

func writeVersions(cacheDir string, m map[string]string) error {
	if err := os.MkdirAll(binDir(cacheDir), 0o700); err != nil {
		return err
	}
	raw, _ := json.MarshalIndent(m, "", "  ")
	tmp := versionsPath(cacheDir) + ".tmp"
	if err := os.WriteFile(tmp, append(raw, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, versionsPath(cacheDir))
}

// DetectVersion runs <bin> --version and parses the first X.Y.Z.
func DetectVersion(ctx context.Context, bin string) string {
	c, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(c, bin, "--version").CombinedOutput()
	if err != nil {
		return ""
	}
	return semverRe.FindString(string(out))
}

// Inspect returns the install status. PATH copies count as installed but not
// tk-managed (updates replace the cache copy, never touch PATH).
func Inspect(ctx context.Context, cacheDir string, b backends.Backend, pin, goos string) Status {
	st := Status{Backend: b, Pin: pin, NeedsInstall: true}
	dest := filepath.Join(binDir(cacheDir), b.BinaryName(goos))
	if _, err := os.Stat(dest); err == nil {
		st.Path = dest
		st.InCache = true
		if v := readVersions(cacheDir)[b.Name]; v != "" {
			st.InstalledVersion = v
		} else {
			st.InstalledVersion = DetectVersion(ctx, dest)
		}
		st.UpToDate = st.InstalledVersion == pin
		st.NeedsInstall = !st.UpToDate
		return st
	}
	if p, err := exec.LookPath(b.Binary); err == nil {
		st.Path = p
		st.InstalledVersion = DetectVersion(ctx, p)
		st.UpToDate = st.InstalledVersion == pin
		st.NeedsInstall = true // PATH copy: tk installs its managed copy anyway
		return st
	}
	return st
}

// Install downloads, checksum-verifies, and atomically installs the backend.
// It is idempotent: byte-identical re-runs rewrite the same file.
// The archive is streamed to a temp file while its sha256 is computed inline
// (never buffered whole in memory — backend archives are hundreds of MB), then
// verified against the manifest before the binary entry is streamed out.
func Install(ctx context.Context, cacheDir string, b backends.Backend, pin, goos, goarch string) (Plan, error) {
	plan, err := ResolvePlan(cacheDir, b, pin, goos, goarch)
	if err != nil {
		return Plan{}, err
	}
	sums, err := fetch(ctx, plan.ChecksumsURL)
	if err != nil {
		return Plan{}, fmt.Errorf("fetch %s: %w", plan.ChecksumsURL, err)
	}
	want, err := checksumFor(sums, plan.Archive)
	if err != nil {
		return Plan{}, fmt.Errorf("checksum manifest: %w", err)
	}
	if err := os.MkdirAll(binDir(cacheDir), 0o700); err != nil {
		return Plan{}, err
	}
	blob := filepath.Join(binDir(cacheDir), "tk-dl-"+plan.Archive+".tmp")
	defer os.Remove(blob) // best-effort: leftover temp is overwritten next run
	got, err := download(ctx, plan.ArchiveURL, blob)
	if err != nil {
		return Plan{}, fmt.Errorf("fetch %s: %w", plan.ArchiveURL, err)
	}
	if hex.EncodeToString(got[:]) != want {
		return Plan{}, fmt.Errorf("checksum mismatch for %s (manifest %s…)", plan.Archive, short(want))
	}
	tmp := plan.Dest + ".tmp"
	// Stream the entry straight to disk: backend binaries can exceed
	// hundreds of MB (CBM embeds grammars + embeddings), so never
	// buffer the whole binary in memory.
	if err := extractBinary(blob, plan.Archive, b.BinaryName(goos), tmp); err != nil {
		_ = os.Remove(tmp)
		return Plan{}, err
	}
	if err := os.Chmod(tmp, 0o755); err != nil {
		_ = os.Remove(tmp)
		return Plan{}, err
	}
	if err := os.Rename(tmp, plan.Dest); err != nil {
		return Plan{}, err
	}
	m := readVersions(cacheDir)
	m[b.Name] = pin
	if err := writeVersions(cacheDir, m); err != nil {
		return Plan{}, err
	}
	return plan, nil
}

func fetch(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "tk-installer")
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %s", resp.Status)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 512<<20)) // checksum manifests are tiny anyway; same guard
}

// download streams a URL body to path while hashing it, bounded to the same
// maxArchiveBytes whole-archive cap as the old in-memory fetch. Memory stays
// flat regardless of archive size.
func download(ctx context.Context, url, path string) ([sha256.Size]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return [sha256.Size]byte{}, err
	}
	req.Header.Set("User-Agent", "tk-installer")
	resp, err := httpClient.Do(req)
	if err != nil {
		return [sha256.Size]byte{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return [sha256.Size]byte{}, fmt.Errorf("HTTP %s", resp.Status)
	}
	out, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return [sha256.Size]byte{}, err
	}
	hasher := sha256.New()
	n, err := io.Copy(io.MultiWriter(out, hasher), io.LimitReader(resp.Body, maxArchiveBytes+1))
	if cerr := out.Close(); err == nil && cerr != nil {
		err = cerr
	}
	if err != nil {
		return [sha256.Size]byte{}, err
	}
	if n > maxArchiveBytes {
		return [sha256.Size]byte{}, fmt.Errorf("archive exceeds %d MiB cap", maxArchiveBytes>>20)
	}
	var sum [sha256.Size]byte
	copy(sum[:], hasher.Sum(nil))
	return sum, nil
}

// checksumFor finds "<sha256>  <filename>" (or *binary) lines.
func checksumFor(manifest []byte, archive string) (string, error) {
	for _, line := range strings.Split(string(manifest), "\n") {
		f := strings.Fields(line)
		if len(f) < 2 {
			continue
		}
		name := strings.TrimPrefix(f[1], "*")
		if name == archive {
			sum := strings.ToLower(f[0])
			if len(sum) != 64 {
				return "", fmt.Errorf("bad sha256 %q for %s", f[0], archive)
			}
			return sum, nil
		}
	}
	return "", fmt.Errorf("%s not listed in manifest", archive)
}

// extractBinary pulls the single named executable out of a .tar.gz or .zip on
// disk and streams it to dest (no size cap, no full buffering — backend
// binaries can be hundreds of MB). Only entry content is used — archive paths
// never touch disk (no traversal).
func extractBinary(blob, archive, binary, dest string) error {
	if strings.HasSuffix(archive, ".zip") {
		return extractZipToFile(blob, binary, archive, dest)
	}
	return extractTarGzToFile(blob, binary, archive, dest)
}

func extractZipToFile(blob, binary, archive, dest string) error {
	f, err := os.Open(blob)
	if err != nil {
		return err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return err
	}
	zr, err := zip.NewReader(f, fi.Size())
	if err != nil {
		return fmt.Errorf("open zip: %w", err)
	}
	for _, zf := range zr.File {
		if base(zf.Name) == binary {
			return streamEntry(zf.Open, dest)
		}
	}
	return fmt.Errorf("%s not found in %s", binary, archive)
}

func extractTarGzToFile(blob, binary, archive, dest string) error {
	f, err := os.Open(blob)
	if err != nil {
		return err
	}
	defer f.Close()
	gr, err := gzip.NewReader(f)
	if err != nil {
		return fmt.Errorf("open tar.gz: %w", err)
	}
	defer gr.Close()
	tr := tar.NewReader(gr)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("read tar: %w", err)
		}
		if hdr.Typeflag != tar.TypeReg || base(hdr.Name) != binary {
			continue
		}
		out, err := os.OpenFile(dest, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(out, tr)
		closeErr := out.Close()
		if copyErr != nil {
			return fmt.Errorf("extract %s: %w", binary, copyErr)
		}
		if closeErr != nil {
			return closeErr
		}
		return nil
	}
	return fmt.Errorf("%s not found in %s", binary, archive)
}

// streamEntry copies a zip entry's content to dest.
func streamEntry(open func() (io.ReadCloser, error), dest string) error {
	rc, err := open()
	if err != nil {
		return err
	}
	defer rc.Close()
	out, err := os.OpenFile(dest, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(out, rc)
	closeErr := out.Close()
	if copyErr != nil {
		return copyErr
	}
	return closeErr
}

func base(p string) string {
	p = strings.ReplaceAll(p, "\\", "/")
	if i := strings.LastIndex(p, "/"); i >= 0 {
		return p[i+1:]
	}
	return p
}

func short(s string) string {
	if len(s) > 12 {
		return s[:12]
	}
	return s
}
