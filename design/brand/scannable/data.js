// Mock data for the scannable proposal: the same day as the other mocks, with routine work and
// dangerous work mixed, so the risk encoding has something to show. Neutral names only.

// Needs you, most consequential first.
export const NEEDS = [
  { kind: 'approval', title: 'Apply the Terraform plan to production', why: 'Replaces 1 web server to fix the SEV2 latency incident', ctx: ['platform-sre', 'infra · envs/prod'], env: 'production', action: 'apply', tier: 'propose', state: 'needs', time: '7 min', act: 'Review', primary: true, id: 'p12 · infra@3f2a9c1d' },
  { kind: 'approval', title: 'Give deploy-bot write access to acme/infra', why: 'It asked to push rollout configs itself', ctx: ['platform-sre', 'acme/infra'], action: 'perms', tier: 'propose', riskWord: 'Grants access', state: 'needs', time: '35 min', act: 'Review', primary: true, id: 'p15 · gh.collaborator_add' },
  { kind: 'approval', title: 'Buy a 1-year compute savings plan', why: 'Saves about $310 a month on steady EC2 usage', ctx: ['finops', 'AWS'], action: 'spend', tier: 'commit_money', riskWord: 'Spends $1,800', state: 'needs', time: '22 min', act: 'Review', primary: true, id: 'p14 · aws.purchase_savings_plan' },
  { kind: 'approval', title: 'Post the monthly forecast to #incidents', why: 'Asked by a Slack user who isn’t one of your operators', ctx: ['billing-sre', 'Slack #incidents'], action: 'post', tier: 'propose', state: 'needs', time: '22 min', act: 'Review', id: 'p4 · slack-ops.post' },
  { kind: 'pr', title: 'Answer pr-review’s question on a draft review', why: 'Approve, revise or discard its review', ctx: ['pr-review', 'acme/infra#77'], action: 'post', tier: 'reversible', state: 'needs', time: '1 h', act: 'Answer', id: 'r-7ed1' },
  { kind: 'credential', title: 'Approve a sign-in on grafana.internal', why: 'The triage agent wants to read a dashboard', ctx: ['issue-triage', 'browser'], action: 'read', tier: 'observe', state: 'needs', time: '3 min', act: 'Open', id: 'b-02' },
];
export const FAILING = [
  { kind: 'run', title: 'ci-fixer couldn’t fix acme/web#1288', why: 'Its change made 2 tests fail. Nothing was pushed.', ctx: ['ci-fixer', 'acme/web#1288'], action: 'push', tier: 'propose', state: 'failed', time: '52 min', act: 'Open', id: 'r-7ef0' },
  { kind: 'run', title: 'release-notes stopped at its budget', why: 'It spent $0.05, its limit, before drafting', ctx: ['release-notes', 'acme/api'], action: 'post', tier: 'reversible', state: 'failed', time: '7 h', act: 'Open', id: 'r-7e64' },
];
export const RUNNING = [
  { kind: 'deploy', title: 'Rolling out api v2.14', why: 'Step 3 of 5: canary at 10%', ctx: ['deploy-bot', 'acme/api'], env: 'staging', action: 'apply', tier: 'propose', state: 'running', time: '4 min', id: 'r-7f40' },
  { kind: 'run', title: 'Fixing the failing check', why: 'Step 3 of 6: running the tests', ctx: ['ci-fixer', 'acme/api#482'], action: 'push', tier: 'propose', state: 'running', time: '6 min', id: 'r-7f3a' },
  { kind: 'incident', title: 'Investigating db-prod-1 disk usage', why: 'Reading metrics and logs for case c31', ctx: ['platform-sre', 'case c31'], env: 'production', action: 'read', tier: 'observe', state: 'running', time: '11 min', id: 'r-7f29' },
  { kind: 'pr', title: 'Reviewing acme/web#1291', why: 'Step 2 of 3: writing a draft review', ctx: ['pr-review', 'acme/web#1291'], action: 'post', tier: 'reversible', state: 'running', time: '2 min', id: 'r-7f2c' },
];
export const FINISHED = [
  { kind: 'pr', title: 'Reviewed acme/api#480', why: 'Left 3 comments, approved', ctx: ['pr-review', 'acme/api#480'], action: 'post', tier: 'reversible', state: 'passed', time: '38 min', id: 'r-7f11' },
  { kind: 'infra', title: 'Applied the Terraform plan to staging', why: 'Approved by ops: 2 changed, nothing destroyed', ctx: ['platform-sre', 'infra · envs/staging'], env: 'staging', action: 'apply', tier: 'propose', state: 'passed', time: '1 h', id: 'r-7ef2' },
  { kind: 'run', title: 'Triaged acme/api#486', why: 'Labelled it “bug” and “api”', ctx: ['issue-triage', 'acme/api#486'], action: 'write', tier: 'reversible', state: 'passed', time: '1 h', id: 'r-7ee3' },
  { kind: 'run', title: 'Bumped 3 dependencies in acme/web', why: 'Pushed to dep-bump/2026-10-16, tests pass', ctx: ['dep-bump', 'acme/web'], action: 'push', tier: 'propose', state: 'passed', time: '2 h', id: 'r-7ec8' },
  { kind: 'spend', title: 'Checked cloud spend', why: '$8.40 today, on track for the month', ctx: ['finops', 'AWS'], action: 'read', tier: 'observe', state: 'passed', time: '2 h', id: 'r-7eb9' },
  { kind: 'incident', title: 'Acknowledged the SEV2 page “api p99 latency”', why: 'Opened case c29 and paged nobody else', ctx: ['platform-sre', 'PagerDuty'], action: 'write', tier: 'reversible', state: 'passed', time: '3 h', id: 'r-7eb2' },
  { kind: 'run', title: 'Held ci-fixer on acme/api#471', why: 'The pull request has the hold label', ctx: ['ci-fixer', 'acme/api#471'], action: 'push', tier: 'propose', state: 'parked', time: '3 h', id: 'r-7ea0' },
  { kind: 'knowledge', title: 'Saved a runbook note on db-prod disks', why: 'From case c29, for next time', ctx: ['platform-sre', 'memory'], action: 'write', tier: 'reversible', state: 'passed', time: '4 h', id: 'k-311' },
  { kind: 'config', title: 'Raised ci-fixer’s budget to $2.00 a run', why: 'Applied with your passkey', ctx: ['ops', 'config'], action: 'apply', tier: 'propose', state: 'passed', time: '5 h', id: 'c-88' },
  { kind: 'connector', title: 'Refreshed the GitHub connector token', why: 'Expires in 30 days', ctx: ['conductor', 'GitHub'], action: 'write', tier: 'reversible', state: 'passed', time: '9 h', id: 'x-12' },
  { kind: 'infra', title: 'Planned Terraform for production', why: 'No changes', ctx: ['platform-sre', 'infra · envs/prod'], env: 'production', action: 'read', tier: 'observe', state: 'passed', time: '10 h', id: 'r-7e44' },
  { kind: 'deploy', title: 'Deployed web v3.8 to production', why: 'Approved by ops; healthy for 11 hours', ctx: ['deploy-bot', 'acme/web'], env: 'production', action: 'apply', tier: 'propose', state: 'passed', time: '11 h', id: 'r-7e38' },
];

