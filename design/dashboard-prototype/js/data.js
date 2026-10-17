/* Fake data for the prototype. Shapes mirror the API in docs/design/dashboard.md
 * (RunHistory/StepRecord from internal/store/history.go, the bots ledger
 * from #178, browser sessions from #182). Every name here is neutral. */
(function () {
  const now = Date.now();
  const m = (min) => new Date(now - min * 60000).toISOString();

  const D = (window.D = {});

  D.daemon = {
    version: 'v0.71.2', commit: '9c41e0d', uptime: '3d 4h 12m', pid: 48211,
    host: 'ops-box', magicdns: 'ops-box.tail0000.ts.net', tailscale_ip: '100.88.12.40',
    listen: ['127.0.0.1:7788', '100.88.12.40:7788'], tls: 'Tailscale cert · ops-box.tail0000.ts.net · renews in 61d',
    config: '~/.config/conductor/config.yaml', config_sha: '4be1f02a', state_dir: '~/.local/state/conductor',
    paused: false, update: { auto: true, deps: true, interval: '10m', available: 'v0.72.0', last_check: m(4) },
    sandbox: { agents: 'on', backend: 'namespace', platform: 'linux', enforcing: true, egress: ['registry.npmjs.org', 'proxy.golang.org', '*.pypi.org'], containers: 2, identities: 9 },
    paseo: { status: 'ok', version: '0.31.4', agents: 6, home: '~/Projects/paseo-home', entries: 'claude-sandboxed, codex-sandboxed (unchanged)' },
  };

  D.operator = { name: 'ops', passkeys: 2, session: { created: m(190), idle: '4m', device: 'Firefox · laptop', ip: '127.0.0.1' } };

  D.connectors = [
    { name: 'gh', use: 'github', status: 'ok', detail: 'App 123456 · webhook via smee · sweep adaptive 30s', events: 1240, verbs: 312, rl: '4,812 / 5,000' },
    { name: 'slack-ops', use: 'slack', status: 'ok', detail: 'Socket Mode connected', events: 88, verbs: 41 },
    { name: 'pd', use: 'pagerduty', status: 'ok', detail: 'webhook :8787/pd · 2 services', events: 14, verbs: 3 },
    { name: 'kv', use: 'kv', status: 'ok', detail: 'boltdb · 214 keys', events: 0, verbs: 902 },
    { name: 'tf-plan', use: 'terraform', status: 'ok', detail: 'engine tofu 1.8 · READ role', events: 0, verbs: 6 },
    { name: 'tf-apply', use: 'terraform', status: 'idle', detail: 'engine tofu 1.8 · WRITE role · no bot verb reaches it', events: 0, verbs: 1 },
    { name: 'handoff', use: 'web', status: 'wait', detail: 'listen :8099 · 1 draft awaiting answer', events: 3, verbs: 3 },
  ];

  D.runtimes = [
    { name: 'paseo', use: 'paseo', status: 'ok', detail: 'local · 6 agents · sandboxed entries written', isolation: 'sandboxed provider entries' },
    { name: 'claude-cli', use: 'cli', status: 'ok', detail: 'claude 2.4 · namespace sandbox', isolation: 'namespace' },
    { name: 'oc', use: 'opencode', status: 'ok', detail: 'per-dispatch serve · expose relay', isolation: 'namespace' },
    { name: 'deck', use: 'agent-deck', status: 'idle', detail: '--wrapper checked', isolation: 'namespace' },
    { name: 'gpu-box', use: 'cli', status: 'ok', detail: 'host: gpu-box (ssh) · container', isolation: 'container · agents:latest' },
  ];
  D.hosts = [ { name: 'gpu-box', addr: 'gpu-box.tail0000.ts.net', user: 'conductor', status: 'ok', rtt: '18ms' }, { name: 'build-box', addr: '10.0.4.12', user: 'ci', status: 'ok', rtt: '3ms' } ];

  // workflows: steps run in list order; parallel: [[…],[…]] branches join before the next step.
  // `needs` here is the graph the dashboard DERIVES from that order (not a config key).
  D.workflows = [
    { name: 'pr-review', source: 'pack review', on: 'gh.review_requested', runs24: 31, ok: 0.94, cost24: 6.12, steps: [
      { id: 'fetch', kind: 'uses', uses: 'gh.pr_diff', needs: [] },
      { id: 'lint', kind: 'code', run: 'js', needs: ['fetch'] },
      { id: 'review', kind: 'agent', runtime: 'claude-cli', model: 'reviewer', gate: 'review/no-todos', needs: ['fetch'] },
      { id: 'post', kind: 'uses', uses: 'gh.review', needs: ['lint', 'review'] } ] },
    { name: 'ci-fixer', source: 'config', on: 'gh.check_failed', runs24: 9, ok: 0.78, cost24: 9.40, steps: [
      { id: 'logs', kind: 'uses', uses: 'gh.check_logs', needs: [] },
      { id: 'triage', kind: 'decide', needs: ['logs'] },
      { id: 'fix', kind: 'agent', runtime: 'claude-cli', model: 'fixer', needs: ['triage'] },
      { id: 'test', kind: 'code', run: 'command', needs: ['fix'] },
      { id: 'push', kind: 'uses', uses: 'git.push', needs: ['test'] },
      { id: 'comment', kind: 'uses', uses: 'gh.comment', needs: ['push'] } ] },
    { name: 'dep-bump', source: 'config', on: 'schedule 0 6 * * 1', runs24: 1, ok: 1, cost24: 0.82, steps: [] },
    { name: 'issue-triage', source: 'config', on: 'gh.issue_opened', runs24: 12, ok: 1, cost24: 1.40, steps: [] },
    { name: 'release-notes', source: 'pack house-style', on: 'gh.release_drafted', runs24: 0, ok: 1, cost24: 0, steps: [] },
    { name: 'bot:platform-sre', source: 'bot', on: 'pd.incident · manual · sweep', runs24: 7, ok: 0.86, cost24: 3.75, steps: [] },
    { name: 'alert', source: 'config', on: 'conductor.escalate · needs_input · failed', runs24: 4, ok: 1, cost24: 0, steps: [] },
  ];

  const st = (id, status, ms, cost, extra) => Object.assign({ id, status, duration_ms: ms, cost_usd: cost }, extra || {});
  D.runs = [
    { id: 'r-7f3a', workflow: 'ci-fixer', trigger: 'gh.check_failed', repo: 'acme/api', number: 482, status: 'run', started: m(6), cost: 0.71, tokens: 48210, steps: [
      st('logs', 'ok', 2100, 0), st('triage', 'ok', 8400, 0.03), st('fix', 'run', 0, 0.68, { started: m(5) }), st('test', 'idle'), st('push', 'idle'), st('comment', 'idle') ] },
    { id: 'r-7f2c', workflow: 'pr-review', trigger: 'gh.review_requested', repo: 'acme/web', number: 1291, status: 'run', started: m(2), cost: 0.12, steps: [
      st('fetch', 'ok', 900, 0), st('lint', 'run', 0, 0), st('review', 'run', 0, 0.12), st('post', 'idle') ] },
    { id: 'r-7f29', workflow: 'bot:platform-sre', trigger: 'manual (spawn w41)', repo: '', number: 0, status: 'run', started: m(11), cost: 0.33, steps: [ st('work', 'run', 0, 0.33) ] },
    { id: 'r-7f11', workflow: 'pr-review', trigger: 'gh.review_requested', repo: 'acme/api', number: 480, status: 'ok', started: m(38), finished: m(31), cost: 0.41, tokens: 30110, steps: [] },
    { id: 'r-7ef0', workflow: 'ci-fixer', trigger: 'gh.check_failed', repo: 'acme/web', number: 1288, status: 'fail', started: m(64), finished: m(52), cost: 1.12, failed_step: 'test', error: 'go test ./...: 2 failures (TestCheckout, TestRefund)', steps: [] },
    { id: 'r-7ee3', workflow: 'issue-triage', trigger: 'gh.issue_opened', repo: 'acme/api', number: 486, status: 'ok', started: m(80), finished: m(79), cost: 0.06, steps: [] },
    { id: 'r-7ed1', workflow: 'pr-review', trigger: 'gh.review_requested', repo: 'acme/infra', number: 77, status: 'wait', started: m(95), cost: 0.38, detail: 'hand-off: awaiting your answer (approve, revise or discard)', steps: [] },
    { id: 'r-7ec8', workflow: 'dep-bump', trigger: 'schedule', repo: 'acme/web', number: 0, status: 'ok', started: m(140), finished: m(131), cost: 0.82, steps: [] },
    { id: 'r-7eb2', workflow: 'bot:platform-sre', trigger: 'pd.incident', repo: '', number: 0, status: 'ok', started: m(190), finished: m(176), cost: 0.54, steps: [] },
    { id: 'r-7ea0', workflow: 'ci-fixer', trigger: 'gh.check_failed', repo: 'acme/api', number: 471, status: 'idle', started: m(230), finished: m(230), cost: 0, detail: 'parked: conductor:hold label', steps: [] },
    { id: 'r-7e91', workflow: 'pr-review', trigger: 'gh.review_requested', repo: 'acme/web', number: 1285, status: 'ok', started: m(260), finished: m(252), cost: 0.29, steps: [] },
    { id: 'r-7e80', workflow: 'issue-triage', trigger: 'gh.issue_opened', repo: 'acme/web', number: 1284, status: 'ok', started: m(300), finished: m(299), cost: 0.05, steps: [] },
    { id: 'r-7e64', workflow: 'release-notes', trigger: 'gh.release_drafted', repo: 'acme/api', number: 0, status: 'fail', started: m(420), finished: m(418), cost: 0.09, failed_step: 'draft', error: 'budget: max_cost_usd 0.05 exceeded (shed)', steps: [] },
    { id: 'r-7e52', workflow: 'pr-review', trigger: 'gh.review_requested', repo: 'acme/api', number: 468, status: 'ok', started: m(510), finished: m(503), cost: 0.36, steps: [] },
    { id: 'r-7e40', workflow: 'ci-fixer', trigger: 'gh.check_failed', repo: 'acme/web', number: 1279, status: 'ok', started: m(610), finished: m(588), cost: 1.48, steps: [] },
    { id: 'r-7e31', workflow: 'bot:platform-sre', trigger: 'sweep', repo: '', number: 0, status: 'ok', started: m(720), finished: m(716), cost: 0.004, steps: [] },
  ];

  // timeline lanes for the overview "score" (last 12h); [startMinAgo, endMinAgo, status, runId]
  D.score = [
    { lane: 'pr-review', marks: [[700,690,'ok'],[640,634,'ok'],[510,503,'ok','r-7e52'],[400,392,'ok'],[260,252,'ok','r-7e91'],[150,141,'ok'],[95,0,'wait','r-7ed1'],[38,31,'ok','r-7f11'],[2,0,'run','r-7f2c']] },
    { lane: 'ci-fixer', marks: [[610,588,'ok','r-7e40'],[480,470,'ok'],[230,229,'idle','r-7ea0'],[64,52,'fail','r-7ef0'],[6,0,'run','r-7f3a']] },
    { lane: 'issue-triage', marks: [[690,689,'ok'],[560,559,'ok'],[300,299,'ok','r-7e80'],[200,199,'ok'],[80,79,'ok','r-7ee3']] },
    { lane: 'platform-sre', marks: [[720,716,'ok','r-7e31'],[480,476,'ok'],[240,236,'ok'],[190,176,'ok','r-7eb2'],[11,0,'run','r-7f29']] },
    { lane: 'dep-bump', marks: [[140,131,'ok','r-7ec8']] },
    { lane: 'release-notes', marks: [[420,418,'fail','r-7e64']] },
  ];

  D.costDaily = [4.1, 6.8, 5.2, 7.9, 3.3, 2.1, 8.4, 9.7, 6.2, 5.5, 7.1, 11.9, 8.8, 6.4].map((v, i) => ({ day: i, usd: v }));

  D.attention = [
    { kind: 'approval', s: 'ember', title: 'terraform apply · infra@3f2a9c1d', sub: 'platform-sre · p12 · +1 ~2 -0 · REPLACES aws_instance.web', href: '#/bots/platform-sre/proposals/p12', age: '7m' },
    { kind: 'approval', s: 'ember', title: 'slack-ops.post to #incidents', sub: 'billing-sre · p4 · requester chat:U-THIRD (user)', href: '#/bots/billing-sre/proposals', age: '22m' },
    { kind: 'failed', s: 'fail', title: 'ci-fixer failed on acme/web#1288', sub: 'step test · 2 failures', href: '#/runs/r-7ef0', age: '52m' },
    { kind: 'needs_input', s: 'wait', title: 'hand-off awaiting answer · acme/infra#77', sub: 'pr-review · handoff draft (approve / revise / discard)', href: '#/runs/r-7ed1', age: '1h' },
    { kind: 'browser', s: 'wait', title: 'push approval requested by app.example.com', sub: 'agent:triage@acme/app · browser login', href: '#/browser/b-02', age: '3m' },
    { kind: 'update', s: 'run', title: 'v0.72.0 available', sub: 'auto-update applies within 10m · preflight passed', href: '#/updates', age: '4m' },
  ];

  D.activity = [
    { t: m(0.2), s: 'run', text: 'r-7f2c pr-review step <b>review</b> started', who: 'acme/web#1291' },
    { t: m(1), s: 'ok', text: 'platform-sre wrote <b>knowledge/db-prod-1.md</b> (commit 8e1f0aa)', who: 'case c31' },
    { t: m(3), s: 'wait', text: 'browser b-02 awaiting push approval on <b>app.example.com</b>', who: 'agent:triage' },
    { t: m(4), s: 'run', text: 'update available <b>v0.72.0</b> · preflight validate passed on a copy', who: 'conductor' },
    { t: m(6), s: 'run', text: 'r-7f3a ci-fixer started on <b>acme/api#482</b>', who: 'gh.check_failed' },
    { t: m(7), s: 'ember', text: 'platform-sre proposed <b>terraform apply</b> p12 (+1 ~2 -0)', who: 'case c29' },
    { t: m(31), s: 'ok', text: 'r-7f11 pr-review posted review on <b>acme/api#480</b>', who: '$0.41' },
    { t: m(52), s: 'fail', text: 'r-7ef0 ci-fixer failed at <b>test</b>', who: 'acme/web#1288' },
    { t: m(70), s: 'idle', text: 'egress denied: <b>pastebin.com</b> from agent:fixer@acme/web (once per browser)', who: 'sandbox' },
  ];

  D.liveLog = [
    ['logs', 'ok', 'fetched 3 failing checks (unit, lint, e2e) · 412 KiB'],
    ['triage', 'info', 'decide: kind=test_failure p=0.91 · flaky=0.06'],
    ['fix', 'info', 'claude-cli · model claude-sonnet-5 (fleet fixer, rung 1) · sandbox namespace · identity agent:fixer@acme/api'],
    ['fix', 'tool', '▸ Read internal/billing/refund.go'],
    ['fix', 'tool', '▸ Bash go test ./internal/billing/ -run TestRefund'],
    ['fix', 'err', '--- FAIL: TestRefund (0.02s) refund_test.go:88: got 1999, want 2000'],
    ['fix', 'info', 'The rounding in applyFee truncates instead of rounding half-even.'],
    ['fix', 'tool', '▸ Edit internal/billing/refund.go (+4 -2)'],
    ['fix', 'tool', '▸ Bash go test ./internal/billing/...'],
    ['fix', 'ok', 'ok  acme/api/internal/billing 0.412s'],
    ['fix', 'tool', '▸ Bash git commit -m "billing: round refund fees half-even"'],
    ['fix', 'info', 'conductor call git.push → refs/heads/conductor/fix-482 (fast-forward, re-signed)'],
    ['fix', 'warn', 'egress denied: storage.googleapis.com (not in sandbox.egress)'],
    ['fix', 'tool', '▸ Bash go vet ./internal/billing/...'],
    ['fix', 'info', 'All billing tests pass. Writing summary for step.done.'],
  ];

  /* ---------- bots (#178) ---------- */
  D.bots = [
    { name: 'platform-sre', color: 'var(--color-icon-default)', desc: 'SRE for the platform services', home: 'live', session: 's7', turns: 41, maxTurns: 100, age: '9h', maxAge: '24h', shadow: false, sandboxed: true, mode: 'namespace',
      operators: ['ops'], open: 4, pending: 1, workers: 2, spend: 0, proactive: { every: '4h', next: '1h 12m', quiet: '22:00-07:00', budget: '2/6 runs · $0.31/$2.00' }, peers: ['billing-sre'] },
    { name: 'billing-sre', color: 'var(--color-icon-default)', desc: 'Cost and billing operations', home: 'idle', session: 's3', turns: 12, maxTurns: 100, age: '2h', maxAge: '24h', shadow: false, sandboxed: true, mode: 'container',
      operators: ['ops'], open: 2, pending: 1, workers: 0, spend: 1240, proactive: null, peers: ['platform-sre'] },
    { name: 'web-triage', color: 'var(--color-icon-default)', desc: 'Triage for the web app’s issues', home: 'live', session: 's1', turns: 3, maxTurns: 60, age: '40m', maxAge: '12h', shadow: true, sandboxed: true, mode: 'namespace',
      operators: ['ops', 'oncall'], open: 1, pending: 0, workers: 0, spend: 0, proactive: null, peers: [] },
  ];

  D.chat = [
    { who: 'event', at: m(48), case: 'c29', text: 'pd.incident · SEV2 · api p99 latency > 2s for 10m (acme/api, us-east)', fenced: true },
    { who: 'bot', at: m(47), text: 'Opened **c29**. Checking the runbook `runbooks/latency.md`. I’ll start a read-only worker to pull the last hour of p99 by route and the deploy log.' },
    { who: 'tool', at: m(47), text: 'conductor bot spawn --case c29 --group recon --read-only "p99 by route, last 60m"' },
    { who: 'tool', at: m(46), text: 'conductor bot spawn --case c29 --group recon --read-only "deploys + config changes, last 3h"' },
    { who: 'bot', at: m(31), text: 'Both workers reported. The regression lines up with **web** instances on the old AMI after the 14:02 scale-out; routes `/checkout` and `/refund` are the slow ones. The fix is in `infra` commit `3f2a9c1d`: it pins the launch template to the current AMI and replaces `aws_instance.web`.' },
    { who: 'bot', at: m(8), text: 'I proposed **p12**: `terraform apply` of infra@3f2a9c1d, dir envs/prod, workspace prod. Conductor planned it: +1 ~2 -0, and it **replaces aws_instance.web**. Awaiting your approval.' },
    { who: 'op', at: m(5), text: 'What happens to in-flight requests on web while it’s replaced?' },
    { who: 'bot', at: m(4), text: 'The ALB drains the old target for 30s (deregistration_delay in `modules/web/alb.tf`); create_before_destroy is set, so the new instance is healthy before the old one goes. Expect no dropped requests; a brief p99 bump during the swap.' },
  ];

  D.cases = [
    { id: 'c31', title: 'Record db-prod-1 failover drill outcome', status: 'open', requester: 'operator:ops', cls: 'operator', opened: m(30), workers: 0, proposals: 0 },
    { id: 'c29', title: 'SEV2 api p99 latency > 2s', status: 'open', requester: 'event:pd.incident', cls: 'event', opened: m(48), workers: 2, proposals: 1, urgent: true },
    { id: 'c27', title: 'FINDING high · cost jump 44% on nat-gateway', status: 'open', requester: 'proactive (sweep x4)', cls: 'proactive', opened: m(260), workers: 0, proposals: 0 },
    { id: 'c24', title: 'Can you bump the cache TTL for /search?', status: 'open', requester: 'chat:U-THIRD', cls: 'user', opened: m(380), workers: 1, proposals: 1 },
    { id: 'c19', title: 'Peer: billing-sre asks for NAT gateway owners', status: 'closed', requester: 'bot:billing-sre', cls: 'peer', opened: m(1600), workers: 0, proposals: 0 },
    { id: 'c12', title: 'Weekly runbook consolidation', status: 'closed', requester: 'operator:ops', cls: 'operator', opened: m(4000), workers: 1, proposals: 0 },
  ];

  D.proposals = [
    { id: 'p12', bot: 'platform-sre', kind: 'terraform', verb: 'tf-apply.apply', tier: 'propose', status: 'pending', case: 'c29', requester: 'event:pd.incident', cls: 'event', worker: null, at: m(8), expires: '23h 52m',
      summary: 'terraform apply of infra@3f2a9c1d07e4, dir envs/prod, workspace prod: +1 ~2 -0; REPLACES aws_instance.web',
      tf: { repo: 'infra', commit: '3f2a9c1d07e4b1c0a9d2e88f31c0b6a1d4e2f907', dir: 'envs/prod', workspace: 'prod', var_files: ['prod.tfvars'], targets: [], add: 1, change: 2, destroy: 0, replaces: ['aws_instance.web'], plan_sha: 'a4c1e09b7f22d81c5e6a03f9b2d47c18e5f0a6b3c9d2e1f4a7b8c0d3e6f9a2b5' },
      digest: 'sha256:9f2c47e1b0a3d58c6e7f1a2b3c4d5e6f708192a3b4c5d6e7f8091a2b3c4d5e6f7' },
    { id: 'p11', bot: 'platform-sre', kind: 'verb', verb: 'kv.set', tier: 'propose', status: 'pending', case: 'c24', requester: 'chat:U-THIRD', cls: 'user', worker: 'w28', at: m(370), expires: '17h 50m',
      summary: 'kv.set search/cache_ttl = "300s" (was "60s")', options: { key: 'search/cache_ttl', value: '300s' }, digest: 'sha256:1b7e0c9d2a4f6e8b0c1d3e5f7a9b2c4d6e8f0a1b3c5d7e9f1a2b4c6d8e0f2a4b' },
    { id: 'p9', bot: 'platform-sre', kind: 'verb', verb: 'slack-ops.post', tier: 'reversible', status: 'shadow', case: 'c27', requester: 'proactive (sweep x4)', cls: 'proactive', at: m(255), summary: 'post finding to #platform-costs', digest: 'sha256:—' },
    { id: 'p8', bot: 'platform-sre', kind: 'verb', verb: 'gh.comment', tier: 'propose', status: 'executed', approver: 'ops (passkey · laptop)', case: 'c22', at: m(1500), summary: 'comment on acme/api#455 with the RCA', digest: 'sha256:5d9a…' },
    { id: 'p6', bot: 'platform-sre', kind: 'terraform', verb: 'tf-apply.apply', tier: 'propose', status: 'refused', case: 'c18', at: m(3100), summary: 'destroys stateful local_file.config — refused by default (allow_destroy)', digest: 'sha256:—' },
    { id: 'p4', bot: 'billing-sre', kind: 'verb', verb: 'slack-ops.post', tier: 'propose', status: 'pending', case: 'c7', requester: 'chat:U-THIRD', cls: 'user', at: m(22), expires: '23h 38m', summary: 'post the monthly forecast to #incidents', options: { channel: '#incidents' }, digest: 'sha256:c3e5a7b9d1f3a5c7e9b1d3f5a7c9e1b3d5f7a9c1e3b5d7f9a1c3e5b7d9f1a3c5' },
  ];

  D.tfPlan = `Terraform will perform the following actions:

  # aws_instance.web must be replaced
+/- resource "aws_instance" "web" {
      ~ ami                          = "ami-0a1b2c3d4e5f60718" -> "ami-0f9e8d7c6b5a40312" # forces replacement
      ~ id                           = "i-0123456789abcdef0" -> (known after apply)
      ~ private_ip                   = "10.0.12.44" -> (known after apply)
        tags                         = { "Name" = "web", "env" = "prod" }
        # (14 unchanged attributes hidden)
    }

  # aws_launch_template.web will be updated in-place
  ~ resource "aws_launch_template" "web" {
      ~ image_id       = "ami-0a1b2c3d4e5f60718" -> "ami-0f9e8d7c6b5a40312"
      ~ latest_version = 7 -> (known after apply)
    }

  # aws_lb_target_group_attachment.web will be updated in-place
  ~ resource "aws_lb_target_group_attachment" "web" {
      ~ target_id = "i-0123456789abcdef0" -> (known after apply)
    }

Plan: 1 to add, 2 to change, 0 to destroy.`;

  D.workers = [
    { id: 'w41', parent: 'home s7', case: 'c29', group: 'recon', status: 'run', verbs: 'kv.get, gh.get_*', readOnly: true, cls: 'event', brief: 'p99 by route, last 60m', run: 'r-7f29', cost: 0.33, started: m(11) },
    { id: 'w40', parent: 'home s7', case: 'c29', group: 'recon', status: 'ok', verbs: 'kv.get, gh.get_*', readOnly: true, cls: 'event', brief: 'deploys + config changes, last 3h', run: 'r-7f20', cost: 0.21, started: m(46) },
    { id: 'w28', parent: 'home s6', case: 'c24', group: '', status: 'ok', verbs: 'kv.get, kv.set', readOnly: false, cls: 'user', brief: 'evaluate cache TTL change for /search', run: 'r-7e70', cost: 0.18, started: m(372), note: 'kv.set became proposal p11' },
    { id: 'x4', parent: 'sweep', case: '—', group: '', status: 'ok', verbs: 'observe only', readOnly: true, cls: 'proactive', brief: 'Proactive checks (charter §)', run: 'r-7e31', cost: 0.004, started: m(720) },
  ];

  D.memory = {
    tree: [
      { p: 'knowledge/', dir: true }, { p: 'knowledge/README.md' }, { p: 'knowledge/db-prod-1.md', recent: true }, { p: 'knowledge/infra-map.md' }, { p: 'knowledge/alb.md' },
      { p: 'runbooks/', dir: true }, { p: 'runbooks/latency.md' }, { p: 'runbooks/page-triage.md' }, { p: 'runbooks/nat-gateway.md' },
      { p: 'episodes/', dir: true }, { p: 'episodes/2026-10-16-c29.md', recent: true }, { p: 'episodes/proactive/2026-10-16-x4.md' },
      { p: 'handoff/', dir: true }, { p: 'handoff/s6.md' },
    ],
    file: `# db-prod-1

Primary Postgres for the platform services (acme/api, acme/web).

- **Engine:** Postgres 16, Multi-AZ, \`db.r6g.2xlarge\`
- **Failover drill:** 2026-10-16 — promoted standby in 48s; app reconnect
  storm capped by pgbouncer \`max_client_conn\`. No data loss.
- **Watch:** replication lag > 5s pages via pd service *db*.

## Runbook links
- [[runbooks/page-triage]]
- [[runbooks/latency]]
`,
    log: [
      { sha: '8e1f0aa', msg: 'knowledge: db-prod-1 failover drill outcome', author: 'platform-sre', trailers: 'Bot-Case: c31 · Bot-Role: home · Bot-Session: s7', at: m(1) },
      { sha: '41c09d2', msg: 'episodes: c29 recon findings', author: 'platform-sre', trailers: 'Bot-Case: c29 · Bot-Worker: w40', at: m(31) },
      { sha: 'b07e5c1', msg: 'runbooks/latency: add ALB deregistration note', author: 'ops (dashboard)', trailers: 'Bot-Role: operator · Edited-Via: dashboard', at: m(1440) },
      { sha: '2aa91f4', msg: 'handoff: s6 → s7', author: 'platform-sre', trailers: 'Bot-Session: s6', at: m(560) },
    ],
  };

  D.access = {
    verbs: [
      { v: 'gh.get_*', tier: 'observe' }, { v: 'kv.get', tier: 'observe' }, { v: 'slack-ops.post', tier: 'reversible', promoted: true },
      { v: 'kv.set', tier: 'propose' }, { v: 'gh.comment', tier: 'propose' }, { v: 'tf-apply.apply', tier: 'propose' }, { v: 'gh.delete_*', tier: 'never' },
    ],
    credentials: [
      { name: 'grafana', origin: 'https://grafana.example.com', identity: 'bot:platform-sre', fields: 'username, password, otp', vault: 'op', ref: 'Agents/Grafana/*' },
      { name: 'statuspage', origin: 'https://manage.statuspage.example', identity: 'bot:platform-sre', fields: 'passkey', vault: 'house', ref: 'passkeys/statuspage' },
    ],
    logins: [ { site: 'console.example.com', identity: 'bot:platform-sre', seeded: m(4300), cookies: 6 } ],
  };

  /* ---------- browser (#182) ---------- */
  D.browsers = [
    { id: 'b-01', identity: 'bot:platform-sre', agent: 'w41', requester: 'event:pd.incident', loggedIn: false, url: 'https://grafana.example.com/d/api-latency?from=now-1h', title: 'API latency · Grafana', status: 'run', tabs: 2, actions: 37, profile: 'anonymous', note: 'requested by event, not the operator → anonymous profile' },
    { id: 'b-02', identity: 'agent:triage@acme/app', agent: 'r-7f2c', requester: 'operator:ops', loggedIn: true, url: 'https://app.example.com/login/approve', title: 'Approve sign-in · App', status: 'wait', tabs: 1, actions: 12, profile: 'logged-in: app.example.com', note: 'push approval: waiting on you' },
    { id: 'b-03', identity: 'agent:docs@acme/web', agent: 'r-7f11', requester: 'operator:ops', loggedIn: false, url: 'https://docs.example.com/api/refunds', title: 'Refunds API · Docs', status: 'idle', tabs: 1, actions: 5, profile: 'anonymous' },
  ];
  D.browserLog = [
    ['open', 'https://app.example.com/', 'ok'], ['snapshot', '142 nodes', 'ok'], ['click', 'e17 “Sign in”', 'ok'],
    ['login --credential app', 'origin ✓ · frame ✓ · field ✓ · allowlist ✓ · username len 18 · password len 24', 'ok'],
    ['await-approval', 'push approval sent to your phone (needs_input)', 'wait'],
  ];

  /* ---------- secrets & vars ---------- */
  D.vaults = [
    { name: 'house', type: 'conductor', status: 'unlocked', detail: 'vault.json · entries encrypted · unlock: creds:', writable: true, count: 14 },
    { name: 'op', type: 'onepassword', status: 'ok', detail: 'service account · account acme', writable: false, count: null },
    { name: 'store', type: 'pass', status: 'ok', detail: 'GPG · prefix conductor', writable: true, count: 6 },
    { name: 'hcv', type: 'hashicorp', status: 'fail', detail: 'KV v2 · token expired 2h ago', writable: true, count: null },
  ];
  D.secrets = [
    { name: 'GH_WEBHOOK_SECRET', ref: 'house/gh-webhook', vault: 'house', set: m(40000), rotated: m(40000), used: ['connectors.gh.webhook.secret'] },
    { name: 'SLACK_BOT_TOKEN', ref: 'op://Agents/Slack/bot-token', vault: 'op', set: null, used: ['connectors.slack-ops.bot_token'] },
    { name: 'SLACK_APP_TOKEN', ref: 'op://Agents/Slack/app-token', vault: 'op', set: null, used: ['connectors.slack-ops.app_token'] },
    { name: 'review_token', ref: 'house/review', vault: 'house', set: m(9000), used: ['packs.review.secrets.review_token'] },
    { name: 'PD_TOKEN', ref: 'store/conductor/pd', vault: 'store', set: m(20000), used: ['connectors.pd.token'] },
    { name: 'app_password', ref: 'op://Agents/App/password', vault: 'op', set: null, used: ['browser.credentials.app.password'] },
    { name: 'app_otp', ref: 'op://Agents/App/one-time password?attribute=otp', vault: 'op', set: null, used: ['browser.credentials.app.otp'] },
    { name: 'statuspage_passkey', ref: 'house/passkeys/statuspage', vault: 'house', set: m(4300), used: ['browser.credentials.statuspage.passkey'] },
    { name: 'VAULT_TOKEN', ref: 'env:VAULT_TOKEN', vault: 'env', set: null, used: ['vaults.hcv.unlock.token'], warn: 'hcv reports the token expired' },
  ];
  D.settings = [
    { name: 'org', value: 'acme', used: 14 }, { name: 'review_channel', value: '#code-reviews', used: 3 },
    { name: 'deploy_repo', value: '${settings.org}/deploys', used: 2, chains: true }, { name: 'bot_channel', value: '${env.BOT_CHANNEL}', used: 1, env: true },
  ];

  /* ---------- extensions ---------- */
  D.packs = [
    { name: 'review', use: 'conductor-packs/pr-review-team', ver: '1.4.2', sha: 'c81e09a', status: 'ok', official: true, hold: false, triggers: '2 armed / 3', overrides: 4, update: '1.5.0' },
    { name: 'house-style', use: './packs/house-style', ver: 'local', sha: '—', status: 'ok', official: false, hold: false, triggers: '1 armed / 1', overrides: 0 },
    { name: 'sre-bot', use: 'conductor-packs/sre-bot', ver: '0.9.0', sha: '3d70b2e', status: 'ok', official: true, hold: true, triggers: '0 armed / 2', overrides: 1 },
    { name: 'kit', use: 'acme/conductor-packs/kit@^1.2', ver: '1.2.7', sha: '9f01c44', status: 'wait', official: false, hold: false, triggers: 'dormant: no pagerduty connector bound', overrides: 0 },
  ];
  D.plugins = [
    { name: 'github', kind: 'connector', ver: 'v2.0.0', src: 'conductor-plugins/github', status: 'ok', instances: ['gh'], reload: 'in place', update: null },
    { name: 'slack', kind: 'connector', ver: 'v1.6.1', src: 'conductor-plugins/slack', status: 'ok', instances: ['slack-ops'], reload: 'in place', update: 'v1.7.0' },
    { name: 'pagerduty', kind: 'connector', ver: 'v1.2.0', src: 'conductor-plugins/pagerduty', status: 'ok', instances: ['pd'], reload: 'in place', update: null },
    { name: 'terraform', kind: 'connector', ver: 'v0.4.0', src: 'conductor-plugins/terraform', status: 'ok', instances: ['tf-plan', 'tf-apply'], reload: 'restart', update: null },
    { name: 'paseo', kind: 'runtime', ver: 'v0.9.3', src: 'conductor-plugins/paseo', status: 'ok', instances: ['paseo'], reload: 'restart', update: null },
    { name: 'js', kind: 'engine', ver: 'v0.3.1', src: 'conductor-plugins/js', status: 'ok', instances: ['run: js'], reload: 'in place', update: null },
  ];
  D.releases = [
    { v: 'v0.72.0', date: '2026-10-16', notes: ['Bots: proactive sweeps report cost from run history', 'Browser: per-identity evaluate', 'Fix: a paused daemon still rotated bot sessions'], preflight: 'passed', current: false },
    { v: 'v0.71.2', date: '2026-10-12', notes: ['Reclaim a checkout:none run’s workspace when its launch fails'], current: true },
    { v: 'v0.71.1', date: '2026-10-09', notes: ['Remove a run’s directory when its workspace is reclaimed'], current: false },
  ];

  D.audit = [
    { ts: m(1), event: 'memory_remember', who: 'bot:platform-sre', detail: 'knowledge/db-prod-1.md · commit 8e1f0aa · case c31' },
    { ts: m(3), event: 'browser_action', who: 'agent:triage@acme/app', detail: 'await-approval · app.example.com/login/approve · logged_in: app.example.com' },
    { ts: m(3), event: 'browser_fill', who: 'agent:triage@acme/app', detail: 'credential app · origin https://app.example.com · field password · len 24' },
    { ts: m(5), event: 'dashboard_session', who: 'operator:ops', detail: 'passkey assertion · credential laptop · 127.0.0.1' },
    { ts: m(7), event: 'bot_proposal', who: 'bot:platform-sre', detail: 'p12 tf-apply.apply · planned +1 ~2 -0 · plan sha a4c1e09b…' },
    { ts: m(31), event: 'verb', who: 'r-7f11', detail: 'gh.review acme/api#480 · as bot' },
    { ts: m(52), event: 'workflow_failed', who: 'r-7ef0', detail: 'ci-fixer · step test · acme/web#1288' },
    { ts: m(70), event: 'egress_denied', who: 'agent:fixer@acme/web', detail: 'pastebin.com:443' },
    { ts: m(1500), event: 'bot_approval', who: 'operator:ops', detail: 'p8 approved · webauthn · credential laptop · digest 5d9a…' },
    { ts: m(1440), event: 'config_apply', who: 'operator:ops', detail: 'bots.platform-sre.proactive.every 6h → 4h · backup cfg-20261015-1412 · validated on copy' },
  ];

  D.configHistory = [
    { id: 'cfg-20261016-0912', at: m(50), who: 'ops (dashboard)', summary: 'triggers[ci-fixer].policy.budget.max_cost_usd 1.50 → 2.00', status: 'live' },
    { id: 'cfg-20261015-1412', at: m(1440), who: 'ops (dashboard)', summary: 'bots.platform-sre.proactive.every 6h → 4h', status: 'backup' },
    { id: 'cfg-20261014-2205', at: m(2100), who: 'file edit (external)', summary: 'detected on reload: connectors.pd added', status: 'backup' },
    { id: 'cfg-20261013-1730', at: m(3400), who: 'ops (dashboard)', summary: 'packs.review.steps.review-flow/review.model my-fleet', status: 'backup' },
  ];
  /* ===================================================================
   * Config nodes. Everything editable carries the node it is written as in
   * config.yaml, so the pipeline can diff, validate and apply it. Runtime
   * status (health, counts) stays beside it and is never written.
   * =================================================================== */
  const V = (vault, key) => `{{ vault "${vault}" "${key}" }}`;
  const CC = {
    gh: { use: 'github', app_id: 123456, private_key: V('house', 'gh-app-key'), webhook_secret: V('house', 'gh-webhook'), webhook: 'smee', sweep: 'adaptive 30s', repos: ['acme/*'] },
    'slack-ops': { use: 'slack', bot_token: V('op', 'Agents/Slack/bot-token'), app_token: V('op', 'Agents/Slack/app-token'), mode: 'socket', default_channel: '#ops' },
    pd: { use: 'pagerduty', token: V('store', 'conductor/pd'), webhook_path: '/pd', services: ['PAPI01', 'PWEB02'] },
    kv: { use: 'kv', store: 'state' },
    'tf-plan': { use: 'terraform', engine: 'tofu', role: 'READ' },
    'tf-apply': { use: 'terraform', engine: 'tofu', role: 'WRITE' },
    handoff: { use: 'web', listen: '127.0.0.1:8099', ttl: '30m' },
  };
  D.connectors.forEach((c) => (c.cfg = CC[c.name]));
  const RC = {
    paseo: { use: 'paseo', bin: 'paseo', default: true },
    'claude-cli': { use: 'cli', tool: 'claude-code', isolation: { mode: 'namespace' } },
    oc: { use: 'opencode', isolation: { mode: 'namespace' } },
    deck: { use: 'agent-deck', wrapper: '/usr/local/bin/deck-wrap' },
    'gpu-box': { use: 'cli', tool: 'claude-code', host: 'gpu-box', isolation: { mode: 'container' } },
  };
  D.runtimes.forEach((r) => (r.cfg = RC[r.name]));
  D.hosts[0].cfg = { addr: 'gpu-box.tail0000.ts.net', user: 'conductor' };
  D.hosts[1].cfg = { addr: '10.0.4.12', user: 'ci', env: { GOFLAGS: '-mod=mod' } };
  D.sandboxCfg = { agents: 'on', backend: 'namespace', egress: ['registry.npmjs.org', 'proxy.golang.org', '*.pypi.org'] };
  D.updateCfg = { auto: true, deps: true, reload: 'in place', apply: 'auto', interval: '10m' };
  D.operatorsCfg = { ops: { ssh_key: 'ssh-ed25519 AAAAC3Nz…Example', require_device_bound: false }, oncall: { ssh_key: 'ssh-ed25519 AAAAC3Nz…Oncall' } };
  D.vaults.forEach((v) => (v.cfg = { house: { type: 'conductor', unlock: 'creds:house' }, op: { type: 'onepassword', account: 'acme', token: 'env:OP_SERVICE_ACCOUNT_TOKEN' }, store: { type: 'pass', prefix: 'conductor' }, hcv: { type: 'hashicorp', address: 'https://vault.acme.internal:8200', mount: 'kv', token: 'env:VAULT_TOKEN' } }[v.name]));
  D.secrets.push({ name: 'OLD_DEPLOY_KEY', ref: 'house/deploy-old', vault: 'house', set: m(90000), used: [] });
  D.secrets.push({ name: 'LINEAR_KEY', ref: 'store/conductor/linear', vault: 'store', set: m(30000), used: [] });

  const BOT = (name, o) => Object.assign({
    description: '', charter_file: `bots/${name}.md`, escalation: 'Page the operator if a sev1 is not mitigated in 15 minutes.', operators: ['ops'],
    home: { rotate: { max_age: '24h', max_turns: 100 } }, worker: { runtime: 'claude-cli', timeout: '2h', max_concurrent: 4 },
    skill: { verbs: ['kv.get', 'gh.get_*'] }, autonomy: { shadow: false, tiers: { observe: ['gh.get_*', 'kv.get'] }, promote: [], approval_ttl: '24h' },
    peers: [], isolation: { mode: 'namespace' },
  }, o);
  D.bots[0].cfg = BOT('platform-sre', { description: 'SRE for the platform services',
    skill: { verbs: ['gh.get_*', 'kv.get', 'kv.set', 'gh.comment', 'slack-ops.post', 'tf-apply.apply', 'gh.delete_*'] },
    autonomy: { shadow: false, tiers: { observe: ['gh.get_*', 'kv.get'], reversible: ['slack-ops.post'], propose: ['kv.set', 'gh.comment', 'tf-apply.apply'], never: ['gh.delete_*'] }, promote: ['slack-ops.post'], approval_ttl: '24h' },
    proactive: { every: '4h', quiet_hours: '22:00-07:00', budget: { max_runs_per_day: 6, max_cost_per_day: 2.0 }, notify_at: 'high' },
    peers: ['billing-sre'] });
  D.bots[1].cfg = BOT('billing-sre', { description: 'Cost and billing operations', skill: { verbs: ['kv.get', 'slack-ops.post'] },
    autonomy: { shadow: false, tiers: { observe: ['kv.get'], propose: ['slack-ops.post'] }, promote: [], approval_ttl: '24h', spend: { per_action: 2000, per_period: 5000, period: '720h' } },
    peers: ['platform-sre'], isolation: { mode: 'container' } });
  D.bots[2].cfg = BOT('web-triage', { description: 'Triage for the web app’s issues', operators: ['ops', 'oncall'], home: { rotate: { max_age: '12h', max_turns: 60 } },
    skill: { verbs: ['gh.get_*', 'gh.label', 'gh.comment'] }, autonomy: { shadow: true, tiers: { observe: ['gh.get_*'], propose: ['gh.label', 'gh.comment'] }, promote: [], approval_ttl: '24h' } });
  D.charters = {
    'platform-sre': 'You are the platform services\' first-line SRE. Triage pages, investigate with read-only tools, propose fixes, and escalate per the policy below.\n\n## Proactive checks\n- error budgets for api and web\n- NAT and egress cost vs last week\n',
    'billing-sre': 'You watch cost and billing. Forecast monthly spend, flag jumps over 20%, and never move money without an approved proposal.\n',
    'web-triage': 'Label and route new issues on acme/web. Ask for reproduction steps when missing.\n',
  };
  D.identities = [
    { id: 'bot:platform-sre', allow: ['grafana.example.com', 'console.example.com'], logins: ['console.example.com'], evaluate: 'off' },
    { id: 'agent:triage@acme/app', allow: ['app.example.com', 'docs.example.com'], logins: ['app.example.com'], evaluate: 'off' },
    { id: 'agent:docs@acme/web', allow: ['docs.example.com'], logins: [], evaluate: 'read-only' },
  ];
  D.access.credentials.forEach((c) => (c.cfg = { origin: c.origin, identity: c.identity, fields: c.fields.split(', '), vault: c.vault, ref: c.ref }));
  D.access.credentials.push({ name: 'app', origin: 'https://app.example.com', identity: 'agent:triage@acme/app', fields: 'username, password, otp, push', vault: 'op', ref: 'Agents/App/*', cfg: { origin: 'https://app.example.com', identity: 'agent:triage@acme/app', fields: ['username', 'password', 'otp', 'push'], vault: 'op', ref: 'Agents/App/*' } });
  D.packs.forEach((p) => (p.cfg = {
    review: { use: 'conductor-packs/pr-review-team', connectors: { github: 'gh' }, steps: { 'review-flow/review': { model: 'my-fleet' } }, triggers: { on_review_request: { enabled: true, repos: ['acme/*'] } } },
    'house-style': { use: './packs/house-style' },
    'sre-bot': { use: 'conductor-packs/sre-bot@0.9.0', hold: true, steps: { 'sweep/check': { model: 'light' } } },
    kit: { use: 'acme/conductor-packs/kit@^1.2' },
  }[p.name]));

  /* Workflows: the YAML is the source; the list and graph are derived. */
  const WY = {
    'ci-fixer': `name: ci-fixer
on: gh.check_failed
filter: { repo: [acme/api, acme/web], not_label_any: [wip] }   # never touch drafts
policy:
  budget: { max_cost_usd: 2.00 }   # raised from 1.50 on 2026-10-16
steps:
  - id: logs
    uses: gh.check_logs
  - id: triage
    model: light
    decide:
      state: "{{.logs.text}}"
      questions:
        kind: { type: choice, choices: [test_failure, flaky, infra], instructions: What kind of failure is this? }
  - id: fix
    if: "{{.triage.kind}} == test_failure"
    type: agent
    runtime: claude-cli
    model: fixer                   # fleet: sonnet, then opus on retry
    prompt: |
      Fix the failing check on {{.repo}}#{{.number}}.
      {{.logs.text}}
  - id: test
    use: cli
    command: [go, test, ./...]
  - id: push
    uses: git.push
  - id: comment
    uses: gh.comment
    options: { repo: "{{.repo}}", number: "{{.number}}", body: "Fixed in {{.push.sha}}" }
# owner: platform (a trailing comment, kept as written)
`,
    'issue-triage': `name: issue-triage
on: gh.issue_opened
filter: { repo: [acme/api, acme/web] }
steps:
  - id: classify
    type: agent
    model: summarizer
    checkout: none
    prompt: |
      Classify issue {{.repo}}#{{.number}}: "{{.title}}".
      Answer with one label from bug, feature, question.
  - id: label
    uses: gh.label
    options: { repo: "{{.repo}}", number: "{{.number}}", labels: ["{{.classify.text}}"] }
`,
    'dep-bump': `name: dep-bump
on: timer.weekly                   # Mondays 06:00
steps:
  - id: bump
    type: agent
    <<: *fixer
    prompt: Bump direct dependencies in acme/web and keep the build green.
  - id: notify
    uses: slack-ops.post
    options: { channel: "#deps", text: "dep-bump opened {{.bump.text}}" }
`,
    alert: `name: alert
on: [conductor.escalate, conductor.needs_input, conductor.failed]
steps:
  - id: post
    uses: slack-ops.post
    options: { channel: "#ops", text: "conductor: {{.kind}} on {{.workflow}} — {{.url}}" }
`,
    'assess-and-post': `name: assess-and-post
description: "triage a PR: agent decides auto vs manual, pings a channel when manual"
inputs:
  repo: { type: string, required: true }
  pr: { type: integer, required: true }
  channel: { type: string, default: C0123456789 }
outputs:
  decision: "{{.triage.decision}}"
steps:
  - id: triage
    type: agent
    model: summarizer
    checkout: none
    output_schema: { type: object, required: [decision], properties: { decision: { enum: [auto, manual] } } }
    prompt: "Assess {{.inputs.repo}}#{{.inputs.pr}}; decide auto vs manual."
  - id: ping
    if: "{{.triage.decision}} == manual"
    uses: slack-ops.post
    options: { channel: "{{.inputs.channel}}", text: "Needs a human: {{.inputs.repo}}#{{.inputs.pr}}" }
`,
    'pr-review': `# review-flow — shipped by conductor-packs/pr-review-team@1.4.2 (c81e09a)
name: review-flow
on: gh.review_requested
gate: { run: [review/no-todos] }
steps:
  - id: fetch
    uses: gh.pr_diff
  - parallel:
      - - id: lint
          use: js
          code: |
            const todos = (ctx.fetch.diff.match(/TODO/g) || []).length;
            return { todos, ok: todos === 0 };
      - - id: review
          type: agent
          model: reviewer
          prompt: "Review {{.repo}}#{{.pr}}. Diff:\\n{{.fetch.diff}}"
  - id: post
    uses: gh.review
    options: { repo: "{{.repo}}", pr: "{{.pr}}", event: COMMENT, body: "{{.review.text}}" }
`,
    'release-notes': `# shipped by ./packs/house-style
name: release-notes
on: gh.release
steps:
  - id: draft
    type: agent
    model: summarizer
    prompt: Write release notes for {{.repo}} {{.tag_name}}.
  - id: post
    uses: slack-ops.post
    options: { channel: "#releases", text: "{{.draft.text}}" }
`,
  };
  D.workflows.forEach((w) => { w.yaml = WY[w.name] || null; w.kind = 'trigger'; });
  D.workflows.splice(5, 0, { name: 'assess-and-post', source: 'config', kind: 'workflow', on: 'call (reusable)', runs24: 3, ok: 1, cost24: 0.31, steps: [], yaml: WY['assess-and-post'] });
  D.workflows.find((w) => w.name === 'pr-review').pack = 'review';
  D.workflows.find((w) => w.name === 'release-notes').pack = 'house-style';

  /* Chat per bot; memory files; access; preferences; staged changes. */
  D.chats = { 'platform-sre': D.chat, 'billing-sre': [{ who: 'bot', at: m(30), text: 'Forecast for October is **$41.2k**, +6% on September. Proposal **p4** posts it to #incidents; awaiting approval.' }], 'web-triage': [] };
  D.memory.files = {
    'knowledge/README.md': '# Knowledge\n\nWhat platform-sre has learned about the systems it runs.\n',
    'knowledge/db-prod-1.md': D.memory.file,
    'knowledge/infra-map.md': '# Infra map\n\n- **api**: ECS on us-east-1, behind the shared ALB\n- **web**: EC2 ASG, launch template `web`\n',
    'knowledge/alb.md': '# ALB\n\n`deregistration_delay` is 30s on every target group.\n',
    'runbooks/latency.md': '# Latency\n\n1. Check p99 by route.\n2. Compare against the deploy log.\n3. ALB deregistration delay is 30s.\n',
    'runbooks/page-triage.md': '# Page triage\n\nAcknowledge within 5 minutes; open a case per page.\n',
    'runbooks/nat-gateway.md': '# NAT gateway\n\nCost jumps usually mean a new cross-AZ path.\n',
    'episodes/2026-10-16-c29.md': '# c29 · api p99\n\nRegression lines up with web on the old AMI after the 14:02 scale-out.\n',
    'episodes/proactive/2026-10-16-x4.md': '# Sweep x4\n\nNAT gateway cost +44% week over week.\n',
    'handoff/s6.md': '# Hand-off s6 → s7\n\nOpen: c24 (cache TTL), c27 (NAT cost).\n',
  };
  D.memory.open = 'knowledge/db-prod-1.md';
  D.passkeys = [
    { id: 'pk1', name: 'laptop · Touch ID', rp: 'ops-box.tail0000.ts.net', kind: 'platform authenticator', used: m(4), count: 212, bound: true },
    { id: 'pk2', name: 'phone · Face ID', rp: 'ops-box.tail0000.ts.net', kind: 'synced passkey', used: m(2880), count: 31, bound: false },
    { id: 'pk3', name: 'security key', rp: 'ops-box.tail0000.ts.net', kind: 'roaming · FIDO2', used: m(27360), count: 4, bound: true },
  ];
  D.sessions = [
    { id: 's-this', device: 'Firefox · laptop', addr: '127.0.0.1', started: m(190), idle: '4m', current: true },
    { id: 's-phone', device: 'Safari · phone', addr: '100.101.7.3 (tailnet)', started: m(2880), idle: '6h' },
  ];
  D.prefs = { notifyApprovals: true, notifyPush: true, notifyFailed: false };
  D.optouts = [{ id: 'oo1', where: 'trigger gh.review_requested (hand-off)', owner: 'your config', detail: 'runs as you through paseo\'s plain provider · titled [UNSANDBOXED]', path: 'triggers[on=gh.review_requested].isolation' }];
  D.containers = [{ identity: 'bot:billing-sre', image: 'agents:latest', volume: 'home-billing-sre', size: '1.4 GB', up: '2h' }, { identity: 'runtime:gpu-box', image: 'agents:latest', volume: 'home-gpu-box', size: '6.1 GB', up: '3d' }];
  D.pending = [];
  D.audit.forEach((a) => (a.run = (a.who.match(/^r-/) ? a.who : '')));

  /* Teams (draft #178 follow-up). A bot is in at most one team. Team
   * defaults are a ceiling bots inherit and can only narrow; bots in a
   * team are peers by default; teams are isolated unless linked. */
  D.teams = [
    { name: 'platform', cfg: { description: 'Platform services: api, web, infra', lead: 'platform-sre',
      defaults: { operators: ['ops', 'oncall'], autonomy: { max_tier: 'reversible', promote: true, approval_ttl: '24h' }, isolation: { mode: 'namespace' },
        budget: { max_cost_per_day: 20.0, proactive_pool: { max_runs_per_day: 10, max_cost_per_day: 4.0 } },
        browser: { logins: ['console.example.com'], credentials: ['grafana', 'statuspage'] } },
      knowledge: { repo: 'team-knowledge/platform', promote: 'approval' },
      routing: [
        { name: 'pd-api', when: { on: 'pd.incident', service: ['api', 'web'] }, to: 'lead' },
        { name: 'web-issues', when: { on: 'gh.issue_opened', repo: ['acme/web'] }, to: 'web-triage' },
      ],
      links: ['finance'] } },
    { name: 'finance', cfg: { description: 'Cost, billing and forecasts',
      defaults: { operators: ['ops'], autonomy: { max_tier: 'propose', promote: false, approval_ttl: '12h' }, isolation: { mode: 'container' }, budget: { max_cost_per_day: 10.0 } },
      routing: [{ name: 'cost-anomaly', when: { on: 'aws-cost.anomaly' }, to: 'billing-sre' }],
      links: ['platform'] } },
  ];
  D.bots[0].cfg.team = 'platform'; D.bots[2].cfg.team = 'platform'; D.bots[1].cfg.team = 'finance';
  // inside a team peers are implicit, so the explicit cross-team link is the only one written
  D.bots.push({ name: 'release-scribe', color: 'var(--color-icon-default)', desc: 'Drafts release notes; no team', home: 'idle', session: 's1', turns: 0, maxTurns: 40, age: '1h', maxAge: '12h', shadow: true, sandboxed: true, mode: 'namespace', operators: ['ops'], open: 0, pending: 0, workers: 0, spend: 0, proactive: null, peers: [],
    cfg: { description: 'Drafts release notes; no team', charter_file: 'bots/release-scribe.md', operators: ['ops'], home: { rotate: { max_age: '12h', max_turns: 40 } }, skill: { verbs: ['gh.get_*', 'slack-ops.post'] }, autonomy: { shadow: true, tiers: { observe: ['gh.get_*'], propose: ['slack-ops.post'] }, promote: [], approval_ttl: '24h' }, peers: [], isolation: { mode: 'namespace' } } });
  D.charters['release-scribe'] = 'Draft release notes from merged PRs. Never post without approval.\n';
  D.chats['release-scribe'] = [];
  D.cases.forEach((c) => (c.bot = 'platform-sre'));
  const R = (o) => Object.assign({ requester_kept: true }, o);
  D.cases.find((c) => c.id === 'c29').routed = R({ by: 'rule', rule: 'pd-api', team: 'platform', via: 'lead', lead: 'platform-sre', to: 'platform-sre', note: 'the lead kept it' });
  D.cases.find((c) => c.id === 'c24').routed = R({ by: 'direct', to: 'platform-sre' });
  D.cases.find((c) => c.id === 'c31').routed = R({ by: 'direct', to: 'platform-sre' });
  D.cases.find((c) => c.id === 'c27').routed = R({ by: 'proactive', to: 'platform-sre' });
  D.cases.find((c) => c.id === 'c19').routed = R({ by: 'link', team: 'finance', to: 'platform-sre', note: 'peer ask over the platform↔finance link' });
  D.cases.find((c) => c.id === 'c12').routed = R({ by: 'direct', to: 'platform-sre' });
  D.cases.push(
    { id: 'c33', bot: 'web-triage', title: 'Checkout button misaligned on Safari', status: 'open', requester: 'event:gh.issue_opened', cls: 'event', opened: m(70), workers: 0, proposals: 0, routed: R({ by: 'rule', rule: 'web-issues', team: 'platform', to: 'web-triage' }) },
    { id: 'c34', bot: 'web-triage', title: 'pd: web 5xx elevated on /cart', status: 'open', requester: 'event:pd.incident', cls: 'event', opened: m(18), workers: 1, proposals: 0, urgent: true, routed: R({ by: 'rule', rule: 'pd-api', team: 'platform', via: 'lead', lead: 'platform-sre', to: 'web-triage', note: 'lead assigned: web owns /cart' }) },
    { id: 'c7', bot: 'billing-sre', title: 'Monthly forecast for October', status: 'open', requester: 'chat:U-THIRD', cls: 'user', opened: m(40), workers: 0, proposals: 1, routed: R({ by: 'direct', to: 'billing-sre' }) },
  );
  D.teamKnowledge = {
    platform: { repo: 'team-knowledge/platform', files: { 'README.md': '# Platform team knowledge\n\nShared by every bot of team platform. Bots propose; the operator approves.\n', 'alb.md': '# ALB\n\nShared target groups; check deregistration delay before blaming the app.\n', 'oncall.md': '# On-call\n\nops is primary, oncall secondary, 22:00-07:00 quiet hours for sweeps.\n' },
      proposals: [
        { id: 'k4', bot: 'platform-sre', from: 'knowledge/alb.md', to: 'alb.md', summary: 'ALB deregistration delay is 30s on every target group', status: 'pending', at: m(25), case: 'c29', diff: ' # ALB\n \n Shared target groups; check deregistration delay before blaming the app.\n+\n+`deregistration_delay` is **30s** on every target group (modules/web/alb.tf).\n' },
        { id: 'k3', bot: 'web-triage', from: 'knowledge/safari.md', to: 'browsers.md', summary: 'Safari 18 flex gap bug: use margin fallback in checkout', status: 'pending', at: m(55), case: 'c33', diff: '+# Browsers\n+\n+Safari 18 mis-sizes flex `gap` inside buttons; checkout uses a margin fallback.\n' },
        { id: 'k2', bot: 'platform-sre', from: 'runbooks/page-triage.md', to: 'oncall.md', summary: 'Ack within 5 minutes; one case per page', status: 'approved', at: m(3000), approver: 'ops (passkey · laptop)', diff: '' },
      ] },
    finance: null,
  };

  /* A fourth platform bot, so the team has someone to delegate data work to. */
  D.bots.splice(3, 0, { name: 'platform-data', color: 'var(--color-icon-default)', desc: 'Data pipelines and the warehouse', home: 'idle', session: 's2', turns: 9, maxTurns: 80, age: '3h', maxAge: '24h', shadow: false, sandboxed: true, mode: 'namespace', operators: ['ops'], open: 0, pending: 0, workers: 0, spend: 0, proactive: null, peers: [],
    cfg: { description: 'Data pipelines and the warehouse', team: 'platform', charter_file: 'bots/platform-data.md', home: { rotate: { max_age: '24h', max_turns: 80 } }, skill: { verbs: ['kv.get', 'gh.get_*'] }, autonomy: { shadow: false, tiers: { observe: ['kv.get', 'gh.get_*'] }, promote: [], approval_ttl: '24h' }, isolation: { mode: 'namespace' } } });
  D.charters['platform-data'] = 'Own the warehouse and its pipelines. Answer data questions from teammates with queries, never with guesses.\n';
  D.chats['platform-data'] = [];

  /* Delegation (`conductor bot delegate`): a bot hands a teammate a task.
   * The recipient works it as a child case under the ORIGINAL requester; the
   * result reports back to the parent case. Across teams only where linked. */
  const DR = (o) => Object.assign({ requester_kept: true }, o);
  D.delegations = [
    { id: 'd1', from: 'platform-sre', to: 'platform-data', parent: 'c29', child: 'c35', task: 'Pull p99 by route from the warehouse for the last 3h', requester: 'event:pd.incident', cls: 'event', status: 'done', result: 'p99 regression starts 14:03, only on /checkout and /refund', at: m(44), done: m(36) },
    { id: 'd2', from: 'platform-sre', to: 'web-triage', parent: 'c29', child: 'c36', task: 'Check whether the web canary shows the same regression', requester: 'event:pd.incident', cls: 'event', status: 'running', at: m(20) },
    { id: 'd3', from: 'billing-sre', to: 'platform-sre', parent: 'c7', child: 'c37', task: 'List the NAT gateway owners for the forecast', requester: 'chat:U-THIRD', cls: 'user', status: 'done', result: '3 gateways (api, web, data); owners in knowledge/infra-map.md', at: m(39), done: m(33) },
  ];
  D.cases.push(
    { id: 'c35', bot: 'platform-data', title: 'Pull p99 by route from the warehouse for the last 3h', status: 'closed', requester: 'event:pd.incident', cls: 'event', opened: m(44), workers: 1, proposals: 0, routed: DR({ by: 'delegate', from: 'platform-sre', to: 'platform-data', parent: 'c29', delegation: 'd1', team: 'platform' }), notes: [{ at: m(36), who: 'bot:platform-data', text: 'p99 regression starts 14:03, only on /checkout and /refund' }] },
    { id: 'c36', bot: 'web-triage', title: 'Check whether the web canary shows the same regression', status: 'open', requester: 'event:pd.incident', cls: 'event', opened: m(20), workers: 1, proposals: 0, routed: DR({ by: 'delegate', from: 'platform-sre', to: 'web-triage', parent: 'c29', delegation: 'd2', team: 'platform' }), notes: [] },
    { id: 'c37', bot: 'platform-sre', title: 'List the NAT gateway owners for the forecast', status: 'closed', requester: 'chat:U-THIRD', cls: 'user', opened: m(39), workers: 0, proposals: 0, routed: DR({ by: 'delegate', from: 'billing-sre', to: 'platform-sre', parent: 'c7', delegation: 'd3', team: 'finance', cross: true }), notes: [{ at: m(33), who: 'bot:platform-sre', text: '3 gateways (api, web, data); owners in knowledge/infra-map.md' }] },
  );
  D.cases.find((c) => c.id === 'c29').notes = [{ at: m(36), who: 'bot:platform-data', text: '↩ result of d1: p99 regression starts 14:03, only on /checkout and /refund' }];
  D.cases.find((c) => c.id === 'c7').notes = [{ at: m(33), who: 'bot:platform-sre', text: '↩ result of d3: 3 gateways (api, web, data); owners in knowledge/infra-map.md' }];
  // data-only asks between peers, for the team's activity graph
  D.asks = [
    { from: 'web-triage', to: 'platform-sre', at: m(65), text: 'Is /cart behind the same ALB as /checkout?' },
    { from: 'platform-data', to: 'platform-sre', at: m(40), text: 'Which time zone is the incident window in?' },
    { from: 'platform-sre', to: 'platform-data', at: m(30), text: 'Can the warehouse split p99 by AMI?' },
    { from: 'billing-sre', to: 'platform-sre', at: m(1600), text: 'Who owns the NAT gateways?' },
  ];

  /* A trigger that hands work to a team: the team's rules (or its lead) pick the bot. */
  D.workflows.splice(6, 0, { name: 'incident-intake', source: 'config', kind: 'trigger', on: 'pd.incident', runs24: 2, ok: 1, cost24: 0, steps: [], yaml: `name: incident-intake
on: pd.incident
filter: { urgency: high }
steps:
  - id: page
    bot:                           # routing rules pick the bot; no match → the lead triages
      team: platform
      title: "{{.title}}"
      text: "{{.incident.service}} · {{.url}}"
` });

  /* Folders: an optional `folder: "a/b"` on any bot or team. Organization only
   * (display, filter, search): no permissions, defaults or behavior. A bot on
   * a team shows under its team; the team sits in a folder. */
  D.teams.find((t) => t.name === 'platform').cfg.folder = 'work/platform';
  D.teams.find((t) => t.name === 'finance').cfg.folder = 'work/billing';
  D.bots.find((b) => b.name === 'release-scribe').cfg.folder = 'work/platform';
  D.bots.push({ name: 'homelab', color: 'var(--color-icon-default)', desc: 'Watches the home lab: backups, certificates, disks', home: 'live', session: 's4', turns: 18, maxTurns: 60, age: '5h', maxAge: '24h', shadow: false, sandboxed: true, mode: 'namespace', operators: ['ops'], open: 1, pending: 0, workers: 0, spend: 0, proactive: { every: '6h', next: '2h 40m', quiet: '23:00-08:00', budget: '1/3 runs' }, peers: [],
    cfg: { description: 'Watches the home lab: backups, certificates, disks', folder: 'personal/home', charter_file: 'bots/homelab.md', operators: ['ops'], skill: { verbs: ['kv.get'] }, autonomy: { shadow: false, tiers: { observe: ['kv.get'] }, promote: [], approval_ttl: '24h' }, proactive: { every: '6h', quiet_hours: '23:00-08:00', budget: { max_runs_per_day: 3, max_cost_per_day: 0.5 } }, isolation: { mode: 'namespace' } } });
  D.charters.homelab = 'Keep an eye on the home lab. Report missed backups and expiring certificates; never change anything.\n';
  D.chats.homelab = [];
  D.cases.push({ id: 'c38', bot: 'homelab', title: 'Nightly backup missed on nas-01', status: 'open', requester: 'proactive (sweep x2)', cls: 'proactive', opened: m(140), workers: 0, proposals: 0, routed: { by: 'proactive', to: 'homelab', requester_kept: true }, notes: [] });
  D.folderDrafts = [];

  /* models: fleets — ranked acceptable models a step names instead of a model id */
  D.fleets = [
    { name: 'fixer', cfg: { any: ['claude-sonnet-5', 'claude-opus-*'] } },
    { name: 'reviewer', cfg: { any: ['claude-opus-*', 'gpt-5.6-*', 'gemini-3.*-pro'] } },
    { name: 'summarizer', cfg: { any: ['claude-haiku-*', '*-mini'] } },
    { name: 'light', cfg: { any: ['claude-haiku-4-5'] } },
    { name: 'my-fleet', cfg: { any: ['claude-opus-5'], required: true } },
  ];

  /* usage: how model consumption is shown (conductor's usage.mode). Off by
   * default: model accounts sit behind a proxy or load balancer, so anything
   * conductor showed unasked would be partial. When enabled, figures are per
   * provider (Claude, Codex), never totalled; plan limits and tokens come only
   * from a configured usage.source. Cloud spend (commit_money, aws-cost) is
   * separate and always shown. */
  D.usageCfg = {};
  const series = (a) => a.map((k) => k * 1000);
  D.usage = {
    providers: { claude: { label: 'Claude', vendor: 'Anthropic' }, codex: { label: 'Codex', vendor: 'OpenAI' } },
    // what conductor itself observes today: runs it dispatched and agent turns, by the provider each ran on
    observed: { claude: { runs: 12, turns: 83 }, codex: { runs: 3, turns: 6 } },
    bots: { 'platform-sre': { claude: { runs: 6, turns: 36 }, codex: { runs: 1, turns: 5 } }, 'billing-sre': { claude: { runs: 2, turns: 12 } }, 'web-triage': { claude: { runs: 2, turns: 8 }, codex: { runs: 1, turns: 1 } }, 'platform-data': { claude: { runs: 1, turns: 9 } }, 'release-scribe': {}, homelab: { codex: { runs: 1, turns: 0 }, claude: { runs: 1, turns: 18 } } },
    // what a configured usage source (the proxy / load balancer in front of the accounts) reports, per provider, in its own units
    reported: {
      claude: { windows: [{ span: '5h', pct: 62, resets: '1h 48m' }, { span: 'weekly', pct: 31, resets: '3d 4h' }], today: 1240000, daily: series([290, 480, 365, 553, 231, 147, 588, 679, 434, 385, 497, 833, 616, 448]), models: { 'claude-opus-5': 812000, 'claude-sonnet-5': 391000, 'claude-haiku-4-5': 37000 } },
      codex: { windows: [{ span: '5h', pct: 18, resets: '3h 10m' }, { span: 'weekly', pct: 9, resets: '5d 1h' }], today: 210000, daily: series([40, 0, 22, 61, 15, 0, 9, 88, 31, 12, 70, 54, 19, 21]), models: { 'gpt-5-codex': 210000 } },
    },
    // api mode: dollars per provider (from runtime reports, or the source when configured)
    usd: { claude: { today: 5.92, daily: [3.6, 6.1, 4.4, 7.0, 2.9, 1.8, 7.5, 8.6, 5.4, 4.9, 6.3, 10.7, 7.8, 5.9] }, codex: { today: 0.48, daily: [0.5, 0.7, 0.8, 0.9, 0.4, 0.3, 0.9, 1.1, 0.8, 0.6, 0.8, 1.2, 1.0, 0.5] } },
  };
  D.bots[0].proactive.budget = '2/6 runs today';
  D.activity.forEach((a) => { if (a.who === '$0.41') a.who = 'acme/api#480'; });
  /* a commit-money proposal (cloud spend, not model spend) with two-person approval — shown with planned features */
  D.proposals.push({ id: 'p13', bot: 'billing-sre', kind: 'verb', verb: 'aws-cost.purchase_savings_plan', tier: 'commit_money', status: 'pending', case: 'c7', requester: 'proactive (sweep x3)', cls: 'proactive', at: m(90), expires: '22h 30m',
    summary: 'Buy a 1-year Compute Savings Plan at $1,200/month (covers 71% of steady EC2)', options: { term: '1y', commitment_usd_month: 1200, payment: 'no_upfront' }, digest: 'sha256:7a11c0de4f9b2e6d8c3a5f1b0e9d7c6a4b2f8e1d3c5a7b9f0e2d4c6a8b1f3e5d', planned: true,
    approvals: [{ who: 'alex', method: 'passkey · phone', at: m(40) }], needs: 2 });
  D.instances = [
    { id: 'personal', name: 'Daniel (personal)', kind: 'personal', host: 'ops-box', need: 6, status: 'ok', signedIn: true, role: 'owner' },
    { id: 'work-platform', name: 'work-platform', kind: 'org', host: 'conductor-platform', need: 3, status: 'ok', signedIn: false, role: 'approver · team platform' },
    { id: 'home-lab', name: 'home-lab', kind: 'org', host: 'lab-nuc', need: 0, status: 'wait', signedIn: false, role: 'operator' },
  ];
  D.people = [
    { name: 'daniel', roles: [['owner', '']], passkeys: ['laptop · Touch ID', 'phone · Face ID'], active: m(4) },
    { name: 'alex', roles: [['approver', 'team platform'], ['operator', 'folder work']], passkeys: ['phone · Face ID'], active: m(40) },
    { name: 'sam', roles: [['viewer', '']], passkeys: ['security key'], active: m(3000) },
    { name: 'ci-bot', roles: [['operator', 'bot release-scribe']], passkeys: [], active: m(600), service: true },
  ];
  D.twoPerson = { observe: false, reversible: false, propose: false, commit_money: true, never: false };

  /* Logins (#182): a credential binding (password in conductor's vault, a
   * vault reference, or a passkey) or a saved session from "sign in yourself".
   * Values never leave the vault; the dashboard sees labels and references. */
  D.opItems = [
    { vault: 'Agents', title: 'Grafana', website: 'https://grafana.example.com', fields: [['username', 'text'], ['password', 'concealed'], ['one-time password', 'otp']] },
    { vault: 'Agents', title: 'Status page', website: 'https://manage.statuspage.example', fields: [['email', 'email'], ['password', 'concealed']] },
    { vault: 'Agents', title: 'App', website: 'https://app.example.com', fields: [['username', 'text'], ['password', 'concealed'], ['one-time password', 'otp']] },
    { vault: 'Ops', title: 'Cloud console', website: 'https://console.example.com', fields: [['login', 'text'], ['password', 'concealed'], ['recovery codes', 'concealed']] },
    { vault: 'Personal', title: 'Home NAS', website: 'https://nas.home.example', fields: [['username', 'text'], ['password', 'concealed']] },
  ];
  D.access.credentials.forEach((c) => { c.cfg.method = c.cfg.fields.includes('passkey') ? 'passkey' : 'vault'; c.cfg.operator_only = true; });
  D.loginMeta = {
    grafana: { lastUsed: m(44), test: { result: 'ok', at: m(300) }, uses: [{ bot: 'platform-sre', case: 'c29', at: m(44) }, { bot: 'platform-sre', case: 'c27', at: m(250) }] },
    statuspage: { lastUsed: m(4300), test: { result: 'ok', at: m(4300) }, uses: [{ bot: 'platform-sre', case: 'c12', at: m(4300) }] },
    app: { lastUsed: m(3), test: { result: 'needs you', at: m(3) }, uses: [{ bot: 'triage', case: '—', at: m(3) }] },
  };
  D.access.logins.forEach((l) => { l.expires = '23d'; });

})();

