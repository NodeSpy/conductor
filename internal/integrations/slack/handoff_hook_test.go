package slack

import (
	"context"
	"encoding/json"
	"testing"
)

// TestReplyHookCapturesThreadReply proves a thread reply is routed to the
// hand-off reply hook (reply capture) and, when consumed, does NOT fall through to
// normal rule matching. The bot's own posts and non-threaded messages are ignored.
func TestReplyHookCapturesThreadReply(t *testing.T) {
	g := newTest(t, baseCfg())
	emit, got := collect()

	type call struct{ channel, threadTS, user, text string }
	var calls []call
	consume := true
	SetReplyHook(func(channel, threadTS, user, text string) bool {
		calls = append(calls, call{channel, threadTS, user, text})
		return consume
	})
	defer SetReplyHook(nil)

	msg := func(fields map[string]any) json.RawMessage {
		b, _ := json.Marshal(map[string]any{"event": fields})
		return b
	}

	// A human thread reply → routed to the hook and consumed (no trigger emitted).
	g.handleEvent(context.Background(), emit, msg(map[string]any{
		"type": "message", "channel": "C1", "user": "U1", "text": "approve", "thread_ts": "T1",
	}))
	if len(calls) != 1 || calls[0].threadTS != "T1" || calls[0].text != "approve" {
		t.Fatalf("thread reply not routed to hook: %+v", calls)
	}
	if len(*got) != 0 {
		t.Fatalf("a consumed reply must not emit a trigger, got %d", len(*got))
	}

	// The bot's own post (bot_id set) is skipped.
	calls = nil
	g.handleEvent(context.Background(), emit, msg(map[string]any{
		"type": "message", "channel": "C1", "user": "U1", "text": "x", "thread_ts": "T1", "bot_id": "B1",
	}))
	if len(calls) != 0 {
		t.Fatalf("bot posts must not reach the hook: %+v", calls)
	}

	// A top-level (non-threaded) message is skipped too.
	g.handleEvent(context.Background(), emit, msg(map[string]any{
		"type": "message", "channel": "C1", "user": "U1", "text": "hi",
	}))
	if len(calls) != 0 {
		t.Fatalf("non-threaded messages must not reach the hook: %+v", calls)
	}
}
