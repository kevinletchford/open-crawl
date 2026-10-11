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
	"more": func(ps []audit.PageResult) int { return max(0, len(ps)-maxMatrixRows) },
	"join": strings.Join,
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
