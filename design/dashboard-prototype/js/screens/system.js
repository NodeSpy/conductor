/* System: packs & plugins, updates, connectors/runtimes/hosts/isolation, logs & audit, phone "more".
 * Every config edit here stages into the pipeline; plugin installs, updates
 * and container archives are daemon operations behind a step-up assertion. */
(function () {
  const { icon, st, tag, ago, esc, usd } = UI;
  const now = () => new Date().toISOString();
  const clone = (o) => JSON.parse(JSON.stringify(o));
  const audit = (event, detail) => D.audit.unshift({ ts: now(), event, who: 'operator:ops', detail, run: '' });
  const stagedTag = (coll, k) => { const c = S.find(coll, k); return c ? tag(c.after == null ? 'removal staged' : c.before == null ? 'new · staged' : 'edit staged', 'run') : ''; };

  /* ================= packs & plugins ================= */
  const REG = [['conductor-packs/sre-bot', 'pack', 'SRE runbooks, knowledge and a bot charter', '0.9.0'], ['conductor-packs/dep-hygiene', 'pack', 'Weekly dependency bumps with a test gate', '2.1.0'], ['conductor-packs/incident-scribe', 'pack', 'Timelines and RCAs from incident channels', '1.0.3'], ['conductor-plugins/aws-cost', 'plugin', 'AWS Cost Explorer connector: costs, anomalies', 'v0.6.0'], ['conductor-plugins/linear', 'plugin', 'Linear connector: issues, comments, webhooks', 'v1.1.0'], ['conductor-plugins/opsgenie', 'plugin', 'Opsgenie connector', 'v0.8.2'], ['conductor-plugins/lua', 'plugin', 'Code-step engine: use: lua', 'v0.2.0']];
  const ex = { q: '' };
  UI.route('extensions/:tab?', ({ tab }) => {
    tab = tab || 'packs';
    // services, agent runtimes and code languages are what conductor can use: they live on Plugins now
    if (tab === 'plugins') { setTimeout(() => location.replace('#/connectors/connectors')); return ''; }
    if (tab === 'browse' && /[?&]kind=pack/.test(location.hash) && UI.catalogKind) UI.catalogKind('pack');
    // one line per pack: its name and what it is; only an update or a dormant trigger asks for attention; the
    // version, hash and counts are a hover away; actions are quiet until you need them
    // one compact line per pack (the Overview's row): its mark, name and where it comes from, what's armed in words (or,
    // when it needs you, why), one action and a menu. No tooltip: everything is on the row or one click away.
    // what a pack does, from the catalog when it's listed there
    const desc = (p) => { const it = (UI.catalogItems ? UI.catalogItems() : []).find((x) => x.kind === 'pack' && (x.name === p.name || x.instance === p.name)); return it ? it.desc : ''; };
    const srcOf = (p) => String((S.staged('packs', p.name) || p.cfg || {}).use || '').split('@')[0].replace(/^conductor-packs\//, '').replace(/^\.\/packs\//, '').replace(/^acme\/conductor-packs\//, '');
    // a pack of only bot templates isn't here: it lives in New bot, and a bot made from it shows its version on its page
    const packs = `<div class="panel"><ul class="dl-list dl-conns pack-list">${D.packs.filter((p) => !(UI.botOnlyPack && UI.botOnlyPack(srcOf(p)))).map((p) => { const c = S.staged('packs', p.name) || p.cfg, mem = (D.packMembers || {})[p.name] || { triggers: [], roles: [] }, upd = p.update && !(c.use || '').endsWith('@' + p.update);
      const armed = Object.values(c.triggers || {}).filter((t) => t.enabled).length, unbound = (mem.roles || []).filter((r) => !(c.connectors || {})[r] && S.view().connectors.filter((cn) => ((S.staged('connectors', cn.name) || cn.cfg || {}).use || cn.use || '').split('@')[0] === r).length !== 1), dormant = p.status === 'wait' || (unbound.length && armed);
      const from = p.official ? 'official' : /^\.\//.test(c.use || '') ? 'your own' : `third party · ${esc(String(c.use).split('@')[0])}`;
      return `<li class="dl-conn pack-row${dormant ? ' attn' : ''}" data-s="${dormant ? 'wait' : c.hold ? 'idle' : 'ok'}"><span class="dl-ng">${dormant ? UI.mark('wait', { lg: true, label: 'Waiting' }) : c.hold ? UI.mark('idle', { lg: true, label: 'Parked' }) : ''}</span>
        <span class="dl-nt"><b>${esc(p.name)} ${stagedTag('packs', p.name)}</b>${dormant ? `<span class="reason">Not working yet: it needs a ${esc(unbound[0] || 'pagerduty')} connection and none is chosen.</span>` : `<span class="dl-why pack-desc">${esc(desc(p) || 'Your own pack.')}</span>`}
          ${UI.factsLine([upd ? UI.pluginFact.update(p.update) : null, (() => { const tp = UI.packContents ? UI.packContents(srcOf(p)).templates : []; return tp.length ? { icon: 'bots', value: `Includes ${tp.length} bot template${tp.length > 1 ? 's' : ''}`, href: `#/bots/new?from=${srcOf(p)}`, label: 'Start a bot from it in New bot' } : null; })(), { icon: 'play', value: (() => { const n = (mem.triggers || []).length; return !n ? 'Nothing to switch on' : n === 1 ? (armed ? 'Automation on' : 'Automation off') : `${armed} of ${n} automations on`; })(), label: 'Its automations' },
            p.official ? UI.pluginFact.origin('official') : Object.assign(UI.pluginFact.origin('custom'), { value: /^\.\//.test(c.use || '') ? 'Your own' : `Third party · ${String(c.use).split('@')[0]}` }),
            p.ver && p.ver !== 'local' ? (c.hold ? UI.pluginFact.policy({ kind: 'pinned', ver: p.ver }) : UI.pluginFact.policy({ kind: 'current', ver: p.ver })) : null, p.ver && p.ver !== 'local' ? UI.pluginFact.version(p.ver) : null])}</span>
        <span class="dl-na">${dormant ? `<a class="btn sm secondary" href="#/extensions/pack/${p.name}">Choose a connection</a>` : upd ? `<button class="btn sm secondary" data-act="pack-plan" data-p="${p.name}" data-update="1">Update to ${p.update}…</button>` : `<a class="btn sm secondary" href="#/extensions/pack/${p.name}" title="Switch its automations on, pick its connections, adjust its steps: kept in your config, so updates don’t undo them">Customize</a>`}<button class="icon-btn" data-act="pack-more" data-p="${p.name}" aria-label="More for ${esc(p.name)}">${icon('more')}</button></span></li>`; }).join('')}</ul></div>
      <p class="dl-sub" style="margin-top:12px">Adding, updating or removing a pack is reviewed like any other change.</p>`;
    const installed = (n) => D.packs.some((p) => (p.cfg.use || '').split('@')[0] === n) || D.plugins.some((p) => p.src === n);
    const browse = `<div class="filters"><div class="input-icon">${icon('search')}<input class="input" placeholder="search the official registry" value="${esc(ex.q)}" data-on-input="reg-q" aria-label="Search registry"></div></div><div class="grid g3" id="reg-grid">${REG.map(([n, k, d, v]) => `<div class="panel bot-card reg-card${ex.q && !(n + d).toLowerCase().includes(ex.q.toLowerCase()) ? ' hidden' : ''}" data-text="${esc(n + ' ' + d)}"><div class="row"><b class="mono" style="font-size:12px">${n}</b><span class="mono tiny muted" style="margin-left:auto">${v}</span></div><div class="small dim">${d}</div><div class="row">${tag(k)}${installed(n) ? tag('installed', 'ok') : `<button class="btn sm primary" data-act="reg-add" data-n="${n}" data-k="${k}" data-v="${v}">${icon('plus')}Add</button>`}<span class="muted tiny" style="margin-left:auto">official registry</span></div></div>`).join('')}</div>`;
    return `<div class="page">
      ${UI.head('Packs', { lede2: `A ${UI.term('pack')} is a bundle of workflows someone else wrote, installed and updated as one unit.`, actions: `<a class="btn primary" href="#/extensions/browse?kind=pack">${icon('plus')}Add a pack</a>` })}
      ${UI.tabs([['packs', 'Installed', '#/extensions/packs', D.packs.filter((p) => !(UI.botOnlyPack && UI.botOnlyPack(String((p.cfg || {}).use || '').split('@')[0].replace(/^conductor-packs\//, '')))).length], ['browse', 'Browse', '#/extensions/browse']], tab)}
      ${tab === 'browse' ? UI.catalogHTML(['pack'], 'browse') : packs}
      <div class="small muted sentence" style="margin-top:6px">${icon('updates')} ${D.updateCfg.deps ? 'Packs update themselves when a new version still checks out against your config.' : 'Packs update only when you ask.'}</div>
    </div>`;
  }, { nav: 'extensions' });
  UI.act('reg-q', (el) => { ex.q = el.value; UI.$$('#reg-grid .reg-card').forEach((c) => c.classList.toggle('hidden', !!ex.q && !c.dataset.text.toLowerCase().includes(ex.q.toLowerCase()))); });
  UI.act('pack-plan', (b) => {
    const p = D.packs.find((x) => x.name === b.dataset.p), to = p.update || p.ver;
    const s = UI.modal(`<div class="modal-head"><div><div class="caps">conductor pack plan</div><h2>${p.update ? `Update ${p.name} to ${p.update}` : `Plan for ${p.name}`}</h2></div><button class="icon-btn" data-close aria-label="Close">${icon('x')}</button></div>
    <div class="modal-body stack small">
      <div class="kv"><dt>from</dt><dd class="mono">${p.ver} · ${p.sha}</dd><dt>to</dt><dd class="mono">${to} · ${p.update ? '0e47d12' : p.sha}</dd><dt>source</dt><dd class="mono">${esc(p.use)} (${p.official ? 'trusted' : 'pack_trust: acme/*'})</dd></dl>
      ${p.update ? `<div class="caps">What changes</div><div class="check-row">${st('ok', '')}<div>workflow <span class="mono">review-flow</span>: step <span class="mono">summarize</span> added (narrows nothing, widens nothing)</div></div><div class="check-row">${st('ok', '')}<div>your ${p.overrides} overrides still apply</div></div><div class="check-row">${st('wait', '')}<div>new <b>disarmed</b> trigger <span class="mono">on_push</span>: nothing runs until you arm it</div></div>` : `<div class="check-row">${st('ok', '')}<div>up to date at the locked sha</div></div>`}
      <div class="check-row">${st('ok', '')}<div>pack lint: no isolation loosening, no runtime/host, no trust: change</div></div>
      <div class="check-row">${st('ok', '')}<div>validated on a copy with ${D.daemon.version} and ${D.daemon.update.available || D.daemon.version}</div></div>
    </div><div class="modal-foot"><button class="btn ghost" data-close>Close</button>${p.update ? `<button class="btn primary" id="pp-go">${icon('check')}Stage update</button>` : ''}</div>`, { wide: true });
    const go = UI.$('#pp-go', s); if (go) go.addEventListener('click', () => { const c = clone(S.staged('packs', p.name)); c.use = c.use.split('@')[0] + '@' + p.update; UI.closeModal(); UI.staged(S.stage('packs', p.name, c, `update packs.${p.name} ${p.ver} → ${p.update}`)); });
  });
  UI.act('pack-more', (b) => { const c = S.staged('packs', b.dataset.p) || {}; UI.menu(b, [['Customize', 'edit', 'strip-go', `data-href="#/extensions/pack/${b.dataset.p}"`], ['What an update would change…', 'eye', 'pack-plan', `data-p="${b.dataset.p}"`], [c.hold ? 'Let it update again' : 'Hold it at this version', 'pause', 'pack-hold', `data-p="${b.dataset.p}"`], ['-'], ['Remove this pack…', 'trash', 'pack-del', `data-p="${b.dataset.p}"`]]); });
  UI.act('pack-hold', (b) => { const c = clone(S.staged('packs', b.dataset.p)); if (c.hold) delete c.hold; else c.hold = true; UI.staged(S.stage('packs', b.dataset.p, c), { go: false }); UI.rerender(); });
  UI.act('pack-del', (b) => UI.confirm({ title: `Remove pack ${b.dataset.p}?`, body: 'Stages removing it from packs: (and its overrides). Its workflows stop; conductor init drops the vendored copy on apply.', ok: 'Stage removal', danger: true }).then((ok) => ok && UI.staged(S.stage('packs', b.dataset.p, null))));
  UI.act('pack-add', () => UI.go('#/extensions/browse?kind=pack'));
  UI.act('pack-source', () => { Object.assign(ps, { use: '', read: null, inst: '' }); UI.go('#/extensions/source/add'); });
  UI.act('reg-add', (b) => {
    if (b.dataset.k === 'pack') { const name = b.dataset.n.split('/')[1]; UI.staged(S.stage('packs', D.packs.find((p) => p.name === name) ? name + '-2' : name, { use: b.dataset.n })); return; }
    const pn = b.dataset.n.split('/')[1];
    UI.stepup({ title: `Install ${pn} ${b.dataset.v}`, purpose: 'Plugins · install', summary: `<p class="su-lede">You're installing <b>${esc(pn)}</b> ${esc(b.dataset.v)} from ${esc(b.dataset.n)}. It runs as a plugin, confined to what it declares: it may reach its service's API and starts no other programs.</p>`, ok: 'Install with passkey', what: `{ purpose: <b>plugin_add</b>, source: <b>${b.dataset.n}</b>, version: <b>${b.dataset.v}</b>, manifest: <b>net: api.*.example · spawns: none</b> }` }).then((ok) => {
      if (!ok) return; const decl = SCHEMA.connectors[pn];
      D.plugins.push({ name: pn, kind: pn === 'lua' ? 'engine' : 'connector', ver: b.dataset.v, src: b.dataset.n, status: 'ok', instances: [], reload: 'in place', update: null });
      audit('plugin_add', `${b.dataset.n}@${b.dataset.v}`); S.save(); UI.rerender();
      UI.toast(`Installed ${pn}`, decl ? 'add a connection to use it' : 'available to code steps', 'ok');
      if (decl) setTimeout(() => connForm(null, pn), 50);
    });
  });
  /* ---------- a pack's overrides, as a guided edit (packs.<instance> in your config; Packs.md) ----------
   * Connection roles bind to compatible instances only; triggers arrive disarmed and are armed with their scope (repos
   * are the consent); steps are overridden in the builder's step form, the pack's value beside yours; anything that
   * loosens the pack is marked and asks for your passkey on apply. YAML is the second view of the same block. */
  UI.act('pack-ov', (b) => UI.go(`#/extensions/pack/${b.dataset.p}`));
  const po = { yaml: false };
  const packCfg = (name) => clone(S.staged('packs', name) || {});
  const savePack = (name, c) => { S.stage('packs', name, c); UI.refreshChrome(); UI.rerender(); };
  UI.route('extensions/pack/:name', ({ name }) => {
    const p = D.packs.find((x) => x.name === name);
    if (!p) return `<div class="page dl-page">${UI.empty({ title: `There is no pack called ${esc(name)}`, action: '<a class="btn" href="#/extensions/packs">Packs</a>' })}</div>`;
    const c = packCfg(name), mem = (D.packMembers || {})[name] || { workflows: {}, triggers: [], roles: [] }, staged = !!S.find('packs', name);
    const conns = S.view().connectors, kindOf = (cn) => ((S.staged('connectors', cn.name) || cn.cfg || {}).use || cn.use);
    const roleRow = (role) => { const fit = conns.filter((cn) => kindOf(cn) === role), cur = (c.connectors || {})[role];
      return `<li class="po-row"><div class="dl-nt"><b>${esc(role)}</b><span class="dl-why">${cur ? `bound to ${esc(cur)}` : fit.length === 1 ? `bound automatically to ${esc(fit[0].name)}, your only ${esc(role)} connection` : fit.length ? `<span class="po-warn">${UI.mark('wait')} ${fit.length} ${esc(role)} connections: choose one, or the pack won’t load</span>` : `<span class="po-warn">${UI.mark('wait')} No ${esc(role)} connection: the triggers that need it stay dormant</span>`}</span></div>
        <label class="dl-search dl-select po-sel"><select data-on-change="po-role" data-p="${name}" data-r="${role}" aria-label="Which ${esc(role)} connection"><option value="">${fit.length === 1 ? `Automatic (${esc(fit[0].name)})` : fit.length ? 'Choose a connection…' : `No ${esc(role)} connection yet`}</option>${fit.map((cn) => `<option value="${esc(cn.name)}"${cn.name === cur ? ' selected' : ''}>${esc(cn.name)}</option>`).join('')}<option value="__new__">+ Create a ${esc(role)} connection…</option></select></label></li>`; };
    const trig = (t) => { const tc = (c.triggers || {})[t] || {}, on = !!tc.enabled, gh = (mem.roles || []).includes('github');
      return `<li class="po-row"><div class="dl-nt"><b>${esc(t)}</b><span class="dl-why">${on ? (gh ? (tc.repos && tc.repos.length ? `armed on ${tc.repos.map(esc).join(', ')}` : '<span class="po-warn">Armed with no repos: it won’t run until you name them</span>') : 'armed') : 'disarmed, as the pack ships it'}</span>
        ${on && gh ? `<input class="input mono po-repos" value="${esc((tc.repos || []).join(', '))}" placeholder="acme/app, acme/web" data-on-change="po-repos" data-p="${name}" data-t="${esc(t)}" aria-label="Repos ${esc(t)} may run on">` : ''}</div>
        ${UI.switchBtn(on, 'po-arm', `data-p="${name}" data-t="${esc(t)}" aria-label="Arm ${esc(t)}"`)}</li>`; };
    const packWfs = D.workflows.filter((w) => w.source === 'pack ' + name);
    const stepRows = Object.entries(mem.workflows || {}).flatMap(([wf, ss]) => ss.map((st2) => { const key = `${wf}/${st2}`, ovr = (c.steps || {})[key];
      return `<li class="po-row"><div class="dl-nt"><b>${esc(wf)} · ${esc(st2)}</b><span class="dl-why">${ovr ? `your override: ${Object.entries(ovr).map(([k, v]) => `${esc(k)} ${esc(typeof v === 'object' ? JSON.stringify(v) : v)}`).join(', ')}` : 'as the pack wrote it'}</span></div>
        <span class="dl-na">${packWfs.length ? `<a class="btn sm secondary" href="#/workflows/${encodeURIComponent(packWfs[0].name)}">Override in the builder</a>` : ''}${ovr ? `<button class="btn sm ghost" data-act="po-reset" data-p="${name}" data-k="steps" data-x="${esc(key)}">Reset</button>` : ''}</span></li>`; })).join('');
    const current = [].concat(Object.entries(c.connectors || {}).map(([k, v]) => ['connectors', k, `the ${k} role → ${v}`]), Object.entries(c.triggers || {}).filter(([, v]) => v.enabled).map(([k, v]) => ['triggers', k, `${k} armed${v.repos ? ' on ' + v.repos.join(', ') : ''}`]), Object.entries(c.steps || {}).map(([k, v]) => ['steps', k, `${k}: ${Object.keys(v).join(', ')}`]), c.policy ? [['policy', '', 'policy changed']] : [], c.models ? [['models', '', 'models changed']] : []);
    const yamlNow = YAML.stringify(Object.fromEntries(Object.entries(c).filter(([k]) => !['use', 'hold'].includes(k))));
    return `<div class="page dl-page">
      ${UI.crumbs([['Packs', '#/extensions/packs'], [name, `#/extensions/item/pack/${encodeURIComponent(name)}`], ['Customize']])}
      <div class="dl-phead"><div><h1>Customize ${esc(name)}</h1><p class="dl-lead">What you change about this pack lives in your config, never in its files, and survives its updates. A pack can only narrow what it does; anything that loosens it is marked and asks for your passkey when you apply.</p></div>
        <div class="dl-actions tight">${staged ? '<a class="btn primary" href="#/config/apply">Review the change</a>' : ''}</div></div>
      <div class="dl-cols"><div class="dl-col-main">
        <section class="dl-card"><header class="dl-ch"><h2>Connections it needs</h2></header>${(mem.roles || []).length ? `<ul class="dl-list">${mem.roles.map(roleRow).join('')}</ul>` : '<p class="dl-sub">It needs no connection.</p>'}</section>
        <section class="dl-card"><header class="dl-ch"><h2>Triggers</h2><span class="dl-sub">they arrive disarmed; arming one with its repos is your consent</span></header><ul class="dl-list">${(mem.triggers || []).map(trig).join('') || '<li class="dl-sub">No triggers.</li>'}</ul></section>
        <section class="dl-card"><header class="dl-ch"><h2>Steps</h2><span class="dl-sub">change a model, a prompt or isolation; the pack’s value is shown beside yours</span></header><ul class="dl-list">${stepRows || '<li class="dl-sub">Nothing to override.</li>'}</ul></section>
      </div><div class="dl-col-side">
        <section class="dl-card"><header class="dl-ch"><h2>Your overrides</h2><span class="dl-sub">${current.length}</span></header>${current.length ? `<ul class="dl-list">${current.map(([k, x, say]) => `<li class="po-row"><span>${esc(say)}</span><button class="btn sm ghost" data-act="po-reset" data-p="${name}" data-k="${k}" data-x="${esc(x)}">Reset to the pack’s</button></li>`).join('')}</ul>` : '<p class="dl-sub">None: it runs exactly as the pack ships.</p>'}</section>
        <details class="dl-card dl-tech-d po-yaml"${po.yaml ? ' open' : ''} data-on-toggle="po-yaml-open"><summary>${icon('chev')}Edit as YAML <span class="dl-sub">the same block, <span class="mono">packs.${esc(name)}</span></span></summary>
          <textarea class="textarea mono" rows="10" id="po-y" data-on-change="po-yaml" data-p="${name}" aria-label="packs.${esc(name)} as YAML">${esc(yamlNow)}</textarea><p class="dl-sub" id="po-y-err"></p></details>
      </div></div></div>`;
  }, { nav: 'extensions' });
  UI.act('po-yaml-open', (el) => { po.yaml = el.open; });
  UI.act('po-role', (el) => { const name = el.dataset.p, role = el.dataset.r; if (el.value === '__new__') { el.value = ''; connForm(null, role); return; } const c = packCfg(name); c.connectors = Object.assign({}, c.connectors); if (el.value) c.connectors[role] = el.value; else delete c.connectors[role]; if (!Object.keys(c.connectors).length) delete c.connectors; savePack(name, c); });
  UI.act('po-arm', (b) => { const name = b.dataset.p, t = b.dataset.t, c = packCfg(name); c.triggers = Object.assign({}, c.triggers); const cur = Object.assign({}, c.triggers[t]); cur.enabled = !cur.enabled; if (!cur.enabled) delete c.triggers[t]; else c.triggers[t] = cur; if (!Object.keys(c.triggers).length) delete c.triggers; savePack(name, c); });
  UI.act('po-repos', (el) => { const name = el.dataset.p, t = el.dataset.t, c = packCfg(name); c.triggers = Object.assign({}, c.triggers); c.triggers[t] = Object.assign({}, c.triggers[t], { repos: el.value.split(',').map((x) => x.trim()).filter(Boolean) }); savePack(name, c); });
  UI.act('po-reset', (b) => { const name = b.dataset.p, k = b.dataset.k, x = b.dataset.x, c = packCfg(name); if (x && c[k]) { delete c[k][x]; if (!Object.keys(c[k]).length) delete c[k]; } else delete c[k]; savePack(name, c); });
  UI.act('po-yaml', (el) => { const name = el.dataset.p, c = packCfg(name); try { const v = YAML.parse(el.value || '').value || {}; const bad = Object.keys(v).filter((k) => !['connectors', 'secrets', 'steps', 'triggers', 'policy', 'models', 'settings', 'preset', 'folders', 'on', 'decide'].includes(k)); if (bad.length) { UI.$('#po-y-err').textContent = `${bad[0]}: a pack can’t be overridden there`; return; } const mem2 = (D.packMembers || {})[name] || { workflows: {}, triggers: [] }, steps2 = Object.entries(mem2.workflows || {}).flatMap(([w, ss]) => ss.map((x) => `${w}/${x}`)), bs = Object.keys(v.steps || {}).filter((k) => !steps2.includes(k)); if (bs.length) { UI.$('#po-y-err').textContent = `steps.${bs[0]}: the pack has no step by that name (it has ${steps2.join(', ')})`; return; } const bt = Object.keys(v.triggers || {}).filter((k) => k !== '*' && !(mem2.triggers || []).includes(k)); if (bt.length) { UI.$('#po-y-err').textContent = `triggers.${bt[0]}: the pack has no trigger by that name`; return; } savePack(name, Object.assign({ use: c.use }, c.hold ? { hold: true } : {}, v)); } catch (e) { UI.$('#po-y-err').textContent = `Not valid YAML: ${e.message}`; } });

  /* ---------- add a pack from a source: read its manifest first, then the same role → connection mapping ---------- */
  const ps = { use: '', read: null, inst: '' };
  UI.route('extensions/source/add', () => {
    const m = ps.read, conns = S.view().connectors, kindOf = (cn) => ((S.staged('connectors', cn.name) || cn.cfg || {}).use || cn.use);
    return `<div class="page dl-page">${UI.crumbs([['Packs', '#/extensions/packs'], ['Browse', '#/extensions/browse'], ['A pack from a source']])}
      <div class="dl-phead"><div><h1>A pack from a source</h1><p class="dl-lead">For a pack that isn’t in the official catalog: one of your own, or a team’s. conductor reads what it needs before anything is added.</p></div></div>
      <div class="dl-cols"><div class="dl-col-main">
        <section class="dl-card"><header class="dl-ch"><h2>Where it is</h2></header>
          <div class="field"><label for="ps-use">Source</label><input class="input mono" id="ps-use" value="${esc(ps.use)}" placeholder="acme/conductor-packs/kit@^1.2" data-on-change="ps-use"><div class="hint">An owner/repo (optionally with a path and <span class="mono">@version</span>), or a local directory such as <span class="mono">./packs/house-style</span>. A source outside the official catalog needs a <span class="mono">pack_trust</span> entry.</div></div>
          <button class="btn secondary" data-act="ps-read" ${ps.use ? '' : UI.off('Enter where the pack is first')}>Read its manifest</button></section>
        ${m ? `<section class="dl-card"><header class="dl-ch"><h2>${esc(m.name)}</h2><span class="dl-sub">${esc(m.ver)}</span></header><p class="dl-sub" style="margin-top:0">${esc(m.desc)}</p>
          <div class="field"><label for="ps-inst">Name it</label><input class="input mono" id="ps-inst" value="${esc(ps.inst)}" data-on-change="ps-inst"><div class="hint">How it’s known in your config (<span class="mono">packs.${esc(ps.inst)}</span>); change it to install the same pack twice.</div></div>
          <h3 class="po-h3">Connections it needs</h3><ul class="dl-list">${m.roles.map((role) => { const fit = conns.filter((cn) => kindOf(cn) === role); return `<li class="po-row"><div class="dl-nt"><b>${esc(role)}</b></div><label class="dl-search dl-select po-sel"><select id="ps-role-${role}" data-on-change="ps-role" data-r="${role}"><option value="">${fit.length ? 'Choose a connection…' : `No ${esc(role)} connection yet`}</option>${fit.map((cn, i) => `<option value="${esc(cn.name)}"${i === 0 && fit.length === 1 ? ' selected' : ''}>${esc(cn.name)}</option>`).join('')}<option value="__new__">+ Create a ${esc(role)} connection…</option></select></label></li>`; }).join('') || '<li class="dl-sub">None.</li>'}</ul>
          <h3 class="po-h3">What it adds</h3><p class="dl-sub" style="margin:0">Workflows ${m.workflows.map((w) => `<span class="mono">${esc(w)}</span>`).join(', ')}; triggers ${m.triggers.map((t) => `<span class="mono">${esc(t)}</span>`).join(', ')}, all disarmed until you arm them.</p>
          <div class="dl-actions"><button class="btn primary" data-act="ps-add">Review the change</button></div></section>` : ''}
      </div><div class="dl-col-side"><section class="dl-card"><header class="dl-ch"><h2>Looking for a pack?</h2></header><p class="dl-sub" style="margin-top:0">The official catalog is searchable and every pack in it is trusted by default.</p><a class="btn secondary" href="#/extensions/browse">Browse the catalog</a></section></div></div></div>`;
  }, { nav: 'extensions' });
  UI.act('ps-use', (el) => { ps.use = el.value.trim(); ps.read = null; UI.rerender(); });
  UI.act('ps-inst', (el) => { ps.inst = el.value.trim(); });
  UI.act('ps-role', (el) => { if (el.value === '__new__') { el.value = ''; connForm(null, el.dataset.r); } });
  UI.act('ps-read', () => { const base = ps.use.split('@')[0], nm = base.split('/').filter(Boolean).pop() || 'pack', known = (D.packMembers || {})[nm];
    ps.read = { name: nm, ver: (ps.use.split('@')[1] || 'latest'), desc: known ? 'A pack your team publishes.' : 'Read from its manifest (conductor-pack.yaml).', roles: known ? known.roles : ['github'], workflows: known ? Object.keys(known.workflows) : [nm], triggers: known ? known.triggers : ['on_' + nm.replace(/-/g, '_')] };
    ps.inst = D.packs.some((x) => x.name === nm) ? `${nm}-2` : nm; UI.rerender(); });
  UI.act('ps-add', () => { const m = ps.read, n = { use: ps.use }, roles = {}; m.roles.forEach((r) => { const v = (UI.$('#ps-role-' + r) || {}).value; if (v && v !== '__new__') roles[r] = v; }); if (Object.keys(roles).length) n.connectors = roles;
    if (!/^[a-z][a-z0-9-]*$/.test(ps.inst) || D.packs.some((x) => x.name === ps.inst)) { UI.toast('Choose another name', 'lowercase letters, digits and dashes, not already used', 'fail'); return; }
    UI.staged(S.stage('packs', ps.inst, n)); });
  UI.act('plugin-update', (b) => { const p = D.plugins.find((x) => x.name === b.dataset.p); UI.stepup({ title: `Update ${p.name} ${p.ver} → ${p.update}`, purpose: 'Plugins · update', summary: `<p class="su-lede">You're updating <b>${esc(p.name)}</b> from ${esc(p.ver)} to ${esc(p.update)}. It was checked against your config on a copy first; it ${p.reload === 'in place' ? 'reloads in place, without a restart' : 'restarts the connections that use it'}.${b.dataset.also ? ` ${esc(b.dataset.also)} ${b.dataset.also.includes(',') ? 'move' : 'moves'} with it (they stay current); held connections stay where they are.` : ''}</p>`, ok: 'Update with passkey', what: `{ purpose: <b>plugin_update</b>, plugin: <b>${p.name}</b>, from: <b>${p.ver}</b>, to: <b>${p.update}</b>, validated_on_copy: <b>true</b>, reload: <b>${p.reload}</b> }` }).then((ok) => { if (!ok) return; audit('plugin_update', `${p.name} ${p.ver} → ${p.update} · ${p.reload}`); p.ver = p.update; p.update = null; S.save(); UI.rerender(); UI.toast(`Updated ${p.name} to ${p.ver}`, p.reload === 'in place' ? 'it reloaded in place, without a restart' : 'conductor restarted the connections that use it', 'ok'); }); });
  UI.act('plugin-menu', (b) => { const p = D.plugins.find((x) => x.name === b.dataset.p); UI.menu(b, [['Show permissions', 'shield', 'plugin-caps', `data-p="${p.name}"`], [p.pinned ? 'Unpin (track releases)' : `Pin to ${p.ver}`, 'link', 'plugin-pin', p.instances.length && p.kind === 'connector' ? `data-p="${p.name}"` : UI.off('Pinning is a use: suffix on a connector instance; this plugin has none')], ['-'], ['Remove', 'trash', 'plugin-del', p.instances.length ? UI.off(`Still used by ${p.instances.join(', ')}: remove those first`) : `data-p="${p.name}"`]]); });
  UI.act('plugin-caps', (b) => { const p = D.plugins.find((x) => x.name === b.dataset.p); UI.modal(`<div class="modal-head"><div><div class="caps">plugin show --caps</div><h2>${p.name} ${p.ver}</h2></div><button class="icon-btn" data-close aria-label="Close">${icon('x')}</button></div><div class="modal-body small"><dl class="kv"><dt>source</dt><dd class="mono">${p.src}</dd><dt>network</dt><dd class="mono">api.${p.name}.com:443</dd><dt>spawns</dt><dd class="mono">none</dd><dt>instances</dt><dd class="mono">${p.instances.join(', ') || '—'}</dd></dl><div class="callout small" style="margin-top:10px">A visible permission manifest, recorded at install and enforced on the subprocess.</div></div><div class="modal-foot"><button class="btn" data-close>Close</button></div>`); });
  UI.act('plugin-pin', (b) => { const p = D.plugins.find((x) => x.name === b.dataset.p); p.instances.forEach((inst) => { const c = clone(S.staged('connectors', inst)); c.use = p.pinned ? c.use.split('@')[0] : `${c.use.split('@')[0]}@${p.ver}`; S.stage('connectors', inst, c); }); p.pinned = !p.pinned; S.save(); UI.staged(S.find('connectors', p.instances[0])); });
  UI.act('plugin-del', (b) => UI.stepup({ title: `Remove plugin ${b.dataset.p}`, purpose: 'Plugins · remove', summary: `<p class="su-lede">You're removing the <b>${esc(b.dataset.p)}</b> plugin. Anything that uses it stops working until it's added back.</p>`, danger: true, ok: 'Remove with passkey', what: `{ purpose: <b>plugin_remove</b>, plugin: <b>${b.dataset.p}</b> }` }).then((ok) => { if (!ok) return; D.plugins = D.plugins.filter((p) => p.name !== b.dataset.p); audit('plugin_remove', b.dataset.p); S.save(); UI.rerender(); }));

  /* ================= updates ================= */
  /* Updates, in plain words first: what's ready, that it installs itself, what it checks, how to go back. Technical
     detail (the preflight command, the kept binary, the swap) is folded. */
  UI.route('updates', () => {
    const d = D.daemon, u = S.staged('update', 'update'), avail = d.update.available;
    const check = (ok, t) => `<li>${UI.mark(ok ? 'ok' : 'wait')}<span>${t}</span></li>`;
    return `<div class="page dl-page">${UI.systemTabs('updates')}
      <div class="dl-phead"><div><h1>Updates</h1></div>
        <div class="dl-actions tight"><button class="btn secondary" data-act="upd-check">${icon('updates')}Check now</button></div></div>
      <div class="dl-cols"><div class="dl-col-main">
        ${avail ? `<section class="dl-card"><header class="dl-ch"><h2>${avail}</h2><span class="dl-sub">found ${UI.agoText(d.update.last_check)}</span></header>
          <ul class="upd-checks">${check(true, 'Downloaded, and its signature checks out')}${check(true, 'It can load your config (checked on a copy; your live config isn’t touched)')}${check(true, 'The plugins it needs are installed or can be fetched')}${check(u.auto, u.auto ? `It installs itself within ${UI.humanSpan(u.interval)}` : 'Automatic installs are off: install it yourself')}</ul>
          <div class="dl-actions"><button class="btn primary" data-act="upd-apply">${icon('download')}Install now</button><span class="dl-sub">The same as the automatic install: the current version is kept, so you can go back.</span></div>
          <details class="dl-tech-d"><summary>${icon('chev')}Technical details</summary><dl class="dl-facts"><dt>Check</dt><dd class="mono">${avail} validate (on a copy of the config and plugin state)</dd><dt>Kept</dt><dd><span class="mono">conductor.prev</span>: the previous binary, for rolling back</dd><dt>Install</dt><dd>an atomic swap of the binary, then a restart; unchanged plugins are reloaded in place when that’s on</dd></dl></details></section>`
          : `<section class="dl-card"><p>You’re on ${d.version}, the newest release. Last checked ${UI.agoText(d.update.last_check)}.</p></section>`}
        <section class="dl-card"><header class="dl-ch"><h2>Releases</h2></header><ul class="dl-list">${D.releases.map((r) => `<li class="upd-rel"><span class="mono">${r.v}</span><div class="dl-nt"><span class="dl-sub">${r.date}${r.current ? ' · running now' : ''}</span><ul>${r.notes.map((n) => `<li>${esc(n)}</li>`).join('')}</ul></div><span class="dl-na">${r.current ? '<span class="dl-sub">in use</span>' : r.v === avail ? '<span class="dl-sub">ready</span>' : `<button class="btn sm secondary" data-act="upd-rollback" data-v="${r.v}">${icon('undo')}Go back to ${r.v}…</button>`}</span></li>`).join('')}</ul></section>
      </div><div class="dl-col-side">
        <section class="dl-card"><header class="dl-ch"><h2>How updates install</h2>${stagedTag('update', 'update')}</header><div class="dl-prefs">
          <label class="row">${UI.switchBtn(u.auto, 'upd-pol', 'data-k="auto" aria-label="Install updates automatically"')}<span><b>Install updates automatically</b> <span class="dl-sub">${u.auto ? 'on, the default' : 'off'}</span></span></label>
          ${u.auto ? `<div class="upd-sub"><label class="row">${UI.switchBtn(u.deps, 'upd-pol', 'data-k="deps" aria-label="Keep packs and plugins up to date"')}<span>Keep packs and plugins up to date too</span></label>
          <label class="row">${UI.switchBtn(u.reload === 'in place', 'upd-pol', 'data-k="reload" aria-label="Restart only the plugins that changed"')}<span>Restart only the plugins that changed</span></label></div>` : '<p class="dl-sub">You install each release yourself, from here. To react to a new release (ask someone first, post it), set that up on a workflow.</p>'}
          <div class="field"><label for="upd-int">Look for updates every</label><input class="input mono" id="upd-int" value="${esc(u.interval)}" style="width:90px" data-on-change="upd-int"></div></div>
          <a class="btn sm primary" href="#/config/apply" ${S.find('update', 'update') ? '' : UI.off('Change a setting first')}>Review the change</a></section>
        <p class="dl-sub">A release that can’t load this machine’s config is never installed, and the previous version is always kept.</p>
      </div></div></div>`;
  });
  UI.act('upd-check', () => { D.daemon.update.last_check = now(); audit('update_check', `git ls-remote refs/dist · ${D.daemon.update.available || D.daemon.version} is newest`); S.save(); UI.rerender(); UI.toast('Checked', `${D.daemon.update.available || D.daemon.version} is newest`, 'ok'); });
  UI.act('upd-apply', () => { const to = D.daemon.update.available; UI.confirm({ title: `Install ${to} now?`, body: `conductor checks it again, keeps ${D.daemon.version} so you can go back, then switches over and restarts. Anything running finishes first.`, ok: 'Install now' }).then((ok) => { if (!ok) return; const from = D.daemon.version; D.daemon.version = to; D.daemon.update.available = null; D.releases.forEach((r) => (r.current = r.v === to)); D.attention = D.attention.filter((a) => a.kind !== 'update'); D.fyi = (D.fyi || []).filter((a) => a.kind !== 'update'); audit('update_apply', `${from} → ${to} · doUpdate · conductor.prev kept`); S.save(); UI.refreshChrome(); UI.rerender(); UI.toast(`Updated to ${to}`, 'preflight ✓ · swap · restart · reconnected', 'ok'); }); });
  UI.act('upd-rollback', (b) => UI.confirm({ title: `Go back to ${b.dataset.v}?`, body: `conductor checks that ${b.dataset.v} loads your config, then switches back and restarts. ${D.daemon.version} is kept, so you can return to it.`, ok: `Go back to ${b.dataset.v}`, danger: true }).then((ok) => { if (!ok) return; const from = D.daemon.version; D.daemon.version = b.dataset.v; D.releases.forEach((r) => (r.current = r.v === b.dataset.v)); if (!D.daemon.update.available) D.daemon.update.available = D.releases[0].v === b.dataset.v ? null : D.releases[0].v; audit('update_rollback', `${from} → ${b.dataset.v}`); S.save(); UI.refreshChrome(); UI.rerender(); }));
  const editUpd = (fn) => { const c = clone(S.staged('update', 'update')); fn(c); S.stage('update', 'update', c); UI.rerender(); UI.refreshChrome(); };
  UI.act('upd-pol', (b) => editUpd((c) => { if (b.dataset.k === 'reload') c.reload = c.reload === 'in place' ? 'restart' : 'in place'; else c[b.dataset.k] = !c[b.dataset.k]; }));
  UI.act('upd-int', (inp) => editUpd((c) => { c.interval = inp.value.trim(); }));

  /* ================= connectors, runtimes, hosts, isolation ================= */
  function connForm(name, presetUse) {
    const cur = name ? S.staged('connectors', name) : null;
    const pick = (use) => {
      const decl = SCHEMA.connectors[use];
      UI.form({ caps: name ? `${decl.label} connection` : 'A new connection', title: name ? `Configure ${name}` : `Add a ${decl.label} connection`, edit: !!name, wide: true,
        // two ways in, as an obvious choice: let conductor create the app (recommended), or type in tokens you have
        intro: `${!name && (use === 'slack' || use === 'github') ? `<div class="conn-paths" role="group" aria-label="How to connect">
          <div class="conn-path conn-rec"><b>Connect with ${use === 'slack' ? 'Slack' : 'GitHub'}</b> <span class="dl-sub">recommended</span><p class="dl-why">conductor creates the ${use === 'slack' ? 'Slack app' : 'GitHub App'} for you and fills all of this in. No tokens to copy.</p><button type="button" class="btn primary" data-act="conn-manifest" data-u="${use}">${icon('link')}Connect with ${use === 'slack' ? 'Slack' : 'GitHub'}</button></div>
          <div class="conn-path"><b>I already have ${use === 'slack' ? 'tokens' : 'an app'}</b><p class="dl-why">Pick where each secret is stored, and fill in the rest below.</p><button type="button" class="btn" data-act="conn-manual">Fill it in myself</button></div></div>` : ''}<p class="dl-sub conn-say">Secrets stay in your vault; the config only points to them.</p>`,
        // a new instance's name is prefilled (gh for GitHub, else the service's use key; -2, -3… when taken)
        // a sensitive setting starts on the vault entry named for this service when there is one (conductor/linear)
        values: cur ? Object.assign({ name }, cur) : { ...Object.fromEntries(decl.settings.filter((f) => f.sensitive || f.type === 'secret').map((f) => { /* a credential is type: secret in the schema (r5: the filter read a flag nothing set, so nothing was ever preselected) */ const keyOf = (x) => x.key || String(x.ref || '').split('/').slice(1).join('/'), hit = S.view().secrets.find((x) => { const k = keyOf(x); return k === use || k.startsWith(use + '-') || k.split('/').pop() === use; }); return [f.k, hit ? `{{ vault "${hit.vault}" "${keyOf(hit)}" }}` : undefined]; }).filter(([, v]) => v)), name: (() => { const base = use === 'github' ? 'gh' : use; let n = base, i = 2; while (S.view().connectors.some((c) => c.name === n)) n = `${base}-${i++}`; return n; })() },
        fields: [{ k: 'name', type: 'string', req: true, label: 'Name', help: name ? '' : 'How you refer to this connection, e.g. gh-work', readonly: !!name, ph: use === 'github' ? 'gh' : use, pattern: /^[a-z][a-z0-9-]*$/, patternMsg: 'lowercase, digits, dashes', validate: (v) => (!name && D.connectors.find((c) => c.name === v) ? 'A connection by that name exists.' : '') }].concat(decl.settings.map((f) => Object.assign({}, f, f.type === 'string' && !/path|listen|url$/.test(f.k) ? { type: 'value' } : {}))),
        note: decl.registry && !D.plugins.find((p) => p.name === use) ? `The <b>${use}</b> plugin isn't installed: applying runs <span class="mono">conductor init</span>, which fetches ${decl.registry}@${decl.ver} (official registry, trusted).` : '',
        submit: 'Review change', submitIcon: 'check',
        onSubmit: (v) => { const n = { use: cur ? cur.use : use }; decl.settings.forEach((f) => { if (v[f.k] !== undefined) S.setPath(n, f.k, v[f.k]); }); Object.keys(cur || {}).forEach((k) => { if (!(k in n) && !decl.settings.find((f) => f.k === k) && k !== 'use') n[k] = cur[k]; }); UI.staged(S.stage('connectors', v.name || name, n)); } });
    };
    if (name) return pick(cur.use.split('@')[0]);
    if (presetUse) return pick(presetUse);
    const s = UI.modal(`<div class="modal-head"><div><div class="caps">A new connection</div><h2>Which service?</h2></div><button class="icon-btn" data-close aria-label="Close">${icon('x')}</button></div>
      <div class="modal-body picker"><div class="pk-grid">${Object.entries(SCHEMA.connectors).filter(([u]) => u !== 'git').map(([u, d]) => `<button type="button" class="pk-item" data-act="conn-pick" data-u="${u}"><span class="sc-ic sm" style="--kc:${d.color}">${icon(d.icon)}</span><span class="grow"><b>${d.label}</b><span class="mono tiny muted">use: ${u}${d.registry ? ' · ' + d.registry : ' · builtin'}${D.connectors.some((c) => c.use === u) ? ' · ' + D.connectors.filter((c) => c.use === u).length + ' configured' : ''}</span></span></button>`).join('')}</div></div>`, { wide: true });
    UI.connPick = (u) => { UI.closeModal(); pick(u); };
  }
  UI.act('conn-pick', (b) => UI.connPick(b.dataset.u));
  UI.act('conn-manual', (b) => { const p = b.closest('.conn-paths'); p.classList.add('manual'); const f = p.closest('.modal').querySelector('#gen-form .field input:not([type=hidden]), #gen-form .field select'); if (f) f.focus(); });
  UI.act('conn-manifest', (b) => { UI.dismissModal ? UI.closeModal() : null; UI.connectManifest(b.dataset.u); });
  // Add a connector: the catalog's searchable picker (built-in, official plugin, custom source; a custom one from a git
  // URL at the end); a service conductor has a settings form for opens it, anything else goes through its install page
  const ENG_BUILTIN = { cli: 'Runs a command: the step’s context on stdin, its outputs on stdout' };
  UI.act('conn-add-use', (b) => connForm(null, b.dataset.u));
  UI.act('lang-add', () => UI.catalogPicker('engine', (it) => { if (it) UI.go(`#/extensions/install/engine/${encodeURIComponent(it.name)}`); }));
  UI.act('conn-add', () => UI.catalogPicker('connector', (it) => { if (!it) return; if (SCHEMA.connectors[it.name]) connForm(null, it.name); else UI.go(`#/extensions/install/connector/${encodeURIComponent(it.name)}`); }));
  UI.act('conn-edit', (b) => connForm(b.dataset.c));
  UI.act('conn-del', (b) => { const used = D.workflows.filter((w) => (w.yaml || '').includes(b.dataset.c + '.')).map((w) => w.name); UI.confirm({ title: `Remove connector ${b.dataset.c}?`, body: `Stages removing it from connectors:.${used.length ? ` <b>Workflows that use it:</b> ${used.join(', ')} — the validator will flag them.` : ''}`, ok: 'Stage removal', danger: true }).then((ok) => ok && UI.staged(S.stage('connectors', b.dataset.c, null))); });
  function rtForm(name, presetUse) {
    const cur = name ? S.staged('runtimes', name) : null;
    const use0 = cur ? cur.use : presetUse || 'cli';
    const fields = [{ k: 'name', type: 'string', req: true, readonly: !!name, pattern: /^[a-z][a-z0-9-]*$/, patternMsg: 'lowercase, digits, dashes', validate: (v) => (!name && D.runtimes.find((r) => r.name === v) ? 'Exists already.' : '') }, { k: 'use', type: 'enum', req: true, options: Object.keys(SCHEMA.runtimes), def: use0 }];
    const seen = new Set(['name', 'use']);
    Object.entries(SCHEMA.runtimes).forEach(([u, d]) => d.settings.forEach((f) => { const key = f.k; if (seen.has(key + '|' + u)) return; seen.add(key + '|' + u); fields.push(Object.assign({}, f, { k: key + '@' + u, label: f.label || key, show: (v) => (v.use || use0) === u })); }));
    UI.form({ caps: 'Plugins · agent runtimes', title: name ? `Edit runtime ${name}` : 'Add a runtime', edit: !!name, wide: true,
      values: cur ? Object.assign({ name, use: cur.use }, Object.fromEntries((SCHEMA.runtimes[cur.use] || { settings: [] }).settings.map((f) => [f.k + '@' + cur.use, S.getPath(cur, f.k)]))) : { use: use0 },
      fields, note: 'isolation: none on a runtime runs every agent it launches as you, unsandboxed. That is a widening; applying asks for your passkey.', submit: 'Review change', submitIcon: 'check',
      onSubmit: (v) => { const u = v.use || use0, n = { use: u }; Object.entries(v).forEach(([k, x]) => { const [fk, fu] = k.split('@'); if (fu === u && x !== undefined && x !== '') S.setPath(n, fk, x === true || x === false ? x : x); }); if (n.default === false) delete n.default; UI.staged(S.stage('runtimes', v.name || name, n)); } });
  }
  // Add a runtime: the same picker as a connection, then its form (67)
  UI.act('rt-add', () => UI.catalogPicker('runtime', (it) => { if (!it) return; if (SCHEMA.runtimes[it.name]) rtForm(null, it.name); else UI.go(`#/extensions/install/runtime/${encodeURIComponent(it.name)}`); }));
  UI.act('rt-more', (b) => { const r = b.dataset.r, rt = D.runtimes.find((x) => x.name === r) || {}, d = `data-r="${r}"`, p = plug2(rt.use);
    UI.menu(b, [['Edit…', 'edit', 'rt-edit', d], ...(p ? [['-'], [p.update ? `Update now to ${p.update}` : 'Update now', 'updates', 'plugin-update', p.update ? `data-p="${p.name}"` : UI.off('Already on the newest release')], [p.pinned ? 'Stay current' : 'Hold at this version', p.pinned ? 'updates' : 'pin', 'pl-hold', `data-p="${p.name}"`], ['Show the plugin’s permissions', 'shield', 'plugin-caps', `data-p="${p.name}"`]] : []), ['-'], ['Remove…', 'trash', 'rt-del', d]]); });
  UI.act('lang-more', (b) => { const p = plug2(b.dataset.p);
    UI.menu(b, p ? [[p.update ? `Update now to ${p.update}` : 'Update now', 'updates', 'plugin-update', p.update ? `data-p="${p.name}"` : UI.off('Already on the newest release')], [p.pinned ? 'Stay current' : 'Hold at this version', p.pinned ? 'updates' : 'pin', 'pl-hold', `data-p="${p.name}"`], ['Show the plugin’s permissions', 'shield', 'plugin-caps', `data-p="${p.name}"`], ['-'], ['Remove…', 'trash', 'plugin-del', p.instances.length ? UI.off(`Still used by ${p.instances.join(', ')}`) : `data-p="${p.name}"`]]
      : [['Show where it’s used', 'workflows', 'lang-where', `data-p="${esc(b.dataset.p)}"`], ['-'], ['Built into conductor', 'zap', 'noop', UI.off('Comes with conductor: it updates with conductor and can’t be removed')]]); });
  // the workflows whose code steps run in it: one opens it, several are offered as a menu, none says so
  UI.act('lang-where', (b) => { const n = b.dataset.p, ws = [...new Set(D.workflows.filter((w) => (w.steps || []).flat(2).some((st) => st && st.kind === 'code' && (st.run === n || (n === 'cli' && st.run === 'command')))).map((w) => w.name))];
    if (ws.length === 1) UI.go(`#/workflows/${encodeURIComponent(ws[0])}`); else if (!ws.length) UI.toast('Not used yet', `No workflow step runs ${n} yet`, 'ok');
    else UI.menu(document.querySelector(`[data-act="lang-more"][data-p="${n}"]`) || b, ws.map((w) => [w, 'workflows', 'lang-open', `data-w="${esc(w)}"`])); });
  UI.act('lang-open', (b) => UI.go(`#/workflows/${encodeURIComponent(b.dataset.w)}`));
  UI.act('host-more', (b) => { const d = `data-h="${b.dataset.h}"`; UI.menu(b, [['Test the connection', 'plugtest', 'host-test', d], ['Edit…', 'edit', 'host-edit', d], ['-'], ['Remove…', 'trash', 'host-del', d]]); });
  const plug2 = (u) => D.plugins.find((x) => x.name === u);
  UI.act('pl-hold', (b) => { const p = plug2(b.dataset.p); p.pinned = !p.pinned; S.save(); UI.rerender({ keepScroll: true }); UI.toast(p.pinned ? `Held at ${p.ver}` : 'Stays current', p.pinned ? `${p.name} won’t update until you let it` : `${p.name} takes each new release`, 'ok'); });
  UI.act('rt-edit', (b) => rtForm(b.dataset.r));
  UI.act('rt-del', (b) => UI.confirm({ title: `Remove runtime ${b.dataset.r}?`, body: 'Stages removing it. Steps and bots that name it fail validation until they pick another.', ok: 'Stage removal', danger: true }).then((ok) => ok && UI.staged(S.stage('runtimes', b.dataset.r, null))));
  function hostForm(name) {
    const cur = name ? S.staged('hosts', name) : null;
    UI.form({ caps: 'Plugins · machines', title: name ? `Edit host ${name}` : 'Add a host', edit: !!name, values: cur ? Object.assign({ name }, cur) : {},
      fields: [{ k: 'name', type: 'string', req: true, readonly: !!name, pattern: /^[a-z][a-z0-9-]*$/, patternMsg: 'lowercase, digits, dashes', validate: (v) => (!name && D.hosts.find((h) => h.name === v) ? 'Exists already.' : '') }, { k: 'addr', type: 'string', req: true, label: 'Address', ph: 'build-box.tail0000.ts.net' }, { k: 'user', type: 'string', req: true, ph: 'conductor' }, { k: 'env', type: 'map', help: 'Environment for commands run there.' }],
      note: 'Remote agents reach conductor by SSH reverse tunnel to the skill socket only, never the dashboard.', submit: 'Review change', submitIcon: 'check',
      onSubmit: (v) => { const n = { addr: v.addr, user: v.user }; if (v.env) n.env = v.env; UI.staged(S.stage('hosts', v.name || name, n)); } });
  }
  UI.act('host-add', () => hostForm(null));
  UI.act('conn-more', (b) => { const c = D.connectors.find((x) => x.name === b.dataset.c), v = UI.connVersion(c), d = `data-c="${b.dataset.c}"`, plug = D.plugins.find((x) => x.name === v.u);
    UI.menu(b, [['Configure…', 'settings', 'conn-edit', d], ...(v.kind === 'builtin' ? [] : [['-'],
      [v.upd ? `Update now to ${v.upd}` : 'Update now', 'updates', 'cn-update', v.upd ? d : UI.off(v.kind === 'range' ? `On the newest release within ${v.range}` : 'Already on the newest release')],
      ['Hold at this version', 'lock', 'cn-hold', v.kind === 'pinned' ? UI.off(`Held at ${v.ver}`) : d],
      ['Stay current', 'updates', 'cn-current', v.kind === 'current' ? UI.off('Already takes every new release') : d],
      ['Stay within…', 'filter', 'cn-range', d],
      ['Choose a version…', 'obj-update', 'cn-choose', d],
      ['Show the plugin’s permissions', 'shield', 'plugin-caps', plug ? `data-p="${plug.name}"` : UI.off('Built in')]]),
      ['-'], ['Remove this connection…', 'trash', 'conn-del', d]]); });
  UI.act('host-edit', (b) => hostForm(b.dataset.h));
  UI.act('host-del', (b) => UI.confirm({ title: `Remove host ${b.dataset.h}?`, body: 'Stages removing it. Runtimes pinned to it fail validation until moved.', ok: 'Stage removal', danger: true }).then((ok) => ok && UI.staged(S.stage('hosts', b.dataset.h, null))));
  UI.act('host-test', (b) => { const h = D.hosts.find((x) => x.name === b.dataset.h); h.rtt = Math.round(2 + Math.random() * 30) + 'ms'; h.status = 'ok'; h.checked = now(); S.save(); UI.rerender(); UI.toast(`${h.name} reachable`, `ssh ${h.user}@${h.addr} · ${h.rtt}`, 'ok'); });

  /* A connection's version (44c): its `use:` tracks the newest release (`use: github`), stays within a range
     (`github@^2.0`) or holds one build (`github@v2.0.0`); two connections may run different versions of one plugin side
     by side. Each change is a normal config change: staged, reviewed, applied. */
  const cv = { q: '', svc: [], status: [], group: 'none', sel: new Set() };
  const semv = (v) => String(v || '').replace(/^v/, '').split('.').map((x) => parseInt(x, 10) || 0);
  const cmpV = (a, b) => { const x = semv(a), y = semv(b); for (let i = 0; i < 3; i++) if ((x[i] || 0) !== (y[i] || 0)) return (x[i] || 0) - (y[i] || 0); return 0; };
  const inRange = (v, r) => { const b = semv(r.slice(1)), x = semv(v); if (r[0] === '^') return x[0] === b[0] && cmpV(v, r.slice(1)) >= 0; if (r[0] === '~') return x[0] === b[0] && x[1] === b[1] && cmpV(v, r.slice(1)) >= 0; return cmpV(v, r) === 0; };
  UI.releasesOf = (u) => { const p = D.plugins.find((x) => x.name === u); return (D.pluginReleases && D.pluginReleases[u]) || (p ? [p.update, p.ver].filter(Boolean).map((v) => ({ v, notes: '' })) : []); };
  UI.connVersion = (c) => { const use = String((S.staged('connectors', c.name) || c.cfg || {}).use || c.use || ''), [u, at] = use.split('@'), p = D.plugins.find((x) => x.name === u);
    if (!p) return { u, kind: 'builtin', ver: '', words: '' };
    const newest = (UI.releasesOf(u)[0] || {}).v || p.update || p.ver;
    if (!at) { const upd = p.update && cmpV(p.update, p.ver) > 0 ? p.update : null; return { u, kind: 'current', ver: p.ver, upd, words: `${p.ver} · stays current` }; }
    if (/^[\^~]/.test(at)) { const fits = UI.releasesOf(u).map((r) => r.v).filter((v) => inRange(v, at)).sort(cmpV), ver = fits.filter((v) => cmpV(v, p.ver) <= 0).pop() || p.ver, top = fits.pop();
      return { u, kind: 'range', range: at, ver, upd: top && cmpV(top, ver) > 0 ? top : null, words: `${ver} · within ${at}` }; }
    const ver = at.startsWith('v') ? at : 'v' + at; return { u, kind: 'pinned', ver, upd: cmpV(newest, ver) > 0 ? newest : null, words: `${ver} · held at this version` }; };
  const setUse = (name, use) => { const c = clone(S.staged('connectors', name)); c.use = use; return S.stage('connectors', name, c); };
  const conn = (n) => D.connectors.find((c) => c.name === n);
  UI.act('cn-sel', (el) => { if (el.checked) cv.sel.add(el.dataset.c); else cv.sel.delete(el.dataset.c); UI.rerender({ keepScroll: true }); });
  UI.act('cn-bulk-none', () => { cv.sel.clear(); UI.rerender({ keepScroll: true }); });
  // Update now: a connection that stays current takes the plugin's update (every connection that stays current moves with
  // it); one held or in a range moves to the newest release it may take
  const updateUse = (c) => { const v = UI.connVersion(c); if (!v.upd) return null; return v.kind === 'range' ? null : `${v.u}@${v.upd}`; };
  UI.act('cn-update', (b) => { const c = conn(b.dataset.c), v = UI.connVersion(c);
    if (v.kind === 'current' || v.kind === 'range') { const p = D.plugins.find((x) => x.name === v.u), also = D.connectors.filter((x) => x !== c && UI.connVersion(x).u === v.u && UI.connVersion(x).kind !== 'pinned').map((x) => x.name);
      // nothing is said before you sign (r5 minor: 'Installing Slack v1.7.0' came before the passkey); the sheet says
      // which connections move with it
      return UI.acts['plugin-update']({ dataset: { p: p.name, also: also.join(', ') } }); }
    UI.staged(setUse(c.name, updateUse(c))); });
  UI.act('cn-hold', (b) => { const c = conn(b.dataset.c), v = UI.connVersion(c); UI.staged(setUse(c.name, `${v.u}@${v.ver}`)); });
  UI.act('cn-current', (b) => { const c = conn(b.dataset.c), v = UI.connVersion(c); UI.staged(setUse(c.name, v.u)); });
  UI.act('cn-range', (b) => { const c = conn(b.dataset.c), v = UI.connVersion(c), [ma, mi] = semv(v.ver);
    UI.form({ caps: `connectors.${c.name}.use`, title: `Keep ${c.name} within a range`, fields: [{ k: 'range', type: 'enum', label: 'Take new releases', options: [[`^${ma}.${mi}`, `Up to the next major version (^${ma}.${mi}): new features and fixes`], [`~${ma}.${mi}`, `Fixes only (~${ma}.${mi}): ${ma}.${mi}.x`]], req: true }], values: { range: v.kind === 'range' ? v.range : `^${ma}.${mi}` }, submit: 'Stage the change',
      onSubmit: (x) => UI.staged(setUse(c.name, `${v.u}@${x.range}`)) }); });
  UI.act('cn-choose', (b) => { const c = conn(b.dataset.c), v = UI.connVersion(c), rs = UI.releasesOf(v.u);
    UI.modal(`<div class="modal-head"><div><div class="caps">connectors.${esc(c.name)}.use</div><h2>Choose a version for ${esc(c.name)}</h2></div><button class="icon-btn" data-close aria-label="Close">${icon('x')}</button></div>
      <div class="modal-body"><p class="dl-sub">It holds the version you choose until you change it. Other connections of ${esc(KINDW(v.u))} keep theirs: they can run side by side.</p><ul class="dl-list cn-rels">${rs.map((r) => `<li class="cn-rel"><span class="dl-nt"><b>${esc(r.v)}${r.v === v.ver ? ' <span class="dl-sub">· in use</span>' : ''}</b><span class="dl-why">${esc(r.notes || '')}</span>${r.date ? `<span class="dl-sub">${esc(r.date)}</span>` : ''}</span><span class="dl-na">${r.v === v.ver ? '' : `<button class="btn sm secondary" data-act="cn-pick" data-c="${esc(c.name)}" data-v="${esc(r.v)}" data-close>Use ${esc(r.v)}</button>`}</span></li>`).join('')}</ul></div>`); });
  UI.act('cn-pick', (b) => { const c = conn(b.dataset.c); UI.staged(setUse(c.name, `${UI.connVersion(c).u}@${b.dataset.v}`)); });
  UI.act('cn-bulk-hold', () => { let ch; [...cv.sel].map(conn).filter(Boolean).forEach((c) => { const v = UI.connVersion(c); if (v.ver) ch = setUse(c.name, `${v.u}@${v.ver}`); }); cv.sel.clear(); if (ch) UI.staged(ch); else UI.rerender(); });
  UI.act('cn-bulk-update', () => { let ch; const cur = new Set(); [...cv.sel].map(conn).filter(Boolean).forEach((c) => { const v = UI.connVersion(c); if (!v.upd) return; if (v.kind === 'pinned') ch = setUse(c.name, `${v.u}@${v.upd}`); else cur.add(v.u); }); cv.sel.clear();
    if (ch) UI.staged(ch); else if (!cur.size) { UI.toast('Nothing to update', 'the selected connections are on the newest release they may take', 'idle'); UI.rerender(); }
    cur.forEach((u) => UI.acts['plugin-update']({ dataset: { p: u } })); });
  const KINDW = (u) => (SCHEMA.connectors[u] || {}).label || u;
  UI.route('connectors/:tab?', ({ tab }) => {
    tab = tab || 'connectors';
    if (tab === 'isolation') { setTimeout(() => location.replace('#/safety')); return ''; } // the sandbox lives in Safety now
    const d = D.daemon, sb = S.staged('sandbox', 'sandbox');
    // one quiet line per connector; only one that needs a look carries a mark and its reason; counts are a hover away
    const badC = D.connectors.filter((c) => !['ok', 'idle'].includes(c.status));
    // one compact line per connection (the Overview's row): its mark, name and kind, what it's done (or, when it needs a
    // look, why, in the state's colour with a leading edge), and its actions in one place
    const KIND = (u) => (SCHEMA.connectors[u] || {}).label || u;
    // which bots reach a connection, and how, from their grants (UI.verbMode), never a hand-written line
    // a connection answers "is it connected and working?" first, in words; which bots use it is a count (the names on hover)
    const EV = (u) => Object.keys((SCHEMA.connectors[u] || {}).events || {}).length;
    const state = (c) => { const use = (c.cfg && c.cfg.use || c.use || '').split('@')[0]; return c.status === 'idle' ? (S.view().bots.some((b) => JSON.stringify((b.cfg || {}).skill || {}).includes(`"${c.name}.`)) ? 'Connected; nothing has used it yet' : 'Connected, not used yet') : c.events ? `Connected and working: ${c.events.toLocaleString()} event${c.events === 1 ? '' : 's'} came in today` : EV(use) ? 'Connected; no events yet today' : 'Connected and ready (used for actions only)'; };
    const users = (c) => { const r = reach(c); if (/^no bot/.test(r)) return ''; const n = r.replace(/^reached by /, '').split(/, (?![^()]*\))/).length; return ` · <span class="dl-sub" title="${esc(r)}">used by ${n} bot${n === 1 ? '' : 's'}</span>`; };
    const reach = (c) => { const by = S.view().bots.map((b) => { const cf = S.staged('bots', b.name) || b.cfg || {}, sv = S.getPath(cf, 'skill.verbs'), vs = (Array.isArray(sv) ? sv : sv ? Object.keys(sv) : []).filter((v) => v.startsWith(c.name + '.')); if (!vs.length) return null; const modes = new Set(vs.map((v) => UI.verbMode(b, v))); return `${b.name} (${modes.has('own') ? 'on its own' : modes.has('asks') ? 'asks first' : 'refused'})`; }).filter(Boolean); return by.length ? `reached by ${by.join(', ')}` : 'no bot reaches it'; };
    const connRow = (c) => { const use = (S.staged('connectors', c.name) || c.cfg || {}).use || c.use, bad = !['ok', 'idle'].includes(c.status), s2 = bad ? (c.status === 'wait' ? 'wait' : 'fail') : c.status === 'idle' ? 'idle' : 'ok';
      return `<li class="dl-conn conn-row${bad ? ' attn' : ''}" data-s="${s2}"><span class="dl-ng">${bad ? UI.mark(s2, { lg: true, label: s2 === 'wait' ? 'Waiting' : 'Failed' }) : ''}</span>
        <span class="dl-nt"><b>${esc(c.name)} <span class="tgt">${esc(KIND(use))}</span>${c.env ? ` ${UI.envTag(c.env)}` : ''} ${stagedTag('connectors', c.name)}</b><span class="${bad ? 'reason' : 'dl-why'}">${bad ? esc(c.detail) : `${state(c)}${users(c)}`}</span></span>
        <span class="dl-na">${bad ? `<button class="btn sm secondary" data-act="conn-edit" data-c="${c.name}">Fix it…</button>` : ''}<button class="icon-btn" data-act="conn-more" data-c="${c.name}" aria-label="More for ${esc(c.name)}">${icon('more')}</button></span></li>`; };
    const conns = `<div class="panel"><ul class="dl-list dl-conns">${badC.map(connRow).join('')}${D.connectors.filter((c) => !badC.includes(c)).map(connRow).join('')}</ul>
${D.pending.filter((x) => x.coll === 'connectors' && x.before == null).map((x) => `<div class="item bot-card" style="border-style:dashed"><span class="conn-name"><b>${x.key}</b> <span class="item-id">use: ${esc(x.after.use)}</span></span><span class="grow">${tag('new · staged', 'run')} <span class="muted small">Not live until you apply it.</span></span><a class="btn sm" href="#/config/apply">Review</a></div>`).join('')}      <div class="panel-foot"><button class="btn" data-act="conn-add">${icon('plus')}Add a connector</button></div></div>`;
    // one place for the services conductor talks to: each service (built in, official or from a source) with its version,
    // an update when there is one, and the connections configured for it, each saying whether it's working
    const plug = (u) => D.plugins.find((x) => x.name === u);
    const svcOriginKey = (u) => ({ 'built in': 'builtin', official: 'official' }[svcOrigin(u)] || 'custom');
    const svcOrigin = (u) => { const p = plug(u); return p ? (/^conductor-plugins\//.test(p.src) ? 'official' : 'from a source') : SCHEMA.connectors[u] && !SCHEMA.connectors[u].registry && !p ? 'built in' : 'official'; };
    const svcHead = (u, n) => { const p = plug(u);
      return `<li class="svc-head" data-svc="${esc(u)}"><span class="svc-name"><b>${esc(KIND(u))}</b> <span class="dl-sub">${svcOrigin(u)}${p ? ` · ${esc(p.ver)}${p.pinned ? ' · kept at this version' : ''}` : ''}</span></span>
        <span class="dl-na">${p && p.update ? `<button class="btn sm secondary" data-act="plugin-update" data-p="${esc(u)}">${icon('updates')}Update to ${esc(p.update)}…</button>` : ''}<button class="btn sm ghost" data-act="conn-add-use" data-u="${esc(u)}">${icon('plus')}${n ? 'Another' : 'Connect'}</button>${p ? `<button class="icon-btn" data-act="plugin-menu" data-p="${esc(u)}" aria-label="More for ${esc(KIND(u))}">${icon('more')}</button>` : ''}</span></li>`; };
    const useOf = (c) => ((S.staged('connectors', c.name) || c.cfg || {}).use || c.use || '').split('@')[0];
    const uses = [...new Set(D.connectors.map(useOf).concat(D.plugins.filter((p) => p.kind === 'connector').map((p) => p.name)))];
    uses.sort((a, b) => (D.connectors.some((c) => useOf(c) === a && badC.includes(c)) ? 0 : 1) - (D.connectors.some((c) => useOf(c) === b && badC.includes(c)) ? 0 : 1) || KIND(a).localeCompare(KIND(b)));
    // the Connections tab: one flat list of connections (no service headers): each row its name, then whether it's
    // working, then the service, its origin and its version policy as facts; Group by Service is there when wanted
    const svcOf = (c) => KIND(useOf(c)), vp = (c) => UI.connVersion(c);
    const keepC = (c) => { const st = c.status === 'idle' ? 'idle' : ['ok'].includes(c.status) ? 'ok' : 'attn';
      return (!cv.svc.length || cv.svc.includes(useOf(c))) && (!cv.status.length || cv.status.includes(st)) && (!cv.q || `${c.name} ${svcOf(c)} ${useOf(c)}`.toLowerCase().includes(cv.q.toLowerCase())); };
    const tally = (xs) => { const m = new Map(); xs.forEach(([v, label]) => { const o = m.get(v) || { v, label, n: 0 }; o.n++; m.set(v, o); }); return [...m.values()]; };
    const cbar = UI.filterBar({ id: 'cn', state: cv, placeholder: 'Find a connection, or filter: service:github', label: 'Find and filter connections', fields: [
      { k: 'svc', label: 'Service', alias: 'service', multi: true, options: () => tally(D.connectors.map((c) => [useOf(c), svcOf(c)])) },
      { k: 'status', label: 'Status', multi: true, keepOrder: true, options: () => [['attn', 'Needs a look'], ['ok', 'Working'], ['idle', 'Not used yet']].map(([v, label]) => ({ v, label, n: D.connectors.filter((c) => (c.status === 'idle' ? 'idle' : c.status === 'ok' ? 'ok' : 'attn') === v).length })).filter((o) => o.n) },
      { k: 'group', label: 'Group by', view: true, keepOrder: true, def: 'none', title: 'Group by', find: 'Group by', options: () => [{ v: 'none', label: 'None' }, { v: 'svc', label: 'Service' }] }],
      onChange: () => UI.rerender({ keepScroll: true }) });
    const cnRow = (c) => { const v = vp(c), bad = !['ok', 'idle'].includes(c.status), s2 = bad ? (c.status === 'wait' ? 'wait' : 'fail') : c.status === 'idle' ? 'idle' : 'ok';
      return `<li class="dl-conn conn-row cn-row${bad ? ' attn' : ''}" data-s="${s2}" data-c="${esc(c.name)}"><label class="cn-sel"><input type="checkbox" class="cbx" data-on-change="cn-sel" data-c="${esc(c.name)}"${cv.sel.has(c.name) ? ' checked' : ''} aria-label="Select ${esc(c.name)}"></label>
        <span class="dl-nt"><b>${esc(c.name)} ${stagedTag('connectors', c.name)}</b><span class="${bad ? 'reason' : 'dl-why'}">${bad ? esc(c.detail) : `${state(c)}${users(c)}`}</span>
          ${UI.factsLine([UI.pluginFact.update(v.upd), UI.pluginFact.env(c.env), UI.pluginFact.service(useOf(c)), UI.pluginFact.origin(svcOriginKey(useOf(c))), UI.pluginFact.policy(v), UI.pluginFact.version(v.ver)])}</span>
        <span class="dl-na">${bad ? `<button class="btn sm secondary" data-act="conn-edit" data-c="${c.name}">Fix it…</button>` : ''}<button class="icon-btn" data-act="conn-more" data-c="${c.name}" aria-label="More for ${esc(c.name)}">${icon('more')}</button></span></li>`; };
    const shownC = D.connectors.filter(keepC).sort((a, b) => (badC.includes(a) ? 0 : 1) - (badC.includes(b) ? 0 : 1) || a.name.localeCompare(b.name));
    const cnBulk = cv.sel.size ? `<div class="m-bulk cn-bulk"><b>${cv.sel.size} selected</b><button class="btn sm secondary" data-act="cn-bulk-update">Update now</button><button class="btn sm secondary" data-act="cn-bulk-hold">Hold at their versions</button><button class="btn sm ghost" data-act="cn-bulk-none">Clear</button></div>` : '';
    const cnList = (xs) => `<ul class="dl-list dl-conns cn-list">${xs.map(cnRow).join('') || '<li class="dl-empty-row dl-sub">No connection matches.</li>'}</ul>`;
    const services = `${cbar}${cnBulk}${cv.group === 'svc' ? `<div class="tk-groups">${[...new Set(shownC.map(useOf))].map((u) => `<section class="dl-card dl-flush cn-group"><header class="dl-ch cn-gh"><h2>${esc(KIND(u))}</h2><span class="dl-sub">${svcOrigin(u)}</span></header>${cnList(shownC.filter((c) => useOf(c) === u))}</section>`).join('')}</div>` : `<div class="panel">${cnList(shownC)}`}
${D.pending.filter((x) => x.coll === 'connectors' && x.before == null).map((x) => `<div class="item bot-card" style="border-style:dashed"><span class="conn-name"><b>${x.key}</b> <span class="item-id">${esc(KIND(String(x.after.use).split('@')[0]))}</span></span><span class="grow">${tag('new · staged', 'run')} <span class="muted small">Not live until you apply it.</span></span><a class="btn sm" href="#/config/apply">Review</a></div>`).join('')}${cv.group === 'svc' ? '' : '</div>'}`;
    // code languages (the engines behind code steps): built in or added, with version and update
    const langRows = Object.entries(ENG_BUILTIN).map(([n, d]) => [n, d, null]).concat(D.plugins.filter((p) => p.kind === 'engine').map((p) => [p.name, '', p]));
    // the other three tabs read like Connections (67): one flat row each, its name, how it is, then its facts (its glyph,
    // where it comes from, how it updates, its version), and one ⋯ for everything you can do with it
    const plRow = (o) => `<li class="dl-conn conn-row pl-row${o.bad ? ' attn' : ''}" data-k="${esc(o.key)}"><span class="dl-ng">${o.bad ? UI.mark('fail', { lg: true, label: 'Needs a look' }) : o.glyph || ''}</span><span class="dl-nt"><b>${esc(o.name)} ${o.staged || ''}</b><span class="${o.bad ? 'reason' : 'dl-why'}">${o.say}</span>${UI.factsLine(o.facts.filter(Boolean))}</span>
      <span class="dl-na">${o.act || ''}<button class="icon-btn" data-act="${o.menu}" ${o.attrs} aria-label="More for ${esc(o.name)}">${icon('more')}</button></span></li>`;
    const plVer = (p) => (p ? { kind: p.pinned ? 'pinned' : 'current', ver: p.ver } : null);
    // where a language is used: the code steps that run in it, each a link to its workflow (67: never 'run: js')
    const langSteps = (n) => D.workflows.flatMap((w) => (w.steps || []).flat(2).filter((st) => st && st.kind === 'code' && (st.run === n || (n === 'cli' && st.run === 'command'))).map(() => w.name));
    const usedSay = (n) => { const ws = langSteps(n), u = [...new Set(ws)]; return ws.length ? `Used by ${ws.length} workflow ${ws.length === 1 ? 'step' : 'steps'}, in ${u.map((w) => `<a href="#/workflows/${encodeURIComponent(w)}">${esc(w)}</a>`).join(', ')}` : 'Not used by any workflow yet'; };
    const languages = `<div class="panel"><ul class="dl-list dl-conns">${langRows.map(([n, d, p]) => plRow({ key: n, name: n, glyph: UI.kindGlyph('engine', n) || icon('code'), say: usedSay(n),
      facts: [p && p.update ? UI.pluginFact.update(p.update) : null, UI.pluginFact.origin(p ? 'official' : 'builtin'), p ? UI.pluginFact.policy(plVer(p)) : null, p ? UI.pluginFact.version(p.ver) : { icon: 'tag', value: D.daemon.version, label: 'Ships with this conductor' }],
      menu: 'lang-more', attrs: `data-p="${esc(n)}" data-builtin="${p ? '' : '1'}"` })).join('')}</ul></div>`;
    const runtimes = `<div class="panel"><ul class="dl-list dl-conns">${D.runtimes.map((r) => { const p = plug(r.use), c = r.cfg || {}, bad = !['ok', 'idle'].includes(r.status), tool = c.tool || r.use;
      return plRow({ key: r.name, name: r.name, glyph: UI.kindGlyph('runtime', tool) || icon('server'), staged: stagedTag('runtimes', r.name), bad, say: bad ? esc(r.detail) : `${r.status === 'idle' ? 'Set up, not used yet' : 'Ready'}${c.default ? ' · the default for new bots' : ''}`,
        facts: [p && p.update ? UI.pluginFact.update(p.update) : null, /none/.test(r.isolation) ? { prod: true, value: 'Not sandboxed', label: 'Every agent it starts runs as you' } : { icon: 'obj-sandbox', value: 'Sandboxed', label: r.isolation },
          Object.assign({ icon: 'server', value: tool, label: 'Agent runtime' }, UI.kindGlyph('runtime', tool) ? { glyph: UI.kindGlyph('runtime', tool) } : {}), UI.pluginFact.origin(p ? 'official' : 'builtin'), p ? UI.pluginFact.policy(plVer(p)) : null, p ? UI.pluginFact.version(p.ver) : null],
        menu: 'rt-more', attrs: `data-r="${esc(r.name)}"` }); }).join('')}</ul></div>`;
    const hosts = `<div class="panel"><ul class="dl-list dl-conns">${D.hosts.map((h) => plRow({ key: h.name, name: h.name, glyph: icon('obj-host'), staged: stagedTag('hosts', h.name), bad: h.status === 'fail',
      say: h.status === 'ok' ? `Reachable · ${esc(h.rtt)} away` : h.status === 'fail' ? 'Can’t be reached' : 'Not checked yet',
      facts: [{ icon: 'globe', value: h.addr, label: 'Address' }, { icon: 'members', value: `as ${h.user}`, label: 'Signs in as' }], menu: 'host-more', attrs: `data-h="${esc(h.name)}"` })).join('')}</ul></div>`;
    const connSay = badC.length ? `<b>${badC.length} ${badC.length === 1 ? 'connector needs' : 'connectors need'} a look.</b> The other ${D.connectors.length - badC.length} are healthy.` : `<b>All ${D.connectors.length} connectors are healthy.</b> ${D.hosts.length} hosts and ${D.runtimes.length} runtimes are set up.`;
    return `<div class="page">${UI.head('Plugins', { lede2: 'What conductor can use: the connections to the services it talks to, and what it runs agents and code with.', actions: (() => { const [act, l] = { runtimes: ['rt-add', 'Add a runtime'], languages: ['lang-add', 'Add a language'], hosts: ['host-add', 'Add a machine'] }[tab] || ['conn-add', 'Add a connection']; return `<button class="btn primary" data-act="${act}">${icon('plus')}${l}</button>`; })() })}
      ${UI.tabs([['connectors', 'Connections', '#/connectors/connectors', D.connectors.length], ['runtimes', 'Agent runtimes', '#/connectors/runtimes', D.runtimes.length], ['languages', 'Code languages', '#/connectors/languages', langRows.length], ['hosts', 'Machines', '#/connectors/hosts', D.hosts.length]], tab)}
      ${tab === 'runtimes' ? runtimes : tab === 'hosts' ? hosts : tab === 'languages' ? languages : services}
      <p class="muted small" style="margin-top:12px">Every change here is reviewed as a diff and validated on a copy before it applies.</p></div>`;
  }, { nav: 'connectors' });
  /* the sandbox, in plain words, for the Safety page: what it protects, on or off, the kind of sandbox, the sites agents
     may reach, what runs outside it, and the containers; paths and signing mechanics are folded under Details */
  UI.sandboxHTML = () => { const d = D.daemon, sb = S.staged('sandbox', 'sandbox'), on = sb.agents !== 'off', staged = !!S.find('sandbox', 'sandbox');
    const outside = D.optouts.map((o) => ({ what: /\(hand-off\)/.test(o.where) ? `The hand-off step for ${o.where.replace(/^trigger /, '').replace(/ \(hand-off\)/, '')} events` : o.where.replace(/^trigger /, 'The trigger '), who: o.owner, why: 'It runs as you, with your files and your network.', href: '#/workflows', go: 'Change it' }))
      .concat(D.runtimes.filter((r) => /none/.test(r.isolation)).map((r) => ({ what: `The runtime ${r.name}`, who: 'your config', why: 'Every agent it starts runs as you.', act: `data-act="rt-edit" data-r="${r.name}"`, go: 'Change it' })));
    return `<div class="dl-cols" style="margin-top:0"><div class="dl-col-main">
      <section class="dl-card sb-card"><header class="dl-ch"><h2>The sandbox</h2>${staged ? UI.statusMark('staged', 'Not applied') : ''}</header>
        <label class="row">${UI.switchBtn(on, 'sb-toggle', 'aria-label="Run agents in the sandbox"')}<span><b>Run agents in the sandbox</b> <span class="dl-sub">${on ? 'on' : 'off'}</span></span></label>
        <p class="sb-say">${on ? 'Agents can’t read your keys or other bots’ files, can reach only the sites below, and can’t push code themselves: conductor pushes for them, signed as you.' : '<b>Off: agents run as you</b>, with your files, your keys and your network. Turning it back on is safe at any time.'}</p>
        <div class="field"><label>Kind of sandbox</label><div class="sb-kinds">
          <label class="sb-kind"><input type="radio" name="sb-kind" data-on-change="sb-kind" value="namespace"${sb.backend !== 'container' ? ' checked' : ''}><span><b>Built-in sandbox</b> <span class="dl-sub">recommended</span><span class="dl-why">Light and fast; each bot gets a fresh, private space. <span class="dl-sub">(namespaces on Linux, Seatbelt on macOS)</span></span></span></label>
          <label class="sb-kind"><input type="radio" name="sb-kind" data-on-change="sb-kind" value="container"${sb.backend === 'container' ? ' checked' : ''}><span><b>Container</b><span class="dl-why">Each bot gets its own small computer that keeps the tools it installs. <span class="dl-sub">(a Docker or Podman container)</span></span></span></label></div></div>
        ${staged ? '<a class="btn sm primary" href="#/config/apply">Review the change</a>' : ''}</section>
      <section class="dl-card"><header class="dl-ch"><h2>Sites agents may reach</h2></header>
        <ul class="dl-list sb-sites">${(sb.egress || []).map((e) => `<li class="acc-row"><span class="mono">${esc(e)}</span><span class="dl-na"><button class="btn sm ghost" data-act="sb-eg-del" data-e="${esc(e)}" aria-label="Remove ${esc(e)}">Remove</button></span></li>`).join('') || '<li class="dl-sub">None: agents can’t reach the network.</li>'}</ul>
        <div class="sb-add"><input class="input mono" id="sb-eg-new" placeholder="api.example.com:443" aria-label="A site agents may reach"><button type="button" class="btn sm secondary" data-act="sb-eg-add">${icon('plus')}Allow this site</button></div>
        <p class="dl-sub">Allowing a new site gives agents more reach, so applying it asks for your passkey.</p></section>
    </div><div class="dl-col-side">
      <section class="dl-card"><header class="dl-ch"><h2>Running outside the sandbox</h2><span class="dl-sub">${outside.length}</span></header>
        ${outside.length ? `<ul class="dl-list">${outside.map((o) => `<li class="acc-row"><div class="dl-nt"><b>${esc(o.what)}</b><span class="dl-why">${esc(o.why)} Set in ${esc(o.who)}.</span></div><span class="dl-na">${o.href ? `<a class="btn sm secondary" href="${o.href}">${o.go}</a>` : `<button class="btn sm secondary" ${o.act}>${o.go}</button>`}</span></li>`).join('')}</ul>` : '<p class="dl-sub">Nothing: every agent runs in the sandbox. A pack can never turn it off.</p>'}</section>
      <section class="dl-card"><header class="dl-ch"><h2>Containers</h2></header>
        ${D.containers.length ? `<ul class="dl-list">${D.containers.map((c) => `<li class="acc-row"><div class="dl-nt"><b>${/^bot:/.test(c.identity) ? UI.nm(c.identity) : esc(c.identity.replace(/^runtime:/, 'runtime '))}</b><span class="dl-why">${c.stopped ? 'stopped; the next run starts it again' : `running for ${UI.humanSpan(c.up)}`} · ${c.size} of files</span></div><span class="dl-na">${c.stopped ? '' : `<button class="btn sm secondary" data-act="ctr-stop" data-i="${c.identity}">Stop</button>`}<button class="icon-btn" data-act="ctr-more" data-i="${c.identity}" aria-label="More for ${esc(c.identity)}">${icon('more')}</button></span></li>`).join('')}</ul>` : '<p class="dl-sub">No containers are running.</p>'}</section>
      <details class="dl-card dl-tech-d acc-tech"><summary>${icon('chev')}Details</summary><dl class="dl-facts"><dt>Identities</dt><dd>${d.sandbox.identities}, each with its own home under <span class="mono">&lt;state&gt;/agent-state</span></dd><dt>Pushing code</dt><dd>fast-forward only, re-signed with your SSH key</dd><dt>The dashboard</dt><dd>its own addresses are always refused by the egress proxy, whatever the list says</dd><dt>Backend</dt><dd class="mono">${d.sandbox.backend} · ${d.sandbox.containers} containers</dd></dl></details>
    </div></div>`; };
  // turning the sandbox off is a widening: say what it means and ask first (r5 major 12: one click turned it off);
  // turning it on needs no confirm
  UI.act('sb-toggle', () => { const cur = (S.staged('sandbox', 'sandbox') || D.sandboxCfg || {}).agents;
    if (cur === 'off') return editSb((c) => (c.agents = 'on'));
    UI.confirm({ title: 'Turn the sandbox off?', body: 'Agents would run as you, with your files and your network: nothing confines what they touch. It’s staged like any change, and applying it asks for your passkey.', ok: 'Stage turning it off', danger: true })
      .then((ok) => { if (ok) editSb((c) => (c.agents = 'off')); }); });
  UI.act('sb-kind', (el) => editSb((c) => (c.backend = el.value)));
  UI.act('ctr-stop', (b) => { const c = D.containers.find((x) => x.identity === b.dataset.i); if (c) c.stopped = true; S.save(); UI.rerender(); UI.toast(`Stopped ${b.dataset.i.replace(/^\w+:/, '')}’s container`, 'its files are kept; the next run starts it again', 'ok'); });
  UI.act('ctr-more', (b) => UI.menu(b, [['Remove the container and its files…', 'trash', 'ctr-archive', `data-i="${b.dataset.i}"`]]));
  const editSb = (fn) => { const c = clone(S.staged('sandbox', 'sandbox')); fn(c); S.stage('sandbox', 'sandbox', c); UI.refreshChrome(); UI.rerender(); };
  UI.act('sb-backend', (b) => editSb((c) => (c.backend = b.dataset.v)));
  UI.act('sb-eg-del', (b) => editSb((c) => (c.egress = (c.egress || []).filter((e) => e !== b.dataset.e))));
  UI.act('sb-eg-add', () => { const v = UI.$('#sb-eg-new').value.trim(); if (!v) { UI.$('#sb-eg-new').focus(); UI.$('#sb-eg-new').classList.add('shake'); return; } editSb((c) => { c.egress = c.egress || []; if (!c.egress.includes(v)) c.egress.push(v); }); });
  document.addEventListener('keydown', (e) => { if (e.target.id === 'sb-eg-new' && e.key === 'Enter') { e.preventDefault(); e.stopPropagation(); UI.acts['sb-eg-add'](); } }, true);
  UI.act('ctr-archive', (b) => UI.confirm({ title: `Remove ${b.dataset.i.replace(/^\w+:/, '')}’s container and its files?`, body: 'The container and everything in its home (the tools and files it kept) are deleted now. The next run starts from a fresh container.', ok: 'Remove container and files', danger: true }).then((ok) => { if (!ok) return; D.containers = D.containers.filter((c) => c.identity !== b.dataset.i); D.daemon.sandbox.containers = D.containers.length; audit('sandbox_archive', b.dataset.i); S.save(); UI.rerender(); }));

  /* ================= logs & audit ================= */
  const LOGS = [['info', 'conductor', 'gh: sweep 14 repos · 0 missed · next in 30s'], ['info', 'paseo', 'provider entries unchanged=2 (claude-sandboxed, codex-sandboxed)'], ['info', 'bots', 'platform-sre: delivered 1 message to home s7 (idle)'], ['warn', 'sandbox', 'egress denied: storage.googleapis.com from agent:fixer@acme/api'], ['info', 'engine', 'r-7f3a ci-fixer step fix: model claude-sonnet-5 (the fixer fleet’s first choice)'], ['info', 'dashboard', 'session ops: passkey assertion ok (laptop) from 127.0.0.1'], ['err', 'vaults', 'hcv: 403 permission denied (token expired)'], ['info', 'update', 'v0.72.0 found · preflight validate on a copy: ok']];
  // both logs filter with the one shared filter bar; an audit entry is one line in words (who did what, when) and its
  // recorded event, actor and detail open on a tap: ids and raw names belong behind that disclosure
  const ld = { q: '', level: [], src: [], since: '7d' }, la = { q: '', area: [], actor: [], since: '7d' };
  const AREA = [['signin', 'Sign-ins', /^dashboard_/], ['bots', 'Bots and tickets', /^(bot_|ticket_|memory_|team_)/], ['browser', 'Browsers', /^browser_/], ['runs', 'Runs', /^(workflow_|verb$)/], ['config', 'Config and updates', /^(config_|plugin_|update_|sandbox_)/], ['safety', 'Blocked by the sandbox', /^egress_/]];
  const areaOf = (e) => (AREA.find(([, , re]) => re.test(e)) || ['other'])[0];
  const actorWords = (w) => { w = String(w || ''); const run = D.runs.find((r) => r.id === w);
    return /^operator:/.test(w) ? (w === 'operator:ops' ? 'You' : w.slice(9)) : /^bot:/.test(w) ? UI.botName(w.slice(4)) : /^agent:/.test(w) ? `The ${w.slice(6).split('@')[0]} agent (${w.split('@')[1] || ''})`.replace(' ()', '') : run ? `A run of ${String(run.workflow).startsWith('bot:') ? UI.botName(run.workflow.slice(4)) : run.workflow}` : w; };
  const EVW = { browser_profile_fill: 'filled in your details', browser_profile_add: 'added personal details', browser_profile_set: 'set a personal detail', browser_profile_unset: 'removed a personal detail', browser_profile_bind: 'changed where personal details are used', browser_profile_remove: 'removed personal details', browser_card_add: 'added a payment card', browser_card_bind: 'changed a payment card', browser_card_remove: 'removed a payment card', browser_card_freeze: 'froze a payment card', browser_card_unfreeze: 'unfroze a payment card', browser_purchase_approve: 'approved a purchase', browser_purchase_deny: 'denied a purchase', browser_purchase_receipt: 'recorded a receipt', memory_remember: 'remembered something', memory_commit: 'saved a memory change', memory_revert: 'reverted a memory change', memory_consolidate: 'tidied its memory', browser_action: 'acted in a browser', browser_fill: 'filled in a sign-in form', browser_logout: 'signed a browser out', dashboard_session: 'signed in to the dashboard', bot_proposal: 'asked for approval', bot_approval: 'decided an approval', bot_deliver: 'gave a bot a ticket', bot_route: 'reassigned a ticket', bot_rotate: 'rotated a bot’s session', bot_sweep: 'started a routine check', bot_delegate: 'handed off part of a ticket', bot_delegate_done: 'reported back on a hand-off', bot_delegate_cancel: 'cancelled a hand-off', bot_worker_stop: 'stopped a worker', verb: 'did something in a connected service', workflow_failed: 'failed', egress_denied: 'was blocked from reaching an address', config_apply: 'applied a config change', plugin_add: 'added a plugin', plugin_remove: 'removed a plugin', plugin_update: 'updated a plugin', update_apply: 'installed an update', update_check: 'checked for updates', update_rollback: 'rolled back an update', sandbox_archive: 'removed a sandbox container', ticket_new: 'created a ticket', ticket_assign: 'assigned a ticket', ticket_comment: 'commented on a ticket', ticket_edit: 'edited a ticket', ticket_ready: 'marked a ticket ready', ticket_decline: 'declined a ticket', bot_case_close: 'marked a ticket done', bot_case_reopen: 'reopened a ticket', team_knowledge_proposed: 'proposed team knowledge', team_knowledge_promoted: 'promoted team knowledge', team_knowledge_denied: 'turned down team knowledge' };
  const auditWords = (a) => `${actorWords(a.by || a.who)} ${a.what || EVW[a.event] || a.event.replace(/_/g, ' ')}`;
  /* An entry leads with who (48): a bot's face, a team's initials, You, or a workflow's icon, and the name in bold; then
     what happened, specifically; then the facts line (what it was about, where, which passkey) */
  const actorOf = (a) => { const w = String(a.by || a.who || ''), run = D.runs.find((r) => r.id === w), wf = /^wf:/.test(w) ? w.slice(3) : run ? run.workflow : '';
    if (/^operator:/.test(w)) return { av: `<span class="avatar au-you" aria-hidden="true">${esc(w.slice(9, 11))}</span>`, name: w === 'operator:ops' ? 'You' : w.slice(9) };
    if (/^bot:/.test(w)) return { av: UI.botAv(w.slice(4), 'sm'), name: UI.botName(w.slice(4)) };
    if (/^team:/.test(w)) return { av: UI.teamAv(w.slice(5), 'sm'), name: `team ${w.slice(5)}` };
    if (wf) return { av: `<span class="sc-ic sm au-wf" aria-hidden="true">${icon(/^bot:/.test(wf) ? 'bots' : 'workflows')}</span>`, name: wf.replace(/^bot:/, '') };
    return { av: `<span class="sc-ic sm au-wf" aria-hidden="true">${icon('dot')}</span>`, name: actorWords(w) }; };
  const auditFacts = (a) => { const d = String(a.detail || ''), area = AREA.find(([, , re]) => re.test(a.event)), fx = [];
    if (area) fx.push({ icon: { signin: 'key', bots: 'bots', browser: 'globe', runs: 'workflows', config: 'settings', safety: 'shield' }[area[0]] || 'dot', value: area[1], label: 'About' });
    const repo = d.match(/\b[\w-]+\/[\w-]+#\d+/); if (repo) fx.push({ icon: 'obj-pr', value: repo[0], label: 'Where' });
    const site = d.match(/\b(?:https?:\/\/)?([a-z0-9-]+\.[a-z0-9.-]+\.[a-z]{2,})(?::\d+)?/i); if (site && !repo) fx.push({ icon: 'globe', value: site[1], label: 'Site' });
    const tk = d.match(/\btask (c\d+)/); const t = tk && (D.cases || []).find((c) => c.id === tk[1]); if (t) fx.push({ icon: 'obj-case', value: t.title, label: 'Ticket', href: `#/tickets/${t.id}` });
    const cred = d.match(/credential (\w+)/); if (cred && /passkey|webauthn/.test(d)) fx.push({ icon: 'key', value: `${cred[1]} passkey`, label: 'Passkey' });
    if (a.by && a.by !== a.who) fx.push({ icon: 'dot', value: `its ${String(a.who).replace(/^agent:/, '').split('@')[0]} agent`, label: 'Which of its agents' });
    return UI.factsLine(fx); };
  UI.auditWords = auditWords;
  const logBar = () => UI.filterBar({ id: 'log', state: ld, placeholder: 'Find in the log', label: 'Find and filter the daemon log', fields: [
    { k: 'level', label: 'Level', multi: true, keepOrder: true, options: () => [['info', 'Info'], ['warn', 'Warnings'], ['err', 'Errors']].map(([v, label]) => ({ v, label, n: LOGS.filter(([k]) => k === v).length })).filter((o) => o.n) },
    { k: 'src', label: 'From', multi: true, options: () => [...new Set(LOGS.map(([, x]) => x))].map((v) => ({ v, label: v, n: LOGS.filter(([, x]) => x === v).length })) }, UI.timeField({ def: '7d', title: 'Show lines from' })], onChange: () => UI.rerender({ keepScroll: true }) });
  const auditBar = () => UI.filterBar({ id: 'aud', state: la, placeholder: 'Find in the audit log', label: 'Find and filter the audit log', fields: [
    { k: 'area', label: 'About', multi: true, keepOrder: true, options: () => AREA.map(([v, label]) => ({ v, label, n: D.audit.filter((a) => areaOf(a.event) === v).length })).filter((o) => o.n) },
    { k: 'actor', label: 'Who', multi: true, options: () => [...new Set(D.audit.map((a) => a.who))].map((v) => ({ v, label: actorWords(v), n: D.audit.filter((a) => a.who === v).length })) }, UI.timeField({ def: '7d', title: 'Show entries from' })], onChange: () => UI.rerender({ keepScroll: true }) });
  UI.route('logs/:tab?', ({ tab }) => {
    tab = tab || 'daemon';
    const lineAt = (i) => new Date(Date.now() - (LOGS.length - i) * 21000).toISOString();
    const lines = LOGS.filter(([k, x, m], i) => UI.withinRange(lineAt(i), ld.since) && (!ld.level.length || ld.level.includes(k)) && (!ld.src.length || ld.src.includes(x)) && (!ld.q || (x + ' ' + m).toLowerCase().includes(ld.q.toLowerCase())));
    const daemon = `${logBar()}<div class="panel"><div class="panel-head">${UI.statusMark('run', 'Following', { sm: true })}<span class="dl-sub">${lines.length} of ${LOGS.length} lines</span></div>
      <div class="log" style="min-height:300px">${lines.map(([k, x, m], i) => `<div class="l ${k === 'err' ? 'err' : k === 'warn' ? 'warn' : ''}"><span class="t">${new Date(Date.now() - (LOGS.length - i) * 21000).toTimeString().slice(0, 8)}</span><span class="src">${x}</span><span class="m">${esc(m)}</span></div>`).join('') || '<div class="l"><span class="m muted">No lines match.</span></div>'}</div>
      <div class="panel-foot small muted">From the daemon's own log ring (journald / launchd log is unchanged). Secret values are redacted at the source.</div></div>`;
    const qp = new URLSearchParams(location.hash.split('?')[1] || ''), runF = qp.get('run') || '';
    const rows = D.audit.filter((a) => UI.withinRange(a.ts, la.since) && (!la.area.length || la.area.includes(areaOf(a.event))) && (!la.actor.length || la.actor.includes(a.who)) && (!runF || a.run === runF || a.who === runF || new RegExp('\\b' + runF + '\\b').test(a.detail || '')) && (!la.q || `${auditWords(a)} ${a.event} ${a.who} ${a.detail}`.toLowerCase().includes(la.q.toLowerCase())));
    const audit2 = `${auditBar()}${runF ? `<p class="dl-sub tk-showing">Only what concerns ${esc(actorWords(runF)).replace(/^A run/, 'one run')} · <a class="link" href="#/logs/audit">Show everything</a></p>` : ''}
      <section class="dl-card au-card"><p class="dl-sub au-n">${rows.length} of ${D.audit.length} entries · append-only; every dashboard action is recorded with who did it, the passkey used and where from</p>
      <ul class="au-list">${rows.map((a) => `<li><details class="au-row"><summary>${(() => { const x = actorOf(a); return `${x.av}<span class="au-main"><span class="au-what"><b>${esc(x.name)}</b> ${esc(a.what || EVW[a.event] || a.event.replace(/_/g, ' '))}</span>${auditFacts(a)}</span>`; })()}<span class="dl-sub au-when">${ago(a.ts)} ago</span></summary>
        <dl class="dl-facts"><dt>Event</dt><dd class="mono">${esc(a.event)}</dd><dt>Who</dt><dd class="mono">${esc(a.who)}</dd><dt>Detail</dt><dd>${esc(a.detail)}</dd><dt>When</dt><dd>${new Date(a.ts).toLocaleString()}</dd></dl></details></li>`).join('') || '<li class="dl-empty-row">No entries match.</li>'}</ul></section>`;
    return `<div class="page dl-page">${UI.systemTabs('logs')}<div class="dl-phead"><div><h1>Logs &amp; audit</h1></div></div>${UI.tabs([['daemon', 'Daemon log', '#/logs/daemon'], ['audit', 'Audit', '#/logs/audit', D.audit.length]], tab)}${tab === 'audit' ? audit2 : daemon}</div>`;
  }, { nav: 'logs' });

  /* ================= phone: more ================= */
  UI.route('more', () => `<div class="page">${UI.head('More')}<div class="more-host">${UI.hostCard()}</div><div class="panel"><div class="list">${[['bots', 'Bots', 'bots'], ['tickets', 'Tickets', 'ticket'], ['workflows', 'Workflows', 'workflows'], ['browser', 'Browsers', 'globe'], ['connectors', 'Plugins', 'connectors'], ['extensions', 'Packs', 'extensions'], ['secrets', 'Secrets', 'secrets'], ['config', 'System', 'settings']].map(([h, l, ic]) => `<a class="item click" href="#/${h}">${icon(ic)}<span class="grow" style="font-weight:600">${l}</span><span class="chev">${icon('chev')}</span></a>`).join('')}
    ${D.pending.length ? `<a class="item click" href="#/config/apply">${icon('edit')}<span class="grow" style="font-weight:600">${D.pending.length} staged change${D.pending.length > 1 ? 's' : ''}</span><span class="chev">${icon('chev')}</span></a>` : ''}
    <button class="item click as-btn" data-act="theme">${icon('moon')}<span class="grow" style="font-weight:600">Toggle theme</span></button>
    <button class="item click as-btn" data-act="reset-demo">${icon('undo')}<span class="grow" style="font-weight:600">Reset demo data</span></button>
    <a class="item click" href="#/signin">${icon('lock')}<span class="grow" style="font-weight:600">Lock (sign out)</span></a></div></div></div>`);
})();
