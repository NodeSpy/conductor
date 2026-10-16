/* Live runs (step graph + streaming log), run history, run detail, needs-you queue. */
(function () {
  const { icon, st, tag, usd, ago, dur, esc } = UI;
  const KIND_IC = { uses: 'zap', agent: 'sparkle', code: 'code', gate: 'shield', decide: 'diamond', trigger: 'live' };

  /* Lay a workflow's steps out left→right by dependency depth. */
  UI.layout = (steps, opts = {}) => {
    const W = opts.w || 172, GX = opts.gx || 64, GY = opts.gy || 22, H = opts.h || 78, X0 = opts.x0 || 28, Y0 = opts.y0 || 28;
    const depth = {};
    const d = (s) => depth[s.id] ?? (depth[s.id] = s.needs.length ? 1 + Math.max(...s.needs.map((n) => d(steps.find((x) => x.id === n)))) : 0);
    steps.forEach(d);
    const cols = {};
    steps.forEach((s) => (cols[depth[s.id]] = cols[depth[s.id]] || []).push(s));
    const maxRows = Math.max(...Object.values(cols).map((c) => c.length));
    const pos = {};
    Object.entries(cols).forEach(([c, list]) => list.forEach((s, i) => {
      const off = ((maxRows - list.length) * (H + GY)) / 2;
      pos[s.id] = { x: X0 + c * (W + GX), y: Y0 + off + i * (H + GY) };
    }));
    const width = X0 * 2 + (Object.keys(cols).length) * (W + GX) - GX, height = Y0 * 2 + maxRows * (H + GY) - GY;
    const edges = steps.flatMap((s) => s.needs.map((n) => {
      const a = pos[n], b = pos[s.id], x1 = a.x + W, y1 = a.y + H / 2, x2 = b.x, y2 = b.y + H / 2, mx = (x1 + x2) / 2;
      return { from: n, to: s.id, d: `M${x1} ${y1} C${mx} ${y1} ${mx} ${y2} ${x2} ${y2}` };
    }));
    return { pos, edges, width, height, W, H };
  };

  const stepStatus = (run, id) => (run.steps.find((s) => s.id === id) || {}).status || 'idle';

  function graph(run, wf, sel) {
    const L = UI.layout(wf.steps, { w: 150, gx: 34, x0: 20 });
    const edges = L.edges.map((e) => {
      const a = stepStatus(run, e.from), b = stepStatus(run, e.to);
      return `<path class="${b === 'run' ? 'active' : a === 'ok' && b !== 'idle' ? 'done' : a === 'ok' ? 'done' : ''}" d="${e.d}"/>`;
    }).join('');
    const nodes = wf.steps.map((s) => {
      const r = run.steps.find((x) => x.id === s.id) || {}, stt = r.status || 'idle', p = L.pos[s.id];
      const what = s.uses || (s.kind === 'agent' ? `${s.runtime} · ${s.model}` : s.kind === 'code' ? `run: ${s.run}` : s.kind === 'gate' ? s.checks.join(', ') : 'decide');
      return `<div class="node${s.id === sel ? ' sel' : ''}" data-s="${stt}" data-step="${s.id}" style="left:${p.x}px;top:${p.y}px;width:${L.W}px">
        <div class="nk"><span>${s.kind}</span><span>${stt === 'run' ? 'running' : stt === 'ok' ? dur(r.duration_ms) : ''}</span></div>
        <div class="nn">${s.id}</div>
        <div class="nm"><span class="muted mono ellipsis" style="font-size:10px;max-width:110px">${esc(what)}</span>${st(stt, '')}</div>
        ${stt === 'run' ? '<div class="prog"><i></i></div>' : ''}
      </div>`;
    }).join('');
    return `<div class="graph" style="height:${L.height + 10}px"><div style="position:relative;width:${L.width}px;height:${L.height}px"><svg class="edges" width="${L.width}" height="${L.height}">${edges}</svg>${nodes}</div></div>`;
  }

  function logLine([src, kind, text], i, fresh) {
    const t = new Date(Date.now() - (D.liveLog.length - i) * 17000).toTimeString().slice(0, 8);
    const cls = { err: 'err', warn: 'warn', tool: 'tool', ok: 'ok' }[kind] || '';
    return `<div class="l ${cls}${fresh ? ' new' : ''}"><span class="t">${t}</span><span class="src">${src}</span><span class="m">${esc(text)}</span></div>`;
  }

  function stepInspector(run, wf, id) {
    const s = wf.steps.find((x) => x.id === id), r = run.steps.find((x) => x.id === id) || {};
    return `<div class="panel-head"><h3>${id}</h3>${st(r.status || 'idle', null, true)}<div class="tools"><button class="btn sm" data-retry="${id}">${UI.icon('undo')}Retry from here</button></div></div>
      <div class="panel-body"><dl class="kv">
        <dt>kind</dt><dd class="mono">${s.kind}${s.uses ? ' · ' + s.uses : ''}</dd>
        ${s.kind === 'agent' ? `<dt>runtime</dt><dd class="mono">${s.runtime} · fleet ${s.model} → claude-sonnet-5 (rung 1)</dd><dt>sandbox</dt><dd>${tag('namespace', 'ok')} ${tag('egress: proxy allowlist')} ${tag('identity agent:fixer@acme/api')}</dd>` : ''}
        <dt>needs</dt><dd class="mono">${s.needs.join(', ') || '—'}</dd>
        <dt>started</dt><dd class="mono">${r.started ? ago(r.started) + ' ago' : r.status === 'ok' ? 'done in ' + dur(r.duration_ms) : '—'}</dd>
        <dt>cost</dt><dd class="mono">${usd(r.cost_usd)}${s.kind === 'agent' ? ' · 41.2k tokens' : ''}</dd>
      </dl>
      ${s.kind === 'agent' ? `<div class="caps" style="margin:14px 0 6px">Proposed diff (so far)</div><div class="diff"><div class="h">internal/billing/refund.go</div>
        <div class="dl"><span>41</span><span>41</span><span> </span><span>func applyFee(cents int64, bps int64) int64 {</span></div>
        <div class="dl del"><span>42</span><span></span><span>-</span><span>	return cents * bps / 10000</span></div>
        <div class="dl add"><span></span><span>42</span><span>+</span><span>	q, r := cents*bps/10000, cents*bps%10000</span></div>
        <div class="dl add"><span></span><span>43</span><span>+</span><span>	return q + roundHalfEven(r, q)</span></div>
        <div class="dl"><span>43</span><span>44</span><span> </span><span>}</span></div></div>` : ''}
      </div>`;
  }

  /* ---------- live ---------- */
  UI.route('live/:id?', ({ id }) => {
    const live = D.runs.filter((r) => r.status === 'run');
    const run = D.runs.find((r) => r.id === id) || live[0];
    const wf = D.workflows.find((w) => w.name === run.workflow && w.steps.length) || D.workflows[1];
    let sel = (run.steps.find((s) => s.status === 'run') || run.steps[0] || {}).id;
    if (!wf.steps.find((s) => s.id === sel)) sel = wf.steps[0].id;
    const elapsed = Math.round((Date.now() - new Date(run.started)) / 60000);
    return {
      toString: () => `<div class="page wide">
        ${UI.head(`${run.workflow}`, { crumbs: `<a href="#/live">Live</a> ${icon('chev')} ${run.id}`, sub: run.repo ? `${run.repo}#${run.number}` : run.trigger,
          actions: `<span class="mono small muted">${elapsed}m elapsed · ${usd(run.cost)} · budget $2.00</span><button class="btn">${icon('terminal')}Open in paseo</button><button class="btn danger" id="stop">${icon('x')}Stop run</button>` })}
        <div class="row wrap" style="margin:-8px 0 16px;gap:8px">${live.map((r) => `<a class="chipsel${r.id === run.id ? ' on' : ''}" href="#/live/${r.id}">${st('run', '')}${r.workflow} <span class="mono muted tiny">${r.repo ? r.repo + '#' + r.number : r.trigger}</span></a>`).join('')}</div>
        <div class="panel" style="margin-bottom:14px"><div class="panel-head"><h3>Steps</h3><span class="muted small">trigger <span class="mono">${run.trigger}</span> · DAG from <span class="mono">needs:</span> · click a step</span><div class="tools"><span class="mono tiny muted">${run.steps.filter((s) => s.status === 'ok').length}/${wf.steps.length} done</span></div></div>${graph(run, wf, sel)}</div>
        <div class="run-layout">
            <div class="panel log-panel"><div class="panel-head"><h3>Log</h3><span class="st" data-s="run"><i><span class="rec" style="background:var(--run)"></span></i>streaming</span>
              <div class="tools"><div class="seg" id="logf"><button class="on">all steps</button><button>fix</button><button>tools</button><button>errors</button></div><button class="icon-btn" title="Follow">${icon('download')}</button></div></div>
              <div class="log" id="log">${D.liveLog.slice(0, 9).map((l, i) => logLine(l, i)).join('')}<div class="l"><span class="t"></span><span class="src">fix</span><span class="m"><span class="cursor-blink"></span></span></div></div></div>
          <div class="panel" id="insp" style="align-self:start">${stepInspector(run, wf, sel)}</div>
        </div></div>`,
      after(main) {
        const log = UI.$('#log', main); let i = 9;
        log.scrollTop = log.scrollHeight;
        UI.every(1500, () => {
          if (i >= D.liveLog.length) return;
          log.lastElementChild.insertAdjacentHTML('beforebegin', logLine(D.liveLog[i], i, true)); i++;
          log.scrollTop = log.scrollHeight;
        });
        UI.bind(main, '.node', 'click', (e, n) => { UI.$$('.node', main).forEach((x) => x.classList.toggle('sel', x === n)); UI.$('#insp').innerHTML = stepInspector(run, wf, n.dataset.step); });
        UI.bind(main, '#logf button', 'click', (e, b) => UI.$$('#logf button').forEach((x) => x.classList.toggle('on', x === b)));
        UI.bind(main, '#stop', 'click', () => UI.modal(`<div class="modal-head"><h2>Stop ${run.id}?</h2><button class="icon-btn" data-close>${icon('x')}</button></div><div class="modal-body"><p>The running step gets a short grace to checkpoint, then its agent is cancelled. The run is recorded as <b>stopped</b> and can be retried from any step.</p></div><div class="modal-foot"><button class="btn ghost" data-close>Keep running</button><button class="btn danger" data-close onclick="UI.toast('Stopping ${run.id}','grace 10s · then cancel','fail')">Stop run</button></div>`));
      },
    };
  }, { nav: 'live' });

  /* ---------- history ---------- */
  UI.route('runs', () => {
    const rows = D.runs.map((r) => `<tr class="click" onclick="location.hash='#/${r.status === 'run' ? 'live' : 'runs'}/${r.id}'">
      <td data-l="status">${st(r.status, r.status === 'run' ? 'running' : r.status === 'idle' ? 'parked' : r.status === 'wait' ? 'waiting' : null)}</td>
      <td data-l="run" class="mono">${r.id}</td>
      <td data-l="workflow"><b>${r.workflow}</b></td>
      <td data-l="trigger" class="mono muted">${r.trigger}</td>
      <td data-l="target" class="mono">${r.repo ? r.repo + '#' + r.number : '—'}</td>
      <td data-l="started" class="mono muted">${ago(r.started)} ago</td>
      <td data-l="duration" class="mono num">${r.finished ? Math.max(1, Math.round((new Date(r.finished) - new Date(r.started)) / 60000)) + 'm' : '…'}</td>
      <td data-l="cost" class="mono num">${usd(r.cost)}</td>
      <td class="muted small ellipsis hide-phone" style="max-width:260px">${esc(r.error || r.detail || '')}</td></tr>`).join('');
    return `<div class="page">
      ${UI.head('Run history', { sub: `${D.runs.length} of 500 kept · 14d retention`, actions: `<button class="btn">${icon('download')}Export JSONL</button>` })}
      <div class="filters">
        <div class="input-icon">${icon('search')}<input class="input" placeholder="repo, #number, run id, error text…"></div>
        ${['ok', 'run', 'fail', 'wait', 'idle'].map((s, i) => `<button class="chipsel${i === 2 ? ' on' : ''}" onclick="this.classList.toggle('on')">${st(s, { ok: 'ok', run: 'running', fail: 'failed', wait: 'waiting', idle: 'parked' }[s])}</button>`).join('')}
        <button class="chipsel">${icon('workflows')}workflow: any ${icon('chevd')}</button>
        <button class="chipsel">${icon('branch')}repo: any ${icon('chevd')}</button>
        <button class="chipsel">${icon('clock')}last 24h ${icon('chevd')}</button>
        <button class="chipsel">${icon('dollar')}cost ≥ … ${icon('chevd')}</button>
      </div>
      <div class="panel"><table class="tbl responsive"><thead><tr><th>status</th><th>run</th><th>workflow</th><th>trigger</th><th>target</th><th>started</th><th class="num">took</th><th class="num">cost</th><th class="hide-phone">note</th></tr></thead><tbody>${rows}</tbody></table>
      <div class="panel-foot small muted"><span>Records are HMAC-signed in <span class="mono">${D.daemon.state_dir}/history/</span>; a record that fails its signature shows as tampered.</span><span style="margin-left:auto" class="mono">1–${D.runs.length}</span></div></div>
    </div>`;
  });

  /* ---------- run detail ---------- */
  UI.route('runs/:id', ({ id }) => {
    const r = D.runs.find((x) => x.id === id) || D.runs[4];
    if (r.status === 'run') { setTimeout(() => UI.go('#/live/' + r.id)); return ''; }
    const fail = r.status === 'fail';
    const steps = fail ? [['logs', 'ok', 0, 4, 0], ['triage', 'ok', 4, 12, 0.03], ['fix', 'ok', 12, 70, 0.98], ['test', 'fail', 70, 96, 0.11], ['push', 'idle'], ['comment', 'idle']]
      : [['fetch', 'ok', 0, 3, 0], ['lint', 'ok', 3, 18, 0], ['review', 'ok', 3, 82, 0.39], ['gate', 'ok', 82, 88, 0.02], ['post', 'ok', 88, 100, 0]];
    const gantt = steps.map(([n, s, a, b, c]) => `<div class="mono">${n}</div><div class="gt" data-s="${s}">${a != null ? `<i style="left:${a}%;width:${Math.max(b - a, 1)}%"></i>` : '<span class="muted tiny">not reached</span>'}</div><div>${st(s, '')}</div><div class="mono num muted">${c != null ? usd(c) : ''}</div>`).join('');
    return `<div class="page">
      ${UI.head(r.workflow, { crumbs: `<a href="#/runs">Run history</a> ${icon('chev')} ${r.id}`, sub: r.repo ? `${r.repo}#${r.number}` : r.trigger,
        actions: `${st(r.status, null, true)}<button class="btn" id="retry">${icon('undo')}Retry${fail ? ' from test' : ''}</button><button class="btn ghost">${icon('download')}Record</button>` })}
      ${fail ? `<div class="callout danger" style="margin-bottom:14px"><b>Failed at step test.</b> <span class="mono">${esc(r.error)}</span></div>` : ''}
      <div class="grid g-side">
        <div class="stack">
          <div class="panel"><div class="panel-head"><h3>Timeline</h3><span class="muted small">${Math.round((new Date(r.finished) - new Date(r.started)) / 60000)}m total</span></div><div class="panel-body tight"><div class="gantt">${gantt}</div></div></div>
          <div class="panel"><div class="panel-head"><h3>Step <span class="mono">${fail ? 'test' : 'review'}</span> · inputs &amp; outputs</h3><div class="tools"><div class="seg"><button class="on">outputs</button><button>inputs</button><button>log</button><button>diff</button></div></div></div>
            <div class="panel-body">${UI.yaml(fail ? `exit_code: 1\nstdout: |\n  --- FAIL: TestCheckout (0.31s)\n      checkout_test.go:142: total = 10.49, want 10.50\n  --- FAIL: TestRefund (0.02s)\n      refund_test.go:88: got 1999, want 2000\n  FAIL acme/web/internal/billing 0.512s\nduration_ms: 26114  # scrubbed: 0 secrets` : `verdict: request_changes\ncomments: 3\nsummary: "Two N+1 queries in the orders handler; missing index on (tenant_id, created_at)."\ntokens: 30110\ncost_usd: 0.39`, { err: fail ? [3, 5] : [] })}</div></div>
        </div>
        <div class="stack">
          <div class="panel"><div class="panel-head"><h3>Run</h3></div><div class="panel-body"><dl class="kv">
            <dt>id</dt><dd class="mono">${r.id}</dd><dt>trigger</dt><dd class="mono">${r.trigger}</dd><dt>started</dt><dd class="mono">${new Date(r.started).toLocaleString()}</dd>
            <dt>cost</dt><dd class="mono">${usd(r.cost)} ${r.tokens ? '· ' + (r.tokens / 1000).toFixed(1) + 'k tok' : ''}</dd><dt>signature</dt><dd>${st('ok', 'HMAC verified')}</dd><dt>retry of</dt><dd class="mono muted">—</dd>
          </dl></div></div>
          <div class="panel"><div class="panel-head"><h3>Audit for this run</h3><a class="btn sm ghost" style="margin-left:auto" href="#/logs/audit">All ${icon('chev')}</a></div><div class="list">${D.audit.slice(4, 8).map((a) => `<div class="item small"><span class="mono tiny muted" style="width:44px">${ago(a.ts)}</span><span class="mono" style="color:var(--run)">${a.event}</span><span class="muted ellipsis grow">${esc(a.detail)}</span></div>`).join('')}</div></div>
        </div>
      </div></div>`;
  });
  UI.afterRender.push((main) => UI.bind(main, '#retry', 'click', () => UI.modal(`<div class="modal-head"><div><div class="caps">retry-from-step</div><h2>Retry from <span class="mono" style="font-style:normal;font-size:22px">test</span></h2></div><button class="icon-btn" data-close>${icon('x')}</button></div>
    <div class="modal-body stack"><p class="dim" style="margin:0">Re-runs the pinned trigger. Steps before <b>test</b> are replayed from their recorded outputs; nothing they did is repeated.</p>
    <div class="field"><label>Resume from</label><select class="select"><option>test (the failed step)</option><option>fix</option><option>the beginning</option></select></div>
    <label class="row small"><button class="switch" onclick="this.classList.toggle('on')"></button>Force replay of steps that already succeeded</label></div>
    <div class="modal-foot"><button class="btn ghost" data-close>Cancel</button><button class="btn primary" data-close onclick="UI.toast('Retry queued','r-7f40 · retry_of r-7ef0 · from test','run')">${icon('play')}Retry</button></div>`)));

  /* ---------- needs you ---------- */
  UI.route('attention', () => `<div class="page">
    ${UI.head('Needs you', { sub: `${D.attention.length} items`, actions: `<div class="seg"><button class="on">all</button><button>approvals</button><button>failures</button><button>waiting</button></div>` })}
    <div class="grid g-side">
      <div class="panel attn">${UI.attentionList()}</div>
      <div class="stack">
        <div class="callout ember"><b>Approvals are yours alone.</b> Each one is a WebAuthn assertion over that proposal's digest, made on this device with user verification. No agent can reach this page, and no verb, chat text or peer message approves anything.</div>
        <div class="panel"><div class="panel-head"><h3>Notify me</h3></div><div class="panel-body stack tight small">
          <label class="row"><button class="switch on" onclick="this.classList.toggle('on')"></button>Browser notifications for new approvals</label>
          <label class="row"><button class="switch on" onclick="this.classList.toggle('on')"></button>Push approvals (browser 2FA) need me</label>
          <label class="row"><button class="switch" onclick="this.classList.toggle('on')"></button>Every failed run</label>
          <div class="muted tiny">Off-box alerts stay with your <span class="mono">conductor.*</span> lifecycle triggers (Slack, PagerDuty…).</div></div></div>
      </div></div></div>`, { after: (p, main) => UI.bindApprove(main) });
})();
