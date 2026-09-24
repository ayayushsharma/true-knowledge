// Package zoekttext embeds trigram text search as a Go library.
//
// Same-language links, cross-language spawns: zoekt is Go, so tk calls it
// in-process instead of shelling out. No binaries to resolve, no downloads,
// no subprocess tax. The pin lives in go.mod, not tk config.
//
// Exactly four entry points (the revert seam — callers must not reach
// past them): IndexRepo, IndexDir, Search, SearchLive.
//
// Import audit @153817f643cd (re-run on pin bumps): the only init() in the
// imported packages is index/builder.go reading the process umask
// (get+restore, no state change). No signal handlers, maxprocs, profilers,
// or os.Exit. Library log.Printf lines go to stderr — the MCP log channel,
// never stdout — so JSON-RPC stays clean.
package zoekttext

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/sourcegraph/zoekt"
	"github.com/sourcegraph/zoekt/gitindex"
	"github.com/sourcegraph/zoekt/index"
	"github.com/sourcegraph/zoekt/query"
	"github.com/sourcegraph/zoekt/search"
)

// Match is one rendered hit: file, 1-based line, decoded text.
type Match struct {
	File string
	Line int
	Text string
}

// IndexRepo indexes a git repo into shardsDir (created if missing).
// Incremental: returns updated=false when the indexed SHAs already cover
// HEAD — the free no-op behind `tk sync`. No daemon, one call.
// Delta builds are enabled: only changed blobs are re-indexed when HEAD
// moves, so re-syncs scale with diff size instead of tree size. An
// inapplicable delta (changed .gitignore, missing prior shards) falls back
// to a normal build inside gitindex.
// Cancellation: gitindex exposes no ctx at this pin; ctx is honored before
// and after the build. A full 19s normal build cannot be interrupted mid-
// flight here (a future zoekt pin with a ctx-aware gitindex removes that).
func IndexRepo(ctx context.Context, shardsDir, repoPath, name string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if err := os.MkdirAll(shardsDir, 0o700); err != nil {
		return false, err
	}
	buildOpts := index.Options{
		IndexDir: shardsDir,
		RepositoryDescription: zoekt.Repository{
			Name: name,
		},
		IsDelta: true,
	}
	buildOpts.SetDefaults()
	updated, err := gitindex.IndexGitRepo(gitindex.Options{
		RepoDir:      repoPath,
		Branches:     []string{"HEAD"},
		BranchPrefix: "refs/heads/",
		Incremental:  true,
		BuildOptions: buildOpts,
	})
	if err != nil {
		return false, fmt.Errorf("zoekt index %q: %w", name, err)
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	return updated, nil
}

// IndexDir indexes a plain directory (non-git) into shardsDir.
// No SHA to compare against, so this always rebuilds — fast at the sizes
// tk allows for non-git trees. ctx is checked between files, so a cancelled
// caller stops the walk promptly.
func IndexDir(ctx context.Context, shardsDir, dir, name string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := os.MkdirAll(shardsDir, 0o700); err != nil {
		return err
	}
	buildOpts := index.Options{
		IndexDir: shardsDir,
		RepositoryDescription: zoekt.Repository{
			Name: name,
			Branches: []zoekt.RepositoryBranch{
				{Name: "HEAD", Version: "files"},
			},
		},
	}
	buildOpts.SetDefaults()
	builder, err := index.NewBuilder(buildOpts)
	if err != nil {
		return fmt.Errorf("zoekt builder: %w", err)
	}
	defer builder.Finish() // nolint:errcheck
	err = filepath.Walk(dir, func(path string, fi os.FileInfo, err error) error {
		if err != nil || fi.IsDir() {
			return nil
		}
		if cerr := ctx.Err(); cerr != nil {
			return cerr
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return nil
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		return builder.Add(index.Document{
			Name:     rel,
			Content:  content,
			Branches: []string{"HEAD"},
		})
	})
	if err != nil {
		if cerr := ctx.Err(); cerr != nil {
			return cerr
		}
		return fmt.Errorf("zoekt walk %q: %w", dir, err)
	}
	return builder.Finish()
}

// Search runs one trigram query against a project's shards and returns up
// to limit matches (<=0 = unbounded). files appends a zoekt `file:` filter.
// Shards open per call (mmap, tens of ms): no daemon. The internal 60s cap
// derives from ctx, so a caller deadline or disconnect cancels immediately.
func Search(ctx context.Context, shardsDir, rawQuery, files string, limit int) ([]Match, error) {
	q := rawQuery
	if files != "" {
		q += " file:" + files
	}
	parsed, err := query.Parse(q)
	if err != nil {
		return nil, fmt.Errorf("zoekt parse %q: %w", rawQuery, err)
	}
	searcher, err := search.NewDirectorySearcher(shardsDir)
	if err != nil {
		return nil, fmt.Errorf("zoekt open %q: %w (run `tk index`)", shardsDir, err)
	}
	defer searcher.Close()
	// WithTimeout over the caller ctx: whichever bound is sooner fires first.
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	opts := &zoekt.SearchOptions{MaxWallTime: 55 * time.Second}
	if limit > 0 {
		opts.TotalMaxMatchCount = limit
	}
	res, err := searcher.Search(ctx, parsed, opts)
	if err != nil {
		return nil, fmt.Errorf("zoekt search: %w", err)
	}
	var out []Match
	for _, f := range res.Files {
		for _, lm := range f.LineMatches {
			if limit > 0 && len(out) >= limit {
				return out, nil
			}
			out = append(out, Match{
				File: f.FileName,
				Line: lm.LineNumber,
				Text: strings.TrimRight(string(lm.Line), "\n"),
			})
		}
	}
	return out, nil
}

// SearchLive is Search plus a per-hit reconcile against the live worktree
// (root = project root the relative file paths live under). Line text of
// each hit is re-sliced from the current disk bytes so rendered snippets
// match the working tree, not the shard; hits whose file vanished on disk
// stay but are tagged "(worktree-missing)". Bounded by the same limit, so
// at most `limit` reads. Never a reindex — pure render-time correction.
func SearchLive(ctx context.Context, shardsDir, root, rawQuery, files string, limit int) ([]Match, error) {
	matches, err := Search(ctx, shardsDir, rawQuery, files, limit)
	if err != nil {
		return nil, err
	}
	for i := range matches {
		m := &matches[i]
		data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(m.File)))
		if err != nil {
			m.Text = "(worktree-missing) " + m.Text
			continue
		}
		lines := strings.Split(string(data), "\n")
		if m.Line >= 1 && m.Line <= len(lines) {
			m.Text = strings.TrimRight(lines[m.Line-1], "\r")
		}
	}
	return matches, nil
}
