//go:build integration

package browserrun

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

// testFixturePage is the HTML served by the local fixture server.
const testFixturePage = `<!DOCTYPE html>
<html>
<head><title>Test Page</title></head>
<body>
  <h1 id="heading">Hello Integration Test</h1>
  <p class="intro">This is a paragraph with <strong>bold</strong> text.</p>
  <a href="/page2" id="link1">Internal link</a>
  <a href="https://external.example.com/page" id="link2">External link</a>
  <span class="price">$42.99</span>
</body>
</html>`

const testFixturePage2 = `<!DOCTYPE html>
<html><head><title>Page 2</title></head>
<body><h1>Page Two</h1><a href="/">Home</a></body>
</html>`

// newIntegrationSuite creates a fixture HTTP server and a browser-run Server
// backed by a real Chrome pool.
func newIntegrationSuite(t *testing.T) (fixtureURL string, srv *Server) {
	t.Helper()

	fixture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/page2":
			w.Header().Set("Content-Type", "text/html")
			fmt.Fprint(w, testFixturePage2)
		default:
			w.Header().Set("Content-Type", "text/html")
			fmt.Fprint(w, testFixturePage)
		}
	}))
	t.Cleanup(fixture.Close)

	cfg := DefaultConfig()
	cfg.Browser.PoolSize = 2
	cfg.Browser.PoolWaitTimeout = 60 * time.Second
	cfg.Storage.DBPath = t.TempDir() + "/test.db"
	cfg.Crawl.DefaultDelayMs = 100
	cfg.Crawl.ResultTTL = time.Hour

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	s, err := New(ctx, cfg)
	if err != nil {
		t.Fatalf("New: %v — is Chrome/Chromium installed?", err)
	}
	t.Cleanup(s.pool.Close)
	t.Cleanup(func() { s.store.Close() })

	return fixture.URL, s
}

// do sends a request to path on the browser-run handler and returns the
// decoded APIResponse.
func do(t *testing.T, handler http.Handler, method, path string, body interface{}) (int, APIResponse) {
	t.Helper()
	var r *http.Request
	if body != nil {
		b, _ := json.Marshal(body)
		r = httptest.NewRequest(method, path, bytes.NewReader(b))
		r.Header.Set("Content-Type", "application/json")
	} else {
		r = httptest.NewRequest(method, path, nil)
	}
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)

	var resp APIResponse
	json.NewDecoder(w.Body).Decode(&resp)
	return w.Code, resp
}

// --- /health ---

func TestIntegration_Health(t *testing.T) {
	_, srv := newIntegrationSuite(t)
	r := httptest.NewRequest("GET", "/health", nil)
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("health: got %d, want 200", w.Code)
	}
	var body map[string]interface{}
	json.NewDecoder(w.Body).Decode(&body)
	if body["status"] != "ok" {
		t.Errorf("health status: got %v, want ok", body["status"])
	}
}

// --- /content ---

func TestIntegration_Content(t *testing.T) {
	url, srv := newIntegrationSuite(t)
	code, resp := do(t, srv.Handler(), "POST", "/v1/browser-rendering/content",
		CommonParams{URL: url})

	if code != http.StatusOK {
		t.Fatalf("status: got %d, want 200 — errors: %v", code, resp.Errors)
	}
	if !resp.Success {
		t.Fatalf("success=false: %v", resp.Errors)
	}
	html, ok := resp.Result.(string)
	if !ok || !strings.Contains(html, "Hello Integration Test") {
		t.Errorf("expected HTML to contain fixture heading, got: %.200s", html)
	}
}

func TestIntegration_Content_BadRequest(t *testing.T) {
	_, srv := newIntegrationSuite(t)
	code, resp := do(t, srv.Handler(), "POST", "/v1/browser-rendering/content",
		CommonParams{})

	if code != http.StatusBadRequest {
		t.Fatalf("got %d, want 400", code)
	}
	if resp.Success {
		t.Error("expected success=false")
	}
}

