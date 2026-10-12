package mcpserver

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/kevinletchford/open-crawl/internal/agents"
	"github.com/kevinletchford/open-crawl/internal/audit"
)

// Audits run as background jobs, because a crawl can outlast a client's
// tool-call timeout. run_audit waits a while for the result, then hands back
// a job ID that audit_status picks up.
const (
	maxRunPages    = 100 // caps a tool-started crawl
	maxRenderPages = 30
	maxRunningJobs = 2
	defaultWait    = 45 * time.Second
	maxWait        = 55 * time.Second
	keepLog        = 20 // progress lines kept per job
)

// Job states.
const (
	JobRunning   = "running"
	JobDone      = "done"
	JobFailed    = "failed"
	JobCancelled = "cancelled"
)

type job struct {
	id      int64
	url     string
	started time.Time
	cancel  context.CancelFunc
	done    chan struct{}

	mu       sync.Mutex
	state    string
	progress []string // the latest keepLog messages
	messages int      // all messages so far
	finished time.Time
	auditID  int64
	summary  *SummaryOut
	err      string
}

func (j *job) log(msg string) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.messages++
	j.progress = append(j.progress, msg)
	if len(j.progress) > keepLog {
		j.progress = j.progress[len(j.progress)-keepLog:]
	}
}

// JobOut reports a job. Summary is set once it's done.
type JobOut struct {
	JobID    int64       `json:"job_id"`
	URL      string      `json:"url"`
	State    string      `json:"state" jsonschema:"running, done, failed or cancelled"`
	Elapsed  string      `json:"elapsed"`
	Progress []string    `json:"progress,omitempty" jsonschema:"latest progress messages"`
	Error    string      `json:"error,omitempty"`
	AuditID  int64       `json:"audit_id,omitempty"`
	Summary  *SummaryOut `json:"summary,omitempty"`
	NextStep string      `json:"next_step,omitempty"`
}

func (j *job) out() JobOut {
	j.mu.Lock()
	defer j.mu.Unlock()
	end := j.finished
	if end.IsZero() {
		end = time.Now()
	}
	o := JobOut{
		JobID: j.id, URL: j.url, State: j.state, Elapsed: end.Sub(j.started).Round(time.Second).String(),
		Progress: append([]string(nil), j.progress...), Error: j.err, AuditID: j.auditID, Summary: j.summary,
	}
	if j.state == JobRunning {
		o.NextStep = fmt.Sprintf("Still crawling. Call audit_status with job_id %d to wait for the result.", j.id)
	} else {
		o.Progress = nil
	}
	return o
}

// jobs tracks the server's audit jobs.
type jobs struct {
	mu   sync.Mutex
	next int64
	all  map[int64]*job
}

func (s *Server) job(id int64) (*job, error) {
	s.jobs.mu.Lock()
	defer s.jobs.mu.Unlock()
	if id == 0 {
		id = s.jobs.next
	}
	j, ok := s.jobs.all[id]
	if !ok {
		if id == 0 {
			return nil, errors.New("no audit jobs have been started; run run_audit")
		}
		return nil, fmt.Errorf("no audit job %d (jobs last only as long as the MCP server)", id)
	}
	return j, nil
}

// start begins an audit in the background, or returns the running job for
// the same URL.
func (s *Server) start(cfg audit.Config, render bool) (*job, error) {
	s.jobs.mu.Lock()
	defer s.jobs.mu.Unlock()
	running := 0
	for _, j := range s.jobs.all {
		j.mu.Lock()
		r := j.state == JobRunning
		j.mu.Unlock()
		if r {
			if j.url == cfg.URL {
				return j, nil
			}
			running++
		}
	}
	if running >= maxRunningJobs {
		return nil, fmt.Errorf("%d audits are already running; wait for one with audit_status or stop one with cancel_audit", running)
	}

	s.jobs.next++
	ctx, cancel := context.WithCancel(s.ctx)
	j := &job{id: s.jobs.next, url: cfg.URL, started: time.Now(), cancel: cancel, done: make(chan struct{}), state: JobRunning}
	if s.jobs.all == nil {
		s.jobs.all = map[int64]*job{}
	}
	s.jobs.all[j.id] = j
	cfg.Progress = j.log

	go func() {
		defer close(j.done)
		defer cancel()
		id, sum, err := s.runJob(ctx, cfg, render, j)
		j.mu.Lock()
		defer j.mu.Unlock()
		j.finished = time.Now()
		switch {
		case err != nil && ctx.Err() != nil:
			j.state, j.err = JobCancelled, "cancelled"
		case err != nil:
			j.state, j.err = JobFailed, err.Error()
		default:
			j.state, j.auditID, j.summary = JobDone, id, sum
		}
	}()
	return j, nil
}

