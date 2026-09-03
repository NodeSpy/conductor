package handoff

import (
	"context"
	"errors"
	"fmt"

	"github.com/NodeSpy/paseo-conductor/internal/config"
)

// ErrNotWired is returned by a hand-off channel's Present when its config
// slipped past validation with a shape buildChannel can't build (e.g. an
// invalid `to`) — defensive, since config.Validate should already have caught
// it. Every channel kind (web, slack, discord) has a real implementation as of
// this build.
var ErrNotWired = errors.New("handoff channel not wired (invalid config reached the registry)")

// WebEntry pairs a configured web hand-off channel with the inbound listen
// address it should be mounted on, so the caller (main.go) can register one
// inbound HTTP handler per web entry without re-deriving the config default.
type WebEntry struct {
	Name   string // the handoffs: entry name
	Listen string // resolved listen address (":8099" default applied)
	Chan   *WebChannel
}

// Registry holds the configured hand-off channels (from `handoffs:`) and
// resolves which one an interactive review step presents its draft on. Mirrors
// controller.Registry's shape and resolution order.
type Registry struct {
	channels    map[string]Channel // user-configured, by name
	defaultName string             // the entry flagged default:true, or ""
	webEntries  []WebEntry

	// slackInbox is shared by every configured `slack:` hand-off entry, so a
	// single slack.SetReplyHook wiring in main.go feeds replies to all of them.
	// hasSlack tracks whether any entry actually uses it (SlackInbox returns nil
	// otherwise, so main.go doesn't wire a hook nobody needs).
	slackInbox *Inbox
	hasSlack   bool

	// discordInbox is shared by every configured `discord:` hand-off entry, fed
	// by RunDiscordGateway's MESSAGE_CREATE handling (see discord_gateway.go).
	// discordTokens collects the distinct bot tokens those entries use, so
	// main.go starts exactly one gateway per token (entries sharing a token
	// share a gateway) rather than one per entry.
	discordInbox  *Inbox
	hasDiscord    bool
	discordTokens []string
}

// NewRegistry builds the hand-off channel set from config. Config is assumed
// already validated (see config.Config.Validate): each entry sets exactly one
// channel sub-block, and at most one is flagged default:true. log may be nil.
func NewRegistry(cfgs map[string]config.HandoffConfig, defaultName string, log func(string, ...any)) *Registry {
	if log == nil {
		log = func(string, ...any) {}
	}
	r := &Registry{
		channels:     make(map[string]Channel, len(cfgs)),
		defaultName:  defaultName,
		slackInbox:   NewInbox(),
		discordInbox: NewInbox(),
	}
	seenTokens := map[string]bool{}
	for name, hc := range cfgs {
		ch := buildChannel(name, hc, r.slackInbox, r.discordInbox, log)
		r.channels[name] = ch
		if hc.Web != nil {
			if w, ok := ch.(*WebChannel); ok {
				r.webEntries = append(r.webEntries, WebEntry{Name: name, Listen: webListen(hc.Web), Chan: w})
			}
		}
		if hc.Slack != nil {
			r.hasSlack = true
		}
		if hc.Discord != nil {
			r.hasDiscord = true
			if hc.Discord.BotToken != "" && !seenTokens[hc.Discord.BotToken] {
				seenTokens[hc.Discord.BotToken] = true
				r.discordTokens = append(r.discordTokens, hc.Discord.BotToken)
			}
		}
	}
	return r
}

// SlackInbox returns the Inbox shared by every configured `slack:` hand-off
// entry, or nil when none is configured. main.go wires this to
// slack.SetReplyHook so the Socket Mode integration feeds replies into it — see
// cmd/paseo-conductor/main.go.
func (r *Registry) SlackInbox() *Inbox {
	if !r.hasSlack {
		return nil
	}
	return r.slackInbox
}

// DiscordInbox returns the Inbox shared by every configured `discord:`
// hand-off entry, or nil when none is configured. main.go feeds it from
// RunDiscordGateway (one goroutine per distinct bot token — see
// DiscordBotTokens), so a gateway's MESSAGE_CREATE resolves the right pending
// hand-off — see cmd/paseo-conductor/main.go.
func (r *Registry) DiscordInbox() *Inbox {
	if !r.hasDiscord {
		return nil
	}
	return r.discordInbox
}

// DiscordBotTokens returns the distinct bot tokens used by configured
// `discord:` hand-off entries, in first-seen order. main.go starts one
// RunDiscordGateway per token — entries sharing a token share a gateway
// connection instead of opening a redundant duplicate. Empty when no discord
// hand-off is configured.
func (r *Registry) DiscordBotTokens() []string { return r.discordTokens }

