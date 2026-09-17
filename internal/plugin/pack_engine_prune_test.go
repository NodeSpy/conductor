package plugin

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/NodeSpy/conductor/internal/config"
)

// packEngineManifest is a pack whose own workflow steps run a code engine. The
// consumer config that installs it names no engine anywhere.
const packEngineManifest = `
pack:
  name: pr-review-team
  version: 1.0.0
  description: Multi-lens PR review
  requires:
    connectors: { github: "*" }
exports:
  workflows: [team-review]
workflows:
  team-review:
    steps:
      - id: score
        use: acme/plugins/js
        code: "return { score: 1 }"
`

// loadPackEngineConfig writes and loads a consumer config whose ONLY reference
// to an engine lives inside the instantiated pack. Offline: the pack source is
// a local directory, and instantiation reads the vendored tree.
func loadPackEngineConfig(t *testing.T) *config.Config {
	t.Helper()
	dir := t.TempDir()
	src := filepath.Join(dir, "src", "pr-review-team")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, config.PackManifestFile), []byte(packEngineManifest), 0o644); err != nil {
		t.Fatal(err)
	}
	body := `
connectors:
  gh:
    use: github
packs:
  team:
    source: ./src/pr-review-team
    version: 1.0.0
    connectors: { github: gh }
`
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := config.ResolvePacks(path); err != nil {
		t.Fatalf("ResolvePacks: %v", err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	return cfg
}

// THE BOX SCENARIO, end to end. A config whose only engine reference is inside
// an instantiated pack must (a) carry that engine in its required set and
// (b) survive a full authorized reconcile with the engine still installed.
//
// On the box this failed as: `installed.yaml` lost its engines/js entry, and
// the next `conductor validate` refused to start with
// "plugin js: ... not installed".
func TestReconcileKeepsPackUsedEngine(t *testing.T) {
	cfg := loadPackEngineConfig(t)
	trust := &config.PackTrustConfig{Allow: []string{"github.com/acme/*"}}

	refs := cfg.PluginRefs()
	if _, ok := refs["engines/js"]; !ok {
		t.Fatalf("the pack's engine is missing from the reconcile's required set: %v", keysIn(refs))
	}

	// The engine is installed, as `conductor init` would have left it.
	st := stateAt(t)
	if _, err := Reconcile(refs, st, trust, stubFor("js", "js/v1.0.0"), Options{Prune: cfg.PluginRefsComplete()}); err != nil {
		t.Fatal(err)
	}
	if _, ok := st.Get("engines/js"); !ok {
		t.Fatalf("the pack's engine was not installed: %v", st.Keys())
	}

	// A second full reconcile — the auto-update / re-init pass that rewrites
	// install state. It is authorized to prune, and it must NOT drop an engine
	// the pack still uses.
	if _, err := Reconcile(cfg.PluginRefs(), st, trust, stubFor("js", "js/v1.0.0"), Options{Prune: cfg.PluginRefsComplete()}); err != nil {
		t.Fatal(err)
	}
	if _, ok := st.Get("engines/js"); !ok {
		t.Fatalf("a reconcile pruned an engine the pack uses: %v", st.Keys())
	}

	// And it survives a reload — this is what boot reads.
	if _, ok := LoadInstallState(st.Dir()).Get("engines/js"); !ok {
		t.Fatal("engines/js is gone from the persisted install state")
	}
}

// A config that declares packs it has NOT instantiated cannot see their
// internal engine references, so it must not authorize a prune — "absent from
// a partial view" is not proof of "unused". This is the guard that holds even
// if some future caller reconciles against a config that never went through
// config.Load.
func TestReconcileSkipsPruneWhenPacksNotInstantiated(t *testing.T) {
	st := stateAt(t)
	trust := &config.PackTrustConfig{Allow: []string{"github.com/acme/*"}}

	js := refFor(t, config.UseKindEngine, "acme/plugins/js")
	if _, err := Reconcile(map[string]config.PluginRef{js.Key(): js}, st, trust, stubFor("js", "js/v1.0.0"), Options{Prune: true}); err != nil {
		t.Fatal(err)
	}

	uninstantiated := &config.Config{Packs: map[string]config.PackInstance{"team": {Source: "./src/pr-review-team"}}}
	if uninstantiated.PluginRefsComplete() {
		t.Fatal("an uninstantiated pack config vouched for its plugin set")
	}
	if _, err := Reconcile(uninstantiated.PluginRefs(), st, trust, stubFor("js"), Options{Prune: uninstantiated.PluginRefsComplete()}); err != nil {
		t.Fatal(err)
	}
	if _, ok := st.Get("engines/js"); !ok {
		t.Fatalf("pruned against a config that cannot see its packs' engines: %v", st.Keys())
	}
}

func keysIn(m map[string]config.PluginRef) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
