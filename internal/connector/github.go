package connector

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"sync"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/core"
	gh "github.com/NodeSpy/conductor/internal/integrations/github"
	"github.com/NodeSpy/conductor/pkg/githubkit"
	"github.com/NodeSpy/conductor/pkg/githubkit/ghplugin"
	"github.com/NodeSpy/conductor/pkg/githubkit/ghsource"
	sdk "github.com/NodeSpy/conductor/pkg/plugin"
)

// githubDecl is the bundled github connector's declaration, built from THE
// github declaration (pkg/githubkit/ghplugin.Decl) the conductor-github
// plugin also describes itself with — connection fields, events with their
// unified-filter facts and match keys, verbs. Declared once, so the two
// implementations cannot offer different surfaces under one name.
var githubDecl = func() *TypeDecl {
	d := ghplugin.Decl()
	return mapDecl(&d)
}()

func init() { RegisterType(githubDecl, newGithubImpl) }

// githubConn is a github connector's connection config (the type-specific
// fields of its `connectors:` entry).
type githubConn struct {
	App            gh.AppConfig      `yaml:"app"`
	Token          string            `yaml:"token"`
	Webhook        githubWebhook     `yaml:"webhook"`
	Sweep          gh.SweepConfig    `yaml:"sweep"`
	Me             config.Actors     `yaml:"me"`
	Repos          []string          `yaml:"repos"`
	Identity       gh.Identity       `yaml:"identity"`
	Retry          config.Retry      `yaml:"retry"`
	ProjectMap     map[string]string `yaml:"project_map"`
	ProjectRewrite gh.ProjectRewrite `yaml:"project_rewrite"`
	APIBase        string            `yaml:"api_base"`
}

// githubWebhook mirrors gh.WebhookConfig: transport (smee_url/listen/path) and
// delivery authentication (secret/verify_signature). Both halves are webhook
// concerns — an App-less connector verifies deliveries without ever naming an
// `app:` block.
type githubWebhook struct {
	SmeeURL   string `yaml:"smee_url"`
	Listen    string `yaml:"listen"`
	Path      string `yaml:"path"`
	Secret    string `yaml:"secret"`
	VerifySig *bool  `yaml:"verify_signature"`
}

type githubImpl struct {
	name string
	conn githubConn
	deps Deps

	// kit is the daemon-agnostic GitHub client (pkg/githubkit) that every verb
	// call delegates to — credentials, HTTP mechanics, caching, rate-limit
	// handling, and the verb switch all live there now.
	kit *githubkit.Client

	// src is the event source Source built, so every context set_status
	// posts under joins its own-status guard. Set once at build; read under
	// srcMu.
	srcMu sync.Mutex
	src   *gh.Integration
}

func newGithubImpl(name string, ref config.ConnectorRef, deps Deps) (Impl, error) {
	var conn githubConn
	if err := ref.Decode(&conn); err != nil {
		return nil, fmt.Errorf("connector %q: decode github connection: %w", name, err)
	}
	// The connection node is decoded non-strictly (ConnectorRef retains a raw
	// node), so the retired app-block webhook keys would otherwise be dropped
	// in silence — taking the operator's secret with them. Name the new home.
	if conn.App.LegacyWebhookKeys() {
		return nil, ConfigErr(fmt.Errorf("connector %q: %w", name, gh.ErrAppWebhookMoved))
	}

	// Resolve secret references in credential fields. An unresolvable secret
	// disables the connector (the registry handles that) rather than failing
	// the boot.
	ctx := context.Background()
	var err error
	if conn.Token, err = deps.Secrets.Resolve(ctx, conn.Token); err != nil {
		return nil, fmt.Errorf("token: %w", err)
	}
	if conn.Webhook.Secret, err = deps.Secrets.Resolve(ctx, conn.Webhook.Secret); err != nil {
		return nil, fmt.Errorf("webhook.secret: %w", err)
	}
	if conn.Token != "" {
		deps.Secrets.Track(conn.Token)
	}
	kitCfg := githubkit.Config{
		Token:      conn.Token,
		WriteToken: conn.Identity.WriteToken,
		APIBase:    conn.APIBase,
	}
	if conn.App.AppID > 0 && conn.App.PrivateKeyPath != "" {
		kitCfg.App = &githubkit.AppConfig{AppID: conn.App.AppID, PrivateKeyPath: conn.App.PrivateKeyPath}
	}
	kit, err := githubkit.NewClient(kitCfg)
	if err != nil {
		return nil, err
	}
	return &githubImpl{name: name, conn: conn, deps: deps, kit: kit}, nil
}

