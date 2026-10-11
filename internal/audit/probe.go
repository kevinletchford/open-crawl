package audit

import (
	"bytes"
	"context"
	"fmt"
	"net/url"
	"slices"
	"sync"

	"github.com/kevinletchford/open-crawl/internal/fetch"
)

// challengeMarkers are strings found in bot-challenge and block pages from
// common CDNs and bot-management services. Matched case-insensitively.
var challengeMarkers = [][]byte{
	[]byte("cf-chl-"),
	[]byte("challenge-platform"),
	[]byte("<title>just a moment...</title>"),
	[]byte("attention required! | cloudflare"),
	[]byte("_incapsula_resource"),
	[]byte("captcha-delivery.com"), // DataDome
	[]byte("px-captcha"),           // HUMAN / PerimeterX
	[]byte("access denied</title>"),
	[]byte("g-recaptcha"),
	[]byte("h-captcha"),
}

func isChallenge(body []byte) bool {
	lower := bytes.ToLower(body)
	for _, m := range challengeMarkers {
		if bytes.Contains(lower, m) {
			return true
		}
	}
	return false
}

// probe re-fetches a few pages with a browser user agent and with each
// agent's user agent, and classifies the differences. The pages are the
// start page plus the first other HTML pages that returned 2xx.
func (a *auditor) probe(ctx context.Context) []ProbeResult {
	if a.cfg.ProbePages < 0 {
		return nil
	}
	var targets []string
	for _, p := range a.pages {
		if len(targets) >= a.cfg.ProbePages {
			break
		}
		if p.Fetched && p.Status >= 200 && p.Status < 300 && isHTML(p.ContentType) {
			targets = append(targets, p.URL)
		}
	}

	out := make([]ProbeResult, len(targets))
	for i, t := range targets {
		a.cfg.Progress("probing " + t + " with each agent's user agent")
		out[i] = a.probeOne(ctx, t)
	}
	return out
}

func (a *auditor) probeOne(ctx context.Context, target string) ProbeResult {
	pr := ProbeResult{URL: target}
	u, err := url.Parse(target)
	if err != nil {
		return pr
	}

	base := a.client.Get(ctx, target, fetch.BrowserUserAgent)
	pr.Baseline = Probe{Status: base.Status, Bytes: len(base.Body)}
	switch {
	case base.Err != nil:
		pr.Baseline.Detail = base.Err.Error()
		pr.Inconclusive = true
	case base.Status >= 400 || isChallenge(base.Body):
		pr.Baseline.Detail = "a browser user agent was blocked or challenged too"
		pr.Inconclusive = true
	}

	pr.Agents = make([]Probe, 0, len(a.cfg.Agents))
	var wg sync.WaitGroup
	var mu sync.Mutex
	for _, ag := range a.cfg.Agents {
		if !ag.Fetches() {
			continue
		}
		if v := a.robots.Policy.Check(ag.Token, pathAndQuery(u)); !v.Allowed {
			mu.Lock()
			pr.Agents = append(pr.Agents, Probe{Agent: ag.Name, Outcome: ProbeSkipped,
				Detail: "robots.txt disallows this agent here, so it wasn't impersonated"})
			mu.Unlock()
			continue
		}
		wg.Add(1)
		go func(name, ua string) {
			defer wg.Done()
			res := a.client.Get(ctx, target, ua)
			p := classify(name, base, res, pr.Inconclusive)
			mu.Lock()
			pr.Agents = append(pr.Agents, p)
			mu.Unlock()
		}(ag.Name, ag.UserAgent)
	}
	wg.Wait()

	// Keep registry order regardless of which request finished first.
	order := map[string]int{}
	for i, ag := range a.cfg.Agents {
		order[ag.Name] = i
	}
	slices.SortStableFunc(pr.Agents, func(x, y Probe) int { return order[x.Agent] - order[y.Agent] })
	return pr
}

// classify compares an agent-UA response with the browser-UA baseline.
func classify(agent string, base, res *fetch.Response, inconclusive bool) Probe {
	p := Probe{Agent: agent, Status: res.Status, Bytes: len(res.Body)}
	switch {
	case res.Err != nil:
		p.Outcome, p.Detail = ProbeError, res.Err.Error()
	case inconclusive:
		p.Outcome = ProbeInconclusive
		p.Detail = "a browser user agent was blocked too, so this says nothing about AI agents"
	case res.Status >= 400 && base.Status < 400:
		p.Outcome = ProbeBlocked
		p.Detail = fmt.Sprintf("HTTP %d, where a browser user agent got HTTP %d", res.Status, base.Status)
	case isChallenge(res.Body) && !isChallenge(base.Body):
		p.Outcome = ProbeChallenged
		p.Detail = "served a bot-challenge page instead of the content"
	case len(base.Body) > 2048 && len(res.Body) < len(base.Body)/2:
		p.Outcome = ProbeDifferent
		p.Detail = fmt.Sprintf("%d bytes, where a browser user agent got %d", len(res.Body), len(base.Body))
	case res.FinalURL != base.FinalURL:
		p.Outcome = ProbeDifferent
		p.Detail = "redirected to " + res.FinalURL + ", where a browser user agent ended at " + base.FinalURL
	default:
		p.Outcome = ProbeSame
	}
	return p
}
