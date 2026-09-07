package main

import (
	"strings"
	"testing"

	"github.com/NodeSpy/conductor/internal/config"
)

// TestResolveCallableMCP (#57 T1) covers the `conductor mcp callable` face's OWN
// token resolution and tool filtering — the client-side gate the daemon later
// re-checks, but which stands on its own. Three behaviors: a token is required
// unless mcp_local opts out; an unknown token is rejected by name; and a scoped
// token sees only the workflows it is scoped to (deny-by-default), even when the
// config declares other callable workflows.
func TestResolveCallableMCP(t *testing.T) {
	yes := true
	cfg := &config.Config{
		Triggers: []config.TriggerSpec{
			{Name: "wfA", Callable: &yes},
			{Name: "wfB", Callable: &yes},
			{Name: "wfC"}, // not callable — never exposed
		},
		Callable: config.CallableConfig{
			Tokens: []config.CallableToken{
				{Name: "tokA", Bearer: "s", Workflows: []string{"wfA"}},
			},
		},
	}

	// (a) No token, mcp_local unset → required.
	if _, _, _, err := resolveCallableMCP(cfg, ""); err == nil || !strings.Contains(err.Error(), "is required") {
		t.Fatalf("no token without mcp_local must be refused as required, got %v", err)
	}

	// (b) Unknown token → rejected by name.
	if _, _, _, err := resolveCallableMCP(cfg, "ghost"); err == nil || !strings.Contains(err.Error(), "no callable token named") {
		t.Fatalf("unknown token must be rejected by name, got %v", err)
	}

	// (c) Scoped token sees only its own workflow — not the other callable one,
	// not the non-callable one.
	tools, tok, scoped, err := resolveCallableMCP(cfg, "tokA")
	if err != nil {
		t.Fatal(err)
	}
	if !scoped || tok.Name != "tokA" {
		t.Fatalf("scoped token must be resolved: scoped=%v tok=%q", scoped, tok.Name)
	}
	var got []string
	for _, tl := range tools {
		got = append(got, tl.Name)
	}
	if len(got) != 1 || got[0] != "wfA" {
		t.Fatalf("scoped tool list must contain only the token's workflows, got %v", got)
	}

	// mcp_local: no token exposes every callable workflow, unscoped.
	cfg.Callable.MCPLocal = true
	tools, _, scoped, err = resolveCallableMCP(cfg, "")
	if err != nil {
		t.Fatal(err)
	}
	if scoped {
		t.Fatal("mcp_local run must be unscoped")
	}
	got = nil
	for _, tl := range tools {
		got = append(got, tl.Name)
	}
	if len(got) != 2 || got[0] != "wfA" || got[1] != "wfB" {
		t.Fatalf("mcp_local must expose all callable workflows, got %v", got)
	}
}
