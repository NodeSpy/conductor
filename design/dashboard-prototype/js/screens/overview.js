/* Home (the Overview), from Daylight's approved mock (design/brand/v2 mock.js overview()) less its greeting and summary
 * sentence: they said nothing the figures and the list below don't, so the page opens on At a glance and Needs you (the mark, a plain title, why, the machine detail last, one
 * verb); beside it Running now, the last 12 hours (a figure, hour bars, the failures in words) and For your
 * information. Status marks and words come from the one vocabulary (UI.statusMark / UI.mark). */
(function () {
  const { icon, esc } = UI;
  const plural = (n, one, many) => `${n} ${n === 1 ? one : many || one + 's'}`;

  /* ---------- needs you: the mock's list (mark · title, why, detail · age · one verb) ---------- */
  const VERB = { approval: 'Review', failed: 'Open', needs_input: 'Answer', browser: 'Open' };
  // everything here waits on a person, so it all takes the needs-you mark; only a failure has its own
  const MARK = { approval: 'ember', failed: 'fail', needs_input: 'ember', browser: 'ember' };
  // the first item's action is the view's one primary button; the rest are secondary
  const needItem = (a, i = 1) => { const verb = a.verb || VERB[a.kind] || 'Open', s = MARK[a.kind] || a.s;
    return `<li class="dl-need" data-s="${s}"><a class="dl-need-a" href="${a.href}"><span class="dl-ng">${UI.mark(s, { lg: true, label: true })}</span>
      <span class="dl-nt"><b>${esc(a.plain || a.title)}</b>${a.why ? `<span class="dl-why">${esc(a.why)}</span>` : ''}<span class="dl-tech mono">${esc(a.plain ? a.title : a.sub)}</span></span>
      <span class="dl-age">${UI.ageText(a.age)}</span><span class="dl-na"><span class="btn sm ${i === 0 ? 'primary' : 'secondary'}">${verb}</span></span></a></li>`; };
  UI.needItem = needItem;
  UI.attentionList = (limit) => {
    const items = D.attention.filter((a) => UI.scopeKeeps(UI.botOfHref(a.href)));
    const note = UI.scopeNote(D.attention.length - items.length);
    if (!items.length) return note + UI.empty({ title: 'Nothing needs you', body: 'When a bot asks for approval or a run fails, it shows up here.', small: true });
    // a long list folds: the first few, then "N more need you" (Needs you itself pages by 20)
    const cap = limit || 20, more = items.length - cap;
    return note + `<ul class="dl-list dl-needs">${items.slice(0, cap).map((a, i) => needItem(a, i)).join('')}</ul>${more > 0 ? `<p class="dl-foot-note dl-more"><a class="dl-link" href="#/attention${limit ? '' : '?all=1'}">${more} more need${more === 1 ? 's' : ''} you →</a></p>` : ''}`;
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
  const hourBars = (w = 300, h = 96) => { const n = 12, per = Array.from({ length: n }, () => [0, 0]);
    window12().runs.forEach((r) => { const ago = (Date.now() - new Date(r.started)) / 60000, i = Math.min(n - 1, Math.max(0, n - 1 - Math.floor(ago / 60))); if (FAILED(r)) per[i][1]++; else per[i][0]++; });
    const mx = Math.max(4, ...per.map(([a, b]) => a + b)), bw = w / n - 6;
    const bars = per.map(([ok, f], i) => { const x = i * (w / n) + 3, ho = (ok / mx) * (h - 22), hf = (f / mx) * (h - 22);
      return `<rect x="${x}" y="${h - 18 - ho}" width="${bw}" height="${Math.max(ho - (hf ? 2 : 0), 0)}" rx="2" class="hb-ok"/>${f ? `<rect x="${x}" y="${h - 18 - ho - hf}" width="${bw}" height="${hf - 1}" rx="2" class="hb-fail"/>` : ''}`; }).join('');
    const fails = per.reduce((a, [, f]) => a + f, 0);
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
      <ul class="dl-list">${top.map((r) => `<li><a class="dl-row-a" href="#/runs/${r.id}"><span class="dl-ng">${UI.mark('run', { lg: true, label: 'Running' })}</span><span class="dl-nt"><b>${esc(UI.runTitle(r))}</b><span class="dl-why">${esc(r.workflow.replace(/^bot:/, ''))} · ${r.repo ? `${r.repo}${r.number ? '#' + r.number : ''}` : 'its own work'}</span></span><span class="dl-age">${UI.durText(Date.now() - new Date(r.started))}</span></a></li>`).join('')}</ul>
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
      <ul class="dl-list">${left.map((x, i) => `<li class="dl-gs-step" data-gs="${x.id}"><div class="dl-nt"><b>${x.title}</b><span class="dl-why">${x.text}</span></div><a class="btn sm ${i === 0 ? 'primary' : 'secondary'}" href="${x.href}">${x.go}</a></li>`).join('')}</ul>
      ${done.length ? `<p class="dl-gs-done">${UI.mark('ok')}Done: ${done.map((x) => x.title).join(' · ')}</p>` : ''}</section>`; }
  UI.act('gs-hide', () => { D.prefs.gsHidden = true; S.save(); UI.rerender(); UI.toast('Get started hidden', 'Bring it back from the button at the top of the Overview.', 'ok'); });
  UI.act('gs-show', () => { D.prefs.gsHidden = false; S.save(); UI.rerender(); });
  UI.act('sys-open', (el) => { D.prefs.sysOpen = el.open; S.save(); });

  /* At a glance: four quiet figures at the top (the bot page's stat tiles, design/brand/v2 botBefore) */
  function glance() {
    const running = D.runs.filter((r) => r.status === 'run'), w = window12(), pending = D.proposals.filter((p) => p.status === 'pending' && UI.live(p)).length;
    const tile = (href, l, v, d) => `<a class="dl-tile" href="${href}"><span class="dl-lbl">${l}</span><b>${v}</b><span class="d">${d}</span></a>`;
    return `<section class="dl-glance" aria-label="At a glance">
      ${tile('#/activity?status=run', 'Running', running.length, running.length ? `longest ${UI.durText(Date.now() - Math.min(...running.map((r) => new Date(r.started))))}` : 'nothing right now')}
      ${tile('#/activity?since=720', 'Runs, last 12 h', w.total, UI.runParts(w.runs) || 'none')}
      ${tile('#/attention', 'Approvals', pending, pending ? 'waiting for you' : 'none waiting')}
      ${tile('#/bots', 'Bots', D.bots.length, `${D.bots.filter((m) => m.home === 'live').length} working · ${plural(D.cases.filter((c) => c.status === 'open').length, 'open case')}`)}
    </section>`;
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
