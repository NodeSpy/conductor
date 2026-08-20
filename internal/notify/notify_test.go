package notify

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/NodeSpy/paseo-conductor/internal/config"
	"github.com/NodeSpy/paseo-conductor/internal/core"
)

// captureNotifier returns a notifier whose log lines are collected.
func captureNotifier(on []string) (*Notifier, *[]string) {
	var lines []string
	n := New(config.Notify{On: on}, func(f string, a ...any) {
		lines = append(lines, fmt.Sprintf(f, a...))
	})
	return n, &lines
}

func TestEmitLogsNeverComments(t *testing.T) {
	n, lines := captureNotifier([]string{"escalate", "needs_input"})
	tr := core.Trigger{Kind: "review_requested", Target: core.Target{Repo: "acme/w", PR: 3, Number: 3}}

	// A non-listed event is silent.
	n.Emit(context.Background(), EventDispatch, tr, "x")
	if len(*lines) != 0 {
		t.Fatalf("dispatch not in policy; should be silent, got %v", *lines)
	}

	// Attention events log a private, actionable line (no PR comment path exists).
	n.Emit(context.Background(), EventNeedsInput, tr, "agent live")
	n.Emit(context.Background(), EventEscalate, tr, "cap reached")
	if len(*lines) != 2 {
		t.Fatalf("want 2 log lines, got %d: %v", len(*lines), *lines)
	}
	for _, l := range *lines {
		if !strings.Contains(l, "acme/w#3") || !strings.Contains(l, "open paseo") {
			t.Fatalf("log line missing ref/hint: %q", l)
		}
	}
}

func TestNotifyPolicyGate(t *testing.T) {
	n := config.Notify{On: []string{"escalate", "dispatch"}}
	if !n.Wants("dispatch") || !n.Wants("escalate") {
		t.Fatal("listed events should be wanted")
	}
	if n.Wants("complete") {
		t.Fatal("unlisted event should not be wanted")
	}
}
