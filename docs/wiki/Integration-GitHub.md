# GitHub connector

```yaml
connectors:
  gh:
    use: github
    app:                                   # GitHub App credentials (optional — see App-less below)
      app_id: 123456
      private_key_path: ~/.config/conductor/github-app.pem
    # token: ${GH_PAT}                     # App-less read credential
    webhook:                               # transport AND delivery auth
      smee_url: ${GH_SMEE_URL}             # and/or listen: + path:
      secret: ${GH_WEBHOOK_SECRET}
      verify_signature: true               # default true
    sweep: { repos: ["your-org/*"] }        # optional — on by default, all installed repos; see Sweep below
    # me: { logins: [your-login] }         # optional — auto-discovered from your write identity; set to override
    repos: ["your-org/*"]                  # default trigger scope
    identity: { read_token: app, write_token: gh_auth, commit_author: self }
    project_map: { Org/Repo: paseo/project }
    project_rewrite: { org: paseo-org }
    retry: { max: 3, backoff: 10s }
    options: { as: me }
    policy: { ignore: { users: ["dependabot[bot]"] }, pause_label: "conductor:hold" }
```

## Setting up the GitHub App

conductor reads GitHub as a **GitHub App** and receives events through the App's
webhook. Here's the short version; the full walkthrough — screenshots-level detail
plus running **without** an App — is on **[[GitHub-App-Setup]]**.

