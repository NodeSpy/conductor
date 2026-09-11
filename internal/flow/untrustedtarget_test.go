package flow

import (
	"context"
	"strings"
	"testing"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/controller"
	"github.com/NodeSpy/conductor/internal/core"
	"github.com/NodeSpy/conductor/internal/dispatch"
	"github.com/NodeSpy/conductor/internal/memory"
)

// ROUND-7 #3. A webhook source's `repo:` renders from the POST BODY — the
// only data it has — so an operator writing the documented
// `repo: "{{.body.owner}}/{{.body.name}}"` lets whoever sends the request
// choose the repo the dispatch claims to be for.
//
// The scope layer's most basic rule is "your own target needs no grant". For
// such a dispatch there is no own: the attacker names it, and the rule would
// hand them scope for any repo they typed.
func TestAForgedTargetGetsNoImplicitOwnScope(t *testing.T) {
	r := scopeRig(t, `
connectors:
  svc: { use: fake }
policy:
  agent_authored:
    allow: ["**"]
    allow_scopes:
      repo: ["listed/repo"]
`)
	pol := r.planPolicy()

	// The same dispatch, twice: once with a platform-assigned target, once
	// with one the request body chose.
	for _, tc := range []struct {
		name      string
		untrusted bool
		ownOK     bool
	}{
		{"a platform-assigned target", false, true},
		{"a target the request body chose", true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			trig := core.Trigger{
				Kind: "delivery", Source: "webhook",
				Target:        core.Target{Repo: "victim/secrets", Owner: "victim", Name: "secrets", Number: 7},
				TargetTrusted: !tc.untrusted,
			}
			// PLAN surface.
			err := r.checkVerbResources(pol, trig, "svc.post", map[string]any{"repo": "victim/secrets"}, nil)
			if refused := scopeRefused(err); refused == tc.ownOK {
				t.Errorf("PLAN surface: refused=%v for %s (err=%v)", refused, tc.name, err)
			}
			// SKILL surface.
			id := SkillIdentity{
				Agent: "probe", Repo: "victim/secrets", Number: 7,
				Verbs: []string{"svc.*"}, TargetTrusted: !tc.untrusted,
			}
			_, serr := r.RunSkillVerb(context.Background(), id, "svc.post",
				map[string]any{"repo": "victim/secrets", "text": "x"})
			if refused := scopeRefused(serr); refused == tc.ownOK {
				t.Errorf("SKILL surface: refused=%v for %s (err=%v)", refused, tc.name, serr)
			}
			// What the operator LISTED still works either way — scoping such a
			// dispatch is possible, it just has to be explicit.
			if err := r.checkVerbResources(pol, trig, "svc.post",
				map[string]any{"repo": "listed/repo"}, nil); err != nil {
				t.Errorf("an explicitly listed repo must work for %s: %v", tc.name, err)
			}
		})
	}
}

// The render facts go with it: a `{{ }}` entry built from a forged target
// renders empty and (fail-closed) matches nothing.
func TestForgedTargetFactsDoNotRender(t *testing.T) {
	r := scopeRig(t, scopeBaseCfg)
	trig := core.Trigger{
		Kind:          "delivery",
		Target:        core.Target{Repo: "victim/secrets", Owner: "victim", Name: "secrets", Number: 7},
		TargetTrusted: false, // the sender chose this target
	}
	data := r.scopeRenderData(trig)
	for _, k := range []string{"repo", "owner", "name"} {
		if s, _ := data[k].(string); s != "" {
			t.Errorf("fact %q rendered %q from a target the sender chose", k, s)
		}
	}
	if n, _ := data["number"].(int); n != 0 {
		t.Errorf("number rendered %d from a target the sender chose", n)
	}
	// `kind` is the source's own event name, from the config, and survives.
	if data["kind"] != "delivery" {
		t.Errorf("kind must survive — it is the operator's own: %v", data["kind"])
	}

	// End to end: the entry matches nothing.
	id := SkillIdentity{
		Agent: "probe", Repo: "victim/secrets", Verbs: []string{"svc.*"},
		// the sender chose this target: TargetTrusted stays false
		Scopes: map[string]map[string][]string{"svc.*": {"repo": {"{{.repo}}"}}},
	}
	_, err := r.RunSkillVerb(context.Background(), id, "svc.post",
		map[string]any{"repo": "victim/secrets", "text": "x"})
	if !scopeRefused(err) {
		t.Fatalf("a {{.repo}} entry must render empty for a forged target, got %v", err)
	}
}

// The memory scope's own-scope anchor rides the same decision, so a forged
// target does not become a memory tenant either.
func TestForgedTargetHasNoOwnMemoryScope(t *testing.T) {
	cfg := loadConfig(t, `
connectors:
  svc: { use: fake }
memory:
  type: memory
policy:
  agent_authored:
    allow: ["**"]
`)
	installTestMemory(t, cfg)
	// No DryRun here: the memory verb has to actually reach CheckOp, and the
	// in-process memory connector is safe to invoke.
	r := newTestRunner(t, cfg, buildRegistry(t, cfg)).Runner
	id := SkillIdentity{
		Agent: "probe", Repo: "victim/secrets", Verbs: []string{"memory.*"},
		// the sender chose this target: TargetTrusted stays false
	}
	_, err := r.RunSkillVerb(context.Background(), id, "memory.remember",
		map[string]any{"text": "x", "scope": "repo:victim/secrets"})
	if err == nil || !strings.Contains(err.Error(), "allow_memory_scopes") {
		t.Fatalf("a forged target must not own a memory scope, got %v", err)
	}
	// The same dispatch with a platform-assigned target owns its scope, so
	// the assertion above cannot pass by refusing everything.
	trusted := id
	trusted.TargetTrusted = true
	if _, err := r.RunSkillVerb(context.Background(), trusted, "memory.remember",
		map[string]any{"text": "x", "scope": "repo:victim/secrets"}); err != nil {
		t.Fatalf("a real target must still own its memory scope: %v", err)
	}
}

