// Mock content for the v2 direction screens. The facts are the dashboard prototype's
// (js/data.js on design/dashboard); the wording applies the information-design principles:
// a plain headline first, technical detail beneath, human times, ids last.

export const NOW = 'Fri 16 Oct · 17:55';

export const NEEDS = [
  { kind: 'approval', glyph: 'needs-you', s: 'accent', title: 'Replace the production web server', why: 'platform-sre wants to run terraform apply on production, for the SEV2 latency incident', tech: 'p12 · infra@3f2a9c1d · +1 ~2 −0', age: '7 min ago', act: 'Review' },
  { kind: 'approval', glyph: 'needs-you', s: 'accent', title: 'Post the monthly forecast to #incidents', why: 'billing-sre, asked by a Slack user who isn’t one of your operators', tech: 'p4 · slack-ops.post', age: '22 min ago', act: 'Review' },
  { kind: 'failed', glyph: 'danger', s: 'danger', title: 'ci-fixer couldn’t fix acme/web#1288', why: 'Its change made 2 tests fail. Nothing was pushed.', tech: 'r-7ef0 · step test', age: '52 min ago', act: 'Open' },
  { kind: 'question', glyph: 'warning', s: 'warning', title: 'pr-review needs your call on a draft', why: 'Approve, revise or discard its review of acme/infra#77', tech: 'r-7ed1 · hand-off', age: '1 h ago', act: 'Answer' },
  { kind: 'signin', glyph: 'warning', s: 'warning', title: 'Approve a sign-in on app.example.com', why: 'The triage agent’s browser is waiting on a push approval', tech: 'b-02 · agent:triage', age: '3 min ago', act: 'Open' },
];
export const FYI = [
  { glyph: 'info', s: 'info', title: 'Update v0.72.0 is ready', why: 'Installs within 10 minutes; running work finishes first.' },
  { glyph: 'shield', s: 'neutral', title: 'Sandbox blocked pastebin.com', why: 'agent:fixer tried to reach it from acme/web. Nothing left the box.' },
];
export const RUNNING = [
  { wf: 'ci-fixer', target: 'acme/api#482', doing: 'Fixing the failing check', step: 3, of: 6, since: '6 min' },
  { wf: 'pr-review', target: 'acme/web#1291', doing: 'Reviewing the pull request', step: 2, of: 3, since: '2 min' },
  { wf: 'platform-sre', target: 'case c31', doing: 'Investigating db-prod-1 disk usage', step: 1, of: 1, since: '11 min' },
];
// runs per hour, last 12h (oldest first): [ok, failed]
export const HOURLY = [[1, 0], [2, 0], [1, 0], [0, 0], [2, 1], [1, 0], [3, 0], [1, 0], [2, 0], [1, 1], [2, 0], [2, 0]];
export const STATS = { runs: 20, fine: 18, failed: 2, cost: '$8.40', costTrend: [4.1, 6.8, 5.2, 7.9, 3.3, 2.1, 8.4, 9.7, 6.2, 5.5, 7.1, 11.9, 8.8, 6.4], approvals: 2 };

// Lanes for the score: minutes ago of each run, and its state.
export const LANES = [
  ['pr-review', [[2, 'running'], [31, 'ok'], [95, 'waiting'], [252, 'ok'], [503, 'ok'], [640, 'ok']]],
  ['ci-fixer', [[6, 'running'], [52, 'failed'], [230, 'parked'], [588, 'ok']]],
  ['issue-triage', [[79, 'ok'], [299, 'ok'], [410, 'ok']]],
  ['platform-sre', [[7, 'approval'], [11, 'running'], [176, 'ok'], [716, 'ok']]],
  ['dep-bump', [[131, 'ok']]],
  ['release-notes', [[418, 'failed']]],
];

export const RUN = {
  id: 'r-7ef0', wf: 'ci-fixer', target: 'acme/web#1288', title: 'ci-fixer couldn’t fix the failing check on acme/web#1288',
  summary: 'It changed how refunds are rounded in refund.go, but 2 tests now fail: TestCheckout and TestRefund. It stopped before pushing anything, so the pull request is unchanged.',
  started: '52 min ago', at: '17:03', took: '12 min', cost: '$1.12', trigger: 'A check failed on acme/web#1288',
  steps: [
    ['Get the failing logs', 'ok', '2 s', '3 failing checks found'],
    ['Decide what failed', 'ok', '8 s', 'A test failure (91% sure, 6% flaky)'],
    ['Fix it', 'ok', '7 min 40 s', 'Changed applyFee to round half-even'],
    ['Run the tests', 'failed', '26 s', '2 of 148 tests fail'],
    ['Push the fix', 'skipped', '', 'Not reached'],
    ['Comment on the PR', 'skipped', '', 'Not reached'],
  ],
  failures: [['TestCheckout', 'checkout_test.go:142', 'total = 10.49, want 10.50'], ['TestRefund', 'refund_test.go:88', 'got 1999, want 2000']],
  log: [
    ['', 'exit_code', ': ', '1', 'num'],
    ['', 'stdout', ': |', '', ''],
    ['fail', '  --- FAIL: TestCheckout (0.31s)', '', '', ''],
    ['', '      checkout_test.go:142: total = 10.49, want 10.50', '', '', ''],
    ['fail', '  --- FAIL: TestRefund (0.02s)', '', '', ''],
    ['', '      refund_test.go:88: got 1999, want 2000', '', '', ''],
    ['', 'duration_ms', ': ', '26114', 'num'],
  ],
};