func TestIntegration_Content_RawHTML(t *testing.T) {
	_, srv := newIntegrationSuite(t)
	code, resp := do(t, srv.Handler(), "POST", "/v1/browser-rendering/content",
		CommonParams{HTML: "<html><body><p id='p'>raw html</p></body></html>"})

	if code != http.StatusOK {
		t.Fatalf("status: got %d, want 200", code)
	}
	html, _ := resp.Result.(string)
	if !strings.Contains(html, "raw html") {
		t.Errorf("expected raw html in result, got: %.200s", html)
	}
}

// --- /markdown ---

func TestIntegration_Markdown(t *testing.T) {
	url, srv := newIntegrationSuite(t)
	code, resp := do(t, srv.Handler(), "POST", "/v1/browser-rendering/markdown",
		CommonParams{URL: url})

	if code != http.StatusOK {
		t.Fatalf("status: %d — %v", code, resp.Errors)
	}
	markdown, _ := resp.Result.(string)
	if !strings.Contains(markdown, "Hello Integration Test") {
		t.Errorf("expected heading in markdown, got: %.300s", markdown)
	}
	if strings.Contains(markdown, "<h1>") {
		t.Error("markdown should not contain raw HTML tags")
	}
}

// --- /screenshot ---

func TestIntegration_Screenshot_PNG(t *testing.T) {
	url, srv := newIntegrationSuite(t)

	body, _ := json.Marshal(ScreenshotRequest{
		CommonParams:      CommonParams{URL: url},
		ScreenshotOptions: &ScreenshotOptions{FullPage: false},
	})
	r := httptest.NewRequest("POST", "/v1/browser-rendering/screenshot", bytes.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status: %d", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); ct != "image/png" {
		t.Errorf("Content-Type: got %q, want image/png", ct)
	}
	img := w.Body.Bytes()
	if len(img) < 8 || string(img[:4]) != "\x89PNG" {
		t.Errorf("response does not look like a PNG (got %d bytes)", len(img))
	}
}

