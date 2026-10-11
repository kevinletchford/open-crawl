package htmlx

import (
	"net/url"
	"reflect"
	"testing"
)

func mustURL(t *testing.T, s string) *url.URL {
	t.Helper()
	u, err := url.Parse(s)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func TestParseServerRenderedPage(t *testing.T) {
	doc := `<!doctype html><html lang="en-GB"><head>
<title> Pricing | Example </title>
<meta name="robots" content="index, follow">
<meta name="GPTBot" content="noindex">
<meta name="viewport" content="width=device-width">
<link rel="canonical" href="/pricing">
<script type="application/ld+json">{"@type":"Product"}</script>
<script src="/app.js"></script>
<style>.x{color:red}</style>
</head><body>
<h1>Plans</h1><h2>Team</h2><h2>Enterprise</h2>
<p>Our team plan costs £10 a month, and it's billed yearly.</p>
<a href="/docs#install">Docs</a>
<a href="/docs">Docs again</a>
<a href="https://other.example/x" rel="nofollow noopener">Elsewhere</a>
<a href="mailto:hi@example.com">Mail</a>
<a href="#top">Top</a>
<script>document.write("not counted")</script>
</body></html>`
	p := Parse(mustURL(t, "https://example.com/pricing?ref=1"), []byte(doc))

	if p.Title != "Pricing | Example" || p.Lang != "en-GB" {
		t.Errorf("title/lang = %q/%q", p.Title, p.Lang)
	}
	if p.Canonical != "https://example.com/pricing" {
		t.Errorf("canonical = %q", p.Canonical)
	}
	if got := p.Directives("robots"); !reflect.DeepEqual(got, []string{"index", "follow"}) {
		t.Errorf("robots directives = %v", got)
	}
	if got := p.Directives("gptbot"); !reflect.DeepEqual(got, []string{"noindex"}) {
		t.Errorf("gptbot directives = %v", got)
	}
	wantLinks := []string{"https://example.com/docs", "https://other.example/x"}
	if !reflect.DeepEqual(p.Links, wantLinks) {
		t.Errorf("links = %v, want %v", p.Links, wantLinks)
	}
	if p.NofollowLinks != 1 {
		t.Errorf("nofollow = %d", p.NofollowLinks)
	}
	if p.Scripts != 2 || p.JSONLD != 1 {
		t.Errorf("scripts/jsonld = %d/%d", p.Scripts, p.JSONLD)
	}
	if p.Headings["h1"] != 1 || p.Headings["h2"] != 2 {
		t.Errorf("headings = %v", p.Headings)
	}
	// "Plans Team Enterprise Our team plan costs £10 a month, and it's billed
	// yearly. Docs Docs again Elsewhere Mail Top" = 20 words.
	if p.WordCount != 20 {
		t.Errorf("word count = %d, want 20", p.WordCount)
	}
	if p.EmptyAppRoot {
		t.Error("server-rendered page flagged as an empty app root")
	}
}

func TestParseClientRenderedShell(t *testing.T) {
	doc := `<html><head><title>App</title><script type="module" src="/main.js"></script></head>
<body><noscript>You need to enable JavaScript to run this app.</noscript><div id="root"></div></body></html>`
	p := Parse(mustURL(t, "https://example.com/"), []byte(doc))
	if !p.EmptyAppRoot {
		t.Error("expected an empty app root")
	}
	// Only the <noscript> message reaches a non-JS crawler.
	if p.WordCount != 9 {
		t.Errorf("word count = %d, want 9", p.WordCount)
	}
}

func TestParseHonoursBaseHref(t *testing.T) {
	doc := `<html><head><base href="https://cdn.example.com/site/"></head><body><a href="page">x</a></body></html>`
	p := Parse(mustURL(t, "https://example.com/a/b"), []byte(doc))
	if len(p.Links) != 1 || p.Links[0] != "https://cdn.example.com/site/page" {
		t.Errorf("links = %v", p.Links)
	}
}
