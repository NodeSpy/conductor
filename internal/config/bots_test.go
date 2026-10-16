package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const botsBase = `
connectors:
  gh: { use: github, token: x, repos: ["acme/*"] }
runtimes:
  paseo: { use: paseo, default: true }
  claude-cli: { use: cli, tool: claude-code }
operators:
  ops: { ssh_key: "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIExample ops@box", aliases: ["slack:U1"] }
`

func loadBots(t *testing.T, body string) (*Config, error) {
	t.Helper()
	dir := t.TempDir()
	SetStateDir(filepath.Join(dir, "state"))
	t.Cleanup(func() { SetStateDir("") })
	p := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(p, []byte(botsBase+body), 0o600); err != nil {
		t.Fatal(err)
	}
	return Load(p)
}

func TestBotsAbsentIsANoOp(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "config.yaml")
	_ = os.WriteFile(p, []byte("connectors:\n  gh: { use: github, token: x, repos: [\"acme/*\"] }\n"), 0o600)
	c, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Bots) != 0 || len(c.Triggers) != 0 {
		t.Fatalf("a config without bots grew bots/triggers: %d %d", len(c.Bots), len(c.Triggers))
	}
}

func TestBotExpandsWorkerTriggers(t *testing.T) {
	c, err := loadBots(t, `
bots:
  sre:
    description: SRE
    skill: { verbs: [gh.comment] }
`)
	if err != nil {
		t.Fatal(err)
	}
	var worker, handoff *TriggerSpec
	for i := range c.Triggers {
		switch c.Triggers[i].Name {
		case BotWorkerTrigger("sre"):
			worker = &c.Triggers[i]
		case BotHandoffTrigger("sre"):
			handoff = &c.Triggers[i]
		}
	}
	if worker == nil || handoff == nil {
		t.Fatal("worker/handoff triggers not synthesized")
	}
	ws := worker.Steps[0]
	if !worker.Manual() || ws.Runtime != "claude-cli" || ws.Background || ws.BoundBot != "sre" || ws.BoundBotRole != BotRoleWorker {
		t.Fatalf("worker step: %+v", ws)
	}
	if ws.Skill == nil || len(ws.Skill.Verbs) != 1 || ws.Skill.Verbs[0] != "gh.comment" {
		t.Fatalf("worker grant must be exactly the bot's: %+v", ws.Skill)
	}
	hs := handoff.Steps[0]
	if !hs.Background || hs.Runtime != "paseo" || hs.BoundBot != "sre" {
		t.Fatalf("handoff step: %+v", hs)
	}
	if !c.Bots["sre"].Autonomy.ShadowMode() {
		t.Fatal("a bot that says nothing must start in shadow mode")
	}
	if got := c.BotRepoDir("sre"); !strings.HasSuffix(got, filepath.Join("bots", "sre", "memory")) {
		t.Fatalf("default repo dir %s", got)
	}
	if ops := c.BotOperators("sre"); len(ops) != 1 || ops[0] != "ops" {
		t.Fatalf("operators default: %v", ops)
	}
}

func TestBoundBotCannotBeWrittenInYAML(t *testing.T) {
	_, err := loadBots(t, `
triggers:
  - on: manual
    name: sneaky
    steps:
      - { type: agent, prompt: hi, boundbot: sre }
`)
	if err == nil {
		t.Fatal("an unknown step key decoded — the binding must not be writable")
	}
}

