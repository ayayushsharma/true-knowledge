package backends_test

import (
	"testing"

	"github.com/ayayushsharma/true-knowledge/internal/backends"
)

func TestCBMArchiveNames(t *testing.T) {
	b := backends.CBM()
	cases := map[[2]string]string{
		{"linux", "amd64"}:   "codebase-memory-mcp-linux-amd64.tar.gz",
		{"linux", "arm64"}:   "codebase-memory-mcp-linux-arm64.tar.gz",
		{"darwin", "amd64"}:  "codebase-memory-mcp-darwin-amd64.tar.gz",
		{"darwin", "arm64"}:  "codebase-memory-mcp-darwin-arm64.tar.gz",
		{"windows", "amd64"}: "codebase-memory-mcp-windows-amd64.zip",
	}
	for k, want := range cases {
		got, err := b.Archive(k[0], k[1])
		if err != nil || got != want {
			t.Fatalf("%s/%s = %q,%v want %q", k[0], k[1], got, err, want)
		}
	}
	if _, err := b.Archive("windows", "arm64"); err == nil {
		t.Fatal("expected error for windows/arm64")
	}
	if _, err := backends.ByName("nope"); err == nil {
		t.Fatal("expected error for unknown backend")
	}
}

// The base URL is what a pin is fetched from, so the override order is a
// safety property: a per-backend mirror must beat the global one, and both
// must beat GitHub. A pin that can drift to a different host is a pin that
// can be served different bytes.
func TestReleaseBaseOverrideOrder(t *testing.T) {
	b := backends.CBM()
	tag := b.Tag("0.11.0")
	if got, want := b.ReleaseBase(tag), "https://github.com/"+b.Repo+"/releases/download/"+tag; got != want {
		t.Errorf("default = %q, want %q", got, want)
	}
	t.Setenv("TK_RELEASE_BASE_URL", "https://global.example")
	if got, want := b.ReleaseBase(tag), "https://global.example/"+tag; got != want {
		t.Errorf("global = %q, want %q", got, want)
	}
	t.Setenv("TK_RELEASE_BASE_URL_CBM", "https://per-backend.example")
	if got, want := b.ReleaseBase(tag), "https://per-backend.example/"+tag; got != want {
		t.Errorf("per-backend = %q, want %q (it must win over the global)", got, want)
	}
}
