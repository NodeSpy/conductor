package dispatch

import (
	"context"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/NodeSpy/conductor/internal/core"
	sdk "github.com/NodeSpy/conductor/pkg/plugin"
)

// stopBackend is a Backend whose ListAgents answers by label and records
// archives; everything else is the embedded (nil) interface — StopTarget
// must not reach it.
type stopBackend struct {
	Backend
	agents   map[string][]AgentInfo // "pr|bound" -> agents ("1" or "")
	archived []string
	lists    int
}

func (b *stopBackend) ListAgents(_ context.Context, labels map[string]string) ([]AgentInfo, error) {
	if labels["pr"] != "" {
		b.lists++ // StopTarget's own listings (Archive lists by other labels)
	}
	if labels["conductor"] != "1" {
		return nil, nil
	}
	return b.agents[labels["pr"]+"|"+labels["bound"]], nil
}

func (b *stopBackend) ArchiveAgent(_ context.Context, id string) error {
	b.archived = append(b.archived, id)
	return nil
}

func (b *stopBackend) Inspect(context.Context, string) (AgentDetail, error) {
	return AgentDetail{}, nil
}

// The paseo runtime's StopTarget archives the closed target's BOUND agents
// only — never another agent on the same PR (a review), never another PR's —
// and only agents the ownership ledger says conductor launched.
func TestDispatcherStopTargetArchivesOnlyOwnedBoundAgents(t *testing.T) {
	be := &stopBackend{agents: map[string][]AgentInfo{
		"acme/app#42|1": {{ID: "fix-1"}, {ID: "fix-2"}, {ID: "not-ours"}},
		"acme/app#42|":  {{ID: "review-1"}},
		"acme/app#43|1": {{ID: "other-pr"}},
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
		t.Fatalf("archived %q, want only the owned bound agents of #42", got)
	}
	if be.lists != 1 {
		t.Fatalf("%d listings, want 1", be.lists)
	}
}

// The bound label is what StopTarget selects by: set exactly when the
// trigger declared bound_to_target.
func TestLabelArgsMarksBoundWork(t *testing.T) {
	for _, bound := range []bool{true, false} {
		tr := core.Trigger{Source: "github", Kind: "merge_conflict", Target: core.Target{Repo: "acme/app", Number: 1}}
		tr.Sem = &sdk.EventSemantics{BoundToTarget: bound}
		got := slices.Contains(labelArgs(Request{Trigger: tr}), "bound=1")
		if got != bound {
			t.Errorf("bound_to_target=%v: bound=1 label present=%v", bound, got)
		}
	}
}
