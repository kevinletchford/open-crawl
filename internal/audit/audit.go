// Package audit checks a site against the AI agents in the registry and
// reports what each one may fetch and what it receives.
//
// It only claims what it can measure. robots.txt, meta robots, headers and
// raw HTML are exact. Requests sent with an agent's user agent are simulated:
// the real agent, verified by IP or signature, may be treated differently.
// Anything inferred is heuristic. Every Finding says which it is.
package audit

import (
	"context"
	"errors"
	"fmt"
	"mime"
	"net/url"
	"path"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/kevinletchford/open-crawl/internal/agents"
	"github.com/kevinletchford/open-crawl/internal/fetch"
	"github.com/kevinletchford/open-crawl/internal/htmlx"
	"github.com/kevinletchford/open-crawl/internal/robots"
	"github.com/kevinletchford/open-crawl/internal/sitemap"
)

// Config is what to audit and how politely.
type Config struct {
	URL    string
	Agents []agents.Agent // empty means every agent

	MaxPages    int           // pages to fetch, default 50
	Concurrency int           // requests in flight, default 4
	Delay       time.Duration // gap between request starts, default 250ms
	Timeout     time.Duration // per request, default 15s
	ProbePages  int           // pages to probe with agent user agents, default 3; negative disables
	// IgnoreRobots lets the audit's own crawler fetch pages robots.txt
	// disallows for it. Only for sites you own or may audit.
	IgnoreRobots bool
	UserAgent    string // the audit's own user agent, default fetch.DefaultUserAgent

	// Renderer, if set, renders up to RenderPages pages with JavaScript so
	// they can be compared with their raw HTML.
	Renderer    Renderer
	RenderPages int // default 10

	// Progress, if set, receives one-line progress messages.
	Progress func(msg string)
}

// auditToken is the robots.txt product token of the audit's own crawler.
const auditToken = "OpenCrawlAudit"

// maxSitemapFiles caps how many sitemap files one audit reads.
const maxSitemapFiles = 10

func (c *Config) defaults() error {
	if c.URL == "" {
		return errors.New("audit: no URL")
	}
	if c.MaxPages <= 0 {
		c.MaxPages = 50
	}
	if c.Concurrency <= 0 {
		c.Concurrency = 4
	}
	if c.Delay == 0 {
		c.Delay = 250 * time.Millisecond
	}
	if c.ProbePages == 0 {
		c.ProbePages = 3
	}
	if c.UserAgent == "" {
		c.UserAgent = fetch.DefaultUserAgent
	}
	if c.RenderPages <= 0 {
		c.RenderPages = 10
	}
	if len(c.Agents) == 0 {
		c.Agents = agents.All()
	}
	if c.Progress == nil {
		c.Progress = func(string) {}
	}
	return nil
}

// Run audits the site at cfg.URL.
func Run(ctx context.Context, cfg Config) (*Report, error) {
	if err := cfg.defaults(); err != nil {
		return nil, err
	}
	start, err := parseStart(cfg.URL)
	if err != nil {
		return nil, err
	}

	a := &auditor{
		cfg: cfg,
		client: fetch.New(fetch.Options{
			Timeout:     cfg.Timeout,
			Concurrency: cfg.Concurrency,
			Delay:       cfg.Delay,
		}),
		seen: map[string]bool{},
		raw:  map[string]htmlx.Page{},
		// recorded holds the URLs already in pages.
		recorded: map[string]bool{},
	}
	began := time.Now()

	// robots.txt comes first: nothing else is fetched until we know the rules.
	a.loadRobots(ctx, start)

	// Follow a redirect on the start URL to another host (example.com →
	// www.example.com) before going further, because robots.txt is per host.
	if a.mayFetch(start) {
		if res := a.client.Get(ctx, start.String(), cfg.UserAgent); res.Err == nil {
			if final, err := url.Parse(res.FinalURL); err == nil && final.Host != start.Host && sameSite(final, start) {
				cfg.Progress(fmt.Sprintf("%s redirects to %s; auditing that host", start.Host, final.Host))
				start = normalizeURL(final)
				a.loadRobots(ctx, start)
			}
		}
	}
	a.host = start.Host

	a.loadLLMsTxt(ctx, start)
	sitemapURLs := a.loadSitemaps(ctx, start)

	a.crawl(ctx, start, sitemapURLs)
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	// Pages finish in any order; report them by depth, then URL.
	slices.SortStableFunc(a.pages, func(x, y PageResult) int {
		if x.Depth != y.Depth {
			return x.Depth - y.Depth
		}
		return strings.Compare(x.URL, y.URL)
	})

	a.renderPages(ctx)
	probes := a.probe(ctx)
	score := a.scorePages(probes)

	r := &Report{
		Version:     ReportVersion,
		Tool:        "opencrawl",
		GeneratedAt: time.Now().UTC(),
		Target:      cfg.URL,
		Host:        a.host,
		Settings: Settings{
			MaxPages:     cfg.MaxPages,
			Concurrency:  cfg.Concurrency,
			DelayMs:      cfg.Delay.Milliseconds(),
			ProbePages:   cfg.ProbePages,
			RenderPages:  renderedCount(cfg),
			IgnoreRobots: cfg.IgnoreRobots,
			UserAgent:    cfg.UserAgent,
		},
		Agents:   cfg.Agents,
		Robots:   a.robots,
		LLMsTxt:  a.llms,
		Sitemaps: a.sitemaps,
		Pages:    a.pages,
		Probes:   probes,
		Score:    score,
	}
	r.Findings = findings(r)
	r.Summary = summarise(r)
	r.Duration = time.Since(began)
	return r, nil
}

