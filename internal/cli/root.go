// Package cli implements the full tk command surface.
package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/ayayushsharma/true-knowledge/internal/cbmexec"
	"github.com/ayayushsharma/true-knowledge/internal/config"
	"github.com/ayayushsharma/true-knowledge/internal/gitx"
	"github.com/ayayushsharma/true-knowledge/internal/logx"
	"github.com/ayayushsharma/true-knowledge/internal/paths"
	"github.com/ayayushsharma/true-knowledge/internal/progress"
	"github.com/ayayushsharma/true-knowledge/internal/resident"
	"github.com/ayayushsharma/true-knowledge/internal/store"
	"github.com/ayayushsharma/true-knowledge/internal/trace"
	"github.com/spf13/cobra"
)

// currentCtx is the invocation under trace. Set by load, read by finalize.
// Single-threaded CLI path only; MCP records are built inline in handle().
var currentCtx *Ctx

// currentSinks is the invocation's record buffers, read by finalize. Set
// alongside currentCtx by NewRoot.
var currentSinks *sinks

// Globals are bound to persistent flags.
type Globals struct {
	Home   string
	JSON   bool
	Budget int
	// Verbose raises the stderr diagnostic level to debug; Quiet drops it to
	// error and suppresses progress. Both are about the diagnostic channel
	// only: neither touches what lands on stdout, so `tk install --json
	// --verbose | jq` still yields an envelope and no log lines in it.
	Verbose bool
	Quiet   bool
	// LongLived marks a process that serves many requests in one run rather than
	// one command and exiting — `tk mcp` and nothing else.
	//
	// It has two consequences, and both follow from the same fact: there is no
	// single invocation to close. Progress is silenced, because the process's
	// stderr belongs to the client that connected to it rather than to a person
	// watching a terminal. And the diagnostic tee is not installed, because the
	// tee buffer is drained by finalize at exit — which never comes, so the
	// buffer would grow for as long as the session did. tk.log is still written
	// here, one record per tool call, by the MCP server's own recorder.
	LongLived bool
}

// Ctx carries resolved state for one invocation.
type Ctx struct {
	G      Globals
	Paths  paths.Paths
	Cfg    config.Config
	Reg    store.Registry
	Run    *cbmexec.Runner // nil when CBM binary missing (fail-open)
	CBMOK  bool
	Events []trace.Event // backend operations, recorded into tk.log
	// Prog is the stderr progress channel. Never nil after load: an inert
	// reporter is the disabled state, so no call site has to nil-check a
	// cosmetic feature.
	Prog *progress.Reporter
}

// progress returns the step reporter, inert when the command has none.
func (c *Ctx) progress() *progress.Reporter {
	if c == nil || c.Prog == nil {
		return progress.Disabled()
	}
	return c.Prog
}

// out renders human text or stable JSON envelope.
func (c *Ctx) out(cmd *cobra.Command, text string, fields map[string]any) error {
	return c.envelope(cmd, true, text, fields)
}

// outFailed renders the same envelope with ok:false and then returns err.
//
// Both faces have to agree. A machine reading --json and a shell reading $?
// are the two ways a caller learns the command failed; if the envelope claims
// ok while the exit code is 1, a caller that trusts the JSON is told the
// install worked. The text is still rendered first, so the diagnostic a person
// needs is on stdout before the error names it on stderr.
func (c *Ctx) outFailed(cmd *cobra.Command, text string, fields map[string]any, err error) error {
	_ = c.envelope(cmd, false, text, fields)
	return err
}

func (c *Ctx) envelope(cmd *cobra.Command, ok bool, text string, fields map[string]any) error {
	if !c.G.JSON {
		_, _ = fmt.Fprintln(cmd.OutOrStdout(), text)
		return nil
	}
	env := map[string]any{"ok": ok, "text": text}
	for k, v := range fields {
		env[k] = v
	}
	raw, _ := json.MarshalIndent(env, "", "  ")
	_, _ = fmt.Fprintln(cmd.OutOrStdout(), string(raw))
	return nil
}

