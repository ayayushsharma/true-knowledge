package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	"github.com/true-knowledge/tk/internal/cbmexec"
	"github.com/true-knowledge/tk/internal/config"
	"github.com/true-knowledge/tk/internal/gitx"
	"github.com/true-knowledge/tk/internal/store"
)

func cmdInit(g *Globals) *cobra.Command {
	return &cobra.Command{
		Use:   "init",
		Short: "Create true-knowledge dirs + default config",
		Example: `  tk init
  TK_HOME=/tmp/x tk init`,
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := load(*g)
			if err != nil {
				return err
			}
			if _, err := os.Stat(c.Paths.ConfigFile()); os.IsNotExist(err) {
				if err := config.Save(c.Paths.ConfigFile(), c.Cfg); err != nil {
					return fail("write config: %v", err)
				}
			}
			if err := c.saveReg(); err != nil {
				return fail("write registry: %v", err)
			}
			return c.out(cmd, fmt.Sprintf("initialized\n  config %s\n  data   %s\n  cache  %s\n  state  %s",
				c.Paths.Config, c.Paths.Data, c.Paths.Cache, c.Paths.State),
				map[string]any{"config": c.Paths.Config, "data": c.Paths.Data, "cache": c.Paths.Cache, "state": c.Paths.State})
		},
	}
}

func cmdRegister(g *Globals) *cobra.Command {
	var name string
	c := &cobra.Command{
		Use:   "register <path>",
		Short: "Register a repo path (no auto-index)",
		Args:  cobra.ExactArgs(1),
		Example: `  tk register ./myrepo --name demo
  tk register /abs/path`,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, err := load(*g)
			if err != nil {
				return err
			}
			abs, err := filepath.Abs(args[0])
			if err != nil {
				return fail("bad path: %v", err)
			}
			if rp, err := filepath.EvalSymlinks(abs); err == nil {
				abs = rp
			}
			if fi, err := os.Stat(abs); err != nil || !fi.IsDir() {
				return fail("not a directory: %s", abs)
			}
			if name == "" {
				name = filepath.Base(abs)
			}
			norm, err := store.NormalizeName(name)
			if err != nil {
				return fail("%v", err)
			}
			if _, exists := ctx.Reg[norm]; exists {
				return fail("project %q already registered (collision-safe); pick --name", norm)
			}
			ctx.Reg[norm] = store.Project{Path: abs, Mode: ctx.Cfg.IndexMode}
			if err := ctx.saveReg(); err != nil {
				return fail("save registry: %v", err)
			}
			return ctx.out(cmd, fmt.Sprintf("registered %q -> %s (run `tk index %s`)", norm, abs, norm),
				map[string]any{"project": norm, "path": abs})
		},
	}
	c.Flags().StringVar(&name, "name", "", "project name (default: directory base)")
	return c
}

func cmdIndex(g *Globals) *cobra.Command {
	var modeFlag string
	var forceFull, forceFast bool
	c := &cobra.Command{
		Use:   "index [project]",
		Short: "Index a project via CBM (default: moderate)",
		Example: `  tk index demo
  tk index demo --full
  tk index  # all registered`,
		ValidArgsFunction: func(cmd *cobra.Command, args []string, _ string) ([]string, cobra.ShellCompDirective) {
			if len(args) == 0 {
				if ctx, err := load(*g); err == nil {
					return ctx.projectNames(), cobra.ShellCompDirectiveNoFileComp
				}
			}
			return nil, cobra.ShellCompDirectiveNoFileComp
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, err := load(*g)
			if err != nil {
				return err
			}
			mode := ctx.Cfg.IndexMode
			if modeFlag != "" {
				mode = modeFlag
			}
			if forceFull {
				mode = "full"
			}
			if forceFast {
				mode = "fast"
			}
			switch mode {
			case "fast", "moderate", "full":
			default:
				return fail("invalid mode %q", mode)
			}
			if _, _, err := ctx.needCBM(cmd.Context()); err != nil {
				return err
			}
			names := args
			if len(names) == 0 {
				names = ctx.Reg.Names()
			}
			if len(names) == 0 {
				return fail("no projects registered; run `tk register <path>`")
			}
			for _, n := range names {
				p, ok := ctx.Reg[n]
				if !ok {
					return fail("unknown project %q; see `tk status`", n)
				}
				out, err := ctx.cbmCall(cmd.Context(), "index_repository", map[string]any{"repo_path": p.Path, "mode": mode, "name": n})
				if err != nil {
					return fail("index %q: %v", n, err)
				}
				// Zoekt pass: in-process trigram index, auto-refresh + registry.
				if _, zerr := ctx.ensureZoektAndTouch(cmd.Context(), n, p.Path, mode); zerr != nil {
					return fail("index %q: %v", n, zerr)
				}
				_ = out
			}
			if err := ctx.saveReg(); err != nil {
				return fail("save registry: %v", err)
			}
			msg := fmt.Sprintf("indexed %d project(s) [%s]", len(names), mode)
			return ctx.out(cmd, msg, map[string]any{"projects": names, "mode": mode})
		},
	}
	c.Flags().StringVar(&modeFlag, "mode", "", "fast|moderate|full")
	c.Flags().BoolVar(&forceFull, "full", false, "shorthand for --mode full")
	c.Flags().BoolVar(&forceFast, "fast", false, "shorthand for --mode fast")
	return c
}

