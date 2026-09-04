package connector

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/NodeSpy/paseo-conductor/internal/config"
	"github.com/NodeSpy/paseo-conductor/internal/core"
	"github.com/NodeSpy/paseo-conductor/internal/handoff"
	slackint "github.com/NodeSpy/paseo-conductor/internal/integrations/slack"
)

// slackCtx is the context schema slack events publish (nested under .slack).
func slackCtxSchema() Schema {
	return Schema{
		"slack.channel": {Type: TString}, "slack.user": {Type: TString},
		"slack.text": {Type: TString}, "slack.ts": {Type: TString},
		"slack.thread_ts": {Type: TString}, "slack.reaction": {Type: TString},
		"slack.command": {Type: TString},
		"repo":          {Type: TString}, "number": {Type: TInt},
		"kind": {Type: TString}, "title": {Type: TString},
	}
}

var slackDecl = &TypeDecl{
	Type: "slack",
	Desc: "Slack: mentions/reactions/slash commands in (Socket Mode); messages, reactions, and asks out.",
	Connection: Schema{
		"app_token":   {Type: TString, Desc: "Socket Mode app token (xapp-…) — needed for events and ask replies"},
		"bot_token":   {Type: TString, Desc: "bot token (xoxb-…) for the Web API verbs"},
		"webhook_url": {Type: TString, Desc: "an incoming-webhook URL — post-only alternative to a bot token"},
	},
	Events: []EventDecl{
		{
			Name: "app_mention", Desc: "the bot was @-mentioned",
			Filters: Schema{
				"channel": {Type: TString, Desc: "only this channel id"},
				"users":   {Type: TList, Desc: "only these user ids"},
			},
			Context: slackCtxSchema(),
		},
		{
			Name: "reaction_added", Desc: "a reaction was added",
			Filters: Schema{
				"reaction": {Type: TString, Desc: "only this emoji name (no colons)"},
				"channel":  {Type: TString},
				"users":    {Type: TList},
			},
			Context: slackCtxSchema(),
		},
		{
			Name: "slash_command", Desc: "a slash command was invoked",
			Filters: Schema{
				"command": {Type: TString, Desc: "only this command (e.g. /fix)"},
				"channel": {Type: TString},
				"users":   {Type: TList},
			},
			Context: slackCtxSchema(),
		},
	},
	Verbs: []VerbDecl{
		{
			Name: "post", Desc: "post a message",
			Options: Schema{
				"channel":   {Type: TString, Desc: "channel id (or set user: for a DM)"},
				"user":      {Type: TString, Desc: "user id to DM"},
				"text":      {Type: TString, Required: true},
				"thread_ts": {Type: TString, Desc: "post into this thread"},
				"ephemeral": {Type: TBool, Desc: "visible only to user: (requires channel: and user:)"},
			},
			Outputs: Schema{"ts": {Type: TString}, "channel": {Type: TString}},
		},
		{
			Name: "react", Desc: "add a reaction to a message",
			Options: Schema{
				"channel": {Type: TString, Required: true},
				"ts":      {Type: TString, Required: true, Desc: "message timestamp to react to"},
				"emoji":   {Type: TString, Required: true, Desc: "emoji name, no colons"},
			},
			Outputs: Schema{"ok": {Type: TBool}},
		},
		{
			Name: "ask", Desc: "present a question/draft and wait for the reply", Ask: true,
			Options: mergeSchema(askOptionBase(), Schema{
				"to":      {Type: TString, Enum: []string{"dm", "thread"}, Required: true},
				"user":    {Type: TString, Desc: "user id (to: dm)"},
				"channel": {Type: TString, Desc: "channel id (to: thread)"},
			}),
			Outputs: askOutputs(),
		},
	},
}

func init() {
	slackDecl.Filter = slackFilter
	RegisterType(slackDecl, newSlackImpl)
}

