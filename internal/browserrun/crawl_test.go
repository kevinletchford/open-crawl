package browserrun

import (
	"testing"
)

// --- compileGlobs / globMatch ---

func TestGlobMatch_NoFilters(t *testing.T) {
	if !globMatch("https://example.com/page", nil, nil) {
		t.Error("expected match with no filters")
	}
}

func TestGlobMatch_Include(t *testing.T) {
	res := compileGlobs([]string{"*/blog/*"})
	if !globMatch("https://example.com/blog/post-1", res, nil) {
		t.Error("expected /blog/post-1 to match include pattern */blog/*")
	}
	if globMatch("https://example.com/about", res, nil) {
		t.Error("expected /about to NOT match include pattern */blog/*")
	}
}

func TestGlobMatch_Exclude(t *testing.T) {
	res := compileGlobs([]string{"*/admin/*"})
	if globMatch("https://example.com/admin/users", nil, res) {
		t.Error("expected /admin/users to be excluded")
	}
	if !globMatch("https://example.com/public", nil, res) {
		t.Error("expected /public to pass exclude filter")
	}
}

func TestGlobMatch_IncludeAndExclude(t *testing.T) {
	include := compileGlobs([]string{"*/docs/*"})
	exclude := compileGlobs([]string{"*/docs/internal/*"})

	if !globMatch("https://example.com/docs/guide", include, exclude) {
		t.Error("expected /docs/guide to match")
	}
	if globMatch("https://example.com/docs/internal/secret", include, exclude) {
		t.Error("expected /docs/internal/secret to be excluded")
	}
	if globMatch("https://example.com/blog", include, exclude) {
		t.Error("expected /blog to be excluded by include filter")
	}
}

func TestGlobMatch_Wildcard(t *testing.T) {
	excl := compileGlobs([]string{"*.pdf"})
	if globMatch("https://example.com/file.pdf", nil, excl) {
		t.Error(".pdf URL should be blocked by *.pdf exclude pattern")
	}
	if !globMatch("https://example.com/file.html", nil, excl) {
		t.Error(".html URL should pass the *.pdf exclude pattern")
	}
}

// --- crawlResolve (domain-filtering version) ---

func TestCrawlResolve_AbsoluteURL(t *testing.T) {
	got := crawlResolve("https://example.com/page", "https://example.com/", "https://example.com")
	if got != "https://example.com/page" {
		t.Errorf("got %q, want https://example.com/page", got)
	}
}

func TestCrawlResolve_RelativeURL(t *testing.T) {
	got := crawlResolve("/about", "https://example.com/blog/", "https://example.com")
	if got != "https://example.com/about" {
		t.Errorf("got %q, want https://example.com/about", got)
	}
}

func TestCrawlResolve_RelativeFile(t *testing.T) {
	got := crawlResolve("page2.html", "https://example.com/docs/", "https://example.com")
	if got != "https://example.com/docs/page2.html" {
		t.Errorf("got %q, want https://example.com/docs/page2.html", got)
	}
}

func TestCrawlResolve_StripFragment(t *testing.T) {
	got := crawlResolve("https://example.com/page#section", "https://example.com/", "https://example.com")
	if got != "https://example.com/page" {
		t.Errorf("fragment should be stripped, got %q", got)
	}
}

func TestCrawlResolve_DifferentDomain(t *testing.T) {
	got := crawlResolve("https://other.com/page", "https://example.com/", "https://example.com")
	if got != "" {
		t.Errorf("cross-domain URL should return empty, got %q", got)
	}
}

func TestCrawlResolve_Empty(t *testing.T) {
	got := crawlResolve("", "https://example.com/", "https://example.com")
	if got != "" {
		t.Errorf("empty link should return empty, got %q", got)
	}
}

func TestCrawlResolve_HashOnly(t *testing.T) {
	got := crawlResolve("#anchor", "https://example.com/page", "https://example.com")
	if got != "" {
		t.Errorf("hash-only link should return empty, got %q", got)
	}
}

// --- resolveLink (no domain filtering) ---

func TestResolveLink_CrossDomain(t *testing.T) {
	got := resolveLink("https://other.com/page", "https://example.com/")
	if got != "https://other.com/page" {
		t.Errorf("cross-domain should be allowed by resolveLink, got %q", got)
	}
}

func TestResolveLink_HashOnly(t *testing.T) {
	got := resolveLink("#anchor", "https://example.com/page")
	if got != "" {
		t.Errorf("hash-only should return empty, got %q", got)
	}
}

// --- crawlBaseURL ---

func TestCrawlBaseURL(t *testing.T) {
	cases := []struct {
		input, want string
	}{
		{"https://example.com/path/to/page", "https://example.com"},
		{"http://sub.domain.com:8080/foo?bar=1", "http://sub.domain.com:8080"},
	}
	for _, c := range cases {
		got, err := crawlBaseURL(c.input)
		if err != nil {
			t.Errorf("crawlBaseURL(%q): %v", c.input, err)
		}
		if got != c.want {
			t.Errorf("crawlBaseURL(%q) = %q, want %q", c.input, got, c.want)
		}
	}
}

func TestCrawlBaseURL_Invalid(t *testing.T) {
	_, err := crawlBaseURL("://bad url")
	if err == nil {
		t.Error("expected error for invalid URL")
	}
}

// --- isSameDomainOrSubdomain ---

func TestIsSameDomainOrSubdomain(t *testing.T) {
	cases := []struct {
		resolved, seed string
		want           bool
	}{
		{"https://example.com/page", "example.com", true},
		{"https://blog.example.com/post", "example.com", true},
		{"https://deep.sub.example.com/", "example.com", true},
		{"https://other.com/page", "example.com", false},
		{"https://notexample.com/", "example.com", false},
	}
	for _, c := range cases {
		got := isSameDomainOrSubdomain(c.resolved, c.seed)
		if got != c.want {
			t.Errorf("isSameDomainOrSubdomain(%q, %q) = %v, want %v", c.resolved, c.seed, got, c.want)
		}
	}
}

// --- containsStr ---

func TestContainsStr(t *testing.T) {
	if !containsStr([]string{"a", "b", "c"}, "b") {
		t.Error("expected true")
	}
	if containsStr([]string{"a", "b"}, "z") {
		t.Error("expected false")
	}
	if containsStr(nil, "x") {
		t.Error("expected false for nil slice")
	}
}

// --- startsWith ---

func TestStartsWith(t *testing.T) {
	if !startsWith("https://example.com/path", "https://example.com") {
		t.Error("expected true")
	}
	if startsWith("https://other.com", "https://example.com") {
		t.Error("expected false")
	}
	if startsWith("", "prefix") {
		t.Error("expected false for empty string")
	}
}
