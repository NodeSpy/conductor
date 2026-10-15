package agentsandbox

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/NodeSpy/conductor/internal/sandbox"
)

func wrapPolicy(t *testing.T) Policy {
	t.Helper()
	p := fakePolicy(t, "agent:a", true)
	p.Tool = "claude-code"
	p.Socket, p.Token = "/host/state/skill.sock", "tok-SECRET"
	p.Env = []string{"GIT_AUTHOR_NAME=Op"}
	p.Container.Session = &ContainerSession{
		Engine: "/usr/bin/docker", Name: "conductor-d-agent_a-1", User: "1000:1000", LaunchID: "L1",
		Helper: sandbox.ContainerHelperDir + "/conductor",
		Env:    []string{"HTTPS_PROXY=http://conductor:PCRED@127.0.0.1:40000", "NO_PROXY=127.0.0.1,localhost,::1", "CONDUCTOR_ENDPOINT=unix:///run/conductor/s/conductor.sock", "CONDUCTOR_SKILL_TOKEN=tok-SECRET"},
	}
	return p
}

// The exec argv carries no secret; the client's environment carries each
// one, by the name the argv passes through, and nothing of the operator's
// credentials.
func TestWrapContainer(t *testing.T) {
	p := wrapPolicy(t)
	inherited := []string{
		"PATH=/usr/bin", "HOME=/home/op", "DOCKER_HOST=unix:///var/run/docker.sock", "XDG_RUNTIME_DIR=/run/user/1000",
		"ANTHROPIC_API_KEY=sk-SECRET", "GH_TOKEN=gh-SECRET", "SSH_AUTH_SOCK=/tmp/agent", "AWS_SECRET_ACCESS_KEY=aws-SECRET", "TERM=xterm",
	}
	l, err := WrapContainer(p, []string{"/home/op/.local/bin/claude", "-p", "hi"}, Deps{Inherited: inherited})
	if err != nil {
		t.Fatal(err)
	}
	argv := strings.Join(l.Argv, " ")
	for _, secret := range []string{"SECRET", "PCRED"} {
		if strings.Contains(argv, secret) {
			t.Errorf("a secret on the command line: %s", argv)
		}
	}
	wantPrefix := "/usr/bin/docker exec -i --user 1000:1000 --workdir " + p.Workspace + " "
	if !strings.HasPrefix(argv, wantPrefix) {
		t.Errorf("argv %s", argv)
	}
	if !strings.HasSuffix(argv, "conductor-d-agent_a-1 "+sandbox.ContainerHelperDir+"/conductor sandbox-net --run L1 -- claude -p hi") {
		t.Errorf("argv tail: %s", argv)
	}
	for _, kv := range []string{"--env HOME=" + sandbox.ContainerHome, "--env TMPDIR=/tmp", "--env ANTHROPIC_API_KEY", "--env CONDUCTOR_SKILL_TOKEN", "--env HTTPS_PROXY", "--env CONDUCTOR_ENDPOINT", "--env GIT_AUTHOR_NAME", "--env TERM"} {
		if !strings.Contains(argv+" ", kv+" ") {
			t.Errorf("argv lacks %q: %s", kv, argv)
		}
	}
	for _, k := range []string{"GH_TOKEN", "SSH_AUTH_SOCK", "AWS_SECRET_ACCESS_KEY", "DOCKER_HOST", "XDG_RUNTIME_DIR", "--env PATH"} {
		if strings.Contains(argv, k) {
			t.Errorf("argv passes %s into the container: %s", k, argv)
		}
	}
	env := map[string]string{}
	for _, kv := range l.Env {
		k, v, _ := strings.Cut(kv, "=")
		env[k] = v
	}
	if env["ANTHROPIC_API_KEY"] != "sk-SECRET" || env["CONDUCTOR_SKILL_TOKEN"] != "tok-SECRET" || env["HTTPS_PROXY"] != "http://conductor:PCRED@127.0.0.1:40000" {
		t.Errorf("the client lacks a passed-through value: %v", env)
	}
	if env["CONDUCTOR_ENDPOINT"] != "unix:///run/conductor/s/conductor.sock" {
		t.Errorf("the conductor endpoint is the host's: %q", env["CONDUCTOR_ENDPOINT"])
	}
	if env["HOME"] != "/home/op" || env["DOCKER_HOST"] != "unix:///var/run/docker.sock" {
		t.Errorf("the engine client lost its own HOME/DOCKER_HOST: %v", env)
	}
	// The engine CLI never goes through the agent's proxy (a tcp:// engine):
	// its own exemption is everything; the agent's is on the command line.
	if env["NO_PROXY"] != "*" || env["no_proxy"] != "*" {
		t.Errorf("the client would send its engine traffic through the agent's proxy: NO_PROXY=%q", env["NO_PROXY"])
	}
	if !strings.Contains(argv, "--env NO_PROXY=127.0.0.1,localhost,::1") {
		t.Errorf("the agent's NO_PROXY is not its own: %s", argv)
	}
	if _, ok := env["GH_TOKEN"]; ok {
		t.Error("GH_TOKEN reached the client env (and so could be passed)")
	}
	if l.Dir != p.Workspace || l.Signal == nil || l.Cleanup == nil {
		t.Errorf("launch %+v", l)
	}
}

