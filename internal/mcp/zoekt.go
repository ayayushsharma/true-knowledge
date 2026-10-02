package mcp

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/ayayushsharma/true-knowledge/internal/zoekttext"
)

// QueryZoekt runs one trigram query against a project's shards and returns
// rendered `file:line: text` output, bounded to limit matches (<=0 = all).
// The library is always linked in — there is no absent-backend case; a
// missing index surfaces as a `tk index` hint instead.
func QueryZoekt(ctx context.Context, shardsDir, pattern, files string, limit int) (string, error) {
	res, err := zoekttext.Search(ctx, shardsDir, pattern, files, limit)
	if err != nil {
		return "", err
	}
	return RenderMatches(res.Matches, false), nil
}

// QueryZoektLive is QueryZoekt plus per-hit live-worktree reconcile (lines
// re-sliced from disk bytes under root). Same semantics otherwise.
func QueryZoektLive(ctx context.Context, shardsDir, root, pattern, files string, limit int) (string, error) {
	res, err := zoekttext.SearchLive(ctx, shardsDir, root, pattern, files, limit)
	if err != nil {
		return "", err
	}
	return RenderMatches(res.Matches, false), nil
}

// FleetMember is one project in a fleet search, already in walk order.
type FleetMember struct {
	Name   string
	Shards string
	Root   string
	// Live reconciles each hit against the worktree. A project with no
	// resolvable root still answers, from shard bytes.
	Live bool
}

// FleetResult is one fleet search's rendered answer plus the accounting the
// completeness line needs. Every field is a fact about the whole walk, not a
// guess: Searched is the projects actually opened, Skipped the ones that could
// not be, NotSearched the ones the limit never reached, and the counts come
// from zoekt's per-project Stats, so they are true totals even though Matches
// is bounded.
type FleetResult struct {
	Text          string
	Searched      []string
	Skipped       []string // "name (reason)"
	NotSearched   []string
	MatchesTotal  int
	FilesTotal    int
	MatchesShown  int
	FilesShown    int
	Truncated     bool
	LimitReached  bool // the walk stopped early because the limit was spent
	BudgetCutText bool
}

// QueryZoektFleet searches every member in order, one searcher at a time, and
// renders as it goes.
//
// Serial by decision, not by omission. Every searcher sizes its own scheduler to
// GOMAXPROCS, so N concurrent searchers oversubscribe the cores they share, and
// merging by completion order would destroy the deterministic (project, rank)
// order a future cursor would need. Measured: ten small projects cost 7ms.
//
// The limit is fleet-wide, not per project: each member is capped at what
// remains of it, so total matches returned never exceeds limit. Once it is
// spent the walk stops and the untouched tail is reported as NotSearched rather
// than silently absent — those projects were never opened, so nothing is known
// about their content.
//
// Rendering streams: each member's group is formatted and appended before the
// next search starts, so peak heap is the largest single member, not the fleet.
func QueryZoektFleet(ctx context.Context, members []FleetMember, pattern, files string, limit int) (FleetResult, error) {
	out := FleetResult{}
	remaining := limit
	for i, m := range members {
		if limit > 0 && remaining <= 0 {
			out.NotSearched = tailNames(members[i:])
			out.LimitReached = true
			break
		}
		// Scoped closure: a defer in the loop body would run at function exit,
		// holding N fsnotify watchers open in the long-lived resident child.
		res, err := searchMember(ctx, m, pattern, files, remaining)
		if err != nil {
			// Fail-open. One unsearchable project names itself and the fleet
			// still answers from the rest; a fleet that searched nothing says so
			// in Skipped rather than reporting an empty result as an absence.
			out.Skipped = append(out.Skipped, fmt.Sprintf("%s (%s)", m.Name, firstLineErr(err)))
			continue
		}
		out.Searched = append(out.Searched, m.Name)
		out.MatchesTotal += res.Total
		out.FilesTotal += res.Files
		out.MatchesShown += len(res.Matches)
		out.FilesShown += distinctFiles(res.Matches)
		out.Truncated = out.Truncated || res.HasMore
		if limit > 0 {
			remaining -= len(res.Matches)
		}
		// A member with no hits renders nothing. Its "(no matches)" sentinel
		// would read as a verdict on the whole fleet, which is the one claim a
		// per-member result cannot support.
		if len(res.Matches) == 0 {
			continue
		}
		// RenderMatches trims its own trailing newline, so members must be
		// re-joined with one: concatenating them directly runs the last hit of
		// one project into the first header of the next.
		if out.Text != "" {
			out.Text += "\n"
		}
		out.Text += RenderMatches(res.Matches, true)
	}
	if limit > 0 && out.MatchesTotal > out.MatchesShown {
		out.Truncated = true
	}
	return out, nil
}

