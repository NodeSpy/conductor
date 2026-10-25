package dashboard

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/dashboard/api"
	"github.com/NodeSpy/conductor/internal/flow"
	"github.com/NodeSpy/conductor/internal/store"
)

const editCfg = `settings:
  concurrency: 4   # inline
  greeting: hello

# an unknown section the editor must never touch
x-notes:
  by: platform-sre

sandbox:
  agents: on

vaults:
  main:
    type: onepassword

secrets:
  token: main/api-token

packs:
  review:
    use: conductor-packs/pr-review-team@2.9.0

connectors:
  gh:
    use: github@1.3.0

update:
  auto: true
  interval: 10m
`

// newEditServer builds a dashboard Server whose config-edit pipeline points at a
// real temp config file, with an injectable validate/reload.
func newEditServer(t *testing.T, validate func(string) error) (*Server, string, *bool) {
	t.Helper()
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(cfgPath, []byte(editCfg), 0o644); err != nil {
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
	reloaded := false
	s := New(Deps{
		Cfg:        &config.Config{},
		Store:      st,
		Events:     flow.NewEventHub(),
		Version:    "test-1.0.0",
		ConfigSHA:  "deadbeef",
		ConfigPath: cfgPath,
		AuditPath:  filepath.Join(dir, "audit.jsonl"),
		Validate:   validate,
		Reload:     func() error { reloaded = true; return nil },
	})
	return s, cfgPath, &reloaded
}

func do(t *testing.T, s *Server, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var r *http.Request
	if body != nil {
		b, _ := json.Marshal(body)
		r = httptest.NewRequest(method, path, bytes.NewReader(b))
	} else {
		r = httptest.NewRequest(method, path, nil)
	}
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, r)
	return rr
}

func TestConfigReadEndpoint(t *testing.T) {
	s, _, _ := newEditServer(t, nil)
	rr := do(t, s, http.MethodGet, "/api/v1/config", nil)
	if rr.Code != 200 {
		t.Fatalf("code %d: %s", rr.Code, rr.Body)
	}
	var got api.ConfigResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got.Text, "concurrency: 4") {
		t.Fatal("text missing")
	}
	// Flattened leaves include settings.concurrency.
	found := false
	for _, l := range got.Leaves {
		if strings.Join(l.Path, ".") == "settings.concurrency" {
			found = true
			if l.Kind != "int" {
				t.Errorf("concurrency kind = %q", l.Kind)
			}
		}
	}
	if !found {
		t.Fatal("settings.concurrency leaf missing")
	}
}

// TestEditDiffApplyFlow is the end-to-end HTTP flow the Playwright test mirrors:
// stage a setting, review the one-line diff, apply with the step-up token, and
// confirm the live file changed and the daemon was asked to reload.
func TestEditDiffApplyFlow(t *testing.T) {
	validated := ""
	s, cfgPath, reloaded := newEditServer(t, func(p string) error {
		b, _ := os.ReadFile(p)
		validated = string(b)
		return nil
	})

	// stage
	rr := do(t, s, http.MethodPost, "/api/v1/config/stage", api.StageRequest{
		Path: []string{"settings", "concurrency"}, Value: 8, Summary: "more parallelism",
	})
	if rr.Code != 200 {
		t.Fatalf("stage code %d: %s", rr.Code, rr.Body)
	}

	// review
	rr = do(t, s, http.MethodGet, "/api/v1/config/review", nil)
	if rr.Code != 200 {
		t.Fatalf("review code %d: %s", rr.Code, rr.Body)
	}
	var rev api.ReviewResponse
	json.Unmarshal(rr.Body.Bytes(), &rev)
	if !rev.Changed || rev.Added != 1 || rev.Removed != 1 {
		t.Fatalf("expected one-line diff, got changed=%v +%d -%d", rev.Changed, rev.Added, rev.Removed)
	}
	if rev.Token == "" {
		t.Fatal("no confirm token")
	}

	// apply without a token → rejected (step-up required)
	rr = do(t, s, http.MethodPost, "/api/v1/config/apply", api.ApplyRequest{})
	if rr.Code != 400 {
		t.Fatalf("apply without token should 400, got %d", rr.Code)
	}

	// apply with the token
	rr = do(t, s, http.MethodPost, "/api/v1/config/apply", api.ApplyRequest{Token: rev.Token})
	if rr.Code != 200 {
		t.Fatalf("apply code %d: %s", rr.Code, rr.Body)
	}
	var applied api.AppliedResponse
	json.Unmarshal(rr.Body.Bytes(), &applied)
	if applied.BackupID == "" {
		t.Fatal("no backup id")
	}
	if !*reloaded {
		t.Fatal("reload not called")
	}
	// Live file changed, comment preserved.
	b, _ := os.ReadFile(cfgPath)
	if !strings.Contains(string(b), "concurrency: 8   # inline") {
		t.Fatalf("live file wrong:\n%s", b)
	}
	if !strings.Contains(validated, "concurrency: 8") {
		t.Fatalf("validate did not see the candidate")
	}
	// History has one entry; restore reverts.
	rr = do(t, s, http.MethodGet, "/api/v1/config/history", nil)
	var hr struct {
		History []api.HistoryItem `json:"history"`
	}
	json.Unmarshal(rr.Body.Bytes(), &hr)
	if len(hr.History) != 1 {
		t.Fatalf("history len %d", len(hr.History))
	}
}

