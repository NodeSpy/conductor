package controller

import (
	"context"
	"testing"

	"github.com/NodeSpy/conductor/internal/acp"
	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/memory"
)

// TestACPMemoryToolInjection: with memory configured (a tool command
// published at daemon boot), a new ACP session carries the conductor-memory
// MCP server with the dispatch's provenance baked into its flags; without
// memory, no MCP servers ride along; a remote host: session skips it (the
// daemon socket doesn't exist there).
func TestACPMemoryToolInjection(t *testing.T) {
	memory.Reset()
	t.Cleanup(memory.Reset)
	memory.Configure(memory.NewManager(memory.NewMemBackend()))
	memory.SetToolCommand([]string{"/usr/local/bin/conductor", "mcp", "memory", "--socket", "/data/memory.sock"})

	newSession := func(c *acpController, agentName string) *fakeACPAgent {
		t.Helper()
		agent := &fakeACPAgent{
			initResult: acp.InitializeResult{ProtocolVersion: acp.ProtocolVersion},
			sessionID:  "s-1",
		}
		c.dial = dialFake(agent)
		req := makeReq("merge_conflict", "fix it")
		req.Action.Agent = agentName
		sess, err := c.NewSession(context.Background(), Spec{Request: req, Cwd: "/wt"}, nil)
		if err != nil {
			t.Fatal(err)
		}
		if as, ok := sess.(*acpSession); ok {
			as.Wait(context.Background(), 0)
		}
		_ = sess.Close(context.Background())
		return agent
	}

	c := newACPController("gem", config.ControllerConfig{Command: []string{"fake"}}, nil)
	agent := newSession(c, "fixer")
	agent.mu.Lock()
	got := agent.gotMcp
	agent.mu.Unlock()
	if len(got) != 1 || got[0].Name != "conductor-memory" || got[0].Command != "/usr/local/bin/conductor" {
		t.Fatalf("mcp servers: %+v", got)
	}
	want := []string{"mcp", "memory", "--socket", "/data/memory.sock",
		"--agent", "fixer", "--repo", "o/r", "--trigger", "merge_conflict", "--number", "7"}
	if len(got[0].Args) != len(want) {
		t.Fatalf("args: %v, want %v", got[0].Args, want)
	}
	for i := range want {
		if got[0].Args[i] != want[i] {
			t.Fatalf("args[%d]: %v, want %v", i, got[0].Args, want)
		}
	}

	// A remote session must not receive the local socket's tool.
	cRemote := newACPController("gem", config.ControllerConfig{Command: []string{"fake"}, Host: "box"}, nil)
	agent = newSession(cRemote, "fixer")
	if len(agent.gotMcp) != 0 {
		t.Fatalf("remote session must skip the memory tool: %+v", agent.gotMcp)
	}

	// No memory configured → nothing injected.
	memory.Reset()
	c2 := newACPController("gem", config.ControllerConfig{Command: []string{"fake"}}, nil)
	agent = newSession(c2, "fixer")
	if len(agent.gotMcp) != 0 {
		t.Fatalf("unconfigured memory must inject nothing: %+v", agent.gotMcp)
	}
}
