package cli

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"
	"github.com/true-knowledge/tk/internal/cbmexec"
	"github.com/true-knowledge/tk/internal/gitx"
	"github.com/true-knowledge/tk/internal/mcp"
	"github.com/true-knowledge/tk/internal/zoekttext"
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
		updated, err := zoekttext.IndexRepo(shards, repoPath, name)
		if err != nil {
			return "", fmt.Errorf("zoekt index %q: %w", name, err)
		}
		if !updated {
			// Incremental no-op — still record HEAD (shards provably cover it).
			return head, nil
		}
		return head, nil
	}
	if err := zoekttext.IndexDir(shards, repoPath, name); err != nil {
		return "", fmt.Errorf("zoekt index %q: %w", name, err)
	}
	return "files", nil
}

func cmdSourceSearch(g *Globals) *cobra.Command {
	var project, files string
	var limit int
	c := &cobra.Command{
		Use:   "source-search <pattern> [project]",
		Short: "Trigram text search via zoekt (explicit, in-process, no magic routing)",
		Example: `  tk source-search ProcessOrder demo
  tk source-search "ok:" demo --files '*.go' --limit 20`,
		Args: cobra.MinimumNArgs(1),
		ValidArgsFunction: func(cmd *cobra.Command, args []string, _ string) ([]string, cobra.ShellCompDirective) {
			if ctx, err := load(*g); err == nil {
				return ctx.projectNames(), cobra.ShellCompDirectiveNoFileComp
			}
			return nil, cobra.ShellCompDirectiveNoFileComp
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, err := load(*g)
			if err != nil {
				return err
			}
			proj, err := requireProject(ctx, project, args)
			if err != nil {
				return err
			}
			text, err := mcp.QueryZoekt(cmd.Context(), ctx.Paths.ZoektShards(proj), args[0], files, limit)
			if err != nil {
				return fail("source-search: %v (shards missing? run `tk index %s`)", err, proj)
			}
			return ctx.outFresh(cmd, proj, cbmexec.Truncate(text, ctx.budget("")), map[string]any{"project": proj, "backend": "zoekt"})
		},
	}
	c.Flags().StringVar(&project, "project", "", "project name")
	c.Flags().StringVar(&files, "files", "", "file glob filter (zoekt file:)")
	c.Flags().IntVar(&limit, "limit", 20, "max matches")
	return c
}
