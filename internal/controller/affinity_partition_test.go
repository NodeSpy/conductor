package controller

import (
	"context"
	"testing"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/core"
	"github.com/NodeSpy/conductor/internal/dispatch"
)

// Session affinity is keyed by (runtime, resolvedModel, key)
// (docs/design/agents-removal.md §3). These tests pin the two structural
// dimensions and the two declaration scopes.

// partRig builds an affinity registry over a config with a runtime-level
// session pool and whatever steps the test needs.
func partRig(t *testing.T, cfg *config.Config) (*Affinity, *affRunner, *memAffStore) {
	t.Helper()
	runner := &affRunner{}
	st := newMemAffStore()
	reg := NewRegistry(cfg.MergedControllers(), cfg.DefaultRuntimeName(), runner, &affSender{})
	return NewAffinity(reg, st, cfg, nil, nil, nil), runner, st
}

func partReq(step config.Step, identity, model, kind string, pr int) dispatch.Request {
	return dispatch.Request{
		Trigger: core.Trigger{
			Source: "github", Instance: "gh", Kind: kind,
			Target: core.Target{Repo: "o/r", PR: pr, Number: pr},
		},
		Action:   config.Action{Type: "agent", Prompt: "handle {{.repo}}#{{.pr}}"},
		Step:     step,
		Identity: identity,
		Model:    model,
	}
}

var partKey = &config.SessionSpec{Key: "{{.repo}}#{{.pr}}"}

// The MODEL partitions affinity: the same key on two models is two agents.
// This is what makes a pack's fleet assignment shape affinity for free.
func TestModelPartitionsAffinity(t *testing.T) {
	cfg := &config.Config{Runtimes: config.RuntimeSet{"paseo": {Use: "paseo", Session: partKey, Default: true}}}
	aff, runner, _ := partRig(t, cfg)

	for _, model := range []string{"claude-opus-5", "claude-haiku-4-5"} {
		req := partReq(config.Step{}, "review", model, "new_comment", 7)
		if _, handled, err := aff.Dispatch(context.Background(), runner, req); err != nil || !handled {
			t.Fatalf("model %s: handled=%v err=%v", model, handled, err)
		}
	}
	if n := runner.dispatches(); n != 2 {
		t.Fatalf("two models at one key must be two agents, got %d dispatch(es)", n)
	}
	// …and the same model at the same key is ONE agent.
	req := partReq(config.Step{}, "review", "claude-opus-5", "new_comment", 7)
	if _, handled, err := aff.Dispatch(context.Background(), runner, req); err != nil || !handled {
		t.Fatalf("handled=%v err=%v", handled, err)
	}
	if n := runner.dispatches(); n != 2 {
		t.Fatalf("same runtime+model+key must reuse the session, got %d dispatch(es)", n)
	}
}

// A BARE LAUNCH pool (no resolved model) is deliberately distinct from a
// pinned-model pool: they are different agents.
func TestBareLaunchIsItsOwnPartition(t *testing.T) {
	cfg := &config.Config{Runtimes: config.RuntimeSet{"paseo": {Use: "paseo", Session: partKey, Default: true}}}
	aff, runner, _ := partRig(t, cfg)
	for _, model := range []string{"", "claude-opus-5"} {
		req := partReq(config.Step{}, "review", model, "new_comment", 7)
		if _, handled, _ := aff.Dispatch(context.Background(), runner, req); !handled {
			t.Fatalf("model %q not handled", model)
		}
	}
	if n := runner.dispatches(); n != 2 {
		t.Fatalf("bare launch and a pinned model must not share a session, got %d", n)
	}
}

// The RUNTIME partitions too: you cannot resume a paseo session on another
// runtime.
func TestRuntimePartitionsAffinity(t *testing.T) {
	cfg := &config.Config{Runtimes: config.RuntimeSet{
		"paseo": {Use: "paseo", Session: partKey, Default: true},
		"other": {Use: "paseo", Session: partKey},
	}}
	aff, runner, _ := partRig(t, cfg)
	for _, rt := range []string{"paseo", "other"} {
		req := partReq(config.Step{Runtime: rt}, "review", "m", "new_comment", 7)
		if _, handled, _ := aff.Dispatch(context.Background(), runner, req); !handled {
			t.Fatalf("runtime %s not handled", rt)
		}
	}
	if n := runner.dispatches(); n != 2 {
		t.Fatalf("two runtimes at one key must be two agents, got %d", n)
	}
}

