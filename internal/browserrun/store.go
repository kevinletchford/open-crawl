package browserrun

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

type Store struct {
	db *sql.DB
}

func NewStore(dbPath string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(dbPath), 0755); err != nil {
		return nil, fmt.Errorf("create db dir: %w", err)
	}
	db, err := sql.Open("sqlite3", dbPath+"?_journal_mode=WAL&_foreign_keys=on")
	if err != nil {
		return nil, fmt.Errorf("open db: %w", err)
	}
	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) migrate() error {
	_, err := s.db.Exec(`
		CREATE TABLE IF NOT EXISTS crawl_jobs (
			id                  TEXT PRIMARY KEY,
			status              TEXT NOT NULL DEFAULT 'queued',
			seed_url            TEXT NOT NULL,
			config_json         TEXT NOT NULL,
			pages_visited       INTEGER NOT NULL DEFAULT 0,
			pages_limit         INTEGER NOT NULL DEFAULT 10,
			browser_seconds_used REAL NOT NULL DEFAULT 0,
			started_at          DATETIME,
			completed_at        DATETIME,
			created_at          DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			expires_at          DATETIME NOT NULL
		);

		CREATE TABLE IF NOT EXISTS crawl_results (
			id          INTEGER PRIMARY KEY AUTOINCREMENT,
			job_id      TEXT NOT NULL REFERENCES crawl_jobs(id),
			url         TEXT NOT NULL,
			url_status  TEXT NOT NULL DEFAULT 'completed',
			status_code INTEGER,
			title       TEXT,
			markdown    TEXT,
			html        TEXT,
			json_result TEXT,
			crawled_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
		);

		CREATE INDEX IF NOT EXISTS idx_crawl_results_job ON crawl_results(job_id);

		CREATE TABLE IF NOT EXISTS request_log (
			id         INTEGER PRIMARY KEY AUTOINCREMENT,
			endpoint   TEXT NOT NULL,
			url        TEXT,
			status     INTEGER NOT NULL,
			ms_used    INTEGER NOT NULL DEFAULT 0,
			created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
		);
	`)
	if err != nil {
		return err
	}
	// Add new columns to existing tables if they don't exist (for upgrades).
	s.addColumnIfMissing("crawl_jobs", "browser_seconds_used", "REAL NOT NULL DEFAULT 0")
	s.addColumnIfMissing("crawl_results", "url_status", "TEXT NOT NULL DEFAULT 'completed'")
	s.addColumnIfMissing("crawl_results", "title", "TEXT")
	return nil
}

func (s *Store) addColumnIfMissing(table, column, definition string) {
	var count int
	s.db.QueryRow(
		`SELECT COUNT(*) FROM pragma_table_info(?) WHERE name=?`, table, column,
	).Scan(&count)
	if count == 0 {
		s.db.Exec(fmt.Sprintf(`ALTER TABLE %s ADD COLUMN %s %s`, table, column, definition))
	}
}

// --- Crawl jobs ---

type CrawlJob struct {
	ID                 string       `json:"id"`
	Status             string       `json:"status"`
	SeedURL            string       `json:"seedUrl"`
	Config             CrawlRequest `json:"config"`
	PagesVisited       int          `json:"pagesVisited"`
	PagesLimit         int          `json:"pagesLimit"`
	BrowserSecondsUsed float64      `json:"browserSecondsUsed"`
	StartedAt          *time.Time   `json:"startedAt"`
	CompletedAt        *time.Time   `json:"completedAt"`
	CreatedAt          time.Time    `json:"createdAt"`
	ExpiresAt          time.Time    `json:"expiresAt"`
}

const sqliteTimeFormat = "2006-01-02 15:04:05"

func (s *Store) CreateJob(job *CrawlJob) error {
	cfgJSON, err := json.Marshal(job.Config)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(
		`INSERT INTO crawl_jobs(id, seed_url, config_json, pages_limit, expires_at)
		 VALUES (?, ?, ?, ?, ?)`,
		job.ID, job.SeedURL, string(cfgJSON), job.PagesLimit,
		job.ExpiresAt.UTC().Format(sqliteTimeFormat),
	)
	return err
}

