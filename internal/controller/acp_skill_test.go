package controller

import (
	"context"
	"testing"

	"github.com/NodeSpy/conductor/internal/acp"
	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/memory"
	"github.com/NodeSpy/conductor/internal/skill"
)

// TestACPSkillTokenInjection: when the dispatched profile carries a skill:
// block and a broker is active, the daemon mints a session token at dispatch
// time, bakes it into the MCP argv, and the broker maps THAT token to the
// real profile's identity and policy server-side. Without a skill: block, no
// token rides along and the broker stays deny-by-default.
func TestACPSkillTokenInjection(t *testing.T) {
	memory.Reset()
	t.Cleanup(memory.Reset)
	memory.SetToolCommand([]string{"/usr/local/bin/conductor", "mcp", "memory", "--socket", "/data/memory.sock", "--no-memory"})

	broker := skill.NewBroker(
		func(name string) (string, bool) { return "s3cretvalue", name == "deploy_key" },
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

	// A skill-enabled profile gets a --token flag…
	got := newSession(config.AgentProfile{Skill: &config.SkillPolicy{
		SecretsVia: "broker", AllowSecrets: []string{"deploy_key"},
	}})
	if len(got) != 1 {
		t.Fatalf("mcp servers: %+v", got)
	}
	var token string
	for i, a := range got[0].Args {
		if a == "--token" && i+1 < len(got[0].Args) {
			token = got[0].Args[i+1]
		}
	}
	if token == "" {
		t.Fatalf("no --token in argv: %v", got[0].Args)
	}

	// …and that token stands for the REAL dispatch identity server-side: the
	// broker honors the profile's own policy under it.
	grant, _, err := broker.Issue(token, "deploy_key")
	if err != nil {
		t.Fatalf("issue under the minted token: %v", err)
	}
	if v, err := broker.Redeem(token, grant); err != nil || v != "s3cretvalue" {
		t.Fatalf("redeem: %q, %v", v, err)
	}
	// The policy is the dispatched profile's, not anything client-supplied.
	if _, _, err := broker.Issue(token, "other_secret"); err == nil {
		t.Fatal("a name outside the dispatched profile's allow_secrets must be denied")
	}

	// A profile without skill: gets no token at all.
	got = newSession(config.AgentProfile{})
	if len(got) != 1 {
		t.Fatalf("mcp servers: %+v", got)
	}
	for _, a := range got[0].Args {
		if a == "--token" {
			t.Fatalf("token minted for a profile without skill:: %v", got[0].Args)
		}
	}
}