func TestApplyRejectsInvalidCandidate(t *testing.T) {
	s, cfgPath, _ := newEditServer(t, func(string) error {
		return errInvalid
	})
	do(t, s, http.MethodPost, "/api/v1/config/stage", api.StageRequest{Path: []string{"settings", "concurrency"}, Value: 0})
	rr := do(t, s, http.MethodGet, "/api/v1/config/review", nil)
	var rev api.ReviewResponse
	json.Unmarshal(rr.Body.Bytes(), &rev)
	rr = do(t, s, http.MethodPost, "/api/v1/config/apply", api.ApplyRequest{Token: rev.Token})
	if rr.Code == 200 {
		t.Fatal("apply should reject an invalid candidate")
	}
	b, _ := os.ReadFile(cfgPath)
	if string(b) != editCfg {
		t.Fatal("live file changed despite validation failure")
	}
}

func TestWideningSurfaced(t *testing.T) {
	s, _, _ := newEditServer(t, nil)
	do(t, s, http.MethodPost, "/api/v1/config/stage", api.StageRequest{Path: []string{"sandbox", "agents"}, Value: "off"})
	rr := do(t, s, http.MethodGet, "/api/v1/config/review", nil)
	var rev api.ReviewResponse
	json.Unmarshal(rr.Body.Bytes(), &rev)
	if len(rev.Widening) != 1 {
		t.Fatalf("expected 1 widening, got %d (%+v)", len(rev.Widening), rev.Effects)
	}
}

func TestSectionReads(t *testing.T) {
	s, _, _ := newEditServer(t, nil)

	var sec api.SecretsResponse
	json.Unmarshal(do(t, s, http.MethodGet, "/api/v1/secrets", nil).Body.Bytes(), &sec)
	if len(sec.Vaults) != 1 || sec.Vaults[0].Name != "main" || sec.Vaults[0].Type != "onepassword" {
		t.Fatalf("vaults: %+v", sec.Vaults)
	}
	if len(sec.Secrets) != 1 || sec.Secrets[0].Ref != "main/api-token" {
		t.Fatalf("secrets: %+v", sec.Secrets)
	}

	var packs api.PacksResponse
	json.Unmarshal(do(t, s, http.MethodGet, "/api/v1/packs", nil).Body.Bytes(), &packs)
	if len(packs.Packs) != 1 || packs.Packs[0].Version != "2.9.0" {
		t.Fatalf("packs: %+v", packs.Packs)
	}

	var plugins api.PluginsResponse
	json.Unmarshal(do(t, s, http.MethodGet, "/api/v1/plugins", nil).Body.Bytes(), &plugins)
	if len(plugins.Plugins) != 1 || plugins.Plugins[0].Name != "github" || !plugins.Plugins[0].Pinned || !plugins.Plugins[0].Builtin {
		t.Fatalf("plugins: %+v", plugins.Plugins)
	}

	var sys api.SystemResponse
	json.Unmarshal(do(t, s, http.MethodGet, "/api/v1/system", nil).Body.Bytes(), &sys)
	if sys.Version != "test-1.0.0" || sys.Safety.SandboxAgents != "on" || !sys.Update.Auto {
		t.Fatalf("system: %+v", sys)
	}
}

var errInvalid = &validationError{"settings.concurrency: must be >= 1"}

type validationError struct{ msg string }

func (e *validationError) Error() string { return e.msg }
