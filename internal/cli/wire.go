package cli

import (
	"bytes"
	"io"
	"os"
	"time"

	"github.com/ayayushsharma/true-knowledge/internal/memory"
	"github.com/ayayushsharma/true-knowledge/internal/trace"
	"github.com/spf13/cobra"
)

// NewRoot builds the full tk command tree.
func NewRoot(g *Globals) *cobra.Command {
	root := &cobra.Command{
		Use:   "tk",
		Short: "Thin Go shipper over codebase-memory-mcp (Linux-first)",
		Long: `tk owns paths + config + spawn + render. CBM owns graph, store, daemon, watcher.
All graph work = codebase-memory-mcp cli <tool> --json via one wrapper.`,
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.PersistentFlags().StringVar(&g.Home, "home", "", "override base home (TK_HOME equivalent: config+data+cache+state under it)")
	root.PersistentFlags().BoolVar(&g.JSON, "json", false, "stable JSON envelope output")
	root.PersistentFlags().IntVar(&g.Budget, "budget", 0, "char budget override (0 = config defaults)")

	root.AddCommand(
		cmdInit(g),
		cmdRegister(g),
		cmdIndex(g),
		cmdSync(g),
		cmdStatus(g),
		cmdFind(g),
		cmdSourceSearch(g),
		cmdOutline(g),
		cmdImpact(g),
		cmdDaemon(g),
		cmdExplain(g),
		cmdGrep(g),
		cmdArch(g),
		cmdQuery(g),
		cmdValidate(g),
		cmdCBM(g),
		cmdMCP(g),
		cmdConfig(g),
		cmdMigrate(g),
		cmdInstall(g),
		cmdSetup(g),
		cmdMCPInstall(g),
		cmdMem(g),
		cmdNote(g),
		cmdLedger(g),
	)

	// Central trace capture: all rendered output flows through root's
	// writers, so one tee sees every command. Per-command RunE wrappers
	// finalize the record (timing, exit, events) on return.
	buf := &bytes.Buffer{}
	root.SetOut(io.MultiWriter(os.Stdout, buf))
	wrapAll(root, buf)
	return root
}

func wrapAll(cmd *cobra.Command, buf *bytes.Buffer) {
	if cmd.RunE != nil {
		orig := cmd.RunE
		start := time.Now()
		cmd.RunE = func(c *cobra.Command, args []string) (err error) {
			defer func() {
				finalize(buf, start, err)
			}()
			return orig(c, args)
		}
	}
	for _, sub := range cmd.Commands() {
		wrapAll(sub, buf)
	}
}

// finalize writes the unified trace record. Best-effort by construction:
// trace.Append swallows all errors, and a missing Ctx just skips.
func finalize(buf *bytes.Buffer, start time.Time, err error) {
	c := currentCtx
	if c == nil {
		return
	}
	code := 0
	errText := ""
	if err != nil {
		code = 1
		errText = err.Error()
	}
	argv := os.Args[1:]
	for i, a := range argv {
		if len(memory.DetectSecret(a)) > 0 {
			argv[i] = "[REDACTED]"
		}
	}
	text := maskedText(buf.String())
	evs := make([]trace.Event, 0, len(c.Events))
	for _, ev := range c.Events {
		ev.Detail = maskedText(ev.Detail)
		ev.Error = maskedText(ev.Error)
		evs = append(evs, ev)
	}
	rec := map[string]any{
		"v":      1,
		"ts":     start.Unix(),
		"dur_ms": time.Since(start).Milliseconds(),
		"argv":   argv,
		"cwd":    cwd(),
		"exit":   code,
		"events": evs,
		"output": map[string]any{"chars": len(text), "text": text},
	}
	if errText != "" {
		rec["error"] = maskedText(errText)
	}
	trace.Append(c.Paths.LogFile(), rec)
}

// maskedText masks secrets in any recorded output: trace.Redact (high
// precision) plus memory.SecretMask (JWT, key-assignment spans).
func maskedText(s string) string {
	return memory.SecretMask(trace.Redact(s))
}

func cwd() string {
	d, err := os.Getwd()
	if err != nil {
		return ""
	}
	return d
}
