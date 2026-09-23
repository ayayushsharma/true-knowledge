// Package store owns tk's tiny registry: project name -> source path.
// Graph data itself lives in CBM's store; we only keep refs + indexed HEAD.
package store

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

var unsafeChars = regexp.MustCompile(`[^a-zA-Z0-9_-]+`)

// Project is one registered repo.
type Project struct {
	Path      string `json:"path"`
	Mode      string `json:"mode"`
	Head      string `json:"head,omitempty"`
	IndexedAt int64  `json:"indexed_at,omitempty"`
	// ZoektHead is the git HEAD covered by the zoekt shards ("" = none/stale).
	ZoektHead string `json:"zoekt_head,omitempty"`
	// Fingerprint covers non-git trees (max mtime + file count).
	// Git repos use Head instead; never both.
	Fingerprint string `json:"fingerprint,omitempty"`
}

// Registry maps canonical name -> project.
type Registry map[string]Project

// NormalizeName makes a stable identifier; rejects empties.
func NormalizeName(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", fmt.Errorf("project name must not be empty")
	}
	s = unsafeChars.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-")
	if s == "" {
		return "", fmt.Errorf("project name %q has no usable characters", s)
	}
	return s, nil
}

// Load reads tk.json or returns empty.
func Load(path string) (Registry, error) {
	r := Registry{}
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return r, nil
		}
		return nil, err
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		return r, nil
	}
	if err := json.Unmarshal(raw, &r); err != nil {
		return nil, fmt.Errorf("invalid registry %s: %w", path, err)
	}
	if r == nil {
		r = Registry{}
	}
	return r, nil
}

// Save writes atomically with 0600.
func Save(path string, r Registry) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Names returns sorted names (sorted for stable completion/output).
func (r Registry) Names() []string {
	out := make([]string, 0, len(r))
	for k := range r {
		out = append(out, k)
	}
	// insertion sort — registries are tiny; avoids extra import.
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j] < out[j-1]; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

// Touch records a successful git-backed index.
func (r Registry) Touch(name, head, mode string) {
	p := r[name]
	p.Head = head
	p.Mode = mode
	p.IndexedAt = time.Now().Unix()
	r[name] = p
}

// TouchFiles records a successful non-git index keyed by fingerprint.
func (r Registry) TouchFiles(name, fingerprint, mode string) {
	p := r[name]
	p.Head = ""
	p.Mode = mode
	p.Fingerprint = fingerprint
	p.IndexedAt = time.Now().Unix()
	r[name] = p
}
