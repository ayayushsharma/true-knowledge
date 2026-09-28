// Package installer fetches, verifies, and atomically installs backend binaries.
//
// Design: stdlib only (net/http, crypto/sha256, archive/tar+zip), checksum
// verification mandatory before any write, and a staged swap: the candidate is
// validated beside the target before the target is replaced, and the old file is
// retained one generation so a failed publish rolls back.
//
// The swap is the whole reason this package is not a shell out. A warm daemon
// holds the old image, and a bare rename over a live target is wrong on both
// platforms — on Windows a running .exe cannot replace its own image, and on
// Linux the running process keeps the old inode after the rename. Stopping that
// daemon is the caller's job, not this package's: this one owns bytes and the
// filesystem, and never spawns a backend. See internal/cli.quiesceDaemon.
//
// A backend is added by extending internal/backends — this package is generic.
package installer

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
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

// maxArchiveBytes caps a downloaded archive, in both fetch paths: the
// checksum manifest (a backstop for a wrong URL) and the streamed archive.
const maxArchiveBytes = 512 << 20

// Status describes one backend's install state.
type Status struct {
	Backend          backends.Backend
	Pin              string
	Path             string // resolved binary (cache/bin or PATH) or ""
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

// binDir is <cache>/bin, which is also CBM_CACHE_DIR/bin — the same place the
// daemon looks, so an installed binary and a running daemon are one subject.
func binDir(cacheDir string) string { return filepath.Join(cacheDir, "bin") }

// DetectVersion runs <bin> --version and parses the first X.Y.Z.
//
// The probe is run with the inherited environment scrubbed of CBM identity
// variables. A PATH copy must not be able to redirect its own store or
// rendezvous into a shared account location just by being asked its version.
func DetectVersion(ctx context.Context, bin string) string {
	c, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(c, bin, "--version")
	cmd.Env = scrubbedEnv()
	out, err := cmd.CombinedOutput()
	if err != nil {
		return ""
	}
	return semverRe.FindString(string(out))
}

// scrubbedEnv is os.Environ() with the CBM identity variables removed. A later
// assignment wins in Go's exec, so a caller that needs one back appends it.
func scrubbedEnv() []string {
	drop := map[string]bool{
		"CBM_CACHE_DIR": true, "CBM_RUNTIME_DIR": true, "CBM_ALLOWED_ROOT": true,
	}
	out := make([]string, 0, len(os.Environ()))
	for _, kv := range os.Environ() {
		if k, _, ok := strings.Cut(kv, "="); ok && drop[k] {
			continue
		}
		out = append(out, kv)
	}
	return out
}

// Inspect returns the install status. PATH copies count as installed but not
// tk-managed (updates replace the cache copy, never touch PATH).
func Inspect(ctx context.Context, cacheDir string, b backends.Backend, pin, goos string) Status {
	st := Status{Backend: b, Pin: pin, NeedsInstall: true}
	dest := filepath.Join(binDir(cacheDir), b.BinaryName(goos))
	if _, err := os.Stat(dest); err == nil {
		st.Path = dest
		st.InCache = true
		st.InstalledVersion = DetectVersion(ctx, dest)
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
//
// The archive is streamed to a temp file while its sha256 is computed inline
// (never buffered whole in memory — backend archives are hundreds of MB), then
// verified against the manifest before the binary entry is streamed out.
//
// Order matters and is the design: verify, stage beside the target, validate the
// staged file, retain the current one, publish, validate again, roll back on any
// failure. A caller that has not stopped the daemon holding this image is on its
// own — see the package comment.
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
		return Plan{}, mismatched(b, pin, want, hex.EncodeToString(got[:]))
	}
	return publish(ctx, plan, b, blob)
}

// publish extracts, validates, and swaps the binary into place.
func publish(ctx context.Context, plan Plan, b backends.Backend, blob string) (Plan, error) {
	staged := plan.Dest + ".new"
	// Stream the entry straight to disk: backend binaries can exceed hundreds of
	// MB (CBM embeds grammars + embeddings), so never buffer it in memory.
	if err := extractBinary(blob, plan.Archive, b.BinaryName(backends.HostGOOS()), staged); err != nil {
		_ = os.Remove(staged)
		return Plan{}, err
	}
	if err := os.Chmod(staged, 0o755); err != nil {
		_ = os.Remove(staged)
		return Plan{}, err
	}
	// Validate before touching the target. A candidate that will not report the
	// pin must never reach the path other processes resolve.
	if v := DetectVersion(ctx, staged); v != plan.Pin {
		_ = os.Remove(staged)
		return Plan{}, unpinned(b, plan.Pin, seenVersion(v), "the downloaded binary reports "+seenVersion(v))
	}
	// A swap that half-completes is the one failure this package cannot paper
	// over: the staged file is already gone, and a silent error here would leave
	// the old binary in place under a success report.
	if err := swap(plan.Dest); err != nil {
		_ = os.Remove(staged)
		return Plan{}, err
	}
	// Validate again at the published path, so a failed install is never
	// reported as a successful one and never leaves a caller believing a pin
	// landed that did not.
	if v := DetectVersion(ctx, plan.Dest); v != plan.Pin {
		rerr := rollback(plan.Dest)
		err := unpinned(b, plan.Pin, seenVersion(v), "the published binary reports "+seenVersion(v))
		if rerr != nil {
			err.Detail = append(err.Detail, "rollback also failed: "+rerr.Error())
			err.Hint = "no working binary is in place; re-run `tk install` to fetch the pin again"
		}
		return Plan{}, err
	}
	_ = os.Remove(plan.Dest + ".prev")
	return plan, nil
}

// swap renames dest to .prev and the staged file into place. Both renames are on
// one filesystem by construction — the stage sits beside the target — so neither
// is a copy. Errors are returned rather than swallowed: the second rename is the
// point of no return, and a caller that cannot tell it happened would report a
// successful install over an unchanged binary.
func swap(dest string) error {
	if err := os.Remove(dest + ".prev"); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("clear the retained generation: %w", err)
	}
	if _, err := os.Stat(dest); err == nil {
		if err := os.Rename(dest, dest+".prev"); err != nil {
			// The target is still exactly where it was, so leaving the staged
			// candidate beside it is a clean no-op, not a partial install.
			return fmt.Errorf("retain the current binary: %w", err)
		}
	}
	if err := os.Rename(dest+".new", dest); err != nil {
		return fmt.Errorf("publish the new binary: %w", err)
	}
	return nil
}

