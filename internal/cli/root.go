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
	"github.com/ayayushsharma/true-knowledge/internal/paths"
	"github.com/ayayushsharma/true-knowledge/internal/store"
	"github.com/ayayushsharma/true-knowledge/internal/trace"
	"github.com/spf13/cobra"
)

// currentCtx is the invocation under trace. Set by load, read by finalize.
// Single-threaded CLI path only; MCP records are built inline in handle().
var currentCtx *Ctx

// Globals are bound to persistent flags.
type Globals struct {
	Home   string
	JSON   bool
	Budget int
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
}

// out renders human text or stable JSON envelope.
func (c *Ctx) out(cmd *cobra.Command, text string, fields map[string]any) error {
	if !c.G.JSON {
		_, _ = fmt.Fprintln(cmd.OutOrStdout(), text)
		return nil
	}
	env := map[string]any{"ok": true, "text": text}
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
	if r, err := cbmexec.New(p, cfg); err == nil {
		c.Run = r
		c.CBMOK = true
	}
	currentCtx = c
	return c, nil
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

// cbmCallJSON runs a read-only graph tool through the envelope-first
// wrapper, recording timing for tk.log. Writes stay on cbmCall.
func (c *Ctx) cbmCallJSON(ctx context.Context, tool string, payload map[string]any) (string, error) {
	t0 := time.Now()
	out, err := c.Run.RunJSON(ctx, tool, payload)
	ev := trace.Event{Backend: "cbm", Op: tool, Ms: sinceMs(t0), OK: err == nil}
	if err != nil {
		ev.Error = firstLine(err.Error())
	}
	c.record(ev)
	return out, err
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
	t0 := time.Now()
	res, err := c.Run.RunStructured(ctx, tool, payload)
	ev := trace.Event{Backend: "cbm", Op: tool, Ms: sinceMs(t0), OK: err == nil, Structured: res.Data != nil}
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
