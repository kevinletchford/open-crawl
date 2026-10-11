package audit

import (
	"context"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/kevinletchford/open-crawl/internal/fetch"
	"github.com/kevinletchford/open-crawl/internal/htmlx"
	"github.com/kevinletchford/open-crawl/internal/render"
)

// Renderer loads a page with JavaScript. *render.Pool implements it; tests
// use a fake.
type Renderer interface {
	Render(ctx context.Context, url, userAgent string) (render.Page, error)
}

// RenderDiff compares a page's raw HTML, which crawlers that don't run
// JavaScript receive, with the DOM after JavaScript has run in Chrome.
type RenderDiff struct {
	RawWords      int `json:"raw_words"`
	RenderedWords int `json:"rendered_words"`
	// MissingWords counts words in rendered text blocks that don't appear
	// anywhere in the raw HTML.
	MissingWords int `json:"missing_words"`
	// MissingShare is MissingWords / RenderedWords.
	MissingShare float64 `json:"missing_share"`
	// MissingHeadings are rendered headings absent from the raw HTML.
	MissingHeadings []string `json:"missing_headings,omitempty"`
	// MissingBlocks is a sample of rendered text blocks absent from the raw
	// HTML, each cut to maxBlockChars.
	MissingBlocks []string `json:"missing_blocks,omitempty"`
	// MissingLinks counts same-page links present only after rendering.
	MissingLinks int    `json:"missing_links"`
	DurationMs   int64  `json:"duration_ms"`
	Error        string `json:"error,omitempty"`
}

const (
	maxMissingSamples = 5
	maxBlockChars     = 160
	maxDiffBlocks     = 500
)

// diffPages compares the raw and rendered versions of one page.
func diffPages(raw, rendered htmlx.Page) RenderDiff {
	d := RenderDiff{RawWords: raw.WordCount, RenderedWords: rendered.WordCount}

	var missing []string
	seen := map[string]bool{}
	for _, b := range rendered.Blocks {
		if len(missing) >= maxDiffBlocks {
			break
		}
		key := strings.ToLower(b)
		if seen[key] || strings.Contains(raw.Text, key) {
			continue
		}
		seen[key] = true
		missing = append(missing, b)
	}

	// htmlx blocks never overlap, so their words can simply be added up.
	for _, b := range missing {
		d.MissingWords += htmlx.CountWords(b)
		if len(d.MissingBlocks) < maxMissingSamples {
			d.MissingBlocks = append(d.MissingBlocks, truncate(b, maxBlockChars))
		}
	}
	if d.RenderedWords > 0 {
		d.MissingShare = min(1, float64(d.MissingWords)/float64(d.RenderedWords))
	}

	for _, h := range rendered.HeadingTexts {
		if !strings.Contains(raw.Text, strings.ToLower(h)) && !slices.Contains(d.MissingHeadings, h) {
			d.MissingHeadings = append(d.MissingHeadings, h)
		}
	}

	rawLinks := map[string]bool{}
	for _, l := range raw.Links {
		rawLinks[l] = true
	}
	for _, l := range rendered.Links {
		if !rawLinks[l] {
			d.MissingLinks++
		}
	}
	return d
}

// renderPages renders up to RenderPages fetched HTML pages and stores a
// RenderDiff on each.
func (a *auditor) renderPages(ctx context.Context) {
	if a.cfg.Renderer == nil {
		return
	}
	var idx []int
	for i, p := range a.pages {
		if len(idx) >= a.cfg.RenderPages {
			break
		}
		if _, ok := a.raw[p.URL]; ok && p.Status >= 200 && p.Status < 300 {
			idx = append(idx, i)
		}
	}

	var wg sync.WaitGroup
	for _, i := range idx {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			p := &a.pages[i]
			a.cfg.Progress("rendering " + p.URL)
			start := time.Now()
			page, err := a.cfg.Renderer.Render(ctx, p.URL, fetch.BrowserUserAgent)
			if err != nil {
				p.Render = &RenderDiff{Error: err.Error(), DurationMs: time.Since(start).Milliseconds()}
				return
			}
			base, err := url.Parse(page.FinalURL)
			if err != nil || base.Host == "" {
				base, _ = url.Parse(p.URL)
			}
			d := diffPages(a.raw[p.URL], htmlx.Parse(base, []byte(page.HTML)))
			d.DurationMs = time.Since(start).Milliseconds()
			p.Render = &d
		}(i)
	}
	wg.Wait()
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}
