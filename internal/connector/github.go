package connector

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os/exec"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/NodeSpy/paseo-conductor/internal/config"
	"github.com/NodeSpy/paseo-conductor/internal/core"
	gh "github.com/NodeSpy/paseo-conductor/internal/integrations/github"
)

// baseGithubFilters are the filter keys every github event accepts.
func baseGithubFilters() Schema {
	return Schema{
		"repos":         {Type: TList, Desc: "repo globs this trigger fires for (default: the connector's repos:)"},
		"exclude_repos": {Type: TList, Desc: "repo globs this trigger never fires for"},
	}
}

// baseGithubContext are the context facts every github event publishes.
func baseGithubContext() Schema {
	return Schema{
		"repo": {Type: TString}, "owner": {Type: TString}, "name": {Type: TString},
		"pr": {Type: TInt}, "issue": {Type: TInt}, "number": {Type: TInt},
		"head": {Type: TString}, "base": {Type: TString}, "url": {Type: TString},
		"kind": {Type: TString}, "title": {Type: TString}, "labels": {Type: TList},
	}
}

// githubEvent builds one event declaration on the shared base.
func githubEvent(name, desc string, filters, contextExtra, options Schema) EventDecl {
	f := baseGithubFilters()
	for k, v := range filters {
		f[k] = v
	}
	c := baseGithubContext()
	for k, v := range contextExtra {
		c[k] = v
	}
	o := Schema{
		"max_attempts_per_head": {Type: TInt, Desc: "soft attempt threshold before backoff"},
	}
	for k, v := range options {
		o[k] = v
	}
	return EventDecl{Name: name, Desc: desc, Filters: f, Context: c, Options: o}
}

