/* Create in place. Every picker that references another entity offers
 * "+ New …" as its first option. Choosing it opens a drawer over the current
 * flow, which keeps its unsaved state; the new thing is selected on return.
 *
 * One rule, used everywhere: a child created in place is STAGED, never
 * applied on its own. When the parent is reviewed, parent and child are one
 * combined diff. Pickers and validation read the staged view (S.view()), so
 * the parent can reference the child at once. The one exception is a secret
 * VALUE: values are never config, so a new secret is written to its vault
 * straight away, behind a step-up, exactly as on the Secrets page. */
(function () {
  const { icon, esc } = UI;
  const V = () => S.view();
  const clone = (o) => JSON.parse(JSON.stringify(o));
  const NAME = { pattern: /^[a-z][a-z0-9-]*$/, patternMsg: 'Lowercase letters, digits and dashes.' };
  const taken = (list, key = 'name') => (v) => (list().some((x) => x[key] === v) ? 'That name is taken.' : '');
  const note = (what) => `This ${what} is <b>staged</b>: it applies together with what you're editing, in one diff.`;
  const drawer = (spec) => UI.form(Object.assign({ drawer: true, submit: 'Create', submitIcon: 'plus' }, spec));
  const stage = (coll, key, node, done, ret) => { S.stage(coll, key, node); UI.refreshChrome(); done(ret || key); };

  const C = (UI.CREATE = {});
  C.bot = (ctx, done) => drawer({ caps: 'new bot', title: ctx.team ? `New bot on team ${ctx.team}` : 'New bot', intro: 'A bot is solo or on a team. It starts in shadow mode; finish its charter and access in its Definition later.',
    values: { team: ctx.team || '' },
    fields: [Object.assign({ k: 'name', type: 'string', req: true, ph: 'e.g. data-steward', validate: taken(() => V().bots) }, NAME), { k: 'description', type: 'string', mono: false },
      { k: 'team', type: 'enum', label: 'Team', options: V().teams.map((t) => t.name), create: 'team', createLabel: 'New team', help: 'Leave empty for a solo bot. On a team it inherits the defaults and can only narrow them.' },
      { k: 'operators', type: 'list', label: 'Operators', help: 'Leave empty to inherit the team\'s.' }],
    note: note('bot'),
    onSubmit: (v) => { const n = { description: v.description || '', charter_file: `bots/${v.name}.md`, autonomy: { shadow: true, approval_ttl: '24h' }, skill: { verbs: ['gh.get_*'] }, isolation: { mode: 'namespace' } }; if (v.team) n.team = v.team; if (v.operators) n.operators = v.operators; else if (!v.team) n.operators = ['ops']; stage('bots', v.name, n, done); } });
  C.team = (ctx, done) => drawer({ caps: 'new team', title: 'New team', intro: 'Bots on a team inherit its defaults and can only narrow them.',
    fields: [Object.assign({ k: 'name', type: 'string', req: true, validate: taken(() => V().teams) }, NAME), { k: 'description', type: 'string', mono: false },
      { k: 'operators', type: 'select-multi', label: 'Operators / approvers', req: true, options: Object.keys(D.operatorsCfg) },
      { k: 'max_tier', type: 'enum', label: 'Autonomy ceiling', req: true, options: ['propose', 'commit_money', 'reversible'], def: 'propose' },
      { k: 'folder', type: 'enum', label: 'Folder', options: UI.allFolders(), create: 'folder', createLabel: 'New folder' }],
    note: note('team'),
    onSubmit: (v) => { const n = { defaults: { operators: v.operators, autonomy: { max_tier: v.max_tier, promote: false }, isolation: { mode: 'namespace' } } }; if (v.description) n.description = v.description; if (v.folder) n.folder = v.folder; stage('teams', v.name, n, done); } });
  C.folder = (ctx, done) => drawer({ caps: 'new folder · organization only', title: 'New folder', values: { parent: ctx.parent || '' },
    fields: [{ k: 'parent', type: 'enum', label: 'Inside', options: [['', 'top level']].concat(UI.allFolders().map((f) => [f, f])) }, { k: 'name', type: 'string', req: true, pattern: /^[\w][\w .-]*$/, patternMsg: 'Letters, digits, space, . _ -; no slashes.' }],
    submit: 'Create folder',
    onSubmit: (v) => { const path = (v.parent ? v.parent + '/' : '') + v.name.trim(); if (UI.allFolders().includes(path)) return `${path} exists already.`; D.folderDrafts = (D.folderDrafts || []).concat([path]); S.save(); done(path); } });
  C.connector = (ctx, done) => {
    const uses = Object.keys(SCHEMA.connectors).filter((u) => u !== 'git' && (!ctx.uses || ctx.uses.includes(u)));
    const fields = [Object.assign({ k: 'name', type: 'string', req: true, label: 'Instance name', validate: taken(() => V().connectors) }, NAME), { k: 'use', type: 'enum', req: true, label: 'Plugin', options: uses.map((u) => [u, `${SCHEMA.connectors[u].label} (${u})`]), def: ctx.use || uses[0] }];
    uses.forEach((u) => SCHEMA.connectors[u].settings.forEach((f) => fields.push(Object.assign({}, f, { k: `${u}__${f.k}`, label: f.k, show: (vals) => (vals.use || ctx.use || uses[0]) === u }))));
    drawer({ caps: 'new connector instance', title: ctx.use ? `New ${SCHEMA.connectors[ctx.use].label} connector` : 'New connector', intro: 'Settings come from the plugin\'s declaration. Secrets are references, never values.', values: { use: ctx.use || uses[0] }, fields, note: note('connector'), wide: true,
      onSubmit: (v) => { const u = v.use || ctx.use || uses[0], n = { use: u }; SCHEMA.connectors[u].settings.forEach((f) => { const x = v[`${u}__${f.k}`]; if (x !== undefined) S.setPath(n, f.k, x); }); stage('connectors', v.name, n, done); } });
  };
  C.runtime = (ctx, done) => drawer({ caps: 'new runtime', title: 'New runtime',
    fields: [Object.assign({ k: 'name', type: 'string', req: true, validate: taken(() => V().runtimes) }, NAME), { k: 'use', type: 'enum', req: true, options: Object.keys(SCHEMA.runtimes), def: 'cli' }, { k: 'host', type: 'host' }, { k: 'isolation', type: 'enum', options: ['namespace', 'container', 'none'], def: 'namespace', help: 'none runs agents as you: a widening.' }],
    note: note('runtime'),
    onSubmit: (v) => { const n = { use: v.use }; if (v.host) n.host = v.host; if (v.isolation && v.isolation !== 'namespace') n.isolation = { mode: v.isolation }; stage('runtimes', v.name, n, done); } });
  C.fleet = (ctx, done) => drawer({ caps: 'new model fleet · models:', title: 'New fleet', intro: 'A ranked list of acceptable models; a step names the fleet, not a model.',
    fields: [{ k: 'name', type: 'string', req: true, pattern: /^[a-z][\w-]*$/, patternMsg: 'letters, digits, - and _', validate: taken(() => V().fleets || []) }, { k: 'any', type: 'list', req: true, label: 'Acceptable models, best first', ph: 'claude-opus-*' }, { k: 'required', type: 'bool', on: 'refuse to run rather than fall back to a bare launch' }],
    note: note('fleet'),
    onSubmit: (v) => { const n = { any: v.any }; if (v.required) n.required = true; stage('models', v.name, n, done); } });
  C.vault = (ctx, done) => drawer({ caps: 'new vault', title: 'New vault',
    fields: [Object.assign({ k: 'name', type: 'string', req: true, validate: taken(() => V().vaults) }, NAME), { k: 'type', type: 'enum', req: true, options: ['conductor', 'pass', 'onepassword', 'hashicorp', 'file'], def: 'conductor' }, { k: 'unlock', type: 'string', label: 'Unlock / token reference', ph: 'creds:house or env:VAULT_TOKEN' }],
    note: note('vault'),
    onSubmit: (v) => { const n = { type: v.type }; if (v.unlock) n[v.type === 'conductor' ? 'unlock' : 'token'] = v.unlock; stage('vaults', v.name, n, done); } });
  C.secret = (ctx, done) => {
    const writable = V().vaults.filter((x) => x.writable !== false && x.status !== 'fail' && x.type !== 'onepassword');
    drawer({ caps: 'new secret · write-only', title: 'New secret', intro: 'A secret\'s value is never config: it goes straight to the vault (with your passkey) and only its reference is used here.',
      values: { vault: ctx.vault && writable.some((x) => x.name === ctx.vault) ? ctx.vault : (writable[0] || {}).name },
      fields: [{ k: 'name', type: 'string', req: true, ph: 'e.g. pagerduty-ui', pattern: /^[\w./-]+$/, patternMsg: 'letters, digits, . _ / -' }, { k: 'vault', type: 'enum', req: true, options: writable.map((x) => x.name), create: 'vault', createLabel: 'New vault' }, { k: 'value', type: 'text', req: true, label: 'Value · write-only' }],
      submit: 'Store with passkey', submitIcon: 'passkey',
      onSubmit: (v, form) => {
        const len = v.value.length; form.querySelector('textarea').value = '';
        UI.stepup({ title: `Store ${esc(v.name)}`, purpose: 'secret write', what: `{ purpose: <b>secret_write</b>, vault: <b>${esc(v.vault)}</b>, key: <b>${esc(v.name)}</b>, length: <b>${len}</b> }`, ok: 'Write with passkey' }).then((ok) => {
          if (!ok) return;
          const ref = `${v.vault}/${v.name}`;
          D.secrets.unshift({ name: v.name, ref, vault: v.vault, set: new Date().toISOString(), used: [] });
          D.audit.unshift({ ts: new Date().toISOString(), event: 'secret_write', who: 'operator:ops', detail: `${ref} · length ${len} · value not recorded`, run: '' });
          S.save(); form.closest('.drawer-scrim').remove();
          done({ name: v.name, ref, expr: `{{ vault "${v.vault}" "${v.name}" }}`, key: v.name, vault: v.vault });
        });
        return false;
      } });
  };
  C.credential = (ctx, done) => drawer({ caps: 'new credential binding · browser.credentials', title: 'New credential binding', values: { identity: ctx.identity || '' }, wide: true,
    fields: [{ k: 'name', type: 'string', req: true, pattern: /^[a-z][\w-]*$/, patternMsg: 'letters, digits, - and _', validate: taken(() => V().access.credentials) }, { k: 'origin', type: 'string', req: true, ph: 'https://grafana.example.com', pattern: /^https:\/\/[\w.-]+(:\d+)?$/, patternMsg: 'An https:// origin, no path.' },
      { k: 'identity', type: 'enum', req: true, options: identities(), create: 'identity', createLabel: 'New browser identity' }, { k: 'fields', type: 'select-multi', req: true, options: ['username', 'password', 'otp', 'push', 'passkey'] },
      { k: 'ref', type: 'enum', req: true, label: 'Secret (vault reference)', options: secretRefs(), create: 'secret', createLabel: 'New secret', ret: 'ref' }],
    note: note('binding'),
    onSubmit: (v) => { const vault = v.ref.startsWith('op://') ? 'op' : v.ref.split('/')[0]; stage('credentials', v.name, { origin: v.origin, identity: v.identity, fields: v.fields, vault, ref: v.ref.replace(/^op:\/\//, '').replace(/^[^/]+\//, '') }, done); } });
  C.host = (ctx, done) => drawer({ caps: 'new host', title: 'New host', fields: [Object.assign({ k: 'name', type: 'string', req: true, validate: taken(() => V().hosts) }, NAME), { k: 'addr', type: 'string', req: true, ph: 'box.tail0000.ts.net' }, { k: 'user', type: 'string', req: true, def: 'conductor' }], note: note('host'),
    onSubmit: (v) => stage('hosts', v.name, { addr: v.addr, user: v.user }, done) });
  C.workflow = (ctx, done) => drawer({ caps: 'new reusable workflow · workflows:', title: 'New workflow', intro: 'Starts with its inputs and no steps; open it in the builder to add them.',
    fields: [Object.assign({ k: 'name', type: 'string', req: true, validate: taken(() => V().workflows) }, NAME), { k: 'inputs', type: 'map', label: 'Inputs (name → type)', help: 'e.g. repo → string' }], note: note('workflow'),
    onSubmit: (v) => { const ins = Object.entries(v.inputs || { repo: 'string' }).map(([k, t]) => `  ${k}: { type: ${t || 'string'} }`).join('\n'); stage('workflows', v.name, `name: ${v.name}\ninputs:\n${ins}\nsteps:\n  - log: "todo"\n`, done); } });
  C.identity = (ctx, done) => drawer({ caps: 'new browser identity', title: 'New browser identity', fields: [{ k: 'id', type: 'string', req: true, ph: 'agent:docs@acme/api or bot:<name>', pattern: /^(bot:|agent:)[\w@./-]+$/, patternMsg: 'bot:<name> or agent:<name>@<repo>', validate: taken(() => V().identities, 'id') }, { k: 'allow', type: 'list', label: 'Allowlist', ph: 'docs.example.com' }], note: note('identity'),
    onSubmit: (v) => stage('identities', v.id, { allow: v.allow || [], logins: [], evaluate: 'off' }, done) });
  C.site = (ctx, done) => drawer({ caps: `browser.identities["${esc(ctx.identity)}"]`, title: 'Add a logged-in site', fields: [{ k: 'site', type: 'string', req: true, ph: 'console.example.com', pattern: /^[\w.-]+$/, patternMsg: 'A host name.' }], note: 'Adds the site to the identity\'s allowlist and logins (a widening), staged.',
    onSubmit: (v) => { const cur = clone(S.staged('identities', ctx.identity) || { allow: [], logins: [], evaluate: 'off' }); cur.allow = [...new Set((cur.allow || []).concat([v.site]))]; cur.logins = [...new Set((cur.logins || []).concat([v.site]))]; stage('identities', ctx.identity, cur, done, v.site); } });
  function identities() { return V().bots.map((b) => 'bot:' + b.name).concat(V().identities.map((i) => i.id).filter((i) => !i.startsWith('bot:'))); }
  function secretRefs() { return V().secrets.map((s) => [s.ref, `${s.name} · ${s.ref}`]); }
  UI.secretRefs = secretRefs; UI.identityList = identities;

  /* the one listener: a "+ New …" option opens its drawer, then selects the result */
  document.addEventListener('focusin', (e) => { if (e.target.matches && e.target.matches('select[data-picker]')) e.target.dataset.prev = e.target.value === '__new__' ? '' : e.target.value; }, true);
  // a select set to "+ New …" isn't a value: keep its input event from reaching the parent form
  document.addEventListener('input', (e) => { if (e.target.matches && e.target.matches('select[data-picker]') && e.target.value === '__new__') e.stopImmediatePropagation(); }, true);
  document.addEventListener('change', (e) => {
    const sel0 = e.target; let sel = sel0;
    if (!sel.matches || !sel.matches('select[data-picker]') || sel.value !== '__new__') return;
    e.stopImmediatePropagation(); e.stopPropagation();
    const kind = sel.dataset.picker, ctx = JSON.parse(sel.dataset.ctx || '{}');
    sel.value = sel.dataset.prev || (sel.classList.contains('list-add-sel') ? '' : sel.options[1] ? sel.options[1].value : '');
    if (ctx.page) { UI.go(ctx.page); return; }
    const fn = C[kind]; if (!fn) return;
    fn(ctx, (res) => {
      if (!document.body.contains(sel)) sel = (sel0.id && document.getElementById(sel0.id)) || null; // the parent re-rendered
      if (!sel) return;
      const val = typeof res === 'object' ? res[sel.dataset.ret] || res.name : res;
      const label = typeof res === 'object' ? `${res.name} · ${res.ref}` : res;
      if (![...sel.options].some((o) => o.value === val)) { const o = document.createElement('option'); o.value = val; o.textContent = `${label} (new · staged)`; sel.insertBefore(o, sel.options[sel.classList.contains('list-add-sel') ? 2 : 1] || null); }
      sel.value = val; sel.dataset.prev = val;
      sel.dispatchEvent(new Event('change', { bubbles: true }));
      sel.dispatchEvent(new Event('input', { bubbles: true }));
      UI.toast(`Created ${esc(typeof res === 'object' ? res.name : res)}`, typeof res === 'object' && res.ref ? 'written to the vault · selected' : 'staged · selected · applies with this change', 'ok');
    });
  }, true);
  /* button-style create affordances (in list pickers such as the step picker) */
  UI.act('create-inline', (b) => { const fn = C[b.dataset.create]; const target = b.dataset.target; if (fn) fn(JSON.parse(b.dataset.ctx || '{}'), (res) => { const t = target && document.getElementById(target); if (t) { t.value = typeof res === 'object' ? res.name : res; t.dispatchEvent(new Event('input', { bubbles: true })); t.dispatchEvent(new Event('change', { bubbles: true })); UI.toast(`Created ${esc(t.value)}`, 'staged · selected · applies with this change', 'ok'); } else if (UI.onCreated) UI.onCreated(b.dataset.create, res); }); });
})();
