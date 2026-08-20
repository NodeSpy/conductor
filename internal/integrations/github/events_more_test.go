package github

import (
	"context"
	"testing"

	"github.com/NodeSpy/paseo-conductor/internal/config"
)

// richConfig enables most kinds for parsing tests.
func richConfig() Config {
	act := func() config.Action { return config.Action{Type: "agent", Agent: "fixer"} }
	return Config{
		App:     AppConfig{AppID: 1, PrivateKeyPath: "x", WebhookSecret: "s"},
		Webhook: WebhookConfig{SmeeURL: "https://smee.io/x"},
		Rules: []Rule{{
			Match:    Match{Repos: []string{"acme/*"}},
			Reviewer: config.Actors{Logins: []string{"me"}},
			Assignee: config.Actors{Logins: []string{"me"}},
			Actions: map[string]config.Action{
				"changes_requested": act(),
				"new_comment":       act(),
				"failing_checks":    act(),
				"merge_conflict":    act(),
				"pr_behind":         {Type: "command", Command: []string{"gh", "pr", "update-branch"}},
				"issue_ready":       {Type: "agent", Agent: "fixer", Checkout: "branch-off", LabelsAny: []string{"Ready"}},
				"review_requested":  {Type: "command", Command: []string{"critique"}},
				"self_review":       {Type: "command", Command: []string{"critique", "--review", "{{.repo}}#{{.pr}}"}},
			},
		}},
	}
}

func do(t *testing.T, g *Integration, event, body string) []string {
	t.Helper()
	trs := g.triggersFor(context.Background(), event, []byte(body))
	kinds := make([]string, len(trs))
	for i, tr := range trs {
		kinds[i] = tr.Kind
	}
	return kinds
}

func TestCheckRunFailing(t *testing.T) {
	g := newTestIntegration(t, richConfig())
	body := `{"action":"completed","repository":{"full_name":"acme/w","name":"w","owner":{"login":"acme"}},
		"check_run":{"conclusion":"failure","name":"build","head_sha":"h9","id":321,"pull_requests":[{"number":4}]}}`
	trs := g.triggersFor(context.Background(), "check_run", []byte(body))
	if len(trs) != 1 || trs[0].Kind != "failing_checks" {
		t.Fatalf("want failing_checks, got %+v", trs)
	}
	if trs[0].Context["run_id"] != int64(321) || trs[0].Dedup != "fail@h9" {
		t.Fatalf("run_id/dedup wrong: %+v", trs[0].Context)
	}
	// A successful check produces nothing.
	ok := `{"action":"completed","repository":{"full_name":"acme/w","name":"w","owner":{"login":"acme"}},
		"check_run":{"conclusion":"success","head_sha":"h","pull_requests":[{"number":4}]}}`
	if k := do(t, g, "check_run", ok); len(k) != 0 {
		t.Fatalf("success check should not trigger, got %v", k)
	}
}

func TestWorkflowRunFailing(t *testing.T) {
	g := newTestIntegration(t, richConfig())
	body := `{"action":"completed","repository":{"full_name":"acme/w","name":"w","owner":{"login":"acme"}},
		"workflow_run":{"conclusion":"timed_out","head_sha":"hh","id":9,"pull_requests":[{"number":2}]}}`
	if k := do(t, g, "workflow_run", body); len(k) != 1 || k[0] != "failing_checks" {
		t.Fatalf("want failing_checks, got %v", k)
	}
}

func TestReviewCommentTriggers(t *testing.T) {
	g := newTestIntegration(t, richConfig())
	body := `{"action":"created","repository":{"full_name":"acme/w","name":"w","owner":{"login":"acme"}},
		"pull_request":{"number":6,"head":{"sha":"h"},"base":{"ref":"main"}},
		"comment":{"id":7,"user":{"login":"bot"},"body":"nit"}}`
	if k := do(t, g, "pull_request_review_comment", body); len(k) != 1 || k[0] != "new_comment" {
		t.Fatalf("want new_comment, got %v", k)
	}
}

