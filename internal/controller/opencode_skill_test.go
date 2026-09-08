package controller

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/memory"
	"github.com/NodeSpy/conductor/internal/skill"
)

// TestOpencodeSkillToolInjection (#123): a native opencode session carries the
// conductor tool server through opencode's own MCP config — a per-session
// 0600 file handed to `opencode serve` via OPENCODE_CONFIG on its env. The
// one-shot skill claim rides the server's environment block (never argv),
// exchanges once against the broker, and the file is removed on Close.
func TestOpencodeSkillToolInjection(t *testing.T) {
	memory.Reset()
	t.Cleanup(memory.Reset)
	memory.SetToolCommand([]string{"/usr/local/bin/conductor", "mcp", "memory", "--socket", "/data/memory.sock", "--no-memory"})

	broker := skill.NewBroker(func(string) (string, bool) { return "", false }, nil)
	skill.SetActive(broker)
	t.Cleanup(func() { skill.SetActive(nil) })

	fs := &fakeOpencodeServer{}
	srv := httptest.NewServer(fs.handler())
	defer srv.Close()

	var gotEnv []string
	c := newOpencodeController("oc", config.ControllerConfig{Type: "opencode"}, &fakeProv{})
	c.dial = func(_ context.Context, _ string, env []string) (string, func() error, error) {
		gotEnv = append([]string(nil), env...)
		return srv.URL, func() error { return nil }, nil
	}

	req := makeReq("merge_conflict", "fix it")
	req.Action.Agent = "deployer"
	req.Profile = config.AgentProfile{Skill: &config.SkillPolicy{Verbs: []string{"gh.comment"}}}
	sess, err := c.NewSession(context.Background(), Spec{Request: req, Cwd: "/wt"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	waitSession(t, sess)

	// OPENCODE_CONFIG points at the per-session tool config…
	var cfgPath string
	for _, e := range gotEnv {
		if strings.HasPrefix(e, "OPENCODE_CONFIG=") {
			cfgPath = strings.TrimPrefix(e, "OPENCODE_CONFIG=")
		}
	}
	if cfgPath == "" {
		t.Fatalf("no OPENCODE_CONFIG on the serve env: %v", gotEnv)
	}
	raw, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	var oc struct {
		MCP map[string]struct {
			Type        string            `json:"type"`
			Command     []string          `json:"command"`
			Enabled     bool              `json:"enabled"`
			Environment map[string]string `json:"environment"`
		} `json:"mcp"`
	}
	if err := json.Unmarshal(raw, &oc); err != nil {
		t.Fatal(err)
	}
	ts, ok := oc.MCP["conductor-memory"]
	if !ok || ts.Type != "local" || !ts.Enabled {
		t.Fatalf("tool server config: %+v", oc.MCP)
	}
	if ts.Command[0] != "/usr/local/bin/conductor" || !strings.Contains(strings.Join(ts.Command, " "), "--agent deployer") {
		t.Fatalf("tool server command: %v", ts.Command)
	}
	// …with the claim in the ENVIRONMENT block, never argv, and it exchanges
	// exactly once against the broker.
	for _, a := range ts.Command {
		if strings.Contains(a, "CONDUCTOR_SKILL") || a == "--token" || a == "--claim" {
			t.Fatalf("secret material on the tool argv: %v", ts.Command)
		}
	}
	claim := ts.Environment["CONDUCTOR_SKILL_CLAIM"]
	if claim == "" {
		t.Fatalf("no claim in the tool environment: %+v", ts.Environment)
	}
	if _, err := broker.ClaimSession(claim, skill.Peer{PID: 9, Valid: true}); err != nil {
		t.Fatalf("claim: %v", err)
	}
	if _, err := broker.ClaimSession(claim, skill.Peer{PID: 10, Valid: true}); err == nil {
		t.Fatal("a spent claim code must be refused")
	}

	// Close removes the per-session config file.
	if err := sess.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(cfgPath); !os.IsNotExist(err) {
		t.Fatalf("tool config must be removed on Close: %v", err)
	}
}

// Without a published tool command (no memory: section, no skill profile in
// play), the opencode launch env is untouched.
func TestOpencodeNoToolServerNoConfig(t *testing.T) {
	memory.Reset()
	t.Cleanup(memory.Reset)

	fs := &fakeOpencodeServer{}
	srv := httptest.NewServer(fs.handler())
	defer srv.Close()

	var gotEnv []string
	c := newOpencodeController("oc", config.ControllerConfig{Type: "opencode"}, &fakeProv{})
	c.dial = func(_ context.Context, _ string, env []string) (string, func() error, error) {
		gotEnv = append([]string(nil), env...)
		return srv.URL, func() error { return nil }, nil
	}
	sess, err := c.NewSession(context.Background(), Spec{Request: makeReq("merge_conflict", "x"), Cwd: "/wt"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	waitSession(t, sess)
	for _, e := range gotEnv {
		if strings.HasPrefix(e, "OPENCODE_CONFIG=") {
			t.Fatalf("unexpected OPENCODE_CONFIG without a tool server: %v", gotEnv)
		}
	}
}
