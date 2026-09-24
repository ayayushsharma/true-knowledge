package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/ayayushsharma/true-knowledge/internal/backends"
	"github.com/ayayushsharma/true-knowledge/internal/config"
	"github.com/ayayushsharma/true-knowledge/internal/installer"
	"github.com/spf13/cobra"
)

var backendVersionRe = regexp.MustCompile(`^(\d+\.\d+\.\d+|[0-9a-f]{7,40})$`)

func backendVersionOK(v string) bool { return backendVersionRe.MatchString(v) }

func cmdConfig(g *Globals) *cobra.Command {
	c := &cobra.Command{Use: "config", Short: "Inspect/change tk config (tk owns, CBM follows via env)"}
	c.AddCommand(
		&cobra.Command{
			Use:   "list",
			Short: "Show all settings",
			RunE: func(cmd *cobra.Command, args []string) error {
				ctx, err := load(*g)
				if err != nil {
					return err
				}
				raw, _ := json.MarshalIndent(ctx.Cfg, "", "  ")
				return ctx.out(cmd, string(raw), map[string]any{"config": ctx.Cfg})
			},
		},
		&cobra.Command{
			Use:   "get <key>",
			Short: "Get one setting",
			Args:  cobra.ExactArgs(1),
			ValidArgsFunction: func(cmd *cobra.Command, args []string, _ string) ([]string, cobra.ShellCompDirective) {
				return config.KnownKeys(), cobra.ShellCompDirectiveNoFileComp
			},
			RunE: func(cmd *cobra.Command, args []string) error {
				ctx, err := load(*g)
				if err != nil {
					return err
				}
				v, err := getKey(ctx.Cfg, args[0])
				if err != nil {
					return fail("%v", err)
				}
				return ctx.out(cmd, v, map[string]any{"key": args[0], "value": v})
			},
		},
	)
	setCmd := &cobra.Command{
		Use:   "set <key> <value>",
		Short: "Set one setting",
		Args:  cobra.ExactArgs(2),
		ValidArgsFunction: func(cmd *cobra.Command, args []string, _ string) ([]string, cobra.ShellCompDirective) {
			if len(args) == 0 {
				return config.KnownKeys(), cobra.ShellCompDirectiveNoFileComp
			}
			return nil, cobra.ShellCompDirectiveNoFileComp
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, err := load(*g)
			if err != nil {
				return err
			}
			cfg := ctx.Cfg
			if err := setKey(&cfg, args[0], args[1]); err != nil {
				return fail("%v", err)
			}
			if err := config.Save(ctx.Paths.ConfigFile(), cfg); err != nil {
				return fail("save config: %v", err)
			}
			note := ""
			if args[0] == "watcher_enabled" {
				note = " (read once at daemon start — run `cbm daemon stop` so the next session picks it up)"
			}
			return ctx.out(cmd, fmt.Sprintf("set %s=%s%s", args[0], args[1], note), map[string]any{"key": args[0]})
		},
	}
	resetCmd := &cobra.Command{
		Use:   "reset <key>",
		Short: "Reset one setting to default",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, err := load(*g)
			if err != nil {
				return err
			}
			def := config.Defaults()
			dv, err := getKey(def, args[0])
			if err != nil {
				return fail("%v", err)
			}
			cfg := ctx.Cfg
			if err := setKey(&cfg, args[0], dv); err != nil {
				return fail("%v", err)
			}
			if err := config.Save(ctx.Paths.ConfigFile(), cfg); err != nil {
				return fail("save config: %v", err)
			}
			return ctx.out(cmd, fmt.Sprintf("reset %s=%s", args[0], dv), nil)
		},
	}
	validateCmd := &cobra.Command{
		Use:   "validate",
		Short: "Validate config schema",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, err := load(*g)
			if err != nil {
				return err
			}
			if err := ctx.Cfg.Validate(); err != nil {
				return fail("invalid: %v", err)
			}
			return ctx.out(cmd, "config valid", nil)
		},
	}
	c.AddCommand(setCmd, resetCmd, validateCmd)
	return c
}

