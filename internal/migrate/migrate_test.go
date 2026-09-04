package migrate

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/NodeSpy/paseo-conductor/internal/config"
	"github.com/NodeSpy/paseo-conductor/internal/connector"
	"github.com/NodeSpy/paseo-conductor/internal/core"
	"github.com/NodeSpy/paseo-conductor/internal/flow"
	gh "github.com/NodeSpy/paseo-conductor/internal/integrations/github"
	"github.com/NodeSpy/paseo-conductor/internal/secrets"
)

// legacyGithub is a representative legacy config exercising the
// rules/defaults merge model, variants, filters, and steps.
const legacyGithub = `
integrations:
  - type: github
    name: gh
    token: ${GH_PAT}
    webhook: { listen: ":8787" }
    app: { webhook_secret: ${GH_WEBHOOK_SECRET} }
    identity: { write_token: gh_auth }
    retry: { max: 2, backoff: 5s }
    defaults:
      me: { logins: [octocat] }
      actions:
        merge_conflict:
          type: agent
          agent: fixer
          prompt: "Fix the conflict on {{.repo}}#{{.pr}}"
    rules:
      - match: { repos: ["acme/*"] }
        actions:
          merge_conflict: {}          # inherits the default variant
          new_comment:
            type: agent
            agent: fixer
            from_users: [alice]
            ignore_users: ["bot[bot]"]
            prompt: "Handle the comment"
          review_requested:
            - name: triage
              type: agent
              agent: planner
              reviewer: { logins: [octocat] }
              exclude: { branches: ["release/*"], labels: [hold], title: ["WIP"] }
              gates: { not_draft: true }
              prompt: "Review {{.repo}}#{{.pr}}"
            - name: full
              type: agent
              agent: fixer
              prompt: "Deep review"
          failing_checks:
            type: agent
            agent: fixer
            ignore_checks: [nightly]
            flaky_rerun: { enabled: true, max: 2 }
            max_attempts_per_head: 5
            prompt: "Fix CI"
      - match: { repos: ["acme/special"] }
        actions:
          merge_conflict:
            type: command
            command: ["gh", "pr", "view", "{{.pr}}"]
agents:
  fixer: { provider: claude }
  planner: { provider: claude }
`

func mustTransform(t *testing.T, raw string) (*Result, *config.Config) {
	t.Helper()
	res, err := Transform([]byte(raw))
	if err != nil {
		t.Fatalf("Transform: %v", err)
	}
	if !res.Changed {
		t.Fatalf("expected a legacy config to change")
	}
	// The output must parse as a config (with env refs masked, mirroring the
	// production load which expands them first).
	var out config.Config
	if err := yaml.Unmarshal(maskEnv(res.Output), &out); err != nil {
		t.Fatalf("migrated config does not parse: %v\n%s", err, res.Output)
	}
	return res, &out
}

