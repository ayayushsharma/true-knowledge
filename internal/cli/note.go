package cli

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/true-knowledge/tk/internal/memory"
	"github.com/true-knowledge/tk/internal/trace"
)

func (c *Ctx) openNotes(ctx context.Context) (*memory.Notes, error) {
	n, err := memory.OpenNotes(ctx, c.Paths.NotesDir())
	if err != nil {
		return nil, err
	}
	if c.Cfg.Embedding.Enabled {
		n.SetEmbedder(memory.NewEmbedder(c.Cfg.Embedding.Endpoint, c.Cfg.Embedding.Model, c.Cfg.Embedding.TimeoutMS))
	}
	return n, nil
}

func cmdNote(g *Globals) *cobra.Command {
	c := &cobra.Command{
		Use:   "note",
		Short: "Capture/search project notes (tk-owned, review-gated, never needs CBM)",
	}
	c.AddCommand(noteSave(g), noteSearch(g), noteTOC(g), noteReindex(g), noteReview(g))
	return c
}

func noteSave(g *Globals) *cobra.Command {
	var project, bodyFile, text string
	c := &cobra.Command{
		Use:   "save <title>",
		Short: "Capture a note into the review queue (searchable after approval)",
		Args:  cobra.ExactArgs(1),
		Example: `  tk note save "deploy cadence" --text "releases ship weekly" --project demo
  tk note save "incident" --body ./postmortem.md --project demo`,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, err := load(*g)
			if err != nil {
				return err
			}
			if (bodyFile == "") == (text == "") {
				return fail("provide exactly one of --body or --text")
			}
			if bodyFile != "" {
				raw, err := os.ReadFile(bodyFile)
				if err != nil {
					return fail("read --body: %v", err)
				}
				text = string(raw)
			}
			n, err := ctx.openNotes(cmd.Context())
			if err != nil {
				return fail("open notes store: %v", err)
			}
			defer n.Close()
			t0 := time.Now()
			ent, err := n.SaveToReview(cmd.Context(), project, args[0], text)
			ctx.record(trace.Event{Backend: "memory", Op: "note:save", Ms: sinceMs(t0), OK: err == nil, Detail: fmt.Sprintf("project=%s title=%q chars=%d", project, args[0], len(text))})
			if err != nil {
				return fail("capture note: %v", err)
			}
			noteText := fmt.Sprintf("captured note %q for review (id %s); approve to make searchable", ent.Title, ent.ID)
			fields := map[string]any{"queued": true, "note": ent}
			return ctx.out(cmd, noteText, fields)
		},
	}
	c.Flags().StringVar(&project, "project", "", "project name (required)")
	c.Flags().StringVar(&bodyFile, "body", "", "file whose contents become the note body")
	c.Flags().StringVar(&text, "text", "", "inline note body")
	return c
}

func noteSearch(g *Globals) *cobra.Command {
	var project string
	var limit int
	c := &cobra.Command{
		Use:   "search <query>",
		Short: "BM25 search over approved notes only",
		Args:  cobra.ExactArgs(1),
		Example: `  tk note search "http client" --project demo
  tk note search ` + "incident" + ` --limit 5`,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, err := load(*g)
			if err != nil {
				return err
			}
			n, err := ctx.openNotes(cmd.Context())
			if err != nil {
				return fail("open notes store: %v", err)
			}
			defer n.Close()
			t0 := time.Now()
			hits, err := n.Search(cmd.Context(), project, args[0], limit)
			ctx.record(trace.Event{Backend: "memory", Op: "note:search", Ms: sinceMs(t0), OK: err == nil, Detail: fmt.Sprintf("matches=%d", len(hits))})
			if err != nil {
				return fail("search notes: %v", err)
			}
			if len(hits) == 0 {
				return ctx.out(cmd, fmt.Sprintf("no approved notes match %q", args[0]), map[string]any{"query": args[0], "hits": []memory.NoteHit{}})
			}
			var b strings.Builder
			for _, h := range hits {
				fmt.Fprintf(&b, "%s  [%s]  %s\n", h.Note.Title, h.Note.Project, h.Excerpt)
			}
			return ctx.out(cmd, strings.TrimRight(b.String(), "\n"), map[string]any{"query": args[0], "hits": hits})
		},
	}
	c.Flags().StringVar(&project, "project", "", "narrow search to one project (default: all)")
	c.Flags().IntVar(&limit, "limit", 10, "max results (1-100)")
	return c
}

