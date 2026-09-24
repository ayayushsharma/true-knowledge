package cli

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/ayayushsharma/true-knowledge/internal/memory"
	"github.com/ayayushsharma/true-knowledge/internal/trace"
	"github.com/spf13/cobra"
)

// openFacts opens the tk-owned facts store under <data>/mem.
func (c *Ctx) openFacts(ctx context.Context) (*memory.Facts, error) {
	return memory.OpenFacts(ctx, c.Paths.MemDir())
}

func cmdMem(g *Globals) *cobra.Command {
	c := &cobra.Command{
		Use:   "mem",
		Short: "Durable scoped facts (tk-owned memory, never needs CBM)",
	}
	c.AddCommand(memSave(g), memRecall(g), memReview(g))
	return c
}

func memSave(g *Globals) *cobra.Command {
	var project, scope, provenance string
	c := &cobra.Command{
		Use:   "save <topic> <value>",
		Short: "Store a fact; secret-looking values queue for review instead",
		Args:  cobra.ExactArgs(2),
		Example: `  tk mem save db postgres --project demo
  tk mem save deploy-cadence "2 week sprints" --scope global --provenance standup`,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, err := load(*g)
			if err != nil {
				return err
			}
			f, err := ctx.openFacts(cmd.Context())
			if err != nil {
				return fail("open facts store: %v", err)
			}
			defer f.Close()
			t0 := time.Now()
			fact, queued, err := f.Save(cmd.Context(), scope, project, args[0], args[1], provenance)
			ctx.record(trace.Event{Backend: "memory", Op: "mem:save", Ms: sinceMs(t0), OK: err == nil, Detail: fmt.Sprintf("topic=%s queued=%v", args[0], queued)})
			if err != nil {
				return fail("save fact: %v", err)
			}
			if queued {
				reasons := memory.DetectSecret(args[1])
				return ctx.out(cmd, fmt.Sprintf("queued for review (looks secret: %s); approval required before search: %s#%s",
					strings.Join(reasons, ","), displayScope(fact), fact.Topic),
					map[string]any{"queued": true, "topic": fact.Topic, "reason": reasons})
			}
			return ctx.out(cmd, fmt.Sprintf("stored fact %s#%s", displayScope(fact), fact.Topic),
				map[string]any{"queued": false, "fact": fact})
		},
	}
	c.Flags().StringVar(&project, "project", "", "project name (required for project scope)")
	c.Flags().StringVar(&scope, "scope", memory.ScopeProject, "global|project")
	c.Flags().StringVar(&provenance, "provenance", "", "origin note for the fact")
	return c
}

func memRecall(g *Globals) *cobra.Command {
	var project string
	c := &cobra.Command{
		Use:   "recall <topic>",
		Short: "Recall facts: named project first, then global (or all + global)",
		Args:  cobra.ExactArgs(1),
		Example: `  tk mem recall db --project demo
  tk mem recall api-base`,
		ValidArgsFunction: func(cmd *cobra.Command, args []string, _ string) ([]string, cobra.ShellCompDirective) {
			return nil, cobra.ShellCompDirectiveNoFileComp
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, err := load(*g)
			if err != nil {
				return err
			}
			f, err := ctx.openFacts(cmd.Context())
			if err != nil {
				return fail("open facts store: %v", err)
			}
			defer f.Close()
			t0 := time.Now()
			facts, err := f.Recall(cmd.Context(), project, args[0])
			ctx.record(trace.Event{Backend: "memory", Op: "mem:recall", Ms: sinceMs(t0), OK: err == nil, Detail: fmt.Sprintf("topic=%s matches=%d", args[0], len(facts))})
			if err != nil {
				return fail("recall: %v", err)
			}
			if len(facts) == 0 {
				return ctx.out(cmd, fmt.Sprintf("no fact for %q", args[0]), map[string]any{"topic": args[0], "facts": []memory.Fact{}})
			}
			var b strings.Builder
			for _, fc := range facts {
				prov := ""
				if fc.Provenance != "" {
					prov = "  (" + fc.Provenance + ")"
				}
				fmt.Fprintf(&b, "%s#%s  %s%s\n", displayScope(fc), fc.Topic, fc.Value, prov)
			}
			return ctx.out(cmd, strings.TrimRight(b.String(), "\n"), map[string]any{"topic": args[0], "facts": facts})
		},
	}
	c.Flags().StringVar(&project, "project", "", "narrow recall to one project (default: all + global)")
	return c
}

