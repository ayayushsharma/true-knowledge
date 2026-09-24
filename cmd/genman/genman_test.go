package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// genman output must be byte-for-byte deterministic (committed artifacts)
// and must not carry cobra's auto-generation stamp.
func TestGenManDeterministicNoAutogenTag(t *testing.T) {
	a := t.TempDir()
	b := t.TempDir()
	if err := run(a); err != nil {
		t.Fatalf("run(a): %v", err)
	}
	if err := run(b); err != nil {
		t.Fatalf("run(b): %v", err)
	}
	files, err := os.ReadDir(a)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) < 20 {
		t.Fatalf("expected the full command tree, got %d pages", len(files))
	}
	seen := map[string]bool{}
	for _, f := range files {
		if f.IsDir() {
			continue
		}
		pa := filepath.Join(a, f.Name())
		pb := filepath.Join(b, f.Name())
		ba, err := os.ReadFile(pa)
		if err != nil {
			t.Fatal(err)
		}
		bb, err := os.ReadFile(pb)
		if err != nil {
			t.Fatal(err)
		}
		if string(ba) != string(bb) {
			t.Fatalf("non-deterministic page %s", f.Name())
		}
		if strings.Contains(string(ba), "Auto generated") {
			t.Fatalf("autogen tag leaked into %s", f.Name())
		}
		seen[f.Name()] = true
	}
	for _, must := range []string{"tk.1", "tk_find.1", "tk_mem.1", "tk_config_set.1"} {
		if !seen[must] {
			t.Fatalf("missing page %s", must)
		}
	}
}
