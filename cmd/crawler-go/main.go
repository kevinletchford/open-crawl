package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"sync"
	"sync/atomic"
	"time"
)

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

	// Crawl state
	var reqCount atomic.Int64
	var bytesRead atomic.Int64

	queue := make(chan string, *maxReqs*10)
	visited := sync.Map{}

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

	for i := 0; i < *concurrency; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for url := range queue {
				// Stop if we hit max requests
				if reqCount.Load() >= *maxReqs {
					return
				}

				resp, err := client.Get(url)
				if err != nil {
					continue
				}

				body, err := io.ReadAll(resp.Body)
				resp.Body.Close()
				if err != nil {
					continue
				}

				// Increment counters
				bytesRead.Add(int64(len(body)))
				reqsDone := reqCount.Add(1)

				// Stop queuing if we are done
				if reqsDone >= *maxReqs {
					return
				}

				// Parse links (naively but fast)
				matches := linkRegex.FindAllSubmatch(body, -1)
				for _, match := range matches {
					link := string(match[1])

					// Resolve local paths back to the target origin
					if link[0] == '/' {
						link = "http://localhost:8080" + link
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
			}
		}()
	}

	// Monitor to close queue when target hits
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			current := reqCount.Load()
			fmt.Fprintf(os.Stderr, "PROGRESS: %d\n", current)
			if current >= *maxReqs {
				close(queue)
				return
			}
			time.Sleep(100 * time.Millisecond)
		}
	}()

	wg.Wait()
	duration := time.Since(start)

	actualReqs := reqCount.Load()
	reqsPerSec := float64(actualReqs) / duration.Seconds()

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
