// Store bundles facts, notes, and ledgers behind one facade for the MCP tool
// bus. It stays decoupled from config: callers supply store handles and
// budgets explicitly.
package memory

import (
	"context"
)

// Store is the in-process backend for the MCP memory profile tools.
// All fields may be nil individually; a nil sub-store surfaces as a tool
// error rather than a panic.
type Store struct {
	Facts          *Facts
	Notes          *Notes
	Ledger         *Ledger
	LedgerBudget   int
	NotesTocBudget int
}

// MemSave stores (or queues for review) one fact.
func (s *Store) MemSave(ctx context.Context, scope, project, topic, value, provenance string) (Fact, bool, error) {
	return s.Facts.Save(ctx, scope, project, topic, value, provenance)
}

func (s *Store) MemRecall(ctx context.Context, project, topic string) ([]Fact, error) {
	return s.Facts.Recall(ctx, project, topic)
}

func (s *Store) MemReviews(ctx context.Context) ([]ReviewEntry, error) {
	return s.Facts.PendingReviews(ctx)
}

func (s *Store) MemApprove(ctx context.Context, id string) (Fact, error) {
	return s.Facts.Approve(ctx, id)
}

func (s *Store) MemReject(ctx context.Context, id string) error {
	return s.Facts.Reject(ctx, id)
}

func (s *Store) NoteSave(ctx context.Context, project, title, text string) (NoteReviewEntry, error) {
	return s.Notes.SaveToReview(ctx, project, title, text)
}

func (s *Store) NoteSearch(ctx context.Context, query, project string, limit int) ([]NoteHit, error) {
	if limit <= 0 || limit > 100 {
		limit = 10
	}
	return s.Notes.Search(ctx, project, query, limit)
}

func (s *Store) NoteTOC(ctx context.Context, project string) ([]string, []Note, error) {
	return s.Notes.TOC(ctx, project, s.NotesTocBudget)
}

func (s *Store) NoteReindex(ctx context.Context, project string) (int, error) {
	return s.Notes.Reindex(ctx, project)
}

func (s *Store) NoteReviews(ctx context.Context) ([]NoteReviewEntry, error) {
	return s.Notes.PendingReviews(ctx)
}

func (s *Store) NoteApprove(ctx context.Context, id string) (Note, error) {
	return s.Notes.Approve(ctx, id)
}

func (s *Store) NoteReject(ctx context.Context, id string) error {
	return s.Notes.Reject(ctx, id)
}

func (s *Store) LedgerGet(ctx context.Context, project string) (string, error) {
	return s.Ledger.Get(project)
}

// LedgerUpdate replaces a project ledger, bounded by the configured budget.
func (s *Store) LedgerUpdate(ctx context.Context, project, text string) (string, error) {
	return s.Ledger.Update(project, text, s.LedgerBudget)
}
