package cli

import (
	"path/filepath"
	"regexp"
	"strings"

	"github.com/spf13/cobra"
	"github.com/true-knowledge/tk/internal/cbmexec"
)

var metaChars = regexp.MustCompile(`[.*()\[\]{}+?^$|\\]`)

func resolveProject(c *Ctx, flag string, args []string) string {
	if flag != "" {
		return flag
	}
	if len(c.Reg) == 1 {
		return c.Reg.Names()[0]
	}
	if len(args) > 0 {
		if _, ok := c.Reg[args[len(args)-1]]; ok {
			return args[len(args)-1]
		}
	}
	return ""
}

// requireProject resolves or fails with a routing hint.
// CBM requires project on nearly every tool; tk never sends "".
func requireProject(c *Ctx, flag string, args []string) (string, error) {
	if p := resolveProject(c, flag, args); p != "" {
		return p, nil
	}
	return "", fail("pass --project (registered: %s); see `tk status`", listNames(c))
}

func listNames(c *Ctx) string {
	names := c.Reg.Names()
	if len(names) == 0 {
		return "none — run `tk register <path>`"
	}
	return strings.Join(names, ", ")
}

// keywords splits a natural-language phrase into CBM semantic keywords.
// semantic_query must be an array of keyword strings, not one string.
func keywords(q string) []string {
	fields := strings.Fields(q)
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		w := strings.Trim(f, "\"'.,;:!?()[]{}")
		if w != "" {
			out = append(out, w)
		}
	}
	return out
}

func cmdFind(g *Globals) *cobra.Command {
	var project, label string
	var limit int
	c := &cobra.Command{
		Use:   "find <query> [project]",
		Short: "Deterministic router: regex→grep, NL→semantic, ident→graph",
		Example: `  tk find ProcessOrder demo
  tk find "retry.*backoff" demo --limit 20
  tk find Handler demo --label Function`,
		Args: cobra.MinimumNArgs(1),
		ValidArgsFunction: func(cmd *cobra.Command, args []string, _ string) ([]string, cobra.ShellCompDirective) {
			if ctx, err := load(*g); err == nil {
				return ctx.projectNames(), cobra.ShellCompDirectiveNoFileComp
			}
			return nil, cobra.ShellCompDirectiveNoFileComp
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, err := load(*g)
			if err != nil {
				return err
			}
			q := args[0]
			proj, err := requireProject(ctx, project, args)
			if err != nil {
				return err
			}
			r, _, err := ctx.needCBM(cmd.Context())
			if err != nil {
				return err
			}
			var tool string
			var payload map[string]any
			switch {
			case metaChars.MatchString(q) || strings.Contains(q, "/"):
				if label != "" {
					return fail("--label needs a graph route (identifier or multi-word query), not a text pattern")
				}
				tool, payload = "search_code", map[string]any{"pattern": q, "project": proj, "limit": limit}
			case strings.Contains(strings.TrimSpace(q), " ") && len(strings.Fields(q)) > 2:
				tool, payload = "search_graph", map[string]any{"semantic_query": keywords(q), "project": proj, "limit": limit}
			default:
				tool, payload = "search_graph", map[string]any{"name_pattern": q, "project": proj, "limit": limit}
			}
			if label != "" {
				payload["label"] = label
			}
			out, err := r.Run(cmd.Context(), tool, payload)
			if err != nil {
				return fail("%v", err)
			}
			out = cbmexec.Truncate(out, ctx.budget(""))
			return ctx.outFresh(cmd, proj, out, map[string]any{"route": tool, "project": proj})
		},
	}
	c.Flags().StringVar(&project, "project", "", "project name")
	c.Flags().StringVar(&label, "label", "", "node-label filter (graph routes only)")
	c.Flags().IntVar(&limit, "limit", 20, "max results")
	return c
}

