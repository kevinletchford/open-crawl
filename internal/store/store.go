// Package store keeps audit reports in SQLite so runs can be listed,
// re-rendered, compared and queried, by the CLI and by the MCP server.
//
// It uses modernc.org/sqlite, a pure-Go driver, so the product still builds
// without cgo. The full report is stored as JSON; the pages, access and
// findings tables duplicate the parts that need querying.
package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"

	"github.com/kevinletchford/open-crawl/internal/audit"
)

// ErrNotFound is returned when an audit ID doesn't exist.
var ErrNotFound = errors.New("audit not found")

// DefaultPath is $OPENCRAWL_DB, or ~/.opencrawl/opencrawl.db.
func DefaultPath() string {
	if p := os.Getenv("OPENCRAWL_DB"); p != "" {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "opencrawl.db"
	}
	return filepath.Join(home, ".opencrawl", "opencrawl.db")
}

// Store is an open audit database.
type Store struct {
	db *sql.DB
}

// migrations are applied in order; PRAGMA user_version records how many ran.
var migrations = []string{
	`CREATE TABLE audits (
		id            INTEGER PRIMARY KEY,
		host          TEXT    NOT NULL,
		target        TEXT    NOT NULL,
		generated_at  TEXT    NOT NULL,
		pages_found   INTEGER NOT NULL,
		pages_fetched INTEGER NOT NULL,
		score         INTEGER,
		errors        INTEGER NOT NULL,
		warnings      INTEGER NOT NULL,
		infos         INTEGER NOT NULL,
		report_json   BLOB    NOT NULL
	);
	CREATE INDEX audits_by_host ON audits (host, generated_at);

	CREATE TABLE pages (
		audit_id      INTEGER NOT NULL REFERENCES audits (id) ON DELETE CASCADE,
		url           TEXT    NOT NULL,
		fetched       INTEGER NOT NULL,
		status        INTEGER NOT NULL,
		words         INTEGER NOT NULL,
		score         INTEGER,
		missing_share REAL,
		PRIMARY KEY (audit_id, url)
	);

	CREATE TABLE access (
		audit_id INTEGER NOT NULL REFERENCES audits (id) ON DELETE CASCADE,
		url      TEXT    NOT NULL,
		agent    TEXT    NOT NULL,
		state    TEXT    NOT NULL,
		reason   TEXT    NOT NULL,
		PRIMARY KEY (audit_id, url, agent)
	);
	CREATE INDEX access_by_agent ON access (audit_id, agent, state);

	CREATE TABLE findings (
		audit_id   INTEGER NOT NULL REFERENCES audits (id) ON DELETE CASCADE,
		finding_id TEXT    NOT NULL,
		severity   TEXT    NOT NULL,
		fidelity   TEXT    NOT NULL,
		title      TEXT    NOT NULL,
		count      INTEGER NOT NULL,
		PRIMARY KEY (audit_id, finding_id)
	);`,
}

// Open opens or creates the database at path, creating its directory and
// applying any pending migrations.
func Open(path string) (*Store, error) {
	if dir := filepath.Dir(path); dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("store: %w", err)
		}
	}
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)")
	if err != nil {
		return nil, fmt.Errorf("store: %w", err)
	}
	s := &Store{db: db}
	if err := s.migrate(context.Background()); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

// Close closes the database.
func (s *Store) Close() error { return s.db.Close() }

func (s *Store) migrate(ctx context.Context) error {
	var version int
	if err := s.db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return fmt.Errorf("store: reading schema version: %w", err)
	}
	for i := version; i < len(migrations); i++ {
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, migrations[i]); err != nil {
			tx.Rollback()
			return fmt.Errorf("store: migration %d: %w", i+1, err)
		}
		if _, err := tx.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", i+1)); err != nil {
			tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}