func (s *Server) runJob(ctx context.Context, cfg audit.Config, render bool, j *job) (int64, *SummaryOut, error) {
	if render {
		j.log("starting Chrome")
		r, closeR, err := s.renderer(ctx)
		if err != nil {
			return 0, nil, fmt.Errorf("%w (render needs Chrome or Chromium; start the server with -chrome /path/to/chrome, or run without render)", err)
		}
		defer closeR()
		cfg.Renderer = r
	}
	r, err := s.run(ctx, cfg)
	if err != nil {
		return 0, nil, err
	}
	id, err := s.st.Save(ctx, r)
	if err != nil {
		return 0, nil, err
	}
	sum := summarize(id, r)
	return id, &sum, nil
}

// wait blocks until j finishes, d passes or ctx ends. While it waits it
// forwards progress to the client if the call asked for progress
// notifications.
func (s *Server) wait(ctx context.Context, req *mcp.CallToolRequest, j *job, d time.Duration) {
	timer := time.NewTimer(d)
	defer timer.Stop()
	tick := time.NewTicker(time.Second)
	defer tick.Stop()

	var token any
	if req != nil && req.Params != nil {
		token = req.Params.GetProgressToken()
	}
	sent := 0
	for {
		select {
		case <-j.done:
			return
		case <-timer.C:
			return
		case <-ctx.Done():
			return
		case <-tick.C:
			if token == nil || req.Session == nil {
				continue
			}
			j.mu.Lock()
			n, msg := j.messages, ""
			if len(j.progress) > 0 {
				msg = j.progress[len(j.progress)-1]
			}
			j.mu.Unlock()
			if n > sent {
				sent = n
				_ = req.Session.NotifyProgress(ctx, &mcp.ProgressNotificationParams{ProgressToken: token, Progress: float64(n), Message: msg})
			}
		}
	}
}

func waitFor(seconds int, def time.Duration) time.Duration {
	if seconds < 0 {
		return 0
	}
	if seconds == 0 {
		return def
	}
	return min(time.Duration(seconds)*time.Second, maxWait)
}

type RunAuditIn struct {
	URL         string   `json:"url" jsonschema:"site to audit, e.g. https://example.com"`
	MaxPages    int      `json:"max_pages,omitempty" jsonschema:"pages to fetch (default 30, at most 100)"`
	Agents      []string `json:"agents,omitempty" jsonschema:"agents to check (default: all)"`
	Render      bool     `json:"render,omitempty" jsonschema:"also render pages in headless Chrome and compare them with the raw HTML that non-JavaScript crawlers get (needs Chrome; slower)"`
	RenderPages int      `json:"render_pages,omitempty" jsonschema:"pages to render when render is set (default 10, at most 30)"`
	WaitSeconds int      `json:"wait_seconds,omitempty" jsonschema:"how long to wait for the result before returning a job ID (default 45, at most 55; -1 returns at once)"`
}

func (s *Server) runAudit(ctx context.Context, req *mcp.CallToolRequest, in RunAuditIn) (*mcp.CallToolResult, JobOut, error) {
	if in.URL == "" {
		return nil, JobOut{}, errors.New("url is required")
	}
	if in.MaxPages <= 0 {
		in.MaxPages = 30
	}
	in.MaxPages = min(in.MaxPages, maxRunPages)
	if in.RenderPages <= 0 {
		in.RenderPages = 10
	}
	in.RenderPages = min(in.RenderPages, maxRenderPages)
	list, err := agents.Select(in.Agents)
	if err != nil {
		return nil, JobOut{}, err
	}
	cfg := audit.Config{URL: in.URL, Agents: list, MaxPages: in.MaxPages}
	if in.Render {
		cfg.RenderPages = in.RenderPages
	}
	j, err := s.start(cfg, in.Render)
	if err != nil {
		return nil, JobOut{}, err
	}
	s.wait(ctx, req, j, waitFor(in.WaitSeconds, defaultWait))
	return nil, j.out(), nil
}

type AuditStatusIn struct {
	JobID       int64 `json:"job_id,omitempty" jsonschema:"job ID from run_audit; default: the latest job"`
	WaitSeconds int   `json:"wait_seconds,omitempty" jsonschema:"how long to wait for the job to finish (default 45, at most 55; -1 returns at once)"`
}

func (s *Server) auditStatus(ctx context.Context, req *mcp.CallToolRequest, in AuditStatusIn) (*mcp.CallToolResult, JobOut, error) {
	j, err := s.job(in.JobID)
	if err != nil {
		return nil, JobOut{}, err
	}
	s.wait(ctx, req, j, waitFor(in.WaitSeconds, defaultWait))
	return nil, j.out(), nil
}

type CancelAuditIn struct {
	JobID int64 `json:"job_id" jsonschema:"job ID from run_audit"`
}

func (s *Server) cancelAudit(ctx context.Context, _ *mcp.CallToolRequest, in CancelAuditIn) (*mcp.CallToolResult, JobOut, error) {
	j, err := s.job(in.JobID)
	if err != nil {
		return nil, JobOut{}, err
	}
	j.cancel()
	select {
	case <-j.done:
	case <-time.After(10 * time.Second):
	case <-ctx.Done():
	}
	return nil, j.out(), nil
}
