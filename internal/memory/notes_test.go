package memory

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
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

// fakeEmbedServer returns embeddings derived from a token→vector map, plus a
// counter so tests can assert the semantic path actually ran.
func fakeEmbedServer(mapping map[string][]float32) (*httptest.Server, *int) {
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		if r.URL.Path != "/api/embed" {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		var req struct {
			Model string   `json:"model"`
			Input []string `json:"input"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		emb := [][]float32{}
		for _, t := range req.Input {
			v, ok := mapping[strings.ToLower(strings.TrimSpace(t))]
			if !ok {
				v = []float32{0, 0, 0}
			}
			emb = append(emb, v)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"model": req.Model, "embeddings": emb})
	}))
	return srv, &hits
}

func earthNote() Note {
	return Note{ID: "earth", Project: "demo", Title: "planet", File: "planet-0001.md",
		Scope: "project", CreatedAt: 1, UpdatedAt: 1, Body: "earth is the third planet"}
}

func marsNote() Note {
	return Note{ID: "mars", Project: "demo", Title: "red planet", File: "red-0002.md",
		Scope: "project", CreatedAt: 2, UpdatedAt: 2, Body: "mars is the red planet"}
}

func TestEmbedClientReply(t *testing.T) {
	srv, _ := fakeEmbedServer(map[string][]float32{"hi": {1, 2, 3}})
	defer srv.Close()
	e := NewEmbedder(srv.URL, "m1", 3000)
	vecs, err := e.Embed(context.Background(), []string{"hi"})
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if len(vecs) != 1 || len(vecs[0]) != 3 || vecs[0][0] != 1 {
		t.Fatalf("unexpected vectors: %v", vecs)
	}
}

func TestEmbedClientEndpointDown(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("port dialing as root is unreliable in CI")
	}
	srv, _ := fakeEmbedServer(map[string][]float32{"hi": {1, 2, 3}})
	url := srv.URL
	srv.Close()
	e := NewEmbedder(url, "m1", 3000)
	if _, err := e.Embed(context.Background(), []string{"hi"}); err == nil {
		t.Fatal("want error for dead endpoint")
	}
}

// TestNotesSemanticFallback: with the endpoint unreachable, search must still
// return BM25 results and never fail.
func TestNotesSemanticEndpointDown(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("port dialing as root is unreliable in CI")
	}
	ctx := context.Background()
	n := openTestNotes(t)
	_, _ = n.SaveToReview(ctx, "demo", "planet", "earth is the third planet around the sun")
	approve(t, n, ctx, "planet")

	dead, _ := fakeEmbedServer(map[string][]float32{})
	url := dead.URL
	dead.Close()
	n.SetEmbedder(NewEmbedder(url, "m1", 3000))

	hits, err := n.Search(ctx, "demo", "planet", 10)
	if err != nil {
		t.Fatalf("Search must not fail when embed endpoint is down: %v", err)
	}
	if len(hits) != 1 || !strings.Contains(hits[0].Excerpt, "third planet") {
		t.Fatalf("BM25 fallback failed: %+v", hits)
	}
}

// TestNotesSemanticRecall: a query that is a BM25 miss (gibberish token) can
// still surface the semantically-identical note via the fused cosine pass.
func TestNotesSemanticRecall(t *testing.T) {
	ctx := context.Background()
	// Vector space: exactly one note has a distinct direction (mars),
	// and the gibberish query "cratered" embeds to that same direction.
	mapping := map[string][]float32{
		"cratered":                  {1, 0},
		"mars is the red planet":    {0.9, 0.1},
		"earth is the third planet": {0.1, 0.9},
	}
	srv, hits := fakeEmbedServer(mapping)
	defer srv.Close()

	root := filepath.Join(t.TempDir(), "notes")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	n, err := OpenNotes(ctx, root)
	if err != nil {
		t.Fatalf("OpenNotes: %v", err)
	}
	defer n.Close()
	if err := os.MkdirAll(filepath.Join(root, "demo"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "demo", "planet-0001.md"), marshalNote(earthNote()), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "demo", "red-0002.md"), marshalNote(marsNote()), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := n.Reindex(ctx, ""); err != nil {
		t.Fatalf("Reindex: %v", err)
	}

	// Pass 1 — BM25-only index: "cratered" is not a token anywhere.
	plainHits, err := n.Search(ctx, "demo", "cratered", 10)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(plainHits) != 0 {
		t.Fatalf("cratered is a BM25 miss, got %+v", plainHits)
	}

	// Pass 2 — embedded index: the semantic pass must pull the mars note up.
	n.SetEmbedder(NewEmbedder(srv.URL, "m1", 3000))
	if _, err := n.Reindex(ctx, ""); err != nil {
		t.Fatalf("Reindex (embedded): %v", err)
	}
	semHits, err := n.Search(ctx, "demo", "cratered", 10)
	if err != nil {
		t.Fatalf("Search semantic: %v", err)
	}
	if len(semHits) == 0 {
		t.Fatal("semantic recall failed: no results despite cosine hit")
	}
	if semHits[0].Note.ID != "mars" {
		t.Fatalf("semantic recall should surface mars first, got %s", semHits[0].Note.ID)
	}
	if *hits < 2 {
		t.Fatalf("embedder never called (hits=%d)", *hits)
	}
}

// TestNotesSemanticModelMismatch: embeddings stored under one model are
// ignored when the query embedder uses another — BM25 only, no error.
func TestNotesSemanticModelMismatch(t *testing.T) {
	ctx := context.Background()
	srv, _ := fakeEmbedServer(map[string][]float32{"earth": {1, 0}, "planet": {1, 0}})
	defer srv.Close()

	root := filepath.Join(t.TempDir(), "notes")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	n, err := OpenNotes(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	defer n.Close()
	if err := os.MkdirAll(filepath.Join(root, "demo"), 0o700); err != nil {
		t.Fatal(err)
	}
	note := earthNote()
	note.EmbedModel = "old-model"
	note.Embedding = []float32{1, 0}
	if err := os.WriteFile(filepath.Join(root, "demo", "planet-0001.md"), marshalNote(note), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := n.Reindex(ctx, ""); err != nil {
		t.Fatalf("Reindex: %v", err)
	}

	n.SetEmbedder(NewEmbedder(srv.URL, "m1", 3000))
	hits, err := n.Search(ctx, "demo", "planet", 10)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(hits) != 1 || !strings.Contains(hits[0].Excerpt, "third planet") {
		t.Fatalf("BM25 fallback on model mismatch failed: %+v", hits)
	}
}

// TestNotesReindexEmbeds: Reindex batch-embeds bodies when an embedder is set.
func TestNotesReindexEmbeds(t *testing.T) {
	ctx := context.Background()
	mapping := map[string][]float32{
		"earth is the third planet": {0.2, 0.8},
		"mars is the red planet":    {0.8, 0.2},
	}
	srv, _ := fakeEmbedServer(mapping)
	defer srv.Close()

	root := filepath.Join(t.TempDir(), "notes")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	n, err := OpenNotes(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	defer n.Close()
	for _, dir := range []string{"demo", "demo2"} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "demo", "planet-0001.md"), marshalNote(earthNote()), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "demo2", "red-0002.md"), marshalNote(marsNote()), 0o600); err != nil {
		t.Fatal(err)
	}
	n.SetEmbedder(NewEmbedder(srv.URL, "m1", 3000))
	if _, err := n.Reindex(ctx, ""); err != nil {
		t.Fatalf("Reindex: %v", err)
	}
	var count int
	if err := n.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM notes WHERE embed_model = ?`, "m1").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("expected both notes embedded under m1, got %d", count)
	}
}
