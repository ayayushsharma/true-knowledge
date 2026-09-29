// Command fakemcp is a minimal stdio MCP server used by the child tests.
//
// It is a real program rather than a shell script because the child protocol
// is line-delimited JSON with id-matched replies, and a shell script cannot
// produce that without a JSON parser. Building a real server means the tests
// exercise the framing, the handshake and the id matching against something
// that behaves like the engine rather than against a transcript of what the
// engine used to do.
//
// Behaviour is selected by argv, so one binary covers every case:
//
//	ok        handshake + tools/call, echoing the tool name
//	noisy     ok, plus a notification and a bare log line before each answer
//	crash     answers once, then exits non-zero with a reason on stderr
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

func main() {
	mode := "ok"
	if len(os.Args) > 1 {
		mode = os.Args[1]
	}
	noise := []string{
		`{"jsonrpc":"2.0","method":"notifications/progress","params":{}}`,
		`[info] store opened`,
	}
	in := bufio.NewScanner(os.Stdin)
	in.Buffer(make([]byte, 1<<20), 1<<20)
	answered := 0
	for in.Scan() {
		line := strings.TrimSpace(in.Text())
		if line == "" {
			continue
		}
		// Decoded as a map so the fake needs no struct tags: a tag would have
		// to nest backticks inside this file's own raw string.
		var req map[string]any
		if json.Unmarshal([]byte(line), &req) != nil {
			continue
		}
		id, hasID := req["id"]
		if !hasID {
			continue // notification: no reply
		}
		method, _ := req["method"].(string)
		// Only tools/call counts toward the crash budget: initialize is part
		// of the handshake, so counting it would kill the child before the
		// test ever made a call.
		if method == "tools/call" {
			answered++
			if mode == "crash" && answered >= 2 {
				// The crash reason a reader needs, on the channel it is on.
				fmt.Fprintln(os.Stderr, "fatal: store unreadable")
				os.Exit(1)
			}
		}
		if mode == "noisy" {
			for _, n := range noise {
				os.Stdout.WriteString(n + "\n")
			}
		}
		result := `{}`
		switch method {
		case "initialize":
			result = `{"protocolVersion":"2025-06-18","serverInfo":{"name":"fakemcp"}}`
		case "tools/call":
			result = textResult(callToolName(req))
		default:
			result = `{}`
		}
		out, _ := json.Marshal(map[string]any{
			"jsonrpc": "2.0", "id": id, "result": json.RawMessage(result),
		})
		os.Stdout.Write(append(out, '\n'))
	}
}

// callToolName digs the tool name out of params.name.
func callToolName(req map[string]any) string {
	params, _ := req["params"].(map[string]any)
	name, _ := params["name"].(string)
	return name
}

func textResult(tool string) string {
	b, _ := json.Marshal(map[string]any{
		"content": []map[string]any{{"type": "text", "text": "answer for " + tool}},
	})
	return string(b)
}
