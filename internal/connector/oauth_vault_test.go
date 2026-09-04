package connector

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/secrets"
	"github.com/NodeSpy/conductor/internal/vaults"
)

// tokenVaultYAML builds an oauth2 rest connector with a token_vault backed
// by a conductor vault at vpath.
func tokenVaultYAML(o *oauthTestServer, grant, vpath, vkey, extra string) string {
	return `
connectors:
  api:
    type: rest
    base_url: ` + o.URL + `/api
    auth:
      type: oauth2
      grant: ` + grant + `
      token_url: ` + o.URL + `/token
      client_id: cid
      client_secret: csec
      token_vault: house
      scopes: [read, offline]
` + extra + `
    verbs:
      get: { method: GET, path: /thing, output: { ok: "{{.response.body.ok}}" } }
vaults:
  house: { type: conductor, path: ` + vpath + `, unlock: { key: "` + vkey + `" } }
`
}

// TestOAuth2TokenVaultRotationAndRestart: with a token_vault, a provider
// rotation persists the new refresh token (plus access token and expiry)
// under oauth/<connector>/*, and a REBUILT registry (the daemon-restart
// case) picks the rotated token from the vault over the refresh_token: seed.
func TestOAuth2TokenVaultRotationAndRestart(t *testing.T) {
	t.Cleanup(vaults.Reset)
	o := newOAuthServer(t)
	o.nextRotate.Store("rt-2")
	vpath, vkey := seedConductorVault(t)
	y := tokenVaultYAML(o, "refresh_token", vpath, vkey, "      refresh_token: rt-1\n")

	reg := buildAPIRegistry(t, y, secrets.New())
	in, _ := reg.Get("api")
	if in.DisabledReason != "" {
		t.Fatalf("disabled: %s", in.DisabledReason)
	}
	if _, err := in.Invoke(context.Background(), "get", nil); err != nil {
		t.Fatal(err)
	}
	// The seed was used for the first fetch…
	if form := o.lastForm.Load().(interface{ Get(string) string }); form.Get("refresh_token") != "rt-1" {
		t.Fatalf("first refresh form: %v", form)
	}
	// …and the rotation + fresh access token persisted to the vault.
	ctx := context.Background()
	if got, err := vaults.Read(ctx, "house", "oauth/api/refresh_token"); err != nil || got != "rt-2" {
		t.Fatalf("rotated refresh in vault: %q %v", got, err)
	}
	if got, err := vaults.Read(ctx, "house", "oauth/api/access_token"); err != nil || got != "at-1" {
		t.Fatalf("access token in vault: %q %v", got, err)
	}
	if exp, err := vaults.Read(ctx, "house", "oauth/api/expiry"); err != nil {
		t.Fatalf("expiry in vault: %v", err)
	} else if _, perr := time.Parse(time.RFC3339, exp); perr != nil {
		t.Fatalf("expiry format: %q", exp)
	}

	// "Restart": rebuild everything; the next fetch must use rt-2, not the
	// rt-1 seed.
	vaults.Reset()
	o.current.Store("at-2") // force a fresh token fetch on the new build
	reg2 := buildAPIRegistry(t, y, secrets.New())
	in2, _ := reg2.Get("api")
	if _, err := in2.Invoke(context.Background(), "get", nil); err != nil {
		t.Fatal(err)
	}
	if form := o.lastForm.Load().(interface{ Get(string) string }); form.Get("refresh_token") != "rt-2" {
		t.Fatalf("post-restart refresh form used the seed, want the vault token: %v", form)
	}
}

