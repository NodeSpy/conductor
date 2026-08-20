package github

import (
	"testing"

	"github.com/NodeSpy/paseo-conductor/internal/config"
)

func identityConfig(me, reviewer []string) Config {
	return Config{
		App:     AppConfig{AppID: 1, PrivateKeyPath: "x", WebhookSecret: "s"},
		Webhook: WebhookConfig{SmeeURL: "https://smee.io/x"},
		Rules: []Rule{{
			Match:    Match{Repos: []string{"acme/*"}},
			Me:       config.Actors{Logins: me},
			Reviewer: config.Actors{Logins: reviewer},
			Actions: map[string]config.Action{
				"new_comment": {Type: "agent", Agent: "fixer"},
				"self_review": {Type: "command", Command: []string{"critique"}},
			},
		}},
	}
}

func comment(login string) string {
	return `{"action":"created","repository":{"full_name":"acme/w","name":"w","owner":{"login":"acme"}},
		"issue":{"number":3,"pull_request":{}},"comment":{"id":1,"user":{"login":"` + login + `"}}}`
}

func prOpenedBy(login string) string {
	return `{"action":"opened","repository":{"full_name":"acme/w","name":"w","owner":{"login":"acme"}},
		"pull_request":{"number":6,"head":{"sha":"h"},"user":{"login":"` + login + `"}}}`
}

func TestIdentityUsesMeNotReviewer(t *testing.T) {
	// me = "me"; reviewer = "teammate" (a different person).
	g := newTestIntegration(t, identityConfig([]string{"me"}, []string{"teammate"}))

	// A comment by the reviewer is NOT self → new_comment fires.
	if k := do(t, g, "issue_comment", comment("teammate")); len(k) != 1 || k[0] != "new_comment" {
		t.Fatalf("reviewer is not 'you' — comment should trigger, got %v", k)
	}
	// A comment by you is filtered.
	if k := do(t, g, "issue_comment", comment("me")); len(k) != 0 {
		t.Fatalf("your own comment should be ignored, got %v", k)
	}
	// self_review only on your own PR.
	if k := do(t, g, "pull_request", prOpenedBy("me")); len(k) == 0 || !has(k, "self_review") {
		t.Fatalf("self_review should fire on your PR, got %v", k)
	}
	if k := do(t, g, "pull_request", prOpenedBy("teammate")); has(k, "self_review") {
		t.Fatalf("self_review should NOT fire on a reviewer's PR, got %v", k)
	}
}

func TestIdentityFallsBackToReviewer(t *testing.T) {
	// No `me:` set → fall back to reviewer/assignee logins.
	g := newTestIntegration(t, identityConfig(nil, []string{"me"}))
	if k := do(t, g, "issue_comment", comment("me")); len(k) != 0 {
		t.Fatalf("fallback: reviewer login should count as you, got %v", k)
	}
	if k := do(t, g, "pull_request", prOpenedBy("me")); !has(k, "self_review") {
		t.Fatalf("fallback: self_review should fire on your PR, got %v", k)
	}
}

func TestActionLevelActors(t *testing.T) {
	// reviewer lives on review_requested; assignee on issue_assigned. No rule-level actors.
	cfg := Config{
		App:     AppConfig{AppID: 1, PrivateKeyPath: "x", WebhookSecret: "s"},
		Webhook: WebhookConfig{SmeeURL: "https://smee.io/x"},
		Rules: []Rule{{
			Match: Match{Repos: []string{"acme/*"}},
			Actions: map[string]config.Action{
				"review_requested": {Type: "command", Command: []string{"critique"},
					Reviewer: config.Actors{Logins: []string{"me"}}},
				"issue_assigned": {Type: "agent", Agent: "fixer",
					Assignee: config.Actors{Logins: []string{"me"}}},
			},
		}},
	}
	g := newTestIntegration(t, cfg)

	rr := func(login string) string {
		return `{"action":"review_requested","repository":{"full_name":"acme/w","name":"w","owner":{"login":"acme"}},
			"pull_request":{"number":6,"head":{"sha":"h"}},"requested_reviewer":{"login":"` + login + `"}}`
	}
	if k := do(t, g, "pull_request", rr("me")); !has(k, "review_requested") {
		t.Fatalf("action-level reviewer should match, got %v", k)
	}
	if k := do(t, g, "pull_request", rr("other")); has(k, "review_requested") {
		t.Fatalf("non-matching reviewer should not fire, got %v", k)
	}

	ia := func(login string) string {
		return `{"action":"assigned","repository":{"full_name":"acme/w","name":"w","owner":{"login":"acme"}},
			"issue":{"number":9},"assignee":{"login":"` + login + `"}}`
	}
	if k := do(t, g, "issues", ia("me")); !has(k, "issue_assigned") {
		t.Fatalf("action-level assignee should match, got %v", k)
	}
	if k := do(t, g, "issues", ia("other")); has(k, "issue_assigned") {
		t.Fatalf("non-matching assignee should not fire, got %v", k)
	}
}

func has(xs []string, want string) bool {
	for _, x := range xs {
		if x == want {
			return true
		}
	}
	return false
}
