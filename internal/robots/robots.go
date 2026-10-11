// Package robots parses robots.txt and answers "may this agent fetch this
// path?" following RFC 9309.
//
// It is pure: fetching robots.txt is the caller's job, and FromResponse turns
// the fetch outcome into a Policy using the RFC's status-code rules.
package robots

import (
	"bytes"
	"fmt"
	"strconv"
	"strings"
)

// MaxSize is the number of bytes of a robots.txt file that crawlers must
// parse, per RFC 9309 section 2.5. Callers should truncate to this.
const MaxSize = 500 * 1024

// Rule is one allow or disallow line.
type Rule struct {
	Allow   bool   `json:"allow"`
	Pattern string `json:"pattern"`
	Line    int    `json:"line"`
}

func (r Rule) String() string {
	if r.Allow {
		return "Allow: " + r.Pattern
	}
	return "Disallow: " + r.Pattern
}

// Group is a run of user-agent lines and the rules that follow them.
type Group struct {
	Agents     []string `json:"agents"`
	Rules      []Rule   `json:"rules"`
	CrawlDelay float64  `json:"crawl_delay,omitempty"`
	// ContentSignals are Content-Signal values (such as
	// "search=yes, ai-train=no") from Cloudflare's Content Signals Policy.
	ContentSignals []string `json:"content_signals,omitempty"`
	Line           int      `json:"line"`
}

// Issue is a line the parser skipped or that probably doesn't do what its
// author meant. Minor issues, such as directives some other crawler may
// understand, don't change any verdict.
type Issue struct {
	Line    int    `json:"line"`
	Message string `json:"message"`
	Minor   bool   `json:"minor,omitempty"`
}

// File is a parsed robots.txt.
type File struct {
	Groups   []Group  `json:"groups"`
	Sitemaps []string `json:"sitemaps"`
	// Licenses are License lines (Really Simple Licensing).
	Licenses []string `json:"licenses,omitempty"`
	Issues   []Issue  `json:"issues,omitempty"`
}

// knownExtensions are non-RFC directives in common use. They don't affect
// allow/disallow verdicts and aren't reported as issues.
var knownExtensions = map[string]bool{
	"host": true, "clean-param": true, "request-rate": true, "visit-time": true,
}

// Parse parses a robots.txt body. It never fails: lines it can't use are
// recorded in File.Issues and skipped, as the RFC requires.
func Parse(data []byte) *File {
	data = bytes.TrimPrefix(data, []byte("\xef\xbb\xbf"))
	if len(data) > MaxSize {
		data = data[:MaxSize]
	}

	f := &File{}
	var cur *Group
	sawRule := false // a rule line has been seen since the last user-agent line

	text := strings.NewReplacer("\r\n", "\n", "\r", "\n").Replace(string(data))
	for i, raw := range strings.Split(text, "\n") {
		n := i + 1
		line := raw
		if j := strings.IndexByte(line, '#'); j >= 0 {
			line = line[:j]
		}
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			f.Issues = append(f.Issues, Issue{Line: n, Message: fmt.Sprintf("no colon in %q", raw)})
			continue
		}
		key = strings.ToLower(strings.TrimSpace(key))
		value = strings.TrimSpace(value)

		switch key {
		case "user-agent", "useragent", "user agent":
			if cur == nil || sawRule {
				f.Groups = append(f.Groups, Group{Line: n})
				cur = &f.Groups[len(f.Groups)-1]
				sawRule = false
			}
			cur.Agents = append(cur.Agents, productToken(value))
		case "allow", "disallow":
			if cur == nil {
				f.Issues = append(f.Issues, Issue{Line: n, Message: key + " line before any user-agent line is ignored"})
				continue
			}
			sawRule = true
			if value == "" {
				// An empty pattern matches nothing, so the line has no effect.
				continue
			}
			if value[0] != '/' && value[0] != '*' {
				f.Issues = append(f.Issues, Issue{Line: n, Message: fmt.Sprintf("pattern %q doesn't start with / or *, so it matches no path", value)})
			}
			cur.Rules = append(cur.Rules, Rule{Allow: key == "allow", Pattern: value, Line: n})
		case "sitemap":
			if value != "" {
				f.Sitemaps = append(f.Sitemaps, value)
			}
		case "crawl-delay":
			if cur == nil {
				continue
			}
			if d, err := strconv.ParseFloat(value, 64); err == nil && d >= 0 {
				cur.CrawlDelay = d
			} else {
				f.Issues = append(f.Issues, Issue{Line: n, Message: fmt.Sprintf("crawl-delay %q is not a number", value), Minor: true})
			}
		case "content-signal":
			if cur != nil {
				cur.ContentSignals = append(cur.ContentSignals, value)
			} else {
				f.Issues = append(f.Issues, Issue{Line: n, Message: "content-signal line before any user-agent line applies to no group"})
			}
		case "license":
			if value != "" {
				f.Licenses = append(f.Licenses, value)
			}
		case "noindex", "nofollow":
			f.Issues = append(f.Issues, Issue{Line: n, Message: fmt.Sprintf("%q isn't a robots.txt directive (Google dropped support in 2019); use a meta tag or X-Robots-Tag header", key)})
		default:
			if !knownExtensions[key] {
				f.Issues = append(f.Issues, Issue{Line: n, Message: fmt.Sprintf("unknown directive %q", key), Minor: true})
			}
		}
	}
	return f
}

// productToken reduces a user-agent line's value to the product token that is
// matched against crawler names: the leading run of letters, '-' and '_'.
// "Googlebot/2.1" becomes "googlebot". "*" is kept as is.
func productToken(v string) string {
	if strings.HasPrefix(v, "*") {
		return "*"
	}
	end := 0
	for end < len(v) {
		c := v[end]
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c == '-' || c == '_') {
			break
		}
		end++
	}
	return strings.ToLower(v[:end])
}

