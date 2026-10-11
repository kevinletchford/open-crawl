package audit

import (
	"fmt"
	"slices"
	"strings"

	"github.com/kevinletchford/open-crawl/internal/agents"
	"github.com/kevinletchford/open-crawl/internal/htmlx"
	"github.com/kevinletchford/open-crawl/internal/robots"
)

// maxSampleURLs caps the URLs listed on one finding.
const maxSampleURLs = 20

// thinWords is the word count below which a page's raw HTML is treated as
// probably empty without JavaScript.
const thinWords = 50

// findings derives every finding from a report's raw results, ranked by
// severity and then by how much they affect.
func findings(r *Report) []Finding {
	var out []Finding
	add := func(f Finding) {
		if len(f.URLs) > maxSampleURLs {
			f.URLs = f.URLs[:maxSampleURLs]
		}
		out = append(out, f)
	}

	robotsFindings(r, add)
	agentRobotsFindings(r, add)
	directiveFindings(r, add)
	probeFindings(r, add)
	renderFindings(r, add)
	thinHTMLFinding(r, add)
	crawlFindings(r, add)

	slices.SortStableFunc(out, func(x, y Finding) int {
		if d := x.Severity.rank() - y.Severity.rank(); d != 0 {
			return d
		}
		return y.Count*max(1, len(y.Agents)) - x.Count*max(1, len(x.Agents))
	})
	return out
}

func robotsFindings(r *Report, add func(Finding)) {
	p := r.Robots.Policy
	switch p.Mode {
	case robots.ModeDisallowAll:
		why := fmt.Sprintf("returned HTTP %d", p.Status)
		if p.Error != "" {
			why = "could not be fetched (" + p.Error + ")"
		}
		add(Finding{
			ID: "robots_unreachable", Severity: SeverityError, Fidelity: Exact,
			Title:  "robots.txt is unreachable, so compliant crawlers treat the whole site as disallowed",
			Detail: fmt.Sprintf("%s %s. Under RFC 9309 a crawler that can't read robots.txt because of a server error must assume it may fetch nothing.", r.Robots.URL, why),
			Fix:    "Serve /robots.txt with HTTP 200, or 404 if you want no rules at all.",
			Agents: fetchingAgentNames(r.Agents), Count: 1, URLs: []string{r.Robots.URL},
		})
	case robots.ModeAllowAll:
		add(Finding{
			ID: "robots_missing", Severity: SeverityInfo, Fidelity: Exact,
			Title:  fmt.Sprintf("No robots.txt (HTTP %d): every agent may fetch every page", p.Status),
			Detail: "Without robots.txt you can't allow some AI agents and refuse others.",
			Fix:    "Add /robots.txt with a group for each agent you want to treat differently.",
			Count:  1, URLs: []string{r.Robots.URL},
		})
	case robots.ModeParsed:
		f := p.File
		if f == nil {
			return
		}
		if len(f.Issues) > 0 {
			var lines []string
			sev := SeverityInfo
			for _, is := range f.Issues {
				lines = append(lines, fmt.Sprintf("line %d: %s", is.Line, is.Message))
				if !is.Minor {
					sev = SeverityWarning
				}
			}
			add(Finding{
				ID: "robots_syntax", Severity: sev, Fidelity: Exact,
				Title:  fmt.Sprintf("robots.txt has %s crawlers will ignore", plural(len(f.Issues), "line", "lines")),
				Detail: strings.Join(lines, "\n"),
				Fix:    "Fix or remove these lines. Crawlers skip what they can't parse, so the rule you meant may not be in effect.",
				Count:  len(f.Issues), URLs: []string{r.Robots.URL},
			})
		}
		var signals []string
		for _, g := range f.Groups {
			for _, cs := range g.ContentSignals {
				signals = append(signals, fmt.Sprintf("User-agent: %s → %s", strings.Join(g.Agents, ", "), cs))
			}
		}
		if len(signals) > 0 || len(f.Licenses) > 0 {
			detail := ""
			if len(signals) > 0 {
				detail = "Content-Signal lines (Cloudflare's Content Signals Policy) say how content may be used after it's fetched, for example ai-train=no. They don't block fetching.\n" + strings.Join(signals, "\n")
			}
			if len(f.Licenses) > 0 {
				detail += "\nLicense lines (Really Simple Licensing) point to the terms for using this content: " + strings.Join(f.Licenses, ", ")
			}
			detail += "\nThe agent registry doesn't record which operators honour these, so they don't change any verdict in this report."
			add(Finding{
				ID: "robots_usage_signals", Severity: SeverityInfo, Fidelity: Exact,
				Title:  "robots.txt declares content-usage preferences",
				Detail: strings.TrimSpace(detail),
				Count:  len(signals) + len(f.Licenses), URLs: []string{r.Robots.URL},
			})
		}
	}
}

