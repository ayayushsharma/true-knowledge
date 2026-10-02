// Package progress renders long-running work as it happens, on stderr.
//
// It exists because the commands that take real time said nothing at all while
// they ran. `tk install` downloaded and unpacked a ~340 MB archive behind one
// silent line; `tk index` shelled into the engine for a minute and looked hung.
// A user who cannot see progress invents a reason to kill the command, and the
// way to see progress without corrupting a pipeline is stderr: stdout keeps
// carrying only the answer.
//
// The rules that make it safe to leave on by default:
//
//   - stderr only. Every byte here is a diagnostic, and stdout is the data
//     channel. `tk install --json 2>/dev/null | jq` must yield a valid envelope.
//   - Never ANSI when stderr is not a terminal. The live line is drawn in place
//     on a TTY and degenerates to one plain line per completed step otherwise,
//     so a CI log or a `2>err.txt` stays readable instead of collecting
//     thousands of carriage returns.
//   - Never per-tick chatter. Only completed steps are emitted unconditionally.
//     Live counters are TTY-only: a byte counter refreshing ten times a second
//     is a signal to a person and pure noise in a file.
//   - Best-effort. No write here returns an error, and none of them can fail a
//     command that would otherwise have succeeded.
//
// A step is a thing that either happened or did not. `Bar` and `Live` are
// furniture for the step currently in flight; they never stand in for one.
package progress

import (
	"fmt"
	"io"
	"os"
	"sync"
	"time"
)

// barWidth is the inner width of the drawn bar. Fixed rather than sized to the
// terminal because the progress channel competes with the command's own output
// for width, and a bar that reflows the whole transcript on every SIGWINCH is
// worse than a bar that is always the same size.
const barWidth = 24

// Reporter renders steps to one writer. The zero value is inert, so a caller
// that never builds one writes nothing.
type Reporter struct {
	mu     sync.Mutex
	w      io.Writer
	record io.Writer
	tty    bool
	total  int
	done   int
	live   bool
	closed bool
}

// Disabled is the reporter that renders nothing. Returned rather than exported
// as a nil sentinel so a call site never has to nil-check before Step().
func Disabled() *Reporter { return &Reporter{} }

// New builds a reporter over w, detecting whether w is a terminal. Pass
// enabled=false for a command whose stderr belongs to someone else — `tk mcp`
// is the case that matters, where stderr is captured by the agent client as
// part of the session record.
func New(w io.Writer, enabled bool) *Reporter {
	if !enabled || w == nil {
		return &Reporter{}
	}
	return &Reporter{w: w, tty: IsTerminal(w)}
}

// NewStderr is New over os.Stderr, which is the only production call site: the
// diagnostic stream is a property of the process, not of a command.
func NewStderr(enabled bool) *Reporter { return New(os.Stderr, enabled) }

// newWithTTY is New with the terminal answer forced, so the in-place rendering
// path is reachable from a test. Every writer a test can hand New is a buffer or
// a file, and every one of those is correctly detected as "not a terminal" — so
// without this seam the erase sequences, the redraw suppression, and the
// terminal/non-terminal divergence would ship untested.
func newWithTTY(w io.Writer) *Reporter {
	return &Reporter{w: w, tty: true}
}

