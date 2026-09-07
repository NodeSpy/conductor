// Package cost is the token/$ accounting layer (#36 §14): per-agent-run
// usage capture (runtime-reported where the output exposes it, estimated and
// marked approximate otherwise), a model→price table, and the rolling-window
// spend meter behind the hard $/token budget caps.
//
// Capture happens where the run's output comes back (the flow runner's agent
// step, the engine's legacy dispatch): ParseReported scans the runtime's
// JSON output for the usage shapes the supported runtimes emit (claude-code
// `-p --output-format json`, paseo `run --json`, opencode message info,
// OpenAI-style usage blocks); anything unrecognized falls back to Estimate
// (chars/4 on prompt and output) with Approximate set — spend is then a
// floor, never a claim of precision.
package cost

import (
	"encoding/json"
	"path"
	"strings"
	"sync"
	"time"
)

// Usage is one agent run's token/$ accounting, stored on the run record and
// in the audit.
type Usage struct {
	InputTokens  int     `json:"input_tokens,omitempty"`
	OutputTokens int     `json:"output_tokens,omitempty"`
	TotalTokens  int     `json:"total_tokens"`
	CostUSD      float64 `json:"cost_usd"`
	Model        string  `json:"model,omitempty"`
	// Approximate marks estimated (not runtime-reported) numbers.
	Approximate bool `json:"approximate,omitempty"`
}

// ModelPrice is $ per 1M tokens for one model (or model pattern).
type ModelPrice struct {
	Input  float64 `yaml:"input" json:"input"`
	Output float64 `yaml:"output" json:"output"`
}

// defaultPrices maps model-name glob patterns to $/1M-token prices. These are
// deliberately coarse defaults — pricing drifts, so a `pricing:` config block
// overrides them; every estimated figure is marked approximate anyway.
var defaultPrices = []struct {
	pat   string
	price ModelPrice
}{
	{"*opus*", ModelPrice{Input: 15, Output: 75}},
	{"*sonnet*", ModelPrice{Input: 3, Output: 15}},
	{"*haiku*", ModelPrice{Input: 0.8, Output: 4}},
	{"*claude*", ModelPrice{Input: 3, Output: 15}},
	{"*gpt-4o-mini*", ModelPrice{Input: 0.15, Output: 0.6}},
	{"*gpt*", ModelPrice{Input: 2.5, Output: 10}},
	{"*o3*", ModelPrice{Input: 2, Output: 8}},
	{"*gemini*flash*", ModelPrice{Input: 0.15, Output: 0.6}},
	{"*gemini*", ModelPrice{Input: 1.25, Output: 10}},
}

// fallbackPrice prices a model no pattern matches (a mid-tier guess).
var fallbackPrice = ModelPrice{Input: 3, Output: 15}

// pricing is the installed override table (config `pricing:`), consulted
// before the built-in defaults.
var pricing struct {
	mu     sync.RWMutex
	models map[string]ModelPrice
	def    *ModelPrice
}

// SetPricing installs config-provided price overrides: models maps model
// glob patterns to prices; def (non-nil) replaces the built-in fallback.
func SetPricing(models map[string]ModelPrice, def *ModelPrice) {
	pricing.mu.Lock()
	pricing.models = models
	pricing.def = def
	pricing.mu.Unlock()
}

// PriceUSD prices in/out tokens for a model: config override patterns first,
// then the built-in defaults, then the fallback.
func PriceUSD(model string, in, out int) float64 {
	p := priceFor(model)
	return (float64(in)*p.Input + float64(out)*p.Output) / 1e6
}

func priceFor(model string) ModelPrice {
	m := strings.ToLower(model)
	pricing.mu.RLock()
	over, def := pricing.models, pricing.def
	pricing.mu.RUnlock()
	for pat, p := range over {
		if ok, err := path.Match(strings.ToLower(pat), m); err == nil && ok {
			return p
		}
	}
	for _, e := range defaultPrices {
		if ok, err := path.Match(e.pat, m); err == nil && ok {
			return e.price
		}
	}
	if def != nil {
		return *def
	}
	return fallbackPrice
}

// ParseReported extracts runtime-reported usage from a run's JSON output.
// Recognized shapes (top-level, or nested one level under the common wrapper
// keys result/output/outputs — paseo wraps its runtime's JSON that way):
//
//   - usage: {input_tokens, output_tokens}          (Anthropic / claude-code)
//   - usage: {prompt_tokens, completion_tokens}     (OpenAI-style)
//   - tokens: {input, output}                       (opencode message info)
//   - total_cost_usd / cost_usd                     ($ figure, e.g. claude-code)
//
// A reported $ figure wins over a computed one. Reported == not approximate.
// ok is false when the output carries no usage signal at all.
func ParseReported(output, model string) (Usage, bool) {
	out := strings.TrimSpace(output)
	if out == "" || !strings.HasPrefix(out, "{") {
		return Usage{}, false
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(out), &m); err != nil {
		return Usage{}, false
	}
	if u, ok := usageFrom(m, model); ok {
		return u, true
	}
	for _, k := range []string{"result", "output", "outputs", "info"} {
		if inner, ok := m[k].(map[string]any); ok {
			if u, ok := usageFrom(inner, model); ok {
				return u, true
			}
		}
	}
	return Usage{}, false
}

