package github

import (
	"context"
	"testing"

	"github.com/NodeSpy/paseo-conductor/internal/config"
)

// newTestIntegration builds an Integration from an in-memory Config.
func newTestIntegration(t *testing.T, cfg Config) *Integration {
	t.Helper()
	ig, err := newIntegration("test", func(v any) error {
		*(v.(*Config)) = cfg
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return ig.(*Integration)
}

func baseConfig() Config {
	return Config{
		App:     AppConfig{AppID: 1, PrivateKeyPath: "x", WebhookSecret: "s"},
		Webhook: WebhookConfig{SmeeURL: "https://smee.io/x"},
		Rules: []Rule{{
			Match:    Match{Repos: []string{"acme/*"}},
			Reviewer: config.Actors{Logins: []string{"me"}},
			Assignee: config.Actors{Logins: []string{"me"}},
			Actions: map[string]config.Action{
				"changes_requested": {Type: "agent", Agent: "fixer"},
				"new_comment":       {Type: "agent", Agent: "fixer"},
				"issue_assigned":    {Type: "agent", Agent: "fixer", Checkout: "branch-off"},
			},
		}},
	}
}

func TestChangesRequested(t *testing.T) {
	g := newTestIntegration(t, baseConfig())
	body := []byte(`{
		"action":"submitted",
		"repository":{"full_name":"acme/widget","name":"widget","owner":{"login":"acme"}},
		"pull_request":{"number":7,"head":{"sha":"abc123"},"base":{"ref":"main"}},
		"review":{"state":"changes_requested","id":99,"user":{"login":"reviewer"}}
	}`)
	trs := g.triggersFor(context.Background(), "pull_request_review", body)
	if len(trs) != 1 {
		t.Fatalf("want 1 trigger, got %d", len(trs))
	}
	if trs[0].Kind != "changes_requested" || trs[0].Target.PR != 7 || trs[0].Target.HeadSHA != "abc123" {
		t.Fatalf("unexpected trigger: %+v", trs[0])
	}
	if trs[0].Action == nil {
		t.Fatal("resolved action not attached")
	}
}

func TestReviewApprovedIgnored(t *testing.T) {
	g := newTestIntegration(t, baseConfig())
	body := []byte(`{
		"action":"submitted",
		"repository":{"full_name":"acme/widget","name":"widget","owner":{"login":"acme"}},
		"pull_request":{"number":7,"head":{"sha":"abc"}},
		"review":{"state":"approved","id":1,"user":{"login":"reviewer"}}
	}`)
	if trs := g.triggersFor(context.Background(), "pull_request_review", body); len(trs) != 0 {
		t.Fatalf("approved review should not trigger, got %d", len(trs))
	}
}

func TestNewCommentIgnoresSelf(t *testing.T) {
	g := newTestIntegration(t, baseConfig())
	// Comment authored by "me" (a reviewer/assignee login) must be ignored.
	self := []byte(`{
		"action":"created",
		"repository":{"full_name":"acme/widget","name":"widget","owner":{"login":"acme"}},
		"issue":{"number":3,"pull_request":{}},
		"comment":{"id":11,"user":{"login":"me"},"body":"hi"}
	}`)
	if trs := g.triggersFor(context.Background(), "issue_comment", self); len(trs) != 0 {
		t.Fatalf("self comment should be ignored, got %d", len(trs))
	}
	// Comment by someone else on a PR should trigger.
	other := []byte(`{
		"action":"created",
		"repository":{"full_name":"acme/widget","name":"widget","owner":{"login":"acme"}},
		"issue":{"number":3,"pull_request":{}},
		"comment":{"id":12,"user":{"login":"bot"},"body":"please fix"}
	}`)
	trs := g.triggersFor(context.Background(), "issue_comment", other)
	if len(trs) != 1 || trs[0].Kind != "new_comment" {
		t.Fatalf("want 1 new_comment, got %+v", trs)
	}
}

func TestRepoNotMatchedNoTrigger(t *testing.T) {
	g := newTestIntegration(t, baseConfig())
	body := []byte(`{
		"action":"submitted",
		"repository":{"full_name":"other/repo","name":"repo","owner":{"login":"other"}},
		"pull_request":{"number":1,"head":{"sha":"x"}},
		"review":{"state":"changes_requested","id":1,"user":{"login":"r"}}
	}`)
	if trs := g.triggersFor(context.Background(), "pull_request_review", body); len(trs) != 0 {
		t.Fatalf("unmatched repo should not trigger, got %d", len(trs))
	}
}

func TestIssueAssignedMatch(t *testing.T) {
	g := newTestIntegration(t, baseConfig())
	body := []byte(`{
		"action":"assigned",
		"repository":{"full_name":"acme/widget","name":"widget","owner":{"login":"acme"}},
		"issue":{"number":42},
		"assignee":{"login":"me"}
	}`)
	trs := g.triggersFor(context.Background(), "issues", body)
	if len(trs) != 1 || trs[0].Kind != "issue_assigned" || trs[0].Target.Issue != 42 {
		t.Fatalf("want issue_assigned for #42, got %+v", trs)
	}
	// Assigned to someone else → no trigger.
	body2 := []byte(`{
		"action":"assigned",
		"repository":{"full_name":"acme/widget","name":"widget","owner":{"login":"acme"}},
		"issue":{"number":42},
		"assignee":{"login":"stranger"}
	}`)
	if trs := g.triggersFor(context.Background(), "issues", body2); len(trs) != 0 {
		t.Fatalf("assignment to another user should not trigger, got %d", len(trs))
	}
}
