package config

import (
	"fmt"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// Model selection — runtime `models:` blocks, named fleets, and the `model:`
// field on a step. See docs/design/runtimes-models-packs.md §1.2 and §2.
//
// The shape is deliberately small: a runtime declares what it may run and how
// it ranks what it can (`models: { default, prefer, allow }`), a fleet names a
// ranked list of ACCEPTABLE models plus a fallback posture
// (`{ any: [...], required: bool }`), and a step names a fleet, a model, or an
// inline fleet. Nothing here reaches the network: patterns are globbed against
// a roster the discovery layer supplies (internal/models), and a pattern that
// matches nothing simply contributes nothing — a wildcard never conjures an
// unavailable model.

// RuntimeModels is the OPTIONAL `models:` block on a `runtimes:` entry. Every
// field is optional, and omitting the whole block leaves the runtime fully
// automatic: roster discovered, default = bare launch, no restrictions.
type RuntimeModels struct {
	// Default is the model this runtime passes when nothing else resolves one.
	// Its ABSENCE is meaningful: no default means BARE LAUNCH — conductor
	// dispatches with no --model and the runtime uses its own built-in default
	// (docs/design/runtimes-models-packs.md §4).
	Default string `yaml:"default,omitempty"`
	// Prefer ranks acceptable models when a fleet offers a choice ("the
	// consumer disposes"). It also picks the effective default among several
	// available models when `default:` is unset.
	Prefer []string `yaml:"prefer,omitempty"`
	// Allow restricts what this runtime may EVER run: the discovered roster is
	// filtered down to entries matching one of these patterns. Supports the
	// same wildcards as a fleet's `any:` (see MatchModelPattern). Empty = no
	// restriction.
	Allow []string `yaml:"allow,omitempty"`
}

// IsZero reports the empty block, so `omitempty` drops it on marshal.
func (m *RuntimeModels) IsZero() bool {
	return m == nil || (m.Default == "" && len(m.Prefer) == 0 && len(m.Allow) == 0)
}

// ModelSpec is the polymorphic acceptable-model value. It is used in two
// places, with the same three shapes and the same meaning:
//
//   - a `models:` fleet entry (top-level or in a pack), and
//   - a step's `model:` field.
//
// YAML shapes (docs/design/runtimes-models-packs.md §2.2):
//
//	model: reviewer                                # string
//	model: [claude-opus-*, gpt-5.6-*]              # array  = { any: [...], required: false }
//	model: { any: [claude-opus-*], required: true} # object
//
// The STRING form is map-key-wins at resolution time: if it names a key in the
// top-level `models:` block it is a fleet reference, otherwise it is a model id
// or a wildcard. That decision belongs to resolution (ResolveModel), not to
// parsing — the parser only records which shape was written.
type ModelSpec struct {
	// Ref is the string form exactly as written: a fleet name, a model id, or
	// a wildcard pattern. Empty for the array/object forms.
	Ref string
	// Any is the ordered acceptable list, best-first. Literals and wildcards.
	Any []string
	// Required makes "nothing acceptable is available" a load-time error
	// instead of a fall-through to bare launch.
	Required bool
	// present distinguishes "no model: key" from "model: {}" — the former
	// inherits, the latter is an explicit (and invalid) empty fleet.
	present bool
}

// FleetSpec is the top-level `models:` map value. It is a ModelSpec: the same
// three shapes parse, so a pack overlay can collapse a fleet to one model
// (`models: { reviewer: claude-opus-5 }`) without a second grammar.
type FleetSpec = ModelSpec

// Set reports whether a `model:` key was written at all.
func (m ModelSpec) Set() bool { return m.present }

// IsZero implements yaml.IsZeroer so an unset `model:` is dropped on marshal
// (the migration and the pack lowering both round-trip these structs).
func (m ModelSpec) IsZero() bool { return !m.present }

// Acceptable returns the ordered acceptable list for this spec: `any:` for the
// array/object forms, or the single string form as a one-element list. It does
// NOT expand a fleet reference — the caller resolves that first.
func (m ModelSpec) Acceptable() []string {
	if len(m.Any) > 0 {
		return m.Any
	}
	if m.Ref != "" {
		return []string{m.Ref}
	}
	return nil
}

// UnmarshalYAML accepts the string, array, and object forms.
func (m *ModelSpec) UnmarshalYAML(n *yaml.Node) error {
	switch n.Kind {
	case yaml.ScalarNode:
		if n.Tag == "!!null" {
			return fmt.Errorf("model: is empty — name a fleet, a model id, a list of acceptable models, or { any: [...], required: bool }")
		}
		var s string
		if err := n.Decode(&s); err != nil {
			return fmt.Errorf("model: must be a fleet/model name, a list, or { any, required }: %w", err)
		}
		s = strings.TrimSpace(s)
		if s == "" {
			return fmt.Errorf("model: is empty — name a fleet, a model id, a list of acceptable models, or { any: [...], required: bool }")
		}
		m.Ref, m.present = s, true
		return nil
	case yaml.SequenceNode:
		var list []string
		if err := n.Decode(&list); err != nil {
			return fmt.Errorf("model: list must be model ids or patterns: %w", err)
		}
		m.Any, m.present = trimAll(list), true
		return nil
	case yaml.MappingNode:
		var obj struct {
			Any      []string `yaml:"any"`
			Required bool     `yaml:"required"`
		}
		if err := strictNodeDecode(n, &obj); err != nil {
			return fmt.Errorf("model: object form takes { any: [...], required: bool }: %w", err)
		}
		m.Any, m.Required, m.present = trimAll(obj.Any), obj.Required, true
		return nil
	}
	return fmt.Errorf("model: must be a fleet/model name, a list of acceptable models, or { any, required }")
}

// MarshalYAML renders the shortest shape that round-trips: the string form for
// a bare reference, a plain list when nothing else is set, else the object.
func (m ModelSpec) MarshalYAML() (any, error) {
	if !m.present {
		return nil, nil
	}
	if m.Ref != "" && len(m.Any) == 0 && !m.Required {
		return m.Ref, nil
	}
	if !m.Required {
		return m.Any, nil
	}
	return struct {
		Any      []string `yaml:"any,omitempty"`
		Required bool     `yaml:"required"`
	}{Any: m.Any, Required: m.Required}, nil
}

// ModelSpecOf builds a string-form spec (tests, migration, pack lowering).
func ModelSpecOf(ref string) ModelSpec {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return ModelSpec{}
	}
	return ModelSpec{Ref: ref, present: true}
}