var githubDecl = &TypeDecl{
	Type: "github",
	Desc: "GitHub: PR/issue/check/release events in; comments, reviews, and review requests out.",
	Connection: Schema{
		"app":             {Type: TMap, Desc: "GitHub App credentials: app_id, private_key_path, webhook_secret, verify_signature"},
		"token":           {Type: TString, Desc: "PAT used when no App is configured (chain: app → token → gh auth token)"},
		"webhook":         {Type: TMap, Desc: "event transport: smee_url and/or listen (+ path)"},
		"sweep":           {Type: TMap, Desc: "catch-up sweep: enabled, interval, min_interval, repos"},
		"me":              {Type: TMap, Desc: "your GitHub login(s): { logins: [...] } — defines \"you\""},
		"repos":           {Type: TList, Desc: "default repo globs for triggers with no repos filter"},
		"identity":        {Type: TMap, Desc: "credential policy: read_token, write_token, commit_author"},
		"retry":           {Type: TMap, Desc: "transient dispatch retry: max, backoff"},
		"project_map":     {Type: TMap, Desc: "repo -> paseo project checkout remap"},
		"project_rewrite": {Type: TMap, Desc: "blanket owner/org rewrite for checkouts"},
	},
	Events: []EventDecl{
		githubEvent("review_requested", "your review was requested on a PR",
			Schema{
				"reviewer": {Type: TMap, Desc: "whose requested review triggers: { logins: [...], teams: [...] }"},
				"gates":    {Type: TMap, Desc: "opt-out toggles, e.g. { not_draft: false }"},
				"exclude":  {Type: TMap, Desc: "skip PRs: { branches: [...], labels: [...], title: [...] }"},
			}, nil, nil),
		githubEvent("changes_requested", "a review requested changes on your PR (or threads went unresolved)",
			nil, Schema{"head_ref": {Type: TString}}, nil),
		githubEvent("new_comment", "a new comment on your PR",
			Schema{
				"from_users":   {Type: TList, Desc: "only these commenters trigger (empty = any)"},
				"ignore_users": {Type: TList, Desc: "never trigger on these commenters"},
			},
			Schema{
				"author": {Type: TString}, "comment_body": {Type: TString}, "head_ref": {Type: TString},
				"comment_id": {Type: TInt}, "comment_kind": {Type: TString},
			}, nil),
		githubEvent("merge_conflict", "your PR became unmergeable", nil, nil, nil),
		githubEvent("pr_behind", "your PR fell behind its base", nil, nil, nil),
		githubEvent("failing_checks", "CI concluded failing on your PR",
			Schema{"ignore_checks": {Type: TList, Desc: "check names that never trigger"}},
			Schema{"failing_check": {Type: TString}, "run_id": {Type: TInt}},
			Schema{"flaky_rerun": {Type: TMap, Desc: "rerun failed jobs once before dispatching: { enabled, max }"}}),
		githubEvent("stuck_checks", "a CI run has been running too long on your PR",
			nil,
			Schema{"run_id": {Type: TInt}, "run_name": {Type: TString}, "run_status": {Type: TString}},
			Schema{
				"stuck_after":   {Type: TDuration, Desc: "how long a run may take before it is stuck (default 30m)"},
				"poll_interval": {Type: TDuration, Desc: "poller cadence (default 15m)"},
			}),
		githubEvent("merge_ready", "your PR turned all-green",
			Schema{
				"require_label": {Type: TString, Desc: "only fire when the PR carries this label"},
				"gates":         {Type: TMap, Desc: "opt-out toggles: not_draft, merge_state, review_decision, non_author_approval, threads_resolved"},
			}, nil, nil),
		githubEvent("self_review", "you opened/updated your own PR", nil, nil, nil),
		githubEvent("issue_matched", "an issue matches your criteria",
			Schema{
				"assignee":      {Type: TMap, Desc: "whose assignment triggers: { logins: [...] }"},
				"sole_assignee": {Type: TBool, Desc: "only when you are the ONLY assignee"},
				"labels_any":    {Type: TList}, "labels_all": {Type: TList},
				"authors": {Type: TList, Desc: "only issues opened by these logins"},
				"exclude": {Type: TMap, Desc: "skip issues: { labels: [...], title: [...] }"},
				"gates":   {Type: TMap, Desc: "no_branch, project: { field: value }"},
			}, nil, nil),
		githubEvent("release", "a release was published",
			Schema{"include_prereleases": {Type: TBool}},
			Schema{"tag_name": {Type: TString}, "prerelease": {Type: TBool}, "draft": {Type: TBool}}, nil),
		githubEvent("deployment_status", "a deployment failed or errored",
			nil, Schema{"state": {Type: TString}, "environment": {Type: TString}, "description": {Type: TString}}, nil),
		githubEvent("dependabot_alert", "a new Dependabot alert",
			nil, Schema{"severity": {Type: TString}, "package": {Type: TString}, "summary": {Type: TString}}, nil),
		githubEvent("secret_scanning_alert", "a new secret-scanning alert",
			nil, Schema{"secret_type": {Type: TString}}, nil),
	},
	Verbs: []VerbDecl{
		{
			Name: "comment", Desc: "post an issue/PR conversation comment",
			Options: Schema{
				"repo":   {Type: TString, Required: true},
				"number": {Type: TInt, Desc: "issue or PR number (alias: pr)"},
				"pr":     {Type: TInt},
				"body":   {Type: TString, Required: true},
				"as":     {Type: TString, Enum: []string{"me", "bot"}, Desc: "identity (default me)"},
			},
			Outputs: Schema{"id": {Type: TInt}, "url": {Type: TString}},
		},
		{
			Name: "reply", Desc: "reply to a PR review comment thread",
			Options: Schema{
				"repo":        {Type: TString, Required: true},
				"pr":          {Type: TInt, Required: true},
				"in_reply_to": {Type: TInt, Required: true, Desc: "review comment id to reply to"},
				"body":        {Type: TString, Required: true},
				"as":          {Type: TString, Enum: []string{"me", "bot"}},
			},
			Outputs: Schema{"id": {Type: TInt}, "url": {Type: TString}},
		},
		{
			Name: "rerequest_review", Desc: "re-request review from reviewers",
			Options: Schema{
				"repo":           {Type: TString, Required: true},
				"pr":             {Type: TInt, Required: true},
				"reviewers":      {Type: TList, Desc: "logins"},
				"team_reviewers": {Type: TList, Desc: "team slugs"},
				"as":             {Type: TString, Enum: []string{"me", "bot"}},
			},
			Outputs: Schema{"ok": {Type: TBool}},
		},
		{
			Name: "submit_review", Desc: "submit a PR review",
			Options: Schema{
				"repo":  {Type: TString, Required: true},
				"pr":    {Type: TInt, Required: true},
				"body":  {Type: TString},
				"event": {Type: TString, Enum: []string{"APPROVE", "REQUEST_CHANGES", "COMMENT"}, Required: true},
				"as":    {Type: TString, Enum: []string{"me", "bot"}},
			},
			Outputs: Schema{"id": {Type: TInt}},
		},
		{
			Name: "add_labels", Desc: "add labels to an issue or PR",
			Options: Schema{
				"repo":   {Type: TString, Required: true},
				"number": {Type: TInt, Required: true},
				"labels": {Type: TList, Required: true},
				"as":     {Type: TString, Enum: []string{"me", "bot"}},
			},
			Outputs: Schema{"ok": {Type: TBool}},
		},
	},
}

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
}

