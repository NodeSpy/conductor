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
}

// AppConfig holds the GitHub App credentials.
type AppConfig struct {
	AppID          int64  `yaml:"app_id"`
	PrivateKeyPath string `yaml:"private_key_path"`
	WebhookSecret  string `yaml:"webhook_secret"`
	VerifySig      *bool  `yaml:"verify_signature"`
}

// Verify reports whether HMAC signature verification is on (default true).
func (a AppConfig) Verify() bool { return a.VerifySig == nil || *a.VerifySig }

// WebhookConfig configures the smee transport.
type WebhookConfig struct {
	SmeeURL string `yaml:"smee_url"`
}

// SweepConfig configures the optional catch-up sweep.
type SweepConfig struct {
	Enabled  bool            `yaml:"enabled"`
	Interval config.Duration `yaml:"interval"`
	Repos    []string        `yaml:"repos"`
}

// Rule is one entry in the instance's `rules` list (or the `defaults` block).
type Rule struct {
	Match     Match                    `yaml:"match"`
	Reviewer  config.Actors            `yaml:"reviewer"`
	Assignee  config.Actors            `yaml:"assignee"`
	Workspace string                   `yaml:"workspace"`
	Actions   map[string]config.Action `yaml:"actions"`
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
	self map[string]bool // logins treated as "you" (for self-comment filtering)
}

func newIntegration(name string, decode func(any) error) (core.Integration, error) {
	var cfg Config
	if err := decode(&cfg); err != nil {
		return nil, fmt.Errorf("github[%s]: decode config: %w", name, err)
	}
	g := &Integration{name: name, cfg: cfg, self: map[string]bool{}}
	for _, r := range append([]Rule{cfg.Defaults}, cfg.Rules...) {
		for _, l := range r.Reviewer.Logins {
			g.self[strings.ToLower(l)] = true
		}
		for _, l := range r.Assignee.Logins {
			g.self[strings.ToLower(l)] = true
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
	if g.cfg.Webhook.SmeeURL == "" {
		return fmt.Errorf("github[%s]: webhook.smee_url is required", g.name)
	}
	return nil
}

// resolve returns the effective rule (reviewer/assignee/actions merged over
// defaults) for a repo, or ok=false if no rule matches.
func (g *Integration) resolve(repo string) (Rule, bool) {
	for _, r := range g.cfg.Rules {
		if matchRepo(r.Match.Repos, repo) {
			return g.merge(r), true
		}
	}
	return Rule{}, false
}

// merge overlays a rule onto the instance defaults.
func (g *Integration) merge(r Rule) Rule {
	out := Rule{
		Reviewer:  g.cfg.Defaults.Reviewer,
		Assignee:  g.cfg.Defaults.Assignee,
		Workspace: g.cfg.Defaults.Workspace,
		Actions:   map[string]config.Action{},
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
	for k, v := range r.Actions {
		out.Actions[k] = mergeAction(out.Actions[k], v)
	}
	return out
}

// mergeAction overlays override fields onto a base action (only non-zero
// override fields win). This lets a rule tweak just an agent/prompt.
func mergeAction(base, over config.Action) config.Action {
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
	if over.RequireLabel != "" {
		base.RequireLabel = over.RequireLabel
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
