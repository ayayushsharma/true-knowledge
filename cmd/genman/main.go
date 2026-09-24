// Command genman renders committed man pages (docs/man/tk*.1) from the tk
// command tree via spf13/cobra/doc. Dev-only build tooling — never part of the
// tk binary. Run with `mise run docs.man` (or `go run ./cmd/genman`).
//
// Output is diff-stable: DisableAutoGenTag suppresses cobra's per-command
// "Auto generated" stamp, the header date is a fixed constant, and Source/
// Manual are pinned. Regenerate deliberately and bump the date with the change.
package main

import (
	"fmt"
	"os"
	"time"

	"github.com/ayayushsharma/true-knowledge/internal/cli"
	"github.com/spf13/cobra/doc"
)

// genDate is pinned so regenerating the committed pages yields identical bytes
// regardless of when generation runs. Bump it when you regenerate on purpose.
var genDate = time.Date(2026, 9, 24, 0, 0, 0, 0, time.UTC)

func run(out string) error {
	root := cli.NewRoot(&cli.Globals{})
	root.DisableAutoGenTag = true
	return doc.GenManTreeFromOpts(root, doc.GenManTreeOptions{
		Path: out,
		Header: &doc.GenManHeader{
			Title:  "true-knowledge",
			Date:   &genDate,
			Source: "true-knowledge",
			Manual: "tk (true-knowledge)",
		},
	})
}

func main() {
	out := "docs/man"
	if len(os.Args) > 1 {
		out = os.Args[1]
	}
	if err := os.MkdirAll(out, 0o755); err != nil {
		fmt.Fprintln(os.Stderr, "genman:", err)
		os.Exit(1)
	}
	if err := run(out); err != nil {
		fmt.Fprintln(os.Stderr, "genman:", err)
		os.Exit(1)
	}
}
