package browserrun

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/chromedp/cdproto/emulation"
	"github.com/chromedp/cdproto/fetch"
	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/chromedp"
)

// buildActions converts CommonParams into a ready-to-run slice of chromedp
// actions covering: viewport, JS toggle, user-agent, headers, auth, resource
// blocking/allowing, navigation, wait condition, cookies, style/script injection,
// waitForSelector, and waitForTimeout.
func buildActions(p CommonParams) ([]chromedp.Action, error) {
	if p.URL == "" && p.HTML == "" {
		return nil, errors.New("either url or html is required")
	}

	var actions []chromedp.Action

	// --- viewport ---
	w, h := int64(1920), int64(1080)
	dpr := 1.0
	if p.Viewport != nil {
		if p.Viewport.Width > 0 {
			w = int64(p.Viewport.Width)
		}
		if p.Viewport.Height > 0 {
			h = int64(p.Viewport.Height)
		}
		if p.Viewport.DeviceScaleFactor > 0 {
			dpr = p.Viewport.DeviceScaleFactor
		}
	}
	actions = append(actions, chromedp.ActionFunc(func(ctx context.Context) error {
		return emulation.SetDeviceMetricsOverride(w, h, dpr, false).Do(ctx)
	}))

	// --- disable JS ---
	if p.SetJavaScriptEnabled != nil && !*p.SetJavaScriptEnabled {
		actions = append(actions, chromedp.ActionFunc(func(ctx context.Context) error {
			return emulation.SetScriptExecutionDisabled(true).Do(ctx)
		}))
	}

	// --- user agent ---
	if p.UserAgent != "" {
		ua := p.UserAgent
		actions = append(actions, chromedp.ActionFunc(func(ctx context.Context) error {
			return emulation.SetUserAgentOverride(ua).Do(ctx)
		}))
	}

	// --- extra HTTP headers (including auth if provided) ---
	headers := network.Headers{}
	for k, v := range p.SetExtraHTTPHeaders {
		headers[k] = v
	}
	if p.Authenticate != nil {
		creds := p.Authenticate.Username + ":" + p.Authenticate.Password
		headers["Authorization"] = "Basic " + base64.StdEncoding.EncodeToString([]byte(creds))
	}
	if len(headers) > 0 {
		h2 := headers
		actions = append(actions, chromedp.ActionFunc(func(ctx context.Context) error {
			return network.SetExtraHTTPHeaders(h2).Do(ctx)
		}))
	}

	// --- block URLs by pattern or resource type ---
	var blockPatterns []*network.BlockPattern
	for _, pat := range p.RejectRequestPattern {
		p2 := pat
		blockPatterns = append(blockPatterns, &network.BlockPattern{URLPattern: p2, Block: true})
	}
	for _, pat := range resourceTypePatterns(p.RejectResourceTypes) {
		p2 := pat
		blockPatterns = append(blockPatterns, &network.BlockPattern{URLPattern: p2, Block: true})
	}
	if len(blockPatterns) > 0 {
		bp := blockPatterns
		actions = append(actions, chromedp.ActionFunc(func(ctx context.Context) error {
			return network.SetBlockedURLs().WithURLPatterns(bp).Do(ctx)
		}))
	}

	// --- allow-only filters via Fetch domain interception ---
	// When allow filters are set, intercept all requests and reject non-matching ones.
	if len(p.AllowResourceTypes) > 0 || len(p.AllowRequestPattern) > 0 {
		allowTypes := normaliseResourceTypes(p.AllowResourceTypes)
		allowURLRes := compileGlobs(p.AllowRequestPattern)

		actions = append(actions, chromedp.ActionFunc(func(ctx context.Context) error {
			chromedp.ListenTarget(ctx, func(ev interface{}) {
				req, ok := ev.(*fetch.EventRequestPaused)
				if !ok {
					return
				}
				go func() {
					allowed := false
					if len(allowTypes) > 0 {
						rt := strings.ToLower(string(req.ResourceType))
						for _, t := range allowTypes {
							if t == rt {
								allowed = true
								break
							}
						}
					}
					if !allowed && len(allowURLRes) > 0 {
						for _, re := range allowURLRes {
							if re.MatchString(req.Request.URL) {
								allowed = true
								break
							}
						}
					}
					// documents are always allowed to avoid breaking navigation
					if strings.ToLower(string(req.ResourceType)) == "document" {
						allowed = true
					}
					if allowed {
						_ = fetch.ContinueRequest(req.RequestID).Do(ctx)
					} else {
						_ = fetch.FailRequest(req.RequestID, network.ErrorReasonAborted).Do(ctx)
					}
				}()
			})
			return fetch.Enable().WithPatterns([]*fetch.RequestPattern{
				{URLPattern: "*"},
			}).Do(ctx)
		}))
	}

	// --- navigate ---
	timeout := 30 * time.Second
	if p.GotoOptions != nil && p.GotoOptions.Timeout > 0 {
		ms := p.GotoOptions.Timeout
		if ms > 60000 {
			ms = 60000
		}
		timeout = time.Duration(ms) * time.Millisecond
	}

	if p.HTML != "" {
		html := p.HTML
		actions = append(actions,
			chromedp.ActionFunc(func(ctx context.Context) error {
				ctx2, cancel := context.WithTimeout(ctx, timeout)
				defer cancel()
				_, _, _, _, err := page.Navigate("about:blank").Do(ctx2)
				return err
			}),
			chromedp.ActionFunc(func(ctx context.Context) error {
				ft, err := page.GetFrameTree().Do(ctx)
				if err != nil {
					return err
				}
				return page.SetDocumentContent(ft.Frame.ID, html).Do(ctx)
			}),
		)
	} else {
		u := p.URL
		actions = append(actions, chromedp.ActionFunc(func(ctx context.Context) error {
			ctx2, cancel := context.WithTimeout(ctx, timeout)
			defer cancel()
			_, _, _, _, err := page.Navigate(u).Do(ctx2)
			return err
		}))
	}

	// --- wait condition ---
	waitUntil := "domcontentloaded"
	if p.GotoOptions != nil && p.GotoOptions.WaitUntil != "" {
		waitUntil = p.GotoOptions.WaitUntil
	}
	actions = append(actions, waitAction(waitUntil))

	// --- cookies (set after navigation so the domain context exists) ---
	if len(p.Cookies) > 0 {
		cookies := p.Cookies
		actions = append(actions, chromedp.ActionFunc(func(ctx context.Context) error {
			var params []*network.CookieParam
			for _, c := range cookies {
				cp := &network.CookieParam{Name: c.Name, Value: c.Value}
				if c.Domain != "" {
					cp.Domain = c.Domain
				}
				if c.Path != "" {
					cp.Path = c.Path
				}
				cp.Secure = c.Secure
				cp.HTTPOnly = c.HttpOnly
				params = append(params, cp)
			}
			return network.SetCookies(params).Do(ctx)
		}))
	}

	// --- inject style tags ---
	for _, tag := range p.AddStyleTag {
		if tag.URL != "" {
			tagURL := tag.URL
			actions = append(actions, chromedp.ActionFunc(func(ctx context.Context) error {
				script := fmt.Sprintf(
					`(function(){var l=document.createElement('link');l.rel='stylesheet';l.href=%q;document.head.appendChild(l)})()`,
					tagURL,
				)
				return chromedp.Evaluate(script, nil).Do(ctx)
			}))
		} else if tag.Content != "" {
			css := tag.Content
			actions = append(actions, chromedp.ActionFunc(func(ctx context.Context) error {
				script := fmt.Sprintf(
					`(function(){var s=document.createElement('style');s.textContent=%q;document.head.appendChild(s)})()`,
					css,
				)
				return chromedp.Evaluate(script, nil).Do(ctx)
			}))
		}
	}

	// --- inject script tags ---
	for _, tag := range p.AddScriptTag {
		if tag.URL != "" {
			tagURL := tag.URL
			actions = append(actions, chromedp.ActionFunc(func(ctx context.Context) error {
				script := fmt.Sprintf(
					`(function(){var s=document.createElement('script');s.src=%q;document.head.appendChild(s)})()`,
					tagURL,
				)
				return chromedp.Evaluate(script, nil).Do(ctx)
			}))
		} else if tag.Content != "" {
			content := tag.Content
			actions = append(actions, chromedp.ActionFunc(func(ctx context.Context) error {
				return chromedp.Evaluate(content, nil).Do(ctx)
			}))
		}
	}

	// --- waitForSelector ---
	if p.WaitForSelector != nil && p.WaitForSelector.Selector != "" {
		sel := p.WaitForSelector.Selector
		vis := p.WaitForSelector.Visible
		ms := p.WaitForSelector.Timeout
		if ms <= 0 {
			ms = 30000
		}
		if ms > 60000 {
			ms = 60000
		}
		d := time.Duration(ms) * time.Millisecond
		actions = append(actions, chromedp.ActionFunc(func(ctx context.Context) error {
			ctx2, cancel := context.WithTimeout(ctx, d)
			defer cancel()
			if vis {
				return chromedp.WaitVisible(sel, chromedp.ByQuery).Do(ctx2)
			}
			return chromedp.WaitReady(sel, chromedp.ByQuery).Do(ctx2)
		}))
	}

	// --- waitForTimeout ---
	if p.WaitForTimeout != nil && *p.WaitForTimeout > 0 {
		ms := *p.WaitForTimeout
		if ms > 60000 {
			ms = 60000
		}
		d := time.Duration(ms) * time.Millisecond
		actions = append(actions, chromedp.ActionFunc(func(ctx context.Context) error {
			select {
			case <-time.After(d):
			case <-ctx.Done():
			}
			return nil
		}))
	}

	return actions, nil
}