// FleetOf builds an object-form spec (tests, migration).
func FleetOf(required bool, any ...string) ModelSpec {
	return ModelSpec{Any: trimAll(any), Required: required, present: true}
}

func trimAll(in []string) []string {
	out := make([]string, 0, len(in))
	for _, s := range in {
		out = append(out, strings.TrimSpace(s))
	}
	return out
}

// ---------------------------------------------------------------------------
// Pattern matching against a roster
// ---------------------------------------------------------------------------

// MatchModelPattern reports whether a model id matches one `any:`/`allow:`
// pattern. The grammar is a plain glob over the whole id — `*` matches any run
// of characters (including none) and `?` matches one. There is deliberately no
// path semantics: a model id may contain "/" (openai/gpt-4o, google/gemini-3),
// and `path.Match` would refuse to let `*` cross it.
//
// The full wildcard "*" matches everything. Note the YAML gotcha: a pattern
// that STARTS with `*` must be quoted ("*", "*-mini", "*pro*") — a bare * is a
// YAML alias indicator.
func MatchModelPattern(pattern, model string) bool {
	return modelGlobMatch(pattern, model)
}

// IsModelPattern reports whether an entry carries a wildcard (as opposed to
// being a literal model id).
func IsModelPattern(s string) bool { return strings.ContainsAny(s, "*?") }

// modelGlobMatch is an iterative `*`/`?` matcher with backtracking — no regexp
// compile per candidate, no path-separator semantics.
func modelGlobMatch(pattern, s string) bool {
	var (
		p, i       int
		star       = -1
		starMatch  int
		plen, slen = len(pattern), len(s)
	)
	for i < slen {
		switch {
		case p < plen && (pattern[p] == '?' || pattern[p] == s[i]):
			p, i = p+1, i+1
		case p < plen && pattern[p] == '*':
			star, starMatch = p, i
			p++
		case star >= 0:
			starMatch++
			p, i = star+1, starMatch
		default:
			return false
		}
	}
	for p < plen && pattern[p] == '*' {
		p++
	}
	return p == plen
}

// ExpandModelPatterns turns an acceptable list (literals + wildcards) into a
// concrete, ordered, de-duplicated model list drawn from the roster.
//
// Ordering (docs/design/runtimes-models-packs.md §2.1): literals keep their
// listed priority; a wildcard expands IN PLACE in roster order (the roster is
// newest-first, as the catalog reports it), de-duped against anything already
// emitted. A literal not present in the roster contributes nothing — a fleet
// can never name a model the box cannot run. A pattern matching nothing simply
// falls through to the next entry.
func ExpandModelPatterns(acceptable, roster []string) []string {
	if len(acceptable) == 0 || len(roster) == 0 {
		return nil
	}
	inRoster := make(map[string]bool, len(roster))
	for _, m := range roster {
		inRoster[m] = true
	}
	seen := make(map[string]bool, len(roster))
	out := make([]string, 0, len(roster))
	for _, entry := range acceptable {
		if entry == "" {
			continue
		}
		if !IsModelPattern(entry) {
			if inRoster[entry] && !seen[entry] {
				seen[entry] = true
				out = append(out, entry)
			}
			continue
		}
		for _, m := range roster {
			if seen[m] || !MatchModelPattern(entry, m) {
				continue
			}
			seen[m] = true
			out = append(out, m)
		}
	}
	return out
}

