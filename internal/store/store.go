// Package store is the SQLite-backed state store (design §10): history, jobs,
// the append-only action log, opt-in memory and small key/value state.
// It uses pure-Go SQLite (no cgo) in WAL mode.
package store

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	_ "modernc.org/sqlite" // pure-Go driver
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// ErrNotFound is returned when a requested row does not exist.
var ErrNotFound = errors.New("store: not found")

// Store is a handle to the database. It is safe for concurrent use.
type Store struct {
	db  *sql.DB
	now func() time.Time
}

// Open opens (creating if needed) the database at path and migrates it to the
// latest schema version.
func Open(ctx context.Context, path string) (*Store, error) {
	q := url.Values{}
	for _, p := range []string{
		"busy_timeout(5000)",
		"journal_mode(WAL)",
		"synchronous(NORMAL)",
		"foreign_keys(ON)",
	} {
		q.Add("_pragma", p)
	}
	db, err := sql.Open("sqlite", "file:"+path+"?"+q.Encode())
	if err != nil {
		return nil, fmt.Errorf("store: open: %w", err)
	}
	// One writer at a time keeps SQLITE_BUSY out of the picture for a local app.
	db.SetMaxOpenConns(1)
	s := &Store{db: db, now: time.Now}
	if err := s.migrate(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

// Close closes the database.
func (s *Store) Close() error { return s.db.Close() }

type migration struct {
	version int
	name    string
	sql     string
}

func loadMigrations() ([]migration, error) {
	entries, err := migrationsFS.ReadDir("migrations")
	if err != nil {
		return nil, err
	}
	var ms []migration
	for _, e := range entries {
		name := e.Name()
		numStr, _, ok := strings.Cut(name, "_")
		if !ok || !strings.HasSuffix(name, ".sql") {
			return nil, fmt.Errorf("store: bad migration name %q", name)
		}
		v, err := strconv.Atoi(numStr)
		if err != nil {
			return nil, fmt.Errorf("store: bad migration name %q", name)
		}
		b, err := migrationsFS.ReadFile("migrations/" + name)
		if err != nil {
			return nil, err
		}
		ms = append(ms, migration{v, name, string(b)})
	}
	sort.Slice(ms, func(i, j int) bool { return ms[i].version < ms[j].version })
	return ms, nil
}

func (s *Store) migrate(ctx context.Context) error {
	ms, err := loadMigrations()
	if err != nil {
		return err
	}
	return applyMigrations(ctx, s.db, ms)
}

func applyMigrations(ctx context.Context, db *sql.DB, ms []migration) error {
	if _, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version    INTEGER PRIMARY KEY,
		name       TEXT    NOT NULL,
		applied_at INTEGER NOT NULL)`); err != nil {
		return fmt.Errorf("store: schema_migrations: %w", err)
	}
	var cur int
	if err := db.QueryRowContext(ctx, `SELECT COALESCE(MAX(version),0) FROM schema_migrations`).Scan(&cur); err != nil {
		return err
	}
	for _, m := range ms {
		if m.version <= cur {
			continue
		}
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, m.sql); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("store: migration %s: %w", m.name, err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO schema_migrations (version, name, applied_at) VALUES (?,?,?)`,
			m.version, m.name, time.Now().UnixMilli()); err != nil {
			_ = tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}

// SchemaVersion returns the highest applied migration version.
func (s *Store) SchemaVersion(ctx context.Context) (int, error) {
	var v int
	err := s.db.QueryRowContext(ctx, `SELECT COALESCE(MAX(version),0) FROM schema_migrations`).Scan(&v)
	return v, err
}

func ms(t time.Time) int64     { return t.UnixMilli() }
func fromMS(v int64) time.Time { return time.UnixMilli(v).UTC() }

// ---- History ----

// HistoryEntry is a transcript or command record.
type HistoryEntry struct {
	ID   int64
	Time time.Time
	Kind string
	Text string
}

// AddHistory appends a history entry and returns its ID.
func (s *Store) AddHistory(ctx context.Context, kind, text string) (int64, error) {
	r, err := s.db.ExecContext(ctx, `INSERT INTO history (ts, kind, text) VALUES (?,?,?)`, ms(s.now()), kind, text)
	if err != nil {
		return 0, err
	}
	return r.LastInsertId()
}

// RecentHistory returns up to limit entries, newest first. An empty kind
// matches all kinds.
func (s *Store) RecentHistory(ctx context.Context, kind string, limit int) ([]HistoryEntry, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, ts, kind, text FROM history WHERE (? = '' OR kind = ?) ORDER BY ts DESC, id DESC LIMIT ?`,
		kind, kind, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []HistoryEntry
	for rows.Next() {
		var e HistoryEntry
		var t int64
		if err := rows.Scan(&e.ID, &t, &e.Kind, &e.Text); err != nil {
			return nil, err
		}
		e.Time = fromMS(t)
		out = append(out, e)
	}
	return out, rows.Err()
}

// ---- Jobs ----

// Job is a background job record.
type Job struct {
	ID        string
	Kind      string
	Status    string
	CreatedAt time.Time
	UpdatedAt time.Time
	Input     string
	Output    string
	Error     string
}

// CreateJob inserts a new job.
func (s *Store) CreateJob(ctx context.Context, id, kind, status, input string) error {
	t := ms(s.now())
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO jobs (id, kind, status, created_at, updated_at, input) VALUES (?,?,?,?,?,?)`,
		id, kind, status, t, t, input)
	return err
}

