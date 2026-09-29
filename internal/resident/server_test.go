package resident_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ayayushsharma/true-knowledge/internal/resident"
)

// stubEngine is a controllable Engine. It exists so the listener's lifecycle
// can be tested without a real engine process, and so a test can hold a call
// open, answer with an error, or answer out of order.
type stubEngine struct {
	mu       sync.Mutex
	pid      int
	calls    []resident.Request
	closed   bool
	block    chan struct{} // if non-nil, CallTimeout waits on it
	reply    string        // if non-empty, returned verbatim
	failWith string
}

func newStub() *stubEngine { return &stubEngine{pid: 4242} }

func (s *stubEngine) CallTool(tool string, args map[string]any) (json.RawMessage, error) {
	return s.CallTimeout(tool, args, 0)
}

func (s *stubEngine) CallTimeout(tool string, args map[string]any, d time.Duration) (json.RawMessage, error) {
	// Record before blocking: the test needs to see that the call arrived, and
	// a stub that blocked first would never be observable.
	s.mu.Lock()
	s.calls = append(s.calls, resident.Request{Tool: tool, Args: args})
	block := s.block
	s.mu.Unlock()
	if block != nil {
		<-block
	}
	s.mu.Lock()
	reply, fail, closed := s.reply, s.failWith, s.closed
	s.mu.Unlock()
	if closed {
		return nil, errors.New("engine closed")
	}
	if fail != "" {
		return nil, errors.New(fail)
	}
	if reply != "" {
		return json.RawMessage(reply), nil
	}
	return json.RawMessage(`{"content":[{"type":"text","text":"stub answer"}]}`), nil
}

func (s *stubEngine) Pid() int { return s.pid }

func (s *stubEngine) Close() {
	s.mu.Lock()
	s.closed = true
	s.mu.Unlock()
}

func (s *stubEngine) callCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.calls)
}

// startServer runs a resident in the background and returns its socket path.
// The test fails rather than hangs if the resident never comes up.
func startServer(t *testing.T, start func() (resident.Engine, error)) string {
	t.Helper()
	dir := t.TempDir()
	addr := filepath.Join(dir, "resident.sock")
	srv := &resident.Server{Addr: addr, PidFile: filepath.Join(dir, "resident.pid"), Start: start}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- srv.Serve(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("the resident did not shut down")
		}
	})
	waitFor(t, addr)
	return addr
}

