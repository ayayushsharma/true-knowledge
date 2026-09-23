package memory

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func openTestFacts(t *testing.T) *Facts {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "mem")
	f, err := OpenFacts(context.Background(), dir)
	if err != nil {
		t.Fatalf("OpenFacts: %v", err)
	}
	t.Cleanup(func() { _ = f.Close() })
	return f
}

func TestFactsSaveRecallUpsert(t *testing.T) {
	ctx := context.Background()
	f := openTestFacts(t)

	fact, queued, err := f.Save(ctx, ScopeProject, "demo", "db", "postgres", "from setup")
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	if queued {
		t.Fatal("plain value should not be queued")
	}
	if fact.Topic != "db" || fact.Value != "postgres" {
		t.Fatalf("unexpected fact: %+v", fact)
	}
	if fact.UpdatedAt == 0 {
		t.Fatal("updated_at not set")
	}

	got, err := f.Recall(ctx, "demo", "db")
	if err != nil {
		t.Fatalf("Recall: %v", err)
	}
	if len(got) != 1 || got[0].Value != "postgres" || got[0].Provenance != "from setup" {
		t.Fatalf("unexpected recall: %+v", got)
	}

	second, queued, err := f.Save(ctx, ScopeProject, "demo", "db", "mysql", "migrated")
	if err != nil || queued {
		t.Fatalf("upsert Save: %v queued=%v", err, queued)
	}
	if second.UpdatedAt < fact.UpdatedAt {
		t.Fatalf("updated_at went backwards: %d -> %d", fact.UpdatedAt, second.UpdatedAt)
	}
	got, _ = f.Recall(ctx, "demo", "db")
	if len(got) != 1 || got[0].Value != "mysql" {
		t.Fatalf("upsert did not replace: %+v", got)
	}
}

func TestFactsGlobalFallback(t *testing.T) {
	ctx := context.Background()
	f := openTestFacts(t)

	if _, _, err := f.Save(ctx, ScopeGlobal, "somewhere", "api-base", "https://api.internal", ""); err != nil {
		t.Fatalf("save global: %v", err)
	}

	got, err := f.Recall(ctx, "unrelated-project", "api-base")
	if err != nil {
		t.Fatalf("Recall: %v", err)
	}
	if len(got) != 1 || got[0].Scope != ScopeGlobal || got[0].Project != "" {
		t.Fatalf("global fallback failed: %+v", got)
	}

	_, queued, err := f.Save(ctx, ScopeProject, "p1", "api-base", "project-specific", "")
	if err != nil {
		t.Fatalf("save project: %v", err)
	}
	if queued {
		t.Fatal("unexpected queue")
	}
	got, _ = f.Recall(ctx, "p1", "api-base")
	if len(got) != 2 {
		t.Fatalf("want project + global, got %d", len(got))
	}
	if got[0].Scope != ScopeProject {
		t.Fatalf("project fact should rank first: %+v", got[0])
	}
}

func TestFactsSecretReviewLifecycle(t *testing.T) {
	ctx := context.Background()
	f := openTestFacts(t)

	secret := "sk-proj-AbCdEf123456789012345678901234"
	fact, queued, err := f.Save(ctx, ScopeProject, "demo", "openai-key", secret, "user")
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	if !queued {
		t.Fatal("secret value must be queued, not stored silently")
	}
	if _, err := f.Recall(ctx, "demo", "openai-key"); err != nil {
		t.Fatalf("Recall: %v", err)
	}
	if got, _ := f.Recall(ctx, "demo", "openai-key"); len(got) != 0 {
		t.Fatalf("queued fact must not be recallable: %+v", got)
	}
	_ = fact

	reviews, err := f.PendingReviews(ctx)
	if err != nil {
		t.Fatalf("PendingReviews: %v", err)
	}
	if len(reviews) != 1 {
		t.Fatalf("want 1 pending, got %d", len(reviews))
	}
	ent := reviews[0]
	if ent.Topic != "openai-key" || len(ent.Reason) == 0 || ent.ID == "" {
		t.Fatalf("bad review entry: %+v", ent)
	}

	approved, err := f.Approve(ctx, ent.ID)
	if err != nil {
		t.Fatalf("Approve: %v", err)
	}
	if approved.Value != secret || approved.Scope != ScopeProject {
		t.Fatalf("bad approved fact: %+v", approved)
	}
	got, _ := f.Recall(ctx, "demo", "openai-key")
	if len(got) != 1 || got[0].Value != secret {
		t.Fatalf("approved fact missing: %+v", got)
	}
	if pending, _ := f.PendingReviews(ctx); len(pending) != 0 {
		t.Fatalf("approved entry still pending: %+v", pending)
	}
}

