// Package mcpconfig registers the opencrawl MCP server with AI clients:
// Claude Code (through its CLI), and Claude Desktop and Cursor (by editing
// their JSON config files).
package mcpconfig

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
)

// Server is the MCP server entry to register.
type Server struct {
	Name    string   // key in the client's config, e.g. "opencrawl"
	Command string   // absolute path to the binary
	Args    []string // e.g. ["mcp"]
}

// Status says what Install did.
type Status string

const (
	Added     Status = "added"
	Updated   Status = "updated"   // replaced a different entry (with force)
	Unchanged Status = "unchanged" // the same entry was already there
	Exists    Status = "exists"    // a different entry is there; not replaced without force
)

// Client is an AI tool that can run MCP servers.
type Client struct {
	ID    string // claude-code, claude-desktop, cursor
	Label string
	// ConfigPath is the JSON file holding "mcpServers", for clients
	// configured by file. Empty for Claude Code.
	ConfigPath string
	// Note is shown after installing, e.g. "restart Claude Desktop".
	Note string
}

// Clients lists the clients this package knows, with config paths for
// this machine. home and configDir are os.UserHomeDir and os.UserConfigDir.
func Clients(home, configDir string) []Client {
	return []Client{
		{ID: "claude-code", Label: "Claude Code", Note: "Added at user scope, so it's available in every project."},
		{ID: "claude-desktop", Label: "Claude Desktop",
			ConfigPath: filepath.Join(configDir, "Claude", "claude_desktop_config.json"),
			Note:       "Quit and reopen Claude Desktop to load it."},
		{ID: "cursor", Label: "Cursor", ConfigPath: filepath.Join(home, ".cursor", "mcp.json"),
			Note: "Cursor picks it up from Settings → MCP; you may need to enable it there."},
	}
}

// Runner runs a command and returns its combined output. exec.Command is the
// default; tests replace it.
type Runner func(ctx context.Context, name string, args ...string) ([]byte, error)

func execRunner(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).CombinedOutput()
}

// Installer adds a server to clients.
type Installer struct {
	Run      Runner                       // default: exec
	LookPath func(string) (string, error) // default: exec.LookPath
}

func (in Installer) run() Runner {
	if in.Run != nil {
		return in.Run
	}
	return execRunner
}

func (in Installer) lookPath(name string) (string, error) {
	if in.LookPath != nil {
		return in.LookPath(name)
	}
	return exec.LookPath(name)
}

// Detected reports whether the client appears to be installed: its CLI is
// on PATH, or its config directory exists.
func (in Installer) Detected(c Client) bool {
	if c.ID == "claude-code" {
		_, err := in.lookPath("claude")
		return err == nil
	}
	_, err := os.Stat(filepath.Dir(c.ConfigPath))
	return err == nil
}

// Configured reports whether the client already has an entry named name.
func (in Installer) Configured(ctx context.Context, c Client, name string) (bool, error) {
	if c.ID == "claude-code" {
		if _, err := in.lookPath("claude"); err != nil {
			return false, err
		}
		_, err := in.run()(ctx, "claude", "mcp", "get", name)
		return err == nil, nil
	}
	cfg, err := readConfig(c.ConfigPath)
	if err != nil {
		return false, err
	}
	servers, _ := cfg["mcpServers"].(map[string]any)
	_, ok := servers[name]
	return ok, nil
}

// Install adds s to c. An existing, different entry with the same name is
// only replaced when force is set. With dryRun nothing is changed.
func (in Installer) Install(ctx context.Context, c Client, s Server, force, dryRun bool) (Status, error) {
	if c.ID == "claude-code" {
		return in.installClaudeCode(ctx, s, force, dryRun)
	}
	return installFile(c.ConfigPath, s, force, dryRun)
}

func (in Installer) installClaudeCode(ctx context.Context, s Server, force, dryRun bool) (Status, error) {
	if _, err := in.lookPath("claude"); err != nil {
		return "", errors.New("the claude CLI isn't on PATH")
	}
	run := in.run()
	status := Added
	if _, err := run(ctx, "claude", "mcp", "get", s.Name); err == nil {
		if !force {
			return Exists, nil
		}
		status = Updated
		if !dryRun {
			if out, err := run(ctx, "claude", "mcp", "remove", "--scope", "user", s.Name); err != nil {
				return "", fmt.Errorf("claude mcp remove: %v: %s", err, strings.TrimSpace(string(out)))
			}
		}
	}
	if dryRun {
		return status, nil
	}
	args := append([]string{"mcp", "add", "--scope", "user", s.Name, "--", s.Command}, s.Args...)
	if out, err := run(ctx, "claude", args...); err != nil {
		return "", fmt.Errorf("claude mcp add: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return status, nil
}

// installFile adds s under "mcpServers" in a JSON config file, keeping
// everything else in it. The previous file is kept as path.bak.
func installFile(path string, s Server, force, dryRun bool) (Status, error) {
	cfg, err := readConfig(path)
	if err != nil {
		return "", err
	}
	servers, ok := cfg["mcpServers"].(map[string]any)
	if !ok {
		if cfg["mcpServers"] != nil {
			return "", fmt.Errorf("%s: mcpServers is not an object", path)
		}
		servers = map[string]any{}
	}
	entry := map[string]any{"command": s.Command, "args": toAny(s.Args)}

	status := Added
	if old, ok := servers[s.Name]; ok {
		if sameEntry(old, entry) {
			return Unchanged, nil
		}
		if !force {
			return Exists, nil
		}
		status = Updated
	}
	if dryRun {
		return status, nil
	}
	servers[s.Name] = entry
	cfg["mcpServers"] = servers
	return status, writeConfig(path, cfg)
}

// sameEntry compares command and args only, so other settings a user added
// to the entry don't count as a difference.
func sameEntry(old any, entry map[string]any) bool {
	m, ok := old.(map[string]any)
	if !ok {
		return false
	}
	return m["command"] == entry["command"] && reflect.DeepEqual(m["args"], entry["args"])
}

func toAny(ss []string) []any {
	out := make([]any, len(ss))
	for i, s := range ss {
		out[i] = s
	}
	return out
}

// readConfig reads a JSON object; a missing or empty file is an empty one.
func readConfig(path string) (map[string]any, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return map[string]any{}, nil
	}
	if err != nil {
		return nil, err
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return map[string]any{}, nil
	}
	var cfg map[string]any
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("%s isn't valid JSON, so it was left alone: %w", path, err)
	}
	if cfg == nil {
		cfg = map[string]any{}
	}
	return cfg, nil
}

// writeConfig backs up the existing file to path.bak and writes cfg through
// a temporary file, so a failed write can't leave a half-written config.
func writeConfig(path string, cfg map[string]any) error {
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	mode := fs.FileMode(0o644)
	if old, err := os.ReadFile(path); err == nil {
		if fi, err := os.Stat(path); err == nil {
			mode = fi.Mode().Perm()
		}
		if err := os.WriteFile(path+".bak", old, mode); err != nil {
			return fmt.Errorf("backing up %s: %w", path, err)
		}
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".opencrawl-*.json")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), mode); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
