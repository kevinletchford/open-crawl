// Command opencrawl audits what AI crawlers may fetch from a site and what
// they receive.
//
//	opencrawl audit https://example.com
//	opencrawl robots https://example.com/pricing
//	opencrawl agents
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/kevinletchford/open-crawl/internal/agents"
	"github.com/kevinletchford/open-crawl/internal/audit"
	"github.com/kevinletchford/open-crawl/internal/fetch"
	"github.com/kevinletchford/open-crawl/internal/render"
	"github.com/kevinletchford/open-crawl/internal/report"
	"github.com/kevinletchford/open-crawl/internal/robots"
	"github.com/kevinletchford/open-crawl/internal/store"
)

const usage = `opencrawl: see what AI crawlers may fetch from your site, and what they get.

Usage:
  opencrawl audit  [flags] <url>   crawl a site and report access for every AI agent
  opencrawl robots [flags] <url>   check one URL against robots.txt for every agent
  opencrawl agents [-json]         list the agents in the registry

  opencrawl history [host]         list saved audits
  opencrawl show <id>              re-print a saved audit, or write its report files
  opencrawl compare <id> [id]      what changed between two audits (default: the one before)

Audits are saved to $OPENCRAWL_DB or ~/.opencrawl/opencrawl.db.

Run "opencrawl <command> -h" for a command's flags.
`

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	var err error
	switch cmd, args := os.Args[1], os.Args[2:]; cmd {
	case "audit":
		err = runAudit(ctx, args)
	case "robots":
		err = runRobots(ctx, args)
	case "agents":
		err = runAgents(args)
	case "history":
		err = runHistory(ctx, args)
	case "show":
		err = runShow(ctx, args)
	case "compare":
		err = runCompare(ctx, args)
	case "-h", "--help", "help":
		fmt.Print(usage)
	default:
		fmt.Fprintf(os.Stderr, "opencrawl: unknown command %q\n\n%s", cmd, usage)
		os.Exit(2)
	}
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			os.Exit(0)
		}
		fmt.Fprintln(os.Stderr, "opencrawl:", err)
		os.Exit(1)
	}
}

// parseArgs parses flags that may come before or after the one positional
// URL, so "opencrawl audit https://x -max-pages 10" works too.
func parseArgs(fs *flag.FlagSet, args []string) (string, error) {
	var positional []string
	for {
		if err := fs.Parse(args); err != nil {
			return "", err
		}
		if fs.NArg() == 0 {
			break
		}
		positional = append(positional, fs.Arg(0))
		args = fs.Args()[1:]
	}
	if len(positional) != 1 {
		fs.Usage()
		return "", errors.New("expected exactly one URL")
	}
	return positional[0], nil
}

func agentList(s string) ([]agents.Agent, error) {
	if s == "" {
		return agents.All(), nil
	}
	return agents.Select(strings.Split(s, ","))
}

