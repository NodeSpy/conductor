package controller

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/NodeSpy/conductor/internal/acp"
	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/dispatch"
	"github.com/NodeSpy/conductor/internal/jail"
	"github.com/NodeSpy/conductor/internal/memory"
	"github.com/NodeSpy/conductor/internal/skill"
)

// withTestJail wires a jail manager that can lay a launch out without a
// kernel (Prepare only plans binds; nothing is mounted until the launch runs)
// and reports the jail as available (up=true) or not.
func withTestJail(t *testing.T, up bool) {
	t.Helper()
	oldM, oldP := JailManager, jailProbe
	t.Cleanup(func() { JailManager, jailProbe = oldM, oldP })
	// A short root: the broker socket lives under it, and a unix socket path
	// is capped at 104 bytes on macOS (t.TempDir() there is a long
	// /var/folders/… path, and the bind fails).
	root, err := os.MkdirTemp("/tmp", "cj")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(root) })
	JailManager = &jail.Manager{
		Root:     root,
		Home:     t.TempDir(),
		SelfExe:  func() (string, error) { return "/bin/true", nil },
		LookPath: func(string) (string, error) { return "", errors.New("not installed") },
	}
	jailProbe = func() string {
		if up {
			return ""
		}
		return "no user namespaces here"
	}
}

// withSkillSurface publishes a tool socket and an active skill broker, so
// dispatch.SkillEnv mints a session for a launch.
func withSkillSurface(t *testing.T) {
	t.Helper()
	memory.Reset()
	t.Cleanup(memory.Reset)
	memory.SetToolCommand([]string{"/usr/local/bin/conductor", "mcp", "memory", "--socket", "/state/memory.sock", "--no-memory"})
	skill.SetActive(skill.NewBroker(func(string) (string, bool) { return "", false }, nil))
	t.Cleanup(func() { skill.SetActive(nil) })
}

