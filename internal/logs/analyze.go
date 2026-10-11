package logs

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/kevinletchford/open-crawl/internal/agents"
	"github.com/kevinletchford/open-crawl/internal/audit"
	"github.com/kevinletchford/open-crawl/internal/robots"
)

// Report is what the logs say about AI agents.
type Report struct {
	GeneratedAt time.Time `json:"generated_at"`
	Lines       int       `json:"lines"`
	Parsed      int       `json:"parsed"`
	From        time.Time `json:"from"`
	To          time.Time `json:"to"`
	// RobotsURL is the robots.txt checked for disallowed fetches, if any.
	RobotsURL string `json:"robots_url,omitempty"`
	// VerificationErrors lists IP range files that couldn't be loaded.
	VerificationErrors map[string]string `json:"verification_errors,omitempty"`
	Agents             []AgentStats      `json:"agents"`
	Findings           []audit.Finding   `json:"findings"`
}

// AgentStats totals one agent's requests.
type AgentStats struct {
	Agent string `json:"agent"`
	Kind  string `json:"kind"`
	// Requests counts every request whose user agent names this agent.
	Requests   int `json:"requests"`
	Verified   int `json:"verified"`
	Unverified int `json:"unverified"`
	Unknown    int `json:"unknown"`
	// SpoofIPs counts distinct unverified IPs.
	SpoofIPs int `json:"spoof_ips"`
	// The fields below count only requests that may be the real agent:
	// verified ones, or unknown ones when the operator publishes no way to
	// verify. Unverified (probably spoofed) requests are left out.
	Status            map[string]int `json:"status"`  // "2xx", "3xx", "4xx", "5xx"
	Refused           int            `json:"refused"` // 401, 403 or 429
	ServerErrors      int            `json:"server_errors"`
	RefusedPaths      []PathCount    `json:"refused_paths,omitempty"`
	TopPaths          []PathCount    `json:"top_paths,omitempty"`
	Disallowed        int            `json:"disallowed"` // fetches of paths robots.txt disallows for the agent
	DisallowedPaths   []PathCount    `json:"disallowed_paths,omitempty"`
	FirstSeen         time.Time      `json:"first_seen"`
	LastSeen          time.Time      `json:"last_seen"`
	VerificationNotes string         `json:"verification_notes,omitempty"`
}

// PathCount is a path and how often it appeared.
type PathCount struct {
	Path  string `json:"path"`
	Count int    `json:"count"`
}

// Analyzer accumulates requests.
type Analyzer struct {
	agents   []agents.Agent
	verifier *Verifier
	policy   *robots.Policy
	report   Report

	stats    map[string]*AgentStats
	paths    map[string]map[string]int // agent → path → count
	refused  map[string]map[string]int
	disallow map[string]map[string]int
	spoofIPs map[string]map[string]bool
}

// NewAnalyzer counts requests from list. verifier may be nil (everything
// Unknown); policy, if set, is the site's robots.txt for the disallowed
// check.
func NewAnalyzer(list []agents.Agent, verifier *Verifier, policy *robots.Policy) *Analyzer {
	a := &Analyzer{
		verifier: verifier, policy: policy,
		stats: map[string]*AgentStats{}, paths: map[string]map[string]int{}, refused: map[string]map[string]int{},
		disallow: map[string]map[string]int{}, spoofIPs: map[string]map[string]bool{},
	}
	for _, ag := range list {
		if ag.Fetches() {
			a.agents = append(a.agents, ag)
		}
	}
	return a
}

// Line records one raw log line.
func (a *Analyzer) Line(ctx context.Context, line string, f Format) {
	a.report.Lines++
	r, ok := ParseLine(line, f)
	if !ok {
		return
	}
	a.report.Parsed++
	if !r.Time.IsZero() {
		if a.report.From.IsZero() || r.Time.Before(a.report.From) {
			a.report.From = r.Time
		}
		if r.Time.After(a.report.To) {
			a.report.To = r.Time
		}
	}
	ag, ok := MatchAgent(a.agents, r.UserAgent)
	if !ok {
		return
	}
	a.add(ctx, ag, r)
}