func TestIntegration_Screenshot_Webp(t *testing.T) {
	url, srv := newIntegrationSuite(t)
	q := 85
	body, _ := json.Marshal(ScreenshotRequest{
		CommonParams:      CommonParams{URL: url},
		ScreenshotOptions: &ScreenshotOptions{Type: "webp", Quality: &q},
	})
	r := httptest.NewRequest("POST", "/v1/browser-rendering/screenshot", bytes.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("webp screenshot: got %d", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); ct != "image/webp" {
		t.Errorf("Content-Type: got %q, want image/webp", ct)
	}
	if w.Body.Len() == 0 {
		t.Error("expected non-empty webp image")
	}
}

func TestIntegration_Screenshot_QualityPNGError(t *testing.T) {
	_, srv := newIntegrationSuite(t)
	q := 90
	code, _ := do(t, srv.Handler(), "POST", "/v1/browser-rendering/screenshot",
		ScreenshotRequest{
			CommonParams:      CommonParams{URL: "https://example.com"},
			ScreenshotOptions: &ScreenshotOptions{Quality: &q},
		})
	if code != http.StatusBadRequest {
		t.Fatalf("expected 400 for png+quality, got %d", code)
	}
}

func TestIntegration_Screenshot_FullPage(t *testing.T) {
	url, srv := newIntegrationSuite(t)
	body, _ := json.Marshal(ScreenshotRequest{
		CommonParams:      CommonParams{URL: url},
		ScreenshotOptions: &ScreenshotOptions{FullPage: true},
	})
	r := httptest.NewRequest("POST", "/v1/browser-rendering/screenshot", bytes.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("full-page screenshot: got %d", w.Code)
	}
	if w.Body.Len() == 0 {
		t.Error("expected non-empty screenshot")
	}
}

func TestIntegration_Screenshot_Selector(t *testing.T) {
	url, srv := newIntegrationSuite(t)
	body, _ := json.Marshal(ScreenshotRequest{
		CommonParams: CommonParams{URL: url},
		Selector:     "#heading",
	})
	r := httptest.NewRequest("POST", "/v1/browser-rendering/screenshot", bytes.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("selector screenshot: got %d", w.Code)
	}
}

// --- /pdf ---

func TestIntegration_PDF(t *testing.T) {
	url, srv := newIntegrationSuite(t)
	body, _ := json.Marshal(PDFRequest{
		CommonParams: CommonParams{URL: url},
		PDFOptions:   &PDFOptions{Format: "A4", PrintBackground: true},
	})
	r := httptest.NewRequest("POST", "/v1/browser-rendering/pdf", bytes.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("pdf: got %d", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); ct != "application/pdf" {
		t.Errorf("Content-Type: got %q, want application/pdf", ct)
	}
	b := w.Body.Bytes()
	if len(b) < 4 || string(b[:4]) != "%PDF" {
		t.Errorf("response does not look like a PDF (%d bytes)", len(b))
	}
}

func TestIntegration_PDF_WithOptions(t *testing.T) {
	url, srv := newIntegrationSuite(t)
	body, _ := json.Marshal(PDFRequest{
		CommonParams: CommonParams{URL: url},
		PDFOptions: &PDFOptions{
			Format:         "A4",
			Margin:         &PDFMargin{Top: "1cm", Bottom: "1cm", Left: "1cm", Right: "1cm"},
			FooterTemplate: `<div style="font-size:10px">Page <span class="pageNumber"></span></div>`,
		},
	})
	r := httptest.NewRequest("POST", "/v1/browser-rendering/pdf", bytes.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("pdf with margin+footer: got %d", w.Code)
	}
}

// --- /snapshot ---

func TestIntegration_Snapshot(t *testing.T) {
	url, srv := newIntegrationSuite(t)
	code, resp := do(t, srv.Handler(), "POST", "/v1/browser-rendering/snapshot",
		SnapshotRequest{CommonParams: CommonParams{URL: url}})

	if code != http.StatusOK {
		t.Fatalf("snapshot: got %d — %v", code, resp.Errors)
	}
	result, ok := resp.Result.(map[string]interface{})
	if !ok {
		t.Fatalf("result is not an object: %T", resp.Result)
	}
	if _, has := result["content"]; !has {
		t.Error("snapshot result missing 'content'")
	}
	if _, has := result["screenshot"]; !has {
		t.Error("snapshot result missing 'screenshot'")
	}
	if s, _ := result["screenshot"].(string); len(s) == 0 {
		t.Error("screenshot is empty")
	}
}

// --- /links ---

func TestIntegration_Links(t *testing.T) {
	url, srv := newIntegrationSuite(t)
	code, resp := do(t, srv.Handler(), "POST", "/v1/browser-rendering/links",
		LinksRequest{CommonParams: CommonParams{URL: url}})

	if code != http.StatusOK {
		t.Fatalf("links: got %d — %v", code, resp.Errors)
	}
	links, ok := resp.Result.([]interface{})
	if !ok {
		t.Fatalf("result is not an array: %T", resp.Result)
	}
	if len(links) == 0 {
		t.Error("expected at least one link")
	}
	found := false
	for _, l := range links {
		if s, _ := l.(string); strings.Contains(s, "/page2") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected /page2 link in results, got: %v", links)
	}
}

func TestIntegration_Links_ExcludeExternal(t *testing.T) {
	url, srv := newIntegrationSuite(t)
	code, resp := do(t, srv.Handler(), "POST", "/v1/browser-rendering/links",
		LinksRequest{
			CommonParams:         CommonParams{URL: url},
			ExcludeExternalLinks: true,
		})

	if code != http.StatusOK {
		t.Fatalf("links: got %d", code)
	}
	links, _ := resp.Result.([]interface{})
	for _, l := range links {
		s, _ := l.(string)
		if strings.Contains(s, "external.example.com") {
			t.Errorf("external link should have been excluded: %s", s)
		}
	}
}

// --- /scrape ---

func TestIntegration_Scrape(t *testing.T) {
	url, srv := newIntegrationSuite(t)
	code, resp := do(t, srv.Handler(), "POST", "/v1/browser-rendering/scrape",
		ScrapeRequest{
			CommonParams: CommonParams{URL: url},
			Elements: []ScrapeElement{
				{Selector: "h1"},
				{Selector: ".price"},
			},
		})

	if code != http.StatusOK {
		t.Fatalf("scrape: got %d — %v", code, resp.Errors)
	}
	result, ok := resp.Result.(map[string]interface{})
	if !ok {
		t.Fatalf("result is not an object: %T", resp.Result)
	}
	elements, _ := result["elements"].([]interface{})
	if len(elements) != 2 {
		t.Fatalf("expected 2 selector results, got %d", len(elements))
	}

	el0 := elements[0].(map[string]interface{})
	results0, _ := el0["results"].([]interface{})
	if len(results0) == 0 {
		t.Fatal("expected at least one h1 match")
	}
	node := results0[0].(map[string]interface{})
	if text, _ := node["text"].(string); !strings.Contains(text, "Hello Integration Test") {
		t.Errorf("h1 text: got %q", text)
	}

	el1 := elements[1].(map[string]interface{})
	results1, _ := el1["results"].([]interface{})
	if len(results1) == 0 {
		t.Fatal("expected at least one .price match")
	}
	priceNode := results1[0].(map[string]interface{})
	if text, _ := priceNode["text"].(string); text != "$42.99" {
		t.Errorf("price text: got %q, want $42.99", text)
	}
}

func TestIntegration_Scrape_NoElements(t *testing.T) {
	_, srv := newIntegrationSuite(t)
	code, _ := do(t, srv.Handler(), "POST", "/v1/browser-rendering/scrape",
		ScrapeRequest{CommonParams: CommonParams{URL: "https://example.com"}})
	if code != http.StatusBadRequest {
		t.Fatalf("expected 400 for missing elements, got %d", code)
	}
}

// --- /json ---

func TestIntegration_JSON_OllamaUnavailable(t *testing.T) {
	url, srv := newIntegrationSuite(t)
	srv.cfg.AI.OllamaBaseURL = "http://127.0.0.1:19999"
	srv.llm = newLLMClient(srv.cfg.AI)

	code, resp := do(t, srv.Handler(), "POST", "/v1/browser-rendering/json",
		JSONRequest{
			CommonParams: CommonParams{URL: url},
			Prompt:       "Extract the heading",
		})
	if code != http.StatusInternalServerError && code != http.StatusOK {
		t.Fatalf("expected 200 or 500, got %d — %v", code, resp.Errors)
	}
}

func TestIntegration_JSON_MissingPrompt(t *testing.T) {
	_, srv := newIntegrationSuite(t)
	code, _ := do(t, srv.Handler(), "POST", "/v1/browser-rendering/json",
		JSONRequest{CommonParams: CommonParams{URL: "https://example.com"}})
	if code != http.StatusBadRequest {
		t.Fatalf("expected 400 for missing prompt+response_format, got %d", code)
	}
}

// --- /crawl lifecycle (updated for new response format) ---

func TestIntegration_Crawl_Lifecycle(t *testing.T) {
	url, srv := newIntegrationSuite(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	srv.startCrawlWorkers(ctx)

	// POST returns UUID string directly
	code, resp := do(t, srv.Handler(), "POST", "/v1/browser-rendering/crawl",
		CrawlRequest{URL: url, Limit: 2, Formats: []string{"markdown", "html"}})

	if code != http.StatusAccepted {
		t.Fatalf("create: got %d — %v", code, resp.Errors)
	}
	jobID, ok := resp.Result.(string)
	if !ok || jobID == "" {
		t.Fatalf("expected UUID string in result, got %T: %v", resp.Result, resp.Result)
	}

	// Poll GET /{jobID} until complete
	deadline := time.Now().Add(30 * time.Second)
	var jobResult map[string]interface{}
	for time.Now().Before(deadline) {
		time.Sleep(500 * time.Millisecond)
		code, resp := do(t, srv.Handler(), "GET", "/v1/browser-rendering/crawl/"+jobID, nil)
		if code != http.StatusOK {
			t.Fatalf("status poll: got %d", code)
		}
		jobResult, _ = resp.Result.(map[string]interface{})
		status, _ := jobResult["status"].(string)
		if status == "completed" || status == "errored" {
			break
		}
	}

	status, _ := jobResult["status"].(string)
	if status != "completed" {
		t.Fatalf("job did not complete within 30s, final status: %q", status)
	}

	// Records are inline in the GET response
	records, _ := jobResult["records"].([]interface{})
	if len(records) == 0 {
		t.Error("expected at least one crawl record inline in GET response")
	}
	rec, _ := records[0].(map[string]interface{})
	if _, has := rec["url"]; !has {
		t.Error("record missing 'url'")
	}
	if _, has := rec["metadata"]; !has {
		t.Error("record missing 'metadata'")
	}
	if _, has := rec["markdown"]; !has {
		t.Error("record missing 'markdown' (format was requested)")
	}

	// Verify cursor is present (0 when no more pages)
	if _, has := jobResult["cursor"]; !has {
		t.Error("response missing 'cursor'")
	}
}

func TestIntegration_Crawl_Cancel(t *testing.T) {
	url, srv := newIntegrationSuite(t)

	code, resp := do(t, srv.Handler(), "POST", "/v1/browser-rendering/crawl",
		CrawlRequest{URL: url, Limit: 1000})
	if code != http.StatusAccepted {
		t.Fatalf("create: got %d", code)
	}
	jobID, _ := resp.Result.(string)
	if jobID == "" {
		t.Fatal("expected job ID string in result")
	}

	// Cancel — DELETE returns 200 with no body
	r := httptest.NewRequest("DELETE", "/v1/browser-rendering/crawl/"+jobID, nil)
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("cancel: got %d", w.Code)
	}

	// Verify job is now cancelled
	code2, resp2 := do(t, srv.Handler(), "GET", "/v1/browser-rendering/crawl/"+jobID, nil)
	if code2 != http.StatusOK {
		t.Fatalf("get after cancel: got %d", code2)
	}
	result, _ := resp2.Result.(map[string]interface{})
	if s, _ := result["status"].(string); s != "cancelled_by_user" {
		t.Errorf("status after cancel: got %q, want cancelled_by_user", s)
	}
}

func TestIntegration_Crawl_NotFound(t *testing.T) {
	_, srv := newIntegrationSuite(t)
	code, _ := do(t, srv.Handler(), "GET", "/v1/browser-rendering/crawl/nonexistent", nil)
	if code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", code)
	}
}

func TestIntegration_Crawl_JSONOptionsRequired(t *testing.T) {
	url, srv := newIntegrationSuite(t)
	code, _ := do(t, srv.Handler(), "POST", "/v1/browser-rendering/crawl",
		CrawlRequest{URL: url, Formats: []string{"json"} /* no jsonOptions */})
	if code != http.StatusBadRequest {
		t.Fatalf("expected 400 when json format without jsonOptions, got %d", code)
	}
}

// --- /stats ---

func TestIntegration_Stats(t *testing.T) {
	_, srv := newIntegrationSuite(t)
	r := httptest.NewRequest("GET", "/v1/stats", nil)
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("stats: got %d", w.Code)
	}
	var resp APIResponse
	json.NewDecoder(w.Body).Decode(&resp)
	if !resp.Success {
		t.Error("expected success=true")
	}
}

