# Slack hand-over

Hand a Slack thread — its text and screenshots — to a new agent workspace
that is **yours**. You use a message shortcut on a message (or @-mention the
app), pick a repo and a mode in a form, and conductor:

1. reads the thread (`slack.thread`) and downloads its files (`slack.download`);
2. launches a **detached** agent step (`detach: true`) in a fresh worktree of
   the chosen repo, with the screenshots attached and the thread quoted in the
   prompt;
3. DMs you the workspace, branch, and agent it started.

From step 2 on, the workspace belongs to you: conductor does not record it,
archive it, message it, or give it conductor credentials, and the agent has
no way to reach conductor. See `detach` in [[Steps]].

The wiring lives in [`config.example.yaml`](https://github.com/NodeSpy/conductor/blob/main/config.example.yaml) (search
for `slack-handover`).

## Slack app setup

The `slack` connector receives events over **Socket Mode**, so it needs no
public URL.

1. Create an app at <https://api.slack.com/apps> → **From an app manifest**,
   and paste the manifest below (change the name and shortcut text as you
   like; keep the `callback_id` in step with your trigger's filter).
2. **Basic Information → App-Level Tokens**: create a token with the
   `connections:write` scope. That is the connector's `app_token` (`xapp-…`).
3. **Install to Workspace**, then copy the **Bot User OAuth Token**
   (`xoxb-…`) — the connector's `bot_token`.
4. Invite the app to the channels you will use it in (`/invite @conductor`).
   It can only read threads in channels it is a member of.
5. Find your Slack member id (profile → ⋯ → **Copy member ID**) and put it
   in the triggers' `users:` filters.

```yaml
display_information:
  name: conductor
features:
  bot_user:
    display_name: conductor
    always_online: false
  shortcuts:
    - name: Hand off to an agent
      type: message
      callback_id: conductor_handover
      description: Start an agent workspace on this thread
oauth_config:
  scopes:
    bot:
      - app_mentions:read
      - chat:write
      - commands
      - channels:history
      - groups:history
      - im:history
      - mpim:history
      - files:read
      - users:read
      - im:write
      # - reactions:write   # only if you use slack.react / react: feedback
settings:
  event_subscriptions:
    bot_events:
      - app_mention
  interactivity:
    is_enabled: true
  socket_mode_enabled: true
  org_deploy_enabled: false
  token_rotation_enabled: false
```

| scope | used for |
| --- | --- |
| `app_mentions:read` | the `app_mention` event |
| `chat:write` | `slack.post`, the ephemeral form button |
| `commands` | message shortcuts (and slash commands) |
| `channels:history`, `groups:history`, `im:history`, `mpim:history` | `slack.thread` / `slack.download` in public, private, DM and group-DM conversations |
| `files:read` | `slack.download` (without it Slack returns its login page, which `download` reports as a skipped file) |
| `users:read` | author names in `slack.thread` |
| `im:write` | `slack.post` with `user:` (opens the DM) |
| `reactions:write` | only for `slack.react` |

Interactivity must be enabled (the manifest does it); with Socket Mode no
request URL is needed. The app token needs `connections:write`.

```yaml
connectors:
  slack-ops:
    use: slack
    app_token: ${SLACK_APP_TOKEN}
    bot_token: ${SLACK_BOT_TOKEN}
```

## Events

| event | fires | filter keys |
| --- | --- | --- |
| `message_shortcut` | a message shortcut was used on a message — after the form is submitted, when the trigger has one | `callback_id`, `channel`, `users` |
| `app_mention` | the app was @-mentioned — with a form: the mentioning user first gets a private, in-thread button that opens it | `channel`, `users` |

Both take two trigger options:

- `form:` — a modal collected before the trigger fires:
  `{ title, submit, fields: [{ name, label, type: select|text|textarea, options, default, optional }] }`.
  `title`/`submit` are at most 24 characters; a `select` has 1–100 literal
  options and its `default` must be one of them. Submitted values publish as
  `{{.slack.form.<name>}}`. A select value is re-checked against the
  configured options when the form is submitted; anything else is refused
  in the form and nothing fires.
- `any_user: true` — see below.

**`users:` is required** on a trigger with a `form:` and on every
`message_shortcut` trigger: a non-empty `users:` filter that every branch of
the filter requires (a `not_users:` or an empty list does not count).
`options.any_user: true` opts out and lets anyone in the workspace use it.
The integration enforces the same rule itself before it opens a form or
fires a submission, and checks that the person submitting is the person the
form was opened for. An `app_mention` trigger without a form is unchanged.

A form trigger's filter is evaluated when the shortcut or mention arrives
(to decide whether to open the form) and again on submission.

### Context

Every slack event publishes these under `.slack`, in addition to `channel`,
`user`, `text`, `ts`, `thread_ts`, `reaction`, `command`:

| key | value |
| --- | --- |
| `form` | the submitted form values by field name (an empty map without a form) |
| `via` | `shortcut` or `mention` |
| `callback_id` | the shortcut's callback id |
| `files` | files on the triggering message: `[{id, name, mimetype}]` |

For a shortcut, `user` is the person who used it, `ts` the message it was
used on, and `thread_ts` that message's thread root (its own ts when it is
not in a thread).

