package flow

import (
	"fmt"
	"sort"
	"strings"

	"github.com/NodeSpy/conductor/internal/config"
)

// DeprecationWarnings returns load-time lint WARNINGS (never errors): the
// deprecated patterns `conductor validate` should flag while back-compat
// keeps them working.
//
// Today that is one pattern (#36 §12): secret material templated into an
// AGENT step's env: — {{.secrets.*}}, {{.vaults.*}}, or a {{ vault … }}
// call. It still works, but it places the raw value in the external
// runtime's environment for the whole run. The replacement is the skill
// surface: verbs as tools (no secret enters the session), or skill
// secrets_via: broker with a {{secret "name"}} handle.
func DeprecationWarnings(cfg *config.Config) []string {
	if cfg == nil {
		return nil
	}
	var warns []string
	seen := map[string]bool{}
	warn := func(w string) {
		if !seen[w] {
			seen[w] = true
			warns = append(warns, w)
		}
	}
	var walk func(where string, steps []config.Step)
	walk = func(where string, steps []config.Step) {
		for i, step := range steps {
			w := fmt.Sprintf("%s.steps[%d]", where, i)
			if step.ID != "" {
				w = fmt.Sprintf("%s.steps[%s]", where, step.ID)
			}
			if step.Form() == "agent" {
				keys := make([]string, 0, len(step.Env))
				for k := range step.Env {
					keys = append(keys, k)
				}
				sort.Strings(keys)
				for _, k := range keys {
					if envTemplatesSecret(step.Env[k]) {
						warn(fmt.Sprintf("%s: env.%s templates a secret into an agent step's environment — DEPRECATED: the raw value reaches the external runtime for the whole run. Prefer the skill surface (verbs as tools, or skill.secrets_via: broker with a {{secret %q}} handle); see the Agent-Skill wiki page.", w, k, "name"))
					}
				}
			}
			if step.Parallel != nil {
				for bi, branch := range step.Parallel.Branches {
					walk(fmt.Sprintf("%s.parallel[%d]", w, bi), branch)
				}
			}
			if step.Compensate != nil {
				walk(w+".compensate", []config.Step{*step.Compensate})
			}
		}
	}
	for i, spec := range cfg.Triggers {
		walk(fmt.Sprintf("triggers[%d]", i), spec.Steps)
	}
	names := make([]string, 0, len(cfg.Workflows))
	for name := range cfg.Workflows {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		walk("workflows."+name, cfg.Workflows[name].Steps)
	}
	return warns
}

// envTemplatesSecret reports whether one env value's template reads secret
// material: a {{.secrets.*}} or {{.vaults.*}} field, or a {{ vault … }}
// call. ({{secret "name"}} handles are NOT flagged — they are the
// replacement: opaque at rest.)
func envTemplatesSecret(s string) bool {
	if !strings.Contains(s, "{{") {
		return false
	}
	refs, err := templateRefs(s)
	if err != nil {
		return false // unparseable templates fail validation elsewhere
	}
	for _, ref := range refs {
		if ref == "secrets" || strings.HasPrefix(ref, "secrets.") ||
			ref == "vaults" || strings.HasPrefix(ref, "vaults.") {
			return true
		}
	}
	calls, err := templateVaultCalls(s)
	return err == nil && len(calls) > 0
}
