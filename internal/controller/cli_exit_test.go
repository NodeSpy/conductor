package controller

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/dispatch"
)

// A cli launch's sandbox (its egress credential, its containment watcher) is
// released when its process exits, not only when its session closes: a
// session outlives its turns, and a watcher left on an ended launch kept
// reporting — and escalating — what happened to its workspace afterwards.
func TestCLILaunchIsReleasedWhenItsProcessExits(t *testing.T) {
	stubPlatform(t)
	var released atomic.Int32
	old := EgressProxyFor
	EgressProxyFor = func([]string) (string, string, func(), error) {
		return "127.0.0.1:1", "cred", func() { released.Add(1) }, nil
	}
	t.Cleanup(func() { EgressProxyFor = old })
	c := newCLIController("r", config.ControllerConfig{Tool: "claude", Isolation: &config.IsolationConfig{
		Network: &config.IsolationNetwork{Egress: []string{"api.example.com:443"}}}}, nil)
	exit := make(chan struct{})
	c.launch = func(context.Context, string, []string, []string, string) (cliProc, error) {
		return &blockedProc{exit: exit}, nil
	}
	sess, err := c.NewSession(context.Background(), Spec{Request: dispatch.Request{}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close(context.Background())
	time.Sleep(20 * time.Millisecond)
	if released.Load() != 0 {
		t.Fatal("released while its process still runs")
	}
	close(exit) // the agent exits (it finished, or was killed); the session stays open
	deadline := time.Now().Add(2 * time.Second)
	for released.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if n := released.Load(); n != 1 {
		t.Fatalf("the launch was released %d times once its process exited, want 1", n)
	}
}

type blockedProc struct{ exit chan struct{} }

func (p *blockedProc) Wait() (string, error) { <-p.exit; return "", nil }
func (p *blockedProc) Kill() error           { return nil }
