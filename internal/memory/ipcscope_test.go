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
		// c.OwnRepo() — never a raw repo. It is "" for a dispatch whose
		// target the event's sender chose, which is the whole point.
		if scope == "repo:only/this-one" || (c.OwnRepo() != "" && scope == "repo:"+c.OwnRepo()) {
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
	// A TRUSTED dispatch: a source that assigned its own target. The forged
	// case has its own test below — it must NOT own its repo.
	dispatch := Source{Step: "probe", Repo: "trigger/repo", TargetTrusted: true}

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

// ROUND-10 #1. The IPC face built its Caller from req.Source.Repo RAW, so a
// dispatch whose target came from a webhook body — the repo the SENDER chose
// — still got implicit own-scope for it. Every other face had been taught the
// rule one round at a time; this one kept the raw repo, and `TargetTrusted`
// appeared nowhere in the file.
//
// The own-repo rule now lives in core.OwnRepo and is applied INSIDE
// NewAgentCaller, so there is no raw repo for a face to reach for.
func TestIPCFaceGrantsNoOwnScopeForAForgedTarget(t *testing.T) {
	m := testManager(t, NewMemBackend())
	m.SetScopeGuard(func(c Caller, _, scope string) error {
		if scope == "repo:only/this-one" || (c.OwnRepo() != "" && scope == "repo:"+c.OwnRepo()) {
			return nil
		}
		return &testDenied{scope}
	})
	if _, err := m.Remember("note", nil, "repo:victim/repo", Source{}); err != nil {
		t.Fatal(err)
	}
	aud, logf := func(map[string]any) {}, func(string, ...any) {}

	for _, tc := range []struct {
		name    string
		src     Source
		refused bool
	}{
		{"a platform-assigned target owns its scope",
			Source{Step: "probe", Repo: "victim/repo", TargetTrusted: true}, false},
		{"a target the request body chose owns nothing",
			Source{Step: "probe", Repo: "victim/repo"}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, op := range []IPCRequest{
				{Op: "remember", Text: "x", Scope: "repo:victim/repo", Source: tc.src},
				{Op: "recall", Scope: "repo:victim/repo", Source: tc.src},
			} {
				resp := handleIPC(m, op, Peer{}, aud, logf)
				if refused := resp.Error != ""; refused != tc.refused {
					t.Errorf("%s: %s refused=%v want %v (%v)", tc.name, op.Op, refused, tc.refused, resp.Error)
				}
			}
			// The operator's explicit list works either way — scoping a
			// forged-target dispatch is possible, it just has to be said.
			resp := handleIPC(m, IPCRequest{Op: "recall", Scope: "repo:only/this-one", Source: tc.src}, Peer{}, aud, logf)
			if resp.Error != "" {
				t.Errorf("%s: an allow-listed scope must work: %v", tc.name, resp.Error)
			}
		})
	}
}
