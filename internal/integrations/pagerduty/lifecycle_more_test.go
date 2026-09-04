package pagerduty

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

func actSet() config.ActionSet { return config.ActionSet{{Type: "agent", Agent: "a"}} }

func TestValidateRejectionTable(t *testing.T) {
	cases := []struct {
		name    string
		cfg     Config
		wantErr string
	}{
		{"no transport", Config{Rules: []Rule{{Actions: actSet()}}}, "set `listen` and/or `smee_url`"},
		{"no rules", Config{Listen: ":0"}, "no rules"},
		{"no actions", Config{Listen: ":0", Rules: []Rule{{}}}, "no actions"},
		{"untyped action", Config{Listen: ":0", Rules: []Rule{{Actions: config.ActionSet{{}}}}}, "action.type is required"},
	}
	for _, c := range cases {
		if err := newTest(t, c.cfg).Validate(); err == nil || !strings.Contains(err.Error(), c.wantErr) {
			t.Errorf("%s: err = %v, want %q", c.name, err, c.wantErr)
		}
	}
	ok := Config{SmeeURL: "https://smee.io/x", Rules: []Rule{{Actions: config.ActionSet{{FlowRef: "0:x.y"}}}}}
	if err := newTest(t, ok).Validate(); err != nil {
		t.Fatalf("valid config rejected: %v", err)
	}
}

func TestNameAndActionsRefs(t *testing.T) {
	g := newTest(t, Config{Listen: ":0", Rules: []Rule{{Actions: actSet()}}})
	if g.Name() != "test" {
		t.Fatalf("Name = %q", g.Name())
	}
	refs := g.Actions()
	if len(refs) != 1 || !strings.Contains(refs[0].Where, "rules[0]") {
		t.Fatalf("Actions: %+v", refs)
	}
}

func TestHandlerMethodGate(t *testing.T) {
	g := newTest(t, Config{Listen: ":0", Rules: []Rule{{Actions: actSet()}}})
	emit := func(context.Context, core.Trigger) {}
	h := g.handler(context.Background(), emit)
	rr := httptest.NewRecorder()
	h(rr, httptest.NewRequest(http.MethodGet, "/x", nil))
	if rr.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET: %d", rr.Code)
	}
}

// TestStartSmeeBranch: the smee branch pumps a frame into deliver and cancel
// ends Start with ctx.Err().
func TestStartSmeeBranch(t *testing.T) {
	sse := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"body\":{\"event\":{\"event_type\":\"incident.triggered\",\"data\":{\"id\":\"P1\",\"title\":\"db down\",\"urgency\":\"high\",\"service\":{\"summary\":\"db\",\"id\":\"SVC1\"},\"html_url\":\"https://pd/1\"}}}}\n\n")
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		<-r.Context().Done()
	}))
	defer sse.Close()

	g := newTest(t, Config{SmeeURL: sse.URL, Rules: []Rule{{Actions: actSet()}}})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	emitted := make(chan core.Trigger, 4)
	done := make(chan error, 1)
	go func() {
		done <- g.Start(ctx, func(_ context.Context, tr core.Trigger) { emitted <- tr })
	}()
	select {
	case tr := <-emitted:
		if tr.Source != "pagerduty" {
			t.Fatalf("trigger: %+v", tr)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no trigger from the smee branch")
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