// fail renders a non-zero error with remediation hint.
func fail(format string, args ...any) error {
	return errors.New("[tk] " + fmt.Sprintf(format, args...))
}

// firstLine clips multi-line diagnostics for event records.
func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// load resolves paths + config + registry + optional runner.
func load(g Globals) (*Ctx, error) {
	p := paths.Resolve(g.Home)
	logx.Debugf("paths home=%s config=%s data=%s cache=%s state=%s",
		displayHome(g.Home), p.Config, p.Data, p.Cache, p.State)
	if err := p.Ensure(); err != nil {
		return nil, fail("cannot create state dirs: %v", err)
	}
	cfg, err := config.Load(p.ConfigFile())
	if err != nil {
		return nil, fail("%v", err)
	}
	reg, err := store.Load(p.RegistryFile())
	if err != nil {
		return nil, fail("%v", err)
	}
	c := &Ctx{G: g, Paths: p, Cfg: cfg, Reg: reg}
	// The step reporter tees into the invocation's diag buffer, so a progress
	// trace shown on a terminal is also in tk.log. Without this the two
	// channels disagree about what happened, which is the one thing a record
	// cannot afford. A long-lived process is excluded: finalize drains that
	// buffer at exit, and exit never comes.
	rec := currentSinks.diagOrNil()
	if g.LongLived {
		rec = nil
	}
	logx.SetRecord(rec)
	c.Prog = progress.NewStderr(!g.LongLived && !g.Quiet).SetRecord(rec)
	if r, err := cbmexec.New(p, cfg); err == nil {
		c.Run = r
		c.CBMOK = true
		logx.Debugf("cbm resolved %s", r.Bin)
	} else {
		logx.Warnf("cbm unavailable (%v); graph and facts are unavailable, install hint: tk install", err)
	}
	logx.Debugf("registry %d project(s)", len(reg))
	currentCtx = c
	return c, nil
}

// displayHome names the base home for the log without leaking a home
// directory into a line nobody needs.
func displayHome(home string) string {
	if home == "" {
		return "(resolved)"
	}
	return home
}

func (c *Ctx) saveReg() error {
	return store.Save(c.Paths.RegistryFile(), c.Reg)
}

// budget resolves effective char budget.
func (c *Ctx) budget(kind string) int {
	if c.G.Budget > 0 {
		return c.G.Budget
	}
	if kind == "arch" {
		return c.Cfg.Budgets.ArchitectureChars
	}
	if kind == "notes_toc" {
		return c.Cfg.Budgets.NotesTocChars
	}
	return c.Cfg.Budgets.DefaultChars
}

// needCBM errors fail-open with install hint when binary missing.
func (c *Ctx) needCBM(ctx context.Context) (*cbmexec.Runner, context.Context, error) {
	if c.CBMOK && c.Run != nil {
		if ctx == nil {
			ctx = context.Background()
		}
		return c.Run, ctx, nil
	}
	return nil, ctx, fail("codebase-memory-mcp not installed; run `tk install` (or set TK_CBM_BIN). Facts/graph unavailable — agent may continue without them.")
}

// record appends a backend event for the unified trace log.
func (c *Ctx) record(e trace.Event) {
	c.Events = append(c.Events, e)
}

func sinceMs(t time.Time) int64 { return time.Since(t).Milliseconds() }

// cbmCall runs one graph tool through the single spawn wrapper,
// recording timing for tk.log. Use everywhere instead of r.Run.
//
// Writes go here and stay one-shot. A resident is a read accelerator: letting
// a write share the child's state would mean a resident that quietly mutates
// a store while an index is running, and the failure would be a corrupt graph
// rather than a visible error.
func (c *Ctx) cbmCall(ctx context.Context, tool string, payload map[string]any) (string, error) {
	t0 := time.Now()
	out, err := c.Run.Run(ctx, tool, payload)
	ev := trace.Event{Backend: "cbm", Op: tool, Ms: sinceMs(t0), OK: err == nil}
	if err != nil {
		ev.Error = firstLine(err.Error())
	}
	c.record(ev)
	return out, err
}