// FilterRoster applies a runtime's `allow:` list to a discovered roster,
// preserving roster order. An empty allow list is no restriction.
func FilterRoster(roster, allow []string) []string {
	if len(allow) == 0 {
		return roster
	}
	out := make([]string, 0, len(roster))
	for _, m := range roster {
		for _, pat := range allow {
			if MatchModelPattern(pat, m) {
				out = append(out, m)
				break
			}
		}
	}
	return out
}

// RankByPrefer reorders candidates so that anything matching an earlier
// `prefer:` entry comes first (the consumer disposes); models matching nothing
// keep their relative order at the back. Stable, so a tie is broken by the
// candidate order the caller supplied.
func RankByPrefer(candidates, prefer []string) []string {
	if len(prefer) == 0 || len(candidates) < 2 {
		return candidates
	}
	rank := func(m string) int {
		for i, p := range prefer {
			if MatchModelPattern(p, m) {
				return i
			}
		}
		return len(prefer)
	}
	out := append([]string(nil), candidates...)
	sort.SliceStable(out, func(i, j int) bool { return rank(out[i]) < rank(out[j]) })
	return out
}

// ---------------------------------------------------------------------------
// Validation
// ---------------------------------------------------------------------------

// validateModels checks the top-level `models:` fleets and every runtime's
// `models:` block. Shape only — whether a model is actually AVAILABLE is a
// resolution-time question, answered against a discovered roster.
func (c *Config) validateModels() error {
	names := make([]string, 0, len(c.Models))
	for n := range c.Models {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, name := range names {
		if strings.TrimSpace(name) == "" {
			return fmt.Errorf("config: models: empty fleet name")
		}
		if err := validateFleet("models."+name, c.Models[name]); err != nil {
			return err
		}
	}
	rnames := make([]string, 0, len(c.Runtimes))
	for n := range c.Runtimes {
		rnames = append(rnames, n)
	}
	sort.Strings(rnames)
	for _, n := range rnames {
		if err := validateRuntimeModels("runtime "+n, c.Runtimes[n].Models); err != nil {
			return err
		}
	}
	return nil
}

// validateFleet checks one `models:` entry.
func validateFleet(where string, f FleetSpec) error {
	if !f.present {
		return fmt.Errorf("config: %s: empty fleet — a fleet is a model id, a list of acceptable models, or { any: [...], required: bool }", where)
	}
	if f.Ref == "" && len(f.Any) == 0 {
		return fmt.Errorf("config: %s: needs at least one acceptable model (any: [...])", where)
	}
	for i, m := range f.Acceptable() {
		if strings.TrimSpace(m) == "" {
			return fmt.Errorf("config: %s: any[%d] is empty", where, i)
		}
	}
	return nil
}

// validateRuntimeModels checks one runtime's `models:` block.
func validateRuntimeModels(where string, m *RuntimeModels) error {
	if m == nil {
		return nil
	}
	if IsModelPattern(m.Default) {
		return fmt.Errorf("config: %s: models.default must be a concrete model id, not a pattern (%q) — use models.prefer to rank, or a fleet's any: to accept several", where, m.Default)
	}
	for i, p := range m.Prefer {
		if strings.TrimSpace(p) == "" {
			return fmt.Errorf("config: %s: models.prefer[%d] is empty", where, i)
		}
	}
	for i, a := range m.Allow {
		if strings.TrimSpace(a) == "" {
			return fmt.Errorf("config: %s: models.allow[%d] is empty", where, i)
		}
	}
	// An explicit default outside the runtime's own allowlist can never run —
	// that is a config contradiction, catchable without any discovery.
	if m.Default != "" && len(m.Allow) > 0 {
		ok := false
		for _, a := range m.Allow {
			if MatchModelPattern(a, m.Default) {
				ok = true
				break
			}
		}
		if !ok {
			return fmt.Errorf("config: %s: models.default %q is excluded by models.allow (%s) — it could never run", where, m.Default, strings.Join(m.Allow, ", "))
		}
	}
	return nil
}
