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
settings:
  heavy_model: { type: string, default: default-heavy }
presets:
  codex: { heavy_model: gpt-5-pro }
exports:
  workflows: [review-flow]
x-steps:
  reviewer: &reviewer
    type: agent
    name: reviewer
    workspace: worktree
  handoff: &handoff
    type: agent
    name: handoff
    workspace: local
    skill:
      secrets_via: broker
      allow_secrets: [api_token]
workflows:
  review-flow:
    steps:
      - id: review
        type: agent
        <<: *reviewer
        prompt: "review with {{.repo}} using ${settings.heavy_model}"
      - id: post
        uses: github.comment
        options: { store: cache, text: "done" }
      - id: handoff
        <<: *handoff
        prompt: "hand it off"
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
// from a local source, binding github->gh and the store/secret, and
// overriding two of the pack's workflow steps by reference.
func baseConfigWithReview(t *testing.T, extraPackFields string) string {
	t.Helper()
	dir := t.TempDir()
	writePackSource(t, dir, "src/review-kit", reviewKitManifest)
	body := `
connectors:
  gh:
    use: github
stores:
  redis1:
    type: boltdb
    path: /tmp/pc-pack-test.db
vaults:
  house:
    type: file
    dir: /tmp/pc-pack-vault
x-steps:
  my-opus: &my-opus
    type: agent
    name: my-opus
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
    steps:
      review-flow/review:  { <<: *my-opus }
      review-flow/handoff: { workspace: worktree }
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

	// The consumer's override addressed review-flow/review and merged the
	// fields of their own anchor onto the pack's step. The pack's identity
	// is namespaced, and the consumer's grant came through.
	if got := wf.Steps[0].Name; got != "review/my-opus" {
		t.Fatalf("override should carry the consumer's name, namespaced; got %q", got)
	}
	if sk := wf.Steps[0].Skill; sk == nil || len(sk.Verbs) != 1 || sk.Verbs[0] != "gh.submit_review" {
		t.Fatalf("override should bring the consumer's grant, rebound; got %+v", sk)
	}

	// Step override: post kept the bundle and merged the override.
	h := packStep(t, cfg, "review/review-flow/handoff")
	if h.Workspace.Isolation != "worktree" {
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
	repos := TriggerRepoScope(tr.Filter)
	if len(repos) != 2 {
		t.Fatalf("trigger repos should be [acme/app acme/api], got %v", repos)
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
connectors: { gh: { use: github } }
stores: { redis1: { type: boltdb, path: /tmp/x.db } }
vaults: { house: { type: file, dir: /tmp/pc-pack-vault } }
packs:
  review:
    source: ./src/review-kit
    connectors: { github: gh }
    stores: { cache: redis1 }
    secrets: { api_token: house/foocorp }
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
	if len(TriggerRepoScope(tr.Filter)) > 0 {
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
    use: github
    identity: { read_token: leaked }
`)
	body := `
connectors: { gh: { use: github } }
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
	// Instance does NOT bind github, and the consumer has no github
	// connector to auto-bind either — so the required-but-unbound path is
	// what this exercises.
	body := `
connectors: { sl: { use: slack, bot_token: x } }
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
connectors: { gh: { use: github } }
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
connectors: { gh: { use: github } }
stores: { redis1: { type: boltdb, path: /tmp/x.db } }
vaults: { house: { type: file, dir: /tmp/pc-pack-vault } }
packs:
  review:
    source: ./src/review-kit
    connectors: { github: gh }
    stores: { cache: redis1 }
    secrets: { api_token: house/foocorp }
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

// --- `packs:` `use:` lowering (design §5.1) --------------------------------

