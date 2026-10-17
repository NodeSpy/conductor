/* Demo scenarios: which fake day the prototype shows. Loaded after data.js, before store.js (whose seed is the
 * result), chosen per browser (localStorage cd-demo; More → Demo data):
 *   calm  (the default) a mostly healthy day: dozens of runs, one approval and one failure need you;
 *   quiet nothing needs you: every run fine, every system healthy (the calm state should say almost nothing);
 *   full  everything at once: every kind of exception, for exercising each flow (the test suite runs on this).
 * Neutral fake data only. */
(function () {
  const D = window.D;
  let mode = 'calm';
  try { mode = localStorage.getItem('cd-demo') || 'calm'; } catch (e) {}
  if (!['calm', 'quiet', 'full'].includes(mode)) mode = 'calm';
  D.demo = mode;
  if (mode === 'full') return;

  const now = Date.now(), m = (min) => new Date(now - min * 60000).toISOString();
  const settle = (id, status = 'executed') => { const p = D.proposals.find((x) => x.id === id); if (p && p.status === 'pending') Object.assign(p, { status, approver: 'ops (passkey · laptop)' }); };
  const finish = (id, extra = {}) => { const r = D.runs.find((x) => x.id === id); if (r) { Object.assign(r, { status: 'ok', error: undefined, failed_step: undefined, detail: undefined, finished: r.finished || new Date(new Date(r.started).getTime() + 9 * 60000).toISOString() }, extra); } };
  const markOk = (lane, runId) => { const l = D.score.find((x) => x.lane === lane); if (!l) return; l.marks.forEach((mk) => { if (!runId || mk[3] === runId) { if (mk[2] === 'wait') mk[1] = Math.max(0, mk[0] - 9); mk[2] = 'ok'; } }); };

  // a realistic day: dozens of passing runs across the lanes, spread over the last 12 hours
  const WF = [['pr-review', 'gh.review_requested', 'acme/web', 7], ['issue-triage', 'gh.issue_opened', 'acme/api', 1], ['pr-review', 'gh.review_requested', 'acme/api', 6], ['bot:platform-sre', 'sweep', '', 4], ['dep-bump', 'schedule', 'acme/api', 9], ['issue-triage', 'gh.issue_opened', 'acme/web', 1], ['ci-fixer', 'gh.check_failed', 'acme/api', 14]];
  const lane = (w) => (w.startsWith('bot:') ? w.slice(4) : w);
  for (let i = 0; i < 28; i++) {
    const [wf, trig, repo, dur] = WF[i % WF.length], start = 24 + i * 24 + (i % 3) * 5;
    if (start > 700) break;
    const id = 'r-6' + (900 - i * 7).toString(16).padStart(3, '0');
    D.runs.push({ id, workflow: wf, trigger: trig, repo, number: repo ? 1200 + i : 0, status: 'ok', started: m(start), finished: m(start - dur), cost: 0.05 + (i % 5) * 0.07, steps: [] });
    const l = D.score.find((x) => x.lane === lane(wf)); if (l) l.marks.push([start, start - dur, 'ok', id]);
  }
  D.runs.sort((a, b) => new Date(b.started) - new Date(a.started));

  // healthy systems
  D.connectors.forEach((c) => { if (c.status !== 'ok' && c.status !== 'idle') { c.status = 'ok'; c.detail = 'connected'; } });
  (D.vaults || []).forEach((v) => { if (v.status === 'fail') { v.status = 'ok'; v.detail = 'reachable'; } });
  (D.packs || []).forEach((p) => { if (p.status === 'wait') p.status = 'ok'; });
  (D.browsers || []).forEach((b) => { if (b.status === 'wait') b.status = 'run'; });
  D.daemon.paused = false;

  // only one approval and one failure need you; everything else was answered or went fine
  ['p11', 'p4', 'p13'].forEach((id) => settle(id));
  finish('r-7ed1'); markOk('pr-review', 'r-7ed1');
  finish('r-7e64'); markOk('release-notes', 'r-7e64');
  D.attention = D.attention.filter((a) => ['#/bots/platform-sre/proposals/p12', '#/runs/r-7ef0'].includes(a.href));
  D.activity = D.activity.filter((a) => a.s !== 'wait' && a.s !== 'idle');

  if (mode === 'quiet') {
    settle('p12', 'executed');
    finish('r-7ef0'); markOk('ci-fixer', 'r-7ef0');
    D.attention = [];
    D.activity = D.activity.filter((a) => a.s !== 'fail' && a.s !== 'ember');
    D.cases.forEach((c) => { c.urgent = false; });
  }
})();