func (a *Analyzer) add(ctx context.Context, ag agents.Agent, r Request) {
	s := a.stats[ag.Name]
	if s == nil {
		s = &AgentStats{Agent: ag.Name, Kind: string(ag.Kind), Status: map[string]int{}}
		a.stats[ag.Name] = s
		a.paths[ag.Name] = map[string]int{}
		a.refused[ag.Name] = map[string]int{}
		a.disallow[ag.Name] = map[string]int{}
		a.spoofIPs[ag.Name] = map[string]bool{}
	}
	s.Requests++

	verdict := Unknown
	if a.verifier != nil {
		verdict = a.verifier.Verify(ctx, ag.Name, r.IP)
	}
	switch verdict {
	case Verified:
		s.Verified++
	case Unverified:
		s.Unverified++
		a.spoofIPs[ag.Name][r.IP.String()] = true
		return // probably not the agent, so not evidence about it
	default:
		s.Unknown++
	}

	if !r.Time.IsZero() {
		if s.FirstSeen.IsZero() || r.Time.Before(s.FirstSeen) {
			s.FirstSeen = r.Time
		}
		if r.Time.After(s.LastSeen) {
			s.LastSeen = r.Time
		}
	}
	if r.Status >= 200 && r.Status < 600 {
		s.Status[fmt.Sprintf("%dxx", r.Status/100)]++
	}
	a.paths[ag.Name][r.Path]++
	switch {
	case r.Status == 401 || r.Status == 403 || r.Status == 429:
		s.Refused++
		a.refused[ag.Name][r.Path]++
	case r.Status >= 500:
		s.ServerErrors++
	}
	if a.policy != nil {
		if v := a.policy.Check(ag.Token, r.Path); !v.Allowed {
			s.Disallowed++
			a.disallow[ag.Name][r.Path]++
		}
	}
}

// Report finishes the analysis.
func (a *Analyzer) Report() *Report {
	r := a.report
	r.GeneratedAt = time.Now().UTC()
	if a.verifier != nil && len(a.verifier.LoadErrors) > 0 {
		r.VerificationErrors = a.verifier.LoadErrors
	}
	for _, ag := range a.agents {
		s, ok := a.stats[ag.Name]
		if !ok {
			continue
		}
		s.SpoofIPs = len(a.spoofIPs[ag.Name])
		s.TopPaths = top(a.paths[ag.Name], 10)
		s.RefusedPaths = top(a.refused[ag.Name], 10)
		s.DisallowedPaths = top(a.disallow[ag.Name], 10)
		switch {
		case ag.Verification == "":
			s.VerificationNotes = "The operator publishes no IP ranges or DNS check, so these requests can't be told apart from spoofed ones."
		case r.VerificationErrors[ag.Name] != "":
			s.VerificationNotes = "The published IP ranges couldn't be loaded, so these requests weren't verified."
		}
		r.Agents = append(r.Agents, *s)
	}
	r.Findings = findings(&r, a.agents)
	return &r
}

// MatchAgent finds the agent whose product token appears in ua as a whole
// token: "GPTBot/1.4" matches GPTBot, "Googlebot-Image/1.0" doesn't match
// Googlebot.
func MatchAgent(list []agents.Agent, ua string) (agents.Agent, bool) {
	lower := strings.ToLower(ua)
	for _, a := range list {
		tok := strings.ToLower(a.Token)
		for i := 0; ; {
			j := strings.Index(lower[i:], tok)
			if j < 0 {
				break
			}
			start, end := i+j, i+j+len(tok)
			if (start == 0 || !tokenChar(lower[start-1])) && (end == len(lower) || !tokenChar(lower[end])) {
				return a, true
			}
			i = start + 1
		}
	}
	return agents.Agent{}, false
}

func tokenChar(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-' || c == '_'
}

func top(m map[string]int, n int) []PathCount {
	out := make([]PathCount, 0, len(m))
	for p, c := range m {
		out = append(out, PathCount{p, c})
	}
	slices.SortFunc(out, func(a, b PathCount) int {
		if a.Count != b.Count {
			return b.Count - a.Count
		}
		return strings.Compare(a.Path, b.Path)
	})
	if len(out) > n {
		out = out[:n]
	}
	return out
}