func searchMember(ctx context.Context, m FleetMember, pattern, files string, limit int) (zoekttext.Result, error) {
	if m.Live {
		return zoekttext.SearchLive(ctx, m.Shards, m.Root, pattern, files, limit)
	}
	return zoekttext.Search(ctx, m.Shards, pattern, files, limit)
}

// Completeness renders the "[source-search: ...]" line that states what a result
// covers and what it withheld. It is a scope and completeness statement, not a
// coverage claim: absence still needs the per-project freshness evidence it had
// before, and the two must not be read as the same thing.
//
// Three withheld states are named separately, because collapsing them is how a
// caller reads "truncated" as "nothing else matched": truncated (matches exist,
// the limit hid them), skipped (a project that could not be searched), and not
// searched (a project the walk never reached, so nothing is known about it).
//
// Counts are zoekt's own Stats, which the display caps do not touch, so a
// truncated search reports real totals rather than the size of its own window.
// MatchesTotal counts line fragments, not lines, and says so.
func (r FleetResult) Completeness(budgetCutText bool) string {
	if len(r.Searched) == 0 && len(r.Skipped) == 0 && len(r.NotSearched) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("[source-search: ")
	if r.MatchesTotal == 0 && r.MatchesShown == 0 {
		b.WriteString("no matches")
	} else {
		fmt.Fprintf(&b, "%s in %s", plural(r.MatchesTotal, "match", "matches"), plural(r.FilesTotal, "file", "files"))
	}
	switch len(r.Searched) {
	case 0:
	case 1:
		b.WriteString(" in " + r.Searched[0])
	default:
		b.WriteString(" across " + strconv.Itoa(len(r.Searched)) + " projects (" + strings.Join(r.Searched, ", ") + ")")
	}
	if r.MatchesTotal > r.MatchesShown || r.Truncated {
		if r.MatchesTotal > 0 {
			fmt.Fprintf(&b, "; returned %s from %s",
				plural(r.MatchesShown, "match", "matches"), plural(r.FilesShown, "file", "files"))
		}
		b.WriteString("; truncated — raise --limit or narrow --files")
	} else if budgetCutText {
		b.WriteString("; returned all")
		b.WriteString(" (budget-truncated; raise --budget)")
	}
	if len(r.Skipped) > 0 {
		b.WriteString("; skipped: " + strings.Join(r.Skipped, ", "))
	}
	if len(r.NotSearched) > 0 {
		b.WriteString("; not searched: " + strings.Join(r.NotSearched, ", ") + " (limit)")
	}
	b.WriteString("]")
	return b.String() + "\n"
}

func plural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return strconv.Itoa(n) + " " + many
}

func tailNames(members []FleetMember) []string {
	out := make([]string, 0, len(members))
	for _, m := range members {
		out = append(out, m.Name)
	}
	return out
}

// distinctFiles counts files among rendered hits, so FilesShown is files
// actually shown rather than files found. Res.Files is grouped per file, so
// this is a run count over adjacent equal names.
func distinctFiles(matches []zoekttext.Match) int {
	n := 0
	for i := 0; i < len(matches); i++ {
		if i == 0 || matches[i].File != matches[i-1].File {
			n++
		}
	}
	return n
}

func firstLineErr(err error) string {
	s := err.Error()
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// RenderMatches formats library matches as human lines.
//
// grouped=false is the single-project face and is byte-stable: `file:line: text`
// with no prefix. grouped=true is the fleet face, where a hit is prefixed with
// the repository it came from and grouped under a per-file header carrying that
// file's match count. The grouping restores what zoekt already computed and tk
// was discarding: res.Files arrives ranked and grouped per file, so twenty hits
// in one function and twenty hits across twenty files can be told apart. That
// distinction is what tells a caller to drill into a file or broaden the query.
//
// A single-member group renders without a header, because with one file the
// count is always the hit count and the header carries no information.
func RenderMatches(matches []zoekttext.Match, grouped bool) string {
	var b strings.Builder
	if !grouped {
		for _, m := range matches {
			writeMatch(&b, m, false)
		}
		return finishMatches(b.String())
	}
	for i := 0; i < len(matches); {
		j := i
		for j < len(matches) && matches[j].File == matches[i].File {
			j++
		}
		if len(matches) > 1 {
			fmt.Fprintf(&b, "%s (%d match", matches[i].File, j-i)
			if j-i != 1 {
				b.WriteString("es")
			}
			b.WriteString(")\n")
		}
		for _, m := range matches[i:j] {
			writeMatch(&b, m, true)
		}
		i = j
	}
	return finishMatches(b.String())
}

func writeMatch(b *strings.Builder, m zoekttext.Match, grouped bool) {
	file := m.File
	if grouped && m.Repo != "" {
		file = m.Repo + ":" + m.File
	}
	fmt.Fprintf(b, "%s:%d: %s\n", file, m.Line, strings.TrimRight(m.Text, "\n"))
}

func finishMatches(s string) string {
	s = strings.TrimRight(s, "\n")
	if s == "" {
		return "(no matches)"
	}
	return s
}
