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
      const what = s.uses || (s.kind === 'agent' ? `${s.runtime || 'paseo'} · ${s.model || 'default'}` : s.kind === 'code' ? `run: ${s.run || 'cli'}` : s.kind);
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
    return `<div class="panel-head"><h3>${s.id}</h3>${st(r.status === 'stopped' ? 'fail' : r.status || 'idle', r.status === 'stopped' ? 'stopped' : null, true)}<div class="tools"><button class="btn sm" data-act="retry" data-run="${run.id}" data-from="${s.id}">${icon('undo')}Retry from here</button></div></div>
      <div class="panel-body"><dl class="kv">
        <dt>kind</dt><dd class="mono">${s.kind}${s.uses ? ' · ' + s.uses : ''}</dd>
        ${s.kind === 'agent' ? `<dt>runtime</dt><dd class="mono">${s.runtime || 'paseo'} · fleet ${s.model || 'default'} → claude-sonnet-5 (rung 1)</dd><dt>sandbox</dt><dd>${tag('namespace', 'ok')} ${tag('egress: proxy allowlist')}</dd>` : ''}
        <dt>after</dt><dd class="mono">${s.needs.join(', ') || 'trigger'}</dd>
        <dt>started</dt><dd class="mono">${r.started ? ago(r.started) + ' ago' : r.status === 'ok' ? 'done in ' + dur(r.duration_ms) : '—'}</dd>
        <dt>cost</dt><dd class="mono">${usd(r.cost_usd)}</dd></dl>
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
          actions: `<span class="mono small muted">${elapsed}m elapsed · ${usd(run.cost)} · budget $2.00</span><button class="btn" ${UI.off('Opens paseo\'s own view of the agent, outside the dashboard (not in the prototype)')}>${icon('terminal')}Open in paseo</button>${run.status === 'run' ? `<button class="btn danger" data-act="stop-run" data-run="${run.id}">${icon('x')}Stop run</button>` : `${st(run.status === 'stopped' ? 'fail' : run.status, run.status, true)}`}` })}
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
    if (hv.status.size && !hv.status.has(r.status === 'stopped' ? 'fail' : r.status)) return false;
    if (hv.wf && r.workflow !== hv.wf) return false;
    if (hv.repo && r.repo !== hv.repo) return false;
    if (hv.since && Date.now() - new Date(r.started) > +hv.since * 60000) return false;
    if (hv.cost && !(r.cost >= +hv.cost)) return false;
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
      <td data-l="target" class="mono">${r.repo ? r.repo + '#' + r.number : '—'}</td><td data-l="started" class="mono muted">${ago(r.started)} ago</td>
      <td data-l="duration" class="mono num">${r.finished ? Math.max(1, Math.round((new Date(r.finished) - new Date(r.started)) / 60000)) + 'm' : '…'}</td>
      <td data-l="cost" class="mono num">${usd(r.cost)}</td><td class="muted small ellipsis hide-phone" style="max-width:260px">${esc(r.error || r.detail || '')}</td></tr>`).join('') || `<tr class="empty-row"><td colspan="9">No run matches these filters. <button class="link" data-act="hist-clear">Clear filters</button></td></tr>`;
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
        ${chipSel('cost', 'Minimum cost', 'dollar', [['', 'cost: any'], ['0.1', '≥ $0.10'], ['0.5', '≥ $0.50'], ['1', '≥ $1.00']])}
        <span class="count-note" id="hist-n">${n} of ${D.runs.length}</span>
      </div>
      <div class="panel"><table class="tbl responsive"><thead><tr><th>status</th><th>run</th><th>workflow</th><th>trigger</th><th>target</th><th>started</th><th class="num">took</th><th class="num">cost</th><th class="hide-phone">note</th></tr></thead><tbody id="hist-body">${histRows()}</tbody></table>
      <div class="panel-foot small muted"><span>Records are HMAC-signed in <span class="mono">${D.daemon.state_dir}/history/</span>; a record that fails its signature shows as tampered.</span></div></div>
    </div>`;
  });
  const histRefresh = () => { UI.$('#hist-body').innerHTML = histRows(); UI.$('#hist-n').textContent = `${D.runs.filter(histMatch).length} of ${D.runs.length}`; };
  UI.act('hist-q', (el) => { hv.q = el.value; histRefresh(); });
  UI.act('hist-st', (b) => { hv.status.has(b.dataset.s) ? hv.status.delete(b.dataset.s) : hv.status.add(b.dataset.s); b.classList.toggle('on'); b.setAttribute('aria-pressed', hv.status.has(b.dataset.s)); histRefresh(); });
  UI.act('hist-sel', (sel) => { hv[sel.dataset.k] = sel.value; sel.closest('.chip-select').classList.toggle('on', !!sel.value); histRefresh(); });
  UI.act('hist-clear', () => { Object.assign(hv, { q: '', wf: '', repo: '', since: '', cost: '' }); hv.status.clear(); UI.rerender(); });
  UI.act('hist-export', () => { const rows = D.runs.filter(histMatch); UI.download('conductor-runs.jsonl', rows.map((r) => JSON.stringify(r)).join('\n') + '\n', 'application/x-ndjson'); UI.toast(`Exported ${rows.length} runs`, 'conductor-runs.jsonl · scrubbed', 'ok'); });

  /* ---------- run detail ---------- */
  const dv = { tab: 'outputs' };
  UI.route('runs/:id', ({ id }) => {
    const r = D.runs.find((x) => x.id === id);
    if (!r) return `<div class="page"><div class="empty"><span class="serif">No run ${esc(id)}</span><a class="btn" href="#/runs">Run history</a></div></div>`;
    if (r.status === 'run') { setTimeout(() => UI.go('#/live/' + r.id)); return ''; }
    const fail = r.status === 'fail' || r.status === 'stopped';
    const steps = r.steps.length ? r.steps.map((s, i, a) => [s.id, s.status === 'stopped' ? 'fail' : s.status, (i / a.length) * 100, ((i + 1) / a.length) * 100, s.cost_usd]) : fail ? [['logs', 'ok', 0, 4, 0], ['triage', 'ok', 4, 12, 0.03], ['fix', 'ok', 12, 70, 0.98], ['test', 'fail', 70, 96, 0.11], ['push', 'idle'], ['comment', 'idle']] : [['fetch', 'ok', 0, 3, 0], ['lint', 'ok', 3, 18, 0], ['review', 'ok', 3, 82, 0.39], ['post', 'ok', 88, 100, 0]];
    const focus = fail ? (r.failed_step || (steps.find((s) => s[1] === 'fail') || steps[0])[0]) : (steps.find((s) => s[0] === 'review') || steps[steps.length - 1])[0];
    const gantt = steps.map(([n, s, a, b, c]) => `<div class="mono">${n}</div><div class="gt" data-s="${s}">${a != null && s !== 'idle' ? `<i style="left:${a}%;width:${Math.max(b - a, 1)}%"></i>` : '<span class="muted tiny">not reached</span>'}</div><div>${st(s, '')}</div><div class="mono num muted">${c != null ? usd(c) : ''}</div>`).join('');
    const content = {
      outputs: UI.yaml(fail ? `exit_code: 1\nstdout: |\n  --- FAIL: TestCheckout (0.31s)\n      checkout_test.go:142: total = 10.49, want 10.50\n  --- FAIL: TestRefund (0.02s)\n      refund_test.go:88: got 1999, want 2000\nduration_ms: 26114  # scrubbed: 0 secrets` : `verdict: request_changes\ncomments: 3\nsummary: "Two N+1 queries in the orders handler; missing index on (tenant_id, created_at)."\ntokens: 30110\ncost_usd: 0.39`, { err: fail ? [3, 5] : [] }),
      inputs: UI.yaml(`repo: ${r.repo || '—'}\nnumber: ${r.number || 0}\nworkdir: <state>/runs/${r.id}/${focus}\ncommand: [go, test, ./...]`),
      log: `<div class="log" style="max-height:260px">${D.liveLog.slice(0, 8).map(([s, k, t], i) => `<div class="l ${logCls(k)}"><span class="t">${UI.clock(new Date(new Date(r.started).getTime() + i * 9000).toISOString())}</span><span class="src">${s}</span><span class="m">${esc(t)}</span></div>`).join('')}</div>`,
      diff: `<div class="diff"><div class="h">internal/billing/refund.go</div><div class="dl del"><span>42</span><span></span><span>-</span><span>	return cents * bps / 10000</span></div><div class="dl add"><span></span><span>42</span><span>+</span><span>	return roundHalfEven(cents*bps, 10000)</span></div></div>`,
    };
    const runAudit = D.audit.filter((a) => a.run === r.id).concat(D.audit.filter((a) => a.run !== r.id).slice(4, 7));
    return `<div class="page">
      ${UI.head(r.workflow, { crumbs: `<a href="#/runs">Run history</a> ${icon('chev')} ${r.id}`, sub: r.repo ? `${r.repo}#${r.number}` : r.trigger,
        actions: `${st(r.status === 'stopped' ? 'fail' : r.status, SL[r.status], true)}<button class="btn" data-act="retry" data-run="${r.id}" data-from="${fail ? focus : ''}">${icon('undo')}Retry${fail ? ' from ' + focus : ''}</button><button class="btn ghost" data-act="run-record" data-run="${r.id}">${icon('download')}Record</button>` })}
      ${fail ? `<div class="callout danger" style="margin-bottom:14px"><b>${r.status === 'stopped' ? 'Stopped' : 'Failed at step ' + focus}.</b> <span class="mono">${esc(r.error || '')}</span></div>` : ''}
      <div class="grid g-side">
        <div class="stack">
          <div class="panel"><div class="panel-head"><h3>Timeline</h3><span class="muted small">${r.finished ? Math.max(1, Math.round((new Date(r.finished) - new Date(r.started)) / 60000)) + 'm total' : ''}</span></div><div class="panel-body tight"><div class="gantt">${gantt}</div></div></div>
          <div class="panel"><div class="panel-head"><h3>Step <span class="mono">${focus}</span></h3><div class="tools">${UI.seg([['outputs', 'outputs'], ['inputs', 'inputs'], ['log', 'log'], ['diff', 'diff']], dv.tab, 'run-tab')}</div></div><div class="panel-body">${content[dv.tab]}</div></div>
        </div>
        <div class="stack">
          <div class="panel"><div class="panel-head"><h3>Run</h3></div><div class="panel-body"><dl class="kv">
            <dt>id</dt><dd class="mono">${r.id}</dd><dt>trigger</dt><dd class="mono">${r.trigger}</dd><dt>started</dt><dd class="mono">${new Date(r.started).toLocaleString()}</dd>
            <dt>cost</dt><dd class="mono">${usd(r.cost)} ${r.tokens ? '· ' + (r.tokens / 1000).toFixed(1) + 'k tok' : ''}</dd><dt>signature</dt><dd>${st('ok', 'HMAC verified')}</dd><dt>retry of</dt><dd class="mono">${r.retry_of ? `<a class="link" href="#/runs/${r.retry_of}">${r.retry_of}</a>` : '—'}</dd></dl></div></div>
          <div class="panel"><div class="panel-head"><h3>Audit for this run</h3><a class="btn sm ghost" style="margin-left:auto" href="#/logs/audit">All ${icon('chev')}</a></div><div class="list">${runAudit.map((a) => `<div class="item small"><span class="mono tiny muted" style="width:44px">${ago(a.ts)}</span><span class="mono" style="color:var(--run)">${a.event}</span><span class="muted ellipsis grow">${esc(a.detail)}</span></div>`).join('')}</div></div>
        </div></div></div>`;
  });
  UI.act('run-tab', (b) => { dv.tab = b.dataset.v; UI.rerender(); });
  UI.act('run-record', (b) => { const r = D.runs.find((x) => x.id === b.dataset.run); UI.download(`${r.id}.json`, JSON.stringify(r, null, 2)); UI.toast('Downloaded the record', `${r.id}.json · signed, scrubbed`, 'ok'); });

  /* ---------- needs you ---------- */
  UI.route('attention', () => `<div class="page">
    ${UI.head('Needs you', { sub: `${D.attention.length} items`, actions: UI.seg([['all', 'all'], ['approvals', 'approvals'], ['failures', 'failures'], ['waiting', 'waiting']], UI.attnFilter || 'all', 'attn-f') })}
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