// agentRobotsFindings reports which agents robots.txt keeps out of which
// URLs. Agents with a group of their own get a finding each, because someone
// wrote rules for them on purpose. Agents that fall back to the "*" group
// share one finding, so a routine "Disallow: /admin" doesn't repeat for
// every agent.
func agentRobotsFindings(r *Report, add func(Finding)) {
	if r.Robots.Policy.Mode == robots.ModeDisallowAll {
		return // already reported once, for every agent
	}

	var wildNames, wildURLs []string
	wildSeen := map[string]bool{}
	wildRule, wildWholeSite := "", false

	for _, a := range r.Agents {
		var blocked []string
		var verdict robots.Verdict
		total := 0
		for _, p := range r.Pages {
			for _, acc := range p.Access {
				if acc.Agent != a.Name {
					continue
				}
				total++
				if acc.State == StateBlocked {
					if len(blocked) == 0 {
						verdict = acc.Robots
					}
					blocked = append(blocked, p.URL)
				}
			}
		}
		if len(blocked) == 0 {
			continue
		}
		wholeSite := !r.Robots.Policy.Check(a.Token, "/").Allowed && len(blocked) == total
		scope := fmt.Sprintf("%d of %d URLs found", len(blocked), total)
		if wholeSite {
			scope = "the whole site"
		}

		if verdict.Group == "*" {
			wildNames = append(wildNames, a.Name)
			for _, u := range blocked {
				if !wildSeen[u] {
					wildSeen[u] = true
					wildURLs = append(wildURLs, u)
				}
			}
			if wildRule == "" {
				wildRule = verdict.Explain()
			}
			wildWholeSite = wildWholeSite || wholeSite
			continue
		}
		add(agentRobotsFinding(a, verdict.Explain(), scope, blocked))
	}

	if len(wildNames) == 0 {
		return
	}
	f := Finding{
		ID: "robots_blocks_wildcard", Severity: SeverityInfo, Fidelity: Exact,
		Title: fmt.Sprintf("robots.txt's catch-all group keeps %s out of %s", plural(len(wildNames), "agent", "agents"), plural(len(wildURLs), "URL", "URLs")),
		Detail: fmt.Sprintf("These agents have no group of their own, so the \"User-agent: *\" rules apply to them: %s. First matching rule: %s. Usually deliberate (admin or account pages, say); check that none of these URLs are pages you want quoted.",
			strings.Join(wildNames, ", "), wildRule),
		Fix:    "To treat any of these agents differently, give it its own User-agent group.",
		Agents: wildNames, URLs: wildURLs, Count: len(wildURLs),
	}
	if wildWholeSite {
		f.Severity = SeverityWarning
		f.Title = fmt.Sprintf("robots.txt's catch-all group keeps %s out of the whole site", plural(len(wildNames), "agent", "agents"))
	}
	add(f)
}

