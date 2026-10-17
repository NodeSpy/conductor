/* New bot, guided: Purpose → Watches → Tells you where → What it may do →
 * Review. Each answer becomes real config: bots.<name> (description,
 * escalation, team or folder, skill.verbs, autonomy.tiers/promote/shadow)
 * plus one trigger per watched event whose step hands the event to the bot
 * (bot: { name, title }). Everything else (runtime, operators, approval TTL)
 * sits under Advanced; the full definition form is one link away. Loaded
 * before bots.js so #/bots/new isn't taken by the bot page route. */
(function () {
  const { icon, esc, tag } = UI;
  const PURPOSES = {
    incidents: { icon: 'warn', title: 'Watch incidents and triage them', desc: 'Reads the alert, checks what changed, tells you what it thinks is wrong.', name: 'incident-bot', description: 'Triages incidents', events: ['pagerduty.incident'] },
    reviews: { icon: 'branch', title: 'Review pull requests', desc: 'Reads each PR when you ask for a review and leaves comments.', name: 'review-bot', description: 'Reviews pull requests', events: ['github.review_requested'] },
    ci: { icon: 'obj-check', title: 'Look into failing CI', desc: 'When a check fails, reads the logs and explains or proposes a fix.', name: 'ci-bot', description: 'Looks into failing CI checks', events: ['github.check_failed'] },
    custom: { icon: 'sparkle', title: 'Something else', desc: 'Start blank and pick what it watches yourself.', name: 'my-bot', description: '', events: [] },
  };
  const PRESETS = {
    read: { title: 'Read only', desc: 'Looks things up. Anything that changes something is never done.' },
    post: { title: 'Can comment and post', desc: 'Looks things up, comments and posts on its own. Asks you before anything else.' },
    ask: { title: 'Asks before anything else', desc: 'Looks things up on its own. Comments, posts and every other change wait for your approval.' },
  };
  const STEPS = ['Purpose', 'Watches', 'Tells you where', 'What it may do', 'Review'];
  const fresh = () => ({ at: 0, purpose: null, name: '', description: '', team: '', folder: '', events: new Set(), repos: 'acme/api', sweep: false, every: '4h', tell: 'dashboard', slack: '', channel: '#ops', preset: 'ask', shadow: true, runtime: 'claude-cli', operators: [], ttl: '24h' });
  let bw = fresh();

  const conns = () => S.view().connectors;
  const instOf = (use) => (conns().find((c) => c.use === use) || {}).name;
  /** Events the configured connectors can deliver: [{ key: 'github.check_failed', on: 'gh.check_failed', label, use }]. */
  const events = () => conns().flatMap((c) => Object.entries((SCHEMA.connectors[c.use] || {}).events || {}).map(([ev, d]) => ({ key: `${c.use}.${ev}`, on: `${c.name}.${ev}`, label: d.label, use: c.use, inst: c.name })));
  const chosenEvents = () => events().filter((e) => bw.events.has(e.key));
  const repos = () => bw.repos.split(/[\s,]+/).filter(Boolean);
  const ceiling = () => (bw.team ? S.ceilingOf({ cfg: { team: bw.team } }) : 'reversible');

  /** The verbs and tiers a preset gives, for the services it watches (and Slack, if it tells you there). */
  function grants() {
    const uses = new Set(chosenEvents().map((e) => e.use)); if (bw.tell === 'slack') uses.add('slack');
    const reads = [], acts = [], tellVerb = bw.tell === 'slack' && bw.slack ? `${bw.slack}.post` : null;
    uses.forEach((u) => { const i = instOf(u); if (!i) return; if (u === 'github') { reads.push(`${i}.get_*`); acts.push(`${i}.comment`); } if (u === 'pagerduty') { reads.push(`${i}.get_*`); acts.push(`${i}.note`); } if (u === 'slack' && i !== bw.slack) acts.push(`${i}.post`); });
    const promoteOK = ceiling() !== 'observe';
    const tiers = { observe: reads.slice() }, promote = [];
    if (bw.preset === 'read') { if (acts.length) tiers.never = acts.slice(); }
    else if (bw.preset === 'post') { tiers.reversible = acts.slice(); if (promoteOK) promote.push(...acts); }
    else if (acts.length) tiers.propose = acts.slice();
    if (tellVerb) { tiers.reversible = (tiers.reversible || []).concat(tellVerb); if (promoteOK) promote.push(tellVerb); }
    Object.keys(tiers).forEach((k) => { if (!tiers[k].length) delete tiers[k]; });
    return { verbs: [...reads, ...acts, ...(tellVerb ? [tellVerb] : [])], tiers, promote, promoteOK };
  }
  function botCfg() {
    const g = grants(), c = {};
    if (bw.description) c.description = bw.description;
    if (bw.team) c.team = bw.team; else if (bw.folder) c.folder = bw.folder;
    c.escalation = bw.tell === 'slack' && bw.slack ? `Tell the operator in ${bw.channel} (${bw.slack}.post). Anything urgent also goes to Needs you.` : 'Tell the operator in the dashboard: its chat and Needs you.';
    if (bw.operators.length) c.operators = bw.operators.slice();
    c.worker = { runtime: bw.runtime };
    if (g.verbs.length) c.skill = { verbs: g.verbs };
    c.autonomy = { shadow: bw.shadow }; if (Object.keys(g.tiers).length) c.autonomy.tiers = g.tiers; if (g.promote.length) c.autonomy.promote = g.promote; if (bw.ttl && bw.ttl !== '24h') c.autonomy.approval_ttl = bw.ttl;
    if (bw.sweep) c.proactive = { every: bw.every, quiet_hours: '22:00-07:00', budget: { max_runs_per_day: 6 } };
    return c;
  }
  const trigName = (e) => `${bw.name}-${e.key.split('.')[1].replace(/_/g, '-')}`;
  const trigYAML = (e) => [`name: ${trigName(e)}`, ...(bw.team ? [] : bw.folder ? [`folder: "${bw.folder}"`] : []), `on: ${e.on}`, ...(e.use === 'github' && repos().length ? [`filter: { repo: [${repos().join(', ')}] }`] : []), 'steps:', '  - id: tell', `    bot: { name: ${bw.name}, title: "{{.title}}" }`].join('\n') + '\n';

  const nameErr = () => (!/^[a-z][a-z0-9-]*$/.test(bw.name) ? 'lowercase letters, digits and dashes' : S.view().bots.some((b) => b.name === bw.name) ? 'a bot by that name exists' : '');
  function stepHTML() {
    const v = S.view();
    if (bw.at === 0) return `<div class="choice-grid">${Object.entries(PURPOSES).map(([k, p]) => UI.choice({ act: 'bw-purpose', attrs: `data-p="${k}"`, on: bw.purpose === k, icon: p.icon, title: p.title, desc: p.desc })).join('')}</div>
      ${bw.purpose ? `<div class="stack" style="gap:12px;margin-top:6px">
        <div class="field${nameErr() ? ' bad' : ''}" data-field="name"><label for="bw-name">Name <span class="req">*</span></label><input class="input mono" id="bw-name" value="${esc(bw.name)}" data-on-input="bw-set" data-k="name" autocomplete="off"><div class="hint">How you'll refer to it, in chat and in config.</div><div class="ferr" role="alert">${nameErr()}</div></div>
        <div class="field"><label for="bw-desc">What it's for, in a line</label><input class="input" id="bw-desc" value="${esc(bw.description)}" data-on-input="bw-set" data-k="description" placeholder="Triages incidents for the API"></div>
        <div class="row wrap" style="gap:12px"><div class="field grow"><label for="bw-team">Team</label><select class="select" id="bw-team" data-on-change="bw-set" data-k="team"><option value="">none (a solo bot)</option>${v.teams.map((t) => `<option value="${esc(t.name)}"${bw.team === t.name ? ' selected' : ''}>${esc(t.name)}</option>`).join('')}</select><div class="hint">A team shares approvers, limits and knowledge; its bots can only be stricter.</div></div>
          ${bw.team ? '' : `<div class="field grow"><label for="bw-folder">Folder <span class="opt">optional, organization only</span></label><input class="input mono" id="bw-folder" value="${esc(bw.folder)}" data-on-input="bw-set" data-k="folder" placeholder="work/platform"></div>`}</div></div>` : ''}`;
    if (bw.at === 1) { const evs = events(), gh = chosenEvents().some((e) => e.use === 'github');
      return `<p class="muted small" style="margin:0">Pick the events it should hear about. Each becomes a trigger that hands the event to ${esc(bw.name)}.</p>
      <div class="panel"><div class="list">${evs.map((e) => `<label class="item click bw-ev"><input type="checkbox" class="cbx" data-on-change="bw-ev" data-k="${e.key}"${bw.events.has(e.key) ? ' checked' : ''}><span class="grow"><b>${esc(e.label)}</b> <span class="muted small">from ${esc(e.inst)}</span></span><span class="mono tiny muted">${esc(e.on)}</span></label>`).join('') || '<div class="empty small">No connected service delivers events yet. <a class="link" href="#/connectors/connectors">Connect one</a>.</div>'}</div></div>
      ${gh ? `<div class="field"><label for="bw-repos">Which repos</label><input class="input mono" id="bw-repos" value="${esc(bw.repos)}" data-on-input="bw-set" data-k="repos" placeholder="acme/api, acme/web"><div class="hint">GitHub events from these repos only.</div></div>` : ''}
      <label class="row" style="gap:10px">${UI.switchBtn(bw.sweep, 'bw-sweep', 'aria-label="Also check in on its own"')}<span><b>Also check in on its own</b> <span class="muted small">every</span> <select class="select sm" style="width:auto;display:inline-block;min-height:28px;padding:2px 26px 2px 8px" data-on-change="bw-set" data-k="every" aria-label="How often">${['1h', '4h', '12h', '24h'].map((x) => `<option${bw.every === x ? ' selected' : ''}>${x}</option>`).join('')}</select> <span class="muted small">(quiet 22:00–07:00, at most 6 a day)</span></span></label>
      ${!bw.events.size && !bw.sweep ? `<div class="callout wait small">${icon('warn')} <span>It won't do anything until it watches something. You can create it anyway and add events later from its page.</span></div>` : ''}`; }
    if (bw.at === 2) { const slacks = conns().filter((c) => c.use === 'slack');
      return `<div class="choice-grid two">${UI.choice({ act: 'bw-tell', attrs: 'data-v="dashboard"', on: bw.tell === 'dashboard', icon: 'grid', title: 'Here, in the dashboard', desc: 'Its chat, and Needs you for anything you have to decide.' })}${UI.choice({ act: 'bw-tell', attrs: 'data-v="slack"', on: bw.tell === 'slack', icon: 'send', title: 'In a Slack channel', desc: 'Posts updates to a channel you pick (decisions still come to Needs you).', off: slacks.length ? '' : 'No Slack connector yet; add one under Connectors.' })}</div>
      ${bw.tell === 'slack' ? `<div class="row wrap" style="gap:12px"><div class="field"><label for="bw-slack">Slack connector</label><select class="select mono" id="bw-slack" data-on-change="bw-set" data-k="slack">${slacks.map((c) => `<option${bw.slack === c.name ? ' selected' : ''}>${esc(c.name)}</option>`).join('')}</select></div><div class="field grow"><label for="bw-channel">Channel</label><input class="input mono" id="bw-channel" value="${esc(bw.channel)}" data-on-input="bw-set" data-k="channel"></div></div>` : ''}`; }
    if (bw.at === 3) { const g = grants(), draft = { name: bw.name, cfg: botCfg() };
      return `<div class="choice-grid three">${Object.entries(PRESETS).map(([k, p]) => UI.choice({ act: 'bw-preset', attrs: `data-v="${k}"`, on: bw.preset === k, icon: k === 'read' ? 'eye' : k === 'post' ? 'send' : 'lock', title: p.title, desc: p.desc })).join('')}</div>
      ${!g.promoteOK ? `<div class="callout small">${icon('lock')} Team ${esc(bw.team)} lets no bot act on its own (its ceiling is <b>observe</b>), so comments and posts will ask you first.</div>` : ''}
      <div class="panel"><div class="panel-head"><h3>What ${esc(bw.name)} can do</h3><span class="muted small">from the services it watches${bw.tell === 'slack' ? ' and the channel it tells you in' : ''}</span></div><div class="panel-body">${g.verbs.length ? UI.botCanHTML(draft, true) : '<div class="muted small">Nothing yet: it watches no service it could act on. It can still chat with you.</div>'}</div></div>
      <label class="row" style="gap:10px">${UI.switchBtn(bw.shadow, 'bw-shadow', 'aria-label="Start in shadow mode"')}<span><b>Start in shadow mode</b> <span class="muted small">it records what it would do, and nothing runs until you turn shadow off on its page</span></span></label>
      <details class="panel adv"><summary class="panel-head"><h3>Advanced</h3><span class="muted small">where its workers run, who approves, how long an approval lasts</span></summary><div class="panel-body stack" style="gap:12px">
        <div class="row wrap" style="gap:12px"><div class="field"><label for="bw-rt">Worker runtime</label><select class="select mono" id="bw-rt" data-on-change="bw-set" data-k="runtime">${S.view().runtimes.map((r) => `<option${bw.runtime === r.name ? ' selected' : ''}>${esc(r.name)}</option>`).join('')}</select></div>
          <div class="field"><label for="bw-ttl">An approval lasts</label><select class="select mono" id="bw-ttl" data-on-change="bw-set" data-k="ttl">${['1h', '8h', '24h', '72h'].map((x) => `<option${bw.ttl === x ? ' selected' : ''}>${x}</option>`).join('')}</select></div></div>
        <div class="field"><label>Who may approve <span class="opt">none ticked = ${bw.team ? `team ${esc(bw.team)}'s approvers` : 'every operator'}</span></label><div class="row wrap" style="gap:12px">${Object.keys(S.view().operatorsCfg || {}).map((o) => `<label class="row small"><input type="checkbox" class="cbx" data-on-change="bw-op" data-k="${esc(o)}"${bw.operators.includes(o) ? ' checked' : ''}>${esc(o)}</label>`).join('')}</div></div>
        <a class="link small" href="#/bots/new/settings">Open the full definition form instead</a></div></details>`; }
    const c = botCfg(), evs = chosenEvents();
    return `<div class="panel"><div class="list">
        <div class="item"><span class="caps" style="width:120px">bot</span><span class="grow"><b class="mono">${esc(bw.name)}</b> ${bw.description ? `<span class="muted">· ${esc(bw.description)}</span>` : ''}${bw.team ? ' ' + tag('team ' + bw.team) : ''}${c.autonomy.shadow ? ' ' + tag('shadow', 'wait') : ''}</span></div>
        <div class="item"><span class="caps" style="width:120px">watches</span><span class="grow">${evs.map((e) => esc(e.label)).join(', ') || '<span class="muted">nothing yet</span>'}${bw.sweep ? `${evs.length ? '; ' : ''}checks in every ${bw.every}` : ''}</span></div>
        <div class="item"><span class="caps" style="width:120px">tells you</span><span class="grow">${bw.tell === 'slack' ? `in ${esc(bw.channel)} via ${esc(bw.slack)}` : 'in the dashboard'}</span></div>
        <div class="item"><span class="caps" style="width:120px">may</span><span class="grow">${esc(PRESETS[bw.preset].title)}</span></div></div></div>
      <div class="panel"><div class="panel-head"><h3>The config this writes</h3><span class="muted small">bots.${esc(bw.name)}${evs.length ? ` and ${evs.length} trigger${evs.length > 1 ? 's' : ''}` : ''} · applied through the diff</span></div><div class="panel-body">${UI.yaml(YAML.stringify({ bots: { [bw.name]: c } }) + (evs.length ? '\ntriggers:\n' + evs.map((e) => '  - ' + trigYAML(e).trimEnd().split('\n').join('\n    ') + '\n').join('') : ''))}</div></div>`;
  }
  const canNext = () => (bw.at === 0 ? (bw.purpose && !nameErr() ? '' : !bw.purpose ? 'Pick what it should do first' : 'Fix the name first') : bw.at === 2 && bw.tell === 'slack' && !bw.slack ? 'Pick a Slack connector' : '');
  UI.route('bots/new', () => { const why = canNext();
    return `<div class="page wizard">${UI.head('New bot', { crumbs: '<a href="#/bots">Bots</a>', sub: 'an AI teammate with memory: it watches what you pick, tells you what it finds, and asks before acting', actions: `<a class="btn ghost" href="#/bots" data-act="bw-cancel">Cancel</a>` })}
      ${UI.stepper(STEPS, bw.at)}
      <div class="stack bw-body" style="gap:14px">${stepHTML()}</div>
      <div class="wizard-foot">${bw.at ? `<button class="btn" data-act="bw-back">${icon('chev', 'flip')}Back</button>` : ''}<span class="grow"></span>
        ${bw.at < STEPS.length - 1 ? `<button class="btn primary" ${why ? UI.off(why) : 'data-act="bw-next"'}>Next</button>` : `<button class="btn primary" data-act="bw-create">${icon('check')}Review and apply</button>`}</div></div>`; }, { nav: 'bots' });
  const redraw = () => UI.rerender();
  UI.act('bw-purpose', (b) => { const p = PURPOSES[b.dataset.p], prev = bw.purpose && PURPOSES[bw.purpose]; bw.purpose = b.dataset.p; if (!bw.name || (prev && bw.name === prev.name)) bw.name = p.name; if (!bw.description || (prev && bw.description === prev.description)) bw.description = p.description; bw.events = new Set(p.events.filter((k) => events().some((e) => e.key === k))); redraw(); });
  UI.act('bw-set', (el) => { bw[el.dataset.k] = el.value; if (el.tagName === 'SELECT') redraw(); else { const f = el.closest('.field'); if (el.dataset.k === 'name' && f) { const e = nameErr(); f.classList.toggle('bad', !!e); f.querySelector('.ferr').textContent = e; const nx = UI.$('.wizard-foot .btn.primary'); if (nx) { const why = canNext(); if (why) { nx.setAttribute('aria-disabled', 'true'); nx.dataset.reason = why; nx.removeAttribute('data-act'); } else { nx.removeAttribute('aria-disabled'); nx.dataset.act = 'bw-next'; } } } } });
  UI.act('bw-ev', (el) => { el.checked ? bw.events.add(el.dataset.k) : bw.events.delete(el.dataset.k); redraw(); });
  UI.act('bw-op', (el) => { bw.operators = el.checked ? bw.operators.concat(el.dataset.k) : bw.operators.filter((o) => o !== el.dataset.k); });
  UI.act('bw-sweep', () => { bw.sweep = !bw.sweep; redraw(); });
  UI.act('bw-shadow', () => { bw.shadow = !bw.shadow; redraw(); });
  UI.act('bw-tell', (b) => { bw.tell = b.dataset.v; if (bw.tell === 'slack' && !bw.slack) bw.slack = instOf('slack') || ''; redraw(); });
  UI.act('bw-preset', (b) => { bw.preset = b.dataset.v; redraw(); });
  UI.act('bw-next', () => { if (canNext()) return; bw.at = Math.min(STEPS.length - 1, bw.at + 1); redraw(); });
  UI.act('bw-back', () => { bw.at = Math.max(0, bw.at - 1); redraw(); });
  UI.act('bw-cancel', () => { bw = fresh(); });
  UI.act('bw-create', () => { const name = bw.name; S.stage('bots', name, botCfg(), `new bot ${name}`); chosenEvents().forEach((e) => S.stage('workflows', trigName(e), trigYAML(e), `trigger ${trigName(e)} hands ${e.on} to ${name}`)); D.prefs.returnTo = { href: `#/bots/${name}`, label: name }; bw = fresh(); UI.staged(D.pending[D.pending.length - 1]); });
})();
