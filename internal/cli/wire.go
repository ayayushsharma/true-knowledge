package cli

import (
	"github.com/spf13/cobra"
)

// NewRoot builds the full tk command tree.
func NewRoot(g *Globals) *cobra.Command {
	root := &cobra.Command{
		Use:   "tk",
		Short: "Thin Go shipper over codebase-memory-mcp (Linux-first)",
		Long: `tk owns paths + config + spawn + render. CBM owns graph, store, daemon, watcher.
All graph work = codebase-memory-mcp cli <tool> --json via one wrapper.`,
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.PersistentFlags().StringVar(&g.Home, "home", "", "override base home (TK_HOME equivalent: config+data+cache+state under it)")
	root.PersistentFlags().BoolVar(&g.JSON, "json", false, "stable JSON envelope output")
	root.PersistentFlags().IntVar(&g.Budget, "budget", 0, "char budget override (0 = config defaults)")

	root.AddCommand(
		cmdInit(g),
		cmdRegister(g),
		cmdIndex(g),
		cmdSync(g),
		cmdStatus(g),
		cmdFind(g),
		cmdSearch(g),
		cmdSourceSearch(g),
		cmdExplain(g),
		cmdGrep(g),
		cmdArch(g),
		cmdQuery(g),
		cmdCBM(g),
		cmdMCP(g),
		cmdConfig(g),
		cmdMigrate(g),
		cmdInstall(g),
		cmdSetup(g),
		cmdMCPInstall(g),
	)
	return root
}
