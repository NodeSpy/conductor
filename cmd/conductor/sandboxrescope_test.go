package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/NodeSpy/conductor/internal/agentsandbox"
	"github.com/NodeSpy/conductor/internal/config"
)

// The daemon reads its workspace registry against the config it runs
// with: a scope recorded under an allowance the config no longer has is
// dropped from the registry when the sandbox is wired, and the resolver
// recomputes every recorded scope at launch.
func TestWiringRecomputesRecordedScopes(t *testing.T) {
	ws := t.TempDir()
	cfg := &config.Config{}
	cfg.Store.StateFile = filepath.Join(t.TempDir(), "state.json")
	stateRoot := cfg.SandboxStateDir()
	if err := os.MkdirAll(stateRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	revoked := &agentsandbox.Scope{Allow: &agentsandbox.Allowance{Tunnels: []string{"netcat"}, TunnelAnyHost: true, By: "workflow deploy"}}
	b, _ := json.Marshal([]map[string]any{{"workspace": ws, "identity": "agent:deploy", "tool": "claude-code", "dispatch": "d1", "scope": revoked}})
	if err := os.WriteFile(filepath.Join(stateRoot, ".workspaces.json"), b, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { agentsandbox.Default.Forget(ws) })

	stop, err := wireAgentSandbox(cfg, cfg.Store.StateFile, t.Logf)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(stop)
	e, ok := agentsandbox.Default.Lookup(ws)
	if !ok || e.Identity != "agent:deploy" {
		t.Fatalf("the recorded workspace was not loaded: %+v", e)
	}
	if e.Scope != nil {
		t.Fatalf("the registry kept a scope its config no longer grants: %+v", e.Scope.Allow)
	}
	r := sandboxResolver.Load()
	if r == nil || r.Rescope == nil {
		t.Fatal("the resolver does not recompute recorded scopes")
	}
	if got := r.Rescope(revoked); got != nil {
		t.Fatalf("the resolver kept a revoked scope: %+v", got.Allow)
	}
}
