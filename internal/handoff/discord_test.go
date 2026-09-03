package handoff

import (
	"context"
	"sync"
	"testing"
	"time"
)

// fakeDiscordPoster records posts and hands out an incrementing message id,
// mirroring fakePoster (slack_test.go).
type fakeDiscordPoster struct {
	mu    sync.Mutex
	posts []string
	n     int
}

func (p *fakeDiscordPoster) Post(_ context.Context, channel, threadTS, text string) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.n++
	p.posts = append(p.posts, channel+"|"+threadTS+"|"+text)
	return "msg-1", nil
}

// fakeDiscordOpener resolves any user to a fixed DM channel, counting calls so
// a test can confirm DiscordChannel caches the result instead of re-opening
// per draft. Mirrors fakeOpener (slack_test.go).
type fakeDiscordOpener struct {
	mu      sync.Mutex
	channel string
	calls   int
	err     error
}

func (o *fakeDiscordOpener) OpenDM(_ context.Context, user string) (string, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.calls++
	if o.err != nil {
		return "", o.err
	}
	return o.channel, nil
}

func TestDiscordChannelReplyCapture(t *testing.T) {
	inbox := NewInbox()
	poster := &fakeDiscordPoster{}
	c := NewDiscordChannel(poster, "C123", inbox, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	pres, err := c.Present(ctx, Draft{Title: "Review", Body: "draft body"})
	if err != nil {
		t.Fatal(err)
	}
	if pres.Ref() != "discord:C123:thread" {
		t.Fatalf("unexpected ref %q", pres.Ref())
	}
	if len(poster.posts) != 1 {
		t.Fatalf("Present should post exactly once, got %d", len(poster.posts))
	}

	// A reply on an unrelated channel is not consumed.
	if inbox.Deliver("C_other", "", "approve") {
		t.Fatal("a reply on a different channel must not resolve this hand-off")
	}

	done := make(chan Decision, 1)
	go func() { d, _ := pres.Await(ctx); done <- d }()
	time.Sleep(10 * time.Millisecond)

	if !inbox.Deliver("C123", "", "please rework the error handling") {
		t.Fatal("a channel reply should be consumed by the pending hand-off")
	}
	select {
	case d := <-done:
		if d.Action != ActionRevise || d.Text != "please rework the error handling" {
			t.Fatalf("reply should map to a revise decision, got %+v", d)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Await never resolved after reply")
	}

	// A second reply to the now-resolved channel is a no-op.
	if inbox.Deliver("C123", "", "approve") {
		t.Fatal("resolved hand-off should not consume further replies")
	}
}

func TestDiscordChannelDMReplyCapture(t *testing.T) {
	inbox := NewInbox()
	poster := &fakeDiscordPoster{}
	opener := &fakeDiscordOpener{channel: "D999"}
	c := NewDiscordDMChannel(poster, opener, "U123", inbox, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	pres, err := c.Present(ctx, Draft{Title: "Review", Body: "draft body"})
	if err != nil {
		t.Fatal(err)
	}
	if pres.Ref() != "discord:D999:dm" {
		t.Fatalf("unexpected ref %q", pres.Ref())
	}
	if len(poster.posts) != 1 || poster.posts[0] != "D999||**Review**\ndraft body\n\n_Reply here:_ `approve`, `discard`, or the revised text to send back to the agent." {
		t.Fatalf("Present should post once to the DM channel, got %v", poster.posts)
	}

	done := make(chan Decision, 1)
	go func() { d, _ := pres.Await(ctx); done <- d }()
	time.Sleep(10 * time.Millisecond)

	if !inbox.Deliver("D999", "", "approve") {
		t.Fatal("a DM reply (matching DM channel) should be consumed by the pending hand-off")
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

// TestDiscordChannelDMOpensOnce confirms POST /users/@me/channels is called at
// most once even across multiple Present calls.
func TestDiscordChannelDMOpensOnce(t *testing.T) {
	inbox := NewInbox()
	poster := &fakeDiscordPoster{}
	opener := &fakeDiscordOpener{channel: "D999"}
	c := NewDiscordDMChannel(poster, opener, "U123", inbox, nil)
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		if _, err := c.Present(ctx, Draft{Title: "t"}); err != nil {
			t.Fatal(err)
		}
	}
	if opener.calls != 1 {
		t.Fatalf("expected the DM open to be called once, got %d", opener.calls)
	}
}

func TestDiscordChannelDMOpenerErrorSurfaces(t *testing.T) {
	inbox := NewInbox()
	poster := &fakeDiscordPoster{}
	opener := &fakeDiscordOpener{err: errDMFailed}
	c := NewDiscordDMChannel(poster, opener, "U123", inbox, nil)

	_, err := c.Present(context.Background(), Draft{Title: "t"})
	if err == nil {
		t.Fatal("a DM-open failure should surface from Present")
	}
}
