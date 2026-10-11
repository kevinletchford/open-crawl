package audit

import (
	"time"

	"github.com/kevinletchford/open-crawl/internal/agents"
	"github.com/kevinletchford/open-crawl/internal/robots"
)

// ReportVersion is bumped whenever the JSON shape changes incompatibly.
const ReportVersion = 1

// Fidelity says how far a finding can be trusted.
type Fidelity string

const (
	// Exact findings come from public, deterministic signals: robots.txt,
	// meta robots, headers, the raw HTML a non-JS crawler receives.
	Exact Fidelity = "exact"
	// Simulated findings come from requests sent with an agent's user agent
	// from our network. The real agent, verified by IP or signature, may be
	// treated differently.
	Simulated Fidelity = "simulated"
	// Heuristic findings are inferences, such as "this page probably needs
	// JavaScript", and can be wrong.
	Heuristic Fidelity = "heuristic"
)

// Severity orders findings.
type Severity string

const (
	SeverityError   Severity = "error"
	SeverityWarning Severity = "warning"
	SeverityInfo    Severity = "info"
)

func (s Severity) rank() int {
	switch s {
	case SeverityError:
		return 0
	case SeverityWarning:
		return 1
	default:
		return 2
	}
}

// Report is the complete result of one audit. It is written as JSON and is
// the input to the HTML report.
type Report struct {
	Version     int           `json:"version"`
	Tool        string        `json:"tool"`
	GeneratedAt time.Time     `json:"generated_at"`
	Duration    time.Duration `json:"duration_ns"`
	Target      string        `json:"target"`
	Host        string        `json:"host"`
	Settings    Settings      `json:"settings"`

	Agents   []agents.Agent  `json:"agents"`
	Robots   RobotsResult    `json:"robots"`
	LLMsTxt  LLMsTxtResult   `json:"llms_txt"`
	Sitemaps []SitemapResult `json:"sitemaps"`
	Pages    []PageResult    `json:"pages"`
	Probes   []ProbeResult   `json:"probes"`
	Findings []Finding       `json:"findings"`
	Summary  Summary         `json:"summary"`
}

// Settings records the options the audit ran with.
type Settings struct {
	MaxPages     int    `json:"max_pages"`
	Concurrency  int    `json:"concurrency"`
	DelayMs      int64  `json:"delay_ms"`
	ProbePages   int    `json:"probe_pages"`
	IgnoreRobots bool   `json:"ignore_robots"`
	UserAgent    string `json:"user_agent"`
}

// RobotsResult is the site's robots.txt and how it was interpreted.
type RobotsResult struct {
	URL    string        `json:"url"`
	Policy robots.Policy `json:"policy"`
	// Body is the robots.txt text, truncated to robots.MaxSize.
	Body string `json:"body,omitempty"`
}

// LLMsTxtResult says whether /llms.txt exists.
type LLMsTxtResult struct {
	URL     string `json:"url"`
	Status  int    `json:"status"`
	Present bool   `json:"present"`
	Bytes   int    `json:"bytes"`
}

// SitemapResult is one sitemap file the audit read.
type SitemapResult struct {
	URL     string `json:"url"`
	Status  int    `json:"status"`
	IsIndex bool   `json:"is_index"`
	URLs    int    `json:"urls"`
	Error   string `json:"error,omitempty"`
}

// PageResult is one URL the audit found.
type PageResult struct {
	URL    string `json:"url"`
	Depth  int    `json:"depth"`
	Source string `json:"source"` // "start", "link" or "sitemap"

	// Fetched is false when the audit didn't request the page, for example
	// because robots.txt disallows the audit's own crawler.
	Fetched    bool   `json:"fetched"`
	SkipReason string `json:"skip_reason,omitempty"`

	Status      int    `json:"status,omitempty"`
	FinalURL    string `json:"final_url,omitempty"`
	ContentType string `json:"content_type,omitempty"`
	Error       string `json:"error,omitempty"`

	Title     string         `json:"title,omitempty"`
	Canonical string         `json:"canonical,omitempty"`
	WordCount int            `json:"word_count"`
	Scripts   int            `json:"scripts"`
	JSONLD    int            `json:"json_ld"`
	Headings  map[string]int `json:"headings,omitempty"`
	// EmptyAppRoot: the raw HTML has an empty single-page-app mount point.
	EmptyAppRoot bool `json:"empty_app_root,omitempty"`
	// XRobotsTag holds the raw X-Robots-Tag header values.
	XRobotsTag []string `json:"x_robots_tag,omitempty"`
	// RobotsMeta holds <meta name="robots"> directives.
	RobotsMeta []string `json:"robots_meta,omitempty"`

	Access []AgentAccess `json:"access"`
}