// rollback restores the retained generation. A rollback that itself fails is
// reported: the caller is already returning an error, and adding why the
// recovery failed is the difference between "re-run tk install" and "you have no
// working binary".
func rollback(dest string) error {
	if _, err := os.Stat(dest + ".prev"); err != nil {
		return nil // nothing retained, so nothing to restore
	}
	if err := os.Rename(dest+".prev", dest); err != nil {
		// dest may still hold the rejected binary. Clearing it is what lets the
		// rename succeed on Windows, where a rename cannot replace an existing
		// file.
		if rmErr := os.Remove(dest); rmErr == nil {
			if err2 := os.Rename(dest+".prev", dest); err2 == nil {
				return nil
			}
		}
		return err
	}
	return nil
}

// seenVersion names a version for an error, where "" means the probe found none.
func seenVersion(v string) string {
	if v == "" {
		return "no version"
	}
	return v
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
	return io.ReadAll(io.LimitReader(resp.Body, maxArchiveBytes)) // manifests are kilobytes; this is a backstop, not a budget
}

// download streams a URL body to path while hashing it, bounded to the same
// maxArchiveBytes whole-archive cap. Memory stays flat regardless of archive
// size.
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
// disk and streams it to dest — no full buffering, so a multi-hundred-MB CBM
// binary never lands in memory. Only entry *content* is written: the entry name
// is matched against the expected binary and never used as a path, so a
// traversal entry in a hostile archive cannot escape dest. The entry itself is
// not size-capped (maxArchiveBytes bounds the compressed download, not what a
// crafted archive inflates to), which is safe here only because the archive
// passed the pinned checksum.
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
