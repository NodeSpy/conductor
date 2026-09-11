package flow

import (
	"context"
	"strings"
	"testing"

	"github.com/NodeSpy/conductor/internal/kv"
)

// The functional half of docs/design/skill-verb-scope.md: the per-verb skill
// grant and the per-dimension plan-surface allowlist, from the operator's
// side rather than the mechanism's.

// scopeRig builds a runner over a config with slack + a fake connector and
// returns it in dry-run (no call reaches a real service).
func scopeRig(t *testing.T, cfgYAML string) *Runner {
	t.Helper()
	kv.SetDataDir(t.TempDir())
	kv.ResetStores()
	t.Cleanup(func() { kv.ResetStores(); kv.SetDataDir("") })
	cfg := loadConfig(t, cfgYAML)
	r := newTestRunner(t, cfg, buildRegistry(t, cfg)).Runner
	r.DryRun = true
	return r
}

const scopeBaseCfg = `
connectors:
  svc: { use: fake }
  slack: { use: slack, bot_token: x }
  gh: { use: github, token: x }
policy:
  agent_authored:
    allow: ["**"]
`

// THE CHANNEL GAP, closed. A grant for one channel does not reach another —
// the finding that had nowhere to live before scoping was connector-driven.
func TestSkillGrantScopesSlackChannel(t *testing.T) {
	r := scopeRig(t, scopeBaseCfg)
	// skill:
	//   verbs:
	//     slack.post: { channel: ["#code-reviews"] }
	granted := SkillIdentity{
		Agent: "reviewer", Repo: "trigger/repo", Number: 3,
		Verbs:  []string{"slack.post"},
		Scopes: map[string]map[string][]string{"slack.post": {"channel": {"#code-reviews"}}},
		// A github-triggered dispatch: no slack context of its own.
	}
	post := func(id SkillIdentity, channel string) error {
		_, err := r.RunSkillVerb(context.Background(), id, "slack.post",
			map[string]any{"channel": channel, "text": "hi"})
		return err
	}
	if err := post(granted, "#code-reviews"); err != nil {
		t.Fatalf("the granted channel must be reachable: %v", err)
	}
	err := post(granted, "#exec-private")
	if err == nil || !strings.Contains(err.Error(), "allow_scopes.channel") {
		t.Fatalf("a grant for #code-reviews must not reach #exec-private, got %v", err)
	}

	// With NO channel list, the dispatch's own channel still works — and only
	// that one. This is what makes the strong default usable.
	own := SkillIdentity{
		Agent: "responder", Verbs: []string{"slack.*"},
		Context: map[string]any{"slack": map[string]any{"channel": "#ops"}},
	}
	if err := post(own, "#ops"); err != nil {
		t.Fatalf("the channel the dispatch came from must need no grant: %v", err)
	}
	if err := post(own, "#exec-private"); err == nil || !strings.Contains(err.Error(), "allow_scopes.channel") {
		t.Fatalf("an ungranted channel must be refused even for a slack dispatch, got %v", err)
	}
}

// `github.submit_review: {}` — access, with the repo left at the dispatch's
// own. The grant an operator writes for "review THIS PR" must not be usable
// against another repo.
func TestSkillGrantPinsRepoToTheDispatch(t *testing.T) {
	r := scopeRig(t, scopeBaseCfg)
	id := SkillIdentity{
		Agent: "reviewer", Repo: "acme/app", Number: 42,
		Verbs:  []string{"gh.submit_review"},
		Scopes: map[string]map[string][]string{"gh.submit_review": {}},
	}
	call := func(repo string) error {
		_, err := r.RunSkillVerb(context.Background(), id, "gh.submit_review",
			map[string]any{"repo": repo, "pr": 42, "event": "COMMENT", "body": "lgtm"})
		return err
	}
	if err := call("acme/app"); err != nil {
		t.Fatalf("the PR under review must be reachable with no list: %v", err)
	}
	if err := call("acme/secrets"); err == nil || !strings.Contains(err.Error(), "allow_scopes.repo") {
		t.Fatalf("a review grant for acme/app must not reach acme/secrets, got %v", err)
	}
}

