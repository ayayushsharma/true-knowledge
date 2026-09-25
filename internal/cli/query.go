package cli

import (
	"context"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/ayayushsharma/true-knowledge/internal/cbmexec"
	"github.com/spf13/cobra"
)

var metaChars = regexp.MustCompile(`[.*()\[\]{}+?^$|\\]`)

// resolveProject returns the effective project: an explicit --project always
// wins (--select is ignored alongside it); else a single registered project
// auto-defaults, except under --select which forces the picker; else "".
func resolveProject(c *Ctx, flag string, sel bool) string {
	if flag != "" {
		return flag
	}
	if sel {
		return "" // --select forces the picker; it never auto-defaults
	}
	if len(c.Reg) == 1 {
		return c.Reg.Names()[0]
	}
	return ""
}

// requireProject resolves or fails with a routing hint.
// CBM requires project on nearly every tool; tk never sends "".
// On interactive TTY (+ !--json + ui.picker) the failure becomes a project
// picker first — agents/scripts never see it (picker is gated off).
// --select forces that picker open even when a single project is registered;
// it is gated identically, so off a TTY / under --json / with ui.picker off it
// hard-fails with the routing hint instead of silently defaulting. --project
// takes precedence over --select when both are passed.
func requireProject(c *Ctx, flag string, sel bool) (string, error) {
	if p := resolveProject(c, flag, sel); p != "" {
		return p, nil
	}
	if sel {
		if len(c.Reg) == 0 {
			return "", fail("--select needs at least one registered project — run `tk register <path>`")
		}
		if !pickerEnabled(c) {
			return "", fail("--select needs an interactive TTY (+ !--json + ui.picker); pass --project (registered: %s); see `tk status`", listNames(c))
		}
		if p, err := pickProject(c); err == nil && p != "" {
			return p, nil
		}
		return "", fail("picker aborted; pass --project (registered: %s); see `tk status`", listNames(c))
	}
	if pickerEnabled(c) {
		if p, err := pickProject(c); err == nil && p != "" {
			return p, nil
		}
	}
	return "", fail("pass --project (registered: %s); see `tk status`", listNames(c))
}

// selectFlag registers -s/--select on a project-resolving command: force the
// interactive picker open even when a single project is registered. It is
// per-command rather than a root persistent flag so the flag only shows up
// (in --help, completions, and man pages) on the commands that honor it.
// Precedence: an explicit --project wins, --select is then ignored.
func selectFlag(c *cobra.Command, sel *bool) {
	c.Flags().BoolVarP(sel, "select", "s", false, "force the interactive project picker open (ignored when --project is set)")
}

func listNames(c *Ctx) string {
	names := c.Reg.Names()
	if len(names) == 0 {
		return "none — run `tk register <path>`"
	}
	return strings.Join(names, ", ")
}

// projectFlagCompletion completes --project from the registry (dynamic +
// instant; registry is the source, no live merge).
func projectFlagCompletion(g *Globals) func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
	return func(cmd *cobra.Command, args []string, _ string) ([]string, cobra.ShellCompDirective) {
		if ctx, err := load(*g); err == nil {
			return ctx.projectNames(), cobra.ShellCompDirectiveNoFileComp
		}
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
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
	var project, query, label string
	var limit int
	var sel bool
	c := &cobra.Command{
		Use:     "find --query <query> [--project <name>]",
		Short:   "Deterministic router: regex→grep, NL→semantic, ident→graph",
		Aliases: []string{"kg_find"},
		Example: `  tk find --query ProcessOrder --project demo
  tk find --query "retry.*backoff" --project demo --limit 20
  tk find --query Handler --project demo --label Function`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, err := load(*g)
			if err != nil {
				return err
			}
			q := query
			proj, err := requireProject(ctx, project, sel)
			if err != nil {
				return err
			}
			if _, _, err := ctx.needCBM(cmd.Context()); err != nil {
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
			out, err := ctx.cbmCallJSON(cmd.Context(), tool, payload)
			if err != nil {
				return fail("%v", err)
			}
			if cbmexec.LooksEmpty(out) {
				out, err = annotateAbsence(cmd.Context(), ctx, proj, out)
				if err != nil {
					return err
				}
			}
			out = cbmexec.Truncate(out, ctx.budget(""))
			return ctx.outFresh(cmd, proj, out, map[string]any{"route": tool, "project": proj})
		},
	}
	c.Flags().StringVar(&project, "project", "", "project name")
	c.Flags().StringVar(&query, "query", "", "identifier, natural-language phrase, or text pattern")
	c.Flags().StringVar(&label, "label", "", "node-label filter (graph routes only)")
	c.Flags().IntVar(&limit, "limit", 20, "max results")
	selectFlag(c, &sel)
	_ = c.MarkFlagRequired("query")
	_ = c.RegisterFlagCompletionFunc("project", projectFlagCompletion(g))
	return c
}