func TestGithubTransformShape(t *testing.T) {
	res, out := mustTransform(t, legacyGithub)
	if len(out.ConnectorsMap) != 1 {
		t.Fatalf("connectors: %d, want 1", len(out.ConnectorsMap))
	}
	if out.ConnectorsMap["gh"].Type != "github" {
		t.Fatalf("gh connector type: %q", out.ConnectorsMap["gh"].Type)
	}
	// rule1: merge_conflict + new_comment + review_requested×2 + failing_checks = 5
	// rule2: merge_conflict = 1
	if len(out.Triggers) != 6 {
		for _, tr := range out.Triggers {
			t.Logf("trigger on=%s name=%s filters=%v", tr.On, tr.Name, tr.Filters)
		}
		t.Fatalf("triggers: %d, want 6", len(out.Triggers))
	}
	byOn := map[string][]config.TriggerSpec{}
	for _, tr := range out.Triggers {
		byOn[tr.On] = append(byOn[tr.On], tr)
	}
	// The inherited default variant carried the defaults' prompt through the
	// merge.
	mc := byOn["gh.merge_conflict"]
	if len(mc) != 2 {
		t.Fatalf("merge_conflict triggers: %d", len(mc))
	}
	if got := mc[0].Steps[0].Prompt; !strings.Contains(got, "Fix the conflict") {
		t.Errorf("inherited default prompt lost: %q", got)
	}
	// The glob rule's triggers exclude the more specific rule's repo.
	if got := fmt.Sprint(mc[0].Filters["exclude_repos"]); !strings.Contains(got, "acme/special") {
		t.Errorf("exclude_repos missing: %v", mc[0].Filters)
	}
	// The specific rule's trigger has no exclusions.
	if _, has := mc[1].Filters["exclude_repos"]; has {
		t.Errorf("specific rule should not exclude: %v", mc[1].Filters)
	}
	if mc[1].Steps[0].Type != "command" {
		t.Errorf("specific rule step type: %q", mc[1].Steps[0].Type)
	}
	// Variant names survive (dedup keys stay kind#variant).
	rr := byOn["gh.review_requested"]
	if len(rr) != 2 || rr[0].Name != "triage" || rr[1].Name != "full" {
		t.Fatalf("review_requested variants: %+v", rr)
	}
	// Filters mapped.
	f := rr[0].Filters
	if fmt.Sprint(f["reviewer"]) == "" || f["gates"] == nil || f["exclude"] == nil {
		t.Errorf("triage filters incomplete: %v", f)
	}
	nc := byOn["gh.new_comment"][0].Filters
	if fmt.Sprint(nc["from_users"]) != "[alice]" || fmt.Sprint(nc["ignore_users"]) != "[bot[bot]]" {
		t.Errorf("comment filters: %v", nc)
	}
	fc := byOn["gh.failing_checks"][0]
	if fmt.Sprint(fc.Filters["ignore_checks"]) != "[nightly]" {
		t.Errorf("ignore_checks: %v", fc.Filters)
	}
	if fc.Options["max_attempts_per_head"] != 5 {
		t.Errorf("options: %v", fc.Options)
	}
	// ${VAR} references survive verbatim.
	if !strings.Contains(string(res.Output), "${GH_PAT}") || !strings.Contains(string(res.Output), "${GH_WEBHOOK_SECRET}") {
		t.Errorf("env references were not preserved:\n%s", res.Output)
	}
	if strings.Contains(string(res.Output), "__CONDUCTOR_ENV__") {
		t.Errorf("masking leaked into output")
	}
}

