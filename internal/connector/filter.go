package connector

import (
	"fmt"
	"strings"

	"github.com/NodeSpy/conductor/internal/config"
)

// ValidateFilter checks one trigger's unified `filter:` against the event's
// declared surface (docs/design/unified-filter.md §Validation):
//
//   - an event with no surface rejects `filter:` outright, rather than
//     accepting a block nothing will ever evaluate;
//   - every fact an expr string READS must be one the event publishes, so
//     `!is_drafft` fails at load instead of quietly resolving to nil (falsy)
//     and inverting the operator's intent;
//   - every object key must be a declared match key (or the reserved `expr:`);
//   - each key's value must be the declared type.
//
// where names the config location for the error; connName is the configured
// connector instance, for an error an operator can act on.
func ValidateFilter(where, connName string, ev EventDecl, f *config.Filter) error {
	if f == nil {
		return nil
	}
	if len(ev.Facts) == 0 && len(ev.MatchKeys) == 0 {
		return fmt.Errorf("%s: `filter:` is not supported on %s.%s — the event publishes no filter facts; use `filters:` here",
			where, connName, ev.Name)
	}
	for _, ref := range f.FactRefs() {
		if _, ok := ev.Facts[ref]; !ok {
			return fmt.Errorf("%s: filter: %s.%s publishes no fact %q (facts: %s)",
				where, connName, ev.Name, ref, strings.Join(sortedFilterKeys(ev.Facts), ", "))
		}
	}
	var bad error
	f.Walk(func(n *config.Filter) {
		if bad != nil || n.Op != config.FilterOpMatch {
			return
		}
		field, ok := ev.MatchKeys[n.Key]
		if !ok {
			bad = fmt.Errorf("%s: filter: %s.%s has no match key %q (keys: %s — or %q for a condition string)",
				where, connName, ev.Name, n.Key,
				strings.Join(sortedFilterKeys(ev.MatchKeys), ", "), config.FilterExprKey)
			return
		}
		if err := checkType(n.Val, field); err != nil {
			bad = fmt.Errorf("%s: filter: key %q: %v", where, n.Key, err)
		}
	})
	return bad
}