func cmdExplain(g *Globals) *cobra.Command {
	var project string
	c := &cobra.Command{
		Use:   "explain <symbol> [project]",
		Short: "Definition + snippet + callers/callees (one bounded call set)",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, err := load(*g)
			if err != nil {
				return err
			}
			sym := args[0]
			proj, err := requireProject(ctx, project, args)
			if err != nil {
				return err
			}
			r, _, err := ctx.needCBM(cmd.Context())
			if err != nil {
				return err
			}
			snip, err := r.Run(cmd.Context(), "get_code_snippet", map[string]any{"qualified_name": sym, "project": proj})
			if err != nil {
				return fail("%v", err)
			}
			trace, err := r.Run(cmd.Context(), "trace_path", map[string]any{"function_name": sym, "project": proj, "direction": "both", "depth": 1})
			if err != nil {
				trace = "(trace unavailable: " + err.Error() + ")"
			}
			combined := "== definition/snippet ==\n" + snip + "\n== callers+callees (depth 1) ==\n" + trace
			return ctx.outFresh(cmd, proj, cbmexec.Truncate(combined, ctx.budget("")), map[string]any{"symbol": sym, "project": proj})
		},
	}
	c.Flags().StringVar(&project, "project", "", "project name")
	return c
}

func cmdGrep(g *Globals) *cobra.Command {
	var project, files string
	var limit int
	var isRegex bool
	c := &cobra.Command{
		Use:   "grep <pattern> [project]",
		Short: "Project-scoped source-text search",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, err := load(*g)
			if err != nil {
				return err
			}
			pat := args[0]
			if isRegex {
				if _, err := regexp.Compile(pat); err != nil {
					return fail("invalid regex %q: %v (not empty results)", pat, err)
				}
			}
			proj, err := requireProject(ctx, project, args)
			if err != nil {
				return err
			}
			r, _, err := ctx.needCBM(cmd.Context())
			if err != nil {
				return err
			}
			out, err := r.Run(cmd.Context(), "search_code", map[string]any{"pattern": pat, "project": proj, "file_pattern": files, "limit": limit, "regex": isRegex})
			if err != nil {
				return fail("%v", err)
			}
			return ctx.outFresh(cmd, proj, cbmexec.Truncate(out, ctx.budget("")), map[string]any{"project": proj})
		},
	}
	c.Flags().StringVar(&project, "project", "", "project name")
	c.Flags().StringVar(&files, "files", "", "file glob filter")
	c.Flags().IntVar(&limit, "limit", 20, "max results")
	c.Flags().BoolVar(&isRegex, "regex", false, "treat pattern as regex (validation error on bad regex)")
	return c
}

func cmdOutline(g *Globals) *cobra.Command {
	var project, labels string
	var limit int
	c := &cobra.Command{
		Use:   "outline <file> [project]",
		Short: "Declarations in one file, in source order (cheap read alternative)",
		Example: `  tk outline orders.go demo
  tk outline src/main.go --label Function`,
		Args: cobra.MinimumNArgs(1),
		ValidArgsFunction: func(cmd *cobra.Command, args []string, _ string) ([]string, cobra.ShellCompDirective) {
			if len(args) == 0 {
				return nil, cobra.ShellCompDirectiveFilterFileExt
			}
			if ctx, err := load(*g); err == nil {
				return ctx.projectNames(), cobra.ShellCompDirectiveNoFileComp
			}
			return nil, cobra.ShellCompDirectiveNoFileComp
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, err := load(*g)
			if err != nil {
				return err
			}
			proj, err := requireProject(ctx, project, args)
			if err != nil {
				return err
			}
			file := repoRelative(ctx, proj, args[0])
			r, _, err := ctx.needCBM(cmd.Context())
			if err != nil {
				return err
			}
			payload := map[string]any{"project": proj, "file_path": file, "limit": limit}
			if labels != "" {
				payload["labels"] = strings.Split(labels, ",")
			}
			out, err := r.Run(cmd.Context(), "get_file_outline", payload)
			if err != nil {
				return fail("%v", err)
			}
			return ctx.outFresh(cmd, proj, cbmexec.Truncate(out, ctx.budget("")), map[string]any{"project": proj, "file": file})
		},
	}
	c.Flags().StringVar(&project, "project", "", "project name")
	c.Flags().StringVar(&labels, "label", "", "comma-separated node-label filter")
	c.Flags().IntVar(&limit, "limit", 100, "max declarations")
	return c
}

