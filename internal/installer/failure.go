package installer

import (
	"errors"
	"fmt"
	"strings"

	"github.com/ayayushsharma/true-knowledge/internal/backends"
)

// diagLines caps how much detail an error carries. Whole records only: half a
// tar or a truncated pty dump is worse than none of it, so this counts lines
// and marks what it dropped.
const diagLines = 10

// errNoPin marks a download that completed and still did not publish the pin.
// There is no network or filesystem error behind it, which is why it needs its
// own sentinel: the fetch succeeded and the outcome is still wrong.
var errNoPin = errors.New("the downloaded binary does not report the pinned version")

// Error is a backend install that did not reach its pin.
//
// tk produces every line of this error itself, so Reason names the cause
// directly rather than deferring to a subprocess diagnostic. Detail carries the
// per-step context, Hint is tk's remediation when the cause is one a reader can
// act on, and Err is wrapped so errors.Is/As still reach the underlying failure.
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

// unpinned reports a candidate that did not report the pin, either at the staged
// path or after publication. A binary that answers a different version is a
// different failure from one that will not answer at all, so the reason names
// the version that was seen.
func unpinned(b backends.Backend, pin, seen, reason string) *Error {
	return &Error{
		Backend: b.Name,
		Pin:     pin,
		Reason:  fmt.Sprintf("install did not reach the pin: %s (pinned %s)", reason, pin),
		Detail:  []string{planLine(b, pin)},
		Hint:    "the pin is compiled into tk; a different version here means the release asset does not match it",
		Err:     errNoPin,
	}
}

// mismatched reports a download whose bytes are not the ones the release
// manifest vouches for. Nothing was written: the checksum is checked before
// the archive is ever opened.
func mismatched(b backends.Backend, pin, want, got string) *Error {
	return &Error{
		Backend: b.Name,
		Pin:     pin,
		Reason:  fmt.Sprintf("checksum mismatch for the %s release asset", b.Display),
		Detail:  []string{"manifest " + short(want) + "…, downloaded " + short(got) + "…"},
		Hint:    "a mirror or proxy is serving different bytes; set TK_RELEASE_BASE_URL to a trusted base, or retry",
		Err:     errNoPin,
	}
}

// planLine is the one context line every tk install error carries: what was
// asked for, and from where.
func planLine(b backends.Backend, pin string) string {
	return fmt.Sprintf("wanted %s %s from %s", b.Display, pin, b.ReleaseBase(b.Tag(pin)))
}

// diagnosticTail is the last non-empty lines of an output stream. The cause is
// always at the end, so this is what a reader needs.
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
