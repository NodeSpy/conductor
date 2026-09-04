package migrate

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/connector"
	"github.com/NodeSpy/conductor/internal/secrets"
	"github.com/NodeSpy/conductor/internal/vaults"
)

// TestVaultsPassStandalone: a connectors-schema config carrying every legacy
// form — scheme refs, a secrets: block with usages, an oauth refresh_token
// vault: ref — is rewritten in one pass, and the pass is idempotent.
func TestVaultsPassStandalone(t *testing.T) {
	pre := `
secrets:
  pat: env:MY_PAT
  lit: literal-value
  ghv: vault:gh
connectors:
  api:
    type: rest
    base_url: http://x
    auth: { type: bearer, token: vault:gh }
    verbs: { v: { method: GET, path: / } }
  xero:
    type: rest
    base_url: http://y
    auth:
      type: oauth2
      grant: refresh_token
      token_url: http://t
      client_id: cid
      client_secret: op://Private/Xero/secret
      refresh_token: vault:xero_rt
    verbs: { v: { method: GET, path: / } }
  chat:
    type: slack
    app_token: pass:conductor/slack-app
    bot_token: file:/run/secrets/slack-bot
stores:
  db: { type: sqlite, path: "file:/data/x.sqlite?mode=ro" }
triggers:
  - on: api.v
    steps:
      - uses: api.v
        options: { text: "p={{.secrets.pat}} l={{.secrets.lit}} g={{ .secrets.ghv }}" }
`
	res, err := Transform([]byte(pre))
	if err != nil {
		t.Fatal(err)
	}
	if !res.Changed {
		t.Fatal("the pass must fire")
	}
	out := string(res.Output)

	// No legacy forms survive (env: is the baseline and stays;
	// the sqlite file: DSN under path: is exempt).
	for _, gone := range []string{"vault:gh", "vault:xero_rt", "op://", "pass:conductor", "file:/run/secrets", "secrets:", ".secrets."} {
		if strings.Contains(out, gone) {
			t.Errorf("output still contains %q:\n%s", gone, out)
		}
	}
	for _, want := range []string{
		`{{ vault "local" "gh" }}`,
		`{{ vault "local" "xero_rt" }}`,
		`{{ vault "op" "Private/Xero/secret" }}`,
		`{{ vault "pass" "conductor/slack-app" }}`,
		`{{ vault "files" "slack-bot" }}`,
		"token_vault: local",
		"local: {type: conductor}",
		"op: {type: onepassword}",
		"pass: {type: pass}",
		"files: {type: file, dir: /run/secrets}",
		`file:/data/x.sqlite?mode=ro`,
		"p=${MY_PAT} l=literal-value g=",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}

	// The output parses and passes config validation (no secrets: rejection).
	var cfg config.Config
	if err := yaml.Unmarshal(res.Output, &cfg); err != nil {
		t.Fatal(err)
	}
	if err := cfg.NormalizeTriggers(); err != nil {
		t.Fatal(err)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("migrated config must validate: %v", err)
	}

	// Idempotent: a second Transform changes nothing.
	res2, err := Transform(res.Output)
	if err != nil {
		t.Fatal(err)
	}
	if res2.Changed {
		t.Fatalf("second pass changed the output:\n%s", string(res2.Output))
	}
}

// TestVaultsPassLeftoverSecretsUseErrors: a .secrets read the rewrite can't
// resolve is a hard error, never silent loss.
func TestVaultsPassLeftoverSecretsUseErrors(t *testing.T) {
	pre := `
secrets:
  pat: env:MY_PAT
connectors:
  box: { type: command }
triggers:
  - on: manual
    steps:
      - uses: box.run
        options: { command: "echo {{.secrets.undeclared}}" }
`
	if _, err := Transform([]byte(pre)); err == nil || !strings.Contains(err.Error(), "migrate it by hand") {
		t.Fatalf("undeclared .secrets use: %v", err)
	}
}

// TestVaultsPassReusesExistingEntries: refs land in already-declared vaults
// when they match instead of minting duplicates.
func TestVaultsPassReusesExistingEntries(t *testing.T) {
	pre := `
vaults:
  house: { type: conductor }
  secretdir: { type: file, dir: /run/secrets }
connectors:
  api:
    type: rest
    base_url: http://x
    auth: { type: bearer, token: vault:gh }
    verbs: { v: { method: GET, path: / } }
  chat:
    type: slack
    app_token: file:/run/secrets/app
    bot_token: file:/other/dir/bot
`
	res, err := Transform([]byte(pre))
	if err != nil {
		t.Fatal(err)
	}
	out := string(res.Output)
	for _, want := range []string{
		`{{ vault "house" "gh" }}`,
		`{{ vault "secretdir" "app" }}`,
		`{{ vault "files" "bot" }}`,
		"files: {type: file, dir: /other/dir}",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "local:") {
		t.Errorf("must reuse the existing conductor vault, got:\n%s", out)
	}
}

