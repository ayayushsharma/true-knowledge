package paths_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ayayushsharma/true-knowledge/internal/paths"
)

func TestResolveTKHomeIsolation(t *testing.T) {
	base := t.TempDir()
	os.Setenv("TK_HOME", filepath.Join(base, "iso"))
	defer os.Unsetenv("TK_HOME")
	p := paths.Resolve("")
	if p.Config != filepath.Join(base, "iso", "config") {
		t.Fatalf("config = %s", p.Config)
	}
	if err := p.Ensure(); err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{p.Config, p.Data, p.Cache, p.State} {
		if _, err := os.Stat(d); err != nil {
			t.Fatalf("missing %s", d)
		}
	}
}

func TestResolveLinuxStyleDefault(t *testing.T) {
	os.Unsetenv("TK_HOME")
	os.Unsetenv("TK_CONFIG_HOME")
	t.Setenv("HOME", t.TempDir())
	p := paths.Resolve("")
	want := filepath.Join(os.Getenv("HOME"), ".config", "true-knowledge")
	if p.Config != want {
		t.Fatalf("config = %s want %s", p.Config, want)
	}
}