func noteTOC(g *Globals) *cobra.Command {
	var project string
	c := &cobra.Command{
		Use:   "toc",
		Short: "List note titles only, budgeted (never bodies)",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, err := load(*g)
			if err != nil {
				return err
			}
			n, err := ctx.openNotes(cmd.Context())
			if err != nil {
				return fail("open notes store: %v", err)
			}
			defer n.Close()
			lines, notes, err := n.TOC(cmd.Context(), project, ctx.budget("notes_toc"))
			if err != nil {
				return fail("note toc: %v", err)
			}
			if len(lines) == 0 {
				return ctx.out(cmd, "no approved notes yet (see `tk note review list`)", map[string]any{"notes": []memory.Note{}})
			}
			return ctx.out(cmd, strings.Join(lines, "\n"), map[string]any{"notes": notes})
		},
	}
	c.Flags().StringVar(&project, "project", "", "limit to one project")
	return c
}

func noteReindex(g *Globals) *cobra.Command {
	var project string
	c := &cobra.Command{
		Use:   "reindex [project]",
		Short: "Rebuild FTS index from the markdown sources of truth",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, err := load(*g)
			if err != nil {
				return err
			}
			if len(args) == 1 {
				project = args[0]
			}
			n, err := ctx.openNotes(cmd.Context())
			if err != nil {
				return fail("open notes store: %v", err)
			}
			defer n.Close()
			t0 := time.Now()
			count, err := n.Reindex(cmd.Context(), project)
			ctx.record(trace.Event{Backend: "memory", Op: "note:reindex", Ms: sinceMs(t0), OK: err == nil, Detail: fmt.Sprintf("notes=%d", count)})
			if err != nil {
				return fail("reindex notes: %v", err)
			}
			return ctx.out(cmd, fmt.Sprintf("reindexed %d note(s)", count), map[string]any{"notes": count})
		},
	}
	c.Flags().StringVar(&project, "project", "", "limit to one project (or pass as arg)")
	return c
}

func noteReview(g *Globals) *cobra.Command {
	c := &cobra.Command{
		Use:   "review",
		Short: "Approve/reject captured notes",
	}
	c.AddCommand(
		&cobra.Command{
			Use:   "list",
			Short: "List captured notes waiting for approval",
			RunE: func(cmd *cobra.Command, args []string) error {
				ctx, err := load(*g)
				if err != nil {
					return err
				}
				n, err := ctx.openNotes(cmd.Context())
				if err != nil {
					return fail("open notes store: %v", err)
				}
				defer n.Close()
				entries, err := n.PendingReviews(cmd.Context())
				if err != nil {
					return fail("list note reviews: %v", err)
				}
				if len(entries) == 0 {
					return ctx.out(cmd, "no pending note reviews", map[string]any{"reviews": []memory.NoteReviewEntry{}})
				}
				var b strings.Builder
				for _, e := range entries {
					b.WriteString(fmt.Sprintf("%s  %s  %q  %d chars\n", e.ID, e.Project, e.Title, len(e.Text)))
				}
				return ctx.out(cmd, strings.TrimRight(b.String(), "\n"), map[string]any{"reviews": entries})
			},
		},
		func() *cobra.Command {
			return &cobra.Command{
				Use:   "approve <id>",
				Short: "Write + index one captured note",
				Args:  cobra.ExactArgs(1),
				RunE: func(cmd *cobra.Command, args []string) error {
					ctx, err := load(*g)
					if err != nil {
						return err
					}
					n, err := ctx.openNotes(cmd.Context())
					if err != nil {
						return fail("open notes store: %v", err)
					}
					defer n.Close()
					note, err := n.Approve(cmd.Context(), args[0])
					ctx.record(trace.Event{Backend: "memory", Op: "note:review", Ms: sinceMs(time.Now()), OK: err == nil, Detail: "action=approve"})
					if err != nil {
						return fail("approve note: %v", err)
					}
					return ctx.out(cmd, fmt.Sprintf("approved %q (%s)", note.Title, note.File), map[string]any{"approved": note})
				},
			}
		}(),
		func() *cobra.Command {
			return &cobra.Command{
				Use:   "reject <id>",
				Short: "Drop one captured note (nothing written/indexed)",
				Args:  cobra.ExactArgs(1),
				RunE: func(cmd *cobra.Command, args []string) error {
					ctx, err := load(*g)
					if err != nil {
						return err
					}
					n, err := ctx.openNotes(cmd.Context())
					if err != nil {
						return fail("open notes store: %v", err)
					}
					defer n.Close()
					if err := n.Reject(cmd.Context(), args[0]); err != nil {
						return fail("reject note: %v", err)
					}
					ctx.record(trace.Event{Backend: "memory", Op: "note:review", Ms: sinceMs(time.Now()), OK: true, Detail: "action=reject"})
					return ctx.out(cmd, fmt.Sprintf("rejected %s", args[0]), map[string]any{"rejected": args[0]})
				},
			}
		}(),
	)
	return c
}