## Verbs

### `thread`

`conversations.replies`, all pages, oldest first.

| option | |
| --- | --- |
| `channel` | required |
| `ts` | required — the thread root (or any message in it) |
| `limit` | max messages, default 200, at most 1000 |

Outputs: `messages` (`[{user, user_name, text, ts, files: [{id, name, mimetype, size}]}]`),
`text` (the thread as plain text, `name (ts):` then the message),
`permalink`, `thread_ts`, `count`, `truncated`. Author names come from
`users.info`, cached for the connector's lifetime.

### `download`

Downloads files from `url_private` with the bot token into
`<state>/slack-files/<channel>-<ts>/` (the directory is reset on each call;
directories older than 7 days are pruned).

| option | default | cap |
| --- | --- | --- |
| `channel`, `ts` | required | |
| `thread` | `true` (every file in the thread); `false`: only the message at `ts` | |
| `max_files` | 10 | 50 |
| `max_file_bytes` | 20 MiB | 50 MiB |
| `max_total_bytes` | 50 MiB | 200 MiB |

Outputs: `dir`, `files` (`[{name, path, mimetype, size}]`), `paths`,
`images` (the `image/*` paths — pass to an agent step's `images:`),
`skipped` (`[{name, reason}]`), `count`.

File names are reduced to `[A-Za-z0-9._-]`, stripped of directory parts and
leading dots, and prefixed with an index; files are written `0600` in a
`0700` directory. The bot token is only sent to `https` `*.slack.com` URLs;
externally hosted files are skipped.

Both verbs are scoped like `post`: the event's own channel needs no grant.

## The example, explained

```yaml
  - name: slack-handover
    abstract: true
    options:
      form:
        title: Hand off to an agent
        fields:
          - { name: repo, type: select, options: [your-org/api, your-org/web] }
          - { name: mode, type: select, options: [plan, default, bypassPermissions], default: plan }
          - { name: notes, type: textarea, optional: true }
    steps:
      - { id: thread, uses: slack-ops.thread, options: { channel: "{{.slack.channel}}", ts: "{{.slack.thread_ts}}" } }
      - { id: files, uses: slack-ops.download, options: { channel: "{{.slack.channel}}", ts: "{{.slack.thread_ts}}" } }
      - id: workspace
        type: agent
        detach: true
        repo: "{{.slack.form.repo}}"
        mode: '{{.slack.form.mode | default "plan"}}'
        images: ["{{.files.images}}"]
        prompt: |
          ... the thread between BEGIN/END markers, labelled as quoted data ...
      - id: tell
        uses: slack-ops.post
        options: { channel: "", user: "{{.slack.user}}", text: "branch: {{.workspace.branch}} ..." }
    hooks:
      - { at: fail, uses: slack-ops.post, options: { channel: "", user: "{{.slack.user}}", text: "Hand-off failed: {{.error}}" } }
  - { on: slack-ops.message_shortcut, extends: slack-handover, filter: { callback_id: conductor_handover, users: [U0123456789] } }
  - { on: slack-ops.app_mention, extends: slack-handover, filter: { users: [U0123456789] } }
```

- **Plan mode by default.** The form's `mode` defaults to `plan`, and the
  step falls back to `plan` if the value is empty. Change either.
- **Private replies only.** `channel: ""` overrides the connector's default
  channel so `post` opens a DM to `user:`; the failure hook does the same.
  Nothing is posted in the channel and no reaction is added.
- **Thread text is data.** The prompt quotes the thread between markers and
  says it is content from Slack, not instructions. Repo and mode come only
  from the form's fixed options.

## Safety notes

- A detached workspace is outside conductor's ownership ledger
  (`owned.json`), so `Dispatcher.Archive` refuses its id; it is never reaped.
  It gets no `CONDUCTOR_ENDPOINT`/skill token, no GitHub token or git
  identity env from conductor, and none of conductor's appended guidance.
  Credentials it needs come from the paseo daemon's own environment.
- `detach:`, `repo:` and `images:` are refused (at load for a pinned
  runtime, else at dispatch) on any runtime but paseo, so a runtime cannot
  silently ignore `detach:` and launch an owned agent instead.
- `detach:` is never accepted on an agent-authored step.
- Launch fields (`repo`, `branch`, `mode`, `images`) are rendered once;
  template syntax inside a submitted value is not evaluated.
