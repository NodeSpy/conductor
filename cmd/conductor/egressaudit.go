package main

import (
	"github.com/NodeSpy/conductor/internal/sandbox"
)

// egressAudit records every egress proxy request in the audit log, by run:
// what an agent reached, how much it moved, and every refusal or closure
// with its reason (an allowlist denial is OnDeny's). A refusal or a
// tunnel closed for a reason (revoked, idle, a cap) is also logged.
func egressAudit(logf func(string, ...any), audit func(map[string]any)) func(sandbox.ConnRecord) {
	return func(r sandbox.ConnRecord) {
		if r.Outcome == "denied" {
			return
		}
		row := map[string]any{"event": "egress", "run": r.Run, "target": r.Target, "outcome": r.Outcome,
			"up": r.Up, "down": r.Down, "duration_ms": r.Duration.Milliseconds()}
		if r.SNI != "" {
			row["sni"] = r.SNI
		}
		if r.Reason != "" {
			row["reason"] = r.Reason
			logf("sandbox: egress %s %s (run %s): %s", r.Outcome, r.Target, r.Run, r.Reason)
		}
		if r.MaxIdle > 0 {
			row["max_idle_ms"] = r.MaxIdle.Milliseconds()
		}
		if r.ServerFirst > 0 {
			row["server_first"] = r.ServerFirst
		}
		audit(row)
	}
}
