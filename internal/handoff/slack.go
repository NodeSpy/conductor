package handoff

import (
	"context"
	"fmt"
	"strings"
	"sync"
)

// Poster posts a message to a Slack channel (optionally threaded) and returns the
// posted message's ts. The real implementation wraps chat.postMessage; tests
// inject a fake. Kept small so the channel doesn't depend on a Slack SDK.
type Poster interface {
	Post(ctx context.Context, channel, threadTS, text string) (ts string, err error)
}

// SlackChannel presents a draft by posting it to a Slack channel (starting a
// thread) and captures your decision from a reply in that thread. Replies arrive
// over the Slack integration's Socket Mode connection, not here, so the channel
// registers each pending thread in a shared Inbox the integration feeds via
// Deliver — that indirection is what keeps this package free of the socket loop
// and unit-testable without a live Slack.
type SlackChannel struct {
	poster  Poster
	channel string
	inbox   *Inbox
	log     func(string, ...any)
}

// NewSlackChannel builds a Slack hand-off channel that posts to channel and
// captures replies routed through inbox. log may be nil.
func NewSlackChannel(poster Poster, channel string, inbox *Inbox, log func(string, ...any)) *SlackChannel {
	if log == nil {
		log = func(string, ...any) {}
	}
	return &SlackChannel{poster: poster, channel: channel, inbox: inbox, log: log}
}

// Present posts the draft to the channel (opening a thread) and registers the
// thread so a reply resolves the Await.
func (c *SlackChannel) Present(ctx context.Context, d Draft) (Presentation, error) {
	ts, err := c.poster.Post(ctx, c.channel, "", renderSlackDraft(d))
	if err != nil {
		return nil, fmt.Errorf("slack handoff: post draft: %w", err)
	}
	pend := c.inbox.register(c.channel, ts)
	c.log("handoff: draft %s posted to slack %s thread %s", d.ID, c.channel, ts)
	return &slackPresentation{c: c, threadTS: ts, pend: pend}, nil
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

// slackPresentation is one draft posted to a thread, awaiting a reply.
type slackPresentation struct {
	c        *SlackChannel
	threadTS string
	pend     *slackPending
}

func (p *slackPresentation) Ref() string {
	return "slack:" + p.c.channel + ":" + p.threadTS
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
	p.c.inbox.unregister(p.c.channel, p.threadTS)
}

// Inbox routes captured Slack thread replies to the Presentation waiting on that
// thread. The Slack integration calls Deliver when a message lands in a thread;
// if a hand-off is pending on that (channel, thread_ts) the reply is parsed into
// a Decision and the Await resolves. Deliver reports whether it consumed the
// message, so the integration can tell a hand-off reply from ordinary chatter.
type Inbox struct {
	mu      sync.Mutex
	pending map[string]*slackPending // key: channel + ":" + threadTS
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