/* Typed knowledge (kinds, not free paths). Every entry is markdown with front
 * matter; its kind decides its home and its fields: knowledge/, runbooks/,
 * watch.md, log/ (bots only), handoff/ (system), and custom collections
 * declared in config (collections:) or shipped by a pack. */
(function () {
  const D = window.D;
  const fm = (o, body) => '---\n' + YAML.stringify(o).replace(/\n?$/, '\n') + '---\n' + (body || '');
  D.memory.files = {
    'knowledge/db-prod-1.md': fm({ kind: 'knowledge', title: 'db-prod-1', statement: 'Primary Postgres for the platform services (acme/api, acme/web): Postgres 16, Multi-AZ, db.r6g.2xlarge.', applies_to: ['db-prod-1', 'postgres', 'acme/api'], source: 'RDS console; failover drill 2026-10-16 (case c31)', confidence: 'high' },
      '- **Failover drill:** 2026-10-16 — promoted standby in 48s; app reconnect\n  storm capped by pgbouncer `max_client_conn`. No data loss.\n\nSee [[runbooks/page-triage]] and [[runbooks/latency]].\n'),
    'knowledge/infra-map.md': fm({ kind: 'knowledge', title: 'Infra map', statement: 'api runs on ECS in us-east-1 behind the shared ALB; web is an EC2 ASG on launch template web.', applies_to: ['acme/api', 'acme/web', 'nat'], source: 'terraform state, modules/web and modules/api', confidence: 'high' }, '- NAT gateways: api, web, data (owners: platform)\n'),
    'knowledge/alb.md': fm({ kind: 'knowledge', title: 'ALB', statement: 'deregistration_delay is 30s on every target group.', applies_to: ['alb', 'acme/web'], source: 'modules/web/alb.tf', confidence: 'high' }, ''),
    'runbooks/latency.md': fm({ kind: 'runbook', title: 'Latency', when: 'api p99 above 800ms for 5 minutes, or a latency page from pd service api', prerequisites: ['read access to the ALB and the deploy log'], steps: [{ do: 'Check p99 by route', verb: 'kv.get' }, { do: 'Compare against the deploy log', verb: 'gh.get_deployments' }, { do: 'Check the ALB deregistration delay (30s, see [[knowledge/alb]])' }], verification: 'p99 back under 500ms for 15 minutes', rollback: 'Nothing to roll back: read-only checks', owner: 'platform-sre', applies_to: ['acme/api', 'alb'] }, ''),
    'runbooks/page-triage.md': fm({ kind: 'runbook', title: 'Page triage', when: 'Any page from pd', steps: [{ do: 'Acknowledge within 5 minutes', verb: 'pd.ack' }, { do: 'Open one case per page' }], verification: 'The incident shows acknowledged and a case is open', owner: 'platform-sre' }, ''),
    'runbooks/nat-gateway.md': fm({ kind: 'runbook', title: 'NAT gateway cost', when: 'NAT spend up more than 30% week over week', steps: [{ do: 'List flows by AZ pair', run: 'aws ec2 describe-flow-logs --filter Name=resource-id,Values=nat-*' }, { do: 'Look for a new cross-AZ path (see [[knowledge/infra-map]])' }], verification: 'Daily NAT spend back within 10% of last week', rollback: 'Revert the route change that added the path', owner: 'platform-sre', applies_to: ['nat'] }, ''),
    'watch.md': '# Watch\n\n## Replication lag on db-prod-1\n- signal: aws.rds ReplicaLag (db-prod-1)\n- threshold: > 5s for 2m\n- why: The failover drill showed reconnect storms when lag is high\n\n## NAT gateway cost\n- signal: aws-cost daily NAT spend\n- threshold: +30% week over week\n- why: A new cross-AZ path doubles transfer cost\n',
    'log/2026-10-16-c29.md': fm({ kind: 'log', title: 'c29 · api p99', case: 'c29', at: '2026-10-16' }, 'Regression lines up with web on the old AMI after the 14:02 scale-out. Followed [[runbooks/latency]]; ALB facts from [[knowledge/alb]].\n'),
    'log/2026-10-16-x4.md': fm({ kind: 'log', title: 'Sweep x4', case: 'x4', at: '2026-10-16' }, 'NAT gateway cost +44% week over week. Followed [[runbooks/nat-gateway]].\n'),
    'handoff/s6.md': fm({ kind: 'handoff', title: 'Hand-off s6 → s7', session: 's6' }, 'Open: c24 (cache TTL), c27 (NAT cost).\n'),
  };
  D.memory.open = 'knowledge/db-prod-1.md';
  const tk = D.teamKnowledge.platform;
  tk.files = {
    'knowledge/alb.md': fm({ kind: 'knowledge', title: 'ALB', statement: 'Shared target groups: check the deregistration delay before blaming the app.', applies_to: ['alb'], source: 'platform on-call notes', confidence: 'medium' }, ''),
    'knowledge/oncall.md': fm({ kind: 'knowledge', title: 'On-call', statement: 'ops is primary, oncall secondary; sweeps keep 22:00-07:00 quiet hours.', applies_to: ['team:platform'], source: 'team charter', confidence: 'high' }, ''),
    'runbooks/failover.md': fm({ kind: 'runbook', title: 'Failover', when: 'The db-prod-1 primary is unhealthy for 2 minutes', prerequisites: ['an approved commit_money-free change window'], steps: [{ do: 'Check replication lag', verb: 'kv.get' }, { do: 'Promote the standby', run: 'aws rds failover-db-cluster --db-cluster-identifier db-prod-1' }, { do: 'Watch the app reconnect' }], verification: 'Writes succeed on the new primary; p99 normal', rollback: 'Fail back after the old primary recovers', owner: 'platform' }, ''),
    'watch.md': '# Watch\n\n## Error budget for api\n- signal: slo api availability (30d)\n- threshold: < 99.9%\n- why: Freeze deploys before the budget runs out\n',
  };
  tk.proposals = [
    { id: 'k4', bot: 'platform-sre', kind: 'knowledge', to: 'knowledge/alb.md', summary: 'ALB deregistration delay is 30s on every target group', status: 'pending', at: new Date(Date.now() - 25 * 60000).toISOString(), case: 'c29',
      content: fm({ kind: 'knowledge', title: 'ALB', statement: 'Shared target groups: deregistration_delay is 30s on every target group; check it before blaming the app.', applies_to: ['alb', 'acme/web'], source: 'modules/web/alb.tf (case c29)', confidence: 'high' }, '') },
    { id: 'k3', bot: 'web-triage', kind: 'knowledge', to: 'knowledge/safari-18-flex-gap.md', summary: 'Safari 18 flex gap bug: use margin fallback in checkout', status: 'pending', at: new Date(Date.now() - 55 * 60000).toISOString(), case: 'c33',
      content: fm({ kind: 'knowledge', title: 'Safari 18 flex gap', statement: 'Safari 18 mis-sizes flex gap inside buttons; checkout uses a margin fallback.', applies_to: ['acme/web', 'safari'], source: 'case c33 repro', confidence: 'medium' }, '') },
    { id: 'k5', bot: 'web-triage', kind: 'runbook', to: 'runbooks/checkout-errors.md', summary: 'Runbook for checkout 5xx bursts', status: 'pending', at: new Date(Date.now() - 70 * 60000).toISOString(), case: 'c33',
      content: fm({ kind: 'runbook', title: 'Checkout errors', when: 'checkout 5xx above 2% for 5 minutes', steps: [{ do: 'Check the canary' }, { run: 'kubectl rollout undo deploy/web' }, { do: 'Tell #incidents', verb: 'slack-ops.post', run: 'echo hi' }] }, '') },
    { id: 'k2', bot: 'platform-sre', kind: 'runbook', to: 'runbooks/page-triage.md', summary: 'Ack within 5 minutes; one case per page', status: 'approved', at: new Date(Date.now() - 3000 * 60000).toISOString(), approver: 'ops (passkey · laptop)', content: '' },
  ];
  D.botMemory = {
    'web-triage': { 'knowledge/checkout-margin.md': fm({ kind: 'knowledge', title: 'Checkout margin fallback', statement: 'Checkout buttons use a margin fallback instead of flex gap, for Safari 18.', applies_to: ['acme/web', 'safari'], source: 'case c33', confidence: 'high' }, '') },
    'billing-sre': { 'knowledge/nat-owners.md': fm({ kind: 'knowledge', title: 'NAT owners', statement: 'Three NAT gateways (api, web, data), all owned by platform.', applies_to: ['nat'], source: 'delegation d3 from platform-sre', confidence: 'medium' }, '') },
  };
  // collections: the operator's own (config, collections:) and those packs ship
  D.collections = [];
  D.packCollections = [{ name: 'postmortems', pack: 'sre-bot', cfg: { dir: 'postmortems', scope: 'team', fields: ['date:string', 'severity:enum(sev1|sev2|sev3)', 'impact:text'], template: '## Timeline\n\n## Root cause\n\n## Actions\n', free_paths: false, write: ['operator', 'bots (proposal)'] } }];
})();

