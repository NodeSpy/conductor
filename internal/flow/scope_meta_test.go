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
	const outOfScope = "meta-test-out-of-scope"
	const allowed = "meta-test-allowed"

	// Every connector type this binary registers, instantiated. A type whose
	// connection config is incomplete builds DISABLED — it keeps its
	// declaration, which is all resource scoping reads, and the scope check
	// runs long before anything would be invoked.
	var b strings.Builder
	b.WriteString("connectors:\n")
	for _, typ := range connector.Types() {
		fmt.Fprintf(&b, "  %s: { use: %s }\n", typ, typ)
	}
	// A vault, too: its verbs carry the `secret` dimension and reach the
	// registry through `vaults:` rather than `connectors:`.
	fmt.Fprintf(&b, "vaults:\n  housevault: { type: file, dir: %s }\n", t.TempDir())
	// The policy that makes the allowlists apply at all (no trust: full), and
	// a per-dimension grant for the POSITIVE half. Dimensions are discovered
	// below, so every dimension any connector declares is listed here.
	b.WriteString("policy:\n  agent_authored:\n    allow: [\"**\"]\n    allow_scopes:\n")
	dims := map[string]bool{}
	for _, typ := range connector.Types() {
		if d, ok := connector.TypeDeclFor(typ); ok {
			for _, dim := range d.ScopeDims() {
				dims[dim] = true
			}
		}
	}
	dims[config.DimSecret] = true // vault verbs, registered per-instance
	for dim := range dims {
		fmt.Fprintf(&b, "      %s: [\"%s\"]\n", dim, allowed)
	}
	cfg := loadConfig(t, b.String())
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

	// A refusal BY THE RESOURCE CHECK, not by something downstream (a
	// disabled connector, a missing required option).
	scopeRefused := func(err error) bool {
		return err != nil && strings.Contains(err.Error(), "allow_scopes")
	}

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
	for _, f := range []struct{ file, what string }{
		{"skillverbs.go", "the skill surface (RunSkillVerb)"},
		{"flow.go", "the plan surface (execVerb / hooks)"},
	} {
		if !strings.Contains(readSource(t, f.file), "checkVerbResources(") {
			t.Errorf("%s no longer calls checkVerbResources — both surfaces must enforce "+
				"resource scoping from one function, or a grant means different things "+
				"depending on which surface the agent uses", f.what)
		}
	}
	// And the check itself must stay generic: an option name written into
	// either surface is the hardcoding this design removed.
	belt := readSource(t, "resources.go")
	if !strings.Contains(belt, "vd.ScopedOptions()") {
		t.Error("checkVerbResources no longer walks the verb's connector-declared scoped " +
			"options — it must not learn option names of its own")
	}
}
