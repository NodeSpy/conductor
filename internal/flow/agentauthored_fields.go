package flow

import (
	"fmt"

	"github.com/NodeSpy/conductor/internal/config"
)

// This file is the ONE place that says which step fields an agent-authored
// step may not carry. Three rounds of review established the failure mode: a
// new field lands on config.Step, the guard is never taught about it, and the
// agent can set it. `skill:` was the third such field.
//
// So the list lives here, once, and every entry path consults it:
//
//	guardPlan          — plan admission, rejects loudly
//	ValidatePlanSteps  — validates independently, so no plan path admits one
//	sanitizeAgentAuthoredStep — strips at DISPATCH construction, which is what
//	                     catches fields that arrive after admission (a team
//	                     role reference merging a config step's Skill in)
//	dispatch.BuildToolServer / SkillEnv — refuse at the read point
//
// TestNoAgentAuthoredPathGrantsSkill is the meta-test over that set.

// forbiddenAgentAuthoredField reports the first field an agent-authored step
// may not set, with the reason, or "" if the step is clean.
//
// Each entry is a capability the OPERATOR owns. An agent that could set it
// would be widening its own authority with output it wrote itself.
func forbiddenAgentAuthoredField(s *config.Step) (field, why string) {
	switch {
	case s.Gate != nil:
		// A step's gate wins over the inherited trigger/workflow default, so
		// an emitted `gate: {run: []}` lets the agent approve its own output.
		return "gate:", "the inherited trigger/workflow gate is the only gate path for agent output"
	case s.Team != nil && s.Team.Gate != nil:
		return "team.gate:", "the operator's configuration owns the checks on agent output"
	case s.Background:
		// A backgrounded agent runs outside the synchronous run, so the gate
		// can't hold output that never returns.
		return "background:", "a backgrounded agent runs outside the gate on agent output"
	case s.Handoff != "":
		return "handoff:", "the review channel for agent output is the operator's to configure"
	case s.Skill != nil:
		// The one that motivated this file. `skill:` is a CAPABILITY GRANT:
		// it mints a broker claim/session token naming the verbs the agent
		// may call back through. An agent that writes its own `skill:` block
		// grants itself the verb set — the plan surface's allow/approve lists
		// would be checked against a step whose authority the step chose.
		return "skill:", "a skill grant is a capability the operator issues, never one the agent writes for itself"
	}
	return "", ""
}

// checkAgentAuthoredFields returns an error naming the offending field, or nil.
// where is the caller's positional label ("plan[0](fix)").
func checkAgentAuthoredFields(where string, s *config.Step) error {
	field, why := forbiddenAgentAuthoredField(s)
	if field == "" {
		return nil
	}
	return fmt.Errorf("%s: agent-authored steps may not set %s — %s", where, field, why)
}

// sanitizeAgentAuthoredStep strips the INHERITABLE grants from a step about to
// be dispatched as agent-authored.
//
// This is the belt to the guards' suspenders, and it is not redundant: the
// guards run at plan ADMISSION, while a team role reference merges a config
// step into the role step afterwards (Runner.roleStep → MergeStepInto). That
// merge carries the referenced step's fields wholesale, so an agent-authored
// team role pointed at a config step with a `skill:` block would inherit that
// step's grant without any guard seeing it. Stripping here closes the window
// regardless of how the field arrived.
//
// Isolation goes too: it selects the sandbox/network profile the launch runs
// under, so inheriting it through a role reference would let an agent-authored
// worker pick up a config step's relaxed network policy and escape the
// deny-by-default egress that #36 §15 gives agent-authored dispatch.
//
// Gate, Background and Handoff are deliberately NOT stripped here even though
// an agent-authored step may not SET them. By dispatch time those fields hold
// RESOLVED operator state, not agent input: the team runner puts the trigger's
// own default gate into the reconciler's Step.Gate (team.go), so nulling it
// would delete the operator's gate rather than the agent's. Their enforcement
// point is admission — checkAgentAuthoredFields, in both guards — which is the
// only place the agent can be the author of the value.
func sanitizeAgentAuthoredStep(s *config.Step) {
	s.Skill = nil
	s.Isolation = nil
}
