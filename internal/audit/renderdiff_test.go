package audit

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/kevinletchford/open-crawl/internal/htmlx"
	"github.com/kevinletchford/open-crawl/internal/render"
)

func parse(t *testing.T, doc string) htmlx.Page {
	t.Helper()
	u, _ := url.Parse("https://example.com/")
	return htmlx.Parse(u, []byte(doc))
}

func TestDiffPagesCountsEachMissingWordOnce(t *testing.T) {
	raw := parse(t, `<html><body><h1>Pricing</h1><p>Plans start at ten pounds.</p></body></html>`)
	rendered := parse(t, `<html><body><h1>Pricing</h1><p>Plans start at ten pounds.</p>
<h2>Compare plans</h2>
<ul><li>Team plan <p>includes five seats and email support</p></li></ul>
<a href="/signup">Sign up</a></body></html>`)

	d := diffPages(raw, rendered)
	// "Compare plans" (2) + "Team plan" (2) + the nested paragraph (6) = 10.
	if d.MissingWords != 10 {
		t.Errorf("missing words = %d, want 10 (blocks %q)", d.MissingWords, d.MissingBlocks)
	}
	if len(d.MissingHeadings) != 1 || d.MissingHeadings[0] != "Compare plans" {
		t.Errorf("missing headings = %v", d.MissingHeadings)
	}
	if d.MissingLinks != 1 {
		t.Errorf("missing links = %d", d.MissingLinks)
	}
	if d.MissingShare <= 0 || d.MissingShare > 1 {
		t.Errorf("share = %v", d.MissingShare)
	}

	same := diffPages(raw, raw)
	if same.MissingWords != 0 || len(same.MissingHeadings) != 0 || same.MissingLinks != 0 {
		t.Errorf("identical pages differ: %+v", same)
	}
}

// fakeRenderer returns what a browser would show: the raw HTML, except for
// pages listed in overrides.
type fakeRenderer struct {
	overrides map[string]string // path → rendered HTML
	fail      map[string]bool
}

func (f fakeRenderer) Render(ctx context.Context, u, ua string) (render.Page, error) {
	pu, _ := url.Parse(u)
	if f.fail[pu.Path] {
		return render.Page{}, context.DeadlineExceeded
	}
	if html, ok := f.overrides[pu.Path]; ok {
		return render.Page{URL: u, FinalURL: u, HTML: html}, nil
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	req.Header.Set("User-Agent", ua)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return render.Page{}, err
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return render.Page{URL: u, FinalURL: u, HTML: string(b)}, nil
}

func TestAuditRenderDiff(t *testing.T) {
	srv := fakeSite(t, http.StatusOK)
	words := strings.Repeat("Dashboard numbers that only the browser sees after hydration. ", 8)
	r := runAudit(t, srv, func(c *Config) {
		c.Renderer = fakeRenderer{
			overrides: map[string]string{"/app": `<html><body><div id="root"><h1>Dashboard</h1><p>` + words + `</p></div></body></html>`},
			fail:      map[string]bool{"/hidden": true},
		}
	})

	app := pageByPath(r, "/app")
	if app.Render == nil || app.Render.MissingWords < jsMinWords || len(app.Render.MissingHeadings) != 1 {
		t.Fatalf("/app render diff = %+v", app.Render)
	}
	if about := pageByPath(r, "/about"); about.Render == nil || about.Render.MissingWords != 0 {
		t.Errorf("/about should render identically: %+v", about.Render)
	}

	f := findingByID(r, "js_dependent_content")
	if f == nil || f.Fidelity != Exact || f.Count != 1 || !strings.HasSuffix(f.URLs[0], "/app") {
		t.Errorf("js_dependent_content = %+v", f)
	}
	if findingByID(r, "thin_raw_html") != nil {
		t.Error("a measured render diff should replace the thin-HTML heuristic for the same page")
	}
	if f := findingByID(r, "render_failed"); f == nil || f.Count != 1 {
		t.Errorf("render_failed = %+v", f)
	}
	if r.Settings.RenderPages != 10 {
		t.Errorf("render pages setting = %d", r.Settings.RenderPages)
	}
}
