package migrate

import (
	"fmt"
	"sort"
	"strings"

	"github.com/NodeSpy/conductor/internal/config"
	cronint "github.com/NodeSpy/conductor/internal/integrations/cron"
	rssint "github.com/NodeSpy/conductor/internal/integrations/rss"
	slackint "github.com/NodeSpy/conductor/internal/integrations/slack"
	webhookint "github.com/NodeSpy/conductor/internal/integrations/webhook"
)

func sortStrings(s []string) { sort.Strings(s) }

// slackTransform maps a legacy slack integration: connection tokens, one
// trigger per rule, and ack/on_done/on_fail feedback as at: start/done/fail
// hooks. A single-variant rule maps 1:1. A MULTI-variant rule becomes ONE
// trigger whose only step is parallel branches (one per enabled variant):
// the join is exactly the legacy HandleCompletion aggregation point — the
// at:done hooks fire once after ALL variants complete, at:fail once when any
// failed — so the migrated feedback timing is identical, not approximate.
func slackTransform(name string, ref config.IntegrationRef, notes *[]string) (map[string]any, []config.TriggerSpec, error) {
	var cfg slackint.Config
	if err := ref.Decode(&cfg); err != nil {
		return nil, nil, fmt.Errorf("slack[%s]: decode: %w", name, err)
	}
	conn := map[string]any{"type": "slack"}
	if cfg.AppToken != "" {
		conn["app_token"] = cfg.AppToken
	}
	if cfg.BotToken != "" {
		conn["bot_token"] = cfg.BotToken
	}
	var triggers []config.TriggerSpec
	for ri, rule := range cfg.Rules {
		where := fmt.Sprintf("slack[%s] triggers[%d]", name, ri)
		filters := map[string]any{}
		if rule.Reaction != "" {
			filters["reaction"] = rule.Reaction
		}
		if rule.Command != "" {
			filters["command"] = rule.Command
		}
		hooks := slackFeedbackHooks(name, rule, where)
		if len(rule.Actions) <= 1 {
			// The common case maps 1:1: one variant, one trigger, hooks on it.
			for vi, act := range rule.Actions {
				awhere := fmt.Sprintf("%s actions[%d]", where, vi)
				steps, err := actionSteps(awhere, act, notes)
				if err != nil {
					return nil, nil, err
				}
				noteInertActionFields(awhere, act, notes)
				triggers = append(triggers, config.TriggerSpec{
					On: name + "." + rule.On, Name: act.Name,
					Enabled: act.Enabled, Shadow: act.Shadow,
					Filters: copyMap(filters), Steps: steps, Hooks: hooks,
				})
			}
			continue
		}
		// Multi-variant: one trigger, parallel branches, hooks on the join.
		var branches [][]config.Step
		for vi, act := range rule.Actions {
			awhere := fmt.Sprintf("%s actions[%d]", where, vi)
			if !act.IsEnabled() {
				*notes = append(*notes, fmt.Sprintf("%s: variant disabled — no branch generated", awhere))
				continue
			}
			steps, err := actionSteps(awhere, act, notes)
			if err != nil {
				return nil, nil, err
			}
			noteInertActionFields(awhere, act, notes)
			if act.Shadow != nil {
				for i := range steps {
					steps[i].Shadow = act.Shadow
				}
			}
			prefix := act.Name
			if prefix == "" {
				prefix = fmt.Sprintf("v%d", vi+1)
			}
			branches = append(branches, prefixBranchSteps(prefix, steps))
		}
		if len(branches) == 0 {
			*notes = append(*notes, fmt.Sprintf("%s: every variant disabled — no trigger generated (legacy fired nothing)", where))
			continue
		}
		*notes = append(*notes, fmt.Sprintf("%s: %d variants merged into parallel branches — on_done/on_fail fire once after all complete, matching legacy aggregation", where, len(branches)))
		triggers = append(triggers, config.TriggerSpec{
			On:      name + "." + rule.On,
			Filters: copyMap(filters),
			Steps: []config.Step{{
				ID:       "variants",
				Parallel: &config.ParallelSpec{Branches: branches},
			}},
			Hooks: hooks,
		})
	}
	return conn, triggers, nil
}

