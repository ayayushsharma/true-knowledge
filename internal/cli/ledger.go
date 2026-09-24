package cli

import (
	"fmt"
	"strings"
	"time"

	"github.com/ayayushsharma/true-knowledge/internal/memory"
	"github.com/ayayushsharma/true-knowledge/internal/trace"
	"github.com/spf13/cobra"
)

func cmdLedger(g *Globals) *cobra.Command {
	c := &cobra.Command{
		Use:   "ledger",
		Short: "Per-project working truth blobs (bounded, full-text replace)",
	}
	c.AddCommand(ledgerGet(g), ledgerUpdate(g))
	return c
}

func openLedger(ctx *Ctx) (*memory.Ledger, error) {
	return memory.OpenLedger(ctx.Paths.LedgerDir())
}

func ledgerGet(g *Globals) *cobra.Command {
	return &cobra.Command{
		Use:   "get [project]",
		Short: "Print one project ledger, or all ledgers",
		Example: `  tk ledger get
  tk ledger get demo`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, err := load(*g)
			if err != nil {
				return err
			}
			l, err := openLedger(ctx)
			if err != nil {
				return fail("open ledger: %v", err)
			}
			if len(args) == 1 {
				text, err := l.Get(args[0])
				if err != nil {
					return fail("ledger get: %v", err)
				}
				return ctx.out(cmd, text, map[string]any{"project": args[0], "ledger": text})
			}
			all, err := l.All()
			if err != nil {
				return fail("ledger get: %v", err)
			}
			if len(all) == 0 {
				return ctx.out(cmd, "no ledgers yet (use `tk ledger update <project> <text>`)", map[string]any{"ledgers": []memory.LedgerEntry{}})
			}
			var b strings.Builder
			for _, e := range all {
				fmt.Fprintf(&b, "=== %s (updated %s) ===\n%s\n", e.Project, time.Unix(e.UpdatedAt, 0).Format("2006-01-02 15:04"), e.Text)
			}
			return ctx.out(cmd, strings.TrimRight(b.String(), "\n"), map[string]any{"ledgers": all})
		},
	}
}

func ledgerUpdate(g *Globals) *cobra.Command {
	return &cobra.Command{
		Use:     "update <project> <text>",
		Short:   "Replace a project ledger (truncated to budgets.ledger_chars)",
		Args:    cobra.MinimumNArgs(1),
		Example: `  tk ledger update demo "demo serves the public API; deploys weekly; owned by platform."`,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, err := load(*g)
			if err != nil {
				return err
			}
			project := args[0]
			text := strings.Join(args[1:], " ")
			if !ctx.Cfg.Ledger.Enabled {
				return fail("ledger is disabled (tk config set ledger.enabled true)")
			}
			l, err := openLedger(ctx)
			if err != nil {
				return fail("open ledger: %v", err)
			}
			t0 := time.Now()
			text, err = l.Update(project, text, ctx.Cfg.Budgets.LedgerChars)
			ctx.record(trace.Event{Backend: "memory", Op: "ledger:update", Ms: sinceMs(t0), OK: err == nil, Detail: fmt.Sprintf("project=%s chars=%d", project, len(text))})
			if err != nil {
				return fail("ledger update: %v", err)
			}
			return ctx.out(cmd, fmt.Sprintf("updated ledger for %s (%d chars)", project, len(text)),
				map[string]any{"project": project, "ledger": text})
		},
	}
}