type auditor struct {
	cfg    Config
	client *fetch.Client
	host   string

	robots   RobotsResult
	llms     LLMsTxtResult
	sitemaps []SitemapResult

	mu    sync.Mutex
	seen  map[string]bool
	pages []PageResult
	// raw holds each fetched HTML page's parse, for the render diff.
	raw      map[string]htmlx.Page
	recorded map[string]bool
}

func parseStart(raw string) (*url.URL, error) {
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("audit: bad URL %q: %w", raw, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" || u.Host == "" {
		return nil, fmt.Errorf("audit: %q is not an http(s) URL", raw)
	}
	if u.Path == "" {
		u.Path = "/"
	}
	return normalizeURL(u), nil
}

func (a *auditor) loadRobots(ctx context.Context, site *url.URL) {
	u := site.Scheme + "://" + site.Host + "/robots.txt"
	a.cfg.Progress("fetching " + u)
	res := a.client.GetLimited(ctx, u, a.cfg.UserAgent, robots.MaxSize)
	a.robots = RobotsResult{URL: u, Policy: robots.FromResponse(res.Status, res.Body, res.Err)}
	if a.robots.Policy.Mode == robots.ModeParsed {
		a.robots.Body = string(res.Body)
	}
}

func (a *auditor) loadLLMsTxt(ctx context.Context, site *url.URL) {
	u := site.Scheme + "://" + site.Host + "/llms.txt"
	a.cfg.Progress("fetching " + u)
	res := a.client.GetLimited(ctx, u, a.cfg.UserAgent, 1<<20)
	a.llms = LLMsTxtResult{URL: u, Status: res.Status, Bytes: len(res.Body)}
	// A soft 404 returns 200 with an HTML page, which isn't an llms.txt.
	a.llms.Present = res.OK() && !isHTML(res.Header.Get("Content-Type")) &&
		!strings.HasPrefix(strings.TrimSpace(string(res.Body)), "<")
}

// loadSitemaps reads the sitemaps robots.txt lists, or /sitemap.xml, and
// returns the same-host page URLs they contain.
func (a *auditor) loadSitemaps(ctx context.Context, site *url.URL) []string {
	var queue []string
	if f := a.robots.Policy.File; f != nil {
		queue = append(queue, f.Sitemaps...)
	}
	if len(queue) == 0 {
		queue = []string{site.Scheme + "://" + site.Host + "/sitemap.xml"}
	}

	var pages []string
	read := map[string]bool{}
	for len(queue) > 0 && len(read) < maxSitemapFiles && len(pages) < a.cfg.MaxPages*4 {
		u := queue[0]
		queue = queue[1:]
		if read[u] {
			continue
		}
		read[u] = true
		a.cfg.Progress("fetching " + u)
		res := a.client.Get(ctx, u, a.cfg.UserAgent)
		sr := SitemapResult{URL: u, Status: res.Status}
		switch {
		case res.Err != nil:
			sr.Error = res.Err.Error()
		case !res.OK():
			sr.Error = fmt.Sprintf("HTTP %d", res.Status)
		default:
			sm, err := sitemap.Parse(res.Body)
			if err != nil {
				sr.Error = err.Error()
				break
			}
			sr.IsIndex, sr.URLs = sm.IsIndex, len(sm.URLs)
			if sm.IsIndex {
				queue = append(queue, sm.URLs...)
			} else {
				pages = append(pages, sm.URLs...)
			}
		}
		a.sitemaps = append(a.sitemaps, sr)
	}
	return pages
}

