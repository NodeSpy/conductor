package handoff

import (
	"context"
	"fmt"
	"strings"
	"sync"
)

// Poster posts a message to a Slack channel (optionally threaded) and returns the
// posted message's ts. The real implementation (WebAPIPoster, see
// slack_poster.go) wraps chat.postMessage; tests inject a fake. Kept small so
// the channel doesn't depend on a Slack SDK.
type Poster interface {
	Post(ctx context.Context, channel, threadTS, text string) (ts string, err error)
}

// DMOpener opens a Slack IM (DM) channel with a user, returning the channel id
// that channel's posts/replies address. Only needed for `to: dm` channels; a
// `to: thread` channel never calls it. The real implementation
// (WebAPIPoster, see slack_poster.go) wraps conversations.open.
type DMOpener interface {
	OpenDM(ctx context.Context, user string) (channel string, err error)
}

// slackMode selects how a SlackChannel presents a draft: in a channel thread, or
// in a DM.
type slackMode int

const (
	modeThread slackMode = iota
	modeDM
)

// SlackChannel presents a draft over Slack and captures your decision from a
// reply. Two modes:
//
//   - thread (NewSlackChannel): posts to a fixed channel, starting a thread;
//     a reply in that thread resolves the Await.
//   - dm (NewSlackDMChannel): opens (or reuses) an IM channel with a configured
//     user and posts there; since DM replies carry no thread_ts, any message
//     landing on that IM channel resolves the Await.
//
// Either way, replies arrive over the Slack integration's Socket Mode
// connection, not here, so the channel registers each pending (channel,
// threadTS) pair in a shared Inbox the integration feeds via Deliver — that
// indirection is what keeps this package free of the socket loop and
// unit-testable without a live Slack.
type SlackChannel struct {
	poster Poster
	mode   slackMode
	inbox  *Inbox
	log    func(string, ...any)

	channel string // mode: thread — channel to post in

	opener DMOpener // mode: dm — resolves user -> IM channel
	user   string   // mode: dm — target user id

	dmMu      sync.Mutex
	dmChannel string // mode: dm — cached IM channel id, resolved on first Present
}

// NewSlackChannel builds a thread-mode Slack hand-off channel: it posts to
// channel (opening a thread per draft) and captures replies routed through
// inbox. log may be nil.
func NewSlackChannel(poster Poster, channel string, inbox *Inbox, log func(string, ...any)) *SlackChannel {
	if log == nil {
		log = func(string, ...any) {}
	}
	return &SlackChannel{poster: poster, mode: modeThread, channel: channel, inbox: inbox, log: log}
}

// NewSlackDMChannel builds a dm-mode Slack hand-off channel: it opens (or
// reuses) a DM with user via opener and posts drafts there, capturing replies
// (any message on that IM channel — DMs carry no thread_ts) routed through
// inbox. log may be nil.
func NewSlackDMChannel(poster Poster, opener DMOpener, user string, inbox *Inbox, log func(string, ...any)) *SlackChannel {
	if log == nil {
		log = func(string, ...any) {}
	}
	return &SlackChannel{poster: poster, mode: modeDM, opener: opener, user: user, inbox: inbox, log: log}
}

// Present posts the draft (to the configured channel/thread, or the resolved
// DM) and registers it so a reply resolves the Await.
func (c *SlackChannel) Present(ctx context.Context, d Draft) (Presentation, error) {
	if c.mode == modeDM {
		return c.presentDM(ctx, d)
	}
	return c.presentThread(ctx, d)
}

func (c *SlackChannel) presentThread(ctx context.Context, d Draft) (Presentation, error) {
	ts, err := c.poster.Post(ctx, c.channel, "", renderSlackDraft(d))
	if err != nil {
		return nil, fmt.Errorf("slack handoff: post draft: %w", err)
	}
	pend := c.inbox.register(c.channel, ts)
	c.log("handoff: draft %s posted to slack %s thread %s", d.ID, c.channel, ts)
	return &slackPresentation{c: c, channel: c.channel, threadTS: ts, pend: pend}, nil
}

