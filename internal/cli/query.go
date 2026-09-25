package cli

import (
	"context"
	"encoding/json"
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

// cbmReadArgs is one CBM read and how to render it.
type cbmReadArgs struct {
	proj    string
	tool    string
	payload map[string]any
	kind    string         // budget kind: "" default, "arch" for the architecture brief
	fields  map[string]any // extra --json fields (project is added here)
	gate    bool           // prove emptiness against whole-project coverage
}

// cbmRead is the single exit for every CBM read command, so the dual path is
// decided in one place: a --json caller gets the engine's own payload, and
// every other caller keeps the rendered table it has always been given.
//
// gate is per command on purpose. It changes what a human reads — an empty
// reply is annotated with a coverage verdict — so a tool that never makes
// negative claims is left alone. On the JSON face there is no text to append
// to, so the gate becomes a `coverage` field instead.
func (c *Ctx) cbmRead(cmd *cobra.Command, a cbmReadArgs) error {
	if a.fields == nil {
		a.fields = map[string]any{}
	}
	// Before the branch, not after: both faces carry the same envelope
	// metadata. A --json caller otherwise cannot tell which project an answer
	// is about without re-reading its own argv, and for payloads that do not
	// name the project themselves (search_code, get_architecture) there is
	// nothing left to infer it from.
	a.fields["project"] = a.proj
	if c.G.JSON {
		res, err := c.cbmCallStructured(cmd.Context(), a.tool, a.payload)
		if err != nil {
			return fail("%v", err)
		}
		if a.gate && c.emptyResult(res) {
			cov, err := c.coverageData(cmd.Context(), a.proj)
			if err != nil {
				return fail("no results and coverage check failed: %v — absence unverified", err)
			}
			a.fields["coverage"] = cov
		}
		return c.outDataFresh(cmd, a.proj, res, a.fields)
	}
	out, err := c.cbmCallJSON(cmd.Context(), a.tool, a.payload)
	if err != nil {
		return fail("%v", err)
	}
	if a.gate && cbmexec.LooksEmpty(out) {
		if out, err = annotateAbsence(cmd.Context(), c, a.proj, out); err != nil {
			return err
		}
	}
	return c.outFresh(cmd, a.proj, cbmexec.Truncate(out, c.budget(a.kind)), a.fields)
}

// coverageData is the structured twin of coverageVerdict: on the JSON face
// the absence gate hands back the engine's own coverage payload, so an agent
// can gate on the same fields tk reads instead of on a rendered verdict
// string. An engine without format support degrades to the verdict text
// rather than leaving the field empty.
func (c *Ctx) coverageData(ctx context.Context, proj string) (any, error) {
	res, err := c.cbmCallStructured(ctx, "check_index_coverage", map[string]any{"project": proj, "scopes": []string{"."}})
	if err != nil {
		return nil, err
	}
	if res.Data != nil {
		return json.RawMessage(res.Data), nil
	}
	verdict, err := coverageVerdict(ctx, c, proj)
	if err != nil {
		return nil, err
	}
	return verdict, nil
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
			return ctx.cbmRead(cmd, cbmReadArgs{
				proj: proj, tool: tool, payload: payload, gate: true,
				fields: map[string]any{"route": tool},
			})
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
			if ctx.G.JSON {
				// explain answers one question with two calls, so the JSON
				// face is two engine payloads under their own keys. Joining
				// them into one string would hand a parser prose where it
				// expects data, which is the thing this change exists to end.
				snip, err := ctx.cbmCallStructured(cmd.Context(), "get_code_snippet", map[string]any{"qualified_name": sym, "project": proj})
				if err != nil {
					return fail("%v", err)
				}
				data := map[string]any{"definition": payloadOf(snip)}
				tr, terr := tracePathStructured(cmd.Context(), ctx, proj, sym, "both", 1)
				if terr != nil {
					// The text face tolerates a dead traversal and says so
					// inline; the JSON face says it with a field.
					data["traversal"] = nil
					data["traversal_error"] = firstLine(terr.Error())
				} else {
					data["traversal"] = payloadOf(tr)
				}
				return ctx.outEnvelope(cmd, proj, data, map[string]any{"symbol": sym, "project": proj})
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

// tracePayload is the one trace_path argument set in the product, shared by
// `trace` and `explain` on both faces, so the two surfaces can never drift in
// payload shape. direction is inbound|outbound|both; depth is 1-5.
func tracePayload(proj, sym, direction string, depth int) map[string]any {
	return map[string]any{"project": proj, "function_name": sym, "direction": direction, "depth": depth}
}

// tracePath is the rendered-tree spawn.
func tracePath(ctx context.Context, c *Ctx, proj, sym, direction string, depth int) (string, error) {
	return c.cbmCallJSON(ctx, "trace_path", tracePayload(proj, sym, direction, depth))
}

// tracePathStructured is the same call asking for the engine's payload.
func tracePathStructured(ctx context.Context, c *Ctx, proj, sym, direction string, depth int) (cbmexec.Result, error) {
	return c.cbmCallStructured(ctx, "trace_path", tracePayload(proj, sym, direction, depth))
}

// payloadOf is one composite key's worth of engine output. On an engine that
// predates format:"json" it degrades to the rendered text, because a
// composite that returns null where a value belongs helps nobody.
func payloadOf(res cbmexec.Result) any {
	if res.Data != nil {
		return json.RawMessage(res.Data)
	}
	return res.Text
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
			// Zero callers/callees is a negative claim ("nothing calls X"), and
			// the engine's usual cause is a name-resolution miss — so an empty
			// traversal must prove itself against whole-project coverage before
			// it is reported. Same rule the search tools follow on both surfaces.
			return ctx.cbmRead(cmd, cbmReadArgs{
				proj:    proj,
				tool:    "trace_path",
				payload: tracePayload(proj, sym, direction, depth),
				gate:    true,
				fields:  map[string]any{"symbol": sym, "direction": direction, "depth": depth},
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
			return ctx.cbmRead(cmd, cbmReadArgs{
				proj:    proj,
				tool:    "search_code",
				payload: map[string]any{"pattern": pat, "project": proj, "file_pattern": files, "limit": limit, "regex": isRegex},
				gate:    true,
			})
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
			return ctx.cbmRead(cmd, cbmReadArgs{
				proj: proj, tool: "get_file_outline", payload: payload,
				fields: map[string]any{"file": rf},
			})
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
			return ctx.cbmRead(cmd, cbmReadArgs{
				proj: proj, tool: "detect_changes",
				payload: map[string]any{"project": proj, "direction": direction, "depth": depth, "limit": limit},
			})
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
			return ctx.cbmRead(cmd, cbmReadArgs{
				proj: proj, tool: "get_architecture",
				payload: map[string]any{"project": proj}, kind: "arch",
			})
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
			return ctx.cbmRead(cmd, cbmReadArgs{
				proj: proj, tool: "query_graph",
				payload: map[string]any{"query": cypher, "project": proj, "max_rows": limit},
			})
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
