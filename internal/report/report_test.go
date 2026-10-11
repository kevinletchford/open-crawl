package report

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kevinletchford/open-crawl/internal/audit"
)

func TestRenderRealAudit(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/robots.txt":
			fmt.Fprint(w, "User-agent: GPTBot\nDisallow: /\n\nUser-agent: *\nContent-Signal: ai-train=no\nAllow: /\n")
		case "/":
			w.Header().Set("Content-Type", "text/html")
			fmt.Fprint(w, `<html><head><title>Home</title></head><body><div id="root"></div><script src="/a.js"></script><a href="/b">B</a></body></html>`)
		case "/b":
			w.Header().Set("Content-Type", "text/html")
			w.Header().Set("X-Robots-Tag", "noindex")
			fmt.Fprint(w, `<html><body><p>Page <b>&lt;b&gt;</b></p></body></html>`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	r, err := audit.Run(context.Background(), audit.Config{URL: srv.URL, Delay: time.Millisecond, ProbePages: 1})
	if err != nil {
		t.Fatal(err)
	}

	var html bytes.Buffer
	if err := HTML(&html, r); err != nil {
		t.Fatalf("HTML: %v", err)
	}
	out := html.String()
	for _, want := range []string{"AI access audit", "robots.txt keeps GPTBot", "simulated", "heuristic", "Content-Signal", `class="s-blocked"`, `class="s-restricted"`} {
		if !strings.Contains(out, want) {
			t.Errorf("HTML report is missing %q", want)
		}
	}

	var text bytes.Buffer
	Text(&text, r)
	if !strings.Contains(text.String(), "GPTBot") || !strings.Contains(text.String(), "Findings (") {
		t.Errorf("text summary:\n%s", text.String())
	}
}
