package code

import (
	"strings"
	"testing"
)

// §1 (round-2 CRITICAL): the H8 guard was wired into the harvest and IPC
// paths but NOT the code-step memory binding. "global" is the SHARED bucket
// — injected into every opted-in agent's prompt on the daemon — so a code
// step writing there leaks one repo's note into every repo's context.
//
// That binding is now the data plane (CtxHandler → memInvoke), which is
// where every engine's ctx.memory lands, so the rule is asserted there.
func TestCodeMemoryRememberRefusesTheSharedScope(t *testing.T) {
	for _, scope := range []string{"global", "Global", " global "} {
		t.Run(scope, func(t *testing.T) {
			m := tempMem(t)
			res := CtxHandler{}.Invoke(CtxRequest{Kind: CtxKindMemory,
				Op: "remember", Args: []any{"leak", []any{}, scope}})
			if res.OK || !strings.Contains(res.Error, "reserved scope") {
				t.Fatalf("a code step must not write the shared scope: %#v", res)
			}
			// Not a POLICY refusal: the reserved bucket is unconditional
			// rather than this execution's guard, and reads for itself.
			if res.Refused {
				t.Errorf("the reserved-bucket rule is not a DataGuard denial: %#v", res)
			}
			if all, _ := m.List(); len(all) != 0 {
				t.Fatalf("nothing should have persisted: %+v", all)
			}
		})
	}
}

// An ordinary scope still works — the guard is the reserved token only.
func TestCodeMemoryRememberAllowsANamedScope(t *testing.T) {
	m := tempMem(t)
	res := CtxHandler{}.Invoke(CtxRequest{Kind: CtxKindMemory,
		Op: "remember", Args: []any{"fine", []any{}, "acme/api"}})
	if !res.OK {
		t.Fatalf("a named scope must still work: %#v", res)
	}
	all, _ := m.List()
	if len(all) != 1 || all[0].Scope != "acme/api" {
		t.Fatalf("want one note scoped acme/api, got %+v", all)
	}
}