func getKey(c config.Config, key string) (string, error) {
	switch key {
	case "index_mode":
		return c.IndexMode, nil
	case "auto_index":
		return strconv.FormatBool(c.AutoIndex), nil
	case "auto_watch":
		return strconv.FormatBool(c.AutoWatch), nil
	case "watcher_enabled":
		return strconv.FormatBool(c.WatcherEnabled), nil
	case "allowed_root":
		return c.AllowedRoot, nil
	case "cbm_binary":
		return c.CBMBinary, nil
	case "cbm_version_pin":
		if c.CBMVersionPin == "" {
			return config.DefaultCBMPin, nil
		}
		return c.CBMVersionPin, nil
	case "budgets.default_chars":
		return strconv.Itoa(c.Budgets.DefaultChars), nil
	case "budgets.architecture_chars":
		return strconv.Itoa(c.Budgets.ArchitectureChars), nil
	case "budgets.notes_toc_chars":
		return strconv.Itoa(c.Budgets.NotesTocChars), nil
	case "budgets.ledger_chars":
		return strconv.Itoa(c.Budgets.LedgerChars), nil
	case "embedding.enabled":
		return strconv.FormatBool(c.Embedding.Enabled), nil
	case "embedding.endpoint":
		return c.Embedding.Endpoint, nil
	case "embedding.model":
		return c.Embedding.Model, nil
	case "embedding.timeout_ms":
		return strconv.Itoa(c.Embedding.TimeoutMS), nil
	case "ledger.enabled":
		return strconv.FormatBool(c.Ledger.Enabled), nil
	case "mcp.profile":
		if c.MCP.Profile == "" {
			return "scout (unset)", nil
		}
		return c.MCP.Profile, nil
	case "ui.picker":
		return strconv.FormatBool(c.UI.Picker), nil
	}
	return "", fmt.Errorf("unknown key %q (see `tk config list` / known keys)", key)
}

func setKey(c *config.Config, key, val string) error {
	switch key {
	case "index_mode":
		if val != "fast" && val != "moderate" && val != "full" {
			return fmt.Errorf("invalid index_mode %q", val)
		}
		c.IndexMode = val
	case "auto_index", "auto_watch", "watcher_enabled":
		b, err := strconv.ParseBool(val)
		if err != nil {
			return fmt.Errorf("want true|false: %w", err)
		}
		switch key {
		case "auto_index":
			c.AutoIndex = b
		case "auto_watch":
			c.AutoWatch = b
		case "watcher_enabled":
			c.WatcherEnabled = b
		}
	case "allowed_root":
		c.AllowedRoot = val
	case "cbm_binary":
		c.CBMBinary = val
	case "cbm_version_pin":
		if !backendVersionOK(val) {
			return fmt.Errorf("want X.Y.Z version, got %q", val)
		}
		c.CBMVersionPin = val
	case "budgets.default_chars", "budgets.architecture_chars", "budgets.notes_toc_chars", "budgets.ledger_chars":
		n, err := strconv.Atoi(val)
		if err != nil || n <= 0 {
			return fmt.Errorf("budget must be positive int")
		}
		switch key {
		case "budgets.default_chars":
			c.Budgets.DefaultChars = n
		case "budgets.architecture_chars":
			c.Budgets.ArchitectureChars = n
		case "budgets.notes_toc_chars":
			c.Budgets.NotesTocChars = n
		case "budgets.ledger_chars":
			c.Budgets.LedgerChars = n
		}
	case "embedding.enabled":
		b, err := strconv.ParseBool(val)
		if err != nil {
			return fmt.Errorf("want true|false: %w", err)
		}
		c.Embedding.Enabled = b
	case "embedding.endpoint":
		c.Embedding.Endpoint = strings.TrimSpace(val)
	case "embedding.model":
		c.Embedding.Model = strings.TrimSpace(val)
	case "embedding.timeout_ms":
		n, err := strconv.Atoi(val)
		if err != nil || n <= 0 {
			return fmt.Errorf("timeout_ms must be positive int")
		}
		c.Embedding.TimeoutMS = n
	case "ledger.enabled":
		b, err := strconv.ParseBool(val)
		if err != nil {
			return fmt.Errorf("want true|false: %w", err)
		}
		c.Ledger.Enabled = b
	case "mcp.profile":
		if val != "" && !slices.Contains(config.ValidProfiles(), val) {
			return fmt.Errorf("want %s or empty to unset, got %q", strings.Join(config.ValidProfiles(), "|"), val)
		}
		c.MCP.Profile = val
	case "ui.picker":
		b, err := strconv.ParseBool(val)
		if err != nil {
			return fmt.Errorf("want true|false: %w", err)
		}
		c.UI.Picker = b
	default:
		return fmt.Errorf("unknown key %q", key)
	}
	return c.Validate()
}