// githubWebhook mirrors gh.WebhookConfig plus a `secret:` alias so an
// App-less connector doesn't have to configure an `app:` block just to hold
// the webhook secret.
type githubWebhook struct {
	SmeeURL string `yaml:"smee_url"`
	Listen  string `yaml:"listen"`
	Path    string `yaml:"path"`
	Secret  string `yaml:"secret"`
}

type githubImpl struct {
	name string
	conn githubConn
	deps Deps

	appTokens *gh.AppTokens // nil when App-less
	httpc     *http.Client

	// ghToken is injectable for tests (defaults to `gh auth token`).
	ghToken func() (string, error)
}

func newGithubImpl(name string, ref config.ConnectorRef, deps Deps) (Impl, error) {
	var conn githubConn
	if err := ref.Decode(&conn); err != nil {
		return nil, fmt.Errorf("connector %q: decode github connection: %w", name, err)
	}
	// Resolve secret references in credential fields. An unresolvable secret
	// disables the connector (the registry handles that) rather than failing
	// the boot.
	ctx := context.Background()
	var err error
	if conn.Token, err = deps.Secrets.Resolve(ctx, conn.Token); err != nil {
		return nil, fmt.Errorf("token: %w", err)
	}
	if conn.App.WebhookSecret, err = deps.Secrets.Resolve(ctx, conn.App.WebhookSecret); err != nil {
		return nil, fmt.Errorf("app.webhook_secret: %w", err)
	}
	if conn.Webhook.Secret, err = deps.Secrets.Resolve(ctx, conn.Webhook.Secret); err != nil {
		return nil, fmt.Errorf("webhook.secret: %w", err)
	}
	if conn.App.WebhookSecret == "" {
		conn.App.WebhookSecret = conn.Webhook.Secret
	}
	if conn.Token != "" {
		deps.Secrets.Track(conn.Token)
	}
	g := &githubImpl{
		name: name, conn: conn, deps: deps,
		httpc:   &http.Client{Timeout: 20 * time.Second},
		ghToken: ghAuthToken,
	}
	if conn.App.AppID > 0 && conn.App.PrivateKeyPath != "" {
		at, err := gh.NewAppTokens(conn.App.AppID, conn.App.PrivateKeyPath)
		if err != nil {
			return nil, fmt.Errorf("app credentials: %w", err)
		}
		g.appTokens = at
	}
	return g, nil
}

// ghAuthToken shells out to `gh auth token` — the last link of the
// app → token → gh credential chain.
func ghAuthToken() (string, error) {
	out, err := exec.Command("gh", "auth", "token").Output()
	if err != nil {
		return "", fmt.Errorf("gh auth token: %w", err)
	}
	tok := strings.TrimSpace(string(out))
	if tok == "" {
		return "", fmt.Errorf("gh auth token returned empty")
	}
	return tok, nil
}

func (g *githubImpl) Validate() error {
	partialApp := (g.conn.App.AppID > 0) != (g.conn.App.PrivateKeyPath != "")
	if partialApp {
		return fmt.Errorf("connector %q: app: needs both app_id and private_key_path", g.name)
	}
	return nil
}

func (g *githubImpl) DeclaredEvents() []string { return nil }

// Source lowers the connector's triggers into a github integration instance.
// Every trigger becomes a variant of its event kind on the Defaults rule; the
// per-variant repos/exclude_repos gates carry each trigger's repo filter, so
// triggers stay independent (all matching triggers fire) while the existing
// integration code evaluates every other filter exactly as legacy configs do.
func (g *githubImpl) Source(triggers []CompiledTrigger) (core.Integration, error) {
	if len(triggers) == 0 {
		return nil, nil
	}
	actions := map[string]config.ActionSet{}
	for _, t := range triggers {
		act, err := g.lowerTrigger(t)
		if err != nil {
			return nil, err
		}
		kind := t.Spec.Event()
		actions[kind] = append(actions[kind], act)
	}
	sweep := g.conn.Sweep
	if sweep.Enabled && len(sweep.Repos) == 0 {
		sweep.Repos = g.conn.Repos
	}
	cfg := gh.Config{
		App:   g.conn.App,
		Token: g.conn.Token,
		Webhook: gh.WebhookConfig{
			SmeeURL: g.conn.Webhook.SmeeURL, Listen: g.conn.Webhook.Listen, Path: g.conn.Webhook.Path,
		},
		Sweep:          sweep,
		Identity:       g.conn.Identity,
		Retry:          g.conn.Retry,
		ProjectMap:     g.conn.ProjectMap,
		ProjectRewrite: g.conn.ProjectRewrite,
		Defaults: gh.Rule{
			Me:      g.conn.Me,
			Actions: actions,
		},
	}
	return buildIntegration("github", g.name, cfg)
}

