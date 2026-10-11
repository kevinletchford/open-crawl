// Package fetch is the audit's HTTP client: one place for timeouts, redirect
// limits, body caps and politeness, so every request the tool sends behaves
// the same way.
package fetch

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"
)

// DefaultUserAgent identifies the audit's own requests.
const DefaultUserAgent = "OpenCrawlAudit/0.1 (+https://open-crawl.com)"

// BrowserUserAgent is an ordinary desktop Chrome, used as the baseline when
// probing whether a site treats AI user agents differently.
const BrowserUserAgent = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36"

// MaxRedirects matches RFC 9309's "at least five" for robots.txt and is
// reasonable for pages too.
const MaxRedirects = 5

// Response is the outcome of one GET. A transport failure is reported in Err
// rather than as a returned error, because to an audit a failed fetch is a
// result to record, not a reason to stop.
type Response struct {
	URL       string        `json:"url"`
	FinalURL  string        `json:"final_url"`
	Status    int           `json:"status"`
	Header    http.Header   `json:"-"`
	Body      []byte        `json:"-"`
	Truncated bool          `json:"truncated,omitempty"`
	Redirects []string      `json:"redirects,omitempty"`
	Duration  time.Duration `json:"duration_ns"`
	Err       error         `json:"-"`
}

// OK reports a 2xx response with no transport error.
func (r *Response) OK() bool { return r.Err == nil && r.Status >= 200 && r.Status < 300 }

// Client sends throttled GET requests.
type Client struct {
	http     *http.Client
	maxBody  int64
	throttle *Throttle
}

// Options configures a Client. Zero values get sensible defaults.
type Options struct {
	Timeout     time.Duration // per request, default 15s
	MaxBody     int64         // bytes read per response, default 5 MiB
	Concurrency int           // requests in flight, default 4
	Delay       time.Duration // minimum gap between request starts, default 250ms
}

// New returns a Client.
func New(o Options) *Client {
	if o.Timeout == 0 {
		o.Timeout = 15 * time.Second
	}
	if o.MaxBody == 0 {
		o.MaxBody = 5 << 20
	}
	if o.Concurrency <= 0 {
		o.Concurrency = 4
	}
	if o.Delay < 0 {
		o.Delay = 0
	}
	return &Client{
		http: &http.Client{
			Timeout: o.Timeout,
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				if len(via) > MaxRedirects {
					return http.ErrUseLastResponse
				}
				return nil
			},
		},
		maxBody:  o.MaxBody,
		throttle: NewThrottle(o.Concurrency, o.Delay),
	}
}

// Get fetches url with the given user agent. It waits its turn on the
// throttle, and returns early with ctx's error if ctx ends first.
func (c *Client) Get(ctx context.Context, url, userAgent string) *Response {
	return c.get(ctx, url, userAgent, c.maxBody)
}

// GetLimited is Get with a smaller body cap, for files such as robots.txt
// that have their own size limit.
func (c *Client) GetLimited(ctx context.Context, url, userAgent string, maxBody int64) *Response {
	return c.get(ctx, url, userAgent, maxBody)
}

func (c *Client) get(ctx context.Context, url, userAgent string, maxBody int64) *Response {
	res := &Response{URL: url, FinalURL: url}
	release, err := c.throttle.Acquire(ctx)
	if err != nil {
		res.Err = err
		return res
	}
	defer release()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		res.Err = err
		return res
	}
	if userAgent == "" {
		userAgent = DefaultUserAgent
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,text/plain;q=0.8,*/*;q=0.5")

	start := time.Now()
	resp, err := c.http.Do(req)
	res.Duration = time.Since(start)
	if err != nil {
		res.Err = err
		return res
	}
	defer resp.Body.Close()

	res.Status = resp.StatusCode
	res.Header = resp.Header
	res.FinalURL = resp.Request.URL.String()
	for r := resp.Request; r != nil && r.Response != nil; r = r.Response.Request {
		res.Redirects = append([]string{r.Response.Request.URL.String()}, res.Redirects...)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody+1))
	if int64(len(body)) > maxBody {
		body = body[:maxBody]
		res.Truncated = true
	}
	res.Body = body
	if err != nil && !errors.Is(err, io.EOF) {
		res.Err = fmt.Errorf("reading body: %w", err)
	}
	res.Duration = time.Since(start)
	return res
}

// Throttle caps requests in flight and spaces out their starts.
type Throttle struct {
	slots chan struct{}
	delay time.Duration

	mu   sync.Mutex
	next time.Time
}

// NewThrottle allows n concurrent holders, each starting at least delay
// after the previous one.
func NewThrottle(n int, delay time.Duration) *Throttle {
	return &Throttle{slots: make(chan struct{}, n), delay: delay}
}

// Acquire blocks until a slot is free and the delay has passed. Call the
// returned func to release the slot.
func (t *Throttle) Acquire(ctx context.Context) (func(), error) {
	select {
	case t.slots <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	release := func() { <-t.slots }

	t.mu.Lock()
	now := time.Now()
	start := t.next
	if start.Before(now) {
		start = now
	}
	t.next = start.Add(t.delay)
	t.mu.Unlock()

	if wait := time.Until(start); wait > 0 {
		timer := time.NewTimer(wait)
		defer timer.Stop()
		select {
		case <-timer.C:
		case <-ctx.Done():
			release()
			return nil, ctx.Err()
		}
	}
	return release, nil
}
