package audit

import (
	"fmt"
	"math"
	"slices"
	"strings"

	"github.com/kevinletchford/open-crawl/internal/agents"
	"github.com/kevinletchford/open-crawl/internal/htmlx"
)

// The readability score is an open list of weighted checks, not a model of
// how any answer engine ranks pages. Every point traces back to one check on
// one page, and every check states its fidelity.

// Score categories.
const (
	CatAccess      = "access"
	CatRendering   = "rendering"
	CatStructure   = "structure"
	CatQuotability = "quotability"
)

// Categories lists the score categories in display order.
var Categories = []string{CatAccess, CatRendering, CatStructure, CatQuotability}

// Check is one scored test on one page.
type Check struct {
	ID       string   `json:"id"`
	Category string   `json:"category"`
	Title    string   `json:"title"`
	Weight   int      `json:"weight"`
	Fidelity Fidelity `json:"fidelity"`
	// Value runs from 0 (fail) to 1 (pass); some checks give partial credit.
	Value  float64 `json:"value"`
	Detail string  `json:"detail,omitempty"`
}

// Passed reports a full pass.
func (c Check) Passed() bool { return c.Value >= 1 }

// PageScore is one page's score.
type PageScore struct {
	Overall    int            `json:"overall"`
	Categories map[string]int `json:"categories"`
	Checks     []Check        `json:"checks"`
}

// SiteScore averages page scores and ranks the fixes.
type SiteScore struct {
	Overall    int            `json:"overall"`
	Categories map[string]int `json:"categories"`
	Pages      int            `json:"pages"`
	Fixes      []Fix          `json:"fixes"`
}

// Fix is one failing check, totalled across pages.
type Fix struct {
	CheckID  string   `json:"check_id"`
	Category string   `json:"category"`
	Title    string   `json:"title"`
	Fidelity Fidelity `json:"fidelity"`
	Pages    int      `json:"pages"`
	// Impact is the score points the fix would add across the site: the
	// lost weight on each failing page, summed.
	Impact float64  `json:"impact"`
	URLs   []string `json:"urls,omitempty"`
}

