package browserrun

import (
	"os"
	"testing"
	"time"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "browserrun-*.db")
	if err != nil {
		t.Fatalf("temp file: %v", err)
	}
	f.Close()

	s, err := NewStore(f.Name())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestStore_CreateAndGetJob(t *testing.T) {
	s := newTestStore(t)

	job := &CrawlJob{
		ID:         "job-001",
		Status:     "queued",
		SeedURL:    "https://example.com",
		Config:     CrawlRequest{URL: "https://example.com", Limit: 10},
		PagesLimit: 10,
		ExpiresAt:  time.Now().Add(24 * time.Hour),
	}

	if err := s.CreateJob(job); err != nil {
		t.Fatalf("CreateJob: %v", err)
	}

	got, err := s.GetJob("job-001")
	if err != nil {
		t.Fatalf("GetJob: %v", err)
	}
	if got == nil {
		t.Fatal("GetJob returned nil")
	}
	if got.ID != "job-001" {
		t.Errorf("ID: got %q, want %q", got.ID, "job-001")
	}
	if got.SeedURL != "https://example.com" {
		t.Errorf("SeedURL: got %q, want %q", got.SeedURL, "https://example.com")
	}
	if got.Status != "queued" {
		t.Errorf("Status: got %q, want %q", got.Status, "queued")
	}
	if got.PagesLimit != 10 {
		t.Errorf("PagesLimit: got %d, want 10", got.PagesLimit)
	}
}

func TestStore_GetJob_NotFound(t *testing.T) {
	s := newTestStore(t)
	got, err := s.GetJob("nonexistent")
	if err != nil {
		t.Fatalf("GetJob: %v", err)
	}
	if got != nil {
		t.Errorf("expected nil for missing job, got %+v", got)
	}
}

func TestStore_UpdateJobStatus(t *testing.T) {
	s := newTestStore(t)
	job := &CrawlJob{
		ID: "job-002", SeedURL: "https://x.com",
		Config: CrawlRequest{URL: "https://x.com"}, PagesLimit: 5,
		ExpiresAt: time.Now().Add(time.Hour),
	}
	s.CreateJob(job)

	for _, status := range []string{"running", "completed"} {
		if err := s.UpdateJobStatus("job-002", status); err != nil {
			t.Fatalf("UpdateJobStatus(%q): %v", status, err)
		}
		got, _ := s.GetJob("job-002")
		if got.Status != status {
			t.Errorf("status after update: got %q, want %q", got.Status, status)
		}
	}
}

func TestStore_IncrementJobPages(t *testing.T) {
	s := newTestStore(t)
	job := &CrawlJob{
		ID: "job-003", SeedURL: "https://x.com",
		Config: CrawlRequest{URL: "https://x.com"}, PagesLimit: 5,
		ExpiresAt: time.Now().Add(time.Hour),
	}
	s.CreateJob(job)

	for i := 1; i <= 3; i++ {
		s.IncrementJobPages("job-003")
		got, _ := s.GetJob("job-003")
		if got.PagesVisited != i {
			t.Errorf("after %d increments: PagesVisited=%d, want %d", i, got.PagesVisited, i)
		}
	}
}

func TestStore_AddBrowserSeconds(t *testing.T) {
	s := newTestStore(t)
	job := &CrawlJob{
		ID: "job-bsec", SeedURL: "https://x.com",
		Config: CrawlRequest{URL: "https://x.com"}, PagesLimit: 5,
		ExpiresAt: time.Now().Add(time.Hour),
	}
	s.CreateJob(job)

	s.AddBrowserSeconds("job-bsec", 1.5)
	s.AddBrowserSeconds("job-bsec", 0.3)
	got, _ := s.GetJob("job-bsec")
	if got.BrowserSecondsUsed < 1.79 || got.BrowserSecondsUsed > 1.81 {
		t.Errorf("BrowserSecondsUsed: got %f, want ~1.8", got.BrowserSecondsUsed)
	}
}

