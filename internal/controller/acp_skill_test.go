package controller

import (
	"context"
	"strings"
	"testing"

	"github.com/NodeSpy/conductor/internal/acp"
	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/memory"
	"github.com/NodeSpy/conductor/internal/skill"
)

// TestACPSkillClaimInjection (#36 §12, hardened per #122 R1): dispatching a
// skill-enabled profile mints a ONE-SHOT claim code delivered via the MCP
// server's ENVIRONMENT — never argv, which any same-user process can read
// from a process listing. The code exchanges exactly once for a session
// token bound server-side to the real profile's policy; a stale code is
// refused. Profiles without skill: get neither.
func TestACPSkillClaimInjection(t *testing.T) {
	memory.Reset()
	t.Cleanup(memory.Reset)
	memory.SetToolCommand([]string{"/usr/local/bin/conductor", "mcp", "memory", "--socket", "/data/memory.sock", "--no-memory"})

	broker := skill.NewBroker(
		func(name string) (string, bool) { return "s3cretvalue", name == "house/deploy_key" },
		nil,
	)
	skill.SetActive(broker)
	t.Cleanup(func() { skill.SetActive(nil) })

	newSession := func(prof config.AgentProfile) []acp.McpServer {
		t.Helper()
		agent := &fakeACPAgent{
			initResult: acp.InitializeResult{ProtocolVersion: acp.ProtocolVersion},
			sessionID:  "s-1",
		}
		c := newACPController("gem", config.ControllerConfig{Command: []string{"fake"}}, nil)
		c.dial = dialFake(agent)
		req := makeReq("merge_conflict", "fix it")
		req.Action.Agent = "deployer"
		req.Profile = prof
		sess, err := c.NewSession(context.Background(), Spec{Request: req, Cwd: "/wt"}, nil)
		if err != nil {
			t.Fatal(err)
		}
		if as, ok := sess.(*acpSession); ok {
			as.Wait(context.Background(), 0)
		}
		_ = sess.Close(context.Background())
		agent.mu.Lock()
		defer agent.mu.Unlock()
		return agent.gotMcp
	}

	got := newSession(config.AgentProfile{Skill: &config.SkillPolicy{
		SecretsVia: "broker", AllowSecrets: []string{"house/deploy_key"},
	}})
	if len(got) != 1 {
		t.Fatalf("mcp servers: %+v", got)
	}
	// REGRESSION (#122 R1): no token, no claim, nothing secret on argv.
	for _, a := range got[0].Args {
		if a == "--token" || a == "--claim" || strings.Contains(a, "CONDUCTOR_SKILL") {
			t.Fatalf("secret material on argv: %v", got[0].Args)
		}
	}
	var claim string
	for _, ev := range got[0].Env {
		if ev.Name == "CONDUCTOR_SKILL_CLAIM" {
			claim = ev.Value
		}
	}
	if claim == "" {
		t.Fatalf("no claim code in the MCP server env: %+v", got[0].Env)
	}

	// The claim exchanges once for a token that stands for the REAL profile's
	// policy server-side…
	peer := skill.Peer{PID: 7, StartTime: 1, Valid: true}
	token, err := broker.ClaimSession(claim, peer)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	grant, _, err := broker.Issue(token, "house/deploy_key", peer)
	if err != nil {
		t.Fatalf("issue under the claimed token: %v", err)
	}
	if v, err := broker.Redeem(token, grant, peer); err != nil || v != "s3cretvalue" {
		t.Fatalf("redeem: %q, %v", v, err)
	}
	if _, _, err := broker.Issue(token, "other_secret", peer); err == nil {
		t.Fatal("a name outside the dispatched profile's allow_secrets must be denied")
	}
	// …and exactly once: a scraped code is dead after the exchange.
	if _, err := broker.ClaimSession(claim, skill.Peer{PID: 666, Valid: true}); err == nil {
		t.Fatal("a spent claim code must be refused")
	}

	// A profile without skill: gets no claim at all.
	got = newSession(config.AgentProfile{})
	if len(got) != 1 {
		t.Fatalf("mcp servers: %+v", got)
	}
	if len(got[0].Env) != 0 {
		t.Fatalf("claim minted for a profile without skill:: %+v", got[0].Env)
	}
}
