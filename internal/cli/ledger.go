package cli

import (
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/ayayushsharma/true-knowledge/internal/memory"
	"github.com/ayayushsharma/true-knowledge/internal/trace"
	"github.com/spf13/cobra"
)

func cmdLedger(g *Globals) *cobra.Command {
	c := &cobra.Command{
		Use:   "ledger",
		Short: "Per-project working truths (append-only, five keys, history, human-only prune)",
	}
	c.AddCommand(ledgerUpdate(g), ledgerGet(g), ledgerHistory(g), ledgerPrune(g))
	return c
}

func openLedger(ctx *Ctx) (*memory.Ledger, error) {
	return memory.OpenLedger(ctx.Paths.LedgerDir())
}

func ledgerUpdate(g *Globals) *cobra.Command {
	return &cobra.Command{
		Use:   "update <project> <key> <value>",
		Short: "Append one immutable ledger entry (key=goal|next|done|decisions|open_questions)",
		Example: `  tk ledger update demo goal "build a codebase-cognizant agent"
  tk ledger update demo done "shipped picker + man pages"`,
		Args: cobra.ExactArgs(3),
		ValidArgsFunction: func(cmd *cobra.Command, args []string, _ string) ([]string, cobra.ShellCompDirective) {
			if ctx, err := load(*g); err == nil {
				switch len(args) {
				case 0:
					return ctx.projectNames(), cobra.ShellCompDirectiveNoFileComp
				case 1:
					return memory.LedgerKeys, cobra.ShellCompDirectiveNoFileComp
				}
			}
			return nil, cobra.ShellCompDirectiveNoFileComp
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, err := load(*g)
			if err != nil {
				return err
			}
			project, key, value := args[0], args[1], args[2]
			if !ctx.Cfg.Ledger.Enabled {
				return fail("%v", memory.ErrLedgerDisabled)
			}
			l, err := openLedger(ctx)
			if err != nil {
				return fail("open ledger: %v", err)
			}
			t0 := time.Now()
			e, err := l.Append(project, key, value)
			detail := fmt.Sprintf("project=%s key=%s", project, key)
			if err == nil {
				detail = fmt.Sprintf("%s seq=%d", detail, e.Seq)
			}
			ctx.record(trace.Event{Backend: "memory", Op: "ledger:update", Ms: sinceMs(t0), OK: err == nil, Detail: detail})
			if err != nil {
				return fail("ledger update: %v", err)
			}
			return ctx.out(cmd, fmt.Sprintf("appended %s/%s (seq %d)", project, key, e.Seq),
				map[string]any{"project": project, "key": key, "seq": e.Seq, "entry": e})
		},
	}
}

func ledgerGet(g *Globals) *cobra.Command {
	return &cobra.Command{
		Use:   "get [project]",
		Short: "Current ledger state: latest winning value per key (capped to budgets.ledger_chars; history holds the rest)",
		Example: `  tk ledger get
  tk ledger get demo`,
		Args: cobra.MaximumNArgs(1),
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
			l, err := openLedger(ctx)
			if err != nil {
				return fail("open ledger: %v", err)
			}
			if len(args) == 1 {
				return renderLedgerGet(ctx, cmd, l, args[0])
			}
			projects, err := l.Projects()
			if err != nil {
				return fail("ledger get: %v", err)
			}
			if len(projects) == 0 {
				return ctx.out(cmd, "no ledgers yet (use `tk ledger update <project> <key> <value>`)",
					map[string]any{"ledgers": map[string]any{}})
			}
			var b strings.Builder
			all := map[string]any{}
			for _, p := range projects {
				fold, err := renderFold(ctx, p)
				if err != nil {
					return fail("ledger get: %v", err)
				}
				all[p] = fold
				if len(fold) > 0 {
					fmt.Fprintf(&b, "=== %s ===\n%s", p, foldText(fold))
				}
			}
			return ctx.out(cmd, strings.TrimRight(b.String(), "\n"), map[string]any{"ledgers": all})
		},
	}
}

