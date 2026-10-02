// Package github is conductor's BUNDLED github source: the in-binary face of
// pkg/githubkit/ghsource. It owns what is conductor's own — the legacy
// `integrations: github` YAML (rules/defaults of config.Action, retry policy,
// the retired app-block webhook keys), the full config.Action merge the
// config migration also uses, the core.Integration registration, and the
// seams cmd/conductor type-asserts on (AppToken, RetryPolicy/IdentityTokens,
// SweepOnce/SweepNow, Force, own-status) — and delegates every event decision
// to the kit, which is the same code the external conductor-github plugin
// runs.
//
// The adapter is a type boundary and nothing more: config.Action becomes a
// ghsource.Action carrying the config.Action in Ext (merged by this package's
// mergeAction), and each ghsource.Trigger becomes a core.Trigger whose Action
// is that merged config.Action.
package github

import (
	"context"
	"errors"
	"fmt"
	"path"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/core"
	"github.com/NodeSpy/conductor/pkg/githubkit/ghplugin"
	"github.com/NodeSpy/conductor/pkg/githubkit/ghsource"
)

func init() { core.Register("github", newIntegration) }

// Config is a github integration instance's configuration — the legacy
// `integrations:` shape, and what the connectors-model lowering
// (internal/connector.githubImpl.Source) builds and round-trips through YAML.
type Config struct {
	App AppConfig `yaml:"app"`
	// Token is the App-less credential: a PAT used for reads/enrichment when
	// no GitHub App is configured. The full chain is app → token → the `gh`
	// binary (`gh auth token`); with no App, events arrive via a plain
	// webhook (+ secret) or the sweep, and repo globs are not expandable.
	Token    string        `yaml:"token"`
	Webhook  WebhookConfig `yaml:"webhook"`
	Sweep    SweepConfig   `yaml:"sweep"`
	Defaults Rule          `yaml:"defaults"`
	Rules    []Rule        `yaml:"rules"`

	// ProjectMap remaps a repo (owner/name) to the paseo project name of an
	// existing workspace, so checkouts reuse it instead of cloning a fresh one.
	// Keys are matched case-insensitively; only affects checkout resolution.
	ProjectMap map[string]string `yaml:"project_map"`

	// ProjectRewrite is a blanket fallback applied to every repo in this instance
	// that has no explicit ProjectMap entry. See ghsource.ProjectRewrite.
	ProjectRewrite ProjectRewrite `yaml:"project_rewrite"`

	// Identity is this integration's credential policy: which token reads vs
	// writes, and commit authorship. Wired via the dispatchTuner seam in main.
	Identity Identity `yaml:"identity"`

	// Retry tunes re-attempts of a transient `paseo run` failure (git-lock/timeout
	// under a sweep fan-out). Dispatch-level, but declared here since it's this
	// integration's agents that contend.
	Retry config.Retry `yaml:"retry"`
}

// Identity, ProjectRewrite, WebhookConfig and Match are the kit's own: the
// source reads them as they are.
type (
	Identity       = ghsource.Identity
	ProjectRewrite = ghsource.ProjectRewrite
	WebhookConfig  = ghsource.WebhookConfig
	Match          = ghsource.Match
)

// AppConfig holds the GitHub App credentials — and ONLY those. Webhook
// verification (the secret and the signature switch) is a property of the
// receiver, not of App auth: an App-less instance verifies deliveries too, and
// writing `app: { webhook_secret: … }` with no App at all was the tell. Both
// keys live on WebhookConfig now.
type AppConfig struct {
	AppID          int64  `yaml:"app_id"`
	PrivateKeyPath string `yaml:"private_key_path"`

	// LegacyWebhookSecret and LegacyVerifySig are the retired app-block webhook
	// keys. They are NOT part of the schema — nothing reads them for behavior.
	// They are retained so a config that still carries them fails naming the
	// new location (see ErrAppWebhookMoved) instead of dying on a generic
	// unknown-field error or, worse, having the key silently dropped by a
	// non-strict decode and verification quietly fall back to its default. The
	// migrator reads them to rewrite the block.
	LegacyWebhookSecret string `yaml:"webhook_secret"`
	LegacyVerifySig     *bool  `yaml:"verify_signature"`
}

// LegacyWebhookKeys reports whether the retired app-block webhook keys are set
// — the detection behind ErrAppWebhookMoved.
func (a AppConfig) LegacyWebhookKeys() bool {
	return a.LegacyWebhookSecret != "" || a.LegacyVerifySig != nil
}

