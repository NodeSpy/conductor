package config

import (
	"os"
	"path/filepath"
	"testing"
)

// enginePackManifest models the live box: a pack whose OWN workflow steps run a
// code engine (`run: js`). The consumer never writes `js` anywhere — the only
// reference to that engine in the whole effective config comes from inside this
// pack.
const enginePackManifest = `
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
        run: js
        code: "return { score: 1 }"
triggers:
  - name: on_pr
    on: github.pull_request
    steps:
      - id: go
        workflow: team-review
`

// enginePackConfig writes a consumer config that instantiates the pack above
// from a local source. armed controls whether the consumer turns the pack's
// trigger on — a disarmed pack still carries its steps, so it must still carry
// its engine requirement.
func enginePackConfig(t *testing.T, armed bool) string {
	t.Helper()
	dir := t.TempDir()
	writePackSource(t, dir, "src/pr-review-team", enginePackManifest)
	arm := ""
	if armed {
		arm = `
    triggers:
      on_pr:
        enabled: true
        repos: [acme/app]
`
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
` + arm
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// An engine used ONLY inside a pack is still an engine this config needs. The
// consumer's own file mentions no engine at all, so if PluginRefs read only the
// consumer's steps the daemon would decide `js` is unreferenced — and the prune
// that follows would uninstall it out from under the pack.
func TestPluginRefsIncludesPackInternalEngine(t *testing.T) {
	for _, armed := range []bool{true, false} {
		name := "disarmed"
		if armed {
			name = "armed"
		}
		t.Run(name, func(t *testing.T) {
			cfg, err := resolveAndLoad(t, enginePackConfig(t, armed))
			if err != nil {
				t.Fatalf("resolveAndLoad: %v", err)
			}
			refs := cfg.PluginRefs()
			js, ok := refs["engines/js"]
			if !ok {
				t.Fatalf("a pack's internal `run: js` is not in the required set: %v", keysOf(refs))
			}
			if js.Kind() != PluginKindEngine || js.Name != "js" {
				t.Fatalf("engine ref = %+v", js)
			}
			// And the set may be treated as authoritative: the packs that
			// contribute to it have been expanded.
			if !cfg.PluginRefsComplete() {
				t.Fatal("a loaded config with instantiated packs reported an incomplete plugin set")
			}
		})
	}
}

// PluginRefsComplete is what stands between a partial view of the config and a
// destructive prune. A config that DECLARES packs but has not instantiated them
// cannot see their internal engine references, so it must refuse to vouch for
// its own derived set.
func TestPluginRefsCompleteRequiresInstantiatedPacks(t *testing.T) {
	// No packs: whatever PluginRefs returns is the whole story.
	plain := &Config{ConnectorsMap: map[string]ConnectorRef{"a": {Use: "acme/p/jira"}}}
	if !plain.PluginRefsComplete() {
		t.Fatal("a config with no packs: block reported an incomplete plugin set")
	}

	// Packs declared, never expanded: their steps are not here, so neither are
	// their engines.
	declared := &Config{Packs: map[string]PackInstance{"team": {Source: "./src/pr-review-team"}}}
	if declared.PluginRefsComplete() {
		t.Fatal("a config whose packs were never instantiated vouched for its plugin set")
	}

	// Expanded: complete again.
	cfg, err := resolveAndLoad(t, enginePackConfig(t, true))
	if err != nil {
		t.Fatalf("resolveAndLoad: %v", err)
	}
	if !cfg.PluginRefsComplete() {
		t.Fatal("an instantiated pack config reported an incomplete plugin set")
	}
}
