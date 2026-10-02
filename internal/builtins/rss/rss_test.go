package rss

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/NodeSpy/conductor/pkg/plugin"
	"github.com/NodeSpy/conductor/pkg/sourcekit"
)

func filterJSON(t *testing.T, v any) json.RawMessage {
	t.Helper()
	f, err := sourcekit.ParseFilter(v)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(f)
	return b
}

// The first poll seeds the backlog silently; a new item afterwards is emitted
// once per trigger whose `match:` it satisfies, routed to that trigger, on a
// synthetic target the source assigns (nothing to check out).
func TestRSSPollsAndRoutesByMatch(t *testing.T) {
	var mu sync.Mutex
	items := `<item><title>old</title><guid>1</guid></item>`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		_, _ = w.Write([]byte(`<rss><channel>` + items + `</channel></rss>`))
	}))
	defer srv.Close()
	r := New()
	r.Stagger = 0
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	got := make(chan plugin.SourceEvent, 8)
	req := plugin.StartSourceRequest{Instance: "news",
		Config: map[string]any{"feeds": map[string]any{"rel": map[string]any{"url": srv.URL, "interval": "50ms"}}},
		Triggers: []plugin.SourceTrigger{
			{ID: "0:news.rel", Event: "rel", Filter: filterJSON(t, map[string]any{"match": "security"})},
			{ID: "1:news.rel", Event: "rel"},
		}}
	go func() {
		_ = r.StartSource(ctx, req, func(p any) error { got <- p.(plugin.SourceEvent); return nil })
	}()
	time.Sleep(120 * time.Millisecond) // the seeding poll
	mu.Lock()
	items += `<item><title>Go released</title><description>runtime notes</description><guid>2</guid></item>`
	mu.Unlock()
	select {
	case ev := <-got:
		if ev.Trigger != "1:news.rel" || ev.Event != "rel" || ev.Target.Repo != "rss:rel" || !ev.Target.Assigned || ev.Title != "Go released" {
			t.Fatalf("event = %+v", ev)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the new item was not emitted")
	}
	select {
	case ev := <-got:
		t.Fatalf("an item that does not match was routed to the matching trigger: %+v", ev)
	case <-time.After(200 * time.Millisecond):
	}
}

func TestMatcher(t *testing.T) {
	facts := map[string]any{"item": map[string]any{"title": "Go 1.26 released", "summary": "toolchain and runtime updates"}}
	for pat, want := range map[string]bool{"go 1\\.26": true, "runtime": true, "security advisory": false} {
		if got, err := matcher("match", pat, facts); err != nil || got != want {
			t.Fatalf("%q: %v %v", pat, got, err)
		}
	}
	if _, err := matcher("match", "(", facts); err == nil {
		t.Fatal("a bad regex must error")
	}
	if p := plugin.ValidateSemantics((&RSS{}).Describe()); len(p) > 0 {
		t.Fatal(p)
	}
}