type crawlItem struct {
	url    *url.URL
	depth  int
	source string
}

// crawl fetches pages breadth first, one depth level at a time, until
// MaxPages pages have been fetched or nothing is left. Every URL found is
// recorded, including those the audit's own crawler may not fetch.
func (a *auditor) crawl(ctx context.Context, start *url.URL, sitemapURLs []string) {
	level := []crawlItem{{url: start, depth: 0, source: "start"}}
	a.seen[start.String()] = true
	var fromSitemap []crawlItem
	for _, s := range sitemapURLs {
		if it, ok := a.admit(start, s, 1, "sitemap"); ok {
			fromSitemap = append(fromSitemap, it)
		}
	}

	fetched := 0
	for depth := 0; len(level) > 0 || len(fromSitemap) > 0; depth++ {
		if depth == 1 {
			// Sitemap URLs join the first level after the start page.
			level = append(level, fromSitemap...)
			fromSitemap = nil
		}
		var next []crawlItem
		var wg sync.WaitGroup
		var nextMu sync.Mutex
		for _, it := range level {
			if ctx.Err() != nil {
				return
			}
			if !a.mayFetch(it.url) {
				v := a.robots.Policy.Check(auditToken, pathAndQuery(it.url))
				a.record(PageResult{URL: it.url.String(), Depth: it.depth, Source: it.source,
					SkipReason: "robots.txt disallows the audit's own crawler: " + v.Explain()})
				continue
			}
			if fetched >= a.cfg.MaxPages {
				break
			}
			fetched++
			wg.Add(1)
			go func(it crawlItem) {
				defer wg.Done()
				links := a.visit(ctx, it)
				nextMu.Lock()
				defer nextMu.Unlock()
				for _, l := range links {
					if n, ok := a.admit(start, l, it.depth+1, "link"); ok {
						next = append(next, n)
					}
				}
			}(it)
		}
		wg.Wait()
		a.cfg.Progress(fmt.Sprintf("depth %d done: %d pages fetched", depth, fetched))
		if fetched >= a.cfg.MaxPages {
			return
		}
		level = next
	}
}

// admit normalises raw and returns it as a crawl item if it is a same-host
// page URL not seen before.
func (a *auditor) admit(start *url.URL, raw string, depth int, source string) (crawlItem, bool) {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return crawlItem{}, false
	}
	u = normalizeURL(u)
	if u.Host != start.Host || skipExtension(u.Path) {
		return crawlItem{}, false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.seen[u.String()] {
		return crawlItem{}, false
	}
	a.seen[u.String()] = true
	return crawlItem{url: u, depth: depth, source: source}, true
}

// visit fetches one page, records it, and returns its links.
func (a *auditor) visit(ctx context.Context, it crawlItem) []string {
	a.cfg.Progress("fetching " + it.url.String())
	res := a.client.Get(ctx, it.url.String(), a.cfg.UserAgent)
	pr := PageResult{
		URL: it.url.String(), Depth: it.depth, Source: it.source,
		Fetched: true, Status: res.Status, FinalURL: res.FinalURL,
	}
	if res.Err != nil {
		pr.Error = res.Err.Error()
		a.record(pr)
		return nil
	}

	// A redirect makes this the same document as its target, so record it
	// once, under the URL it ended at.
	pageURL := it.url
	if final, err := url.Parse(res.FinalURL); err == nil {
		final = normalizeURL(final)
		if final.String() != it.url.String() {
			if final.Host != it.url.Host {
				pr.Error = "redirects to another host: " + final.String()
				a.record(pr)
				return nil
			}
			a.mu.Lock()
			dup := a.seen[final.String()] && a.recorded[final.String()]
			a.seen[final.String()] = true
			a.mu.Unlock()
			if dup {
				return nil
			}
			pr.RequestedURL, pr.URL = pr.URL, final.String()
			pageURL = final
		}
	}
	pr.ContentType = res.Header.Get("Content-Type")
	pr.XRobotsTag = res.Header.Values("X-Robots-Tag")

	var d *pageDirectives
	var links []string
	if isHTML(pr.ContentType) {
		final, err := url.Parse(res.FinalURL)
		if err != nil {
			final = it.url
		}
		page := htmlx.Parse(final, res.Body)
		pr.Title, pr.Canonical = page.Title, page.Canonical
		pr.WordCount, pr.Scripts, pr.JSONLD = page.WordCount, page.Scripts, page.JSONLD
		pr.Headings, pr.EmptyAppRoot = page.Headings, page.EmptyAppRoot
		pr.HasDescription = len(page.Meta["description"]) > 0 && strings.TrimSpace(page.Meta["description"][0]) != ""
		pr.RobotsMeta = page.Directives("robots")
		dd := collectDirectives(page, pr.XRobotsTag, tokens(a.cfg.Agents))
		d = &dd
		a.mu.Lock()
		a.raw[pr.URL] = page
		a.mu.Unlock()
		if res.OK() && !slices.Contains(dd.generic, "nofollow") && !slices.Contains(dd.generic, "none") {
			links = page.Links
		}
	} else {
		dd := collectDirectives(htmlx.Page{}, pr.XRobotsTag, tokens(a.cfg.Agents))
		d = &dd
	}
	pr.Access = a.evaluateAll(pageURL, d)
	a.record(pr)
	return links
}