// ErrAppWebhookMoved is the migration-specific rejection for a config still
// carrying the webhook keys under `app:`. Every layer that decodes an AppConfig
// wraps it with its own instance prefix, so the message names both the old and
// the new home rather than reading like a typo.
var ErrAppWebhookMoved = errors.New("app.webhook_secret moved to webhook.secret " +
	"(and app.verify_signature → webhook.verify_signature) — run 'conductor config migrate'")

// SweepConfig configures the catch-up sweep (see ghsource.SweepConfig, which
// it converts to: the same fields, with config.Duration's YAML spelling).
type SweepConfig struct {
	// Enabled defaults TRUE (nil → on). Without a webhook the sweep is the only
	// event source, so on-by-default is what makes conductor work out of the box;
	// set it false to turn polling off.
	Enabled *bool `yaml:"enabled"`
	// Interval is the CEILING of the adaptive cadence (default 1h). Only used
	// in webhook mode.
	Interval config.Duration `yaml:"interval"`
	// MinInterval is the tight cadence (default 2m): the adaptive floor in
	// webhook mode, and the fixed poll interval in no-webhook mode.
	MinInterval config.Duration `yaml:"min_interval"`
	// Repos optionally NARROWS the sweep to specific repos or owner-globs
	// (`acme/*`). Omitted → every repo across every App installation.
	Repos []string `yaml:"repos"`
}

// IsEnabled reports whether the sweep runs. Absent (nil) means yes — the sweep is
// on by default so a conductor with no webhook still receives events.
func (s SweepConfig) IsEnabled() bool { return s.Enabled == nil || *s.Enabled }

func (s SweepConfig) kit() ghsource.SweepConfig {
	return ghsource.SweepConfig{Enabled: s.Enabled, Interval: s.Interval.D(), MinInterval: s.MinInterval.D(), Repos: s.Repos}
}

// Rule is one entry in the instance's `rules` list (or the `defaults` block).
type Rule struct {
	Match     Match                       `yaml:"match"`
	Me        config.Actors               `yaml:"me"`       // your GitHub login(s) — defines "you"
	Reviewer  config.Actors               `yaml:"reviewer"` // whose requested review triggers review_requested
	Assignee  config.Actors               `yaml:"assignee"` // whose assignment triggers issue_assigned
	Workspace string                      `yaml:"workspace"`
	Actions   map[string]config.ActionSet `yaml:"actions"` // kind -> one or more named variants
}

// Integration implements core.Integration for one github instance: the kit's
// Source behind conductor's types.
type Integration struct {
	name string
	cfg  Config
	src  *ghsource.Source
}

func newIntegration(name string, decode func(any) error) (core.Integration, error) {
	var cfg Config
	if err := decode(&cfg); err != nil {
		return nil, fmt.Errorf("github[%s]: decode config: %w", name, err)
	}
	if cfg.App.LegacyWebhookKeys() {
		return nil, fmt.Errorf("github[%s]: %w", name, ErrAppWebhookMoved)
	}
	src, err := ghsource.New(name, cfg.kit())
	if err != nil {
		return nil, err
	}
	return &Integration{name: name, cfg: cfg, src: src}, nil
}

// NewConnector builds the bundled connectors-model source: kc is the source
// config the connector lowered (ghsource.Connection.SourceConfig — the same
// function the conductor-github plugin calls), each Action carrying its
// lowered config.Action in Ext. legacy carries what is conductor's own — the
// retry policy, the sweep block as written, and the lowered rules for the
// CLI's action enumeration.
func NewConnector(name string, kc ghsource.Config, legacy Config) (*Integration, error) {
	kc.MergeExt = mergeExt
	src, err := ghsource.New(name, kc)
	if err != nil {
		return nil, err
	}
	return &Integration{name: name, cfg: legacy, src: src}, nil
}

// kit converts the instance config to the source's: every config.Action
// becomes a ghsource.Action carrying itself in Ext, and the merge hook is this
// package's full config.Action merge, so a resolved trigger's Action is
// exactly what MergeRule would have produced.
func (c Config) kit() ghsource.Config {
	out := ghsource.Config{
		App:            ghsource.AppConfig{AppID: c.App.AppID, PrivateKeyPath: c.App.PrivateKeyPath},
		Token:          c.Token,
		Webhook:        c.Webhook,
		Sweep:          c.Sweep.kit(),
		Defaults:       c.Defaults.kit(),
		ProjectMap:     c.ProjectMap,
		ProjectRewrite: c.ProjectRewrite,
		Identity:       c.Identity,
		MergeExt:       mergeExt,
	}
	for _, r := range c.Rules {
		out.Rules = append(out.Rules, r.kit())
	}
	return out
}