// TestOAuth2TokenVaultMustBeUsable: an undefined or read-only token_vault
// disables the connector with a clear reason.
func TestOAuth2TokenVaultMustBeUsable(t *testing.T) {
	t.Cleanup(vaults.Reset)
	o := newOAuthServer(t)
	var cfg config.Config
	if err := yaml.Unmarshal([]byte(`
connectors:
  api:
    type: rest
    base_url: `+o.URL+`/api
    auth: { type: oauth2, grant: refresh_token, token_url: `+o.URL+`/token, client_id: cid, token_vault: ghost }
    verbs: { get: { method: GET, path: /thing } }
`), &cfg); err != nil {
		t.Fatal(err)
	}
	reg, err := Build(&cfg, Deps{Secrets: secrets.New(), Config: &cfg})
	if err != nil {
		t.Fatal(err)
	}
	in, _ := reg.Get("api")
	if !strings.Contains(in.DisabledReason, `no vault named "ghost"`) {
		t.Fatalf("undefined token_vault: %q", in.DisabledReason)
	}

	// Read-only vault: disabled naming the capability.
	vaults.Reset()
	fdir := t.TempDir()
	var cfg2 config.Config
	if err := yaml.Unmarshal([]byte(`
connectors:
  api:
    type: rest
    base_url: `+o.URL+`/api
    auth: { type: oauth2, grant: refresh_token, token_url: `+o.URL+`/token, client_id: cid, token_vault: files }
    verbs: { get: { method: GET, path: /thing } }
vaults:
  files: { type: file, dir: `+fdir+` }
`), &cfg2); err != nil {
		t.Fatal(err)
	}
	reg2, err := Build(&cfg2, Deps{Secrets: secrets.New(), Config: &cfg2})
	if err != nil {
		t.Fatal(err)
	}
	in2, _ := reg2.Get("api")
	if !strings.Contains(in2.DisabledReason, "read-only") {
		t.Fatalf("read-only token_vault: %q", in2.DisabledReason)
	}
}

