package migrate

import (
	"fmt"

	"github.com/NodeSpy/conductor/internal/config"
)

// actionSteps maps one legacy action's WORK — its steps list, or the action
// itself — onto new-schema steps. Trigger-level concerns (filters, options,
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
		st.RerequestReview = a.RerequestReview
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

// actionFilters extracts a top-level github action's filter fields into the
// trigger's filters map.
func actionFilters(a config.Action) map[string]any {
	f := map[string]any{}
	if len(a.Reviewer.Logins) > 0 || len(a.Reviewer.Teams) > 0 {
		f["reviewer"] = actorsMap(a.Reviewer)
	}
	if len(a.Assignee.Logins) > 0 || len(a.Assignee.Teams) > 0 {
		f["assignee"] = actorsMap(a.Assignee)
	}
	if a.SoleAssignee {
		f["sole_assignee"] = true
	}
	if len(a.LabelsAny) > 0 {
		f["labels_any"] = strSlice(a.LabelsAny)
	}
	if len(a.LabelsAll) > 0 {
		f["labels_all"] = strSlice(a.LabelsAll)
	}
	if len(a.Authors) > 0 {
		f["authors"] = strSlice(a.Authors)
	}
	if len(a.FromUsers) > 0 {
		f["from_users"] = strSlice(a.FromUsers)
	}
	if len(a.IgnoreUsers) > 0 {
		f["ignore_users"] = strSlice(a.IgnoreUsers)
	}
	if len(a.IgnoreChecks) > 0 {
		f["ignore_checks"] = strSlice(a.IgnoreChecks)
	}
	if a.RequireLabel != "" {
		f["require_label"] = a.RequireLabel
	}
	if a.IncludePrereleases {
		f["include_prereleases"] = true
	}
	if len(a.Gates) > 0 {
		f["gates"] = a.Gates
	}
	if !a.Exclude.Empty() {
		ex := map[string]any{}
		if len(a.Exclude.Branches) > 0 {
			ex["branches"] = strSlice(a.Exclude.Branches)
		}
		if len(a.Exclude.Labels) > 0 {
			ex["labels"] = strSlice(a.Exclude.Labels)
		}
		if len(a.Exclude.Title) > 0 {
			ex["title"] = strSlice(a.Exclude.Title)
		}
		f["exclude"] = ex
	}
	return f
}

// actionOptions extracts a top-level github action's source-side options.
func actionOptions(a config.Action) map[string]any {
	o := map[string]any{}
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
