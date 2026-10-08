package cli

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/ayayushsharma/true-knowledge/internal/gitx"
	"github.com/ayayushsharma/true-knowledge/internal/zoekttext"
	"github.com/spf13/cobra"
)

// TestZoektTextQueryQualifiesWhatTheIndexAnswers pins the gate between the two
// text backends. It accepts every regular expression the index answers with the
// engine's meaning — metacharacters included, anchors included — because
// internal/zoekttext proves that equivalence hit for hit against RE2. It
// refuses exactly three things: multi-token queries, which zoekt ANDs per file
// rather than reading as a phrase, the query-language prefixes, and the three
// constructs measured to answer differently (`\z`, `\A`, `(?s)`).
func TestZoektTextQueryQualifiesWhatTheIndexAnswers(t *testing.T) {
	qualifies := []string{
		"internal/cli",
		"internal/mcp",
		"cmd/mcp-server",
		"internal/cli/query.go", // a path: dots are regex, and the index answers it
		"https://example.com/x", // likewise
		"tsconfig.json",
		"retry.*backoff",   // dot-star
		`retry\.*backoff`,  // escaped star, then any
		`retry\.\*backoff`, // both metacharacters escaped
		`id=[0-9]+`,        // character class with plus
		`\d+`,              // class alone
		`(Widget|Retry)`,   // alternation
		"^func",            // start anchor
		"Widget$",          // end anchor
		`\bHandler\b`,      // word boundaries
		`\Qa.b*c\E`,        // quoted: the escapes inside are text
		`\Qa\\b\\z\E`,      // anchors inside a quote are text, so it qualifies
		"ProcessOrder()",
		"orElse", // a word that merely starts with a reserved one
		"andromeda",
	}
	for _, in := range qualifies {
		got, ok := zoektTextQuery(in)
		if !ok {
			t.Errorf("zoektTextQuery(%q) refused a pattern the index answers with the engine's meaning", in)
		}
		if got != in {
			t.Errorf("zoektTextQuery(%q) = %q, want it unchanged", in, got)
		}
	}
	refused := []struct {
		in  string
		why string
	}{
		{"internal/cli query.go", "two tokens: zoekt ANDs them per file, the caller wrote a path"},
		{"retry backoff", "a phrase search, which is a different question"},
		{"file:svc.go", "zoekt would filter to the file instead of searching for it"},
		{"repo:alpha", "zoekt would drop the content constraint entirely"},
		{"case:yes", "operator prefix"},
		{"lang:go", "operator prefix"},
		{"or", "reserved combining word"},
		{"not", "reserved combining word"},
		{`backoff\z`, `\z answers differently: it drops the branch entirely`},
		{`Widget$|^retry\z`, `\z answers differently, even inside an alternation`},
		{`\Aretry`, `\A answers differently: it is read as start-of-line`},
		{"(?i)WIDGET", "fold flag searches the uppercased text sensitively"},
		{"(?i)widget", "same, against lowercase text"},
		{"(?i:widget)", "scoped spelling diverges the same way"},
		{"(?-i)widget", "the disable spelling is ignored, so the fold stays on"},
		{"(?s)retry.*backoff", "dotall degrades to a near-match-everything filter"},
		{"(?is)retry.*backoff", "same flag inside a multi-flag group"},
		{"(?s:retry.*)", "same flag in a scoped group"},
	}
	for _, tc := range refused {
		if got, ok := zoektTextQuery(tc.in); ok {
			t.Errorf("zoektTextQuery(%q) = %q, want refusal: %s", tc.in, got, tc.why)
		}
	}
}

// findCtx wires one registered git repo. c.Run is left nil, which is the
// strongest available statement that this route needs no engine: a nil
// *cbmexec.Runner panics on first use, so any code path that reached for one
// fails the test instead of quietly passing. The spawn count is proven for real
// in the e2e suite.
func findCtx(t *testing.T, file, body string) *Ctx {
	t.Helper()
	c := fleetCtx(t, 1)
	commitFile(t, c.Reg["alpha"].Path, file, body)
	c.Run = nil
	return c
}

// commitFile adds a second commit to an already-initialized repo, so the
// zoekt index has to delta forward and the result is not just the file
// fleetCtx happened to commit.
func commitFile(t *testing.T, dir, file, body string) {
	t.Helper()
	initRepoWithFile(t, dir, file, body)
}

func findText(t *testing.T, c *Ctx, proj, query string, limit int) (string, map[string]any) {
	t.Helper()
	cmd := &cobra.Command{}
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetContext(context.Background())
	text, fields, err := c.findTextRead(cmd, proj, query, limit)
	if err != nil {
		t.Fatalf("findText(%q): %v", query, err)
	}
	return text, fields
}

// TestFindTextAnswersFromTkIndex: a literal pattern hits tk's own index, and
// the engine is never consulted — c.Run is nil, so a call would panic.
func TestFindTextAnswersFromTkIndex(t *testing.T) {
	c := findCtx(t, "notes.md", "the route lives in internal/cli/query.go\n")
	text, _ := findText(t, c, "alpha", "internal/cli/query.go", 20)
	if !strings.Contains(text, "notes.md") {
		t.Fatalf("want the hit from tk's index, got:\n%s", text)
	}
}

