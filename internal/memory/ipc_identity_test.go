package memory

import (
	"strings"
	"testing"
)

// A token-carrying memory op derives its provenance from the broker's identity
// for the token, NOT from a body-supplied Source a caller could spoof. This
// matters on every token-carrying transport — the local CLI socket and, for a
// remote agent, the SSH-forwarded socket (where the connection's peer identity
// is the ssh relay, so provenance MUST come from the token, not the peer).
func TestHandleIPCTokenBindsSource(t *testing.T) {
	m := testManager(t, NewMemBackend())
	SetLiveOps(LiveOps{
		Identify: func(token string, _ Peer) (Source, int, bool) {
			if token == "good" {
				return Source{Agent: "fixer", Repo: "o/r", Trigger: "review"}, 7, true
			}
			return Source{}, 0, false
		},
	})
	t.Cleanup(func() { SetLiveOps(LiveOps{}) })

	var audited []map[string]any
	audit := func(e map[string]any) { audited = append(audited, e) }

	// Body claims agent "evil"; the token maps to "fixer" — the write must be
	// attributed to "fixer".
	resp := handleIPC(m, IPCRequest{
		Op: "remember", Token: "good", Text: "note",
		Source: Source{Agent: "evil", Repo: "evil/repo"},
	}, Peer{}, audit, nil)
	if !resp.OK {
		t.Fatalf("remember: %+v", resp)
	}
	found := false
	for _, e := range audited {
		if e["event"] == "memory_remember" {
			found = true
			if e["agent"] != "fixer" || e["repo"] != "o/r" {
				t.Fatalf("write attributed to %v/%v, want fixer/o/r (body Source must be ignored)", e["agent"], e["repo"])
			}
		}
	}
	if !found {
		t.Fatalf("no memory_remember audit: %v", audited)
	}

	// A token the broker rejects → the op is denied, never falls back to the
	// body Source.
	if resp := handleIPC(m, IPCRequest{Op: "recall", Token: "bad", Substring: "x"}, Peer{}, nil, nil); resp.OK || !strings.Contains(resp.Error, "unauthorized session token") {
		t.Fatalf("recall with rejected token: %+v", resp)
	}
}
