# Examples

Worked trigger recipes in the connectors schema, roughly in order of depth.
All assume connectors named `gh` (github), `slack-ops` (slack), `timer`
(cron), and `hoff` (web), the `fixer`/`planner` agents, and the defaults from
`config.example.yaml`. Start with [[Quickstart]] if these are your first
triggers.

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

## Release announcement (cross-boundary one-liner)

```yaml
- on: gh.release
  filters: { include_prereleases: false }
  steps:
    - { id: announce, uses: slack-ops.post,
        options: { channel: "#releases", text: "released {{.tag_name}}: {{.url}}" } }
```

## One agent per PR comment burst ([[Grouping]])

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

## Alarm webhook → reshape in js → act

```yaml
- on: hooks.cloudwatch
  steps:
    - { id: shape, run: js,
        code: "return { sev: ctx.body.detail.severity || 'low', name: ctx.body.detail.alarmName }" }
    - { id: page, if: "{{.shape.sev}} == high", uses: slack-ops.post,
        options: { text: "ALARM {{.shape.name}} — {{.url}}" } }
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

## Review triage → draft → human ask → submit ([[Hand-offs]])

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

## Act once per incident id — durable state across runs ([[Configuration]])

```yaml
stores:
  state: { type: boltdb }

triggers:
  - name: new-incidents
    on: [ oncall.incident ]
    steps:
      - { id: gate, uses: kv.get, options: { store: state, namespace: pagerduty, key: last-seen, default: "" } }
      - if: "{{ .pagerduty.id }} != {{ .gate.value }}"
        uses: slack-ops.post
        options: { channel: "#outages", text: "New incident {{ .pagerduty.id }}" }
      - { uses: kv.set, options: { store: state, namespace: pagerduty, key: last-seen, value: "{{ .pagerduty.id }}" } }
```

The value survives restarts (bbolt, fsync on commit). Read it inline
elsewhere with `{{ kv "state" "pagerduty" "last-seen" }}`; SQL stores work
the same way through `sql.query`/`sql.exec` with bound `args:`.

## Nightly remote deploy over SSH ([[Hosts]])

A `type: command` step with `host:` runs on the named SSH target and outputs
`{stdout, stderr, exit_code}` — a non-zero exit fails the step, so the
`done`/`fail` hooks are the report:

```yaml
- on: timer.nightly-tidy
  steps:
    - { id: deploy, type: command, host: build-box, command: [make, -C, /srv/app, deploy] }
  hooks:
    - { at: done, uses: slack-ops.post, options: { text: "deploy ok: {{.deploy.stdout}}" } }
    - { at: fail, uses: slack-ops.post, options: { text: "deploy FAILED: {{.error}}" } }
```

(A `run: sh, host: build-box` code step is the same idea for inline scripts;
its outputs are the script's stdout — a non-zero exit is a step error, not an
`exit_code` output. For exit-code-as-data, use a `type: command` **connector**
whose `run` verb returns it: `uses: build-box.run`.)

## Fan one check across services (for_each)

`for_each:` runs a step once per element with `{{.item}}`/`{{.index}}` in
scope (`parallel: true` fans out, bounded); at runtime the step's collected
outputs land under `{{.<id>.items}}` / `{{.<id>.count}}`. Here against a
`type: command` connector (`build-box`), whose `run` verb returns exit codes
as data:

```yaml
- on: timer.rate-check
  steps:
    - { id: services, run: js, code: "return { value: ['api', 'web', 'worker'] }" }
    - id: probe
      for_each: "{{.services.value}}"
      parallel: true
      uses: build-box.run
      options: { command: "systemctl is-active {{.item}}" }
    - { id: report, uses: slack-ops.post,
        options: { text: "checked {{ len .services.value }} services" } }
