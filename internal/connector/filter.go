package connector

import (
	"fmt"
	"strings"

	"github.com/NodeSpy/conductor/internal/config"
)

// ValidateFilter checks one trigger's `filter:` against the event's declared
// surface (docs/design/unified-filter.md §Validation):
//
//   - an event with no surface at all rejects `filter:` outright, rather than
//     accepting a block nothing will ever evaluate;
//   - every fact an expr string READS must be one the event publishes, so
//     `!is_drafft` fails at load instead of quietly resolving to nil (falsy)
//     and inverting the operator's intent;
//   - every object key must be a declared match key (or the reserved `expr:`);
//   - each key's value must be the declared type.
//
// Only BASE keys are ever checked: the grammar turns `not_<key>` into a Not
// around `<key>`, so a connector declares one key and both spellings validate
// (and neither can drift from the other).
//
// where names the config location for the error; connName is the configured
// connector instance, for an error an operator can act on.
func ValidateFilter(where, connName string, ev EventDecl, f *config.Filter) error {
	if f == nil {
		return nil
	}
	keys, facts := ev.FilterKeys(), ev.FilterFacts()
	if len(keys) == 0 && len(facts) == 0 {
		return fmt.Errorf("%s: `filter:` is not supported on %s.%s — the event publishes no facts and declares no match keys",
			where, connName, ev.Name)
	}
	for _, ref := range f.FactRefs() {
		if _, ok := facts[ref]; !ok {
			return fmt.Errorf("%s: filter: %s.%s publishes no fact %q (facts: %s)",
				where, connName, ev.Name, ref, orNone(sortedFilterKeys(facts)))
		}
	}
	var bad error
	f.Walk(func(n *config.Filter) {
		if bad != nil || n.Op != config.FilterOpMatch {
			return
		}
		field, ok := keys[n.Key]
		if !ok {
			bad = fmt.Errorf("%s: filter: %s.%s has no match key %q (keys: %s — each also legal as %s%s, or %q for a condition string)",
				where, connName, ev.Name, n.Key, orNone(sortedFilterKeys(keys)),
				config.FilterNotPrefix, "<key>", config.FilterExprKey)
			return
		}
		if err := checkType(n.Val, field); err != nil {
			bad = fmt.Errorf("%s: filter: key %q: %v", where, n.Key, err)
		}
	})
	return bad
}

// orNone renders an empty key set as "none" rather than an empty tail, so the
// error reads as a statement about the event instead of a truncated list.
func orNone(keys []string) string {
	if len(keys) == 0 {
		return "none"
	}
	return strings.Join(keys, ", ")
}

// GenericFilterMatcher adapts a connector's TypeDecl.Filter to the one-key
// FilterMatcher the IR evaluates with: the grammar owns AND/OR/NOT and hands
// the connector a single key at a time. A connector that declares no Filter
// func has no generic keys, so any Match on its events is a load error and
// this never runs.
func GenericFilterMatcher(decl *TypeDecl, event string) config.FilterMatcher {
	return func(key string, val any, facts map[string]any) (bool, error) {
		if decl == nil || decl.Filter == nil {
			return false, fmt.Errorf("filter: %s declares no match keys", event)
		}
		return decl.Filter(event, map[string]any{key: val}, facts)
	}
}
