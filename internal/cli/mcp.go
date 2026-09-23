package cli

import (
	"context"

	"github.com/spf13/cobra"
	"github.com/true-knowledge/tk/internal/mcp"
	"github.com/true-knowledge/tk/internal/memory"
)

func cmdMCP(g *Globals) *cobra.Command {
	var profile string
	c := &cobra.Command{
		Use:   "mcp",
		Short: "Run MCP stdio proxy (profile: scout|analysis|minimal|memory), stdin EOF = instant exit",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, err := load(*g)
			if err != nil {
				return err
			}
			budget := ctx.budget("")
			s := &mcp.Server{Budget: budget, Profile: profile}
			if ctx.CBMOK {
				s.Run = ctx.Run
			}
			paths := ctx.Paths
			s.ShardsFor = paths.ZoektShards
			s.LogPath = ctx.Paths.LogFile()
			store, err := ctx.memoryStore(cmd.Context())
			if err == nil {
				s.Mem = store
			}
			_ = s.Serve(context.Background())
			return nil
		},
	}
	c.Flags().StringVar(&profile, "tool-profile", mcp.ProfileScout,
		"tool surface: scout (11) | analysis (14) | minimal (3) | memory (20)")
	_ = c.RegisterFlagCompletionFunc("tool-profile", func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
		return []string{mcp.ProfileScout, mcp.ProfileAnalysis, mcp.ProfileMinimal, mcp.ProfileMemory}, cobra.ShellCompDirectiveNoFileComp
	})
	return c
}

// memoryStore opens the three memory sub-stores wired for the memory profile.
func (c *Ctx) memoryStore(ctx context.Context) (*memory.Store, error) {
	facts, err := memory.OpenFacts(ctx, c.Paths.MemDir())
	if err != nil {
		return nil, err
	}
	notes, err := memory.OpenNotes(ctx, c.Paths.NotesDir())
	if err != nil {
		return nil, err
	}
	if c.Cfg.Embedding.Enabled {
		notes.SetEmbedder(memory.NewEmbedder(c.Cfg.Embedding.Endpoint, c.Cfg.Embedding.Model, c.Cfg.Embedding.TimeoutMS))
	}
	ledger, err := memory.OpenLedger(c.Paths.LedgerDir())
	if err != nil {
		return nil, err
	}
	return &memory.Store{
		Facts:          facts,
		Notes:          notes,
		Ledger:         ledger,
		LedgerBudget:   c.Cfg.Budgets.LedgerChars,
		NotesTocBudget: c.Cfg.Budgets.NotesTocChars,
	}, nil
}
