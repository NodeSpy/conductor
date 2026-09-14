package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// TestCheckNoEnvironmentAllSections proves EACH bind-only section is rejected by
// name — the security boundary is six independent branches, not just connectors.
func TestCheckNoEnvironmentAllSections(t *testing.T) {
	one := map[string]yaml.Node{"x": {Kind: yaml.ScalarNode, Value: "y"}}
	node := yaml.Node{Kind: yaml.MappingNode}
	cases := []struct {
		name string
		m    PackManifest
	}{
		{"connectors", PackManifest{Connectors: one}},
		{"secrets", PackManifest{Secrets: one}},
		{"vaults", PackManifest{Vaults: one}},
		{"stores", PackManifest{StoresRaw: one}},
		{"runtimes", PackManifest{Runtimes: one}},
		{"hosts", PackManifest{Hosts: one}},
		{"memory", PackManifest{Memory: node}},
	}
	for _, c := range cases {
		c.m.Pack.Name = "p"
		err := c.m.checkNoEnvironment()
		if err == nil {
			t.Errorf("shipping %s should be rejected", c.name)
			continue
		}
		if !strings.Contains(err.Error(), c.name) {
			t.Errorf("rejection for %s should name it: %v", c.name, err)
		}
	}
	// A behavior-only manifest passes.
	if err := (&PackManifest{Pack: PackMeta{Name: "p"}}).checkNoEnvironment(); err != nil {
		t.Errorf("a behavior-only pack must pass: %v", err)
	}
}

// TestPackWarnings exercises the three warning sites, which otherwise have no
// observer: deprecation, armed-with-no-repos, and an undeclared setting.
func TestPackWarnings(t *testing.T) {
	dir := t.TempDir()
	writePackSource(t, dir, "src/dep", `
pack:
  name: dep-pack
  version: 0.1.0
  deprecated: "use dep-pack v2"
  requires: { conductor: ">=0.1" }
settings:
  known: { type: string, default: k }
triggers:
  - name: t1
    on: manual
    steps: [ { id: s, run: js, code: "return {}" } ]
`)
	body := `
connectors: { gh: { use: github } }
packs:
  dep:
    source: ./src/dep
    settings: { unknown_setting: v }
    triggers:
      t1: { enabled: true }        # armed but NO repos
`
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := resolveAndLoad(t, path)
	if err != nil {
		t.Fatalf("resolveAndLoad: %v", err)
	}
	ws := strings.Join(cfg.PackWarnings(), "\n")
	for _, want := range []string{"deprecated", "not declared"} {
		if !strings.Contains(ws, want) {
			t.Errorf("expected a warning containing %q, got:\n%s", want, ws)
		}
	}
}

// TestPackGithubTriggerArmedNeedsRepos proves the fail-closed consent boundary:
// a github pack trigger armed with enabled:true but no repos is rejected (an
// empty repo set would otherwise match every repo the connector observes),
// while the same arm WITH repos succeeds.
func TestPackGithubTriggerArmedNeedsRepos(t *testing.T) {
	const manifest = `
pack:
  name: gh-pack
  version: 1.0.0
  requires:
    conductor: ">=0.1"
    connectors: [github]
triggers:
  - name: on_review
    on: github.review_requested
    steps: [ { id: s, run: js, code: "return {}" } ]
`
	write := func(t *testing.T, arm string) string {
		dir := t.TempDir()
		writePackSource(t, dir, "src", manifest)
		body := `
connectors: { gh: { use: github } }
packs:
  p:
    source: ./src
    connectors: { github: gh }
    triggers:
      on_review:
` + arm
		path := filepath.Join(dir, "config.yaml")
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		return path
	}

	// Armed, no repos → hard error naming the fix.
	if _, err := resolveAndLoad(t, write(t, "        enabled: true\n")); err == nil {
		t.Fatal("armed github pack trigger with no repos was accepted; the consent boundary must reject it")
	} else if !strings.Contains(err.Error(), "repos") {
		t.Fatalf("error did not name repos as the fix: %v", err)
	}

	// Armed, WITH repos → accepted.
	if _, err := resolveAndLoad(t, write(t, "        enabled: true\n        repos: [acme/app]\n")); err != nil {
		t.Fatalf("armed github pack trigger WITH repos must be accepted: %v", err)
	}
}

