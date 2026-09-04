package migrate

import (
	"fmt"
	"sort"

	"github.com/NodeSpy/paseo-conductor/internal/config"
	cronint "github.com/NodeSpy/paseo-conductor/internal/integrations/cron"
	pdint "github.com/NodeSpy/paseo-conductor/internal/integrations/pagerduty"
	rssint "github.com/NodeSpy/paseo-conductor/internal/integrations/rss"
	sentryint "github.com/NodeSpy/paseo-conductor/internal/integrations/sentry"
	slackint "github.com/NodeSpy/paseo-conductor/internal/integrations/slack"
	webhookint "github.com/NodeSpy/paseo-conductor/internal/integrations/webhook"
)

func sortStrings(s []string) { sort.Strings(s) }

// slackTransform maps a legacy slack integration: connection tokens, one
// trigger per rule, and ack/on_done/on_fail feedback as at: start/done/fail
// hooks on that trigger (the new engine's hooks are per-trigger, matching the
// single-action case exactly; multi-variant aggregation timing is noted).
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
		// Each variant fired independently in legacy — one trigger per
		// variant, in order. Feedback hooks ride the first variant's trigger
		// (legacy fired ack once per event and aggregated on_done/on_fail
		// across variants; the single-variant case — the normal one — is
		// exactly equivalent, the multi-variant timing shift is noted).
		hooks := slackFeedbackHooks(name, rule, where)
		if len(rule.Actions) > 1 && (rule.OnDone != nil || rule.OnFail != nil) {
			*notes = append(*notes, fmt.Sprintf("%s: on_done/on_fail aggregated across %d variants in legacy; now fire when the first variant's trigger completes", where, len(rule.Actions)))
		}
		for vi, act := range rule.Actions {
			awhere := fmt.Sprintf("%s actions[%d]", where, vi)
			steps, err := actionSteps(awhere, act, notes)
			if err != nil {
				return nil, nil, err
			}
			noteInertActionFields(awhere, act, notes)
			spec := config.TriggerSpec{
				On: name + "." + rule.On, Name: act.Name,
				Enabled: act.Enabled, Shadow: act.Shadow,
				Filters: copyMap(filters), Steps: steps,
			}
			if vi == 0 {
				spec.Hooks = hooks
			}
			triggers = append(triggers, spec)
		}
	}
	return conn, triggers, nil
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

// sentryTransform maps rules to triggers (order preserved: sentry triggers
// stay first-match-wins, mirroring legacy rules).
func sentryTransform(name string, ref config.IntegrationRef, notes *[]string) (map[string]any, []config.TriggerSpec, error) {
	var cfg sentryint.Config
	if err := ref.Decode(&cfg); err != nil {
		return nil, nil, fmt.Errorf("sentry[%s]: decode: %w", name, err)
	}
	conn := map[string]any{"type": "sentry"}
	if cfg.Listen != "" {
		conn["listen"] = cfg.Listen
	}
	if cfg.SmeeURL != "" {
		conn["smee_url"] = cfg.SmeeURL
	}
	if cfg.Path != "" {
		conn["path"] = cfg.Path
	}
	if cfg.ClientSecret != "" {
		conn["client_secret"] = cfg.ClientSecret
	}
	var triggers []config.TriggerSpec
	for ri, rule := range cfg.Rules {
		where := fmt.Sprintf("sentry[%s] rules[%d]", name, ri)
		filters := map[string]any{}
		if len(rule.Match.Projects) > 0 {
			filters["projects"] = strSlice(rule.Match.Projects)
		}
		if len(rule.Match.Levels) > 0 {
			filters["levels"] = strSlice(rule.Match.Levels)
		}
		if len(rule.Match.Environments) > 0 {
			filters["environments"] = strSlice(rule.Match.Environments)
		}
		for vi, act := range rule.Actions {
			awhere := fmt.Sprintf("%s actions[%d]", where, vi)
			steps, err := actionSteps(awhere, act, notes)
			if err != nil {
				return nil, nil, err
			}
			noteInertActionFields(awhere, act, notes)
			triggers = append(triggers, config.TriggerSpec{
				On: name + ".alert", Name: act.Name,
				Enabled: act.Enabled, Shadow: act.Shadow,
				Filters: copyMap(filters), Repo: rule.Repo, Steps: steps,
			})
		}
	}
	return conn, triggers, nil
}

// pagerdutyTransform mirrors sentryTransform for pagerduty incidents.
func pagerdutyTransform(name string, ref config.IntegrationRef, notes *[]string) (map[string]any, []config.TriggerSpec, error) {
	var cfg pdint.Config
	if err := ref.Decode(&cfg); err != nil {
		return nil, nil, fmt.Errorf("pagerduty[%s]: decode: %w", name, err)
	}
	conn := map[string]any{"type": "pagerduty"}
	if cfg.Listen != "" {
		conn["listen"] = cfg.Listen
	}
	if cfg.SmeeURL != "" {
		conn["smee_url"] = cfg.SmeeURL
	}
	if cfg.Path != "" {
		conn["path"] = cfg.Path
	}
	if cfg.SigningSecret != "" {
		conn["signing_secret"] = cfg.SigningSecret
	}
	var triggers []config.TriggerSpec
	for ri, rule := range cfg.Rules {
		where := fmt.Sprintf("pagerduty[%s] rules[%d]", name, ri)
		filters := map[string]any{}
		if len(rule.Match.EventTypes) > 0 {
			filters["event_types"] = strSlice(rule.Match.EventTypes)
		}
		if len(rule.Match.Services) > 0 {
			filters["services"] = strSlice(rule.Match.Services)
		}
		if len(rule.Match.Urgencies) > 0 {
			filters["urgencies"] = strSlice(rule.Match.Urgencies)
		}
		if len(rule.Match.Priorities) > 0 {
			filters["priorities"] = strSlice(rule.Match.Priorities)
		}
		for vi, act := range rule.Actions {
			awhere := fmt.Sprintf("%s actions[%d]", where, vi)
			steps, err := actionSteps(awhere, act, notes)
			if err != nil {
				return nil, nil, err
			}
			noteInertActionFields(awhere, act, notes)
			triggers = append(triggers, config.TriggerSpec{
				On: name + ".incident", Name: act.Name,
				Enabled: act.Enabled, Shadow: act.Shadow,
				Filters: copyMap(filters), Repo: rule.Repo, Steps: steps,
			})
		}
	}
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

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
