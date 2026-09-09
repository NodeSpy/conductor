package config

import (
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// vaultTemplateRE matches a `{{ vault … }}` (or `{{- vault …}}`) template call.
var vaultTemplateRE = regexp.MustCompile(`\{\{-?\s*vault\b`)

// lintPackRefs warns about references that pack namespacing/rebinding does NOT
// reach, so a pack author sees them at install instead of hitting a silent
// runtime failure:
//
//   - a `{{ vault … }}` template anywhere in the pack — a pack reaches secrets
//     through requires.secrets + the broker, never a vault directly (that is
//     environment);
//   - a bound environment name (a required connector/store/secret/handoff)
//     appearing inside a `code:` step body, which is not rewritten.
func (st *packInstantiation) lintPackRefs(ns string, man *PackManifest, env envBindings) {
	// Pack-side required names (what the author writes, before rebinding).
	names := map[string]bool{}
	for _, m := range []map[string]string{env.conn, env.store, env.secret, env.handoff} {
		for k := range m {
			names[k] = true
		}
	}

	vaultSeen := false
	codeHits := map[string]bool{} // name -> seen (dedup the warning)

	visit := func(s *Step) {
		// Vault template in any string-bearing field.
		fields := []string{s.Prompt, s.Code}
		if b, err := yaml.Marshal(s.Options); err == nil {
			fields = append(fields, string(b))
		}
		for _, f := range fields {
			if vaultTemplateRE.MatchString(f) {
				vaultSeen = true
			}
		}
		// Bound names inside a code body.
		if s.Run != "" && s.Code != "" {
			for name := range names {
				if containsIdentifier(s.Code, name) {
					codeHits[name] = true
				}
			}
		}
	}

	for _, wf := range man.Workflows {
		walkSteps(wf.Steps, visit)
	}
	for _, tr := range man.Triggers {
		walkSteps(tr.Steps, visit)
	}
	for _, ck := range man.Checks {
		s := ck
		walkSteps([]Step{s}, visit)
	}

	if vaultSeen {
		st.warnf("pack %q: uses a {{ vault … }} template — a pack reaches secrets via requires.secrets and the broker, not a vault directly; the vault name is not rebound", ns)
	}
	if len(codeHits) > 0 {
		hits := make([]string, 0, len(codeHits))
		for n := range codeHits {
			hits = append(hits, n)
		}
		sort.Strings(hits)
		st.warnf("pack %q: code: step body references bound name(s) %s — references inside code are NOT rebound; pass the value via a setting or workflow input instead", ns, strings.Join(hits, ", "))
	}
}

// walkSteps applies fn to every step, recursing into nested step forms.
func walkSteps(steps []Step, fn func(*Step)) {
	for i := range steps {
		s := &steps[i]
		fn(s)
		if s.Compensate != nil {
			walkSteps([]Step{*s.Compensate}, fn)
		}
		if s.Parallel != nil {
			for bi := range s.Parallel.Branches {
				walkSteps(s.Parallel.Branches[bi], fn)
			}
		}
	}
}

// containsIdentifier reports whether name appears in s as a whole identifier
// (not a substring of a longer name), so "cache" matches ctx.store("cache")
// but not "cached".
func containsIdentifier(s, name string) bool {
	from := 0
	for {
		i := strings.Index(s[from:], name)
		if i < 0 {
			return false
		}
		i += from
		before := i == 0 || !isIdentByte(s[i-1])
		after := i+len(name) >= len(s) || !isIdentByte(s[i+len(name)])
		if before && after {
			return true
		}
		from = i + len(name)
	}
}

func isIdentByte(b byte) bool {
	return b == '_' || b == '-' ||
		(b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9')
}