// A bot's permissions: every verb it may call, what it does, where, its risk and who decides.
export const PERMS = {
  bot: 'platform-sre', role: 'Keeps production healthy for the platform team', team: 'platform',
  groups: [
    ['observe', 'Does on its own', 'Runs without asking.', [
      ['read', 'Reads metrics and logs', 'dd.query_*', '', 'observe'],
      ['read', 'Reads repositories and pull requests', 'gh.get_*', '', 'observe'],
      ['read', 'Plans Terraform', 'tf-plan.plan', 'production', 'observe'],
    ]],
    ['reversible', 'Does on its own once promoted', 'A proposal until you promote it; then it runs, sandboxed.', [
      ['write', 'Acknowledges pages', 'pd.ack', '', 'reversible', 'promoted'],
      ['write', 'Writes to the KV store', 'kv.set', 'staging', 'reversible'],
    ]],
    ['propose', 'Asks you first', 'Always a proposal; runs when an operator approves it.', [
      ['post', 'Comments on pull requests', 'gh.comment', '', 'propose'],
      ['post', 'Posts to #platform', 'slack-ops.post', '', 'propose'],
      ['apply', 'Applies Terraform to staging', 'tf-apply.apply', 'staging', 'propose'],
      ['apply', 'Applies Terraform to production', 'tf-apply.apply', 'production', 'propose'],
    ]],
    ['commit_money', 'Asks you, with an amount', 'A proposal with a typed amount, under its spend cap ($2,000 a purchase, $5,000 a month).', [
      ['spend', 'Buys savings plans', 'aws.purchase_savings_plan', '', 'commit_money', '', 'Spends up to $2,000'],
    ]],
    ['never', 'Never', 'Refused outright, whoever asks.', [
      ['perms', 'Changes IAM roles and policies', 'aws.iam_*', '', 'never', '', 'Grants access'],
      ['destroy', 'Deletes repositories', 'gh.delete_*', '', 'never', '', 'Destroys'],
    ]],
  ],
};

// The step picker: built-in steps and connector verbs, by what they do.
export const PICKER = [
  ['Read and decide', [
    ['action', 'read', 'Get a PR diff', 'GitHub · gh.pr_diff', 'observe'],
    ['action', 'read', 'Get failing check logs', 'GitHub · gh.check_logs', 'observe'],
    ['action', 'read', 'Query metrics', 'Datadog · dd.query', 'observe'],
    ['action', 'read', 'Plan Terraform', 'tf-plan · plan', 'observe'],
    ['step', 'decide', 'Decide', 'Typed questions, answered with probabilities', 'observe'],
  ]],
  ['Change things', [
    ['step', 'agent', 'AI agent', 'A prompt, a runtime and a model, in the sandbox', 'reversible'],
    ['step', 'command', 'Run a command', 'A shell command in the sandbox', 'reversible'],
    ['action', 'post', 'Comment on a pull request', 'GitHub · gh.comment', 'reversible'],
    ['action', 'push', 'Push to the pull request', 'GitHub · gh.push', 'propose'],
    ['action', 'post', 'Post to Slack', 'slack-ops · post', 'propose'],
  ]],
  ['Production, money and access', [
    ['action', 'apply', 'Apply Terraform', 'tf-apply · apply (production when the workspace is prod)', 'propose', 'production'],
    ['action', 'spend', 'Buy a savings plan', 'AWS · aws.purchase_savings_plan', 'commit_money'],
    ['action', 'destroy', 'Delete a branch', 'GitHub · gh.delete_branch', 'propose'],
    ['action', 'perms', 'Add a collaborator', 'GitHub · gh.collaborator_add', 'propose'],
  ]],
  ['Flow', [
    ['kind', 'approval', 'Ask a person', 'Waits for approve, revise or discard', 'observe'],
    ['kind', 'workflow', 'Call a workflow', 'Runs another workflow with inputs', 'observe'],
    ['step', 'wait', 'Wait', 'For a time or a condition', 'observe'],
  ]],
];
