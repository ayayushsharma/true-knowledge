package resident

import (
	"context"
	"encoding/json"
	"net"
	"path/filepath"
	"testing"
	"time"
)

// idleEngine keeps the listener up. This test is about a connection that sends
// no request, so the engine is never called — it only has to exist, because
// Serve closes the listener if starting one fails.
type idleEngine struct{}

func (idleEngine) CallTool(string, map[string]any) (json.RawMessage, error) {
	return json.RawMessage(`{}`), nil
}
func (idleEngine) CallTimeout(string, map[string]any, time.Duration) (json.RawMessage, error) {
	return json.RawMessage(`{}`), nil
}
func (idleEngine) Pid() int { return 1 }
func (idleEngine) Close()   {}

// The hangup has to be proven, not asserted. The external test file could not
// reach readDeadline, so the only way to observe a 30-second bound without
// waiting 30 seconds is an internal test.
//
// The test that used to claim this coverage had closed its silent peer before
// the assertion that mattered, so the claim outlived the check.
//
// A client-side timeout here means the server never hung up; EOF means it did.
func TestSilentConnectionIsHungUpOn(t *testing.T) {
	dir := t.TempDir()
	addr := filepath.Join(dir, "resident.sock")
	srv := &Server{
		Addr:         addr,
		readDeadline: 150 * time.Millisecond,
		Start:        func() (Engine, error) { return idleEngine{}, nil },
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = srv.Serve(ctx) }()

	var quiet net.Conn
	for deadline := time.Now().Add(3 * time.Second); time.Now().Before(deadline); {
		if conn, err := net.Dial("unix", addr); err == nil {
			quiet = conn
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if quiet == nil {
		t.Fatal("the resident never started listening")
	}
	defer quiet.Close()

	_ = quiet.SetReadDeadline(time.Now().Add(10 * time.Second))
	_, err := quiet.Read(make([]byte, 1))
	if err == nil {
		t.Fatal("the server accepted a connection that never sent a request")
	}
	if ne, ok := err.(net.Error); ok && ne.Timeout() {
		t.Fatalf("the client timed out, so the server never hung up: %v", err)
	}
}