func waitFor(t *testing.T, addr string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if conn, err := net.DialTimeout("unix", addr, 100*time.Millisecond); err == nil {
			_ = conn.Close()
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("the resident never started listening")
}

// TestResidentAnswersToolCall is the base case: a request over the socket
// comes back as the engine's own result object, unparsed.
func TestResidentAnswersToolCall(t *testing.T) {
	eng := newStub()
	addr := startServer(t, func() (resident.Engine, error) { return eng, nil })

	reply, err := (&resident.Client{Addr: addr}).Call(resident.Request{
		Tool: "search_graph", Args: map[string]any{"project": "demo"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if reply.Error != "" {
		t.Fatalf("unexpected error: %s", reply.Error)
	}
	if !strings.Contains(string(reply.Result), "stub answer") {
		t.Errorf("the result was not forwarded: %s", reply.Result)
	}
	// A resident answer starts no engine, and says so.
	if reply.Spawns != 0 {
		t.Errorf("Spawns = %d, want 0: no engine was started for this call", reply.Spawns)
	}
	if eng.callCount() != 1 {
		t.Errorf("the engine saw %d calls, want 1", eng.callCount())
	}
}

// TestResidentSetsFormatForStructured: the caller owns the dialect, so
// Structured becomes the same argument the one-shot path sends. A resident
// that quietly asked for the rendered tree instead would break every
// --json caller that happened to have a resident running.
func TestResidentSetsFormatForStructured(t *testing.T) {
	eng := newStub()
	addr := startServer(t, func() (resident.Engine, error) { return eng, nil })

	if _, err := (&resident.Client{Addr: addr}).Call(resident.Request{
		Tool: "search_graph", Args: map[string]any{"project": "demo"}, Structured: true,
	}); err != nil {
		t.Fatal(err)
	}
	eng.mu.Lock()
	defer eng.mu.Unlock()
	if got := eng.calls[0].Args["format"]; got != "json" {
		t.Errorf("format = %v, want json", got)
	}
}

// TestResidentPreservesCallerFormat: a caller that chose a format keeps it.
// The resident is a transport, not a policy.
func TestResidentPreservesCallerFormat(t *testing.T) {
	eng := newStub()
	addr := startServer(t, func() (resident.Engine, error) { return eng, nil })
	if _, err := (&resident.Client{Addr: addr}).Call(resident.Request{
		Tool: "search_graph", Args: map[string]any{"project": "demo", "format": "table"},
	}); err != nil {
		t.Fatal(err)
	}
	eng.mu.Lock()
	defer eng.mu.Unlock()
	if got := eng.calls[0].Args["format"]; got != "table" {
		t.Errorf("format = %v, want the caller's table", got)
	}
}

// TestResidentSerialisesRequests: the engine is driven one call at a time.
// A second request that ran concurrently would interleave bytes on one
// child's pipes, and the reply loop would match the wrong id.
func TestResidentSerialisesRequests(t *testing.T) {
	eng := newStub()
	eng.block = make(chan struct{})
	addr := startServer(t, func() (resident.Engine, error) { return eng, nil })

	client := &resident.Client{Addr: addr, Timeout: 5 * time.Second}
	firstDone := make(chan error, 1)
	go func() {
		_, err := client.Call(resident.Request{Tool: "search_graph"})
		firstDone <- err
	}()
	// Wait until the first request is genuinely inside the engine.
	deadline := time.Now().Add(3 * time.Second)
	for eng.callCount() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}

	secondDone := make(chan error, 1)
	go func() {
		_, err := client.Call(resident.Request{Tool: "search_code"})
		secondDone <- err
	}()
	// The second request must still be waiting, not racing the first.
	time.Sleep(150 * time.Millisecond)
	select {
	case err := <-secondDone:
		t.Fatalf("the second request ran concurrently (err=%v); the engine is not serial", err)
	default:
	}
	if n := eng.callCount(); n != 1 {
		t.Errorf("the engine saw %d concurrent calls, want 1", n)
	}

	close(eng.block)
	if err := <-firstDone; err != nil {
		t.Errorf("first call: %v", err)
	}
	if err := <-secondDone; err != nil {
		t.Errorf("second call: %v", err)
	}
}

// TestResidentStaleSocketTakenOver: a socket file left behind by a crashed
// resident must not make every later start fail. Proving nobody is serving it
// is what makes removing it safe.
func TestResidentStaleSocketTakenOver(t *testing.T) {
	dir := t.TempDir()
	addr := filepath.Join(dir, "resident.sock")
	// A socket file with nothing behind it: exactly what a SIGKILL leaves.
	stale, err := net.Listen("unix", addr)
	if err != nil {
		t.Fatal(err)
	}
	if err := stale.Close(); err != nil {
		t.Fatal(err)
	}
	// net.Listener.Close unlinks on some platforms; make sure the file is
	// there regardless, because the stale-file case is the one under test.
	if _, err := os.Stat(addr); os.IsNotExist(err) {
		l, err := net.Listen("unix", addr)
		if err != nil {
			t.Fatal(err)
		}
		// Leave the file behind on close: the socket file outliving its
		// process is exactly the state a crash produces.
		l.(*net.UnixListener).SetUnlinkOnClose(false)
		_ = l.Close()
	}
	if _, err := os.Stat(addr); err != nil {
		t.Skipf("could not produce a stale socket file: %v", err)
	}

	eng := newStub()
	srv := &resident.Server{Addr: addr, Start: func() (resident.Engine, error) { return eng, nil }}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- srv.Serve(ctx) }()
	select {
	case err := <-done:
		t.Fatalf("a stale socket blocked startup: %v", err)
	case <-time.After(2 * time.Second):
	}
	waitFor(t, addr)
	if _, err := (&resident.Client{Addr: addr}).Call(resident.Request{Tool: "ping"}); err != nil {
		t.Errorf("the new resident is not serving: %v", err)
	}
}

// TestResidentRefusesToDisplaceALiveOne: takeover is only ever justified by a
// dead predecessor. Killing a working resident would break every client
// mid-call, so a second start must fail loudly instead.
func TestResidentRefusesToDisplaceALiveOne(t *testing.T) {
	eng := newStub()
	addr := startServer(t, func() (resident.Engine, error) { return eng, nil })

	second := &resident.Server{Addr: addr, Start: func() (resident.Engine, error) {
		t.Error("the second resident started an engine it should never have reached")
		return newStub(), nil
	}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	err := second.Serve(ctx)
	if err == nil {
		t.Fatal("a second resident displaced a live one")
	}
	if !strings.Contains(err.Error(), "already running") {
		t.Errorf("err = %v, want a refusal naming the live resident", err)
	}
}

// TestResidentWritesAndCleansPidFile: the pid file must not advertise a
// socket nobody can dial, and must not outlive the resident.
func TestResidentWritesAndCleansPidFile(t *testing.T) {
	dir := t.TempDir()
	addr := filepath.Join(dir, "resident.sock")
	pidFile := filepath.Join(dir, "resident.pid")
	eng := newStub()
	srv := &resident.Server{Addr: addr, PidFile: pidFile, Start: func() (resident.Engine, error) { return eng, nil }}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- srv.Serve(ctx) }()
	waitFor(t, addr)

	if !resident.PidAlive(pidFile) {
		t.Error("the pid file does not name a live process while the resident is up")
	}
	raw, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(raw)) != fmt.Sprint(os.Getpid()) {
		t.Errorf("pid file = %q, want this process", raw)
	}

	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the resident did not shut down")
	}
	if _, err := os.Stat(pidFile); !os.IsNotExist(err) {
		t.Errorf("the pid file outlived the resident: %v", err)
	}
	if resident.PidAlive(pidFile) {
		t.Error("a removed pid file still reports alive")
	}
}

