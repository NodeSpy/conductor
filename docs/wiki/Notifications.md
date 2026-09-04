# Notifications

conductor posts short messages when something needs your attention. Every event is
written to the journal (the audit log) regardless of configuration; `notify.on` selects
which of those events *also* push to whichever external sinks you've configured. Nothing
notify sends is ever posted as a PR/issue comment — it's private, addressed to you alone.

The `notify:` block is unchanged in the connectors model — it covers the
daemon's own lifecycle events (dispatch / complete / escalate / needs_input).
Workflow-level feedback ("this trigger finished") is better expressed as
[[Workflows|hooks]] calling [[Verbs]] — per-trigger, position-scoped, and
addressed wherever the connector posts.

## Config

```yaml
notify:
  push: true                                    # Paseo's own push/attention-flag surface (journal); default true
  on: [escalate, needs_input]                    # events to also push to the sinks below (default [escalate])
  slack_webhook_url: ${SLACK_WEBHOOK_URL}         # Slack incoming webhook (Slack app → Incoming Webhooks)
  discord_webhook_url: ${DISCORD_WEBHOOK_URL}     # Discord channel → Integrations → Webhooks
  ntfy:                                           # publish to an ntfy topic
    server: https://ntfy.sh                       #   defaults to https://ntfy.sh; point at a self-hosted server instead
    topic: my-conductor
  pushover:                                       # push via Pushover
    token: ${PUSHOVER_TOKEN}                      #   application token, from pushover.net
    user: ${PUSHOVER_USER}                        #   your user/group key
  notifiarr:                                      # relay to Discord via a Notifiarr passthrough integration
    api_key: ${NOTIFIARR_API_KEY}
    channel_id: "0000"                             #   optional: Discord channel ID override
  digest: 24h                                     # periodic activity summary; 0 = off
```

Every sink is independently optional — omit a block entirely to skip it. There's no
"choose one"; configure as many as you want and each enabled event posts to all of them.

## Field reference

| Field | Meaning | Where it comes from |
| --- | --- | --- |
| `push` | Whether events also surface through Paseo's own push/attention-flag mechanism (today: the service log). Default `true`. Independent of the external sinks below. | — |
| `on` | List of event kinds that push to the configured sinks (see [Events](#events)). Default `[escalate]`. | — |
| `slack_webhook_url` | A Slack **incoming webhook** URL; posts to whatever channel the webhook is bound to. | Slack app → Features → Incoming Webhooks → Add New Webhook to Workspace |
| `discord_webhook_url` | A Discord channel webhook URL. | Discord channel → Edit Channel → Integrations → Webhooks → New Webhook |
| `ntfy.server` | Base URL of the ntfy server to publish to. Default `https://ntfy.sh`. | Any ntfy instance — the public one, or your own self-hosted server |
| `ntfy.topic` | Topic name events are published under (`<server>/<topic>`). | Chosen by you; subscribe to the same topic in the ntfy app to receive it on your phone |
| `pushover.token` | Application token identifying conductor as a Pushover app. Required alongside `user`. | pushover.net → Create an Application/API Token |
| `pushover.user` | Your Pushover user (or group) key. Required alongside `token`. | pushover.net account page |
| `notifiarr.api_key` | Your Notifiarr API key. | notifiarr.com account settings |
| `notifiarr.channel_id` | Optional Discord channel ID override for the passthrough. | The target Discord channel's ID (Developer Mode → right-click → Copy Channel ID) |
| `digest` | Interval (e.g. `24h`) for an additional periodic activity summary, independent of `on`. `0` disables it. | — |

## Events

`notify.on` selects which of these push to the configured sinks (default `[escalate]`):

| Event | Fires when |
| --- | --- |
| `escalate` | A dispatch failed after exhausting its retries — something needs a look. |
| `needs_input` | A workflow handed a PR to a live agent and is waiting on your review. This is the event that carries a [[Hand-offs\|hand-off]] link (`web`) or notes that a draft posted to a DM/thread (`slack`/`discord`). |
| `complete` | A workflow finished. |
| `dispatch` | Every dispatch, successful or not — noisy; usually left off. |

Everything fires to the **journal** unconditionally, whether or not it's in `on` — the
journal is the full audit trail; `notify.on` only governs what's worth interrupting you
for.

## Behavior

- **Private-only delivery.** Every sink here is addressed to you — a Slack/Discord
  webhook posts to a channel only you (or your ops team) watch, ntfy/Pushover push to
  your own device, Notifiarr relays to your own Discord. conductor never comments on a
  PR or issue to notify anyone; agent replies (via your `gh` token) are the only thing
  that ever posts publicly, and that's a separate mechanism entirely.
- **Sinks are additive, not exclusive.** Configure Slack *and* ntfy *and* Pushover if you
  want; each enabled event fans out to every configured sink independently. A sink with
  no keys set (e.g. no `pushover:` block at all) is simply skipped.
- **`digest`** is orthogonal to `on` — it's a separate periodic summary (dispatch counts,
  outstanding attention items) sent every `digest` interval regardless of which
  individual events are enabled. Set it to `0` to turn it off entirely.
- **Hand-off links ride on `needs_input`.** A `web` [[Hand-offs|hand-off]] channel's
  crypto-token URL is delivered *only* through whichever sinks `needs_input` is routed
  to — it is never printed anywhere else, logged in the journal in the clear, or posted
  publicly. If `needs_input` isn't in `on`, a hand-off still creates the link and waits,
  but you won't be told about it outside the journal — keep `needs_input` enabled if you
  use hand-offs.
- **Best-effort, never a dispatch failure.** A sink erroring (bad webhook URL, network
  blip, rate limit) is logged but never fails the underlying action — notifications are
  a side channel, not part of the pipeline's success/failure path.

## Setup

- **Slack**: create (or reuse) a Slack app, enable **Incoming Webhooks**, add one to the
  workspace, and copy the webhook URL into `slack_webhook_url`. This is unrelated to the
  [[Integration-Slack|`slack` control-plane integration]] — you don't need Socket Mode or
  a bot token just to receive notifications.
- **Discord**: on the target channel, add a webhook under Integrations and copy its URL
  into `discord_webhook_url`. Also unrelated to a `discord:` [[Hand-offs|hand-off]]
  channel, which needs its own bot token and gateway connection.
- **ntfy**: pick a hard-to-guess topic name (anyone who knows `<server>/<topic>` can
  publish to or read it on the public server), then subscribe to that topic in the ntfy
  mobile/desktop app.
- **Pushover**: register an application at pushover.net to get `token`; `user` is your
  account's user key (or a group key to fan out to multiple devices).
- **Notifiarr**: create a passthrough integration in your Notifiarr account for the API
  key; `channel_id` is only needed to override which Discord channel the passthrough
  posts to.

## See also

[[Hand-offs]] for how the `needs_input` event carries a review link or DM/thread draft,
[[Integration-Slack]] for the separate bidirectional Slack control plane, and
[[Configuration]] for where `notify:` sits among the other top-level config sections.
