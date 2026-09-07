package connector

import (
	"strings"
	"testing"

	"github.com/NodeSpy/conductor/internal/secrets"
)

// TestChatWebhookURLTracked proves the incoming-webhook URL — which embeds a
// bearer token in its path — is registered for redaction when the slack and
// discord connectors are built. It is a credential, not a public endpoint, so
// it must never survive into logs or audit records in cleartext.
func TestChatWebhookURLTracked(t *testing.T) {
	const slackHook = "https://hooks.slack.com/services/T0000/B0000/xoxbSECRETpath"
	const discordHook = "https://discord.com/api/webhooks/12345/dscSECRETpath"

	cfg := mustDecodeConfig(t, `
connectors:
  sl:
    type: slack
    webhook_url: `+slackHook+`
  dc:
    type: discord
    webhook_url: `+discordHook+`
`)
	sec := secrets.New()
	if _, err := Build(cfg, Deps{Secrets: sec}); err != nil {
		t.Fatalf("Build: %v", err)
	}

	for _, tc := range []struct {
		name, hook string
	}{{"slack", slackHook}, {"discord", discordHook}} {
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