// TestGithubBehavioralEquivalence is the golden proof: the SAME webhook
// events, translated by the legacy integration built from the legacy config
// and by the lowered integration built from the MIGRATED config, produce the
// same triggers firing the same work.
func TestGithubBehavioralEquivalence(t *testing.T) {
	res, out := mustTransform(t, legacyGithub)
	_ = res

	// Legacy integration straight from the legacy YAML.
	var legacy config.Config
	if err := yaml.Unmarshal(maskEnv([]byte(legacyGithub)), &legacy); err != nil {
		t.Fatal(err)
	}
	legacyIG, err := core.Build("github", "gh", legacy.Integrations[0].Decode)
	if err != nil {
		t.Fatal(err)
	}

	// Migrated integration through the connector lowering.
	sec := secrets.New()
	sec.LookupEnv = func(string) (string, bool) { return "tok", true }
	reg, err := connector.Build(out, connector.Deps{Secrets: sec, Config: out})
	if err != nil {
		t.Fatal(err)
	}
	if err := flow.Validate(out, reg); err != nil {
		t.Fatalf("migrated config fails semantic validation: %v", err)
	}
	in, _ := reg.Get("gh")
	var compiled []connector.CompiledTrigger
	for i, spec := range out.Triggers {
		compiled = append(compiled, connector.CompiledTrigger{Index: i, Spec: spec})
	}
	migratedIG, err := in.Impl.Source(compiled)
	if err != nil {
		t.Fatal(err)
	}

	type fired struct {
		Kind, Variant, Dedup, Work string
	}
	summarize := func(trs []core.Trigger, migrated bool) []fired {
		var out []fired
		for _, tr := range trs {
			act, _ := tr.Action.(config.Action)
			work := ""
			if migrated {
				// The migrated action's work lives in the referenced spec.
				var idx int
				fmt.Sscanf(act.FlowRef, "%d:", &idx)
				st := compiled[idx].Spec.Steps[0]
				work = st.Type + "|" + st.Agent + "|" + st.Prompt + "|" + strings.Join(st.Command, " ")
			} else {
				work = act.Type + "|" + act.Agent + "|" + act.Prompt + "|" + strings.Join(act.Command, " ")
			}
			out = append(out, fired{tr.Kind, tr.Variant, tr.Dedup, work})
		}
		sort.Slice(out, func(i, j int) bool {
			return fmt.Sprint(out[i]) < fmt.Sprint(out[j])
		})
		return out
	}

	events := []struct {
		name, event string
		payload     map[string]any
	}{
		{
			"review_requested on matching repo",
			"pull_request",
			map[string]any{
				"action": "review_requested",
				"repository": map[string]any{
					"full_name": "acme/widgets",
					"owner":     map[string]any{"login": "acme"}, "name": "widgets",
				},
				"pull_request": map[string]any{
					"number": 7, "draft": false, "title": "Add thing",
					"user":                map[string]any{"login": "someone"},
					"head":                map[string]any{"sha": "abc123", "ref": "feat"},
					"base":                map[string]any{"ref": "main"},
					"html_url":            "http://x/pr/7",
					"requested_reviewers": []any{map[string]any{"login": "octocat"}},
				},
				"requested_reviewer": map[string]any{"login": "octocat"},
			},
		},
		{
			"review_requested on excluded branch",
			"pull_request",
			map[string]any{
				"action": "review_requested",
				"repository": map[string]any{
					"full_name": "acme/widgets",
					"owner":     map[string]any{"login": "acme"}, "name": "widgets",
				},
				"pull_request": map[string]any{
					"number": 8, "draft": false, "title": "hotfix",
					"user":                map[string]any{"login": "someone"},
					"head":                map[string]any{"sha": "def456", "ref": "release/1.2"},
					"base":                map[string]any{"ref": "main"},
					"html_url":            "http://x/pr/8",
					"requested_reviewers": []any{map[string]any{"login": "octocat"}},
				},
				"requested_reviewer": map[string]any{"login": "octocat"},
			},
		},
		{
			"comment from allowed user",
			"issue_comment",
			map[string]any{
				"action": "created",
				"repository": map[string]any{
					"full_name": "acme/widgets",
					"owner":     map[string]any{"login": "acme"}, "name": "widgets",
				},
				"issue": map[string]any{
					"number": 7, "title": "Add thing",
					"pull_request": map[string]any{"url": "http://x"},
				},
				"comment": map[string]any{
					"id": 111, "body": "please fix",
					"user": map[string]any{"login": "alice"},
				},
			},
		},
		{
			"comment from ignored user",
			"issue_comment",
			map[string]any{
				"action": "created",
				"repository": map[string]any{
					"full_name": "acme/widgets",
					"owner":     map[string]any{"login": "acme"}, "name": "widgets",
				},
				"issue": map[string]any{
					"number": 7, "title": "Add thing",
					"pull_request": map[string]any{"url": "http://x"},
				},
				"comment": map[string]any{
					"id": 112, "body": "bot noise",
					"user": map[string]any{"login": "bot[bot]"},
				},
			},
		},
		{
			"repo outside every rule",
			"pull_request",
			map[string]any{
				"action": "review_requested",
				"repository": map[string]any{
					"full_name": "other/repo",
					"owner":     map[string]any{"login": "other"}, "name": "repo",
				},
				"pull_request": map[string]any{
					"number": 9, "draft": false, "title": "x",
					"user":                map[string]any{"login": "someone"},
					"head":                map[string]any{"sha": "aaa", "ref": "b"},
					"base":                map[string]any{"ref": "main"},
					"html_url":            "http://x/pr/9",
					"requested_reviewers": []any{map[string]any{"login": "octocat"}},
				},
				"requested_reviewer": map[string]any{"login": "octocat"},
			},
		},
	}

	type translator interface {
		Translate(ctx context.Context, eventType string, body []byte) []core.Trigger
	}
	lt := legacyIG.(translator)
	mt := migratedIG.(translator)
	for _, ev := range events {
		body, _ := json.Marshal(ev.payload)
		got := summarize(mt.Translate(context.Background(), ev.event, body), true)
		want := summarize(lt.Translate(context.Background(), ev.event, body), false)
		if len(got) != len(want) {
			t.Fatalf("%s: legacy fired %d trigger(s), migrated fired %d\nlegacy: %+v\nmigrated: %+v",
				ev.name, len(want), len(got), want, got)
		}
		for i := range got {
			if got[i].Kind != want[i].Kind || got[i].Variant != want[i].Variant || got[i].Dedup != want[i].Dedup {
				t.Errorf("%s[%d]: identity mismatch\nlegacy:   %+v\nmigrated: %+v", ev.name, i, want[i], got[i])
			}
			// The work must be the same modulo prompt guidance suffixes the
			// engine appends at dispatch (identical for both paths).
			if got[i].Work != want[i].Work {
				t.Errorf("%s[%d]: work mismatch\nlegacy:   %s\nmigrated: %s", ev.name, i, want[i].Work, got[i].Work)
			}
		}
	}
}

