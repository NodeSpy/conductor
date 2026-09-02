// Package slack is a control plane: Slack events (an @-mention, an emoji reaction,
// a slash command) dispatch a Paseo agent, and the agent's ack/replies post back to
// the thread. It connects over Socket Mode (an outbound WebSocket) so it needs no
// public URL — ideal for a self-hosted box. Posting to a channel for notifications
// is handled separately by the notify package's slack_webhook_url.
package slack

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/coder/websocket"

	"github.com/NodeSpy/paseo-conductor/internal/config"
	"github.com/NodeSpy/paseo-conductor/internal/core"
	"github.com/NodeSpy/paseo-conductor/internal/inbound"
)

func init() { core.Register("slack", newIntegration) }

// Config is one slack instance.
type Config struct {
	AppToken string `yaml:"app_token"` // xapp-… (Socket Mode connect)
	BotToken string `yaml:"bot_token"` // xoxb-… (posting; exposed to actions as {{.slack_bot_token}})
	Ack      string `yaml:"ack"`       // optional message posted to the thread when a rule fires
	Rules    []Rule `yaml:"triggers"`
}

// Rule routes one kind of Slack event to action(s).
type Rule struct {
	On       string           `yaml:"on"`       // app_mention | reaction_added | slash_command
	Reaction string           `yaml:"reaction"` // reaction_added: which emoji (e.g. "eyes"); "" = any
	Command  string           `yaml:"command"`  // slash_command: which command (e.g. "/conductor"); "" = any
	Actions  config.ActionSet `yaml:"actions"`
}

// Integration implements core.Integration for one slack instance.
type Integration struct {
	name string
	cfg  Config
	seen *inbound.DeliveryDedup
}

func newIntegration(name string, decode func(any) error) (core.Integration, error) {
	var cfg Config
	if err := decode(&cfg); err != nil {
		return nil, fmt.Errorf("slack[%s]: decode config: %w", name, err)
	}
	return &Integration{name: name, cfg: cfg, seen: inbound.NewDeliveryDedup(4096)}, nil
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