// repoRelative makes absolute paths repo-relative when they sit under the
// project root; CBM requires repository-relative file paths.
func repoRelative(ctx *Ctx, proj, file string) string {
	if !filepath.IsAbs(file) {
		return file
	}
	if p, ok := ctx.Reg[proj]; ok {
		if rel, err := filepath.Rel(p.Path, file); err == nil && !strings.HasPrefix(rel, "..") {
			return rel
		}
	}
	return file
}

func cmdImpact(g *Globals) *cobra.Command {
	var project, direction string
	var depth, limit int
	c := &cobra.Command{
		Use:   "impact [project]",
		Short: "Map working-tree diff to affected symbols + blast radius",
		Example: `  tk impact demo
  tk impact --direction outbound --depth 3`,
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
			proj, err := requireProject(ctx, project, args)
			if err != nil {
				return err
			}
			switch direction {
			case "inbound", "outbound", "both":
			default:
				return fail("invalid --direction %q (want inbound|outbound|both)", direction)
			}
			r, _, err := ctx.needCBM(cmd.Context())
			if err != nil {
				return err
			}
			out, err := r.Run(cmd.Context(), "detect_changes", map[string]any{
				"project": proj, "direction": direction, "depth": depth, "limit": limit,
			})
			if err != nil {
				return fail("%v", err)
			}
			return ctx.outFresh(cmd, proj, cbmexec.Truncate(out, ctx.budget("")), map[string]any{"project": proj})
		},
	}
	c.Flags().StringVar(&project, "project", "", "project name")
	c.Flags().StringVar(&direction, "direction", "inbound", "inbound|outbound|both")
	c.Flags().IntVar(&depth, "depth", 2, "traversal depth")
	c.Flags().IntVar(&limit, "limit", 50, "max rows")
	return c
}

func cmdArch(g *Globals) *cobra.Command {
	var project string
	c := &cobra.Command{
		Use:   "arch [project]",
		Short: "Architecture brief (languages, packages, entry points, hotspots)",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, err := load(*g)
			if err != nil {
				return err
			}
			proj, err := requireProject(ctx, project, args)
			if err != nil {
				return err
			}
			r, _, err := ctx.needCBM(cmd.Context())
			if err != nil {
				return err
			}
			out, err := r.Run(cmd.Context(), "get_architecture", map[string]any{"project": proj})
			if err != nil {
				return fail("%v", err)
			}
			return ctx.outFresh(cmd, proj, cbmexec.Truncate(out, ctx.budget("arch")), map[string]any{"project": proj})
		},
	}
	c.Flags().StringVar(&project, "project", "", "project name")
	return c
}

func cmdQuery(g *Globals) *cobra.Command {
	var project string
	var limit int
	c := &cobra.Command{
		Use:     "query <cypher> [project]",
		Short:   "Raw read-only graph query (analysis profile)",
		Example: `  tk query "MATCH (f:Function) RETURN f.name LIMIT 5" demo`,
		Args:    cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, err := load(*g)
			if err != nil {
				return err
			}
			proj, err := requireProject(ctx, project, args)
			if err != nil {
				return err
			}
			r, _, err := ctx.needCBM(cmd.Context())
			if err != nil {
				return err
			}
			out, err := r.Run(cmd.Context(), "query_graph", map[string]any{"query": args[0], "project": proj, "max_rows": limit})
			if err != nil {
				return fail("%v", err)
			}
			return ctx.outFresh(cmd, proj, cbmexec.Truncate(out, ctx.budget("")), map[string]any{"project": proj})
		},
	}
	c.Flags().StringVar(&project, "project", "", "project name")
	c.Flags().IntVar(&limit, "limit", 20, "row limit guardrail")
	return c
}

func cmdCBM(g *Globals) *cobra.Command {
	c := &cobra.Command{
		Use:                "cbm <engine-tool> [json-or-flags...]",
		Short:              "Low-level CBM passthrough (admin/debug)",
		DisableFlagParsing: true,
		Example:            `  tk cbm list_projects`,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, err := load(*g)
			if err != nil {
				return err
			}
			r, _, err := ctx.needCBM(cmd.Context())
			if err != nil {
				return err
			}
			out, err := r.RunRaw(cmd.Context(), args...)
			if err != nil {
				return fail("%v", err)
			}
			return ctx.out(cmd, cbmexec.Truncate(out, ctx.budget("")), nil)
		},
	}
	return c
}
