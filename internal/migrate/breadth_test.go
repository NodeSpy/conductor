package migrate

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/NodeSpy/paseo-conductor/internal/config"
)

// TestGithubAlertKindsTransform (D1): the security/deploy kinds map onto
// their own `on:` triggers with the work intact.
func TestGithubAlertKindsTransform(t *testing.T) {
	_, out := mustTransform(t, `
integrations:
  - type: github
    name: gh
    token: ${GH_PAT}
    webhook: { listen: ":8787" }
    rules:
      - match: { repos: ["acme/*"] }
        actions:
          deployment_status:
            type: agent
            agent: fixer
            prompt: "Investigate the failed deploy on {{.repo}}"
          dependabot_alert:
            type: agent
            agent: fixer
            prompt: "Assess the dependabot alert"
          secret_scanning_alert:
            type: command
            command: ["./rotate.sh", "{{.repo}}"]
agents:
  fixer: { provider: claude }
`)
	byOn := map[string]config.TriggerSpec{}
	for _, tr := range out.Triggers {
		byOn[tr.On] = tr
	}
	dep, ok := byOn["gh.deployment_status"]
	if !ok || !strings.Contains(dep.Steps[0].Prompt, "failed deploy") {
		t.Fatalf("deployment_status trigger missing/wrong: %+v", dep)
	}
	da, ok := byOn["gh.dependabot_alert"]
	if !ok || da.Steps[0].Agent != "fixer" {
		t.Fatalf("dependabot_alert trigger missing/wrong: %+v", da)
	}
	ssa, ok := byOn["gh.secret_scanning_alert"]
	if !ok || ssa.Steps[0].Type != "command" || ssa.Steps[0].Command[0] != "./rotate.sh" {
		t.Fatalf("secret_scanning_alert trigger missing/wrong: %+v", ssa)
	}
}

// TestGithubIssueFilterFieldsTransform (D2): labels_any / labels_all /
// authors / sole_assignee / assignee land on the migrated trigger's filters.
func TestGithubIssueFilterFieldsTransform(t *testing.T) {
	_, out := mustTransform(t, `
integrations:
  - type: github
    name: gh
    token: ${GH_PAT}
    webhook: { listen: ":8787" }
    rules:
      - match: { repos: ["acme/*"] }
        actions:
          issue_matched:
            type: agent
            agent: fixer
            labels_any: [bug, p1]
            labels_all: [triaged]
            authors: [alice]
            sole_assignee: true
            assignee: { logins: [octocat] }
            prompt: "Handle the issue"
agents:
  fixer: { provider: claude }
`)
	byOn := map[string]config.TriggerSpec{}
	for _, tr := range out.Triggers {
		byOn[tr.On] = tr
	}
	f := byOn["gh.issue_matched"].Filters
	if fmt.Sprint(f["labels_any"]) != "[bug p1]" || fmt.Sprint(f["labels_all"]) != "[triaged]" {
		t.Errorf("label filters lost: %v", f)
	}
	if fmt.Sprint(f["authors"]) != "[alice]" || f["sole_assignee"] != true {
		t.Errorf("authors/sole_assignee lost: %v", f)
	}
	if f["assignee"] == nil || !strings.Contains(fmt.Sprint(f["assignee"]), "octocat") {
		t.Errorf("assignee filter lost: %v", f)
	}
}

// TestDiscordHandoffTransform (D3): a legacy discord hand-off becomes an
// ask-capable discord connector with the connection carried over.
func TestDiscordHandoffTransform(t *testing.T) {
	_, out := mustTransform(t, `
integrations:
  - type: github
    name: gh
    token: ${GH_PAT}
    webhook: { listen: ":8787" }
    rules:
      - match: { repos: ["acme/*"] }
        actions:
          review_requested:
            type: agent
            agent: fixer
            background: true
            handoff: disc
            prompt: "Review it"
handoffs:
  disc:
    discord: { bot_token: ${DISCORD_TOKEN}, to: dm, user: "189" }
agents:
  fixer: { provider: claude }
`)
	ref, ok := out.ConnectorsMap["disc"]
	if !ok || ref.Type != "discord" {
		t.Fatalf("discord handoff should become a discord connector, got %+v", out.ConnectorsMap)
	}
	// The hand-off target maps onto the connector's default options, which
	// the ask verb (and background reviews) inherit.
	var conn struct {
		BotToken string `yaml:"bot_token"`
		Options  struct {
			To   string `yaml:"to"`
			User string `yaml:"user"`
		} `yaml:"options"`
	}
	if err := ref.Decode(&conn); err != nil {
		t.Fatal(err)
	}
	// mustTransform masks ${VARS}; the reference must still be there.
	if conn.Options.To != "dm" || conn.Options.User != "189" || conn.BotToken == "" {
		t.Fatalf("discord connection lost in transfer: %+v", conn)
	}
	// The step keeps addressing it.
	if got := out.Triggers[0].Steps[0].Handoff; got != "disc" {
		t.Fatalf("step handoff ref: %q", got)
	}
}

