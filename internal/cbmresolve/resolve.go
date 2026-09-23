// Package cbmresolve locates the codebase-memory-mcp binary.
// Order: TK_CBM_BIN > tk config cbm_binary > sibling of tk exe (bundled
// release layout) > tk-managed cache/bin > PATH.
// The managed copy wins over PATH so `tk install`/`tk update` take effect.
package cbmresolve

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
)

// Find returns the binary path or an install-hint error.
func Find(configured, cacheDir string) (string, error) {
	if v := os.Getenv("TK_CBM_BIN"); v != "" {
		if _, err := os.Stat(v); err == nil {
			return v, nil
		}
		return "", fmt.Errorf("TK_CBM_BIN=%s not found; run `tk install` for setup", v)
	}
	if configured != "" {
		if _, err := os.Stat(configured); err == nil {
			return configured, nil
		}
	}
	bin := "codebase-memory-mcp"
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	// Bundled release layout: cbm ships next to tk in the same archive.
	if exe, err := os.Executable(); err == nil {
		if sib := filepath.Join(filepath.Dir(exe), bin); statOK(sib) {
			return sib, nil
		}
	}
	if cached := filepath.Join(cacheDir, "bin", bin); statOK(cached) {
		return cached, nil
	}
	for _, name := range []string{"codebase-memory-mcp", "cbm"} {
		if p, err := exec.LookPath(name); err == nil {
			return p, nil
		}
	}
	return "", fmt.Errorf("codebase-memory-mcp not found; run `tk install` (or set TK_CBM_BIN)")
}

func statOK(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && !fi.IsDir()
}
