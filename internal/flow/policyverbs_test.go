package flow

import (
	"strings"
	"testing"

	"github.com/NodeSpy/conductor/internal/core"
)

// THE RENAME'S ENFORCEMENT CONTRACT (docs/design/config-surface-refinements.md
// §1&2): the plan surface's `verbs:` map gates per-verb resource scope exactly
// as the flat `allow_scopes:` it replaces did — a value outside the list is
// denied, a value inside is allowed — and, unlike the flat map, a scope
// written under one verb does NOT apply to another.
//
// Every dimension the old flat map could name is covered: repo, channel,
// store, and memory's scope.
func TestPolicyVerbsMapEnforcesPerVerbScope(t *testing.T) {
	r := scopeRig(t, `
connectors:
  slack: { use: slack, bot_token: x }
  svc:   { use: fake }
  other: { use: fake }
policy:
  agent_authored:
    verbs:
      slack.post:       { channel: ["#code-reviews"] }
      svc.post:         { repo: ["acme/docs"] }
      kv.*:             { store: [cache] }
stores:
  cache: { type: boltdb }
`)
	pol := r.planPolicy()
	trig := newTrigger("ping", nil)

	for _, tc := range []struct {
		name, uses string
		opts       map[string]any
		denied     bool
		why        string
	}{
		{"the channel its verb names", "slack.post",
			map[string]any{"channel": "#code-reviews", "text": "x"}, false,
			"an in-list value is admitted, as allow_scopes.channel admitted it"},
		{"a channel nobody listed", "slack.post",
			map[string]any{"channel": "#exec-private", "text": "x"}, true,
			"deny-by-default still holds after the rename"},
		{"the repo its verb names", "svc.post",
			map[string]any{"repo": "acme/docs", "text": "x"}, false, ""},
		{"a repo nobody listed", "svc.post",
			map[string]any{"repo": "acme/secrets", "text": "x"}, true, ""},
		{"the store its verb names", "kv.get",
			map[string]any{"store": "cache", "key": "k"}, false,
			"kv.* covers kv.get"},
		{"a store nobody listed", "kv.get",
			map[string]any{"store": "someone-elses", "key": "k"}, true, ""},

		// THE PER-VERB PROPERTY. Under the old flat `allow_scopes: {repo:
		// [acme/docs]}` this was ALLOWED: one repo list applied to every verb
		// that had a repo: option. Now svc.post's repo scope is svc.post's.
		{"another verb's repo scope does not leak", "other.post",
			map[string]any{"repo": "acme/docs", "text": "x"}, true,
			"acme/docs is scoped to svc.post — other.post was never granted it, " +
				"though the flat allow_scopes this replaces would have admitted it"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := r.checkVerbResources(pol, trig, tc.uses, tc.opts, nil)
			if got := err != nil; got != tc.denied {
				t.Fatalf("denied=%v want %v (err=%v) — %s", got, tc.denied, err, tc.why)
			}
			if tc.denied && !scopeRefused(err) {
				t.Fatalf("a denial must name the config path to edit, got %v", err)
			}
		})
	}
}

