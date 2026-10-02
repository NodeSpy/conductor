// Package notify surfaces conductor activity to *you* — never to the PR. It
// logs to the daemon journal (so `journalctl --user -u conductor` shows it)
// and feeds the conductor.* lifecycle source, so a trigger can alert through
// any connector (Slack, Discord, ntfy, Pushover, Notifiarr, email, …) via its
// own verbs. It deliberately does NOT post comments on PRs: a handoff/
// escalation nudge is for you, and posting it publicly on someone's PR (as
// you) is noise/leakage.
package notify

import (
	"context"
	"fmt"

	"github.com/NodeSpy/conductor/internal/core"
	"github.com/NodeSpy/conductor/internal/secrets"
)

// Events.
const (
	EventDispatch   = "dispatch"
	EventComplete   = "complete"
	EventEscalate   = "escalate"
	EventNeedsInput = "needs_input" // a workflow handed a PR to a live agent; you need to weigh in
	EventFailed     = "failed"      // a run errored (distinct from escalate: gave up after retries)
	// EventUpdated / EventUpdateAvailable are the self-update lifecycle:
	// updated fires on the first boot of a new release; update_available
	// fires instead of self-applying under `update: { apply: workflow }`.
	EventUpdated         = "updated"
	EventUpdateAvailable = "update_available"
)

// Notifier emits notifications per the configured policy.
type Notifier struct {
	log   func(string, ...any)
	audit func(map[string]any) // optional: record attention events for status/report

	// publish feeds the event into the conductor.* lifecycle source (wired
	// by main to connector.EmitLifecycle) — every event, unconditionally:
	// the triggers do their own selection, and the source's loop guard
	// keeps a lifecycle run's own events from re-feeding.
	publish func(ctx context.Context, event string, t core.Trigger, line string, extra map[string]any)

	// resolver redacts tracked secret values from every outbound surface —
	// messages LEAVE the machine (via conductor.* triggers posting through a
	// connector) and land in the audit/journal too. nil passes through.
	resolver *secrets.Resolver
}

// SetSecrets wires the redaction resolver (main, once the stack exists).
func (n *Notifier) SetSecrets(r *secrets.Resolver) { n.resolver = r }

// redact scrubs tracked secret values from one outbound string.
func (n *Notifier) redact(s string) string {
	if n.resolver == nil {
		return s
	}
	return n.resolver.Redact(s)
}

// redactMap scrubs a fan-out data map (lifecycle extras).
func (n *Notifier) redactMap(m map[string]any) map[string]any {
	if n.resolver == nil || m == nil {
		return m
	}
	if out, ok := n.resolver.RedactValue(m).(map[string]any); ok {
		return out
	}
	return m
}

// SetPublisher wires the conductor.* lifecycle source.
func (n *Notifier) SetPublisher(publish func(ctx context.Context, event string, t core.Trigger, line string, extra map[string]any)) {
	n.publish = publish
}

// Publish emits a lifecycle event that has no ordinary composition — the
// self-update events, which carry a version. It journals, audits, and feeds
// the conductor.* source.
func (n *Notifier) Publish(ctx context.Context, event string, t core.Trigger, msg string, extra map[string]any) {
	// Redact ONCE, before any surface sees the message: journal, audit, and
	// the lifecycle source (whose triggers may post anywhere).
	msg = n.redact(msg)
	extra = n.redactMap(extra)
	n.log("notify [%s] %s", event, msg)
	if n.audit != nil {
		n.audit(map[string]any{"event": event, "msg": msg})
	}
	if n.publish != nil {
		n.publish(ctx, event, t, msg, extra)
	}
}

// New builds a Notifier. log is the structured logger (the journal); audit (may be
// nil) records attention events (escalate/needs_input/complete) to the audit log so
// `status` and `report` can surface them.
func New(log func(string, ...any), audit func(map[string]any)) *Notifier {
	if log == nil {
		log = func(string, ...any) {}
	}
	return &Notifier{log: log, audit: audit}
}

// Emit records a notification for the given event: the journal, always, plus
// the conductor.* lifecycle source so a configured trigger (`on:
// conductor.escalate`, …) can alert through any connector. The two attention
// events get an explicit, actionable line.
func (n *Notifier) Emit(ctx context.Context, event string, t core.Trigger, msg string) {
	// Redact ONCE, before ANY fan-out.
	msg = n.redact(msg)
	// Record attention/terminal events (escalate/needs_input/complete) for
	// status/report regardless of policy — dispatch is already captured
	// richly by the engine's own dispatch audit, so skip it here.
	if n.audit != nil && event != EventDispatch {
		n.audit(map[string]any{"event": event, "repo": t.Target.Repo,
			"number": t.Target.Number, "kind": t.Kind, "msg": msg})
	}
	ref := fmt.Sprintf("%s#%d", t.Target.Repo, t.Target.Number)
	var line string
	switch event {
	case EventEscalate:
		line = fmt.Sprintf("[escalate] %s %s gave up after retries — %s (open paseo)", ref, t.Kind, msg)
	case EventNeedsInput:
		line = fmt.Sprintf("[needs_input] %s %s handed to a live agent — %s (open paseo)", ref, t.Kind, msg)
	default:
		line = fmt.Sprintf("[%s] %s %s: %s", event, ref, t.Kind, msg)
	}
	n.log("notify %s", line)
	// The conductor.* lifecycle source sees EVERY event (triggers select);
	// the loop guard lives on the source side.
	if n.publish != nil {
		n.publish(ctx, event, t, line, nil)
	}
}