// agentRobotsFinding describes robots.txt rules aimed at one agent.
func agentRobotsFinding(a agents.Agent, rule, scope string, blocked []string) Finding {
	f := Finding{
		ID: "robots_blocks_" + strings.ToLower(a.Token), Fidelity: Exact,
		Agents: []string{a.Name}, URLs: blocked, Count: len(blocked),
	}
	switch a.Kind {
	case agents.KindTraining:
		f.Severity = SeverityInfo
		f.Title = fmt.Sprintf("robots.txt keeps %s (training) out of %s", a.Name, scope)
		f.Detail = fmt.Sprintf("%s %s only collects training data; search and user-requested fetches are made by separate agents with their own tokens. First matching rule: %s.", a.Purpose, a.Name, rule)
		f.Fix = "Nothing to do if this is deliberate."
	case agents.KindTokenOnly:
		f.Severity = SeverityInfo
		f.Title = fmt.Sprintf("robots.txt disallows %s for %s", a.Name, scope)
		f.Detail = fmt.Sprintf("%s %s still fetches these pages. First matching rule: %s.", a.Purpose, a.FetchedBy, rule)
		f.Fix = "Nothing to do if this is deliberate."
	default:
		f.Severity = SeverityWarning
		f.Title = fmt.Sprintf("robots.txt keeps %s out of %s", a.Name, scope)
		f.Detail = fmt.Sprintf("%s %s can't fetch these pages for %s's answers. First matching rule: %s.", a.Purpose, a.Name, a.Operator, rule)
		f.Fix = fmt.Sprintf("If you want these pages in %s's answers, allow them in the \"User-agent: %s\" group.", a.Operator, a.Token)
	}
	if a.RobotsTxtNote != "" {
		f.Detail += " Note: " + a.RobotsTxtNote
	}
	return f
}

func directiveFindings(r *Report, add func(Finding)) {
	var noindex, scoped, noai []string
	for _, p := range r.Pages {
		if !p.Fetched {
			continue
		}
		generic := append(append([]string(nil), p.RobotsMeta...), genericXRobots(p.XRobotsTag)...)
		if slices.Contains(generic, "noindex") || slices.Contains(generic, "none") {
			noindex = append(noindex, p.URL)
		}
		if slices.Contains(generic, "noai") || slices.Contains(generic, "noimageai") {
			noai = append(noai, p.URL)
		}
		for _, acc := range p.Access {
			if len(acc.Directives) > 0 && !sameSet(acc.Directives, restricting(generic)) {
				scoped = append(scoped, p.URL)
				break
			}
		}
	}

	if len(noindex) > 0 {
		var honour, unknown []string
		for _, a := range r.Agents {
			switch a.MetaRobots {
			case agents.Honours:
				honour = append(honour, a.Name)
			case agents.Unknown:
				unknown = append(unknown, a.Name)
			}
		}
		add(Finding{
			ID: "noindex", Severity: SeverityWarning, Fidelity: Exact,
			Title: fmt.Sprintf("%s every crawler not to index them", plural(len(noindex), "page tells", "pages tell")),
			Detail: fmt.Sprintf("A noindex meta tag or X-Robots-Tag header applies to all crawlers. Documented to honour it: %s. Undocumented: %s.",
				orNone(honour), orNone(unknown)),
			Fix:    "Remove noindex from pages you want found and quoted.",
			Agents: honour, URLs: noindex, Count: len(noindex),
		})
	}
	if len(scoped) > 0 {
		add(Finding{
			ID: "agent_directives", Severity: SeverityInfo, Fidelity: Exact,
			Title:  fmt.Sprintf("%s some agents different directives from the rest", plural(len(scoped), "page gives", "pages give")),
			Detail: "These pages have meta tags or X-Robots-Tag values addressed to specific crawlers. See the access matrix for which agents they restrict.",
			URLs:   scoped, Count: len(scoped),
		})
	}
	if len(noai) > 0 {
		add(Finding{
			ID: "noai_directives", Severity: SeverityInfo, Fidelity: Exact,
			Title:  fmt.Sprintf("%s the non-standard noai/noimageai directives", plural(len(noai), "page uses", "pages use")),
			Detail: "No agent in the registry documents support for noai or noimageai. To keep content from AI agents, use robots.txt groups for their tokens.",
			URLs:   noai, Count: len(noai),
		})
	}
}

