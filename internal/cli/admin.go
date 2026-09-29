package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/ayayushsharma/true-knowledge/internal/backends"
	"github.com/ayayushsharma/true-knowledge/internal/config"
	"github.com/ayayushsharma/true-knowledge/internal/installer"
	"github.com/ayayushsharma/true-knowledge/internal/resident"
	"github.com/spf13/cobra"
)

func cmdConfig(g *Globals) *cobra.Command {
	c := &cobra.Command{Use: "config", Short: "Inspect/change tk config (tk owns, CBM follows via env)"}
	c.AddCommand(
		&cobra.Command{
			Use:   "list",
			Short: "Show all settings",
			RunE: func(cmd *cobra.Command, args []string) error {
				ctx, err := load(*g)
				if err != nil {
					return err
				}
				raw, _ := json.MarshalIndent(ctx.Cfg, "", "  ")
				return ctx.out(cmd, string(raw), map[string]any{"config": ctx.Cfg})
			},
		},
		&cobra.Command{
			Use:   "get <key>",
			Short: "Get one setting",
			Args:  cobra.ExactArgs(1),
			ValidArgsFunction: func(cmd *cobra.Command, args []string, _ string) ([]string, cobra.ShellCompDirective) {
				return config.KnownKeys(), cobra.ShellCompDirectiveNoFileComp
			},
			RunE: func(cmd *cobra.Command, args []string) error {
				ctx, err := load(*g)
				if err != nil {
					return err
				}
				v, err := config.GetKey(ctx.Cfg, args[0])
				if err != nil {
					return fail("%v", err)
				}
				return ctx.out(cmd, v, map[string]any{"key": args[0], "value": v})
			},
		},
	)
	setCmd := &cobra.Command{
		Use:   "set <key> <value>",
		Short: "Set one setting",
		Args:  cobra.ExactArgs(2),
		ValidArgsFunction: func(cmd *cobra.Command, args []string, _ string) ([]string, cobra.ShellCompDirective) {
			if len(args) == 0 {
				return config.KnownKeys(), cobra.ShellCompDirectiveNoFileComp
			}
			return nil, cobra.ShellCompDirectiveNoFileComp
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, err := load(*g)
			if err != nil {
				return err
			}
			cfg := ctx.Cfg
			if err := config.SetKey(&cfg, args[0], args[1]); err != nil {
				return fail("%v", err)
			}
			if err := config.Save(ctx.Paths.ConfigFile(), cfg); err != nil {
				return fail("save config: %v", err)
			}
			note := ""
			if args[0] == "watcher_enabled" {
				note = " (read once at daemon start — run `cbm daemon stop` so the next session picks it up)"
			}
			return ctx.out(cmd, fmt.Sprintf("set %s=%s%s", args[0], args[1], note), map[string]any{"key": args[0]})
		},
	}
	resetCmd := &cobra.Command{
		Use:   "reset <key>",
		Short: "Reset one setting to default",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, err := load(*g)
			if err != nil {
				return err
			}
			def := config.Defaults()
			dv, err := config.GetKey(def, args[0])
			if err != nil {
				return fail("%v", err)
			}
			cfg := ctx.Cfg
			if err := config.SetKey(&cfg, args[0], dv); err != nil {
				return fail("%v", err)
			}
			if err := config.Save(ctx.Paths.ConfigFile(), cfg); err != nil {
				return fail("save config: %v", err)
			}
			return ctx.out(cmd, fmt.Sprintf("reset %s=%s", args[0], dv), nil)
		},
	}
	validateCmd := &cobra.Command{
		Use:   "validate",
		Short: "Validate config schema",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, err := load(*g)
			if err != nil {
				return err
			}
			if err := ctx.Cfg.Validate(); err != nil {
				return fail("invalid: %v", err)
			}
			return ctx.out(cmd, "config valid", nil)
		},
	}
	c.AddCommand(setCmd, resetCmd, validateCmd)
	return c
}

