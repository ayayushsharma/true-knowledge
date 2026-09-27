package installer

import (
	"errors"
	"fmt"
	"strings"

	"github.com/ayayushsharma/true-knowledge/internal/backends"
)

// diagLines caps how much of the backend's own output tk carries into an
// error. Whole records only: half a tar or activation message is worse than
// none of it, so this counts lines and marks what it dropped.
const diagLines = 10

// errNoPin marks a script that ran to completion and still did not publish the
// pin. There is no exec or filesystem error behind it, which is why it needs
// its own sentinel: the run succeeded and the outcome is still wrong.
var errNoPin = errors.New("the installer script did not publish the pinned binary")

// Error is a backend install that did not reach its pin.
//
// Reason is the one line a table row carries. Detail is the backend's own
// diagnostic, which is what actually names the cause — CBM's exit status is
// "exit status 1" for a full disk, a corrupt archive, and a daemon that
// refused to drain alike. Hint is tk's remediation when the cause is one a
// reader can act on. Err is wrapped so errors.Is/As still reach the exec or
// filesystem failure underneath.
type Error struct {
	Backend string
	Pin     string
	Reason  string
	Detail  []string
	Hint    string
	Err     error
}

func (e *Error) Error() string {
	var b strings.Builder
	b.WriteString(e.Reason)
	for _, d := range e.Detail {
		b.WriteString("\n  ")
		b.WriteString(d)
	}
	if e.Hint != "" {
		b.WriteString("\n  ")
		b.WriteString(e.Hint)
	}
	return b.String()
}

func (e *Error) Unwrap() error { return e.Err }

// scriptFailure reports a non-zero exit from the backend's installer.
func scriptFailure(b backends.Backend, pin string, out output, err error) *Error {
	detail := out.diagnose()
	return &Error{
		Backend: b.Name,
		Pin:     pin,
		Reason:  "the backend's installer failed",
		Detail:  detail,
		Hint:    daemonHint(detail),
		Err:     err,
	}
}

// unpinned reports a script that exited 0 without publishing the pin, which is
// what CBM does when the binary it would replace is owned by mise, Homebrew or
// nix, and when an install is configured but publishes no binary.
func unpinned(b backends.Backend, pin string, out output, reason string, err error) *Error {
	detail := out.diagnose()
	return &Error{
		Backend: b.Name,
		Pin:     pin,
		Reason:  fmt.Sprintf("the installer script exited 0 but %s", reason),
		Detail:  detail,
		Hint:    daemonHint(detail),
		Err:     err,
	}
}

// daemonHint is tk's remediation for the one install failure a reader can
// always clear: a daemon still holding the backend's coordination lock. The
// backend cannot drain a daemon that predates its drain protocol, so it refuses
// rather than guess, and stopping that daemon is the fix.
//
// It matches on the backend's prose, which is brittle by nature. Two things
// keep that from becoming a wrong answer:
//
//   - It only ever adds a line. A word CBM rewords costs the hint, never the
//     diagnosis in Detail.
//   - A backend that says the failure is *not* a session problem wins. CBM
//     prints exactly that when its reservation failed for a reason of its own,
//     and telling that reader to stop a daemon contradicts the only authority
//     in the room.
func daemonHint(detail []string) string {
	// Negations first: they veto every signal below, in any of the words used
	// to state the veto.
	for _, d := range detail {
		if isNotSessionProblem(strings.ToLower(d)) {
			return ""
		}
	}
	for _, d := range detail {
		low := strings.ToLower(d)
		for _, s := range daemonSignals {
			if strings.Contains(low, s) {
				return "if a backend daemon is running, stop it and retry: tk daemon stop"
			}
		}
	}
	return ""
}

// daemonSignals are the phrasings that mean a daemon is the thing to clear.
// They are coordination nouns, not the word "session" on its own: a backend
// refers to a session in plenty of failures a daemon cannot fix.
var daemonSignals = []string{
	"daemon",
	"cohort",
	"lifetime",
	"running session",
	"active session",
	"drain",
	"reserve exclusive",
}

// isNotSessionProblem recognises a refusal that explicitly disclaims being a
// session problem. Matched loosely on purpose — the veto is worth more when it
// over-matches, since a missed hint is recoverable and a wrong one is not.
//
// "not" must be a whole word: "cannot drain" is a drain failure, the exact
// opposite of a veto, and a substring test reads it as one.
func isNotSessionProblem(low string) bool {
	for i := 0; i+3 <= len(low); i++ {
		if low[i:i+3] != "not" {
			continue
		}
		if i > 0 && !isDelim(low[i-1]) {
			continue
		}
		if i+3 < len(low) && !isDelim(low[i+3]) {
			continue
		}
		rest := low[i:]
		for _, w := range []string{"running session", "active session", "session", "daemon", "drain"} {
			if strings.Contains(rest, w) {
				return true
			}
		}
	}
	return false
}

// isDelim reports whether c separates words. Everything else — letters, digits,
// and the punctuation that glues a compound together like "running-session" —
// counts as part of the surrounding word.
func isDelim(c byte) bool {
	return c == ' ' || c == '_' || c == '\t' || c == '\n' || c == '\r' ||
		c == ',' || c == '.' || c == ';' || c == ':' || c == ')' || c == '(' ||
		c == '!' || c == '?' || c == '"' || c == '\''
}

// diagnosticTail is the last non-empty lines of the backend's output. The cause
// is always at the end: the vendor installer runs under set -e, so its own
// error is the last thing it can print.
func diagnosticTail(out string) []string {
	all := make([]string, 0, diagLines+1)
	for _, line := range strings.Split(out, "\n") {
		if s := strings.TrimRight(line, "\r"); strings.TrimSpace(s) != "" {
			all = append(all, s)
		}
	}
	if len(all) <= diagLines {
		return all
	}
	dropped := len(all) - diagLines
	tail := make([]string, 0, diagLines+1)
	tail = append(tail, fmt.Sprintf("(%d earlier line(s) omitted)", dropped))
	return append(tail, all[len(all)-diagLines:]...)
}
