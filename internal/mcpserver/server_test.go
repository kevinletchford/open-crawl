package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/kevinletchford/open-crawl/internal/audit"
	"github.com/kevinletchford/open-crawl/internal/store"
)

// connect starts the server and a client over in-memory transports.
func connect(t *testing.T, s *Server) *mcp.ClientSession {
	t.Helper()
	ctx := context.Background()
	serverT, clientT := mcp.NewInMemoryTransports()
	if _, err := s.MCP().Connect(ctx, serverT, nil); err != nil {
		t.Fatal(err)
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil)
	cs, err := client.Connect(ctx, clientT, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cs.Close() })
	return cs
}

// call invokes a tool and decodes its structured output into out. It fails
// the test on a tool error unless wantErr is set.
func call(t *testing.T, cs *mcp.ClientSession, name string, args map[string]any, out any, wantErr bool) string {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	text := ""
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			text += tc.Text
		}
	}
	if res.IsError != wantErr {
		t.Fatalf("%s: IsError = %v, content %q", name, res.IsError, text)
	}
	if out != nil && !wantErr {
		b, _ := json.Marshal(res.StructuredContent)
		if err := json.Unmarshal(b, out); err != nil {
			t.Fatalf("%s: decoding %s: %v", name, b, err)
		}
	}
	return text
}

// site serves a small site: robots.txt blocks GPTBot, /products/app is a
// client-rendered shell and /products/old has noindex.
func site(t *testing.T) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		words := strings.Repeat("Words a crawler can read. ", 40)
		w.Header().Set("Content-Type", "text/html")
		switch r.URL.Path {
		case "/robots.txt":
			w.Header().Set("Content-Type", "text/plain")
			fmt.Fprint(w, "User-agent: GPTBot\nDisallow: /\n")
		case "/":
			fmt.Fprint(w, `<html><body><h1>Shop</h1><p>`+words+`</p><a href="/products/app">a</a><a href="/products/old">o</a></body></html>`)
		case "/products/app":
			fmt.Fprint(w, `<html><head><script src="/a.js"></script></head><body><div id="root"></div></body></html>`)
		case "/products/old":
			fmt.Fprint(w, `<html><head><meta name="robots" content="noindex"></head><body><p>`+words+`</p></body></html>`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func newServer(t *testing.T) *Server {
	st, err := store.Open(filepath.Join(t.TempDir(), "mcp.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	s := New(st)
	// Use a fast, polite-enough config in tests.
	s.run = func(ctx context.Context, c audit.Config) (*audit.Report, error) {
		c.Delay = time.Millisecond
		c.ProbePages = -1
		return audit.Run(ctx, c)
	}
	return s
}

func TestToolsEndToEnd(t *testing.T) {
	s := newServer(t)
	cs := connect(t, s)
	srv := site(t)
	host := strings.TrimPrefix(srv.URL, "http://")

	// Nothing saved yet.
	text := call(t, cs, "audit_summary", map[string]any{}, nil, true)
	if !strings.Contains(text, "run_audit") {
		t.Errorf("empty-store error should point to run_audit: %q", text)
	}

	var sum SummaryOut
	call(t, cs, "run_audit", map[string]any{"url": srv.URL, "max_pages": 500}, &sum, false)
	if sum.AuditID != 1 || sum.Host != host || sum.PagesFetched != 3 || sum.Score == nil {
		t.Fatalf("run_audit = %+v", sum)
	}
	if !hasFinding(sum, "thin_raw_html", "heuristic") || !hasFinding(sum, "noindex", "exact") {
		t.Errorf("findings = %+v", sum.Findings)
	}

	var list ListAuditsOut
	call(t, cs, "list_audits", map[string]any{"host": "http://" + host + "/"}, &list, false)
	if len(list.Audits) != 1 {
		t.Errorf("list_audits = %+v", list)
	}

	// "Which product pages can't ClaudeBot read?" — restricted by noindex.
	var acc QueryAccessOut
	call(t, cs, "query_access", map[string]any{"host": host, "agent": "claudebot", "state": "restricted", "path": "/products/*"}, &acc, false)
	if len(acc.Rows) != 1 || !strings.HasSuffix(acc.Rows[0].URL, "/products/old") || !strings.Contains(acc.Rows[0].Reason, "noindex") {
		t.Errorf("query_access = %+v", acc)
	}
	call(t, cs, "query_access", map[string]any{"agent": "GPTBot", "state": "blocked"}, &acc, false)
	if len(acc.Rows) != 3 {
		t.Errorf("GPTBot is blocked everywhere; got %d rows", len(acc.Rows))
	}
	call(t, cs, "query_access", map[string]any{"agent": "NoSuchBot"}, nil, true)
	call(t, cs, "query_access", map[string]any{"state": "maybe"}, nil, true)

	var f GetFindingOut
	call(t, cs, "get_finding", map[string]any{"finding_id": "thin_raw_html"}, &f, false)
	if f.Finding.Fidelity != audit.Heuristic || len(f.Finding.URLs) != 1 {
		t.Errorf("get_finding = %+v", f)
	}

	call(t, cs, "compare_audits", map[string]any{"host": host}, nil, true) // only one audit
	call(t, cs, "run_audit", map[string]any{"url": srv.URL}, &sum, false)
	var cmp CompareOut
	call(t, cs, "compare_audits", map[string]any{"host": host}, &cmp, false)
	if cmp.OldID != 1 || cmp.NewID != 2 || !cmp.Comparison.Empty() {
		t.Errorf("compare_audits = %+v", cmp)
	}

	var ag ListAgentsOut
	call(t, cs, "list_agents", map[string]any{}, &ag, false)
	if len(ag.Agents) < 10 {
		t.Errorf("list_agents returned %d agents", len(ag.Agents))
	}
}

func TestRunAuditCapsPages(t *testing.T) {
	s := newServer(t)
	var got int
	s.run = func(ctx context.Context, c audit.Config) (*audit.Report, error) {
		got = c.MaxPages
		return &audit.Report{Host: "x.test", Summary: audit.Summary{Findings: map[string]int{}}}, nil
	}
	cs := connect(t, s)
	call(t, cs, "run_audit", map[string]any{"url": "https://x.test", "max_pages": 10000}, nil, false)
	if got != maxRunPages {
		t.Errorf("max pages = %d, want the cap %d", got, maxRunPages)
	}
}

func TestInstructionsMentionFidelity(t *testing.T) {
	cs := connect(t, newServer(t))
	ins := cs.InitializeResult().Instructions
	for _, w := range []string{"exact", "simulated", "heuristic", "can't tell"} {
		if !strings.Contains(ins, w) {
			t.Errorf("instructions don't mention %q", w)
		}
	}
}

func hasFinding(s SummaryOut, id, fidelity string) bool {
	for _, f := range s.Findings {
		if f.ID == id && f.Fidelity == fidelity {
			return true
		}
	}
	return false
}
