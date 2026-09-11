package flow

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/connector"
	"github.com/NodeSpy/conductor/internal/core"
)

// The values every meta-test below judges: one no context and no list can
// reach, one an allowlist names.
const (
	metaOutOfScope = "meta-test-out-of-scope"
	metaAllowed    = "meta-test-allowed"
)

// everyConnectorYAML instantiates every connector type this binary
// registers, plus a vault (whose verbs carry the `secret` dimension and reach
// the registry through `vaults:` rather than `connectors:`). A type whose
// connection config is incomplete builds DISABLED — it keeps its
// declaration, which is all resource scoping reads, and the scope check runs
// long before anything would be invoked.
func everyConnectorYAML(t *testing.T) string {
	t.Helper()
	var b strings.Builder
	b.WriteString("connectors:\n")
	for _, typ := range connector.Types() {
		fmt.Fprintf(&b, "  %s: { use: %s }\n", typ, typ)
	}
	fmt.Fprintf(&b, "vaults:\n  housevault: { type: file, dir: %s }\n", t.TempDir())
	return b.String()
}

// everyScopeDim is every dimension any registered connector declares.
func everyScopeDim() []string {
	dims := map[string]bool{config.DimSecret: true} // vaults register per-instance
	for _, typ := range connector.Types() {
		if d, ok := connector.TypeDeclFor(typ); ok {
			for _, dim := range d.ScopeDims() {
				dims[dim] = true
			}
		}
	}
	out := make([]string, 0, len(dims))
	for dim := range dims {
		out = append(out, dim)
	}
	sort.Strings(out)
	return out
}

// eachScopedOption calls fn for every scope-tagged option of every verb of
// every connector on the registry, and fails if there are none — a meta-test
// that enumerates nothing passes vacuously forever.
func eachScopedOption(t *testing.T, r *Runner, fn func(uses string, so connector.ScopedOption)) {
	t.Helper()
	checked := 0
	for _, connName := range r.Conns.Names() {
		// conductor.*/workflow.* are never served on either agent surface.
		if connName == "workflow" || connName == "conductor" {
			continue
		}
		in, ok := r.Conns.Get(connName)
		if !ok || in.Decl == nil {
			continue
		}
		for _, vd := range in.Decl.Verbs {
			for _, so := range vd.ScopedOptions() {
				checked++
				fn(connName+"."+vd.Name, so)
			}
		}
	}
	if checked == 0 {
		t.Fatal("enumerated NO scope-tagged options — either every connector lost its Scope tags " +
			"or ScopedOptions stopped reporting them; this test would pass vacuously from here on")
	}
}

// scopeRefused reports a refusal BY THE RESOURCE CHECK, not by something
// downstream (a disabled connector, a missing required option).
func scopeRefused(err error) bool {
	return err != nil && strings.Contains(err.Error(), "allow_scopes")
}