// mergeSchema overlays b onto a copy of a.
func mergeSchema(a, b Schema) Schema {
	out := Schema{}
	for k, v := range a {
		out[k] = v
	}
	for k, v := range b {
		out[k] = v
	}
	return out
}

type slackConn struct {
	AppToken string `yaml:"app_token"`
	BotToken string `yaml:"bot_token"`
	// WebhookURL is the post-only alternative: an incoming webhook. With no
	// bot_token, `post` sends {"text": …} there (the legacy notify sink's
	// exact payload); react/ask still need the bot token.
	WebhookURL string `yaml:"webhook_url"`
}

type slackImpl struct {
	name string
	conn slackConn
	deps Deps

	api *slackAPI
	// inbox captures thread/DM replies for ask verbs; the Socket Mode source
	// (the slack integration) delivers them via the reply hook wired in main.
	inbox *handoff.Inbox
}

func newSlackImpl(name string, ref config.ConnectorRef, deps Deps) (Impl, error) {
	var conn slackConn
	if err := ref.Decode(&conn); err != nil {
		return nil, fmt.Errorf("connector %q: decode slack connection: %w", name, err)
	}
	ctx := context.Background()
	var err error
	if conn.AppToken, err = deps.Secrets.Resolve(ctx, conn.AppToken); err != nil {
		return nil, fmt.Errorf("app_token: %w", err)
	}
	if conn.BotToken, err = deps.Secrets.Resolve(ctx, conn.BotToken); err != nil {
		return nil, fmt.Errorf("bot_token: %w", err)
	}
	if conn.WebhookURL, err = deps.Secrets.Resolve(ctx, conn.WebhookURL); err != nil {
		return nil, fmt.Errorf("webhook_url: %w", err)
	}
	for _, t := range []string{conn.AppToken, conn.BotToken} {
		if t != "" {
			deps.Secrets.Track(t)
		}
	}
	return &slackImpl{
		name: name, conn: conn, deps: deps,
		api:   newSlackAPI(conn.BotToken),
		inbox: handoff.NewInbox(),
	}, nil
}

func (s *slackImpl) Validate() error {
	if s.conn.BotToken == "" && s.conn.WebhookURL == "" {
		return fmt.Errorf("connector %q: set bot_token (Web API) or webhook_url (post-only)", s.name)
	}
	return nil
}

func (s *slackImpl) DeclaredEvents() []string { return nil }

// Inbox exposes the reply inbox so main wiring can feed Socket Mode replies
// into pending asks (alongside the legacy handoffs inbox).
func (s *slackImpl) Inbox() *handoff.Inbox { return s.inbox }

// Source lowers the connector's triggers into a slack integration. All
// triggers of one event share a single rule (the integration fires every
// enabled variant); per-trigger filters (reaction/command/channel/users) are
// evaluated by the flow runner against the published context.
func (s *slackImpl) Source(triggers []CompiledTrigger) (core.Integration, error) {
	if len(triggers) == 0 {
		return nil, nil
	}
	if s.conn.AppToken == "" {
		return nil, fmt.Errorf("connector %q: app_token is required to receive slack events", s.name)
	}
	byEvent := map[string]config.ActionSet{}
	order := []string{}
	for _, t := range triggers {
		ev := t.Spec.Event()
		if _, ok := byEvent[ev]; !ok {
			order = append(order, ev)
		}
		byEvent[ev] = append(byEvent[ev], config.Action{
			Name:    t.Spec.Name,
			Enabled: t.Spec.Enabled,
			Shadow:  t.Spec.Shadow,
			FlowRef: t.Ref(),
		})
	}
	cfg := slackint.Config{AppToken: s.conn.AppToken, BotToken: s.conn.BotToken}
	for _, ev := range order {
		cfg.Rules = append(cfg.Rules, slackint.Rule{On: ev, Actions: byEvent[ev]})
	}
	return buildIntegration("slack", s.name, cfg)
}

