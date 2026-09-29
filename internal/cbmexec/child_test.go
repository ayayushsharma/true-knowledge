package cbmexec_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ayayushsharma/true-knowledge/internal/cbmexec"
	"github.com/ayayushsharma/true-knowledge/internal/config"
	"github.com/ayayushsharma/true-knowledge/internal/paths"
)

// buildFakeMCP compiles the testdata fake engine once per test binary.
//
// The fake is a real Go program rather than a shell script because the child
// protocol is line-delimited JSON with id-matched replies, and a script
// cannot produce that without a JSON parser.
var (
	fakeOnce sync.Once
	fakePath string
	fakeErr  error
)

func fakeMCP(t *testing.T, mode string) string {
	t.Helper()
	fakeOnce.Do(func() {
		dir, err := os.MkdirTemp("", "tk-fakemcp-*")
		if err != nil {
			fakeErr = err
			return
		}
		bin := filepath.Join(dir, "fakemcp")
		build := exec.Command("go", "build", "-o", bin, "./testdata/fakemcp")
		if out, err := build.CombinedOutput(); err != nil {
			fakeErr = err
			t.Logf("build output: %s", out)
			return
		}
		fakePath = bin
	})
	if fakeErr != nil {
		t.Skipf("cannot build the fake MCP engine: %v", fakeErr)
	}
	if mode == "ok" {
		return fakePath
	}
	// Modes are argv-selected, so a wrapper passes the mode through without
	// rebuilding anything.
	dir := t.TempDir()
	shim := filepath.Join(dir, "fakemcp")
	script := "#!/bin/sh\nexec " + fakePath + " " + mode + " \"$@\"\n"
	if err := os.WriteFile(shim, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return shim
}

func childRunner(t *testing.T, bin string) *cbmexec.Runner {
	t.Helper()
	return &cbmexec.Runner{
		Bin:   bin,
		Paths: paths.Resolve(filepath.Join(t.TempDir(), "home")),
		Cfg:   config.Config{},
	}
}

// TestChildHandshakeAndCall: the child must complete initialize before any
// call, and must hand back the engine's own result object rather than a
// re-rendering of it.
func TestChildHandshakeAndCall(t *testing.T) {
	c, err := childRunner(t, fakeMCP(t, "ok")).StartChild()
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	res, err := c.CallTool("search_graph", map[string]any{"project": "demo"})
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(res, &got); err != nil {
		t.Fatalf("result is not the engine's envelope: %v (%s)", err, res)
	}
	if !strings.Contains(got.Content[0].Text, "search_graph") {
		t.Errorf("the tool name did not reach the engine: %q", got.Content[0].Text)
	}
}

// TestChildReusedAcrossCalls is the reason the type exists: N calls, one
// process. A child that quietly respawned per call would pass every other
// test here and would deliver none of the win.
func TestChildReusedAcrossCalls(t *testing.T) {
	c, err := childRunner(t, fakeMCP(t, "ok")).StartChild()
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	pid := c.Pid()
	if pid == 0 {
		t.Fatal("no pid: the child was never started")
	}
	for i := 0; i < 5; i++ {
		if _, err := c.CallTool("search_graph", map[string]any{"project": "demo"}); err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
	}
	if c.Pid() != pid {
		t.Errorf("the child was replaced mid-session: %d -> %d", pid, c.Pid())
	}
}

// TestChildSkipsNotifications: the engine may log or notify on stdout. A
// line that is not our id-matched reply must be skipped, not returned as an
// answer and not treated as a fault — otherwise a chatty engine makes tk
// look broken.
func TestChildSkipsNotifications(t *testing.T) {
	c, err := childRunner(t, fakeMCP(t, "noisy")).StartChild()
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	res, err := c.CallTool("search_graph", nil)
	if err != nil {
		t.Fatalf("noise on stdout broke the reply loop: %v", err)
	}
	if !strings.Contains(string(res), "answer for search_graph") {
		t.Errorf("got a notification instead of the answer: %s", res)
	}
}

// TestChildDeadIsSticky: once the engine is gone, later calls must report
// why rather than a fresh bare EOF. One crash otherwise produces a hundred
// uninformative errors, and the real reason is the first to scroll away.
func TestChildDeadIsSticky(t *testing.T) {
	c, err := childRunner(t, fakeMCP(t, "crash")).StartChild()
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if _, err := c.CallTool("search_graph", nil); err != nil {
		t.Fatalf("first call: %v", err)
	}
	_, first := c.CallTool("search_graph", nil)
	if first == nil {
		t.Fatal("a crashed child reported success")
	}
	if !strings.Contains(first.Error(), "store unreadable") {
		t.Errorf("the crash reason was dropped: %v", first)
	}
	// The latched reason must survive verbatim: same message, not a bare EOF.
	_, second := c.CallTool("search_graph", nil)
	if second == nil || second.Error() != first.Error() {
		t.Errorf("the death was not sticky: first=%v second=%v", first, second)
	}
}

// TestChildCloseIsIdempotent: Close runs from a defer and from the resident's
// shutdown path, and a second close must not panic on a closed pipe.
func TestChildCloseIsIdempotent(t *testing.T) {
	c, err := childRunner(t, fakeMCP(t, "ok")).StartChild()
	if err != nil {
		t.Fatal(err)
	}
	c.Close()
	c.Close()
	if _, err := c.CallTool("search_graph", nil); err == nil {
		t.Error("a closed child answered a call")
	}
}

// TestCallTimeoutKeepsChildAlive: a timeout bounds the wait, not the
// process. Discarding a warm child because one query was slow would
// reintroduce exactly the spawn cost the child exists to remove.
func TestCallTimeoutKeepsChildAlive(t *testing.T) {
	c, err := childRunner(t, fakeMCP(t, "ok")).StartChild()
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	pid := c.Pid()
	start := time.Now()
	// The fake answers instantly, so this asserts the shape of the deadline
	// rather than a slow query. A real timeout is exercised by the resident's
	// own tests against a hanging engine.
	if _, err := c.CallTimeout("search_graph", nil, time.Nanosecond); err == nil {
		t.Skip("the fake beat the deadline; this run proves nothing")
	}
	if time.Since(start) > 2*time.Second {
		t.Error("CallTimeout did not return promptly")
	}
	if c.Pid() != pid || pid == 0 {
		t.Errorf("a request timeout killed the child: %d -> %d", pid, c.Pid())
	}
}
