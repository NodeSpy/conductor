package slack

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/NodeSpy/paseo-conductor/internal/core"
	"github.com/NodeSpy/paseo-conductor/internal/inbound"
)

var httpc = &http.Client{Timeout: 15 * time.Second}

// eventCallback is the Events API payload delivered inside an events_api envelope.
type eventCallback struct {
	Event struct {
		Type     string `json:"type"`
		Text     string `json:"text"`
		User     string `json:"user"`
		Channel  string `json:"channel"`
		TS       string `json:"ts"`
		ThreadTS string `json:"thread_ts"`
		Reaction string `json:"reaction"`
		Item     struct {
			Channel string `json:"channel"`
			TS      string `json:"ts"`
		} `json:"item"`
	} `json:"event"`
}

// slashPayload is the slash_commands envelope payload.
type slashPayload struct {
	Command   string `json:"command"`
	Text      string `json:"text"`
	ChannelID string `json:"channel_id"`
	UserID    string `json:"user_id"`
}

func (g *Integration) handleEvent(ctx context.Context, emit core.EmitFunc, raw json.RawMessage) {
	var cb eventCallback
	if json.Unmarshal(raw, &cb) != nil {
		return
	}
	e := cb.Event
	switch e.Type {
	case "app_mention":
		g.fire(ctx, emit, "app_mention", ruleMatchMention, evt{
			text: e.Text, user: e.User, channel: e.Channel, ts: e.TS, threadTS: firstNonEmpty(e.ThreadTS, e.TS),
		})
	case "reaction_added":
		g.fire(ctx, emit, "reaction_added", func(r Rule, ev evt) bool {
			return r.Reaction == "" || r.Reaction == ev.reaction
		}, evt{
			reaction: e.Reaction, user: e.User, channel: e.Item.Channel, ts: e.Item.TS, threadTS: e.Item.TS,
		})
	}
}

func (g *Integration) handleSlash(ctx context.Context, emit core.EmitFunc, raw json.RawMessage) {
	var p slashPayload
	if json.Unmarshal(raw, &p) != nil {
		return
	}
	g.fire(ctx, emit, "slash_command", func(r Rule, ev evt) bool {
		return r.Command == "" || r.Command == p.Command
	}, evt{
		text: p.Text, user: p.UserID, channel: p.ChannelID, command: p.Command, ts: "", threadTS: "",
	})
}

// evt is the normalized Slack event we route + template on.
type evt struct {
	text, user, channel, ts, threadTS, reaction, command string
}

func ruleMatchMention(Rule, evt) bool { return true }

// fire matches the event against every rule of the given `on` type and emits a
// trigger per enabled action variant of each matching rule. It also posts the
// optional ack to the thread.
func (g *Integration) fire(ctx context.Context, emit core.EmitFunc, on string, match func(Rule, evt) bool, ev evt) {
	// Dedup on channel+ts+on so a redelivered envelope doesn't double-fire.
	dedupKey := on + ":" + ev.channel + ":" + firstNonEmpty(ev.ts, ev.reaction+ev.text)
	if !g.seen.Add(dedupKey) {
		return
	}
	fired := false
	for _, r := range g.cfg.Rules {
		if r.On != on || !match(r, ev) {
			continue
		}
		title := firstLine(firstNonEmpty(ev.text, ev.command+" reaction:"+ev.reaction, "slack "+on))
		target := inbound.SyntheticTarget("slack:"+ev.channel, dedupKey)
		sctx := map[string]any{
			"channel": ev.channel, "user": ev.user, "text": ev.text, "ts": ev.ts,
			"thread_ts": ev.threadTS, "reaction": ev.reaction, "command": ev.command,
		}
		for _, act := range r.Actions {
			if !act.IsEnabled() {
				continue
			}
			act = inbound.ForceNoCheckout(act)
			emit(ctx, core.Trigger{
				Source:   "slack",
				Instance: g.name,
				Kind:     on,
				Variant:  act.Name,
				Target:   target,
				Title:    title,
				Dedup:    dedupKey,
				Context: map[string]any{
					"slack":           sctx,
					"slack_bot_token": g.cfg.BotToken, // so a command action can reply with {{.slack_bot_token}}
				},
				Action: act,
			})
			fired = true
		}
	}
	if fired && g.cfg.Ack != "" && g.cfg.BotToken != "" && ev.channel != "" {
		g.postMessage(ctx, ev.channel, ev.threadTS, g.cfg.Ack)
	}
}

// openSocket opens a Socket Mode session and returns the WebSocket URL.
func (g *Integration) openSocket(ctx context.Context) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://slack.com/api/apps.connections.open", nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+g.cfg.AppToken)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := httpc.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var out struct {
		OK    bool   `json:"ok"`
		URL   string `json:"url"`
		Error string `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", err
	}
	if !out.OK {
		return "", fmt.Errorf("apps.connections.open: %s", out.Error)
	}
	return out.URL, nil
}

// postMessage posts text to a channel (optionally threaded) as the bot.
func (g *Integration) postMessage(ctx context.Context, channel, threadTS, text string) {
	payload := map[string]string{"channel": channel, "text": text}
	if threadTS != "" {
		payload["thread_ts"] = threadTS
	}
	body, _ := json.Marshal(payload)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://slack.com/api/chat.postMessage", bytes.NewReader(body))
	if err != nil {
		return
	}
	req.Header.Set("Authorization", "Bearer "+g.cfg.BotToken)
	req.Header.Set("Content-Type", "application/json")
	resp, err := httpc.Do(req)
	if err != nil {
		log.Printf("slack[%s]: post failed: %v", g.name, err)
		return
	}
	resp.Body.Close()
}

func firstNonEmpty(vs ...string) string {
	for _, v := range vs {
		if v != "" {
			return v
		}
	}
	return ""
}
