# GitHub App Setup

> **The full walkthrough now lives with the plugin, in the conductor-plugins repo:**
>
> ### → [GitHub App Setup](https://github.com/NodeSpy/conductor-plugins/blob/main/docs/connectors/github-app-setup.md)
>
> Registering the App (permissions, webhook events, the private key, the webhook
> secret) and the two delivery transports (a smee.io relay or a direct HTTP
> listener), for the `use: github` connector.

The `github` connector runs as a [[Plugins|plugin]], so its setup and its full
verb/event reference are documented alongside the plugin:

- **[GitHub App Setup](https://github.com/NodeSpy/conductor-plugins/blob/main/docs/connectors/github-app-setup.md)** — register the App and wire up webhooks.
- **[`github` connector reference](https://github.com/NodeSpy/conductor-plugins/blob/main/docs/connectors/github.md)** — connection keys, verbs, and source events.

See the [`github` connector reference](https://github.com/NodeSpy/conductor-plugins/blob/main/docs/connectors/github.md#source-events)
for what each subscribed event becomes, and [[Connectors]] for the built-in
`github` type's events and verbs.

## Running without an App

An App is not required — the connector resolves credentials `app:` → `token:`
(a PAT) → the `gh` CLI's stored login. The full App-less path (events via a plain
repository webhook pointed at `webhook.listen`, reads on the PAT/`gh` token, no
bot identity) is covered under **[Running without an
App](https://github.com/NodeSpy/conductor-plugins/blob/main/docs/connectors/github-app-setup.md#running-without-an-app)**
on the plugins page.
