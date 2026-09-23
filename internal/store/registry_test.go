package store_test

import (
	"path/filepath"
	"testing"

	"github.com/true-knowledge/tk/internal/store"
)

func TestNormalize(t *testing.T) {
	n, err := store.NormalizeName("  my repo! ")
	if err != nil || n != "my-repo" {
		t.Fatalf("got %q %v", n, err)
	}
	if _, err := store.NormalizeName("   "); err == nil {
		t.Fatal("expected empty error")
	}
}

func TestSaveLoad(t *testing.T) {
	p := filepath.Join(t.TempDir(), "tk.json")
	r, err := store.Load(p)
	if err != nil {
		t.Fatal(err)
	}
	r["demo"] = store.Project{Path: "/x", Mode: "moderate"}
	if err := store.Save(p, r); err != nil {
		t.Fatal(err)
	}
	r2, err := store.Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if r2["demo"].Path != "/x" {
		t.Fatal("roundtrip failed")
	}
	if len(r2.Names()) != 1 || r2.Names()[0] != "demo" {
		t.Fatal("names failed")
	}
}
