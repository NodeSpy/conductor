package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Config reuse via YAML anchors (docs/design/anchors-reuse.md).
//
// The two properties under test are inseparable: `x-` holders must pass a
// STRICT decode, and everything that is not `x-` must still be caught by it.

func TestExtensionKeysAreIgnoredNotRejected(t *testing.T) {
	src := `
x-templates:
  reviewer: &reviewer
    type: agent
    workspace: worktree
x-anything-else: [1, 2, 3]
connectors:
  gh: { use: github }
`
	var c Config
	if err := strictUnmarshal([]byte(src), &c); err != nil {
		t.Fatalf("a top-level x- key must be ignored, got %v", err)
	}
	if len(c.ConnectorsMap) != 1 {
		t.Fatalf("the rest of the document must still decode: %+v", c.ConnectorsMap)
	}
}

// The exemption is the PREFIX and nothing more: an ordinary typo is still a
// named load error, which is the whole reason strict decode exists.
func TestStrictDecodeStillRejectsNonExtensionUnknownKeys(t *testing.T) {
	tests := map[string]string{
		"a top-level typo":              "connectors:\n  gh: { use: github }\nstpes: {}\n",
		"a plausible-looking section":   "connectors:\n  gh: { use: github }\nagents:\n  a: {}\n",
		"a key that merely contains x-": "connectors:\n  gh: { use: github }\nnot-x-templates: {}\n",
		"a nested unknown key":          "connectors:\n  gh: { use: github }\ntriggers:\n  github.pull_request: { filtres: {} }\n",
		"an unknown key inside a step":  "connectors:\n  gh: { use: github }\nworkflows:\n  w: { steps: [{ id: a, bogus: 1 }] }\n",
	}
	for name, src := range tests {
		t.Run(name, func(t *testing.T) {
			var c Config
			err := strictUnmarshal([]byte(src), &c)
			if err == nil {
				t.Fatalf("strict decode must still reject this:\n%s", src)
			}
			if !strings.Contains(err.Error(), "not found in type") {
				t.Fatalf("want an unknown-field error, got %v", err)
			}
		})
	}
}

// --- the merge itself ------------------------------------------------------

func TestMergeKeyResolvesAfterHolderIsDropped(t *testing.T) {
	src := `
x-templates:
  reviewer: &reviewer
    type: agent
    workspace: worktree
    model: claude-opus-5
connectors:
  gh: { use: github }
workflows:
  review:
    steps:
      - <<: *reviewer
        id: review
        prompt: "look at the diff"
`
	var c Config
	if err := strictUnmarshal([]byte(src), &c); err != nil {
		t.Fatalf("merge under strict decode: %v", err)
	}
	steps := c.Workflows["review"].Steps
	if len(steps) != 1 {
		t.Fatalf("steps = %+v", steps)
	}
	s := steps[0]
	if s.Type != "agent" || s.Workspace != "worktree" || s.Model.Ref != "claude-opus-5" {
		t.Fatalf("merged fields missing: %+v", s)
	}
	if s.ID != "review" || s.Prompt != "look at the diff" {
		t.Fatalf("the step's own fields were lost: %+v", s)
	}
}

// A step's own key wins over the merged one — plain YAML merge semantics,
// and the reason `<<:` is useful at all.
func TestStepKeysOverrideMergedKeys(t *testing.T) {
	src := `
x-t:
  base: &base { type: agent, workspace: worktree, model: heavy }
connectors:
  gh: { use: github }
workflows:
  w:
    steps:
      - <<: *base
        id: a
        workspace: local
`
	var c Config
	if err := strictUnmarshal([]byte(src), &c); err != nil {
		t.Fatal(err)
	}
	s := c.Workflows["w"].Steps[0]
	if s.Workspace != "local" {
		t.Fatalf("the step's own workspace must win, got %q", s.Workspace)
	}
	if s.Model.Ref != "heavy" {
		t.Fatalf("unset fields still merge, got %+v", s.Model)
	}
}

