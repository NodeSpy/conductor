package config

import (
	"fmt"
	"strings"
	"testing"
)

// teamsOps continues botsBase's operators: block (which defines ops).
const teamsOps = `  oncall: { ssh_key: "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIExample2 oncall@box" }
  billing-ops: { ssh_key: "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIExample3 b@box" }
`

// loadTeams loads botsBase with extra operators and body.
func loadTeams(t *testing.T, body string) (*Config, error) {
	t.Helper()
	return loadBots(t, teamsOps+"vaults:\n  house: { type: file, dir: /nonexistent/vault }\n"+body)
}

func TestTeamsAbsentChangeNothing(t *testing.T) {
	c, err := loadBots(t, "bots:\n  a: {}\n  b: {}\n")
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Teams) != 0 || c.Bots["a"].Team != "" {
		t.Fatalf("teams appeared from nowhere: %+v", c.Teams)
	}
	if c.BotLinked("a", "b") {
		t.Fatal("two teamless bots are linked")
	}
	if got := c.BotOperators("a"); len(got) != 1 || got[0] != "ops" {
		t.Fatalf("a teamless bot's operators changed: %v", got)
	}
}

func TestTeamMembershipIsOneTeam(t *testing.T) {
	for name, body := range map[string]string{
		"unknown team": "bots:\n  a: { team: nope }\n",
		"two teams":    "teams:\n  x: {}\n  y: {}\nbots:\n  a: { team: [x, y] }\n",
		"team twice":   "teams:\n  x: {}\n  y: {}\nbots:\n  a:\n    team: x\n    team: y\n",
	} {
		_, err := loadTeams(t, body)
		if err == nil {
			t.Errorf("%s accepted", name)
			continue
		}
		t.Logf("%s: %v", name, err)
	}
	_, err := loadTeams(t, "teams:\n  x: {}\nbots:\n  a: { team: [x, x] }\n")
	if err == nil || !strings.Contains(err.Error(), "at most one team") {
		t.Fatalf("a team list is not refused as two teams: %v", err)
	}
}