// ROUND-8 #2. run_step rebuilds a trigger from the launching dispatch's baked-in
// provenance. It rebuilt everything BUT the target's provenance, so a
// run_step invoked under a forged-target dispatch regained the own-repo and
// own-memory-scope trust the dispatch itself had been denied — the guard was
// there, and the reconstruction walked around it.
func TestRunLiveStepCarriesTargetProvenance(t *testing.T) {
	for _, tc := range []struct {
		name    string
		trusted bool
	}{
		{"a platform-assigned target", true},
		{"a target the request body chose", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := loadConfig(t, `
connectors:
  svc: { use: fake }
policy:
  agent_authored:
    allow: ["**"]
`)
			rig := newTestRunner(t, cfg, buildRegistry(t, cfg))
			src := memory.Source{Step: "probe", Trigger: "delivery",
				Repo: "victim/secrets", TargetTrusted: tc.trusted}
			_, err := rig.Runner.RunLiveStep(context.Background(), src, 7, map[string]any{
				"uses": "svc.post", "options": map[string]any{"repo": "victim/secrets", "text": "x"},
			})
			refused := err != nil && strings.Contains(err.Error(), "allow_scopes.repo")
			if refused == tc.trusted {
				t.Fatalf("run_step under %s: refused=%v (err=%v)", tc.name, refused, err)
			}
		})
	}
}

// ROUND-11 #2. run_step rebuilds a trigger with the literals Source="live",
// Instance="live", and the IPC handler calls it on a BARE context (so there
// is no history recorder either). For an UNTRUSTED target that left
// agentAuthoredNamespace with nothing: OwnRepo() is "", histFrom is nil, and
// the fallback is "agent:live:live:<kind>" — IDENTICAL for every run_step on
// the daemon. Two different dispatches' agent-authored steps therefore shared
// one session-binding namespace, which is the round-10 #2 class reopened on
// the one path that had no repo to be confined by.
//
// The anchor is now the daemon-assigned dispatch id, carried on the
// provenance the live tool was handed.
func TestRunStepNamespacesPerLaunchingDispatch(t *testing.T) {
	ns := func(dispatchID string) string {
		// The trigger RunLiveStep builds: untrusted target, literal source.
		trig := core.Trigger{
			Source: "live", Instance: "live", Kind: "delivery",
			Target:     core.Target{Repo: "victim/repo", Number: 7},
			DispatchID: dispatchID,
		}
		return stepIdentity(markAgentAuthored(context.Background(), trig),
			config.Step{Type: "agent", Name: "review"}, "steps[0]")
	}
	a, b := ns("run-abc:step1"), ns("run-xyz:step1")
	if a == b {
		t.Fatalf("two launching dispatches share an agent-authored namespace (%q) — one "+
			"run_step's step can address the other's live session", a)
	}
	// The forged repo must not appear in either: it is not the wall.
	for _, got := range []string{a, b} {
		if strings.Contains(got, "victim/repo") {
			t.Errorf("the namespace was built from a repo the sender chose: %q", got)
		}
	}
	// Two steps of ONE launching dispatch still share, so continuity within a
	// run_step plan — the legitimate use — survives.
	if ns("run-abc:step1") != a {
		t.Error("two steps of one dispatch must share a namespace")
	}
	// And the binding keys they produce differ, which is what the affinity
	// registry actually matches on.
	if controller.StepSessionKey(a, "shared") == controller.StepSessionKey(b, "shared") {
		t.Fatal("the session binding keys collide across launching dispatches")
	}
}

// The anchor must reach RunLiveStep from the provenance the tool was handed,
// not be re-derived: the IPC handler calls it on a bare context precisely
// because it has no run of its own.
func TestRunLiveStepCarriesTheDispatchAnchor(t *testing.T) {
	cfg := loadConfig(t, `
connectors:
  svc: { use: fake }
policy:
  agent_authored:
    allow: ["**"]
`)
	rig := newTestRunner(t, cfg, buildRegistry(t, cfg))
	var seen []string
	rig.Agents.dispatchFunc = func(ctx context.Context, req dispatch.Request) (dispatch.RunRef, error) {
		seen = append(seen, req.Identity)
		return dispatch.RunRef{AgentID: "a", Output: "done"}, nil
	}
	run := func(dispatchID string) {
		src := memory.Source{Step: "probe", Trigger: "delivery", Repo: "victim/repo", Dispatch: dispatchID}
		_, _ = rig.Runner.RunLiveStep(context.Background(), src, 7,
			map[string]any{"type": "agent", "name": "review", "model": "m", "prompt": "p"})
	}
	run("run-abc:step1")
	run("run-xyz:step1")
	if len(seen) != 2 {
		t.Fatalf("expected two dispatches, got %v", seen)
	}
	if seen[0] == seen[1] {
		t.Fatalf("two run_step launches produced the same agent identity %q — their sessions "+
			"would bind to one key", seen[0])
	}
	for _, id := range seen {
		if !strings.Contains(id, "agent:dispatch:") {
			t.Errorf("the identity must be confined to the launching dispatch, got %q", id)
		}
	}
}