// A command inside the workspace keeps its path; launcher temp files and
// an unprepared policy refuse.
func TestWrapContainerRefusals(t *testing.T) {
	p := wrapPolicy(t)
	script := filepath.Join(p.Workspace, "run.sh")
	l, err := WrapContainer(p, []string{script}, Deps{})
	if err != nil || l.Argv[len(l.Argv)-1] != script {
		t.Errorf("a workspace command: %v %v", l, err)
	}
	tmp := filepath.Join(os.TempDir(), "paseo-ext-"+nonce(3)+".ts")
	_ = os.WriteFile(tmp, []byte("x"), 0o600)
	defer os.Remove(tmp)
	if _, err := WrapContainer(p, []string{"pi", "--extension", tmp}, Deps{}); err == nil || !strings.Contains(err.Error(), "temp dir") {
		t.Errorf("launcher file: %v", err)
	}
	q := wrapPolicy(t)
	q.Container.Session = nil
	if _, err := WrapContainer(q, []string{"claude"}, Deps{}); err == nil {
		t.Error("unprepared")
	}
	if _, err := WrapContainer(p, nil, Deps{}); err == nil {
		t.Error("nothing to launch")
	}
	q = wrapPolicy(t)
	q.Container.Session.User = "0:0"
	if _, err := WrapContainer(q, []string{"claude"}, Deps{}); err == nil {
		t.Error("root")
	}
	// Wrap routes a container policy here (after Validate: no socket file).
	p.Socket = ""
	if l, err := Wrap(p, []string{"claude"}, Deps{}); err != nil || !strings.Contains(strings.Join(l.Argv, " "), " exec -i ") {
		t.Errorf("Wrap: %v %v", l, err)
	}
}

// Signal and Cleanup reach the launch's group in its container through
// the engine.
func TestWrapContainerSignal(t *testing.T) {
	dir := t.TempDir()
	log := filepath.Join(dir, "log")
	eng := filepath.Join(dir, "engine")
	_ = os.WriteFile(eng, []byte("#!/bin/sh\necho \"$@\" >> "+log+"\n"), 0o755)
	p := wrapPolicy(t)
	p.Container.Session.Engine = eng
	l, err := WrapContainer(p, []string{"claude"}, Deps{Inherited: []string{"PATH=/usr/bin:/bin"}})
	if err != nil {
		t.Fatal(err)
	}
	l.Signal("TERM")
	l.Cleanup()
	deadline := time.Now().Add(5 * time.Second)
	for {
		b, _ := os.ReadFile(log)
		got := strings.TrimSpace(string(b))
		want := "exec --user 1000:1000 conductor-d-agent_a-1 " + sandbox.ContainerHelperDir + "/conductor sandbox-net --signal L1 TERM\n" +
			"exec --user 1000:1000 conductor-d-agent_a-1 " + sandbox.ContainerHelperDir + "/conductor sandbox-net --signal L1 KILL"
		if got == want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("engine got\n%s\nwant\n%s", got, want)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
