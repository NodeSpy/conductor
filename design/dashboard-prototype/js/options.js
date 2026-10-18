/* Options, not product: the frames of browse-options.html. Each frame is the real shell (rail, top bar, tab bar)
 * around one direction, drawn with the prototype's own CSS and helpers on deterministic fake data at a chosen scale.
 *   browse-options-frame.html?what=bots|workflows|facts|newbot&dir=A|B|C&n=6&theme=dark
 * Nothing here is wired to the store: it's for choosing a direction, then the chosen one is built properly. */
(function () {
  const { icon, esc } = UI;
  const q = new URLSearchParams(location.search);
  const WHAT = q.get('what') || 'bots', DIR = q.get('dir') || 'A', N = +(q.get('n') || 6), THEME = q.get('theme') || 'dark';
  const phone = () => innerWidth <= 720;

  /* ---------- a deterministic fake fleet ---------- */
  let seed = 7; const rnd = () => ((seed = (seed * 16807) % 2147483647) / 2147483647); const pick = (xs) => xs[Math.floor(rnd() * xs.length)];
  const FOLDERS = ['work/platform', 'work/billing', 'work/web', 'work/data', 'ops/oncall', 'ops/security', 'personal'];
  const TEAMS = { 'work/platform': 'platform', 'work/billing': 'billing', 'work/web': 'web-ops', 'work/data': 'data-eng', 'ops/security': 'sec' };
  const ROLES = ['sre', 'triage', 'scribe', 'guard', 'janitor', 'forecaster', 'reviewer', 'watcher', 'fixer', 'auditor', 'herald', 'sweeper'];
  const AREAS = ['platform', 'billing', 'web', 'data', 'api', 'infra', 'release', 'deps', 'cost', 'oncall', 'search', 'mobile', 'auth', 'edge', 'ml'];
  const TASKS = ['SEV2 latency on checkout', 'Rotate the staging DB password', 'Weekly dependency bumps', 'Safari flex-gap bug on checkout', 'October cloud spend forecast', 'A 5xx burst on the api', 'Release notes for 2.9', 'Stale feature flags', 'Disk filling on gpu-box', 'Flaky TestRefund on main', 'Unused IAM keys', 'Slow search queries'];
  const ASKS = ['Replace the production web server', 'Post the monthly forecast to #incidents', 'Merge the dependency bump to main', 'Rotate the production API key', 'Delete 14 stale feature flags', 'Scale the search cluster to 6 nodes'];
  function fleet(n) {
    seed = 7 + n; const used = new Set(), out = [];
    const needN = n <= 6 ? 1 : n <= 40 ? 3 : 6, workN = n <= 6 ? 2 : n <= 40 ? 7 : 18;
    for (let i = 0; i < n; i++) { let name; do name = `${pick(AREAS)}-${pick(ROLES)}`; while (used.has(name)); used.add(name);
      const folder = pick(FOLDERS), team = rnd() < 0.7 ? TEAMS[folder] : null;
      out.push({ name, folder, team, need: i < needN ? 1 + (i % 2) : 0, working: i >= needN && i < needN + workN, task: TASKS[i % TASKS.length], ask: ASKS[i % ASKS.length], idle: 5 + Math.floor(rnd() * 4000), practice: i === needN + workN }); }
    if (n === 6) ['platform-sre', 'billing-sre', 'web-triage', 'release-scribe', 'homelab', 'platform-data'].forEach((x, i) => (out[i].name = x));
    return out;
  }
  const WF_SENT = [['ci-fixer', 'When a check fails on a repo, an agent fixes it and pushes'], ['pr-review', 'When someone asks for a review, a panel of agents reviews the PR'], ['release-notes', 'When a release is drafted, an agent writes its notes'], ['dep-bump', 'Every Monday, bump dependencies and open a PR if tests pass'], ['incident-intake', 'When PagerDuty pages, open a task for the on-call bot'], ['nightly-backup', 'Every night at 02:00, back up the config and the knowledge'], ['stale-flags', 'Every Friday, list feature flags nobody has touched in 90 days'], ['cost-report', 'On the 1st, post last month’s cloud spend to #finance'], ['triage-issues', 'When an issue is opened, label it and assign an owner']];
  const PACKS = ['pr-review-team', 'dep-hygiene', 'incident-scribe', 'sre-bot'];
  function flows(n) {
    seed = 11 + n; const out = [];
    for (let i = 0; i < n; i++) { const [base, sent] = WF_SENT[i % WF_SENT.length], k = Math.floor(i / WF_SENT.length);
      const src = i % 5 === 1 ? { kind: 'pack', pack: PACKS[i % PACKS.length] } : { kind: 'yours' };
      const failing = n <= 9 ? i === 0 || i === 2 : i % 37 === 0 || i % 53 === 4, running = !failing && (n <= 9 ? i === 1 || i === 3 : i % 23 === 5), broken = n > 9 && i % 61 === 7;
      out.push({ name: k ? `${base}-${k + 1}` : base, sent, src, folder: pick(FOLDERS), failing, running, broken, last: 2 + Math.floor(rnd() * 900), runs24: Math.floor(rnd() * 40), fails: failing ? 1 + (i % 3) : 0, err: ['2 tests failed in TestCheckout', 'its budget cap stopped it', 'the GitHub token expired', 'a step timed out after 30 min'][i % 4] }); }
    return out;
  }
  const ago = (m) => (m < 60 ? `${m} min ago` : m < 1440 ? `${Math.floor(m / 60)} h ago` : Math.floor(m / 1440) === 1 ? 'yesterday' : `${Math.floor(m / 1440)} days ago`);
  const initials = (n) => n.split(/[-_]/).map((w) => w[0]).join('').slice(0, 2);
  const path = (f) => f.split('/').join(' ▸ ');

  /* ---------- the shared pieces every direction uses ---------- */
  const botRow = (b, o = {}) => `<li class="dl-need bot-li${b.need ? ' attn' : ''}"${b.need ? ' data-s="ember"' : ''}><a class="dl-need-a" href="#/opt"><span class="dl-ng"><span class="dl-bot-av sm">${esc(initials(b.name))}</span></span>
    <span class="dl-nt"><b>${esc(b.name)}</b><span class="dl-why">${b.need ? `Asks you: ${esc(b.ask)}` : b.working ? `Working on: ${esc(b.task)}` : b.practice ? 'Practice mode: proposes, never acts' : `Idle · last active ${ago(b.idle)}`}</span>${o.where === false ? '' : `<span class="bot-where">${esc(path(b.folder))}${b.team ? ` · team ${esc(b.team)}` : ''}</span>`}</span>
    <span class="dl-age">${b.working ? UI.statusMark('run', 'Running') : b.practice ? UI.statusMark('shadow', 'Practice') : b.need ? '' : ''}</span><span class="dl-na">${b.need ? UI.statusMark('ember', `${b.need} need${b.need === 1 ? 's' : ''} you`) : ''}</span></a></li>`;
  const wfRow = (w) => `<li class="dl-need${w.failing || w.broken ? ' attn' : ''}"${w.failing ? ' data-s="fail"' : w.broken ? ' data-s="wait"' : ''}><a class="dl-need-a opt-wf" href="#/opt"><span class="dl-ng">${w.failing ? UI.mark('fail', { lg: true }) : w.broken ? UI.mark('wait', { lg: true }) : w.running ? UI.mark('run', { lg: true }) : ''}</span>
    <span class="dl-nt"><b>${esc(w.name)}</b><span class="${w.failing || w.broken ? 'reason' : 'dl-why'}">${w.failing ? `Failed ${w.fails}× today: ${esc(w.err)}` : w.broken ? 'Not running: it needs a PagerDuty connection' : esc(w.sent)}</span></span>
    <span class="dl-age">${w.running ? 'running now' : `ran ${ago(w.last)}`}</span><span class="dl-na">${w.failing ? '<span class="btn sm secondary row-act">See why</span>' : w.broken ? '<span class="btn sm secondary row-act">Fix it</span>' : ''}</span></a></li>`;
  const card = (title, sub, body, extra = '') => `<section class="dl-card${extra}"><header class="dl-ch"><h2>${title}</h2>${sub ? `<span class="dl-sub">${sub}</span>` : ''}</header>${body}</section>`;
  const tally = (xs) => { const m = new Map(); xs.forEach((x) => x && m.set(x, (m.get(x) || 0) + 1)); return [...m].map(([v, n]) => ({ v, label: v, n })); };
  const st = { q: '', status: [], folder: '', team: [], source: [], trigger: [] };
  const fbBots = (bots) => UI.filterBar({ id: 'opt', state: st, placeholder: 'Find a bot, or filter: team:platform folder:work/platform', label: 'Find and filter bots', fields: [
    { k: 'status', label: 'Status', multi: true, keepOrder: true, plural: 'statuses', options: () => [{ v: 'needs-you', label: 'Needs you', n: bots.filter((b) => b.need).length }, { v: 'working', label: 'Working', n: bots.filter((b) => b.working).length }, { v: 'idle', label: 'Idle', n: bots.filter((b) => !b.need && !b.working).length }] },
    { k: 'team', label: 'Team', multi: true, options: () => tally(bots.map((b) => b.team)) },
    { k: 'folder', label: 'Folder', path: true, allLabel: 'Every folder', find: 'Find a folder', options: () => FOLDERS.map((f) => ({ v: f, label: f, n: bots.filter((b) => b.folder.startsWith(f)).length })) }], onChange: () => {} });
  const fbFlows = (wfs) => UI.filterBar({ id: 'opt', state: st, placeholder: 'Find a workflow, or filter: status:failing source:pack', label: 'Find and filter workflows', fields: [
    { k: 'status', label: 'Status', multi: true, keepOrder: true, plural: 'statuses', options: () => [{ v: 'failing', label: 'Failing', n: wfs.filter((w) => w.failing).length }, { v: 'not-running', label: 'Not running', n: wfs.filter((w) => w.broken).length }, { v: 'running', label: 'Running now', n: wfs.filter((w) => w.running).length }, { v: 'healthy', label: 'Healthy', n: wfs.filter((w) => !w.failing && !w.broken).length }] },
    { k: 'source', label: 'Source', multi: true, keepOrder: true, options: () => [{ v: 'yours', label: 'Yours', n: wfs.filter((w) => w.src.kind === 'yours').length }].concat(tally(wfs.filter((w) => w.src.kind === 'pack').map((w) => w.src.pack)).map((o) => ({ ...o, label: `From ${o.v}` }))) },
    { k: 'trigger', label: 'Starts on', multi: true, plural: 'triggers', options: () => [{ v: 'schedule', label: 'A schedule' }, { v: 'github', label: 'A GitHub event' }, { v: 'pagerduty', label: 'A page' }, { v: 'manual', label: 'Only when you run it' }] },
    { k: 'folder', label: 'Folder', path: true, allLabel: 'Every folder', find: 'Find a folder', options: () => FOLDERS.map((f) => ({ v: f, label: f })) }], onChange: () => {} });
  const head = (h, actions = '') => `<div class="dl-phead"><div><h1>${h}</h1></div>${actions ? `<div class="dl-actions tight">${actions}</div>` : ''}</div>`;
  const foldRow = (n, word, what) => `<button type="button" class="opt-fold"><span><b>${n} ${word}</b> · ${what}</span><span class="dl-link">Show</span></button>`;

  /* ---------- Bots ---------- */
  const BOTS = {
    // A (the suggestion): exceptions first, the rest a count; find by search; folder and team are filters, teams have pages
    A: (bots) => { const need = bots.filter((b) => b.need), work = bots.filter((b) => b.working), rest = bots.filter((b) => !b.need && !b.working), teams = tally(bots.map((b) => b.team));
      return `${head('Bots', `<a class="btn primary">${icon('plus')}New bot</a>`)}${fbBots(bots)}
      ${need.length ? card('Need you', need.length, `<ul class="dl-list">${need.map((b) => botRow(b)).join('')}</ul>`) : ''}
      ${card('Working now', work.length, `<ul class="dl-list">${work.slice(0, 8).map((b) => botRow(b)).join('')}</ul>${work.length > 8 ? `<p class="dl-foot-note"><a class="dl-link">${work.length - 8} more working →</a></p>` : ''}`, ' opt-gap')}
      <div class="opt-gap">${foldRow(rest.length, rest.length === 1 ? 'other bot' : 'other bots', 'idle or in practice mode · find one with search')}</div>
      <p class="opt-teams">${icon('obj-team')}Teams: ${teams.map((t) => `<a class="dl-link">${esc(t.v)}</a> <span class="dl-sub">${t.n}</span>`).join(' · ')}</p>`; },
    // B: three status lanes side by side (a board): Needs you · Working · Idle, each capped, with the groups as a scope row
    B: (bots) => { const need = bots.filter((b) => b.need), work = bots.filter((b) => b.working), rest = bots.filter((b) => !b.need && !b.working);
      const groups = [...new Set(bots.map((b) => b.team || b.folder))].map((g) => { const xs = bots.filter((b) => (b.team || b.folder) === g); return { g, n: xs.length, need: xs.filter((b) => b.need).length, work: xs.filter((b) => b.working).length }; }).sort((a, b) => b.need - a.need || b.work - a.work || b.n - a.n);
      const lane = (t, xs, cap, s) => `<section class="dl-card opt-lane"><header class="dl-ch"><h2>${t}</h2><span class="dl-sub">${xs.length}</span></header><ul class="dl-list">${xs.slice(0, cap).map((b) => botRow(b, { where: false })).join('') || '<li class="dl-sub opt-empty">None</li>'}</ul>${xs.length > cap ? `<p class="dl-foot-note"><a class="dl-link">All ${xs.length} ${s} →</a></p>` : ''}</section>`;
      return `${head('Bots', `<a class="btn primary">${icon('plus')}New bot</a>`)}
      <div class="opt-groups" aria-label="Scope">${[{ g: 'All bots', n: bots.length, need: need.length, work: work.length }].concat(groups.slice(0, phone() ? 4 : 7)).map((x, i) => `<button type="button" class="opt-group${i === 0 ? ' on' : ''}"><b>${esc(x.g === 'All bots' ? x.g : FOLDERS.includes(x.g) ? path(x.g) : 'team ' + x.g)}</b><span>${x.n} bots${x.need ? ` · <em>${x.need} need you</em>` : ''}${x.work ? ` · ${x.work} working` : ''}</span></button>`).join('')}${groups.length > 7 ? `<button type="button" class="opt-group more">${groups.length - 7} more ▾</button>` : ''}</div>
      ${fbBots(bots)}
      <div class="opt-lanes">${lane('Need you', need, 6, 'needing you')}${lane('Working', work, 6, 'working')}${lane('Idle', rest.sort((a, b) => a.idle - b.idle), 6, 'idle')}</div>`; },
    // C: one sentence of status, then groups (team or folder) as collapsed rows with a roll-up; one group open at a time
    C: (bots) => { const need = bots.filter((b) => b.need), work = bots.filter((b) => b.working);
      const groups = [...new Set(bots.map((b) => b.team ? 'team ' + b.team : path(b.folder)))].map((g) => { const xs = bots.filter((b) => (b.team ? 'team ' + b.team : path(b.folder)) === g); return { g, xs, need: xs.filter((b) => b.need).length, work: xs.filter((b) => b.working).length }; }).sort((a, b) => b.need - a.need || b.work - a.work || a.g.localeCompare(b.g));
      return `${head('Bots', `<a class="btn primary">${icon('plus')}New bot</a>`)}
      <p class="opt-summary"><b class="ember">${need.length} need you</b> · <b>${work.length} working</b> · ${bots.length - need.length - work.length} idle</p>${fbBots(bots)}
      <section class="dl-card dl-flush"><ul class="opt-grouplist">${groups.map((x, i) => `<li${i === 0 ? ' class="open"' : ''}><button type="button" class="opt-gh">${icon(i === 0 ? 'chevd' : 'chev')}<b>${esc(x.g)}</b><span class="dl-sub">${x.xs.length} bot${x.xs.length > 1 ? 's' : ''}</span>${x.need ? UI.statusMark('ember', `${x.need} need${x.need > 1 ? '' : 's'} you`) : ''}${x.work ? UI.statusMark('run', `${x.work} working`) : ''}</button>${i === 0 ? `<ul class="dl-list">${x.xs.sort((a, b) => b.need - a.need || b.working - a.working).slice(0, 8).map((b) => botRow(b, { where: false })).join('')}</ul>` : ''}</li>`).join('')}</ul></section>`; },
  };

  /* ---------- Workflows ---------- */
  const WFS = {
    A: (wfs) => { const bad = wfs.filter((w) => w.failing || w.broken), run = wfs.filter((w) => w.running), ok = wfs.filter((w) => !w.failing && !w.broken && !w.running);
      return `${head('Workflows', `<a class="btn primary">${icon('plus')}New workflow</a>`)}${fbFlows(wfs)}
      ${bad.length ? card('Need a look', bad.length, `<ul class="dl-list">${bad.map(wfRow).join('')}</ul>`) : ''}
      ${run.length ? card('Running now', run.length, `<ul class="dl-list">${run.slice(0, 6).map(wfRow).join('')}</ul>`, ' opt-gap') : ''}
      <div class="opt-gap">${foldRow(ok.length, 'more', `all healthy · ${ok.filter((w) => w.src.kind === 'pack').length} from packs`)}</div>
      <p class="opt-teams">${icon('bots')}Workflows a bot made for itself are on that bot’s page.</p>`; },
    B: (wfs) => { const bad = wfs.filter((w) => w.failing || w.broken), run = wfs.filter((w) => w.running), ok = wfs.filter((w) => !w.failing && !w.broken && !w.running);
      const lane = (t, xs, cap) => `<section class="dl-card opt-lane"><header class="dl-ch"><h2>${t}</h2><span class="dl-sub">${xs.length}</span></header><ul class="dl-list">${xs.slice(0, cap).map(wfRow).join('') || '<li class="dl-sub opt-empty">None</li>'}</ul>${xs.length > cap ? `<p class="dl-foot-note"><a class="dl-link">All ${xs.length} →</a></p>` : ''}</section>`;
      const groups = [{ g: 'All', n: wfs.length }, { g: 'Yours', n: wfs.filter((w) => w.src.kind === 'yours').length }].concat(tally(wfs.filter((w) => w.src.kind === 'pack').map((w) => w.src.pack)).map((x) => ({ g: x.v, n: x.n })));
      return `${head('Workflows', `<a class="btn primary">${icon('plus')}New workflow</a>`)}
      <div class="opt-groups">${groups.map((x, i) => `<button type="button" class="opt-group${i === 0 ? ' on' : ''}"><b>${esc(x.g)}</b><span>${x.n}</span></button>`).join('')}</div>${fbFlows(wfs)}
      <div class="opt-lanes">${lane('Need a look', bad, 6)}${lane('Running', run, 6)}${lane('Healthy', ok.sort((a, b) => a.last - b.last), 6)}</div>`; },
    C: (wfs) => { const groups = [...new Set(wfs.map((w) => (w.src.kind === 'pack' ? 'from ' + w.src.pack : path(w.folder))))].map((g) => { const xs = wfs.filter((w) => (w.src.kind === 'pack' ? 'from ' + w.src.pack : path(w.folder)) === g); return { g, xs, bad: xs.filter((w) => w.failing || w.broken).length, run: xs.filter((w) => w.running).length }; }).sort((a, b) => b.bad - a.bad || b.run - a.run || a.g.localeCompare(b.g));
      const bad = wfs.filter((w) => w.failing || w.broken).length;
      return `${head('Workflows', `<a class="btn primary">${icon('plus')}New workflow</a>`)}
      <p class="opt-summary">${bad ? `<b class="fail">${bad} need a look</b> · ` : ''}<b>${wfs.filter((w) => w.running).length} running</b> · ${wfs.length} in all</p>${fbFlows(wfs)}
      <section class="dl-card dl-flush"><ul class="opt-grouplist">${groups.map((x, i) => `<li${i === 0 ? ' class="open"' : ''}><button type="button" class="opt-gh">${icon(i === 0 ? 'chevd' : 'chev')}<b>${esc(x.g)}</b><span class="dl-sub">${x.xs.length}</span>${x.bad ? UI.statusMark('fail', `${x.bad} failing`) : ''}${x.run ? UI.statusMark('run', `${x.run} running`) : ''}</button>${i === 0 ? `<ul class="dl-list">${x.xs.slice(0, 8).map(wfRow).join('')}</ul>` : ''}</li>`).join('')}</ul></section>`; },
  };

  /* ---------- Facts line: the third line of a row, as facts a person scans for ---------- */
  const NEEDS = [
    { m: 'ember', t: 'Replace the production web server', why: 'platform-sre wants to run terraform apply on production, for the SEV2 latency incident', f: { bot: 'platform-sre', where: 'acme/infra', env: 'production', act: ['Changes infrastructure', 'act-infra', 'hi'], touches: 'aws_instance.web (replaced)' }, age: '7 min ago', verb: 'Review' },
    { m: 'ember', t: 'Post the monthly forecast to #incidents', why: 'billing-sre, asked by a Slack user who isn’t one of your operators', f: { bot: 'billing-sre', service: 'Slack', where: '#incidents', act: ['Posts a message', 'act-post', 'lo'] }, age: '22 min ago', verb: 'Review' },
    { m: 'fail', t: 'ci-fixer couldn’t fix acme/web#1288', why: 'Its change made 2 tests fail. Nothing was pushed.', f: { wf: 'ci-fixer', pr: 'acme/web#1288' }, age: '52 min ago', verb: 'See why' },
    { m: 'ember', t: 'pr-review needs your call on a draft', why: 'Approve, revise or discard its review', f: { wf: 'pr-review', pr: 'acme/infra#77' }, age: '1 h ago', verb: 'Answer' },
    { m: 'ember', t: 'Approve a sign-in on app.example.com', why: 'The triage agent’s browser is waiting on a push approval', f: { bot: 'web-triage', site: 'app.example.com' }, age: '3 min ago', verb: 'Approve' },
    { m: 'ember', t: 'Change the /search cache TTL to 300s', why: 'platform-sre asks before it does this', f: { bot: 'platform-sre', service: 'kv store', env: 'staging', act: ['Changes a setting', 'act-change', 'mid'] }, age: '6 h ago', verb: 'Review' },
  ];
  const FICON = { bot: 'obj-bot', wf: 'obj-workflow', pr: 'obj-pr', where: 'branch', service: 'obj-connector', site: 'globe', env: 'server', touches: 'box' };
  const FWORD = { bot: 'Bot', wf: 'Workflow', pr: 'Pull request', where: 'Where', service: 'Service', site: 'Site', env: 'Environment', act: 'Does', touches: 'Touches' };
  const LINK = { bot: 1, wf: 1, pr: 1 };
  const order = ['bot', 'wf', 'pr', 'where', 'service', 'site', 'env', 'act', 'touches'];
  const factsT = {
    // 1: icon + value, quiet ink, gaps not separators; production carries the warning glyph and word in its colour
    A: (f) => `<span class="fx fx-a">${order.filter((k) => f[k]).map((k) => k === 'act' ? `<span class="fx-i${f.act[2] === 'hi' ? ' hi' : ''}">${icon(f.act[1])}${esc(f.act[0])}</span>` : k === 'env' ? `<span class="fx-i env${f.env === 'production' ? ' prod' : ''}">${f.env === 'production' ? UI.glyph ? `<i class="fx-g">${UI.glyph('warning')}</i>` : '' : icon(FICON.env)}${esc(f.env)}</span>` : `<span class="fx-i${LINK[k] ? ' ln' : ''}">${icon(FICON[k])}${esc(f[k])}</span>`).join('')}</span>`,
    // 2: labelled: tiny small-caps keys over values, a quiet two-tone strip; production in the warning colour with a rule
    B: (f) => `<span class="fx fx-b">${order.filter((k) => f[k]).map((k) => `<span class="fx-k"><small>${FWORD[k]}</small>${k === 'act' ? `<span class="${f.act[2] === 'hi' ? 'hi' : ''}">${esc(f.act[0])}</span>` : k === 'env' ? `<span class="${f.env === 'production' ? 'prod' : ''}">${esc(f.env)}</span>` : LINK[k] ? `<span class="ln">${esc(f[k])}</span>` : `<span>${esc(f[k])}</span>`}</span>`).join('')}</span>`,
    // 3: a sentence of facts: "platform-sre · terraform on acme/infra in ▲production · replaces aws_instance.web": the
    //    objects as underlined links in ink, the connectives quiet; production is an inline small-caps word with a mark
    C: (f) => { const parts = []; if (f.bot) parts.push(`<span class="ln">${esc(f.bot)}</span>`); if (f.wf) parts.push(`<span class="ln">${esc(f.wf)}</span>`);
      if (f.act) parts.push(`<span class="${f.act[2] === 'hi' ? 'hi' : ''}">${esc(f.act[0].toLowerCase())}</span>`);
      if (f.pr) parts.push(`on <span class="ln">${esc(f.pr)}</span>`); if (f.where) parts.push(`${f.service ? 'in' : 'on'} <span class="o">${esc(f.where)}</span>`); if (f.service) parts.push(`via <span class="o">${esc(f.service)}</span>`); if (f.site) parts.push(`at <span class="o">${esc(f.site)}</span>`);
      if (f.env) parts.push(`<span class="env${f.env === 'production' ? ' prod' : ''}">${f.env === 'production' ? `<i class="fx-g">${UI.glyph('warning')}</i>` : ''}${esc(f.env)}</span>`); if (f.touches) parts.push(`<span class="o">${esc(f.touches)}</span>`);
      return `<span class="fx fx-c">${parts.join(' ')}</span>`; },
  };
  const needRow = (it, T) => `<li class="dl-need" data-s="${it.m}"><a class="dl-need-a" href="#/opt"><span class="dl-ng">${UI.mark(it.m, { lg: true })}</span><span class="dl-nt"><b>${esc(it.t)}</b><span class="dl-why">${esc(it.why)}</span>${factsT[T](it.f)}</span><span class="dl-age">${it.age}</span><span class="dl-na"><span class="btn sm secondary">${it.verb}</span></span></a></li>`;
  const RUNS = [{ m: 'fail', wf: 'ci-fixer', pr: 'acme/web#1288', why: 'Failed at “test”: 2 tests failed (TestCheckout, TestRefund).', f: { wf: 'ci-fixer', pr: 'acme/web#1288', act: ['Pushes code', 'act-push', 'mid'] }, age: '1 h ago' }, { m: 'run', wf: 'pr-review', pr: 'acme/web#1291', why: 'Running “review”, 2 min so far', f: { wf: 'pr-review', pr: 'acme/web#1291' }, age: '2 min ago' }, { m: 'run', wf: 'platform-sre', why: 'Working on: SEV2 latency on checkout', f: { bot: 'platform-sre', env: 'production', service: 'PagerDuty' }, age: '18 min ago' }, { m: 'ok', wf: 'dep-bump', pr: 'acme/api#480', why: 'Passed in 6 min', f: { wf: 'dep-bump', pr: 'acme/api#480' }, age: '3 h ago' }];
  const runRow = (r, T) => `<li class="dl-need${r.m === 'fail' ? ' attn' : ''}" data-s="${r.m}"><a class="dl-need-a" href="#/opt"><span class="dl-ng">${r.m === 'ok' ? '' : UI.mark(r.m, { lg: true })}</span><span class="dl-nt"><b>${esc(r.wf)}</b><span class="${r.m === 'fail' ? 'reason' : 'dl-why'}">${esc(r.why)}</span>${factsT[T](r.f)}</span><span class="dl-age">${r.age}</span><span class="dl-na">${r.m === 'fail' ? '<span class="btn sm secondary row-act">See why</span>' : ''}</span></a></li>`;
  const FACTS = (T) => `${head('Needs you')}${card('', '', `<ul class="dl-list dl-needs">${NEEDS.map((x) => needRow(x, T)).join('')}</ul>`, ' opt-nohead')}
    <h2 class="opt-h2">Activity rows</h2>${card('', '', `<ul class="dl-list">${RUNS.map((r) => runRow(r, T)).join('')}</ul>`, ' opt-nohead')}`;

  /* ---------- New bot: leaving is obvious; start from a community bot, a built-in starting point, or blank ---------- */
  const TEMPLATES = [
    { name: 'SRE on-call', by: 'official', pack: 'sre-bot', ver: '0.9.0', does: 'Takes PagerDuty pages, works out what’s wrong from metrics and logs, and proposes a fix.', needs: ['PagerDuty', 'GitHub'], asks: ['Restart a service', 'Change infrastructure (Terraform)'] },
    { name: 'Dependency janitor', by: 'official', pack: 'dep-hygiene', ver: '2.1.0', does: 'Bumps dependencies weekly, runs the tests and opens a PR when they pass.', needs: ['GitHub'], asks: ['Push a branch', 'Open a pull request'] },
    { name: 'Incident scribe', by: 'official', pack: 'incident-scribe', ver: '1.0.3', does: 'Keeps a timeline in the incident channel and drafts the write-up afterwards.', needs: ['Slack', 'PagerDuty'], asks: ['Post in a channel'] },
    { name: 'Cost watcher', by: 'third party · acme-labs', pack: 'finops-kit', ver: '0.4.1', does: 'Watches cloud spend, flags anomalies and posts a monthly forecast.', needs: ['AWS', 'Slack'], asks: ['Post in a channel'] },
    { name: 'Docs gardener', by: 'yours', pack: './packs/docs', ver: 'local', does: 'Finds stale pages in the docs repo and opens small fix-up PRs.', needs: ['GitHub'], asks: ['Push a branch'] },
  ];
  const tplCard = (t, sel) => `<li class="opt-tpl${sel ? ' on' : ''}"><div class="opt-tpl-h"><b>${esc(t.name)}</b><span class="dl-sub">${t.by === 'official' ? `${icon('check')}official` : t.by === 'yours' ? 'yours' : esc(t.by)} · ${esc(t.pack)}${t.ver !== 'local' ? ` ${esc(t.ver)}` : ''}</span></div>
    <p>${esc(t.does)}</p><dl class="opt-tpl-f"><dt>Needs</dt><dd>${t.needs.map((x) => `${icon('obj-connector')}${esc(x)}`).join(' ')}</dd><dt>Will ask to</dt><dd>${t.asks.map(esc).join(', ')}</dd></dl></li>`;
  const startStep = () => { const fs = { q: '', by: [], needs: [] };
    return `<div class="opt-start"><h2 class="opt-h2">How do you want to start?</h2>
      <div class="opt-starts"><button type="button" class="opt-startc on">${icon('obj-pack')}<b>From a community bot</b><span>A ready-made bot from a pack: its charter, knowledge and runbooks, filled in.</span></button><button type="button" class="opt-startc">${icon('sparkle')}<b>From a starting point</b><span>On-call, code reviewer, release scribe or watcher: conductor’s built-in shapes.</span></button><button type="button" class="opt-startc">${icon('edit')}<b>Blank</b><span>Name it and say what it’s for; add everything else yourself.</span></button></div>
      ${UI.filterBar({ id: 'tpl', state: fs, placeholder: 'Find a community bot: on-call, cost, docs…', label: 'Find a community bot', fields: [{ k: 'by', label: 'Published by', multi: true, keepOrder: true, options: () => [{ v: 'official', label: 'Official', n: 3 }, { v: 'third-party', label: 'Third party', n: 1 }, { v: 'yours', label: 'Yours', n: 1 }] }, { k: 'needs', label: 'Needs', multi: true, options: () => tally(TEMPLATES.flatMap((t) => t.needs)) }], onChange: () => {} })}
      <ul class="opt-tpls">${TEMPLATES.map((t, i) => tplCard(t, i === 0)).join('')}</ul>
      <div class="opt-safe">${UI.mark('shadow', { lg: true })}<div><b>Every community bot starts in practice mode, read-only.</b> It proposes and never acts until you say so; anything more than reading is a grant you approve with your passkey. Its knowledge stays yours: when the pack updates, the changes come to Needs you as an approval, never a silent overwrite.</div></div></div>`; };
  const NEWBOT = {
    // A: a large sheet over the Bots page (full screen on a phone), with a visible ✕ and Esc; a quiet progress line
    A: () => `<div class="opt-under">${BOTS.A(fleet(40))}</div><div class="opt-scrim"></div>
      <div class="opt-sheet" role="dialog" aria-label="New bot"><header class="opt-sh"><button type="button" class="icon-btn opt-x" aria-label="Close">${icon('x')}</button><div><b>New bot</b><span class="dl-sub">Step 1 of 5 · How to start</span></div><div class="opt-prog"><i style="width:20%"></i></div></header>
        <div class="opt-sb">${startStep()}</div><footer class="opt-sf"><span class="dl-sub">Esc closes. You’ll be asked before anything you’ve entered is thrown away.</span><button class="btn primary">Continue with SRE on-call</button></footer></div>`,
    // B: its own page, but with a clear way out: a bar at the top with ← Bots and a Close button, the steps as a quiet line
    B: () => `<div class="opt-wbar"><a class="btn sm secondary">${icon('chev', 'flip')}Bots</a><span class="opt-wsteps">${['How to start', 'Purpose', 'Watches', 'Permissions', 'Review'].map((s, i) => `<span class="${i === 0 ? 'on' : ''}">${i + 1}. ${s}</span>`).join('')}</span><button type="button" class="btn sm secondary">${icon('x')}Close</button></div>
      <div class="dl-page opt-wpage">${startStep()}<div class="dl-actions"><button class="btn primary">Continue with SRE on-call</button></div></div>`,
    // what a template fills in: the review step, read-only + practice mode, every grant visible and signed
    C: () => `<div class="opt-under">${BOTS.A(fleet(40))}</div><div class="opt-scrim"></div>
      <div class="opt-sheet" role="dialog" aria-label="New bot"><header class="opt-sh"><button type="button" class="icon-btn opt-x" aria-label="Close">${icon('x')}</button><div><b>New bot from SRE on-call</b><span class="dl-sub">Step 5 of 5 · Review</span></div><div class="opt-prog"><i style="width:100%"></i></div></header>
        <div class="opt-sb"><h2 class="opt-h2">platform-oncall</h2><p class="dl-sub">From <b>sre-bot 0.9.0</b> (official). Its page will say so, and pack updates to its knowledge come to Needs you for approval.</p>
          <section class="dl-card"><header class="dl-ch"><h2>How it starts</h2></header><ul class="dl-list"><li class="opt-li">${UI.mark('shadow', { lg: true })}<span><b>In practice mode</b><span class="dl-why">It proposes what it would do; nothing happens until you switch it to live.</span></span></li><li class="opt-li">${icon('eye')}<span><b>Read-only</b><span class="dl-why">It can read PagerDuty incidents, GitHub repos and logs.</span></span></li></ul></section>
          <section class="dl-card"><header class="dl-ch"><h2>What it asks for</h2><span class="dl-sub">each one is yours to grant</span></header><ul class="dl-list"><li class="opt-li">${icon('act-change')}<span><b>Restart a service</b><span class="dl-why">asks you first each time</span></span><span class="opt-grant"><label class="row">${UI.switchBtn(false, 'x', 'aria-label="Grant"')}Grant</label></span></li><li class="opt-li">${icon('act-infra')}<span><b>Change infrastructure (Terraform)</b><span class="dl-why">asks you first each time; production needs your passkey</span></span><span class="opt-grant"><label class="row">${UI.switchBtn(true, 'x', 'aria-label="Grant"')}Grant</label></span></li></ul></section>
          <section class="dl-card"><header class="dl-ch"><h2>What it comes with</h2></header><p>A charter, 6 knowledge notes and 3 runbooks (page triage, rollback, cache flush). Needs PagerDuty (<b>pd</b>, connected) and GitHub (<b>gh</b>, connected).</p></section></div>
        <footer class="opt-sf"><span class="dl-sub">1 grant widens what it may do: your passkey signs it.</span><button class="btn primary">${icon('passkey')}Create with passkey</button></footer></div>`,
  };

  const CSS = `
  .opt-gap { margin-top: 18px; } .opt-nohead .dl-ch { display: none; } .opt-h2 { margin: 26px 0 10px !important; }
  .opt-fold { display: flex; justify-content: space-between; width: 100%; padding: 14px 18px; border-radius: var(--radius-card); border: var(--border-width) dashed var(--color-border-default); background: none; font: inherit; color: var(--color-text-secondary); cursor: pointer; text-align: left; gap: 12px; }
  .opt-teams { display: flex; flex-wrap: wrap; align-items: center; gap: 6px; margin-top: 16px; font-size: 13.5px; color: var(--color-text-secondary); } .opt-teams svg { width: 16px; height: 16px; }
  .opt-groups { display: flex; flex-wrap: wrap; gap: 8px; margin: 18px 0 0; } .opt-group { display: grid; gap: 2px; padding: 8px 12px; border-radius: var(--radius-card); border: var(--border-width) solid var(--color-border-subtle); background: var(--color-bg-surface); font: inherit; text-align: left; color: var(--color-text-primary); cursor: pointer; min-width: 120px; }
  .opt-group span { font-size: 12px; color: var(--color-text-tertiary); } .opt-group em { font-style: normal; color: var(--color-text-accent); font-weight: 700; } .opt-group.on { border-color: var(--color-text-primary); box-shadow: inset 0 0 0 1px var(--color-text-primary); } .opt-group.more { color: var(--color-text-secondary); place-items: center; }
  .opt-lanes { display: grid; grid-template-columns: repeat(3, minmax(0, 1fr)); gap: 16px; } .opt-lane .dl-need-a { grid-template-columns: 32px minmax(0, 1fr) auto !important; } .opt-lane .dl-age:empty { display: none; } .opt-empty { padding: 14px 0; list-style: none; }
  .opt-summary { margin: 16px 0 0; font-size: 15px; color: var(--color-text-secondary); } .opt-summary .ember { color: var(--color-text-accent); } .opt-summary .fail { color: var(--color-status-danger-fg); }
  .opt-grouplist { list-style: none; margin: 0; padding: 0; } .opt-grouplist > li { border-top: var(--border-width) solid var(--color-border-subtle); } .opt-grouplist > li:first-child { border-top: 0; }
  .opt-gh { display: flex; align-items: center; gap: 10px; width: 100%; padding: 14px 18px; border: 0; background: none; font: inherit; color: var(--color-text-primary); cursor: pointer; text-align: left; flex-wrap: wrap; } .opt-gh > svg { width: 16px; height: 16px; } .opt-grouplist .dl-list { padding: 0 18px 8px 44px; }
  .fx { display: flex; flex-wrap: wrap; align-items: center; gap: 4px 14px; margin-top: 6px; font-size: 12.5px; color: var(--color-text-secondary); }
  .fx-a .fx-i { display: inline-flex; align-items: center; gap: 5px; } .fx-a svg { width: 13px; height: 13px; color: var(--color-icon-default); } .fx-a .ln { color: var(--color-text-primary); text-decoration: underline; text-decoration-color: var(--color-border-strong); text-underline-offset: 3px; }
  .fx-a .prod, .fx-b .prod, .fx-c .prod { color: var(--color-status-warning-fg); font-weight: 700; } .fx-g { display: inline-flex; width: 13px; height: 13px; margin-right: 4px; vertical-align: -2px; } .fx-g svg { width: 13px; height: 13px; }
  .fx-a .hi, .fx-b .hi, .fx-c .hi { color: var(--color-text-primary); font-weight: 700; } .fx-a .hi svg { color: var(--color-text-primary); }
  .fx-b { gap: 6px 18px; } .fx-b .fx-k { display: grid; line-height: 1.25; } .fx-b small { font-size: 10px; letter-spacing: .07em; text-transform: uppercase; color: var(--color-text-tertiary); } .fx-b .fx-k > :last-child { color: var(--color-text-primary); }
  .fx-b .prod { border-bottom: 2px solid var(--color-status-warning-fg); } .fx-b .ln { text-decoration: underline; text-decoration-color: var(--color-border-strong); text-underline-offset: 3px; }
  .fx-c { display: block; font-size: 13px; } .fx-c .ln, .fx-c .o { color: var(--color-text-primary); } .fx-c .ln { text-decoration: underline; text-decoration-color: var(--color-border-strong); text-underline-offset: 3px; } .fx-c .env { font-variant: all-small-caps; letter-spacing: .04em; font-weight: 700; }
  .opt-under { pointer-events: none; } .opt-scrim { position: fixed; inset: 0; background: var(--color-scrim); z-index: 50; }
  .opt-sheet { position: fixed; z-index: 60; top: 24px; bottom: 24px; left: 50%; transform: translateX(-50%); width: min(920px, calc(100vw - 48px)); display: flex; flex-direction: column; background: var(--color-bg-canvas); border: var(--border-width) solid var(--color-border-default); border-radius: var(--radius-overlay, 14px); box-shadow: var(--shadow-3); overflow: hidden; }
  .opt-sh { display: grid; grid-template-columns: auto 1fr; align-items: center; gap: 4px 12px; padding: 14px 20px 0; } .opt-sh b { display: block; font-family: var(--font-family-serif); font-size: 20px; } .opt-x { width: 40px; height: 40px; border: var(--border-width) solid var(--color-border-default); border-radius: var(--radius-control); }
  .opt-prog { grid-column: 1 / -1; height: 3px; background: var(--color-border-subtle); border-radius: 2px; margin: 12px 0 0; } .opt-prog i { display: block; height: 100%; background: var(--color-accent-default); border-radius: 2px; }
  .opt-sb { flex: 1; overflow-y: auto; padding: 4px 24px 24px; } .opt-sf { display: flex; align-items: center; gap: 14px; justify-content: space-between; padding: 12px 20px; border-top: var(--border-width) solid var(--color-border-subtle); }
  .opt-starts { display: grid; grid-template-columns: repeat(3, minmax(0, 1fr)); gap: 12px; } .opt-startc { display: grid; gap: 4px; padding: 14px 16px; border-radius: var(--radius-card); border: var(--border-width) solid var(--color-border-subtle); background: var(--color-bg-surface); font: inherit; text-align: left; color: var(--color-text-secondary); cursor: pointer; } .opt-startc b { color: var(--color-text-primary); } .opt-startc svg { width: 20px; height: 20px; color: var(--color-icon-default); } .opt-startc.on { border-color: var(--color-text-primary); box-shadow: inset 0 0 0 1px var(--color-text-primary); }
  .opt-tpls { list-style: none; margin: 0; padding: 0; display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 12px; } .opt-tpl { padding: 14px 16px; border-radius: var(--radius-card); border: var(--border-width) solid var(--color-border-subtle); background: var(--color-bg-surface); } .opt-tpl.on { border-color: var(--color-text-primary); box-shadow: inset 0 0 0 1px var(--color-text-primary); }
  .opt-tpl-h { display: flex; flex-wrap: wrap; align-items: baseline; gap: 4px 10px; } .opt-tpl-h svg { width: 13px; height: 13px; vertical-align: -2px; } .opt-tpl p { margin: 6px 0; font-size: 14px; color: var(--color-text-secondary); }
  .opt-tpl-f { display: grid; grid-template-columns: auto 1fr; gap: 2px 10px; margin: 0; font-size: 12.5px; } .opt-tpl-f dt { color: var(--color-text-tertiary); } .opt-tpl-f dd { margin: 0; } .opt-tpl-f svg { width: 13px; height: 13px; vertical-align: -2px; margin-right: 3px; }
  .opt-safe { display: flex; gap: 12px; align-items: start; margin-top: 16px; padding: 14px 16px; border-radius: var(--radius-card); background: var(--color-bg-sunken); font-size: 14px; color: var(--color-text-secondary); } .opt-safe b { color: var(--color-text-primary); }
  .opt-li { display: grid; grid-template-columns: 22px minmax(0, 1fr) auto; gap: 10px; align-items: start; padding: 10px 0; } .opt-li > svg { width: 18px; height: 18px; margin-top: 2px; } .opt-li b { display: block; }
  .opt-wbar { position: sticky; top: 0; z-index: 5; display: flex; align-items: center; gap: 14px; padding: 10px 36px; background: var(--color-bg-canvas); border-bottom: var(--border-width) solid var(--color-border-subtle); } .opt-wbar .flip { transform: rotate(180deg); } .opt-wsteps { flex: 1; display: flex; gap: 16px; font-size: 13px; color: var(--color-text-tertiary); overflow: hidden; white-space: nowrap; } .opt-wsteps .on { color: var(--color-text-primary); font-weight: 700; }
  .opt-wpage { padding-top: 12px; }
  @media (max-width: 720px) { .opt-lanes { grid-template-columns: 1fr; } .opt-starts, .opt-tpls { grid-template-columns: 1fr; } .opt-sheet { inset: 0; transform: none; width: auto; border-radius: 0; border: 0; } .opt-sh { padding-top: calc(10px + var(--sat, 0px)); } .opt-sf { flex-direction: column; align-items: stretch; } .opt-wsteps span:not(.on) { display: none; } .opt-wbar { padding: 8px 16px; } .opt-gh .st { font-size: 12px; } }`;

  window.OPT = { render() {
    if (WHAT === 'chat' && UI.navGroups) UI.navGroups[0][1].splice(2, 0, ['chat', 'Chat', 'message']); // Chat as a main destination (item 20)
    UI.route('opt', () => { if (window.OPT3 && OPT3.handles(WHAT)) return `<div class="page dl-page${WHAT === 'chat' ? ' wide ch-page' : ''}">${OPT3.page(WHAT, DIR)}</div>`; if (window.OPT2 && OPT2.handles(WHAT)) return `<div class="page dl-page${WHAT === 'mbots' ? ' bot-list' : ''}">${OPT2.page(WHAT, DIR, N)}</div>`; const body = WHAT === 'bots' ? BOTS[DIR](fleet(N)) : WHAT === 'workflows' ? WFS[DIR](flows(N)) : WHAT === 'facts' ? FACTS(DIR) : NEWBOT[DIR]();
      return WHAT === 'newbot' && DIR === 'B' ? `<div>${body}</div>` : `<div class="page dl-page${WHAT === 'bots' || WHAT === 'newbot' ? ' bot-list' : ''}">${body}</div>`; }, { nav: WHAT === 'workflows' || WHAT === 'mwfs' ? 'workflows' : WHAT === 'facts' ? 'attention' : WHAT === 'chat' ? 'chat' : 'bots' });
    history.replaceState(null, '', location.pathname + location.search + '#/opt');
    const s = document.createElement('style'); s.textContent = CSS + (window.OPT2 ? OPT2.css : '') + (window.OPT3 ? OPT3.css : ''); document.head.appendChild(s);
    window.__PROTO_FAST = true; UI.boot(); document.documentElement.dataset.theme = THEME;
    if (WHAT === 'chat') { const tb = document.querySelector('#tabbar a[data-nav="activity"]'); if (tb) { tb.dataset.nav = 'chat'; tb.setAttribute('href', '#/opt'); tb.innerHTML = UI.icon('message') + '<span>Chat</span>'; tb.classList.add('on'); } }
  } };
})();