func memReview(g *Globals) *cobra.Command {
	c := &cobra.Command{
		Use:   "review",
		Short: "Approve/reject captured facts (never stored until approved)",
	}
	c.AddCommand(
		&cobra.Command{
			Use:   "list",
			Short: "List pending facts",
			RunE: func(cmd *cobra.Command, args []string) error {
				ctx, err := load(*g)
				if err != nil {
					return err
				}
				f, err := ctx.openFacts(cmd.Context())
				if err != nil {
					return fail("open facts store: %v", err)
				}
				defer f.Close()
				entries, err := f.PendingReviews(cmd.Context())
				if err != nil {
					return fail("list review: %v", err)
				}
				if len(entries) == 0 {
					return ctx.out(cmd, "no pending fact reviews", map[string]any{"reviews": []memory.ReviewEntry{}})
				}
				var b strings.Builder
				for _, e := range entries {
					key := e.Topic
					if e.Project != "" {
						key = e.Project + "#" + e.Topic
					}
					fmt.Fprintf(&b, "%s  %s  [%s]  value=%s\n", e.ID, displayScope(memory.Fact{Scope: e.Scope, Project: e.Project}), strings.Join(e.Reason, ","), e.Value)
					_ = key
				}
				return ctx.out(cmd, strings.TrimRight(b.String(), "\n"), map[string]any{"reviews": entries})
			},
		},
		func() *cobra.Command {
			return &cobra.Command{
				Use:   "approve <id>",
				Short: "Promote one captured fact into the store",
				Args:  cobra.ExactArgs(1),
				RunE: func(cmd *cobra.Command, args []string) error {
					ctx, err := load(*g)
					if err != nil {
						return err
					}
					f, err := ctx.openFacts(cmd.Context())
					if err != nil {
						return fail("open facts store: %v", err)
					}
					defer f.Close()
					fact, err := f.Approve(cmd.Context(), args[0])
					ctx.record(trace.Event{Backend: "memory", Op: "mem:review", Ms: sinceMs(time.Now()), OK: err == nil, Detail: "action=approve"})
					if err != nil {
						return fail("approve: %v", err)
					}
					return ctx.out(cmd, fmt.Sprintf("approved %s#%s", displayScope(fact), fact.Topic), map[string]any{"approved": fact})
				},
			}
		}(),
		func() *cobra.Command {
			return &cobra.Command{
				Use:   "reject <id>",
				Short: "Drop one captured fact without storing anything",
				Args:  cobra.ExactArgs(1),
				RunE: func(cmd *cobra.Command, args []string) error {
					ctx, err := load(*g)
					if err != nil {
						return err
					}
					f, err := ctx.openFacts(cmd.Context())
					if err != nil {
						return fail("open facts store: %v", err)
					}
					defer f.Close()
					if err := f.Reject(cmd.Context(), args[0]); err != nil {
						return fail("reject: %v", err)
					}
					ctx.record(trace.Event{Backend: "memory", Op: "mem:review", Ms: sinceMs(time.Now()), OK: true, Detail: "action=reject"})
					return ctx.out(cmd, fmt.Sprintf("rejected %s", args[0]), map[string]any{"rejected": args[0]})
				},
			}
		}(),
	)
	return c
}

func displayScope(f memory.Fact) string {
	if f.Scope == memory.ScopeGlobal {
		return "global"
	}
	if f.Project == "" {
		return "project"
	}
	return f.Project
}
