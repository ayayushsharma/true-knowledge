// Notes store: markdown files under notes/<project>/ are the durable source
// of truth; notes/index.db (SQLite FTS5) is a derived, rebuildable index.
// Captured notes land in notes/review/<project>.jsonl and only become
// searchable after review approval — notes never enter the index silently.
package memory

import (
	"bufio"
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

const notesSchemaVersion = 2

const notesSchema = `
CREATE TABLE IF NOT EXISTS notes (
	id         TEXT PRIMARY KEY,
	project    TEXT NOT NULL,
	title      TEXT NOT NULL,
	file       TEXT NOT NULL,
	scope      TEXT NOT NULL DEFAULT 'project',
	created_at INTEGER NOT NULL,
	updated_at INTEGER NOT NULL,
	pinned     INTEGER NOT NULL DEFAULT 0,
	body       TEXT NOT NULL,
	embedding  BLOB,
	embed_model TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS notes_project_idx ON notes (project);
CREATE VIRTUAL TABLE IF NOT EXISTS notes_fts USING fts5(
	title, body
);
`

// Note is one approved, indexed knowledge entry.
type Note struct {
	ID        string `json:"id"`
	Project   string `json:"project"`
	Title     string `json:"title"`
	File      string `json:"file"`
	Scope     string `json:"scope"`
	CreatedAt int64  `json:"created_at"`
	UpdatedAt int64  `json:"updated_at"`
	Pinned    bool   `json:"pinned"`
	Body      string `json:"body,omitempty"`
	// Embedding and model tag are derived data, never serialized.
	Embedding  []float32 `json:"-"`
	EmbedModel string    `json:"-"`
}

// NoteHit is one ranked search result.
type NoteHit struct {
	Note    Note    `json:"note"`
	Score   float64 `json:"score,omitempty"`
	Excerpt string  `json:"excerpt,omitempty"`
}

// NoteReviewEntry is a captured note awaiting approval.
type NoteReviewEntry struct {
	ID        string   `json:"id"`
	Project   string   `json:"project"`
	Title     string   `json:"title"`
	Text      string   `json:"text,omitempty"`
	Reason    []string `json:"reason,omitempty"`
	CreatedAt int64    `json:"created_at"`
}

// Notes is the approved-notes store plus its FTS5 index.
type Notes struct {
	root  string
	db    *sql.DB
	embed *Embedder
}

// SetEmbedder attaches an embedder for semantic-aware search (nil = BM25 only).
func (n *Notes) SetEmbedder(e *Embedder) { n.embed = e }

// OpenNotes opens (creating if needed) the notes tree under root.
func OpenNotes(ctx context.Context, root string) (*Notes, error) {
	for _, d := range []string{root, filepath.Join(root, "review")} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return nil, err
		}
	}
	db, err := sql.Open("sqlite", filepath.Join(root, "index.db"))
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	n := &Notes{root: root, db: db}
	if err := n.migrate(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	chmodFacts(root, filepath.Join(root, "index.db"))
	return n, nil
}

func (n *Notes) migrate(ctx context.Context) error {
	var v int
	if err := n.db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&v); err != nil {
		return fmt.Errorf("read notes index schema: %w", err)
	}
	if v > notesSchemaVersion {
		return fmt.Errorf("notes index schema %d is newer than supported %d", v, notesSchemaVersion)
	}
	if v < notesSchemaVersion {
		var mode string
		_ = n.db.QueryRowContext(ctx, "PRAGMA journal_mode(WAL)").Scan(&mode)
		for _, pragma := range []string{"synchronous(NORMAL)", "busy_timeout(10000)"} {
			_, _ = n.db.ExecContext(ctx, "PRAGMA "+pragma)
		}
		if v == 1 {
			if _, err := n.db.ExecContext(ctx, `
				ALTER TABLE notes ADD COLUMN embedding BLOB;
				ALTER TABLE notes ADD COLUMN embed_model TEXT NOT NULL DEFAULT ''`); err != nil {
				return fmt.Errorf("migrate notes index to v2: %w", err)
			}
		} else if _, err := n.db.ExecContext(ctx, notesSchema); err != nil {
			return fmt.Errorf("create notes index schema: %w", err)
		}
		if _, err := n.db.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", notesSchemaVersion)); err != nil {
			return fmt.Errorf("set notes index schema version: %w", err)
		}
	}
	return nil
}

