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
        <div class="nn">${s.id}</div><div class="nm"><span class="muted mono ellipsis" style="font-size:10px;max-width:110px">${esc(what)}</span>${st(stt === 'stopped' ? 'fail' : stt, '')}</div>
        ${stt === 'run' ? '<div class="prog"><i></i></div>' : ''}</button>`;
    }).join('');
    return `<div class="graph" style="height:${L.height + 10}px"><div style="position:relative;width:${L.width}px;height:${L.height}px"><svg class="edges" width="${L.width}" height="${L.height}">${edges}</svg>${nodes}</div></div>`;
  }
  const lv = { f: 'all', follow: true, sel: null, run: null, n: 9 };
  const logCls = (k) => ({ err: 'err', warn: 'warn', tool: 'tool', ok: 'ok' }[k] || '');
  const logVisible = ([src, kind]) => lv.f === 'all' || (lv.f === 'tools' ? kind === 'tool' : lv.f === 'errors' ? kind === 'err' || kind === 'warn' : src === lv.f);
  function logLine([src, kind, text], i, fresh) {
    const t = new Date(Date.now() - (D.liveLog.length - i) * 17000).toTimeString().slice(0, 8);
    return `<div class="l ${logCls(kind)}${fresh ? ' new' : ''}${logVisible([src, kind]) ? '' : ' hidden'}" data-src="${src}" data-kind="${kind}"><span class="t">${t}</span><span class="src">${src}</span><span class="m">${esc(text)}</span></div>`;
  }
  function stepInspector(run, steps, id) {
    const s = steps.find((x) => x.id === id) || steps[0], r = run.steps.find((x) => x.id === s.id) || {};
    return `<div class="panel-head"><h3>${s.id}</h3>${st(r.status === 'stopped' ? 'fail' : r.status || 'idle', r.status === 'stopped' ? 'stopped' : null, true)}<div class="tools">${r.status === 'run' ? `<button class="btn sm" data-act="restart-step" data-run="${run.id}" data-from="${s.id}">${icon('undo')}Restart this step</button>` : run.status === 'run' && (!r.status || r.status === 'idle') ? `<span class="muted tiny">not started yet</span>` : `<button class="btn sm" data-act="retry" data-run="${run.id}" data-from="${s.id}">${icon('undo')}Retry from here</button>`}</div></div>
      <div class="panel-body"><dl class="kv">
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
  UI.route('live/:id?', ({ id }) => {
    const live = D.runs.filter((r) => r.status === 'run');
    const run = D.runs.find((r) => r.id === id) || live[0];
    if (!run) return `<div class="page">${UI.head('Live')}<div class="panel"><div class="empty"><span class="serif">Nothing is running</span><a class="btn" href="#/workflows">Run a workflow</a></div></div></div>`;
    if (run.status !== 'run' && !id) return '';
    const steps = graphOf(run);
    if (lv.run !== run.id) { lv.run = run.id; lv.sel = (run.steps.find((s) => s.status === 'run') || run.steps[0] || {}).id || (steps[0] || {}).id; lv.n = 9; }
    const elapsed = Math.round((Date.now() - new Date(run.started)) / 60000);
    const srcs = [...new Set(D.liveLog.map((l) => l[0]))];
    return {
      toString: () => `<div class="page wide">
        ${UI.head(`${run.workflow}`, { crumbs: `<a href="#/live">Live</a> ${icon('chev')} ${run.id}`, sub: run.repo ? `${run.repo}#${run.number}` : run.trigger,
          actions: `<span class="mono small muted">${elapsed}m elapsed</span>${UI.runFig(run)}${UI.isApi() ? '<span class="mono small muted">budget $2.00</span>' : ''}<button class="btn" ${UI.off('Opens paseo\'s own view of the agent, outside the dashboard (not in the prototype)')}>${icon('terminal')}Open in paseo</button>${run.status === 'run' ? `<button class="btn danger" data-act="stop-run" data-run="${run.id}">${icon('x')}Stop run</button>` : `${st(run.status === 'stopped' ? 'fail' : run.status, run.status, true)}`}` })}
        <div class="row wrap" style="margin:-8px 0 16px;gap:8px">${live.map((r) => `<a class="chipsel${r.id === run.id ? ' on' : ''}" href="#/live/${r.id}"${r.id === run.id ? ' aria-current="page"' : ''}>${st('run', '')}${r.workflow} <span class="mono muted tiny">${r.repo ? r.repo + '#' + r.number : r.trigger}</span></a>`).join('')}</div>
        <div class="panel" style="margin-bottom:14px"><div class="panel-head"><h3>Steps</h3><span class="muted small">trigger <span class="mono">${run.trigger}</span> · from step order + <span class="mono">parallel:</span> · click a step</span><div class="tools"><span class="mono tiny muted">${run.steps.filter((s) => s.status === 'ok').length}/${steps.length} done</span></div></div><div id="graph">${graph(run, steps, lv.sel)}</div></div>
        <div class="run-layout">
          <div class="panel log-panel"><div class="panel-head"><h3>Log</h3>${run.status === 'run' ? '<span class="st" data-s="run"><i><span class="rec" style="background:var(--run)"></span></i>streaming</span>' : ''}
            <div class="tools">${UI.seg([['all', 'all steps']].concat(srcs.map((x) => [x, x])).concat([['tools', 'tools'], ['errors', 'errors']]), lv.f, 'log-f')}<button class="icon-btn${lv.follow ? ' on' : ''}" data-act="log-follow" aria-pressed="${lv.follow}" data-tip="${lv.follow ? 'Following new lines: click to stop' : 'Follow new lines'}" aria-label="Follow">${icon('download')}</button></div></div>
            <div class="log" id="log" data-volatile>${D.liveLog.slice(0, lv.n).map((l, i) => logLine(l, i)).join('')}<div class="l"><span class="t"></span><span class="src">…</span><span class="m"><span class="cursor-blink"></span></span></div></div></div>
          <div class="panel" id="insp" style="align-self:start">${stepInspector(run, steps, lv.sel)}</div>
        </div></div>`,
      after(main) {
        const log = UI.$('#log', main);
        if (lv.follow) log.scrollTop = log.scrollHeight;
        if (run.status === 'run') UI.every(UI.fast ? 400 : 1500, () => { if (lv.n >= D.liveLog.length) return; log.lastElementChild.insertAdjacentHTML('beforebegin', logLine(D.liveLog[lv.n], lv.n, true)); lv.n++; if (lv.follow) log.scrollTop = log.scrollHeight; });
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
  const hv = { q: '', status: new Set(), wf: '', repo: '', since: '', cost: '' };
  const SL = { ok: 'ok', run: 'running', fail: 'failed', wait: 'waiting', idle: 'parked', stopped: 'stopped' };
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
  const chipSel = (key, label, ic, opts) => `<label class="chip-select${hv[key] ? ' on' : ''}">${icon(ic)}<span class="sr-only">${label}</span><select data-on-change="hist-sel" data-k="${key}" aria-label="${label}">${opts.map(([v, l]) => `<option value="${esc(v)}"${String(hv[key]) === String(v) ? ' selected' : ''}>${esc(l)}</option>`).join('')}</select></label>`;
  function histRows() {
    const rows = D.runs.filter(histMatch);
    return rows.map((r) => `<tr class="click" data-href="#/${r.status === 'run' ? 'live' : 'runs'}/${r.id}">
      <td data-l="status">${st(r.status === 'stopped' ? 'fail' : r.status, SL[r.status] || r.status)}</td>
      <td data-l="run" class="mono"><a class="link-plain" href="#/${r.status === 'run' ? 'live' : 'runs'}/${r.id}">${r.id}</a></td>
      <td data-l="workflow"><b>${r.workflow}</b></td><td data-l="trigger" class="mono muted">${r.trigger}</td>
      <td data-l="target" class="mono">${UI.runTarget(r)}</td><td data-l="started" class="mono muted">${ago(r.started)} ago</td>
      <td data-l="duration" class="mono num">${r.finished ? Math.max(1, Math.round((new Date(r.finished) - new Date(r.started)) / 60000)) + 'm' : '…'}</td>
      ${UI.usageOn() ? `<td data-l="usage" class="num">${UI.runFig(r)}</td>` : ''}<td class="muted small ellipsis hide-phone" style="max-width:260px">${esc(r.error || r.detail || '')}</td></tr>`).join('') || `<tr class="empty-row"><td colspan="9">No run matches these filters. <button class="link" data-act="hist-clear">Clear filters</button></td></tr>`;
  }
  UI.route('runs', () => {
    const n = D.runs.filter(histMatch).length;
    return `<div class="page">
      ${UI.head('Run history', { sub: `${D.runs.length} of 500 kept · 14d retention`, actions: `<button class="btn" data-act="hist-export">${icon('download')}Export JSONL</button>` })}
      <div class="filters">
        <div class="input-icon">${icon('search')}<input class="input" placeholder="repo, #number, run id, error text…" value="${esc(hv.q)}" data-on-input="hist-q" aria-label="Search runs"></div>
        ${['ok', 'run', 'fail', 'wait', 'idle'].map((s) => `<button class="chipsel${hv.status.has(s) ? ' on' : ''}" data-act="hist-st" data-s="${s}" aria-pressed="${hv.status.has(s)}">${st(s, SL[s])}</button>`).join('')}
        ${chipSel('wf', 'Workflow', 'workflows', [['', 'workflow: any']].concat([...new Set(D.runs.map((r) => r.workflow))].map((w) => [w, w])))}
        ${chipSel('repo', 'Repo', 'branch', [['', 'repo: any']].concat([...new Set(D.runs.map((r) => r.repo).filter(Boolean))].map((w) => [w, w])))}
        ${chipSel('since', 'Time range', 'clock', [['', 'any time'], ['60', 'last 1h'], ['360', 'last 6h'], ['1440', 'last 24h']])}
        ${!UI.usageOn() ? '' : chipSel('cost', 'Provider', 'zap', [['', 'provider: any']].concat(Object.keys(D.usage.providers).map((p) => [p, UI.provLabel(p)])))}
        ${UI.folderChip()}<span class="count-note" id="hist-n">${n} of ${D.runs.length}</span>
      </div>
      <div class="panel"><table class="tbl responsive"><thead><tr><th>status</th><th>run</th><th>workflow</th><th>trigger</th><th>target</th><th>started</th><th class="num">took</th>${UI.usageOn() ? `<th class="num">usage · ${UI.measHead()}</th>` : ''}<th class="hide-phone">note</th></tr></thead><tbody id="hist-body">${histRows()}</tbody></table>
      <div class="panel-foot small muted"><span>Records are HMAC-signed in <span class="mono">${D.daemon.state_dir}/history/</span>; a record that fails its signature shows as tampered.</span></div></div>
    </div>`;
  });
  const histRefresh = () => { UI.$('#hist-body').innerHTML = histRows(); UI.$('#hist-n').textContent = `${D.runs.filter(histMatch).length} of ${D.runs.length}`; };
  UI.act('hist-q', (el) => { hv.q = el.value; histRefresh(); });
  UI.act('hist-st', (b) => { hv.status.has(b.dataset.s) ? hv.status.delete(b.dataset.s) : hv.status.add(b.dataset.s); b.classList.toggle('on'); b.setAttribute('aria-pressed', hv.status.has(b.dataset.s)); histRefresh(); });
  UI.act('hist-sel', (sel) => { hv[sel.dataset.k] = sel.value; sel.closest('.chip-select').classList.toggle('on', !!sel.value); histRefresh(); });
  UI.act('hist-clear', () => { Object.assign(hv, { q: '', wf: '', repo: '', since: '', cost: '' }); hv.status.clear(); UI.rerender(); });
  UI.act('hist-export', () => { const rows = D.runs.filter(histMatch); UI.download('conductor-runs.jsonl', rows.map((r) => JSON.stringify(r)).join('\n') + '\n', 'application/x-ndjson'); UI.toast(`Exported ${rows.length} runs`, 'conductor-runs.jsonl · scrubbed', 'ok'); });

  /* ---------- run detail: every step selectable, step-scoped tabs, run-scoped audit, a time axis ---------- */
  const dv = { tab: 'outputs', sel: {} };
  const SSL = Object.assign({}, SL, { idle: 'not reached' });
  const mmss = (ms) => { const s = Math.round(ms / 1000); return s < 60 ? `${s}s` : `${Math.floor(s / 60)}m${String(s % 60).padStart(2, '0')}s`; };
  const tabsFor = (s) => [s.outputs ? ['outputs', 'Outputs'] : null, s.inputs ? ['inputs', 'Inputs'] : null, s.log.length ? ['log', 'Log'] : null, s.diff ? ['diff', 'Diff'] : null].filter(Boolean);
  const ganttHTML = (r, rec, sel) => { const T = Math.max(1, rec.total), ticks = [0, 0.25, 0.5, 0.75, 1];
    return `<div class="gantt gantt-sel" role="listbox" aria-label="Steps of ${esc(r.id)}" data-run="${r.id}"><div></div><div class="gt-axis" aria-hidden="true">${ticks.map((f) => `<span style="left:${f * 100}%">${mmss(f * T)}</span>`).join('')}</div><div></div><div></div>
      ${rec.steps.map((s) => { const stt = s.status === 'stopped' ? 'fail' : s.status; return `<button type="button" class="gt-row${s.id === sel ? ' on' : ''}" role="option" aria-selected="${s.id === sel}" data-act="run-step" data-run="${r.id}" data-step="${s.id}"><span class="mono">${esc(s.id)}</span><span class="gt" data-s="${stt}">${s.t0 != null ? `<i style="left:${(s.t0 / T) * 100}%;width:${Math.max((s.ms / T) * 100, 0.8)}%" title="${esc(s.id)} · ${mmss(s.ms)}"></i>` : '<span class="muted tiny">not reached</span>'}</span><span>${st(stt, '')}</span><span class="mono num muted tiny">${s.t0 != null ? mmss(s.ms) : ''}</span></button>`; }).join('')}</div>`; };
  const stepPanel = (r, rec, s) => { const tabs = tabsFor(s); const tab = tabs.some(([v]) => v === dv.tab) ? dv.tab : (tabs[0] || [''])[0];
    const body = { outputs: () => UI.yaml(UI.stepOutputsYAML(r, s), { err: s.status === 'fail' ? UI.stepOutputsYAML(r, s).split('\n').map((l, i) => (/FAIL|error/.test(l) ? i + 1 : 0)).filter(Boolean) : [] }), inputs: () => UI.yaml(YAML.stringify(s.inputs)), log: () => `<div class="log" style="max-height:280px">${s.log.map(([k, t], i) => `<div class="l ${logCls(k)}"><span class="t">${UI.clock(new Date(new Date(r.started).getTime() + (s.t0 || 0) + (i * s.ms) / Math.max(1, s.log.length)).toISOString())}</span><span class="src">${esc(s.id)}</span><span class="m">${esc(t)}</span></div>`).join('')}</div>`, diff: () => UI.stepDiffHTML(s.diff) };
    const what = { verb: 'calls a verb', code: 'runs a command', agent: 'agent', decide: 'an agent decides', bot: 'hands to a bot' }[s.kind] || s.kind;
    return `<div class="panel" id="run-step"><div class="panel-head"><h3>Step <span class="mono">${esc(s.id)}</span></h3>${st(s.status === 'stopped' ? 'fail' : s.status, SSL[s.status] || s.status, true)}<span class="muted small">${esc(what)} · <span class="mono">${esc(s.what)}</span>${s.t0 != null ? ` · ${mmss(s.ms)}` : ''}${UI.usageOn() && s.cost ? ` · ${UI.runFig({ id: r.id + s.id, cost: s.cost, provider: UI.runProv(r), model_id: UI.runModel(r) })}` : ''}</span>
      <div class="tools">${tabs.length ? UI.seg(tabs, tab, 'run-tab') : ''}${r.status !== 'run' && s.status !== 'idle' ? `<button class="btn sm ghost" data-act="retry" data-run="${r.id}" data-from="${s.id}">${icon('undo')}Retry from here</button>` : ''}</div></div>
      <div class="panel-body">${s.status === 'idle' ? `<div class="empty small"><span class="serif">Not reached</span>The run ${r.status === 'idle' ? 'was parked before any step ran' : `stopped at ${esc(rec.failAt || rec.waitAt || '')}`}, so this step never ran. Its inputs would have been:</div>${UI.yaml(YAML.stringify(s.inputs || {}))}` : tab ? body[tab]() : '<div class="muted small">This step recorded nothing.</div>'}</div></div>`; };
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
      ${UI.head(r.workflow, { crumbs: `<a href="#/runs">Run history</a> ${icon('chev')} ${r.id}`, sub: r.repo ? UI.runTarget(r) + ' · ' + r.trigger : r.trigger,
        actions: `${st(r.status === 'stopped' ? 'fail' : r.status, SL[r.status], true)}<button class="btn" data-act="retry" data-run="${r.id}" data-from="${rec.failAt || ''}">${icon('undo')}Retry…</button>${same.length ? `<a class="btn" href="#/runs/${r.id}/compare">${icon('split')}Compare…</a>` : ''}<button class="btn ghost" data-act="run-record" data-run="${r.id}" data-tip="The signed JSON record: every step's inputs, outputs and timing, scrubbed">${icon('download')}Download run record</button>` })}
      ${storyHTML(r)}
      <div class="grid g-side">
        <div class="stack">
          <div class="panel"><div class="panel-head"><h3>Timeline</h3><span class="muted small">${mmss(rec.total)} total · pick a step, or use ↑ ↓</span></div><div class="panel-body tight">${ganttHTML(r, rec, s.id)}</div></div>
          ${stepPanel(r, rec, s)}
        </div>
        <div class="stack">
          <div class="panel"><div class="panel-head"><h3>Run</h3></div><div class="panel-body"><dl class="kv">
            <dt>id</dt><dd class="mono">${r.id}</dd><dt>target</dt><dd class="mono">${UI.runTarget(r)}</dd><dt>trigger</dt><dd class="mono">${esc(r.trigger)}</dd><dt>started</dt><dd class="mono">${new Date(r.started).toLocaleString()}</dd>
            ${UI.usageOn() ? `<dt>usage</dt><dd>${UI.runFig(r)} <span class="mono tiny muted">${UI.runModel(r)} · ${UI.srcLabel(UI.isApi() || UI.usageSource() ? 'source' : 'observed')}</span></dd>` : ''}<dt>signature</dt><dd>${st('ok', 'HMAC verified')}</dd><dt>retry of</dt><dd class="mono">${r.retry_of ? `<a class="link" href="#/runs/${r.retry_of}">${r.retry_of}</a>` : '—'}</dd></dl></div></div>
          <div class="panel"><div class="panel-head"><h3>Audit for this run</h3><a class="btn sm ghost" style="margin-left:auto" href="#/logs/audit?run=${r.id}">All ${icon('chev')}</a></div><div class="list run-audit">${audit.map((a) => `<div class="item small"><span class="mono tiny muted" style="width:44px">${ago(a.ts)}</span><span class="mono" style="color:var(--run)">${a.event}</span><span class="muted ellipsis grow">${esc(a.detail)}</span></div>`).join('') || '<div class="empty small">No audit events for this run.</div>'}</div></div>
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
    const head = (sub) => UI.head(`Compare ${a.id}`, { crumbs: `<a href="#/runs">Run history</a> ${icon('chev')} <a href="#/runs/${a.id}">${a.id}</a>`, sub });
    if (!other) return `<div class="page">${head(`pick another ${esc(a.workflow)} run`)}<div class="panel"><div class="list">${same.map((x) => `<a class="item" href="#/runs/${a.id}/compare/${x.id}">${st(x.status === 'stopped' ? 'fail' : x.status, SL[x.status])}<span class="mono">${x.id}</span><span class="mono muted">${UI.runTarget(x)}</span><span class="muted small grow">${ago(x.started)} ago</span>${icon('chev')}</a>`).join('') || '<div class="empty small">No other run of this workflow to compare with.</div>'}</div></div></div>`;
    const b = D.runs.find((x) => x.id === other); if (!b) return `<div class="page">${head('')}<div class="empty"><span class="serif">No run ${esc(other)}</span></div></div>`;
    const ra = UI.runRecord(a), rb = UI.runRecord(b), ids = [...new Set(ra.steps.map((s) => s.id).concat(rb.steps.map((s) => s.id)))];
    const pick = cv.step && ids.includes(cv.step) ? cv.step : (ids.find((i) => { const x = ra.steps.find((s) => s.id === i), y = rb.steps.find((s) => s.id === i); return !x || !y || x.status !== y.status; }) || ids[0]);
    const cell = (s) => (s ? `${st(s.status === 'stopped' ? 'fail' : s.status, SSL[s.status] || s.status)} <span class="mono tiny muted">${s.t0 != null ? mmss(s.ms) : ''}</span>` : '<span class="muted">—</span>');
    const delta = (x, y) => (x && y && x.t0 != null && y.t0 != null ? `<span class="mono tiny ${y.ms > x.ms * 1.2 ? 'warn-t' : 'muted'}">${y.ms >= x.ms ? '+' : '−'}${mmss(Math.abs(y.ms - x.ms))}</span>` : '');
    const sa = ra.steps.find((s) => s.id === pick), sb = rb.steps.find((s) => s.id === pick);
    const yA = sa && sa.outputs ? UI.stepOutputsYAML(a, sa) : '', yB = sb && sb.outputs ? UI.stepOutputsYAML(b, sb) : '';
    return `<div class="page">${head(`${esc(a.workflow)} · ${UI.runTarget(a)} vs ${UI.runTarget(b)}`)}
      <div class="row wrap" style="gap:8px;margin-bottom:12px"><span class="muted small">Comparing</span><a class="chip" href="#/runs/${a.id}">${st(a.status === 'stopped' ? 'fail' : a.status, '')}<span class="mono">${a.id}</span></a><span class="muted">with</span><label class="chip-select on">${icon('split')}<select data-on-change="cmp-other" data-run="${a.id}" aria-label="Compare with">${same.map((x) => `<option value="${x.id}"${x.id === b.id ? ' selected' : ''}>${x.id} · ${SL[x.status] || x.status} · ${UI.runTarget(x)}</option>`).join('')}</select></label></div>
      <div class="panel"><table class="tbl cmp-tbl"><thead><tr><th>step</th><th>${a.id}</th><th>${b.id}</th><th class="num">Δ time</th></tr></thead><tbody>
        ${ids.map((i) => { const x = ra.steps.find((s) => s.id === i), y = rb.steps.find((s) => s.id === i); const differs = !x || !y || x.status !== y.status || JSON.stringify(x.outputs) !== JSON.stringify(y.outputs); return `<tr class="click${i === pick ? ' on' : ''}" data-act="cmp-step" data-step="${esc(i)}" tabindex="0"><td class="mono">${esc(i)}${differs ? ' <span class="tag" data-s="wait">differs</span>' : ''}</td><td>${cell(x)}</td><td>${cell(y)}</td><td class="num">${delta(x, y)}</td></tr>`; }).join('')}
        <tr><td class="muted">total</td><td class="mono tiny">${mmss(ra.total)}</td><td class="mono tiny">${mmss(rb.total)}</td><td class="num">${delta({ t0: 0, ms: ra.total }, { t0: 0, ms: rb.total })}</td></tr></tbody></table></div>
      <div class="panel" style="margin-top:14px"><div class="panel-head"><h3>Outputs of <span class="mono">${esc(pick)}</span></h3><span class="muted small">${a.id} → ${b.id}</span></div><div class="panel-body">${yA || yB ? UI.diffView(yA, yB, 'unified', { file: `${pick} outputs` }).html : '<div class="muted small">Neither run recorded outputs for this step.</div>'}</div></div>
      ${sa && sb && (sa.diff || sb.diff) ? `<div class="grid g2" style="margin-top:14px"><div class="panel"><div class="panel-head"><h3>${a.id} · ${esc(pick)} diff</h3></div><div class="panel-body">${sa.diff ? UI.stepDiffHTML(sa.diff) : '<span class="muted small">no diff</span>'}</div></div><div class="panel"><div class="panel-head"><h3>${b.id} · ${esc(pick)} diff</h3></div><div class="panel-body">${sb.diff ? UI.stepDiffHTML(sb.diff) : '<span class="muted small">no diff</span>'}</div></div></div>` : ''}</div>`;
  }, { nav: 'runs' });
  UI.act('cmp-step', (b) => { cv.step = b.dataset.step; UI.rerender(); });
  UI.act('cmp-other', (el) => UI.go(`#/runs/${el.dataset.run}/compare/${el.value}`));

  /* ---------- needs you ---------- */
  UI.route('attention', () => `<div class="page">
    ${UI.head('Needs you', { sub: `${D.attention.length} items`, actions: UI.folderChip() + UI.seg([['all', 'all'], ['approvals', 'approvals'], ['failures', 'failures'], ['waiting', 'waiting']], UI.attnFilter || 'all', 'attn-f') })}
    <div class="grid g-side">
      <div class="panel attn">${UI.attentionList()}</div>
      <div class="stack">
        <div class="callout ember"><b>Approvals are yours alone.</b> Each one is a WebAuthn assertion over that proposal's digest, made on this device with user verification. No agent can reach this page, and no verb, chat text or peer message approves anything.</div>
        <div class="panel"><div class="panel-head"><h3>Notify me</h3></div><div class="panel-body stack tight small">
          <label class="row">${UI.switchBtn(D.prefs.notifyApprovals, 'pref', 'data-k="notifyApprovals" aria-label="Browser notifications for new approvals"')}Browser notifications for new approvals</label>
          <label class="row">${UI.switchBtn(D.prefs.notifyPush, 'pref', 'data-k="notifyPush" aria-label="Push approvals need me"')}Push approvals (browser 2FA) need me</label>
          <label class="row">${UI.switchBtn(D.prefs.notifyFailed, 'pref', 'data-k="notifyFailed" aria-label="Every failed run"')}Every failed run</label>
          <div class="muted tiny">Off-box alerts stay with your <span class="mono">conductor.*</span> lifecycle triggers (Slack, PagerDuty…).</div></div></div>
      </div></div></div>`);
  UI.act('attn-f', (b) => { UI.attnFilter = b.dataset.v; UI.rerender(); });
  UI.act('pref', (b) => { D.prefs[b.dataset.k] = !D.prefs[b.dataset.k]; S.save(); b.classList.toggle('on', D.prefs[b.dataset.k]); b.setAttribute('aria-checked', D.prefs[b.dataset.k]); });
})();
