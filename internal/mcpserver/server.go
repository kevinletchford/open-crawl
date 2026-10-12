// Package mcpserver exposes saved audits to AI tools over the Model Context
// Protocol, so an assistant can answer "which product pages can't ClaudeBot
// read?" from real crawl data.
package mcpserver

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/kevinletchford/open-crawl/internal/agents"
	"github.com/kevinletchford/open-crawl/internal/audit"
	"github.com/kevinletchford/open-crawl/internal/logs"
	"github.com/kevinletchford/open-crawl/internal/render"
	"github.com/kevinletchford/open-crawl/internal/store"
)

// Version is reported to MCP clients.
const Version = "0.1.0"

const instructions = `open-crawl audits what AI crawlers (GPTBot, ClaudeBot, PerplexityBot, Google-Extended…) may fetch from a website and what they receive.

Every finding has a fidelity. Pass it on when you answer:
- exact: from public signals (robots.txt, meta robots, X-Robots-Tag, raw HTML, a headless-Chrome render diff).
- simulated: requests sent with an agent's user agent from the user's own machine and network. The real agents crawl from their operators' data centres; firewalls that verify bots by IP may treat them differently.
- heuristic: an inference that can be wrong.

The audit can't tell whether content was used for training, how it ranks, or whether an assistant cites it. Don't claim otherwise.

Start with list_audits or audit_summary. Use query_access for per-URL, per-agent questions. Most tools default to the latest audit of a host when no audit_id is given.

run_audit crawls from the user's own machine and network. A crawl can take minutes: if run_audit returns state "running", call audit_status with the job_id until it is done. Only audit sites the user owns or may audit.

analyze_logs reads the site's own access logs. It is the only source here of what the real agents fetched: requests are checked against each operator's published IP ranges or reverse DNS, and spoofed ones are counted separately.`

// Options configures a Server.
type Options struct {
	// ChromePath is the browser used when run_audit renders pages. Empty
	// means search the usual install locations.
	ChromePath string
}

// Server serves audit data from a store.
type Server struct {
	st   *store.Store
	srv  *mcp.Server
	jobs jobs
	// ctx bounds background audits; Close cancels it.
	ctx  context.Context
	stop context.CancelFunc

	// run performs an audit; tests replace it.
	run func(context.Context, audit.Config) (*audit.Report, error)
	// renderer starts a headless browser; tests replace it.
	renderer func(context.Context) (audit.Renderer, func(), error)
	// analyze reads access logs; tests replace it.
	analyze func(context.Context, []string, logs.Options) (*logs.Report, error)
}

