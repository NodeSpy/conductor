package handoff

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/coder/websocket"
)

// Discord gateway opcodes this client speaks.
// https://discord.com/developers/docs/topics/opcodes-and-status-codes
const (
	gwOpDispatch       = 0  // server->client: an event (t names it, d carries it)
	gwOpHeartbeat      = 1  // client->server: keep the connection alive
	gwOpIdentify       = 2  // client->server: authenticate + declare intents
	gwOpReconnect      = 7  // server->client: reconnect (a fresh session, in this client)
	gwOpInvalidSession = 9  // server->client: session invalid; reconnect
	gwOpHello          = 10 // server->client: first frame; carries heartbeat_interval
	gwOpHeartbeatACK   = 11 // server->client: heartbeat acknowledged
)

// discordIntents is the numeric sum of gateway intents a hand-off gateway
// needs: GUILDS (1<<0) so guild/channel state resolves, GUILD_MESSAGES (1<<9)
// so a channel/thread reply's MESSAGE_CREATE fires, DIRECT_MESSAGES (1<<12) so
// a DM reply's does too, and MESSAGE_CONTENT (1<<15) — a privileged intent
// that must ALSO be turned on for the bot in the Discord developer portal —
// so `content` is actually populated on those events (without it every
// MESSAGE_CREATE arrives with content empty).
const discordIntents = 1<<0 | 1<<9 | 1<<12 | 1<<15

// discordGatewayPath is the bootstrap endpoint returning the wss:// URL to
// dial. Resolved against discordAPIBase (see discord_poster.go), so
// PC_DISCORD_API_URL overrides both REST calls and this one for hermetic
// tests.
const discordGatewayPath = "/gateway/bot"

// gatewayFrame is one Discord gateway payload, sent or received.
type gatewayFrame struct {
	Op int             `json:"op"`
	D  json.RawMessage `json:"d,omitempty"`
	S  *int            `json:"s,omitempty"`
	T  string          `json:"t,omitempty"`
}

// discordGatewayState tracks the mutable state one gateway connection
// accumulates across frames: the last dispatch sequence number (required on
// every heartbeat), and the bot's own user id, captured from READY, so its
// own posts are never mistaken for a reply. Fresh per connection attempt.
type discordGatewayState struct {
	seq    *int
	selfID string
}

// discordGatewayAction tells the connection loop what to do after one frame.
// Parsing + deciding is pure (handleDiscordFrame); only the loop performs I/O
// (sending IDENTIFY, starting the heartbeat, tearing down for a reconnect) —
// that split is what makes gateway behavior testable without a live socket.
type discordGatewayAction int

const (
	discordActionNone      discordGatewayAction = iota
	discordActionIdentify                       // HELLO: send IDENTIFY, start heartbeating
	discordActionReconnect                      // op 7 or 9: drop the connection, reconnect
)

// helloPayload is HELLO's (op 10) `d`.
type helloPayload struct {
	HeartbeatInterval int `json:"heartbeat_interval"`
}

// readyPayload is READY's (op 0, t="READY") `d` — only the field this gateway
// needs.
type readyPayload struct {
	User struct {
		ID string `json:"id"`
	} `json:"user"`
}

// messageCreatePayload is MESSAGE_CREATE's (op 0, t="MESSAGE_CREATE") `d`.
type messageCreatePayload struct {
	ChannelID string `json:"channel_id"`
	Content   string `json:"content"`
	Author    struct {
		ID  string `json:"id"`
		Bot bool   `json:"bot"`
	} `json:"author"`
}

// handleDiscordFrame parses one raw gateway frame, updates gs (the sequence
// number off every frame that carries one; the bot's own user id off READY),
// delivers a MESSAGE_CREATE's content to inbox — skipping the bot's own
// messages (author.bot true, or author.id == gs.selfID) so the gateway can
// never resolve its own Await by posting the draft — and reports what the
// connection loop should do next. heartbeatMS is only meaningful (and only
// set) when the returned action is discordActionIdentify.
func handleDiscordFrame(gs *discordGatewayState, raw []byte, inbox *Inbox, log func(string, ...any)) (action discordGatewayAction, heartbeatMS int) {
	if log == nil {
		log = func(string, ...any) {}
	}
	var f gatewayFrame
	if err := json.Unmarshal(raw, &f); err != nil {
		log("discord gateway: malformed frame: %v", err)
		return discordActionNone, 0
	}
	if f.S != nil {
		s := *f.S
		gs.seq = &s
	}
	switch f.Op {
	case gwOpHello:
		var h helloPayload
		if err := json.Unmarshal(f.D, &h); err != nil {
			log("discord gateway: malformed HELLO: %v", err)
			return discordActionNone, 0
		}
		return discordActionIdentify, h.HeartbeatInterval
	case gwOpReconnect, gwOpInvalidSession:
		return discordActionReconnect, 0
	case gwOpDispatch:
		switch f.T {
		case "READY":
			var r readyPayload
			if err := json.Unmarshal(f.D, &r); err != nil {
				log("discord gateway: malformed READY: %v", err)
				return discordActionNone, 0
			}
			gs.selfID = r.User.ID
		case "MESSAGE_CREATE":
			var m messageCreatePayload
			if err := json.Unmarshal(f.D, &m); err != nil {
				log("discord gateway: malformed MESSAGE_CREATE: %v", err)
				return discordActionNone, 0
			}
			if m.Author.Bot || (gs.selfID != "" && m.Author.ID == gs.selfID) {
				return discordActionNone, 0
			}
			inbox.Deliver(m.ChannelID, "", m.Content)
		}
	}
	return discordActionNone, 0
}

