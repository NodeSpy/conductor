package core

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	sdk "github.com/NodeSpy/conductor/pkg/plugin"
)

// The engine's view of a trigger's declared semantics
// (docs/design/plugin-contract.md §2.2). Every engine behavior that used to
// key on a kind's NAME keys on one of these instead, so a plugin adding an
// event with any of these behaviors needs no conductor change.

// SemanticsFallback, when set, supplies semantics for a trigger that carries
// none. It exists for TESTS that build triggers by kind name; production
// triggers carry their semantics from the source adapter, and production
// never sets this.
var SemanticsFallback func(Trigger) *sdk.EventSemantics

var noSemantics = &sdk.EventSemantics{}

// Semantics is the trigger's declared semantics, never nil.
func (t Trigger) Semantics() *sdk.EventSemantics {
	if t.Sem != nil {
		return t.Sem
	}
	if SemanticsFallback != nil {
		if s := SemanticsFallback(t); s != nil {
			return s
		}
	}
	return noSemantics
}

// ClosesTarget: the event is terminal for its target.
func (t Trigger) ClosesTarget() bool { return t.Semantics().ClosesTarget != nil }

// BoundToTarget: a run this event starts dies with its target.
func (t Trigger) BoundToTarget() bool { return t.Semantics().BoundToTarget }

// LevelTriggered: the condition is external state a poll re-derives — never
// recorded done, skipped while an agent is live on the target.
func (t Trigger) LevelTriggered() bool {
	c := t.Semantics().Completion
	return c != nil && c.Level != nil
}

// RearmOnRevision: a parked level-triggered target re-engages when its
// revision changes.
func (t Trigger) RearmOnRevision() bool {
	c := t.Semantics().Completion
	return c != nil && c.Level != nil && c.Level.RearmOn == "revision"
}

// NoAttemptCap: each event is a distinct item, outside the per-revision
// attempt cap.
func (t Trigger) NoAttemptCap() bool {
	a := t.Semantics().Attempts
	return a != nil && a.Cap == "none"
}

// Interactive: the event takes the high-priority dispatch lane.
func (t Trigger) Interactive() bool { return t.Semantics().Priority == "interactive" }

// Feedback: feedback on work in flight (may adopt an open workspace).
func (t Trigger) Feedback() bool { return t.Semantics().Feedback }

// VerificationFailed: the target's revision failed verification.
func (t Trigger) VerificationFailed() bool { return t.Semantics().VerificationFailed }

// Cursor is the trigger's monotonic cursor: its id (ok=false when the event
// declares none or the fact is absent or not a positive integer) and its
// stream (the declared template rendered over the facts; "" when none).
func (t Trigger) Cursor() (id int64, stream string, ok bool) {
	c := t.Semantics().Cursor
	if c == nil {
		return 0, "", false
	}
	id, ok = intFact(fact(t, c.ID))
	if !ok || id <= 0 {
		return 0, "", false
	}
	return id, renderFacts(c.Stream, t.Context), true
}

// AuthorLogin is the event author's login ("" when undeclared or absent).
func (t Trigger) AuthorLogin() string {
	a := t.Semantics().Author
	if a == nil {
		return ""
	}
	s, _ := fact(t, a.Login).(string)
	return s
}

// AuthorAutomated reports whether the event's author is automated.
func (t Trigger) AuthorAutomated() bool {
	a := t.Semantics().Author
	if a == nil || a.Automated == "" {
		return false
	}
	b, _ := fact(t, a.Automated).(bool)
	return b
}

// TargetLabels are the target's labels, from the fact the event declares.
func (t Trigger) TargetLabels() []string {
	f := t.Semantics().Labels
	if f == "" {
		return nil
	}
	var out []string
	switch v := fact(t, f).(type) {
	case []string:
		out = v
	case []any:
		for _, x := range v {
			if s, ok := x.(string); ok {
				out = append(out, s)
			}
		}
	}
	return out
}

// PrivateFacts are facts never rendered into an agent prompt.
func (t Trigger) PrivateFacts() []string { return t.Semantics().Private }

// SecretFacts are facts never persisted and redacted everywhere.
func (t Trigger) SecretFacts() []string { return t.Semantics().Secret }

// CloseOutcome is a closing event's outcome (accepted | rejected; "" when
// the event declares none).
func (t Trigger) CloseOutcome() string {
	c := t.Semantics().ClosesTarget
	if c == nil || c.Outcome == nil {
		return ""
	}
	if b, _ := fact(t, c.Outcome.Fact).(bool); b {
		return c.Outcome.True
	}
	return c.Outcome.False
}

// Reverts are the sibling target numbers a closing event reverts, and
// whether the claim is corroborated.
func (t Trigger) Reverts() (numbers []int, corroborated bool) {
	c := t.Semantics().ClosesTarget
	if c == nil || c.Reverts == nil {
		return nil, false
	}
	if c.Reverts.Corroborated != "" {
		corroborated, _ = fact(t, c.Reverts.Corroborated).(bool)
	}
	switch v := fact(t, c.Reverts.Fact).(type) {
	case []int:
		numbers = v
	case []any:
		for _, x := range v {
			if n, ok := intFact(x); ok {
				numbers = append(numbers, int(n))
			}
		}
	}
	return numbers, corroborated
}

func intFact(v any) (int64, bool) {
	switch n := v.(type) {
	case int:
		return int64(n), true
	case int64:
		return n, true
	case float64:
		if n == float64(int64(n)) {
			return int64(n), true
		}
	case string:
		i, err := strconv.ParseInt(n, 10, 64)
		return i, err == nil
	}
	return 0, false
}

