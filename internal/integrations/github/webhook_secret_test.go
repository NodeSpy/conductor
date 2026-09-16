package github

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/NodeSpy/conductor/internal/core"
)

// decodeConfig builds an integration the way the loader does — from YAML —
// so these tests exercise the keys an operator actually writes.
func decodeConfig(t *testing.T, doc string) (core.Integration, error) {
	t.Helper()
	return newIntegration("gh", func(v any) error { return yaml.Unmarshal([]byte(doc), v) })
}

// The webhook secret and the signature switch are read from `webhook:`.
func TestWebhookSecretParsesUnderWebhook(t *testing.T) {
	ig, err := decodeConfig(t, `
app: { app_id: 1, private_key_path: x }
webhook:
  smee_url: https://smee.io/x
  secret: s3cr3t
  verify_signature: true
`)
	if err != nil {
		t.Fatal(err)
	}
	g := ig.(*Integration)
	if g.cfg.Webhook.Secret != "s3cr3t" {
		t.Fatalf("webhook.secret not read: %q", g.cfg.Webhook.Secret)
	}
	if !g.cfg.Webhook.Verify() {
		t.Fatal("webhook.verify_signature: true not read")
	}
	// The secret that arrives under webhook: is the one HMAC verification uses.
	body := []byte(`{"action":"opened"}`)
	if !verifySignature(g.cfg.Webhook.Secret, body, sign("s3cr3t", body)) {
		t.Fatal("a delivery signed with webhook.secret was rejected")
	}
	if verifySignature(g.cfg.Webhook.Secret, body, sign("wrong", body)) {
		t.Fatal("a delivery signed with the wrong secret was accepted")
	}
}

// verify_signature defaults to ON: an unset switch must not quietly turn
// verification off when the key moves house.
func TestWebhookVerifyDefaultsOn(t *testing.T) {
	ig, err := decodeConfig(t, `
app: { app_id: 1, private_key_path: x }
webhook: { smee_url: https://smee.io/x, secret: s }
`)
	if err != nil {
		t.Fatal(err)
	}
	if !ig.(*Integration).cfg.Webhook.Verify() {
		t.Fatal("verification must default to on")
	}
}

// The retired app-block keys are a HARD error naming where they went — not an
// unknown-field message, and never a silent drop.
func TestAppWebhookKeysRejected(t *testing.T) {
	for _, tc := range []struct{ name, doc string }{
		{"secret", `
app: { app_id: 1, private_key_path: x, webhook_secret: s }
webhook: { smee_url: https://smee.io/x }`},
		{"verify_signature", `
app: { app_id: 1, private_key_path: x, verify_signature: false }
webhook: { smee_url: https://smee.io/x, secret: s }`},
		{"appless", `
token: t
app: { webhook_secret: s }
webhook: { listen: 127.0.0.1:8787 }`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := decodeConfig(t, tc.doc)
			if err == nil {
				t.Fatal("the retired app-block webhook keys must be refused")
			}
			if !errors.Is(err, ErrAppWebhookMoved) {
				t.Fatalf("want ErrAppWebhookMoved, got %v", err)
			}
			for _, want := range []string{"github[gh]", "webhook.secret", "webhook.verify_signature", "conductor config migrate"} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error does not name %q: %v", want, err)
				}
			}
		})
	}
}

// App-less is the case the move exists for: no app: block at all, the webhook
// carrying both its transport and its verification.
func TestApplessWebhookSecretValidates(t *testing.T) {
	ig, err := decodeConfig(t, `
token: ghp_x
webhook: { listen: 127.0.0.1:8787, secret: s3cr3t, verify_signature: true }
rules:
  - match: { repos: ["acme/w"] }
    actions: { merge_conflict: { type: command, command: [true] } }
`)
	if err != nil {
		t.Fatal(err)
	}
	if err := ig.Validate(); err != nil {
		t.Fatalf("App-less webhook.secret must validate: %v", err)
	}
	// An EMPTY app: block is App-less too — nothing is left in it to require.
	ig, err = decodeConfig(t, `
token: ghp_x
app: {}
webhook: { listen: 127.0.0.1:8787, secret: s3cr3t }
rules:
  - match: { repos: ["acme/w"] }
    actions: { merge_conflict: { type: command, command: [true] } }
`)
	if err != nil {
		t.Fatal(err)
	}
	if err := ig.Validate(); err != nil {
		t.Fatalf("an empty app: block must stay App-less: %v", err)
	}
}

// Verification on with nowhere to get the secret from is the check that used
// to read App.WebhookSecret; it must now name webhook.secret.
func TestWebhookVerifyWithoutSecretErrors(t *testing.T) {
	ig, err := decodeConfig(t, `
token: ghp_x
webhook: { listen: 127.0.0.1:8787, verify_signature: true }
`)
	if err != nil {
		t.Fatal(err)
	}
	err = ig.Validate()
	if err == nil || !strings.Contains(err.Error(), "webhook.secret required") {
		t.Fatalf("want a webhook.secret-required error, got %v", err)
	}
}

// A real App still validates with its secret under webhook: — the move does
// not make the App path a special case.
func TestAppWithWebhookSecretValidates(t *testing.T) {
	keyPath := filepath.Join(t.TempDir(), "key.pem")
	if err := os.WriteFile(keyPath, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	ig, err := decodeConfig(t, `
app: { app_id: 1, private_key_path: `+keyPath+` }
webhook: { smee_url: https://smee.io/x, secret: s3cr3t }
rules:
  - match: { repos: ["acme/w"] }
    actions: { merge_conflict: { type: command, command: [true] } }
`)
	if err != nil {
		t.Fatal(err)
	}
	if err := ig.Validate(); err != nil {
		t.Fatalf("App + webhook.secret must validate: %v", err)
	}
}
