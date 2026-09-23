package memory

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

const factsSchemaVersion = 1

const factsSchema = `
CREATE TABLE IF NOT EXISTS facts (
	scope       TEXT NOT NULL,
	project     TEXT NOT NULL DEFAULT '',
	topic       TEXT NOT NULL,
	value       TEXT NOT NULL,
	provenance  TEXT NOT NULL DEFAULT '',
	updated_at  INTEGER NOT NULL,
	PRIMARY KEY (scope, project, topic)
);
CREATE TABLE IF NOT EXISTS review_queue (
	id          TEXT PRIMARY KEY,
	scope       TEXT NOT NULL,
	project     TEXT NOT NULL DEFAULT '',
	topic       TEXT NOT NULL,
	value       TEXT NOT NULL,
	provenance  TEXT NOT NULL DEFAULT '',
	reason      TEXT NOT NULL DEFAULT '',
	created_at  INTEGER NOT NULL,
	status      TEXT NOT NULL DEFAULT 'pending'
);
CREATE INDEX IF NOT EXISTS review_idx ON review_queue (status);
`

// Fact scopes.
const (
	ScopeGlobal  = "global"
	ScopeProject = "project"
)

// ValidScopes lists accepted fact scopes.
func ValidScopes() []string { return []string{ScopeProject, ScopeGlobal} }

// Fact is one durable memory statement.
type Fact struct {
	Scope      string `json:"scope"`
	Project    string `json:"project"`
	Topic      string `json:"topic"`
	Value      string `json:"value"`
	Provenance string `json:"provenance,omitempty"`
	UpdatedAt  int64  `json:"updated_at"`
}

// ReviewEntry is a captured fact candidate awaiting manual approval.
type ReviewEntry struct {
	ID         string   `json:"id"`
	Scope      string   `json:"scope"`
	Project    string   `json:"project,omitempty"`
	Topic      string   `json:"topic"`
	Value      string   `json:"value"`
	Provenance string   `json:"provenance,omitempty"`
	Reason     []string `json:"reason"`
	CreatedAt  int64    `json:"created_at"`
}

// Facts is the durable scoped-statement store backed by facts.db.
type Facts struct {
	db   *sql.DB
	path string
	dir  string
}

// OpenFacts opens (creating if needed) <dir>/facts.db.
func OpenFacts(ctx context.Context, dir string) (*Facts, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	path := filepath.Join(dir, "facts.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	f := &Facts{db: db, path: path, dir: dir}
	if err := f.migrate(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	chmodFacts(f.dir, f.path)
	return f, nil
}

func (f *Facts) migrate(ctx context.Context) error {
	var v int
	if err := f.db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&v); err != nil {
		return fmt.Errorf("read facts.db schema: %w", err)
	}
	if v > factsSchemaVersion {
		return fmt.Errorf("facts.db schema %d is newer than supported %d", v, factsSchemaVersion)
	}
	if v < factsSchemaVersion {
		var mode string
		_ = f.db.QueryRowContext(ctx, "PRAGMA journal_mode(WAL)").Scan(&mode)
		for _, pragma := range []string{"synchronous(NORMAL)", "busy_timeout(10000)"} {
			_, _ = f.db.ExecContext(ctx, "PRAGMA "+pragma)
		}
		if _, err := f.db.ExecContext(ctx, factsSchema); err != nil {
			return fmt.Errorf("create facts.db schema: %w", err)
		}
		if _, err := f.db.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", factsSchemaVersion)); err != nil {
			return fmt.Errorf("set facts.db schema version: %w", err)
		}
	}
	return nil
}

// Close releases the database.
func (f *Facts) Close() error {
	err := f.db.Close()
	chmodFacts(f.dir, f.path)
	return err
}

// Save stores a fact under (scope, project, topic). Secret-looking values are
// never stored silently: they are queued for review and reported via the
// returned queued flag.
func (f *Facts) Save(ctx context.Context, scope, project, topic, value, provenance string) (Fact, bool, error) {
	scope = strings.TrimSpace(scope)
	switch scope {
	case ScopeGlobal:
		project = ""
	case ScopeProject:
		project = strings.TrimSpace(project)
		if project == "" {
			return Fact{}, false, errors.New("project scope requires a project name")
		}
	default:
		return Fact{}, false, fmt.Errorf("invalid scope %q (want %s|%s)", scope, ScopeProject, ScopeGlobal)
	}
	topic = strings.TrimSpace(topic)
	if topic == "" {
		return Fact{}, false, errors.New("topic must not be empty")
	}
	if len(topic) > 256 {
		return Fact{}, false, errors.New("topic too long (max 256)")
	}
	if len(value) > 4096 {
		return Fact{}, false, errors.New("value too long (max 4096)")
	}
	provenance = strings.TrimSpace(provenance)
	now := time.Now().Unix()
	fact := Fact{Scope: scope, Project: project, Topic: topic, Value: value, Provenance: provenance, UpdatedAt: now}

	if reasons := DetectSecret(value); len(reasons) > 0 {
		ent := ReviewEntry{ID: newID(), Scope: scope, Project: project, Topic: topic,
			Value: value, Provenance: provenance, Reason: reasons, CreatedAt: now}
		if _, err := f.db.ExecContext(ctx, `
			INSERT INTO review_queue (id, scope, project, topic, value, provenance, reason, created_at, status)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, 'pending')`,
			ent.ID, scope, project, topic, value, provenance, strings.Join(reasons, ","), now); err != nil {
			return Fact{}, false, err
		}
		return fact, true, nil
	}

	if _, err := f.db.ExecContext(ctx, `
		INSERT INTO facts (scope, project, topic, value, provenance, updated_at)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(scope, project, topic) DO UPDATE SET
			value = excluded.value,
			provenance = excluded.provenance,
			updated_at = excluded.updated_at`,
		scope, project, topic, value, provenance, now); err != nil {
		return Fact{}, false, err
	}
	return fact, false, nil
}