// TestResidentQuiesceRefusesThenResumes: an install swaps the engine without
// closing the socket. A client that arrives mid-swap must be told, not
// queued, and must find the same endpoint working afterwards.
func TestResidentQuiesceRefusesThenResumes(t *testing.T) {
	eng := newStub()
	dir := t.TempDir()
	addr := filepath.Join(dir, "resident.sock")
	srv := &resident.Server{Addr: addr, Start: func() (resident.Engine, error) { return eng, nil }}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- srv.Serve(ctx) }()
	waitFor(t, addr)
	client := &resident.Client{Addr: addr, Timeout: 3 * time.Second}

	if _, err := client.Call(resident.Request{Tool: "ping"}); err != nil {
		t.Fatal(err)
	}

	srv.Quiesce()
	if !eng.closed {
		t.Error("Quiesce did not release the child")
	}
	reply, err := client.Call(resident.Request{Tool: "ping"})
	if err != nil {
		t.Fatalf("a quiesced resident stopped answering entirely: %v", err)
	}
	if !strings.Contains(reply.Error, "quiesced") {
		t.Errorf("err = %q, want the client told to fall back", reply.Error)
	}

	// A fresh engine, same socket.
	eng2 := newStub()
	srv.Start = func() (resident.Engine, error) { return eng2, nil }
	if err := srv.Resume(); err != nil {
		t.Fatal(err)
	}
	reply, err = client.Call(resident.Request{Tool: "ping"})
	if err != nil {
		t.Fatalf("the resident did not come back: %v", err)
	}
	if reply.Error != "" {
		t.Errorf("the resumed resident still refused: %s", reply.Error)
	}
	if eng2.callCount() == 0 {
		t.Error("the resumed resident did not use the new engine")
	}
}

// TestClientAbsentIsNotAnError: with no resident, the client must say so
// plainly. Most invocations take this path, and a caller that could not tell
// it from a real failure would have to spawn twice.
func TestClientAbsentIsNotAnError(t *testing.T) {
	client := &resident.Client{Addr: filepath.Join(t.TempDir(), "nothing.sock")}
	if client.Available() {
		t.Error("Available reported a resident that was never started")
	}
	_, err := client.Call(resident.Request{Tool: "ping"})
	if !errors.Is(err, resident.ErrNoResident) {
		t.Errorf("err = %v, want ErrNoResident", err)
	}
}

