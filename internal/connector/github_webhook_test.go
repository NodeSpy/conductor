package connector

import (
	"errors"
	"strings"
	"testing"

	ghint "github.com/NodeSpy/conductor/internal/integrations/github"
	"github.com/NodeSpy/conductor/internal/secrets"
)

// The webhook secret and the signature switch are read from webhook:, and they
// reach the lowered integration — an App-less connector verifies deliveries
// without an app: block anywhere in the file.
func TestGithubConnectorWebhookSecret(t *testing.T) {
	cfg := mustDecodeConfig(t, `
connectors:
  gh:
    use: github
    token: ghp_x
    webhook:
      listen: "127.0.0.1:8787"
      secret: s3cr3t
      verify_signature: true
`)
	reg, err := Build(cfg, Deps{Secrets: secrets.New()})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	in, ok := reg.Get("gh")
	if !ok || in.DisabledReason != "" {
		t.Fatalf("gh not built cleanly: ok=%v reason=%q", ok, in.DisabledReason)
	}
	ig, err := in.Impl.Source([]CompiledTrigger{{
		Index: 0,
		Spec:  mkTriggerSpec(t, "gh.merge_conflict", "", `repo: [acme/w]`),
	}})
	if err != nil {
		t.Fatal(err)
	}
	if err := ig.Validate(); err != nil {
		t.Fatalf("App-less connector with webhook.secret must validate: %v", err)
	}
}

// The retired app-block keys fail the LOAD. A disabled connector would be the
// wrong outcome twice over: the box would boot with verification running off a
// setting the operator believes is in effect, and no retry could ever clear it.
func TestGithubConnectorRejectsAppWebhookKeys(t *testing.T) {
	for _, doc := range []string{`
connectors:
  gh:
    use: github
    app: { app_id: 1, private_key_path: /nonexistent.pem, webhook_secret: s }
    webhook: { listen: "127.0.0.1:8787" }
`, `
connectors:
  gh:
    use: github
    token: ghp_x
    app: { verify_signature: false }
    webhook: { listen: "127.0.0.1:8787", secret: s }
`} {
		cfg := mustDecodeConfig(t, doc)
		_, err := Build(cfg, Deps{Secrets: secrets.New()})
		if err == nil {
			t.Fatal("the retired app-block webhook keys must fail the load, not disable the connector")
		}
		if !errors.Is(err, ghint.ErrAppWebhookMoved) {
			t.Fatalf("want ErrAppWebhookMoved, got %v", err)
		}
		for _, want := range []string{`connector "gh"`, "webhook.secret", "webhook.verify_signature", "conductor config migrate"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("error does not name %q: %v", want, err)
			}
		}
	}
}
