package config

import (
	"reflect"
	"strings"
	"testing"
)

// The two reuse mechanisms, side by side
// (docs/design/merge-and-extends.md). What separates them is WHEN they
// run: `<<:` is finished before conductor sees the document, so it can
// only override; `extends:` hands conductor both layers, so it can append.

// stepOf loads a one-step workflow and returns the step.
func stepOf(t *testing.T, body string) Step {
	t.Helper()
	var c Config
	if err := strictUnmarshal([]byte(body), &c); err != nil {
		t.Fatalf("decode: %v\n---\n%s", err, body)
	}
	wf, ok := c.Workflows["w"]
	if !ok || len(wf.Steps) == 0 {
		t.Fatalf("no step decoded from:\n%s", body)
	}
	return wf.Steps[0]
}

const reuseBase = `
x-t:
  reviewer: &reviewer
    type: agent
    model: heavy
    workspace: worktree
    guidance: "Terse and human."
    labels: { team: autopilot, tier: base }
    skill: { verbs: [github.comment], secrets_via: broker }
`

// --- <<: stays dumb ---------------------------------------------------------

// The whole point of keeping both: `<<:` overrides everything the child
// sets, LISTS included. Nothing about extends: may leak into it.
func TestMergeKeyOverridesEvenLists(t *testing.T) {
	s := stepOf(t, reuseBase+`
workflows:
  w:
    steps:
      - <<: *reviewer
        id: a
        model: light
        guidance: "Only this."
        labels: { role: ci }
        skill: { verbs: [github.submit_review] }
`)
	if s.Model.Ref != "light" {
		t.Errorf("scalar override: model = %q", s.Model.Ref)
	}
	if s.Workspace != "worktree" {
		t.Errorf("an unset key still comes from the base: workspace = %q", s.Workspace)
	}
	if got := s.Skill.Verbs; !reflect.DeepEqual(got, []string{"github.submit_review"}) {
		t.Errorf("`<<:` must REPLACE a list, not append: %v", got)
	}
	if got := s.Guidance.Parts; !reflect.DeepEqual(got, []string{"Only this."}) {
		t.Errorf("`<<:` must REPLACE guidance, not stack it: %v", got)
	}
	// A merged map is replaced wholesale too — that is plain YAML, and
	// the sharpest difference from extends:, which would deep-merge.
	if !reflect.DeepEqual(s.Labels, map[string]string{"role": "ci"}) {
		t.Errorf("`<<:` must replace a whole map, not deep-merge it: %v", s.Labels)
	}
}

// --- extends: is field-aware ------------------------------------------------

func TestExtendsScalarsOverrideListsAppend(t *testing.T) {
	s := stepOf(t, reuseBase+`
workflows:
  w:
    steps:
      - extends: *reviewer
        id: a
        model: light
        skill: { verbs: [linear.create] }
`)
	if s.Model.Ref != "light" {
		t.Errorf("scalar: child overrides, got %q", s.Model.Ref)
	}
	if s.Workspace != "worktree" {
		t.Errorf("scalar: unset inherits, got %q", s.Workspace)
	}
	want := []string{"github.comment", "linear.create"}
	if got := s.Skill.Verbs; !reflect.DeepEqual(got, want) {
		t.Errorf("list: base items then child items, got %v want %v", got, want)
	}
	// The child's skill map named only `verbs`; the base's other key
	// survives the deep-merge.
	if s.Skill.SecretsVia != "broker" {
		t.Errorf("map: deep-merge, got secrets_via %q", s.Skill.SecretsVia)
	}
}

func TestExtendsDeepMergesMaps(t *testing.T) {
	s := stepOf(t, reuseBase+`
workflows:
  w:
    steps:
      - extends: *reviewer
        id: a
        labels: { tier: fixer, role: ci }
`)
	want := map[string]string{"team": "autopilot", "tier": "fixer", "role": "ci"}
	if !reflect.DeepEqual(s.Labels, want) {
		t.Errorf("labels deep-merge = %v, want %v", s.Labels, want)
	}
}

// Guidance stacks with NO opt-in. This is the behavior the escape hatches
// below exist to escape.
func TestExtendsGuidanceAppendsByDefault(t *testing.T) {
	s := stepOf(t, reuseBase+`
workflows:
  w:
    steps:
      - extends: *reviewer
        id: a
        guidance: "Also cite file:line."
`)
	want := []string{"Terse and human.", "Also cite file:line."}
	if got := s.Guidance.Parts; !reflect.DeepEqual(got, want) {
		t.Fatalf("guidance must stack base-under-child with no opt-in: got %v, want %v", got, want)
	}
}