// renderFacts substitutes {{.fact}} references (plain fact names only) in a
// declaration's template — the subset declarations use for keys and
// streams. A reference to an absent fact renders empty.
func renderFacts(tmpl string, facts map[string]any) string {
	if !strings.Contains(tmpl, "{{") {
		return tmpl
	}
	var b strings.Builder
	for {
		i := strings.Index(tmpl, "{{")
		if i < 0 {
			b.WriteString(tmpl)
			return b.String()
		}
		j := strings.Index(tmpl[i:], "}}")
		if j < 0 {
			b.WriteString(tmpl)
			return b.String()
		}
		b.WriteString(tmpl[:i])
		name := strings.TrimPrefix(strings.TrimSpace(tmpl[i+2:i+j]), ".")
		if v, ok := LookupFact(facts, name); ok && v != nil {
			b.WriteString(fmt.Sprint(v))
		}
		tmpl = tmpl[i+j+2:]
	}
}

// RenderFacts is renderFacts for other packages rendering a declaration's
// templates (target keys, checkout refs, credential args).
func RenderFacts(tmpl string, facts map[string]any) string { return renderFacts(tmpl, facts) }

// DeclaredArgs renders a declaration's verb args over an event's facts: a
// template naming one fact passes that fact's value as is (an integer stays
// an integer); other templates render to strings; a literal parses as JSON
// when it can (true, 3), else stays a string.
func DeclaredArgs(args map[string]string, facts map[string]any) map[string]any {
	out := make(map[string]any, len(args))
	for k, v := range args {
		if name := sdk.FactName(v); name != v {
			out[k] = facts[name]
			continue
		}
		if strings.Contains(v, "{{") {
			out[k] = renderFacts(v, facts)
			continue
		}
		var lit any
		if json.Unmarshal([]byte(v), &lit) == nil {
			out[k] = lit
		} else {
			out[k] = v
		}
	}
	return out
}

// HasSemantics reports whether the trigger's event declares any semantics.
// An event from a plugin that declares none keeps the target-shaped
// behavior older plugins were built against.
func (t Trigger) HasSemantics() bool { return t.Semantics() != noSemantics }

// CheckoutSpec is a target's declared checkout, rendered over its facts.
type CheckoutSpec struct {
	Remote     string            // the repository's git URL
	FetchRef   string            // the ref holding the target's code ("" = the base branch)
	PushBranch string            // where the work is pushed ("" = a fresh branch)
	Hints      map[string]string // passed to the agent runtime unread
}

// Checkout is the trigger's declared checkout; ok=false when its event
// declares none (no workspace checkout: a synthetic target).
func (t Trigger) Checkout() (CheckoutSpec, bool) {
	c := t.Semantics().Checkout
	if c == nil {
		return CheckoutSpec{}, false
	}
	facts := t.Facts()
	spec := CheckoutSpec{
		Remote:   renderFacts(c.Remote, facts),
		FetchRef: renderFacts(c.FetchRef, facts),
	}
	if c.PushBranch != "" {
		spec.PushBranch, _ = facts[c.PushBranch].(string)
	}
	if len(c.RuntimeHints) > 0 {
		spec.Hints = map[string]string{}
		for k, v := range c.RuntimeHints {
			spec.Hints[k] = renderFacts(v, facts)
		}
	}
	return spec, true
}

// Facts is the view a declaration's templates render over: the base facts
// every target carries (what templates have always seen — repo, owner,
// name, pr, issue, number, head, base, url), with the event's own facts on
// top.
func (t Trigger) Facts() map[string]any {
	out := map[string]any{
		"repo": t.Target.Repo, "owner": t.Target.Owner, "name": t.Target.Name,
		"pr": t.Target.PR, "issue": t.Target.Issue, "number": t.Target.Number,
		"head": t.Target.HeadSHA, "base": t.Target.BaseRef, "url": t.Target.HTMLURL,
	}
	for k, v := range out {
		if v == "" || v == 0 {
			delete(out, k)
		}
	}
	for k, v := range t.Context {
		out[k] = v
	}
	return out
}

// TargetScope is the value the event's declared target scope gives
// dimension dim (target.scope: [{dimension, fact}]) — "" when it declares
// none for dim, and always "" for a target the platform did not assign: a
// sender-chosen target implies no scope.
func (t Trigger) TargetScope(dim string) string {
	ts := t.Semantics().Target
	if ts == nil || !t.TargetTrusted {
		return ""
	}
	for _, sc := range ts.Scope {
		if sc.Dimension != dim {
			continue
		}
		if v, ok := LookupFact(t.Facts(), sc.Fact); ok {
			if s, ok := v.(string); ok {
				return s
			}
		}
	}
	return ""
}

// DeclaresTargetScope reports whether the event declares its target's scope
// at all (an event declaring none keeps the legacy repo-from-target rule).
func (t Trigger) DeclaresTargetScope() bool {
	ts := t.Semantics().Target
	return ts != nil && len(ts.Scope) > 0
}

// LookupFact reads a fact by name, walking a dotted path through nested maps
// ("chat.user" → facts["chat"]["user"]); a flat key of that exact name
// wins.
func LookupFact(facts map[string]any, name string) (any, bool) {
	if v, ok := facts[name]; ok {
		return v, true
	}
	var cur any = facts
	for _, part := range strings.Split(name, ".") {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		if cur, ok = m[part]; !ok {
			return nil, false
		}
	}
	return cur, true
}

// fact reads a declared fact off the trigger's context (dotted paths walk
// nested maps).
func fact(t Trigger, name string) any {
	v, _ := LookupFact(t.Context, name)
	return v
}