// prefixBranchSteps renames a branch's step ids with a variant prefix so ids
// stay unique across parallel branches, rewriting intra-branch references
// ({{.<id>.…}} and the legacy steps.<id>. spelling) in every templated field.
func prefixBranchSteps(prefix string, steps []config.Step) []config.Step {
	rename := map[string]string{}
	for i := range steps {
		old := steps[i].ID
		if old == "" {
			old = fmt.Sprintf("step%d", i+1)
		}
		rename[old] = prefix + "-" + old
		steps[i].ID = prefix + "-" + old
	}
	sub := func(v string) string {
		for old, neu := range rename {
			v = strings.ReplaceAll(v, "{{."+old+".", "{{."+neu+".")
			v = strings.ReplaceAll(v, "steps."+old+".", "steps."+neu+".")
		}
		return v
	}
	var subAny func(v any) any
	subAny = func(v any) any {
		switch x := v.(type) {
		case string:
			return sub(x)
		case map[string]any:
			out := make(map[string]any, len(x))
			for k, e := range x {
				out[k] = subAny(e)
			}
			return out
		case []any:
			out := make([]any, len(x))
			for i, e := range x {
				out[i] = subAny(e)
			}
			return out
		}
		return v
	}
	for i := range steps {
		st := &steps[i]
		st.If = sub(st.If)
		st.Prompt = sub(st.Prompt)
		st.WorkDir = sub(st.WorkDir)
		for j := range st.Command {
			st.Command[j] = sub(st.Command[j])
		}
		for k, v := range st.Env {
			st.Env[k] = sub(v)
		}
		if st.Options != nil {
			st.Options = subAny(st.Options).(map[string]any)
		}
		if st.With != nil {
			st.With = subAny(st.With).(map[string]any)
		}
	}
	return steps
}

// slackFeedbackHooks maps ack/on_done/on_fail Feedback blocks to hooks.
func slackFeedbackHooks(conn string, rule slackint.Rule, where string) []config.Hook {
	var hooks []config.Hook
	add := func(at string, f *slackint.Feedback) {
		if f == nil {
			return
		}
		if f.React != "" {
			hooks = append(hooks, config.Hook{
				At: at, Uses: conn + ".react",
				Options: map[string]any{
					"channel": "{{.slack.channel}}", "ts": "{{.slack.ts}}", "emoji": f.React,
				},
			})
		}
		if f.Say != "" {
			opts := map[string]any{"channel": "{{.slack.channel}}", "text": f.Say}
			if f.Ephemeral {
				opts["ephemeral"] = true
				opts["user"] = "{{.slack.user}}"
			}
			if f.InThread == nil || *f.InThread {
				opts["thread_ts"] = "{{.slack.thread_ts}}"
			}
			hooks = append(hooks, config.Hook{At: at, Uses: conn + ".post", Options: opts})
		}
	}
	add("start", rule.Ack)
	add("done", rule.OnDone)
	add("fail", rule.OnFail)
	return hooks
}

// cronTransform maps schedules to the connection block + one trigger each.
func cronTransform(name string, ref config.IntegrationRef, notes *[]string) (map[string]any, []config.TriggerSpec, error) {
	var cfg cronint.Config
	if err := ref.Decode(&cfg); err != nil {
		return nil, nil, fmt.Errorf("cron[%s]: decode: %w", name, err)
	}
	schedules := map[string]any{}
	var triggers []config.TriggerSpec
	for si, s := range cfg.Schedules {
		where := fmt.Sprintf("cron[%s] schedules[%d] (%s)", name, si, s.Name)
		if s.Name == "" {
			return nil, nil, fmt.Errorf("%s: schedule has no name", where)
		}
		sch := map[string]any{}
		if s.Cron != "" {
			sch["cron"] = s.Cron
		}
		if s.Every != 0 {
			sch["every"] = s.Every.String()
		}
		if s.RunOnStart {
			sch["run_on_start"] = true
		}
		schedules[s.Name] = sch
		steps, err := actionSteps(where, s.Action, notes)
		if err != nil {
			return nil, nil, err
		}
		noteInertActionFields(where, s.Action, notes)
		triggers = append(triggers, config.TriggerSpec{
			On: name + "." + s.Name, Steps: steps,
			Enabled: s.Action.Enabled, Shadow: s.Action.Shadow,
		})
	}
	conn := map[string]any{"type": "cron", "schedules": schedules}
	return conn, triggers, nil
}

