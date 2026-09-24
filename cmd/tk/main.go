package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/ayayushsharma/true-knowledge/internal/cli"
)

func main() {
	// Ctrl-C / SIGTERM cancel the root ctx: long commands (index, install,
	// mcp serve, sync) unwind instead of dying mid-write. Server surfaces
	// already gate on ctx; CLI loops check it between steps.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	g := &cli.Globals{}
	root := cli.NewRoot(g)
	if err := root.ExecuteContext(ctx); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