func cmdMigrate(g *Globals) *cobra.Command {
	var from, dryRun string
	var isDry bool
	c := &cobra.Command{
		Use:   "migrate",
		Short: "Move legacy ~/.tk / $TK_HOME / Library tree into XDG true-knowledge dirs",
		Example: `  tk migrate --dry-run
  tk migrate --from ~/.tk`,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, err := load(*g)
			if err != nil {
				return err
			}
			_ = dryRun
			cands := []string{from}
			if from == "" {
				home, _ := os.UserHomeDir()
				cands = []string{
					filepath.Join(home, ".tk"),
					os.Getenv("TK_HOME"),
					filepath.Join(home, "Library", "Application Support", "true-knowledge"),
				}
			}
			found := []string{}
			live := map[string]bool{
				ctx.Paths.Config: true, ctx.Paths.Data: true, ctx.Paths.Cache: true, ctx.Paths.State: true,
			}
			if tkHome := os.Getenv("TK_HOME"); tkHome != "" {
				if abs, err := filepath.Abs(tkHome); err == nil {
					live[abs] = true
				} else {
					live[tkHome] = true
				}
			}
			for _, d := range cands {
				if d == "" {
					continue
				}
				abs := d
				if a, err := filepath.Abs(d); err == nil {
					abs = a
				}
				if live[d] || live[abs] {
					continue // never migrate from the live home itself
				}
				if fi, err := os.Stat(d); err == nil && fi.IsDir() {
					found = append(found, d)
				}
			}
			if len(found) == 0 {
				return ctx.out(cmd, "nothing to migrate (no legacy dirs found)", map[string]any{"migrated": false})
			}
			if isDry {
				return ctx.out(cmd, "would migrate: "+strings.Join(found, ", "), map[string]any{"from": found, "dry_run": true})
			}
			// Marker-only MVP: copy config.json + tk.json if present, stamp MIGRATED.
			moved := []string{}
			for _, d := range found {
				for _, f := range []string{"config.json", "tk.json"} {
					src := filepath.Join(d, f)
					if _, err := os.Stat(src); err != nil {
						continue
					}
					raw, err := os.ReadFile(src)
					if err != nil {
						continue
					}
					dst := ctx.Paths.ConfigFile()
					if f == "tk.json" {
						dst = ctx.Paths.RegistryFile()
					}
					if _, err := os.Stat(dst); os.IsNotExist(err) {
						_ = os.WriteFile(dst, raw, 0o600)
						moved = append(moved, f+" from "+d)
					}
				}
			}
			_ = os.WriteFile(filepath.Join(ctx.Paths.Data, "MIGRATED"), []byte("migrated\n"), 0o600)
			return ctx.out(cmd, fmt.Sprintf("migrated %d file(s); marker written (re-run needs --force, not yet required)", len(moved)),
				map[string]any{"moved": moved})
		},
	}
	c.Flags().StringVar(&from, "from", "", "legacy dir (default: auto-detect ~/.tk, $TK_HOME, Library)")
	c.Flags().BoolVar(&isDry, "dry-run", false, "print without writing")
	return c
}

