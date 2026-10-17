package controller

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/NodeSpy/conductor/internal/agentsandbox"
	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/core"
	"github.com/NodeSpy/conductor/internal/dispatch"
	"github.com/NodeSpy/conductor/internal/gitdiff"
)

func TestSandboxApplies(t *testing.T) {
	old := AgentSandbox
	t.Cleanup(func() { AgentSandbox = old })
	AgentSandbox = nil
	agent := withAgent(launchOpts{}, "claude", nil, false)
	if sandboxApplies("", agent) {
		t.Fatal("off when not wired")
	}
	AgentSandbox = &SandboxWiring{}
	if !sandboxApplies("", agent) {
		t.Fatal("a local agent launch is sandboxed by default")
	}
	if sandboxApplies("box", agent) || sandboxApplies("", launchOpts{}) {
		t.Fatal("remote launches and non-agent launches are not")
	}
	for mode, want := range map[string]bool{"none": false, "user": false, "container": true, "namespace": true} {
		o := agent
		o.iso = &config.IsolationConfig{Mode: mode}
		if got := sandboxApplies("", o); got != want {
			t.Errorf("mode %s: %v, want %v", mode, got, want)
		}
	}
	o := agent
	o.iso = &config.IsolationConfig{Mode: "namespace", Privileged: true}
	if sandboxApplies("", o) {
		t.Fatal("privileged namespace keeps its own wrapper")
	}
}

func TestDispatchIdentity(t *testing.T) {
	req := dispatch.Request{DispatchID: "d1", Action: config.Action{Agent: "fixer"},
		Trigger: core.Trigger{Target: core.Target{Repo: "acme/app"}, TargetTrusted: true}}
	if got := DispatchIdentity(req, "claude-code"); got != "agent:fixer@acme/app" {
		t.Fatal(got)
	}
	req.Trigger.TargetTrusted = false
	if got := DispatchIdentity(req, "claude-code"); got != "agent:fixer@dispatch-d1" {
		t.Fatalf("a sender-chosen target never shares the real repo's tool state: %s", got)
	}
	req.Action.Agent = ""
	req.Trigger = core.Trigger{TargetTrusted: true}
	if got := DispatchIdentity(req, "codex"); got != "agent:runtime-codex" {
		t.Fatal(got)
	}
}

func TestNonCredentialEnv(t *testing.T) {
	got := strings.Join(nonCredentialEnv([]string{"GIT_AUTHOR_NAME=Op", "GH_TOKEN=x", "GITHUB_TOKEN=x", "MY_API_KEY=x", "AWS_PROFILE=p",
		"FOO_MODE=fast", "NPM_TOKEN=x", "SSH_AUTH_SOCK=/s", "BASIC_AUTH=x", "GIT_AUTHOR_EMAIL=o@x", "HOME=/h", "MY_SECRET=x"}), " ")
	if got != "GIT_AUTHOR_NAME=Op FOO_MODE=fast GIT_AUTHOR_EMAIL=o@x" {
		t.Fatal(got)
	}
}

// conductor's own diff read of a sandboxed workspace runs sandboxed: a clean
// filter the agent planted in its checkout's config runs inside the
// sandbox, never as conductor on the host.
func TestConfinedGitRunsAPlantedFilterInsideTheSandbox(t *testing.T) {
	if why := agentsandbox.Probe(); why != "" || testing.Short() {
		t.Skipf("no sandbox here: %s", why)
	}
	root, err := agentsandbox.SocketDir("cgt")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(root) })
	ws := filepath.Join(root, "ws")
	marker := filepath.Join(root, "PWNED")
	git := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = ws
		cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_AUTHOR_NAME=a", "GIT_AUTHOR_EMAIL=a@x", "GIT_COMMITTER_NAME=a", "GIT_COMMITTER_EMAIL=a@x")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	os.MkdirAll(ws, 0o700)
	git("init", "-q", "-b", "main")
	os.WriteFile(filepath.Join(ws, "a.txt"), []byte("one\n"), 0o644)
	git("add", ".")
	git("commit", "-q", "-m", "x")
	// The agent's plant: a clean filter on every file, and a change for
	// `git diff HEAD` to run it on.
	os.WriteFile(filepath.Join(ws, ".gitattributes"), []byte("* filter=evil\n"), 0o644)
	git("config", "filter.evil.clean", "sh -c 'touch "+marker+"; cat'")
	os.WriteFile(filepath.Join(ws, "a.txt"), []byte("two\n"), 0o644)

	self := buildConductor(t)
	oldSelf, oldSB := launchSelfExe, AgentSandbox
	t.Cleanup(func() { launchSelfExe, AgentSandbox, gitdiff.Confine = oldSelf, oldSB, nil })
	launchSelfExe = func() (string, error) { return self, nil }
	AgentSandbox = &SandboxWiring{Resolver: &agentsandbox.Resolver{StateRoot: filepath.Join(root, "st"), GOOS: runtime.GOOS}}
	os.MkdirAll(AgentSandbox.Resolver.StateRoot, 0o700)
	unreg := agentsandbox.Default.Register(agentsandbox.Entry{Workspace: ws, Identity: "agent:t"})
	defer unreg()
	gitdiff.Confine = ConfinedGit

	if o, ok, err := ConfinedGit(context.Background(), ws, []string{"diff", "HEAD"}); !ok || err != nil {
		t.Fatalf("confined git diff: %q %v %v", o, ok, err)
	}
	diff, err := gitdiff.Proposed(context.Background(), ws, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(diff, "+two") {
		t.Fatalf("the diff is still read: %q", diff)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("the planted filter ran on the host, as conductor")
	}
	// Control: unconfined, the same read runs the plant on the host — what
	// the sandbox is preventing above.
	gitdiff.Confine = nil
	if _, err := gitdiff.Proposed(context.Background(), ws, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatal("control: the plant should fire unconfined (the test is not exercising anything)")
	}
}

func buildConductor(t *testing.T) string {
	t.Helper()
	if runtime.GOOS != "linux" {
		return "/usr/bin/true"
	}
	bin := filepath.Join(t.TempDir(), "conductor")
	if out, err := exec.Command("go", "build", "-o", bin, "github.com/NodeSpy/conductor/cmd/conductor").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	return bin
}
