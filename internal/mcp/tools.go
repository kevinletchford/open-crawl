package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/kevinletchford/open-crawl/internal/crawler"
	"github.com/kevinletchford/open-crawl/internal/db"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Tool input/output types

type CrawlURLInput struct {
	URL         string `json:"url" jsonschema:"The URL to start crawling from"`
	MaxRequests int64  `json:"max_requests,omitempty" jsonschema:"Maximum number of requests to make (default 1000)"`
	Concurrency int    `json:"concurrency,omitempty" jsonschema:"Number of concurrent workers (default 100)"`
}

type CrawlURLOutput struct {
	Language       string   `json:"language"`
	Requests       int64    `json:"requests"`
	TimeTakenMs    int64    `json:"time_taken_ms"`
	ReqPerSec      float64  `json:"req_per_sec"`
	BytesRead      int64    `json:"bytes_read"`
	URLsDiscovered int64    `json:"urls_discovered"`
	SampleURLs     []string `json:"sample_urls,omitempty"`
}

type GetHistoryInput struct {
	TargetURL string `json:"target_url,omitempty" jsonschema:"Filter by target URL"`
	Language  string `json:"language,omitempty" jsonschema:"Filter by crawler language (e.g. Go)"`
	Limit     int    `json:"limit,omitempty" jsonschema:"Maximum number of results (default 50)"`
}

type GetHistoryOutput struct {
	Benchmarks []db.BenchmarkRecord `json:"benchmarks"`
	Total      int                  `json:"total"`
}

type GetStatisticsInput struct {
	TargetURL string `json:"target_url,omitempty" jsonschema:"Filter by target URL"`
	Language  string `json:"language,omitempty" jsonschema:"Filter by crawler language"`
}

type GetStatisticsOutput struct {
	ByLanguage map[string]db.LanguageStats `json:"by_language"`
	TotalRuns  int                         `json:"total_runs"`
}

type GenerateReportInput struct {
	TargetURL string `json:"target_url" jsonschema:"Target URL for the report"`
}

type GenerateReportOutput struct {
	Content string `json:"content"`
}

type CompareCrawlersInput struct {
	TargetURL   string `json:"target_url" jsonschema:"URL to crawl"`
	MaxRequests int64  `json:"max_requests,omitempty" jsonschema:"Max requests per run (default 1000)"`
}

type CompareCrawlersOutput struct {
	CurrentRun       CrawlURLOutput     `json:"current_run"`
	HistoricalAvg    float64            `json:"historical_avg"`
	ComparisonPct    float64            `json:"comparison_percent"`
	ComparisonStatus string             `json:"comparison_status"`
}

func (s *Server) registerTools() {
	// Tool: crawl_url
	mcp.AddTool(s.mcpServer, &mcp.Tool{
		Name:        "crawl_url",
		Description: "Crawl a URL using the Go web crawler and return benchmark results including requests per second, bytes read, and URLs discovered.",
	}, s.handleCrawlURL)

	// Tool: get_benchmark_history
	mcp.AddTool(s.mcpServer, &mcp.Tool{
		Name:        "get_benchmark_history",
		Description: "Retrieve historical benchmark results from the database with optional filtering by URL and language.",
	}, s.handleGetHistory)

	// Tool: get_benchmark_statistics
	mcp.AddTool(s.mcpServer, &mcp.Tool{
		Name:        "get_benchmark_statistics",
		Description: "Get aggregated benchmark statistics including average, min, and max requests per second grouped by crawler language.",
	}, s.handleGetStatistics)

	// Tool: generate_report
	mcp.AddTool(s.mcpServer, &mcp.Tool{
		Name:        "generate_report",
		Description: "Generate a markdown benchmark report for a specific target URL showing historical performance data.",
	}, s.handleGenerateReport)

	// Tool: compare_crawlers
	mcp.AddTool(s.mcpServer, &mcp.Tool{
		Name:        "compare_crawlers",
		Description: "Run a crawl on a URL and compare the results to historical average performance.",
	}, s.handleCompareCrawlers)
}