const legacyKitchen = `
integrations:
  - type: slack
    name: ops
    app_token: ${SLACK_APP_TOKEN}
    bot_token: ${SLACK_BOT_TOKEN}
    triggers:
      - on: app_mention
        ack: { react: eyes }
        on_done: { react: white_check_mark, say: "done!", in_thread: true }
        on_fail: { say: "failed", ephemeral: true }
        actions: { type: agent, agent: fixer, prompt: "Do {{.slack.text}}" }
      - on: reaction_added
        reaction: rocket
        actions: { type: command, command: ["echo", "hi"] }
      - on: slash_command
        command: /deploy
        actions: { type: agent, agent: fixer, prompt: "deploy" }
  - type: cron
    name: chores
    schedules:
      - name: nightly
        cron: "0 2 * * *"
        run_on_start: true
        action: { type: command, command: ["make", "tidy"] }
  - type: webhook
    name: hooks
    listen: ":8099"
    sources:
      - name: alarm
        path: /hooks/alarm
        sign: { header: X-Sig, secret: ${HOOK_SECRET}, scheme: sha256 }
        match: '{{if eq .body.state "ALARM"}}true{{end}}'
        title: "{{.body.name}}"
        dedup: "{{.body.id}}"
        repo: acme/infra
        actions: { type: agent, agent: fixer, prompt: "investigate {{.body.name}}" }
  - type: sentry
    name: errors
    listen: ":8098"
    client_secret: ${SENTRY_SECRET}
    rules:
      - match: { projects: [backend], levels: [error, fatal] }
        repo: acme/backend
        actions: { type: agent, agent: fixer, prompt: "fix {{.sentry.title}}" }
      - match: {}
        actions: { type: command, command: ["echo", "{{.sentry.title}}"] }
  - type: pagerduty
    name: oncall
    listen: ":8097"
    signing_secret: ${PD_SECRET}
    rules:
      - match: { event_types: [incident.triggered], urgencies: [high] }
        actions: { type: agent, agent: fixer, prompt: "mitigate {{.pagerduty.title}}" }
  - type: rss
    name: upstream
    feeds:
      - name: changelog
        url: https://example.com/feed.xml
        interval: 45m
        match: "(?i)security"
        actions: { type: agent, agent: fixer, prompt: "read {{.item.link}}" }
handoffs:
  review:
    slack: { to: dm, user: U123, bot_token: ${SLACK_BOT_TOKEN} }
    default: true
  page:
    web: { base_url: "https://c.example.com", listen: ":8099", ttl: 45m,
           tunnel: { provider: cloudflared } }
controllers:
  deck: { type: agent-deck }
  gem:  { agent: gemini, default: true }
control:
  enabled: true
  pause_label: "conductor:hold"
  shadow: false
  max_concurrent_agents: 5
  max_agents_per_hour: 40
paseo_bin: /usr/local/bin/paseo
agents:
  fixer: { provider: claude, controller: gem }
notify:
  on: [escalate]
  slack_webhook_url: ${NOTIFY_HOOK}
store:
  state_file: /tmp/state.json
update:
  auto: true
`