func cmdExplain(g *Globals) *cobra.Command {
	var project, sym string
	var sel bool
	c := &cobra.Command{
		Use:     "explain --symbol <symbol> [--project <name>]",
		Short:   "Definition + snippet + callers/callees (one bounded call set)",
		Aliases: []string{"kg_explain"},
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, err := load(*g)
			if err != nil {
				return err
			}
			proj, err := requireProject(ctx, project, sel)
			if err != nil {
				return err
			}
			if _, _, err := ctx.needCBM(cmd.Context()); err != nil {
				return err
			}
			snip, err := ctx.cbmCallJSON(cmd.Context(), "get_code_snippet", map[string]any{"qualified_name": sym, "project": proj})
			if err != nil {
				return fail("%v", err)
			}
			trace, err := tracePath(cmd.Context(), ctx, proj, sym, "both", 1)
			if err != nil {
				trace = "(trace unavailable: " + err.Error() + ")"
			}
			combined := "== definition/snippet ==\n" + snip + "\n== callers+callees (depth 1) ==\n" + trace
			return ctx.outFresh(cmd, proj, cbmexec.Truncate(combined, ctx.budget("")), map[string]any{"symbol": sym, "project": proj})
		},
	}
	c.Flags().StringVar(&project, "project", "", "project name")
	c.Flags().StringVar(&sym, "symbol", "", "qualified symbol name")
	selectFlag(c, &sel)
	_ = c.MarkFlagRequired("symbol")
	_ = c.RegisterFlagCompletionFunc("project", projectFlagCompletion(g))
	return c
}

// tracePath is the single trace_path spawn shared by `trace` and `explain`
// (and nothing else calls trace_path), so the two surfaces can never drift
// in payload shape. direction is inbound|outbound|both; depth is 1-5.
func tracePath(ctx context.Context, c *Ctx, proj, sym, direction string, depth int) (string, error) {
	return c.cbmCallJSON(ctx, "trace_path", map[string]any{
		"project": proj, "function_name": sym, "direction": direction, "depth": depth,
	})
}

// traceDirections and the traceDepth bounds mirror the engine's own
// vocabulary, so the flag help, the validators, and the MCP schema text
// cannot disagree.
var traceDirections = []string{"inbound", "outbound", "both"}

const (
	traceDepthMin = 1
	traceDepthMax = 5
)

// traceDirection validates --direction locally, before the CBM gate: a bad
// value is a hard error, never a silently empty traversal.
func traceDirection(direction string) error {
	for _, d := range traceDirections {
		if d == direction {
			return nil
		}
	}
	return fail("invalid --direction %q (want inbound|outbound|both)", direction)
}

// traceDepth validates --depth against the engine's 1-5 range for the same
// reason: an out-of-range depth must fail loudly, not traverse nothing.
func traceDepth(depth int) error {
	if depth < traceDepthMin || depth > traceDepthMax {
		return fail("invalid --depth %d (want %d-%d)", depth, traceDepthMin, traceDepthMax)
	}
	return nil
}

