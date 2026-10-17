/* Overview, in Daylight: a greeting and one sentence that answers "does anything need me, is anything broken, is
 * everything else fine?"; then what needs you (plain title, why it matters, machine detail last, one verb); what
 * changed since you last looked (Broadsheet's digest); a calm at-a-glance card; the runs of the last hours as
 * activity lanes (Score's pattern, shared with Activity); and, one step down, system health, the feed and the
 * workflows (or model usage). Getting started stays until dismissed. */
(function () {
  const { icon, esc } = UI;
  let SPAN = 720; // minutes shown on the lanes (12h by default; the seg changes it)
  const SPANS = { '1h': 60, '12h': 720, '24h': 1440, '7d': 10080 };
  const spanKey = () => Object.keys(SPANS).find((k) => SPANS[k] === SPAN);

  /* ---------- needs you: the list pattern ---------- */
  const VERB = { approval: 'Review', failed: 'Open', needs_input: 'Answer', browser: 'Open' };
  const WORD = { ember: 'Needs you', fail: 'Failed', wait: 'Waiting on you', run: 'Running', idle: 'Parked' };
  UI.attentionList = (limit) => {
    const f = UI.attnFilter || 'all';
    const byKind = D.attention.filter((a) => f === 'all' || (f === 'approvals' ? a.kind === 'approval' : f === 'failures' ? a.kind === 'failed' : !['approval', 'failed'].includes(a.kind)));
    const items = byKind.filter((a) => UI.scopeKeeps(UI.botOfHref(a.href))).slice(0, limit || 99);
    const note = UI.scopeNote(byKind.length - byKind.filter((a) => UI.scopeKeeps(UI.botOfHref(a.href))).length);
    if (!items.length) return note + UI.empty({ title: `Nothing ${f === 'all' ? '' : 'here '}needs you`, body: 'When a bot asks for approval or a run fails, it shows up here.', small: true });
    return note + `<div class="list needs">${items.map((a) => { const verb = a.verb || VERB[a.kind] || 'Open';
      return `<a class="item click att" href="${a.href}"${UI.rowState(a.s)}>
        ${UI.statusMark(a.s, `<span class="sr-only">${WORD[a.s] || a.s}</span>`, { cls: 'att-mark' })}
        <div class="grow att-text"><div class="item-t">${esc(a.plain || a.title)}</div>${a.why ? `<div class="item-why">${esc(a.why)}</div>` : ''}<div class="item-id">${esc(a.plain ? `${a.title} · ${a.sub}` : a.sub)}</div></div>
        <span class="att-age">${UI.ageText(a.age)}</span>
        <span class="btn sm${a.kind === 'approval' ? ' primary' : ''}">${a.kind === 'approval' ? icon('passkey') : ''}${verb}</span>
      </a>`; }).join('')}</div>`;
  };

  UI.bindApprove = (root) => UI.bind(root, '[data-approve]', 'click', (e, el) => {
    e.preventDefault(); e.stopPropagation();
    const p = D.proposals.find((x) => x.id === el.dataset.approve && x.bot === el.dataset.bot) || D.proposals.find((x) => x.status === 'pending' && x.bot === el.dataset.bot);
    if (p) UI.go(`#/bots/${p.bot}/proposals/${p.id}`);
  });

  UI.act('score-span', (b) => { SPAN = SPANS[b.dataset.v]; UI.rerender(); });
  UI.act('pause', () => { D.daemon.paused = !D.daemon.paused; D.audit.unshift({ ts: new Date().toISOString(), event: D.daemon.paused ? 'daemon_pause' : 'daemon_resume', who: 'operator:ops', detail: 'dispatch ' + (D.daemon.paused ? 'paused' : 'resumed') + ' from the dashboard', run: '' }); S.save(); UI.refreshChrome(); UI.rerender(); UI.toast(D.daemon.paused ? 'Dispatch paused' : 'Dispatch resumed', D.daemon.paused ? 'Runs in flight continue; new events queue until you resume.' : 'Queued events are dispatching now.', D.daemon.paused ? 'wait' : 'ok'); });

  /* ---------- activity lanes (from Score): one named lane per workflow, time left to right ----------
   * Every run is a mark whose shape gives its state: an ink dot passed, a red cross failed, a blue arc running,
   * an ember diamond waiting on you, bars parked. Lanes are labelled, not coloured; the accent is only "now". */
  const MARK = {
    ok: '<circle cx="8" cy="8" r="4.5" fill="currentColor"/>',
    fail: '<path d="M4 4l8 8M12 4l-8 8" stroke="currentColor" stroke-width="2.2" stroke-linecap="round"/>',
    run: '<path d="M13.5 8A5.5 5.5 0 1 1 8 2.5" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round"/>',
    wait: '<path d="M8 2.2l5.8 5.8L8 13.8 2.2 8z" fill="currentColor"/>',
    idle: '<path d="M6 3.5v9M10 3.5v9" stroke="currentColor" stroke-width="2.2" stroke-linecap="round"/>',
  };
  const MW = { ok: 'passed', fail: 'failed', run: 'running', wait: 'waiting on you', idle: 'parked' };
  UI.laneMark = (s) => `<svg viewBox="0 0 16 16" aria-hidden="true">${MARK[s] || MARK.ok}</svg>`;
  const laneKeeps = (l) => { const f = UI.folderScope(); if (!f) return true; if (D.bots.some((b) => b.name === l.lane)) return UI.scopeKeeps(l.lane); const w = D.workflows.find((x) => x.name === l.lane); const p = w ? UI.folderOfWf(w, false) : ''; return p === f || p.startsWith(f + '/'); };
  let ALL_LANES = false; const TOP = 6;
  UI.act('score-all', () => { ALL_LANES = !ALL_LANES; UI.rerender(); });
  const spanWords = (mins) => (mins >= 1440 ? `${Math.round(mins / 1440)} d ago` : mins >= 60 ? `${Math.round(mins / 60)} h ago` : `${mins} min ago`);
  /** The lanes. o: { span, all, top, legend } → { html, kept } */
  UI.lanes = (o = {}) => {
    const span = o.span || 720, top = o.top || TOP;
    const kept = D.score.filter(laneKeeps).map((l) => Object.assign({ n: l.marks.filter(([a]) => a <= span).length }, l)).sort((a, b) => b.n - a.n);
    const shown = o.all ? kept : kept.slice(0, top);
    const ticks = [4, 3, 2, 1].map((k) => `<span style="left:${((4 - k) / 4) * 100}%">${spanWords((span / 4) * k)}</span>`).join('') + '<span class="now" style="left:100%">now</span>';
    const rows = shown.map((l) => `<div class="lane"><div class="lane-name">${esc(l.lane)}</div><div class="lane-track">${l.marks.filter(([a]) => a <= span || span >= 720).map(([a, b, s, id]) => {
      const left = Math.max(0, Math.min(100, ((span - Math.min(a, span)) / span) * 100));
      const run = id && D.runs.find((r) => r.id === id);
      const tip = `<b>${esc(l.lane)}</b> · ${MW[s] || s} · ${spanWords(a)}${run && run.repo ? `<br>${run.repo}#${run.number}` : ''}${id ? `<br><span class='mono'>${id}</span>` : ''}${run && UI.usageOn() ? ' · ' + UI.provLabel(UI.runProv(run)) + ' ' + (UI.isApi() ? UI.usd(run.cost) : UI.usageSource() ? UI.fmtTok(UI.tokOf(run)) + ' tok' : UI.runTurns(run) + ' turns') : ''}`;
      // only marks with a recorded run open one; older marks are history without a record here
      return id ? `<a class="mark" data-m="${s}" style="left:${left}%" href="#/${s === 'run' ? 'live' : 'runs'}/${id}" data-tip="${tip}" aria-label="${esc(l.lane)}: ${MW[s]} run ${id}">${UI.laneMark(s)}</a>` : `<span class="mark" data-m="${s}" style="left:${left}%" data-tip="${tip}" role="img" aria-label="${esc(l.lane)}: ${MW[s]}">${UI.laneMark(s)}</span>`;
    }).join('')}</div></div>`).join('');
    const legend = `<div class="lane-legend">${['ok', 'fail', 'run', 'wait', 'idle'].map((s) => `<span data-m="${s}">${UI.laneMark(s)}${{ ok: 'Passed', fail: 'Failed', run: 'Running', wait: 'Waiting on you', idle: 'Parked' }[s]}</span>`).join('')}</div>`;
    return { kept, legend, html: `<div class="lanes">${rows || UI.empty({ title: 'No runs here', body: 'Nothing in this folder ran in that time.', small: true })}${rows ? `<div class="lane"><div></div><div class="lane-axis">${ticks}</div></div><div class="nowline" aria-hidden="true"></div>` : ''}</div>` };
  };
  function score() {
    const L = UI.lanes({ span: SPAN, all: ALL_LANES });
    return `<div class="panel score">
      <div class="panel-head"><h3>Runs, last ${spanKey()}</h3><span class="muted small hide-phone">one row per workflow or bot, busiest first</span>
        <div class="tools">${UI.folderChip()}${UI.seg(Object.keys(SPANS).map((k) => [k, k]), spanKey(), 'score-span')}</div></div>
      <div class="panel-body">${L.legend}${L.html}</div>
      <div class="panel-foot">${L.kept.length > TOP ? `<button class="link" data-act="score-all">${ALL_LANES ? 'Show the busiest ' + TOP : `Show all ${L.kept.length}`}</button>` : ''}<span style="margin-left:auto" class="muted">${SPAN > 720 ? 'Older runs aren’t in the prototype’s data.' : 'Select a mark to open the run.'}</span></div>
    </div>`;
  }

  /* ---------- numbers the summary, the digest and the tiles share ---------- */
  const window12 = () => { const marks = D.score.flatMap((l) => l.marks.map((m) => ({ lane: l.lane, a: m[0], b: m[1], s: m[2], id: m[3] }))).filter((m) => m.a <= 720);
    return { marks, total: marks.length, ok: marks.filter((m) => m.s === 'ok').length, fail: marks.filter((m) => m.s === 'fail').length, parked: marks.filter((m) => m.s === 'idle').length, waiting: marks.filter((m) => m.s === 'wait').length }; };
  const plural = (n, one, many) => `${n} ${n === 1 ? one : many || one + 's'}`;

  /* ---------- since you last looked (from Broadsheet) ---------- */
  // when you last looked: this browser only (a display state, never config). Leaving the overview moves it on.
  const LOOK_KEY = 'cd-last-looked';
  const lastLooked = () => { try { const v = +localStorage.getItem(LOOK_KEY); if (v) return v; } catch (e) {} return Date.now() - 120 * 60000; };
  const ageMin = (a) => { const m = String(a).match(/^(\d+)\s*([smhd])$/); return m ? +m[1] * { s: 1 / 60, m: 1, h: 60, d: 1440 }[m[2]] : 9999; };
  function digest() {
    const since = lastLooked(), mins = Math.max(1, Math.round((Date.now() - since) / 60000));
    const w = window12(), newer = w.marks.filter((m) => Math.min(m.a, 720) <= mins || m.b <= mins);
    const passed = newer.filter((m) => m.s === 'ok' && m.b <= mins), prs = passed.filter((m) => m.lane === 'pr-review').length;
    const fails = newer.filter((m) => m.s === 'fail' && m.b <= mins);
    const appr = D.attention.filter((a) => a.kind === 'approval' && ageMin(a.age) <= mins), asks = D.attention.filter((a) => a.kind !== 'approval' && a.kind !== 'failed' && ageMin(a.age) <= mins);
    const row = (n, text, s, href) => `<a class="dg-row" href="${href}"${s ? ` data-s="${s}"` : ''}><span class="dg-n">${n}</span><span class="dg-t">${text}</span>${s ? UI.statusMark(s, `<span class="sr-only">${WORD[s]}</span>`) : '<span></span>'}</a>`;
    const runOf = (m) => D.runs.find((r) => r.id === m.id);
    const rows = [
      passed.length ? row(passed.length, `${passed.length === 1 ? 'run' : 'runs'} passed${prs ? `, including ${plural(prs, 'pull-request review')}` : ''}`, '', '#/runs') : '',
      ...fails.map((m) => { const r = runOf(m); return row(1, `new failure: ${esc(m.lane)}${r && r.repo ? ` on ${r.repo}${r.number ? '#' + r.number : ''}` : ''}`, 'fail', m.id ? '#/runs/' + m.id : '#/runs'); }),
      ...appr.map((a) => row(1, `new approval: ${esc((a.plain || a.title).replace(/^./, (c) => c.toLowerCase()))}`, 'ember', a.href)),
      ...asks.map((a) => row(1, `waiting on you: ${esc((a.plain || a.title).replace(/^./, (c) => c.toLowerCase()))}`, 'wait', a.href)),
      D.daemon.update.available ? row(1, `update ready: ${D.daemon.update.available}`, '', '#/updates') : '',
    ].filter(Boolean);
    const when = mins < 60 ? `${mins} min ago` : `${Math.round(mins / 60)} h ago`;
    return `<div class="panel digest-card"><div class="panel-head"><h2>Since you last looked</h2><span class="muted small">${when}</span></div>
      <div class="panel-body">${rows.length > (D.daemon.update.available ? 1 : 0) ? `<div class="dg">${rows.join('')}</div>` : `<p class="dim" style="margin:0">Nothing new since you looked ${when}. ${plural(w.ok, 'run')} of ${w.total} in the last 12 hours went fine.</p>${rows.length ? `<div class="dg">${rows.join('')}</div>` : ''}`}</div></div>`;
  }

  /* ---------- at a glance: four quiet figures, one step down ---------- */
  function glance() {
    const running = D.runs.filter((r) => r.status === 'run'), w = window12(), pending = D.proposals.filter((p) => p.status === 'pending' && UI.live(p)).length;
    return `<div class="panel glance"><div class="panel-head"><h3>At a glance</h3></div><div class="stats">
      <a class="stat" href="#/live"><span class="k">${UI.statusMark('run', 'Running')}</span><span class="v">${running.length}</span><span class="d">${running.slice(0, 3).map((r) => esc(r.workflow)).join(' · ')}${running.length > 3 ? ` + ${running.length - 3} more` : ''}</span></a>
      <a class="stat" href="#/runs"><span class="k">Runs, last 12h</span><span class="v">${w.total}<small>${w.ok} went fine</small></span><span class="d">${plural(w.fail, 'failed', 'failed')} · ${w.parked} parked · ${w.waiting} waiting</span></a>
      ${UI.usageOn() ? UI.usageStat()
        : `<a class="stat" href="#/attention"><span class="k">Approvals</span><span class="v">${pending}<small>pending</small></span><span class="d">signed with your passkey</span></a>`}
      <a class="stat bots" href="#/bots"><span class="k">Bots</span><span class="v">${D.bots.length}<small>bots</small></span><span class="d">${D.bots.filter((m) => m.home === 'live').length} in a live session · ${D.cases.filter((c) => c.status === 'open').length} open cases</span></a>
    </div></div>`;
  }

  /* ---------- for your information: true, but not waiting on you ---------- */
  function fyi() {
    if (!(D.fyi || []).length) return '';
    return `<div class="panel"><div class="panel-head"><h2>For your information</h2>${UI.count(D.fyi.length)}</div><div class="list">${D.fyi.map((a) => `<a class="item click" href="${a.href}"><div class="grow"><div class="item-t">${esc(a.plain)}</div><div class="item-why">${esc(a.why)}</div><div class="item-id">${esc(a.title)} · ${esc(a.sub)}</div></div><span class="att-age">${UI.ageText(a.age)}</span><span class="chev">${icon('chev')}</span></a>`).join('')}</div></div>`;
  }

  function workflowsPanel() {
    return `<div class="panel"><div class="panel-head"><h3>Workflows</h3><span class="muted small">runs in the last 24 hours</span><div class="tools"><a class="btn sm ghost" href="#/workflows">All ${icon('chev')}</a></div></div>
      <div class="list">${D.workflows.filter((w) => w.runs24).sort((a, b) => b.runs24 - a.runs24).slice(0, 7).map((w) => `<a class="item click" href="#/workflows/${w.name}"><span class="grow"><b>${esc(w.name)}</b></span><span class="muted num">${plural(w.runs24, 'run')}</span>${w.ok >= 0.9 ? `<span class="num dim">${Math.round(w.ok * 100)}% fine</span>` : UI.statusMark('wait', `${Math.round(w.ok * 100)}% fine`)}</a>`).join('')}</div></div>`;
  }

  function health() {
    const d = D.daemon;
    const row = (s, k, v, href) => `<a class="item${href ? ' click' : ''}" ${href ? `href="${href}"` : ''}>${UI.statusMark(s, `<span class="sr-only">${s}</span>`)}<span class="sys-k">${k}</span><span class="muted small ellipsis grow">${v}</span></a>`;
    const bad = D.connectors.filter((c) => c.status !== 'ok').length;
    return `<details class="panel sys-card"${D.prefs.sysOpen ? ' open' : ''} data-on-toggle="sys-open">
      <summary class="panel-head"><h3>System</h3><span class="muted small">${bad ? `${plural(bad, 'connector')} need${bad > 1 ? '' : 's'} a look` : 'Everything is running'}</span><div class="tools"><span class="muted small hide-phone">checked 4 s ago</span>${icon('chev')}</div></summary>
      <div class="list">
        ${row('ok', 'conductor', `${d.version} · up ${d.uptime} · <span class="mono">pid ${d.pid} · config ${d.config_sha}</span>`, '#/logs')}
        ${row('ok', 'Sandbox', `enforcing · ${d.sandbox.identities} identities · ${d.sandbox.containers} containers · <span class="mono">${d.sandbox.backend}</span>`, '#/connectors')}
        ${row('ok', 'Agents', `paseo ${d.paseo.version} · ${d.paseo.agents} agents`, '#/connectors')}
        ${D.connectors.map((c) => row(c.status, esc(c.name), `${c.use} · ${c.detail}`, '#/connectors')).join('')}
        ${row('run', 'Update', `${d.update.available} ready · installs itself · next check within ${d.update.interval}`, '#/updates')}
      </div></details>`;
  }

  function activity() {
    return `<div class="panel">
      <div class="panel-head"><h3>Latest events</h3><div class="tools">${UI.statusMark('run', 'Live', { sm: true })}</div></div>
      <div class="list feed" id="feed" data-volatile>${D.activity.map((a) => feedItem(a.s, a.text, a.who, UI.when(a.t))).join('')}</div></div>`;
  }
  const feedItem = (s, text, who, when, fresh) => `<div class="item${fresh ? ' new-item' : ''}">${UI.statusMark(s === 'idle' ? 'idle' : s, `<span class="sr-only">${WORD[s] || s}</span>`, { sm: true })}<div class="grow small">${text}<div class="item-id">${who}</div></div><span class="att-age">${when}</span></div>`;

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
    return `<div class="panel gs"><div class="panel-head"><h2>Get started</h2><span class="muted small">${n} of ${st.length} done</span><div class="tools"><button class="btn sm ghost" data-act="gs-hide">Hide</button></div></div>
      <div class="gs-grid">${st.map((x, i) => `<a class="gs-card${x.done ? ' done' : ''}" href="${x.href}" data-gs="${x.id}"><span class="gs-n">${x.done ? UI.glyph('success') : i + 1}</span><b>${x.title}</b><span class="dim small">${x.text}</span><span class="gs-go">${x.done ? 'Done · ' : ''}${x.go} ${icon('chev')}</span></a>`).join('')}</div></div>`; }
  UI.act('gs-hide', () => { D.prefs.gsHidden = true; S.save(); UI.rerender(); UI.toast('Get started hidden', 'Bring it back from the button at the top of the Overview.', 'ok'); });
  UI.act('gs-show', () => { D.prefs.gsHidden = false; S.save(); UI.rerender(); });
  UI.act('sys-open', (el) => { D.prefs.sysOpen = el.open; S.save(); });

  const greeting = () => { const h = new Date().getHours(); return `Good ${h < 5 ? 'evening' : h < 12 ? 'morning' : h < 18 ? 'afternoon' : 'evening'}, ${esc(D.operator.name)}.`; };
  /** The overview's one sentence, bold count first when something needs you. */
  UI.overviewSummary = () => { const need = D.attention.length, w = window12(), running = D.runs.filter((r) => r.status === 'run').length;
    const rest = `${w.ok} of ${w.total} runs in the last 12 hours went fine${running ? `, and ${running} ${running === 1 ? 'is' : 'are'} running now` : ''}.`;
    return need ? `<b>${need} ${need === 1 ? 'thing needs' : 'things need'} you.</b> Everything else is running normally: ${rest}` : `<b>Nothing needs you.</b> ${rest.replace(/^./, (c) => c.toUpperCase())}`; };

  UI.route('overview', () => ({
    after(main) {
      // leaving the overview counts as having looked
      UI.onLeave(() => { if (/^#?\/?(overview)?([?].*)?$/.test(location.hash)) return; try { localStorage.setItem(LOOK_KEY, String(Date.now())); } catch (e) {} });
      const feed = UI.$('#feed'); const extra = [
        ['ok', 'r-7f2c pr-review step <b>lint</b> passed (0 findings)', 'acme/web#1291'],
        ['run', 'billing-sre took a turn in its home session (case c7)', 'turn 13'],
        ['ok', 'GitHub sweep: no missed events across 14 repos', 'adaptive 30s'],
      ]; let i = 0;
      UI.every(5200, () => { if (!feed || i >= extra.length) return; const [s, t, w] = extra[i++]; feed.insertAdjacentHTML('afterbegin', feedItem(s, t, w, 'just now', true)); });
    },
    toString() {
      return `<div class="page">
      <div class="page-head hello"><div><h1>${greeting()}</h1><p class="lede">${UI.overviewSummary()}</p>
        <p class="lede-2">conductor runs your ${UI.term('bots', 'bot')} and ${UI.term('workflows', 'workflow')} on this machine; anything risky waits for your passkey.</p></div>
        ${UI.sunrise('hide-phone hello-art')}
        <div class="actions">${D.prefs.gsHidden ? `<button class="btn ghost" data-act="gs-show">${icon('sparkle')}Get started</button>` : ''}<button class="btn" data-act="pause">${D.daemon.paused ? icon('play') + 'Resume dispatch' : icon('pause') + 'Pause dispatch'}</button><a class="btn" href="#/workflows">${icon('play')}Run a workflow</a></div></div>
      ${getStarted()}
      <div class="grid ov-top">
        <div class="panel attn">
          <div class="panel-head"><h2>Needs you</h2>${UI.count(D.attention.length, { hot: D.attention.length > 0 })}<span class="muted small">${D.attention.length} items · approvals are signed with your passkey</span><div class="tools">${UI.folderChip()}<a class="btn sm ghost" href="#/attention">See all ${icon('chev')}</a></div></div>
          ${UI.attentionList(6)}
        </div>
        <div class="stack ov-side">${digest()}${glance()}</div>
      </div>
      ${score()}
      <div class="grid g3 ov-bottom">${fyi()}${activity()}${UI.usageOn() ? UI.usagePanel() : workflowsPanel()}</div>
      <div class="ov-sys">${health()}</div>
    </div>`;
    },
  }));
})();
