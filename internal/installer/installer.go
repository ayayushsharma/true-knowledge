// Package installer installs backend binaries by delegating to the backend's
// own release installer script.
//
// Design: tk does not reimplement download, checksum verification, or archive
// extraction. codebase-memory-mcp ships install.sh (install.ps1 on Windows)
// which does all three and then hands the job to the candidate binary's own
// `install` command, which owns process draining, the admission barrier, and a
// transactional binary swap with rollback. That swap is what makes a warm
// daemon and a running Windows image safe; tk cannot replicate it cheaply, so
// it does not try.
//
// What tk still owns: the version pin. CBM_DOWNLOAD_URL is set to the tag's
// release base, so a pinned install fetches exactly the pinned tag and
// verifies it against that tag's checksums.txt — performed by CBM, not tk.
//
// A backend is added by extending internal/backends.
package installer

import (
	"context"
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

// maxScriptBytes caps the installer script fetch. A script is kilobytes; this
// is a backstop against a hostile or wrong URL, not a budget.
const maxScriptBytes = 1 << 20

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

// Plan describes the concrete install for one backend.
type Plan struct {
	Backend    backends.Backend
	Pin        string
	Archive    string
	ArchiveURL string
	ScriptURL  string
	Dest       string // <cache>/bin/<binary>
}

// ResolvePlan builds the install plan for the host (or given) platform.
func ResolvePlan(cacheDir string, b backends.Backend, pin, goos, goarch string) (Plan, error) {
	archive, err := b.Archive(goos, goarch)
	if err != nil {
		return Plan{}, err
	}
	tag := b.Tag(pin)
	base := b.ReleaseBase(tag)
	return Plan{
		Backend:    b,
		Pin:        pin,
		Archive:    archive,
		ArchiveURL: base + "/" + archive,
		ScriptURL:  b.ScriptURL(tag, goos),
		Dest:       filepath.Join(cacheDir, "bin", b.BinaryName(goos)),
	}, nil
}

// binDir is <cache>/bin, which is also CBM_CACHE_DIR/bin.
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
// tk-managed (tk always installs its managed copy alongside).
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

// Install installs the backend at its pin by running the pin's own installer
// script. It is idempotent: re-running at the same pin is a no-op upstream.
//
// env supplies the CBM_* identity for the install so the backend writes its
// store to tk's cache and drains tk's cohort, not the account-wide one.
func Install(ctx context.Context, cacheDir string, b backends.Backend, pin, goos, goarch string, env []string) (Plan, error) {
	plan, err := ResolvePlan(cacheDir, b, pin, goos, goarch)
	if err != nil {
		return Plan{}, err
	}
	if err := os.MkdirAll(binDir(cacheDir), 0o700); err != nil {
		return Plan{}, err
	}
	script, err := fetchScript(ctx, plan.ScriptURL)
	if err != nil {
		return Plan{}, fmt.Errorf("fetch %s: %w", plan.ScriptURL, err)
	}
	stage, err := stageScript(cacheDir, goos, script)
	if err != nil {
		return Plan{}, err
	}
	defer os.RemoveAll(filepath.Dir(stage))

	args, err := scriptArgs(stage, plan, goos)
	if err != nil {
		return Plan{}, err
	}
	// Pin the download source to this tag so the script cannot drift to latest.
	runEnv := append(scrubbedEnv(), env...)
	runEnv = append(runEnv, "CBM_DOWNLOAD_URL="+b.ReleaseBase(b.Tag(pin)))
	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	cmd.Env = runEnv
	cmd.Stdout = os.Stderr // CBM's progress lines: tk's own output is separate
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return Plan{}, fmt.Errorf("run %s: %w", filepath.Base(stage), err)
	}
	return plan, nil
}

// scriptArgs builds the interpreter and argv for the pinned installer script.
// Windows uses the PowerShell variant; POSIX shells out to bash, which is the
// documented invocation for this script.
func scriptArgs(stage string, plan Plan, goos string) ([]string, error) {
	if goos == "windows" {
		shell, err := exec.LookPath("powershell")
		if err != nil {
			return nil, fmt.Errorf("powershell not found; CBM install on windows needs it")
		}
		return []string{shell, "-ExecutionPolicy", "Bypass", "-File", stage,
			"--dir=" + filepath.Dir(plan.Dest), "--skip-config"}, nil
	}
	shell, err := exec.LookPath("bash")
	if err != nil {
		return nil, fmt.Errorf("bash not found; CBM install needs it on this platform")
	}
	return []string{shell, stage, "--dir=" + filepath.Dir(plan.Dest), "--skip-config"}, nil
}

// stageScript writes the fetched script to a private file and returns its path.
// It is never the live path a previous run used, so a re-run cannot race or
// read a half-written script.
func stageScript(cacheDir, goos string, script []byte) (string, error) {
	name := "install.sh"
	if goos == "windows" {
		name = "install.ps1"
	}
	dir, err := os.MkdirTemp(binDir(cacheDir), ".stage-")
	if err != nil {
		return "", err
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, script, 0o700); err != nil {
		os.RemoveAll(dir)
		return "", err
	}
	return path, nil
}

// fetchScript GETs an installer script over HTTPS. A non-HTTPS scheme is
// refused outright: the script is executed, so it must not be interceptable.
func fetchScript(ctx context.Context, url string) ([]byte, error) {
	if !strings.HasPrefix(url, "https://") && !strings.HasPrefix(url, "http://127.0.0.1") &&
		!strings.HasPrefix(url, "http://localhost") {
		return nil, fmt.Errorf("refusing non-HTTPS installer URL: %s", url)
	}
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
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxScriptBytes))
	if err != nil {
		return nil, err
	}
	if len(body) == 0 {
		return nil, fmt.Errorf("empty installer script")
	}
	return body, nil
}
