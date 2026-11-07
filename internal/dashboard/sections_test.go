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

// vaultRefCfg is a config using the current, documented vault-reference
// syntax (`{{ vault "<name>" "<key>" }}`) instead of the legacy top-level
// `secrets:` map, plus a host's env vars (one literal, one a reference) and a
// setting a trigger reads — the shapes a real box uses, per phase 8's
// "Secrets page is empty on a real box" gap. hosts.*.env is the only place
// config has a named, scoped env map outside a step — a runtimes: entry has
// no env: field (config.RuntimeConfig), so one isn't seeded here.
const vaultRefCfg = `settings:
  base_url: https://example.invalid

vaults:
  house: { type: conductor }

connectors:
  gh:
    use: github@1.3.0
    auth:
      type: header
      name: X-Token
      value: '{{ vault "house" "gh-token" }}'
  billing:
    use: stripe@1.0.0
    auth:
      type: bearer
      token: '{{ .vaults.house.stripe-key }}'

runtimes:
  claude:
    use: claude-cli

hosts:
  deploy:
    host: deploy.example.invalid
    env:
      DEPLOY_TOKEN: 'env:DEPLOY_TOKEN'
      LOG_LEVEL: debug

triggers:
  - name: nightly
    on: manual
    policy:
      budget:
        max_cost_usd: '${settings.base_url}'
    steps:
      - id: run
        run: bash
`

func newSectionsServer(t *testing.T, cfgText string) *Server {
	t.Helper()
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
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
		ConfigSHA: "deadbeef", ConfigPath: cfgPath, AuditPath: filepath.Join(dir, "audit.jsonl"),
		Validate: func(string) error { return nil },
		Reload:   func() error { return nil },
	})
}

// TestSecretsFromVaultRefs covers item 2's gap: a real config mostly skips
// secrets: and names a vault entry directly as a whole-field reference
// ({{ vault "name" "key" }} or the {{ .vaults.name.key }} spelling). Both
// forms must show up on /api/v1/secrets, named by their key, with every path
// that references them — and the value itself must never appear.
func TestSecretsFromVaultRefs(t *testing.T) {
	s := newSectionsServer(t, vaultRefCfg)
	rr := do(t, s, http.MethodGet, "/api/v1/secrets", nil)
	if rr.Code != 200 {
		t.Fatalf("code %d: %s", rr.Code, rr.Body)
	}
	var resp api.SecretsResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Vaults) != 1 || resp.Vaults[0].Name != "house" {
		t.Fatalf("vaults: %+v", resp.Vaults)
	}
	want := map[string]string{
		"gh-token":   "house/gh-token",
		"stripe-key": "house/stripe-key",
	}
	got := map[string]api.SecretInfo{}
	for _, si := range resp.Secrets {
		got[si.Name] = si
		for _, v := range si.UsedBy {
			if v == "" {
				t.Fatalf("secret %q has an empty used-by path", si.Name)
			}
		}
		// The value must never be echoed: Ref names vault+key, never a
		// resolved secret.
		if si.Ref == "" || si.Ref == si.Name {
			t.Fatalf("secret %q: ref looks wrong: %q", si.Name, si.Ref)
		}
	}
	for name, ref := range want {
		si, ok := got[name]
		if !ok {
			t.Fatalf("missing secret %q in %+v", name, resp.Secrets)
		}
		if si.Ref != ref {
			t.Fatalf("secret %q: ref = %q, want %q", name, si.Ref, ref)
		}
	}
	if si := got["gh-token"]; len(si.UsedBy) != 1 || si.UsedBy[0] != "connectors.gh.auth.value" {
		t.Fatalf("gh-token used_by: %+v", si.UsedBy)
	}
	if si := got["stripe-key"]; len(si.UsedBy) != 1 || si.UsedBy[0] != "connectors.billing.auth.token" {
		t.Fatalf("stripe-key used_by: %+v", si.UsedBy)
	}
}

// TestSystemEnvAndSettingsUsedBy covers the Settings tab's "used by" and the
// host env var list the brief asks for (names and where each lives, never a
// literal value).
func TestSystemEnvAndSettingsUsedBy(t *testing.T) {
	s := newSectionsServer(t, vaultRefCfg)
	rr := do(t, s, http.MethodGet, "/api/v1/system", nil)
	if rr.Code != 200 {
		t.Fatalf("code %d: %s", rr.Code, rr.Body)
	}
	var sys api.SystemResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &sys); err != nil {
		t.Fatal(err)
	}
	if by := sys.SettingsUsedBy["base_url"]; len(by) != 1 || by[0] != "triggers.0.policy.budget.max_cost_usd" {
		t.Fatalf("settings used_by: %+v", sys.SettingsUsedBy)
	}
	byScope := map[string]api.EnvVarInfo{}
	for _, e := range sys.EnvVars {
		byScope[e.Scope+"."+e.Name] = e
	}
	logLevel, ok := byScope["host:deploy.LOG_LEVEL"]
	if !ok || !logLevel.Literal || logLevel.Ref != "" {
		t.Fatalf("LOG_LEVEL should be a literal, got %+v", logLevel)
	}
	deployToken, ok := byScope["host:deploy.DEPLOY_TOKEN"]
	if !ok || deployToken.Literal || deployToken.Ref != "env:DEPLOY_TOKEN" {
		t.Fatalf("DEPLOY_TOKEN should be reported as its reference, got %+v", deployToken)
	}
}

