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
| `failing_checks` | `ignore_checks` | `failing_check`, `run_id`; options `flaky_rerun: {enabled, max}` |
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
| `rerequest_review` | `repo`*, `pr`*, `reviewers`/`team_reviewers`, `as` | `ok` |
| `submit_review` | `repo`*, `pr`*, `event`* (APPROVE\|REQUEST_CHANGES\|COMMENT), `body`, `as` | `id` |
| `add_labels` | `repo`*, `number`*, `labels`*, `as` | `ok` |

`as: me` (default) posts as you; `as: bot` as the App's bot user.

## Legacy

The legacy `integrations: - type: github` block with its `rules:`/`defaults:`
model still loads unchanged; [[Migration]] flattens it into per-trigger
filters with the same most-specific-repo winner.

Related: [[Connectors]] · [[GitHub-App-Setup]] · [[Workflows]] · [[Migration]]
