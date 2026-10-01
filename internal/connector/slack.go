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

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/core"
	"github.com/NodeSpy/conductor/internal/handoff"
	slackint "github.com/NodeSpy/conductor/internal/integrations/slack"
)

// slackCtx is the context schema slack events publish (nested under .slack).
func slackCtxSchema() Schema {
	return Schema{
		"slack.channel": {Type: TString}, "slack.user": {Type: TString},
		"slack.text": {Type: TString}, "slack.ts": {Type: TString},
		"slack.thread_ts": {Type: TString}, "slack.reaction": {Type: TString},
		"slack.command": {Type: TString},
		// Interactive (form/shortcut) additions; present on every event so a
		// template never trips on a missing key.
		"slack.form":        {Type: TMap, Desc: "submitted form values by field name"},
		"slack.via":         {Type: TString, Desc: "shortcut | mention (how a form trigger fired)"},
		"slack.callback_id": {Type: TString, Desc: "message_shortcut: the shortcut's callback id"},
		"slack.files":       {Type: TList, Desc: "files on the triggering message: {id, name, mimetype}"},
		"repo":              {Type: TString}, "number": {Type: TInt},
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
			Name: "app_mention", Desc: "the bot was @-mentioned (with options.form: the mentioning user gets a private button that opens the form; the trigger fires on submission)",
			Filters: Schema{
				"channel": {Type: TString, Desc: "only this channel id"},
				"users":   {Type: TList, Desc: "only these user ids"},
			},
			Options: slackFormOptions(),
			Context: slackCtxSchema(),
		},
		{
			Name: "message_shortcut", Desc: "a message shortcut was used on a message (with options.form: opens the form first; the trigger fires on submission)",
			Filters: Schema{
				"callback_id": {Type: TString, Desc: "only this shortcut callback id"},
				"channel":     {Type: TString, Desc: "only this channel id"},
				"users":       {Type: TList, Desc: "only these user ids (required unless options.any_user)"},
			},
			Options: slackFormOptions(),
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
				"channel":   {Type: TString, Scope: "channel", Desc: "channel id (or set user: for a DM)"},
				"user":      {Type: TString, Scope: "user", Desc: "user id to DM"},
				"text":      {Type: TString, Required: true},
				"thread_ts": {Type: TString, Desc: "post into this thread"},
				"ephemeral": {Type: TBool, Desc: "visible only to user: (requires channel: and user:)"},
			},
			Outputs: Schema{"ts": {Type: TString}, "channel": {Type: TString}},
		},
		{
			Name: "react", Desc: "add a reaction to a message",
			Options: Schema{
				"channel": {Type: TString, Required: true, Scope: "channel"},
				"ts":      {Type: TString, Required: true, Desc: "message timestamp to react to"},
				"emoji":   {Type: TString, Required: true, Desc: "emoji name, no colons"},
			},
			Outputs: Schema{"ok": {Type: TBool}},
		},
		{
			Name: "ask", Desc: "present a question/draft and wait for the reply", Ask: true,
			Options: mergeSchema(askOptionBase(), Schema{
				"to":        {Type: TString, Enum: []string{"dm", "thread"}, Required: true},
				"user":      {Type: TString, Scope: "user", Desc: "user id (to: dm)"},
				"channel":   {Type: TString, Scope: "channel", Desc: "channel id (to: thread)"},
				"approvers": {Type: TList, Desc: "to: thread — only these user ids may resolve the ask (default: anyone in the channel)"},
			}),
			Outputs: askOutputs(),
		},
	},
}

// slackFormOptions are the per-trigger options of the form-capable events.
func slackFormOptions() Schema {
	return Schema{
		"form":     {Type: TMap, Desc: "a modal to collect before firing: {title, submit, fields: [{name, label, type: select|text|textarea, options, default, optional}]}"},
		"any_user": {Type: TBool, Desc: "allow a form/shortcut trigger with no users: filter (anyone in the workspace)"},
	}
}

func init() {
	slackDecl.Filter = slackint.FilterMatch
	slackDecl.ValidateTrigger = validateSlackTrigger
	slackDecl.Verbs = append(slackDecl.Verbs, slackFileVerbs()...)
	RegisterType(slackDecl, newSlackImpl)
}

// validateSlackTrigger enforces the form/shortcut rules at load: the form
// itself must be valid, and a trigger that opens a form or answers a message
// shortcut must name who may use it — a non-empty `users:` filter every
// match path requires — unless `options.any_user: true` opts out.
func validateSlackTrigger(event string, spec config.TriggerSpec) error {
	form, err := slackint.ParseForm(spec.Options["form"])
	if err != nil {
		return err
	}
	if form != nil && event != "app_mention" && event != "message_shortcut" {
		return fmt.Errorf("options.form applies to app_mention and message_shortcut only")
	}
	if form == nil && event != "message_shortcut" {
		return nil
	}
	if truthy(spec.Options["any_user"]) {
		return nil
	}
	if len(requiredMatchValues(spec.Filter, "users")) == 0 {
		return fmt.Errorf("a %s trigger needs a non-empty `users:` filter (Slack user ids allowed to use it), or options.any_user: true to allow anyone in the workspace", map[bool]string{true: "form", false: "message_shortcut"}[form != nil])
	}
	return nil
}

