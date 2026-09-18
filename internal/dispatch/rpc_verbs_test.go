package dispatch

import (
	"testing"

	"github.com/NodeSpy/conductor/internal/plugin"
)

func verbs(names ...string) *plugin.Decl {
	d := &plugin.Decl{}
	for _, n := range names {
		d.Verbs = append(d.Verbs, plugin.Verb{Name: n})
	}
	return d
}

// SpeaksBackendRPC is the dialect gate: a runtime plugin is Backend-RPC only if
// it declares EVERY required verb. Missing one → treated as ACP-dialect.
func TestSpeaksBackendRPC(t *testing.T) {
	if !SpeaksBackendRPC(verbs(RequiredVerbs...)) {
		t.Fatal("the full required verb set must be recognized as Backend-RPC")
	}
	// A superset (extra verbs, e.g. logs) still qualifies.
	if !SpeaksBackendRPC(verbs(append(append([]string{}, RequiredVerbs...), "logs", "extra")...)) {
		t.Fatal("a superset of the required verbs must still qualify")
	}
	// Missing any one required verb disqualifies.
	for _, drop := range RequiredVerbs {
		var kept []string
		for _, v := range RequiredVerbs {
			if v != drop {
				kept = append(kept, v)
			}
		}
		if SpeaksBackendRPC(verbs(kept...)) {
			t.Fatalf("missing %q must disqualify", drop)
		}
	}
	if SpeaksBackendRPC(verbs("echo")) {
		t.Fatal("an unrelated verb set (ACP-dialect) must not qualify")
	}
	if SpeaksBackendRPC(nil) {
		t.Fatal("nil decl must not qualify")
	}
}
