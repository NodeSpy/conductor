/* Bots, organized in folders. A bot or team may carry `folder: "a/b"` in
 * config; slashes nest. Folders are organization only — display, filter,
 * search — with no permissions, defaults or behavior (teams own those). A bot
 * on a team shows under its team; the team sits in a folder; solo bots sit in
 * folders alongside teams.
 *
 * Moving a team or a solo bot (drag-and-drop or "Move to…") is a config edit,
 * so it stages into the pipeline. A new folder exists only in this browser
 * until something is moved into it, because config holds no empty folders. */
(function () {
  const { icon, st, tag, esc } = UI;
  const T = (n) => D.teams.find((t) => t.name === n), M = (n) => D.bots.find((b) => b.name === n);
  const clone = (o) => JSON.parse(JSON.stringify(o));
  const enc = (p) => encodeURIComponent(p);
  const parentOf = (p) => (p.includes('/') ? p.slice(0, p.lastIndexOf('/')) : '');
  const leaf = (p) => p.split('/').pop();
  const within = (path, f) => !f || path === f || path.startsWith(f + '/');

  /* ---------- where things live ---------- */
  const cfgOf = (coll, name, staged) => (staged ? S.staged(coll, name) : ((coll === 'teams' ? T(name) : M(name)) || {}).cfg) || {};
  UI.folderOfTeam = (t, staged = true) => cfgOf('teams', t, staged).folder || '';
  UI.folderOfBot = (b, staged = true) => { const c = cfgOf('bots', b, staged); return c.team ? UI.folderOfTeam(c.team, staged) : c.folder || ''; };
  UI.inFolder = (bot, f) => within(UI.folderOfBot(bot, false), f);
  /* workflows share the same folders */
  UI.wfOwner = (w) => (w.source.startsWith('pack') ? 'pack' : w.source === 'bot' ? 'bot' : 'config');
  UI.packOf = (w) => w.source.replace(/^pack /, '');
  UI.folderOfWf = (w, staged = true) => {
    const o = UI.wfOwner(w);
    if (o === 'bot') return UI.folderOfBot(w.name.slice(4), staged);
    if (o === 'pack') { const inst = UI.packOf(w), c = staged ? S.staged('packs', inst) : (D.packs.find((p) => p.name === inst) || {}).cfg; return (c && c.folders && c.folders[w.name]) || `packs/${inst}`; }
    const y = staged ? S.staged('workflows', w.name) || w.yaml : w.yaml; const m = String(y || '').match(/^folder:\s*"?([^"\n]+?)"?\s*$/m); return m ? m[1] : '';
  };
  UI.wfsIn = (f, deep = true) => S.view().workflows.filter((w) => { const x = UI.folderOfWf(w); return deep ? within(x, f) && (!f || x) : x === f; });
  const lastRun = (name) => D.runs.find((r) => r.workflow === name);
  UI.wfRoll = (ws) => { const runs = ws.reduce((a, w) => a + (w.runs24 || 0), 0), ok = runs ? ws.reduce((a, w) => a + (w.runs24 || 0) * (w.ok == null ? 1 : w.ok), 0) / runs : 1, failing = ws.filter((w) => { const r = lastRun(w.name); return r && (r.status === 'fail' || r.status === 'stopped'); }).length; return { n: ws.length, runs, ok: Math.round(ok * 100), failing }; };
  const prefixes = (p) => p.split('/').map((_, i, a) => a.slice(0, i + 1).join('/'));
  UI.allFolders = (staged = true, botsOnly = false) => {
    const set = new Set();
    D.teams.forEach((t) => { const f = UI.folderOfTeam(t.name, staged); if (f) prefixes(f).forEach((x) => set.add(x)); });
    D.bots.forEach((b) => { const c = cfgOf('bots', b.name, staged); if (!c.team && c.folder) prefixes(c.folder).forEach((x) => set.add(x)); });
    if (!botsOnly) S.view().workflows.forEach((w) => { const f = UI.folderOfWf(w, staged); if (f) prefixes(f).forEach((x) => set.add(x)); });
    (D.folderDrafts || []).forEach((f) => prefixes(f).forEach((x) => set.add(x)));
    return [...set].sort();
  };
  UI.folderCrumbs = (path, tail = '') => `<a href="#/bots">Bots</a>${path ? path.split('/').map((seg, i, a) => ` › <a href="#/folders/${enc(a.slice(0, i + 1).join('/'))}">${esc(seg)}</a>`).join('') : ''}${tail}`;

  /* ---------- the folder filter, shared by Needs you, approvals, runs, browsers ---------- */
  UI.folderScope = () => (D.prefs && D.prefs.folder) || '';
  UI.folderChip = (extra = '') => { const f = UI.folderScope(); return UI.selectChip({ icon: 'folder', on: !!f, label: 'Folder filter', wrapAttrs: extra, attrs: 'data-on-change="folder-scope"', options: `<option value="">all folders</option>${UI.allFolders(false).map((x) => `<option value="${esc(x)}"${x === f ? ' selected' : ''}>${esc(x)}</option>`).join('')}` }); };
  UI.act('folder-scope', (sel) => { D.prefs.folder = sel.value; S.save(); UI.rerender(); });
  /** The bot an attention item, a run or a browser session belongs to, if any. */
  UI.botOfHref = (href) => { const m = String(href || '').match(/#\/bots\/([^/]+)/); if (m) return m[1]; const b = String(href || '').match(/#\/browser\/([^/]+)/); if (b) { const s = D.browsers.find((x) => x.id === b[1]); return s && s.identity.startsWith('bot:') ? s.identity.slice(4) : null; } return null; };
  UI.botOfRun = (r) => (r.workflow.startsWith('bot:') ? r.workflow.slice(4) : (D.workers.find((w) => w.run === r.id) || {}).bot || null);
  UI.scopeKeeps = (bot) => { const f = UI.folderScope(); return !f || (bot && UI.inFolder(bot, f)); };
  UI.scopeNote = (hidden) => { const f = UI.folderScope(); return f ? `<div class="scope-note small">${icon('folder')} Showing <b>${esc(f)}</b>${hidden ? ` · ${hidden} item${hidden > 1 ? 's' : ''} not tied to a bot there are hidden` : ''} · <button class="link" data-act="folder-clear">show all</button></div>` : ''; };
  UI.act('folder-clear', () => { D.prefs.folder = ''; S.save(); UI.rerender(); });

  /* ---------- the tree ---------- */
  const pendingOf = (b) => D.proposals.filter((p) => p.bot === b && p.status === 'pending' && UI.live(p)).length;
  const openCases = (b) => D.cases.filter((c) => c.bot === b && c.status === 'open').length;
  function build() {
    const root = { path: '', kids: {}, teams: [], solos: [] };
    const node = (p) => { let n = root; if (!p) return n; p.split('/').forEach((seg, i, a) => { const full = a.slice(0, i + 1).join('/'); n = n.kids[seg] = n.kids[seg] || { path: full, kids: {}, teams: [], solos: [] }; }); return n; };
    UI.allFolders(true, true).forEach(node);
    D.teams.forEach((t) => node(UI.folderOfTeam(t.name)).teams.push(t.name));
    D.pending.filter((c) => c.coll === 'teams' && c.before == null && c.after).forEach((c) => { if (!T(c.key)) node(c.after.folder || '').teams.push(c.key); });
    D.bots.forEach((b) => { const c = cfgOf('bots', b.name, true); if (!c.team) node(c.folder || '').solos.push(b.name); });
    return root;
  }
  const botsUnder = (n) => { const out = []; n.teams.forEach((t) => D.bots.forEach((b) => { if (cfgOf('bots', b.name, true).team === t) out.push(b.name); })); out.push(...n.solos); Object.values(n.kids).forEach((k) => out.push(...botsUnder(k))); return out; };
  // a folder's roll-up says only what needs you there (pending approvals, failing workflows); the neutral counts are a hover away
  const roll = (n) => { const bs = botsUnder(n); const p = bs.reduce((a, b) => a + pendingOf(b), 0), c = bs.reduce((a, b) => a + openCases(b), 0), live = bs.filter((b) => (M(b) || {}).home === 'live').length; const wr = UI.wfRoll(UI.wfsIn(n.path));
    const tip = `${bs.length} bot${bs.length === 1 ? '' : 's'} · ${wr.n} workflow${wr.n === 1 ? '' : 's'} · ${c} open case${c === 1 ? '' : 's'} · ${live} working now`;
    // in words, so there is no legend to decode; what needs you first and coloured, the rest quiet
    return `<span class="roll" data-tip="${tip}">${p ? `<span class="hot">${p} pending</span>` : ''}${wr.failing ? `<span class="hot">${wr.failing} failing</span>` : ''}<span class="calm-n">${bs.length} bot${bs.length === 1 ? '' : 's'} · ${wr.n} workflow${wr.n === 1 ? '' : 's'} · ${c} open case${c === 1 ? '' : 's'}${live ? ` · ${live} working` : ''}</span></span>`; };
  const fv = { q: '' };
  const closed = () => new Set(D.prefs.closedFolders || []);
  const matchBot = (b) => { const m = M(b) || { name: b, desc: '' }; const q = fv.q.toLowerCase(); return (m.name + ' ' + (m.desc || '') + ' ' + UI.folderOfBot(b) + ' ' + (cfgOf('bots', b, true).team || '')).toLowerCase().includes(q); };
  const matchTeam = (t) => { const q = fv.q.toLowerCase(); return (t + ' ' + (cfgOf('teams', t, true).description || '') + ' ' + UI.folderOfTeam(t)).toLowerCase().includes(q) || D.bots.some((b) => cfgOf('bots', b.name, true).team === t && matchBot(b.name)); };
  const nodeMatches = (n) => !fv.q || n.path.toLowerCase().includes(fv.q.toLowerCase()) || n.teams.some(matchTeam) || n.solos.some(matchBot) || Object.values(n.kids).some(nodeMatches);
  const stagedMove = (coll, k) => { const c = S.find(coll, k); return c && c.before && c.after && (c.before.folder || '') !== (c.after.folder || ''); };
  function botRow(b, d, onTeam) {
    const m = M(b); if (!m) return '';
    const p = pendingOf(b);
    return `<div class="frow bot${stagedMove('bots', b) ? ' moved' : ''}" role="treeitem" style="--d:${d}" ${onTeam ? '' : `draggable="true" data-drag="bot:${b}"`}>
      ${onTeam ? '<span class="grip off"></span>' : `<span class="grip" data-tip="Drag onto a folder">${icon('more')}</span>`}${UI.sigil(m, 'sm')}
      <a class="fname" href="#/bots/${b}"><b>${esc(b)}</b><span class="muted small ellipsis">${onTeam ? '' : 'solo · '}${esc(m.desc || '')}</span></a>
      <span class="fstat">${p ? tag(p + ' pending', 'ember') : ''}${stagedMove('bots', b) ? ' ' + tag('move staged', 'run') : ''}${m.home === 'live' ? st('run', 'working') : ''}</span>
      <button class="icon-btn sm" data-act="f-item-menu" data-kind="bot" data-name="${b}" aria-label="Actions for ${b}">${icon('more')}</button></div>`;
  }
  function teamRow(t, d) {
    const c = cfgOf('teams', t, true), bs = D.bots.filter((b) => cfgOf('bots', b.name, true).team === t).map((b) => b.name);
    const pend = bs.reduce((a, b) => a + pendingOf(b), 0);
    return `<div class="frow team${stagedMove('teams', t) ? ' moved' : ''}" role="treeitem" style="--d:${d}" draggable="true" data-drag="team:${t}">
      <span class="grip" data-tip="Drag onto a folder">${icon('more')}</span><span class="fic team-ic">${icon('members')}</span>
      <a class="fname" href="#/teams/${t}"><b>${esc(t)}</b><span class="muted small ellipsis">team · ${bs.length} bot${bs.length === 1 ? '' : 's'}${c.lead ? ' · lead ' + esc(c.lead) : ''}</span></a>
      <span class="fstat">${stagedMove('teams', t) ? ' ' + tag('move staged', 'run') : ''}${S.find('teams', t) && S.find('teams', t).before == null ? ' ' + tag('new · staged', 'run') : ''}</span>
      <button class="icon-btn sm" data-act="f-item-menu" data-kind="team" data-name="${t}" aria-label="Actions for team ${t}">${icon('more')}</button></div>
      ${bs.filter((b) => !fv.q || matchBot(b) || matchTeam(t)).map((b) => botRow(b, d + 1, true)).join('')}`;
  }
  function folderRows(n, d) {
    const cl = closed();
    return Object.values(n.kids).sort((a, b) => a.path.localeCompare(b.path)).filter(nodeMatches).map((k) => {
      const open = fv.q ? true : !cl.has(k.path), draft = (D.folderDrafts || []).includes(k.path) && !botsUnder(k).length && !k.teams.length;
      return `<div class="frow folder" role="treeitem" aria-expanded="${open}" style="--d:${d}" data-drop="${esc(k.path)}">
        <button class="icon-btn sm ftoggle" data-act="f-toggle" data-path="${esc(k.path)}" aria-label="${open ? 'Collapse' : 'Expand'} ${esc(k.path)}">${icon(open ? 'chevd' : 'chev')}</button><span class="fic">${icon('folder')}</span>
        <a class="fname" href="#/folders/${enc(k.path)}"><b>${esc(leaf(k.path))}</b>${draft ? '<span class="muted small">empty · saved to config once something moves in</span>' : ''}</a>
        ${roll(k)}<button class="icon-btn sm" data-act="f-menu" data-path="${esc(k.path)}" aria-label="Folder actions for ${esc(k.path)}">${icon('more')}</button></div>
        ${open ? `<div role="group">${folderRows(k, d + 1)}${k.teams.filter((t) => !fv.q || matchTeam(t)).map((t) => teamRow(t, d + 1)).join('')}${k.solos.filter((b) => !fv.q || matchBot(b)).map((b) => botRow(b, d + 1, false)).join('')}</div>` : ''}`;
    }).join('');
  }
  function treeHTML(n) {
    const rows = folderRows(n, 0) + n.teams.filter((t) => !fv.q || matchTeam(t)).map((t) => teamRow(t, 0)).join('') + n.solos.filter((b) => !fv.q || matchBot(b)).map((b) => botRow(b, 0, false)).join('');
    return rows || `<div class="empty small">${fv.q ? `Nothing matches “${esc(fv.q)}”.` : 'Nothing here yet.'}</div>`;
  }
  const findNode = (root, path) => { let n = root; if (!path) return n; for (const seg of path.split('/')) { n = n.kids[seg]; if (!n) return null; } return n; };

  /* phone: one level at a time */
  function levelHTML(n) {
    if (fv.q) { // on a phone, search is a flat list across every folder
      const ts = D.teams.map((t) => t.name).filter(matchTeam), bs = D.bots.map((b) => b.name).filter(matchBot);
      return `<div class="list drill">${ts.map((t) => `<a class="item click" href="#/teams/${t}"><span class="fic team-ic">${icon('members')}</span><div class="grow"><b>${esc(t)}</b><div class="muted tiny">team · ${esc(UI.folderOfTeam(t) || 'top level')}</div></div></a>`).join('')}${bs.map((b) => `<a class="item click" href="#/bots/${b}">${UI.sigil(M(b), 'sm')}<div class="grow"><b>${esc(b)}</b><div class="muted tiny">${esc(UI.folderOfBot(b) || 'top level')}${cfgOf('bots', b, true).team ? ' · team ' + esc(cfgOf('bots', b, true).team) : ' · solo'}</div></div></a>`).join('') || `<div class="item muted small">Nothing matches “${esc(fv.q)}”.</div>`}</div>`;
    }
    const kids = Object.values(n.kids).sort((a, b) => a.path.localeCompare(b.path)).filter(nodeMatches);
    return `<div class="list drill">${kids.map((k) => `<div class="item" data-drop="${esc(k.path)}"><span class="fic">${icon('folder')}</span><a class="grow" href="#/folders/${enc(k.path)}"><b>${esc(leaf(k.path))}</b>${roll(k)}</a><button class="icon-btn" data-act="f-menu" data-path="${esc(k.path)}" aria-label="Folder actions for ${esc(k.path)}">${icon('more')}</button><a class="chev" href="#/folders/${enc(k.path)}" aria-label="Open ${esc(k.path)}">${icon('chev')}</a></div>`).join('')}
      ${n.teams.filter((t) => !fv.q || matchTeam(t)).map((t) => { const bs = D.bots.filter((b) => cfgOf('bots', b.name, true).team === t); return `<div class="item"><span class="fic team-ic">${icon('members')}</span><a class="grow" href="#/teams/${t}"><b>${esc(t)}</b><div class="muted tiny">team · ${bs.map((b) => b.name).join(', ')}</div></a><button class="icon-btn" data-act="f-item-menu" data-kind="team" data-name="${t}" aria-label="Actions for team ${t}">${icon('more')}</button></div>`; }).join('')}
      ${n.solos.filter((b) => !fv.q || matchBot(b)).map((b) => { const m = M(b); return `<div class="item">${UI.sigil(m, 'sm')}<a class="grow" href="#/bots/${b}"><b>${esc(b)}</b><div class="muted tiny">solo · ${esc(m.desc || '')}</div></a><button class="icon-btn" data-act="f-item-menu" data-kind="bot" data-name="${b}" aria-label="Actions for ${b}">${icon('more')}</button></div>`; }).join('')}
      ${!kids.length && !n.teams.length && !n.solos.length ? '<div class="item muted small">Empty folder.</div>' : ''}</div>`;
  }

  function page(path) {
    const root = build();
    let n = findNode(root, path);
    // a folder emptied by a staged move (or deleted) falls back to its nearest existing ancestor
    while (!n && path) { path = parentOf(path); n = findNode(root, path); }
    const phone = innerWidth <= 720;
    const moves = D.pending.filter((c) => (c.coll === 'bots' || c.coll === 'teams') && c.before && c.after && (c.before.folder || '') !== (c.after.folder || '')).length;
    return `<div class="page">
      ${UI.head(path ? esc(leaf(path)) : 'Bots', { crumbs: path ? UI.folderCrumbs(parentOf(path)) : '', sub: path ? `folder ${esc(path)}` : '', lede: path ? '' : (() => { const pend = D.proposals.filter((x) => x.status === 'pending' && UI.live(x)).length, live = D.bots.filter((b) => b.home === 'live').length; return `${pend ? `<b>${pend} ${pend === 1 ? 'approval waits' : 'approvals wait'} for you.</b> ` : '<b>Nothing needs you.</b> '}${D.bots.length} bots on ${D.teams.length} teams; ${live} working now.`; })(), lede2: path ? '' : `A ${UI.term('bot')} is an AI teammate with memory you can chat with; it watches what you pick and proposes actions. Bots work alone or on a ${UI.term('team')}.`, actions: `<button class="btn" data-act="f-new" data-parent="${esc(path)}">${icon('folder')}New folder</button><button class="btn" data-act="team-new">${icon('plus')}New team</button><a class="btn primary" href="#/bots/new">${icon('plus')}New bot</a>` })}
      ${moves ? `<div class="callout small staged-moves">${icon('folder')} <b>${moves} move${moves > 1 ? 's' : ''} staged.</b> Folders live in config, so moves apply through the diff.<a class="btn sm primary" href="#/config/apply">Review &amp; apply</a></div>` : ''}
      <div class="filters"><div class="input-icon">${icon('search')}<input class="input" placeholder="search bots, teams and folders" value="${esc(fv.q)}" data-on-input="f-q" aria-label="Search bots, teams and folders"></div></div>
      <div class="panel ftree-panel"><div class="ftree" id="ftree" role="tree" data-path="${esc(path)}">
        ${phone ? levelHTML(n) : `${!path ? '<div class="frow rootdrop" data-drop="" style="--d:0"><span class="fic">' + icon('folder') + '</span><span class="muted small">top level · drop here to take something out of its folder</span></div>' : `<div class="frow rootdrop" data-drop="${esc(path)}" style="--d:0"><span class="fic">${icon('folder')}</span><span class="muted small">${esc(path)}</span>${roll(n)}</div>`}${treeHTML(n)}`}
      </div><div class="panel-foot small muted"><span>${UI.term('Folders', 'folder')} are organization only: no permissions, defaults or behavior. Teams own those.</span></div></div>
      ${path ? '' : `<details class="panel peers-panel"${D.prefs.peersOpen ? ' open' : ''}><summary class="panel-head"><h3>Peers &amp; links map</h3><span class="muted small">inside a team bots are peers; across teams they're isolated unless linked</span></summary><div class="panel-body tight">${UI.peersGraph()}</div></details>`}
    </div>`;
  }
  /* a folder, everything in it: bots, teams and workflows, with roll-ups and what needs you there */
  const movesCallout = () => { const moves = D.pending.filter((c) => ((c.coll === 'bots' || c.coll === 'teams') && c.before && c.after && (c.before.folder || '') !== (c.after.folder || '')) || (c.coll === 'workflows' && /^move workflow/.test(c.summary || '')) || (c.coll === 'packs' && /^move pack workflow/.test(c.summary || ''))).length; return moves ? `<div class="callout small staged-moves">${icon('folder')} <b>${moves} move${moves > 1 ? 's' : ''} staged.</b> Folders live in config, so moves apply through the diff.<a class="btn sm primary" href="#/config/apply">Review &amp; apply</a></div>` : ''; };
  function folderView(path, tab) {
    const root = build(); let n = findNode(root, path);
    while (!n && path) { path = parentOf(path); n = findNode(root, path); }
    if (!path) return page('');
    tab = ['bots', 'teams', 'workflows'].includes(tab) ? tab : 'bots';
    const phone = innerWidth <= 720, base = '#/folders/' + enc(path);
    const bots = botsUnder(n), teams = D.teams.map((t) => t.name).filter((t) => UI.folderOfTeam(t) && within(UI.folderOfTeam(t), path)), ws = UI.wfsIn(path), wr = UI.wfRoll(ws);
    const pend = bots.reduce((a, b) => a + pendingOf(b), 0), live = bots.filter((b) => (M(b) || {}).home === 'live').length;
    const fails = ws.map((w) => [w, lastRun(w.name)]).filter(([, r]) => r && (r.status === 'fail' || r.status === 'stopped'));
    const att = D.attention.filter((a) => { const b = UI.botOfHref(a.href); return b && bots.includes(b); });
    const props = D.proposals.filter((p) => p.status === 'pending' && UI.live(p) && bots.includes(p.bot) && !att.some((a) => (a.sub || '').includes(p.id))).map((p) => ({ s: 'wait', title: `${p.verb || p.kind} · ${p.bot}`, sub: `${p.id} · ${p.summary || p.tier}`, href: `#/bots/${p.bot}/proposals` }));
    const needs = att.concat(props).map((a) => `<a class="item click" href="${a.href}">${st(a.s === 'ember' ? 'wait' : a.s || 'wait', '')}<div class="grow small"><b>${esc(a.title)}</b><div class="muted tiny ellipsis">${esc(a.sub || '')}</div></div><span class="chev">${icon('chev')}</span></a>`)
      .concat(fails.map(([w, r]) => `<a class="item click" href="#/runs/${r.id}">${st('fail', '')}<div class="grow small"><b>${esc(w.name)} failed</b><div class="muted tiny ellipsis">${esc(r.error || 'its last run failed')}</div></div><span class="chev">${icon('chev')}</span></a>`));
    const stat = (k, v, d, hot) => `<div class="panel stat${hot ? ' hot' : ''}"><span class="k">${k}</span><span class="v">${v}</span>${d ? `<span class="d">${d}</span>` : ''}</div>`;
    const body = tab === 'workflows' ? (UI.wfTable ? UI.wfTable(ws, { folder: path }) : '')
      : tab === 'teams' ? `<div class="panel"><div class="list">${teams.map((t) => { const c = cfgOf('teams', t, true), bs = D.bots.filter((b) => cfgOf('bots', b.name, true).team === t); return `<a class="item click" href="#/teams/${t}"><span class="fic team-ic">${icon('members')}</span><div class="grow"><b>${esc(t)}</b><div class="muted tiny">${bs.length} bot${bs.length === 1 ? '' : 's'}${c.lead ? ' · lead ' + esc(c.lead) : ''} · ${esc(UI.folderOfTeam(t))}</div></div><span class="chev">${icon('chev')}</span></a>`; }).join('') || '<div class="empty small">No teams in this folder.</div>'}</div></div>`
      : `<div class="panel ftree-panel"><div class="ftree" id="ftree" role="tree" data-path="${esc(path)}">${phone ? levelHTML(n) : `<div class="frow rootdrop" data-drop="${esc(path)}" style="--d:0"><span class="fic">${icon('folder')}</span><span class="muted small">${esc(path)}</span></div>${treeHTML(n)}`}</div></div>`;
    return `<div class="page">
      ${UI.head(esc(leaf(path)), { crumbs: UI.folderCrumbs(parentOf(path)), sub: `folder ${esc(path)} · organization only`, actions: `<button class="btn" data-act="f-new" data-parent="${esc(path)}">${icon('folder')}New folder inside</button><button class="btn ghost" data-act="f-menu" data-path="${esc(path)}" aria-label="Folder actions">${icon('more')}</button>` })}
      ${movesCallout()}
      <div class="grid g4 stats folder-stats">${stat('Bots', bots.length, `${live} live · ${teams.length} team${teams.length === 1 ? '' : 's'}`)}${stat('Workflows', wr.n, `${wr.runs} runs 24h · ${wr.ok}% ok`)}${stat('Failing', wr.failing, wr.failing ? 'last run failed' : 'none', wr.failing)}${stat('Approvals', pend, 'pending in this folder', pend)}</div>
      <div class="panel" style="margin:14px 0"><div class="panel-head"><span class="attn-mark">${icon('diamond')}</span><h3>Needs you here</h3><span class="muted small">${needs.length} item${needs.length === 1 ? '' : 's'} in ${esc(path)}</span></div><div class="list">${needs.join('') || '<div class="item small muted">Nothing in this folder needs you.</div>'}</div></div>
      ${UI.tabs([['bots', 'Bots', base + '/bots', bots.length], ['teams', 'Teams', base + '/teams', teams.length], ['workflows', 'Workflows', base + '/workflows', ws.length]], tab)}
      ${body}
      <div class="small muted" style="margin-top:12px">Folders are organization only: no permissions, defaults or behavior.</div></div>`;
  }
  UI.route('bots', () => page(''), { nav: 'bots' });
  UI.route('folders/:path/:tab?', ({ path, tab }) => folderView(path, tab), { nav: 'bots' });
  document.addEventListener('toggle', (e) => { if (e.target.matches && e.target.matches('details.peers-panel')) { D.prefs.peersOpen = e.target.open; S.save(); } }, true);

  UI.act('f-q', (el) => { fv.q = el.value; const box = UI.$('#ftree'); const root = build(), n = findNode(root, box.dataset.path) || root; const rows = innerWidth <= 720 ? levelHTML(n) : treeHTML(n); box.querySelectorAll('.frow:not(.rootdrop), .empty, .drill, [role="group"]').forEach((x) => x.remove()); box.insertAdjacentHTML('beforeend', rows); });
  UI.act('f-toggle', (b) => { const s = closed(); s.has(b.dataset.path) ? s.delete(b.dataset.path) : s.add(b.dataset.path); D.prefs.closedFolders = [...s]; S.save(); UI.rerender(); });

  /* ---------- folder operations ---------- */
  const SEG = /^[\w][\w .-]*$/;
  const itemsUnder = (path) => ({ teams: D.teams.map((t) => t.name).filter((t) => within(UI.folderOfTeam(t), path) && UI.folderOfTeam(t)), bots: D.bots.map((b) => b.name).filter((b) => { const c = cfgOf('bots', b, true); return !c.team && c.folder && within(c.folder, path); }) });
  function stageFolder(coll, name, folder, summary) { const c = clone(S.staged(coll, name)); if (folder) c.folder = folder; else delete c.folder; return S.stage(coll, name, c, summary); }
  UI.act('f-new', (b) => UI.form({ caps: 'organization only', title: 'New folder', intro: 'A folder groups bots and teams for display, filtering and search. It has no permissions, defaults or behavior.',
    values: { parent: b.dataset.parent || '' },
    fields: [{ k: 'parent', type: 'enum', label: 'Inside', options: [['', 'top level']].concat(UI.allFolders().map((f) => [f, f])), create: 'folder', createLabel: 'New folder' }, { k: 'name', type: 'string', req: true, ph: 'e.g. clients', pattern: SEG, patternMsg: 'Letters, digits, space, . _ -; no slashes (nest with Inside).' }],
    submit: 'Create folder', submitIcon: 'folder',
    onSubmit: (v) => { const path = (v.parent ? v.parent + '/' : '') + v.name.trim(); if (UI.allFolders().includes(path)) return `${path} exists already.`; D.folderDrafts = (D.folderDrafts || []).concat([path]); S.save(); UI.rerender(); UI.toast(`Folder ${esc(path)} created`, 'it reaches config when you move a bot or team into it', 'ok'); } }));
  UI.act('f-menu', (b) => { const p = b.dataset.path, it = itemsUnder(p); UI.menu(b, [['New folder inside', 'folder', 'f-new', `data-parent="${esc(p)}"`], ['Rename…', 'edit', 'f-rename', `data-path="${esc(p)}"`], ['Open', 'chev', 'f-open', `data-path="${esc(p)}"`], ['-'], [it.teams.length + it.bots.length ? `Delete (moves ${it.teams.length + it.bots.length} up)` : 'Delete', 'trash', 'f-delete', `data-path="${esc(p)}"`]]); });
  UI.act('f-open', (b) => UI.go('#/folders/' + enc(b.dataset.path)));
  UI.act('f-rename', (b) => {
    const p = b.dataset.path;
    UI.form({ caps: `folder ${esc(p)}`, title: 'Rename folder', edit: true, values: { name: leaf(p) }, fields: [{ k: 'name', type: 'string', req: true, pattern: SEG, patternMsg: 'Letters, digits, space, . _ -; no slashes.' }],
      note: 'Renaming rewrites the <span class="mono">folder:</span> of every team and solo bot inside it, so it goes through the config diff.',
      submit: 'Rename', submitIcon: 'check',
      onSubmit: (v) => {
        const np = (parentOf(p) ? parentOf(p) + '/' : '') + v.name.trim(); if (np === p) return false; if (UI.allFolders().includes(np)) return `${np} exists already.`;
        const it = itemsUnder(p), re = (f) => np + f.slice(p.length);
        D.folderDrafts = (D.folderDrafts || []).map((f) => (within(f, p) ? re(f) : f));
        let last = null;
        it.teams.forEach((t) => (last = stageFolder('teams', t, re(UI.folderOfTeam(t)), `rename folder ${p} → ${np}: teams.${t}`) || last));
        it.bots.forEach((bn) => (last = stageFolder('bots', bn, re(cfgOf('bots', bn, true).folder), `rename folder ${p} → ${np}: bots.${bn}`) || last));
        D.prefs.closedFolders = (D.prefs.closedFolders || []).map((f) => (within(f, p) ? re(f) : f));
        S.save(); UI.closeModal();
        if (last) UI.staged(last); else { UI.rerender(); UI.toast(`Renamed to ${esc(np)}`, 'an empty folder lives only in this browser', 'ok'); }
        return false;
      } });
  });
  UI.act('f-delete', (b) => {
    const p = b.dataset.path, it = itemsUnder(p), n = it.teams.length + it.bots.length, up = parentOf(p);
    const drop = () => { D.folderDrafts = (D.folderDrafts || []).filter((f) => !within(f, p)); D.prefs.closedFolders = (D.prefs.closedFolders || []).filter((f) => !within(f, p)); S.save(); };
    if (!n) { drop(); if (location.hash.startsWith('#/folders/' + enc(p))) UI.go(up ? '#/folders/' + enc(up) : '#/bots'); else UI.rerender(); UI.toast(`Deleted ${esc(p)}`, 'it was empty', 'ok'); return; }
    UI.confirm({ title: `Delete folder ${esc(p)}?`, body: `Its ${n} item${n > 1 ? 's' : ''} (${it.teams.map((t) => 'team ' + t).concat(it.bots).join(', ')}) move up to <b>${up ? esc(up) : 'the top level'}</b>. Nothing else changes: folders carry no behavior. Stages the moves for review.`, ok: 'Stage the moves', danger: true }).then((ok) => {
      if (!ok) return; let last = null;
      const re = (f) => (up ? up + f.slice(p.length) : f.slice(p.length + 1)) || up;
      it.teams.forEach((t) => (last = stageFolder('teams', t, re(UI.folderOfTeam(t)), `delete folder ${p}: teams.${t} → ${up || 'top level'}`) || last));
      it.bots.forEach((bn) => (last = stageFolder('bots', bn, re(cfgOf('bots', bn, true).folder), `delete folder ${p}: bots.${bn} → ${up || 'top level'}`) || last));
      drop(); UI.staged(last);
    });
  });

  /* ---------- moving a team or a solo bot ---------- */
  function move(kind, name, folder) {
    const coll = kind === 'team' ? 'teams' : 'bots';
    const ch = stageFolder(coll, name, folder, `move ${kind === 'team' ? 'team ' : ''}${name} → ${folder || 'top level'}`);
    D.folderDrafts = (D.folderDrafts || []).filter((f) => !within(folder || '\u0000', f));
    S.save(); UI.refreshChrome(); UI.rerender();
    UI.toast(ch ? `Move staged: ${esc(name)} → ${esc(folder || 'top level')}` : 'Already there', ch ? 'review and apply it with the other staged changes' : '', ch ? 'run' : 'idle');
  }
  UI.act('f-item-menu', (b) => {
    const kind = b.dataset.kind, name = b.dataset.name, onTeam = kind === 'bot' && cfgOf('bots', name, true).team;
    UI.menu(b, [['Open', 'chev', 'f-go', `data-href="${kind === 'team' ? '#/teams/' : '#/bots/'}${name}"`], ['Move to…', 'folder', 'f-move', onTeam ? UI.off(`${name} is on team ${onTeam}, so it shows under the team: move the team, or take ${name} off the team in its definition`) : `data-kind="${kind}" data-name="${name}"`]]);
  });
  UI.act('f-go', (b) => UI.go(b.dataset.href));
  UI.act('f-move', (b) => {
    const kind = b.dataset.kind, name = b.dataset.name, cur = kind === 'team' ? UI.folderOfTeam(name) : cfgOf('bots', name, true).folder || '';
    UI.form({ caps: `${kind === 'team' ? 'teams' : 'bots'}.${name}.folder`, title: `Move ${kind === 'team' ? 'team ' : ''}${name}`, values: { to: cur },
      fields: [{ k: 'to', type: 'enum', label: 'Folder', options: [['', 'top level']].concat(UI.allFolders().map((f) => [f, f])), create: 'folder', createLabel: 'New folder', createCtx: { parent: cur } }],
      note: 'Folders live in config, so the move is staged for the diff. It changes nothing else.', submit: 'Stage move', submitIcon: 'folder',
      onSubmit: (v) => move(kind, name, v.to || '') });
  });
  /* drag-and-drop onto a folder row (mouse); touch uses Move to… */
  let drag = null;
  document.addEventListener('dragstart', (e) => { const r = e.target.closest && e.target.closest('.ftree [data-drag]'); if (!r) return; drag = r.dataset.drag; e.dataTransfer.effectAllowed = 'move'; e.dataTransfer.setData('text/plain', drag); r.classList.add('dragging'); });
  document.addEventListener('dragend', () => { drag = null; UI.$$('.ftree .dragging, .ftree .drop-on').forEach((x) => x.classList.remove('dragging', 'drop-on')); });
  document.addEventListener('dragover', (e) => { if (!drag) return; const f = e.target.closest('.ftree [data-drop]'); UI.$$('.ftree .drop-on').forEach((x) => x !== f && x.classList.remove('drop-on')); if (!f) return; e.preventDefault(); f.classList.add('drop-on'); });
  document.addEventListener('drop', (e) => { if (!drag) return; const f = e.target.closest('.ftree [data-drop]'); if (!f) return; e.preventDefault(); const [kind, name] = drag.split(':'); drag = null; move(kind, name, f.dataset.drop); });

  /* ⌘K: folders, teams and bots, with their paths */
})();
