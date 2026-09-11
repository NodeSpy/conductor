package flow

import (
	"context"
	"strings"
	"testing"

	"github.com/NodeSpy/conductor/internal/core"
)

// Part B (docs/design/scope-templating.md): an allowlist entry containing
// `{{ }}` is rendered against THIS DISPATCH's facts before it is matched, so
// one line can mean a different resource per event.

// The headline: `#pr-{{.number}}` is a per-PR channel grant.
func TestScopeAllowlistRendersPerEvent(t *testing.T) {
	r := scopeRig(t, scopeBaseCfg)
	id := func(number int) SkillIdentity {
		return SkillIdentity{
			Agent: "reviewer", Repo: "acme/app", Number: number,
			Verbs:  []string{"slack.post"},
			Scopes: map[string]map[string][]string{"slack.post": {"channel": {"#pr-{{.number}}"}}},
		}
	}
	post := func(number int, channel string) error {
		_, err := r.RunSkillVerb(context.Background(), id(number), "slack.post",
			map[string]any{"channel": channel, "text": "hi"})
		return err
	}
	if err := post(42, "#pr-42"); err != nil {
		t.Fatalf("the channel this dispatch's PR renders to must be allowed: %v", err)
	}
	if err := post(42, "#pr-99"); !scopeRefused(err) {
		t.Fatalf("a PR-42 dispatch must not reach #pr-99 — the entry renders per event, got %v", err)
	}
	// The same grant follows the dispatch: on PR 99 it is #pr-99 that opens.
	if err := post(99, "#pr-99"); err != nil {
		t.Fatalf("the same grant must follow the dispatch: %v", err)
	}
	if err := post(99, "#pr-42"); !scopeRefused(err) {
		t.Fatalf("…and close behind it, got %v", err)
	}
}

// The plan surface renders the same way, off policy.agent_authored.
func TestPlanSurfaceRendersAllowlistEntries(t *testing.T) {
	r := scopeRig(t, `
connectors:
  svc: { use: fake }
policy:
  agent_authored:
    allow: ["**"]
    allow_scopes:
      repo: ["{{.owner}}/docs"]
`)
	trig := core.Trigger{Kind: "ping", Target: core.Target{Repo: "acme/app", Owner: "acme"}}
	pol := r.planPolicy()
	if err := r.checkVerbResources(pol, trig, "svc.post", map[string]any{"repo": "acme/docs"}, nil); err != nil {
		t.Fatalf("the rendered entry must admit acme/docs: %v", err)
	}
	if err := r.checkVerbResources(pol, trig, "svc.post", map[string]any{"repo": "evil/docs"}, nil); !scopeRefused(err) {
		t.Fatalf("another owner's docs must be refused, got %v", err)
	}
}

// FAIL-CLOSED. A template that errors, references something this dispatch
// doesn't have, or renders empty matches NOTHING — a pattern that can't be
// evaluated must never admit.
func TestScopeTemplateFailsClosed(t *testing.T) {
	r := scopeRig(t, scopeBaseCfg)
	for _, tc := range []struct{ name, entry, attempt string }{
		{"a secret reference renders empty", "{{.secrets.token}}", ""},
		{"an unparseable template", "#{{ .broken", "#{{ .broken"},
		{"a fact this dispatch lacks", "#ch-{{.nonexistent_fact}}", "#ch-"},
		{"a side-effecting func is not available", `{{ kv "s" "n" "k" }}`, ""},
		{"a vault read is not available", `{{ vault "house" "k" }}`, ""},
		{"renders to nothing at all", "{{ \"\" }}", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			id := SkillIdentity{
				Agent: "probe", Repo: "acme/app", Number: 1,
				Verbs:  []string{"slack.post"},
				Scopes: map[string]map[string][]string{"slack.post": {"channel": {tc.entry}}},
			}
			// Whatever the agent guesses the entry might have become, it is
			// refused: the entry contributed nothing to the allowlist.
			for _, guess := range []string{tc.attempt, "", "#anything", tc.entry} {
				if guess == "" {
					continue
				}
				_, err := r.RunSkillVerb(context.Background(), id, "slack.post",
					map[string]any{"channel": guess, "text": "hi"})
				if !scopeRefused(err) {
					t.Errorf("entry %q + channel %q: must be refused (fail-closed), got %v", tc.entry, guess, err)
				}
			}
		})
	}
}