func (g *githubImpl) Validate() error {
	partialApp := (g.conn.App.AppID > 0) != (g.conn.App.PrivateKeyPath != "")
	if partialApp {
		return fmt.Errorf("connector %q: app: needs both app_id and private_key_path", g.name)
	}
	return nil
}

func (g *githubImpl) DeclaredEvents() []string { return nil }

// Source lowers the connector's triggers into a github source: each trigger
// through ghsource.LowerTrigger, the connection plus all of them through
// ghsource.Connection.SourceConfig — the two functions the conductor-github
// plugin calls on the far side of the wire, so a config builds the same
// source in either. Every trigger is a variant of its event kind; its
// lowered config.Action rides in Ext and is what the engine runs.
func (g *githubImpl) Source(triggers []CompiledTrigger) (core.Integration, error) {
	if len(triggers) == 0 {
		return nil, nil
	}
	kitActions := map[string]ghsource.ActionSet{}
	legacyActions := map[string]config.ActionSet{}
	for _, t := range triggers {
		act, k, err := g.lower(t)
		if err != nil {
			return nil, err
		}
		kind := t.Spec.Event()
		kitActions[kind] = append(kitActions[kind], k)
		legacyActions[kind] = append(legacyActions[kind], act)
	}
	kc := g.connection().SourceConfig(kitActions)
	// legacy is the same lowering in conductor's own types: the retry policy
	// and the sweep block as written (SweepSettings), and the rules the CLI
	// enumerates (Actions).
	legacy := gh.Config{
		App:   g.conn.App,
		Token: g.conn.Token,
		Webhook: gh.WebhookConfig{
			SmeeURL: g.conn.Webhook.SmeeURL, Listen: g.conn.Webhook.Listen, Path: g.conn.Webhook.Path,
			Secret: g.conn.Webhook.Secret, VerifySig: g.conn.Webhook.VerifySig,
		},
		Sweep:          g.conn.Sweep,
		Identity:       g.conn.Identity,
		Retry:          g.conn.Retry,
		ProjectMap:     g.conn.ProjectMap,
		ProjectRewrite: g.conn.ProjectRewrite,
		Defaults:       gh.Rule{Me: g.conn.Me},
		Rules:          []gh.Rule{{Match: gh.Match{Repos: []string{"*/*"}}, Actions: legacyActions}},
	}
	if legacy.Sweep.IsEnabled() && len(legacy.Sweep.Repos) == 0 {
		legacy.Sweep.Repos = g.conn.Repos
	}
	src, err := gh.NewConnector(g.name, kc, legacy)
	if err != nil {
		return nil, err
	}
	g.srcMu.Lock()
	g.src = src
	g.srcMu.Unlock()
	return src, nil
}

