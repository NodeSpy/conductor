package agentsandbox

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func readClaudeJSON(t *testing.T, home string) map[string]any {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(home, ".claude.json"))
	if err != nil {
		t.Fatal(err)
	}
	m := map[string]any{}
	if len(b) == 0 {
		return m // the placeholder PrepareHome makes
	}
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("%v: %s", err, b)
	}
	return m
}

func trusted(m map[string]any, dir string) bool {
	p, _ := m["projects"].(map[string]any)
	e, _ := p[dir].(map[string]any)
	v, _ := e["hasTrustDialogAccepted"].(bool)
	return v
}

func TestTrustClaudeWorkspace(t *testing.T) {
	home := t.TempDir()
	os.WriteFile(filepath.Join(home, ".claude.json"), []byte(`{"theme":"dark","projects":{"/other":{"allowedTools":["x"]}}}`), 0o600)
	if err := trustClaudeWorkspace(home, "/ws"); err != nil {
		t.Fatal(err)
	}
	m := readClaudeJSON(t, home)
	if !trusted(m, "/ws") || m["theme"] != "dark" {
		t.Fatalf("trust recorded, the rest kept: %v", m)
	}
	if o := m["projects"].(map[string]any)["/other"].(map[string]any); o["allowedTools"] == nil || trusted(m, "/other") {
		t.Fatalf("another project untouched: %v", o)
	}
	fi, _ := os.Stat(filepath.Join(home, ".claude.json"))
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", fi.Mode())
	}
	// Idempotent: already trusted, the file is not rewritten (claude-code may
	// be writing it); an empty or missing file starts fresh.
	before, _ := os.Stat(filepath.Join(home, ".claude.json"))
	if err := trustClaudeWorkspace(home, "/ws"); err != nil || !trusted(readClaudeJSON(t, home), "/ws") {
		t.Fatal(err)
	}
	if after, _ := os.Stat(filepath.Join(home, ".claude.json")); !os.SameFile(before, after) {
		t.Fatal("an already-trusted workspace rewrote the file")
	}
	empty := t.TempDir()
	if err := trustClaudeWorkspace(empty, "/ws"); err != nil || !trusted(readClaudeJSON(t, empty), "/ws") {
		t.Fatal("a missing file", err)
	}
	ents, _ := os.ReadDir(home)
	for _, e := range ents {
		if e.Name() != ".claude.json" {
			t.Fatalf("leftover %s", e.Name())
		}
	}
}

// The agent owns its home: a .claude.json it replaced with a symlink is
// neither read through nor written through.
func TestTrustClaudeWorkspaceNeverFollowsTheAgentsSymlink(t *testing.T) {
	home, other := t.TempDir(), t.TempDir()
	target := filepath.Join(other, "secret.json")
	os.WriteFile(target, []byte(`{"token":"SECRET"}`), 0o600)
	os.Symlink(target, filepath.Join(home, ".claude.json"))
	if err := trustClaudeWorkspace(home, "/ws"); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(target); string(b) != `{"token":"SECRET"}` {
		t.Fatalf("written through the symlink: %s", b)
	}
	if fi, _ := os.Lstat(filepath.Join(home, ".claude.json")); fi.Mode()&os.ModeSymlink == 0 {
		t.Fatal("the symlink was replaced with the target's content")
	}
	// Malformed state is left for claude-code to deal with.
	bad := t.TempDir()
	os.WriteFile(filepath.Join(bad, ".claude.json"), []byte("not json"), 0o600)
	if err := trustClaudeWorkspace(bad, "/ws"); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(bad, ".claude.json")); string(b) != "not json" {
		t.Fatal("malformed state rewritten")
	}
	os.WriteFile(filepath.Join(bad, ".claude.json"), []byte(`{"projects":"x"}`), 0o600)
	if err := trustClaudeWorkspace(bad, "/ws"); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(bad, ".claude.json")); string(b) != `{"projects":"x"}` {
		t.Fatal("an unexpected projects shape rewritten")
	}
}

// Only a dispatch's workspace (conductor's own) is trusted, only for
// claude-code; a directory the operator started an agent in is not.
func TestPolicyTrustsOnlyADispatchWorkspace(t *testing.T) {
	root := t.TempDir()
	ws := filepath.Join(root, "ws")
	os.MkdirAll(filepath.Join(ws, "sub"), 0o700)
	ws = canon(ws)
	reg := &Registry{}
	r := &Resolver{Registry: reg, StateRoot: filepath.Join(root, "st"), GOOS: "linux"}
	reg.Register(Entry{Workspace: ws, Identity: "agent:a", Dispatch: "d1"})
	p, err := r.PolicyFor(filepath.Join(ws, "sub"), "claude", nil, false)
	if err != nil {
		t.Fatal(err)
	}
	m := readClaudeJSON(t, p.Home)
	if !trusted(m, ws) || !trusted(m, filepath.Join(ws, "sub")) {
		t.Fatalf("the dispatch workspace (and the cwd in it) trusted: %v", m)
	}
	cx := filepath.Join(root, "cx")
	os.MkdirAll(cx, 0o700)
	reg.Register(Entry{Workspace: cx, Identity: "agent:b", Dispatch: "d2"})
	p, err = r.PolicyFor(cx, "codex", nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(p.Home, ".claude.json")); err == nil {
		t.Fatal("another tool's home gets no claude state")
	}
	own := filepath.Join(root, "mine")
	os.MkdirAll(own, 0o700)
	reg.Register(Entry{Workspace: own, Identity: "member:m"})
	p, err = r.PolicyFor(own, "claude", nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if trusted(readClaudeJSON(t, p.Home), canon(own)) {
		t.Fatal("a workspace with no dispatch is not trusted on its behalf")
	}
	dir := filepath.Join(root, "adhoc")
	os.MkdirAll(dir, 0o700)
	p, err = r.PolicyFor(dir, "claude", nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if trusted(readClaudeJSON(t, p.Home), canon(dir)) {
		t.Fatal("an operator's own directory is not trusted on their behalf")
	}
}
