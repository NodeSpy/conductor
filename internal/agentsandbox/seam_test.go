package agentsandbox

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The seam's shape is what other branches build against (the browser's
// engine mirrors ResolveEngine and Ping, and moves onto them): a change here
// is a change to them, so it breaks this build first.
var (
	_ func(context.Context, string) (*CLIEngine, error)                                = FindEngine
	_ func(string, string, func(string) (string, error), []string) (*CLIEngine, error) = ResolveEngine
	_ func(*CLIEngine, context.Context) error                                          = (*CLIEngine).Ping
	_ func(*CLIEngine, context.Context) bool                                           = (*CLIEngine).Rootless
	_ func() *Containers                                                               = DaemonContainers
	_ func(*Containers, context.Context, LaunchSpec) (*Launch, error)                  = (*Containers).Launch
)

var (
	_ = LaunchSpec{Identity: "", Workspace: "", Home: "", Container: Container{}, Egress: nil, Loopback: nil, Expose: nil, Env: nil, Argv: nil, Inherited: nil}
	_ = Launch{Argv: nil, Env: nil, Dir: "", Cleanup: nil, Exposed: nil}
)

// fakeEngineBin makes PATH an engine CLI named name that runs script, and
// nothing else (a runner's own podman or docker in /usr/bin is not found).
func fakeEngineBin(t *testing.T, name, script string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\n"+script+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
}

// FindEngine: an engine that is installed and answers — not one that is
// missing, or installed but not running (Docker Desktop not started).
func TestFindEngine(t *testing.T) {
	ctx := context.Background()
	fakeEngineBin(t, "docker", "exit 0")
	if _, err := FindEngine(ctx, "podman"); err == nil {
		t.Error("found a podman that is not installed")
	}
	e, err := FindEngine(ctx, "docker")
	if err != nil || e.Kind() != "docker" || !strings.HasSuffix(e.Bin(), "/docker") {
		t.Fatalf("an answering docker: %v %v", e, err)
	}
	if e.Rootless(ctx) {
		t.Error("docker reported rootless")
	}
	fakeEngineBin(t, "docker", `case "$1" in info) echo true;; esac; exit 0`)
	if e, err := FindEngine(ctx, "docker"); err != nil || e.Rootless(ctx) {
		t.Errorf("only podman is rootless (keep-id), whatever docker says: %v", err)
	}
	if _, err := ResolveEngine("nerdctl", "linux", func(string) (string, error) { return "/usr/bin/nerdctl", nil }, nil); err == nil {
		t.Error("an engine that is neither docker nor podman was accepted")
	}
	fakeEngineBin(t, "docker", `echo "Cannot connect to the Docker daemon" >&2; exit 1`)
	if _, err := FindEngine(ctx, "docker"); err == nil || !strings.Contains(err.Error(), "not answering") {
		t.Errorf("a docker that does not answer: %v", err)
	}
	fakeEngineBin(t, "podman", `case "$1" in info) echo true;; esac; exit 0`)
	e, err = FindEngine(ctx, "")
	if err != nil || e.Kind() != "podman" || !e.Rootless(ctx) {
		t.Errorf("rootless podman: %v %v", e, err)
	}
}

// Launch: the same prepared container, an exec into it, and a cleanup that
// ends the launch's share (a per-launch container goes, with its HOME).
func TestContainersLaunchSeam(t *testing.T) {
	e := newFakeEngine(t)
	c := fakeContainers(t, e)
	ws := t.TempDir()
	l, err := c.Launch(context.Background(), LaunchSpec{
		Identity: "browser:op", Workspace: ws,
		Expose:    []string{"127.0.0.1:9222"},
		Env:       []string{"BROWSER_FLAGS=--headless"},
		Argv:      []string{"chromium", "--remote-debugging-port=9222"},
		Inherited: []string{"PATH=/usr/bin", "HOME=/home/op"},
	})
	if err != nil {
		t.Fatal(err)
	}
	argv := strings.Join(l.Argv, " ")
	if !strings.HasPrefix(argv, "fake-engine exec -i --user 1000:1000") || !strings.HasSuffix(argv, "-- chromium --remote-debugging-port=9222") || !strings.Contains(argv, "--env BROWSER_FLAGS") {
		t.Errorf("argv %s", argv)
	}
	if l.Exposed["127.0.0.1:9222"] == "" {
		t.Error("the exposed port has no host socket")
	}
	homes, _ := filepath.Glob(filepath.Join(c.Root, "home-*"))
	if len(homes) != 1 || len(e.containers) != 1 {
		t.Fatalf("homes %v containers %d", homes, len(e.containers))
	}
	l.Cleanup()
	if len(e.containers) != 0 {
		t.Error("the per-launch container outlived its launch")
	}
	if _, err := os.Stat(homes[0]); err == nil {
		t.Error("the launch's HOME outlived it")
	}
	if _, err := c.Launch(context.Background(), LaunchSpec{Workspace: ws, Argv: []string{"x"}}); err == nil {
		t.Error("launched with no identity")
	}
	var none *Containers
	if _, err := none.Launch(context.Background(), LaunchSpec{Identity: "x", Workspace: ws, Argv: []string{"x"}}); err == nil {
		t.Error("launched with no backend")
	}
	// The daemon's backend, for a caller outside the agent path.
	SetDaemonContainers(c)
	if DaemonContainers() != c {
		t.Error("DaemonContainers")
	}
	SetDaemonContainers(nil)
	if DaemonContainers() != nil {
		t.Error("cleared")
	}
}