/* Environment variables, by scope. Daemon scope is conductor.env (loaded by
 * the daemon at startup); the rest live on the runtime, bot/team, workflow,
 * plugin or host that exports them. Values of sensitive-looking plain vars
 * are never shown, only their length. */
(function () {
  window.D.env = [
    { scope: 'daemon', name: 'APP_USER', value: 'svc-app', plain: true },
    { scope: 'daemon', name: 'BOT_CHANNEL', value: '#bots', plain: true },
    { scope: 'daemon', name: 'GH_HOST', value: 'github.com', plain: true },
    { scope: 'daemon', name: 'APP_PASSWORD', value: '', len: 24, plain: true, looksSecret: true },
    { scope: 'daemon', name: 'VAULT_TOKEN', value: '{{ vault "op" "Agents/HCV/token" }}', plain: false },
    { scope: 'runtime:paseo', name: 'AWS_PROFILE', value: 'ops', plain: true },
    { scope: 'host:build-box', name: 'TZ', value: 'UTC', plain: true },
    { scope: 'workflow:ci-fixer', name: 'NODE_OPTIONS', value: '--max-old-space-size=4096', plain: true },
  ];
})();

/* Folders are one shared organization: the same folder: path places bots,
 * teams and workflows. Your workflows carry folder: in config; pack workflows
 * sit under packs/<instance>/ unless you place them (packs.<i>.folders); a
 * bot's synthesized workflow follows its bot or team. */
