package connector

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"io"
	"net/http"
	"net/url"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/core"
	gh "github.com/NodeSpy/conductor/internal/integrations/github"
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
			Schema{
				"author_bot": {Type: TBool, Desc: "true = only bot reviewers trigger, false = only humans (absent = either)"},
			},
			Schema{
				"head_ref": {Type: TString},
				"author":   {Type: TString}, "author_is_bot": {Type: TBool, Desc: "the reviewer is an automated bot (account type Bot, or a [bot] login)"},
			}, nil),
		githubEvent("new_comment", "a new comment on your PR",
			Schema{
				"from_users":   {Type: TList, Desc: "only these commenters trigger (empty = any)"},
				"ignore_users": {Type: TList, Desc: "never trigger on these commenters"},
				"author_bot":   {Type: TBool, Desc: "true = only bot comments trigger, false = only humans (absent = either)"},
			},
			Schema{
				"author": {Type: TString}, "author_is_bot": {Type: TBool, Desc: "the commenter is an automated bot (account type Bot, or a [bot] login)"},
				"comment_body": {Type: TString}, "head_ref": {Type: TString},
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
			Name: "submit_review", Desc: "submit a PR review: a summary + verdict, with optional inline file:line comments",
			Options: Schema{
				"repo":  {Type: TString, Required: true},
				"pr":    {Type: TInt, Required: true},
				"body":  {Type: TString, Desc: "the review summary (top-level comment)"},
				"event": {Type: TString, Enum: []string{"APPROVE", "REQUEST_CHANGES", "COMMENT"}, Required: true},
				"comments": {Type: TList, Desc: "inline comments posted with the review: a list of " +
					"{path, line, body, side?, start_line?, start_side?}. line is the file's line number; " +
					"side defaults to RIGHT (the new version). start_line/start_side make a multi-line range. " +
					"Every commented line MUST fall within the PR's diff, or GitHub rejects the whole review."},
				"as": {Type: TString, Enum: []string{"me", "bot"}},
			},
			Outputs: Schema{"id": {Type: TInt}, "comments": {Type: TInt, Desc: "inline comments posted"}},
		},
		{
			Name: "pr_diff", Desc: "the PR's unified diff (cached; GitHub caps the .diff media type around 300 files)",
			Options: Schema{
				"repo": {Type: TString, Required: true}, "pr": {Type: TInt, Required: true},
				"as": {Type: TString, Enum: []string{"me", "bot"}},
			},
			Outputs: Schema{"diff": {Type: TString}},
		},
		{
			Name: "pr_get", Desc: "PR metadata: title, body, state, author, base/head, line counts, labels",
			Options: Schema{
				"repo": {Type: TString, Required: true}, "pr": {Type: TInt, Required: true},
				"as": {Type: TString, Enum: []string{"me", "bot"}},
			},
			Outputs: Schema{
				"title": {Type: TString}, "body": {Type: TString}, "state": {Type: TString},
				"draft": {Type: TBool}, "author": {Type: TString}, "base": {Type: TString},
				"head": {Type: TString}, "head_sha": {Type: TString}, "additions": {Type: TInt},
				"deletions": {Type: TInt}, "changed_files": {Type: TInt}, "labels": {Type: TList}, "url": {Type: TString},
			},
		},
		{
			Name: "pr_files", Desc: "changed files: [{path, status, additions, deletions, changes}] (100/page; pass page for more)",
			Options: Schema{
				"repo": {Type: TString, Required: true}, "pr": {Type: TInt, Required: true},
				"page": {Type: TInt, Desc: "1-based page (default 1; 100 files per page)"},
				"as":   {Type: TString, Enum: []string{"me", "bot"}},
			},
			Outputs: Schema{"files": {Type: TList}},
		},
		{
			Name: "review_comments", Desc: "existing inline review comments on the PR: [{path, line, body, user, id}] (100/page)",
			Options: Schema{
				"repo": {Type: TString, Required: true}, "pr": {Type: TInt, Required: true},
				"page": {Type: TInt, Desc: "1-based page (default 1)"},
				"as":   {Type: TString, Enum: []string{"me", "bot"}},
			},
			Outputs: Schema{"comments": {Type: TList}},
		},
		{
			Name: "file", Desc: "a repo file's raw contents at a ref (cached; GitHub's raw media type caps at ~1 MiB)",
			Options: Schema{
				"repo": {Type: TString, Required: true},
				"path": {Type: TString, Required: true, Desc: "repo-relative file path"},
				"ref":  {Type: TString, Desc: "branch / tag / sha (default: the repo's default branch)"},
				"as":   {Type: TString, Enum: []string{"me", "bot"}},
			},
			Outputs: Schema{"text": {Type: TString}},
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
		{
			Name: "sweep", Desc: "run the catch-up sweep now (daemon-global; same as `conductor sweep --now`)",
			Options: Schema{},
			Outputs: Schema{"nudged": {Type: TInt, Desc: "integrations whose sweep was nudged"}},
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

	// GET response cache (reads only) + last-seen rate-limit state, so a
	// fan-out of reviewers/verifiers that all want the same diff/metadata hits
	// GitHub once and backs off gracefully near the limit. Guarded by mu.
	mu          sync.Mutex
	getCache    map[string]*ghCacheEntry
	cacheTTL    time.Duration // how long a GET body is served without revalidating
	rlRemaining int           // X-RateLimit-Remaining from the last response (-1 = unknown)
	rlReset     time.Time     // when the primary limit resets
}

// ghCacheEntry is one cached GET body + its ETag (for cheap revalidation).
type ghCacheEntry struct {
	etag    string
	body    []byte
	fetched time.Time
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
		httpc:       &http.Client{Timeout: 20 * time.Second},
		ghToken:     ghAuthToken,
		getCache:    map[string]*ghCacheEntry{},
		cacheTTL:    defaultCacheTTL,
		rlRemaining: -1,
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
		Defaults:       gh.Rule{Me: g.conn.Me},
		// One catch-all rule carries every trigger as a variant: the legacy
		// resolve() only matches explicit rules (defaults never fire on their
		// own), and per-variant repos/exclude_repos gates scope each trigger.
		Rules: []gh.Rule{{
			Match:   gh.Match{Repos: []string{"*/*"}},
			Actions: actions,
		}},
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
	if b, ok := f["author_bot"].(bool); ok {
		act.AuthorBot = &b
	}
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
	// sweep is daemon-global (no repo/token): nudge the running catch-up
	// sweep now, exactly like SIGUSR1 / `conductor sweep --now`.
	if verb == "sweep" {
		nudged, err := runSweepHook(ctx)
		if err != nil {
			return nil, err
		}
		return map[string]any{"nudged": nudged}, nil
	}
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
		comments, err := reviewComments(opts["comments"])
		if err != nil {
			return nil, err
		}
		if len(comments) > 0 {
			body["comments"] = comments
		}
		if err := g.post(ctx, tok, fmt.Sprintf("%s/repos/%s/pulls/%d/reviews", base, repo, number), body, &out); err != nil {
			return nil, err
		}
		return map[string]any{"id": out.ID, "comments": len(comments)}, nil
	case "pr_diff":
		if number == 0 {
			return nil, fmt.Errorf("github.pr_diff: options.pr is required")
		}
		diff, err := g.getText(ctx, tok, fmt.Sprintf("%s/repos/%s/pulls/%d", base, repo, number), "application/vnd.github.diff")
		if err != nil {
			return nil, err
		}
		return map[string]any{"diff": diff}, nil
	case "pr_get":
		if number == 0 {
			return nil, fmt.Errorf("github.pr_get: options.pr is required")
		}
		var pr struct {
			Title        string `json:"title"`
			Body         string `json:"body"`
			State        string `json:"state"`
			Draft        bool   `json:"draft"`
			Merged       bool   `json:"merged"`
			Mergeable    *bool  `json:"mergeable"`
			Additions    int    `json:"additions"`
			Deletions    int    `json:"deletions"`
			ChangedFiles int    `json:"changed_files"`
			HTMLURL      string `json:"html_url"`
			User         struct {
				Login string `json:"login"`
			} `json:"user"`
			Base struct {
				Ref string `json:"ref"`
			} `json:"base"`
			Head struct {
				Ref string `json:"ref"`
				SHA string `json:"sha"`
			} `json:"head"`
			Labels []struct {
				Name string `json:"name"`
			} `json:"labels"`
		}
		if err := g.get(ctx, tok, fmt.Sprintf("%s/repos/%s/pulls/%d", base, repo, number), &pr); err != nil {
			return nil, err
		}
		labels := make([]string, 0, len(pr.Labels))
		for _, l := range pr.Labels {
			labels = append(labels, l.Name)
		}
		res := map[string]any{
			"title": pr.Title, "body": pr.Body, "state": pr.State, "draft": pr.Draft,
			"merged": pr.Merged, "author": pr.User.Login, "base": pr.Base.Ref,
			"head": pr.Head.Ref, "head_sha": pr.Head.SHA, "additions": pr.Additions,
			"deletions": pr.Deletions, "changed_files": pr.ChangedFiles, "labels": labels, "url": pr.HTMLURL,
		}
		if pr.Mergeable != nil {
			res["mergeable"] = *pr.Mergeable
		}
		return res, nil
	case "pr_files":
		if number == 0 {
			return nil, fmt.Errorf("github.pr_files: options.pr is required")
		}
		page := toInt(opts["page"])
		if page < 1 {
			page = 1
		}
		var raw []struct {
			Filename  string `json:"filename"`
			Status    string `json:"status"`
			Additions int    `json:"additions"`
			Deletions int    `json:"deletions"`
			Changes   int    `json:"changes"`
		}
		u := fmt.Sprintf("%s/repos/%s/pulls/%d/files?per_page=100&page=%d", base, repo, number, page)
		if err := g.get(ctx, tok, u, &raw); err != nil {
			return nil, err
		}
		files := make([]any, 0, len(raw))
		for _, f := range raw {
			files = append(files, map[string]any{
				"path": f.Filename, "status": f.Status,
				"additions": f.Additions, "deletions": f.Deletions, "changes": f.Changes,
			})
		}
		return map[string]any{"files": files}, nil
	case "review_comments":
		if number == 0 {
			return nil, fmt.Errorf("github.review_comments: options.pr is required")
		}
		page := toInt(opts["page"])
		if page < 1 {
			page = 1
		}
		var raw []struct {
			ID           int64  `json:"id"`
			Path         string `json:"path"`
			Line         int    `json:"line"`
			OriginalLine int    `json:"original_line"`
			Body         string `json:"body"`
			User         struct {
				Login string `json:"login"`
			} `json:"user"`
		}
		u := fmt.Sprintf("%s/repos/%s/pulls/%d/comments?per_page=100&page=%d", base, repo, number, page)
		if err := g.get(ctx, tok, u, &raw); err != nil {
			return nil, err
		}
		comments := make([]any, 0, len(raw))
		for _, c := range raw {
			line := c.Line
			if line == 0 {
				line = c.OriginalLine
			}
			comments = append(comments, map[string]any{
				"id": c.ID, "path": c.Path, "line": line, "body": c.Body, "user": c.User.Login,
			})
		}
		return map[string]any{"comments": comments}, nil
	case "file":
		path, _ := opts["path"].(string)
		if path == "" {
			return nil, fmt.Errorf("github.file: options.path is required")
		}
		u := fmt.Sprintf("%s/repos/%s/contents/%s", base, repo, path)
		if ref, _ := opts["ref"].(string); ref != "" {
			u += "?ref=" + url.QueryEscape(ref)
		}
		text, err := g.getText(ctx, tok, u, "application/vnd.github.raw")
		if err != nil {
			return nil, err
		}
		return map[string]any{"text": text}, nil
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
	g.noteRateLimit(resp)
	if resp.StatusCode/100 != 2 {
		if isRateLimited(resp) {
			return g.rateLimitError()
		}
		return ghHTTPError("POST", url, resp)
	}
	if out != nil {
		return json.NewDecoder(resp.Body).Decode(out)
	}
	return nil
}

const (
	// maxReadBytes caps a raw text read (a diff, a repo file) so a pathologically
	// large response can't exhaust memory. A body over the limit is truncated.
	maxReadBytes = 16 << 20 // 16 MiB
	// defaultCacheTTL is how long a GET body is served without revalidating —
	// long enough that a review fan-out (6 reviewers + verifiers, same PR) hits
	// the API once, short enough that a read after a write sees fresh data soon.
	defaultCacheTTL = 45 * time.Second
	// maxCacheEntries bounds the read cache over the daemon's lifetime.
	maxCacheEntries = 1024
	// maxRateWait caps how long a single GET will block waiting out a rate
	// limit before giving up (and serving stale, or erroring).
	maxRateWait = 30 * time.Second
)

// get issues an authenticated JSON GET (cached) and decodes into out.
func (g *githubImpl) get(ctx context.Context, token, url string, out any) error {
	b, err := g.cachedGet(ctx, token, url, "application/vnd.github+json")
	if err != nil {
		return err
	}
	if out != nil {
		return json.Unmarshal(b, out)
	}
	return nil
}

// getText issues an authenticated GET (cached) with a caller-supplied Accept
// (the diff or raw media type) and returns the body as a string.
func (g *githubImpl) getText(ctx context.Context, token, url, accept string) (string, error) {
	b, err := g.cachedGet(ctx, token, url, accept)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// cachedGet is the read path shared by every GET verb: an in-process TTL cache
// with ETag revalidation, and rate-limit awareness. A fresh entry is served
// without a round-trip; a stale one revalidates conditionally (a 304 costs no
// body). On a rate-limit response it waits out a short Retry-After once, then
// falls back to a stale cached body if it has one, else errors with the reset.
func (g *githubImpl) cachedGet(ctx context.Context, token, url, accept string) ([]byte, error) {
	key := cacheKey(token, accept, url)
	now := time.Now()

	g.mu.Lock()
	e := g.getCache[key]
	if e != nil && now.Sub(e.fetched) < g.cacheTTL {
		body := e.body
		g.mu.Unlock()
		return body, nil
	}
	etag := ""
	if e != nil {
		etag = e.etag
	}
	g.mu.Unlock()

	for attempt := 0; ; attempt++ {
		resp, err := g.getRaw(ctx, token, url, accept, etag)
		if err != nil {
			return nil, err
		}
		g.noteRateLimit(resp)

		switch {
		case resp.StatusCode == http.StatusNotModified:
			resp.Body.Close()
			g.mu.Lock()
			if e := g.getCache[key]; e != nil {
				e.fetched = time.Now()
				body := e.body
				g.mu.Unlock()
				return body, nil
			}
			g.mu.Unlock()
			etag = "" // cache was evicted under us — refetch unconditionally
			continue

		case resp.StatusCode/100 == 2:
			b, rerr := io.ReadAll(io.LimitReader(resp.Body, maxReadBytes))
			newEtag := resp.Header.Get("ETag")
			resp.Body.Close()
			if rerr != nil {
				return nil, rerr
			}
			g.storeCache(key, newEtag, b)
			return b, nil

		case isRateLimited(resp):
			wait := retryAfter(resp)
			resp.Body.Close()
			if attempt == 0 && wait > 0 && wait <= maxRateWait {
				if err := sleepCtx(ctx, wait); err != nil {
					return nil, err
				}
				continue // one retry after the window
			}
			// Prefer stale data over failing the caller — a review shouldn't
			// die because the limit blipped when we already hold the diff.
			g.mu.Lock()
			if e := g.getCache[key]; e != nil {
				body := e.body
				g.mu.Unlock()
				return body, nil
			}
			g.mu.Unlock()
			return nil, g.rateLimitError()

		default:
			err := ghHTTPError("GET", url, resp)
			resp.Body.Close()
			return nil, err
		}
	}
}

// getRaw builds and sends an authenticated GET; the caller reads/closes the
// body. A non-empty etag makes it a conditional request (If-None-Match).
func (g *githubImpl) getRaw(ctx context.Context, token, url, accept, etag string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", accept)
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if etag != "" {
		req.Header.Set("If-None-Match", etag)
	}
	return g.httpc.Do(req)
}

func (g *githubImpl) storeCache(key, etag string, body []byte) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if len(g.getCache) >= maxCacheEntries {
		now := time.Now()
		for k, e := range g.getCache { // drop expired first
			if now.Sub(e.fetched) >= g.cacheTTL {
				delete(g.getCache, k)
			}
		}
		if len(g.getCache) >= maxCacheEntries {
			g.getCache = map[string]*ghCacheEntry{} // still full: reset
		}
	}
	g.getCache[key] = &ghCacheEntry{etag: etag, body: body, fetched: time.Now()}
}

// noteRateLimit records the primary rate-limit state from a response's headers.
func (g *githubImpl) noteRateLimit(resp *http.Response) {
	rem, err := strconv.Atoi(resp.Header.Get("X-RateLimit-Remaining"))
	if err != nil {
		return
	}
	g.mu.Lock()
	g.rlRemaining = rem
	if s, err := strconv.ParseInt(resp.Header.Get("X-RateLimit-Reset"), 10, 64); err == nil {
		g.rlReset = time.Unix(s, 0)
	}
	g.mu.Unlock()
}

func (g *githubImpl) rateLimitError() error {
	g.mu.Lock()
	reset := g.rlReset
	g.mu.Unlock()
	if !reset.IsZero() {
		return fmt.Errorf("github: rate limit reached; resets in %s", time.Until(reset).Round(time.Second))
	}
	return fmt.Errorf("github: rate limit reached")
}

// isRateLimited reports whether a response is a GitHub rate-limit refusal —
// primary (403 with X-RateLimit-Remaining: 0) or secondary (403/429 with a
// Retry-After).
func isRateLimited(resp *http.Response) bool {
	if resp.StatusCode != http.StatusForbidden && resp.StatusCode != http.StatusTooManyRequests {
		return false
	}
	if resp.Header.Get("Retry-After") != "" {
		return true
	}
	return resp.Header.Get("X-RateLimit-Remaining") == "0"
}

// retryAfter is how long to wait before retrying a rate-limited response, from
// Retry-After (seconds) or the X-RateLimit-Reset epoch, clamped to a sane bound.
func retryAfter(resp *http.Response) time.Duration {
	if ra := resp.Header.Get("Retry-After"); ra != "" {
		if n, err := strconv.Atoi(strings.TrimSpace(ra)); err == nil && n >= 0 {
			return time.Duration(n) * time.Second
		}
	}
	if rs := resp.Header.Get("X-RateLimit-Reset"); rs != "" {
		if s, err := strconv.ParseInt(rs, 10, 64); err == nil {
			if d := time.Until(time.Unix(s, 0)); d > 0 {
				return d
			}
		}
	}
	return 0
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// cacheKey namespaces a cached GET by token (so as:me and as:bot never share)
// without storing the secret, plus the Accept (diff vs json vs raw differ) and
// the URL.
func cacheKey(token, accept, url string) string {
	h := fnv.New64a()
	_, _ = h.Write([]byte(token))
	return strconv.FormatUint(h.Sum64(), 36) + "\x00" + accept + "\x00" + url
}

// ghHTTPError renders a non-2xx GitHub response into an error, surfacing the
// API's own message when present.
func ghHTTPError(method, url string, resp *http.Response) error {
	var msg struct {
		Message string `json:"message"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&msg)
	if msg.Message != "" {
		return fmt.Errorf("%s %s: HTTP %d: %s", method, url, resp.StatusCode, msg.Message)
	}
	return fmt.Errorf("%s %s: HTTP %d", method, url, resp.StatusCode)
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
// reviewComments coerces the submit_review `comments` option into GitHub review
// comment objects. Each needs a path + body; line/side/start_line/start_side are
// passed through when set (a line-based comment defaults to side RIGHT — the new
// version of the file). GitHub requires every commented line to fall within the
// PR's diff; a comment outside it makes the whole review 422, so callers should
// only comment on changed lines. nil/empty is fine — a review with no inline
// comments, just a summary + verdict.
func reviewComments(v any) ([]map[string]any, error) {
	if v == nil {
		return nil, nil
	}
	raw, ok := v.([]any)
	if !ok {
		return nil, fmt.Errorf("github.submit_review: comments must be a list of {path, line, body}, got %T", v)
	}
	out := make([]map[string]any, 0, len(raw))
	for i, e := range raw {
		m, ok := e.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("github.submit_review: comments[%d] must be an object {path, line, body}, got %T", i, e)
		}
		path, _ := m["path"].(string)
		cbody, _ := m["body"].(string)
		if path == "" || cbody == "" {
			return nil, fmt.Errorf("github.submit_review: comments[%d] needs a non-empty path and body", i)
		}
		c := map[string]any{"path": path, "body": cbody}
		if n := toInt(m["line"]); n > 0 {
			c["line"] = n
		}
		if s, _ := m["side"].(string); s != "" {
			c["side"] = s
		}
		if n := toInt(m["start_line"]); n > 0 {
			c["start_line"] = n
		}
		if s, _ := m["start_side"].(string); s != "" {
			c["start_side"] = s
		}
		out = append(out, c)
	}
	return out, nil
}

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
