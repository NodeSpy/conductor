//go:build linux || darwin

package controller

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/NodeSpy/conductor/internal/agentsandbox"
	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/core"
	"github.com/NodeSpy/conductor/internal/dispatch"
	"github.com/NodeSpy/conductor/internal/sandbox"
)

// fakeClaude stands in for claude-code inside the real sandbox: a fresh
// session writes its transcript under $HOME, as claude does; --resume finds
// it there or fails as claude does when it is not.
const fakeClaude = `#!/bin/sh
sid=; res=
while [ $# -gt 0 ]; do
  case "$1" in --session-id) sid=$2; shift ;; --resume) res=$2; shift ;; esac
  shift
done
cat >/dev/null
d="$HOME/.claude/projects/wt"
if [ -n "$res" ]; then
  if [ ! -f "$d/$res.jsonl" ]; then echo "No conversation found with session ID: $res"; exit 1; fi
  printf '{"session_id":"%s","result":"resumed HOME=%s"}\n' "$res" "$HOME"
  exit 0
fi
mkdir -p "$d" && echo '{}' > "$d/$sid.jsonl"
printf '{"session_id":"%s","result":"started HOME=%s"}\n' "$sid" "$HOME"
`

// A sandboxed cli step's agent, resumed after a daemon restart, runs in the
// home its session was created in — under its new dispatch id — and finds
// its transcript there. Without the recorded home it lands in a fresh one
// and claude has no conversation to resume.
func TestResumedCLIStepFindsItsSessionInItsHome(t *testing.T) {
	if why := agentsandbox.Probe(); why != "" || testing.Short() {
		t.Skipf("no sandbox here: %s", why)
	}
	root, err := agentsandbox.SocketDir("rh")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(root) })
	if r, err := filepath.EvalSymlinks(root); err == nil {
		root = r
	}
	ws := filepath.Join(root, "wt")
	os.MkdirAll(ws, 0o700)
	bin := filepath.Join(ws, "claude")
	if err := os.WriteFile(bin, []byte(fakeClaude), 0o755); err != nil {
		t.Fatal(err)
	}

	self := buildConductor(t)
	pm := sandbox.NewProxyManager(nil)
	rel, err := agentsandbox.NewRelayer()
	if err != nil {
		t.Fatal(err)
	}
	oldSelf, oldSB, oldReg := launchSelfExe, AgentSandbox, agentsandbox.Default
	oldU, oldA, oldUS, oldAS := EgressProxyUnix, EgressProxyFor, EgressProxyUnixSession, EgressProxyForSession
	t.Cleanup(func() {
		launchSelfExe, AgentSandbox, agentsandbox.Default = oldSelf, oldSB, oldReg
		EgressProxyUnix, EgressProxyFor, EgressProxyUnixSession, EgressProxyForSession = oldU, oldA, oldUS, oldAS
		pm.Close()
		rel.Close()
	})
	launchSelfExe = func() (string, error) { return self, nil }
	EgressProxyUnix, EgressProxyFor, EgressProxyUnixSession, EgressProxyForSession = pm.UnixEndpoint, pm.Endpoint, pm.UnixEndpointSession, pm.EndpointSession
	state := filepath.Join(root, "agent-state")
	regFile := filepath.Join(root, "registry.json")

	// daemon wires a (re)started daemon's sandbox from what is on disk, and
	// its cli runtime.
	daemon := func() *cliController {
		t.Helper()
		agentsandbox.Default = &agentsandbox.Registry{}
		if err := agentsandbox.Default.PersistTo(regFile); err != nil {
			t.Fatal(err)
		}
		AgentSandbox = &SandboxWiring{Resolver: &agentsandbox.Resolver{StateRoot: state, GOOS: runtime.GOOS,
			Persist: agentsandbox.PersistNone, Env: os.Environ()}, Relay: rel.Relay}
		return newCLIController("cc", config.ControllerConfig{Transport: "cli", Tool: "claude-code", Bin: bin}, nil)
	}
	step := func(dispatchID string, resume *dispatch.AgentSession) dispatch.Request {
		req := makeReq("pr_review", "fix it")
		req.Trigger = core.Trigger{Kind: "pr_review", TargetTrusted: true, Target: core.Target{Repo: "acme/app", PR: 7, Number: 7}}
		req.DispatchID, req.Resume = dispatchID, resume
		req.OnAgentStart = func(dispatch.AgentStart) {}
		return req
	}
	homeOf := func(s Session) string {
		t.Helper()
		out := s.(interface{ Output() string }).Output()
		_, home, ok := strings.Cut(out, "HOME=")
		if !ok {
			t.Fatalf("no HOME in the agent's output %q (turn: %v)", out, s.(interface{ TurnErr() error }).TurnErr())
		}
		return strings.TrimSpace(home)
	}

	// The first daemon launches the step; it records its session.
	s1, err := daemon().NewSession(context.Background(), Spec{Request: step("rOld:address", nil), Cwd: ws}, nil)
	if err != nil {
		t.Fatal(err)
	}
	waitSession(t, s1)
	sid := s1.(ResumableSession).ResumeID()
	home := homeOf(s1)
	if sid == "" || filepath.Dir(home) != filepath.Join(state, ".dispatch-homes") {
		t.Fatalf("session %q in home %q", sid, home)
	}
	if _, err := os.Stat(filepath.Join(home, ".claude", "projects", "wt", sid+".jsonl")); err != nil {
		t.Fatalf("the session's transcript is not in its home: %v", err)
	}

	// A restarted daemon resumes it under the resumed run's dispatch id,
	// with the home the flow recorded for the session.
	prior := &dispatch.AgentSession{Runtime: "cc", SessionID: sid, Cwd: ws, CanResume: true, Home: "rOld:address"}
	s2, err := daemon().ResumeStep(context.Background(), Spec{Request: step("rNew:address", prior), Cwd: ws}, sid, nil)
	if err != nil {
		t.Fatal(err)
	}
	waitSession(t, s2)
	if err := s2.(ResumeFailer).ResumeErr(); err != nil {
		t.Fatalf("resume failed: %v", err)
	}
	if got := homeOf(s2); got != home {
		t.Fatalf("resumed launch HOME = %s, want the session's %s", got, home)
	}

	// Control: a resume with no recorded home (what every resume was) lands
	// in a fresh home, where claude finds no conversation.
	noHome := *prior
	noHome.Home = ""
	s3, err := daemon().ResumeStep(context.Background(), Spec{Request: step("rNewer:address", &noHome), Cwd: ws}, sid, nil)
	if err != nil {
		t.Fatal(err)
	}
	waitSession(t, s3)
	if s3.(ResumeFailer).ResumeErr() == nil {
		t.Fatal("control: a resume in a fresh home found the session (the test exercises nothing)")
	}
}