func TestKitchenSinkTransform(t *testing.T) {
	res, out := mustTransform(t, legacyKitchen)

	// Every integration + both handoffs became connectors.
	wantConns := []string{"ops", "chores", "hooks", "errors", "oncall", "upstream", "review", "page"}
	for _, n := range wantConns {
		if _, ok := out.ConnectorsMap[n]; !ok {
			t.Errorf("missing connector %q", n)
		}
	}
	if out.ConnectorsMap["review"].Type != "slack" || out.ConnectorsMap["page"].Type != "web" {
		t.Errorf("handoff connector types: review=%s page=%s",
			out.ConnectorsMap["review"].Type, out.ConnectorsMap["page"].Type)
	}
	// Handoff target rides as default options.
	if to := out.ConnectorsMap["review"].Options["to"]; to != "dm" {
		t.Errorf("review handoff options: %v", out.ConnectorsMap["review"].Options)
	}

	// Slack rules → triggers with hooks on the first.
	var mention *config.TriggerSpec
	for i := range out.Triggers {
		if out.Triggers[i].On == "ops.app_mention" {
			mention = &out.Triggers[i]
		}
	}
	if mention == nil {
		t.Fatal("no ops.app_mention trigger")
	}
	if len(mention.Hooks) != 4 { // ack react, done react, done say, fail say
		t.Fatalf("mention hooks: %+v", mention.Hooks)
	}
	phases := map[string]int{}
	for _, h := range mention.Hooks {
		phases[h.At]++
		if !strings.HasPrefix(h.Uses, "ops.") {
			t.Errorf("hook uses %q, want ops.*", h.Uses)
		}
	}
	if phases["start"] != 1 || phases["done"] != 2 || phases["fail"] != 1 {
		t.Errorf("hook phases: %v", phases)
	}

	// Runtimes from controllers + paseo_bin.
	if len(out.Runtimes) != 3 {
		t.Fatalf("runtimes: %+v", out.Runtimes)
	}
	if out.Runtimes["paseo"].Bin != "/usr/local/bin/paseo" {
		t.Errorf("paseo bin: %+v", out.Runtimes["paseo"])
	}
	if !out.Runtimes["gem"].Default {
		t.Errorf("gem default lost")
	}

	// control → policy.
	if out.Policy == nil || out.Policy.PauseLabel == nil || *out.Policy.PauseLabel != "conductor:hold" {
		t.Fatalf("policy: %+v", out.Policy)
	}
	if out.Policy.Concurrency == nil || *out.Policy.Concurrency.MaxAgents != 5 || *out.Policy.Concurrency.MaxAgentsPerHour != 40 {
		t.Errorf("policy concurrency: %+v", out.Policy.Concurrency)
	}

	// Carried blocks intact.
	if out.Notify.SlackWebhookURL == "" || out.Store.StateFile != "/tmp/state.json" || !out.Update.Auto {
		t.Errorf("carried blocks lost: notify=%v store=%v update=%v", out.Notify, out.Store, out.Update)
	}
	if _, ok := out.Agents["fixer"]; !ok {
		t.Errorf("agents block lost")
	}
	// Legacy keys gone.
	if len(out.Integrations) != 0 || len(out.Handoffs) != 0 || len(out.Controllers) != 0 || out.PaseoBin != "" {
		t.Errorf("legacy keys not dropped: integrations=%d handoffs=%d controllers=%d paseo_bin=%q",
			len(out.Integrations), len(out.Handoffs), len(out.Controllers), out.PaseoBin)
	}

	// The migrated config passes the FULL semantic validation.
	sec := secrets.New()
	sec.LookupEnv = func(string) (string, bool) { return "resolved", true }
	reg, err := connector.Build(out, connector.Deps{Secrets: sec, Config: out})
	if err != nil {
		t.Fatal(err)
	}
	if err := flow.Validate(out, reg); err != nil {
		t.Fatalf("migrated kitchen-sink config fails validation: %v", err)
	}

	// Second transform of the output is a no-op (idempotent).
	res2, err := Transform(res.Output)
	if err != nil {
		t.Fatal(err)
	}
	if res2.Changed {
		t.Fatal("transform of a migrated config must be a no-op")
	}
}

