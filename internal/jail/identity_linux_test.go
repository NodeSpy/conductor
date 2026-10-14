//go:build linux

package jail

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/sandbox"
)

// runIdentity runs script in a jail for a launch with the given StateKey.
func (f *fixture) runIdentity(t *testing.T, m *Manager, id, key, script string) string {
	t.Helper()
	l, err := m.Prepare(context.Background(), LaunchSpec{DispatchID: id, Tool: "claude-code", Workspace: f.ws, Label: "paseo " + id, StateKey: key, AgentID: id})
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	defer l.Close()
	deps := sandbox.LocalWrapDeps{SelfExe: m.SelfExe, Confine: true, ExtraBinds: l.Binds}
	env := stripCreds(append(sandbox.MinimalEnv(), l.Env...))
	argv, outEnv, cleanup, err := sandbox.WrapLocalCommand(sandbox.FromConfig(&config.IsolationConfig{Mode: "namespace"}),
		[]string{"/bin/sh", "-c", script}, f.ws, env, deps)
	if err != nil {
		t.Fatalf("wrap: %v", err)
	}
	defer cleanup()
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Dir, cmd.Env = f.ws, outEnv
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s: %v\n%s", id, err, out)
	}
	return string(out)
}

// Two launch identities each get their agent CLI's state from a directory of
// their own: neither sees the other's transcripts nor the operator's, both
// are signed in with the operator's (shared) login, the operator's settings
// are seeded without their project history, and an identity's state
// survives to its next launch.
func TestJailToolStateIsPerIdentity(t *testing.T) {
	needJailKernel(t)
	f := newFixture(t)
	m := f.manager(t)
	m.IdentityStateDir = filepath.Join(f.root, "state", "agent-state")
	claude := filepath.Join(f.home, ".claude")
	for p, body := range map[string]string{
		"projects/op/history.jsonl": "OPERATOR-TRANSCRIPT",
		".credentials.json":         "LOGIN-v1",
		"settings.json":             `{"theme":"op"}`,
	} {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(claude, p)), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(claude, p), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(f.home, ".claude.json"), []byte(`{"oauthAccount":{"emailAddress":"op@example.test"},"projects":{"/secret/repo":{"history":["x"]}}}`), 0o600); err != nil {
		t.Fatal(err)
	}

	a := f.runIdentity(t, m, "a1", "paseo-a1", `
ls -R ~/.claude/projects 2>&1 | grep -q history.jsonl && echo SEES-OPERATOR || echo no-operator
cat ~/.claude/.credentials.json; echo
cat ~/.claude/settings.json; echo
cat ~/.claude.json; echo
mkdir -p ~/.claude/projects/a && echo A-TRANSCRIPT > ~/.claude/projects/a/s.jsonl
printf LOGIN-v2 > ~/.claude/.credentials.json
`)
	for _, want := range []string{"no-operator", "LOGIN-v1", `{"theme":"op"}`, "op@example.test"} {
		if !strings.Contains(a, want) {
			t.Fatalf("identity a: want %q in:\n%s", want, a)
		}
	}
	if strings.Contains(a, "SEES-OPERATOR") || strings.Contains(a, "/secret/repo") {
		t.Fatalf("identity a sees the operator's history:\n%s", a)
	}
	if b, _ := os.ReadFile(filepath.Join(claude, ".credentials.json")); string(b) != "LOGIN-v2" {
		t.Fatalf("the login is the operator's own file (a refresh lands there), got %q", b)
	}
	if _, err := os.Stat(filepath.Join(claude, "projects", "a")); !os.IsNotExist(err) {
		t.Fatal("an identity's transcripts never reach the operator's ~/.claude")
	}

	b := f.runIdentity(t, m, "b1", "paseo-b1", `
ls -R ~/.claude/projects 2>&1 | grep -q s.jsonl && echo SEES-A || echo no-a
cat ~/.claude/.credentials.json; echo
`)
	if !strings.Contains(b, "no-a") || !strings.Contains(b, "LOGIN-v2") {
		t.Fatalf("identity b: its own state, the shared login:\n%s", b)
	}
	again := f.runIdentity(t, m, "a2", "paseo-a1", `cat ~/.claude/projects/a/s.jsonl`)
	if !strings.Contains(again, "A-TRANSCRIPT") {
		t.Fatalf("identity a's state survives to its next launch:\n%s", again)
	}
	shared := f.runIdentity(t, m, "d1", "", `ls -R ~/.claude/projects 2>&1 | grep -q history.jsonl && echo shared-state`)
	if !strings.Contains(shared, "shared-state") {
		t.Fatalf("a launch with no StateKey keeps the real tool state:\n%s", shared)
	}
}

// The identity key is a directory name: anything else in it is replaced, and
// a key that names nothing (".", "..") gets no identity state at all.
func TestIdentityRootSanitizes(t *testing.T) {
	m := &Manager{IdentityStateDir: "/st"}
	for key, want := range map[string]string{
		"paseo-abc":    "/st/paseo-abc",
		"../../etc":    "/st/.._.._etc",
		"a/b":          "/st/a_b",
		"..":           "",
		"":             "",
		"agent 1\x00x": "/st/agent_1_x",
	} {
		if got := m.identityRoot(&Dispatch{LaunchSpec: LaunchSpec{StateKey: key}}); got != want {
			t.Errorf("%q → %q, want %q", key, got, want)
		}
	}
}