// New returns a server over st with every tool registered.
func New(st *store.Store, o Options) *Server {
	s := &Server{st: st, run: audit.Run, analyze: logs.AnalyzeFiles}
	s.ctx, s.stop = context.WithCancel(context.Background())
	s.renderer = func(ctx context.Context) (audit.Renderer, func(), error) {
		pool, err := render.New(ctx, render.Options{ExecPath: o.ChromePath})
		if err != nil {
			return nil, nil, err
		}
		return pool, pool.Close, nil
	}
	s.srv = mcp.NewServer(&mcp.Implementation{Name: "open-crawl", Version: Version},
		&mcp.ServerOptions{Instructions: instructions})

	mcp.AddTool(s.srv, &mcp.Tool{Name: "list_audits",
		Description: "List saved audits, newest first, with score and finding counts. Filter by host."}, s.listAudits)
	mcp.AddTool(s.srv, &mcp.Tool{Name: "audit_summary",
		Description: "Summarise one audit: readability score by category, top fixes, findings (severity, fidelity, title, count) and each agent's allowed/restricted/blocked totals."}, s.auditSummary)
	mcp.AddTool(s.srv, &mcp.Tool{Name: "query_access",
		Description: "Which URLs each agent may fetch in an audit. Filter by agent (e.g. ClaudeBot), state (allowed, restricted, blocked) and path glob (e.g. /products/*). Each row gives the robots.txt rule or directive behind the state. All rows are exact."}, s.queryAccess)
	mcp.AddTool(s.srv, &mcp.Tool{Name: "get_finding",
		Description: "The full text of one finding in an audit: detail, fix, affected agents and sample URLs."}, s.getFinding)
	mcp.AddTool(s.srv, &mcp.Tool{Name: "compare_audits",
		Description: "What changed between two audits of a host: score, new, resolved and changed findings, and agents whose robots.txt verdict for / changed. Defaults to the latest audit against the one before it."}, s.compareAudits)
	mcp.AddTool(s.srv, &mcp.Tool{Name: "list_agents",
		Description: "The agent registry: each AI crawler, fetcher or robots.txt token with its operator, kind, whether it runs JavaScript and its documented robots.txt stance."}, s.listAgents)
	mcp.AddTool(s.srv, &mcp.Tool{Name: "run_audit",
		Description: fmt.Sprintf("Audit a website now and save the result. Crawls politely from this machine (robots.txt obeyed, at most %d pages). Waits up to wait_seconds for the result; if the crawl is still going it returns state \"running\" and a job_id for audit_status. Set render to compare JavaScript-rendered pages with raw HTML. Only audit sites the user owns or may audit.", maxRunPages)}, s.runAudit)
	mcp.AddTool(s.srv, &mcp.Tool{Name: "audit_status",
		Description: "Wait for an audit started by run_audit and return its progress, or its summary once done."}, s.auditStatus)
	mcp.AddTool(s.srv, &mcp.Tool{Name: "cancel_audit",
		Description: "Stop a running audit."}, s.cancelAudit)
	mcp.AddTool(s.srv, &mcp.Tool{Name: "analyze_logs",
		Description: "Read web server or CDN access logs from local files (nginx/Apache combined, Cloudflare Logpush, Vercel or generic JSON lines; .gz is fine) and report what each AI agent really requested: request counts, status codes, refused and top paths. Requests are verified against operators' published IP ranges or reverse DNS; spoofed user agents are counted separately. Give site to also flag fetches of paths robots.txt disallows."}, s.analyzeLogs)
	return s
}

// Close stops any running audits.
func (s *Server) Close() { s.stop() }

// MCP returns the underlying MCP server.
func (s *Server) MCP() *mcp.Server { return s.srv }

// Run serves on the given transport until ctx ends or the client leaves.
func (s *Server) Run(ctx context.Context, t mcp.Transport) error {
	defer s.Close()
	return s.srv.Run(ctx, t)
}

// AuditRef picks an audit: an explicit ID, else the latest audit of Host,
// else the latest audit overall.
type AuditRef struct {
	AuditID int64  `json:"audit_id,omitempty" jsonschema:"audit ID; default: the latest audit of host"`
	Host    string `json:"host,omitempty" jsonschema:"site host, e.g. example.com, used when audit_id is not given"`
}

func (s *Server) resolve(ctx context.Context, ref AuditRef) (int64, error) {
	if ref.AuditID > 0 {
		return ref.AuditID, nil
	}
	id, err := s.st.Latest(ctx, normalizeHost(ref.Host))
	if err != nil {
		return 0, err
	}
	if id == 0 {
		if ref.Host != "" {
			return 0, fmt.Errorf("no saved audits of %s; run run_audit first", ref.Host)
		}
		return 0, errors.New("no saved audits; run run_audit first")
	}
	return id, nil
}

// normalizeHost accepts "https://Example.com/x" as well as "example.com".
func normalizeHost(h string) string {
	h = strings.ToLower(strings.TrimSpace(h))
	if i := strings.Index(h, "://"); i >= 0 {
		h = h[i+3:]
	}
	if i := strings.IndexByte(h, '/'); i >= 0 {
		h = h[:i]
	}
	return h
}

type ListAuditsIn struct {
	Host  string `json:"host,omitempty" jsonschema:"only audits of this host"`
	Limit int    `json:"limit,omitempty" jsonschema:"maximum audits to return (default 20)"`
}

