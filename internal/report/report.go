// Package report renders an audit.Report for people: a self-contained HTML
// page and a plain-text terminal summary.
package report

import (
	_ "embed"
	"fmt"
	"html/template"
	"io"
	"strings"
	"text/tabwriter"

	"github.com/kevinletchford/open-crawl/internal/audit"
	"github.com/kevinletchford/open-crawl/internal/logs"
)

//go:embed report.html.tmpl
var htmlTemplate string

// maxMatrixRows caps the URL × agent table in the HTML report.
const maxMatrixRows = 300

var tmpl = template.Must(template.New("report").Funcs(template.FuncMap{
	"stateFor": func(p audit.PageResult, agent string) audit.AgentAccess {
		for _, a := range p.Access {
			if a.Agent == agent {
				return a
			}
		}
		return audit.AgentAccess{}
	},
	"capPages": func(ps []audit.PageResult) []audit.PageResult {
		if len(ps) > maxMatrixRows {
			return ps[:maxMatrixRows]
		}
		return ps
	},
	"more":       func(ps []audit.PageResult) int { return max(0, len(ps)-maxMatrixRows) },
	"join":       strings.Join,
	"percent":    func(f float64) string { return fmt.Sprintf("%.0f%%", f*100) },
	"categories": func() []string { return audit.Categories },
	"scoreTip": func(s *audit.PageScore) string {
		var b strings.Builder
		for _, c := range s.Checks {
			mark := "✓"
			if !c.Passed() {
				mark = "✗"
			}
			fmt.Fprintf(&b, "%s %s (%s)\n", mark, c.Title, c.Fidelity)
		}
		return b.String()
	},
	"tip": func(a audit.AgentAccess) string {
		parts := []string{a.Robots.Explain()}
		if len(a.Directives) > 0 {
			parts = append(parts, "directives: "+strings.Join(a.Directives, ", "))
		}
		parts = append(parts, a.Notes...)
		return strings.Join(parts, "\n")
	},
}).Parse(htmlTemplate))

// HTML writes the report as one self-contained HTML page.
func HTML(w io.Writer, r *audit.Report) error {
	return tmpl.Execute(w, r)
}

// Text writes a terminal summary: findings, then one line per agent.
func Text(w io.Writer, r *audit.Report) {
	fmt.Fprintf(w, "\nOpenCrawl audit of %s: %d URLs found, %d fetched, in %s\n",
		r.Host, r.Summary.PagesFound, r.Summary.PagesFetched, r.Duration.Round(1e8))
	fmt.Fprintf(w, "robots.txt: %s", r.Robots.Policy.Mode)
	if r.Robots.Policy.Status != 0 {
		fmt.Fprintf(w, " (HTTP %d)", r.Robots.Policy.Status)
	}
	fmt.Fprintln(w)

	if s := r.Score; s != nil {
		fmt.Fprintf(w, "\nAI readability score: %d", s.Overall)
		for _, cat := range audit.Categories {
			if v, ok := s.Categories[cat]; ok {
				fmt.Fprintf(w, " · %s %d", cat, v)
			}
		}
		fmt.Fprintf(w, " (%d pages)\n", s.Pages)
		for i, f := range s.Fixes {
			if i == 3 {
				break
			}
			fmt.Fprintf(w, "  failing: %s (%d pages, +%.1f points if fixed)\n", f.Title, f.Pages, f.Impact)
		}
	}

	fmt.Fprintf(w, "\nFindings (%d)\n", len(r.Findings))
	for _, f := range r.Findings {
		fmt.Fprintf(w, "  %-7s %-9s %s\n", strings.ToUpper(string(f.Severity)), "["+string(f.Fidelity)+"]", f.Title)
	}

	fmt.Fprintln(w, "\nAgents")
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "  AGENT\tKIND\tROOT\tALLOWED\tRESTRICTED\tBLOCKED\tUA PROBE")
	for _, s := range r.Summary.Agents {
		probe := s.Probe
		if probe == "" {
			probe = "-"
		}
		fmt.Fprintf(tw, "  %s\t%s\t%s\t%d\t%d\t%d\t%s\n", s.Agent, s.Kind, s.Root, s.Allowed, s.Restricted, s.Blocked, probe)
	}
	tw.Flush()
	fmt.Fprintln(w, "\nexact = from public signals · simulated = our requests with the agent's user agent, not the real agent · heuristic = an inference")
}

