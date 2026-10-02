package github

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"testing"

	"github.com/NodeSpy/conductor/internal/config"
)

// as1 wraps single actions into one-variant ActionSets, keeping the many
// single-action test configs terse after the multi-variant (ActionSet) change.
func as1(m map[string]config.Action) map[string]config.ActionSet {
	out := make(map[string]config.ActionSet, len(m))
	for k, v := range m {
		out[k] = config.ActionSet{v}
	}
	return out
}

// newTestIntegration builds an Integration from an in-memory Config.
func newTestIntegration(t *testing.T, cfg Config) *Integration {
	t.Helper()
	ig, err := newIntegration("test", func(v any) error {
		*(v.(*Config)) = cfg
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return ig.(*Integration)
}

func baseConfig() Config {
	return Config{
		App:     AppConfig{AppID: 1, PrivateKeyPath: "x"},
		Webhook: WebhookConfig{SmeeURL: "https://smee.io/x", Secret: "s"},
		Rules: []Rule{{
			Match:    Match{Repos: []string{"acme/*"}},
			Reviewer: config.Actors{Logins: []string{"me"}},
			Assignee: config.Actors{Logins: []string{"me"}},
			Actions: as1(map[string]config.Action{
				"changes_requested":     {Type: "agent", Agent: "fixer"},
				"new_comment":           {Type: "agent", Agent: "fixer"},
				"issue_matched":         {Type: "agent", Agent: "fixer", Checkout: "branch-off"},
				"release":               {Type: "agent", Agent: "fixer", Checkout: "none"},
				"deployment_status":     {Type: "agent", Agent: "fixer", Checkout: "none"},
				"dependabot_alert":      {Type: "agent", Agent: "fixer", Checkout: "branch-off"},
				"secret_scanning_alert": {Type: "command", Command: []string{"echo"}},
			}),
		}},
	}
}

// sign is the X-Hub-Signature-256 a delivery signed with secret carries.
func sign(secret string, body []byte) string {
	m := hmac.New(sha256.New, []byte(secret))
	m.Write(body)
	return "sha256=" + hex.EncodeToString(m.Sum(nil))
}
