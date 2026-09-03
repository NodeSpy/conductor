// Package slack is a control plane: Slack events (an @-mention, an emoji reaction,
// a slash command) dispatch a Paseo agent, and the agent's ack/replies post back to
// the thread. It connects over Socket Mode (an outbound WebSocket) so it needs no
// public URL. Posting to a channel for notifications is handled separately by the
// notify package's slack_webhook_url.
package slack

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"

	"github.com/NodeSpy/paseo-conductor/internal/config"
	"github.com/NodeSpy/paseo-conductor/internal/core"
	"github.com/NodeSpy/paseo-conductor/internal/inbound"
)

func init() { core.Register("slack", newIntegration) }

// replyHook, when set, receives Slack thread replies so an interactive hand-off
// posted to a thread (see internal/handoff) can capture your reply. It reports
// whether it consumed the message (a reply to a pending hand-off) so ordinary
// chatter still flows to normal rule matching. nil (default) → thread replies are
// ignored, and behavior is exactly as before.
var replyHook func(channel, threadTS, user, text string) bool

// SetReplyHook installs the hand-off reply hook (set once at startup by main
// wiring). Passing nil clears it.
func SetReplyHook(fn func(channel, threadTS, user, text string) bool) { replyHook = fn }

// maxPendingFeedback bounds the per-instance on_done/on_fail stash so a kind the
// engine never reports completion for (e.g. permanently queued work) can't grow
// the map without limit. Oldest entries are evicted first.
const maxPendingFeedback = 2048

// Config is one slack instance.
type Config struct {
	AppToken string `yaml:"app_token"` // xapp-… (Socket Mode connect)
	BotToken string `yaml:"bot_token"` // xoxb-… (posting; exposed to actions as {{.slack_bot_token}})
	Rules    []Rule `yaml:"triggers"`
}

// Feedback describes one optional Slack response: a reaction, a message, or
// both. Omitting a Feedback block entirely (nil) means silence for that moment.
// The same struct is reused for a rule's `ack` (fired at dispatch), `on_done`
// (fired when the dispatched work finishes successfully) and `on_fail` (fired
// when it fails).
type Feedback struct {
	React string `yaml:"react"` // reactions.add emoji name, no colons (e.g. "eyes"); "" = no reaction
	Say   string `yaml:"say"`   // chat.postMessage/postEphemeral text; "" = no message
	// Ephemeral sends Say only to the triggering user via chat.postEphemeral
	// instead of posting it for the channel to see. Requires a user id (present
	// on every trigger kind); falls back to a normal post if one isn't available.
	Ephemeral bool `yaml:"ephemeral"`
	// InThread posts Say in the triggering message's thread instead of the
	// channel. nil defaults to true.
	InThread *bool `yaml:"in_thread"`
}

func (f *Feedback) empty() bool { return f == nil || (f.React == "" && f.Say == "") }

func (f *Feedback) inThread() bool { return f == nil || f.InThread == nil || *f.InThread }

// Rule routes one kind of Slack event to action(s), with optional feedback.
type Rule struct {
	On       string           `yaml:"on"`       // app_mention | reaction_added | slash_command
	Reaction string           `yaml:"reaction"` // reaction_added: which emoji (e.g. "eyes"); "" = any
	Command  string           `yaml:"command"`  // slash_command: which command (e.g. "/conductor"); "" = any
	Ack      *Feedback        `yaml:"ack"`      // fired when the rule matches and dispatches
	OnDone   *Feedback        `yaml:"on_done"`  // fired when the dispatched work finishes successfully
	OnFail   *Feedback        `yaml:"on_fail"`  // fired when the dispatched work fails
	Actions  config.ActionSet `yaml:"actions"`
}

// pendingFeedback is stashed per originating Slack event (keyed by the
// Trigger.Dedup shared by every action variant that event dispatched) so the
// engine completion seam can post on_done/on_fail once every variant has
// reported its outcome. See Integration.HandleCompletion.
type pendingFeedback struct {
	onDone, onFail              *Feedback
	channel, ts, threadTS, user string
	sctx                        map[string]any
	remaining                   int
	failed                      bool
}

// Integration implements core.Integration for one slack instance.
type Integration struct {
	name string
	cfg  Config
	seen *inbound.DeliveryDedup

	pendingMu   sync.Mutex
	pending     map[string]*pendingFeedback
	pendingRing []string
}