func (s *Store) GetJob(id string) (*CrawlJob, error) {
	row := s.db.QueryRow(
		`SELECT id, status, seed_url, config_json, pages_visited, pages_limit,
		        browser_seconds_used, started_at, completed_at, created_at, expires_at
		 FROM crawl_jobs WHERE id = ?`, id)
	return scanJob(row)
}

func (s *Store) ListJobs() ([]*CrawlJob, error) {
	rows, err := s.db.Query(
		`SELECT id, status, seed_url, config_json, pages_visited, pages_limit,
		        browser_seconds_used, started_at, completed_at, created_at, expires_at
		 FROM crawl_jobs ORDER BY created_at DESC LIMIT 200`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var jobs []*CrawlJob
	for rows.Next() {
		j, err := scanJob(rows)
		if err != nil {
			return nil, err
		}
		jobs = append(jobs, j)
	}
	return jobs, rows.Err()
}

type scanner interface {
	Scan(dest ...interface{}) error
}

func scanJob(row scanner) (*CrawlJob, error) {
	var j CrawlJob
	var cfgJSON string
	var startedAt, completedAt sql.NullString
	var createdAt, expiresAt string
	err := row.Scan(
		&j.ID, &j.Status, &j.SeedURL, &cfgJSON,
		&j.PagesVisited, &j.PagesLimit, &j.BrowserSecondsUsed,
		&startedAt, &completedAt,
		&createdAt, &expiresAt,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	json.Unmarshal([]byte(cfgJSON), &j.Config)
	parseT := func(s string) time.Time {
		t, _ := time.Parse(sqliteTimeFormat, s)
		return t
	}
	if startedAt.Valid {
		t := parseT(startedAt.String)
		j.StartedAt = &t
	}
	if completedAt.Valid {
		t := parseT(completedAt.String)
		j.CompletedAt = &t
	}
	j.CreatedAt = parseT(createdAt)
	j.ExpiresAt = parseT(expiresAt)
	return &j, nil
}

func (s *Store) UpdateJobStatus(id, status string) error {
	switch status {
	case "running":
		_, err := s.db.Exec(
			`UPDATE crawl_jobs SET status=?, started_at=datetime('now') WHERE id=?`, status, id)
		return err
	case "completed", "errored", "cancelled_by_user", "cancelled_due_to_timeout", "cancelled_due_to_limits":
		_, err := s.db.Exec(
			`UPDATE crawl_jobs SET status=?, completed_at=datetime('now') WHERE id=?`, status, id)
		return err
	default:
		_, err := s.db.Exec(`UPDATE crawl_jobs SET status=? WHERE id=?`, status, id)
		return err
	}
}

func (s *Store) IncrementJobPages(id string) error {
	_, err := s.db.Exec(`UPDATE crawl_jobs SET pages_visited=pages_visited+1 WHERE id=?`, id)
	return err
}

func (s *Store) AddBrowserSeconds(id string, seconds float64) error {
	_, err := s.db.Exec(
		`UPDATE crawl_jobs SET browser_seconds_used=browser_seconds_used+? WHERE id=?`,
		seconds, id,
	)
	return err
}

// --- Crawl results ---

type CrawlResultRow struct {
	ID         int64
	JobID      string
	URL        string
	URLStatus  string
	StatusCode int
	Title      string
	Markdown   string
	HTML       string
	JSONResult string
	CrawledAt  time.Time
}

func (s *Store) SaveResult(r *CrawlResultRow) error {
	if r.URLStatus == "" {
		r.URLStatus = "completed"
	}
	_, err := s.db.Exec(
		`INSERT INTO crawl_results(job_id, url, url_status, status_code, title, markdown, html, json_result)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		r.JobID, r.URL, r.URLStatus, r.StatusCode, r.Title, r.Markdown, r.HTML, r.JSONResult,
	)
	return err
}

// GetResultsCursor returns results for a job using cursor-based pagination.
// cursor=0 starts from the beginning. Returns the next cursor (0 = no more rows).
// statusFilter is optional — empty string returns all statuses.
func (s *Store) GetResultsCursor(jobID string, limit int, cursor int64, statusFilter string) ([]*CrawlResultRow, int64, error) {
	if limit <= 0 {
		limit = 50
	}
	if limit > 1000 {
		limit = 1000
	}

	var (
		rows *sql.Rows
		err  error
	)
	if statusFilter != "" {
		rows, err = s.db.Query(
			`SELECT id, job_id, url, url_status, status_code, title, markdown, html, json_result, crawled_at
			 FROM crawl_results WHERE job_id=? AND id>? AND url_status=?
			 ORDER BY id LIMIT ?`,
			jobID, cursor, statusFilter, limit,
		)
	} else {
		rows, err = s.db.Query(
			`SELECT id, job_id, url, url_status, status_code, title, markdown, html, json_result, crawled_at
			 FROM crawl_results WHERE job_id=? AND id>?
			 ORDER BY id LIMIT ?`,
			jobID, cursor, limit,
		)
	}
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	var results []*CrawlResultRow
	for rows.Next() {
		var r CrawlResultRow
		var crawledAt string
		if err := rows.Scan(&r.ID, &r.JobID, &r.URL, &r.URLStatus, &r.StatusCode, &r.Title,
			&r.Markdown, &r.HTML, &r.JSONResult, &crawledAt); err != nil {
			return nil, 0, err
		}
		r.CrawledAt, _ = time.Parse("2006-01-02T15:04:05Z", crawledAt)
		results = append(results, &r)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}

	var nextCursor int64
	if len(results) == limit {
		nextCursor = results[len(results)-1].ID
	}
	return results, nextCursor, nil
}

// GetResultsTotal returns the total count of crawl results for a job.
func (s *Store) GetResultsTotal(jobID string) int {
	var total int
	s.db.QueryRow(`SELECT COUNT(*) FROM crawl_results WHERE job_id=?`, jobID).Scan(&total)
	return total
}

// --- Request log ---

func (s *Store) LogRequest(endpoint, url string, status, msUsed int) {
	s.db.Exec(
		`INSERT INTO request_log(endpoint, url, status, ms_used) VALUES (?, ?, ?, ?)`,
		endpoint, url, status, msUsed,
	)
}

type RequestLogEntry struct {
	ID        int64     `json:"id"`
	Endpoint  string    `json:"endpoint"`
	URL       string    `json:"url"`
	Status    int       `json:"status"`
	MsUsed    int       `json:"msUsed"`
	CreatedAt time.Time `json:"createdAt"`
}

func (s *Store) RecentRequests(limit int) ([]*RequestLogEntry, error) {
	rows, err := s.db.Query(
		`SELECT id, endpoint, url, status, ms_used, created_at
		 FROM request_log ORDER BY id DESC LIMIT ?`, limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var entries []*RequestLogEntry
	for rows.Next() {
		var e RequestLogEntry
		var createdAt string
		if err := rows.Scan(&e.ID, &e.Endpoint, &e.URL, &e.Status, &e.MsUsed, &createdAt); err != nil {
			return nil, err
		}
		e.CreatedAt, _ = time.Parse("2006-01-02T15:04:05Z", createdAt)
		entries = append(entries, &e)
	}
	return entries, rows.Err()
}

// Purge deletes expired crawl jobs and their results.
func (s *Store) Purge() error {
	_, err := s.db.Exec(`
		DELETE FROM crawl_results WHERE job_id IN (
			SELECT id FROM crawl_jobs WHERE expires_at < datetime('now')
		);
		DELETE FROM crawl_jobs WHERE expires_at < datetime('now');
		DELETE FROM request_log WHERE created_at < datetime('now', '-7 days');
	`)
	return err
}