export const PROPOSAL = {
  bot: 'platform-sre', role: 'Keeps production healthy for the platform team', status: 'Working on case c31',
  title: 'Replace the production web server', ask: 'platform-sre wants to run terraform apply on production.',
  why: 'It’s answering the SEV2 incident “api p99 latency” (case c29): the web servers still run the old image, and this rolls out the new one.',
  changes: [['Adds', 1, 'success'], ['Changes', 2, 'running'], ['Replaces', 1, 'warning'], ['Destroys', 0, 'neutral']],
  risk: [['Where', 'Production · workspace prod'], ['Effect', 'About 2 minutes of drain while 1 server is replaced'], ['Undo', 'Re-apply the previous plan (kept 7 days)']],
  origin: 'Started by a PagerDuty incident, not a person.', expires: 'Expires in 23 h 52 min',
  tech: [['Proposal', 'p12'], ['Plan', 'infra@3f2a9c1d · envs/prod'], ['Replaces', 'aws_instance.web'], ['Plan sha256', '9c1e…4b07']],
  can: { own: ['Read GitHub and metrics', 'Post to #platform', 'Open cases'], asks: ['terraform apply', 'Write to the KV store', 'Comment on pull requests'], never: ['Delete repositories', 'Change IAM'] },
};

export const HISTORY = [
  ['Today', [
    ['running', 'ci-fixer', 'acme/api#482', 'A check failed', '6 min ago', '', '$0.71', 'r-7f3a'],
    ['running', 'pr-review', 'acme/web#1291', 'Review requested', '2 min ago', '', '$0.12', 'r-7f2c'],
    ['running', 'platform-sre', 'case c31', 'Started by you', '11 min ago', '', '$0.33', 'r-7f29'],
    ['ok', 'pr-review', 'acme/api#480', 'Review requested', '38 min ago', '7 min', '$0.41', 'r-7f11'],
    ['failed', 'ci-fixer', 'acme/web#1288', 'A check failed', '1 h ago', '12 min', '$1.12', 'r-7ef0', '2 tests fail after its change'],
    ['ok', 'issue-triage', 'acme/api#486', 'Issue opened', '1 h ago', '1 min', '$0.06', 'r-7ee3'],
    ['waiting', 'pr-review', 'acme/infra#77', 'Review requested', '1 h ago', '', '$0.38', 'r-7ed1', 'Waiting for your answer on a draft'],
    ['ok', 'dep-bump', 'acme/web', 'Mondays at 06:00', '2 h ago', '9 min', '$0.82', 'r-7ec8'],
    ['ok', 'platform-sre', 'PagerDuty incident', 'Incident triggered', '3 h ago', '14 min', '$0.54', 'r-7eb2'],
    ['parked', 'ci-fixer', 'acme/api#471', 'A check failed', '3 h ago', '1 min', '$0.00', 'r-7ea0', 'Parked: the PR has the hold label'],
    ['ok', 'pr-review', 'acme/web#1285', 'Review requested', '4 h ago', '8 min', '$0.29', 'r-7e91'],
    ['ok', 'issue-triage', 'acme/web#1284', 'Issue opened', '5 h ago', '1 min', '$0.05', 'r-7e80'],
    ['failed', 'release-notes', 'acme/api', 'Release drafted', '7 h ago', '2 min', '$0.09', 'r-7e64', 'Stopped at its $0.05 budget'],
    ['ok', 'pr-review', 'acme/api#468', 'Review requested', '8 h ago', '7 min', '$0.36', 'r-7e52'],
    ['ok', 'ci-fixer', 'acme/web#1279', 'A check failed', '10 h ago', '22 min', '$1.48', 'r-7e40'],
    ['ok', 'platform-sre', 'routine sweep', 'Every 30 min', '12 h ago', '4 min', '$0.00', 'r-7e31'],
  ]],
];

export const WORKFLOW = {
  name: 'ci-fixer', folder: 'work / platform', desc: 'Fixes failing CI checks and comments on the pull request.',
  when: 'A CI check fails', where: 'on acme/api or acme/web', unless: 'unless the pull request is labelled “wip”',
  steps: [
    ['logs', 'Get the failing logs', 'GitHub · check logs', 'action', ''],
    ['triage', 'Decide what kind of failure it is', 'AI decision, small model · test failure, flaky or infrastructure', 'decide', ''],
    ['fix', 'Fix it', 'AI agent · claude-cli as “fixer” · “Fix the failing check on {repo}#{number}…”', 'agent', 'Only if it’s a test failure'],
    ['test', 'Run the tests', 'Command · go test ./...', 'code', ''],
    ['push', 'Push the fix to the pull request', 'GitHub · push', 'action', 'Only if the tests pass'],
    ['comment', 'Comment on the pull request', 'GitHub · comment', 'action', ''],
  ],
  budget: 'Up to $2.00 per run', recent: '9 runs in 24 h · 7 fixed · 1 failed · 1 parked', testEvent: 'acme/api#482 · unit tests failed',
  yaml: [
    ['name', 'ci-fixer'], ['folder', '"work/platform"'], ['on', 'gh.check_failed'], ['filter', '{ repo: [acme/api, acme/web], not_label_any: [wip] }'],
    ['policy', ''], ['  budget', '{ max_cost_usd: 2.00 }'], ['steps', ''], ['  - id', 'logs'], ['    uses', 'gh.check_logs'], ['  - id', 'triage'], ['    model', 'light'],
  ],
};