// renderedCount is the RenderPages setting, or 0 when rendering is off.
func renderedCount(c Config) int {
	if c.Renderer == nil {
		return 0
	}
	return c.RenderPages
}

// mayFetch reports whether the audit's own crawler may fetch u.
func (a *auditor) mayFetch(u *url.URL) bool {
	return a.cfg.IgnoreRobots || a.robots.Policy.Check(auditToken, pathAndQuery(u)).Allowed
}

func (a *auditor) record(pr PageResult) {
	if !pr.Fetched {
		u, err := url.Parse(pr.URL)
		if err == nil {
			pr.Access = a.evaluateAll(u, nil)
		}
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.recorded[pr.URL] {
		return // two redirects in one level reached the same page
	}
	a.recorded[pr.URL] = true
	a.pages = append(a.pages, pr)
}

func (a *auditor) evaluateAll(u *url.URL, d *pageDirectives) []AgentAccess {
	out := make([]AgentAccess, 0, len(a.cfg.Agents))
	for _, ag := range a.cfg.Agents {
		out = append(out, evaluate(ag, a.robots.Policy, u, d))
	}
	return out
}

func tokens(list []agents.Agent) []string {
	out := make([]string, len(list))
	for i, a := range list {
		out[i] = a.Token
	}
	return out
}

// normalizeURL drops the fragment, lower-cases the host and drops a default
// port, so equivalent URLs compare equal.
func normalizeURL(u *url.URL) *url.URL {
	n := *u
	n.Fragment, n.RawFragment = "", ""
	n.Scheme = strings.ToLower(n.Scheme)
	host := strings.ToLower(n.Host)
	if h, port, ok := strings.Cut(host, ":"); ok && (n.Scheme == "http" && port == "80" || n.Scheme == "https" && port == "443") {
		host = h
	}
	n.Host = host
	if n.Path == "" {
		n.Path = "/"
	}
	return &n
}

// sameSite reports whether two hosts differ only by a leading "www.".
func sameSite(a, b *url.URL) bool {
	return strings.TrimPrefix(a.Hostname(), "www.") == strings.TrimPrefix(b.Hostname(), "www.")
}

func isHTML(contentType string) bool {
	mt, _, err := mime.ParseMediaType(contentType)
	if err != nil {
		return strings.Contains(strings.ToLower(contentType), "html")
	}
	return mt == "text/html" || mt == "application/xhtml+xml"
}

var skippedExtensions = map[string]bool{
	".pdf": true, ".jpg": true, ".jpeg": true, ".png": true, ".gif": true, ".webp": true, ".avif": true,
	".svg": true, ".ico": true, ".css": true, ".js": true, ".mjs": true, ".json": true, ".xml": true,
	".zip": true, ".gz": true, ".mp4": true, ".webm": true, ".mp3": true, ".woff": true, ".woff2": true,
	".ttf": true, ".dmg": true, ".exe": true,
}

// skipExtension reports URLs that are almost certainly not HTML pages.
func skipExtension(p string) bool {
	return skippedExtensions[strings.ToLower(path.Ext(p))]
}
