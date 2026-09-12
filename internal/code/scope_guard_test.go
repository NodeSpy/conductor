package code

import (
	"context"
	"strings"
	"testing"
)

// §1 (round-2 CRITICAL): the H8 guard was wired into the harvest and IPC
// paths but NOT the `run: code` memory binding. "global" is the SHARED
// bucket — injected into every opted-in agent's prompt on the daemon — so
// an agent-authored code step writing there leaks one repo's note into
// every repo's context.
func TestCodeMemoryRememberRefusesTheSharedScope(t *testing.T) {
	for _, scope := range []string{"global", "Global", " global "} {
		t.Run(scope, func(t *testing.T) {
			m := tempMem(t)
			e := &Executor{}
			_, err := e.Exec(context.Background(), Spec{Run: "js", Code: `
				return { v: ctx.memory.remember("leak", [], "` + scope + `") };`}, nil)
			if err == nil || !strings.Contains(err.Error(), "reserved scope") {
				t.Fatalf("run: code must refuse the shared scope, got %v", err)
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
	e := &Executor{}
	if _, err := e.Exec(context.Background(), Spec{Run: "js", Code: `
		return { v: ctx.memory.remember("fine", [], "acme/api") };`}, nil); err != nil {
		t.Fatalf("a named scope must still work: %v", err)
	}
	all, _ := m.List()
	if len(all) != 1 || all[0].Scope != "acme/api" {
		t.Fatalf("want one note scoped acme/api, got %+v", all)
	}
}
