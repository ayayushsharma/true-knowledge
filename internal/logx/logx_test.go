package logx

import (
	"bytes"
	"io"
	"os"
	"strings"
	"testing"
)

const (
	tokDebug = "argv test"
	tokError = "boom"
)

// The one property every other test in this package assumes, asserted the only
// way that means anything: capture the real os.Stdout and prove nothing arrives.
// A log line there corrupts `tk install --json | jq`, and it is the failure
// nobody notices until a script starts consuming a broken envelope.
func TestNeverWritesStdout(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	orig := os.Stdout
	os.Stdout = w
	t.Cleanup(func() { os.Stdout = orig })

	var sink bytes.Buffer
	SetOutput(&sink)
	SetRecord(nil)
	SetLevel(LevelDebug)
	t.Cleanup(func() { SetOutput(discardWriter{}) })

	Debugf("%s", tokDebug)
	Errorf("%s", tokError)
	Infof("msg-info")
	Warnf("msg-warn")
	Scope("scoped").Debugf("msg-scoped")

	// Close the write end so the read below terminates instead of blocking.
	os.Stdout = orig
	_ = w.Close()
	leaked, _ := io.ReadAll(r)
	_ = r.Close()

	if len(leaked) > 0 {
		t.Fatalf("diagnostics reached stdout: %q", leaked)
	}
	if !strings.Contains(sink.String(), tokDebug) || !strings.Contains(sink.String(), tokError) {
		t.Fatalf("the sink must still receive everything: %q", sink.String())
	}
}

// Level is a threshold, not a switch: setting warn must silence debug without
// silencing the failures, or `--quiet` would hide exactly what it exists to
// surface.
func TestLevelThreshold(t *testing.T) {
	cases := []struct {
		level        Level
		wantDebug    bool
		wantInfo     bool
		wantWarn     bool
		wantError    bool
		wantNoOutput bool
	}{
		{LevelOff, false, false, false, false, true},
		{LevelError, false, false, false, true, false},
		{LevelWarn, false, false, true, true, false},
		{LevelInfo, false, true, true, true, false},
		{LevelDebug, true, true, true, true, false},
	}
	for _, tc := range cases {
		var sink bytes.Buffer
		SetOutput(&sink)
		SetRecord(nil)
		SetLevel(tc.level)
		Debugf("msg-debug")
		Infof("msg-info")
		Warnf("msg-warn")
		Errorf("msg-error")
		got := sink.String()
		if tc.wantNoOutput {
			if got != "" {
				t.Fatalf("%v must be silent, got %q", tc.level, got)
			}
			continue
		}
		for _, chk := range []struct {
			level Level
			want  bool
		}{
			{LevelDebug, tc.wantDebug},
			{LevelInfo, tc.wantInfo},
			{LevelWarn, tc.wantWarn},
			{LevelError, tc.wantError},
		} {
			marker := "[tk] " + chk.level.String() + ": msg-" + chk.level.String()
			has := strings.Contains(got, marker)
			if has != chk.want {
				t.Fatalf("%v: %s present=%v want=%v in %q", tc.level, chk.level, has, chk.want, got)
			}
		}
	}
}

// A scope is what makes a 40-second narration readable: without it, every line
// in every subsystem looks identical and the reader has to guess which one is
// talking.
func TestScopeNamesItsLines(t *testing.T) {
	var sink bytes.Buffer
	SetOutput(&sink)
	SetRecord(nil)
	SetLevel(LevelDebug)
	Scope("installer").Debugf("verify sha256")
	if !strings.Contains(sink.String(), "[tk] debug: installer: verify sha256") {
		t.Fatalf("scoped line missing its scope: %q", sink.String())
	}
}

// The redaction has to be here, not only in tk.log. stderr gets pasted into an
// issue tracker; a guard that covers one channel guards nothing.
func TestRedactsBeforeWriting(t *testing.T) {
	var sink bytes.Buffer
	SetOutput(&sink)
	SetRecord(nil)
	SetLevel(LevelDebug)
	Debugf("token sk-abcdefghijklmnopqrstuvwx")
	if strings.Contains(sink.String(), "sk-abcdefghijklmnopqrstuvwx") {
		t.Fatalf("secret leaked to the live channel: %q", sink.String())
	}
	if !strings.Contains(sink.String(), "[REDACTED]") {
		t.Fatalf("want a redaction marker: %q", sink.String())
	}
}

// The record tee is what makes verbosity durable. Without it a narration that
// scrolled past is gone, and "why did that take 40 seconds" has no answer.
func TestSetRecordTeesWholeLines(t *testing.T) {
	var live, rec bytes.Buffer
	SetOutput(&live)
	SetRecord(&rec)
	SetLevel(LevelDebug)
	t.Cleanup(func() { SetRecord(nil) })

	Debugf("spawn cli search_graph")

	if !strings.Contains(live.String(), "spawn cli search_graph") {
		t.Fatalf("live channel missing the line: %q", live.String())
	}
	if !strings.Contains(rec.String(), "spawn cli search_graph") {
		t.Fatalf("record missing the line: %q", rec.String())
	}
	// The record gets the same redacted bytes, not a second formatting path.
	if strings.Contains(rec.String(), "sk-") {
		t.Fatalf("record is not redacted: %q", rec.String())
	}
}

// A level gate that is checked before the argument work has to be able to say
// so, or every call site grows an if.
func TestEnabledMatchesTheGate(t *testing.T) {
	SetLevel(LevelWarn)
	if Enabled(LevelDebug) {
		t.Fatal("debug must be disabled at warn")
	}
	if !Enabled(LevelWarn) || !Enabled(LevelError) {
		t.Fatal("warn and error must be enabled at warn")
	}
	SetLevel(LevelOff)
	if Enabled(LevelError) {
		t.Fatal("nothing may be enabled at off")
	}
}

// A typo in TK_LOG must not silently produce no logs at all. Off-by-default with
// no explanation is how an operator concludes tk has no instrumentation.
func TestParseLevelRejectsGarbage(t *testing.T) {
	if _, ok := ParseLevel("debugg"); ok {
		t.Fatal("a misspelled level must not parse")
	}
	if l, ok := ParseLevel(" DEBUG "); !ok || l != LevelDebug {
		t.Fatalf("case and padding are tolerated: %v %v", l, ok)
	}
}

// Flags beat the environment. A flag on the command line is the more specific
// statement of what this invocation wants.
func TestConfigureFlagBeatsEnv(t *testing.T) {
	t.Cleanup(func() { SetLevel(DefaultLevel) })

	t.Setenv("TK_LOG", "off")
	Configure(true, false)
	if GetLevel() != LevelDebug {
		t.Fatalf("--verbose must override TK_LOG=off, got %v", GetLevel())
	}

	t.Setenv("TK_LOG", "debug")
	Configure(false, true)
	if GetLevel() != LevelError {
		t.Fatalf("--quiet must cap TK_LOG=debug at error, got %v", GetLevel())
	}

	t.Setenv("TK_LOG", "info")
	Configure(false, false)
	if GetLevel() != LevelInfo {
		t.Fatalf("TK_LOG alone must apply, got %v", GetLevel())
	}
}

type discardWriter struct{}

func (discardWriter) Write(p []byte) (int, error) { return len(p), nil }