// TestFindTextEmptyStatesItsScope: an empty answer must prove it looked. This
// is the verdict the engine's check_index_coverage probe used to supply at the
// cost of a second round trip.
func TestFindTextEmptyStatesItsScope(t *testing.T) {
	c := findCtx(t, "notes.md", "nothing interesting here\n")
	text, _ := findText(t, c, "alpha", "no/such/path-anywhere", 20)
	if !strings.Contains(text, "0 matches in the tk text index") {
		t.Fatalf("empty result must carry its scope, got:\n%s", text)
	}
	if !strings.Contains(text, "HEAD@") {
		t.Fatalf("scope must name the snapshot, got:\n%s", text)
	}
	if !strings.Contains(text, "absence verified") {
		t.Fatalf("scope must state the absence is verified, got:\n%s", text)
	}
}

// TestFindTextTruncationIsDisclosed: a cut result says how many were withheld.
// "truncated" as a field the caller may not read is not disclosure.
func TestFindTextTruncationIsDisclosed(t *testing.T) {
	c := fleetCtx(t, 1)
	body := ""
	for i := 0; i < 12; i++ {
		body += "see widget/x for the wiring\n"
	}
	commitFile(t, c.Reg["alpha"].Path, "many.md", body)
	text, _ := findText(t, c, "alpha", "widget/x", 3)
	if !strings.Contains(text, "matches, showing 3") {
		t.Fatalf("truncated result must disclose the cut, got:\n%s", text)
	}
	if !strings.Contains(text, "12 matches") {
		t.Fatalf("truncated result must name the true total, got:\n%s", text)
	}
}

// TestFindTextScopeStaysSilentOnPlainHits: a hit needs no header. Every line
// of narration is a line a slow model pays for on every call.
func TestFindTextScopeStaysSilentOnPlainHits(t *testing.T) {
	res := zoekttext.Result{
		Total:   1,
		Files:   1,
		Matches: []zoekttext.Match{{File: "a.go", Line: 3, Text: "x := 1"}},
	}
	if got := findTextScope(res, "abcdef1234"); got != "" {
		t.Errorf("plain hit should print no scope line, got %q", got)
	}
	// A missing shard is never silent: it is an error upstream of here.
	res.Matches = nil
	got := findTextScope(res, "")
	if !strings.Contains(got, "0 matches in the tk text index") {
		t.Errorf("empty result without a HEAD must still state its scope, got %q", got)
	}
	if strings.Contains(got, "HEAD@") {
		t.Errorf("a plain directory has no HEAD to name, got %q", got)
	}
}

// TestFindTextNamesItsRoute: the --json fields must say which index answered.
// A caller comparing a find result against the engine needs to know it was
// not the engine.
func TestFindTextNamesItsRoute(t *testing.T) {
	c := findCtx(t, "notes.md", "see docs/hello for details\n")
	_, fields := findText(t, c, "alpha", "docs/hello", 20)
	if fields["route"] != "zoekt" || fields["backend"] != "zoekt" {
		t.Fatalf("fields must name the serving index, got %v", fields)
	}
	if fields["query"] != "docs/hello" {
		t.Fatalf("fields must record the caller's query, got %v", fields["query"])
	}
	if _, ok := fields["files_searched"]; ok {
		t.Fatal("zoekt cannot report files searched; publishing that name would be a false claim")
	}
	if fields["matches_found"] == nil || fields["matches_returned"] == nil {
		t.Fatalf("fields must carry match counts, got %v", fields)
	}
}

// TestZoektStaleNoteUsesCallerLabel: a freshness note must name the command the
// caller ran. A note reading "[source-search: ...]" above a `tk find` answer
// sends the reader to the wrong tool when they go looking for the answer.
func TestZoektStaleNoteUsesCallerLabel(t *testing.T) {
	c := fleetCtx(t, 1)
	p := c.Reg["alpha"]
	// A ZoektHead behind the live HEAD is what makes the note fire.
	p.ZoektHead = "0000000"
	note, _, _ := zoektStaleNote(p, false, "find")
	if !strings.Contains(note, "[find: ") || !strings.Contains(note, "live HEAD") {
		t.Fatalf("stale note must carry the caller's label and the drift, got %q", note)
	}
	note, _, _ = zoektStaleNote(p, false, "source-search")
	if !strings.Contains(note, "[source-search: ") {
		t.Fatalf("label must come from the caller, got %q", note)
	}
	// Nothing to say is silence, not a note with zero parts in it.
	fresh := c.Reg["alpha"]
	head := gitx.Head(fresh.Path)
	fresh.ZoektHead = head
	if note, _, _ := zoektStaleNote(fresh, false, "find"); note != "" {
		t.Fatalf("index covering HEAD must stay silent, got %q", note)
	}
}