// TestNotifyDigestPushCarried (D4): notify sinks map onto via routes while
// push/digest/on stay on the notify block.
func TestNotifyDigestPushCarried(t *testing.T) {
	_, out := mustTransform(t, `
integrations:
  - type: cron
    name: c
    schedules:
      - name: s
        cron: "0 4 * * *"
        action: { type: command, command: [make, tidy] }
notify:
  on: [escalate, needs_input]
  push: true
  digest: 24h
  ntfy: { topic: ops }
`)
	if !out.Notify.Push {
		t.Error("notify.push lost")
	}
	if out.Notify.Digest.D() != 24*time.Hour {
		t.Errorf("notify.digest lost: %v", out.Notify.Digest)
	}
	if fmt.Sprint(out.Notify.On) != "[escalate needs_input]" {
		t.Errorf("notify.on lost: %v", out.Notify.On)
	}
	if len(out.Notify.Via) == 0 {
		t.Error("ntfy sink should map onto a via route")
	}
	if out.Notify.Ntfy.Topic != "" {
		t.Errorf("legacy ntfy sink field should be mapped away, got %+v", out.Notify.Ntfy)
	}
}

// TestConnectorIdentityRetryMeTransfer (D5): me / identity / retry on the
// legacy integration land on the migrated connector connection.
func TestConnectorIdentityRetryMeTransfer(t *testing.T) {
	_, out := mustTransform(t, legacyGithub)
	ref := out.ConnectorsMap["gh"]
	var conn struct {
		Me struct {
			Logins []string `yaml:"logins"`
		} `yaml:"me"`
		Identity struct {
			WriteToken string `yaml:"write_token"`
		} `yaml:"identity"`
		Retry struct {
			Max     int             `yaml:"max"`
			Backoff config.Duration `yaml:"backoff"`
		} `yaml:"retry"`
	}
	if err := ref.Decode(&conn); err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(conn.Me.Logins) != "[octocat]" {
		t.Errorf("me.logins lost: %v", conn.Me.Logins)
	}
	if conn.Identity.WriteToken != "gh_auth" {
		t.Errorf("identity.write_token lost: %q", conn.Identity.WriteToken)
	}
	if conn.Retry.Max != 2 || conn.Retry.Backoff.D() != 5*time.Second {
		t.Errorf("retry lost: %+v", conn.Retry)
	}
}

// TestDefaultHandoffStamped (D6): a background step that named no handoff
// gets the default entry's name stamped (legacy resolution made it implicit).
func TestDefaultHandoffStamped(t *testing.T) {
	_, out := mustTransform(t, `
integrations:
  - type: github
    name: gh
    token: ${GH_PAT}
    webhook: { listen: ":8787" }
    rules:
      - match: { repos: ["acme/*"] }
        actions:
          review_requested:
            type: agent
            agent: fixer
            background: true
            prompt: "Review it"
          new_comment:
            type: agent
            agent: fixer
            background: true
            handoff: page
            prompt: "Reply"
handoffs:
  review:
    slack: { to: dm, user: U1, bot_token: ${SLACK_BOT} }
    default: true
  page:
    web: { base_url: "https://c.example.com" }
agents:
  fixer: { provider: claude }
`)
	byOn := map[string]config.TriggerSpec{}
	for _, tr := range out.Triggers {
		byOn[tr.On] = tr
	}
	if got := byOn["gh.review_requested"].Steps[0].Handoff; got != "review" {
		t.Errorf("default handoff not stamped, got %q", got)
	}
	// An explicit handoff is left alone.
	if got := byOn["gh.new_comment"].Steps[0].Handoff; got != "page" {
		t.Errorf("explicit handoff overwritten, got %q", got)
	}
}

// TestFlatMultiStepActionTransform (D7): a legacy flat steps: action carries
// every per-step field through — id, type, prompt, output_schema, checkout,
// env, retry.
func TestFlatMultiStepActionTransform(t *testing.T) {
	_, out := mustTransform(t, `
integrations:
  - type: github
    name: gh
    token: ${GH_PAT}
    webhook: { listen: ":8787" }
    rules:
      - match: { repos: ["acme/*"] }
        actions:
          merge_conflict:
            steps:
              - id: plan
                type: agent
                agent: planner
                checkout: none
                env: { SCOPE: narrow }
                output_schema:
                  type: object
                  required: [approach]
                prompt: "Plan the fix"
              - id: apply
                type: command
                command: [make, fix]
                retry: { while_output_matches: "not ready", interval: 1s, timeout: 5s }
agents:
  planner: { provider: claude }
`)
	if len(out.Triggers) != 1 {
		t.Fatalf("triggers: %d", len(out.Triggers))
	}
	steps := out.Triggers[0].Steps
	if len(steps) != 2 {
		t.Fatalf("steps: %d, want 2", len(steps))
	}
	plan := steps[0]
	if plan.ID != "plan" || plan.Type != "agent" || plan.Agent != "planner" ||
		plan.Checkout != "none" || plan.Prompt != "Plan the fix" {
		t.Errorf("plan step fields lost: %+v", plan)
	}
	if plan.Env["SCOPE"] != "narrow" {
		t.Errorf("step env lost: %v", plan.Env)
	}
	if plan.OutputSchema == nil || fmt.Sprint(plan.OutputSchema["required"]) != "[approach]" {
		t.Errorf("output_schema lost: %v", plan.OutputSchema)
	}
	apply := steps[1]
	if apply.ID != "apply" || apply.Type != "command" || fmt.Sprint(apply.Command) != "[make fix]" {
		t.Errorf("apply step fields lost: %+v", apply)
	}
	if apply.Retry == nil || apply.Retry.WhileOutputMatches != "not ready" ||
		apply.Retry.Interval.D() != time.Second || apply.Retry.Timeout.D() != 5*time.Second {
		t.Errorf("step retry lost: %+v", apply.Retry)
	}
}
