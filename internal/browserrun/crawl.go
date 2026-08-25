package browserrun

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/chromedp/chromedp"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

// --- HTTP handlers ---

func (s *Server) handleCrawlCreate(w http.ResponseWriter, r *http.Request) {
	var req CrawlRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, errResponse(400, "invalid request body: "+err.Error()))
		return
	}
	if req.URL == "" {
		writeJSON(w, http.StatusBadRequest, errResponse(400, "url is required"))
		return
	}
	if req.Limit <= 0 {
		req.Limit = 10
	}
	if req.Limit > 100000 {
		req.Limit = 100000
	}
	if req.Depth <= 0 {
		req.Depth = 100000
	}
	if req.DelayMs <= 0 {
		req.DelayMs = s.cfg.Crawl.DefaultDelayMs
	}
	if len(req.Formats) == 0 {
		req.Formats = []string{"markdown"}
	}
	if containsStr(req.Formats, "json") && req.JSONOptions == nil {
		writeJSON(w, http.StatusBadRequest, errResponse(400, "jsonOptions is required when formats includes 'json'"))
		return
	}
	render := true
	if req.Render != nil {
		render = *req.Render
	}
	req.Render = &render

	job := &CrawlJob{
		ID:         uuid.New().String(),
		Status:     "queued",
		SeedURL:    req.URL,
		Config:     req,
		PagesLimit: req.Limit,
		ExpiresAt:  time.Now().Add(s.cfg.Crawl.ResultTTL),
	}

	if err := s.store.CreateJob(job); err != nil {
		writeJSON(w, http.StatusInternalServerError, errResponse(500, err.Error()))
		return
	}

	select {
	case s.crawlQueue <- job.ID:
	default:
	}

	// Return just the UUID string, matching Cloudflare's response shape.
	writeJSON(w, http.StatusAccepted, successResponse(job.ID))
}

func (s *Server) handleCrawlGet(w http.ResponseWriter, r *http.Request) {
	jobID := chi.URLParam(r, "jobID")
	job, err := s.store.GetJob(jobID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errResponse(500, err.Error()))
		return
	}
	if job == nil {
		writeJSON(w, http.StatusNotFound, errResponse(404, "job not found"))
		return
	}

	// Pagination params
	limit := 50
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			limit = n
		}
	}
	var cursor int64
	if v := r.URL.Query().Get("cursor"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			cursor = n
		}
	}
	statusFilter := r.URL.Query().Get("status")

	rows, nextCursor, err := s.store.GetResultsCursor(jobID, limit, cursor, statusFilter)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errResponse(500, err.Error()))
		return
	}

	type recordMetadata struct {
		Status int    `json:"status"`
		Title  string `json:"title"`
		URL    string `json:"url"`
	}
	type crawlRecord struct {
		URL      string         `json:"url"`
		Status   string         `json:"status"`
		Markdown string         `json:"markdown,omitempty"`
		HTML     string         `json:"html,omitempty"`
		JSON     string         `json:"json,omitempty"`
		Metadata recordMetadata `json:"metadata"`
	}

	records := make([]crawlRecord, 0, len(rows))
	for _, row := range rows {
		records = append(records, crawlRecord{
			URL:      row.URL,
			Status:   row.URLStatus,
			Markdown: row.Markdown,
			HTML:     row.HTML,
			JSON:     row.JSONResult,
			Metadata: recordMetadata{
				Status: row.StatusCode,
				Title:  row.Title,
				URL:    row.URL,
			},
		})
	}

	total := s.store.GetResultsTotal(jobID)

	writeJSON(w, http.StatusOK, successResponse(map[string]interface{}{
		"id":                 job.ID,
		"status":             job.Status,
		"total":              total,
		"finished":           job.PagesVisited,
		"browserSecondsUsed": job.BrowserSecondsUsed,
		"cursor":             nextCursor,
		"records":            records,
	}))
}

func (s *Server) handleCrawlCancel(w http.ResponseWriter, r *http.Request) {
	jobID := chi.URLParam(r, "jobID")
	job, err := s.store.GetJob(jobID)
	if err != nil || job == nil {
		writeJSON(w, http.StatusNotFound, errResponse(404, "job not found"))
		return
	}
	if job.Status != "queued" && job.Status != "running" {
		writeJSON(w, http.StatusBadRequest, errResponse(400, "job is not cancellable"))
		return
	}

	s.cancelJobMu.Lock()
	if cancel, ok := s.cancelJobs[jobID]; ok {
		cancel()
	}
	s.cancelJobMu.Unlock()

	s.store.UpdateJobStatus(jobID, "cancelled_by_user")
	w.WriteHeader(http.StatusOK)
}