// UpdateJob sets a job's status, output and error.
func (s *Store) UpdateJob(ctx context.Context, id, status, output, errMsg string) error {
	r, err := s.db.ExecContext(ctx,
		`UPDATE jobs SET status=?, output=?, error=?, updated_at=? WHERE id=?`,
		status, output, errMsg, ms(s.now()), id)
	if err != nil {
		return err
	}
	if n, _ := r.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// GetJob returns a job by ID.
func (s *Store) GetJob(ctx context.Context, id string) (Job, error) {
	var j Job
	var c, u int64
	err := s.db.QueryRowContext(ctx,
		`SELECT id, kind, status, created_at, updated_at, input, output, error FROM jobs WHERE id=?`, id).
		Scan(&j.ID, &j.Kind, &j.Status, &c, &u, &j.Input, &j.Output, &j.Error)
	if errors.Is(err, sql.ErrNoRows) {
		return Job{}, ErrNotFound
	}
	if err != nil {
		return Job{}, err
	}
	j.CreatedAt, j.UpdatedAt = fromMS(c), fromMS(u)
	return j, nil
}

// ListJobs returns jobs with the given status (all if empty), oldest first.
func (s *Store) ListJobs(ctx context.Context, status string) ([]Job, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, kind, status, created_at, updated_at, input, output, error FROM jobs
		 WHERE (? = '' OR status = ?) ORDER BY created_at, id`, status, status)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []Job
	for rows.Next() {
		var j Job
		var c, u int64
		if err := rows.Scan(&j.ID, &j.Kind, &j.Status, &c, &u, &j.Input, &j.Output, &j.Error); err != nil {
			return nil, err
		}
		j.CreatedAt, j.UpdatedAt = fromMS(c), fromMS(u)
		out = append(out, j)
	}
	return out, rows.Err()
}

// ---- Action log (append-only) ----

// ActionEntry is one action-log record. Args must already have secrets
// redacted by the caller.
type ActionEntry struct {
	ID           int64
	Time         time.Time // set by the store on append
	Actor        string    // who: the recognized speaker
	Action       string    // what
	Tool         string
	Args         string
	Result       string
	Confirmation string // how it was confirmed
	Reason       string // the request that led to it
}

// AppendAction appends an entry to the action log and returns its ID.
// There is deliberately no update or delete method.
func (s *Store) AppendAction(ctx context.Context, e ActionEntry) (int64, error) {
	r, err := s.db.ExecContext(ctx,
		`INSERT INTO action_log (ts, actor, action, tool, args, result, confirmation, reason) VALUES (?,?,?,?,?,?,?,?)`,
		ms(s.now()), e.Actor, e.Action, e.Tool, e.Args, e.Result, e.Confirmation, e.Reason)
	if err != nil {
		return 0, err
	}
	return r.LastInsertId()
}

// RecentActions returns up to limit action-log entries, newest first.
func (s *Store) RecentActions(ctx context.Context, limit int) ([]ActionEntry, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, ts, actor, action, tool, args, result, confirmation, reason
		 FROM action_log ORDER BY ts DESC, id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []ActionEntry
	for rows.Next() {
		var e ActionEntry
		var t int64
		if err := rows.Scan(&e.ID, &t, &e.Actor, &e.Action, &e.Tool, &e.Args, &e.Result, &e.Confirmation, &e.Reason); err != nil {
			return nil, err
		}
		e.Time = fromMS(t)
		out = append(out, e)
	}
	return out, rows.Err()
}

// ---- Retention ----

// Pruned reports how many rows a retention pass removed.
type Pruned struct {
	History   int64
	ActionLog int64
}

// Prune deletes history and action-log rows older than the given ages. A zero
// or negative age leaves that table untouched. This is the only path that can
// delete action-log rows.
func (s *Store) Prune(ctx context.Context, historyOlderThan, actionLogOlderThan time.Duration) (Pruned, error) {
	var p Pruned
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return p, err
	}
	defer func() { _ = tx.Rollback() }()
	now := s.now()
	if historyOlderThan > 0 {
		r, err := tx.ExecContext(ctx, `DELETE FROM history WHERE ts < ?`, ms(now.Add(-historyOlderThan)))
		if err != nil {
			return p, err
		}
		p.History, _ = r.RowsAffected()
	}
	if actionLogOlderThan > 0 {
		cutoff := ms(now.Add(-actionLogOlderThan))
		if _, err := tx.ExecContext(ctx, `UPDATE action_log_prune_gate SET cutoff=? WHERE id=1`, cutoff); err != nil {
			return p, err
		}
		r, err := tx.ExecContext(ctx, `DELETE FROM action_log WHERE ts < ?`, cutoff)
		if err != nil {
			return p, err
		}
		p.ActionLog, _ = r.RowsAffected()
		if _, err := tx.ExecContext(ctx, `UPDATE action_log_prune_gate SET cutoff=NULL WHERE id=1`); err != nil {
			return p, err
		}
	}
	return p, tx.Commit()
}

// ---- Memory (opt-in facts) ----

// Fact is a remembered fact.
type Fact struct {
	Key       string
	Value     string
	CreatedAt time.Time
	UpdatedAt time.Time
}

// Remember stores or replaces a fact.
func (s *Store) Remember(ctx context.Context, key, value string) error {
	t := ms(s.now())
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO memory (key, value, created_at, updated_at) VALUES (?,?,?,?)
		 ON CONFLICT(key) DO UPDATE SET value=excluded.value, updated_at=excluded.updated_at`,
		key, value, t, t)
	return err
}

