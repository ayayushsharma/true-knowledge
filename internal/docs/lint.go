// Package docs is the doc gate: it checks the shape, the links, the size, and
// the immutability of AGENT_DOCS so the doc-writing contract in
// AGENT_DOCS/00-INDEX.md is enforced rather than merely stated.
package docs

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// MaxLines is the size budget for one authored doc. A file that hits it gets
// split or becomes an ADR; otherwise a consolidated doc becomes the thing it
// replaced.
const MaxLines = 200

// DocsDir is the authored-truth directory, relative to the module root.
const DocsDir = "AGENT_DOCS"

// HistoryDir holds the immutable record: ADRs plus the pre-consolidation docs.
const HistoryDir = "AGENT_DOCS/history"

// ForbiddenRefs are pre-consolidation paths. They are legal inside HistoryDir,
// which is where those documents now live, and a bug everywhere else.
var ForbiddenRefs = []string{
	"docs/DECISIONS/",
	"docs/00-AUTHORITY.md",
	"docs/AGENT-PROFILES.md",
	"docs/CBM-BOUNDARY.md",
	"docs/INDEXING.md",
	"docs/PATHS-CONFIG.md",
	"docs/REMAINING-WORK.md",
	"docs/ROADMAP.md",
}

type manifest struct {
	Files []struct {
		ID     string `json:"id"`
		Path   string `json:"path"`
		Status string `json:"status"`
	} `json:"files"`
}

// Check runs every doc assertion and returns one problem string per failure.
func Check(root string) []string {
	var problems []string
	problems = append(problems, checkAuthored(root)...)
	problems = append(problems, checkRefs(root)...)
	problems = append(problems, checkSpecPlacement(root)...)
	problems = append(problems, checkManifest(root)...)
	problems = append(problems, checkHistoryImmutable(root)...)
	return problems
}

// SpecRel is the frozen v1 spec's only legal home. It was a repo-root file
// until 2026-09-27, and root placement gave the one document that contradicts
// 02-BOUNDARY the most visible address in the repository.
const SpecRel = DocsDir + "/history/compatible-implementation-spec.md"

// SpecOldPath is where the spec lived before it joined history.
const SpecOldPath = "compatible-implementation-spec.md"

// checkSpecPlacement asserts both halves of that move. It is deliberately not
// part of the forbidden-path scan: the old name is a suffix of the new path, so
// a substring rule would reject the correct citation along with the wrong one.
func checkSpecPlacement(root string) []string {
	var problems []string
	if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(SpecRel))); err != nil {
		problems = append(problems, fmt.Sprintf("%s: frozen v1 spec is missing; it belongs in history, not at the repo root", SpecRel))
	}
	if _, err := os.Stat(filepath.Join(root, SpecOldPath)); err == nil {
		problems = append(problems, fmt.Sprintf("%s: frozen v1 spec is back at the repo root; it belongs at %s", SpecOldPath, SpecRel))
	}
	return problems
}

func checkAuthored(root string) []string {
	var problems []string
	paths, err := filepath.Glob(filepath.Join(root, DocsDir, "*.md"))
	if err != nil {
		return []string{fmt.Sprintf("glob %s: %v", DocsDir, err)}
	}
	sort.Strings(paths)
	if len(paths) == 0 {
		return []string{DocsDir + " has no top-level .md files"}
	}
	ids := map[string]string{}
	for _, path := range paths {
		rel, _ := filepath.Rel(root, path)
		fm, err := readFrontMatter(path)
		if err != nil {
			problems = append(problems, fmt.Sprintf("%s: %v", rel, err))
			continue
		}
		for _, key := range []string{"id", "title", "status", "date", "supersedes", "superseded-by"} {
			if fm[key] == "" {
				problems = append(problems, fmt.Sprintf("%s: front-matter key %q is missing or empty", rel, key))
			}
		}
		if d := fm["date"]; d != "" {
			if _, err := time.Parse("2006-01-02", d); err != nil {
				problems = append(problems, fmt.Sprintf("%s: date %q is not YYYY-MM-DD", rel, d))
			}
		}
		want := strings.ToLower(strings.TrimSuffix(filepath.Base(path), ".md"))
		if id := fm["id"]; id != "" && id != want {
			problems = append(problems, fmt.Sprintf("%s: id %q does not match filename stem %q", rel, id, want))
		}
		if prev, dup := ids[fm["id"]]; fm["id"] != "" && dup {
			problems = append(problems, fmt.Sprintf("%s: id %q already used by %s", rel, fm["id"], prev))
		}
		ids[fm["id"]] = rel
		for _, ref := range append(splitRefs(fm["supersedes"]), splitRefs(fm["superseded-by"])...) {
			target := cleanRef(ref)
			if target == "" {
				continue
			}
			if _, err := os.Stat(filepath.Join(root, target)); err != nil {
				problems = append(problems, fmt.Sprintf("%s: reference %q does not resolve", rel, target))
			}
		}
		data, err := os.ReadFile(path)
		if err != nil {
			problems = append(problems, fmt.Sprintf("%s: %v", rel, err))
			continue
		}
		if n := countLines(data); n > MaxLines {
			problems = append(problems, fmt.Sprintf("%s: %d lines, over the %d-line budget", rel, n, MaxLines))
		}
	}
	return problems
}

// ExemptFromRefCheck names the files allowed to mention a pre-consolidation
// path: the linter's own source and the machine index that declares the rule.
// The frozen v1 spec no longer needs an exemption — it lives under HistoryDir,
// which the walk skips — so a bare spec filename elsewhere is a real finding.
var ExemptFromRefCheck = []string{
	filepath.Join("internal", "docs", "lint.go"),
	filepath.Join("internal", "docs", "lint_test.go"),
	DocsDir + "/manifest.json",
}

