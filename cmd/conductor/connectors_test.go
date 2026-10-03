package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/connector"
	"github.com/NodeSpy/conductor/internal/core"
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
    use: cron
    enabled: false
    schedules: { tick: { every: 1h } }
  box:
    use: command
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
    use: slack
    app_token: file:/nonexistent/pc-test-app-token
    bot_token: file:/nonexistent/pc-test-bot-token
  box:
    use: command
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

// PLUGINS FIRST, NEVER BOOT-FATAL: a connector whose plugin is referenced but
// not installed yet (its fetch failed) loads disabled with the reason — its
// triggers and verbs are accepted unchecked — and the rest of the config
// boots.
func TestMissingConnectorPluginDisablesOnlyItsConnector(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	pendingPlugins = nil
	cfg := loadConfigDoc(t, `
connectors:
  forge:
    use: acme/conductor-plugins/connectors/widget
  box:
    use: command
triggers:
  - on: forge.new_comment
    steps: [{ id: hi, uses: forge.comment, options: { body: "{{.comment_id}}" } }]
  # A filter and options on the missing plugin's event cannot be checked
  # until it describes itself — they must not fail the boot either.
  - on: forge.failing_checks
    filter: { author: [someone], not_draft: true }
    options: { max_attempts_per_head: 2 }
    steps: [{ id: fix, uses: forge.comment, options: { body: "x" } }]
  - on: manual
    name: still-works
    steps: [{ id: ok, uses: box.run, options: { command: "true" } }]
`)
	stack, err := buildFlowStack(cfg, nil, nil, true)
	if err != nil {
		t.Fatalf("a missing plugin must not fail the boot: %v", err)
	}
	defer stack.Close()
	defer connector.UnregisterExternalType("widget")
	in, ok := stack.Registry.Get("forge")
	if !ok || !strings.Contains(in.DisabledReason, "not installed") {
		t.Fatalf("forge = %+v, want disabled as not installed", in)
	}
	if box, _ := stack.Registry.Get("box"); box == nil || box.DisabledReason != "" {
		t.Fatal("an unrelated connector was affected")
	}
	if len(pendingPlugins) != 1 || pendingPlugins[0] != "widget" {
		t.Fatalf("pending = %v", pendingPlugins)
	}
}

// TestCorruptInstalledConnectorPluginDisablesOnlyItsConnector: an INSTALLED
// plugin binary that fails verify/spawn/describe (corrupt binary, noexec
// mount, bad sha, a sandbox preflight failure) must not crash-loop the whole
// daemon (a hard boot error would exit 1 and have systemd restart into the
// same failure forever). Q12's "a plugin problem keeps only that connector
// down" applies the same way to a failed START as it does to a missing
// fetch.
func TestCorruptInstalledConnectorPluginDisablesOnlyItsConnector(t *testing.T) {
	// A binary that exists, is executable, and verifies (safe perms) but does
	// not speak the plugin wire protocol at all — Describe() fails against it
	// exactly like a corrupt binary or a bad build would.
	pendingPlugins = nil
	bin, _ := tempExecutable(t)
	cfg := loadConfigDoc(t, fmt.Sprintf(`
connectors:
  forge:
    use: %s
  box:
    use: command
triggers:
  - on: forge.new_comment
    steps: [{ id: hi, uses: forge.comment, options: { body: "{{.comment_id}}" } }]
  - on: manual
    name: still-works
    steps: [{ id: ok, uses: box.run, options: { command: "true" } }]
`, bin))
	stack, err := buildFlowStack(cfg, nil, nil, true)
	if err != nil {
		t.Fatalf("a plugin that fails to start must not fail the boot: %v", err)
	}
	defer stack.Close()
	defer connector.UnregisterExternalType("my-runtime") // tempExecutable's derived plugin name
	in, ok := stack.Registry.Get("forge")
	if !ok || in.DisabledReason == "" || !strings.Contains(in.DisabledReason, "failed to start") {
		t.Fatalf("forge = %+v, want disabled as failed to start", in)
	}
	if box, _ := stack.Registry.Get("box"); box == nil || box.DisabledReason != "" {
		t.Fatal("an unrelated connector was affected")
	}
	// Unlike the not-installed case, this plugin must NOT be queued for the
	// background install-gap retry: it IS installed, so pendingPluginRetry's
	// gap-fill would never change anything about it.
	for _, p := range pendingPlugins {
		if p == "my-runtime" {
			t.Fatal("a corrupt-but-installed plugin must not be queued for install-gap retry")
		}
	}
}
