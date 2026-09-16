package migrate

import (
	"strings"
	"testing"
)

// The webhook secret and the signature switch moved out of `app:` — they
// describe the receiver, not the App credential. `conductor config migrate`
// rewrites them to webhook.secret / webhook.verify_signature, and the runtime
// refuses the old location outright, so a config that does not get rewritten
// here dead-ends at the next boot.
const legacyGithubAppWebhook = `
integrations:
  - type: github
    name: gh
    app:
      app_id: 123
      private_key_path: /etc/conductor/github-app.pem
      webhook_secret: ${GH_WEBHOOK_SECRET}
      verify_signature: false
    webhook: { smee_url: https://smee.io/x }
    rules:
      - match: { repos: ["acme/*"] }
        actions:
          merge_conflict: { type: command, command: ["true"] }
`

// legacyGithubApplessWebhook is the shape the move exists for: no App at all,
// the webhook secret parked under `app:` because that was the only place it
// could go. After the migration `app:` has nothing left to hold.
const legacyGithubApplessWebhook = `
integrations:
  - type: github
    name: gh
    token: ${GH_PAT}
    app: { webhook_secret: ${GH_WEBHOOK_SECRET} }
    webhook: { listen: "127.0.0.1:8787" }
    rules:
      - match: { repos: ["acme/w"] }
        actions:
          merge_conflict: { type: command, command: ["true"] }
`

func TestMigrateAppWebhookKeysMoveToWebhook(t *testing.T) {
	res, _ := mustTransform(t, legacyGithubAppWebhook)
	out := string(res.Output)

	// The secret landed under webhook:, with its ${VAR} reference intact.
	if !strings.Contains(out, "secret: ${GH_WEBHOOK_SECRET}") {
		t.Errorf("webhook.secret missing from the migrated config:\n%s", out)
	}
	if !strings.Contains(out, "verify_signature: false") {
		t.Errorf("webhook.verify_signature missing from the migrated config:\n%s", out)
	}
	// ...and nothing is left in app: but App auth.
	if strings.Contains(out, "webhook_secret") {
		t.Errorf("app.webhook_secret survived the migration:\n%s", out)
	}
	for _, want := range []string{"app_id: 123", "private_key_path: /etc/conductor/github-app.pem"} {
		if !strings.Contains(out, want) {
			t.Errorf("App auth lost in the migration (want %q):\n%s", want, out)
		}
	}
	// The move is reported, not silent.
	var noted bool
	for _, s := range res.Summary {
		if strings.Contains(s, "app.webhook_secret/app.verify_signature → webhook.secret/webhook.verify_signature") {
			noted = true
		}
	}
	if !noted {
		t.Errorf("the move is not in the mapping summary: %v", res.Summary)
	}
}

// App-less: the whole `app:` block disappears, since the secret was the only
// thing in it. An absent app: block is exactly what App-less should look like.
func TestMigrateApplessAppBlockDropped(t *testing.T) {
	res, out := mustTransform(t, legacyGithubApplessWebhook)
	doc := string(res.Output)
	if !strings.Contains(doc, "secret: ${GH_WEBHOOK_SECRET}") {
		t.Errorf("webhook.secret missing:\n%s", doc)
	}
	if strings.Contains(doc, "app:") {
		t.Errorf("App-less config kept an app: block:\n%s", doc)
	}
	// And the result is a real, buildable connector — not just matching text.
	ref, ok := out.ConnectorsMap["gh"]
	if !ok {
		t.Fatalf("no gh connector in the migrated config")
	}
	var conn struct {
		Webhook struct {
			Listen string `yaml:"listen"`
			Secret string `yaml:"secret"`
		} `yaml:"webhook"`
	}
	if err := ref.Decode(&conn); err != nil {
		t.Fatal(err)
	}
	if conn.Webhook.Listen != "127.0.0.1:8787" || conn.Webhook.Secret == "" {
		t.Fatalf("webhook block did not survive: %+v", conn.Webhook)
	}
}