// A chain resolves base-first, so the outermost layer speaks last.
func TestExtendsChainStacksBaseFirst(t *testing.T) {
	s := stepOf(t, `
x-t:
  house: &house
    type: agent
    guidance: "House voice."
    skill: { verbs: [a.one] }
  team: &team
    extends: *house
    guidance: "Team voice."
    skill: { verbs: [a.two] }
workflows:
  w:
    steps:
      - extends: *team
        id: a
        guidance: "Step voice."
        skill: { verbs: [a.three] }
`)
	wantG := []string{"House voice.", "Team voice.", "Step voice."}
	if got := s.Guidance.Parts; !reflect.DeepEqual(got, wantG) {
		t.Errorf("guidance stack = %v, want %v", got, wantG)
	}
	wantV := []string{"a.one", "a.two", "a.three"}
	if got := s.Skill.Verbs; !reflect.DeepEqual(got, wantV) {
		t.Errorf("list append through a chain = %v, want %v", got, wantV)
	}
}

// --- the escape hatches -----------------------------------------------------

// THE pair that makes always-append safe: without a way out, a base's tone
// would be unremovable.
func TestOverrideAndResetEscapeTheAppend(t *testing.T) {
	over := stepOf(t, reuseBase+`
workflows:
  w:
    steps:
      - extends: *reviewer
        id: a
        guidance: !override "Only this."
        skill: { verbs: !override [linear.create] }
`)
	if got := over.Guidance.Parts; !reflect.DeepEqual(got, []string{"Only this."}) {
		t.Errorf("!override guidance must replace, got %v", got)
	}
	if got := over.Skill.Verbs; !reflect.DeepEqual(got, []string{"linear.create"}) {
		t.Errorf("!override list must replace, got %v", got)
	}

	reset := stepOf(t, reuseBase+`
workflows:
  w:
    steps:
      - extends: *reviewer
        id: a
        guidance: !reset
        labels: !reset
        skill: { verbs: !reset }
`)
	if reset.Guidance != nil && len(reset.Guidance.Parts) > 0 {
		t.Errorf("!reset guidance must drop the inherited value, got %+v", reset.Guidance)
	}
	if len(reset.Labels) != 0 {
		t.Errorf("!reset must drop an inherited map, got %v", reset.Labels)
	}
	if len(reset.Skill.Verbs) != 0 {
		t.Errorf("!reset must drop an inherited list, got %v", reset.Skill.Verbs)
	}
	// …and only what it named: the rest of the base is untouched.
	if reset.Model.Ref != "heavy" || reset.Skill.SecretsVia != "broker" {
		t.Errorf("!reset must be per-key, got model=%q secrets_via=%q", reset.Model.Ref, reset.Skill.SecretsVia)
	}
}

// The legacy spelling (#149) predates the tags and means the same thing.
func TestGuidanceReplaceIsOverride(t *testing.T) {
	legacy := stepOf(t, reuseBase+`
workflows:
  w:
    steps:
      - extends: *reviewer
        id: a
        guidance: { replace: "Only this." }
`)
	tagged := stepOf(t, reuseBase+`
workflows:
  w:
    steps:
      - extends: *reviewer
        id: a
        guidance: !override "Only this."
`)
	if !reflect.DeepEqual(legacy.Guidance.Parts, tagged.Guidance.Parts) {
		t.Fatalf("{replace:} and !override must agree: %v vs %v", legacy.Guidance.Parts, tagged.Guidance.Parts)
	}
	if got := legacy.Guidance.Parts; !reflect.DeepEqual(got, []string{"Only this."}) {
		t.Fatalf("guidance = %v, want [Only this.]", got)
	}
}

// A tag on a field nothing was inherited for means the same with or
// without the base, so it is consumed rather than failing the load.
func TestTagsWithoutABaseAreHarmless(t *testing.T) {
	s := stepOf(t, `
workflows:
  w:
    steps:
      - id: a
        type: agent
        prompt: p
        guidance: !override "Mine."
        labels: !reset
`)
	if got := s.Guidance.Parts; !reflect.DeepEqual(got, []string{"Mine."}) {
		t.Errorf("!override with no base is just the value, got %v", got)
	}
	if len(s.Labels) != 0 {
		t.Errorf("!reset with no base is absent, got %v", s.Labels)
	}
}

// --- precedence, identity, bounds -------------------------------------------

