package config

import (
	"fmt"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// `skill.verbs` is polymorphic (docs/design/skill-verb-scope.md), the same
// list-or-map shape `runtimes:` and `requires.connectors` use:
//
//	verbs: [slack.post, github.submit_review]     access only
//
//	verbs:                                        access + resource scope
//	  slack.post:           { channel: ["#code-reviews"] }
//	  github.submit_review: {}
//	  kv.*:                 { store: ["shared-kv"] }
//
// Both forms grant the same verbs; the map form additionally says WHICH
// resource each call may name in a given option. An entry with no constraints
// (`{}`) is exactly the list form's entry — the verb is granted and every
// scoped option stays pinned to the dispatch's own context.
//
// The option keys are checked against the connector's declared scope options
// at validate time (flow.validateSkillProfiles), not here: config has no
// registry, and a typo caught with "slack.post has no resource option
// \"chanel\"" needs the real verb schema to say so.

// UnmarshalYAML decodes the `skill:` block, splitting the polymorphic
// `verbs:` key out of the strict field decode of everything else.
func (p *SkillPolicy) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind == yaml.ScalarNode && n.Tag == "!!null" {
		*p = SkillPolicy{}
		return nil
	}
	if n.Kind != yaml.MappingNode {
		return fmt.Errorf("skill: want a block with verbs:/secrets_via:/…, got a %s", nodeKindName(n))
	}
	// Everything but `verbs:` keeps the ordinary strict decode, so a typo in
	// a sibling key is still an unknown-field error and a field added to
	// SkillPolicy later needs no change here.
	rest := *n
	rest.Content = nil
	var verbs *yaml.Node
	for i := 0; i+1 < len(n.Content); i += 2 {
		if n.Content[i].Value == "verbs" {
			verbs = n.Content[i+1]
			continue
		}
		rest.Content = append(rest.Content, n.Content[i], n.Content[i+1])
	}
	type plain SkillPolicy
	var q plain
	if err := strictNodeDecode(&rest, &q); err != nil {
		return err
	}
	*p = SkillPolicy(q)
	pats, scopes, err := decodeVerbGrant(verbs)
	if err != nil {
		return err
	}
	p.Verbs, p.VerbScopes = pats, scopes
	return nil
}

// decodeVerbGrant reads the list or map form into (patterns, per-verb scopes).
func decodeVerbGrant(n *yaml.Node) ([]string, map[string]map[string][]string, error) {
	if n == nil || (n.Kind == yaml.ScalarNode && n.Tag == "!!null") {
		return nil, nil, nil
	}
	switch n.Kind {
	case yaml.SequenceNode:
		var pats []string
		if err := n.Decode(&pats); err != nil {
			return nil, nil, fmt.Errorf("skill.verbs: list form takes verb patterns (e.g. [gh.comment, kv.*]): %w", err)
		}
		for _, p := range pats {
			if strings.TrimSpace(p) == "" {
				return nil, nil, fmt.Errorf("skill.verbs: empty verb pattern")
			}
		}
		return pats, nil, nil
	case yaml.MappingNode:
		pats := make([]string, 0, len(n.Content)/2)
		scopes := map[string]map[string][]string{}
		for i := 0; i+1 < len(n.Content); i += 2 {
			pat := strings.TrimSpace(n.Content[i].Value)
			if pat == "" {
				return nil, nil, fmt.Errorf("skill.verbs: empty verb pattern")
			}
			if _, dup := scopes[pat]; dup {
				return nil, nil, fmt.Errorf("skill.verbs: duplicate verb %q", pat)
			}
			cons, err := decodeVerbConstraints(pat, n.Content[i+1])
			if err != nil {
				return nil, nil, err
			}
			pats = append(pats, pat)
			scopes[pat] = cons
		}
		return pats, scopes, nil
	}
	return nil, nil, fmt.Errorf("skill.verbs: want a list of verb patterns or a map of verb -> {option: [values]}, got a %s", nodeKindName(n))
}

