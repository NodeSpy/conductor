package flow

import (
	"context"
	"strings"
	"testing"

	"github.com/NodeSpy/conductor/internal/targets"
)

// targetWritesRig builds a runner with a github connector reachable by every
// verb (policy.agent_authored.verbs: ["**"]) in dry-run, so a call that clears
// the target-lifecycle gate stubs rather than hitting a real API.
func targetWritesRig(t *testing.T) *Runner {
	t.Helper()
	cfg := loadConfig(t, `
connectors:
  gh: { use: github, token: x }
policy:
  agent_authored:
    verbs: ["**"]
`)
	r := newTestRunner(t, cfg, buildRegistry(t, cfg)).Runner
	r.DryRun = true
	return r
}

func TestRunSkillVerbCommentOwnPRAllowed(t *testing.T) {
	r := targetWritesRig(t)
	id := SkillIdentity{TargetTrusted: true, Agent: "fixer", Repo: "acme/app", Number: 1, Verbs: []string{"gh.comment"}}
	if _, err := r.RunSkillVerb(context.Background(), id, "gh.comment",
		map[string]any{"repo": "acme/app", "pr": 1, "body": "hi"}); err != nil {
		t.Fatalf("comment on the dispatch's own PR should be allowed: %v", err)
	}
}

func TestRunSkillVerbCommentOtherPRRefusedAndAudited(t *testing.T) {
	cfg := loadConfig(t, `
connectors:
  gh: { use: github, token: x }
policy:
  agent_authored:
    verbs: ["**"]
`)
	rig := newTestRunner(t, cfg, buildRegistry(t, cfg))
	r := rig.Runner
	r.DryRun = true
	id := SkillIdentity{TargetTrusted: true, Agent: "fixer", Repo: "acme/app", Number: 1, Verbs: []string{"gh.comment"}}
	_, err := r.RunSkillVerb(context.Background(), id, "gh.comment",
		map[string]any{"repo": "acme/app", "pr": 2, "body": "hi"})
	if err == nil {
		t.Fatal("comment on a different PR should be refused")
	}
	want := "target: write to acme/app#2 but dispatch target is #1"
	if !strings.Contains(err.Error(), want) {
		t.Fatalf("error = %v, want it to contain %q", err, want)
	}
	audits := rig.Store.auditsWithEvent("verb")
	found := false
	for _, e := range audits {
		if e["via"] == "skill" && e["uses"] == "gh.comment" && e["outcome"] == "denied" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected a denied via:skill audit row, got %+v", audits)
	}
}

func TestRunSkillVerbCreatePRRefused(t *testing.T) {
	r := targetWritesRig(t)
	id := SkillIdentity{TargetTrusted: true, Agent: "fixer", Repo: "acme/app", Number: 1, Verbs: []string{"gh.create_pr"}}
	_, err := r.RunSkillVerb(context.Background(), id, "gh.create_pr",
		map[string]any{"repo": "acme/app", "title": "t", "head": "h", "base": "main"})
	if err == nil {
		t.Fatal("create_pr should be refused by the default fixer policy")
	}
	if !strings.Contains(err.Error(), "opening a PR is refused") {
		t.Fatalf("error = %v", err)
	}
}

func TestRunSkillVerbOnClosedTargetRefused(t *testing.T) {
	r := targetWritesRig(t)
	targets.Default.MarkClosed("acme/closedtarget", 5, true)
	t.Cleanup(func() { targets.Default.Reopen("acme/closedtarget", 5) })

	id := SkillIdentity{TargetTrusted: true, Agent: "fixer", Repo: "acme/closedtarget", Number: 5, Verbs: []string{"gh.comment"}}
	_, err := r.RunSkillVerb(context.Background(), id, "gh.comment",
		map[string]any{"repo": "acme/closedtarget", "pr": 5, "body": "hi"})
	if err == nil {
		t.Fatal("a write to a merged/closed target should be refused")
	}
	if !strings.Contains(err.Error(), "is merged") {
		t.Fatalf("error = %v", err)
	}
}

// TestRunSkillVerbIncidentSequence: after the target is marked merged, both a
// create_pr (a brand new object) and a comment on a DIFFERENT PR number are
// refused — the two-pronged "a stray write after the target died" case.
func TestRunSkillVerbIncidentSequence(t *testing.T) {
	r := targetWritesRig(t)
	targets.Default.MarkClosed("acme/incident", 10, true)
	t.Cleanup(func() { targets.Default.Reopen("acme/incident", 10) })

	id := SkillIdentity{TargetTrusted: true, Agent: "fixer", Repo: "acme/incident", Number: 10, Verbs: []string{"gh.*"}}

	if _, err := r.RunSkillVerb(context.Background(), id, "gh.create_pr",
		map[string]any{"repo": "acme/incident", "title": "t", "head": "h", "base": "main"}); err == nil {
		t.Fatal("create_pr after the target merged should be refused")
	}
	if _, err := r.RunSkillVerb(context.Background(), id, "gh.comment",
		map[string]any{"repo": "acme/incident", "pr": 11, "body": "hi"}); err == nil {
		t.Fatal("a comment on a different PR after the target merged should be refused")
	}
}
