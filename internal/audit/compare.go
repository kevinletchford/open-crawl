package audit

import "slices"

// Comparison is what changed between two audits of the same site.
type Comparison struct {
	// ScoreBefore and ScoreAfter are -1 when that audit has no score.
	ScoreBefore int `json:"score_before"`
	ScoreAfter  int `json:"score_after"`
	// PagesBefore and PagesAfter are the pages fetched. When they differ,
	// the audits covered different parts of the site and the scores aren't
	// directly comparable.
	PagesBefore int `json:"pages_before"`
	PagesAfter  int `json:"pages_after"`

	New      []Finding `json:"new"`      // in the new audit only
	Resolved []Finding `json:"resolved"` // in the old audit only
	// Changed are findings in both whose count changed; Before holds the
	// old count.
	Changed []ChangedFinding `json:"changed"`

	// Agents whose robots.txt verdict for "/" changed.
	AgentChanges []AgentChange `json:"agent_changes"`
}

// ChangedFinding is a finding present in both audits with a new count.
type ChangedFinding struct {
	Finding
	Before int `json:"before"`
}

// AgentChange is a change in an agent's robots.txt verdict for "/".
type AgentChange struct {
	Agent  string `json:"agent"`
	Before string `json:"before"`
	After  string `json:"after"`
}

// Empty reports whether nothing changed.
func (c Comparison) Empty() bool {
	return c.ScoreBefore == c.ScoreAfter && len(c.New) == 0 && len(c.Resolved) == 0 &&
		len(c.Changed) == 0 && len(c.AgentChanges) == 0
}

// Compare reports what changed from old to new. Findings are matched by ID.
func Compare(old, new *Report) Comparison {
	c := Comparison{ScoreBefore: -1, ScoreAfter: -1,
		PagesBefore: old.Summary.PagesFetched, PagesAfter: new.Summary.PagesFetched}
	if old.Score != nil {
		c.ScoreBefore = old.Score.Overall
	}
	if new.Score != nil {
		c.ScoreAfter = new.Score.Overall
	}

	before := map[string]Finding{}
	for _, f := range old.Findings {
		before[f.ID] = f
	}
	after := map[string]bool{}
	for _, f := range new.Findings {
		after[f.ID] = true
		prev, ok := before[f.ID]
		switch {
		case !ok:
			c.New = append(c.New, f)
		case prev.Count != f.Count:
			c.Changed = append(c.Changed, ChangedFinding{Finding: f, Before: prev.Count})
		}
	}
	for _, f := range old.Findings {
		if !after[f.ID] {
			c.Resolved = append(c.Resolved, f)
		}
	}

	roots := map[string]string{}
	for _, s := range old.Summary.Agents {
		roots[s.Agent] = s.Root
	}
	for _, s := range new.Summary.Agents {
		if b, ok := roots[s.Agent]; ok && b != s.Root {
			c.AgentChanges = append(c.AgentChanges, AgentChange{Agent: s.Agent, Before: b, After: s.Root})
		}
	}
	slices.SortFunc(c.AgentChanges, func(a, b AgentChange) int {
		if a.Agent < b.Agent {
			return -1
		}
		if a.Agent > b.Agent {
			return 1
		}
		return 0
	})
	return c
}
