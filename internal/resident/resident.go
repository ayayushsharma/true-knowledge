// Package resident holds the long-lived CBM child tk can attach to.
//
// The resident exists to remove the per-spawn cost from graph reads. A
// `cbm cli --json` spawn pays process start, a store open and a
// version-cohort lock admission before the engine reads a byte, and tk paid
// all of it again on every call. A resident pays it once, when it starts, and
// then answers over a Unix socket.
//
// What it is not: a pool, a queue, a scheduler, or a second MCP server. One
// child, one request at a time, one result back. The engine is driven
// serially on purpose — measured CBM concurrency is real, but a tk resident
// that multiplexed would need request ids, an in-flight cap and a fairness
// story to buy a win nobody asked for, on a path whose entire reason to exist
// is to be small enough to reason about.
//
// The result is forwarded verbatim. The client hands it to the same
// parseEnvelope a one-shot answer goes through, so a resident answer and a
// spawned answer are parsed by identical code. Render parity is therefore
// structural: there is no second formatter to keep in sync.
package resident

import (
	"encoding/json"
	"errors"
	"fmt"
)

// errUnsupported is returned on a platform with no Unix socket, so a caller
// gets a sentence it can print instead of a compile-time absence. The
// resident is Linux and macOS first; Windows is a named follow-up.
var errUnsupported = errors.New("resident: unix sockets are not supported on this platform")

// Request is one tool call over the socket. Structured asks the engine for
// its JSON payload; it is the same argument the one-shot path passes, carried
// through so the resident does not decide a dialect the caller already chose.
type Request struct {
	Tool       string         `json:"tool"`
	Args       map[string]any `json:"args"`
	Structured bool           `json:"structured,omitempty"`
	// TimeoutMS bounds the wait. Zero means the resident's default. A request
	// that exceeds it is answered with an error and the child is left running:
	// a slow query is not a reason to discard a warm process.
	TimeoutMS int `json:"timeout_ms,omitempty"`

	// Action runs a control operation instead of a tool call, and Tool is
	// ignored when it is set.
	//
	// This exists because `tk install cbm` is a different process from the
	// resident it has to swap the engine under. In-process methods cannot
	// serve it: an installer that could not reach the resident would have to
	// kill it, and killing it is the one outcome the design forbids — the
	// socket is the promise, and a restart breaks it for every client that
	// redials mid-upgrade.
	Action string `json:"action,omitempty"`
}

// Control actions. They are a closed set rather than free-form verbs because
// each one restarts or drops a process: an unknown action is a protocol error,
// never something to forward to a shell or an engine.
const (
	// ActionQuiesce drains in-flight work and releases the child. The socket
	// stays bound and keeps answering, with ErrQuiesced, for as long as the
	// swap takes.
	ActionQuiesce = "quiesce"
	// ActionResume starts a fresh engine — from the binary as it exists now,
	// which is how an install is picked up — and answers again.
	ActionResume = "resume"
	// ActionStatus reports liveness and whether an engine is attached. It is
	// the one action that must work while quiesced, so a client can tell
	// "swapping" from "dead" without a second guessing mechanism.
	ActionStatus = "status"
)

// Reply is the answer to exactly one Request.
//
// Result is the engine's `result` object, unparsed, so the client owns
// interpretation. Engine errors are carried here rather than as a Go error
// because they are answers: a tool that failed is a completed call, and the
// client must be able to tell "the engine said no" from "the resident could
// not reach the engine".
type Reply struct {
	Result json.RawMessage `json:"result,omitempty"`
	Error  string          `json:"error,omitempty"`
	// Spawns is 0 for a resident answer: no engine was started to serve it.
	// The field exists so a trace reader can see the zero instead of inferring
	// it, and so a client that adds up engine costs does not report a call
	// that was really served by a process started minutes ago as free.
	Spawns int `json:"spawns"`

	// Status is set only for ActionStatus. EnginePID is 0 while quiesced, and
	// that zero is the answer a caller needs: it distinguishes "holding a
	// swapped-out child" from "serving".
	Status *Status `json:"status,omitempty"`
}

// Status is the resident's own view of itself.
type Status struct {
	// EnginePID is the warm child's pid, or 0 when none is attached.
	EnginePID int `json:"engine_pid"`
	// Quiesced is true between a quiesce and its resume.
	Quiesced bool `json:"quiesced"`
	// PID is the resident process, not the child. It is what a stop signal
	// has to be sent to.
	PID int `json:"pid"`
}

// The wire framing is line-delimited JSON: one request line, one reply line.
// Not length-prefixed, so the same bytes are readable with nc, socat and jq —
// which is the debugging story tk owes an operator staring at a resident at
// 2am.

// Encode writes a value as one line.
func Encode(v any) ([]byte, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return append(raw, '\n'), nil
}

// Decode reads one line into v.
func Decode(line []byte, v any) error {
	if err := json.Unmarshal(line, v); err != nil {
		return fmt.Errorf("resident: malformed reply: %w", err)
	}
	return nil
}
