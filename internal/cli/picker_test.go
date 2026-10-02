package cli

import (
	"strings"
	"testing"

	"github.com/ayayushsharma/true-knowledge/internal/store"
)

func TestPickerTTYGate(t *testing.T) {
	cases := []struct {
		name   string
		json   bool
		picker bool
		inTTY  bool
		outTTY bool
		wantOn bool
	}{
		{"default interactive", false, true, true, true, true},
		{"--json kills picker even on TTY", true, true, true, true, false},
		{"ui.picker=false disables", false, false, true, true, false},
		{"stdin not a TTY", false, true, false, true, false},
		{"stdout not a TTY", false, true, true, false, false},
		{"both non-TTY", false, true, false, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := pickerTTY(tc.json, tc.picker, tc.inTTY, tc.outTTY); got != tc.wantOn {
				t.Fatalf("pickerTTY = %v, want %v", got, tc.wantOn)
			}
		})
	}
}

func TestPickerItems(t *testing.T) {
	c := testCtx(store.Registry{
		"demo":  {Path: "/a/demo"},
		"other": {Path: "/b/other"},
	})
	labels, names := pickerItems(c)
	if len(labels) != 2 || len(names) != 2 {
		t.Fatalf("len = %d/%d", len(labels), len(names))
	}
	if !strings.HasSuffix(labels[0], "(/b/other)") && !strings.HasSuffix(labels[1], "(/b/other)") {
		t.Fatalf("path hint missing: %q", labels)
	}
	want := c.Reg.Names()
	for i, n := range want {
		if names[i] != n {
			t.Fatalf("name order mismatch: %q vs %q", names, want)
		}
	}
}

// The picker filters as you type, case-insensitively and fuzzily. Without a
// non-nil Searcher promptui builds no input at all (select.go: canSearch) and
// swallows every printable key, which is why "fuzzy picker" was a doc claim
// with no implementation behind it.
func TestFuzzyFilter(t *testing.T) {
	cases := []struct {
		name, query, label string
		want               bool
	}{
		{"empty query matches everything", "", "demo  (/a/demo)", true},
		{"whitespace query matches everything", "   ", "demo  (/a/demo)", true},
		{"exact", "demo", "demo  (/a/demo)", true},
		{"upper case query", "DEMO", "demo  (/a/demo)", true},
		{"mixed case query", "DeMo", "demo  (/a/demo)", true},
		{"upper case label", "demo", "DEMO  (/a/demo)", true},
		{"substring", "mo", "demo  (/a/demo)", true},
		{"subsequence", "dm", "demo  (/a/demo)", true},
		{"subsequence across a separator", "tkk", "true-knowledge  (/w/true-knowledge)", true},
		{"path fragment", "work/api", "api  (/w/work/api)", true},
		{"non-match", "zzz", "demo  (/a/demo)", false},
		{"too long", "demox", "demo  (/a/demo)", false},
		{"order matters", "od", "demo  (/w/zzz)", false},
		{"order respected", "do", "demo  (/w/zzz)", true},
		{"empty label", "demo", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := fuzzyFilter(tc.query, tc.label); got != tc.want {
				t.Fatalf("fuzzyFilter(%q, %q) = %v, want %v", tc.query, tc.label, got, tc.want)
			}
		})
	}
}

// The Searcher promptui is given filters the rendered labels by index, so the
// index it passes has to select the same label pickerItems handed the widget —
// an off-by-one here would filter the wrong project.
func TestPickerSearcherIndexesLabels(t *testing.T) {
	c := testCtx(store.Registry{
		"alpha": {Path: "/w/alpha"},
		"beta":  {Path: "/w/beta"},
	})
	labels, _ := pickerItems(c)
	search := func(query string, i int) bool { return fuzzyFilter(query, labels[i]) }
	if !search("beta", 1) {
		t.Fatal(`search("beta", 1) = false, want true`)
	}
	if search("beta", 0) {
		t.Fatal(`search("beta", 0) = true, want false`)
	}
	if !search("", 0) {
		t.Fatal("an empty query must match every label")
	}
}

// requireProject must keep its exact routing error whenever the picker cannot
// run (zero-value Ctx = ui.picker off + real stdin/stdout are not guaranteed
// TTYs under `go test`, so the picker can never be reached here). The picker
// must never leak into --json or scripted runs.
func TestRequireProjectPickerDisabled(t *testing.T) {
	c := testCtx(store.Registry{
		"demo":  {Path: "/a"},
		"other": {Path: "/b"},
	})
	if c.Cfg.UI.Picker {
		t.Fatal("zero-value Ctx must default ui.picker off for hermetic tests")
	}
	p, err := requireProject(c, "arch", "", false)
	if err == nil || p != "" {
		t.Fatalf("want routing error for a bare invocation, got %q %v", p, err)
	}
	for _, want := range []string{"--project", "--select"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("want %q named in the routing error, got %v", want, err)
		}
	}
	// --select cannot reach a disabled picker either: it hard-fails with the
	// same routing hint rather than opening a menu or falling back.
	if p, err := requireProject(c, "arch", "", true); err == nil || p != "" {
		t.Fatalf("want hard error for --select with the picker disabled, got %q %v", p, err)
	} else if !strings.Contains(err.Error(), "pass --project") {
		t.Fatalf("want routing hint in --select error, got %v", err)
	}
}