// TestLintPackManifestNegatives feeds one malformed manifest per problem type
// and asserts the specific lint message — currently only the clean pack is linted.
func TestLintPackManifestNegatives(t *testing.T) {
	cases := []struct {
		name string
		man  PackManifest
		want string
	}{
		{"missing version", PackManifest{Pack: PackMeta{Name: "p", Requires: PackRequires{Conductor: ">=0.1"}}}, "pack.version is required"},
		{"missing conductor", PackManifest{Pack: PackMeta{Name: "p", Version: "1"}}, "requires.conductor is required"},
		{"dangling export wf", PackManifest{
			Pack:    PackMeta{Name: "p", Version: "1", Requires: PackRequires{Conductor: ">=0.1"}},
			Exports: PackExports{Workflows: []string{"ghost"}},
		}, "exports.workflows"},
		// requires.roles is gone (§E); requires.connectors took over the
		// "what must a binding be able to do" job by bounding the grant.
		{"grant outside requires.connectors", PackManifest{
			Pack: PackMeta{Name: "p", Version: "1", Requires: PackRequires{Conductor: ">=0.1", Connectors: ConnectorReqs{"github": {Version: AnyVersion, Required: true}}}},
			Workflows: map[string]WorkflowDef{"f": {Steps: []Step{
				{ID: "a", Type: "agent", Skill: &SkillPolicy{Verbs: []string{"pagerduty.trigger"}}},
			}}},
		}, "requires.connectors"},
	}
	for _, c := range cases {
		problems := LintPackManifest(&c.man)
		if !containsSubstr(problems, c.want) {
			t.Errorf("%s: expected a problem containing %q, got %v", c.name, c.want, problems)
		}
	}
}

// TestLintUndeclaredSettingsRef proves the settings-ref lint catches a prompt
// that references a setting the pack never declares.
func TestLintUndeclaredSettingsRef(t *testing.T) {
	dir := t.TempDir()
	writePackSource(t, dir, "p", `
pack:
  name: p
  version: 1.0.0
  requires: { conductor: ">=0.1" }
workflows:
  flow:
    steps:
      - id: s
        type: agent
        workspace: local
        prompt: "uses ${settings.nope}"
`)
	problems, err := LintPackDir(filepath.Join(dir, "p"))
	if err != nil {
		t.Fatal(err)
	}
	if !containsSubstr(problems, "${settings.nope}") {
		t.Fatalf("expected an undeclared-settings-ref problem, got %v", problems)
	}
}

