package main

import (
	"fmt"
	"os"

	"github.com/true-knowledge/tk/internal/cli"
)

func main() {
	g := &cli.Globals{}
	root := cli.NewRoot(g)
	if err := root.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
