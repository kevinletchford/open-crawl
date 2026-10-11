package robots

import (
	"errors"
	"testing"
)

func TestCheck(t *testing.T) {
	tests := []struct {
		name    string
		robots  string
		token   string
		path    string
		allowed bool
		group   string
	}{
		// Group selection.
		{"no groups allows all", "Sitemap: https://x/s.xml", "GPTBot", "/a", true, ""},
		{"falls back to *", "User-agent: *\nDisallow: /private", "GPTBot", "/private/x", false, "*"},
		{"own group replaces *", "User-agent: *\nDisallow: /\n\nUser-agent: GPTBot\nAllow: /", "GPTBot", "/a", true, "gptbot"},
		{"own group with no rules allows all", "User-agent: *\nDisallow: /\n\nUser-agent: GPTBot\n", "GPTBot", "/a", true, "gptbot"},
		{"token match ignores case", "User-agent: gptbot\nDisallow: /", "GPTBot", "/", false, "gptbot"},
		{"version suffix is ignored", "User-agent: Googlebot/2.1\nDisallow: /x", "Googlebot", "/x", false, "googlebot"},
		{"prefix of another token does not match", "User-agent: Googlebot-News\nDisallow: /", "Googlebot", "/", true, ""},
		{"groups for the same token merge", "User-agent: ClaudeBot\nDisallow: /a\n\nUser-agent: ClaudeBot\nDisallow: /b", "ClaudeBot", "/b", false, "claudebot"},
		{"consecutive user-agent lines share rules", "User-agent: GPTBot\nUser-agent: ClaudeBot\nDisallow: /", "ClaudeBot", "/", false, "claudebot"},
		{"user-agent after a rule starts a new group", "User-agent: GPTBot\nDisallow: /\nUser-agent: ClaudeBot\nAllow: /", "ClaudeBot", "/x", true, "claudebot"},

		// Precedence.
		{"longest match wins", "User-agent: *\nDisallow: /docs\nAllow: /docs/public", "x", "/docs/public/a", true, "*"},
		{"longest match wins, disallow", "User-agent: *\nAllow: /docs\nDisallow: /docs/private", "x", "/docs/private", false, "*"},
		{"tie goes to allow", "User-agent: *\nDisallow: /page\nAllow: /page", "x", "/page", true, "*"},
		{"empty disallow allows", "User-agent: *\nDisallow:", "x", "/a", true, "*"},

		// Patterns.
		{"prefix match", "User-agent: *\nDisallow: /fish", "x", "/fish.html", false, "*"},
		{"case-sensitive paths", "User-agent: *\nDisallow: /fish", "x", "/Fish", true, "*"},
		{"wildcard", "User-agent: *\nDisallow: /*.php", "x", "/a/b.php?x=1", false, "*"},
		{"anchor", "User-agent: *\nDisallow: /*.php$", "x", "/a/b.php?x=1", true, "*"},
		{"anchor matches end", "User-agent: *\nDisallow: /*.php$", "x", "/a/b.php", false, "*"},
		{"anchor without wildcard", "User-agent: *\nDisallow: /$", "x", "/", false, "*"},
		{"anchor without wildcard, longer path", "User-agent: *\nDisallow: /$", "x", "/a", true, "*"},
		{"query strings count", "User-agent: *\nDisallow: /search?q=", "x", "/search?q=cats", false, "*"},
		{"percent-encoding of unreserved chars is equivalent", "User-agent: *\nDisallow: /%7Ejoe", "x", "/~joe/x", false, "*"},
		{"robots.txt is always allowed", "User-agent: *\nDisallow: /", "x", "/robots.txt", true, ""},

		// Syntax tolerance.
		{"comments and blank lines", "# hi\nUser-agent: * # all\n\nDisallow: /x # no\n", "x", "/x", false, "*"},
		{"CRLF line endings", "User-agent: *\r\nDisallow: /x\r\n", "x", "/x", false, "*"},
		{"BOM", "\xef\xbb\xbfUser-agent: *\nDisallow: /x", "x", "/x", false, "*"},
		{"rules before any group are ignored", "Disallow: /\nUser-agent: *\nAllow: /", "x", "/a", true, "*"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v := Parse([]byte(tt.robots)).Check(tt.token, tt.path)
			if v.Allowed != tt.allowed {
				t.Errorf("allowed = %v, want %v (%s)", v.Allowed, tt.allowed, v.Explain())
			}
			if tt.path != "/robots.txt" && v.Group != tt.group {
				t.Errorf("group = %q, want %q", v.Group, tt.group)
			}
		})
	}
}