func findings(r *Report, list []agents.Agent) []audit.Finding {
	var out []audit.Finding
	byName := map[string]agents.Agent{}
	for _, a := range list {
		byName[a.Name] = a
	}
	paths := func(pcs []PathCount) []string {
		var ps []string
		for _, pc := range pcs {
			ps = append(ps, fmt.Sprintf("%s (%d)", pc.Path, pc.Count))
		}
		return ps
	}

	var seen []string
	for _, s := range r.Agents {
		seen = append(seen, s.Agent)
		ag := byName[s.Agent]
		who := "the real " + s.Agent + ", verified by IP"
		if s.Unknown > 0 && s.Verified > 0 {
			who = fmt.Sprintf("%s requests (%d verified by IP, %d that couldn't be checked)", s.Agent, s.Verified, s.Unknown)
		}
		fid := audit.Exact
		if s.Verified == 0 {
			who = "requests claiming to be " + s.Agent + " (the operator publishes no way to verify them)"
			if s.VerificationNotes != "" && strings.Contains(s.VerificationNotes, "couldn't be loaded") {
				who = "requests claiming to be " + s.Agent + " (not verified: the IP list couldn't be loaded)"
			}
		}

		if s.Refused > 0 {
			sev := audit.SeverityWarning
			if ag.Kind == agents.KindTraining {
				sev = audit.SeverityInfo
			}
			out = append(out, audit.Finding{
				ID: "logs_refused_" + strings.ToLower(ag.Token), Severity: sev, Fidelity: fid,
				Title:  fmt.Sprintf("Your server refused %s %s (401/403/429)", s.Agent, plural(s.Refused, "time", "times")),
				Detail: fmt.Sprintf("Counted from %s. These are the requests your firewall or server turned away, which a crawl from outside can't see.", who),
				Fix:    "If you want this agent in, find the rule that returned 403/429 (often a CDN bot-management setting) and allow the agent's verified IPs.",
				Agents: []string{s.Agent}, URLs: paths(s.RefusedPaths), Count: s.Refused,
			})
		}
		if s.ServerErrors > 0 {
			out = append(out, audit.Finding{
				ID: "logs_server_errors_" + strings.ToLower(ag.Token), Severity: audit.SeverityWarning, Fidelity: fid,
				Title:  fmt.Sprintf("%s got %s (5xx)", s.Agent, plural(s.ServerErrors, "server error", "server errors")),
				Detail: fmt.Sprintf("Counted from %s. Crawlers that keep hitting errors usually slow down or stop.", who),
				Agents: []string{s.Agent}, Count: s.ServerErrors,
			})
		}
		if s.Unverified > 0 {
			out = append(out, audit.Finding{
				ID: "logs_spoofed_" + strings.ToLower(ag.Token), Severity: audit.SeverityInfo, Fidelity: audit.Exact,
				Title: fmt.Sprintf("%s to be %s from %s outside its published ranges", plural(s.Unverified, "request claimed", "requests claimed"), s.Agent, plural(s.SpoofIPs, "IP", "IPs")),
				Detail: "Anyone can send an AI agent's user-agent string. These requests did, from addresses the operator doesn't use, so they were left out of every other count. " +
					"Rules that trust the user-agent string let these through.",
				Fix:    "Allow or block AI agents by verified IP ranges, not by user-agent string.",
				Agents: []string{s.Agent}, Count: s.Unverified,
			})
		}
		if s.Disallowed > 0 {
			sev := audit.SeverityInfo
			detail := fmt.Sprintf("Counted from %s. robots.txt disallows these paths for %s.", who, s.Agent)
			switch ag.RobotsTxt {
			case agents.MayIgnore:
				detail += " " + ag.RobotsTxtNote
			case agents.Honours:
				if s.Verified > 0 {
					sev = audit.SeverityWarning
					detail += " The operator says it honours robots.txt. Crawlers cache robots.txt for up to a day, so recent changes can explain this."
				}
			}
			out = append(out, audit.Finding{
				ID: "logs_disallowed_" + strings.ToLower(ag.Token), Severity: sev, Fidelity: fid,
				Title:  fmt.Sprintf("%s fetched %s robots.txt disallows for it", s.Agent, plural(s.Disallowed, "URL", "URLs")),
				Detail: detail, Agents: []string{s.Agent}, URLs: paths(s.DisallowedPaths), Count: s.Disallowed,
			})
		}
	}

	var absent []string
	for _, a := range list {
		if !slices.Contains(seen, a.Name) {
			absent = append(absent, a.Name)
		}
	}
	if len(absent) > 0 && r.Parsed > 0 {
		out = append(out, audit.Finding{
			ID: "logs_not_seen", Severity: audit.SeverityInfo, Fidelity: audit.Exact,
			Title:  fmt.Sprintf("%s no requests in these logs", plural(len(absent), "agent made", "agents made")),
			Detail: "No request in the log window named these agents. That can mean robots.txt or a firewall keeps them out, or just that they didn't visit in this period.",
			Agents: absent, Count: len(absent),
		})
	}
	if r.Parsed == 0 && r.Lines > 0 {
		out = append(out, audit.Finding{
			ID: "logs_unparsed", Severity: audit.SeverityError, Fidelity: audit.Exact,
			Title:  "No log lines could be parsed",
			Detail: "Supported formats: nginx/Apache combined, and JSON lines from Cloudflare Logpush, Vercel log drains or similar. Try -format json or -format combined.",
			Count:  r.Lines,
		})
	}

	rank := map[audit.Severity]int{audit.SeverityError: 0, audit.SeverityWarning: 1, audit.SeverityInfo: 2}
	slices.SortStableFunc(out, func(a, b audit.Finding) int {
		if d := rank[a.Severity] - rank[b.Severity]; d != 0 {
			return d
		}
		// "Agents not seen" is context, not an issue: keep it last.
		if (a.ID == "logs_not_seen") != (b.ID == "logs_not_seen") {
			if a.ID == "logs_not_seen" {
				return 1
			}
			return -1
		}
		return b.Count - a.Count
	})
	return out
}

func plural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return fmt.Sprintf("%d %s", n, many)
}
