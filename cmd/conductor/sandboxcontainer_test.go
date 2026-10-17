package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/NodeSpy/conductor/internal/agentsandbox"
	"github.com/NodeSpy/conductor/internal/controller"
	"github.com/NodeSpy/conductor/internal/sandbox"
)

// slowEngine is a container engine whose `start` takes a while (an engine
// under load); everything else answers at once, and its exec serves a real
// container mux in-process.
type slowEngine struct {
	mu    sync.Mutex
	start time.Duration
	cmds  []string
	dir   string
}

func (e *slowEngine) Bin() string  { return "slow-engine" }
func (e *slowEngine) Kind() string { return "docker" }

func (e *slowEngine) Output(_ context.Context, args ...string) ([]byte, error) {
	e.mu.Lock()
	e.cmds = append(e.cmds, strings.Join(args, " "))
	e.mu.Unlock()
	switch args[0] {
	case "image":
		return []byte(`["PATH=/bin"]` + "\tsha256:1\n"), nil
	case "start":
		time.Sleep(e.start)
	}
	return nil, nil
}

func (e *slowEngine) Start(args ...string) (io.Reader, io.WriteCloser, func() error, error) {
	h2cR, h2cW := io.Pipe()
	c2hR, c2hW := io.Pipe()
	done := make(chan error, 1)
	go func() {
		done <- sandbox.ServeContainerMux(h2cR, c2hW, e.dir)
		c2hW.Close()
	}()
	return c2hR, h2cW, func() error { return <-done }, nil
}

func (e *slowEngine) ran(prefix string) int {
	e.mu.Lock()
	defer e.mu.Unlock()
	n := 0
	for _, c := range e.cmds {
		if strings.HasPrefix(c, prefix) {
			n++
		}
	}
	return n
}

// containerDaemon wires a resolver whose default backend is a container and
// a container backend on engine.
func containerDaemon(t *testing.T, engine agentsandbox.Engine, resolve func() (agentsandbox.Engine, error)) {
	t.Helper()
	oldR, oldW := sandboxResolver.Load(), controller.AgentSandbox
	t.Cleanup(func() { sandboxResolver.Store(oldR); controller.AgentSandbox = oldW })
	r := &agentsandbox.Resolver{Registry: &agentsandbox.Registry{}, StateRoot: t.TempDir(), GOOS: sandbox.CheckGOOS, Container: &agentsandbox.Container{Image: "img:1"}}
	sandboxResolver.Store(r)
	c := &agentsandbox.Containers{Engine: engine, Resolve: resolve, DaemonID: "t", UID: 1000, GID: 1000, Helper: t.TempDir(), IdleStop: time.Hour, Logf: t.Logf}
	t.Cleanup(c.Close)
	controller.AgentSandbox = &controller.SandboxWiring{Resolver: r, Containers: c}
}

// No engine: the daemon refuses the container policy — it never answers
// with a policy the launcher could run some other way.
func TestSandboxPolicyContainerNoEngine(t *testing.T) {
	containerDaemon(t, nil, func() (agentsandbox.Engine, error) {
		return nil, errors.New("no container engine (docker, podman) on PATH")
	})
	r, release := sandboxPolicyResponse(context.Background(), nil, controlRequest{Cwd: t.TempDir(), Tool: "claude"}, t.Logf)
	if r.Error == "" || r.Policy != nil || release != nil {
		t.Fatalf("no engine: %+v (release %v)", r, release != nil)
	}
	if !strings.Contains(r.Error, "unavailable") || !strings.Contains(r.Error, "not launched") {
		t.Errorf("the refusal: %s", r.Error)
	}
	controller.AgentSandbox.Containers = nil
	if r, release := sandboxPolicyResponse(context.Background(), nil, controlRequest{Cwd: t.TempDir(), Tool: "claude"}, t.Logf); r.Error == "" || r.Policy != nil || release != nil {
		t.Fatalf("no backend: %+v", r)
	}
}

