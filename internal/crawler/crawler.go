package crawler

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

var linkRegex = regexp.MustCompile(`href=["'](.*?)["']`)

// Crawl performs a web crawl starting from the given URL.
// It respects the context for cancellation and calls progressCallback periodically if provided.
func Crawl(ctx context.Context, cfg Config, progressCallback ProgressCallback) (*Result, error) {
	// Apply defaults
	if cfg.MaxRequests == 0 {
		cfg.MaxRequests = 1000
	}
	if cfg.Concurrency == 0 {
		cfg.Concurrency = 100
	}

	parsedURL, err := url.Parse(cfg.URL)
	if err != nil {
		return nil, err
	}
	baseURL := parsedURL.Scheme + "://" + parsedURL.Host

	// Crawl state
	var reqCount atomic.Int64
	var bytesRead atomic.Int64
	var urlsDiscovered atomic.Int64

	queue := make(chan string, cfg.MaxRequests*10)
	visited := sync.Map{}
	recentURLs := &recentURLsQueue{}

	queue <- cfg.URL
	visited.Store(cfg.URL, true)

	var wg sync.WaitGroup

	// Custom HTTP client for speed
	client := &http.Client{
		Transport: &http.Transport{
			MaxIdleConns:        cfg.Concurrency * 2,
			MaxIdleConnsPerHost: cfg.Concurrency * 2,
			IdleConnTimeout:     30 * time.Second,
			DisableCompression:  true,
		},
		Timeout: 5 * time.Second,
	}

	start := time.Now()

	crawlCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	var activeWorkers atomic.Int32

	for i := 0; i < cfg.Concurrency; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-crawlCtx.Done():
					return
				case targetURL, ok := <-queue:
					if !ok {
						return
					}

					activeWorkers.Add(1)

					// Stop if we hit max requests
					if reqCount.Load() >= cfg.MaxRequests {
						activeWorkers.Add(-1)
						return
					}

					resp, err := client.Get(targetURL)
					if err != nil {
						activeWorkers.Add(-1)
						continue
					}

					body, err := io.ReadAll(resp.Body)
					resp.Body.Close()
					if err != nil {
						activeWorkers.Add(-1)
						continue
					}

					// Increment counters
					bytesRead.Add(int64(len(body)))
					reqsDone := reqCount.Add(1)
					recentURLs.Add(targetURL)

					// Stop queuing if we are done
					if reqsDone >= cfg.MaxRequests {
						cancel()
						activeWorkers.Add(-1)
						return
					}

					// Parse links (naively but fast)
					matches := linkRegex.FindAllSubmatch(body, -1)
					for _, match := range matches {
						link := string(match[1])

						// Resolve local paths back to the target origin
						if len(link) > 0 && link[0] == '/' {
							link = baseURL + link
						}

						// Restrict to same domain
						if !strings.HasPrefix(link, baseURL) {
							continue
						}

						// If not visited, add to queue
						if _, loaded := visited.LoadOrStore(link, true); !loaded {
							urlsDiscovered.Add(1)
							select {
							case queue <- link:
							default:
								// Queue full, drop link
							}
						}
					}

					activeWorkers.Add(-1)
				}
			}
		}()
	}

	// Monitor goroutine
	monitorWg := sync.WaitGroup{}
	monitorWg.Add(1)
	go func() {
		defer monitorWg.Done()
		for {
			select {
			case <-crawlCtx.Done():
				return
			default:
				current := reqCount.Load()
				discovered := urlsDiscovered.Load()

				// Call progress callback if provided
				if progressCallback != nil {
					progressCallback(current, discovered, recentURLs.Get())
				}

				// Exit condition 1: reached max requests
				if current >= cfg.MaxRequests {
					cancel()
					return
				}

				// Exit condition 2: exhausted all URLs
				if activeWorkers.Load() == 0 && len(queue) == 0 {
					time.Sleep(50 * time.Millisecond)
					if activeWorkers.Load() == 0 && len(queue) == 0 {
						cancel()
						return
					}
				}

				time.Sleep(100 * time.Millisecond)
			}
		}
	}()

	wg.Wait()
	cancel()
	monitorWg.Wait()

	duration := time.Since(start)

	actualReqs := reqCount.Load()
	reqsPerSec := float64(actualReqs) / duration.Seconds()

	return &Result{
		Language:       "Go",
		Requests:       actualReqs,
		TimeTakenMs:    duration.Milliseconds(),
		ReqPerSec:      reqsPerSec,
		BytesRead:      bytesRead.Load(),
		URLsDiscovered: urlsDiscovered.Load(),
		SampleURLs:     recentURLs.Get(),
	}, nil
}