func TestReviewRequestedMatch(t *testing.T) {
	g := newTestIntegration(t, richConfig())
	yes := `{"action":"review_requested","repository":{"full_name":"acme/w","name":"w","owner":{"login":"acme"}},
		"pull_request":{"number":6,"head":{"sha":"h"}},"requested_reviewer":{"login":"me"}}`
	if k := do(t, g, "pull_request", yes); len(k) != 1 || k[0] != "review_requested" {
		t.Fatalf("want review_requested, got %v", k)
	}
	no := `{"action":"review_requested","repository":{"full_name":"acme/w","name":"w","owner":{"login":"acme"}},
		"pull_request":{"number":6,"head":{"sha":"h"}},"requested_reviewer":{"login":"someone-else"}}`
	if k := do(t, g, "pull_request", no); len(k) != 0 {
		t.Fatalf("reviewer mismatch should not trigger, got %v", k)
	}
}

func TestPullRequestClosedEmitsKindClosed(t *testing.T) {
	g := newTestIntegration(t, richConfig())
	body := `{"action":"closed","repository":{"full_name":"acme/w","name":"w","owner":{"login":"acme"}},
		"pull_request":{"number":6,"head":{"sha":"h"}}}`
	trs := g.triggersFor(context.Background(), "pull_request", []byte(body))
	if len(trs) != 1 || trs[0].Kind == "" || trs[0].Kind != "_closed" {
		t.Fatalf("want _closed, got %+v", trs)
	}
}

func TestIssueReadyLabelFilter(t *testing.T) {
	g := newTestIntegration(t, richConfig())
	ready := `{"action":"labeled","repository":{"full_name":"acme/w","name":"w","owner":{"login":"acme"}},
		"issue":{"number":10},"label":{"name":"Ready"}}`
	if k := do(t, g, "issues", ready); len(k) != 1 || k[0] != "issue_ready" {
		t.Fatalf("want issue_ready, got %v", k)
	}
	other := `{"action":"labeled","repository":{"full_name":"acme/w","name":"w","owner":{"login":"acme"}},
		"issue":{"number":10},"label":{"name":"wontfix"}}`
	if k := do(t, g, "issues", other); len(k) != 0 {
		t.Fatalf("non-Ready label should not trigger, got %v", k)
	}
}

func TestNewCommentFromUsersFilter(t *testing.T) {
	cfg := richConfig()
	cfg.Rules[0].Actions["new_comment"] = config.Action{Type: "agent", Agent: "fixer", FromUsers: []string{"coderabbitai[bot]"}}
	g := newTestIntegration(t, cfg)

	match := `{"action":"created","repository":{"full_name":"acme/w","name":"w","owner":{"login":"acme"}},
		"issue":{"number":3,"pull_request":{}},"comment":{"id":1,"user":{"login":"coderabbitai[bot]"},"body":"x"}}`
	if k := do(t, g, "issue_comment", match); len(k) != 1 {
		t.Fatalf("allowed bot should trigger, got %v", k)
	}
	skip := `{"action":"created","repository":{"full_name":"acme/w","name":"w","owner":{"login":"acme"}},
		"issue":{"number":3,"pull_request":{}},"comment":{"id":2,"user":{"login":"randomuser"},"body":"x"}}`
	if k := do(t, g, "issue_comment", skip); len(k) != 0 {
		t.Fatalf("non-listed user should be filtered, got %v", k)
	}
}

func TestDisabledActionNoTrigger(t *testing.T) {
	cfg := richConfig()
	dis := false
	a := cfg.Rules[0].Actions["changes_requested"]
	a.Enabled = &dis
	cfg.Rules[0].Actions["changes_requested"] = a
	g := newTestIntegration(t, cfg)
	body := `{"action":"submitted","repository":{"full_name":"acme/w","name":"w","owner":{"login":"acme"}},
		"pull_request":{"number":6,"head":{"sha":"h"}},"review":{"state":"changes_requested","id":1,"user":{"login":"r"}}}`
	if k := do(t, g, "pull_request_review", body); len(k) != 0 {
		t.Fatalf("disabled action should not trigger, got %v", k)
	}
}
