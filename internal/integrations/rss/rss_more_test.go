package rss

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/core"
)

func TestValidateRejections(t *testing.T) {
	act := config.ActionSet{{Type: "agent", Agent: "a"}}
	cases := []struct {
		name    string
		cfg     Config
		wantErr string
	}{
		{"no feeds", Config{}, "no feeds"},
		{"missing name", Config{Feeds: []Feed{{URL: "https://x", Actions: act}}}, "missing name"},
		{"duplicate name", Config{Feeds: []Feed{
			{Name: "a", URL: "https://x", Actions: act},
			{Name: "a", URL: "https://y", Actions: act},
		}}, `duplicate feed name "a"`},
		{"bad scheme", Config{Feeds: []Feed{{Name: "a", URL: "ftp://x", Actions: act}}}, "url must be http(s)"},
		{"bad regex", Config{Feeds: []Feed{{Name: "a", URL: "https://x", Match: "(", Actions: act}}}, "bad match regex"},
		{"no actions", Config{Feeds: []Feed{{Name: "a", URL: "https://x"}}}, "no actions"},
		{"untyped action", Config{Feeds: []Feed{{Name: "a", URL: "https://x", Actions: config.ActionSet{{}}}}}, "action.type is required"},
	}
	for _, c := range cases {
		g := newTest(t, c.cfg)
		err := g.Validate()
		if err == nil || !strings.Contains(err.Error(), c.wantErr) {
			t.Errorf("%s: err = %v, want %q", c.name, err, c.wantErr)
		}
	}

	// A valid config (including a FlowRef-only lowered action) passes.
	ok := Config{Feeds: []Feed{
		{Name: "a", URL: "https://x", Match: "deprecat", Actions: act},
		{Name: "b", URL: "http://y", Actions: config.ActionSet{{FlowRef: "0:news.b"}}},
	}}
	if err := newTest(t, ok).Validate(); err != nil {
		t.Fatalf("valid config rejected: %v", err)
	}
}

func TestNameIntervalAndActions(t *testing.T) {
	g := newTest(t, Config{Feeds: []Feed{
		{Name: "a", URL: "https://x", Actions: config.ActionSet{{Type: "agent", Agent: "x"}}},
		{Name: "b", URL: "https://y", Actions: config.ActionSet{{Type: "command", Command: []string{"c"}}}},
	}})
	if g.Name() != "test" {
		t.Fatalf("Name = %q", g.Name())
	}
	refs := g.Actions()
	if len(refs) != 2 || !strings.Contains(refs[0].Where, `feed "a"`) {
		t.Fatalf("Actions refs: %+v", refs)
	}
	if (Feed{}).interval() != defaultInterval {
		t.Fatal("default interval expected")
	}
	if got := (Feed{Interval: config.Duration(time.Minute)}).interval(); got != time.Minute {
		t.Fatalf("explicit interval = %v", got)
	}
}

func TestNewIntegrationDecodeError(t *testing.T) {
	_, err := newIntegration("bad", func(any) error { return errors.New("boom") })
	if err == nil || !strings.Contains(err.Error(), "rss[bad]: decode config: boom") {
		t.Fatalf("decode error: %v", err)
	}
}

// TestStartPollsAndEmits drives the real Start loop: the first poll seeds the
// backlog, the second (a new item appeared) emits, and cancel stops the loop.
func TestStartPollsAndEmits(t *testing.T) {
	var calls atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		items := `<item><title>old</title><guid>g1</guid></item>`
		if n > 1 {
			items += `<item><title>fresh item</title><guid>g2</guid><link>https://x/2</link></item>`
		}
		fmt.Fprintf(w, `<?xml version="1.0"?><rss version="2.0"><channel>%s</channel></rss>`, items)
	}))
	defer srv.Close()

	g := newTest(t, Config{Feeds: []Feed{{
		Name: "rel", URL: srv.URL, Interval: config.Duration(20 * time.Millisecond),
		Actions: config.ActionSet{{Type: "agent", Agent: "assess"}},
	}}})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	emitted := make(chan core.Trigger, 4)
	done := make(chan error, 1)
	go func() {
		done <- g.Start(ctx, func(_ context.Context, tr core.Trigger) { emitted <- tr })
	}()

	select {
	case tr := <-emitted:
		if tr.Title != "fresh item" || tr.Kind != "rel" || tr.Dedup != "rel\x00g2" {
			t.Fatalf("unexpected trigger: %+v", tr)
		}
		if tr.Target.Repo == "" { // synthetic target
			t.Fatal("synthetic target should still carry a repo key")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no trigger emitted from the poll loop")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Start should return ctx.Err(), got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Start did not stop on cancel")
	}
}

