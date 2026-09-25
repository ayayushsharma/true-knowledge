package cli

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/ayayushsharma/true-knowledge/internal/cbmexec"
	"github.com/ayayushsharma/true-knowledge/internal/gitx"
	"github.com/ayayushsharma/true-knowledge/internal/mcp"
	"github.com/ayayushsharma/true-knowledge/internal/store"
	"github.com/ayayushsharma/true-knowledge/internal/trace"
	"github.com/ayayushsharma/true-knowledge/internal/zoekttext"
	"github.com/spf13/cobra"
)

// ensureZoektIndex refreshes a project's trigram shards, in-process.
// Git repos: incremental (IndexRepo reports updated=false when the SHA is
// already covered — the free no-op behind `tk sync`).
// Plain dirs: full rebuild (fast at the sizes tk allows for non-git trees).
// The library is always linked in: no absent-backend case exists.
// Returns indexed HEAD (or "files") and any error.
func ensureZoektIndex(ctx context.Context, c *Ctx, name, repoPath string) (string, error) {
	shards := c.Paths.ZoektShards(name)
	if head := gitx.Head(repoPath); head != "" {
		t0 := time.Now()
		updated, err := zoekttext.IndexRepo(ctx, shards, repoPath, name)
		ms := time.Since(t0).Milliseconds()
		ev := trace.Event{Backend: "zoekt", Op: "index", Ms: ms, OK: err == nil, Detail: fmt.Sprintf("updated=%v", updated && err == nil)}
		if err != nil {
			ev.Error = firstLine(err.Error())
		}
		c.record(ev)
		if err != nil {
			return "", fmt.Errorf("zoekt index %q: %w", name, err)
		}
		return head, nil
	}
	t0 := time.Now()
	err := zoekttext.IndexDir(ctx, shards, repoPath, name, loadIgnoreLines(c))
	ev := trace.Event{Backend: "zoekt", Op: "index", Ms: time.Since(t0).Milliseconds(), OK: err == nil, Detail: "plain-dir"}
	if err != nil {
		ev.Error = firstLine(err.Error())
	}
	c.record(ev)
	if err != nil {
		return "", fmt.Errorf("zoekt index %q: %w", name, err)
	}
	return "files", nil
}

// loadIgnoreLines reads the global plain-dir ignore file, if any. Parsing
// (comments, anchors, dir-only) happens inside zoekttext; here we just ship
// the non-comment, non-blank lines (parseIgnores strips them again so the
// file's #docstring lines can never leak into matching).
func loadIgnoreLines(c *Ctx) []string {
	raw, err := os.ReadFile(c.Paths.IgnoreFile())
	if err != nil {
		return nil
	}
	var lines []string
	for _, ln := range strings.Split(string(raw), "\n") {
		ln = strings.TrimSpace(ln)
		if ln == "" || strings.HasPrefix(ln, "#") {
			continue
		}
		lines = append(lines, ln)
	}
	return lines
}

// ensureZoektAndTouch refreshes a project's trigram shards in-process and
// records the covered HEAD/fingerprint in the registry — the zoekt half of
// `tk index`/`tk sync`, shared by `source-search`'s auto-refresh so a search
// never serves a committed state older than HEAD. Registry is only mutated
// when the covered HEAD changes (clean searches stay no-ops).
func (c *Ctx) ensureZoektAndTouch(ctx context.Context, name, repoPath, mode string) (string, error) {
	zhead, err := ensureZoektIndex(ctx, c, name, repoPath)
	if err != nil {
		return "", err
	}
	p := c.Reg[name]
	if zhead == p.ZoektHead {
		return zhead, nil
	}
	if head := gitx.Head(repoPath); head != "" {
		c.Reg.Touch(name, head, mode)
	} else if fp, ferr := store.Fingerprint(repoPath); ferr == nil {
		c.Reg.TouchFiles(name, fp, mode)
	} else {
		c.Reg.Touch(name, "", mode)
	}
	p = c.Reg[name]
	p.ZoektHead = zhead
	c.Reg[name] = p
	return zhead, nil
}