func TestMergeSequenceAndPlainAlias(t *testing.T) {
	src := `
x-t:
  a: &a { type: agent, workspace: worktree }
  b: &b { model: heavy }
  labels: &labels { team: autopilot }
connectors:
  gh: { use: github }
workflows:
  w:
    steps:
      - <<: [*a, *b]
        id: merged
      - id: aliased
        type: agent
        labels: *labels
`
	var c Config
	if err := strictUnmarshal([]byte(src), &c); err != nil {
		t.Fatal(err)
	}
	merged := c.Workflows["w"].Steps[0]
	if merged.Workspace != "worktree" || merged.Model.Ref != "heavy" {
		t.Fatalf("a merge sequence should combine both: %+v", merged)
	}
	aliased := c.Workflows["w"].Steps[1]
	if aliased.Labels["team"] != "autopilot" {
		t.Fatalf("a plain alias should resolve: %+v", aliased.Labels)
	}
}

// Anchors work in the sections a step is not — reuse is a YAML property, not
// a step feature.
func TestAnchorsWorkOnAnySection(t *testing.T) {
	src := `
x-t:
  iso: &iso { mode: namespace }
connectors:
  gh: { use: github }
runtimes:
  a: { use: cli, tool: claude, isolation: *iso }
  b: { use: cli, tool: codex, isolation: *iso }
`
	var c Config
	if err := strictUnmarshal([]byte(src), &c); err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"a", "b"} {
		if iso := c.Runtimes[n].Isolation; iso == nil || iso.Mode != "namespace" {
			t.Fatalf("runtime %s did not resolve the alias: %+v", n, iso)
		}
	}
	// …and the two must not share a pointer, or a later mutation of one
	// would silently rewrite the other.
	if c.Runtimes["a"].Isolation == c.Runtimes["b"].Isolation {
		t.Fatal("two aliases of one anchor must decode to independent values")
	}
}

// --- through the real loader ----------------------------------------------

func TestLoadAcceptsAnchorsInTheMainConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(`
x-templates:
  reviewer: &reviewer
    type: agent
    workspace: worktree
connectors:
  github: { use: github, token: x }
triggers:
  github.pull_request:
    steps:
      - <<: *reviewer
        id: review
        prompt: "review it"
`), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	s := cfg.Triggers[0].Steps[0]
	if s.Type != "agent" || s.Workspace != "worktree" || s.ID != "review" {
		t.Fatalf("merged step = %+v", s)
	}
}

