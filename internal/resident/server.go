package resident

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ayayushsharma/true-knowledge/internal/cbmexec"
)

// Engine is the part of the child the server needs. It is an interface so the
// server can be tested against a stub, and so the install path can swap the
// implementation without the listener knowing.
type Engine interface {
	CallTool(tool string, args map[string]any) (json.RawMessage, error)
	CallTimeout(tool string, args map[string]any, d time.Duration) (json.RawMessage, error)
	Pid() int
	Close()
}

// defaultTimeout bounds one request when the caller does not set one. It is
// generous on purpose: the engine is answering a local SQLite query, and a
// timeout that fires on a slow machine converts a working resident into a
// resident the client has stopped trusting.
const defaultTimeout = 120 * time.Second

// Server is a listening resident: a Unix socket in front of one CBM child.
type Server struct {
	// Addr is the socket path. PidFile is written after the listener is up,
	// so a pid file never advertises a socket nobody can dial.
	Addr    string
	PidFile string
	LogPath string

	// Start opens the engine. It is a field rather than a constructor argument
	// because the install path replaces the engine underneath a live server,
	// and the swap needs to happen without rebinding the socket: a client that
	// re-dials during an upgrade must find the same endpoint, not a gap.
	Start func() (Engine, error)

	mu       sync.Mutex
	engine   Engine
	ln       net.Listener
	quiesced bool
	closed   bool

	// one serializes requests. The engine is driven one at a time, so a second
	// caller waits here rather than interleaving bytes on the child's pipes.
	one sync.Mutex

	// readDeadline bounds how long one connection may sit without sending a
	// request. Zero means defaultConnReadDeadline. It is a field only so a test
	// can prove the hangup happens without waiting 30 seconds; nothing outside
	// this package sets it.
	readDeadline time.Duration
}

// defaultConnReadDeadline is how long a connection may sit without sending a
// request before the resident hangs up on it. Since accepting moved out of the
// accept loop, a silent connection no longer blocks anyone, so this bounds a
// goroutine rather than preventing an outage.
const defaultConnReadDeadline = 30 * time.Second

// connReadDeadline resolves the zero-value default.
func (s *Server) connReadDeadline() time.Duration {
	if s.readDeadline > 0 {
		return s.readDeadline
	}
	return defaultConnReadDeadline
}

// ErrQuiesced is returned to a client that arrives while the resident is
// being upgraded. It is an error and not a queue entry on purpose: the client
// has a one-shot path, and a queued read behind a 30-second install would be
// slower than the spawn it was trying to avoid.
var ErrQuiesced = errors.New("resident: quiesced for engine swap")

// Serve listens until ctx is done or Close is called.
//
// One connection carries one request and is then closed. There is no keep-alive
// and no per-connection loop: a client that wants two answers dials twice, and
// a half-dead connection cannot desynchronise the engine's reply stream.
func (s *Server) Serve(ctx context.Context) error {
	ln, err := s.listen()
	if err != nil {
		s.logStartFailure(err)
		return err
	}
	s.mu.Lock()
	s.ln = ln
	s.mu.Unlock()
	defer ln.Close()

	if s.PidFile != "" {
		if err := writePid(s.PidFile, os.Getpid()); err != nil {
			return fmt.Errorf("resident: write pid: %w", err)
		}
		defer os.Remove(s.PidFile)
	}
	if err := s.ensureEngine(); err != nil {
		s.logStartFailure(err)
		return err
	}
	s.log("resident listening on " + s.Addr)

	go func() {
		<-ctx.Done()
		s.Close()
	}()

	for {
		conn, err := ln.Accept()
		if err != nil {
			s.mu.Lock()
			closed := s.closed
			s.mu.Unlock()
			if closed || errors.Is(err, net.ErrClosed) {
				return nil
			}
			return err
		}
		// Per-connection, but still one request at a time. The serialization
		// that matters is the ENGINE's: s.one in serveConn is what keeps the
		// CBM child driven one call at a time, and it holds for any number of
		// goroutines.
		//
		// Accepting inline instead made the read half of the protocol as
		// fragile as the write half. Measured: one client that connects and
		// never writes delayed the NEXT unrelated read by 19.5s — the caller's
		// whole fallback budget — and tk silently answered from a fresh
		// one-shot engine, so a warm resident was destroyed by a peer that
		// said nothing. tk causes this itself: CallTimeout abandons a slow
		// request without closing the connection, because the child must be
		// left alive. So "clients are well-behaved" was never true here.
		//
		// The read deadline below still bounds a silent connection's goroutine,
		// so the cost is bounded per connection and not per accept loop.
		go s.serveConn(conn)
	}
}

