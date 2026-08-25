package db

import "time"

// BenchmarkRecord represents a single benchmark run stored in the database.
type BenchmarkRecord struct {
	ID          int64     `json:"id"`
	Timestamp   time.Time `json:"timestamp"`
	TargetURL   string    `json:"target_url"`
	Language    string    `json:"language"`
	Requests    int64     `json:"requests"`
	TimeTakenMs int64     `json:"time_taken_ms"`
	ReqPerSec   float64   `json:"req_per_sec"`
	BytesRead   int64     `json:"bytes_read"`
}

// LanguageStats holds aggregated statistics for a specific crawler language.
type LanguageStats struct {
	RunCount      int     `json:"run_count"`
	AvgReqPerSec  float64 `json:"avg_req_per_sec"`
	MaxReqPerSec  float64 `json:"max_req_per_sec"`
	MinReqPerSec  float64 `json:"min_req_per_sec"`
	TotalRequests int64   `json:"total_requests"`
	TotalBytes    int64   `json:"total_bytes"`
}
