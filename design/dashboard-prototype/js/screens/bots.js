/* Teams & bots. Teams group bots: team defaults are a ceiling
 * bots inherit and can only narrow, bots in a team are peers, teams are
 * isolated unless linked, work routes to a team by rule or through its lead.
 * Per bot: chat, cases (with routing), proposals, memory, workers, access,
 * definition. Config edits stage into the pipeline; ledger ops are immediate. */
(function () {
  const { icon, st, tag, tier, usd, ago, esc, md } = UI;
  const M = (n) => D.bots.find((m) => m.name === n);
  const T = (n) => D.teams.find((t) => t.name === n);
  const props = (n) => D.proposals.filter((p) => p.bot === n && UI.live(p));
  const pending = (n) => props(n).filter((p) => p.status === 'pending');
  const teamOf = (m) => (m && m.cfg && m.cfg.team ? T(m.cfg.team) : null);
  const botsOf = (t) => D.bots.filter((m) => m.cfg && m.cfg.team === t);
  const staged = (coll, k) => S.staged(coll, k);
  const now = () => new Date().toISOString();
  const audit = (event, detail) => D.audit.unshift({ ts: now(), event, who: 'operator:ops', detail, run: '' });

  /* ---------- the peers graph, with team boundaries ---------- */
  function peersGraph() {
    const teams = D.teams.map((t) => ({ t, ms: botsOf(t.name) })), loose = D.bots.filter((m) => !teamOf(m));
    const W = 760, pad = 16, gap = 26; let x = pad;
    const total = teams.reduce((a, g) => a + Math.max(1, g.ms.length), 0) + (loose.length ? loose.length * 0.8 : 0);
    const unit = (W - pad * 2 - gap * (teams.length + (loose.length ? 1 : 0) - 1)) / Math.max(total, 1);
    const pos = {}, boxes = [];
    teams.forEach((g) => { const w = Math.max(1, g.ms.length) * unit; boxes.push({ g, x, w }); g.ms.forEach((m, i) => (pos[m.name] = { x: x + (w / (g.ms.length + 1)) * (i + 1), y: 92 })); x += w + gap; });
    loose.forEach((m, i) => (pos[m.name] = { x: x + unit * 0.4 + i * unit * 0.8, y: 92 }));
    const H = 168;
    const edges = [];
    teams.forEach((g) => g.ms.forEach((a, i) => g.ms.slice(i + 1).forEach((b) => edges.push(`<path d="M${pos[a.name].x} ${pos[a.name].y} Q ${(pos[a.name].x + pos[b.name].x) / 2} ${pos[a.name].y + 40} ${pos[b.name].x} ${pos[b.name].y}" class="pg-in"/>`))));
    const linked = new Set();

    const bx = (n) => boxes.find((b) => b.g.t.name === n);
    [...linked].forEach((k) => { const [a, b] = k.split('|').map(bx); if (!a || !b) return; const [l, r] = a.x < b.x ? [a, b] : [b, a]; const x1 = l.x + l.w, x2 = r.x; edges.push(`<path d="M${x1} 112 L ${x2} 112" class="pg-link"/><text x="${(x1 + x2) / 2}" y="104" class="pg-lt" text-anchor="middle">linked</text>`); });
    D.bots.forEach((m) => ((m.cfg && m.cfg.peers) || []).forEach((p) => { const o = M(p); if (!o || !pos[p] || m.name > p) return; const tm = teamOf(m), to = teamOf(o); if (tm && to && tm.name === to.name) return; edges.push(`<path d="M${pos[m.name].x} ${pos[m.name].y} C ${pos[m.name].x} 20, ${pos[p].x} 20, ${pos[p].x} ${pos[p].y}" class="pg-peer"/>`); }));
    return `<svg class="peers-graph" viewBox="0 0 ${W} ${H}" role="img" aria-label="Bots grouped by team, with peer links">
      ${boxes.map((b) => `<rect x="${b.x}" y="50" width="${b.w}" height="98" rx="14" class="pg-team"/><a href="#/teams/${b.g.t.name}" aria-label="Team ${esc(b.g.t.name)}"><text x="${b.x + 12}" y="68" class="pg-tn">${esc(b.g.t.name)} ›</text></a>`).join('')}
      ${loose.length ? `<text x="${pos[loose[0].name].x - 20}" y="68" class="pg-tn muted">no team</text>` : ''}
      ${edges.join('')}
      ${D.bots.map((m) => { const p = pos[m.name]; const lead = teamOf(m) && leadOf(teamOf(m)) === m.name; return `<a href="#/bots/${m.name}"><circle cx="${p.x}" cy="${p.y}" r="15" fill="${m.color}" class="pg-node"/>${lead ? `<circle cx="${p.x}" cy="${p.y}" r="19" class="pg-lead"/>` : ''}<text x="${p.x}" y="${p.y + 4}" text-anchor="middle" class="pg-init">${m.name[0]}</text><text x="${p.x}" y="${p.y + 34}" text-anchor="middle" class="pg-name">${esc(m.name)}</text></a>`; }).join('')}
    </svg><div class="pg-legend small muted"><span><i class="lg-in"></i>peers inside a team (data-only ask)</span><span><i class="lg-link"></i>teams linked</span><span><i class="lg-peer"></i>explicit cross-team peer</span><span><i class="lg-lead"></i>lead</span></div>`;
  }

  /* ---------- list ---------- */
  // a bot card says one thing: what needs you about this bot, or what it's doing; the rest is on its page
  const botCard = (m) => `<a class="panel bot-card" href="#/bots/${m.name}"${UI.rowState(pending(m.name).length ? 'ember' : '')}>
        <div class="row">${UI.sigil(m, 'lg')}<div class="grow"><h3>${m.name}</h3><div class="muted small">${esc(m.desc)}</div></div></div>
        <div class="small dim">${UI.botLine(m)}</div>
      </a>`;
  UI.peersGraph = peersGraph;

  /* ---------- team pages ---------- */
  const leadOf = (t) => (t && t.cfg && t.cfg.routing && t.cfg.routing.lead) || null;
  UI.leadOf = leadOf;
  /* the team form: conductor's teams.<t> keys (feat/members teams.go) */
  function teamForm(t) {
    const isNew = !t, c = t ? staged('teams', t.name) : { operators: ['ops'], autonomy: { ceiling: 'reversible' }, isolation: { sandbox: 'required' } };
    const g = (p) => S.getPath(c, p);
    const initial = isNew ? { ceiling: 'reversible', sandbox: 'required', peers: 'team' } : { description: c.description, operators: c.operators, ceiling: g('autonomy.ceiling') || 'reversible', shadow: !!g('autonomy.shadow'), sandbox: g('isolation.sandbox') || 'optional', handoff: g('isolation.handoff_workers') !== false, peers: c.peers || 'team', depth: g('delegation.max_depth'), pool_runs: g('budget.proactive.max_runs_per_day'), pool_turns: g('budget.proactive.max_turns_per_day'), pool_tokens: g('budget.proactive.max_tokens_per_day'), pool_cost: g('budget.proactive.max_cost_per_day'), cap_action: g('budget.spend_cap.per_action'), cap_period: g('budget.spend_cap.per_period'), secrets_via: g('credentials.secrets_via') || 'none', allow_secrets: g('credentials.allow_secrets'), logins: g('browser.logins'), credentials: g('browser.credentials'), repo: g('knowledge.repo'), promotion: g('knowledge.promotion') || 'proposal', lead: g('routing.lead'), fallback: g('routing.fallback') };
    UI.form({ caps: isNew ? 'teams:' : `teams.${t.name}`, title: isNew ? 'New team' : `Edit team ${t.name}`, wide: true,
      intro: 'A team groups bots: they share approvers and ceilings, and a bot on it can only be stricter. Teams are the unit for shared access.',
      values: initial,
      fields: [
        isNew ? { k: 'name', type: 'string', req: true, ph: 'e.g. data', pattern: /^[a-z][a-z0-9-]*$/, patternMsg: 'Lowercase letters, digits and dashes.', validate: (v) => (T(v) ? 'A team by that name exists.' : '') } : null,
        { k: 'description', type: 'string', mono: false, label: 'Description' },
        isNew ? { k: 'bots', type: 'select-multi', label: 'Bots', options: S.view().bots.map((m) => m.name), create: 'bot', createLabel: 'New bot', help: 'A bot is on at most one team: adding one here moves it.' } : null,
        { section: 'Who approves', k: 'operators', type: 'select-multi', label: 'Operators (approvers)', options: Object.keys(D.operatorsCfg), help: 'May approve the bots\' proposals and knowledge promotions. A bot\'s operators must be a subset. Empty: every operator.' },
        { section: 'What bots may do on their own', k: 'ceiling', type: 'seg', label: 'Ceiling (the most a bot may run without asking)', options: [['reversible', 'reversible: undoable actions'], ['observe', 'observe: read only']], help: 'A bot may promote a verb (run it without your signature) only up to this tier. propose, commit_money and never always ask or refuse.' },
        { k: 'shadow', type: 'bool', label: 'Shadow mode', on: 'hold every bot in shadow mode (proposals are recorded, nothing executes)' },
        { k: 'sandbox', type: 'seg', label: 'Agent sandbox', options: [['required', 'required'], ['optional', 'optional']], help: 'required: the bots\' sessions start only while conductor\'s agent sandbox is enforcing.' },
        { k: 'handoff', type: 'bool', label: 'Hand-off sessions', on: 'bots may spawn a session a human can take over (otherwise cli workers only)' },
        { section: 'Talking and delegating', k: 'peers', type: 'seg', label: 'Teammates', options: [['team', 'may message each other'], ['none', 'only explicit links']] },
        { k: 'depth', type: 'number', label: 'Delegation · most hops', ph: '3 (default)' },
        { section: 'Budgets', k: 'pool_runs', type: 'number', label: 'Proactive sweeps · runs / day (shared pool)' },
        { k: 'pool_turns', type: 'number', label: 'Proactive sweeps · model turns / day' },
        { k: 'pool_tokens', type: 'string', label: 'Proactive sweeps · tokens / day', ph: '500k' },
        { k: 'pool_cost', type: 'number', label: 'Proactive sweeps · USD / day (model spend)', show: () => UI.isApi() },
        { k: 'cap_action', type: 'number', label: 'Cloud spend cap · per action (commit_money)', help: 'Real money a bot may commit in one approved action. Not model spend.' },
        { k: 'cap_period', type: 'number', label: 'Cloud spend cap · per 30 days' },
        { section: 'Shared access', k: 'secrets_via', type: 'seg', label: 'Secrets via', options: [['none', 'none'], ['broker', 'broker (single-use grants)'], ['env', 'env']] },
        { k: 'allow_secrets', type: 'select-multi', label: 'Vault entries the broker may issue', options: S.view().secrets.map((x) => x.ref), show: (v) => v.secrets_via === 'broker' },
        { k: 'logins', type: 'list', label: 'Saved logins bots may use (sites)', ph: 'console.example.com' },
        { k: 'credentials', type: 'select-multi', label: 'Sign-in credentials bots may use', options: S.view().access.credentials.map((x) => x.name), create: 'credential', createLabel: 'Add a login' },
        { section: 'Knowledge and routing', k: 'repo', type: 'string', label: 'Team knowledge repo', ph: 'team-knowledge/<team>', help: 'Optional. The team\'s shared knowledge.' },
        { k: 'promotion', type: 'seg', label: 'Promotion', options: [['proposal', 'an operator approves'], ['direct', 'lands at once']], show: (v) => !!v.repo },
        isNew ? null : { k: 'lead', type: 'enum', label: 'Lead (triages what no rule matched)', options: botsOf(t.name).map((m) => m.name), create: 'bot', createCtx: { team: t.name }, createLabel: 'New bot on this team' },
        isNew ? null : { k: 'fallback', type: 'enum', label: 'Fallback (when the lead can\'t triage)', options: botsOf(t.name).map((m) => m.name) },
      ].filter(Boolean),
      submit: 'Review change', submitIcon: 'check',
      onSubmit: (vIn) => {
        // a form edits only what you changed: the node is built from the submitted values and from the values the form
        // opened with, and only the difference is applied to the current config (so unchanged fields, key order and
        // keys the form doesn't show, such as knowledge.promotion, stay byte-for-byte)
        const v0 = isNew ? {} : Object.assign({}, initial), empty = (x) => x == null || x === '' || (Array.isArray(x) && !x.length);
        const v = Object.assign({}, vIn); Object.keys(v).forEach((k) => { if (empty(v[k]) && empty(v0[k])) v[k] = v0[k]; });
        const build = (v) => { const name = isNew ? v.name : t.name, n = {};
        if (v.description) n.description = v.description;
        if (isNew ? false : c.collections) n.collections = c.collections;
        if ((v.operators || []).length) n.operators = v.operators;
        if (v.peers === 'none') n.peers = 'none';
        if (v.depth != null) n.delegation = { max_depth: v.depth };
        n.autonomy = {}; if (v.ceiling && v.ceiling !== 'reversible') n.autonomy.ceiling = v.ceiling; else if (v.ceiling) n.autonomy.ceiling = 'reversible'; if (v.shadow) n.autonomy.shadow = true; if (g('autonomy.tiers')) n.autonomy.tiers = g('autonomy.tiers');
        const pool = {}; if (v.pool_runs != null) pool.max_runs_per_day = v.pool_runs; if (v.pool_turns != null) pool.max_turns_per_day = v.pool_turns; if (v.pool_tokens) pool.max_tokens_per_day = v.pool_tokens; if (UI.isApi() ? v.pool_cost != null : g('budget.proactive.max_cost_per_day') != null) pool.max_cost_per_day = UI.isApi() ? v.pool_cost : g('budget.proactive.max_cost_per_day');
        const cap = {}; if (v.cap_action != null) cap.per_action = v.cap_action; if (v.cap_period != null) cap.per_period = v.cap_period;
        if (Object.keys(pool).length || Object.keys(cap).length) { n.budget = {}; if (Object.keys(cap).length) n.budget.spend_cap = cap; if (Object.keys(pool).length) n.budget.proactive = pool; }
        n.isolation = { sandbox: v.sandbox || 'optional' }; if (v.handoff === false) n.isolation.handoff_workers = false;
        if (v.secrets_via && v.secrets_via !== 'none') { n.credentials = { secrets_via: v.secrets_via }; if ((v.allow_secrets || []).length) n.credentials.allow_secrets = v.allow_secrets; }
        if ((v.logins || []).length || (v.credentials || []).length) { n.browser = {}; if ((v.logins || []).length) n.browser.logins = v.logins; if ((v.credentials || []).length) n.browser.credentials = v.credentials; }
        if (v.repo) { n.knowledge = { repo: v.repo }; if (v.promotion === 'direct') n.knowledge.promotion = 'direct'; }
        const ro = Object.assign({}, isNew ? {} : c.routing || {}); if (v.lead) ro.lead = v.lead; if (v.fallback) ro.fallback = v.fallback; if (Object.keys(ro).length) n.routing = ro;
        return n; };
        const name = isNew ? v.name : t.name, n1 = build(v);
        const apply = (tgt, a, b) => { new Set([...Object.keys(a || {}), ...Object.keys(b || {})]).forEach((k) => { const x = (a || {})[k], y = (b || {})[k]; if (JSON.stringify(x) === JSON.stringify(y)) return;
          const obj = (o) => o && typeof o === 'object' && !Array.isArray(o); if (obj(x) && obj(y) && obj(tgt[k])) apply(tgt[k], x, y); else if (y === undefined) delete tgt[k]; else tgt[k] = y; }); return tgt; };
        const n = isNew ? n1 : apply(Y_clone(c), build(v0), n1);
        S.stage('teams', name, n);
        (v.bots || []).forEach((mn) => { const mc = Y_clone(staged('bots', mn)); mc.team = name; S.stage('bots', mn, mc); });
        if (isNew && !D.teamKnowledge[name] && v.repo) D.teamKnowledge[name] = { repo: v.repo, files: {}, proposals: [] };
        UI.staged(S.find('teams', name));
      } });
  }
  const Y_clone = (o) => JSON.parse(JSON.stringify(o));
  UI.act('team-new', () => teamForm(null));
  // the team's own ⋯: on a phone the header keeps one action (New ticket) and the rest are here
  UI.act('team-more', (b) => UI.menu(b, [['Route an event here…', 'send', 'team-deliver', `data-t="${b.dataset.t}"`], ['Edit team…', 'edit', 'team-edit', `data-t="${b.dataset.t}"`], ['-'], ['Remove this team…', 'trash', 'team-del', `data-t="${b.dataset.t}"`]]));
  UI.act('team-edit', (b) => teamForm(T(b.dataset.t)));
  UI.act('team-del', (b) => {
    const t = T(b.dataset.t), ms = botsOf(t.name);
    UI.confirm({ title: `Remove team ${t.name}?`, body: `${ms.length ? `Its ${ms.length} bot${ms.length > 1 ? 's' : ''} (${ms.map((m) => m.name).join(', ')}) leave the team and keep their own settings. ` : ''}Stages the removal; review and apply with your passkey.`, ok: 'Stage removal', danger: true }).then((ok) => {
      if (!ok) return; ms.forEach((m) => { const c = Y_clone(staged('bots', m.name)); delete c.team; S.stage('bots', m.name, c); });
      UI.staged(S.stage('teams', t.name, null));
    });
  });

  const routeLabel = (r) => !r ? '' : r.by === 'rule' ? `routed to <b>${r.team}</b> by rule <span class="mono">${esc(r.rule)}</span>${r.via === 'lead' ? ` → assigned by lead <b>${esc(r.lead)}</b> to <b>${esc(r.to)}</b>` : ` → <b>${esc(r.to)}</b>`}` : r.by === 'lead' ? `assigned by lead <b>${esc(r.lead)}</b> to <b>${esc(r.to)}</b>` : r.by === 'operator' ? `reassigned by operator <b>ops</b> to <b>${esc(r.to)}</b>${r.from ? ` (was ${esc(r.from)})` : ''}` : r.by === 'link' ? `arrived over the team link from <b>${esc(r.team)}</b>` : r.by === 'proactive' ? 'opened by a proactive sweep' : r.by === 'delegate' ? `delegated by <b>${esc(r.from)}</b> → <b>${esc(r.to)}</b> from ticket <span class="mono">${esc(r.parent)}</span>${r.cross ? ` (across the ${esc(r.team)} link)` : ''}` : `delivered to <b>${esc(r.to)}</b> directly`;
  UI.routeLabel = routeLabel;
  const glob = (pat, v) => new RegExp('^' + String(pat).split('*').map((x) => x.replace(/[.+?^${}()|[\]\\]/g, '\\$&')).join('.*') + '$').test(String(v));
  const matchRule = (t, ev) => (((t.cfg.routing || {}).rules) || []).find((r) => r.match && Object.entries(r.match).every(([k, v]) => (k === 'on' ? glob(v, ev.on) : glob(v, ev.payload[k] != null ? ev.payload[k] : S.getPath(ev.payload, 'incident.' + k) || ''))));
  const tv = { ev: 'ev-pd' };

  function teamShell(t, tab, body) {
    const c = staged('teams', t.name) || t.cfg, ms = botsOf(t.name);
    const pend = c.knowledge ? R.pending('team:' + t.name) : 0;
    // the bot page's header (crumbs, avatar, name, one line): what's true of the whole team, in words, never capsules
    const lead = leadOf({ cfg: c }), ceilW = { observe: 'only read', reversible: 'do undoable things' }[S.getPath(c, 'autonomy.ceiling') || 'reversible'] || S.getPath(c, 'autonomy.ceiling');
    return `<div class="page dl-page team-page">${UI.crumbs([['Bots', '#/bots'], [t.name]])}
      <div class="dl-bothead">${UI.teamAv(t.name, 'xl')}<div><h1>${esc(t.name)}</h1><p class="dl-lead">${ms.length} bot${ms.length === 1 ? '' : 's'}${lead ? `, led by ${esc(lead)}` : ''}. ${(c.operators || []).length ? `${(c.operators || []).map(esc).join(' or ')} approve${(c.operators || []).length === 1 ? 's' : ''}` : 'Any operator approves'}; on their own, its bots may ${ceilW}.</p></div>${S.find('teams', t.name) ? UI.statusMark('staged', 'Change not applied') : ''}
        <button class="icon-btn" data-act="team-more" data-t="${t.name}" aria-label="More for team ${esc(t.name)}">${icon('more')}</button></div>
      <div class="dl-actions"><a class="btn secondary" href="#/tickets/new?team=${encodeURIComponent(t.name)}">${icon('plus')}New ticket</a><button class="btn secondary ph-hide" data-act="team-deliver" data-t="${t.name}">${icon('send')}Route an event here</button><button class="btn secondary ph-hide" data-act="team-edit" data-t="${t.name}">${icon('edit')}Edit team</button></div>
      ${UI.tabs([['overview', 'Overview', `#/teams/${t.name}`], ['routing', 'Routing', `#/teams/${t.name}/routing`, ((c.routing || {}).rules || []).length], ['defaults', 'Ceilings & scopes', `#/teams/${t.name}/defaults`], ['tickets', 'Tickets', `#/teams/${t.name}/tickets`, D.cases.filter((x) => x.status === 'open' && UI.ticketTeam(x) === t.name).length || null], ['knowledge', 'Knowledge', `#/teams/${t.name}/knowledge`, pend || null]], tab)}
      ${body}</div>`;
  }
  /* ---------- delegation ---------- */
  const DST = { running: 'run', done: 'ok', failed: 'fail', cancelled: 'idle' };
  /** Who `from` may delegate to: teammates, and bots in teams linked both ways. Others carry the reason they can't. */
  UI.delegationReach = (from) => {
    const f = M(from), ft = f && f.cfg && f.cfg.team, fteam = ft && T(ft);
    return D.bots.filter((b) => b.name !== from).map((b) => { const bt = b.cfg && b.cfg.team;
      const mates = ft && bt === ft && (!fteam || fteam.cfg.peers !== 'none');
      const why = mates || S.linked(from, b.name) ? '' : ft && bt === ft ? `team ${ft} sets peers: none; link the two bots to delegate` : `${from} and ${b.name} aren't linked: both list each other under peers, or add a bot_links pair`;
      return { name: b.name, team: bt, ok: !why, why, cross: !!(bt && ft && bt !== ft) }; });
  };
  // a hand-off in words: who handed what to whom, where it stands, what came back; the requester said plainly (it is
  // carried through, so the teammate works with no more trust than whoever asked)
  const reqWords = (r) => (/^operator:/.test(r) ? 'you' : /^event:pd/.test(r) ? 'a PagerDuty incident' : /^event:gh/.test(r) ? 'a GitHub event' : /^event:/.test(r) ? 'an event' : /^chat:/.test(r) ? 'someone in chat' : /^bot:/.test(r) ? r.slice(4) : 'a routine check');
  const edgeHTML = (d) => `<div class="item deleg" data-deleg="${d.id}">${UI.mark(DST[d.status] || 'idle')}<div class="grow small deleg-b">
      <div>delegated by <b>${esc(d.from)}</b> → <a class="dl-link" href="#/tickets/${d.child}"><b>${esc(d.to)}</b></a>${M(d.from) && M(d.to) && teamOf(M(d.from)) && teamOf(M(d.to)) && teamOf(M(d.from)).name !== teamOf(M(d.to)).name ? ' · linked bots' : ''} · <span class="deleg-st">${esc(d.status)}</span></div>
      <div class="deleg-task">“${esc(d.task)}”</div>
      ${d.result ? `<div class="deleg-result">↩ result: ${esc(d.result)}</div>` : ''}
      <div class="dl-sub">For ${esc(reqWords(d.requester || ''))}, whose trust it keeps · ${ago(d.at)} ago${d.done ? ` · reported back ${ago(d.done)} ago` : ''}</div></div>
      ${d.status === 'running' ? `<button class="btn sm ghost" data-act="deleg-cancel" data-id="${d.id}">${icon('x')}Cancel</button>` : ''}</div>`;
  UI.act('deleg-cancel', (b) => UI.confirm({ title: `Cancel delegation ${b.dataset.id}?`, body: 'The recipient\'s child task is closed and the delegating bot is told the task was cancelled. Immediate, audited.', ok: 'Cancel delegation', danger: true }).then((ok) => {
    if (!ok) return; const d = D.delegations.find((x) => x.id === b.dataset.id); d.status = 'cancelled'; d.done = now();
    const ch = D.cases.find((x) => x.id === d.child); if (ch) ch.status = 'closed';
    const par = D.cases.find((x) => x.id === d.parent); if (par) (par.notes = par.notes || []).push({ at: now(), who: 'conductor', text: `delegation ${d.id} to ${d.to} cancelled by operator ops` });
    audit('bot_delegate_cancel', `${d.id} · ${d.from} → ${d.to}`); S.save(); UI.rerender();
  }));
  UI.act('case-delegate', (b) => {
    const m = M(b.dataset.m), c = D.cases.find((x) => x.id === b.dataset.c), reach = UI.delegationReach(m.name);
    UI.form({ caps: `conductor bot delegate ${m.name} --case ${c.id}`, title: `Have ${m.name} delegate a task`, intro: `${m.name} hands a teammate a task from <b>${c.id}</b>. The recipient works it as a child task under the original requester (<span class="mono">${esc(c.requester)}</span>), and the result reports back here.`,
      fields: [{ k: 'to', type: 'enum', req: true, label: 'Delegate to', options: reach.map((r) => [r.name, `${r.name}${r.team ? ' · ' + r.team : ' · solo'}${r.ok ? (r.cross ? ' (linked)' : '') : ' — not linked'}`]), def: (reach.find((r) => r.ok) || reach[0]).name, validate: (v) => { const r = reach.find((x) => x.name === v); return r && !r.ok ? r.why : ''; } },
        { k: 'task', type: 'text', req: true, label: 'Task', ph: 'What should the teammate do, and what should it report back?' }],
      submit: 'Delegate', submitIcon: 'send',
      onSubmit: (v) => {
        const r = reach.find((x) => x.name === v.to);
        const id = 'd' + (D.delegations.length + 1), child = newCase(v.to, { title: v.task.split('\n')[0].slice(0, 90), requester: c.requester, cls: c.cls, routed: { by: 'delegate', from: m.name, to: v.to, parent: c.id, delegation: id, team: teamOf(m) ? teamOf(m).name : null, cross: r.cross, requester_kept: true }, notes: [] });
        D.delegations.unshift({ id, from: m.name, to: v.to, parent: c.id, child, task: v.task, requester: c.requester, cls: c.cls, status: 'running', at: now() });
        audit('bot_delegate', `${id} · ${m.name} → ${v.to} · case ${c.id} → ${child} · requester ${c.requester} kept`); S.save(); UI.rerender();
        UI.toast(`Delegated to ${v.to}`, `child task ${child} · result reports back to ${c.id}`, 'run');
      } });
  });

  /** The team's ask & delegate activity: teammates as nodes, linked-team bots at the edge. */
  function activityGraph(t) {
    const ms = botsOf(t.name).map((b) => b.name);
    const edges = {};
    const add = (a, b, kind, d) => { const k = `${a}>${b}>${kind}`; edges[k] = edges[k] || { a, b, kind, n: 0, running: 0 }; edges[k].n++; if (d && d.status === 'running') edges[k].running++; };
    D.asks.forEach((x) => { if (ms.includes(x.from) || ms.includes(x.to)) add(x.from, x.to, 'ask'); });
    D.delegations.forEach((d) => { if (ms.includes(d.from) || ms.includes(d.to)) add(d.from, d.to, 'delegate', d); });
    const outside = [...new Set(Object.values(edges).flatMap((e) => [e.a, e.b]).filter((n) => !ms.includes(n)))];
    const W = 600, H = 290, cx = 240, cy = 148, R = 92, pos = {};
    ms.forEach((n, i) => { const a = -Math.PI / 2 + (i / Math.max(ms.length, 1)) * Math.PI * 2; pos[n] = { x: cx + R * Math.cos(a), y: cy + R * Math.sin(a) }; });
    outside.forEach((n, i) => (pos[n] = { x: 520, y: 90 + i * 80 }));
    const line = (e) => { const p = pos[e.a], q = pos[e.b]; if (!p || !q) return ''; const dx = q.x - p.x, dy = q.y - p.y, L = Math.hypot(dx, dy) || 1, off = e.kind === 'ask' ? 8 : -8; const nx = -dy / L * off, ny = dx / L * off; const x1 = p.x + dx / L * 20 + nx, y1 = p.y + dy / L * 20 + ny, x2 = q.x - dx / L * 22 + nx, y2 = q.y - dy / L * 22 + ny; return `<line x1="${x1}" y1="${y1}" x2="${x2}" y2="${y2}" class="ag-${e.kind}${e.running ? ' live' : ''}" marker-end="url(#ag-arrow-${e.kind})"/><text x="${(x1 + x2) / 2 + nx}" y="${(y1 + y2) / 2 + ny}" class="ag-n" text-anchor="middle">${e.kind === 'ask' ? 'ask' : 'delegate'} ×${e.n}</text>`; };
    return `<svg class="act-graph" viewBox="0 0 ${W} ${H}" role="img" aria-label="Ask and delegate activity in team ${esc(t.name)}">
      <defs><marker id="ag-arrow-ask" viewBox="0 0 8 8" refX="7" refY="4" markerWidth="7" markerHeight="7" orient="auto"><path d="M0 0L8 4L0 8z" class="ag-head-ask"/></marker><marker id="ag-arrow-delegate" viewBox="0 0 8 8" refX="7" refY="4" markerWidth="7" markerHeight="7" orient="auto"><path d="M0 0L8 4L0 8z" class="ag-head-delegate"/></marker></defs>
      <circle cx="${cx}" cy="${cy}" r="${R + 40}" class="ag-team"/><text x="${cx}" y="${cy + R + 30}" class="pg-tn" text-anchor="middle">team ${esc(t.name)}</text>
      ${Object.values(edges).map(line).join('')}
      ${ms.concat(outside).map((n) => { const b = M(n), p = pos[n]; return `<a href="#/bots/${n}"><circle cx="${p.x}" cy="${p.y}" r="17" fill="var(--color-icon-default)" class="pg-node${ms.includes(n) ? '' : ' outside'}"/><text x="${p.x}" y="${p.y + 5}" text-anchor="middle" class="pg-init">${n[0]}</text><text x="${p.x}" y="${p.y + 33}" text-anchor="middle" class="pg-name">${esc(n)}${ms.includes(n) ? '' : ' · ' + esc((teamOf(b) || { name: 'solo' }).name)}</text></a>`; }).join('')}
    </svg><div class="pg-legend small muted"><span><i class="lg-in"></i>ask (data only)</span><span><i class="lg-peer"></i>delegate (a task, under the requester's provenance)</span><span>bots outside the circle are in linked teams</span></div>`;
  }

  function teamOverview(t) {
    const c = staged('teams', t.name) || t.cfg, ms = botsOf(t.name);
    const others = S.view().bots.filter((m) => !ms.some((x) => x.name === m.name));
    const pool = S.getPath(c, 'budget.proactive'), claimed = ms.reduce((a, m) => a + (S.getPath(m.cfg, 'proactive.budget.max_runs_per_day') || 0), 0);
    const cases = D.cases.filter((x) => x.routed && (x.routed.team === t.name || ms.some((m) => m.name === x.routed.to))).slice(0, 8);
    const cross = ms.flatMap((m) => S.linksOf(m.name).filter((o) => { const ob = M(o); return !(ob && ob.cfg && ob.cfg.team === t.name); }).map((o) => [m.name, o]));
    return teamShell(t, 'overview', `<div class="grid g-side">
      <div class="stack">
        <div class="panel"><div class="panel-head"><h3>Bots on this team</h3><span class="muted small">peers of each other by default</span></div><div class="list">
          ${ms.map((m) => `<div class="item"><a href="#/bots/${m.name}" class="row grow" style="gap:10px">${UI.botAv(m.name, 'md')}<div><b>${esc(UI.botName(m.name))}</b> <span class="bav-id">${m.name}</span> ${leadOf({ cfg: c }) === m.name ? tag('lead') : ''}<div class="muted tiny">${esc(m.desc)}</div></div></a>
            ${leadOf({ cfg: c }) === m.name ? '' : `<button class="btn sm ghost" data-act="team-lead" data-t="${t.name}" data-m="${m.name}">Make lead</button>`}<button class="btn sm ghost" data-act="team-remove" data-t="${t.name}" data-m="${m.name}" aria-label="Remove ${m.name} from team">${icon('x')}Remove</button></div>`).join('') || '<div class="item muted small">No bots yet.</div>'}</div>
          <div class="panel-foot"><label class="row small grow"><span class="muted">Add a bot</span><select class="select mono" id="team-add-sel" aria-label="Bot to add" data-picker="bot" data-ctx='${esc(JSON.stringify({ page: `#/bots/new/settings?team=${t.name}&back=${encodeURIComponent('#/teams/' + t.name)}` }))}'><option value="">Choose a bot…</option>${others.map((m) => `<option value="${m.name}">${m.name}${teamOf(m) ? ' (moves from ' + teamOf(m).name + ')' : ' (solo)'}</option>`).join('')}<option value="__new__">+ Create a new bot in this team…</option></select></label><button class="btn sm" ${others.length ? `data-act="team-add" data-t="${t.name}"` : UI.off('Every existing bot is already here: create a new one from the list')}>${icon('plus')}Add to team</button></div></div>
        <div class="panel"><div class="panel-head"><h3>Delegations</h3><span class="muted small">teammates hand each other tasks; results report back</span></div><div class="list">${D.delegations.filter((d) => botsOf(t.name).some((b) => b.name === d.from || b.name === d.to)).map(edgeHTML).join('') || '<div class="item small muted">No delegations yet.</div>'}</div>
          <div class="panel-foot small muted">Beyond the team, a bot delegates only to a bot it's linked with (both list each other as peers, or an operator's bot_links pair).</div></div>
        <div class="panel"><div class="panel-head"><h3>Routing decisions</h3><span class="muted small">requester provenance kept through every hop</span></div><div class="list">${cases.map((x) => `<a class="item click" href="#/tickets/${x.id}"><span class="mono tiny muted">${x.id}</span><div class="grow small"><div class="ellipsis"><b>${esc(x.title)}</b></div><div class="tiny muted">${routeLabel(x.routed)} · requester ${UI.label(`${x.requester}`, { cls: 'taint ' + x.cls })}</div></div><span class="chev">${icon('chev')}</span></a>`).join('') || '<div class="item muted small">Nothing routed yet.</div>'}</div></div>
      </div>
      <div class="stack">
        ${!UI.usageOn() ? '' : (() => { const bp = UI.botsProv(ms.map((m) => m.name)), b0 = {}, rc = b0.max_runs_per_day, tc = b0.max_tokens_per_day, uc = UI.isApi() ? b0.max_cost_per_day : null, runs = ms.reduce((a, m) => a + Object.values(D.usage.bots[m.name] || {}).reduce((x, y) => x + (y.runs || 0), 0), 0), src = UI.usageSource();
          const meter = (v, cap, label, f) => `<div class="row"><span class="grow muted">${label}</span><span class="mono">${f(v)} / ${f(cap)}</span></div><div class="meter"><i style="width:${Math.min(100, (v / cap) * 100)}%"></i></div>`;
          return `<div class="panel" data-usage><div class="panel-head"><h3>Usage today</h3><span class="muted small">per provider, never combined</span></div><div class="panel-body stack tight small">
          ${Object.keys(D.usage.providers).map((p) => { const x = bp[p] || { runs: 0, turns: 0 }; return `<div class="prov-row" data-prov="${p}"><div class="row"><b class="grow">${UI.provLabel(p)}</b><span class="mono" data-figure>${x.runs} runs · ${x.turns} turns${x.tokens != null ? ' · ' + UI.fmtTok(x.tokens) + ' tok' : ''}${UI.isApi() ? ' · ' + usd(x.usd || 0) : ''}</span></div>${tc && x.tokens != null ? meter(x.tokens, tc, 'token cap', UI.fmtTok) : ''}${uc != null ? meter(x.usd || 0, uc, 'spend cap', usd) : ''}</div>`; }).join('')}
          <div class="pv-src tiny" data-src="${src ? 'source' : 'observed'}">runs and turns observed by conductor${src ? ' · tokens' + (UI.isApi() ? ' and dollars' : '') + ' from ' + esc(src.name) : UI.isApi() ? ' · dollars from runtime reports' : ''}</div>
          ${tc || uc != null ? '<div class="muted tiny">Token and spend caps apply to each provider on its own.</div>' : ''}
          ${rc ? `<div data-budget><div class="row"><span class="grow muted">runs dispatched · budget</span><span class="mono">${runs} / ${rc}</span></div><div class="meter"><i style="width:${Math.min(100, (runs / rc) * 100)}%"></i></div></div>` : ''}
          ${pool ? `<div data-budget><div style="margin-top:6px"><b>Proactive pool (shared)</b></div><div class="mono">${claimed} / ${pool.max_runs_per_day} runs claimed${pool.max_tokens_per_day ? ' · ' + UI.fmtTok(pool.max_tokens_per_day) + ' tok/day per provider' : ''}${UI.isApi() && pool.max_cost_per_day != null ? ' · ' + usd(pool.max_cost_per_day) + '/day per provider' : ''}</div><div class="meter"><i style="width:${Math.min(100, (claimed / pool.max_runs_per_day) * 100)}%"></i></div><div class="muted tiny">Each bot's sweep budget draws on the pool; the validator refuses a bot over it.</div></div>` : '<div class="muted tiny">No shared proactive pool.</div>'}</div></div>`; })()}
        <div class="panel"><div class="panel-head"><h3>Links</h3></div><div class="panel-body stack tight small">
          <div><span class="caps">inside the team</span><div class="muted tiny" style="margin-top:3px">${ms.length > 1 ? ms.map((m) => m.name).join(' ↔ ') + ' · data-only ask, no extra config' : 'One bot: nobody to ask yet.'}</div></div>
          <div><span class="caps">beyond the team</span>${cross.map(([a, o]) => `<div class="row" style="margin-top:6px"><span class="mono grow">${esc(a)} ↔ ${esc(o)}</span><span class="muted tiny">${(D.botLinks || []).some((pr) => pr.includes(a) && pr.includes(o)) ? 'bot_links' : 'both list each other as peers'}</span>${(D.botLinks || []).some((pr) => pr.includes(a) && pr.includes(o)) ? `<button class="btn sm ghost" data-act="botlink-del" data-a="${a}" data-b="${o}">Unlink</button>` : ''}</div>`).join('') || '<div class="muted tiny" style="margin-top:4px">No links: the team\'s bots talk only to each other.</div>'}<button class="btn sm" style="margin-top:8px" data-act="botlink-add" data-t="${t.name}">${icon('link')}Link a bot…</button></div>
          <div class="muted tiny">A link lets two bots message each other and delegate, data only. It's a pair of bots, never a whole team, and a config edit (widening).</div>
          <div><span class="caps">delegation reach</span>${botsOf(t.name).map((b) => { const r = UI.delegationReach(b.name); return `<div class="tiny" style="margin-top:4px"><b class="mono">${b.name}</b> → ${r.filter((x) => x.ok).map((x) => `<span class="mono">${x.name}</span>`).join(', ') || '<span class="muted">no one</span>'}${r.some((x) => !x.ok && x.team) ? ` <span class="muted">· isolated from ${r.filter((x) => !x.ok && x.team).map((x) => x.name).join(', ')}</span>` : ''}</div>`; }).join('')}</div></div></div>
      </div></div>`);
  }
  /** A routing rule as a sentence: "When a PagerDuty incident comes in, for service api*, it goes to platform-sre." */
  const ruleWords = (r, c) => { const m = r.match || r.when || {}, ev = (SCHEMA.events.find((e) => e.on === m.on) || {}), conn = String(m.on || '').split('.')[0], use = ((D.connectors.find((x) => x.name === conn) || {}).use) || conn;
    const cd = SCHEMA.connectors[use] || {}, ed = (cd.events || {})[String(m.on || '').split('.')[1]];
    const what = m.on ? (ed ? `When “${ed.label.toLowerCase()}” arrives from ${cd.label || use}` : `When ${m.on} arrives`) : 'When anything arrives';
    const conds = Object.entries(m).filter(([k]) => k !== 'on').map(([k, v]) => `${k.replace(/_/g, ' ')} ${[].concat(v).join(' or ')}`);
    const to = r.to === 'lead' ? `the lead (${(c.routing || {}).lead || 'none yet'})` : r.to;
    return `${what}${conds.length ? `, for ${conds.join(', ')}` : ''}, it goes to ${to}.`; };
  UI.act('rule-more', (b) => { const i = +b.dataset.i, n = +b.dataset.n, t = b.dataset.t; UI.menu(b, [['Move up', 'chev', 'rule-move', i ? `data-t="${t}" data-i="${i}" data-d="-1"` : UI.off('Already first')], ['Move down', 'chevd', 'rule-move', i < n - 1 ? `data-t="${t}" data-i="${i}" data-d="1"` : UI.off('Already last')], ['-'], ['Delete rule', 'trash', 'rule-del', `data-t="${t}" data-i="${i}"`]]); });
  function teamRouting(t) {
    const c = staged('teams', t.name) || t.cfg, rules = (c.routing || {}).rules || [];
    const ev = SCHEMA.events.find((e) => e.id === tv.ev) || SCHEMA.events[0];
    const hit = matchRule({ cfg: c }, ev);
    return teamShell(t, 'routing', `<div class="grid g-side"><div class="stack">
      <div class="panel"><div class="panel-head"><h3>Rules</h3><span class="muted small">first match wins · no match: the lead triages · then the fallback</span><div class="tools"><button class="btn sm" data-act="rule-edit" data-t="${t.name}" data-i="-1">${icon('plus')}Add a rule</button></div></div>
        <ol class="rule-list">${rules.map((r, i) => `<li class="rule-row"><span class="rule-n">${i + 1}</span><div class="rule-t"><b>${esc(ruleWords(r, c))}</b><span class="dl-sub">${esc(r.name || 'rule ' + (i + 1))}</span></div>
          <span class="dl-na"><button class="btn sm secondary" data-act="rule-edit" data-t="${t.name}" data-i="${i}">${icon('edit')}Edit</button><button class="icon-btn" data-act="rule-more" data-t="${t.name}" data-i="${i}" data-n="${rules.length}" aria-label="More for this rule">${icon('more')}</button></span></li>`).join('') || '<li class="dl-sub rule-none">No rules: work reaches this team only through its bots directly.</li>'}</ol>
        <div class="panel-foot small muted">Changing a rule is reviewed like any config change. Lead: <b>${esc((c.routing || {}).lead || 'none')}</b> · fallback: <b>${esc((c.routing || {}).fallback || 'none')}</b>.${S.find('teams', t.name) ? ' <a class="link" href="#/config/apply">Review staged change</a>' : ''}</div></div>
      </div><div class="stack">
      <div class="panel"><div class="panel-head"><h3>Try a rule</h3></div><div class="panel-body stack tight small"><label class="field"><span class="caps">a real past event</span><select class="select mono" data-on-change="route-ev" aria-label="Event to route">${SCHEMA.events.map((e) => `<option value="${e.id}"${e.id === ev.id ? ' selected' : ''}>${esc(e.label)}</option>`).join('')}</select></label>
        ${hit ? `<div class="callout ok small">Matches <b class="mono">${esc(hit.name)}</b> → <b>${esc(hit.to)}</b>. The case keeps requester <span class="mono">event:${esc(ev.on)}</span>.</div>` : `<div class="callout small">No rule in <b>${t.name}</b> matches <span class="mono">${esc(ev.on)}</span>; it goes to whichever trigger or bot it was addressed to.</div>`}</div></div>
      <div class="callout info small">The lead is a bot: it reads the case, then assigns it with <span class="mono">bot assign</span>. The requester never changes on the way, so the assignee runs on no more trust than whoever asked.</div></div></div>`);
  }
  function teamDefaults(t) {
    const c = staged('teams', t.name) || t.cfg, ms = botsOf(t.name);
    const st2 = Object.assign({}, D, { teams: D.teams.map((x) => (x.name === t.name ? { name: x.name, cfg: c } : x)) });
    const effs = ms.map((m) => { const mm = Object.assign({}, m, { cfg: staged('bots', m.name) }); return { m: mm, e: S.effective(mm, st2), errs: S.narrowErrors(m.name, mm.cfg, st2) }; });
    const badge = (src, ok = true) => (!ok ? UI.label('wider ✗', { cls: 'inherit wid' }) : src === 'inherits' ? UI.label('inherits', { cls: 'inherit inh' }) : src === 'equal' ? UI.label('same as team', { cls: 'inherit inh' }) : src === 'narrows' ? UI.label('narrows', { cls: 'inherit nar' }) : src === 'own' ? UI.label('own', { cls: 'inherit inh' }) : UI.label(esc(src), { cls: 'inherit inh' }));
    const row = (label, teamV, cell) => `<tr><td data-l="setting"><b>${label}</b></td><td data-l="team" class="mono small">${teamV}</td>${effs.map((x) => `<td data-l="${x.m.name}" class="small">${cell(x)}</td>`).join('')}</tr>`;
    const subset = (a, b) => !b || (a || []).every((x) => b.includes(x));
    const ceiling = S.getPath(c, 'autonomy.ceiling') || 'reversible';
    const pool = S.getPath(c, 'budget.proactive');
    return teamShell(t, 'defaults', `<div class="callout small" style="margin-bottom:12px">A bot on a team can only be <b>stricter</b>: approvers from the team's list, promotions no higher than the ceiling, the team's tier classifications and sandbox, sweeps inside the shared pool, logins inside the team's scope. Anything wider is refused when the config is validated, naming the team value it breaks.</div>
      <div class="panel" style="margin-top:14px"><div class="panel-head"><h3>Team values and what each bot does with them</h3><div class="tools"><button class="btn sm" data-act="team-edit" data-t="${t.name}">${icon('edit')}Edit team</button></div></div>
      <table class="tbl responsive inherit-tbl"><thead><tr><th>setting</th><th>team</th>${effs.map((x) => `<th><a href="#/bots/${x.m.name}/settings">${x.m.name}</a></th>`).join('')}</tr></thead><tbody>
        ${row('approvers', (c.operators || []).join(', ') || 'every operator', ({ e }) => `<span class="mono">${(e.operators.v || []).join(', ') || '—'}</span> ${badge(e.operators.src, subset(e.operators.v, c.operators))}`)}
        ${row('runs alone up to (ceiling)', ceiling, ({ m }) => { const pr = S.getPath(m.cfg, 'autonomy.promote') || []; const over = pr.filter((v) => S.LADDER.indexOf(S.tierFor(m, v, st2).tier) > S.LADDER.indexOf(ceiling)); return `<span class="mono">${pr.join(', ') || 'nothing promoted'}</span> ${badge(pr.length ? 'own' : 'inherits', !over.length)}`; })}
        ${row('tier classifications', Object.entries(S.getPath(c, 'autonomy.tiers') || {}).map(([k, v]) => `${k}: ${v.join(', ')}`).join('; ') || '—', ({ errs }) => { const te = errs.filter((x) => /classifies/.test(x)); return te.length ? `${UI.label('wider ✗', { cls: 'inherit wid' })} <span class="tiny">${esc(te[0])}</span>` : UI.label('stricter or equal', { cls: 'inherit inh' }); })}
        ${row('shadow mode', S.getPath(c, 'autonomy.shadow') ? 'held in shadow' : 'not held', ({ e }) => `<span class="mono">${e.shadow.v === undefined ? (S.getPath(c, 'autonomy.shadow') ? 'shadow' : '—') : e.shadow.v ? 'shadow' : 'off'}</span> ${badge(e.shadow.src, !(S.getPath(c, 'autonomy.shadow') && e.shadow.v === false))}`)}
        ${row('agent sandbox', S.getPath(c, 'isolation.sandbox') || 'optional', ({ e }) => `<span class="mono">${e.sandbox.v || 'optional'}</span> ${badge(e.sandbox.src, !(S.getPath(c, 'isolation.sandbox') === 'required' && e.sandbox.v === 'optional'))}`)}
        ${row('proactive sweeps (shared pool)', pool ? Object.entries(pool).map(([k, v]) => `${k.replace(/^max_|_per_day$/g, '')} ${v}`).join(' · ') : '—', ({ m, errs }) => { const b = S.getPath(m.cfg, 'proactive.budget'); return b ? `<span class="mono">${Object.entries(b).map(([k, v]) => `${k.replace(/^max_|_per_day$/g, '')} ${v}`).join(' · ')}</span> ${badge(pool ? 'narrows' : 'own', !errs.some((x) => /proactive pool/.test(x)))}` : '<span class="muted">no sweeps</span>'; })}
        ${row('cloud spend cap', S.getPath(c, 'budget.spend_cap') ? `${S.getPath(c, 'budget.spend_cap.per_action') || '—'} / action` : '—', ({ e, errs }) => `<span class="mono">${e.spendCap.v ? (e.spendCap.v.per_action || '—') + ' / action' : '—'}</span> ${badge(e.spendCap.src, !errs.some((x) => /spend_cap/.test(x)))}`)}
        ${row('sign-in credentials', (S.getPath(c, 'browser.credentials') || []).join(', ') || 'any', ({ e }) => `<span class="mono">${e.credentials.v.join(', ') || 'none'}</span> ${badge(!e.credentials.scope ? 'own' : e.credentials.v.length === e.credentials.scope.length && subset(e.credentials.v, e.credentials.scope) ? 'equal' : 'narrows', subset(e.credentials.v, e.credentials.scope))}`)}
        ${row('saved logins', (S.getPath(c, 'browser.logins') || []).join(', ') || 'any', ({ e }) => `<span class="mono">${e.logins.v.join(', ') || 'none'}</span> ${badge(!e.logins.scope ? 'own' : e.logins.v.length === e.logins.scope.length && subset(e.logins.v, e.logins.scope) ? 'equal' : 'narrows', subset(e.logins.v, e.logins.scope))}`)}
      </tbody></table></div>`);
  }
  /** The tier ladder, explained once: what each tier means for execution. */
  function teamKnowledge(t) {
    const c = staged('teams', t.name) || t.cfg;
    if (!c.knowledge) return teamShell(t, 'knowledge', `<div class="panel"><div class="empty"><span class="serif">No shared knowledge repo</span>A team can keep one repo every bot on it reads. You write to it directly; bots propose and you approve.<button class="btn primary" data-act="team-edit" data-t="${t.name}">${icon('plus')}Add a knowledge repo</button></div></div>`);
    return teamShell(t, 'knowledge', UI.repoView('team:' + t.name, { title: c.knowledge.repo }));
  }
  UI.route('teams/:name/:tab?', ({ name, tab }) => {
    const t = T(name);
    if (!t) return `<div class="page"><div class="empty"><span class="serif">No team named ${esc(name)}</span><a class="btn" href="#/bots">Bots</a></div></div>`;
    return tab === 'tickets' ? teamShell(t, 'tickets', UI.teamTickets(t)) : tab === 'routing' ? teamRouting(t) : tab === 'defaults' ? teamDefaults(t) : tab === 'knowledge' ? teamKnowledge(t) : teamOverview(t);
  }, { nav: 'bots' });
  UI.act('team-lead', (b) => { const c = Y_clone(staged('teams', b.dataset.t)); c.routing = Object.assign({}, c.routing, { lead: b.dataset.m }); UI.staged(S.stage('teams', b.dataset.t, c)); });
  // removing the lead leaves routing without one: say so first
  UI.act('team-remove', (b) => { const t = T(b.dataset.t), lead = t && leadOf({ cfg: staged('teams', t.name) || t.cfg }) === b.dataset.m; if (lead && !b.dataset.ok) return UI.confirm({ title: `Remove ${esc(b.dataset.m)}, the team's lead?`, body: `Work routed to the lead falls to the team's rules or its fallback until you pick another lead.`, ok: 'Remove the lead', danger: true }).then((ok) => { if (ok) { b.dataset.ok = '1'; UI.acts['team-remove'](b); } });
    const c = Y_clone(staged('bots', b.dataset.m)); delete c.team; S.stage('bots', b.dataset.m, c); const tc = Y_clone(staged('teams', b.dataset.t)); const ro = tc.routing || {}; let ch = false; if (ro.lead === b.dataset.m) { delete ro.lead; ch = true; } if (ro.fallback === b.dataset.m) { delete ro.fallback; ch = true; } const before = (ro.rules || []).length; ro.rules = (ro.rules || []).filter((r) => r.to !== b.dataset.m); if (ro.rules.length !== before) ch = true; if (!ro.rules.length) delete ro.rules; if (ch) { if (Object.keys(ro).length) tc.routing = ro; else delete tc.routing; S.stage('teams', b.dataset.t, tc); } UI.staged(S.find('bots', b.dataset.m)); });
  UI.act('team-add', (b) => { const mn = UI.$('#team-add-sel').value; if (!mn) return UI.nudge(UI.$('#team-add-sel')); const c = Y_clone(staged('bots', mn)); c.team = b.dataset.t; UI.staged(S.stage('bots', mn, c)); });
  UI.act('botlink-add', (b) => { const t = b.dataset.t, mine = botsOf(t).map((m) => m.name), others = D.bots.filter((x) => !mine.includes(x.name)).map((x) => [x.name, `${x.name}${x.cfg && x.cfg.team ? ' · team ' + x.cfg.team : ' · solo'}`]);
    UI.form({ caps: 'bot_links', title: 'Link two bots', intro: 'They may message each other and delegate, data only, as if both listed the other under peers. Requester provenance is kept on every hop.', fields: [{ k: 'a', type: 'enum', req: true, label: `A bot on ${t}`, options: mine }, { k: 'b', type: 'enum', req: true, label: 'The bot to link', options: others }], submit: 'Review change', submitIcon: 'link',
      onSubmit: (v) => UI.staged(S.stage('botLinks', [v.a, v.b].sort().join('+'), [v.a, v.b], `link ${v.a} ↔ ${v.b}`)) }); });
  UI.act('botlink-del', (b) => UI.confirm({ title: `Unlink ${esc(b.dataset.a)} and ${esc(b.dataset.b)}?`, body: 'Stages removing the bot_links pair. Delegations already running finish.', ok: 'Stage it', cancel: 'Keep it', danger: true }).then((ok) => ok && UI.staged(S.stage('botLinks', [b.dataset.a, b.dataset.b].sort().join('+'), null))));
  UI.act('route-ev', (sel) => { tv.ev = sel.value; UI.rerender(); });
  const evOn = () => [...new Set(SCHEMA.events.map((e) => e.on).concat(['aws-cost.anomaly', 'slack-ops.mention']))];
  const rulesOf = (c) => { c.routing = c.routing || {}; c.routing.rules = c.routing.rules || []; return c.routing.rules; };
  const tidy = (c) => { if (c.routing && c.routing.rules && !c.routing.rules.length) delete c.routing.rules; if (c.routing && !Object.keys(c.routing).length) delete c.routing; };
  UI.act('rule-edit', (b) => {
    const t = T(b.dataset.t), c = Y_clone(staged('teams', t.name)), i = +b.dataset.i, rules = rulesOf(c), r = i >= 0 ? rules[i] : null;
    const conds = r ? Object.fromEntries(Object.entries(r.match || {}).filter(([k]) => k !== 'on')) : {};
    UI.form({ caps: `teams.${t.name}.routing.rules`, title: r ? `Edit rule ${r.name}` : 'Add a routing rule', values: r ? { name: r.name, on: (r.match || {}).on, conds, to: r.to } : {}, edit: !!r,
      intro: 'Deterministic: the event\'s fields matched against globs, no model. First match wins; no match goes to the lead to triage.',
      fields: [{ k: 'name', type: 'string', req: true, pattern: /^[a-z][\w-]*$/, patternMsg: 'letters, digits, - and _' }, { k: 'on', type: 'enum', label: 'When the event is', req: true, options: evOn() }, { k: 'conds', type: 'map', label: 'and its fields match (globs)', help: 'e.g. service → api*, repo → acme/web.' }, { k: 'to', type: 'enum', req: true, label: 'Route to', options: botsOf(t.name).map((m) => m.name), create: 'bot', createCtx: { team: t.name }, createLabel: 'New bot on this team' }],
      submit: 'Stage rule', onSubmit: (v) => { const match = { on: v.on }; Object.entries(v.conds || {}).forEach(([k, x]) => { match[k] = String(x); }); const nr = { name: v.name, match, to: v.to }; if (r) rules[i] = nr; else rules.push(nr); UI.staged(S.stage('teams', t.name, c), { go: false }); UI.closeModal(); UI.rerender(); return false; } });
  });
  UI.act('rule-del', (b) => { const c = Y_clone(staged('teams', b.dataset.t)); rulesOf(c).splice(+b.dataset.i, 1); tidy(c); UI.staged(S.stage('teams', b.dataset.t, c), { go: false }); UI.rerender(); });
  UI.act('rule-move', (b) => { const c = Y_clone(staged('teams', b.dataset.t)); const rr = rulesOf(c), i = +b.dataset.i, j = i + +b.dataset.d; [rr[i], rr[j]] = [rr[j], rr[i]]; UI.staged(S.stage('teams', b.dataset.t, c), { go: false }); UI.rerender(); });
  UI.act('kn-open', (a) => { D.teamKnowledge[a.dataset.t].open = a.dataset.f; S.save(); UI.rerender(); });
  UI.act('kn-decide', (b) => {
    const kn = D.teamKnowledge[b.dataset.t], p = kn.proposals.find((x) => x.id === b.dataset.id), approve = b.dataset.d === 'approve';
    UI.stepup({ title: `${approve ? 'Approve' : 'Deny'} promoting “${esc(p.summary)}”`, purpose: 'team knowledge', danger: !approve, ok: approve ? 'Approve with passkey' : 'Deny with passkey', what: `{ team: <b>${b.dataset.t}</b>, proposal: <b>${p.id}</b>, bot: <b>${p.bot}</b>, path: <b>${esc(p.to)}</b>, decision: <b>${approve ? 'approve' : 'deny'}</b> }` }).then((ok) => {
      if (!ok) return;
      p.status = approve ? 'approved' : 'denied'; p.approver = 'ops (passkey · laptop)';
      if (approve) { const body = p.diff.split('\n').filter((l) => l[0] === '+').map((l) => l.slice(1)).join('\n'); kn.files[p.to] = (kn.files[p.to] && !p.diff.startsWith('+#') ? kn.files[p.to].replace(/\n?$/, '\n') : '') + body + '\n'; kn.open = p.to; }
      audit('team_knowledge_' + (approve ? 'promoted' : 'denied'), `${b.dataset.t} · ${p.id} · ${p.bot} → ${p.to}`); S.save(); UI.rerender();
      UI.toast(approve ? `Promoted to ${esc(p.to)}` : 'Denied', approve ? `one commit to ${kn.repo}` : 'the bot is told', approve ? 'ok' : 'fail');
    });
  });
  UI.ticketKit = { M, T, teamOf, botsOf, routeLabel: (r) => routeLabel(r), edgeHTML: (d) => edgeHTML(d) };
  function newCase(bot, o) {
    const id = 'c' + (40 + D.cases.length);
    D.cases.unshift(Object.assign({ id, bot, status: 'open', opened: now(), workers: 0, proposals: 0, notes: [] }, o));
    return id;
  }
  UI.act('team-deliver', (b) => {
    const t = T(b.dataset.t), c = staged('teams', t.name) || t.cfg;
    UI.form({ caps: `team ${esc(t.name)}`, title: `Route work to ${t.name}`, intro: 'The team\'s routing rules decide who gets it; with no match it goes to the lead.',
      fields: [{ k: 'event', type: 'enum', label: 'Replay a past event', options: [['', 'none: write a request']].concat(SCHEMA.events.map((e) => [e.id, e.label])) }, { k: 'title', type: 'string', mono: false, show: (v) => !v.event }, { k: 'text', type: 'text', label: 'Text (reaches the bot fenced as untrusted data)', show: (v) => !v.event }],
      submit: 'Route', submitIcon: 'send',
      onSubmit: (v) => {
        const ev = SCHEMA.events.find((e) => e.id === v.event);
        if (!ev && !v.title) return 'Give it a title, or pick an event.';
        const hit = ev && matchRule({ cfg: c }, ev);
        const lead = (c.routing || {}).lead, ms = botsOf(t.name);
        if (!hit && !lead && !ms.length) return `Team ${t.name} has no bots to route to.`;
        let to, routed;
        if (hit) { to = hit.to; routed = { by: 'rule', rule: hit.name, team: t.name, to }; }
        else { const assignee = (ms.find((m) => m.name !== lead) || ms[0]).name; to = assignee; routed = hit ? { by: 'rule', rule: hit.name, team: t.name, via: 'lead', lead, to, note: 'the lead triaged and assigned it' } : { by: 'lead', lead: lead || assignee, team: t.name, to }; }
        const id = newCase(to, { title: ev ? ev.label : v.title, requester: ev ? 'event:' + ev.on : 'operator:ops', cls: ev ? 'event' : 'operator', routed: Object.assign({ requester_kept: true }, routed) });
        audit('bot_route', `${t.name} · ${id} → ${to} · ${routed.by}${routed.rule ? ' ' + routed.rule : ''}`); S.save();
        UI.go(`#/tickets/${id}`);
      } });
  });

  /* ---------- bot shell ---------- */
  /** A bot's state in one sentence: what needs you first, then what it's doing; quiet when nothing does. */
  UI.botLine = (m) => { const pend = D.proposals.filter((p) => p.bot === m.name && p.status === 'pending' && UI.live(p)).length, open = D.cases.filter((c) => c.bot === m.name && c.status === 'open').length || m.open || 0;
    const first = pend ? `<b class="loud-word">${pend} ${pend === 1 ? 'approval waits' : 'approvals wait'} for you.</b> ` : '';
    const doing = m.shadow ? 'In shadow mode: it watches and proposes, and never acts.' : m.home === 'live' ? `Working in a live session${open ? ` on ${open} open task${open === 1 ? '' : 's'}` : ''}.` : open ? `Idle, with ${open} open task${open === 1 ? '' : 's'}.` : 'Idle. Nothing open.';
    return first + doing; };
  /** The bot's head, as in the approved mock: avatar, name, what it's for in one line, and what it's doing now. */
  function hero(m, sub) {
    const t = teamOf(m), open = D.cases.filter((c) => c.bot === m.name && c.status === 'open'), cur = open[0];
    const role = (S.staged('bots', m.name) || m.cfg || {}).description || m.role || (t ? `A bot on the ${t.name} team` : 'A bot');
    const now = m.shadow ? UI.statusMark('shadow', 'Shadow: practice mode') : m.home === 'live' ? UI.statusMark('run', 'Running') : UI.statusMark('idle', 'Parked');
    return `${UI.crumbs([['Bots', '#/bots'], t ? [t.name, `#/teams/${t.name}`] : null, sub ? [m.name, `#/bots/${m.name}`] : [m.name], ...(sub || [])])}
      <div class="dl-bothead">${UI.botAv(m.name, 'xl')}<div><h1>${esc(UI.botName(m.name))}${UI.botName(m.name) !== m.name ? ` <span class="bav-id h1-id">${esc(m.name)}</span>` : ''}</h1><p class="dl-lead">${esc(role)}</p>${cur && m.home === 'live' ? `<p class="bot-working">Working on: <a href="#/tickets/${cur.id}">${esc(cur.title)}</a></p>` : ''}</div>${now}
        <button class="icon-btn" data-act="bot-more" data-m="${m.name}" aria-label="More for ${esc(m.name)}">${icon('more')}</button></div>`;
  }
  UI.act('bot-more', (b) => { const m = M(b.dataset.m); UI.menu(b, [['Rotate session…', 'updates', 'm-rotate', `data-m="${m.name}"`], ['Sweep now', 'eye', 'm-sweep', m.proactive ? `data-m="${m.name}"` : UI.off('It has no proactive: block in its definition')], ['Deliver an event…', 'send', 'm-deliver', `data-m="${m.name}"`], ['-'], ['Workers', 'workflows', 'strip-go', `data-href="#/bots/${m.name}/workers"`]]); });
  UI.act('bot-watch-add', (b) => { const name = b.dataset.m, evs = S.view().connectors.flatMap((c) => Object.entries((SCHEMA.connectors[c.use] || {}).events || {}).map(([ev, d]) => [`${c.name}.${ev}`, `${d.label} (${c.name}.${ev})`, c.use]));
    UI.form({ caps: 'triggers:', title: `What should ${name} watch?`, intro: 'Adds a trigger that hands each matching event to the bot (a bot: step). It reads the event, then tells you or proposes what to do.',
      fields: [{ k: 'on', type: 'enum', req: true, label: 'When this happens', options: evs.map(([v, l]) => [v, l]) }, { k: 'repos', type: 'list', label: 'Only these repos (GitHub events)', ph: 'acme/api', show: (v) => /^gh\./.test(v.on || '') }],
      submit: 'Review change', submitIcon: 'check',
      onSubmit: (v) => { const tn = `${name}-${v.on.split('.')[1].replace(/_/g, '-')}`; const y = [`name: ${tn}`, `on: ${v.on}`, ...((v.repos || []).length ? [`filter: { repo: [${v.repos.join(', ')}] }`] : []), 'steps:', '  - id: tell', `    bot: { name: ${name}, title: "{{.title}}" }`].join('\n') + '\n'; D.prefs.returnTo = { href: `#/bots/${name}`, label: name }; UI.staged(S.stage('workflows', tn, y, `trigger ${tn} hands ${v.on} to ${name}`)); } }); });
  /** A bot's page: its header and tabs, then the tab; sub is the path of a page a tab drills into ([[label, href], …, [title]]). */
  function shell(m, tab, body, cls = '', sub) {
    const pc = pending(m.name).length, w = UI.botWatches(m.name), cases = D.cases.filter((c) => c.bot === m.name && c.status === 'open').length;
    const watchNote = !w.triggers.length && !w.sweep ? `<div class="callout wait watch-empty" style="margin:0 0 14px">${UI.statusMark('wait', `<b>${m.name} isn't watching anything yet.</b>`)} <span class="grow">No trigger hands it events and it doesn't check in on its own, so it only answers when someone talks to it: you here, or anyone in a chat it's in.</span><button class="btn sm primary" data-act="bot-watch-add" data-m="${m.name}">${icon('plus')}Choose what it watches</button></div>` : '';
    const T = [['proposals', 'Approvals', pc, true], ['chat', 'Chat'], ['cases', 'Tickets', cases], ['memory', 'Memory'], ['access', 'What it can do'], ['settings', 'Settings']];
    return `<div class="page dl-page${cls ? ' ' + cls : ''}">${hero(m, sub)}
      <nav class="dl-tabs" role="tablist">${T.map(([k, l, n, hot]) => `<a href="#/bots/${m.name}/${k}" role="tab" class="${k === tab ? 'on' : ''}"${k === tab ? ' aria-current="page" aria-selected="true"' : ''}>${l}${n ? ` ${UI.count(n, { hot: !!hot })}` : ''}</a>`).join('')}</nav>
      <div class="dl-tab-body">${watchNote}${body}</div></div>`;
  }
  UI.act('m-rotate', (b) => { const m = M(b.dataset.m); UI.confirm({ title: 'Rotate the home session?', body: `<p style="margin:0 0 8px">conductor asks the current session for a hand-off note, waits for that turn to settle, launches a fresh session, and only then archives the old one. Its token is revoked at once.</p><div class="kv"><dt>current</dt><dd class="mono">${m.session} · ${m.turns} turns</dd><dt>next</dt><dd class="mono">s${+m.session.slice(1) + 1} · resumes from handoff/${m.session}.md + ledger</dd></div>`, ok: 'Rotate' }).then((ok) => { if (!ok) return; const old = m.session; m.session = 's' + (+old.slice(1) + 1); m.turns = 0; m.age = '0m'; m.home = 'live'; audit('bot_rotate', `${m.name} · ${old} → ${m.session}`); S.save(); UI.rerender(); UI.toast('Rotated', `${old} archived · ${m.session} live`, 'ok'); }); });
  UI.act('m-sweep', (b) => { const m = M(b.dataset.m); const id = 'x' + (5 + D.workers.filter((w) => w.parent === 'sweep').length); D.workers.unshift({ id, bot: m.name, parent: 'sweep', case: '—', group: '', status: 'run', verbs: 'observe only', readOnly: true, cls: 'proactive', brief: 'Proactive checks (charter §)', run: 'r-7f29', cost: 0, started: now() }); audit('bot_sweep', `${m.name} · ${id} · read-only`); S.save(); UI.go(`#/bots/${m.name}/workers`); UI.toast(`Sweep ${id} started`, 'read-only · draws on the team pool', 'run'); });
  UI.act('m-deliver', (b) => { const m = M(b.dataset.m); UI.form({ caps: `bot ${esc(m.name)}`, title: `Deliver an event to ${m.name}`, fields: [{ k: 'title', type: 'string', req: true, mono: false, ph: 'Investigate elevated 5xx on /refund' }, { k: 'text', type: 'text', label: 'Text (reaches the bot fenced as untrusted data)' }, { k: 'requester', type: 'enum', req: true, options: [['operator:ops', 'operator:ops (you)'], ['event:manual', 'event']], def: 'operator:ops' }, { k: 'urgent', type: 'bool', on: 'start a worker now' }], submit: 'Deliver', submitIcon: 'send', onSubmit: (v) => { const id = newCase(m.name, { title: v.title, requester: v.requester, cls: v.requester.startsWith('operator') ? 'operator' : 'event', urgent: !!v.urgent, routed: { by: 'direct', to: m.name, requester_kept: true }, notes: v.text ? [{ at: now(), who: v.requester, text: v.text }] : [] }); (D.chats[m.name] = D.chats[m.name] || []).push({ who: 'event', at: now(), case: id, text: v.title + (v.text ? ' · ' + v.text : ''), fenced: true }); audit('bot_deliver', `${m.name} · ${id} · ${v.requester}`); S.save(); UI.go(`#/tickets/${id}`); } }); });

  /** Who asked for a task, in words: you, a PagerDuty incident, someone in chat, its own routine check, another bot. */
  const askedBy = (c) => { const r = String(c.requester || ''); return /^operator:/.test(r) ? (r === 'operator:ops' ? 'you' : r.slice(9)) : /^event:pd/.test(r) ? 'a PagerDuty incident' : /^event:gh/.test(r) ? 'a GitHub event' : /^event:/.test(r) ? 'an event' : /^chat:/.test(r) ? 'someone in chat' : c.cls === 'proactive' ? 'its own routine check' : /^bot:/.test(r) ? `${r.slice(4)} (another bot)` : r || 'someone'; };
  /* ---------- chat ---------- */
  // the thread and the composer are js/chat.js; beside them, what waits on you (the approval in its own words and facts)
  // and what the bot is working on (each task by name, who asked, how long ago)
  // paseo's own view of the home session lives outside the dashboard: say what it is, then open it
  UI.act('chat-paseo', (b) => { const m = M(b.dataset.m);
    UI.modal(`<div class="modal-head"><h2>Open in paseo</h2><button class="icon-btn" data-close aria-label="Close">${icon('x')}</button></div>
      <div class="modal-body"><p style="margin:0 0 10px">${esc(m.name)}’s home session is a paseo agent. paseo shows all of it: every turn, the files it touched and its terminal. The chat here is the conversation only.</p>
      <p class="dl-sub" style="margin:0">It opens in paseo on ${esc(D.daemon.host)}, in a new tab.</p></div>
      <div class="modal-foot"><button class="btn ghost" data-close>Cancel</button><a class="btn primary" href="http://${esc(D.daemon.host)}:6767/" target="_blank" rel="noopener noreferrer" data-close>${icon('external')}Open paseo</a></div>`); });
  function chat(m) {
    const pp = pending(m.name), msgs = D.chats[m.name] || [], open = D.cases.filter((c) => c.bot === m.name && c.status === 'open');
    const paseo = ((D.daemon || {}).paseo || {}).status === 'ok';
    const need = (p) => `<li><a class="cm-side-a" href="#/bots/${m.name}/proposals/${p.id}"><b>${esc(p.title || p.summary)}</b>${UI.factsLine([UI.envFact(p.env), UI.actionFact(p.verb, p.env, p.tier)])}<span class="cm-side-f"><span class="btn sm secondary">Review</span><span class="dl-sub">expires in ${esc(p.expires)}</span></span></a></li>`;
    const task = (c) => `<li><a class="cm-side-a" href="#/tickets/${c.id}"><b>${c.urgent ? UI.mark('wait', { label: 'Urgent' }) + ' ' : ''}${esc(c.title)}</b>${UI.factsLine([{ icon: 'message', value: `Asked by ${askedBy(c)}`, pri: 1, label: 'Who asked' }, { icon: 'clock', value: UI.agoText(c.opened), pri: 2, label: 'Opened' }])}</a></li>`;
    return shell(m, 'chat', `<div class="grid g-side chat-grid">
      <div class="panel chat"><div class="panel-head cm-head"><h3>Home session</h3><span class="dl-sub cm-rule">Your messages reach it between turns, never mid-turn</span>
        <a class="btn sm secondary" href="#/chat/bot/${m.name}">${icon('message')}Open in Chat</a>${paseo ? `<button class="btn sm secondary" data-act="chat-paseo" data-m="${m.name}">${icon('external')}Open in paseo</button>` : '<span class="dl-sub">paseo isn’t reachable, so its own view can’t open</span>'}</div>
        ${UI.chatThread(msgs, m.name)}${UI.chatComposer(m.name)}<div class="cm-drop" aria-hidden="true">${icon('attach')}Drop to attach to your message</div></div>
      <div class="stack cm-side">
        ${pp.length ? `<section class="panel cm-need"><div class="panel-head">${UI.statusMark('ember', 'Waiting for you')}${UI.count(pp.length, { hot: true })}</div><ul class="cm-side-l">${pp.map(need).join('')}</ul></section>` : ''}
        <section class="panel"><div class="panel-head"><h3>Working on</h3><a class="btn sm ghost" style="margin-left:auto" href="#/bots/${m.name}/cases">All tickets</a></div>${open.length ? `<ul class="cm-side-l">${open.map(task).join('')}</ul>` : '<p class="dl-sub cm-side-none">Nothing open.</p>'}</section>
      </div></div>`);
  }

  /* ---------- cases ---------- */
  // a bot's Tickets tab: the tickets assigned to it (js/screens/tickets.js); a ticket opens on its own page
  function cases(m, sub) {
    const c = sub && D.cases.find((x) => x.id === sub);
    if (c) return shell(m, 'cases', UI.ticketBody(c.id, { base: `#/bots/${m.name}/cases` }), 'tk-page', [['Tickets', `#/bots/${m.name}/cases`], [c.title]]);
    if (sub) { setTimeout(() => location.replace(`#/tickets/${sub}`)); return ''; }
    return shell(m, 'cases', UI.botTickets(m));
  }
  document.addEventListener('click', (e) => { const tr = e.target.closest('tr[data-href]'); if (tr && !e.target.closest('a,button,input,label,select,.sel')) UI.go(tr.dataset.href); });
  UI.act('case-close', (b) => { const c = D.cases.find((x) => x.id === b.dataset.c); c.status = c.status === 'open' ? 'closed' : 'open'; if (c.status === 'open') delete c.declined;
    const d = D.delegations.find((x) => x.child === c.id && x.status === 'running');
    if (d && c.status === 'closed') { const last = (c.notes || []).filter((n) => !n.ack && n.text).slice(-1)[0]; d.status = 'done'; d.done = now(); d.result = last ? last.text : `closed by operator ops with no note`; const par = D.cases.find((x) => x.id === d.parent); if (par) (par.notes = par.notes || []).push({ at: now(), who: `bot:${d.to}`, text: `↩ result of ${d.id}: ${d.result}` }); audit('bot_delegate_done', `${d.id} · ${d.to} → ${d.from} · ${d.result.slice(0, 60)}`); } audit('bot_case_' + (c.status === 'open' ? 'reopen' : 'close'), `${b.dataset.m} · ${c.id}`); S.save(); UI.rerender(); UI.refreshChrome(); });
  /* ---------- proposals ---------- */
  const PS = { pending: 'ember', shadow: 'shadow', executed: 'ok', approved: 'ok', refused: 'fail', denied: 'fail', failed: 'fail' };
  const pv = { f: 'all', plan: {} };
  function proposals(m, sub) {
    const list = props(m.name);
    const p = sub && list.find((x) => x.id === sub);
    // on a phone the approval stands on its own (no bot header or tabs above it): the title, why and effect come first
    if (p) return shell(m, 'proposals', proposalDetail(m, p), 'prop-page', [['Approvals', `#/bots/${m.name}/proposals`], [p.title || p.summary]]);
    // what's waiting for you first; what was decided is history, folded to a count; a practice-mode item says so in words
    const waiting = list.filter((x) => x.status === 'pending'), past = list.filter((x) => x.status !== 'pending');
    const word = (x) => x.status === 'shadow' ? 'Practice only: what it would have done in shadow mode (nothing was done)' : `${esc(x.status[0].toUpperCase() + x.status.slice(1))}${x.approver ? ` by ${esc(x.approver)}` : ''}`;
    const row = (x) => { const pend = x.status === 'pending';
      return `<li class="dl-need proposal-row ${x.status}"><a class="dl-need-a" href="#/bots/${m.name}/proposals/${x.id}"><span class="dl-ng">${UI.mark(PS[x.status] || 'idle', { lg: true, label: pend ? 'Needs you' : x.status })}</span>
        <span class="dl-nt"><b>${esc(x.title || x.summary)}</b><span class="dl-why">${pend ? UI.taskWords(x.ask || x.why || x.summary) : word(x)}</span>${UI.factsLine([UI.envFact(x.env), ...UI.verbFacts(x.verb, x.options), UI.actionFact(x.verb, x.env, x.tier)])}</span>
        <span class="dl-age">${UI.agoText(x.at)}</span><span class="dl-na">${pend ? '<span class="btn sm secondary">Review</span>' : ''}</span></a></li>`; };
    return shell(m, 'proposals', `<section class="dl-card"><header class="dl-ch"><h2>Waiting for you</h2>${waiting.length ? UI.count(waiting.length, { hot: true }) : ''}</header>${waiting.length ? `<ul class="dl-list">${waiting.map(row).join('')}</ul>` : `<p class="dl-sub">Nothing waits for you. When ${esc(m.name)} wants to do something it has to ask about, it shows up here and in Needs you.</p>`}</section>
      ${past.length ? `<details class="dl-card dl-files-d past-d"${pv.f === 'decided' ? ' open' : ''}><summary class="panel-head"><h3>Past decisions</h3><span class="muted small">${past.length}</span></summary><ul class="dl-list">${past.map(row).join('')}</ul></details>` : ''}`);
  }
  UI.act('prop-f', (b) => { pv.f = b.dataset.v; UI.rerender(); });
  /* a Terraform plan in Terraform's own conventions, on Daylight's diff tokens: + create (the diff's add colour), - destroy
     (its delete colour), ~ update (amber), -/+ or +/- replace (the strongest: delete colour, bold, a bar), <= read (a muted
     blue); resource headers emphasised; "(known after apply)" and hidden lines muted. The symbols stay, so colour is never
     the only cue. A jump list of the resources, grouped by action, sits on top. */
  const PLAN_ACT = [['replace', /must be replaced|will be replaced/], ['destroy', /will be destroyed/], ['create', /will be created/], ['update', /will be updated in-place/], ['read', /will be read during apply/]];
  // one vocabulary for a plan: the tiles, the list under them and the plan's own groups say the same verbs
  const PLAN_WORD = { create: 'Adds', update: 'Changes', replace: 'Replaces', destroy: 'Destroys', read: 'Reads' };
  function planHTML(t) {
    const lines = String(t).split('\n'), res = [], ind = (raw) => (raw.trim() ? ` style="--i:${raw.match(/^\s*/)[0].length}"` : '');
    const body = lines.map((raw, i) => { const l = esc(raw); let m;
      if ((m = raw.match(/^\s*# (\S+) (.*)$/)) && /will be|must be/.test(raw)) { const act = (PLAN_ACT.find(([, re]) => re.test(raw)) || ['update'])[0]; res.push([act, m[1], i]); return `<span class="pl pl-h pl-${act}" id="plan-${i}"${ind(raw)}>${l}</span>`; }
      const kind = /^\s*(-\/\+|\+\/-)\s/.test(raw) ? 'replace' : /^\s*\+\s/.test(raw) ? 'create' : /^\s*-\s/.test(raw) ? 'destroy' : /^\s*~\s/.test(raw) ? 'update' : /^\s*<=\s/.test(raw) ? 'read' : /^\s*#|unchanged|hidden/.test(raw) ? 'muted' : /^Plan:/.test(raw) ? 'sum' : '';
      const withMuted = l.replace(/\(known after apply\)/g, '<span class="pl-kaa">(known after apply)</span>').replace(/# forces replacement/g, '<b class="pl-force"># forces replacement</b>');
      return `<span class="pl${kind ? ' pl-' + kind : ''}"${ind(raw)}>${withMuted || ' '}</span>`; }).join('');
    const groups = Object.keys(PLAN_WORD).map((k) => [k, res.filter(([a]) => a === k)]).filter(([, xs]) => xs.length);
    const jump = groups.length ? `<div class="plan-jump">${groups.map(([k, xs]) => `<span class="pj pj-${k}"><b>${PLAN_WORD[k]}</b> ${xs.map(([, r, i]) => `<a href="#" data-act="plan-jump" data-l="plan-${i}" class="mono">${esc(r)}</a>`).join(', ')}</span>`).join('')}</div>` : '';
    const count = (k) => res.filter(([a]) => a === k).length;
    return { jump, body, counts: { create: count('create'), update: count('update'), replace: count('replace'), destroy: count('destroy') } }; }
  /** Who asked, in words: a non-operator requester gets a banner, never just a colored chip. */
  UI.requesterBanner = (p) => {
    const r = p.requester || '';
    if (/^chat:/.test(r) || p.cls === 'user') return `<div class="req-banner untrusted">${icon('warn')}<div><b>Requested by a chat user who isn't one of your operators</b> (<span class="mono">${esc(r)}</span>). The bot is acting on their request, so it runs with no more trust than they have. Check this is something you want.</div></div>`;
    if (/^event:/.test(r) || p.cls === 'event') return `<div class="req-banner event">${icon('live')}<div><b>Started by an event, not a person</b> (<span class="mono">${esc(r)}</span>). No human asked for this action; the bot proposed it while handling the event.</div></div>`;
    if (p.cls === 'proactive') return `<div class="req-banner event">${icon('clock')}<div><b>The bot's own sweep</b> (<span class="mono">${esc(r)}</span>): nobody asked; it found this on a scheduled check.</div></div>`;
    if (p.cls === 'peer') return `<div class="req-banner untrusted">${icon('bots')}<div><b>Asked for by another bot</b> (<span class="mono">${esc(r)}</span>). It carries the original requester's trust, no more.</div></div>`;
    return `<div class="req-banner op">${icon('users')}<div><b>Requested by an operator</b> (<span class="mono">${esc(r)}</span>).</div></div>`;
  };
  /** The whole signed payload: every option, untruncated; long text in a scroll box. */
  UI.payloadHTML = (p) => `<div class="payload"><div class="pl-row"><span class="pl-k">verb</span><span class="pl-v mono">${esc(p.verb)}</span></div>${Object.entries(p.options || {}).map(([k, v]) => `<div class="pl-row"><span class="pl-k mono">${esc(k)}</span>${String(v).length > 80 || /\n/.test(String(v)) ? `<div class="pl-long">${esc(v)}</div>` : `<span class="pl-v mono">${esc(typeof v === 'object' ? JSON.stringify(v) : v)}</span>`}</div>`).join('') || '<div class="pl-row muted small">no options</div>'}</div>`;
  /** What a proposal is, and its risk: from its tier, its verb, and the environment it lands in. */
  UI.propObj = (p) => (p.kind === 'terraform' ? 'terraform' : UI.actionOf(p.verb) === 'money' ? 'spend' : UI.actionOf(p.verb) === 'post' ? 'message' : UI.actionOf(p.verb) === 'push' ? 'pr' : 'approval');
  UI.propKind = (p) => (p.kind === 'terraform' ? 'Terraform apply' : { spend: 'Purchase', message: 'Post', pr: 'Code change', approval: 'Approval' }[UI.propObj(p)]);
  UI.propRisk = (p) => UI.riskOf({ tier: p.tier, verb: p.verb, env: p.env, destroys: p.tf ? p.tf.destroy : 0 });
  /** The approval, as in the approved mock (approvalPage): a needs-you kicker, the plain title, what it asks, why,
      what changes, where / effect / undo, who started it, the decision, and the technical details folded. */
  function proposalDetail(m, p) {
    const tf = p.tf, pend = p.status === 'pending', t = teamOf(m);
    const ask = p.ask || (tf ? `${m.name} wants to run terraform apply on ${p.env || tf.workspace}.` : `${m.name} wants to ${esc(String(p.summary || p.verb))}.`);
    const pc = tf ? planHTML(D.tfPlan || '').counts : null;
    const changes = tf ? `<div class="dl-changes">${[[PLAN_WORD.create, pc.create], [PLAN_WORD.update, pc.update], [PLAN_WORD.replace, pc.replace, 'wait'], [PLAN_WORD.destroy, pc.destroy, 'fail']].map(([l, n, st]) => `<div${n && st ? ` data-s="${st}"` : ''}><b>${n}</b><span>${l}</span></div>`).join('')}
      ${tf.replaces.length ? `<p class="dl-chg-note">${UI.mark('wait')}<span><b>Replaces ${tf.replaces.length} ${tf.replaces.length === 1 ? 'server' : 'resources'}</b> (${tf.replaces.map(esc).join(', ')}). Stateful things such as databases are never replaced without a separate rule.</span></p>` : ''}</div>`
      : `<div class="dl-pwhy"><span class="dl-lbl">Exactly what runs</span>${UI.payloadHTML(p)}</div>`;
    const where = [['Where', p.env ? `${p.env[0].toUpperCase()}${p.env.slice(1)}${tf ? ` · workspace ${esc(tf.workspace)}` : ''}` : tf ? `workspace ${esc(tf.workspace)}` : ''], ['Effect', esc(p.impact || '')], ['Undo', esc(p.reversible ? p.reversible.replace(/^./, (c) => c.toUpperCase()) : 'conductor can’t undo this')]].filter(([, v]) => v);
    const origin = /^event:/.test(p.requester || '') || p.cls === 'event' ? `${icon('zap')}Started by an event (<span class="mono">${esc(String(p.requester).replace(/^event:/, ''))}</span>), not a person.`
      : /^chat:/.test(p.requester || '') || p.cls === 'user' ? `${icon('warn')}Asked for in chat by someone who isn’t one of your operators (<span class="mono">${esc(p.requester)}</span>); it runs with no more trust than they have.`
      : p.cls === 'proactive' ? `${icon('clock')}Found on the bot’s own routine check; nobody asked.` : p.cls === 'peer' ? `${icon('bots')}Asked for by another bot (<span class="mono">${esc(p.requester)}</span>), carrying the original requester’s trust.` : `${icon('users')}Asked for by an operator (<span class="mono">${esc(p.requester || '')}</span>).`;
    const techFacts = `<dl class="dl-facts prop-tech-facts"><dt>Proposal</dt><dd><span class="mono">${p.id}</span> · tier <span class="mono">${esc(p.tier)}</span></dd><dt>Verb</dt><dd class="mono">${esc(p.verb)}</dd>
      ${tf ? `<dt>Exact command</dt><dd class="mono">terraform apply · repo ${esc(tf.repo)} · dir ${esc(tf.dir)} · workspace ${esc(tf.workspace)}${(tf.var_files || []).length ? ` · var files ${tf.var_files.map(esc).join(', ')}` : ''}</dd><dt>Commit</dt><dd class="mono">${tf.commit}</dd><dt>Plan sha256</dt><dd class="mono">${tf.plan_sha}</dd>` : ''}
      <dt>Ticket</dt><dd><a class="mono dl-link" href="#/tickets/${p.case}">${p.case}</a></dd><dt>Asked for by</dt><dd class="mono">${esc(p.requester || '—')}${p.cls ? ` (${esc(p.cls)})` : ''}</dd><dt>Digest</dt><dd class="mono">${p.digest}</dd><dt>Signed by</dt><dd>${p.approver ? esc(p.approver) : 'not yet: your passkey signs this digest'}</dd></dl>`;
    const decided = !pend && p.status !== 'shadow' ? `<p class="dl-decided">${UI.statusMark(PS[p.status] || 'idle', p.status.startsWith('approved') || p.status === 'executed' ? 'Approved' : p.status[0].toUpperCase() + p.status.slice(1))}${p.approver ? ` by ${esc(p.approver)}` : ''}${p.reason ? `: “${esc(p.reason)}”` : ''}. ${p.status.startsWith('approved') || p.status === 'executed' ? 'conductor applied exactly what was signed.' : 'The bot was told.'}</p>` : p.status === 'shadow' ? '<p class="dl-decided dl-sub">Practice only (shadow mode): what the bot would have done. It can’t be approved.</p>' : '';
    // one column, in the order the decision needs and always in view: what will change (for a plan, the counts and the
    // resources by action, in Terraform's colours; otherwise exactly what it sends), why, where and what it does and how
    // to undo it, who or what asked. The full plan opens in place; the integrity data (ids, hashes, the digest the passkey
    // signs) is for audit: one quiet line at the end, folded. No tabs: nothing the decision depends on is ever hidden.
    const pl = tf ? planHTML(D.tfPlan || '') : null;
    const what = tf ? `<div class="dl-pwhy prop-what"><span class="dl-lbl">What will change</span>${changes}${pl.jump}</div>` : `<div class="dl-pwhy"><span class="dl-lbl">Exactly what it sends</span>${UI.payloadHTML(p)}</div>`;
    const plan = tf ? `<details class="dl-tech-d prop-plan-d"${pv.plan[p.id] ? ' open' : ''} data-on-toggle="prop-plan-t" data-p="${p.id}"><summary>${icon('chev')}The full plan <span class="dl-sub">as tf-plan ran it, with the read role</span></summary><pre class="dl-code prop-plan">${pl.body}</pre></details>` : '';
    const audit = `<details class="dl-audit-d"><summary>${icon('chev')}For audit: ids, hashes and the digest your passkey signs</summary>${techFacts}</details>`;
    const body = `${what}
      ${p.why ? `<div class="dl-pwhy"><span class="dl-lbl">Why</span><p>${UI.taskWords(p.why, true)}</p></div>` : ''}
      ${where.length ? `<dl class="dl-where">${where.map(([k, v]) => `<dt>${k}</dt><dd>${v}</dd>`).join('')}</dl>` : ''}
      <p class="dl-origin">${origin}</p>${pend ? '' : decided}${plan}<p class="dl-sub prop-can-link"><a class="dl-link" href="#/bots/${m.name}/access">What ${esc(m.name)} may do on its own, and what it asks first →</a></p>${audit}`;
    const card = `<article class="dl-card dl-proposal">${pend ? `<div class="dl-kick">${UI.mark('ember')}Needs you · <span class="nowrap">asked ${UI.agoText(p.at)}</span>${p.expires ? ` · <span class="nowrap">expires in ${UI.humanSpan(p.expires)}</span>` : ''}</div>` : `<div class="dl-kick dl-sub">${UI.agoText(p.at)}</div>`}
      <h2 class="dl-ptitle">${esc(p.title || p.summary)}</h2><p class="dl-ask">${ask}</p>
      <div class="prop-body">${body}</div>
      ${pend ? `<div class="dl-decide prop-decide"><button class="btn lg primary" data-act="p-approve" data-p="${p.id}" data-d="approve">${icon('passkey')}Approve with passkey</button><button class="btn lg secondary" data-act="p-approve" data-p="${p.id}" data-d="deny">Deny…</button><span class="hint">${(() => { const ops = S.effective(m).operators.v || []; return ops.length ? `Only ${ops.map(esc).join(' or ')} can approve.` : 'Any operator can approve.'; })()}</span></div>` : p.status === 'shadow' ? decided : ''}</article>`;
    return `<div class="prop-single">${card}</div>`;
  }
  const decidedWord = (p) => (p.status === 'executed' || String(p.status).startsWith('approved') ? 'Approved' : p.status === 'shadow' ? 'Shadow: not for approval' : p.status[0].toUpperCase() + p.status.slice(1));
  // a resource in the summary opens the full plan at its block
  UI.act('plan-jump', (b) => { const el = document.getElementById(b.dataset.l); if (!el) return; const d = el.closest('details'); if (d && !d.open) { d.open = true; pv.plan[d.dataset.p] = true; } setTimeout(() => { el.scrollIntoView({ block: 'center' }); el.classList.add('flash-target'); setTimeout(() => el.classList.remove('flash-target'), 1600); }); });
  UI.act('prop-plan-t', (d) => { pv.plan[d.dataset.p] = d.open; });
  UI.act('p-approve', (b) => { const p = D.proposals.find((x) => x.id === b.dataset.p); UI.approve(p, b.dataset.d, () => { S.save(); UI.rerender(); }); });

  /* ---------- memory ---------- */
  const memOf = (m) => { if (m.name === 'platform-sre') return D.memory; D.memories = D.memories || {}; return (D.memories[m.name] = D.memories[m.name] || { files: { 'knowledge/README.md': `# ${m.name}\n\nWhat ${m.name} has learned.\n` }, log: [], open: 'knowledge/README.md' }); };
  const mv = { edit: false };
  function memory(m) {
    return shell(m, 'memory', `<div class="callout small" style="margin-bottom:12px">${esc(m.name)} writes its own memory as it works. You can read, correct, pin and delete what it knows; every save is one commit authored by you.</div>${UI.repoView('bot:' + m.name, { title: `${m.name} · memory repo` })}`);
  }
  const sha = () => Math.random().toString(16).slice(2, 9);
  document.addEventListener('input', (e) => { if (e.target.id !== 'mem-text') return; const b = UI.$('#mem-commit'); const m = M(b.dataset.m), mem = memOf(m); const same = e.target.value === mem.files[mem.open || Object.keys(mem.files)[0]]; if (same) { b.setAttribute('aria-disabled', 'true'); b.dataset.reason = b.dataset.tip = 'Nothing changed yet: edit the file first'; } else { b.removeAttribute('aria-disabled'); delete b.dataset.reason; delete b.dataset.tip; } });
  UI.act('mem-open', (a) => { const mem = memOf(M(a.dataset.m)); mem.open = a.dataset.f; mv.edit = false; S.save(); UI.rerender(); });
  UI.act('mem-mode', (b) => { mv.edit = b.dataset.v === 'edit'; UI.rerender(); });
  UI.act('mem-commit', (b) => { const m = M(b.dataset.m), mem = memOf(m), f = mem.open || Object.keys(mem.files)[0], txt = UI.$('#mem-text').value; if (txt === mem.files[f]) { UI.toast('No change to commit', '', 'idle'); return; } const prev = mem.files[f]; mem.files[f] = txt; const s = sha(); mem.log.unshift({ sha: s, msg: UI.$('#mem-msg').value || 'operator edit', author: 'ops (dashboard)', trailers: 'Bot-Role: operator · Edited-Via: dashboard', at: now(), file: f, prev }); mv.edit = false; audit('memory_commit', `${m.name} · ${f} · ${s}`); S.save(); UI.rerender(); UI.toast(`Committed ${s}`, 'the bot sees it on its next read', 'ok'); });
  UI.act('mem-new', (b) => { const m = M(b.dataset.m); UI.form({ caps: 'memory repo', title: 'New file', fields: [{ k: 'path', type: 'string', req: true, ph: 'runbooks/new.md', pattern: /^(knowledge|runbooks|episodes|handoff)\/[\w./-]+\.md$/, patternMsg: 'Under knowledge/, runbooks/, episodes/ or handoff/, ending .md' }], submit: 'Create', onSubmit: (v) => { const mem = memOf(m); if (mem.files[v.path]) return 'That file exists.'; mem.files[v.path] = `# ${v.path.split('/').pop().replace('.md', '')}\n`; mem.open = v.path; mv.edit = true; S.save(); UI.rerender(); } }); });
  UI.act('mem-revert', (b) => { const m = M(b.dataset.m), mem = memOf(m), c = mem.log.find((x) => x.sha === b.dataset.sha); UI.confirm({ title: `Revert ${c.sha}?`, body: `Adds a commit that undoes “${esc(c.msg)}”. History is kept.`, ok: 'Revert' }).then((ok) => { if (!ok) return; if (c.prev !== undefined && c.file) mem.files[c.file] = c.prev; const s = sha(); mem.log.unshift({ sha: s, msg: `Revert "${c.msg}"`, author: 'ops (dashboard)', trailers: 'Bot-Role: operator · Edited-Via: dashboard · Reverts: ' + c.sha, at: now() }); audit('memory_revert', `${m.name} · ${c.sha} → ${s}`); S.save(); UI.rerender(); }); });
  UI.act('mem-consolidate', (b) => { const m = M(b.dataset.m), mem = memOf(m), s = sha(); mem.log.unshift({ sha: s, msg: 'consolidate: merge episodes into knowledge', author: m.name, trailers: 'Bot-Role: consolidate · Requested-By: operator:ops', at: now() }); audit('memory_consolidate', `${m.name} · ${s}`); S.save(); UI.rerender(); UI.toast('Consolidation ran', `commit ${s}`, 'ok'); });
  UI.act('mem-promote', (b) => { const m = M(b.dataset.m), mem = memOf(m), f = mem.open, t = teamOf(m), kn = D.teamKnowledge[t.name]; const id = 'k' + (kn.proposals.length + 5); kn.proposals.unshift({ id, bot: m.name, from: f, to: f.split('/').pop(), summary: `Promote ${f.split('/').pop()} from ${m.name}`, status: (staged('teams', t.name) || t.cfg).knowledge.promote === 'auto' ? 'approved' : 'pending', at: now(), diff: (mem.files[f] || '').split('\n').map((l) => '+' + l).join('\n') }); audit('team_knowledge_proposed', `${t.name} · ${id} · ${m.name}: ${f}`); S.save(); UI.go(`#/teams/${t.name}/knowledge`); });

  /* ---------- workers ---------- */
  // a bot's workers in words: what each is doing, for which ticket, whether it may change anything and with which
  // services; the worker ids, groups, trust class and verb lists are folded "for audit"
  const svcOf = (verbs) => [...new Set(String(verbs || '').split(/,\s*/).filter((v) => v.includes('.')).map((v) => v.split('.')[0]))].map((x) => { const c = D.connectors.find((k) => k.name === x); return c ? ((SCHEMA.connectors[c.use] || {}).label || c.use) : x; });
  function workers(m) {
    const ws = D.workers.filter((w) => (w.bot || 'platform-sre') === m.name);
    const runOf = (w) => (w.status === 'run' ? 'r-7f29' : D.runs.find((r) => r.id === w.run) ? w.run : 'r-7e52');
    const row = (w) => { const t = (D.cases || []).find((c) => c.id === w.case), s = w.status === 'stopped' ? 'fail' : w.status === 'run' ? 'run' : w.status === 'ok' ? 'ok' : 'idle';
      const pm = String(w.note || '').match(/proposal (p\d+)/), p = pm && D.proposals.find((x) => x.id === pm[1]);
      return `<li class="dl-need wk-row" data-w="${w.id}"><div class="dl-need-a"><span class="dl-ng">${UI.mark(s, { lg: true, label: s === 'run' ? 'Running' : s === 'ok' ? 'Finished' : s === 'fail' ? 'Stopped' : 'Idle' })}</span>
        <span class="dl-nt"><b>${esc(w.brief[0].toUpperCase() + w.brief.slice(1))}</b><span class="dl-why">${w.status === 'run' ? 'Working' : w.status === 'stopped' ? 'Stopped' : 'Finished'}${t ? ` · for the ticket <a class="dl-link" href="#/tickets/${t.id}">${esc(t.title)}</a>` : ''}${p ? ` · asked you to approve <a class="dl-link" href="#/bots/${m.name}/proposals/${p.id}">${esc(p.title || 'a change')}</a>` : ''}</span>
        ${UI.factsLine([{ icon: w.readOnly ? 'act-read' : 'act-change', value: w.readOnly ? 'Read-only' : 'May change things (it asks first)', pri: 1, label: 'What it may do' }, ...svcOf(w.verbs).map((x) => ({ icon: 'obj-connector', value: x, pri: 2, label: 'Uses' })), w.parent === 'sweep' ? { icon: 'eye', value: 'A routine check', pri: 2 } : null])}</span>
        <span class="dl-age">${UI.agoText(w.started)}</span><span class="dl-na"><a class="btn sm secondary" href="#/runs/${runOf(w)}">See its run</a>${w.status === 'run' ? `<button class="btn sm secondary" data-act="w-stop" data-w="${w.id}">Stop</button>` : ''}</span></div></li>`; };
    const cur = ws.filter((w) => w.parent === `home ${m.session}`), sweep = ws.filter((w) => w.parent === 'sweep'), past = ws.filter((w) => !cur.includes(w) && !sweep.includes(w));
    const sec = (title, sub, xs) => (xs.length ? `<section class="dl-card"><header class="dl-ch"><h2>${title}</h2>${UI.count(xs.length)}</header><p class="dl-sub" style="margin:0 0 8px">${sub}</p><ul class="dl-list">${xs.map(row).join('')}</ul></section>` : '');
    return shell(m, 'workers', `<div class="stack">
      ${sec('From its current session', `Helpers ${esc(m.name)} started for its work now. Each may do what ${esc(m.name)} may, or less, and never trusts more than whoever asked.`, cur)}
      ${sec('Routine checks', 'The checks it runs on its own schedule. Read-only.', sweep)}
      ${sec('From earlier sessions', 'Kept for the record.', past)}
      ${ws.length ? '' : `<section class="dl-card"><p class="dl-sub">${esc(m.name)} hasn’t started any helpers yet.</p></section>`}
<details class="panel tech env-tech" ><summary class="panel-head"><h3>Effective environment for its workers</h3><span class="muted small">runtime ${esc(S.getPath(m.cfg || {}, 'worker.runtime') || 'paseo')}, in the sandbox</span></summary><div class="panel-body">${UI.effectiveEnvHTML({ runtime: S.getPath(m.cfg || {}, 'worker.runtime') || 'paseo', sandboxed: true })}</div></details>
      <details class="dl-card dl-audit-d"><summary>${icon('chev')}For audit: worker ids, groups, trust and verbs</summary><dl class="dl-facts">${ws.map((w) => `<dt class="mono">${w.id}</dt><dd><span class="mono">${esc(w.parent)}${w.group ? ' · group ' + esc(w.group) : ''} · ticket ${w.case} · trust: ${esc(w.cls)} · verbs: ${esc(w.verbs)}</span></dd>`).join('')}</dl></details>
    </div>`);
  }
  UI.act('w-stop', (b) => UI.confirm({ title: 'Stop this helper?', body: 'Its run is cancelled, with a moment to save its work. The bot that started it is told.', ok: 'Stop worker', danger: true }).then((ok) => { if (!ok) return; const w = D.workers.find((x) => x.id === b.dataset.w); w.status = 'stopped'; audit('bot_worker_stop', `${w.id} · case ${w.case}`); S.save(); UI.rerender(); }));

  /* ---------- Safety: every bot, in words ---------- */
  UI.route('safety', () => {
    if (!D.prefs.seenSafety) { D.prefs.seenSafety = true; S.save(); }
    const rows = D.bots.map((m) => ({ m, x: UI.botCan(m) }));
    const own = rows.reduce((a, r) => a + r.x.own.filter((i) => i.t !== 'observe').length, 0);
    const paused = D.daemon.paused;
    // the emergency stop: findable, in plain words, with what it does and how to undo it
    const stop = `<section class="dl-card safety-stop${paused ? ' need' : ''}" id="pause"><header class="dl-ch"><h2>${paused ? 'All automation is paused' : 'Pause all automation'}</h2></header>
      <p>${paused ? 'Nothing new starts: workflows don’t run and bots don’t pick up new work. Runs that were already going have finished or are finishing.' : 'An emergency stop. Nothing new starts until you resume: no workflow runs and no bot picks up new work. Runs already going finish; nothing is lost from your config.'}</p>
      <p class="dl-sub">${paused ? 'Resuming starts dispatching again straight away.' : 'Resume here (or from Needs you, where it waits while paused). Events that arrive while paused may not be kept.'}</p>
      <div class="dl-actions"><button class="btn ${paused ? 'primary' : 'secondary danger-text'}" data-act="pause">${icon(paused ? 'play' : 'pause')}${paused ? 'Resume automation' : 'Pause all automation…'}</button></div></section>`;
    return `<div class="page dl-page">${UI.systemTabs('safety')}<div class="dl-phead"><div><h1>Safety</h1><p class="dl-lead"><b>Nothing risky happens without you.</b> Agents run sandboxed, and every bot does only what you allowed: some things on its own, some only after asking you, some never.</p></div></div>
      ${paused ? stop : ''}${UI.sandboxHTML()}
      <div class="dl-phead" style="margin-top:28px"><div><h2>What each bot may do</h2><p class="dl-sub" style="margin-top:4px">${own ? `${own} action${own > 1 ? 's' : ''} beyond reading run${own > 1 ? '' : 's'} without asking.` : 'Nothing beyond reading runs without asking.'}</p></div></div>
      <div class="stack" style="margin-top:14px">${rows.map(({ m }) => `<div class="panel"><div class="panel-head">${UI.sigil(m, 'sm')}<h3><a class="link-plain" href="#/bots/${m.name}/access">${esc(m.name)}</a></h3><span class="muted small">${teamOf(m) ? `team ${esc(teamOf(m).name)} · runs alone up to ${S.ceilingOf(m)}` : 'solo'}</span><div class="tools"><a class="btn sm ghost" href="#/bots/${m.name}/access">${icon('edit')}Change</a></div></div><div class="panel-body">${UI.botCanHTML(m, true)}</div></div>`).join('')}</div>${paused ? '' : `<div style="margin-top:28px">${stop}</div>`}</div>`;
  }, { nav: 'system' });

  /* ---------- access (config edits through the pipeline) ---------- */
  /** What a bot can do, in words: on its own / asks you first / never (the effective tier of every granted verb). */
  /** What a bot does with a verb, in one place: 'never' (refused), 'own' (runs without asking: read-only, or reversible
      and promoted under a ceiling that allows it) or 'asks' (waits for your passkey). Every statement about a bot's
      reach (the three groups, the table's column, Safety's count) comes from this. */
  UI.verbMode = (m, v) => { const c = (S.staged('bots', m.name) || m.cfg || {}), t = S.tierFor({ cfg: c, name: m.name }, v).tier, promote = S.getPath(c, 'autonomy.promote') || [], ceiling = S.ceilingOf(Object.assign({}, m, { cfg: c }));
    return t === 'never' ? 'never' : t === 'observe' ? 'own' : t === 'reversible' && promote.includes(v) && ceiling !== 'observe' ? 'own' : 'asks'; };
  // a verb pattern in plain words (the pattern itself is on hover): gh.get_* → "read GitHub", gh.delete_* → "delete in GitHub"
  const PATWORD = [[/^(get|list|read|search|fetch|describe)_?$/, 'read'], [/^(delete|remove|destroy)_?$/, 'delete in'], [/^(create|add|post|comment)_?$/, 'add to'], [/^(update|edit|set)_?$/, 'change things in']];
  const patternWords = (v) => { const [inst, rest = ''] = v.replace(/\*$/, '').split('.'), cn = S.view().connectors.find((x) => x.name === inst), svc = cn ? ((SCHEMA.connectors[(cn.cfg && cn.cfg.use || cn.use || '').split('@')[0]] || {}).label || inst) : inst, w = PATWORD.find(([re]) => re.test(rest)); return w ? `${w[1]} ${svc}` : `${svc}: anything starting “${rest}”`; };
  UI.botCan = (m) => {
    const c = (S.staged('bots', m.name) || m.cfg || {}), sv = S.getPath(c, 'skill.verbs'), verbs = Array.isArray(sv) ? sv : sv ? Object.keys(sv) : [];
    const promote = S.getPath(c, 'autonomy.promote') || [], shadow = S.getPath(c, 'autonomy.shadow') || S.getPath((teamOf(m) || { cfg: {} }).cfg, 'autonomy.shadow');
    const out = { own: [], asks: [], never: [], shadow: !!shadow };
    const label = (v) => { const d = SCHEMA.verb(v.replace(/\*$/, '')) || null; return d ? `${d.label.replace(/^./, (x) => x.toLowerCase())} (${v})` : v.endsWith('*') ? `${patternWords(v)} (${v})` : v; };
    verbs.forEach((v) => { const t = S.tierFor({ cfg: c, name: m.name }, v).tier, mode = UI.verbMode(m, v); const it = { v, t, text: label(v) }; (mode === 'never' ? out.never : mode === 'own' ? out.own : out.asks).push(it); }); void promote;
    return out;
  };
  /** What a bot can do, as the approved mock draws it: three plain groups (on its own · asks you first · never), each
      headed by its mark; items in plain words, where they land as a quiet word. The tier names live in the table below. */
  UI.botCanHTML = (m, compact) => { const x = UI.botCan(m), plain = (i) => esc(String(i.text).replace(/ \([^)]*\)$/, '').replace(/^./, (c) => c.toUpperCase()));
    const grp = (h, s2, items, empty) => `<div class="cang" data-s="${s2}"><h3>${UI.mark(s2)}${h}</h3><ul>${items.map((i) => { const env = ((S.view().connectors.find((cc) => i.v.startsWith(cc.name + '.')) || {}).env); return `<li title="${esc(i.v)}">${plain(i)}${env ? ` <span class="dl-sub">· ${esc(env)}</span>` : ''}</li>`; }).join('') || `<li class="dl-sub">${empty}</li>`}</ul></div>`;
    return `<div class="dl-can${compact ? ' compact' : ''}">${x.shadow ? '<p class="dl-sub">In shadow mode: even what it does on its own is only recorded, never done.</p>' : ''}<div class="dl-can-grid">${grp('Does on its own', 'ok', x.own, 'Nothing: it asks first')}${grp('Asks you first', 'ember', x.asks, 'Nothing needs your approval')}${grp('Never', 'fail', x.never, 'Nothing is refused outright')}</div></div>`; };
  const TIERW = { observe: 'reads only', reversible: 'can be undone', propose: 'asks first', commit_money: 'spends money', never: 'never' };
  function access(m) {
    const c = staged('bots', m.name), promote = S.getPath(c, 'autonomy.promote') || [];
    const sv = S.getPath(c, 'skill.verbs'), vlist = Array.isArray(sv) ? sv : sv ? Object.keys(sv) : [];
    const team = teamOf(m), ceiling = S.ceilingOf(Object.assign({}, m, { cfg: c }));
    const verbs = vlist.map((v) => { const tf = S.tierFor(Object.assign({}, m, { cfg: c }), v); return { v, tier: tf.own || 'propose', eff: tf.tier, teamTier: tf.team }; });
    const creds = D.access.credentials.filter((x) => ((S.staged('credentials', x.name) || x.cfg || {}).identities || []).includes('bot:' + m.name));
    const logins = D.access.logins.filter((l) => l.identity === 'bot:' + m.name);
    const eff = S.effective(m);
    const stagedM = S.find('bots', m.name);
    // the plain list is the editor: each thing it may do sits in its group (on its own · asks you first · never) and its
    // menu moves it or removes it; "Allow something new" adds one. The action's own name (gh.comment) is detail, shown
    // small under its words. Each change waits for your review.
    const x = UI.botCan(Object.assign({}, m, { cfg: c })), cap = (t) => esc(String(t).replace(/ \([^)]*\)$/, '').replace(/^./, (ch) => ch.toUpperCase()));
    const item = (it) => `<li class="can-item"><div><span>${cap(it.text)}</span><span class="can-id mono">${esc(it.v)}</span></div><button class="icon-btn" data-act="can-more" data-m="${m.name}" data-v="${esc(it.v)}" aria-label="Change what ${esc(m.name)} does with ${esc(it.v)}">${icon('more')}</button></li>`;
    const grp = (h, s2, items, empty) => `<div class="cang" data-s="${s2}"><h3>${UI.mark(s2)}${h}</h3><ul>${items.map(item).join('') || `<li class="dl-sub">${empty}</li>`}</ul></div>`;
    return shell(m, 'access', `<section class="dl-card dl-can can-edit" style="margin-top:20px"><header class="dl-ch"><h2>What ${esc(m.name)} can do</h2><span class="dl-sp"></span><button class="btn sm secondary" data-act="acc-addverb" data-m="${m.name}">${icon('plus')}Allow something new…</button></header>
        ${x.shadow ? '<p class="dl-sub">It’s in practice mode: even what it does on its own is only recorded, never done.</p>' : ''}
        <div class="dl-can-grid">${grp('Does on its own', 'ok', x.own, 'Nothing: it asks first')}${grp('Asks you first', 'ember', x.asks, 'Nothing needs your approval')}${grp('Never', 'fail', x.never, 'Nothing is refused outright')}</div>
        ${m.spend != null ? `<p class="small sentence" style="margin:12px 0 0" data-cloud-spend>${icon('dollar')}Cloud spend it caused this period: <b>${usd(m.spend)}</b>, within its limit for spending money.</p>` : ''}
        <p class="dl-sub" style="margin:12px 0 0">Use an item’s menu to move it or take it away; each change waits for your review${team ? `, and on the ${esc(team.name)} team a bot does on its own only things that ${ceiling === 'observe' ? 'just read' : 'can be undone'}` : ''}.${stagedM ? ` <a class="dl-link" href="#/config/apply">Review the change</a>` : ''}</p></section>
      <div class="acc-stack">
        <div class="stack">
          <div class="panel"><div class="panel-head"><h3>Sites it can sign in to</h3><div class="tools"><button class="btn sm" data-act="cred-bind" data-m="${m.name}">${icon('plus')}Let it use a login</button></div></div><div class="list">${creds.map((x) => { const cf = S.staged('credentials', x.name) || x.cfg; return `<div class="item acc-row"><div class="dl-nt"><b>${x.name} <span class="dl-sub">${esc(cf.origin)}</span></b><span class="dl-why">signs in with ${['username', 'password', 'otp', 'passkey'].filter((k) => cf[k]).map((k) => ({ otp: 'a one-time code', passkey: 'a passkey' }[k] || `a ${k}`)).join(', ')}</span></div><span class="dl-na"><a class="btn sm secondary" href="#/browser/logins">Edit the login</a><button class="icon-btn" data-act="cred-more" data-m="${m.name}" data-c="${x.name}" aria-label="More for ${esc(x.name)}">${icon('more')}</button></span></div>`; }).join('') || '<div class="item small muted">No sign-in credentials name this bot.</div>'}</div></div>
          <div class="panel"><div class="panel-head"><h3>Saved browser sessions</h3><div class="tools"><a class="btn sm" href="#/browser/add">${icon('key')}Add a login</a></div></div><div class="list">${logins.map((l) => `<div class="item"><span class="icon-btn" aria-hidden="true">${icon('cookie')}</span><div class="grow"><b>${l.site}</b><div class="mono tiny muted">${l.cookies} cookies · seeded ${ago(l.seeded)} ago · operator requests only</div></div><button class="btn sm secondary" data-act="login-revoke" data-site="${l.site}" data-id="${l.identity}">Remove…</button></div>`).join('') || '<div class="item small muted">No saved logins.</div>'}</div></div>
          <div class="panel"><div class="panel-head"><h3>Who approves what it asks</h3></div><div class="list">${(eff.operators.v || []).map((o) => `<div class="item"><span class="avatar">${o.slice(0, 2)}</span><b>${o}</b><span class="grow"></span>${o === 'ops' ? tag('you') : ''}</div>`).join('')}</div></div>
        </div></div>`);
  }
  UI.act('cred-more', (b) => UI.menu(b, [[`Stop ${b.dataset.m} using it…`, 'x', 'cred-unbind', `data-m="${b.dataset.m}" data-c="${b.dataset.c}"`]]));
  const editM = (name, fn) => { const c = Y_clone(staged('bots', name)); fn(c); const ch = S.stage('bots', name, c); UI.refreshChrome(); UI.rerender(); if (ch) UI.toast('Staged', esc(ch.summary), 'run'); };
  UI.act('acc-tier', (sel) => editM(sel.dataset.m, (c) => { const T2 = (c.autonomy = c.autonomy || {}).tiers = c.autonomy.tiers || {}; Object.keys(T2).forEach((t) => { T2[t] = T2[t].filter((v) => v !== sel.dataset.v); if (!T2[t].length) delete T2[t]; }); (T2[sel.value] = T2[sel.value] || []).push(sel.dataset.v); if (sel.value !== 'reversible' && c.autonomy.promote) c.autonomy.promote = c.autonomy.promote.filter((v) => v !== sel.dataset.v); }));
  // move a thing a bot may do between the groups: on its own (an undoable action it no longer asks about; reading always
  // is), asks first, never; the team's rules disable a move they forbid, with the reason
  const setMode = (c, v, mode) => { const A = (c.autonomy = c.autonomy || {}), T2 = (A.tiers = A.tiers || {}), cur = S.tierFor({ cfg: c }, v).tier;
    Object.keys(T2).forEach((t) => { T2[t] = T2[t].filter((x) => x !== v); if (!T2[t].length) delete T2[t]; }); A.promote = (A.promote || []).filter((x) => x !== v);
    const tier = mode === 'never' ? 'never' : mode === 'asks' ? (cur === 'commit_money' ? 'commit_money' : 'propose') : cur === 'observe' ? 'observe' : 'reversible';
    (T2[tier] = T2[tier] || []).push(v); if (mode === 'own' && tier === 'reversible') A.promote.push(v); if (!A.promote.length) delete A.promote; };
  UI.act('can-more', (b) => { const m = M(b.dataset.m), c = staged('bots', m.name), v = b.dataset.v, mode = UI.verbMode(Object.assign({}, m, { cfg: c }), v), tf = S.tierFor(Object.assign({}, m, { cfg: c }), v), team = teamOf(m), ceiling = S.ceilingOf(Object.assign({}, m, { cfg: c }));
    const LV = ['observe', 'reversible', 'propose', 'commit_money', 'never'], looser = (t) => tf.team && LV.indexOf(t) < LV.indexOf(tf.team);
    const ownOff = mode === 'own' ? 'It already does this on its own' : looser('reversible') ? `Team ${team.name} keeps this at “asks you first”` : ceiling === 'observe' && tf.tier !== 'observe' ? `On team ${team.name} a bot does on its own only things that read` : '';
    UI.menu(b, [['Do it on its own', 'check', 'can-move', ownOff ? UI.off(ownOff) : `data-m="${m.name}" data-v="${esc(v)}" data-mode="own"`], ['Ask me first', 'hand', 'can-move', mode === 'asks' ? UI.off('It already asks first') : looser('propose') ? UI.off(`Team ${team.name} never allows this`) : `data-m="${m.name}" data-v="${esc(v)}" data-mode="asks"`], ['Never', 'x', 'can-move', mode === 'never' ? UI.off('It already never does this') : `data-m="${m.name}" data-v="${esc(v)}" data-mode="never"`], ['-'], ['Take it away', 'trash', 'acc-delverb', `data-m="${m.name}" data-v="${esc(v)}"`]]); });
  UI.act('can-move', (b) => editM(b.dataset.m, (c) => setMode(c, b.dataset.v, b.dataset.mode)));
  UI.act('acc-promote', (b) => editM(b.dataset.m, (c) => { const p = (c.autonomy.promote = c.autonomy.promote || []); const i = p.indexOf(b.dataset.v); if (i >= 0) p.splice(i, 1); else p.push(b.dataset.v); }));
  const verbsOf = (c) => { const v = S.getPath(c, 'skill.verbs'); return Array.isArray(v) ? v : v ? Object.keys(v) : []; };
  const addVerb = (c, verb) => { c.skill = c.skill || {}; const v = c.skill.verbs; if (v && !Array.isArray(v)) v[verb] = {}; else c.skill.verbs = (v || []).concat([verb]); };
  const dropVerb = (c, verb) => { const v = S.getPath(c, 'skill.verbs'); if (v && !Array.isArray(v)) delete v[verb]; else if (v) c.skill.verbs = v.filter((x) => x !== verb); };
  UI.act('acc-delverb', (b) => editM(b.dataset.m, (c) => { dropVerb(c, b.dataset.v); c.autonomy = c.autonomy || {}; Object.values(c.autonomy.tiers || {}).forEach((vs) => { const i = vs.indexOf(b.dataset.v); if (i >= 0) vs.splice(i, 1); }); Object.keys(c.autonomy.tiers || {}).forEach((t) => { if (!c.autonomy.tiers[t].length) delete c.autonomy.tiers[t]; }); if (c.autonomy.promote) c.autonomy.promote = c.autonomy.promote.filter((v) => v !== b.dataset.v); }));
  const verbList = () => { const out = []; D.connectors.forEach((c) => { const d = SCHEMA.connectors[c.use]; if (d) { out.push(`${c.name}.*`); Object.keys(d.verbs).forEach((v) => out.push(`${c.name}.${v}`)); } }); return out; };
  UI.act('acc-addverb', (b) => { const c0 = staged('bots', b.dataset.m), name = (v) => { const d = SCHEMA.verb(v.replace(/\*$/, '')); return d ? `${d.label} (${v})` : v.endsWith('.*') ? `Anything on ${v.slice(0, -2)} (${v})` : v; };
    UI.form({ caps: `bot ${b.dataset.m}`, title: 'Allow something new', intro: 'Pick what it may do, and whether it asks you first. Giving a bot something new widens what it can do, so applying asks for your passkey.', fields: [{ k: 'verb', type: 'enum', req: true, label: 'What it may do', options: verbList().filter((v) => !verbsOf(c0).includes(v)).map((v) => [v, name(v)]) }, { k: 'mode', type: 'seg', req: true, label: 'How', options: [['asks', 'Ask me first'], ['own', 'On its own']], def: 'asks' }], submit: 'Review change', onSubmit: (v) => { editM(b.dataset.m, (c) => { addVerb(c, v.verb); setMode(c, v.verb, v.mode || 'asks'); }); } }); });
  /* a bot may use a login when the credential's identities name it: binding edits that list (one diff) */
  UI.act('cred-bind', (b) => { const id = 'bot:' + b.dataset.m; const others = S.view().access.credentials.filter((c) => !((c.cfg || {}).identities || []).includes(id));
    UI.form({ caps: 'browser.credentials[…].identities', title: `Let ${b.dataset.m} use a login`, fields: [{ k: 'cred', type: 'enum', req: true, label: 'Login', options: others.map((c) => [c.name, `${c.name} · ${(c.cfg || {}).origin || ''}`]), create: 'credential', createCtx: { bot: b.dataset.m }, createLabel: 'Add a login' }],
      note: 'Adds the bot to that login\'s identities. Used only on work you started. A team that sets browser.credentials must list the login too.', submit: 'Review change', submitIcon: 'check',
      onSubmit: (v) => { const cf = JSON.parse(JSON.stringify(S.staged('credentials', v.cred))); if (!(cf.identities || []).includes(id)) cf.identities = (cf.identities || []).concat([id]); UI.staged(S.stage('credentials', v.cred, cf, `let ${b.dataset.m} use login ${v.cred}`)); } }); });
  UI.act('cred-unbind', (b) => { const id = 'bot:' + b.dataset.m, cf = JSON.parse(JSON.stringify(S.staged('credentials', b.dataset.c))); const rest = (cf.identities || []).filter((x) => x !== id);
    UI.confirm({ title: `Stop ${esc(b.dataset.m)} using ${esc(b.dataset.c)}?`, body: rest.length ? `Removes bot:${esc(b.dataset.m)} from its identities; ${rest.map(esc).join(', ')} keep it. The vault entries stay.` : 'No other identity uses it, so this removes the login itself. The vault entries stay.', ok: 'Stage it', cancel: 'Keep it', danger: true }).then((ok) => { if (!ok) return; if (rest.length) { cf.identities = rest; UI.staged(S.stage('credentials', b.dataset.c, cf)); } else UI.staged(S.stage('credentials', b.dataset.c, null)); }); });
  UI.act('login-revoke', (b) => UI.confirm({ title: `Log out of ${b.dataset.site}?`, body: 'Deletes the saved cookie jar now (browser logout). Immediate, audited.', ok: 'Revoke', danger: true }).then((ok) => { if (!ok) return; D.access.logins = D.access.logins.filter((l) => !(l.site === b.dataset.site && l.identity === b.dataset.id)); audit('browser_logout', `${b.dataset.id} · ${b.dataset.site}`); S.save(); UI.rerender(); UI.toast(`Logged out of ${b.dataset.site}`, 'saved jar deleted · audited', 'ok'); }));

  /* ---------- definition editor: a form over bots.<m>, live YAML beside it ---------- */
  const DF = [
    { k: 'description', type: 'string', mono: false, label: 'Description', sec: 'Identity & charter' },
    { k: 'team', type: 'enum', label: 'Team', options: () => S.view().teams.map((t) => t.name), create: 'team', createLabel: 'New team', help: 'Empty for a solo bot. On a team, defaults come from the team and this bot can only narrow them.' },
    { k: 'operators', type: 'list', label: 'Operators (approvers)', help: 'Leave empty to inherit the team\'s.' },
    { k: 'escalation', type: 'string', mono: false, label: 'Escalation (when to involve you)' },
    { k: 'autonomy.shadow', type: 'bool', label: 'Practice mode (shadow)', on: 'it says what it would do; nothing is done', sec: 'Autonomy' },
    { k: 'autonomy.approval_ttl', type: 'duration', label: 'Approval expires after', help: 'A request you haven’t answered by then lapses.' },
    { k: 'autonomy.spend_cap.per_action', type: 'number', label: 'Cloud spend cap · per action (commit_money)', cloud: true, help: 'Real money one approved action may commit. Not model spend.' },
    { k: 'autonomy.spend_cap.per_period', type: 'number', label: 'Cloud spend cap · per 30 days', cloud: true },
    { k: 'proactive.every', type: 'duration', label: 'Sweep every', sec: 'Proactive sweeps', help: 'Empty: no sweeps (a bot opts in).' },
    { k: 'proactive.checklist.section', type: 'string', label: 'Checklist: a charter section', ph: 'Proactive checks', help: 'What every sweep works through: a heading in the charter, or a file below (exactly one).' },
    { k: 'proactive.checklist.file', type: 'string', label: '…or a checklist file', ph: 'bots/<name>-checks.md' },
    { k: 'proactive.quiet_hours', type: 'string', label: 'Quiet hours', ph: '22:00-07:00' },
    { k: 'proactive.notify_at', type: 'enum', label: 'Notify at', options: ['high', 'medium', 'low'] },
    { k: 'proactive.budget.max_runs_per_day', type: 'number', label: 'Max runs / day' },
    { k: 'proactive.budget.max_turns_per_day', type: 'number', label: 'Max turns / day' },
    { k: 'proactive.budget.max_tokens_per_day', type: 'string', label: 'Max tokens / day', ph: '500k' },
    { k: 'proactive.budget.max_cost_per_day', type: 'number', label: 'Max cost / day (USD, per provider)', api: true },
    { k: 'home.rotate.max_age', type: 'duration', label: 'Rotate after', sec: 'Sessions, workers, isolation, peers' },
    { k: 'home.rotate.max_turns', type: 'number', label: '…or turns' },
    { k: 'worker.runtime', type: 'enum', label: 'Worker runtime', options: () => S.view().runtimes.map((r) => r.name), create: 'runtime', createLabel: 'New runtime' },
    { k: 'worker.max_concurrent', type: 'number', label: 'Max concurrent workers' },
    { k: 'isolation.sandbox', type: 'seg', label: 'Agent sandbox', options: [['', 'inherit'], ['optional', 'optional'], ['required', 'required']], help: 'required: its sessions start only while the agent sandbox is enforcing. On a team, only ever stricter than the team.' },
    { k: 'peers', type: 'select-multi', label: 'Peers (bots it may message)', options: () => S.view().bots.map((b) => b.name), create: 'bot', createLabel: 'New bot', help: 'Teammates are already peers. A link needs both bots to list each other (or an operator bot_links pair).' },
  ];
  const dv = { name: null, draft: null, base: null, charter: null };
  function settings(m, isNew) {
    const key = isNew ? '$new' : m.name;
    const q = new URLSearchParams((location.hash.split('?')[1] || ''));
    if (dv.name !== key || (isNew && (q.get('team') || '') !== (dv.team || ''))) { dv.name = key; dv.team = isNew ? q.get('team') || '' : ''; dv.back = isNew ? q.get('back') || '' : ''; dv.base = isNew ? Object.assign({ description: '', autonomy: { shadow: true, approval_ttl: '24h' } }, dv.team ? { team: dv.team } : { operators: ['ops'] }) : Y_clone(staged('bots', m.name)); dv.draft = Y_clone(dv.base); dv.newName = ''; dv.charter = isNew ? '' : D.charters[m.name] || ''; }
    const name = isNew ? dv.newName || 'new-bot' : m.name;
    const errs = S.cfg.bots.validate(name, dv.draft, S.candidate(D.pending.filter((c) => !(c.coll === 'bots' && c.key === name)).concat([{ coll: 'bots', key: name, after: dv.draft }])));
    const warns = S.cfg.bots.warn ? S.cfg.bots.warn(name, dv.draft, D) : [];
    const team = dv.draft.team && T(dv.draft.team), td = team ? team.cfg || {} : {};
    const inh = (path) => { const v = S.getPath(td, path); return v === undefined ? '' : `<div class="hint inh-hint">${icon('link')} team ${team.name}: <span class="mono">${esc(Array.isArray(v) ? v.join(', ') : v)}</span></div>`; };
    const inhMap = { operators: 'operators', 'autonomy.approval_ttl': 'autonomy.approval_ttl', 'isolation.sandbox': 'isolation.sandbox', 'proactive.budget.max_runs_per_day': 'budget.proactive.max_runs_per_day', 'proactive.budget.max_cost_per_day': 'budget.proactive.max_cost_per_day' };
    let sec = 'Identity & charter';
    const charter = `<div class="field"><label for="df-charter">Charter <span class="opt">bots/${esc(name)}.md · config only</span></label><textarea class="textarea" rows="6" id="df-charter" data-df-charter="1">${esc(dv.charter)}</textarea><div class="hint">Nothing on the agent-facing surface writes it.</div></div>`;
    const fields = DF.map((f) => { const fd = Object.assign({}, f, { options: typeof f.options === 'function' ? f.options() : f.options }); const head = f.sec && f.sec !== sec ? (`${sec === 'Identity & charter' ? charter : ''}</div></div><div class="panel"><div class="panel-head"><h3>${(sec = f.sec)}</h3></div><div class="panel-body def-grid">`) : ''; if (f.api && !UI.isApi()) return head; const html = UI.fieldHTML(Object.assign({}, fd, { k: 'df.' + f.k }), S.getPath(dv.draft, f.k)); return head + html.replace('<div class="ferr"', (inhMap[f.k] && team ? inh(inhMap[f.k]) : '') + '<div class="ferr"'); }).join('');
    const yaml = YAML.stringify({ bots: { [name]: dv.draft } });
    const changed = !YAML.eq(dv.draft, dv.base) || (isNew ? !!dv.newName : dv.charter !== (D.charters[m.name] || ''));
    const body = `<div class="grid g2" style="align-items:start">
        <div class="stack def-form" data-bot="${esc(key)}"><div class="panel"><div class="panel-head"><h3>Identity &amp; charter</h3></div><div class="panel-body def-grid">
          ${isNew ? UI.fieldHTML({ k: 'df.$name', type: 'string', req: true, label: 'Name', ph: 'e.g. data-steward' }, dv.newName) : ''}
          ${fields}
        </div></div></div>
        <div class="stack def-yaml"><div class="panel"><div class="panel-head"><h3>As YAML</h3><span class="muted small">config.yaml · <span class="mono">bots.${esc(name)}</span></span><div class="tools">${errs.length ? st('fail', `${errs.length} error${errs.length > 1 ? 's' : ''}`) : st('ok', 'validates')}</div></div><div class="panel-body tight" id="df-yaml">${UI.yaml(yaml)}</div>
          ${errs.length || warns.length ? `<div class="panel-body" style="border-top:1px solid var(--color-border-subtle)">${errs.map((e) => `<div class="callout danger small" style="margin-bottom:6px">${esc(e)}</div>`).join('')}${warns.map((e) => `<div class="callout warn small" style="margin-bottom:6px">${esc(e)}</div>`).join('')}</div>` : ''}
          <div class="panel-foot"><button class="btn ghost" data-act="df-reset" ${changed ? '' : UI.off('No edits')}>${icon('undo')}Reset</button><button class="btn primary" style="margin-left:auto" data-act="df-review" ${!changed ? UI.off('Change something first') : errs.length ? UI.off('Fix the errors first') : ''}>${icon('check')}Review changes</button></div></div></div></div>`;
    return isNew ? `<div class="page">${UI.head('New bot', { crumbs: [['Bots', '#/bots'], ['New bot']], sub: dv.team ? `on team ${esc(dv.team)} · starts in shadow mode` : 'solo or on a team · starts in shadow mode' })}${body}</div>` : shell(m, 'settings', body);
  }
  let dfT;
  document.addEventListener('input', (e) => { const f = e.target.closest('.def-form'); if (!f) return; clearTimeout(dfT); dfT = setTimeout(() => readDef(f), 350); });
  document.addEventListener('change', (e) => { const f = e.target.closest('.def-form'); if (!f) return; clearTimeout(dfT); readDef(f); });
  function readDef(f) {
    const d = Y_clone(dv.draft);
    DF.forEach((fd) => { const v = UI.readField(f, Object.assign({}, fd, { k: 'df.' + fd.k })); if (v === undefined) return; S.setPath(d, fd.k, fd.type === 'bool' ? (v ? true : fd.k === 'autonomy.shadow' ? false : undefined) : v === '' ? undefined : v); });
    if (S.getPath(d, 'autonomy.shadow') === false && !S.getPath(dv.base, 'autonomy.shadow')) S.setPath(d, 'autonomy.shadow', false);
    const nm = f.querySelector('#f-df__name'); if (nm) dv.newName = nm.value.trim();
    const ch = f.querySelector('[data-df-charter]'); if (ch) dv.charter = ch.value;
    if (!d.proactive || !d.proactive.every) delete d.proactive;
    dv.draft = d;
    const active = document.activeElement && document.activeElement.id;
    const pos = active && document.activeElement.selectionStart;
    UI.render({ keepScroll: true });
    if (active) { const el = document.getElementById(active); if (el) { el.focus(); try { if (pos != null) el.setSelectionRange(pos, pos); } catch (x) {} } }
  }
  UI.act('df-reset', () => { dv.name = null; UI.rerender(); });
  UI.act('df-review', () => {
    const isNew = dv.name === '$new', name = isNew ? dv.newName : dv.name;
    const nameErr = (msg) => { const f = document.getElementById('f-df__name'), fld = f && f.closest('.field'); if (!fld) { UI.toast(msg, '', 'fail'); return; } fld.classList.add('bad'); let e = fld.querySelector('.ferr'); if (!e) { e = document.createElement('div'); e.className = 'ferr'; e.setAttribute('role', 'alert'); fld.appendChild(e); } e.textContent = msg; f.focus(); };
    if (isNew && !/^[a-z][a-z0-9-]*$/.test(name || '')) return nameErr('Name the bot first: lowercase letters, digits and dashes.');
    if (isNew && M(name)) return nameErr('A bot by that name exists.');
    if (!dv.draft.charter_file) dv.draft.charter_file = `bots/${name}.md`;
    D.charters[name] = dv.charter; const ch = S.stage('bots', name, dv.draft);
    if (dv.back) { D.prefs.returnTo = { href: dv.back, label: dv.team ? `team ${dv.team}` : 'where you were' }; S.save(); }
    dv.name = null; UI.staged(ch);
  });

  // "members" were renamed "bots": old links still land
  UI.route('members/:rest?/:a?/:b?', () => { setTimeout(() => UI.go(location.hash.replace('#/members', '#/bots'))); return ''; }, { nav: 'bots' });
  UI.route('bots/:name/:tab?/:sub?', ({ name, tab, sub }) => {
    if (name === 'new') return settings(null, true);
    const m = M(name);
    if (!m) return `<div class="page"><div class="empty"><span class="serif">No bot named ${esc(name)}</span><a class="btn" href="#/bots">Bots</a></div></div>`;
    switch (tab || (pending(m.name).length ? 'proposals' : 'chat')) {
      case 'cases': return cases(m, sub);
      case 'proposals': return proposals(m, sub);
      case 'memory': return memory(m);
      case 'workers': return workers(m);
      case 'access': return access(m);
      case 'settings': return settings(m);
      default: return { toString: () => chat(m), after: (main) => { const b = UI.$('#msgs', main); if (b) b.scrollTop = b.scrollHeight; } };
    }
  }, { nav: 'bots' });
})();
