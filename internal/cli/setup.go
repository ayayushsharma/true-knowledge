package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	"github.com/true-knowledge/tk/internal/backends"
	"github.com/true-knowledge/tk/internal/config"
	"github.com/true-knowledge/tk/internal/installer"
	"github.com/true-knowledge/tk/internal/store"
)

// mcpSnippet renders the manual-paste MCP client snippet shared by
// mcp-install and setup.
func mcpSnippet(exe, client string, dry bool) (string, map[string]any, error) {
	switch client {
	case "pi":
		return "", nil, fail("Pi integration slipped this build (no reviewed extension yet); use --client opencode|claude|codex")
	case "opencode", "claude", "codex":
		snippet := map[string]any{
			"mcpServers": map[string]any{
				"tk": map[string]any{"command": exe, "args": []string{"mcp"}},
			},
		}
		raw, _ := json.MarshalIndent(snippet, "", "  ")
		target := map[string]string{
			"opencode": "~/.config/opencode/opencode.json (mcpServers.tk)",
			"claude":   "~/.claude.json (mcpServers.tk) — then restart + /mcp",
			"codex":    "$CODEX_HOME/config.toml [mcp_servers.tk]",
		}[client]
		if dry {
			return fmt.Sprintf("would add to %s:\n%s", target, raw), map[string]any{"dry_run": true}, nil
		}
		return fmt.Sprintf("add to %s:\n%s\n(Pi slipped; opencode/claude/codex manual paste this build — `cbm install --dry-run` parity next)", target, raw), nil, nil
	default:
		return "", nil, fail("unknown --client %q (want pi|opencode|claude|codex)", client)
	}
}

func cmdSetup(g *Globals) *cobra.Command {
	var doRegister bool
	var name, client string
	var dry bool
	c := &cobra.Command{
		Use:   "setup",
		Short: "Make this machine ready: init + install backends (+ optional register/client)",
		Long: `Idempotent fresh-machine setup. Re-running changes nothing when up-to-date.
Steps: init dirs/config → install all backends at pins → (opt-in) register cwd → (opt-in) client snippet.`,
		Example: `  tk setup
  tk setup --dry-run
  tk setup --register --name demo
  tk setup --client opencode`,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, err := load(*g)
			if err != nil {
				return err
			}
			sections := []string{}
			// 1. init (idempotent: only writes missing files).
			if _, err := os.Stat(ctx.Paths.ConfigFile()); os.IsNotExist(err) {
				if err := config.Save(ctx.Paths.ConfigFile(), ctx.Cfg); err != nil {
					return fail("write config: %v", err)
				}
			}
			if err := ctx.saveReg(); err != nil {
				return fail("write registry: %v", err)
			}
			sections = append(sections, fmt.Sprintf("init: %s", ctx.Paths.Config))
			// 2. backends at pins.
			for _, b := range backends.All() {
				pin := ctx.Cfg.CBMVersionPin
				if pin == "" {
					pin = config.DefaultCBMPin
				}
				if dry {
					plan, perr := installer.ResolvePlan(ctx.Paths.Cache, b, pin, backends.HostGOOS(), backends.HostGOARCH())
					url := plan.ArchiveURL
					if perr != nil {
						url = perr.Error()
					}
					sections = append(sections, fmt.Sprintf("install %s: dry-run pin %s %s", b.Name, pin, url))
					continue
				}
				st := installer.Inspect(cmd.Context(), ctx.Paths.Cache, b, pin, backends.HostGOOS())
				if st.UpToDate && st.Path != "" {
					sections = append(sections, fmt.Sprintf("install %s: up-to-date %s (%s)", b.Name, st.InstalledVersion, st.Path))
					continue
				}
				plan, err := installer.Install(cmd.Context(), ctx.Paths.Cache, b, pin, backends.HostGOOS(), backends.HostGOARCH())
				if err != nil {
					sections = append(sections, fmt.Sprintf("install %s: FAILED %v (agent continues fail-open)", b.Name, err))
					continue
				}
				sections = append(sections, fmt.Sprintf("install %s: installed %s -> %s", b.Name, pin, plan.Dest))
			}
			// 3. opt-in register of cwd.
			if doRegister {
				cwd, err := os.Getwd()
				if err != nil {
					return fail("cwd: %v", err)
				}
				abs := cwd
				if rp, err := filepath.EvalSymlinks(abs); err == nil {
					abs = rp
				}
				if name == "" {
					name = filepath.Base(abs)
				}
				norm, err := store.NormalizeName(name)
				if err != nil {
					return fail("%v", err)
				}
				if _, exists := ctx.Reg[norm]; exists {
					return fail("project %q already registered; pick --name", norm)
				}
				ctx.Reg[norm] = store.Project{Path: abs, Mode: ctx.Cfg.IndexMode}
				if err := ctx.saveReg(); err != nil {
					return fail("save registry: %v", err)
				}
				sections = append(sections, fmt.Sprintf("register: %q -> %s", norm, abs))
			}
			// 4. opt-in client snippet.
			if client != "" {
				exe, err := os.Executable()
				if err != nil {
					exe = "tk"
				}
				text, _, err := mcpSnippet(exe, client, dry)
				if err != nil {
					return err
				}
				sections = append(sections, "client:\n"+text)
			}
			return ctx.out(cmd, strings.Join(sections, "\n"), nil)
		},
	}
	c.Flags().BoolVar(&doRegister, "register", false, "register the current directory as a project (opt-in)")
	c.Flags().StringVar(&name, "name", "", "project name for --register (default: directory base)")
	c.Flags().StringVar(&client, "client", "", "print MCP snippet for pi|opencode|claude|codex")
	c.Flags().BoolVar(&dry, "dry-run", false, "print plan without downloading or writing")
	_ = c.RegisterFlagCompletionFunc("client", func(cmd *cobra.Command, args []string, _ string) ([]string, cobra.ShellCompDirective) {
		return []string{"pi", "opencode", "claude", "codex"}, cobra.ShellCompDirectiveNoFileComp
	})
	return c
}
