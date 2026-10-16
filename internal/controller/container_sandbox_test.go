package controller

import (
	"context"
	"io"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/NodeSpy/conductor/internal/agentsandbox"
	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/sandbox"
)

// recEngine records engine calls and serves a real container mux in-process.
type recEngine struct {
	mu   sync.Mutex
	cmds []string
	dir  string
}

func (e *recEngine) Bin() string  { return "rec-engine" }
func (e *recEngine) Kind() string { return "docker" }
func (e *recEngine) Output(_ context.Context, args ...string) ([]byte, error) {
	e.mu.Lock()
	e.cmds = append(e.cmds, strings.Join(args, " "))
	e.mu.Unlock()
	if args[0] == "image" {
		return []byte(`["PATH=/bin"]` + "\tsha256:1\n"), nil
	}
	return nil, nil
}
func (e *recEngine) Start(...string) (io.Reader, io.WriteCloser, func() error, error) {
	h2cR, h2cW := io.Pipe()
	c2hR, c2hW := io.Pipe()
	done := make(chan error, 1)
	go func() { done <- sandbox.ServeContainerMux(h2cR, c2hW, e.dir); c2hW.Close() }()
	return c2hR, h2cW, func() error { return <-done }, nil
}
func (e *recEngine) ran(prefix string) int {
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

// A cli/acp launch with isolation: {mode: container} runs in its identity's
// container; `mode: namespace` named explicitly keeps the jail even when the
// default backend is a container; no backend refuses.
func TestPrepareAgentSandboxContainer(t *testing.T) {
	root := t.TempDir()
	e := &recEngine{dir: t.TempDir()}
	c := &agentsandbox.Containers{Engine: e, DaemonID: "t", UID: 1000, GID: 1000, Helper: t.TempDir(), IdleStop: time.Hour, Root: filepath.Join(root, "c")}
	t.Cleanup(c.Close)
	old := AgentSandbox
	t.Cleanup(func() { AgentSandbox = old })
	AgentSandbox = &SandboxWiring{Resolver: &agentsandbox.Resolver{StateRoot: filepath.Join(root, "st"), GOOS: sandbox.CheckGOOS}, Containers: c}

	ws := t.TempDir()
	defer agentsandbox.Default.Forget(ws)
	opt := withAgent(launchOpts{iso: &config.IsolationConfig{Mode: "container", Image: "img:1"}}, "probe", nil, false)
	argv, _, env, cleanup, err := prepareAgentSandbox(ws, nil, []string{"/usr/local/bin/probe", "--x"}, opt)
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(argv, " ")
	if !strings.HasPrefix(got, "rec-engine exec -i --user 1000:1000") || !strings.HasSuffix(got, "-- probe --x") {
		t.Errorf("a container launch: %s", got)
	}
	if e.ran("create") != 1 || e.ran("start") != 1 {
		t.Errorf("engine calls: %v", e.cmds)
	}
	for _, kv := range env {
		if strings.HasPrefix(kv, "NO_PROXY=") && kv != "NO_PROXY=*" {
			t.Errorf("client env %s", kv)
		}
	}
	cleanup()
	if e.ran("rm -f") != 1 {
		t.Error("the per-launch container outlived its launch")
	}

	// The default backend is a container; an explicit namespace block keeps
	// the jail (the engine is not asked for a container).
	AgentSandbox.Resolver.Container = &agentsandbox.Container{Image: "img:1"}
	before := e.ran("create")
	ws2 := t.TempDir()
	defer agentsandbox.Default.Forget(ws2)
	opt = withAgent(launchOpts{iso: &config.IsolationConfig{Mode: "namespace"}}, "probe", nil, false)
	argv, _, _, cleanup, err = prepareAgentSandbox(ws2, nil, []string{"/bin/true"}, opt)
	if err == nil {
		cleanup()
		if strings.HasPrefix(strings.Join(argv, " "), "rec-engine") {
			t.Error("mode: namespace ran in a container")
		}
	}
	if e.ran("create") != before {
		t.Error("mode: namespace asked the engine for a container")
	}

	// No container backend: refused, never unsandboxed.
	AgentSandbox.Containers = nil
	ws3 := t.TempDir()
	defer agentsandbox.Default.Forget(ws3)
	opt = withAgent(launchOpts{iso: &config.IsolationConfig{Mode: "container"}}, "probe", nil, false)
	if _, _, _, _, err := prepareAgentSandbox(ws3, nil, []string{"probe"}, opt); err == nil || !strings.Contains(err.Error(), "not launched unsandboxed") {
		t.Errorf("no backend: %v", err)
	}
}
