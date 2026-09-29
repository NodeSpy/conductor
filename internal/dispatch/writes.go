package dispatch

import (
	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/targets"
)

// GlobalIsolation is the config's top-level isolation: block, wired at boot
// (the base of every dispatch's write policy).
var GlobalIsolation *config.IsolationConfig

// reviewKinds are trigger kinds whose agents review rather than fix.
var reviewKinds = map[string]bool{"review_requested": true, "self_review": true}

// EffectiveWrites decides a dispatch's write policy (#154 §5) from its step
// and the operator's runtime / top-level isolation blocks (nil entries
// skipped). Writes are bound to the dispatch's own target; review steps
// write nothing by default — a decide step, a step with an output schema, a
// checkout-less step, or a review trigger — unless `expect_push:` or an
// explicit `writes:` says otherwise. A pack step's widening is capped by the
// operator's own writes block.
func EffectiveWrites(req Request, runtimeIso *config.IsolationConfig) (readOnly bool, eff *config.WritesPolicy) {
	var op *config.WritesPolicy
	for _, l := range []*config.IsolationConfig{GlobalIsolation, runtimeIso} {
		if l != nil && l.Writes != nil {
			op = l.Writes
		}
	}
	var st *config.WritesPolicy
	if req.Step.Isolation != nil {
		st = req.Step.Isolation.Writes
	}
	eff = op
	if st != nil {
		eff = st
		if req.Step.FromPack && st.Widens() {
			c := *st
			c.CreatePR = st.CreatePR && op != nil && op.CreatePR
			c.CreateIssue = st.CreateIssue && op != nil && op.CreateIssue
			c.OtherTargets = st.OtherTargets && op != nil && op.OtherTargets
			c.Merge = st.Merge && op != nil && op.Merge
			c.Branches = nil
			if op != nil {
				for _, b := range st.Branches {
					for _, o := range op.Branches {
						if b == o {
							c.Branches = append(c.Branches, b)
						}
					}
				}
			}
			eff = &c
		}
	}
	if eff != nil && eff.ReadOnly {
		return true, eff
	}
	if eff != nil && (eff.Target || eff.Widens()) {
		return false, eff
	}
	s := req.Step
	review := s.DecisionLaunch != nil || len(s.OutputSchema) > 0 || s.Checkout == "none" || reviewKinds[req.Trigger.Kind]
	return review && !s.ExpectPush, eff
}

// WritePolicyFor renders EffectiveWrites as the targets policy the verb and
// jail bindings check against.
func WritePolicyFor(req Request, runtimeIso *config.IsolationConfig) targets.WritePolicy {
	ro, w := EffectiveWrites(req, runtimeIso)
	p := targets.WritePolicy{ReadOnly: ro}
	if w != nil {
		p.CreatePR, p.CreateIssue, p.OtherTargets, p.Branches, p.Merge = w.CreatePR, w.CreateIssue, w.OtherTargets, w.Branches, w.Merge
	}
	return p
}
