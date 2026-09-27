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
// What tk owns about failure: the reason. The backend's exit status alone is
// "exit status 1" whether the disk filled, the archive was corrupt, or a
// daemon it could not drain refused the swap — and CBM's own diagnostic is the
// only thing that tells those apart. So tk keeps a copy, names the reason, and
// checks that the pin actually landed, because a backend that exits 0 without
// publishing anything is a real case, not a hypothetical one.
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
//
// It returns an *Error whenever the pin did not land, including when the
// script exited 0 without publishing it. A caller that trusts the exit status
// reports a successful install that no binary backs.
func Install(ctx context.Context, cacheDir string, b backends.Backend, pin, goos, goarch string, env []string) (Plan, error) {
	plan, err := ResolvePlan(cacheDir, b, pin, goos, goarch)
	if err != nil {
		return Plan{}, err
	}
	if err := os.MkdirAll(binDir(cacheDir), 0o700); err != nil {
		return Plan{}, err
	}
	// One private dir holds the staged script and serves as the install's
	// TMPDIR. The install is transient-heavy: install.sh downloads a 40 MB
	// archive and unpacks a 300 MB binary into mktemp -d, which defaults to the
	// system temp dir. On a small tmpfs that is ENOSPC, and the backend reports
	// it as a bare exit status. This moves ~340 MB of transient space next to
	// the target, on the cache's filesystem, where the binary is going anyway.
	//
	// It does not move everything. The backend's own prepared-candidate copy is
	// staged under cbm_tmpdir(), which its own source documents as separate
	// behaviour from $TMPDIR on POSIX, so that copy still follows the platform
	// temp. tk does not reach past the vendor's contract to relocate it; what
	// tk does is stop being opaque when that space runs out.
	work, err := os.MkdirTemp(cacheDir, ".install-")
	if err != nil {
		return Plan{}, err
	}
	defer os.RemoveAll(work)

	script, err := fetchScript(ctx, plan.ScriptURL)
	if err != nil {
		return Plan{}, fmt.Errorf("fetch %s: %w", plan.ScriptURL, err)
	}
	stage, err := stageScript(work, goos, script)
	if err != nil {
		return Plan{}, err
	}
	args, err := scriptArgs(stage, plan, goos)
	if err != nil {
		return Plan{}, err
	}
	// Pin the download source to this tag so the script cannot drift to latest.
	runEnv := append(scrubbedEnv(), env...)
	runEnv = append(runEnv,
		"CBM_DOWNLOAD_URL="+b.ReleaseBase(b.Tag(pin)),
		"TMPDIR="+work, "TMP="+work, "TEMP="+work,
	)
	out, err := runScript(ctx, args, runEnv)
	if err != nil {
		return Plan{}, scriptFailure(b, pin, out, fmt.Errorf("run %s: %w", filepath.Base(stage), err))
	}
	// Exit 0 is not proof the pin landed. CBM leaves a binary owned by
	// mise/Homebrew/nix alone and says so, and a config-only install publishes
	// no binary at all; both exit 0 with nothing at the target.
	if _, err := os.Stat(plan.Dest); err != nil {
		return Plan{}, unpinned(b, pin, out, "placed no binary at "+plan.Dest, err)
	}
	if got := DetectVersion(ctx, plan.Dest); got != pin {
		seen := got
		if seen == "" {
			seen = "no version"
		}
		return Plan{}, unpinned(b, pin, out,
			fmt.Sprintf("%s reports %s, not the pinned %s", plan.Dest, seen, pin), errNoPin)
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
func stageScript(dir, goos string, script []byte) (string, error) {
	name := "install.sh"
	if goos == "windows" {
		name = "install.ps1"
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, script, 0o700); err != nil {
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