func (c *SlackChannel) presentDM(ctx context.Context, d Draft) (Presentation, error) {
	imChannel, err := c.dmChannelID(ctx)
	if err != nil {
		return nil, fmt.Errorf("slack handoff: open dm with %s: %w", c.user, err)
	}
	if _, err := c.poster.Post(ctx, imChannel, "", renderSlackDraft(d)); err != nil {
		return nil, fmt.Errorf("slack handoff: post draft: %w", err)
	}
	// DM replies carry no thread_ts — register with an empty one, so Deliver
	// matches any message landing on this IM channel (see Inbox).
	pend := c.inbox.register(imChannel, "")
	c.log("handoff: draft %s posted to slack dm %s (channel %s)", d.ID, c.user, imChannel)
	return &slackPresentation{c: c, channel: imChannel, dm: true, pend: pend}, nil
}

// dmChannelID resolves (and caches) the IM channel id for c.user. Slack's
// conversations.open is idempotent — repeated calls for the same user return
// the same channel id — so caching just avoids a redundant API call per draft;
// concurrent Present calls are safe.
func (c *SlackChannel) dmChannelID(ctx context.Context) (string, error) {
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

func renderSlackDraft(d Draft) string {
	var b strings.Builder
	if d.Title != "" {
		b.WriteString("*")
		b.WriteString(d.Title)
		b.WriteString("*\n")
	}
	b.WriteString(d.Body)
	b.WriteString("\n\n_Reply in this thread:_ `approve`, `discard`, or the revised text to send back to the agent.")
	return b.String()
}

// slackPresentation is one draft posted to a thread (or a DM), awaiting a
// reply. channel is the real channel or IM channel id it was posted to;
// threadTS is empty for a DM (see dm).
type slackPresentation struct {
	c        *SlackChannel
	channel  string
	threadTS string
	dm       bool
	pend     *slackPending
}

func (p *slackPresentation) Ref() string {
	if p.dm {
		return "slack:" + p.channel + ":dm"
	}
	return "slack:" + p.channel + ":" + p.threadTS
}

func (p *slackPresentation) Await(ctx context.Context) (Decision, error) {
	select {
	case <-ctx.Done():
		return Decision{}, ctx.Err()
	case d := <-p.pend.done:
		return d, nil
	}
}

func (p *slackPresentation) Close() {
	p.c.inbox.unregister(p.channel, p.threadTS)
}

// Inbox routes a captured Slack reply to the Presentation waiting on it. The
// Slack integration calls Deliver for every inbound message; if a hand-off is
// pending on that (channel, thread_ts) pair the reply is parsed into a Decision
// and the Await resolves. Deliver reports whether it consumed the message, so
// the integration can tell a hand-off reply from ordinary chatter.
//
// The same (channel, threadTS) key serves both hand-off modes: a thread reply
// matches on (channel, thread_ts); a DM reply carries no thread_ts, so it
// matches on (im channel, "") — register/Deliver never special-case which, the
// empty-threadTS case just falls out of using it as an ordinary map key. Safe
// for concurrent use by the integration's event loop and any number of pending
// hand-offs.
type Inbox struct {
	mu      sync.Mutex
	pending map[string]*slackPending // key: channel + ":" + threadTS ("" for a DM)
}

type slackPending struct {
	done chan Decision
	once sync.Once
}

// NewInbox builds an empty Inbox.
func NewInbox() *Inbox { return &Inbox{pending: map[string]*slackPending{}} }

func inboxKey(channel, threadTS string) string { return channel + ":" + threadTS }

func (i *Inbox) register(channel, threadTS string) *slackPending {
	p := &slackPending{done: make(chan Decision, 1)}
	i.mu.Lock()
	i.pending[inboxKey(channel, threadTS)] = p
	i.mu.Unlock()
	return p
}

func (i *Inbox) unregister(channel, threadTS string) {
	i.mu.Lock()
	delete(i.pending, inboxKey(channel, threadTS))
	i.mu.Unlock()
}

// Deliver routes a thread reply to a pending hand-off, if one is waiting on that
// thread. Returns true when the reply resolved a hand-off (so the caller treats
// it as consumed, not as a fresh command).
func (i *Inbox) Deliver(channel, threadTS, text string) bool {
	i.mu.Lock()
	p := i.pending[inboxKey(channel, threadTS)]
	i.mu.Unlock()
	if p == nil {
		return false
	}
	delivered := false
	p.once.Do(func() {
		p.done <- parseReply(text)
		delivered = true
	})
	return delivered
}
