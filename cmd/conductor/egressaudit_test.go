package main

import (
	"strings"
	"testing"
	"time"

	"github.com/NodeSpy/conductor/internal/sandbox"
)

// Every containment event is audited; a detection or a limit kill also
// reaches the operator.
func TestSandboxEventsAreAuditedAndDetectionsNotified(t *testing.T) {
	var rows []map[string]any
	var notes []string
	ev := sandboxEventAudit(func(string, ...any) {}, func(r map[string]any) { rows = append(rows, r) },
		func(m string) { notes = append(notes, m) })
	ev("detection", map[string]any{"run": "d1", "kind": "tunnel_tool", "reason": "ssh", "action": "kill"})
	ev("leftover_killed", map[string]any{"run": "d1", "processes": 2})
	ev("limit", map[string]any{"run": "d2", "kind": "disk_low", "reason": "3% free", "action": "kill"})
	if len(rows) != 3 || rows[0]["event"] != "sandbox_detection" || rows[1]["event"] != "sandbox_leftover_killed" || rows[0]["run"] != "d1" {
		t.Fatalf("audit rows %v", rows)
	}
	if len(notes) != 2 || !strings.Contains(notes[0], "tunnel_tool") || !strings.Contains(notes[0], "run d1") || !strings.Contains(notes[1], "disk_low") {
		t.Fatalf("notifications %q", notes)
	}
	sandboxEventAudit(func(string, ...any) {}, func(map[string]any) {}, nil)("detection", map[string]any{}) // no notifier: audit only
}

func TestEgressAuditRecordsSessions(t *testing.T) {
	var rows []map[string]any
	var logged int
	a := egressAudit(func(string, ...any) { logged++ }, func(r map[string]any) { rows = append(rows, r) })
	a(sandbox.ConnRecord{Run: "d1", Target: "x.test:443", Outcome: "denied"})
	a(sandbox.ConnRecord{Run: "d1", Target: "x.test:443", Outcome: "closed", Up: 10, Down: 20, Duration: time.Second, SNI: "x.test"})
	a(sandbox.ConnRecord{Run: "d1", Target: "y.test:22", Outcome: "refused", Reason: "not TLS"})
	if len(rows) != 2 || rows[0]["sni"] != "x.test" || rows[0]["up"] != int64(10) || rows[1]["reason"] != "not TLS" {
		t.Fatalf("rows %v", rows)
	}
	if logged != 1 {
		t.Fatalf("only a refusal or a closure with a reason is logged: %d", logged)
	}
}