// SetRecord tees whole step lines into w — the invocation's tk.log diag buffer.
//
// Only completed steps reach it, never the in-place live line. A record
// containing ten frames of a moving bar is noise; a record containing the
// twelve steps the command actually performed is the answer to "what did it do
// while I waited". Returns the receiver so it chains onto New.
func (r *Reporter) SetRecord(w io.Writer) *Reporter {
	if r == nil {
		return Disabled()
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.record = w
	return r
}

// Closed reports whether Close has been called. A call site that keeps ticking
// after the command is over consults it so a late frame cannot redraw under a
// finished line.
func (r *Reporter) Closed() bool {
	if !r.Enabled() {
		return true
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.closed
}

// Enabled reports whether anything will be written. Callers use it to skip
// building a message nobody will see.
func (r *Reporter) Enabled() bool { return r != nil && r.w != nil }

// IsTerminal reports whether w is a character device. The lightest TTY check
// available without a dependency; a pipe, a file, and a pipe-to-file are all
// not one, which is exactly the set that must get plain lines.
func IsTerminal(w io.Writer) bool {
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	fi, err := f.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}

// Total declares the size of the run that starts at the next Step, resetting the
// counter.
//
// Declaring a total mid-command is how one command gets two runs with a single
// honest denominator between them: `tk install` opens with an unnumbered header
// and its environment-specific quiesce notes, then hands the numbered pipeline
// to the install package, which knows it is always eight stages. Resetting is
// what makes that safe — a total that did not reset would render the first
// pipeline step as [2/8].
func (r *Reporter) Total(n int) {
	if !r.Enabled() {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.total = n
	r.done = 0
}

// Step reports one completed step. It is the only unconditional output.
func (r *Reporter) Step(format string, args ...any) {
	if !r.Enabled() {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.done++
	msg := fmt.Sprintf(format, args...)
	if pos := r.posLocked(); pos != "" {
		r.emitLocked("[" + pos + "] " + msg)
		return
	}
	r.emitLocked(msg)
}

// Phase reports a labelled heading for the steps that follow.
//
// It is neither a Step nor a Note: nothing is counted, because nothing finished.
// A header that consumed a number would make a fixed pipeline read [2/8] on its
// first line, and a header rendered as a Note would be indented as though it
// belonged to the step above it. `tk install` uses one per backend; the fixed
// eight-stage pipeline underneath it is what the numbers belong to.
func (r *Reporter) Phase(format string, args ...any) {
	if !r.Enabled() {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.emitLocked(fmt.Sprintf(format, args...))
}

// Note reports detail under the current step, indented so it reads as a child
// of the line above rather than as a step of its own — and unnumbered, so an
// environment-dependent line (a daemon that happened to be running) cannot push
// a fixed pipeline's counter off its denominator.
func (r *Reporter) Note(format string, args ...any) {
	if !r.Enabled() {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.emitLocked("       " + fmt.Sprintf(format, args...))
}

// Live draws a single in-place line and returns. On a terminal it replaces the
// previous one; everywhere else it writes nothing, because an unfinished thing
// is not a report and a half-drawn bar in a log is a lie about how far the work
// got.
func (r *Reporter) Live(format string, args ...any) {
	if !r.Enabled() {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.tty {
		return
	}
	r.drawLocked(fmt.Sprintf("%s", fmt.Sprintf(format, args...)))
}

// Bytes renders a transfer position. total may be 0 when the server sent no
// Content-Length, in which case only the transferred count is claimed — never a
// percentage of a denominator that does not exist.
func (r *Reporter) Bytes(done, total int64) {
	if !r.Enabled() {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.tty {
		return
	}
	// A separator, because "531 B[####" reads as a broken count rather than as
	// a count and a bar.
	r.drawLocked(Bytes(done) + " " + Bar(frac(done, total)))
}

// Bar draws a fractional bar with its percentage. frac clamps to [0,1]; a
// server that reports more bytes than it promised must not render a bar past
// its own end.
func (r *Reporter) Bar(frac float64, format string, args ...any) {
	if !r.Enabled() {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.tty {
		return
	}
	r.drawLocked(fmt.Sprintf(format, args...) + " " + Bar(frac))
}

// Close finishes any live line, so a command that ends mid-transfer does not
// leave the terminal with a half-written bar and the next prompt appended to it.
//
// It also closes the reporter for good. A transfer in flight ticks on its own
// goroutine, and one of those ticks arriving after the last step line would
// redraw a bar under a finished command — so Close is the signal that says no
// more drawing, not just "clear the screen". Safe to call more than once, and on
// an inert Reporter.
func (r *Reporter) Close() {
	if !r.Enabled() {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.clearLocked()
	r.closed = true
}

// clearLocked erases the in-place line. On a non-terminal there is nothing in
// flight to erase, so this only has to deal with the TTY case.
func (r *Reporter) clearLocked() {
	if !r.tty || !r.live {
		return
	}
	_, _ = io.WriteString(r.w, "\r\x1b[2K")
	r.live = false
}

// emitLocked ends the live line and prints one final line for the step.
func (r *Reporter) emitLocked(line string) {
	r.clearLocked()
	_, _ = fmt.Fprintln(r.w, line)
	if r.record != nil {
		_, _ = fmt.Fprintln(r.record, line)
	}
	r.live = false
}

// drawLocked replaces the live line. Clearing before writing matters: the new
// text is almost never as long as the old, and without the erase the tail of
// the previous longer line survives as debris.
func (r *Reporter) drawLocked(line string) {
	if r.closed {
		return
	}
	_, _ = io.WriteString(r.w, "\r\x1b[2K"+line)
	r.live = true
}

// posLocked is the "[3/9]" prefix, or "" when the total was never declared.
func (r *Reporter) posLocked() string {
	if r.total <= 0 {
		return ""
	}
	return fmt.Sprintf("%d/%d", r.done, r.total)
}

func frac(done, total int64) float64 {
	if total <= 0 {
		return 0
	}
	f := float64(done) / float64(total)
	switch {
	case f < 0:
		return 0
	case f > 1:
		return 1
	default:
		return f
	}
}

// Bar renders a fixed-width bar. ASCII on purpose: it survives a locale, a
// dumb terminal, a serial console, and a `grep` over a CI log, none of which
// are worth degrading a progress line to improve.
func Bar(frac float64) string {
	if frac < 0 {
		frac = 0
	}
	if frac > 1 {
		frac = 1
	}
	filled := int(frac*barWidth + 0.5)
	return fmt.Sprintf("[%s%s] %5.1f%%", repeat('#', filled), repeat('-', barWidth-filled), frac*100)
}

// Bytes is a binary-unit count. Decimal units would make a 340 MB archive read
// as 363 MB and quietly disagree with every download manager on earth.
func Bytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for v := n / unit; v >= unit && exp < 4; v /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTP"[exp])
}

// Rate is a transfer speed, or "" before there is enough elapsed time to mean
// anything. A rate computed over 3ms is noise presented as a measurement.
func Rate(n int64, d time.Duration) string {
	if d < 50*time.Millisecond {
		return ""
	}
	return Bytes(int64(float64(n)/d.Seconds())) + "/s"
}

// Dur is a duration at a resolution a person reads at a glance. Sub-10ms work
// is reported as "<10ms" rather than as a nine-digit nanosecond count.
func Dur(d time.Duration) string {
	switch {
	case d < 10*time.Millisecond:
		return "<10ms"
	case d < time.Second:
		return fmt.Sprintf("%dms", d.Milliseconds())
	case d < time.Minute:
		return fmt.Sprintf("%.1fs", d.Seconds())
	default:
		return fmt.Sprintf("%dm%02ds", int(d.Minutes()), int(d.Seconds())%60)
	}
}

func repeat(c byte, n int) string {
	if n <= 0 {
		return ""
	}
	b := make([]byte, n)
	for i := range b {
		b[i] = c
	}
	return string(b)
}