// usageFrom reads the usage shapes off one JSON object.
func usageFrom(m map[string]any, model string) (Usage, bool) {
	u := Usage{Model: model}
	found := false
	if mm, ok := m["model"].(string); ok && mm != "" {
		u.Model = mm
	}
	if ub, ok := m["usage"].(map[string]any); ok {
		in, okIn := intField(ub, "input_tokens", "prompt_tokens")
		out, okOut := intField(ub, "output_tokens", "completion_tokens")
		if okIn || okOut {
			u.InputTokens, u.OutputTokens = in, out
			found = true
		}
		if tot, ok := intField(ub, "total_tokens"); ok && u.InputTokens+u.OutputTokens == 0 {
			u.TotalTokens = tot
			found = true
		}
	}
	if tb, ok := m["tokens"].(map[string]any); ok && !found {
		in, okIn := intField(tb, "input")
		out, okOut := intField(tb, "output")
		if okIn || okOut {
			u.InputTokens, u.OutputTokens = in, out
			found = true
		}
	}
	if u.TotalTokens == 0 {
		u.TotalTokens = u.InputTokens + u.OutputTokens
	}
	costReported := false
	for _, k := range []string{"total_cost_usd", "cost_usd", "total_cost"} {
		if f, ok := m[k].(float64); ok {
			u.CostUSD = f
			costReported = true
			found = true
			break
		}
	}
	if !found {
		return Usage{}, false
	}
	// Self-reported figures are untrusted input (#36 review H5): a negative
	// value would poison the shared spend meter (draining other runs' budget
	// windows), so clamp everything to >= 0 before it goes anywhere.
	u.InputTokens = max(u.InputTokens, 0)
	u.OutputTokens = max(u.OutputTokens, 0)
	u.TotalTokens = max(u.TotalTokens, 0)
	u.CostUSD = max(u.CostUSD, 0)
	if u.TotalTokens < u.InputTokens+u.OutputTokens {
		u.TotalTokens = u.InputTokens + u.OutputTokens
	}
	if !costReported && u.TotalTokens > 0 {
		u.CostUSD = PriceUSD(u.Model, u.InputTokens, u.OutputTokens)
	}
	return u, true
}

func intField(m map[string]any, keys ...string) (int, bool) {
	for _, k := range keys {
		if f, ok := m[k].(float64); ok {
			return int(f), true
		}
	}
	return 0, false
}

// Estimate approximates usage from the prompt and output text when the
// runtime reported nothing: chars/4 per side, priced by model. Always marked
// approximate.
func Estimate(model, prompt, output string) Usage {
	in, out := len(prompt)/4, len(output)/4
	return Usage{
		InputTokens: in, OutputTokens: out, TotalTokens: in + out,
		CostUSD: PriceUSD(model, in, out), Model: model, Approximate: true,
	}
}

// underReportDivisor is the plausibility floor: a reported figure below
// 1/10th of the model+I/O estimate is treated as evasion (or breakage), not
// truth — a runtime "reporting" near-zero usage must not slip under budget
// caps. Legitimate savings (prompt caching) don't shrink usage 10x below the
// raw I/O size; when they someday do, the floor errs toward over-charging
// the budget, never under.
const underReportDivisor = 10

// FromRun resolves one run's usage: runtime-reported when the output carries
// it — clamped and floored against the model+I/O estimate (#36 review H5) —
// estimated (approximate) otherwise.
func FromRun(model, prompt, output string) Usage {
	u, ok := ParseReported(output, model)
	if !ok {
		return Estimate(model, prompt, output)
	}
	est := Estimate(model, prompt, output)
	// Implausibly low token report → the estimate is the floor.
	if u.TotalTokens*underReportDivisor < est.TotalTokens {
		return est
	}
	// Tokens plausible but the $ figure is far below what those very tokens
	// cost at table price → recompute from the reported tokens.
	if floor := PriceUSD(u.Model, u.InputTokens, u.OutputTokens); u.CostUSD*underReportDivisor < floor {
		u.CostUSD = floor
	}
	return u
}

// Meter is the rolling-window spend ledger behind the hard budget caps. Like
// the engine's agents-per-hour window it is in-memory — a restart resets the
// window, and the durable record is the audit trail. Entries are kept per
// scope key ("global", "profile:<name>", "workflow:<on>").
type Meter struct {
	mu      sync.Mutex
	entries map[string][]entry
	now     func() time.Time
}

type entry struct {
	at     time.Time
	tokens int
	usd    float64
}

// NewMeter builds an empty meter.
func NewMeter() *Meter {
	return &Meter{entries: map[string][]entry{}, now: time.Now}
}

// SetNow injects a clock for tests.
func (m *Meter) SetNow(fn func() time.Time) { m.now = fn }

// Record charges one run's usage to every given scope.
func (m *Meter) Record(scopes []string, u Usage) {
	if m == nil {
		return
	}
	e := entry{tokens: u.TotalTokens, usd: u.CostUSD}
	m.mu.Lock()
	e.at = m.now()
	for _, s := range scopes {
		if s != "" {
			m.entries[s] = append(m.entries[s], e)
		}
	}
	m.mu.Unlock()
}

// SpentIn sums a scope's spend over the trailing window, pruning entries
// older than the window as it goes.
func (m *Meter) SpentIn(scope string, window time.Duration) (tokens int, usd float64) {
	if m == nil || window <= 0 {
		return 0, 0
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	cut := m.now().Add(-window)
	kept := m.entries[scope][:0]
	for _, e := range m.entries[scope] {
		if e.at.After(cut) {
			kept = append(kept, e)
			tokens += e.tokens
			usd += e.usd
		}
	}
	if len(kept) == 0 {
		delete(m.entries, scope)
	} else {
		m.entries[scope] = kept
	}
	return tokens, usd
}
