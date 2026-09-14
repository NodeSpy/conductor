package connector

import (
	"context"
	"strings"
	"testing"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/core"
)

// ROUND-13 #4. `kind` came off the wire verbatim, so a source plugin could
// emit `_closed` or `failing_checks` — kinds the ENGINE interprets as facts
// about a target: consuming its engagements, settling its outcome, re-running
// its CI with the operator's token. The bundled integrations produce those
// from payloads they verified; a plugin asserting one is claiming a fact
// about somebody else's world.
func TestPluginSourceCannotEmitAReservedKind(t *testing.T) {
	for _, tc := range []struct {
		name        string
		event       map[string]any
		wantKind    string
		wantDropped bool
	}{
		{name: "a reserved kind on a declared event",
			event: map[string]any{"event": "ticket", "kind": "_closed"}, wantKind: "ticket"},
		{name: "failing_checks",
			event: map[string]any{"event": "ticket", "kind": "failing_checks"}, wantKind: "ticket"},
		{name: "its own event name is fine",
			event: map[string]any{"event": "ticket", "kind": "ticket_opened"}, wantKind: "ticket_opened"},
		{name: "no kind falls back to the event",
			event: map[string]any{"event": "ticket"}, wantKind: "ticket"},
		{name: "a reserved EVENT name is dropped",
			event: map[string]any{"event": "_closed"}, wantDropped: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var logged strings.Builder
			p := &pluginSourceIntegration{
				source:   fakeSourcer{events: []map[string]any{tc.event}},
				instance: "acme", typ: "acme",
				triggers: []CompiledTrigger{{Spec: config.TriggerSpec{On: "acme.ticket"}}},
				log:      func(f string, a ...any) { logged.WriteString(f) },
			}
			var got []core.Trigger
			if err := p.Start(context.Background(), func(_ context.Context, tr core.Trigger) {
				got = append(got, tr)
			}); err != nil {
				t.Fatal(err)
			}
			if tc.wantDropped {
				if len(got) != 0 {
					t.Fatalf("a reserved event name must be dropped, got %+v", got)
				}
				return
			}
			if len(got) != 1 {
				t.Fatalf("expected one trigger, got %d", len(got))
			}
			if got[0].Kind != tc.wantKind {
				t.Fatalf("kind=%q want %q — a plugin must not name a kind the engine interprets",
					got[0].Kind, tc.wantKind)
			}
			if core.ReservedKind(got[0].Kind) {
				t.Fatalf("a reserved kind reached the engine: %q", got[0].Kind)
			}
		})
	}
}
