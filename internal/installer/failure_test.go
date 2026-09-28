package installer

import (
	"strings"
	"testing"
)

func TestDiagnosticTailKeepsWholeLinesAndMarksWhatItDropped(t *testing.T) {
	in := ""
	for i := 0; i < 25; i++ {
		in += "line " + string(rune('a'+i%26)) + "\n"
	}
	got := diagnosticTail(in)
	if len(got) != diagLines+1 {
		t.Fatalf("want %d lines, got %d", diagLines+1, len(got))
	}
	if !strings.HasPrefix(got[0], "(15 earlier line(s) omitted)") {
		t.Fatalf("first line must name what was dropped, got %q", got[0])
	}
	// The tail is the last diagLines lines, in order, whole.
	if got[1] != "line p" {
		t.Fatalf("tail must start at the last %d lines, got %q", diagLines, got[1])
	}
	if got[len(got)-1] != "line y" {
		t.Fatalf("tail must end at the last line, got %q", got[len(got)-1])
	}
}

func TestDiagnosticTailShortOutputIsUntouched(t *testing.T) {
	got := diagnosticTail("first\n\nsecond\n\n")
	if len(got) != 2 || got[0] != "first" || got[1] != "second" {
		t.Fatalf("blank lines dropped, order kept: %q", got)
	}
}

func TestUnpinnedNamesTheVersionItSaw(t *testing.T) {
	// tk produces this error itself, so the reason has to carry the version
	// that was actually observed. A candidate that answers nothing at all is
	// a different failure from one that answers a different version.
	e := unpinned(cbmBackend(), "0.11.0", "no version", "the downloaded binary reports no version")
	if e.Err != errNoPin {
		t.Fatalf("want the errNoPin sentinel so errors.Is works, got %v", e.Err)
	}
	if !contains(e.Error(), "no version") || !contains(e.Error(), "0.11.0") {
		t.Fatalf("reason must name both the seen and pinned versions: %q", e.Error())
	}
}

func TestMismatchedReportsBothDigestsAndNoWrite(t *testing.T) {
	e := mismatched(cbmBackend(), "0.11.0", repeat64("a"), repeat64("b"))
	if !contains(e.Error(), "checksum mismatch") {
		t.Fatalf("reason must name the cause: %q", e.Error())
	}
	if !contains(e.Error(), "aaaaaaaaaaaa") || !contains(e.Error(), "bbbbbbbbbbbb") {
		t.Fatalf("both digests must be visible: %q", e.Error())
	}
	if e.Hint == "" {
		t.Fatal("a mirror serving other bytes has a fix; the error must name it")
	}
}
