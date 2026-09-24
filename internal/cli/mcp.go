package cli

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/ayayushsharma/true-knowledge/internal/config"
	"github.com/ayayushsharma/true-knowledge/internal/mcp"
	"github.com/ayayushsharma/true-knowledge/internal/memory"
	"github.com/spf13/cobra"
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
			profile, err := resolveProfile(cmd.Flags().Changed("tool-profile"), profile, os.Getenv("TK_MCP_PROFILE"), ctx.Cfg.MCPProfile)
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
			// source_search freshness contract: refresh before searching,
			// live-slice results, and annotate any remaining staleness.
			s.EnsureIndex = func(project string) error {
				p, ok := ctx.Reg[project]
				if !ok {
					return fmt.Errorf("unknown project %q", project)
				}
				if _, zerr := ctx.ensureZoektAndTouch(cmd.Context(), project, p.Path, ctx.Cfg.IndexMode); zerr != nil {
					return zerr
				}
				return ctx.saveReg()
			}
			s.Staleness = func(project string) string {
				p, ok := ctx.Reg[project]
				if !ok {
					return ""
				}
				note, _, _ := zoektStaleNote(p)
				return note
			}
			s.ProjectRoot = func(project string) string {
				if p, ok := ctx.Reg[project]; ok {
					return p.Path
				}
				return ""
			}
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
		"tool surface: scout (11) | analysis (14) | minimal (3) | memory (22); explicit flag beats TK_MCP_PROFILE and config mcp.profile")
	_ = c.RegisterFlagCompletionFunc("tool-profile", func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
		return config.ValidProfiles(), cobra.ShellCompDirectiveNoFileComp
	})
	return c
}

const mcpProfileEnv = "TK_MCP_PROFILE"

// resolveProfile picks the MCP tool profile by precedence: an explicitly-set
// --tool-profile flag wins, then the TK_MCP_PROFILE env var, then the config
// mcp.profile machine default, then scout. An invalid value at any level is a
// hard error (never a silent fallback) so a typo'd env never silently exposes
// an agent to the wrong tool surface.
func resolveProfile(flagChanged bool, flagVal, envVal, cfgVal string) (string, error) {
	var candidate, source string
	switch {
	case flagChanged:
		candidate, source = flagVal, "--tool-profile"
	case envVal != "":
		candidate, source = envVal, "TK_MCP_PROFILE"
	case cfgVal != "":
		candidate, source = cfgVal, "mcp.profile"
	default:
		return mcp.ProfileScout, nil
	}
	for _, p := range config.ValidProfiles() {
		if candidate == p {
			return p, nil
		}
	}
	return "", fmt.Errorf("invalid %s %q (want %s)", source, candidate, strings.Join(config.ValidProfiles(), "|"))
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
		LedgerEnabled:  c.Cfg.Ledger.Enabled,
		LedgerBudget:   c.Cfg.Budgets.LedgerChars,
		NotesTocBudget: c.Cfg.Budgets.NotesTocChars,
	}, nil
}