// TestSentryPrecedencePreservedViaExcludes: connectors-model triggers are
// independent, so the migration reproduces legacy first-match-wins by giving
// each later trigger an exclude of every earlier rule's match. The golden
// proof drives BOTH triggers' filters against the same event contexts through
// the flow-side evaluator: an event the first rule matched fires ONLY the
// first trigger; an event only the second rule matched fires only the second.
func TestSentryPrecedencePreservedViaExcludes(t *testing.T) {
	_, out := mustTransform(t, legacyKitchen)
	var sentryTriggers []config.TriggerSpec
	for _, tr := range out.Triggers {
		if tr.Connector() == "errors" {
			sentryTriggers = append(sentryTriggers, tr)
		}
	}
	if len(sentryTriggers) != 2 {
		t.Fatalf("sentry triggers: %d, want 2", len(sentryTriggers))
	}
	first, second := sentryTriggers[0], sentryTriggers[1]
	if !strings.Contains(fmt.Sprint(first.Filters["projects"]), "backend") {
		t.Fatalf("first trigger should be the backend rule: %v", first.Filters)
	}
	if _, hasEx := first.Filters["exclude"]; hasEx {
		t.Fatal("the first trigger must not exclude anything")
	}
	if _, hasEx := second.Filters["exclude"]; !hasEx {
		t.Fatalf("the second trigger must exclude the first rule's match: %v", second.Filters)
	}

	sec := secrets.New()
	sec.LookupEnv = func(string) (string, bool) { return "x", true }
	reg, err := connector.Build(out, connector.Deps{Secrets: sec, Config: out})
	if err != nil {
		t.Fatal(err)
	}
	runner := flow.New(flow.Runner{Cfg: out, Conns: reg})
	evalBoth := func(sctx map[string]any) (bool, bool) {
		trig := core.Trigger{Kind: "sentry_alert", Context: map[string]any{"sentry": sctx}}
		m1, err1 := runner.FilterMatch(trig, first)
		m2, err2 := runner.FilterMatch(trig, second)
		if err1 != nil || err2 != nil {
			t.Fatalf("filter errors: %v %v", err1, err2)
		}
		return m1, m2
	}

	// A backend error: legacy rule1 won; only trigger1 may fire.
	m1, m2 := evalBoth(map[string]any{"project": "backend", "level": "error"})
	if !m1 || m2 {
		t.Fatalf("backend error: trigger1=%v trigger2=%v (want true,false)", m1, m2)
	}
	// A frontend warning: legacy fell through to rule2 (catch-all).
	m1, m2 = evalBoth(map[string]any{"project": "frontend", "level": "warning"})
	if m1 || !m2 {
		t.Fatalf("frontend warning: trigger1=%v trigger2=%v (want false,true)", m1, m2)
	}
	// A backend warning: rule1 requires level error|fatal → rule2 won.
	m1, m2 = evalBoth(map[string]any{"project": "backend", "level": "warning"})
	if m1 || !m2 {
		t.Fatalf("backend warning: trigger1=%v trigger2=%v (want false,true)", m1, m2)
	}
}

