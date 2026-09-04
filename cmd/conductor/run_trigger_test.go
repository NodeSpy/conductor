package main

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/connector"
	"github.com/NodeSpy/conductor/internal/core"
)

// manualCfg builds a config with one manual trigger for handler tests and
// returns it with its file path (for CLI arg parsing).
func manualCfg(t *testing.T) (*config.Config, string) {
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
`
	if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	return cfg, path
}

// TestRunManualTrigger: the control handler resolves the trigger by name and
// emits it with the CLI inputs in context, Force set, and the FlowRef back to
// the manual spec.
func TestRunManualTrigger(t *testing.T) {
	cfg, _ := manualCfg(t)
	manual := manualTriggersByName(cfg)
	if len(manual) != 1 {
		t.Fatalf("manual index: %v", manual)
	}

	var got core.Trigger
	emit := func(_ context.Context, tr core.Trigger) { got = tr }
	msg, err := runManualTrigger(context.Background(), manual,
		controlRequest{Cmd: "run", Name: "deploy", Inputs: map[string]any{"env": "prod"}}, emit)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(msg, `"deploy"`) {
		t.Fatalf("msg: %s", msg)
	}
	if got.Source != "manual" || got.Kind != "manual" || got.Variant != "deploy" || !got.Force {
		t.Fatalf("trigger identity: %+v", got)
	}
	act, ok := got.Action.(config.Action)
	if !ok || act.FlowRef != "0:manual" || act.Name != "deploy" {
		t.Fatalf("action: %+v", got.Action)
	}
	if act.TargetRepo != "acme/app" {
		t.Fatalf("repo pin: %q", act.TargetRepo)
	}
	// Inputs land top-level and under .inputs.
	if got.Context["env"] != "prod" {
		t.Fatalf("top-level input: %v", got.Context)
	}
	if in, ok := got.Context["inputs"].(map[string]any); !ok || in["env"] != "prod" {
		t.Fatalf(".inputs: %v", got.Context)
	}

	// Unknown name errors and lists what exists.
	_, err = runManualTrigger(context.Background(), manual, controlRequest{Cmd: "run", Name: "ghost"}, emit)
	if err == nil || !strings.Contains(err.Error(), `no manual trigger named "ghost"`) || !strings.Contains(err.Error(), "deploy") {
		t.Fatalf("unknown name: %v", err)
	}
	_, err = runManualTrigger(context.Background(), map[string]connector.CompiledTrigger{}, controlRequest{Cmd: "run", Name: "x"}, emit)
	if err == nil || !strings.Contains(err.Error(), "no trigger declares `on: manual`") {
		t.Fatalf("no manual triggers: %v", err)
	}
}

// TestControlSocketRun: the run command round-trips through the real unix
// socket protocol (serveControl + sendControl).
func TestControlSocketRun(t *testing.T) {
	cfg, _ := manualCfg(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var mu sync.Mutex
	var emitted []core.Trigger
	emit := func(_ context.Context, tr core.Trigger) {
		mu.Lock()
		defer mu.Unlock()
		emitted = append(emitted, tr)
	}
	go serveControl(ctx, controlSockPath(cfg), nil, emit, manualTriggersByName(cfg), func(string, ...any) {})

	// Wait for the socket to exist.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(controlSockPath(cfg)); err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	resp, err := sendControl(cfg, controlRequest{Cmd: "run", Name: "deploy",
		Inputs: map[string]any{"contact_id": "abc-123", "adjustments": map[string]any{"Quantity": float64(2)}}})
	if err != nil {
		t.Fatal(err)
	}
	if !resp.OK || resp.Dispatched != 1 {
		t.Fatalf("response: %+v", resp)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(emitted) != 1 {
		t.Fatalf("emitted %d triggers", len(emitted))
	}
	in := emitted[0].Context["inputs"].(map[string]any)
	if in["contact_id"] != "abc-123" {
		t.Fatalf("structured inputs: %v", in)
	}
	if adj, ok := in["adjustments"].(map[string]any); !ok || adj["Quantity"] != float64(2) {
		t.Fatalf("nested JSON input survived the socket: %v", in)
	}

	// Unknown name over the socket comes back as a response error.
	resp, err = sendControl(cfg, controlRequest{Cmd: "run", Name: "ghost"})
	if err != nil {
		t.Fatal(err)
	}
	if resp.OK || !strings.Contains(resp.Error, `no manual trigger named "ghost"`) {
		t.Fatalf("unknown name response: %+v", resp)
	}
}

// TestCmdRunTriggerCLI: flag parsing (--input k=v over --json), the
// daemon-down error, and the positional/daemon routing predicate.
func TestCmdRunTriggerCLI(t *testing.T) {
	cfg, cfgPath := manualCfg(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var mu sync.Mutex
	var emitted []core.Trigger
	emit := func(_ context.Context, tr core.Trigger) {
		mu.Lock()
		defer mu.Unlock()
		emitted = append(emitted, tr)
	}
	go serveControl(ctx, controlSockPath(cfg), nil, emit, manualTriggersByName(cfg), func(string, ...any) {})
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(controlSockPath(cfg)); err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	args := []string{"deploy", "--config", cfgPath,
		"--json", `{"contact_id":"from-json","note":"kept"}`,
		"--input", "contact_id=from-flag"}
	out, err := captureStdout(t, func() error { return cmdRunTrigger(args) })
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `"deploy"`) {
		t.Fatalf("cli output: %s", out)
	}
	mu.Lock()
	in := emitted[0].Context["inputs"].(map[string]any)
	mu.Unlock()
	if in["contact_id"] != "from-flag" || in["note"] != "kept" {
		t.Fatalf("--input must overlay --json: %v", in)
	}

	// Parse errors.
	for _, bad := range [][]string{
		{"deploy", "--config", cfgPath, "--input", "novalue"},
		{"deploy", "--config", cfgPath, "--json", "{broken"},
		{"deploy", "extra", "--config", cfgPath},
		{"--config", cfgPath},                        // no name
		{"deploy", "--config", cfgPath, "--input"},   // missing value
		{"deploy", "--config", cfgPath, "--json"},    // missing value
		{"deploy", "--config", cfgPath, "--bogus-x"}, // unknown flag
	} {
		if err := cmdRunTrigger(bad); err == nil {
			t.Fatalf("args %v must fail", bad)
		}
	}

	// Daemon down: a clear connect error.
	cancel()
	waitGone := time.Now().Add(3 * time.Second)
	for time.Now().Before(waitGone) {
		if _, err := net.Dial("unix", controlSockPath(cfg)); err != nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	err = cmdRunTrigger([]string{"deploy", "--config", cfgPath})
	if err == nil || !strings.Contains(err.Error(), "is it running?") {
		t.Fatalf("daemon-down error: %v", err)
	}

	// Routing: a positional means trigger mode; flags alone mean the daemon.
	if !hasPositional([]string{"deploy", "--input", "a=b"}) {
		t.Fatal("positional not detected")
	}
	if hasPositional([]string{"--config", "x.yaml"}) {
		t.Fatal("flag value misread as positional")
	}
}
