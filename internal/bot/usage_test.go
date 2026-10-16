package bot

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/cost"
	"github.com/NodeSpy/conductor/internal/store"
)

// UNIT BUDGETS: runs, turns and tokens cap sweeps with no dollar figure
// anywhere; the first cap reached stops the next sweep. Each unit alone.
func TestSweepUnitBudgets(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name   string
		budget config.ProactiveBudget
		usage  RunUsage
		want   string // the cap that stops the third sweep
	}{
		{"turns", config.ProactiveBudget{MaxTurnsPerDay: 10}, RunUsage{Turns: 5, Tokens: 1}, "max_turns_per_day"},
		{"tokens", config.ProactiveBudget{MaxTokensPerDay: 20000}, RunUsage{Turns: 1, Tokens: 10000}, "max_tokens_per_day"},
		{"runs", config.ProactiveBudget{MaxRunsPerDay: 2}, RunUsage{Turns: 1, Tokens: 1}, "max_runs_per_day"},
		{"first to hit wins", config.ProactiveBudget{MaxRunsPerDay: 9, MaxTurnsPerDay: 100, MaxTokensPerDay: 20000}, RunUsage{Turns: 3, Tokens: 10000}, "max_tokens_per_day"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newSweepFixture(t, config.BotProactive{Every: config.Duration(time.Hour), Budget: tc.budget})
			f.usage = tc.usage
			for i := 0; i < 2; i++ {
				f.svc.Tick(ctx, "sre")
				fired := f.sweepFired()
				if len(fired) != i+1 {
					t.Fatalf("sweep %d did not start under the cap: %d fired", i+1, len(fired))
				}
				f.finish(t, fired[i], map[string]any{"summary": "ok"})
				f.clk.add(time.Hour)
			}
			sw := f.svc.Sweeps("sre", 1)[0]
			if sw.Turns != tc.usage.Turns || sw.Tokens != tc.usage.Tokens {
				t.Fatalf("sweep usage not recorded: %+v", sw)
			}
			f.svc.Tick(ctx, "sre")
			if n := len(f.sweepFired()); n != 2 {
				t.Fatalf("a sweep ran past %s (%d)", tc.want, n)
			}
			_, err := f.svc.StartSweep(ctx, "sre", true)
			if err == nil || !strings.Contains(err.Error(), tc.want) || strings.Contains(err.Error(), "$") {
				t.Fatalf("budget: %v", err)
			}
			// The window rolls.
			f.clk.add(SweepWindow)
			if _, err := f.svc.StartSweep(ctx, "sre", true); err != nil {
				t.Fatalf("after the window: %v", err)
			}
		})
	}
}

func TestBudgetExceededBoundaries(t *testing.T) {
	b := config.ProactiveBudget{MaxRunsPerDay: 3, MaxTurnsPerDay: 30, MaxTokensPerDay: 300, MaxCostPerDay: 3}
	if why := b.Exceeded(config.BudgetUsage{Runs: 2, Turns: 29, Tokens: 299, CostUSD: 2.99}); why != "" {
		t.Fatalf("under every cap: %s", why)
	}
	for _, tc := range []struct {
		u    config.BudgetUsage
		want string
	}{
		{config.BudgetUsage{Runs: 3}, "max_runs_per_day"},
		{config.BudgetUsage{Turns: 30}, "max_turns_per_day"},
		{config.BudgetUsage{Tokens: 300}, "max_tokens_per_day"},
		{config.BudgetUsage{CostUSD: 3}, "max_cost_per_day"},
	} {
		if why := b.Exceeded(tc.u); !strings.Contains(why, tc.want) {
			t.Errorf("%+v: %q, want %s", tc.u, why, tc.want)
		}
	}
	if why := (config.ProactiveBudget{}).Exceeded(config.BudgetUsage{Runs: 1e6, Turns: 1e6, Tokens: 1e9, CostUSD: 1e6}); why != "" {
		t.Fatalf("no cap set, yet exceeded: %s", why)
	}
}

