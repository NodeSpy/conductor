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
  const botCard = (m) => `<a class="panel bot-card" href="#/bots/${m.name}">
        <div class="row">${UI.sigil(m, 'lg')}<div class="grow"><h3>${m.name}</h3><div class="muted small">${esc(m.desc)}</div></div></div>
        <div class="row wrap">${m.home === 'live' ? st('run', 'home live') : st('idle', 'home idle')}${m.shadow ? st('shadow', 'shadow mode') : ''}${tag(S.getPath(m.cfg || {}, 'isolation.sandbox') === 'required' || S.getPath((teamOf(m) || { cfg: {} }).cfg, 'isolation.sandbox') === 'required' ? 'sandbox required' : 'sandboxed', 'ok')}${teamOf(m) && teamOf(m).cfg.lead === m.name ? tag('lead', 'ember') : ''}</div>
        <div class="grid g3" style="gap:8px">
          <div><div class="caps">cases</div><div class="serif" style="font-size:26px">${D.cases.filter((c) => c.bot === m.name && c.status === 'open').length}</div></div>
          <div><div class="caps">pending</div><div class="serif" style="font-size:26px;color:${pending(m.name).length ? 'var(--accent)' : 'inherit'}">${pending(m.name).length}</div></div>
          <div><div class="caps">workers</div><div class="serif" style="font-size:26px">${D.workers.filter((w) => (w.bot || 'platform-sre') === m.name && w.status === 'run').length}</div></div>
        </div>
        <div class="row small muted"><span class="mono tiny">session ${m.session} · ${m.turns}/${m.maxTurns} turns</span><span style="margin-left:auto">${m.proactive ? icon('clock') + ' sweep in ' + m.proactive.next : ''}</span></div>
      </a>`;
  UI.peersGraph = peersGraph;

  /* ---------- team pages ---------- */
  const leadOf = (t) => (t && t.cfg && t.cfg.routing && t.cfg.routing.lead) || null;
  UI.leadOf = leadOf;
  /* the team form: conductor's teams.<t> keys (feat/members teams.go) */
  function teamForm(t) {
    const isNew = !t, c = t ? staged('teams', t.name) : { operators: ['ops'], autonomy: { ceiling: 'reversible' }, isolation: { sandbox: 'required' } };
    const g = (p) => S.getPath(c, p);
    UI.form({ caps: isNew ? 'teams:' : `teams.${t.name}`, title: isNew ? 'New team' : `Edit team ${t.name}`, wide: true,
      intro: 'A team groups bots: they share approvers and ceilings, and a bot on it can only be stricter. Teams are the unit for shared access.',
      values: isNew ? { ceiling: 'reversible', sandbox: 'required', peers: 'team' } : { description: c.description, folder: c.folder, operators: c.operators, ceiling: g('autonomy.ceiling') || 'reversible', shadow: !!g('autonomy.shadow'), sandbox: g('isolation.sandbox') || 'optional', handoff: g('isolation.handoff_workers') !== false, peers: c.peers || 'team', depth: g('delegation.max_depth'), pool_runs: g('budget.proactive.max_runs_per_day'), pool_turns: g('budget.proactive.max_turns_per_day'), pool_tokens: g('budget.proactive.max_tokens_per_day'), pool_cost: g('budget.proactive.max_cost_per_day'), cap_action: g('budget.spend_cap.per_action'), cap_period: g('budget.spend_cap.per_period'), secrets_via: g('credentials.secrets_via') || 'none', allow_secrets: g('credentials.allow_secrets'), logins: g('browser.logins'), credentials: g('browser.credentials'), repo: g('knowledge.repo'), promotion: g('knowledge.promotion') || 'proposal', lead: g('routing.lead'), fallback: g('routing.fallback') },
      fields: [
        isNew ? { k: 'name', type: 'string', req: true, ph: 'e.g. data', pattern: /^[a-z][a-z0-9-]*$/, patternMsg: 'Lowercase letters, digits and dashes.', validate: (v) => (T(v) ? 'A team by that name exists.' : '') } : null,
        { k: 'description', type: 'string', mono: false, label: 'Description' },
        isNew ? { k: 'bots', type: 'select-multi', label: 'Bots', options: S.view().bots.map((m) => m.name), create: 'bot', createLabel: 'New bot', help: 'A bot is on at most one team: adding one here moves it.' } : null,
        { k: 'folder', type: 'enum', label: 'Folder', options: UI.allFolders(), create: 'folder', createLabel: 'New folder', help: 'Organization only: where the team shows. No permissions.' },
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
      onSubmit: (v) => {
        const name = isNew ? v.name : t.name, n = {};
        if (v.description) n.description = v.description;
        if (v.folder) n.folder = v.folder;
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
        S.stage('teams', name, n);
        (v.bots || []).forEach((mn) => { const mc = Y_clone(staged('bots', mn)); mc.team = name; S.stage('bots', mn, mc); });
        if (isNew && !D.teamKnowledge[name] && v.repo) D.teamKnowledge[name] = { repo: v.repo, files: {}, proposals: [] };
        UI.staged(S.find('teams', name));
      } });
  }
  const Y_clone = (o) => JSON.parse(JSON.stringify(o));
  UI.act('team-new', () => teamForm(null));
  UI.act('team-edit', (b) => teamForm(T(b.dataset.t)));
  UI.act('team-del', (b) => {
    const t = T(b.dataset.t), ms = botsOf(t.name);
    UI.confirm({ title: `Remove team ${t.name}?`, body: `${ms.length ? `Its ${ms.length} bot${ms.length > 1 ? 's' : ''} (${ms.map((m) => m.name).join(', ')}) leave the team and keep their own settings. ` : ''}Stages the removal; review and apply with your passkey.`, ok: 'Stage removal', danger: true }).then((ok) => {
      if (!ok) return; ms.forEach((m) => { const c = Y_clone(staged('bots', m.name)); delete c.team; S.stage('bots', m.name, c); });
      UI.staged(S.stage('teams', t.name, null));
    });
  });

  const routeLabel = (r) => !r ? '' : r.by === 'rule' ? `routed to <b>${r.team}</b> by rule <span class="mono">${esc(r.rule)}</span>${r.via === 'lead' ? ` → assigned by lead <b>${esc(r.lead)}</b> to <b>${esc(r.to)}</b>` : ` → <b>${esc(r.to)}</b>`}` : r.by === 'lead' ? `assigned by lead <b>${esc(r.lead)}</b> to <b>${esc(r.to)}</b>` : r.by === 'operator' ? `reassigned by operator <b>ops</b> to <b>${esc(r.to)}</b>${r.from ? ` (was ${esc(r.from)})` : ''}` : r.by === 'link' ? `arrived over the team link from <b>${esc(r.team)}</b>` : r.by === 'proactive' ? 'opened by a proactive sweep' : r.by === 'delegate' ? `delegated by <b>${esc(r.from)}</b> → <b>${esc(r.to)}</b> from case <span class="mono">${esc(r.parent)}</span>${r.cross ? ` (across the ${esc(r.team)} link)` : ''}` : `delivered to <b>${esc(r.to)}</b> directly`;
  UI.routeLabel = routeLabel;
  const glob = (pat, v) => new RegExp('^' + String(pat).split('*').map((x) => x.replace(/[.+?^${}()|[\]\\]/g, '\\$&')).join('.*') + '$').test(String(v));
  const matchRule = (t, ev) => (((t.cfg.routing || {}).rules) || []).find((r) => r.match && Object.entries(r.match).every(([k, v]) => (k === 'on' ? glob(v, ev.on) : glob(v, ev.payload[k] != null ? ev.payload[k] : S.getPath(ev.payload, 'incident.' + k) || ''))));
  const tv = { ev: 'ev-pd' };

  function teamShell(t, tab, body) {
    const c = staged('teams', t.name) || t.cfg, ms = botsOf(t.name);
    const pend = c.knowledge ? R.pending('team:' + t.name) : 0;
    return `<div class="page"><div class="bot-hero team-hero"><span class="sigil lg team-sigil">${esc(t.name[0])}</span><div class="grow"><div class="crumbs mono tiny muted">${UI.folderCrumbs(UI.folderOfTeam(t.name, false))} › team</div><h1>${esc(t.name)}</h1>
      <div class="facts">${tag(ms.length + ' bot' + (ms.length === 1 ? '' : 's'))}${leadOf({ cfg: c }) ? tag('lead ' + leadOf({ cfg: c }), 'ember') : tag('no lead')}${tag(`approvers: ${(c.operators || []).join(', ') || 'every operator'}`)}${tag(`runs alone up to: ${S.getPath(c, 'autonomy.ceiling') || 'reversible'}`)}${tag(icon('shield') + ' ' + (S.getPath(c, 'defaults.isolation.mode') || 'namespace'), 'ok')}${S.find('teams', t.name) ? tag('change staged', 'run') : ''}</div></div>
      <div class="row wrap"><button class="btn" data-act="team-deliver" data-t="${t.name}">${icon('send')}Route work here</button><button class="btn" data-act="team-edit" data-t="${t.name}">${icon('edit')}Edit team</button><button class="btn ghost" data-act="team-del" data-t="${t.name}" aria-label="Remove team">${icon('trash')}</button></div></div>
      ${UI.tabs([['overview', 'Overview', `#/teams/${t.name}`], ['routing', 'Routing', `#/teams/${t.name}/routing`, ((c.routing || {}).rules || []).length], ['defaults', 'Ceilings & scopes', `#/teams/${t.name}/defaults`], ['knowledge', 'Knowledge', `#/teams/${t.name}/knowledge`, pend || null, pend > 0]], tab)}
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
  const edgeHTML = (d) => `<div class="item deleg" data-deleg="${d.id}" style="align-items:flex-start">${st(DST[d.status], '')}<div class="grow small">
      <div><span class="mono tiny muted">${d.id}</span> delegated by <b>${esc(d.from)}</b> → <a class="link" href="#/bots/${d.to}/cases/${d.child}"><b>${esc(d.to)}</b></a> <span class="tag" data-s="${DST[d.status]}">${d.status}</span>${M(d.from) && M(d.to) && teamOf(M(d.from)) && teamOf(M(d.to)) && teamOf(M(d.from)).name !== teamOf(M(d.to)).name ? ' ' + tag('linked bots') : ''}</div>
      <div class="muted">“${esc(d.task)}”</div>
      ${d.result ? `<div class="deleg-result">↩ result: ${esc(d.result)}</div>` : ''}
      <div class="tiny muted">requester <span class="taint ${d.cls}">${esc(d.requester)}</span> kept · ${ago(d.at)} ago${d.done ? ` · reported back ${ago(d.done)} ago` : ''}</div></div>
      ${d.status === 'running' ? `<button class="btn sm ghost" data-act="deleg-cancel" data-id="${d.id}">${icon('x')}Cancel</button>` : ''}</div>`;
  UI.act('deleg-cancel', (b) => UI.confirm({ title: `Cancel delegation ${b.dataset.id}?`, body: 'The recipient\'s child case is closed and the delegating bot is told the task was cancelled. Immediate, audited.', ok: 'Cancel delegation', danger: true }).then((ok) => {
    if (!ok) return; const d = D.delegations.find((x) => x.id === b.dataset.id); d.status = 'cancelled'; d.done = now();
    const ch = D.cases.find((x) => x.id === d.child); if (ch) ch.status = 'closed';
    const par = D.cases.find((x) => x.id === d.parent); if (par) (par.notes = par.notes || []).push({ at: now(), who: 'conductor', text: `delegation ${d.id} to ${d.to} cancelled by operator ops` });
    audit('bot_delegate_cancel', `${d.id} · ${d.from} → ${d.to}`); S.save(); UI.rerender();
  }));
  UI.act('case-delegate', (b) => {
    const m = M(b.dataset.m), c = D.cases.find((x) => x.id === b.dataset.c), reach = UI.delegationReach(m.name);
    UI.form({ caps: `conductor bot delegate ${m.name} --case ${c.id}`, title: `Have ${m.name} delegate a task`, intro: `${m.name} hands a teammate a task from <b>${c.id}</b>. The recipient works it as a child case under the original requester (<span class="mono">${esc(c.requester)}</span>), and the result reports back here.`,
      fields: [{ k: 'to', type: 'enum', req: true, label: 'Delegate to', options: reach.map((r) => [r.name, `${r.name}${r.team ? ' · ' + r.team : ' · solo'}${r.ok ? (r.cross ? ' (linked)' : '') : ' — not linked'}`]), def: (reach.find((r) => r.ok) || reach[0]).name, validate: (v) => { const r = reach.find((x) => x.name === v); return r && !r.ok ? r.why : ''; } },
        { k: 'task', type: 'text', req: true, label: 'Task', ph: 'What should the teammate do, and what should it report back?' }],
      submit: 'Delegate', submitIcon: 'send',
      onSubmit: (v) => {
        const r = reach.find((x) => x.name === v.to);
        const id = 'd' + (D.delegations.length + 1), child = newCase(v.to, { title: v.task.split('\n')[0].slice(0, 90), requester: c.requester, cls: c.cls, routed: { by: 'delegate', from: m.name, to: v.to, parent: c.id, delegation: id, team: teamOf(m) ? teamOf(m).name : null, cross: r.cross, requester_kept: true }, notes: [] });
        D.delegations.unshift({ id, from: m.name, to: v.to, parent: c.id, child, task: v.task, requester: c.requester, cls: c.cls, status: 'running', at: now() });
        audit('bot_delegate', `${id} · ${m.name} → ${v.to} · case ${c.id} → ${child} · requester ${c.requester} kept`); S.save(); UI.rerender();
        UI.toast(`Delegated to ${v.to}`, `child case ${child} · result reports back to ${c.id}`, 'run');
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
      ${ms.concat(outside).map((n) => { const b = M(n), p = pos[n]; return `<a href="#/bots/${n}"><circle cx="${p.x}" cy="${p.y}" r="17" fill="${b ? b.color : '#888'}" class="pg-node${ms.includes(n) ? '' : ' outside'}"/><text x="${p.x}" y="${p.y + 5}" text-anchor="middle" class="pg-init">${n[0]}</text><text x="${p.x}" y="${p.y + 33}" text-anchor="middle" class="pg-name">${esc(n)}${ms.includes(n) ? '' : ' · ' + esc((teamOf(b) || { name: 'solo' }).name)}</text></a>`; }).join('')}
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
          ${ms.map((m) => `<div class="item"><a href="#/bots/${m.name}" class="row grow" style="gap:10px">${UI.sigil(m)}<div><b>${m.name}</b> ${leadOf({ cfg: c }) === m.name ? tag('lead', 'ember') : ''}<div class="muted tiny">${esc(m.desc)}</div></div></a>
            ${leadOf({ cfg: c }) === m.name ? '' : `<button class="btn sm ghost" data-act="team-lead" data-t="${t.name}" data-m="${m.name}">Make lead</button>`}<button class="btn sm ghost" data-act="team-remove" data-t="${t.name}" data-m="${m.name}" aria-label="Remove ${m.name} from team">${icon('x')}Remove</button></div>`).join('') || '<div class="item muted small">No bots yet.</div>'}</div>
          <div class="panel-foot"><label class="row small grow"><span class="muted">Add a bot</span><select class="select mono" id="team-add-sel" aria-label="Bot to add" data-picker="bot" data-ctx='${esc(JSON.stringify({ page: `#/bots/new/settings?team=${t.name}&back=${encodeURIComponent('#/teams/' + t.name)}` }))}'><option value="__new__">+ Create a new bot in this team…</option>${others.map((m) => `<option value="${m.name}"${m === others[0] ? ' selected' : ''}>${m.name}${teamOf(m) ? ' (moves from ' + teamOf(m).name + ')' : ' (solo)'}</option>`).join('')}</select></label><button class="btn sm" ${others.length ? `data-act="team-add" data-t="${t.name}"` : UI.off('Every existing bot is already here: create a new one from the list')}>${icon('plus')}Add to team</button></div></div>
        <div class="panel"><div class="panel-head"><h3>Ask &amp; delegate activity</h3><span class="muted small">last 24h</span></div><div class="panel-body tight">${activityGraph(t)}</div></div>
        <div class="panel"><div class="panel-head"><h3>Delegations</h3><span class="muted small">teammates hand each other tasks; results report back</span></div><div class="list">${D.delegations.filter((d) => botsOf(t.name).some((b) => b.name === d.from || b.name === d.to)).map(edgeHTML).join('') || '<div class="item small muted">No delegations yet.</div>'}</div>
          <div class="panel-foot small muted">Beyond the team, a bot delegates only to a bot it's linked with (both list each other as peers, or an operator's bot_links pair).</div></div>
        <div class="panel"><div class="panel-head"><h3>Routing decisions</h3><span class="muted small">requester provenance kept through every hop</span></div><div class="list">${cases.map((x) => `<a class="item click" href="#/bots/${x.bot}/cases/${x.id}"><span class="mono tiny muted">${x.id}</span><div class="grow small"><div class="ellipsis"><b>${esc(x.title)}</b></div><div class="tiny muted">${routeLabel(x.routed)} · requester <span class="taint ${x.cls}">${x.requester}</span></div></div><span class="chev">${icon('chev')}</span></a>`).join('') || '<div class="item muted small">Nothing routed yet.</div>'}</div></div>
      </div>
      <div class="stack">
        ${!UI.usageOn() ? '' : (() => { const bp = UI.botsProv(ms.map((m) => m.name)), b0 = {}, rc = b0.max_runs_per_day, tc = b0.max_tokens_per_day, uc = UI.isApi() ? b0.max_cost_per_day : null, runs = ms.reduce((a, m) => a + Object.values(D.usage.bots[m.name] || {}).reduce((x, y) => x + (y.runs || 0), 0), 0), src = UI.usageSource();
          const meter = (v, cap, label, f) => `<div class="row"><span class="grow muted">${label}</span><span class="mono">${f(v)} / ${f(cap)}</span></div><div class="meter"><i style="width:${Math.min(100, (v / cap) * 100)}%"></i></div>`;
          return `<div class="panel" data-usage><div class="panel-head"><h3>Usage today</h3><span class="muted small">per provider, never combined</span></div><div class="panel-body stack tight small">
          ${Object.keys(D.usage.providers).map((p) => { const x = bp[p] || { runs: 0, turns: 0 }; return `<div class="prov-row" data-prov="${p}"><div class="row"><b class="grow">${UI.provLabel(p)}</b><span class="mono" data-figure>${x.runs} runs · ${x.turns} turns${x.tokens != null ? ' · ' + UI.fmtTok(x.tokens) + ' tok' : ''}${UI.isApi() ? ' · ' + usd(x.usd || 0) : ''}</span></div>${tc && x.tokens != null ? meter(x.tokens, tc, 'token cap', UI.fmtTok) : ''}${uc != null ? meter(x.usd || 0, uc, 'spend cap', usd) : ''}</div>`; }).join('')}
          <div class="pv-src tiny" data-src="${src ? 'source' : 'observed'}">runs and turns observed by conductor${src ? ' · tokens' + (UI.isApi() ? ' and dollars' : '') + ' from ' + esc(src.name) : UI.isApi() ? ' · dollars from runtime reports' : ''}</div>
          ${tc || uc != null ? '<div class="muted tiny">Token and spend caps apply to each provider on its own.</div>' : ''}
          ${rc ? `<div data-budget><div class="row"><span class="grow muted">runs dispatched · budget</span><span class="mono">${runs} / ${rc}</span></div><div class="meter"><i style="width:${Math.min(100, (runs / rc) * 100)}%"></i></div></div>` : ''}
          ${pool ? `<div data-budget><div style="margin-top:6px"><b>Proactive pool (shared)</b></div><div class="mono">${claimed} / ${pool.max_runs_per_day} runs claimed${pool.max_tokens_per_day ? ' · ' + UI.fmtTok(pool.max_tokens_per_day) + ' tok/day per provider' : ''}${UI.isApi() && pool.max_cost_per_day != null ? ' · ' + usd(pool.max_cost_per_day) + '/day per provider' : ''}</div><div class="meter"><i style="width:${Math.min(100, (claimed / pool.max_runs_per_day) * 100)}%;background:var(--run)"></i></div><div class="muted tiny">Each bot's sweep budget draws on the pool; the validator refuses a bot over it.</div></div>` : '<div class="muted tiny">No shared proactive pool.</div>'}</div></div>`; })()}
        <div class="panel"><div class="panel-head"><h3>Links</h3></div><div class="panel-body stack tight small">
          <div><span class="caps">inside the team</span><div class="muted tiny" style="margin-top:3px">${ms.length > 1 ? ms.map((m) => m.name).join(' ↔ ') + ' · data-only ask, no extra config' : 'One bot: nobody to ask yet.'}</div></div>
          <div><span class="caps">beyond the team</span>${cross.map(([a, o]) => `<div class="row" style="margin-top:6px"><span class="mono grow">${esc(a)} ↔ ${esc(o)}</span><span class="muted tiny">${(D.botLinks || []).some((pr) => pr.includes(a) && pr.includes(o)) ? 'bot_links' : 'both list each other as peers'}</span>${(D.botLinks || []).some((pr) => pr.includes(a) && pr.includes(o)) ? `<button class="btn sm ghost" data-act="botlink-del" data-a="${a}" data-b="${o}">Unlink</button>` : ''}</div>`).join('') || '<div class="muted tiny" style="margin-top:4px">No links: the team\'s bots talk only to each other.</div>'}<button class="btn sm" style="margin-top:8px" data-act="botlink-add" data-t="${t.name}">${icon('link')}Link a bot…</button></div>
          <div class="muted tiny">A link lets two bots message each other and delegate, data only. It's a pair of bots, never a whole team, and a config edit (widening).</div>
          <div><span class="caps">delegation reach</span>${botsOf(t.name).map((b) => { const r = UI.delegationReach(b.name); return `<div class="tiny" style="margin-top:4px"><b class="mono">${b.name}</b> → ${r.filter((x) => x.ok).map((x) => `<span class="mono">${x.name}</span>`).join(', ') || '<span class="muted">no one</span>'}${r.some((x) => !x.ok && x.team) ? ` <span class="muted">· isolated from ${r.filter((x) => !x.ok && x.team).map((x) => x.name).join(', ')}</span>` : ''}</div>`; }).join('')}</div></div></div>
      </div></div>`);
  }
  function teamRouting(t) {
    const c = staged('teams', t.name) || t.cfg, rules = (c.routing || {}).rules || [];
    const ev = SCHEMA.events.find((e) => e.id === tv.ev) || SCHEMA.events[0];
    const hit = matchRule({ cfg: c }, ev);
    return teamShell(t, 'routing', `<div class="grid g-side"><div class="stack">
      <div class="panel"><div class="panel-head"><h3>Rules</h3><span class="muted small">first match wins · no match: the lead triages · then the fallback</span><div class="tools"><button class="btn sm" data-act="rule-edit" data-t="${t.name}" data-i="-1">${icon('plus')}Add a rule</button></div></div>
        <div class="list">${rules.map((r, i) => `<div class="item"><span class="mono tiny muted">${i + 1}</span><div class="grow"><b class="mono small">${esc(r.name || 'rule ' + (i + 1))}</b><div class="tiny muted mono">when ${esc(r.when ? Object.entries(r.when).map(([k, v]) => `${k}: ${[].concat(v).join('|')}`).join(' · ') : '—')} → ${r.to === 'lead' ? `lead (${esc(c.lead || 'none')})` : esc(r.to)}</div></div>
          <button class="icon-btn" ${i ? `data-act="rule-move" data-t="${t.name}" data-i="${i}" data-d="-1"` : UI.off('Already first')} aria-label="Move up">${icon('chev', 'up')}</button><button class="icon-btn" ${i < rules.length - 1 ? `data-act="rule-move" data-t="${t.name}" data-i="${i}" data-d="1"` : UI.off('Already last')} aria-label="Move down">${icon('chev', 'down')}</button><button class="btn sm ghost" data-act="rule-edit" data-t="${t.name}" data-i="${i}">${icon('edit')}Edit</button><button class="btn sm ghost" data-act="rule-del" data-t="${t.name}" data-i="${i}" aria-label="Delete rule">${icon('trash')}</button></div>`).join('') || '<div class="item muted small">No rules: work reaches this team only through its bots directly.</div>'}</div>
        <div class="panel-foot small muted">Rule edits stage a config change to <span class="mono">teams.${t.name}.routing.rules</span>. Lead: <b>${esc((c.routing || {}).lead || 'none')}</b> · fallback: <b>${esc((c.routing || {}).fallback || 'none')}</b>.${S.find('teams', t.name) ? ' <a class="link" href="#/config/apply">Review staged change</a>' : ''}</div></div>
      </div><div class="stack">
      <div class="panel"><div class="panel-head"><h3>Try a rule</h3></div><div class="panel-body stack tight small"><label class="field"><span class="caps">a real past event</span><select class="select mono" data-on-change="route-ev" aria-label="Event to route">${SCHEMA.events.map((e) => `<option value="${e.id}"${e.id === ev.id ? ' selected' : ''}>${esc(e.label)}</option>`).join('')}</select></label>
        ${hit ? `<div class="callout ok small">Matches <b class="mono">${esc(hit.name)}</b> → <b>${esc(hit.to)}</b>. The case keeps requester <span class="mono">event:${esc(ev.on)}</span>.</div>` : `<div class="callout small">No rule in <b>${t.name}</b> matches <span class="mono">${esc(ev.on)}</span>; it goes to whichever trigger or bot it was addressed to.</div>`}</div></div>
      <div class="callout info small">The lead is a bot: it reads the case, then assigns it with <span class="mono">bot assign</span>. The requester never changes on the way, so the assignee runs on no more trust than whoever asked.</div></div></div>`);
  }
  function teamDefaults(t) {
    const c = staged('teams', t.name) || t.cfg, ms = botsOf(t.name);
    const st2 = Object.assign({}, D, { teams: D.teams.map((x) => (x.name === t.name ? { name: x.name, cfg: c } : x)) });
    const effs = ms.map((m) => { const mm = Object.assign({}, m, { cfg: staged('bots', m.name) }); return { m: mm, e: S.effective(mm, st2), errs: S.narrowErrors(m.name, mm.cfg, st2) }; });
    const badge = (src, ok = true) => (!ok ? '<span class="wid">wider ✗</span>' : src === 'inherits' ? '<span class="inh">inherits</span>' : src === 'equal' ? '<span class="inh">same as team</span>' : src === 'narrows' ? '<span class="nar">narrows</span>' : src === 'own' ? '<span class="inh">own</span>' : `<span class="inh">${esc(src)}</span>`);
    const row = (label, teamV, cell) => `<tr><td data-l="setting"><b>${label}</b></td><td data-l="team" class="mono small">${teamV}</td>${effs.map((x) => `<td data-l="${x.m.name}" class="small">${cell(x)}</td>`).join('')}</tr>`;
    const subset = (a, b) => !b || (a || []).every((x) => b.includes(x));
    const ceiling = S.getPath(c, 'autonomy.ceiling') || 'reversible';
    const pool = S.getPath(c, 'budget.proactive');
    return teamShell(t, 'defaults', `<div class="callout small" style="margin-bottom:12px">A bot on a team can only be <b>stricter</b>: approvers from the team's list, promotions no higher than the ceiling, the team's tier classifications and sandbox, sweeps inside the shared pool, logins inside the team's scope. Anything wider is refused when the config is validated, naming the team value it breaks.</div>
      ${UI.tierLadder()}
      <div class="panel" style="margin-top:14px"><div class="panel-head"><h3>Team values and what each bot does with them</h3><div class="tools"><button class="btn sm" data-act="team-edit" data-t="${t.name}">${icon('edit')}Edit team</button></div></div>
      <table class="tbl responsive inherit-tbl"><thead><tr><th>setting</th><th>team</th>${effs.map((x) => `<th><a href="#/bots/${x.m.name}/settings">${x.m.name}</a></th>`).join('')}</tr></thead><tbody>
        ${row('approvers', (c.operators || []).join(', ') || 'every operator', ({ e }) => `<span class="mono">${(e.operators.v || []).join(', ') || '—'}</span> ${badge(e.operators.src, subset(e.operators.v, c.operators))}`)}
        ${row('runs alone up to (ceiling)', ceiling, ({ m }) => { const pr = S.getPath(m.cfg, 'autonomy.promote') || []; const over = pr.filter((v) => S.LADDER.indexOf(S.tierFor(m, v, st2).tier) > S.LADDER.indexOf(ceiling)); return `<span class="mono">${pr.join(', ') || 'nothing promoted'}</span> ${badge(pr.length ? 'own' : 'inherits', !over.length)}`; })}
        ${row('tier classifications', Object.entries(S.getPath(c, 'autonomy.tiers') || {}).map(([k, v]) => `${k}: ${v.join(', ')}`).join('; ') || '—', ({ errs }) => { const te = errs.filter((x) => /classifies/.test(x)); return te.length ? `<span class="wid">wider ✗</span> <span class="tiny">${esc(te[0])}</span>` : '<span class="inh">stricter or equal</span>'; })}
        ${row('shadow mode', S.getPath(c, 'autonomy.shadow') ? 'held in shadow' : 'not held', ({ e }) => `<span class="mono">${e.shadow.v === undefined ? (S.getPath(c, 'autonomy.shadow') ? 'shadow' : '—') : e.shadow.v ? 'shadow' : 'off'}</span> ${badge(e.shadow.src, !(S.getPath(c, 'autonomy.shadow') && e.shadow.v === false))}`)}
        ${row('agent sandbox', S.getPath(c, 'isolation.sandbox') || 'optional', ({ e }) => `<span class="mono">${e.sandbox.v || 'optional'}</span> ${badge(e.sandbox.src, !(S.getPath(c, 'isolation.sandbox') === 'required' && e.sandbox.v === 'optional'))}`)}
        ${row('proactive sweeps (shared pool)', pool ? Object.entries(pool).map(([k, v]) => `${k.replace(/^max_|_per_day$/g, '')} ${v}`).join(' · ') : '—', ({ m, errs }) => { const b = S.getPath(m.cfg, 'proactive.budget'); return b ? `<span class="mono">${Object.entries(b).map(([k, v]) => `${k.replace(/^max_|_per_day$/g, '')} ${v}`).join(' · ')}</span> ${badge(pool ? 'narrows' : 'own', !errs.some((x) => /proactive pool/.test(x)))}` : '<span class="muted">no sweeps</span>'; })}
        ${row('cloud spend cap', S.getPath(c, 'budget.spend_cap') ? `${S.getPath(c, 'budget.spend_cap.per_action') || '—'} / action` : '—', ({ e, errs }) => `<span class="mono">${e.spendCap.v ? (e.spendCap.v.per_action || '—') + ' / action' : '—'}</span> ${badge(e.spendCap.src, !errs.some((x) => /spend_cap/.test(x)))}`)}
        ${row('sign-in credentials', (S.getPath(c, 'browser.credentials') || []).join(', ') || 'any', ({ e }) => `<span class="mono">${e.credentials.v.join(', ') || 'none'}</span> ${badge(!e.credentials.scope ? 'own' : e.credentials.v.length === e.credentials.scope.length && subset(e.credentials.v, e.credentials.scope) ? 'equal' : 'narrows', subset(e.credentials.v, e.credentials.scope))}`)}
        ${row('saved logins', (S.getPath(c, 'browser.logins') || []).join(', ') || 'any', ({ e }) => `<span class="mono">${e.logins.v.join(', ') || 'none'}</span> ${badge(!e.logins.scope ? 'own' : e.logins.v.length === e.logins.scope.length && subset(e.logins.v, e.logins.scope) ? 'equal' : 'narrows', subset(e.logins.v, e.logins.scope))}`)}
      </tbody></table></div>`);
  }
  /** The tier ladder, explained once: what each tier means for execution. */
  UI.tierLadder = () => `<div class="tier-ladder" role="list" aria-label="Tiers, least to most cautious">${[['observe', 'Reads only: runs on its own.'], ['reversible', 'Acts, and the action can be undone: runs on its own only if promoted (up to the team ceiling), otherwise asks.'], ['propose', 'Asks you first: a proposal you approve with your passkey.'], ['commit_money', 'Spends real money: asks you, within the spend cap.'], ['never', 'Refused outright.']].map(([t, d], i) => `<div class="tl-step" role="listitem"><span class="tier ${t}">${t}</span><span class="small">${d}</span>${i < 4 ? '<span class="tl-arrow" aria-hidden="true">›</span>' : ''}</div>`).join('')}</div>`;
  function teamKnowledge(t) {
    const c = staged('teams', t.name) || t.cfg;
    if (!c.knowledge) return teamShell(t, 'knowledge', `<div class="panel"><div class="empty"><span class="serif">No shared knowledge repo</span>A team can keep one repo every bot on it reads. You write to it directly; bots propose and you approve.<button class="btn primary" data-act="team-edit" data-t="${t.name}">${icon('plus')}Add a knowledge repo</button></div></div>`);
    return teamShell(t, 'knowledge', UI.repoView('team:' + t.name, { title: c.knowledge.repo }));
  }
  UI.route('teams/:name/:tab?', ({ name, tab }) => {
    const t = T(name);
    if (!t) return `<div class="page"><div class="empty"><span class="serif">No team named ${esc(name)}</span><a class="btn" href="#/bots">Bots</a></div></div>`;
    return tab === 'routing' ? teamRouting(t) : tab === 'defaults' ? teamDefaults(t) : tab === 'knowledge' ? teamKnowledge(t) : teamOverview(t);
  }, { nav: 'bots' });
  UI.act('team-lead', (b) => { const c = Y_clone(staged('teams', b.dataset.t)); c.routing = Object.assign({}, c.routing, { lead: b.dataset.m }); UI.staged(S.stage('teams', b.dataset.t, c)); });
  UI.act('team-remove', (b) => { const c = Y_clone(staged('bots', b.dataset.m)); delete c.team; S.stage('bots', b.dataset.m, c); const tc = Y_clone(staged('teams', b.dataset.t)); const ro = tc.routing || {}; let ch = false; if (ro.lead === b.dataset.m) { delete ro.lead; ch = true; } if (ro.fallback === b.dataset.m) { delete ro.fallback; ch = true; } const before = (ro.rules || []).length; ro.rules = (ro.rules || []).filter((r) => r.to !== b.dataset.m); if (ro.rules.length !== before) ch = true; if (!ro.rules.length) delete ro.rules; if (ch) { if (Object.keys(ro).length) tc.routing = ro; else delete tc.routing; S.stage('teams', b.dataset.t, tc); } UI.staged(S.find('bots', b.dataset.m)); });
  UI.act('team-add', (b) => { const mn = UI.$('#team-add-sel').value; const c = Y_clone(staged('bots', mn)); c.team = b.dataset.t; UI.staged(S.stage('bots', mn, c)); });
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
  function newCase(bot, o) {
    const id = 'c' + (40 + D.cases.length);
    D.cases.unshift(Object.assign({ id, bot, status: 'open', opened: now(), workers: 0, proposals: 0, notes: [] }, o));
    return id;
  }
  UI.act('team-deliver', (b) => {
    const t = T(b.dataset.t), c = staged('teams', t.name) || t.cfg;
    UI.form({ caps: 'conductor bot deliver --team', title: `Route work to ${t.name}`, intro: 'The team\'s routing rules decide who gets it; with no match it goes to the lead.',
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
        UI.go(`#/bots/${to}/cases/${id}`);
      } });
  });

  /* ---------- bot shell ---------- */
  function hero(m) {
    const t = teamOf(m), lead = t && leadOf(t) === m.name;
    return `<div class="bot-hero">${UI.sigil(m, 'lg')}<div class="grow"><div class="crumbs mono tiny muted">${UI.folderCrumbs(UI.folderOfBot(m.name, false))} › ${t ? `<a href="#/teams/${t.name}">team ${t.name}</a> › ` : ''}${m.name}</div><h1>${m.name}</h1>
      <div class="facts">${m.home === 'live' ? st('run', `home ${m.session} live`, true) : st('idle', `home ${m.session} idle`, true)}
        ${t ? `<a href="#/teams/${t.name}">${tag('team ' + t.name + (lead ? ' · lead' : ''), 'ember')}</a>` : tag('no team')}${tag(`${m.turns}/${m.maxTurns} ${UI.term('turns', 'turn')}`)}${UI.usageOn() ? UI.provSet(UI.botProv(m.name)) : ''}${m.shadow ? tag(UI.term('shadow mode', 'shadow'), 'shadow') : tag('approved execution', 'ok')}
        ${tag(`${icon('shield')} ${m.mode}`, 'ok')}${tag(`operators: ${(S.effective(m).operators.v || []).join(', ')}`)}</div></div>
      <div class="row wrap"><button class="btn" data-act="m-rotate" data-m="${m.name}">${icon('updates')}Rotate session</button><button class="btn" ${m.proactive ? `data-act="m-sweep" data-m="${m.name}"` : UI.off('No proactive: block in its definition')}>${icon('eye')}Sweep now</button><button class="btn" data-act="m-deliver" data-m="${m.name}">${icon('send')}Deliver event</button></div></div>`;
  }
  UI.act('bot-watch-add', (b) => { const name = b.dataset.m, evs = S.view().connectors.flatMap((c) => Object.entries((SCHEMA.connectors[c.use] || {}).events || {}).map(([ev, d]) => [`${c.name}.${ev}`, `${d.label} (${c.name}.${ev})`, c.use]));
    UI.form({ caps: 'triggers:', title: `What should ${name} watch?`, intro: 'Adds a trigger that hands each matching event to the bot (a bot: step). It reads the event, then tells you or proposes what to do.',
      fields: [{ k: 'on', type: 'enum', req: true, label: 'When this happens', options: evs.map(([v, l]) => [v, l]) }, { k: 'repos', type: 'list', label: 'Only these repos (GitHub events)', ph: 'acme/api', show: (v) => /^gh\./.test(v.on || '') }],
      submit: 'Review change', submitIcon: 'check',
      onSubmit: (v) => { const tn = `${name}-${v.on.split('.')[1].replace(/_/g, '-')}`; const y = [`name: ${tn}`, `on: ${v.on}`, ...((v.repos || []).length ? [`filter: { repo: [${v.repos.join(', ')}] }`] : []), 'steps:', '  - id: tell', `    bot: { name: ${name}, title: "{{.title}}" }`].join('\n') + '\n'; D.prefs.returnTo = { href: `#/bots/${name}`, label: name }; UI.staged(S.stage('workflows', tn, y, `trigger ${tn} hands ${v.on} to ${name}`)); } }); });
  function shell(m, tab, body) {
    const pc = pending(m.name).length;
    const w = UI.botWatches(m.name);
    const watchNote = !w.triggers.length && !w.sweep ? `<div class="callout wait watch-empty" style="margin-bottom:14px">${icon('eye')}<span class="grow"><b>${m.name} isn't watching anything yet.</b> No trigger hands it events and it doesn't check in on its own, so it only answers when you chat with it.</span><button class="btn sm primary" data-act="bot-watch-add" data-m="${m.name}">${icon('plus')}Add what it watches</button></div>`
      : `<div class="small muted watch-line" style="margin:-6px 0 12px">${icon('eye')} Watches ${w.triggers.map((n) => `<a class="link mono" href="#/workflows/${esc(n)}">${esc(n)}</a>`).join(', ') || 'no events'}${w.sweep ? `${w.triggers.length ? ' · ' : ''}checks in every ${esc(S.getPath(staged('bots', m.name), 'proactive.every') || '')}` : ''} · <button class="link" data-act="bot-watch-add" data-m="${m.name}">add</button></div>`;
    return `<div class="page">${hero(m)}${watchNote}
      ${UI.tabs([['chat', 'Chat', `#/bots/${m.name}/chat`], ['cases', 'Cases', `#/bots/${m.name}/cases`, D.cases.filter((c) => c.bot === m.name && c.status === 'open').length], ['proposals', 'Proposals', `#/bots/${m.name}/proposals`, pc, pc > 0], ['memory', 'Memory', `#/bots/${m.name}/memory`], ['workers', 'Workers', `#/bots/${m.name}/workers`, D.workers.filter((w) => (w.bot || 'platform-sre') === m.name && w.status === 'run').length], ['access', 'Access', `#/bots/${m.name}/access`], ['settings', 'Definition', `#/bots/${m.name}/settings`]], tab)}
      ${body}</div>`;
  }
  UI.act('m-rotate', (b) => { const m = M(b.dataset.m); UI.confirm({ title: 'Rotate the home session?', body: `<p style="margin:0 0 8px">Conductor asks the current session for a hand-off note, waits for that turn to settle, launches a fresh session, and only then archives the old one. Its token is revoked at once.</p><div class="kv"><dt>current</dt><dd class="mono">${m.session} · ${m.turns} turns</dd><dt>next</dt><dd class="mono">s${+m.session.slice(1) + 1} · resumes from handoff/${m.session}.md + ledger</dd></div>`, ok: 'Rotate' }).then((ok) => { if (!ok) return; const old = m.session; m.session = 's' + (+old.slice(1) + 1); m.turns = 0; m.age = '0m'; m.home = 'live'; audit('bot_rotate', `${m.name} · ${old} → ${m.session}`); S.save(); UI.rerender(); UI.toast('Rotated', `${old} archived · ${m.session} live`, 'ok'); }); });
  UI.act('m-sweep', (b) => { const m = M(b.dataset.m); const id = 'x' + (5 + D.workers.filter((w) => w.parent === 'sweep').length); D.workers.unshift({ id, bot: m.name, parent: 'sweep', case: '—', group: '', status: 'run', verbs: 'observe only', readOnly: true, cls: 'proactive', brief: 'Proactive checks (charter §)', run: 'r-7f29', cost: 0, started: now() }); audit('bot_sweep', `${m.name} · ${id} · read-only`); S.save(); UI.go(`#/bots/${m.name}/workers`); UI.toast(`Sweep ${id} started`, 'read-only · draws on the team pool', 'run'); });
  UI.act('m-deliver', (b) => { const m = M(b.dataset.m); UI.form({ caps: 'conductor bot deliver', title: `Deliver an event to ${m.name}`, fields: [{ k: 'title', type: 'string', req: true, mono: false, ph: 'Investigate elevated 5xx on /refund' }, { k: 'text', type: 'text', label: 'Text (reaches the bot fenced as untrusted data)' }, { k: 'requester', type: 'enum', req: true, options: [['operator:ops', 'operator:ops (you)'], ['event:manual', 'event']], def: 'operator:ops' }, { k: 'urgent', type: 'bool', on: 'start a worker now' }], submit: 'Deliver', submitIcon: 'send', onSubmit: (v) => { const id = newCase(m.name, { title: v.title, requester: v.requester, cls: v.requester.startsWith('operator') ? 'operator' : 'event', urgent: !!v.urgent, routed: { by: 'direct', to: m.name, requester_kept: true }, notes: v.text ? [{ at: now(), who: v.requester, text: v.text }] : [] }); (D.chats[m.name] = D.chats[m.name] || []).push({ who: 'event', at: now(), case: id, text: v.title + (v.text ? ' · ' + v.text : ''), fenced: true }); audit('bot_deliver', `${m.name} · ${id} · ${v.requester}`); S.save(); UI.go(`#/bots/${m.name}/cases/${id}`); } }); });

  /* ---------- chat ---------- */
  const bubble = (c, name) => { const who = c.who === 'op' ? 'you · ops' : c.who === 'bot' ? name : c.who === 'event' ? `event → case ${c.case}` : 'tool'; return `<div class="msg ${c.who}"><div class="meta"><span>${who}</span><span>${UI.clock(c.at)}</span></div><div class="bubble">${c.who === 'tool' ? '$ ' + esc(c.text) : md(c.text)}</div></div>`; };
  function chat(m) {
    const pp = pending(m.name), msgs = D.chats[m.name] || [];
    return shell(m, 'chat', `<div class="grid g-side">
      <div class="panel chat"><div class="panel-head"><h3>Home session <span class="mono muted">${m.session}</span></h3><span class="muted small">messages arrive when the agent is idle, never mid-turn</span><div class="tools"><button class="btn sm ghost" ${UI.off('Opens paseo\'s own agent view, outside the dashboard (not in the prototype)')}>${icon('terminal')}Open in paseo</button></div></div>
        <div class="msgs" id="msgs">${msgs.map((c) => bubble(c, m.name)).join('') || '<div class="empty small">No conversation yet. Say hello.</div>'}</div>
        <div class="composer"><textarea class="textarea" id="say" rows="1" placeholder="Message ${m.name}…  (you are the requester: operator:ops)" aria-label="Message"></textarea><button class="btn primary" data-act="chat-send" data-m="${m.name}">${icon('send')}Send</button></div></div>
      <div class="stack">
        ${pp.map((p) => `<div class="panel" style="border-color:color-mix(in srgb,var(--accent) 40%,var(--line))"><div class="panel-head">${st('ember', 'awaiting you')}<span class="mono tiny muted" style="margin-left:auto">${p.id} · ${ago(p.at)}</span></div><div class="panel-body small"><div style="font-weight:600;margin-bottom:6px">${esc(p.summary)}</div><div class="row"><a class="btn sm primary" href="#/bots/${m.name}/proposals/${p.id}">${icon('passkey')}Review &amp; sign</a><span class="muted tiny">expires in ${p.expires}</span></div></div></div>`).join('')}
        <div class="panel"><div class="panel-head"><h3>Open cases</h3><a class="btn sm ghost" style="margin-left:auto" href="#/bots/${m.name}/cases">All</a></div><div class="list">${D.cases.filter((c) => c.bot === m.name && c.status === 'open').map((c) => `<a class="item small click" href="#/bots/${m.name}/cases/${c.id}"><span class="mono tiny muted">${c.id}</span><span class="grow ellipsis">${esc(c.title)}</span><span class="taint ${c.cls}">${c.cls}</span></a>`).join('') || '<div class="item small muted">None open.</div>'}</div></div>
      </div></div>`);
  }
  UI.act('chat-send', (b) => {
    const ta = UI.$('#say'), v = ta.value.trim(); if (!v) return UI.nudge(ta, `Type a message for ${b.dataset.m} first`);
    const L = (D.chats[b.dataset.m] = D.chats[b.dataset.m] || []); L.push({ who: 'op', at: now(), text: v }); ta.value = ''; S.save();
    const box = UI.$('#msgs'); box.innerHTML = L.map((c) => bubble(c, b.dataset.m)).join('') + `<div class="msg bot" id="typing"><div class="meta"><span>${b.dataset.m}</span><span>queued → delivered (idle)</span></div><div class="bubble typing"><i></i><i></i><i></i></div></div>`; box.scrollTop = box.scrollHeight;
    setTimeout(() => { L.push({ who: 'bot', at: now(), text: `Noted: “${v.slice(0, 80)}”. I’ll act on it as your request (operator trust) and report back on the case.` }); S.save(); const bx = UI.$('#msgs'); if (bx) { bx.innerHTML = L.map((c) => bubble(c, b.dataset.m)).join(''); bx.scrollTop = bx.scrollHeight; } }, UI.fast ? 50 : 1400);
  });
  document.addEventListener('keydown', (e) => { if (e.target.id === 'say' && e.key === 'Enter' && !e.shiftKey) { e.preventDefault(); const b = UI.$('[data-act="chat-send"]'); if (b) b.click(); } });

  /* ---------- cases ---------- */
  const cv = { f: 'open' };
  function cases(m, sub) {
    const list = D.cases.filter((c) => c.bot === m.name);
    const c = sub && list.find((x) => x.id === sub);
    if (c) return shell(m, 'cases', caseDetail(m, c));
    const vis = list.filter((c) => cv.f === 'all' || c.status === cv.f);
    return shell(m, 'cases', `<div class="filters">${UI.seg([['open', `open · ${list.filter((x) => x.status === 'open').length}`], ['closed', `closed · ${list.filter((x) => x.status === 'closed').length}`], ['all', 'all']], cv.f, 'case-f')}<button class="btn sm" data-act="m-deliver" data-m="${m.name}">${icon('plus')}Open a case</button><span class="muted small hide-phone">A case's requester class decides how much trust its work runs on.</span></div>
      <div class="panel"><table class="tbl responsive"><thead><tr><th>case</th><th>title</th><th>requester</th><th>routed</th><th>opened</th><th>status</th></tr></thead><tbody>
      ${vis.map((c) => `<tr class="click" data-href="#/bots/${m.name}/cases/${c.id}"><td data-l="case" class="mono"><a class="link-plain" href="#/bots/${m.name}/cases/${c.id}">${c.id}</a>${c.urgent ? ' ' + tag('urgent', 'fail') : ''}</td><td data-l="title"><b>${esc(c.title)}</b></td><td data-l="requester"><span class="taint ${c.cls}">${c.requester}</span></td><td data-l="routed" class="small route-cell">${c.routed ? routeLabel(c.routed) : '—'}${D.delegations.some((d) => d.parent === c.id) ? ` ${tag(`${D.delegations.filter((d) => d.parent === c.id).length} delegated`, 'run')}` : ''}</td><td data-l="opened" class="mono muted">${ago(c.opened)} ago</td><td data-l="status">${c.status === 'open' ? st('run', 'open') : st('ok', 'closed')}</td></tr>`).join('') || `<tr class="empty-row"><td colspan="6">No ${cv.f === 'all' ? '' : cv.f + ' '}cases.</td></tr>`}
      </tbody></table></div>
      <div class="row small muted" style="margin-top:10px" data-cloud-spend>${icon('dollar')} cloud spend this period: <span class="mono">${usd(m.spend)}</span> of the commit_money cap · ledger healthy</div>`);
  }
  function caseDetail(m, c) {
    const t = teamOf(m), mates = t ? botsOf(t.name).filter((x) => x.name !== m.name) : [];
    return `<div class="row" style="margin-bottom:12px"><a class="btn sm ghost back" href="#/bots/${m.name}/cases">${icon('chev')}All cases</a></div>
      <div class="grid g-side"><div class="stack">
        <div class="panel"><div class="panel-head">${c.status === 'open' ? st('run', 'open', true) : st('ok', 'closed', true)}<h3 class="mono">${c.id}</h3>${c.urgent ? tag('urgent', 'fail') : ''}<div class="tools"><span class="mono tiny muted">opened ${ago(c.opened)} ago</span></div></div>
          <div class="panel-body"><div class="serif" style="font-size:24px;line-height:1.2;margin-bottom:12px">${esc(c.title)}</div>
          <div class="route-box"><div class="caps">routing</div><div class="small">${c.routed ? routeLabel(c.routed) : 'delivered directly'}${c.routed && c.routed.note ? ` <span class="muted">· ${esc(c.routed.note)}</span>` : ''}</div><div class="tiny muted">requester <span class="taint ${c.cls}">${c.requester}</span> preserved through every hop: the assignee runs on no more trust than that.</div></div></div></div>
        ${(() => { const out = D.delegations.filter((d) => d.parent === c.id), inn = D.delegations.find((d) => d.child === c.id); return out.length || inn ? `<div class="panel"><div class="panel-head"><h3>Delegation</h3><span class="muted small">${out.length ? `${out.length} handed out from this case` : ''}${inn ? 'this case is a delegated task' : ''}</span></div><div class="list">${inn ? edgeHTML(inn).replace('class="item deleg"', 'class="item deleg inbound"') : ''}${out.map(edgeHTML).join('')}</div>${inn && inn.status === 'running' ? '<div class="panel-foot small muted">Closing this case reports its last note back to the delegating bot as the result.</div>' : ''}</div>` : ''; })()}
        <div class="panel"><div class="panel-head"><h3>Notes</h3></div><div class="list">${(c.notes || []).map((n) => `<div class="item small" style="align-items:flex-start"><span class="mono tiny muted" style="width:60px">${ago(n.at)} ago</span><div class="grow"><span class="mono tiny">${esc(n.who)}</span><div>${esc(n.text)}</div></div></div>`).join('') || '<div class="item small muted">No notes yet.</div>'}</div>
          <div class="panel-foot"><input class="input grow" id="case-note" placeholder="Add a note (bot case note)" aria-label="Note"><button class="btn sm" data-act="case-note" data-m="${m.name}" data-c="${c.id}">${icon('plus')}Add note</button></div></div>
      </div><div class="stack">
        <div class="panel"><div class="panel-body stack tight">
          <button class="btn" ${c.status !== 'open' ? UI.off('Reopen the case to delegate from it') : !UI.delegationReach(m.name).some((r) => r.ok) ? UI.off(`${m.name} has no teammate or linked bot to delegate to: put it on a team or link it with another bot`) : `data-act="case-delegate" data-m="${m.name}" data-c="${c.id}"`}>${icon('send')}Delegate a task…</button>
          ${c.status === 'open' ? `<button class="btn" data-act="case-close" data-m="${m.name}" data-c="${c.id}">${icon('check')}Close case</button>` : `<button class="btn" data-act="case-close" data-m="${m.name}" data-c="${c.id}">${icon('undo')}Reopen</button>`}
          ${t ? `<label class="field"><span class="caps">reassign within ${t.name}</span><select class="select mono" id="case-to" aria-label="Reassign to">${mates.map((x) => `<option>${x.name}</option>`).join('')}</select></label><button class="btn sm" ${mates.length && c.status === 'open' ? `data-act="case-reassign" data-m="${m.name}" data-c="${c.id}"` : UI.off(c.status !== 'open' ? 'Reopen it first' : 'Nobody else in the team')}>${icon('send')}Reassign</button>` : '<div class="muted tiny">Not in a team: no one to reassign to.</div>'}
        </div></div>
        <div class="panel"><div class="panel-head"><h3>Linked</h3></div><div class="panel-body small"><dl class="kv"><dt>workers</dt><dd class="mono">${c.workers}</dd><dt>proposals</dt><dd class="mono">${c.proposals}</dd><dt>class</dt><dd><span class="taint ${c.cls}">${c.cls}</span></dd></dl></div></div>
      </div></div>`;
  }
  UI.act('case-f', (b) => { cv.f = b.dataset.v; UI.rerender(); });
  document.addEventListener('click', (e) => { const tr = e.target.closest('tr[data-href]'); if (tr && !e.target.closest('a,button')) UI.go(tr.dataset.href); });
  UI.act('case-note', (b) => { const inp = UI.$('#case-note'), v = inp.value.trim(); if (!v) return UI.nudge(inp, 'Type the note first'); const c = D.cases.find((x) => x.id === b.dataset.c); (c.notes = c.notes || []).push({ at: now(), who: 'operator:ops', text: v }); audit('bot_case_note', `${b.dataset.m} · ${c.id}`); S.save(); UI.rerender(); });
  UI.act('case-close', (b) => { const c = D.cases.find((x) => x.id === b.dataset.c); c.status = c.status === 'open' ? 'closed' : 'open';
    const d = D.delegations.find((x) => x.child === c.id && x.status === 'running');
    if (d && c.status === 'closed') { const last = (c.notes || []).slice(-1)[0]; d.status = 'done'; d.done = now(); d.result = last ? last.text : `closed by operator ops with no note`; const par = D.cases.find((x) => x.id === d.parent); if (par) (par.notes = par.notes || []).push({ at: now(), who: `bot:${d.to}`, text: `↩ result of ${d.id}: ${d.result}` }); audit('bot_delegate_done', `${d.id} · ${d.to} → ${d.from} · ${d.result.slice(0, 60)}`); } audit('bot_case_' + (c.status === 'open' ? 'reopen' : 'close'), `${b.dataset.m} · ${c.id}`); S.save(); UI.rerender(); UI.refreshChrome(); });
  UI.act('case-reassign', (b) => { const c = D.cases.find((x) => x.id === b.dataset.c), to = UI.$('#case-to').value; const t = teamOf(M(b.dataset.m)); c.routed = { by: 'operator', from: c.bot, to, team: t.name, requester_kept: true }; c.bot = to; audit('bot_route', `${c.id} → ${to} · operator reassign · requester ${c.requester} kept`); S.save(); UI.go(`#/bots/${to}/cases/${c.id}`); });

  /* ---------- proposals ---------- */
  const PS = { pending: 'ember', shadow: 'shadow', executed: 'ok', approved: 'ok', refused: 'fail', denied: 'fail', failed: 'fail' };
  const pv = { f: 'all' };
  function proposals(m, sub) {
    const list = props(m.name);
    const p = sub && list.find((x) => x.id === sub);
    if (p) return innerWidth <= 720 ? `<div class="page">${proposalDetail(m, p)}</div>` : shell(m, 'proposals', proposalDetail(m, p));
    const vis = list.filter((x) => pv.f === 'all' || (pv.f === 'pending' ? x.status === 'pending' : x.status !== 'pending'));
    return shell(m, 'proposals', `<div class="filters">${UI.seg([['all', 'all'], ['pending', `pending · ${pending(m.name).length}`], ['decided', 'decided']], pv.f, 'prop-f')}</div><div class="panel">${vis.map((p) => `
      <a class="proposal ${p.status}" href="#/bots/${m.name}/proposals/${p.id}">
        <div>${st(PS[p.status] || 'idle', p.status)}</div>
        <div><div class="row wrap" style="gap:6px;margin-bottom:4px"><span class="mono" style="font-weight:600">${p.id}</span>${tier(p.tier)}<span class="mono small">${p.verb}</span><span class="muted tiny mono">case ${p.case}</span>${p.requester ? `<span class="taint ${p.cls}">${p.requester}</span>` : ''}${p.worker ? tag('worker ' + p.worker) : ''}</div><div>${esc(p.summary)}</div>${p.approver ? `<div class="muted tiny mono" style="margin-top:4px">decided by ${p.approver}</div>` : ''}</div>
        <div class="row">${p.status === 'pending' ? `<span class="muted tiny mono">expires ${p.expires}</span><span class="btn sm primary">${icon('passkey')}Review</span>` : `<span class="muted tiny mono">${ago(p.at)} ago</span>`}</div>
      </a>`).join('') || '<div class="empty small">No proposals here.</div>'}</div>
      <p class="muted small" style="margin-top:10px">Shadow proposals are the calibration record of a bot in shadow mode: they can't be approved. Also from the CLI: <span class="mono">conductor bot approve ${m.name} &lt;id&gt;</span> (SSH-signed).</p>`);
  }
  UI.act('prop-f', (b) => { pv.f = b.dataset.v; UI.rerender(); });
  function planHTML(t) { return esc(t).split('\n').map((l) => /must be replaced|forces replacement/.test(l) ? `<span class="rep">${l}</span>` : /^\s*\+\/-|^\s*~|will be updated/.test(l) ? `<span class="chg">${l}</span>` : /^\s*\+ /.test(l) ? `<span class="add">${l}</span>` : /#|unchanged/.test(l) ? `<span class="c">${l}</span>` : l).join('\n'); }
  /** Who asked, in words: a non-operator requester gets a banner, never just a colored chip. */
  UI.requesterBanner = (p) => {
    const r = p.requester || '';
    if (/^chat:/.test(r) || p.cls === 'user') return `<div class="req-banner untrusted">${icon('warn')}<div><b>Requested by a chat user who isn't one of your operators</b> (<span class="mono">${esc(r)}</span>). The bot is acting on their request, so it runs with no more trust than they have. Check this is something you want.</div></div>`;
    if (/^event:/.test(r) || p.cls === 'event') return `<div class="req-banner event">${icon('live')}<div><b>Started by an event, not a person</b> (<span class="mono">${esc(r)}</span>). No human asked for this action; the bot proposed it while handling the event.</div></div>`;
    if (p.cls === 'proactive') return `<div class="req-banner event">${icon('clock')}<div><b>The bot's own sweep</b> (<span class="mono">${esc(r)}</span>): nobody asked; it found this on a scheduled check.</div></div>`;
    if (p.cls === 'peer') return `<div class="req-banner untrusted">${icon('bots')}<div><b>Asked for by another bot</b> (<span class="mono">${esc(r)}</span>). It carries the original requester's trust, no more.</div></div>`;
    return `<div class="req-banner op">${icon('passkey')}<div><b>Requested by an operator</b> (<span class="mono">${esc(r)}</span>).</div></div>`;
  };
  /** The whole signed payload: every option, untruncated; long text in a scroll box. */
  UI.payloadHTML = (p) => `<div class="payload"><div class="pl-row"><span class="pl-k">verb</span><span class="pl-v mono">${esc(p.verb)}</span></div>${Object.entries(p.options || {}).map(([k, v]) => `<div class="pl-row"><span class="pl-k mono">${esc(k)}</span>${String(v).length > 80 || /\n/.test(String(v)) ? `<div class="pl-long">${esc(v)}</div>` : `<span class="pl-v mono">${esc(typeof v === 'object' ? JSON.stringify(v) : v)}</span>`}</div>`).join('') || '<div class="pl-row muted small">no options</div>'}</div>`;
  function proposalDetail(m, p) {
    const tf = p.tf, phone = innerWidth <= 720, pending = p.status === 'pending';
    const why = `<div class="why-impact"><div><span class="caps">Why</span><p>${esc(p.why || p.summary)}</p></div>${p.impact ? `<div><span class="caps">Impact</span><p>${esc(p.impact)}</p></div>` : ''}</div>`;
    const tiles = tf ? `<div class="tf-tiles"><div><span class="k">add</span><b style="color:var(--ok)">+${tf.add}</b></div><div><span class="k">change</span><b style="color:var(--wait)">~${tf.change}</b></div><div><span class="k">destroy</span><b>-${tf.destroy}</b></div><div><span class="k">replaces</span><b style="color:var(--fail)">${tf.replaces.length}</b></div></div>` : '';
    const action = tf ? `${tiles}<div class="callout warn small" style="margin:10px 0"><b>Replaces aws_instance.web.</b> It isn't on this bot's stateful list, so the replacement is allowed; a stateful type (a database, say) would be refused outright.</div><details class="tech"><summary>The plan (tf-plan ran it with the READ role)</summary><div class="plan">${planHTML(D.tfPlan)}</div></details>`
      : `<div class="caps" style="margin:4px 0 6px">Exactly what runs</div>${UI.payloadHTML(p)}`;
    const tech = `<details class="tech"><summary>Technical details: what the passkey signs</summary><dl class="kv small">${tf ? `<dt>action</dt><dd class="mono">terraform apply of ${esc(tf.repo)}, dir ${esc(tf.dir)}, workspace ${esc(tf.workspace)}</dd><dt>commit</dt><dd class="hash">${tf.commit}</dd><dt>plan sha256</dt><dd class="hash">${tf.plan_sha}</dd>` : ''}<dt>verb</dt><dd class="mono">${esc(p.verb)}</dd><dt>case</dt><dd class="mono">${p.case}</dd><dt>requester</dt><dd class="mono">${esc(p.requester || '—')} (${esc(p.cls || '')})</dd><dt>digest</dt><dd class="hash">${p.digest}</dd></dl></details>`;
    const buttons = pending ? `<button class="btn lg primary grow" data-act="p-approve" data-p="${p.id}" data-d="approve">${icon('passkey')}Approve</button><button class="btn lg danger" data-act="p-approve" data-p="${p.id}" data-d="deny">Deny</button>` : '';
    const done = !pending && p.status !== 'shadow' ? `<div class="callout ${p.status.startsWith('approved') ? 'ok' : 'danger'} small">${p.status.startsWith('approved') ? `${icon('check')} Approved${p.approver ? ' · ' + esc(p.approver) : ''}. conductor is applying exactly the signed action.` : `Denied${p.approver ? ' · ' + esc(p.approver) : ''}${p.reason ? `: “${esc(p.reason)}”` : ''}. The bot was told.`}</div>` : '';
    const head = `<div class="prop-head">${st(PS[p.status], p.status, true)}${tier(p.tier)}<span class="mono small muted">${p.id} · ${esc(p.verb)}</span><span class="mono tiny muted" style="margin-left:auto">proposed ${ago(p.at)} ago${p.expires && pending ? ' · expires in ' + p.expires : ''}</span></div><h2 class="prop-title">${esc(p.title || p.summary)}</h2>`;
    if (phone) return `<div class="prop-sheet"><div class="row" style="margin-bottom:8px"><a class="btn sm ghost back" href="${UI.navOrigin && UI.navOrigin.hash === location.hash ? UI.navOrigin.back : '#/bots/' + m.name + '/proposals'}">${icon('chev', '')}Back</a></div>
      <div class="panel"><div class="panel-body stack">${head}${UI.requesterBanner(p)}${why}${done}${action}${tech}</div></div>
      ${pending ? `<div class="sticky-decide">${buttons}</div>` : p.status === 'shadow' ? '<div class="callout small">Shadow proposals can\'t be approved.</div>' : ''}</div>`;
    return `<div class="row" style="margin-bottom:12px"><a class="btn sm ghost back" href="#/bots/${m.name}/proposals">${icon('chev', '')}All proposals</a></div>
      <div class="grid g-side">
        <div class="stack"><div class="panel"><div class="panel-body stack">${head}${UI.requesterBanner(p)}${why}${action}${tech}</div></div></div>
        <div class="stack">
          <div class="panel approve-panel"><div class="panel-head"><h3>Your decision</h3></div><div class="panel-body stack">${done}
            ${pending ? `<div class="small">${p.reversible ? `If it goes wrong: ${esc(p.reversible)}.` : 'This can\'t be undone by conductor.'}</div><div class="row approve-row">${buttons}</div><div class="muted tiny">Needs one of: <span class="mono">${(S.effective(m).operators.v || []).join(', ')}</span>${teamOf(m) ? ` (from team ${teamOf(m).name})` : ''}, with user verification. Deny can carry a reason for the bot.</div>` : p.status === 'shadow' ? '<div class="callout small">Shadow proposals can\'t be approved.</div>' : ''}
          </div></div>
          <div class="panel"><div class="panel-head"><h3>Timeline</h3></div><div class="list small">
            <div class="item">${st('ok', '')}<span class="grow">proposed by home ${m.session}</span><span class="mono tiny muted">${ago(p.at)}</span></div>
            <div class="item">${st(pending ? 'ember' : PS[p.status], '')}<span class="grow">${pending ? 'awaiting operator signature' : p.status + (p.approver ? ' · ' + esc(p.approver) : '')}</span></div></div></div>
        </div></div>`;
  }
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
  function workers(m) {
    const ws = D.workers.filter((w) => (w.bot || 'platform-sre') === m.name);
    const card = (w) => `<div class="wnode"><div class="wcard" data-s="${w.status}"><div class="row wrap" style="gap:6px">${st(w.status === 'stopped' ? 'fail' : w.status, w.status === 'stopped' ? 'stopped' : '')}<b class="mono">${w.id}</b>${w.group ? tag('group ' + w.group) : ''}<span class="taint ${w.cls}">taint: ${w.cls}</span>${w.readOnly ? tag('read-only', 'ok') : ''}<span class="mono tiny muted" style="margin-left:auto">case ${w.case}${UI.usageOn() ? ' · ' + UI.runFig({ id: w.id || w.case, cost: w.cost }) : ''} · ${ago(w.started)}</span></div>
      <div class="small" style="margin:5px 0 3px">${esc(w.brief)}</div><div class="mono tiny muted">verbs: ${w.verbs}${w.note ? ` · <span style="color:var(--wait)">${w.note}</span>` : ''}</div>
      <div class="row" style="margin-top:6px;gap:6px"><a class="btn sm" href="#/${w.status === 'run' ? 'live/r-7f29' : 'runs/' + (D.runs.find((r) => r.id === w.run) ? w.run : 'r-7e52')}">${icon('runs')}Run</a>${w.status === 'run' ? `<button class="btn sm danger" data-act="w-stop" data-w="${w.id}">Stop</button>` : ''}</div></div></div>`;
    const groups = [...new Set(ws.map((w) => w.parent))];
    return shell(m, 'workers', `<div class="grid g-side"><div class="panel"><div class="panel-head"><h3>Worker tree</h3><span class="muted small">instructions flow down, authority never grows</span></div><div class="wtree">
      <div class="wnode"><span class="sigil sm" style="--sig:${m.color}">${m.name[0]}</span><div><b>home ${m.session}</b> <span class="muted small">${m.home} · turn ${m.turns}</span></div></div>
      ${groups.map((g) => `<div class="wnode" style="opacity:.85"><span class="sigil sm" style="--sig:var(--ink-4)">${g === 'sweep' ? icon('eye') : m.name[0]}</span><div><b>${g === 'sweep' ? 'proactive sweeps' : g}</b></div></div><div class="wkids">${ws.filter((w) => w.parent === g).map(card).join('')}</div>`).join('') || '<div class="empty small">No workers yet.</div>'}
    </div></div>
    <div class="stack"><div class="callout small">A child carries its bot's verbs or fewer, and the <b>less trusted</b> of its case's requester and its parent's trust.</div><div class="callout info small">Only the parent (the home session) tells a worker. From here you can open a worker's run, or stop it.</div></div></div><details class="panel tech env-tech" style="margin-top:14px"><summary class="panel-head"><h3>Effective environment for its workers</h3><span class="muted small">runtime ${esc(S.getPath(m.cfg || {}, 'worker.runtime') || 'paseo')}, in the sandbox</span></summary><div class="panel-body">${UI.effectiveEnvHTML({ runtime: S.getPath(m.cfg || {}, 'worker.runtime') || 'paseo', sandboxed: true })}</div></details>`);
  }
  UI.act('w-stop', (b) => UI.confirm({ title: `Stop worker ${b.dataset.w}?`, body: 'Its run is cancelled with the checkpoint grace. The parent is told.', ok: 'Stop worker', danger: true }).then((ok) => { if (!ok) return; const w = D.workers.find((x) => x.id === b.dataset.w); w.status = 'stopped'; audit('bot_worker_stop', `${w.id} · case ${w.case}`); S.save(); UI.rerender(); }));

  /* ---------- Safety: every bot, in words ---------- */
  UI.route('safety', () => {
    if (!D.prefs.seenSafety) { D.prefs.seenSafety = true; S.save(); }
    const rows = D.bots.map((m) => ({ m, x: UI.botCan(m) }));
    const own = rows.reduce((a, r) => a + r.x.own.filter((i) => i.t !== 'observe').length, 0);
    return `<div class="page">${UI.head('Safety', { sub: 'what your bots may do without asking you' })}
      <div class="callout small" style="margin-bottom:14px">${icon('shield')} The agent sandbox is <b>enforcing</b>: bots read through conductor and can't reach files or the network beyond what you allowed. Below, every bot's granted actions by what happens: done on its own, asked first, or refused. ${own ? `<b>${own} action${own > 1 ? 's' : ''}</b> beyond reading run${own > 1 ? '' : 's'} without asking.` : 'Nothing beyond reading runs without asking.'}</div>
      ${UI.tierLadder()}
      <div class="stack" style="margin-top:14px">${rows.map(({ m }) => `<div class="panel"><div class="panel-head">${UI.sigil(m, 'sm')}<h3><a class="link-plain" href="#/bots/${m.name}/access">${esc(m.name)}</a></h3><span class="muted small">${teamOf(m) ? `team ${esc(teamOf(m).name)} · runs alone up to ${S.ceilingOf(m)}` : 'solo'}</span><div class="tools"><a class="btn sm ghost" href="#/bots/${m.name}/access">${icon('edit')}Change</a></div></div><div class="panel-body">${UI.botCanHTML(m, true)}</div></div>`).join('')}</div></div>`;
  }, { nav: 'security' });

  /* ---------- access (config edits through the pipeline) ---------- */
  /** What a bot can do, in words: on its own / asks you first / never (the effective tier of every granted verb). */
  UI.botCan = (m) => {
    const c = (S.staged('bots', m.name) || m.cfg || {}), sv = S.getPath(c, 'skill.verbs'), verbs = Array.isArray(sv) ? sv : sv ? Object.keys(sv) : [];
    const promote = S.getPath(c, 'autonomy.promote') || [], shadow = S.getPath(c, 'autonomy.shadow') || S.getPath((teamOf(m) || { cfg: {} }).cfg, 'autonomy.shadow');
    const out = { own: [], asks: [], never: [], shadow: !!shadow };
    const label = (v) => { const d = SCHEMA.verb(v.replace(/\*$/, '')) || null; return d ? `${d.label.replace(/^./, (x) => x.toLowerCase())} (${v})` : v.endsWith('*') ? `anything matching ${v}` : v; };
    verbs.forEach((v) => { const t = S.tierFor({ cfg: c, name: m.name }, v).tier; const it = { v, t, text: label(v) }; if (t === 'never') out.never.push(it); else if (t === 'observe' || (t === 'reversible' && promote.includes(v))) out.own.push(it); else out.asks.push(it); });
    return out;
  };
  UI.botCanHTML = (m, compact) => { const x = UI.botCan(m); const col = (title, cls, items, empty) => `<div class="can-col ${cls}"><div class="can-h">${title}</div>${items.map((i) => `<div class="can-i"><span class="tier ${i.t}">${i.t}</span><span>${esc(i.text)}</span></div>`).join('') || `<div class="muted tiny">${empty}</div>`}</div>`;
    return `<div class="can${compact ? ' compact' : ''}">${x.shadow ? '<div class="callout small">In <b>shadow mode</b>: even “on its own” actions are only recorded, never executed.</div>' : ''}<div class="can-grid">${col('Does on its own', 'own', x.own, 'Nothing: it asks before every action.')}${col('Asks you first', 'asks', x.asks, 'Nothing needs your approval.')}${col('Never', 'never', x.never, 'Nothing is refused outright.')}</div></div>`; };
  function access(m) {
    const c = staged('bots', m.name), promote = S.getPath(c, 'autonomy.promote') || [];
    const sv = S.getPath(c, 'skill.verbs'), vlist = Array.isArray(sv) ? sv : sv ? Object.keys(sv) : [];
    const team = teamOf(m), ceiling = S.ceilingOf(Object.assign({}, m, { cfg: c }));
    const verbs = vlist.map((v) => { const tf = S.tierFor(Object.assign({}, m, { cfg: c }), v); return { v, tier: tf.own || 'propose', eff: tf.tier, teamTier: tf.team }; });
    const creds = D.access.credentials.filter((x) => ((S.staged('credentials', x.name) || x.cfg || {}).identities || []).includes('bot:' + m.name));
    const logins = D.access.logins.filter((l) => l.identity === 'bot:' + m.name);
    const eff = S.effective(m);
    const stagedM = S.find('bots', m.name);
    const tierSel = (x) => `<select class="select mono tier-sel" data-on-change="acc-tier" data-m="${m.name}" data-v="${esc(x.v)}" aria-label="Tier for ${esc(x.v)}">${S.LADDER.map((t) => { const looser = x.teamTier && S.LADDER.indexOf(t) < S.LADDER.indexOf(x.teamTier); return `<option${t === x.tier ? ' selected' : ''}${looser ? ` disabled title="team ${esc(team.name)} classifies ${esc(x.v)} as ${x.teamTier}; a bot can only be stricter"` : ''}>${t}${looser ? ' (team: ' + x.teamTier + ')' : ''}</option>`; }).join('')}</select>`;
    const promoteCell = (x) => { if (!['observe', 'reversible'].includes(x.eff)) return `<span class="muted tiny" title="Only a reversible verb can run without your signature">asks</span>`; if (x.eff === 'observe') return '<span class="muted tiny">runs (read only)</span>'; const off = ceiling === 'observe' ? `Team ${team ? team.name : ''}'s ceiling is observe: no bot promotes anything` : ''; return UI.switchBtn(promote.includes(x.v), 'acc-promote', `data-m="${m.name}" data-v="${esc(x.v)}" aria-label="Let ${esc(x.v)} run without asking"${off ? ' ' + UI.off(off) : ''}`); };
    return shell(m, 'access', `<div class="panel" style="margin-bottom:14px"><div class="panel-head"><h3>What ${esc(m.name)} can do</h3><span class="muted small">from its grants, tiers and promotions${team ? `, inside team ${esc(team.name)}` : ''}</span><div class="tools"><a class="btn sm ghost" href="#/safety">All bots</a></div></div><div class="panel-body">${UI.botCanHTML(Object.assign({}, m, { cfg: c }))}</div></div>
      ${UI.tierLadder()}
      <div class="callout small" style="margin:14px 0">Grants and tiers live in config: each change here is staged (validated on a copy, shown as a diff, applied with your passkey). Wider changes are flagged.${team ? ` On team <b>${team.name}</b> a bot may run verbs without asking only up to <span class="tier ${ceiling}">${ceiling}</span>, and only as strict or stricter than the team's own classifications.` : ''}${stagedM ? ` <a class="link" href="#/config/apply">${D.pending.length} staged · review</a>` : ''}</div>
      <div class="grid g2">
        <div class="panel"><div class="panel-head"><h3>Verbs and tiers</h3><span class="muted small">skill.verbs × autonomy.tiers</span><div class="tools"><button class="btn sm" data-act="acc-addverb" data-m="${m.name}">${icon('plus')}Grant a verb</button></div></div>
          <table class="tbl compact responsive"><thead><tr><th>verb</th><th>tier</th><th>runs without asking</th><th></th></tr></thead><tbody>${verbs.map((x) => `<tr><td data-l="verb" class="mono">${x.v}${x.teamTier && x.teamTier !== x.tier ? `<div class="tiny muted">team: ${x.teamTier}</div>` : ''}</td><td data-l="tier">${tierSel(x)}</td><td data-l="runs without asking">${promoteCell(x)}</td><td class="num"><button class="icon-btn" data-act="acc-delverb" data-m="${m.name}" data-v="${esc(x.v)}" aria-label="Remove ${esc(x.v)}">${icon('trash')}</button></td></tr>`).join('') || '<tr class="empty-row"><td colspan="4">No verbs granted: it can only read what it\'s told.</td></tr>'}</tbody></table>
          <div class="panel-foot small muted">An unclassified verb is <span class="tier propose">propose</span> (asks first). Tiers looser than the team's are disabled, and validation refuses them.</div></div>
        <div class="stack">
          <div class="panel"><div class="panel-head"><h3>Sign-in credentials</h3><span class="muted small">browser.credentials naming bot:${m.name}</span><div class="tools"><button class="btn sm" data-act="cred-bind" data-m="${m.name}">${icon('plus')}Let it use a login</button></div></div><div class="list">${creds.map((x) => { const cf = S.staged('credentials', x.name) || x.cfg; return `<div class="item"><span class="icon-btn" aria-hidden="true">${icon('key')}</span><div class="grow"><b>${x.name}</b> <span class="mono tiny muted">${esc(cf.origin)}</span><div class="mono tiny muted">${['username', 'password', 'otp', 'passkey'].filter((k) => cf[k]).map((k) => `${k} ${esc(cf[k].vault)} · ${esc(cf[k].key)}`).join(' · ')}</div></div><a class="btn sm ghost" href="#/browser/logins">${icon('edit')}Logins</a><button class="btn sm ghost" data-act="cred-unbind" data-m="${m.name}" data-c="${x.name}">Remove ${esc(m.name)}</button></div>`; }).join('') || '<div class="item small muted">No sign-in credentials name this bot.</div>'}</div></div>
          <div class="panel"><div class="panel-head"><h3>Browser logins</h3><div class="tools"><a class="btn sm" href="#/browser/add">${icon('key')}Add a login</a></div></div><div class="list">${logins.map((l) => `<div class="item"><span class="icon-btn" aria-hidden="true">${icon('cookie')}</span><div class="grow"><b>${l.site}</b><div class="mono tiny muted">${l.cookies} cookies · seeded ${ago(l.seeded)} ago · operator requests only</div></div><button class="btn sm danger" data-act="login-revoke" data-site="${l.site}" data-id="${l.identity}">Revoke</button></div>`).join('') || '<div class="item small muted">No saved logins.</div>'}</div></div>
          <div class="panel"><div class="panel-head"><h3>Operators who can approve</h3><span class="muted small">${eff.operators.src}</span></div><div class="list">${(eff.operators.v || []).map((o) => `<div class="item"><span class="avatar">${o.slice(0, 2)}</span><b>${o}</b><span class="muted small grow mono">${esc((D.operatorsCfg[o] || {}).ssh_key || '')}</span>${o === 'ops' ? tag('you', 'ember') : ''}</div>`).join('')}</div></div>
        </div></div>`);
  }
  const editM = (name, fn) => { const c = Y_clone(staged('bots', name)); fn(c); const ch = S.stage('bots', name, c); UI.refreshChrome(); UI.rerender(); if (ch) UI.toast('Staged', esc(ch.summary), 'run'); };
  UI.act('acc-tier', (sel) => editM(sel.dataset.m, (c) => { const T2 = (c.autonomy = c.autonomy || {}).tiers = c.autonomy.tiers || {}; Object.keys(T2).forEach((t) => { T2[t] = T2[t].filter((v) => v !== sel.dataset.v); if (!T2[t].length) delete T2[t]; }); (T2[sel.value] = T2[sel.value] || []).push(sel.dataset.v); if (sel.value !== 'reversible' && c.autonomy.promote) c.autonomy.promote = c.autonomy.promote.filter((v) => v !== sel.dataset.v); }));
  UI.act('acc-promote', (b) => editM(b.dataset.m, (c) => { const p = (c.autonomy.promote = c.autonomy.promote || []); const i = p.indexOf(b.dataset.v); if (i >= 0) p.splice(i, 1); else p.push(b.dataset.v); }));
  const verbsOf = (c) => { const v = S.getPath(c, 'skill.verbs'); return Array.isArray(v) ? v : v ? Object.keys(v) : []; };
  const addVerb = (c, verb) => { c.skill = c.skill || {}; const v = c.skill.verbs; if (v && !Array.isArray(v)) v[verb] = {}; else c.skill.verbs = (v || []).concat([verb]); };
  const dropVerb = (c, verb) => { const v = S.getPath(c, 'skill.verbs'); if (v && !Array.isArray(v)) delete v[verb]; else if (v) c.skill.verbs = v.filter((x) => x !== verb); };
  UI.act('acc-delverb', (b) => editM(b.dataset.m, (c) => { dropVerb(c, b.dataset.v); c.autonomy = c.autonomy || {}; Object.values(c.autonomy.tiers || {}).forEach((vs) => { const i = vs.indexOf(b.dataset.v); if (i >= 0) vs.splice(i, 1); }); Object.keys(c.autonomy.tiers || {}).forEach((t) => { if (!c.autonomy.tiers[t].length) delete c.autonomy.tiers[t]; }); if (c.autonomy.promote) c.autonomy.promote = c.autonomy.promote.filter((v) => v !== b.dataset.v); }));
  const verbList = () => { const out = []; D.connectors.forEach((c) => { const d = SCHEMA.connectors[c.use]; if (d) { out.push(`${c.name}.*`); Object.keys(d.verbs).forEach((v) => out.push(`${c.name}.${v}`)); } }); return out; };
  UI.act('acc-addverb', (b) => { const c0 = staged('bots', b.dataset.m); UI.form({ caps: `bots.${b.dataset.m}.skill.verbs`, title: 'Grant a verb', fields: [{ k: 'verb', type: 'enum', req: true, options: verbList().filter((v) => !verbsOf(c0).includes(v)) }, { k: 'tier', type: 'enum', req: true, options: ['observe', 'reversible', 'propose', 'commit_money', 'never'], def: 'propose', help: 'Adding a verb widens what the bot can do.' }], submit: 'Stage grant', onSubmit: (v) => { editM(b.dataset.m, (c) => { addVerb(c, v.verb); c.autonomy = c.autonomy || {}; const T2 = (c.autonomy.tiers = c.autonomy.tiers || {}); (T2[v.tier] = T2[v.tier] || []).push(v.verb); }); } }); });
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
    { k: 'description', type: 'string', mono: false, sec: 'Identity & charter' },
    { k: 'team', type: 'enum', label: 'Team', options: () => S.view().teams.map((t) => t.name), create: 'team', createLabel: 'New team', help: 'Empty for a solo bot. On a team, defaults come from the team and this bot can only narrow them.' },
    { k: 'folder', type: 'enum', label: 'Folder', options: () => UI.allFolders(), create: 'folder', createLabel: 'New folder', help: 'Organization only. A bot on a team shows under its team instead.', solo: true },
    { k: 'operators', type: 'list', label: 'Operators (approvers)', help: 'Leave empty to inherit the team\'s.' },
    { k: 'escalation', type: 'string', mono: false },
    { k: 'autonomy.shadow', type: 'bool', label: 'Shadow mode', on: 'record proposals; nothing executes', sec: 'Autonomy' },
    { k: 'autonomy.approval_ttl', type: 'duration', label: 'Approval expires after', help: 'An unapproved proposal lapses.' },
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
    const fields = DF.map((f) => { const fd = Object.assign({}, f, { options: typeof f.options === 'function' ? f.options() : f.options }); const head = f.sec && f.sec !== sec ? (`${sec === 'Identity & charter' ? charter : ''}</div></div><div class="panel"><div class="panel-head"><h3>${(sec = f.sec)}</h3></div><div class="panel-body def-grid">`) : ''; if (f.api && !UI.isApi()) return head; if (f.solo && dv.draft.team) return head + `<div class="field"><label>Folder</label><div class="hint inh-hint">${icon('folder')} shows under team ${esc(dv.draft.team)}${UI.folderOfTeam(dv.draft.team) ? ' in ' + esc(UI.folderOfTeam(dv.draft.team)) : ''}</div></div>`; const html = UI.fieldHTML(Object.assign({}, fd, { k: 'df.' + f.k }), S.getPath(dv.draft, f.k)); return head + html.replace('<div class="ferr"', (inhMap[f.k] && team ? inh(inhMap[f.k]) : '') + '<div class="ferr"'); }).join('');
    const yaml = YAML.stringify({ bots: { [name]: dv.draft } });
    const changed = !YAML.eq(dv.draft, dv.base) || (isNew ? !!dv.newName : dv.charter !== (D.charters[m.name] || ''));
    const body = `<div class="grid g2" style="align-items:start">
        <div class="stack def-form" data-bot="${esc(key)}"><div class="panel"><div class="panel-head"><h3>Identity &amp; charter</h3></div><div class="panel-body def-grid">
          ${isNew ? UI.fieldHTML({ k: 'df.$name', type: 'string', req: true, label: 'Name', ph: 'e.g. data-steward' }, dv.newName) : ''}
          ${fields}
        </div></div></div>
        <div class="stack def-yaml"><div class="panel"><div class="panel-head"><h3>As YAML</h3><span class="muted small">config.yaml · <span class="mono">bots.${esc(name)}</span></span><div class="tools">${errs.length ? st('fail', `${errs.length} error${errs.length > 1 ? 's' : ''}`) : st('ok', 'validates')}</div></div><div class="panel-body tight" id="df-yaml">${UI.yaml(yaml)}</div>
          ${errs.length || warns.length ? `<div class="panel-body" style="border-top:1px solid var(--line)">${errs.map((e) => `<div class="callout danger small" style="margin-bottom:6px">${esc(e)}</div>`).join('')}${warns.map((e) => `<div class="callout warn small" style="margin-bottom:6px">${esc(e)}</div>`).join('')}</div>` : ''}
          <div class="panel-foot"><button class="btn ghost" data-act="df-reset" ${changed ? '' : UI.off('No edits')}>${icon('undo')}Reset</button><button class="btn primary" style="margin-left:auto" data-act="df-review" ${!changed ? UI.off('Change something first') : errs.length ? UI.off('Fix the errors first') : ''}>${icon('check')}Review changes</button></div></div></div></div>`;
    return isNew ? `<div class="page">${UI.head('New bot', { crumbs: '<a href="#/bots">Bots</a>', sub: dv.team ? `on team ${esc(dv.team)} · starts in shadow mode` : 'solo or on a team · starts in shadow mode' })}${body}</div>` : shell(m, 'settings', body);
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
    if (isNew && !/^[a-z][a-z0-9-]*$/.test(name || '')) { UI.toast('Name the bot first', 'lowercase letters, digits and dashes', 'fail'); return; }
    if (isNew && M(name)) { UI.toast('A bot by that name exists', '', 'fail'); return; }
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
    switch (tab || 'chat') {
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