func newIntegration(name string, decode func(any) error) (core.Integration, error) {
	var cfg Config
	if err := decode(&cfg); err != nil {
		return nil, fmt.Errorf("slack[%s]: decode config: %w", name, err)
	}
	return &Integration{name: name, cfg: cfg, seen: inbound.NewDeliveryDedup(4096), pending: map[string]*pendingFeedback{}}, nil
}

func (g *Integration) Name() string { return g.name }

func (g *Integration) Validate() error {
	if g.cfg.AppToken == "" {
		return fmt.Errorf("slack[%s]: app_token is required (xapp- Socket Mode token)", g.name)
	}
	if len(g.cfg.Rules) == 0 {
		return fmt.Errorf("slack[%s]: no triggers", g.name)
	}
	valid := map[string]bool{"app_mention": true, "reaction_added": true, "slash_command": true}
	for i, r := range g.cfg.Rules {
		if !valid[r.On] {
			return fmt.Errorf("slack[%s]: triggers[%d]: `on` must be app_mention|reaction_added|slash_command", g.name, i)
		}
		if len(r.Actions) == 0 {
			return fmt.Errorf("slack[%s]: triggers[%d]: no actions", g.name, i)
		}
		for _, a := range r.Actions {
			if a.Type == "" {
				return fmt.Errorf("slack[%s]: triggers[%d]: action.type is required", g.name, i)
			}
		}
		for _, fb := range []struct {
			field string
			fb    *Feedback
		}{{"ack", r.Ack}, {"on_done", r.OnDone}, {"on_fail", r.OnFail}} {
			if fb.fb == nil {
				continue
			}
			if fb.fb.empty() {
				return fmt.Errorf("slack[%s]: triggers[%d].%s: neither react nor say is set; omit %s for silence instead of an empty block", g.name, i, fb.field, fb.field)
			}
			if fb.fb.Ephemeral && fb.fb.Say == "" {
				return fmt.Errorf("slack[%s]: triggers[%d].%s: ephemeral only applies to say", g.name, i, fb.field)
			}
		}
	}
	return nil
}

// Actions enumerates every trigger rule's actions with their location, for the
// CLI's cross-config checks.
func (g *Integration) Actions() []config.ActionRef {
	var refs []config.ActionRef
	for i, r := range g.cfg.Rules {
		refs = append(refs, r.Actions.Refs(fmt.Sprintf("slack[%s] triggers[%d] (%s)", g.name, i, r.On))...)
	}
	return refs
}

// Start runs the Socket Mode connection, reconnecting with capped backoff until ctx
// is cancelled (Slack recycles connections periodically via a `disconnect` frame).
func (g *Integration) Start(ctx context.Context, emit core.EmitFunc) error {
	log.Printf("slack[%s]: connecting (Socket Mode)", g.name)
	backoff := time.Second
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		err := g.runOnce(ctx, emit)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		log.Printf("slack[%s]: connection ended (%v); reconnecting in %s", g.name, err, backoff)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(backoff):
		}
		if backoff < 30*time.Second {
			backoff *= 2
		}
	}
}

// runOnce opens one Socket Mode session and pumps envelopes until it closes.
func (g *Integration) runOnce(ctx context.Context, emit core.EmitFunc) error {
	wss, err := g.openSocket(ctx)
	if err != nil {
		return err
	}
	c, _, err := websocket.Dial(ctx, wss, nil)
	if err != nil {
		return err
	}
	defer c.Close(websocket.StatusNormalClosure, "")
	c.SetReadLimit(1 << 20)

	for {
		_, data, err := c.Read(ctx)
		if err != nil {
			return err
		}
		var env envelope
		if json.Unmarshal(data, &env) != nil {
			continue
		}
		// ACK first (Slack requires it within 3s), then process.
		if env.EnvelopeID != "" {
			ack, _ := json.Marshal(map[string]string{"envelope_id": env.EnvelopeID})
			_ = c.Write(ctx, websocket.MessageText, ack)
		}
		switch env.Type {
		case "hello":
			// connected
		case "disconnect":
			return fmt.Errorf("disconnect: %s", env.Reason)
		case "events_api":
			g.handleEvent(ctx, emit, env.Payload)
		case "slash_commands":
			g.handleSlash(ctx, emit, env.Payload)
		}
	}
}

// envelope is the Socket Mode frame wrapper.
type envelope struct {
	Type       string          `json:"type"`
	EnvelopeID string          `json:"envelope_id"`
	Reason     string          `json:"reason"`
	Payload    json.RawMessage `json:"payload"`
}

// firstLine trims a message to a short single-line title.
func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > 80 {
		s = s[:80] + "…"
	}
	return s
}
