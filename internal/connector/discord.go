package connector

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/core"
	"github.com/NodeSpy/conductor/internal/handoff"
)

var discordDecl = &TypeDecl{
	Type: "discord",
	Desc: "Discord: post messages and asks over a bot; no source events (replies arrive via the discord gateway, wired directly to Inbox).",
	Connection: Schema{
		"bot_token":   {Type: TString, Desc: "Discord bot token (from the developer portal)"},
		"webhook_url": {Type: TString, Desc: "an incoming-webhook URL — post-only alternative to a bot token"},
	},
	Verbs: []VerbDecl{
		{
			Name: "post", Desc: "post a message to a channel or DM",
			Options: Schema{
				"channel": {Type: TString, Desc: "channel id (or set user: for a DM)"},
				"user":    {Type: TString, Desc: "user id to DM"},
				"text":    {Type: TString, Required: true},
			},
			Outputs: Schema{"id": {Type: TString}, "channel": {Type: TString}},
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

func init() { RegisterType(discordDecl, newDiscordImpl) }

// discordConn is a discord connector's connection config.
type discordConn struct {
	BotToken string `yaml:"bot_token"`
	// WebhookURL is the post-only alternative: with no bot_token, `post`
	// sends {"content": …} there (the legacy notify sink's exact payload).
	WebhookURL string `yaml:"webhook_url"`
}

// discordPoster is the Post+OpenDM surface discord's post/ask verbs need.
// handoff.RESTPoster (the production implementation) satisfies it; tests
// inject a fake pointed at an httptest server — handoff.RESTPoster itself
// talks to a package-level base URL fixed at handoff's init() time (see
// discord_poster.go), which a later PC_DISCORD_API_URL env-var override from
// this package's tests can't reach.
type discordPoster interface {
	handoff.Poster
	handoff.DMOpener
}

type discordImpl struct {
	name string
	conn discordConn
	deps Deps

	poster discordPoster
	// inbox captures channel/DM replies for ask verbs; the discord gateway
	// (RunDiscordGateway) delivers them via the reply hook wired in main.
	inbox *handoff.Inbox
}

func newDiscordImpl(name string, ref config.ConnectorRef, deps Deps) (Impl, error) {
	var conn discordConn
	if err := ref.Decode(&conn); err != nil {
		return nil, fmt.Errorf("connector %q: decode discord connection: %w", name, err)
	}
	ctx := context.Background()
	var err error
	if conn.BotToken, err = deps.Secrets.Resolve(ctx, conn.BotToken); err != nil {
		return nil, fmt.Errorf("bot_token: %w", err)
	}
	if conn.WebhookURL, err = deps.Secrets.Resolve(ctx, conn.WebhookURL); err != nil {
		return nil, fmt.Errorf("webhook_url: %w", err)
	}
	if conn.BotToken != "" {
		deps.Secrets.Track(conn.BotToken)
	}
	return &discordImpl{
		name: name, conn: conn, deps: deps,
		poster: handoff.NewRESTPoster(conn.BotToken),
		inbox:  handoff.NewInbox(),
	}, nil
}

func (d *discordImpl) Validate() error {
	if d.conn.BotToken == "" && d.conn.WebhookURL == "" {
		return fmt.Errorf("connector %q: set bot_token (Web API) or webhook_url (post-only)", d.name)
	}
	return nil
}

func (d *discordImpl) DeclaredEvents() []string { return nil }

// Inbox exposes the reply inbox so main wiring can feed the discord gateway's
// MESSAGE_CREATE events (see RunDiscordGateway) into pending asks.
func (d *discordImpl) Inbox() *handoff.Inbox { return d.inbox }

// BotToken exposes the connector's bot token so main wiring knows which
// gateway connection (RunDiscordGateway) feeds this connector's Inbox.
func (d *discordImpl) BotToken() string { return d.conn.BotToken }

// Source: discord has no source events. Present triggers referencing it are
// a config error (there is nothing for them to fire on); with none, there is
// nothing to lower.
func (d *discordImpl) Source(triggers []CompiledTrigger) (core.Integration, error) {
	if len(triggers) == 0 {
		return nil, nil
	}
	return nil, fmt.Errorf("connector %q (discord) has no source events", d.name)
}

func (d *discordImpl) Invoke(ctx context.Context, verb string, opts map[string]any) (map[string]any, error) {
	switch verb {
	case "post":
		channel, _ := opts["channel"].(string)
		user, _ := opts["user"].(string)
		text, _ := opts["text"].(string)
		if text == "" {
			return nil, fmt.Errorf("discord.post: options.text is required")
		}
		// Webhook-only connection: post {"content": …} to the incoming
		// webhook (byte-identical to the legacy notify sink).
		if d.conn.BotToken == "" {
			if err := postIncomingWebhook(ctx, d.httpc(), d.conn.WebhookURL, map[string]string{"content": text}); err != nil {
				return nil, fmt.Errorf("discord.post: %w", err)
			}
			return map[string]any{"id": "", "channel": ""}, nil
		}
		if channel == "" && user == "" {
			return nil, fmt.Errorf("discord.post: set options.channel or options.user")
		}
		if channel == "" {
			dm, err := d.poster.OpenDM(ctx, user)
			if err != nil {
				return nil, fmt.Errorf("discord.post: open dm: %w", err)
			}
			channel = dm
		}
		id, err := d.poster.Post(ctx, channel, "", text)
		if err != nil {
			return nil, err
		}
		return map[string]any{"id": id, "channel": channel}, nil
	case "ask":
		if d.conn.BotToken == "" {
			return nil, fmt.Errorf("discord.ask needs a bot_token (the webhook_url connection is post-only)")
		}
		ch, err := d.AskChannel(opts)
		if err != nil {
			return nil, err
		}
		return runAsk(ctx, ch, opts)
	}
	return nil, fmt.Errorf("discord: unknown verb %q", verb)
}

// AskChannel builds the hand-off channel an ask presents on — it also
// implements AskChanneler so a background step's `handoff: <name>` review
// rides the same channel as this connector's own ask verb.
func (d *discordImpl) AskChannel(opts map[string]any) (handoff.Channel, error) {
	to, _ := opts["to"].(string)
	logf := d.deps.Log
	if logf == nil {
		logf = func(string, ...any) {}
	}
	switch to {
	case "dm":
		user, _ := opts["user"].(string)
		if user == "" {
			return nil, fmt.Errorf("discord.ask: to: dm needs options.user (a Discord user id)")
		}
		return handoff.NewDiscordDMChannel(d.poster, d.poster, user, d.inbox, logf), nil
	case "thread":
		channel, _ := opts["channel"].(string)
		if channel == "" {
			return nil, fmt.Errorf("discord.ask: to: thread needs options.channel")
		}
		return handoff.NewDiscordChannel(d.poster, channel, d.inbox, logf), nil
	}
	return nil, fmt.Errorf("discord.ask: options.to must be dm|thread, got %q", to)
}

// httpc is the webhook-post client (the REST poster owns its own).
func (d *discordImpl) httpc() *http.Client {
	return &http.Client{Timeout: 10 * time.Second}
}
