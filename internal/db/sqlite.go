package db

import (
	"database/sql"
	"fmt"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

// SQLite wraps a SQLite database connection for benchmark storage.
type SQLite struct {
	db *sql.DB
}

// NewSQLite opens or creates a SQLite database at the given path.
func NewSQLite(path string) (*SQLite, error) {
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		return nil, fmt.Errorf("failed to open database: %w", err)
	}

	// Ensure table exists
	createTableSQL := `CREATE TABLE IF NOT EXISTS benchmarks (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		timestamp DATETIME DEFAULT CURRENT_TIMESTAMP,
		target_url TEXT,
		language TEXT,
		requests INTEGER,
		time_taken_ms INTEGER,
		req_per_sec REAL,
		bytes_read INTEGER
	);`

	if _, err := db.Exec(createTableSQL); err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to create table: %w", err)
	}

	return &SQLite{db: db}, nil
}

// Close closes the database connection.
func (s *SQLite) Close() error {
	return s.db.Close()
}

// SaveBenchmark inserts a new benchmark record into the database.
func (s *SQLite) SaveBenchmark(targetURL, language string, requests, timeTakenMs int64, reqPerSec float64, bytesRead int64) error {
	insertSQL := `INSERT INTO benchmarks(target_url, language, requests, time_taken_ms, req_per_sec, bytes_read)
	              VALUES (?, ?, ?, ?, ?, ?)`
	_, err := s.db.Exec(insertSQL, targetURL, language, requests, timeTakenMs, reqPerSec, bytesRead)
	return err
}

// GetHistory retrieves benchmark records with optional filtering.
func (s *SQLite) GetHistory(targetURL, language string, limit int) ([]BenchmarkRecord, error) {
	query := `SELECT id, timestamp, target_url, language, requests, time_taken_ms, req_per_sec, bytes_read
	          FROM benchmarks WHERE 1=1`
	args := []interface{}{}

	if targetURL != "" {
		query += " AND target_url = ?"
		args = append(args, targetURL)
	}
	if language != "" {
		query += " AND language LIKE ?"
		args = append(args, language+"%")
	}

	query += " ORDER BY timestamp DESC"

	if limit > 0 {
		query += " LIMIT ?"
		args = append(args, limit)
	}

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var records []BenchmarkRecord
	for rows.Next() {
		var r BenchmarkRecord
		var ts string
		err := rows.Scan(&r.ID, &ts, &r.TargetURL, &r.Language, &r.Requests, &r.TimeTakenMs, &r.ReqPerSec, &r.BytesRead)
		if err != nil {
			return nil, err
		}
		r.Timestamp, _ = time.Parse("2006-01-02 15:04:05", ts)
		records = append(records, r)
	}
	return records, nil
}

// GetStatistics returns aggregated statistics grouped by language.
func (s *SQLite) GetStatistics(targetURL, language string) (map[string]LanguageStats, error) {
	query := `SELECT language,
	                 COUNT(*) as run_count,
	                 AVG(req_per_sec) as avg_req,
	                 MAX(req_per_sec) as max_req,
	                 MIN(req_per_sec) as min_req,
	                 SUM(requests) as total_requests,
	                 SUM(bytes_read) as total_bytes
	          FROM benchmarks WHERE 1=1`
	args := []interface{}{}

	if targetURL != "" {
		query += " AND target_url = ?"
		args = append(args, targetURL)
	}
	if language != "" {
		query += " AND language LIKE ?"
		args = append(args, language+"%")
	}

	query += " GROUP BY language"

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	stats := make(map[string]LanguageStats)
	for rows.Next() {
		var lang string
		var st LanguageStats
		err := rows.Scan(&lang, &st.RunCount, &st.AvgReqPerSec, &st.MaxReqPerSec,
			&st.MinReqPerSec, &st.TotalRequests, &st.TotalBytes)
		if err != nil {
			return nil, err
		}
		stats[lang] = st
	}
	return stats, nil
}

// GetHistoricalAverage returns the average req/sec for a given URL and language.
func (s *SQLite) GetHistoricalAverage(targetURL, language string) (float64, error) {
	query := `SELECT AVG(req_per_sec) FROM benchmarks WHERE target_url = ? AND language LIKE ?`
	var avgReqPerSec sql.NullFloat64
	err := s.db.QueryRow(query, targetURL, language+"%").Scan(&avgReqPerSec)
	if err != nil {
		return 0, err
	}
	if !avgReqPerSec.Valid {
		return 0, fmt.Errorf("no historical data")
	}
	return avgReqPerSec.Float64, nil
}