// serveConn answers exactly one request then closes. It runs in its own
// goroutine, so it must be safe to call concurrently with itself.
func (s *Server) serveConn(conn net.Conn) {
	defer conn.Close()
	// A read deadline is not optional. A client that connects and never writes
	// would otherwise hold a goroutine for the life of the resident, and the
	// process would grow one per silent peer. It no longer blocks anyone, so
	// this bounds a leak instead of preventing an outage — which is the whole
	// reason accepting moved out of the loop.
	_ = conn.SetReadDeadline(time.Now().Add(s.connReadDeadline()))
	line, err := bufio.NewReader(io.LimitReader(conn, 8<<20)).ReadBytes('\n')
	if err != nil && len(line) == 0 {
		return
	}
	var req Request
	if err := json.Unmarshal(line, &req); err != nil {
		_ = writeReply(conn, Reply{Error: "resident: malformed request"})
		return
	}
	// The action check precedes the tool check, and that order is the whole
	// point. A control request names no tool — `{"action":"quiesce"}` is
	// complete on its own — so requiring a tool first would reject every
	// control message as "no tool named" and the install path could never
	// reach the resident it is trying to swap.
	if req.Action != "" {
		s.serveAction(conn, req)
		return
	}
	if req.Tool == "" {
		_ = writeReply(conn, Reply{Error: "resident: no tool named"})
		return
	}

	s.mu.Lock()
	quiesced, closed := s.quiesced, s.closed
	eng := s.engine
	s.mu.Unlock()
	if closed {
		_ = writeReply(conn, Reply{Error: "resident: closed"})
		return
	}
	if quiesced {
		// Answered, not dropped: a dropped connection looks like a hung
		// resident, and the client would retry against a socket that is
		// listening but has nothing to say.
		_ = writeReply(conn, Reply{Error: ErrQuiesced.Error()})
		return
	}
	if eng == nil {
		_ = writeReply(conn, Reply{Error: "resident: no engine"})
		return
	}

	args := req.Args
	if req.Structured {
		// The resident does not own the dialect: the caller's Structured
		// flag becomes the same argument the one-shot path sends, so both
		// paths ask the engine the identical question.
		args = withFormat(args, "json")
	}
	timeout := time.Duration(req.TimeoutMS) * time.Millisecond
	if timeout <= 0 {
		timeout = defaultTimeout
	}

	s.one.Lock()
	res, err := eng.CallTimeout(req.Tool, args, timeout)
	s.one.Unlock()

	if err != nil {
		_ = writeReply(conn, Reply{Error: err.Error()})
		return
	}
	_ = writeReply(conn, Reply{Result: res})
}

// serveAction runs a control request. It bypasses the quiesce check on
// purpose: resume is exactly the call a quiesced resident has to accept, and
// status has to work in the same window so a client can see the difference.
//
// Every action is answered, including a rejected one. A silent control request
// would leave an installer waiting on a reply that is never coming, and it
// would wait through a swap it is not supposed to interrupt.
func (s *Server) serveAction(conn io.Writer, req Request) {
	switch req.Action {
	case ActionStatus:
		s.mu.Lock()
		eng, quiesced := s.engine, s.quiesced
		s.mu.Unlock()
		st := &Status{Quiesced: quiesced, PID: os.Getpid()}
		if eng != nil {
			st.EnginePID = eng.Pid()
		}
		_ = writeReply(conn, Reply{Status: st})
	case ActionQuiesce:
		s.Quiesce()
		s.log("resident quiesced for engine swap")
		s.mu.Lock()
		st := &Status{Quiesced: true, PID: os.Getpid()}
		s.mu.Unlock()
		_ = writeReply(conn, Reply{Status: st})
	case ActionResume:
		if err := s.Resume(); err != nil {
			_ = writeReply(conn, Reply{Error: err.Error()})
			return
		}
		s.log("resident resumed with a new engine")
		s.mu.Lock()
		eng, quiesced := s.engine, s.quiesced
		s.mu.Unlock()
		st := &Status{Quiesced: quiesced, PID: os.Getpid()}
		if eng != nil {
			st.EnginePID = eng.Pid()
		}
		_ = writeReply(conn, Reply{Status: st})
	default:
		_ = writeReply(conn, Reply{Error: "resident: unknown action " + req.Action})
	}
}