func (s *Server) handleCrawlURL(ctx context.Context, req *mcp.CallToolRequest, input CrawlURLInput) (*mcp.CallToolResult, CrawlURLOutput, error) {
	// Apply defaults
	if input.MaxRequests == 0 {
		input.MaxRequests = 1000
	}
	if input.Concurrency == 0 {
		input.Concurrency = 100
	}

	// Normalize URL
	url := input.URL
	if !strings.HasPrefix(url, "http://") && !strings.HasPrefix(url, "https://") {
		url = "https://" + url
	}

	// Run the crawler
	result, err := crawler.Crawl(ctx, crawler.Config{
		URL:         url,
		MaxRequests: input.MaxRequests,
		Concurrency: input.Concurrency,
	}, nil)
	if err != nil {
		return nil, CrawlURLOutput{}, fmt.Errorf("crawl failed: %w", err)
	}

	// Save to database
	if s.db != nil {
		s.db.SaveBenchmark(url, result.Language, result.Requests, result.TimeTakenMs, result.ReqPerSec, result.BytesRead)
	}

	output := CrawlURLOutput{
		Language:       result.Language,
		Requests:       result.Requests,
		TimeTakenMs:    result.TimeTakenMs,
		ReqPerSec:      result.ReqPerSec,
		BytesRead:      result.BytesRead,
		URLsDiscovered: result.URLsDiscovered,
		SampleURLs:     result.SampleURLs,
	}

	// Format result text
	text := fmt.Sprintf("Crawl completed!\n\nTarget: %s\nRequests: %d\nTime: %dms\nRequests/sec: %.2f\nBytes read: %d\nURLs discovered: %d",
		url, output.Requests, output.TimeTakenMs, output.ReqPerSec, output.BytesRead, output.URLsDiscovered)

	return &mcp.CallToolResult{
		Content: []mcp.Content{
			&mcp.TextContent{Text: text},
		},
	}, output, nil
}

func (s *Server) handleGetHistory(ctx context.Context, req *mcp.CallToolRequest, input GetHistoryInput) (*mcp.CallToolResult, GetHistoryOutput, error) {
	if s.db == nil {
		return nil, GetHistoryOutput{}, fmt.Errorf("database not initialized")
	}

	limit := input.Limit
	if limit == 0 {
		limit = 50
	}

	records, err := s.db.GetHistory(input.TargetURL, input.Language, limit)
	if err != nil {
		return nil, GetHistoryOutput{}, fmt.Errorf("failed to get history: %w", err)
	}

	output := GetHistoryOutput{
		Benchmarks: records,
		Total:      len(records),
	}

	// Format as JSON for readability
	jsonBytes, _ := json.MarshalIndent(output, "", "  ")
	text := fmt.Sprintf("Found %d benchmark records:\n\n%s", len(records), string(jsonBytes))

	return &mcp.CallToolResult{
		Content: []mcp.Content{
			&mcp.TextContent{Text: text},
		},
	}, output, nil
}

func (s *Server) handleGetStatistics(ctx context.Context, req *mcp.CallToolRequest, input GetStatisticsInput) (*mcp.CallToolResult, GetStatisticsOutput, error) {
	if s.db == nil {
		return nil, GetStatisticsOutput{}, fmt.Errorf("database not initialized")
	}

	stats, err := s.db.GetStatistics(input.TargetURL, input.Language)
	if err != nil {
		return nil, GetStatisticsOutput{}, fmt.Errorf("failed to get statistics: %w", err)
	}

	totalRuns := 0
	for _, st := range stats {
		totalRuns += st.RunCount
	}

	output := GetStatisticsOutput{
		ByLanguage: stats,
		TotalRuns:  totalRuns,
	}

	// Format statistics text
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Benchmark Statistics (%d total runs)\n\n", totalRuns))

	for lang, st := range stats {
		sb.WriteString(fmt.Sprintf("## %s\n", lang))
		sb.WriteString(fmt.Sprintf("- Runs: %d\n", st.RunCount))
		sb.WriteString(fmt.Sprintf("- Avg Req/s: %.2f\n", st.AvgReqPerSec))
		sb.WriteString(fmt.Sprintf("- Max Req/s: %.2f\n", st.MaxReqPerSec))
		sb.WriteString(fmt.Sprintf("- Min Req/s: %.2f\n", st.MinReqPerSec))
		sb.WriteString(fmt.Sprintf("- Total Requests: %d\n", st.TotalRequests))
		sb.WriteString(fmt.Sprintf("- Total Bytes: %d\n\n", st.TotalBytes))
	}

	return &mcp.CallToolResult{
		Content: []mcp.Content{
			&mcp.TextContent{Text: sb.String()},
		},
	}, output, nil
}

