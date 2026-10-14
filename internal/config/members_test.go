package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
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
	if !strings.Contains(w, "jailed") || !strings.Contains(w, "no skill.verbs") {
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