func cmdSync(g *Globals) *cobra.Command {
	c := &cobra.Command{
		Use:   "sync [project]",
		Short: "Fast no-op on clean Git HEAD, else delegate to watcher/index",
		Example: `  tk sync demo
  tk sync  # all`,
		ValidArgsFunction: func(cmd *cobra.Command, args []string, _ string) ([]string, cobra.ShellCompDirective) {
			if len(args) == 0 {
				if ctx, err := load(*g); err == nil {
					return ctx.projectNames(), cobra.ShellCompDirectiveNoFileComp
				}
			}
			return nil, cobra.ShellCompDirectiveNoFileComp
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, err := load(*g)
			if err != nil {
				return err
			}
			names := args
			if len(names) == 0 {
				names = ctx.Reg.Names()
			}
			if len(names) == 0 {
				return fail("no projects registered")
			}
			clean, dirty := 0, []string{}
			for _, n := range names {
				p, ok := ctx.Reg[n]
				if !ok {
					return fail("unknown project %q", n)
				}
				if head := gitx.Head(p.Path); head != "" {
					if head == p.Head {
						clean++
						continue
					}
				} else if fp, ferr := store.Fingerprint(p.Path); ferr == nil && fp == p.Fingerprint && p.Fingerprint != "" {
					// Non-git tree unchanged since index.
					clean++
					continue
				}
				dirty = append(dirty, n)
			}
			if len(dirty) == 0 {
				return ctx.out(cmd, fmt.Sprintf("clean: %d project(s), no-op (watcher owns freshness)", clean),
					map[string]any{"clean": clean, "indexed": []string{}})
			}
			// Dirty: prefer watcher; do one explicit moderate index per dirty project.
			if _, _, err := ctx.needCBM(cmd.Context()); err != nil {
				return ctx.out(cmd, fmt.Sprintf("%d clean, %d dirty but CBM unavailable — watcher will catch up", clean, len(dirty)),
					map[string]any{"clean": clean, "dirty": dirty})
			}
			for _, n := range dirty {
				p := ctx.Reg[n]
				if _, err := ctx.cbmCall(cmd.Context(), "index_repository", map[string]any{"repo_path": p.Path, "mode": ctx.Cfg.IndexMode, "name": n}); err != nil {
					return fail("sync %q: %v", n, err)
				}
				if _, zerr := ctx.ensureZoektAndTouch(cmd.Context(), n, p.Path, ctx.Cfg.IndexMode); zerr != nil {
					return fail("sync %q: %v", n, zerr)
				}
			}
			_ = ctx.saveReg()
			return ctx.out(cmd, fmt.Sprintf("synced %d dirty, %d clean", len(dirty), clean),
				map[string]any{"clean": clean, "indexed": dirty})
		},
	}
	return c
}

func cmdStatus(g *Globals) *cobra.Command {
	c := &cobra.Command{
		Use:   "status",
		Short: "List projects, HEAD, freshness",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, err := load(*g)
			if err != nil {
				return err
			}
			names := ctx.Reg.Names()
			if len(names) == 0 {
				return ctx.out(cmd, "no projects registered", map[string]any{"projects": []string{}})
			}
			var b strings.Builder
			rows := []map[string]any{}
			for _, n := range names {
				p := ctx.Reg[n]
				head := gitx.Head(p.Path)
				state := "clean"
				zstate := "z:-"
				if head == "" {
					// Non-git: fingerprint decides; zoekt tracks "files".
					if fp, ferr := store.Fingerprint(p.Path); ferr != nil || p.Fingerprint == "" || fp != p.Fingerprint {
						state = "dirty"
					}
					if p.ZoektHead == "files" && state == "clean" {
						zstate = "z:ok"
					} else if p.ZoektHead != "" {
						zstate = "z:stale"
					}
				} else {
					if head != p.Head {
						state = "dirty"
					}
					switch {
					case p.ZoektHead == head:
						zstate = "z:ok"
					case p.ZoektHead != "":
						zstate = "z:stale"
					}
				}
				fmt.Fprintf(&b, "%-20s %-6s %-7s %s  %s\n", n, state, zstate, shortHead(head), p.Path)
				rows = append(rows, map[string]any{"project": n, "state": state, "head": head, "path": p.Path, "mode": p.Mode, "zoekt_head": p.ZoektHead, "fingerprint": p.Fingerprint})
			}
			_ = cbmexec.Truncate
			return ctx.out(cmd, strings.TrimRight(b.String(), "\n"), map[string]any{"projects": rows})
		},
	}
	return c
}

func shortHead(h string) string {
	if len(h) > 8 {
		return h[:8]
	}
	if h == "" {
		return "-"
	}
	return h
}
