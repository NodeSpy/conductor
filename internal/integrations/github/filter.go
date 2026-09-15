package github

import (
	"fmt"
	"log"
	"sort"

	"github.com/NodeSpy/conductor/internal/config"
)

// This file is the github half of the unified `filter:`
// (docs/design/unified-filter.md): the FACTS each filterable event publishes,
// the MATCH predicate for each structured key, and the lowering that turns the
// legacy `filters: {exclude, gates, labels_any, …}` block into the same IR.
//
// Every keep-condition site now evaluates exactly ONE filter — the trigger's
// own `filter:` when it set one, otherwise its legacy fields lowered here. The
// legacy path must stay bit-identical, so each Match predicate's BODY is the
// matcher that site already called (config.Exclude.Matches, anyFold/allFold,
// containsFold, loginMatch, authorBotMatch, the gate readers) rather than a
// reimplementation, and each site lowers only the conjuncts IT evaluated.

// Fact and match-key value kinds, as internal/connector turns them into its
// own schema types. Plain strings because internal/connector imports this
// package, not the other way round.
const (
	FilterString = "string"
	FilterBool   = "bool"
	FilterList   = "list"
)

// filterFacts declares, per event, the facts a `filter:` may reference by name
// — the surface load-time validation checks expr strings against.
//
// It is deliberately a SUBSET of what the runtime facts map carries. The
// issue_matched map, for instance, also carries an empty `head_branch` so a
// legacy `exclude.branches` lowers to a bit-identical `path.Match(glob, "")`;
// declaring that as a referenceable fact would only invite `filter:` authors
// to read a value that is always empty.
var filterFacts = map[string]map[string]string{
	"review_requested": {
		"head_branch": FilterString, "base_branch": FilterString,
		"title": FilterString, "labels": FilterList,
		"is_draft": FilterBool, "author": FilterString,
	},
	"changes_requested": {
		"head_branch": FilterString, "base_branch": FilterString,
		"title": FilterString, "labels": FilterList,
		"author": FilterString, "reviewer": FilterString,
		"author_is_bot": FilterBool,
	},
	"new_comment": {
		"comment_author": FilterString, "comment_body": FilterString,
		"author_is_bot": FilterBool,
	},
	"issue_matched": {
		"title": FilterString, "labels": FilterList,
		"author": FilterString, "sole_assignee": FilterBool,
	},
	"merge_ready": {
		"labels": FilterList, "author": FilterString, "is_draft": FilterBool,
		"merge_state": FilterString, "review_decision": FilterString,
		"non_author_approval": FilterBool, "threads_resolved": FilterBool,
	},
}

// matchKeyFacts maps each structured match key to the fact it reads and the
// type of value it takes. A key is legal for an event exactly when that event
// publishes the fact it reads, so the two surfaces cannot drift apart.
var matchKeyFacts = map[string]struct {
	fact string
	typ  string
}{
	"branches":            {"head_branch", FilterList},
	"base_branches":       {"base_branch", FilterList},
	"title":               {"title", FilterList},
	"labels_any":          {"labels", FilterList},
	"labels_all":          {"labels", FilterList},
	"require_label":       {"labels", FilterString},
	"authors":             {"author", FilterList},
	"from_users":          {"comment_author", FilterList},
	"ignore_users":        {"comment_author", FilterList},
	"author_bot":          {"author_is_bot", FilterBool},
	"sole_assignee":       {"sole_assignee", FilterBool},
	"not_draft":           {"is_draft", FilterBool},
	"merge_state":         {"merge_state", FilterBool},
	"review_decision":     {"review_decision", FilterBool},
	"non_author_approval": {"non_author_approval", FilterBool},
	"threads_resolved":    {"threads_resolved", FilterBool},
}

