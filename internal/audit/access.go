package audit

import (
	"net/url"
	"slices"
	"strings"

	"github.com/kevinletchford/open-crawl/internal/agents"
	"github.com/kevinletchford/open-crawl/internal/htmlx"
	"github.com/kevinletchford/open-crawl/internal/robots"
)

// pageDirectives are the robots directives a fetched page gives, from meta
// tags and X-Robots-Tag headers, split into those for every crawler and
// those addressed to one token.
type pageDirectives struct {
	generic []string
	byToken map[string][]string // lower-cased token → directives
}

// collectDirectives reads directives from a parsed page and its
// X-Robots-Tag header values.
func collectDirectives(page htmlx.Page, xRobots []string, tokens []string) pageDirectives {
	d := pageDirectives{byToken: map[string][]string{}}
	d.generic = append(d.generic, page.Directives("robots")...)
	for _, tok := range tokens {
		tok = strings.ToLower(tok)
		if ds := page.Directives(tok); len(ds) > 0 {
			d.byToken[tok] = append(d.byToken[tok], ds...)
		}
	}
	for _, v := range xRobots {
		scope, rest := splitXRobotsScope(v)
		if scope == "" {
			d.generic = append(d.generic, htmlx.SplitDirectives(rest)...)
		} else {
			d.byToken[scope] = append(d.byToken[scope], htmlx.SplitDirectives(rest)...)
		}
	}
	return d
}

// xRobotsParams are directives whose value follows a colon, so a colon after
// them doesn't introduce a crawler scope.
var xRobotsParams = []string{"unavailable_after", "max-snippet", "max-image-preview", "max-video-preview"}

// splitXRobotsScope splits "googlebot: noindex, nofollow" into
// ("googlebot", "noindex, nofollow"). An unscoped value gets scope "".
func splitXRobotsScope(v string) (scope, rest string) {
	before, after, ok := strings.Cut(v, ":")
	if !ok {
		return "", v
	}
	name := strings.ToLower(strings.TrimSpace(before))
	if name == "" || strings.ContainsAny(name, " ,") || slices.Contains(xRobotsParams, name) {
		return "", v
	}
	return name, after
}

// restricting reports the directives that stop a page from being indexed or
// used: noindex, none, and the non-standard noai and noimageai.
func restricting(ds []string) []string {
	var out []string
	for _, d := range ds {
		switch d {
		case "noindex", "none", "noai", "noimageai":
			if !slices.Contains(out, d) {
				out = append(out, d)
			}
		}
	}
	return out
}

// evaluate decides one agent's access to one URL. d is nil when the page
// wasn't fetched.
func evaluate(a agents.Agent, policy robots.Policy, u *url.URL, d *pageDirectives) AgentAccess {
	acc := AgentAccess{Agent: a.Name, Robots: policy.Check(a.Token, pathAndQuery(u))}

	if !acc.Robots.Allowed {
		acc.State = StateBlocked
		switch a.RobotsTxt {
		case agents.MayIgnore:
			acc.Notes = append(acc.Notes, "The operator says this agent may not apply robots.txt to user-initiated fetches.")
		case agents.Unknown:
			acc.Notes = append(acc.Notes, "The operator hasn't documented whether this agent honours robots.txt.")
		}
		if a.Kind == agents.KindTokenOnly {
			acc.Notes = append(acc.Notes, a.FetchedBy+" may still fetch this URL; the token only controls how the content is used.")
		}
		return acc
	}

	acc.State = StateAllowed
	if d == nil {
		return acc
	}
	acc.DirectivesKnown = true

	if a.MetaRobots == agents.NotSupported {
		if len(restricting(d.generic)) > 0 {
			acc.Notes = append(acc.Notes, "The page has noindex-style directives, but the operator says this agent ignores robots meta tags.")
		}
		return acc
	}

	acc.Directives = restricting(append(append([]string(nil), d.generic...), d.byToken[strings.ToLower(a.Token)]...))
	if len(acc.Directives) > 0 {
		acc.State = StateRestricted
		if a.MetaRobots == agents.Unknown {
			acc.Notes = append(acc.Notes, "The operator hasn't documented whether this agent honours robots meta tags or X-Robots-Tag.")
		}
	}
	return acc
}

// pathAndQuery is what robots.txt rules are matched against.
func pathAndQuery(u *url.URL) string {
	p := u.EscapedPath()
	if p == "" {
		p = "/"
	}
	if u.RawQuery != "" {
		p += "?" + u.RawQuery
	}
	return p
}
