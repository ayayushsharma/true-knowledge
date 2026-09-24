package cli

import (
	"strings"

	"github.com/ayayushsharma/true-knowledge/internal/cbmexec"
	"github.com/spf13/cobra"
)

// cmdDaemon is a CLI-ONLY passthrough to `cbm daemon ...`.
// Model-facing MCP must never control daemon lifecycle: stopping the shared
// daemon mid-session would strand other agents' watchers and index jobs.
// This exists for one documented flow: applying `watcher_enabled` flips,
// which CBM reads once at daemon start.
func cmdDaemon(g *Globals) *cobra.Command {
	c := &cobra.Command{
		Use:   "daemon",
		Short: "Inspect/stop the shared CBM daemon (human use only, never MCP)",
	}
	for _, sub := range []string{"status", "stop"} {
		sub := sub
		c.AddCommand(&cobra.Command{
			Use:   sub,
			Short: "cbm daemon " + sub,
			Args:  cobra.NoArgs,
			RunE: func(cmd *cobra.Command, args []string) error {
				ctx, err := load(*g)
				if err != nil {
					return err
				}
				if _, _, err := ctx.needCBM(cmd.Context()); err != nil {
					return err
				}
				out, err := ctx.cbmDaemon(cmd.Context(), "daemon", sub)
				// A daemon that reports state on a nonzero exit ("daemon: not
				// running") answered the question: print its report, exit 0.
				// Only a total lack of output is a real failure.
				if strings.TrimSpace(out) != "" {
					return ctx.out(cmd, cbmexec.Truncate(strings.TrimRight(out, "\n"), ctx.budget("")), nil)
				}
				if err != nil {
					return fail("%v", err)
				}
				return ctx.out(cmd, cbmexec.Truncate(out, ctx.budget("")), nil)
			},
		})
	}
	return c
}