// TextComparison prints what changed between two saved audits.
func TextComparison(w io.Writer, c audit.Comparison, oldID, newID int64) {
	fmt.Fprintf(w, "\nChanges since audit #%d (this is #%d)\n", oldID, newID)
	if c.Empty() {
		fmt.Fprintln(w, "  nothing changed")
		return
	}
	if c.PagesBefore != c.PagesAfter {
		fmt.Fprintf(w, "  note: the audits fetched different numbers of pages (%d → %d), so changes may reflect coverage, not the site\n", c.PagesBefore, c.PagesAfter)
	}
	if c.ScoreBefore != c.ScoreAfter && c.ScoreBefore >= 0 && c.ScoreAfter >= 0 {
		fmt.Fprintf(w, "  score: %d → %d\n", c.ScoreBefore, c.ScoreAfter)
	}
	for _, a := range c.AgentChanges {
		fmt.Fprintf(w, "  %s on /: %s → %s\n", a.Agent, a.Before, a.After)
	}
	for _, f := range c.New {
		fmt.Fprintf(w, "  + new      %-7s %s\n", strings.ToUpper(string(f.Severity)), f.Title)
	}
	for _, f := range c.Resolved {
		fmt.Fprintf(w, "  - resolved %-7s %s\n", strings.ToUpper(string(f.Severity)), f.Title)
	}
	for _, f := range c.Changed {
		fmt.Fprintf(w, "  ~ changed  %-7s %s (count %d → %d)\n", strings.ToUpper(string(f.Severity)), f.Title, f.Before, f.Count)
	}
}

// LogsText prints a log analysis.
func LogsText(w io.Writer, r *logs.Report) {
	fmt.Fprintf(w, "\nAccess logs: %d lines, %d parsed", r.Lines, r.Parsed)
	if !r.From.IsZero() {
		fmt.Fprintf(w, ", %s to %s", r.From.Format("2006-01-02 15:04"), r.To.Format("2006-01-02 15:04 MST"))
	}
	fmt.Fprintln(w)
	if r.RobotsURL != "" {
		fmt.Fprintf(w, "Disallowed fetches checked against %s\n", r.RobotsURL)
	}
	for agent, msg := range r.VerificationErrors {
		fmt.Fprintf(w, "warning: couldn't load %s's IP ranges (%s); its requests stay unverified\n", agent, msg)
	}

	if len(r.Agents) == 0 {
		fmt.Fprintln(w, "\nNo requests from known AI agents.")
	} else {
		fmt.Fprintln(w, "\nAgents (outcome columns count only verified or unverifiable requests; spoofed ones are left out)")
		tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
		fmt.Fprintln(tw, "  AGENT\tREQUESTS\tVERIFIED\tSPOOFED\tUNVERIFIABLE\t2XX\t3XX\t4XX\t5XX\tREFUSED\tDISALLOWED")
		for _, s := range r.Agents {
			fmt.Fprintf(tw, "  %s\t%d\t%d\t%d\t%d\t%d\t%d\t%d\t%d\t%d\t%d\n", s.Agent, s.Requests, s.Verified, s.Unverified, s.Unknown,
				s.Status["2xx"], s.Status["3xx"], s.Status["4xx"], s.Status["5xx"], s.Refused, s.Disallowed)
		}
		tw.Flush()
	}

	fmt.Fprintf(w, "\nFindings (%d)\n", len(r.Findings))
	for _, f := range r.Findings {
		fmt.Fprintf(w, "  %-7s %-9s %s\n", strings.ToUpper(string(f.Severity)), "["+string(f.Fidelity)+"]", f.Title)
	}
}