// FilterFacts returns the facts the named event publishes to a `filter:`
// (fact name → value kind), or nil when the event has no filter surface —
// phase 1 wires the five events whose keep-conditions evaluate a predicate.
func FilterFacts(event string) map[string]string {
	out := make(map[string]string, len(filterFacts[event]))
	for k, v := range filterFacts[event] {
		out[k] = v
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// FilterMatchKeys returns the structured match keys legal inside a `filter:`
// object for the named event (key → value kind): every key whose underlying
// fact the event publishes.
func FilterMatchKeys(event string) map[string]string {
	facts := filterFacts[event]
	if len(facts) == 0 {
		return nil
	}
	out := map[string]string{}
	for key, m := range matchKeyFacts {
		if _, ok := facts[m.fact]; ok {
			out[key] = m.typ
		}
	}
	return out
}

// FilterEvents lists the events that accept a `filter:`, sorted — for the
// error a trigger gets when it puts one on an event that has none.
func FilterEvents() []string {
	out := make([]string, 0, len(filterFacts))
	for e := range filterFacts {
		out = append(out, e)
	}
	sort.Strings(out)
	return out
}

// --- evaluation ------------------------------------------------------------

// filterPasses evaluates the one filter gating a keep-condition: the action's
// unified `filter:` when it set one, else the legacy conjuncts that site
// already evaluated, lowered into the same IR by the caller.
//
// An evaluation error fails CLOSED — a filter that cannot be evaluated is not
// a filter that passed — and is logged with where it came from, since a
// silently non-firing trigger is otherwise invisible. The legacy lowering is
// built from typed config fields and cannot error, so this only ever bites a
// hand-written `filter:`.
func (g *Integration) filterPasses(act config.Action, where string, facts map[string]any, legacy *config.Filter) bool {
	f := act.Filter
	if f == nil {
		f = legacy
	}
	ok, err := f.Eval(facts, matchFilterKey)
	if err != nil {
		log.Printf("github[%s]: %s: filter not evaluated (%v) — not firing", g.name, where, err)
		return false
	}
	return ok
}

// matchFilterKey evaluates one structured match key against the event's facts.
// Each case delegates to the matcher the legacy call site used, so a lowered
// legacy block and a hand-written `filter:` share one implementation.
func matchFilterKey(key string, val any, facts map[string]any) (bool, error) {
	switch key {
	// --- denylist-shaped keys: the config.Exclude matchers, one arm each ---
	case "branches":
		globs, err := filterStrings(key, val)
		if err != nil {
			return false, err
		}
		return config.Exclude{Branches: globs}.Matches(factString(facts, "head_branch"), "", nil), nil
	case "base_branches":
		globs, err := filterStrings(key, val)
		if err != nil {
			return false, err
		}
		return config.Exclude{Branches: globs}.Matches(factString(facts, "base_branch"), "", nil), nil
	case "title":
		// Case-insensitive SUBSTRING match — the legacy semantics, footgun and
		// all ("Release " matches "…each release entry…"). New configs should
		// reach for expr + startswith()/contains() instead.
		subs, err := filterStrings(key, val)
		if err != nil {
			return false, err
		}
		return config.Exclude{Title: subs}.Matches("", factString(facts, "title"), nil), nil

	// --- label / login sets ---
	case "labels_any":
		want, err := filterStrings(key, val)
		if err != nil {
			return false, err
		}
		return anyFold(factStrings(facts, "labels"), want), nil
	case "labels_all":
		want, err := filterStrings(key, val)
		if err != nil {
			return false, err
		}
		return allFold(factStrings(facts, "labels"), want), nil
	case "require_label":
		label, err := filterString(key, val)
		if err != nil {
			return false, err
		}
		return containsFold(factStrings(facts, "labels"), label), nil
	case "authors":
		want, err := filterStrings(key, val)
		if err != nil {
			return false, err
		}
		return containsFold(want, factString(facts, "author")), nil
	case "from_users":
		want, err := filterStrings(key, val)
		if err != nil {
			return false, err
		}
		return loginMatch(want, factString(facts, "comment_author")), nil
	case "ignore_users":
		deny, err := filterStrings(key, val)
		if err != nil {
			return false, err
		}
		return !loginMatch(deny, factString(facts, "comment_author")), nil
	case "author_bot":
		want, err := filterBool(key, val)
		if err != nil {
			return false, err
		}
		return authorBotMatch(&want, factBool(facts, "author_is_bot")), nil

	// --- opt-out toggles: false relaxes the check, true (or absent) enforces ---
	case "sole_assignee":
		return gatedBy(key, val, func() bool { return factBool(facts, "sole_assignee") })
	case "not_draft":
		return gatedBy(key, val, func() bool { return !factBool(facts, "is_draft") })
	case "merge_state":
		return gatedBy(key, val, func() bool { return factString(facts, "merge_state") == "CLEAN" })
	case "review_decision":
		return gatedBy(key, val, func() bool { return factString(facts, "review_decision") == "APPROVED" })
	case "non_author_approval":
		return gatedBy(key, val, func() bool { return factBool(facts, "non_author_approval") })
	case "threads_resolved":
		return gatedBy(key, val, func() bool { return factBool(facts, "threads_resolved") })
	}
	return false, fmt.Errorf("filter: github has no match key %q", key)
}

// gatedBy evaluates a boolean toggle key: `true` enforces the check, `false`
// waives it (the key is then vacuously satisfied).
func gatedBy(key string, val any, check func() bool) (bool, error) {
	on, err := filterBool(key, val)
	if err != nil {
		return false, err
	}
	return !on || check(), nil
}

// --- value coercion --------------------------------------------------------

// filterStrings coerces a match key's value to a string list, accepting the
// single-string shorthand (`branches: main`) YAML users expect.
func filterStrings(key string, val any) ([]string, error) {
	switch x := val.(type) {
	case nil:
		return nil, nil
	case []string:
		return x, nil
	case string:
		return []string{x}, nil
	case []any:
		out := make([]string, 0, len(x))
		for _, e := range x {
			s, ok := e.(string)
			if !ok {
				return nil, fmt.Errorf("filter: %s: want a list of strings, got a %T entry", key, e)
			}
			out = append(out, s)
		}
		return out, nil
	}
	return nil, fmt.Errorf("filter: %s: want a list of strings, got %T", key, val)
}

func filterString(key string, val any) (string, error) {
	s, ok := val.(string)
	if !ok {
		return "", fmt.Errorf("filter: %s: want a string, got %T", key, val)
	}
	return s, nil
}

func filterBool(key string, val any) (bool, error) {
	b, ok := val.(bool)
	if !ok {
		return false, fmt.Errorf("filter: %s: want true or false, got %T", key, val)
	}
	return b, nil
}

// --- fact accessors --------------------------------------------------------

func factString(facts map[string]any, key string) string {
	s, _ := facts[key].(string)
	return s
}

func factBool(facts map[string]any, key string) bool {
	b, _ := facts[key].(bool)
	return b
}

func factStrings(facts map[string]any, key string) []string {
	v, _ := facts[key].([]string)
	return v
}

// --- legacy lowering -------------------------------------------------------

// matchIf emits a Match node only for a non-empty list. Emptiness is why the
// lowering cannot be a blanket "one Match per key": `labels_any: []` means "no
// constraint" in the legacy block but "has any of nothing" (false) as a
// predicate, so an unset legacy key must produce NO node rather than an empty
// one. FilterAnd/FilterOr drop the nils.
func matchIf(key string, vals []string) *config.Filter {
	if len(vals) == 0 {
		return nil
	}
	return config.FilterMatch(key, vals)
}

// lowerExclude lowers a legacy `exclude:` denylist. It is an OR across its
// arms, and "not excluded" is the negation of that OR — so an empty exclude
// lowers to nothing (Or of no arms is false, Not of that is true), matching
// config.Exclude.Matches returning false for an empty Exclude.
func lowerExclude(e config.Exclude) *config.Filter {
	if e.Empty() {
		return nil
	}
	return config.FilterNot(config.FilterOr(
		matchIf("branches", e.Branches),
		matchIf("labels_any", e.Labels),
		matchIf("title", e.Title),
	))
}

// lowerReviewRequested lowers what a review_requested keep-condition
// evaluated inline: the opt-IN `gates.not_draft` toggle AND "not excluded".
func lowerReviewRequested(act config.Action) *config.Filter {
	return config.FilterAnd(
		config.FilterMatch("not_draft", gateEnabled(act.Gates, "not_draft")),
		lowerExclude(act.Exclude),
	)
}

// lowerReadyReview lowers the ready_for_review keep-condition, which checks
// the exclude list but deliberately NOT the not_draft gate — the PR just left
// draft, and re-applying the gate here is what the transition exists to undo.
func lowerReadyReview(act config.Action) *config.Filter {
	return lowerExclude(act.Exclude)
}

// lowerIssueMatch lowers issue_matched's payload filters. The `assignee` key
// stays outside the IR: it resolves against the connector's `me` identity
// rather than a published fact, so it has no match key.
func lowerIssueMatch(act config.Action) *config.Filter {
	var sole *config.Filter
	if act.SoleAssignee {
		sole = config.FilterMatch("sole_assignee", true)
	}
	return config.FilterAnd(
		sole,
		matchIf("labels_any", act.LabelsAny),
		matchIf("labels_all", act.LabelsAll),
		lowerExclude(act.Exclude),
		matchIf("authors", act.Authors),
	)
}

// lowerMergeReady lowers merge_ready's `require_label` plus its `gates:` map.
// These gates are opt-OUT (absent means enforced — mergeGateOn), the opposite
// of the opt-IN not_draft toggle draftGate reads, which is why the same
// `not_draft` match key is lowered from a different reader here.
func lowerMergeReady(act config.Action) *config.Filter {
	var require *config.Filter
	if act.RequireLabel != "" {
		require = config.FilterMatch("require_label", act.RequireLabel)
	}
	gates := make([]*config.Filter, 0, len(mergeGateKeys))
	for _, k := range mergeGateKeys {
		gates = append(gates, config.FilterMatch(k, mergeGateOn(act.Gates, k)))
	}
	return config.FilterAnd(require, config.FilterAnd(gates...))
}

// lowerComment lowers a new_comment keep-condition. withAuthorBot reflects a
// real difference between the two sites: the webhook path also gates on
// author_bot, while the sweep's missed-comment recovery does not (its comment
// listing carries no account type). A hand-written `filter:` replaces both and
// applies uniformly.
func lowerComment(act config.Action, withAuthorBot bool) *config.Filter {
	var bot *config.Filter
	if withAuthorBot && act.AuthorBot != nil {
		bot = config.FilterMatch("author_bot", *act.AuthorBot)
	}
	return config.FilterAnd(
		matchIf("from_users", act.FromUsers),
		matchIf("ignore_users", act.IgnoreUsers),
		bot,
	)
}

// lowerChangesRequested lowers the changes_requested keep-condition: the
// reviewer's bot-ness, when the trigger constrains it.
func lowerChangesRequested(act config.Action) *config.Filter {
	if act.AuthorBot == nil {
		return nil
	}
	return config.FilterMatch("author_bot", *act.AuthorBot)
}

// --- facts builders --------------------------------------------------------

// prFilterFacts builds the facts a PR-shaped event filters on.
func prFilterFacts(headBranch, baseBranch, title, author string, labels []string, isDraft bool) map[string]any {
	return map[string]any{
		"head_branch": headBranch,
		"base_branch": baseBranch,
		"title":       title,
		"author":      author,
		"labels":      labels,
		"is_draft":    isDraft,
	}
}

// issueFilterFacts builds issue_matched's facts. head_branch/base_branch are
// present but empty: a legacy `exclude.branches` on an issue ran its globs
// against "" (config.Exclude.Matches with no branch), and the lowered Match
// has to do the same to stay bit-identical. Neither is a DECLARED fact, so no
// `filter:` can read them.
func issueFilterFacts(st issueMatchState, soleAssignee bool) map[string]any {
	return map[string]any{
		"head_branch":   "",
		"base_branch":   "",
		"title":         st.title,
		"author":        st.author,
		"labels":        st.labels,
		"is_draft":      false,
		"sole_assignee": soleAssignee,
	}
}

// mergeReadyFilterFacts builds merge_ready's facts from the composite gate.
// The gate query carries no title or head ref, so neither is declared.
func mergeReadyFilterFacts(gate *mergeGate) map[string]any {
	return map[string]any{
		"labels":              gate.Labels,
		"author":              gate.Author,
		"is_draft":            gate.IsDraft,
		"merge_state":         gate.MergeStateStatus,
		"review_decision":     gate.ReviewDecision,
		"non_author_approval": gate.NonAuthorApprove,
		"threads_resolved":    gate.ThreadsResolved,
	}
}

// commentFilterFacts builds new_comment's facts. The author here is the
// COMMENTER, not the PR author — `comment_author` says so in the name, which
// is the whole reason the unified grammar does not reuse `author`.
func commentFilterFacts(commentAuthor, body string, isBot bool) map[string]any {
	return map[string]any{
		"comment_author": commentAuthor,
		"comment_body":   body,
		"author_is_bot":  isBot,
	}
}