// TestVaultsPassInsideLegacyTransform: a fully-legacy config whose
// credential fields carry scheme refs comes out on the connectors schema
// with the refs rewritten — one migration, no intermediate state.
func TestVaultsPassInsideLegacyTransform(t *testing.T) {
	pre := `
integrations:
  - name: chat
    type: slack
    app_token: vault:slack_app
    bot_token: ${SLACK_BOT}
    channel: C123
    actions:
      - on: app_mention
        prompt: "hi"
controller: { type: paseo }
`
	res, err := Transform([]byte(pre))
	if err != nil {
		t.Fatal(err)
	}
	if !res.Changed {
		t.Fatal("legacy config must transform")
	}
	out := string(res.Output)
	if strings.Contains(out, "vault:slack_app") {
		t.Fatalf("legacy scheme ref survived:\n%s", out)
	}
	for _, want := range []string{`{{ vault "local" "slack_app" }}`, "local: {type: conductor}", "${SLACK_BOT}"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
}

// TestVaultsMigrationBehavioralEquivalence is the golden: the same secrets
// resolve to the same values after migration, and the same connectors get
// credentials — through the real vault backends (conductor + file real, op
// via an injected exec).
func TestVaultsMigrationBehavioralEquivalence(t *testing.T) {
	t.Cleanup(vaults.Reset)
	home := t.TempDir()
	t.Setenv("HOME", home)
	key, err := secrets.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("CONDUCTOR_VAULT_KEY", key)
	t.Setenv("CREDENTIALS_DIRECTORY", "")
	t.Setenv("MY_PAT", "env-val-1")

	// The pre-migration world: the single vault file at the default path,
	// a mounted secret file, an env secret.
	vpath := filepath.Join(home, ".config/conductor/vault.json")
	v, err := secrets.InitVault(vpath, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	_ = v.Set("gh", "tok-vault-1")
	_ = v.Set("xero_rt", "rt-seed-1")
	if err := v.Save(); err != nil {
		t.Fatal(err)
	}
	secDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(secDir, "slack-bot"), []byte("tok-file-1\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	pre := `
secrets:
  pat: env:MY_PAT
connectors:
  api:
    type: rest
    base_url: http://x.invalid
    auth: { type: bearer, token: vault:gh }
    verbs: { v: { method: GET, path: / } }
  opapi:
    type: rest
    base_url: http://y.invalid
    auth: { type: bearer, token: op://Private/GitHub/token }
    verbs: { v: { method: GET, path: / } }
  chat:
    type: slack
    app_token: file:` + secDir + `/slack-bot
    bot_token: file:` + secDir + `/slack-bot
triggers:
  - on: manual
    steps: [ { uses: api.v, options: { text: "{{.secrets.pat}}" } } ]
`
	res, err := Transform([]byte(pre))
	if err != nil {
		t.Fatal(err)
	}
	var cfg config.Config
	if err := yaml.Unmarshal(res.Output, &cfg); err != nil {
		t.Fatal(err)
	}

	sec := secrets.New()
	opCalls := 0
	deps := connector.Deps{Secrets: sec, Config: &cfg,
		VaultExec: func(_ context.Context, stdin string, env []string, name string, args ...string) (string, error) {
			opCalls++
			if name != "op" || args[2] != "op://Private/GitHub/token" {
				t.Fatalf("op exec: %s %v", name, args)
			}
			return "tok-op-1", nil
		}}
	reg, err := connector.Build(&cfg, deps)
	if err != nil {
		t.Fatal(err)
	}

	// Same values resolve through the migrated model.
	ctx := context.Background()
	if got, err := vaults.Read(ctx, "local", "gh"); err != nil || got != "tok-vault-1" {
		t.Fatalf("conductor value: %q %v", got, err)
	}
	fileVault := ""
	for _, n := range vaults.Names() {
		if vaults.Type(n) == "file" {
			fileVault = n
		}
	}
	if got, err := vaults.Read(ctx, fileVault, "slack-bot"); err != nil || got != "tok-file-1" {
		t.Fatalf("file value: %q %v", got, err)
	}
	if got, err := vaults.Read(ctx, "op", "Private/GitHub/token"); err != nil || got != "tok-op-1" || opCalls != 1 {
		t.Fatalf("op value: %q %v (calls %d)", got, err, opCalls)
	}
	// The env-backed secret usage became the loader-expanded ${VAR} form.
	if !strings.Contains(string(res.Output), "${MY_PAT}") {
		t.Fatalf("env secret usage lost:\n%s", string(res.Output))
	}

	// The same connectors get credentials: nothing is disabled.
	for _, name := range []string{"api", "opapi", "chat"} {
		in, ok := reg.Get(name)
		if !ok || in.DisabledReason != "" {
			t.Fatalf("connector %s after migration: ok=%v disabled=%q", name, ok, in.DisabledReason)
		}
	}
	// The oauth-less bearer token resolved to the vault value (tracked →
	// redacted).
	if got := sec.Redact("x tok-vault-1 y"); !strings.Contains(got, secrets.Placeholder) {
		t.Fatalf("migrated credential not tainted: %q", got)
	}
}
