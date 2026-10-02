package handoff

import (
	"context"
	"testing"
	"time"
)

// A closed conversation stops receiving replies, and a cancelled wait errors.
func TestConversationCloseAndCancel(t *testing.T) {
	inbox := NewInbox()
	p := inbox.OpenConversation("chat", "C1:t1", "C1/t1", nil)
	p.Close()
	if inbox.DeliverReply("chat", "C1:t1", "U1", "approve") {
		t.Fatal("a closed conversation must not receive replies")
	}
	q := inbox.OpenConversation("chat", "C9:t", "", nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := q.Await(ctx); err == nil {
		t.Fatal("a cancelled await must error")
	}
}

// Replies resolve only their own (instance, id), only for an approver when
// the conversation names some, and only once.
func TestConversationReplyRouting(t *testing.T) {
	inbox := NewInbox()
	p := inbox.OpenConversation("chat", "C1:t1", "ref", []string{"U1"})
	defer p.Close()
	if inbox.DeliverReply("other", "C1:t1", "U1", "approve") {
		t.Fatal("a reply for another instance resolved this conversation")
	}
	if inbox.DeliverReply("chat", "C1:t1", "U2", "approve") {
		t.Fatal("a non-approver resolved the conversation")
	}
	if inbox.DeliverReply("chat", "C1:t1", "", "approve") {
		t.Fatal("an unknown author resolved a conversation with approvers")
	}
	if !inbox.DeliverReply("chat", "C1:t1", "U1", "revise: tighten it") {
		t.Fatal("the approver's reply was not delivered")
	}
	if inbox.DeliverReply("chat", "C1:t1", "U1", "approve") {
		t.Fatal("a second reply resolved it again")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	d, err := p.Await(ctx)
	if err != nil || d.Action != ActionRevise {
		t.Fatalf("decision = %+v, %v", d, err)
	}
}