func jailedCLILaunch(t *testing.T, up bool) launchCall {
	t.Helper()
	withTestJail(t, up)
	withSkillSurface(t)
	l := &fakeLauncher{out: func([]string) (string, error) { return `{"session_id":"s-1","result":"ok"}`, nil }}
	c := newCLIController("r", config.ControllerConfig{Transport: "cli", Tool: "claude-code"}, nil)
	c.launch = l.launch
	req := makeReq("merge_conflict", "fix the conflict"+dispatch.WriteWrapperGuidance)
	req.DispatchID = "d-jail-1"
	req.Trigger.TargetTrusted = true
	sess, err := c.NewSession(context.Background(), Spec{Request: req, Cwd: t.TempDir()}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if w, ok := sess.(waiter); ok {
		w.Wait(context.Background(), 0)
	}
	_ = sess.Close(context.Background())
	if l.count() == 0 {
		t.Fatal("no launch")
	}
	return l.call(0)
}

// A jailed launch describes the jail: commits, pushes and host commands go
// through conductor, and no instruction points at a token variable or SSH.
func TestJailedPromptCarriesNoTokenOrSSHInstructions(t *testing.T) {
	call := jailedCLILaunch(t, true)
	if !strings.Contains(call.stdin, dispatch.JailedIdentityGuidance) {
		t.Fatalf("the jailed launch must carry the jailed identity guidance:\n%s", call.stdin)
	}
	for _, bad := range []string{"TOKEN", "GH_TOKEN", "GITHUB_TOKEN", "PC_GH_APP_TOKEN", "SSH", "ssh", "$"} {
		if strings.Contains(call.stdin, bad) {
			t.Errorf("the jailed prompt must not mention %q:\n%s", bad, call.stdin)
		}
	}
	if !strings.Contains(call.stdin, "SCOPE: your writes are bound to THIS target") {
		t.Error("the target scope guidance is kept in the jail")
	}
}

// Unjailed — including a default jail that degraded on this box — the
// prompt keeps today's identity text, byte for byte.
func TestUnjailedPromptIsUnchanged(t *testing.T) {
	call := jailedCLILaunch(t, false)
	want := "fix the conflict" + dispatch.WriteWrapperGuidance
	if !strings.Contains(call.stdin, want) {
		t.Fatalf("the unjailed prompt changed:\n%s", call.stdin)
	}
	if strings.Contains(call.stdin, dispatch.JailedIdentityGuidance) {
		t.Fatal("an unjailed launch must not get the jailed text")
	}
}

// The conductor skill surface survives the jail: the launch env carries the
// dispatch's own endpoint and session token (bound server-side to the uid and
// dispatch), while the GitHub tokens are gone.
func TestJailedLaunchKeepsTheDispatchSkillEnv(t *testing.T) {
	call := jailedCLILaunch(t, true)
	env := strings.Join(call.env, "\n")
	if !strings.Contains(env, "CONDUCTOR_ENDPOINT=unix:///state/memory.sock") {
		t.Errorf("jailed launch lost CONDUCTOR_ENDPOINT:\n%s", env)
	}
	if !strings.Contains(env, "CONDUCTOR_SKILL_TOKEN=") {
		t.Errorf("jailed launch lost its skill token:\n%s", env)
	}
	for _, gone := range []string{"GH_TOKEN=", "GITHUB_TOKEN=", "PC_GH_APP_TOKEN=", "PC_GH_WRITE_TOKEN="} {
		if strings.Contains(env, gone) {
			t.Errorf("%s must not reach the jail", gone)
		}
	}
	// The daemon's OWN inherited skill variables never ride along.
	base := strings.Join(scrubJailEnv([]string{"CONDUCTOR_ENDPOINT=unix:///other", "CONDUCTOR_SKILL_TOKEN=inherited"}, "claude-code"), "\n")
	if strings.Contains(base, "CONDUCTOR_") {
		t.Errorf("inherited skill variables must be dropped: %s", base)
	}
}

// A controller dispatch is bound for step.done: in flight while its Dispatch
// runs, then resolvable to its session until the session is forgotten.
func TestControllerRunnerBindsDispatchForStepDone(t *testing.T) {
	l := &fakeLauncher{out: func([]string) (string, error) { return `{"session_id":"s-1","result":"ok"}`, nil }}
	c := newCLIController("r", config.ControllerConfig{Transport: "cli", Tool: "claude-code"}, nil)
	c.launch = l.launch
	r := newControllerRunner(c, nil, nil)
	req := makeReq("merge_conflict", "fix it")
	req.DispatchID = "d-7"
	req.Interactive = true // a background hand-off: Dispatch returns with the session live
	ref, err := r.Dispatch(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if r.DispatchInFlight("d-7") {
		t.Fatal("a dispatch whose Dispatch call returned is not in flight")
	}
	if got := r.AgentForDispatch("d-7"); got == "" || got != ref.AgentID {
		t.Fatalf("AgentForDispatch = %q, want %q", got, ref.AgentID)
	}
	if r.AgentForDispatch("d-other") != "" {
		t.Fatal("an unknown dispatch resolves to nothing")
	}
	if err := r.Archive(context.Background(), ref.AgentID); err != nil {
		t.Fatal(err)
	}
	if r.AgentForDispatch("d-7") != "" {
		t.Fatal("an archived session no longer resolves")
	}
}

// A recipe that passes the prompt as an argument (an operator `command:`)
// gets the jailed identity in that argument, inside the jail wrapper.
func TestJailedArgvPromptCarriesTheJailedIdentity(t *testing.T) {
	withTestJail(t, true)
	l := &fakeLauncher{}
	c := newCLIController("x", config.ControllerConfig{Transport: "cli", Tool: "claude-code", Command: []string{"mytool", "--flag"}}, nil)
	c.launch = l.launch
	req := makeReq("merge_conflict", "go"+dispatch.WriteWrapperGuidance)
	req.Trigger.TargetTrusted = true
	sess, err := c.NewSession(context.Background(), Spec{Request: req, Cwd: t.TempDir()}, nil)
	if err != nil {
		t.Fatal(err)
	}
	waitSession(t, sess)
	argv := strings.Join(l.call(0).argv, " ")
	if !strings.Contains(argv, "conductor signs each commit") {
		t.Fatalf("the jailed argv prompt must carry the jailed identity: %.300s", argv)
	}
	if strings.Contains(argv, "GH_TOKEN/GITHUB_TOKEN are MY token") {
		t.Fatal("the jailed argv prompt still carries the token/SSH identity")
	}
}

// A foreground dispatch is in flight for step.done for as long as its turn
// runs — from before the session opens.
func TestControllerRunnerForegroundDispatchIsInFlight(t *testing.T) {
	var r *controllerRunner
	var during bool
	l := &fakeLauncher{out: func([]string) (string, error) {
		during = r.DispatchInFlight("d-fg")
		return `{"session_id":"s-1","result":"ok"}`, nil
	}}
	c := newCLIController("r", config.ControllerConfig{Transport: "cli", Tool: "claude-code"}, nil)
	c.launch = l.launch
	r = newControllerRunner(c, nil, nil)
	req := makeReq("merge_conflict", "fix it")
	req.DispatchID = "d-fg"
	req.Wait = true
	if _, err := r.Dispatch(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if !during {
		t.Fatal("the dispatch must be in flight while its foreground turn runs")
	}
	if r.DispatchInFlight("d-fg") {
		t.Fatal("no longer in flight once Dispatch returns")
	}
}

// ACP sends its first prompt over the protocol once the (jailed) agent is up:
// that prompt carries the jailed identity; an unjailed one today's text.
func TestACPJailedPromptCarriesTheJailedIdentity(t *testing.T) {
	for _, jailed := range []bool{true, false} {
		agent := &fakeACPAgent{initResult: acp.InitializeResult{ProtocolVersion: acp.ProtocolVersion}, sessionID: "s-1"}
		c := newACPController("gem", config.ControllerConfig{Command: []string{"fake"}}, nil)
		c.dial = dialFake(agent)
		c.dialJailed = jailed
		sess, err := c.NewSession(context.Background(), Spec{Request: makeReq("merge_conflict", "fix it"+dispatch.WriteWrapperGuidance), Cwd: "/wt"}, nil)
		if err != nil {
			t.Fatal(err)
		}
		if as, ok := sess.(*acpSession); ok {
			as.Wait(context.Background(), 0)
		}
		_ = sess.Close(context.Background())
		agent.mu.Lock()
		got := agent.gotPrompt
		agent.mu.Unlock()
		want, not := dispatch.JailedIdentityGuidance, dispatch.WriteWrapperGuidance
		if !jailed {
			want, not = not, want
		}
		if !strings.Contains(got, want) || strings.Contains(got, not) {
			t.Errorf("jailed=%v: wrong identity guidance in the ACP prompt:\n%s", jailed, got)
		}
	}
}