// FilterEvent evaluates a slack trigger's filters against the event context —
// the flow runner calls this before running the trigger's steps.
func slackFilter(event string, filters map[string]any, trigCtx map[string]any) (bool, error) {
	sc, _ := trigCtx["slack"].(map[string]any)
	get := func(k string) string {
		if sc == nil {
			return ""
		}
		v, _ := sc[k].(string)
		return v
	}
	if want, _ := filters["channel"].(string); want != "" && want != get("channel") {
		return false, nil
	}
	if users := toStrings(filters["users"]); len(users) > 0 {
		ok := false
		for _, u := range users {
			if strings.EqualFold(u, get("user")) {
				ok = true
				break
			}
		}
		if !ok {
			return false, nil
		}
	}
	switch event {
	case "reaction_added":
		if want, _ := filters["reaction"].(string); want != "" && want != get("reaction") {
			return false, nil
		}
	case "slash_command":
		if want, _ := filters["command"].(string); want != "" && want != get("command") {
			return false, nil
		}
	}
	return true, nil
}

func (s *slackImpl) Invoke(ctx context.Context, verb string, opts map[string]any) (map[string]any, error) {
	switch verb {
	case "post":
		channel, _ := opts["channel"].(string)
		user, _ := opts["user"].(string)
		text, _ := opts["text"].(string)
		if text == "" {
			return nil, fmt.Errorf("slack.post: options.text is required")
		}
		// Webhook-only connection: the incoming webhook is the channel — post
		// {"text": …} there (byte-identical to the legacy notify sink).
		if s.conn.BotToken == "" {
			if err := postIncomingWebhook(ctx, s.api.httpc, s.conn.WebhookURL, map[string]string{"text": text}); err != nil {
				return nil, fmt.Errorf("slack.post: %w", err)
			}
			return map[string]any{"ts": "", "channel": ""}, nil
		}
		if channel == "" && user == "" {
			return nil, fmt.Errorf("slack.post: set options.channel or options.user")
		}
		if truthy(opts["ephemeral"]) {
			if channel == "" || user == "" {
				return nil, fmt.Errorf("slack.post: ephemeral needs both channel and user")
			}
			if err := s.api.postEphemeral(ctx, channel, user, text); err != nil {
				return nil, err
			}
			return map[string]any{"ts": "", "channel": channel}, nil
		}
		if channel == "" {
			dm, err := s.api.openDM(ctx, user)
			if err != nil {
				return nil, fmt.Errorf("slack.post: open dm: %w", err)
			}
			channel = dm
		}
		threadTS, _ := opts["thread_ts"].(string)
		ts, err := s.api.postMessage(ctx, channel, threadTS, text)
		if err != nil {
			return nil, err
		}
		return map[string]any{"ts": ts, "channel": channel}, nil
	case "react":
		if s.conn.BotToken == "" {
			return nil, fmt.Errorf("slack.react needs a bot_token (the webhook_url connection is post-only)")
		}
		channel, _ := opts["channel"].(string)
		ts, _ := opts["ts"].(string)
		emoji, _ := opts["emoji"].(string)
		if channel == "" || ts == "" || emoji == "" {
			return nil, fmt.Errorf("slack.react: options.channel, ts, and emoji are required")
		}
		if err := s.api.react(ctx, channel, ts, strings.Trim(emoji, ":")); err != nil {
			return nil, err
		}
		return map[string]any{"ok": true}, nil
	case "ask":
		if s.conn.BotToken == "" {
			return nil, fmt.Errorf("slack.ask needs a bot_token and app_token (the webhook_url connection is post-only)")
		}
		ch, err := s.AskChannel(opts)
		if err != nil {
			return nil, err
		}
		return runAsk(ctx, ch, opts)
	}
	return nil, fmt.Errorf("slack: unknown verb %q", verb)
}