// lowerTrigger maps one trigger spec's filters/options onto the legacy Action
// fields the github integration's matchers evaluate.
func (g *githubImpl) lowerTrigger(t CompiledTrigger) (config.Action, error) {
	f := t.Spec.Filters
	act := config.Action{
		Name:    t.Spec.Name,
		Enabled: t.Spec.Enabled,
		Shadow:  t.Spec.Shadow,
		FlowRef: t.Ref(),
	}
	act.Repos = toStrings(f["repos"])
	if len(act.Repos) == 0 {
		act.Repos = g.conn.Repos
	}
	act.ExcludeRepos = toStrings(f["exclude_repos"])
	act.Reviewer = toActors(f["reviewer"])
	act.Assignee = toActors(f["assignee"])
	act.SoleAssignee, _ = f["sole_assignee"].(bool)
	act.LabelsAny = toStrings(f["labels_any"])
	act.LabelsAll = toStrings(f["labels_all"])
	act.Authors = toStrings(f["authors"])
	act.FromUsers = toStrings(f["from_users"])
	act.IgnoreUsers = toStrings(f["ignore_users"])
	act.IgnoreChecks = toStrings(f["ignore_checks"])
	act.RequireLabel, _ = f["require_label"].(string)
	act.IncludePrereleases, _ = f["include_prereleases"].(bool)
	if m, ok := f["gates"].(map[string]any); ok {
		act.Gates = m
	}
	if m, ok := f["exclude"].(map[string]any); ok {
		act.Exclude = config.Exclude{
			Branches: toStrings(m["branches"]),
			Labels:   toStrings(m["labels"]),
			Title:    toStrings(m["title"]),
		}
	}
	o := t.Spec.Options
	if n := toInt(o["max_attempts_per_head"]); n > 0 {
		act.MaxAttemptsPerHead = n
	}
	if m, ok := o["flaky_rerun"].(map[string]any); ok {
		act.FlakyRerun = config.FlakyRerun{Enabled: truthy(m["enabled"]), Max: toInt(m["max"])}
	}
	if d, err := toDuration(o["stuck_after"]); err != nil {
		return act, fmt.Errorf("trigger on %s: options.stuck_after: %w", t.Spec.On, err)
	} else if d > 0 {
		act.StuckAfter = config.Duration(d)
	}
	if d, err := toDuration(o["poll_interval"]); err != nil {
		return act, fmt.Errorf("trigger on %s: options.poll_interval: %w", t.Spec.On, err)
	} else if d > 0 {
		act.PollInterval = config.Duration(d)
	}
	return act, nil
}

// tokenFor resolves the identity a verb call acts as. `me` follows the
// connector's write-token policy (gh auth token by default, a literal
// write_token otherwise, the PAT as a fallback when gh isn't available);
// `bot` requires App credentials and posts as the App's bot user.
func (g *githubImpl) tokenFor(ctx context.Context, as, repo string) (string, error) {
	switch as {
	case "", "me":
		wt := g.conn.Identity.WriteToken
		if wt != "" && wt != "gh_auth" {
			return wt, nil // literal token (already ${ENV}-expanded / secret-resolved)
		}
		tok, err := g.ghToken()
		if err == nil {
			return tok, nil
		}
		if g.conn.Token != "" {
			return g.conn.Token, nil
		}
		return "", fmt.Errorf("as: me — no write credential: %v (configure identity.write_token, token:, or log in with gh)", err)
	case "bot":
		if g.appTokens == nil {
			return "", fmt.Errorf("as: bot needs GitHub App credentials (app:) on connector %q", g.name)
		}
		owner, name, ok := strings.Cut(repo, "/")
		if !ok {
			return "", fmt.Errorf("as: bot needs a repo in owner/name form, got %q", repo)
		}
		return g.appTokens.TokenForRepo(ctx, owner, name)
	}
	return "", fmt.Errorf("as: must be me|bot, got %q", as)
}

