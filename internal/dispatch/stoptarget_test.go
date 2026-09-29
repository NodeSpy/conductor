package dispatch

import (
	"context"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// stopBackend is a Backend whose ListAgents answers by label and records
// archives; everything else is the embedded (nil) interface — StopTarget
// must not reach it.
type stopBackend struct {
	Backend
	agents   map[string][]AgentInfo // "pr|kind" -> agents
	archived []string
}

func (b *stopBackend) ListAgents(_ context.Context, labels map[string]string) ([]AgentInfo, error) {
	if labels["conductor"] != "1" {
		return nil, nil
	}
	return b.agents[labels["pr"]+"|"+labels["kind"]], nil
}

func (b *stopBackend) ArchiveAgent(_ context.Context, id string) error {
	b.archived = append(b.archived, id)
	return nil
}

func (b *stopBackend) Inspect(context.Context, string) (AgentDetail, error) {
	return AgentDetail{}, nil
}

// The paseo runtime's StopTarget archives the closed PR's FIXER agents only —
// never a reviewer on the same PR, never another PR's — and only agents the
// ownership ledger says conductor launched.
func TestDispatcherStopTargetArchivesOnlyOwnedFixers(t *testing.T) {
	be := &stopBackend{agents: map[string][]AgentInfo{
		"acme/app#42|merge_conflict":   {{ID: "fix-1"}},
		"acme/app#42|new_comment":      {{ID: "fix-2"}, {ID: "not-ours"}},
		"acme/app#42|review_requested": {{ID: "review-1"}},
		"acme/app#43|merge_conflict":   {{ID: "other-pr"}},
	}}
	d := &Dispatcher{Owned: NewOwnedSet(filepath.Join(t.TempDir(), "owned.json"))}
	for _, id := range []string{"fix-1", "fix-2", "review-1", "other-pr"} {
		d.Owned.AddAgent(id)
	}
	d.SetBackend(be)
	if n := d.StopTarget(context.Background(), "acme/app#42"); n != 2 {
		t.Fatalf("stopped %d, want 2", n)
	}
	sort.Strings(be.archived)
	if got := strings.Join(be.archived, ","); got != "fix-1,fix-2" {
		t.Fatalf("archived %q, want only the owned fixers of #42", got)
	}
}
