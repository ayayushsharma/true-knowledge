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
	if p, err := requireProject(c, ""); err == nil || p != "" {
		t.Fatalf("want routing error for ambiguous multi-project, got %q %v", p, err)
	} else if !strings.Contains(err.Error(), "pass --project") {
		t.Fatalf("want routing hint in error, got %v", err)
	}
}
