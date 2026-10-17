/* Live runs (step graph + streaming log), run history, run detail, needs-you queue. */
(function () {
  const { icon, st, tag, usd, ago, dur, esc } = UI;
  const now = () => new Date().toISOString();

  /* Lay a workflow's steps out left→right by dependency depth (derived from
   * step order + parallel: branches; conductor has no needs: key). */
  UI.layout = (steps, opts = {}) => {
    const W = opts.w || 172, GX = opts.gx || 64, GY = opts.gy || 22, H = opts.h || 78, X0 = opts.x0 || 28, Y0 = opts.y0 || 28;
    const depth = {};
    const d = (s) => depth[s.id] ?? (depth[s.id] = s.needs.length ? 1 + Math.max(...s.needs.map((n) => d(steps.find((x) => x.id === n)) || 0)) : 0);
    steps.forEach(d);
    const cols = {};
    steps.forEach((s) => (cols[depth[s.id]] = cols[depth[s.id]] || []).push(s));
    const maxRows = Math.max(1, ...Object.values(cols).map((c) => c.length));
    const pos = {};
    Object.entries(cols).forEach(([c, list]) => list.forEach((s, i) => { const off = ((maxRows - list.length) * (H + GY)) / 2; pos[s.id] = { x: X0 + c * (W + GX), y: Y0 + off + i * (H + GY) }; }));
    const width = X0 * 2 + Object.keys(cols).length * (W + GX) - GX, height = Y0 * 2 + maxRows * (H + GY) - GY;
    const edges = steps.flatMap((s) => s.needs.filter((n) => pos[n]).map((n) => { const a = pos[n], b = pos[s.id], x1 = a.x + W, y1 = a.y + H / 2, x2 = b.x, y2 = b.y + H / 2, mx = (x1 + x2) / 2; return { from: n, to: s.id, d: `M${x1} ${y1} C${mx} ${y1} ${mx} ${y2} ${x2} ${y2}` }; }));
    return { pos, edges, width, height, W, H };
  };
  const stepStatus = (run, id) => (run.steps.find((s) => s.id === id) || {}).status || 'idle';
  const graphOf = (run) => { const wf = D.workflows.find((w) => w.name === run.workflow); if (wf && wf.steps && wf.steps.length) return wf.steps; if (wf && wf.yaml) return S.deriveGraph(wf.yaml); return run.steps.map((s, i) => ({ id: s.id, kind: 'agent', needs: i ? [run.steps[i - 1].id] : [] })); };

  function graph(run, steps, sel) {
    const L = UI.layout(steps, { w: 150, gx: 34, x0: 20 });
    const edges = L.edges.map((e) => { const a = stepStatus(run, e.from), b = stepStatus(run, e.to); return `<path class="${b === 'run' ? 'active' : a === 'ok' ? 'done' : ''}" d="${e.d}"/>`; }).join('');
    const nodes = steps.map((s) => {
      const r = run.steps.find((x) => x.id === s.id) || {}, stt = r.status || 'idle', p = L.pos[s.id];
      const what = s.uses || (s.kind === 'agent' ? `${s.runtime || 'paseo'} · ${s.model || 'default'}` : s.kind === 'code' ? (s.command || !s.run ? `use: ${s.use || 'cli'}${s.command ? ' · ' + [].concat(s.command).join(' ') : ''}` : `run: ${s.run}`) : s.kind);
      return `<button type="button" class="node${s.id === sel ? ' sel' : ''}" data-s="${stt}" data-act="live-step" data-step="${s.id}" style="left:${p.x}px;top:${p.y}px;width:${L.W}px" aria-pressed="${s.id === sel}">
        <div class="nk"><span>${s.kind}</span><span>${stt === 'run' ? 'running' : stt === 'ok' ? dur(r.duration_ms) : ''}</span></div>
        <div class="nn">${s.id}</div><div class="nm"><span class="muted mono tiny ellipsis" style="max-width:110px">${esc(what)}</span>${st(stt === 'stopped' ? 'fail' : stt, '')}</div>
        ${stt === 'run' ? '<div class="prog"><i></i></div>' : ''}</button>`;
    }).join('');
    return `<div class="graph" style="height:${L.height + 10}px"><div style="position:relative;width:${L.width}px;height:${L.height}px"><svg class="edges" width="${L.width}" height="${L.height}">${edges}</svg>${nodes}</div></div>`;
  }
  const lv = { f: 'all', follow: true, sel: null, run: null, n: 9 };
  const logCls = (k) => ({ err: 'err', warn: 'warn', tool: 'tool', ok: 'ok' }[k] || '');
  const logVisible = ([src, kind]) => lv.f === 'all' || (lv.f === 'tools' ? kind === 'tool' : lv.f === 'errors' ? kind === 'err' || kind === 'warn' : src === lv.f);
  // the sample log, told in this run's own step names (sources map onto the run's steps in order, up to the one running)
  let LOG = D.liveLog;
  const logOf = (run) => { const ids = run.steps.filter((s) => s.status && s.status !== 'idle').map((s) => s.id), srcs = [...new Set(D.liveLog.map((l) => l[0]))];
    if (!ids.length || srcs.every((x) => ids.includes(x))) return D.liveLog;
    return D.liveLog.map(([src, ...rest]) => [ids[Math.min(srcs.indexOf(src), ids.length - 1)], ...rest]); };
  function logLine([src, kind, text], i, fresh) {
    const t = new Date(Date.now() - (LOG.length - i) * 17000).toTimeString().slice(0, 8);
    return `<div class="l ${logCls(kind)}${fresh ? ' new' : ''}${logVisible([src, kind]) ? '' : ' hidden'}" data-src="${src}" data-kind="${kind}"><span class="t">${t}</span><span class="src">${src}</span><span class="m">${esc(text)}</span></div>`;
  }
  /** Only steps with a live paseo session have somewhere to open: an agent on a paseo runtime, a hand-off to a bot's
      home session. cli, code and verb steps don't. */
  const paseoOf = (s) => (s.kind === 'agent' && /^paseo/.test(s.runtime || 'paseo')) || s.kind === 'bot' || s.kind === 'handoff';
  function stepInspector(run, steps, id) {
    const s = steps.find((x) => x.id === id) || steps[0], r = run.steps.find((x) => x.id === s.id) || {};
    return `<div class="panel-head"><h3>${s.id}</h3>${st(r.status === 'stopped' ? 'fail' : r.status || 'idle', r.status === 'stopped' ? 'stopped' : null, true)}<div class="tools">${r.status === 'run' ? `<button class="btn sm" data-act="restart-step" data-run="${run.id}" data-from="${s.id}">${icon('undo')}Restart this step</button>` : run.status === 'run' && (!r.status || r.status === 'idle') ? `<span class="muted tiny">not started yet</span>` : `<button class="btn sm" data-act="retry" data-run="${run.id}" data-from="${s.id}">${icon('undo')}Retry from here</button>`}</div></div>
      <div class="panel-body">${paseoOf(s) && r.status === 'run' ? `<div class="paseo-open"><button class="btn sm" ${UI.off('Opens this step’s agent in paseo, outside the dashboard (not in the prototype)')}>${icon('terminal')}Open in paseo</button><span class="muted small">Watch or take over this agent’s conversation.</span></div>` : ''}<dl class="kv">
        <dt>kind</dt><dd class="mono">${s.kind}${s.uses ? ' · ' + s.uses : ''}</dd>
        ${s.kind === 'agent' ? `<dt>runtime</dt><dd class="mono">${s.runtime || 'paseo'} · fleet ${s.model || 'default'} → claude-sonnet-5 (rung 1)</dd><dt>sandbox</dt><dd>${tag('namespace', 'ok')} ${tag('egress: proxy allowlist')}</dd>` : ''}
        <dt>after</dt><dd class="mono">${s.needs.join(', ') || 'trigger'}</dd>
        <dt>started</dt><dd class="mono">${r.started ? ago(r.started) + ' ago' : r.status === 'ok' ? 'done in ' + dur(r.duration_ms) : '—'}</dd>
        ${UI.usageOn() && s.kind === 'agent' ? `<dt>usage</dt><dd>${UI.runFig({ id: run.id + s.id, cost: r.cost_usd, provider: 'claude', model_id: 'claude-sonnet-5' })}</dd>` : ''}</dl>
      ${s.kind === 'agent' && r.status === 'run' ? `<div class="caps" style="margin:14px 0 6px">Proposed diff (so far)</div><div class="diff"><div class="h">internal/billing/refund.go</div>
        <div class="dl"><span>41</span><span>41</span><span> </span><span>func applyFee(cents int64, bps int64) int64 {</span></div>
        <div class="dl del"><span>42</span><span></span><span>-</span><span>	return cents * bps / 10000</span></div>
        <div class="dl add"><span></span><span>42</span><span>+</span><span>	q, r := cents*bps/10000, cents*bps%10000</span></div>
        <div class="dl add"><span></span><span>43</span><span>+</span><span>	return q + roundHalfEven(r, q)</span></div></div>` : ''}</div>`;
  }

  /* ---------- live ---------- */
  /* ---------- activity: what ran when (Score's lanes), what's running now, the live feed ---------- */
  const av = { span: 720 };
  const ASPANS = { '1h': 60, '12h': 720, '24h': 1440 };
  UI.act('act-span', (b) => { av.span = ASPANS[b.dataset.v]; UI.rerender(); });
  const runProgress = (r) => { const steps = graphOf(r), i = Math.max(0, r.steps.findIndex((x) => x.status === 'run')), n = Math.max(steps.length, r.steps.length, 1); return { at: Math.min(i + 1, n), n }; };
  UI.runningList = () => { const live = D.runs.filter((r) => r.status === 'run');
    return live.length ? `<div class="list">${live.map((r) => { const p = runProgress(r); return UI.row({ href: `#/live/${r.id}`, cls: 'item click run-row', obj: UI.runObj(r), title: esc(UI.runTitle(r)), ctx: `${esc(r.workflow)} · ${UI.runTarget(r)}`, risk: UI.runRisk(r), s: 'run', sword: `step ${p.at} of ${p.n}`, time: `started ${UI.agoText(r.started)}`, attrs: `data-tip="<span class='mono'>${r.id}</span>"` }); }).join('')}</div>`
      : UI.empty({ title: 'Nothing is running', body: 'Runs start when something a workflow or bot watches happens, or when you start one.', action: `<a class="btn" href="#/workflows">${icon('play')}Run a workflow</a>`, small: true }); };
  function activityPage() {
    const live = D.runs.filter((r) => r.status === 'run'), L = UI.lanes({ span: av.span, all: true }), k = Object.keys(ASPANS).find((x) => ASPANS[x] === av.span);
    const marks = D.score.flatMap((l) => l.marks).filter(([a]) => a <= av.span), fails = marks.filter((m) => m[2] === 'fail').length;
    return `<div class="page">${UI.head('Activity', { lede: `${live.length ? `<b>${live.length} ${live.length === 1 ? 'run is' : 'runs are'} going now.</b> ` : '<b>Nothing is running.</b> '}In the last ${k === '1h' ? 'hour' : k.replace('h', ' hours')}, ${marks.filter((m) => m[2] === 'ok').length} of ${marks.length} runs went fine${fails ? `; ${fails} failed` : ''}.`, actions: `<a class="btn" href="#/runs">${icon('runs')}Run history</a>` })}
      <div class="panel score"><div class="panel-head"><h2>What ran when</h2><span class="muted small hide-phone">one lane per workflow or bot · now is on the right</span><div class="tools">${UI.seg(Object.keys(ASPANS).map((x) => [x, x]), k, 'act-span')}</div></div><div class="panel-body">${L.legend}${L.html}</div></div>
      <div class="grid act-grid" style="margin-top:var(--gap-stack)">
        <div class="panel"><div class="panel-head"><h2>Running now</h2>${UI.count(live.length)}</div>${UI.runningList()}</div>
        <div class="panel"><div class="panel-head"><h3>Latest events</h3><div class="tools">${UI.statusMark('run', 'Live', { sm: true })}</div></div><div class="list feed">${D.activity.slice(0, 7).map((a) => UI.row({ cls: 'item', obj: /knowledge/.test(a.text) ? 'knowledge' : /browser/.test(a.text) ? 'browser' : /update/.test(a.text) ? 'update' : /proposed/.test(a.text) ? 'approval' : /egress/.test(a.text) ? 'sandbox' : 'run', title: `<span class="ev-t">${a.text}</span>`, ctx: a.who, s: a.s, sword: { ok: 'done', run: 'running', wait: 'waiting', fail: 'failed', ember: 'needs you', idle: 'blocked' }[a.s] || a.s, time: UI.agoText(a.t) })).join('')}</div></div>
      </div></div>`;
  }

  /* ---------- one live run ---------- */
  UI.route('live/:id?', ({ id }) => {
    // Activity keeps its filters (module state) and its scroll position, so Back from a run lands where you were
    if (!id) return { toString: activityPage, after(main) { if (/^#\/live\/./.test(UI.prevHash || '')) main.scrollTop = UI.actScroll || 0; UI.onLeave(() => { UI.actScroll = main.scrollTop; }); } };
    const live = D.runs.filter((r) => r.status === 'run');
    const run = D.runs.find((r) => r.id === id);
    if (!run) return `<div class="page">${UI.head('Activity')}${UI.empty({ title: 'No such run', body: `There is no run ${esc(id)}.`, action: `<a class="btn" href="#/live">Back to Activity</a>` })}</div>`;
    const steps = graphOf(run);
    if (lv.run !== run.id) { lv.run = run.id; lv.sel = (run.steps.find((s) => s.status === 'run') || run.steps[0] || {}).id || (steps[0] || {}).id; lv.n = 9; }
    // stepping through the list you came from: the running runs, in Activity's order
    const pos = live.findIndex((r) => r.id === run.id), prev = pos > 0 ? live[pos - 1] : null, next = pos >= 0 && pos < live.length - 1 ? live[pos + 1] : null;
    LOG = logOf(run);
    const srcs = [...new Set(LOG.map((l) => l[0]))];
    return {
      toString: () => `<div class="page wide">
        <div class="run-nav"><a class="btn sm ghost back" href="#/live">${icon('chev')}Back to Activity</a>${pos >= 0 && live.length > 1 ? `<span class="pager"><a class="icon-btn sm" ${prev ? `href="#/live/${prev.id}" aria-label="Previous running run"` : UI.off('This is the first running run')}>${icon('chev', 'flip')}</a><span class="muted small">${pos + 1} of ${live.length} running</span><a class="icon-btn sm" ${next ? `href="#/live/${next.id}" aria-label="Next running run"` : UI.off('This is the last running run')}>${icon('chev')}</a></span>` : ''}</div>
        ${UI.head(esc(UI.runTitle(run)), { gen: true, detail: `${st(UI.runS(run.status), UI.runWord(run.status))}${UI.riskMarker(UI.runRisk(run))}${UI.runFig(run)}`,
          actions: run.status === 'run' ? `${UI.riskMarker({ level: 5, word: 'stops work in progress' })}<button class="btn danger quiet" data-act="stop-run" data-run="${run.id}" data-tip="Cancels the running step after a short grace; the run is recorded as stopped and can be retried">${icon('x')}Stop run…</button>` : '' })}
        ${UI.metaLine([{ label: 'Started', value: `${UI.hm(run.started)} <span class="muted">(${UI.agoText(run.started)})</span>` }, run.status === 'run' ? { label: 'Running for', value: UI.durText(Date.now() - new Date(run.started)) } : { label: 'Took', value: UI.runDur(run) }, { obj: 'run', label: 'Run', value: run.id, mono: true }, { obj: 'workflow', label: 'Workflow', value: esc(run.workflow) }, UI.targetItem(run), UI.isApi() ? { label: 'Budget', value: '$2.00' } : null])}
        <div class="panel" style="margin-bottom:14px"><div class="panel-head"><h3>Steps</h3><span class="muted small">trigger <span class="mono">${run.trigger}</span> · from step order + <span class="mono">parallel:</span> · click a step</span><div class="tools"><span class="mono tiny muted">${run.steps.filter((s) => s.status === 'ok').length}/${steps.length} done</span></div></div><div id="graph">${graph(run, steps, lv.sel)}</div></div>
        <div class="run-layout">
          <div class="panel log-panel"><div class="panel-head"><h3>Log</h3>${run.status === 'run' ? UI.statusMark('run', 'streaming', { sm: true }) : ''}
            <div class="tools">${UI.seg([['all', 'all steps']].concat(srcs.map((x) => [x, x])).concat([['tools', 'tools'], ['errors', 'errors']]), lv.f, 'log-f')}<button class="icon-btn${lv.follow ? ' on' : ''}" data-act="log-follow" aria-pressed="${lv.follow}" data-tip="${lv.follow ? 'Following new lines: click to stop' : 'Follow new lines'}" aria-label="Follow">${icon('download')}</button></div></div>
            <div class="log" id="log" data-volatile>${LOG.slice(0, lv.n).map((l, i) => logLine(l, i)).join('')}<div class="l"><span class="t"></span><span class="src">…</span><span class="m"><span class="cursor-blink"></span></span></div></div></div>
          <div class="panel" id="insp" style="align-self:start">${stepInspector(run, steps, lv.sel)}</div>
        </div></div>`,
      after(main) {
        const log = UI.$('#log', main);
        if (lv.follow) log.scrollTop = log.scrollHeight;
        if (run.status === 'run') UI.every(UI.fast ? 400 : 1500, () => { if (lv.n >= LOG.length) return; log.lastElementChild.insertAdjacentHTML('beforebegin', logLine(LOG[lv.n], lv.n, true)); lv.n++; if (lv.follow) log.scrollTop = log.scrollHeight; });
      },
    };
  }, { nav: 'live' });
  UI.act('live-step', (b) => { lv.sel = b.dataset.step; const run = D.runs.find((r) => r.id === lv.run); UI.$$('#graph .node').forEach((x) => { x.classList.toggle('sel', x === b); x.setAttribute('aria-pressed', x === b); }); UI.$('#insp').innerHTML = stepInspector(run, graphOf(run), lv.sel); });
  UI.act('log-f', (b) => { lv.f = b.dataset.v; UI.$$('[data-act="log-f"]').forEach((x) => { x.classList.toggle('on', x === b); x.setAttribute('aria-pressed', x === b); }); UI.$$('#log .l[data-src]').forEach((l) => l.classList.toggle('hidden', !logVisible([l.dataset.src, l.dataset.kind]))); });
  UI.act('log-follow', (b) => { lv.follow = !lv.follow; b.classList.toggle('on', lv.follow); b.setAttribute('aria-pressed', lv.follow); b.dataset.tip = lv.follow ? 'Following new lines: click to stop' : 'Follow new lines'; if (lv.follow) { const l = UI.$('#log'); l.scrollTop = l.scrollHeight; } });
  UI.act('stop-run', (b) => UI.confirm({ title: `Stop ${b.dataset.run}?`, body: 'The running step gets a short grace to checkpoint, then its agent is cancelled. The run is recorded as <b>stopped</b> and can be retried from any step.', ok: 'Stop run', danger: true }).then((ok) => {
    if (!ok) return; const r = D.runs.find((x) => x.id === b.dataset.run); r.status = 'stopped'; r.finished = now(); r.error = 'stopped by operator ops'; r.steps.forEach((s) => { if (s.status === 'run') s.status = 'stopped'; });
    D.audit.unshift({ ts: now(), event: 'run_stop', who: 'operator:ops', detail: `${r.id} · ${r.workflow} · grace 10s`, run: r.id }); S.save(); UI.refreshChrome(); UI.go('#/runs/' + r.id); UI.toast(`Stopped ${r.id}`, 'retry it from any step', 'fail');
  }));
  UI.act('restart-step', (b) => { const r = D.runs.find((x) => x.id === b.dataset.run); UI.confirm({ title: `Restart ${esc(b.dataset.from)}?`, body: `This stops ${r.id} first (the step gets a short grace to checkpoint), then starts a retry from <b>${esc(b.dataset.from)}</b>. Steps before it are replayed from their recorded outputs; what this step did so far is discarded.`, ok: 'Stop and restart', cancel: 'Keep it running', danger: true }).then((ok) => { if (!ok) return;
    r.status = 'stopped'; r.finished = now(); r.failed_step = b.dataset.from; r.error = 'stopped by operator:ops to restart ' + b.dataset.from; r.steps.forEach((s) => { if (s.status === 'run') s.status = 'stopped'; }); D.audit.unshift({ ts: now(), event: 'run_stop', who: 'operator:ops', detail: `${r.id} · ${r.workflow} · grace 10s · restart ${b.dataset.from}`, run: r.id });
    const steps = graphOf(r), from = steps.findIndex((s) => s.id === b.dataset.from), id = 'r-' + Math.random().toString(16).slice(2, 6);
    D.runs.unshift({ id, workflow: r.workflow, trigger: r.trigger + ' (retry)', repo: r.repo, number: r.number, status: 'run', started: now(), cost: 0, retry_of: r.id, steps: steps.map((s, i) => ({ id: s.id, status: i < from ? 'ok' : i === from ? 'run' : 'idle', duration_ms: i < from ? 900 : 0, cost_usd: 0, started: i === from ? now() : undefined })) });
    D.audit.unshift({ ts: now(), event: 'run_retry', who: 'operator:ops', detail: `${id} · retry_of ${r.id} · from ${b.dataset.from}`, run: id }); S.save(); UI.refreshChrome(); UI.go('#/live/' + id); UI.toast(`Restarted ${b.dataset.from}`, `${r.id} stopped · ${id} running`, 'run'); }); });
  UI.act('retry', (b) => {
    const r = D.runs.find((x) => x.id === b.dataset.run), steps = graphOf(r);
    UI.form({ caps: 'retry-from-step', title: `Retry ${r.id}`, intro: 'Re-runs the pinned trigger. Steps before the chosen one are replayed from their recorded outputs; nothing they did is repeated.',
      fields: [{ k: 'from', type: 'enum', label: 'Resume from', req: true, options: steps.map((s) => [s.id, s.id + (s.id === (r.failed_step || b.dataset.from) ? ' (selected)' : '')]).concat([['$start', 'the beginning']]), def: b.dataset.from || r.failed_step || (steps[0] || {}).id }, { k: 'force', type: 'bool', label: 'Force replay', on: 'also re-run steps that already succeeded' }],
      submit: 'Retry', submitIcon: 'play',
      onSubmit: (v) => {
        const id = 'r-' + Math.random().toString(16).slice(2, 6); const from = v.from === '$start' ? 0 : steps.findIndex((s) => s.id === v.from);
        D.runs.unshift({ id, workflow: r.workflow, trigger: r.trigger + ' (retry)', repo: r.repo, number: r.number, status: 'run', started: now(), cost: 0, retry_of: r.id, steps: steps.map((s, i) => ({ id: s.id, status: i < from && !v.force ? 'ok' : i === from ? 'run' : 'idle', duration_ms: i < from ? 900 : 0, cost_usd: 0, started: i === from ? now() : undefined })) });
        D.audit.unshift({ ts: now(), event: 'run_retry', who: 'operator:ops', detail: `${id} · retry_of ${r.id} · from ${v.from}${v.force ? ' · force_replay' : ''}`, run: id });
        S.save(); UI.refreshChrome(); UI.go('#/live/' + id);
      } });
  });

  /* ---------- history ---------- */
  const PAGE = 50;
  const hv = { q: '', status: new Set(), wf: '', repo: '', since: '', cost: '', sel: new Set(), last: null, limit: PAGE };
  const SL = { ok: 'passed', run: 'running', fail: 'failed', wait: 'waiting on you', idle: 'parked', stopped: 'stopped' };
  const histMatch = (r) => {
    if (!UI.scopeKeeps(UI.botOfRun(r))) return false;
    if (hv.status.size && !hv.status.has(r.status === 'stopped' ? 'fail' : r.status)) return false;
    if (hv.wf && r.workflow !== hv.wf) return false;
    if (hv.repo && r.repo !== hv.repo) return false;
    if (hv.since && Date.now() - new Date(r.started) > +hv.since * 60000) return false;
    if (hv.cost && UI.runProv(r) !== hv.cost) return false;
    if (hv.q) { const t = `${r.id} ${r.workflow} ${r.repo}#${r.number} ${r.error || ''} ${r.detail || ''} ${r.trigger}`.toLowerCase(); if (!t.includes(hv.q.toLowerCase())) return false; }
    return true;
  };
  const chipSel = (key, label, ic, opts) => UI.selectChip({ icon: ic, on: !!hv[key], label, attrs: `data-on-change="hist-sel" data-k="${key}"`, options: opts.map(([v, l]) => `<option value="${esc(v)}"${String(hv[key]) === String(v) ? ' selected' : ''}>${esc(l)}</option>`).join('') });
  /* history pages by cursor (the API returns { runs, next_cursor }): the first 50, then "Load 50 more" */
  // grouped: what needs someone first, then what's running, then what finished (quiet ink). Paging cuts across groups.
  const GROUP = (r) => (['fail', 'stopped', 'wait'].includes(r.status) ? 'attn' : r.status === 'run' ? 'run' : 'done');
  const reason = (r) => (r.status === 'fail' || r.status === 'stopped' ? `${r.failed_step ? `Stopped at ${esc(r.failed_step)}: ` : ''}${esc(UI.plainError ? UI.plainError(r) : r.error || '')}` : r.status === 'wait' ? esc(String(r.detail || '').replace(/^hand-off: /, 'Hand-off: ')) : r.status === 'idle' ? esc(String(r.detail || 'parked').replace(/^parked: /, 'Parked: ')) : '');
  function histRows() {
    const all = D.runs.filter(histMatch).sort((x, y) => ['attn', 'run', 'done'].indexOf(GROUP(x)) - ['attn', 'run', 'done'].indexOf(GROUP(y)) || new Date(y.started) - new Date(x.started));
    const rows = all.slice(0, hv.limit), more = all.length - rows.length;
    const n = { attn: all.filter((r) => GROUP(r) === 'attn'), run: all.filter((r) => GROUP(r) === 'run'), done: all.filter((r) => GROUP(r) === 'done') };
    const failed = n.attn.filter((r) => r.status !== 'wait');
    const heads = {
      attn: { title: 'Needs attention', n: n.attn.length, tone: 'attn', say: [failed.length ? `${failed.length} failed` : '', n.attn.length - failed.length ? `${n.attn.length - failed.length} waiting on you` : ''].filter(Boolean).join(', ') + '.', action: failed.length ? `<button class="btn sm" data-act="hist-retry-failed">${icon('undo')}Retry the failed ones…</button>` : '' },
      run: { title: 'Running now', n: n.run.length, tone: 'run', say: 'Open one to watch its steps live.' },
      done: { title: 'Finished', n: n.done.length, tone: 'done', say: `${n.done.filter((r) => r.status === 'ok').length} passed${n.done.some((r) => r.status === 'idle') ? `, ${n.done.filter((r) => r.status === 'idle').length} parked` : ''}.` },
    };
    // finished work folds into one line unless you ask for it (or filter for it)
    const fold = hv.showDone === false && !hv.status.has('ok') && !hv.status.has('idle') && !hv.q; // shown unless you hide them
    const parked = n.done.filter((r) => r.status === 'idle').length;
    heads.done = { title: 'Finished', n: n.done.length, tone: 'done', say: parked ? `${n.done.length - parked} passed, ${parked} parked.` : 'All passed.', action: `<button class="btn sm ghost" data-act="hist-done" aria-expanded="${!fold}">${fold ? 'Show them' : 'Hide them'}</button>` };
    let g = null;
    const html = rows.map((r) => { const gr = GROUP(r), head = gr !== g ? UI.groupRow(heads[gr]) : ''; g = gr; const href = `#/${r.status === 'run' ? 'live' : 'runs'}/${r.id}`, why = reason(r);
      return head + `<tr class="click${gr === 'done' ? ' quiet' : ''}${gr === 'done' && fold ? ' folded' : ''}" data-href="${href}"${UI.rowState(r.status)}>
      <td class="sel" data-l=""><input type="checkbox" class="cbx" data-act="hist-sel-row" data-id="${r.id}" aria-label="Select ${r.id}"${hv.sel.has(r.id) ? ' checked' : ''}></td>
      <td data-l="kind">${UI.objectIcon(UI.runObj(r))}</td>
      <td data-l="run" class="run-what" data-tip="<span class='mono'>${r.id} · ${esc(r.trigger)}</span>"><a class="link-plain" href="${href}"><b>${esc(UI.runTitle(r))}</b></a><span class="why-line">${why || `${esc(r.workflow)}${UI.runTarget(r) !== '—' ? ` · <span data-l="target">${UI.runTarget(r)}</span>` : ''}`}${why ? ` <span class="sub-target">· <span data-l="target">${UI.runTarget(r)}</span></span>` : ''}</span></td>
      <td data-l="risk">${UI.riskMarker(UI.runRisk(r))}</td>
      <td data-l="status">${st(UI.runS(r.status), UI.runWord(r.status))}</td>
      <td data-l="started" class="nowrap muted">${UI.when(r.started)}</td>
      <td data-l="took" class="num muted">${r.status === 'run' ? '' : UI.runDur(r)}</td>
      ${UI.usageOn() ? `<td data-l="usage" class="num">${UI.runFig(r)}</td>` : ''}</tr>`; }).join('');
    return (html || `<tr class="empty-row"><td colspan="10">No run matches these filters. <button class="link" data-act="hist-clear">Clear filters</button></td></tr>`) + (more > 0 ? `<tr class="more-row"><td colspan="10"><button class="btn sm" data-act="hist-more">Load ${Math.min(PAGE, more)} more</button> <span class="muted small">${more} older · next cursor <span class="mono">${esc(btoa(rows[rows.length - 1].id).slice(0, 10))}</span></span></td></tr>` : '');
  }
  UI.act('hist-done', () => { hv.showDone = hv.showDone === false; histRefresh(); });
  UI.act('hist-retry-failed', () => { hv.sel = new Set(D.runs.filter(histMatch).filter((r) => r.status === 'fail' || r.status === 'stopped').map((r) => r.id)); UI.acts['hist-bulk-retry'](); });
  UI.act('hist-more', () => { hv.limit += PAGE; histRefresh(); });
  /** Run history's one sentence. */
  UI.runsSummary = () => { const attn = D.runs.filter((r) => GROUP(r) === 'attn'), run = D.runs.filter((r) => r.status === 'run').length, ok = D.runs.filter((r) => r.status === 'ok').length;
    return `${attn.length ? `<b>${attn.length} ${attn.length === 1 ? 'run needs' : 'runs need'} attention.</b> ` : '<b>Nothing needs attention.</b> '}${run ? `${run} ${run === 1 ? 'is' : 'are'} running now; ` : ''}${ok} finished fine in the last 12 hours.`; };
  UI.route('runs', () => {
    const n = D.runs.filter(histMatch).length;
    return `<div class="page">
      ${UI.head('Run history', { lede: UI.runsSummary(), actions: `<button class="btn" data-act="hist-export">${icon('download')}Export JSONL</button>` })}
      <div class="filters">
        <div class="input-icon">${icon('search')}<input class="input" placeholder="Search runs: repo, #number, run id, error" value="${esc(hv.q)}" data-on-input="hist-q" aria-label="Search runs"></div>
        <details class="filter-more"${hv.status.size || hv.wf || hv.repo || hv.since || hv.cost || UI.folderScope() ? ' open' : ''}><summary class="btn ghost">${icon('filter')}Filter${(() => { const k = hv.status.size + ['wf', 'repo', 'since', 'cost'].filter((x) => hv[x]).length + (UI.folderScope() ? 1 : 0); return k ? ` · ${k}` : ''; })()}</summary><div class="filter-set">
        ${['ok', 'run', 'fail', 'wait', 'idle'].map((s) => UI.filterChip({ on: hv.status.has(s), act: 'hist-st', attrs: `data-st="${s}"`, noCheck: true, text: st(s, SL[s]) })).join('')}
        ${chipSel('wf', 'Workflow', 'workflows', [['', 'workflow: any']].concat([...new Set(D.runs.map((r) => r.workflow))].map((w) => [w, w])))}
        ${chipSel('repo', 'Repo', 'branch', [['', 'repo: any']].concat([...new Set(D.runs.map((r) => r.repo).filter(Boolean))].map((w) => [w, w])))}
        ${chipSel('since', 'Time range', 'clock', [['', 'any time'], ['60', 'last 1h'], ['360', 'last 6h'], ['1440', 'last 24h']])}
        ${!UI.usageOn() ? '' : chipSel('cost', 'Provider', 'sparkle', [['', 'provider: any']].concat(Object.keys(D.usage.providers).map((p) => [p, UI.provLabel(p)])))}
        ${UI.folderChip()}</div></details><span class="count-note" id="hist-n">${n} of ${D.runs.length}</span>
      </div>
      <div class="panel"><table class="tbl responsive"><thead><tr><th class="sel"><input type="checkbox" class="cbx" data-act="hist-sel-all" aria-label="Select all shown"></th><th>What it is</th><th>What happened</th><th>Risk</th><th>Status</th><th>Started</th><th class="num">Took</th>${UI.usageOn() ? `<th class="num">Usage · ${UI.measHead()}</th>` : ''}</tr></thead><tbody id="hist-body">${histRows()}</tbody></table><div id="hist-bulk">${histBulk()}</div>
      <div class="panel-foot"><span data-tip="<span class='mono'>${D.daemon.state_dir}/history/</span>">Runs are kept for 14 days. Each record is signed; one that fails its signature shows as tampered.</span></div></div>
    </div>`;
  });
  function histBulk() { const ids = [...hv.sel], retryable = ids.filter((id) => { const r = D.runs.find((x) => x.id === id); return r && (r.status === 'fail' || r.status === 'stopped'); });
    return ids.length ? UI.bulkBar(ids.length, 'run', [['Retry from the failed step', 'undo', 'hist-bulk-retry', '', retryable.length ? '' : 'None of the selected runs failed'], ['Export selected (JSONL)', 'download', 'hist-bulk-export']], 'hist-bulk-clear') : ''; }
  const bulkRefresh = () => { const el = UI.$('#hist-bulk'); if (el) el.innerHTML = histBulk(); };
  UI.act('hist-sel-row', (cb, e) => { const boxes = UI.$$('#hist-body input[data-act="hist-sel-row"]'), i = boxes.indexOf(cb), on = !hv.sel.has(cb.dataset.id);
    const set = (x) => { x.checked = on; on ? hv.sel.add(x.dataset.id) : hv.sel.delete(x.dataset.id); };
    if (e && e.shiftKey && hv.last != null) boxes.slice(Math.min(hv.last, i), Math.max(hv.last, i) + 1).forEach(set); else set(cb);
    hv.last = i; setTimeout(() => { boxes.forEach((x) => { x.checked = hv.sel.has(x.dataset.id); }); bulkRefresh(); }); });
  UI.act('hist-sel-all', (cb) => { const boxes = UI.$$('#hist-body input[data-act="hist-sel-row"]'), on = boxes.some((x) => !hv.sel.has(x.dataset.id)); boxes.forEach((x) => (on ? hv.sel.add(x.dataset.id) : hv.sel.delete(x.dataset.id))); setTimeout(() => { boxes.forEach((x) => { x.checked = hv.sel.has(x.dataset.id); }); cb.checked = on; bulkRefresh(); }); });
  UI.act('hist-bulk-clear', () => { hv.sel.clear(); UI.$$('#hist-body input[data-act="hist-sel-row"]').forEach((x) => { x.checked = false; }); bulkRefresh(); });
  UI.act('hist-bulk-export', () => { const rows = D.runs.filter((r) => hv.sel.has(r.id)); UI.download('conductor-runs-selected.jsonl', rows.map((r) => JSON.stringify(r)).join('\n') + '\n', 'application/x-ndjson'); UI.toast(`Exported ${rows.length} runs`, 'scrubbed', 'ok'); });
  UI.act('hist-bulk-retry', () => { const rs = D.runs.filter((r) => hv.sel.has(r.id) && (r.status === 'fail' || r.status === 'stopped'));
    UI.confirm({ title: `Retry ${rs.length} run${rs.length > 1 ? 's' : ''}?`, body: `Each re-runs from its failed step; earlier steps replay from their recorded outputs. ${rs.map((r) => `<span class="mono">${r.id}</span> from <b>${esc(r.failed_step || 'the start')}</b>`).join(', ')}.`, ok: 'Retry them' }).then((ok) => { if (!ok) return;
      rs.forEach((r) => { const id = 'r-' + Math.random().toString(16).slice(2, 6); D.runs.unshift({ id, workflow: r.workflow, trigger: r.trigger + ' (retry)', repo: r.repo, number: r.number, status: 'run', started: now(), cost: 0, retry_of: r.id, steps: [] }); D.audit.unshift({ ts: now(), event: 'run_retry', who: 'operator:ops', detail: `${id} · retry_of ${r.id} · from ${r.failed_step || 'start'}`, run: id }); });
      hv.sel.clear(); S.save(); UI.refreshChrome(); UI.rerender(); UI.toast(`Retried ${rs.length}`, 'they show in Live', 'run'); }); });
  const histRefresh = () => { UI.$('#hist-body').innerHTML = histRows(); UI.$('#hist-n').textContent = `${D.runs.filter(histMatch).length} of ${D.runs.length}`; };
  UI.act('hist-q', (el) => { hv.q = el.value; histRefresh(); });
  UI.act('hist-st', (b) => { const k = b.dataset.st; hv.status.has(k) ? hv.status.delete(k) : hv.status.add(k); b.classList.toggle('on'); b.setAttribute('aria-pressed', hv.status.has(k)); histRefresh(); });
  UI.act('hist-sel', (sel) => { hv[sel.dataset.k] = sel.value; sel.closest('.chip').classList.toggle('on', !!sel.value); histRefresh(); });
  UI.act('hist-clear', () => { Object.assign(hv, { q: '', wf: '', repo: '', since: '', cost: '' }); hv.status.clear(); UI.rerender(); });
  UI.act('hist-export', () => { const rows = D.runs.filter(histMatch); UI.download('conductor-runs.jsonl', rows.map((r) => JSON.stringify(r)).join('\n') + '\n', 'application/x-ndjson'); UI.toast(`Exported ${rows.length} runs`, 'conductor-runs.jsonl · scrubbed', 'ok'); });

  /* ---------- run detail: every step selectable, step-scoped tabs, run-scoped audit, a time axis ---------- */
  const dv = { tab: 'outputs', sel: {} };
  const SSL = Object.assign({}, SL, { idle: 'not reached' });
  const mmss = (ms) => { const s = Math.round(ms / 1000); return s < 60 ? `${s}s` : `${Math.floor(s / 60)}m${String(s % 60).padStart(2, '0')}s`; };
  const tabsFor = (s) => [s.outputs ? ['outputs', 'Outputs'] : null, s.inputs ? ['inputs', 'Inputs'] : null, s.log.length ? ['log', 'Log'] : null, s.diff ? ['diff', 'Diff'] : null].filter(Boolean);
  /* the run story: steps top to bottom, each with its outcome in a sentence and its duration on the right, the failing
     step called out, later steps "Not reached"; a thin bar places each step on the run's time axis. Nested workflow
     calls are one node you can open. Every row selects its step (click, or ↑/↓). */
  const stepSentence = (r, rec, s) => { if (s.status === 'idle') return 'Not reached'; if (s.status === 'run') return 'Running now';
    const lines = (s.log || []).map(([, t]) => String(t)); const bad = lines.find((t) => /FAIL|error|exceeded/i.test(t));
    if (s.status === 'fail' || s.status === 'stopped') return (r.failed_step === s.id && r.error ? UI.plainError(r) : '') || bad || 'Failed';
    const out = s.outputs || {}; return out.summary || lines[0] || 'Done'; };
  const storyRow = (r, rec, s, sel, T) => { const stt = UI.runS(s.status);
    return `<button type="button" class="gt-row${s.id === sel ? ' on' : ''}" role="option" aria-selected="${s.id === sel}" data-act="run-step" data-run="${r.id}" data-step="${s.id}"${UI.rowState(stt)}><span class="gt-mark">${st(stt === 'idle' ? 'skip' : stt, `<span class="sr-only">${SSL[s.status] || s.status}</span>`)}</span><span class="gt-what"><b>${esc(s.id)}</b><span class="why-line">${esc(stepSentence(r, rec, s))}</span></span><span class="gt" data-s="${stt}">${s.t0 != null ? `<i style="left:${(s.t0 / T) * 100}%;width:${Math.max((s.ms / T) * 100, 0.8)}%" title="${esc(s.id)} · ${mmss(s.ms)}"></i>` : ''}</span><span class="num gt-dur">${s.t0 != null ? UI.durText(s.ms) : ''}</span></button>${s.children && s.children.length ? `<details class="gt-call"><summary>${s.children.length} steps inside ${esc(s.what || s.id)}</summary>${s.children.map((c) => storyRow(r, rec, c, sel, T)).join('')}</details>` : ''}`; };
  const ganttHTML = (r, rec, sel) => { const T = Math.max(1, rec.total), ticks = [0, 1 / 3, 2 / 3, 1];
    return `<div class="gantt gantt-sel story-tree" role="listbox" aria-label="Steps of ${esc(r.id)}" data-run="${r.id}"><div class="gt-head"><span></span><span></span><span class="gt-axis" aria-hidden="true">${ticks.map((f) => `<span style="left:${f * 100}%">${f ? UI.durText(f * T) : 'start'}</span>`).join('')}</span><span></span></div>
      ${rec.steps.map((s) => storyRow(r, rec, s, sel, T)).join('')}</div>`; };
  const stepPanel = (r, rec, s) => { const tabs = tabsFor(s); const tab = tabs.some(([v]) => v === dv.tab) ? dv.tab : (tabs[0] || [''])[0];
    const body = { outputs: () => UI.yaml(UI.stepOutputsYAML(r, s), { err: s.status === 'fail' ? UI.stepOutputsYAML(r, s).split('\n').map((l, i) => (/FAIL|error/.test(l) ? i + 1 : 0)).filter(Boolean) : [] }), inputs: () => UI.yaml(YAML.stringify(s.inputs)), log: () => `<div class="log" style="max-height:280px">${s.log.map(([k, t], i) => `<div class="l ${logCls(k)}"><span class="t">${UI.clock(new Date(new Date(r.started).getTime() + (s.t0 || 0) + (i * s.ms) / Math.max(1, s.log.length)).toISOString())}</span><span class="src">${esc(s.id)}</span><span class="m">${esc(t)}</span></div>`).join('')}</div>`, diff: () => UI.stepDiffHTML(s.diff) };
    const what = { verb: 'calls a verb', code: 'runs a command', agent: 'agent', decide: 'an agent decides', bot: 'hands to a bot' }[s.kind] || s.kind;
    return `<div class="panel" id="run-step"><div class="panel-head"><h3>Step <span class="mono">${esc(s.id)}</span></h3>${st(s.status === 'stopped' ? 'fail' : s.status, SSL[s.status] || s.status, true)}<span class="muted small">${esc(what)} · <span class="mono">${esc(s.what)}</span>${s.t0 != null ? ` · ${mmss(s.ms)}` : ''}${UI.usageOn() && s.cost ? ` · ${UI.runFig({ id: r.id + s.id, cost: s.cost, provider: UI.runProv(r), model_id: UI.runModel(r) })}` : ''}</span>
      <div class="tools">${tabs.length ? UI.seg(tabs, tab, 'run-tab') : ''}${r.status !== 'run' && s.status !== 'idle' ? `<button class="btn sm ghost" data-act="retry" data-run="${r.id}" data-from="${s.id}">${icon('undo')}Retry from here</button>` : ''}</div></div>
      <div class="panel-body">${s.status === 'idle' ? `<div class="empty small"><span class="serif">Not reached</span>The run ${r.status === 'idle' ? 'was parked before any step ran' : `stopped at ${esc(rec.failAt || rec.waitAt || '')}`}, so this step never ran. Its inputs would have been:</div>${UI.yaml(YAML.stringify(s.inputs || {}))}` : tab ? body[tab]() : '<div class="muted small">This step recorded nothing.</div>'}${['agent', 'code', 'decide'].includes(s.kind) ? `<details class="tech env-tech"><summary>Effective environment for this step</summary>${UI.effectiveEnvHTML({ runtime: s.kind === 'code' ? '' : 'claude-cli', workflow: r.workflow, sandboxed: s.kind !== 'code' })}</details>` : ''}</div></div>`; };
  const storyHTML = (r) => { const s = UI.runStory(r); if (!s) return '';
    const btn = ([act, a, b, label]) => act === 'link' ? `<a class="btn sm" href="${a}">${esc(label)}</a>` : act === 'retry' ? `<button class="btn sm" data-act="retry" data-run="${r.id}" data-from="${esc(a)}">${icon('undo')}${esc(label)}</button>` : act === 'run-pick' ? `<button class="btn sm" data-act="run-step" data-run="${r.id}" data-step="${esc(a)}" data-tab="${esc(b)}">${esc(label)}</button>` : act === 'run-cmp' ? `<a class="btn sm" href="#/runs/${r.id}/compare/${esc(a)}">${icon('split')}${esc(label)}</a>` : `<button class="btn sm ghost" data-act="run-dismiss" data-run="${r.id}">${esc(label)}</button>`;
    return `<div class="callout ${s.tone} run-story" style="margin-bottom:14px"><div class="grow"><div class="caps">What happened</div><p style="margin:4px 0 10px">${s.happened}</p><div class="caps">What you can do</div><div class="row wrap" style="gap:6px;margin-top:6px">${s.can.map(btn).join('')}</div></div></div>`; };
  UI.route('runs/:id', ({ id }) => {
    const r = D.runs.find((x) => x.id === id);
    if (!r) return `<div class="page"><div class="empty"><span class="serif">No run ${esc(id)}</span><a class="btn" href="#/runs">Run history</a></div></div>`;
    if (r.status === 'run') { setTimeout(() => UI.go('#/live/' + r.id)); return ''; }
    const rec = UI.runRecord(r), sel = dv.sel[r.id] || rec.failAt || rec.waitAt || (rec.steps.find((s) => s.kind === 'agent') || rec.steps[rec.steps.length - 1]).id, s = rec.steps.find((x) => x.id === sel) || rec.steps[0];
    const audit = UI.runAudit(r), same = D.runs.filter((x) => x.workflow === r.workflow && x.id !== r.id && x.status !== 'run');
    return `<div class="page">
      ${UI.head(esc(UI.runTitle(r)), { gen: true, crumbs: `<a href="#/runs">Run history</a> ${icon('chev')} <span class="mono">${r.id}</span>`, detail: `${st(UI.runS(r.status), UI.runWord(r.status))}${UI.riskMarker(UI.runRisk(r))}`,
        actions: `<button class="btn" data-act="retry" data-run="${r.id}" data-from="${rec.failAt || ''}">${icon('undo')}Retry…</button>${same.length ? `<a class="btn" href="#/runs/${r.id}/compare">${icon('split')}Compare…</a>` : ''}<button class="btn ghost" data-act="run-record" data-run="${r.id}" data-tip="The signed JSON record: every step's inputs, outputs and timing, scrubbed">${icon('download')}Download run record</button>` })}
      ${UI.metaLine([{ label: 'Started', value: `${UI.hm(r.started)} <span class="muted">(${UI.agoText(r.started)})</span>` }, { label: 'Took', value: UI.runDur(r) }, { obj: 'run', label: 'Run', value: r.id, mono: true }, { obj: 'workflow', label: 'Workflow', value: esc(r.workflow) }, UI.targetItem(r), r.repo ? { obj: 'connector', label: 'Started by', value: esc(r.trigger), mono: true } : null])}
      ${storyHTML(r)}
      <div class="grid g-side">
        <div class="stack">
          <div class="panel"><div class="panel-head"><h2>Steps</h2><span class="muted small">${UI.durText(rec.total)} in all · pick a step, or use ↑ ↓</span></div><div class="panel-body tight">${ganttHTML(r, rec, s.id)}</div></div>
          ${stepPanel(r, rec, s)}
        </div>
        <div class="stack">
          <div class="panel"><div class="panel-head"><h3>Run record</h3></div><div class="panel-body"><dl class="kv">
            ${UI.usageOn() ? `<dt>usage</dt><dd>${UI.runFig(r)} <span class="mono tiny muted">${UI.runModel(r)} · ${UI.srcLabel(UI.isApi() || UI.usageSource() ? 'source' : 'observed')}</span></dd>` : ''}<dt>signature</dt><dd>${st('ok', 'HMAC verified')}</dd><dt>retry of</dt><dd class="mono">${r.retry_of ? `<a class="link" href="#/runs/${r.retry_of}">${r.retry_of}</a>` : '—'}</dd></dl></div></div>
          <div class="panel"><div class="panel-head"><h3>Audit for this run</h3><a class="btn sm ghost" style="margin-left:auto" href="#/logs/audit?run=${r.id}">All ${icon('chev')}</a></div><div class="list run-audit">${audit.map((a) => `<div class="item small"><span class="grow"><span class="ellipsis" style="display:block">${esc(a.detail)}</span><span class="item-id">${a.event}</span></span><span class="att-age">${UI.agoText(a.ts)}</span></div>`).join('') || '<div class="empty small">No audit events for this run.</div>'}</div></div>
        </div></div></div>`;
  });
  UI.act('run-tab', (b) => { dv.tab = b.dataset.v; UI.rerender(); });
  UI.act('run-step', (b) => { dv.sel[b.dataset.run] = b.dataset.step; if (b.dataset.tab) dv.tab = b.dataset.tab; UI.rerender(); requestAnimationFrame(() => { const x = UI.$(`.gt-row[data-step="${b.dataset.step}"]`); if (x && b.classList.contains('gt-row')) x.focus(); if (b.dataset.tab) { const p = UI.$('#run-step'); if (p) p.scrollIntoView({ block: 'nearest' }); } }); });
  document.addEventListener('keydown', (e) => { if (!['ArrowUp', 'ArrowDown'].includes(e.key)) return; const row = e.target.closest && e.target.closest('.gt-row'); if (!row) return; e.preventDefault(); const rows = UI.$$('.gt-row'), i = rows.indexOf(row), n = rows[i + (e.key === 'ArrowDown' ? 1 : -1)]; if (n) n.click(); });
  UI.act('run-record', (b) => { const r = D.runs.find((x) => x.id === b.dataset.run); UI.download(`${r.id}.json`, JSON.stringify(Object.assign({}, r, { steps: UI.runRecord(r).steps.map((s) => ({ id: s.id, status: s.status, started_offset_ms: s.t0, duration_ms: s.ms, inputs: s.inputs, outputs: s.outputs })) }), null, 2)); UI.toast('Downloaded the run record', `${r.id}.json · signed, scrubbed`, 'ok'); });
  UI.act('run-dismiss', (b) => { const href = '#/runs/' + b.dataset.run; const n = D.attention.length; D.attention = D.attention.filter((a) => a.href !== href); S.save(); UI.refreshChrome(); UI.toast(n > D.attention.length ? 'Dismissed from Needs you' : 'Not in Needs you', 'the run and its record stay in history', 'ok'); });

  /* ---------- compare two runs: step status, durations, and the selected step's outputs side by side ---------- */
  const cv = { step: null };
  UI.route('runs/:id/compare/:other?', ({ id, other }) => {
    const a = D.runs.find((x) => x.id === id); if (!a) return `<div class="page"><div class="empty"><span class="serif">No run ${esc(id)}</span><a class="btn" href="#/runs">Run history</a></div></div>`;
    const same = D.runs.filter((x) => x.workflow === a.workflow && x.id !== a.id && x.status !== 'run');
    const head = (sub) => UI.head(`Compare two ${esc(a.workflow)} runs`, { gen: true, crumbs: `<a href="#/runs">Run history</a> ${icon('chev')} <a href="#/runs/${a.id}">${esc(UI.runTitle(a))}</a>`, sub });
    if (!other) return `<div class="page">${head(`pick another ${esc(a.workflow)} run`)}<div class="panel"><div class="list">${same.map((x) => `<a class="item" href="#/runs/${a.id}/compare/${x.id}">${st(x.status === 'stopped' ? 'fail' : x.status, SL[x.status])}<span class="mono">${x.id}</span><span class="mono muted">${UI.runTarget(x)}</span><span class="muted small grow">${ago(x.started)} ago</span>${icon('chev')}</a>`).join('') || '<div class="empty small">No other run of this workflow to compare with.</div>'}</div></div></div>`;
    const b = D.runs.find((x) => x.id === other); if (!b) return `<div class="page">${head('')}<div class="empty"><span class="serif">No run ${esc(other)}</span></div></div>`;
    const ra = UI.runRecord(a), rb = UI.runRecord(b), ids = [...new Set(ra.steps.map((s) => s.id).concat(rb.steps.map((s) => s.id)))];
    const pick = cv.step && ids.includes(cv.step) ? cv.step : (ids.find((i) => { const x = ra.steps.find((s) => s.id === i), y = rb.steps.find((s) => s.id === i); return !x || !y || x.status !== y.status; }) || ids[0]);
    const cell = (s) => (s ? `${st(s.status === 'stopped' ? 'fail' : s.status, SSL[s.status] || s.status)} <span class="tiny muted">${s.t0 != null ? UI.durText(s.ms) : ''}</span>` : '<span class="muted">—</span>');
    const delta = (x, y) => (x && y && x.t0 != null && y.t0 != null ? `<span class="tiny ${y.ms > x.ms * 1.2 ? 'warn-t' : 'muted'}">${y.ms >= x.ms ? '+' : '−'}${UI.durText(Math.abs(y.ms - x.ms))}</span>` : '');
    const sa = ra.steps.find((s) => s.id === pick), sb = rb.steps.find((s) => s.id === pick);
    const yA = sa && sa.outputs ? UI.stepOutputsYAML(a, sa) : '', yB = sb && sb.outputs ? UI.stepOutputsYAML(b, sb) : '';
    const runItem = (label, x) => ({ obj: 'run', label, value: `${st(x.status === 'stopped' ? 'fail' : x.status, SL[x.status] || x.status, true)} <a class="mono" href="#/runs/${x.id}">${x.id}</a> <span class="muted">· ${UI.runTarget(x)} · ${UI.agoText(x.started)}</span>` });
    return `<div class="page">${head('')}
      ${UI.metaLine([{ obj: 'workflow', label: 'Workflow', value: `<a href="#/workflows/${encodeURIComponent(a.workflow)}">${esc(a.workflow)}</a>` }, runItem('This run', a), runItem('Compared with', b)])}
      <div class="row wrap" style="gap:8px;margin-bottom:12px"><span class="muted small">Compare with another run</span>${UI.selectChip({ icon: 'split', on: true, label: 'Compare with', attrs: `data-on-change="cmp-other" data-run="${a.id}"`, options: same.map((x) => `<option value="${x.id}"${x.id === b.id ? ' selected' : ''}>${x.id} · ${SL[x.status] || x.status} · ${UI.runTarget(x)}</option>`).join('') })}</div>
      <div class="panel"><table class="tbl cmp-tbl"><thead><tr><th>Step</th><th>This run <span class="mono muted">${a.id}</span></th><th>Compared with <span class="mono muted">${b.id}</span></th><th class="num">Difference</th></tr></thead><tbody>
        ${ids.map((i) => { const x = ra.steps.find((s) => s.id === i), y = rb.steps.find((s) => s.id === i); const differs = !x || !y || x.status !== y.status || JSON.stringify(x.outputs) !== JSON.stringify(y.outputs); return `<tr class="click${i === pick ? ' on' : ''}"${i === pick ? UI.rowState('sel') : ''} data-act="cmp-step" data-step="${esc(i)}" tabindex="0"><td class="mono">${esc(i)}${differs ? ' ' + UI.label('differs', { s: 'wait' }) : ''}</td><td>${cell(x)}</td><td>${cell(y)}</td><td class="num">${delta(x, y)}</td></tr>`; }).join('')}
        <tr><td class="muted">Total</td><td class="tiny">${UI.durText(ra.total)}</td><td class="tiny">${UI.durText(rb.total)}</td><td class="num">${delta({ t0: 0, ms: ra.total }, { t0: 0, ms: rb.total })}</td></tr></tbody></table></div>
      <div class="panel" style="margin-top:14px"><div class="panel-head"><h3>Outputs of <span class="mono">${esc(pick)}</span></h3><span class="muted small">${a.id} → ${b.id}</span></div><div class="panel-body">${yA || yB ? UI.diffView(yA, yB, 'unified', { file: `${pick} outputs` }).html : '<div class="muted small">Neither run recorded outputs for this step.</div>'}</div></div>
      ${sa && sb && (sa.diff || sb.diff) ? `<div class="grid g2" style="margin-top:14px"><div class="panel"><div class="panel-head"><h3>${a.id} · ${esc(pick)} diff</h3></div><div class="panel-body">${sa.diff ? UI.stepDiffHTML(sa.diff) : '<span class="muted small">no diff</span>'}</div></div><div class="panel"><div class="panel-head"><h3>${b.id} · ${esc(pick)} diff</h3></div><div class="panel-body">${sb.diff ? UI.stepDiffHTML(sb.diff) : '<span class="muted small">no diff</span>'}</div></div></div>` : ''}</div>`;
  }, { nav: 'runs' });
  UI.act('cmp-step', (b) => { cv.step = b.dataset.step; UI.rerender(); });
  UI.act('cmp-other', (el) => UI.go(`#/runs/${el.dataset.run}/compare/${el.value}`));

  /* ---------- needs you ---------- */
  UI.route('attention', () => `<div class="page">
    ${UI.head('Needs you', { lede: UI.needsSummary(), actions: UI.folderChip() + UI.seg([['all', 'All'], ['approvals', 'Approvals'], ['failures', 'Failures'], ['waiting', 'Waiting']], UI.attnFilter || 'all', 'attn-f') })}
    <div class="calm">${D.attention.length ? `<div class="panel attn">${UI.attentionList(0, true)}</div>` : `<div class="panel">${UI.empty({ title: 'All quiet', body: 'Nothing needs you. When a bot asks for approval, a run fails, or something waits on your answer, it shows up here.' })}</div>`}
      <div class="quiet-rows" style="margin-top:var(--gap-section)">
        ${(D.fyi || []).length ? `<details class="quiet-row" open><summary><span class="qr-dot"></span><b>For your information</b><span class="qr-line">${D.fyi.map((a) => esc(a.plain)).join(' · ')}</span>${icon('chevd', 'qr-chev')}</summary><div class="qr-body"><div class="list">${D.fyi.map((a) => `<a class="item click quiet" href="${a.href}"><div class="grow"><div class="item-t">${esc(a.plain)}</div><div class="item-why">${esc(a.why)}</div></div><span class="att-age">${UI.ageText(a.age)}</span><span class="chev">${icon('chev')}</span></a>`).join('')}</div></div></details>` : ''}
        <details class="quiet-row"><summary><span class="qr-dot"></span><b>How approvals work</b><span class="qr-line">Only you can approve, with your passkey, on this device.</span>${icon('chevd', 'qr-chev')}</summary><div class="qr-body"><p class="dim" style="margin:0;max-width:var(--layout-container-prose)">Each approval is a WebAuthn assertion over that proposal's digest, made on this device with user verification. No agent can reach this page, and no verb, chat text or peer message approves anything.</p></div></details>
        <details class="quiet-row"><summary><span class="qr-dot"></span><b>Notify me</b><span class="qr-line">${[D.prefs.notifyApprovals ? 'new approvals' : '', D.prefs.notifyPush ? 'push approvals' : '', D.prefs.notifyFailed ? 'every failed run' : ''].filter(Boolean).join(', ') || 'off'}</span>${icon('chevd', 'qr-chev')}</summary><div class="qr-body stack tight small">
          <label class="row">${UI.switchBtn(D.prefs.notifyApprovals, 'pref', 'data-k="notifyApprovals" aria-label="Browser notifications for new approvals"')}Browser notifications for new approvals</label>
          <label class="row">${UI.switchBtn(D.prefs.notifyPush, 'pref', 'data-k="notifyPush" aria-label="Push approvals need me"')}Push approvals (browser 2FA) need me</label>
          <label class="row">${UI.switchBtn(D.prefs.notifyFailed, 'pref', 'data-k="notifyFailed" aria-label="Every failed run"')}Every failed run</label>
          <div class="muted">Off-box alerts stay with your <span class="mono">conductor.*</span> lifecycle triggers (Slack, PagerDuty…).</div></div></details>
      </div></div></div>`);
  UI.needsSummary = () => { const a = D.attention, n = (k) => a.filter((x) => (k === 'other' ? !['approval', 'failed'].includes(x.kind) : x.kind === k)).length;
    if (!a.length) return '<b>Nothing needs you.</b> Approvals, failed runs and questions from bots land here.';
    const parts = [[n('approval'), 'approval'], [n('failed'), 'failed run'], [n('other'), 'question']].filter(([k]) => k).map(([k, w]) => `${k} ${w}${k === 1 ? '' : 's'}`);
    return `<b>${a.length} ${a.length === 1 ? 'thing needs' : 'things need'} you:</b> ${parts.join(', ').replace(/, ([^,]*)$/, ' and $1')}. `; };
  UI.act('attn-f', (b) => { UI.attnFilter = b.dataset.v; UI.rerender(); });
  UI.act('pref', (b) => { D.prefs[b.dataset.k] = !D.prefs[b.dataset.k]; S.save(); b.classList.toggle('on', D.prefs[b.dataset.k]); b.setAttribute('aria-checked', D.prefs[b.dataset.k]); });
})();
