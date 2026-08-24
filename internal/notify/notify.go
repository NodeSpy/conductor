// Package notify surfaces conductor activity to *you* — never to the PR. It logs
// to the daemon journal (so `journalctl --user -u paseo-conductor` shows it) and,
// if a Slack incoming-webhook URL is configured, also posts there. The
// interactive/failed agents additionally surface in paseo itself (attention flag).
// It deliberately does NOT post comments on PRs: a handoff/escalation nudge is for
// you, and posting it publicly on someone's PR (as you) is noise/leakage.
package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

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
	cfg   config.Notify
	log   func(string, ...any)
	http  *http.Client
	audit func(map[string]any) // optional: record attention events for status/report
}

// New builds a Notifier. log is the structured logger (the journal); audit (may be
// nil) records attention events (escalate/needs_input/complete) to the audit log so
// `status` and `report` can surface them.
func New(cfg config.Notify, log func(string, ...any), audit func(map[string]any)) *Notifier {
	if log == nil {
		log = func(string, ...any) {}
	}
	return &Notifier{cfg: cfg, log: log, audit: audit, http: &http.Client{Timeout: 10 * time.Second}}
}

// Emit records a notification for the given event if enabled by policy. All
// channels are private to you (the journal, plus an optional Slack webhook). The
// two attention events get an explicit, actionable line.
func (n *Notifier) Emit(ctx context.Context, event string, t core.Trigger, msg string) {
	// Record attention/terminal events (escalate/needs_input/complete) for
	// status/report regardless of notify policy — dispatch is already captured
	// richly by the engine's own dispatch audit, so skip it here.
	if n.audit != nil && event != EventDispatch {
		n.audit(map[string]any{"event": event, "repo": t.Target.Repo,
			"number": t.Target.Number, "kind": t.Kind, "msg": msg})
	}
	if !n.cfg.Wants(event) {
		return
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
	if n.cfg.SlackWebhookURL != "" {
		go n.postSlack(ctx, line)
	}
}

// postSlack posts a plain message to a Slack incoming webhook. Best-effort: a
// failure is logged, never fatal (the journal line already recorded the event).
func (n *Notifier) postSlack(ctx context.Context, text string) {
	body, _ := json.Marshal(map[string]string{"text": "paseo-conductor " + text})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, n.cfg.SlackWebhookURL, bytes.NewReader(body))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := n.http.Do(req)
	if err != nil {
		n.log("notify: slack post failed: %v", err)
		return
	}
	resp.Body.Close()
}
