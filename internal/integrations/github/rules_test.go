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

func TestResolveMostSpecificWinsAndMerge(t *testing.T) {
	cfg := Config{
		App:     AppConfig{AppID: 1, PrivateKeyPath: "x", WebhookSecret: "s"},
		Webhook: WebhookConfig{SmeeURL: "https://smee.io/x"},
		Defaults: Rule{
			Reviewer: config.Actors{Logins: []string{"me"}},
			Assignee: config.Actors{Logins: []string{"me"}},
			Actions: as1(map[string]config.Action{
				"failing_checks": {Type: "agent", Agent: "fixer", Prompt: "base"},
			}),
		},
		Rules: []Rule{
			{Match: Match{Repos: []string{"acme/special"}},
				Actions: as1(map[string]config.Action{"failing_checks": {Agent: "special-fixer"}})},
			{Match: Match{Repos: []string{"acme/*"}}},
		},
	}
	g := newTestIntegration(t, cfg)

	// First rule wins for the specific repo, overriding just the agent.
	r, ok := g.resolve("acme/special")
	if !ok {
		t.Fatal("expected match")
	}
	fc := r.Actions["failing_checks"][0]
	if fc.Agent != "special-fixer" || fc.Prompt != "base" || fc.Type != "agent" {
		t.Fatalf("merge wrong: %+v", fc)
	}
	if !r.Reviewer.HasLogin("me") {
		t.Fatal("reviewer should inherit from defaults")
	}

	// Generic repo falls to the second rule (defaults only).
	r2, _ := g.resolve("acme/other")
	if r2.Actions["failing_checks"][0].Agent != "fixer" {
		t.Fatalf("generic repo should use default agent, got %q", r2.Actions["failing_checks"][0].Agent)
	}

	// Unmatched repo.
	if _, ok := g.resolve("nope/repo"); ok {
		t.Fatal("unmatched repo should not resolve")
	}
}

// TestResolveMostSpecificIgnoresOrder pins the key property: the most-specific
// matching rule wins regardless of config order. Here the general "EdnitionCode/*"
// rule is listed BEFORE the specific "EdnitionCode/RosterStream" rule; RosterStream
// must still resolve to the specific one (under the old first-match-wins it would
// have wrongly picked the general rule).
func TestResolveMostSpecificIgnoresOrder(t *testing.T) {
	cfg := Config{
		App:     AppConfig{AppID: 1, PrivateKeyPath: "x", WebhookSecret: "s"},
		Webhook: WebhookConfig{SmeeURL: "https://smee.io/x"},
		Rules: []Rule{
			{Match: Match{Repos: []string{"EdnitionCode/*"}}, // general FIRST
				Actions: as1(map[string]config.Action{"new_comment": {Type: "agent", Agent: "general"}})},
			{Match: Match{Repos: []string{"EdnitionCode/RosterStream"}}, // specific SECOND
				Actions: as1(map[string]config.Action{"new_comment": {Type: "agent", Agent: "specific"}})},
			{Match: Match{Repos: []string{"*/*"}}, // catch-all, least specific
				Actions: as1(map[string]config.Action{"new_comment": {Type: "agent", Agent: "catchall"}})},
		},
	}
	g := newTestIntegration(t, cfg)

	if r, _ := g.resolve("EdnitionCode/RosterStream"); r.Actions["new_comment"][0].Agent != "specific" {
		t.Fatalf("exact match should win over org-wildcard, got %q", r.Actions["new_comment"][0].Agent)
	}
	if r, _ := g.resolve("EdnitionCode/infra"); r.Actions["new_comment"][0].Agent != "general" {
		t.Fatalf("org-wildcard should win over */*, got %q", r.Actions["new_comment"][0].Agent)
	}
	if r, _ := g.resolve("other/repo"); r.Actions["new_comment"][0].Agent != "catchall" {
		t.Fatalf("*/* should match everything else, got %q", r.Actions["new_comment"][0].Agent)
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