// An engine slower than the control socket's usual deadline still gets its
// answer to the launcher; the launch's share lasts while the launcher holds
// the connection, and ends when it closes it.
func TestSandboxPolicyOutlivesTheControlDeadline(t *testing.T) {
	old := controlConnDeadline
	t.Cleanup(func() { controlConnDeadline = old })
	controlConnDeadline = 150 * time.Millisecond
	e := &slowEngine{start: 600 * time.Millisecond, dir: t.TempDir()}
	containerDaemon(t, e, nil)
	ws := t.TempDir()
	srv, cli := net.Pipe()
	done := make(chan struct{})
	go func() {
		handleControlConn(context.Background(), srv, nil, nil, nil, nil, nil, nil, t.Logf)
		close(done)
	}()
	_ = cli.SetDeadline(time.Now().Add(20 * time.Second))
	// A tool with no model endpoints: no egress to wire.
	if err := json.NewEncoder(cli).Encode(controlRequest{Cmd: "sandbox-policy", Cwd: ws, Tool: "probe"}); err != nil {
		t.Fatal(err)
	}
	var resp controlResponse
	if err := json.NewDecoder(cli).Decode(&resp); err != nil {
		t.Fatalf("no answer after a slow start: %v", err)
	}
	if resp.Error != "" || resp.Policy == nil || resp.Policy.Container == nil || resp.Policy.Container.Session == nil {
		t.Fatalf("answer %+v", resp)
	}
	// The launch holds its share: the per-launch container stays.
	time.Sleep(300 * time.Millisecond)
	if e.ran("rm") != 0 {
		t.Fatal("the container went while its launch held the connection")
	}
	select {
	case <-done:
		t.Fatal("the daemon let go of the launch")
	default:
	}
	cli.Close()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("the launch's share outlived its connection")
	}
	if e.ran("rm -f") != 1 {
		t.Errorf("the per-launch container was not removed with its launch: %v", e.cmds)
	}
}

// `sandbox exec` with a container policy: the engine's exec, no secret on
// its command line, the lease held for the launch and the launch's group
// killed after it.
func TestSandboxExecContainerLaunch(t *testing.T) {
	dir := t.TempDir()
	log := filepath.Join(dir, "engine.log")
	eng := filepath.Join(dir, "engine")
	script := "#!/bin/sh\n{ echo \"ARGS $*\"; echo \"TOKEN=$CONDUCTOR_SKILL_TOKEN\"; } >> " + log + "\ncase \"$*\" in *--signal*) exit 0;; esac\nexit 7\n"
	if err := os.WriteFile(eng, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	ws, home := t.TempDir(), t.TempDir()
	t.Chdir(ws)
	pol := &agentsandbox.Policy{Identity: "x", Workspace: ws, Home: home, Token: "tok-SECRET", Container: &agentsandbox.Container{
		Session: &agentsandbox.ContainerSession{Engine: eng, Name: "c1", User: "1000:1000", LaunchID: "L1", Helper: "/run/conductor/bin/conductor",
			Env: []string{"CONDUCTOR_ENDPOINT=unix:///run/conductor/s/conductor.sock", "CONDUCTOR_SKILL_TOKEN=tok-SECRET"}},
	}}
	var mu sync.Mutex
	closedAt := time.Time{}
	sockDir, err := agentsandbox.SocketDir("cct")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(sockDir) })
	sock := filepath.Join(sockDir, "control.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		var r controlRequest
		_ = json.NewDecoder(c).Decode(&r)
		_ = json.NewEncoder(c).Encode(controlResponse{OK: true, Policy: pol})
		_, _ = io.Copy(io.Discard, c) // the lease
		mu.Lock()
		closedAt = time.Now()
		mu.Unlock()
		c.Close()
	}()
	rc, errOut := stderrOf(t, func() int {
		return cmdSandboxExec([]string{"--socket", sock, "--", "/usr/local/bin/claude", "-p", "hi"})
	})
	if rc != 7 {
		t.Fatalf("the launch's status is not passed back: %d (%s)", rc, errOut)
	}
	b, _ := os.ReadFile(log)
	got := string(b)
	lines := strings.Split(strings.TrimSpace(got), "\n")
	if len(lines) != 4 {
		t.Fatalf("engine calls:\n%s", got)
	}
	want := "ARGS exec -i --user 1000:1000 --workdir " + ws
	if !strings.HasPrefix(lines[0], want) || !strings.HasSuffix(lines[0], "c1 /run/conductor/bin/conductor sandbox-net --run L1 -- claude -p hi") {
		t.Errorf("exec argv: %s", lines[0])
	}
	if strings.Contains(lines[0], "SECRET") || lines[1] != "TOKEN=tok-SECRET" {
		t.Errorf("the token travels by the client's environment, never the argv:\n%s", got)
	}
	if lines[2] != "ARGS exec --user 1000:1000 c1 /run/conductor/bin/conductor sandbox-net --signal L1 KILL" {
		t.Errorf("the launch's group not killed after it: %s", lines[2])
	}
	time.Sleep(100 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	if closedAt.IsZero() {
		t.Error("the lease connection was not closed when the launch ended")
	}
	// Unprepared: refused.
	pol.Container.Session = nil
	unprepared, _ := fakeControl(t, func(controlRequest) controlResponse { return controlResponse{OK: true, Policy: pol} })
	if rc, errOut := stderrOf(t, func() int { return cmdSandboxExec([]string{"--socket", unprepared, "--", "claude"}) }); rc != sandboxRefused || !strings.Contains(errOut, "did not prepare") {
		t.Errorf("unprepared container policy: %d %s", rc, errOut)
	}
}