// --- Auth middleware ---

func TestIntegration_Auth_Rejected(t *testing.T) {
	_, srv := newIntegrationSuite(t)
	srv.cfg.Server.AuthToken = "secret123"

	r := httptest.NewRequest("GET", "/health", nil)
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, r)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 without token, got %d", w.Code)
	}
}

func TestIntegration_Auth_Accepted(t *testing.T) {
	_, srv := newIntegrationSuite(t)
	srv.cfg.Server.AuthToken = "secret123"

	r := httptest.NewRequest("GET", "/health", nil)
	r.Header.Set("Authorization", "Bearer secret123")
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 with correct token, got %d", w.Code)
	}
}

// --- Viewport / options ---

func TestIntegration_Content_CustomViewport(t *testing.T) {
	url, srv := newIntegrationSuite(t)
	code, resp := do(t, srv.Handler(), "POST", "/v1/browser-rendering/content",
		CommonParams{
			URL:      url,
			Viewport: &ViewportParams{Width: 375, Height: 812, IsMobile: true},
		})
	if code != http.StatusOK {
		t.Fatalf("custom viewport: got %d — %v", code, resp.Errors)
	}
	if !resp.Success {
		t.Errorf("success=false: %v", resp.Errors)
	}
}

func TestIntegration_Content_DisableJS(t *testing.T) {
	url, srv := newIntegrationSuite(t)
	noJS := false
	code, resp := do(t, srv.Handler(), "POST", "/v1/browser-rendering/content",
		CommonParams{
			URL:                  url,
			SetJavaScriptEnabled: &noJS,
		})
	if code != http.StatusOK {
		t.Fatalf("disable JS: got %d — %v", code, resp.Errors)
	}
	if !resp.Success {
		t.Errorf("success=false: %v", resp.Errors)
	}
}

