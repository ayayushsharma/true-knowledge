// Package gitx has tiny git helpers for freshness checks.
package gitx

import (
	"bufio"
	"os/exec"
	"strings"
	"sync"
	"time"
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

// dirtyCacheTTL bounds how stale a Dirty annotation may be: small enough that
// a fresh edit surface within ~2s, wide enough that a burst of searches from
// several agents sharing one `tk mcp` process does not each re-run
// `git status` over the whole worktree (~68ms at 37k files, scales with repo
// size). Only successful lookups are cached; errors retry next call. Head is
// deliberately NOT cached — commit visibility must stay immediate.
var dirtyCacheTTL = 2 * time.Second

// dirtyCache is a process-local, mutex-guarded cache keyed by repo dir.
// One entry per registered repo; expired entries are swept on insert so the
// map cannot grow unbounded across sessions.
var (
	dirtyMu    sync.Mutex
	dirtyCache = map[string]dirtyDirty{}
)

type dirtyDirty struct {
	modified, untracked int
	err                 error
	expires             time.Time
}

// Dirty reports worktree drift (modified + untracked counts) via
// `git status --porcelain` (default `normal` untracked mode, so
// core.untrackedCache/fsmonitor apply). Returns (0,0,nil) for a clean or
// non-git dir, and an error only when git itself fails to run. Results are
// TTL-cached; callers must treat counts as advisory for worktree annotations,
// not as a transactional ground truth.
func Dirty(dir string) (modified, untracked int, err error) {
	dirtyMu.Lock()
	defer dirtyMu.Unlock()
	if e, ok := dirtyCache[dir]; ok && time.Now().Before(e.expires) {
		return e.modified, e.untracked, e.err
	}
	cmd := exec.Command("git", "-C", dir, "status", "--porcelain")
	out, err := cmd.Output()
	if err != nil {
		return 0, 0, err
	}
	sc := bufio.NewScanner(strings.NewReader(string(out)))
	for sc.Scan() {
		line := sc.Text()
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "??") {
			untracked++
			continue
		}
		modified++
	}
	if err := sc.Err(); err != nil {
		return 0, 0, err
	}
	now := time.Now()
	dirtyCache[dir] = dirtyDirty{modified: modified, untracked: untracked, expires: now.Add(dirtyCacheTTL)}
	for k, e := range dirtyCache {
		if now.After(e.expires) {
			delete(dirtyCache, k)
		}
	}
	return modified, untracked, nil
}
