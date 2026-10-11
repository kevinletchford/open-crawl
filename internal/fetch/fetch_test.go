package fetch

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestThrottleCapsConcurrencyAndSpacesStarts(t *testing.T) {
	th := NewThrottle(2, 20*time.Millisecond)
	var inFlight, peak atomic.Int32
	var mu sync.Mutex
	var starts []time.Time
	var wg sync.WaitGroup
	for range 5 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			release, err := th.Acquire(context.Background())
			if err != nil {
				t.Error(err)
				return
			}
			defer release()
			mu.Lock()
			starts = append(starts, time.Now())
			mu.Unlock()
			n := inFlight.Add(1)
			for {
				p := peak.Load()
				if n <= p || peak.CompareAndSwap(p, n) {
					break
				}
			}
			time.Sleep(5 * time.Millisecond)
			inFlight.Add(-1)
		}()
	}
	wg.Wait()
	if peak.Load() > 2 {
		t.Errorf("peak concurrency %d, want <= 2", peak.Load())
	}
	if span := starts[len(starts)-1].Sub(starts[0]); span < 70*time.Millisecond {
		t.Errorf("5 starts spanned %v; with a 20ms gap they need at least 80ms", span)
	}
}

func TestGetSendsUserAgentFollowsRedirectsAndCapsBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/old":
			http.Redirect(w, r, "/new", http.StatusMovedPermanently)
		case "/new":
			fmt.Fprint(w, r.UserAgent()+strings.Repeat("x", 100))
		}
	}))
	defer srv.Close()

	c := New(Options{Delay: -1, MaxBody: 20})
	res := c.Get(context.Background(), srv.URL+"/old", "TestBot/1.0")
	if !res.OK() || res.FinalURL != srv.URL+"/new" || len(res.Redirects) != 1 {
		t.Fatalf("res = %+v", res)
	}
	if !strings.HasPrefix(string(res.Body), "TestBot/1.0") || len(res.Body) != 20 || !res.Truncated {
		t.Errorf("body = %q (truncated %v)", res.Body, res.Truncated)
	}
}
