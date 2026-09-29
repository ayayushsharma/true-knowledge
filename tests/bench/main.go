// Command bench measures where tk's wall-clock time actually goes, layer by
// layer, so a latency complaint is answered with a number instead of an
// opinion. It changes no behavior and ships no tk command: it is a measurement
// tool, read with the same eyes as `tk.log`.
//
//	go run ./tests/bench
//
// Environment (all required unless noted):
//
//	TK_CBM_BIN   the engine to measure, e.g. $(tk config get ...) or the
//	             managed <cache>/bin/codebase-memory-mcp
//	TK_PROJECT   a project name already registered and indexed
//	TK_HOME      the home that project is registered in (for the tk layers)
//	TK_BIN       the tk binary (default ./tk)
//	TK_BENCH_N   repetitions per layer, default 15
//
// CBM_CACHE_DIR, CBM_RUNTIME_DIR and CBM_ALLOWED_ROOT are inherited verbatim
// rather than reconstructed: a layer that ran under a different environment
// than the one tk spawns with would measure a program nobody runs.
//
// The layers, cheapest to most expensive:
//
//	A  cbm --version                     process floor: exec, link, exit
//	B  cbm cli list_projects --format json  + store open, trivial answer
//	C  cbm cli search_graph --format json    + a real graph query
//	D  tk find --json                    what a human or agent actually waits for
//	E  one cbm MCP child, N calls        the proposed fix, measured not assumed
//	F  spawns per D                      read back out of tk.log
//
// A to B is store-open cost. B to C is query cost. If A dominates, the spawn is
// the problem and a persistent child is the fix. If C dominates, a persistent
// child buys nothing and the engine is the thing to fix.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

func main() {
	bin := need("TK_CBM_BIN")
	project := need("TK_PROJECT")
	tkBin := env("TK_BIN", "./tk")
	n := 15
	if v := os.Getenv("TK_BENCH_N"); v != "" {
		if k, err := strconv.Atoi(v); err == nil && k > 0 {
			n = k
		}
	}

	fmt.Printf("engine  %s\n", bin)
	fmt.Printf("project %s\n", project)
	fmt.Printf("n       %d per layer (one warmup discarded)\n\n", n)

	symbol := harvest(tkBin, project)
	fmt.Printf("symbol  %s\n\n", symbol)

	rows := []row{}

	// A: the process floor. --version parses argv and exits, so what is left
	// is exec plus dynamic linking: the cost every spawn pays before CBM has
	// read a single byte of the store.
	rows = append(rows, measure("A  cbm --version", n, func() error {
		return run(bin, "--version")
	}))

	// B: store open. list_projects declares no arguments, so it opens the
	// store and answers without querying it.
	rows = append(rows, measure("B  cbm cli list_projects", n, func() error {
		return run(bin, "cli", "list_projects", "--format", "json")
	}))

	// C: a real graph query, the same tool and dialect tk's read path uses.
	search := []string{"cli", "search_graph", "--project", project, "--name-pattern", symbol, "--limit", "20", "--format", "json"}
	rows = append(rows, measure("C  cbm cli search_graph", n, func() error {
		return run(bin, search...)
	}))

	// D: the whole command. `tk find` is used because its router sends an
	// identifier straight to search_graph, so D and C ask the engine the same
	// question and D - C is tk's own overhead rather than a different query.
	if _, err := os.Stat(tkBin); err != nil {
		fmt.Fprintf(os.Stderr, "\nskip D/F: %s not found (set TK_BIN)\n", tkBin)
	} else {
		rows = append(rows, measure("D  tk find --json", n, func() error {
			return run(tkBin, "find", "--query", symbol, "--project", project, "--json")
		}))
		rows = append(rows, spawnCount(tkBin, project, symbol, bin))
	}

	// E: the fix under test. One child, one handshake, N calls: the shape
	// `tk mcp` would take if it stopped spawning per call. Its per-call figure
	// is the ceiling of what that change can win.
	child, err := newChild(bin)
	if err != nil {
		fmt.Fprintf(os.Stderr, "\nskip E: %v\n", err)
	} else {
		rows = append(rows, child.measure(n, project, symbol))
		child.close()
	}

	report(rows)
}

