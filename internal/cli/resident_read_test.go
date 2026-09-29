package cli

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ayayushsharma/true-knowledge/internal/paths"
	"github.com/ayayushsharma/true-knowledge/internal/resident"
)

// routingEngine is a resident Engine that answers from memory. It counts calls
// so a test can prove a read reached the warm child instead of the spawn
// fallback, and it can be told to refuse.
type routingEngine struct {
	mu     sync.Mutex
	calls  int
	refuse string
	reply  string
	closed bool
	pid    int
}

func (e *routingEngine) CallTool(tool string, args map[string]any) (json.RawMessage, error) {
	return e.CallTimeout(tool, args, 0)
}

func (e *routingEngine) CallTimeout(tool string, args map[string]any, d time.Duration) (json.RawMessage, error) {
	e.mu.Lock()
	e.calls++
	refuse, reply, closed := e.refuse, e.reply, e.closed
	e.mu.Unlock()
	if closed {
		return nil, errors.New("engine closed")
	}
	if refuse != "" {
		return nil, errors.New(refuse)
	}
	if reply != "" {
		return json.RawMessage(reply), nil
	}
	return json.RawMessage(`{"content":[{"type":"text","text":"results: 1\ntotal: 1"}]}`), nil
}

// Pid reports the engine's identity, which is how a test tells one warm child
// from the next. A stub that always answered 5150 would make an engine swap
// indistinguishable from no swap at all.
func (e *routingEngine) Pid() int {
	if e.pid == 0 {
		return 5150
	}
	return e.pid
}

func (e *routingEngine) Close() {
	e.mu.Lock()
	e.closed = true
	e.mu.Unlock()
}

func (e *routingEngine) callCount() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.calls
}

// startResidentCtx wires a real resident onto a scratch home and returns a Ctx
// whose Paths point at it. The returned engine is the single stub instance
// every call is served by.
func startResidentCtx(t *testing.T, reply, refuse string) (*Ctx, *routingEngine) {
	t.Helper()
	home := t.TempDir()
	p := paths.Resolve(home)
	if err := p.Ensure(); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	eng := &routingEngine{reply: reply, refuse: refuse}
	srv := &resident.Server{
		Addr:    p.ResidentSocket(),
		PidFile: p.ResidentPid(),
		LogPath: p.ResidentLog(),
		Start:   func() (resident.Engine, error) { return eng, nil },
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _ = srv.Serve(ctx); close(done) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("resident did not stop")
		}
	})
	// Wait for a real accept(2), the same readiness bar `tk mcp --detach` uses.
	//
	// A timeout here is fatal, not a fall-through. Proceeding without a
	// resident turns every assertion below into a statement about the spawn
	// path, which is the opposite of what this helper exists to test, and the
	// failure then reads as a missing log file rather than as a resident that
	// never started. Silent absence is the failure mode this repo forbids.
	// Readiness is a status reply, not a dial. bind(2) also listen(2)s, so a
	// connect succeeds before Serve reaches its accept loop — a dial-based wait
	// returns while the engine is still being started, and every assertion
	// below then races a server that is not finished booting.
	if !waitForServing(p.ResidentSocket(), 10*time.Second) {
		t.Fatalf("resident never served on %s", p.ResidentSocket())
	}
	return &Ctx{Paths: p, CBMOK: true}, eng
}

// waitForServing polls until the resident answers a status with an engine
// attached, and reports whether it did. It returns a bool rather than failing
// the test itself so the caller decides how loud to be about it.
func waitForServing(addr string, d time.Duration) bool {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if (&resident.Client{Addr: addr}).Running() {
			return true
		}
		time.Sleep(5 * time.Millisecond)
	}
	return false
}

func loggedBackends(c *Ctx) []string {
	out := make([]string, 0, len(c.Events))
	for _, e := range c.Events {
		out = append(out, e.Backend)
	}
	return out
}

// A resident that refuses must surface the engine's own error, and must not
// re-run the call. The refusal is an answer: re-running it in a fresh process
// produces the same refusal, slower, and hides which engine said it.
func TestResidentRefusalPropagatesAndDoesNotFallback(t *testing.T) {
	c, eng := startResidentCtx(t, "", "index is corrupted; run tk index --rebuild")

	_, err := c.cbmCallStructured(context.Background(), "search_graph", map[string]any{"name_pattern": "Demo"})
	if err == nil {
		t.Fatal("engine refusal reported as success")
	}
	if !strings.Contains(err.Error(), "index is corrupted") {
		t.Fatalf("error lost the engine's own message: %v", err)
	}
	if !strings.Contains(err.Error(), "search_graph") {
		t.Fatalf("error does not name the tool: %v", err)
	}
	if got := eng.callCount(); got != 1 {
		t.Fatalf("engine called %d times, want 1 (a refusal must not retry)", got)
	}
	if got := loggedBackends(c); len(got) != 1 || got[0] != "resident" {
		t.Fatalf("backends = %v, want exactly [resident] (no spawn fallback)", got)
	}
	// The failed call has to reach tk.log, or the refusal is invisible.
	var sawFailed bool
	for _, e := range c.Events {
		if e.Backend == "resident" && !e.OK && e.Error != "" {
			sawFailed = true
		}
	}
	if !sawFailed {
		t.Error("refusal not recorded as a failed resident call")
	}
}

