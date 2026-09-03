package slack

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"text/template"
	"time"

	"github.com/NodeSpy/paseo-conductor/internal/core"
	"github.com/NodeSpy/paseo-conductor/internal/inbound"
)

var httpc = &http.Client{Timeout: 15 * time.Second}

// Slack Web API endpoints, overridable in tests (see notify's pushoverURL).
var (
	chatPostMessageURL   = "https://slack.com/api/chat.postMessage"
	chatPostEphemeralURL = "https://slack.com/api/chat.postEphemeral"
	reactionsAddURL      = "https://slack.com/api/reactions.add"
)

// eventCallback is the Events API payload delivered inside an events_api envelope.
type eventCallback struct {
	Event struct {
		Type        string `json:"type"`
		Text        string `json:"text"`
		User        string `json:"user"`
		Channel     string `json:"channel"`
		ChannelType string `json:"channel_type"` // "im" for a DM; only present on message events
		TS          string `json:"ts"`
		ThreadTS    string `json:"thread_ts"`
		Reaction    string `json:"reaction"`
		BotID       string `json:"bot_id"` // set on messages the bot itself posted (skip those)
		Item        struct {
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
	// A human reply inside a thread — or any message in a DM — may be answering
	// an interactive hand-off posted there. Route it to the hand-off reply hook
	// first; if it's consumed, it isn't ordinary chatter and shouldn't fall
	// through to rule matching. Skip the bot's own posts (bot_id set). A DM
	// message carries channel_type "im" and typically no thread_ts, so it's
	// routed on that alone; a channel message still requires a thread_ts (this
	// integration never treats bare channel chatter as a hand-off reply).
	if e.Type == "message" && replyHook != nil && e.BotID == "" && e.User != "" && (e.ThreadTS != "" || e.ChannelType == "im") {
		if replyHook(e.Channel, e.ThreadTS, e.User, e.Text) {
			return
		}
	}
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
// trigger per enabled action variant of each matching rule. It also fires each
// matching rule's `ack` feedback, and stashes `on_done`/`on_fail` (if set) keyed
// by the shared Dedup so HandleCompletion can post them once the engine reports
// every emitted variant's outcome.
//
// If more than one rule of the same `on` matches a single physical event, they
// share one Dedup (the physical event, not the rule, is the dedup unit); the
// last matching rule that sets ack/on_done/on_fail wins for that block.
func (g *Integration) fire(ctx context.Context, emit core.EmitFunc, on string, match func(Rule, evt) bool, ev evt) {
	// Dedup on channel+ts+on so a redelivered envelope doesn't double-fire.
	dedupKey := on + ":" + ev.channel + ":" + firstNonEmpty(ev.ts, ev.reaction+ev.text)
	if !g.seen.Add(dedupKey) {
		return
	}
	sctx := map[string]any{
		"channel": ev.channel, "user": ev.user, "text": ev.text, "ts": ev.ts,
		"thread_ts": ev.threadTS, "reaction": ev.reaction, "command": ev.command,
	}
	variants := 0
	var ack, onDone, onFail *Feedback
	for _, r := range g.cfg.Rules {
		if r.On != on || !match(r, ev) {
			continue
		}
		title := firstLine(firstNonEmpty(ev.text, ev.command+" reaction:"+ev.reaction, "slack "+on))
		target := inbound.SyntheticTarget("slack:"+ev.channel, dedupKey)
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
			variants++
		}
		if r.Ack != nil {
			ack = r.Ack
		}
		if r.OnDone != nil {
			onDone = r.OnDone
		}
		if r.OnFail != nil {
			onFail = r.OnFail
		}
	}
	if variants == 0 {
		return
	}
	if ack != nil {
		g.sendFeedback(ctx, ack, ev, sctx)
	}
	if onDone != nil || onFail != nil {
		g.stashPending(dedupKey, onDone, onFail, ev, sctx, variants)
	}
}

// stashPending records the on_done/on_fail feedback for a Slack event so
// HandleCompletion can find it once the engine reports outcomes for the
// variants it dispatched. Bounded to maxPendingFeedback entries (oldest
// evicted first) so an unreported kind can't leak memory indefinitely.
func (g *Integration) stashPending(dedupKey string, onDone, onFail *Feedback, ev evt, sctx map[string]any, variants int) {
	g.pendingMu.Lock()
	defer g.pendingMu.Unlock()
	g.pending[dedupKey] = &pendingFeedback{
		onDone: onDone, onFail: onFail,
		channel: ev.channel, ts: ev.ts, threadTS: ev.threadTS, user: ev.user,
		sctx: sctx, remaining: variants,
	}
	g.pendingRing = append(g.pendingRing, dedupKey)
	if len(g.pendingRing) > maxPendingFeedback {
		old := g.pendingRing[0]
		g.pendingRing = g.pendingRing[1:]
		delete(g.pending, old)
	}
}

// HandleCompletion is the engine completion seam (core.SetCompletionHook):
// invoked once per dispatched trigger with its final outcome (ok, failed,
// skipped, adopted, queued, or shadow), right after the engine stamps it. It
// only acts on triggers this instance itself emitted. It aggregates outcomes
// per originating Slack event (keyed by Dedup, shared by every variant that
// event dispatched): once every variant has reported, it posts on_fail if any
// variant's outcome was "failed", else on_done — then drops the entry.
func (g *Integration) HandleCompletion(t core.Trigger, outcome string) {
	if t.Source != "slack" || t.Instance != g.name || t.Dedup == "" {
		return
	}
	g.pendingMu.Lock()
	pf, ok := g.pending[t.Dedup]
	if !ok {
		g.pendingMu.Unlock()
		return
	}
	if outcome == "failed" {
		pf.failed = true
	}
	pf.remaining--
	ready := pf.remaining <= 0
	if ready {
		delete(g.pending, t.Dedup)
	}
	g.pendingMu.Unlock()
	if !ready {
		return
	}
	fb := pf.onDone
	if pf.failed {
		fb = pf.onFail
	}
	if fb == nil {
		return
	}
	g.sendFeedback(context.Background(), fb, evt{channel: pf.channel, ts: pf.ts, threadTS: pf.threadTS, user: pf.user}, pf.sctx)
}

// sendFeedback carries out one Feedback block: an optional reaction on the
// triggering message and/or an optional message. Both are best-effort (errors
// are logged, never returned — feedback never blocks or fails a dispatch).
func (g *Integration) sendFeedback(ctx context.Context, fb *Feedback, ev evt, sctx map[string]any) {
	if fb == nil || g.cfg.BotToken == "" {
		return
	}
	if fb.React != "" {
		if ev.channel == "" || ev.ts == "" {
			log.Printf("slack[%s]: react %q has no triggering message to react to — skipping", g.name, fb.React)
		} else {
			g.addReaction(ctx, ev.channel, ev.ts, fb.React)
		}
	}
	if fb.Say == "" {
		return
	}
	text := renderSay(fb.Say, sctx)
	if fb.Ephemeral {
		if ev.user == "" {
			log.Printf("slack[%s]: ephemeral say has no triggering user — posting normally instead", g.name)
		} else {
			g.postEphemeral(ctx, ev.channel, ev.user, text)
			return
		}
	}
	threadTS := ""
	if fb.inThread() {
		threadTS = ev.threadTS
	}
	g.postMessage(ctx, ev.channel, threadTS, text)
}

// renderSay expands {{.field}} references in a say template over the Slack
// event context (channel/user/text/ts/thread_ts/reaction/command). A bad
// template, or one referencing an unknown field, falls back to the literal
// string rather than dropping the feedback.
func renderSay(s string, data map[string]any) string {
	if !strings.Contains(s, "{{") {
		return s
	}
	tmpl, err := template.New("say").Option("missingkey=zero").Parse(s)
	if err != nil {
		return s
	}
	var b bytes.Buffer
	if err := tmpl.Execute(&b, data); err != nil {
		return s
	}
	return b.String()
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
	g.callAPI(ctx, chatPostMessageURL, payload)
}

// postEphemeral posts text visible only to user in channel as the bot.
func (g *Integration) postEphemeral(ctx context.Context, channel, user, text string) {
	g.callAPI(ctx, chatPostEphemeralURL, map[string]string{"channel": channel, "user": user, "text": text})
}

// addReaction adds an emoji reaction (name without colons) to a message.
func (g *Integration) addReaction(ctx context.Context, channel, ts, name string) {
	g.callAPI(ctx, reactionsAddURL, map[string]string{"channel": channel, "timestamp": ts, "name": name})
}

// callAPI POSTs a JSON payload to a Slack Web API method as the bot, logging
// (never returning) any error — every caller here is best-effort feedback.
func (g *Integration) callAPI(ctx context.Context, url string, payload map[string]string) {
	body, _ := json.Marshal(payload)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return
	}
	req.Header.Set("Authorization", "Bearer "+g.cfg.BotToken)
	req.Header.Set("Content-Type", "application/json")
	resp, err := httpc.Do(req)
	if err != nil {
		log.Printf("slack[%s]: %s failed: %v", g.name, url, err)
		return
	}
	defer resp.Body.Close()
	var out struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	if json.NewDecoder(resp.Body).Decode(&out) == nil && !out.OK {
		log.Printf("slack[%s]: %s: %s", g.name, url, out.Error)
	}
}

func firstNonEmpty(vs ...string) string {
	for _, v := range vs {
		if v != "" {
			return v
		}
	}
	return ""
}