```

(Note: on a for_each **verb** step, `validate` today checks later references
against the verb's own output schema, so read the source list — as above —
rather than `{{.probe.count}}`; for_each over a code step has no schema and
either read passes.)

## One live agent per PR — session affinity ([[Agents]])

Every event on a PR — comments, review changes, failing checks — reaches the
same live agent as a follow-up, so it keeps the whole conversation. `group:`
above batches one burst; `session:` extends the one-run-per-key idea across
the PR's life. `end_on: [ gh._closed ]` evicts the session when the PR
closes **or merges** (github reports both as the one internal close signal;
it is eviction-only — you cannot trigger `on:` it):

```yaml
agents:
  pr-agent:
    provider: claude
    memory: true                             # inject repo memories on first spawn
    session:
      key: "{{.repo}}#{{.pr}}"
      idle_ttl: 12h
      end_on: [ gh._closed ]

triggers:
  - on: [ gh.new_comment, gh.changes_requested, gh.failing_checks ]
    steps:
      - type: agent
        agent: pr-agent
        prompt: "New activity ({{.kind}}) on {{.repo}}#{{.pr}} — continue where you left off."
    hooks:
      - { at: done, uses: memory.remember,
          options: { text: "handled {{.kind}} on {{.repo}}#{{.pr}}", scope: repo } }
```

(The `memory.remember` hook needs a top-level `memory:` section —
[[Memory]].)

## Alert on conductor itself ([[Notifications]])

```yaml
- name: act-now
  on: [ conductor.escalate, conductor.failed, conductor.needs_input ]
  steps:
    - { uses: slack-ops.post, options: { text: "conductor {{.message}}" } }
```

## Agent-driven: consult the catalog, run the fit ([[Workflows]] + [[Policy]])

Requires a `policy.agent_authored` block — without one, plans are rejected:

```yaml
- on: gh.issue_matched
  filters: { labels_any: [auto] }
  steps:
    - id: triage
      type: agent
      agent: planner
      checkout: none
      prompt: |
        Goal: handle "{{.title}}". Consult the workflow catalog; if a
        workflow fits, run it and say why. Reply with a ```plan block, e.g.
        - uses: workflow.run
          options: { name: assess-and-post, with: { repo: "{{.repo}}", pr: "{{.pr}}" }, reason: "..." }
```

Related: [[Quickstart]] · [[Workflows]] · [[Connectors]] · [[Code-Steps]] ·
[[Grouping]] · [[Hand-offs]] · [[Configuration]]

## Gate the fix, approve the diff, team up on epics ([[Gates]] + [[Teams]])

The agent-quality layer end to end: a gated fixer whose proposed diff is
approved before it pushes (with a per-profile spend cap and outcome-tuned
guidance), and a planner/workers/critic team for issues labeled `epic`.

```yaml
checks:
  test: { type: command, command: ["make", "test"] }

agents:
  fixer:     { provider: claude, workspace: worktree, outcome_feedback: true,
               budget: { window: 1h, max_cost_usd: 2 } }
  architect: { provider: claude }
  reviewer:  { provider: claude }

triggers:
  - on: gh.failing_checks
    steps:
      - { id: fix, type: agent, agent: fixer, prompt: "Fix the failing checks on {{.repo}}#{{.pr}}.",
          gate: { run: [ test ], max_revisions: 2 } }
      - { id: ok, uses: slack-ops.ask,
          options: { to: dm, user: U0123ABCD, prompt: "Gate passed. Apply?\n{{.fix.diff}}" } }
      - { id: push, if: "{{.ok.action}} == approve", type: command,
          command: ["git", "-C", "{{.fix.workdir}}", "push"] }

  - on: gh.issue_matched
    filters: { labels_any: [epic] }
    steps:
      - id: feature
        prompt: "Implement the feature in {{.url}}."
        team: { planner: architect, worker: fixer, critic: reviewer, max_workers: 4 }
```

Watch it live with `conductor watch`, inspect or retry afterwards with
`conductor runs` ([[Runs]]); merges/reverts feed back per agent
([[Outcomes]], [[Cost-Accounting]]).