// residentTry asks a running resident to answer one read.
//
// It returns ok=false when there is no resident to ask, which is the normal case
// and not an error: the resident is opt-in, so most invocations spawn exactly
// as they did before it existed.
//
// A resident that answers with an error is NOT treated as absent — the engine
// said no, and re-running the same call in a fresh process would only produce
// the same no, slower. So the three outcomes stay distinct, which is why this
// returns a separate error rather than folding the refusal into ok=false:
// absent, refused, and answered must never collapse into one. Folding them
// reports a failed engine call as a successful empty result.
func (c *Ctx) residentTry(tool string, payload map[string]any, structured bool) (cbmexec.Result, bool, error) {
	if !c.CBMOK {
		return cbmexec.Result{}, false, nil
	}
	t0 := time.Now()
	reply, err := (&resident.Client{Addr: c.Paths.ResidentSocket()}).Call(resident.Request{
		Tool: tool, Args: payload, Structured: structured,
	})
	if err != nil {
		return cbmexec.Result{}, false, nil
	}
	if reply.Error != "" {
		// The engine refused. That is an answer, and it is recorded as a
		// failed cbm call so tk.log shows what happened without inventing a
		// second attempt nobody made.
		ev := trace.Event{Backend: "resident", Op: tool, Ms: sinceMs(t0), OK: false, Error: firstLine(reply.Error)}
		c.record(ev)
		return cbmexec.Result{}, true, errors.New(reply.Error)
	}
	res, perr := cbmexec.ParseResult(reply.Result)
	ev := trace.Event{Backend: "resident", Op: tool, Ms: sinceMs(t0), OK: perr == nil, Structured: res.Data != nil}
	if perr != nil {
		ev.Error = firstLine(perr.Error())
	}
	c.record(ev)
	if perr != nil {
		return cbmexec.Result{}, true, perr
	}
	return res, true, nil
}

// cbmCallJSON runs a read-only graph tool through the envelope-first
// wrapper, recording timing for tk.log. Writes stay on cbmCall.
//
// A resident is asked first and the spawn is the fallback. The order matters:
// the resident is warm and the spawn is not, so dialling first is what makes a
// running resident worth starting. Falling back on any dial failure is what
// makes the resident safe to leave running.
func (c *Ctx) cbmCallJSON(ctx context.Context, tool string, payload map[string]any) (string, error) {
	if res, ok, rerr := c.residentTry(tool, payload, false); ok {
		if rerr != nil {
			return "", fmt.Errorf("cbm %s: %w", tool, rerr)
		}
		if res.Text == "" && res.Data == nil {
			return "", fmt.Errorf("cbm %s: resident returned nothing", tool)
		}
		return res.Text, nil
	}
	t0 := time.Now()
	res, err := c.Run.RunJSONResult(ctx, tool, payload)
	ev := trace.Event{Backend: "cbm", Op: tool, Ms: sinceMs(t0), OK: err == nil, Detail: spawnDetail(res)}
	if err != nil {
		ev.Error = firstLine(err.Error())
	}
	c.record(ev)
	return res.Text, err
}

// spawnDetail annotates the call's engine cost when it was not the expected
// one. One is unremarkable and stays out of the log; two names the format
// fallback that caused it, which is the only way a reader can tell an engine
// that needed two spawns from one that needed one.
func spawnDetail(res cbmexec.Result) string {
	if res.Spawns > 1 {
		return fmt.Sprintf("spawns=%d", res.Spawns)
	}
	return ""
}