// A secret must not reach the render context, and must not reach the DENIAL
// either — a check that leaked what it rendered would be an oracle.
func TestScopeRenderNeverSeesSecrets(t *testing.T) {
	r := scopeRig(t, scopeBaseCfg)
	r.Secrets.Track("s3kr1t-value")
	id := SkillIdentity{
		Agent: "probe", Repo: "acme/app", Number: 1,
		Verbs:  []string{"slack.post"},
		Scopes: map[string]map[string][]string{"slack.post": {"channel": {"#{{.secrets.token}}"}}},
		// A trigger context carrying a credential, as slack's really does.
		Context: map[string]any{"slack_bot_token": "s3kr1t-value", "slack": map[string]any{"channel": "#ops"}},
	}
	// The channel deliberately is NOT the secret's value: a call carrying
	// tracked material is refused by the relay barrier before the scope check
	// even runs, and that would prove nothing about the render.
	_, err := r.RunSkillVerb(context.Background(), id, "slack.post",
		map[string]any{"channel": "#whatever-it-rendered-to", "text": "hi"})
	if !scopeRefused(err) {
		t.Fatalf("a secret-referencing entry must match nothing, got %v", err)
	}
	if strings.Contains(errText(err), "s3kr1t-value") {
		t.Fatalf("the denial leaked secret material: %v", err)
	}

	// And the render data itself is the CLOSED SET and nothing else — see
	// TestScopeRenderContextIsAClosedSet below, which enumerates it.
}

// THE CLASS-CLOSER (round-6 A). The render context started as a DENYLIST:
// baseData minus a few secret-bearing keys, everything else passed through.
// That let PR-AUTHOR-CONTROLLED free text — head_ref, title, comment_body,
// author, labels, copied verbatim out of the webhook — into a security
// check's template, so `channel: ["{{.head_ref}}"]` could be FORGED by naming
// a branch after the channel you wanted. It also meant the NEXT enriched
// context fact a connector adds would be silently interpolatable.
//
// It is an allowlist now, and this enumerates it: exactly the platform-
// assigned facts, nothing else, no matter what the trigger carries.
func TestScopeRenderContextIsAClosedSet(t *testing.T) {
	r := scopeRig(t, scopeBaseCfg)
	r.Secrets.Track("s3kr1t-value")
	// A trigger carrying every attacker-reachable fact conductor publishes,
	// plus a credential and an arbitrary enriched fact.
	trig := core.Trigger{
		Kind:  "review_requested",
		Title: "attacker's title",
		Target: core.Target{
			Repo: "acme/app", Owner: "acme", Name: "app", Number: 42, PR: 42,
			HeadSHA: "deadbeef", BaseRef: "main", HTMLURL: "https://example.com/pr/42",
		},
		Context: map[string]any{
			"head_ref": "general", "author": "attacker", "comment_body": "#ops",
			"labels": []any{"#ops"}, "title": "attacker's title",
			"slack_bot_token":                     "s3kr1t-value",
			"a_new_enriched_fact_nobody_reviewed": "general",
		},
	}
	data := r.scopeRenderData(trig)

	want := map[string]any{
		"number": 42, "owner": "acme", "name": "app", "repo": "acme/app",
		"kind": "review_requested",
	}
	for k, v := range want {
		if data[k] != v {
			t.Errorf("safe fact %q: got %v, want %v — the facts an operator CAN key off must stay available", k, data[k], v)
		}
	}
	for k := range data {
		if _, ok := want[k]; !ok {
			t.Errorf("key %q leaked into the scope render context. It is an ALLOWLIST: a fact "+
				"reaches a security check's template only by being added to scopeFacts "+
				"deliberately, with the question 'can a PR author choose this value?' answered", k)
		}
	}
	// The specific forgeable ones, named, so a regression says which.
	for _, forgeable := range []string{
		"title", "head_ref", "author", "labels", "comment_body", "head", "base", "url",
		"pr", "issue", "inputs", "secrets", "vaults", "slack_bot_token",
		"a_new_enriched_fact_nobody_reviewed",
	} {
		if _, present := data[forgeable]; present {
			t.Errorf("%q is interpolatable — it is author-controlled, or unreviewed, or both", forgeable)
		}
	}
}

