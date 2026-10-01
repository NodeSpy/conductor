package flow

import (
	"strings"
	"testing"
)

// Load-time rules for slack form/shortcut triggers: a trigger that opens a
// form or answers a message shortcut must name who may use it (a non-empty
// users: filter on every match path) unless options.any_user opts out — and
// nothing that validated before this feature stops validating.

func slackFormCfg(trigger string) string {
	return `
connectors:
  slack: { use: slack, app_token: xapp, bot_token: xoxb }
triggers:
` + trigger
}

const slackFormBlock = `
    options:
      form:
        title: Hand off
        fields:
          - { name: repo, type: select, options: [acme/api, acme/web] }
          - { name: notes, type: textarea, optional: true }
    steps:
      - uses: slack.post
        options: { user: "{{.slack.user}}", text: "{{.slack.form.repo}} {{.slack.via}}" }
`

func TestSlackFormTriggerValidation(t *testing.T) {
	cases := []struct {
		name, trigger, wantErr string
	}{
		{"shortcut with users and form", `
  - on: slack.message_shortcut
    filter: { callback_id: conductor_handover, users: [U1] }` + slackFormBlock, ""},
		{"mention with users and form", `
  - on: slack.app_mention
    filter: { users: [U1, U2] }` + slackFormBlock, ""},
		{"users on every OR branch", `
  - on: slack.app_mention
    filter:
      - { users: [U1], channel: C1 }
      - { users: [U2] }` + slackFormBlock, ""},
		{"any_user opt-out", `
  - on: slack.message_shortcut
    filter: { callback_id: x }
    options: { any_user: true }
    steps: [{ uses: slack.post, options: { user: "{{.slack.user}}", text: hi } }]
`, ""},
		{"plain mention without users is unchanged", `
  - on: slack.app_mention
    steps: [{ uses: slack.post, options: { channel: "{{.slack.channel}}", text: hi } }]
`, ""},
		{"shortcut without form still needs users", `
  - on: slack.message_shortcut
    filter: { callback_id: x }
    steps: [{ uses: slack.post, options: { user: "{{.slack.user}}", text: hi } }]
`, "non-empty `users:` filter"},
		{"form without users", `
  - on: slack.app_mention` + slackFormBlock, "non-empty `users:` filter"},
		{"users on only one OR branch", `
  - on: slack.app_mention
    filter:
      - { users: [U1] }
      - { channel: C1 }` + slackFormBlock, "non-empty `users:` filter"},
		{"negated users does not count", `
  - on: slack.app_mention
    filter: { not_users: [U1] }` + slackFormBlock, "non-empty `users:` filter"},
		{"empty users list does not count", `
  - on: slack.app_mention
    filter: { users: [] }` + slackFormBlock, "non-empty `users:` filter"},
		{"bad form", `
  - on: slack.app_mention
    filter: { users: [U1] }
    options: { form: { fields: [{ name: repo, type: select }] } }
    steps: [{ uses: slack.post, options: { user: "{{.slack.user}}", text: hi } }]
`, "a select needs"},
		{"form on an event without forms", `
  - on: slack.reaction_added
    filter: { users: [U1] }` + slackFormBlock, "unknown key \"form\""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateFilterCfg(t, slackFormCfg(tc.trigger))
			switch {
			case tc.wantErr == "" && err != nil:
				t.Fatalf("want valid, got %v", err)
			case tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)):
				t.Fatalf("want error containing %q, got %v", tc.wantErr, err)
			}
		})
	}
}
