/* Chat (items 20, 25: direction 1, accent A): a main destination, like a messaging app. On the left, your
 * conversations: each bot as a person (its face, its name, its last message, unread, whether it's working now) and
 * each team as a group (its members' faces); pinned first, then teams, then the rest by recency, with a search that
 * finds any bot. On the right, the conversation: the bot chat of item 19 (the same thread and composer as a bot's Chat
 * tab, which is the same conversation). In a team's chat you talk to the whole team: its lead (or its routing) picks
 * it up; "Everyone" asks every bot on it; @name addresses one bot. Every reply says which bot it is.
 * Routes: #/chat (the list; on a desktop, with the first conversation open), #/chat/bot/<name>, #/chat/team/<name>.
 * On a phone the list and the conversation are two screens, like Messages. */
(function () {
  const esc = UI.esc, icon = UI.icon;
  const now = () => new Date().toISOString();
  const phone = () => innerWidth <= 720;
  const M = (n) => D.bots.find((b) => b.name === n);
  const teamBots = (t) => D.bots.filter((b) => b.cfg && b.cfg.team === t).map((b) => b.name);
  const leadOf = (t) => { const x = D.teams.find((y) => y.name === t); return (x && ((S.staged('teams', t) || x.cfg || {}).routing || {}).lead) || teamBots(t)[0]; };
  const working = (b) => (D.workers || []).some((w) => (w.bot || 'platform-sre') === b && w.status === 'run');
  const stack = (t) => `<span class="cv-stack">${teamBots(t).slice(0, 3).map((b) => UI.botAv(b, 'xs')).join('')}</span>`;
  const lastOf = (L) => [...(L || [])].reverse().find((c) => c.who === 'bot' || c.who === 'op');
  const plain = (t) => String(t || '').replace(/\*\*|`/g, '');
  /** Every conversation, newest first: [kind, key, title, last line, when, unread, working]. */
  const convs = () => {
    const out = D.bots.map((b) => { const L = D.chats[b.name] || [], l = lastOf(L);
      return { kind: 'bot', key: b.name, href: `#/chat/bot/${b.name}`, title: UI.botName(b.name), sub: b.name, last: l ? `${l.who === 'op' ? 'You: ' : ''}${plain(l.text)}` : UI.ident(b.name) ? UI.ident(b.name).tagline : 'No messages yet', at: l ? l.at : null, unread: D.chatUnread[b.name] || 0, working: working(b.name) }; });
    D.teams.forEach((t) => { const L = D.teamChats[t.name] || [], l = lastOf(L);
      out.push({ kind: 'team', key: 'team:' + t.name, href: `#/chat/team/${t.name}`, title: `team ${t.name}`, last: l ? `${l.who === 'op' ? 'You' : UI.botName(l.bot)}: ${plain(l.text)}` : `${teamBots(t.name).length} bots`, at: l ? l.at : null, unread: D.chatUnread['team:' + t.name] || 0, working: teamBots(t.name).some(working) }); });
    return out; };
  const cv = { q: '' };
  const row = (c, on) => `<li><a class="cv-c${on ? ' on' : ''}" href="${c.href}"${on ? ' aria-current="page"' : ''}>${c.kind === 'team' ? stack(c.key.slice(5)) : UI.botAv(c.key, 'md')}
    <span class="cv-ct"><b>${esc(c.title)}</b>${c.working ? `<span class="cv-work" title="Working now">${UI.mark('run')}</span>` : ''}<span class="dl-sub cv-at">${c.at ? UI.agoText(c.at) : ''}</span><span class="cv-last">${esc(c.last)}</span></span>
    ${c.unread ? `<span class="cv-un" aria-label="${c.unread} unread">${c.unread}</span>` : ''}</a></li>`;
  function list(cur) {
    const all = convs(), q = cv.q.toLowerCase(), hit = (c) => !q || `${c.title} ${c.sub || ''} ${c.last}`.toLowerCase().includes(q);
    const pinned = all.filter((c) => D.chatPinned.includes(c.key) && hit(c)), teams = all.filter((c) => c.kind === 'team' && !D.chatPinned.includes(c.key) && hit(c));
    const recent = all.filter((c) => c.kind === 'bot' && !D.chatPinned.includes(c.key) && hit(c)).sort((a, b) => (Date.parse(b.at || 0) || 0) - (Date.parse(a.at || 0) || 0));
    const grp = (h, xs) => (xs.length ? `<h2 class="cv-g">${h}</h2><ul class="cv-l">${xs.map((c) => row(c, c.key === cur)).join('')}</ul>` : '');
    return `<aside class="cv-list" aria-label="Conversations"><div class="cv-lh"><h1>Chat</h1></div>
      <label class="dl-search cv-s">${icon('search')}<input id="cv-q" type="search" placeholder="Find a bot or a team" value="${esc(cv.q)}" aria-label="Find a bot or a team" data-on-input="cv-q"></label>
      ${grp('Pinned', pinned)}${grp('Teams', teams)}${grp(q ? 'Bots' : 'Bots', recent)}${pinned.length + teams.length + recent.length ? '' : '<p class="dl-sub cv-none">No bot or team by that name.</p>'}</aside>`;
  }
  const curKey = () => { const mt = location.hash.match(/#\/chat\/(bot|team)\/([^/?]+)/); return mt ? (mt[1] === 'team' ? 'team:' : '') + decodeURIComponent(mt[2]) : ''; };
  UI.act('cv-q', (el) => { cv.q = el.value; const box = UI.$('.cv-list'); if (!box) return; const pos = el.selectionStart; box.outerHTML = list(curKey()); const q = UI.$('#cv-q'); q.focus(); q.setSelectionRange(pos, pos); });

  /* ---------- a conversation ---------- */
  const to = {}; // per team: 'lead' or 'all'
  function thread(kind, name) {
    if (kind === 'team') { const t = D.teams.find((x) => x.name === name); if (!t) return '<section class="cv-main"><p class="dl-sub">No team by that name.</p></section>';
      const ms = teamBots(name), lead = leadOf(name), mode = to[name] || 'lead';
      return `<section class="cv-main" data-team="${esc(name)}"><header class="cv-th">${stack(name)}<div class="cv-tt"><b>team ${esc(name)}</b><span class="dl-sub">${ms.length} bot${ms.length === 1 ? '' : 's'} · ${esc(UI.botName(lead))} leads and picks up what you ask</span></div><a class="btn sm secondary" href="#/teams/${esc(name)}">Team</a></header>
        <div class="panel chat cv-chat">${UI.chatThread(D.teamChats[name] || [], lead, { empty: `Say hello to team ${esc(name)}. ${esc(UI.botName(lead))} answers first.` })}
        <div class="cv-to" role="group" aria-label="Who it goes to"><span class="dl-sub">To</span><button type="button" class="cv-chip${mode === 'lead' ? ' on' : ''}" data-act="cv-to" data-t="${esc(name)}" data-v="lead" aria-pressed="${mode === 'lead'}">${UI.botAv(lead, 'xs')}${esc(UI.botName(lead))}, the lead</button><button type="button" class="cv-chip${mode === 'all' ? ' on' : ''}" data-act="cv-to" data-t="${esc(name)}" data-v="all" aria-pressed="${mode === 'all'}">Everyone on the team</button><span class="dl-sub cv-at-hint">or @name one bot</span></div>
        ${UI.chatComposer('team:' + name, { to: `team ${name}` })}<div class="cm-drop" aria-hidden="true">${icon('attach')}Drop to attach to your message</div></div></section>`; }
    const m = M(name); if (!m) return '<section class="cv-main"><p class="dl-sub">No bot by that name.</p></section>';
    const id = UI.ident(name) || {}, open = D.cases.filter((c) => c.bot === name && c.status === 'open')[0];
    return `<section class="cv-main"><header class="cv-th">${UI.botAv(name, 'lg')}<div class="cv-tt"><span>${UI.botWho(name)}</span><span class="dl-sub">${esc(id.tagline || m.desc || '')}${working(name) && open ? ` · working on “${esc(open.title)}”` : ''}</span></div><a class="btn sm secondary" href="#/bots/${esc(name)}">Bot page</a></header>
      <div class="panel chat cv-chat">${UI.chatThread(D.chats[name] || [], name, { empty: `No conversation yet. Ask ${esc(UI.botName(name))} anything; it answers here.` })}${UI.chatComposer(name)}<div class="cm-drop" aria-hidden="true">${icon('attach')}Drop to attach to your message</div></div></section>`;
  }
  UI.act('cv-to', (b) => { to[b.dataset.t] = b.dataset.v; b.parentElement.querySelectorAll('.cv-chip').forEach((x) => { const on = x === b; x.classList.toggle('on', on); x.setAttribute('aria-pressed', on); }); });

  /** You, in a team's chat: the lead answers, or everyone does, or the bot you @mention. */
  UI.teamSay = (team, v, done) => { const L = (D.teamChats[team] = D.teamChats[team] || []), ms = teamBots(team), lead = leadOf(team);
    const named = ms.filter((b) => new RegExp(`@(${b}|${UI.botName(b)})\\b`, 'i').test(v.text));
    const who = named.length ? named : to[team] === 'all' ? ms : [lead];
    const c = { who: 'op', at: now(), text: v.text, state: 'queued', toWord: who.length > 1 ? 'the team' : UI.botName(who[0]) };
    if (v.attachments.length) c.attachments = v.attachments.map(({ id, kind, name, mime, size, thumb, preview }) => ({ id, kind, name, mime, size, src: thumb, preview }));
    if (v.links.length) c.links = v.links.map(({ url, host, path }) => ({ url, host, path }));
    L.push(c); S.save(); done(); UI.chatRedraw('team:' + team, '');
    setTimeout(() => { c.state = 'delivered'; S.save(); UI.chatRedraw('team:' + team, ''); }, UI.fast ? 60 : 1400);
    who.forEach((b, i) => setTimeout(() => { L.push({ who: 'bot', bot: b, at: now(), text: who.length > 1 ? (b === lead ? 'On it. I’ll split this up and report back here.' : `${UI.botName(b)} here: I’ll check my side and add what I find.`) : b === lead ? 'I’ll take this. If it needs a teammate, I’ll hand them the part and tell you who.' : 'Got it, I’ll look into that and report back here.' }); S.save(); UI.chatRedraw('team:' + team, ''); }, (UI.fast ? 140 : 2600) + i * (UI.fast ? 40 : 900)));
  };

  /* ---------- the page ---------- */
  function page(kind, name) {
    const all = convs(), first = all.find((c) => D.chatPinned.includes(c.key)) || all[0];
    if (!kind && !phone() && first) { kind = first.kind; name = first.kind === 'team' ? first.key.slice(5) : first.key; }
    const key = kind === 'team' ? 'team:' + name : name;
    if (key && D.chatUnread[key]) { delete D.chatUnread[key]; S.save(); setTimeout(() => UI.refreshChrome && UI.refreshChrome()); }
    if (phone()) return kind ? `<div class="page dl-page cv-page cv-one">${UI.crumbs([['Chat', '#/chat'], [kind === 'team' ? `team ${name}` : UI.botName(name)]])}${thread(kind, name)}</div>`
      : `<div class="page dl-page cv-page cv-one">${list('')}</div>`;
    return `<div class="page dl-page cv-page"><div class="cv-app">${list(key)}${kind ? thread(kind, name) : '<section class="cv-main"><p class="dl-sub">No conversations yet.</p></section>'}</div></div>`;
  }
  UI.route('chat', () => page(), { nav: 'chat' });
  UI.route('chat/:kind/:name', ({ kind, name }) => page(kind, name), { nav: 'chat' });
  UI.afterRender.push((main) => { if (main.querySelector('.cv-chat #msgs')) UI.chatAnchor(); });
  /** The unread count across conversations (the nav's quiet count). */
  UI.chatUnreadCount = () => Object.values(D.chatUnread || {}).reduce((a, n) => a + n, 0);
})();
