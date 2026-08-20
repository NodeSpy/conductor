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
	EventDispatch   = "dispatch"
	EventComplete   = "complete"
	EventEscalate   = "escalate"
	EventNeedsInput = "needs_input" // a workflow handed a PR to a live agent; you need to weigh in
)

// PostFunc posts a comment on a PR/issue. sub is "pr" or "issue". Injectable
// so tests can avoid shelling out to gh.
type PostFunc func(ctx context.Context, sub, ref, body, token string) error

// Notifier emits notifications per the configured policy.
type Notifier struct {
	cfg       config.Notify
	userToken func() (string, error) // for posting a comment as you
	post      PostFunc
	log       func(string, ...any)
}

// New builds a Notifier. userToken supplies your `gh` token for escalation
// comments; log is the structured logger.
func New(cfg config.Notify, userToken func() (string, error), log func(string, ...any)) *Notifier {
	if log == nil {
		log = func(string, ...any) {}
	}
	return &Notifier{cfg: cfg, userToken: userToken, post: ghComment, log: log}
}

// SetPoster overrides how comments are posted (tests inject a spy).
func (n *Notifier) SetPoster(p PostFunc) { n.post = p }

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

	// Both escalate ("gave up") and needs_input ("waiting on you") post a PR
	// comment as you, gated by the same CommentOnEscalate switch — only the
	// wording differs.
	if n.cfg.CommentOnEscalate {
		switch event {
		case EventEscalate:
			if err := n.comment(ctx, t, fmt.Sprintf("gave up after retries — %s", msg)); err != nil {
				n.log("notify: escalation comment failed: %v", err)
			}
		case EventNeedsInput:
			if err := n.comment(ctx, t, msg); err != nil {
				n.log("notify: needs-input comment failed: %v", err)
			}
		}
	}
}

// comment posts a one-line summary on the PR/issue, as you (user token). detail
// is the trailing text after the "paseo-conductor: <kind>" prefix.
func (n *Notifier) comment(ctx context.Context, t core.Trigger, detail string) error {
	if n.userToken == nil || n.post == nil || t.Target.Repo == "" || t.Target.Number == 0 {
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
	body := fmt.Sprintf("paseo-conductor: %s %s", t.Kind, detail)
	ref := fmt.Sprintf("%s#%d", t.Target.Repo, t.Target.Number)
	return n.post(ctx, sub, ref, body, tok)
}

// ghComment is the default poster: `gh <sub> comment <ref> --body ...` as you.
func ghComment(ctx context.Context, sub, ref, body, token string) error {
	c := exec.CommandContext(ctx, "gh", sub, "comment", ref, "--body", body)
	c.Env = append(os.Environ(), "GH_TOKEN="+token)
	if out, err := c.CombinedOutput(); err != nil {
		return fmt.Errorf("%w: %s", err, out)
	}
	return nil
}
