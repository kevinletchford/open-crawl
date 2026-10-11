package audit

import "testing"

func TestCompare(t *testing.T) {
	old := &Report{
		Score:    &SiteScore{Overall: 70},
		Findings: []Finding{{ID: "noindex", Count: 3}, {ID: "llms_txt_missing", Count: 1}, {ID: "fetch_errors", Count: 2}},
		Summary:  Summary{Agents: []AgentSummary{{Agent: "GPTBot", Root: "allowed"}, {Agent: "ClaudeBot", Root: "allowed"}}},
	}
	new := &Report{
		Score:    &SiteScore{Overall: 82},
		Findings: []Finding{{ID: "noindex", Count: 1}, {ID: "llms_txt_missing", Count: 1}, {ID: "ua_blocked", Count: 4}},
		Summary:  Summary{Agents: []AgentSummary{{Agent: "GPTBot", Root: "blocked"}, {Agent: "ClaudeBot", Root: "allowed"}}},
	}
	c := Compare(old, new)
	if c.ScoreBefore != 70 || c.ScoreAfter != 82 {
		t.Errorf("scores = %d → %d", c.ScoreBefore, c.ScoreAfter)
	}
	if len(c.New) != 1 || c.New[0].ID != "ua_blocked" {
		t.Errorf("new = %+v", c.New)
	}
	if len(c.Resolved) != 1 || c.Resolved[0].ID != "fetch_errors" {
		t.Errorf("resolved = %+v", c.Resolved)
	}
	if len(c.Changed) != 1 || c.Changed[0].ID != "noindex" || c.Changed[0].Before != 3 || c.Changed[0].Count != 1 {
		t.Errorf("changed = %+v", c.Changed)
	}
	if len(c.AgentChanges) != 1 || c.AgentChanges[0] != (AgentChange{"GPTBot", "allowed", "blocked"}) {
		t.Errorf("agent changes = %+v", c.AgentChanges)
	}
	if c.Empty() || !Compare(new, new).Empty() {
		t.Error("Empty() is wrong")
	}
}