// TestApplyAgentOverrideReplacesLists locks in the security-relevant override
// semantics: a pack override REPLACES a bundled list (it does not append), so a
// consumer can NARROW a bundled agent's skill.verbs — not only widen it.
func TestApplyAgentOverrideReplacesLists(t *testing.T) {
	base := Step{
		Workspace: "worktree",
		Skill:     &SkillPolicy{Verbs: []string{"gh.comment", "gh.submit_review"}},
	}
	out, err := applyStepOverride(base, map[string]any{
		"skill": map[string]any{"verbs": []any{"gh.comment"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	// replace semantics: the override's single verb wins; the bundle's extra
	// verb is dropped (a real restriction).
	if len(out.Skill.Verbs) != 1 || out.Skill.Verbs[0] != "gh.comment" {
		t.Fatalf("override should REPLACE the bundled list (narrowing), got %v", out.Skill.Verbs)
	}
	// non-overridden scalar fields survive the deep-merge.
	if out.Workspace != "worktree" {
		t.Fatalf("non-overridden field should survive, got workspace=%q", out.Workspace)
	}
}

// …and the MAP form narrows identically (round-4 F2). It did not: once
// skill.verbs grew a map form, the override path's deep-merge copied every
// base key the override omitted, so a consumer narrowing a bundled grant kept
// the verbs it had just dropped — failing OPEN, in the newer spelling only.
//
// The rule is per FORM-INDEPENDENT: the set of verbs the override names is the
// final set. Whatever a permission key looks like, omitting an entry removes
// it.
func TestApplyAgentOverrideNarrowsTheMapForm(t *testing.T) {
	base := Step{
		Workspace: "worktree",
		Skill: &SkillPolicy{
			Verbs: []string{"gh.comment", "gh.submit_review"},
			VerbScopes: map[string]map[string][]string{
				"gh.comment":       {"repo": {"acme/app", "acme/docs"}},
				"gh.submit_review": {},
			},
		},
	}
	out, err := applyStepOverride(base, map[string]any{
		"skill": map[string]any{"verbs": map[string]any{
			"gh.comment": map[string]any{"repo": []any{"acme/docs"}},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Skill.Verbs) != 1 || out.Skill.Verbs[0] != "gh.comment" {
		t.Fatalf("the override named ONE verb, so the step must grant exactly that one; got %v "+
			"— a consumer cannot narrow a bundled grant and the pack keeps a permission it was denied",
			out.Skill.Verbs)
	}
	// The kept verb's own constraints are the override's, narrowed too.
	if got := out.Skill.VerbScopes["gh.comment"]["repo"]; len(got) != 1 || got[0] != "acme/docs" {
		t.Fatalf("the kept verb's per-option list must be the override's, got %v", got)
	}
	if _, dropped := out.Skill.VerbScopes["gh.submit_review"]; dropped {
		t.Fatalf("a dropped verb must not keep constraints behind: %v", out.Skill.VerbScopes)
	}
	if out.Workspace != "worktree" {
		t.Fatalf("non-overridden field should survive, got workspace=%q", out.Workspace)
	}
}

// …and at every DEPTH (round-5 #2). isPermissionSet matched the full path
// exactly, so it recognized `skill.verbs` on a top-level step and nowhere
// else: a `compensate.skill.verbs` (three segments) or a parallel branch's
// (deeper still) fell through to the generic deep-merge, and the consumer
// could not narrow a grant it inherited. Failing open, in the nesting a
// reviewer is least likely to check.
func TestApplyAgentOverrideNarrowsNestedGrants(t *testing.T) {
	twoVerbs := func() *SkillPolicy {
		return &SkillPolicy{
			Verbs: []string{"gh.comment", "gh.merge"},
			VerbScopes: map[string]map[string][]string{
				"gh.comment": {"repo": {"acme/app"}}, "gh.merge": {"repo": {"acme/app"}},
			},
		}
	}
	narrowTo := map[string]any{"skill": map[string]any{"verbs": map[string]any{
		"gh.comment": map[string]any{"repo": []any{"acme/app"}},
	}}}

	t.Run("compensate", func(t *testing.T) {
		base := Step{Skill: twoVerbs(), Compensate: &Step{ID: "undo", Skill: twoVerbs()}}
		out, err := applyStepOverride(base, map[string]any{
			"skill":      narrowTo["skill"],
			"compensate": narrowTo,
		})
		if err != nil {
			t.Fatal(err)
		}
		if got := out.Skill.Verbs; len(got) != 1 {
			t.Fatalf("the top-level grant did not narrow: %v", got)
		}
		if out.Compensate == nil || out.Compensate.Skill == nil {
			t.Fatal("the compensation step lost its skill block")
		}
		if got := out.Compensate.Skill.Verbs; len(got) != 1 || got[0] != "gh.comment" {
			t.Fatalf("a compensation step's grant must narrow like any other; gh.merge survived: %v", got)
		}
	})

	// A parallel branch reaches its steps through a LIST, and lists are
	// replaced wholesale — so this nesting was already safe. It is here as a
	// regression guard, and to record WHICH nestings the bug could reach: the
	// map-valued ones (compensate, and anything else that hangs a step off a
	// key rather than an index).
	t.Run("parallel branch", func(t *testing.T) {
		base := Step{Parallel: &ParallelSpec{Branches: [][]Step{{{ID: "b0", Skill: twoVerbs()}}}}}
		// `parallel:` marshals as a list of branches, each a list of steps.
		out, err := applyStepOverride(base, map[string]any{
			"parallel": []any{
				[]any{map[string]any{"id": "b0", "skill": narrowTo["skill"]}},
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		if out.Parallel == nil || len(out.Parallel.Branches) != 1 || len(out.Parallel.Branches[0]) != 1 {
			t.Fatalf("branch structure lost: %+v", out.Parallel)
		}
		sk := out.Parallel.Branches[0][0].Skill
		if sk == nil {
			t.Fatal("the branch step lost its skill block")
		}
		if got := sk.Verbs; len(got) != 1 || got[0] != "gh.comment" {
			t.Fatalf("a parallel branch's grant must narrow like any other; gh.merge survived: %v", got)
		}
	})
}

// Narrowing must hold across the FORM BOUNDARY too — a map-form bundle
// overridden by a list, and a list-form bundle overridden by a map. A
// permission rule that depends on which spelling each side happened to use is
// the same bug wearing different clothes.
func TestApplyAgentOverrideNarrowsAcrossForms(t *testing.T) {
	mapBase := &SkillPolicy{
		Verbs: []string{"gh.comment", "gh.merge"},
		VerbScopes: map[string]map[string][]string{
			"gh.comment": {"repo": {"acme/app"}}, "gh.merge": {"repo": {"acme/app"}},
		},
	}
	listBase := &SkillPolicy{Verbs: []string{"gh.comment", "gh.merge"}}

	for _, tc := range []struct {
		name     string
		base     *SkillPolicy
		override any
	}{
		{"map bundle, list override", mapBase, []any{"gh.comment"}},
		{"list bundle, map override", listBase, map[string]any{"gh.comment": map[string]any{}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sk := *tc.base
			out, err := applyStepOverride(Step{Skill: &sk},
				map[string]any{"skill": map[string]any{"verbs": tc.override}})
			if err != nil {
				t.Fatal(err)
			}
			if len(out.Skill.Verbs) != 1 || out.Skill.Verbs[0] != "gh.comment" {
				t.Fatalf("gh.merge survived a narrowing override: %v", out.Skill.Verbs)
			}
		})
	}
}

func containsSubstr(ss []string, sub string) bool {
	for _, s := range ss {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

// The ref-lint warns about a bound name used inside a code body and a
// {{ vault }} template — references the rewriter cannot reach.
func TestPackRefLintWarnings(t *testing.T) {
	dir := t.TempDir()
	writePackSource(t, dir, "src/p", `
pack:
  name: p
  version: 1.0.0
  requires:
    conductor: ">=0.1"
    stores: [cache]
    secrets: { tok: { desc: x } }
workflows:
  flow:
    steps:
      - id: a
        run: js
        code: "const v = ctx.store('cache'); return { v }"
      - id: b
        run: js
        code: "return { s: '{{ vault \"house\" \"k\" }}' }"
`)
	body := `
connectors: { gh: { use: github } }
vaults: { house: { type: file, dir: /tmp/pc-reflint } }
stores: { redis1: { type: boltdb, path: /tmp/pc-reflint.db } }
packs:
  p:
    source: ./src/p
    stores: { cache: redis1 }
    secrets: { tok: house/k }
`
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := resolveAndLoad(t, path)
	if err != nil {
		t.Fatalf("resolveAndLoad: %v", err)
	}
	ws := strings.Join(cfg.PackWarnings(), "\n")
	if !strings.Contains(ws, "code: step body references bound name(s) cache") {
		t.Errorf("expected a code-ref warning for 'cache', got:\n%s", ws)
	}
	if !strings.Contains(ws, "vault") {
		t.Errorf("expected a vault-template warning, got:\n%s", ws)
	}
}