// TestDeviceFlowBootstrap: the device grant requests a user code, polls
// through authorization_pending, and stores the captured tokens in the
// token_vault.
func TestDeviceFlowBootstrap(t *testing.T) {
	t.Cleanup(vaults.Reset)
	old := devicePollInterval
	devicePollInterval = func(int) time.Duration { return time.Millisecond }
	t.Cleanup(func() { devicePollInterval = old })

	var polls atomic.Int64
	mux := http.NewServeMux()
	mux.HandleFunc("/device", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if r.Form.Get("client_id") != "cid" || r.Form.Get("scope") != "read offline" {
			t.Errorf("device form: %v", r.Form)
		}
		fmt.Fprint(w, `{"device_code":"dc-1","user_code":"ABCD-1234","verification_uri":"https://login.example/device","expires_in":300,"interval":1}`)
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if r.Form.Get("grant_type") != "urn:ietf:params:oauth:grant-type:device_code" || r.Form.Get("device_code") != "dc-1" {
			t.Errorf("poll form: %v", r.Form)
		}
		if polls.Add(1) < 3 {
			w.WriteHeader(400)
			fmt.Fprint(w, `{"error":"authorization_pending"}`)
			return
		}
		fmt.Fprint(w, `{"access_token":"dev-at","expires_in":900,"refresh_token":"dev-rt"}`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	vpath, vkey := seedConductorVault(t)
	var cfg config.Config
	if err := yaml.Unmarshal([]byte(`
connectors:
  iot:
    type: rest
    base_url: `+srv.URL+`/api
    auth:
      type: oauth2
      grant: device
      token_url: `+srv.URL+`/token
      device_auth_url: `+srv.URL+`/device
      client_id: cid
      token_vault: house
      scopes: [read, offline]
    verbs: { get: { method: GET, path: /thing } }
vaults:
  house: { type: conductor, path: `+vpath+`, unlock: { key: "`+vkey+`" } }
`), &cfg); err != nil {
		t.Fatal(err)
	}

	var sb strings.Builder
	if err := AuthBootstrap(context.Background(), &cfg, secrets.New(), "iot", &sb, nil); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(sb.String(), "ABCD-1234") || !strings.Contains(sb.String(), "login.example/device") {
		t.Fatalf("device instructions: %q", sb.String())
	}
	if strings.Contains(sb.String(), "dev-at") || strings.Contains(sb.String(), "dev-rt") {
		t.Fatalf("token values must not print: %q", sb.String())
	}
	ctx := context.Background()
	if got, _ := vaults.Read(ctx, "house", "oauth/iot/refresh_token"); got != "dev-rt" {
		t.Fatalf("device refresh stored: %q", got)
	}
	if got, _ := vaults.Read(ctx, "house", "oauth/iot/access_token"); got != "dev-at" {
		t.Fatalf("device access stored: %q", got)
	}
	if polls.Load() != 3 {
		t.Fatalf("polls: %d (want pending, pending, success)", polls.Load())
	}
}

// TestAuthListAndRevoke: ls reports per-connector login state + expiry from
// the token_vault; --revoke clears the stored keys.
func TestAuthListAndRevoke(t *testing.T) {
	t.Cleanup(vaults.Reset)
	vpath, vkey := seedConductorVault(t)
	var cfg config.Config
	if err := yaml.Unmarshal([]byte(`
connectors:
  xero:
    type: rest
    base_url: http://x
    auth: { type: oauth2, grant: authorization_code, token_url: http://t, auth_url: http://a, client_id: c, token_vault: house }
    verbs: { v: { method: GET, path: / } }
  fresh:
    type: rest
    base_url: http://y
    auth: { type: oauth2, grant: device, token_url: http://t, device_auth_url: http://d, client_id: c, token_vault: house }
    verbs: { v: { method: GET, path: / } }
  plain:
    type: rest
    base_url: http://z
    auth: { type: bearer, token: tok }
    verbs: { v: { method: GET, path: / } }
vaults:
  house: { type: conductor, path: `+vpath+`, unlock: { key: "`+vkey+`" } }
`), &cfg); err != nil {
		t.Fatal(err)
	}
	deps := Deps{Secrets: secrets.New(), Config: &cfg}
	ctx := context.Background()

	// Seed xero's stored tokens as a completed login would.
	if _, _, err := OpenVaultBackendRegistered(&cfg, "house", deps); err != nil {
		t.Fatal(err)
	}
	exp := time.Now().Add(30 * time.Minute).Format(time.RFC3339)
	for k, v := range map[string]string{
		"oauth/xero/access_token":  "at-9",
		"oauth/xero/refresh_token": "rt-9",
		"oauth/xero/expiry":        exp,
	} {
		if err := vaults.Write(ctx, "house", k, v); err != nil {
			t.Fatal(err)
		}
	}

	statuses, err := AuthList(ctx, &cfg, deps)
	if err != nil {
		t.Fatal(err)
	}
	if len(statuses) != 2 { // plain (bearer) is not oauth2
		t.Fatalf("statuses: %+v", statuses)
	}
	byName := map[string]AuthStatus{}
	for _, s := range statuses {
		byName[s.Name] = s
	}
	if s := byName["xero"]; !s.HasRefresh || s.Expiry != exp || s.TokenVault != "house" || s.Grant != "authorization_code" {
		t.Fatalf("xero status: %+v", s)
	}
	if s := byName["fresh"]; s.HasRefresh || s.Expiry != "" {
		t.Fatalf("fresh status: %+v", s)
	}

	// Revoke clears the three keys.
	if err := AuthRevoke(ctx, &cfg, deps, "xero"); err != nil {
		t.Fatal(err)
	}
	if _, err := vaults.Read(ctx, "house", "oauth/xero/refresh_token"); err == nil {
		t.Fatal("refresh token survived revoke")
	}
	statuses, _ = AuthList(ctx, &cfg, deps)
	byName = map[string]AuthStatus{}
	for _, s := range statuses {
		byName[s.Name] = s
	}
	if byName["xero"].HasRefresh {
		t.Fatal("xero still reports logged in after revoke")
	}
	// Revoking again: nothing stored.
	if err := AuthRevoke(ctx, &cfg, deps, "xero"); err == nil || !strings.Contains(err.Error(), "no stored tokens") {
		t.Fatalf("double revoke: %v", err)
	}
	// Unknown connector / no token_vault errors.
	if err := AuthRevoke(ctx, &cfg, deps, "ghost"); err == nil {
		t.Fatal("unknown connector must error")
	}
}