func waitAction(waitUntil string) chromedp.Action {
	switch waitUntil {
	case "domcontentloaded":
		return chromedp.WaitReady("body", chromedp.ByQuery)
	case "networkidle0", "networkidle2":
		return chromedp.ActionFunc(func(ctx context.Context) error {
			if err := chromedp.WaitReady("body", chromedp.ByQuery).Do(ctx); err != nil {
				return err
			}
			select {
			case <-time.After(1500 * time.Millisecond):
			case <-ctx.Done():
			}
			return nil
		})
	default: // "load"
		return chromedp.WaitReady("body", chromedp.ByQuery)
	}
}

// resourceTypePatterns maps abstract resource type names to URLPattern globs
// recognised by Chrome's network blocker.
func resourceTypePatterns(types []string) []string {
	m := map[string][]string{
		"image":      {"*.jpg", "*.jpeg", "*.png", "*.gif", "*.webp", "*.svg", "*.ico", "*.avif"},
		"stylesheet": {"*.css"},
		"font":       {"*.woff", "*.woff2", "*.ttf", "*.eot", "*.otf"},
		"media":      {"*.mp4", "*.mp3", "*.avi", "*.wav", "*.ogg", "*.webm", "*.flac"},
		"script":     {"*.js", "*.mjs"},
	}
	var patterns []string
	for _, t := range types {
		if p, ok := m[strings.ToLower(t)]; ok {
			patterns = append(patterns, p...)
		}
	}
	return patterns
}

