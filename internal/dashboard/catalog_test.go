package dashboard

import (
	"bytes"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/connector"
	"github.com/NodeSpy/conductor/internal/dashboard/api"
	"github.com/NodeSpy/conductor/internal/flow"
	"github.com/NodeSpy/conductor/internal/plugin"
	"github.com/NodeSpy/conductor/internal/store"
)

// A synthetic plugin-backed connector kind, registered the way a loaded
// plugin registers (RegisterExternalType): neutral names, its own fields.
// Nothing in the dashboard knows "widgetsvc" — every field below must reach
// the catalog from this declaration alone.
const synthKind = "widgetsvc"

func registerSynthKind(t *testing.T) {
	t.Helper()
	decl := &connector.TypeDecl{
		Type: synthKind,
		Desc: "a synthetic service for tests",
		Connection: connector.Schema{
			"endpoint": {Type: connector.TString, Required: true, Desc: "where it answers"},
			"api_key":  {Type: connector.TString, Required: true, Secret: true},
			"mode":     {Type: connector.TString, Enum: []string{"fast", "slow"}},
		},
	}
	if err := connector.RegisterExternalType(decl, nil); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { connector.UnregisterExternalType(synthKind) })
}

const catalogCfg = `connectors:
  w1: { use: widgetsvc, endpoint: https://w.example.invalid }
  hooks: { use: webhook }
runtimes:
  r1: { use: cli, tool: claude-code, default: true, host: h1, isolation: { mode: none } }
hosts:
  h1: { host: h1.example.invalid, user: ci, port: 2222, env: { A: b } }
bots:
  b1:
    worker: { runtime: r1 }
    skill: { verbs: [w1.read] }
triggers:
  - name: nightly
    on: manual
    steps:
      - id: one
        run: bash
      - id: two
        use: cli
        command: "true"
`