// THE META-TEST (docs/design/skill-verb-scope.md). Verb ACCESS and resource
// SCOPE are orthogonal: `skill.verbs: [slack.post]` says the agent may post,
// not WHICH channel it may post to. Conductor used to answer the second
// question with two hardcoded option names (repo, store), so every connector
// that invented its own resource dimension — slack's channel, blob's path,
// a future jira project or s3 bucket — had nowhere to be enforced, and the
// grant quietly meant "anywhere the token reaches".
//
// The fix is connector-declared (Field.Scope) and walked generically by one
// shared check. This test is what keeps it that way: for EVERY connector on
// the daemon it enumerates every verb's scope-tagged options and asserts that
// BOTH agent-facing surfaces
//
//	refuse an out-of-context value that is not allow-listed, and
//	admit one that is
//
// so a connector that adds a scoped option which either surface forgets to
// enforce fails HERE, rather than shipping as an open door. The second half
// matters as much as the first: a surface that refused everything would
// otherwise satisfy this test while breaking every legitimate call.
func TestEveryScopedOptionIsEnforcedOnBothSurfaces(t *testing.T) {
	const outOfScope = metaOutOfScope
	const allowed = metaAllowed

	// Every connector type this binary registers, plus a policy that makes
	// the allowlists apply (no trust: full) and lists every dimension for the
	// POSITIVE half.
	var pb strings.Builder
	pb.WriteString("policy:\n  agent_authored:\n    allow: [\"**\"]\n    allow_scopes:\n")
	for _, dim := range everyScopeDim() {
		fmt.Fprintf(&pb, "      %s: [\"%s\"]\n", dim, allowed)
	}
	cfg := loadConfig(t, everyConnectorYAML(t)+pb.String())
	reg := buildRegistry(t, cfg)
	rig := newTestRunner(t, cfg, reg)
	r := rig.Runner
	// Nothing in this test may reach a real service: the scope check happens
	// before dispatch, and an admitted call stubs out.
	r.DryRun = true

	// The dispatch every call is judged against. Its own repo is
	// "trigger/repo" and its own slack channel is "#trigger-channel" — the
	// values a grant does NOT have to name.
	trig := core.Trigger{
		Source: "github", Instance: "gh", Kind: "review_requested",
		Target:  core.Target{Repo: "trigger/repo", Number: 7},
		Context: map[string]any{"slack": map[string]any{"channel": "#trigger-channel", "user": "U-trigger"}},
	}
	pol := cfg.Policy.AgentAuthored

	checked := 0
	perConn := map[string]int{}
	for _, connName := range reg.Names() {
		// conductor.*/workflow.* are never served on either agent surface.
		if connName == "workflow" || connName == "conductor" {
			continue
		}
		in, ok := reg.Get(connName)
		if !ok || in.Decl == nil {
			continue
		}
		for _, vd := range in.Decl.Verbs {
			uses := connName + "." + vd.Name
			for _, so := range vd.ScopedOptions() {
				checked++
				perConn[connName]++
				// The grant an operator would write for this dispatch: the
				// verb, with no per-option widening.
				id := SkillIdentity{
					Agent: "probe", Verbs: []string{connName + ".*"},
					Repo: trig.Target.Repo, Number: 7, Context: trig.Context,
				}
				for _, tc := range []struct {
					name    string
					value   string
					refused bool
				}{
					{"out of context and not listed", outOfScope, true},
					{"allow-listed", allowed, false},
				} {
					opts := map[string]any{so.Name: tc.value}
					where := fmt.Sprintf("%s option %q (dimension %q), %s", uses, so.Name, so.Dim, tc.name)

					planErr := r.checkVerbResources(pol, trig, uses, opts, nil)
					if got := scopeRefused(planErr); got != tc.refused {
						t.Errorf("PLAN surface: %s: refused=%v, want %v (err=%v)", where, got, tc.refused, planErr)
					}

					_, skillErr := r.RunSkillVerb(context.Background(), id, uses, opts)
					if got := scopeRefused(skillErr); got != tc.refused {
						t.Errorf("SKILL surface: %s: refused=%v, want %v (err=%v)", where, got, tc.refused, skillErr)
					}
				}
			}
		}
	}
	if checked == 0 {
		t.Fatal("enumerated NO scope-tagged options — either every connector lost its Scope tags " +
			"or ScopedOptions stopped reporting them; this test would pass vacuously from here on")
	}
	names := make([]string, 0, len(perConn))
	for n := range perConn {
		names = append(names, fmt.Sprintf("%s=%d", n, perConn[n]))
	}
	sort.Strings(names)
	t.Logf("checked %d scope-tagged options across both surfaces: %s", checked, strings.Join(names, " "))
}