// THE FORGE ATTEMPT, end to end. An operator writes the natural extension of
// the documented idiom; an attacker names their branch after the channel they
// want. The entry must render to nothing and admit nothing.
func TestAuthorControlledFactsCannotForgeAnAllowlistMatch(t *testing.T) {
	r := scopeRig(t, scopeBaseCfg)
	for _, entry := range []string{
		"{{.head_ref}}", "{{.title}}", "{{.comment_body}}", "{{.author}}", "{{.labels}}",
		"#{{.head_ref}}", "{{.head_ref}}/prod",
	} {
		id := SkillIdentity{
			Agent: "probe", Repo: "acme/app", Number: 42,
			Verbs:  []string{"slack.post"},
			Scopes: map[string]map[string][]string{"slack.post": {"channel": {entry}}},
			// The attacker's own strings, exactly as the webhook delivered them.
			Context: map[string]any{
				"head_ref": "general", "title": "general", "comment_body": "general",
				"author": "general", "labels": "general",
			},
		}
		for _, guess := range []string{"general", "#general", "general/prod", ""} {
			if guess == "" {
				continue
			}
			_, err := r.RunSkillVerb(context.Background(), id, "slack.post",
				map[string]any{"channel": guess, "text": "hi"})
			if !scopeRefused(err) {
				t.Errorf("entry %q + channel %q: an author-chosen fact forged an allowlist "+
					"match — got %v", entry, guess, err)
			}
		}
	}
	// The platform-assigned facts still work, or the feature is gone.
	ok := SkillIdentity{
		Agent: "probe", Repo: "acme/app", Number: 42,
		Verbs:  []string{"slack.post"},
		Scopes: map[string]map[string][]string{"slack.post": {"channel": {"#pr-{{.number}}"}}},
	}
	if _, err := r.RunSkillVerb(context.Background(), ok, "slack.post",
		map[string]any{"channel": "#pr-42", "text": "hi"}); err != nil {
		t.Fatalf("a platform-assigned fact must still render and match: %v", err)
	}
}

// THE INJECTION QUESTION. The agent supplies the option value; it must never
// be able to influence what the allowlist renders to. If it could, the check
// would be checking the agent's own claim.
func TestAgentOptionValueCannotSteerTheAllowlist(t *testing.T) {
	r := scopeRig(t, scopeBaseCfg)
	id := SkillIdentity{
		Agent: "probe", Repo: "acme/app", Number: 7,
		Verbs:  []string{"slack.post"},
		Scopes: map[string]map[string][]string{"slack.post": {"channel": {"#pr-{{.number}}"}}},
	}
	// Every one of these tries to get `.channel`, `.text`, or a template of
	// its own into the render: an option key colliding with a fact name, a
	// value that IS a template, a value that is the raw entry.
	for _, opts := range []map[string]any{
		{"channel": "#pr-{{.number}}", "text": "hi"},
		{"channel": "#pr-999", "text": "hi", "number": 999},
		{"channel": "{{.channel}}", "text": "hi"},
		{"channel": "#pr-999", "text": "{{.number}}"},
		{"channel": "#pr-{{.pr}}", "text": "hi"},
	} {
		_, err := r.RunSkillVerb(context.Background(), id, "slack.post", opts)
		if !scopeRefused(err) {
			t.Errorf("options %v: the agent influenced the allowlist render — the value being "+
				"checked must never be a render SOURCE; got %v", opts, err)
		}
	}
	// The one value that does pass is the one the TRUSTED fact renders to.
	if _, err := r.RunSkillVerb(context.Background(), id, "slack.post",
		map[string]any{"channel": "#pr-7", "text": "hi"}); err != nil {
		t.Fatalf("the dispatch's own PR channel must still be allowed: %v", err)
	}
}

// Entries with no `{{` are not rendered at all — the fast path that keeps
// every existing config byte-identical in behavior.
func TestUntemplatedEntriesAreNotRendered(t *testing.T) {
	// A literal that LOOKS like a template to a careless matcher but has no
	// action in it stays literal, braces and all.
	if got, err := renderScopePattern("#ops", map[string]any{"ops": "x"}); err != nil || got != "#ops" {
		t.Fatalf("an untemplated entry must pass through untouched: %q %v", got, err)
	}
	rp := &resourcePolicy{render: map[string]any{"number": 7}}
	in := []string{"#a", "#b"}
	out := rp.expand(in)
	if &in[0] != &out[0] {
		t.Error("a list with no templated entry should be returned as-is (no per-call allocation)")
	}
}
