/* Tickets: the work conductor's bots do, as tickets, in conductor's own ticket system (there is no outside one). A
 * ticket moves draft → ready → in progress → waiting on you → done, or is declined. A draft is an idea that you and
 * the bots shape together (a bot may draft one, refine it, add notes); nobody starts work on it. Marking it ready, an
 * operator's explicit act with an assignee set (a bot, or a team whose lead or routing picks one), is what lets work
 * start. A ticket has a priority, labels, maybe a due date, a description and comments (the chat component: its
 * assignee answers there); its approvals, workers and hand-offs are linked. The store is the bots' ledger (D.cases).
 * Pages: #/tickets (a list or a board by status, with the shared filter bar), #/tickets/new, #/tickets/<id>; a bot's
 * Tickets tab and a team's Tickets tab are the same rows, narrowed. */
(function () {
  const esc = UI.esc, icon = UI.icon, K = () => UI.ticketKit;
  const now = () => new Date().toISOString();
  const audit = (event, detail) => D.audit.unshift({ ts: now(), event, who: 'operator:ops', detail, run: '' });
  const T = (id) => D.cases.find((c) => c.id === id);
  const PRI = [['urgent', 'Urgent'], ['high', 'High'], ['normal', 'Normal'], ['low', 'Low']];
  const priWord = (p) => (PRI.find(([k]) => k === p) || PRI[2])[1];
  const teamOf = (c) => c.team || ((K().teamOf(K().M(c.bot)) || {}).name) || '';
  UI.ticketTeam = teamOf;
  /** Where a ticket stands, in words: draft and ready are set by a person; in progress and waiting on you follow from
      the work; done or declined close it. [word, mark] */
  const STATUS = { draft: ['Draft', 'draft'], ready: ['Ready', 'ready'], progress: ['In progress', 'progress'], waiting: ['Waiting on you', 'ember'], done: ['Done', 'ok'], declined: ['Declined', 'fail'] };
  const ORDER = ['draft', 'ready', 'progress', 'waiting', 'done', 'declined'];
  const statusOf = (c) => c.status === 'closed' ? (c.declined ? 'declined' : 'done') : c.stage === 'draft' ? 'draft' : (D.proposals || []).some((p) => p.case === c.id && p.status === 'pending') ? 'waiting'
    : c.workers > 0 || (D.delegations || []).some((d) => d.parent === c.id && d.status === 'running') ? 'progress' : 'ready';
  UI.ticketStatus = (c) => { const k = statusOf(c); return { k, word: STATUS[k][0], mark: STATUS[k][1] }; };
  const assignee = (c) => (c.bot ? UI.botName(c.bot) : c.team ? `team ${c.team}` : 'Nobody yet');
  /** Who asked (or, for a draft, who wrote it), in words. */
  const askedBy = (c) => { const r = String(c.requester || ''); return /^operator:/.test(r) ? (r === 'operator:ops' ? 'you' : r.slice(9)) : /^event:pd/.test(r) ? 'a PagerDuty incident' : /^event:gh/.test(r) ? 'a GitHub event' : /^event:/.test(r) ? 'an event' : /^chat:/.test(r) ? 'someone in chat' : c.cls === 'proactive' ? 'a routine check' : /^bot:/.test(r) ? `${UI.botName(r.slice(4))} (a bot)` : r || 'someone'; };
  const dueWord = (d) => { if (!d) return ''; const days = Math.round((Date.parse(d + 'T12:00:00') - Date.now()) / 86400000); return days < 0 ? `overdue by ${-days} day${days === -1 ? '' : 's'}` : days === 0 ? 'due today' : days === 1 ? 'due tomorrow' : `due in ${days} days`; };
  /** A ticket's facts: who has it, priority when it isn't normal, when it's due, its labels. */
  UI.ticketFacts = (c, o = {}) => [o.noAssignee ? null : c.bot ? { icon: 'obj-bot', value: c.bot, href: `#/bots/${c.bot}`, pri: 1, label: 'Assigned to' } : c.team ? { icon: 'team', value: `team ${c.team}`, href: `#/teams/${c.team}`, pri: 1, label: 'Assigned to the team' } : null,
    c.priority === 'urgent' || c.priority === 'high' ? { icon: 'warn', value: priWord(c.priority), risky: c.priority === 'urgent', pri: 1, label: 'Priority' } : null,
    c.due && c.status !== 'closed' ? { icon: 'calendar', value: dueWord(c.due), risky: /overdue|today/.test(dueWord(c.due)), pri: 2, label: 'Due' } : null,
    ...liveFacts(c), ...(c.labels || []).map((l) => ({ icon: 'tag', value: l, pri: 3, label: 'Label' }))];
  // in progress is a state, not activity (59): its mark is static; only while a worker of it is executing now does the
  // row say so, with the spinner; otherwise its last activity, so a ticket that has stalled is visible
  const liveFacts = (c) => { if (UI.ticketStatus(c).k !== 'progress') return []; const ws = (D.workers || []).filter((w) => w.case === c.id), live = ws.filter((w) => w.status === 'run');
    if (live.length) return [{ glyph: UI.mark('run'), value: live.length === 1 ? 'a worker is running' : `${live.length} workers are running`, pri: 1, label: 'Executing now' }];
    const t = [c.updated, c.opened, ...ws.map((w) => w.started)].filter(Boolean).sort().pop(), old = t && Date.now() - Date.parse(t) > 864e5;
    if (!t || (t === c.opened && !old)) return []; // the row's age already says it
    return [{ icon: 'clock', value: `last activity ${UI.ago(t)} ago`, risky: old, pri: 2, label: old ? 'Nothing has happened on it for over a day' : 'Its last activity' }]; };
  /** A ticket's row: the same anatomy as every list (mark, title, a line in words, the facts, the age, the action). */
  const by = (c) => (statusOf(c) === 'draft' ? `drafted by ${esc(askedBy(c).replace(' (a bot)', ''))}` : `asked by ${esc(askedBy(c))}`);
  // the whole row opens the ticket; a button only for a real next action: a draft is marked ready, a ticket waiting on
  // you opens what it waits for (the approval)
  // the phone's status switcher scrolls: a fade at its edge until it's scrolled to the end (r5 major 16)
  (UI.afterRender = UI.afterRender || []).push(() => document.querySelectorAll('.tk-colpick').forEach((el) => { const f = () => { el.classList.toggle('scrolls', el.scrollWidth > el.clientWidth + 1); el.classList.toggle('at-end', el.scrollLeft + el.clientWidth >= el.scrollWidth - 2); }; el.onscroll = f; f(); }));
  UI.ticketAct = (c) => { const s = UI.ticketStatus(c), wait = s.k === 'waiting' && (D.proposals || []).find((p) => p.case === c.id && p.status === 'pending');
    return s.k === 'draft' ? `<span class="btn sm secondary" role="button" tabindex="0" data-act="tk-ready" data-t="${c.id}">Mark ready…</span>` : wait ? `<span class="btn sm secondary" role="link" tabindex="0" data-fx-href="#/bots/${wait.bot}/proposals/${wait.id}">Review</span>` : ''; };
  UI.ticketRow = (c, o = {}) => { const s = UI.ticketStatus(c), act = UI.ticketAct(c);
    return `<li class="dl-need tk-row" data-s="${s.mark}" data-t="${c.id}"><a class="dl-need-a" href="${o.base || '#/tickets'}/${c.id}"><span class="dl-ng">${UI.mark(s.mark, { lg: true, label: s.word })}</span>
      <span class="dl-nt"><b>${esc(c.title)}</b><span class="dl-why">${o.byStatus || o.statusFact ? by(c).replace(/^./, (x) => x.toUpperCase()) : `${s.word} · ${by(c)}`}</span>${UI.factsLine((o.statusFact ? [{ glyph: UI.glyph(UI.markGlyph ? UI.markGlyph(s.mark) : s.mark), value: s.word, pri: 1, label: 'Status' }] : []).concat(UI.ticketFacts(c, o)))}</span>
      <span class="dl-age">${UI.agoText(c.opened)}</span><span class="dl-na">${act}</span></a></li>`; };

  /* ---------- the comment thread: notes as chat rows ---------- */
  UI.ticketRows = (id) => { const c = T(id); return ((c && c.notes) || []).map((n) => { const w = String(n.who || '');
    if (/^operator:/.test(w)) return Object.assign({}, n, { who: 'op' });
    if (/^bot:/.test(w)) return Object.assign({}, n, { who: 'bot', bot: w.slice(4), text: String(n.text || '').replace(/^↩ result of d\d+: /, 'Reporting back on the part I was handed: ') });
    if (/^event:/.test(w)) return { who: 'event', at: n.at, text: n.text, source: askedBy({ requester: w }) };
    return { who: 'sys', at: n.at, text: n.text }; }); };
  UI.ticketComment = (id, key, v, done) => { const c = T(id); if (!c) return;
    const note = { at: now(), who: 'operator:ops', text: v.text, state: 'queued' };
    if (v.attachments.length) note.attachments = v.attachments.map(({ id: aid, kind, name, mime, size, thumb, preview }) => ({ id: aid, kind, name, mime, size, src: thumb, preview }));
    if (v.links.length) note.links = v.links.map(({ url, host, path }) => ({ url, host, path }));
    (c.notes = c.notes || []).push(note); audit('ticket_comment', `${id}`); S.save(); done(); UI.chatRedraw(key, '', id);
    setTimeout(() => { note.state = 'delivered'; S.save(); UI.chatRedraw(key, '', id); }, UI.fast ? 60 : 1400);
    if (c.bot && c.status === 'open') setTimeout(() => { c.notes.push({ at: now(), who: 'bot:' + c.bot, text: c.stage === 'draft' ? 'Noted. It’s still a draft, so I’ll help shape it but won’t start until it’s marked ready.' : 'Noted on the ticket. I’ll take it into account and report back here.', ack: true }); S.save(); UI.chatRedraw(key, '', id); }, UI.fast ? 140 : 2800);
  };

  /* ---------- the list ---------- */
  const tv = { q: '', who: [], status: [], priority: [], label: [], page: 0, older: false, col: '' };
  // the Board by default (60); your choice is remembered (per operator)
  const viewOf = () => (D.prefs && D.prefs.ticketView) || 'board';
  const allLabels = () => [...new Set(D.cases.flatMap((c) => c.labels || []))].sort();
  const keep = (c) => { const s = statusOf(c);
    return (!tv.who.length || tv.who.includes(c.bot || (c.team ? 'team:' + c.team : 'nobody')) || (c.team && tv.who.includes('team:' + c.team)) || (!c.bot && !c.team && tv.who.includes('nobody')) || (c.bot && tv.who.includes('team:' + teamOf(c))))
      && (!tv.status.length || tv.status.includes(s)) && (!tv.priority.length || tv.priority.includes(c.priority)) && (!tv.label.length || (c.labels || []).some((l) => tv.label.includes(l)))
      && (!tv.q || `${c.title} ${c.desc || ''} ${c.bot || ''} ${c.team || ''} ${(c.labels || []).join(' ')}`.toLowerCase().includes(tv.q.toLowerCase())); };
  const tally = (xs) => { const mm = new Map(); xs.forEach((x) => mm.set(x, (mm.get(x) || 0) + 1)); return mm; };
  const bar = (list) => UI.filterBar({ id: 'tk', state: tv, end: viewCtl, placeholder: 'Find a ticket, or filter: status:draft', label: 'Find and filter tickets', fields: [
    { k: 'who', label: 'Assignee', multi: true, alias: 'assignee', options: () => { const m = tally(list.map((c) => c.bot || (c.team ? 'team:' + c.team : 'nobody')));
      D.teams.forEach((t) => { if (!m.has('team:' + t.name)) m.set('team:' + t.name, 0); });
      return [...m].map(([v, n]) => ({ v, label: v === 'nobody' ? 'Nobody yet' : v.startsWith('team:') ? `team ${v.slice(5)}` : UI.botName(v), n: v.startsWith('team:') ? list.filter((c) => c.team === v.slice(5) || teamOf(c) === v.slice(5)).length : n })).filter((o) => o.n); } },
    { k: 'status', label: 'Status', multi: true, keepOrder: true, options: () => ORDER.map((v) => ({ v, label: STATUS[v][0], n: list.filter((c) => statusOf(c) === v).length })).filter((o) => o.n) },
    { k: 'priority', label: 'Priority', multi: true, keepOrder: true, options: () => PRI.map(([v, label]) => ({ v, label, n: list.filter((c) => c.priority === v).length })).filter((o) => o.n) },
    { k: 'label', label: 'Label', multi: true, more: true, options: () => [...tally(list.flatMap((c) => c.labels || []))].map(([v, n]) => ({ v, label: v, n })) }],
    onChange: () => { tv.page = 0; UI.rerender({ keepScroll: true }); } });
  const byPri = (a, b) => PRI.findIndex(([k]) => k === a.priority) - PRI.findIndex(([k]) => k === b.priority) || Date.parse(b.opened) - Date.parse(a.opened);
  const section = (title, rows, o = {}) => `<section class="dl-card tk-sec"${o.id ? ` id="${o.id}"` : ''}><header class="dl-ch"><h2>${title}</h2>${UI.count(rows.length, { hot: !!o.hot })}${o.sub ? `<span class="dl-sub">${o.sub}</span>` : ''}</header><ul class="dl-list">${rows.sort(byPri).map((c) => UI.ticketRow(c, o)).join('')}</ul></section>`;
  /* The board (60, 60b): one column per status, Draft to Done; each column scrolls inside itself, Done holds the last
     7 days (Show older for the rest); drag a card to move it, within the rules (conductor alone moves a ticket into In
     progress or Waiting on you). On a phone, one column at a time. The list: one flat list, 20 to a page, its status a
     fact. Nothing grows without limit. */
  const WEEK = 7 * 864e5, recent = (c) => Date.now() - Date.parse(c.closedAt || c.updated || c.opened) <= WEEK;
  const card = (c, o) => `<li class="tk-card" data-t="${c.id}" draggable="true"><a href="${o.base || '#/tickets'}/${c.id}" draggable="false"><b>${esc(c.title)}</b><span class="dl-why">${by(c).replace(/^./, (x) => x.toUpperCase())}</span>${UI.factsLine(UI.ticketFacts(c, o))}${UI.ticketAct(c) ? `<span class="tk-card-act">${UI.ticketAct(c)}</span>` : ''}</a></li>`;
  const col = (k, xs, o, extra = '') => `<section class="tk-col${tv.col === k ? ' on' : ''}" data-col="${k}" aria-label="${STATUS[k][0]}"><header class="tk-colh">${UI.mark(STATUS[k][1])}<h2>${STATUS[k][0]}</h2>${UI.count(xs.length, { hot: k === 'waiting' && xs.length > 0 })}</header>
    <ul class="tk-cards">${xs.sort(byPri).map((c) => card(c, o)).join('') || `<li class="tk-none dl-sub">${k === 'draft' ? 'No drafts. Write one, or a bot will when it has an idea.' : 'None'}</li>`}${extra}</ul></section>`;
  const viewCtl = () => `<div class="seg fb-seg tk-view" role="group" aria-label="Show as">${[['board', 'Board'], ['list', 'List']].map(([v, l]) => `<button type="button" data-act="tk-view" data-v="${v}" aria-pressed="${viewOf() === v}" aria-label="${l}">${icon(v === 'list' ? 'logs' : 'grid')}<span class="tk-vl">${l}</span></button>`).join('')}</div>`;
  const PAGE = 20;
  UI.ticketList = (list, o = {}) => { const shown = list.filter(keep); let body = '';
    if (viewOf() === 'board') {
      const cols = ORDER.filter((k) => k !== 'declined'), of = (k) => shown.filter((c) => statusOf(c) === k || (k === 'done' && statusOf(c) === 'declined'));
      if (!tv.col || !cols.includes(tv.col)) tv.col = cols.find((k) => of(k).length) || 'draft';
      const done = of('done'), doneShown = tv.older ? done : done.filter(recent), olderN = done.length - doneShown.length;
      const more = olderN ? `<li class="tk-older"><button type="button" class="btn sm ghost" data-act="tk-older">Show ${olderN} older</button></li>` : tv.older && done.some((c) => !recent(c)) ? `<li class="tk-older"><button type="button" class="btn sm ghost" data-act="tk-older">Only the last 7 days</button></li>` : '';
      const pick = `<div class="tk-colpick" role="tablist" aria-label="Status">${cols.map((k) => `<button type="button" role="tab" data-act="tk-col" data-v="${k}" aria-selected="${tv.col === k}">${STATUS[k][0]} <span class="dl-sub">${(k === 'done' ? doneShown : of(k)).length}</span></button>`).join('')}</div>`;
      body = `${pick}<div class="tk-board" data-base="${esc(o.base || '#/tickets')}">${cols.map((k) => k === 'done' ? col(k, doneShown, o, more) : col(k, of(k), o)).join('')}</div>`;
    } else {
      const all = shown.slice().sort((a, b) => ORDER.indexOf(statusOf(a)) - ORDER.indexOf(statusOf(b)) || byPri(a, b)), pages = Math.max(1, Math.ceil(all.length / PAGE)); tv.page = Math.min(tv.page, pages - 1);
      const from = tv.page * PAGE, xs = all.slice(from, from + PAGE);
      body = all.length ? `<section class="dl-card tk-sec tk-flat"><ul class="dl-list">${xs.map((c) => UI.ticketRow(c, Object.assign({ statusFact: true }, o))).join('')}</ul>${pages > 1 ? `<div class="pk-pages"><span class="dl-sub">${from + 1}–${from + xs.length} of ${all.length}</span><button class="btn sm" ${tv.page ? 'data-act="tk-page" data-d="-1"' : UI.off('The first page')}>${icon('chev', 'flip')}Previous</button><button class="btn sm" ${tv.page < pages - 1 ? 'data-act="tk-page" data-d="1"' : UI.off('The last page')}>Next${icon('chev')}</button></div>` : ''}</section>` : '';
    }
    const filtered = shown.length < list.length;
    return `${bar(list)}${filtered ? `<p class="dl-sub tk-showing">Showing ${shown.length} of ${list.length} · <button class="link" data-act="tk-clear">Clear</button></p>` : ''}${body || `<p class="dl-sub">${filtered ? 'No ticket matches.' : o.none || 'No tickets yet.'}</p>`}`; };
  UI.act('tk-view', (b) => { D.prefs = D.prefs || {}; D.prefs.ticketView = b.dataset.v; S.save(); UI.rerender({ keepScroll: true }); });
  UI.act('tk-page', (b) => { tv.page += +b.dataset.d; UI.rerender(); });
  UI.act('tk-older', () => { tv.older = !tv.older; UI.rerender({ keepScroll: true }); });
  UI.act('tk-col', (b) => { tv.col = b.dataset.v; UI.rerender({ keepScroll: true }); });
  // drag a card to another column: within the rules, or a word on why not
  const move = (id, to) => { const c = T(id); if (!c) return; const from = statusOf(c); if (from === to || (to === 'done' && from === 'declined')) return;
    const note = (t) => { (c.notes = c.notes || []).push({ at: now(), who: 'conductor', text: t }); };
    if (to === 'progress') return UI.toast('conductor moves it there', 'a ticket is in progress once its bot starts on it', 'idle');
    if (to === 'waiting') return UI.toast('conductor moves it there', 'a ticket waits on you when its bot asks you something', 'idle');
    if (to === 'draft') { if (from !== 'ready') return UI.toast('Only a ready ticket goes back to draft', 'work has started on this one', 'idle'); c.stage = 'draft'; note('Moved back to draft by you'); }
    else if (to === 'ready') { if (from === 'draft') return UI.acts['tk-ready']({ dataset: { t: id } }); if (from !== 'done' && from !== 'declined') return UI.toast('It’s already under way', 'conductor moves it back when its work is done', 'idle'); c.status = 'open'; c.declined = false; c.stage = 'ready'; note('Reopened by you'); }
    else if (to === 'done') { c.status = 'closed'; c.declined = false; c.closedAt = now(); note('Marked done by you'); }
    audit('ticket_move', `${c.id} ${from} → ${to}`); S.save(); UI.rerender({ keepScroll: true }); UI.toast(STATUS[to][0], esc(c.title), 'ok'); };
  UI.ticketMove = move;
  document.addEventListener('dragstart', (e) => { const li = e.target.closest && e.target.closest('.tk-card[data-t]'); if (!li) return; e.dataTransfer.setData('text/x-ticket', li.dataset.t); e.dataTransfer.effectAllowed = 'move'; li.classList.add('drag'); });
  document.addEventListener('dragend', (e) => { const li = e.target.closest && e.target.closest('.tk-card'); if (li) li.classList.remove('drag'); document.querySelectorAll('.tk-col.over').forEach((x) => x.classList.remove('over')); });
  document.addEventListener('dragover', (e) => { const col2 = e.target.closest && e.target.closest('.tk-col[data-col]'); if (!col2) return; e.preventDefault(); document.querySelectorAll('.tk-col.over').forEach((x) => x !== col2 && x.classList.remove('over')); col2.classList.add('over'); });
  document.addEventListener('drop', (e) => { const col2 = e.target.closest && e.target.closest('.tk-col[data-col]'); if (!col2) return; e.preventDefault(); col2.classList.remove('over'); const id = e.dataTransfer.getData('text/x-ticket'); if (id) move(id, col2.dataset.col); });
  UI.act('tk-clear', () => { Object.assign(tv, { q: '', who: [], status: [], priority: [], label: [], page: 0 }); UI.rerender({ keepScroll: true }); });

  UI.route('tickets/:id?', ({ id }) => {
    if (id === 'new') return newTicket();
    if (id) return detail(id);
    const list = D.cases, open = list.filter((c) => c.status !== 'closed').length, drafts = list.filter((c) => statusOf(c) === 'draft').length;
    return `<div class="page dl-page tk-page"><div class="dl-phead"><div><h1>Tickets <span class="h1-count">${open} open${drafts ? ` · ${drafts} draft${drafts > 1 ? 's' : ''}` : ''}</span></h1>
      <p class="lede-2">A ticket is one piece of work for a team or a bot. Drafts are ideas you and the bots shape together; work starts only once someone marks a ticket ready.</p></div>
      <div class="dl-actions"><a class="btn primary" href="#/tickets/new">${icon('plus')}New ticket</a></div></div>
      ${UI.ticketList(list)}</div>`;
  }, { nav: 'tickets' });

  /* ---------- a ticket ---------- */
  function detail(id) {
    const c = T(id); if (!c) return `<div class="page"><div class="empty"><span class="serif">No ticket here</span><a class="btn" href="#/tickets">Tickets</a></div></div>`;
    return `<div class="page dl-page tk-page">${UI.crumbs([['Tickets', '#/tickets'], [c.title]])}${UI.ticketBody(id)}</div>`;
  }
  /** A ticket: on its own page under Tickets, or inside a bot's Tickets tab (the breadcrumb says which). */
  UI.ticketBody = (id, o = {}) => { const c = T(id);
    const k = K(), m = k.M(c.bot), s = UI.ticketStatus(c), open = c.status !== 'closed', team = teamOf(c);
    const props = (D.proposals || []).filter((p) => p.case === c.id && p.kind !== 'purchase'), workers = (D.workers || []).filter((w) => w.case === c.id), dels = (D.delegations || []).filter((d) => d.parent === c.id || d.child === c.id);
    const canDel = open && m && UI.delegationReach(m.name).some((r) => r.ok);
    const key = 't:' + c.id, draft = s.k === 'draft';
    const hist = [{ at: c.opened, t: c.stage === 'draft' || /^bot:/.test(c.requester || '') ? `Drafted by ${askedBy(c).replace(' (a bot)', '')}` : `Opened by ${askedBy(c)}` }].concat(c.routed ? [{ at: c.opened, t: ((x) => x.charAt(0).toUpperCase() + x.slice(1))(k.routeLabel(c.routed).replace(/<[^>]+>/g, '')) }] : [])
      .concat(D.audit.filter((a) => new RegExp(`\\b${c.id}\\b`).test(a.detail || '')).map((a) => ({ at: a.ts, t: histWord(a) }))).sort((a, b) => Date.parse(b.at) - Date.parse(a.at));
    const ctl = (k2, label, val, opts) => `<div class="tk-f"><span class="dl-lbl">${label}</span><button class="tk-pick" data-act="tk-set" data-k="${k2}" data-t="${c.id}" aria-label="${label}: ${esc(val)}. Change">${esc(val)}${icon('chevd')}</button></div>`;
    return `<section class="dl-card case-head tk-head"><div class="tk-ht">${UI.mark(s.mark, { lg: true, label: s.word })}<h1 class="case-title dl-h1-long">${esc(c.title)}</h1><button class="icon-btn" data-act="tk-more" data-t="${c.id}" aria-label="More for this ticket">${icon('more')}</button></div>
        <p class="dl-sub case-state">${s.word}${c.urgent && open ? ' · <b class="danger-text">urgent</b>' : ''} · ${draft ? `${by(c)} ${UI.agoText(c.opened)}` : `${open ? 'opened' : 'closed'} ${UI.agoText(c.opened)} by ${esc(askedBy(c))}`}</p>
        ${UI.factsLine(UI.ticketFacts(c))}
        ${draft ? `<div class="tk-ready"><p><b>A draft.</b> ${c.bot || c.team ? `${esc(assignee(c))} can help shape it, but won’t start` : 'Nobody works on it'} until you mark it ready${c.bot || c.team ? '' : ', and it needs someone to do it first'}.</p><button class="btn primary" data-act="tk-ready" data-t="${c.id}">${icon('check')}Mark ready</button></div>` : ''}
        <div class="dl-actions">${(() => { const w = UI.ticketStatus(c).k === 'waiting' && (D.proposals || []).find((p) => p.case === c.id && p.status === 'pending'); return w ? `<a class="btn primary" href="#/bots/${w.bot}/proposals/${w.id}">${icon(w.kind === 'purchase' ? 'dollar' : 'obj-approval')}Review ${w.kind === 'purchase' ? 'the purchase' : 'what it asks'}</a>` : ''; })()}${draft ? '' : `<button class="btn secondary" data-act="case-close" data-m="${esc(c.bot || '')}" data-c="${c.id}">${icon(open ? 'check' : 'undo')}${open ? 'Mark done' : 'Reopen'}</button>`}
          ${open ? `<button class="btn secondary" data-act="tk-decline" data-t="${c.id}">${icon('x')}Decline</button>` : ''}
          ${m && !draft ? `<button class="btn secondary" ${canDel ? `data-act="case-delegate" data-m="${m.name}" data-c="${c.id}"` : UI.off(!open ? 'Reopen the ticket to hand off part of it' : `${m.name} has no teammate or linked bot to hand off to`)}>${icon('send')}Hand off part of it</button>` : ''}
</div>
        <p class="dl-sub case-route">${c.routed ? k.routeLabel(c.routed) + '.' : c.bot ? 'Assigned to it directly.' : c.team ? `Assigned to team ${esc(c.team)}: its lead or routing picks a bot.` : 'Not assigned yet: nobody works on it until it is.'}</p>
        <details class="dl-audit-d"><summary>${icon('chev')}For audit: the ticket id and who asked, as recorded</summary><dl class="dl-facts"><dt>Ticket</dt><dd class="mono">${c.id}</dd><dt>Asked by</dt><dd><span class="mono">${esc(c.requester)}</span> (${esc(c.cls)}); carried through every hop, so whoever works it runs with no more trust than that</dd></dl></details></section>
      <div class="grid g-side tk-grid">
        <div class="stack">
          <section class="dl-card tk-desc"><header class="dl-ch"><h2>Description</h2><span class="dl-sp"></span><button class="btn sm ghost" data-act="tk-edit" data-t="${c.id}">${icon('edit')}Edit</button></header>${c.desc ? `<p class="tk-body">${UI.md(c.desc)}</p>` : '<p class="dl-sub">No description.</p>'}</section>
          <section class="panel chat tk-chat"><div class="panel-head"><h3>Comments</h3><span class="dl-sub">${c.bot ? `${UI.nm(c.bot)} reads them and answers here` : 'The bot that takes it reads them'}</span></div>
            ${UI.chatThread(UI.ticketRows(c.id), c.bot || 'conductor', { ticket: c.id, empty: 'No comments yet.' })}${UI.chatComposer(key, { ticket: c.id })}<div class="cm-drop" aria-hidden="true">${icon('attach')}Drop to attach to your comment</div></section>
          ${dels.length ? `<div class="panel"><div class="panel-head"><h3>Hand-offs</h3><span class="muted small">parts handed to teammates; results report back here</span></div><div class="list">${dels.map((d) => (d.child === c.id ? k.edgeHTML(d).replace('class="item deleg"', 'class="item deleg inbound"') : k.edgeHTML(d))).join('')}</div></div>` : ''}
        </div>
        <div class="stack tk-side">
          <section class="panel"><div class="panel-head"><h3>Details</h3></div><div class="tk-fields">
            <div class="tk-f"><span class="dl-lbl">Status</span><span class="tk-st">${UI.mark(s.mark)} ${s.word}</span></div>${ctl('who', 'Assigned to', assignee(c))}${c.bot && team ? `<div class="tk-f"><span class="dl-lbl">Team</span><a class="dl-link" href="#/teams/${team}">${esc(team)}</a></div>` : ''}
            ${ctl('priority', 'Priority', priWord(c.priority))}${ctl('due', 'Due', c.due ? `${c.due} · ${dueWord(c.due)}` : 'No date')}${ctl('labels', 'Labels', (c.labels || []).join(', ') || 'None')}</div></section>
          ${props.length || workers.length ? `<section class="panel"><div class="panel-head"><h3>Linked work</h3></div><ul class="cm-side-l">${props.map((p) => `<li><a class="cm-side-a" href="#/bots/${p.bot}/proposals/${p.id}"><b>${UI.mark(p.status === 'pending' ? 'ember' : p.status === 'executed' || p.status === 'approved' ? 'ok' : 'idle')} ${esc(p.title || p.summary)}</b>${UI.factsLine([UI.envFact(p.env), UI.actionFact(p.verb, p.env, p.tier)])}<span class="dl-sub">${p.status === 'pending' ? 'Waiting for you' : esc(p.status)}</span></a></li>`).join('')}
            ${workers.map((w) => `<li><a class="cm-side-a" href="${w.run ? `#/runs/${w.run}` : `#/bots/${w.bot || c.bot}/workers`}"><b>${UI.mark(w.status === 'run' ? 'run' : w.status === 'ok' ? 'ok' : 'idle')} ${esc(w.brief || 'A worker')}</b><span class="dl-sub">${w.readOnly ? 'Read-only worker' : 'Worker'} · ${w.status === 'run' ? 'running' : 'finished'}</span></a></li>`).join('')}</ul></section>` : ''}
          ${UI.ticketBudget ? UI.ticketBudget(c) : ''}${UI.ticketPurchases ? UI.ticketPurchases(c) : ''}
          <section class="panel"><div class="panel-head"><h3>History</h3></div><ol class="tk-hist">${hist.map((h) => `<li><span>${esc(h.t)}</span><span class="dl-sub">${UI.agoText(h.at)}</span></li>`).join('')}</ol></section>
        </div></div>`;
  };
  const histWord = (a) => ({ browser_case_budget: 'Spending limit changed', browser_purchase_refused: 'A purchase was refused', browser_profile_fill: 'Your details were filled in', browser_purchase_approve: 'You approved a purchase', browser_purchase_deny: 'You denied a purchase', ticket_comment: 'You commented', ticket_assign: 'Assigned', ticket_edit: 'Edited', ticket_ready: 'Marked ready', ticket_decline: 'Declined', ticket_new: 'Created', bot_case_close: 'Marked done', bot_case_reopen: 'Reopened', bot_case_note: 'You commented', bot_route: 'Reassigned', bot_delegate: 'Part handed off', bot_delegate_done: 'A hand-off reported back' }[a.event] || a.event.replace(/_/g, ' '));
  UI.afterRender.push((main) => { const t = main.querySelector('.tk-chat #msgs'); if (t) t.scrollTop = t.scrollHeight; });

  /* ---------- changing a ticket ---------- */
  const whoOptions = () => [['', 'Nobody yet']].concat(D.teams.map((t) => ['team:' + t.name, `team ${t.name}: its lead or routing picks a bot`]), D.bots.map((b) => [b.name, `${UI.botName(b.name)}${b.cfg && b.cfg.team ? ' · team ' + b.cfg.team : ' · no team'}`]));
  const assign = (c, v) => { const was = assignee(c); if (!v) { delete c.bot; delete c.team; } else if (v.startsWith('team:')) { c.team = v.slice(5); delete c.bot; } else { c.bot = v; c.team = ((K().teamOf(K().M(v)) || {}).name) || undefined; if (!c.team) delete c.team; }
    c.routed = Object.assign({}, c.routed || {}, { by: 'operator', from: was, to: assignee(c).replace('Nobody yet', 'nobody'), team: c.team, requester_kept: true });
    (c.notes = c.notes || []).push({ at: now(), who: 'conductor', text: `Assigned to ${assignee(c)} by you` }); audit('ticket_assign', `${c.id} → ${assignee(c)}`); };
  UI.act('tk-set', (b) => { const c = T(b.dataset.t), k = b.dataset.k;
    const F = { who: { k: 'who', type: 'enum', label: 'Assign to', options: whoOptions(), def: c.bot || (c.team ? 'team:' + c.team : '') },
      priority: { k: 'priority', type: 'enum', label: 'Priority', options: PRI, def: c.priority },
      due: { k: 'due', type: 'string', label: 'Due date (YYYY-MM-DD, empty for none)', mono: false, ph: '2026-10-25', def: c.due || '', validate: (x) => (!x || /^\d{4}-\d\d-\d\d$/.test(x) ? '' : 'A date like 2026-10-25') },
      labels: { k: 'labels', type: 'list', label: 'Labels', def: c.labels || [], help: `In use: ${allLabels().join(', ')}` } }[k];
    UI.form({ caps: 'ticket', title: { who: 'Assign the ticket', priority: 'Priority', due: 'Due date', labels: 'Labels' }[k], values: { [k]: F.def }, fields: [F], submit: 'Save',
      onSubmit: (v) => { if (k === 'who') assign(c, v.who); else { if (k === 'due' && !v.due) delete c.due; else c[k] = v[k]; audit('ticket_edit', `${c.id} · ${k}`); }
        S.save(); UI.closeModal(); UI.rerender({ keepScroll: true }); UI.refreshChrome(); } }); });
  UI.act('tk-edit', (b) => { const c = T(b.dataset.t); UI.form({ caps: 'ticket', title: 'Edit the ticket', values: { title: c.title, desc: c.desc || '' }, fields: [{ k: 'title', type: 'string', req: true, mono: false }, { k: 'desc', type: 'text', label: 'Description' }], submit: 'Save',
    onSubmit: (v) => { c.title = v.title; c.desc = v.desc; audit('ticket_edit', `${c.id} · text`); S.save(); UI.closeModal(); UI.rerender({ keepScroll: true }); } }); });
  UI.act('tk-more', (b) => { const c = T(b.dataset.t); UI.menu(b, [['Assign…', 'users', 'tk-set', `data-k="who" data-t="${c.id}"`], ['Edit…', 'edit', 'tk-edit', `data-t="${c.id}"`], ...(statusOf(c) === 'ready' ? [['Back to draft', 'undo', 'tk-draft', `data-t="${c.id}"`]] : []), ['-'], ['Copy a link to it', 'copy', 'tk-copy', `data-t="${c.id}"`]]); });
  UI.act('tk-copy', (b) => { try { navigator.clipboard.writeText(location.origin + location.pathname + '#/tickets/' + b.dataset.t).catch(() => {}); } catch (e) {} UI.toast('Link copied', esc(T(b.dataset.t).title), 'ok'); });
  // Ready: an operator's explicit act, and only with someone to do it; from then the assignee may start
  UI.act('tk-ready', (b) => { const c = T(b.dataset.t); const go = () => { c.stage = 'ready'; (c.notes = c.notes || []).push({ at: now(), who: 'conductor', text: `Marked ready by you: ${assignee(c)} may start` }); audit('ticket_ready', `${c.id} → ${assignee(c)}`); S.save(); UI.rerender({ keepScroll: true }); UI.toast('Ready', esc(`${assignee(c)} may start on it`), 'ok'); };
    if (c.bot || c.team) return go();
    UI.form({ caps: 'ticket', title: 'Who should do it?', intro: 'A ready ticket needs someone to do it: a bot, or a team whose lead or routing picks one.', fields: [{ k: 'who', type: 'enum', req: true, label: 'Assign to', options: whoOptions().filter(([v]) => v), def: '' }], submit: 'Assign and mark ready',
      onSubmit: (v) => { if (!v.who) return 'Pick a bot or a team.'; assign(c, v.who); UI.closeModal(); go(); } }); });
  UI.act('tk-draft', (b) => { const c = T(b.dataset.t); c.stage = 'draft'; c.notes.push({ at: now(), who: 'conductor', text: 'Moved back to draft by you: work pauses until it’s ready again' }); audit('ticket_edit', `${c.id} · back to draft`); S.save(); UI.rerender({ keepScroll: true }); });
  UI.act('tk-decline', (b) => { const c = T(b.dataset.t); UI.confirm({ title: 'Decline this ticket?', body: `“${esc(c.title)}” closes without being done. You can reopen it.`, ok: 'Decline', danger: true }).then((ok) => { if (!ok) return;
    c.status = 'closed'; c.declined = true; (c.notes = c.notes || []).push({ at: now(), who: 'conductor', text: 'Declined by you' }); audit('ticket_decline', c.id); S.save(); UI.rerender({ keepScroll: true }); }); });

  /* ---------- New ticket ---------- */
  const nv = { files: [], err: '' };
  function newTicket() {
    const q = new URLSearchParams((location.hash.split('?')[1]) || ''), bot = q.get('bot'), team = q.get('team'), from = q.get('from');
    const msg = bot && from != null ? (D.chats[bot] || [])[+from] : null, text = msg ? String(msg.text || '') : '';
    const title = text ? (text.split(/(?<=[.?!])\s/)[0] || text).replace(/\*\*|`/g, '').slice(0, 90) : '';
    const def = bot ? bot : team ? 'team:' + team : '';
    const opts = whoOptions().map(([v, l]) => `<option value="${esc(v)}"${v === def ? ' selected' : ''}>${esc(l)}</option>`).join('');
    return `<div class="page dl-page tk-page tk-new">${UI.crumbs([['Tickets', '#/tickets'], ['New ticket']])}
      <div class="dl-phead"><div><h1>New ticket</h1>${msg ? `<p class="lede-2">From a message in ${esc(bot)}’s chat. It keeps a link back to the conversation.</p>` : ''}</div></div>
      <form class="dl-card tk-form" novalidate>
        <label class="tk-l"><span class="dl-lbl">Title</span><input class="input" id="tk-title" required maxlength="140" value="${esc(title)}" placeholder="What needs doing, in a line"></label>
        <label class="tk-l"><span class="dl-lbl">Description</span><textarea class="textarea" id="tk-desc" rows="5" placeholder="What you know, what done looks like, links">${esc(text)}</textarea></label>
        <div class="tk-l"><span class="dl-lbl">Attachments</span><div class="tk-att"><button type="button" class="btn sm secondary" data-act="tk-attach">${icon('attach')}Attach files or images</button><input type="file" id="tk-file" multiple hidden accept="image/png,image/jpeg,image/gif,image/webp,application/pdf,text/*,.log,.csv,.json,.yaml,.yml,.md">
          <span class="dl-sub">Up to ${UI.chatLimits.files} files, ${UI.bytes(UI.chatLimits.bytes)} each: images, PDF and text</span></div><div class="cm-pend" id="tk-pend"${nv.files.length ? '' : ' hidden'}>${nv.files.map(fileChip).join('')}</div><p class="cm-err" id="tk-err"${nv.err ? '' : ' hidden'}>${esc(nv.err)}</p></div>
        <div class="tk-row2"><label class="tk-l"><span class="dl-lbl">Assign to</span><select class="select" id="tk-who">${opts}</select><span class="dl-sub">A team’s lead or routing picks the bot.</span></label>
          <label class="tk-l"><span class="dl-lbl">Priority</span><select class="select" id="tk-pri">${PRI.map(([v, l]) => `<option value="${v}"${v === 'normal' ? ' selected' : ''}>${l}</option>`).join('')}</select></label></div>
        <div class="tk-row2"><label class="tk-l"><span class="dl-lbl">Due</span><input class="input" type="date" id="tk-due"></label>
          <label class="tk-l"><span class="dl-lbl">Labels</span><input class="input" id="tk-labels" placeholder="bug, web" list="tk-label-list"><datalist id="tk-label-list">${allLabels().map((l) => `<option value="${esc(l)}">`).join('')}</datalist></label></div>
        ${UI.ticketBudgetFields ? UI.ticketBudgetFields() : ''}
        <input type="hidden" id="tk-from" value="${msg ? esc(bot + ':' + from) : ''}">
        <div class="dl-actions"><button class="btn primary" type="button" data-act="tk-create" data-stage="ready">${icon('check')}Create, ready to start</button><button class="btn secondary" type="button" data-act="tk-create" data-stage="draft">${icon('edit')}Save as a draft</button><a class="btn ghost" href="${bot ? `#/bots/${bot}/chat` : '#/tickets'}">Cancel</a></div></form></div>`;
  }
  const fileChip = (a, i) => `<span class="cm-p"><span>${icon(a.kind === 'image' ? 'image' : 'file')}</span><span class="cm-pt"><b>${esc(a.name)}</b><span class="dl-sub">${UI.bytes(a.size)}</span></span><button type="button" class="icon-btn sm" data-act="tk-unattach" data-i="${i}" aria-label="Remove ${esc(a.name)}">${icon('x')}</button></span>`;
  const drawFiles = () => { const el = UI.$('#tk-pend'), er = UI.$('#tk-err'); if (el) { el.innerHTML = nv.files.map(fileChip).join(''); el.hidden = !nv.files.length; } if (er) { er.textContent = nv.err; er.hidden = !nv.err; } };
  UI.act('tk-attach', () => UI.$('#tk-file').click());
  UI.act('tk-unattach', (b) => { nv.files.splice(+b.dataset.i, 1); nv.err = ''; drawFiles(); });
  document.addEventListener('change', (e) => { if (e.target.id !== 'tk-file') return; const L = UI.chatLimits, errs = [];
    [...e.target.files].forEach((f) => { const img = /^image\/(png|jpeg|gif|webp)$/.test(f.type), ok = img || L.types[1][1].test(f.type || '') || L.exts.test(f.name);
      if (!ok) errs.push(`${f.name} wasn’t attached: only images, PDF and text files can be attached`); else if (f.size > L.bytes) errs.push(`${f.name} wasn’t attached: it’s ${UI.bytes(f.size)}, and the limit is ${UI.bytes(L.bytes)} each`); else if (nv.files.length >= L.files) errs.push(`${f.name} wasn’t attached: up to ${L.files} files`); else nv.files.push({ kind: img ? 'image' : 'file', name: f.name, mime: f.type || 'text/plain', size: f.size, id: 'att_' + Math.random().toString(16).slice(2, 6) }); });
    nv.err = errs.join('. '); e.target.value = ''; drawFiles(); });
  UI.act('tk-create', (btn) => { const stage = btn.dataset.stage || 'ready', ti = UI.$('#tk-title'), title = ti.value.trim(); if (!title) return UI.nudge(ti, 'Give it a title');
    const who = UI.$('#tk-who').value; if (stage === 'ready' && !who) return UI.nudge(UI.$('#tk-who'), 'Who should do it?'), UI.toast('A ready ticket needs someone to do it', 'Pick a bot or a team, or save it as a draft', 'wait');
    const id = 'c' + (40 + D.cases.length), from = UI.$('#tk-from').value;
    const c = { id, title, desc: UI.$('#tk-desc').value.trim(), status: 'open', requester: 'operator:ops', cls: 'operator', opened: now(), workers: 0, proposals: 0, stage, priority: UI.$('#tk-pri').value || 'normal',
      labels: UI.$('#tk-labels').value.split(',').map((x) => x.trim()).filter(Boolean), notes: [] };
    if (UI.$('#tk-due').value) c.due = UI.$('#tk-due').value;
    const sl = UI.readTicketBudget ? UI.readTicketBudget(btn.closest('form')) : undefined; if (sl === null) return; if (sl) c.spend_limit = sl;
    const create = () => {
    if (nv.files.length) c.notes.push({ at: now(), who: 'operator:ops', text: '', state: 'delivered', attachments: nv.files.slice() });
    if (from) c.notes.unshift({ at: now(), who: 'conductor', text: `Made from a message in ${from.split(':')[0]}’s chat` });
    D.cases.unshift(c); if (who) assign(c, who); c.notes = c.notes.filter((n) => !/^Assigned to/.test(n.text) || who); if (!who) delete c.routed; else c.routed = who.startsWith('team:') ? { by: 'direct', team: who.slice(5), to: `team ${who.slice(5)}`, requester_kept: true } : { by: 'direct', to: who, requester_kept: true };
    audit('ticket_new', `${id} · ${title.slice(0, 60)} · ${assignee(c)}`); nv.files = []; nv.err = ''; S.save(); UI.refreshChrome(); UI.go(`#/tickets/${id}`); UI.toast(stage === 'draft' ? 'Draft saved' : 'Ticket created', esc(stage === 'draft' ? 'Nobody starts on it until you mark it ready' : `${assignee(c)} may start on it`), 'ok'); };
    // a new spending limit is a widening (the backend's rule), so it asks for the passkey first
    if (sl) UI.ticketBudgetGate(title, sl, create, stage); else create(); });
  document.addEventListener('submit', (e) => { if (e.target.matches && e.target.matches('.tk-form')) { e.preventDefault(); UI.$('[data-act="tk-create"][data-stage="ready"]').click(); } });

  /* ---------- a bot's tickets and a team's tickets: the same rows, narrowed ---------- */
  UI.botTickets = (m) => { const list = D.cases.filter((c) => c.bot === m.name);
    return `<section class="tk-in"><header class="dl-ch tk-inh"><p class="dl-sub">Tickets assigned to ${UI.nm(m.name)}: from you, a chat, an event or its own checks. It keeps its notes on each and the parts it hands to teammates.</p><span class="dl-sp"></span><a class="btn sm secondary" href="#/tickets/new?bot=${encodeURIComponent(m.name)}">${icon('plus')}New ticket for it</a></header>
      ${UI.ticketList(list, { noAssignee: true, base: `#/bots/${m.name}/tickets`, none: `Nothing assigned to ${esc(m.name)}.` })}</section>`; };
  UI.teamTickets = (t) => { const list = D.cases.filter((c) => c.team === t.name || teamOf(c) === t.name);
    return `<section class="tk-in"><header class="dl-ch tk-inh"><p class="dl-sub">Tickets for team ${esc(t.name)} and its bots. One for the team goes to its lead or routing, which picks a bot.</p><span class="dl-sp"></span><a class="btn sm secondary" href="#/tickets/new?team=${encodeURIComponent(t.name)}">${icon('plus')}New ticket for the team</a></header>
      ${UI.ticketList(list, { none: `No tickets for ${esc(t.name)}.` })}</section>`; };
})();
