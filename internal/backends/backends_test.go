package backends_test

import (
	"testing"

	"github.com/true-knowledge/tk/internal/backends"
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