// connection is the connector's connection block as the source reads it.
func (g *githubImpl) connection() ghsource.Connection {
	return ghsource.Connection{
		App:   ghsource.AppConfig{AppID: g.conn.App.AppID, PrivateKeyPath: g.conn.App.PrivateKeyPath},
		Token: g.conn.Token,
		Webhook: gh.WebhookConfig{
			SmeeURL: g.conn.Webhook.SmeeURL, Listen: g.conn.Webhook.Listen, Path: g.conn.Webhook.Path,
			Secret: g.conn.Webhook.Secret, VerifySig: g.conn.Webhook.VerifySig,
		},
		Sweep: ghsource.SweepConfig{
			Enabled: g.conn.Sweep.Enabled, Interval: g.conn.Sweep.Interval.D(),
			MinInterval: g.conn.Sweep.MinInterval.D(), Repos: g.conn.Sweep.Repos,
		},
		Me:             ghsource.Actors{Logins: g.conn.Me.Logins, Teams: g.conn.Me.Teams},
		Repos:          g.conn.Repos,
		Identity:       g.conn.Identity,
		ProjectMap:     g.conn.ProjectMap,
		ProjectRewrite: g.conn.ProjectRewrite,
		APIBase:        g.conn.APIBase,
	}
}

// lower lowers one trigger twice over the same reading: the source's
// Action (ghsource.LowerTrigger — routing, identity gates, the options the
// source evaluates, the filter-or-default decision) and the config.Action the
// engine runs, which carries the same values plus what only the engine reads
// (the flow reference, shadow, the attempt threshold, the flaky rerun). The
// config.Action rides in the source Action's Ext.
func (g *githubImpl) lower(t CompiledTrigger) (config.Action, ghsource.Action, error) {
	k, err := ghsource.LowerTrigger(ghsource.TriggerSpec{
		Name: t.Spec.Name, Event: t.Spec.Event(), Enabled: t.Spec.Enabled,
		Options: t.Spec.Options, Filter: t.Spec.Filter.Kit(),
	}, g.conn.Repos)
	if err != nil {
		return config.Action{}, k, fmt.Errorf("trigger on %s: %w", t.Spec.On, err)
	}
	act := config.Action{
		Name:               t.Spec.Name,
		Enabled:            t.Spec.Enabled,
		Shadow:             t.Spec.Shadow,
		FlowRef:            t.Ref(),
		Repos:              k.Repos,
		ExcludeRepos:       k.ExcludeRepos,
		Reviewer:           config.Actors{Logins: k.Reviewer.Logins, Teams: k.Reviewer.Teams},
		Assignee:           config.Actors{Logins: k.Assignee.Logins, Teams: k.Assignee.Teams},
		IgnoreChecks:       k.IgnoreChecks,
		IncludePrereleases: k.IncludePrereleases,
		StuckAfter:         config.Duration(k.StuckAfter),
		PollInterval:       config.Duration(k.PollInterval),
	}
	if k.Filter != nil {
		// The operator's own node, so the action keeps its surface spelling.
		act.Filter = t.Spec.Filter
	}
	lowerEngineOptions(&act, t.Spec.Options, githubEventSemantics(t.Spec.Event()))
	k.Ext = act
	return act, k, nil
}

// lowerTrigger is lower's engine half: the config.Action a trigger runs as.
func (g *githubImpl) lowerTrigger(t CompiledTrigger) (config.Action, error) {
	act, _, err := g.lower(t)
	return act, err
}

// Invoke runs a github verb. sweep is daemon-global (no repo/token involved)
// and is intercepted here; every other verb delegates to the daemon-agnostic
// githubkit.Client, which resolves the `as: me|bot` identity, issues the
// authenticated API call, and returns its outputs.
func (g *githubImpl) Invoke(ctx context.Context, verb string, opts map[string]any) (map[string]any, error) {
	if verb == githubDecl.PollVerb() {
		return pollNow(ctx, g.name)
	}
	out, err := g.kit.Invoke(ctx, verb, opts)
	if err == nil && verb == "set_status" {
		// Whatever context a status went out under is conductor's own from
		// now on: the source never reads it back as a CI signal, or a
		// `failure` a hook posted could dispatch the next fixer.
		if c, _ := out["context"].(string); c != "" {
			g.srcMu.Lock()
			src := g.src
			g.srcMu.Unlock()
			if src != nil {
				src.NoteOwnStatusContext(c)
			}
		}
	}
	return out, err
}