// TestHistoryAppliedChange covers item 1: the Config screen's applied-changes
// history is built from configedit's own backups plus the audit log, with no
// second record kept anywhere. One apply should produce one history entry
// naming the operator, the changed leaf path, and a non-empty diff.
func TestHistoryAppliedChange(t *testing.T) {
	s := newSectionsServer(t, vaultRefCfg)

	do(t, s, http.MethodPost, "/api/v1/config/stage", api.StageRequest{
		Path: []string{"settings", "base_url"}, Value: "https://changed.invalid", Summary: "change the base url",
	})
	rr := do(t, s, http.MethodGet, "/api/v1/config/review", nil)
	var rev api.ReviewResponse
	json.Unmarshal(rr.Body.Bytes(), &rev)
	if rev.Token == "" {
		t.Fatal("no confirm token")
	}
	rr = do(t, s, http.MethodPost, "/api/v1/config/apply", api.ApplyRequest{Token: rev.Token})
	if rr.Code != 200 {
		t.Fatalf("apply code %d: %s", rr.Code, rr.Body)
	}
	var applied api.AppliedResponse
	json.Unmarshal(rr.Body.Bytes(), &applied)

	rr = do(t, s, http.MethodGet, "/api/v1/config/history", nil)
	var hr struct {
		History []api.HistoryItem `json:"history"`
	}
	json.Unmarshal(rr.Body.Bytes(), &hr)
	if len(hr.History) != 1 {
		t.Fatalf("history len %d: %+v", len(hr.History), hr.History)
	}
	h := hr.History[0]
	if h.ID != applied.BackupID {
		t.Fatalf("history id %q != backup id %q", h.ID, applied.BackupID)
	}
	if h.Event != "apply" {
		t.Fatalf("event = %q, want apply", h.Event)
	}
	found := false
	for _, p := range h.Changed {
		if p == "settings.base_url" {
			found = true
		}
	}
	if !found {
		t.Fatalf("changed paths %+v missing settings.base_url", h.Changed)
	}

	rr = do(t, s, http.MethodGet, "/api/v1/config/history/"+h.ID+"/diff", nil)
	if rr.Code != 200 {
		t.Fatalf("diff code %d: %s", rr.Code, rr.Body)
	}
	var diff api.HistoryDiffResponse
	json.Unmarshal(rr.Body.Bytes(), &diff)
	if !diff.Changed || len(diff.Lines) == 0 {
		t.Fatalf("expected a non-empty diff, got %+v", diff)
	}
}

// TestPackManifestRoles covers item 3: a pack's requires: (read from its
// vendored manifest) matched against this instance's live bindings and the
// consumer's own connectors of a compatible kind.
func TestPackManifestRoles(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	manDir := filepath.Join(config.PackVendorDir(dir), "review")
	if err := os.MkdirAll(manDir, 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := `pack:
  name: pr-review-team
  version: 1.0.0
  requires:
    connectors: { github: "*" }
workflows:
  review-flow:
    steps:
      - id: summarize
        run: bash
triggers:
  - name: on_pr
    on: github.pull_request
`
	if err := os.WriteFile(filepath.Join(manDir, "conductor-pack.yaml"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	cfgText := `connectors:
  gh:
    use: github@1.3.0

packs:
  review:
    use: conductor-packs/pr-review-team@1.0.0
    connectors:
      github: gh
`
	if err := os.WriteFile(cfgPath, []byte(cfgText), 0o644); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(store.Options{StatePath: filepath.Join(dir, "state.json"), AuditPath: filepath.Join(dir, "audit.jsonl")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	s := New(Deps{
		Cfg: &config.Config{}, Store: st, Events: flow.NewEventHub(), Version: "test",
		ConfigPath: cfgPath, AuditPath: filepath.Join(dir, "audit.jsonl"),
	})

	rr := do(t, s, http.MethodGet, "/api/v1/packs/review/manifest", nil)
	if rr.Code != 200 {
		t.Fatalf("code %d: %s", rr.Code, rr.Body)
	}
	var man api.PackManifestResponse
	json.Unmarshal(rr.Body.Bytes(), &man)
	if len(man.Connectors) != 1 || man.Connectors[0].Name != "github" {
		t.Fatalf("connectors: %+v", man.Connectors)
	}
	if man.Connectors[0].Bound != "gh" {
		t.Fatalf("bound: %+v", man.Connectors[0])
	}
	if len(man.Connectors[0].Candidates) != 1 || man.Connectors[0].Candidates[0] != "gh" {
		t.Fatalf("candidates: %+v", man.Connectors[0])
	}
	if len(man.Steps) != 1 || man.Steps[0].Ref != "review-flow/summarize" {
		t.Fatalf("steps: %+v", man.Steps)
	}
	if len(man.Triggers) != 1 || man.Triggers[0].Name != "on_pr" || man.Triggers[0].Armed {
		t.Fatalf("triggers: %+v", man.Triggers)
	}
}
