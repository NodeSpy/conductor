package controller

import (
	"errors"
	"strings"
	"testing"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/preflight"
)

func TestJailPreflight(t *testing.T) {
	old := jailProbe
	defer func() { jailProbe = old }()
	env := preflight.Env{LookPath: func(n string) (string, error) {
		if n == "gh" {
			return "/usr/bin/gh", nil
		}
		return "", errors.New("absent")
	}}
	cfg := func(iso *config.IsolationConfig) *config.Config {
		return &config.Config{Controllers: map[string]config.ControllerConfig{
			"claude": {Type: "cli", Tool: "claude-code", Isolation: iso},
			"deck":   {Type: "agent-deck"},
		}}
	}
	jailProbe = func() string { return "" }
	f := jailPreflight(cfg(nil), env)
	joined := func(fs []preflight.Finding) string {
		var b strings.Builder
		for _, x := range fs {
			b.WriteString(x.Level + ": " + x.What + " — " + x.Why + "\n")
		}
		return b.String()
	}
	if s := joined(f); !strings.Contains(s, "jail: on for runtimes claude") || !strings.Contains(s, "host commands: gh") || !strings.Contains(s, "runtime deck: the agent workspace jail does not apply") {
		t.Fatalf("available: %s", s)
	}
	jailProbe = func() string { return "no user namespaces" }
	if s := joined(jailPreflight(cfg(nil), env)); !strings.Contains(s, "warn: the agent workspace jail is unavailable") {
		t.Fatalf("the synthesized default degrades with a warning: %s", s)
	}
	if s := joined(jailPreflight(cfg(&config.IsolationConfig{FS: []string{"/x"}}), env)); !strings.Contains(s, "error: an explicit isolation: block requires the workspace jail") {
		t.Fatalf("an explicit block fails closed: %s", s)
	}
	if s := joined(jailPreflight(cfg(&config.IsolationConfig{Mode: "none"}), env)); !strings.Contains(s, "unconfined (isolation mode: none)") {
		t.Fatalf("mode none is called out: %s", s)
	}
}