// webListen returns the entry's configured listen address, defaulting to
// :8099 (mirrors the default previously applied in cmd/paseo-conductor/main.go).
func webListen(w *config.HandoffWeb) string {
	if w.Listen != "" {
		return w.Listen
	}
	return ":8099"
}

// buildChannel constructs one hand-off channel from its config: a Web entry
// builds a real *WebChannel wired to its tunnel (see NewTunnel); a Slack entry
// builds a real *SlackChannel (dm or thread, per hc.Slack.To) sharing
// slackInbox with every other slack entry; a Discord entry builds a real
// *DiscordChannel (dm or thread, per hc.Discord.To) sharing discordInbox with
// every other discord entry, fed by RunDiscordGateway (see
// cmd/paseo-conductor/main.go). A malformed `to` that slipped past
// config.Validate falls back to a stub whose Present fails loudly with
// ErrNotWired rather than a nil dereference.
func buildChannel(name string, hc config.HandoffConfig, slackInbox, discordInbox *Inbox, log func(string, ...any)) Channel {
	switch {
	case hc.Web != nil:
		w := NewWebChannel(hc.Web.BaseURL, hc.Web.TTL.D(), log)
		t, err := NewTunnel(hc.Web.Tunnel, hc.Web.BaseURL, log)
		if err != nil {
			// config.Validate already guards the provider/mode/ssh_host/url_pattern
			// shape, so this only fires when a caller builds a Registry from
			// unvalidated config; fall back to base_url rather than leaving the
			// channel unusable.
			log("handoff %s: tunnel config invalid (%v); falling back to base_url", name, err)
		} else {
			w.SetTunnel(t, webListen(hc.Web))
		}
		return w
	case hc.Slack != nil:
		// config.Validate already guards `to`/channel/user/bot_token, so this
		// only fires when a caller builds a Registry from unvalidated config.
		if hc.Slack.To != "dm" && hc.Slack.To != "thread" {
			log("handoff %s: slack.to must be dm|thread, got %q; hand-off will fail on use", name, hc.Slack.To)
			return notWiredChannel{name: name}
		}
		poster := NewWebAPIPoster(hc.Slack.BotToken)
		if hc.Slack.To == "dm" {
			return NewSlackDMChannel(poster, poster, hc.Slack.User, slackInbox, log)
		}
		return NewSlackChannel(poster, hc.Slack.Channel, slackInbox, log)
	case hc.Discord != nil:
		// config.Validate already guards `to`/channel/user/bot_token, so this
		// only fires when a caller builds a Registry from unvalidated config.
		if hc.Discord.To != "dm" && hc.Discord.To != "thread" {
			log("handoff %s: discord.to must be dm|thread, got %q; hand-off will fail on use", name, hc.Discord.To)
			return notWiredChannel{name: name}
		}
		poster := NewRESTPoster(hc.Discord.BotToken)
		if hc.Discord.To == "dm" {
			return NewDiscordDMChannel(poster, poster, hc.Discord.User, discordInbox, log)
		}
		return NewDiscordChannel(poster, hc.Discord.Channel, discordInbox, log)
	default:
		// config.Validate already rejected zero/multiple channel sub-blocks set.
		return notWiredChannel{name: name}
	}
}

// WebEntries returns the configured web hand-off channels paired with their
// resolved listen address, so the caller mounts one inbound HTTP handler per
// entry (see cmd/paseo-conductor/main.go).
func (r *Registry) WebEntries() []WebEntry { return r.webEntries }

// Resolve returns the hand-off channel a step should present its draft on,
// applying the resolution order: an explicit per-step `handoff:` name → the
// entry flagged default:true → the sole configured entry → nil (no error) when
// none of those apply, meaning the review hand-off keeps paseo-native behavior.
func (r *Registry) Resolve(name string) (Channel, error) {
	if name != "" {
		ch, ok := r.channels[name]
		if !ok {
			return nil, fmt.Errorf("unknown handoff %q", name)
		}
		return ch, nil
	}
	if r.defaultName != "" {
		ch, ok := r.channels[r.defaultName]
		if !ok {
			return nil, fmt.Errorf("default handoff %q not found", r.defaultName)
		}
		return ch, nil
	}
	if len(r.channels) == 1 {
		for _, ch := range r.channels {
			return ch, nil
		}
	}
	return nil, nil
}

// notWiredChannel is a defensive fallback for a slack/discord entry with an
// invalid `to` that somehow skipped config.Validate (every channel kind has a
// real implementation as of this build — this is not the normal path for any
// of them). It satisfies Channel so the registry and its resolution order stay
// fully exercisable; only Present is refused.
type notWiredChannel struct{ name string }

func (c notWiredChannel) Present(context.Context, Draft) (Presentation, error) {
	return nil, fmt.Errorf("handoff %q: %w", c.name, ErrNotWired)
}
