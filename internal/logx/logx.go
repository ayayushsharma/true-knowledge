// Package logx is tk's live diagnostic channel: leveled narration on stderr,
// never on stdout.
//
// The split is the whole point. stdout is a data channel — `tk install --json |
// jq .ok`, `tk status --json | grep demo`, `tk note toc > toc.md` — so nothing
// that is not the answer is allowed to write there. stderr is the channel a
// human watches and a CI log keeps: progress, warnings, debug narration, and the
// engine's own refusals. A user who pipes stdout to a file and forgets stderr
// gets the answer; a user who watches the terminal gets the story.
//
// Two sinks, two jobs, and they are not interchangeable:
//
//   - tk.log  (<state>/logs/tk.log, via internal/trace) is the RECORD. It
//     survives the command, carries argv, timings, and the engine payloads, and
//     is what you read when answering "what happened at 03:14".
//   - stderr   (here) is the LIVE VIEW. It is gone when the command ends and
//     exists so the person watching knows it is still working.
//
// Duplicating the record onto stderr would put engine payloads in front of a
// human waiting for a download. Not duplicating the narration into tk.log would
// leave "why did it take 40s" unanswerable. So this package narrates and
// internal/trace records, and neither one is asked to do the other's job.
//
// Level comes from TK_LOG (off|error|warn|info|debug) and the persistent
// --verbose/--quiet flags. The default is warn, because a degraded install that
// says nothing until the final table is exactly the failure this exists to
// prevent.
//
// Every write is best-effort and passes through the same secret redaction as
// the trace log: stderr is pasted into issues, and a redactor that only guards
// one of the two channels guards nothing.
package logx

import (
	"fmt"
	"io"
	"os"
	"strings"
	"sync"

	"github.com/ayayushsharma/true-knowledge/internal/trace"
)

// Level is a diagnostic severity. Ordering is ascending, so Enabled is a
// simple comparison and a new level in the middle cannot silently reorder it.
type Level int

const (
	LevelOff Level = iota
	LevelError
	LevelWarn
	LevelInfo
	LevelDebug
)

func (l Level) String() string {
	switch l {
	case LevelError:
		return "error"
	case LevelWarn:
		return "warn"
	case LevelInfo:
		return "info"
	case LevelDebug:
		return "debug"
	default:
		return "off"
	}
}

// ParseLevel resolves a TK_LOG value. An unrecognised value is off with a
// warning rather than a silent default: a typo in TK_LOG=debugg must not
// quietly leave the operator with no logs and no explanation.
func ParseLevel(s string) (Level, bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "off", "none", "silent", "0":
		return LevelOff, true
	case "error", "err", "1":
		return LevelError, true
	case "warn", "warning", "2":
		return LevelWarn, true
	case "info", "3":
		return LevelInfo, true
	case "debug", "trace", "verbose", "4":
		return LevelDebug, true
	default:
		return LevelOff, false
	}
}

// DefaultLevel is warn. Errors are already rendered by the command's own
// envelope, so the floor is the class that would otherwise be invisible: a
// fallback taken, a skipped step, a degraded path that still exits 0.
const DefaultLevel = LevelWarn

var (
	mu     sync.Mutex
	level            = DefaultLevel
	out    io.Writer = os.Stderr
	record io.Writer
)

// SetOutput redirects the channel. Tests use it; production never does, which
// is the property that keeps stdout out of this package's reach.
func SetOutput(w io.Writer) {
	mu.Lock()
	defer mu.Unlock()
	out = w
}

// SetRecord tees every emitted line into w — the invocation's tk.log diag
// buffer.
//
// This is what makes verbosity worth having. A narration that exists only on a
// terminal is gone when the user scrolls back, so "why did that take 40 seconds"
// would have no answer a week later. Tee'd, it is in the record beside the
// timings that explain it. A nil record is the normal case and costs one branch.
func SetRecord(w io.Writer) {
	mu.Lock()
	defer mu.Unlock()
	record = w
}

