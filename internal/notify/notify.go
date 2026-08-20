// Package notify sends notifications about conductor activity: a Paseo push
// and/or a one-line summary comment posted as you. Escalation (attempt cap
// reached) is the most important event — it means the conductor gave up and
// wants your attention rather than looping silently.
package notify

import (
	"context"
	"fmt"
	"os"
	"os/exec"

	"github.com/NodeSpy/paseo-conductor/internal/config"
	"github.com/NodeSpy/paseo-conductor/internal/core"
)

// Events.
const (
	EventDispatch = "dispatch"
	EventComplete = "complete"
	EventEscalate = "escalate"
)

// Notifier emits notifications per the configured policy.
type Notifier struct {
	cfg       config.Notify
	userToken func() (string, error) // for posting a comment as you
	log       func(string, ...any)
}

// New builds a Notifier. userToken supplies your `gh` token for escalation
// comments; log is the structured logger.
func New(cfg config.Notify, userToken func() (string, error), log func(string, ...any)) *Notifier {
	if log == nil {
		log = func(string, ...any) {}
	}
	return &Notifier{cfg: cfg, userToken: userToken, log: log}
}

// Emit sends a notification for the given event if enabled by policy.
func (n *Notifier) Emit(ctx context.Context, event string, t core.Trigger, msg string) {
	if !n.cfg.Wants(event) {
		return
	}
	line := fmt.Sprintf("[%s] %s %s#%d %s: %s", event, t.Source, t.Target.Repo, t.Target.Number, t.Kind, msg)
	n.log("notify %s", line)

	if n.cfg.Push {
		// The daemon holds push tokens; a dedicated push endpoint is a
		// follow-up. For now the notification is surfaced via the logger
		// (journald) so `journalctl --user -u paseo-conductor` shows it.
		n.log("push: %s", line)
	}

	if event == EventEscalate && n.cfg.CommentOnEscalate {
		if err := n.comment(ctx, t, msg); err != nil {
			n.log("notify: escalation comment failed: %v", err)
		}
	}
}

// comment posts a one-line summary on the PR/issue, as you (user token).
func (n *Notifier) comment(ctx context.Context, t core.Trigger, msg string) error {
	if n.userToken == nil || t.Target.Repo == "" || t.Target.Number == 0 {
		return nil
	}
	tok, err := n.userToken()
	if err != nil {
		return err
	}
	sub := "pr"
	if t.Target.PR == 0 && t.Target.Issue > 0 {
		sub = "issue"
	}
	body := fmt.Sprintf("paseo-conductor: %s (%s) — %s", t.Kind, "gave up after retries", msg)
	c := exec.CommandContext(ctx, "gh", sub, "comment",
		fmt.Sprintf("%s#%d", t.Target.Repo, t.Target.Number), "--body", body)
	c.Env = append(os.Environ(), "GH_TOKEN="+tok)
	out, err := c.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w: %s", err, out)
	}
	return nil
}
