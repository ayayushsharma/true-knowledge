package cbmexec

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"sync"
	"time"
)

// Child is one long-lived `cbm` MCP server, spoken to over its stdio pipes.
//
// It exists because the per-spawn cost, not the query, is what made graph
// reads slow: a `cli --json` spawn pays process start, a store open and a
// version-cohort lock admission before the engine reads a byte, and that
// whole cost was charged again on every call. A child pays it once.
//
// The child speaks the same MCP dialect tk already proxies, and returns the
// `result` object of the engine's envelope verbatim. That is deliberate: the
// caller hands it to the existing parseEnvelope, so a resident answer is
// parsed by exactly the code that parses a one-shot answer. Parity is
// therefore structural rather than something a test has to keep re-proving
// whenever either side changes.
type Child struct {
	cmd  *exec.Cmd
	in   io.WriteCloser
	out  *bufio.Reader
	errb *tailBuffer

	mu     sync.Mutex // serialises Call: the engine is driven one at a time
	id     int
	closed bool
	// dead records that the child is no longer usable, so a second call after
	// a broken pipe reports the real reason instead of a fresh EOF.
	dead error
}

// tailBuffer keeps the child's stderr for diagnostics without letting a chatty
// engine grow without bound in a process that may live for hours. It holds
// only the last write, which is where a crash reason lives.
type tailBuffer struct {
	mu  sync.Mutex
	buf []byte
	cap int
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf = append(t.buf, p...)
	if len(t.buf) > t.cap {
		t.buf = t.buf[len(t.buf)-t.cap:]
	}
	return len(p), nil
}

func (t *tailBuffer) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return string(t.buf)
}

// StartChild spawns the engine's MCP server and completes the handshake.
//
// The command is deliberately not context-bound. Every one-shot path uses
// exec.CommandContext so a cancelled CLI dies with its caller, and copying
// that here would let the first cancelled request kill the session for every
// later one. The child's lifetime is the resident's, and the resident
// decides when that is.
func (r *Runner) StartChild() (*Child, error) {
	cmd := exec.Command(r.Bin)
	cmd.Env = r.env()
	applyStackLimit(cmd)
	// stdout is the protocol channel, so it must stay a pipe: a *os.File
	// would be handed to the child directly and the reader would lose the
	// framing the reply loop depends on.
	in, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("cbm child stdin: %w", err)
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		_ = in.Close()
		return nil, fmt.Errorf("cbm child stdout: %w", err)
	}
	errb := &tailBuffer{cap: 8 << 10}
	cmd.Stderr = errb
	if err := cmd.Start(); err != nil {
		_ = in.Close()
		return nil, fmt.Errorf("start cbm child: %w", err)
	}
	c := &Child{cmd: cmd, in: in, out: bufio.NewReader(out), errb: errb}
	if err := c.handshake(); err != nil {
		c.Close()
		return nil, err
	}
	return c, nil
}

// handshake performs initialize + notifications/initialized.
//
// Without it the engine rejects every call, and the rejection would surface
// as a per-request protocol error rather than as the one clear "this binary
// is not an MCP server" diagnosis it is.
func (c *Child) handshake() error {
	if _, err := c.call("initialize", map[string]any{
		"protocolVersion": "2025-06-18",
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "tk", "version": "0.1.0"},
	}); err != nil {
		return fmt.Errorf("cbm child initialize: %w", err)
	}
	// A notification has no id and expects no reply, so it is written without
	// waiting for one.
	return c.send(map[string]any{
		"jsonrpc": "2.0", "method": "notifications/initialized", "params": map[string]any{},
	})
}

// CallTool asks the child to run one tool and returns the engine's `result`
// object, unparsed. The caller owns parsing so that the resident and the
// one-shot path converge on the same code.
func (c *Child) CallTool(tool string, args map[string]any) (json.RawMessage, error) {
	if args == nil {
		args = map[string]any{}
	}
	return c.call("tools/call", map[string]any{"name": tool, "arguments": args})
}

