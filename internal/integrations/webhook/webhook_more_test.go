package webhook

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/core"
)

func act() config.ActionSet { return config.ActionSet{{Type: "agent", Agent: "a"}} }

func TestValidateRejectionTable(t *testing.T) {
	cases := []struct {
		name    string
		cfg     Config
		wantErr string
	}{
		{"no transport", Config{Sources: []Source{{Name: "s", Actions: act()}}}, "set `listen` and/or `smee_url`"},
		{"no sources", Config{Listen: ":0"}, "no sources"},
		{"missing name", Config{Listen: ":0", Sources: []Source{{Path: "/x", Actions: act()}}}, "missing name"},
		{"duplicate name", Config{Listen: ":0", Sources: []Source{
			{Name: "s", Path: "/a", Actions: act()}, {Name: "s", Path: "/b", Actions: act()},
		}}, `duplicate source name "s"`},
		{"path required with listener", Config{Listen: ":0", Sources: []Source{{Name: "s", Actions: act()}}}, "`path` is required"},
		{"match required multi-smee", Config{SmeeURL: "https://smee.io/x", Sources: []Source{
			{Name: "a", Actions: act()}, {Name: "b", Actions: act()},
		}}, "`match` is required"},
		{"no actions", Config{Listen: ":0", Sources: []Source{{Name: "s", Path: "/x"}}}, "no actions"},
		{"untyped action", Config{Listen: ":0", Sources: []Source{{Name: "s", Path: "/x", Actions: config.ActionSet{{}}}}}, "action.type is required"},
		{"bad template", Config{Listen: ":0", Sources: []Source{{Name: "s", Path: "/x", Title: "{{.broken", Actions: act()}}}, "bad title template"},
	}
	for _, c := range cases {
		if err := newTest(t, c.cfg).Validate(); err == nil || !strings.Contains(err.Error(), c.wantErr) {
			t.Errorf("%s: err = %v, want %q", c.name, err, c.wantErr)
		}
	}
	ok := Config{SmeeURL: "https://smee.io/x", Sources: []Source{
		{Name: "only", Actions: config.ActionSet{{FlowRef: "0:hooks.only"}}}, // single smee source: no match needed
	}}
	if err := newTest(t, ok).Validate(); err != nil {
		t.Fatalf("valid config rejected: %v", err)
	}
}

func TestNameAndActions(t *testing.T) {
	g := newTest(t, Config{Listen: ":0", Sources: []Source{{Name: "cw", Path: "/x", Actions: act()}}})
	if g.Name() != "test" {
		t.Fatalf("Name = %q", g.Name())
	}
	refs := g.Actions()
	if len(refs) != 1 || !strings.Contains(refs[0].Where, `source "cw"`) {
		t.Fatalf("Actions: %+v", refs)
	}
}

func TestHandlerMethodAndAccept(t *testing.T) {
	g := newTest(t, Config{})
	emit, got := collect()
	h := g.handler(context.Background(), emit, Source{Name: "s", Title: "t", Actions: act()})

	rr := httptest.NewRecorder()
	h(rr, httptest.NewRequest(http.MethodGet, "/x", nil))
	if rr.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET: %d", rr.Code)
	}

	rr = httptest.NewRecorder()
	h(rr, httptest.NewRequest(http.MethodPost, "/x", strings.NewReader(`{"a":1}`)))
	if rr.Code != http.StatusAccepted || len(*got) != 1 {
		t.Fatalf("POST: %d emitted=%d", rr.Code, len(*got))
	}
}

// TestStartSmeeRoutesToSources: Start's smee branch pumps frames through
// per-source match routing, and cancel ends Start with ctx.Err().
func TestStartSmeeRoutesToSources(t *testing.T) {
	// A smee-shaped SSE server delivering one alarm frame, then holding open.
	sse := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"body\":{\"state\":\"ALARM\",\"name\":\"disk\"}}\n\n")
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		<-r.Context().Done()
	}))
	defer sse.Close()

	g := newTest(t, Config{SmeeURL: sse.URL, Sources: []Source{
		{Name: "alarms", Match: `{{if eq .body.state "ALARM"}}true{{end}}`,
			Title: "{{.body.name}}", Actions: act()},
		{Name: "other", Match: `{{if eq .body.state "OK"}}true{{end}}`, Actions: act()},
	}})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	emitted := make(chan core.Trigger, 4)
	done := make(chan error, 1)
	go func() {
		done <- g.Start(ctx, func(_ context.Context, tr core.Trigger) { emitted <- tr })
	}()
	select {
	case tr := <-emitted:
		if tr.Kind != "alarms" || tr.Title != "disk" {
			t.Fatalf("routed trigger: %+v", tr)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no trigger from the smee branch")
	}
	select {
	case tr := <-emitted:
		t.Fatalf("the non-matching source must not fire: %+v", tr)
	case <-time.After(100 * time.Millisecond):
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Start: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Start did not stop")
	}
}

// TestStartListenerBranch: the direct-listener branch registers every source
// and serves a POST end to end.
func TestStartListenerBranch(t *testing.T) {
	addr := "127.0.0.1:18742"
	g := newTest(t, Config{Listen: addr, Sources: []Source{
		{Name: "cw", Path: "/hooks/cw", Title: "{{.body.n}}", Actions: act()},
	}})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	emitted := make(chan core.Trigger, 2)
	go g.Start(ctx, func(_ context.Context, tr core.Trigger) { emitted <- tr })

	var resp *http.Response
	var err error
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		resp, err = http.Post("http://"+addr+"/hooks/cw", "application/json", strings.NewReader(`{"n":"hello"}`))
		if err == nil {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("listener never came up: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("status %d", resp.StatusCode)
	}
	select {
	case tr := <-emitted:
		if tr.Title != "hello" {
			t.Fatalf("trigger: %+v", tr)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no trigger from the listener")
	}
}

func TestParseBodyAndRenderFallbacks(t *testing.T) {
	if m, ok := parseBody([]byte(`{"a":1}`)).(map[string]any); !ok || m["a"] != float64(1) {
		t.Fatal("json body")
	}
	if m, ok := parseBody([]byte("plain")).(map[string]any); !ok || m["raw"] != "plain" {
		t.Fatal("raw fallback")
	}
	if render("", nil) != "" {
		t.Fatal("empty template")
	}
	if render("{{.broken", nil) != "" {
		t.Fatal("parse error renders empty")
	}
	if render("{{fail}}", nil) != "" {
		t.Fatal("exec error renders empty")
	}
	if got := render("v={{.x}}", map[string]any{"x": 7}); got != "v=7" {
		t.Fatalf("render: %q", got)
	}
}
