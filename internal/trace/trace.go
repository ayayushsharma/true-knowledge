// Package trace owns tk's unified invocation log: one JSON object per line
// in <state>/logs/tk.log, carrying input AND output plus backend timings.
// It replaces history.jsonl (argv-only): a trace log that can't show what
// was returned is useless for debugging agent sessions.
//
// The file is jq-native by design (no `tk log` command will ever exist —
// tail/grep/jq already read it better):
//
//	tail -n 50 tk.log | jq .
//	jq -c 'select(.exit != 0)' tk.log
//	jq -r '[.ts, (.argv|join(" "))] | @tsv' tk.log
//
// Safety: 0600 files, high-precision secret redaction before write,
// 10MB rotation keeping 2 backups, and every write path best-effort —
// logging can never fail a command.
package trace

import (
	"encoding/json"
	"fmt"
	"os"
	"regexp"
)

// MaxLogBytes triggers rotation; var for tests.
var MaxLogBytes int64 = 10 << 20

// Backups kept beside the live log (tk.log.1, tk.log.2).
const Backups = 2

// Event is one backend operation inside an invocation.
type Event struct {
	Backend string `json:"backend"` // "cbm" | "zoekt"
	Op      string `json:"op"`      // cbm tool name, or "index"|"search"
	Ms      int64  `json:"ms"`
	OK      bool   `json:"ok"`
	// Structured records whether the engine honoured format:"json", i.e.
	// whether the call got structuredContent rather than a rendered tree.
	// One call answering false is a capability answer, not a failure: it is
	// how an operator sees that the installed CBM predates the flag.
	Structured bool   `json:"structured,omitempty"`
	Detail     string `json:"detail,omitempty"` // "matches=3", "updated=true"
	Error      string `json:"error,omitempty"`
}

// redactRes holds high-precision secret patterns ONLY. Generic
// `api_key=`-style patterns are deliberately excluded: they fire on
// legitimate code-search output and would corrupt the trace.
var redactRes = []*regexp.Regexp{
	regexp.MustCompile(`AKIA[0-9A-Z]{16}`),
	regexp.MustCompile(`gh[pous]_[A-Za-z0-9]{20,}|github_pat_[A-Za-z0-9_]{20,}`),
	regexp.MustCompile(`(?i)Bearer\s+[A-Za-z0-9\-._~+/=]+`),
	regexp.MustCompile(`sk-[A-Za-z0-9]{20,}`),
	regexp.MustCompile(`(?s)-----BEGIN [A-Z0-9 ]*PRIVATE KEY-----.*?-----END [A-Z0-9 ]*PRIVATE KEY-----`),
}

// Redact replaces recognized secrets with [REDACTED].
func Redact(s string) string {
	for _, re := range redactRes {
		s = re.ReplaceAllString(s, "[REDACTED]")
	}
	return s
}

// Append writes one record (already JSON-marshalable) to logPath,
// rotating first when over budget. Best-effort: all errors swallowed.
func Append(logPath string, rec map[string]any) {
	raw, err := json.Marshal(rec)
	if err != nil {
		return
	}
	rotate(logPath)
	f, err := openAppend(logPath)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = f.Write(append(raw, '\n'))
}

func openAppend(path string) (*os.File, error) {
	if err := os.MkdirAll(dirOf(path), 0o700); err != nil {
		return nil, err
	}
	return os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
}

func dirOf(path string) string {
	for i := len(path) - 1; i >= 0; i-- {
		if path[i] == '/' {
			return path[:i]
		}
	}
	return "."
}

func rotate(path string) {
	fi, err := os.Stat(path)
	if err != nil || fi.Size() <= MaxLogBytes {
		return
	}
	_ = os.Remove(fmt.Sprintf("%s.%d", path, Backups))
	for i := Backups - 1; i >= 1; i-- {
		_ = os.Rename(fmt.Sprintf("%s.%d", path, i), fmt.Sprintf("%s.%d", path, i+1))
	}
	_ = os.Rename(path, path+".1")
}