func TestFactsReviewReject(t *testing.T) {
	ctx := context.Background()
	f := openTestFacts(t)

	_, _, _ = f.Save(ctx, ScopeProject, "demo", "token", "ghp_AbCdEf1234567890123456789012345678", "")
	reviews, err := f.PendingReviews(ctx)
	if err != nil || len(reviews) != 1 {
		t.Fatalf("want 1 pending, got %d err=%v", len(reviews), err)
	}
	if err := f.Reject(ctx, reviews[0].ID); err != nil {
		t.Fatalf("Reject: %v", err)
	}
	if got, _ := f.Recall(ctx, "demo", "token"); len(got) != 0 {
		t.Fatalf("rejected fact leaked into store: %+v", got)
	}
	if pending, _ := f.PendingReviews(ctx); len(pending) != 0 {
		t.Fatalf("rejected entry still pending: %+v", pending)
	}
	if err := f.Reject(ctx, reviews[0].ID); err == nil {
		t.Fatal("rejecting twice should fail")
	}
}

func TestFactsScopeAndBoundsValidation(t *testing.T) {
	ctx := context.Background()
	f := openTestFacts(t)

	if _, _, err := f.Save(ctx, "bogus", "p", "t", "v", ""); err == nil {
		t.Fatal("invalid scope accepted")
	}
	if _, _, err := f.Save(ctx, ScopeProject, "", "t", "v", ""); err == nil {
		t.Fatal("project-less project-scope accepted")
	}
	if _, _, err := f.Save(ctx, ScopeProject, "p", "", "v", ""); err == nil {
		t.Fatal("empty topic accepted")
	}
	long := make([]byte, 257)
	for i := range long {
		long[i] = 'a'
	}
	if _, _, err := f.Save(ctx, ScopeProject, "p", string(long), "v", ""); err == nil {
		t.Fatal("overlong topic accepted")
	}
}

func TestFactsPersistenceAcrossReopen(t *testing.T) {
	ctx := context.Background()
	dir := filepath.Join(t.TempDir(), "mem")
	f1, err := OpenFacts(ctx, dir)
	if err != nil {
		t.Fatalf("OpenFacts: %v", err)
	}
	if _, _, err := f1.Save(ctx, ScopeProject, "demo", "lang", "go", ""); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if err := f1.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	f2, err := OpenFacts(ctx, dir)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer f2.Close()
	got, err := f2.Recall(ctx, "demo", "lang")
	if err != nil || len(got) != 1 || got[0].Value != "go" {
		t.Fatalf("fact lost across reopen: %+v err=%v", got, err)
	}
}

func TestFactsFilePermissions(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("chmod assertions are meaningless as root")
	}
	ctx := context.Background()
	dir := filepath.Join(t.TempDir(), "mem")
	f, err := OpenFacts(ctx, dir)
	if err != nil {
		t.Fatalf("OpenFacts: %v", err)
	}
	if _, _, err := f.Save(ctx, ScopeProject, "demo", "t", "v", ""); err != nil {
		t.Fatalf("Save: %v", err)
	}
	_ = f.Close()

	fi, err := os.Stat(filepath.Join(dir, "facts.db"))
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("facts.db mode = %o, want 600", fi.Mode().Perm())
	}
	di, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat dir: %v", err)
	}
	if di.Mode().Perm() != 0o700 {
		t.Fatalf("mem dir mode = %o, want 700", di.Mode().Perm())
	}
}
