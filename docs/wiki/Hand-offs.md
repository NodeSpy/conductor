# Hand-offs (`ask` verbs)

A hand-off presents work to a human and returns their answer into the
workflow. In the connectors model it is a request-response verb — `uses:
<conn>.ask` — on the ask-capable connector types: `web`, `slack`, `discord`.
The channel machinery (draft pages, tunnels, TTLs, reply capture) is the
implementation of those verbs.

```yaml
steps:
  - { id: draft,  type: agent, agent: critique, checkout: none,
      prompt: "Draft the review for {{.repo}}#{{.pr}}." }
  - { id: review, uses: slack-ops.ask,
      options: { to: dm, user: U0123ABCD, prompt: "Submit this review?", draft: "{{.draft.text}}", timeout: 2h } }
  - { id: submit, if: "{{.review.action}} == approve",
      uses: gh.submit_review, options: { repo: "{{.repo}}", pr: "{{.pr}}", event: COMMENT, body: "{{.review.text}}" } }
```

Outputs of every ask: `{action: approve|revise|discard, text, ref}` — `text`
is the reply (a revision) or the draft on approve; `ref` is where it was
presented. `timeout:` (default 1h) bounds an unanswered ask.

## Channels

- **`web`** — an approve / revise / discard page with an editable draft,
  served on the inbound listener. `base_url:` for a fixed origin, or a
  `tunnel:` provider (`lan`, `cloudflared`, `ngrok`, `tailscale`, `ssh`,
  `localxpose`, `command`) for a fresh public URL per ask. Links carry a
  192-bit token and expire (`ttl:`, default 30m).
- **`slack`** — `to: dm` (a user id) or `to: thread` (a channel); the reply is
  captured over the connector's Socket Mode connection. Replies parse as
  approve (`approve`, `lgtm`, `+1`, …), discard (`discard`, `cancel`, …), or
  anything else = a revision.
- **`discord`** — same shape; conductor runs the bot gateway itself.

## Background review steps

An agent step with `background: true` launches a live agent you drive. Its
`handoff:` names an ask-capable **connector** to present the review loop on
(present → approve/revise/discard → revise re-presents); with none, the
hand-off stays runtime-native — the notification tells you to open the live
agent (paseo's interactive surface). The agent is held from the reaper either
way.

## Legacy `handoffs:`

The legacy named `handoffs:` block still loads and resolves exactly as
before. [[Migration]] converts each entry into a connector of the matching
type (its dm/thread target becomes the connector's default `options:`) and
stamps the default entry's name onto background steps that named none.

Related: [[Verbs]] · [[Connectors]] · [[Runtimes]] · [[Workflows]]