func ledgerHistory(g *Globals) *cobra.Command {
	return &cobra.Command{
		Use:     "history <project>",
		Short:   "Complete ledger log: every entry, chronological, uncapped (the audit trail)",
		Example: `  tk ledger history demo`,
		Args:    cobra.ExactArgs(1),
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
			l, err := openLedger(ctx)
			if err != nil {
				return fail("open ledger: %v", err)
			}
			entries, err := l.History(args[0])
			if err != nil {
				return fail("ledger history: %v", err)
			}
			if len(entries) == 0 {
				return ctx.out(cmd, fmt.Sprintf("ledger %s is empty (use `tk ledger update %s goal \"...\"` to start)", args[0], args[0]),
					map[string]any{"project": args[0], "entries": []memory.LedgerEntry{}})
			}
			var b strings.Builder
			for _, e := range entries {
				fmt.Fprintf(&b, "%d  %s  %s  %s\n", e.Seq, e.TS, e.Key, e.Value)
			}
			return ctx.out(cmd, strings.TrimRight(b.String(), "\n"), map[string]any{"project": args[0], "entries": entries})
		},
	}
}

func ledgerPrune(g *Globals) *cobra.Command {
	return &cobra.Command{
		Use:     "prune <project>",
		Short:   "Delete a project ledger permanently — interactive TTY confirmation only (no MCP tool; humans-only)",
		Example: `  tk ledger prune demo`,
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, err := load(*g)
			if err != nil {
				return err
			}
			project := args[0]
			yes, err := confirmPrune(cmd.OutOrStdout(), os.Stdin, ctx.G.JSON, isCharDevice(os.Stdin), isCharDevice(os.Stdout), project)
			if err != nil {
				return err
			}
			if !yes {
				return ctx.out(cmd, "prune cancelled", map[string]any{"project": project, "pruned": false})
			}
			l, err := openLedger(ctx)
			if err != nil {
				return fail("open ledger: %v", err)
			}
			t0 := time.Now()
			err = l.Prune(project)
			ctx.record(trace.Event{Backend: "memory", Op: "ledger:prune", Ms: sinceMs(t0), OK: err == nil, Detail: "project=" + project})
			if err != nil {
				return fail("ledger prune: %v", err)
			}
			return ctx.out(cmd, fmt.Sprintf("pruned ledger %s", project), map[string]any{"project": project, "pruned": true})
		},
	}
}

// confirmPrune is the injectable human gate: JSON mode or a non-TTY context is
// a hard refusal (there is no --yes); on a TTY the human must literally type
// "yes". Returns (confirmed, nil) or a refusal error.
func confirmPrune(w io.Writer, r io.Reader, json, stdinTTY, stdoutTTY bool, project string) (bool, error) {
	if json || !stdinTTY || !stdoutTTY {
		return false, fail("refusing to prune off an interactive terminal (no --yes exists; agents cannot delete — type `tk ledger prune %s` in a terminal)", project)
	}
	fmt.Fprintf(w, "prune the entire ledger for %q forever? type \"yes\" to confirm: ", project)
	var line string
	if _, err := fmt.Fscanln(r, &line); err != nil {
		return false, fail("prune aborted: %v", err)
	}
	return strings.TrimSpace(line) == "yes", nil
}

// renderFold returns the capped winning entries for one project.
func renderFold(ctx *Ctx, project string) (map[string]memory.LedgerEntry, error) {
	l, err := openLedger(ctx)
	if err != nil {
		return nil, err
	}
	return l.Get(project, ctx.Cfg.Budgets.LedgerChars)
}

func renderLedgerGet(ctx *Ctx, cmd *cobra.Command, l *memory.Ledger, project string) error {
	fold, err := l.Get(project, ctx.Cfg.Budgets.LedgerChars)
	if err != nil {
		return fail("ledger get: %v", err)
	}
	fields := map[string]any{"project": project, "keys": fold}
	if len(fold) == 0 {
		return ctx.out(cmd, fmt.Sprintf("ledger %s is empty (use `tk ledger update %s goal \"...\"` to start)", project, project), fields)
	}
	return ctx.out(cmd, strings.TrimRight(foldText(fold), "\n"), fields)
}

// foldText renders winning values in canonical key order.
func foldText(fold map[string]memory.LedgerEntry) string {
	var b strings.Builder
	for _, k := range memory.LedgerKeys {
		if e, ok := fold[k]; ok {
			fmt.Fprintf(&b, "%s  %s\n", k, e.Value)
		}
	}
	return b.String()
}
