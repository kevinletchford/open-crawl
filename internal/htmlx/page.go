// Package htmlx extracts what a crawler that doesn't run JavaScript gets
// from a raw HTML document. It is pure: bytes in, facts out.
package htmlx

import (
	"bytes"
	"net/url"
	"strings"
	"unicode"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// Page holds the facts extracted from one HTML document.
type Page struct {
	Title     string `json:"title,omitempty"`
	Lang      string `json:"lang,omitempty"`
	Canonical string `json:"canonical,omitempty"`
	// Meta maps each lower-cased <meta name> to its content values, in
	// document order. Callers look up "robots" and crawler tokens here.
	Meta map[string][]string `json:"-"`
	// Links are absolute http(s) URLs from <a href>, without fragments, in
	// document order, deduplicated. rel=nofollow links are included; see
	// NofollowLinks.
	Links         []string `json:"-"`
	NofollowLinks int      `json:"nofollow_links,omitempty"`
	// WordCount counts words in the text a non-JS crawler receives: the body
	// minus <script>, <style> and <template>. <noscript> content counts,
	// because it is exactly what such a crawler sees.
	WordCount int            `json:"word_count"`
	Headings  map[string]int `json:"headings,omitempty"`
	// HeadingTexts are the h1–h6 texts, whitespace-collapsed, in order.
	HeadingTexts []string `json:"-"`
	// Blocks are the texts of block-level elements (headings, paragraphs,
	// list items, table cells…), whitespace-collapsed, in order. Each holds
	// only the element's own text, not that of blocks nested inside it, so
	// blocks never overlap.
	Blocks []string `json:"-"`
	// Text is all the visible body text, whitespace-collapsed and
	// lower-cased, for containment checks.
	Text    string `json:"-"`
	Scripts int    `json:"scripts"`
	JSONLD  int    `json:"json_ld"`
	// EmptyAppRoot is true when the body holds a single-page-app mount point
	// (#root, #app, #__next, #__nuxt…) with no text in it.
	EmptyAppRoot bool `json:"empty_app_root,omitempty"`
}

var appRootIDs = map[string]bool{
	"root": true, "app": true, "__next": true, "__nuxt": true, "___gatsby": true, "svelte": true, "q-app": true,
}

// Parse extracts facts from body. base is the URL the document was fetched
// from; a <base href> in the document overrides it for link resolution.
func Parse(base *url.URL, body []byte) Page {
	p := Page{Meta: map[string][]string{}, Headings: map[string]int{}}
	doc, err := html.Parse(bytes.NewReader(body))
	if err != nil {
		return p
	}

	if b := findBase(doc); b != "" {
		if u, err := base.Parse(b); err == nil {
			base = u
		}
	}

	seen := map[string]bool{}
	var text strings.Builder

	var walk func(n *html.Node, inBody bool)
	walk = func(n *html.Node, inBody bool) {
		if n.Type == html.ElementNode && inBody && blockElements[n.DataAtom] {
			if t := Collapse(ownText(n)); t != "" {
				p.Blocks = append(p.Blocks, t)
			}
		}
		if n.Type == html.ElementNode {
			switch n.DataAtom {
			case atom.Html:
				p.Lang = attr(n, "lang")
			case atom.Body:
				inBody = true
			case atom.Title:
				if p.Title == "" {
					p.Title = strings.TrimSpace(textOf(n))
				}
				return
			case atom.Meta:
				name := strings.ToLower(strings.TrimSpace(attr(n, "name")))
				if name != "" {
					p.Meta[name] = append(p.Meta[name], attr(n, "content"))
				}
			case atom.Link:
				if hasToken(attr(n, "rel"), "canonical") && p.Canonical == "" {
					if u, err := base.Parse(strings.TrimSpace(attr(n, "href"))); err == nil {
						p.Canonical = u.String()
					}
				}
			case atom.A:
				if u, ok := resolveLink(base, attr(n, "href")); ok && !seen[u] {
					seen[u] = true
					p.Links = append(p.Links, u)
					if hasToken(attr(n, "rel"), "nofollow") {
						p.NofollowLinks++
					}
				}
			case atom.Script:
				if strings.EqualFold(strings.TrimSpace(attr(n, "type")), "application/ld+json") {
					p.JSONLD++
				} else {
					p.Scripts++
				}
				return
			case atom.Style, atom.Template:
				return
			case atom.H1, atom.H2, atom.H3, atom.H4, atom.H5, atom.H6:
				p.Headings[n.Data]++
				if t := Collapse(visibleText(n)); t != "" {
					p.HeadingTexts = append(p.HeadingTexts, t)
				}
			case atom.Div, atom.Main, atom.Section:
				if inBody && appRootIDs[attr(n, "id")] && strings.TrimSpace(visibleText(n)) == "" {
					p.EmptyAppRoot = true
				}
			case atom.Noscript:
				// The html package keeps <noscript> content as raw text when
				// scripting is assumed on, so parse it as markup to count it.
				if inBody {
					text.WriteString(" ")
					text.WriteString(noscriptText(n))
					text.WriteString(" ")
				}
				return
			}
		}
		if n.Type == html.TextNode && inBody {
			text.WriteString(n.Data)
			text.WriteString(" ")
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c, inBody)
		}
	}
	walk(doc, false)

	p.WordCount = countWords(text.String())
	p.Text = strings.ToLower(Collapse(text.String()))
	return p
}

