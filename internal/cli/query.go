package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/ayayushsharma/true-knowledge/internal/cbmexec"
	"github.com/ayayushsharma/true-knowledge/internal/mcp"
	"github.com/ayayushsharma/true-knowledge/internal/trace"
	"github.com/ayayushsharma/true-knowledge/internal/zoekttext"
	"github.com/spf13/cobra"
)

var metaChars = regexp.MustCompile(`[.*()\[\]{}+?^$|\\]`)

// zoektOperatorPrefixes are the query-language keywords zoekt's parser claims
// for itself, copied from its prefixes table (query/parse.go) rather than
// guessed. A pattern starting with one would filter instead of search.
var zoektOperatorPrefixes = []string{
	"archived:", "b:", "branch:", "c:", "case:", "content:", "f:", "file:",
	"fork:", "public:", "r:", "regex:", "repo:", "lang:", "sym:", "t:",
	"type:", "meta.",
}

// zoektReservedWords are the bare words that change how surrounding terms
// combine: "or" makes a query a union, where a caller writing "or" almost
// always meant the word.
var zoektReservedWords = map[string]bool{"or": true, "and": true, "not": true}

// zoektUnsupported is the short list of constructs measured to answer
// differently from RE2, and therefore never allowed to answer here. Each one
// was probed against a real index, hit for hit, against Go's own regexp; the
// evidence lives in internal/zoekttext as TestSearchAgreesWithRE2 and
// TestUnsupportedConstructsAnswerDifferently.
//
//	\z   end-of-text. Always zero: `backoff\z` finds nothing in a line ending
//	     in backoff, while `retry.*backoff$` finds it. The whole \z branch is
//	     dropped, so an alternation keeps only its other side.
//	\A   start-of-text. Treated as start of *line*: `\Aret` finds 1 of the 3
//	     lines RE2 finds, `\Athis line` finds none.
//	(?s) dot-matches-newline. The flag degrades matching to a near-match-
//	     everything filter: `(?s)retry.*backoff` answers 18 of 19 probe lines
//	     where RE2 answers 3. False positives, the worse direction.
//	(?i) case-folding, in every spelling. `(?i)demo` searches case-sensitively
//	     for the *uppercased* text, so it misses the `Demo` that is there;
//	     `(?-i)upper` is ignored, so it over-matches the `UPPER` that RE2
//	     would not. Any inline group naming `i` diverges, however it spells it.
//
// Everything else agrees with RE2 hit for hit: `^`, `$`, `\b`, `\d` and the
// other classes, POSIX classes, Unicode scripts, counted repetition,
// alternation, named and non-capturing groups, `\Q...\E`, and the `m` and `U`
// flags. So this list is short because it was measured, not because regex
// syntax is scary.
var zoektUnsupported = []string{`\z`, `\A`}

// zoektTextQuery reports whether tk's own index can answer this pattern with
// the same meaning the engine would give it, and returns the query to run when
// it can: one token, not an operator, not a reserved word, and none of the
// constructs in zoektUnsupported.
//
// Two of the four exclusions are about zoekt's query language rather than its
// regex engine:
//
//   - Two or more tokens. zoekt ANDs terms per *file*, so "retry backoff"
//     matches a file whose two words sit on different lines, where the engine's
//     search_code reads one pattern. That is a phrase search the caller meant.
//   - An operator prefix (file:, repo:, case:, …), which zoekt would turn into
//     a filter over names, and a bare combining word (or, and, not), which
//     changes what surrounds it.
//
// The other two, zoektUnsupported and the flag scan, are the ones that could
// answer "nothing is here" wrongly, and they exist because of measurement
// rather than reading.
func zoektTextQuery(q string) (string, bool) {
	if len(strings.Fields(q)) != 1 {
		return "", false
	}
	lower := strings.ToLower(q)
	if zoektReservedWords[lower] {
		return "", false
	}
	for _, p := range zoektOperatorPrefixes {
		if strings.HasPrefix(lower, p) {
			return "", false
		}
	}
	// \Q...\E quotes its contents, so a \z or (?i) inside one is text.
	for _, unquoted := range unquoteAll(q) {
		if mentionsFoldOrDotAll(unquoted) {
			return "", false
		}
		for _, bad := range zoektUnsupported {
			if strings.Contains(unquoted, bad) {
				return "", false
			}
		}
	}
	return q, true
}