func cmdMigrate(g *Globals) *cobra.Command {
	var from, dryRun string
	var isDry bool
	c := &cobra.Command{
		Use:   "migrate",
		Short: "Move legacy ~/.tk / $TK_HOME / Library tree into XDG true-knowledge dirs",
		Example: `  tk migrate --dry-run
  tk migrate --from ~/.tk`,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, err := load(*g)
			if err != nil {
				return err
			}
			_ = dryRun
			cands := []string{from}
			if from == "" {
				home, _ := os.UserHomeDir()
				cands = []string{
					filepath.Join(home, ".tk"),
					os.Getenv("TK_HOME"),
					filepath.Join(home, "Library", "Application Support", "true-knowledge"),
				}
			}
			found := []string{}
			live := map[string]bool{
				ctx.Paths.Config: true, ctx.Paths.Data: true, ctx.Paths.Cache: true, ctx.Paths.State: true,
			}
			if tkHome := os.Getenv("TK_HOME"); tkHome != "" {
				if abs, err := filepath.Abs(tkHome); err == nil {
					live[abs] = true
				} else {
					live[tkHome] = true
				}
			}
			for _, d := range cands {
				if d == "" {
					continue
				}
				abs := d
				if a, err := filepath.Abs(d); err == nil {
					abs = a
				}
				if live[d] || live[abs] {
					continue // never migrate from the live home itself
				}
				if fi, err := os.Stat(d); err == nil && fi.IsDir() {
					found = append(found, d)
				}
			}
			if len(found) == 0 {
				return ctx.out(cmd, "nothing to migrate (no legacy dirs found)", map[string]any{"migrated": false})
			}
			if isDry {
				return ctx.out(cmd, "would migrate: "+strings.Join(found, ", "), map[string]any{"from": found, "dry_run": true})
			}
			// Marker-only MVP: copy config.json + tk.json if present, stamp MIGRATED.
			moved := []string{}
			for _, d := range found {
				for _, f := range []string{"config.json", "tk.json"} {
					src := filepath.Join(d, f)
					if _, err := os.Stat(src); err != nil {
						continue
					}
					raw, err := os.ReadFile(src)
					if err != nil {
						continue
					}
					dst := ctx.Paths.ConfigFile()
					if f == "tk.json" {
						dst = ctx.Paths.RegistryFile()
					}
					if _, err := os.Stat(dst); os.IsNotExist(err) {
						_ = os.WriteFile(dst, raw, 0o600)
						moved = append(moved, f+" from "+d)
					}
				}
			}
			_ = os.WriteFile(filepath.Join(ctx.Paths.Data, "MIGRATED"), []byte("migrated\n"), 0o600)
			return ctx.out(cmd, fmt.Sprintf("migrated %d file(s); marker written (re-run needs --force, not yet required)", len(moved)),
				map[string]any{"moved": moved})
		},
	}
	c.Flags().StringVar(&from, "from", "", "legacy dir (default: auto-detect ~/.tk, $TK_HOME, Library)")
	c.Flags().BoolVar(&isDry, "dry-run", false, "print without writing")
	return c
}

