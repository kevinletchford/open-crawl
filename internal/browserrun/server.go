package browserrun

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
)

// Server is the Browser Run local server.
type Server struct {
	cfg         Config
	pool        *Pool
	store       *Store
	llm         *LLMClient
	crawlQueue  chan string
	cancelJobs  map[string]context.CancelFunc
	cancelJobMu sync.Mutex
	httpServer  *http.Server
}

// New constructs a Server and warms up the browser pool.
func New(ctx context.Context, cfg Config) (*Server, error) {
	store, err := NewStore(cfg.Storage.DBPath)
	if err != nil {
		return nil, fmt.Errorf("store: %w", err)
	}

	pool, err := NewPool(ctx, PoolConfig{
		Size:         cfg.Browser.PoolSize,
		WaitTimeout:  cfg.Browser.PoolWaitTimeout,
		ChromiumPath: cfg.Browser.ChromiumPath,
		Headless:     true,
	})
	if err != nil {
		store.Close()
		return nil, fmt.Errorf("browser pool: %w", err)
	}

	s := &Server{
		cfg:        cfg,
		pool:       pool,
		store:      store,
		llm:        newLLMClient(cfg.AI),
		crawlQueue: make(chan string, 1000),
		cancelJobs: make(map[string]context.CancelFunc),
	}

	s.httpServer = &http.Server{
		Addr:         fmt.Sprintf("%s:%d", cfg.Server.Host, cfg.Server.Port),
		Handler:      s.routes(),
		ReadTimeout:  5 * time.Minute,
		WriteTimeout: 5 * time.Minute,
		IdleTimeout:  120 * time.Second,
	}

	return s, nil
}

// Run starts the HTTP server and background workers.
// It blocks until ctx is cancelled, then shuts down gracefully.
func (s *Server) Run(ctx context.Context) error {
	s.startCrawlWorkers(ctx)
	s.startJanitor(ctx)

	slog.Info("browser-run server starting", "addr", s.httpServer.Addr)

	errCh := make(chan error, 1)
	go func() {
		if err := s.httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errCh <- err
		}
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
	}

	shutCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := s.httpServer.Shutdown(shutCtx); err != nil {
		slog.Error("shutdown error", "err", err)
	}

	s.pool.Close()
	s.store.Close()
	slog.Info("browser-run server stopped")
	return nil
}

func (s *Server) routes() http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(middleware.Recoverer)
	r.Use(s.authMiddleware)

	r.Get("/health", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"status":      "ok",
			"pool_active": s.pool.ActiveCount(),
			"pool_size":   s.pool.Size(),
		})
	})

	r.Get("/v1/stats", s.handleStats)

	r.Route("/v1/browser-rendering", func(r chi.Router) {
		r.Post("/screenshot", s.handleScreenshot)
		r.Post("/pdf", s.handlePDF)
		r.Post("/content", s.handleContent)
		r.Post("/markdown", s.handleMarkdown)
		r.Post("/snapshot", s.handleSnapshot)
		r.Post("/links", s.handleLinks)
		r.Post("/scrape", s.handleScrape)
		r.Post("/json", s.handleJSON)

		r.Post("/crawl", s.handleCrawlCreate)
		r.Get("/crawl/{jobID}", s.handleCrawlGet)
		r.Delete("/crawl/{jobID}", s.handleCrawlCancel)
	})

	return r
}

func (s *Server) handleStats(w http.ResponseWriter, r *http.Request) {
	recent, _ := s.store.RecentRequests(50)
	writeJSON(w, http.StatusOK, successResponse(map[string]interface{}{
		"pool": map[string]int{
			"active": s.pool.ActiveCount(),
			"size":   s.pool.Size(),
		},
		"recentRequests": recent,
	}))
}

// authMiddleware checks the Bearer token if one is configured.
func (s *Server) authMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.cfg.Server.AuthToken == "" {
			next.ServeHTTP(w, r)
			return
		}
		token := r.Header.Get("Authorization")
		expected := "Bearer " + s.cfg.Server.AuthToken
		if token != expected {
			writeJSON(w, http.StatusUnauthorized, errResponse(401, "unauthorized"))
			return
		}
		next.ServeHTTP(w, r)
	})
}

// startJanitor runs periodic cleanup of expired jobs and old request logs.
func (s *Server) startJanitor(ctx context.Context) {
	go func() {
		t := time.NewTicker(5 * time.Minute)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				if err := s.store.Purge(); err != nil {
					slog.Warn("janitor purge error", "err", err)
				}
			}
		}
	}()
}

// Handler returns the HTTP handler so tests can use httptest.NewServer
// without binding a port.
func (s *Server) Handler() http.Handler { return s.routes() }

// writeJSON writes v as JSON with the given status code.
func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

// suppress unused-import lint; fmt is used in pdf.go, screenshot.go, etc.
var _ = fmt.Sprint