type ListAuditsOut struct {
	Audits []store.AuditInfo `json:"audits"`
}

func (s *Server) listAudits(ctx context.Context, _ *mcp.CallToolRequest, in ListAuditsIn) (*mcp.CallToolResult, ListAuditsOut, error) {
	if in.Limit <= 0 {
		in.Limit = 20
	}
	list, err := s.st.List(ctx, normalizeHost(in.Host), in.Limit)
	if err != nil {
		return nil, ListAuditsOut{}, err
	}
	if list == nil {
		list = []store.AuditInfo{}
	}
	return nil, ListAuditsOut{Audits: list}, nil
}

type FindingBrief struct {
	ID       string `json:"id"`
	Severity string `json:"severity"`
	Fidelity string `json:"fidelity"`
	Title    string `json:"title"`
	Count    int    `json:"count"`
}

type SummaryOut struct {
	AuditID      int64                `json:"audit_id"`
	Host         string               `json:"host"`
	GeneratedAt  time.Time            `json:"generated_at"`
	PagesFound   int                  `json:"pages_found"`
	PagesFetched int                  `json:"pages_fetched"`
	RobotsTxt    string               `json:"robots_txt"`
	Score        *audit.SiteScore     `json:"score,omitempty"`
	Findings     []FindingBrief       `json:"findings"`
	Agents       []audit.AgentSummary `json:"agents"`
}

func (s *Server) auditSummary(ctx context.Context, _ *mcp.CallToolRequest, in AuditRef) (*mcp.CallToolResult, SummaryOut, error) {
	id, err := s.resolve(ctx, in)
	if err != nil {
		return nil, SummaryOut{}, err
	}
	r, err := s.st.Get(ctx, id)
	if err != nil {
		return nil, SummaryOut{}, err
	}
	return nil, summarize(id, r), nil
}

func summarize(id int64, r *audit.Report) SummaryOut {
	out := SummaryOut{
		AuditID: id, Host: r.Host, GeneratedAt: r.GeneratedAt,
		PagesFound: r.Summary.PagesFound, PagesFetched: r.Summary.PagesFetched,
		RobotsTxt: string(r.Robots.Policy.Mode), Agents: r.Summary.Agents, Findings: []FindingBrief{},
	}
	if r.Score != nil {
		sc := *r.Score
		for i := range sc.Fixes {
			sc.Fixes[i].URLs = nil // keep the summary small; get_finding and query_access have URLs
		}
		if len(sc.Fixes) > 5 {
			sc.Fixes = sc.Fixes[:5]
		}
		out.Score = &sc
	}
	for _, f := range r.Findings {
		out.Findings = append(out.Findings, FindingBrief{ID: f.ID, Severity: string(f.Severity), Fidelity: string(f.Fidelity), Title: f.Title, Count: f.Count})
	}
	return out
}

type QueryAccessIn struct {
	AuditRef
	Agent string `json:"agent,omitempty" jsonschema:"agent name, e.g. ClaudeBot (case-insensitive)"`
	State string `json:"state,omitempty" jsonschema:"allowed, restricted (a noindex-style directive applies) or blocked (robots.txt disallows)"`
	Path  string `json:"path,omitempty" jsonschema:"glob on the URL path, e.g. /products/*"`
	Limit int    `json:"limit,omitempty" jsonschema:"maximum rows (default 200)"`
}

type QueryAccessOut struct {
	AuditID int64             `json:"audit_id"`
	Rows    []store.AccessRow `json:"rows"`
	Note    string            `json:"note"`
}