// blockElements hold a unit of text worth comparing between versions of a
// page.
var blockElements = map[atom.Atom]bool{
	atom.H1: true, atom.H2: true, atom.H3: true, atom.H4: true, atom.H5: true, atom.H6: true,
	atom.P: true, atom.Li: true, atom.Td: true, atom.Th: true, atom.Dt: true, atom.Dd: true,
	atom.Blockquote: true, atom.Pre: true, atom.Figcaption: true, atom.Summary: true,
}

// Collapse trims s and replaces each run of whitespace with one space.
func Collapse(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// CountWords counts words the same way WordCount does.
func CountWords(s string) int { return countWords(s) }

// Directives returns the lower-cased robots directives a page gives the meta
// name (such as "robots" or "googlebot").
func (p Page) Directives(name string) []string {
	var out []string
	for _, c := range p.Meta[strings.ToLower(name)] {
		out = append(out, SplitDirectives(c)...)
	}
	return out
}

// SplitDirectives splits a robots meta content or X-Robots-Tag value into
// lower-cased directives.
func SplitDirectives(s string) []string {
	var out []string
	for _, d := range strings.Split(s, ",") {
		if d = strings.ToLower(strings.TrimSpace(d)); d != "" {
			out = append(out, d)
		}
	}
	return out
}

func findBase(n *html.Node) string {
	if n.Type == html.ElementNode && n.DataAtom == atom.Base {
		return attr(n, "href")
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if h := findBase(c); h != "" {
			return h
		}
	}
	return ""
}

func resolveLink(base *url.URL, href string) (string, bool) {
	href = strings.TrimSpace(href)
	if href == "" || strings.HasPrefix(href, "#") {
		return "", false
	}
	u, err := base.Parse(href)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return "", false
	}
	u.Fragment = ""
	u.RawFragment = ""
	return u.String(), true
}

func attr(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if strings.EqualFold(a.Key, key) {
			return a.Val
		}
	}
	return ""
}

func hasToken(list, token string) bool {
	for _, f := range strings.Fields(strings.ToLower(list)) {
		if f == token {
			return true
		}
	}
	return false
}

func textOf(n *html.Node) string {
	var b strings.Builder
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.TextNode {
			b.WriteString(n.Data)
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	return b.String()
}

// visibleText is textOf without script, style and template contents.
func visibleText(n *html.Node) string {
	var b strings.Builder
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode && (n.DataAtom == atom.Script || n.DataAtom == atom.Style || n.DataAtom == atom.Template) {
			return
		}
		if n.Type == html.TextNode {
			b.WriteString(n.Data)
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	return b.String()
}

// ownText is visibleText without the text of nested block elements.
func ownText(n *html.Node) string {
	var b strings.Builder
	var walk func(*html.Node)
	walk = func(c *html.Node) {
		for ; c != nil; c = c.NextSibling {
			if c.Type == html.ElementNode {
				if blockElements[c.DataAtom] || c.DataAtom == atom.Script || c.DataAtom == atom.Style || c.DataAtom == atom.Template {
					continue
				}
				walk(c.FirstChild)
				continue
			}
			if c.Type == html.TextNode {
				b.WriteString(c.Data)
				b.WriteString(" ")
			}
		}
	}
	walk(n.FirstChild)
	return b.String()
}

func noscriptText(n *html.Node) string {
	raw := textOf(n)
	frag, err := html.ParseFragment(strings.NewReader(raw), &html.Node{Type: html.ElementNode, Data: "div", DataAtom: atom.Div})
	if err != nil {
		return raw
	}
	var b strings.Builder
	for _, f := range frag {
		b.WriteString(visibleText(f))
		b.WriteString(" ")
	}
	return b.String()
}

func countWords(s string) int {
	n := 0
	in := false
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsNumber(r) {
			if !in {
				n++
				in = true
			}
		} else if !(r == '\'' || r == '’' || r == '-') || !in {
			in = false
		}
	}
	return n
}
