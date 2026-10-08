package zoekttext

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// probeLines is the corpus the differential test below matches against. It is
// deliberately full of the things that separate a real regular-expression
// matcher from a substring matcher: anchors, a literal dollar, a literal star,
// escaped metacharacters, character classes, alternation, and words that also
// exist as substrings of other lines. A trigram prefilter with no verification
// pass cannot get every one of these right.
var probeLines = []string{
	"retry.*backoff applies after three failures",
	"retry..backoff works too",
	"retryXbackoff without any separator",
	"Widget$ appears here literally",
	"this line really ends with a dollar$",
	"^a caret starts this line",
	"^another caret line",
	"Widget",
	"const Widget$ = 2",
	"id=42 and port=8080",
	"id=x and port=y",
	"a.*b. Digits: 42",
	"alpha beta",
	"beta",
	"alphabetagamma",
	"UPPER lower MiXeD",
	"the upper bound holds",
	"call Handler() after init",
	"the HandlerFunc adapts it",
	"def retry_handler(attempts: int = 3):",
	"\tretry\\.*backoff", // a literal backslash, then a star
	"",
}

func writeProbeCorpus(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "probe.go"), []byte(strings.Join(probeLines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

// TestSearchAgreesWithRE2 is the evidence for two rules elsewhere in tk:
//
//  1. zoekt answers a content regexp with RE2 semantics, not with "a trigram
//     filter that silently drops what it cannot represent". Candidates are
//     distilled from the pattern, and every candidate is then verified with
//     regexp.MatchString (internal/index/matchtree.go, newRegexpMatchTree), so
//     the answer is the regular expression's answer.
//  2. therefore a single-token pattern may be answered by this index whenever it
//     answers the engine's search_code, and the CLI may route on that basis
//     rather than reserving the index for bare literals.
//
// The test is differential on purpose: expected hits are computed by running
// the same pattern through Go's regexp over the same lines, so it fails if the
// two ever disagree. It covers the shapes once believed to be unsafe —
// anchored, escaped, and character-class patterns.
//
// Two differences are expected and pinned here rather than papered over, and
// both belong to zoekt's query language rather than its regex engine:
//
//   - a two-token query ANDs its terms instead of matching a phrase, which is
//     why zoektTextQuery keeps multi-token patterns off this route;
//   - an empty line has no content to match, so `^.*$` does not report it.
//
// A third difference this test cannot see is corpus, not matcher: zoekt indexes
// the tracked tree at HEAD, so it holds no uncommitted work, while the engine's
// index is whatever `tk index` built. That is disclosed at answer time by
// findTextScope and zoektStaleNote.
func TestSearchAgreesWithRE2(t *testing.T) {
	dir := writeProbeCorpus(t)
	shards := t.TempDir()
	if err := IndexDir(context.Background(), shards, dir, "probe", nil); err != nil {
		t.Fatal(err)
	}
	// anded holds the patterns whose meaning zoekt's parser changes, mapped to
	// the RE2 spelling with the match set it should produce.
	patterns := []string{
		"retry",            // bare literal
		"retry.*backoff",   // dot-star
		`retry\.*backoff`,  // escaped star, then any
		`retry\.\*backoff`, // both metacharacters escaped
		"Widget$",          // end-of-line anchor
		"^retry",           // start-of-line anchor
		`Widget\$`,         // escaped dollar
		`id=[0-9]+`,        // class with plus
		`\d+`,              // class alone
		`a.*b`,             // leading and trailing dots

		"beta",                // substring of alphabetagamma
		"(Widget|Retry)",      // alternation
		"retry_handler",       // snake case identifier
		`\\\*`,                // escaped backslash and star
		"MiXeD",               // exact-case hit under the auto rule
		"mixed",               // lowercase: the auto rule folds, RE2 would not
		`zzz-nothing-matches`, // a real absence
		`[[:alpha:]]+backoff`, // POSIX class
		`\bHandler\b`,         // word boundary, with a HandlerFunc negative
		`\QHandler()\E`,       // quoted metacharacters
		"(?m)^call",           // multiline flag: a no-op over per-line matching
		`[0-9]{2}`,            // counted repetition
		`\s+`,                 // whitespace class
		`(?P<w>Handler)`,      // named group
		"(?U)a+",              // ungreedy flag
		`Widget$|^retry`,      // alternation across shapes
		`^(?:retry|alpha)`,    // non-capturing group
		"^.*$",                // every non-empty line
	}
	for _, pat := range patterns {
		t.Run(pat, func(t *testing.T) {
			want := re2Lines(equivalentPattern(pat))
			res, err := Search(context.Background(), shards, pat, "", 0)
			if err != nil {
				t.Fatalf("search %q: %v", pat, err)
			}
			if !equalSets(resLines(res), want) {
				t.Errorf("zoekt and RE2 disagree for %q\n zoekt: %v\n   RE2: %v", pat, resLines(res), want)
			}
		})
	}
}

// equivalentPattern returns the RE2 spelling whose match set zoekt's query
// language should produce for this pattern.
//
// Two translations, both load-bearing. The empty line has no content, so `^.*$`
// is compared against `^.+$`. And zoekt folds case by default for a pattern
// with no uppercase letter ("auto": case-sensitive only when the pattern
// differs from its own lowercase), so such a pattern is compared against its
// case-folded RE2 spelling. Without this, `mixed` matching `MiXeD` would look
// like a disagreement; with it, the auto rule is pinned as agreement, and the
// `(?i)` rows in the diverges table pin the flag as the thing that breaks it.
func equivalentPattern(pat string) string {
	if pat == "^.*$" {
		return `^.+$`
	}
	// ASCII scope is enough: no agrees-row holds a non-ASCII uppercase letter.
	if !strings.ContainsAny(pat, "ABCDEFGHIJKLMNOPQRSTUVWXYZ") {
		return "(?i)" + pat
	}
	return pat
}

// re2Lines is the ground truth: the lines RE2 says match, 1-based.
func re2Lines(pat string) []string {
	re, err := regexp.Compile(pat)
	if err != nil {
		// zoekt's parser is not RE2's; a pattern RE2 rejects is out of scope for
		// this differential, not a disagreement.
		return nil
	}
	var out []string
	for i, line := range probeLines {
		if re.MatchString(line) {
			out = append(out, fmt.Sprintf("probe.go:%d", i+1))
		}
	}
	sort.Strings(out)
	return out
}

func resLines(res Result) []string {
	var out []string
	for _, m := range res.Matches {
		out = append(out, fmt.Sprintf("%s:%d", m.File, m.Line))
	}
	sort.Strings(out)
	return out
}

// equalSets compares as sets because zoekt returns at most one hit per matching
// line, while RE2 here reports one entry per matching line too, but a line
// matching twice must not count twice.
func equalSets(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	seen := map[string]int{}
	for _, s := range a {
		seen[s]++
	}
	for _, s := range b {
		seen[s]--
	}
	for _, n := range seen {
		if n != 0 {
			return false
		}
	}
	return true
}

// TestUnsupportedConstructsAnswerDifferently pins the three divergences that
// keep a pattern off this index, so a zoekt pin bump that fixes or worsens one
// shows up here instead of silently changing what a search means.
//
// Each case records what RE2 says and what zoekt says. They are not expected to
// agree — that is the point — so the test asserts the *difference*, and the CLI
// gate (zoektTextQuery) keeps these constructs on the engine, which is where
// their meaning comes from.
func TestUnsupportedConstructsAnswerDifferently(t *testing.T) {
	dir := writeProbeCorpus(t)
	shards := t.TempDir()
	if err := IndexDir(context.Background(), shards, dir, "probe", nil); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		pattern string
		reason  string
	}{
		{`backoff\z`, `\z end-of-text is dropped, so a real match is lost`},
		{`\Athis line`, `\A start-of-text is read as start-of-line, so nothing matches`},
		{`\Aret`, `\A narrows to the first line only`},
		{`(?s)retry.*backoff`, `(?s) degrades to a near-match-everything filter`},
		{`(?i)upper`, `(?i) searches the uppercased text sensitively, missing the lowercase line`},
		{`(?i)bound`, `(?i) misses entirely: no uppercase BOUND exists`},
		{`(?i)handler`, `(?i) misses both Handler lines for the same reason`},
		{`(?i:MixeD)`, `the scoped spelling diverges the same way`},
		{`(?-i)upper`, `(?-i) is ignored, so the default fold over-matches UPPER`},
		{`(?i)(?-i)upper`, `cancelling spellings still diverge from RE2`},
		{"alpha beta", "two terms AND per file, not a phrase on one line"},
	}
	for _, tc := range cases {
		t.Run(tc.pattern, func(t *testing.T) {
			res, err := Search(context.Background(), shards, tc.pattern, "", 0)
			if err != nil {
				t.Fatalf("search: %v", err)
			}
			got, want := resLines(res), re2Lines(tc.pattern)
			if equalSets(got, want) {
				t.Fatalf("zoekt now agrees with RE2 for %q (%v) — the CLI gate and this table are stale", tc.pattern, want)
			}
			t.Logf("%s\n   zoekt: %v\n   RE2:   %v", tc.reason, got, want)
		})
	}
}
