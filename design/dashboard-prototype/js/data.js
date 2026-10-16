/* Fake data for the prototype. Shapes mirror the API in docs/design/dashboard.md
 * (RunHistory/StepRecord from internal/store/history.go, the members ledger
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
    { name: 'tf-apply', use: 'terraform', status: 'idle', detail: 'engine tofu 1.8 · WRITE role · no member verb reaches it', events: 0, verbs: 1 },
    { name: 'hoff', use: 'web', status: 'wait', detail: 'listen :8099 · 1 draft awaiting answer', events: 3, verbs: 3 },
  ];

  D.runtimes = [
    { name: 'paseo', use: 'paseo', status: 'ok', detail: 'local · 6 agents · sandboxed entries written', isolation: 'sandboxed provider entries' },
    { name: 'claude-cli', use: 'cli', status: 'ok', detail: 'claude 2.4 · namespace sandbox', isolation: 'namespace' },
    { name: 'oc', use: 'opencode', status: 'ok', detail: 'per-dispatch serve · expose relay', isolation: 'namespace' },
    { name: 'deck', use: 'agent-deck', status: 'idle', detail: '--wrapper checked', isolation: 'namespace' },
    { name: 'gpu-box', use: 'cli', status: 'ok', detail: 'host: gpu-box (ssh) · container', isolation: 'container · agents:latest' },
  ];
  D.hosts = [ { name: 'gpu-box', addr: 'gpu-box.tail0000.ts.net', user: 'conductor', status: 'ok', rtt: '18ms' }, { name: 'build-box', addr: '10.0.4.12', user: 'ci', status: 'ok', rtt: '3ms' } ];

  // workflows: steps form a DAG via needs:
  D.workflows = [
    { name: 'pr-review', source: 'pack review', on: 'gh.review_requested', runs24: 31, ok: 0.94, cost24: 6.12, steps: [
      { id: 'fetch', kind: 'uses', uses: 'gh.pr_diff', needs: [] },
      { id: 'lint', kind: 'code', run: 'js', needs: ['fetch'] },
      { id: 'review', kind: 'agent', runtime: 'claude-cli', model: 'reviewer', needs: ['fetch'] },
      { id: 'gate', kind: 'gate', checks: ['review/no-todos'], needs: ['review', 'lint'] },
      { id: 'post', kind: 'uses', uses: 'gh.review', needs: ['gate'] } ] },
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
    { name: 'member:platform-sre', source: 'member', on: 'pd.incident · manual · sweep', runs24: 7, ok: 0.86, cost24: 3.75, steps: [] },
    { name: 'alert', source: 'config', on: 'conductor.escalate · needs_input · failed', runs24: 4, ok: 1, cost24: 0, steps: [] },
  ];

  const st = (id, status, ms, cost, extra) => Object.assign({ id, status, duration_ms: ms, cost_usd: cost }, extra || {});
  D.runs = [
    { id: 'r-7f3a', workflow: 'ci-fixer', trigger: 'gh.check_failed', repo: 'acme/api', number: 482, status: 'run', started: m(6), cost: 0.71, tokens: 48210, steps: [
      st('logs', 'ok', 2100, 0), st('triage', 'ok', 8400, 0.03), st('fix', 'run', 0, 0.68, { started: m(5) }), st('test', 'idle'), st('push', 'idle'), st('comment', 'idle') ] },
    { id: 'r-7f2c', workflow: 'pr-review', trigger: 'gh.review_requested', repo: 'acme/web', number: 1291, status: 'run', started: m(2), cost: 0.12, steps: [
      st('fetch', 'ok', 900, 0), st('lint', 'run', 0, 0), st('review', 'run', 0, 0.12), st('gate', 'idle'), st('post', 'idle') ] },
    { id: 'r-7f29', workflow: 'member:platform-sre', trigger: 'manual (spawn w41)', repo: '', number: 0, status: 'run', started: m(11), cost: 0.33, steps: [ st('work', 'run', 0, 0.33) ] },
    { id: 'r-7f11', workflow: 'pr-review', trigger: 'gh.review_requested', repo: 'acme/api', number: 480, status: 'ok', started: m(38), finished: m(31), cost: 0.41, tokens: 30110, steps: [] },
    { id: 'r-7ef0', workflow: 'ci-fixer', trigger: 'gh.check_failed', repo: 'acme/web', number: 1288, status: 'fail', started: m(64), finished: m(52), cost: 1.12, failed_step: 'test', error: 'go test ./...: 2 failures (TestCheckout, TestRefund)', steps: [] },
    { id: 'r-7ee3', workflow: 'issue-triage', trigger: 'gh.issue_opened', repo: 'acme/api', number: 486, status: 'ok', started: m(80), finished: m(79), cost: 0.06, steps: [] },
    { id: 'r-7ed1', workflow: 'pr-review', trigger: 'gh.review_requested', repo: 'acme/infra', number: 77, status: 'wait', started: m(95), cost: 0.38, detail: 'hand-off: awaiting answer on hoff (approve / revise / discard)', steps: [] },
    { id: 'r-7ec8', workflow: 'dep-bump', trigger: 'schedule', repo: 'acme/web', number: 0, status: 'ok', started: m(140), finished: m(131), cost: 0.82, steps: [] },
    { id: 'r-7eb2', workflow: 'member:platform-sre', trigger: 'pd.incident', repo: '', number: 0, status: 'ok', started: m(190), finished: m(176), cost: 0.54, steps: [] },
    { id: 'r-7ea0', workflow: 'ci-fixer', trigger: 'gh.check_failed', repo: 'acme/api', number: 471, status: 'idle', started: m(230), finished: m(230), cost: 0, detail: 'parked: conductor:hold label', steps: [] },
    { id: 'r-7e91', workflow: 'pr-review', trigger: 'gh.review_requested', repo: 'acme/web', number: 1285, status: 'ok', started: m(260), finished: m(252), cost: 0.29, steps: [] },
    { id: 'r-7e80', workflow: 'issue-triage', trigger: 'gh.issue_opened', repo: 'acme/web', number: 1284, status: 'ok', started: m(300), finished: m(299), cost: 0.05, steps: [] },
    { id: 'r-7e64', workflow: 'release-notes', trigger: 'gh.release_drafted', repo: 'acme/api', number: 0, status: 'fail', started: m(420), finished: m(418), cost: 0.09, failed_step: 'draft', error: 'budget: max_cost_usd 0.05 exceeded (shed)', steps: [] },
    { id: 'r-7e52', workflow: 'pr-review', trigger: 'gh.review_requested', repo: 'acme/api', number: 468, status: 'ok', started: m(510), finished: m(503), cost: 0.36, steps: [] },
    { id: 'r-7e40', workflow: 'ci-fixer', trigger: 'gh.check_failed', repo: 'acme/web', number: 1279, status: 'ok', started: m(610), finished: m(588), cost: 1.48, steps: [] },
    { id: 'r-7e31', workflow: 'member:platform-sre', trigger: 'sweep', repo: '', number: 0, status: 'ok', started: m(720), finished: m(716), cost: 0.004, steps: [] },
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
    { kind: 'approval', s: 'ember', title: 'terraform apply · infra@3f2a9c1d', sub: 'platform-sre · p12 · +1 ~2 -0 · REPLACES aws_instance.web', href: '#/members/platform-sre/proposals/p12', age: '7m' },
    { kind: 'approval', s: 'ember', title: 'slack-ops.post to #incidents', sub: 'billing-sre · p4 · requester chat:U-THIRD (user)', href: '#/members/billing-sre/proposals', age: '22m' },
    { kind: 'failed', s: 'fail', title: 'ci-fixer failed on acme/web#1288', sub: 'step test · 2 failures', href: '#/runs/r-7ef0', age: '52m' },
    { kind: 'needs_input', s: 'wait', title: 'hand-off awaiting answer · acme/infra#77', sub: 'pr-review · hoff draft (approve / revise / discard)', href: '#/runs/r-7ed1', age: '1h' },
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

  /* ---------- members (#178) ---------- */
  D.members = [
    { name: 'platform-sre', color: '#ff6a2b', desc: 'SRE for the platform services', home: 'live', session: 's7', turns: 41, maxTurns: 100, age: '9h', maxAge: '24h', shadow: false, sandboxed: true, mode: 'namespace',
      operators: ['ops'], open: 4, pending: 1, workers: 2, spend: 0, proactive: { every: '4h', next: '1h 12m', quiet: '22:00-07:00', budget: '2/6 runs · $0.31/$2.00' }, peers: ['billing-sre'] },
    { name: 'billing-sre', color: '#4fb8e8', desc: 'Cost and billing operations', home: 'idle', session: 's3', turns: 12, maxTurns: 100, age: '2h', maxAge: '24h', shadow: false, sandboxed: true, mode: 'container',
      operators: ['ops'], open: 2, pending: 1, workers: 0, spend: 1240, proactive: null, peers: ['platform-sre'] },
    { name: 'web-triage', color: '#3fbf7f', desc: 'Triage for the web app’s issues', home: 'live', session: 's1', turns: 3, maxTurns: 60, age: '40m', maxAge: '12h', shadow: true, sandboxed: true, mode: 'namespace',
      operators: ['ops', 'oncall'], open: 1, pending: 0, workers: 0, spend: 0, proactive: null, peers: [] },
  ];

  D.chat = [
    { who: 'event', at: m(48), case: 'c29', text: 'pd.incident · SEV2 · api p99 latency > 2s for 10m (acme/api, us-east)', fenced: true },
    { who: 'member', at: m(47), text: 'Opened **c29**. Checking the runbook `runbooks/latency.md`. I’ll start a read-only worker to pull the last hour of p99 by route and the deploy log.' },
    { who: 'tool', at: m(47), text: 'conductor member spawn --case c29 --group recon --read-only "p99 by route, last 60m"' },
    { who: 'tool', at: m(46), text: 'conductor member spawn --case c29 --group recon --read-only "deploys + config changes, last 3h"' },
    { who: 'member', at: m(31), text: 'Both workers reported. The regression lines up with **web** instances on the old AMI after the 14:02 scale-out; routes `/checkout` and `/refund` are the slow ones. The fix is in `infra` commit `3f2a9c1d`: it pins the launch template to the current AMI and replaces `aws_instance.web`.' },
    { who: 'member', at: m(8), text: 'I proposed **p12**: `terraform apply` of infra@3f2a9c1d, dir envs/prod, workspace prod. Conductor planned it: +1 ~2 -0, and it **replaces aws_instance.web**. Awaiting your approval.' },
    { who: 'op', at: m(5), text: 'What happens to in-flight requests on web while it’s replaced?' },
    { who: 'member', at: m(4), text: 'The ALB drains the old target for 30s (deregistration_delay in `modules/web/alb.tf`); create_before_destroy is set, so the new instance is healthy before the old one goes. Expect no dropped requests; a brief p99 bump during the swap.' },
  ];

  D.cases = [
    { id: 'c31', title: 'Record db-prod-1 failover drill outcome', status: 'open', requester: 'operator:ops', cls: 'operator', opened: m(30), workers: 0, proposals: 0 },
    { id: 'c29', title: 'SEV2 api p99 latency > 2s', status: 'open', requester: 'event:pd.incident', cls: 'event', opened: m(48), workers: 2, proposals: 1, urgent: true },
    { id: 'c27', title: 'FINDING high · cost jump 44% on nat-gateway', status: 'open', requester: 'proactive (sweep x4)', cls: 'proactive', opened: m(260), workers: 0, proposals: 0 },
    { id: 'c24', title: 'Can you bump the cache TTL for /search?', status: 'open', requester: 'chat:U-THIRD', cls: 'user', opened: m(380), workers: 1, proposals: 1 },
    { id: 'c19', title: 'Peer: billing-sre asks for NAT gateway owners', status: 'closed', requester: 'member:billing-sre', cls: 'peer', opened: m(1600), workers: 0, proposals: 0 },
    { id: 'c12', title: 'Weekly runbook consolidation', status: 'closed', requester: 'operator:ops', cls: 'operator', opened: m(4000), workers: 1, proposals: 0 },
  ];

  D.proposals = [
    { id: 'p12', member: 'platform-sre', kind: 'terraform', verb: 'terraform.apply', tier: 'propose', status: 'pending', case: 'c29', requester: 'event:pd.incident', cls: 'event', worker: null, at: m(8), expires: '23h 52m',
      summary: 'terraform apply of infra@3f2a9c1d07e4, dir envs/prod, workspace prod: +1 ~2 -0; REPLACES aws_instance.web',
      tf: { repo: 'infra', commit: '3f2a9c1d07e4b1c0a9d2e88f31c0b6a1d4e2f907', dir: 'envs/prod', workspace: 'prod', var_files: ['prod.tfvars'], targets: [], add: 1, change: 2, destroy: 0, replaces: ['aws_instance.web'], plan_sha: 'a4c1e09b7f22d81c5e6a03f9b2d47c18e5f0a6b3c9d2e1f4a7b8c0d3e6f9a2b5' },
      digest: 'sha256:9f2c47e1b0a3d58c6e7f1a2b3c4d5e6f708192a3b4c5d6e7f8091a2b3c4d5e6f7' },
    { id: 'p11', member: 'platform-sre', kind: 'verb', verb: 'kv.set', tier: 'propose', status: 'pending', case: 'c24', requester: 'chat:U-THIRD', cls: 'user', worker: 'w28', at: m(370), expires: '17h 50m',
      summary: 'kv.set search/cache_ttl = "300s" (was "60s")', options: { key: 'search/cache_ttl', value: '300s' }, digest: 'sha256:1b7e0c9d2a4f6e8b0c1d3e5f7a9b2c4d6e8f0a1b3c5d7e9f1a2b4c6d8e0f2a4b' },
    { id: 'p9', member: 'platform-sre', kind: 'verb', verb: 'slack-ops.post', tier: 'reversible', status: 'shadow', case: 'c27', requester: 'proactive (sweep x4)', cls: 'proactive', at: m(255), summary: 'post finding to #platform-costs', digest: 'sha256:—' },
    { id: 'p8', member: 'platform-sre', kind: 'verb', verb: 'gh.comment', tier: 'propose', status: 'executed', approver: 'ops (passkey · laptop)', case: 'c22', at: m(1500), summary: 'comment on acme/api#455 with the RCA', digest: 'sha256:5d9a…' },
    { id: 'p6', member: 'platform-sre', kind: 'terraform', verb: 'terraform.apply', tier: 'propose', status: 'refused', case: 'c18', at: m(3100), summary: 'destroys stateful local_file.config — refused by default (allow_destroy)', digest: 'sha256:—' },
    { id: 'p4', member: 'billing-sre', kind: 'verb', verb: 'slack-ops.post', tier: 'propose', status: 'pending', case: 'c7', requester: 'chat:U-THIRD', cls: 'user', at: m(22), expires: '23h 38m', summary: 'post the monthly forecast to #incidents', options: { channel: '#incidents' }, digest: 'sha256:c3e5a7b9d1f3a5c7e9b1d3f5a7c9e1b3d5f7a9c1e3b5d7f9a1c3e5b7d9f1a3c5' },
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
      { sha: '8e1f0aa', msg: 'knowledge: db-prod-1 failover drill outcome', author: 'platform-sre', trailers: 'Member-Case: c31 · Member-Role: home · Member-Session: s7', at: m(1) },
      { sha: '41c09d2', msg: 'episodes: c29 recon findings', author: 'platform-sre', trailers: 'Member-Case: c29 · Member-Worker: w40', at: m(31) },
      { sha: 'b07e5c1', msg: 'runbooks/latency: add ALB deregistration note', author: 'ops (dashboard)', trailers: 'Member-Role: operator · Edited-Via: dashboard', at: m(1440) },
      { sha: '2aa91f4', msg: 'handoff: s6 → s7', author: 'platform-sre', trailers: 'Member-Session: s6', at: m(560) },
    ],
  };

  D.access = {
    verbs: [
      { v: 'gh.get_*', tier: 'observe' }, { v: 'kv.get', tier: 'observe' }, { v: 'slack-ops.post', tier: 'reversible', promoted: true },
      { v: 'kv.set', tier: 'propose' }, { v: 'gh.comment', tier: 'propose' }, { v: 'terraform.apply', tier: 'propose' }, { v: 'gh.delete_*', tier: 'never' },
    ],
    credentials: [
      { name: 'grafana', origin: 'https://grafana.example.com', identity: 'member:platform-sre', fields: 'username, password, otp', vault: 'op', ref: 'Agents/Grafana/*' },
      { name: 'statuspage', origin: 'https://manage.statuspage.example', identity: 'member:platform-sre', fields: 'passkey', vault: 'house', ref: 'passkeys/statuspage' },
    ],
    logins: [ { site: 'console.example.com', identity: 'member:platform-sre', seeded: m(4300), cookies: 6 } ],
  };

  /* ---------- browser (#182) ---------- */
  D.browsers = [
    { id: 'b-01', identity: 'member:platform-sre', agent: 'w41', requester: 'event:pd.incident', loggedIn: false, url: 'https://grafana.example.com/d/api-latency?from=now-1h', title: 'API latency · Grafana', status: 'run', tabs: 2, actions: 37, profile: 'anonymous', note: 'requested by event, not the operator → anonymous profile' },
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
    { name: 'app password', ref: 'op://Agents/App/password', vault: 'op', set: null, used: ['browser.credentials.app.password'] },
    { name: 'app otp', ref: 'op://Agents/App/one-time password?attribute=otp', vault: 'op', set: null, used: ['browser.credentials.app.otp'] },
    { name: 'statuspage passkey', ref: 'house/passkeys/statuspage', vault: 'house', set: m(4300), used: ['browser.credentials.statuspage.passkey'] },
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
    { name: 'sre-member', use: 'conductor-packs/sre-member', ver: '0.9.0', sha: '3d70b2e', status: 'ok', official: true, hold: true, triggers: '0 armed / 2', overrides: 1 },
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
    { v: 'v0.72.0', date: '2026-10-16', notes: ['Members: proactive sweeps report cost from run history', 'Browser: per-identity evaluate', 'Fix: a paused daemon still rotated member sessions'], preflight: 'passed', current: false },
    { v: 'v0.71.2', date: '2026-10-12', notes: ['Reclaim a checkout:none run’s workspace when its launch fails'], current: true },
    { v: 'v0.71.1', date: '2026-10-09', notes: ['Remove a run’s directory when its workspace is reclaimed'], current: false },
  ];

  D.audit = [
    { ts: m(1), event: 'memory_remember', who: 'member:platform-sre', detail: 'knowledge/db-prod-1.md · commit 8e1f0aa · case c31' },
    { ts: m(3), event: 'browser_action', who: 'agent:triage@acme/app', detail: 'await-approval · app.example.com/login/approve · logged_in: app.example.com' },
    { ts: m(3), event: 'browser_fill', who: 'agent:triage@acme/app', detail: 'credential app · origin https://app.example.com · field password · len 24' },
    { ts: m(5), event: 'dashboard_session', who: 'operator:ops', detail: 'passkey assertion · credential laptop · 127.0.0.1' },
    { ts: m(7), event: 'member_proposal', who: 'member:platform-sre', detail: 'p12 terraform.apply · planned +1 ~2 -0 · plan sha a4c1e09b…' },
    { ts: m(31), event: 'verb', who: 'r-7f11', detail: 'gh.review acme/api#480 · as bot' },
    { ts: m(52), event: 'workflow_failed', who: 'r-7ef0', detail: 'ci-fixer · step test · acme/web#1288' },
    { ts: m(70), event: 'egress_denied', who: 'agent:fixer@acme/web', detail: 'pastebin.com:443' },
    { ts: m(1500), event: 'member_approval', who: 'operator:ops', detail: 'p8 approved · webauthn · credential laptop · digest 5d9a…' },
    { ts: m(1440), event: 'config_apply', who: 'operator:ops', detail: 'members.platform-sre.proactive.every 6h → 4h · backup cfg-20261015-1412 · validated on copy' },
  ];

  D.configHistory = [
    { id: 'cfg-20261016-0912', at: m(50), who: 'ops (dashboard)', summary: 'triggers[ci-fixer].policy.budget.max_cost_usd 1.50 → 2.00', status: 'live' },
    { id: 'cfg-20261015-1412', at: m(1440), who: 'ops (dashboard)', summary: 'members.platform-sre.proactive.every 6h → 4h', status: 'backup' },
    { id: 'cfg-20261014-2205', at: m(2100), who: 'file edit (external)', summary: 'detected on reload: connectors.pd added', status: 'backup' },
    { id: 'cfg-20261013-1730', at: m(3400), who: 'ops (dashboard)', summary: 'packs.review.steps.review-flow/review.model my-fleet', status: 'backup' },
  ];
})();
