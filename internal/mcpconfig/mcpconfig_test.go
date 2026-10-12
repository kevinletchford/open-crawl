package mcpconfig

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

var srv = Server{Name: "opencrawl", Command: "/usr/local/bin/opencrawl", Args: []string{"mcp"}}

func readJSON(t *testing.T, path string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestInstallFileKeepsOtherSettings(t *testing.T) {
	path := filepath.Join(t.TempDir(), "claude_desktop_config.json")
	orig := `{"theme":"dark","mcpServers":{"other":{"command":"x","args":["y"],"env":{"K":"V"}}}}`
	if err := os.WriteFile(path, []byte(orig), 0o600); err != nil {
		t.Fatal(err)
	}

	st, err := installFile(path, srv, false, false)
	if err != nil || st != Added {
		t.Fatalf("install = %v, %v", st, err)
	}
	cfg := readJSON(t, path)
	if cfg["theme"] != "dark" {
		t.Errorf("theme lost: %v", cfg)
	}
	servers := cfg["mcpServers"].(map[string]any)
	if _, ok := servers["other"]; !ok {
		t.Errorf("other server lost: %v", servers)
	}
	want := map[string]any{"command": srv.Command, "args": []any{"mcp"}}
	if !reflect.DeepEqual(servers["opencrawl"], want) {
		t.Errorf("entry = %v", servers["opencrawl"])
	}
	if bak, _ := os.ReadFile(path + ".bak"); string(bak) != orig {
		t.Errorf("backup = %q", bak)
	}
	if fi, _ := os.Stat(path); fi.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v, want 0600 kept", fi.Mode().Perm())
	}

	if st, _ := installFile(path, srv, false, false); st != Unchanged {
		t.Errorf("second install = %v", st)
	}
	moved := srv
	moved.Command = "/opt/homebrew/bin/opencrawl"
	if st, _ := installFile(path, moved, false, false); st != Exists {
		t.Errorf("different entry without force = %v", st)
	}
	if st, _ := installFile(path, moved, true, true); st != Updated {
		t.Errorf("dry run with force = %v", st)
	}
	if got := readJSON(t, path)["mcpServers"].(map[string]any)["opencrawl"].(map[string]any)["command"]; got != srv.Command {
		t.Errorf("dry run changed the file: %v", got)
	}
	if st, _ := installFile(path, moved, true, false); st != Updated {
		t.Errorf("force = %v", st)
	}
	if got := readJSON(t, path)["mcpServers"].(map[string]any)["opencrawl"].(map[string]any)["command"]; got != moved.Command {
		t.Errorf("force didn't replace: %v", got)
	}
}

func TestInstallFileCreatesMissingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".cursor", "mcp.json")
	if st, err := installFile(path, srv, false, false); err != nil || st != Added {
		t.Fatalf("install = %v, %v", st, err)
	}
	if _, ok := readJSON(t, path)["mcpServers"].(map[string]any)["opencrawl"]; !ok {
		t.Error("entry missing")
	}
	if _, err := os.Stat(path + ".bak"); err == nil {
		t.Error("backup written for a new file")
	}
}

func TestInstallFileLeavesBadJSONAlone(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mcp.json")
	os.WriteFile(path, []byte(`{"mcpServers": {`), 0o644)
	if _, err := installFile(path, srv, false, false); err == nil || !strings.Contains(err.Error(), "left alone") {
		t.Errorf("err = %v", err)
	}
	if data, _ := os.ReadFile(path); string(data) != `{"mcpServers": {` {
		t.Errorf("file changed: %q", data)
	}
}

func TestInstallClaudeCode(t *testing.T) {
	var calls []string
	existing := false
	in := Installer{
		LookPath: func(string) (string, error) { return "/bin/claude", nil },
		Run: func(_ context.Context, name string, args ...string) ([]byte, error) {
			calls = append(calls, strings.Join(args, " "))
			if args[1] == "get" && !existing {
				return []byte("not found"), errors.New("exit 1")
			}
			return nil, nil
		},
	}
	cc := Clients("/home", "/config")[0]
	ctx := context.Background()

	if st, err := in.Install(ctx, cc, srv, false, false); err != nil || st != Added {
		t.Fatalf("install = %v, %v", st, err)
	}
	if want := "mcp add --scope user opencrawl -- /usr/local/bin/opencrawl mcp"; calls[len(calls)-1] != want {
		t.Errorf("calls = %q", calls)
	}

	existing, calls = true, nil
	if st, _ := in.Install(ctx, cc, srv, false, false); st != Exists || len(calls) != 1 {
		t.Errorf("existing without force = %v, calls %q", st, calls)
	}
	calls = nil
	if st, _ := in.Install(ctx, cc, srv, true, false); st != Updated || len(calls) != 3 || !strings.HasPrefix(calls[1], "mcp remove --scope user") {
		t.Errorf("force = %v, calls %q", st, calls)
	}

	in.LookPath = func(string) (string, error) { return "", errors.New("not found") }
	if in.Detected(cc) {
		t.Error("detected without the claude CLI")
	}
	if _, err := in.Install(ctx, cc, srv, false, false); err == nil {
		t.Error("install without the claude CLI succeeded")
	}
}
