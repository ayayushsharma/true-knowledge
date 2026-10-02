package cli

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/ayayushsharma/true-knowledge/internal/resident"
	"github.com/spf13/cobra"
)

// detachEnv tells a re-executed tk that it is the background half of
// `tk mcp --detach`. It is an env var rather than a flag because the flag
// would be a second way to ask for the same thing, and a user who found it
// would reasonably expect it to print output that nobody is there to read.
const detachEnv = "TK_RESIDENT_CHILD"

// detachTimeout bounds the wait for a freshly started resident to accept a
// connection.
//
// The parent cannot simply return after spawning: a caller that runs
// `tk mcp --detach && tk find` would otherwise race the socket and silently
// take the spawn path, and the resident would look broken while being fine.
// So the parent waits for a real connection before reporting success.
const detachTimeout = 15 * time.Second

// cmdResident builds the hidden background half. It is not a documented verb:
// the surface is `tk mcp --detach`, and this exists only so that verb has
// something to exec.
//
// Hidden rather than refused, because it does real work and refusing it would
// be a lie about what the binary can do. Hiding keeps it out of `tk --help` and
// out of `tk __complete` alike — Cobra omits a Hidden command from completion
// candidates, which is the whole effect and the reason not to expect a
// discoverable path to it here. The pid file is the documented way to reach a
// resident; this verb is how tk starts one.
func cmdResident(g *Globals) *cobra.Command {
	return &cobra.Command{
		Use:    "resident",
		Short:  "Run the CBM resident in the foreground (started by `tk mcp --detach`)",
		Hidden: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, err := load(*g)
			if err != nil {
				return err
			}
			if !ctx.CBMOK {
				return fail("codebase-memory-mcp not installed; run `tk install` first")
			}
			srv := &resident.Server{
				Addr:    ctx.Paths.ResidentSocket(),
				PidFile: ctx.Paths.ResidentPid(),
				LogPath: ctx.Paths.ResidentLog(),
				Start:   resident.StartEngine(ctx.Run),
			}
			// SIGINT and SIGTERM are the ordinary ways an operator stops a
			// resident. Without a handler the process would die without
			// closing its child, leaving an engine holding the store open.
			sig := make(chan os.Signal, 1)
			signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
			ctxRun, cancel := context.WithCancel(cmd.Context())
			defer cancel()
			go func() {
				<-sig
				cancel()
			}()
			return srv.Serve(ctxRun)
		},
	}
}

// runDetach implements `tk mcp --detach`: start the resident, wait until it
// really serves, and return.
//
// The wait is the part that makes the flag usable. Without it, a caller
// running `tk mcp --detach && tk find` would race the socket, silently take
// the spawn path, and conclude the resident is broken while it is merely not
// listening yet. Reporting success before accept(2) works would be a lie the
// next command disproves.
func runDetach(cmd *cobra.Command, c *Ctx) error {
	if !c.CBMOK {
		return fail("codebase-memory-mcp not installed; run `tk install` first")
	}
	addr := c.Paths.ResidentSocket()

	// Starting a second resident is not an error to paper over: the first one
	// is already warm, which is the entire point, and replacing it would drop
	// the state the user paid for.
	if (&resident.Client{Addr: addr}).Available() {
		return c.out(cmd, fmt.Sprintf("resident already running on %s", addr), map[string]any{
			"socket": addr, "pid": residentPid(c), "started": false,
		})
	}
	if err := startDetached(filepath.Dir(c.Paths.State)); err != nil {
		return err
	}
	if err := waitReady(addr, detachTimeout); err != nil {
		return fail("%v (nothing is listening; check %s)", err, c.Paths.ResidentLog())
	}
	return c.out(cmd, fmt.Sprintf("resident listening on %s (pid %d)", addr, residentPid(c)), map[string]any{
		"socket": addr, "pid": residentPid(c), "started": true,
	})
}

// residentPid reads the resident's pid for reporting. Zero means the pid file
// has not landed yet, which is a reporting gap and never a reason to fail a
// command whose socket is already answering.
func residentPid(c *Ctx) int {
	raw, err := os.ReadFile(c.Paths.ResidentPid())
	if err != nil {
		return 0
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil {
		return 0
	}
	return pid
}

// startDetached launches the background resident.
//
// It re-executes this same binary rather than forking, because Go cannot fork
// safely without re-executing anyway, and a re-exec is the one sequence whose
// behaviour is identical on Linux and macOS. The child gets its own process
// group so a Ctrl-C in the foreground shell does not take the resident down
// with it — the whole point of detaching is that it outlives the terminal.
//
// The resolved home is passed on the command line rather than left to be
// re-derived, and that is not belt-and-braces. TK_HOME alone does not survive:
// a child that re-derived its home could land on a different one, and the
// symptom is the worst kind — the socket and pid file appear, `tk mcp --detach`
// reports success, and every later read quietly spawns a fresh engine for the
// home the user is not using. Passing it explicitly also pins the child against
// an environment that changes between the detach and the next read.
func startDetached(home string) error {
	self, err := os.Executable()
	if err != nil {
		return fail("cannot locate the tk binary: %v", err)
	}
	cmd := exec.Command(self, "--home", home, "resident")
	cmd.Env = append(os.Environ(), detachEnv+"=1")
	// Detach from the controlling terminal: a new session, and every standard
	// stream pointed at /dev/null. Inheriting this shell's stdout would mean
	// the resident holds a pipe open long after tk returned, and any caller
	// waiting on our output would hang until the resident died.
	devnull, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		return fail("cannot open %s: %v", os.DevNull, err)
	}
	defer devnull.Close()
	cmd.Stdin, cmd.Stdout, cmd.Stderr = devnull, devnull, devnull
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return fail("cannot start the resident: %v", err)
	}
	// Deliberately not waited on: the child is the resident now, and reaping
	// it here would make tk the parent of a process meant to outlive it.
	go func() { _ = cmd.Wait() }()
	return nil
}

// waitReady blocks until the resident is serving reads, or the deadline passes.
//
// The probe asks for status, not just a dial. net.Listen binds AND listens, so
// a connect succeeds the moment it returns — before Serve has reached its
// accept loop, and therefore before an engine exists. A dial-based readiness
// check reports a resident that is bound but has nothing to answer with, and
// the failure lands on the user's next read instead of here.
//
// Status is the honest signal: it can only be answered from inside the accept
// loop, and it reports an engine pid, so a true answer means a read would
// actually be served. A resident whose engine failed to start exits instead,
// which shows up here as a timeout naming the lifecycle log.
func waitReady(addr string, d time.Duration) error {
	deadline := time.Now().Add(d)
	cli := &resident.Client{Addr: addr}
	for time.Now().Before(deadline) {
		if cli.Running() {
			return nil
		}
		time.Sleep(25 * time.Millisecond)
	}
	return fmt.Errorf("the resident did not start serving on %s within %s", addr, d)
}

// residentRunning reports whether a resident is serving this home.
func (c *Ctx) residentRunning() bool {
	return (&resident.Client{Addr: c.Paths.ResidentSocket()}).Available()
}
