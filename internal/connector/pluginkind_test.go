package connector

import (
	"context"
	"testing"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/core"
)

// There are no reserved kind names (plugin-contract.md §0): a name carries no
// meaning, so a plugin emitting `_closed` or `failing_checks` claims nothing —
// what the engine does with an event comes from its DECLARED semantics. A
// plugin that declares its events may emit only those (an undeclared kind
// falls back to its declared event's name); one that declares none is a
// plain source whose events are taken by name, with no semantics at all.
func TestPluginKindNamesCarryNoMeaning(t *testing.T) {
	for _, tc := range []struct {
		name        string
		declared    map[string]bool
		event       map[string]any
		wantKind    string
		wantDropped bool
	}{
		{name: "an undeclared kind on a declared event falls back to the event",
			declared: map[string]bool{"ticket": true},
			event:    map[string]any{"event": "ticket", "kind": "_closed"}, wantKind: "ticket"},
		{name: "a declared kind is kept",
			declared: map[string]bool{"ticket": true, "ticket_opened": true},
			event:    map[string]any{"event": "ticket", "kind": "ticket_opened"}, wantKind: "ticket_opened"},
		{name: "no kind is the event",
			declared: map[string]bool{"ticket": true},
			event:    map[string]any{"event": "ticket"}, wantKind: "ticket"},
		{name: "an undeclared event is dropped",
			declared: map[string]bool{"ticket": true},
			event:    map[string]any{"event": "_closed"}, wantDropped: true},
		{name: "a plain source's names are names",
			event: map[string]any{"event": "ticket", "kind": "failing_checks"}, wantKind: "failing_checks"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := &pluginSourceIntegration{
				source:   fakeSourcer{events: []map[string]any{tc.event}},
				instance: "acme", typ: "acme", declared: tc.declared,
				triggers: []CompiledTrigger{{Spec: config.TriggerSpec{On: "acme.ticket"}}},
				log:      func(string, ...any) {},
			}
			var got []core.Trigger
			if err := p.Start(context.Background(), func(_ context.Context, tr core.Trigger) {
				got = append(got, tr)
			}); err != nil {
				t.Fatal(err)
			}
			if tc.wantDropped {
				if len(got) != 0 {
					t.Fatalf("an undeclared event must be dropped, got %+v", got)
				}
				return
			}
			if len(got) != 1 || got[0].Kind != tc.wantKind {
				t.Fatalf("got %+v, want one %q", got, tc.wantKind)
			}
			// Whatever its name, an event that declares nothing gets no
			// engine behavior.
			if got[0].ClosesTarget() || got[0].VerificationFailed() || got[0].BoundToTarget() {
				t.Fatalf("a kind name conferred semantics: %+v", got[0].Semantics())
			}
		})
	}
}