// Close releases the index database.
func (n *Notes) Close() error {
	err := n.db.Close()
	chmodFacts(n.root, filepath.Join(n.root, "index.db"))
	return err
}

// noteFileName builds <slug>-<id8>.md under notes/<project>/.
func noteFileName(title, id string) string {
	slug := regexp.MustCompile(`[^a-z0-9]+`).ReplaceAllString(strings.ToLower(strings.TrimSpace(title)), "-")
	slug = strings.Trim(slug, "-")
	if len(slug) > 48 {
		slug = slug[:48]
	}
	if slug == "" {
		slug = "note"
	}
	if len(id) > 8 {
		return slug + "-" + id[:8] + ".md"
	}
	return slug + "-" + id + ".md"
}

// SaveToReview captures a note into the review queue. Secret-looking text is
// recorded as a reason; nothing is indexed or stored as a note file yet.
func (n *Notes) SaveToReview(ctx context.Context, project, title, text string) (NoteReviewEntry, error) {
	project, err := validProject(project)
	if err != nil {
		return NoteReviewEntry{}, err
	}
	title = strings.TrimSpace(title)
	if title == "" {
		return NoteReviewEntry{}, errors.New("note title must not be empty")
	}
	if len(text) > 262144 {
		return NoteReviewEntry{}, errors.New("note text too long (max 262144)")
	}
	ent := NoteReviewEntry{
		ID:        newID(),
		Project:   project,
		Title:     title,
		Text:      text,
		Reason:    DetectSecret(text),
		CreatedAt: time.Now().Unix(),
	}
	raw, _ := json.Marshal(ent)
	return ent, appendLine(filepath.Join(n.reviewDir(), project+".jsonl"), raw)
}

// PendingReviews lists every captured note across projects, newest first.
func (n *Notes) PendingReviews(ctx context.Context) ([]NoteReviewEntry, error) {
	entries, err := n.readAllReviews()
	if err != nil {
		return nil, err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].CreatedAt > entries[j].CreatedAt })
	return entries, nil
}

// Approve promotes one captured note: writes the markdown source, indexes it,
// and removes it from the review queue.
func (n *Notes) Approve(ctx context.Context, id string) (Note, error) {
	ent, file, err := n.findReview(id)
	if err != nil {
		return Note{}, err
	}
	now := time.Now().Unix()
	note := Note{
		ID:        ent.ID,
		Project:   ent.Project,
		Title:     ent.Title,
		File:      noteFileName(ent.Title, ent.ID),
		Scope:     "project",
		CreatedAt: now,
		UpdatedAt: now,
		Pinned:    false,
		Body:      ent.Text,
	}
	mdPath := filepath.Join(n.notesDir(note.Project), note.File)
	if err := os.MkdirAll(filepath.Dir(mdPath), 0o700); err != nil {
		return Note{}, err
	}
	if err := atomicWrite(mdPath, marshalNote(note)); err != nil {
		return Note{}, fmt.Errorf("write note: %w", err)
	}
	n.attachEmbedding(ctx, &note)
	if err := n.indexNote(ctx, note); err != nil {
		return Note{}, fmt.Errorf("index note: %w", err)
	}
	if err := dropLine(file, ent.ID); err != nil {
		return Note{}, fmt.Errorf("review queue: %w", err)
	}
	return note, nil
}

// Reject drops one captured note without writing or indexing anything.
func (n *Notes) Reject(ctx context.Context, id string) error {
	ent, file, err := n.findReview(id)
	if err != nil {
		return err
	}
	return dropLine(file, ent.ID)
}

// attachEmbedding fills note.Embedding/EmbedModel when an embedder is set.
// Any embed failure leaves the note keyword-only; search still works.
func (n *Notes) attachEmbedding(ctx context.Context, note *Note) {
	if n.embed == nil {
		return
	}
	vec, err := n.embed.EmbedOne(ctx, note.Body)
	if err != nil {
		return
	}
	note.Embedding = vec
	note.EmbedModel = n.embed.Model
}

