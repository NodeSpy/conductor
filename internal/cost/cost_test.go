package cost

import (
	"math"
	"strings"
	"testing"
	"time"
)

func approxEq(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func TestParseReportedShapes(t *testing.T) {
	cases := []struct {
		name   string
		output string
		wantIn int
		wantOu int
		wantOK bool
		usd    float64 // <0 = don't check
	}{
		{"claude-code", `{"result":"done","total_cost_usd":0.42,"usage":{"input_tokens":1000,"output_tokens":500}}`, 1000, 500, true, 0.42},
		{"anthropic usage", `{"usage":{"input_tokens":10,"output_tokens":20}}`, 10, 20, true, -1},
		{"openai usage", `{"usage":{"prompt_tokens":30,"completion_tokens":40}}`, 30, 40, true, -1},
		{"opencode tokens", `{"tokens":{"input":7,"output":9}}`, 7, 9, true, -1},
		{"paseo wrapper", `{"output":{"usage":{"input_tokens":5,"output_tokens":6}}}`, 5, 6, true, -1},
		{"cost only", `{"cost_usd":0.05}`, 0, 0, true, 0.05},
		{"no signal", `{"result":"done"}`, 0, 0, false, -1},
		{"not json", `plain text output`, 0, 0, false, -1},
		{"empty", ``, 0, 0, false, -1},
	}
	for _, tc := range cases {
		u, ok := ParseReported(tc.output, "claude-sonnet")
		if ok != tc.wantOK {
			t.Errorf("%s: ok=%v want %v", tc.name, ok, tc.wantOK)
			continue
		}
		if !ok {
			continue
		}
		if u.InputTokens != tc.wantIn || u.OutputTokens != tc.wantOu {
			t.Errorf("%s: tokens %d/%d want %d/%d", tc.name, u.InputTokens, u.OutputTokens, tc.wantIn, tc.wantOu)
		}
		if u.Approximate {
			t.Errorf("%s: reported usage must not be approximate", tc.name)
		}
		if tc.usd >= 0 && !approxEq(u.CostUSD, tc.usd) {
			t.Errorf("%s: cost %v want %v", tc.name, u.CostUSD, tc.usd)
		}
		if u.TotalTokens != tc.wantIn+tc.wantOu && tc.wantIn+tc.wantOu > 0 {
			t.Errorf("%s: total %d", tc.name, u.TotalTokens)
		}
	}
}

func TestParseReportedModelWins(t *testing.T) {
	u, ok := ParseReported(`{"model":"claude-opus-x","usage":{"input_tokens":1,"output_tokens":1}}`, "cfg-model")
	if !ok || u.Model != "claude-opus-x" {
		t.Fatalf("output model must win: %+v %v", u, ok)
	}
}

func TestEstimate(t *testing.T) {
	u := Estimate("claude-sonnet", "aaaa", "bbbbbbbb") // 4 + 8 chars → 1 + 2 tokens
	if !u.Approximate {
		t.Fatal("estimates are approximate")
	}
	if u.InputTokens != 1 || u.OutputTokens != 2 || u.TotalTokens != 3 {
		t.Fatalf("chars/4: %+v", u)
	}
	// sonnet pricing: 1*3/1e6 + 2*15/1e6
	if !approxEq(u.CostUSD, (1*3.0+2*15.0)/1e6) {
		t.Fatalf("priced estimate: %v", u.CostUSD)
	}
}

func TestFromRunFallsBack(t *testing.T) {
	if u := FromRun("m", "prompt", `{"usage":{"input_tokens":9,"output_tokens":1}}`); u.Approximate || u.TotalTokens != 10 {
		t.Fatalf("reported path: %+v", u)
	}
	if u := FromRun("m", "prompt", "plain"); !u.Approximate {
		t.Fatalf("estimate path: %+v", u)
	}
}

func TestPriceOverrides(t *testing.T) {
	defer SetPricing(nil, nil)
	// Built-in default: opus.
	if got := PriceUSD("claude-opus-4", 1_000_000, 0); !approxEq(got, 15) {
		t.Fatalf("builtin opus: %v", got)
	}
	// Config override wins.
	SetPricing(map[string]ModelPrice{"claude-opus-*": {Input: 1, Output: 2}}, nil)
	if got := PriceUSD("claude-opus-4", 1_000_000, 1_000_000); !approxEq(got, 3) {
		t.Fatalf("override: %v", got)
	}
	// Custom fallback for unmatched models.
	SetPricing(nil, &ModelPrice{Input: 10, Output: 10})
	if got := PriceUSD("mystery-model", 500_000, 500_000); !approxEq(got, 10) {
		t.Fatalf("fallback: %v", got)
	}
}

func TestMeterWindows(t *testing.T) {
	m := NewMeter()
	now := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	m.SetNow(func() time.Time { return now })

	m.Record([]string{"global", "profile:fixer"}, Usage{TotalTokens: 100, CostUSD: 1})
	now = now.Add(30 * time.Minute)
	m.Record([]string{"global"}, Usage{TotalTokens: 50, CostUSD: 0.5})

	if tok, usd := m.SpentIn("global", time.Hour); tok != 150 || !approxEq(usd, 1.5) {
		t.Fatalf("global 1h: %d %v", tok, usd)
	}
	if tok, usd := m.SpentIn("profile:fixer", time.Hour); tok != 100 || !approxEq(usd, 1) {
		t.Fatalf("profile 1h: %d %v", tok, usd)
	}
	// At 12:50 the 12:00 entry has aged out of a 45m window; 12:30 hasn't.
	now = now.Add(20 * time.Minute)
	if tok, usd := m.SpentIn("global", 45*time.Minute); tok != 50 || !approxEq(usd, 0.5) {
		t.Fatalf("global 45m: %d %v", tok, usd)
	}
	// Advance past everything: all pruned.
	now = now.Add(2 * time.Hour)
	if tok, _ := m.SpentIn("global", time.Hour); tok != 0 {
		t.Fatalf("aged out: %d", tok)
	}
	// Unknown scope / nil meter are safe.
	if tok, _ := m.SpentIn("nope", time.Hour); tok != 0 {
		t.Fatal("unknown scope")
	}
	var nilM *Meter
	nilM.Record([]string{"x"}, Usage{})
	if tok, _ := nilM.SpentIn("x", time.Hour); tok != 0 {
		t.Fatal("nil meter")
	}
}

// Regression (#36 review H5): self-reported usage is untrusted — negatives
// are clamped (they'd drain other runs' shared budget windows) and
// absurdly-low reports are floored to the model+I/O estimate (a runtime
// "reporting" near-zero must not slip under hard caps).
func TestReportedUsageClampedAndFloored(t *testing.T) {
	// Negative report → clamped at parse.
	u, ok := ParseReported(`{"usage":{"input_tokens":-5000,"output_tokens":-1},"total_cost_usd":-9.5}`, "claude-sonnet")
	if !ok || u.InputTokens != 0 || u.OutputTokens != 0 || u.TotalTokens != 0 || u.CostUSD != 0 {
		t.Fatalf("negative report must clamp to zero: %+v (ok=%v)", u, ok)
	}
	// And a negative report can't poison the meter through FromRun either.
	m := NewMeter()
	m.Record([]string{"global"}, FromRun("claude-sonnet", "p", `{"usage":{"input_tokens":-5000,"output_tokens":0}}`))
	if tok, usd := m.SpentIn("global", time.Hour); tok < 0 || usd < 0 {
		t.Fatalf("meter poisoned: %d %v", tok, usd)
	}

	// A big run "reporting" near-zero tokens is floored to the estimate.
	prompt := strings.Repeat("p", 8000) // ~2000 tokens
	output := strings.Repeat("o", 8000) // ~2000 tokens
	_ = output
	low := `{"usage":{"input_tokens":1,"output_tokens":1}}`
	u = FromRun("claude-sonnet", prompt, low)
	est := Estimate("claude-sonnet", prompt, low)
	if !u.Approximate || u.TotalTokens != est.TotalTokens {
		t.Fatalf("under-report must be floored to the estimate: %+v (est %+v)", u, est)
	}

	// Plausible tokens with an absurdly low $ figure → cost recomputed from
	// the reported tokens at table price.
	rep := `{"usage":{"input_tokens":2000,"output_tokens":2000},"total_cost_usd":0.000001}`
	u = FromRun("claude-sonnet", prompt, rep)
	want := PriceUSD("claude-sonnet", 2000, 2000)
	if u.CostUSD != want {
		t.Fatalf("lowballed cost must be floored: %v want %v", u.CostUSD, want)
	}
	if u.Approximate {
		t.Fatalf("reported-token usage stays non-approximate: %+v", u)
	}

	// An honest report well within range passes through untouched.
	honest := `{"usage":{"input_tokens":1900,"output_tokens":600},"total_cost_usd":0.02}`
	u = FromRun("claude-sonnet", prompt, honest)
	if u.TotalTokens != 2500 || u.CostUSD != 0.02 || u.Approximate {
		t.Fatalf("honest report altered: %+v", u)
	}
}
