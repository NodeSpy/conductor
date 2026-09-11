package flow

import (
	"context"
	"strings"
	"testing"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/controller"
	"github.com/NodeSpy/conductor/internal/core"
)

// META-TEST (round-8 #1). An agent-authored step's identity is what its
// SESSION BINDING KEY is built from:
//
//	bindingKey(runtime, model, StepSessionKey(identity, rendered session.key))
//
// and the identity ladder returns an author-pinned `name:` VERBATIM, with no
// scope of its own. `name:` and `session:` were not on the forbidden-field
// list — they are not capability grants, so nobody looked at them — which
// meant an agent-authored step could write
//
//	{type: agent, name: "review", session: {key: "acme/other-repo#42"}}
//
// and compute exactly the key a legitimate operator step named "review"
// produces in ANOTHER repo. The affinity registry's live-binding fast path
// then delivers this agent's prompt into that repo's running session. Repo
// and PR number are public; the step-name convention comes from a shared
// pack.
//
// The fix confines an agent-authored identity to its own dispatch. This
// asserts the property where it matters: the KEY, which is the thing the
// registry matches on.
func TestAgentAuthoredStepCannotAddressAnotherDispatchsSession(t *testing.T) {
	const victimName, victimKey = "review", "acme/other-repo#42"

	// What the VICTIM's operator-authored step binds under, in its own repo.
	victimIdentity := config.IdentityFor(
		config.TriggerScope("github.review_requested"), victimName, "steps[0]", nil)
	victimBinding := controller.StepSessionKey(victimIdentity, victimKey)

	// The attacker: an agent-authored step in a DIFFERENT dispatch, writing
	// the same name and the same session key.
	attacker := core.Trigger{Kind: "review_requested", TargetTrusted: true, Target: core.Target{Repo: "attacker/sandbox"}}
	ctx := markAgentAuthored(context.Background(), attacker)
	attackerIdentity := stepIdentity(ctx, config.Step{Type: "agent", Name: victimName}, "steps[0]")
	attackerBinding := controller.StepSessionKey(attackerIdentity, victimKey)

	if attackerBinding == victimBinding {
		t.Fatalf("an agent-authored step computed the victim's session binding key (%q) — "+
			"its prompt would be delivered into another repo's live session", attackerBinding)
	}
	if !strings.Contains(attackerIdentity, "attacker/sandbox") {
		t.Errorf("the confinement must name the dispatch's OWN trusted scope, got %q", attackerIdentity)
	}

	// Two agent-authored steps in the SAME dispatch still share an identity,
	// so continuity inside a plan — the legitimate use — is untouched.
	same := stepIdentity(ctx, config.Step{Type: "agent", Name: victimName}, "steps[7]")
	if same != attackerIdentity {
		t.Errorf("session continuity within one dispatch must survive: %q vs %q", same, attackerIdentity)
	}

	// A different dispatch of the same shape gets a different namespace.
	other := core.Trigger{Kind: "review_requested", TargetTrusted: true, Target: core.Target{Repo: "victim/repo"}}
	otherID := stepIdentity(markAgentAuthored(context.Background(), other),
		config.Step{Type: "agent", Name: victimName}, "steps[0]")
	if otherID == attackerIdentity {
		t.Error("two dispatches in different repos share an agent-authored identity")
	}

	// An OPERATOR step is untouched: its config IS the trust boundary, and
	// pinning a name across runs is what `name:` is for.
	if got := stepIdentity(context.Background(), config.Step{Type: "agent", Name: victimName}, "steps[0]"); got != victimName {
		t.Errorf("an operator-authored identity must stay verbatim, got %q", got)
	}
}

// A FORGED target cannot be the wall either: when the dispatch's own repo was
// chosen by whoever sent the request (round-7 #3), namespacing by it would let
// an attacker name the repo they want to reach.
func TestAgentAuthoredNamespaceIgnoresAForgedTarget(t *testing.T) {
	forged := core.Trigger{
		Kind: "delivery", Source: "webhook", Instance: "hooks",
		Target:        core.Target{Repo: "victim/repo"},
		TargetTrusted: false, // the sender chose this target
	}
	id := stepIdentity(markAgentAuthored(context.Background(), forged),
		config.Step{Type: "agent", Name: "review"}, "steps[0]")
	if strings.Contains(id, "victim/repo") {
		t.Fatalf("the namespace was built from a repo the sender chose: %q", id)
	}
	// It still confines — to something the sender does not pick.
	if !strings.HasPrefix(id, "agent:") {
		t.Fatalf("a forged-target dispatch must still be confined, got %q", id)
	}
}

