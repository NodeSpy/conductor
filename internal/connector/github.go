package connector

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/core"
	gh "github.com/NodeSpy/conductor/internal/integrations/github"
	"github.com/NodeSpy/conductor/pkg/githubkit"
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
				"repo":   {Type: TString, Required: true, Scope: "repo"},
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
				"repo":        {Type: TString, Required: true, Scope: "repo"},
				"pr":          {Type: TInt, Required: true},
				"in_reply_to": {Type: TInt, Required: true, Desc: "review comment id to reply to"},
				"body":        {Type: TString, Required: true},
				"as":          {Type: TString, Enum: []string{"me", "bot"}},
			},
			Outputs: Schema{"id": {Type: TInt}, "url": {Type: TString}},
		},
		{
			Name: "request_review", Desc: "request review from users/teams on a PR (also re-requests one who already reviewed)",
			Options: Schema{
				"repo":           {Type: TString, Required: true, Scope: "repo"},
				"pr":             {Type: TInt, Required: true},
				"reviewers":      {Type: TList, Desc: "user logins"},
				"team_reviewers": {Type: TList, Desc: "team slugs"},
				"as":             {Type: TString, Enum: []string{"me", "bot"}},
			},
			Outputs: Schema{"ok": {Type: TBool}},
		},
		{
			// Back-compat alias of request_review: GitHub has one endpoint for
			// requesting reviewers, and re-requesting a prior reviewer is the
			// same call. Kept because live configs reference it for the
			// re-review-on-new-changes flow.
			Name: "rerequest_review", Desc: "re-request review (alias of request_review)",
			Options: Schema{
				"repo":           {Type: TString, Required: true, Scope: "repo"},
				"pr":             {Type: TInt, Required: true},
				"reviewers":      {Type: TList, Desc: "logins"},
				"team_reviewers": {Type: TList, Desc: "team slugs"},
				"as":             {Type: TString, Enum: []string{"me", "bot"}},
			},
			Outputs: Schema{"ok": {Type: TBool}},
		},
		{
			Name: "remove_reviewer", Desc: "cancel a pending review request (remove requested users/teams)",
			Options: Schema{
				"repo":           {Type: TString, Required: true, Scope: "repo"},
				"pr":             {Type: TInt, Required: true},
				"reviewers":      {Type: TList, Desc: "user logins to un-request"},
				"team_reviewers": {Type: TList, Desc: "team slugs to un-request"},
				"as":             {Type: TString, Enum: []string{"me", "bot"}},
			},
			Outputs: Schema{"ok": {Type: TBool}},
		},
		{
			Name: "submit_review", Desc: "submit a PR review: a summary + verdict, with optional inline file:line comments",
			Options: Schema{
				"repo":  {Type: TString, Required: true, Scope: "repo"},
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
				"repo": {Type: TString, Required: true, Scope: "repo"}, "pr": {Type: TInt, Required: true},
				"as": {Type: TString, Enum: []string{"me", "bot"}},
			},
			Outputs: Schema{"diff": {Type: TString}},
		},
		{
			Name: "pr_get", Desc: "PR metadata: title, body, state, author, base/head, line counts, labels",
			Options: Schema{
				"repo": {Type: TString, Required: true, Scope: "repo"}, "pr": {Type: TInt, Required: true},
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
				"repo": {Type: TString, Required: true, Scope: "repo"}, "pr": {Type: TInt, Required: true},
				"all": {Type: TBool, Desc: "fetch every page (default: first 100)"},
				"as":  {Type: TString, Enum: []string{"me", "bot"}},
			},
			Outputs: Schema{"files": {Type: TList}},
		},
		{
			Name: "review_comments", Desc: "existing inline review comments on the PR: [{path, line, body, user, id}] (100/page)",
			Options: Schema{
				"repo": {Type: TString, Required: true, Scope: "repo"}, "pr": {Type: TInt, Required: true},
				"all": {Type: TBool, Desc: "fetch every page (default: first 100)"},
				"as":  {Type: TString, Enum: []string{"me", "bot"}},
			},
			Outputs: Schema{"comments": {Type: TList}},
		},
		{
			Name: "file", Desc: "a repo file's raw contents at a ref (cached; GitHub's raw media type caps at ~1 MiB)",
			Options: Schema{
				"repo":     {Type: TString, Required: true, Scope: "repo"},
				"path":     {Type: TString, Required: true, Desc: "repo-relative file path"},
				"ref":      {Type: TString, Desc: "branch / tag / sha (default: the repo's default branch)"},
				"as":       {Type: TString, Enum: []string{"me", "bot"}},
				"optional": {Type: TBool, Desc: "return empty text instead of erroring when the file is missing (404) — for optional convention files"},
			},
			Outputs: Schema{"text": {Type: TString}},
		},
		{
			Name: "create_pr", Desc: "open a pull request",
			Options: Schema{
				"repo":  {Type: TString, Required: true, Scope: "repo"},
				"title": {Type: TString, Required: true},
				"head":  {Type: TString, Required: true, Desc: "the branch with your changes (owner:branch for a fork)"},
				"base":  {Type: TString, Required: true, Desc: "the branch to merge into"},
				"body":  {Type: TString},
				"draft": {Type: TBool},
				"as":    {Type: TString, Enum: []string{"me", "bot"}},
			},
			Outputs: Schema{"number": {Type: TInt}, "url": {Type: TString}},
		},
		{
			Name: "merge_pr", Desc: "merge a pull request",
			Options: Schema{
				"repo": {Type: TString, Required: true, Scope: "repo"}, "pr": {Type: TInt, Required: true},
				"method":         {Type: TString, Enum: []string{"merge", "squash", "rebase"}, Desc: "default merge"},
				"commit_title":   {Type: TString},
				"commit_message": {Type: TString},
				"sha":            {Type: TString, Desc: "require the PR head to match this sha (safety)"},
				"as":             {Type: TString, Enum: []string{"me", "bot"}},
			},
			Outputs: Schema{"merged": {Type: TBool}, "sha": {Type: TString}},
		},
		{
			Name: "update_pr", Desc: "edit a PR: state (open|closed → close/reopen), title, body, base",
			Options: Schema{
				"repo": {Type: TString, Required: true, Scope: "repo"}, "pr": {Type: TInt, Required: true},
				"state": {Type: TString, Enum: []string{"open", "closed"}},
				"title": {Type: TString}, "body": {Type: TString},
				"base": {Type: TString, Desc: "retarget the PR onto this branch"},
				"as":   {Type: TString, Enum: []string{"me", "bot"}},
			},
			Outputs: Schema{"number": {Type: TInt}, "state": {Type: TString}},
		},
		{
			Name: "create_issue", Desc: "open an issue",
			Options: Schema{
				"repo": {Type: TString, Required: true, Scope: "repo"}, "title": {Type: TString, Required: true},
				"body":   {Type: TString},
				"labels": {Type: TList}, "assignees": {Type: TList, Desc: "logins to assign"},
				"as": {Type: TString, Enum: []string{"me", "bot"}},
			},
			Outputs: Schema{"number": {Type: TInt}, "url": {Type: TString}},
		},
		{
			Name: "update_issue", Desc: "edit an issue: state (open|closed → close/reopen), state_reason, title, body",
			Options: Schema{
				"repo": {Type: TString, Required: true, Scope: "repo"}, "number": {Type: TInt, Required: true},
				"state":        {Type: TString, Enum: []string{"open", "closed"}},
				"state_reason": {Type: TString, Enum: []string{"completed", "not_planned", "reopened"}},
				"title":        {Type: TString}, "body": {Type: TString},
				"as": {Type: TString, Enum: []string{"me", "bot"}},
			},
			Outputs: Schema{"number": {Type: TInt}, "state": {Type: TString}},
		},
		{
			Name: "assign", Desc: "add and/or remove issue/PR assignees",
			Options: Schema{
				"repo":   {Type: TString, Required: true, Scope: "repo"},
				"number": {Type: TInt, Desc: "issue or PR number (alias: pr)"}, "pr": {Type: TInt},
				"add": {Type: TList, Desc: "logins to assign"}, "remove": {Type: TList, Desc: "logins to unassign"},
				"as": {Type: TString, Enum: []string{"me", "bot"}},
			},
			Outputs: Schema{"assignees": {Type: TList}},
		},
		{
			Name: "remove_label", Desc: "remove one label from an issue or PR",
			Options: Schema{
				"repo": {Type: TString, Required: true, Scope: "repo"}, "number": {Type: TInt, Required: true},
				"label": {Type: TString, Required: true},
				"as":    {Type: TString, Enum: []string{"me", "bot"}},
			},
			Outputs: Schema{"ok": {Type: TBool}},
		},
		{
			Name: "get_issue", Desc: "read an issue: title, body, state, labels, assignees, author, url",
			Options: Schema{
				"repo": {Type: TString, Required: true, Scope: "repo"}, "number": {Type: TInt, Required: true},
				"as": {Type: TString, Enum: []string{"me", "bot"}},
			},
			Outputs: Schema{
				"title": {Type: TString}, "body": {Type: TString}, "state": {Type: TString},
				"labels": {Type: TList}, "assignees": {Type: TList}, "author": {Type: TString}, "url": {Type: TString},
			},
		},
		{
			Name: "put_file", Desc: "create or update a file in one commit",
			Options: Schema{
				"repo": {Type: TString, Required: true, Scope: "repo"}, "path": {Type: TString, Required: true},
				"content": {Type: TString, Required: true, Desc: "the new file content (UTF-8 text; base64-encoded for the API automatically)"},
				"message": {Type: TString, Required: true, Desc: "commit message"},
				"branch":  {Type: TString, Desc: "branch to commit on (default: the repo's default branch)"},
				"sha":     {Type: TString, Desc: "blob sha of the file being replaced (required to UPDATE an existing file; get it from `file`)"},
				"as":      {Type: TString, Enum: []string{"me", "bot"}},
			},
			Outputs: Schema{"commit": {Type: TString}, "sha": {Type: TString, Desc: "the new blob sha"}},
		},
		{
			Name: "delete_file", Desc: "delete a file in one commit",
			Options: Schema{
				"repo": {Type: TString, Required: true, Scope: "repo"}, "path": {Type: TString, Required: true},
				"message": {Type: TString, Required: true},
				"sha":     {Type: TString, Required: true, Desc: "blob sha of the file to delete"},
				"branch":  {Type: TString},
				"as":      {Type: TString, Enum: []string{"me", "bot"}},
			},
			Outputs: Schema{"commit": {Type: TString}},
		},
		{
			Name: "get_ref", Desc: "the commit sha a branch/tag/ref points at",
			Options: Schema{
				"repo": {Type: TString, Required: true, Scope: "repo"},
				"ref":  {Type: TString, Required: true, Desc: "branch, tag, or sha"},
				"as":   {Type: TString, Enum: []string{"me", "bot"}},
			},
			Outputs: Schema{"sha": {Type: TString}},
		},
		{
			Name: "create_branch", Desc: "create a branch from another ref",
			Options: Schema{
				"repo":   {Type: TString, Required: true, Scope: "repo"},
				"branch": {Type: TString, Required: true, Desc: "new branch name"},
				"from":   {Type: TString, Desc: "source branch/tag/sha (default: the default branch's HEAD)"},
				"as":     {Type: TString, Enum: []string{"me", "bot"}},
			},
			Outputs: Schema{"sha": {Type: TString}},
		},
		{
			Name: "dispatch_workflow", Desc: "trigger a workflow_dispatch run",
			Options: Schema{
				"repo":     {Type: TString, Required: true, Scope: "repo"},
				"workflow": {Type: TString, Required: true, Desc: "workflow file name (ci.yml) or numeric id"},
				"ref":      {Type: TString, Required: true, Desc: "branch or tag to run on"},
				"inputs":   {Type: TMap, Desc: "workflow_dispatch inputs"},
				"as":       {Type: TString, Enum: []string{"me", "bot"}},
			},
			Outputs: Schema{"ok": {Type: TBool}},
		},
		{
			Name: "rerun_run", Desc: "re-run a workflow run (optionally only its failed jobs)",
			Options: Schema{
				"repo": {Type: TString, Required: true, Scope: "repo"}, "run_id": {Type: TInt, Required: true},
				"failed_only": {Type: TBool, Desc: "re-run only failed jobs"},
				"as":          {Type: TString, Enum: []string{"me", "bot"}},
			},
			Outputs: Schema{"ok": {Type: TBool}},
		},
		{
			Name: "cancel_run", Desc: "cancel a workflow run",
			Options: Schema{
				"repo": {Type: TString, Required: true, Scope: "repo"}, "run_id": {Type: TInt, Required: true},
				"as": {Type: TString, Enum: []string{"me", "bot"}},
			},
			Outputs: Schema{"ok": {Type: TBool}},
		},
		{
			Name: "list_runs", Desc: "recent workflow runs: [{id, name, status, conclusion, head_branch, head_sha, url}]",
			Options: Schema{
				"repo":     {Type: TString, Required: true, Scope: "repo"},
				"branch":   {Type: TString, Desc: "filter to a branch"},
				"status":   {Type: TString, Desc: "queued|in_progress|completed|success|failure|…"},
				"per_page": {Type: TInt, Desc: "default 20, max 100"},
				"all":      {Type: TBool, Desc: "fetch every page"},
				"as":       {Type: TString, Enum: []string{"me", "bot"}},
			},
			Outputs: Schema{"runs": {Type: TList}},
		},
		{
			Name: "create_release", Desc: "publish a release for a tag",
			Options: Schema{
				"repo":   {Type: TString, Required: true, Scope: "repo"},
				"tag":    {Type: TString, Required: true, Desc: "the tag to release (created if it doesn't exist, on target)"},
				"target": {Type: TString, Desc: "commitish the tag points at when created (default: default branch)"},
				"name":   {Type: TString, Desc: "release title"}, "body": {Type: TString, Desc: "release notes"},
				"draft": {Type: TBool}, "prerelease": {Type: TBool},
				"as": {Type: TString, Enum: []string{"me", "bot"}},
			},
			Outputs: Schema{"id": {Type: TInt}, "url": {Type: TString}, "upload_url": {Type: TString}},
		},
		{
			Name: "upload_asset", Desc: "attach a file to a release",
			Options: Schema{
				"repo":         {Type: TString, Required: true, Scope: "repo"},
				"release_id":   {Type: TInt, Required: true, Desc: "id from create_release"},
				"name":         {Type: TString, Required: true, Desc: "asset file name"},
				"content":      {Type: TString, Desc: "inline asset bytes (mutually exclusive with path)"},
				"path":         {Type: TString, Desc: "local file to upload"},
				"content_type": {Type: TString, Desc: "MIME type (default application/octet-stream)"},
				"as":           {Type: TString, Enum: []string{"me", "bot"}},
			},
			Outputs: Schema{"id": {Type: TInt}, "url": {Type: TString}},
		},
		{
			Name: "list_issues", Desc: "list issues (PRs excluded): [{number, title, state, labels, author, url}]",
			Options: Schema{
				"repo":     {Type: TString, Required: true, Scope: "repo"},
				"state":    {Type: TString, Desc: "open|closed|all (default open)"},
				"labels":   {Type: TList, Desc: "filter to issues with all these labels"},
				"assignee": {Type: TString, Desc: "filter to this assignee (or * / none)"},
				"per_page": {Type: TInt, Desc: "default 30, max 100"},
				"all":      {Type: TBool, Desc: "fetch every page"},
				"as":       {Type: TString, Enum: []string{"me", "bot"}},
			},
			Outputs: Schema{"issues": {Type: TList}},
		},
		{
			Name: "search_issues", Desc: "search issues/PRs in this repo: [{number, title, state, is_pr, url}]",
			Options: Schema{
				"repo":     {Type: TString, Required: true, Scope: "repo"},
				"q":        {Type: TString, Required: true, Desc: "GitHub search query (scoped to this repo automatically)"},
				"per_page": {Type: TInt, Desc: "default 30, max 100"},
				"all":      {Type: TBool, Desc: "fetch every page"},
				"as":       {Type: TString, Enum: []string{"me", "bot"}},
			},
			Outputs: Schema{"total": {Type: TInt}, "items": {Type: TList}},
		},
		{
			Name: "checks", Desc: "check-run status for a ref: [{name, status, conclusion, url}]",
			Options: Schema{
				"repo": {Type: TString, Required: true, Scope: "repo"},
				"ref":  {Type: TString, Required: true, Desc: "branch, tag, or sha"},
				"as":   {Type: TString, Enum: []string{"me", "bot"}},
			},
			Outputs: Schema{"checks": {Type: TList}},
		},
		{
			Name: "ready_for_review", Desc: "mark a draft PR ready for review",
			Options: Schema{
				"repo": {Type: TString, Required: true, Scope: "repo"}, "pr": {Type: TInt, Required: true},
				"as": {Type: TString, Enum: []string{"me", "bot"}},
			},
			Outputs: Schema{"ok": {Type: TBool}},
		},
		{
			Name: "convert_to_draft", Desc: "convert a PR back to a draft",
			Options: Schema{
				"repo": {Type: TString, Required: true, Scope: "repo"}, "pr": {Type: TInt, Required: true},
				"as": {Type: TString, Enum: []string{"me", "bot"}},
			},
			Outputs: Schema{"ok": {Type: TBool}},
		},
		{
			Name: "create_gist", Desc: "create a gist (user-scoped, no repo)",
			Options: Schema{
				"files":       {Type: TMap, Required: true, Desc: "{filename: content} — the gist's files"},
				"description": {Type: TString},
				"public":      {Type: TBool, Desc: "default false (secret gist)"},
			},
			Outputs: Schema{"id": {Type: TString}, "url": {Type: TString}},
		},
		{
			Name: "get_gist", Desc: "read a gist: its files, description, visibility",
			Options: Schema{
				"id": {Type: TString, Required: true},
			},
			Outputs: Schema{"files": {Type: TMap, Desc: "{filename: content}"}, "description": {Type: TString}, "public": {Type: TBool}, "url": {Type: TString}},
		},
		{
			Name: "update_gist", Desc: "edit a gist's files and/or description",
			Options: Schema{
				"id":          {Type: TString, Required: true},
				"files":       {Type: TMap, Desc: "{filename: content}; a null/empty content deletes that file"},
				"description": {Type: TString},
			},
			Outputs: Schema{"id": {Type: TString}, "url": {Type: TString}},
		},
		{
			Name: "delete_gist", Desc: "delete a gist",
			Options: Schema{"id": {Type: TString, Required: true}},
			Outputs: Schema{"ok": {Type: TBool}},
		},
		{
			Name: "list_gists", Desc: "list gists: [{id, description, public, url}]",
			Options: Schema{
				"user":     {Type: TString, Desc: "whose public gists (default: your own, incl. secret)"},
				"per_page": {Type: TInt, Desc: "default 30, max 100"},
				"all":      {Type: TBool, Desc: "fetch every page, not just the first"},
			},
			Outputs: Schema{"gists": {Type: TList}},
		},
		{
			Name: "add_labels", Desc: "add labels to an issue or PR",
			Options: Schema{
				"repo":   {Type: TString, Required: true, Scope: "repo"},
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

	// kit is the daemon-agnostic GitHub client (pkg/githubkit) that every verb
	// call delegates to — credentials, HTTP mechanics, caching, rate-limit
	// handling, and the verb switch all live there now.
	kit *githubkit.Client
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
	kitCfg := githubkit.Config{
		Token:      conn.Token,
		WriteToken: conn.Identity.WriteToken,
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

// Invoke runs a github verb. sweep is daemon-global (no repo/token involved)
// and is intercepted here; every other verb delegates to the daemon-agnostic
// githubkit.Client, which resolves the `as: me|bot` identity, issues the
// authenticated API call, and returns its outputs.
func (g *githubImpl) Invoke(ctx context.Context, verb string, opts map[string]any) (map[string]any, error) {
	if verb == "sweep" {
		nudged, err := runSweepHook(ctx)
		if err != nil {
			return nil, err
		}
		return map[string]any{"nudged": nudged}, nil
	}
	return g.kit.Invoke(ctx, verb, opts)
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
