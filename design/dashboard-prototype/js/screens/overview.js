/* Overview: what needs you, what is playing now (the score), health, spend. */
(function () {
  const { icon, st, tag, usd, ago, esc } = UI;
  let SPAN = 720; // minutes shown on the score (12h by default; the seg changes it)
  const SPANS = { '1h': 60, '12h': 720, '24h': 1440, '7d': 10080 };

  UI.attentionList = (limit) => {
    const f = UI.attnFilter || 'all';
    const items = D.attention.filter((a) => f === 'all' || (f === 'approvals' ? a.kind === 'approval' : f === 'failures' ? a.kind === 'failed' : !['approval', 'failed'].includes(a.kind))).slice(0, limit || 99);
    if (!items.length) return `<div class="empty"><span class="serif">Nothing ${f === 'all' ? '' : 'here '}needs you.</span>Approvals, failures and parked work land here.</div>`;
    return `<div class="list">${items.map((a) => `
      <a class="item click att" href="${a.href}">
        ${st(a.s, '', false)}
        <div class="grow"><div class="ellipsis" style="font-weight:600">${esc(a.title)}</div><div class="muted small ellipsis mono" style="font-size:10.5px">${esc(a.sub)}</div></div>
        <span class="muted mono tiny">${a.age}</span>
        ${a.kind === 'approval' ? `<span class="btn sm primary">${icon('passkey')}Review</span>` : `<span class="chev">${icon('chev')}</span>`}
      </a>`).join('')}</div>`;
  };

  UI.bindApprove = (root) => UI.bind(root, '[data-approve]', 'click', (e, el) => {
    e.preventDefault(); e.stopPropagation();
    const p = D.proposals.find((x) => x.id === el.dataset.approve && x.bot === el.dataset.bot) || D.proposals.find((x) => x.status === 'pending' && x.bot === el.dataset.bot);
    if (p) UI.go(`#/bots/${p.bot}/proposals/${p.id}`);
  });

  const ov = { cost: 'chart' };
  UI.act('score-span', (b) => { SPAN = SPANS[b.dataset.v]; UI.rerender(); });
  UI.act('cost-view', (b) => { ov.cost = b.dataset.v; UI.rerender(); });
  UI.act('pause', () => { D.daemon.paused = !D.daemon.paused; D.audit.unshift({ ts: new Date().toISOString(), event: D.daemon.paused ? 'daemon_pause' : 'daemon_resume', who: 'operator:ops', detail: 'dispatch ' + (D.daemon.paused ? 'paused' : 'resumed') + ' from the dashboard', run: '' }); S.save(); UI.rerender(); UI.toast(D.daemon.paused ? 'Dispatch paused' : 'Dispatch resumed', D.daemon.paused ? 'runs in flight continue; new events queue' : 'queued events dispatch now', D.daemon.paused ? 'wait' : 'ok'); });
  function score() {
    const ticks = [];
    const step = SPAN / 6;
    for (let k = 6; k >= 0; k--) { const mins = k * step; ticks.push(`<span style="left:${((SPAN - mins) / SPAN) * 100}%">${mins ? '−' + (mins >= 1440 ? Math.round(mins / 1440) + 'd' : mins >= 60 ? Math.round(mins / 60) + 'h' : mins + 'm') : 'now'}</span>`); }
    const lanes = D.score.map((l) => `
      <div class="lane"><div class="lane-name mono">${l.lane}</div><div class="lane-track">${l.marks.filter(([a]) => a <= SPAN || SPAN >= 720).map(([a, b, s, id]) => {
        const left = Math.max(0, ((SPAN - a) / SPAN) * 100), w = Math.max(((Math.min(a, SPAN) - b) / SPAN) * 100, 0.55);
        const run = id && D.runs.find((r) => r.id === id);
        const tip = `<b>${l.lane}</b> · ${s === 'run' ? 'running' : s}<br><span class='mono'>${id || 'run'} · ${a - b || '<1'}m${run && run.repo ? ' · ' + run.repo + '#' + run.number : ''}${run ? ' · ' + usd(run.cost) : ''}</span>`;
        // only marks with a recorded run open one; older marks are history without a record here
        return id ? `<a class="mark" data-s="${s}" style="left:${left}%;width:${w}%" href="#/${s === 'run' ? 'live' : 'runs'}/${id}" data-tip="${tip}" aria-label="${l.lane} run ${id}"></a>` : `<span class="mark" data-s="${s}" style="left:${left}%;width:${w}%" data-tip="${tip}"></span>`;
      }).join('')}</div></div>`).join('');
    return `<div class="panel score">
      <div class="panel-head"><h3>The score</h3><span class="muted small">every run in the last ${Object.keys(SPANS).find((k) => SPANS[k] === SPAN)}, one staff per workflow</span>
        <div class="tools">${UI.seg(Object.keys(SPANS).map((k) => [k, k]), Object.keys(SPANS).find((k) => SPANS[k] === SPAN), 'score-span')}</div></div>
      <div class="score-body">${lanes}<div class="ticks">${ticks.join('')}</div><div class="nowline"><span></span></div></div>
      <div class="panel-foot small muted">${['ok', 'run', 'wait', 'fail', 'idle'].map((s) => st(s, { ok: 'ok', run: 'running', wait: 'waiting on you', fail: 'failed', idle: 'parked' }[s])).join('<span style="width:14px"></span>')}<span style="margin-left:auto" class="mono tiny">${SPAN > 720 ? 'older runs aren\'t in the prototype\'s data' : 'click a mark to open the run'}</span></div>
    </div>`;
  }

  function costChart() {
    const max = Math.max(...D.costDaily.map((d) => d.usd)), top = Math.ceil(max / 4) * 4;
    const bars = D.costDaily.map((d, i) => {
      const day = new Date(Date.now() - (13 - i) * 864e5);
      const lbl = day.toLocaleDateString('en', { month: 'short', day: 'numeric' });
      return `<div class="cbar${i === 13 ? ' today' : ''}" data-tip="<b>${lbl}</b><br><span class='mono'>${usd(d.usd)} · ${Math.round(d.usd * 9)} runs</span>"><i style="height:${(d.usd / top) * 100}%"></i></div>`;
    }).join('');
    const total = D.costDaily.reduce((a, d) => a + d.usd, 0);
    return `<div class="panel">
      <div class="panel-head"><h3>Spend</h3><span class="muted small">per day, last 14 days</span><div class="tools">${UI.seg([['chart', 'Chart'], ['table', 'Table']], ov.cost, 'cost-view')}</div></div>
      <div class="panel-body">
        <div class="row" style="align-items:baseline;gap:14px;margin-bottom:10px"><span class="serif" style="font-size:34px">${usd(total)}</span><span class="muted small">14 days · today ${usd(D.costDaily[13].usd)} · budget shed 1 run</span></div>
        <div id="cost-chart" class="${ov.cost === 'chart' ? '' : 'hide'}"><div class="cchart"><div class="cgrid">${[top, top / 2, 0].map((v) => `<span><em class="mono">$${v}</em></span>`).join('')}</div><div class="cbars">${bars}</div></div>
          <div class="row between mono tiny muted" style="margin-top:6px;padding-left:34px"><span>${new Date(Date.now() - 13 * 864e5).toLocaleDateString('en', { month: 'short', day: 'numeric' })}</span><span>today</span></div></div>
        <div id="cost-table" class="${ov.cost === 'table' ? '' : 'hide'}"><table class="tbl compact"><thead><tr><th>day</th><th class="num">spend</th></tr></thead><tbody>${D.costDaily.slice().reverse().map((d, i) => `<tr><td class="mono">${new Date(Date.now() - i * 864e5).toISOString().slice(0, 10)}</td><td class="num mono">${usd(d.usd)}</td></tr>`).join('')}</tbody></table></div>
      </div></div>`;
  }

  function health() {
    const d = D.daemon;
    const row = (s, k, v, href) => `<a class="item${href ? ' click' : ''}" ${href ? `href="${href}"` : ''}>${st(s, '')}<span style="font-weight:600;min-width:84px">${k}</span><span class="muted small ellipsis grow">${v}</span></a>`;
    return `<div class="panel">
      <div class="panel-head"><h3>Health</h3><div class="tools"><span class="mono tiny muted">checked 4s ago</span></div></div>
      <div class="list">
        ${row('ok', 'daemon', `${d.version} · pid ${d.pid} · up ${d.uptime} · config ${d.config_sha}`, '#/logs')}
        ${row('ok', 'sandbox', `agents ${d.sandbox.agents} · ${d.sandbox.backend} · ${d.sandbox.identities} identities · ${d.sandbox.containers} containers`, '#/connectors')}
        ${row('ok', 'paseo', `${d.paseo.version} · ${d.paseo.agents} agents · provider entries ${d.paseo.entries}`, '#/connectors')}
        ${D.connectors.map((c) => row(c.status, c.name, `${c.use} · ${c.detail}`, '#/connectors')).join('')}
        ${row('run', 'update', `${d.update.available} available · auto-update on · next check ≤ ${d.update.interval}`, '#/updates')}
      </div></div>`;
  }

  function activity() {
    return `<div class="panel">
      <div class="panel-head"><h3>Activity</h3><div class="tools"><span class="st" data-s="run"><i>${icon('dot')}</i>live</span></div></div>
      <div class="list feed" id="feed" data-volatile>${D.activity.map((a) => `<div class="item"><span class="dot" data-s="${a.s}" style="--sc:var(--${a.s === 'ember' ? 'accent' : a.s})"></span><div class="grow small">${a.text}<div class="mono tiny muted">${a.who}</div></div><span class="mono tiny muted">${ago(a.t)}</span></div>`).join('')}</div></div>`;
  }

  UI.route('overview', () => {
    const running = D.runs.filter((r) => r.status === 'run');
    const ok24 = D.runs.filter((r) => r.status === 'ok').length, fail24 = D.runs.filter((r) => r.status === 'fail').length;
    return {
      after(main) {
        const feed = UI.$('#feed'); const extra = [
          ['ok', 'r-7f2c pr-review step <b>lint</b> ok (0 findings)', 'acme/web#1291'],
          ['run', 'billing-sre home session took a turn (c7)', 'turn 13'],
          ['ok', 'gh sweep: 0 missed events across 14 repos', 'adaptive 30s'],
        ]; let i = 0;
        UI.every(5200, () => { if (!feed || i >= extra.length) return; const [s, t, w] = extra[i++]; const el = document.createElement('div'); el.className = 'item new-item'; el.innerHTML = `<span class="dot" style="--sc:var(--${s})"></span><div class="grow small">${t}<div class="mono tiny muted">${w}</div></div><span class="mono tiny muted">now</span>`; feed.prepend(el); });
      },
      toString() {
        return `<div class="page">
        ${UI.head('Overview', { crumbs: `${D.daemon.host} · ${D.daemon.magicdns}`, actions: `<button class="btn" data-act="pause">${D.daemon.paused ? icon('play') + 'Resume dispatch' : icon('pause') + 'Pause dispatch'}</button><a class="btn" href="#/workflows">${icon('play')}Run a workflow</a>` })}
        <div class="grid ov-top">
          <div class="panel attn">
            <div class="panel-head"><span class="attn-mark">${icon('diamond')}</span><h3>Needs you</h3><span class="muted small">${D.attention.length} items · approvals are signed with your passkey</span><div class="tools"><a class="btn sm ghost" href="#/attention">All ${icon('chev')}</a></div></div>
            ${UI.attentionList(6)}
          </div>
          <div class="grid g2 stats">
            <a class="panel stat" href="#/live"><span class="k"><span class="pulse run"></span>Running</span><span class="v">${running.length}</span><span class="d">${running.map((r) => r.workflow).join(' · ')}</span></a>
            <a class="panel stat" href="#/runs"><span class="k">Runs · 12h</span><span class="v">${ok24 + fail24 + 9}<small>${Math.round(((ok24 + 9) / (ok24 + fail24 + 9)) * 100)}% ok</small></span><span class="d">${fail24} failed · 1 parked · 1 waiting</span></a>
            <a class="panel stat" href="#/runs"><span class="k">Spend · today</span><span class="v">${usd(D.costDaily[13].usd)}</span><span class="d">budget $25/day · 1 step shed</span></a>
            <a class="panel stat hot" href="#/bots"><span class="k">Bots</span><span class="v">${D.bots.length}<small>${D.bots.filter((m) => m.home === 'live').length} live</small></span><span class="d">${D.proposals.filter((p) => p.status === 'pending').length} proposals pending · ${D.cases.filter((c) => c.status === 'open').length} open cases</span></a>
          </div>
        </div>
        ${score()}
        <div class="grid g3 ov-bottom">${health()}${activity()}${costChart()}</div>
      </div>`;
      },
    };
  });
})();