func TestParseRecordsIssuesAndSitemaps(t *testing.T) {
	f := Parse([]byte("Disallow: /early\nUser-agent: *\nDisalow: /typo\nDisallow: nope\nCrawl-delay: 2.5\nSitemap: https://example.com/s.xml\n"))
	if len(f.Sitemaps) != 1 || f.Sitemaps[0] != "https://example.com/s.xml" {
		t.Errorf("sitemaps = %v", f.Sitemaps)
	}
	if got := f.CrawlDelay("anything"); got != 2.5 {
		t.Errorf("crawl delay = %v, want 2.5", got)
	}
	lines := map[int]bool{}
	for _, is := range f.Issues {
		lines[is.Line] = true
	}
	for _, want := range []int{1, 3, 4} {
		if !lines[want] {
			t.Errorf("expected an issue on line %d, got %+v", want, f.Issues)
		}
	}
}

func TestParseExtensions(t *testing.T) {
	f := Parse([]byte("User-Agent: *\nContent-Signal: search=yes, ai-train=no\nAllow: /\nHost: example.com\nLicense: https://example.com/license.xml\nNoindex: /x\nFoo: bar\n"))
	if len(f.Groups) != 1 || len(f.Groups[0].ContentSignals) != 1 || f.Groups[0].ContentSignals[0] != "search=yes, ai-train=no" {
		t.Errorf("content signals = %+v", f.Groups)
	}
	if len(f.Licenses) != 1 {
		t.Errorf("licenses = %v", f.Licenses)
	}
	if len(f.Issues) != 2 || f.Issues[0].Line != 6 || f.Issues[0].Minor || f.Issues[1].Line != 7 || !f.Issues[1].Minor {
		t.Errorf("issues = %+v; want Noindex (major) and Foo (minor) only", f.Issues)
	}
}

func TestVerdictReportsRule(t *testing.T) {
	v := Parse([]byte("User-agent: GPTBot\nDisallow: /docs\n")).Check("GPTBot", "/docs/a")
	if v.Rule == nil || v.Rule.Line != 2 || v.Rule.String() != "Disallow: /docs" {
		t.Fatalf("rule = %+v", v.Rule)
	}
	if got, want := v.Explain(), `Disallow: /docs (line 2, group "gptbot")`; got != want {
		t.Errorf("Explain() = %q, want %q", got, want)
	}
}

func TestFromResponse(t *testing.T) {
	tests := []struct {
		status  int
		err     error
		mode    Mode
		allowed bool
	}{
		{200, nil, ModeParsed, false},
		{404, nil, ModeAllowAll, true},
		{403, nil, ModeAllowAll, true},
		{429, nil, ModeDisallowAll, false},
		{503, nil, ModeDisallowAll, false},
		{0, errors.New("dial tcp: refused"), ModeDisallowAll, false},
	}
	for _, tt := range tests {
		p := FromResponse(tt.status, []byte("User-agent: *\nDisallow: /"), tt.err)
		if p.Mode != tt.mode {
			t.Errorf("status %d: mode = %s, want %s", tt.status, p.Mode, tt.mode)
		}
		if v := p.Check("GPTBot", "/a"); v.Allowed != tt.allowed {
			t.Errorf("status %d: allowed = %v, want %v", tt.status, v.Allowed, tt.allowed)
		}
		if v := p.Check("GPTBot", "/robots.txt"); !v.Allowed {
			t.Errorf("status %d: /robots.txt must always be allowed", tt.status)
		}
	}
}
