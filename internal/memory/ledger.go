// Ledger: per-project working truths stored as bounded JSON files under
// ledger/<project>.json. The file is the record of a single LLM-maintained
// "what does project X currently look like" blob; updates are full-text
// replacements (never line merges), enforced to fit a char budget. Files are
// 0600 like every other tk-owned secret-capable artifact.
package memory

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// LedgerEntry is the on-disk shape of one project ledger.
type LedgerEntry struct {
	Project   string `json:"project"`
	UpdatedAt int64  `json:"updated_at"`
	Text      string `json:"text"`
}

// Ledger is a directory of per-project ledger files.
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

// Get returns the ledger text for one project (empty when none exists).
func (l *Ledger) Get(project string) (string, error) {
	p, err := validProject(project)
	if err != nil {
		return "", err
	}
	raw, err := os.ReadFile(l.path(p))
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}
	var e LedgerEntry
	if err := json.Unmarshal(raw, &e); err != nil {
		return "", fmt.Errorf("ledger %s: %w", p, err)
	}
	return e.Text, nil
}

// All returns every project ledger, newest-updated first.
func (l *Ledger) All() ([]LedgerEntry, error) {
	ents, err := os.ReadDir(l.dir)
	if err != nil {
		return nil, err
	}
	var out []LedgerEntry
	for _, e := range ents {
		if !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(l.dir, e.Name()))
		if err != nil {
			return nil, err
		}
		var le LedgerEntry
		if err := json.Unmarshal(raw, &le); err != nil {
			return nil, fmt.Errorf("ledger %s: %w", e.Name(), err)
		}
		out = append(out, le)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].UpdatedAt > out[j].UpdatedAt })
	return out, nil
}

// Projects lists project names with a ledger file.
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
		if strings.HasSuffix(e.Name(), ".json") {
			out = append(out, strings.TrimSuffix(e.Name(), ".json"))
		}
	}
	sort.Strings(out)
	return out, nil
}

// Update replaces a project ledger with text, truncating to maxChars by whole
// runes. Empty projects are allowed (clears/ignores); the file is removed
// when text is empty so disabled ledgers leave no trace.
func (l *Ledger) Update(project, text string, maxChars int) (string, error) {
	p, err := validProject(project)
	if err != nil {
		return "", err
	}
	if maxChars <= 0 {
		maxChars = 1500
	}
	if text = strings.TrimSpace(text); text != "" {
		r := []rune(text)
		if len(r) > maxChars {
			r = r[:maxChars]
		}
		text = strings.TrimSpace(string(r))
	}
	e := LedgerEntry{Project: p, UpdatedAt: time.Now().Unix(), Text: text}
	if text == "" {
		_ = os.Remove(l.path(p))
		return "", nil
	}
	raw, _ := json.MarshalIndent(e, "", "  ")
	raw = append(raw, '\n')
	if err := atomicWrite(l.path(p), raw); err != nil {
		return "", err
	}
	return e.Text, nil
}

func (l *Ledger) path(project string) string {
	return filepath.Join(l.dir, project+".json")
}
