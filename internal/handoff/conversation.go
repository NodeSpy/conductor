package handoff

import (
	"context"
	"sync"
)

// Conversations is the engine's one inbox for every plugin's conversations
// (plugin-contract.md §2.2–§2.3): a verb declaring opens_conversation posts a
// question and the engine waits here under (instance, conversation id); an
// event declaring conversation_reply is delivered here before any trigger
// sees it. The connector's vendor (a chat service, a ticket thread) is the
// plugin's business; the waiting, the approvers rule and the reply parsing
// are the same for all of them.
var Conversations = NewInbox()

// conversationPresentation is one open conversation awaiting its reply.
type conversationPresentation struct {
	inbox            *Inbox
	instance, id, at string
	p                *pending
	once             sync.Once
}

// OpenConversation registers a pending conversation for instance/id; ref is
// where it was presented. approvers, when set, are the only authors whose
// reply resolves it.
func (i *Inbox) OpenConversation(instance, id, ref string, approvers []string) Presentation {
	return &conversationPresentation{inbox: i, instance: instance, id: id, at: ref,
		p: i.register(instance, id, approvers)}
}

func (c *conversationPresentation) Ref() string { return c.at }

func (c *conversationPresentation) Await(ctx context.Context) (Decision, error) {
	select {
	case d := <-c.p.done:
		return d, nil
	case <-ctx.Done():
		return Decision{}, ctx.Err()
	}
}

func (c *conversationPresentation) Close() {
	c.once.Do(func() { c.inbox.unregister(c.instance, c.id) })
}

// DeliverReply routes a conversation reply: true when it resolved a pending
// conversation (the event is consumed, not a fresh trigger).
func (i *Inbox) DeliverReply(instance, id, author, text string) bool {
	return i.DeliverFrom(instance, id, author, text)
}

// Inbox routes a conversation reply to the Presentation waiting on it, keyed
// by (instance, conversation id). A reply is parsed into a Decision; delivery
// reports whether it was consumed, so the caller can tell a reply from
// ordinary chatter. Safe for concurrent use.
type Inbox struct {
	mu      sync.Mutex
	pending map[string]*pending
}

type pending struct {
	done      chan Decision
	once      sync.Once
	approvers []string // empty = anyone may resolve
}

// NewInbox builds an empty Inbox.
func NewInbox() *Inbox { return &Inbox{pending: map[string]*pending{}} }

func inboxKey(instance, id string) string { return instance + ":" + id }

func (i *Inbox) register(instance, id string, approvers []string) *pending {
	p := &pending{done: make(chan Decision, 1), approvers: approvers}
	i.mu.Lock()
	i.pending[inboxKey(instance, id)] = p
	i.mu.Unlock()
	return p
}

func (i *Inbox) unregister(instance, id string) {
	i.mu.Lock()
	delete(i.pending, inboxKey(instance, id))
	i.mu.Unlock()
}

// DeliverFrom routes a reply to a pending conversation, if one is waiting on
// (instance, id) AND the author may resolve it (a conversation with an
// approvers list ignores every other author, an unknown one included).
func (i *Inbox) DeliverFrom(instance, id, author, text string) bool {
	i.mu.Lock()
	p := i.pending[inboxKey(instance, id)]
	i.mu.Unlock()
	if p == nil {
		return false
	}
	if len(p.approvers) > 0 {
		allowed := false
		for _, a := range p.approvers {
			if a != "" && a == author {
				allowed = true
				break
			}
		}
		if !allowed {
			return false
		}
	}
	delivered := false
	p.once.Do(func() {
		p.done <- parseReply(text)
		delivered = true
	})
	return delivered
}

// Waiting reports whether a conversation is open on (instance, id).
func (i *Inbox) Waiting(instance, id string) bool {
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.pending[inboxKey(instance, id)] != nil
}
