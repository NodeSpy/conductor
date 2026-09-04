package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/NodeSpy/paseo-conductor/internal/config"
	"github.com/NodeSpy/paseo-conductor/internal/core"
)

func loadConfigDoc(t *testing.T, doc string) *config.Config {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

// TestDisabledConnectorSourceSuppressed: an author-set `enabled: false`
// suppresses the connector's source integration — its triggers go inert
// instead of firing (and instead of failing the boot).
func TestDisabledConnectorSourceSuppressed(t *testing.T) {
	cfg := loadConfigDoc(t, `
connectors:
  timer:
    type: cron
    enabled: false
    schedules: { tick: { every: 1h } }
  box:
    type: command
triggers:
  - on: timer.tick
    steps: [{ id: hi, uses: box.run, options: { command: "true" } }]
`)
	stack, err := buildFlowStack(cfg, nil, nil, true)
	if err != nil {
		t.Fatalf("a disabled connector must not fail the boot: %v", err)
	}
	if len(stack.Integrations) != 0 {
		t.Fatalf("disabled connector must open no sources, got %d", len(stack.Integrations))
	}
	in, _ := stack.Registry.Get("timer")
	if in.Enabled {
		t.Fatal("enabled: false lost in lowering")
	}
}

type captureEmitter struct {
	events []string
	msgs   []string
}

func (c *captureEmitter) Emit(_ context.Context, event string, t core.Trigger, msg string) {
	c.events = append(c.events, event+"/"+t.Source+"/"+t.Kind)
	c.msgs = append(c.msgs, msg)
}

// TestConnectorCredFailureNotifies: a connector whose own credential does not
// resolve (a file: secret ref pointing nowhere; missing ${VARS} already fail
// the load itself) is disabled AND escalated through the notifier — #36
// requires notify, not just a log line.
func TestConnectorCredFailureNotifies(t *testing.T) {
	cfg := loadConfigDoc(t, `
connectors:
  slack-ops:
    type: slack
    app_token: file:/nonexistent/pc-test-app-token
    bot_token: file:/nonexistent/pc-test-bot-token
  box:
    type: command
triggers:
  - on: slack-ops.app_mention
    steps: [{ id: hi, uses: box.run, options: { command: "true" } }]
`)
	stack, err := buildFlowStack(cfg, nil, nil, true)
	if err != nil {
		t.Fatalf("a broken connector must not fail the boot: %v", err)
	}
	if len(stack.ConnectorErrs) != 1 || !strings.Contains(stack.ConnectorErrs[0], `connector "slack-ops" disabled`) {
		t.Fatalf("want one connector failure naming slack-ops, got %v", stack.ConnectorErrs)
	}
	if !strings.Contains(stack.ConnectorErrs[0], "1 trigger(s) inert") {
		t.Fatalf("failure should count the inert triggers, got %v", stack.ConnectorErrs)
	}

	cap := &captureEmitter{}
	notifyStackFailures(stack, cap)
	if len(cap.events) != 1 || cap.events[0] != "escalate/connectors/connector_disabled" {
		t.Fatalf("want one connector_disabled escalate, got %v", cap.events)
	}
	if !strings.Contains(cap.msgs[0], "slack-ops") {
		t.Fatalf("notification must name the connector, got %q", cap.msgs[0])
	}

	// The healthy connector still built; the box keeps running.
	if in, ok := stack.Registry.Get("box"); !ok || in.DisabledReason != "" {
		t.Fatal("healthy connector should be unaffected")
	}
}
