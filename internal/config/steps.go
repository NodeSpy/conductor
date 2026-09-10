package config

import (
	"fmt"
	"strings"
)

// The step surface that replaced `agents:` (docs/design/agents-removal.md).
//
// Everything a named agent profile used to carry now lives on the step that
// dispatches the work, and everything that used to be keyed BY an agent name
// is keyed by the step's identity (identity.go). This file holds the walker
// every cross-cutting pass shares and the validation those fields need.

// WalkSteps visits every step in the config — trigger steps, workflow steps,
// named checks, and the named steps of the `steps:` registry — with its
// identity scope and slot, recursing into parallel branches and
// compensations.
//
// The visitor gets a POINTER so a pass can rewrite in place; order is
// deterministic (triggers by position, maps by sorted key) so diagnostics are
// stable across runs.
func (c *Config) WalkSteps(fn func(scope IdentityScope, slot int, s *Step)) {
	for i := range c.Triggers {
		scope := ScopeForTrigger(c.Triggers[i], i)
		walkStepList(scope, c.Triggers[i].Steps, fn)
	}
	for _, name := range sortedNames(c.Workflows) {
		wf := c.Workflows[name]
		walkStepList(WorkflowScope(name), wf.Steps, fn)
	}
	for _, name := range sortedNames(c.Checks) {
		s := c.Checks[name]
		walkStep(CheckScope(name), 0, &s, fn)
		c.Checks[name] = s
	}
	for _, name := range sortedNames(c.Steps) {
		s := c.Steps[name]
		// A named step's identity scope is its own key: a team role that
		// resolves to it inherits that name, so the two agree.
		walkStep(IdentityScope{Kind: "step", Name: name}, 0, &s, fn)
		c.Steps[name] = s
	}
}

func walkStepList(scope IdentityScope, steps []Step, fn func(IdentityScope, int, *Step)) {
	for i := range steps {
		walkStep(scope, i, &steps[i], fn)
	}
}

func walkStep(scope IdentityScope, slot int, s *Step, fn func(IdentityScope, int, *Step)) {
	fn(scope, slot, s)
	if s.Parallel != nil {
		for bi := range s.Parallel.Branches {
			walkStepList(scope, s.Parallel.Branches[bi], fn)
		}
	}
	if s.Compensate != nil {
		walkStep(scope, slot, s.Compensate, fn)
	}
}

// StepLabel is a human-readable location for a step, for error messages.
func StepLabel(scope IdentityScope, slot int, s Step) string {
	where := scope.String()
	if where == "" {
		where = "step"
	}
	return where + " " + s.slotLabel(slot)
}

// validateSteps checks the behavior fields that moved off `agents:` onto the
// step — the same checks the agent-profile loop used to run, now anchored on
// the step that carries them.
func (c *Config) validateSteps() error {
	var firstErr error
	fail := func(err error) {
		if firstErr == nil {
			firstErr = err
		}
	}
	c.WalkSteps(func(scope IdentityScope, slot int, s *Step) {
		if firstErr != nil {
			return
		}
		where := StepLabel(scope, slot, *s)
		if s.Workspace != "" && s.Workspace != "local" && s.Workspace != "worktree" {
			fail(fmt.Errorf("config: %s: workspace must be local|worktree, got %q", where, s.Workspace))
			return
		}
		if s.Runtime != "" {
			_, isRuntime := c.Runtimes[s.Runtime]
			_, isController := c.Controllers[s.Runtime]
			if !isRuntime && !isController {
				fail(fmt.Errorf("config: %s: unknown runtime %q (defined: %s)", where, s.Runtime, c.runtimeNames()))
				return
			}
		}
		if s.Host != "" {
			if _, ok := c.Hosts[s.Host]; !ok {
				fail(fmt.Errorf("config: %s: unknown host %q (defined: %s)", where, s.Host, c.hostNames()))
				return
			}
		}
		if err := c.validateStepIsolation(where, *s); err != nil {
			fail(err)
			return
		}
		if err := c.validateStepSkillIsolation(where, *s); err != nil {
			fail(err)
			return
		}
		if err := c.validateStepSkill(where, *s); err != nil {
			fail(err)
			return
		}
		if err := validateStepSession(where, s.Session); err != nil {
			fail(err)
			return
		}
	})
	return firstErr
}

// validateStepSkill checks a step's `skill:` block.
func (c *Config) validateStepSkill(where string, s Step) error {
	if s.Skill == nil {
		return nil
	}
	switch s.Skill.SecretsVia {
	case "", "none", "env", "broker":
	default:
		return fmt.Errorf("config: %s: skill.secrets_via must be broker|env|none, got %q", where, s.Skill.SecretsVia)
	}
	if s.Skill.MaxCalls < 0 {
		return fmt.Errorf("config: %s: skill.max_calls must be >= 0, got %d", where, s.Skill.MaxCalls)
	}
	// allow_secrets only works through the broker: the broker refuses to issue
	// to a session whose secrets_via is not "broker" (default "none"). An
	// allow_secrets list without it is a silent footgun — the grants would
	// never resolve — so reject it.
	if len(s.Skill.AllowSecrets) > 0 {
		via := s.Skill.SecretsVia
		if via == "" {
			via = "none"
		}
		if via != "broker" {
			return fmt.Errorf("config: %s: skill.allow_secrets is set but skill.secrets_via is %q — the broker only issues secrets to a session with secrets_via: broker, so these grants would never resolve; set secrets_via: broker", where, via)
		}
	}
	for _, ref := range s.Skill.AllowSecrets {
		// The current model names a vault entry: "<vault>/<key>" (the key half
		// resolves at issue time — non-listable vaults can't be checked
		// statically). A bare name checks the retired named-secrets block.
		if vault, _, isVault := strings.Cut(ref, "/"); isVault {
			if _, ok := c.Vaults[vault]; !ok {
				return fmt.Errorf("config: %s: skill.allow_secrets %q names unknown vault %q (defined: %s)", where, ref, vault, c.vaultNames())
			}
		} else if _, ok := c.SecretRefs[ref]; !ok {
			return fmt.Errorf("config: %s: skill.allow_secrets names unknown secret %q — name a vault entry as \"<vault>/<key>\"", where, ref)
		}
	}
	return nil
}
