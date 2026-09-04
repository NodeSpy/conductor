package migrate

import (
	"fmt"
	"path"
	"strings"

	"github.com/NodeSpy/conductor/internal/config"
	gh "github.com/NodeSpy/conductor/internal/integrations/github"
)

// githubTransform maps one legacy github integration to a connector entry +
// triggers. The rules/defaults most-specific-repo model flattens to
// per-trigger repos/exclude_repos filters with the SAME winner per repo:
// each rule becomes triggers scoped to its repo patterns, excluding patterns
// of any other rule that would out-rank it for a shared repo.
func githubTransform(name string, ref config.IntegrationRef, notes *[]string) (map[string]any, []config.TriggerSpec, error) {
	var cfg gh.Config
	if err := ref.Decode(&cfg); err != nil {
		return nil, nil, fmt.Errorf("github[%s]: decode: %w", name, err)
	}
	where := "github[" + name + "]"

	conn := map[string]any{"type": "github"}
	if cfg.App.AppID != 0 || cfg.App.PrivateKeyPath != "" || cfg.App.WebhookSecret != "" || cfg.App.VerifySig != nil {
		app := map[string]any{}
		if cfg.App.AppID != 0 {
			app["app_id"] = cfg.App.AppID
		}
		if cfg.App.PrivateKeyPath != "" {
			app["private_key_path"] = cfg.App.PrivateKeyPath
		}
		if cfg.App.WebhookSecret != "" {
			app["webhook_secret"] = cfg.App.WebhookSecret
		}
		if cfg.App.VerifySig != nil {
			app["verify_signature"] = *cfg.App.VerifySig
		}
		conn["app"] = app
	}
	if cfg.Token != "" {
		conn["token"] = cfg.Token
	}
	if cfg.Webhook.SmeeURL != "" || cfg.Webhook.Listen != "" || cfg.Webhook.Path != "" {
		wh := map[string]any{}
		if cfg.Webhook.SmeeURL != "" {
			wh["smee_url"] = cfg.Webhook.SmeeURL
		}
		if cfg.Webhook.Listen != "" {
			wh["listen"] = cfg.Webhook.Listen
		}
		if cfg.Webhook.Path != "" {
			wh["path"] = cfg.Webhook.Path
		}
		conn["webhook"] = wh
	}
	if cfg.Sweep.Enabled || len(cfg.Sweep.Repos) > 0 || cfg.Sweep.Interval != 0 || cfg.Sweep.MinInterval != 0 {
		sw := map[string]any{"enabled": cfg.Sweep.Enabled}
		if cfg.Sweep.Interval != 0 {
			sw["interval"] = cfg.Sweep.Interval.String()
		}
		if cfg.Sweep.MinInterval != 0 {
			sw["min_interval"] = cfg.Sweep.MinInterval.String()
		}
		if len(cfg.Sweep.Repos) > 0 {
			sw["repos"] = strSlice(cfg.Sweep.Repos)
		}
		conn["sweep"] = sw
	}
	if cfg.Identity.ReadToken != "" || cfg.Identity.WriteToken != "" || cfg.Identity.CommitAuthor != "" {
		id := map[string]any{}
		if cfg.Identity.ReadToken != "" {
			id["read_token"] = cfg.Identity.ReadToken
		}
		if cfg.Identity.WriteToken != "" {
			id["write_token"] = cfg.Identity.WriteToken
		}
		if cfg.Identity.CommitAuthor != "" {
			id["commit_author"] = cfg.Identity.CommitAuthor
		}
		conn["identity"] = id
	}
	if cfg.Retry.Max != 0 || cfg.Retry.Backoff != 0 {
		rt := map[string]any{}
		if cfg.Retry.Max != 0 {
			rt["max"] = cfg.Retry.Max
		}
		if cfg.Retry.Backoff != 0 {
			rt["backoff"] = cfg.Retry.Backoff.String()
		}
		conn["retry"] = rt
	}
	if len(cfg.ProjectMap) > 0 {
		pm := map[string]any{}
		for k, v := range cfg.ProjectMap {
			pm[k] = v
		}
		conn["project_map"] = pm
	}
	if cfg.ProjectRewrite.Org != "" {
		conn["project_rewrite"] = map[string]any{"org": cfg.ProjectRewrite.Org}
	}
	// Bot-authored comments: migrated connectors state the default explicitly
	// — replies back to a bot author are decline-only (an intended behavioral
	// change; legacy configs had no equivalent). Independent of ignore.users,
	// which skips the trigger entirely.
	conn["policy"] = map[string]any{"reply_to_bots": "decline_only"}
	*notes = append(*notes, fmt.Sprintf("%s: policy.reply_to_bots: decline_only (bot-authored comments get fixes, not pleasantries)", where))

	// me: — the legacy self-set was collected from defaults + rules (falling
	// back to reviewer/assignee logins, which the connector keeps doing at the
	// integration layer). Collect explicit me: from every rule.
	me := map[string]bool{}
	var meOrder []string
	for _, r := range append([]gh.Rule{cfg.Defaults}, cfg.Rules...) {
		for _, l := range r.Me.Logins {
			if !me[l] {
				me[l] = true
				meOrder = append(meOrder, l)
			}
		}
	}
	if len(meOrder) > 0 {
		conn["me"] = map[string]any{"logins": strSlice(meOrder)}
	}

	// Defaults with actions but NO rules never fired in the legacy engine
	// (resolve() only matches explicit rules) — surface that loudly.
	if len(cfg.Rules) == 0 && len(cfg.Defaults.Actions) > 0 {
		*notes = append(*notes, fmt.Sprintf("%s: defaults.actions with no rules: never fired in the legacy engine (no rule ever matched) — no triggers generated", where))
	}

	// Inert rule-level fields.
	for i, r := range append([]gh.Rule{cfg.Defaults}, cfg.Rules...) {
		rwhere := fmt.Sprintf("%s rules[%d]", where, i-1)
		if i == 0 {
			rwhere = where + " defaults"
		}
		if r.Workspace != "" {
			*notes = append(*notes, fmt.Sprintf("%s: workspace: %q was never read by the legacy engine — dropped (set workspace on the agent profile instead)", rwhere, r.Workspace))
		}
		if r.Match.Project != "" || r.Match.Status != "" {
			*notes = append(*notes, fmt.Sprintf("%s: match.project/match.status were reserved (never evaluated) — dropped", rwhere))
		}
	}

	var triggers []config.TriggerSpec
	for ri, rule := range cfg.Rules {
		rwhere := fmt.Sprintf("%s rules[%d]", where, ri)
		if len(rule.Match.Repos) == 0 {
			return nil, nil, fmt.Errorf("%s: rule has no match.repos — the legacy engine never matched it; delete it or give it repos", rwhere)
		}
		merged := gh.MergeRule(cfg.Defaults, rule)
		exclude := ruleExclusions(cfg.Rules, ri)
		for _, kind := range sortedKeys(merged.Actions) {
			if !gh.KnownKinds()[kind] {
				return nil, nil, fmt.Errorf("%s: unknown action kind %q", rwhere, kind)
			}
			set := merged.Actions[kind]
			for vi, act := range set {
				awhere := fmt.Sprintf("%s actions.%s[%d]", rwhere, kind, vi)
				steps, err := actionSteps(awhere, act, notes)
				if err != nil {
					return nil, nil, err
				}
				noteInertActionFields(awhere, act, notes)
				filters := actionFilters(act)
				filters["repos"] = strSlice(rule.Match.Repos)
				if len(exclude) > 0 {
					filters["exclude_repos"] = strSlice(exclude)
				}
				// The merged rule's reviewer/assignee apply when the action
				// itself set none (legacy gates read the ACTION's fields,
				// which mergeAction seeded from rule defaults — merged already
				// reflects that, so nothing extra to do here).
				spec := config.TriggerSpec{
					On:      name + "." + kind,
					Name:    act.Name,
					Enabled: act.Enabled,
					Shadow:  act.Shadow,
					Filters: filters,
					Steps:   steps,
				}
				if o := actionOptions(act); len(o) > 0 {
					spec.Options = o
				}
				triggers = append(triggers, spec)
			}
		}
	}
	return conn, triggers, nil
}

