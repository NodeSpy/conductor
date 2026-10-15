package main

import "github.com/NodeSpy/conductor/internal/skill"

// mayRunStep reports whether a skill identity may execute an agent-authored
// step through the run_step live tool.
//
// run_step is conductor's own orchestration surface, governed daemon-wide by
// policy.agent_authored and deliberately SEPARATE from skill.verbs (the doc on
// config.SkillPolicy: "conductor's own orchestration goes through run_step under
// policy.agent_authored"). So this gate does NOT intersect with skill.verbs —
// that would fight the design. It excludes only an AUTHENTICATION-ONLY identity:
// one with neither dispatch provenance (Dispatch == "") nor any grant
// (Policy.Verbs empty). Those are the agent sandbox's ad-hoc directory ("dir:")
// and scratch identities — sessions conductor did not launch as a dispatch,
// whose step.done is already refused for the same reason. A real
// conductor-launched dispatch (Dispatch set) or a configured skill agent (verbs
// granted) is unaffected, so agent-authored workflows keep working.
func mayRunStep(id skill.Identity) (bool, string) {
	if id.Dispatch == "" && len(id.Policy.Verbs) == 0 {
		return false, "run_step: refused for an authentication-only identity (an ad-hoc sandbox session, not a conductor-launched dispatch); agent-authored steps run under a real dispatch"
	}
	return true, ""
}