func (g *githubImpl) Invoke(ctx context.Context, verb string, opts map[string]any) (map[string]any, error) {
	repo, _ := opts["repo"].(string)
	if repo == "" {
		return nil, fmt.Errorf("github.%s: options.repo is required", verb)
	}
	as, _ := opts["as"].(string)
	tok, err := g.tokenFor(ctx, as, repo)
	if err != nil {
		return nil, fmt.Errorf("github.%s: %w", verb, err)
	}
	number := toInt(opts["number"])
	if number == 0 {
		number = toInt(opts["pr"])
	}
	base := gh.APIBaseURL()
	switch verb {
	case "comment":
		if number == 0 {
			return nil, fmt.Errorf("github.comment: options.number (or pr) is required")
		}
		var out struct {
			ID      int64  `json:"id"`
			HTMLURL string `json:"html_url"`
		}
		err := g.post(ctx, tok, fmt.Sprintf("%s/repos/%s/issues/%d/comments", base, repo, number),
			map[string]any{"body": opts["body"]}, &out)
		if err != nil {
			return nil, err
		}
		return map[string]any{"id": out.ID, "url": out.HTMLURL}, nil
	case "reply":
		id := toInt(opts["in_reply_to"])
		if number == 0 || id == 0 {
			return nil, fmt.Errorf("github.reply: options.pr and options.in_reply_to are required")
		}
		var out struct {
			ID      int64  `json:"id"`
			HTMLURL string `json:"html_url"`
		}
		err := g.post(ctx, tok, fmt.Sprintf("%s/repos/%s/pulls/%d/comments/%d/replies", base, repo, number, id),
			map[string]any{"body": opts["body"]}, &out)
		if err != nil {
			return nil, err
		}
		return map[string]any{"id": out.ID, "url": out.HTMLURL}, nil
	case "rerequest_review":
		if number == 0 {
			return nil, fmt.Errorf("github.rerequest_review: options.pr is required")
		}
		body := map[string]any{}
		if rs := toStrings(opts["reviewers"]); len(rs) > 0 {
			body["reviewers"] = rs
		}
		if ts := toStrings(opts["team_reviewers"]); len(ts) > 0 {
			body["team_reviewers"] = ts
		}
		if len(body) == 0 {
			return nil, fmt.Errorf("github.rerequest_review: set options.reviewers and/or team_reviewers")
		}
		if err := g.post(ctx, tok, fmt.Sprintf("%s/repos/%s/pulls/%d/requested_reviewers", base, repo, number), body, nil); err != nil {
			return nil, err
		}
		return map[string]any{"ok": true}, nil
	case "submit_review":
		if number == 0 {
			return nil, fmt.Errorf("github.submit_review: options.pr is required")
		}
		event, _ := opts["event"].(string)
		if event == "" {
			return nil, fmt.Errorf("github.submit_review: options.event (APPROVE|REQUEST_CHANGES|COMMENT) is required")
		}
		var out struct {
			ID int64 `json:"id"`
		}
		body := map[string]any{"event": event}
		if b, _ := opts["body"].(string); b != "" {
			body["body"] = b
		}
		if err := g.post(ctx, tok, fmt.Sprintf("%s/repos/%s/pulls/%d/reviews", base, repo, number), body, &out); err != nil {
			return nil, err
		}
		return map[string]any{"id": out.ID}, nil
	case "add_labels":
		if number == 0 {
			return nil, fmt.Errorf("github.add_labels: options.number is required")
		}
		labels := toStrings(opts["labels"])
		if len(labels) == 0 {
			return nil, fmt.Errorf("github.add_labels: options.labels is required")
		}
		if err := g.post(ctx, tok, fmt.Sprintf("%s/repos/%s/issues/%d/labels", base, repo, number),
			map[string]any{"labels": labels}, nil); err != nil {
			return nil, err
		}
		return map[string]any{"ok": true}, nil
	}
	return nil, fmt.Errorf("github: unknown verb %q", verb)
}

// post issues one authenticated JSON POST against the GitHub API.
func (g *githubImpl) post(ctx context.Context, token, url string, body, out any) error {
	b, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("Content-Type", "application/json")
	resp, err := g.httpc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		var msg struct {
			Message string `json:"message"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&msg)
		if msg.Message != "" {
			return fmt.Errorf("POST %s: HTTP %d: %s", url, resp.StatusCode, msg.Message)
		}
		return fmt.Errorf("POST %s: HTTP %d", url, resp.StatusCode)
	}
	if out != nil {
		return json.NewDecoder(resp.Body).Decode(out)
	}
	return nil
}

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