// cbmRaw runs raw `cbm cli` argv (tk cbm passthrough), recorded.
func (c *Ctx) cbmRaw(ctx context.Context, argv ...string) (string, error) {
	t0 := time.Now()
	out, err := c.Run.RunRaw(ctx, argv...)
	ev := trace.Event{Backend: "cbm", Op: "cli:" + strings.Join(argv, " "), Ms: sinceMs(t0), OK: err == nil}
	if err != nil {
		ev.Error = firstLine(err.Error())
	}
	c.record(ev)
	return out, err
}

// cbmDaemon runs `cbm daemon ...` (tk daemon, CLI-only), recorded.
func (c *Ctx) cbmDaemon(ctx context.Context, argv ...string) (string, error) {
	t0 := time.Now()
	out, err := c.Run.RunDaemon(ctx, argv...)
	ev := trace.Event{Backend: "cbm", Op: "daemon:" + strings.Join(argv, " "), Ms: sinceMs(t0), OK: err == nil}
	if err != nil {
		ev.Error = firstLine(err.Error())
	}
	c.record(ev)
	return out, err
}

// projectNames for completion (registry is the source; no live merge,
// to keep TAB instant).
func (c *Ctx) projectNames() []string {
	return c.Reg.Names()
}

// freshness describes whether the serving index covers the live tree.
// Git repos compare HEADs; plain dirs compare mtime fingerprints.
// cbmCallStructured runs a read-only graph tool asking for format:"json",
// recorded for tk.log like every other call. The caller gets both faces of
// the reply: Data for machines, Text for humans.
func (c *Ctx) cbmCallStructured(ctx context.Context, tool string, payload map[string]any) (cbmexec.Result, error) {
	if res, ok, rerr := c.residentTry(tool, payload, true); ok {
		if rerr != nil {
			return cbmexec.Result{}, fmt.Errorf("cbm %s: %w", tool, rerr)
		}
		return res, nil
	}
	t0 := time.Now()
	res, err := c.Run.RunStructured(ctx, tool, payload)
	ev := trace.Event{Backend: "cbm", Op: tool, Ms: sinceMs(t0), OK: err == nil, Structured: res.Data != nil, Detail: spawnDetail(res)}
	if err != nil {
		ev.Error = firstLine(err.Error())
	}
	c.record(ev)
	return res, err
}

// outEnvelope renders a tk-assembled answer for --json callers: the engine
// payloads under "data", tk's own fields beside them. Composite commands
// (explain, validate) call this because they make several calls and so have
// no single engine Result to render. Budgeting applies to the assembled
// object, so one pass trims whichever sub-payload is overspending.
func (c *Ctx) outEnvelope(cmd *cobra.Command, proj string, data map[string]any, fields map[string]any) error {
	if fields == nil {
		fields = map[string]any{}
	}
	if proj != "" {
		for k, v := range c.freshness(proj) {
			if _, exists := fields[k]; !exists {
				fields[k] = v
			}
		}
	}
	env := map[string]any{"ok": true, "data": cbmexec.BudgetResult(mustJSON(data), c.budget("json"))}
	for k, v := range fields {
		env[k] = v
	}
	raw, _ := json.MarshalIndent(env, "", "  ")
	_, _ = fmt.Fprintln(cmd.OutOrStdout(), string(raw))
	return nil
}

// mustJSON encodes a value tk assembled. A value that cannot be encoded is
// a bug in the caller, and a nil payload is the honest rendering of it: the
// command still exits 0 with the fields it did manage to record.
func mustJSON(v any) json.RawMessage {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	return raw
}

