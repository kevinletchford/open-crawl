// Package render loads pages in headless Chrome and returns the DOM after
// JavaScript has run, which is what a visitor's browser shows and what a
// JavaScript-running crawler such as Googlebot can see.
//
// The pool design (one Chrome process, a semaphore bounding open tabs) comes
// from the Browser Run server on the feat/browser-run-server branch.
package render

import (
	"context"
	"fmt"
	"time"

	"github.com/chromedp/cdproto/emulation"
	"github.com/chromedp/chromedp"
)

// Page is one rendered page.
type Page struct {
	URL      string
	FinalURL string
	// HTML is document.documentElement.outerHTML once the page settled.
	HTML     string
	Duration time.Duration
}

// Options configures a Pool.
type Options struct {
	// Tabs is how many pages render at once. Default 2.
	Tabs int
	// Timeout bounds one render, including the settle wait. Default 30s.
	Timeout time.Duration
	// Settle is the longest to wait, after the load event, for the page's
	// text to stop changing. Default 5s.
	Settle time.Duration
	// ExecPath is Chrome's path. Empty means chromedp's search of the usual
	// install locations.
	ExecPath string
}

// Pool renders pages in one headless Chrome process.
type Pool struct {
	allocCtx context.Context
	cancel   context.CancelFunc
	tabs     chan struct{}
	opts     Options
}

// New starts Chrome. It fails if Chrome can't be found or launched.
func New(ctx context.Context, o Options) (*Pool, error) {
	if o.Tabs <= 0 {
		o.Tabs = 2
	}
	if o.Timeout <= 0 {
		o.Timeout = 30 * time.Second
	}
	if o.Settle <= 0 {
		o.Settle = 5 * time.Second
	}
	flags := append(chromedp.DefaultExecAllocatorOptions[:],
		chromedp.DisableGPU,
		chromedp.Flag("disable-dev-shm-usage", true),
		chromedp.WindowSize(1366, 900),
	)
	if o.ExecPath != "" {
		flags = append(flags, chromedp.ExecPath(o.ExecPath))
	}
	allocCtx, cancel := chromedp.NewExecAllocator(ctx, flags...)

	// Launch Chrome now, so a missing browser fails here rather than on the
	// first page.
	warm, warmCancel := chromedp.NewContext(allocCtx)
	defer warmCancel()
	if err := chromedp.Run(warm); err != nil {
		cancel()
		return nil, fmt.Errorf("render: starting Chrome: %w", err)
	}
	return &Pool{allocCtx: allocCtx, cancel: cancel, tabs: make(chan struct{}, o.Tabs), opts: o}, nil
}

// Close shuts Chrome down.
func (p *Pool) Close() { p.cancel() }

// Render loads url with the given user agent (empty for Chrome's own) and
// returns the DOM once the page has settled.
func (p *Pool) Render(ctx context.Context, url, userAgent string) (Page, error) {
	select {
	case p.tabs <- struct{}{}:
	case <-ctx.Done():
		return Page{}, ctx.Err()
	}
	defer func() { <-p.tabs }()

	tab, closeTab := chromedp.NewContext(p.allocCtx)
	defer closeTab()
	tab, cancel := context.WithTimeout(tab, p.opts.Timeout)
	defer cancel()
	// Stop the render if the caller gives up.
	stop := context.AfterFunc(ctx, cancel)
	defer stop()

	start := time.Now()
	out := Page{URL: url}
	actions := []chromedp.Action{}
	if userAgent != "" {
		actions = append(actions, chromedp.ActionFunc(func(ctx context.Context) error {
			return emulation.SetUserAgentOverride(userAgent).Do(ctx)
		}))
	}
	actions = append(actions,
		chromedp.Navigate(url),
		waitForStableText(p.opts.Settle),
		chromedp.Location(&out.FinalURL),
		chromedp.OuterHTML("html", &out.HTML, chromedp.ByQuery),
	)
	if err := chromedp.Run(tab, actions...); err != nil {
		return Page{}, fmt.Errorf("render %s: %w", url, err)
	}
	out.Duration = time.Since(start)
	return out, nil
}

// waitForStableText polls the length of the page's text until it is the same
// on three checks in a row, or until max has passed. Client-rendered pages
// often fill in after the load event, so the load event alone is too early.
func waitForStableText(max time.Duration) chromedp.Action {
	return chromedp.ActionFunc(func(ctx context.Context) error {
		deadline := time.Now().Add(max)
		last, same := -1, 0
		for time.Now().Before(deadline) {
			var n int
			if err := chromedp.Evaluate(`document.body ? document.body.innerText.length : 0`, &n).Do(ctx); err != nil {
				return err
			}
			if n == last {
				same++
				if same >= 2 {
					return nil
				}
			} else {
				last, same = n, 0
			}
			select {
			case <-time.After(250 * time.Millisecond):
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		return nil
	})
}