// The TEAM POOL counts every unit across the team's bots.
func TestTeamPoolUnits(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name string
		pool config.ProactiveBudget
		set  func(*store.Sweep)
		want string
	}{
		{"turns", config.ProactiveBudget{MaxTurnsPerDay: 10}, func(s *store.Sweep) { s.Turns = 5 }, "max_turns_per_day"},
		{"tokens", config.ProactiveBudget{MaxTokensPerDay: 1000}, func(s *store.Sweep) { s.Tokens = 500 }, "max_tokens_per_day"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newTeamFixture(t, func(c *config.Config) {
				tc2 := c.Teams["platform"]
				pool := tc.pool
				tc2.Budget.Proactive = &pool
				c.Teams["platform"] = tc2
				for _, n := range []string{"a", "b", "c"} {
					m := c.Bots[n]
					m.Charter = "## Proactive checks\n- x"
					m.Proactive = &config.BotProactive{Every: config.Duration(time.Hour), Checklist: config.ProactiveChecklist{Section: "Proactive checks"}}
					c.Bots[n] = m
				}
			})
			// a and b each used half the pool; c is on another team.
			for _, n := range []string{"a", "b", "c"} {
				_ = f.st.UpdateBot(n, func(r *store.BotRecord) error {
					sw := &store.Sweep{ID: "x1", Status: "quiet", Started: f.clk.now().Add(-time.Hour)}
					tc.set(sw)
					r.Sweeps = map[string]*store.Sweep{"x1": sw}
					return nil
				})
			}
			if _, err := f.svc.StartSweep(ctx, "a", true); err == nil || !strings.Contains(err.Error(), "team platform's budget") || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("the pool did not stop a teammate on %s: %v", tc.name, err)
			}
			if _, err := f.svc.StartSweep(ctx, "c", true); err != nil {
				t.Fatalf("another team's bot was charged to this pool: %v", err)
			}
		})
	}
}

// DISPLAY follows usage.mode (default off); enforcement does not.
func TestUsageDisplayModes(t *testing.T) {
	defer cost.SetDisplayMode("")
	sw := store.Sweep{ID: "x1", CostUSD: 1.5, Tokens: 300, Turns: 4, Started: time.Now(),
		Usage: []store.ModelUsage{{Runtime: "claude-cli", Model: "claude-x", Runs: 1, Turns: 3, Tokens: 200, CostUSD: 1},
			{Runtime: "codex-cli", Model: "gpt-y", Runs: 1, Turns: 1, Tokens: 100, CostUSD: 0.5}}}
	recs := []store.BotRecord{{Sweeps: map[string]*store.Sweep{"x1": &sw}}}
	pool := config.ProactiveBudget{MaxRunsPerDay: 5, MaxTokensPerDay: 9000}

	cost.SetDisplayMode("")
	if d := sweepForDisplay(sw); d.CostUSD != 0 || d.Tokens != 0 || d.Turns != 0 || d.Usage != nil {
		t.Fatalf("off (the default) shows usage: %+v", d)
	}
	p := poolForDisplay(pool, time.Now(), recs)
	if _, ok := p["observed_24h"]; ok || p["max_runs_per_day"] != 5 || p["max_tokens_per_day"] != 9000 {
		t.Fatalf("off: the pool shows usage, or hides its caps: %v", p)
	}

	cost.SetDisplayMode("subscription")
	d := sweepForDisplay(sw)
	if d.CostUSD != 0 || d.Tokens != 0 || d.Turns != 0 || len(d.Usage) != 2 || d.Usage[0].CostUSD != 0 || d.Usage[1].CostUSD != 0 || d.Usage[0].Tokens != 200 {
		t.Fatalf("subscription: per model, units only: %+v", d)
	}
	p = poolForDisplay(pool, time.Now(), recs)
	obs := p["observed_24h"].(map[string]any)
	if bm := obs["by_model"].([]any); len(bm) != 2 || bm[0].(map[string]any)["cost_usd"] != nil {
		t.Fatalf("subscription pool: %v", obs)
	}

	cost.SetDisplayMode("api")
	if d := sweepForDisplay(sw); d.Tokens != 0 || d.Usage[0].CostUSD != 1 || d.Usage[1].CostUSD != 0.5 {
		t.Fatalf("api: per-model dollars, no cross-provider totals: %+v", d)
	}
}