// Scope resolution: a step's own session: wins; with none, the step joins the
// runtime's overall pool; with neither, dispatch is fresh (unhandled).
func TestSessionScopeResolution(t *testing.T) {
	withRuntimePool := &config.Config{Runtimes: config.RuntimeSet{
		"paseo": {Use: "paseo", Session: partKey, Default: true},
	}}
	noPool := &config.Config{Runtimes: config.RuntimeSet{"paseo": {Use: "paseo", Default: true}}}

	// No session: anywhere → affinity does not own the dispatch.
	aff, runner, _ := partRig(t, noPool)
	if _, handled, _ := aff.Dispatch(context.Background(), runner, partReq(config.Step{}, "s", "m", "new_comment", 7)); handled {
		t.Fatal("with no session: anywhere, dispatch must be fresh")
	}

	// A step with no session: joins the RUNTIME pool — two different steps
	// at one key reach one agent.
	aff, runner, _ = partRig(t, withRuntimePool)
	for _, ident := range []string{"step-a", "step-b"} {
		if _, handled, _ := aff.Dispatch(context.Background(), runner, partReq(config.Step{}, ident, "m", "new_comment", 7)); !handled {
			t.Fatalf("%s not handled", ident)
		}
	}
	if n := runner.dispatches(); n != 1 {
		t.Fatalf("the overall pool is shared across steps, got %d agents", n)
	}

	// A step with its OWN session: gets its own pool, namespaced to its
	// identity — so an identical key string is still a distinct session.
	aff, runner, _ = partRig(t, withRuntimePool)
	own := config.Step{Session: partKey}
	for _, ident := range []string{"step-a", "step-b"} {
		if _, handled, _ := aff.Dispatch(context.Background(), runner, partReq(own, ident, "m", "new_comment", 7)); !handled {
			t.Fatalf("%s not handled", ident)
		}
	}
	if n := runner.dispatches(); n != 2 {
		t.Fatalf("step-scoped pools must be distinct per step, got %d agents", n)
	}
}

// A step pool must not collide with the runtime's overall pool at the same
// rendered key.
func TestStepPoolDoesNotCollideWithRuntimePool(t *testing.T) {
	cfg := &config.Config{Runtimes: config.RuntimeSet{"paseo": {Use: "paseo", Session: partKey, Default: true}}}
	aff, runner, _ := partRig(t, cfg)
	if _, handled, _ := aff.Dispatch(context.Background(), runner, partReq(config.Step{}, "shared", "m", "new_comment", 7)); !handled {
		t.Fatal("runtime pool dispatch not handled")
	}
	if _, handled, _ := aff.Dispatch(context.Background(), runner, partReq(config.Step{Session: partKey}, "shared", "m", "new_comment", 7)); !handled {
		t.Fatal("step pool dispatch not handled")
	}
	if n := runner.dispatches(); n != 2 {
		t.Fatalf("the step pool must be namespaced away from the overall pool, got %d", n)
	}
}

// A binding survives a restart: a second Affinity over the same store rebinds
// by (runtime, model, key) rather than spawning again.
func TestPartitionedBindingSurvivesRestart(t *testing.T) {
	cfg := &config.Config{Runtimes: config.RuntimeSet{"paseo": {Use: "paseo", Session: partKey, Default: true}}}
	aff, runner, st := partRig(t, cfg)
	req := partReq(config.Step{}, "review", "claude-opus-5", "new_comment", 7)
	if _, handled, _ := aff.Dispatch(context.Background(), runner, req); !handled {
		t.Fatal("first dispatch not handled")
	}
	if n := runner.dispatches(); n != 1 {
		t.Fatalf("first dispatch should spawn once, got %d", n)
	}
	// Restart: a fresh Affinity over the same persisted store.
	reg2 := NewRegistry(cfg.MergedControllers(), cfg.DefaultRuntimeName(), runner, &affSender{})
	aff2 := NewAffinity(reg2, st, cfg, nil, nil, nil)
	if _, handled, _ := aff2.Dispatch(context.Background(), runner, req); !handled {
		t.Fatal("post-restart dispatch not handled")
	}
	if n := runner.dispatches(); n != 1 {
		t.Fatalf("a restored binding must not spawn again, got %d dispatch(es)", n)
	}
}

// StepSessionKey is what keeps a step's pool distinct; assert its shape
// rather than leaving the namespacing implicit.
func TestStepSessionKeyNamespaces(t *testing.T) {
	if got := StepSessionKey("review", "o/r#7"); got == "o/r#7" {
		t.Fatal("a step-scoped key must be namespaced")
	}
	if StepSessionKey("a", "k") == StepSessionKey("b", "k") {
		t.Fatal("two steps with one key string must not collide")
	}
	// No identity (the runtime pool) leaves the key as written.
	if got := StepSessionKey("", "o/r#7"); got != "o/r#7" {
		t.Fatalf("the overall pool uses the key as written, got %q", got)
	}
}
