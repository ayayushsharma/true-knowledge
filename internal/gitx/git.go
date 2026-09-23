// Package gitx has tiny git helpers for freshness checks.
package gitx

import (
	"os/exec"
	"strings"
)

// Head returns `git rev-parse HEAD` in dir, or "" when not a repo / git missing.
func Head(dir string) string {
	cmd := exec.Command("git", "-C", dir, "rev-parse", "HEAD")
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// Root returns the worktree root or "".
func Root(dir string) string {
	cmd := exec.Command("git", "-C", dir, "rev-parse", "--show-toplevel")
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}
