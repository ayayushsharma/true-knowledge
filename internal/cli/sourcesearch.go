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
//
// countWorktree is what makes the fleet cheaper: it runs `git status` over
// every file in the worktree (measured 3ms small, ~68ms at 37k files — it
// scales with repo size), so a fleet passes false and keeps only the committed
// drift check. Per-project worktree counts are the least load-bearing fact in a
// scope annotation, and the fleet already reports per-project freshness.
func zoektStaleNote(p store.Project, countWorktree bool) (note string, modified, untracked int) {
	head := gitx.Head(p.Path)
	if head == "" {
		return "", 0, 0
	}
	parts := []string{}
	if p.ZoektHead != "" && p.ZoektHead != head {
		parts = append(parts, fmt.Sprintf("index covers HEAD@%s; live HEAD %s not indexed", shortHead(p.ZoektHead), shortHead(head)))
	}
	if countWorktree {
		modified, untracked, _ = gitx.Dirty(p.Path)
		if modified > 0 || untracked > 0 {
			parts = append(parts, fmt.Sprintf("%d modified, %d untracked in worktree not indexed", modified, untracked))
		}
	}
	if len(parts) == 0 {
		return "", 0, 0
	}
	return "[source-search: " + strings.Join(parts, "; ") + "]\n", modified, untracked
}

// resolveSourceScope resolves what a source-search invocation covers: one
// project by name, the interactive picker, or the whole fleet.
//
// It does not use requireProject. That helper resolves by inference — a single
// registered project auto-selects, otherwise a picker may open — and source-search
// never infers a scope. This is the one project-resolving command whose absence
// of --project is an error naming every route, because "search everything" must
// be asked for rather than inferred from an argument the caller left out.
// resolveProject keeps its auto-select for the other ten commands.
func resolveSourceScope(ctx *Ctx, project string, sel, all bool) (string, error) {
	switch {
	case project != "" && all:
		return "", fail("--project and --all-projects are mutually exclusive; pass one")
	case project != "":
		if _, ok := ctx.Reg[project]; !ok {
			return "", fail("unknown project %q (registered: %s); see `tk status`", project, listNames(ctx))
		}
		return project, nil
	case sel:
		if len(ctx.Reg) == 0 {
			return "", fail("--select needs at least one registered project — run `tk register <path>`")
		}
		if !pickerEnabled(ctx) {
			return "", fail("--select needs an interactive TTY (+ !--json + ui.picker); pass --project (registered: %s); see `tk status`", listNames(ctx))
		}
		if p, err := pickProject(ctx); err == nil && p != "" {
			return p, nil
		}
		return "", fail("picker aborted; pass --project (registered: %s); see `tk status`", listNames(ctx))
	case all:
		if len(ctx.Reg) == 0 {
			return "", fail("--all-projects needs at least one registered project — run `tk register <path>`")
		}
		return "", nil
	default:
		return "", fail("source-search needs a scope: --project <name>, --select, or --all-projects (registered: %s); see `tk status`", listNames(ctx))
	}
}

func cmdSourceSearch(g *Globals) *cobra.Command {
	var project, files, pat string
	var limit int
	var sel, all bool
	c := &cobra.Command{
		Use:   "source-search --pattern <pattern> [--project <name> | --select | --all-projects]",
		Short: "Trigram text search via zoekt (explicit, in-process, auto-refresh)",
		Example: `  tk source-search --pattern ProcessOrder --project demo
  tk source-search --pattern "ok:" --project demo --files '*.go' --limit 20
  tk source-search --pattern ProcessOrder --all-projects`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, err := load(*g)
			if err != nil {
				return err
			}
			proj, err := resolveSourceScope(ctx, project, sel, all)
			if err != nil {
				return err
			}
			if all {
				return runSourceSearchFleet(cmd, ctx, pat, files, limit)
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
			note, modCount, untrackedCount := zoektStaleNote(p, true)
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
	c.Flags().IntVar(&limit, "limit", 20, "max matches (0 = no limit)")
	c.Flags().BoolVar(&all, "all-projects", false, "search every registered project")
	selectFlag(c, &sel)
	_ = c.MarkFlagRequired("pattern")
	_ = c.RegisterFlagCompletionFunc("project", projectFlagCompletion(g))
	return c
}

// runSourceSearchFleet searches every registered project in name order, one
// zoekt searcher at a time.
//
// The order is load-bearing, not cosmetic: sorted names plus zoekt's per-project
// ranking give a deterministic (project, rank) sequence, which is the only shape
// a future result cursor could resume from. Projects whose refresh fails are
// dropped from the walk and reported as skipped, so a refresh failure never
// reads as a project with no matches.
func runSourceSearchFleet(cmd *cobra.Command, ctx *Ctx, pat, files string, limit int, capture ...func(map[string]any)) error {
	names := ctx.Reg.Names()
	members := make([]mcp.FleetMember, 0, len(names))
	notes := []string{}
	for _, name := range names {
		p := ctx.Reg[name]
		if _, err := ctx.ensureZoektAndTouch(cmd.Context(), name, p.Path, ctx.Cfg.IndexMode); err != nil {
			notes = append(notes, fmt.Sprintf("[source-search: %s: index refresh failed: %s]\n", name, firstLine(err.Error())))
			continue
		}
		note, _, _ := zoektStaleNote(ctx.Reg[name], false)
		if note != "" {
			notes = append(notes, note)
		}
		members = append(members, mcp.FleetMember{
			Name:   name,
			Shards: ctx.Paths.ZoektShards(name),
			Root:   p.Path,
			Live:   true,
		})
	}
	if err := ctx.saveReg(); err != nil {
		return fail("save registry: %v", err)
	}
	t0 := time.Now()
	fleet, err := mcp.QueryZoektFleet(cmd.Context(), members, pat, files, limit)
	ev := trace.Event{Backend: "zoekt", Op: "search", Ms: time.Since(t0).Milliseconds(), OK: err == nil,
		Detail: fmt.Sprintf("projects=%d", len(members))}
	if err != nil {
		ev.Error = firstLine(err.Error())
	}
	ctx.record(ev)
	if err != nil {
		return fail("source-search: %v (shards missing? run `tk index`)", err)
	}
	// The completeness line is built before Truncate and reports whether the
	// budget will actually cut, so it never names a cut that did not happen.
	body := fleet.Text
	budgetCut := false
	if b := ctx.budget(""); b > 0 && len(fleet.Completeness(true)+body) > b {
		budgetCut = true
	}
	text := fleet.Completeness(budgetCut) + body
	for i := len(notes) - 1; i >= 0; i-- {
		text = notes[i] + text
	}
	fields := map[string]any{
		"backend":           "zoekt",
		"scope":             "all-projects",
		"projects":          names,
		"projects_searched": fleet.Searched,
		"matches_total":     fleet.MatchesTotal,
		"files_total":       fleet.FilesTotal,
		"matches_returned":  fleet.MatchesShown,
		"files_returned":    fleet.FilesShown,
		"truncated":         fleet.Truncated,
	}
	if len(fleet.Skipped) > 0 {
		fields["skipped"] = fleet.Skipped
	}
	if len(fleet.NotSearched) > 0 {
		fields["not_searched"] = fleet.NotSearched
	}
	// capture lets a test read the --json fields this face pairs with the text.
	// Production passes none; the fields are identical either way, because both
	// come from the same map below rather than from a second computation.
	if len(capture) > 0 && capture[0] != nil {
		capture[0](fields)
	}
	return ctx.out(cmd, cbmexec.Truncate(text, ctx.budget("")), fields)
}