// askChannel builds the hand-off channel an ask presents on.
func (s *slackImpl) AskChannel(opts map[string]any) (handoff.Channel, error) {
	to, _ := opts["to"].(string)
	logf := s.deps.Log
	if logf == nil {
		logf = func(string, ...any) {}
	}
	switch to {
	case "dm":
		user, _ := opts["user"].(string)
		if user == "" {
			return nil, fmt.Errorf("slack.ask: to: dm needs options.user (a Slack user id)")
		}
		return handoff.NewSlackDMChannel(s.api, s.api, user, s.inbox, logf), nil
	case "thread":
		channel, _ := opts["channel"].(string)
		if channel == "" {
			return nil, fmt.Errorf("slack.ask: to: thread needs options.channel")
		}
		return handoff.NewSlackChannel(s.api, channel, s.inbox, logf), nil
	}
	return nil, fmt.Errorf("slack.ask: options.to must be dm|thread, got %q", to)
}

// slackAPI is a minimal Slack Web API client for the verb face. It implements
// handoff.Poster and handoff.DMOpener so ask verbs reuse the hand-off
// channels. The base URL honors PC_SLACK_API_URL for hermetic tests, matching
// internal/handoff's poster.
type slackAPI struct {
	base  string
	token string
	httpc *http.Client
}

func newSlackAPI(botToken string) *slackAPI {
	base := "https://slack.com/api"
	if v := os.Getenv("PC_SLACK_API_URL"); v != "" {
		base = strings.TrimRight(v, "/")
	}
	return &slackAPI{base: base, token: botToken, httpc: &http.Client{Timeout: 15 * time.Second}}
}

// call POSTs one Web API method and fails on ok=false.
func (a *slackAPI) call(ctx context.Context, method string, payload map[string]any, out any) error {
	b, _ := json.Marshal(payload)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.base+"/"+method, bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+a.token)
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	resp, err := a.httpc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	var envelope struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
		TS    string `json:"ts"`
		Chan  struct {
			ID string `json:"id"`
		} `json:"channel"`
	}
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return fmt.Errorf("slack %s: HTTP %d: %w", method, resp.StatusCode, err)
	}
	if !envelope.OK {
		return fmt.Errorf("slack %s: %s", method, envelope.Error)
	}
	if out != nil {
		return json.Unmarshal(raw, out)
	}
	return nil
}

func (a *slackAPI) postMessage(ctx context.Context, channel, threadTS, text string) (string, error) {
	payload := map[string]any{"channel": channel, "text": text}
	if threadTS != "" {
		payload["thread_ts"] = threadTS
	}
	var out struct {
		TS string `json:"ts"`
	}
	if err := a.call(ctx, "chat.postMessage", payload, &out); err != nil {
		return "", err
	}
	return out.TS, nil
}

func (a *slackAPI) postEphemeral(ctx context.Context, channel, user, text string) error {
	return a.call(ctx, "chat.postEphemeral", map[string]any{"channel": channel, "user": user, "text": text}, nil)
}

func (a *slackAPI) react(ctx context.Context, channel, ts, emoji string) error {
	return a.call(ctx, "reactions.add", map[string]any{"channel": channel, "timestamp": ts, "name": emoji}, nil)
}

// Post implements handoff.Poster.
func (a *slackAPI) Post(ctx context.Context, channel, threadTS, text string) (string, error) {
	return a.postMessage(ctx, channel, threadTS, text)
}

// OpenDM implements handoff.DMOpener.
func (a *slackAPI) OpenDM(ctx context.Context, user string) (string, error) {
	return a.openDM(ctx, user)
}

func (a *slackAPI) openDM(ctx context.Context, user string) (string, error) {
	var out struct {
		Channel struct {
			ID string `json:"id"`
		} `json:"channel"`
	}
	if err := a.call(ctx, "conversations.open", map[string]any{"users": user}, &out); err != nil {
		return "", err
	}
	return out.Channel.ID, nil
}