// Save stores a report and returns its audit ID.
func (s *Store) Save(ctx context.Context, r *audit.Report) (int64, error) {
	blob, err := json.Marshal(r)
	if err != nil {
		return 0, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	var score any
	if r.Score != nil {
		score = r.Score.Overall
	}
	res, err := tx.ExecContext(ctx, `INSERT INTO audits
		(host, target, generated_at, pages_found, pages_fetched, score, errors, warnings, infos, report_json)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		r.Host, r.Target, r.GeneratedAt.UTC().Format(time.RFC3339Nano), r.Summary.PagesFound, r.Summary.PagesFetched,
		score, r.Summary.Findings["error"], r.Summary.Findings["warning"], r.Summary.Findings["info"], blob)
	if err != nil {
		return 0, fmt.Errorf("store: saving audit: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}

	pageStmt, err := tx.PrepareContext(ctx, `INSERT OR IGNORE INTO pages (audit_id, url, fetched, status, words, score, missing_share) VALUES (?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return 0, err
	}
	accStmt, err := tx.PrepareContext(ctx, `INSERT OR IGNORE INTO access (audit_id, url, agent, state, reason) VALUES (?, ?, ?, ?, ?)`)
	if err != nil {
		return 0, err
	}
	for _, p := range r.Pages {
		var pageScore, missing any
		if p.Score != nil {
			pageScore = p.Score.Overall
		}
		if p.Render != nil && p.Render.Error == "" {
			missing = p.Render.MissingShare
		}
		if _, err := pageStmt.ExecContext(ctx, id, p.URL, p.Fetched, p.Status, p.WordCount, pageScore, missing); err != nil {
			return 0, err
		}
		for _, a := range p.Access {
			reason := a.Robots.Explain()
			if len(a.Directives) > 0 {
				reason += "; directives: " + strings.Join(a.Directives, ", ")
			}
			if _, err := accStmt.ExecContext(ctx, id, p.URL, a.Agent, a.State, reason); err != nil {
				return 0, err
			}
		}
	}
	for _, f := range r.Findings {
		if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO findings (audit_id, finding_id, severity, fidelity, title, count) VALUES (?, ?, ?, ?, ?, ?)`,
			id, f.ID, f.Severity, f.Fidelity, f.Title, f.Count); err != nil {
			return 0, err
		}
	}
	return id, tx.Commit()
}

// AuditInfo is one row of the audit list.
type AuditInfo struct {
	ID           int64     `json:"id"`
	Host         string    `json:"host"`
	Target       string    `json:"target"`
	GeneratedAt  time.Time `json:"generated_at"`
	PagesFound   int       `json:"pages_found"`
	PagesFetched int       `json:"pages_fetched"`
	// Score is -1 when the audit has none.
	Score    int `json:"score"`
	Errors   int `json:"errors"`
	Warnings int `json:"warnings"`
	Infos    int `json:"infos"`
}

// List returns audits, newest first. host filters when non-empty; limit <= 0
// means 50.
func (s *Store) List(ctx context.Context, host string, limit int) ([]AuditInfo, error) {
	if limit <= 0 {
		limit = 50
	}
	q := `SELECT id, host, target, generated_at, pages_found, pages_fetched, COALESCE(score, -1), errors, warnings, infos FROM audits`
	args := []any{}
	if host != "" {
		q += ` WHERE host = ?`
		args = append(args, host)
	}
	q += ` ORDER BY generated_at DESC, id DESC LIMIT ?`
	args = append(args, limit)

	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AuditInfo
	for rows.Next() {
		var a AuditInfo
		var at string
		if err := rows.Scan(&a.ID, &a.Host, &a.Target, &at, &a.PagesFound, &a.PagesFetched, &a.Score, &a.Errors, &a.Warnings, &a.Infos); err != nil {
			return nil, err
		}
		a.GeneratedAt, _ = time.Parse(time.RFC3339Nano, at)
		out = append(out, a)
	}
	return out, rows.Err()
}

// Get loads a full report.
func (s *Store) Get(ctx context.Context, id int64) (*audit.Report, error) {
	var blob []byte
	err := s.db.QueryRowContext(ctx, `SELECT report_json FROM audits WHERE id = ?`, id).Scan(&blob)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("%w: #%d", ErrNotFound, id)
	}
	if err != nil {
		return nil, err
	}
	var r audit.Report
	if err := json.Unmarshal(blob, &r); err != nil {
		return nil, fmt.Errorf("store: decoding audit #%d: %w", id, err)
	}
	return &r, nil
}

// Previous returns the ID of the most recent audit of host before id, or 0.
func (s *Store) Previous(ctx context.Context, host string, id int64) (int64, error) {
	var prev int64
	err := s.db.QueryRowContext(ctx, `SELECT id FROM audits WHERE host = ? AND id < ? ORDER BY id DESC LIMIT 1`, host, id).Scan(&prev)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return prev, err
}

// Latest returns the ID of the newest audit, of host if non-empty, or 0.
func (s *Store) Latest(ctx context.Context, host string) (int64, error) {
	list, err := s.List(ctx, host, 1)
	if err != nil || len(list) == 0 {
		return 0, err
	}
	return list[0].ID, nil
}

// AccessRow is one agent's access to one URL.
type AccessRow struct {
	URL    string `json:"url"`
	Agent  string `json:"agent"`
	State  string `json:"state"`
	Reason string `json:"reason"`
}

// AccessQuery filters QueryAccess. Empty fields match everything. Path is a
// glob on the URL's path, where * matches any run of characters.
type AccessQuery struct {
	Agent string
	State string
	Path  string
	Limit int // default 200
}

// QueryAccess answers questions like "which pages can't ClaudeBot read?".
func (s *Store) QueryAccess(ctx context.Context, id int64, q AccessQuery) ([]AccessRow, error) {
	if q.Limit <= 0 {
		q.Limit = 200
	}
	sqlq := `SELECT a.url, a.agent, a.state, a.reason FROM access a WHERE a.audit_id = ?`
	args := []any{id}
	if q.Agent != "" {
		sqlq += ` AND a.agent = ? COLLATE NOCASE`
		args = append(args, q.Agent)
	}
	if q.State != "" {
		sqlq += ` AND a.state = ?`
		args = append(args, q.State)
	}
	if q.Path != "" {
		// Match the path after "scheme://host".
		sqlq += ` AND substr(a.url, instr(substr(a.url, 9), '/') + 8) GLOB ?`
		args = append(args, q.Path)
	}
	sqlq += ` ORDER BY a.url, a.agent LIMIT ?`
	args = append(args, q.Limit)

	rows, err := s.db.QueryContext(ctx, sqlq, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AccessRow
	for rows.Next() {
		var r AccessRow
		if err := rows.Scan(&r.URL, &r.Agent, &r.State, &r.Reason); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