1. **Register the App** at
   [github.com/settings/apps/new](https://github.com/settings/apps/new) (personal)
   or `https://github.com/organizations/<your-org>/settings/apps/new` (org). The App
   **name** becomes the bot login you list under `me:`.
2. **Permissions** — set these *before* events (GitHub only lists events for
   permissions you've granted): Contents **Read & write**, Pull requests
   **Read & write**, Issues **Read & write**, Checks **Read-only**, Metadata
   **Read-only**, and Organization → Projects **Read-only** (org installs only —
   this is what surfaces `projects_v2_item`).
3. **Subscribe to events**: `pull_request`, `pull_request_review`,
   `pull_request_review_comment`, `pull_request_review_thread`, `issue_comment`,
   `check_run`, `check_suite`, `workflow_run`, `push`, `issues`, `projects_v2_item`.
4. **Webhook** — point the App's Webhook URL at a [smee.io](https://smee.io) channel
   (no inbound port needed) or your own listener, and set a **Webhook secret**
   (`openssl rand -hex 32`). These become `webhook.smee_url` (or `webhook.listen`)
   and `webhook.secret` in the config above.
5. **Private key** — generate one, download the `.pem`, point `app.private_key_path`
   at it, and put the numeric App ID in `app.app_id`.
6. **Install** the App on the repositories (or the whole org) conductor should act on.

> **No App?** conductor runs fully App-less on just your `gh`/PAT credentials plus a
> webhook — see [Running without an App](GitHub-App-Setup#running-without-an-app) and
> the App-less note below. Webhook verification (`webhook.secret` /
> `webhook.verify_signature`) lives under `webhook:`, never `app:`.

## Credentials: app → token → gh

Reads resolve a GitHub App installation token when `app:` is configured, else
the `token:` PAT, else the `gh` CLI's stored login. **App-less operation
works**: events arrive via a plain webhook (+ `webhook.secret`) or the sweep
(explicit repos — glob expansion is an App endpoint), and reads use the PAT /
gh token. Writes are always you (`identity.write_token`: `gh_auth` default or
a literal) unless a verb sets `as: bot` — which requires App credentials.

**`me:` (who "you" are — your PRs, your reviews, your own comments to ignore) is
auto-discovered** and optional. Your write credential *is* you, so at startup
conductor calls `GET /user` on it and takes that login as `me`. Set `me.logins`
only to override — several accounts, or a write credential that isn't the human
you want tracked. If the whoami can't run (no `gh`, offline), it logs a hint and
leaves `me` unset rather than failing.

## Sweep (catch-up polling)

The **sweep** polls GitHub for state your webhooks might have missed — a PR that
went unmergeable, a review request, unresolved threads — and emits the same
events a webhook would, through the same trigger `filter:`. **It is on by
default and every field is optional**: an omitted `sweep:` block still sweeps.

```yaml
sweep:
  enabled: true            # default true — set false to turn polling off
  repos: [your-org/*]      # OPTIONAL: narrow to these; omit → all installed repos
  min_interval: 2m         # default 2m
  interval: 1h             # default 1h
```

- **Scope — omit `repos:` and the sweep covers every repo the App is installed
  on** (it enumerates the App's installations). Set `repos:` (exact names or
  `owner/*` globs) to narrow it. The App installation is already your event
  boundary — webhooks arrive for exactly these repos — so a broad sweep ingests
  nothing new; what conductor *acts* on is still gated by each trigger's
  `filter:`. (App-less/token mode can't enumerate installations, so there list
  `repos:` explicitly.)
- **Cadence depends on whether a webhook is configured**, chosen automatically:
  - **With a webhook** (`smee_url`/`listen`) the sweep is catch-up, so it runs
    on an **adaptive** cadence — tight after startup or a reconnect
    (`min_interval`, 2m), backing off ×2 toward the ceiling (`interval`, 1h)
    while quiet.
  - **Without a webhook** the sweep *is* your event source, so it runs on a
    **fixed** cadence at `min_interval` (2m) — no backoff, so events are never
    left unseen for up to an `interval`. This is what makes a webhook-less
    conductor work out of the box.
- A `sweep` verb (`conductor sweep --now`) triggers an immediate pass.

## Events (`on: gh.<event>`)

All events accept the routing keys `repo:` / `not_repo:` in their `filter:`
(globs; default: the connector's `repos:`) and publish the base context
(`repo`, `owner`, `name`, `pr`, `issue`, `number`, `head`, `base`, `url`,
`kind`, `title`, `labels`). Every match key is also legal as `not_<key>`.
Per-event additions:

| event | extra `filter:` match keys | extra context / options |
|---|---|---|
| `review_requested` | `branch`, `base_branch`, `title`, `label_any`, `label_all`, `require_label`, `author`, `draft` | option `reviewer: {logins, teams}` |
| `changes_requested` | `branch`, `base_branch`, `title`, `label_any`, `label_all`, `author`, `author_bot` | `head_ref`, `author`, `author_is_bot` |
| `new_comment` | `comment_author`, `author_bot` | `author`, `comment_body`, `comment_id`, `comment_kind`, `head_ref` |
| `merge_conflict`, `pr_behind`, `self_review` | *(routing only)* | |
| `failing_checks` | *(routing only)* | `failing_check`, `run_id` (the Actions *workflow run* id, resolved from a `check_run`/`check_suite`; `0` for a non-Actions check); options `ignore_checks`, `flaky_rerun: {enabled, max}` — reruns the failed run once it has finished, before the fixer; a rerun that couldn't be requested isn't counted toward `max` |
| `stuck_checks` | *(routing only)* | `run_id`, `run_name`, `run_status`; options `stuck_after`, `poll_interval` |
| `merge_ready` | `require_label`, `label_any`, `label_all`, `author`, `draft`, `merge_state`, `review_decision`, `non_author_approval`, `threads_resolved` | the last five are opt-OUT gates: all enforced unless the trigger's `filter:` says otherwise |
| `issue_matched` | `sole_assignee`, `label_any`, `label_all`, `require_label`, `title`, `author` | option `assignee: {logins}` |
| `release` | *(routing only)* | `tag_name`, `prerelease`, `draft`; option `include_prereleases` |
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
| `get_run` | `repo`*, `run_id`*, `as` | `run_id`, `name`, `status`, `conclusion`, `head_branch`, `head_sha`, `url` — one workflow run by id (poll it from a `wait_for:` step) |
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
model still loads unchanged; [[Migration]] flattens it into a per-trigger
`filter:` with the same most-specific-repo winner (the losing repos become a
top-level `not_repo:`).

Related: [[Connectors]] · [[GitHub-App-Setup]] · [[Workflows]] · [[Migration]]
