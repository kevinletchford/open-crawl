package audit

import (
	"net/http"
	"strings"
	"testing"
)

func TestTallyWeightsAndPartialCredit(t *testing.T) {
	s := tally([]Check{
		{Category: CatAccess, Weight: 3, Value: 1},
		{Category: CatAccess, Weight: 1, Value: 0},
		{Category: CatRendering, Weight: 2, Value: 0.5},
	})
	if s.Categories[CatAccess] != 75 || s.Categories[CatRendering] != 50 {
		t.Errorf("categories = %v", s.Categories)
	}
	if _, ok := s.Categories[CatStructure]; ok {
		t.Error("a category with no checks must be left out, not scored 0")
	}
	if s.Overall != 63 { // round((75 + 50) / 2)
		t.Errorf("overall = %d, want 63", s.Overall)
	}
}

func TestAuditScoresPagesAndRanksFixes(t *testing.T) {
	r := runAudit(t, fakeSite(t, http.StatusOK), nil)
	if r.Score == nil || r.Score.Pages == 0 {
		t.Fatalf("site score = %+v", r.Score)
	}

	about, app, hidden := pageByPath(r, "/about"), pageByPath(r, "/app"), pageByPath(r, "/hidden")
	if about.Score == nil || app.Score == nil || hidden.Score == nil {
		t.Fatal("fetched 2xx HTML pages should be scored")
	}
	if pageByPath(r, "/gone").Score != nil {
		t.Error("a 404 page should not be scored")
	}
	if app.Score.Categories[CatRendering] >= about.Score.Categories[CatRendering] {
		t.Errorf("SPA shell rendering %d should score below server-rendered %d",
			app.Score.Categories[CatRendering], about.Score.Categories[CatRendering])
	}
	if hidden.Score.Categories[CatAccess] >= about.Score.Categories[CatAccess] {
		t.Errorf("noindex page access %d should score below %d",
			hidden.Score.Categories[CatAccess], about.Score.Categories[CatAccess])
	}
	for _, c := range app.Score.Checks {
		if c.ID == "render_content_in_html" && c.Fidelity != Heuristic {
			t.Error("without a render, the rendering check is a heuristic")
		}
	}

	// UA probes saw ClaudeBot (training) blocked, which says nothing about
	// search or user agents, so the simulated check passes.
	for _, c := range about.Score.Checks {
		if c.ID == "access_no_ua_blocking" && !c.Passed() {
			t.Error("only a training agent was blocked; the answer-agent check should pass")
		}
	}

	if len(r.Score.Fixes) == 0 {
		t.Fatal("expected ranked fixes")
	}
	for i := 1; i < len(r.Score.Fixes); i++ {
		if r.Score.Fixes[i].Impact > r.Score.Fixes[i-1].Impact {
			t.Errorf("fixes not ranked by impact: %+v", r.Score.Fixes)
		}
	}
	found := false
	for _, f := range r.Score.Fixes {
		if f.CheckID == "structure_json_ld" && f.Pages > 1 && strings.Contains(f.Title, "JSON-LD") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected missing JSON-LD across pages as a fix: %+v", r.Score.Fixes)
	}
}
