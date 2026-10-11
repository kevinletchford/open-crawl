package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/kevinletchford/open-crawl/internal/audit"
	"github.com/kevinletchford/open-crawl/internal/robots"
)

func report(host string, at time.Time, score int, findings ...audit.Finding) *audit.Report {
	gpt := robots.Verdict{Allowed: false, Group: "gptbot", Rule: &robots.Rule{Pattern: "/", Line: 2}}
	ok := robots.Verdict{Allowed: true, Group: "*"}
	r := &audit.Report{
		Version: audit.ReportVersion, Host: host, Target: "https://" + host, GeneratedAt: at,
		Pages: []audit.PageResult{
			{URL: "https://" + host + "/", Fetched: true, Status: 200, WordCount: 300,
				Score: &audit.PageScore{Overall: score},
				Access: []audit.AgentAccess{
					{Agent: "GPTBot", State: audit.StateBlocked, Robots: gpt},
					{Agent: "ClaudeBot", State: audit.StateAllowed, Robots: ok},
				}},
			{URL: "https://" + host + "/products/widget", Fetched: true, Status: 200, WordCount: 12,
				Render: &audit.RenderDiff{MissingShare: 0.9},
				Access: []audit.AgentAccess{
					{Agent: "GPTBot", State: audit.StateBlocked, Robots: gpt},
					{Agent: "ClaudeBot", State: audit.StateRestricted, Robots: ok, Directives: []string{"noindex"}},
				}},
		},
		Findings: findings,
		Score:    &audit.SiteScore{Overall: score},
		Summary: audit.Summary{PagesFound: 2, PagesFetched: 2, Findings: map[string]int{"warning": len(findings)},
			Agents: []audit.AgentSummary{{Agent: "GPTBot", Root: "blocked"}}},
	}
	return r
}

func TestSaveListGetQuery(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "nested", "audits.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	t0 := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	id1, err := s.Save(ctx, report("example.com", t0, 70, audit.Finding{ID: "noindex", Severity: audit.SeverityWarning, Count: 1}))
	if err != nil {
		t.Fatal(err)
	}
	id2, _ := s.Save(ctx, report("example.com", t0.Add(time.Hour), 80))
	if _, err := s.Save(ctx, report("other.org", t0, 50)); err != nil {
		t.Fatal(err)
	}

	list, err := s.List(ctx, "example.com", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 || list[0].ID != id2 || list[0].Score != 80 || !list[1].GeneratedAt.Equal(t0) {
		t.Errorf("list = %+v", list)
	}
	if all, _ := s.List(ctx, "", 0); len(all) != 3 {
		t.Errorf("unfiltered list has %d rows", len(all))
	}
	if prev, _ := s.Previous(ctx, "example.com", id2); prev != id1 {
		t.Errorf("previous = %d, want %d", prev, id1)
	}
	if latest, _ := s.Latest(ctx, "other.org"); latest != 3 {
		t.Errorf("latest = %d", latest)
	}

	r, err := s.Get(ctx, id1)
	if err != nil || r.Host != "example.com" || len(r.Findings) != 1 || r.Pages[1].Render.MissingShare != 0.9 {
		t.Fatalf("get = %+v, %v", r, err)
	}
	if _, err := s.Get(ctx, 999); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing audit error = %v", err)
	}

	rows, err := s.QueryAccess(ctx, id1, AccessQuery{Agent: "claudebot", State: audit.StateRestricted, Path: "/products/*"})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].URL != "https://example.com/products/widget" || rows[0].Reason != `no rule matches in group "*"; directives: noindex` {
		t.Errorf("rows = %+v", rows)
	}
	if rows, _ := s.QueryAccess(ctx, id1, AccessQuery{Agent: "GPTBot"}); len(rows) != 2 {
		t.Errorf("GPTBot rows = %d, want 2", len(rows))
	}
}

func TestReopenKeepsDataAndSchema(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "a.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	s.Save(ctx, report("example.com", time.Now(), 60))
	s.Close()

	s, err = Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer s.Close()
	if list, _ := s.List(ctx, "", 0); len(list) != 1 {
		t.Errorf("after reopen: %d audits", len(list))
	}
}
