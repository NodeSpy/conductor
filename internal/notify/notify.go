// Package notify surfaces conductor activity to *you* — never to the PR. It logs
// to the daemon journal (so `journalctl --user -u conductor` shows it) and,
// if a Slack, Discord, ntfy, Pushover, or Notifiarr sink is configured, also
// posts there. The interactive/failed agents additionally surface in paseo
// itself (attention flag). It deliberately does NOT post comments on PRs: a
// handoff/escalation nudge is for you, and posting it publicly on someone's
// PR (as you) is noise/leakage.
package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/core"
)

// pushoverURL and notifiarrURL are the fixed API endpoints for those services.
// They're package-level vars (rather than inline literals) so tests can point
// them at an httptest server.
var (
	pushoverURL  = "https://api.pushover.net/1/messages.json"
	notifiarrURL = "https://notifiarr.com/api/v1/notification/passthrough/%s"
	defaultNtfy  = "https://ntfy.sh"
)

// Pushover and Notifiarr post to fixed vendor hosts with no config knob (unlike
// Slack/Discord/ntfy, whose URLs come from config). So a hermetic test harness
// (test/e2e/) can assert those two sinks too, their base URLs may be redirected
// via env at startup. Unset — the production case — leaves the vendor endpoints
// unchanged. PC_NOTIFIARR_URL without a `%s` gets the passthrough/<key> path
// appended, matching the vendor URL shape.
func init() {
	if v := os.Getenv("PC_PUSHOVER_URL"); v != "" {
		pushoverURL = v
	}
	if v := os.Getenv("PC_NOTIFIARR_URL"); v != "" {
		notifiarrURL = normalizeNotifiarrURL(v)
	}
	if v := os.Getenv("PC_NTFY_DEFAULT_URL"); v != "" {
		defaultNtfy = v
	}
}

// normalizeNotifiarrURL keeps the vendor passthrough shape: the URL must carry a
// single `%s` where the API key is interpolated. A bare base (no `%s`) gets the
// standard passthrough/<key> path appended.
func normalizeNotifiarrURL(v string) string {
	if strings.Contains(v, "%s") {
		return v
	}
	return strings.TrimRight(v, "/") + "/api/v1/notification/passthrough/%s"
}

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

	// route delivers one notify.via route through a connector verb — the
	// connectors-model delivery, wired by main when a connectors: block
	// exists (see SetRouter). nil with via: configured logs a warning once.
	route      func(ctx context.Context, r config.NotifyRoute, data map[string]any) error
	warnedOnce bool
}

// SetRouter wires the verb-layer delivery for notify.via routes.
func (n *Notifier) SetRouter(route func(ctx context.Context, r config.NotifyRoute, data map[string]any) error) {
	n.route = route
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
	n.notifyAll(ctx, line, event, map[string]any{
		"message": line, "event": event, "ref": ref,
		"repo": t.Target.Repo, "number": t.Target.Number,
		"kind": t.Kind, "title": t.Title,
	})
}

// Digest emits a periodic activity summary (journal + Slack + audit). Unlike Emit
// it isn't gated by notify.on — it's opt-in via notify.digest and always sent.
func (n *Notifier) Digest(ctx context.Context, summary string) {
	n.log("notify [digest] %s", summary)
	if n.audit != nil {
		n.audit(map[string]any{"event": "digest", "msg": summary})
	}
	n.notifyAll(ctx, "[digest] "+summary, "digest", map[string]any{
		"message": "[digest] " + summary, "event": "digest",
	})
}

// notifyAll fans a message out to every configured sink (Slack, Discord,
// ntfy, Pushover, Notifiarr — the legacy delivery) AND every notify.via route
// (the verb-layer delivery). Each post is best-effort and non-blocking — a
// failure is logged, never fatal (the journal line already recorded the event).
func (n *Notifier) notifyAll(ctx context.Context, text, event string, data map[string]any) {
	for _, r := range n.cfg.Via {
		if !n.cfg.WantsRoute(r, event) {
			continue
		}
		if n.route == nil {
			if !n.warnedOnce {
				n.warnedOnce = true
				n.log("notify: via routes configured but no connectors are wired — %s not delivered", r.Uses)
			}
			continue
		}
		r := r
		go func() {
			if err := n.route(ctx, r, data); err != nil {
				n.log("notify: via %s failed: %v", r.Uses, err)
			}
		}()
	}
	if n.cfg.SlackWebhookURL != "" {
		go n.postSlack(ctx, text)
	}
	if n.cfg.DiscordWebhookURL != "" {
		go n.postDiscord(ctx, text)
	}
	if n.cfg.Ntfy.Topic != "" {
		go n.postNtfy(ctx, text)
	}
	if n.cfg.Pushover.Token != "" && n.cfg.Pushover.User != "" {
		go n.postPushover(ctx, text)
	}
	if n.cfg.Notifiarr.APIKey != "" {
		go n.postNotifiarr(ctx, text)
	}
}

// postSlack posts a plain message to a Slack incoming webhook.
func (n *Notifier) postSlack(ctx context.Context, text string) {
	body, _ := json.Marshal(map[string]string{"text": "conductor " + text})
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

// postDiscord posts a plain message to a Discord incoming webhook.
func (n *Notifier) postDiscord(ctx context.Context, text string) {
	body, _ := json.Marshal(map[string]string{"content": "conductor " + text})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, n.cfg.DiscordWebhookURL, bytes.NewReader(body))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := n.http.Do(req)
	if err != nil {
		n.log("notify: discord post failed: %v", err)
		return
	}
	resp.Body.Close()
}

// postNtfy publishes a plain-text message to an ntfy topic. Server defaults
// to https://ntfy.sh when unset, so a self-hosted server is a config away.
func (n *Notifier) postNtfy(ctx context.Context, text string) {
	server := n.cfg.Ntfy.Server
	if server == "" {
		server = defaultNtfy
	}
	endpoint := strings.TrimRight(server, "/") + "/" + n.cfg.Ntfy.Topic
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(text))
	if err != nil {
		return
	}
	req.Header.Set("Title", "conductor")
	resp, err := n.http.Do(req)
	if err != nil {
		n.log("notify: ntfy post failed: %v", err)
		return
	}
	resp.Body.Close()
}

// postPushover posts a message via the Pushover message API.
func (n *Notifier) postPushover(ctx context.Context, text string) {
	form := url.Values{
		"token":   {n.cfg.Pushover.Token},
		"user":    {n.cfg.Pushover.User},
		"message": {"conductor " + text},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, pushoverURL, strings.NewReader(form.Encode()))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := n.http.Do(req)
	if err != nil {
		n.log("notify: pushover post failed: %v", err)
		return
	}
	resp.Body.Close()
}

// postNotifiarr posts a message to a Notifiarr passthrough integration, which
// relays it to a Discord channel on Notifiarr's side.
func (n *Notifier) postNotifiarr(ctx context.Context, text string) {
	discord := map[string]any{
		"text": map[string]string{"description": text},
	}
	if n.cfg.Notifiarr.ChannelID != "" {
		discord["ids"] = map[string]string{"channel": n.cfg.Notifiarr.ChannelID}
	}
	body, _ := json.Marshal(map[string]any{
		"notification": map[string]string{"name": "conductor"},
		"discord":      discord,
	})
	endpoint := fmt.Sprintf(notifiarrURL, n.cfg.Notifiarr.APIKey)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Api-Key", n.cfg.Notifiarr.APIKey)
	resp, err := n.http.Do(req)
	if err != nil {
		n.log("notify: notifiarr post failed: %v", err)
		return
	}
	resp.Body.Close()
}
