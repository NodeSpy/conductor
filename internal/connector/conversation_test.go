package connector

import (
	"context"
	"testing"
	"time"

	"github.com/NodeSpy/conductor/internal/plugin"
	sdk "github.com/NodeSpy/conductor/pkg/plugin"
)

// The generic conversation path (plugin-contract.md §2.2–§2.3): a plugin's
// ask verb declaring opens_conversation posts and returns the conversation's
// id; the engine waits in its one inbox; the plugin's conversation_reply
// event resolves the wait — by the declared approver only — and is consumed.
// A reply nobody awaits is an ordinary event.
func TestPluginConversationResolvesAnAsk(t *testing.T) {
	decl := mapDecl(&sdk.Decl{Type: "chat",
		Verbs: []sdk.Verb{{Name: "ask", Ask: true, Semantics: &sdk.VerbSemantics{
			OpensConversation: &sdk.OpensConversation{ID: "conversation_id", Approvers: "approvers"}}}},
		Events: []sdk.Event{{Name: "reply", Semantics: &sdk.EventSemantics{
			ConversationReply: &sdk.ConversationReply{ID: "{{.chat.room}}:{{.chat.thread}}", Author: "chat.user", Text: "chat.text"}}}},
	})
	inv := &postedInvoker{posted: make(chan string, 1), out: map[string]any{"conversation_id": "R1:t9", "ref": "chat://R1/t9"}}
	e := &externalImpl{client: inv, decl: decl, instance: "chat1", log: t.Logf}

	got := make(chan map[string]any, 1)
	go func() {
		out, err := e.Invoke(context.Background(), "ask", map[string]any{"prompt": "ship it?", "approvers": []any{"U-lead"}, "timeout": "5s"})
		if err != nil {
			t.Error(err)
		}
		got <- out
	}()
	reply := func(user, text string) sdk.SourceEvent {
		return sdk.SourceEvent{Event: "reply", Context: map[string]any{"chat": map[string]any{"room": "R1", "thread": "t9", "user": user, "text": text}}}
	}
	psi := &pluginSourceIntegration{instance: "chat1", typ: "chat", log: t.Logf,
		declared: map[string]bool{"reply": true}, sem: map[string]*sdk.EventSemantics{"reply": decl.Events[0].Semantics}}
	// Wait until the ask has posted (the plugin was called).
	select {
	case <-inv.posted:
	case <-time.After(5 * time.Second):
		t.Fatal("the ask never posted")
	}
	time.Sleep(20 * time.Millisecond)                     // the conversation registers after the post returns
	psi.triggersFor(reply("U-someone", "approve"), false) // not an approver: not consumed
	select {
	case out := <-got:
		t.Fatalf("a non-approver resolved the ask: %v", out)
	case <-time.After(50 * time.Millisecond):
	}
	if trs := psi.triggersFor(reply("U-lead", "lgtm"), false); len(trs) != 0 {
		t.Fatalf("a consumed reply must fire no trigger: %+v", trs)
	}
	select {
	case out := <-got:
		if out["action"] != "approve" || out["ref"] != "chat://R1/t9" {
			t.Fatalf("ask outputs = %v", out)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the reply did not resolve the ask")
	}
}

// postedInvoker answers every call with out and reports the first one.
type postedInvoker struct {
	posted chan string
	out    map[string]any
}

func (p *postedInvoker) Invoke(_ context.Context, req plugin.InvokeRequest) (map[string]any, error) {
	select {
	case p.posted <- req.Verb:
	default:
	}
	return p.out, nil
}