// zoektStaleNote returns a leading "[source-search: ...]" annotation when
// the served shards do not cover the live tree, plus worktree change counts.
// Git-only: committed drift (covered HEAD != live HEAD) and uncommitted
// drift (modified/untracked counts) both annotate but never block. "" = the
// index is as fresh as the worktree allows.
func zoektStaleNote(p store.Project) (note string, modified, untracked int) {
	head := gitx.Head(p.Path)
	if head == "" {
		return "", 0, 0
	}
	parts := []string{}
	if p.ZoektHead != "" && p.ZoektHead != head {
		parts = append(parts, fmt.Sprintf("index covers HEAD@%s; live HEAD %s not indexed", shortHead(p.ZoektHead), shortHead(head)))
	}
	modified, untracked, _ = gitx.Dirty(p.Path)
	if modified > 0 || untracked > 0 {
		parts = append(parts, fmt.Sprintf("%d modified, %d untracked in worktree not indexed", modified, untracked))
	}
	if len(parts) == 0 {
		return "", 0, 0
	}
	return "[source-search: " + strings.Join(parts, "; ") + "]\n", modified, untracked
}

func cmdSourceSearch(g *Globals) *cobra.Command {
	var project, files, pat string
	var limit int
	c := &cobra.Command{
		Use:   "source-search --pattern <pattern> [--project <name>]",
		Short: "Trigram text search via zoekt (explicit, in-process, auto-refresh)",
		Example: `  tk source-search --pattern ProcessOrder --project demo
  tk source-search --pattern "ok:" --project demo --files '*.go' --limit 20`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, err := load(*g)
			if err != nil {
				return err
			}
			proj, err := requireProject(ctx, project)
			if err != nil {
				return err
			}
			p := ctx.Reg[proj]
			// Auto-refresh before searching: clean HEAD = the ~1ms zoekt
			// no-op; moved HEAD = a delta reindex. Never serve stale commits.
			if _, zerr := ctx.ensureZoektAndTouch(cmd.Context(), proj, p.Path, ctx.Cfg.IndexMode); zerr != nil {
				return fail("source-search: %v", zerr)
			}
			if err := ctx.saveReg(); err != nil {
				return fail("save registry: %v", err)
			}
			// Worktree drift (uncommitted edits are invisible to the index):
			// counts annotate results; git-only, never blocks.
			p = ctx.Reg[proj]
			note, modCount, untrackedCount := zoektStaleNote(p)
			t0 := time.Now()
			text, err := mcp.QueryZoektLive(cmd.Context(), ctx.Paths.ZoektShards(proj), p.Path, pat, files, limit)
			ev := trace.Event{Backend: "zoekt", Op: "search", Ms: time.Since(t0).Milliseconds(), OK: err == nil}
			if err != nil {
				ev.Error = firstLine(err.Error())
			}
			ctx.record(ev)
			if err != nil {
				return fail("source-search: %v (shards missing? run `tk index %s`)", err, proj)
			}
			text = note + text
			return ctx.outFresh(cmd, proj, cbmexec.Truncate(text, ctx.budget("")), map[string]any{
				"project":            proj,
				"backend":            "zoekt",
				"zoekt_head":         p.ZoektHead,
				"worktree_modified":  modCount,
				"worktree_untracked": untrackedCount,
			})
		},
	}
	c.Flags().StringVar(&project, "project", "", "project name")
	c.Flags().StringVar(&pat, "pattern", "", "search pattern")
	c.Flags().StringVar(&files, "files", "", "file glob filter (zoekt file:)")
	c.Flags().IntVar(&limit, "limit", 20, "max matches")
	_ = c.MarkFlagRequired("pattern")
	_ = c.RegisterFlagCompletionFunc("project", projectFlagCompletion(g))
	return c
}