// --- Background worker pool ---

func (s *Server) startCrawlWorkers(ctx context.Context) {
	n := s.cfg.Crawl.MaxConcurrentJobs
	if n <= 0 {
		n = 3
	}
	for i := 0; i < n; i++ {
		go s.crawlWorker(ctx)
	}
}

func (s *Server) crawlWorker(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case jobID := <-s.crawlQueue:
			s.runCrawlJob(ctx, jobID)
		}
	}
}

func (s *Server) runCrawlJob(ctx context.Context, jobID string) {
	job, err := s.store.GetJob(jobID)
	if err != nil || job == nil {
		return
	}

	jobCtx, cancel := context.WithCancel(ctx)

	s.cancelJobMu.Lock()
	s.cancelJobs[jobID] = cancel
	s.cancelJobMu.Unlock()

	defer func() {
		cancel()
		s.cancelJobMu.Lock()
		delete(s.cancelJobs, jobID)
		s.cancelJobMu.Unlock()
	}()

	s.store.UpdateJobStatus(jobID, "running")

	req := job.Config
	render := true
	if req.Render != nil {
		render = *req.Render
	}

	err = s.executeCrawl(jobCtx, jobID, crawlConfig{
		seedURL:        req.URL,
		limit:          req.Limit,
		depth:          req.Depth,
		formats:        req.Formats,
		options:        req.Options,
		jsonOptions:    req.JSONOptions,
		source:         req.Source,
		delayMs:        req.DelayMs,
		maxConcurrency: req.MaxConcurrency,
		render:         render,
	})

	if err != nil && jobCtx.Err() == nil {
		s.store.UpdateJobStatus(jobID, "errored")
		return
	}
	if jobCtx.Err() == nil {
		s.store.UpdateJobStatus(jobID, "completed")
	}
}

type crawlConfig struct {
	seedURL        string
	limit          int
	depth          int
	formats        []string
	options        CrawlOptions
	jsonOptions    *JSONOptions
	source         string
	delayMs        int
	maxConcurrency int
	render         bool
}

func (s *Server) executeCrawl(ctx context.Context, jobID string, cfg crawlConfig) error {
	visited := &sync.Map{}
	queue := make(chan crawlTask, 100000)

	concurrency := cfg.maxConcurrency
	if concurrency <= 0 {
		concurrency = 3
	}
	sem := make(chan struct{}, concurrency)

	seedBase, err := crawlBaseURL(cfg.seedURL)
	if err != nil {
		return err
	}
	seedDomain := hostFromBase(seedBase)

	includeREs := compileGlobs(cfg.options.IncludePatterns)
	excludeREs := compileGlobs(cfg.options.ExcludePatterns)

	delay := time.Duration(cfg.delayMs) * time.Millisecond
	if delay <= 0 {
		delay = 500 * time.Millisecond
	}

	// Seed the queue based on source mode.
	switch cfg.source {
	case "sitemaps":
		// Seed exclusively from sitemap; don't add the seed URL itself.
		sitemapURLs := fetchSitemapURLs(seedBase)
		for _, u := range sitemapURLs {
			if _, loaded := visited.LoadOrStore(u, true); !loaded {
				select {
				case queue <- crawlTask{url: u, depth: 0}:
				default:
				}
			}
		}
		// If sitemap is empty, fall through to seed URL.
		if len(sitemapURLs) == 0 {
			queue <- crawlTask{url: cfg.seedURL, depth: 0}
			visited.Store(cfg.seedURL, true)
		}
	case "all":
		// Seed from both the start URL and sitemaps.
		queue <- crawlTask{url: cfg.seedURL, depth: 0}
		visited.Store(cfg.seedURL, true)
		for _, u := range fetchSitemapURLs(seedBase) {
			if _, loaded := visited.LoadOrStore(u, true); !loaded {
				select {
				case queue <- crawlTask{url: u, depth: 0}:
				default:
				}
			}
		}
	default: // "links" or unset
		queue <- crawlTask{url: cfg.seedURL, depth: 0}
		visited.Store(cfg.seedURL, true)
	}

	var wg sync.WaitGroup
	var count int
	var mu sync.Mutex

	for {
		select {
		case <-ctx.Done():
			wg.Wait()
			return nil
		case task := <-queue:
			mu.Lock()
			if count >= cfg.limit {
				mu.Unlock()
				wg.Wait()
				return nil
			}
			count++
			mu.Unlock()

			sem <- struct{}{}
			wg.Add(1)

			go func(t crawlTask) {
				defer func() {
					<-sem
					wg.Done()
				}()

				pageStart := time.Now()
				links, _ := s.crawlPage(ctx, jobID, t.url, cfg.formats, cfg.render, cfg.jsonOptions)
				s.store.IncrementJobPages(jobID)
				s.store.AddBrowserSeconds(jobID, time.Since(pageStart).Seconds())

				if t.depth >= cfg.depth {
					return
				}

				for _, link := range links {
					resolved := resolveLink(link, t.url)
					if resolved == "" {
						continue
					}

					// Apply domain filtering.
					if !cfg.options.IncludeExternalLinks {
						if cfg.options.IncludeSubdomains {
							if !isSameDomainOrSubdomain(resolved, seedDomain) {
								continue
							}
						} else {
							if !startsWith(resolved, seedBase) {
								continue
							}
						}
					}

					if !globMatch(resolved, includeREs, excludeREs) {
						continue
					}
					if _, loaded := visited.LoadOrStore(resolved, true); loaded {
						continue
					}
					select {
					case queue <- crawlTask{url: resolved, depth: t.depth + 1}:
					case <-ctx.Done():
						return
					default:
					}
				}
				time.Sleep(delay)
			}(task)
		}
	}
}

