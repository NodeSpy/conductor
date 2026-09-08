# GitHub connector

```yaml
connectors:
  gh:
    type: github
    app:                                   # GitHub App credentials (optional — see App-less below)
      app_id: 123456
      private_key_path: ~/.config/conductor/github-app.pem
      webhook_secret: ${GH_WEBHOOK_SECRET}
      verify_signature: true               # default true
    # token: ${GH_PAT}                     # App-less read credential
    webhook: { smee_url: ${GH_SMEE_URL} }  # and/or listen: + path:
    sweep: { enabled: true, repos: ["your-org/*"] }
    me: { logins: [your-login] }           # defines "you"
    repos: ["your-org/*"]                  # default trigger scope
    identity: { read_token: app, write_token: gh_auth, commit_author: self }
    project_map: { Org/Repo: paseo/project }
    project_rewrite: { org: paseo-org }
    retry: { max: 3, backoff: 10s }
    options: { as: me }
    policy: { ignore: { users: ["dependabot[bot]"] }, pause_label: "conductor:hold" }
```

## Credentials: app → token → gh

Reads resolve a GitHub App installation token when `app:` is configured, else
the `token:` PAT, else the `gh` CLI's stored login. **App-less operation
works**: events arrive via a plain webhook (+ `webhook_secret`) or the sweep
(explicit repos — glob expansion is an App endpoint), and reads use the PAT /
gh token. Writes are always you (`identity.write_token`: `gh_auth` default or
a literal) unless a verb sets `as: bot` — which requires App credentials.

## Events (`on: gh.<event>`)

All events accept `repos:` / `exclude_repos:` filters (globs; default: the
connector's `repos:`) and publish the base context (`repo`, `owner`, `name`,
`pr`, `issue`, `number`, `head`, `base`, `url`, `kind`, `title`, `labels`).
Per-event additions:

| event | extra filters | extra context / options |
|---|---|---|
| `review_requested` | `reviewer: {logins, teams}`, `gates: {not_draft}`, `exclude: {branches, labels, title}` | |
| `changes_requested` | | `head_ref` |
| `new_comment` | `from_users`, `ignore_users` | `author`, `comment_body`, `comment_id`, `comment_kind`, `head_ref` |
| `merge_conflict`, `pr_behind`, `self_review` | | |
| `failing_checks` | `ignore_checks` | `failing_check`, `run_id` (the Actions *workflow run* id, resolved from a `check_run`/`check_suite`; `0` for a non-Actions check); options `flaky_rerun: {enabled, max}` — reruns the failed run once it has finished, before the fixer; a rerun that couldn't be requested isn't counted toward `max` |
| `stuck_checks` | | `run_id`, `run_name`, `run_status`; options `stuck_after`, `poll_interval` |
| `merge_ready` | `require_label`, `gates: {not_draft, merge_state, review_decision, non_author_approval, threads_resolved}` | |
| `issue_matched` | `assignee`, `sole_assignee`, `labels_any`, `labels_all`, `authors`, `exclude`, `gates: {no_branch, project}` | |
| `release` | `include_prereleases` | `tag_name`, `prerelease`, `draft` |
| `deployment_status` | | `state`, `environment`, `description` |
| `dependabot_alert` | | `severity`, `package`, `summary` |
| `secret_scanning_alert` | | `secret_type` |

Every event also accepts the option `max_attempts_per_head`. Triggers on the
same event are independent — each matching trigger fires (a variant `name:`
keeps their dedup state separate).

## Verbs (`uses: gh.<verb>`)

