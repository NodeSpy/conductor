/* A run's record, the shape of conductor's RunHistory/StepRecord: every step of
 * the workflow with its status, start offset and duration, inputs, outputs,
 * its own log lines and (agent steps only) the diff it made. Derived once per
 * run from the workflow's step graph and a per-workflow template, so the run
 * detail, the compare view and the audit all tell the same story. Usage
 * figures (tokens, dollars) stay out of outputs unless usage display is on. */
(function () {
  const MIN = 60000;
  const tests = {
    fail: ['--- FAIL: TestCheckout (0.31s)', '    checkout_test.go:142: total = 10.49, want 10.50', '--- FAIL: TestRefund (0.02s)', '    refund_test.go:88: got 1999, want 2000', 'FAIL acme/web/internal/billing 0.412s'],
    ok: ['ok   acme/web/internal/billing 0.398s', 'ok   acme/web/internal/cart 0.211s', 'ok   acme/web/internal/api 1.022s'],
  };
  const diffs = {
    refund: { file: 'internal/billing/refund.go', at: 41, ctx: 'func applyFee(cents int64, bps int64) int64 {', del: ['\treturn cents * bps / 10000'], add: ['\treturn roundHalfEven(cents*bps, 10000)'] },
    tax: { file: 'internal/cart/tax.go', at: 18, ctx: 'func lineTax(c Line) int64 {', del: ['\treturn c.Cents * c.RateBps / 10000'], add: ['\treturn (c.Cents*c.RateBps + 5000) / 10000 // round half up, as the tests expect'] },
    gomod: { file: 'go.mod', at: 7, ctx: 'require (', del: ['\tgithub.com/go-chi/chi/v5 v5.0.12', '\tgolang.org/x/net v0.27.0'], add: ['\tgithub.com/go-chi/chi/v5 v5.1.0', '\tgolang.org/x/net v0.30.0'] },
  };
  /* Per workflow, per step: weight (seconds before scaling), share of the run's agent spend, and what it saw and did.
     ok = the step succeeded; ctx = { r, fail } (the run failed at this step when fail). */
  const T = {
    'ci-fixer': {
      logs: { w: 4, in: (r) => ({ repo: r.repo, number: r.number, checks: 'failing' }), out: () => ({ failing: ['unit'], bytes: '412 KiB' }), log: () => [['ok', 'fetched 1 failing check (unit) · 412 KiB']] },
      triage: { w: 8, cost: 0.03, in: () => ({ question: 'What kind of failure is this?', options: ['test_failure', 'flaky', 'infra'] }), out: () => ({ choice: 'test_failure', confidence: 0.91 }), log: () => [['info', 'decide: kind=test_failure p=0.91 · flaky=0.06']] },
      fix: { w: 420, cost: 0.9, in: (r) => ({ runtime: 'paseo', model: 'fleet fixer → claude-sonnet-5', sandbox: 'namespace', identity: `agent:fixer@${r.repo}`, prompt: 'Fix the failing check. Keep the change minimal; run the tests before you finish.' }),
        out: (r) => (r.id === 'r-7e40' ? { summary: 'lineTax truncated; the tests expect round-half-up. Changed it and ran the cart tests.', commits: 1, files_changed: 1 } : { summary: 'applyFee truncated refunds; changed it to round half-even. Ran the billing tests in the sandbox.', commits: 1, files_changed: 1 }),
        log: (r) => (r.id === 'r-7e40' ? [['info', 'claude-sonnet-5 (fleet fixer, rung 1) · sandbox namespace'], ['tool', '▸ Read internal/cart/tax.go'], ['err', '--- FAIL: TestLineTax tax_test.go:31: got 104, want 105'], ['tool', '▸ Edit internal/cart/tax.go (+1 -1)'], ['tool', '▸ Bash go test ./internal/cart/...'], ['ok', 'ok  acme/web/internal/cart 0.211s'], ['tool', '▸ Bash git commit -m "cart: round line tax half up"']]
          : D.liveLog.filter(([s]) => s === 'fix').map(([, k, t]) => [k, t]).filter(([, t]) => !/egress denied/.test(t))),
        diff: (r) => (r.id === 'r-7e40' ? diffs.tax : diffs.refund) },
      test: { w: 26, in: (r, s) => ({ use: 'cli', command: ['go', 'test', './...'], workdir: `<state>/runs/${r.id}/${s}` }), out: (r, c) => (c.fail ? { exit_code: 1, stdout: tests.fail.join('\n') } : { exit_code: 0, stdout: tests.ok.join('\n') }), log: (r, c) => (c.fail ? tests.fail.map((l) => [/FAIL/.test(l) ? 'err' : 'info', l]) : tests.ok.map((l) => ['ok', l])) },
      push: { w: 3, in: (r) => ({ branch: `conductor/fix-${r.number}` }), out: (r) => ({ ref: `refs/heads/conductor/fix-${r.number}`, fast_forward: true }), log: (r) => [['info', `git.push → refs/heads/conductor/fix-${r.number} (fast-forward, re-signed)`]] },
      comment: { w: 2, in: () => ({ body: 'Pushed a fix for the failing unit check; tests pass.' }), out: (r) => ({ url: `https://github.com/${r.repo}/pull/${r.number}#issuecomment-1` }), log: () => [['ok', 'gh.comment posted']] },
    },
    'pr-review': {
      fetch: { w: 3, in: (r) => ({ repo: r.repo, number: r.number }), out: () => ({ files: 7, additions: 214, deletions: 61 }), log: () => [['ok', 'gh.pr_diff · 7 files · +214 -61']] },
      lint: { w: 40, in: (r, s) => ({ use: 'cli', command: ['golangci-lint', 'run'], workdir: `<state>/runs/${r.id}/${s}` }), out: () => ({ exit_code: 0, stdout: '0 issues.' }), log: () => [['ok', 'golangci-lint run · 0 issues.']] },
      review: { w: 300, cost: 1, in: () => ({ runtime: 'paseo', model: 'fleet reviewer → claude-sonnet-5', prompt: 'Review the diff for correctness, queries and tests.' }), out: (r, c) => (c.wait ? { handoff: 'awaiting answer on hoff', choices: ['approve', 'revise', 'discard'] } : { verdict: 'request_changes', comments: 3, summary: 'Two N+1 queries in the orders handler; missing index on (tenant_id, created_at).' }), log: (r, c) => [['info', 'claude-sonnet-5 (fleet reviewer, rung 1)'], ['tool', '▸ Read handlers/orders.go'], ['tool', '▸ Grep "SELECT" handlers/'], c.wait ? ['warn', 'hand-off: waiting for an answer on hoff'] : ['info', 'Found 2 N+1 queries; drafting 3 comments.']] },
      post: { w: 2, in: () => ({ event: 'REQUEST_CHANGES', comments: 3 }), out: (r) => ({ review: `https://github.com/${r.repo}/pull/${r.number}#pullrequestreview-1` }), log: () => [['ok', 'gh.review posted as the bot']] },
    },
    'issue-triage': {
      classify: { w: 40, cost: 1, in: (r) => ({ repo: r.repo, issue: r.number, prompt: 'Label the issue and set a priority.' }), out: () => ({ labels: ['bug', 'area/billing'], priority: 'p2' }), log: () => [['info', 'claude-haiku-4-5 · labels bug, area/billing · p2']] },
      label: { w: 2, in: () => ({ add: ['bug', 'area/billing', 'p2'] }), out: () => ({ added: 3 }), log: () => [['ok', 'gh.label · 3 labels']] },
    },
    'dep-bump': {
      bump: { w: 480, cost: 1, in: (r) => ({ repo: r.repo, prompt: 'Bump minor versions of direct dependencies; run the tests.' }), out: () => ({ bumped: ['github.com/go-chi/chi/v5 v5.1.0', 'golang.org/x/net v0.30.0'], tests: 'pass' }), log: () => [['tool', '▸ Bash go get -u=patch ./...'], ['tool', '▸ Bash go test ./...'], ['ok', 'ok  (12 packages)']], diff: () => diffs.gomod },
      notify: { w: 2, in: () => ({ channel: '#deps', text: 'dep-bump: 2 modules bumped on acme/web; tests pass.' }), out: () => ({ ts: '1760600000.000100' }), log: () => [['ok', 'slack-ops.post #deps']] },
    },
    'release-notes': {
      draft: { w: 100, cost: 1, in: (r) => ({ repo: r.repo, prompt: 'Draft release notes from the merged PRs.' }), out: (r, c) => (c.fail ? { error: r.error } : { notes: '…' }), log: (r, c) => [['info', 'claude-sonnet-5 · reading 23 merged PRs'], c.fail ? ['err', String(r.error)] : ['ok', 'drafted']] },
      post: { w: 2, in: () => ({ channel: '#releases' }), out: () => ({ ts: '1760600000.000200' }), log: () => [['ok', 'slack-ops.post #releases']] },
    },
  };
  const generic = { w: 60, cost: 1, in: () => ({}), out: (r, c) => (c.fail ? { error: r.error || 'failed' } : { summary: r.detail || 'done' }), log: (r, c) => [[c.fail ? 'err' : 'ok', c.fail ? r.error || 'failed' : r.detail || 'done']] };

  const graph = (r) => { const wf = D.workflows.find((w) => w.name === r.workflow); const g = wf && wf.steps && wf.steps.length ? wf.steps : wf && wf.yaml ? S.deriveGraph(wf.yaml) : null; return g && g.length ? g : (r.steps.length ? r.steps : [{ id: 'work' }]).map((s, i, a) => ({ id: s.id, kind: 'agent', needs: i ? [a[i - 1].id] : [] })); };
  const cache = new Map();

  /** UI.runRecord(r) → { steps: [{ id, kind, what, status, t0, ms, cost, inputs, outputs, log, diff }], total, live }. */
  UI.runRecord = (r) => {
    const key = r.id + ':' + r.status + ':' + (r.steps || []).map((s) => s.status).join(','); if (cache.has(key)) return cache.get(key);
    const g = graph(r), tpl = T[r.workflow] || {}, fail = r.status === 'fail' || r.status === 'stopped';
    const failAt = fail ? r.failed_step || (r.steps.find((s) => s.status === 'fail' || s.status === 'stopped') || {}).id || g[g.length - 1].id : null;
    const waitAt = r.status === 'wait' ? (g.find((s) => s.kind === 'agent') || g[0]).id : null;
    const order = g.map((s) => s.id), idx = (id) => order.indexOf(id);
    const statusOf = (s) => { const live = (r.steps || []).find((x) => x.id === s.id); if (live) return live.status; if (r.status === 'idle') return 'idle'; if (r.status === 'ok') return 'ok';
      const stop = failAt || waitAt; if (s.id === stop) return failAt ? (r.status === 'stopped' ? 'stopped' : 'fail') : 'wait'; return idx(s.id) < idx(stop) ? 'ok' : 'idle'; };
    // timing: weights along the graph, scaled so the critical path spans the run
    const end = {}, t0 = {}, w = {};
    g.forEach((s) => { const t = tpl[s.id] || generic, live = (r.steps || []).find((x) => x.id === s.id); w[s.id] = live && live.duration_ms ? live.duration_ms / 1000 : t.w; });
    g.forEach((s) => { t0[s.id] = Math.max(0, ...(s.needs || []).map((n) => end[n] || 0)); end[s.id] = t0[s.id] + w[s.id]; });
    const ran = g.filter((s) => statusOf(s) !== 'idle'); const span = Math.max(1, ...ran.map((s) => end[s.id]));
    const total = r.finished ? new Date(r.finished) - new Date(r.started) : Math.max(MIN, Date.now() - new Date(r.started));
    const k = r.finished ? total / (span * 1000) : 1; // an unfinished run: finished steps keep their own time; the current one runs till now
    const agentW = ran.reduce((a, s) => a + ((tpl[s.id] || generic).cost || 0) * w[s.id], 0) || 1;
    const steps = g.map((s) => { const t = tpl[s.id] || generic, status = statusOf(s), c = { fail: status === 'fail' || status === 'stopped', wait: status === 'wait' };
      const reached = status !== 'idle', running = status === 'run' || status === 'wait';
      const kind = s.kind === 'uses' ? 'verb' : s.kind || 'agent', what = s.uses || (kind === 'code' ? `use: cli · ${[].concat(s.command || ((t.in(r, s.id) || {}).command) || []).join(' ')}` : kind === 'decide' ? 'decide' : kind === 'agent' ? `${s.runtime || 'paseo'} · ${s.model || 'default'}` : kind);
      const ms = reached ? Math.round((running ? Math.max(0, total - t0[s.id] * k * 1000) : w[s.id] * k * 1000)) : 0;
      return { id: s.id, kind, what, needs: s.needs || [], status, t0: reached ? Math.round(t0[s.id] * k * 1000) : null, ms, cost: reached && t.cost ? +(r.cost * (t.cost * w[s.id]) / agentW).toFixed(3) : 0,
        inputs: reached || status === 'idle' ? t.in(r, s.id) : null, outputs: reached && !running ? t.out(r, c) : c.wait ? t.out(r, c) : null, log: reached ? t.log(r, c) : [], diff: reached && t.diff ? t.diff(r) : null };
    });
    const rec = { steps, total, failAt, waitAt };
    cache.set(key, rec); return rec;
  };
  /** A step's outputs as YAML, with usage figures only when usage display is on. */
  UI.stepOutputsYAML = (r, s) => { if (!s.outputs) return ''; const o = Object.assign({}, s.outputs); if (UI.usageOn() && s.cost) Object.assign(o, { cost_usd: s.cost }); return YAML.stringify(o); };
  /** A diff object as the shared diff markup. */
  UI.stepDiffHTML = (d) => { const E = UI.esc; let a = d.at, b = d.at; const rows = [`<div class="dl"><span>${a}</span><span>${b}</span><span> </span><span>${E(d.ctx)}</span></div>`]; a++; b++;
    d.del.forEach((l) => rows.push(`<div class="dl del"><span>${a++}</span><span></span><span>-</span><span>${E(l)}</span></div>`)); d.add.forEach((l) => rows.push(`<div class="dl add"><span></span><span>${b++}</span><span>+</span><span>${E(l)}</span></div>`));
    return `<div class="diff"><div class="h">${E(d.file)} · <span class="muted">${d.add.length ? '+' + d.add.length : ''} ${d.del.length ? '−' + d.del.length : ''}</span></div>${rows.join('')}</div>`; };
  /** The run's target, never a fake "#0". */
  UI.runTarget = (r) => (r.repo ? r.repo + (r.number ? '#' + r.number : '') : '—');
  /** Audit lines that belong to this run, plus the record's own events. */
  UI.runAudit = (r) => { const rec = UI.runRecord(r), at = (ms) => new Date(new Date(r.started).getTime() + ms).toISOString();
    const own = D.audit.filter((a) => a.run === r.id || a.who === r.id || new RegExp('\\b' + r.id + '\\b').test(a.detail || ''));
    const syn = [{ ts: r.started, event: 'run_started', who: r.id, detail: `${r.workflow} · ${r.trigger}${r.repo ? ' · ' + UI.runTarget(r) : ''}` }];
    rec.steps.filter((s) => s.kind === 'verb' && s.status === 'ok').forEach((s) => syn.push({ ts: at(s.t0 + s.ms), event: 'verb', who: r.id, detail: `${s.what}${r.repo ? ' ' + UI.runTarget(r) : ''} · step ${s.id}` }));
    rec.steps.filter((s) => s.diff && s.status === 'ok').forEach((s) => syn.push({ ts: at(s.t0 + s.ms), event: 'agent_commit', who: r.id, detail: `step ${s.id} · ${s.diff.file}` }));
    if (r.status === 'ok' && r.finished) syn.push({ ts: r.finished, event: 'workflow_done', who: r.id, detail: `${r.workflow} · ${rec.steps.length} steps` });
    const seen = new Set(own.map((a) => a.event)); return own.concat(syn.filter((a) => !(a.event === 'workflow_failed' && seen.has(a.event)))).sort((a, b) => new Date(b.ts) - new Date(a.ts)); };
  /** "What happened / What you can do" for a run, in words. */
  UI.runStory = (r) => { const rec = UI.runRecord(r), f = rec.steps.find((s) => s.id === rec.failAt);
    if (r.workflow === 'ci-fixer' && r.status === 'fail') return { tone: 'danger', happened: `The check that started this run failed in <b>TestRefund</b>. The <b>fix</b> step changed rounding in <span class="mono">internal/billing/refund.go</span> (truncate → round half-even). Then <b>test</b> ran <span class="mono">go test ./...</span>: TestRefund still fails by one cent, and <b>TestCheckout</b> now fails too. The fix made it worse, so nothing was pushed.`,
      can: [['run-pick', 'fix', 'diff', 'See what the fix changed'], ['retry', 'fix', '', 'Retry from fix'], ['run-cmp', 'r-7e40', '', 'Compare with the last good ci-fixer run'], ['run-dismiss', '', '', 'Dismiss from Needs you']] };
    if (r.workflow === 'release-notes' && r.status === 'fail') return { tone: 'danger', happened: `The <b>draft</b> step hit the trigger's budget (<span class="mono">policy.budget.max_cost_usd: 0.05</span>) and was shed, so nothing was posted.`, can: [['link', '#/workflows/release-notes', '', 'Raise the budget in the workflow'], ['retry', 'draft', '', 'Retry from draft'], ['run-dismiss', '', '', 'Dismiss from Needs you']] };
    if (r.status === 'fail' || r.status === 'stopped') return { tone: 'danger', happened: `${r.status === 'stopped' ? 'Stopped' : 'Failed'} at step <b>${UI.esc(rec.failAt || '')}</b>${f && f.log.length ? ': ' + UI.esc(f.log[f.log.length - 1][1]) : ''}.`, can: [['retry', rec.failAt, '', 'Retry from ' + rec.failAt], ['run-dismiss', '', '', 'Dismiss from Needs you']] };
    if (r.status === 'wait') return { tone: 'wait', happened: `Waiting at step <b>${UI.esc(rec.waitAt)}</b>: ${UI.esc(r.detail || 'waiting for an answer')}.`, can: [['link', '#/attention', '', 'Answer it in Needs you']] };
    if (r.status === 'idle') return { tone: 'info', happened: `Parked before any step ran: ${UI.esc(r.detail || 'held')}. Remove the hold and it runs.`, can: [['retry', '$start', '', 'Run it now']] };
    return null; };
})();