// normaliseResourceTypes lowercases resource type names for comparison against
// CDP's network.ResourceType values.
func normaliseResourceTypes(types []string) []string {
	out := make([]string, 0, len(types))
	for _, t := range types {
		out = append(out, strings.ToLower(t))
	}
	return out
}

// cssToInches converts a CSS length string to inches for PDF margin params.
func cssToInches(css string) float64 {
	css = strings.TrimSpace(css)
	var v float64
	switch {
	case strings.HasSuffix(css, "cm"):
		fmt.Sscanf(strings.TrimSuffix(css, "cm"), "%f", &v)
		return v * 0.3937
	case strings.HasSuffix(css, "mm"):
		fmt.Sscanf(strings.TrimSuffix(css, "mm"), "%f", &v)
		return v * 0.03937
	case strings.HasSuffix(css, "in"):
		fmt.Sscanf(strings.TrimSuffix(css, "in"), "%f", &v)
		return v
	case strings.HasSuffix(css, "px"):
		fmt.Sscanf(strings.TrimSuffix(css, "px"), "%f", &v)
		return v / 96.0
	default:
		fmt.Sscanf(css, "%f", &v)
		return v / 96.0
	}
}

// paperFormatInches returns width, height in inches for common paper formats.
func paperFormatInches(format string) (float64, float64) {
	switch strings.ToUpper(format) {
	case "A3":
		return 11.69, 16.54
	case "A4":
		return 8.27, 11.69
	case "A5":
		return 5.83, 8.27
	case "LEGAL":
		return 8.5, 14
	case "TABLOID", "LEDGER":
		return 11, 17
	default: // Letter
		return 8.5, 11
	}
}
