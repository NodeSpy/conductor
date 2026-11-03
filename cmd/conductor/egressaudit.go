package main

import (
	"fmt"
	"strings"
	"unicode"

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
		if kind == "egress" || kind == "egress_denied" || kind == "model_request" {
			ev = kind
		}
		row := map[string]any{"event": ev}
		for k, v := range fields {
			row[k] = v
		}
		// A model request is logged only when it failed.
		quiet := ev == "egress" && fields["reason"] == nil
		if ev == "model_request" {
			st, _ := fields["status"].(int)
			if f, ok := fields["status"].(float64); ok { // forwarded by sandbox exec as JSON
				st = int(f)
			}
			quiet = st != 0 && st < 400
		}
		if !quiet {
			logf("sandbox: %s %v", kind, fields)
		}
		audit(row)
		// An allowed detection (the launch's allowance covers it) is
		// audited and logged, not escalated.
		if notify != nil && (kind == "detection" || kind == "limit") && fields["action"] != "allowed" {
			// Every field may carry what the agent chose (a process's argv, a
			// host): no chat markup survives into the operator's alert.
			msg := fmt.Sprintf("agent sandbox %s: %s (%s) — action %s", alertField(kind), alertField(fields["kind"]), alertField(fields["reason"]), alertField(fields["action"]))
			if run, _ := fields["run"].(string); run != "" {
				msg += ", run " + alertField(run)
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

// alertMax bounds one field of a sandbox alert.
const alertMax = 160

// alertMarkup neutralises chat markup: Slack's <!channel>, <@U…>, <#C…> and
// <url|text>, Discord's @everyone / @here, <@id> and [text](url), and the
// auto-linking of scheme://.
var alertMarkup = strings.NewReplacer(
	"<", "‹", ">", "›", "@", "@\u200b", "[", "⟦", "]", "⟧", "`", "'", "|", "¦", "://", "[:]//",
)

// alertField is v as one line of plain text for a notification: control
// characters (newlines included) collapsed, chat markup neutralised,
// truncated to alertMax runes.
func alertField(v any) string {
	s := strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || r == '\u2028' || r == '\u2029' {
			return ' '
		}
		return r
	}, fmt.Sprint(v))
	if r := []rune(s); len(r) > alertMax {
		s = string(r[:alertMax]) + "…"
	}
	return alertMarkup.Replace(s)
}
