// Package cli implements the full tk command surface.
package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/true-knowledge/tk/internal/cbmexec"
	"github.com/true-knowledge/tk/internal/config"
	"github.com/true-knowledge/tk/internal/gitx"
	"github.com/true-knowledge/tk/internal/paths"
	"github.com/true-knowledge/tk/internal/store"
	"github.com/true-knowledge/tk/internal/trace"
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
// Fields merge into --json envelopes so agents can gate absence claims.
func (c *Ctx) freshness(proj string) map[string]any {
	p, ok := c.Reg[proj]
	if !ok {
		return map[string]any{"fresh": false}
	}
	if head := gitx.Head(p.Path); head != "" {
		return map[string]any{"head": p.Head, "current": head, "fresh": head == p.Head}
	}
	live, err := store.Fingerprint(p.Path)
	if err != nil {
		return map[string]any{"head": p.Fingerprint, "fresh": false}
	}
	return map[string]any{"head": p.Fingerprint, "current": live, "fresh": live == p.Fingerprint}
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