// Both on one step is unusual, but defined: `<<:` is YAML-level and lands
// first, then `extends:` merges field-aware on top of the result.
func TestMergeKeyResolvesBeforeExtends(t *testing.T) {
	s := stepOf(t, `
x-t:
  yaml_base: &yaml_base { type: agent, guidance: "From <<.", model: m1 }
  smart_base: &smart_base { guidance: "From extends.", workspace: worktree }
workflows:
  w:
    steps:
      - <<: *yaml_base
        extends: *smart_base
        id: a
        prompt: p
`)
	// `<<:` supplied the step's own guidance; extends: then stacked its
	// base UNDER it.
	want := []string{"From extends.", "From <<."}
	if got := s.Guidance.Parts; !reflect.DeepEqual(got, want) {
		t.Fatalf("guidance = %v, want %v (<<: resolves first, extends: layers under it)", got, want)
	}
	if s.Model.Ref != "m1" || s.Workspace != "worktree" {
		t.Fatalf("both layers should contribute: %+v", s)
	}
}

// extends: is a CONFIG merge. It says nothing about who the step is.
func TestExtendsDoesNotTouchIdentity(t *testing.T) {
	body := `
x-t:
  named: &named { type: agent, name: base-name, model: m }
workflows:
  w:
    steps:
      - extends: *named
        id: mine
        prompt: p
      - id: other
        type: agent
        prompt: p
`
	var c Config
	if err := strictUnmarshal([]byte(body), &c); err != nil {
		t.Fatal(err)
	}
	steps := c.Workflows["w"].Steps
	// A `name:` in the base is ordinary config and merges like one — the
	// point is that extends: itself contributes nothing to identity.
	if got := steps[0].Identity(WorkflowScope("w"), 0); got != "base-name" {
		t.Fatalf("a merged name: is still just the name, got %q", got)
	}
	// Without one, identity is structural on the step's OWN slot — the
	// base it extended is irrelevant.
	nameless := stepOf(t, `
x-t:
  anon: &anon { type: agent, model: m }
workflows:
  w:
    steps:
      - extends: *anon
        id: mine
        prompt: p
`)
	if got := nameless.Identity(WorkflowScope("w"), 0); got != "workflow:w/mine" {
		t.Fatalf("identity should be structural on this step's slot, got %q", got)
	}
	if got := steps[1].Identity(WorkflowScope("w"), 1); got != "workflow:w/other" {
		t.Fatalf("a step that extends nothing is unaffected, got %q", got)
	}
}

// An alias cannot loop (YAML requires the anchor first), but a chain of
// inline maps can nest without limit. It must error, not hang.
func TestInlineExtendsChainIsBounded(t *testing.T) {
	var b strings.Builder
	b.WriteString("workflows:\n  w:\n    steps:\n      - id: a\n        type: agent\n        prompt: p\n")
	indent := "        "
	for i := 0; i < maxExtendsDepth+5; i++ {
		b.WriteString(indent + "extends:\n")
		indent += "  "
		b.WriteString(indent + "type: agent\n")
	}
	var c Config
	err := strictUnmarshal([]byte(b.String()), &c)
	if err == nil || !strings.Contains(err.Error(), "chain is more than") {
		t.Fatalf("a runaway inline extends: chain must be a bounded error, got %v", err)
	}
}

func TestExtendsRejectsANonMapTarget(t *testing.T) {
	var c Config
	err := strictUnmarshal([]byte("workflows:\n  w: { steps: [{ id: a, extends: reviewer }] }\n"), &c)
	if err == nil || !strings.Contains(err.Error(), "anchor alias or an inline map") {
		t.Fatalf("a bare name is not a step extends: target (there is no registry), got %v", err)
	}
}

// extends: works wherever a step does, including a pack manifest — same
// anchors, same merge.
func TestExtendsWorksInAPackManifest(t *testing.T) {
	dir := t.TempDir()
	writePackSource(t, dir, "src/kit", `
x-t:
  house: &house
    type: agent
    guidance: "Pack voice."
    skill: { verbs: [github.comment] }
pack:
  name: kit
  version: "1.0.0"
  requires: { conductor: ">=0.1", connectors: [github] }
workflows:
  flow:
    steps:
      - extends: *house
        id: review
        guidance: "Cite file:line."
        prompt: "review"
`)
	cfg, err := resolveAndLoad(t, writeDoc(t, dir, `
connectors:
  gh: { use: github, token: x }
packs:
  kit: { source: ./src/kit, connectors: { github: gh } }
`))
	if err != nil {
		t.Fatalf("a pack manifest must support extends:: %v", err)
	}
	s := packStep(t, cfg, "kit/flow/review")
	if got := s.Guidance.Parts; !reflect.DeepEqual(got, []string{"Pack voice.", "Cite file:line."}) {
		t.Fatalf("pack guidance should stack, got %v", got)
	}
	// …and the pack's grant still went through the boundary rebind.
	if got := s.Skill.Verbs; !reflect.DeepEqual(got, []string{"gh.comment"}) {
		t.Fatalf("merged grant should still be rebound and bounded, got %v", got)
	}
}
