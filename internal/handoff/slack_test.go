package handoff

import (
	"context"
	"sync"
	"testing"
	"time"
)

// fakePoster records posts and hands out incrementing ts values so the test can
// address the thread it needs to reply into.
type fakePoster struct {
	mu    sync.Mutex
	posts []string
	n     int
}

func (p *fakePoster) Post(_ context.Context, channel, threadTS, text string) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.n++
	p.posts = append(p.posts, channel+"|"+threadTS+"|"+text)
	return "ts-1", nil
}

func TestSlackChannelReplyCapture(t *testing.T) {
	inbox := NewInbox()
	poster := &fakePoster{}
	c := NewSlackChannel(poster, "C123", inbox, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	pres, err := c.Present(ctx, Draft{Title: "Review", Body: "draft body"})
	if err != nil {
		t.Fatal(err)
	}
	if pres.Ref() != "slack:C123:ts-1" {
		t.Fatalf("unexpected ref %q", pres.Ref())
	}
	if len(poster.posts) != 1 {
		t.Fatalf("Present should post exactly once, got %d", len(poster.posts))
	}

	// A reply to an unrelated thread is not consumed.
	if inbox.Deliver("C123", "other-ts", "approve") {
		t.Fatal("reply to a different thread must not resolve this hand-off")
	}

	done := make(chan Decision, 1)
	go func() { d, _ := pres.Await(ctx); done <- d }()
	time.Sleep(10 * time.Millisecond)

	if !inbox.Deliver("C123", "ts-1", "please rework the error handling") {
		t.Fatal("a thread reply should be consumed by the pending hand-off")
	}
	select {
	case d := <-done:
		if d.Action != ActionRevise || d.Text != "please rework the error handling" {
			t.Fatalf("reply should map to a revise decision, got %+v", d)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Await never resolved after reply")
	}

	// A second reply to the now-resolved thread is a no-op.
	if inbox.Deliver("C123", "ts-1", "approve") {
		t.Fatal("resolved hand-off should not consume further replies")
	}
}

func TestParseReply(t *testing.T) {
	cases := map[string]Decision{
		"approve":             {Action: ActionApprove},
		"LGTM":                {Action: ActionApprove},
		"discard":             {Action: ActionDiscard},
		"cancel":              {Action: ActionDiscard},
		"revise: do it again": {Action: ActionRevise, Text: "do it again"},
		"just change this":    {Action: ActionRevise, Text: "just change this"},
	}
	for in, want := range cases {
		got := parseReply(in)
		if got.Action != want.Action || got.Text != want.Text {
			t.Errorf("parseReply(%q) = %+v, want %+v", in, got, want)
		}
	}
}
