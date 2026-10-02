package progress

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

// Not a terminal is the case that matters most in practice, because it is the
// case a pipeline creates. A carriage-return bar written into a redirected
// stderr is thousands of frames of noise, and the completed steps must remain
// readable without a terminal to interpret them.
func TestNonTerminalStepsArePlainLines(t *testing.T) {
	var sink bytes.Buffer
	r := New(&sink, true) // a bytes.Buffer is not a character device
	r.Total(3)
	r.Step("fetch checksum manifest")
	r.Step("download 340.0 MiB")
	r.Live("mid-transfer 12.0 MiB")
	r.Bytes(1024, 4096)
	r.Bar(0.5, "extracting")
	r.Close()

	got := sink.String()
	if strings.Contains(got, "\r") || strings.Contains(got, "\x1b") {
		t.Fatalf("escape sequences must never reach a non-terminal: %q", got)
	}
	if strings.Contains(got, "mid-transfer") {
		t.Fatalf("an unfinished line is not a report: %q", got)
	}
	for _, want := range []string{"[1/3] fetch checksum manifest", "[2/3] download 340.0 MiB"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in %q", want, got)
		}
	}
}

// The live line exists only on a terminal. Asserting it against a pipe is the
// point: it is what keeps `tk install 2>install.log` readable instead of
// collecting a moving smear.
func TestLiveLinesAreTerminalOnly(t *testing.T) {
	var sink bytes.Buffer
	r := New(&sink, true)
	r.Live("working")
	r.Bytes(100, 200)
	r.Bar(0.25, "unpacking")
	if sink.Len() != 0 {
		t.Fatalf("a non-terminal must receive nothing until a step completes: %q", sink.String())
	}
	r.Step("done")
	if !strings.Contains(sink.String(), "done") {
		t.Fatalf("the step must still land: %q", sink.String())
	}
}

// The record is the durable copy, and it is whole steps only. Ten frames of a
// moving bar in tk.log is noise; the twelve steps the command performed is the
// answer to "what did it do while I waited".
func TestRecordTeesWholeStepsOnly(t *testing.T) {
	var live, rec bytes.Buffer
	r := New(&live, true).SetRecord(&rec)
	r.Live("mid-transfer")
	r.Bytes(1, 2)
	r.Step("download 340.0 MiB")
	r.Step("verify sha256")

	if strings.Contains(rec.String(), "mid-transfer") {
		t.Fatalf("a live frame must not be recorded: %q", rec.String())
	}
	if strings.Count(rec.String(), "\n") != 2 {
		t.Fatalf("want exactly the two completed steps in the record: %q", rec.String())
	}
	if rec.String() != live.String() {
		t.Fatalf("record and live channel must agree:\n live: %q\n  rec: %q", live.String(), rec.String())
	}
}

// tk mcp is why the gate exists: its stderr is captured by the agent client, so
// a step reporter there is a foreign object in someone else's transcript.
func TestDisabledReporterIsInert(t *testing.T) {
	var sink bytes.Buffer
	r := New(&sink, false)
	if r.Enabled() {
		t.Fatal("a disabled reporter must report itself disabled")
	}
	r.Total(2)
	r.Step("x")
	r.Note("y")
	r.Live("z")
	r.Bytes(1, 2)
	r.Bar(0.5, "q")
	r.Close()
	if sink.Len() != 0 {
		t.Fatalf("a disabled reporter wrote %q", sink.String())
	}
}

// Disabled() rather than nil: a call site should never have to nil-check a
// cosmetic feature, and a nil *Reporter would panic on Step.
func TestDisabledIsUsable(t *testing.T) {
	r := Disabled()
	if r.Enabled() {
		t.Fatal("Disabled must be inert")
	}
	r.Step("no panic")
	r.SetRecord(nil)
	if r == nil {
		t.Fatal("SetRecord on a disabled reporter must return a usable reporter")
	}
}

// Total is declared or it is not. A guessed denominator produces "[4/7]" on the
// ninth step, which is worse than no position at all.
func TestUnknownTotalOmitsPosition(t *testing.T) {
	var sink bytes.Buffer
	r := New(&sink, true)
	r.Step("a step")
	if strings.Contains(sink.String(), "/") {
		t.Fatalf("position must be omitted when the total is unknown: %q", sink.String())
	}
}

// The declared total is a promise. Exceeding it prints "[10/9]", which is worse
// than printing nothing, so this is the invariant the whole design turns on.
func TestTotalIsNeverExceeded(t *testing.T) {
	var sink bytes.Buffer
	r := New(&sink, true)
	r.Phase("replace something — currently - at (missing)") // header, unnumbered
	r.Total(3)
	r.Step("a")
	r.Step("b")
	r.Note("a note is not a step")
	r.Step("c")

	lines := strings.Split(strings.TrimRight(sink.String(), "\n"), "\n")
	var last string
	for _, l := range lines {
		if i := strings.Index(l, "/"); i > 0 && strings.HasPrefix(l, "[") {
			if j := strings.Index(l, "]"); j > i {
				last = l[1:j]
			}
		}
	}
	if last != "3/3" {
		t.Fatalf("last numbered step is %q, want 3/3:\n%s", last, sink.String())
	}
	if strings.Contains(sink.String(), "]") && strings.Contains(sink.String(), "[1/4]") {
		t.Fatalf("a note or header consumed a step number:\n%s", sink.String())
	}
	// The header must not be indented: it is a heading, not a detail line.
	if !strings.HasPrefix(lines[0], "replace something") {
		t.Fatalf("a phase must render unindented:\n%s", sink.String())
	}
}