// TestPollFetchErrorLogsAndKeepsSeen: a failing fetch neither seeds nor emits.
func TestPollFetchErrorLogsAndKeepsSeen(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	g := newTest(t, Config{})
	emit, got := collect()
	seen := map[string]bool{}
	primed := false
	g.poll(context.Background(), emit, Feed{Name: "a", URL: srv.URL}, nil, seen, &primed)
	if primed || len(seen) != 0 || len(*got) != 0 {
		t.Fatalf("failed poll must not prime/seed/emit: primed=%v seen=%d got=%d", primed, len(seen), len(*got))
	}

	// A good poll primes (seeding silently).
	ok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(rssXML))
	}))
	defer ok.Close()
	g.poll(context.Background(), emit, Feed{Name: "a", URL: ok.URL}, nil, seen, &primed)
	if !primed || len(seen) != 2 || len(*got) != 0 {
		t.Fatalf("first good poll seeds silently: primed=%v seen=%d got=%d", primed, len(seen), len(*got))
	}
}

func TestEmitItemRepoAndDisabled(t *testing.T) {
	g := newTest(t, Config{})
	off := false
	f := Feed{Name: "rel", Repo: "acme/widget", Actions: config.ActionSet{
		{Type: "agent", Agent: "a"},
		{Type: "agent", Agent: "b", Enabled: &off},
	}}
	emit, got := collect()
	g.emitItem(context.Background(), emit, f, Item{Title: "T", Link: "https://x/1", ID: "g1"})
	if len(*got) != 1 {
		t.Fatalf("disabled action must not emit; got %d", len(*got))
	}
	tr := (*got)[0]
	if tr.Target.Repo != "acme/widget" || tr.Target.Owner != "acme" || tr.Target.Name != "widget" {
		t.Fatalf("repo target: %+v", tr.Target)
	}
	if tr.Target.HTMLURL != "https://x/1" || tr.Context["url"] != "https://x/1" {
		t.Fatalf("link plumbed: %+v", tr)
	}
	// A real-repo feed keeps the action's checkout (no ForceNoCheckout).
	if act, ok := tr.Action.(config.Action); !ok || act.Agent != "a" {
		t.Fatalf("action: %+v", tr.Action)
	}
}

func TestFetchErrors(t *testing.T) {
	g := newTest(t, Config{})
	if _, err := g.fetch(context.Background(), "http://127.0.0.1:1/nope"); err == nil {
		t.Fatal("unreachable host should error")
	}
	if _, err := g.fetch(context.Background(), "://bad"); err == nil {
		t.Fatal("bad URL should error")
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()
	if _, err := g.fetch(context.Background(), srv.URL); err == nil || !strings.Contains(err.Error(), "HTTP 404") {
		t.Fatalf("non-200 should error with the status, got %v", err)
	}
}

func TestItemDedupIDFallbacks(t *testing.T) {
	if got := (Item{ID: "id", Link: "l", Title: "t"}).dedupID(); got != "id" {
		t.Fatalf("id wins: %q", got)
	}
	if got := (Item{Link: "l", Title: "t"}).dedupID(); got != "l" {
		t.Fatalf("link next: %q", got)
	}
	if got := (Item{Title: "t"}).dedupID(); got != "t" {
		t.Fatalf("title last: %q", got)
	}
}

func TestAtomLinkRelPreference(t *testing.T) {
	// A rel=self link is skipped in favor of the alternate/unmarked one.
	doc := `<?xml version="1.0"?><feed xmlns="http://www.w3.org/2005/Atom">
	  <entry><title>v1</title>
	    <link href="https://x/self" rel="self"/>
	    <link href="https://x/alt"/>
	    <id>e1</id><content>full body</content><published>2026-01-01</published></entry>
	</feed>`
	items := parseFeed([]byte(doc))
	if len(items) != 1 || items[0].Link != "https://x/alt" {
		t.Fatalf("alternate link preferred: %+v", items)
	}
	// content fills summary when summary is empty; published preferred over updated.
	if items[0].Summary != "full body" || items[0].Published != "2026-01-01" {
		t.Fatalf("fallback fields: %+v", items[0])
	}
	// Only a self link → falls back to it (firstNonEmpty exercised the other way).
	onlySelf := `<?xml version="1.0"?><feed xmlns="http://www.w3.org/2005/Atom">
	  <entry><title>v2</title><link href="https://x/self" rel="self"/><id>e2</id></entry></feed>`
	items = parseFeed([]byte(onlySelf))
	if len(items) != 1 || items[0].Link != "https://x/self" {
		t.Fatalf("self-only fallback: %+v", items)
	}
}

func TestWrapHelpers(t *testing.T) {
	if got := wrap("n", "m", errors.New("e")).Error(); got != "rss[n]: m: e" {
		t.Fatalf("wrap with err: %q", got)
	}
	if got := wrap("n", "m", nil).Error(); got != "rss[n]: m" {
		t.Fatalf("wrap nil: %q", got)
	}
	if got := wrapf("n", "x %d", 7).Error(); got != "rss[n]: x 7" {
		t.Fatalf("wrapf: %q", got)
	}
}