func (r Rule) kit() ghsource.Rule {
	out := ghsource.Rule{
		Match:    r.Match,
		Me:       actors(r.Me),
		Reviewer: actors(r.Reviewer),
		Assignee: actors(r.Assignee),
	}
	if r.Actions != nil {
		out.Actions = make(map[string]ghsource.ActionSet, len(r.Actions))
		for kind, set := range r.Actions {
			ks := make(ghsource.ActionSet, len(set))
			for i, a := range set {
				ks[i] = KitAction(a)
			}
			out.Actions[kind] = ks
		}
	}
	return out
}

// KitAction is a config.Action as the github source evaluates it: the fields
// it reads, plus the action itself in Ext.
func KitAction(a config.Action) ghsource.Action {
	return ghsource.Action{
		Name:               a.Name,
		Enabled:            a.Enabled,
		Repos:              a.Repos,
		ExcludeRepos:       a.ExcludeRepos,
		Filter:             a.Filter.Kit(),
		Reviewer:           actors(a.Reviewer),
		Assignee:           actors(a.Assignee),
		IgnoreChecks:       a.IgnoreChecks,
		StuckAfter:         a.StuckAfter.D(),
		PollInterval:       a.PollInterval.D(),
		IncludePrereleases: a.IncludePrereleases,
		Exclude:            ghsource.Exclude{Branches: a.Exclude.Branches, Labels: a.Exclude.Labels, Title: a.Exclude.Title},
		FromUsers:          a.FromUsers,
		IgnoreUsers:        a.IgnoreUsers,
		AuthorBot:          a.AuthorBot,
		LabelsAny:          a.LabelsAny,
		LabelsAll:          a.LabelsAll,
		Authors:            a.Authors,
		SoleAssignee:       a.SoleAssignee,
		RequireLabel:       a.RequireLabel,
		Gates:              a.Gates,
		Ext:                a,
	}
}

func actors(a config.Actors) ghsource.Actors {
	return ghsource.Actors{Logins: a.Logins, Teams: a.Teams}
}

// mergeExt is the kit's Ext merge: this package's full config.Action merge.
func mergeExt(base, over any) any {
	b, _ := base.(config.Action)
	o, _ := over.(config.Action)
	return mergeAction(b, o)
}

// trigger converts one kit trigger to conductor's. The matched variant's Ext
// is the merged config.Action the engine asserts on.
func trigger(t ghsource.Trigger) core.Trigger {
	ct := core.Trigger{
		Source: t.Source, Instance: t.Instance, Kind: t.Kind, Variant: t.Variant,
		Target: core.Target(t.Target), Title: t.Title, Context: t.Context,
		Dedup: t.Dedup, Labels: t.Labels,
		TargetTrusted: t.TargetTrusted, CatchUp: t.CatchUp, Force: t.Force,
		// The event's declared semantics: the same declaration the plugin
		// sends (ghplugin), so both implementations drive the engine alike.
		Sem: ghplugin.EventSemantics(t.Kind),
	}
	if a, ok := t.Action.(ghsource.Action); ok {
		if ext, ok := a.Ext.(config.Action); ok {
			ct.Action = ext
		} else {
			ct.Action = config.Action{}
		}
	}
	return ct
}

func triggers(ts []ghsource.Trigger) []core.Trigger {
	if ts == nil {
		return nil
	}
	out := make([]core.Trigger, len(ts))
	for i, t := range ts {
		out[i] = trigger(t)
	}
	return out
}

func kitEmit(emit core.EmitFunc) ghsource.EmitFunc {
	return func(ctx context.Context, t ghsource.Trigger) { emit(ctx, trigger(t)) }
}

// Name returns the instance name.
func (g *Integration) Name() string { return g.name }

// Validate checks the instance configuration.
func (g *Integration) Validate() error { return g.src.Validate() }

// Start runs the event source until ctx is cancelled.
func (g *Integration) Start(ctx context.Context, emit core.EmitFunc) error {
	return g.src.Start(ctx, kitEmit(emit))
}

// Translate maps a raw webhook event to Triggers. Exported for the `replay`
// dev command; conflict/behind kinds need live REST and are skipped when
// clients aren't initialized.
func (g *Integration) Translate(ctx context.Context, eventType string, body []byte) []core.Trigger {
	return triggers(g.src.Translate(ctx, eventType, body))
}

// SweepOnce runs a single catch-up sweep (for the `sweep` command).
func (g *Integration) SweepOnce(ctx context.Context, emit core.EmitFunc) error {
	return g.src.SweepOnce(ctx, kitEmit(emit))
}

