/* Bots: managing bots and building teams (the chosen direction 1, "teams are the page"). Each team is a section whose
 * header says, in words, its shared rules (the most its bots may do, who approves, what work is routed to it, its
 * budget) and builds the team in place (Add a bot, Shape the team); its bots are below; solo bots come last. A row
 * says what the bot is for, then its facts (lead, live or practice, what it may do, what it's connected to, its
 * model); its status is one quiet mark that links to Needs you or Activity, which answer "what needs me" and "what's
 * running". Select rows for bulk changes, each staged into the one diff. Folders are gone from the dashboard. */
(function () {
  const { icon, esc } = UI;
  const T = (n) => D.teams.find((t) => t.name === n), M = (n) => D.bots.find((b) => b.name === n);
  const clone = (o) => JSON.parse(JSON.stringify(o || {}));
  const cfgOf = (coll, name) => S.staged(coll, name) || ((coll === 'teams' ? T(name) : M(name)) || {}).cfg || {};
  UI.wfOwner = (w) => (w.source.startsWith('pack') ? 'pack' : w.source === 'bot' ? 'bot' : 'config');
  UI.packOf = (w) => w.source.replace(/^pack /, '');
  UI.botOfHref = (href) => { const m = String(href || '').match(/#\/bots\/([^/]+)/); if (m) return m[1]; const b = String(href || '').match(/#\/browser\/([^/]+)/); if (b) { const s = D.browsers.find((x) => x.id === b[1]); return s && s.identity.startsWith('bot:') ? s.identity.slice(4) : null; } return null; };
  UI.botOfRun = (r) => (r.workflow.startsWith('bot:') ? r.workflow.slice(4) : (D.workers.find((w) => w.run === r.id) || {}).bot || null);
  /** Whether an item tied to a bot is kept by a set of bots (a filter): an empty set keeps everything. */
  UI.scopeKeeps = (bot, set) => !set || !set.length || set.includes(bot);

  /* ---------- what a bot is, in facts ---------- */
  const teamOf = (b) => cfgOf('bots', b).team || '';
  const leadOf = (t) => { const c = cfgOf('teams', t); return S.getPath(c, 'routing.lead') || c.lead || ''; };
  const practice = (b) => { const c = cfgOf('bots', b), t = teamOf(b); return !!(S.getPath(c, 'autonomy.shadow') || (t && S.getPath(cfgOf('teams', t), 'autonomy.shadow')) || (M(b) || {}).shadow); };
  const archived = (b) => !!cfgOf('bots', b).disabled;
  const modelOf = (b) => { const c = cfgOf('bots', b); return S.getPath(c, 'worker.model') || S.getPath(c, 'worker.runtime') || S.getPath(c, 'runtime') || 'paseo'; };
  const connsOf = (b) => { const c = cfgOf('bots', b), sv = S.getPath(c, 'skill.verbs'), vs = Array.isArray(sv) ? sv : sv ? Object.keys(sv) : [];
    return [...new Set(vs.map((v) => v.split('.')[0]).map((inst) => { const cn = S.view().connectors.find((x) => x.name === inst); if (!cn) return ''; const use = String(((cn.cfg || {}).use) || cn.use || '').split('@')[0]; return (SCHEMA.connectors[use] || {}).label || use; }).filter(Boolean))]; };
  const mayWords = (m) => { const x = UI.botCan ? UI.botCan(m) : { own: [], asks: [] }; return x.own.length && x.asks.length ? `${x.own.length} on its own · asks before ${x.asks.length}` : x.asks.length ? `asks before everything (${x.asks.length})` : x.own.length ? `${x.own.length} on its own` : 'only reads'; };
  const needOf = (b) => D.attention.filter((a) => UI.botOfHref(a.href) === b).length;
  const quiet = (m) => { const n = needOf(m.name); return n ? `<a class="q-st need" href="#/attention">${UI.mark('ember')}${n} need${n > 1 ? '' : 's'} you</a>` : m.home === 'live' ? `<a class="q-st" href="#/activity?bot=${encodeURIComponent(m.name)}">${UI.mark('run')}Working</a>` : ''; };
  const facts = (m, o = {}) => UI.factsLine([o.team ? (teamOf(m.name) ? { icon: 'obj-team', value: teamOf(m.name), href: `#/teams/${teamOf(m.name)}`, pri: 1, label: 'Team' } : { icon: 'obj-team', value: 'solo', pri: 1, label: 'Not on a team' }) : null,
    teamOf(m.name) && leadOf(teamOf(m.name)) === m.name ? { icon: 'sparkle', value: 'lead', pri: 1, label: 'Leads its team' } : null,
    archived(m.name) ? { icon: 'box', value: 'archived', pri: 1, label: 'Switched off' } : practice(m.name) ? { icon: 'ghost', value: 'practice mode', pri: 1, label: 'Proposes, never acts' } : { icon: 'play', value: 'live', pri: 3, label: 'Acts as allowed' },
    { icon: 'shield', value: mayWords(m), href: `#/bots/${m.name}/access`, pri: 2, label: 'What it may do' },
    connsOf(m.name).length ? { icon: 'link', value: connsOf(m.name).join(', '), pri: 2, label: 'Connected to' } : null, { icon: 'code', value: modelOf(m.name), pri: 3, label: 'Model or runtime' }]);

  /* ---------- a team, in words ---------- */
  const CEIL = { observe: 'Its bots only read', reversible: 'Its bots do undoable things on their own; ask before the rest', propose: 'Its bots ask before anything that changes something', commit_money: 'Its bots may spend within the budget' };
  const teamFacts = (t) => { const c = cfgOf('teams', t), d = c.defaults || {}, ceil = S.getPath(c, 'autonomy.ceiling') || S.getPath(d, 'autonomy.max_tier'), ops = c.operators || d.operators || [], rules = S.getPath(c, 'routing.rules') || [], fb = S.getPath(c, 'routing.fallback');
    // dollar budgets only when spend is shown (usage display: api); otherwise the run budget, never a dollar figure
    const dollars = UI.usageMode && UI.usageMode() === 'api';
    const budget = dollars && S.getPath(c, 'budget.spend_cap.per_period') ? `spend capped at $${Number(S.getPath(c, 'budget.spend_cap.per_period')).toLocaleString()}` : S.getPath(c, 'budget.proactive.max_runs_per_day') ? `${S.getPath(c, 'budget.proactive.max_runs_per_day')} self-started runs a day` : dollars && S.getPath(d, 'budget.max_cost_per_day') ? `$${S.getPath(d, 'budget.max_cost_per_day')} a day` : '';
    return UI.factsLine([ceil ? { icon: 'shield', value: CEIL[ceil] || ceil, href: `#/teams/${t}/defaults`, pri: 1, label: 'The most its bots may do' } : null, ops.length ? { icon: 'members', value: `approved by ${ops.join(', ')}`, pri: 1, label: 'Who approves' } : null,
      rules.length || fb ? { icon: 'zap', value: `${rules.length ? `${rules.length} routing rule${rules.length > 1 ? 's' : ''}` : ''}${rules.length && fb ? ' · ' : ''}${fb ? `the rest to ${fb}` : ''}`, href: `#/teams/${t}/routing`, pri: 2, label: 'How work is routed to it' } : { icon: 'zap', value: 'no routing yet', href: `#/teams/${t}/routing`, pri: 2 },
      S.getPath(c, 'isolation.sandbox') === 'required' || S.getPath(d, 'isolation') ? { icon: 'obj-sandbox', value: 'sandboxed', pri: 3, label: 'Sandbox' } : null, budget ? { icon: 'dollar', value: budget, pri: 3, label: 'Budget' } : null]); };

  /* ---------- the page ---------- */
  const bv = { q: '', team: [], mode: [], model: [], conn: [], group: 'team', sel: new Set(), open: null };
  const allBots = () => S.view().bots.map((b) => M(b.name) || b);
  const modeOf = (m) => (archived(m.name) ? 'archived' : practice(m.name) ? 'practice' : 'live');
  const keep = (m) => (!bv.team.length || bv.team.includes(teamOf(m.name) || 'solo')) && (!bv.mode.length || bv.mode.includes(modeOf(m))) && (!bv.model.length || bv.model.includes(modelOf(m.name))) && (!bv.conn.length || connsOf(m.name).some((x) => bv.conn.includes(x)))
    && (!bv.q || `${m.name} ${m.desc || ''} ${cfgOf('bots', m.name).description || ''} ${teamOf(m.name)}`.toLowerCase().includes(bv.q.toLowerCase()));
  const tally = (xs) => { const mm = new Map(); xs.filter(Boolean).forEach((x) => mm.set(x, (mm.get(x) || 0) + 1)); return [...mm].map(([v, n]) => ({ v, label: v, n })); };
  const bar = (bots) => UI.filterBar({ id: 'bots', state: bv, placeholder: 'Find a bot, or filter: team:platform', label: 'Find and filter bots', fields: [
    { k: 'team', label: 'Team', multi: true, options: () => tally(bots.map((m) => teamOf(m.name) || 'solo')) },
    { k: 'mode', label: 'Mode', multi: true, keepOrder: true, options: () => [['live', 'Live'], ['practice', 'Practice mode'], ['archived', 'Archived']].map(([v, label]) => ({ v, label, n: bots.filter((m) => modeOf(m) === v).length })).filter((o) => o.n) },
    { k: 'model', label: 'Model', multi: true, options: () => tally(bots.map((m) => modelOf(m.name))) },
    { k: 'conn', label: 'Connected to', multi: true, plural: 'services', options: () => tally(bots.flatMap((m) => connsOf(m.name))) },
    { k: 'group', label: 'Group by', view: true, keepOrder: true, def: 'team', title: 'Group by', find: 'Group by', options: () => [{ v: 'team', label: 'Team' }, { v: 'none', label: 'None' }] }], onChange: () => UI.rerender({ keepScroll: true }) });
  const cbx = (m) => `<input type="checkbox" class="cbx m-cbx" data-act="bv-sel" data-m="${esc(m.name)}" aria-label="Select ${esc(m.name)}"${bv.sel.has(m.name) ? ' checked' : ''}>`;
  const row = (m, o = {}) => `<li class="m-row${archived(m.name) ? ' off' : ''}" data-bot="${esc(m.name)}"><span class="m-sel">${cbx(m)}</span>${UI.botAv(m.name, 'md')}
    <a class="m-main" href="#/bots/${m.name}"><b>${esc(UI.botName(m.name))}</b>${UI.botName(m.name) !== m.name ? ` <span class="bav-id">${esc(m.name)}</span>` : ''}<span class="dl-why">${esc(cfgOf('bots', m.name).description || m.desc || '')}</span>${facts(m, o)}</a>
    <span class="m-st">${quiet(m)}</span><button type="button" class="icon-btn" data-act="bv-more" data-m="${esc(m.name)}" aria-label="More for ${esc(m.name)}">${icon('more')}</button></li>`;
  const isOpen = (key, i, total) => (bv.open && key in bv.open ? bv.open[key] : total <= 40 || i === 0);
  const teamSection = (t, ms, i, total) => { const open = isOpen(t, i, total), lead = leadOf(t), prac = ms.filter((m) => practice(m.name)).length, need = ms.reduce((a, m) => a + needOf(m.name), 0), shown = ms.filter(keep);
    return `<section class="dl-card m-team" data-team="${esc(t)}"><header class="m-team-h"><button type="button" class="m-tog" data-act="bv-tog" data-k="${esc(t)}" aria-expanded="${open}" aria-label="${open ? 'Fold' : 'Show'} ${esc(t)}">${icon(open ? 'chevd' : 'chev')}</button>
      <div class="m-team-t"><div class="m-team-n">${UI.teamAv(t, 'md')}<h2><a class="link-plain" href="#/teams/${t}">${esc(t)}</a></h2></div><span class="dl-sub">${ms.length} bot${ms.length === 1 ? '' : 's'}${lead ? ` · led by ${esc(lead)}` : ' · no lead yet'}${prac ? ` · ${prac} in practice mode` : ''}</span>${teamFacts(t)}</div>
      <span class="m-st">${need ? `<a class="q-st need" href="#/attention">${UI.mark('ember')}${need} need${need > 1 ? '' : 's'} you</a>` : ''}</span>
      <div class="m-team-a"><button type="button" class="btn sm secondary" data-act="bv-team-add" data-t="${esc(t)}">${icon('plus')}Add a bot</button><a class="btn sm secondary ph-hide" href="#/teams/${t}/defaults">${icon('settings')}Shape the team</a><button type="button" class="icon-btn" data-act="bv-team-more" data-t="${esc(t)}" aria-label="More for team ${esc(t)}">${icon('more')}</button></div></header>
      ${open ? `<ul class="m-list">${shown.map((m) => row(m)).join('') || '<li class="dl-empty-row">No bot here matches the filter.</li>'}</ul>` : ''}</section>`; };
  function page() {
    const bots = allBots(), teams = D.teams.map((t) => t.name), solo = bots.filter((m) => !teamOf(m.name)), shown = bots.filter(keep), total = bots.length;
    const bulk = bv.sel.size ? `<div class="m-bulk"><b>${bv.sel.size} selected</b><button class="btn sm secondary" data-act="bv-bulk-team">Move to a team…</button><button class="btn sm secondary" data-act="bv-bulk-model">Change model…</button><button class="btn sm secondary" data-act="bv-bulk-practice">Practice mode</button><button class="btn sm secondary" data-act="bv-bulk-archive">Archive…</button><button class="link" data-act="bv-sel-clear">Clear</button></div>` : '';
    const groups = bv.group === 'none' ? `<section class="dl-card m-team"><ul class="m-list m-flat">${shown.sort((a, b) => a.name.localeCompare(b.name)).map((m) => row(m, { team: true })).join('') || '<li class="dl-empty-row">No bot matches.</li>'}</ul></section>`
      : teams.filter((t) => !bv.team.length || bv.team.includes(t)).map((t, i) => teamSection(t, bots.filter((m) => teamOf(m.name) === t), i, total)).join('')
        + ((!bv.team.length || bv.team.includes('solo')) && solo.length ? (() => { const open = isOpen('solo', 1, total), sh = solo.filter(keep);
          return `<section class="dl-card m-team" data-team="solo"><header class="m-team-h"><button type="button" class="m-tog" data-act="bv-tog" data-k="solo" aria-expanded="${open}" aria-label="${open ? 'Fold' : 'Show'} solo bots">${icon(open ? 'chevd' : 'chev')}</button><div class="m-team-t"><h2>Solo</h2><span class="dl-sub">${solo.length} bot${solo.length === 1 ? '' : 's'} not on a team</span></div><span class="m-st"></span><div class="m-team-a"><button type="button" class="btn sm secondary" data-act="team-new">${icon('members')}Make a team…</button></div></header>${open ? `<ul class="m-list">${sh.map((m) => row(m)).join('') || '<li class="dl-empty-row">No solo bot matches the filter.</li>'}</ul>` : ''}</section>`; })() : '');
    return `<div class="page dl-page bot-list m-page">
      <div class="dl-phead"><div><h1>Bots <span class="h1-count">${total} bot${total === 1 ? '' : 's'} · ${teams.length} team${teams.length === 1 ? '' : 's'} · ${solo.length} solo${shown.length < total ? ` · showing ${shown.length}` : ''}</span></h1><p class="lede-2">A ${UI.term('bot')} is an AI teammate with memory; on a ${UI.term('team')}, bots share who approves, the most they may do, and the work routed to them.</p></div><div class="dl-actions tight"><button class="btn secondary" data-act="team-new">${icon('members')}New team</button><a class="btn primary" href="#/bots/new">${icon('plus')}New bot</a></div></div>
      ${bar(bots)}
      ${bulk}${groups}</div>`;
  }
  UI.route('bots', () => page(), { nav: 'bots' });
  UI.botsPage = () => page(); // drawn under the New bot sheet
  // folders are gone from the dashboard: old folder links land on Bots
  UI.route('folders/:path/:tab?', () => { setTimeout(() => location.replace('#/bots')); return ''; }, { nav: 'bots' });

  /* ---------- actions: each change is staged into the one diff ---------- */
  const stageBot = (name, fn, why) => { const c = clone(cfgOf('bots', name)); fn(c); return S.stage('bots', name, c, why); };
  UI.act('bv-tog', (b) => { bv.open = bv.open || {}; bv.open[b.dataset.k] = b.getAttribute('aria-expanded') !== 'true'; UI.rerender({ keepScroll: true }); });
  UI.act('bv-clear', () => { Object.assign(bv, { q: '', team: [], mode: [], model: [], conn: [] }); UI.rerender({ keepScroll: true }); });
  UI.act('bv-sel', (cb) => { cb.checked ? bv.sel.add(cb.dataset.m) : bv.sel.delete(cb.dataset.m); UI.rerender({ keepScroll: true }); });
  UI.act('bv-sel-clear', () => { bv.sel.clear(); UI.rerender({ keepScroll: true }); });
  const teamOptions = () => [['', 'No team (solo)']].concat(D.teams.map((t) => [t.name, t.name]));
  const moveTo = (names, team) => { let last = null; names.forEach((n) => { if ((teamOf(n) || '') === (team || '')) return; last = stageBot(n, (c) => { if (team) c.team = team; else delete c.team; }, team ? `${n} joins team ${team}` : `${n} leaves its team`); }); return last; };
  UI.act('bv-bulk-team', () => { const names = [...bv.sel]; UI.form({ caps: 'bots', title: `Move ${names.length} bot${names.length > 1 ? 's' : ''} to a team`, fields: [{ k: 'team', type: 'enum', label: 'Team', options: teamOptions(), def: '' }], note: 'One staged change per bot, reviewed together as one diff. A bot takes its new team’s rules: the most it may do can only narrow.', submit: 'Stage moves', onSubmit: (v) => { const ch = moveTo(names, v.team); bv.sel.clear(); if (ch) UI.staged(ch); else UI.rerender(); } }); });
  UI.act('bv-bulk-model', () => { const names = [...bv.sel]; UI.form({ caps: 'bots', title: `Change the model for ${names.length} bot${names.length > 1 ? 's' : ''}`, fields: [{ k: 'model', type: 'string', req: true, label: 'Model (or fleet)', ph: 'claude-sonnet-5', help: 'A model id or a fleet name (fixer, reviewer…).' }], submit: 'Stage changes', onSubmit: (v) => { let last = null; names.forEach((n) => { last = stageBot(n, (c) => S.setPath(c, 'worker.model', v.model), `${n} uses ${v.model}`); }); bv.sel.clear(); UI.staged(last); } }); });
  UI.act('bv-bulk-practice', () => { let last = null; [...bv.sel].forEach((n) => { last = stageBot(n, (c) => S.setPath(c, 'autonomy.shadow', true), `${n} in practice mode`); }); bv.sel.clear(); UI.staged(last); });
  UI.act('bv-bulk-archive', () => { const names = [...bv.sel]; UI.confirm({ title: `Archive ${names.length} bot${names.length > 1 ? 's' : ''}?`, body: 'An archived bot stops: it hears no events and starts nothing. Its memory and history stay, and you can switch it back on. This stages the change for review.', ok: 'Stage archive', danger: true }).then((ok) => { if (!ok) return; let last = null; names.forEach((n) => { last = stageBot(n, (c) => { c.disabled = true; }, `archive ${n}`); }); bv.sel.clear(); UI.staged(last); }); });
  UI.act('bv-team-add', (b) => { const t = b.dataset.t, others = allBots().filter((m) => teamOf(m.name) !== t);
    UI.form({ caps: `team ${t}`, title: `Add a bot to ${t}`, fields: [{ k: 'bot', type: 'enum', req: true, label: 'Bot', options: others.map((m) => [m.name, `${m.name}${teamOf(m.name) ? ` · now on ${teamOf(m.name)}` : ' · solo'}`]), create: 'bot', createLabel: 'A new bot' }], note: `It takes ${esc(t)}’s rules; the most it may do can only narrow.`, submit: 'Stage it', onSubmit: (v) => UI.staged(moveTo([v.bot], t)) }); });
  UI.act('bv-team-more', (b) => { const t = b.dataset.t; UI.menu(b, [['Open the team', 'members', 'strip-go', `data-href="#/teams/${t}"`], ['Shape the team', 'settings', 'strip-go', `data-href="#/teams/${t}/defaults"`], ['How work is routed…', 'zap', 'strip-go', `data-href="#/teams/${t}/routing"`], ['Shared knowledge', 'file', 'strip-go', `data-href="#/teams/${t}/knowledge"`], ['-'], ['Edit the team…', 'edit', 'team-edit', `data-t="${esc(t)}"`]]); });
  UI.act('bv-more', (b) => { const n = b.dataset.m, m = M(n) || { name: n };
    UI.menu(b, [['Open', 'bots', 'strip-go', `data-href="#/bots/${n}"`], ['Edit its settings', 'edit', 'strip-go', `data-href="#/bots/${n}/settings"`], ['Clone…', 'copy', 'bv-clone', `data-m="${esc(n)}"`], ['Move to a team…', 'members', 'bv-move', `data-m="${esc(n)}"`],
      [practice(n) ? 'Switch to live…' : 'Practice mode', 'ghost', 'bv-practice', `data-m="${esc(n)}"`], ['-'], [archived(n) ? 'Switch back on' : 'Archive…', 'box', 'bv-archive', `data-m="${esc(n)}"`]]); void m; });
  UI.act('bv-move', (b) => { const n = b.dataset.m; UI.form({ caps: `bots.${n}.team`, title: `Move ${n} to a team`, values: { team: teamOf(n) }, fields: [{ k: 'team', type: 'enum', label: 'Team', options: teamOptions() }], submit: 'Stage move', onSubmit: (v) => { const ch = moveTo([n], v.team); if (ch) UI.staged(ch); } }); });
  UI.act('bv-practice', (b) => { const n = b.dataset.m, on = !practice(n); UI.staged(stageBot(n, (c) => { if (on) S.setPath(c, 'autonomy.shadow', true); else if (c.autonomy) delete c.autonomy.shadow; }, on ? `${n} in practice mode` : `${n} goes live`)); });
  UI.act('bv-archive', (b) => { const n = b.dataset.m, on = !archived(n); if (!on) { UI.staged(stageBot(n, (c) => { delete c.disabled; }, `switch ${n} back on`)); return; }
    UI.confirm({ title: `Archive ${esc(n)}?`, body: 'It stops: it hears no events and starts nothing. Its memory and history stay, and you can switch it back on. This stages the change for review.', ok: 'Stage archive', danger: true }).then((ok) => ok && UI.staged(stageBot(n, (c) => { c.disabled = true; }, `archive ${n}`))); });
  UI.act('bv-clone', (b) => { const n = b.dataset.m; UI.form({ caps: `a copy of ${n}`, title: `Clone ${n}`, values: { name: `${n}-2` }, fields: [{ k: 'name', type: 'string', req: true, pattern: /^[a-z][a-z0-9-]*$/, patternMsg: 'Lowercase letters, digits and dashes.', validate: (x) => (S.view().bots.some((m) => m.name === x) ? 'A bot by that name exists.' : '') }], note: 'The copy takes its settings, permissions and team; not its memory or history. It starts in practice mode.', submit: 'Stage the copy',
    onSubmit: (v) => { const c = clone(cfgOf('bots', n)); S.setPath(c, 'autonomy.shadow', true); delete c.disabled; UI.staged(S.stage('bots', v.name, c, `new bot ${v.name}, a copy of ${n}`)); } }); });
})();