// mentionsFoldOrDotAll reports whether any inline flag group names the `i`
// or `s` flag. Any spelling counts — `(?i)`, `(?-i)`, `(?is:...)`, `(?s)` —
// because every one diverges: `(?i)` searches the uppercased text sensitively,
// `(?-i)` is ignored so the default fold stays on, `(?s)` degrades to a broad
// over-match. The scan reads only the flag letters right after `(?`, so a group
// name is never mistaken for a flag: `(?P<miss>...)` stops at `P`. Groups the
// index honors (`m`, `U`, `(?:...)`, `(?P<...>...)`) pass through untouched.
func mentionsFoldOrDotAll(q string) bool {
	for i := 0; i+2 < len(q); i++ {
		if q[i] != '(' || q[i+1] != '?' {
			continue
		}
		for j := i + 2; j < len(q); j++ {
			switch q[j] {
			case 'i', 's':
				return true
			case 'm', 'U', '-':
				continue
			default:
				// End of the flag letters: `)`, `:`, a group name, anything.
				j = len(q)
			}
		}
	}
	return false
}

// unquoteAll splits a pattern into the spans a \Q...\E quote does not cover,
// so a scan for anchors sees only the parts the regex engine will interpret.
func unquoteAll(q string) []string {
	if !strings.Contains(q, `\Q`) {
		return []string{q}
	}
	var out []string
	rest := q
	for {
		i := strings.Index(rest, `\Q`)
		if i < 0 {
			out = append(out, rest)
			return out
		}
		out = append(out, rest[:i])
		rest = rest[i+2:]
		if j := strings.Index(rest, `\E`); j >= 0 {
			rest = rest[j+2:]
		}
	}
}

// findTextScope is the absence verdict this route owes its caller, and it is
// deliberately silent when there is nothing to judge. A hit speaks for itself
// and a header would cost every caller a line of tokens; an empty or cut
// result is exactly where a bare answer reads as a claim about the codebase,
// so only those two cases speak.
//
// The wording names the index and the snapshot rather than a file count,
// because zoekt does not report how many files it looked at — its FileCount is
// files *containing a match* (verified in zoekt api.go), which would print "0
// files searched" beside "0 matches" and verify nothing. Worktree drift above
// this line is the disclosure that qualifies the snapshot.
func findTextScope(res zoekttext.Result, head string) string {
	snapshot := "the tk text index"
	if head != "" {
		snapshot = "the tk text index at HEAD@" + shortHead(head)
	}
	switch {
	case len(res.Matches) == 0:
		return fmt.Sprintf("[find: 0 matches in %s — absence verified against that snapshot]\n", snapshot)
	case res.HasMore:
		return fmt.Sprintf("[find: %s; %d matches, showing %d]\n", snapshot, res.Total, len(res.Matches))
	}
	return ""
}

// findText answers a text pattern from tk's own trigram index.
//
// Only zoektTextQuery-verified patterns arrive here; see its comment for the
// differential test that lets regexes come here too. What this route buys is
// the whole cost of a text search: no process spawn, no engine round trip, and
// no engine installed at all.
//
// The verdict is the part that must not be lost. An empty answer used to be
// gated on check_index_coverage so it could prove it had looked; the local index
// makes the same proof cheaper and better, because what it searched is knowable
// rather than probed. An empty answer says so in words; a cut one says how much
// was withheld.
func (c *Ctx) findText(cmd *cobra.Command, proj, q string, limit int) error {
	text, fields, err := c.findTextRead(cmd, proj, q, limit)
	if err != nil {
		return err
	}
	return c.outFresh(cmd, proj, text, fields)
}

// findTextRead is the half of find's text route that can be tested without a
// writer: it refreshes, searches, and returns the text a human reads beside the
// --json fields it is paired with. Both come from this one computation, the way
// runSourceSearchFleet builds its fields before truncating — a caller can never
// see fields that describe a different run than the text above them.
func (c *Ctx) findTextRead(cmd *cobra.Command, proj, q string, limit int) (string, map[string]any, error) {
	p := c.Reg[proj]
	// Same auto-refresh as source-search: clean HEAD is the ~1ms no-op, a
	// moved HEAD is a delta reindex, so a search never serves stale commits.
	if _, zerr := c.ensureZoektAndTouch(cmd.Context(), proj, p.Path, c.Cfg.IndexMode); zerr != nil {
		return "", nil, fail("find: %v", zerr)
	}
	if err := c.saveReg(); err != nil {
		return "", nil, fail("save registry: %v", err)
	}
	p = c.Reg[proj]
	note, mod, untracked := zoektStaleNote(p, true, "find")
	t0 := time.Now()
	res, err := mcp.ZoektQuery(cmd.Context(), c.Paths.ZoektShards(proj), p.Path, q, "", limit)
	ev := trace.Event{Backend: "zoekt", Op: "search", Ms: time.Since(t0).Milliseconds(), OK: err == nil, Detail: "find route"}
	if err != nil {
		ev.Error = firstLine(err.Error())
	}
	c.record(ev)
	if err != nil {
		// A missing index is a failure, never an empty result: the shards were
		// never searched, so there is nothing to be absent from.
		return "", nil, fail("find: %v (shards missing? run `tk index %s`)", err, proj)
	}
	body := ""
	if len(res.Matches) > 0 {
		body = mcp.RenderMatches(res.Matches, false)
	}
	text := note + findTextScope(res, p.ZoektHead) + body
	return cbmexec.Truncate(text, c.budget("")), map[string]any{
		"route":              "zoekt",
		"backend":            "zoekt",
		"project":            proj,
		"query":              q,
		"zoekt_head":         p.ZoektHead,
		"worktree_modified":  mod,
		"worktree_untracked": untracked,
		"matches_found":      res.Total,
		"matches_returned":   len(res.Matches),
		"files_matched":      res.Files,
		"truncated":          res.HasMore,
	}, nil
}

