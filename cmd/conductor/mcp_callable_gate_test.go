package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/core"
)

// gateCfg builds a config with two manual triggers (deploy, secret) so the MCP
// callable gate can be exercised over the real control socket.
func gateCfg(t *testing.T) *config.Config {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	doc := `
store: { state_file: ` + filepath.Join(dir, "state.json") + ` }
connectors:
  box: { type: command }
triggers:
  - name: deploy
    on: manual
    repo: acme/app
    steps: [{ uses: box.run, options: { command: "true" } }]
  - name: secret
    on: manual
    repo: acme/app
    steps: [{ uses: box.run, options: { command: "true" } }]
`
	if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

// TestMCPCallableGate exercises the exact hole in #36 §13 review item 2: a run
// that arrived via the MCP callable face (req.Callable) must be held to the same
// token model as the HTTP surface — the daemon re-checks the callable opt-in +
// the presenting token's scope at dispatch time, and audits every allowed
// invoke. Before the fix the MCP face dispatched straight through with no
// re-check, no scope, and no audit.
func TestMCPCallableGate(t *testing.T) {
	cfg := gateCfg(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var mu sync.Mutex
	var emitted []core.Trigger
	emit := func(_ context.Context, tr core.Trigger) {
		mu.Lock()
		defer mu.Unlock()
		emitted = append(emitted, tr)
	}
	var audits []map[string]any
	auditFn := func(e map[string]any) {
		mu.Lock()
		defer mu.Unlock()
		audits = append(audits, e)
	}

	// "ci" is scoped to deploy only; both triggers are callable at run time.
	callableNow := true
	gate := &callableGate{
		tokens: map[string]config.CallableToken{
			"ci": {Name: "ci", Workflows: []string{"deploy"}},
		},
		callable: func(string) bool { return callableNow },
		audit:    auditFn,
	}
	go serveControl(ctx, controlSockPath(cfg), nil, emit, manualTriggersByName(cfg), nil, nil, gate, func(string, ...any) {})

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(controlSockPath(cfg)); err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	countEmitted := func() int { mu.Lock(); defer mu.Unlock(); return len(emitted) }

	// (1) Allowed: scoped, callable → dispatched + a callable_invoke audit.
	resp, err := sendControl(cfg, controlRequest{
		Cmd: "run", Name: "deploy", Callable: true, Caller: "ci", HistoryID: "run-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !resp.OK || resp.Dispatched != 1 {
		t.Fatalf("allowed invoke: %+v", resp)
	}
	if got := countEmitted(); got != 1 {
		t.Fatalf("allowed invoke should dispatch once, got %d", got)
	}
	mu.Lock()
	if len(audits) != 1 || audits[0]["event"] != "callable_invoke" ||
		audits[0]["caller"] != "ci" || audits[0]["workflow"] != "deploy" || audits[0]["run_id"] != "run-1" {
		mu.Unlock()
		t.Fatalf("callable_invoke audit not written correctly: %v", audits)
	}
	mu.Unlock()

	// (2) Out-of-scope: "ci" is not scoped to `secret` → refused, not dispatched.
	resp, err = sendControl(cfg, controlRequest{Cmd: "run", Name: "secret", Callable: true, Caller: "ci"})
	if err != nil {
		t.Fatal(err)
	}
	if resp.OK || !strings.Contains(resp.Error, "not scoped") {
		t.Fatalf("out-of-scope must be refused: %+v", resp)
	}

	// (3) Unknown token → refused (deny-by-default caller identity).
	resp, err = sendControl(cfg, controlRequest{Cmd: "run", Name: "deploy", Callable: true, Caller: "ghost"})
	if err != nil {
		t.Fatal(err)
	}
	if resp.OK || !strings.Contains(resp.Error, "unknown callable token") {
		t.Fatalf("unknown token must be refused: %+v", resp)
	}

	// (4) Revoked reachability: removing `callable:` (callable() now false) must
	// revoke the MCP face even though the token still lists the workflow.
	callableNow = false
	resp, err = sendControl(cfg, controlRequest{Cmd: "run", Name: "deploy", Callable: true, Caller: "ci"})
	if err != nil {
		t.Fatal(err)
	}
	if resp.OK || !strings.Contains(resp.Error, "not callable") {
		t.Fatalf("revoked callable must be refused: %+v", resp)
	}
	callableNow = true

	// Only the first (allowed) invoke ever dispatched or audited.
	if got := countEmitted(); got != 1 {
		t.Fatalf("refused invokes must not dispatch: %d total", got)
	}
	mu.Lock()
	if len(audits) != 1 {
		mu.Unlock()
		t.Fatalf("refused invokes must not audit: %v", audits)
	}
	mu.Unlock()

	// (5) A local `conductor run` (Callable false) stays ungated — same socket,
	// no token, still dispatches. This is the boundary the fix must not move.
	resp, err = sendControl(cfg, controlRequest{Cmd: "run", Name: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	if !resp.OK || resp.Dispatched != 1 {
		t.Fatalf("local run must stay ungated: %+v", resp)
	}
	if got := countEmitted(); got != 2 {
		t.Fatalf("local run should dispatch, total %d", got)
	}
}
