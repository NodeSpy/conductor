package dispatch

import (
	"os"
	"testing"

	"github.com/NodeSpy/conductor/internal/core"
	"github.com/NodeSpy/conductor/internal/skill"
)

// TestSkillIdentityRequester: a dispatch's token says who asked — the
// operator only for a trigger they fired themselves.
func TestSkillIdentityRequester(t *testing.T) {
	id, ok := SkillIdentityFor(Request{Trigger: core.Trigger{Operator: true}})
	if !ok || id.Requester != skill.RequesterOperator {
		t.Fatalf("operator run: %q", id.Requester)
	}
	id, _ = SkillIdentityFor(Request{Trigger: core.Trigger{Source: "manual", Force: true}})
	if id.Requester != skill.RequesterEvent {
		t.Fatalf("a manual-looking trigger without the operator flag: %q", id.Requester)
	}
	id, _ = SkillIdentityFor(Request{Trigger: core.Trigger{Kind: "review_requested", TargetTrusted: true}})
	if id.Requester != skill.RequesterEvent {
		t.Fatalf("an event: %q", id.Requester)
	}
}

// TestSkillEnvForBindsTheSandbox: a sandboxed launch's token stands for its
// sandbox — the browser finds the workspace there, never by a lookup that a
// resumed registration could answer for another dispatch.
func TestSkillEnvForBindsTheSandbox(t *testing.T) {
	b := skill.NewBroker(nil, nil)
	skill.SetActive(b)
	defer skill.SetActive(nil)
	sb := &skill.SandboxBinding{Identity: "agent:x@o/r", Workspace: "/w"}
	env := SkillEnvFor(Request{DispatchID: "d2"}, "unix:///s", sb)
	tok := env["CONDUCTOR_SKILL_TOKEN"]
	// A sandboxed launch's token is minted PENDING (rt2 #12): root does not
	// exist yet at mint time (the env it goes into must be complete before
	// exec.Cmd.Start creates the process it belongs to), so nothing
	// authorizes it until the launcher binds the real root — here, this test
	// process standing in for the sandbox's root right after "Start".
	if _, err := b.Authorize(tok, skill.Peer{PID: os.Getpid(), Valid: true}); err == nil {
		t.Fatal("a pending sandbox token must not authorize before BindRoot")
	}
	if err := b.BindRoot(tok, os.Getpid()); err != nil {
		t.Fatalf("BindRoot: %v", err)
	}
	id, err := b.Authorize(tok, skill.Peer{PID: os.Getpid(), Valid: true})
	if err != nil || id.Sandbox == nil || id.Sandbox.Workspace != "/w" || id.Dispatch != "d2" {
		t.Fatalf("%+v %v", id, err)
	}
	env = SkillEnv(Request{DispatchID: "d3"}, "unix:///s")
	if id, _ := b.Authorize(env["CONDUCTOR_SKILL_TOKEN"], skill.Peer{}); id.Sandbox != nil {
		t.Fatal("an unsandboxed launch's token claims a sandbox")
	}
}
