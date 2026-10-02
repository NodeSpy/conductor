package handoff

import (
	"context"
	"testing"
)

// stubTool writes an executable named name into a dir prepended to PATH.
func TestInboxUnregisterAndPresentationClose(t *testing.T) {
	inbox := NewInbox()
	inbox.register("C1", "t1", nil)
	p := &slackPresentation{c: &SlackChannel{inbox: inbox}, channel: "C1", threadTS: "t1"}
	p.Close()
	if inbox.Deliver("C1", "t1", "approve") {
		t.Fatal("closed presentation must not receive deliveries")
	}
	inbox.register("D1", "", nil)
	dp := &discordPresentation{c: &DiscordChannel{inbox: inbox}, channel: "D1"}
	dp.Close()
	if inbox.Deliver("D1", "", "approve") {
		t.Fatal("closed discord presentation must not receive deliveries")
	}
}

func TestAwaitCancelled(t *testing.T) {
	inbox := NewInbox()
	sp := &slackPresentation{c: &SlackChannel{inbox: inbox}, channel: "C9", threadTS: "t", pend: inbox.register("C9", "t", nil)}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := sp.Await(ctx); err == nil {
		t.Fatal("cancelled slack await must error")
	}
	dp := &discordPresentation{c: &DiscordChannel{inbox: inbox}, channel: "D9", pend: inbox.register("D9", "", nil)}
	if _, err := dp.Await(ctx); err == nil {
		t.Fatal("cancelled discord await must error")
	}
}