func TestPackUseLowersToSource(t *testing.T) {
	tests := []struct {
		name  string
		packs map[string]PackInstance
		want  map[string]string
	}{
		{
			name:  "the reserved namespace resolves to the official pack repo",
			packs: map[string]PackInstance{"pr-review-team": {Use: "conductor-packs/pr-review-team"}},
			want:  map[string]string{"pr-review-team": "github.com/NodeSpy/conductor-packs//pr-review-team"},
		},
		{
			name:  "…and the instance name need not match the pack name",
			packs: map[string]PackInstance{"review": {Use: "conductor-packs/pr-review-team"}},
			want:  map[string]string{"review": "github.com/NodeSpy/conductor-packs//pr-review-team"},
		},
		{
			name:  "the official repo spelled out still works",
			packs: map[string]PackInstance{"review": {Use: "NodeSpy/conductor-packs/pr-review-team"}},
			want:  map[string]string{"review": "github.com/NodeSpy/conductor-packs//pr-review-team"},
		},
		{
			name:  "use: a local folder",
			packs: map[string]PackInstance{"house-style": {Use: "./packs/house-style"}},
			want:  map[string]string{"house-style": "./packs/house-style"},
		},
		{
			name:  "use: an explicit repo",
			packs: map[string]PackInstance{"kit": {Use: "acme/conductor-packs/kit"}},
			want:  map[string]string{"kit": "github.com/acme/conductor-packs//kit"},
		},
		{
			name:  "use: carries a version suffix",
			packs: map[string]PackInstance{"kit": {Use: "acme/packs/kit@~> 1.2"}},
			want:  map[string]string{"kit": "github.com/acme/packs//kit@~> 1.2"},
		},
		{
			name:  "the reserved namespace carries a version suffix too",
			packs: map[string]PackInstance{"kit": {Use: "conductor-packs/kit@^1.2"}},
			want:  map[string]string{"kit": "github.com/NodeSpy/conductor-packs//kit@^1.2"},
		},
		{
			name:  "an explicit source: still wins",
			packs: map[string]PackInstance{"kit": {Use: "acme/packs/kit", Source: "git::ssh://git@x/y//kit"}},
			want:  map[string]string{"kit": "git::ssh://git@x/y//kit"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if err := applyPackSourceDefaults(tc.packs); err != nil {
				t.Fatal(err)
			}
			for name, want := range tc.want {
				if got := tc.packs[name].Source; got != want {
					t.Errorf("%s: source = %q, want %q", name, got, want)
				}
			}
		})
	}
}

// A `packs:` entry with neither `use:` nor `source:` used to fall through to
// the entry KEY, treated as a bare name — i.e. a silent fetch from the
// official registry. That is the ambiguity `conductor-packs/<name>` replaced,
// so it is now an error that names both routes.
func TestPackEntryWithoutUseIsAnError(t *testing.T) {
	packs := map[string]PackInstance{"pr-review-team": {}}
	err := applyPackSourceDefaults(packs)
	if err == nil {
		t.Fatalf("a pack entry with no use: must not resolve (got source %q)", packs["pr-review-team"].Source)
	}
	for _, want := range []string{
		`pack "pr-review-team"`,
		`use: conductor-packs/pr-review-team`,
		`use: owner/repo/pr-review-team`,
		`use: ./packs/pr-review-team`,
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error must name %q, got: %v", want, err)
		}
	}
}

// The `use:` lowering must not reach pack DEPENDENCIES: their source is
// declared by the parent's requires.packs, which the resolver reads later.
func TestPackUseLoweringDoesNotTouchDependencies(t *testing.T) {
	packs := map[string]PackInstance{
		"kit": {Use: "acme/packs/kit", Packs: map[string]PackInstance{"base": {}}},
	}
	if err := applyPackSourceDefaults(packs); err != nil {
		t.Fatal(err)
	}
	if got := packs["kit"].Packs["base"].Source; got != "" {
		t.Fatalf("dependency source was pre-filled with %q — it must come from requires.packs first", got)
	}
}

// The official pack repo is trusted by default, exactly as the plugin repo is.
func TestOfficialPackRepoIsTrustedByDefault(t *testing.T) {
	var trust *PackTrustConfig // no operator allowlist configured
	if !trust.SourceAllowed("github.com/NodeSpy/conductor-packs//pr-review-team") {
		t.Error("the official pack repo should be trusted by default")
	}
	trust = &PackTrustConfig{Allow: []string{"github.com/acme/*"}}
	if !trust.SourceAllowed("github.com/NodeSpy/conductor-packs//x") {
		t.Error("an allowlist must not revoke the official pack repo")
	}
	if trust.SourceAllowed("github.com/stranger/packs//x") {
		t.Error("a third-party source still needs an explicit entry")
	}
}

// The blessed `conductor-packs/<name>` form must be default-trusted with no
// allowlist ceremony — that is the whole point of naming the registry rather
// than making operators spell out NodeSpy/conductor-packs and then list it.
// Asserted through the RESOLVER's own output, so the alias and the trust check
// cannot drift: a trust check that silently misses is worse than a strict one.
func TestPacksNamespaceAliasIsDefaultTrusted(t *testing.T) {
	src, err := packUseSource(PacksNamespaceAlias + "/pr-review-team")
	if err != nil {
		t.Fatal(err)
	}
	if want := OfficialPacksSource + "//pr-review-team"; src != want {
		t.Fatalf("source = %q, want %q", src, want)
	}
	var none *PackTrustConfig // no operator allowlist configured at all
	if !none.SourceAllowed(src) {
		t.Errorf("%q must be trusted with no pack_trust block", src)
	}
	// An operator who adds an allowlist for their own packs must not thereby
	// revoke the official registry.
	scoped := &PackTrustConfig{Allow: []string{"github.com/acme/*"}}
	if !scoped.SourceAllowed(src) {
		t.Errorf("%q must survive an unrelated allowlist", src)
	}
	if !scoped.PluginSourceAllowed(src) {
		t.Errorf("%q must be trusted on the plugin surface too", src)
	}

	// …while a third-party pack still needs an explicit entry.
	third, err := packUseSource("stranger/packs/foo")
	if err != nil {
		t.Fatal(err)
	}
	if scoped.SourceAllowed(third) {
		t.Errorf("%q must require an allowlist entry", third)
	}
	listed := &PackTrustConfig{Allow: []string{"stranger/packs"}}
	if !listed.SourceAllowed(third) {
		t.Errorf("%q must resolve once listed", third)
	}
}

