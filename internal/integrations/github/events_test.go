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

func TestProjectMapStampsTarget(t *testing.T) {
	cfg := baseConfig()
	// Key casing differs from the incoming repo to exercise case-insensitivity.
	cfg.ProjectMap = map[string]string{"Acme/Widget": "acme-internal/widget"}
	g := newTestIntegration(t, cfg)
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
	tgt := trs[0].Target
	if tgt.Repo != "acme/widget" {
		t.Fatalf("forge repo must be preserved, got %q", tgt.Repo)
	}
	if tgt.Project != "acme-internal/widget" {
		t.Fatalf("Project should be remapped, got %q", tgt.Project)
	}
	if got := tgt.CheckoutRepo(); got != "acme-internal/widget" {
		t.Fatalf("CheckoutRepo should use Project, got %q", got)
	}
}

func TestProjectMapUnsetLeavesTargetRepo(t *testing.T) {
	g := newTestIntegration(t, baseConfig())
	tgt := g.target("acme/widget", 7, "abc", "main", "")
	if tgt.Project != "" {
		t.Fatalf("unmapped repo should leave Project empty, got %q", tgt.Project)
	}
	if tgt.CheckoutRepo() != "acme/widget" {
		t.Fatalf("CheckoutRepo should fall back to Repo, got %q", tgt.CheckoutRepo())
	}
}

func TestProjectRewrite(t *testing.T) {
	cases := []struct {
		name    string
		rewrite ProjectRewrite
		repo    string
		want    string // expected Target.Project ("" = falls back to Repo)
	}{
		{"org+lowercase", ProjectRewrite{Org: "ednition", Lowercase: true}, "EdnitionCode/RosterStream", "ednition/rosterstream"},
		{"org only", ProjectRewrite{Org: "ednition"}, "EdnitionCode/RosterStream", "ednition/RosterStream"},
		{"lowercase only", ProjectRewrite{Lowercase: true}, "EdnitionCode/RosterStream", "ednitioncode/rosterstream"},
		{"noop when already normalized", ProjectRewrite{Org: "acme", Lowercase: true}, "acme/widget", ""},
		{"inactive rewrite", ProjectRewrite{}, "EdnitionCode/RosterStream", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := baseConfig()
			cfg.ProjectRewrite = tc.rewrite
			g := newTestIntegration(t, cfg)
			if got := g.mapProject(tc.repo); got != tc.want {
				t.Fatalf("mapProject(%q) = %q, want %q", tc.repo, got, tc.want)
			}
		})
	}
}

func TestProjectMapWinsOverRewrite(t *testing.T) {
	// An explicit per-repo mapping takes precedence over the org-wide rewrite.
	cfg := baseConfig()
	cfg.ProjectMap = map[string]string{"EdnitionCode/Special": "custom/project"}
	cfg.ProjectRewrite = ProjectRewrite{Org: "ednition", Lowercase: true}
	g := newTestIntegration(t, cfg)
	if got := g.mapProject("EdnitionCode/Special"); got != "custom/project" {
		t.Fatalf("explicit map should win, got %q", got)
	}
	// A repo without an explicit entry still gets the rewrite.
	if got := g.mapProject("EdnitionCode/Other"); got != "ednition/other" {
		t.Fatalf("rewrite should apply to unlisted repos, got %q", got)
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