func runAudit(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("audit", flag.ContinueOnError)
	maxPages := fs.Int("max-pages", 50, "maximum pages to fetch")
	concurrency := fs.Int("concurrency", 4, "requests in flight at once")
	delay := fs.Duration("delay", 250*time.Millisecond, "minimum gap between request starts")
	timeout := fs.Duration("timeout", 15*time.Second, "per-request timeout")
	probePages := fs.Int("probe-pages", 3, "pages to re-fetch with each agent's user agent (0 to skip)")
	agentNames := fs.String("agents", "", "comma-separated agents to check (default: all)")
	ignoreRobots := fs.Bool("ignore-robots", false, "let the audit's own crawler fetch pages robots.txt disallows for it (only for sites you own)")
	out := fs.String("out", "", "directory for report.json and report.html (default: opencrawl-<host>-<time>)")
	jsonOnly := fs.Bool("json", false, "print the JSON report to stdout and write no files")
	quiet := fs.Bool("q", false, "no progress output")
	renderJS := fs.Bool("render", false, "render pages in headless Chrome and compare them with the raw HTML")
	renderPages := fs.Int("render-pages", 10, "pages to render with -render")
	chrome := fs.String("chrome", "", "path to Chrome or Chromium for -render (default: search the usual places)")
	dbPath := dbFlag(fs)
	noSave := fs.Bool("no-save", false, "don't save the audit to the database")
	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), "Usage: opencrawl audit [flags] <url>")
		fs.PrintDefaults()
	}
	target, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	list, err := agentList(*agentNames)
	if err != nil {
		return err
	}
	probes := *probePages
	if probes == 0 {
		probes = -1 // audit.Config treats 0 as "default"
	}

	cfg := audit.Config{
		URL:          target,
		Agents:       list,
		MaxPages:     *maxPages,
		Concurrency:  *concurrency,
		Delay:        *delay,
		Timeout:      *timeout,
		ProbePages:   probes,
		IgnoreRobots: *ignoreRobots,
	}
	if !*quiet {
		cfg.Progress = func(msg string) { fmt.Fprintln(os.Stderr, "  "+msg) }
	}
	if *renderJS {
		pool, err := render.New(ctx, render.Options{ExecPath: *chrome})
		if err != nil {
			return fmt.Errorf("%w\n-render needs Chrome or Chromium; install it or pass -chrome /path/to/chrome", err)
		}
		defer pool.Close()
		cfg.Renderer = pool
		cfg.RenderPages = *renderPages
	}

	r, err := audit.Run(ctx, cfg)
	if err != nil {
		return err
	}

	var saved int64
	var cmp *audit.Comparison
	var prevID int64
	if !*noSave {
		st, err := store.Open(*dbPath)
		if err != nil {
			return err
		}
		defer st.Close()
		if saved, err = st.Save(ctx, r); err != nil {
			return err
		}
		if prevID, err = st.Previous(ctx, r.Host, saved); err == nil && prevID != 0 {
			if prev, err := st.Get(ctx, prevID); err == nil {
				c := audit.Compare(prev, r)
				cmp = &c
			}
		}
	}

	if *jsonOnly {
		return writeJSON(os.Stdout, r)
	}

	dir := *out
	if dir == "" {
		dir = defaultOutDir(r)
	}
	htmlPath, err := writeReportFiles(dir, r)
	if err != nil {
		return err
	}

	report.Text(os.Stdout, r)
	if cmp != nil {
		report.TextComparison(os.Stdout, *cmp, prevID, saved)
	}
	if saved != 0 {
		fmt.Printf("\nSaved as audit #%d in %s\n", saved, *dbPath)
	}
	fmt.Printf("Report: %s\nJSON:   %s\n", htmlPath, filepath.Join(dir, "report.json"))
	return nil
}

func defaultOutDir(r *audit.Report) string {
	return fmt.Sprintf("opencrawl-%s-%s", strings.ReplaceAll(r.Host, ":", "_"), r.GeneratedAt.Format("20060102-150405"))
}

// writeReportFiles writes report.json and report.html into dir and returns
// the HTML path.
func writeReportFiles(dir string, r *audit.Report) (string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	if err := writeFile(filepath.Join(dir, "report.json"), func(w io.Writer) error { return writeJSON(w, r) }); err != nil {
		return "", err
	}
	htmlPath := filepath.Join(dir, "report.html")
	if err := writeFile(htmlPath, func(w io.Writer) error { return report.HTML(w, r) }); err != nil {
		return "", err
	}
	return htmlPath, nil
}

func dbFlag(fs *flag.FlagSet) *string {
	return fs.String("db", store.DefaultPath(), "audit database")
}