// decodeVerbConstraints reads one map entry's per-option allowlists. An empty
// or null value is a grant with no widening — the common `verb: {}` case.
func decodeVerbConstraints(pat string, v *yaml.Node) (map[string][]string, error) {
	if v == nil || (v.Kind == yaml.ScalarNode && v.Tag == "!!null") {
		return map[string][]string{}, nil
	}
	if v.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("skill.verbs.%s: want {option: [allowed values]} (or {} for access only), got a %s", pat, nodeKindName(v))
	}
	out := make(map[string][]string, len(v.Content)/2)
	for i := 0; i+1 < len(v.Content); i += 2 {
		opt := strings.TrimSpace(v.Content[i].Value)
		if opt == "" {
			return nil, fmt.Errorf("skill.verbs.%s: empty option name", pat)
		}
		if _, dup := out[opt]; dup {
			return nil, fmt.Errorf("skill.verbs.%s: duplicate option %q", pat, opt)
		}
		var vals []string
		val := v.Content[i+1]
		switch val.Kind {
		case yaml.ScalarNode:
			// A bare value is the one-element list — `channel: "#ops"`.
			var one string
			if err := val.Decode(&one); err != nil {
				return nil, fmt.Errorf("skill.verbs.%s.%s: want a value or a list of values: %w", pat, opt, err)
			}
			vals = []string{one}
		case yaml.SequenceNode:
			if err := val.Decode(&vals); err != nil {
				return nil, fmt.Errorf("skill.verbs.%s.%s: want a list of allowed values: %w", pat, opt, err)
			}
		default:
			return nil, fmt.Errorf("skill.verbs.%s.%s: want a value or a list of values, got a %s", pat, opt, nodeKindName(val))
		}
		for _, s := range vals {
			if strings.TrimSpace(s) == "" {
				return nil, fmt.Errorf("skill.verbs.%s.%s: empty value", pat, opt)
			}
		}
		out[opt] = vals
	}
	return out, nil
}

// MarshalYAML re-emits the form the grant was written in, so a pack override
// or an instantiation round-trip (Step → YAML → Step) doesn't quietly lose the
// per-verb constraints. It emits only patterns still present in Verbs: a
// narrowing pass that drops a pattern must never see it come back through a
// stale scopes entry.
func (p SkillPolicy) MarshalYAML() (any, error) {
	type plain SkillPolicy
	q := plain(p)
	if len(p.VerbScopes) == 0 {
		return q, nil
	}
	q.Verbs = nil
	b, err := yaml.Marshal(q)
	if err != nil {
		return nil, err
	}
	var m map[string]any
	if err := yaml.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	if m == nil {
		m = map[string]any{}
	}
	verbs := make(map[string]any, len(p.Verbs))
	for _, pat := range p.Verbs {
		entry := map[string]any{}
		for opt, vals := range p.VerbScopes[pat] {
			entry[opt] = vals
		}
		verbs[pat] = entry
	}
	m["verbs"] = verbs
	return m, nil
}

// pruneVerbScopes drops constraints whose verb pattern no longer appears in
// the grant (a pack boundary narrowing, a consumer override).
func pruneVerbScopes(scopes map[string]map[string][]string, keep []string) map[string]map[string][]string {
	if len(scopes) == 0 {
		return nil
	}
	out := make(map[string]map[string][]string, len(keep))
	for _, pat := range keep {
		if c, ok := scopes[pat]; ok {
			out[pat] = c
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// ScopesFor returns the per-option allowlists a grant attaches to one CONCRETE
// verb id: every entry whose pattern admits it, merged. `kv.*: {store: [s]}`
// therefore constrains kv.get and kv.set alike, and a verb named twice (by a
// pattern and by its own name) gets the union — listing only ever widens.
func (p *SkillPolicy) ScopesFor(uses string, match func(pattern, uses string) bool) map[string][]string {
	if p == nil || len(p.VerbScopes) == 0 {
		return nil
	}
	pats := make([]string, 0, len(p.VerbScopes))
	for pat := range p.VerbScopes {
		pats = append(pats, pat)
	}
	sort.Strings(pats) // deterministic merge order
	var out map[string][]string
	for _, pat := range pats {
		if !match(pat, uses) {
			continue
		}
		for opt, vals := range p.VerbScopes[pat] {
			if out == nil {
				out = map[string][]string{}
			}
			out[opt] = append(out[opt], vals...)
		}
	}
	return out
}
