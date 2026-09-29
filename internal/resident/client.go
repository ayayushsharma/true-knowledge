package resident

import (
	"bufio"
	"errors"
	"fmt"
	"net"
	"time"
)

// dialTimeout bounds the connect. It is short on purpose: the resident either
// answers a warm child in tens of milliseconds or it is not there, and a
// client that waited out a long TCP-style timeout would reintroduce the very
// spawn it is trying to avoid.
const dialTimeout = 2 * time.Second

// ErrNoResident means nobody is serving the socket.
//
// It is a normal outcome, not a fault. The resident is opt-in, so most
// invocations will always take this path, and the caller's one-shot spawn is
// the answer rather than a degraded mode.
var ErrNoResident = errors.New("resident: not running")

// Client dials a resident and sends one request per call.
//
// Every method returns ErrNoResident when there is nothing to talk to, and a
// caller is expected to fall back to spawning. That asymmetry is the design:
// the resident is an accelerator, and tk must be exactly as correct with it
// absent as with it present.
type Client struct {
	Addr string
	// Timeout bounds one request end to end. Zero uses the resident's own
	// default, which is what a human-facing call wants.
	Timeout time.Duration
}

// Available reports whether a resident is serving the socket.
//
// It is a hint, not a guarantee: a resident can die between this check and
// the dial. Callers must still handle ErrNoResident, and must not use this to
// decide anything they would regret being wrong about.
func (c *Client) Available() bool {
	conn, err := net.DialTimeout("unix", c.Addr, dialTimeout)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

// Call sends one request and returns the reply.
//
// One connection per request, matching the server's one-request-per-
// connection rule. The alternative — a pooled connection with a reply loop —
// would need to survive a half-closed socket and a mid-request restart, and
// the measured win is already banked by not spawning. Simplicity here is what
// keeps the failure mode "dial failed", which the caller already handles.
func (c *Client) Call(req Request) (Reply, error) {
	conn, err := net.DialTimeout("unix", c.Addr, dialTimeout)
	if err != nil {
		return Reply{}, ErrNoResident
	}
	defer conn.Close()

	// The deadline covers write plus read, so a resident that accepts the
	// connection and then wedges still frees the caller. The caller's timeout
	// and the request's own are independent knobs: whichever is set and
	// shorter wins, and when neither is set the resident's default applies.
	// A zero value must never become the deadline, or every call would time
	// out instantly.
	var deadline time.Time
	switch {
	case c.Timeout > 0 && req.TimeoutMS > 0:
		deadline = time.Now().Add(minDur(c.Timeout, time.Duration(req.TimeoutMS)*time.Millisecond))
	case c.Timeout > 0:
		deadline = time.Now().Add(c.Timeout)
	case req.TimeoutMS > 0:
		deadline = time.Now().Add(time.Duration(req.TimeoutMS) * time.Millisecond)
	default:
		deadline = time.Now().Add(defaultTimeout)
	}
	_ = conn.SetDeadline(deadline)

	raw, err := Encode(req)
	if err != nil {
		return Reply{}, err
	}
	if _, err := conn.Write(raw); err != nil {
		return Reply{}, ErrNoResident
	}
	line, err := bufio.NewReader(conn).ReadBytes('\n')
	if err != nil && len(line) == 0 {
		// EOF with no reply: the resident closed on us. Treated as absent so
		// the caller spawns instead of reporting a protocol fault to a user
		// who only asked a question.
		return Reply{}, ErrNoResident
	}
	var reply Reply
	if err := Decode(line, &reply); err != nil {
		return Reply{}, err
	}
	// An engine refusal comes back in Reply.Error, not as a Go error: the
	// call did complete, and the caller has to be able to tell "the engine
	// said no" from "no resident answered" because only the second one means
	// it should spawn.
	return reply, nil
}

// Control sends an action and returns the status the resident reports.
//
// It reports ErrNoResident when there is nothing to control, which is the
// normal case for an install on a machine that never detached one. Callers
// must treat that as "nothing to do", not as a failure: an install has no
// business failing because a resident was not running.
func (c *Client) Control(action string) (*Status, error) {
	reply, err := c.Call(Request{Action: action})
	if err != nil {
		return nil, err
	}
	if reply.Error != "" {
		return nil, errors.New(reply.Error)
	}
	if reply.Status == nil {
		return nil, fmt.Errorf("resident: %s returned no status", action)
	}
	return reply.Status, nil
}

// Running reports whether a resident is serving this home right now.
//
// Available is not enough. It answers "does something accept on this socket",
// which a quiesced resident also does, and the one caller that matters here —
// `tk install cbm` deciding whether to swap an engine — must not treat a
// resident mid-swap as one that will pick up a new binary on its own.
func (c *Client) Running() bool {
	st, err := c.Control(ActionStatus)
	return err == nil && !st.Quiesced && st.EnginePID != 0
}

// minDur returns the shorter of two durations.
func minDur(a, b time.Duration) time.Duration {
	if a < b {
		return a
	}
	return b
}