// ruleExclusions computes the repo patterns a rule's triggers must exclude to
// replicate most-specific-rule-wins: every pattern in ANOTHER rule that (a)
// can overlap one of this rule's patterns and (b) would win the legacy
// resolve() for a repo matching both (higher specificity, or equal
// specificity in an earlier rule).
func ruleExclusions(rules []gh.Rule, ri int) []string {
	var out []string
	seen := map[string]bool{}
	mine := rules[ri].Match.Repos
	for rj, other := range rules {
		if rj == ri {
			continue
		}
		for _, pj := range other.Match.Repos {
			if seen[pj] {
				continue
			}
			sj := gh.PatternSpecificity(pj)
			for _, pi := range mine {
				if !patternsOverlap(pi, pj) {
					continue
				}
				si := gh.PatternSpecificity(pi)
				if sj > si || (sj == si && rj < ri) {
					out = append(out, pj)
					seen[pj] = true
					break
				}
			}
		}
	}
	return out
}

// patternsOverlap reports whether two repo patterns could match the same
// repo. Literal/literal compares equality, literal/glob path.Match, and
// glob/glob is conservatively true.
func patternsOverlap(a, b string) bool {
	ga := strings.ContainsAny(a, "*?[")
	gb := strings.ContainsAny(b, "*?[")
	switch {
	case !ga && !gb:
		return a == b
	case !ga:
		ok, _ := path.Match(b, a)
		return ok
	case !gb:
		ok, _ := path.Match(a, b)
		return ok
	default:
		return true
	}
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sortStrings(out)
	return out
}
