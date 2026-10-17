/* ⌘K: one command line for everything. It indexes the live, staged-aware view (pages, bots, teams, folders,
 * workflows, runs, pending approvals, connectors, verbs, vaults, secret references, settings, config paths,
 * glossary terms) and actions that do what they say (destructive ones confirm first). Results are deduped,
 * every query that finds nothing says so and suggests what to try.
 * Keys anywhere (outside text fields): ? shortcut sheet · / focus the page's filter · g then o/l/a/r/w/b/c/s
 * to jump · j/k move through a list · Enter opens · x ticks the row's checkbox. */
(function () {
  const { icon, esc } = UI;
  const go = (h) => () => UI.go(h);
  const act = (name, el) => () => UI.acts[name] && UI.acts[name](el || document.createElement('button'));
  /** Config paths from the YAML, up to three levels: triggers[ci-fixer].policy.budget, bots.platform-sre.autonomy … */
  function configPaths() {
    const out = [], stack = []; let item = null;
    S.configText().split('\n').forEach((line, i) => {
      const m = line.match(/^(\s*)(- )?(?:name:\s*([\w:.-]+)|([\w.@/-]+):)/); if (!m) return;
      const ind = m[1].length + (m[2] ? 2 : 0);
      while (stack.length && stack[stack.length - 1].ind >= ind) stack.pop();
      if (m[3] && m[2]) { const parent = stack[stack.length - 1]; if (parent) { stack.push({ ind: ind - 1, key: `${parent.key}[${m[3]}]`, list: true }); stack[stack.length - 2] = parent; } return; }
      const key = m[4]; if (!key) return;
      const parent = stack[stack.length - 1], full = parent ? (parent.list ? `${parent.key}.${key}` : `${parent.key}.${key}`) : key;
      if (stack.length < 4) out.push([full, i + 1]);
      stack.push({ ind, key: full });
    });
    return out;
  }
  function index() {
    const v = S.view(), I = [];
    const add = (kind, label, run, o = {}) => I.push(Object.assign({ kind, label, run, key: kind + '|' + label }, o));
    // actions first: they are what people type
    add('action', 'New bot', go('#/bots/new'), { icon: 'plus', words: 'create add bot teammate watcher' });
    add('action', 'New workflow', () => { UI.go('#/workflows'); setTimeout(() => UI.acts['wf-new'] && UI.acts['wf-new'](document.createElement('button')), 60); }, { icon: 'plus', words: 'create trigger recipe' });
    add('action', 'New team', () => { UI.go('#/bots'); setTimeout(act('team-new'), 60); }, { icon: 'plus' });
    add('action', 'Add a login', go('#/browser/add'), { icon: 'passkey', words: 'credential password site browser' });
    add('action', 'Connect a service (Slack, GitHub…)', go('#/connectors/connectors'), { icon: 'plus', words: 'connector slack github pagerduty add integrate' });
    add('action', D.daemon.paused ? 'Resume dispatch' : 'Pause dispatch', () => UI.confirm({ title: D.daemon.paused ? 'Resume dispatch?' : 'Pause dispatch?', body: D.daemon.paused ? 'New runs start again.' : 'No new runs start until you resume. Running ones finish.', ok: D.daemon.paused ? 'Resume' : 'Pause', danger: !D.daemon.paused }).then((ok) => ok && act('pause')()), { icon: 'pause', words: 'stop halt' });
    add('action', 'Review and apply staged changes', go('#/config/apply'), { icon: 'check', words: 'apply diff staged', hint: D.pending.length ? `${D.pending.length} staged` : 'nothing staged' });
    add('action', 'Switch to the ' + (document.documentElement.dataset.theme === 'dark' ? 'light' : 'dark') + ' theme', () => UI.theme(document.documentElement.dataset.theme === 'dark' ? 'light' : 'dark'), { icon: 'sun', words: 'theme dark light mode' });
    add('action', 'What can my bots do? (Safety)', go('#/safety'), { icon: 'shield', words: 'safety permissions tiers sandbox' });
    add('action', 'Keyboard shortcuts', () => UI.shortcuts(), { icon: 'keyboard', words: 'keys help ?' });
    add('action', 'Update conductor', go('#/updates'), { icon: 'updates', words: 'upgrade version', hint: D.daemon.update.available || '' });
    // what needs you
    D.proposals.filter((p) => p.status === 'pending' && UI.live(p)).forEach((p) => add('approval', `Approve or deny ${p.id}: ${p.title || p.summary || p.verb}`, go(`#/bots/${p.bot}/proposals/${p.id}`), { icon: 'passkey', words: `approve deny ${p.bot} ${p.verb} ${p.tf || /^tf|terraform/.test(p.verb || '') ? 'terraform plan apply' : ''} ${JSON.stringify(p.options || {}).slice(0, 200)}` }));
    // pages
    UI.navGroups.forEach(([, items]) => items.forEach(([k, l, ic]) => add('page', l, go('#/' + k), { icon: ic })));
    [['Glossary', 'glossary', 'info'], ['Collections', 'config/collections', 'folder'], ['Raw config', 'config/raw', 'code'], ['Isolation', 'connectors/isolation', 'shield'], ['Runtimes', 'connectors/runtimes', 'server'], ['Hosts', 'connectors/hosts', 'server'], ['Audit log', 'logs/audit', 'runs'], ['Phone layouts', 'phone.html', 'phone']].forEach(([l, h, ic]) => add('page', l, () => { if (h.endsWith('.html')) location.href = h; else UI.go('#/' + h); }, { icon: ic }));
    // things
    v.bots.forEach((b) => add('bot', b.name, go('#/bots/' + b.name), { icon: 'members', hint: [b.cfg && b.cfg.team ? 'team ' + b.cfg.team : '', UI.folderOfBot ? UI.folderOfBot(b.name) : ''].filter(Boolean).join(' · '), words: b.cfg && b.cfg.description }));
    v.teams.forEach((t) => add('team', t.name, go('#/teams/' + t.name), { icon: 'members', words: t.cfg && t.cfg.description }));
    (UI.allFolders ? UI.allFolders(true) : []).forEach((f) => add('folder', f, go('#/folders/' + encodeURIComponent(f)), { icon: 'folder' }));
    v.workflows.forEach((w) => add('workflow', w.name, go('#/workflows/' + w.name), { icon: 'workflows', hint: w.source === 'config' ? 'your config' : w.source, words: (w.yaml || '').slice(0, 200) }));
    D.runs.forEach((r) => add('run', `${r.id} · ${r.workflow}${r.repo ? ' · ' + UI.runTarget(r) : ''}`, go(`#/${r.status === 'run' ? 'live' : 'runs'}/${r.id}`), { icon: 'runs', hint: r.status, words: `${r.error || ''} ${r.trigger}` }));
    v.connectors.forEach((c) => { add('connector', `${c.name} (${c.use})`, go('#/connectors/connectors'), { icon: 'link' }); const d = SCHEMA.connectors[c.use]; if (d) Object.entries(d.verbs || {}).forEach(([vn, vd]) => add('verb', `${c.name}.${vn}`, go('#/safety'), { icon: 'zap', hint: vd.label, words: vd.label })); });
    (v.vaults || []).forEach((x) => add('vault', x.name, go('#/secrets/vaults'), { icon: 'lock', hint: x.kind || x.type || '' }));
    (v.secrets || []).forEach((x) => add('secret', `${x.vault}/${x.key}`, go('#/secrets'), { icon: 'lock', hint: x.alias ? 'alias ' + x.alias : '', words: x.alias }));
    Object.keys(v.settings || {}).forEach((k) => add('setting', '${settings.' + k + '}', go('#/secrets/settings'), { icon: 'code' }));
    configPaths().forEach(([p, line]) => add('config', p, go(`#/config/raw?line=${line}`), { icon: 'code', hint: `config.yaml line ${line}` }));
    Object.entries(UI.GLOSSARY || {}).forEach(([k, [t, d]]) => add('term', `${t}: what it means`, go('#/glossary#g-' + k), { icon: 'info', words: d }));
    const seen = new Set(); return I.filter((x) => (seen.has(x.key) ? false : seen.add(x.key)));
  }
  const RANK = { action: 0, approval: 1, page: 2, bot: 3, team: 3, workflow: 3, run: 4, folder: 4, connector: 5, verb: 6, vault: 6, secret: 6, setting: 6, config: 7, term: 8 };
  function search(all, q) {
    q = q.trim().toLowerCase(); if (!q) return all.filter((x) => ['action', 'approval', 'page'].includes(x.kind)).slice(0, 14);
    const toks = q.split(/\s+/);
    return all.map((x) => { const hay = `${x.label} ${x.kind} ${x.hint || ''} ${x.words || ''}`.toLowerCase(), lab = x.label.toLowerCase(); if (!toks.every((t) => hay.includes(t))) return null; let s = RANK[x.kind] * 10; if (lab.startsWith(q)) s -= 30; else if (lab.includes(q)) s -= 15; return [s, x]; })
      .filter(Boolean).sort((a, b) => a[0] - b[0]).map(([, x]) => x).slice(0, 30);
  }
  UI.palette = () => {
    if (document.querySelector('.palette')) return;
    const all = index();
    const el = document.createElement('div');
    el.innerHTML = `<div class="scrim" id="pal-scrim" style="place-items:start center;background:rgba(8,8,6,.4)"></div><div class="palette" role="dialog" aria-label="Command line"><input placeholder="Type a command, a name, a run id, a config key…" aria-label="Search or run a command" autocomplete="off" spellcheck="false"><div class="res" role="listbox"></div><div class="pal-foot muted tiny"><span><span class="kbd">↑↓</span> move</span><span><span class="kbd">↵</span> open or run</span><span><span class="kbd">esc</span> close</span><span><span class="kbd">?</span> all shortcuts</span></div></div>`;
    document.body.appendChild(el);
    const inp = el.querySelector('input'), res = el.querySelector('.res'); let sel = 0, list = search(all, '');
    const draw = () => { const q = inp.value.trim();
      res.innerHTML = list.length ? list.map((x, i) => `<a href="#" role="option" data-i="${i}" class="${i === sel ? 'on' : ''}" aria-selected="${i === sel}">${icon(x.icon || 'dot')}<span class="grow ellipsis">${esc(x.label)}</span>${x.hint ? `<span class="muted tiny ellipsis" style="max-width:40%">${esc(x.hint)}</span>` : ''}<span class="pal-kind">${x.kind}</span></a>`).join('')
        : `<div class="empty small pal-empty"><b>No results for “${esc(q)}”.</b> Try a bot or workflow name, a run id like r-7ef0, a config key like budget, or a command: <button class="link" data-q="new bot">new bot</button>, <button class="link" data-q="approve">approve</button>, <button class="link" data-q="add a login">add a login</button>.</div>`; };
    const close = () => el.remove();
    const run = (x) => { close(); x.run(); };
    inp.addEventListener('input', () => { list = search(all, inp.value); sel = 0; draw(); });
    inp.addEventListener('keydown', (e) => {
      if (e.key === 'ArrowDown') { sel = Math.min(sel + 1, list.length - 1); draw(); e.preventDefault(); }
      if (e.key === 'ArrowUp') { sel = Math.max(sel - 1, 0); draw(); e.preventDefault(); }
      if (e.key === 'Enter' && list[sel]) { e.preventDefault(); run(list[sel]); }
      if (e.key === 'Escape') { e.preventDefault(); close(); }
    });
    res.addEventListener('click', (e) => { const a = e.target.closest('a[data-i]'); if (a) { e.preventDefault(); run(list[+a.dataset.i]); return; } const b = e.target.closest('[data-q]'); if (b) { inp.value = b.dataset.q; inp.dispatchEvent(new Event('input')); inp.focus(); } });
    el.querySelector('#pal-scrim').addEventListener('click', close);
    draw(); setTimeout(() => inp.focus(), 10);
  };

  /* ---------- single-key shortcuts ---------- */
  const KEYS = [['⌘K / Ctrl K', 'command line: find anything, run actions'], ['?', 'this sheet'], ['/', 'focus the page\'s search or filter'], ['g o', 'Overview'], ['g l', 'Live'], ['g a', 'Needs you (approvals)'], ['g r', 'Run history'], ['g w', 'Workflows'], ['g b', 'Bots'], ['g c', 'Config'], ['g s', 'Secrets & vars'], ['j / k', 'next / previous row in a list'], ['Enter', 'open the selected row'], ['x', 'tick the selected row (bulk select)'], ['Esc', 'close a dialog, drawer or menu']];
  UI.shortcuts = () => UI.modal(`<div class="modal-head"><div><div class="caps">help</div><h2>Keyboard shortcuts</h2></div><button class="icon-btn" data-close aria-label="Close">${icon('x')}</button></div><div class="modal-body"><dl class="gloss keys">${KEYS.map(([k, d]) => `<div><dt><span class="kbd">${esc(k)}</span></dt><dd>${esc(d)}</dd></div>`).join('')}</dl></div>`);
  const GO = { o: 'overview', l: 'live', a: 'attention', r: 'runs', w: 'workflows', b: 'bots', c: 'config', s: 'secrets' };
  let gAt = 0, kSel = -1;
  const rows = () => [...document.querySelectorAll('#main tr[data-href], #main tr.click, #main a.item, #main .item.click, #main .ftree .frow a, #main .kind-entries .entry')].filter((r) => r.offsetParent);
  const mark = (rs, i) => { rs.forEach((r) => r.classList.remove('kbd-sel')); const r = rs[i]; if (!r) return; r.classList.add('kbd-sel'); r.scrollIntoView({ block: 'nearest' }); };
  document.addEventListener('keydown', (e) => {
    if (e.metaKey || e.ctrlKey || e.altKey) return;
    const t = e.target; if (t && (t.closest('input, textarea, select, [contenteditable="true"]') || document.querySelector('.scrim, .drawer-scrim, .palette'))) return;
    if (e.key === '?') { e.preventDefault(); UI.shortcuts(); return; }
    if (e.key === '/') { const f = document.querySelector('#main .filters input, #main input[type="search"], #main .input-icon input, #main [data-on-change="rp-q"]'); if (f) { e.preventDefault(); f.focus(); } return; }
    if (e.key === 'g') { gAt = Date.now(); return; }
    if (gAt && Date.now() - gAt < 1200 && GO[e.key]) { e.preventDefault(); gAt = 0; UI.go('#/' + GO[e.key]); kSel = -1; return; }
    gAt = 0;
    if (e.key === 'j' || e.key === 'k') { const rs = rows(); if (!rs.length) return; const cur = rs.findIndex((r) => r.classList.contains('kbd-sel')); kSel = cur < 0 ? (e.key === 'j' ? 0 : rs.length - 1) : Math.max(0, Math.min(rs.length - 1, cur + (e.key === 'j' ? 1 : -1))); mark(rs, kSel); e.preventDefault(); return; }
    if (e.key === 'Enter') { const r = document.querySelector('#main .kbd-sel'); if (r) { e.preventDefault(); const a = r.matches('a') ? r : r.querySelector('a[href]'); if (r.dataset.href) UI.go(r.dataset.href); else if (a) a.click(); else r.click(); } return; }
    if (e.key === 'x') { const r = document.querySelector('#main .kbd-sel'); const cb = r && r.querySelector('input[type="checkbox"]'); if (cb) { e.preventDefault(); cb.click(); } }
  });
  window.addEventListener('hashchange', () => { kSel = -1; });
})();