// Access states, from most to least open.
const (
	StateAllowed    = "allowed"
	StateRestricted = "restricted" // fetchable, but a noindex-style directive applies
	StateBlocked    = "blocked"    // robots.txt disallows it
)

// AgentAccess is what one agent may do with one URL. Every field is exact:
// it restates public signals and the operator's documented stance.
type AgentAccess struct {
	Agent  string         `json:"agent"`
	State  string         `json:"state"`
	Robots robots.Verdict `json:"robots"`
	// Directives are the noindex-style directives that apply to this agent,
	// from meta tags and X-Robots-Tag, generic or addressed to its token.
	Directives []string `json:"directives,omitempty"`
	// DirectivesKnown is false when the page wasn't fetched, so meta tags
	// and headers couldn't be read.
	DirectivesKnown bool `json:"directives_known"`
	// Notes qualify the state, for example "operator says robots.txt may not
	// apply to user-initiated fetches".
	Notes []string `json:"notes,omitempty"`
}

// ProbeOutcome classifies an agent-UA request against a browser-UA baseline.
type ProbeOutcome string

const (
	ProbeSame       ProbeOutcome = "same"
	ProbeBlocked    ProbeOutcome = "blocked"    // error status where the browser got 2xx/3xx
	ProbeChallenged ProbeOutcome = "challenged" // bot-challenge page where the browser got content
	ProbeDifferent  ProbeOutcome = "different"  // both succeeded, but the content differs a lot
	ProbeError      ProbeOutcome = "error"
	// ProbeInconclusive: the browser baseline was blocked or challenged too.
	ProbeInconclusive ProbeOutcome = "inconclusive"
	ProbeSkipped      ProbeOutcome = "skipped"
)

// ProbeResult compares one URL fetched with a browser user agent and with
// each agent's user agent. All of it is Simulated.
type ProbeResult struct {
	URL      string `json:"url"`
	Baseline Probe  `json:"baseline"`
	// Inconclusive is set when the baseline itself was blocked or
	// challenged, so differences say nothing about AI agents.
	Inconclusive bool    `json:"inconclusive,omitempty"`
	Agents       []Probe `json:"agents"`
}

// Probe is one request in a ProbeResult.
type Probe struct {
	Agent   string       `json:"agent,omitempty"`
	Status  int          `json:"status"`
	Bytes   int          `json:"bytes"`
	Outcome ProbeOutcome `json:"outcome,omitempty"`
	Detail  string       `json:"detail,omitempty"`
}

// Finding is one issue or observation, ranked for the reader.
type Finding struct {
	ID       string   `json:"id"`
	Severity Severity `json:"severity"`
	Fidelity Fidelity `json:"fidelity"`
	Title    string   `json:"title"`
	Detail   string   `json:"detail"`
	Fix      string   `json:"fix,omitempty"`
	Agents   []string `json:"agents,omitempty"`
	// URLs is a sample of affected URLs; Count is the full number.
	URLs  []string `json:"urls,omitempty"`
	Count int      `json:"count"`
}

// Summary is the at-a-glance view.
type Summary struct {
	PagesFound   int            `json:"pages_found"`
	PagesFetched int            `json:"pages_fetched"`
	Findings     map[string]int `json:"findings"`
	Agents       []AgentSummary `json:"agents"`
}

// AgentSummary totals one agent's access across every URL found.
type AgentSummary struct {
	Agent      string `json:"agent"`
	Kind       string `json:"kind"`
	Allowed    int    `json:"allowed"`
	Restricted int    `json:"restricted"`
	Blocked    int    `json:"blocked"`
	// Root is the robots.txt verdict for "/".
	Root string `json:"root"`
	// Probe summarises the user-agent probes: "same", "blocked", … or ""
	// when the agent wasn't probed.
	Probe string `json:"probe,omitempty"`
}