// `code: {store: […], scope: […]}` is where a run:code step's ctx.kv / ctx.sql
// / ctx.memory reach now lives. A code step names no verb, so before the
// rename these came from the policy-wide allow_stores + allow_memory_scopes;
// they are now the `code` step class's own entry, and the DataGuard reads them
// from there.
func TestCodeClassScopesGateCtxData(t *testing.T) {
	rig := scopeRig(t, `
connectors:
  svc: { use: fake }
policy:
  agent_authored:
    verbs:
      code: { store: [cache], scope: ["repo:acme/shared"] }
stores:
  cache:  { type: boltdb }
  hidden: { type: boltdb }
`)
	// planResourcePolicy resolves the code class's entry into the two lists
	// the DataGuard consults.
	rp := planResourcePolicy(rig.planPolicy(), core.Trigger{
		TargetTrusted: true, Source: "github",
		Target: core.Target{Repo: "acme/app", Number: 1},
	})
	if rp == nil {
		t.Fatal("a policy without trust: full must produce a resource policy")
	}
	for _, tc := range []struct {
		name  string
		ok    bool
		check func() bool
		why   string
	}{
		{"a store the code entry names", true,
			func() bool { return rp.storeOK("cache") }, "verbs.code.store lists it"},
		{"a store it does not", false,
			func() bool { return rp.storeOK("hidden") },
			"deny-by-default: ctx.kv may not reach an unlisted store"},
		{"the memory scope the code entry names", true,
			func() bool { return rp.memoryScopeOK("repo:acme/shared") },
			"verbs.code.scope lists it"},
		{"this dispatch's OWN memory scope", true,
			func() bool { return rp.memoryScopeOK("repo:acme/app") },
			"own scope is implicit and must survive the rename"},
		{"someone else's memory scope", false,
			func() bool { return rp.memoryScopeOK("repo:other/tenant") },
			"the cross-tenant read this closes"},
		{"an UNSCOPED memory op", false,
			func() bool { return rp.memoryScopeOK("") },
			"a recall naming no scope would read every tenant's entries"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.check(); got != tc.ok {
				t.Fatalf("allowed=%v want %v — %s", got, tc.ok, tc.why)
			}
		})
	}
}

// A `verbs:` entry under one verb must not become a code step's reach, and
// vice versa: the two are different keys and mean different things.
func TestCodeScopesAndVerbScopesStaySeparate(t *testing.T) {
	rig := scopeRig(t, `
connectors:
  svc: { use: fake }
policy:
  agent_authored:
    verbs:
      kv.*: { store: [verb-only] }
      code: { store: [code-only] }
stores:
  verb-only: { type: boltdb }
  code-only: { type: boltdb }
`)
	pol := rig.planPolicy()
	trig := newTrigger("ping", nil)
	rp := planResourcePolicy(pol, trig)

	if rp.storeOK("verb-only") {
		t.Error("kv.*'s store grant must not widen a code step's ctx.kv reach")
	}
	if !rp.storeOK("code-only") {
		t.Error("the code class's own store grant must apply to ctx.kv")
	}
	if err := rig.checkVerbResources(pol, trig, "kv.get",
		map[string]any{"store": "code-only", "key": "k"}, nil); err == nil {
		t.Error("the code class's store grant must not widen the kv.get VERB")
	}
	if err := rig.checkVerbResources(pol, trig, "kv.get",
		map[string]any{"store": "verb-only", "key": "k"}, nil); err != nil {
		t.Errorf("kv.* keeps its own store grant: %v", err)
	}
}

// An entry may be keyed by the option's own NAME or by the scope DIMENSION the
// connector declares on it. They are not always the same word — slack's
// `channel_id` option carries dimension `channel`, a vault's `key` carries
// `secret` — and an `allow_scopes: {channel: […]}` line that stopped applying
// to `channel_id` after the rename would be a scope silently switching off.
func TestVerbScopeKeyMayBeOptionNameOrDimension(t *testing.T) {
	// notifiarr.notify's option is `channel_id`, carrying dimension `channel`.
	r := scopeRig(t, `
connectors:
  nf: { use: notifiarr, api_key: k }
policy:
  agent_authored:
    verbs:
      nf.notify: { channel: ["42"] }
`)
	pol := r.planPolicy()
	trig := newTrigger("ping", nil)
	if err := r.checkVerbResources(pol, trig, "nf.notify",
		map[string]any{"channel_id": "42", "text": "x"}, nil); err != nil {
		t.Fatalf("a DIMENSION-keyed entry must reach the option carrying that dimension: %v", err)
	}
	if err := r.checkVerbResources(pol, trig, "nf.notify",
		map[string]any{"channel_id": "99", "text": "x"}, nil); err == nil {
		t.Fatal("…and must still deny everything it does not name")
	} else if !strings.Contains(err.Error(), "channel_id") {
		t.Fatalf("the denial should name the option the agent actually wrote: %v", err)
	}
}
