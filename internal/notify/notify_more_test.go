package notify

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/core"
)

func TestSetRouterDeliversViaRoutes(t *testing.T) {
	cfg := config.Notify{
		On:  []string{EventEscalate},
		Via: []config.NotifyRoute{{Uses: "slack-ops.post", Options: map[string]any{"text": "{{.message}}"}}},
	}
	var mu sync.Mutex
	var logs []string
	n := New(cfg, func(f string, a ...any) {
		mu.Lock()
		logs = append(logs, f)
		mu.Unlock()
	}, nil)

	// nil router: warn once, never panic.
	tr := core.Trigger{Kind: "merge_conflict", Target: core.Target{Repo: "a/w", Number: 3}}
	n.Emit(context.Background(), EventEscalate, tr, "gave up")
	n.Emit(context.Background(), EventEscalate, tr, "gave up again")
	mu.Lock()
	warns := 0
	for _, l := range logs {
		if strings.Contains(l, "no connectors are wired") {
			warns++
		}
	}
	mu.Unlock()
	if warns != 1 {
		t.Fatalf("nil-router warning should log once, got %d", warns)
	}

	// Wired router: the route and rendered data arrive.
	got := make(chan map[string]any, 2)
	n.SetRouter(func(_ context.Context, r config.NotifyRoute, data map[string]any) error {
		if r.Uses == "slack-ops.post" {
			got <- data
		}
		return nil
	})
	n.Emit(context.Background(), EventEscalate, tr, "boom")
	select {
	case data := <-got:
		msg, _ := data["message"].(string)
		if !strings.Contains(msg, "[escalate] a/w#3") || data["kind"] != "merge_conflict" {
			t.Fatalf("route data: %+v", data)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("route never delivered")
	}

	// A non-matching event never routes.
	n.Emit(context.Background(), EventDispatch, tr, "started")
	select {
	case data := <-got:
		t.Fatalf("dispatch should not route: %+v", data)
	case <-time.After(100 * time.Millisecond):
	}
}

func TestNewNilLog(t *testing.T) {
	n := New(config.Notify{}, nil, nil)
	// The no-op default logger must be callable.
	n.Emit(context.Background(), EventComplete, core.Trigger{}, "x")
}