// META-TEST, THE SECOND AXIS (round-4 F3). The test above always built its
// policy from `cfg.Policy.AgentAuthored`, so every case it ran had a
// policy.agent_authored block — and that is exactly the assumption the bug
// lived under. RunSkillVerb enforced through the PLAN policy, which resolves
// to nil when there is no such block (and under trust: full), and a nil
// policy skipped the walk entirely: a grant spelling out
// `slack.post: {channel: ["#x"]}` did NO scoping at all, and the agent posted
// wherever the token reached.
//
// The class is "skill-grant enforcement coupled to a different surface's
// config". So this enumerates the same options across the POLICY SHAPES a
// config can have, and asserts the skill surface refuses an out-of-context,
// unlisted value in every one of them — a nil policy anywhere in the chain
// can no longer turn the grant off.
func TestSkillGrantScopesUnderEveryPolicyShape(t *testing.T) {
	shapes := []struct {
		name    string
		policy  string
		widens  bool // does allow_scopes widen under this shape?
		because string
	}{
		{
			name: "no policy block at all", policy: "",
			widens:  false,
			because: "the grant is the operator's sentence about this agent; a block governing a different surface is not what switches it on",
		},
		{
			name: "policy: with no agent_authored", policy: "policy:\n  rate_limits: { per_minute: 10 }\n",
			widens:  false,
			because: "same: agent_authored is absent, the grant still means what it says",
		},
		{
			name: "trust: full", policy: "policy:\n  agent_authored:\n    trust: full\n",
			widens:  false,
			because: "trust: full is plan latitude — it does not lift a constraint the operator wrote onto a named verb",
		},
		{
			name:   "a policy that widens",
			policy: "policy:\n  agent_authored:\n    allow: [\"**\"]\n    allow_scopes:\n",
			widens: true, because: "allow_scopes is the operator's own widening, and it applies",
		},
	}
	for _, shape := range shapes {
		t.Run(shape.name, func(t *testing.T) {
			pol := shape.policy
			if shape.widens {
				for _, dim := range everyScopeDim() {
					pol += fmt.Sprintf("      %s: [\"%s\"]\n", dim, metaAllowed)
				}
			}
			cfg := loadConfig(t, everyConnectorYAML(t)+pol)
			r := newTestRunner(t, cfg, buildRegistry(t, cfg)).Runner
			r.DryRun = true
			trigCtx := map[string]any{"slack": map[string]any{"channel": "#trigger-channel", "user": "U-trigger"}}

			eachScopedOption(t, r, func(uses string, so connector.ScopedOption) {
				connName, _, _ := strings.Cut(uses, ".")
				id := SkillIdentity{
					Agent: "probe", Verbs: []string{connName + ".*"},
					Repo: "trigger/repo", Number: 7, Context: trigCtx,
				}
				call := func(value string, grant map[string]map[string][]string) error {
					id := id
					id.Scopes = grant
					_, err := r.RunSkillVerb(context.Background(), id, uses, map[string]any{so.Name: value})
					return err
				}

				// THE REGRESSION: out of context, not in the grant, not
				// listed anywhere — refused under every shape.
				if err := call(metaOutOfScope, nil); !scopeRefused(err) {
					t.Errorf("%s option %q (%q): NOT refused with %s — %s (err=%v)",
						uses, so.Name, so.Dim, shape.name, shape.because, err)
				}
				// The grant's own list is intrinsic too: it must WIDEN under
				// every shape, or scoping would be unusable without a policy.
				granted := map[string]map[string][]string{
					connName + ".*": {so.Name: {metaOutOfScope}},
				}
				if err := call(metaOutOfScope, granted); scopeRefused(err) {
					t.Errorf("%s option %q (%q): the grant's own list did not widen under %s (err=%v)",
						uses, so.Name, so.Dim, shape.name, err)
				}
				// And the policy's allow_scopes widens when there is one.
				if shape.widens {
					if err := call(metaAllowed, nil); scopeRefused(err) {
						t.Errorf("%s option %q (%q): allow_scopes did not widen under %s (err=%v)",
							uses, so.Name, so.Dim, shape.name, err)
					}
				}
			})
		})
	}
}

// The plan surface's nil-policy path is MOOT, and this is what makes saying so
// legitimate: an agent-authored plan cannot run at all without a
// policy.agent_authored block, so there is no unguarded plan for
// planResourcePolicy's nil to wave through. If this ever stops holding, the
// plan surface needs the same deny-by-default the skill surface now has, and
// this test is where that gets noticed.
func TestAgentAuthoredPlansNeedAPolicy(t *testing.T) {
	cfg := loadConfig(t, "connectors:\n  svc: { use: fake }\n")
	reg := buildRegistry(t, cfg)
	steps := []config.Step{{Uses: "svc.post", Options: map[string]any{"text": "hi", "repo": "victim/repo"}}}
	_, err := guardPlan(cfg, reg, nil /* no policy.agent_authored */, steps)
	if err == nil {
		t.Fatal("an agent-authored plan ran with NO policy.agent_authored block — the plan surface's " +
			"nil-policy path is only safe because the plan is rejected first; it now needs its own " +
			"deny-by-default resource scoping")
	}
	if !strings.Contains(err.Error(), "agent-authored plans are disabled") {
		t.Fatalf("unexpected rejection reason (the nil-policy reasoning depends on this one): %v", err)
	}
}

