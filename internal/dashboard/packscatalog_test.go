package dashboard

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/dashboard/api"
	"github.com/NodeSpy/conductor/internal/flow"
	"github.com/NodeSpy/conductor/internal/store"
)

// A synthetic local registry: two packs, neutral names, no copy of a real
// pack — one plain workflow pack and one bot-template-only pack (so the
// catalog's hidden-template-pack behavior is exercised too).
var catalogRegistryPacks = map[string]string{
	"greeter/" + config.PackManifestFile: `pack: { name: greeter, version: 1.0.0, description: "says hello on a schedule" }
workflows:
  say_hi: { steps: [ { log: "hi" } ] }
triggers:
  - { name: on_tick, on: cron }
`,
	"onboarder/" + config.PackManifestFile: `pack: { name: onboarder, version: 0.5.0, description: "onboarding bot template only" }
bots:
  buddy:
    description: Onboarding buddy
    charter: "Welcome new hires."
`,
}

func writeCatalogRegistry(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for rel, body := range catalogRegistryPacks {
		full := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func newCatalogServer(t *testing.T, cfgText, registry string) *Server {
	t.Helper()
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	if cfgText == "" {
		cfgText = "runtimes:\n  claude: { use: cli, tool: claude-code }\n"
	}
	if err := os.WriteFile(cfgPath, []byte(cfgText), 0o644); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(store.Options{
		StatePath: filepath.Join(dir, "state.json"),
		AuditPath: filepath.Join(dir, "audit.jsonl"),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return New(Deps{
		Cfg: &config.Config{}, Store: st, Events: flow.NewEventHub(), Version: "test",
		ConfigPath: cfgPath, AuditPath: filepath.Join(dir, "audit.jsonl"),
		Validate:     func(string) error { return nil },
		Reload:       func() error { return nil },
		PackRegistry: registry,
	})
}

// TestPacksCatalogEndpoint: the Browse tab reads the same catalog
// `conductor pack search` does — a local synthetic registry, searched, with
// template-only packs left out by default.
func TestPacksCatalogEndpoint(t *testing.T) {
	registry := writeCatalogRegistry(t)
	s := newCatalogServer(t, "", registry)

	rr := do(t, s, http.MethodGet, "/api/v1/packs/catalog", nil)
	if rr.Code != 200 {
		t.Fatalf("code %d: %s", rr.Code, rr.Body)
	}
	var resp api.CatalogResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Packs) != 1 || resp.Packs[0].Name != "greeter" {
		t.Fatalf("default catalog (template packs hidden): %+v", resp.Packs)
	}
	if resp.HiddenTemplatePacks != 1 {
		t.Fatalf("hidden_template_packs = %d, want 1", resp.HiddenTemplatePacks)
	}
	if resp.Packs[0].Install == "" {
		t.Fatalf("greeter should carry an install block")
	}

	// Search narrows by name/description/kind.
	rr = do(t, s, http.MethodGet, "/api/v1/packs/catalog?q=hello", nil)
	json.Unmarshal(rr.Body.Bytes(), &resp)
	if len(resp.Packs) != 1 || resp.Packs[0].Name != "greeter" {
		t.Fatalf("search by description word: %+v", resp.Packs)
	}
	rr = do(t, s, http.MethodGet, "/api/v1/packs/catalog?q=nonesuch", nil)
	json.Unmarshal(rr.Body.Bytes(), &resp)
	if len(resp.Packs) != 0 {
		t.Fatalf("search with no match: %+v", resp.Packs)
	}
}

// TestPacksCatalogReadOnly: with no config path configured, the catalog
// reports unavailable rather than guessing at a registry.
func TestPacksCatalogReadOnly(t *testing.T) {
	s, _, _ := newTestServer(t)
	rr := do(t, s, http.MethodGet, "/api/v1/packs/catalog", nil)
	if rr.Code != http.StatusConflict {
		t.Fatalf("code %d, want 409: %s", rr.Code, rr.Body)
	}
}

// TestPackReviewStagesConfigOnly: Add fetches a source and returns a review —
// it must never touch the config file itself. Only after the operator stages
// the returned ConfigValue through the normal config-edit endpoint does
// anything land, and then only as a STAGED edit, never applied.
func TestPackReviewStagesConfigOnly(t *testing.T) {
	// A pack source as a local directory (no git) with a role it requires and
	// a disarmed trigger, so the review surfaces both.
	srcDir := t.TempDir()
	man := `pack:
  name: reviewer
  version: 2.0.0
  description: a pack with a role and a trigger
  requires:
    connectors: [github]
triggers:
  - { name: on_push, on: github }
`
	if err := os.WriteFile(filepath.Join(srcDir, config.PackManifestFile), []byte(man), 0o644); err != nil {
		t.Fatal(err)
	}

	s := newCatalogServer(t, "", "")
	before := do(t, s, http.MethodGet, "/api/v1/config", nil)
	var beforeCfg api.ConfigResponse
	json.Unmarshal(before.Body.Bytes(), &beforeCfg)

	rr := do(t, s, http.MethodPost, "/api/v1/packs/review", api.PackReviewRequest{Source: srcDir})
	if rr.Code != 200 {
		t.Fatalf("review code %d: %s", rr.Code, rr.Body)
	}
	var rv api.PackReviewResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &rv); err != nil {
		t.Fatal(err)
	}
	if rv.Name != "reviewer" || rv.Version != "2.0.0" {
		t.Fatalf("review: %+v", rv)
	}
	if len(rv.Connectors) != 1 || rv.Connectors[0] != "github" {
		t.Fatalf("review connectors: %+v", rv.Connectors)
	}
	if len(rv.Triggers) != 1 || rv.Triggers[0].Name != "on_push" {
		t.Fatalf("review triggers: %+v", rv.Triggers)
	}
	if rv.ConfigValue["source"] != srcDir || rv.ConfigValue["version"] != "2.0.0" {
		t.Fatalf("config value: %+v", rv.ConfigValue)
	}

	// The config file on disk, and anything staged, must be unchanged by
	// review alone.
	after := do(t, s, http.MethodGet, "/api/v1/config", nil)
	var afterCfg api.ConfigResponse
	json.Unmarshal(after.Body.Bytes(), &afterCfg)
	if afterCfg.Text != beforeCfg.Text {
		t.Fatalf("review must not touch the config file")
	}
	if len(afterCfg.Staged) != 0 {
		t.Fatalf("review must not stage anything on its own: %+v", afterCfg.Staged)
	}

	// Now stage it, the way the Add dialog's confirm does: through the
	// generic config-stage endpoint, nothing bespoke.
	stageReq := api.StageRequest{
		Path: []string{"packs", rv.Instance}, Value: rv.ConfigValue,
		Summary: "install " + rv.Name,
	}
	rr = do(t, s, http.MethodPost, "/api/v1/config/stage", stageReq)
	if rr.Code != 200 {
		t.Fatalf("stage code %d: %s", rr.Code, rr.Body)
	}
	var staged struct {
		Staged []api.ConfigChange `json:"staged"`
	}
	json.Unmarshal(rr.Body.Bytes(), &staged)
	if len(staged.Staged) != 1 {
		t.Fatalf("staged: %+v", staged.Staged)
	}
	sc := staged.Staged[0]
	if len(sc.Path) != 2 || sc.Path[0] != "packs" || sc.Path[1] != "reviewer" {
		t.Fatalf("staged path: %+v", sc.Path)
	}
	// Still unapplied: the config file on disk is untouched.
	final := do(t, s, http.MethodGet, "/api/v1/config", nil)
	var finalCfg api.ConfigResponse
	json.Unmarshal(final.Body.Bytes(), &finalCfg)
	if finalCfg.Text != beforeCfg.Text {
		t.Fatalf("staging must not apply: config file changed")
	}
}

// TestPackReviewBadSource: an unreachable/invalid source reports a plain
// error, never a panic or a 500 that leaks internals.
func TestPackReviewBadSource(t *testing.T) {
	s := newCatalogServer(t, "", "")
	rr := do(t, s, http.MethodPost, "/api/v1/packs/review", api.PackReviewRequest{Source: filepath.Join(t.TempDir(), "nonexistent")})
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("code %d, want 400: %s", rr.Code, rr.Body)
	}
}
