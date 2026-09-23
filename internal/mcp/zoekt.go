package mcp

import (
	"context"
	"fmt"
	"strings"

	"github.com/true-knowledge/tk/internal/zoekttext"
)

// QueryZoekt runs one trigram query against a project's shards and returns
// rendered `file:line: text` output, bounded to limit matches (<=0 = all).
// The library is always linked in — there is no absent-backend case; a
// missing index surfaces as a `tk index` hint instead.
func QueryZoekt(ctx context.Context, shardsDir, pattern, files string, limit int) (string, error) {
	matches, err := zoekttext.Search(shardsDir, pattern, files, limit)
	if err != nil {
		return "", err
	}
	return RenderMatches(matches), nil
}

// RenderMatches formats library matches as human lines.
func RenderMatches(matches []zoekttext.Match) string {
	var b strings.Builder
	for _, m := range matches {
		fmt.Fprintf(&b, "%s:%d: %s\n", m.File, m.Line, strings.TrimRight(m.Text, "\n"))
	}
	s := strings.TrimRight(b.String(), "\n")
	if s == "" {
		return "(no matches)"
	}
	return s
}