// Byte counts are binary units. Decimal would make a 340 MB archive read as
// 363 MB and disagree with every download manager on earth.
func TestBytesIsBinary(t *testing.T) {
	cases := map[int64]string{
		0:                 "0 B",
		512:               "512 B",
		1024:              "1.0 KiB",
		1536:              "1.5 KiB",
		340 * 1024 * 1024: "340.0 MiB",
	}
	for n, want := range cases {
		if got := Bytes(n); got != want {
			t.Fatalf("Bytes(%d) = %q, want %q", n, got, want)
		}
	}
}

// A rate over 3ms is noise presented as a measurement. Refusing to print it is
// the honest option and the whole reason the threshold exists.
func TestRateRefusesToGuess(t *testing.T) {
	if got := Rate(1024, 3*time.Millisecond); got != "" {
		t.Fatalf("want no rate from a 3ms sample, got %q", got)
	}
	if got := Rate(1<<20, time.Second); got != "1.0 MiB/s" {
		t.Fatalf("Rate = %q, want 1.0 MiB/s", got)
	}
}

// Sub-10ms work reported as a nine-digit nanosecond count is unreadable at a
// glance, which is the only glance this line ever gets.
func TestDurIsReadable(t *testing.T) {
	cases := map[time.Duration]string{
		2 * time.Millisecond:    "<10ms",
		125 * time.Millisecond:  "125ms",
		2500 * time.Millisecond: "2.5s",
		90 * time.Second:        "1m30s",
	}
	for d, want := range cases {
		if got := Dur(d); got != want {
			t.Fatalf("Dur(%s) = %q, want %q", d, got, want)
		}
	}
}

// A server that reports more bytes than it promised must not render a bar past
// its own end, and a division by zero from a missing Content-Length must not
// happen at all.
func TestBarClamps(t *testing.T) {
	if got := Bar(2.0); !strings.Contains(got, "100.0%") {
		t.Fatalf("Bar(2.0) must clamp: %q", got)
	}
	if got := Bar(-1); !strings.Contains(got, "  0.0%") {
		t.Fatalf("Bar(-1) must clamp: %q", got)
	}
	if got := Bar(0); !strings.Contains(got, "  0.0%") {
		t.Fatalf("Bar(0) = %q", got)
	}
}

// Close must be callable on a reporter that never drew anything, and twice.
// A command that returns early through an error path still calls it, and a
// panic there would replace a diagnosis with a crash.
func TestCloseIsIdempotent(t *testing.T) {
	var sink bytes.Buffer
	r := New(&sink, true)
	r.Close()
	r.Close()
	Disabled().Close()
}

// A transfer ticks on its own goroutine. A frame arriving after the command is
// over would redraw a bar under a finished step line, so Close is the signal
// that says stop drawing — not merely "erase the current line".
//
// Forced-terminal, because the frame it guards is a frame a real terminal would
// have shown: against a buffer the live path writes nothing and the assertion
// would pass without Close doing anything.
func TestCloseStopsDrawing(t *testing.T) {
	var sink bytes.Buffer
	r := newWithTTY(&sink)
	if r.Closed() {
		t.Fatal("a fresh reporter must not report itself closed")
	}
	r.Live("a late tick")
	if !strings.Contains(sink.String(), "a late tick") {
		t.Fatalf("a live frame must draw on a terminal: %q", sink.String())
	}
	r.Close()
	before := sink.String()
	r.Live("a later tick")
	r.Bytes(1, 2)
	r.Bar(0.5, "also later")
	if sink.String() != before {
		t.Fatalf("a frame after Close must not draw:\n was %q\n now %q", before, sink.String())
	}
	if !r.Closed() {
		t.Fatal("Close must close the reporter")
	}
}

// The in-place line has to be erased before the next one is drawn, or the tail
// of a longer previous line survives as debris. Close is what erases it, so a
// command that ends mid-transfer does not leave the next prompt glued to a
// half-written bar.
func TestTerminalRedrawErasesThePreviousLine(t *testing.T) {
	var sink bytes.Buffer
	r := newWithTTY(&sink)
	r.Live("short")
	r.Live("a much much longer line")
	// Every frame is preceded by a carriage return plus an erase-line, so the
	// final line cannot be longer than what is on screen.
	frames := strings.Split(sink.String(), "\r\x1b[2K")
	if len(frames) < 2 {
		t.Fatalf("no redraw happened: %q", sink.String())
	}
	if frames[0] != "" {
		t.Fatalf("the first frame must be nothing, got %q", frames[0])
	}
	// A step terminates the line rather than erasing it.
	before := sink.String()
	r.Step("done")
	if !strings.HasSuffix(sink.String(), "done\n") {
		t.Fatalf("a step must end with a newline: %q", sink.String())
	}
	if !strings.Contains(before, "a much much longer line") {
		t.Fatalf("the live line was never drawn: %q", before)
	}
}
