package config

import (
	"fmt"
	"strings"

	"github.com/NodeSpy/conductor/internal/systemone"
)

// DecideSpec is a `decide:` step: a typed, probabilistic decision in the
// `system_one/v1` contract (internal/systemone) — yes/no, pick-one, and score
// questions answered with calibrated probabilities rather than free text.
//
// A decide step is backend-neutral. The step's own `model:` names the tier it
// resolves against, exactly as an agent step's does; resolution then offers
// it every (runtime, model) pair that can answer — a DECISION runtime that
// speaks the protocol natively (Jev) first, then any agent runtime, which
// answers through conductor's v1 adapter in one restricted session. The
// answering pair is recorded as `_by`; which one answered never changes the
// shape downstream steps read.
//
// Execution is deliberately fixed, not configurable: no checkout, no tools,
// one turn, no guidance. validateDecideStep rejects every agent-behavior
// field on a decide step, because a decision that needs to explore is an
// agent step, not a decision.
type DecideSpec struct {
	// Protocol names the contract version. Empty is system_one/v1, the only
	// version today; it exists so a second version can be added beside v1
	// rather than instead of it.
	Protocol string `yaml:"protocol,omitempty"`
	// State is the whole of what the decision sees: a templated string, or a
	// structured value rendered with types preserved. Nothing else — no
	// checkout, no tools — reaches the answering model.
	State any `yaml:"state,omitempty"`
	// Questions are the named questions, in order.
	Questions systemone.Questions `yaml:"questions,omitempty"`
	// Default is the answers used only when EVERY candidate fails — each a
	// full v1 answer or just its headline value ({ noul: 0 }). Unset, a step
	// whose candidates all fail fails.
	Default map[string]any `yaml:"default,omitempty"`
	// Escalate asks a second model when a successful answer is uncertain.
	// Off unless declared.
	Escalate *EscalateSpec `yaml:"escalate,omitempty"`
	// Observe names a SQL `stores:` entry every decision (and escalation) is
	// recorded to, for calibrating thresholds against outcomes. A pack
	// instance sets it for its pack's decide steps with
	// `packs.<name>.decide.observe`.
	Observe string `yaml:"observe,omitempty"`
}

// EscalateSpec is a decide step's opt-in second opinion.
type EscalateSpec struct {
	// When is an expression over this step's answers (the question names are
	// its roots: `refuted.noul >= 0.5 && refuted.noul < 0.8`). It is the
	// uncertain zone relative to the pack's own decision boundary, which is
	// why it is an expression and not one confidence number.
	When string `yaml:"when,omitempty"`
	// To is the tier (or inline fleet) the second opinion comes from. Unset
	// means the next candidate in the step's own ranked list.
	To ModelSpec `yaml:"to,omitempty"`
	// Max bounds the escalation hops. Unset is 1.
	Max int `yaml:"max,omitempty"`
}

// maxEscalationHops is the ceiling on escalate.max: every hop is another
// model call, and a chain past a handful is a cost bug, not a policy.
const maxEscalationHops = 5

// ProtocolOrDefault is the step's protocol, defaulting to system_one/v1.
func (d *DecideSpec) ProtocolOrDefault() string {
	if d == nil || strings.TrimSpace(d.Protocol) == "" {
		return systemone.ProtocolV1
	}
	return strings.TrimSpace(d.Protocol)
}

// Hops is the escalation hop bound: 0 when escalation is off, else Max
// defaulted to 1.
func (e *EscalateSpec) Hops() int {
	if e == nil {
		return 0
	}
	if e.Max <= 0 {
		return 1
	}
	return e.Max
}

// supportedDecideProtocols are the contract versions conductor speaks.
var supportedDecideProtocols = map[string]bool{systemone.ProtocolV1: true}

