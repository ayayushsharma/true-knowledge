package memory

import (
	"context"
	"testing"
)

// TestStoreNilSubstoresNotImplemented covers the documented Store contract:
// a nil sub-store surfaces as an error, never a panic.
func TestStoreNilSubstoresErrorNotPanic(t *testing.T) {
	ctx := context.Background()
	calls := []struct {
		name string
		fn   func(s *Store) error
	}{
		{"mem_save", func(s *Store) error { _, _, err := s.MemSave(ctx, "project", "p", "t", "v", ""); return err }},
		{"mem_recall", func(s *Store) error { _, err := s.MemRecall(ctx, "p", "t"); return err }},
		{"mem_review", func(s *Store) error { _, err := s.MemReviews(ctx); return err }},
		{"mem_approve", func(s *Store) error { _, err := s.MemApprove(ctx, "id"); return err }},
		{"mem_reject", func(s *Store) error { return s.MemReject(ctx, "id") }},
		{"note_save", func(s *Store) error { _, err := s.NoteSave(ctx, "p", "t", "x"); return err }},
		{"note_search", func(s *Store) error { _, err := s.NoteSearch(ctx, "q", "p", 10); return err }},
		{"note_toc", func(s *Store) error { _, _, err := s.NoteTOC(ctx, "p"); return err }},
		{"note_reindex", func(s *Store) error { _, err := s.NoteReindex(ctx, "p"); return err }},
		{"note_review", func(s *Store) error { _, err := s.NoteReviews(ctx); return err }},
		{"note_approve", func(s *Store) error { _, err := s.NoteApprove(ctx, "id"); return err }},
		{"note_reject", func(s *Store) error { return s.NoteReject(ctx, "id") }},
		{"ledger_get", func(s *Store) error { _, err := s.LedgerGet(ctx, "p"); return err }},
		{"ledger_history", func(s *Store) error { _, err := s.LedgerHistory(ctx, "p"); return err }},
		{"ledger_append", func(s *Store) error { _, err := s.LedgerAppend(ctx, "p", KeyGoal, "v"); return err }},
	}
	for _, call := range calls {
		s := &Store{} // every sub-store nil: each method must error, never panic
		if err := call.fn(s); err == nil {
			t.Errorf("%s: expected an error from a Store with nil sub-stores, got nil", call.name)
		}
	}
}

// TestStoreLedgerEnabledGate: the enabled gate fails before the nil check for
// an unset store so the message stays "ledger is disabled".
func TestStoreLedgerDisabledGateBeforeNil(t *testing.T) {
	s := &Store{}
	_, err := s.LedgerAppend(context.Background(), "p", KeyGoal, "v")
	if err != ErrLedgerDisabled {
		t.Fatalf("want ErrLedgerDisabled, got %v", err)
	}
}
