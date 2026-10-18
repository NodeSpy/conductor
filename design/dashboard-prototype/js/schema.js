/* What plugins declare (the prototype's stand-in for `conductor schema`):
 * connector settings, events with their fields and filters, verbs with typed
 * options and outputs; runtime settings; the builder's built-in step kinds;
 * and a handful of real past events from run history for per-step Test.
 * Field shape: { k, label?, type, req?, options?, help?, def?, ph? }
 * types: string · text (multi-line, templated) · number · bool · enum · list ·
 *        secret (a vault ref) · duration · map · code */
(function () {
  const SCHEMA = (window.SCHEMA = {});
  const f = (k, type, o = {}) => Object.assign({ k, type }, o);

  SCHEMA.connectors = {
    github: {
      label: 'GitHub', icon: 'branch', color: 'var(--color-icon-default)', builtin: true,
      attrs: {
        repo: { label: 'Repo', icon: 'branch', search: true, menu: true, display: true },
        pr: { label: 'PR', icon: 'obj-pr', search: true, menu: true, display: true, from: 'number', format: '{repo}#{number}', link: 'https://github.com/{repo}/pull/{number}', linkLabel: 'Open the pull request on GitHub' },
        branch: { label: 'Branch', icon: 'branch', search: true, from: 'head' },
        author: { label: 'Author', icon: 'members', search: true },
        label: { label: 'Label', icon: 'pin', search: true, from: 'labels' },
        check_name: { label: 'Check', icon: 'obj-check', search: true },
      },
      target: '{repo}#{number}',
      settings: [
        f('app_id', 'number', { req: true, help: 'The GitHub App id. Create one under Settings → Developer settings.' }),
        f('private_key', 'secret', { req: true, help: 'The App private key, as a vault reference.' }),
        f('webhook_secret', 'secret', { req: true, help: 'Shared secret for webhook signatures.' }),
        f('webhook', 'enum', { options: ['smee', 'listen', 'off'], def: 'smee', help: 'How events arrive. smee relays webhooks without exposing a port.' }),
        f('sweep', 'enum', { options: ['adaptive 30s', '60s', '5m', 'off'], def: 'adaptive 30s', help: 'Poll as a backstop for missed webhooks.' }),
        f('repos', 'list', { help: 'Repos this connector watches. Wildcards allowed (acme/*).', ph: 'acme/api' }),
      ],
      events: {
        check_failed: { label: 'A check failed', fields: { repo: 'acme/api', owner: 'acme', name: 'api', number: 482, pr: 482, title: 'billing: refund rounding', url: 'https://github.com/acme/api/pull/482', head: 'fix/refund', base: 'main', head_sha: '9e1c03f', check_name: 'unit', run_id: 88123, author: 'dev-a' }, filters: ['repo', 'branch', 'label_any', 'not_label_any', 'check_name', 'author'] },
        review_requested: { label: 'Review requested', fields: { repo: 'acme/web', owner: 'acme', name: 'web', number: 1291, pr: 1291, title: 'checkout: retry idempotency key', url: 'https://github.com/acme/web/pull/1291', head: 'feat/retry', base: 'main', author: 'dev-b', reviewer: 'conductor-bot' }, filters: ['repo', 'branch', 'not_branch', 'label_any', 'not_label_any', 'author'] },
        pull_request: { label: 'Pull request opened or updated', fields: { repo: 'acme/api', number: 480, pr: 480, title: 'orders: paginate list', url: 'https://github.com/acme/api/pull/480', head: 'feat/paginate', base: 'main', author: 'dev-c', kind: 'opened' }, filters: ['repo', 'branch', 'label_any', 'not_label_any', 'author', 'draft'] },
        issue_opened: { label: 'Issue opened', fields: { repo: 'acme/api', number: 486, title: 'Refund webhook retries forever', url: 'https://github.com/acme/api/issues/486', author: 'user-x', labels: ['bug'], body: 'After a 500 from the processor the webhook retries every minute.' }, filters: ['repo', 'label_any', 'not_label_any', 'author'] },
        new_comment: { label: 'New comment on a PR', fields: { repo: 'acme/web', number: 1288, pr: 1288, comment_body: 'Can we keep the old flag?', comment_author: 'dev-b', url: 'https://github.com/acme/web/pull/1288' }, filters: ['repo', 'not_comment_author', 'author_bot'] },
        merge_conflict: { label: 'Merge conflict', fields: { repo: 'acme/api', pr: 471, number: 471, base: 'main', head: 'fix/flaky', url: 'https://github.com/acme/api/pull/471' }, filters: ['repo', 'branch'] },
        release: { label: 'Release published', fields: { repo: 'acme/api', tag_name: 'v2.14.0', url: 'https://github.com/acme/api/releases/v2.14.0', prerelease: false }, filters: ['repo'] },
      },
      verbs: {
        pr_diff: { label: 'Get a PR diff', read: true, fields: [f('repo', 'string', { req: true, def: '{{.repo}}' }), f('pr', 'number', { req: true, def: '{{.pr}}' })], outputs: { diff: 'diff --git a/orders.go b/orders.go\n+ rows.Next()…', files: 4, additions: 61, deletions: 12 } },
        check_logs: { label: 'Get failing check logs', read: true, fields: [f('repo', 'string', { req: true, def: '{{.repo}}' }), f('run_id', 'number', { def: '{{.run_id}}', help: 'Defaults to the event\'s run.' })], outputs: { text: '--- FAIL: TestRefund (0.02s)\n    refund_test.go:88: got 1999, want 2000', failed_checks: ['unit'], bytes: 421888 } },
        pr_get: { label: 'Get a PR', read: true, fields: [f('repo', 'string', { req: true, def: '{{.repo}}' }), f('pr', 'number', { req: true, def: '{{.pr}}' })], outputs: { state: 'open', merged: false, head_sha: '9e1c03f', review_decision: 'REVIEW_REQUIRED', mergeable: true } },
        comment: { label: 'Comment on an issue or PR', fields: [f('repo', 'string', { req: true, def: '{{.repo}}' }), f('number', 'number', { req: true, def: '{{.number}}' }), f('body', 'text', { req: true, help: 'Markdown. References to earlier steps render at run time.' }), f('as', 'enum', { options: ['me', 'bot'], help: 'Whose identity posts. Defaults to me.' })], outputs: { id: 1844120391, url: 'https://github.com/acme/api/pull/482#issuecomment-1844120391' } },
        review: { label: 'Post a review', fields: [f('repo', 'string', { req: true, def: '{{.repo}}' }), f('pr', 'number', { req: true, def: '{{.pr}}' }), f('event', 'enum', { req: true, options: ['COMMENT', 'APPROVE', 'REQUEST_CHANGES'], def: 'COMMENT' }), f('body', 'text', { req: true })], outputs: { id: 2207718, state: 'COMMENTED' } },
        submit_review: { label: 'Submit a drafted review', fields: [f('repo', 'string', { req: true, def: '{{.repo}}' }), f('pr', 'number', { req: true, def: '{{.pr}}' }), f('event', 'enum', { req: true, options: ['COMMENT', 'APPROVE', 'REQUEST_CHANGES'], def: 'COMMENT' }), f('body', 'text', { req: true })], outputs: { id: 2207719, state: 'COMMENTED' } },
        label: { label: 'Add labels', fields: [f('repo', 'string', { req: true, def: '{{.repo}}' }), f('number', 'number', { req: true, def: '{{.number}}' }), f('labels', 'list', { req: true, ph: 'needs-triage' })], outputs: { labels: ['bug', 'needs-triage'] } },
        create_issue: { label: 'Create an issue', fields: [f('repo', 'string', { req: true }), f('title', 'string', { req: true }), f('body', 'text'), f('labels', 'list')], outputs: { number: 487, url: 'https://github.com/acme/api/issues/487' } },
        merge: { label: 'Merge a PR', fields: [f('repo', 'string', { req: true, def: '{{.repo}}' }), f('pr', 'number', { req: true, def: '{{.pr}}' }), f('method', 'enum', { options: ['squash', 'merge', 'rebase'], def: 'squash' })], outputs: { sha: '4f1a9e0', merged: true } },
        get_run: { label: 'Get a workflow run', read: true, fields: [f('repo', 'string', { req: true, def: '{{.repo}}' }), f('run_id', 'number', { req: true, def: '{{.run_id}}' })], outputs: { status: 'completed', conclusion: 'failure' } },
      },
    },
    slack: {
      label: 'Slack', icon: 'send', color: 'var(--color-icon-default)',
      attrs: { channel: { label: 'Channel', icon: 'message', search: true, menu: true, display: true }, user: { label: 'User', icon: 'members', search: true } },
      target: '{channel}',
      settings: [
        f('bot_token', 'secret', { req: true, help: 'xoxb- token, as a vault reference.' }),
        f('app_token', 'secret', { req: true, help: 'xapp- token for Socket Mode.' }),
        f('mode', 'enum', { options: ['socket', 'events api'], def: 'socket', help: 'Socket Mode needs no inbound port.' }),
        f('default_channel', 'string', { ph: '#ops' }),
      ],
      events: {
        mention: { label: 'The bot is mentioned', fields: { channel: '#platform', user: 'U-OPS', text: '@conductor what failed overnight?', ts: '1760620011.0042' }, filters: ['channel', 'user'] },
        message: { label: 'A message in a channel', fields: { channel: '#incidents', user: 'U-THIRD', text: 'is checkout slow for anyone?', ts: '1760620100.0007' }, filters: ['channel', 'user'] },
      },
      verbs: {
        post: { label: 'Post a message', fields: [f('channel', 'value', { req: true, ph: '#ops', sources: ['typed', 'setting', 'env'], help: 'A channel name or id, typed or from a setting or env var.' }), f('text', 'text', { req: true }), f('thread_ts', 'string', { help: 'Reply in a thread.' })], outputs: { ts: '1760620213.0091', channel: 'C0OPS', permalink: 'https://acme.slack.com/archives/C0OPS/p1760620213009100' } },
        ask: { label: 'Ask a human (hand-off)', ask: true, fields: [f('to', 'enum', { req: true, options: ['dm', 'thread'], def: 'thread' }), f('channel', 'string', { ph: '#reviews', help: 'For to: thread.' }), f('user', 'string', { ph: 'U0123ABCD', help: 'For to: dm.' }), f('prompt', 'text', { req: true }), f('draft', 'text', { help: 'Shown for approve / revise / discard.' }), f('approvers', 'list', { help: 'Only these users may resolve it.' }), f('timeout', 'duration', { def: '1h' })], outputs: { action: 'approve', text: 'LGTM, ship it', ref: 'https://acme.slack.com/archives/C0REV/p1760620300' } },
        react: { label: 'Add a reaction', fields: [f('channel', 'string', { req: true }), f('ts', 'string', { req: true }), f('emoji', 'string', { req: true, def: 'eyes' })], outputs: { ok: true } },
      },
    },
    pagerduty: {
      label: 'PagerDuty', icon: 'warn', color: 'var(--color-icon-default)',
      attrs: { service: { label: 'Service', icon: 'server', search: true, menu: true, display: true, from: 'incident.service' }, urgency: { label: 'Urgency', icon: 'warn', search: true, from: 'incident.urgency' } },
      target: '{service} incident',
      settings: [f('token', 'secret', { req: true }), f('webhook_path', 'string', { def: '/pd', help: 'Mounted on the shared inbound listener.' }), f('services', 'list', { ph: 'PXXXXXX' })],
      events: { incident: { label: 'Incident triggered', fields: { incident: { id: 'Q1W2E3', title: 'api p99 latency > 2s', urgency: 'high', service: 'api' }, title: 'api p99 latency > 2s', url: 'https://acme.pagerduty.com/incidents/Q1W2E3' }, filters: ['service', 'urgency'] } },
      verbs: {
        ack: { label: 'Acknowledge an incident', fields: [f('incident', 'string', { req: true, def: '{{.incident.id}}' })], outputs: { status: 'acknowledged' } },
        note: { label: 'Add a note', fields: [f('incident', 'string', { req: true, def: '{{.incident.id}}' }), f('text', 'text', { req: true })], outputs: { id: 'N8K2' } },
        resolve: { label: 'Resolve an incident', fields: [f('incident', 'string', { req: true, def: '{{.incident.id}}' })], outputs: { status: 'resolved' } },
      },
    },
    kv: {
      label: 'KV store', icon: 'box', color: 'var(--color-icon-default)', builtin: true,
      settings: [f('store', 'string', { def: 'state', help: 'Names a stores: entry.' })],
      events: {},
      verbs: {
        get: { label: 'Read a key', read: true, fields: [f('store', 'string', { req: true, def: 'state' }), f('namespace', 'string', { req: true }), f('key', 'string', { req: true }), f('default', 'string')], outputs: { value: '60s', found: true } },
        set: { label: 'Write a key', fields: [f('store', 'string', { req: true, def: 'state' }), f('namespace', 'string', { req: true }), f('key', 'string', { req: true }), f('value', 'string', { req: true }), f('ttl', 'duration')], outputs: { ok: true } },
      },
    },
    terraform: {
      label: 'Terraform', icon: 'server', color: 'var(--color-icon-default)',
      settings: [f('engine', 'enum', { options: ['tofu', 'terraform'], def: 'tofu' }), f('role', 'enum', { req: true, options: ['READ', 'WRITE'], help: 'WRITE is reachable only through an approved bot proposal.' }), f('allow_destroy', 'list', { help: 'Stateful types it may destroy. Empty refuses all.' })],
      events: {},
      verbs: {
        plan: { label: 'Plan', read: true, fields: [f('repo', 'string', { req: true }), f('dir', 'string', { req: true }), f('workspace', 'string', { def: 'default' })], outputs: { add: 1, change: 2, destroy: 0, plan_sha: 'a4c1e09b…' } },
        apply: { label: 'Apply a signed plan', fields: [f('plan_sha', 'string', { req: true })], outputs: { applied: true } },
      },
    },
    web: {
      label: 'Web hand-off', icon: 'globe', color: 'var(--color-icon-default)', builtin: true,
      settings: [f('listen', 'string', { def: '127.0.0.1:8099', help: 'Loopback by default; draft pages carry approve actions.' }), f('base_url', 'string', { ph: 'https://review.example.com' }), f('ttl', 'duration', { def: '30m' })],
      events: {},
      verbs: { ask: { label: 'Ask a human (hand-off)', ask: true, fields: [f('prompt', 'text', { req: true }), f('draft', 'text'), f('timeout', 'duration', { def: '1h' })], outputs: { action: 'approve', text: 'Looks right.', ref: 'http://127.0.0.1:8099/d/7Hk2' } } },
    },
    git: {
      label: 'git', icon: 'commit', color: 'var(--color-icon-default)', builtin: true, settings: [], events: {},
      verbs: { push: { label: 'Push the step\'s commits', fields: [f('branch', 'string', { help: 'Defaults to conductor/<workflow>-<number>.' }), f('force', 'bool', { help: 'Never allowed to a protected branch.' })], outputs: { sha: '7d02c4e', ref: 'refs/heads/conductor/fix-482', fast_forward: true } } },
    },
    'aws-cost': {
      label: 'AWS Cost Explorer', icon: 'dollar', color: 'var(--color-icon-default)', registry: 'conductor-plugins/aws-cost', ver: 'v0.6.0',
      settings: [f('role_arn', 'string', { req: true, ph: 'arn:aws:iam::000000000000:role/cost-read' }), f('region', 'enum', { options: ['us-east-1', 'us-west-2', 'eu-west-1'], def: 'us-east-1' }), f('granularity', 'enum', { options: ['DAILY', 'MONTHLY'], def: 'DAILY' })],
      events: { anomaly: { label: 'Cost anomaly detected', fields: { service: 'NAT Gateway', impact_usd: 412.5, pct: 44, window: '24h' }, filters: ['service'] } },
      verbs: {
        get_costs: { label: 'Get costs', read: true, fields: [f('start', 'string', { req: true, def: '-7d' }), f('end', 'string', { def: 'today' }), f('group_by', 'enum', { options: ['SERVICE', 'ACCOUNT', 'TAG'], def: 'SERVICE' })], outputs: { total_usd: 1840.22, top: { service: 'EC2', usd: 902.1 } } },
        anomalies: { label: 'List anomalies', read: true, fields: [f('days', 'number', { def: 7 })], outputs: { count: 1, items: [{ service: 'NAT Gateway', impact_usd: 412.5 }] } },
      },
    },
    linear: {
      label: 'Linear', icon: 'runs', color: 'var(--color-icon-default)', registry: 'conductor-plugins/linear', ver: 'v1.1.0',
      settings: [f('api_key', 'secret', { req: true }), f('team', 'string', { ph: 'PLAT' })],
      events: { issue_created: { label: 'Issue created', fields: { id: 'PLAT-112', title: 'Rotate staging certs', url: 'https://linear.app/acme/issue/PLAT-112' }, filters: ['team', 'label_any'] } },
      verbs: { create_issue: { label: 'Create an issue', fields: [f('title', 'string', { req: true }), f('description', 'text'), f('team', 'string')], outputs: { id: 'PLAT-113', url: 'https://linear.app/acme/issue/PLAT-113' } } },
    },
    opsgenie: {
      label: 'Opsgenie', icon: 'warn', color: 'var(--color-icon-default)', registry: 'conductor-plugins/opsgenie', ver: 'v0.8.2',
      settings: [f('api_key', 'secret', { req: true }), f('region', 'enum', { options: ['us', 'eu'], def: 'us' })],
      events: { alert: { label: 'Alert created', fields: { alert: { id: 'og-41', message: 'disk 92% on build-box', priority: 'P2' } }, filters: ['priority'] } },
      verbs: { close: { label: 'Close an alert', fields: [f('alert', 'string', { req: true })], outputs: { ok: true } } },
    },
  };
  // conductor's own sources
  SCHEMA.sources = {
    manual: { label: 'Manual run', icon: 'play', fields: { inputs: {} }, filters: [] },
    timer: { label: 'Schedule', icon: 'clock', fields: { fired_at: '2026-10-16T06:00:00Z' }, filters: [] },
  };

  SCHEMA.runtimes = {
    paseo: { label: 'paseo', settings: [f('bin', 'string', { def: 'paseo' }), f('home', 'string', { ph: '~/paseo-home', help: 'Pins one paseo daemon. Without it conductor walks the daemon ladder.' }), f('host', 'host', { help: 'Run on a hosts: entry over SSH.' }), f('models.default', 'string', { help: 'Omit for a bare launch.' }), f('models.allow', 'list', { help: 'A hard ceiling on what may run.' }), f('default', 'bool', { help: 'The runtime a step uses when it names none.' })] },
    cli: { label: 'cli', settings: [f('tool', 'enum', { req: true, options: ['claude-code', 'codex', 'gemini'], def: 'claude-code' }), f('host', 'host'), f('models.default', 'string'), f('isolation.mode', 'enum', { label: 'isolation', options: [['', 'none: agents run as you (widening)'], ['namespace', 'namespace'], ['container', 'container (needs an image)'], ['user', 'user (a low-privilege account)']], def: 'namespace' })] },
    opencode: { label: 'opencode', settings: [f('host', 'host'), f('models.default', 'string'), f('isolation.mode', 'enum', { label: 'isolation', options: [['', 'none: agents run as you (widening)'], ['namespace', 'namespace'], ['container', 'container (needs an image)'], ['user', 'user (a low-privilege account)']], def: 'namespace' })] },
    'agent-deck': { label: 'agent-deck', settings: [f('wrapper', 'string', { ph: '/usr/local/bin/deck-wrap' }), f('isolation.mode', 'enum', { label: 'isolation', options: [['', 'none: agents run as you (widening)'], ['namespace', 'namespace'], ['container', 'container (needs an image)'], ['user', 'user (a low-privilege account)']], def: 'namespace' })] },
    acp: { label: 'acp', settings: [f('agent', 'string', { req: true, ph: 'gemini' }), f('host', 'host'), f('isolation.mode', 'enum', { label: 'isolation', options: [['', 'none: agents run as you (widening)'], ['namespace', 'namespace'], ['container', 'container (needs an image)'], ['user', 'user (a low-privilege account)']], def: 'namespace' })] },
  };

  SCHEMA.fleets = ['fixer', 'reviewer', 'summarizer', 'light', 'my-fleet'];
  SCHEMA.models = ['claude-opus-5', 'claude-sonnet-5', 'claude-haiku-4-5', 'gpt-5.6-sol', 'gemini-3.1-pro'];
  SCHEMA.engines = [['js', 'JavaScript (QuickJS, sandboxed)'], ['python3', 'Python (host interpreter)'], ['sh', 'Shell (host interpreter)'], ['node', 'Node (host interpreter)'], ['lua', 'Lua (engine plugin)'], ['risor', 'Risor (engine plugin)'], ['go-embed', 'Go (yaegi)'], ['cli', 'Command (argv)']];
  SCHEMA.checks = ['test', 'lint', 'critic', 'review/no-todos'];

  /* Built-in step kinds for the picker; `make` returns the YAML node. */
  SCHEMA.kinds = [
    { k: 'agent', label: 'Agent', icon: 'sparkle', desc: 'An agent with a prompt, a runtime and a model, sandboxed.', make: (id) => ({ id, type: 'agent', prompt: '' }) },
    { k: 'code', label: 'Code', icon: 'code', desc: 'Inline JavaScript, Python, shell… stdout becomes outputs.', make: (id) => ({ id, use: 'js', code: 'return { ok: true };\n' }) },
    { k: 'decide', label: 'Decide', icon: 'obj-question', desc: 'Typed questions answered with probabilities.', make: (id) => ({ id, model: 'light', decide: { state: '', questions: { proceed: { type: 'noul', instructions: '' } } } }) },
    { k: 'ask', label: 'Hand-off / approval', icon: 'hand', desc: 'Ask a human and wait: approve, revise or discard.', make: (id) => ({ id, uses: 'handoff.ask', options: { prompt: '', timeout: '1h' } }) },
    { k: 'wait', label: 'Wait', icon: 'clock', desc: 'sleep: a duration, or wait_for: a condition on a read verb.', make: () => ({ sleep: '30s' }) },
    { k: 'parallel', label: 'Parallel group', icon: 'workflows', desc: 'Branches run at once and join before the next step.', make: (id) => ({ parallel: [[{ id: id + '-a', log: 'branch a' }], [{ id: id + '-b', log: 'branch b' }]] }) },
    { k: 'bot', label: 'Hand to a bot or team', icon: 'members', desc: 'Deliver to a bot, or to a team whose rules (or lead) route it.', make: (id) => ({ id, bot: { team: 'platform', title: '{{.title}}' } }) },
    { k: 'call', label: 'Call a workflow', icon: 'link', desc: 'Run a reusable workflow with inputs; its outputs come back.', make: (id) => ({ id, call: 'assess-and-post', with: {} }) },
    { k: 'log', label: 'Log', icon: 'logs', desc: 'A breadcrumb in the run log.', make: () => ({ log: '' }) },
    { k: 'set', label: 'Set values', icon: 'edit', desc: 'Publish computed values as outputs.', make: (id) => ({ id, set: { key: 'value' } }) },
    { k: 'assert', label: 'Assert', icon: 'check', desc: 'Fail the run unless a condition holds.', make: () => ({ assert: '' }) },
  ];

  /* Real past events, pinned from run history, for Test. */
  SCHEMA.events = [
    { id: 'ev-482', run: 'r-7f3a', on: 'gh.check_failed', at: 6, label: 'acme/api#482 · unit failed', payload: SCHEMA.connectors.github.events.check_failed.fields },
    { id: 'ev-1288', run: 'r-7ef0', on: 'gh.check_failed', at: 64, label: 'acme/web#1288 · test failed', payload: { repo: 'acme/web', owner: 'acme', name: 'web', number: 1288, pr: 1288, title: 'checkout: tax rounding', url: 'https://github.com/acme/web/pull/1288', head: 'fix/tax', base: 'main', head_sha: '0b77ad2', check_name: 'test', run_id: 88017, author: 'dev-b' } },
    { id: 'ev-471', run: 'r-7ea0', on: 'gh.check_failed', at: 230, label: 'acme/api#471 · e2e failed (on hold)', payload: { repo: 'acme/api', owner: 'acme', name: 'api', number: 471, pr: 471, title: 'flaky: retry e2e login', url: 'https://github.com/acme/api/pull/471', head: 'fix/flaky', base: 'main', head_sha: '51aa0de', check_name: 'e2e', run_id: 87702, author: 'dev-a', labels: ['conductor:hold'] } },
    { id: 'ev-1291', run: 'r-7f2c', on: 'gh.review_requested', at: 2, label: 'acme/web#1291 · review requested', payload: SCHEMA.connectors.github.events.review_requested.fields },
    { id: 'ev-480', run: 'r-7f11', on: 'gh.review_requested', at: 38, label: 'acme/api#480 · review requested', payload: { repo: 'acme/api', owner: 'acme', name: 'api', number: 480, pr: 480, title: 'orders: paginate list', url: 'https://github.com/acme/api/pull/480', head: 'feat/paginate', base: 'main', author: 'dev-c', reviewer: 'conductor-bot' } },
    { id: 'ev-486', run: 'r-7ee3', on: 'gh.issue_opened', at: 80, label: 'acme/api#486 · Refund webhook retries forever', payload: SCHEMA.connectors.github.events.issue_opened.fields },
    { id: 'ev-1284', run: 'r-7e80', on: 'gh.issue_opened', at: 300, label: 'acme/web#1284 · Dark mode toggle resets', payload: { repo: 'acme/web', number: 1284, title: 'Dark mode toggle resets on reload', url: 'https://github.com/acme/web/issues/1284', author: 'user-y', labels: [], body: 'Choosing dark mode does not stick.' } },
    { id: 'ev-pd', run: 'r-7eb2', on: 'pd.incident', at: 190, label: 'pd Q1W2E3 · api p99 latency', payload: SCHEMA.connectors.pagerduty.events.incident.fields },
    { id: 'ev-sched', run: 'r-7ec8', on: 'timer.weekly', at: 140, label: 'schedule · Monday 06:00', payload: { fired_at: '2026-10-12T06:00:00Z' } },
    { id: 'ev-manual', run: null, on: 'manual', at: 0, label: 'manual run · no inputs', payload: { inputs: {} } },
  ];

  /** Find a verb declaration from a `uses:` value, through the connector instance. */
  SCHEMA.verb = (uses) => {
    if (!uses || !uses.includes('.')) return null;
    const [inst, verb] = uses.split('.');
    const c = (((window.S && window.S.view) ? window.S.view() : window.D).connectors || []).find((x) => x.name === inst);
    const use = c ? c.use : inst;
    const decl = SCHEMA.connectors[use];
    return decl && decl.verbs[verb] ? Object.assign({ inst, verb, use, conn: decl }, decl.verbs[verb]) : null;
  };
  SCHEMA.event = (on) => {
    if (!on) return null;
    if (on === 'manual') return Object.assign({ inst: 'manual', ev: 'manual' }, SCHEMA.sources.manual);
    if (on.startsWith('timer.') || on.startsWith('schedule')) return Object.assign({ inst: 'timer', ev: on.split('.')[1] || 'schedule', label: 'Schedule' }, SCHEMA.sources.timer);
    const [inst, ev] = on.split('.');
    const c = (((window.S && window.S.view) ? window.S.view() : window.D).connectors || []).find((x) => x.name === inst);
    const decl = c && SCHEMA.connectors[c.use];
    return decl && decl.events[ev] ? Object.assign({ inst, ev, use: c.use, conn: decl }, decl.events[ev]) : null;
  };
})();