func checkRefs(root string) []string {
	var problems []string
	exempt := map[string]bool{}
	for _, p := range ExemptFromRefCheck {
		exempt[filepath.ToSlash(p)] = true
	}
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			rel, _ := filepath.Rel(root, path)
			if rel == ".git" || rel == "dist" || rel == HistoryDir {
				return filepath.SkipDir
			}
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		if exempt[filepath.ToSlash(rel)] {
			return nil
		}
		switch filepath.Ext(path) {
		case ".md", ".go", ".toml", ".json", ".sh", ".jsonc":
		default:
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		seen := map[string]bool{}
		for _, line := range strings.Split(string(data), "\n") {
			for _, bad := range ForbiddenRefs {
				if strings.Contains(line, bad) && !seen[bad] {
					seen[bad] = true
					problems = append(problems, fmt.Sprintf("%s: stale path %q; it lives at %s", rel, bad, HistoryDir))
				}
			}
		}
		return nil
	})
	if err != nil {
		problems = append(problems, fmt.Sprintf("walk: %v", err))
	}
	return problems
}

func checkManifest(root string) []string {
	var problems []string
	data, err := os.ReadFile(filepath.Join(root, DocsDir, "manifest.json"))
	if err != nil {
		return []string{fmt.Sprintf("%s/manifest.json: %v", DocsDir, err)}
	}
	var m manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return []string{fmt.Sprintf("%s/manifest.json: %v", DocsDir, err)}
	}
	listed := make([]string, 0, len(m.Files))
	ids := make([]string, 0, len(m.Files))
	for _, f := range m.Files {
		listed = append(listed, f.Path)
		ids = append(ids, f.ID)
	}
	onDisk, err := filepath.Glob(filepath.Join(root, DocsDir, "*.md"))
	if err != nil {
		return []string{fmt.Sprintf("glob: %v", err)}
	}
	actual := make([]string, 0, len(onDisk))
	for _, p := range onDisk {
		rel, _ := filepath.Rel(root, p)
		actual = append(actual, rel)
	}
	sort.Strings(listed)
	sort.Strings(actual)
	if strings.Join(listed, "\n") != strings.Join(actual, "\n") {
		problems = append(problems, fmt.Sprintf("%s/manifest.json: files list does not match disk", DocsDir))
		for i := 0; i < len(listed) || i < len(actual); i++ {
			var l, a string
			if i < len(listed) {
				l = listed[i]
			}
			if i < len(actual) {
				a = actual[i]
			}
			if l != a {
				problems = append(problems, fmt.Sprintf("  manifest %q vs disk %q", l, a))
			}
		}
	}
	for i := 0; i < len(ids); i++ {
		for j := i + 1; j < len(ids); j++ {
			if ids[i] == ids[j] {
				problems = append(problems, fmt.Sprintf("%s/manifest.json: duplicate id %q", DocsDir, ids[i]))
			}
		}
	}
	return problems
}

func checkHistoryImmutable(root string) []string {
	var problems []string
	err := filepath.WalkDir(filepath.Join(root, HistoryDir), func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || filepath.Ext(path) != ".md" {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		have, err := os.ReadFile(path)
		if err != nil {
			problems = append(problems, fmt.Sprintf("%s: %v", rel, err))
			return nil
		}
		was, ok := blobAtHEAD(root, rel)
		if !ok {
			return nil
		}
		if string(was) != string(have) {
			problems = append(problems, fmt.Sprintf("%s: history file changed; history is immutable, add a new ADR instead", rel))
		}
		return nil
	})
	if err != nil {
		problems = append(problems, fmt.Sprintf("walk %s: %v", HistoryDir, err))
	}
	return problems
}

func blobAtHEAD(root, rel string) ([]byte, bool) {
	out, err := exec.Command("git", "-C", root, "show", "HEAD:"+filepath.ToSlash(rel)).Output()
	if err != nil {
		return nil, false
	}
	return out, true
}

func readFrontMatter(path string) (map[string]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	lines := strings.Split(string(data), "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != "---" {
		return nil, fmt.Errorf("no front-matter fence")
	}
	fm := map[string]string{}
	for _, line := range lines[1:] {
		if strings.TrimSpace(line) == "---" {
			return fm, nil
		}
		key, value, ok := strings.Cut(line, ":")
		if !ok || strings.HasPrefix(line, " ") || strings.HasPrefix(line, "#") {
			continue
		}
		fm[strings.TrimSpace(key)] = strings.TrimSpace(value)
	}
	return nil, fmt.Errorf("front-matter fence never closes")
}

func splitRefs(value string) []string {
	value = strings.TrimSpace(value)
	if value == "" || value == "null" || value == "[]" {
		return nil
	}
	value = strings.TrimPrefix(value, "[")
	value = strings.TrimSuffix(value, "]")
	var refs []string
	depth := 0
	start := 0
	for i, r := range value {
		switch r {
		case '(':
			depth++
		case ')':
			depth--
		case ',':
			if depth == 0 {
				refs = append(refs, strings.TrimSpace(value[start:i]))
				start = i + 1
			}
		}
	}
	if rest := strings.TrimSpace(value[start:]); rest != "" {
		refs = append(refs, rest)
	}
	return refs
}

func cleanRef(ref string) string {
	ref, _, _ = strings.Cut(ref, " §")
	ref, _, _ = strings.Cut(ref, ":")
	ref, _, _ = strings.Cut(ref, " ")
	return strings.TrimSpace(ref)
}

func countLines(data []byte) int {
	if len(data) == 0 {
		return 0
	}
	n := strings.Count(string(data), "\n")
	if !strings.HasSuffix(string(data), "\n") {
		n++
	}
	return n
}