// The reserved `conductor-packs/<name>` namespace is the blessed form: it
// resolves exactly where a bare name used to, but the operator has NAMED the
// registry, so a network fetch from a conductor-operated repo reads as one.
func TestPackNamespaceAliasResolvesToTheOfficialRepo(t *testing.T) {
	u, err := ParseUse(UseKindPack, "conductor-packs/pr-review-team")
	if err != nil {
		t.Fatal(err)
	}
	if u.Origin != OriginOfficial {
		t.Errorf("origin = %q, want %q", u.Origin, OriginOfficial)
	}
	if u.Host != "github.com" {
		t.Errorf("host = %q, want github.com", u.Host)
	}
	if u.Repo != OfficialPacksRepo {
		t.Errorf("repo = %q, want %q", u.Repo, OfficialPacksRepo)
	}
	// A pack sits at the root of the packs repo — not under a kind directory.
	if u.Component != "pr-review-team" {
		t.Errorf("component = %q, want pr-review-team", u.Component)
	}
	if u.Name != "pr-review-team" {
		t.Errorf("name = %q, want pr-review-team", u.Name)
	}
	// The source string is what pack_trust and the lockfile compare against.
	if got, want := u.Source(), OfficialPacksSource+"//pr-review-team"; got != want {
		t.Errorf("source = %q, want %q", got, want)
	}
	// A name that is a builtin RUNTIME is a perfectly good pack name: the
	// connector/runtime confusion check must not fire across repos. Nor may
	// the CONNECTOR registry claim one — `builtinFor` falls through to
	// connectors for any non-runtime kind, so `conductor-packs/github` must
	// not come back as an in-binary connector.
	for _, name := range []string{"paseo", "github", "slack"} {
		u, err := ParseUse(UseKindPack, PacksNamespaceAlias+"/"+name)
		if err != nil {
			t.Errorf("a pack may share a builtin's name: %v", err)
			continue
		}
		if u.Origin != OriginOfficial {
			t.Errorf("%s: origin = %q, want %q — a pack has no builtins", name, u.Origin, OriginOfficial)
		}
	}
}

// The reserved namespace takes a version suffix and a multi-segment component,
// and rejects being written with no pack after it.
func TestPackNamespaceAliasEdges(t *testing.T) {
	u, err := ParseUse(UseKindPack, "conductor-packs/pr-review-team@^1.2")
	if err != nil {
		t.Fatal(err)
	}
	if u.Version != "^1.2" || u.Component != "pr-review-team" {
		t.Errorf("version = %q, component = %q", u.Version, u.Component)
	}
	// The `//` component separator is accepted here as it is everywhere else.
	u, err = ParseUse(UseKindPack, "conductor-packs//pr-review-team")
	if err != nil {
		t.Fatal(err)
	}
	if u.Repo != OfficialPacksRepo || u.Component != "pr-review-team" {
		t.Errorf("repo = %q, component = %q", u.Repo, u.Component)
	}
	// The alias alone names a registry and no pack.
	if _, err := ParseUse(UseKindPack, "conductor-packs"); err == nil {
		t.Error("the alias with no pack after it must not resolve")
	} else if !strings.Contains(err.Error(), "conductor-packs/<name>") {
		t.Errorf("the error should say what to write, got: %v", err)
	}
	// An org LITERALLY named conductor-packs is still reachable — with the
	// host written out, which is the documented escape from the reservation.
	u, err = ParseUse(UseKindPack, "github.com/conductor-packs/kit/foo")
	if err != nil {
		t.Fatal(err)
	}
	if u.Repo != "conductor-packs/kit" || u.Origin != OriginGitHub {
		t.Errorf("repo = %q, origin = %q — the host-qualified form must stay third-party", u.Repo, u.Origin)
	}
}

// A bare pack name is no longer a lookup in the official repo: packs have no
// builtins, so it could only ever have meant that, and a blessed-registry
// fetch must not look like an arbitrary name.
func TestBarePackNameIsAmbiguous(t *testing.T) {
	_, err := ParseUse(UseKindPack, "pr-review-team")
	if err == nil {
		t.Fatal("a bare pack name must not resolve")
	}
	for _, want := range []string{
		"a bare pack name is ambiguous",
		`"conductor-packs/pr-review-team"`,
		`"owner/repo/pr-review-team"`,
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error must name %q, got: %v", want, err)
		}
	}
}