// RecordSet reports whether a record sink is installed. Exposed so a caller that
// decides whether to install one can assert the decision took effect — a
// long-lived process must not, and the consequence of getting it wrong is a
// buffer that grows silently for the length of a session.
func RecordSet() bool {
	mu.Lock()
	defer mu.Unlock()
	return record != nil
}

// SetLevel sets the active threshold.
func SetLevel(l Level) {
	mu.Lock()
	defer mu.Unlock()
	level = l
}

// GetLevel reads the active threshold.
func GetLevel() Level {
	mu.Lock()
	defer mu.Unlock()
	return level
}

// Configure resolves the level from TK_LOG and the persistent flags, flags
// winning. Quiet is the floor (error) and verbose is the ceiling (debug); an
// explicit TK_LOG is overridden by either, because a flag on the command line
// is the more specific statement of intent.
func Configure(verbose, quiet bool) {
	l := DefaultLevel
	if v := strings.TrimSpace(os.Getenv("TK_LOG")); v != "" {
		parsed, ok := ParseLevel(v)
		if !ok {
			Warnf("TK_LOG=%q is not a level (off|error|warn|info|debug); using %s", v, l)
		} else {
			l = parsed
		}
	}
	if quiet && l > LevelError {
		l = LevelError
	}
	if verbose && l < LevelDebug {
		l = LevelDebug
	}
	SetLevel(l)
}

// Enabled reports whether a level would be emitted, so callers can skip
// building an expensive message (a rendered payload, a marshal) that would be
// thrown away.
func Enabled(l Level) bool { return l <= GetLevel() && GetLevel() != LevelOff }

// Logger is a named scope. One per subsystem, created once:
//
//	var log = logx.Scope("installer")
//
// The scope is in the line rather than derived from the call stack because a
// reader watching a 40 MB download needs to know which subsystem is talking
// without a file:line column they would have to look up anyway.
type Logger struct{ scope string }

// Scope returns a logger that tags its lines with name.
func Scope(name string) *Logger { return &Logger{scope: name} }

func (l *Logger) Debugf(format string, args ...any) { l.emit(LevelDebug, format, args...) }
func (l *Logger) Infof(format string, args ...any)  { l.emit(LevelInfo, format, args...) }
func (l *Logger) Warnf(format string, args ...any)  { l.emit(LevelWarn, format, args...) }
func (l *Logger) Errorf(format string, args ...any) { l.emit(LevelError, format, args...) }

func (l *Logger) emit(lv Level, format string, args ...any) {
	if !Enabled(lv) {
		return
	}
	msg := fmt.Sprintf(format, args...)
	if l.scope != "" {
		msg = l.scope + ": " + msg
	}
	write(lv, msg)
}

// Package-level entry points for call sites that are already self-describing
// (a command verb, a tool name). Anything deeper uses Scope.
func Debugf(format string, args ...any) { emit(LevelDebug, format, args...) }
func Infof(format string, args ...any)  { emit(LevelInfo, format, args...) }
func Warnf(format string, args ...any)  { emit(LevelWarn, format, args...) }
func Errorf(format string, args ...any) { emit(LevelError, format, args...) }

func emit(lv Level, format string, args ...any) {
	if !Enabled(lv) {
		return
	}
	write(lv, fmt.Sprintf(format, args...))
}

// write renders one line under the package lock.
//
// The lock is not paranoia: progress rendering and the MCP server both write to
// stderr from their own goroutines, and a partial line from one interleaved into
// the middle of another's is the corrupt-transcript case stderr exists to
// avoid. Failures are dropped — a diagnostic that cannot be written must never
// become the failure it was describing.
func write(lv Level, msg string) {
	mu.Lock()
	defer mu.Unlock()
	if level == LevelOff || lv > level {
		return
	}
	line := fmt.Sprintf("[tk] %s: %s\n", lv, trace.Redact(msg))
	_, _ = io.WriteString(out, line)
	if record != nil {
		_, _ = io.WriteString(record, line)
	}
}