func (s *Server) handleGenerateReport(ctx context.Context, req *mcp.CallToolRequest, input GenerateReportInput) (*mcp.CallToolResult, GenerateReportOutput, error) {
	if s.db == nil {
		return nil, GenerateReportOutput{}, fmt.Errorf("database not initialized")
	}

	// Get history for this URL
	records, err := s.db.GetHistory(input.TargetURL, "", 100)
	if err != nil {
		return nil, GenerateReportOutput{}, fmt.Errorf("failed to get history: %w", err)
	}

	// Get statistics
	stats, err := s.db.GetStatistics(input.TargetURL, "")
	if err != nil {
		return nil, GenerateReportOutput{}, fmt.Errorf("failed to get statistics: %w", err)
	}

	var sb strings.Builder
	sb.WriteString("# Open-Crawl Benchmark Report\n\n")
	sb.WriteString(fmt.Sprintf("**Generated:** %s\n", time.Now().Format(time.RFC1123)))
	sb.WriteString(fmt.Sprintf("**Target URL:** %s\n\n", input.TargetURL))

	sb.WriteString("## Summary Statistics\n\n")
	sb.WriteString("| Language | Runs | Avg Req/s | Max Req/s | Min Req/s |\n")
	sb.WriteString("|----------|------|-----------|-----------|----------|\n")

	for lang, st := range stats {
		sb.WriteString(fmt.Sprintf("| %s | %d | %.2f | %.2f | %.2f |\n",
			lang, st.RunCount, st.AvgReqPerSec, st.MaxReqPerSec, st.MinReqPerSec))
	}

	sb.WriteString("\n## Recent Benchmarks\n\n")
	sb.WriteString("| Date | Language | Requests | Time (ms) | Req/s |\n")
	sb.WriteString("|------|----------|----------|-----------|-------|\n")

	for i, r := range records {
		if i >= 20 {
			sb.WriteString(fmt.Sprintf("\n... and %d more records\n", len(records)-20))
			break
		}
		sb.WriteString(fmt.Sprintf("| %s | %s | %d | %d | %.2f |\n",
			r.Timestamp.Format("2006-01-02 15:04"), r.Language, r.Requests, r.TimeTakenMs, r.ReqPerSec))
	}

	output := GenerateReportOutput{
		Content: sb.String(),
	}

	return &mcp.CallToolResult{
		Content: []mcp.Content{
			&mcp.TextContent{Text: output.Content},
		},
	}, output, nil
}

func (s *Server) handleCompareCrawlers(ctx context.Context, req *mcp.CallToolRequest, input CompareCrawlersInput) (*mcp.CallToolResult, CompareCrawlersOutput, error) {
	// Apply defaults
	if input.MaxRequests == 0 {
		input.MaxRequests = 1000
	}

	// Normalize URL
	url := input.TargetURL
	if !strings.HasPrefix(url, "http://") && !strings.HasPrefix(url, "https://") {
		url = "https://" + url
	}

	// Get historical average first
	var historicalAvg float64
	if s.db != nil {
		avg, err := s.db.GetHistoricalAverage(url, "Go")
		if err == nil {
			historicalAvg = avg
		}
	}

	// Run the crawler
	result, err := crawler.Crawl(ctx, crawler.Config{
		URL:         url,
		MaxRequests: input.MaxRequests,
		Concurrency: 100,
	}, nil)
	if err != nil {
		return nil, CompareCrawlersOutput{}, fmt.Errorf("crawl failed: %w", err)
	}

	// Save to database
	if s.db != nil {
		s.db.SaveBenchmark(url, result.Language, result.Requests, result.TimeTakenMs, result.ReqPerSec, result.BytesRead)
	}

	currentRun := CrawlURLOutput{
		Language:       result.Language,
		Requests:       result.Requests,
		TimeTakenMs:    result.TimeTakenMs,
		ReqPerSec:      result.ReqPerSec,
		BytesRead:      result.BytesRead,
		URLsDiscovered: result.URLsDiscovered,
		SampleURLs:     result.SampleURLs,
	}

	// Calculate comparison
	var comparisonPct float64
	comparisonStatus := "No historical data"

	if historicalAvg > 0 {
		comparisonPct = ((result.ReqPerSec - historicalAvg) / historicalAvg) * 100
		if comparisonPct > 0 {
			comparisonStatus = fmt.Sprintf("+%.2f%% faster than average", comparisonPct)
		} else if comparisonPct < 0 {
			comparisonStatus = fmt.Sprintf("%.2f%% slower than average", comparisonPct)
		} else {
			comparisonStatus = "Same as average"
		}
	}

	output := CompareCrawlersOutput{
		CurrentRun:       currentRun,
		HistoricalAvg:    historicalAvg,
		ComparisonPct:    comparisonPct,
		ComparisonStatus: comparisonStatus,
	}

	// Format result text
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("# Crawl Comparison Report\n\n"))
	sb.WriteString(fmt.Sprintf("**Target:** %s\n\n", url))
	sb.WriteString(fmt.Sprintf("## Current Run\n"))
	sb.WriteString(fmt.Sprintf("- Requests: %d\n", currentRun.Requests))
	sb.WriteString(fmt.Sprintf("- Time: %dms\n", currentRun.TimeTakenMs))
	sb.WriteString(fmt.Sprintf("- Requests/sec: %.2f\n\n", currentRun.ReqPerSec))
	sb.WriteString(fmt.Sprintf("## Comparison\n"))
	sb.WriteString(fmt.Sprintf("- Historical Average: %.2f req/s\n", historicalAvg))
	sb.WriteString(fmt.Sprintf("- Status: %s\n", comparisonStatus))

	return &mcp.CallToolResult{
		Content: []mcp.Content{
			&mcp.TextContent{Text: sb.String()},
		},
	}, output, nil
}