type crawlTask struct {
	url   string
	depth int
}

func (s *Server) crawlPage(ctx context.Context, jobID, pageURL string, formats []string, render bool, jsonOpts *JSONOptions) ([]string, error) {
	var pageHTML string
	var links []string
	var title string
	statusCode := 200

	if render {
		bCtx, release, err := s.pool.Acquire(ctx)
		if err != nil {
			s.store.SaveResult(&CrawlResultRow{
				JobID: jobID, URL: pageURL, URLStatus: "errored", StatusCode: 0,
			})
			return nil, err
		}
		defer release()

		actions, err := buildActions(CommonParams{URL: pageURL})
		if err != nil {
			return nil, err
		}
		actions = append(actions,
			outerHTMLAction(&pageHTML),
			linksAction(&links),
			chromedp.Evaluate(`document.title`, &title),
		)
		if err := chromedp.Run(bCtx, actions...); err != nil {
			s.store.SaveResult(&CrawlResultRow{
				JobID: jobID, URL: pageURL, URLStatus: "errored", StatusCode: 0,
			})
			return nil, err
		}
	}

	result := &CrawlResultRow{
		JobID:      jobID,
		URL:        pageURL,
		URLStatus:  "completed",
		StatusCode: statusCode,
		Title:      title,
	}
	for _, f := range formats {
		switch f {
		case "html":
			result.HTML = pageHTML
		case "markdown":
			if pageHTML != "" {
				result.Markdown, _ = htmlToMarkdown(pageHTML, pageURL)
			}
		case "json":
			if jsonOpts != nil && pageHTML != "" {
				md, _ := htmlToMarkdown(pageHTML, pageURL)
				extracted, err := s.llm.Extract(ctx, md, jsonOpts.Prompt, jsonOpts.ResponseFormat, jsonOpts.CustomAI)
				if err == nil {
					result.JSONResult = string(extracted)
				}
			}
		}
	}
	s.store.SaveResult(result)
	return links, nil
}

// --- URL / pattern helpers ---

func compileGlobs(patterns []string) []*regexp.Regexp {
	out := make([]*regexp.Regexp, 0, len(patterns))
	for _, p := range patterns {
		escaped := regexp.QuoteMeta(p)
		escaped = regexp.MustCompile(`\\\*`).ReplaceAllString(escaped, `.*`)
		escaped = regexp.MustCompile(`\\\?`).ReplaceAllString(escaped, `.`)
		r, err := regexp.Compile(`(?i)` + escaped)
		if err == nil {
			out = append(out, r)
		}
	}
	return out
}

