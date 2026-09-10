package config

import (
	"reflect"
	"strings"
	"testing"
)

// §C: a pack's skill grant is bounded by requires.connectors — enforced at
// lint (so the author sees it) and again at instantiate (so a pack that
// never ran lint still cannot exceed its interface).

func TestGrantConnector(t *testing.T) {
	tests := map[string]string{
		"github.submit_review": "github",
		"github.*":             "github",
		"*":                    "", // the full wildcard names no connector
		"":                     "",
		// A globbed connector half names no ONE connector — but see
		// splitGrant/TestBoundGrant: the verb suffix is not lost with it.
		"*.read":   "",
		"gh?.read": "",
	}
	for in, want := range tests {
		if got := grantConnector(in); got != want {
			t.Errorf("grantConnector(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestBoundGrant(t *testing.T) {
	tests := []struct {
		name           string
		patterns       []string
		declared       []string
		wantKept       []string
		wantDroppedAny bool
	}{
		{
			name:     "a wildcard expands to the declared connectors only",
			patterns: []string{"*"}, declared: []string{"github", "sentry"},
			wantKept: []string{"github.*", "sentry.*"},
		},
		{
			name:     "a declared connector passes through",
			patterns: []string{"github.*"}, declared: []string{"github"},
			wantKept: []string{"github.*"},
		},
		{
			name:     "a specific declared verb passes through",
			patterns: []string{"github.submit_review"}, declared: []string{"github"},
			wantKept: []string{"github.submit_review"},
		},
		{
			name:     "an undeclared connector is dropped",
			patterns: []string{"github.*", "pagerduty.*"}, declared: []string{"github"},
			wantKept: []string{"github.*"}, wantDroppedAny: true,
		},
		{
			// A globbed connector half is bounded to the declared set, but
			// the VERB restriction the author wrote survives. Expanding
			// `*.read` to `github.*` handed a read-only grant full write.
			name:     "a globbed connector half keeps its verb suffix",
			patterns: []string{"*.read"}, declared: []string{"github", "sentry"},
			wantKept: []string{"github.read", "sentry.read"},
		},
		{
			name:     "the bare wildcard still opens every verb",
			patterns: []string{"*"}, declared: []string{"github"},
			wantKept: []string{"github.*"},
		},
		{
			name:     "`*.*` is the bare wildcard spelled long",
			patterns: []string{"*.*"}, declared: []string{"github"},
			wantKept: []string{"github.*"},
		},
		{
			name:     "nothing declared means nothing granted",
			patterns: []string{"*"}, declared: nil,
			wantKept: nil, wantDroppedAny: true,
		},
		{
			name:     "an empty grant stays empty",
			patterns: nil, declared: []string{"github"},
			wantKept: nil,
		},
		{
			name:     "duplicate expansions collapse",
			patterns: []string{"*", "github.*"}, declared: []string{"github"},
			wantKept: []string{"github.*"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			kept, dropped := boundGrant(tc.patterns, tc.declared)
			if len(kept) == 0 && len(tc.wantKept) == 0 {
				// ok
			} else if !reflect.DeepEqual(kept, tc.wantKept) {
				t.Fatalf("kept = %v, want %v", kept, tc.wantKept)
			}
			if got := len(dropped) > 0; got != tc.wantDroppedAny {
				t.Fatalf("dropped = %v, wantAny = %v", dropped, tc.wantDroppedAny)
			}
		})
	}
}

// --- lint ------------------------------------------------------------------

func TestLintRejectsGrantOutsideRequires(t *testing.T) {
	man := &PackManifest{
		Pack: PackMeta{Name: "p", Version: "1.0.0", Requires: PackRequires{
			Conductor: ">=0.1", Connectors: ConnectorReqs{"github": AnyVersion},
		}},
		Workflows: map[string]WorkflowDef{"flow": {Steps: []Step{
			{ID: "reviewer", Type: "agent", Skill: &SkillPolicy{Verbs: []string{"github.submit_review", "pagerduty.trigger"}}},
		}}},
	}
	problems := strings.Join(LintPackManifest(man), "\n")
	if !strings.Contains(problems, "pagerduty") {
		t.Fatalf("lint should name the undeclared connector: %s", problems)
	}
	if !strings.Contains(problems, "requires.connectors") {
		t.Fatalf("lint should name the boundary: %s", problems)
	}
	if !strings.Contains(problems, "flow/reviewer") {
		t.Fatalf("lint should name the step: %s", problems)
	}
	if strings.Contains(problems, "github.submit_review") {
		t.Fatalf("a declared connector must not be flagged: %s", problems)
	}
}

func TestLintAcceptsGrantInsideRequires(t *testing.T) {
	man := &PackManifest{
		Pack: PackMeta{Name: "p", Version: "1.0.0", Requires: PackRequires{
			Conductor: ">=0.1", Connectors: ConnectorReqs{"github": AnyVersion, "sentry": AnyVersion},
		}},
		Workflows: map[string]WorkflowDef{"flow": {Steps: []Step{
			{ID: "a", Type: "agent", Skill: &SkillPolicy{Verbs: []string{"github.*", "sentry.issue"}}},
			{ID: "b", Type: "agent", Skill: &SkillPolicy{Verbs: []string{"*"}}}, // bounded at instantiate
		}}},
	}
	for _, p := range LintPackManifest(man) {
		if strings.Contains(p, "skill.verbs") {
			t.Fatalf("unexpected skill lint problem: %s", p)
		}
	}
}

// A wildcard with nothing declared grants nothing — worth saying out loud,
// because the author probably meant to declare something.
func TestLintFlagsWildcardWithNoDeclaredConnectors(t *testing.T) {
	man := &PackManifest{
		Pack: PackMeta{Name: "p", Version: "1.0.0", Requires: PackRequires{Conductor: ">=0.1"}},
		Workflows: map[string]WorkflowDef{"flow": {Steps: []Step{
			{ID: "a", Type: "agent", Skill: &SkillPolicy{Verbs: []string{"*"}}},
		}}},
	}
	problems := strings.Join(LintPackManifest(man), "\n")
	if !strings.Contains(problems, "declares no requires.connectors") {
		t.Fatalf("a wildcard with no declared connectors should be flagged: %s", problems)
	}
}

// --- instantiate (the belt) ------------------------------------------------

const skillPack = `
pack:
  name: granty
  version: "1.0.0"
  requires:
    conductor: ">=0.1"
    connectors: [github]
workflows:
  flow:
    steps:
      - id: reviewer
        type: agent
        skill:
          verbs: ["*"]
`

// A hand-authored pack that skipped lint still cannot exceed its interface:
// its `*` resolves to its declared connectors, rebound to the consumer's
// instance names.
func TestInstantiateBoundsPackWildcardToRequires(t *testing.T) {
	dir := t.TempDir()
	writePackSource(t, dir, "src/granty", skillPack)
	cfg, err := resolveAndLoad(t, writeDoc(t, dir, `
connectors:
  gh: { use: github, token: x }
  pd: { use: pagerduty, token: y }
packs:
  granty:
    source: ./src/granty
    connectors: { github: gh }
`))
	if err != nil {
		t.Fatal(err)
	}
	step := packStep(t, cfg, "granty/flow/reviewer")
	// `*` became the declared connector only, rebound gh.
	if !reflect.DeepEqual(step.Skill.Verbs, []string{"gh.*"}) {
		t.Fatalf("a pack wildcard must be bounded to its requires: got %v", step.Skill.Verbs)
	}
	// …and specifically not the consumer's other connector.
	for _, v := range step.Skill.Verbs {
		if strings.HasPrefix(v, "pd.") {
			t.Fatalf("the pack reached an undeclared connector: %v", step.Skill.Verbs)
		}
	}
}

// An undeclared NAMED connector is dropped at instantiate and surfaced.
func TestInstantiateDropsUndeclaredGrant(t *testing.T) {
	dir := t.TempDir()
	writePackSource(t, dir, "src/granty", strings.Replace(skillPack,
		`      verbs: ["*"]`, `      verbs: [github.comment, pagerduty.trigger]`, 1))
	cfg, err := resolveAndLoad(t, writeDoc(t, dir, `
connectors:
  gh: { use: github, token: x }
  pd: { use: pagerduty, token: y }
packs:
  granty:
    source: ./src/granty
    connectors: { github: gh }
`))
	if err != nil {
		t.Fatal(err)
	}
	got := packStep(t, cfg, "granty/flow/reviewer").Skill.Verbs
	if !reflect.DeepEqual(got, []string{"gh.comment"}) {
		t.Fatalf("the undeclared grant should be dropped, got %v", got)
	}
	warns := strings.Join(cfg.PackWarnings(), "\n")
	if !strings.Contains(warns, "pagerduty.trigger") || !strings.Contains(warns, "requires.connectors") {
		t.Fatalf("the drop must be surfaced: %s", warns)
	}
}

// A pack that declares nothing grants nothing, however broadly it asks.
func TestInstantiateWildcardWithNoRequiresGrantsNothing(t *testing.T) {
	dir := t.TempDir()
	writePackSource(t, dir, "src/granty", `
pack:
  name: granty
  version: "1.0.0"
  requires: { conductor: ">=0.1" }
workflows:
  flow:
    steps:
      - id: reviewer
        type: agent
        prompt: p
        skill: { verbs: ["*"] }
`)
	cfg, err := resolveAndLoad(t, writeDoc(t, dir, `
connectors:
  gh: { use: github, token: x }
packs:
  granty: { source: ./src/granty }
`))
	if err != nil {
		t.Fatal(err)
	}
	if got := packStep(t, cfg, "granty/flow/reviewer").Skill.Verbs; len(got) != 0 {
		t.Fatalf("a pack with no declared connectors must grant nothing, got %v", got)
	}
}
