package controller

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/NodeSpy/conductor/internal/agentsandbox"
	"github.com/NodeSpy/conductor/internal/dispatch"
	"github.com/NodeSpy/conductor/internal/sandbox"
)

// A cached launch's credential values — what its publish is scanned for —
// are the dispatch's credentials, its git fetch secrets, its skill token,
// and its credential-named environment; never a path or a plain value.
func TestLaunchSecretsCollectsTheLaunchsCredentials(t *testing.T) {
	req := &dispatch.Request{Credentials: dispatch.Credentials{
		Env:       map[string]string{"GH_TOKEN": "ghs_envvalue"},
		Templates: map[string]string{"api": "tmpl-secret-value"},
	}}
	pol := agentsandbox.Policy{Token: "skill-token-value", GitAuth: []sandbox.GitAuth{{Secret: "git-fetch-secret"}}}
	got := launchSecrets(req, pol, []string{"ANTHROPIC_API_KEY=sk-ant-value", "PATH=/usr/bin:/bin", "HOME=/home/x", "NPM_AUTH=npm-auth-value"})
	for _, want := range []string{"ghs_envvalue", "tmpl-secret-value", "skill-token-value", "git-fetch-secret", "sk-ant-value", "npm-auth-value"} {
		if !slices.Contains(got, want) {
			t.Errorf("%s not collected: %v", want, got)
		}
	}
	for _, not := range []string{"/usr/bin:/bin", "/home/x"} {
		if slices.Contains(got, not) {
			t.Errorf("%s collected: %v", not, got)
		}
	}
}

// What the agent really holds is screened: a step env value under a name
// the sandbox keeps (DEPLOY_KEY={{.secrets.deploy}}), a secret-shaped
// value under any name, the {{.secrets}}/{{.vaults}} values its templates
// could render, and the tokens of the login files shared or copied into
// its home. A plain value (NODE_ENV=production), a path or a URL is not.
func TestStepEnvSecretIsScreened(t *testing.T) {
	const v = "deploy-secret-value-0123456789"
	env := []string{"DEPLOY_KEY=" + v, "NPM_CONFIG__AUTH=" + v + "2", "SIGNING_PASSPHRASE=correct horse battery staple",
		"BUILD_ID=a8Fq20LmXz93kQ7vB1", "NODE_ENV=production", "HOME=/var/x/home1234567890ab",
		"GIT_CONFIG_VALUE_0=ssh://git@github.com/", "MIRROR=https://mirror2.example.com/simple/"}
	kept := nonCredentialEnv(env)
	login := filepath.Join(t.TempDir(), ".credentials.json")
	if err := os.WriteFile(login, []byte(`{"claudeAiOauth":{"accessToken":"sk-ant-oat01-ACCESS0123456789","refreshToken":"sk-ant-ort01-REFRESH0123456789","expiresAt":1790000000000,"scopes":["user:inference"]}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	req := &dispatch.Request{Data: map[string]any{
		"secrets": map[string]any{"deploy": "named-secret-value-42"},
		"vaults":  map[string]any{"prod": map[string]any{"db": "vault-entry-value-43"}},
	}}
	got := launchSecrets(req, agentsandbox.Policy{Logins: []agentsandbox.Login{{Src: login}}}, kept)
	all := strings.Join(got, "\n")
	for _, kv := range kept {
		k, val, _ := strings.Cut(kv, "=")
		want := k != "NODE_ENV" && k != "HOME" && k != "MIRROR" && !strings.HasPrefix(k, "GIT_CONFIG_")
		if slices.Contains(got, val) != want {
			t.Errorf("%s: screened %v, want %v", kv, !want, want)
		}
	}
	for _, w := range []string{"named-secret-value-42", "vault-entry-value-43", "sk-ant-oat01-ACCESS0123456789", "sk-ant-ort01-REFRESH0123456789"} {
		if !strings.Contains(all, w) {
			t.Errorf("%s not screened", w)
		}
	}
	if strings.Contains(all, "1790000000000") || strings.Contains(all, "user:inference") {
		t.Errorf("a login file's non-token field is screened: %v", got)
	}
}