// SweepNow triggers an immediate catch-up sweep (and resets the adaptive
// cadence) when the sweep is enabled. Non-blocking and coalescing.
func (g *Integration) SweepNow() bool { return g.src.SweepNow() }

// AppToken mints a fresh App installation token for the given installation id.
// Used to re-mint the (short-lived) token when a persisted workflow resumes.
func (g *Integration) AppToken(ctx context.Context, instID int64) (string, error) {
	return g.src.AppToken(ctx, instID)
}

// Force builds and emits trigger(s) for kind on repo#number on demand (the
// `force` command); see ghsource.Source.Force.
func (g *Integration) Force(ctx context.Context, kind, repo string, number int, emit core.EmitFunc) (int, error) {
	return g.src.Force(ctx, kind, repo, number, kitEmit(emit))
}

// NoteOwnStatusContext records a commit-status context conductor posts
// under, so a delivery of that status never reads as CI.
func (g *Integration) NoteOwnStatusContext(c string) { g.src.NoteOwnStatusContext(c) }

// OwnStatus reports whether a commit-status context is one conductor posts.
func (g *Integration) OwnStatus(c string) bool { return g.src.OwnStatus(c) }

// RetryPolicy exposes this integration's dispatch retry tuning (the dispatchTuner
// seam in main uses it to build the shared Dispatcher).
func (g *Integration) RetryPolicy() config.Retry { return g.cfg.Retry }

// SweepSettings exposes the effective catch-up sweep config (for the
// connector-lowering tests and introspection).
func (g *Integration) SweepSettings() SweepConfig { return g.cfg.Sweep }

// IdentityTokens exposes this integration's credential policy (raw values, already
// ${ENV}-expanded by the loader). main resolves the "app"/"gh_auth" keywords
// against its App-token and `gh auth token` sources; any other value is a literal.
func (g *Integration) IdentityTokens() (read, write, commitAuthor string) {
	return g.src.IdentityTokens()
}

// Actions enumerates every configured action (defaults + every rule, every kind,
// every variant) with its location, for the CLI's cross-config checks.
func (g *Integration) Actions() []config.ActionRef {
	var refs []config.ActionRef
	add := func(where string, actions map[string]config.ActionSet) {
		for kind, set := range actions {
			refs = append(refs, set.Refs(fmt.Sprintf("github[%s] %s.%s", g.name, where, kind))...)
		}
	}
	add("defaults.actions", g.cfg.Defaults.Actions)
	for i, r := range g.cfg.Rules {
		add(fmt.Sprintf("rules[%d].actions", i), r.Actions)
	}
	return refs
}

// resolve returns the effective rule (reviewer/assignee/actions merged over
// defaults) for a repo, or ok=false if no rule matches — the rule the source
// picks (ghsource.BestRule), merged with this package's MergeRule.
func (g *Integration) resolve(repo string) (Rule, bool) {
	kitRules := make([]ghsource.Rule, len(g.cfg.Rules))
	for i, r := range g.cfg.Rules {
		kitRules[i] = ghsource.Rule{Match: r.Match}
	}
	i := ghsource.BestRule(kitRules, repo)
	if i < 0 {
		return Rule{}, false
	}
	return MergeRule(g.cfg.Defaults, g.cfg.Rules[i]), true
}

// PatternSpecificity exposes the repo-glob specificity scoring for the config
// migration (it must replicate resolve()'s most-specific-wins outcome as
// per-trigger exclusions).
func PatternSpecificity(p string) int { return ghsource.PatternSpecificity(p) }

// KnownKinds exposes the set of github event kinds for the migration.
func KnownKinds() map[string]bool { return ghsource.KnownKinds() }

// MergeRule overlays a rule onto a defaults rule — the resolve() semantics,
// exported so the config migration can flatten the rules/defaults model into
// per-trigger filters with identical behavior.
func MergeRule(defaults, r Rule) Rule {
	out := Rule{
		Reviewer:  defaults.Reviewer,
		Assignee:  defaults.Assignee,
		Workspace: defaults.Workspace,
		Actions:   map[string]config.ActionSet{},
	}
	for k, v := range defaults.Actions {
		out.Actions[k] = v
	}
	if len(r.Reviewer.Logins) > 0 || len(r.Reviewer.Teams) > 0 {
		out.Reviewer = r.Reviewer
	}
	if len(r.Assignee.Logins) > 0 {
		out.Assignee = r.Assignee
	}
	if r.Workspace != "" {
		out.Workspace = r.Workspace
	}
	// A rule's set for a kind replaces the defaults' set; each variant is merged
	// over the default base (the first default variant) for that kind.
	for k, set := range r.Actions {
		var base config.Action
		if d := defaults.Actions[k]; len(d) > 0 {
			base = d[0]
		}
		merged := make(config.ActionSet, len(set))
		for i, v := range set {
			merged[i] = mergeAction(base, v)
		}
		out.Actions[k] = merged
	}
	return out
}