func cmdInstall(g *Globals) *cobra.Command {
	var check, update, dry bool
	var version string
	c := &cobra.Command{
		Use:   "install [backend...]",
		Short: "Install/update managed indexing backends (default: all)",
		Long: `Installs missing backends and updates stale ones to their pins.
Backends live in <cache>/bin (== CBM_CACHE_DIR/bin); checksums verified before any write.
Adding a backend is one entry in internal/backends — see docs/DECISIONS.`,
		Example: `  tk install                  # install all missing backends
  tk install cbm --update     # (re)install cbm at its pin
  tk install --check          # report only, no network
  tk install cbm --version 0.12.0 --dry-run`,
		ValidArgsFunction: func(cmd *cobra.Command, args []string, _ string) ([]string, cobra.ShellCompDirective) {
			out := []string{}
			for _, b := range backends.All() {
				out = append(out, b.Name)
			}
			return out, cobra.ShellCompDirectiveNoFileComp
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, err := load(*g)
			if err != nil {
				return err
			}
			names := args
			if len(names) == 0 {
				for _, b := range backends.All() {
					names = append(names, b.Name)
				}
			}
			type row struct {
				Backend string `json:"backend"`
				Status  string `json:"status"`
				Detail  string `json:"detail,omitempty"`
			}
			rows := []row{}
			lines := []string{}
			changedPin := false
			for _, name := range names {
				b, err := backends.ByName(name)
				if err != nil {
					return fail("%v", err)
				}
				pin := ctx.Cfg.CBMVersionPin
				if pin == "" {
					pin = config.DefaultCBMPin
				}
				if version != "" && len(names) == 1 {
					pin = version
				} else if version != "" {
					return fail("--version only works with a single backend")
				}
				st := installer.Inspect(cmd.Context(), ctx.Paths.Cache, b, pin, backends.HostGOOS())
				switch {
				case check || dry:
					plan, perr := installer.ResolvePlan(ctx.Paths.Cache, b, pin, backends.HostGOOS(), backends.HostGOARCH())
					detail := plan.ArchiveURL
					if perr != nil {
						detail = perr.Error()
					}
					state := "up-to-date"
					if st.NeedsInstall {
						state = "would-install"
					}
					if dry {
						state = "dry-run"
					}
					rows = append(rows, row{name, state, detail})
					lines = append(lines, fmt.Sprintf("%-8s %-12s pin %s  %s", name, state, pin, detail))
				case st.UpToDate && !update:
					rows = append(rows, row{name, "up-to-date", st.Path})
					lines = append(lines, fmt.Sprintf("%-8s up-to-date %s (%s)", name, st.InstalledVersion, displayPath(st)))
				default:
					plan, err := installer.Install(cmd.Context(), ctx.Paths.Cache, b, pin, backends.HostGOOS(), backends.HostGOARCH())
					if err != nil {
						rows = append(rows, row{name, "failed", err.Error()})
						lines = append(lines, fmt.Sprintf("%-8s FAILED: %v", name, err))
						continue
					}
					rows = append(rows, row{name, "installed", plan.Dest})
					lines = append(lines, fmt.Sprintf("%-8s installed %s -> %s", name, pin, plan.Dest))
				}
				if version != "" && pin == version {
					ctx.Cfg.CBMVersionPin = version
					changedPin = true
				}
			}
			if changedPin {
				if err := config.Save(ctx.Paths.ConfigFile(), ctx.Cfg); err != nil {
					return fail("save pin: %v", err)
				}
				lines = append(lines, fmt.Sprintf("pinned %s=%s in config", names[0], version))
			}
			text := strings.Join(lines, "\n")
			if text == "" {
				text = "nothing to do"
			}
			return ctx.out(cmd, text, map[string]any{"backends": rows})
		},
	}
	c.Flags().BoolVar(&check, "check", false, "report status only, no network")
	c.Flags().BoolVar(&update, "update", false, "reinstall even when up-to-date (downgrade-safe)")
	c.Flags().BoolVar(&dry, "dry-run", false, "print plan URLs without downloading")
	c.Flags().StringVar(&version, "version", "", "override pin X.Y.Z for this run (persists to config)")
	return c
}

func displayPath(st installer.Status) string {
	if st.Path != "" {
		return st.Path
	}
	return "(missing)"
}

func cmdMCPInstall(g *Globals) *cobra.Command {
	var client, profile string
	var dry bool
	c := &cobra.Command{
		Use:   "mcp-install",
		Short: "Point an agent client at `tk mcp` (Pi slipped this build)",
		Example: `  tk mcp-install --client opencode --dry-run
  tk mcp-install --client claude --tool-profile memory`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if profile != "" && !slices.Contains(config.ValidProfiles(), profile) {
				return fmt.Errorf("unknown --tool-profile %q (want %s)", profile, strings.Join(config.ValidProfiles(), "|"))
			}
			ctx, err := load(*g)
			if err != nil {
				return err
			}
			exe, err := os.Executable()
			if err != nil {
				exe = "tk"
			}
			text, fields, err := mcpSnippet(exe, client, profile, dry)
			if err != nil {
				return err
			}
			return ctx.out(cmd, text, fields)
		},
	}
	c.Flags().StringVar(&client, "client", "", "pi|opencode|claude|codex (required)")
	_ = c.MarkFlagRequired("client")
	c.Flags().StringVar(&profile, "tool-profile", "", "tool surface (scout|analysis|minimal|memory); emitted as TK_MCP_PROFILE env, default scout")
	c.Flags().BoolVar(&dry, "dry-run", false, "print without writing")
	_ = c.RegisterFlagCompletionFunc("client", func(cmd *cobra.Command, args []string, _ string) ([]string, cobra.ShellCompDirective) {
		return []string{"pi", "opencode", "claude", "codex"}, cobra.ShellCompDirectiveNoFileComp
	})
	_ = c.RegisterFlagCompletionFunc("tool-profile", func(cmd *cobra.Command, args []string, _ string) ([]string, cobra.ShellCompDirective) {
		return config.ValidProfiles(), cobra.ShellCompDirectiveNoFileComp
	})
	return c
}