func TestStore_SaveAndGetResultsCursor(t *testing.T) {
	s := newTestStore(t)
	job := &CrawlJob{
		ID: "job-004", SeedURL: "https://x.com",
		Config: CrawlRequest{URL: "https://x.com"}, PagesLimit: 5,
		ExpiresAt: time.Now().Add(time.Hour),
	}
	s.CreateJob(job)

	for i := 0; i < 5; i++ {
		s.SaveResult(&CrawlResultRow{
			JobID:      "job-004",
			URL:        "https://x.com/page" + string(rune('0'+i)),
			URLStatus:  "completed",
			StatusCode: 200,
			Title:      "Page Title",
			Markdown:   "# Page",
		})
	}

	// First page: limit=3, cursor=0
	rows, nextCursor, err := s.GetResultsCursor("job-004", 3, 0, "")
	if err != nil {
		t.Fatalf("GetResultsCursor: %v", err)
	}
	if len(rows) != 3 {
		t.Errorf("page 1 len: got %d, want 3", len(rows))
	}
	if nextCursor == 0 {
		t.Error("expected non-zero next cursor after page 1")
	}
	if rows[0].Title != "Page Title" {
		t.Errorf("Title: got %q, want %q", rows[0].Title, "Page Title")
	}
	if rows[0].URLStatus != "completed" {
		t.Errorf("URLStatus: got %q, want completed", rows[0].URLStatus)
	}

	// Second page using the returned cursor
	rows2, nextCursor2, err := s.GetResultsCursor("job-004", 3, nextCursor, "")
	if err != nil {
		t.Fatalf("GetResultsCursor page 2: %v", err)
	}
	if len(rows2) != 2 {
		t.Errorf("page 2 len: got %d, want 2", len(rows2))
	}
	if nextCursor2 != 0 {
		t.Errorf("expected cursor=0 (no more rows) after page 2, got %d", nextCursor2)
	}

	// Status filter
	s.SaveResult(&CrawlResultRow{
		JobID: "job-004", URL: "https://x.com/err", URLStatus: "errored", StatusCode: 0,
	})
	errored, _, _ := s.GetResultsCursor("job-004", 10, 0, "errored")
	if len(errored) != 1 {
		t.Errorf("errored filter: got %d rows, want 1", len(errored))
	}

	// GetResultsTotal
	total := s.GetResultsTotal("job-004")
	if total != 6 {
		t.Errorf("GetResultsTotal: got %d, want 6", total)
	}
}

func TestStore_LogAndRecentRequests(t *testing.T) {
	s := newTestStore(t)

	s.LogRequest("/screenshot", "https://a.com", 200, 142)
	s.LogRequest("/pdf", "https://b.com", 200, 891)
	s.LogRequest("/content", "https://c.com", 400, 5)

	entries, err := s.RecentRequests(10)
	if err != nil {
		t.Fatalf("RecentRequests: %v", err)
	}
	if len(entries) != 3 {
		t.Fatalf("expected 3 entries, got %d", len(entries))
	}
	// Most recent first
	if entries[0].Endpoint != "/content" {
		t.Errorf("first entry: got %q, want /content", entries[0].Endpoint)
	}
}

func TestStore_ListJobs(t *testing.T) {
	s := newTestStore(t)
	for i := 0; i < 3; i++ {
		s.CreateJob(&CrawlJob{
			ID:         string(rune('A' + i)),
			SeedURL:    "https://x.com",
			Config:     CrawlRequest{URL: "https://x.com"},
			PagesLimit: 1,
			ExpiresAt:  time.Now().Add(time.Hour),
		})
	}
	jobs, err := s.ListJobs()
	if err != nil {
		t.Fatalf("ListJobs: %v", err)
	}
	if len(jobs) != 3 {
		t.Errorf("expected 3 jobs, got %d", len(jobs))
	}
}

func TestStore_Purge(t *testing.T) {
	s := newTestStore(t)
	// Expired job
	s.CreateJob(&CrawlJob{
		ID:         "expired",
		SeedURL:    "https://x.com",
		Config:     CrawlRequest{URL: "https://x.com"},
		PagesLimit: 1,
		ExpiresAt:  time.Now().Add(-time.Hour),
	})
	s.SaveResult(&CrawlResultRow{JobID: "expired", URL: "https://x.com", StatusCode: 200})
	// Live job
	s.CreateJob(&CrawlJob{
		ID:         "live",
		SeedURL:    "https://x.com",
		Config:     CrawlRequest{URL: "https://x.com"},
		PagesLimit: 1,
		ExpiresAt:  time.Now().Add(time.Hour),
	})

	if err := s.Purge(); err != nil {
		t.Fatalf("Purge: %v", err)
	}

	got, _ := s.GetJob("expired")
	if got != nil {
		t.Error("expected expired job to be purged")
	}
	live, _ := s.GetJob("live")
	if live == nil {
		t.Error("expected live job to survive purge")
	}
}
