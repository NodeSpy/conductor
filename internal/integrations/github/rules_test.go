package github

import (
	"testing"

	"github.com/NodeSpy/paseo-conductor/internal/config"
)

func TestMatchRepo(t *testing.T) {
	cases := []struct {
		patterns []string
		repo     string
		want     bool
	}{
		{[]string{"acme/*"}, "acme/widget", true},
		{[]string{"acme/*"}, "other/widget", false},
		{[]string{"acme/widget"}, "acme/widget", true},
		{[]string{"*/*"}, "acme/widget", true},
		{[]string{"acme/w*"}, "acme/widget", true},
		{[]string{"acme/*"}, "acme/a/b", false}, // '*' doesn't cross '/'
	}
	for _, c := range cases {
		if got := matchRepo(c.patterns, c.repo); got != c.want {
			t.Errorf("matchRepo(%v,%q)=%v want %v", c.patterns, c.repo, got, c.want)
		}
	}
}

func TestResolveFirstMatchWinsAndMerge(t *testing.T) {
	cfg := Config{
		App:     AppConfig{AppID: 1, PrivateKeyPath: "x", WebhookSecret: "s"},
		Webhook: WebhookConfig{SmeeURL: "https://smee.io/x"},
		Defaults: Rule{
			Reviewer: config.Actors{Logins: []string{"me"}},
			Assignee: config.Actors{Logins: []string{"me"}},
			Actions: map[string]config.Action{
				"failing_checks": {Type: "agent", Agent: "fixer", Prompt: "base"},
			},
		},
		Rules: []Rule{
			{Match: Match{Repos: []string{"acme/special"}},
				Actions: map[string]config.Action{"failing_checks": {Agent: "special-fixer"}}},
			{Match: Match{Repos: []string{"acme/*"}}},
		},
	}
	g := newTestIntegration(t, cfg)

	// First rule wins for the specific repo, overriding just the agent.
	r, ok := g.resolve("acme/special")
	if !ok {
		t.Fatal("expected match")
	}
	fc := r.Actions["failing_checks"]
	if fc.Agent != "special-fixer" || fc.Prompt != "base" || fc.Type != "agent" {
		t.Fatalf("merge wrong: %+v", fc)
	}
	if !r.Reviewer.HasLogin("me") {
		t.Fatal("reviewer should inherit from defaults")
	}

	// Generic repo falls to the second rule (defaults only).
	r2, _ := g.resolve("acme/other")
	if r2.Actions["failing_checks"].Agent != "fixer" {
		t.Fatalf("generic repo should use default agent, got %q", r2.Actions["failing_checks"].Agent)
	}

	// Unmatched repo.
	if _, ok := g.resolve("nope/repo"); ok {
		t.Fatal("unmatched repo should not resolve")
	}
}

func TestMergeAction(t *testing.T) {
	base := config.Action{Type: "agent", Agent: "fixer", Prompt: "p", Checkout: "checkout-pr"}
	over := config.Action{Prompt: "q", Enabled: func() *bool { b := false; return &b }()}
	got := mergeAction(base, over)
	if got.Prompt != "q" || got.Agent != "fixer" || got.Checkout != "checkout-pr" {
		t.Fatalf("mergeAction overlay wrong: %+v", got)
	}
	if got.IsEnabled() {
		t.Fatal("override Enabled=false should win")
	}
}

func TestMergeActionKeepsExcludeAndRerequest(t *testing.T) {
	// Regression: mergeAction must carry newer fields, or they silently vanish
	// whenever a rule is resolved.
	over := config.Action{
		Type:            "agent",
		RerequestReview: true,
		Exclude:         config.Exclude{Branches: []string{"release/*"}},
	}
	got := mergeAction(config.Action{}, over)
	if !got.RerequestReview {
		t.Error("rerequest_review dropped by merge")
	}
	if got.Exclude.Empty() || !got.Exclude.Matches("release/1.0", "", nil) {
		t.Error("exclude dropped by merge")
	}
}
