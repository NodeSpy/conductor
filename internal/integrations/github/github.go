// Package github is paseo-conductor's first integration. It receives GitHub App
// webhooks over a smee.io SSE channel, verifies them, translates payloads into
// core.Triggers, and resolves the effective action from the instance's rules.
//
// Reads/enrichment use the App installation token (its own rate pool); identity
// actions run as you (see internal/dispatch).
package github

import (
	"context"
	"fmt"
	"os"
	"path"
	"strings"

	"github.com/NodeSpy/paseo-conductor/internal/config"
	"github.com/NodeSpy/paseo-conductor/internal/core"
)

func init() { core.Register("github", newIntegration) }

// Config is a github integration instance's configuration.
type Config struct {
	App      AppConfig     `yaml:"app"`
	Webhook  WebhookConfig `yaml:"webhook"`
	Sweep    SweepConfig   `yaml:"sweep"`
	Defaults Rule          `yaml:"defaults"`
	Rules    []Rule        `yaml:"rules"`

	// ProjectMap remaps a repo (owner/name) to the paseo project name of an
	// existing workspace, so checkouts reuse it instead of cloning a fresh one.
	// Useful when the forge repo and the registered paseo project differ in org
	// or casing (e.g. EdnitionCode/RosterStream -> ednition/rosterstream). Keys
	// are matched case-insensitively; only affects checkout resolution.
	ProjectMap map[string]string `yaml:"project_map"`

	// ProjectRewrite is a blanket fallback applied to every repo in this instance
	// that has no explicit ProjectMap entry — the shortcut for a whole org whose
	// paseo projects share a naming convention. See ProjectRewrite.
	ProjectRewrite ProjectRewrite `yaml:"project_rewrite"`

	// Identity is this integration's credential policy: which token reads vs
	// writes, and commit authorship. Wired via the dispatchTuner seam in main.
	Identity Identity `yaml:"identity"`

	// Retry tunes re-attempts of a transient `paseo run` failure (git-lock/timeout
	// under a sweep fan-out). Dispatch-level, but declared here since it's this
	// integration's agents that contend.
	Retry config.Retry `yaml:"retry"`
}

// Identity controls which credential reads vs writes, and commit authorship.
// Values are inline: a known keyword, or (after ${ENV} expansion) a literal token.
//   - ReadToken:  "app" (default) — the App installation token; "gh_auth" — `gh
//     auth token`; anything else — a literal token used verbatim for reads.
//   - WriteToken: "gh_auth" (default) — `gh auth token`; anything else — a literal
//     token (e.g. a PAT via ${GH_PAT}) used verbatim for posts. Writes are always
//     you, never the bot, so "app" is not a write option.
//   - CommitAuthor: "self" (default) — commits/pushes carry your git identity.
type Identity struct {
	ReadToken    string `yaml:"read_token"`
	WriteToken   string `yaml:"write_token"`
	CommitAuthor string `yaml:"commit_author"`
}

// ProjectRewrite derives a paseo project name from a repo (owner/name) without
// listing each repo. Org, when set, replaces the owner segment (e.g. a webhook's
// EdnitionCode -> the registered ednition). The result is always matched
// case-insensitively and normalized to lowercase, since paseo project names are
// lowercased — so casing differences between the forge repo and the registered
// project never force a fresh clone. It applies to every repo in the integration;
// ProjectMap entries take precedence. Only affects checkout.
type ProjectRewrite struct {
	Org string `yaml:"org"` // override the owner/org segment
}

// active reports whether the rewrite changes anything.
func (r ProjectRewrite) active() bool { return r.Org != "" }

// AppConfig holds the GitHub App credentials.
type AppConfig struct {
	AppID          int64  `yaml:"app_id"`
	PrivateKeyPath string `yaml:"private_key_path"`
	WebhookSecret  string `yaml:"webhook_secret"`
	VerifySig      *bool  `yaml:"verify_signature"`
}

// Verify reports whether HMAC signature verification is on (default true).
func (a AppConfig) Verify() bool { return a.VerifySig == nil || *a.VerifySig }

// WebhookConfig configures how webhooks arrive: via a smee.io channel, a direct
// HTTP listener, or both.
type WebhookConfig struct {
	SmeeURL string `yaml:"smee_url"` // subscribe to a smee.io SSE channel
	Listen  string `yaml:"listen"`   // bind a direct HTTP receiver, e.g. "127.0.0.1:8787"
	Path    string `yaml:"path"`     // HTTP path (default "/webhook")
}