// An IMPORTED file gets the same treatment: its own `x-` holder is dropped
// per file, which is also what keeps anchors from leaking across files.
func TestImportedFileMayCarryItsOwnAnchors(t *testing.T) {
	dir := writeTree(t, map[string]string{
		"config.yaml": `
imports: [conf.d/wf.yaml]
x-main:
  unused: &unused { type: agent }
connectors:
  gh: { use: github, token: x }
`,
		"conf.d/wf.yaml": `
x-templates:
  base: &base
    type: agent
    workspace: worktree
workflows:
  w:
    steps:
      - <<: *base
        id: a
        prompt: p
`,
	})
	cfg, err := Load(filepath.Join(dir, "config.yaml"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	s := cfg.Workflows["w"].Steps[0]
	if s.Workspace != "worktree" || s.ID != "a" {
		t.Fatalf("the imported file's anchor did not resolve: %+v", s)
	}
}

// Anchors are file-local — a YAML property, not a conductor limitation. An
// alias to another file's anchor is a parse error, and the docs point at
// `extends:` for cross-file reuse.
func TestAnchorsDoNotCrossImports(t *testing.T) {
	dir := writeTree(t, map[string]string{
		"config.yaml": `
imports: [conf.d/wf.yaml]
x-templates:
  base: &base { type: agent, workspace: worktree }
connectors:
  gh: { use: github, token: x }
`,
		"conf.d/wf.yaml": `
workflows:
  w:
    steps:
      - <<: *base
        id: a
`,
	})
	_, err := Load(filepath.Join(dir, "config.yaml"))
	if err == nil || !strings.Contains(err.Error(), "anchor") {
		t.Fatalf("an alias must not cross files, got %v", err)
	}
}

// A pack manifest is decoded by the same front door, so a pack author gets
// anchors too.
func TestPackManifestAcceptsAnchors(t *testing.T) {
	dir := t.TempDir()
	writePackSource(t, dir, "src/anchored", `
x-templates:
  reviewer: &reviewer
    type: agent
    workspace: worktree
pack:
  name: anchored
  version: "1.0.0"
  requires: { conductor: ">=0.1" }
workflows:
  flow:
    steps:
      - <<: *reviewer
        id: review
        prompt: "review"
`)
	cfg, err := resolveAndLoad(t, writeDoc(t, dir, `
connectors:
  gh: { use: github, token: x }
packs:
  anchored: { source: ./src/anchored }
`))
	if err != nil {
		t.Fatalf("a pack manifest must accept anchors: %v", err)
	}
	wf, ok := cfg.Workflows["anchored/flow"]
	if !ok {
		t.Fatalf("workflow missing, have %v", mapKeys(cfg.Workflows))
	}
	if s := wf.Steps[0]; s.Workspace != "worktree" || s.ID != "review" {
		t.Fatalf("pack anchor did not resolve: %+v", s)
	}
}

// --- the helpers -----------------------------------------------------------

func TestIsExtensionKey(t *testing.T) {
	for key, want := range map[string]bool{
		"x-templates": true, "x-": true, "x-a": true,
		"steps": false, "not-x-templates": false, "X-templates": false, "": false,
	} {
		if got := IsExtensionKey(key); got != want {
			t.Errorf("IsExtensionKey(%q) = %v, want %v", key, got, want)
		}
	}
}

func TestStripExtensionKeys(t *testing.T) {
	m := map[string]any{"x-a": 1, "x-b": 2, "connectors": map[string]any{"x-nested": 3}}
	StripExtensionKeys(m)
	if _, ok := m["x-a"]; ok {
		t.Fatal("top-level x- keys should be stripped")
	}
	conns := m["connectors"].(map[string]any)
	if _, ok := conns["x-nested"]; !ok {
		t.Fatal("nested x- keys must be untouched")
	}
}

// M5: `x-` is exempt because a DOCUMENT needs somewhere to park anchors.
// A step or a runtime has no such need, so an `x-` key nested inside one
// is a typo like any other. The re-entrant strict decode used to run the
// same top-level strip at every custom-UnmarshalYAML boundary, silently
// swallowing them.
func TestNestedExtensionKeysAreRejectedNotDropped(t *testing.T) {
	cases := map[string]string{
		"on a step":     "connectors:\n  gh: { use: github }\nworkflows:\n  w: { steps: [{ id: a, type: agent, prompt: p, x-note: hi }] }\n",
		"on a trigger":  "connectors:\n  gh: { use: github }\ntriggers:\n  - { on: gh.pull_request, x-note: hi, steps: [] }\n",
		"on a runtime":  "connectors:\n  gh: { use: github }\nruntimes:\n  r: { use: cli, tool: claude, x-note: hi }\n",
		"on a workflow": "connectors:\n  gh: { use: github }\nworkflows:\n  w: { steps: [], x-note: hi }\n",
	}
	for name, src := range cases {
		t.Run(name, func(t *testing.T) {
			var c Config
			err := strictUnmarshal([]byte(src), &c)
			if err == nil || !strings.Contains(err.Error(), "x-note") {
				t.Fatalf("a nested x- key must be an unknown-field error, got %v", err)
			}
		})
	}
}

// …while the document's own holder still passes, and its anchors still
// resolve into the very steps that now reject their own `x-` keys.
func TestTopLevelHolderStillExemptAlongsideStrictNesting(t *testing.T) {
	var c Config
	if err := strictUnmarshal([]byte(`
x-templates:
  base: &base { type: agent, workspace: worktree }
connectors:
  gh: { use: github }
workflows:
  w:
    steps: [{ <<: *base, id: a, prompt: p }]
`), &c); err != nil {
		t.Fatalf("the top-level holder must still be exempt: %v", err)
	}
	if got := c.Workflows["w"].Steps[0].Workspace; got != "worktree" {
		t.Fatalf("the anchor should still merge: %q", got)
	}
}