// THE CLASS. `name:` and `session:` were missed because the forbidden-field
// list is about CAPABILITY GRANTS and these are not grants — they are
// ADDRESSES. This enumerates the Step fields that feed an identity or a
// session binding key, and asserts each is confined for an agent-authored
// step. A new field that reaches IdentityFor/StepSessionKey has to be added
// here, and the assertion tells its author what the requirement is.
func TestEveryIdentityAffectingFieldIsConfinedForAgentAuthoredSteps(t *testing.T) {
	trusted := core.Trigger{Kind: "review_requested", TargetTrusted: true,
		Target: core.Target{Repo: "own/repo", Number: 7}}
	ctx := markAgentAuthored(context.Background(), trusted)

	// Each case sets ONE identity/session-affecting field to a value naming
	// somebody else's binding, and asserts the resulting key is confined to
	// this dispatch.
	for _, tc := range []struct {
		field string
		step  config.Step
		key   string
	}{
		{"name:", config.Step{Type: "agent", Name: "review"}, "acme/other#42"},
		{"session.key:", config.Step{Type: "agent", Name: "review",
			Session: &config.SessionSpec{Key: "acme/other#42"}}, "acme/other#42"},
		{"id: (the slot label the identity falls back to)",
			config.Step{Type: "agent", ID: "review"}, "acme/other#42"},
		{"no pin at all (structural identity)", config.Step{Type: "agent"}, "acme/other#42"},
	} {
		t.Run(tc.field, func(t *testing.T) {
			id := stepIdentity(ctx, tc.step, "steps[0]")
			if !strings.HasPrefix(id, "agent:own/repo#review_requested#7/") {
				t.Fatalf("%s produced an unconfined identity %q — every field that feeds "+
					"IdentityFor or StepSessionKey must be namespaced to the dispatch, or an "+
					"agent can address another tenant's live session", tc.field, id)
			}
			// …and the binding key that identity produces is likewise not one
			// an operator step elsewhere could produce.
			bound := controller.StepSessionKey(id, tc.key)
			if !strings.Contains(bound, "agent:own/repo#review_requested#7/") {
				t.Fatalf("%s: the session binding key escaped the namespace: %q", tc.field, bound)
			}
		})
	}
}

// ROUND-10 #2. The namespace was repo#kind — no per-dispatch discriminator —
// so two pull requests on ONE repo shared it. Two untrusted contributors, one
// namespace: an agent-authored step on PR #42 could name its way onto the
// session running for PR #99. The repo wall was there; the wall between
// contributors inside it was not.
func TestAgentAuthoredNamespaceSeparatesTargetsInOneRepo(t *testing.T) {
	ns := func(number int) string {
		trig := core.Trigger{
			Kind: "review_requested", TargetTrusted: true,
			Target: core.Target{Repo: "acme/app", Number: number},
		}
		return stepIdentity(markAgentAuthored(context.Background(), trig),
			config.Step{Type: "agent", Name: "review"}, "steps[0]")
	}
	pr42, pr99 := ns(42), ns(99)
	if pr42 == pr99 {
		t.Fatalf("two PRs on one repo share an agent-authored namespace (%q) — a step on one "+
			"can address the other's live session", pr42)
	}
	// The binding keys they produce differ too, which is the thing the
	// affinity registry actually matches on.
	if controller.StepSessionKey(pr42, "shared-key") == controller.StepSessionKey(pr99, "shared-key") {
		t.Fatal("the session binding keys collide across targets in one repo")
	}
	// …and the SAME target still shares, so continuity within a dispatch —
	// the legitimate use — survives.
	if ns(42) != pr42 {
		t.Error("two steps of one dispatch must share a namespace")
	}
}
