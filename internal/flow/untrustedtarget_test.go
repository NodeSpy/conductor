package flow

import (
	"context"
	"strings"
	"testing"

	"github.com/NodeSpy/conductor/internal/core"
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
				Target:          core.Target{Repo: "victim/secrets", Owner: "victim", Name: "secrets", Number: 7},
				TargetUntrusted: tc.untrusted,
			}
			// PLAN surface.
			err := r.checkVerbResources(pol, trig, "svc.post", map[string]any{"repo": "victim/secrets"}, nil)
			if refused := scopeRefused(err); refused == tc.ownOK {
				t.Errorf("PLAN surface: refused=%v for %s (err=%v)", refused, tc.name, err)
			}
			// SKILL surface.
			id := SkillIdentity{
				Agent: "probe", Repo: "victim/secrets", Number: 7,
				Verbs: []string{"svc.*"}, TargetUntrusted: tc.untrusted,
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
		Kind:            "delivery",
		Target:          core.Target{Repo: "victim/secrets", Owner: "victim", Name: "secrets", Number: 7},
		TargetUntrusted: true,
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
		TargetUntrusted: true,
		Scopes:          map[string]map[string][]string{"svc.*": {"repo": {"{{.repo}}"}}},
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
		TargetUntrusted: true,
	}
	_, err := r.RunSkillVerb(context.Background(), id, "memory.remember",
		map[string]any{"text": "x", "scope": "repo:victim/secrets"})
	if err == nil || !strings.Contains(err.Error(), "allow_memory_scopes") {
		t.Fatalf("a forged target must not own a memory scope, got %v", err)
	}
	// The same dispatch with a platform-assigned target owns its scope, so
	// the assertion above cannot pass by refusing everything.
	trusted := id
	trusted.TargetUntrusted = false
	if _, err := r.RunSkillVerb(context.Background(), trusted, "memory.remember",
		map[string]any{"text": "x", "scope": "repo:victim/secrets"}); err != nil {
		t.Fatalf("a real target must still own its memory scope: %v", err)
	}
}
