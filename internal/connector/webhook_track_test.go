package connector

import (
	"strings"
	"testing"

	"github.com/NodeSpy/conductor/internal/secrets"
)

// TestChatWebhookURLTracked proves a connection field the plugin declares
// `secret` — an incoming-webhook URL, which embeds a bearer token in its path
// — is registered for redaction even when written literally. It is a
// credential, not a public endpoint, so it must never survive into logs or
// audit records in cleartext.
func TestChatWebhookURLTracked(t *testing.T) {
	const slackHook = "https://hooks.example.com/services/T0000/B0000/xoxbSECRETpath"
	const botToken = "xoxb-literal-SECRET"

	cfg := mustDecodeConfig(t, `
connectors:
  sl:
    use: slack
    webhook_url: `+slackHook+`
  sl2:
    use: slack
    bot_token: `+botToken+`
`)
	sec := secrets.New()
	if _, err := Build(cfg, Deps{Secrets: sec}); err != nil {
		t.Fatalf("Build: %v", err)
	}

	for _, tc := range []struct {
		name, hook string
	}{{"webhook_url", slackHook}, {"bot_token", botToken}} {
		line := "posting via " + tc.hook + " now"
		if red := sec.Redact(line); strings.Contains(red, tc.hook) {
			t.Fatalf("%s webhook URL leaked through Redact: %q", tc.name, red)
		}
	}

	// A non-secret URL must pass through untouched — redaction is scoped to
	// the tracked credentials, not any URL-shaped string.
	const plain = "posting via https://example.com/public now"
	if red := sec.Redact(plain); red != plain {
		t.Fatalf("non-secret URL altered by Redact: %q", red)
	}
}