func probeFindings(r *Report, add func(Finding)) {
	byOutcome := map[ProbeOutcome]map[string][]string{} // outcome → agent → URLs
	details := map[ProbeOutcome][]string{}
	inconclusive := 0
	for _, pr := range r.Probes {
		if pr.Inconclusive {
			inconclusive++
			continue
		}
		for _, p := range pr.Agents {
			switch p.Outcome {
			case ProbeBlocked, ProbeChallenged, ProbeDifferent:
				if byOutcome[p.Outcome] == nil {
					byOutcome[p.Outcome] = map[string][]string{}
				}
				byOutcome[p.Outcome][p.Agent] = append(byOutcome[p.Outcome][p.Agent], pr.URL)
				details[p.Outcome] = append(details[p.Outcome], fmt.Sprintf("%s on %s: %s", p.Agent, pr.URL, p.Detail))
			}
		}
	}

	caveat := " These requests came from OpenCrawl's network with the agent's user-agent string. Firewalls that verify bots by IP range, reverse DNS or signature may treat the real agent differently, in either direction. Check your CDN or server logs for requests from the agent's published IP ranges."

	blockedAgents := map[string][]string{}
	for _, o := range []ProbeOutcome{ProbeBlocked, ProbeChallenged} {
		for ag, urls := range byOutcome[o] {
			blockedAgents[ag] = append(blockedAgents[ag], urls...)
		}
	}
	if len(blockedAgents) > 0 {
		names, urls := flatten(blockedAgents)
		add(Finding{
			ID: "ua_blocked", Severity: SeverityWarning, Fidelity: Simulated,
			Title:  fmt.Sprintf("Your site blocks or challenges requests that use %s user agent", plural(len(names), "AI agent's", "AI agents'")),
			Detail: "A browser user agent got the page, but these user agents got an error or a bot challenge:\n" + strings.Join(append(details[ProbeBlocked], details[ProbeChallenged]...), "\n") + "\n" + caveat,
			Fix:    "If you want these agents in, look for user-agent rules or \"block AI bots\" settings in your CDN or firewall. Allowlist verified bots rather than user-agent strings, which anyone can send.",
			Agents: names, URLs: urls, Count: len(urls),
		})
	}
	if diff := byOutcome[ProbeDifferent]; len(diff) > 0 {
		names, urls := flatten(diff)
		add(Finding{
			ID: "ua_different", Severity: SeverityInfo, Fidelity: Simulated,
			Title:  fmt.Sprintf("Requests with %s user agent get noticeably different content", plural(len(names), "AI agent's", "AI agents'")),
			Detail: strings.Join(details[ProbeDifferent], "\n") + "\n" + caveat,
			Agents: names, URLs: urls, Count: len(urls),
		})
	}
	if inconclusive > 0 {
		add(Finding{
			ID: "probe_inconclusive", Severity: SeverityInfo, Fidelity: Simulated,
			Title:  fmt.Sprintf("%s inconclusive: an ordinary browser user agent was blocked too", plural(inconclusive, "probe was", "probes were")),
			Detail: "The site blocks or challenges requests from OpenCrawl's network whatever the user agent, so the probes can't tell how it treats AI agents.",
			Count:  inconclusive,
		})
	}
}

func thinHTMLFinding(r *Report, add func(Finding)) {
	var thin []string
	for _, p := range r.Pages {
		rendered := p.Render != nil && p.Render.Error == ""
		if !rendered && p.Fetched && p.Status >= 200 && p.Status < 300 && isHTML(p.ContentType) &&
			p.WordCount < thinWords && (p.EmptyAppRoot || p.Scripts > 0) {
			thin = append(thin, p.URL)
		}
	}
	if len(thin) == 0 {
		return
	}
	var noJS []string
	for _, a := range r.Agents {
		if a.Fetches() && !a.RendersJS {
			noJS = append(noJS, a.Name)
		}
	}
	add(Finding{
		ID: "thin_raw_html", Severity: SeverityWarning, Fidelity: Heuristic,
		Title: fmt.Sprintf("%s almost no text without JavaScript", plural(len(thin), "page has", "pages have")),
		Detail: fmt.Sprintf("These pages have fewer than %d words in their raw HTML but load scripts, so their content is probably rendered in the browser. Agents that don't run JavaScript (%s) receive only what's in the raw HTML. A render diff will confirm this; until then it's an inference.",
			thinWords, strings.Join(noJS, ", ")),
		Fix:    "Server-render or pre-render the main content of these pages.",
		Agents: noJS, URLs: thin, Count: len(thin),
	})
}