// requiredMatchValues returns the string values of match key `key` that
// EVERY path through filter f requires: a non-negated match under ANDs, or
// one on each branch of an OR (their union). nil when some path to a match
// does not require the key.
func requiredMatchValues(f *config.Filter, key string) []string {
	if f == nil {
		return nil
	}
	switch f.Op {
	case config.FilterOpMatch:
		if f.Key == key {
			return toStrings(f.Val)
		}
	case config.FilterOpAnd:
		for _, k := range f.Kids {
			if v := requiredMatchValues(k, key); len(v) > 0 {
				return v
			}
		}
	case config.FilterOpOr:
		var out []string
		for _, k := range f.Kids {
			v := requiredMatchValues(k, key)
			if len(v) == 0 {
				return nil
			}
			out = append(out, v...)
		}
		return out
	}
	return nil
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
	// names caches users.info display names for the thread verb.
	names userNames
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
	// The incoming-webhook URL embeds a bearer token in its path — it is a
	// credential, not a public endpoint, so track it alongside the tokens.
	for _, t := range []string{conn.AppToken, conn.BotToken, conn.WebhookURL} {
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

// ContextScope is the resource-scoping adapter hook (scope.go): the channel
// and user a slack-triggered dispatch may address WITHOUT an operator grant
// are the ones the event itself came from — replying where you were spoken to
// needs no config, while any other channel is a listed grant.
//
// The facts come from the trigger context the slack source publishes
// (`{{.slack.channel}}`), so this reads the same values a step's template
// would. A dispatch from another source carries none and returns "" — deny,
// unless the operator listed a channel.
func (s *slackImpl) ContextScope(dim string, t core.Trigger) string {
	sctx, _ := t.Context["slack"].(map[string]any)
	if sctx == nil {
		return ""
	}
	switch dim {
	case "channel":
		v, _ := sctx["channel"].(string)
		return v
	case "user":
		v, _ := sctx["user"].(string)
		return v
	}
	return ""
}

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
	var interactive []slackint.Rule
	for _, t := range triggers {
		ev := t.Spec.Event()
		form, err := slackint.ParseForm(t.Spec.Options["form"])
		if err != nil {
			return nil, fmt.Errorf("connector %q: trigger on %s: %w", s.name, t.Spec.On, err)
		}
		if form != nil || ev == "message_shortcut" {
			// A form/shortcut trigger is its own rule: the integration
			// pre-matches it (users + the trigger's filter) synchronously,
			// opens ITS form, and fires exactly it on submission.
			cb := requiredMatchValues(t.Spec.Filter, "callback_id")
			r := slackint.Rule{
				On: ev, Form: form, Key: t.Ref(), Filter: t.Spec.Filter,
				Users: requiredMatchValues(t.Spec.Filter, "users"), AnyUser: truthy(t.Spec.Options["any_user"]),
				Actions: config.ActionSet{{Name: t.Spec.Name, Enabled: t.Spec.Enabled, Shadow: t.Spec.Shadow, FlowRef: t.Ref()}},
			}
			if len(cb) == 1 {
				r.CallbackID = cb[0]
			}
			interactive = append(interactive, r)
			continue
		}
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
	cfg.Rules = append(cfg.Rules, interactive...)
	return buildIntegration("slack", s.name, cfg)
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
	case "thread", "download":
		if s.conn.BotToken == "" {
			return nil, fmt.Errorf("slack.%s needs a bot_token (the webhook_url connection is post-only)", verb)
		}
		if verb == "thread" {
			return s.threadVerb(ctx, opts)
		}
		return s.downloadVerb(ctx, opts)
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
		return handoff.NewSlackChannel(s.api, channel, askApprovers(opts), s.inbox, logf), nil
	}
	return nil, fmt.Errorf("slack.ask: options.to must be dm|thread, got %q", to)
}

// askApprovers reads the optional options.approvers list (user ids allowed
// to resolve a thread-mode ask; empty = anyone in the channel).
func askApprovers(opts map[string]any) []string {
	var out []string
	switch v := opts["approvers"].(type) {
	case []any:
		for _, e := range v {
			if s, ok := e.(string); ok && s != "" {
				out = append(out, s)
			}
		}
	case []string:
		out = v
	case string:
		if v != "" {
			out = []string{v}
		}
	}
	return out
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
