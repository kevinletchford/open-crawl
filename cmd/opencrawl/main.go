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
	"github.com/kevinletchford/open-crawl/internal/report"
	"github.com/kevinletchford/open-crawl/internal/robots"
)

const usage = `opencrawl: see what AI crawlers may fetch from your site, and what they get.

Usage:
  opencrawl audit  [flags] <url>   crawl a site and report access for every AI agent
  opencrawl robots [flags] <url>   check one URL against robots.txt for every agent
  opencrawl agents [-json]         list the agents in the registry

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

	r, err := audit.Run(ctx, cfg)
	if err != nil {
		return err
	}

	if *jsonOnly {
		return writeJSON(os.Stdout, r)
	}

	dir := *out
	if dir == "" {
		dir = fmt.Sprintf("opencrawl-%s-%s", strings.ReplaceAll(r.Host, ":", "_"), r.GeneratedAt.Format("20060102-150405"))
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	if err := writeFile(filepath.Join(dir, "report.json"), func(w io.Writer) error { return writeJSON(w, r) }); err != nil {
		return err
	}
	htmlPath := filepath.Join(dir, "report.html")
	if err := writeFile(htmlPath, func(w io.Writer) error { return report.HTML(w, r) }); err != nil {
		return err
	}

	report.Text(os.Stdout, r)
	fmt.Printf("\nReport: %s\nJSON:   %s\n", htmlPath, filepath.Join(dir, "report.json"))
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
