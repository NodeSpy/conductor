package controller

import (
	"slices"
	"testing"

	"github.com/NodeSpy/conductor/internal/agentsandbox"
	"github.com/NodeSpy/conductor/internal/dispatch"
	"github.com/NodeSpy/conductor/internal/sandbox"
)

// A cached launch's credential values — what its publish is scanned for —
// are the dispatch's credentials, its git fetch secrets, its skill token,
// and its credential-named environment; nothing else of the environment.
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