(function () {
  const D = window.D;
  const put = (name, folder) => { const w = D.workflows.find((x) => x.name === name); if (w && w.yaml && !/^folder:/m.test(w.yaml)) w.yaml = w.yaml.replace(/^(name: .*\n)/m, `$1folder: "${folder}"\n`); };
  put('ci-fixer', 'work/platform'); put('incident-intake', 'work/platform'); put('assess-and-post', 'work/platform'); put('issue-triage', 'work/web'); put('alert', 'personal/home');
})();

/* What each installed pack exposes to overrides (conductor pack plan): its
 * workflows' step ids and its trigger names. An overlay key that matches
 * nothing is an error (Packs.md). */
window.D.packMembers = {
  review: { workflows: { 'review-flow': ['fetch', 'lint', 'review', 'post', 'summarize'] }, triggers: ['on_review_request', 'on_push', 'on_label'], roles: ['github'] },
  'house-style': { workflows: { 'release-notes': ['collect', 'draft', 'post'] }, triggers: ['on_release'], roles: ['github'] },
  'sre-bot': { workflows: { sweep: ['check', 'report'] }, triggers: ['sweep', 'on_incident'], roles: ['pagerduty'] },
  kit: { workflows: { triage: ['classify', 'route'] }, triggers: ['on_incident'], roles: ['pagerduty'] },
};

