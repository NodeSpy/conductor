package config

import (
	"fmt"
	"sort"
	"strings"
)

// Quality-gate validation (#36 §16). Structure enforced at load:
//
//   - `checks:` entries are single agent/command/code/verb steps — the forms
//     with a crisp pass/fail reading. Workflow calls, background agents,
//     fan-out, and nested gates are rejected.
//   - `gate:` blocks name existing checks, and step-level gates sit only on
//     foreground agent steps (a background hand-off is human-reviewed by
//     definition; other forms have no "proposed change" to check).

// validateChecks validates the top-level checks: map.
func (c *Config) validateChecks() error {
	for name, chk := range c.Checks {
		if strings.TrimSpace(name) == "" {
			return fmt.Errorf("config: checks: empty check name")
		}
		if strings.Contains(name, ":") {
			return fmt.Errorf("config: checks: %q — check names may not contain ':' (reserved for built-in ephemeral checks like team:critic)", name)
		}
		w := "check " + name
		// The check-specific shape rules run FIRST so their messages win over
		// the generic step validation's.
		switch f := chk.Form(); f {
		case "agent", "command", "code", "verb":
		default:
			return fmt.Errorf("config: %s: a gate check must be an agent/command/code/verb step (got %s form) — it needs a crisp pass/fail reading", w, orIndeterminate(f))
		}
		if chk.Background {
			return fmt.Errorf("config: %s: a gate check cannot be a background agent (the gate waits for its verdict)", w)
		}
		if chk.Gate != nil {
			return fmt.Errorf("config: %s: a gate check cannot carry its own gate:", w)
		}
		if chk.ForEach != "" || chk.Parallel != nil {
			return fmt.Errorf("config: %s: a gate check cannot fan out (for_each/parallel) — one check, one verdict", w)
		}
		if err := validateStep(w, chk, c); err != nil {
			return err
		}
	}
	return nil
}

func orIndeterminate(f string) string {
	if f == "" {
		return "indeterminate"
	}
	return f
}

// validateGate checks one gate: block's shape against the checks: map.
func (c *Config) validateGate(where string, g *GateSpec) error {
	if g == nil {
		return nil
	}
	if len(g.Run) == 0 {
		return fmt.Errorf("config: %s: gate needs `run: [<check>, …]`", where)
	}
	for _, name := range g.Run {
		if strings.TrimSpace(name) == "" {
			return fmt.Errorf("config: %s: gate.run: empty check name", where)
		}
		if _, ok := c.Checks[name]; !ok {
			return fmt.Errorf("config: %s: gate.run names unknown check %q (defined: %s)", where, name, c.checkNames())
		}
	}
	switch g.Require {
	case "", "pass":
	default:
		return fmt.Errorf("config: %s: gate.require must be \"pass\" (the default), got %q", where, g.Require)
	}
	if g.MaxRevisions != nil && *g.MaxRevisions < 0 {
		return fmt.Errorf("config: %s: gate.max_revisions must be >= 0", where)
	}
	return nil
}

// validateStepGate enforces gate placement on one step.
func (c *Config) validateStepGate(w string, s Step) error {
	if s.Gate == nil {
		return nil
	}
	// On a team step the gate applies to the RECONCILER's merged change; the
	// workers' gate lives inside team: (gate + critic).
	if s.Form() != "agent" && s.Form() != "team" {
		return fmt.Errorf("config: %s: gate: applies to agent (or team) steps only (this is a %s step) — a gate checks an agent's PROPOSED change", w, orIndeterminate(s.Form()))
	}
	if s.Background {
		return fmt.Errorf("config: %s: gate: cannot apply to a background agent — the interactive hand-off IS its review; gate foreground steps", w)
	}
	return c.validateGate(w, s.Gate)
}

// checkNames lists defined checks, sorted, for error messages.
func (c *Config) checkNames() string {
	if len(c.Checks) == 0 {
		return "none — define a top-level checks: map"
	}
	names := make([]string, 0, len(c.Checks))
	for n := range c.Checks {
		names = append(names, n)
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}

// validateTeam checks a team: step's shape (#36 §19).
func (c *Config) validateTeam(w string, ts *TeamSpec) error {
	if ts == nil {
		return nil
	}
	roles := []struct{ role, name string }{
		{"planner", ts.Planner}, {"worker", ts.Worker},
		{"critic", ts.Critic}, {"reconcile", ts.Reconcile},
	}
	for _, r := range roles {
		if r.name == "" {
			if r.role == "planner" || r.role == "worker" {
				return fmt.Errorf("config: %s: team needs `%s:` (a steps: template)", w, r.role)
			}
			continue
		}
		if _, ok := c.Steps[r.name]; !ok {
			return fmt.Errorf("config: %s: team.%s names unknown steps: template %q (defined: %s)", w, r.role, r.name, sortedKeys(c.Steps))
		}
	}
	if ts.MaxWorkers < 0 || ts.MaxWorkers > 16 {
		return fmt.Errorf("config: %s: team.max_workers must be 1..16 (0 = default %d)", w, DefaultTeamMaxWorkers)
	}
	return c.validateGate(w+" team", ts.Gate)
}