// row is one measured layer.
type row struct {
	label  string
	median time.Duration
	p95    time.Duration
	min    time.Duration
	note   string
}

// measure runs fn n times after one discarded warmup. The warmup matters: the
// first spawn on a cold page cache is the worst case, and reporting it as the
// typical cost would overstate what a running session actually pays.
func measure(label string, n int, fn func() error) row {
	if err := fn(); err != nil {
		return row{label: label, note: "error: " + firstLine(err.Error())}
	}
	ds := make([]time.Duration, 0, n)
	for i := 0; i < n; i++ {
		t0 := time.Now()
		err := fn()
		ds = append(ds, time.Since(t0))
		if err != nil {
			return row{label: label, note: "error: " + firstLine(err.Error())}
		}
	}
	return stat(label, ds, "")
}

// spawnCount answers "how many engines did one tk command cost".
//
// It reads the trace log, and it also reports the logged count next to what a
// counting wrapper around the engine observed, because the two disagree and
// the disagreement is itself the finding. runEnvelope's exit-0-no-envelope
// fallback (cbmexec/exec.go) re-spawns outside every traced call, so a command
// can cost two engines and log one. A spawn count read only from tk.log is a
// lower bound, and a latency budget built on one under-counts by exactly the
// call nobody recorded.
func spawnCount(tkBin, project, symbol, cbmBin string) row {
	label := "F  spawns per D"
	before := traceRecords()
	_, counted, unwrap := countingWrapper(cbmBin)
	defer unwrap()

	run(tkBin, "find", "--query", symbol, "--project", project, "--json")

	observed := 0
	if b, err := os.ReadFile(counted); err == nil {
		for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
			if strings.TrimSpace(line) != "" {
				observed++
			}
		}
	}

	after := traceRecords()
	logged := 0
	var names []string
	if len(after) > len(before) {
		for _, e := range after[len(after)-1] {
			logged++
			names = append(names, e.Op)
		}
	}
	if logged == 0 {
		return row{label: label, note: "no new tk.log record (is TK_HOME right?)"}
	}
	note := fmt.Sprintf("logged %d (%s)", logged, strings.Join(names, " + "))
	if observed > 0 {
		note += fmt.Sprintf("; observed %d", observed)
		if observed > logged {
			note += fmt.Sprintf("  <-- %d untraced", observed-logged)
		}
	}
	return row{label: label, note: note}
}

// countingWrapper interposes a script that appends one line per real engine
// execution, and points TK_CBM_BIN at it for the duration. It is how the
// ground-truth spawn count is obtained without a debugger and without tk
// cooperating, and it is torn down even if the measured command fails.
func countingWrapper(cbmBin string) (wrapper, countFile string, cleanup func()) {
	dir, err := os.MkdirTemp("", "tk-bench-count-*")
	if err != nil {
		return "", "", func() {}
	}
	countFile = filepath.Join(dir, "spawns")
	script := filepath.Join(dir, "cbm")
	body := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> " + countFile + "\nexec " + cbmBin + " \"$@\"\n"
	if err := os.WriteFile(script, []byte(body), 0o700); err != nil {
		os.RemoveAll(dir)
		return "", "", func() {}
	}
	prev, had := os.LookupEnv("TK_CBM_BIN")
	os.Setenv("TK_CBM_BIN", script)
	return script, countFile, func() {
		if had {
			os.Setenv("TK_CBM_BIN", prev)
		} else {
			os.Unsetenv("TK_CBM_BIN")
		}
		os.RemoveAll(dir)
	}
}

type traceEvent struct {
	Op   string `json:"op"`
	Ms   int64  `json:"ms"`
	Back string `json:"backend"`
}

// traceRecords reads the tail of tk.log as records of backend events. A
// missing or unreadable log yields no records, which the caller reports rather
// than treats as zero spawns.
func traceRecords() [][]traceEvent {
	p := os.Getenv("TK_HOME")
	if p == "" {
		return nil
	}
	f, err := os.Open(p + "/state/logs/tk.log")
	if err != nil {
		f, err = os.Open(p + "/logs/tk.log")
		if err != nil {
			return nil
		}
	}
	defer f.Close()
	var out [][]traceEvent
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		var rec struct {
			Events []traceEvent `json:"events"`
		}
		if json.Unmarshal(sc.Bytes(), &rec) == nil {
			out = append(out, rec.Events)
		}
	}
	return out
}

