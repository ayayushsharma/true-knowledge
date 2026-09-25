package cli

import (
	"context"
	"fmt"
	"strings"

	"github.com/ayayushsharma/true-knowledge/internal/cbmexec"
	"github.com/spf13/cobra"
)

// coverageVerdict probes whole-project coverage (scopes=.) via CBM.
// Returns a one-line verdict for absence annotations.
func coverageVerdict(ctx context.Context, c *Ctx, proj string) (string, error) {
	out, err := c.cbmCallJSON(ctx, "check_index_coverage",
		map[string]any{"project": proj, "scopes": []string{"."}})
	if err != nil {
		return "", err
	}
	return cbmexec.CoverageVerdict(out), nil
}

// annotateAbsence appends a coverage verdict to empty output.
// Clean coverage verifies absence; a gap warns it unverified; a failed
// coverage call on empty results is a hard error (fail loudly).
func annotateAbsence(ctx context.Context, c *Ctx, proj, out string) (string, error) {
	verdict, err := coverageVerdict(ctx, c, proj)
	if err != nil {
		return "", fail("no results and coverage check failed: %v — absence unverified", err)
	}
	if strings.HasPrefix(verdict, "coverage: GAP") {
		return out + "\n(" + verdict + "; absence unverified)", nil
	}
	return out + "\n(" + verdict + ")", nil
}

func cmdValidate(g *Globals) *cobra.Command {
	var project, sym string
	var limit int
	c := &cobra.Command{
		Use:     "validate --symbol <symbol> [--project <name>]",
		Short:   "Symbol existence + near-miss candidates, coverage-annotated (analysis profile)",
		Example: `  tk validate --symbol ProcessOrder --project demo`,
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, err := load(*g)
			if err != nil {
				return err
			}
			proj, err := requireProject(ctx, project)
			if err != nil {
				return err
			}
			if _, _, err := ctx.needCBM(cmd.Context()); err != nil {
				return err
			}
			out, err := ctx.cbmCallJSON(cmd.Context(), "search_graph",
				map[string]any{"name_pattern": sym, "project": proj, "limit": limit})
			if err != nil {
				return fail("%v", err)
			}
			fields := map[string]any{"symbol": sym, "project": proj}
			if !cbmexec.LooksEmpty(out) && strings.Contains(strings.ToLower(out), strings.ToLower(sym)) {
				fields["valid"] = true
				text := fmt.Sprintf("valid: %q found in %s\n%s", sym, proj, out)
				return ctx.outFresh(cmd, proj, cbmexec.Truncate(text, ctx.budget("")), fields)
			}
			fields["valid"] = false
			cands := "(no candidates)"
			if toks := cbmexec.NearMissTokens(sym); len(toks) > 0 {
				if near, nerr := ctx.cbmCallJSON(cmd.Context(), "search_graph",
					map[string]any{"name_pattern": toks[0], "project": proj, "limit": limit}); nerr == nil && !cbmexec.LooksEmpty(near) {
					cands = near
				}
			}
			verdict, verr := coverageVerdict(cmd.Context(), ctx, proj)
			if verr != nil {
				return fail("no exact hit and coverage check failed: %v — absence unverified", verr)
			}
			text := fmt.Sprintf("invalid: %q not found in %s\n== near-miss ==\n%s\n(%s)", sym, proj, cands, verdict)
			return ctx.outFresh(cmd, proj, cbmexec.Truncate(text, ctx.budget("")), fields)
		},
	}
	c.Flags().StringVar(&project, "project", "", "project name")
	c.Flags().StringVar(&sym, "symbol", "", "symbol to validate")
	c.Flags().IntVar(&limit, "limit", 5, "max rows per lookup")
	_ = c.MarkFlagRequired("symbol")
	_ = c.RegisterFlagCompletionFunc("project", projectFlagCompletion(g))
	return c
}
