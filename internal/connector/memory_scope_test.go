package connector

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/NodeSpy/conductor/internal/memory"
)

func tempMemForVerb(t *testing.T) *memory.Manager {
	t.Helper()
	memory.Reset()
	t.Cleanup(memory.Reset)
	m := memory.NewManager(memory.NewMemBackend())
	base := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)
	n := 0
	m.SetClock(func() time.Time { n++; return base.Add(time.Duration(n) * time.Second) }, nil)
	memory.Configure(m)
	return m
}

// §1 (round-2 CRITICAL): the `memory.remember` VERB takes an
// agent-supplied scope and had no guard — reachable by any
// `skill.verbs: [memory.*]` grant. "global" is the shared bucket every
// opted-in agent on the daemon reads.
func TestMemoryRememberVerbRefusesTheSharedScope(t *testing.T) {
	for _, scope := range []string{"global", "Global", " global "} {
		t.Run(scope, func(t *testing.T) {
			m := tempMemForVerb(t)
			_, err := memoryImpl{}.Invoke(context.Background(), "remember", map[string]any{
				"text": "leak", "scope": scope,
			})
			if err == nil || !strings.Contains(err.Error(), "reserved scope") {
				t.Fatalf("the memory.remember verb must refuse the shared scope, got %v", err)
			}
			if all, _ := m.List(); len(all) != 0 {
				t.Fatalf("nothing should have persisted: %+v", all)
			}
		})
	}
}

func TestMemoryRememberVerbAllowsANamedScope(t *testing.T) {
	m := tempMemForVerb(t)
	_, err := memoryImpl{}.Invoke(context.Background(), "remember", map[string]any{
		"text": "fine", "scope": "acme/api",
	})
	if err != nil {
		t.Fatalf("a named scope must still work: %v", err)
	}
	all, _ := m.List()
	if len(all) != 1 || all[0].Scope != "acme/api" {
		t.Fatalf("want one note scoped acme/api, got %+v", all)
	}
}