| verb | options | outputs |
|---|---|---|
| `comment` | `repo`*, `number`/`pr`*, `body`*, `as` | `id`, `url` |
| `reply` | `repo`*, `pr`*, `in_reply_to`*, `body`*, `as` | `id`, `url` |
| `request_review` | `repo`*, `pr`*, `reviewers`/`team_reviewers`, `as` | `ok` — request review (also re-requests a prior reviewer) |
| `rerequest_review` | `repo`*, `pr`*, `reviewers`/`team_reviewers`, `as` | `ok` — alias of `request_review` |
| `remove_reviewer` | `repo`*, `pr`*, `reviewers`/`team_reviewers`, `as` | `ok` — cancel a pending review request |
| `submit_review` | `repo`*, `pr`*, `event`* (APPROVE\|REQUEST_CHANGES\|COMMENT), `body`, `comments`, `as` | `id`, `comments` |
| `add_labels` | `repo`*, `number`*, `labels`*, `as` | `ok` |
| `sweep` | — | `nudged` — run the catch-up sweep now (daemon-global; `conductor sweep --now`, verb-shaped) |
| `pr_diff` | `repo`*, `pr`*, `as` | `diff` — the PR's unified diff |
| `pr_get` | `repo`*, `pr`*, `as` | `title`, `body`, `state`, `draft`, `author`, `base`, `head`, `head_sha`, `additions`, `deletions`, `changed_files`, `labels`, `url` |
| `pr_files` | `repo`*, `pr`*, `all`, `as` | `files`: `[{path, status, additions, deletions, changes}]` (100/page) |
| `review_comments` | `repo`*, `pr`*, `all`, `as` | `comments`: existing inline review comments `[{path, line, body, user, id}]` |
| `file` | `repo`*, `path`*, `ref`, `as` | `text` — a repo file's raw contents at a ref |
| `create_pr` | `repo`*, `title`*, `head`*, `base`*, `body`, `draft`, `as` | `number`, `url` |
| `merge_pr` | `repo`*, `pr`*, `method` (merge\|squash\|rebase), `commit_title`, `commit_message`, `sha`, `as` | `merged`, `sha` |
| `update_pr` | `repo`*, `pr`*, `state` (open\|closed), `title`, `body`, `base`, `as` | `number`, `state` — close/reopen/edit |
| `create_issue` | `repo`*, `title`*, `body`, `labels`, `assignees`, `as` | `number`, `url` |
| `update_issue` | `repo`*, `number`*, `state`, `state_reason`, `title`, `body`, `as` | `number`, `state` — close/reopen/edit |
| `assign` | `repo`*, `number`*/`pr`, `add`, `remove`, `as` | `assignees` |
| `remove_label` | `repo`*, `number`*, `label`*, `as` | `ok` |
| `get_issue` | `repo`*, `number`*, `as` | `title`, `body`, `state`, `labels`, `assignees`, `author`, `url` |
| `put_file` | `repo`*, `path`*, `content`*, `message`*, `branch`, `sha`, `as` | `commit`, `sha` — create or update in one commit |
| `delete_file` | `repo`*, `path`*, `message`*, `sha`*, `branch`, `as` | `commit` |
| `get_ref` | `repo`*, `ref`*, `as` | `sha` — the commit a branch/tag/ref points at |
| `create_branch` | `repo`*, `branch`*, `from`, `as` | `sha` — branch off another ref (default the default branch's HEAD) |
| `dispatch_workflow` | `repo`*, `workflow`*, `ref`*, `inputs`, `as` | `ok` — trigger a workflow_dispatch |
| `rerun_run` | `repo`*, `run_id`*, `failed_only`, `as` | `ok` |
| `cancel_run` | `repo`*, `run_id`*, `as` | `ok` |
| `list_runs` | `repo`*, `branch`, `status`, `per_page`, `all`, `as` | `runs` — recent workflow runs |
| `checks` | `repo`*, `ref`*, `as` | `checks` — check-run status for a ref |
| `create_release` | `repo`*, `tag`*, `target`, `name`, `body`, `draft`, `prerelease`, `as` | `id`, `url`, `upload_url` |
| `upload_asset` | `repo`*, `release_id`*, `name`*, `content`/`path`, `content_type`, `as` | `id`, `url` |
| `list_issues` | `repo`*, `state`, `labels`, `assignee`, `per_page`, `all`, `as` | `issues` (PRs excluded) |
| `search_issues` | `repo`*, `q`*, `per_page`, `all`, `as` | `total`, `items` — search scoped to the repo |
| `ready_for_review` | `repo`*, `pr`*, `as` | `ok` — mark a draft PR ready (GraphQL) |
| `convert_to_draft` | `repo`*, `pr`*, `as` | `ok` — convert a PR back to draft (GraphQL) |
| `create_gist` | `files`*, `description`, `public` | `id`, `url` — user-scoped, no repo |
| `get_gist` | `id`* | `files` `{name: content}`, `description`, `public`, `url` |
| `update_gist` | `id`*, `files`, `description` | `id`, `url` |
| `delete_gist` | `id`* | `ok` |
| `list_gists` | `user`, `per_page`, `all` | `gists` — your gists (or a user's public ones) |

The **read** verbs (`pr_diff`, `pr_get`, `pr_files`, `review_comments`, `file`)
are cached in-process for ~45s with ETag revalidation, so a fan-out that all
wants the same PR (e.g. a multi-reviewer workflow) hits GitHub once. Every call
tracks the rate limit: on a limit refusal the connector waits out a short
`Retry-After` once, then serves a cached copy if it has one, else errors with
the reset time — so a review job degrades instead of storming the API.

The **list** verbs (`pr_files`, `review_comments`, `list_issues`, `list_runs`,
`search_issues`, `list_gists`) return one page (~100) by default; pass
`all: true` to follow pagination to completion.

`as: me` (default) posts as you; `as: bot` as the App's bot user.

`submit_review` posts a real code review — a summary (`body`) + a verdict
(`event`) and, optionally, **inline comments** anchored to file lines:

```yaml
uses: gh.submit_review
options:
  repo: "{{.repo}}"
  pr: "{{.pr}}"
  event: REQUEST_CHANGES        # APPROVE | REQUEST_CHANGES | COMMENT
  body: "2 blocking issues; details inline."
  comments:                     # each line MUST be within the PR's diff
    - { path: internal/x.go, line: 42, body: "nil deref when cfg is empty." }
    - { path: internal/y.go, line: 8, start_line: 5, side: RIGHT, body: "tighten this range." }
```

Each comment needs `path` + `body`; `line` is the file's line number and
`side` defaults to `RIGHT` (the new version). `start_line`/`start_side` make a
multi-line range. Omit `line` for a file-level comment. GitHub rejects the
**whole** review (422) if any commented line falls outside the diff, so only
comment on changed lines. Output `comments` is the count posted.

## Legacy

The legacy `integrations: - type: github` block with its `rules:`/`defaults:`
model still loads unchanged; [[Migration]] flattens it into per-trigger
filters with the same most-specific-repo winner.

Related: [[Connectors]] · [[GitHub-App-Setup]] · [[Workflows]] · [[Migration]]