// scorePage runs every check on one fetched HTML page. raw is the page's
// parse; uaBlocked is true when probes saw search or user agents blocked.
func scorePage(p PageResult, raw htmlx.Page, list []agents.Agent, uaBlocked *bool) *PageScore {
	var checks []Check
	add := func(c Check) { checks = append(checks, c) }
	pass := func(ok bool) float64 {
		if ok {
			return 1
		}
		return 0
	}

	// Access.
	var answering, allowed int
	for _, acc := range p.Access {
		a, ok := findAgent(list, acc.Agent)
		if !ok || (a.Kind != agents.KindSearchIndex && a.Kind != agents.KindUserFetch) {
			continue
		}
		answering++
		if acc.State == StateAllowed {
			allowed++
		}
	}
	if answering > 0 {
		add(Check{ID: "access_answer_agents", Category: CatAccess, Weight: 3, Fidelity: Exact,
			Title: "Search and assistant agents may fetch the page",
			Value: float64(allowed) / float64(answering),
			Detail: fmt.Sprintf("%d of %d search-index and user-fetch agents are allowed (not blocked by robots.txt or restricted by noindex)",
				allowed, answering)})
	}
	add(Check{ID: "access_status_ok", Category: CatAccess, Weight: 2, Fidelity: Exact,
		Title: "The page returns HTTP 2xx", Value: pass(p.Status >= 200 && p.Status < 300),
		Detail: fmt.Sprintf("HTTP %d", p.Status)})
	generic := append(append([]string(nil), p.RobotsMeta...), genericXRobots(p.XRobotsTag)...)
	add(Check{ID: "access_indexable", Category: CatAccess, Weight: 2, Fidelity: Exact,
		Title: "No noindex for every crawler", Value: pass(!slices.Contains(generic, "noindex") && !slices.Contains(generic, "none"))})
	if uaBlocked != nil {
		add(Check{ID: "access_no_ua_blocking", Category: CatAccess, Weight: 1, Fidelity: Simulated,
			Title: "The site doesn't block search or assistant agents' user agents", Value: pass(!*uaBlocked)})
	}

	// Rendering.
	if d := p.Render; d != nil && d.Error == "" {
		add(Check{ID: "render_content_in_html", Category: CatRendering, Weight: 3, Fidelity: Exact,
			Title: "Content is in the HTML, not added by JavaScript", Value: 1 - d.MissingShare,
			Detail: fmt.Sprintf("%d of %d rendered words are missing from the raw HTML", d.MissingWords, d.RenderedWords)})
		add(Check{ID: "render_headings_in_html", Category: CatRendering, Weight: 1, Fidelity: Exact,
			Title: "Headings are in the HTML", Value: pass(len(d.MissingHeadings) == 0)})
	} else {
		ok := p.WordCount >= thinWords || (!p.EmptyAppRoot && p.Scripts == 0)
		add(Check{ID: "render_content_in_html", Category: CatRendering, Weight: 3, Fidelity: Heuristic,
			Title: "Content is in the HTML, not added by JavaScript", Value: pass(ok),
			Detail: fmt.Sprintf("not rendered; the raw HTML has %d words and %d scripts (run with -render to measure)", p.WordCount, p.Scripts)})
	}
	add(Check{ID: "render_title", Category: CatRendering, Weight: 1, Fidelity: Exact,
		Title: "The raw HTML has a <title>", Value: pass(p.Title != "")})

	// Structure.
	h1 := p.Headings["h1"]
	add(Check{ID: "structure_one_h1", Category: CatStructure, Weight: 2, Fidelity: Exact,
		Title: "Exactly one <h1>", Value: pass(h1 == 1), Detail: fmt.Sprintf("%d <h1> elements", h1)})
	if p.WordCount > 300 {
		add(Check{ID: "structure_subheadings", Category: CatStructure, Weight: 1, Fidelity: Exact,
			Title: "Long pages are broken up with <h2> headings", Value: pass(p.Headings["h2"] > 0)})
	}
	add(Check{ID: "structure_canonical", Category: CatStructure, Weight: 1, Fidelity: Exact,
		Title: "A canonical URL is declared", Value: pass(p.Canonical != "")})
	add(Check{ID: "structure_lang", Category: CatStructure, Weight: 1, Fidelity: Exact,
		Title: "The page declares its language", Value: pass(raw.Lang != "")})
	add(Check{ID: "structure_json_ld", Category: CatStructure, Weight: 1, Fidelity: Exact,
		Title: "Structured data (JSON-LD) is present", Value: pass(p.JSONLD > 0)})

	// Quotability. These are heuristics about text, not measurements of
	// whether anything is quoted.
	add(Check{ID: "quote_substantive_text", Category: CatQuotability, Weight: 2, Fidelity: Heuristic,
		Title: "At least 150 words of text in the HTML", Value: math.Min(1, float64(p.WordCount)/150),
		Detail: fmt.Sprintf("%d words", p.WordCount)})
	add(Check{ID: "quote_description", Category: CatQuotability, Weight: 1, Fidelity: Exact,
		Title: "A meta description summarises the page", Value: pass(p.HasDescription)})
	if avg, n := avgBlockWords(raw.Blocks); n >= 3 {
		add(Check{ID: "quote_short_paragraphs", Category: CatQuotability, Weight: 1, Fidelity: Heuristic,
			Title: "Paragraphs are short enough to quote", Value: pass(avg <= 80),
			Detail: fmt.Sprintf("%.0f words per text block on average", avg)})
	}
	snippetsAllowed := !slices.Contains(generic, "nosnippet") && !slices.Contains(generic, "max-snippet:0")
	add(Check{ID: "quote_snippets_allowed", Category: CatQuotability, Weight: 2, Fidelity: Exact,
		Title: "Snippets are allowed (no nosnippet or max-snippet:0)", Value: pass(snippetsAllowed)})

	return tally(checks)
}