// Pages count as depending on JavaScript when at least this share of the
// rendered words, and at least jsMinWords words, are missing from the raw
// HTML, or when a rendered heading is.
const (
	jsMinShare = 0.25
	jsMinWords = 25
)

func renderFindings(r *Report, add func(Finding)) {
	var dependent, failed, details []string
	missingWords := 0
	for _, p := range r.Pages {
		if p.Render == nil {
			continue
		}
		d := p.Render
		if d.Error != "" {
			failed = append(failed, p.URL)
			continue
		}
		if (d.MissingShare >= jsMinShare && d.MissingWords >= jsMinWords) || len(d.MissingHeadings) > 0 {
			dependent = append(dependent, p.URL)
			missingWords += d.MissingWords
			line := fmt.Sprintf("%s: %d of %d words only appear after JavaScript runs", p.URL, d.MissingWords, d.RenderedWords)
			if len(d.MissingHeadings) > 0 {
				line += fmt.Sprintf("; missing headings: %q", d.MissingHeadings)
			}
			details = append(details, line)
		}
	}
	if len(dependent) > 0 {
		var noJS []string
		for _, a := range r.Agents {
			if a.Fetches() && !a.RendersJS {
				noJS = append(noJS, a.Name)
			}
		}
		add(Finding{
			ID: "js_dependent_content", Severity: SeverityWarning, Fidelity: Exact,
			Title: fmt.Sprintf("%s content that only appears after JavaScript runs", plural(len(dependent), "page has", "pages have")),
			Detail: "Each page was rendered in headless Chrome and compared with its raw HTML. Agents that don't run JavaScript (" +
				strings.Join(noJS, ", ") + ") receive only the raw HTML, so they don't see this content.\n" + strings.Join(details, "\n"),
			Fix:    "Server-render or pre-render this content, so it's in the HTML response.",
			Agents: noJS, URLs: dependent, Count: len(dependent),
		})
	}
	if len(failed) > 0 {
		add(Finding{
			ID: "render_failed", Severity: SeverityInfo, Fidelity: Exact,
			Title:  fmt.Sprintf("%s couldn't be rendered in Chrome", plural(len(failed), "page", "pages")),
			Detail: "These pages timed out or failed in headless Chrome, so they have no render diff.",
			URLs:   failed, Count: len(failed),
		})
	}
}

func crawlFindings(r *Report, add func(Finding)) {
	var failed, skipped []string
	for _, p := range r.Pages {
		switch {
		case !p.Fetched:
			skipped = append(skipped, p.URL)
		case p.Error != "" || p.Status >= 400:
			failed = append(failed, p.URL)
		}
	}
	if len(failed) > 0 {
		add(Finding{
			ID: "fetch_errors", Severity: SeverityWarning, Fidelity: Exact,
			Title:  fmt.Sprintf("%s an error or failed to load", plural(len(failed), "page returned", "pages returned")),
			Detail: "These URLs are linked from your site or listed in a sitemap, but returned HTTP 4xx/5xx or failed outright. Any agent that follows them hits the same error.",
			Fix:    "Fix or redirect the URLs, or remove the links to them.",
			URLs:   failed, Count: len(failed),
		})
	}
	if len(skipped) > 0 {
		add(Finding{
			ID: "audit_disallowed", Severity: SeverityInfo, Fidelity: Exact,
			Title:  fmt.Sprintf("%s fetched because robots.txt disallows the audit's crawler", plural(len(skipped), "URL wasn't", "URLs weren't")),
			Detail: "Their robots.txt verdicts are still reported for every agent, but meta tags, headers and content couldn't be checked. If you own the site, re-run with --ignore-robots.",
			URLs:   skipped, Count: len(skipped),
		})
	}
	if !r.LLMsTxt.Present {
		add(Finding{
			ID: "llms_txt_missing", Severity: SeverityInfo, Fidelity: Exact,
			Title:  "No /llms.txt",
			Detail: "llms.txt is a proposed convention for giving language models a summary of a site. No agent in the registry documents reading it, so treat it as optional.",
			Count:  1, URLs: []string{r.LLMsTxt.URL},
		})
	}
	hasSitemap := false
	for _, s := range r.Sitemaps {
		if s.Error == "" {
			hasSitemap = true
		}
	}
	if !hasSitemap {
		add(Finding{
			ID: "no_sitemap", Severity: SeverityInfo, Fidelity: Exact,
			Title:  "No readable sitemap",
			Detail: "No sitemap was listed in robots.txt or found at /sitemap.xml, so crawlers can only find pages by following links.",
			Fix:    "Publish a sitemap and list it in robots.txt with a Sitemap: line.",
			Count:  1,
		})
	}
}

