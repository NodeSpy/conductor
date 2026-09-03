package handoff

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

var errDMFailed = errors.New("open dm failed")

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

// fakeOpener resolves any user to a fixed IM channel, counting calls so a test
// can confirm SlackChannel caches the result instead of re-opening per draft.
type fakeOpener struct {
	mu      sync.Mutex
	channel string
	calls   int
	err     error
}

func (o *fakeOpener) OpenDM(_ context.Context, user string) (string, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.calls++
	if o.err != nil {
		return "", o.err
	}
	return o.channel, nil
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

func TestSlackChannelDMReplyCapture(t *testing.T) {
	inbox := NewInbox()
	poster := &fakePoster{}
	opener := &fakeOpener{channel: "D999"}
	c := NewSlackDMChannel(poster, opener, "U123", inbox, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	pres, err := c.Present(ctx, Draft{Title: "Review", Body: "draft body"})
	if err != nil {
		t.Fatal(err)
	}
	if pres.Ref() != "slack:D999:dm" {
		t.Fatalf("unexpected ref %q", pres.Ref())
	}
	if len(poster.posts) != 1 || poster.posts[0] != "D999||*Review*\ndraft body\n\n_Reply in this thread:_ `approve`, `discard`, or the revised text to send back to the agent." {
		t.Fatalf("Present should post once to the IM channel with no thread_ts, got %v", poster.posts)
	}

	// A reply on an unrelated channel is not consumed.
	if inbox.Deliver("C_other", "", "approve") {
		t.Fatal("a reply on a different channel must not resolve this hand-off")
	}
	// A threaded reply (non-empty threadTS) on the SAME channel id is a distinct
	// key from the DM's empty-threadTS registration and must not match either.
	if inbox.Deliver("D999", "111.1", "approve") {
		t.Fatal("a threaded reply must not resolve a dm hand-off registered with an empty threadTS")
	}

	done := make(chan Decision, 1)
	go func() { d, _ := pres.Await(ctx); done <- d }()
	time.Sleep(10 * time.Millisecond)

	if !inbox.Deliver("D999", "", "approve") {
		t.Fatal("a DM reply (empty threadTS, matching IM channel) should be consumed by the pending hand-off")
	}
	select {
	case d := <-done:
		if d.Action != ActionApprove {
			t.Fatalf("reply should map to an approve decision, got %+v", d)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Await never resolved after DM reply")
	}
}

// TestSlackChannelDMOpensOnce confirms conversations.open is called at most
// once even across multiple Present calls (Slack's endpoint is idempotent, but
// there's no reason to spend an extra API call per draft).
func TestSlackChannelDMOpensOnce(t *testing.T) {
	inbox := NewInbox()
	poster := &fakePoster{}
	opener := &fakeOpener{channel: "D999"}
	c := NewSlackDMChannel(poster, opener, "U123", inbox, nil)
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		if _, err := c.Present(ctx, Draft{Title: "t"}); err != nil {
			t.Fatal(err)
		}
	}
	if opener.calls != 1 {
		t.Fatalf("expected conversations.open to be called once, got %d", opener.calls)
	}
}

func TestSlackChannelDMOpenerErrorSurfaces(t *testing.T) {
	inbox := NewInbox()
	poster := &fakePoster{}
	opener := &fakeOpener{err: errDMFailed}
	c := NewSlackDMChannel(poster, opener, "U123", inbox, nil)

	_, err := c.Present(context.Background(), Draft{Title: "t"})
	if err == nil {
		t.Fatal("a conversations.open failure should surface from Present")
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
