// Package notify surfaces conductor activity to *you* — never to the PR. It logs
// to the daemon journal (so `journalctl --user -u paseo-conductor` shows it); the
// interactive/failed agents also surface in paseo itself (attention flag). It
// deliberately does NOT post comments on PRs: a handoff/escalation nudge is for
// you, and posting it publicly on someone's PR (as you) is noise/leakage.
package notify

import (
	"context"
	"fmt"

	"github.com/NodeSpy/paseo-conductor/internal/config"
	"github.com/NodeSpy/paseo-conductor/internal/core"
)

// Events.
const (
	EventDispatch   = "dispatch"
	EventComplete   = "complete"
	EventEscalate   = "escalate"
	EventNeedsInput = "needs_input" // a workflow handed a PR to a live agent; you need to weigh in
)

// Notifier emits notifications per the configured policy.
type Notifier struct {
	cfg config.Notify
	log func(string, ...any)
}

// New builds a Notifier. log is the structured logger (the journal).
func New(cfg config.Notify, log func(string, ...any)) *Notifier {
	if log == nil {
		log = func(string, ...any) {}
	}
	return &Notifier{cfg: cfg, log: log}
}

// Emit records a notification for the given event if enabled by policy. All
// channels are private to you (the journal today; a push endpoint later). The
// two attention events get an explicit, actionable line.
func (n *Notifier) Emit(_ context.Context, event string, t core.Trigger, msg string) {
	if !n.cfg.Wants(event) {
		return
	}
	ref := fmt.Sprintf("%s#%d", t.Target.Repo, t.Target.Number)
	switch event {
	case EventEscalate:
		n.log("notify [escalate] %s %s gave up after retries — %s (open paseo)", ref, t.Kind, msg)
	case EventNeedsInput:
		n.log("notify [needs_input] %s %s handed to a live agent — %s (open paseo)", ref, t.Kind, msg)
	default:
		n.log("notify [%s] %s %s: %s", event, ref, t.Kind, msg)
	}
}
