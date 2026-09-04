package github

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/NodeSpy/conductor/internal/config"
)

// validatableConfig passes the App/webhook checks so Validate reaches the
// action-kind allowlist. Uses a temp private key so os.Stat succeeds.
func validatableConfig(t *testing.T, actions map[string]config.Action) Config {
	t.Helper()
	keyPath := filepath.Join(t.TempDir(), "key.pem")
	if err := os.WriteFile(keyPath, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	return Config{
		App:     AppConfig{AppID: 1, PrivateKeyPath: keyPath, WebhookSecret: "s"},
		Webhook: WebhookConfig{SmeeURL: "https://smee.io/x"},
		Rules:   []Rule{{Match: Match{Repos: []string{"acme/*"}}, Actions: as1(actions)}},
	}
}

func TestValidateRejectsUnknownKind(t *testing.T) {
	// The old name is no longer a kind → validate must catch it (not silently no-op).
	g := newTestIntegration(t, validatableConfig(t, map[string]config.Action{
		"issue_ready": {Type: "agent", Agent: "fixer"},
	}))
	err := g.Validate()
	if err == nil || !strings.Contains(err.Error(), "issue_ready") {
		t.Fatalf("expected unknown-kind error mentioning issue_ready, got %v", err)
	}
}

func TestValidateAcceptsKnownKinds(t *testing.T) {
	g := newTestIntegration(t, validatableConfig(t, map[string]config.Action{
		"issue_matched":     {Type: "agent", Agent: "fixer"},
		"changes_requested": {Type: "agent", Agent: "fixer"},
		"review_requested":  {Type: "command", Command: []string{"critique"}},
	}))
	if err := g.Validate(); err != nil {
		t.Fatalf("known kinds should validate, got %v", err)
	}
}