// child is one long-lived `cbm` MCP stdio server, driven over its pipes. This
// is the transport tk would use instead of a spawn per call, so measuring it
// measures the change rather than a theory of it.
type child struct {
	cmd *exec.Cmd
	in  io.WriteCloser
	out *bufio.Reader
	id  int
}

func newChild(bin string) (*child, error) {
	cmd := exec.Command(bin)
	cmd.Env = cbmEnv()
	in, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start cbm MCP child: %w", err)
	}
	c := &child{cmd: cmd, in: in, out: bufio.NewReader(out)}
	if err := c.handshake(); err != nil {
		c.close()
		return nil, err
	}
	return c, nil
}

// handshake performs initialize + notifications/initialized. Without it the
// server rejects every call, and the rejection would be measured as engine
// latency rather than as a protocol error.
func (c *child) handshake() error {
	if _, err := c.call("initialize", map[string]any{
		"protocolVersion": "2025-06-18",
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "tk-bench", "version": "0"},
	}); err != nil {
		return fmt.Errorf("initialize: %w", err)
	}
	return c.notify("notifications/initialized", map[string]any{})
}

func (c *child) notify(method string, params map[string]any) error {
	return c.send(map[string]any{"jsonrpc": "2.0", "method": method, "params": params})
}

// call sends one request and reads until the reply carrying its id arrives.
// Server-initiated notifications share stdout, so matching on id is what keeps
// a notification from being counted as an answer.
func (c *child) call(method string, params map[string]any) (json.RawMessage, error) {
	c.id++
	id := c.id
	if err := c.send(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params}); err != nil {
		return nil, err
	}
	for {
		line, err := c.out.ReadBytes('\n')
		if err != nil {
			return nil, err
		}
		var resp struct {
			ID     json.RawMessage `json:"id"`
			Result json.RawMessage `json:"result"`
			Error  json.RawMessage `json:"error"`
		}
		if json.Unmarshal(line, &resp) != nil || len(resp.ID) == 0 {
			continue // a notification or an unparseable line, not our answer
		}
		var got int
		if json.Unmarshal(resp.ID, &got) != nil || got != id {
			continue
		}
		if len(resp.Error) > 0 {
			return nil, fmt.Errorf("%s: %s", method, firstLine(string(resp.Error)))
		}
		return resp.Result, nil
	}
}

func (c *child) send(msg map[string]any) error {
	raw, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	_, err = c.in.Write(raw)
	return err
}

// measure times N tools/call requests inside the one child. The handshake is
// excluded deliberately: it is paid once per session, and including it would
// hide the per-call figure that decides the architecture.
func (c *child) measure(n int, project, symbol string) row {
	label := "E  cbm MCP child (1 spawn)"
	ds := make([]time.Duration, 0, n)
	for i := 0; i < n+1; i++ {
		t0 := time.Now()
		_, err := c.call("tools/call", map[string]any{
			"name":      "search_graph",
			"arguments": map[string]any{"project": project, "name_pattern": symbol, "limit": 20, "format": "json"},
		})
		d := time.Since(t0)
		if err != nil {
			return row{label: label, note: "error: " + firstLine(err.Error())}
		}
		if i > 0 {
			ds = append(ds, d)
		}
	}
	return stat(label, ds, "per call, 1 spawn total")
}

func (c *child) close() {
	if c.in != nil {
		c.in.Close() // EOF is the documented instant exit
	}
	if c.cmd != nil {
		c.cmd.Wait()
	}
}