// indexNote inserts a note row + FTS entry in one transaction.
func (n *Notes) indexNote(ctx context.Context, note Note) error {
	tx, err := n.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `
		INSERT OR REPLACE INTO notes (id, project, title, file, scope, created_at, updated_at, pinned, body, embedding, embed_model)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		note.ID, note.Project, note.Title, note.File, note.Scope, note.CreatedAt, note.UpdatedAt, boolInt(note.Pinned), note.Body,
		encodeVecOrNil(note.Embedding), note.EmbedModel); err != nil {
		return err
	}
	var rowid int64
	if err := tx.QueryRowContext(ctx, `SELECT rowid FROM notes WHERE id = ?`, note.ID).Scan(&rowid); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM notes_fts WHERE rowid = ?`, rowid); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO notes_fts (rowid, title, body) VALUES (?, ?, ?)`, rowid, note.Title, note.Body); err != nil {
		return err
	}
	return tx.Commit()
}

// searchRow is an intermediate ranking candidate.
type searchRow struct {
	note  Note
	score float64
}

// Search ranks approved notes with FTS5 BM25, fused (RRF) with cosine
// similarity over stored embeddings when an embedder is configured and the
// index has notes embedded by a matching model. Any embedder failure, model
// mismatch, or empty index silently falls back to BM25 — never fails.
// The query is AND'd over whitespace terms so arbitrary keyboard input
// cannot break MATCH syntax.
func (n *Notes) Search(ctx context.Context, project, query string, limit int) ([]NoteHit, error) {
	if limit <= 0 || limit > 100 {
		limit = 10
	}
	fts := ftsQuery(query)
	if fts == "" {
		return nil, errors.New("empty search query")
	}
	project = strings.TrimSpace(project)
	const pool = 50

	rows, err := n.db.QueryContext(ctx, `
		SELECT n.id, n.project, n.title, n.file, n.scope, n.created_at, n.updated_at, n.pinned, n.body,
		       bm25(notes_fts) AS score
		FROM notes_fts
		JOIN notes n ON n.rowid = notes_fts.rowid
		WHERE notes_fts MATCH ?
		  AND (? = '' OR n.project = ?)
		ORDER BY n.pinned DESC, score
		LIMIT ?`, fts, project, project, pool)
	if err != nil {
		return nil, err
	}
	var bmRows []searchRow
	for rows.Next() {
		var r searchRow
		if err := rows.Scan(&r.note.ID, &r.note.Project, &r.note.Title, &r.note.File, &r.note.Scope,
			&r.note.CreatedAt, &r.note.UpdatedAt, &r.note.Pinned, &r.note.Body, &r.score); err != nil {
			rows.Close()
			return nil, err
		}
		bmRows = append(bmRows, r)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()

	semRows, semIdeal := n.semanticRows(ctx, query, project, pool)
	if !semIdeal {
		if len(bmRows) > limit {
			bmRows = bmRows[:limit]
		}
		return toHits(bmRows), nil
	}

	bmIDs := make([]string, len(bmRows))
	for i, r := range bmRows {
		bmIDs[i] = r.note.ID
	}
	semIDs := make([]string, len(semRows))
	for i, r := range semRows {
		semIDs[i] = r.note.ID
	}
	byID := map[string]searchRow{}
	for _, r := range bmRows {
		byID[r.note.ID] = r
	}
	for _, r := range semRows {
		byID[r.note.ID] = r
	}
	sc := rrScoreMap(bmIDs, semIDs)
	var ids []string
	for id := range sc {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool {
		if sc[ids[i]] != sc[ids[j]] {
			return sc[ids[i]] > sc[ids[j]]
		}
		return ids[i] < ids[j]
	})
	if len(ids) > limit {
		ids = ids[:limit]
	}
	var out []NoteHit
	for _, id := range ids {
		r := byID[id]
		out = append(out, NoteHit{Note: r.note, Score: sc[id], Excerpt: excerpt(r.note.Body)})
	}
	return out, nil
}

// semanticRows ranks stored embeddings by cosine against the query. ok=false
// means embeddings are not usable (no embedder, endpoint down, model
// mismatch, or empty matching set) and search must fall back to BM25.
func (n *Notes) semanticRows(ctx context.Context, query, project string, cap int) ([]searchRow, bool) {
	var out []searchRow
	if n.embed == nil {
		return out, false
	}
	qv, err := n.embed.EmbedOne(ctx, query)
	if err != nil {
		return out, false
	}
	srows, err := n.db.QueryContext(ctx, `
		SELECT id, project, title, file, scope, created_at, updated_at, pinned, body, embedding
		FROM notes
		WHERE (? = '' OR project = ?) AND embed_model = ?`, project, project, n.embed.Model)
	if err != nil {
		return out, false
	}
	defer srows.Close()
	for srows.Next() {
		var r searchRow
		var blob []byte
		if err := srows.Scan(&r.note.ID, &r.note.Project, &r.note.Title, &r.note.File, &r.note.Scope,
			&r.note.CreatedAt, &r.note.UpdatedAt, &r.note.Pinned, &r.note.Body, &blob); err != nil {
			return out, false
		}
		vec, ok := decodeVec(blob)
		if !ok || len(vec) != len(qv) {
			continue
		}
		r.note.Body = ""
		r.score = float64(cosine(qv, vec))
		out = append(out, r)
	}
	if err := srows.Err(); err != nil || len(out) == 0 {
		return out, false
	}
	sort.Slice(out, func(i, j int) bool { return out[i].score > out[j].score })
	if len(out) > cap {
		out = out[:cap]
	}
	return out, true
}

func toHits(rows []searchRow) []NoteHit {
	out := make([]NoteHit, 0, len(rows))
	for _, r := range rows {
		out = append(out, NoteHit{Note: r.note, Score: r.score, Excerpt: excerpt(r.note.Body)})
	}
	return out
}

// TOC lists note titles newest-first, pinned first, trimmed to maxChars.
// Only note titles ever appear — never bodies.
func (n *Notes) TOC(ctx context.Context, project string, maxChars int) ([]string, []Note, error) {
	if maxChars <= 0 {
		maxChars = 700
	}
	project = strings.TrimSpace(project)
	rows, err := n.db.QueryContext(ctx, `
		SELECT id, project, title, file, scope, created_at, updated_at, pinned
		FROM notes
		WHERE ? = '' OR project = ?
		ORDER BY pinned DESC, updated_at DESC`, project, project)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	var notes []Note
	for rows.Next() {
		var nr Note
		if err := rows.Scan(&nr.ID, &nr.Project, &nr.Title, &nr.File, &nr.Scope,
			&nr.CreatedAt, &nr.UpdatedAt, &nr.Pinned); err != nil {
			return nil, nil, err
		}
		notes = append(notes, nr)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	var lines []string
	used := 0
	for _, nr := range notes {
		line := nr.Title
		if nr.Pinned {
			line += " [pinned]"
		}
		if used > 0 {
			used++
		}
		if used+len(line) > maxChars {
			break
		}
		used += len(line)
		lines = append(lines, line)
	}
	return lines, notes, nil
}

// Reindex rebuilds the index from the markdown files themselves (the source
// of truth), for one project or all. Returns the note count.
func (n *Notes) Reindex(ctx context.Context, project string) (int, error) {
	project = strings.TrimSpace(project)
	if _, err := n.db.ExecContext(ctx, `DROP TABLE IF EXISTS notes_fts`); err != nil {
		return 0, err
	}
	if _, err := n.db.ExecContext(ctx, `DROP TABLE IF EXISTS notes`); err != nil {
		return 0, err
	}
	if _, err := n.db.ExecContext(ctx, notesSchema); err != nil {
		return 0, err
	}
	type entry struct {
		note Note
	}
	var entries []entry
	err := walkProjects(n.notesRoot(), func(proj, file string) error {
		if project != "" && proj != project {
			return nil
		}
		raw, err := os.ReadFile(filepath.Join(n.notesDir(proj), file))
		if err != nil {
			return err
		}
		note, err := parseNoteFile(raw)
		if err != nil {
			return fmt.Errorf("%s: %w", file, err)
		}
		entries = append(entries, entry{note})
		return nil
	})
	if err != nil {
		return 0, err
	}
	// Batch-embed all bodies before the single insert transaction so the
	// transaction never holds while an HTTP embed call is in flight.
	if n.embed != nil && len(entries) > 0 {
		bodies := make([]string, len(entries))
		for i, e := range entries {
			bodies[i] = e.note.Body
		}
		if vecs, eerr := n.embed.Embed(ctx, bodies); eerr == nil {
			for i := range entries {
				entries[i].note.Embedding = vecs[i]
				entries[i].note.EmbedModel = n.embed.Model
			}
		}
	}
	tx, err := n.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	count := 0
	for _, e := range entries {
		if _, err := tx.ExecContext(ctx, `
			INSERT OR REPLACE INTO notes (id, project, title, file, scope, created_at, updated_at, pinned, body, embedding, embed_model)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			e.note.ID, e.note.Project, e.note.Title, e.note.File, e.note.Scope, e.note.CreatedAt, e.note.UpdatedAt,
			boolInt(e.note.Pinned), e.note.Body, encodeVecOrNil(e.note.Embedding), e.note.EmbedModel); err != nil {
			return 0, err
		}
		var rowid int64
		if err := tx.QueryRowContext(ctx, `SELECT rowid FROM notes WHERE id = ?`, e.note.ID).Scan(&rowid); err != nil {
			return 0, err
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO notes_fts (rowid, title, body) VALUES (?, ?, ?)`, rowid, e.note.Title, e.note.Body); err != nil {
			return 0, err
		}
		count++
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	_, _ = n.db.ExecContext(ctx, "PRAGMA optimize")
	return count, nil
}

func (n *Notes) notesRoot() string        { return filepath.Join(n.root) }
func (n *Notes) notesDir(p string) string { return filepath.Join(n.root, p) }
func (n *Notes) reviewDir() string        { return filepath.Join(n.root, "review") }

// reviewFiles lists every project review jsonl path.
func (n *Notes) reviewFiles() ([]string, error) {
	ents, err := os.ReadDir(n.reviewDir())
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range ents {
		if strings.HasSuffix(e.Name(), ".jsonl") {
			out = append(out, filepath.Join(n.reviewDir(), e.Name()))
		}
	}
	return out, nil
}

// readAllReviews scans every project review file.
func (n *Notes) readAllReviews() ([]NoteReviewEntry, error) {
	files, err := n.reviewFiles()
	if err != nil {
		return nil, err
	}
	var out []NoteReviewEntry
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			return nil, err
		}
		r := bufio.NewReader(bytes.NewReader(raw))
		for {
			line, err := r.ReadBytes('\n')
			if len(bytes.TrimSpace(line)) > 0 {
				var e NoteReviewEntry
				if jerr := json.Unmarshal(line, &e); jerr == nil {
					out = append(out, e)
				}
			}
			if err != nil {
				break
			}
		}
	}
	return out, nil
}

// findReview locates an entry and the file holding it.
func (n *Notes) findReview(id string) (NoteReviewEntry, string, error) {
	files, err := n.reviewFiles()
	if err != nil {
		return NoteReviewEntry{}, "", err
	}
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			return NoteReviewEntry{}, "", err
		}
		r := bufio.NewReader(bytes.NewReader(raw))
		for {
			line, err := r.ReadBytes('\n')
			if len(bytes.TrimSpace(line)) > 0 {
				var e NoteReviewEntry
				if jerr := json.Unmarshal(line, &e); jerr == nil {
					if e.ID == id {
						return e, f, nil
					}
				}
			}
			if err != nil {
				break
			}
		}
	}
	return NoteReviewEntry{}, "", fmt.Errorf("no pending note review %q", id)
}

// dropLine removes the entry with id from a review jsonl.
func dropLine(file, id string) error {
	raw, err := os.ReadFile(file)
	if err != nil {
		return err
	}
	var kept []byte
	r := bufio.NewReader(bytes.NewReader(raw))
	for {
		line, err := r.ReadBytes('\n')
		if len(bytes.TrimSpace(line)) > 0 {
			var e NoteReviewEntry
			if jerr := json.Unmarshal(line, &e); jerr == nil && e.ID == id {
				// skip
			} else {
				kept = append(kept, line...)
			}
		}
		if err != nil {
			break
		}
	}
	return atomicWrite(file, kept)
}

// walkProjects visits every project dir and its .md files (non-recursive).
func walkProjects(root string, fn func(project, file string) error) error {
	ents, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	for _, e := range ents {
		if !e.IsDir() {
			continue
		}
		files, err := os.ReadDir(filepath.Join(root, e.Name()))
		if err != nil {
			return err
		}
		for _, f := range files {
			if strings.HasSuffix(f.Name(), ".md") {
				if err := fn(e.Name(), f.Name()); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// marshalNote renders front-matter JSON then a --- separator, then the body.
func marshalNote(n Note) []byte {
	fm := struct {
		ID        string `json:"id"`
		Title     string `json:"title"`
		Project   string `json:"project"`
		File      string `json:"file"`
		Scope     string `json:"scope"`
		CreatedAt int64  `json:"created_at"`
		UpdatedAt int64  `json:"updated_at"`
		Pinned    bool   `json:"pinned"`
	}{n.ID, n.Title, n.Project, n.File, n.Scope, n.CreatedAt, n.UpdatedAt, n.Pinned}
	raw, _ := json.MarshalIndent(fm, "", "  ")
	out := make([]byte, 0, len(raw)+4+len(n.Body))
	out = append(out, raw...)
	out = append(out, '\n', '-', '-', '-', '\n')
	out = append(out, n.Body...)
	if len(n.Body) == 0 || n.Body[len(n.Body)-1] != '\n' {
		out = append(out, '\n')
	}
	return out
}

// parseNoteFile reads back what marshalNote writes.
func parseNoteFile(raw []byte) (Note, error) {
	seg := bytes.SplitN(raw, []byte("\n---\n"), 2)
	if len(seg) != 2 {
		return Note{}, errors.New("missing front-matter separator")
	}
	var fm struct {
		ID        string `json:"id"`
		Title     string `json:"title"`
		Project   string `json:"project"`
		File      string `json:"file"`
		Scope     string `json:"scope"`
		CreatedAt int64  `json:"created_at"`
		UpdatedAt int64  `json:"updated_at"`
		Pinned    bool   `json:"pinned"`
	}
	if err := json.Unmarshal(seg[0], &fm); err != nil {
		return Note{}, fmt.Errorf("bad front matter: %w", err)
	}
	if fm.ID == "" || fm.Title == "" {
		return Note{}, errors.New("front matter missing id/title")
	}
	return Note{ID: fm.ID, Title: fm.Title, Project: fm.Project, File: fm.File, Scope: fm.Scope,
		CreatedAt: fm.CreatedAt, UpdatedAt: fm.UpdatedAt, Pinned: fm.Pinned,
		Body: strings.TrimSuffix(string(seg[1]), "\n")}, nil
}

// validProject guards against traversal/missing names for memory stores.
func validProject(p string) (string, error) {
	p = strings.TrimSpace(p)
	if p == "" || strings.ContainsAny(p, `/\\`) {
		return "", errors.New("invalid project name")
	}
	return p, nil
}

// atomicWrite writes tmp + rename at 0600.
func atomicWrite(path string, raw []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func appendLine(path string, raw []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.Write(append(raw, '\n'))
	return err
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// encodeVecOrNil stores an embedding as a BLOB (empty vector -> NULL).
func encodeVecOrNil(v []float32) []byte {
	if len(v) == 0 {
		return nil
	}
	return encodeVec(v)
}

// ftsQuery ANDs whitespace terms as quoted phrases.
func ftsQuery(q string) string {
	var parts []string
	for _, t := range strings.Fields(q) {
		t = strings.Trim(t, `"`)
		if t != "" {
			parts = append(parts, `"`+t+`"`)
		}
	}
	return strings.Join(parts, " ")
}

func excerpt(body string) string {
	b := strings.Join(strings.Fields(body), " ")
	if len(b) > 200 {
		return b[:200] + "..."
	}
	return b
}
