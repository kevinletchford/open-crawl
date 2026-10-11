package audit

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/kevinletchford/open-crawl/internal/agents"
)

// fakeSite serves a small site with one example of each thing the audit
// should find.
func fakeSite(t *testing.T, robotsStatus int) *httptest.Server {
	t.Helper()
	words := strings.Repeat("Plain server-rendered words that any crawler can read. ", 20)
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ua := r.UserAgent()
		switch {
		case strings.Contains(ua, "ClaudeBot"):
			http.Error(w, "Forbidden", http.StatusForbidden)
			return
		case strings.Contains(ua, "Bytespider"):
			w.Header().Set("Content-Type", "text/html")
			fmt.Fprint(w, `<html><head><title>Just a moment...</title></head><body><div id="cf-chl-widget"></div></body></html>`)
			return
		}

		html := func(body string) {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			fmt.Fprint(w, body)
		}
		switch r.URL.Path {
		case "/robots.txt":
			if robotsStatus != http.StatusOK {
				w.WriteHeader(robotsStatus)
				return
			}
			fmt.Fprintf(w, "User-agent: *\nDisallow: /private\n\nUser-agent: GPTBot\nDisallow: /\n\nUser-agent: PerplexityBot\nDisallow: /private\nDisallow: /about\n\nUser-agent: Google-Extended\nDisallow: /\n\nSitemap: %s/sitemap.xml\n", srv.URL)
		case "/sitemap.xml":
			fmt.Fprintf(w, `<urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9"><url><loc>%[1]s/</loc></url><url><loc>%[1]s/private/report</loc></url><url><loc>%[1]s/about</loc></url></urlset>`, srv.URL)
		case "/":
			html(`<html><head><title>Home</title></head><body><h1>Home</h1><p>` + words + `</p>
<a href="/about">About</a> <a href="/app">App</a> <a href="/hidden">Hidden</a> <a href="/gone">Gone</a>
<a href="/private/report">Report</a> <a href="https://elsewhere.example/">Out</a> <a href="/logo.png">Logo</a></body></html>`)
		case "/about":
			html(`<html><head><title>About</title></head><body><h1>About</h1><p>` + words + `</p></body></html>`)
		case "/app":
			html(`<html><head><title>App</title><script src="/bundle.js"></script></head><body><div id="root"></div></body></html>`)
		case "/hidden":
			html(`<html><head><title>Hidden</title><meta name="robots" content="noindex"></head><body><p>` + words + `</p></body></html>`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func runAudit(t *testing.T, srv *httptest.Server, mutate func(*Config)) *Report {
	t.Helper()
	cfg := Config{
		URL:        srv.URL,
		Delay:      time.Millisecond,
		ProbePages: 2,
	}
	if mutate != nil {
		mutate(&cfg)
	}
	r, err := Run(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func findingByID(r *Report, id string) *Finding {
	for i := range r.Findings {
		if r.Findings[i].ID == id {
			return &r.Findings[i]
		}
	}
	return nil
}

func pageByPath(r *Report, path string) *PageResult {
	for i := range r.Pages {
		if strings.HasSuffix(r.Pages[i].URL, path) {
			return &r.Pages[i]
		}
	}
	return nil
}

func access(p *PageResult, agent string) AgentAccess {
	for _, a := range p.Access {
		if a.Agent == agent {
			return a
		}
	}
	return AgentAccess{}
}

func TestAuditFindsWhatTheSiteDoes(t *testing.T) {
	srv := fakeSite(t, http.StatusOK)
	r := runAudit(t, srv, nil)

	// Discovery: links and the sitemap, same host only, no images.
	for _, p := range []string{"/", "/about", "/app", "/hidden", "/gone", "/private/report"} {
		if pageByPath(r, p) == nil {
			t.Errorf("page %s not found", p)
		}
	}
	if pageByPath(r, "/logo.png") != nil {
		t.Error("image link should not be crawled")
	}
	for _, p := range r.Pages {
		if strings.Contains(p.URL, "elsewhere.example") {
			t.Error("off-site link should not be crawled")
		}
	}

	// The audit's own crawler obeys "*" and skips /private, but still
	// reports every agent's verdict for it.
	priv := pageByPath(r, "/private/report")
	if priv.Fetched {
		t.Error("/private/report should not be fetched")
	}
	if a := access(priv, "PerplexityBot"); a.State != StateBlocked || a.DirectivesKnown {
		t.Errorf("PerplexityBot on /private/report = %+v", a)
	}
	if a := access(priv, "ClaudeBot"); a.State != StateBlocked || a.Robots.Group != "*" {
		t.Errorf("ClaudeBot falls back to *: %+v", a)
	}

	// robots.txt verdicts.
	if f := findingByID(r, "robots_blocks_gptbot"); f == nil || f.Severity != SeverityInfo || f.Fidelity != Exact ||
		!strings.Contains(f.Title, "the whole site") {
		t.Errorf("GPTBot finding = %+v", f)
	}
	if f := findingByID(r, "robots_blocks_perplexitybot"); f == nil || f.Severity != SeverityWarning || f.Count != 2 {
		t.Errorf("PerplexityBot finding = %+v", f)
	}
	if f := findingByID(r, "robots_blocks_google-extended"); f == nil || !strings.Contains(f.Detail, "Googlebot still fetches") {
		t.Errorf("Google-Extended finding = %+v", f)
	}
	// Agents without a group of their own share one catch-all finding.
	if f := findingByID(r, "robots_blocks_wildcard"); f == nil || f.Severity != SeverityInfo ||
		!slices.Contains(f.Agents, "ClaudeBot") || slices.Contains(f.Agents, "PerplexityBot") || slices.Contains(f.Agents, "GPTBot") {
		t.Errorf("wildcard finding = %+v", f)
	}
	if findingByID(r, "robots_blocks_claudebot") != nil {
		t.Error("ClaudeBot has no group of its own, so it belongs in the wildcard finding")
	}
	if a := access(pageByPath(r, "/about"), "OAI-SearchBot"); a.State != StateAllowed {
		t.Errorf("OAI-SearchBot on /about = %+v", a)
	}

	// Directives: noindex restricts agents that honour meta robots, but not
	// those whose operators say they ignore it.
	hidden := pageByPath(r, "/hidden")
	if a := access(hidden, "Googlebot"); a.State != StateRestricted || !slices.Contains(a.Directives, "noindex") {
		t.Errorf("Googlebot on /hidden = %+v", a)
	}
	if a := access(hidden, "Amazonbot"); a.State != StateAllowed || len(a.Notes) == 0 {
		t.Errorf("Amazonbot on /hidden = %+v", a)
	}
	if f := findingByID(r, "noindex"); f == nil || f.Count != 1 {
		t.Errorf("noindex finding = %+v", f)
	}

	// Raw HTML: the SPA shell is flagged, as a heuristic.
	if f := findingByID(r, "thin_raw_html"); f == nil || f.Fidelity != Heuristic || f.Count != 1 || !strings.HasSuffix(f.URLs[0], "/app") {
		t.Errorf("thin HTML finding = %+v", f)
	}

	// Probes: simulated, never run for agents robots.txt keeps out.
	f := findingByID(r, "ua_blocked")
	if f == nil || f.Fidelity != Simulated {
		t.Fatalf("ua_blocked finding = %+v", f)
	}
	if !slices.Contains(f.Agents, "ClaudeBot") || !slices.Contains(f.Agents, "Bytespider") || slices.Contains(f.Agents, "GPTBot") {
		t.Errorf("ua_blocked agents = %v", f.Agents)
	}
	for _, pr := range r.Probes {
		for _, p := range pr.Agents {
			if p.Agent == "GPTBot" && p.Outcome != ProbeSkipped {
				t.Errorf("GPTBot probe on %s = %+v; robots.txt disallows it, so it must not be impersonated", pr.URL, p)
			}
			if p.Agent == "Google-Extended" {
				t.Error("token-only agents send no requests and must not be probed")
			}
		}
	}

	// Everything else.
	if findingByID(r, "fetch_errors") == nil {
		t.Error("expected /gone to be reported as an error")
	}
	if findingByID(r, "audit_disallowed") == nil {
		t.Error("expected the skipped /private URL to be reported")
	}
	if findingByID(r, "llms_txt_missing") == nil {
		t.Error("expected llms_txt_missing")
	}
	if findingByID(r, "no_sitemap") != nil {
		t.Error("the sitemap exists")
	}
	for i := 1; i < len(r.Findings); i++ {
		if r.Findings[i-1].Severity.rank() > r.Findings[i].Severity.rank() {
			t.Errorf("findings not sorted by severity at %d", i)
		}
	}

	var gpt AgentSummary
	for _, s := range r.Summary.Agents {
		if s.Agent == "GPTBot" {
			gpt = s
		}
	}
	if gpt.Root != "blocked" || gpt.Allowed != 0 || gpt.Blocked != r.Summary.PagesFound {
		t.Errorf("GPTBot summary = %+v (pages %d)", gpt, r.Summary.PagesFound)
	}
}

func TestAuditRobotsServerError(t *testing.T) {
	srv := fakeSite(t, http.StatusServiceUnavailable)
	r := runAudit(t, srv, nil)

	f := findingByID(r, "robots_unreachable")
	if f == nil || f.Severity != SeverityError {
		t.Fatalf("robots_unreachable = %+v", f)
	}
	if r.Summary.PagesFetched != 0 {
		t.Errorf("fetched %d pages; a 503 robots.txt disallows everything, including the audit", r.Summary.PagesFetched)
	}

	r = runAudit(t, srv, func(c *Config) { c.IgnoreRobots = true })
	if r.Summary.PagesFetched == 0 {
		t.Error("IgnoreRobots should let the audit fetch pages")
	}
}

func TestAuditAgentSelectionAndLimits(t *testing.T) {
	srv := fakeSite(t, http.StatusOK)
	sel, err := agents.Select([]string{"GPTBot", "Googlebot"})
	if err != nil {
		t.Fatal(err)
	}
	r := runAudit(t, srv, func(c *Config) {
		c.Agents = sel
		c.MaxPages = 2
		c.ProbePages = -1
	})
	if r.Summary.PagesFetched != 2 {
		t.Errorf("fetched %d pages, want 2", r.Summary.PagesFetched)
	}
	if len(r.Probes) != 0 {
		t.Error("probes should be disabled")
	}
	for _, p := range r.Pages {
		if len(p.Access) != 2 {
			t.Fatalf("%s: %d agents evaluated, want 2", p.URL, len(p.Access))
		}
	}
}