// harvest finds a symbol that actually resolves, so every layer above is
// timing a hit rather than an absence. An absence costs an extra coverage
// probe on the tk layers, which would make D incomparable to C. Cypher is used
// because it is the one tool that can be asked for a name without already
// knowing one.
func harvest(tkBin, project string) string {
	if env("TK_BENCH_SYMBOL", "") != "" {
		return os.Getenv("TK_BENCH_SYMBOL")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, tkBin, "query", "--cypher",
		"MATCH (f:Function) RETURN f.qualified_name AS qn LIMIT 1",
		"--project", project, "--json")
	cmd.Env = os.Environ()
	b, err := cmd.Output()
	if err != nil {
		fmt.Fprintf(os.Stderr, "warn: could not harvest a symbol (%v); timing whatever TK_BENCH_SYMBOL names\n", err)
		return ".*"
	}
	var env struct {
		Data struct {
			Rows [][]any `json:"rows"`
		} `json:"data"`
	}
	if json.Unmarshal(b, &env) == nil {
		for _, row := range env.Data.Rows {
			if len(row) > 0 {
				if s, ok := row[0].(string); ok && s != "" {
					return s
				}
			}
		}
	}
	return ".*"
}

// cbmEnv mirrors internal/cbmexec.Runner.env. tk exports these on every
// spawn, and CBM resolves its store and runtime dirs from them, so a layer
// that spawns the engine directly without them measures a cold, empty
// database instead of the one tk actually queries.
func cbmEnv() []string {
	e := os.Environ()
	home := env("TK_HOME", "")
	if home == "" {
		return e
	}
	e = append(e,
		"CBM_CACHE_DIR="+filepath.Join(home, "cache"),
		"CBM_RUNTIME_DIR="+filepath.Join(home, "state", "rendezvous"),
	)
	if root := os.Getenv("CBM_ALLOWED_ROOT"); root != "" {
		e = append(e, "CBM_ALLOWED_ROOT="+root)
	}
	return e
}

func run(bin string, args ...string) error {
	cmd := exec.Command(bin, args...)
	cmd.Env = cbmEnv()
	var errb strings.Builder
	cmd.Stderr = &errb
	if err := cmd.Run(); err != nil {
		if msg := strings.TrimSpace(errb.String()); msg != "" {
			return fmt.Errorf("%s", firstLine(msg))
		}
		return err
	}
	return nil
}

func stat(label string, ds []time.Duration, note string) row {
	if len(ds) == 0 {
		return row{label: label, note: "no samples"}
	}
	sort.Slice(ds, func(i, j int) bool { return ds[i] < ds[j] })
	// Nearest-rank p95: with a handful of samples an interpolating percentile
	// invents precision the data does not have.
	p95 := ds[(len(ds)*95+99)/100-1]
	return row{label: label, median: ds[len(ds)/2], p95: p95, min: ds[0], note: note}
}

func report(rows []row) {
	w := 0
	for _, r := range rows {
		if len(r.label) > w {
			w = len(r.label)
		}
	}
	fmt.Printf("%-*s  %9s  %9s  %9s  %s\n", w, "layer", "median", "p95", "min", "note")
	for _, r := range rows {
		if r.note != "" && r.median == 0 {
			fmt.Printf("%-*s  %9s  %9s  %9s  %s\n", w, r.label, "-", "-", "-", r.note)
			continue
		}
		note := r.note
		if note == "" {
			note = "-"
		}
		fmt.Printf("%-*s  %9s  %9s  %9s  %s\n", w, r.label,
			ms(r.median), ms(r.p95), ms(r.min), note)
	}
	fmt.Printf("\nread: A is the per-spawn floor. A->B is store open. B->C is the query.\n")
	fmt.Printf("     D - C is tk's own overhead. E is the per-call cost with one spawn.\n")
	fmt.Printf("     spawn overhead per call ~= D - E. It is what a persistent child removes.\n")
}

func ms(d time.Duration) string {
	if d == 0 {
		return "-"
	}
	if d < time.Millisecond {
		return fmt.Sprintf("%.2fms", float64(d.Microseconds())/1000)
	}
	return fmt.Sprintf("%.1fms", float64(d.Microseconds())/1000)
}

func need(k string) string {
	v := os.Getenv(k)
	if v == "" {
		fmt.Fprintf(os.Stderr, "%s is required\n", k)
		os.Exit(2)
	}
	return v
}

func env(k, fallback string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return fallback
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	if len(s) > 200 {
		return s[:200] + "…"
	}
	return s
}
