package mcpserver

import (
	"compress/gzip"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kevinletchford/open-crawl/internal/audit"
)

// blockingRun returns an audit runner that waits for release (or for its
// context to end) before finishing.
func blockingRun(release <-chan struct{}) func(context.Context, audit.Config) (*audit.Report, error) {
	return func(ctx context.Context, c audit.Config) (*audit.Report, error) {
		c.Progress("crawling " + c.URL)
		select {
		case <-release:
			return &audit.Report{Host: "x.test", Summary: audit.Summary{Findings: map[string]int{}}}, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

func TestRunAuditReturnsJobWhileRunning(t *testing.T) {
	s := newServer(t)
	release := make(chan struct{})
	s.run = blockingRun(release)
	cs := connect(t, s)

	var job JobOut
	call(t, cs, "run_audit", map[string]any{"url": "https://x.test", "wait_seconds": -1}, &job, false)
	if job.State != JobRunning || job.JobID != 1 || !strings.Contains(job.NextStep, "audit_status") {
		t.Fatalf("run_audit = %+v", job)
	}

	// Asking again for the same URL joins the running job.
	var again JobOut
	call(t, cs, "run_audit", map[string]any{"url": "https://x.test", "wait_seconds": -1}, &again, false)
	if again.JobID != 1 {
		t.Errorf("second run_audit started job %d, want to join job 1", again.JobID)
	}

	call(t, cs, "audit_status", map[string]any{"job_id": 1, "wait_seconds": 1}, &job, false)
	if job.State != JobRunning || len(job.Progress) == 0 {
		t.Errorf("audit_status while running = %+v", job)
	}

	close(release)
	call(t, cs, "audit_status", map[string]any{}, &job, false)
	if job.State != JobDone || job.AuditID != 1 || job.Summary == nil || job.Summary.Host != "x.test" {
		t.Errorf("audit_status after finish = %+v", job)
	}
	call(t, cs, "audit_status", map[string]any{"job_id": 9}, nil, true)
}

func TestCancelAudit(t *testing.T) {
	s := newServer(t)
	s.run = blockingRun(make(chan struct{}))
	cs := connect(t, s)

	var job JobOut
	call(t, cs, "run_audit", map[string]any{"url": "https://x.test", "wait_seconds": -1}, &job, false)
	call(t, cs, "cancel_audit", map[string]any{"job_id": job.JobID}, &job, false)
	if job.State != JobCancelled {
		t.Errorf("cancel_audit = %+v", job)
	}
}

func TestRunningJobLimit(t *testing.T) {
	s := newServer(t)
	s.run = blockingRun(make(chan struct{}))
	cs := connect(t, s)
	for i, u := range []string{"https://a.test", "https://b.test"} {
		var job JobOut
		call(t, cs, "run_audit", map[string]any{"url": u, "wait_seconds": -1}, &job, false)
		if job.JobID != int64(i+1) {
			t.Fatalf("job ID = %d", job.JobID)
		}
	}
	text := call(t, cs, "run_audit", map[string]any{"url": "https://c.test", "wait_seconds": -1}, nil, true)
	if !strings.Contains(text, "already running") {
		t.Errorf("third audit error = %q", text)
	}
}

func TestRunAuditCapsPages(t *testing.T) {
	s := newServer(t)
	var got audit.Config
	s.run = func(ctx context.Context, c audit.Config) (*audit.Report, error) {
		got = c
		return &audit.Report{Host: "x.test", Summary: audit.Summary{Findings: map[string]int{}}}, nil
	}
	s.renderer = func(context.Context) (audit.Renderer, func(), error) { return fakeRenderer{}, func() {}, nil }
	cs := connect(t, s)
	call(t, cs, "run_audit", map[string]any{"url": "https://x.test", "max_pages": 10000, "render": true, "render_pages": 500}, nil, false)
	if got.MaxPages != maxRunPages || got.RenderPages != maxRenderPages || got.Renderer == nil {
		t.Errorf("config = pages %d, render pages %d, renderer %v", got.MaxPages, got.RenderPages, got.Renderer)
	}
}

func TestRenderWithoutChromeFails(t *testing.T) {
	s := newServer(t)
	s.renderer = func(context.Context) (audit.Renderer, func(), error) { return nil, nil, errors.New("no Chrome") }
	cs := connect(t, s)
	var job JobOut
	call(t, cs, "run_audit", map[string]any{"url": "https://x.test", "render": true}, &job, false)
	if job.State != JobFailed || !strings.Contains(job.Error, "-chrome") {
		t.Errorf("run_audit = %+v", job)
	}
}

func TestAnalyzeLogs(t *testing.T) {
	cs := connect(t, newServer(t))
	dir := t.TempDir()
	path := filepath.Join(dir, "access.log.gz")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	zw := gzip.NewWriter(f)
	zw.Write([]byte(`203.0.113.9 - - [10/Oct/2026:13:55:36 +0000] "GET /pricing HTTP/1.1" 200 2326 "-" "Mozilla/5.0 (compatible; GPTBot/1.2; +https://openai.com/gptbot)"
203.0.113.9 - - [10/Oct/2026:13:55:37 +0000] "GET /docs HTTP/1.1" 403 12 "-" "Mozilla/5.0 (compatible; GPTBot/1.2; +https://openai.com/gptbot)"
`))
	zw.Close()
	f.Close()

	var out AnalyzeLogsOut
	call(t, cs, "analyze_logs", map[string]any{"paths": []string{path}, "no_verify": true}, &out, false)
	if out.Report == nil || out.Report.Parsed != 2 || !strings.Contains(out.Note, "verified") {
		t.Fatalf("analyze_logs = %+v", out)
	}
	found := false
	for _, a := range out.Report.Agents {
		if a.Agent == "GPTBot" && a.Requests == 2 && a.Unknown == 2 {
			found = true
		}
	}
	if !found {
		t.Errorf("GPTBot stats missing: %+v", out.Report.Agents)
	}

	call(t, cs, "analyze_logs", map[string]any{"paths": []string{"-"}}, nil, true)
	call(t, cs, "analyze_logs", map[string]any{"paths": []string{dir}}, nil, true)
	call(t, cs, "analyze_logs", map[string]any{"paths": []string{filepath.Join(dir, "missing.log")}}, nil, true)
}

type fakeRenderer struct{ audit.Renderer }
