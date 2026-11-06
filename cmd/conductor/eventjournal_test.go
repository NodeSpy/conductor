package main

import (
	"context"
	"testing"

	"github.com/NodeSpy/conductor/internal/attention"
	"github.com/NodeSpy/conductor/internal/core"
	"github.com/NodeSpy/conductor/internal/dashboard"
	"github.com/NodeSpy/conductor/internal/flow"
	"github.com/NodeSpy/conductor/internal/journal"
	"github.com/NodeSpy/conductor/internal/notify"
)

// TestEscalationRaisesAttentionUntilARunSucceeds: an escalation still reaches
// the lifecycle source, raises an attention item, and a later successful run
// of the same kind on the same target resolves it.
func TestEscalationRaisesAttentionUntilARunSucceeds(t *testing.T) {
	j, err := journal.Open(journal.Options{})
	if err != nil {
		t.Fatal(err)
	}
	att := attention.New(attention.Options{Journal: j})
	var forwarded []string
	pub := lifecyclePublisher(att, func(_ context.Context, event string, _ core.Trigger, _ string, _ map[string]any) {
		forwarded = append(forwarded, event)
	})
	tr := core.Trigger{Kind: "fix", Target: core.Target{Repo: "o/r", Number: 3}}
	pub(context.Background(), notify.EventEscalate, tr, "gave up", nil)
	pub(context.Background(), notify.EventComplete, tr, "done", nil)

	if len(forwarded) != 2 {
		t.Fatalf("forwarded %v", forwarded)
	}
	items, _ := att.Snapshot()
	if len(items) != 1 || items[0].Kind != notify.EventEscalate || items[0].Severity != attention.Urgent || items[0].Title != "Escalated: fix on o/r#3" {
		t.Fatalf("items = %+v", items)
	}
	dashboard.RecordRunEvent(j, att, flow.RunEvent{Type: "run_done", Status: "ok", Run: "h", Kind: "fix", Repo: "o/r", Number: 4})
	if items, _ = att.Snapshot(); len(items) != 1 {
		t.Fatal("a run on another target resolved the escalation")
	}
	dashboard.RecordRunEvent(j, att, flow.RunEvent{Type: "run_done", Status: "ok", Run: "h", Kind: "fix", Repo: "o/r", Number: 3})
	if items, _ = att.Snapshot(); len(items) != 0 {
		t.Fatalf("still open: %+v", items)
	}
}
