package memory

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func openTestNotes(t *testing.T) *Notes {
	t.Helper()
	n, err := OpenNotes(context.Background(), filepath.Join(t.TempDir(), "notes"))
	if err != nil {
		t.Fatalf("OpenNotes: %v", err)
	}
	t.Cleanup(func() { _ = n.Close() })
	return n
}

func approve(t *testing.T, n *Notes, ctx context.Context, title string) Note {
	t.Helper()
	reviews, err := n.PendingReviews(ctx)
	if err != nil {
		t.Fatalf("PendingReviews: %v", err)
	}
	for _, r := range reviews {
		if r.Title == title {
			nt, err := n.Approve(ctx, r.ID)
			if err != nil {
				t.Fatalf("Approve %q: %v", title, err)
			}
			return nt
		}
	}
	t.Fatalf("no pending review titled %q", title)
	return Note{}
}

func TestNotesReviewGateSearch(t *testing.T) {
	ctx := context.Background()
	n := openTestNotes(t)

	if _, err := n.SaveToReview(ctx, "demo", "auth design", "users log in with OAuth2 against the identity provider"); err != nil {
		t.Fatalf("SaveToReview: %v", err)
	}

	hits, err := n.Search(ctx, "demo", "identity", 10)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(hits) != 0 {
		t.Fatalf("captured note must NOT be searchable before approval: %+v", hits)
	}

	pending, err := n.PendingReviews(ctx)
	if err != nil || len(pending) != 1 {
		t.Fatalf("want 1 pending, got %d err=%v", len(pending), err)
	}
	approve(t, n, ctx, "auth design")

	hits, err = n.Search(ctx, "demo", "identity", 10)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(hits) != 1 || hits[0].Note.Title != "auth design" {
		t.Fatalf("approved note missing from search: %+v", hits)
	}
	file := n.notesDir("demo") + "/" + hits[0].Note.File
	if _, err := os.Stat(file); err != nil {
		t.Fatalf("approved note file missing: %v", err)
	}
}

func TestNotesSearchRankingAndFilter(t *testing.T) {
	ctx := context.Background()
	n := openTestNotes(t)

	_, _ = n.SaveToReview(ctx, "demo", "cache", "the http client caches responses aggressively")
	_, _ = n.SaveToReview(ctx, "demo", "other", "unrelated note about testing frameworks")
	_, _ = n.SaveToReview(ctx, "otherproj", "cache2", "another project caches http too")
	approve(t, n, ctx, "cache")
	approve(t, n, ctx, "other")
	approve(t, n, ctx, "cache2")

	hits, err := n.Search(ctx, "demo", "http", 10)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(hits) != 1 || hits[0].Note.Project != "demo" {
		t.Fatalf("project filter failed: %+v", hits)
	}

	all, _ := n.Search(ctx, "", "http", 10)
	if len(all) != 2 {
		t.Fatalf("cross-project search should return 2, got %d", len(all))
	}
}

func TestNotesSecretFlagged(t *testing.T) {
	ctx := context.Background()
	n := openTestNotes(t)

	ent, err := n.SaveToReview(ctx, "demo", "credentials", "the api key is sk-proj-SecretValue0123456789012345678 for prod")
	if err != nil {
		t.Fatalf("SaveToReview: %v", err)
	}
	if len(ent.Reason) == 0 {
		t.Fatal("secret text must carry a reason")
	}
}

func TestNotesTOCBudget(t *testing.T) {
	ctx := context.Background()
	n := openTestNotes(t)

	for _, title := range []string{"alpha", "beta-longer-title", "gamma"} {
		_, _ = n.SaveToReview(ctx, "demo", title, "body of "+title)
		approve(t, n, ctx, title)
	}

	lines, _, err := n.TOC(ctx, "demo", 30)
	if err != nil {
		t.Fatalf("TOC: %v", err)
	}
	if len(lines) == 0 {
		t.Fatal("empty TOC")
	}
	total := 0
	for _, l := range lines {
		for _, want := range []string{"alpha", "beta-longer-title", "gamma"} {
			if l == want {
				total += len(l)
			}
		}
	}
	if total > 36 {
		t.Fatalf("TOC over budget: %d chars in %v", total, lines)
	}
}

func TestNotesTOCNewestFirst(t *testing.T) {
	ctx := context.Background()
	n := openTestNotes(t)
	for _, title := range []string{"first", "second"} {
		_, _ = n.SaveToReview(ctx, "demo", title, "body of "+title)
		approve(t, n, ctx, title)
		// second-resolution timestamps: force ordering to advance.
		time.Sleep(1100 * time.Millisecond)
	}
	lines, _, err := n.TOC(ctx, "demo", 0)
	if err != nil {
		t.Fatalf("TOC: %v", err)
	}
	if len(lines) < 2 || lines[0] != "second" {
		t.Fatalf("newest note should lead, got %v", lines)
	}
}

func TestNotesRoundTripParse(t *testing.T) {
	n := Note{ID: "x123", Project: "demo", Title: "spaces and Things", File: "spaces-and-things-x123.md",
		Scope: "project", CreatedAt: 10, UpdatedAt: 20, Pinned: true, Body: "# hello\n\nworld"}
	raw := marshalNote(n)
	got, err := parseNoteFile(raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if got.ID != n.ID || got.Title != n.Title || got.Body != n.Body || got.Pinned != true || got.CreatedAt != 10 {
		t.Fatalf("round trip mismatch: %+v", got)
	}
	if !bytes.HasPrefix(raw, []byte("{")) || !bytes.Contains(raw, []byte("\n---\n")) {
		t.Fatalf("bad front-matter shape")
	}
}

func TestNotesReindexFromFiles(t *testing.T) {
	ctx := context.Background()
	n := openTestNotes(t)

	_, _ = n.SaveToReview(ctx, "demo", "deploy", "releases ship via rolling deploys to the cluster")
	note := approve(t, n, ctx, "deploy")

	if err := n.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	n2, err := OpenNotes(ctx, n.root)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer n2.Close()

	count, err := n2.Reindex(ctx, "demo")
	if err != nil {
		t.Fatalf("Reindex: %v", err)
	}
	if count != 1 {
		t.Fatalf("reindex count = %d, want 1", count)
	}
	hits, err := n2.Search(ctx, "demo", "rolling", 10)
	if err != nil || len(hits) != 1 || hits[0].Note.File != note.File {
		t.Fatalf("reindexed search failed: %+v err=%v", hits, err)
	}
}

func TestNotesValidation(t *testing.T) {
	ctx := context.Background()
	n := openTestNotes(t)

	if _, err := n.SaveToReview(ctx, "../evil", "t", "x"); err == nil {
		t.Fatal("traversal project accepted")
	}
	if _, err := n.SaveToReview(ctx, "demo", "", "x"); err == nil {
		t.Fatal("empty title accepted")
	}
	if _, err := n.Search(ctx, "demo", `"`, 10); err == nil {
		t.Fatal("quote-only query accepted")
	}
}

func TestNotesFilePermissions(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("chmod assertions are meaningless as root")
	}
	ctx := context.Background()
	n := openTestNotes(t)
	_, _ = n.SaveToReview(ctx, "demo", "perm", "hello world about permissions on files")
	note := approve(t, n, ctx, "perm")

	fi, err := os.Stat(filepath.Join(n.notesDir("demo"), note.File))
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("note mode = %o, want 600", fi.Mode().Perm())
	}
	fi, err = os.Stat(filepath.Join(n.root, "index.db"))
	if err != nil {
		t.Fatalf("stat index: %v", err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("index.db mode = %o, want 600", fi.Mode().Perm())
	}
}
