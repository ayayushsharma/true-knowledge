package cli

import (
	"context"

	"github.com/spf13/cobra"
	"github.com/true-knowledge/tk/internal/mcp"
)

func cmdMCP(g *Globals) *cobra.Command {
	c := &cobra.Command{
		Use:   "mcp",
		Short: "Run MCP stdio proxy (scout + snippet + source_search), stdin EOF = instant exit",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, err := load(*g)
			if err != nil {
				return err
			}
			budget := ctx.budget("")
			s := &mcp.Server{Budget: budget}
			if ctx.CBMOK {
				s.Run = ctx.Run
			}
			paths := ctx.Paths
			s.ShardsFor = paths.ZoektShards
			s.LogPath = ctx.Paths.LogFile()
			code := s.Serve(context.Background())
			_ = code
			return nil
		},
	}
	return c
}