func TestBotValidation(t *testing.T) {
	cases := map[string]string{
		"bad name": `
bots:
  SRE!: {}`,
		"charter both": `
bots:
  sre: { charter: a, charter_file: b.md }`,
		"missing charter file": `
bots:
  sre: { charter_file: nope.md }`,
		"unknown operator": `
bots:
  sre: { operators: [mallory] }`,
		"home not paseo": `
bots:
  sre: { home: { runtime: claude-cli } }`,
		"worker on paseo": `
bots:
  sre: { worker: { runtime: paseo } }`,
		"unknown tier": `
bots:
  sre: { autonomy: { tiers: { yolo: [gh.*] } } }`,
		"money without cap": `
bots:
  sre: { autonomy: { tiers: { commit_money: [gh.buy] }, amounts: { gh.buy: usd } } }`,
		"money without amounts": `
bots:
  sre: { autonomy: { tiers: { commit_money: [gh.buy] }, spend_cap: { per_action: 1, per_period: 2 } } }`,
		"cap inverted": `
bots:
  sre: { autonomy: { spend_cap: { per_action: 5, per_period: 2 } } }`,
		"cap zero": `
bots:
  sre: { autonomy: { spend_cap: { per_action: 0, per_period: 2 } } }`,
		"bad operator key": `
operators:
  bad: { ssh_key: "not a key" }
bots:
  sre: {}`,
		"bot step unknown": `
bots:
  sre: {}
triggers:
  - on: manual
    name: r
    steps: [ { bot: { name: nobody } } ]`,
		"bot step bad action": `
bots:
  sre: {}
triggers:
  - on: manual
    name: r
    steps: [ { bot: { name: sre, action: explode } } ]`,
		"bot step rotate with text": `
bots:
  sre: {}
triggers:
  - on: manual
    name: r
    steps: [ { bot: { name: sre, action: rotate, text: hi } } ]`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := loadBots(t, body); err == nil {
				t.Fatal("accepted")
			}
		})
	}
}

func TestBotNeedsACLIRuntime(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "config.yaml")
	_ = os.WriteFile(p, []byte(`
connectors:
  gh: { use: github, token: x, repos: ["acme/*"] }
bots:
  sre: {}
`), 0o600)
	_, err := Load(p)
	if err == nil || !strings.Contains(err.Error(), "use: cli") {
		t.Fatalf("want a clear 'add a cli runtime' error, got %v", err)
	}
}

func TestBotStepAndWarnings(t *testing.T) {
	c, err := loadBots(t, `
bots:
  sre:
    autonomy: { shadow: false, promote: [gh.comment] }
triggers:
  - on: manual
    name: page
    steps: [ { bot: { name: sre, title: "{{.title}}", urgent: true } } ]
`)
	if err != nil {
		t.Fatal(err)
	}
	if c.Bots["sre"].Autonomy.ShadowMode() {
		t.Fatal("shadow: false ignored")
	}
	w := strings.Join(c.BotWarnings(), "\n")
	if !strings.Contains(w, "sandbox") || !strings.Contains(w, "no skill.verbs") {
		t.Fatalf("warnings: %s", w)
	}
	var step Step
	for _, tr := range c.Triggers {
		if tr.Name == "page" {
			step = tr.Steps[0]
		}
	}
	if step.HelperForm() != HelperBot {
		t.Fatalf("bot: step form = %q", step.HelperForm())
	}
}

func TestClassifyTierRank(t *testing.T) {
	if TierRank(TierNever) <= TierRank(TierCommitMoney) || TierRank(TierObserve) != 0 || TierRank("x") != -1 {
		t.Fatal("tier order")
	}
}

// A home session must rotate before its idle conductor token can expire.
func TestRotateAgeCappedByTheTokenLifetime(t *testing.T) {
	limit := HandoffSessionTTL - BotRotateMargin
	for age, ok := range map[string]bool{"24h": true, limit.String(): true, (limit + time.Second).String(): false, HandoffSessionTTL.String(): false, "336h": false} {
		_, err := loadBots(t, "\nbots:\n  sre:\n    home: { rotate: { max_age: "+age+" } }\n")
		if ok && err != nil {
			t.Errorf("max_age %s rejected: %v", age, err)
		}
		if !ok && (err == nil || !strings.Contains(err.Error(), "token")) {
			t.Errorf("max_age %s: want a rejection explaining the token lifetime, got %v", age, err)
		}
	}
	if BotRotateMargin < time.Hour || HandoffSessionTTL-BotRotateMargin < DefaultBotRotateAge {
		t.Fatal("the default rotation age must sit well inside the token lifetime")
	}
}

