package crawler

import "sync"

// Config holds configuration for a crawl operation.
type Config struct {
	URL         string
	MaxRequests int64
	Concurrency int
}

// Result holds the results of a completed crawl.
type Result struct {
	Language       string   `json:"language"`
	Requests       int64    `json:"requests"`
	TimeTakenMs    int64    `json:"time_taken_ms"`
	ReqPerSec      float64  `json:"req_per_sec"`
	BytesRead      int64    `json:"bytes_read"`
	URLsDiscovered int64    `json:"urls_discovered"`
	SampleURLs     []string `json:"sample_urls,omitempty"`
}

// ProgressCallback is called periodically during crawling to report progress.
type ProgressCallback func(requests, urlsDiscovered int64, recentURLs []string)

// recentURLsQueue is a thread-safe queue that keeps track of recently crawled URLs.
type recentURLsQueue struct {
	mu   sync.Mutex
	urls []string
}

func (q *recentURLsQueue) Add(url string) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.urls = append(q.urls, url)
	if len(q.urls) > 20 {
		q.urls = q.urls[len(q.urls)-20:]
	}
}

func (q *recentURLsQueue) Get() []string {
	q.mu.Lock()
	defer q.mu.Unlock()
	copied := make([]string, len(q.urls))
	copy(copied, q.urls)
	return copied
}
