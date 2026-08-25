package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/kevinletchford/open-crawl/internal/db"
	mcpserver "github.com/kevinletchford/open-crawl/internal/mcp"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func main() {
	// Initialize database
	dbPath := "./exports/benchmarks.db"

	// Ensure exports directory exists
	if err := os.MkdirAll("./exports", 0755); err != nil {
		log.Fatalf("Failed to create exports directory: %v", err)
	}

	database, err := db.NewSQLite(dbPath)
	if err != nil {
		log.Fatalf("Failed to initialize database: %v", err)
	}
	defer database.Close()

	// Create MCP server
	server := mcpserver.NewServer(database)

	// Setup graceful shutdown
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigChan
		cancel()
	}()

	// Run server on stdio transport
	if err := server.Run(ctx, &mcp.StdioTransport{}); err != nil {
		log.Fatalf("Server error: %v", err)
	}
}