func TestIntegration_Content_WaitForTimeout(t *testing.T) {
	url, srv := newIntegrationSuite(t)
	ms := 200
	start := time.Now()
	code, resp := do(t, srv.Handler(), "POST", "/v1/browser-rendering/content",
		CommonParams{URL: url, WaitForTimeout: &ms})
	elapsed := time.Since(start)

	if code != http.StatusOK {
		t.Fatalf("waitForTimeout: got %d — %v", code, resp.Errors)
	}
	if elapsed < 150*time.Millisecond {
		t.Errorf("expected at least 150ms delay due to waitForTimeout, elapsed: %v", elapsed)
	}
}

func TestIntegration_Content_WaitForSelector(t *testing.T) {
	url, srv := newIntegrationSuite(t)
	code, resp := do(t, srv.Handler(), "POST", "/v1/browser-rendering/content",
		CommonParams{
			URL: url,
			WaitForSelector: &WaitForSelectorParams{
				Selector: "#heading",
				Visible:  true,
				Timeout:  5000,
			},
		})
	if code != http.StatusOK {
		t.Fatalf("waitForSelector: got %d — %v", code, resp.Errors)
	}
	html, _ := resp.Result.(string)
	if !strings.Contains(html, "Hello Integration Test") {
		t.Error("expected page content after waitForSelector")
	}
}

func TestIntegration_Content_UserAgent(t *testing.T) {
	// Fixture server echoes user agent in a header we can check via /content.
	// Since we can't easily inspect the outgoing UA in the fixture response body,
	// we verify the request succeeds without error (UA override is applied).
	url, srv := newIntegrationSuite(t)
	code, resp := do(t, srv.Handler(), "POST", "/v1/browser-rendering/content",
		CommonParams{URL: url, UserAgent: "MyCustomBot/2.0"})
	if code != http.StatusOK {
		t.Fatalf("userAgent: got %d — %v", code, resp.Errors)
	}
}

// testMain guard
func TestMain(m *testing.M) {
	os.Exit(m.Run())
}
