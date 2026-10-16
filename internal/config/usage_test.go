package config

import (
	"strings"
	"testing"
)

const sweepBody = "bots:\n  sre:\n    charter: \"## Proactive checks\\n- x\"\n    proactive:\n      every: 4h\n      checklist: { section: Proactive checks }\n"

func TestProactiveBudgetUnitsAreOptional(t *testing.T) {
	// No cap at all: allowed (every: already bounds the schedule).
	c, err := loadBots(t, sweepBody)
	if err != nil {
		t.Fatalf("an uncapped sweep schedule: %v", err)
	}
	if !c.Bots["sre"].Proactive.Budget.IsZero() {
		t.Fatal("a cap appeared from nowhere")
	}
	// Units alone, any combination, with the token shorthand.
	c, err = loadBots(t, sweepBody+"      budget: { max_turns_per_day: 40, max_tokens_per_day: 500k }\n")
	if err != nil {
		t.Fatal(err)
	}
	if b := c.Bots["sre"].Proactive.Budget; b.MaxTurnsPerDay != 40 || b.MaxTokensPerDay != 500000 || b.MaxCostPerDay != 0 {
		t.Fatalf("units: %+v", b)
	}
	// The sweep trigger carries tokens (and dollars, when set) as its
	// workflow budget, so a running sweep is bounded too.
	var pol *Policy
	for _, tr := range c.Triggers {
		if tr.Name == BotSweepTrigger("sre") {
			pol = tr.Policy
		}
	}
	if pol == nil || pol.Budget == nil || pol.Budget.MaxTokens != 500000 || pol.Budget.MaxCostUSD != 0 {
		t.Fatalf("sweep workflow budget: %+v", pol)
	}
	// Back-compat: a dollar cap alone still loads, as before.
	if _, err := loadBots(t, sweepBody+"      budget: { max_cost_per_day: 2.5 }\n"); err != nil {
		t.Fatalf("a dollar cap: %v", err)
	}
	for _, neg := range []string{"max_runs_per_day: -1", "max_turns_per_day: -1", "max_tokens_per_day: -5", "max_cost_per_day: -1"} {
		if _, err := loadBots(t, sweepBody+"      budget: { "+neg+" }\n"); err == nil || !strings.Contains(err.Error(), "negative") {
			t.Errorf("%s: %v", neg, err)
		}
	}
}

