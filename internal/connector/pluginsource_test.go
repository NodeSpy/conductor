package connector

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/core"
	"github.com/NodeSpy/conductor/internal/plugin"
)

// fakeSourcer streams a fixed set of pre-serialized events on StartSource.
type fakeSourcer struct{ events []map[string]any }

func (f fakeSourcer) StartSource(ctx context.Context, _ plugin.StartSourceRequest, emit func(json.RawMessage)) error {
	for _, e := range f.events {
		raw, _ := json.Marshal(e)
		emit(raw)
	}
	return nil
}

// TestPluginSourceMatchesAndResolvesAction proves the daemon-side adapter: a
// source plugin's streamed events are matched against the configured triggers
// (on: + filters) and lowered to a core.Trigger with the action resolved on the
// DAEMON side — the plugin never carries the action.
func TestPluginSourceMatchesAndResolvesAction(t *testing.T) {
	enabled := true
	trig := CompiledTrigger{Index: 0, Spec: config.TriggerSpec{
		On:      "sentry1.issue_alert",
		Name:    "",
		Enabled: &enabled,
		Filters: map[string]any{"level": []any{"error", "fatal"}},
	}}
	src := fakeSourcer{events: []map[string]any{
		{ // matches: level error, and correct event
			"event":   "issue_alert",
			"title":   "Boom in prod",
			"dedup":   "abc",
			"target":  map[string]any{"repo": "acme/app"},
			"context": map[string]any{"level": "error", "project": "web"},
		},
		{ // filtered out: level info not in [error,fatal]
			"event":   "issue_alert",
			"context": map[string]any{"level": "info"},
		},
		{ // wrong event name: no matching trigger
			"event":   "spike",
			"context": map[string]any{"level": "error"},
		},
	}}

	psi := &pluginSourceIntegration{
		source:   src,
		instance: "sentry1",
		typ:      "sentry",
		triggers: []CompiledTrigger{trig},
		log:      func(string, ...any) {},
	}

	var got []core.Trigger
	emit := func(_ context.Context, tr core.Trigger) { got = append(got, tr) }
	if err := psi.Start(context.Background(), emit); err != nil {
		t.Fatalf("Start: %v", err)
	}

	if len(got) != 1 {
		t.Fatalf("emitted %d triggers, want 1 (only the matching error alert)", len(got))
	}
	tr := got[0]
	if tr.Source != "sentry" || tr.Instance != "sentry1" || tr.Kind != "issue_alert" {
		t.Fatalf("trigger identity wrong: %+v", tr)
	}
	if tr.Title != "Boom in prod" || tr.Dedup != "abc" || tr.Target.Repo != "acme/app" {
		t.Fatalf("trigger payload not threaded: %+v", tr)
	}
	// The action was resolved DAEMON-side from the matched trigger (lowerAction),
	// not carried over the wire.
	act, ok := tr.Action.(config.Action)
	if !ok {
		t.Fatalf("Action is %T, want config.Action (resolved daemon-side)", tr.Action)
	}
	if act.FlowRef != trig.Ref() {
		t.Fatalf("Action.FlowRef = %q, want %q", act.FlowRef, trig.Ref())
	}
}
