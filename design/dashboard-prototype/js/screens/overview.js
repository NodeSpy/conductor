/* Home (the Overview), from Daylight's approved mock (design/brand/v2 mock.js overview()) less its greeting and summary
 * sentence: they said nothing the figures and the list below don't, so the page opens on At a glance and Needs you (the mark, a plain title, why, the machine detail last, one
 * verb); beside it Running now, the last 12 hours (a figure, hour bars, the failures in words) and For your
 * information. Status marks and words come from the one vocabulary (UI.statusMark / UI.mark). */
(function () {
  const { icon, esc } = UI;
  const plural = (n, one, many) => `${n} ${n === 1 ? one : many || one + 's'}`;

  /* ---------- needs you: the mock's list (mark · title, why, detail · age · one verb) ---------- */
  // the verb is the first thing you'll do on the page it opens, the same for every item of a kind: review an approval,
  // answer a question, approve a sign-in, see why a run failed, fix a connection, resume automation
  const VERB = { approval: 'Review', failed: 'See why', needs_input: 'Answer', browser: 'Approve', connection: 'Fix it', paused: 'Resume', update: 'Review' };
  // everything here waits on a person, so it all takes the needs-you mark; only a failure has its own
  const MARK = { approval: 'ember', failed: 'fail', needs_input: 'ember', browser: 'ember' };
  // the rows of a list are peers: every row's action looks the same, in a fixed-width column so the times and the
  // buttons line up; what's more urgent says so with its mark and its place in the list, never with a louder button
  /** An item's facts: who (the bot, the workflow), where (what the connector says it's about, a site), the environment,
      and what kind of action, from the item's own source (the approval, the run, the sign-in). */
  UI.needFacts = (a) => { const href = String(a.href || ''), bot = UI.botOfHref(href), botF = (b) => (b ? { icon: 'obj-bot', value: b, href: `#/bots/${b}`, pri: 1, label: 'Bot' } : null);
    const pm = href.match(/proposals\/([\w-]+)/), p = pm && (D.proposals || []).find((x) => x.id === pm[1] && (!bot || x.bot === bot));
    if (p) return [botF(p.bot), UI.envFact(p.env), ...UI.verbFacts(p.verb, p.options), UI.actionFact(p.verb, p.env, p.tier)];
    const rm = href.match(/#\/runs\/([\w-]+)/), r = rm && D.runs.find((x) => x.id === rm[1]);
    if (r) { const wf = r.workflow.replace(/^bot:/, ''), b = UI.botOfRun(r); return [b ? botF(b) : { icon: 'obj-workflow', value: wf, href: `#/workflows/${encodeURIComponent(r.workflow)}`, pri: 1, label: 'Workflow' }, ...UI.itemFacts(r).map((f) => ({ ...f, pri: /#/.test(f.value) ? 1 : 3 }))]; }
    const site = ((a.sub || '').match(/https?:\/\/([^/\s]+)/) || (a.plain || '').match(/on ([\w.-]+\.[a-z]{2,})/) || [])[1];
    return [botF(bot), site ? { icon: 'globe', value: site, pri: 2, label: 'Site' } : null]; };
  const needItem = (a) => { const verb = VERB[a.kind] || a.verb || 'Open', s = MARK[a.kind] || a.s;
    return `<li class="dl-need" data-s="${s}"><a class="dl-need-a" href="${a.href}"><span class="dl-ng">${UI.mark(s, { lg: true, label: true })}</span>
      <span class="dl-nt"><b>${esc(a.plain || a.title)}</b>${a.why ? `<span class="dl-why">${UI.taskWords(a.why)}</span>` : ''}${UI.factsLine(UI.needFacts(a))}</span>
      <span class="dl-age">${UI.ageText(a.age)}</span><span class="dl-na"><span class="btn sm secondary">${verb}</span></span></a></li>`; };
  UI.needItem = needItem;
  /** The kinds of thing that wait on a person, one vocabulary for Home's tile, Needs you's breakdown and its Kind menu:
      [kind, one, many, icon]. */
  UI.NEED_KINDS = [['approval', 'approval', 'approvals', 'hand'], ['failed', 'failed run', 'failed runs', 'x'], ['needs_input', 'question', 'questions', 'message'], ['browser', 'sign-in', 'sign-ins', 'login'], ['connection', 'connection', 'connections', 'obj-connector'], ['update', 'update', 'updates', 'obj-update'], ['paused', 'pause', 'pauses', 'pause']];
  /** "3 approvals · 1 failed run": each count kept with its noun (a no-break space), so a wrap never orphans a number. */
  UI.needBreakdown = (items) => UI.NEED_KINDS.map(([k, one, many]) => [items.filter((a) => a.kind === k).length, one, many]).filter(([n]) => n).map(([n, one, many]) => `${n}\u00a0${n === 1 ? one : many}`).join(' · ');
  UI.attentionList = (limit, list, note = '') => {
    const items = list || D.attention;
    if (!items.length && list && D.attention.length) return note;
    if (!items.length) return note + UI.empty({ title: 'Nothing needs you', body: 'When a bot asks for approval or a run fails, it shows up here.', action: location.hash.startsWith('#/attention') ? '' : '<a class="btn sm secondary" href="#/preferences">Choose how you’re told</a>', small: true });
    // a long list folds: the first few, then "N more need you" (Needs you itself pages by 20)
    const cap = limit || 20, more = items.length - cap;
    return note + `<ul class="dl-list dl-needs">${items.slice(0, cap).map((a) => needItem(a)).join('')}</ul>${more > 0 ? `<p class="dl-foot-note dl-more"><a class="dl-link" href="#/attention${limit ? '' : '?all=1'}">${more} more need${more === 1 ? 's' : ''} you →</a></p>` : ''}`;
  };
  UI.bindApprove = (root) => UI.bind(root, '[data-approve]', 'click', (e, el) => {
    e.preventDefault(); e.stopPropagation();
    const p = D.proposals.find((x) => x.id === el.dataset.approve && x.bot === el.dataset.bot) || D.proposals.find((x) => x.status === 'pending' && x.bot === el.dataset.bot);
    if (p) UI.go(`#/bots/${p.bot}/proposals/${p.id}`);
  });

  /* ---------- the last 12 hours ---------- */
  // one source for every count, strip and time: the run records Activity lists (D.runs), started in the last 12 hours;
  // a failure is timed when it failed (its finish), the same on Home, Activity and the run page
  const FAILED = (r) => r.status === 'fail' || r.status === 'stopped';
  UI.failedAt = (r) => UI.hm(r.finished || r.started);
  const window12 = () => { const runs = D.runs.filter((r) => (Date.now() - new Date(r.started)) / 60000 <= 720);
    return { runs, total: runs.length, ok: runs.filter((r) => r.status === 'ok').length, fail: runs.filter(FAILED).length, running: runs.filter((r) => r.status === 'run').length, parked: runs.filter((r) => r.status === 'idle').length, waiting: runs.filter((r) => r.status === 'wait').length }; };
  UI.window12 = window12;
  /** Runs per hour over the last 12 hours, failures stacked on top in the danger colour (the mock's hourBars). */
  /** One bar per bucket, stacked from the ground: passed (a softer green), waiting or parked (neutral), running (blue),
      failed (red, on top, so the rare failure pops first). per[i] = { ok, other, run, fail }. */
  const SEG = [['ok', 'hb-ok'], ['other', 'hb-other'], ['run', 'hb-run'], ['fail', 'hb-fail']];
  UI.runBucket = (r) => (r.status === 'fail' || r.status === 'stopped' ? 'fail' : r.status === 'ok' ? 'ok' : r.status === 'run' ? 'run' : 'other');
  UI.stackBars = (per, w, h) => { const n = per.length, mx = Math.max(4, ...per.map((p) => p.ok + p.other + p.run + p.fail)), bw = w / n - 6;
    return per.map((p, i) => { const x = i * (w / n) + 3; let y = h - 18; const segs = SEG.filter(([k]) => p[k]);
      return segs.map(([k, cls], j) => { const hh = (p[k] / mx) * (h - 22); y -= hh; return `<rect x="${x}" y="${y}" width="${bw}" height="${Math.max(0, hh - (j < segs.length - 1 ? 2 : 0))}" rx="2" class="${cls}"/>`; }).join(''); }).join(''); };
  /** Runs per hour over the last 12 hours (the mock's hourBars). */
  const hourBars = (w = 300, h = 96) => { const n = 12, per = Array.from({ length: n }, () => ({ ok: 0, other: 0, run: 0, fail: 0 }));
    window12().runs.forEach((r) => { const ago = (Date.now() - new Date(r.started)) / 60000, i = Math.min(n - 1, Math.max(0, n - 1 - Math.floor(ago / 60))); per[i][FAILED(r) ? 'fail' : UI.runBucket(r)]++; });
    const bars = UI.stackBars(per, w, h), fails = per.reduce((a, p) => a + p.fail, 0);
    return `<svg class="dl-hours" viewBox="0 0 ${w} ${h}" role="img" aria-label="Runs per hour, last 12 hours: ${plural(fails, 'failure')}">${bars}<line x1="0" x2="${w}" y1="${h - 17}" y2="${h - 17}" class="hb-axis"/><text x="0" y="${h - 3}" class="hb-ax">12 h ago</text><text x="${w}" y="${h - 3}" class="hb-ax" text-anchor="end">now</text></svg>`; };
  function last12() {
    const w = window12(), fails = w.runs.filter(FAILED).sort((a, b) => new Date(b.finished || b.started) - new Date(a.finished || a.started));
    const spent = D.runs.filter((r) => (Date.now() - new Date(r.started)) / 60000 <= 720).reduce((a, r) => a + (r.cost || 0), 0);
    return `<section class="dl-card"><header class="dl-ch"><h2>Last 12 hours</h2></header>
      <div class="dl-stat-line"><b>${w.ok}</b><span>of ${w.total} runs went fine</span>${UI.isApi && UI.isApi() ? `<b class="r">$${spent.toFixed(2)}</b><span>spent</span>` : ''}</div>
      ${hourBars()}
      ${fails.length ? `<p class="dl-foot-note">${UI.mark('fail')}<span>${plural(fails.length, 'failed', 'failed')}: ${fails.slice(0, 4).map((r) => `<a class="dl-link" href="#/runs/${r.id}">${esc(r.workflow.replace(/^bot:/, ''))} at ${UI.failedAt(r)}</a>`).join(', ')}${fails.length > 4 ? ` and ${fails.length - 4} more` : ''}</span></p>` : `<p class="dl-foot-note">${UI.mark('ok')}Nothing failed.</p>`}</section>`;
  }

  /* ---------- running now (no step counts: step detail lives on the run page) ---------- */
  function runningNow() {
    // at most three, the ones most worth a glance first (the longest-running); the rest is a quiet link to Activity
    const live = D.runs.filter((r) => r.status === 'run').sort((x, y) => new Date(x.started) - new Date(y.started)), top = live.slice(0, 3), more = live.length - top.length;
    if (!live.length) return '';
    return `<section class="dl-card dl-running"><header class="dl-ch"><h2>Running now</h2><span class="dl-sub">${live.length}</span></header>
      <ul class="dl-list">${top.map((r) => `<li><a class="dl-row-a" href="#/runs/${r.id}"><span class="dl-ng">${UI.mark('run', { lg: true, label: 'Running' })}</span><span class="dl-nt"><b>${esc(UI.runTitle(r))}</b><span class="dl-why">${r.workflow.startsWith('bot:') ? (UI.runTarget(r) ? esc(UI.runTarget(r)) : /spawn/.test(r.trigger) ? 'a task it started itself' : /sweep/.test(r.trigger) ? 'its routine sweep' : 'its own work') : `${esc(r.workflow)} · ${UI.runTarget(r) ? esc(UI.runTarget(r)) : esc(r.trigger)}`}</span></span><span class="dl-age">${UI.runningFor(r.started)}</span></a></li>`).join('')}</ul>
      ${more > 0 ? `<a class="dl-link dl-more" href="#/activity?status=run">+${more} more running →</a>` : ''}</section>`;
  }

  /* ---------- for your information: true, but not waiting on you ---------- */
  function fyi() {
    if (!(D.fyi || []).length) return '';
    return `<section class="dl-card"><header class="dl-ch"><h2>For your information</h2></header><ul class="dl-list">${D.fyi.map((a) => `<li><a class="dl-row-a" href="${a.href}"><span class="dl-ng dl-fyi-ic">${icon(/update/i.test(a.title + a.plain) ? 'updates' : 'shield')}</span><span class="dl-nt"><b>${esc(a.plain)}</b><span class="dl-why">${esc(a.why)}</span></span></a></li>`).join('')}</ul></section>`;
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
  // only what's left, the next step as the one primary button; what's done is a quiet ticked line
  function getStarted() { if (D.prefs.gsHidden) return ''; const st = gsSteps(), left = st.filter((x) => !x.done), done = st.filter((x) => x.done);
    if (!left.length) return '';
    return `<section class="dl-card dl-gs"><header class="dl-ch"><h2>Get started</h2><span class="dl-sub">${done.length} of ${st.length} done</span><span class="dl-sp"></span><button class="btn sm ghost" data-act="gs-hide">Hide</button></header>
      <ul class="dl-list">${left.map((x) => `<li class="dl-gs-step" data-gs="${x.id}"><div class="dl-nt"><b>${x.title}</b><span class="dl-why">${x.text}</span></div><a class="btn sm secondary" href="${x.href}">${x.go}</a></li>`).join('')}</ul>
      ${done.length ? `<p class="dl-gs-done">${UI.mark('ok')}Done: ${done.map((x) => x.title).join(' · ')}</p>` : ''}</section>`; }
  UI.act('gs-hide', () => { D.prefs.gsHidden = true; S.save(); UI.rerender(); UI.toast('Get started hidden', 'Bring it back from your menu, top right: Show Get started.', 'ok', { label: 'Undo', fn: () => { D.prefs.gsHidden = false; S.save(); UI.rerender(); } }); });
  UI.act('gs-show', () => { D.prefs.gsHidden = false; S.save(); UI.rerender(); });
  UI.act('sys-open', (el) => { D.prefs.sysOpen = el.open; S.save(); });

  /* At a glance: four quiet figures at the top (the bot page's stat tiles, design/brand/v2 botBefore) */
  // four figures that agree with each other and with the badge: Needs you is the same count as the rail's, broken down
  // by kind; every run in the last 12 hours is in one of its parts
  function glance() {
    const running = D.runs.filter((r) => r.status === 'run'), w = window12(), A = D.attention;
    const needs = UI.needBreakdown(A);
    const tile = (href, label, value, detail, hot) => UI.statTile({ href, label, value, detail, hot });
    return UI.glance([
      tile('#/attention', 'Needs you', A.length, needs || 'nothing', A.length > 0),
      tile('#/activity?status=run', 'Running', running.length, running.length ? `longest ${UI.runningFor(Math.min(...running.map((r) => new Date(r.started))))}` : 'nothing right now'),
      tile('#/activity?since=720', 'Runs, last 12 h', w.total, UI.runParts(w.runs) || 'none'),
      tile('#/bots', 'Bots', D.bots.length, `${D.bots.filter((m) => m.home === 'live').length} working now`),
    ]);
  }

  UI.route('overview', () => {
    const need = D.attention.length;
    return `<div class="page dl-page">
      <h1 class="sr-only">Home</h1>
      ${glance()}
      <div class="dl-cols"><div class="dl-col-main">
        <section class="dl-card dl-needs-card"><header class="dl-ch"><h2>Needs you</h2>${need ? UI.count(need, { hot: true }) : ''}<span class="dl-sp"></span><a class="dl-link" href="#/attention">See all</a></header>
          ${UI.attentionList(5)}</section></div>
        <div class="dl-col-side">${runningNow()}${UI.usageOn() ? UI.usagePanel() : ''}${last12()}${fyi()}</div></div>
      ${getStarted()}
    </div>`;
  });
})();
