/* Overview: getting started (until dismissed), what needs you, runs in the last hours, activity, system health. */
(function () {
  const { icon, st, tag, usd, ago, esc } = UI;
  let SPAN = 720; // minutes shown on the score (12h by default; the seg changes it)
  const SPANS = { '1h': 60, '12h': 720, '24h': 1440, '7d': 10080 };

  UI.attentionList = (limit) => {
    const f = UI.attnFilter || 'all';
    const byKind = D.attention.filter((a) => f === 'all' || (f === 'approvals' ? a.kind === 'approval' : f === 'failures' ? a.kind === 'failed' : !['approval', 'failed'].includes(a.kind)));
    const items = byKind.filter((a) => UI.scopeKeeps(UI.botOfHref(a.href))).slice(0, limit || 99);
    const note = UI.scopeNote(byKind.length - byKind.filter((a) => UI.scopeKeeps(UI.botOfHref(a.href))).length);
    if (!items.length) return note + `<div class="empty"><span class="serif">Nothing ${f === 'all' ? '' : 'here '}needs you.</span>Approvals, failures and parked work land here.</div>`;
    return note + `<div class="list">${items.map((a) => `
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
  UI.act('pause', () => { D.daemon.paused = !D.daemon.paused; D.audit.unshift({ ts: new Date().toISOString(), event: D.daemon.paused ? 'daemon_pause' : 'daemon_resume', who: 'operator:ops', detail: 'dispatch ' + (D.daemon.paused ? 'paused' : 'resumed') + ' from the dashboard', run: '' }); S.save(); UI.rerender(); UI.toast(D.daemon.paused ? 'Dispatch paused' : 'Dispatch resumed', D.daemon.paused ? 'runs in flight continue; new events queue' : 'queued events dispatch now', D.daemon.paused ? 'wait' : 'ok'); });
  /* which workflows a lane stands for, for the folder filter: a bot's lane follows the bot; a workflow's, its folder */
  const laneKeeps = (l) => { const f = UI.folderScope(); if (!f) return true; if (D.bots.some((b) => b.name === l.lane)) return UI.scopeKeeps(l.lane); const w = D.workflows.find((x) => x.name === l.lane); const p = w ? UI.folderOfWf(w, false) : ''; return p === f || p.startsWith(f + '/'); };
  let ALL_LANES = false; const TOP = 6;
  UI.act('score-all', () => { ALL_LANES = !ALL_LANES; UI.rerender(); });
  function score() {
    const ticks = [];
    const step = SPAN / 6;
    for (let k = 6; k >= 0; k--) { const mins = k * step; ticks.push(`<span style="left:${((SPAN - mins) / SPAN) * 100}%">${mins ? '−' + (mins >= 1440 ? Math.round(mins / 1440) + 'd' : mins >= 60 ? Math.round(mins / 60) + 'h' : mins + 'm') : 'now'}</span>`); }
    const kept = D.score.filter(laneKeeps).map((l) => Object.assign({ n: l.marks.filter(([a]) => a <= SPAN).length }, l)).sort((a, b) => b.n - a.n), shown = ALL_LANES ? kept : kept.slice(0, TOP);
    const lanes = shown.map((l) => `
      <div class="lane"><div class="lane-name mono">${l.lane}</div><div class="lane-track">${l.marks.filter(([a]) => a <= SPAN || SPAN >= 720).map(([a, b, s, id]) => {
        const left = Math.max(0, ((SPAN - a) / SPAN) * 100), w = Math.max(((Math.min(a, SPAN) - b) / SPAN) * 100, 0.55);
        const run = id && D.runs.find((r) => r.id === id);
        const tip = `<b>${l.lane}</b> · ${s === 'run' ? 'running' : s}<br><span class='mono'>${id || 'run'} · ${a - b || '<1'}m${run && run.repo ? ' · ' + run.repo + '#' + run.number : ''}${run && UI.usageOn() ? ' · ' + UI.provLabel(UI.runProv(run)) + ' ' + (UI.isApi() ? UI.usd(run.cost) : UI.usageSource() ? UI.fmtTok(UI.tokOf(run)) + ' tok' : UI.runTurns(run) + ' turns') : ''}</span>`;
        // only marks with a recorded run open one; older marks are history without a record here
        return id ? `<a class="mark" data-s="${s}" style="left:${left}%;width:${w}%" href="#/${s === 'run' ? 'live' : 'runs'}/${id}" data-tip="${tip}" aria-label="${l.lane} run ${id}"></a>` : `<span class="mark" data-s="${s}" style="left:${left}%;width:${w}%" data-tip="${tip}"></span>`;
      }).join('')}</div></div>`).join('');
    return `<div class="panel score">
      <div class="panel-head"><h3>Runs, last ${Object.keys(SPANS).find((k) => SPANS[k] === SPAN)}</h3><span class="muted small">one row per workflow or bot, busiest first</span>
        <div class="tools">${UI.folderChip()}${UI.seg(Object.keys(SPANS).map((k) => [k, k]), Object.keys(SPANS).find((k) => SPANS[k] === SPAN), 'score-span')}</div></div>
      <div class="score-body">${lanes || '<div class="empty small">No runs in this folder in that time.</div>'}<div class="ticks">${ticks.join('')}</div><div class="nowline"><span></span></div></div>
      <div class="panel-foot small muted">${['ok', 'run', 'wait', 'fail', 'idle'].map((s) => st(s, { ok: 'ok', run: 'running', wait: 'waiting on you', fail: 'failed', idle: 'parked' }[s])).join('<span style="width:14px"></span>')}${kept.length > TOP ? `<button class="link small" style="margin-left:14px" data-act="score-all">${ALL_LANES ? 'Show the busiest ' + TOP : `Show all ${kept.length}`}</button>` : ''}<span style="margin-left:auto" class="mono tiny">${SPAN > 720 ? 'older runs aren\'t in the prototype\'s data' : 'click a mark to open the run'}</span></div>
    </div>`;
  }

  function workflowsPanel() {
    return `<div class="panel"><div class="panel-head"><h3>Workflows</h3><span class="muted small">runs in the last 24h</span><div class="tools"><a class="btn sm ghost" href="#/workflows">All ${icon('chev')}</a></div></div>
      <div class="list">${D.workflows.filter((w) => w.runs24).sort((a, b) => b.runs24 - a.runs24).slice(0, 7).map((w) => `<a class="item click" href="#/workflows/${w.name}"><span class="mono small grow">${w.name}</span><span class="mono tiny muted">${w.runs24} runs</span>${st(w.ok >= 0.9 ? 'ok' : 'wait', Math.round(w.ok * 100) + '%')}</a>`).join('')}</div></div>`;
  }
  function costChart() {
    return UI.usageOn() ? UI.usagePanel() : workflowsPanel();
  }

  function health() {
    const d = D.daemon;
    const row = (s, k, v, href) => `<a class="item${href ? ' click' : ''}" ${href ? `href="${href}"` : ''}>${st(s, '')}<span style="font-weight:600;min-width:84px">${k}</span><span class="muted small ellipsis grow">${v}</span></a>`;
    const bad = D.connectors.filter((c) => c.status !== 'ok').length;
    return `<details class="panel sys-card"${D.prefs.sysOpen ? ' open' : ''} data-on-toggle="sys-open">
      <summary class="panel-head"><h3>System</h3><span class="muted small">${bad ? `${bad} connector${bad > 1 ? 's' : ''} need${bad > 1 ? '' : 's'} a look` : 'everything is running'} · conductor, the agent sandbox, agents, connectors, updates</span><div class="tools"><span class="mono tiny muted">checked 4s ago</span>${icon('chev')}</div></summary>
      <div class="list">
        ${row('ok', 'conductor', `${d.version} · pid ${d.pid} · up ${d.uptime} · config ${d.config_sha}`, '#/logs')}
        ${row('ok', 'sandbox', `agents ${d.sandbox.agents} · ${d.sandbox.backend} · ${d.sandbox.identities} identities · ${d.sandbox.containers} containers`, '#/connectors')}
        ${row('ok', 'agents (paseo)', `${d.paseo.version} · ${d.paseo.agents} agents · provider entries ${d.paseo.entries}`, '#/connectors')}
        ${D.connectors.map((c) => row(c.status, c.name, `${c.use} · ${c.detail}`, '#/connectors')).join('')}
        ${row('run', 'update', `${d.update.available} available · auto-update on · next check ≤ ${d.update.interval}`, '#/updates')}
      </div></details>`;
  }

  function activity() {
    return `<div class="panel">
      <div class="panel-head"><h3>Activity</h3><div class="tools"><span class="st" data-s="run"><i>${icon('dot')}</i>live</span></div></div>
      <div class="list feed" id="feed" data-volatile>${D.activity.map((a) => `<div class="item"><span class="dot" data-s="${a.s}" style="--sc:var(--${a.s === 'ember' ? 'accent' : a.s})"></span><div class="grow small">${a.text}<div class="mono tiny muted">${a.who}</div></div><span class="mono tiny muted">${ago(a.t)}</span></div>`).join('')}</div></div>`;
  }

  /* Get started: four plain steps with ticks derived from the setup; dismissible (a display preference, this browser only) */
  UI.botWatches = (name) => { const b = D.bots.find((x) => x.name === name); const team = b && b.cfg && b.cfg.team; const re = new RegExp(`bot:\\s*(${name}\\b|\\{[^}]*\\b(name:\\s*${name}|team:\\s*${team || '__none__'})\\b)`);
    const trig = S.view().workflows.filter((w) => w.kind !== 'workflow' && w.yaml && re.test(w.yaml)).map((w) => w.name); return { triggers: trig, sweep: !!(b && b.cfg && b.cfg.proactive && b.cfg.proactive.every) }; };
  const gsSteps = () => [
    { id: 'connect', done: D.connectors.length > 0, title: 'Connect a service', text: 'GitHub, Slack, PagerDuty… so bots can see events and act.', href: '#/connectors/connectors', go: 'Connectors' },
    { id: 'watch', done: D.bots.some((b) => UI.botWatches(b.name).triggers.length), title: 'Make a bot that watches something', text: 'A bot is an AI teammate with memory; tell it what to watch and where to tell you.', href: '#/bots/new', go: 'New bot' },
    { id: 'safety', done: !!D.prefs.seenSafety, title: 'Check what your bots can do', text: 'In plain words: what each bot does on its own, what it asks you first, and what it never does.', href: '#/safety', go: 'Safety' },
    { id: 'approve', done: D.proposals.some((p) => p.approver || p.status === 'denied'), title: 'Try an approval', text: 'Risky actions wait for you. You approve with your passkey, on this laptop or your phone.', href: '#/attention', go: 'Needs you' },
  ];
  function getStarted() { if (D.prefs.gsHidden) return ''; const st = gsSteps(), n = st.filter((x) => x.done).length;
    return `<div class="panel gs" style="margin-bottom:14px"><div class="panel-head"><h3>Get started</h3><span class="muted small">${n} of ${st.length} done</span><div class="tools"><button class="btn sm ghost" data-act="gs-hide">Hide</button></div></div>
      <div class="gs-grid">${st.map((x, i) => `<a class="gs-card${x.done ? ' done' : ''}" href="${x.href}" data-gs="${x.id}"><span class="gs-n">${x.done ? icon('check') : i + 1}</span><b>${x.title}</b><span class="muted small">${x.text}</span><span class="gs-go small">${x.done ? 'Done · ' : ''}${x.go} ${icon('chev')}</span></a>`).join('')}</div></div>`; }
  UI.act('gs-hide', () => { D.prefs.gsHidden = true; S.save(); UI.rerender(); UI.toast('Get started hidden', 'bring it back from the Overview header', 'ok'); });
  UI.act('gs-show', () => { D.prefs.gsHidden = false; S.save(); UI.rerender(); });
  UI.act('sys-open', (el) => { D.prefs.sysOpen = el.open; S.save(); });

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
        ${UI.head('Overview', { crumbs: `${D.daemon.host} · ${D.daemon.magicdns}`, sub: 'conductor runs your bots and automations on this machine; anything risky waits for your passkey', actions: `${D.prefs.gsHidden ? `<button class="btn ghost" data-act="gs-show">${icon('sparkle')}Get started</button>` : ''}<button class="btn" data-act="pause">${D.daemon.paused ? icon('play') + 'Resume dispatch' : icon('pause') + 'Pause dispatch'}</button><a class="btn" href="#/workflows">${icon('play')}Run a workflow</a>` })}
        ${getStarted()}
        <div class="grid ov-top">
          <div class="panel attn">
            <div class="panel-head"><span class="attn-mark">${icon('diamond')}</span><h3>Needs you</h3><span class="muted small">${D.attention.length} items · approvals are signed with your passkey</span><div class="tools">${UI.folderChip()}<a class="btn sm ghost" href="#/attention">All ${icon('chev')}</a></div></div>
            ${UI.attentionList(6)}
          </div>
          <div class="grid g2 stats">
            <a class="panel stat" href="#/live"><span class="k"><span class="pulse run"></span>Running</span><span class="v">${running.length}</span><span class="d">${running.slice(0, 3).map((r) => esc(r.workflow)).join(' · ')}${running.length > 3 ? ` + ${running.length - 3} more` : ''}</span></a>
            <a class="panel stat" href="#/runs"><span class="k">Runs, last 12h</span><span class="v">${ok24 + fail24 + 9}<small>${Math.round(((ok24 + 9) / (ok24 + fail24 + 9)) * 100)}% ok</small></span><span class="d">${fail24} failed · 1 parked · 1 waiting</span></a>
            ${UI.usageOn() ? UI.usageStat()
              : `<a class="panel stat" href="#/attention"><span class="k">Approvals</span><span class="v">${D.proposals.filter((p) => p.status === 'pending' && UI.live(p)).length}<small>pending</small></span><span class="d">signed with your passkey</span></a>`}
            <a class="panel stat hot" href="#/bots"><span class="k">Bots</span><span class="v">${D.bots.length}<small>bots</small></span><span class="d">${D.bots.filter((m) => m.home === 'live').length} in a live session · ${D.proposals.filter((p) => p.status === 'pending' && UI.live(p)).length} proposals pending · ${D.cases.filter((c) => c.status === 'open').length} open cases</span></a>
          </div>
        </div>
        ${score()}
        <div class="grid g3 ov-bottom">${health()}${activity()}${costChart()}</div>
      </div>`;
      },
    };
  });
})();
