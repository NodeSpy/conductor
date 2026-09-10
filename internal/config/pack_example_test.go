package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// reviewKitDir locates the shipped example pack relative to this package.
func reviewKitDir(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	// internal/config -> repo root -> examples/packs/review-kit
	dir := filepath.Join(wd, "..", "..", "examples", "packs", "review-kit")
	if _, err := os.Stat(filepath.Join(dir, PackManifestFile)); err != nil {
		t.Skipf("example pack not found at %s: %v", dir, err)
	}
	abs, _ := filepath.Abs(dir)
	return abs
}

// TestExampleReviewKitLints guards the shipped reference pack: it must stay
// well-formed (this is the format example authors copy).
func TestExampleReviewKitLints(t *testing.T) {
	problems, err := LintPackDir(reviewKitDir(t))
	if err != nil {
		t.Fatalf("lint load: %v", err)
	}
	if len(problems) != 0 {
		t.Fatalf("example review-kit pack should lint clean, got: %v", problems)
	}
}

// TestExampleReviewKitInstantiates instantiates the real example pack end to
// end: resolve (local copy) -> load -> namespace + bind + settings + disarmed
// trigger. It exercises the same manifest a consumer installs.
func TestExampleReviewKitInstantiates(t *testing.T) {
	dir := t.TempDir()
	src := reviewKitDir(t)
	body := `
connectors:
  gh:
    use: github
vaults:
  house:
    type: file
    dir: /tmp/pc-pack-vault
steps:
  my-opus:
    type: agent
    name: my-opus
    skill:
      verbs: [github.submit_review, github.comment]
packs:
  review:
    source: ` + src + `
    preset: codex
    connectors: { github: gh }
    secrets:    { review_token: house/review }
    steps:      { reviewer: my-opus }
    triggers:
      on_review_request:
        enabled: true
        repos: [your-org/app]
`
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := resolveAndLoad(t, path)
	if err != nil {
		t.Fatalf("resolveAndLoad the example pack: %v", err)
	}

	// Namespaced workflow present; bound reviewer resolves to the global.
	wf, ok := cfg.Workflows["review/review-flow"]
	if !ok {
		t.Fatalf("expected review/review-flow, have %v", workflowKeys(cfg))
	}
	if wf.Steps[0].Extends != "my-opus" {
		t.Fatalf("bound reviewer should resolve to my-opus, got %q", wf.Steps[0].Extends)
	}
	// Preset codex applied: heavy_model=gpt-5-pro substituted into the prompt.
	if !strings.Contains(wf.Steps[0].Prompt, "gpt-5-pro") {
		t.Fatalf("preset settings substitution failed, prompt=%q", wf.Steps[0].Prompt)
	}
	// The bundled handoff agent's skill.verbs connector prefix is rebound
	// github.* -> gh.* (the consumer's connector name).
	if h, ok := cfg.Steps["review/handoff"]; !ok || h.Skill == nil {
		t.Fatalf("expected review/handoff with a skill block")
	} else {
		for _, v := range h.Skill.Verbs {
			if strings.HasPrefix(v, "github.") {
				t.Fatalf("skill.verbs connector prefix not rebound: %q", v)
			}
		}
		if !contains(h.Skill.Verbs, "gh.submit_review") {
			t.Fatalf("expected gh.submit_review in handoff skill.verbs, got %v", h.Skill.Verbs)
		}
	}

	// Trigger armed + scoped.
	tr := findTrigger(cfg, "review/on_review_request")
	if tr == nil || tr.Enabled == nil || !*tr.Enabled {
		t.Fatalf("on_review_request should be armed")
	}
	if tr.On != "gh.review_requested" {
		t.Fatalf("trigger connector rebind failed, got %q", tr.On)
	}
}
