package github

import "github.com/NodeSpy/conductor/pkg/githubkit/ghsource"

// The github half of the unified `filter:` lives in the kit
// (pkg/githubkit/ghsource/filter.go): the facts each event publishes, the
// match keys, and the intrinsic-default lowering. These re-exports keep the
// daemon's consumers (internal/connector's decl, the config migration) on the
// one definition.
const (
	FilterString  = ghsource.FilterString
	FilterBool    = ghsource.FilterBool
	FilterList    = ghsource.FilterList
	FilterRepoKey = ghsource.FilterRepoKey
)

// FilterFacts returns the facts the named event publishes to a `filter:`.
func FilterFacts(event string) map[string]string { return ghsource.FilterFacts(event) }

// FilterMatchKeys returns the structured match keys legal in the named event's
// `filter:`.
func FilterMatchKeys(event string) map[string]string { return ghsource.FilterMatchKeys(event) }

// FilterEvents lists the events that publish predicate facts, sorted.
func FilterEvents() []string { return ghsource.FilterEvents() }

// MergeGateKeys lists the merge-ready gate toggles (for the config migration).
func MergeGateKeys() []string { return ghsource.MergeGateKeys() }

// MergeGateOn reports whether one merge-ready gate is enforced (opt-out).
func MergeGateOn(gates map[string]any, key string) bool { return ghsource.MergeGateOn(gates, key) }
