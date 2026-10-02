package cli

import (
	"bytes"
	"io"
	"os"
	"time"

	"github.com/ayayushsharma/true-knowledge/internal/logx"
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
	// The diagnostic channel is configured before any RunE can emit into it,
	// including the ones that fail inside load() and never reach a command body.
	// A level resolved after the first log line is a level that missed one.
	root.PersistentPreRunE = func(cmd *cobra.Command, args []string) error {
		logx.Configure(g.Verbose, g.Quiet)
		return nil
	}
	root.PersistentFlags().StringVar(&g.Home, "home", "", "override base home (TK_HOME equivalent: config+data+cache+state under it)")
	root.PersistentFlags().BoolVar(&g.JSON, "json", false, "stable JSON envelope output")
	root.PersistentFlags().IntVar(&g.Budget, "budget", 0, "char budget override (0 = config defaults)")
	root.PersistentFlags().BoolVar(&g.Verbose, "verbose", false, "debug narration on stderr (TK_LOG=debug)")
	root.PersistentFlags().BoolVar(&g.Quiet, "quiet", false, "stderr carries errors only; suppress progress (TK_LOG=error)")

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
		cmdTrace(g),
		cmdGrep(g),
		cmdArch(g),
		cmdQuery(g),
		cmdValidate(g),
		cmdCBM(g),
		cmdMCP(g),
		cmdResident(g),
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
	//
	// Two buffers, two channels. out is the answer — the bytes a caller pipes.
	// diag is the narration — progress steps and any stderr-level logging. They
	// are captured separately and recorded separately, because "what did it
	// print" and "what did it say while working" are different questions and
	// merging them makes the first one unanswerable.
	inv := &sinks{out: &bytes.Buffer{}, diag: &bytes.Buffer{}}
	currentSinks = inv
	root.SetOut(io.MultiWriter(os.Stdout, inv.out))
	wrapAll(root, inv)
	return root
}

// sinks holds the per-invocation record buffers for one root command tree.
type sinks struct {
	out  *bytes.Buffer
	diag *bytes.Buffer
}

// diagOrNil is the record sink for the stderr channel, or nil when no tree has
// been built (a bare Ctx in a test). nil is the honest "no record" value: both
// logx and progress treat it as one write branch rather than an error.
func (s *sinks) diagOrNil() io.Writer {
	if s == nil || s.diag == nil {
		return nil
	}
	return s.diag
}

func wrapAll(cmd *cobra.Command, inv *sinks) {
	if cmd.RunE != nil {
		orig := cmd.RunE
		start := time.Now()
		cmd.RunE = func(c *cobra.Command, args []string) (err error) {
			defer func() {
				finalize(inv, start, err)
			}()
			return orig(c, args)
		}
	}
	for _, sub := range cmd.Commands() {
		wrapAll(sub, inv)
	}
}

// finalize writes the unified trace record. Best-effort by construction:
// trace.Append swallows all errors, and a missing Ctx just skips.
func finalize(inv *sinks, start time.Time, err error) {
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
	text := maskedText(inv.out.String())
	diag := maskedText(inv.diag.String())
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
		"output": map[string]any{"chars": len(text), "text": text, "diag": diag},
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
