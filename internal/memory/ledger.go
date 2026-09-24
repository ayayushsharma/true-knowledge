// Ledger v2: per-project working truths as an append-only JSONL log. Every
// entry is immutable: {seq, ts, key, value}. The write path is verbatim and
// never truncates, folds, or rewrites (retrieval-only budget, see the ledger
// ADR). get() folds the latest entry per key (last write wins); history()
// returns the complete uncapped log; prune() is the human-only deletion
// surface. Files are 0600 like every other tk-owned artifact.
package memory

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	KeyGoal          = "goal"
	KeyNext          = "next"
	KeyDone          = "done"
	KeyDecisions     = "decisions"
	KeyOpenQuestions = "open_questions"
)

// LedgerKeys are the fixed five keys (canonical display order). get() folds
// these; unknown keys are rejected at write time so every read shape stays
// deterministic.
var LedgerKeys = []string{KeyGoal, KeyNext, KeyDone, KeyDecisions, KeyOpenQuestions}

// ValidLedgerKey reports whether key is one of the fixed five.
func ValidLedgerKey(key string) bool {
	for _, k := range LedgerKeys {
		if key == k {
			return true
		}
	}
	return false
}

// LedgerEntry is one immutable line of a project ledger.
type LedgerEntry struct {
	Seq   int    `json:"seq"`
	TS    string `json:"ts"`
	Key   string `json:"key"`
	Value string `json:"value"`
}

// Ledger is a directory of per-project append-only JSONL files.
type Ledger struct {
	dir string
}

// OpenLedger ensures the ledger directory exists.
func OpenLedger(dir string) (*Ledger, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	return &Ledger{dir: dir}, nil
}

// Append adds one immutable entry. Validation is limited to a safe project
// name, a valid key, and a non-empty value; the value is stored verbatim.
func (l *Ledger) Append(project, key, value string) (LedgerEntry, error) {
	p, err := validProject(project)
	if err != nil {
		return LedgerEntry{}, err
	}
	if !ValidLedgerKey(key) {
		return LedgerEntry{}, fmt.Errorf("ledger key %q not one of %s", key, strings.Join(LedgerKeys, "|"))
	}
	if strings.TrimSpace(value) == "" {
		return LedgerEntry{}, errors.New("ledger value must be non-empty")
	}
	seq, err := l.lastSeq(p)
	if err != nil {
		return LedgerEntry{}, err
	}
	e := LedgerEntry{Seq: seq + 1, TS: time.Now().UTC().Format(time.RFC3339), Key: key, Value: value}
	raw, err := json.Marshal(e)
	if err != nil {
		return LedgerEntry{}, err
	}
	if err := appendLine(l.path(p), raw); err != nil {
		return LedgerEntry{}, err
	}
	return e, nil
}

// Get folds the latest entry per key (last write wins) and caps each returned
// value to retrChars by whole runes + "...truncated". The stored log is never
// touched. An absent ledger yields an empty map, not an error.
func (l *Ledger) Get(project string, retrChars int) (map[string]LedgerEntry, error) {
	p, err := validProject(project)
	if err != nil {
		return nil, err
	}
	entries, err := l.readAll(p)
	if err != nil {
		return nil, err
	}
	out := map[string]LedgerEntry{}
	for _, e := range entries {
		out[e.Key] = e // later entries win
	}
	for k, e := range out {
		e.Value = capValue(e.Value, retrChars)
		out[k] = e
	}
	return out, nil
}

// History returns every entry in append order — complete and uncapped. This
// is the audit trail ("what do durable facts and knowledge evolution look
// like over time"); it is never truncated.
func (l *Ledger) History(project string) ([]LedgerEntry, error) {
	p, err := validProject(project)
	if err != nil {
		return nil, err
	}
	return l.readAll(p)
}

// Prune deletes a project's ledger forever — the human-only deletion surface.
// Removes both the v2 JSONL and any legacy v1 JSON blob.
func (l *Ledger) Prune(project string) error {
	p, err := validProject(project)
	if err != nil {
		return err
	}
	for _, path := range []string{l.path(p), l.pathLegacy(p)} {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}

// Projects lists project names with a v2 ledger file (legacy JSON is ignored).
func (l *Ledger) Projects() ([]string, error) {
	ents, err := os.ReadDir(l.dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []string
	for _, e := range ents {
		if strings.HasSuffix(e.Name(), ".jsonl") {
			out = append(out, strings.TrimSuffix(e.Name(), ".jsonl"))
		}
	}
	sort.Strings(out)
	return out, nil
}

// lastSeq returns the seq of the last valid entry, 0 when empty.
func (l *Ledger) lastSeq(p string) (int, error) {
	entries, err := l.readAll(p)
	if err != nil {
		return 0, err
	}
	if len(entries) == 0 {
		return 0, nil
	}
	return entries[len(entries)-1].Seq, nil
}

// readAll parses every line. A trailing partial/unparseable line (a crash
// mid-append) is tolerated and dropped; blank lines are skipped; interior
// corruption fails loudly so serves never hide damage.
func (l *Ledger) readAll(p string) ([]LedgerEntry, error) {
	raw, err := os.ReadFile(l.path(p))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	lines := strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n")
	var entries []LedgerEntry
	for i, ln := range lines {
		if len(strings.TrimSpace(ln)) == 0 {
			continue
		}
		var e LedgerEntry
		if err := json.Unmarshal([]byte(ln), &e); err != nil {
			if i == len(lines)-1 {
				continue // trailing partial line from an interrupted append
			}
			return nil, fmt.Errorf("%s line %d: %w", l.path(p), i+1, err)
		}
		entries = append(entries, e)
	}
	return entries, nil
}

// capValue truncates by whole runes to max chars, appending a marker. max<=0
// disables the cap. The stored value is never touched — this bounds only what
// get() renders into a context window.
func capValue(v string, max int) string {
	if max <= 0 {
		return v
	}
	r := []rune(v)
	if len(r) <= max {
		return v
	}
	const marker = "...truncated"
	if max <= len(marker) {
		return string(r[:min(max, len(r))])
	}
	return string(r[:max-len(marker)]) + marker
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func (l *Ledger) path(p string) string {
	return filepath.Join(l.dir, p+".jsonl")
}

func (l *Ledger) pathLegacy(p string) string {
	return filepath.Join(l.dir, p+".json")
}