func globMatch(u string, include, exclude []*regexp.Regexp) bool {
	if len(include) > 0 {
		matched := false
		for _, re := range include {
			if re.MatchString(u) {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}
	for _, re := range exclude {
		if re.MatchString(u) {
			return false
		}
	}
	return true
}

func crawlBaseURL(rawURL string) (string, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return "", err
	}
	return u.Scheme + "://" + u.Host, nil
}

func hostFromBase(base string) string {
	u, err := url.Parse(base)
	if err != nil {
		return base
	}
	return u.Hostname()
}

// isSameDomainOrSubdomain returns true if resolved shares the same host or is a subdomain of seedDomain.
func isSameDomainOrSubdomain(resolved, seedDomain string) bool {
	u, err := url.Parse(resolved)
	if err != nil {
		return false
	}
	h := u.Hostname()
	return h == seedDomain || strings.HasSuffix(h, "."+seedDomain)
}

// crawlResolve resolves link relative to pageURL, filtering to seedBase domain.
// Returns "" for empty, hash-only, or cross-domain links.
func crawlResolve(link, pageURL, seedBase string) string {
	resolved := resolveLink(link, pageURL)
	if resolved == "" {
		return ""
	}
	if !startsWith(resolved, seedBase) {
		return ""
	}
	return resolved
}

// resolveLink resolves a link relative to pageURL without domain filtering.
// Returns "" for empty, hash-only, or unparseable links.
func resolveLink(link, pageURL string) string {
	if link == "" || link == "#" || (len(link) > 0 && link[0] == '#') {
		return ""
	}
	u, err := url.Parse(link)
	if err != nil {
		return ""
	}
	if u.IsAbs() {
		u.Fragment = ""
		return u.String()
	}
	base, err := url.Parse(pageURL)
	if err != nil {
		return ""
	}
	resolved := base.ResolveReference(u)
	resolved.Fragment = ""
	return resolved.String()
}

// --- Sitemap fetching ---

type sitemapURLSet struct {
	XMLName xml.Name     `xml:"urlset"`
	URLs    []sitemapURL `xml:"url"`
}

type sitemapIndex struct {
	XMLName  xml.Name        `xml:"sitemapindex"`
	Sitemaps []sitemapEntry  `xml:"sitemap"`
}

type sitemapURL struct {
	Loc string `xml:"loc"`
}

type sitemapEntry struct {
	Loc string `xml:"loc"`
}

func fetchSitemapURLs(seedBase string) []string {
	candidates := []string{
		seedBase + "/sitemap.xml",
		seedBase + "/sitemap_index.xml",
	}
	var urls []string
	seen := map[string]bool{}
	for _, loc := range candidates {
		urls = append(urls, fetchOneSitemap(loc, seen, 0)...)
	}
	return urls
}

func fetchOneSitemap(loc string, seen map[string]bool, depth int) []string {
	if depth > 3 || seen[loc] {
		return nil
	}
	seen[loc] = true

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Get(loc)
	if err != nil || resp.StatusCode != 200 {
		return nil
	}
	defer resp.Body.Close()

	var urlSet sitemapURLSet
	var idxSet sitemapIndex

	// Try as sitemap index first
	dec := xml.NewDecoder(resp.Body)
	if err := dec.Decode(&idxSet); err == nil && len(idxSet.Sitemaps) > 0 {
		var urls []string
		for _, sm := range idxSet.Sitemaps {
			urls = append(urls, fetchOneSitemap(sm.Loc, seen, depth+1)...)
		}
		return urls
	}

	// Re-fetch as regular sitemap (body already consumed)
	resp2, err := client.Get(loc)
	if err != nil || resp2.StatusCode != 200 {
		return nil
	}
	defer resp2.Body.Close()
	if err := xml.NewDecoder(resp2.Body).Decode(&urlSet); err != nil {
		return nil
	}
	var urls []string
	for _, u := range urlSet.URLs {
		if u.Loc != "" {
			urls = append(urls, u.Loc)
		}
	}
	return urls
}

// containsStr reports whether ss contains s.
func containsStr(ss []string, s string) bool {
	for _, v := range ss {
		if v == s {
			return true
		}
	}
	return false
}

// startsWith reports whether s starts with prefix.
func startsWith(s, prefix string) bool {
	return len(s) >= len(prefix) && s[:len(prefix)] == prefix
}