// Every ceiling: a bot setting past its team's is a load error naming
// both values; at the ceiling (or under it) it loads.
func TestTeamCeilingsNarrowNeverWiden(t *testing.T) {
	cases := []struct {
		name, team, bot string
		want            []string // substrings of the error ("" bot loads)
	}{
		{"operators outside", "operators: [ops]", "operators: [oncall]", []string{`"oncall"`, "team t's operators [ops]"}},
		{"operators inside", "operators: [ops, oncall]", "operators: [oncall]", nil},
		{"promote above observe ceiling", "autonomy: { ceiling: observe }", "autonomy: { promote: [pd.ack], tiers: { reversible: [pd.ack] } }",
			[]string{"autonomy.promote [pd.ack]", "reversible", "team t's autonomy.ceiling observe"}},
		{"promote under reversible ceiling", "autonomy: { ceiling: reversible }", "autonomy: { promote: [pd.ack], tiers: { reversible: [pd.ack] } }", nil},
		{"shadow off under a shadow team", "autonomy: { shadow: true }", "autonomy: { shadow: false }", []string{"autonomy.shadow false", "team t's autonomy.shadow true"}},
		{"shadow on under a shadow team", "autonomy: { shadow: true }", "autonomy: { shadow: true }", nil},
		{"tier less strict", "autonomy: { tiers: { propose: [kv.*] } }", "autonomy: { tiers: { observe: [kv.set] } }",
			[]string{`autonomy.tiers.observe "kv.set"`, `team t's autonomy.tiers.propose "kv.*"`}},
		{"tier as strict", "autonomy: { tiers: { propose: [kv.*] } }", "autonomy: { tiers: { never: [kv.set] } }", nil},
		{"spend per_action", "budget: { spend_cap: { per_action: 100, per_period: 500 } }", "autonomy: { spend_cap: { per_action: 200, per_period: 500 } }",
			[]string{"per_action 200", "per_action 100"}},
		{"spend per_period", "budget: { spend_cap: { per_action: 100, per_period: 500 } }", "autonomy: { spend_cap: { per_action: 100, per_period: 600 } }",
			[]string{"per_period 600", "per_period 500"}},
		{"spend shorter period", "budget: { spend_cap: { per_action: 100, per_period: 500, period: 720h } }", "autonomy: { spend_cap: { per_action: 100, per_period: 500, period: 24h } }",
			[]string{"period 24h0m0s", "period 720h0m0s"}},
		{"spend currency", "budget: { spend_cap: { per_action: 100, per_period: 500 } }", "autonomy: { spend_cap: { per_action: 1, per_period: 5, currency: EUR } }",
			[]string{"currency EUR", "currency USD"}},
		{"spend inside", "budget: { spend_cap: { per_action: 100, per_period: 500, period: 24h } }", "autonomy: { spend_cap: { per_action: 50, per_period: 500, period: 720h } }", nil},
		{"sweep runs over the pool", "budget: { proactive: { max_runs_per_day: 4 } }",
			"charter: \"## Checks\\nlook\", proactive: { every: 1h, checklist: { section: Checks }, budget: { max_runs_per_day: 5 } }",
			[]string{"max_runs_per_day 5", "max_runs_per_day 4"}},
		{"sweep cost over the pool", "budget: { proactive: { max_cost_per_day: 2 } }",
			"charter: \"## Checks\\nlook\", proactive: { every: 1h, checklist: { section: Checks }, budget: { max_cost_per_day: 3 } }",
			[]string{"max_cost_per_day 3", "max_cost_per_day 2"}},
		{"sweep inside the pool", "budget: { proactive: { max_runs_per_day: 4 } }",
			"charter: \"## Checks\\nlook\", proactive: { every: 1h, checklist: { section: Checks }, budget: { max_runs_per_day: 4 } }", nil},
		{"sandbox optional under required", "isolation: { sandbox: required }", "isolation: { sandbox: optional }", []string{"isolation.sandbox optional", "isolation.sandbox required"}},
		{"handoff on under off", "isolation: { handoff_workers: false }", "isolation: { handoff_workers: true }", []string{"handoff_workers true", "handoff_workers false"}},
		{"prompt skip on under off", "isolation: { skip_permission_prompts: false }", "isolation: { skip_permission_prompts: true }", []string{"skip_permission_prompts true", "skip_permission_prompts false"}},
		{"bypass home mode under no-skip", "isolation: { skip_permission_prompts: false }", "home: { mode: bypassPermissions }", []string{`home.mode "bypassPermissions"`, "skip_permission_prompts is false (team t)"}},
		{"secrets env over broker", "credentials: { secrets_via: broker, allow_secrets: [house/pd] }", "skill: { verbs: [x.y], secrets_via: env }", []string{"secrets_via env", "secrets_via broker"}},
		{"secrets any over none", "credentials: { secrets_via: none }", "skill: { verbs: [x.y], secrets_via: broker }", []string{"secrets_via broker", "secrets_via none"}},
		{"secret outside", "credentials: { secrets_via: broker, allow_secrets: [house/pd] }", "skill: { verbs: [x.y], allow_secrets: [house/aws] }", []string{`allow_secrets "house/aws"`, "allow_secrets [house/pd]"}},
		{"secret inside", "credentials: { secrets_via: broker, allow_secrets: [house/pd, house/aws] }", "skill: { verbs: [x.y], secrets_via: broker, allow_secrets: [house/aws] }", nil},
		{"login outside", "browser: { logins: [app.example.com] }", "browser: { logins: [admin.example.com] }", []string{`browser.logins "admin.example.com"`, "browser.logins [app.example.com]"}},
		{"login under a domain", "browser: { logins: [\"*.example.com\"] }", "browser: { logins: [app.example.com, \"*.eu.example.com\"] }", nil},
		{"domain wider than a host", "browser: { logins: [app.example.com] }", "browser: { logins: [\"*.example.com\"] }", []string{`"*.example.com"`}},
		{"host outside a domain", "browser: { logins: [\"*.example.com\"] }", "browser: { logins: [app.example.org] }", []string{`"app.example.org"`, `browser.logins [*.example.com]`}},
		{"browser credential outside", "browser: { credentials: [app] }", "browser: { credentials: [admin] }", []string{`browser.credentials "admin"`, "browser.credentials [app]"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := "teams:\n  t: { " + tc.team + " }\nbots:\n  m: { team: t, " + tc.bot + " }\n"
			_, err := loadTeams(t, body)
			if tc.want == nil {
				if err != nil {
					t.Fatalf("a bot at or under its team's ceiling is refused: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("a bot past its team's ceiling loaded:\n%s", body)
			}
			for _, w := range tc.want {
				if !strings.Contains(err.Error(), w) {
					t.Errorf("error %q does not name %q", err, w)
				}
			}
			if !strings.Contains(err.Error(), "bots.m") {
				t.Errorf("error %q does not name the bot", err)
			}
		})
	}
}

// What a bot leaves unset, it inherits; what it sets narrower, it keeps.
func TestTeamDefaultsInherited(t *testing.T) {
	c, err := loadTeams(t, `
teams:
  platform:
    operators: [ops, oncall]
    autonomy: { shadow: false, tiers: { never: [aws.iam_*] } }
    budget:
      spend_cap: { per_action: 100, per_period: 500 }
      proactive: { max_runs_per_day: 6, max_cost_per_day: 2 }
    isolation: { sandbox: required, handoff_workers: false, skip_permission_prompts: false }
    credentials: { secrets_via: broker, allow_secrets: [house/pd, house/aws] }
    browser: { logins: [app.example.com], credentials: [app] }
bots:
  inherit:
    team: platform
    charter: "## Checks\nlook"
    proactive: { every: 1h, checklist: { section: Checks } }
  narrow:
    team: platform
    operators: [oncall]
    skill: { verbs: [pd.get], allow_secrets: [house/pd] }
    autonomy: { shadow: true, spend_cap: { per_action: 10, per_period: 50 }, tiers: { observe: [pd.get] } }
`)
	if err != nil {
		t.Fatal(err)
	}
	in := c.Bots["inherit"]
	if got := c.BotOperators("inherit"); strings.Join(got, ",") != "ops,oncall" {
		t.Errorf("operators not inherited: %v", got)
	}
	if in.Autonomy.ShadowMode() {
		t.Error("shadow: false not inherited")
	}
	if !hasString(in.Autonomy.Tiers[TierNever], "aws.iam_*") {
		t.Errorf("team tiers not merged: %v", in.Autonomy.Tiers)
	}
	if in.Autonomy.SpendCap == nil || in.Autonomy.SpendCap.PerAction != 100 {
		t.Errorf("spend cap not inherited: %+v", in.Autonomy.SpendCap)
	}
	if b := in.Proactive.Budget; b.MaxRunsPerDay != 6 || b.MaxCostPerDay != 2 {
		t.Errorf("proactive budget not inherited from the pool: %+v", b)
	}
	if !in.Isolation.SandboxRequired() || in.Isolation.HandoffAllowed() || in.Isolation.PromptSkipAllowed() {
		t.Errorf("isolation not inherited: %+v", in.Isolation)
	}
	if in.Skill == nil || in.Skill.SecretsVia != "broker" || strings.Join(in.Skill.AllowSecrets, ",") != "house/pd,house/aws" {
		t.Errorf("credentials not inherited: %+v", in.Skill)
	}
	if in.Browser == nil || strings.Join(in.Browser.Logins, ",") != "app.example.com" {
		t.Errorf("browser scope not inherited: %+v", in.Browser)
	}
	nw := c.Bots["narrow"]
	if got := c.BotOperators("narrow"); strings.Join(got, ",") != "oncall" {
		t.Errorf("narrowed operators widened: %v", got)
	}
	if !nw.Autonomy.ShadowMode() || nw.Autonomy.SpendCap.PerAction != 10 || strings.Join(nw.Skill.AllowSecrets, ",") != "house/pd" {
		t.Errorf("narrowed settings widened: %+v %+v", nw.Autonomy, nw.Skill)
	}
	// The team's never tier binds a bot that classified nothing itself,
	// and a verb the bot made observe stays as strict as the team says.
	if !hasString(nw.Autonomy.Tiers[TierNever], "aws.iam_*") || !hasString(nw.Autonomy.Tiers[TierObserve], "pd.get") {
		t.Errorf("tiers not merged: %v", nw.Autonomy.Tiers)
	}
	// Inheriting is idempotent: resolving again changes nothing and errs on
	// nothing.
	if err := c.ResolveTeams(); err != nil {
		t.Fatalf("resolving twice: %v", err)
	}
	if err := c.Validate(); err != nil {
		t.Fatalf("validate after resolve: %v", err)
	}
	// The worker triggers carry the inherited grant.
	for _, tr := range c.Triggers {
		if tr.Name == BotWorkerTrigger("inherit") && (tr.Steps[0].Skill == nil || tr.Steps[0].Skill.SecretsVia != "broker") {
			t.Errorf("worker trigger did not get the inherited credentials: %+v", tr.Steps[0].Skill)
		}
	}
	// A team's tier does not alias into the bot's map.
	c.Bots["inherit"].Autonomy.Tiers[TierNever][0] = "changed"
	if c.Teams["platform"].Autonomy.Tiers[TierNever][0] != "aws.iam_*" {
		t.Error("a bot's inherited tiers alias its team's")
	}
}

// Each half of ResolveTeams/validateTeams refuses a widening on its own: a
// Config assembled in code reaches either one first.
func TestTeamCeilingChecksStandAlone(t *testing.T) {
	widen := func() *Config {
		return &Config{
			Operators: map[string]OperatorConfig{"ops": {}, "other": {}},
			Teams:     map[string]TeamConfig{"t": {Operators: []string{"ops"}}},
			Bots:      map[string]BotConfig{"m": {Team: "t", Operators: []string{"other"}}},
		}
	}
	if err := widen().ResolveTeams(); err == nil || !strings.Contains(err.Error(), `"other" is not one of team t's operators [ops]`) {
		t.Fatalf("ResolveTeams let a widening through: %v", err)
	}
	if err := widen().validateTeams(); err == nil || !strings.Contains(err.Error(), `"other" is not one of team t's operators [ops]`) {
		t.Fatalf("validateTeams let a widening through: %v", err)
	}
	ok := widen()
	ok.Bots["m"] = BotConfig{Team: "t"}
	if err := ok.ResolveTeams(); err != nil {
		t.Fatal(err)
	}
	if err := ok.validateTeams(); err != nil || strings.Join(ok.Bots["m"].Operators, ",") != "ops" {
		t.Fatalf("an inheriting bot: %v %v", err, ok.Bots["m"].Operators)
	}
}

// Inheritance copies: two bots sharing one tier slice (a Config built in
// code) do not write each other's team tiers into it.
func TestTeamInheritanceDoesNotAlias(t *testing.T) {
	shared := make([]string, 1, 8)
	shared[0] = "kv.get"
	c := &Config{
		Teams: map[string]TeamConfig{
			"x": {Autonomy: TeamAutonomy{Tiers: map[string][]string{TierObserve: {"x.only"}}}},
			"y": {Autonomy: TeamAutonomy{Tiers: map[string][]string{TierObserve: {"y.only"}}}},
		},
		Bots: map[string]BotConfig{
			"a": {Team: "x", Autonomy: BotAutonomy{Tiers: map[string][]string{TierObserve: shared}}},
			"b": {Team: "y", Autonomy: BotAutonomy{Tiers: map[string][]string{TierObserve: shared}}},
		},
	}
	if err := c.ResolveTeams(); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(c.Bots["a"].Autonomy.Tiers[TierObserve], ","); got != "kv.get,x.only" {
		t.Errorf("a's tiers: %s", got)
	}
	if got := strings.Join(c.Bots["b"].Autonomy.Tiers[TierObserve], ","); got != "kv.get,y.only" {
		t.Errorf("b's tiers: %s", got)
	}
}

func TestTeamPeering(t *testing.T) {
	c, err := loadTeams(t, `
teams:
  platform: {}
  billing: {}
  quiet: { peers: none }
bots:
  a: { team: platform }
  b: { team: platform }
  c: { team: billing }
  d: { team: quiet }
  e: { team: quiet }
  f: { team: quiet }
  g: {}
bot_links:
  - [d, e]
  - [a, c]
`)
	if err != nil {
		t.Fatal(err)
	}
	for _, x := range []struct {
		a, b string
		want bool
	}{
		{"a", "b", true}, {"b", "a", true}, // teammates peer by default
		{"b", "c", false}, {"c", "b", false}, // across teams: isolated
		{"a", "c", true},                     // …unless the operator links them
		{"d", "f", false}, {"e", "f", false}, // peers: none
		{"d", "e", true},                     // an explicit link still counts
		{"a", "g", false}, {"g", "a", false}, // a teamless bot
		{"a", "a", false},
	} {
		if got := c.BotLinked(x.a, x.b); got != x.want {
			t.Errorf("linked(%s,%s) = %v, want %v", x.a, x.b, got, x.want)
		}
	}
	if _, err := loadTeams(t, "teams:\n  x: { peers: all }\n"); err == nil {
		t.Error("peers: all accepted")
	}
}

func TestTeamValidation(t *testing.T) {
	ok := `
teams:
  platform:
    operators: [ops]
    knowledge: { promotion: direct }
    routing:
      rules: [ { name: billing, match: { service: "billing-*", source: "pd.*" }, to: b } ]
      lead: a
      fallback: b
bots:
  a: { team: platform }
  b: { team: platform }
  c: {}
triggers:
  - on: manual
    name: page
    steps: [ { bot: { team: platform, title: "x" } } ]
`
	c, err := loadTeams(t, ok)
	if err != nil {
		t.Fatal(err)
	}
	if got := c.TeamBots("platform"); strings.Join(got, ",") != "a,b" {
		t.Errorf("team's bots: %v", got)
	}
	if !strings.HasSuffix(c.TeamKnowledgeDir("platform"), "teams/platform/knowledge") || c.TeamKnowledgeDir("nope") != "" {
		t.Errorf("knowledge dir: %q", c.TeamKnowledgeDir("platform"))
	}
	if got := c.TeamOperators("platform"); strings.Join(got, ",") != "ops" {
		t.Errorf("team operators: %v", got)
	}
	for name, edit := range map[string][2]string{
		"unknown operator":    {"operators: [ops]", "operators: [nobody]"},
		"bad promotion":       {"promotion: direct", "promotion: always"},
		"lead outside":        {"lead: a", "lead: c"},
		"unknown lead":        {"lead: a", "lead: zz"},
		"fallback outside":    {"fallback: b", "fallback: c"},
		"no fallback":         {"      fallback: b\n", ""},
		"rule outside":        {"to: b }", "to: c }"},
		"empty match":         {`match: { service: "billing-*", source: "pd.*" }`, "match: {}"},
		"bad pattern":         {`"billing-*"`, `"[billing"`},
		"unknown routed team": {"bot: { team: platform", "bot: { team: nope"},
		"name and team":       {"bot: { team: platform", "bot: { name: a, team: platform"},
		"team action":         {`title: "x" }`, `action: rotate }`},
	} {
		body := strings.Replace(ok, edit[0], edit[1], 1)
		if body == ok {
			t.Fatalf("%s: edit did not apply", name)
		}
		if _, err := loadTeams(t, body); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	// A team without routing cannot be routed to.
	if _, err := loadTeams(t, "teams:\n  x: {}\nbots:\n  a: { team: x }\ntriggers:\n  - on: manual\n    name: p\n    steps: [ { bot: { team: x } } ]\n"); err == nil ||
		!strings.Contains(err.Error(), "no routing") {
		t.Errorf("routing to a routing-less team: %v", err)
	}
	for name, team := range map[string]string{
		"bad ceiling":    "autonomy: { ceiling: propose }",
		"bad tier":       "autonomy: { tiers: { sometimes: [x] } }",
		"bad sandbox":    "isolation: { sandbox: maybe }",
		"bad secrets":    "credentials: { secrets_via: smoke }",
		"secrets unused": "credentials: { allow_secrets: [house/pd] }",
		"negative pool":  "budget: { proactive: { max_runs_per_day: -1 } }",
		"bad spend cap":  "budget: { spend_cap: { per_action: 5, per_period: 1 } }",
		"zero spend cap": "budget: { spend_cap: { per_action: 0, per_period: 1 } }",
		"bad name":       "",
	} {
		body := "teams:\n  x: { " + team + " }\n"
		if name == "bad name" {
			body = "teams:\n  X: {}\n"
		}
		if _, err := loadTeams(t, body); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	w := strings.Join(func() []string {
		c, err := loadTeams(t, "teams:\n  empty: {}\n  boxed: { isolation: { sandbox: required } }\nbots:\n  a: { team: boxed }\n")
		if err != nil {
			t.Fatal(err)
		}
		return c.BotWarnings()
	}(), "\n")
	if !strings.Contains(w, "teams.empty: no enabled bot") || !strings.Contains(w, "teams.boxed: isolation.sandbox required") {
		t.Errorf("team warnings: %s", w)
	}
}

func TestTeamDelegationDepth(t *testing.T) {
	c, err := loadTeams(t, "teams:\n  x: { delegation: { max_depth: 2 } }\n  y: {}\nbots:\n  a: { team: x }\n  b: { team: y }\n  s: {}\n")
	if err != nil {
		t.Fatal(err)
	}
	if got := c.MaxDelegationDepth("a"); got != 2 {
		t.Errorf("team x: %d", got)
	}
	if got := c.MaxDelegationDepth("b"); got != DefaultDelegationMaxDepth {
		t.Errorf("a team that sets none: %d", got)
	}
	if got := c.MaxDelegationDepth("s"); got != DefaultDelegationMaxDepth {
		t.Errorf("a teamless bot: %d", got)
	}
	for _, d := range []string{"0", "-1", "11"} {
		if _, err := loadTeams(t, "teams:\n  x: { delegation: { max_depth: "+d+" } }\nbots:\n  a: { team: x }\n"); err == nil || !strings.Contains(err.Error(), "delegation.max_depth") {
			t.Errorf("max_depth %s: %v", d, err)
		}
	}
	for _, d := range []string{"1", "10"} {
		if _, err := loadTeams(t, "teams:\n  x: { delegation: { max_depth: "+d+" } }\nbots:\n  a: { team: x }\n"); err != nil {
			t.Errorf("max_depth %s: %v", d, err)
		}
	}
}

// Team chat's knobs: on by default, bounded.
func TestTeamChatLimits(t *testing.T) {
	var c TeamChat
	if c.EveryonePerHourOr() != DefaultEveryonePerHour || c.MaxReplyOr() != DefaultChatReply {
		t.Fatalf("defaults: %d %d", c.EveryonePerHourOr(), c.MaxReplyOr())
	}
	base := "teams:\n  platform:\n    routing: { fallback: sre }\n    chat: %s\nbots:\n  sre: { team: platform }\n"
	for body, want := range map[string]string{
		"{ everyone_per_hour: 3, max_reply: 8k }": "",
		"{ everyone_per_hour: 61 }":               "chat.everyone_per_hour: want 1 to 60",
		"{ max_reply: 20k }":                      "chat.max_reply: want 1 to 16384 bytes",
		"{ everyone: 3 }":                         "everyone",
	} {
		_, err := loadBots(t, fmt.Sprintf(base, body))
		switch {
		case want == "" && err != nil:
			t.Errorf("%s: %v", body, err)
		case want != "" && (err == nil || !strings.Contains(err.Error(), want)):
			t.Errorf("%s: got %v, want %q", body, err, want)
		}
	}
}

// tickets.mark_own_ready: off by default; a team's false is a ceiling; on,
// it is named as a widening. default_priority is a team's.
func TestTicketSettings(t *testing.T) {
	base := "teams:\n  platform:\n    routing: { fallback: sre }\n    tickets: %s\nbots:\n  sre:\n    team: platform\n    tickets: %s\n"
	c, err := loadBots(t, fmt.Sprintf(base, "{ default_priority: high }", "{}"))
	if err != nil {
		t.Fatal(err)
	}
	if c.BotMarksOwnReady("sre") || c.TicketDefaultPriority("bot:sre") != "high" || c.TicketDefaultPriority("team:platform") != "high" || c.TicketDefaultPriority("") != "normal" {
		t.Fatalf("defaults: %v %s", c.BotMarksOwnReady("sre"), c.TicketDefaultPriority("bot:sre"))
	}
	c, err = loadBots(t, fmt.Sprintf(base, "{ mark_own_ready: true }", "{}"))
	if err != nil || !c.BotMarksOwnReady("sre") {
		t.Fatalf("inherited from the team: %v", err)
	}
	if !strings.Contains(strings.Join(c.BotWarnings(), "\n"), "bots.sre: may mark the tickets it creates ready (tickets.mark_own_ready) — a WIDENING") {
		t.Fatalf("no widening warning: %v", c.BotWarnings())
	}
	if _, err := loadBots(t, fmt.Sprintf(base, "{ mark_own_ready: false }", "{ mark_own_ready: true }")); err == nil || !strings.Contains(err.Error(), "widens team platform's tickets.mark_own_ready false") {
		t.Fatalf("the team's ceiling: %v", err)
	}
	if c, err := loadBots(t, fmt.Sprintf(base, "{ mark_own_ready: true }", "{ mark_own_ready: false }")); err != nil || c.BotMarksOwnReady("sre") {
		t.Fatalf("a bot narrows: %v", err)
	}
	if _, err := loadBots(t, fmt.Sprintf(base, "{ default_priority: asap }", "{}")); err == nil || !strings.Contains(err.Error(), "default_priority") {
		t.Fatalf("a bad priority: %v", err)
	}
	if _, err := loadBots(t, fmt.Sprintf(base, "{}", "{ default_priority: high }")); err == nil || !strings.Contains(err.Error(), "a team setting") {
		t.Fatalf("a bot's default_priority: %v", err)
	}
}
