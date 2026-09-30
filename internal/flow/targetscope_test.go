package flow

import (
	"context"
	"strings"
	"testing"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/hostcmd"
	"github.com/NodeSpy/conductor/internal/targets"
)

// A github connector instance named "gh" — deliberately the same word as the
// gh binary, which is unrelated: `conductor call gh.<verb>` is conductor's own
// verb on this instance; the `gh` binary is a host command with its own
// profile.
func ghVerbRunner(t *testing.T) *Runner {
	t.Helper()
	cfg := loadConfig(t, "connectors:\n  gh: { use: github }\n")
	r := newTestRunner(t, cfg, buildRegistry(t, cfg)).Runner
	r.DryRun = true
	return r
}

// The dispatch: acme/app PR #42, head branch fix/42.
func ghFixer(verbs []string, scopes map[string]map[string][]string) SkillIdentity {
	return SkillIdentity{
		TargetTrusted: true, Agent: "fixer", Trigger: "merge_conflict",
		Repo: "acme/app", Number: 42, Verbs: verbs, Scopes: scopes,
		Context: map[string]any{"head_ref": "fix/42"},
	}
}

func verbErr(r *Runner, id SkillIdentity, uses string, opts map[string]any) error {
	_, err := r.RunSkillVerb(context.Background(), id, uses, opts)
	return err
}

// Conductor's own github verbs are bound to the dispatch's target by the
// connector's scopes: its own PR and head branch are in context; another
// PR, another branch, the default branch — or a closed target — need the
// grant to name them.
func TestGithubVerbsAreBoundToTheDispatchTargetByScope(t *testing.T) {
	r := ghVerbRunner(t)
	fixer := ghFixer([]string{"gh.comment", "gh.put_file", "gh.merge_pr"}, nil)
	comment := func(pr int) map[string]any { return map[string]any{"repo": "acme/app", "pr": pr, "body": "x"} }

	if err := verbErr(r, fixer, "gh.comment", comment(42)); err != nil {
		t.Fatalf("a comment on its own PR: %v", err)
	}
	if err := verbErr(r, fixer, "gh.comment", comment(43)); err == nil || !strings.Contains(err.Error(), "names pr \"43\"") {
		t.Fatalf("a comment on another PR is refused by the number scope: %v", err)
	}
	wide := ghFixer([]string{"gh.comment"}, map[string]map[string][]string{"gh.comment": {"pr": {"*"}}})
	if err := verbErr(r, wide, "gh.comment", comment(43)); err != nil {
		t.Fatalf("the grant naming any PR widens it: %v", err)
	}
	put := func(branch string) map[string]any {
		o := map[string]any{"repo": "acme/app", "path": "f", "content": "x", "message": "m"}
		if branch != "" {
			o["branch"] = branch
		}
		return o
	}
	if err := verbErr(r, fixer, "gh.put_file", put("fix/42")); err != nil {
		t.Fatalf("a commit on its own head branch: %v", err)
	}
	if err := verbErr(r, fixer, "gh.put_file", put("stray")); err == nil || !strings.Contains(err.Error(), "branch") {
		t.Fatalf("another branch is refused by the branch scope: %v", err)
	}
	if err := verbErr(r, fixer, "gh.put_file", put("")); err == nil || !strings.Contains(err.Error(), "@default-branch") {
		t.Fatalf("no branch (the default branch) is refused: %v", err)
	}
	// A target the event's sender chose owns nothing: even with every repo
	// granted, its "own" PR is not in context.
	forged := ghFixer([]string{"gh.comment"}, map[string]map[string][]string{"gh.comment": {"repo": {"*"}}})
	forged.TargetTrusted = false
	if err := verbErr(r, forged, "gh.comment", comment(42)); err == nil || !strings.Contains(err.Error(), "names pr") {
		t.Fatalf("an untrusted target has no own PR: %v", err)
	}
	// Its own PR merged: nothing of it is in context any more.
	targets.Default.MarkClosed("acme/app", 42, true)
	defer targets.Default.Reopen("acme/app", 42)
	if err := verbErr(r, fixer, "gh.comment", comment(42)); err == nil {
		t.Fatal("a comment on its own merged PR must be refused")
	}
	if err := verbErr(r, fixer, "gh.put_file", put("fix/42")); err == nil {
		t.Fatal("a commit to a merged PR's branch must be refused")
	}
}

// THE TWO SURFACES ARE INDEPENDENT. Opening a PR is refused on both by
// default — the gh binary by its profile, conductor's verb by the verb
// grant — each can be allowed on its own, and allowing one never allows the
// other.
func TestBinaryAndVerbSurfacesAreIndependent(t *testing.T) {
	r := ghVerbRunner(t)
	hostCtx := hostcmd.Context{Repo: "acme/app", Number: 42, IsPR: true, HeadBranch: "fix/42"}
	ghBinary := func(rule hostcmd.Rule) hostcmd.Decision {
		return hostcmd.Decide(hostcmd.Request{Tool: "gh", Args: []string{"pr", "create", "-t", "x", "-b", "y"}}, rule, hostCtx)
	}
	createPR := map[string]any{"repo": "acme/app", "title": "x", "head": "fix/42", "base": "main"}
	defaultBinary := hostcmd.Resolve("gh", nil, false)
	allowedBinary := hostcmd.Resolve("gh", []*config.IsolationConfig{{Host: map[string]*config.HostCommand{"gh": {Allow: []string{"*", "pr create *"}}}}}, false)
	noGrant := ghFixer([]string{"gh.comment"}, nil)
	grant := ghFixer([]string{"gh.create_pr"}, nil) // skill.verbs: [gh.create_pr]

	// Default: both refuse.
	if d := ghBinary(defaultBinary); d.Allow {
		t.Fatal("gh pr create must be refused by the gh profile by default")
	}
	if err := verbErr(r, noGrant, "gh.create_pr", createPR); err == nil || !strings.Contains(err.Error(), "skill.verbs") {
		t.Fatalf("conductor call gh.create_pr must be refused by the verb grant by default: %v", err)
	}
	// Allow the binary only: the binary opens PRs, the verb still refuses.
	if d := ghBinary(allowedBinary); !d.Allow {
		t.Fatalf("host.gh.allow opens gh pr create: %q", d.Reason)
	}
	if err := verbErr(r, noGrant, "gh.create_pr", createPR); err == nil {
		t.Fatal("allowing the gh binary must not grant the verb")
	}
	// Grant the verb only: the verb opens PRs, the binary still refuses.
	if err := verbErr(r, grant, "gh.create_pr", createPR); err != nil {
		t.Fatalf("skill.verbs: [gh.create_pr] grants the verb: %v", err)
	}
	if d := ghBinary(defaultBinary); d.Allow {
		t.Fatal("granting the verb must not allow the gh binary")
	}
}