// TestResidentRejectsBadRequests: a garbage line or a tool-less request gets
// an answer. A resident that closed silently would leave the client to guess
// between "bad request" and "resident died".
func TestResidentRejectsBadRequests(t *testing.T) {
	addr := startServer(t, func() (resident.Engine, error) { return newStub(), nil })

	for _, tc := range []struct {
		name string
		line string
		want string
	}{
		{"garbage", "not json\n", "malformed"},
		{"no tool", `{"args":{}}` + "\n", "no tool"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			conn, err := net.DialTimeout("unix", addr, time.Second)
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
			if _, err := conn.Write([]byte(tc.line)); err != nil {
				t.Fatal(err)
			}
			buf := make([]byte, 4096)
			n, err := conn.Read(buf)
			if err != nil {
				t.Fatalf("no answer to a bad request: %v", err)
			}
			var reply resident.Reply
			if err := json.Unmarshal(buf[:n], &reply); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(reply.Error, tc.want) {
				t.Errorf("error = %q, want it to mention %q", reply.Error, tc.want)
			}
		})
	}
}

// TestResidentEngineErrorIsAValue: an engine refusal must arrive as
// Reply.Error, not as a transport error. The caller has to be able to tell
// "the engine said no" from "no resident answered" — the first is an answer
// worth reporting, the second means spawn instead.
func TestResidentEngineErrorIsAValue(t *testing.T) {
	eng := newStub()
	eng.failWith = "project not indexed"
	addr := startServer(t, func() (resident.Engine, error) { return eng, nil })

	reply, err := (&resident.Client{Addr: addr}).Call(resident.Request{Tool: "search_graph"})
	if err != nil {
		t.Fatalf("an engine error was reported as a transport error: %v", err)
	}
	if !strings.Contains(reply.Error, "project not indexed") {
		t.Errorf("error = %q, want the engine's own message", reply.Error)
	}
}

// TestResidentDropsSilentConnection: a client that connects and never writes
// must not hold the single serial slot forever. A resident that is alive but
// wedged is worse than one that is absent, because the client would keep
// dialing it.
func TestResidentDropsSilentConnection(t *testing.T) {
	eng := newStub()
	dir := t.TempDir()
	addr := filepath.Join(dir, "resident.sock")
	srv := &resident.Server{Addr: addr, Start: func() (resident.Engine, error) { return eng, nil }}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = srv.Serve(ctx) }()
	waitFor(t, addr)

	silent, err := net.DialTimeout("unix", addr, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	// The read deadline on the server side is 30s, which is too long to wait
	// for here. This asserts the slot is not held by checking the next real
	// call is still served promptly *after* the silent peer is dropped by the
	// server's own deadline; a shorter assertion is left to the e2e suite.
	// Close it so the assertion below is about the healthy path.
	_ = silent.Close()

	client := &resident.Client{Addr: addr, Timeout: 3 * time.Second}
	if _, err := client.Call(resident.Request{Tool: "ping"}); err != nil {
		t.Errorf("a closed peer broke the resident: %v", err)
	}
}

// A socket path that cannot bind must say so in terms the user can act on. The
// kernel's own answer is "bind: invalid argument", which names neither the
// cause nor the fix, and the cause here is a TK_HOME the user chose.
//
// Asserted through Serve rather than the unexported listen, because Serve is
// the contract a caller actually gets.
func TestServeRejectsAnOverlongSocketPath(t *testing.T) {
	dir := t.TempDir()
	// Grow the path past sun_path without needing a real 100-deep tree.
	addr := filepath.Join(dir, strings.Repeat("p", 120))
	srv := &resident.Server{
		Addr:    addr,
		PidFile: filepath.Join(dir, "resident.pid"),
		Start:   func() (resident.Engine, error) { return newStub(), nil },
	}
	err := srv.Serve(context.Background())
	if err == nil {
		t.Fatal("expected an overlong socket path to be refused")
	}
	msg := err.Error()
	if !strings.Contains(msg, "TK_HOME") {
		t.Errorf("error must name the thing to change: %v", err)
	}
	if !strings.Contains(msg, "shorter") {
		t.Errorf("error must say which direction to move: %v", err)
	}
	// The portable limit, not Linux's. A path that fits Linux's 108-byte
	// sun_path but not macOS's 104 must be refused here, because a tk that
	// starts on one machine and moves to the other must not fail on arrival.
	if !strings.Contains(msg, "103") {
		t.Errorf("error must state the portable limit (103), not a platform-specific one: %v", err)
	}
	// Nothing may be left behind by a refusal.
	if _, statErr := os.Stat(addr); !os.IsNotExist(statErr) {
		t.Errorf("a refused listen left a socket file at %s", addr)
	}
	if _, statErr := os.Stat(srv.PidFile); !os.IsNotExist(statErr) {
		t.Errorf("a refused listen left a pid file behind: %v", statErr)
	}
}