// The dispatch's OWN resource needs no grant — on both surfaces, for every
// dimension a connector can answer. This is the other half of deny-by-default:
// scoping must not break "act on the PR you were dispatched for" or "reply in
// the channel that summoned you", which is what would push an operator to
// write allow_scopes: {"*": ["*"]} and lose the whole mechanism.
func TestDispatchOwnResourceNeedsNoGrant(t *testing.T) {
	cfg := loadConfig(t, `
connectors:
  svc: { use: fake }
  slack: { use: slack, bot_token: x }
policy:
  agent_authored:
    allow: ["**"]
`)
	reg := buildRegistry(t, cfg)
	r := newTestRunner(t, cfg, reg).Runner
	r.DryRun = true
	trig := core.Trigger{
		Source: "slack", Instance: "slack", Kind: "app_mention",
		Target:  core.Target{Repo: "trigger/repo"},
		Context: map[string]any{"slack": map[string]any{"channel": "#trigger-channel"}},
	}
	pol := cfg.Policy.AgentAuthored

	for _, tc := range []struct{ uses, opt, value string }{
		{"svc.post", "repo", "trigger/repo"},           // the dispatch's own target
		{"slack.post", "channel", "#trigger-channel"},  // the channel it came from
		{"slack.react", "channel", "#trigger-channel"}, // …on every verb, not just post
	} {
		opts := map[string]any{tc.opt: tc.value}
		if err := r.checkVerbResources(pol, trig, tc.uses, opts, nil); err != nil {
			t.Errorf("PLAN surface refused the dispatch's own %s: %v", tc.opt, err)
		}
		id := SkillIdentity{
			Agent: "probe", Verbs: []string{"svc.*", "slack.*"},
			Repo: "trigger/repo", Context: trig.Context,
		}
		if _, err := r.RunSkillVerb(context.Background(), id, tc.uses, opts); err != nil &&
			strings.Contains(err.Error(), "allow_scopes") {
			t.Errorf("SKILL surface refused the dispatch's own %s: %v", tc.opt, err)
		}
	}
}

// Both surfaces must call the SHARED check. A private reimplementation on
// either side is how the two drifted apart in the first place — and a
// refactor that stops calling it would leave every test above green while the
// door stands open.
func TestBothSurfacesCallTheSharedScopeCheck(t *testing.T) {
	for _, f := range []struct{ file, call, what string }{
		{"skillverbs.go", "checkSkillVerbResources(", "the skill surface (RunSkillVerb)"},
		{"flow.go", "checkVerbResources(", "the plan surface (execVerb / hooks)"},
	} {
		if !strings.Contains(readSource(t, f.file), f.call) {
			t.Errorf("%s no longer calls %s — both surfaces must enforce resource scoping "+
				"from one walk, or a grant means different things depending on which "+
				"surface the agent uses", f.what, f.call)
		}
	}
	belt := readSource(t, "resources.go")
	// The two entry points differ ONLY in the policy they resolve; both must
	// funnel into the shared walk, or they are two checks wearing one name.
	if n := strings.Count(belt, "r.checkVerbScopes("); n < 2 {
		t.Errorf("the surface entry points no longer both delegate to checkVerbScopes (found %d) — "+
			"whichever one stopped is now enforcing on its own and will drift", n)
	}
	// The skill entry must build the never-nil policy. Routing it through
	// planResourcePolicy is precisely the round-4 F3 bug: a config with no
	// policy.agent_authored block (or trust: full) turned the grant off.
	if !strings.Contains(belt, "skillResourcePolicy(pol, t)") {
		t.Error("checkSkillVerbResources no longer builds skillResourcePolicy — the skill " +
			"surface must not resolve its scoping through the PLAN policy, which is nil " +
			"without a policy.agent_authored block and under trust: full")
	}
	// And the walk itself must stay generic: an option name written into
	// either surface is the hardcoding this design removed.
	if !strings.Contains(belt, "vd.ScopedOptions()") {
		t.Error("the walk no longer reads the verb's connector-declared scoped options — " +
			"it must not learn option names of its own")
	}
}