// SweepConfig configures the optional catch-up sweep.
type SweepConfig struct {
	Enabled  bool            `yaml:"enabled"`
	Interval config.Duration `yaml:"interval"`
	Repos    []string        `yaml:"repos"`
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

// Match selects which events a rule applies to.
type Match struct {
	Repos   []string `yaml:"repos"`   // globs, e.g. "owner/*"
	Project string   `yaml:"project"` // Projects v2 title/number (M3)
	Status  string   `yaml:"status"`  // Projects v2 status/field value (M3)
}

// Integration implements core.Integration for one github instance.
type Integration struct {
	name string
	cfg  Config
	app  *appAuth
	rest *restClient
	// self = your GitHub login(s), used to ignore your own comments, detect your
	// own PRs (self_review), and filter authored PRs during sweep. Built from
	// `me:` if set anywhere, else falls back to reviewer/assignee logins.
	self map[string]bool
	// projectMap is cfg.ProjectMap keyed lowercase for case-insensitive lookup.
	projectMap map[string]string
}

func newIntegration(name string, decode func(any) error) (core.Integration, error) {
	var cfg Config
	if err := decode(&cfg); err != nil {
		return nil, fmt.Errorf("github[%s]: decode config: %w", name, err)
	}
	if cfg.Identity.ReadToken == "" {
		cfg.Identity.ReadToken = "app"
	}
	if cfg.Identity.WriteToken == "" {
		cfg.Identity.WriteToken = "gh_auth"
	}
	if cfg.Identity.CommitAuthor == "" {
		cfg.Identity.CommitAuthor = "self"
	}
	g := &Integration{name: name, cfg: cfg, self: map[string]bool{},
		projectMap: map[string]string{}}
	for k, v := range cfg.ProjectMap {
		g.projectMap[strings.ToLower(k)] = v
	}
	rules := append([]Rule{cfg.Defaults}, cfg.Rules...)

	// Prefer an explicit `me:`; only fall back to reviewer/assignee if none set.
	for _, r := range rules {
		for _, l := range r.Me.Logins {
			g.self[strings.ToLower(l)] = true
		}
	}
	if len(g.self) == 0 {
		add := func(a config.Actors) {
			for _, l := range a.Logins {
				g.self[strings.ToLower(l)] = true
			}
		}
		for _, r := range rules {
			add(r.Reviewer) // rule-level fallback
			add(r.Assignee)
			for _, set := range r.Actions { // action-level reviewer/assignee (per variant)
				for _, a := range set {
					add(a.Reviewer)
					add(a.Assignee)
				}
			}
		}
	}
	return g, nil
}

// Name returns the instance name.
func (g *Integration) Name() string { return g.name }

// ensureClients builds the App auth + REST client once (idempotent).
func (g *Integration) ensureClients() error {
	if g.app != nil {
		return nil
	}
	app, err := newAppAuth(g.cfg.App.AppID, g.cfg.App.PrivateKeyPath)
	if err != nil {
		return err
	}
	g.app = app
	g.rest = newRESTClient(app)
	return nil
}

// Translate maps a raw webhook event to Triggers. Exported for the `replay`
// dev command; conflict/behind kinds need live REST and are skipped when
// clients aren't initialized.
func (g *Integration) Translate(ctx context.Context, eventType string, body []byte) []core.Trigger {
	return g.triggersFor(ctx, eventType, body)
}

// SweepOnce runs a single catch-up sweep (for the `sweep` command).
func (g *Integration) SweepOnce(ctx context.Context, emit core.EmitFunc) error {
	if err := g.ensureClients(); err != nil {
		return err
	}
	return g.sweep(ctx, emit)
}

// AppToken mints a fresh App installation token for the given installation id.
// Used to re-mint the (short-lived) token when a persisted workflow resumes.
func (g *Integration) AppToken(ctx context.Context, instID int64) (string, error) {
	if err := g.ensureClients(); err != nil {
		return "", err
	}
	return g.app.installationToken(ctx, instID)
}

// RetryPolicy exposes this integration's dispatch retry tuning (the dispatchTuner
// seam in main uses it to build the shared Dispatcher).
func (g *Integration) RetryPolicy() config.Retry { return g.cfg.Retry }

// IdentityTokens exposes this integration's credential policy (raw values, already
// ${ENV}-expanded by the loader). main resolves the "app"/"gh_auth" keywords
// against its App-token and `gh auth token` sources; any other value is a literal.
func (g *Integration) IdentityTokens() (read, write, commitAuthor string) {
	id := g.cfg.Identity
	return id.ReadToken, id.WriteToken, id.CommitAuthor
}

// Validate checks the instance configuration.
func (g *Integration) Validate() error {
	if g.cfg.App.AppID == 0 {
		return fmt.Errorf("github[%s]: app.app_id is required", g.name)
	}
	if g.cfg.App.PrivateKeyPath == "" {
		return fmt.Errorf("github[%s]: app.private_key_path is required", g.name)
	}
	if _, err := os.Stat(expandHome(g.cfg.App.PrivateKeyPath)); err != nil {
		return fmt.Errorf("github[%s]: private key not readable: %w", g.name, err)
	}
	if g.cfg.App.Verify() && g.cfg.App.WebhookSecret == "" {
		return fmt.Errorf("github[%s]: webhook_secret required when verify_signature is on", g.name)
	}
	if g.cfg.Webhook.SmeeURL == "" && g.cfg.Webhook.Listen == "" {
		return fmt.Errorf("github[%s]: set webhook.smee_url and/or webhook.listen", g.name)
	}
	// Action maps are keyed by kind; a typo or a renamed kind (e.g. the old
	// issue_labeled) would otherwise sit in config doing nothing. Reject unknown keys.
	check := func(where string, actions map[string]config.ActionSet) error {
		for k := range actions {
			if !knownKinds[k] {
				return fmt.Errorf("github[%s]: unknown action kind %q in %s", g.name, k, where)
			}
		}
		return nil
	}
	if err := check("defaults.actions", g.cfg.Defaults.Actions); err != nil {
		return err
	}
	for i, r := range g.cfg.Rules {
		if err := check(fmt.Sprintf("rules[%d].actions", i), r.Actions); err != nil {
			return err
		}
	}
	return nil
}

// knownKinds is the set of GitHub trigger kinds an action map may configure.
var knownKinds = map[string]bool{
	"merge_conflict": true, "pr_behind": true, "failing_checks": true,
	"changes_requested": true, "new_comment": true, "review_requested": true,
	"self_review": true, "merge_ready": true, "issue_matched": true,
	"release": true,
	// issue_assigned + issue_project_moved were merged into issue_matched (v0.4.48).
}

// resolve returns the effective rule (reviewer/assignee/actions merged over
// defaults) for a repo, or ok=false if no rule matches.
func (g *Integration) resolve(repo string) (Rule, bool) {
	// MOST-SPECIFIC match wins (not first-listed), so rule order doesn't matter:
	// an exact "EdnitionCode/RosterStream" beats "EdnitionCode/*" beats "*/*".
	// Ties (equally-specific matches) keep the earliest rule for determinism.
	bestIdx, bestScore := -1, -1
	for i, r := range g.cfg.Rules {
		score := ruleSpecificity(r.Match.Repos, repo)
		if score > bestScore {
			bestScore, bestIdx = score, i
		}
	}
	if bestIdx < 0 {
		return Rule{}, false
	}
	return g.merge(g.cfg.Rules[bestIdx]), true
}

// ruleSpecificity returns the highest match specificity of any repo pattern in
// the rule that matches repo, or -1 if none match.
func ruleSpecificity(patterns []string, repo string) int {
	best := -1
	for _, p := range patterns {
		if p != repo {
			if ok, _ := path.Match(p, repo); !ok {
				continue
			}
		}
		if s := patternSpecificity(p); s > best {
			best = s
		}
	}
	return best
}

// patternSpecificity scores a repo glob: an exact (wildcard-free) pattern beats
// any wildcard pattern; among wildcard patterns, more literal (non-glob) chars
// wins, so "EdnitionCode/*" (13 literal) outranks "*/*" (1). Kept simple: `*`,
// `?`, and `[` are treated as glob metacharacters.
func patternSpecificity(p string) int {
	glob := strings.Count(p, "*") + strings.Count(p, "?") + strings.Count(p, "[")
	literal := len(p) - glob
	if glob == 0 {
		return 100000 + literal // exact match dominates any wildcard
	}
	return literal
}

// merge overlays a rule onto the instance defaults.
func (g *Integration) merge(r Rule) Rule {
	out := Rule{
		Reviewer:  g.cfg.Defaults.Reviewer,
		Assignee:  g.cfg.Defaults.Assignee,
		Workspace: g.cfg.Defaults.Workspace,
		Actions:   map[string]config.ActionSet{},
	}
	for k, v := range g.cfg.Defaults.Actions {
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
		if d := g.cfg.Defaults.Actions[k]; len(d) > 0 {
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
	if len(over.FromUsers) > 0 {
		base.FromUsers = over.FromUsers
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
	if !over.Exclude.Empty() {
		base.Exclude = over.Exclude
	}
	if over.RerequestReview {
		base.RerequestReview = over.RerequestReview
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

func expandHome(p string) string {
	if strings.HasPrefix(p, "~/") {
		if h, err := os.UserHomeDir(); err == nil {
			return h + p[1:]
		}
	}
	return p
}