// The JSON face must refuse the same way. An earlier version returned an empty
// result with a nil error here, which rendered as a successful empty search.
func TestResidentRefusalPropagatesOnJSONFace(t *testing.T) {
	c, eng := startResidentCtx(t, "", "store is locked")

	out, err := c.cbmCallJSON(context.Background(), "search_graph", map[string]any{"name_pattern": "Demo"})
	if err == nil {
		t.Fatalf("engine refusal reported as success, got %q", out)
	}
	if !strings.Contains(err.Error(), "store is locked") {
		t.Fatalf("error lost the engine's own message: %v", err)
	}
	if out != "" {
		t.Fatalf("a refused call returned output: %q", out)
	}
	if got := eng.callCount(); got != 1 {
		t.Fatalf("engine called %d times, want 1", got)
	}
}

// No resident is the normal case and must fall through to the spawn path. The
// Ctx here has no Runner, so the fallback fails loudly — which is the proof
// that the resident was consulted first and did not answer.
func TestNoResidentFallsBack(t *testing.T) {
	home := t.TempDir()
	p := paths.Resolve(home)
	if err := p.Ensure(); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if _, err := os.Stat(p.ResidentSocket()); !os.IsNotExist(err) {
		t.Fatalf("expected no socket, stat err = %v", err)
	}
	c := &Ctx{Paths: p, CBMOK: true}

	if _, ok, rerr := c.residentTry("search_graph", nil, false); ok || rerr != nil {
		t.Fatalf("absent resident = ok:%v err:%v, want ok:false err:nil", ok, rerr)
	}
	if len(c.Events) != 0 {
		t.Fatalf("a silent dial must not log a backend call, got %+v", c.Events)
	}
}

// A resident that answers must be used, and its text returned unchanged, with
// no spawn recorded. This is the whole point of the flag.
func TestResidentAnswersWithoutSpawning(t *testing.T) {
	reply := `{"content":[{"type":"text","text":"results: 1\n  live.Demo Function main.go\ntotal: 1"}]}`
	c, eng := startResidentCtx(t, reply, "")

	out, err := c.cbmCallJSON(context.Background(), "search_graph", map[string]any{"name_pattern": "Demo"})
	if err != nil {
		t.Fatalf("resident read failed: %v", err)
	}
	if !strings.Contains(out, "live.Demo") {
		t.Fatalf("text face not returned: %q", out)
	}
	if got := eng.callCount(); got != 1 {
		t.Fatalf("engine called %d times, want 1", got)
	}
	if got := loggedBackends(c); len(got) != 1 || got[0] != "resident" {
		t.Fatalf("backends = %v, want exactly [resident]", got)
	}
}

// CBMOK false short-circuits before any dial: a home with no engine must not
// pay for a socket round trip on every read.
func TestResidentSkippedWhenCBMMissing(t *testing.T) {
	home := t.TempDir()
	p := paths.Resolve(home)
	if err := p.Ensure(); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	c := &Ctx{Paths: p, CBMOK: false}
	if _, ok, err := c.residentTry("search_graph", nil, false); ok || err != nil {
		t.Fatalf("CBMOK=false = ok:%v err:%v, want ok:false err:nil", ok, err)
	}
}

// The lifecycle log must be a different file from the trace log. tk.log is one
// invocation envelope per line and is read with jq recipes that assume argv
// exists; a "listening" line with no argv breaks the whole file, not just
// itself.
func TestResidentLifecycleLogIsSeparateFromTrace(t *testing.T) {
	c, _ := startResidentCtx(t, "", "")
	if c.Paths.ResidentLog() == c.Paths.LogFile() {
		t.Fatal("resident lifecycle log must not be the trace log")
	}
	if !strings.Contains(c.Paths.ResidentLog(), "resident.log") {
		t.Fatalf("unexpected lifecycle log path %q", c.Paths.ResidentLog())
	}
	if filepath.Dir(c.Paths.ResidentLog()) != filepath.Dir(c.Paths.LogFile()) {
		t.Fatal("resident.log should sit beside tk.log in state/logs")
	}
	// Nothing may have been written to the trace log by the resident.
	if raw, err := os.ReadFile(c.Paths.LogFile()); err == nil && len(raw) != 0 {
		t.Fatalf("resident wrote to the trace log: %q", raw)
	}
	// The lifecycle line belongs in its own file, and must stay parseable.
	if _, err := os.Stat(c.Paths.ResidentLog()); err != nil {
		t.Fatalf("expected a lifecycle log at %s: %v", c.Paths.ResidentLog(), err)
	}
	raw, err := os.ReadFile(c.Paths.ResidentLog())
	if err != nil {
		t.Fatalf("read lifecycle log: %v", err)
	}
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		if line == "" {
			continue
		}
		var rec map[string]any
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("lifecycle line is not JSON: %q", line)
		}
		if _, isInvocation := rec["argv"]; isInvocation {
			continue
		}
		if rec["backend"] != "resident" {
			t.Fatalf("unexpected lifecycle record: %v", rec)
		}
	}
}