// logStartFailure records why the resident never came up.
//
// Without it the only evidence of a failed start is the reason in a process
// that has already exited. `tk mcp --detach` waits for a socket, and on
// failure it can only say "nothing is listening" and point at this log — which
// is a dead end if the log is empty. The common causes (a TK_HOME too long for
// sun_path, a binary that will not exec) are exactly the ones a user needs
// spelled out, and they are all knowable only here.
func (s *Server) logStartFailure(err error) {
	s.log("resident failed to start: " + oneLine(err.Error()))
}

// oneLine keeps a log record to one line. The log is line-delimited JSON, so a
// multi-line reason would break the format for every record after it — and a
// broken log is worse than a truncated message.
func oneLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > 300 {
		s = s[:300] + "..."
	}
	return s
}

func writeReply(w io.Writer, r Reply) error {
	raw, err := Encode(r)
	if err != nil {
		return err
	}
	_, err = w.Write(raw)
	return err
}

// withFormat sets format:"json" unless the caller already chose one, matching
// cbmexec.withFormat so the resident and the spawn send the same argument.
func withFormat(payload map[string]any, format string) map[string]any {
	if payload == nil {
		return map[string]any{"format": format}
	}
	if _, ok := payload["format"]; ok {
		return payload
	}
	out := make(map[string]any, len(payload)+1)
	for k, v := range payload {
		out[k] = v
	}
	out["format"] = format
	return out
}

// Quiesce stops answering and releases the child so it can be replaced.
//
// In-flight work is finished first. Swapping the engine out from under a
// running query would lose that query's answer for no benefit: the install
// that triggered the swap is already the slow part, and the caller has a
// one-shot fallback for the seconds it takes.
func (s *Server) Quiesce() {
	s.mu.Lock()
	s.quiesced = true
	eng := s.engine
	s.mu.Unlock()

	// Take the serial slot: once it is held, no request is in flight.
	s.one.Lock()
	s.one.Unlock()

	if eng != nil {
		eng.Close()
	}
	s.mu.Lock()
	s.engine = nil
	s.mu.Unlock()
}

// Resume starts a fresh engine and answers again. The socket is never closed:
// a client that re-dials during the swap finds the same endpoint, so the
// upgrade is invisible to callers.
//
// The quiesce flag is cleared only once the engine is attached, not before.
// Clearing it first would open a window where the resident claims to be
// serving but has no engine, and a client arriving in that window would be
// told "no engine" — which reads as a broken resident rather than as a swap in
// progress. Order is the whole fix: stay quiesced until there is something to
// be quiesced about.
func (s *Server) Resume() error {
	if err := s.ensureEngine(); err != nil {
		// Left quiesced on purpose. A failed start is not a serving resident,
		// and claiming otherwise would send the next client to a socket that
		// answers "no engine" instead of letting it fall back to a spawn.
		return err
	}
	s.mu.Lock()
	s.quiesced = false
	s.mu.Unlock()
	return nil
}

func (s *Server) ensureEngine() error {
	if s.Start == nil {
		return errors.New("resident: no engine starter")
	}
	eng, err := s.Start()
	if err != nil {
		return fmt.Errorf("resident: start engine: %w", err)
	}
	s.mu.Lock()
	s.engine = eng
	s.mu.Unlock()
	s.log("resident engine pid=" + strconv.Itoa(eng.Pid()))
	return nil
}

// Close stops the resident and its child.
func (s *Server) Close() {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	ln, eng := s.ln, s.engine
	s.engine = nil
	s.mu.Unlock()

	if ln != nil {
		_ = ln.Close()
	}
	if eng != nil {
		eng.Close()
	}
	if s.PidFile != "" {
		_ = os.Remove(s.PidFile)
	}
}

