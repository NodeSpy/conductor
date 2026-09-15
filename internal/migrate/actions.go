package migrate

import (
	"fmt"

	"github.com/NodeSpy/conductor/internal/config"
	gh "github.com/NodeSpy/conductor/internal/integrations/github"
)

// actionSteps maps one legacy action's WORK — its steps list, or the action
// itself — onto new-schema steps. Trigger-level concerns (filter, options,
// name, enabled, shadow) are extracted by the caller from the top action; a
// filter field set on a nested step was inert in the legacy engine and is
// dropped with a summary note, never silently.
func actionSteps(where string, a config.Action, notes *[]string) ([]config.Step, error) {
	if len(a.Steps) > 0 {
		steps := make([]config.Step, 0, len(a.Steps))
		for i, sub := range a.Steps {
			if len(sub.Steps) > 0 {
				return nil, fmt.Errorf("%s: step %d has nested steps — the legacy engine never executed nested steps, migrate it by hand", where, i+1)
			}
			st, err := oneStep(fmt.Sprintf("%s step %d", where, i+1), sub, notes)
			if err != nil {
				return nil, err
			}
			noteInertStepFilters(fmt.Sprintf("%s step %d", where, i+1), sub, notes)
			steps = append(steps, st)
		}
		return steps, nil
	}
	st, err := oneStep(where, a, notes)
	if err != nil {
		return nil, err
	}
	return []config.Step{st}, nil
}

// oneStep maps a single legacy action to a step.
func oneStep(where string, a config.Action, notes *[]string) (config.Step, error) {
	st := config.Step{
		ID:      a.ID,
		If:      a.If,
		WorkDir: a.WorkDir,
		Env:     a.Env,
		Backend: a.Backend,
	}
	if a.Retry != nil {
		st.Retry = &config.RetrySpec{
			WhileOutputMatches: a.Retry.WhileOutputMatches,
			Interval:           a.Retry.Interval,
			Timeout:            a.Retry.Timeout,
		}
	}
	switch a.Type {
	case "agent":
		st.Type = "agent"
		st.Agent = a.Agent
		st.Prompt = a.Prompt
		st.Checkout = a.Checkout
		st.OutputSchema = a.OutputSchema
		st.Background = a.Background
		st.Handoff = a.Handoff
	case "command":
		st.Type = "command"
		st.Command = a.Command
		if a.Checkout != "" {
			// The legacy engine passed checkout through to command dispatch
			// only for workdir resolution; carry it as a note.
			*notes = append(*notes, fmt.Sprintf("%s: command checkout: %q carried via workdir semantics", where, a.Checkout))
		}
	case "":
		return st, fmt.Errorf("%s: action has no type", where)
	default:
		return st, fmt.Errorf("%s: unmappable action type %q (agent|command)", where, a.Type)
	}
	return st, nil
}

// noteInertStepFilters records filter-shaped fields on a nested step: the
// legacy engine only consulted them on the TOP-level action, so they were
// inert — dropping them changes nothing, but it is said out loud.
func noteInertStepFilters(where string, a config.Action, notes *[]string) {
	inert := []struct {
		set  bool
		name string
	}{
		{len(a.LabelsAny) > 0, "labels_any"},
		{len(a.LabelsAll) > 0, "labels_all"},
		{len(a.Authors) > 0, "authors"},
		{len(a.FromUsers) > 0, "from_users"},
		{len(a.IgnoreUsers) > 0, "ignore_users"},
		{len(a.IgnoreChecks) > 0, "ignore_checks"},
		{a.RequireLabel != "", "require_label"},
		{a.SoleAssignee, "sole_assignee"},
		{len(a.Gates) > 0, "gates"},
		{!a.Exclude.Empty(), "exclude"},
		{len(a.Reviewer.Logins)+len(a.Reviewer.Teams) > 0, "reviewer"},
		{len(a.Assignee.Logins) > 0, "assignee"},
	}
	for _, f := range inert {
		if f.set {
			*notes = append(*notes, fmt.Sprintf("%s: %s was set on a nested step — inert in the legacy engine, dropped", where, f.name))
		}
	}
}