// byRelevance orders most-recent first, project-scoped ahead of global.
func byRelevance(out []Fact) {
	sort.Slice(out, func(i, j int) bool {
		if out[i].UpdatedAt != out[j].UpdatedAt {
			return out[i].UpdatedAt > out[j].UpdatedAt
		}
		if (out[i].Scope == ScopeProject) != (out[j].Scope == ScopeProject) {
			return out[i].Scope == ScopeProject
		}
		return out[i].Topic < out[j].Topic
	})
}

// Recall returns facts matching topic in scope-priority order: the given
// project (when named) first, then global. An empty project means every
// project's fact for that topic plus the global set.
func (f *Facts) Recall(ctx context.Context, project, topic string) ([]Fact, error) {
	topic = strings.TrimSpace(topic)
	if topic == "" {
		return nil, errors.New("topic must not be empty")
	}
	project = strings.TrimSpace(project)
	rows, err := f.db.QueryContext(ctx, `
		SELECT scope, project, topic, value, provenance, updated_at
		FROM facts
		WHERE topic = ?
		  AND (scope = ? OR (scope = ? AND ? = '') OR (scope = ? AND project = ?))`,
		topic, ScopeGlobal, ScopeProject, project, ScopeProject, project)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Fact
	for rows.Next() {
		var fc Fact
		if err := rows.Scan(&fc.Scope, &fc.Project, &fc.Topic, &fc.Value, &fc.Provenance, &fc.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, fc)
	}
	byRelevance(out)
	return out, rows.Err()
}

// All returns every project-scoped fact plus the global set.
func (f *Facts) All(ctx context.Context, project string) ([]Fact, error) {
	rows, err := f.db.QueryContext(ctx, `
		SELECT scope, project, topic, value, provenance, updated_at
		FROM facts
		WHERE scope = ? OR (scope = ? AND project = ?)`,
		ScopeGlobal, ScopeProject, strings.TrimSpace(project))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Fact
	for rows.Next() {
		var fc Fact
		if err := rows.Scan(&fc.Scope, &fc.Project, &fc.Topic, &fc.Value, &fc.Provenance, &fc.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, fc)
	}
	byRelevance(out)
	return out, rows.Err()
}

// PendingReviews lists captured facts waiting for approval, newest first.
func (f *Facts) PendingReviews(ctx context.Context) ([]ReviewEntry, error) {
	rows, err := f.db.QueryContext(ctx, `
		SELECT id, scope, project, topic, value, provenance, reason, created_at
		FROM review_queue
		WHERE status = 'pending'
		ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ReviewEntry
	for rows.Next() {
		var e ReviewEntry
		var reason string
		if err := rows.Scan(&e.ID, &e.Scope, &e.Project, &e.Topic, &e.Value, &e.Provenance, &reason, &e.CreatedAt); err != nil {
			return nil, err
		}
		if reason != "" {
			e.Reason = strings.Split(reason, ",")
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// Approve promotes a pending review into the facts table and marks it approved.
func (f *Facts) Approve(ctx context.Context, id string) (Fact, error) {
	tx, err := f.db.BeginTx(ctx, nil)
	if err != nil {
		return Fact{}, err
	}
	defer tx.Rollback()

	var e struct {
		scope, project, topic, value, provenance string
		created                                  int64
	}
	err = tx.QueryRowContext(ctx, `
		SELECT scope, project, topic, value, provenance, created_at
		FROM review_queue WHERE id = ? AND status = 'pending'`, id).
		Scan(&e.scope, &e.project, &e.topic, &e.value, &e.provenance, &e.created)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Fact{}, fmt.Errorf("no pending review entry %q", id)
		}
		return Fact{}, err
	}
	fact := Fact{Scope: e.scope, Project: e.project, Topic: e.topic, Value: e.value,
		Provenance: e.provenance, UpdatedAt: e.created}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO facts (scope, project, topic, value, provenance, updated_at)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(scope, project, topic) DO UPDATE SET
			value = excluded.value,
			provenance = excluded.provenance,
			updated_at = excluded.updated_at`,
		fact.Scope, fact.Project, fact.Topic, fact.Value, fact.Provenance, e.created); err != nil {
		return Fact{}, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE review_queue SET status = 'approved' WHERE id = ?`, id); err != nil {
		return Fact{}, err
	}
	if err := tx.Commit(); err != nil {
		return Fact{}, err
	}
	return fact, nil
}

// Reject drops a pending review without storing anything.
func (f *Facts) Reject(ctx context.Context, id string) error {
	res, err := f.db.ExecContext(ctx,
		`UPDATE review_queue SET status = 'rejected' WHERE id = ? AND status = 'pending'`, id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("no pending review entry %q", id)
	}
	return nil
}

// DataDir exposes the backing directory (for permission checks in tests).
func (f *Facts) DataDir() string { return f.dir }

func chmodFacts(dir, path string) {
	_ = os.Chmod(path, 0o600)
	_ = os.Chmod(path+"-wal", 0o600)
	_ = os.Chmod(path+"-shm", 0o600)
	_ = os.Chmod(dir, 0o700)
}

func newID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}