// requireProject resolves the project a project-resolving command runs against,
// or fails naming every route that exists for it.
//
// Nothing is inferred. A bare invocation never picks a project, never opens a
// menu, and never auto-selects the only registered one: a project is either
// stated (--project) or requested (--select). --project wins when both are
// passed and --select is then ignored; a --project that is not in the registry
// is a hard error naming the command that would register it, checked here so a
// typo cannot reach CBM or degrade into a quiet `fresh: false`.
//
// --select is the only path to a menu, and it is gated for humans: under
// --json, off a TTY, or with ui.picker off it hard-fails naming --project rather
// than blocking on a pipe or guessing.
func requireProject(c *Ctx, verb, flag string, sel bool) (string, error) {
	if flag != "" {
		if _, ok := c.Reg[flag]; !ok {
			return "", unknownProject(verb, flag, c)
		}
		return flag, nil
	}
	if !sel {
		return "", fail("%s needs a project: pass --project <name> (registered: %s), or --select to pick one; see `tk status`", verb, listNames(c))
	}
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

// unknownProject is the one wording for a name the registry does not hold, so
// every project-resolving verb fails identically and hands back the command
// that fixes it. An empty registry gets the shorter form; listing "none" next
// to a register command twice reads worse than saying it once.
func unknownProject(verb, name string, c *Ctx) error {
	if len(c.Reg) == 0 {
		return fail("%s: project %q is not currently registered; register it with `tk register <path> --name %s`", verb, name, name)
	}
	return fail("%s: project %q is not currently registered (registered: %s); register it with `tk register <path> --name %s`", verb, name, listNames(c), name)
}

// selectFlag registers -s/--select on a project-resolving command: ask for the
// interactive project picker. It is the only way a menu opens — a bare
// invocation fails with the routing hint instead — so it exists for the case
// where the human does not know the name and does not want to look it up. It is
// per-command rather than a root persistent flag so the flag only shows up
// (in --help, completions, and man pages) on the commands that honor it.
// Precedence: an explicit --project wins, --select is then ignored.
func selectFlag(c *cobra.Command, sel *bool) {
	c.Flags().BoolVarP(sel, "select", "s", false, "open the interactive project picker (ignored when --project is set)")
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
		Short:   "Deterministic router: single-token→own index, ident→graph",
		Aliases: []string{"kg_find"},
		Example: `  tk find --query ProcessOrder --project demo
  tk find --query internal/cli/query.go --project demo`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, err := load(*g)
			if err != nil {
				return err
			}
			q := query
			proj, err := requireProject(ctx, cmd.Name(), project, sel)
			if err != nil {
				return err
			}
			// The text route is answered in one of two places, and which one is
			// decided before needCBM: a pattern tk's own index answers with the
			// engine's meaning must not require an engine it never calls. A phrase
			// or an operator-shaped pattern stays on the engine, which is where its
			// meaning comes from. See zoektTextQuery.
			if metaChars.MatchString(q) || strings.Contains(q, "/") {
				if label != "" {
					return fail("--label needs a graph route (identifier or multi-word query), not a text pattern")
				}
				if zq, ok := zoektTextQuery(q); ok {
					return ctx.findText(cmd, proj, zq, limit)
				}
			}
			if _, _, err := ctx.needCBM(cmd.Context()); err != nil {
				return err
			}
			var tool string
			var payload map[string]any
			switch {
			case metaChars.MatchString(q) || strings.Contains(q, "/"):
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
			proj, err := requireProject(ctx, cmd.Name(), project, sel)
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
			proj, err := requireProject(ctx, cmd.Name(), project, sel)
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
			proj, err := requireProject(ctx, cmd.Name(), project, sel)
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
			proj, err := requireProject(ctx, cmd.Name(), project, sel)
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
			proj, err := requireProject(ctx, cmd.Name(), project, sel)
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
			proj, err := requireProject(ctx, cmd.Name(), project, sel)
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
			proj, err := requireProject(ctx, cmd.Name(), project, sel)
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