func cmdTrace(g *Globals) *cobra.Command {
	var project, sym, direction string
	var depth int
	var sel bool
	c := &cobra.Command{
		Use:     "trace --symbol <symbol> [--project <name>]",
		Short:   "BFS callers/callees of one symbol (who calls it / what it calls)",
		Aliases: []string{"kg_trace"},
		Example: `  tk trace --symbol ProcessOrder --project demo
  tk trace --symbol ProcessOrder --direction outbound --depth 3 --project demo`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, err := load(*g)
			if err != nil {
				return err
			}
			proj, err := requireProject(ctx, project, sel)
			if err != nil {
				return err
			}
			// Flag validation precedes the CBM gate: an out-of-range traversal
			// is a usage error even when the backend is missing.
			if err := traceDirection(direction); err != nil {
				return err
			}
			if err := traceDepth(depth); err != nil {
				return err
			}
			if _, _, err := ctx.needCBM(cmd.Context()); err != nil {
				return err
			}
			out, err := tracePath(cmd.Context(), ctx, proj, sym, direction, depth)
			if err != nil {
				return fail("%v", err)
			}
			// Zero callers/callees is a negative claim ("nothing calls X"), and
			// the engine's usual cause is a name-resolution miss — so an empty
			// traversal must prove itself against whole-project coverage before
			// it is reported. Same rule the search tools follow on both surfaces.
			if cbmexec.LooksEmpty(out) {
				var aerr error
				out, aerr = annotateAbsence(cmd.Context(), ctx, proj, out)
				if aerr != nil {
					return aerr
				}
			}
			return ctx.outFresh(cmd, proj, cbmexec.Truncate(out, ctx.budget("")), map[string]any{
				"symbol": sym, "project": proj, "direction": direction, "depth": depth,
			})
		},
	}
	c.Flags().StringVar(&project, "project", "", "project name")
	c.Flags().StringVar(&sym, "symbol", "", "symbol to trace (callers by default)")
	c.Flags().StringVar(&direction, "direction", "inbound", "inbound=callers, outbound=callees, both")
	c.Flags().IntVar(&depth, "depth", traceDepthMin, "traversal depth 1-5")
	selectFlag(c, &sel)
	_ = c.MarkFlagRequired("symbol")
	_ = c.RegisterFlagCompletionFunc("project", projectFlagCompletion(g))
	return c
}

func cmdGrep(g *Globals) *cobra.Command {
	var project, files, pat string
	var limit int
	var isRegex, sel bool
	c := &cobra.Command{
		Use:     "grep --pattern <pattern> [--project <name>]",
		Short:   "Project-scoped source-text search",
		Aliases: []string{"kg_grep"},
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, err := load(*g)
			if err != nil {
				return err
			}
			if isRegex {
				if _, err := regexp.Compile(pat); err != nil {
					return fail("invalid regex %q: %v (not empty results)", pat, err)
				}
			}
			proj, err := requireProject(ctx, project, sel)
			if err != nil {
				return err
			}
			if _, _, err := ctx.needCBM(cmd.Context()); err != nil {
				return err
			}
			out, err := ctx.cbmCallJSON(cmd.Context(), "search_code", map[string]any{"pattern": pat, "project": proj, "file_pattern": files, "limit": limit, "regex": isRegex})
			if err != nil {
				return fail("%v", err)
			}
			if cbmexec.LooksEmpty(out) {
				var aerr error
				out, aerr = annotateAbsence(cmd.Context(), ctx, proj, out)
				if aerr != nil {
					return aerr
				}
			}
			return ctx.outFresh(cmd, proj, cbmexec.Truncate(out, ctx.budget("")), map[string]any{"project": proj})
		},
	}
	c.Flags().StringVar(&project, "project", "", "project name")
	c.Flags().StringVar(&pat, "pattern", "", "search pattern (plain text, or --regex)")
	c.Flags().StringVar(&files, "files", "", "file glob filter")
	c.Flags().IntVar(&limit, "limit", 20, "max results")
	c.Flags().BoolVar(&isRegex, "regex", false, "treat pattern as regex (validation error on bad regex)")
	selectFlag(c, &sel)
	_ = c.MarkFlagRequired("pattern")
	_ = c.RegisterFlagCompletionFunc("project", projectFlagCompletion(g))
	return c
}