func summarise(r *Report) Summary {
	s := Summary{Findings: map[string]int{}}
	for _, f := range r.Findings {
		s.Findings[string(f.Severity)]++
	}
	idx := map[string]int{}
	for _, a := range r.Agents {
		idx[a.Name] = len(s.Agents)
		root := r.Robots.Policy.Check(a.Token, "/")
		rootState := "allowed"
		if !root.Allowed {
			rootState = "blocked"
		}
		s.Agents = append(s.Agents, AgentSummary{Agent: a.Name, Kind: string(a.Kind), Root: rootState})
	}
	for _, p := range r.Pages {
		s.PagesFound++
		if p.Fetched {
			s.PagesFetched++
		}
		for _, acc := range p.Access {
			as := &s.Agents[idx[acc.Agent]]
			switch acc.State {
			case StateAllowed:
				as.Allowed++
			case StateRestricted:
				as.Restricted++
			case StateBlocked:
				as.Blocked++
			}
		}
	}
	// The probe summary is the worst outcome seen for each agent.
	worst := map[string]ProbeOutcome{}
	rank := map[ProbeOutcome]int{ProbeSame: 1, ProbeSkipped: 0, ProbeInconclusive: 2, ProbeDifferent: 3, ProbeError: 4, ProbeChallenged: 5, ProbeBlocked: 6}
	for _, pr := range r.Probes {
		for _, p := range pr.Agents {
			if cur, ok := worst[p.Agent]; !ok || rank[p.Outcome] > rank[cur] {
				worst[p.Agent] = p.Outcome
			}
		}
	}
	for i := range s.Agents {
		s.Agents[i].Probe = string(worst[s.Agents[i].Agent])
	}
	return s
}

func fetchingAgentNames(list []agents.Agent) []string {
	var out []string
	for _, a := range list {
		if a.Fetches() {
			out = append(out, a.Name)
		}
	}
	return out
}

func genericXRobots(values []string) []string {
	var out []string
	for _, v := range values {
		if scope, rest := splitXRobotsScope(v); scope == "" {
			out = append(out, htmlx.SplitDirectives(rest)...)
		}
	}
	return out
}

func sameSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for _, x := range a {
		if !slices.Contains(b, x) {
			return false
		}
	}
	return true
}

// plural formats n with the singular or plural phrase: "1 page has",
// "3 pages have".
func plural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return fmt.Sprintf("%d %s", n, many)
}

func orNone(list []string) string {
	if len(list) == 0 {
		return "none"
	}
	return strings.Join(list, ", ")
}

// flatten turns agent → URLs into sorted agent names and deduplicated URLs.
func flatten(m map[string][]string) ([]string, []string) {
	var names, urls []string
	seen := map[string]bool{}
	for name, us := range m {
		names = append(names, name)
		for _, u := range us {
			if !seen[u] {
				seen[u] = true
				urls = append(urls, u)
			}
		}
	}
	slices.Sort(names)
	slices.Sort(urls)
	return names, urls
}