func cmdInstall(g *Globals) *cobra.Command {
	var check, update, dry bool
	var version string
	c := &cobra.Command{
		Use:   "install [backend...]",
		Short: "Install/update managed indexing backends (default: all)",
		Long: `Installs missing backends and updates stale ones to their pins.
Backends live in <cache>/bin (== CBM_CACHE_DIR/bin); checksums verified before any write.
Replacing a managed binary stops the daemon holding it first, and a daemon with
live sessions refuses to stop — close them, then retry. No flag overrides that.
Adding a backend is one entry in internal/backends — see AGENT_DOCS/history/DECISIONS.`,
		Example: `  tk install                  # install all missing backends
  tk install cbm --update     # (re)install cbm at its pin
  tk install --check          # report only, no network
  tk install cbm --version 0.12.0 --dry-run`,
		ValidArgsFunction: func(cmd *cobra.Command, args []string, _ string) ([]string, cobra.ShellCompDirective) {
			out := []string{}
			for _, b := range backends.All() {
				out = append(out, b.Name)
			}
			return out, cobra.ShellCompDirectiveNoFileComp
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, err := load(*g)
			if err != nil {
				return err
			}
			names := args
			if len(names) == 0 {
				for _, b := range backends.All() {
					names = append(names, b.Name)
				}
			}
			type row struct {
				Backend string `json:"backend"`
				Status  string `json:"status"`
				Detail  string `json:"detail,omitempty"`
			}
			rows := []row{}
			lines := []string{}
			changedPin := false
			failed := []string{}
			for _, name := range names {
				b, err := backends.ByName(name)
				if err != nil {
					return fail("%v", err)
				}
				pin := ctx.Cfg.CBMVersionPin
				if pin == "" {
					pin = config.DefaultCBMPin
				}
				if version != "" && len(names) == 1 {
					pin = version
				} else if version != "" {
					return fail("--version only works with a single backend")
				}
				st := installer.Inspect(cmd.Context(), ctx.Paths.Cache, b, pin, backends.HostGOOS())
				switch {
				case check || dry:
					plan, perr := installer.ResolvePlan(ctx.Paths.Cache, b, pin, backends.HostGOOS(), backends.HostGOARCH())
					detail := plan.ArchiveURL
					if perr != nil {
						detail = perr.Error()
					}
					state := "up-to-date"
					if st.NeedsInstall {
						state = "would-install"
					}
					if dry {
						state = "dry-run"
					}
					rows = append(rows, row{name, state, detail})
					lines = append(lines, fmt.Sprintf("%-8s %-12s pin %s  %s", name, state, pin, detail))
				case st.UpToDate && !update:
					rows = append(rows, row{name, "up-to-date", st.Path})
					lines = append(lines, fmt.Sprintf("%-8s up-to-date %s (%s)", name, st.InstalledVersion, displayPath(st)))
				default:
					// Only a real replacement stops a daemon. --check, --dry-run
					// and an up-to-date no-op never reach this branch, and a
					// first install has no daemon to stop.
					//
					// The resident is quiesced for the same reason and in the
					// same branch, because it too holds the old image open. It
					// is a separate call rather than part of the daemon one:
					// they are different processes with different failure
					// modes, and the resident's socket must survive the swap
					// while the daemon's does not.
					// Two separate results, deliberately not one. They describe
					// two different processes with two different recovery paths,
					// and folding them into a single value loses one of them:
					// an install with a daemon but no resident would find the
					// resident's empty result had overwritten the daemon's, and
					// silently leave the daemon stopped.
					qd, qerr := ctx.quiesceDaemon(cmd.Context(), st.InCache)
					var qr Quiesce
					if qerr == nil {
						qr, qerr = ctx.quiesceResident()
					}
					if qerr != nil {
						failed = append(failed, name)
						rows = append(rows, row{name, "failed", qerr.Error()})
						lines = append(lines, fmt.Sprintf("%-8s FAILED: %v", name, qerr))
						continue
					}
					if qd.Was {
						lines = append(lines, fmt.Sprintf("%-8s %s", name, qd.Note()))
					}
					if qr.Was {
						lines = append(lines, fmt.Sprintf("%-8s %s", name, qr.Note()))
					}
					// The engine is released only for the CBM binary. Quiescing
					// around an unrelated backend's install would idle a warm
					// resident for nothing.
					engineReleased := qr.Was && name == "cbm"
					plan, err := installer.Install(cmd.Context(), ctx.Paths.Cache, b, pin, backends.HostGOOS(), backends.HostGOARCH())
					if err != nil {
						// A backend that did not install is a failed command, not a
						// warning. This used to print FAILED and continue, so
						// `tk install` exited 0 with nothing at the target and the
						// caller went looking for the problem somewhere else.
						failed = append(failed, name)
						rows = append(rows, row{name, "failed", err.Error()})
						lines = append(lines, fmt.Sprintf("%-8s FAILED: %v", name, err))
						// The resident is left quiesced here, which is a safe
						// state but not a running one. Resuming costs a spawn and
						// returns the machine to serving, so it is done even on
						// the failure path.
						if engineReleased {
							if rerr := ctx.resumeResident(); rerr != nil {
								lines = append(lines, fmt.Sprintf("%-8s note: resident left quiesced: %v", name, rerr))
							}
						}
						continue
					}
					// A daemon that was running is brought back on the new binary.
					// The old image survives a rename, so without this the new
					// build would not be in use until the daemon drained on its
					// own. A failure to resume is reported, not fatal: the install
					// itself succeeded and the binary is on disk.
					note := ""
					if qd.Was {
						if rerr := ctx.resumeDaemon(cmd.Context()); rerr != nil {
							note = fmt.Sprintf(" (daemon not resumed: %v)", rerr)
						} else {
							note = " (daemon restarted)"
						}
					}
					// The resident is resumed on the new binary. Its socket was
					// never unbound, so this is the only step that makes the
					// install visible to the reads that were falling back to
					// spawns while it was quiesced.
					if engineReleased {
						if rerr := ctx.resumeResident(); rerr != nil {
							note += fmt.Sprintf(" (resident not resumed: %v)", rerr)
						} else {
							note += " (resident engine replaced)"
						}
					}
					rows = append(rows, row{name, "installed", plan.Dest})
					lines = append(lines, fmt.Sprintf("%-8s installed %s -> %s%s", name, pin, plan.Dest, note))
				}
				if version != "" && pin == version {
					ctx.Cfg.CBMVersionPin = version
					changedPin = true
				}
			}
			if changedPin {
				if err := config.Save(ctx.Paths.ConfigFile(), ctx.Cfg); err != nil {
					return fail("save pin: %v", err)
				}
				lines = append(lines, fmt.Sprintf("pinned %s=%s in config", names[0], version))
			}
			text := strings.Join(lines, "\n")
			if text == "" {
				text = "nothing to do"
			}
			fields := map[string]any{"backends": rows}
			if len(failed) > 0 {
				return ctx.outFailed(cmd, text, fields,
					fail("%s: install failed; see the FAILED line above", strings.Join(failed, ", ")))
			}
			return ctx.out(cmd, text, fields)
		},
	}
	c.Flags().BoolVar(&check, "check", false, "report status only, no network")
	c.Flags().BoolVar(&update, "update", false, "reinstall even when up-to-date (downgrade-safe)")
	c.Flags().BoolVar(&dry, "dry-run", false, "print plan URLs without downloading")
	c.Flags().StringVar(&version, "version", "", "override pin X.Y.Z for this run (persists to config)")
	return c
}

