package flow

import (
	"testing"

	"github.com/NodeSpy/conductor/internal/cost"
)

// A run's usage is kept per runtime and model — never summed across
// providers — alongside the totals the caps enforce.
func TestCostAccPerModel(t *testing.T) {
	a := &costAcc{}
	a.add("claude-cli", cost.Usage{Model: "claude-x", TotalTokens: 100, Turns: 3, CostUSD: 1})
	a.add("codex-cli", cost.Usage{Model: "gpt-y", TotalTokens: 50, Turns: 1, CostUSD: 0.5, Approximate: true})
	a.add("claude-cli", cost.Usage{Model: "claude-x", TotalTokens: 10, Turns: 1})
	us := a.usage()
	if len(us) != 2 {
		t.Fatalf("per model: %+v", us)
	}
	if c := us[0]; c.Runtime != "claude-cli" || c.Model != "claude-x" || c.Runs != 2 || c.Tokens != 110 || c.Turns != 4 || c.CostUSD != 1 || c.Approximate {
		t.Fatalf("claude: %+v", c)
	}
	if c := us[1]; c.Runtime != "codex-cli" || c.Tokens != 50 || !c.Approximate {
		t.Fatalf("codex: %+v", c)
	}
	if tok, _, _ := a.totals(); tok != 160 || a.turnsTotal() != 5 {
		t.Fatalf("totals: %d %d", tok, a.turnsTotal())
	}
	var nilAcc *costAcc
	nilAcc.add("x", cost.Usage{})
	if nilAcc.usage() != nil || nilAcc.turnsTotal() != 0 {
		t.Fatal("nil accumulator")
	}
}