func TestUnmappableConstructsHardError(t *testing.T) {
	cases := []struct {
		name, yaml, wantErr string
	}{
		{
			"unknown integration type",
			"integrations:\n  - {type: gitlab, name: gl}\n",
			`unknown type "gitlab"`,
		},
		{
			"nested steps",
			`
integrations:
  - type: cron
    name: c
    schedules:
      - name: s
        cron: "* * * * *"
        action:
          type: agent
          agent: a
          steps:
            - type: agent
              agent: a
              steps: [{type: command, command: [x]}]
`,
			"nested steps",
		},
		{
			"typeless action",
			`
integrations:
  - type: cron
    name: c
    schedules:
      - name: s
        cron: "* * * * *"
        action: { agent: a, prompt: hi }
`,
			"no type",
		},
		{
			"rule without repos",
			`
integrations:
  - type: github
    name: gh
    rules:
      - actions: { merge_conflict: { type: agent, agent: a } }
`,
			"no match.repos",
		},
		{
			"mixed schema",
			"integrations:\n  - {type: cron, name: c, schedules: [{name: s, cron: '* * * * *', action: {type: command, command: [x]}}]}\nconnectors:\n  x: {type: slack}\n",
			"finish the migration by hand",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Transform([]byte(tc.yaml))
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("want error containing %q, got %v", tc.wantErr, err)
			}
		})
	}
}

func TestInertFieldsNotedNeverSilent(t *testing.T) {
	res, _ := mustTransform(t, `
integrations:
  - type: github
    name: gh
    rules:
      - match: { repos: ["a/b"] }
        workspace: custom
        actions:
          merge_conflict: { type: agent, agent: a, prompt: p, method: squash, project: { status: Ready } }
`)
	joined := strings.Join(res.Summary, "\n")
	for _, want := range []string{"workspace", "method", "project"} {
		if !strings.Contains(joined, want) {
			t.Errorf("summary missing inert-field note for %q:\n%s", want, joined)
		}
	}
}

func TestNoLegacyMeansNoChange(t *testing.T) {
	res, err := Transform([]byte("connectors:\n  gh: {type: github}\ntriggers:\n  - on: gh.release\n    steps: [{type: command, command: [x]}]\n"))
	if err != nil {
		t.Fatal(err)
	}
	if res.Changed {
		t.Fatal("connectors-only config must be a no-op")
	}
}

func TestExampleConfigTransforms(t *testing.T) {
	raw, err := os.ReadFile("../../config.example.legacy.yaml")
	if os.IsNotExist(err) {
		raw, err = os.ReadFile("../../config.example.yaml")
	}
	if err != nil {
		t.Skipf("example config not found: %v", err)
	}
	res, err := Transform(raw)
	if err != nil {
		t.Fatalf("the shipped example config must transform cleanly: %v", err)
	}
	if !res.Changed {
		t.Skip("example config already on the connectors schema")
	}
	var out config.Config
	if err := yaml.Unmarshal(maskEnv(res.Output), &out); err != nil {
		t.Fatalf("migrated example config does not parse: %v", err)
	}
	if len(out.ConnectorsMap) == 0 || len(out.Triggers) == 0 {
		t.Fatalf("example transform produced %d connectors / %d triggers", len(out.ConnectorsMap), len(out.Triggers))
	}
	sec := secrets.New()
	sec.LookupEnv = func(string) (string, bool) { return "resolved", true }
	reg, err := connector.Build(&out, connector.Deps{Secrets: sec, Config: &out})
	if err != nil {
		t.Fatal(err)
	}
	if err := flow.Validate(&out, reg); err != nil {
		t.Fatalf("migrated example config fails semantic validation: %v", err)
	}
}