func cmdOutline(g *Globals) *cobra.Command {
	var project, labels, file string
	var limit int
	var sel bool
	c := &cobra.Command{
		Use:   "outline --file <file> [--project <name>]",
		Short: "Declarations in one file, in source order (cheap read alternative)",
		Example: `  tk outline --file orders.go --project demo
  tk outline --file src/main.go --label Function --project demo`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, err := load(*g)
			if err != nil {
				return err
			}
			proj, err := requireProject(ctx, project, sel)
			if err != nil {
				return err
			}
			rf := repoRelative(ctx, proj, file)
			if _, _, err := ctx.needCBM(cmd.Context()); err != nil {
				return err
			}
			payload := map[string]any{"project": proj, "file_path": rf, "limit": limit}
			if labels != "" {
				payload["labels"] = strings.Split(labels, ",")
			}
			out, err := ctx.cbmCallJSON(cmd.Context(), "get_file_outline", payload)
			if err != nil {
				return fail("%v", err)
			}
			return ctx.outFresh(cmd, proj, cbmexec.Truncate(out, ctx.budget("")), map[string]any{"project": proj, "file": rf})
		},
	}
	c.Flags().StringVar(&project, "project", "", "project name")
	c.Flags().StringVar(&file, "file", "", "repo-relative or absolute file path")
	c.Flags().StringVar(&labels, "label", "", "comma-separated node-label filter")
	c.Flags().IntVar(&limit, "limit", 100, "max declarations")
	selectFlag(c, &sel)
	_ = c.MarkFlagRequired("file")
	_ = c.RegisterFlagCompletionFunc("project", projectFlagCompletion(g))
	_ = c.RegisterFlagCompletionFunc("file", func(cmd *cobra.Command, args []string, _ string) ([]string, cobra.ShellCompDirective) {
		return nil, cobra.ShellCompDirectiveFilterFileExt
	})
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
	var sel bool
	c := &cobra.Command{
		Use:   "impact [--project <name>]",
		Short: "Map working-tree diff to affected symbols + blast radius",
		Example: `  tk impact --project demo
  tk impact --direction outbound --depth 3 --project demo`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, err := load(*g)
			if err != nil {
				return err
			}
			proj, err := requireProject(ctx, project, sel)
			if err != nil {
				return err
			}
			switch direction {
			case "inbound", "outbound", "both":
			default:
				return fail("invalid --direction %q (want inbound|outbound|both)", direction)
			}
			if _, _, err := ctx.needCBM(cmd.Context()); err != nil {
				return err
			}
			out, err := ctx.cbmCallJSON(cmd.Context(), "detect_changes", map[string]any{
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
	selectFlag(c, &sel)
	_ = c.RegisterFlagCompletionFunc("project", projectFlagCompletion(g))
	return c
}

func cmdArch(g *Globals) *cobra.Command {
	var project string
	var sel bool
	c := &cobra.Command{
		Use:   "arch [--project <name>]",
		Short: "Architecture brief (languages, packages, entry points, hotspots)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, err := load(*g)
			if err != nil {
				return err
			}
			proj, err := requireProject(ctx, project, sel)
			if err != nil {
				return err
			}
			if _, _, err := ctx.needCBM(cmd.Context()); err != nil {
				return err
			}
			out, err := ctx.cbmCallJSON(cmd.Context(), "get_architecture", map[string]any{"project": proj})
			if err != nil {
				return fail("%v", err)
			}
			return ctx.outFresh(cmd, proj, cbmexec.Truncate(out, ctx.budget("arch")), map[string]any{"project": proj})
		},
	}
	c.Flags().StringVar(&project, "project", "", "project name")
	selectFlag(c, &sel)
	_ = c.RegisterFlagCompletionFunc("project", projectFlagCompletion(g))
	return c
}

func cmdQuery(g *Globals) *cobra.Command {
	var project, cypher string
	var limit int
	var sel bool
	c := &cobra.Command{
		Use:     "query --cypher <cypher> [--project <name>]",
		Short:   "Raw read-only graph query (analysis profile)",
		Example: `  tk query --cypher "MATCH (f:Function) RETURN f.name LIMIT 5" --project demo`,
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, err := load(*g)
			if err != nil {
				return err
			}
			proj, err := requireProject(ctx, project, sel)
			if err != nil {
				return err
			}
			if _, _, err := ctx.needCBM(cmd.Context()); err != nil {
				return err
			}
			out, err := ctx.cbmCallJSON(cmd.Context(), "query_graph", map[string]any{"query": cypher, "project": proj, "max_rows": limit})
			if err != nil {
				return fail("%v", err)
			}
			return ctx.outFresh(cmd, proj, cbmexec.Truncate(out, ctx.budget("")), map[string]any{"project": proj})
		},
	}
	c.Flags().StringVar(&project, "project", "", "project name")
	c.Flags().StringVar(&cypher, "cypher", "", "read-only Cypher graph query")
	c.Flags().IntVar(&limit, "limit", 20, "row limit guardrail")
	selectFlag(c, &sel)
	_ = c.MarkFlagRequired("cypher")
	_ = c.RegisterFlagCompletionFunc("project", projectFlagCompletion(g))
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
			if _, _, err := ctx.needCBM(cmd.Context()); err != nil {
				return err
			}
			out, err := ctx.cbmRaw(cmd.Context(), args...)
			if err != nil {
				return fail("%v", err)
			}
			return ctx.out(cmd, cbmexec.Truncate(out, ctx.budget("")), nil)
		},
	}
	return c
}
