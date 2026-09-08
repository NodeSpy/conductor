package dispatch

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestInjectClaudeMCP(t *testing.T) {
	dir := t.TempDir()
	if out, err := exec.Command("git", "-C", dir, "init").CombinedOutput(); err != nil {
		t.Skipf("git init unavailable: %v %s", err, out)
	}
	ts := &ToolServerSpec{
		Command: "conductor",
		Args:    []string{"mcp", "memory", "--socket", "/run/c.sock", "--agent", "fixer"},
		Env:     map[string]string{"CONDUCTOR_SKILL_CLAIM": "one-shot-code"},
	}
	injected, err := InjectClaudeMCP(dir, ts)
	if err != nil || !injected {
		t.Fatalf("inject: injected=%v err=%v", injected, err)
	}

	// .mcp.json defines the conductor server with command/args/env.
	var mcp struct {
		MCPServers map[string]struct {
			Command string            `json:"command"`
			Args    []string          `json:"args"`
			Env     map[string]string `json:"env"`
		} `json:"mcpServers"`
	}
	readJSON(t, filepath.Join(dir, ".mcp.json"), &mcp)
	srv, ok := mcp.MCPServers["conductor"]
	if !ok || srv.Command != "conductor" || len(srv.Args) != 6 || srv.Env["CONDUCTOR_SKILL_CLAIM"] != "one-shot-code" {
		t.Fatalf("bad .mcp.json server: %+v", srv)
	}

	// settings.local.json enables ONLY our server and pre-approves its tools.
	var settings struct {
		Enabled     []string `json:"enabledMcpjsonServers"`
		Permissions struct {
			Allow []string `json:"allow"`
		} `json:"permissions"`
	}
	readJSON(t, filepath.Join(dir, ".claude", "settings.local.json"), &settings)
	if len(settings.Enabled) != 1 || settings.Enabled[0] != "conductor" {
		t.Errorf("enabledMcpjsonServers = %v, want [conductor]", settings.Enabled)
	}
	if len(settings.Permissions.Allow) != 1 || settings.Permissions.Allow[0] != "mcp__conductor" {
		t.Errorf("permissions.allow = %v, want [mcp__conductor]", settings.Permissions.Allow)
	}

	// Both files git-excluded so the agent's commits never carry them.
	excl, _ := os.ReadFile(filepath.Join(dir, ".git", "info", "exclude"))
	for _, want := range []string{".mcp.json", ".claude/settings.local.json"} {
		if !strings.Contains(string(excl), want) {
			t.Errorf(".git/info/exclude missing %q:\n%s", want, excl)
		}
	}
	// Confirm git actually ignores them.
	out, _ := exec.Command("git", "-C", dir, "status", "--porcelain").CombinedOutput()
	if strings.Contains(string(out), ".mcp.json") {
		t.Errorf("git still sees .mcp.json:\n%s", out)
	}
}

func TestInjectClaudeMCPSkipsRepoOwnConfig(t *testing.T) {
	dir := t.TempDir()
	own := `{"mcpServers":{"repo-tool":{"command":"x"}}}`
	if err := os.WriteFile(filepath.Join(dir, ".mcp.json"), []byte(own), 0o644); err != nil {
		t.Fatal(err)
	}
	injected, err := InjectClaudeMCP(dir, &ToolServerSpec{Command: "conductor"})
	if err != nil {
		t.Fatal(err)
	}
	if injected {
		t.Fatal("must not inject when the repo ships its own .mcp.json")
	}
	got, _ := os.ReadFile(filepath.Join(dir, ".mcp.json"))
	if string(got) != own {
		t.Fatalf("repo .mcp.json was clobbered: %s", got)
	}
}

func readJSON(t *testing.T, path string, v any) {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if err := json.Unmarshal(b, v); err != nil {
		t.Fatalf("parse %s: %v\n%s", path, err, b)
	}
}
