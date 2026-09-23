package cli

import (
	"context"

	"github.com/spf13/cobra"
	"github.com/true-knowledge/tk/internal/mcp"
)

func cmdMCP(g *Globals) *cobra.Command {
	var allowWrite bool
	c := &cobra.Command{
		Use:   "mcp",
		Short: "Run MCP stdio proxy (scout + snippet + source_search), stdin EOF = instant exit",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, err := load(*g)
			if err != nil {
				return err
			}
			_ = allowWrite
			budget := ctx.budget("")
			s := &mcp.Server{Budget: budget}
			if ctx.CBMOK {
				s.Run = ctx.Run
			}
			paths := ctx.Paths
			s.ShardsFor = paths.ZoektShards
			code := s.Serve(context.Background())
			_ = code
			return nil
		},
	}
	c.Flags().BoolVar(&allowWrite, "allow-write", false, "reserved: index_repository stays approval-gated (no-op this build)")
	return c
}
