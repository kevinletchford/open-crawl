package render

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestRenderRunsJavaScript needs Chrome. It is skipped when Chrome can't be
// started, so `go test` still passes on machines without it.
func TestRenderRunsJavaScript(t *testing.T) {
	if testing.Short() {
		t.Skip("needs Chrome")
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprintf(w, `<html><body><div id="root"></div><p>ua=<span id="ua"></span></p><script>
setTimeout(function () { document.getElementById("root").innerHTML = "<h1>Rendered later</h1>"; }, 300);
document.getElementById("ua").textContent = navigator.userAgent;
</script></body></html>`)
	}))
	defer srv.Close()

	ctx := context.Background()
	pool, err := New(ctx, Options{Settle: 3 * time.Second})
	if err != nil {
		t.Skipf("Chrome unavailable: %v", err)
	}
	defer pool.Close()

	page, err := pool.Render(ctx, srv.URL, "TestAgent/1.0")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(page.HTML, "<h1>Rendered later</h1>") {
		t.Errorf("content added after load is missing:\n%s", page.HTML)
	}
	if !strings.Contains(page.HTML, "ua=<span id=\"ua\">TestAgent/1.0</span>") {
		t.Errorf("user agent override not applied:\n%s", page.HTML)
	}
}
