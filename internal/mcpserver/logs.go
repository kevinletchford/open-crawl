package mcpserver

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/kevinletchford/open-crawl/internal/agents"
	"github.com/kevinletchford/open-crawl/internal/logs"
)

type AnalyzeLogsIn struct {
	Paths  []string `json:"paths" jsonschema:"log files on this machine; ~ is expanded and .gz files are decompressed"`
	Site   string   `json:"site,omitempty" jsonschema:"the site the logs are from, e.g. example.com; its robots.txt is used to flag fetches of disallowed paths"`
	Format string   `json:"format,omitempty" jsonschema:"auto (default), combined (nginx/Apache) or json (Cloudflare Logpush, Vercel, generic)"`
	Agents []string `json:"agents,omitempty" jsonschema:"agents to report (default: all)"`
	// NoVerify skips downloading the IP range lists, e.g. when offline.
	NoVerify bool `json:"no_verify,omitempty" jsonschema:"skip verification (offline); every request is then counted as unknown"`
}

type AnalyzeLogsOut struct {
	Report *logs.Report `json:"report"`
	Note   string       `json:"note"`
}

const logsNote = "Counts are exact for the lines in these files. \"verified\" means the request came from the operator's published IP ranges or passed its reverse-DNS check; \"unverified\" requests named the agent but failed that check (probably spoofed) and are left out of the other counts; \"unknown\" means the operator publishes no way to verify. The logs show what was fetched and served, not whether it was used for training, ranking or answers."

func (s *Server) analyzeLogs(ctx context.Context, req *mcp.CallToolRequest, in AnalyzeLogsIn) (*mcp.CallToolResult, AnalyzeLogsOut, error) {
	if len(in.Paths) == 0 {
		return nil, AnalyzeLogsOut{}, errors.New("paths is required")
	}
	files := make([]string, 0, len(in.Paths))
	for _, p := range in.Paths {
		p, err := expandPath(p)
		if err != nil {
			return nil, AnalyzeLogsOut{}, err
		}
		files = append(files, p)
	}
	list, err := agents.Select(in.Agents)
	if err != nil {
		return nil, AnalyzeLogsOut{}, err
	}
	var token any
	if req != nil && req.Params != nil {
		token = req.Params.GetProgressToken()
	}
	step := 0
	r, err := s.analyze(ctx, files, logs.Options{
		Format: logs.Format(in.Format), Agents: list, Site: in.Site,
		Verify: !in.NoVerify, Resolver: net.DefaultResolver,
		// Stdin stays nil: it carries the MCP protocol.
		Progress: func(msg string) {
			if token == nil || req.Session == nil {
				return
			}
			step++
			_ = req.Session.NotifyProgress(ctx, &mcp.ProgressNotificationParams{ProgressToken: token, Progress: float64(step), Message: msg})
		},
	})
	if err != nil {
		return nil, AnalyzeLogsOut{}, err
	}
	return nil, AnalyzeLogsOut{Report: r, Note: logsNote}, nil
}

// expandPath resolves ~ and makes p absolute, and refuses stdin and
// directories.
func expandPath(p string) (string, error) {
	p = strings.TrimSpace(p)
	if p == "" || p == "-" {
		return "", fmt.Errorf("give a log file path, not %q", p)
	}
	if p == "~" || strings.HasPrefix(p, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		p = filepath.Join(home, p[1:])
	}
	p, err := filepath.Abs(p)
	if err != nil {
		return "", err
	}
	fi, err := os.Stat(p)
	if err != nil {
		return "", err
	}
	if fi.IsDir() {
		return "", fmt.Errorf("%s is a directory; list the log files in it", p)
	}
	return p, nil
}
