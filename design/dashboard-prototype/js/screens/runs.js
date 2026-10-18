/* Activity (every run, grouped by who needs to act; Run history merged in), the run page, compare, and Needs you. */
(function () {
  const { icon, st, tag, usd, ago, dur, esc } = UI;
  const now = () => new Date().toISOString();

  const graphOf = (run) => { const wf = D.workflows.find((w) => w.name === run.workflow); if (wf && wf.steps && wf.steps.length) return wf.steps; if (wf && wf.yaml) return S.deriveGraph(wf.yaml); return run.steps.map((s, i) => ({ id: s.id, kind: 'agent', needs: i ? [run.steps[i - 1].id] : [] })); };

  UI.act('stop-run', (b) => UI.confirm({ title: 'Stop this run?', body: 'It stops mid-step; anything already pushed stays. You can retry it from any step.', ok: 'Stop run', danger: true }).then((ok) => {
    if (!ok) return; const r = D.runs.find((x) => x.id === b.dataset.run); r.status = 'stopped'; r.finished = now(); r.error = 'stopped by operator ops'; r.steps.forEach((s) => { if (s.status === 'run') s.status = 'stopped'; });
    D.audit.unshift({ ts: now(), event: 'run_stop', who: 'operator:ops', detail: `${r.id} · ${r.workflow} · grace 10s`, run: r.id }); S.save(); UI.refreshChrome(); UI.go('#/runs/' + r.id); UI.toast(`Stopped ${r.id}`, 'retry it from any step', 'fail');
  }));
  UI.act('restart-step', (b) => { const r = D.runs.find((x) => x.id === b.dataset.run); UI.confirm({ title: `Restart ${esc(b.dataset.from)}?`, body: `This stops ${r.id} first (the step gets a short grace to checkpoint), then starts a retry from <b>${esc(b.dataset.from)}</b>. Steps before it are replayed from their recorded outputs; what this step did so far is discarded.`, ok: 'Stop and restart', cancel: 'Keep it running', danger: true }).then((ok) => { if (!ok) return;
    r.status = 'stopped'; r.finished = now(); r.failed_step = b.dataset.from; r.error = 'stopped by operator:ops to restart ' + b.dataset.from; r.steps.forEach((s) => { if (s.status === 'run') s.status = 'stopped'; }); D.audit.unshift({ ts: now(), event: 'run_stop', who: 'operator:ops', detail: `${r.id} · ${r.workflow} · grace 10s · restart ${b.dataset.from}`, run: r.id });
    const steps = graphOf(r), from = steps.findIndex((s) => s.id === b.dataset.from), id = 'r-' + Math.random().toString(16).slice(2, 6);
    D.runs.unshift({ id, workflow: r.workflow, trigger: r.trigger + ' (retry)', repo: r.repo, number: r.number, status: 'run', started: now(), cost: 0, retry_of: r.id, steps: steps.map((s, i) => ({ id: s.id, status: i < from ? 'ok' : i === from ? 'run' : 'idle', duration_ms: i < from ? 900 : 0, cost_usd: 0, started: i === from ? now() : undefined })) });
    D.audit.unshift({ ts: now(), event: 'run_retry', who: 'operator:ops', detail: `${id} · retry_of ${r.id} · from ${b.dataset.from}`, run: id }); S.save(); UI.refreshChrome(); UI.go('#/runs/' + id); UI.toast(`Restarted ${b.dataset.from}`, `${r.id} stopped · ${id} running`, 'run'); }); });
  UI.act('retry', (b) => {
    const r = D.runs.find((x) => x.id === b.dataset.run), steps = graphOf(r);
    UI.form({ caps: `${esc(r.workflow.replace(/^bot:/, ''))}${r.repo ? ' on ' + UI.runTarget(r) : ''}`, title: 'Retry from a step', intro: 'Re-runs the pinned trigger. Steps before the chosen one are replayed from their recorded outputs; nothing they did is repeated.',
      fields: [{ k: 'from', type: 'enum', label: 'Resume from', req: true, options: steps.map((s) => [s.id, s.id + (s.id === (r.failed_step || b.dataset.from) ? ' (selected)' : '')]).concat([['$start', 'the beginning']]), def: b.dataset.from || r.failed_step || (steps[0] || {}).id }, { k: 'force', type: 'bool', label: 'Force replay', on: 'also re-run steps that already succeeded' }],
      submit: 'Retry', submitIcon: 'play',
      onSubmit: (v) => {
        const id = 'r-' + Math.random().toString(16).slice(2, 6); const from = v.from === '$start' ? 0 : steps.findIndex((s) => s.id === v.from);
        D.runs.unshift({ id, workflow: r.workflow, trigger: r.trigger + ' (retry)', repo: r.repo, number: r.number, status: 'run', started: now(), cost: 0, retry_of: r.id, steps: steps.map((s, i) => ({ id: s.id, status: i < from && !v.force ? 'ok' : i === from ? 'run' : 'idle', duration_ms: i < from ? 900 : 0, cost_usd: 0, started: i === from ? now() : undefined })) });
        D.audit.unshift({ ts: now(), event: 'run_retry', who: 'operator:ops', detail: `${id} · retry_of ${r.id} · from ${v.from}${v.force ? ' · force_replay' : ''}`, run: id });
        S.save(); UI.refreshChrome(); UI.go('#/runs/' + id);
      } });
  });

  /* ---------- Activity: every run in one place (Run history merged in), grouped by who needs to act ----------
   * As approved (explore/status-column.html, the D+A combination, every row drawn): "Needs attention · N" (failed
   * and needs-you runs, each with its reason in a sentence and one action) → "Running now" → "Finished" (paged).
   * Above it, runs per hour for the chosen range (the Overview's strip: the same size for 10 workflows or 500), and
   * the filters, the time range and search on the same page, as far back as you need. */
  const PAGE = 20;
  const RANGES = [['60', 'Last hour'], ['720', 'Last 12 hours'], ['1440', 'Last 24 hours'], ['10080', 'Last 7 days'], ['', 'All time']];
  const av = { q: '', status: '', wf: '', bot: '', repo: '', since: '720', sel: new Set(), last: null, limit: PAGE };
  UI.activityState = av;
  const GROUP = (r) => (['fail', 'stopped', 'wait'].includes(r.status) ? 'attn' : r.status === 'run' ? 'run' : 'done');
  const botOf = (r) => (String(r.workflow).startsWith('bot:') ? r.workflow.slice(4) : '');
  const actMatch = (r) => {
    if (!UI.scopeKeeps(UI.botOfRun(r))) return false;
    if (av.status) { const st = r.status === 'stopped' ? 'fail' : r.status; if (av.status === 'attn' ? GROUP(r) !== 'attn' : st !== av.status) return false; }
    if (av.wf && r.workflow !== av.wf) return false;
    if (av.bot && botOf(r) !== av.bot) return false;
    if (av.repo && r.repo !== av.repo) return false;
    if (av.since && r.status !== 'run' && GROUP(r) !== 'attn' && Date.now() - new Date(r.started) > +av.since * 60000) return false;
    if (av.q) { const t = `${r.id} ${r.workflow} ${r.repo}#${r.number} ${r.error || ''} ${r.detail || ''} ${r.trigger}`.toLowerCase(); if (!t.includes(av.q.toLowerCase())) return false; }
    return true;
  };
  UI.activityMatch = actMatch;
  const target = (r) => (r.repo ? `${r.repo}${r.number ? '#' + r.number : ''}` : botOf(r) ? (/sweep/.test(r.trigger) ? 'routine sweep' : /incident|pd\./.test(r.trigger) ? 'PagerDuty incident' : /spawn/.test(r.trigger) ? 'a task it started itself' : 'its own work') : esc(r.trigger));
  const reason = (r) => { if (r.status === 'wait') return `Waiting for your answer: ${esc(String(r.detail || 'approve, revise or discard its draft').replace(/^hand-off[^:]*:\s*/, '').replace(/^awaiting your answer\s*\(?/, '').replace(/\)$/, ''))}.`;
    const e = UI.plainError ? UI.plainError(r) : r.error || ''; return `${r.failed_step ? `Failed at “${esc(r.failed_step)}”` : 'Failed'}${e ? `: ${esc(e)}` : ''}${/\.$/.test(e) ? '' : '.'}`; };
  const runName = (r) => `<b>${esc(r.workflow.replace(/^bot:/, ''))}</b> <span class="tgt">${target(r)}</span>`;
  const statusCell = (r) => UI.statusMark(UI.runS(r.status), UI.runWord(r.status));
  const actBtn = (r) => (r.status === 'wait' ? `<a class="btn sm secondary" href="#/runs/${r.id}">Answer</a>` : `<a class="btn sm secondary" href="#/runs/${r.id}">Open</a>`);
  const edge = (r) => (r.status === 'wait' ? 'ember' : 'fail');
  const cbx = (r) => `<input type="checkbox" class="cbx" data-act="act-sel-row" data-id="${r.id}" aria-label="Select ${esc(r.workflow)} ${r.id}"${av.sel.has(r.id) ? ' checked' : ''}>`;
  function groups() {
    const all = D.runs.filter(actMatch).sort((x, y) => new Date(y.started) - new Date(x.started));
    return { all, attn: all.filter((r) => GROUP(r) === 'attn'), run: all.filter((r) => GROUP(r) === 'run'), done: all.filter((r) => GROUP(r) === 'done') };
  }
  const cols = () => 6 + (UI.usageOn() ? 1 : 0);
  function tableBody(G) {
    const row = (r, rich) => `<tr class="${rich ? 'attn' : ''}" data-href="#/runs/${r.id}"${rich ? ` data-s="${edge(r)}"` : ''}>
      <td class="sel">${cbx(r)}</td>
      <td class="run">${runName(r)}${rich ? `<span class="reason">${reason(r)}</span>` : ''}</td>
      <td class="st-c">${statusCell(r)}</td>
      <td class="when nowrap">${UI.agoText(r.started)}</td>
      <td class="num">${r.status === 'run' || r.status === 'wait' ? `<span class="dl-sub">${UI.runningFor(r.started)} so far</span>` : UI.runDur(r)}</td>
      ${UI.usageOn() ? `<td class="num">${UI.runFig(r)}</td>` : ''}
      <td class="mono id">${r.id}</td>
      <td class="act">${rich ? actBtn(r) : ''}</td></tr>`;
    const head = (cls, title, n) => `<tr class="gh"><td colspan="${cols() + 1}"><b>${title}</b><span class="gn">${n}</span></td></tr>`;
    const shown = G.done.slice(0, av.limit), more = G.done.length - shown.length;
    if (!G.all.length) return `<tbody><tr><td colspan="${cols() + 1}" class="dl-empty-row">No run matches these filters. <button class="link" data-act="act-clear">Clear filters</button></td></tr></tbody>`;
    return `${G.attn.length ? `<tbody class="g-attn">${head('g-attn', 'Needs attention', G.attn.length)}${G.attn.map((r) => row(r, true)).join('')}</tbody>` : ''}
      ${G.run.length ? `<tbody class="g-live">${head('g-live', 'Running now', G.run.length)}${G.run.map((r) => row(r)).join('')}</tbody>` : ''}
      ${G.done.length ? `<tbody class="g-done">${head('g-done', 'Finished', G.done.length)}${shown.map((r) => row(r)).join('')}${more > 0 ? `<tr class="more"><td colspan="${cols() + 1}"><button class="btn sm secondary" data-act="act-more" id="act-more">Show ${Math.min(PAGE, more)} more (${G.done.length} in all)</button></td></tr>` : ''}</tbody>` : ''}`;
  }
  function phoneBody(G) {
    const li = (r, rich) => `<li class="${rich ? 'attn' : ''}"${rich ? ` data-s="${edge(r)}"` : ''}>${cbx(r)}<a href="#/runs/${r.id}"><span class="dl-nt"><b>${runName(r)}</b>${rich ? `<span class="reason">${reason(r)}</span>` : ''}${statusCell(r)}</span><span class="dl-age">${UI.agoText(r.started).replace(' ago', '')}</span></a></li>`;
    const grp = (t, xs, rich, total = xs.length) => (xs.length ? `<h3 class="ph-g">${t}<span>${total}</span></h3><ul class="dl-list dl-runs-phone">${xs.map((r) => li(r, rich)).join('')}</ul>` : '');
    const shown = G.done.slice(0, av.limit), more = G.done.length - shown.length;
    return grp('Needs attention', G.attn, true) + grp('Running now', G.run) + grp('Finished', shown, false, G.done.length) + (more > 0 ? `<button class="btn sm secondary" data-act="act-more">Show ${Math.min(PAGE, more)} more (${G.done.length} in all)</button>` : '');
  }
  const theadHTML = (G) => (av.sel.size ? `<thead class="selecting"><tr><th colspan="${cols() + 1}">${selBar(G)}</th></tr></thead>`
    : `<thead><tr><th class="sel"><input type="checkbox" class="cbx" data-act="act-sel-all" aria-label="Select every run shown"></th><th>Run</th><th>Status</th><th>When</th><th class="num">Took</th>${UI.usageOn() ? `<th class="num">${UI.measHead()}</th>` : ''}<th class="id-h">ID</th><th></th></tr></thead>`);
  function selBar(G) {
    const sel = D.runs.filter((r) => av.sel.has(r.id)), retry = sel.filter((r) => r.status === 'fail' || r.status === 'stopped').length, stop = sel.filter((r) => r.status === 'run').length;
    return UI.bulkBar(sel.length, 'run', [[`Retry${retry && retry < sel.length ? ` ${retry}` : ''}…`, 'undo', 'act-bulk-retry', '', retry ? '' : 'None of the selected runs failed'], [`Stop${stop && stop < sel.length ? ` ${stop}` : ''}…`, 'x', 'act-bulk-stop', '', stop ? '' : 'None of the selected runs is running']], 'act-sel-clear', { all: G.all.length, allAct: 'act-sel-matching' });
  }
  /** Runs per hour for the chosen range: an aggregate strip, failures stacked in the danger colour and named below. */
  function strip(G) {
    const span = +av.since || 10080, n = span <= 60 ? 12 : span <= 1440 ? Math.round(span / 60) : 7, bucket = span / n, w = 960, h = 96;
    const per = Array.from({ length: n }, () => [0, 0]);
    G.all.forEach((r) => { const m = (Date.now() - new Date(r.started)) / 60000; if (m > span) return; const i = Math.min(n - 1, Math.max(0, n - 1 - Math.floor(m / bucket))); if (r.status === 'fail' || r.status === 'stopped') per[i][1]++; else per[i][0]++; });
    const mx = Math.max(4, ...per.map(([a, b]) => a + b)), bw = w / n - 6;
    const bars = per.map(([ok, f], i) => { const x = i * (w / n) + 3, ho = (ok / mx) * (h - 22), hf = (f / mx) * (h - 22); return `<rect x="${x}" y="${h - 18 - ho}" width="${bw}" height="${Math.max(ho - (hf ? 2 : 0), 0)}" rx="2" class="hb-ok"/>${f ? `<rect x="${x}" y="${h - 18 - ho - hf}" width="${bw}" height="${Math.max(0, hf - 1)}" rx="2" class="hb-fail"/>` : ''}`; }).join('');
    const label = RANGES.find(([v]) => v === av.since)[1].toLowerCase().replace('last ', ''), unit = span <= 60 ? '5 minutes' : span <= 1440 ? 'hour' : 'day';
    const fails = G.all.filter((r) => (r.status === 'fail' || r.status === 'stopped') && (Date.now() - new Date(r.started)) / 60000 <= span);
    return `<section class="dl-card dl-strip"><header class="dl-ch"><h2>Runs per ${unit}</h2><span class="dl-sub">${av.since ? `the last ${label}` : 'the last 7 days'}</span></header>
      <svg class="dl-hours" viewBox="0 0 ${w} ${h}" preserveAspectRatio="none" role="img" aria-label="Runs per ${unit}: ${fails.length} failed">${bars}<line x1="0" x2="${w}" y1="${h - 17}" y2="${h - 17}" class="hb-axis"/></svg><div class="dl-axis"><span>${av.since ? `${label.replace(/^hour$/, '1 hour')} ago` : '7 days ago'}</span><span>now</span></div>
      ${fails.length ? `<p class="dl-foot-note">${UI.mark('fail')}<span>${fails.length} failed: ${fails.slice(0, 4).map((r) => `<a class="dl-link" href="#/runs/${r.id}">${esc(r.workflow.replace(/^bot:/, ''))} at ${UI.failedAt(r)}</a>`).join(', ')}${fails.length > 4 ? ` and ${fails.length - 4} more` : ''}</span></p>` : `<p class="dl-foot-note">${UI.mark('ok')}Nothing failed.</p>`}</section>`;
  }
  const chip = (key, icon_, label, opts) => `<label class="dl-chip${av[key] ? ' on' : ''}">${icon(icon_)}<span class="sr-only">${label}</span><select data-on-change="act-f" data-k="${key}" aria-label="${label}">${opts.map(([v, l]) => `<option value="${esc(v)}"${String(av[key]) === String(v) ? ' selected' : ''}>${esc(l)}</option>`).join('')}</select></label>`;
  function activityPage() {
    const G = groups(), wfs = [...new Set(D.runs.map((r) => r.workflow).filter((w) => !w.startsWith('bot:')))].sort(), bots = [...new Set(D.runs.map(botOf).filter(Boolean))].sort();
    return `<div class="page dl-page">
      <div class="dl-phead"><div><h1>Activity</h1></div></div>
      <div class="dl-filters" id="act-filters">
        <label class="dl-search">${icon('search')}<input value="${esc(av.q)}" placeholder="Search repo, PR, run or error" data-on-input="act-q" aria-label="Search runs"></label>
        ${chip('status', 'filter', 'Status', [['', 'Any status'], ['attn', 'Needs attention'], ['run', 'Running'], ['fail', 'Failed'], ['wait', 'Needs you'], ['ok', 'Passed'], ['idle', 'Parked']])}
        ${chip('wf', 'workflows', 'Workflow', [['', 'Any workflow']].concat(wfs.map((w) => [w, w])))}
        ${chip('bot', 'bots', 'Bot', [['', 'Any bot']].concat(bots.map((b) => [b, b])))}
        ${UI.folderChip()}
        ${chip('repo', 'branch', 'Repo', [['', 'Any repo']].concat([...new Set(D.runs.map((r) => r.repo).filter(Boolean))].sort().map((x) => [x, x])))}
        ${chip('since', 'clock', 'Time range', RANGES)}
      </div>
      <div id="act-strip">${strip(G)}</div>
      <section class="dl-card dl-flush dl-runs-card" style="margin-top:20px"><table class="dl-tbl grouped" id="act-tbl">${theadHTML(G)}${tableBody(G)}</table></section>
      <div class="dl-phone-groups" id="act-phone">${av.sel.size ? selBar(G) : ''}${phoneBody(G)}</div>
      <p class="dl-sub dl-keep">Runs are kept for 14 days; each record is signed, and one that fails its signature shows as tampered.</p>
    </div>`;
  }
  const actRefresh = () => { const G = groups(), t = UI.$('#act-tbl'); if (!t) return; t.innerHTML = theadHTML(G) + tableBody(G); UI.$('#act-phone').innerHTML = (av.sel.size ? selBar(G) : '') + phoneBody(G); UI.$('#act-strip').innerHTML = strip(G); };
  UI.act('act-q', (el) => { av.q = el.value; av.limit = PAGE; actRefresh(); });
  UI.act('act-f', (sel) => { av[sel.dataset.k] = sel.value; av.limit = PAGE; sel.closest('.dl-chip').classList.toggle('on', !!sel.value); actRefresh(); });
  UI.act('act-clear', () => { Object.assign(av, { q: '', status: '', wf: '', bot: '', repo: '', since: '' }); UI.rerender(); });
  UI.act('act-more', () => { av.limit += PAGE; actRefresh(); });
  UI.act('act-sel-row', (cb, e) => { const boxes = UI.$$('#act-tbl input[data-act="act-sel-row"]'), i = boxes.findIndex((x) => x.dataset.id === cb.dataset.id), on = !av.sel.has(cb.dataset.id);
    if (e && e.shiftKey && av.last != null && i >= 0) boxes.slice(Math.min(av.last, i), Math.max(av.last, i) + 1).forEach((x) => (on ? av.sel.add(x.dataset.id) : av.sel.delete(x.dataset.id)));
    else on ? av.sel.add(cb.dataset.id) : av.sel.delete(cb.dataset.id);
    av.last = i; setTimeout(actRefresh); });
  UI.act('act-sel-all', () => { groups().all.slice(0, 999).filter((r) => GROUP(r) !== 'done' || groups().done.indexOf(r) < av.limit).forEach((r) => av.sel.add(r.id)); setTimeout(actRefresh); });
  UI.act('act-sel-matching', () => { groups().all.forEach((r) => av.sel.add(r.id)); actRefresh(); });
  UI.act('act-sel-clear', () => { av.sel.clear(); actRefresh(); });
  const newRun = (r, from) => { const id = 'r-' + Math.random().toString(16).slice(2, 6); D.runs.unshift({ id, workflow: r.workflow, trigger: r.trigger + ' (retry)', repo: r.repo, number: r.number, status: 'run', started: now(), cost: 0, retry_of: r.id, steps: [] }); D.audit.unshift({ ts: now(), event: 'run_retry', who: 'operator:ops', detail: `${id} · retry_of ${r.id} · from ${from || 'the start'}`, run: id }); return id; };
  UI.act('act-bulk-retry', () => { const rs = D.runs.filter((r) => av.sel.has(r.id) && (r.status === 'fail' || r.status === 'stopped'));
    UI.confirm({ title: `Retry ${rs.length} run${rs.length > 1 ? 's' : ''}?`, body: `Each starts again from the step that failed; the steps before it are replayed from what they recorded, so nothing they did is repeated.`, ok: `Retry ${rs.length}` }).then((ok) => { if (!ok) return;
      rs.forEach((r) => newRun(r, r.failed_step)); av.sel.clear(); S.save(); UI.refreshChrome(); actRefresh(); UI.toast(`Retried ${rs.length} run${rs.length > 1 ? 's' : ''}`, 'they’re running now', 'run'); }); });
  UI.act('act-bulk-stop', () => { const rs = D.runs.filter((r) => av.sel.has(r.id) && r.status === 'run');
    UI.confirm({ title: `Stop ${rs.length} running run${rs.length > 1 ? 's' : ''}?`, body: 'Each stops mid-step; anything already pushed stays. You can retry them from any step.', ok: `Stop ${rs.length}`, danger: true }).then((ok) => { if (!ok) return;
      rs.forEach((r) => { r.status = 'stopped'; r.finished = now(); r.steps.forEach((x) => { if (x.status === 'run') x.status = 'stopped'; }); D.audit.unshift({ ts: now(), event: 'run_stop', who: 'operator:ops', detail: `${r.id} · ${r.workflow}`, run: r.id }); });
      av.sel.clear(); S.save(); UI.refreshChrome(); actRefresh(); UI.toast(`Stopped ${rs.length}`, 'retry them from any step', 'ok'); }); });
  document.addEventListener('click', (e) => { const tr = e.target.closest && e.target.closest('.dl-tbl tr[data-href]'); if (!tr || e.target.closest('a, button, input, label, select')) return; UI.go(tr.dataset.href); });

  // Activity keeps its filters (module state) and its scroll position, so Back from a run lands where you were
  UI.route('activity', () => { const q = new URLSearchParams(location.hash.split('?')[1] || '');
    if ([...q.keys()].length) { Object.assign(av, { q: '', status: '', wf: '', bot: '', repo: '', limit: PAGE }); ['status', 'wf', 'bot', 'repo', 'since', 'q'].forEach((k) => { if (q.has(k)) av[k] = q.get(k); }); if (av.status === 'run') av.since = ''; }
    return { toString: activityPage, after(main) { if (/^#\/runs\/./.test(UI.prevHash || '')) main.scrollTop = UI.actScroll || 0; UI.onLeave(() => { UI.actScroll = main.scrollTop; }); } }; }, { nav: 'activity' });
  // old links: Run history and the live pages are Activity now; their filters carry over
  UI.route('runs', () => { const q = new URLSearchParams((location.hash.split('?')[1] || '')); ['status', 'wf', 'bot', 'repo', 'since', 'q'].forEach((k) => { if (q.has(k)) av[k] = q.get(k); }); if (q.has('workflow')) av.wf = q.get('workflow'); setTimeout(() => location.replace('#/activity')); return ''; }, { nav: 'activity' });
  UI.route('live/:id?', ({ id }) => { setTimeout(() => location.replace(id ? '#/runs/' + id : '#/activity')); return ''; }, { nav: 'activity' });

  /* ---------- one run: the approved run page (mock runPage + the step tree), for a running run and a finished one ---------- */
  const dv = { tab: 'outputs', sel: {} };
  const tabsFor = (s) => [s.outputs ? ['outputs', 'Outputs'] : null, s.inputs ? ['inputs', 'Inputs'] : null, s.log.length ? ['log', 'Log'] : null, s.diff ? ['diff', 'Diff'] : null].filter(Boolean);
  const STEP_S = { ok: 'ok', run: 'run', fail: 'fail', stopped: 'skip', wait: 'ember', idle: 'skip' };
  const stepSentence = (r, s) => { if (s.status === 'idle') return 'Not reached'; if (s.status === 'run') return 'Working on it now';
    const lines = (s.log || []).map(([, t]) => String(t)); const bad = lines.find((t) => /FAIL|error|exceeded/i.test(t));
    if (s.status === 'fail' || s.status === 'stopped') return (r.failed_step === s.id && r.error ? UI.plainError(r) : '') || bad || 'Failed';
    if (s.status === 'wait') return esc(String(r.detail || 'Waiting for your answer'));
    const out = s.outputs || {}; return out.summary || lines[0] || 'Done'; };
  const tree = (r, rec, sel) => `<ol class="dl-tree">${rec.steps.map((s, i) => `<li class="${s.status === 'fail' ? 'failed' : s.status === 'idle' ? 'skipped' : ''}"><button type="button" class="tl${s.id === sel ? ' on' : ''}" data-act="run-step" data-run="${r.id}" data-step="${s.id}" aria-pressed="${s.id === sel}"><span class="tn">${i + 1}</span>${UI.mark(STEP_S[s.status] || 'skip', { label: true })}<span class="tt"><b>${esc(s.id)}</b><span>${esc(stepSentence(r, s))}</span></span><span></span><em>${s.t0 != null ? UI.durText(s.ms) : ''}</em></button></li>`).join('')}</ol>`;
  // the run's live log, in the running step's card
  const lv = { follow: true, n: 9 };
  let LOG = D.liveLog;
  // each running run streams its own workflow's lines (never another workflow's fixture): ci-fixer's live tail, or the
  // lines its run record writes for each step it has reached
  const logOf = (run) => { if (run.workflow === 'ci-fixer') return D.liveLog;
    const rec = UI.runRecord(run); return rec.steps.filter((s) => s.status !== 'idle').flatMap((s) => (s.status === 'run' && (D.liveLogs || {})[run.workflow] ? D.liveLogs[run.workflow] : s.log).map(([k, t]) => [s.id, k, t])); };
  const logLine = ([src, kind, text], i, fresh) => `<span class="ln ${kind === 'err' ? 'fail' : ''}${fresh ? ' new' : ''}"><i>${new Date(Date.now() - (LOG.length - i) * 17000).toTimeString().slice(0, 8)}</i><span class="src">${esc(src)}</span> ${esc(text)}</span>`;
  /** Only a step with a live paseo session has somewhere to open: an agent on a paseo runtime, a hand-off, a bot's home session. */
  const paseoOf = (r, s) => (s.kind === 'agent' && /^paseo/.test(String(s.what || 'paseo'))) || s.kind === 'bot' || s.kind === 'handoff' || String(r.workflow).startsWith('bot:');
  function stepCard(r, rec, s) {
    const tabs = tabsFor(s), tab = tabs.some(([v]) => v === dv.tab) ? dv.tab : (tabs[0] || [''])[0];
    const live = s.status === 'run';
    const body = live ? `<pre class="dl-code dl-log" id="log" data-volatile>${LOG.filter(([src]) => src === s.id || !rec.steps.some((x) => x.id === src)).slice(0, lv.n).map((l, i) => logLine(l, i)).join('')}<span class="ln"><i></i><span class="cursor-blink"></span></span></pre>`
      : s.status === 'idle' ? `<p class="dl-sub">Not reached: the run ${r.status === 'idle' ? 'was parked before any step ran' : `stopped at ${esc(rec.failAt || rec.waitAt || '')}`}, so this step never ran.</p>`
      : { outputs: () => UI.yaml(UI.stepOutputsYAML(r, s)), inputs: () => UI.yaml(YAML.stringify(s.inputs)), log: () => `<pre class="dl-code" style="--gut:5ch">${s.log.map(([k, t], i) => `<span class="ln ${k === 'err' ? 'fail' : ''}"><i>${i + 1}</i>${esc(t)}</span>`).join('')}</pre>`, diff: () => UI.stepDiffHTML(s.diff) }[tab]?.() || '';
    return `<section class="dl-card" id="run-step"><header class="dl-ch"><h2>${esc(s.id)}</h2>${UI.statusMark(STEP_S[s.status] || 'skip', s.status === 'idle' ? 'Not reached' : s.status === 'stopped' ? 'Stopped' : null)}<span class="dl-sp"></span>${!live && tabs.length > 1 ? UI.seg(tabs, tab, 'run-tab') : ''}</header>
      ${live && paseoOf(r, s) ? `<div class="dl-step-open"><button class="btn sm secondary" ${UI.off('Opens this step’s conversation in paseo, outside the dashboard (not in the prototype)')}>${icon('terminal')}Open in paseo</button><span class="dl-sub">Watch or take over this agent’s conversation.</span></div>` : ''}
      ${live ? `<div class="dl-step-open"><button class="btn sm secondary" data-act="restart-step" data-run="${r.id}" data-from="${s.id}">${icon('undo')}Restart this step…</button></div>` : r.status !== 'run' && s.status !== 'idle' ? `<div class="dl-step-open"><button class="btn sm secondary" data-act="retry" data-run="${r.id}" data-from="${s.id}">${icon('undo')}Retry from here…</button></div>` : ''}
      ${body}${['agent', 'code', 'decide'].includes(s.kind) && !live ? `<details class="dl-tech-d env-tech"><summary>${icon('chev')}Its environment <span class="dl-sub">the variables this step saw, by scope; values are never shown</span></summary>${UI.effectiveEnvHTML({ runtime: s.kind === 'code' ? '' : 'claude-cli', workflow: r.workflow, sandboxed: s.kind !== 'code' })}</details>` : ''}</section>`;
  }
  const trigWords = (r) => { const t = String(r.trigger); const where = r.repo ? ` on ${UI.runTarget(r)}` : '';
    return /check_failed/.test(t) ? `A check failed${where}` : /review_requested/.test(t) ? `A review was requested${where}` : /issue_opened/.test(t) ? `An issue was opened${where}` : /schedule/.test(t) ? 'Its schedule' : /spawn/.test(t) && String(r.workflow).startsWith('bot:') ? `${esc(r.workflow.slice(4))} started it itself${(t.match(/spawn (\w+)/) || [])[1] ? ` (worker ${esc(t.match(/spawn (\w+)/)[1])})` : ''}` : /manual/.test(t) ? 'You started it' : /incident|pd\./.test(t) ? 'A PagerDuty incident' : /sweep/.test(t) ? 'Its routine sweep' : `${esc(t)}${where}`; };
  UI.route('runs/:id', ({ id }) => {
    const r = D.runs.find((x) => x.id === id);
    if (!r) return `<div class="page dl-page"><div class="dl-crumbs"><a href="#/activity">${icon('chev', 'flip')}Back to Activity</a></div>${UI.empty({ title: `There is no run ${esc(id)}`, body: 'It may be older than the 14 days runs are kept.', action: '<a class="btn" href="#/activity">Back to Activity</a>' })}</div>`;
    const rec = UI.runRecord(r), sel = dv.sel[r.id] || (r.steps.find((x) => x.status === 'run') || {}).id || rec.failAt || rec.waitAt || (rec.steps.find((x) => x.kind === 'agent') || rec.steps[rec.steps.length - 1]).id, s = rec.steps.find((x) => x.id === sel) || rec.steps[0];
    const story = UI.runStory(r), audit = UI.runAudit(r), same = D.runs.filter((x) => x.workflow === r.workflow && x.id !== r.id && x.status !== 'run'), inNeeds = D.attention.some((a) => a.href === '#/runs/' + r.id);
    if (r.status === 'run') LOG = logOf(r);
    const title = `${esc(UI.runTitle(r))}${r.repo ? ` on ${UI.runTarget(r)}` : ''}`;
    const lead = story ? story.happened : r.status === 'run' ? `${trigWords(r)} ${UI.agoText(r.started)}; now on step <b>${esc((r.steps.find((x) => x.status === 'run') || s).id)}</b>.` : r.status === 'ok' ? `Finished in ${UI.runDur(r)}; every step passed.` : '';
    const prBtn = r.repo && r.number ? `<a class="btn secondary" href="#/runs/${r.id}" ${UI.off('Opens the pull request on GitHub (not in the prototype)')}>${icon('pr')}Open the ${/issue/.test(r.trigger) ? 'issue' : 'pull request'}</a>` : '';
    const actions = r.status === 'run' ? `<button class="btn secondary" data-act="stop-run" data-run="${r.id}">${icon('x')}Stop run…</button>${prBtn}`
      : `${['fail', 'stopped', 'idle'].includes(r.status) ? (() => {
          // when a check failed on a change an earlier step made, rerunning the check fails again: the primary redoes the
          // step that made the change (its agent gets the failure), and rerunning the check alone is secondary
          const at = rec.steps.findIndex((x) => x.id === rec.failAt), maker = at > 0 ? rec.steps.slice(0, at).reverse().find((x) => x.diff) : null, failed = rec.steps[at];
          const one = (from, label, cls) => `<button class="btn ${cls}" data-act="retry" data-run="${r.id}" data-from="${esc(from)}">${icon('updates')}${label}…</button>`;
          return maker && failed && failed.kind !== 'agent' ? one(maker.id, `Retry from “${esc(maker.id)}”`, 'primary') + one(rec.failAt, `Rerun “${esc(rec.failAt)}” only`, 'secondary') : one(rec.failAt || '', `Retry${rec.failAt ? ` from “${esc(rec.failAt)}”` : ''}`, 'primary'); })() : r.status === 'wait' ? `<a class="btn primary" href="#/attention">Answer it</a>` : ''}${prBtn}${same.length ? `<a class="btn secondary" href="#/runs/${r.id}/compare">${icon('split')}Compare…</a>` : ''}${inNeeds ? `<button class="btn ghost" data-act="run-dismiss" data-run="${r.id}">Dismiss</button>` : ''}`;
    const facts = [['Started', `${UI.agoText(r.started)} <span class="dl-sub">(${UI.hm(r.started)})</span>`], r.status === 'run' ? ['Running for', UI.durText(Date.now() - new Date(r.started))] : ['Took', UI.runDur(r)], UI.usageOn() ? ['Cost', UI.runFig(r)] : null, ['Triggered by', trigWords(r)], ['Workflow', `<a class="dl-link" href="#/workflows/${encodeURIComponent(r.workflow)}">${esc(r.workflow)}</a>`], ['Run', `<span class="mono">${r.id}</span>`], r.retry_of ? ['Retry of', `<a class="mono dl-link" href="#/runs/${r.retry_of}">${r.retry_of}</a>`] : null, ['Record', r.status === 'run' || r.status === 'wait' ? 'signed when it finishes' : 'signed; the signature checks out']];
    return { toString: () => `<div class="page dl-page">
      <div class="dl-crumbs run-nav"><a href="#/activity">${icon('chev', 'flip')}Back to Activity</a></div>
      <div class="dl-phead"><div><h1 class="dl-h1-long">${title}</h1>${lead ? `<p class="dl-lead">${lead}</p>` : ''}</div>${UI.statusMark(UI.runS(r.status), UI.runWord(r.status), { lg: true })}</div>
      <div class="dl-actions">${actions}</div>
      <div class="dl-cols"><div class="dl-col-main">
        <section class="dl-card"><header class="dl-ch"><h2>What happened</h2><span class="dl-sub">${rec.steps.length} steps · ${r.status === 'run' ? 'running' : UI.durText(rec.total)}</span></header>${tree(r, rec, s.id)}</section>
        ${stepCard(r, rec, s)}</div>
        <div class="dl-col-side"><section class="dl-card"><header class="dl-ch"><h2>Details</h2></header>${UI.facts(facts)}</section>
          <section class="dl-card run-audit"><header class="dl-ch"><h2>Audit</h2><span class="dl-sp"></span><a class="dl-link" href="#/logs/audit?run=${r.id}">All</a></header><ul class="dl-list">${audit.slice(0, 6).map((a) => `<li class="dl-audit"><span class="dl-nt"><b>${esc(a.event.replace(/_/g, ' '))}</b><span class="dl-why">${esc(a.detail || '')}</span></span><span class="dl-age">${UI.agoText(a.ts)}</span></li>`).join('')}</ul></section></div></div>
    </div>`,
    after(main) { if (r.status !== 'run') return; const log = UI.$('#log', main); if (!log) return; log.scrollTop = log.scrollHeight;
      UI.every(UI.fast ? 400 : 1500, () => { if (lv.n >= LOG.length) return; log.lastElementChild.insertAdjacentHTML('beforebegin', logLine(LOG[lv.n], lv.n, true)); lv.n++; log.scrollTop = log.scrollHeight; }); } };
  }, { nav: 'activity' });
  UI.act('run-tab', (b) => { dv.tab = b.dataset.v; UI.rerender(); });
  UI.act('run-step', (b) => { dv.sel[b.dataset.run] = b.dataset.step; if (b.dataset.tab) dv.tab = b.dataset.tab; UI.rerender(); });
  UI.act('run-pick', (b) => { dv.sel[b.dataset.run] = b.dataset.step; dv.tab = b.dataset.tab || dv.tab; UI.rerender(); });
  // dismissing is reversible, so it asks nothing; its toast carries the Undo
  // the step list is one control to the keyboard: ↑/↓ move the selection (and the step card follows)
  document.addEventListener('keydown', (e) => { if (!['ArrowDown', 'ArrowUp'].includes(e.key) || !e.target.matches || !e.target.matches('.dl-tree .tl')) return;
    const all = [...document.querySelectorAll('.dl-tree .tl')], i = all.indexOf(e.target), next = all[i + (e.key === 'ArrowDown' ? 1 : -1)]; if (!next) return;
    e.preventDefault(); const step = next.dataset.step; next.click(); setTimeout(() => { const b = document.querySelector(`.dl-tree .tl[data-step="${step}"]`); if (b) b.focus(); }); });
  UI.act('run-dismiss', (b) => { const href = '#/runs/' + b.dataset.run, was = D.attention.some((a) => a.href === href); D.dismissed = [...new Set((D.dismissed || []).concat(href))]; S.save(); UI.rerender();
    UI.toast(was ? 'Dismissed from Needs you' : 'Not in Needs you', 'the run and its record stay in Activity', 'ok', was ? { label: 'Undo', fn: () => { D.dismissed = D.dismissed.filter((h) => h !== href); S.save(); UI.rerender(); } } : null); });

  /* ---------- compare two runs of a workflow: step by step, then the chosen step's outputs side by side ---------- */
  const cv = { step: null };
  const SL = { ok: 'passed', run: 'running', fail: 'failed', wait: 'needs you', idle: 'parked', stopped: 'stopped' };
  UI.route('runs/:id/compare/:other?', ({ id, other }) => {
    const a = D.runs.find((x) => x.id === id); if (!a) return `<div class="page dl-page">${UI.empty({ title: `There is no run ${esc(id)}`, action: '<a class="btn" href="#/activity">Back to Activity</a>' })}</div>`;
    const same = D.runs.filter((x) => x.workflow === a.workflow && x.id !== a.id && x.status !== 'run');
    const head = `<div class="dl-crumbs"><a href="#/runs/${a.id}">${icon('chev', 'flip')}Back to the run</a></div><div class="dl-phead"><div><h1>Compare two ${esc(a.workflow)} runs</h1></div></div>`;
    if (!other) return `<div class="page dl-page">${head}<section class="dl-card" style="margin-top:20px"><header class="dl-ch"><h2>Pick a run to compare with</h2></header><ul class="dl-list">${same.map((x) => `<li><a class="dl-row-a" href="#/runs/${a.id}/compare/${x.id}"><span class="dl-ng">${UI.mark(UI.runS(x.status), { label: UI.runWord(x.status) })}</span><span class="dl-nt"><b>${esc(x.workflow)} <span class="tgt">${UI.runTarget(x)}</span></b><span class="dl-why">${UI.runWord(x.status)} · ${UI.agoText(x.started)} · <span class="mono">${x.id}</span></span></span></a></li>`).join('') || '<li class="dl-sub">No other run of this workflow to compare with.</li>'}</ul></section></div>`;
    const b = D.runs.find((x) => x.id === other); if (!b) return `<div class="page dl-page">${head}${UI.empty({ title: `There is no run ${esc(other)}` })}</div>`;
    const ra = UI.runRecord(a), rb = UI.runRecord(b), ids = [...new Set(ra.steps.map((s) => s.id).concat(rb.steps.map((s) => s.id)))];
    const pick = cv.step && ids.includes(cv.step) ? cv.step : (ids.find((i) => { const x = ra.steps.find((s) => s.id === i), y = rb.steps.find((s) => s.id === i); return !x || !y || x.status !== y.status; }) || ids[0]);
    const S2 = { ok: 'ok', run: 'run', fail: 'fail', stopped: 'skip', wait: 'ember', idle: 'skip' };
    const cell = (s) => (s ? `${UI.statusMark(S2[s.status] || 'skip', s.status === 'idle' ? 'Not reached' : null)}${s.t0 != null ? ` <span class="dl-sub">${UI.durText(s.ms)}</span>` : ''}` : '<span class="dl-sub">—</span>');
    const delta = (x, y) => (x && y && x.t0 != null && y.t0 != null ? `<span class="${y.ms > x.ms * 1.2 ? 'warn-t' : 'dl-sub'}">${y.ms >= x.ms ? '+' : '−'}${UI.durText(Math.abs(y.ms - x.ms))}</span>` : '');
    const sa = ra.steps.find((s) => s.id === pick), sb = rb.steps.find((s) => s.id === pick);
    const yA = sa && sa.outputs ? UI.stepOutputsYAML(a, sa) : '', yB = sb && sb.outputs ? UI.stepOutputsYAML(b, sb) : '';
    const runCell = (x) => `${UI.statusMark(UI.runS(x.status), UI.runWord(x.status))} <a class="mono dl-link" href="#/runs/${x.id}">${x.id}</a> <span class="dl-sub">${UI.runTarget(x)} · ${UI.agoText(x.started)}</span>`;
    return `<div class="page dl-page">${head}
      <div class="dl-filters"><span class="dl-sub">This run</span>${runCell(a)}<span class="dl-sub">compared with</span><label class="dl-chip on">${icon('split')}<select data-on-change="cmp-other" data-run="${a.id}" aria-label="Compare with">${same.map((x) => `<option value="${x.id}"${x.id === b.id ? ' selected' : ''}>${x.id} · ${SL[x.status] || x.status} · ${UI.runTarget(x)}</option>`).join('')}</select></label></div>
      <section class="dl-card dl-flush"><table class="dl-tbl cmp-tbl"><thead><tr><th>Step</th><th>This run</th><th>Compared with</th><th class="num">Difference</th></tr></thead><tbody>
        ${ids.map((i) => { const x = ra.steps.find((s) => s.id === i), y = rb.steps.find((s) => s.id === i); const differs = !x || !y || x.status !== y.status || JSON.stringify(x.outputs) !== JSON.stringify(y.outputs); return `<tr class="click${i === pick ? ' on' : ''}"${i === pick ? UI.rowState('sel') : ''} data-act="cmp-step" data-step="${esc(i)}" tabindex="0" data-step-row><td><b>${esc(i)}</b>${differs ? ' ' + UI.label('differs', { s: 'wait' }) : ''}</td><td>${cell(x)}</td><td>${cell(y)}</td><td class="num">${delta(x, y)}</td></tr>`; }).join('')}
        <tr><td class="dl-sub">Total</td><td>${UI.durText(ra.total)}</td><td>${UI.durText(rb.total)}</td><td class="num">${delta({ t0: 0, ms: ra.total }, { t0: 0, ms: rb.total })}</td></tr></tbody></table></section>
      <section class="dl-card" style="margin-top:20px"><header class="dl-ch"><h2>What ${esc(pick)} produced</h2><span class="dl-sub">this run → compared with</span></header>${yA || yB ? UI.diffView(yA, yB, 'unified', { file: `${pick} outputs` }).html : '<p class="dl-sub">Neither run recorded outputs for this step.</p>'}</section>
      ${sa && sb && (sa.diff || sb.diff) ? `<div class="dl-cols" style="grid-template-columns:1fr 1fr"><section class="dl-card"><header class="dl-ch"><h2>${a.id} · ${esc(pick)} diff</h2></header>${sa.diff ? UI.stepDiffHTML(sa.diff) : '<p class="dl-sub">No diff</p>'}</section><section class="dl-card"><header class="dl-ch"><h2>${b.id} · ${esc(pick)} diff</h2></header>${sb.diff ? UI.stepDiffHTML(sb.diff) : '<p class="dl-sub">No diff</p>'}</section></div>` : ''}</div>`;
  }, { nav: 'activity' });
  UI.act('cmp-step', (b) => { cv.step = b.dataset.step; UI.rerender(); });
  UI.act('cmp-other', (el) => UI.go(`#/runs/${el.dataset.run}/compare/${el.value}`));

  /* ---------- Needs you: the Overview's list, every item, then the quiet things ---------- */
  UI.route('attention', () => `<div class="page dl-page">
    <div class="dl-phead"><div><h1>Needs you</h1></div>${UI.folderChip()}</div>
    <div class="dl-cols"><div class="dl-col-main"><section class="dl-card">${UI.attentionList(/[?&]all=1/.test(location.hash) ? 1e9 : 0)}</section></div>
      <div class="dl-col-side">
        ${(D.fyi || []).length ? `<section class="dl-card"><header class="dl-ch"><h2>For your information</h2></header><ul class="dl-list">${D.fyi.map((a) => `<li><a class="dl-row-a" href="${a.href}"><span class="dl-ng">${icon(/update/i.test(a.title + a.plain) ? 'updates' : 'shield')}</span><span class="dl-nt"><b>${esc(a.plain)}</b><span class="dl-why">${esc(a.why)}</span></span></a></li>`).join('')}</ul></section>` : ''}
        <section class="dl-card" id="notify"><header class="dl-ch"><h2>Tell me in this browser</h2></header>
          <div class="dl-prefs">
          <label class="row">${UI.switchBtn(D.prefs.notifyApprovals, 'pref', 'data-k="notifyApprovals" aria-label="When a bot asks for approval"')}When a bot asks for approval</label>
          <label class="row">${UI.switchBtn(D.prefs.notifyPush, 'pref', 'data-k="notifyPush" aria-label="When a browser sign-in needs me"')}When a browser sign-in needs me</label>
          <label class="row">${UI.switchBtn(D.prefs.notifyFailed, 'pref', 'data-k="notifyFailed" aria-label="When any run fails"')}When any run fails</label></div>
          <p class="dl-sub">Alerts outside the dashboard (Slack, PagerDuty) are set up as workflows on conductor’s own events.</p></section>
      </div></div></div>`);
  UI.act('pref', (b) => { D.prefs[b.dataset.k] = !D.prefs[b.dataset.k]; S.save(); b.classList.toggle('on', D.prefs[b.dataset.k]); b.setAttribute('aria-checked', D.prefs[b.dataset.k]); });
})();
