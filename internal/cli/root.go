// Package cli implements the full tk command surface.
package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"github.com/true-knowledge/tk/internal/cbmexec"
	"github.com/true-knowledge/tk/internal/config"
	"github.com/true-knowledge/tk/internal/paths"
	"github.com/true-knowledge/tk/internal/store"
)

// Globals are bound to persistent flags.
type Globals struct {
	Home   string
	JSON   bool
	Budget int
}

// Ctx carries resolved state for one invocation.
type Ctx struct {
	G     Globals
	Paths paths.Paths
	Cfg   config.Config
	Reg   store.Registry
	Run   *cbmexec.Runner // nil when CBM binary missing (fail-open)
	CBMOK bool
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

// projectNames for completion.
func (c *Ctx) projectNames() []string {
	names := c.Reg.Names()
	if c.CBMOK && c.Run != nil {
		// Best-effort live merge is skipped for completion speed; registry is source.
		_ = os.Getenv("TK_LIVE_COMPLETION")
	}
	return names
}