func (s *Server) queryAccess(ctx context.Context, _ *mcp.CallToolRequest, in QueryAccessIn) (*mcp.CallToolResult, QueryAccessOut, error) {
	switch in.State {
	case "", audit.StateAllowed, audit.StateRestricted, audit.StateBlocked:
	default:
		return nil, QueryAccessOut{}, fmt.Errorf("state must be allowed, restricted or blocked, not %q", in.State)
	}
	if in.Agent != "" {
		a, ok := agents.Lookup(in.Agent)
		if !ok {
			return nil, QueryAccessOut{}, fmt.Errorf("unknown agent %q; list_agents has the names", in.Agent)
		}
		in.Agent = a.Name
	}
	id, err := s.resolve(ctx, in.AuditRef)
	if err != nil {
		return nil, QueryAccessOut{}, err
	}
	rows, err := s.st.QueryAccess(ctx, id, store.AccessQuery{Agent: in.Agent, State: in.State, Path: in.Path, Limit: in.Limit})
	if err != nil {
		return nil, QueryAccessOut{}, err
	}
	if rows == nil {
		rows = []store.AccessRow{}
	}
	return nil, QueryAccessOut{AuditID: id, Rows: rows,
		Note: "Exact: from robots.txt and the page's meta robots / X-Robots-Tag. Whether an agent honours them is in list_agents (robots_txt, meta_robots)."}, nil
}

type GetFindingIn struct {
	AuditRef
	FindingID string `json:"finding_id" jsonschema:"finding ID from audit_summary, e.g. js_dependent_content"`
}

type GetFindingOut struct {
	AuditID int64         `json:"audit_id"`
	Finding audit.Finding `json:"finding"`
}

func (s *Server) getFinding(ctx context.Context, _ *mcp.CallToolRequest, in GetFindingIn) (*mcp.CallToolResult, GetFindingOut, error) {
	id, err := s.resolve(ctx, in.AuditRef)
	if err != nil {
		return nil, GetFindingOut{}, err
	}
	r, err := s.st.Get(ctx, id)
	if err != nil {
		return nil, GetFindingOut{}, err
	}
	for _, f := range r.Findings {
		if f.ID == in.FindingID {
			return nil, GetFindingOut{AuditID: id, Finding: f}, nil
		}
	}
	return nil, GetFindingOut{}, fmt.Errorf("audit #%d has no finding %q", id, in.FindingID)
}

type CompareIn struct {
	Host  string `json:"host,omitempty" jsonschema:"host whose latest two audits to compare, when new_id is not given"`
	OldID int64  `json:"old_id,omitempty" jsonschema:"earlier audit; default: the audit before new_id"`
	NewID int64  `json:"new_id,omitempty" jsonschema:"later audit; default: the latest audit of host"`
}

type CompareOut struct {
	OldID      int64            `json:"old_id"`
	NewID      int64            `json:"new_id"`
	Comparison audit.Comparison `json:"comparison"`
}

func (s *Server) compareAudits(ctx context.Context, _ *mcp.CallToolRequest, in CompareIn) (*mcp.CallToolResult, CompareOut, error) {
	newID, err := s.resolve(ctx, AuditRef{AuditID: in.NewID, Host: in.Host})
	if err != nil {
		return nil, CompareOut{}, err
	}
	newR, err := s.st.Get(ctx, newID)
	if err != nil {
		return nil, CompareOut{}, err
	}
	oldID := in.OldID
	if oldID == 0 {
		if oldID, err = s.st.Previous(ctx, newR.Host, newID); err != nil {
			return nil, CompareOut{}, err
		}
		if oldID == 0 {
			return nil, CompareOut{}, fmt.Errorf("audit #%d is the first audit of %s; nothing to compare", newID, newR.Host)
		}
	}
	oldR, err := s.st.Get(ctx, oldID)
	if err != nil {
		return nil, CompareOut{}, err
	}
	return nil, CompareOut{OldID: oldID, NewID: newID, Comparison: audit.Compare(oldR, newR)}, nil
}

type ListAgentsIn struct{}

type ListAgentsOut struct {
	Agents []agents.Agent `json:"agents"`
}

func (s *Server) listAgents(context.Context, *mcp.CallToolRequest, ListAgentsIn) (*mcp.CallToolResult, ListAgentsOut, error) {
	return nil, ListAgentsOut{Agents: agents.All()}, nil
}
