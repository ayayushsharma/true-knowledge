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

// validateMiss renders the --json face of a miss. A miss is a claim about
// absence, so it ships the evidence for that claim next to it: the near-miss
// candidates that were considered, and the coverage the engine reports for
// the project. Neither is decorative — without them "not found" is an
// assertion, and with them it is a checkable one.
func (c *Ctx) validateMiss(cmd *cobra.Command, proj, sym string, limit int, fields map[string]any) error {
	var cands any
	if toks := cbmexec.NearMissTokens(sym); len(toks) > 0 {
		if near, err := c.cbmCallStructured(cmd.Context(), "search_graph",
			map[string]any{"name_pattern": toks[0], "project": proj, "limit": limit}); err == nil && c.found(near) {
			cands = payloadOf(near)
		}
	}
	cov, err := c.coverageData(cmd.Context(), proj)
	if err != nil {
		return fail("no exact hit and coverage check failed: %v — absence unverified", err)
	}
	return c.outEnvelope(cmd, proj,
		map[string]any{"match": nil, "near_miss": cands, "coverage": cov}, fields)
}

func cmdValidate(g *Globals) *cobra.Command {
	var project, sym string
	var limit int
	var sel bool
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
			proj, err := requireProject(ctx, project, sel)
			if err != nil {
				return err
			}
			if _, _, err := ctx.needCBM(cmd.Context()); err != nil {
				return err
			}
			fields := map[string]any{"symbol": sym, "project": proj}
			// One search per face, asked for in that face's own dialect. The
			// verdict is read the same way on both, but a human is shown the
			// rendered tree and a machine is handed the payload, so asking for
			// format:"json" on the human path would replace the tree it reads
			// with the JSON encoding of the same rows.
			search := map[string]any{"name_pattern": cbmexec.LeafName(sym), "project": proj, "limit": limit}
			if ctx.G.JSON {
				res, err := ctx.cbmCallStructured(cmd.Context(), "search_graph", search)
				if err != nil {
					return fail("%v", err)
				}
				hit := cbmexec.MatchSymbol(cbmexec.QualifiedNames(res.Data), sym)
				if hit == "" {
					fields["valid"] = false
					return ctx.validateMiss(cmd, proj, sym, limit, fields)
				}
				fields["valid"] = true
				fields["match"] = hit
				return ctx.outEnvelope(cmd, proj,
					map[string]any{"match": hit, "results": payloadOf(res)}, fields)
			}
			out, err := ctx.cbmCallJSON(cmd.Context(), "search_graph", search)
			if err != nil {
				return fail("%v", err)
			}
			hit := cbmexec.MatchSymbol(cbmexec.QualifiedNamesFromText(out), sym)
			if hit != "" {
				fields["valid"] = true
				fields["match"] = hit
				text := fmt.Sprintf("valid: %q found in %s\n%s", sym, proj, out)
				return ctx.outFresh(cmd, proj, cbmexec.Truncate(text, ctx.budget("")), fields)
			}
			fields["valid"] = false
			// A miss is a negative claim, so it is only allowed to stand once
			// coverage agrees. Near-miss candidates are a convenience, not
			// evidence: they are searched for separately and reported apart.
			candText := "(no candidates)"
			if toks := cbmexec.NearMissTokens(sym); len(toks) > 0 {
				if near, nerr := ctx.cbmCallJSON(cmd.Context(), "search_graph",
					map[string]any{"name_pattern": toks[0], "project": proj, "limit": limit}); nerr == nil && !cbmexec.LooksEmpty(near) {
					candText = near
				}
			}
			verdict, verr := coverageVerdict(cmd.Context(), ctx, proj)
			if verr != nil {
				return fail("no exact hit and coverage check failed: %v — absence unverified", verr)
			}
			text := fmt.Sprintf("invalid: %q not found in %s\n== near-miss ==\n%s\n(%s)", sym, proj, candText, verdict)
			return ctx.outFresh(cmd, proj, cbmexec.Truncate(text, ctx.budget("")), fields)
		},
	}
	c.Flags().StringVar(&project, "project", "", "project name")
	c.Flags().StringVar(&sym, "symbol", "", "symbol to validate")
	c.Flags().IntVar(&limit, "limit", 5, "max rows per lookup")
	selectFlag(c, &sel)
	_ = c.MarkFlagRequired("symbol")
	_ = c.RegisterFlagCompletionFunc("project", projectFlagCompletion(g))
	return c
}