// newPluginCatalogServer serves catalogCfg with a synthetic install state that
// holds one build of the synthetic plugin.
func newPluginCatalogServer(t *testing.T) (*Server, string, string) {
	t.Helper()
	registerSynthKind(t)
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(cfgPath, []byte(catalogCfg), 0o644); err != nil {
		t.Fatal(err)
	}
	var cfg config.Config
	if err := yaml.Unmarshal([]byte(catalogCfg), &cfg); err != nil {
		t.Fatal(err)
	}
	stDir := filepath.Join(dir, "plugins")
	u, err := config.ParseUse(config.UseKindConnector, synthKind)
	if err != nil {
		t.Fatal(err)
	}
	prev := loadPluginState
	loadPluginState = func() *plugin.InstallState {
		st := plugin.LoadInstallState(stDir)
		st.Plugins = []plugin.Installed{{
			Key: u.InstallKey(), Kind: "connector", Name: synthKind, Use: synthKind,
			Source: u.Source(), Resolved: "v1.2.0", Path: "/nonexistent/bin",
			Manifest: plugin.Manifest{Egress: []string{"w.example.invalid:443"}},
		}}
		return st
	}
	t.Cleanup(func() { loadPluginState = prev })
	st, err := store.Open(store.Options{StatePath: filepath.Join(dir, "state.json"), AuditPath: filepath.Join(dir, "audit.jsonl")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	s := New(Deps{
		Cfg: &cfg, Store: st, Events: flow.NewEventHub(), Version: "test",
		ConfigSHA: "deadbeef", ConfigPath: cfgPath, AuditPath: filepath.Join(dir, "audit.jsonl"),
		Validate: func(string) error { return nil },
		Reload:   func() error { return nil },
	})
	return s, cfgPath, stDir
}

func getJSONInto(t *testing.T, s *Server, path string, v any) {
	t.Helper()
	rr := do(t, s, http.MethodGet, path, nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("GET %s: %d %s", path, rr.Code, rr.Body)
	}
	if err := json.Unmarshal(rr.Body.Bytes(), v); err != nil {
		t.Fatal(err)
	}
}

func TestConnectionsRowsFromConfigAndInstallState(t *testing.T) {
	s, _, _ := newPluginCatalogServer(t)
	var resp api.ConnectionsResponse
	getJSONInto(t, s, "/api/v1/connections", &resp)

	if len(resp.Connectors) != 2 {
		t.Fatalf("connectors: %+v", resp.Connectors)
	}
	w := resp.Connectors[1]
	if w.Name != "w1" || w.Origin != "official" || w.Version != "v1.2.0" || w.Policy != "current" {
		t.Errorf("w1 row: %+v", w)
	}
	if w.Unavailable != "" {
		t.Errorf("a registered kind is not unavailable: %q", w.Unavailable)
	}
	if len(w.Bots) != 1 || w.Bots[0] != "b1" {
		t.Errorf("bots: %v", w.Bots)
	}
	if len(w.Permissions) == 0 {
		t.Errorf("permissions come from the install record's manifest")
	}
	if h := resp.Connectors[0]; h.Name != "hooks" || h.Origin != "builtin" || h.Version != "test" || h.Policy != "" {
		t.Errorf("builtin row: %+v", h)
	}

	if len(resp.Runtimes) != 1 {
		t.Fatalf("runtimes: %+v", resp.Runtimes)
	}
	r := resp.Runtimes[0]
	if !r.Default || r.Tool != "claude-code" || r.Isolation != "none" || r.Host != "h1" || r.Origin != "builtin" {
		t.Errorf("runtime row: %+v", r)
	}
	if len(r.Bots) != 1 || r.Bots[0] != "b1" {
		t.Errorf("runtime bots: %v", r.Bots)
	}

	if len(resp.Hosts) != 1 || resp.Hosts[0].Port != 2222 || resp.Hosts[0].Env != 1 || len(resp.Hosts[0].Runtimes) != 1 {
		t.Errorf("hosts: %+v", resp.Hosts)
	}

	langs := map[string]api.LanguageInfo{}
	for _, l := range resp.Languages {
		langs[l.Name] = l
	}
	if l := langs["cli"]; l.Class != "builtin" || l.Steps != 1 {
		t.Errorf("cli: %+v", l)
	}
	if l := langs["bash"]; l.Class != "host" || l.Steps != 1 || len(l.Workflows) != 1 || l.Workflows[0] != "nightly" {
		t.Errorf("bash: %+v", l)
	}
}

func TestCatalogFieldsComeFromTheDeclaration(t *testing.T) {
	s, _, _ := newPluginCatalogServer(t)
	var resp api.PluginCatalogResponse
	getJSONInto(t, s, "/api/v1/plugins/catalog?kind=connector", &resp)
	var it *api.CatalogItem
	for i := range resp.Items {
		if resp.Items[i].Name == synthKind {
			it = &resp.Items[i]
		}
	}
	if it == nil {
		t.Fatalf("the registered kind is in the catalog: %+v", resp.Items)
	}
	if !it.HasSchema || it.Configured != 1 || it.Installed != "v1.2.0" || it.Desc == "" {
		t.Errorf("item: %+v", it)
	}
	got := map[string]api.CatalogField{}
	for _, f := range it.Fields {
		got[f.Key] = f
	}
	if !got["api_key"].Secret || !got["api_key"].Required || !got["endpoint"].Required || len(got["mode"].Enum) != 2 {
		t.Errorf("fields: %+v", it.Fields)
	}

	if rr := do(t, s, http.MethodGet, "/api/v1/plugins/catalog?kind=nonsense", nil); rr.Code != http.StatusBadRequest {
		t.Errorf("a bad kind is refused: %d", rr.Code)
	}
	var rt api.PluginCatalogResponse
	getJSONInto(t, s, "/api/v1/plugins/catalog?kind=runtime", &rt)
	found := false
	for _, x := range rt.Items {
		if x.Name == "cli" && x.Origin == "builtin" && x.Configured == 1 {
			found = true
		}
	}
	if !found {
		t.Errorf("runtime catalog: %+v", rt.Items)
	}
}

func TestReviewMatchesPluginAdd(t *testing.T) {
	s, _, _ := newPluginCatalogServer(t)
	var rev api.AddReview
	getJSONInto(t, s, "/api/v1/plugins/review?kind=connector&use="+synthKind+"&as=w2", &rev)
	if !rev.Trusted || rev.Origin != "official" || rev.Installed != "v1.2.0" || rev.Instance != "w2" || !rev.HasSchema {
		t.Errorf("review: %+v", rev)
	}
	want, err := plugin.ReviewAdd(config.UseKindConnector, synthKind, "w2", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if rev.Snippet != want.Snippet {
		t.Errorf("snippet %q, want plugin add's %q", rev.Snippet, want.Snippet)
	}

	var third api.AddReview
	getJSONInto(t, s, "/api/v1/plugins/review?kind=connector&use=git.example.invalid/acme/widget", &third)
	if third.Trusted || third.TrustEntry == "" || third.Origin != "custom" || third.Installed != "" {
		t.Errorf("third-party review: %+v", third)
	}
}

// Adding through the dashboard only stages: the review and catalog reads
// install nothing, and staging the connectors: block leaves the config file
// on disk untouched until Review & apply.
func TestAddingOnlyStages(t *testing.T) {
	s, cfgPath, stDir := newPluginCatalogServer(t)
	before, _ := os.ReadFile(cfgPath)

	do(t, s, http.MethodGet, "/api/v1/plugins/catalog?kind=connector", nil)
	do(t, s, http.MethodGet, "/api/v1/plugins/review?kind=connector&use=git.example.invalid/acme/widget&as=w2", nil)
	rr := do(t, s, http.MethodPost, "/api/v1/config/stage", api.StageRequest{
		Path:  []string{"connectors", "w2"},
		Value: map[string]any{"use": synthKind, "endpoint": "https://w2.example.invalid", "api_key": `{{ vault "main" "w2-key" }}`},
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("stage: %d %s", rr.Code, rr.Body)
	}
	var cfg api.ConfigResponse
	getJSONInto(t, s, "/api/v1/config", &cfg)
	if len(cfg.Staged) != 1 || cfg.Staged[0].Path[0] != "connectors" {
		t.Errorf("staged: %+v", cfg.Staged)
	}
	after, _ := os.ReadFile(cfgPath)
	if !bytes.Equal(before, after) {
		t.Errorf("the config file changed before apply")
	}
	if _, err := os.Stat(stDir); !os.IsNotExist(err) {
		t.Errorf("nothing was installed (install dir: %v)", err)
	}
}

func TestBotVerbsFromThePluginDeclaration(t *testing.T) {
	s, _, _ := newPluginCatalogServer(t)
	d, _ := connector.TypeDeclFor(synthKind)
	d.Verbs = []connector.VerbDecl{{Name: "read", Desc: "Read a widget"}}
	var resp struct {
		Verbs []botVerb `json:"verbs"`
	}
	getJSONInto(t, s, "/api/v1/connections/verbs", &resp)
	var got *botVerb
	for i := range resp.Verbs {
		if resp.Verbs[i].Verb == "w1.read" {
			got = &resp.Verbs[i]
		}
	}
	if got == nil || got.Desc != "Read a widget" || got.Connection != "w1" {
		t.Fatalf("verbs: %+v", resp.Verbs)
	}
}