func TestAutoMigrateFailSafe(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	legacy := []byte(legacyKitchen)
	if err := os.WriteFile(path, legacy, 0o600); err != nil {
		t.Fatal(err)
	}

	// Validation failure: the original must be restored, the backup kept, and
	// the error must say "manual migration".
	calls := 0
	_, _, err := AutoMigrate(path, func() error { calls++; return fmt.Errorf("boom") }, nil)
	if err == nil || !strings.Contains(err.Error(), "manual migration") {
		t.Fatalf("want manual-migration error, got %v", err)
	}
	if calls != 1 {
		t.Fatalf("validate calls: %d", calls)
	}
	restored, _ := os.ReadFile(path)
	if string(restored) != string(legacy) {
		t.Fatal("original config was not restored after validation failure")
	}
	if _, err := os.Stat(path + BackupSuffix); err != nil {
		t.Fatal("backup missing after failed migration")
	}

	// Successful migration: swapped, backed up, idempotent.
	n, summary, err := AutoMigrate(path, func() error { return nil }, nil)
	if err != nil || n != 1 {
		t.Fatalf("migrate: n=%d err=%v", n, err)
	}
	if len(summary) == 0 {
		t.Fatal("no summary")
	}
	migrated, _ := os.ReadFile(path)
	if !strings.Contains(string(migrated), "connectors:") {
		t.Fatal("config not migrated")
	}
	backup, _ := os.ReadFile(path + BackupSuffix)
	if string(backup) != string(legacy) {
		t.Fatal("backup does not hold the original")
	}
	n2, _, err := AutoMigrate(path, func() error { return nil }, nil)
	if err != nil || n2 != 0 {
		t.Fatalf("second run must be a no-op: n=%d err=%v", n2, err)
	}
}

func TestAutoMigrateImports(t *testing.T) {
	dir := t.TempDir()
	main := filepath.Join(dir, "config.yaml")
	sub := filepath.Join(dir, "cron.yaml")
	os.WriteFile(main, []byte("imports: [cron.yaml]\nagents:\n  a: {provider: claude}\n"), 0o600)
	os.WriteFile(sub, []byte(`
integrations:
  - type: cron
    name: c
    schedules:
      - name: s
        cron: "* * * * *"
        action: { type: command, command: [x] }
`), 0o600)
	n, _, err := AutoMigrate(main, func() error { return nil }, nil)
	if err != nil || n != 1 {
		t.Fatalf("n=%d err=%v", n, err)
	}
	migrated, _ := os.ReadFile(sub)
	if !strings.Contains(string(migrated), "connectors:") {
		t.Fatalf("imported file not migrated:\n%s", migrated)
	}
	if _, err := os.Stat(sub + BackupSuffix); err != nil {
		t.Fatal("imported file backup missing")
	}
}

// TestGithubExclusionSemantics proves the exclude_repos computation replicates
// resolve()'s most-specific-wins for representative pattern shapes.
func TestGithubExclusionSemantics(t *testing.T) {
	rules := []gh.Rule{
		{Match: gh.Match{Repos: []string{"acme/*"}}},
		{Match: gh.Match{Repos: []string{"acme/special"}}},
		{Match: gh.Match{Repos: []string{"*/*"}}},
		{Match: gh.Match{Repos: []string{"acme/*"}}}, // duplicate pattern, later rule
	}
	// Rule 0 (acme/*): excludes acme/special (more specific literal). The
	// broader */* does not win, and the duplicate later acme/* loses ties.
	got := ruleExclusions(rules, 0)
	if len(got) != 1 || got[0] != "acme/special" {
		t.Errorf("rule0 exclusions: %v", got)
	}
	// Rule 2 (*/*): excludes both more-literal patterns.
	got = ruleExclusions(rules, 2)
	sort.Strings(got)
	if fmt.Sprint(got) != "[acme/* acme/special]" {
		t.Errorf("rule2 exclusions: %v", got)
	}
	// Rule 3 (duplicate acme/*): loses the tie to rule 0 → excludes it, plus
	// the literal.
	got = ruleExclusions(rules, 3)
	sort.Strings(got)
	if fmt.Sprint(got) != "[acme/* acme/special]" {
		t.Errorf("rule3 exclusions: %v", got)
	}
}