// maxAddrLen is the longest socket path that binds on every platform tk ships.
//
// sockaddr_un.sun_path is a fixed buffer: 104 bytes on macOS, 108 on Linux, both
// counting the NUL. 103 is the smaller of the two usable lengths, so a path
// that passes here works on both. The check exists because the kernel's own
// answer is `bind: invalid argument`, which names neither the cause nor the
// fix — and the cause is a long TK_HOME, which is exactly the kind of thing a
// user needs told plainly.
const maxAddrLen = 103

// listen binds the socket, taking over a stale one.
//
// A leftover socket file from a crashed resident would otherwise make every
// later start fail with EADDRINUSE, and the documented recovery would be a
// manual rm. Proving the file is stale first is the whole trick: dial it, and
// if nobody answers, the previous resident is gone and the path is ours to
// reuse. A live resident is never displaced — that would break every client
// mid-call.
func (s *Server) listen() (net.Listener, error) {
	if len(s.Addr) > maxAddrLen {
		return nil, fmt.Errorf("resident: socket path is %d bytes, over the %d a unix socket can hold; "+
			"use a shorter TK_HOME (set --home or TK_HOME to a path under ~100 characters)", len(s.Addr), maxAddrLen)
	}
	ln, err := net.Listen("unix", s.Addr)
	if err == nil {
		return ln, nil
	}
	if !errors.Is(err, errAddrInUse) {
		return nil, fmt.Errorf("resident: listen %s: %w", s.Addr, err)
	}
	if alive(s.Addr) {
		return nil, fmt.Errorf("resident: already running on %s", s.Addr)
	}
	// The socket file outlived its process. Removing it is safe only because
	// alive() just proved nobody is serving it.
	if err := os.Remove(s.Addr); err != nil {
		return nil, fmt.Errorf("resident: remove stale socket: %w", err)
	}
	ln, err = net.Listen("unix", s.Addr)
	if err != nil {
		return nil, fmt.Errorf("resident: listen %s: %w", s.Addr, err)
	}
	return ln, nil
}

// alive reports whether something is serving the socket. A successful dial
// means a resident is up; the connection is closed immediately, and a resident
// that sees a dial with no request simply drops it.
func alive(addr string) bool {
	conn, err := net.DialTimeout("unix", addr, 500*time.Millisecond)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

func writePid(path string, pid int) error {
	return os.WriteFile(path, []byte(strconv.Itoa(pid)+"\n"), 0o600)
}

// PidAlive reports whether a pid file names a live process.
//
// It is used by `tk status` to tell a warm resident from a stale pid file, and
// by the tests that assert the file is cleaned up. Signal 0 is the check that
// asks "does this process exist" without touching it.
func PidAlive(path string) bool {
	raw, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil || pid <= 0 {
		return false
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return pidAlive(proc)
}

// log appends one line to tk.log's sibling resident log. Best-effort: a
// resident that cannot write its own log must still serve requests.
func (s *Server) log(msg string) {
	if s.LogPath == "" {
		return
	}
	raw, err := Encode(map[string]any{
		"ts":      time.Now().UTC().Format(time.RFC3339Nano),
		"backend": "resident",
		"op":      "lifecycle",
		"message": msg,
	})
	if err != nil {
		return
	}
	f, err := os.OpenFile(s.LogPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = f.Write(raw)
}

// childEngine adapts *cbmexec.Child to Engine. It exists so this package does
// not depend on cbmexec's concrete type, which keeps the swap seam honest.
type childEngine struct{ *cbmexec.Child }

func (c childEngine) CallTool(tool string, args map[string]any) (json.RawMessage, error) {
	return c.Child.CallTool(tool, args)
}

func (c childEngine) CallTimeout(tool string, args map[string]any, d time.Duration) (json.RawMessage, error) {
	return c.Child.CallTimeout(tool, args, d)
}

// StartEngine is the Engine factory for a real Runner. tk mcp --detach passes
// this; the tests pass their own.
func StartEngine(r *cbmexec.Runner) func() (Engine, error) {
	return func() (Engine, error) {
		c, err := r.StartChild()
		if err != nil {
			return nil, err
		}
		return childEngine{c}, nil
	}
}
