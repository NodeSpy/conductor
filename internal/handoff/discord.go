package handoff

import (
	"context"
	"fmt"
	"strings"
	"sync"
)

// discordMode selects how a DiscordChannel presents a draft: in a channel (or
// a channel that's itself a thread), or in a DM.
type discordMode int

const (
	discordModeThread discordMode = iota
	discordModeDM
)

// DiscordChannel presents a draft over Discord and captures your decision from
// a reply. Two modes, mirroring SlackChannel:
//
//   - thread (NewDiscordChannel): posts to a fixed channel id (which may itself
//     be a Discord thread channel); any message landing on that channel
//     resolves the Await.
//   - dm (NewDiscordDMChannel): opens (or reuses) a DM channel with a
//     configured Discord user id and posts there.
//
// Discord has no Slack-style thread_ts — a reply is just another message on
// the same channel id — so both modes register their pending Await in the
// shared Inbox keyed by (channelID, "") (see Inbox; the empty second key
// component is exactly the DM convention Slack's Inbox already supports).
// Replies arrive over the Discord gateway connection, not here (see
// discord_gateway.go), which is what keeps this package free of the
// websocket loop and unit-testable without a live Discord.
type DiscordChannel struct {
	poster Poster
	mode   discordMode
	inbox  *Inbox
	log    func(string, ...any)

	channel string // mode: thread — channel (or thread-channel) id to post in

	opener DMOpener // mode: dm — resolves user -> DM channel id
	user   string   // mode: dm — target Discord user id

	dmMu      sync.Mutex
	dmChannel string // mode: dm — cached DM channel id, resolved on first Present
}

// NewDiscordChannel builds a thread-mode Discord hand-off channel: it posts to
// channel and captures replies (any message landing on that channel) routed
// through inbox. log may be nil.
func NewDiscordChannel(poster Poster, channel string, inbox *Inbox, log func(string, ...any)) *DiscordChannel {
	if log == nil {
		log = func(string, ...any) {}
	}
	return &DiscordChannel{poster: poster, mode: discordModeThread, channel: channel, inbox: inbox, log: log}
}

// NewDiscordDMChannel builds a dm-mode Discord hand-off channel: it opens (or
// reuses) a DM with user via opener and posts drafts there, capturing replies
// routed through inbox. log may be nil.
func NewDiscordDMChannel(poster Poster, opener DMOpener, user string, inbox *Inbox, log func(string, ...any)) *DiscordChannel {
	if log == nil {
		log = func(string, ...any) {}
	}
	return &DiscordChannel{poster: poster, mode: discordModeDM, opener: opener, user: user, inbox: inbox, log: log}
}

// Present posts the draft (to the configured channel, or the resolved DM) and
// registers it so a reply resolves the Await.
func (c *DiscordChannel) Present(ctx context.Context, d Draft) (Presentation, error) {
	if c.mode == discordModeDM {
		return c.presentDM(ctx, d)
	}
	return c.presentThread(ctx, d)
}

func (c *DiscordChannel) presentThread(ctx context.Context, d Draft) (Presentation, error) {
	if _, err := c.poster.Post(ctx, c.channel, "", renderDiscordDraft(d)); err != nil {
		return nil, fmt.Errorf("discord handoff: post draft: %w", err)
	}
	pend := c.inbox.register(c.channel, "")
	c.log("handoff: draft %s posted to discord channel %s", d.ID, c.channel)
	return &discordPresentation{c: c, channel: c.channel, pend: pend}, nil
}

func (c *DiscordChannel) presentDM(ctx context.Context, d Draft) (Presentation, error) {
	dmChannel, err := c.dmChannelID(ctx)
	if err != nil {
		return nil, fmt.Errorf("discord handoff: open dm with %s: %w", c.user, err)
	}
	if _, err := c.poster.Post(ctx, dmChannel, "", renderDiscordDraft(d)); err != nil {
		return nil, fmt.Errorf("discord handoff: post draft: %w", err)
	}
	pend := c.inbox.register(dmChannel, "")
	c.log("handoff: draft %s posted to discord dm %s (channel %s)", d.ID, c.user, dmChannel)
	return &discordPresentation{c: c, channel: dmChannel, dm: true, pend: pend}, nil
}

// dmChannelID resolves (and caches) the DM channel id for c.user. Discord's
// POST /users/@me/channels is idempotent for a given recipient, so caching
// just avoids a redundant API call per draft; concurrent Present calls are
// safe.
func (c *DiscordChannel) dmChannelID(ctx context.Context) (string, error) {
	c.dmMu.Lock()
	defer c.dmMu.Unlock()
	if c.dmChannel != "" {
		return c.dmChannel, nil
	}
	ch, err := c.opener.OpenDM(ctx, c.user)
	if err != nil {
		return "", err
	}
	c.dmChannel = ch
	return ch, nil
}

func renderDiscordDraft(d Draft) string {
	var b strings.Builder
	if d.Title != "" {
		b.WriteString("**")
		b.WriteString(d.Title)
		b.WriteString("**\n")
	}
	b.WriteString(d.Body)
	b.WriteString("\n\n_Reply here:_ `approve`, `discard`, or the revised text to send back to the agent.")
	return b.String()
}

// discordPresentation is one draft posted to a channel (or a DM), awaiting a
// reply. channel is the real channel or DM channel id it was posted to.
type discordPresentation struct {
	c       *DiscordChannel
	channel string
	dm      bool
	pend    *slackPending
}

func (p *discordPresentation) Ref() string {
	if p.dm {
		return "discord:" + p.channel + ":dm"
	}
	return "discord:" + p.channel + ":thread"
}

func (p *discordPresentation) Await(ctx context.Context) (Decision, error) {
	select {
	case <-ctx.Done():
		return Decision{}, ctx.Err()
	case d := <-p.pend.done:
		return d, nil
	}
}

func (p *discordPresentation) Close() {
	p.c.inbox.unregister(p.channel, "")
}
