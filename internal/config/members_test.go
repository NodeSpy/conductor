package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const membersBase = `
connectors:
  gh: { use: github, token: x, repos: ["acme/*"] }
runtimes:
  paseo: { use: paseo, default: true }
  claude-cli: { use: cli, tool: claude-code }
operators:
  ops: { ssh_key: "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIExample ops@box", aliases: ["slack:U1"] }
`

func loadMembers(t *testing.T, body string) (*Config, error) {
	t.Helper()
	dir := t.TempDir()
	SetStateDir(filepath.Join(dir, "state"))
	t.Cleanup(func() { SetStateDir("") })
	p := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(p, []byte(membersBase+body), 0o600); err != nil {
		t.Fatal(err)
	}
	return Load(p)
}

func TestMembersAbsentIsANoOp(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "config.yaml")
	_ = os.WriteFile(p, []byte("connectors:\n  gh: { use: github, token: x, repos: [\"acme/*\"] }\n"), 0o600)
	c, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Members) != 0 || len(c.Triggers) != 0 {
		t.Fatalf("a config without members grew members/triggers: %d %d", len(c.Members), len(c.Triggers))
	}
}

func TestMemberExpandsWorkerTriggers(t *testing.T) {
	c, err := loadMembers(t, `
members:
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
		case MemberWorkerTrigger("sre"):
			worker = &c.Triggers[i]
		case MemberHandoffTrigger("sre"):
			handoff = &c.Triggers[i]
		}
	}
	if worker == nil || handoff == nil {
		t.Fatal("worker/handoff triggers not synthesized")
	}
	ws := worker.Steps[0]
	if !worker.Manual() || ws.Runtime != "claude-cli" || ws.Background || ws.BoundMember != "sre" || ws.BoundMemberRole != MemberRoleWorker {
		t.Fatalf("worker step: %+v", ws)
	}
	if ws.Skill == nil || len(ws.Skill.Verbs) != 1 || ws.Skill.Verbs[0] != "gh.comment" {
		t.Fatalf("worker grant must be exactly the member's: %+v", ws.Skill)
	}
	hs := handoff.Steps[0]
	if !hs.Background || hs.Runtime != "paseo" || hs.BoundMember != "sre" {
		t.Fatalf("handoff step: %+v", hs)
	}
	if !c.Members["sre"].Autonomy.ShadowMode() {
		t.Fatal("a member that says nothing must start in shadow mode")
	}
	if got := c.MemberRepoDir("sre"); !strings.HasSuffix(got, filepath.Join("members", "sre", "memory")) {
		t.Fatalf("default repo dir %s", got)
	}
	if ops := c.MemberOperators("sre"); len(ops) != 1 || ops[0] != "ops" {
		t.Fatalf("operators default: %v", ops)
	}
}

func TestBoundMemberCannotBeWrittenInYAML(t *testing.T) {
	_, err := loadMembers(t, `
triggers:
  - on: manual
    name: sneaky
    steps:
      - { type: agent, prompt: hi, boundmember: sre }
`)
	if err == nil {
		t.Fatal("an unknown step key decoded — the binding must not be writable")
	}
}

func TestMemberValidation(t *testing.T) {
	cases := map[string]string{
		"bad name": `
members:
  SRE!: {}`,
		"charter both": `
members:
  sre: { charter: a, charter_file: b.md }`,
		"missing charter file": `
members:
  sre: { charter_file: nope.md }`,
		"unknown operator": `
members:
  sre: { operators: [mallory] }`,
		"home not paseo": `
members:
  sre: { home: { runtime: claude-cli } }`,
		"worker on paseo": `
members:
  sre: { worker: { runtime: paseo } }`,
		"unknown tier": `
members:
  sre: { autonomy: { tiers: { yolo: [gh.*] } } }`,
		"money without cap": `
members:
  sre: { autonomy: { tiers: { commit_money: [gh.buy] }, amounts: { gh.buy: usd } } }`,
		"money without amounts": `
members:
  sre: { autonomy: { tiers: { commit_money: [gh.buy] }, spend_cap: { per_action: 1, per_period: 2 } } }`,
		"cap inverted": `
members:
  sre: { autonomy: { spend_cap: { per_action: 5, per_period: 2 } } }`,
		"cap zero": `
members:
  sre: { autonomy: { spend_cap: { per_action: 0, per_period: 2 } } }`,
		"bad operator key": `
operators:
  bad: { ssh_key: "not a key" }
members:
  sre: {}`,
		"member step unknown": `
members:
  sre: {}
triggers:
  - on: manual
    name: r
    steps: [ { member: { name: nobody } } ]`,
		"member step bad action": `
members:
  sre: {}
triggers:
  - on: manual
    name: r
    steps: [ { member: { name: sre, action: explode } } ]`,
		"member step rotate with text": `
members:
  sre: {}
triggers:
  - on: manual
    name: r
    steps: [ { member: { name: sre, action: rotate, text: hi } } ]`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := loadMembers(t, body); err == nil {
				t.Fatal("accepted")
			}
		})
	}
}

func TestMemberNeedsACLIRuntime(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "config.yaml")
	_ = os.WriteFile(p, []byte(`
connectors:
  gh: { use: github, token: x, repos: ["acme/*"] }
members:
  sre: {}
`), 0o600)
	_, err := Load(p)
	if err == nil || !strings.Contains(err.Error(), "use: cli") {
		t.Fatalf("want a clear 'add a cli runtime' error, got %v", err)
	}
}

func TestMemberStepAndWarnings(t *testing.T) {
	c, err := loadMembers(t, `
members:
  sre:
    autonomy: { shadow: false, promote: [gh.comment] }
triggers:
  - on: manual
    name: page
    steps: [ { member: { name: sre, title: "{{.title}}", urgent: true } } ]
`)
	if err != nil {
		t.Fatal(err)
	}
	if c.Members["sre"].Autonomy.ShadowMode() {
		t.Fatal("shadow: false ignored")
	}
	w := strings.Join(c.MemberWarnings(), "\n")
	if !strings.Contains(w, "sandbox") || !strings.Contains(w, "no skill.verbs") {
		t.Fatalf("warnings: %s", w)
	}
	var step Step
	for _, tr := range c.Triggers {
		if tr.Name == "page" {
			step = tr.Steps[0]
		}
	}
	if step.HelperForm() != HelperMember {
		t.Fatalf("member: step form = %q", step.HelperForm())
	}
}

func TestClassifyTierRank(t *testing.T) {
	if TierRank(TierNever) <= TierRank(TierCommitMoney) || TierRank(TierObserve) != 0 || TierRank("x") != -1 {
		t.Fatal("tier order")
	}
}

// A home session must rotate before its idle conductor token can expire.
func TestRotateAgeCappedByTheTokenLifetime(t *testing.T) {
	limit := HandoffSessionTTL - MemberRotateMargin
	for age, ok := range map[string]bool{"24h": true, limit.String(): true, (limit + time.Second).String(): false, HandoffSessionTTL.String(): false, "336h": false} {
		_, err := loadMembers(t, "\nmembers:\n  sre:\n    home: { rotate: { max_age: "+age+" } }\n")
		if ok && err != nil {
			t.Errorf("max_age %s rejected: %v", age, err)
		}
		if !ok && (err == nil || !strings.Contains(err.Error(), "token")) {
			t.Errorf("max_age %s: want a rejection explaining the token lifetime, got %v", age, err)
		}
	}
	if MemberRotateMargin < time.Hour || HandoffSessionTTL-MemberRotateMargin < DefaultMemberRotateAge {
		t.Fatal("the default rotation age must sit well inside the token lifetime")
	}
}

func TestMemberTerraformValidation(t *testing.T) {
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
members:
  sre:
    skill: { verbs: [tf-read.plan, gh.comment] }
    terraform: { plan_connector: tf-read, apply_connector: tf-write, repos: { infra: git@example.com:acme/infra.git } }
`
	c, err := load(good)
	if err != nil {
		t.Fatal(err)
	}
	r, err := c.MemberTerraformRole("tf-write")
	if err != nil || r.Bin() != "/usr/bin/tofu" || r.Env["AWS_PROFILE"] != "acme-write" {
		t.Fatalf("write role: %+v %v", r, err)
	}
	if got := c.Members["sre"].Terraform.StatefulTypes(); len(got) == 0 || got[0] != DefaultStatefulTypes[0] {
		t.Fatal("default stateful list not applied")
	}
	for name, body := range map[string]string{
		"no connectors":       "\nmembers:\n  sre:\n    terraform: { repos: { infra: x } }\n",
		"unknown connector":   "\nmembers:\n  sre:\n    terraform: { plan_connector: nope, apply_connector: tf-write, repos: { infra: x } }\n",
		"not terraform":       "\nmembers:\n  sre:\n    terraform: { plan_connector: gh, apply_connector: tf-write, repos: { infra: x } }\n",
		"no repos":            "\nmembers:\n  sre:\n    terraform: { plan_connector: tf-read, apply_connector: tf-write }\n",
		"grant reaches apply": "\nmembers:\n  sre:\n    skill: { verbs: [tf-write.apply] }\n    terraform: { plan_connector: tf-read, apply_connector: tf-write, repos: { infra: x } }\n",
		"apply by pattern":    "\nmembers:\n  sre:\n    skill: { verbs: [\"tf-*.plan\"] }\n    terraform: { plan_connector: tf-read, apply_connector: tf-write, repos: { infra: x } }\n",
		"wildcard grant":      "\nmembers:\n  sre:\n    skill: { verbs: [\"*\"] }\n    terraform: { plan_connector: tf-read, apply_connector: tf-write, repos: { infra: x } }\n",
		"bad repo name":       "\nmembers:\n  sre:\n    terraform: { plan_connector: tf-read, apply_connector: tf-write, repos: { Infra!: x } }\n",
		"bad pattern":         "\nmembers:\n  sre:\n    terraform: { plan_connector: tf-read, apply_connector: tf-write, repos: { infra: x }, stateful: [\"[\"] }\n",
	} {
		if _, err := load(base + body); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestMemberProactiveConfig(t *testing.T) {
	const ok = `
members:
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
	c, err := loadMembers(t, ok)
	if err != nil {
		t.Fatal(err)
	}
	var sweep *TriggerSpec
	for i := range c.Triggers {
		if c.Triggers[i].Name == MemberSweepTrigger("sre") {
			sweep = &c.Triggers[i]
		}
	}
	if sweep == nil || sweep.Steps[0].BoundMember != "sre" || sweep.Steps[0].WaitTimeout.D() != DefaultProactiveTimeout {
		t.Fatalf("sweep trigger: %+v", sweep)
	}
	if sweep.Policy == nil || sweep.Policy.Budget == nil || sweep.Policy.Budget.MaxCostUSD != 2.5 || sweep.Policy.Budget.WindowOrDefault() != 24*time.Hour {
		t.Fatalf("the sweep trigger must carry the daily cost budget: %+v", sweep.Policy)
	}
	cl, err := c.MemberChecklist("sre")
	if err != nil || cl != "- cost anomalies\n### detail\n- nested stays" {
		t.Fatalf("checklist section: %q %v", cl, err)
	}
	p := c.Members["sre"].Proactive
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
	// A member without proactive: gets no sweep trigger.
	c2, _ := loadMembers(t, "members:\n  sre: {}\n")
	for _, tr := range c2.Triggers {
		if tr.Name == MemberSweepTrigger("sre") {
			t.Fatal("a member that did not opt in got a sweep trigger")
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
		_, err := loadMembers(t, "members:\n  sre:\n    charter: \"## Proactive checks\\n- x\"\n    proactive:\n      "+body+"\n")
		if err == nil || !strings.Contains(err.Error(), "proactive") {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestMemberPeerLinks(t *testing.T) {
	c, err := loadMembers(t, `
members:
  a: { peers: [b] }
  b: { peers: [a] }
  c: { peers: [a] }
  d: {}
member_links:
  - [d, c]
`)
	if err != nil {
		t.Fatal(err)
	}
	for _, x := range []struct {
		a, b string
		want bool
	}{{"a", "b", true}, {"b", "a", true}, {"a", "c", false}, {"c", "a", false}, {"c", "d", true}, {"d", "c", true}, {"a", "a", false}, {"a", "zz", false}} {
		if got := c.MemberLinked(x.a, x.b); got != x.want {
			t.Errorf("linked(%s,%s) = %v", x.a, x.b, got)
		}
	}
	w := strings.Join(c.MemberWarnings(), "\n")
	if !strings.Contains(w, `members.c: peers lists "a", but a does not list c back`) {
		t.Fatalf("one-sided peers not warned: %s", w)
	}
	for name, body := range map[string]string{
		"self peer":    "members:\n  a: { peers: [a] }\n",
		"unknown peer": "members:\n  a: { peers: [zz] }\n",
		"bad link":     "members:\n  a: {}\nmember_links: [[a]]\n",
		"self link":    "members:\n  a: {}\nmember_links: [[a, a]]\n",
		"unknown link": "members:\n  a: {}\nmember_links: [[a, zz]]\n",
	} {
		if _, err := loadMembers(t, body); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
}
