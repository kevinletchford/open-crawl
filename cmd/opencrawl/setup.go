package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strings"
	"time"

	"github.com/kevinletchford/open-crawl/internal/mcpconfig"
	"github.com/kevinletchford/open-crawl/internal/render"
	"github.com/kevinletchford/open-crawl/internal/store"
)

// version is set at release time with -ldflags "-X main.version=…".
var version = ""

func buildVersion() string {
	if version != "" {
		return version
	}
	if bi, ok := debug.ReadBuildInfo(); ok && bi.Main.Version != "" && bi.Main.Version != "(devel)" {
		return bi.Main.Version // go install …@v0.2.0
	}
	return "dev"
}

func runVersion() error {
	fmt.Printf("opencrawl %s (%s/%s, %s)\n", buildVersion(), runtime.GOOS, runtime.GOARCH, runtime.Version())
	return nil
}

// selfPath is the path MCP clients should run. It prefers the opencrawl on
// PATH when that is this binary, so a Homebrew symlink survives upgrades.
func selfPath() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	real, err := filepath.EvalSymlinks(exe)
	if err != nil {
		real = exe
	}
	if p, err := exec.LookPath("opencrawl"); err == nil {
		if rp, err := filepath.EvalSymlinks(p); err == nil && rp == real {
			if abs, err := filepath.Abs(p); err == nil {
				return abs, nil
			}
		}
	}
	if strings.Contains(real, "go-build") {
		return "", errors.New("this binary is temporary (go run); build it first with make opencrawl or go install")
	}
	return real, nil
}

func clients() []mcpconfig.Client {
	home, _ := os.UserHomeDir()
	configDir, _ := os.UserConfigDir()
	return mcpconfig.Clients(home, configDir)
}

func clientIDs() string {
	var ids []string
	for _, c := range clients() {
		ids = append(ids, c.ID)
	}
	return strings.Join(ids, ", ")
}

func runMCPInstall(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("mcp install", flag.ContinueOnError)
	only := fs.String("client", "", "comma-separated clients to configure: "+clientIDs()+" (default: every one found on this machine)")
	force := fs.Bool("force", false, "replace an existing opencrawl entry that points somewhere else")
	dryRun := fs.Bool("dry-run", false, "show what would change without changing anything")
	dbPath := fs.String("db", "", "database the server should use (default: $OPENCRAWL_DB or ~/.opencrawl/opencrawl.db at run time)")
	chrome := fs.String("chrome", "", "Chrome or Chromium for audits with render (default: search the usual places at run time)")
	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), "Usage: opencrawl mcp install [flags]\n\nRegisters \"opencrawl mcp\" with Claude Code, Claude Desktop and Cursor.")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		fs.Usage()
		return fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}

	self, err := selfPath()
	if err != nil {
		return err
	}
	srv := mcpconfig.Server{Name: "opencrawl", Command: self, Args: []string{"mcp"}}
	if *dbPath != "" {
		abs, err := filepath.Abs(*dbPath)
		if err != nil {
			return err
		}
		srv.Args = append(srv.Args, "-db", abs)
	}
	if *chrome != "" {
		srv.Args = append(srv.Args, "-chrome", *chrome)
	}

	var in mcpconfig.Installer
	var targets []mcpconfig.Client
	if *only != "" {
		want := map[string]bool{}
		for _, id := range strings.Split(*only, ",") {
			want[strings.TrimSpace(id)] = true
		}
		for _, c := range clients() {
			if want[c.ID] {
				targets = append(targets, c)
				delete(want, c.ID)
			}
		}
		for id := range want {
			return fmt.Errorf("unknown client %q; choose from %s", id, clientIDs())
		}
	} else {
		for _, c := range clients() {
			if in.Detected(c) {
				targets = append(targets, c)
			}
		}
		if len(targets) == 0 {
			fmt.Printf("No MCP clients found. Add this server to your client by hand:\n\n  command: %s\n  args:    %s\n", srv.Command, strings.Join(srv.Args, " "))
			return nil
		}
	}

	fmt.Printf("Server: %s %s\n", srv.Command, strings.Join(srv.Args, " "))
	if *dryRun {
		fmt.Println("Dry run: nothing will be changed.")
	}
	failed := 0
	for _, c := range targets {
		st, err := in.Install(ctx, c, srv, *force, *dryRun)
		where := c.ConfigPath
		if where == "" {
			where = "claude mcp add"
		}
		switch {
		case err != nil:
			failed++
			fmt.Printf("  ✗ %-15s %v\n", c.Label, err)
		case st == mcpconfig.Exists:
			fmt.Printf("  - %-15s already has an \"opencrawl\" server with a different command; -force replaces it (%s)\n", c.Label, where)
		case st == mcpconfig.Unchanged:
			fmt.Printf("  ✓ %-15s already set up (%s)\n", c.Label, where)
		default:
			verb := map[mcpconfig.Status]string{mcpconfig.Added: "added", mcpconfig.Updated: "updated"}[st]
			if *dryRun {
				verb = "would be " + verb
			}
			fmt.Printf("  ✓ %-15s %s (%s)\n", c.Label, verb, where)
			if !*dryRun && c.Note != "" {
				fmt.Printf("    %s\n", c.Note)
			}
		}
	}
	if failed > 0 {
		return fmt.Errorf("%d client(s) not configured", failed)
	}
	if !*dryRun {
		fmt.Println("\nTry asking your assistant: \"Audit https://your-site.example with opencrawl and tell me what GPTBot and ClaudeBot can read.\"")
	}
	return nil
}

func runDoctor(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("doctor", flag.ContinueOnError)
	dbPath := dbFlag(fs)
	chrome := fs.String("chrome", "", "Chrome or Chromium to test (default: search the usual places)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	problems := 0
	ok := func(label, msg string) { fmt.Printf("  ✓ %-15s %s\n", label, msg) }
	bad := func(label, msg string) { problems++; fmt.Printf("  ✗ %-15s %s\n", label, msg) }
	note := func(label, msg string) { fmt.Printf("  - %-15s %s\n", label, msg) }

	fmt.Printf("opencrawl %s (%s/%s)\n", buildVersion(), runtime.GOOS, runtime.GOARCH)
	self, err := selfPath()
	if err != nil {
		note("binary", err.Error())
	} else {
		ok("binary", self)
	}

	if st, err := store.Open(*dbPath); err != nil {
		bad("database", err.Error())
	} else {
		list, err := st.List(ctx, "", 1000)
		st.Close()
		if err != nil {
			bad("database", err.Error())
		} else {
			ok("database", fmt.Sprintf("%s (%d saved audits)", *dbPath, len(list)))
		}
	}

	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	pool, err := render.New(cctx, render.Options{ExecPath: *chrome})
	if err != nil {
		note("chrome", "not available, so -render and render: true won't work: "+err.Error())
	} else {
		pool.Close()
		ok("chrome", "starts, with its sandbox on")
	}
	cancel()

	var in mcpconfig.Installer
	for _, c := range clients() {
		if !in.Detected(c) {
			note(c.ID, "not found")
			continue
		}
		has, err := in.Configured(ctx, c, "opencrawl")
		switch {
		case err != nil:
			bad(c.ID, err.Error())
		case has:
			ok(c.ID, "opencrawl is configured")
		default:
			note(c.ID, "found, but opencrawl isn't configured; run opencrawl mcp install")
		}
	}
	if problems > 0 {
		return fmt.Errorf("%d problem(s) found", problems)
	}
	return nil
}
