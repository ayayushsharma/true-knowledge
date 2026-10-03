package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ayayushsharma/true-knowledge/internal/cbmexec"
	"github.com/ayayushsharma/true-knowledge/internal/resident"
)

// countingRunner is the spawn, instrumented. The gate for this whole file is a
// count, not an output: "the resident answered" is only true if nothing here
// was called, and an assertion on the answer alone would pass even if the
// server had dialled, ignored the reply, and spawned anyway.
type countingRunner struct {
	calls int
	tools []string
}

func (r *countingRunner) RunJSON(ctx context.Context, tool string, payload map[string]any) (string, error) {
	r.calls++
	r.tools = append(r.tools, tool)
	return `{"content":[{"type":"text","text":"spawn-text"}]}`, nil
}

func (r *countingRunner) RunStructured(ctx context.Context, tool string, payload map[string]any) (cbmexec.Result, error) {
	r.calls++
	r.tools = append(r.tools, tool)
	return cbmexec.Result{Text: "spawn-text", Data: json.RawMessage(`{"kind":"spawn"}`)}, nil
}

// stubResident answers one canned reply, or fails the dial.
type stubResident struct {
	reply resident.Reply
	err   error
	calls int
	got   resident.Request
}

func (s *stubResident) Call(req resident.Request) (resident.Reply, error) {
	s.calls++
	s.got = req
	if s.err != nil {
		return resident.Reply{}, s.err
	}
	return s.reply, nil
}

func residentText(text string) resident.Reply {
	return resident.Reply{Result: json.RawMessage(`{"content":[{"type":"text","text":"` + text + `"}]}`)}
}

const searchGraphCall = `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"search_graph","arguments":{"project":"p","name_pattern":"residentTry"}}}`

// TestResidentAnswersWithoutSpawn is the gate for the whole feature: one engine
// call, answered over the socket, zero spawns.
func TestResidentAnswersWithoutSpawn(t *testing.T) {
	run := &countingRunner{}
	res := &stubResident{reply: residentText("resident-text")}
	resp := serveOne(t, &Server{Profile: ProfileScout, Run: run, Resident: res}, searchGraphCall)
	if resp["error"] != nil {
		t.Fatalf("call failed: %v", resp)
	}
	if run.calls != 0 {
		t.Fatalf("spawned %d time(s) with a resident available: %v", run.calls, run.tools)
	}
	if got := responseText(t, resp); !strings.Contains(got, "resident-text") {
		t.Fatalf("answer did not come from the resident: %q", got)
	}
	if res.got.Tool != "search_graph" {
		t.Fatalf("dialled %q, want search_graph", res.got.Tool)
	}
	if res.got.Args["project"] != "p" {
		t.Fatalf("args not forwarded: %v", res.got.Args)
	}
}

// TestResidentAbsentFallsBackToSpawn covers the common case: the resident is
// opt-in, so most sessions dial, find nothing, and must behave exactly as they
// did before the resident existed.
func TestResidentAbsentFallsBackToSpawn(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
	}{
		{"no socket", resident.ErrNoResident},
		{"dial error", errors.New("dial unix: connection refused")},
		{"resident closed early", errors.New("EOF")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			run := &countingRunner{}
			res := &stubResident{err: tc.err}
			resp := serveOne(t, &Server{Profile: ProfileScout, Run: run, Resident: res}, searchGraphCall)
			if resp["error"] != nil {
				t.Fatalf("a missing resident must not fail the call: %v", resp)
			}
			if run.calls != 1 || run.tools[0] != "search_graph" {
				t.Fatalf("want exactly one spawn of search_graph, got %v", run.tools)
			}
			// search_graph is a structured tool, so what renders is the spawn's
			// structuredContent. The marker is "spawn", and the resident's text
			// must be nowhere in it.
			got := responseText(t, resp)
			if !strings.Contains(got, "spawn") || strings.Contains(got, "resident") {
				t.Fatalf("answer did not come from the spawn: %q", got)
			}
		})
	}
}

// TestResidentRefusalIsNotACleanMiss is the reason the three outcomes stay
// distinct. The engine said no; spawning the identical call again would say no
// again, slower, and folding the refusal into "absent" would report a failed
// engine call as a successful empty result.
func TestResidentRefusalIsNotACleanMiss(t *testing.T) {
	run := &countingRunner{}
	res := &stubResident{reply: resident.Reply{Error: "project not indexed"}}
	resp := serveOne(t, &Server{Profile: ProfileScout, Run: run, Resident: res}, searchGraphCall)
	if resp["error"] == nil {
		t.Fatalf("a refusal must surface as an error, got %v", resp)
	}
	if !strings.Contains(resp["error"].(map[string]any)["message"].(string), "project not indexed") {
		t.Fatalf("refusal message lost: %v", resp["error"])
	}
	if run.calls != 0 {
		t.Fatalf("a refusal must not spawn: %v", run.tools)
	}
}