// actionFilter rewrites a top-level github action's filter fields as the one
// unified `filter:` object (docs/design/unified-filter-phase2.md): renamed
// match keys, `exclude:` arms as `not_…` denials, and the `gates:` map as
// explicit conjuncts.
//
// Two translations are worth spelling out.
//
// A legacy gate set to FALSE waived it. There is nothing to write for that
// now: a filter REPLACES the event's intrinsic default, so a waived gate is
// simply a conjunct the filter does not carry.
//
// merge_ready's gates are the mirror image — absent meant ENFORCED — so every
// on-gate is emitted explicitly whenever that kind gets a filter at all.
// Leaving them implicit would be a silent relaxation of the one event whose
// default is not "fire".
func actionFilter(kind string, a config.Action) map[string]any {
	f := map[string]any{}
	if a.SoleAssignee {
		f["sole_assignee"] = true
	}
	if len(a.LabelsAny) > 0 {
		f["label_any"] = strSlice(a.LabelsAny)
	}
	if len(a.LabelsAll) > 0 {
		f["label_all"] = strSlice(a.LabelsAll)
	}
	if len(a.Authors) > 0 {
		f["author"] = strSlice(a.Authors)
	}
	if len(a.FromUsers) > 0 {
		f["comment_author"] = strSlice(a.FromUsers)
	}
	if len(a.IgnoreUsers) > 0 {
		f["not_comment_author"] = strSlice(a.IgnoreUsers)
	}
	if a.RequireLabel != "" {
		f["require_label"] = a.RequireLabel
	}
	if kind == "merge_ready" {
		for _, k := range gh.MergeGateKeys() {
			if !gh.MergeGateOn(a.Gates, k) {
				continue
			}
			if k == "not_draft" {
				f["not_draft"] = true
			} else {
				f[k] = true
			}
		}
	} else if gateTruthy(a.Gates["not_draft"]) {
		f["not_draft"] = true
	}
	if len(a.Exclude.Branches) > 0 {
		f["not_branch"] = strSlice(a.Exclude.Branches)
	}
	if len(a.Exclude.Labels) > 0 {
		f["not_label_any"] = strSlice(a.Exclude.Labels)
	}
	if len(a.Exclude.Title) > 0 {
		f["not_title"] = strSlice(a.Exclude.Title)
	}
	return f
}

// gateTruthy mirrors the legacy opt-in gate reading (absent or an explicit
// false/no/"" is off).
func gateTruthy(v any) bool {
	switch x := v.(type) {
	case bool:
		return x
	case string:
		return x != "" && x != "false" && x != "no"
	default:
		return false
	}
}

// noteDroppedGates records `gates:` keys with no unified spelling. The
// GraphQL-backed issue gates are the only ones: they read an enrichment query
// rather than a published fact, so they are not expressible as a filter key
// and no connectors-model config could set them.
func noteDroppedGates(where string, a config.Action, notes *[]string) {
	for _, k := range []string{"no_branch", "project"} {
		if _, ok := a.Gates[k]; ok {
			*notes = append(*notes, fmt.Sprintf("%s: gates.%s has no unified `filter:` spelling (it reads a GraphQL enrichment, not a published fact) — dropped", where, k))
		}
	}
}

// actionOptions extracts a top-level github action's source-side options —
// including the four former `filters:` keys that were never predicates over
// the event: the reviewer/assignee identity gates, per-check suppression, and
// the release prerelease switch.
func actionOptions(a config.Action) map[string]any {
	o := map[string]any{}
	if len(a.Reviewer.Logins) > 0 || len(a.Reviewer.Teams) > 0 {
		o["reviewer"] = actorsMap(a.Reviewer)
	}
	if len(a.Assignee.Logins) > 0 || len(a.Assignee.Teams) > 0 {
		o["assignee"] = actorsMap(a.Assignee)
	}
	if len(a.IgnoreChecks) > 0 {
		o["ignore_checks"] = strSlice(a.IgnoreChecks)
	}
	if a.IncludePrereleases {
		o["include_prereleases"] = true
	}
	if a.MaxAttemptsPerHead != 0 {
		o["max_attempts_per_head"] = a.MaxAttemptsPerHead
	}
	if a.FlakyRerun.Enabled || a.FlakyRerun.Max != 0 {
		fr := map[string]any{"enabled": a.FlakyRerun.Enabled}
		if a.FlakyRerun.Max != 0 {
			fr["max"] = a.FlakyRerun.Max
		}
		o["flaky_rerun"] = fr
	}
	if a.StuckAfter != 0 {
		o["stuck_after"] = a.StuckAfter.String()
	}
	if a.PollInterval != 0 {
		o["poll_interval"] = a.PollInterval.String()
	}
	return o
}

// noteInertActionFields records top-level action fields that were dead in the
// legacy engine (decoded but never read) — dropped with a note.
func noteInertActionFields(where string, a config.Action, notes *[]string) {
	if len(a.Project) > 0 {
		*notes = append(*notes, fmt.Sprintf("%s: project: was never evaluated by the legacy engine (the issue_matched project gate lives under gates.project) — dropped", where))
	}
	if a.Method != "" {
		*notes = append(*notes, fmt.Sprintf("%s: method: %q was never evaluated by the legacy engine — dropped", where, a.Method))
	}
}

func actorsMap(a config.Actors) map[string]any {
	m := map[string]any{}
	if len(a.Logins) > 0 {
		m["logins"] = strSlice(a.Logins)
	}
	if len(a.Teams) > 0 {
		m["teams"] = strSlice(a.Teams)
	}
	return m
}

func strSlice(in []string) []any {
	out := make([]any, len(in))
	for i, s := range in {
		out[i] = s
	}
	return out
}
