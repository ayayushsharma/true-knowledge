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
				// The exit code is the verdict. CBM exits nonzero both when it
				// answers ("daemon: not running") and when it refuses (clients are
				// committed), and those are opposite outcomes, so a nonzero exit
				// cannot be collapsed into one answer the way a nonempty stdout
				// could be. 01-ARCHITECTURE.md is explicit that a refusal is a
				// failed command, and that no flag overrides it.
				text := strings.TrimRight(strings.TrimSpace(out), "\n")
				if err != nil && !daemonAbsent(out) {
					// A refusal names the committed pids, and those pids are the
					// fix, so they reach stdout whole — untruncated, because a
					// clipped pid list is a wrong answer. An output shape tk does
					// not recognise also lands here, which is the safe direction
					// for a stop: refuse rather than report a success nobody can
					// account for.
					if text != "" {
						return ctx.outFailed(cmd, text, map[string]any{"sub": sub}, err)
					}
					return fail("%v", err)
				}
				return ctx.out(cmd, cbmexec.Truncate(text, ctx.budget("")), nil)
			},
		})
	}
	return c
}

// daemonAbsent reports that the engine answered "nothing is running" rather than
// failing. `daemon status` exits nonzero when stopped and `daemon stop` is
// idempotent, so a nonzero exit carrying this line is a reply, not a fault, and
// tk reports it at exit 0.
//
// Matched narrowly, on purpose, and never by negating daemonActive: an
// unrecognised shape must read as a failure, because the alternative lets a
// reworded refusal exit 0. The shape is pinned by TestDaemonStatusProse, so a
// CBM release that changes it breaks a test instead of a verdict.
func daemonAbsent(out string) bool {
	return strings.Contains(out, "daemon: not running")
}