// quiesceDaemon stops the daemon holding the managed binary, once, and reports
// what it found so the caller can bring it back on the new image.
//
// It stops only when the install will actually replace a binary. A daemon on a
// live image is the reason a rename is unsafe: on Windows the .exe cannot
// replace itself, and on Linux the running process keeps the old inode.
//
// A refusal is a failed install. `cbm daemon stop` is refuse-if-busy, not force:
// it names the committed clients and stops nothing. tk does not swap past that
// refusal, because the new binary would then be refused admission by the very
// daemon it was installed under — every later command would fail a build
// comparison. The fix is the one the daemon itself prints: close the sessions.
//
// The stop is scoped to tk's own rendezvous (CBM_RUNTIME_DIR is
// <state>/rendezvous), so this can never stop an account-wide daemon a consumer
// laptop is running. Stopping is safe across a version boundary: CBM handles
// control-plane requests ahead of its build-identity check, so the installed
// binary can stop a daemon that is an older build.
func (c *Ctx) quiesceDaemon(ctx context.Context, replacing bool) (Quiesce, error) {
	if !replacing || c.Run == nil || !c.CBMOK {
		return Quiesce{}, nil // nothing installed, or nothing to replace
	}
	out, _ := c.cbmDaemon(ctx, "daemon", "status")
	if !daemonActive(out) {
		return Quiesce{}, nil
	}
	q := Quiesce{Was: true, Pid: daemonPid(out)}
	stopped, err := c.cbmDaemon(ctx, "daemon", "stop")
	if err != nil {
		return Quiesce{}, busyDaemon(stopped, err)
	}
	return q, nil
}