func runHistory(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("history", flag.ContinueOnError)
	dbPath := dbFlag(fs)
	limit := fs.Int("n", 20, "number of audits to list")
	asJSON := fs.Bool("json", false, "print as JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	st, err := store.Open(*dbPath)
	if err != nil {
		return err
	}
	defer st.Close()
	list, err := st.List(ctx, fs.Arg(0), *limit)
	if err != nil {
		return err
	}
	if *asJSON {
		return writeJSON(os.Stdout, list)
	}
	if len(list) == 0 {
		fmt.Println("No saved audits. Run `opencrawl audit <url>` first.")
		return nil
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tWHEN\tHOST\tPAGES\tSCORE\tERRORS\tWARNINGS\tINFO")
	for _, a := range list {
		score := "-"
		if a.Score >= 0 {
			score = fmt.Sprint(a.Score)
		}
		fmt.Fprintf(tw, "#%d\t%s\t%s\t%d\t%s\t%d\t%d\t%d\n", a.ID, a.GeneratedAt.Local().Format("2006-01-02 15:04"),
			a.Host, a.PagesFetched, score, a.Errors, a.Warnings, a.Infos)
	}
	return tw.Flush()
}

func parseID(s string) (int64, error) {
	var id int64
	if _, err := fmt.Sscan(strings.TrimPrefix(s, "#"), &id); err != nil || id <= 0 {
		return 0, fmt.Errorf("bad audit ID %q", s)
	}
	return id, nil
}

func runShow(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("show", flag.ContinueOnError)
	dbPath := dbFlag(fs)
	out := fs.String("out", "", "also write report.json and report.html to this directory")
	asJSON := fs.Bool("json", false, "print the JSON report")
	arg, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	id, err := parseID(arg)
	if err != nil {
		return err
	}
	st, err := store.Open(*dbPath)
	if err != nil {
		return err
	}
	defer st.Close()
	r, err := st.Get(ctx, id)
	if err != nil {
		return err
	}
	if *asJSON {
		return writeJSON(os.Stdout, r)
	}
	report.Text(os.Stdout, r)
	if *out != "" {
		htmlPath, err := writeReportFiles(*out, r)
		if err != nil {
			return err
		}
		fmt.Printf("\nReport: %s\n", htmlPath)
	}
	return nil
}

func runCompare(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("compare", flag.ContinueOnError)
	dbPath := dbFlag(fs)
	asJSON := fs.Bool("json", false, "print as JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() < 1 || fs.NArg() > 2 {
		return errors.New("usage: opencrawl compare <id> [id]")
	}
	st, err := store.Open(*dbPath)
	if err != nil {
		return err
	}
	defer st.Close()

	newID, err := parseID(fs.Arg(fs.NArg() - 1))
	if err != nil {
		return err
	}
	newR, err := st.Get(ctx, newID)
	if err != nil {
		return err
	}
	var oldID int64
	if fs.NArg() == 2 {
		if oldID, err = parseID(fs.Arg(0)); err != nil {
			return err
		}
	} else if oldID, err = st.Previous(ctx, newR.Host, newID); err != nil {
		return err
	} else if oldID == 0 {
		return fmt.Errorf("audit #%d is the first audit of %s; nothing to compare", newID, newR.Host)
	}
	oldR, err := st.Get(ctx, oldID)
	if err != nil {
		return err
	}
	c := audit.Compare(oldR, newR)
	if *asJSON {
		return writeJSON(os.Stdout, c)
	}
	report.TextComparison(os.Stdout, c, oldID, newID)
	return nil
}

func runRobots(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("robots", flag.ContinueOnError)
	agentNames := fs.String("agents", "", "comma-separated agents to check (default: all)")
	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), "Usage: opencrawl robots [flags] <url>")
		fs.PrintDefaults()
	}
	target, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if !strings.Contains(target, "://") {
		target = "https://" + target
	}
	u, err := url.Parse(target)
	if err != nil || u.Host == "" {
		return fmt.Errorf("bad URL %q", target)
	}
	list, err := agentList(*agentNames)
	if err != nil {
		return err
	}

	robotsURL := u.Scheme + "://" + u.Host + "/robots.txt"
	res := fetch.New(fetch.Options{}).GetLimited(ctx, robotsURL, "", robots.MaxSize)
	policy := robots.FromResponse(res.Status, res.Body, res.Err)
	path := u.EscapedPath()
	if path == "" {
		path = "/"
	}
	if u.RawQuery != "" {
		path += "?" + u.RawQuery
	}

	fmt.Printf("%s: %s", robotsURL, policy.Mode)
	if policy.Status != 0 {
		fmt.Printf(" (HTTP %d)", policy.Status)
	}
	fmt.Printf("\nPath: %s\n\n", path)
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "AGENT\tKIND\tVERDICT\tWHY")
	for _, a := range list {
		v := policy.Check(a.Token, path)
		verdict := "allowed"
		if !v.Allowed {
			verdict = "BLOCKED"
		}
		why := v.Explain()
		if !v.Allowed && a.RobotsTxt == agents.MayIgnore {
			why += " (operator says it may not apply)"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", a.Name, a.Kind, verdict, why)
	}
	return tw.Flush()
}

func runAgents(args []string) error {
	fs := flag.NewFlagSet("agents", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "print the registry as JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	list := agents.All()
	if *asJSON {
		return writeJSON(os.Stdout, list)
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "AGENT\tOPERATOR\tKIND\tRUNS JS\tROBOTS.TXT\tUSER AGENT")
	for _, a := range list {
		js := "no"
		if a.RendersJS {
			js = "yes"
		}
		ua := a.UserAgentSource
		if a.Kind == agents.KindTokenOnly {
			js, ua = "-", "none (fetched by "+a.FetchedBy+")"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", a.Name, a.Operator, a.Kind, js, a.RobotsTxt, ua)
	}
	return tw.Flush()
}

func writeJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

func writeFile(path string, write func(io.Writer) error) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	if err := write(f); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}
