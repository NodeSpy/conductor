package memory

import (
	"strings"
	"testing"
)

// ROUND-7 #2. handleIPC serves the MCP/CLI memory tool an agent drives
// directly — the THIRD agent-facing face. recall reached m.Recall with no
// guard at all and remember carried only the reserved-bucket check, so
// `allow_memory_scopes` and the own-scope rule did not apply to it: an agent
// read and wrote every tenant's memories through the tool it is handed by
// default.
//
// The round-5 meta-test enumerated two faces by hand and could not fail for a
// face nobody had listed. It discovers them now.
func TestIPCFaceEnforcesTheScopeAllowlist(t *testing.T) {
	m := testManager(t, NewMemBackend())
	// The operator's allowlist: one scope, plus whatever the dispatch owns.
	m.SetScopeGuard(func(c Caller, _, scope string) error {
		if scope == "repo:only/this-one" || (c.Repo != "" && scope == "repo:"+c.Repo) {
			return nil
		}
		return &testDenied{scope}
	})
	// A memory in the victim's scope, so the refusal is about the SCOPE.
	if _, err := m.Remember("victim's note", nil, "repo:victim/other", Source{}); err != nil {
		t.Fatal(err)
	}
	aud := func(map[string]any) {}
	logf := func(string, ...any) {}
	dispatch := Source{Step: "probe", Repo: "trigger/repo"}

	for _, tc := range []struct {
		name string
		req  IPCRequest
	}{
		{"remember into another tenant's scope", IPCRequest{
			Op: "remember", Text: "exfiltrate", Scope: "repo:victim/other", Source: dispatch}},
		{"recall another tenant's scope", IPCRequest{
			Op: "recall", Scope: "repo:victim/other", Source: dispatch}},
		{"an UNSCOPED recall, which reads every scope on the daemon", IPCRequest{
			Op: "recall", Source: dispatch}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resp := handleIPC(m, tc.req, Peer{}, aud, logf)
			if resp.Error == "" {
				t.Fatalf("%s was NOT refused — the MCP/CLI memory tool bypasses "+
					"allow_memory_scopes entirely (entries=%d)", tc.name, len(resp.Entries))
			}
			if !strings.Contains(resp.Error, "denied") {
				t.Errorf("refused for the wrong reason: %v", resp.Error)
			}
		})
	}

	// The dispatch's OWN scope, and the listed one, still work — or the tool
	// is broken rather than guarded.
	for _, req := range []IPCRequest{
		{Op: "remember", Text: "mine", Scope: "repo:trigger/repo", Source: dispatch},
		{Op: "remember", Text: "listed", Scope: "repo:only/this-one", Source: dispatch},
		{Op: "recall", Scope: "repo:trigger/repo", Source: dispatch},
	} {
		if resp := handleIPC(m, req, Peer{}, aud, logf); resp.Error != "" {
			t.Errorf("%s %q must be allowed: %v", req.Op, req.Scope, resp.Error)
		}
	}
}
