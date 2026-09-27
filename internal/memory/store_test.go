package memory

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// storeCalls is every exported Store method, as a thunk returning only the
// error, so one table can drive all three unusable-store shapes.
func storeCalls(ctx context.Context) []struct {
	name string
	fn   func(s *Store) error
} {
	return []struct {
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
}

// TestStoreNilSubstoresErrorNotPanic covers the documented Store contract:
// a sub-store that cannot serve surfaces as an error, never a panic. All three
// shapes are checked, because they fail at different depths:
//
//	nil pointer  - &Store{}
//	nil receiver - (*Store)(nil)
//	zero value   - &Store{Facts: &Facts{}}, i.e. a non-nil sub-store holding a
//	                nil *sql.DB (database/sql has no nil-receiver guard, so a
//	                pointer-only check reached ExecContext and panicked there)
//	                or, for the ledger, an empty dir, which makes path()
//	                relative and would write into the working directory.
func TestStoreNilSubstoresErrorNotPanic(t *testing.T) {
	ctx := context.Background()
	shapes := []struct {
		name string
		make func() *Store
	}{
		{"nil sub-store pointers", func() *Store { return &Store{} }},
		{"zero-value sub-stores", func() *Store {
			return &Store{Facts: &Facts{}, Notes: &Notes{}, Ledger: &Ledger{}, LedgerEnabled: true}
		}},
		{"nil receiver", func() *Store { return nil }},
	}
	for _, shape := range shapes {
		t.Run(shape.name, func(t *testing.T) {
			for _, call := range storeCalls(ctx) {
				s := shape.make()
				if err := call.fn(s); err == nil {
					t.Errorf("%s: expected an error from an unusable store, got nil", call.name)
				}
			}
		})
	}
}

// A zero-value Ledger is the dangerous shape: it does not error, it writes.
// filepath.Join("", "p.jsonl") is relative, so Append would create p.jsonl in
// the process working directory. The guard must turn that into an error.
func TestStoreZeroLedgerDoesNotWriteToCWD(t *testing.T) {
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	project := "zero-ledger-should-never-be-written"
	t.Cleanup(func() {
		if _, err := os.Stat(filepath.Join(cwd, project+".jsonl")); err == nil {
			t.Errorf("a zero-value Ledger wrote %s.jsonl into the working directory", project)
			_ = os.Remove(filepath.Join(cwd, project+".jsonl"))
		}
	})
	s := &Store{Ledger: &Ledger{}, LedgerEnabled: true}
	if _, err := s.LedgerAppend(context.Background(), project, KeyGoal, "v"); err == nil {
		t.Fatal("expected an error from a zero-value Ledger")
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