// outData renders a CBM read on the dual path: humans get the engine's
// rendered table, `--json` callers get the engine's own payload under
// "data" and no rendered "text" beside it.
//
// The text field is dropped on purpose. It is the same rows re-encoded as
// box drawing, so carrying both means shipping every result twice and
// leaving a machine to parse art to find the value it was given as JSON.
// Engine output is passed through verbatim: tk does not re-model CBM's
// schema, and anything tk needs to add (freshness, emptiness, the budget
// marker) is a sibling key it owns.
func (c *Ctx) outData(cmd *cobra.Command, res cbmexec.Result, fields map[string]any) error {
	if !c.G.JSON {
		_, _ = fmt.Fprintln(cmd.OutOrStdout(), res.Text)
		return nil
	}
	env := map[string]any{"ok": true}
	for k, v := range fields {
		env[k] = v
	}
	switch {
	case res.Data != nil:
		env["data"] = cbmexec.BudgetResult(res.Data, c.budget("json"))
	default:
		// An engine that predates format:"json". The text is the only
		// payload there is, so it is passed through rather than dropped —
		// a machine on an old binary gets the old shape, not a hole.
		env["text"] = res.Text
		env["structured"] = false
	}
	env["empty"] = c.emptyResult(res)
	raw, _ := json.MarshalIndent(env, "", "  ")
	_, _ = fmt.Fprintln(cmd.OutOrStdout(), string(raw))
	return nil
}

// outDataFresh renders like outData but merges freshness fields for proj.
func (c *Ctx) outDataFresh(cmd *cobra.Command, proj string, res cbmexec.Result, fields map[string]any) error {
	if fields == nil {
		fields = map[string]any{}
	}
	for k, v := range c.freshness(proj) {
		if _, exists := fields[k]; !exists {
			fields[k] = v
		}
	}
	return c.outData(cmd, res, fields)
}

// found reports whether a search reply found anything.
//
// The two faces judge differently, on purpose. A human reads the rendered
// reply, so the human path keeps judging the rendered reply and its output
// cannot move; only a machine, who is handed the payload, is judged on the
// engine's own counters. Falling back to the text markers when the engine
// predates format:"json" keeps the judgement available either way.
func (c *Ctx) found(res cbmexec.Result) bool {
	if c.G.JSON && res.Data != nil {
		return !cbmexec.LooksEmptyData(res.Data)
	}
	return !cbmexec.LooksEmpty(res.Text)
}

// emptyResult reports whether the call proved there was nothing to find.
// Structured replies are judged on the engine's own counters; a reply that
// states no counter is never called empty, because "found nothing" and
// "found an unknown amount" are different claims and only one of them is
// supported by the evidence.
func (c *Ctx) emptyResult(res cbmexec.Result) bool {
	if res.Data != nil {
		return cbmexec.LooksEmptyData(res.Data)
	}
	return cbmexec.LooksEmpty(res.Text)
}

// zoekt_head/zoekt_fresh are the trigram-shard side of the same check
// (registry reads only — worktree drift counts live in source-search).
// Fields merge into --json envelopes so agents can gate absence claims.
func (c *Ctx) freshness(proj string) map[string]any {
	p, ok := c.Reg[proj]
	if !ok {
		return map[string]any{"fresh": false}
	}
	if head := gitx.Head(p.Path); head != "" {
		return map[string]any{
			"head":        p.Head,
			"current":     head,
			"fresh":       head == p.Head,
			"zoekt_head":  p.ZoektHead,
			"zoekt_fresh": p.ZoektHead == head,
		}
	}
	live, err := store.Fingerprint(p.Path)
	if err != nil {
		return map[string]any{"head": p.Fingerprint, "fresh": false, "zoekt_head": p.ZoektHead, "zoekt_fresh": false}
	}
	return map[string]any{
		"head":        p.Fingerprint,
		"current":     live,
		"fresh":       live == p.Fingerprint,
		"zoekt_head":  p.ZoektHead,
		"zoekt_fresh": p.ZoektHead == "files" && live == p.Fingerprint,
	}
}

// outFresh renders like out but merges freshness fields for proj.
func (c *Ctx) outFresh(cmd *cobra.Command, proj, text string, fields map[string]any) error {
	if fields == nil {
		fields = map[string]any{}
	}
	for k, v := range c.freshness(proj) {
		if _, exists := fields[k]; !exists {
			fields[k] = v
		}
	}
	return c.out(cmd, text, fields)
}
