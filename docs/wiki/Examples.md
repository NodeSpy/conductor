# Examples

Worked trigger recipes in the connectors schema. All assume connectors named
`gh` (github) and `slack-ops` (slack), the `fixer`/`planner` agents, and the
defaults from `config.example.yaml`.

## Autonomous conflict fixer with lifecycle hooks

```yaml
- on: gh.merge_conflict
  steps:
    - { id: fix, type: agent, agent: fixer,
        prompt: "Resolve the conflict on {{.repo}}#{{.pr}} against {{.base}}, verify, push." }
  hooks:
    - { at: start, uses: slack-ops.post, options: { text: "conflict on {{.repo}}#{{.pr}} — on it" } }
    - { at: done,  uses: slack-ops.post, options: { text: "resolved {{.repo}}#{{.pr}}" } }
    - { at: fail,  uses: slack-ops.post, options: { text: "couldn't fix {{.repo}}#{{.pr}}: {{.error}}" } }
```

## One agent per PR comment burst

```yaml
- on: gh.new_comment
  filters: { ignore_users: ["ci-bot"] }
  group: { key: "{{.repo}}#{{.pr}}", window: 15s }
  steps:
    - id: handle
      type: agent
      agent: fixer
      prompt: |
        Address the comments on {{.repo}}#{{.pr}}:
        {{range .group.events}}- {{.comment_body}}
        {{end}}
    - { id: ack, uses: gh.comment,
        options: { repo: "{{.repo}}", number: "{{.pr}}", body: "Addressed {{.group.count}} comment(s)." } }
```

## Review triage → draft → human ask → submit

```yaml
- on: gh.review_requested
  filters: { reviewer: { logins: [your-login] }, exclude: { branches: ["release/*"] } }
  steps:
    - { id: a, workflow: assess-and-post, with: { repo: "{{.repo}}", pr: "{{.pr}}" } }
    - { id: draft, if: "{{.a.decision}} == auto", type: agent, agent: planner,
        checkout: none, prompt: "Draft the review for {{.repo}}#{{.pr}}." }
    - { id: review, if: "{{.a.decision}} == auto", uses: hoff.ask,
        options: { prompt: "Submit this review?", draft: "{{.draft.text}}", timeout: 2h } }
    - { id: submit, if: "{{.review.action}} == approve", uses: gh.submit_review,
        options: { repo: "{{.repo}}", pr: "{{.pr}}", event: COMMENT, body: "{{.review.text}}" } }
```

## Incident: research, then page or log

```yaml
- on: oncall.incident
  filters: { event_types: [incident.triggered] }
  steps:
    - { id: dig, type: agent, agent: planner, checkout: none,
        prompt: "Research {{.pagerduty.title}} ({{.pagerduty.url}}); return severity + summary.",
        output_schema: { type: object, required: [sev, summary],
                         properties: { sev: { enum: [low, high] }, summary: { type: string } } } }
    - { id: page, if: "{{.dig.sev}} == high", uses: slack-ops.post,
        options: { channel: "#outage", text: "{{.dig.summary}}" } }
    - { id: log, if: "{{.dig.sev}} != high", uses: slack-ops.post,
        options: { text: "incident (low): {{.dig.summary}}" } }
```

## Alarm webhook → reshape in js → act

```yaml
- on: hooks.cloudwatch
  steps:
    - { id: shape, run: js,
        code: "return { sev: ctx.body.detail.severity || 'low', name: ctx.body.detail.alarmName }" }
    - { id: page, if: "{{.shape.sev}} == high", uses: slack-ops.post,
        options: { text: "ALARM {{.shape.name}} — {{.url}}" } }
```

## Nightly remote deploy over SSH

```yaml
- on: timer.nightly-tidy
  steps:
    - { id: deploy, run: sh, host: build-box, code: "make -C /srv/app deploy" }
    - { id: tell, uses: slack-ops.post,
        options: { text: "deploy: exit {{.deploy.exit_code}}" } }
```

## Release announcement (cross-boundary one-liner)

```yaml
- on: gh.release
  filters: { include_prereleases: false }
  steps:
    - { id: announce, uses: slack-ops.post,
        options: { channel: "#releases", text: "released {{.tag_name}}: {{.url}}" } }
```

## Fan a check across machines

```yaml
- on: timer.rate-check
  steps:
    - id: probe
      for_each: "{{.hosts_list}}"       # e.g. published by a prior code step
      parallel: true
      run: sh
      code: "df -h / | tail -1"
    - { id: report, uses: slack-ops.post, options: { text: "disk: {{.probe.count}} hosts checked" } }
```

Related: [[Workflows]] · [[Connectors]] · [[Code-Steps]] · [[Grouping]] · [[Hand-offs]]