// Narrow, never widen — per unit.
func TestTeamPoolCeilingPerUnit(t *testing.T) {
	pool := "teams:\n  x: { budget: { proactive: { max_runs_per_day: 6, max_turns_per_day: 60, max_tokens_per_day: 600k, max_cost_per_day: 3 } } }\n"
	bot := func(budget string) string {
		return pool + "bots:\n  a:\n    team: x\n    charter: \"## Proactive checks\\n- x\"\n    proactive:\n      every: 4h\n      checklist: { section: Proactive checks }\n" + budget
	}
	for unit, b := range map[string]string{
		"max_runs_per_day":   "      budget: { max_runs_per_day: 7 }\n",
		"max_turns_per_day":  "      budget: { max_turns_per_day: 61 }\n",
		"max_tokens_per_day": "      budget: { max_tokens_per_day: 601k }\n",
		"max_cost_per_day":   "      budget: { max_cost_per_day: 3.5 }\n",
	} {
		_, err := loadTeams(t, bot(b))
		if err == nil || !strings.Contains(err.Error(), "proactive.budget."+unit) || !strings.Contains(err.Error(), "team x's budget.proactive."+unit) {
			t.Errorf("%s past the pool: %v", unit, err)
		}
	}
	// At or under the ceiling is fine; unset units inherit the pool's.
	c, err := loadTeams(t, bot("      budget: { max_turns_per_day: 60, max_tokens_per_day: 100k }\n"))
	if err != nil {
		t.Fatal(err)
	}
	if b := c.Bots["a"].Proactive.Budget; b.MaxRunsPerDay != 6 || b.MaxTurnsPerDay != 60 || b.MaxTokensPerDay != 100000 || b.MaxCostPerDay != 3 {
		t.Fatalf("inheritance per unit: %+v", b)
	}
	// A pool that caps one unit says nothing about the others.
	c, err = loadTeams(t, "teams:\n  x: { budget: { proactive: { max_turns_per_day: 10 } } }\nbots:\n  a:\n    team: x\n    charter: \"## Proactive checks\\n- x\"\n    proactive:\n      every: 4h\n      checklist: { section: Proactive checks }\n      budget: { max_runs_per_day: 50, max_tokens_per_day: 9m }\n")
	if err != nil {
		t.Fatalf("an uncapped unit is not a ceiling: %v", err)
	}
	if b := c.Bots["a"].Proactive.Budget; b.MaxTurnsPerDay != 10 || b.MaxRunsPerDay != 50 {
		t.Fatalf("one-unit pool: %+v", b)
	}
	// Each unit inherits on its own: a token-only pool fills tokens.
	c, err = loadTeams(t, "teams:\n  x: { budget: { proactive: { max_tokens_per_day: 2m } } }\nbots:\n  a:\n    team: x\n    charter: \"## Proactive checks\\n- x\"\n    proactive:\n      every: 4h\n      checklist: { section: Proactive checks }\n      budget: { max_turns_per_day: 9 }\n")
	if err != nil {
		t.Fatal(err)
	}
	if b := c.Bots["a"].Proactive.Budget; b.MaxTokensPerDay != 2000000 || b.MaxTurnsPerDay != 9 || b.MaxRunsPerDay != 0 {
		t.Fatalf("token inheritance: %+v", b)
	}
	// An empty pool loads, with a warning.
	c, err = loadTeams(t, "teams:\n  x: { budget: { proactive: {} } }\nbots:\n  a: { team: x }\n")
	if err != nil {
		t.Fatal(err)
	}
	if w := strings.Join(c.BotWarnings(), "\n"); !strings.Contains(w, "teams.x: budget.proactive sets no cap") {
		t.Fatalf("no warning for an empty pool: %s", w)
	}
}

func TestUsageConfig(t *testing.T) {
	c, err := loadBots(t, "")
	if err != nil {
		t.Fatal(err)
	}
	if c.UsageMode() != UsageOff {
		t.Fatalf("default mode: %q", c.UsageMode())
	}
	for _, m := range []string{"off", "subscription", "api"} {
		c, err := loadBots(t, "usage: { mode: "+m+" }\n")
		if err != nil || c.UsageMode() != m {
			t.Errorf("mode %s: %v", m, err)
		}
	}
	c, err = loadBots(t, "usage:\n  mode: subscription\n  sources:\n    - { name: proxy, type: http, url: \"http://127.0.0.1:8080/usage\", headers: { Authorization: x }, timeout: 3s }\n")
	if err != nil || len(c.Usage.Sources) != 1 || c.Usage.Sources[0].Timeout.D().Seconds() != 3 {
		t.Fatalf("a source: %+v %v", c.Usage, err)
	}
	for name, body := range map[string]string{
		"bad mode":     "usage: { mode: dollars }\n",
		"unknown type": "usage: { sources: [ { name: s, type: vendorx, url: \"http://h/u\" } ] }\n",
		"bad url":      "usage: { sources: [ { name: s, type: http, url: \"ftp://h/u\" } ] }\n",
		"no url":       "usage: { sources: [ { name: s, type: http } ] }\n",
		"bad name":     "usage: { sources: [ { name: S!, type: http, url: \"http://h/u\" } ] }\n",
		"twice":        "usage: { sources: [ { name: s, type: http, url: \"http://h/u\" }, { name: s, type: http, url: \"http://h/v\" } ] }\n",
	} {
		if _, err := loadBots(t, body); err == nil || !strings.Contains(err.Error(), "usage") {
			t.Errorf("%s: %v", name, err)
		}
	}
}
