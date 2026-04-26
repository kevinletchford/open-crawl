package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type recentURLsQueue struct {
	mu   sync.Mutex
	urls []string
}

func (q *recentURLsQueue) Add(url string) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.urls = append(q.urls, url)
	if len(q.urls) > 20 {
		q.urls = q.urls[len(q.urls)-20:]
	}
}

func (q *recentURLsQueue) Get() []string {
	q.mu.Lock()
	defer q.mu.Unlock()
	copied := make([]string, len(q.urls))
	copy(copied, q.urls)
	return copied
}

// Output standard format for all crawlers
type BenchmarkResult struct {
	Language    string  `json:"language"`
	Requests    int64   `json:"requests"`
	TimeTakenMs int64   `json:"time_taken_ms"`
	ReqPerSec   float64 `json:"req_per_sec"`
	BytesRead   int64   `json:"bytes_read"`
}

var linkRegex = regexp.MustCompile(`href=["'](.*?)["']`)

func main() {
	targetURL := flag.String("url", "http://localhost:8080/page/1", "Target URL to crawl")
	maxReqs := flag.Int64("max", 10000, "Maximum number of requests")
	concurrency := flag.Int("c", 100, "Number of concurrent workers")
	flag.Parse()

	parsedURL, err := url.Parse(*targetURL)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Invalid target URL: %v\n", err)
		os.Exit(1)
	}
	baseURL := parsedURL.Scheme + "://" + parsedURL.Host

	// Crawl state
	var reqCount atomic.Int64
	var bytesRead atomic.Int64

	queue := make(chan string, *maxReqs*10)
	visited := sync.Map{}
	recentURLs := &recentURLsQueue{}

	queue <- *targetURL
	visited.Store(*targetURL, true)

	var wg sync.WaitGroup

	// Custom HTTP client for speed
	client := &http.Client{
		Transport: &http.Transport{
			MaxIdleConns:        *concurrency * 2,
			MaxIdleConnsPerHost: *concurrency * 2,
			IdleConnTimeout:     30 * time.Second,
			DisableCompression:  true, // Raw throughput testing
		},
		Timeout: 5 * time.Second,
	}

	start := time.Now()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var activeWorkers atomic.Int32

	for i := 0; i < *concurrency; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-ctx.Done():
					return
				case url, ok := <-queue:
					if !ok {
						return
					}

					activeWorkers.Add(1)

					// Stop if we hit max requests
					if reqCount.Load() >= *maxReqs {
						activeWorkers.Add(-1)
						return
					}

					resp, err := client.Get(url)
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
					recentURLs.Add(url)

					// Stop queuing if we are done
					if reqsDone >= *maxReqs {
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

	// Monitor to close queue when target hits or queue is exhausted
	monitorWg := sync.WaitGroup{}
	monitorWg.Add(1)
	go func() {
		defer monitorWg.Done()
		for {
			select {
			case <-ctx.Done():
				return
			default:
				current := reqCount.Load()
				visitedCount := 0
				visited.Range(func(key, value interface{}) bool {
					visitedCount++
					return true
				})
				recentData, _ := json.Marshal(recentURLs.Get())
				fmt.Fprintf(os.Stderr, "PROGRESS: %d | %d | %s\n", current, visitedCount, string(recentData))

				// Exit condition 1: reached max requests
				if current >= *maxReqs {
					cancel()
					return
				}

				// Exit condition 2: exhausted all URLs
				if activeWorkers.Load() == 0 && len(queue) == 0 {
					// wait a tiny bit to ensure no late additions
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
	cancel()         // Ensure everything is cancelled
	monitorWg.Wait() // wait for monitor to finish logging

	duration := time.Since(start)

	actualReqs := reqCount.Load()
	reqsPerSec := float64(actualReqs) / duration.Seconds()

	visitedFile, _ := os.Create("/tmp/crawl_urls.txt")
	visited.Range(func(key, value interface{}) bool {
		visitedFile.WriteString(fmt.Sprintf("%v\n", key))
		return true
	})
	visitedFile.Close()

	result := BenchmarkResult{
		Language:    "Go",
		Requests:    actualReqs,
		TimeTakenMs: duration.Milliseconds(),
		ReqPerSec:   reqsPerSec,
		BytesRead:   bytesRead.Load(),
	}

	// The ONLY thing printed to stdout must be the JSON result
	jsonOut, _ := json.Marshal(result)
	fmt.Println(string(jsonOut))
	os.Exit(0)
}
