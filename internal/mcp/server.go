package mcp

import (
	"context"

	"github.com/kevinletchford/open-crawl/internal/db"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Server wraps an MCP server with database access.
type Server struct {
	mcpServer *mcp.Server
	db        *db.SQLite
}

// NewServer creates a new MCP server for open-crawl.
func NewServer(database *db.SQLite) *Server {
	s := &Server{db: database}

	s.mcpServer = mcp.NewServer(&mcp.Implementation{
		Name:    "open-crawl",
		Version: "1.0.0",
	}, nil)

	// Register tools
	s.registerTools()

	// Register resources
	s.registerResources()

	return s
}

// Run starts the MCP server on the given transport.
func (s *Server) Run(ctx context.Context, transport mcp.Transport) error {
	return s.mcpServer.Run(ctx, transport)
}