// RunDiscordGateway runs a persistent Discord bot gateway connection,
// reconnecting with capped backoff until ctx is cancelled — the same loop
// shape as the slack integration's Socket Mode connection (see
// internal/integrations/slack.Integration.Start). It authenticates with
// botToken, identifies with discordIntents, and feeds every inbound
// MESSAGE_CREATE through handleDiscordFrame so a DiscordChannel's pending
// Await resolves on a reply. Meant to be started as a goroutine — one per
// distinct configured discord bot_token — from cmd/paseo-conductor/main.go
// once at daemon startup; it blocks (never returns) until ctx is done. log
// may be nil.
func RunDiscordGateway(ctx context.Context, botToken string, inbox *Inbox, log func(string, ...any)) {
	if log == nil {
		log = func(string, ...any) {}
	}
	log("discord gateway: connecting")
	backoff := time.Second
	for {
		if ctx.Err() != nil {
			return
		}
		err := runDiscordGatewayOnce(ctx, botToken, inbox, log)
		if ctx.Err() != nil {
			return
		}
		log("discord gateway: connection ended (%v); reconnecting in %s", err, backoff)
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		if backoff < 30*time.Second {
			backoff *= 2
		}
	}
}

// runDiscordGatewayOnce opens one gateway session and pumps frames until it
// closes or the gateway asks for a reconnect.
func runDiscordGatewayOnce(ctx context.Context, botToken string, inbox *Inbox, log func(string, ...any)) error {
	wss, err := fetchDiscordGatewayURL(ctx, botToken)
	if err != nil {
		return fmt.Errorf("gateway/bot: %w", err)
	}
	c, _, err := websocket.Dial(ctx, wss, nil)
	if err != nil {
		return err
	}
	defer c.Close(websocket.StatusNormalClosure, "")
	c.SetReadLimit(1 << 20)

	hbCtx, stopHeartbeat := context.WithCancel(ctx)
	defer stopHeartbeat()

	gs := &discordGatewayState{}
	var startHeartbeat sync.Once
	for {
		_, data, err := c.Read(ctx)
		if err != nil {
			return err
		}
		action, heartbeatMS := handleDiscordFrame(gs, data, inbox, log)
		switch action {
		case discordActionIdentify:
			if err := sendDiscordIdentify(ctx, c, botToken); err != nil {
				return fmt.Errorf("identify: %w", err)
			}
			startHeartbeat.Do(func() {
				go discordHeartbeatLoop(hbCtx, c, gs, time.Duration(heartbeatMS)*time.Millisecond, log)
			})
		case discordActionReconnect:
			return fmt.Errorf("gateway requested reconnect")
		}
	}
}

// discordHeartbeatLoop sends op 1 (heartbeat, carrying the last-seen sequence
// number) every interval until ctx is done or a send fails — a failed send
// ends the read loop too (the connection is dead either way), which
// runDiscordGatewayOnce's caller reconnects on.
func discordHeartbeatLoop(ctx context.Context, c *websocket.Conn, gs *discordGatewayState, interval time.Duration, log func(string, ...any)) {
	if interval <= 0 {
		interval = 30 * time.Second // HELLO should always set this; a floor in case it doesn't.
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := sendDiscordHeartbeat(ctx, c, gs); err != nil {
				log("discord gateway: heartbeat failed: %v", err)
				return
			}
		}
	}
}

func sendDiscordHeartbeat(ctx context.Context, c *websocket.Conn, gs *discordGatewayState) error {
	f := gatewayFrame{Op: gwOpHeartbeat, D: json.RawMessage("null")}
	if gs.seq != nil {
		b, err := json.Marshal(*gs.seq)
		if err != nil {
			return err
		}
		f.D = b
	}
	data, err := json.Marshal(f)
	if err != nil {
		return err
	}
	return c.Write(ctx, websocket.MessageText, data)
}

func sendDiscordIdentify(ctx context.Context, c *websocket.Conn, botToken string) error {
	d, err := json.Marshal(map[string]any{
		"token":   botToken,
		"intents": discordIntents,
		"properties": map[string]string{
			"os":      "linux",
			"browser": "paseo-conductor",
			"device":  "paseo-conductor",
		},
	})
	if err != nil {
		return err
	}
	data, err := json.Marshal(gatewayFrame{Op: gwOpIdentify, D: d})
	if err != nil {
		return err
	}
	return c.Write(ctx, websocket.MessageText, data)
}

// fetchDiscordGatewayURL resolves the wss:// URL to dial via GET
// /gateway/bot, authenticating as the bot (this endpoint, unlike the
// unauthenticated /gateway, is rate-limited per bot rather than globally).
func fetchDiscordGatewayURL(ctx context.Context, botToken string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, discordAPIBase+discordGatewayPath, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bot "+botToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("unexpected status %d", resp.StatusCode)
	}
	var out struct {
		URL string `json:"url"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", err
	}
	if out.URL == "" {
		return "", fmt.Errorf("empty gateway url")
	}
	return out.URL + "?v=10&encoding=json", nil
}
