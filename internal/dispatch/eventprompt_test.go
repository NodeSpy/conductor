package dispatch

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/NodeSpy/conductor/internal/core"
)

func TestEventPromptShapeAndStripsCredentials(t *testing.T) {
	tr := core.Trigger{
		Source: "github",
		Kind:   "failing_checks",
		Title:  "Fix the flaky test",
		Target: core.Target{Repo: "o/r", Owner: "o", Name: "r", PR: 7, Number: 7, BaseRef: "main", HTMLURL: "https://forge/o/r/pull/7"},
		Context: map[string]any{
			"author":          "octocat",
			"comment_body":    "please fix",
			"app_token":       "SECRET-APP",
			"gh_token":        "SECRET-USER",
			"installation_id": 42,
			"secrets":         map[string]any{"deploy_key": "x"},
			"empty":           "",
			"nilv":            nil,
		},
	}
	p := EventPrompt(tr)

	if !strings.HasPrefix(p, "Act on this event:\n\n{") {
		t.Fatalf("prompt should lead with the imperative + JSON, got: %.40q", p)
	}
	// No tool/connector-command assumptions in the framing.
	for _, banned := range []string{"gh ", "git ", "conductor "} {
		if strings.Contains(p, banned) {
			t.Errorf("framing must name no tools, found %q", banned)
		}
	}
	// Credentials/plumbing must never reach the agent's event.
	for _, secret := range []string{"SECRET-APP", "SECRET-USER", "app_token", "gh_token", "installation_id", "secrets", "deploy_key"} {
		if strings.Contains(p, secret) {
			t.Errorf("credential/plumbing %q leaked into the event prompt", secret)
		}
	}

	// The JSON body must parse and carry the event essentials.
	body := p[strings.Index(p, "{"):]
	var ev map[string]any
	if err := json.Unmarshal([]byte(body), &ev); err != nil {
		t.Fatalf("event body is not valid JSON: %v\n%s", err, body)
	}
	if ev["source"] != "github" || ev["kind"] != "failing_checks" || ev["title"] != "Fix the flaky test" {
		t.Fatalf("event missing source/kind/title: %+v", ev)
	}
	tgt, _ := ev["target"].(map[string]any)
	if tgt["repo"] != "o/r" || tgt["pr"].(float64) != 7 || tgt["base"] != "main" {
		t.Fatalf("target fields wrong: %+v", tgt)
	}
	ctx, _ := ev["context"].(map[string]any)
	if ctx["author"] != "octocat" || ctx["comment_body"] != "please fix" {
		t.Fatalf("context event fields missing: %+v", ctx)
	}
	if _, bad := ctx["empty"]; bad {
		t.Error("empty-string context value should be dropped")
	}
	if _, bad := ctx["nilv"]; bad {
		t.Error("nil context value should be dropped")
	}
}

func TestEventPromptDeterministic(t *testing.T) {
	tr := core.Trigger{Source: "github", Kind: "merge_conflict", Target: core.Target{Repo: "o/r", Number: 3},
		Context: map[string]any{"b": 2, "a": 1, "c": 3}}
	if EventPrompt(tr) != EventPrompt(tr) {
		t.Fatal("EventPrompt must be stable for a given trigger (json sorts map keys)")
	}
}
