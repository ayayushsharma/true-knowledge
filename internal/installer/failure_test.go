package installer

import (
	"fmt"
	"strings"
	"testing"
)

// The tail drops whole records and says so. A half-line of a tar or activation
// message is worse than none of it: a reader cannot tell where it was cut, and
// a truncated message is the one thing that must never look complete.
func TestDiagnosticTailKeepsWholeLinesAndMarksWhatItDropped(t *testing.T) {
	var b strings.Builder
	for i := 1; i <= 25; i++ {
		fmt.Fprintf(&b, "line %02d with enough words to be unmistakable\n", i)
	}
	got := diagnosticTail(b.String())

	if len(got) != diagLines+1 {
		t.Fatalf("len = %d, want %d (marker + %d lines)", len(got), diagLines+1, diagLines)
	}
	if !strings.HasPrefix(got[0], "(15 earlier line(s) omitted)") {
		t.Errorf("truncation not marked: %q", got[0])
	}
	if got[1] != "line 16 with enough words to be unmistakable" {
		t.Errorf("tail starts at the wrong record: %q", got[1])
	}
	if got[len(got)-1] != "line 25 with enough words to be unmistakable" {
		t.Errorf("tail lost the last record: %q", got[len(got)-1])
	}
}

func TestDiagnosticTailShortOutputIsUntouched(t *testing.T) {
	got := diagnosticTail("\nerror: one\n\nerror: two\r\n\n")
	want := []string{"error: one", "error: two"}
	if len(got) != len(want) {
		t.Fatalf("got %q, want %q", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("line %d = %q, want %q", i, got[i], want[i])
		}
	}
}

// The hint only ever adds a line, so a word CBM rewords costs the hint and never
// the diagnosis. Assert that independence: the same prose, with and without the
// hint, must carry the same reason.
func TestDaemonHintIsAdditive(t *testing.T) {
	detail := []string{"error: activation could not reserve exclusive access"}
	e := &Error{Reason: "the backend's installer failed", Detail: detail, Hint: daemonHint(detail)}
	plain := strings.SplitN(e.Error(), "\n", 2)[0]
	if plain != "the backend's installer failed" {
		t.Errorf("hint leaked into the headline: %q", plain)
	}
	if !strings.Contains(e.Error(), "tk daemon stop") {
		t.Errorf("hint missing: %q", e.Error())
	}
}

func TestDaemonHintOnlyForCoordinationWords(t *testing.T) {
	for _, line := range []string{
		"tar: install.sh: Cannot write: Disk quota exceeded",
		"error: CHECKSUM MISMATCH — download may be corrupted!",
		"error: release archive does not match the exact member set",
	} {
		if h := daemonHint([]string{line}); h != "" {
			t.Errorf("hint %q on unrelated output %q", h, line)
		}
	}
	for _, line := range []string{
		"error: activation could not reserve exclusive access; no activation was committed.",
		"another cohort holds the lifetime lock",
		"error: the codebase-memory-mcp daemon is running an incompatible activation",
	} {
		if daemonHint([]string{line}) == "" {
			t.Errorf("no hint for coordination output %q", line)
		}
	}
}

// CBM states a veto in the same breath as the signal that would otherwise
// trigger the hint. When it says the failure is not a session problem, telling
// the reader to stop a daemon contradicts the only authority in the room, so
// the veto wins — even though the line above it carries a real coordination
// signal. This is the exact shape CBM prints.
func TestDaemonHintYieldsToBackendVeto(t *testing.T) {
	vetoed := []string{
		"error: activation could not reserve exclusive access; no activation was committed.",
		"error: this is NOT a running-session problem — the reservation itself failed.",
	}
	if h := daemonHint(vetoed); h != "" {
		t.Errorf("hint %q despite backend veto in %q", h, vetoed)
	}
	for _, line := range []string{
		"error: not a daemon problem; the session lock file is unwritable",
		"error: this is not a drain failure; the cohort ledger is corrupt",
	} {
		if h := daemonHint([]string{line}); h != "" {
			t.Errorf("hint %q despite backend veto %q", h, line)
		}
	}
	// "cannot" contains "not". A substring test reads it as a veto and silences
	// the hint on a real drain failure — the one case the hint exists for.
	for _, line := range []string{
		"error: cannot drain: the cohort lifetime lock is held by another cohort",
		"error: cannot reserve exclusive access; the daemon holds the activation",
	} {
		if daemonHint([]string{line}) == "" {
			t.Errorf("no hint for coordination failure %q", line)
		}
	}
}