// Quiesce is what a stop found. Was is the only thing that decides whether to
// start a daemon again; Pid is reported so the operator can see exactly which
// process tk retired.
type Quiesce struct {
	Was bool
	Pid string
	// Resident distinguishes the two things tk stops. The wording matters: a
	// daemon is retired and replaced by a new process, a resident is asked to
	// release one child and keeps serving throughout. Printing them the same
	// way would make an operator think their resident had been stopped.
	Resident bool
}

// Note is the line tk prints when it stops something. Announcing the pid is not
// decoration: a daemon stop is the one action tk takes on a process the user did
// not name, so it has to be attributable.
func (q Quiesce) Note() string {
	if q.Resident {
		if q.Pid == "" {
			return "asked the resident to release its engine before replacing its binary"
		}
		return fmt.Sprintf("asked the resident (pid %s) to release its engine before replacing its binary", q.Pid)
	}
	if q.Pid == "" {
		return "stopped the tk daemon before replacing its binary"
	}
	return fmt.Sprintf("stopped the tk daemon (pid %s) before replacing its binary", q.Pid)
}

// resumeDaemon starts a fresh daemon on the newly installed binary. Only called
// when one was running before, so a first install never creates a permanent
// daemon that nothing asked for.
func (c *Ctx) resumeDaemon(ctx context.Context) error {
	_, err := c.cbmDaemon(ctx, "daemon", "start")
	return err
}

// quiesceResident asks a running resident to release its warm child so the
// binary underneath it can be replaced, and reports what it found.
//
// The resident is NOT killed, and the difference is the whole design. A killed
// resident takes its socket with it, so every client that redials during an
// upgrade either blocks on a dead path or silently spawns; and it discards the
// warm state the user started it for. Quiescing keeps the endpoint bound and
// answering — with ErrQuiesced, which the read path already treats as "not
// served, fall back" — for exactly as long as the install takes.
//
// Not a failure when there is no resident: the resident is opt-in, so most
// installs have nothing to quiesce and must not be slowed or broken by it.
// A refusal from a resident that IS running, though, is fatal to the install.
// Its child holds the same inode the new binary wants, and continuing would
// install a file the resident could never pick up.
func (c *Ctx) quiesceResident() (Quiesce, error) {
	if c.Run == nil || !c.CBMOK {
		return Quiesce{}, nil
	}
	cli := &resident.Client{Addr: c.Paths.ResidentSocket()}
	if !cli.Available() {
		return Quiesce{}, nil
	}
	st, err := cli.Control(resident.ActionQuiesce)
	if err != nil {
		return Quiesce{}, fmt.Errorf("resident would not release its engine: %w", err)
	}
	return Quiesce{Was: true, Pid: strconv.Itoa(st.PID), Resident: true}, nil
}

// resumeResident starts a fresh engine on the newly installed binary. The
// resident process is untouched, so the socket never went away and no client
// ever saw a gap.
//
// A failure here is reported, never fatal: the resident is already quiesced and
// correctly refusing work, which is a safe state to leave a machine in. Failing
// the install would be wrong, because the binary did land — and refusing to
// resume would be worse, so a resident that cannot be resumed is still asked
// once more on the next install.
func (c *Ctx) resumeResident() error {
	st, err := (&resident.Client{Addr: c.Paths.ResidentSocket()}).Control(resident.ActionResume)
	if err != nil {
		return err
	}
	if st.EnginePID == 0 {
		return fmt.Errorf("resident resumed without an engine")
	}
	return nil
}