// Forget deletes a fact. It returns ErrNotFound if there was none.
func (s *Store) Forget(ctx context.Context, key string) error {
	r, err := s.db.ExecContext(ctx, `DELETE FROM memory WHERE key=?`, key)
	if err != nil {
		return err
	}
	if n, _ := r.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// Facts lists all remembered facts ordered by key.
func (s *Store) Facts(ctx context.Context) ([]Fact, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT key, value, created_at, updated_at FROM memory ORDER BY key`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []Fact
	for rows.Next() {
		var f Fact
		var c, u int64
		if err := rows.Scan(&f.Key, &f.Value, &c, &u); err != nil {
			return nil, err
		}
		f.CreatedAt, f.UpdatedAt = fromMS(c), fromMS(u)
		out = append(out, f)
	}
	return out, rows.Err()
}

// ---- Key/value ----

// SetKV stores a small setting.
func (s *Store) SetKV(ctx context.Context, key, value string) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO kv (key, value) VALUES (?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, key, value)
	return err
}

// GetKV returns a stored setting, or ErrNotFound.
func (s *Store) GetKV(ctx context.Context, key string) (string, error) {
	var v string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM kv WHERE key=?`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	return v, err
}

// DeleteKV removes a stored setting (a no-op if absent).
func (s *Store) DeleteKV(ctx context.Context, key string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM kv WHERE key=?`, key)
	return err
}