// call sends one request and reads until the reply carrying its id arrives.
//
// Matching on id is what keeps a server-initiated notification — which
// shares stdout with the answers — from being counted as one. The loop is
// also the only place a malformed line is survivable: the engine is free to
// log to stdout, and a log line must not be mistaken for a protocol fault.
func (c *Child) call(method string, params map[string]any) (json.RawMessage, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.dead != nil {
		return nil, c.dead
	}
	if c.closed {
		return nil, errors.New("cbm child is closed")
	}
	c.id++
	id := c.id
	if err := c.send(map[string]any{
		"jsonrpc": "2.0", "id": id, "method": method, "params": params,
	}); err != nil {
		return nil, c.fail(err)
	}
	for {
		line, err := c.out.ReadBytes('\n')
		if err != nil {
			if len(line) == 0 {
				return nil, c.fail(fmt.Errorf("cbm child ended: %w%s", err, c.stderrHint()))
			}
		}
		if len(bytesTrim(line)) == 0 {
			if err != nil {
				return nil, c.fail(fmt.Errorf("cbm child ended: %w%s", err, c.stderrHint()))
			}
			continue
		}
		var resp struct {
			ID     json.RawMessage `json:"id"`
			Result json.RawMessage `json:"result"`
			Error  json.RawMessage `json:"error"`
		}
		if json.Unmarshal(bytesTrim(line), &resp) != nil || len(resp.ID) == 0 {
			if err != nil {
				return nil, c.fail(fmt.Errorf("cbm child ended: %w%s", err, c.stderrHint()))
			}
			continue
		}
		var got int
		if json.Unmarshal(resp.ID, &got) != nil || got != id {
			continue
		}
		if len(resp.Error) > 0 {
			return nil, fmt.Errorf("cbm %s: %s", method, firstLine(diagnosis(string(resp.Error))))
		}
		return resp.Result, nil
	}
}

// fail latches the death of the child. A long-lived child that died once
// will not come back, and reporting a bare EOF on every later call would hide
// the first, real reason behind a hundred uninformative ones.
func (c *Child) fail(err error) error {
	if c.dead == nil {
		c.dead = err
	}
	return err
}

func (c *Child) stderrHint() string {
	s := firstLine(c.errb.String())
	if s == "" {
		return ""
	}
	return " (stderr: " + s + ")"
}

func (c *Child) send(msg map[string]any) error {
	raw, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	_, err = c.in.Write(raw)
	return err
}

// CallTimeout bounds one call so a wedged engine cannot hold the resident's
// single serial slot forever. It is a wall-clock cap on the request, not a
// cancel of the child: the engine keeps running and stays usable, because a
// slow query is not a reason to throw away a warm process.
func (c *Child) CallTimeout(tool string, args map[string]any, d time.Duration) (json.RawMessage, error) {
	if d <= 0 {
		return c.CallTool(tool, args)
	}
	type reply struct {
		res json.RawMessage
		err error
	}
	ch := make(chan reply, 1)
	go func() {
		res, err := c.CallTool(tool, args)
		ch <- reply{res, err}
	}()
	select {
	case r := <-ch:
		return r.res, r.err
	case <-time.After(d):
		return nil, fmt.Errorf("cbm %s: no answer in %s", tool, d)
	}
}

// Close shuts the child down. stdin EOF is the engine's documented instant
// exit, so that is the signal sent; Wait is bounded because a child that
// ignores EOF must not hang the resident's own shutdown.
func (c *Child) Close() {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	c.closed = true
	c.mu.Unlock()

	_ = c.in.Close()
	done := make(chan struct{})
	go func() {
		_ = c.cmd.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		// A child that outlives its stdin is not going to be talked down.
		// Killing is only reached on this path, never on the normal one.
		_ = c.cmd.Process.Kill()
		<-done
	}
}

// Pid is the engine process id, for the resident's bookkeeping.
func (c *Child) Pid() int {
	if c.cmd.Process == nil {
		return 0
	}
	return c.cmd.Process.Pid
}

// bytesTrim strips the framing whitespace a reply line carries. A child's
// final line may arrive without its newline when the engine exits, so this
// cannot assume one is present.
func bytesTrim(b []byte) []byte {
	start := 0
	for start < len(b) && isSpaceByte(b[start]) {
		start++
	}
	end := len(b)
	for end > start && isSpaceByte(b[end-1]) {
		end--
	}
	return b[start:end]
}

func isSpaceByte(c byte) bool {
	return c == ' ' || c == '\t' || c == '\r' || c == '\n'
}