/* One secret reference syntax ({{ vault "<vault>" "<key>" }}, Secrets.md):
 * the op:// scheme and the named secrets: block are retired. A secret is
 * known by <vault>/<key>; a name is at most a dashboard-only alias. Browser
 * credentials name each value as { vault, key } (feat/agent-browser), and
 * vaults use their real connection keys. */
(function () {
  const D = window.D;
  const split = (ref) => { if (/^op:\/\//.test(ref)) return ['op', ref.slice(5)]; const i = ref.indexOf('/'); return [ref.slice(0, i), ref.slice(i + 1)]; };
  D.secrets = D.secrets.filter((s) => !/^env:/.test(s.ref)).map((s) => { const [vault, key] = split(s.ref); return Object.assign({}, s, { alias: s.name, name: `${vault}/${key}`, ref: `${vault}/${key}`, vault, key }); });
  const add = (vault, key, alias, used, set) => { if (!D.secrets.some((s) => s.ref === `${vault}/${key}`)) D.secrets.push({ name: `${vault}/${key}`, ref: `${vault}/${key}`, vault, key, alias, set: set || null, used: used || [] }); };
  add('house', 'gh-app-key', 'GH_APP_KEY', ['connectors.gh.private_key'], new Date(Date.now() - 4e9).toISOString());
  add('op', 'Agents/Grafana/username', '', ['browser.credentials.grafana.username']);
  add('op', 'Agents/Grafana/password', '', ['browser.credentials.grafana.password']);
  add('op', 'Agents/Grafana/one-time password?attribute=otp', '', ['browser.credentials.grafana.otp']);
  add('op', 'Agents/App/username', '', ['browser.credentials.app.username']);
  D.vaults.forEach((v) => { v.cfg = ({ conductor: { type: 'conductor', unlock: { key: 'creds:house' } }, onepassword: { type: 'onepassword', account: 'acme', service_account: 'env:OP_SA' }, pass: { type: 'pass', prefix: 'conductor' }, hashicorp: { type: 'hashicorp', addr: 'https://vault.acme.internal:8200', mount: 'secret', unlock: { token: 'env:VAULT_TOKEN' } } })[v.type] || v.cfg; });
  const R = (vault, key) => ({ vault, key });
  const cred = (name, cfg) => { const c = D.access.credentials.find((x) => x.name === name); if (c) { c.cfg = Object.assign({ name }, cfg); c.identities = cfg.identities; c.origin = cfg.origin; } };
  cred('grafana', { origin: 'https://grafana.example.com', identities: ['bot:platform-sre'], username: R('op', 'Agents/Grafana/username'), password: R('op', 'Agents/Grafana/password'), otp: R('op', 'Agents/Grafana/one-time password?attribute=otp') });
  cred('statuspage', { origin: 'https://manage.statuspage.example', identities: ['bot:platform-sre'], passkey: R('house', 'passkeys/statuspage') });
  cred('app', { origin: 'https://app.example.com', identities: ['agent:triage@acme/app'], username: R('op', 'Agents/App/username'), password: R('op', 'Agents/App/password'), otp: R('op', 'Agents/App/one-time password?attribute=otp'), approval: true });
})();

/* Approvals say why and what happens, in words; the payload is shown whole. */
(function () {
  const P = (id) => window.D.proposals.find((p) => p.id === id) || {};
  Object.assign(P('p12'), { title: 'Replace the production web server', why: 'platform-sre is answering the SEV2 “api p99 latency” incident (case c29): the web servers still run the old AMI, and the plan rolls the new one out.', impact: 'Production · replaces 1 server (aws_instance.web), adds 1, changes 2 · expect a brief drain while the instance is replaced · undo by re-applying the previous AMI.', reversible: 'by re-applying the previous plan' });
  Object.assign(P('p4'), { title: 'Post the monthly forecast to #incidents', options: { channel: '#incidents', text: 'October forecast: AWS spend is tracking $41.2k against a $38k budget (+8%). Main driver: NAT gateway transfer (+44% week over week). Savings Plan coverage is 64%. Full breakdown in the billing dashboard.' }, why: 'A Slack user asked billing-sre (case c7) to share the monthly forecast. They aren\'t one of your operators.', impact: 'Posts one public message to #incidents, a channel for live incidents, which is unusual for a forecast · can be deleted afterwards, but people will have seen it.', reversible: 'the message can be deleted' });
  Object.assign(P('p11'), { title: 'Change the /search cache TTL to 300s', options: Object.assign({ store: 'state', namespace: 'search', key: 'cache_ttl_s', value: '300' }, P('p11').options || {}), why: 'A Slack user asked platform-sre to evaluate the /search cache TTL (case c24); a worker measured hit rates at 300s and proposes it.', impact: 'Changes one key in the shared KV store; /search caches results longer (up to 5 minutes stale) · undo by setting it back.', reversible: 'set the key back' });
})();

/* Teams and bots in conductor's real shapes (feat/members teams.go, bots.go).
 * A team's autonomy ceiling is the highest tier a bot may be PROMOTED to (run
 * without your signature): reversible or observe. Team tiers only make a bot
 * stricter. Routing is rules → the lead's triage → the fallback. Cross-team
 * talk is a pair of bots that list each other as peers, or an operator
 * bot_links entry; there are no team-to-team links. */
(function () {
  const D = window.D;
  const T = (n) => D.teams.find((t) => t.name === n);
  const B = (n) => D.bots.find((b) => b.name === n);
  const plat = T('platform'), fin = T('finance');
  if (plat) plat.cfg = { description: 'Platform services: api, web, infra', folder: plat.cfg.folder || 'work/platform', operators: ['ops', 'oncall'], autonomy: { ceiling: 'reversible', tiers: { never: ['gh.delete_*'] } }, budget: { proactive: { max_runs_per_day: 10, max_turns_per_day: 120 } }, isolation: { sandbox: 'required' }, browser: { logins: ['console.example.com'], credentials: ['grafana', 'statuspage'] }, knowledge: { repo: 'team-knowledge/platform', promotion: 'proposal' }, routing: { rules: [{ name: 'pd-api', match: { on: 'pd.incident', service: 'api*' }, to: 'platform-sre' }, { name: 'web-issues', match: { on: 'gh.issue_opened', repo: 'acme/web' }, to: 'web-triage' }], lead: 'platform-sre', fallback: 'platform-sre' } };
  if (fin) fin.cfg = { description: 'Cost, billing and forecasts', folder: fin.cfg.folder || 'work/billing', operators: ['ops'], autonomy: { ceiling: 'observe' }, budget: { spend_cap: { per_action: 2500, per_period: 6000 } }, isolation: { sandbox: 'required', handoff_workers: false }, routing: { rules: [{ name: 'cost-anomaly', match: { on: 'aws-cost.anomaly' }, to: 'billing-sre' }], fallback: 'billing-sre' } };
  const fix = (n, f) => { const b = B(n); if (b && b.cfg) f(b.cfg); };
  D.bots.forEach((b) => { const c = b.cfg || {}; if (c.isolation && c.isolation.mode) c.isolation = c.isolation.mode === 'container' ? { sandbox: 'required' } : {}; if (c.isolation && !Object.keys(c.isolation).length) delete c.isolation; if (c.autonomy && c.autonomy.spend) { c.autonomy.spend_cap = c.autonomy.spend; delete c.autonomy.spend; } if (c.autonomy && Array.isArray(c.autonomy.promote) && !c.autonomy.promote.length) delete c.autonomy.promote; });
  fix('platform-sre', (c) => { c.proactive = { every: '4h', quiet_hours: '22:00-07:00', budget: { max_runs_per_day: 6, max_turns_per_day: 60 }, checklist: { section: 'Proactive checks' }, notify_at: 'high' }; });
  fix('homelab', (c) => { c.proactive = { every: '6h', quiet_hours: '23:00-08:00', budget: { max_runs_per_day: 3 }, checklist: { file: 'bots/homelab-checks.md' } }; });
  // the one cross-team pair: both list each other (a link needs both sides, or bot_links)
  D.botLinks = [];
})();

/* Content kinds as conductor implements them (feat/members internal/bot/kinds.go):
 * knowledge {title, applies_to, evidence, confidence} + body; runbook {title,
 * when_to_use, prerequisites, steps (strings), verification, rollback, owner};
 * watch items are files watch/<slug>.md {what, signal, threshold, why}; log
 * {title, case, sweep, flagged_wrong}; handoff {session}. Collections live on a
 * team or bot (collections:), a pack's via from:. */
(function () {
  const D = window.D;
  const fm = (o, body) => '---\n' + YAML.stringify(o).replace(/\n?$/, '\n') + '---\n' + (body || '');
  const parse = (t) => { if (!/^---\n/.test(t || '')) return null; const e = t.indexOf('\n---\n', 3); try { return { fm: YAML.parse(t.slice(4, e + 1)).value || {}, body: t.slice(e + 5) }; } catch (x) { return null; } };
  const slug = (t) => String(t || '').toLowerCase().replace(/[^a-z0-9]+/g, '-').replace(/^-+|-+$/g, '');
  const stepStr = (s) => (typeof s === 'string' ? s : !s ? s : `${s.do || ''}${s.run ? ` (run: \`${s.run}\`)` : s.verb ? ` (verb: ${s.verb})` : ''}`);
  const convert = (text) => {
    const x = parse(text); if (!x) return text; const f = x.fm;
    if (f.kind === 'knowledge') return fm({ kind: 'knowledge', title: f.title, applies_to: f.applies_to, evidence: f.source || f.evidence || 'operator note', confidence: f.confidence || 'medium' }, (f.statement ? f.statement + '\n\n' : '') + (x.body || ''));
    if (f.kind === 'runbook') { const o = { kind: 'runbook', title: f.title, when_to_use: f.when || f.when_to_use, prerequisites: f.prerequisites, steps: (f.steps || []).map(stepStr), verification: f.verification, rollback: f.rollback || 'Nothing to roll back: the steps only read.', owner: f.owner }; Object.keys(o).forEach((k) => o[k] == null && delete o[k]); return fm(o, x.body); }
    if (f.kind === 'log') { const o = { kind: 'log', title: f.title }; if (/^c\d+$/.test(f.case || '')) o.case = f.case; else if (f.case) o.sweep = f.case; return fm(o, x.body); }
    return text;
  };
  const watchSplit = (files) => { const w = files['watch.md']; if (w == null) return; delete files['watch.md']; String(w).split(/^## /m).slice(1).forEach((chunk) => { const [head, ...rest] = chunk.split('\n'); const e = { kind: 'watch', what: head.trim() }; rest.forEach((l) => { const m = l.match(/^- (signal|threshold|why):\s*(.*)$/); if (m) e[m[1]] = m[2]; }); files[`watch/${slug(e.what)}.md`] = fm(e, ''); }); };
  const conv = (files) => { Object.keys(files).forEach((p) => { files[p] = convert(files[p]); }); watchSplit(files); };
  conv(D.memory.files);
  Object.values(D.teamKnowledge).forEach((tk) => { if (!tk) return; conv(tk.files); (tk.proposals || []).forEach((p) => { if (p.content) p.content = convert(p.content); }); });
  Object.values(D.botMemory || {}).forEach(conv);
  // a malformed bot proposal, as conductor would reject it
  const k5 = D.teamKnowledge.platform.proposals.find((p) => p.id === 'k5');
  if (k5) k5.content = fm({ kind: 'runbook', title: 'Checkout errors', when_to_use: 'checkout 5xx above 2% for 5 minutes', steps: ['Check the canary', { run: 'kubectl rollout undo deploy/web' }, ''] }, '');
  // collections: the operator's on a team; a pack's by from:
  D.packCollectionFiles = { '.conductor/packs/sre-bot/collections/postmortems.yaml': { dir: 'postmortems', fields: { title: { type: 'string', required: true }, date: { type: 'string' }, severity: { type: 'enum', values: ['sev1', 'sev2', 'sev3'], required: true }, impact: { type: 'text' } }, template: '## Timeline\n\n## Cause\n\n## Follow-ups\n', writers: ['operator', 'proposal'] } };
  const plat = D.teams.find((t) => t.name === 'platform');
  if (plat) plat.cfg.collections = { postmortems: { from: '.conductor/packs/sre-bot/collections/postmortems.yaml' } };
  D.collections = []; D.packCollections = [];
})();

/* 2026-10 addendum (Daylight): plain headlines over the technical detail. Each needs-you item gets a title in
 * words, the reason it matters and the one verb; the machine detail (the old title and sub) stays, last, in mono.
 * An update that installs itself is not "needing you": it moves to "For your information". */
(function () {
  const D = window.D;
  const PLAIN = {
    '#/bots/platform-sre/proposals/p12': ['Replace the production web server', 'platform-sre wants to run terraform apply on production, for the SEV2 latency incident', 'Review'],
    '#/bots/billing-sre/proposals': ['Post the monthly forecast to #incidents', 'billing-sre, asked by a Slack user who isn’t one of your operators', 'Review'],
    '#/runs/r-7ef0': ['ci-fixer couldn’t fix acme/web#1288', 'Its change made 2 tests fail. Nothing was pushed.', 'Open'],
    '#/runs/r-7ed1': ['pr-review needs your call on a draft', 'Approve, revise or discard its review of acme/infra#77', 'Answer'],
    '#/browser/b-02': ['Approve a sign-in on app.example.com', 'The triage agent’s browser is waiting on a push approval', 'Open'],
  };
  D.fyi = D.attention.filter((a) => a.kind === 'update').map((a) => Object.assign({}, a, { plain: 'Update v0.72.0 is ready', why: 'It installs itself within 10 minutes; running work finishes first.' }));
  D.fyi.push({ kind: 'sandbox', s: 'idle', plain: 'The sandbox blocked pastebin.com', why: 'A ci-fixer agent tried to reach it; nothing left the box.', title: 'egress denied', sub: 'agent:fixer@acme/web · once per browser', href: '#/logs/audit', age: '1h' });
  D.attention = D.attention.filter((a) => a.kind !== 'update');
  D.attention.forEach((a) => { const p = PLAIN[a.href]; if (p) [a.plain, a.why, a.verb] = p; });
})();