// validateDecideStep checks a decide: step. It runs inside validateStep once
// the step is known to be the decide form.
func validateDecideStep(w string, s Step, c *Config) error {
	d := s.Decide
	if p := d.ProtocolOrDefault(); !supportedDecideProtocols[p] {
		return fmt.Errorf("config: %s: decide.protocol %q is not supported (supported: %s)", w, p, systemone.ProtocolV1)
	}
	if d.State == nil {
		return fmt.Errorf("config: %s: decide.state is required — it is everything the decision sees", w)
	}
	if str, ok := d.State.(string); ok && strings.TrimSpace(str) == "" {
		return fmt.Errorf("config: %s: decide.state is empty", w)
	}
	if err := d.Questions.Validate(); err != nil {
		return fmt.Errorf("config: %s: decide.questions: %w", w, err)
	}
	if d.Default != nil {
		if _, err := systemone.DefaultAnswers(d.Questions, d.Default); err != nil {
			return fmt.Errorf("config: %s: decide.default: %w", w, err)
		}
	}
	if e := d.Escalate; e != nil {
		if strings.TrimSpace(e.When) == "" {
			return fmt.Errorf("config: %s: decide.escalate needs `when:` — the uncertain zone that earns a second opinion", w)
		}
		if e.Max < 0 || e.Max > maxEscalationHops {
			return fmt.Errorf("config: %s: decide.escalate.max must be 1..%d (got %d)", w, maxEscalationHops, e.Max)
		}
		if e.To.Set() {
			if err := validateFleet(w+" decide.escalate.to", e.To); err != nil {
				return err
			}
		}
	}
	if d.Observe != "" && c != nil {
		if _, ok := c.Stores[d.Observe]; !ok {
			return fmt.Errorf("config: %s: decide.observe: unknown store %q (it names a SQL stores: entry)", w, d.Observe)
		}
	}
	if bad := decideForbiddenFields(s); len(bad) > 0 {
		return fmt.Errorf("config: %s: a decide step runs restricted — no checkout, no tools, one turn — so it takes no %s. A decision that needs to explore the repo is a `type: agent` step", w, strings.Join(bad, ", "))
	}
	return nil
}

// decideForbiddenFields lists the agent-behavior fields set on a decide step.
// Each would widen what the decision can do or see beyond `state`.
func decideForbiddenFields(s Step) []string {
	var bad []string
	add := func(set bool, name string) {
		if set {
			bad = append(bad, name)
		}
	}
	add(s.Agent != "", "agent:")
	add(s.Prompt != "", "prompt:")
	add(s.Checkout != "", "checkout:")
	add(len(s.OutputSchema) > 0, "output_schema:")
	add(s.Background, "background:")
	add(s.Handoff != "", "handoff:")
	add(len(s.Command) > 0, "command:")
	add(s.WorkDir != "", "workdir:")
	add(len(s.Env) > 0, "env:")
	add(s.Mode != "", "mode:")
	add(s.Thinking != "", "thinking:")
	add(!s.Workspace.IsZero(), "workspace:")
	add(s.Guidance != nil, "guidance:")
	add(s.Memory != nil, "memory:")
	add(s.Session != nil, "session:")
	add(s.Skill != nil, "skill:")
	add(s.Isolation != nil, "isolation:")
	add(s.Gate != nil, "gate:")
	add(s.Team != nil, "team:")
	add(s.ExpectPush, "expect_push:")
	add(s.Watch != nil, "watch:")
	add(s.IdleTimeout > 0, "idle_timeout:")
	add(s.EscalateTo != "", "escalate_to:")
	add(s.Host != "" || s.SSH != nil, "host:/ssh:")
	return bad
}

// PackDecide is a pack instance's `decide:` block: settings applied to every
// decide step the pack ships, lowered onto each at instantiation.
type PackDecide struct {
	// Observe records this pack's decisions to one of YOUR SQL stores
	// (a global stores: name, not a pack binding).
	Observe string `yaml:"observe,omitempty"`
	// Escalate: false turns off every escalate: the pack declares — the
	// consumer's cost kill-switch. Unset leaves the pack's declarations.
	Escalate *bool `yaml:"escalate,omitempty"`
}

// lowerPackDecide applies a pack instance's decide settings to one decide
// step. Observe fills only a step that names no store of its own;
// escalate: false removes the pack's escalation outright. The rewriter calls
// it per step (it already walks nested steps).
func lowerPackDecide(pd *PackDecide, s *Step) {
	if pd == nil || s.Decide == nil {
		return
	}
	if pd.Observe != "" && s.Decide.Observe == "" {
		s.Decide.Observe = pd.Observe
	}
	if pd.Escalate != nil && !*pd.Escalate {
		s.Decide.Escalate = nil
	}
}

// DecisionLaunch marks the synthesized agent step a decide: step runs on an
// AGENT runtime (the adapter path), and carries the adapter's rendering in
// parts, so a runtime that can run a lean decision session (no tools, its
// own system prompt, native structured output) uses them instead of
// launching a full agent. It is never read from YAML.
//
// Its presence also means the prompt is LITERAL: Document already contains
// the rendered state (a PR diff, a comment body — attacker-controllable
// text), so it must never go through prompt templating again. A second
// render would evaluate any {{…}} the text carries against the dispatch's
// template data, which includes credentials.
type DecisionLaunch struct {
	// System is the adapter's system prompt.
	System string
	// Document is the questions and the escaped state — the user turn.
	Document string
	// Schema is the output schema the reply must match.
	Schema map[string]any
}
