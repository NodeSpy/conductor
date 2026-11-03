package config

import (
	"fmt"
	"strings"
)

// CodeConfig is the OPTIONAL top-level `code:` block tuning the default
// isolation a LOCAL cli/host code step (including `run: go`) gets when it
// writes no `isolation:` of its own (see defaultCodeIsolation,
// internal/config/plugins.go, and applyCodeIsolationDefaults below).
type CodeConfig struct {
	// IsolationDefault selects the posture: "namespace" (the default, also
	// the empty value) synthesizes the least-privilege namespace jail — the
	// same confined-by-default treatment a pack's own shipped code already
	// gets — on every local cli/host/go step that declares no isolation: and
	// no `trust: full`. "none" restores the pre-existing bare-by-default
	// behavior for every such step, for a deployment that is not ready for
	// the stricter default (e.g. steps that reach the network with no
	// `isolation:` block of their own) while it migrates its configs to
	// declare isolation: explicitly where they need it loosened.
	IsolationDefault string `yaml:"isolation_default,omitempty"`
}

// CodeIsolationDefaultOn reports whether a local cli/host/go step with no
// isolation: of its own gets the synthesized namespace jail (the default:
// anything but the explicit opt-out `code: { isolation_default: none }`).
func (c *Config) CodeIsolationDefaultOn() bool {
	if c == nil {
		return true
	}
	return !strings.EqualFold(strings.TrimSpace(c.Code.IsolationDefault), "none")
}

func validateCodeConfig(c CodeConfig) error {
	switch strings.ToLower(strings.TrimSpace(c.IsolationDefault)) {
	case "", "namespace", "none":
		return nil
	default:
		return fmt.Errorf("config: code.isolation_default must be namespace|none, got %q", c.IsolationDefault)
	}
}

// applyCodeIsolationDefaults confines every LOCAL cli/host code step
// (including `run: go`) that still carries no isolation: after pack
// instantiation has run its own confined-by-default pass (§15) — i.e. every
// operator-authored one, since a pack-shipped step was already covered
// there. It is the same synthesized jail a pack step gets, for the same
// reason one level out: an operator-authored step's `command:`/`args:`/
// `env:` can be rendered from event/trigger data the operator does not fully
// control, so "the operator wrote this step" does not mean "the operator
// controls everything this step's argv can become" at run time.
//
// Skipped entirely when `code: { isolation_default: none }` opts the whole
// config out (the escape hatch for a deployment not yet ready for the
// stricter default) — every step then runs exactly as it did before this
// pass existed. Per-step opt-outs (an explicit isolation:, including
// `{mode: none}`, or `trust: full`) are respected the same way the pack path
// respects them; see IsolationWarnings for the boot-time warning listing
// which steps took one.
func (c *Config) applyCodeIsolationDefaults() {
	if !c.CodeIsolationDefaultOn() {
		return
	}
	c.WalkSteps(func(_ IdentityScope, _ int, s *Step) {
		if _, class := s.StepEngine(); class != EngineCLI && class != EngineHost {
			return
		}
		if s.Host != "" || s.SSH != nil {
			return // a remote code step has no local wrap to apply
		}
		if s.Isolation != nil || s.Trust == "full" {
			return // explicit isolation (including {mode: none}), or a deliberate opt-out
		}
		s.Isolation = defaultCodeIsolation()
		s.IsolationDefaulted = true
	})
}
