package notify

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/NodeSpy/conductor/internal/core"
	"github.com/NodeSpy/conductor/internal/secrets"
)

// captureNotifier returns a notifier whose log lines are collected.
func captureNotifier() (*Notifier, *[]string) {
	var lines []string
	n := New(func(f string, a ...any) {
		lines = append(lines, fmt.Sprintf(f, a...))
	}, nil)
	return n, &lines
}

func TestNewNilLog(t *testing.T) {
	n := New(nil, nil)
	// The no-op default logger must be callable.
	n.Emit(context.Background(), EventComplete, core.Trigger{}, "x")
}

func TestEmitLogsEveryEvent(t *testing.T) {
	n, lines := captureNotifier()
	tr := core.Trigger{Kind: "review_requested", Target: core.Target{Repo: "acme/w", PR: 3, Number: 3}}

	// Every event is journaled now — alerting lives entirely in conductor.*
	// triggers, not a config-level on: allowlist.
	n.Emit(context.Background(), EventDispatch, tr, "x")
	n.Emit(context.Background(), EventNeedsInput, tr, "agent live")
	n.Emit(context.Background(), EventEscalate, tr, "cap reached")
	if len(*lines) != 3 {
		t.Fatalf("want 3 log lines, got %d: %v", len(*lines), *lines)
	}
	for _, l := range (*lines)[1:] {
		if !strings.Contains(l, "acme/w#3") || !strings.Contains(l, "open paseo") {
			t.Fatalf("log line missing ref/hint: %q", l)
		}
	}
}

func TestEmitPublishesLifecycleForEveryEvent(t *testing.T) {
	n, _ := captureNotifier()
	var published []string
	n.SetPublisher(func(_ context.Context, event string, _ core.Trigger, _ string, _ map[string]any) {
		published = append(published, event)
	})
	tr := core.Trigger{Kind: "merge_conflict", Target: core.Target{Repo: "acme/w", Number: 7}}
	n.Emit(context.Background(), EventComplete, tr, "done")
	n.Emit(context.Background(), EventEscalate, tr, "gave up")
	if len(published) != 2 || published[0] != EventComplete || published[1] != EventEscalate {
		t.Fatalf("every event should feed the lifecycle publisher, got %v", published)
	}
}

func TestEmitAuditsAttentionEventsOnly(t *testing.T) {
	n, _ := captureNotifier()
	var audited []map[string]any
	n2 := New(n.log, func(e map[string]any) { audited = append(audited, e) })
	tr := core.Trigger{Kind: "merge_conflict", Target: core.Target{Repo: "acme/w", Number: 9}}
	n2.Emit(context.Background(), EventDispatch, tr, "x") // not audited
	n2.Emit(context.Background(), EventEscalate, tr, "gave up")
	if len(audited) != 1 || audited[0]["event"] != EventEscalate {
		t.Fatalf("want exactly one audited (escalate) event, got %+v", audited)
	}
}

// REGRESSION: the notifier had no secrets resolver — a tracked secret in a
// notify message rode the audit, the journal, AND the lifecycle publish
// verbatim. Everything is redacted ONCE before any fan-out now.
func TestNotifyRedactsSecretsEverywhere(t *testing.T) {
	const secret = "notify-s3cr3t-XYZZY"
	res := secrets.New()
	res.Track(secret)
	var audits []map[string]any
	var logs []string
	var published []string
	n := New(func(f string, a ...any) { logs = append(logs, fmt.Sprintf(f, a...)) },
		func(e map[string]any) { audits = append(audits, e) })
	n.SetSecrets(res)
	n.SetPublisher(func(_ context.Context, _ string, _ core.Trigger, line string, extra map[string]any) {
		published = append(published, line, fmt.Sprint(extra))
	})

	tr := core.Trigger{Kind: "merge_conflict", Target: core.Target{Repo: "acme/w", Number: 7},
		Title: "title with " + secret}
	n.Emit(context.Background(), EventEscalate, tr, "error: token "+secret+" rejected")

	for _, l := range published {
		if strings.Contains(l, secret) {
			t.Fatalf("secret reached the lifecycle publisher: %s", l)
		}
	}
	for _, e := range audits {
		if strings.Contains(fmt.Sprint(e), secret) {
			t.Fatalf("secret reached the audit: %v", e)
		}
	}
	for _, l := range logs {
		if strings.Contains(l, secret) {
			t.Fatalf("secret reached the journal: %s", l)
		}
	}

	// Publish redacts the same way.
	n.Publish(context.Background(), EventUpdated, tr, "now on "+secret, map[string]any{"note": secret})
	for _, e := range audits {
		if strings.Contains(fmt.Sprint(e), secret) {
			t.Fatalf("secret reached the audit via Publish: %v", e)
		}
	}
}
