package connector

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/NodeSpy/conductor/internal/handoff"
)

// fakeDiscordPoster is a hermetic discordPoster fake: it records calls and
// returns canned ids, avoiding any network/env dependency.
type fakeDiscordPoster struct {
	mu       sync.Mutex
	posts    []struct{ channel, threadTS, text string }
	dmOpens  []string
	postID   string
	dmResult string
	postErr  error
	dmErr    error
}

func (f *fakeDiscordPoster) Post(ctx context.Context, channel, threadTS, text string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.posts = append(f.posts, struct{ channel, threadTS, text string }{channel, threadTS, text})
	if f.postErr != nil {
		return "", f.postErr
	}
	id := f.postID
	if id == "" {
		id = "msg-1"
	}
	return id, nil
}

func (f *fakeDiscordPoster) OpenDM(ctx context.Context, user string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.dmOpens = append(f.dmOpens, user)
	if f.dmErr != nil {
		return "", f.dmErr
	}
	dm := f.dmResult
	if dm == "" {
		dm = "dm-" + user
	}
	return dm, nil
}

func newDiscordTestImpl(poster discordPoster) *discordImpl {
	return &discordImpl{
		name:   "disc",
		conn:   discordConn{BotToken: "tok"},
		deps:   Deps{},
		poster: poster,
		inbox:  handoff.NewInbox(),
	}
}

func TestDiscordVerbPostChannel(t *testing.T) {
	fp := &fakeDiscordPoster{postID: "999"}
	impl := newDiscordTestImpl(fp)
	out, err := impl.Invoke(context.Background(), "post", map[string]any{"channel": "C1", "text": "hi"})
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	if out["id"] != "999" || out["channel"] != "C1" {
		t.Fatalf("out = %v", out)
	}
	if len(fp.posts) != 1 || fp.posts[0].channel != "C1" || fp.posts[0].text != "hi" {
		t.Fatalf("posts = %+v", fp.posts)
	}
	if len(fp.dmOpens) != 0 {
		t.Fatalf("should not have opened a dm: %v", fp.dmOpens)
	}
}

func TestDiscordVerbPostDM(t *testing.T) {
	fp := &fakeDiscordPoster{dmResult: "D1", postID: "1"}
	impl := newDiscordTestImpl(fp)
	out, err := impl.Invoke(context.Background(), "post", map[string]any{"user": "U1", "text": "hi"})
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	if out["channel"] != "D1" {
		t.Fatalf("out = %v", out)
	}
	if len(fp.dmOpens) != 1 || fp.dmOpens[0] != "U1" {
		t.Fatalf("dmOpens = %v", fp.dmOpens)
	}
	if len(fp.posts) != 1 || fp.posts[0].channel != "D1" {
		t.Fatalf("posts = %+v", fp.posts)
	}
}

func TestDiscordVerbPostRequiresChannelOrUser(t *testing.T) {
	impl := newDiscordTestImpl(&fakeDiscordPoster{})
	_, err := impl.Invoke(context.Background(), "post", map[string]any{"text": "hi"})
	if err == nil || !strings.Contains(err.Error(), "set options.channel or options.user") {
		t.Fatalf("got %v", err)
	}
}

func TestDiscordVerbPostRequiresText(t *testing.T) {
	impl := newDiscordTestImpl(&fakeDiscordPoster{})
	_, err := impl.Invoke(context.Background(), "post", map[string]any{"channel": "C1"})
	if err == nil || !strings.Contains(err.Error(), "options.text is required") {
		t.Fatalf("got %v", err)
	}
}

func TestDiscordVerbPostOpenDMError(t *testing.T) {
	fp := &fakeDiscordPoster{dmErr: fmt.Errorf("boom")}
	impl := newDiscordTestImpl(fp)
	_, err := impl.Invoke(context.Background(), "post", map[string]any{"user": "U1", "text": "hi"})
	if err == nil || !strings.Contains(err.Error(), "open dm") {
		t.Fatalf("got %v", err)
	}
}

func TestDiscordAskThread(t *testing.T) {
	fp := &fakeDiscordPoster{postID: "1"}
	impl := newDiscordTestImpl(fp)

	type result struct {
		out map[string]any
		err error
	}
	done := make(chan result, 1)
	go func() {
		out, err := impl.Invoke(context.Background(), "ask", map[string]any{
			"to": "thread", "channel": "C1", "prompt": "ok?", "timeout": "5s",
		})
		done <- result{out, err}
	}()

	// discord's thread mode registers with an empty second key component.
	deadline := time.Now().Add(2 * time.Second)
	for {
		if impl.inbox.Deliver("C1", "", "approve") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for the ask to register on the inbox")
		}
		time.Sleep(5 * time.Millisecond)
	}

	r := <-done
	if r.err != nil {
		t.Fatalf("ask: %v", r.err)
	}
	if r.out["action"] != "approve" {
		t.Fatalf("out = %v", r.out)
	}
}

func TestDiscordAskDM(t *testing.T) {
	fp := &fakeDiscordPoster{dmResult: "D1", postID: "1"}
	impl := newDiscordTestImpl(fp)

	type result struct {
		out map[string]any
		err error
	}
	done := make(chan result, 1)
	go func() {
		out, err := impl.Invoke(context.Background(), "ask", map[string]any{
			"to": "dm", "user": "U1", "prompt": "ok?", "timeout": "5s",
		})
		done <- result{out, err}
	}()

	// discord's dm mode also registers with an empty second key component.
	deadline := time.Now().Add(2 * time.Second)
	for {
		if impl.inbox.Deliver("D1", "", "approve") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for the ask to register on the inbox")
		}
		time.Sleep(5 * time.Millisecond)
	}

	r := <-done
	if r.err != nil {
		t.Fatalf("ask: %v", r.err)
	}
	if r.out["action"] != "approve" {
		t.Fatalf("out = %v", r.out)
	}
}

func TestDiscordAskMissingToErrors(t *testing.T) {
	impl := newDiscordTestImpl(&fakeDiscordPoster{})
	_, err := impl.Invoke(context.Background(), "ask", map[string]any{"prompt": "ok?"})
	if err == nil || !strings.Contains(err.Error(), "must be dm|thread") {
		t.Fatalf("got %v", err)
	}
}

func TestDiscordSourceNoTriggersReturnsNil(t *testing.T) {
	impl := newDiscordTestImpl(&fakeDiscordPoster{})
	result, err := impl.Source(nil)
	if err != nil || result != nil {
		t.Fatalf("Source(nil) = %v, %v; want nil, nil", result, err)
	}
}

func TestDiscordSourceWithTriggersErrors(t *testing.T) {
	impl := newDiscordTestImpl(&fakeDiscordPoster{})
	trig := CompiledTrigger{Index: 0, Spec: mkTriggerSpec("discord.whatever", "", nil)}
	_, err := impl.Source([]CompiledTrigger{trig})
	if err == nil || !strings.Contains(err.Error(), "has no source events") {
		t.Fatalf("got %v", err)
	}
}

func TestDiscordDeclaredEventsEmpty(t *testing.T) {
	impl := newDiscordTestImpl(&fakeDiscordPoster{})
	if events := impl.DeclaredEvents(); events != nil {
		t.Fatalf("DeclaredEvents() = %v, want nil", events)
	}
}