func TestBotTerraformValidation(t *testing.T) {
	base := `
connectors:
  gh: { use: github, token: x, repos: ["acme/*"] }
  tf-read:  { use: terraform, env: { AWS_PROFILE: acme-read } }
  tf-write: { use: terraform, env: { AWS_PROFILE: acme-write }, binary: /usr/bin/tofu }
runtimes:
  paseo: { use: paseo, default: true }
  claude-cli: { use: cli, tool: claude-code }
`
	load := func(body string) (*Config, error) {
		dir := t.TempDir()
		SetStateDir(filepath.Join(dir, "state"))
		t.Cleanup(func() { SetStateDir("") })
		p := filepath.Join(dir, "config.yaml")
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		return Load(p)
	}
	good := base + `
bots:
  sre:
    skill: { verbs: [tf-read.plan, gh.comment] }
    terraform: { plan_connector: tf-read, apply_connector: tf-write, repos: { infra: git@example.com:acme/infra.git } }
`
	c, err := load(good)
	if err != nil {
		t.Fatal(err)
	}
	r, err := c.BotTerraformRole("tf-write")
	if err != nil || r.Bin() != "/usr/bin/tofu" || r.Env["AWS_PROFILE"] != "acme-write" {
		t.Fatalf("write role: %+v %v", r, err)
	}
	if got := c.Bots["sre"].Terraform.StatefulTypes(); len(got) == 0 || got[0] != DefaultStatefulTypes[0] {
		t.Fatal("default stateful list not applied")
	}
	for name, body := range map[string]string{
		"no connectors":       "\nbots:\n  sre:\n    terraform: { repos: { infra: x } }\n",
		"unknown connector":   "\nbots:\n  sre:\n    terraform: { plan_connector: nope, apply_connector: tf-write, repos: { infra: x } }\n",
		"not terraform":       "\nbots:\n  sre:\n    terraform: { plan_connector: gh, apply_connector: tf-write, repos: { infra: x } }\n",
		"no repos":            "\nbots:\n  sre:\n    terraform: { plan_connector: tf-read, apply_connector: tf-write }\n",
		"grant reaches apply": "\nbots:\n  sre:\n    skill: { verbs: [tf-write.apply] }\n    terraform: { plan_connector: tf-read, apply_connector: tf-write, repos: { infra: x } }\n",
		"apply by pattern":    "\nbots:\n  sre:\n    skill: { verbs: [\"tf-*.plan\"] }\n    terraform: { plan_connector: tf-read, apply_connector: tf-write, repos: { infra: x } }\n",
		"wildcard grant":      "\nbots:\n  sre:\n    skill: { verbs: [\"*\"] }\n    terraform: { plan_connector: tf-read, apply_connector: tf-write, repos: { infra: x } }\n",
		"bad repo name":       "\nbots:\n  sre:\n    terraform: { plan_connector: tf-read, apply_connector: tf-write, repos: { Infra!: x } }\n",
		"bad pattern":         "\nbots:\n  sre:\n    terraform: { plan_connector: tf-read, apply_connector: tf-write, repos: { infra: x }, stateful: [\"[\"] }\n",
	} {
		if _, err := load(base + body); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestBotProactiveConfig(t *testing.T) {
	const ok = `
bots:
  sre:
    charter: |
      Keep it up.
      ## Proactive checks
      - cost anomalies
      ### detail
      - nested stays
      ## Never
      - nothing
    proactive:
      every: 4h
      quiet_hours: "22:00-07:00"
      timezone: UTC
      budget: { max_runs_per_day: 6, max_cost_per_day: 2.5 }
      checklist: { section: "Proactive checks" }
`
	c, err := loadBots(t, ok)
	if err != nil {
		t.Fatal(err)
	}
	var sweep *TriggerSpec
	for i := range c.Triggers {
		if c.Triggers[i].Name == BotSweepTrigger("sre") {
			sweep = &c.Triggers[i]
		}
	}
	if sweep == nil || sweep.Steps[0].BoundBot != "sre" || sweep.Steps[0].WaitTimeout.D() != DefaultProactiveTimeout {
		t.Fatalf("sweep trigger: %+v", sweep)
	}
	if sweep.Policy == nil || sweep.Policy.Budget == nil || sweep.Policy.Budget.MaxCostUSD != 2.5 || sweep.Policy.Budget.WindowOrDefault() != 24*time.Hour {
		t.Fatalf("the sweep trigger must carry the daily cost budget: %+v", sweep.Policy)
	}
	cl, err := c.BotChecklist("sre")
	if err != nil || cl != "- cost anomalies\n### detail\n- nested stays" {
		t.Fatalf("checklist section: %q %v", cl, err)
	}
	p := c.Bots["sre"].Proactive
	for at, want := range map[string]bool{"23:30": true, "03:00": true, "07:00": false, "12:00": false, "22:00": true} {
		tm, _ := time.Parse("15:04", at)
		tm = time.Date(2026, 10, 14, tm.Hour(), tm.Minute(), 0, 0, time.UTC)
		if p.Quiet(tm) != want {
			t.Errorf("quiet at %s = %v", at, !want)
		}
	}
	if p.NotifyLevel() != "high" {
		t.Fatal("default notify_at must be high")
	}
	// A bot without proactive: gets no sweep trigger.
	c2, _ := loadBots(t, "bots:\n  sre: {}\n")
	for _, tr := range c2.Triggers {
		if tr.Name == BotSweepTrigger("sre") {
			t.Fatal("a bot that did not opt in got a sweep trigger")
		}
	}
	for name, body := range map[string]string{
		"no budget":       "every: 4h\n      checklist: { section: Proactive checks }",
		"too frequent":    "every: 1m\n      budget: { max_runs_per_day: 1 }\n      checklist: { section: Proactive checks }",
		"no checklist":    "every: 4h\n      budget: { max_runs_per_day: 1 }",
		"both checklists": "every: 4h\n      budget: { max_runs_per_day: 1 }\n      checklist: { section: Proactive checks, file: x.md }",
		"missing section": "every: 4h\n      budget: { max_runs_per_day: 1 }\n      checklist: { section: Nope }",
		"missing file":    "every: 4h\n      budget: { max_runs_per_day: 1 }\n      checklist: { file: nope.md }",
		"bad quiet":       "every: 4h\n      quiet_hours: 25-7\n      budget: { max_runs_per_day: 1 }\n      checklist: { section: Proactive checks }",
		"bad notify":      "every: 4h\n      notify_at: loud\n      budget: { max_runs_per_day: 1 }\n      checklist: { section: Proactive checks }",
		"bad zone":        "every: 4h\n      timezone: Mars/Base\n      budget: { max_runs_per_day: 1 }\n      checklist: { section: Proactive checks }",
	} {
		_, err := loadBots(t, "bots:\n  sre:\n    charter: \"## Proactive checks\\n- x\"\n    proactive:\n      "+body+"\n")
		if err == nil || !strings.Contains(err.Error(), "proactive") {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestBotPeerLinks(t *testing.T) {
	c, err := loadBots(t, `
bots:
  a: { peers: [b] }
  b: { peers: [a] }
  c: { peers: [a] }
  d: {}
bot_links:
  - [d, c]
`)
	if err != nil {
		t.Fatal(err)
	}
	for _, x := range []struct {
		a, b string
		want bool
	}{{"a", "b", true}, {"b", "a", true}, {"a", "c", false}, {"c", "a", false}, {"c", "d", true}, {"d", "c", true}, {"a", "a", false}, {"a", "zz", false}} {
		if got := c.BotLinked(x.a, x.b); got != x.want {
			t.Errorf("linked(%s,%s) = %v", x.a, x.b, got)
		}
	}
	w := strings.Join(c.BotWarnings(), "\n")
	if !strings.Contains(w, `bots.c: peers lists "a", but a does not list c back`) {
		t.Fatalf("one-sided peers not warned: %s", w)
	}
	for name, body := range map[string]string{
		"self peer":    "bots:\n  a: { peers: [a] }\n",
		"unknown peer": "bots:\n  a: { peers: [zz] }\n",
		"bad link":     "bots:\n  a: {}\nbot_links: [[a]]\n",
		"self link":    "bots:\n  a: {}\nbot_links: [[a, a]]\n",
		"unknown link": "bots:\n  a: {}\nbot_links: [[a, zz]]\n",
	} {
		if _, err := loadBots(t, body); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
}
