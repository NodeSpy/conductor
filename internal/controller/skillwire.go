package controller

import (
	"strconv"

	"github.com/NodeSpy/conductor/internal/memory"
	"github.com/NodeSpy/conductor/internal/skill"
)

// The conductor-memory MCP tool server (memory + run_step + the skill's verb
// tools and secret broker) is injected per dispatch by every controller whose
// runtime can accept MCP servers at launch. This file is the SHARED wiring —
// what to launch, with which provenance flags, and the one-shot skill claim
// (#122 R1) — so the ACP and opencode controllers assemble the same server
// instead of copy-pasting it. Runtimes with no MCP launch surface (paseo,
// agent-deck, cli) cannot carry it; `conductor validate` warns when a skill:
// profile targets one (see flow.SkillWarnings).

// toolServerSpec is one dispatch's conductor tool server: the subprocess to
// launch, its argv, and the environment to set on it.
type toolServerSpec struct {
	Command string
	Args    []string
	// Env carries the ONE-SHOT skill claim code (CONDUCTOR_SKILL_CLAIM) for
	// a skill-enabled profile — environment, never argv, which any same-user
	// process can read from a process listing. Empty for profiles without
	// skill:.
	Env map[string]string
}

// buildToolServer assembles the tool server for one dispatch, or nil when
// there is nothing to inject: no tool command published at boot (no memory:
// section and no skill-enabled profile), or a remote (host:) session — the
// daemon's socket doesn't exist on that box.
//
// When the dispatched profile carries a skill: block (#36 §12), this is
// where the daemon binds identity SERVER-SIDE: it mints a one-shot claim
// code mapped to the real profile and its policy in the skill broker. The
// subprocess exchanges the code (single-use, short TTL) for the session
// token over the socket at startup, and the broker binds the session to that
// process's kernel peer credentials. The --agent/--repo flags remain
// provenance labels for memory writes; the broker never trusts them.
func buildToolServer(spec Spec, controllerHost string) *toolServerSpec {
	argv := memory.ToolCommand()
	if len(argv) == 0 || resolveHost(controllerHost, spec.Request.Profile.Host) != "" {
		return nil
	}
	args := append([]string(nil), argv[1:]...)
	if a := spec.Request.Action.Agent; a != "" {
		args = append(args, "--agent", a)
	}
	if repo := spec.Request.Trigger.Target.Repo; repo != "" {
		args = append(args, "--repo", repo)
	}
	if kind := spec.Request.Trigger.Kind; kind != "" {
		args = append(args, "--trigger", kind)
	}
	if n := spec.Request.Trigger.Target.Number; n > 0 {
		args = append(args, "--number", strconv.Itoa(n))
	}
	out := &toolServerSpec{Command: argv[0], Args: args}
	if b := skill.Active(); b != nil && spec.Request.Profile.Skill != nil {
		claim, err := b.MintClaim(skill.Identity{
			Agent:   spec.Request.Action.Agent,
			Repo:    spec.Request.Trigger.Target.Repo,
			Trigger: spec.Request.Trigger.Kind,
			Number:  spec.Request.Trigger.Target.Number,
			Policy:  *spec.Request.Profile.Skill,
		})
		if err == nil {
			out.Env = map[string]string{"CONDUCTOR_SKILL_CLAIM": claim}
		}
	}
	return out
}
