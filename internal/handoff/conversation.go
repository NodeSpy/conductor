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
	p                *slackPending
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
