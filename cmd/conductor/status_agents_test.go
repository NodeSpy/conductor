package main

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/NodeSpy/conductor/internal/controller"
)

// `conductor status` lists the agents the daemon's own runtimes have open
// (a cli claude-code fixer is a child of the daemon, not a paseo agent):
// the control socket's `agents` answers them, and status renders each.
func TestStatusListsRuntimeAgentsFromTheDaemon(t *testing.T) {
	cfg, _ := manualCfg(t)
	since := time.Now().Add(-90 * time.Second)
	controlLiveAgents = func() []controller.LiveAgent {
		return []controller.LiveAgent{{ID: "claude-code-4", Runtime: "claude", Transport: "cli",
			Target: "o/r#7", Kind: "new_comment", Since: since}}
	}
	defer func() { controlLiveAgents = nil }()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go serveControl(ctx, controlSockPath(cfg), nil, nil, nil, nil, nil, nil, func(string, ...any) {})
	waitForSock(t, controlSockPath(cfg))

	got := daemonLiveAgents(cfg)
	if len(got) != 1 || got[0].ID != "claude-code-4" || got[0].Target != "o/r#7" {
		t.Fatalf("agents over the control socket: %+v", got)
	}
	var out strings.Builder
	printLiveAgents(&out, []agentLine{{ShortID: "abc123", Status: "running", Name: "review"}}, got, since.Add(90*time.Second))
	text := out.String()
	for _, want := range []string{"live agents (2):", "abc123", "claude-code-4", "cli", "o/r#7", "new_comment", "running 1m30s"} {
		if !strings.Contains(text, want) {
			t.Fatalf("status lacks %q:\n%s", want, text)
		}
	}
}

// No daemon running: status lists none rather than failing.
func TestStatusRuntimeAgentsWithoutADaemon(t *testing.T) {
	cfg, _ := manualCfg(t)
	if got := daemonLiveAgents(cfg); got != nil {
		t.Fatalf("no daemon: %+v", got)
	}
}
