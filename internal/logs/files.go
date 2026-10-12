package logs

import (
	"bufio"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"net/url"
	"os"
	"strings"

	"github.com/kevinletchford/open-crawl/internal/agents"
	"github.com/kevinletchford/open-crawl/internal/fetch"
	"github.com/kevinletchford/open-crawl/internal/robots"
)

// Options configures AnalyzeFiles.
type Options struct {
	Format Format
	Agents []agents.Agent // empty means every agent
	// Site, if set, is the site whose robots.txt is checked for fetches of
	// disallowed paths.
	Site string
	// Verify downloads the operators' published IP ranges and checks
	// reverse DNS. Without it every request is Unknown.
	Verify   bool
	Client   *fetch.Client // default fetch.New
	Resolver Resolver      // for reverse-DNS checks; nil skips them
	// Stdin is read for the file name "-". Nil means "-" is refused.
	Stdin io.Reader
	// Progress, if set, receives one-line progress messages.
	Progress func(msg string)
}

// AnalyzeFiles reads every file (gunzipping .gz files) and reports what the
// AI agents in them requested.
func AnalyzeFiles(ctx context.Context, files []string, o Options) (*Report, error) {
	if len(files) == 0 {
		return nil, fmt.Errorf("logs: no files given")
	}
	switch o.Format {
	case "":
		o.Format = FormatAuto
	case FormatAuto, FormatCombined, FormatJSON:
	default:
		return nil, fmt.Errorf("logs: unknown format %q", o.Format)
	}
	if len(o.Agents) == 0 {
		o.Agents = agents.All()
	}
	if o.Client == nil {
		o.Client = fetch.New(fetch.Options{})
	}
	progress := func(msg string) {
		if o.Progress != nil {
			o.Progress(msg)
		}
	}

	var verifier *Verifier
	if o.Verify {
		progress("loading published IP ranges")
		verifier = NewVerifier(ctx, o.Client, o.Agents, o.Resolver)
	}
	var policy *robots.Policy
	robotsURL := ""
	if o.Site != "" {
		u, err := siteURL(o.Site)
		if err != nil {
			return nil, err
		}
		robotsURL = u.Scheme + "://" + u.Host + "/robots.txt"
		progress("fetching " + robotsURL)
		res := o.Client.GetLimited(ctx, robotsURL, "", robots.MaxSize)
		p := robots.FromResponse(res.Status, res.Body, res.Err)
		policy = &p
	}

	an := NewAnalyzer(o.Agents, verifier, policy)
	for _, name := range files {
		progress("reading " + name)
		if err := readFile(ctx, name, o.Stdin, func(line string) { an.Line(ctx, line, o.Format) }); err != nil {
			return nil, err
		}
	}
	r := an.Report()
	r.RobotsURL = robotsURL
	return r, nil
}

func siteURL(s string) (*url.URL, error) {
	if !strings.Contains(s, "://") {
		s = "https://" + s
	}
	u, err := url.Parse(s)
	if err != nil || u.Host == "" {
		return nil, fmt.Errorf("logs: bad site %q", s)
	}
	return u, nil
}

// readFile calls fn with each line of name ("-" for stdin), gunzipping .gz
// files.
func readFile(ctx context.Context, name string, stdin io.Reader, fn func(string)) error {
	r := stdin
	if name == "-" {
		if stdin == nil {
			return fmt.Errorf("logs: can't read stdin here")
		}
	} else {
		f, err := os.Open(name)
		if err != nil {
			return err
		}
		defer f.Close()
		r = f
		if strings.HasSuffix(name, ".gz") {
			zr, err := gzip.NewReader(f)
			if err != nil {
				return fmt.Errorf("%s: %w", name, err)
			}
			defer zr.Close()
			r = zr
		}
	}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 4<<20)
	for sc.Scan() {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		fn(sc.Text())
	}
	if err := sc.Err(); err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	return nil
}