// webhookTransform maps sources to the connection block + triggers.
func webhookTransform(name string, ref config.IntegrationRef, notes *[]string) (map[string]any, []config.TriggerSpec, error) {
	var cfg webhookint.Config
	if err := ref.Decode(&cfg); err != nil {
		return nil, nil, fmt.Errorf("webhook[%s]: decode: %w", name, err)
	}
	conn := map[string]any{"type": "webhook"}
	if cfg.Listen != "" {
		conn["listen"] = cfg.Listen
	}
	if cfg.SmeeURL != "" {
		conn["smee_url"] = cfg.SmeeURL
	}
	sources := map[string]any{}
	var triggers []config.TriggerSpec
	for si, s := range cfg.Sources {
		where := fmt.Sprintf("webhook[%s] sources[%d] (%s)", name, si, s.Name)
		if s.Name == "" {
			return nil, nil, fmt.Errorf("%s: source has no name", where)
		}
		src := map[string]any{}
		if s.Path != "" {
			src["path"] = s.Path
		}
		if s.Sign.Header != "" || s.Sign.Secret != "" || s.Sign.Scheme != "" {
			sign := map[string]any{}
			if s.Sign.Header != "" {
				sign["header"] = s.Sign.Header
			}
			if s.Sign.Secret != "" {
				sign["secret"] = s.Sign.Secret
			}
			if s.Sign.Scheme != "" {
				sign["scheme"] = s.Sign.Scheme
			}
			src["sign"] = sign
		}
		if s.Match != "" {
			src["match"] = s.Match
		}
		if s.Title != "" {
			src["title"] = s.Title
		}
		if s.Dedup != "" {
			src["dedup"] = s.Dedup
		}
		sources[s.Name] = src
		for vi, act := range s.Actions {
			awhere := fmt.Sprintf("%s actions[%d]", where, vi)
			steps, err := actionSteps(awhere, act, notes)
			if err != nil {
				return nil, nil, err
			}
			noteInertActionFields(awhere, act, notes)
			triggers = append(triggers, config.TriggerSpec{
				On: name + "." + s.Name, Name: act.Name,
				Enabled: act.Enabled, Shadow: act.Shadow,
				Repo: s.Repo, Steps: steps,
			})
		}
	}
	conn["sources"] = sources
	return conn, triggers, nil
}

// rssTransform maps feeds to the connection block + one trigger per feed with
// the feed's match as a trigger filter.
func rssTransform(name string, ref config.IntegrationRef, notes *[]string) (map[string]any, []config.TriggerSpec, error) {
	var cfg rssint.Config
	if err := ref.Decode(&cfg); err != nil {
		return nil, nil, fmt.Errorf("rss[%s]: decode: %w", name, err)
	}
	feeds := map[string]any{}
	var triggers []config.TriggerSpec
	for fi, f := range cfg.Feeds {
		where := fmt.Sprintf("rss[%s] feeds[%d] (%s)", name, fi, f.Name)
		if f.Name == "" {
			return nil, nil, fmt.Errorf("%s: feed has no name", where)
		}
		fd := map[string]any{"url": f.URL}
		if f.Interval != 0 {
			fd["interval"] = f.Interval.String()
		}
		feeds[f.Name] = fd
		filters := map[string]any{}
		if f.Match != "" {
			filters["match"] = f.Match
		}
		for vi, act := range f.Actions {
			awhere := fmt.Sprintf("%s actions[%d]", where, vi)
			steps, err := actionSteps(awhere, act, notes)
			if err != nil {
				return nil, nil, err
			}
			noteInertActionFields(awhere, act, notes)
			triggers = append(triggers, config.TriggerSpec{
				On: name + "." + f.Name, Name: act.Name,
				Enabled: act.Enabled, Shadow: act.Shadow,
				Filters: copyMap(filters), Repo: f.Repo, Steps: steps,
			})
		}
	}
	conn := map[string]any{"type": "rss", "feeds": feeds}
	return conn, triggers, nil
}

func copyMap(m map[string]any) map[string]any {
	if len(m) == 0 {
		return nil
	}
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}
