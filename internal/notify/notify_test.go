package notify

import (
	"context"
	"testing"

	"github.com/NodeSpy/paseo-conductor/internal/config"
	"github.com/NodeSpy/paseo-conductor/internal/core"
)

type postCall struct {
	sub, ref, body, token string
}

func TestEscalationPostsAsYou(t *testing.T) {
	var calls []postCall
	n := New(config.Notify{On: []string{"escalate"}, CommentOnEscalate: true},
		func() (string, error) { return "utok", nil }, nil)
	n.SetPoster(func(_ context.Context, sub, ref, body, token string) error {
		calls = append(calls, postCall{sub, ref, body, token})
		return nil
	})

	tr := core.Trigger{Kind: "merge_conflict", Target: core.Target{Repo: "acme/w", PR: 3, Number: 3}}

	// A non-listed event does nothing.
	n.Emit(context.Background(), EventDispatch, tr, "x")
	if len(calls) != 0 {
		t.Fatalf("dispatch not in policy; should not post, got %d", len(calls))
	}

	// Escalation posts a comment as you.
	n.Emit(context.Background(), EventEscalate, tr, "cap reached")
	if len(calls) != 1 {
		t.Fatalf("want 1 post, got %d", len(calls))
	}
	c := calls[0]
	if c.sub != "pr" || c.ref != "acme/w#3" || c.token != "utok" {
		t.Fatalf("unexpected post: %+v", c)
	}
}

func TestEscalationIssueSubject(t *testing.T) {
	var subs []string
	n := New(config.Notify{On: []string{"escalate"}, CommentOnEscalate: true},
		func() (string, error) { return "t", nil }, nil)
	n.SetPoster(func(_ context.Context, sub, _, _, _ string) error { subs = append(subs, sub); return nil })
	tr := core.Trigger{Kind: "issue_assigned", Target: core.Target{Repo: "a/w", Issue: 9, Number: 9}}
	n.Emit(context.Background(), EventEscalate, tr, "m")
	if len(subs) != 1 || subs[0] != "issue" {
		t.Fatalf("want issue subject, got %v", subs)
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
