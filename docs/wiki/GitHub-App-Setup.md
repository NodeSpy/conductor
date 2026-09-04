# GitHub App Setup

The `github` integration is driven entirely by a GitHub App, not a personal access
token: the App carries the webhook subscription and all of conductor's own API
reads, while writes are attributed to you (see [[Integration-GitHub]]). This page
covers registering the App — permissions, events, the private key, the webhook
secret — and the two ways a webhook delivery reaches conductor: a smee.io relay or a
direct HTTP listener.

## Register the App

Create a new GitHub App at:

- Personal account: <https://github.com/settings/apps/new>
- Organization: `https://github.com/organizations/<ORG>/settings/apps/new`
  (e.g. `https://github.com/organizations/NodeSpy/settings/apps/new`)

([GitHub's own guide](https://docs.github.com/en/apps/creating-github-apps/registering-a-github-app/registering-a-github-app).)

| Field | Suggested value |
| --- | --- |
| GitHub App name | Must be globally unique — personalize it, e.g. `conductor-<your-handle>` or `<your-org>-conductor`. This name becomes the bot login used in `me:`/`reviewer:`/`assignee:` matching. |
| Homepage URL | Anything valid — the conductor repo or `https://paseo.sh`. |
| Webhook URL | A smee.io channel URL (see [Transports](#transports-smee-andor-direct-http) below) or your own listener's public address. |
| Webhook secret | A random string, e.g. `openssl rand -hex 32`. Store it as `GH_WEBHOOK_SECRET` in `conductor.env`. |

Set **permissions before events** — GitHub only lists webhook events for
permissions you've already granted.

## Permissions

| Scope | Permission | Access |
| --- | --- | --- |
| Repository | Contents | Read & write |
| Repository | Pull requests | Read & write |
| Repository | Issues | Read & write |
| Repository | Checks | Read-only |
| Repository | Metadata | Read-only |
| Organization | Projects | Read-only |

The Organization → Projects permission is required for `projects_v2_item` to
appear as a subscribable event, and is only delivered for Apps installed at the
organization level (not a personal-account install).

## Webhook events

Subscribe to all of:

```
pull_request
pull_request_review
pull_request_review_comment
pull_request_review_thread
issue_comment
check_run
check_suite
workflow_run
push
issues
projects_v2_item
```

`projects_v2_item` only appears in the event list once the Organization → Projects
permission above has been granted.

## Credentials

After granting permissions and events:

1. **Generate a private key** (App settings → "Generate a private key") and save
   the downloaded `.pem` at the path you'll put in `private_key_path`.
2. **Install the App** on the repos/orgs you want conductor to act on.
3. Note the **App ID** (shown at the top of the App's settings page).
4. Put the App id, key path, and webhook secret into config:

```yaml
integrations:
  - type: github
    name: github
    enabled: true
    app:
      app_id: 123456                                    # the App's numeric id
      private_key_path: ~/.config/conductor/github-app.pem  # the generated .pem
      webhook_secret: ${GH_WEBHOOK_SECRET}               # from conductor.env
      verify_signature: false                            # see Transports below
    webhook:
      smee_url: ${GH_SMEE_URL}       # https://smee.io/<channel> — and/or a direct listener:
      # listen: 127.0.0.1:8787
      # path: /webhook
```

| Field | Meaning |
| --- | --- |
| `app.app_id` | The App's numeric id. |
| `app.private_key_path` | Path to the App's generated `.pem` private key, used to mint installation tokens. |
| `app.webhook_secret` | The secret configured on the App's webhook — verifies delivery authenticity when `verify_signature: true`. |
| `app.verify_signature` | Whether to check the `X-Hub-Signature-256` HMAC on each delivery. `false` with smee (see below), `true` behind a direct listener. |
| `webhook.smee_url` | A smee.io channel URL — conductor connects to it itself; no inbound port needed. |
| `webhook.listen` | A direct HTTP listen address (e.g. `127.0.0.1:8787`) for a plain webhook receiver. |
| `webhook.path` | HTTP path for the direct listener. Default `/webhook`. |

An **installation id** is implicit — conductor resolves it from the App's
installations at startup rather than taking it as a separate config field; the
App only needs to be installed on the target repos/orgs.

## Transports: smee and/or direct HTTP

Set `webhook.smee_url`, `webhook.listen`, or both.

### smee.io (no inbound port)

Open <https://smee.io/new>, copy the channel URL it shows (e.g.
`https://smee.io/AbC123`), and use that **same URL** in two places: as the App's
Webhook URL, and as `GH_SMEE_URL` in `conductor.env`.

You do **not** install or run the `smee` client. smee.io is a public relay;
conductor subscribes to your channel itself (auto-reconnecting) and receives the
forwarded deliveries — there is nothing else to start.

Caveat: smee re-serializes the JSON body in transit, so HMAC verification usually
won't match the original payload bytes. Keep `verify_signature: false` when using
smee — the unguessable channel URL itself is the shared secret.

### Direct HTTP

`webhook.listen: 127.0.0.1:8787` (with optional `webhook.path`, default
`/webhook`) runs a plain webhook receiver with no relay in between. Point the
App's Webhook URL at it — typically via your own tunnel (e.g. pangolin) if the
box has no public address of its own.

Because the raw body reaches conductor intact here, set `app.verify_signature:
true` so deliveries are checked against `webhook_secret`.

## Behavior

- Both transports feed the same event pipeline — a delivery from either one is
  normalized into the same set of GitHub trigger kinds.
- The smee connection auto-reconnects with backoff on drop; tuned TCP
  keep-alive surfaces a half-open connection in roughly 80 seconds rather than
  the kernel's ~15-minute default, so an outage is detected quickly.
- smee.io does **not** buffer — any delivery sent while conductor is disconnected
  is lost. This is what the periodic sweep exists to catch; see
  [[Integration-GitHub]] for the sweep/recovery model in full.
- A direct listener has no such gap (subject to your own uptime/tunnel
  reliability), so a sweep is still useful as a general catch-up net but isn't
  compensating for a lossy relay.

## Explanation

Splitting the transport from the App registration means the same App
credentials work whether the box has a public address or not — swap
`webhook.smee_url` for `webhook.listen` (and flip `verify_signature`) without
touching permissions, events, or the private key. Multiple `type: github`
entries can each register a separate App (e.g. one per org), each with its own
transport choice.

See [[Configuration]] for the full config file shape and [[Integration-GitHub]]
for what each subscribed event turns into once it reaches the engine.

## Running without an App

An App is not required. The github connector's credentials resolve
`app:` → `token:` (a PAT) → the `gh` CLI's stored login. App-less:

- Events arrive via a **plain repository/organization webhook** pointed at
  `webhook.listen` (set the same secret in the webhook and in
  `app.webhook_secret` or `webhook.secret`), or by **polling** — enable the
  sweep with explicit repos (`owner/*` glob expansion is an App-only
  endpoint).
- Reads use the PAT / gh token; writes are you, as always.
- `as: bot` verb calls need App credentials and fail with a clear error
  without them.

The App still buys a separate read-rate pool, webhook management on install,
and the bot identity — but a personal setup runs with none of it.