// A pattern grant scopes every verb it admits: `kv.*: {store: [...]}` is one
// line, not one line per verb.
func TestSkillGrantPatternScopesEveryMatchedVerb(t *testing.T) {
	r := scopeRig(t, `
connectors:
  svc: { use: fake }
stores:
  shared-kv: { type: boltdb }
policy:
  agent_authored:
    allow: ["**"]
`)
	id := SkillIdentity{
		Agent: "worker", Repo: "trigger/repo",
		Verbs:  []string{"kv.*"},
		Scopes: map[string]map[string][]string{"kv.*": {"store": {"shared-kv"}}},
	}
	for _, verb := range []string{"kv.get", "kv.set", "kv.delete"} {
		if _, err := r.RunSkillVerb(context.Background(), id, verb,
			map[string]any{"store": "shared-kv", "key": "k", "value": "v"}); err != nil {
			t.Errorf("%s on the granted store: %v", verb, err)
		}
		_, err := r.RunSkillVerb(context.Background(), id, verb,
			map[string]any{"store": "someone-elses", "key": "k", "value": "v"})
		if err == nil || !strings.Contains(err.Error(), "allow_scopes.store") {
			t.Errorf("%s must not reach an ungranted store, got %v", verb, err)
		}
	}
}

// A constraint on an option the verb does not declare as a resource is a LOAD
// ERROR — the typo class (`chanel:`), and the "I thought text was a
// destination" class, both caught before the daemon runs.
func TestVerbScopeOnNonResourceOptionIsALoadError(t *testing.T) {
	for _, tc := range []struct{ name, opt, wantIn string }{
		{"a content option", "text", `constrains "text"`},
		{"a typo", "chanel", `constrains "chanel"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := loadConfig(t, `
connectors:
  slack: { use: slack, bot_token: x }
triggers:
  - on: slack.app_mention
    steps:
      - type: agent
        name: responder
        model: m
        prompt: respond
        skill:
          verbs:
            slack.post: { `+tc.opt+`: ["x"] }
`)
			err := Validate(cfg, buildRegistry(t, cfg))
			if err == nil || !strings.Contains(err.Error(), tc.wantIn) {
				t.Fatalf("want a load error naming %q, got %v", tc.opt, err)
			}
			// …and it must say what IS scopeable, or the operator is left guessing.
			if !strings.Contains(err.Error(), "channel") {
				t.Errorf("the error should offer the verb's real resource options: %v", err)
			}
		})
	}
	// The same constraint on the option the verb DOES declare loads clean.
	cfg := loadConfig(t, `
connectors:
  slack: { use: slack, bot_token: x }
triggers:
  - on: slack.app_mention
    steps:
      - type: agent
        name: responder
        model: m
        prompt: respond
        skill:
          verbs:
            slack.post: { channel: ["#ops"] }
`)
	if err := Validate(cfg, buildRegistry(t, cfg)); err != nil {
		t.Fatalf("a constraint on a real resource option must load: %v", err)
	}
}

// The PLAN surface reaches the new dimensions through the same map: a slack
// step in an agent-authored plan is governed by allow_scopes.channel, which
// no hardcoded allow_targets/allow_stores could ever have expressed.
func TestPlanSurfaceAllowScopesChannel(t *testing.T) {
	r := scopeRig(t, `
connectors:
  slack: { use: slack, bot_token: x }
policy:
  agent_authored:
    allow: ["**"]
    allow_scopes:
      channel: ["#code-reviews"]
`)
	pol := r.planPolicy()
	t9 := newTrigger("ping", nil)
	if err := r.checkVerbResources(pol, t9, "slack.post",
		map[string]any{"channel": "#code-reviews", "text": "x"}, nil); err != nil {
		t.Fatalf("allow_scopes.channel must admit the channel it names: %v", err)
	}
	err := r.checkVerbResources(pol, t9, "slack.post",
		map[string]any{"channel": "#exec-private", "text": "x"}, nil)
	if err == nil || !strings.Contains(err.Error(), "allow_scopes.channel") {
		t.Fatalf("an unlisted channel must be refused on the plan surface, got %v", err)
	}
}

// BACK-COMPAT. allow_targets/allow_stores/allow_secrets are the legacy
// spellings of the repo/store/secret dimensions: an existing config behaves
// EXACTLY as the allow_scopes form it is an alias for, and mixing the two
// unions them rather than one silently winning.
func TestLegacyAllowListsAreDimensionAliases(t *testing.T) {
	legacy := scopeRig(t, `
connectors:
  svc: { use: fake }
stores:
  main: { type: boltdb }
policy:
  agent_authored:
    allow: ["**"]
    allow_targets: [ "friendly/*" ]
    allow_stores: [ main ]
`)
	modern := scopeRig(t, `
connectors:
  svc: { use: fake }
stores:
  main: { type: boltdb }
policy:
  agent_authored:
    allow: ["**"]
    allow_scopes:
      repo: [ "friendly/*" ]
      store: [ main ]
`)
	trig := newTrigger("ping", nil) // targets o/r
	cases := []struct {
		opts    map[string]any
		refused bool
		why     string
	}{
		{map[string]any{"repo": "friendly/box"}, false, "allow_targets names it"},
		{map[string]any{"repo": "o/r"}, false, "the triggering target is implicit"},
		{map[string]any{"repo": "hostile/box"}, true, "not listed, not the trigger's"},
		{map[string]any{"store": "main"}, false, "allow_stores names it"},
		{map[string]any{"store": "other"}, true, "not listed"},
	}
	for _, c := range cases {
		lerr := legacy.checkVerbResources(legacy.planPolicy(), trig, "svc.post", c.opts, nil)
		merr := modern.checkVerbResources(modern.planPolicy(), trig, "svc.post", c.opts, nil)
		if (lerr != nil) != c.refused {
			t.Errorf("legacy form: %v refused=%v, want %v (%s): %v", c.opts, lerr != nil, c.refused, c.why, lerr)
		}
		if (lerr != nil) != (merr != nil) {
			t.Errorf("legacy and allow_scopes disagree about %v: legacy=%v modern=%v", c.opts, lerr, merr)
		}
	}

	// Both spellings at once union; neither shadows the other.
	both := scopeRig(t, `
connectors:
  svc: { use: fake }
policy:
  agent_authored:
    allow: ["**"]
    allow_targets: [ "legacy/*" ]
    allow_scopes:
      repo: [ "modern/*" ]
`)
	for _, repo := range []string{"legacy/a", "modern/b"} {
		if err := both.checkVerbResources(both.planPolicy(), trig, "svc.post",
			map[string]any{"repo": repo}, nil); err != nil {
			t.Errorf("both spellings must apply together, %s was refused: %v", repo, err)
		}
	}
}

// Operator-authored `uses:` steps are NOT gated — the operator wrote the
// config with their own credential. Only agent-authored plans and skill
// grants are scoped, and that split must survive the generalization.
func TestConfigAuthoredStepsAreNotScoped(t *testing.T) {
	cfg := loadConfig(t, `
connectors:
  slack: { use: slack, bot_token: x }
policy:
  agent_authored:
    allow: ["**"]
    allow_scopes:
      channel: ["#code-reviews"]
`)
	reg := buildRegistry(t, cfg)
	rig := newTestRunner(t, cfg, reg)
	rig.Runner.DryRun = true
	spec := mustSpec(t, `
on: slack.app_mention
steps:
  - id: a
    uses: slack.post
    options: { channel: "#anywhere-the-operator-likes", text: hi }
`)
	runTrigger(rig, newTrigger("ping", nil), spec)
	if failed, errStr := rig.workflowFailed(); failed {
		t.Fatalf("a config-authored step must not be resource-scoped: %s", errStr)
	}
}
