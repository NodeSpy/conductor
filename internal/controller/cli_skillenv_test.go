package controller

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/memory"
	"github.com/NodeSpy/conductor/internal/skill"
)

// A local cli launch carries the dispatch's conductor session (endpoint +
// token) so the agent can `conductor call step.done` and its granted verbs —
// and a member worker's token is bound to its member and case, which is what
// puts its calls through the member gate.
func TestCLILaunchCarriesSkillSession(t *testing.T) {
	prev := memory.ToolCommand()
	memory.SetToolCommand([]string{"conductor", "mcp", "memory", "--socket", "/tmp/x/memory.sock"})
	t.Cleanup(func() { memory.SetToolCommand(prev) })
	b := skill.NewBroker(nil, nil)
	skill.SetActive(b)
	t.Cleanup(func() { skill.SetActive(nil) })

	l := &fakeLauncher{}
	c := newCLIController("claude", config.ControllerConfig{Transport: "cli", Tool: "claude-code"}, nil)
	c.launch = l.launch
	req := makeReq("manual", "work")
	req.DispatchID = "m-sre-w1-x:work"
	req.Step.BoundMember, req.Step.BoundMemberRole = "sre", config.MemberRoleWorker
	req.Trigger.Context = map[string]any{"member_case": "c3"}
	sess, err := c.NewSession(context.Background(), Spec{Request: req, Cwd: t.TempDir()}, nil)
	if err != nil {
		t.Fatal(err)
	}
	waitSession(t, sess)
	var endpoint, token string
	for _, kv := range l.call(0).env {
		if v, ok := strings.CutPrefix(kv, "CONDUCTOR_ENDPOINT="); ok {
			endpoint = v
		}
		if v, ok := strings.CutPrefix(kv, "CONDUCTOR_SKILL_TOKEN="); ok {
			token = v
		}
	}
	if endpoint != "unix:///tmp/x/memory.sock" || token == "" {
		t.Fatalf("cli launch env lacks the conductor session: endpoint=%q token=%v", endpoint, token != "")
	}
	id, err := b.Authorize(token, skill.Peer{UID: uint32(os.Getuid()), Valid: true})
	if err != nil {
		t.Fatal(err)
	}
	if id.Dispatch != "m-sre-w1-x:work" || id.Member != "sre" || id.MemberRole != config.MemberRoleWorker || id.CaseID != "c3" {
		t.Fatalf("token identity = %+v", id)
	}
}
