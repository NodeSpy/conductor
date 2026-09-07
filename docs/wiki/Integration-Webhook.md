# Webhook connector

Generic inbound webhooks (any JSON-posting service) plus a generic outbound
HTTP verb. Sources are declared on the connection; each source name is an
event.

```yaml
connectors:
  hooks:
    type: webhook
    listen: ":8099"                  # direct receiver (shared per address)
    # smee_url: ${SMEE_URL}          # and/or a smee channel; `match` routes shared channels
    sources:
      cloudwatch:
        path: /hooks/cloudwatch
        sign: { header: X-Signature-256, secret: ${CW_SECRET}, scheme: sha256 }  # optional HMAC
        match: '{{if eq .body.detail.state "ALARM"}}true{{end}}'  # fire only when it renders "true"
        title: "{{.body.detail.alarmName}}"
        dedup: "{{.body.detail.alarmName}}-{{.body.time}}"        # "" = fire every delivery

triggers:
  - on: hooks.cloudwatch
    repo: acme/infra                 # optional: a real repo enables checkout
    steps:
      - { id: shape, run: js, code: "return { sev: ctx.body.detail.severity }" }
```

Context: `body` (the parsed JSON payload; templates dig with
`{{.body.path.to.field}}`), `kind`, `title`. sign schemes: hex/sha256
(GitHub-style `sha256=`) or base64.

## Outbound verb

| verb | options | outputs |
|---|---|---|
| `post` | `url`*, `method` (default POST), `headers` (map), `body` or `json` (mutually exclusive), `timeout` (default 30s) | `status`, `body` |

Legacy `integrations: - type: webhook` still loads; [[Migration]] moves
sources onto the connection (a source's `repo:` becomes the trigger's).

Related: [[Connectors]] · [[Code-Steps]]