// TestResidentEmptyAnswerIsAnError: an answer with neither text nor data is not
// an empty result, it is a resident that returned nothing.
func TestResidentEmptyAnswerIsAnError(t *testing.T) {
	run := &countingRunner{}
	res := &stubResident{reply: resident.Reply{}}
	resp := serveOne(t, &Server{Profile: ProfileScout, Run: run, Resident: res}, searchGraphCall)
	if resp["error"] == nil {
		t.Fatalf("an empty resident reply must be an error, got %v", resp)
	}
	if run.calls != 0 {
		t.Fatalf("an empty reply must not spawn: %v", run.tools)
	}
}

// TestNoResidentFieldNeverDials: nil Resident is the pre-resident behaviour,
// byte for byte.
func TestNoResidentFieldNeverDials(t *testing.T) {
	run := &countingRunner{}
	resp := serveOne(t, &Server{Profile: ProfileScout, Run: run}, searchGraphCall)
	if resp["error"] != nil {
		t.Fatalf("call failed: %v", resp)
	}
	if run.calls != 1 {
		t.Fatalf("want one spawn with no resident wired, got %v", run.tools)
	}
}

// TestStructuredReadOverResident covers the second entry point: a structured
// tool must return the resident's structuredContent, not spawn for it.
func TestStructuredReadOverResident(t *testing.T) {
	run := &countingRunner{}
	res := &stubResident{reply: resident.Reply{Result: json.RawMessage(
		`{"structuredContent":{"kind":"resident"},"content":[{"type":"text","text":"resident-text"}]}`)}}
	call := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"check_index_coverage","arguments":{"project":"p"}}}`
	resp := serveOne(t, &Server{Profile: ProfileAnalysis, Run: run, Resident: res}, call)
	if resp["error"] != nil {
		t.Fatalf("call failed: %v", resp)
	}
	if run.calls != 0 {
		t.Fatalf("spawned with a resident available: %v", run.tools)
	}
	if !res.got.Structured {
		t.Fatal("structured request flag not set on the dial")
	}
	if !strings.Contains(responseText(t, resp), "resident") {
		t.Fatalf("resident payload not rendered: %q", responseText(t, resp))
	}
}

// TestResidentOverRealSocket exercises the wire the stub stands in for: a real
// unix socket, the real client, resident.Encode/Decode on both ends.
func TestResidentOverRealSocket(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "resident.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	served := make(chan resident.Request, 1)
	go func() {
		conn, aerr := ln.Accept()
		if aerr != nil {
			return
		}
		defer conn.Close()
		line, _ := bufio.NewReader(conn).ReadBytes('\n')
		var req resident.Request
		if json.Unmarshal(line, &req) != nil {
			return
		}
		served <- req
		_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
		raw, _ := resident.Encode(resident.Reply{Result: json.RawMessage(
			`{"content":[{"type":"text","text":"over-the-wire"}]}`)})
		_, _ = conn.Write(raw)
	}()

	run := &countingRunner{}
	resp := serveOne(t, &Server{
		Profile:  ProfileScout,
		Run:      run,
		Resident: &resident.Client{Addr: sock},
	}, searchGraphCall)
	if resp["error"] != nil {
		t.Fatalf("call failed: %v", resp)
	}
	if run.calls != 0 {
		t.Fatalf("spawned over a live socket: %v", run.tools)
	}
	if got := responseText(t, resp); !strings.Contains(got, "over-the-wire") {
		t.Fatalf("socket answer not rendered: %q", got)
	}
	select {
	case req := <-served:
		if req.Tool != "search_graph" {
			t.Fatalf("socket saw %q", req.Tool)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("resident socket never received the request")
	}
}

// TestBackendIsRecordedPerCall: tk.log has to say which path served a call, or
// a reader chasing a slow tool has to run the clock to find out.
func TestBackendIsRecordedPerCall(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "tk.log")
	run := &countingRunner{}
	s := &Server{Profile: ProfileScout, Run: run, LogPath: logPath,
		Resident: &stubResident{err: resident.ErrNoResident}}
	serveOne(t, s, searchGraphCall)
	s.Resident = &stubResident{reply: residentText("resident-text")}
	serveOne(t, s, searchGraphCall)

	var backends []string
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		var rec map[string]any
		if json.Unmarshal([]byte(line), &rec) != nil {
			continue
		}
		b, _ := rec["backend"].(string)
		backends = append(backends, b)
	}
	if len(backends) != 2 || backends[0] != backendSpawn || backends[1] != backendResident {
		t.Fatalf("want [spawn resident], got %v", backends)
	}
}

// TestBackendNotInheritedByMemoryCall: a per-call field that survives into the
// next record is worse than no field, because it says "resident" about a call
// that never dialled.
func TestBackendNotInheritedByMemoryCall(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "tk.log")
	store := openTestMemStore(t)
	s := &Server{Profile: ProfileMemory, Run: &countingRunner{}, LogPath: logPath,
		Resident: &stubResident{reply: residentText("resident-text")}, Mem: store}
	serveOne(t, s, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"note_toc","arguments":{}}}`)

	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	if strings.Contains(string(data), backendResident) {
		t.Fatalf("memory call inherited a backend: %s", data)
	}
}
