// Store bundles facts, notes, and ledgers behind one facade for the MCP tool
// bus. It stays decoupled from config: callers supply store handles and
// budgets explicitly.
package memory

import (
	"context"
	"errors"
)

// Store is the in-process backend for the MCP memory profile tools.
// All fields may be nil individually; a nil sub-store surfaces as a tool
// error rather than a panic.
type Store struct {
	Facts          *Facts
	Notes          *Notes
	Ledger         *Ledger
	LedgerEnabled  bool
	LedgerBudget   int
	NotesTocBudget int
}

var (
	errFactsNil  = errors.New("facts store not opened")
	errNotesNil  = errors.New("notes store not opened")
	errLedgerNil = errors.New("ledger store not opened")
)

func (s *Store) facts() error {
	if s.Facts == nil {
		return errFactsNil
	}
	return nil
}

func (s *Store) notes() error {
	if s.Notes == nil {
		return errNotesNil
	}
	return nil
}

func (s *Store) ledger() error {
	if s.Ledger == nil {
		return errLedgerNil
	}
	return nil
}

// MemSave stores (or queues for review) one fact.
func (s *Store) MemSave(ctx context.Context, scope, project, topic, value, provenance string) (Fact, bool, error) {
	if err := s.facts(); err != nil {
		return Fact{}, false, err
	}
	return s.Facts.Save(ctx, scope, project, topic, value, provenance)
}

func (s *Store) MemRecall(ctx context.Context, project, topic string) ([]Fact, error) {
	if err := s.facts(); err != nil {
		return nil, err
	}
	return s.Facts.Recall(ctx, project, topic)
}

func (s *Store) MemReviews(ctx context.Context) ([]ReviewEntry, error) {
	if err := s.facts(); err != nil {
		return nil, err
	}
	return s.Facts.PendingReviews(ctx)
}

func (s *Store) MemApprove(ctx context.Context, id string) (Fact, error) {
	if err := s.facts(); err != nil {
		return Fact{}, err
	}
	return s.Facts.Approve(ctx, id)
}

func (s *Store) MemReject(ctx context.Context, id string) error {
	if err := s.facts(); err != nil {
		return err
	}
	return s.Facts.Reject(ctx, id)
}

func (s *Store) NoteSave(ctx context.Context, project, title, text string) (NoteReviewEntry, error) {
	if err := s.notes(); err != nil {
		return NoteReviewEntry{}, err
	}
	return s.Notes.SaveToReview(ctx, project, title, text)
}

func (s *Store) NoteSearch(ctx context.Context, query, project string, limit int) ([]NoteHit, error) {
	if err := s.notes(); err != nil {
		return nil, err
	}
	if limit <= 0 || limit > 100 {
		limit = 10
	}
	return s.Notes.Search(ctx, project, query, limit)
}

func (s *Store) NoteTOC(ctx context.Context, project string) ([]string, []Note, error) {
	if err := s.notes(); err != nil {
		return nil, nil, err
	}
	return s.Notes.TOC(ctx, project, s.NotesTocBudget)
}

func (s *Store) NoteReindex(ctx context.Context, project string) (int, error) {
	if err := s.notes(); err != nil {
		return 0, err
	}
	return s.Notes.Reindex(ctx, project)
}

func (s *Store) NoteReviews(ctx context.Context) ([]NoteReviewEntry, error) {
	if err := s.notes(); err != nil {
		return nil, err
	}
	return s.Notes.PendingReviews(ctx)
}

func (s *Store) NoteApprove(ctx context.Context, id string) (Note, error) {
	if err := s.notes(); err != nil {
		return Note{}, err
	}
	return s.Notes.Approve(ctx, id)
}

func (s *Store) NoteReject(ctx context.Context, id string) error {
	if err := s.notes(); err != nil {
		return err
	}
	return s.Notes.Reject(ctx, id)
}

// LedgerGet folds the latest entry per key with values capped to the
// retrieval budget (LedgerBudget). Never truncates the stored log.
func (s *Store) LedgerGet(ctx context.Context, project string) (map[string]LedgerEntry, error) {
	if err := s.ledger(); err != nil {
		return nil, err
	}
	return s.Ledger.Get(project, s.LedgerBudget)
}

// LedgerHistory returns the complete, uncapped append log for a project.
func (s *Store) LedgerHistory(ctx context.Context, project string) ([]LedgerEntry, error) {
	if err := s.ledger(); err != nil {
		return nil, err
	}
	return s.Ledger.History(project)
}

// LedgerAppend adds one immutable ledger entry verbatim (no budget applied on
// the write path). It honors the ledger.enabled gate exactly like the CLI:
// writes fail with ErrLedgerDisabled while reads (LedgerGet/LedgerHistory)
// stay open. No concurrency fix is needed (see the ledger ADR): appends are
// immutable, LWW by key, crash-tolerant, and all surfaces are single-threaded.
func (s *Store) LedgerAppend(ctx context.Context, project, key, value string) (LedgerEntry, error) {
	if !s.LedgerEnabled {
		return LedgerEntry{}, ErrLedgerDisabled
	}
	if err := s.ledger(); err != nil {
		return LedgerEntry{}, err
	}
	return s.Ledger.Append(project, key, value)
}
