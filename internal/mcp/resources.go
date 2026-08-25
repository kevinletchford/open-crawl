package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func (s *Server) registerResources() {
	// Resource: benchmark://history
	s.mcpServer.AddResource(&mcp.Resource{
		URI:         "benchmark://history",
		Name:        "Benchmark History",
		Description: "Complete benchmark history from SQLite database",
		MIMEType:    "application/json",
	}, s.handleHistoryResource)

	// Resource: benchmark://urls
	s.mcpServer.AddResource(&mcp.Resource{
		URI:         "benchmark://urls",
		Name:        "URL History",
		Description: "Recent URLs that have been crawled (from history.json)",
		MIMEType:    "application/json",
	}, s.handleURLsResource)

	// Resource: benchmark://config
	s.mcpServer.AddResource(&mcp.Resource{
		URI:         "benchmark://config",
		Name:        "Server Configuration",
		Description: "Current MCP server configuration and capabilities",
		MIMEType:    "application/json",
	}, s.handleConfigResource)

	// Resource template: benchmark://reports/{filename}
	s.mcpServer.AddResourceTemplate(&mcp.ResourceTemplate{
		URITemplate: "benchmark://reports/{filename}",
		Name:        "Benchmark Report",
		Description: "Access a specific benchmark report by filename",
		MIMEType:    "text/markdown",
	}, s.handleReportResource)
}

func (s *Server) handleHistoryResource(ctx context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
	if s.db == nil {
		return nil, fmt.Errorf("database not initialized")
	}

	records, err := s.db.GetHistory("", "", 100)
	if err != nil {
		return nil, fmt.Errorf("failed to get history: %w", err)
	}

	jsonBytes, err := json.MarshalIndent(records, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("failed to marshal history: %w", err)
	}

	return &mcp.ReadResourceResult{
		Contents: []*mcp.ResourceContents{
			{
				URI:      "benchmark://history",
				MIMEType: "application/json",
				Text:     string(jsonBytes),
			},
		},
	}, nil
}

func (s *Server) handleURLsResource(ctx context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
	historyPath := "./exports/history.json"
	data, err := os.ReadFile(historyPath)
	if err != nil {
		// Return empty array if file doesn't exist
		data = []byte("[]")
	}

	return &mcp.ReadResourceResult{
		Contents: []*mcp.ResourceContents{
			{
				URI:      "benchmark://urls",
				MIMEType: "application/json",
				Text:     string(data),
			},
		},
	}, nil
}

func (s *Server) handleConfigResource(ctx context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
	config := map[string]interface{}{
		"name":    "open-crawl",
		"version": "1.0.0",
		"tools": []string{
			"crawl_url",
			"get_benchmark_history",
			"get_benchmark_statistics",
			"generate_report",
			"compare_crawlers",
		},
		"crawler": map[string]interface{}{
			"language":             "Go",
			"default_concurrency":  100,
			"default_max_requests": 1000,
			"timeout":              "5s",
		},
		"database": map[string]interface{}{
			"type": "sqlite",
			"path": "./exports/benchmarks.db",
		},
	}

	jsonBytes, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("failed to marshal config: %w", err)
	}

	return &mcp.ReadResourceResult{
		Contents: []*mcp.ResourceContents{
			{
				URI:      "benchmark://config",
				MIMEType: "application/json",
				Text:     string(jsonBytes),
			},
		},
	}, nil
}

func (s *Server) handleReportResource(ctx context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
	// Extract filename from URI: benchmark://reports/{filename}
	uri := req.Params.URI
	filename := strings.TrimPrefix(uri, "benchmark://reports/")

	if filename == "" {
		// List available reports
		reports, err := listReports()
		if err != nil {
			return nil, fmt.Errorf("failed to list reports: %w", err)
		}

		jsonBytes, _ := json.MarshalIndent(reports, "", "  ")
		return &mcp.ReadResourceResult{
			Contents: []*mcp.ResourceContents{
				{
					URI:      uri,
					MIMEType: "application/json",
					Text:     string(jsonBytes),
				},
			},
		}, nil
	}

	// Read specific report
	reportPath := filepath.Join("./exports", filename)
	if !strings.HasSuffix(reportPath, ".md") {
		reportPath += ".md"
	}

	data, err := os.ReadFile(reportPath)
	if err != nil {
		return nil, fmt.Errorf("report not found: %s", filename)
	}

	return &mcp.ReadResourceResult{
		Contents: []*mcp.ResourceContents{
			{
				URI:      uri,
				MIMEType: "text/markdown",
				Text:     string(data),
			},
		},
	}, nil
}

func listReports() ([]string, error) {
	var reports []string

	entries, err := os.ReadDir("./exports")
	if err != nil {
		return nil, err
	}

	for _, entry := range entries {
		if !entry.IsDir() && strings.HasPrefix(entry.Name(), "report_") && strings.HasSuffix(entry.Name(), ".md") {
			reports = append(reports, entry.Name())
		}
	}

	return reports, nil
}
