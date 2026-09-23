package cli

import (
	"context"

	"github.com/spf13/cobra"
	"github.com/true-knowledge/tk/internal/mcp"
)

func cmdMCP(g *Globals) *cobra.Command {
	var profile string
	c := &cobra.Command{
		Use:   "mcp",
		Short: "Run MCP stdio proxy (profile: scout|analysis|minimal), stdin EOF = instant exit",
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
			code := s.Serve(context.Background())
			_ = code
			return nil
		},
	}
	c.Flags().StringVar(&profile, "tool-profile", mcp.ProfileScout,
		"tool surface: scout (11) | analysis (14) | minimal (3)")
	return c
}