func tally(checks []Check) *PageScore {
	s := &PageScore{Categories: map[string]int{}, Checks: checks}
	total, n := 0, 0
	for _, cat := range Categories {
		var got, max float64
		for _, c := range checks {
			if c.Category == cat {
				got += float64(c.Weight) * c.Value
				max += float64(c.Weight)
			}
		}
		if max == 0 {
			continue
		}
		v := int(math.Round(100 * got / max))
		s.Categories[cat] = v
		total += v
		n++
	}
	if n > 0 {
		s.Overall = int(math.Round(float64(total) / float64(n)))
	}
	return s
}

// siteScore averages the scored pages and ranks fixes by impact.
func siteScore(pages []PageResult) *SiteScore {
	s := &SiteScore{Categories: map[string]int{}}
	sums := map[string]int{}
	counts := map[string]int{}
	overall := 0
	fixes := map[string]*Fix{}
	var order []string
	for _, p := range pages {
		if p.Score == nil {
			continue
		}
		s.Pages++
		overall += p.Score.Overall
		for cat, v := range p.Score.Categories {
			sums[cat] += v
			counts[cat]++
		}
		for _, c := range p.Score.Checks {
			if c.Passed() {
				continue
			}
			f, ok := fixes[c.ID]
			if !ok {
				f = &Fix{CheckID: c.ID, Category: c.Category, Title: c.Title, Fidelity: c.Fidelity}
				fixes[c.ID] = f
				order = append(order, c.ID)
			}
			f.Pages++
			f.Impact += float64(c.Weight) * (1 - c.Value)
			if len(f.URLs) < maxSampleURLs {
				f.URLs = append(f.URLs, p.URL)
			}
		}
	}
	if s.Pages == 0 {
		return nil
	}
	s.Overall = int(math.Round(float64(overall) / float64(s.Pages)))
	for cat, sum := range sums {
		s.Categories[cat] = int(math.Round(float64(sum) / float64(counts[cat])))
	}
	for _, id := range order {
		f := fixes[id]
		f.Impact = math.Round(f.Impact*10) / 10
		s.Fixes = append(s.Fixes, *f)
	}
	slices.SortStableFunc(s.Fixes, func(a, b Fix) int {
		switch {
		case a.Impact > b.Impact:
			return -1
		case a.Impact < b.Impact:
			return 1
		}
		return strings.Compare(a.CheckID, b.CheckID)
	})
	return s
}

// scorePages scores every fetched 2xx HTML page and returns the site score.
func (a *auditor) scorePages(probes []ProbeResult) *SiteScore {
	uaBlocked := answerAgentsUABlocked(a.cfg.Agents, probes)
	for i := range a.pages {
		p := &a.pages[i]
		raw, ok := a.raw[p.URL]
		if !ok || p.Status < 200 || p.Status >= 300 {
			continue
		}
		p.Score = scorePage(*p, raw, a.cfg.Agents, uaBlocked)
	}
	return siteScore(a.pages)
}

// answerAgentsUABlocked is nil when nothing conclusive was probed, else
// whether any search or user-fetch agent was blocked or challenged.
func answerAgentsUABlocked(list []agents.Agent, probes []ProbeResult) *bool {
	probed, blocked := false, false
	for _, pr := range probes {
		if pr.Inconclusive {
			continue
		}
		for _, p := range pr.Agents {
			a, ok := findAgent(list, p.Agent)
			if !ok || (a.Kind != agents.KindSearchIndex && a.Kind != agents.KindUserFetch) {
				continue
			}
			switch p.Outcome {
			case ProbeSame, ProbeDifferent:
				probed = true
			case ProbeBlocked, ProbeChallenged:
				probed, blocked = true, true
			}
		}
	}
	if !probed {
		return nil
	}
	return &blocked
}

func findAgent(list []agents.Agent, name string) (agents.Agent, bool) {
	for _, a := range list {
		if a.Name == name {
			return a, true
		}
	}
	return agents.Agent{}, false
}

func avgBlockWords(blocks []string) (float64, int) {
	words, n := 0, 0
	for _, b := range blocks {
		if w := htmlx.CountWords(b); w > 0 {
			words += w
			n++
		}
	}
	if n == 0 {
		return 0, 0
	}
	return float64(words) / float64(n), n
}
