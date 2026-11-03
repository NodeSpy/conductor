package main

import (
	"fmt"

	"github.com/NodeSpy/conductor/internal/sandbox"
)

// sandboxEventAudit records an agent sandbox's containment event in the
// audit log and the daemon's log. A detection (but an allowed one) or a
// resource-limit kill is also an attention event (notify, when set): the operator hears of it
// through their conductor.escalate triggers.
func sandboxEventAudit(logf func(string, ...any), audit func(map[string]any), notify func(msg string)) func(string, map[string]any) {
	return func(kind string, fields map[string]any) {
		// A sandbox exec launch's egress records are the same rows as the
		// daemon's own launches' (egress, egress_denied).
		ev := "sandbox_" + kind
		if kind == "egress" || kind == "egress_denied" {
			ev = kind
		}
		row := map[string]any{"event": ev}
		for k, v := range fields {
			row[k] = v
		}
		if ev != "egress" || fields["reason"] != nil {
			logf("sandbox: %s %v", kind, fields)
		}
		audit(row)
		// An allowed detection (the launch's allowance covers it) is
		// audited and logged, not escalated.
		if notify != nil && (kind == "detection" || kind == "limit") && fields["action"] != "allowed" {
			msg := fmt.Sprintf("agent sandbox %s: %v (%v) — action %v", kind, fields["kind"], fields["reason"], fields["action"])
			if run, _ := fields["run"].(string); run != "" {
				msg += ", run " + run
			}
			notify(msg)
		}
	}
}

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
		if r.AllowedBy != "" {
			// A tunnel the launch's allowance let skip a check (raw TCP to
			// a non_tls host): visible in the log too.
			row["allowed_by"] = r.AllowedBy
			logf("sandbox: egress %s %s (run %s): non-TLS, allowed by %s", r.Outcome, r.Target, r.Run, r.AllowedBy)
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
