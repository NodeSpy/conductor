package config

import (
	"fmt"
	"sort"
	"strings"
)

// The step surface that replaced `agents:` (docs/design/agents-removal.md).
//
// Everything a named agent profile used to carry now lives on the step that
// dispatches the work, and everything that used to be keyed BY an agent name
// is keyed by the step's identity (identity.go). This file holds the walker
// every cross-cutting pass shares and the validation those fields need.

// WalkSteps visits every step in the config — trigger steps, workflow
// steps, and named checks — with its identity scope and slot, recursing
// into parallel branches and compensations.
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
}

func walkStepList(scope IdentityScope, steps []Step, fn func(IdentityScope, int, *Step)) {
	for i := range steps {
		walkStep(scope, i, &steps[i], fn)
	}
}

func walkStep(scope IdentityScope, slot int, s *Step, fn func(IdentityScope, int, *Step)) {
	fn(scope, slot, s)
	if s.Parallel != nil {
		// Each branch is its own scope. Walking them all with the parent's
		// scope and a slot restarting at 0 gave branch-0 steps in different
		// branches ONE identity — one session pool, one track record, for
		// work that is deliberately concurrent and unrelated.
		for bi := range s.Parallel.Branches {
			walkStepList(BranchScope(scope, s.slotLabel(slot), bi), s.Parallel.Branches[bi], fn)
		}
	}
	if s.Compensate != nil {
		// The undo is its own work with its own outcome; walking it with
		// the parent's scope AND slot gave it the parent's identity.
		walkStep(CompensateScope(scope, s.slotLabel(slot)), 0, s.Compensate, fn)
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
// warnSharedAnchorIDs surfaces a shared base that carries an `id:`.
//
// Reuse copies fields, `id:` among them — so two steps merging one anchor
// that sets `id: review` both land on the SAME structural identity, and
// silently share a memory namespace, session pool, and track record. That
// is occasionally intended (`name:` is the explicit way to say it) and
// usually a surprise, so it is a notice, not an error.
func (c *Config) warnSharedAnchorIDs() {
	byID := map[string][]string{}
	c.WalkSteps(func(scope IdentityScope, slot int, s *Step) {
		if strings.TrimSpace(s.ID) == "" || strings.TrimSpace(s.Name) != "" {
			return // no id to collide, or an explicit name already decides
		}
		byID[s.Identity(scope, slot)] = append(byID[s.Identity(scope, slot)], StepLabel(scope, slot, *s))
	})
	for id, where := range byID {
		if len(where) < 2 {
			continue
		}
		sort.Strings(where)
		c.packWarnings = append(c.packWarnings, fmt.Sprintf(
			"steps %s share the identity %q — they carry the same id: in the same scope, most likely from a shared base that sets one. They will share a memory namespace, session pool, and track record. Give each its own id:, or pin `name:` if sharing is what you meant",
			strings.Join(where, " and "), id))
	}
}

func (c *Config) validateSteps() error {
	c.warnSharedAnchorIDs()
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
