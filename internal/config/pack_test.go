package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writePackSource writes a pack manifest into <dir>/<rel>/conductor-pack.yaml.
func writePackSource(t *testing.T, dir, rel, manifest string) {
	t.Helper()
	d := filepath.Join(dir, rel)
	if err := os.MkdirAll(d, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(d, PackManifestFile), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
}

// resolveAndLoad runs the init-style resolve then loads the config, mirroring
// the daemon's boot after `conductor init`.
func resolveAndLoad(t *testing.T, path string) (*Config, error) {
	t.Helper()
	if _, err := ResolvePacks(path); err != nil {
		return nil, err
	}
	return Load(path)
}

const reviewKitManifest = `
pack:
  name: review-kit
  version: 1.2.0
  description: Multi-lens PR review with human handoff
  requires:
    conductor: ">=0.1"
    connectors: [github]
    stores: [cache]
    secrets:
      api_token: { desc: token the review-poster uses }
    roles:
      handoff:  { skill: [github.submit_review] }
      reviewer: {}
settings:
  heavy_model: { type: string, default: default-heavy }
presets:
  codex: { heavy_model: gpt-5-pro }
exports:
  workflows: [review-flow]
agents:
  reviewer:
    workspace: worktree
  handoff:
    workspace: local
    skill:
      secrets_via: broker
      allow_secrets: [api_token]
workflows:
  review-flow:
    steps:
      - id: review
        type: agent
        agent: reviewer
        prompt: "review with {{.repo}} using ${settings.heavy_model}"
      - id: post
        uses: github.comment
        options: { store: cache, text: "done" }
checks:
  lint: { run: js, code: "return { pass: true }" }
triggers:
  - name: on_review_request
    on: github.review_requested
    steps:
      - id: run
        workflow: review-flow
    hooks:
      - { at: done, uses: github.comment, options: { text: "done" } }
`

// baseConfigWithReview writes a consumer config that instantiates review-kit
// from a local source, binding github->gh and the store/secret, binding the
// reviewer role to a global and overriding handoff.
func baseConfigWithReview(t *testing.T, extraPackFields string) string {
	t.Helper()
	dir := t.TempDir()
	writePackSource(t, dir, "src/review-kit", reviewKitManifest)
	body := `
connectors:
  gh:
    type: github
stores:
  redis1:
    type: boltdb
    path: /tmp/pc-pack-test.db
vaults:
  house:
    type: file
    dir: /tmp/pc-pack-vault
agents:
  my-opus:
    provider: claude
    skill:
      verbs: [github.submit_review]
packs:
  review:
    source: ./src/review-kit
    version: 1.2.0
    settings: { heavy_model: gpt-5-pro }
    connectors: { github: gh }
    stores:     { cache: redis1 }
    secrets:    { api_token: house/foocorp }
    agents:
      reviewer: my-opus
      handoff:  { workspace: worktree }
    triggers:
      on_review_request:
        enabled: true
        repos: [acme/app, acme/api]
` + extraPackFields
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestPackInstantiateNamespaceAndBind(t *testing.T) {
	path := baseConfigWithReview(t, "")
	cfg, err := resolveAndLoad(t, path)
	if err != nil {
		t.Fatalf("resolveAndLoad: %v", err)
	}

	// Workflow is namespaced under the instance name.
	wf, ok := cfg.Workflows["review/review-flow"]
	if !ok {
		t.Fatalf("expected namespaced workflow review/review-flow, have %v", workflowKeys(cfg))
	}
	if _, collide := cfg.Workflows["review-flow"]; collide {
		t.Fatal("bare workflow name must not leak into the main namespace")
	}

	// Agent bind: reviewer -> my-opus (global), so the step ref resolves to the
	// global and NO namespaced copy is emitted.
	if _, leaked := cfg.Agents["review/reviewer"]; leaked {
		t.Fatal("a bound role must not emit a namespaced agent copy")
	}
	if got := wf.Steps[0].Agent; got != "my-opus" {
		t.Fatalf("bound agent ref should resolve to the global my-opus, got %q", got)
	}

	// Agent override: handoff kept the bundle and merged the override.
	h, ok := cfg.Agents["review/handoff"]
	if !ok {
		t.Fatalf("expected namespaced agent review/handoff, have %v", agentKeys(cfg))
	}
	if h.Workspace != "worktree" {
		t.Fatalf("handoff override workspace=worktree, got %q", h.Workspace)
	}
	// Secret rebind on the overridden agent: api_token -> house/foocorp.
	if h.Skill == nil || len(h.Skill.AllowSecrets) != 1 || h.Skill.AllowSecrets[0] != "house/foocorp" {
		t.Fatalf("handoff allow_secrets should rebind api_token->house/foocorp, got %+v", h.Skill)
	}

	// Settings substitution into the prompt.
	if !strings.Contains(wf.Steps[0].Prompt, "gpt-5-pro") {
		t.Fatalf("settings substitution failed, prompt=%q", wf.Steps[0].Prompt)
	}
	if strings.Contains(wf.Steps[0].Prompt, "${settings") {
		t.Fatalf("unsubstituted settings token remains: %q", wf.Steps[0].Prompt)
	}

	// Connector + store rebind inside the workflow verb step.
	if wf.Steps[1].Uses != "gh.comment" {
		t.Fatalf("verb connector rebind github->gh failed, got %q", wf.Steps[1].Uses)
	}
	if got, _ := wf.Steps[1].Options["store"].(string); got != "redis1" {
		t.Fatalf("store selector rebind cache->redis1 failed, got %q", got)
	}

	// Check namespaced + workflow ref namespaced.
	if _, ok := cfg.Checks["review/lint"]; !ok {
		t.Fatalf("expected namespaced check review/lint, have %v", checkKeys(cfg))
	}
}

func TestPackDisarmedTriggerArming(t *testing.T) {
	path := baseConfigWithReview(t, "")
	cfg, err := resolveAndLoad(t, path)
	if err != nil {
		t.Fatalf("resolveAndLoad: %v", err)
	}
	tr := findTrigger(cfg, "review/on_review_request")
	if tr == nil {
		t.Fatalf("expected namespaced trigger review/on_review_request, triggers=%v", triggerNamesOf(cfg))
	}
	// Armed by the instance block.
	if tr.Enabled == nil || !*tr.Enabled {
		t.Fatal("trigger should be armed (enabled:true) by the instance block")
	}
	// Repo scope is the consent.
	repos, _ := tr.Filters["repos"].([]any)
	if len(repos) != 2 {
		t.Fatalf("trigger repos should be [acme/app acme/api], got %v", tr.Filters["repos"])
	}
	// Connector rebind on the trigger source and hook.
	if tr.On != "gh.review_requested" {
		t.Fatalf("trigger on: connector rebind failed, got %q", tr.On)
	}
	if tr.Hooks[0].Uses != "gh.comment" {
		t.Fatalf("hook connector rebind failed, got %q", tr.Hooks[0].Uses)
	}
	// The workflow step ref inside the trigger is namespaced.
	if tr.Steps[0].Workflow != "review/review-flow" {
		t.Fatalf("trigger workflow ref namespacing failed, got %q", tr.Steps[0].Workflow)
	}
}

func TestPackTriggerDisarmedByDefault(t *testing.T) {
	// No triggers: block in the instance => the shipped trigger stays inert.
	dir := t.TempDir()
	writePackSource(t, dir, "src/review-kit", reviewKitManifest)
	body := `
connectors: { gh: { type: github } }
stores: { redis1: { type: boltdb, path: /tmp/x.db } }
vaults: { house: { type: file, dir: /tmp/pc-pack-vault } }
agents: { my-opus: { provider: claude, skill: { verbs: [github.submit_review] } } }
packs:
  review:
    source: ./src/review-kit
    connectors: { github: gh }
    stores: { cache: redis1 }
    secrets: { api_token: house/foocorp }
    agents: { reviewer: my-opus }
`
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := resolveAndLoad(t, path)
	if err != nil {
		t.Fatalf("resolveAndLoad: %v", err)
	}
	tr := findTrigger(cfg, "review/on_review_request")
	if tr == nil {
		t.Fatal("shipped trigger should still be present")
	}
	if tr.Enabled != nil && *tr.Enabled {
		t.Fatal("an un-armed shipped trigger must be disabled (inert)")
	}
	if _, ok := tr.Filters["repos"]; ok {
		t.Fatal("an un-armed shipped trigger must carry no repo scope")
	}
}

func TestPackRejectsShippedEnvironment(t *testing.T) {
	dir := t.TempDir()
	writePackSource(t, dir, "src/bad", `
pack:
  name: bad-pack
  version: 0.1.0
connectors:
  smuggled:
    type: github
    identity: { read_token: leaked }
`)
	body := `
connectors: { gh: { type: github } }
packs:
  bad:
    source: ./src/bad
`
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := resolveAndLoad(t, path)
	if err == nil {
		t.Fatal("a pack shipping connectors: must be rejected")
	}
	if !strings.Contains(err.Error(), "bind-only") || !strings.Contains(err.Error(), "connectors") {
		t.Fatalf("rejection should name the bind-only violation, got: %v", err)
	}
}

func TestPackUnsatisfiedRequireErrors(t *testing.T) {
	dir := t.TempDir()
	writePackSource(t, dir, "src/needs", `
pack:
  name: needs-gh
  version: 0.1.0
  requires:
    connectors: [github]
workflows:
  flow: { steps: [ { id: x, run: js, code: "return {}" } ] }
`)
	// Instance does NOT bind github.
	body := `
connectors: { gh: { type: github } }
packs:
  needs:
    source: ./src/needs
`
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := resolveAndLoad(t, path)
	if err == nil || !strings.Contains(err.Error(), "requires connector") {
		t.Fatalf("an unbound required connector should error, got: %v", err)
	}
}

func TestPackConductorVersionConstraint(t *testing.T) {
	old := runtimeVersion
	SetRuntimeVersion("0.5.0")
	defer SetRuntimeVersion(old)

	dir := t.TempDir()
	writePackSource(t, dir, "src/newpack", `
pack:
  name: newpack
  version: 1.0.0
  requires:
    conductor: ">=0.8"
workflows:
  flow: { steps: [ { id: x, run: js, code: "return {}" } ] }
`)
	body := `
connectors: { gh: { type: github } }
packs:
  np:
    source: ./src/newpack
`
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := resolveAndLoad(t, path)
	if err == nil || !strings.Contains(err.Error(), "requires conductor") {
		t.Fatalf("a daemon below the pack's required version should error, got: %v", err)
	}
}

func TestCheckConductorConstraint(t *testing.T) {
	cases := []struct {
		constraint, version string
		ok                  bool
	}{
		{">=0.8", "0.9.0", true},
		{">=0.8", "0.7.9", false},
		{"^1.2", "1.5.0", true},
		{"^1.2", "2.0.0", false},
		{"^0.8", "0.8.5", true},   // 0.x caret: locked to the minor
		{"^0.8", "0.9.0", false},  // 0.9 is NOT compatible with ^0.8
		{"^0.0.3", "0.0.3", true}, // 0.0.z caret: exact patch
		{"^0.0.3", "0.0.4", false},
		{"~1.2.3", "1.2.9", true},
		{"~1.2.3", "1.3.0", false},
		{">=0.8 <2.0", "1.4.0", true},
		{">=0.8 <2.0", "2.1.0", false},
		{"", "1.0.0", true},         // no constraint
		{">=0.8", "dev", true},      // dev build skips
		{"garbage", "1.0.0", false}, // unrecognized -> error, don't assume compatible
	}
	for _, c := range cases {
		err := checkConductorConstraint(c.constraint, c.version)
		if (err == nil) != c.ok {
			t.Errorf("checkConductorConstraint(%q,%q): ok=%v err=%v", c.constraint, c.version, c.ok, err)
		}
	}
}

func TestParseSource(t *testing.T) {
	base := "/cfg"
	cases := []struct {
		in                         string
		git                        bool
		gitURL, subdir, ref, local string
	}{
		{in: "./packs/review", local: "/cfg/packs/review"},
		{in: "/abs/pack", local: "/abs/pack"},
		{in: "file:///abs/pack", local: "/abs/pack"},
		{in: "github.com/acme/packs//review-kit@v1.2.0", git: true, gitURL: "https://github.com/acme/packs", subdir: "review-kit", ref: "v1.2.0"},
		{in: "https://github.com/acme/packs.git//review-kit@main", git: true, gitURL: "https://github.com/acme/packs.git", subdir: "review-kit", ref: "main"},
		{in: "git::ssh://git@github.com/acme/packs//sub", git: true, gitURL: "ssh://git@github.com/acme/packs", subdir: "sub"},
		{in: "git::file:///tmp/repo//kit@v2", git: true, gitURL: "file:///tmp/repo", subdir: "kit", ref: "v2"},
	}
	for _, c := range cases {
		got, err := parseSource(c.in, base)
		if err != nil {
			t.Errorf("parseSource(%q): %v", c.in, err)
			continue
		}
		if got.git != c.git || got.gitURL != c.gitURL || got.subdir != c.subdir || got.ref != c.ref || got.local != c.local {
			t.Errorf("parseSource(%q) = %+v, want git=%v url=%q subdir=%q ref=%q local=%q",
				c.in, got, c.git, c.gitURL, c.subdir, c.ref, c.local)
		}
	}
}

func TestParseSourceRejectsInjectionAndTraversal(t *testing.T) {
	bad := []string{
		"git::--upload-pack=evil",        // leading '-' => git option injection
		"git::ssh://h/r@-somebranch",     // ref begins with '-'
		"github.com/o/r//../../etc@main", // subdir traversal via ..
		"github.com/o/r//sub/../../x",    // .. in a later segment
	}
	for _, s := range bad {
		if _, err := parseSource(s, "/cfg"); err == nil {
			t.Errorf("parseSource(%q) should be rejected (injection/traversal), got nil error", s)
		}
	}
	// A normal source with a ref that merely CONTAINS a dash is fine.
	if _, err := parseSource("github.com/o/r//kit@release-1.2", "/cfg"); err != nil {
		t.Errorf("a normal dashed ref should parse: %v", err)
	}
}

func TestCopyTreeSkipsSymlinks(t *testing.T) {
	src := t.TempDir()
	secret := filepath.Join(src, "regular.txt")
	if err := os.WriteFile(secret, []byte("ok"), 0o644); err != nil {
		t.Fatal(err)
	}
	// A symlink pointing outside the tree must not be copied (no file, and
	// certainly not the target's contents).
	link := filepath.Join(src, "escape")
	if err := os.Symlink("/etc/hostname", link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	dst := filepath.Join(t.TempDir(), "out")
	if err := copyTree(src, dst); err != nil {
		t.Fatalf("copyTree: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(dst, "escape")); !os.IsNotExist(err) {
		t.Fatalf("symlink should be skipped, but %q exists", filepath.Join(dst, "escape"))
	}
	if _, err := os.Stat(filepath.Join(dst, "regular.txt")); err != nil {
		t.Fatalf("regular file should be copied: %v", err)
	}
}

func TestPackLockfileWritten(t *testing.T) {
	path := baseConfigWithReview(t, "")
	lock, err := ResolvePacks(path)
	if err != nil {
		t.Fatalf("ResolvePacks: %v", err)
	}
	if len(lock.Packs) != 1 {
		t.Fatalf("expected 1 lock entry, got %d", len(lock.Packs))
	}
	e := lock.Packs[0]
	if e.Instance != "review" || e.Name != "review-kit" || e.Resolved != "local" {
		t.Fatalf("unexpected lock entry: %+v", e)
	}
	if !strings.HasPrefix(e.Digest, "sha256:") {
		t.Fatalf("lock entry should carry a sha256 digest, got %q", e.Digest)
	}
	// Lockfile is on disk next to the config.
	if _, err := os.Stat(filepath.Join(filepath.Dir(path), LockfileName)); err != nil {
		t.Fatalf("lockfile not written: %v", err)
	}
	// It round-trips.
	got, err := ReadLockfile(filepath.Dir(path))
	if err != nil || got == nil || len(got.Packs) != 1 {
		t.Fatalf("ReadLockfile: %v (%+v)", err, got)
	}
}

func TestPackMissingVendorErrorsClearly(t *testing.T) {
	// A packs: block but no `conductor init` (no vendored packs) → a clear,
	// actionable error, not a crash.
	dir := t.TempDir()
	writePackSource(t, dir, "src/review-kit", reviewKitManifest)
	body := `
connectors: { gh: { type: github } }
stores: { redis1: { type: boltdb, path: /tmp/x.db } }
vaults: { house: { type: file, dir: /tmp/pc-pack-vault } }
agents: { my-opus: { provider: claude } }
packs:
  review:
    source: ./src/review-kit
    connectors: { github: gh }
    stores: { cache: redis1 }
    secrets: { api_token: house/foocorp }
    agents: { reviewer: my-opus }
`
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := Load(path) // no ResolvePacks first
	if err == nil || !strings.Contains(err.Error(), "conductor init") {
		t.Fatalf("missing vendored pack should point to `conductor init`, got: %v", err)
	}
}

// --- small accessors for assertions ---

func workflowKeys(c *Config) []string { return mapKeys(c.Workflows) }
func agentKeys(c *Config) []string    { return mapKeys(c.Agents) }
func checkKeys(c *Config) []string    { return mapKeys(c.Checks) }

func findTrigger(c *Config, name string) *TriggerSpec {
	for i := range c.Triggers {
		if c.Triggers[i].Name == name {
			return &c.Triggers[i]
		}
	}
	return nil
}

func triggerNamesOf(c *Config) []string {
	var out []string
	for _, t := range c.Triggers {
		out = append(out, t.Name)
	}
	return out
}
