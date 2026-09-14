package memory

import (
	"strings"
	"testing"
)

// H8: "global" normalizes to the SHARED bucket — the set injected into
// every opted-in agent's prompt on the daemon. An agent that writes there
// leaks one repo's note into every repo's context.
func TestAgentSuppliedGlobalScopeIsRejected(t *testing.T) {
	for _, s := range []string{"global", " global ", "GLOBAL"} {
		if err := CheckAgentScope(s); err == nil {
			t.Errorf("agent scope %q must be refused — it is the shared set", s)
		}
	}
	// Empty means "unscoped"; the caller supplies its own default.
	if err := CheckAgentScope(""); err != nil {
		t.Errorf("an empty scope is the caller's default, not an agent claim: %v", err)
	}
	for _, ok := range []string{"acme/api", "global-search", "myglobal"} {
		if err := CheckAgentScope(ok); err != nil {
			t.Errorf("scope %q should be allowed: %v", ok, err)
		}
	}
}

// Both agent-facing write paths enforce it: the output contract…
func TestHarvestRefusesTheSharedScope(t *testing.T) {
	m := testManager(t, NewMemBackend())
	_, err := m.HarvestOutput("```remember\n- text: a fact\n  scope: global\n```", Source{Step: "s"})
	if err == nil || !strings.Contains(err.Error(), "reserved scope") {
		t.Fatalf("the output contract must not reach the shared set, got %v", err)
	}
	all, _ := m.List()
	if len(all) != 0 {
		t.Fatalf("nothing should have persisted: %+v", all)
	}
}

// …and the live skill/MCP tool.
func TestIPCRememberRefusesTheSharedScope(t *testing.T) {
	m := testManager(t, NewMemBackend())
	authenticated(t, Source{Step: "s", Repo: "o/r", TargetTrusted: true}, 0)
	resp := handleIPC(m, IPCRequest{Op: "remember", Text: "a fact", Scope: "global",
		Token: "test-credential"}, Peer{}, func(map[string]any) {}, func(string, ...any) {})
	if resp.Error == "" || !strings.Contains(resp.Error, "reserved scope") {
		t.Fatalf("memory_remember must refuse the shared scope, got %+v", resp)
	}
	all, _ := m.List()
	if len(all) != 0 {
		t.Fatalf("nothing should have persisted: %+v", all)
	}
}