// daemonActive reads `daemon status` output. It exits nonzero when nothing is
// running and says "daemon: not running", which cbmDaemon preserves, so the
// prose is the signal rather than the exit code.
func daemonActive(out string) bool {
	return strings.Contains(out, "daemon: active")
}

var pidLine = regexp.MustCompile(`(?m)^\s*pid:\s*(\d+)`)

// daemonPid pulls the pid out of `daemon status`, which prints it on its own
// indented line:
//
//	daemon: active (permanent)
//	  pid: 13482
//
// Best-effort: an output shape tk does not recognise still yields a usable
// Quiesce with an empty Pid, because refusing to stop on an unparsed status
// would be worse than stopping without naming the pid.
func daemonPid(out string) string {
	if m := pidLine.FindStringSubmatch(out); m != nil {
		return m[1]
	}
	return ""
}

// busyDaemon turns a refused stop into an actionable error carrying the client
// pids CBM printed. Those pids are the whole fix.
func busyDaemon(out string, err error) error {
	detail := strings.TrimSpace(out)
	if detail == "" {
		detail = err.Error()
	}
	return fail("the CBM daemon has live sessions and refused to stop; tk did not replace the binary it holds.\n%s\nClose those sessions, then re-run `tk install` — tk cannot override the refusal, and swapping past it would leave a binary the daemon refuses to admit.",
		indent(detail))
}

// indent shifts a multi-line daemon report under the error's first line.
func indent(s string) string {
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		if i == 0 {
			continue
		}
		lines[i] = "  " + l
	}
	return strings.Join(lines, "\n")
}

func displayPath(st installer.Status) string {
	if st.Path != "" {
		return st.Path
	}
	return "(missing)"
}

func cmdMCPInstall(g *Globals) *cobra.Command {
	var client, profile string
	var dry bool
	c := &cobra.Command{
		Use:   "mcp-install",
		Short: "Point an agent client at `tk mcp` (Pi slipped this build)",
		Example: `  tk mcp-install --client opencode --dry-run
  tk mcp-install --client claude --tool-profile memory`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if profile != "" && !slices.Contains(config.ValidProfiles(), profile) {
				return fmt.Errorf("unknown --tool-profile %q (want %s)", profile, strings.Join(config.ValidProfiles(), "|"))
			}
			ctx, err := load(*g)
			if err != nil {
				return err
			}
			exe, err := os.Executable()
			if err != nil {
				exe = "tk"
			}
			text, fields, err := mcpSnippet(exe, client, profile, dry)
			if err != nil {
				return err
			}
			return ctx.out(cmd, text, fields)
		},
	}
	c.Flags().StringVar(&client, "client", "", "pi|opencode|claude|codex (required)")
	_ = c.MarkFlagRequired("client")
	c.Flags().StringVar(&profile, "tool-profile", "", "tool surface (scout|analysis|minimal|memory); emitted as TK_MCP_PROFILE env, default scout")
	c.Flags().BoolVar(&dry, "dry-run", false, "print without writing")
	_ = c.RegisterFlagCompletionFunc("client", func(cmd *cobra.Command, args []string, _ string) ([]string, cobra.ShellCompDirective) {
		return []string{"pi", "opencode", "claude", "codex"}, cobra.ShellCompDirectiveNoFileComp
	})
	_ = c.RegisterFlagCompletionFunc("tool-profile", func(cmd *cobra.Command, args []string, _ string) ([]string, cobra.ShellCompDirective) {
		return config.ValidProfiles(), cobra.ShellCompDirectiveNoFileComp
	})
	return c
}