// mergeAction overlays override fields onto a base action (only non-zero
// override fields win). This lets a rule tweak just an agent/prompt.
func mergeAction(base, over config.Action) config.Action {
	if over.Name != "" {
		base.Name = over.Name
	}
	if over.Type != "" {
		base.Type = over.Type
	}
	if over.Agent != "" {
		base.Agent = over.Agent
	}
	if over.Prompt != "" {
		base.Prompt = over.Prompt
	}
	if over.Backend != "" {
		base.Backend = over.Backend
	}
	if over.Checkout != "" {
		base.Checkout = over.Checkout
	}
	if over.WorkDir != "" {
		base.WorkDir = over.WorkDir
	}
	if len(over.Command) > 0 {
		base.Command = over.Command
	}
	if len(over.Steps) > 0 {
		base.Steps = over.Steps
	}
	if over.ID != "" {
		base.ID = over.ID
	}
	if over.If != "" {
		base.If = over.If
	}
	if len(over.OutputSchema) > 0 {
		base.OutputSchema = over.OutputSchema
	}
	if len(over.Env) > 0 {
		base.Env = over.Env
	}
	if over.Enabled != nil {
		base.Enabled = over.Enabled
	}
	if over.Shadow != nil {
		base.Shadow = over.Shadow
	}
	// Kind-specific options.
	if over.MaxAttemptsPerHead != 0 {
		base.MaxAttemptsPerHead = over.MaxAttemptsPerHead
	}
	if len(over.IgnoreChecks) > 0 {
		base.IgnoreChecks = over.IgnoreChecks
	}
	if over.FlakyRerun.Enabled || over.FlakyRerun.Max != 0 {
		base.FlakyRerun = over.FlakyRerun
	}
	if over.StuckAfter.D() > 0 {
		base.StuckAfter = over.StuckAfter
	}
	if over.PollInterval.D() > 0 {
		base.PollInterval = over.PollInterval
	}
	if len(over.FromUsers) > 0 {
		base.FromUsers = over.FromUsers
	}
	if len(over.IgnoreUsers) > 0 {
		base.IgnoreUsers = over.IgnoreUsers
	}
	if over.AuthorBot != nil {
		base.AuthorBot = over.AuthorBot
	}
	if len(over.LabelsAny) > 0 {
		base.LabelsAny = over.LabelsAny
	}
	if len(over.LabelsAll) > 0 {
		base.LabelsAll = over.LabelsAll
	}
	if len(over.Authors) > 0 {
		base.Authors = over.Authors
	}
	if over.SoleAssignee {
		base.SoleAssignee = over.SoleAssignee
	}
	if len(over.Reviewer.Logins) > 0 || len(over.Reviewer.Teams) > 0 {
		base.Reviewer = over.Reviewer
	}
	if len(over.Assignee.Logins) > 0 {
		base.Assignee = over.Assignee
	}
	if over.RequireLabel != "" {
		base.RequireLabel = over.RequireLabel
	}
	if over.IncludePrereleases {
		base.IncludePrereleases = over.IncludePrereleases
	}
	if over.Method != "" {
		base.Method = over.Method
	}
	if len(over.Gates) > 0 {
		base.Gates = over.Gates
	}
	if len(over.Project) > 0 {
		base.Project = over.Project
	}
	// Connectors-model internals: the lowered variant's flow reference and
	// per-variant repo gates must survive the defaults merge.
	if over.FlowRef != "" {
		base.FlowRef = over.FlowRef
	}
	if len(over.Repos) > 0 {
		base.Repos = over.Repos
	}
	if len(over.ExcludeRepos) > 0 {
		base.ExcludeRepos = over.ExcludeRepos
	}
	if !over.Exclude.Empty() {
		base.Exclude = over.Exclude
	}
	if over.Filter != nil {
		base.Filter = over.Filter
	}
	if over.Background {
		base.Background = over.Background
	}
	if over.Handoff != "" {
		base.Handoff = over.Handoff
	}
	if over.Retry != nil {
		base.Retry = over.Retry
	}
	return base
}

func matchRepo(patterns []string, repo string) bool {
	for _, p := range patterns {
		if p == repo {
			return true
		}
		if ok, _ := path.Match(p, repo); ok {
			return true
		}
	}
	return false
}