// …and the blast radius stops at packs. Connectors and runtimes HAVE builtins,
// so a bare name there asks a real question and keeps its official fallback.
func TestBareNameResolutionIsUnchangedForConnectorsAndRuntimes(t *testing.T) {
	for _, tc := range []struct {
		kind       UseKind
		ref        string
		wantOrigin UseOrigin
		wantComp   string
	}{
		{UseKindConnector, "github", OriginBuiltin, ""},
		{UseKindRuntime, "paseo", OriginBuiltin, ""},
		{UseKindConnector, "linear", OriginOfficial, "connectors/linear"},
		{UseKindRuntime, "aider", OriginOfficial, "runtimes/aider"},
	} {
		u, err := ParseUse(tc.kind, tc.ref)
		if err != nil {
			t.Errorf("ParseUse(%s, %q): %v", tc.kind, tc.ref, err)
			continue
		}
		if u.Origin != tc.wantOrigin || u.Component != tc.wantComp {
			t.Errorf("ParseUse(%s, %q) = origin %q component %q, want %q / %q",
				tc.kind, tc.ref, u.Origin, u.Component, tc.wantOrigin, tc.wantComp)
		}
		if tc.wantOrigin == OriginOfficial && u.Repo != OfficialRepo {
			t.Errorf("ParseUse(%s, %q): repo = %q, want the PLUGIN repo %q",
				tc.kind, tc.ref, u.Repo, OfficialRepo)
		}
	}
	// The reserved pack namespace is pack-only: for a connector it is just an
	// owner/repo pair on github.com, as it always was.
	u, err := ParseUse(UseKindConnector, "conductor-packs/whatever")
	if err != nil {
		t.Fatal(err)
	}
	if u.Origin != OriginGitHub || u.Repo != "conductor-packs/whatever" {
		t.Errorf("connector origin = %q repo = %q — the pack alias must not leak across kinds", u.Origin, u.Repo)
	}
}

// A third-party pack is untouched by all of this.
func TestThirdPartyPackStillResolves(t *testing.T) {
	u, err := ParseUse(UseKindPack, "acme/my-packs/foo")
	if err != nil {
		t.Fatal(err)
	}
	if u.Origin != OriginGitHub {
		t.Errorf("origin = %q, want %q", u.Origin, OriginGitHub)
	}
	if u.Repo != "acme/my-packs" || u.Component != "foo" || u.Name != "foo" {
		t.Errorf("repo = %q component = %q name = %q", u.Repo, u.Component, u.Name)
	}
	if got, want := u.Source(), "github.com/acme/my-packs//foo"; got != want {
		t.Errorf("source = %q, want %q", got, want)
	}
}

// §15: a pack's `models:` block IS instantiated as `<ns>/<name>` fleets,
// but nothing rewrote a step's `model:` reference — so a pack step saying
// `model: reviewer` resolved against the CONSUMER's globals. Silent
// mis-resolution at best; a reach across the namespace boundary if the
// consumer happens to have that name.
func TestPackStepModelResolvesToThePacksOwnFleet(t *testing.T) {
	dir := t.TempDir()
	writePackSource(t, dir, "src/kit", `
pack:
  name: kit
  version: "1.0.0"
  requires: { conductor: ">=0.1" }
models:
  reviewer: { any: ["claude-opus-*"] }
workflows:
  flow:
    steps:
      - { id: review, type: agent, prompt: p, model: reviewer }
      - { id: pinned, type: agent, prompt: p, model: claude-opus-5 }
`)
	cfg, err := resolveAndLoad(t, writeDoc(t, dir, `
connectors:
  gh: { use: github, token: x }
models:
  reviewer: { any: ["a-consumer-model"] }
packs:
  kit: { source: ./src/kit }
`))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	// The pack's own fleet, not the consumer's same-named one.
	if got := packStep(t, cfg, "kit/flow/review").Model.Ref; got != "kit/reviewer" {
		t.Fatalf("a pack step's fleet ref must resolve inside the pack, got %q", got)
	}
	if _, ok := cfg.Models["kit/reviewer"]; !ok {
		t.Fatalf("the namespaced fleet should exist, have %v", mapKeys(cfg.Models))
	}
	// An exact pin is not a fleet name and must pass through untouched —
	// `model:` is map-key-wins.
	if got := packStep(t, cfg, "kit/flow/pinned").Model.Ref; got != "claude-opus-5" {
		t.Fatalf("an exact model id must not be namespaced, got %q", got)
	}
}