// Verdict is the answer for one agent and one path.
type Verdict struct {
	Allowed bool `json:"allowed"`
	// Group is the user-agent the applied rules came from: the agent's own
	// token, "*", or "" when no group applies.
	Group string `json:"group"`
	// Rule is the rule that decided, or nil if no rule matched.
	Rule *Rule `json:"rule,omitempty"`
	// Reason explains verdicts that came from the fetch status rather than
	// from a rule, such as "robots.txt returned 503".
	Reason string `json:"reason,omitempty"`
}

// Explain describes the verdict in one line.
func (v Verdict) Explain() string {
	switch {
	case v.Reason != "":
		return v.Reason
	case v.Rule != nil:
		return fmt.Sprintf("%s (line %d, group %q)", v.Rule, v.Rule.Line, v.Group)
	case v.Group != "":
		return fmt.Sprintf("no rule matches in group %q", v.Group)
	default:
		return "no group applies"
	}
}

// Check decides whether the agent with product token may fetch path. path is
// the URL's escaped path plus any "?query".
func (f *File) Check(token, path string) Verdict {
	if path == "" {
		path = "/"
	}
	if path == "/robots.txt" {
		return Verdict{Allowed: true, Reason: "/robots.txt is always allowed"}
	}

	group, rules := f.rulesFor(strings.ToLower(token))
	if group == "" {
		return Verdict{Allowed: true}
	}

	path = normalize(path)
	var best *Rule
	bestLen := -1
	for i := range rules {
		r := &rules[i]
		p := normalize(r.Pattern)
		if !match(p, path) {
			continue
		}
		// The most specific (longest) match wins; on a tie, allow wins.
		if len(p) > bestLen || len(p) == bestLen && r.Allow && !best.Allow {
			best, bestLen = r, len(p)
		}
	}
	if best == nil {
		return Verdict{Allowed: true, Group: group}
	}
	return Verdict{Allowed: best.Allow, Group: group, Rule: best}
}

// rulesFor merges every group naming token, falling back to every "*" group.
func (f *File) rulesFor(token string) (string, []Rule) {
	collect := func(name string) []Rule {
		var rules []Rule
		found := false
		for _, g := range f.Groups {
			for _, a := range g.Agents {
				if a == name {
					rules = append(rules, g.Rules...)
					found = true
					break
				}
			}
		}
		if !found {
			return nil
		}
		if rules == nil {
			rules = []Rule{}
		}
		return rules
	}
	if rules := collect(token); rules != nil {
		return token, rules
	}
	if rules := collect("*"); rules != nil {
		return "*", rules
	}
	return "", nil
}

// CrawlDelay returns the crawl-delay, in seconds, of the group that applies
// to token, or 0.
func (f *File) CrawlDelay(token string) float64 {
	token = strings.ToLower(token)
	var anyDelay float64
	for _, g := range f.Groups {
		for _, a := range g.Agents {
			if a == token && g.CrawlDelay > 0 {
				return g.CrawlDelay
			}
			if a == "*" && g.CrawlDelay > 0 && anyDelay == 0 {
				anyDelay = g.CrawlDelay
			}
		}
	}
	return anyDelay
}

// NamesToken reports whether any group names token explicitly.
func (f *File) NamesToken(token string) bool {
	token = strings.ToLower(token)
	for _, g := range f.Groups {
		for _, a := range g.Agents {
			if a == token {
				return true
			}
		}
	}
	return false
}

// match reports whether path matches pattern, where '*' matches any run of
// characters and a trailing '$' anchors the end.
func match(pattern, path string) bool {
	anchored := strings.HasSuffix(pattern, "$")
	if anchored {
		pattern = pattern[:len(pattern)-1]
	}
	parts := strings.Split(pattern, "*")
	if !strings.HasPrefix(path, parts[0]) {
		return false
	}
	pos := len(parts[0])
	if len(parts) == 1 {
		return !anchored || pos == len(path)
	}
	for _, part := range parts[1 : len(parts)-1] {
		i := strings.Index(path[pos:], part)
		if i < 0 {
			return false
		}
		pos += i + len(part)
	}
	last := parts[len(parts)-1]
	if anchored {
		return len(path)-len(last) >= pos && strings.HasSuffix(path, last)
	}
	return strings.Contains(path[pos:], last)
}

// normalize puts a path or pattern into a canonical percent-encoded form so
// that "/%7Efoo", "/~foo" and "/%7efoo" compare equal: unreserved characters
// are decoded, other escapes get upper-case hex, and bytes outside printable
// ASCII are escaped.
func normalize(s string) string {
	const hex = "0123456789ABCDEF"
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '%' && i+2 < len(s) && isHex(s[i+1]) && isHex(s[i+2]):
			v := unhex(s[i+1])<<4 | unhex(s[i+2])
			if isUnreserved(v) {
				b.WriteByte(v)
			} else {
				b.WriteByte('%')
				b.WriteByte(hex[v>>4])
				b.WriteByte(hex[v&15])
			}
			i += 2
		case c <= 0x20 || c >= 0x7f:
			b.WriteByte('%')
			b.WriteByte(hex[c>>4])
			b.WriteByte(hex[c&15])
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}

func isHex(c byte) bool {
	return c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F'
}

func unhex(c byte) byte {
	switch {
	case c >= '0' && c <= '9':
		return c - '0'
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10
	default:
		return c - 'A' + 10
	}
}

func isUnreserved(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' ||
		c == '-' || c == '.' || c == '_' || c == '~'
}