// post/patch/put/del are the write verbs' authenticated JSON requests.
// isRateLimited/retryAfter are thin wrappers over githubkit's exported
// helpers, kept as package-level functions in `connector` for existing test
// call sites (github_test.go calls them unqualified).
func isRateLimited(resp *http.Response) bool       { return githubkit.IsRateLimited(resp) }
func retryAfter(resp *http.Response) time.Duration { return githubkit.RetryAfter(resp) }

// reviewComments is a thin wrapper over githubkit.ReviewComments, kept as a
// package-level function for github_test.go's direct unit test.
func reviewComments(v any) ([]map[string]any, error) { return githubkit.ReviewComments(v) }

// --- shared option/filter coercion helpers ---

// toStrings coerces a YAML list (or single string) into []string.
func toStrings(v any) []string {
	switch x := v.(type) {
	case []string:
		return x
	case []any:
		out := make([]string, 0, len(x))
		for _, e := range x {
			if s, ok := e.(string); ok {
				out = append(out, s)
			} else {
				out = append(out, fmt.Sprintf("%v", e))
			}
		}
		return out
	case string:
		if x == "" {
			return nil
		}
		return []string{x}
	}
	return nil
}

// toActors coerces { logins: [...], teams: [...] } (or a bare list = logins).
func toActors(v any) config.Actors {
	switch x := v.(type) {
	case map[string]any:
		return config.Actors{Logins: toStrings(x["logins"]), Teams: toStrings(x["teams"])}
	case []any, []string:
		return config.Actors{Logins: toStrings(x)}
	}
	return config.Actors{}
}

// toInt coerces YAML integer shapes.
func toInt(v any) int {
	switch x := v.(type) {
	case int:
		return x
	case int64:
		return int(x)
	case uint64:
		return int(x)
	case float64:
		return int(x)
	}
	return 0
}

// toDuration coerces a duration string or integer seconds ("" -> 0).
func toDuration(v any) (time.Duration, error) {
	switch x := v.(type) {
	case nil:
		return 0, nil
	case string:
		if x == "" {
			return 0, nil
		}
		return time.ParseDuration(x)
	case int:
		return time.Duration(x) * time.Second, nil
	case int64:
		return time.Duration(x) * time.Second, nil
	case float64:
		return time.Duration(x) * time.Second, nil
	}
	return 0, fmt.Errorf("want a duration, got %T", v)
}

// truthy mirrors YAML-ish truthiness for option maps.
func truthy(v any) bool {
	switch x := v.(type) {
	case bool:
		return x
	case string:
		return x != "" && x != "false" && x != "no" && x != "0"
	case int:
		return x != 0
	case float64:
		return x != 0
	}
	return false
}

// buildIntegration constructs a legacy integration instance from an in-memory
// config struct by round-tripping it through YAML into core.Build — the same
// decode path a hand-written legacy config takes, so lowered connectors run
// the exact code legacy configs run.
func buildIntegration(typ, name string, cfg any) (core.Integration, error) {
	b, err := yaml.Marshal(cfg)
	if err != nil {
		return nil, fmt.Errorf("connector %q: lower to %s config: %w", name, typ, err)
	}
	var node yaml.Node
	if err := yaml.Unmarshal(b, &node); err != nil {
		return nil, fmt.Errorf("connector %q: reparse %s config: %w", name, typ, err)
	}
	decode := func(v any) error { return node.Decode(v) }
	return core.Build(typ, name, decode)
}

// sortedFilterKeys is a debug/introspection helper listing a schema's keys.
func sortedFilterKeys(s Schema) []string {
	out := make([]string, 0, len(s))
	for k := range s {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// githubEventSemantics is the declared semantics of one github event.
func githubEventSemantics(event string) *sdk.EventSemantics {
	if ev, ok := githubDecl.Event(event); ok {
		return ev.Semantics
	}
	return nil
}
