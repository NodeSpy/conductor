/* The prototype's fake daemon state, persisted in localStorage so a session
 * survives navigation and reloads ("Reset demo data" puts the seed back), and
 * the config-edit pipeline (design §7) over it:
 *
 *   an editor stages a node change   S.stage('connectors', 'gh', node)
 *   the apply page diffs config.yaml  S.configText(before) vs S.configText(after)
 *   validates the candidate           S.validate()
 *   applies with a backup             S.apply()  → D.configHistory
 *   and restores any backup           S.restore(id) → the same pipeline
 *
 * Every collection here is a top-level config section; runtime status lives
 * beside the node (item.status, item.detail) and is never written. */
(function () {
  const S = (window.S = {});
  const KEY = 'cd-proto-state-v4';
  const Y = window.YAML;
  const clone = Y.clone, eq = Y.eq;
  const SEED = clone(window.D);

  /* ---------- persistence ---------- */
  S.save = () => { S._ver = (S._ver || 0) + 1; try { localStorage.setItem(KEY, JSON.stringify(window.D)); } catch (e) {} };
  /** The state as it will be once staged changes apply: what pickers offer and validation resolves against. */
  S.view = () => { const k = (S._ver || 0) + ':' + window.D.pending.length; if (S._vk !== k || !S._v) { S._v = window.D.pending.length ? S.candidate() : window.D; S._vk = k; } return S._v; };
  S.load = () => {
    try {
      const raw = localStorage.getItem(KEY);
      if (!raw) return false;
      const saved = JSON.parse(raw);
      Object.keys(saved).forEach((k) => (window.D[k] = saved[k]));
      return true;
    } catch (e) { return false; }
  };
  S.reset = () => {
    try { localStorage.removeItem(KEY); } catch (e) {}
    const fresh = clone(SEED);
    Object.keys(window.D).forEach((k) => delete window.D[k]);
    Object.assign(window.D, fresh);
    seedHistory();
  };
  S.dirty = () => { try { return !!localStorage.getItem(KEY); } catch (e) { return false; } };

  /* ---------- helpers ---------- */
  const D = () => window.D;
  const byName = (list, k, key = 'name') => list.find((x) => x[key] === k);
  const upsert = (list, k, item, key = 'name') => { const i = list.findIndex((x) => x[key] === k); if (i >= 0) list[i] = Object.assign(list[i], item); else list.push(item); };
  const drop = (list, k, key = 'name') => { const i = list.findIndex((x) => x[key] === k); if (i >= 0) list.splice(i, 1); };
  S.getPath = (o, p) => p.split('.').reduce((a, k) => (a == null ? undefined : a[k]), o);
  S.setPath = (o, p, v) => {
    const ks = p.split('.'); let a = o;
    ks.slice(0, -1).forEach((k) => { if (a[k] == null || typeof a[k] !== 'object') a[k] = {}; a = a[k]; });
    const last = ks[ks.length - 1];
    if (v === undefined || v === '' || (Array.isArray(v) && !v.length && !S.keepEmpty)) delete a[last]; else a[last] = v;
    // prune empty parents
    for (let n = ks.length - 1; n > 0; n--) { const par = S.getPath(o, ks.slice(0, n - 1).join('.')) || o; const ch = par[ks[n - 1]]; if (ch && typeof ch === 'object' && !Array.isArray(ch) && !Object.keys(ch).length) delete par[ks[n - 1]]; else break; }
    return o;
  };
  const vref = (s) => { const m = String(s || '').match(/vault\s+"([^"]+)"\s+"([^"]+)"/); return m ? { vault: m[1], key: m[2] } : String(s || '').startsWith('env:') ? { vault: 'env', key: s.slice(4) } : null; };
  S.vref = vref;

  /* Workflow YAML → a derived graph for the live view: series-parallel. */
  S.deriveGraph = (yaml) => {
    let v; try { v = Y.parse(yaml).value; } catch (e) { return []; }
    const out = []; let prev = [];
    const kind = (s) => (s.uses ? 'uses' : s.decide ? 'decide' : s.type === 'agent' || s.prompt ? 'agent' : s.code || s.command || s.use || s.run ? 'code' : 'uses');
    const add = (s, needs) => { const id = s.id || (s.log ? 'log' : s.sleep ? 'sleep' : 'step') + out.length; out.push({ id, kind: kind(s), uses: s.uses, model: s.model, runtime: s.runtime, run: s.use || s.run, needs }); return id; };
    ((v && v.steps) || []).forEach((s) => {
      if (Array.isArray(s.parallel)) { const ends = []; s.parallel.forEach((br) => { let p = prev; (br || []).forEach((x) => { p = [add(x, p)]; }); ends.push(...p); }); prev = ends; }
      else prev = [add(s, prev)];
    });
    return out;
  };
  const wfFromYaml = (name, yaml, prev) => {
    let v = {}; try { v = Y.parse(yaml).value || {}; } catch (e) {}
    const on = Array.isArray(v.on) ? v.on.map((x) => (typeof x === 'string' ? x : Object.keys(x)[0])).join(' · ') : v.on || (v.inputs ? 'call (reusable)' : '');
    return Object.assign(prev || { runs24: 0, ok: 1, cost24: 0, source: 'config' }, { name, yaml, on, kind: v.on ? 'trigger' : 'workflow', steps: S.deriveGraph(yaml) });
  };

  /* ---------- the config sections ---------- */
  const C = (S.cfg = {});
  const listColl = (sec, list, opts = {}) => (C[sec] = Object.assign({
    section: sec, file: 'config.yaml',
    keys: () => list().map((x) => x.name),
    get: (k) => { const it = byName(list(), k); return it ? clone(it.cfg) : null; },
    set: (k, node) => { if (node == null) drop(list(), k); else { const it = byName(list(), k); if (it) it.cfg = node; else list().push(Object.assign({ name: k, status: 'idle', detail: 'added from the dashboard · not yet reported' }, opts.fresh ? opts.fresh(k, node) : {}, { cfg: node })); } },
    path: (k) => `${sec}.${k}`,
  }, opts));

  listColl('connectors', () => D().connectors, {
    label: 'connector', href: () => '#/connectors/connectors',
    fresh: (k, n) => ({ use: n.use, events: 0, verbs: 0, detail: `${n.use} · registered on reload` }),
    set(k, node) { const L = D().connectors; if (node == null) return drop(L, k); const it = byName(L, k); if (it) { it.cfg = node; it.use = node.use; } else L.push({ name: k, use: node.use, status: 'idle', detail: `${node.use} · registered on reload`, events: 0, verbs: 0, cfg: node }); },
    validate(k, n, all) {
      const e = [];
      if (!/^[a-z][a-z0-9-]*$/.test(k)) e.push(`connector name "${k}": lowercase letters, digits and dashes`);
      const decl = window.SCHEMA.connectors[n.use];
      if (!decl) e.push(`connectors.${k}.use: "${n.use}" is neither a builtin nor in the official registry`);
      else decl.settings.filter((s) => s.req).forEach((s) => { if (S.getPath(n, s.k) == null || S.getPath(n, s.k) === '') e.push(`connectors.${k}.${s.k} is required by the ${n.use} plugin`); });
      Object.entries(n).forEach(([fk, fv]) => { const r = vref(fv); if (r && r.vault !== 'env') { const v = byName(all.vaults, r.vault); if (!v) e.push(`connectors.${k}.${fk}: vault "${r.vault}" is not defined`); else if (v.status === 'fail') e.push(`connectors.${k}.${fk}: vault "${r.vault}" is unreachable (its unlock token expired)`); } });
      return e;
    },
    warn(k, n) { const d = window.SCHEMA.connectors[n.use]; return d && d.registry && !D().plugins.find((p) => p.name === n.use) ? [`plugin ${n.use} is not installed: conductor init fetches ${d.registry}@${d.ver} on apply`] : []; },
  });
  listColl('runtimes', () => D().runtimes, {
    label: 'runtime', href: () => '#/connectors/runtimes',
    set(k, node) { const L = D().runtimes; if (node == null) return drop(L, k); const iso = (node.isolation && node.isolation.mode) || 'namespace'; const it = byName(L, k); const patch = { use: node.use, cfg: node, isolation: node.host ? `${iso} · host ${node.host}` : iso }; if (it) Object.assign(it, patch); else L.push(Object.assign({ name: k, status: 'idle', detail: 'added from the dashboard' }, patch)); },
    validate(k, n, all) {
      const e = [];
      if (!window.SCHEMA.runtimes[n.use]) e.push(`runtimes.${k}.use: unknown runtime "${n.use}"`);
      if (n.host && !byName(all.hosts, n.host)) e.push(`runtimes.${k}.host: no hosts: entry named "${n.host}"`);
      if (n.default) { const others = all.runtimes.filter((r) => r.name !== k && r.cfg && r.cfg.default); if (others.length) e.push(`runtimes: only one runtime can be default: true (also ${others.map((r) => r.name).join(', ')})`); }
      return e;
    },
    widens: (b, a) => ((a && a.isolation && a.isolation.mode === 'none') && !(b && b.isolation && b.isolation.mode === 'none') ? ['runs agents unsandboxed (isolation: none)'] : []),
  });
  /* env: environment variables by scope; daemon scope is conductor.env, not config.yaml */
  const envKey = (e) => e.scope + ':' + e.name;
  C.env = { section: 'env', file: 'conductor.env', label: 'env var', href: () => '#/secrets/env',
    keys: () => (D().env || []).map(envKey),
    get: (k) => { const e = (D().env || []).find((x) => envKey(x) === k); return e ? clone(e) : null; },
    set: (k, n) => { const L = D().env = D().env || []; const i = L.findIndex((x) => envKey(x) === k); if (n == null) { if (i >= 0) L.splice(i, 1); return; } if (i >= 0) L[i] = Object.assign({}, n); else L.push(Object.assign({}, n)); },
    path: (k) => (k.startsWith('daemon:') ? `conductor.env:${k.slice(7)}` : `${k.split(':')[0]}s.${k.split(':')[1]}.env.${k.split(':').slice(2).join(':')}`),
    validate(k, n) { const e = []; if (!/^[A-Z_][A-Z0-9_]*$/.test(n.name || '')) e.push(`env ${n.name}: names are UPPER_SNAKE_CASE`); if (n.value == null || n.value === '') { if (!n.len) e.push(`env ${n.name}: no value`); } return e; } };
  /** conductor.env as the daemon would write it (with a backup); values that look secret are never rendered. */
  S.envText = (state) => ((state || window.D).env || []).filter((e) => e.scope === 'daemon').map((e) => `${e.name}=${e.looksSecret && e.plain ? '•'.repeat(Math.min(e.len || 8, 12)) + '  # plain text in this file' : e.value}`).join('\n') + '\n';
  /* collections: custom content kinds, declared under teams.<t>.collections or bots.<b>.collections
     (conductor's CollectionSpec: dir, fields {type, required, values}, template, free_paths, writers, from) */
  S.RESERVED_DIRS = ['knowledge', 'runbooks', 'watch', 'log', 'handoff', 'episodes'];
  S.FIELD_TYPES = ['string', 'text', 'list', 'steps', 'enum', 'number', 'bool'];
  S.collErrors = (P, cols, owner) => { const e = [], dirs = {};
    Object.entries(cols || {}).forEach(([name, c]) => { const Q = `${P}.collections.${name}`; c = c || {};
      if (!/^[a-z][a-z0-9_-]{0,39}$/.test(name)) e.push(`${Q}: names are lowercase letters, digits, _ and -, up to 40`);
      if (c.from) { if (['dir', 'fields', 'template', 'free_paths', 'writers'].some((x) => c[x] != null)) e.push(`${Q}: from: names a pack's definition, so it can't also set dir, fields, template, free_paths or writers`); return; }
      const dir = String(c.dir || name).replace(/\/$/, '');
      if (!/^[a-z][a-z0-9_-]*(\/[a-z0-9_-]+)*$/.test(dir)) e.push(`${Q}.dir: a relative folder such as postmortems or notes/vendors`);
      else if (S.RESERVED_DIRS.includes(dir.split('/')[0])) e.push(`${Q}.dir: ${dir.split('/')[0]}/ belongs to a built-in kind; pick another folder`);
      else if (dirs[dir]) e.push(`${Q}.dir: ${dir}/ is already ${dirs[dir]}'s folder`); else dirs[dir] = name;
      Object.entries(c.fields || {}).forEach(([f, d]) => { d = d || {}; if (!/^[a-z][a-z0-9_]*$/.test(f)) e.push(`${Q}.fields.${f}: field names are lowercase letters, digits and _`); if (!S.FIELD_TYPES.includes(d.type || 'string')) e.push(`${Q}.fields.${f}.type: one of ${S.FIELD_TYPES.join(', ')}, not "${d.type}"`); if (d.type === 'enum' && !(d.values || []).length) e.push(`${Q}.fields.${f}.values: an enum needs its values`); if (d.values && d.type !== 'enum') e.push(`${Q}.fields.${f}.values: only an enum has values`); });
      const W = owner === 'team' ? ['operator', 'proposal'] : ['operator', 'bots']; (c.writers || []).forEach((w) => { if (!W.includes(w)) e.push(`${Q}.writers: "${w}" — a ${owner}'s writers are ${W.join(' or ')}`); }); });
    return e; };
  listColl('hosts', () => D().hosts, {
    label: 'host', href: () => '#/connectors/hosts',
    set(k, node) { const L = D().hosts; if (node == null) return drop(L, k); const it = byName(L, k); if (it) Object.assign(it, { cfg: node, addr: node.addr, user: node.user }); else L.push({ name: k, addr: node.addr, user: node.user, status: 'wait', rtt: '—', cfg: node }); },
    validate(k, n, all) { const e = []; if (!n.addr) e.push(`hosts.${k}.addr is required`); if (!n.user) e.push(`hosts.${k}.user is required`); return e; },
    refs(k, all) { return all.runtimes.filter((r) => r.cfg && r.cfg.host === k).map((r) => `runtimes.${r.name}.host`); },
  });
  listColl('vaults', () => D().vaults, {
    label: 'vault', href: () => '#/secrets',
    set(k, node) { const L = D().vaults; if (node == null) return drop(L, k); const it = byName(L, k); if (it) { it.cfg = node; it.type = node.type; const tok = S.getPath(node, 'unlock.token'); if (it.name === 'hcv' && tok && tok !== 'env:VAULT_TOKEN') { it.status = 'ok'; it.detail = 'KV v2 · token from ' + tok; } } else L.push({ name: k, type: node.type, status: 'ok', detail: `${node.type} · added from the dashboard`, writable: node.type !== 'onepassword', count: 0, cfg: node }); },
    validate(k, n) { const e = []; if (!/^[a-z][a-z0-9-]*$/.test(k)) e.push(`vault name "${k}": lowercase letters, digits and dashes`); const KEYS = { conductor: ['type', 'path', 'unlock'], onepassword: ['type', 'account', 'service_account'], pass: ['type', 'prefix'], file: ['type', 'dir'], hashicorp: ['type', 'addr', 'mount', 'namespace', 'unlock'] }[n.type]; if (!KEYS) e.push(`vaults.${k}.type: conductor, onepassword, pass, file or hashicorp`); else Object.keys(n).forEach((x) => { if (!KEYS.includes(x)) e.push(`vaults.${k}.${x}: not a ${n.type} vault key (${KEYS.slice(1).join(', ')})`); }); if (n.type === 'file' && !n.dir) e.push(`vaults.${k}.dir: required for a file vault`); if (n.type === 'hashicorp' && !n.addr) e.push(`vaults.${k}.addr: required`); return e; },
    refs(k, all) { return all.secrets.filter((s) => s.vault === k).map((s) => s.used[0] || s.name).filter(Boolean); },
  });
  C.settings = { section: 'settings', file: 'config.yaml', label: 'setting', href: () => '#/secrets',
    keys: () => D().settings.map((s) => s.name), get: (k) => { const s = byName(D().settings, k); return s ? s.value : null; },
    set: (k, v) => { if (v == null) drop(D().settings, k); else { const s = byName(D().settings, k); if (s) s.value = v, (s.chains = String(v).includes('${settings.')), (s.env = String(v).includes('${env.')); else D().settings.push({ name: k, value: v, used: 0, chains: String(v).includes('${settings.'), env: String(v).includes('${env.') }); } },
    path: (k) => `settings.${k}`,
    validate(k, v, all) { const e = []; if (!/^[a-z_][a-z0-9_]*$/.test(k)) e.push(`settings.${k}: names are lowercase snake_case`); const m = String(v).match(/\$\{settings\.(\w+)\}/g) || []; m.forEach((x) => { const n = x.slice(11, -1); if (!byName(all.settings, n)) e.push(`settings.${k}: \${settings.${n}} does not exist`); if (n === k) e.push(`settings.${k} refers to itself`); }); return e; },
    refs(k, all) { const s = byName(all.settings, k); return s && s.used ? [`${s.used} reference${s.used > 1 ? 's' : ''} in config`] : []; },
  };
  /* secrets: an entry is <vault>/<key>; "repointing" one rewrites every place that uses it to another
   * entry, in the one syntax: {{ vault "<vault>" "<key>" }} (browser credentials: { vault, key }). */
  const splitRef = (ref) => { const i = String(ref).indexOf('/'); return [ref.slice(0, i), ref.slice(i + 1)]; };
  S.vaultExpr = (ref) => { const [v, k] = splitRef(ref); return `{{ vault "${v}" "${k}" }}`; };
  C.secrets = { section: 'secrets', file: 'config.yaml', label: 'secret reference', href: () => '#/secrets',
    keys: () => D().secrets.map((s) => s.ref), get: (k) => { const s = byName(D().secrets, k); return s ? s.ref : null; },
    set: (k, ref) => {
      if (ref == null) return drop(D().secrets, k);
      const [v, key] = splitRef(ref);
      const s = byName(D().secrets, k);
      if (!s) { D().secrets.push({ name: ref, ref, vault: v, key, alias: '', set: null, used: [] }); return; }
      Object.assign(s, { ref, vault: v, key, name: ref });
      (s.used || []).forEach((u) => {
        const [sec, name, ...rest] = u.split('.');
        if (sec === 'browser') { const c = byName(D().access.credentials, name); if (c && c.cfg) c.cfg[rest[0]] = { vault: v, key }; return; }
        const coll = { connectors: D().connectors, packs: D().packs }[sec];
        const it = coll && byName(coll, name); if (!it || !it.cfg) return;
        S.setPath(it.cfg, rest.join('.'), S.vaultExpr(ref));
      });
    },
    path: (k) => { const s = byName(D().secrets, k); return (s && s.used[0]) || `vaults.${k.split('/')[0]}`; },
    validate(k, ref, all) { if (!/^[a-z][\w-]*\/.+/.test(ref || '')) return [`${k}: "${ref}" is not <vault>/<key>`]; const [v] = splitRef(ref); const vv = byName(all.vaults, v); return !vv ? [`${k}: no vault "${v}" (a reference to an unknown vault fails conductor validate)`] : vv.status === 'fail' ? [`${k}: vault ${v} is unreachable, so the reference can't be checked`] : []; },
    refs(k, all) { const s = byName(all.secrets, k); return s ? s.used : []; },
  };
  C.bots = { section: 'bots', file: 'config.yaml', label: 'bot', href: (k) => `#/bots/${k}/settings`,
    keys: () => D().bots.map((m) => m.name), get: (k) => { const m = byName(D().bots, k); return m ? clone(m.cfg) : null; },
    set(k, n) {
      const L = D().bots; if (n == null) return drop(L, k);
      let m = byName(L, k);
      if (!m) { m = { name: k, color: ['#ff6a2b', '#4fb8e8', '#3fbf7f', '#e6b33c', '#a59fc4'][L.length % 5], home: 'idle', session: 's1', turns: 0, open: 0, pending: 0, workers: 0, spend: 0 }; L.push(m); D().chats[k] = D().chats[k] || []; }
      Object.assign(m, { cfg: n, team: n.team || null, desc: n.description || '', shadow: !!(n.autonomy && n.autonomy.shadow), mode: (n.isolation && n.isolation.mode) || 'namespace', operators: n.operators || [], peers: n.peers || [], maxTurns: S.getPath(n, 'home.rotate.max_turns') || 100, maxAge: S.getPath(n, 'home.rotate.max_age') || '24h', age: m.age || '0m', sandboxed: true,
        proactive: n.proactive ? { every: n.proactive.every, next: (m.proactive && m.proactive.next) || n.proactive.every, quiet: n.proactive.quiet_hours, budget: `0/${S.getPath(n, 'proactive.budget.max_runs_per_day') || '∞'} runs` } : null });
    },
    path: (k) => `bots.${k}`,
    validate(k, n, all) {
      const e = [];
      if (!/^[a-z][a-z0-9-]*$/.test(k)) e.push(`bot name "${k}": lowercase letters, digits and dashes`);
      e.push(...S.collErrors(`bots.${k}`, n.collections, 'bot'));
      if (n.folder != null && !S.FOLDER_RE.test(n.folder)) e.push(`bots.${k}.folder: "${n.folder}" — segments of letters, digits, space, . _ -, joined by /`);
      if (!Object.keys(all.operatorsCfg || {}).length) e.push(`bots.${k}.operators: no operators: are configured, so nobody could approve its proposals`); // empty = the team's, or every operator
      (n.operators || []).forEach((o) => { if (!all.operatorsCfg[o]) e.push(`bots.${k}.operators: "${o}" is not in operators:`); });
      (n.peers || []).forEach((p) => { if (p === k) e.push(`bots.${k}.peers: a bot can't peer with itself`); else if (!byName(all.bots, p)) e.push(`bots.${k}.peers: no bot named "${p}"`); });
      const tiers = S.getPath(n, 'autonomy.tiers') || {}; const seen = {};
      Object.entries(tiers).forEach(([t, vs]) => (vs || []).forEach((v) => { if (seen[v]) e.push(`bots.${k}.autonomy.tiers: ${v} is in both ${seen[v]} and ${t}`); seen[v] = t; }));
      if (window.RS) Object.keys(n).forEach((x) => { if (!RS.bot.has(x)) e.push(`bots.${k}.${x}: not a bot key (field ${x} not found in type config.BotConfig)`); });
      (S.getPath(n, 'autonomy.promote') || []).forEach((v) => { const tr = S.tierFor({ cfg: n }, v, all).tier; if (!['observe', 'reversible'].includes(tr)) e.push(`bots.${k}.autonomy.promote: ${v} runs at ${tr}; only a reversible-tier verb can be promoted (run without your signature)`); });
      if (n.isolation) { Object.keys(n.isolation).forEach((x) => { if (!['sandbox', 'handoff_workers', 'skip_permission_prompts'].includes(x)) e.push(`bots.${k}.isolation.${x}: not a bot isolation key (sandbox, handoff_workers, skip_permission_prompts)`); }); if (n.isolation.sandbox != null && !['optional', 'required'].includes(n.isolation.sandbox)) e.push(`bots.${k}.isolation.sandbox: optional or required`); }
      if (n.proactive) { if (!n.proactive.every) e.push(`bots.${k}.proactive.every: required (how often it sweeps)`); const ch = n.proactive.checklist || {}; if (!!ch.section === !!ch.file) e.push(`bots.${k}.proactive.checklist: exactly one of section (a charter heading) or file`); }
      const sc = S.getPath(n, 'autonomy.spend_cap'); if (sc && Object.keys(sc).some((x) => !['per_action', 'per_period', 'period', 'currency'].includes(x))) e.push(`bots.${k}.autonomy.spend_cap: per_action, per_period, period, currency`);
      if (n.worker && n.worker.runtime && !byName(all.runtimes, n.worker.runtime)) e.push(`bots.${k}.worker.runtime: no runtime "${n.worker.runtime}"`);
      e.push(...S.narrowErrors(k, n, all));
      return e;
    },
    warn(k, n, all) { const w = []; (n.peers || []).forEach((p) => { const o = byName(all.bots, p); if (o && o.cfg && n.team && o.cfg.team === n.team) { w.push(`bots.${k}.peers: ${p} is in the same team, so they are already peers`); return; } if (o && !((o.cfg && o.cfg.peers) || []).includes(k)) w.push(`bots.${k}.peers: ${p} doesn't list ${k} back, so the link stays closed until it does`); }); if (n.team && n.folder) w.push(`bots.${k}.folder: ${k} is on team ${n.team}, so it shows under the team; its own folder is ignored`); if (!S.getPath(n, 'autonomy.shadow') && !all.bots.find((m) => m.name === k)) w.push(`bots.${k}: a new bot normally starts in shadow mode`); return w; },
    widens(b, a) {
      const r = [], B = b || {}, A = a || {};
      const bv = new Set(S.getPath(B, 'skill.verbs') || []); (S.getPath(A, 'skill.verbs') || []).forEach((v) => { if (!bv.has(v)) r.push(`adds verb ${v}`); });
      const order = ['never', 'propose', 'commit_money', 'reversible', 'observe'];
      const tierOf = (n, v) => Object.entries(S.getPath(n, 'autonomy.tiers') || {}).find(([, vs]) => (vs || []).includes(v));
      (S.getPath(A, 'skill.verbs') || []).forEach((v) => { const tb = tierOf(B, v), ta = tierOf(A, v); const ib = order.indexOf(tb ? tb[0] : 'propose'), ia = order.indexOf(ta ? ta[0] : 'propose'); if (bv.has(v) && ia > ib) r.push(`loosens ${v}: ${tb ? tb[0] : 'propose'} → ${ta[0]}`); });
      const bp = new Set(S.getPath(B, 'autonomy.promote') || []); (S.getPath(A, 'autonomy.promote') || []).forEach((v) => { if (!bp.has(v)) r.push(`promotes ${v}`); });
      if (b && S.getPath(B, 'autonomy.shadow') && !S.getPath(A, 'autonomy.shadow')) r.push('leaves shadow mode: proposals become approvable');
      if (b && A.team && A.team !== B.team) { const t = byName(window.D.teams, A.team); if (t) (S.getPath(t.cfg, 'defaults.operators') || []).forEach((o) => { if (!(B.operators || []).includes(o) && !A.operators) r.push(`inherits approver ${o} from team ${A.team}`); }); }
      return r;
    },
  };
  C.identities = { section: 'browser', file: 'config.yaml', label: 'browser identity', href: () => '#/browser',
    keys: () => D().identities.map((i) => i.id), get: (k) => { const i = D().identities.find((x) => x.id === k); return i ? { allow: i.allow, logins: i.logins, evaluate: i.evaluate } : null; },
    set: (k, n) => { if (n == null) return drop(D().identities, k, 'id'); const i = D().identities.find((x) => x.id === k); if (i) Object.assign(i, n); else D().identities.push(Object.assign({ id: k }, n)); },
    path: (k) => `browser.identities["${k}"]`,
    validate(k, n) { const e = []; (n.allow || []).forEach((h) => { if (h === '*' || h.startsWith('*.')) e.push(`browser.identities["${k}"].allow: a "${h}" allowlist is refused; name the sites`); }); (n.logins || []).forEach((h) => { if (!(n.allow || []).includes(h)) e.push(`browser.identities["${k}"].logins: ${h} must also be in allow`); }); return e; },
    widens(b, a) { const r = [], B = b || {}, A = a || {}; (A.allow || []).forEach((h) => { if (!(B.allow || []).includes(h)) r.push(`allows site ${h}`); }); (A.logins || []).forEach((h) => { if (!(B.logins || []).includes(h)) r.push(`adds login ${h}`); }); if (A.evaluate && A.evaluate !== 'off' && A.evaluate !== B.evaluate) r.push(`turns evaluate ${A.evaluate}`); return r; },
  };
  /* browser.credentials (feat/agent-browser): a list of { name, origin, identities, login_url?, username/password/otp/passkey: { vault, key }, approval? } */
  C.credentials = { section: 'browser', file: 'config.yaml', label: 'credential binding', href: () => '#/browser/logins',
    keys: () => D().access.credentials.map((c) => c.name), get: (k) => { const c = byName(D().access.credentials, k); return c ? clone(c.cfg) : null; },
    set: (k, n) => { const L = D().access.credentials; if (n == null) return drop(L, k); const c = byName(L, k); const patch = { cfg: Object.assign({ name: k }, n), origin: n.origin, identities: n.identities || [] }; if (c) Object.assign(c, patch); else L.push(Object.assign({ name: k }, patch)); },
    path: (k) => `browser.credentials[name=${k}]`,
    validate(k, n, all) {
      const e = [], P = `browser.credentials[${k}]`;
      Object.keys(n).forEach((f) => { if (!['name', 'origin', 'identities', 'login_url', 'username', 'password', 'otp', 'passkey', 'approval', 'email_code'].includes(f)) e.push(`${P}.${f}: not a credential key (field ${f} not found in type config.BrowserCredential)`); });
      if (!/^https:\/\/[\w.-]+(:\d+)?(\/[\w./-]*)?$/.test(n.origin || '')) e.push(`${P}.origin: "${n.origin || ''}" must be an exact https:// origin (scheme://host[:port], optional path prefix)`);
      if (!(n.identities || []).length) e.push(`${P}.identities: name at least one identity that may use it (a bot: bot:<name>)`);
      (n.identities || []).forEach((id) => { if (/^(folder|team):/.test(id)) e.push(`${P}.identities: "${id}" is not an identity; folders and teams don't hold access (name the bots)`); else if (/^bot:/.test(id) && !byName(all.bots, id.slice(4))) e.push(`${P}.identities: no bot "${id.slice(4)}"`); });
      ['username', 'password', 'otp', 'passkey'].forEach((f) => { const r = n[f]; if (r == null) return; if (typeof r !== 'object' || !r.vault || !r.key) return e.push(`${P}.${f}: { vault, key } naming where the value lives, never a value`); const v = byName(all.vaults, r.vault); if (!v) e.push(`${P}.${f}: no vault "${r.vault}"`); else if (v.status === 'fail') e.push(`${P}.${f}: vault ${r.vault} is unreachable`); });
      if (!n.passkey && !(n.username && n.password)) e.push(`${P}: needs username and password (or a passkey)`);
      return e; },
  };;
  C.packs = { section: 'packs', file: 'config.yaml', label: 'pack', href: () => '#/extensions/packs',
    keys: () => D().packs.map((p) => p.name), get: (k) => { const p = byName(D().packs, k); return p ? clone(p.cfg) : null; },
    set(k, n) { const L = D().packs; if (n == null) return drop(L, k); const p = byName(L, k); const ver = (n.use.split('@')[1] || '').replace('^', ''); const ov = Object.keys(n.steps || {}).length + Object.keys(n.triggers || {}).length; if (p) { const moved = ver && p.update && ver === p.update; Object.assign(p, { cfg: n, use: n.use.split('@')[0], hold: !!n.hold, overrides: ov, ver: moved ? ver : p.ver, update: moved ? null : p.update, sha: moved ? '0e47d12' : p.sha }); } else L.push({ name: k, use: n.use.split('@')[0], ver: ver || '1.0.0', sha: 'b51d0a2', status: 'ok', official: n.use.startsWith('conductor-packs/'), hold: !!n.hold, triggers: '0 armed / 1 · arm one to start', overrides: ov, cfg: n }); },
    path: (k) => `packs.${k}`,
    validate(k, n, all) { const e = []; if (!n.use) e.push(`packs.${k}.use is required`); else if (!/^(conductor-packs\/|\.\/|acme\/)/.test(n.use)) e.push(`packs.${k}.use: ${n.use.split('@')[0]} is not allowed by pack_trust (add a pack_trust source first)`); const mem = (D().packMembers || {})[k]; if (mem) { const addr = Object.entries(mem.workflows).flatMap(([w, ss]) => ss.map((x) => `${w}/${x}`)); Object.keys(n.steps || {}).forEach((key) => { if (!addr.includes(key)) e.push(`packs.${k}.steps["${key}"]: matches nothing in the pack (its steps: ${addr.join(', ')}); an overlay key that matches nothing is an error`); }); Object.keys(n.triggers || {}).forEach((t) => { if (!mem.triggers.includes(t)) e.push(`packs.${k}.triggers.${t}: the pack has no trigger "${t}" (it has ${mem.triggers.join(', ')})`); }); Object.keys(n.connectors || {}).forEach((role) => { if (!mem.roles.includes(role)) e.push(`packs.${k}.connectors.${role}: the pack declares no "${role}" role (requires: ${mem.roles.join(', ')})`); }); Object.keys(n).forEach((key) => { if (!['use', 'connectors', 'steps', 'triggers', 'hold', 'secrets', 'folders', 'settings'].includes(key)) e.push(`packs.${k}.${key}: not a pack instance key`); }); Object.entries(n.triggers || {}).forEach(([t, arm]) => { if (arm && arm.enabled && /github|gh/.test((mem.roles || []).join(',')) && !(arm.repos || []).length) e.push(`packs.${k}.triggers.${t}: arming a github trigger needs repos: (the repos are your consent)`); }); } Object.entries(n.connectors || {}).forEach(([role, inst]) => { if (!byName(all.connectors, inst)) e.push(`packs.${k}.connectors.${role}: no connector "${inst}"`); }); return e; },
    widens(b, a) { const r = []; Object.entries((a && a.steps) || {}).forEach(([s, o]) => { if (o && o.isolation && o.isolation.mode === 'none' && !S.getPath(b || {}, `steps`)?.[s]?.isolation) r.push(`runs pack step ${s} unsandboxed`); }); if (a && !b) r.push(`adds pack source ${a.use.split('@')[0]}`); return r; },
  };
  C.workflows = { section: 'triggers', file: 'config.yaml', label: 'workflow', href: (k) => `#/workflows/${k}`, text: true,
    keys: () => D().workflows.filter((w) => w.yaml && w.source === 'config').map((w) => w.name),
    get: (k) => { const w = byName(D().workflows, k); return w && w.source === 'config' ? w.yaml : null; },
    set(k, yaml) { const L = D().workflows; if (yaml == null) return drop(L, k); const w = byName(L, k); const nw = wfFromYaml(k, yaml, w); if (!w) L.push(nw); },
    path: (k) => { const w = byName(D().workflows, k); return w && w.kind === 'workflow' ? `workflows.${k}` : `triggers[name=${k}]`; },
    validate(k, yaml, all) { try { Y.parse(yaml); } catch (e) { return [`${k}: ${e.message}`]; } return window.WF ? window.WF.validate(yaml).filter((x) => x.level === 'error').map((x) => `${k}: line ${x.line}: ${x.msg}`) : []; },
    warn(k, yaml) { return window.WF ? window.WF.validate(yaml).filter((x) => x.level === 'warn').map((x) => `${k}: line ${x.line}: ${x.msg}`) : []; },
    widens(b, a) { const r = []; if (/isolation:\s*\{?\s*mode:\s*none/.test(a || '') && !/isolation:\s*\{?\s*mode:\s*none/.test(b || '')) r.push('a step runs unsandboxed (isolation: none)'); if (/callable:\s*true/.test(a || '') && !/callable:\s*true/.test(b || '')) r.push('makes the trigger callable:'); return r; },
  };
  const single = (sec, getter, setter, o = {}) => (C[sec] = Object.assign({ section: sec, file: 'config.yaml', keys: () => [sec], get: () => clone(getter()), set: (k, n) => setter(n), path: () => sec }, o));
  single('sandbox', () => D().sandboxCfg, (n) => { D().sandboxCfg = n; D().daemon.sandbox.backend = n.backend; D().daemon.sandbox.egress = n.egress || []; D().daemon.sandbox.agents = n.agents; }, {
    label: 'sandbox', href: () => '#/connectors/isolation',
    validate(k, n) { const e = []; (n.egress || []).forEach((h) => { if (h === '*') e.push('sandbox.egress: "*" is refused; name the hosts'); if (/(^|\.)tail0000\.ts\.net$|^127\.|^localhost/.test(h)) e.push(`sandbox.egress: ${h} is the dashboard's own address; the proxy hard-denies it`); }); return e; },
    widens(b, a) { const r = []; (a.egress || []).forEach((h) => { if (!(b.egress || []).includes(h)) r.push(`adds egress ${h}`); }); if (a.agents === 'off' && b.agents !== 'off') r.push('turns the agent sandbox off'); return r; },
  });
  single('usage', () => D().usageCfg, (n) => { D().usageCfg = n; }, { label: 'usage mode', href: () => '#/config',
    validate: (k, n) => [].concat(['off', 'subscription', 'api'].includes(n.mode) ? [] : [`usage.mode: "${n.mode}" is not off, subscription or api`], n.source && !n.source.name ? ['usage.source: needs a name'] : [], n.source && !/^https?:\/\/[^\s/]+/.test(n.source.url || '') ? ['usage.source.url: an http(s) URL the source reports usage on'] : []) });
  single('update', () => D().updateCfg, (n) => { D().updateCfg = n; D().daemon.update.auto = n.auto; D().daemon.update.interval = n.interval; }, { label: 'update policy', href: () => '#/updates',
    validate: (k, n) => (/^\d+[smh]$/.test(n.interval || '') ? [] : [`update.interval: "${n.interval}" is not a duration (e.g. 10m)`]) });
  C.operators = { section: 'operators', file: 'config.yaml', label: 'operator', href: () => '#/security',
    keys: () => Object.keys(D().operatorsCfg), get: (k) => clone(D().operatorsCfg[k] || null), set: (k, n) => { if (n == null) delete D().operatorsCfg[k]; else D().operatorsCfg[k] = n; }, path: (k) => `operators.${k}` };


  /* ---------- teams ---------- */
  const TIER_ORDER = ['never', 'propose', 'commit_money', 'reversible', 'observe']; // looser →
  S.TIER_ORDER = TIER_ORDER;
  S.FOLDER_RE = /^[\w][\w .-]*(\/[\w][\w .-]*)*$/;
  S.teamOf = (bot, st = window.D) => { const m = byName(st.bots, bot); const t = m && m.cfg && m.cfg.team; return t ? byName(st.teams, t) : null; };
  /** Tier strictness: observe < reversible < propose < commit_money < never (a verb in several tiers takes the strictest). */
  /** least to most cautious: observe < reversible < propose < commit_money < never */
  S.LADDER = ['observe', 'reversible', 'propose', 'commit_money', 'never'];
  const STRICT = (t) => S.LADDER.indexOf(t);
  const tierOfIn = (tiers, verb) => { let best = null; Object.entries(tiers || {}).forEach(([t, vs]) => (vs || []).forEach((p) => { if (p === verb || (p.endsWith('*') && verb.startsWith(p.slice(0, -1)))) { if (best == null || STRICT(t) > STRICT(best)) best = t; } })); return best; };
  S.tierOfIn = tierOfIn;
  /** The tier a verb runs at for a bot: the strictest of its own and its team's classification (unclassified: propose). */
  S.tierFor = (m, verb, st = window.D) => { const n = m.cfg || {}, t = n.team && byName(st.teams, n.team); const own = tierOfIn(S.getPath(n, 'autonomy.tiers'), verb), team = t ? tierOfIn(S.getPath(t.cfg, 'autonomy.tiers'), verb) : null; const ts = [own, team].filter(Boolean); return { tier: ts.length ? ts.sort((a, b) => STRICT(b) - STRICT(a))[0] : 'propose', own, team, teamName: t && t.name }; };
  /** The team ceiling: the highest tier a bot may be promoted to (run without a signature). */
  S.ceilingOf = (m, st = window.D) => { const n = m.cfg || {}, t = n.team && byName(st.teams, n.team); return t ? S.getPath(t.cfg, 'autonomy.ceiling') || 'reversible' : 'reversible'; };
  /** What a bot actually runs with: the team's values, only ever narrowed by the bot. */
  S.effective = (m, st = window.D) => {
    const n = m.cfg || {}, t = n.team && byName(st.teams, n.team), tc = (t && t.cfg) || {};
    const same = (a, b) => JSON.stringify(a) === JSON.stringify(b);
    const pick = (mine, theirs) => (mine !== undefined ? { v: mine, src: theirs === undefined ? 'own' : same(mine, theirs) ? 'equal' : 'narrows' } : theirs !== undefined ? { v: theirs, src: 'inherits' } : { v: undefined, src: 'unset' });
    return {
      team: t ? t.name : null,
      operators: pick(n.operators, tc.operators),
      sandbox: pick(S.getPath(n, 'isolation.sandbox'), S.getPath(tc, 'isolation.sandbox')),
      ceiling: { v: S.getPath(tc, 'autonomy.ceiling') || (t ? 'reversible' : undefined), src: t ? 'team' : 'unset' },
      maxTier: { v: S.getPath(tc, 'autonomy.ceiling') || (t ? 'reversible' : undefined), src: t ? 'team' : 'unset' },
      shadow: pick(S.getPath(n, 'autonomy.shadow'), S.getPath(tc, 'autonomy.shadow')),
      promote: { v: S.getPath(n, 'autonomy.promote') || [], src: 'own' },
      approvalTtl: pick(S.getPath(n, 'autonomy.approval_ttl'), undefined),
      spendCap: pick(S.getPath(n, 'autonomy.spend_cap'), S.getPath(tc, 'budget.spend_cap')),
      proactive: pick(n.proactive && n.proactive.budget, S.getPath(tc, 'budget.proactive')),
      credentials: { v: st.access.credentials.filter((c) => (c.identities || []).includes('bot:' + m.name)).map((c) => c.name), scope: S.getPath(tc, 'browser.credentials') },
      logins: { v: (st.identities.find((i) => i.id === 'bot:' + m.name) || {}).logins || [], scope: S.getPath(tc, 'browser.logins') },
      peersIn: t && tc.peers !== 'none' ? st.bots.filter((x) => x.name !== m.name && x.cfg && x.cfg.team === t.name).map((x) => x.name) : [],
      peersOut: S.linksOf(m.name, st).filter((p) => { const o = byName(st.bots, p); return !t || !o || !o.cfg || o.cfg.team !== t.name; }),
    };
  };
  /** Bots this one may message: mutual peers, or an operator bot_links pair. */
  S.linksOf = (bot, st = window.D) => { const m = byName(st.bots, bot); const mine = (m && m.cfg && m.cfg.peers) || []; const mutual = mine.filter((p) => { const o = byName(st.bots, p); return o && o.cfg && (o.cfg.peers || []).includes(bot); }); const ops = (st.botLinks || []).filter((pr) => pr.includes(bot)).map((pr) => pr.find((x) => x !== bot)); return [...new Set(mutual.concat(ops))]; };
  S.linked = (a, b, st = window.D) => S.linksOf(a, st).includes(b);
  S.narrowErrors = (k, n, all) => {
    const e = []; if (!n.team) return e;
    const t = byName(all.teams, n.team);
    if (!t) return [`bots.${k}.team: no team named "${n.team}"`];
    const tc = t.cfg || {};
    if (n.operators && tc.operators) n.operators.forEach((o) => { if (!tc.operators.includes(o)) e.push(`bots.${k}.operators: ${o} is not one of team ${t.name}'s operators (${tc.operators.join(', ')}); a bot's operators must be a subset`); });
    const ceiling = S.getPath(tc, 'autonomy.ceiling') || 'reversible';
    const bt = S.getPath(n, 'autonomy.tiers') || {}, tt = S.getPath(tc, 'autonomy.tiers') || {};
    Object.entries(bt).forEach(([tier, vs]) => (vs || []).forEach((v) => { const teamTier = tierOfIn(tt, v); if (teamTier && STRICT(teamTier) > STRICT(tier)) e.push(`bots.${k}.autonomy.tiers.${tier}: team ${t.name} classifies ${v} as ${teamTier}; a bot can only be stricter (Team ceilings)`); }));
    (S.getPath(n, 'autonomy.promote') || []).forEach((v) => { const tier = S.tierFor({ cfg: n }, v, all).tier; if (STRICT(tier) > STRICT(ceiling)) e.push(`bots.${k}.autonomy.promote: ${v} runs at ${tier}; team ${t.name}'s ceiling is ${ceiling}, so it can't run without your signature (Team ceilings)`); });
    if (S.getPath(tc, 'autonomy.shadow') === true && S.getPath(n, 'autonomy.shadow') === false) e.push(`bots.${k}.autonomy.shadow: team ${t.name} holds every bot in shadow mode; a bot can't set shadow: false (Team ceilings)`);
    if (S.getPath(tc, 'isolation.sandbox') === 'required' && S.getPath(n, 'isolation.sandbox') === 'optional') e.push(`bots.${k}.isolation.sandbox: team ${t.name} requires the sandbox; optional would loosen it (Team ceilings)`);
    const pool = S.getPath(tc, 'budget.proactive'), mb = n.proactive && n.proactive.budget;
    if (pool && mb) ['max_runs_per_day', 'max_turns_per_day', 'max_tokens_per_day', 'max_cost_per_day'].forEach((f) => { if (pool[f] != null && mb[f] != null && Number(mb[f]) > Number(pool[f])) e.push(`bots.${k}.proactive.budget.${f}: ${mb[f]} doesn't fit team ${t.name}'s proactive pool (${pool[f]})`); });
    const sc = S.getPath(tc, 'budget.spend_cap'), bs = S.getPath(n, 'autonomy.spend_cap');
    if (sc && bs) ['per_action', 'per_period'].forEach((f) => { if (sc[f] != null && bs[f] != null && bs[f] > sc[f]) e.push(`bots.${k}.autonomy.spend_cap.${f}: ${bs[f]} is over team ${t.name}'s cap (${sc[f]})`); });
    const scope = S.getPath(tc, 'browser.credentials'); if (scope) all.access.credentials.filter((c) => (c.identities || []).includes('bot:' + k) && !scope.includes(c.name)).forEach((c) => e.push(`browser.credentials.${c.name}: names bot:${k}, outside team ${t.name}'s credential scope (teams.${t.name}.browser.credentials)`));
    const ls = S.getPath(tc, 'browser.logins'); const idn = all.identities.find((i) => i.id === 'bot:' + k); if (ls && idn) (idn.logins || []).filter((h) => !ls.includes(h)).forEach((h) => e.push(`browser.logins: ${h} for bot:${k} is outside team ${t.name}'s login scope`));
    if (n.isolation && n.isolation.handoff_workers === true && S.getPath(tc, 'isolation.handoff_workers') === false) e.push(`bots.${k}.isolation.handoff_workers: team ${t.name} allows cli workers only`);
    return e;
  };
  C.teams = { section: 'teams', file: 'config.yaml', label: 'team', href: (k) => `#/teams/${k}`,
    keys: () => D().teams.map((t) => t.name), get: (k) => { const t = byName(D().teams, k); return t ? clone(t.cfg) : null; },
    set: (k, n) => { const L = D().teams; if (n == null) return drop(L, k); const t = byName(L, k); if (t) t.cfg = n; else L.push({ name: k, cfg: n }); },
    path: (k) => `teams.${k}`,
    validate(k, n, all) {
      const e = [];
      if (!/^[a-z][a-z0-9-]*$/.test(k)) e.push(`team name "${k}": lowercase letters, digits and dashes`);
      if (window.RS) Object.keys(n).forEach((x) => { if (!RS.team.has(x)) e.push(`teams.${k}.${x}: not a team key (field ${x} not found in type config.TeamConfig)`); });
      if (n.folder != null && !S.FOLDER_RE.test(n.folder)) e.push(`teams.${k}.folder: "${n.folder}" — segments of letters, digits, space, . _ -, joined by /`);
      const mem = all.bots.filter((m) => m.cfg && m.cfg.team === k).map((m) => m.name);
      e.push(...S.collErrors(`teams.${k}`, n.collections, 'team'));
      const c = S.getPath(n, 'autonomy.ceiling'); if (c != null && !['reversible', 'observe'].includes(c)) e.push(`teams.${k}.autonomy.ceiling: reversible (the most promotion can reach) or observe (no bot promotes anything), not "${c}"`);
      if (n.peers != null && !['team', 'none'].includes(n.peers)) e.push(`teams.${k}.peers: team or none`);
      const sb = S.getPath(n, 'isolation.sandbox'); if (sb != null && !['optional', 'required'].includes(sb)) e.push(`teams.${k}.isolation.sandbox: optional or required`);
      const sv = S.getPath(n, 'credentials.secrets_via'); if (sv != null && !['none', 'broker', 'env'].includes(sv)) e.push(`teams.${k}.credentials.secrets_via: none, broker or env`);
      const pr = S.getPath(n, 'knowledge.promotion'); if (pr != null && !['proposal', 'direct'].includes(pr)) e.push(`teams.${k}.knowledge.promotion: proposal or direct`);
      const ro = n.routing; if (ro) { if (ro.lead && !mem.includes(ro.lead)) e.push(`teams.${k}.routing.lead: ${ro.lead} is not a bot of team ${k}`); if (ro.fallback && !mem.includes(ro.fallback)) e.push(`teams.${k}.routing.fallback: ${ro.fallback} is not a bot of team ${k}`); (ro.rules || []).forEach((r, i) => { const nm = r.name || `rules[${i}]`; if (!r.match || !Object.keys(r.match).length) e.push(`teams.${k}.routing.rules.${nm}: match is required`); if (!r.to) e.push(`teams.${k}.routing.rules.${nm}: to is required`); else if (!mem.includes(r.to)) e.push(`teams.${k}.routing.rules.${nm}: ${r.to} is not a bot of team ${k}`); }); }
      const pool = S.getPath(n, 'budget.proactive'); if (pool && !['max_runs_per_day', 'max_turns_per_day', 'max_tokens_per_day', 'max_cost_per_day'].some((f) => pool[f] != null)) e.push(`teams.${k}: budget.proactive sets no cap, so it limits nothing (set max_runs_per_day, max_turns_per_day, max_tokens_per_day or max_cost_per_day)`);
      (n.operators || []).forEach((o) => { if (!all.operatorsCfg[o]) e.push(`teams.${k}.operators: "${o}" is not in operators:`); });
      all.bots.filter((m) => m.cfg && m.cfg.team === k).forEach((m) => e.push(...S.narrowErrors(m.name, m.cfg, all)));
      return e;
    },
    warn(k, n) { const w = []; if (S.getPath(n, 'knowledge.promotion') === 'direct') w.push(`teams.${k}.knowledge.promotion: direct — a bot's promotion lands as a commit at once when its request's provenance would let a promoted verb run`); return w; },
    refs(k, all) { return all.bots.filter((m) => m.cfg && m.cfg.team === k).map((m) => `bots.${m.name}.team`); },
    widens(b, a) { return S.classify('teams', b, a); },
  };
  /* bot_links: operator-side peer links between pairs of bots (top-level) */
  C.botLinks = { section: 'bot_links', file: 'config.yaml', label: 'bot link', href: () => '#/bots',
    keys: () => (D().botLinks || []).map((p) => p.slice().sort().join('+')),
    get: (k) => { const p = (D().botLinks || []).find((x) => x.slice().sort().join('+') === k); return p ? p.slice() : null; },
    set: (k, n) => { D().botLinks = (D().botLinks || []).filter((x) => x.slice().sort().join('+') !== k); if (n) D().botLinks.push(n); },
    path: () => 'bot_links',
    validate(k, n, all) { return (n || []).length !== 2 || n[0] === n[1] ? [`bot_links: a pair of two different bots`] : n.filter((x) => !byName(all.bots, x)).map((x) => `bot_links: no bot "${x}"`); },
    widens(b, a) { return S.classify('botLinks', b, a); },
  };
  C.models = { section: 'models', file: 'config.yaml', label: 'fleet', href: () => '#/connectors/runtimes',
    keys: () => D().fleets.map((f) => f.name), get: (k) => { const f = byName(D().fleets, k); return f ? clone(f.cfg) : null; },
    set: (k, n) => { if (n == null) return drop(D().fleets, k); const f = byName(D().fleets, k); if (f) f.cfg = n; else D().fleets.push({ name: k, cfg: n }); },
    path: (k) => `models.${k}`,
    validate: (k, n) => (/^[a-z][\w-]*$/.test(k) ? [] : [`fleet name "${k}": letters, digits, - and _`]).concat((n.any || []).length ? [] : [`models.${k}.any: list at least one model or wildcard`]) };
  /* ONE widening classifier for every edit surface (builder, bots, teams, packs, sandbox, browser): each item
   * names what got wider and where. Official packs and local packs aren't flagged as new sources. */
  const added = (b, a) => (a || []).filter((x) => !(b || []).includes(x));
  const yv = (t) => { if (t == null) return null; if (typeof t !== 'string') return t; try { return Y.parse(t).value || {}; } catch (e) { return {}; } };
  const stepsOf = (w) => { const out = []; const walk = (ss) => (ss || []).forEach((st) => { if (!st || typeof st !== 'object') return; out.push(st); (st.parallel || []).forEach((br) => walk(br)); }); walk(w && w.steps); return out; };
  S.classify = (coll, before, after) => {
    const r = [], B = yv(before) || {}, A = yv(after) || {}, isNew = before == null;
    if (after == null) return r;
    const tiersLoosened = (bt, at, where) => { const verbs = new Set([].concat(...Object.values(bt || {}), ...Object.values(at || {}))); verbs.forEach((v) => { const tb = tierOfIn(bt, v) || 'propose', ta = tierOfIn(at, v) || 'propose'; if (STRICT(ta) < STRICT(tb)) r.push(`${where}: loosens ${v} from ${tb} to ${ta}`); }); };
    if (coll === 'bots') {
      tiersLoosened(S.getPath(B, 'autonomy.tiers'), S.getPath(A, 'autonomy.tiers'), 'autonomy.tiers');
      added(S.getPath(B, 'autonomy.promote'), S.getPath(A, 'autonomy.promote')).forEach((v) => r.push(`autonomy.promote: ${v} now runs without your signature`));
      if (S.getPath(B, 'autonomy.shadow') === true && S.getPath(A, 'autonomy.shadow') !== true) r.push('autonomy.shadow: leaves shadow mode (approved actions execute)');
      if (S.getPath(B, 'isolation.sandbox') === 'required' && S.getPath(A, 'isolation.sandbox') !== 'required') r.push('isolation.sandbox: no longer requires the agent sandbox');
      if (!isNew) added(B.operators, A.operators).forEach((o) => r.push(`operators: adds approver ${o}`));
      added(Array.isArray(S.getPath(B, 'skill.verbs')) ? S.getPath(B, 'skill.verbs') : Object.keys(S.getPath(B, 'skill.verbs') || {}), Array.isArray(S.getPath(A, 'skill.verbs')) ? S.getPath(A, 'skill.verbs') : Object.keys(S.getPath(A, 'skill.verbs') || {})).forEach((v) => r.push(`skill.verbs: grants ${v}`));
      added(B.peers, A.peers).forEach((p) => r.push(`peers: may message ${p}`));
      if ((S.getPath(A, 'skill.secrets_via') || 'none') !== 'none' && (S.getPath(B, 'skill.secrets_via') || 'none') === 'none') r.push(`skill.secrets_via: ${S.getPath(A, 'skill.secrets_via')} (the agent can obtain secrets)`);
      added(S.getPath(B, 'skill.allow_secrets'), S.getPath(A, 'skill.allow_secrets')).forEach((x) => r.push(`skill.allow_secrets: ${x}`));
    } else if (coll === 'teams') {
      if ((S.getPath(B, 'autonomy.ceiling') || 'reversible') === 'observe' && (S.getPath(A, 'autonomy.ceiling') || 'reversible') === 'reversible') r.push('autonomy.ceiling: raised to reversible (bots may promote reversible verbs)');
      if (S.getPath(B, 'autonomy.shadow') === true && S.getPath(A, 'autonomy.shadow') !== true) r.push('autonomy.shadow: bots leave shadow mode');
      tiersLoosened(S.getPath(B, 'autonomy.tiers'), S.getPath(A, 'autonomy.tiers'), 'autonomy.tiers');
      if (S.getPath(B, 'isolation.sandbox') === 'required' && S.getPath(A, 'isolation.sandbox') !== 'required') r.push('isolation.sandbox: bots no longer need the agent sandbox');
      if (!isNew) added(B.operators, A.operators).forEach((o) => r.push(`operators: adds approver ${o} for every bot`));
      added(S.getPath(B, 'browser.credentials'), S.getPath(A, 'browser.credentials')).forEach((x) => r.push(`browser.credentials: bots may use ${x}`));
      added(S.getPath(B, 'browser.logins'), S.getPath(A, 'browser.logins')).forEach((x) => r.push(`browser.logins: bots may use your session on ${x}`));
      added(S.getPath(B, 'credentials.allow_secrets'), S.getPath(A, 'credentials.allow_secrets')).forEach((x) => r.push(`credentials.allow_secrets: ${x}`));
      if (S.getPath(A, 'knowledge.promotion') === 'direct' && S.getPath(B, 'knowledge.promotion') !== 'direct') r.push('knowledge.promotion: bots promote shared knowledge without approval');
      if (B.peers === 'none' && A.peers !== 'none') r.push('peers: teammates may message each other');
      if (S.getPath(B, 'isolation.handoff_workers') === false && S.getPath(A, 'isolation.handoff_workers') !== false) r.push('isolation.handoff_workers: hand-off sessions allowed');
    } else if (coll === 'botLinks') { if (isNew) r.push(`bot_links: ${A[0]} and ${A[1]} may message each other`); }
    else if (coll === 'workflows') {
      const bs = stepsOf(B), as = stepsOf(A), byId = (ss, id) => ss.find((x) => x.id === id) || {};
      as.forEach((st, i) => { const prev = st.id ? byId(bs, st.id) : bs[i] || {}; const P = `steps[${st.id || i}]`;
        added(S.getPath(prev, 'isolation.network.egress'), S.getPath(st, 'isolation.network.egress')).forEach((h) => r.push(`${P}.isolation.network.egress: adds ${h}`));
        if (prev.isolation && !st.isolation) r.push(`${P}.isolation: removed (the step runs with the runtime's isolation only)`);
        if (S.getPath(st, 'isolation.privileged') && !S.getPath(prev, 'isolation.privileged')) r.push(`${P}.isolation.privileged: sees the daemon's own files`);
        if (S.getPath(prev, 'isolation.network.deny') && !S.getPath(st, 'isolation.network.deny')) r.push(`${P}.isolation.network: network no longer cut`);
        const vb = S.getPath(prev, 'skill.verbs'), va = S.getPath(st, 'skill.verbs'); added(Array.isArray(vb) ? vb : Object.keys(vb || {}), Array.isArray(va) ? va : Object.keys(va || {})).forEach((v) => r.push(`${P}.skill.verbs: grants ${v}`));
        if (va && typeof va === 'object' && !Array.isArray(va)) Object.entries(va).forEach(([v, sc]) => Object.entries(sc || {}).forEach(([rk, xs]) => added(S.getPath(vb || {}, `${v}.${rk}`) || [], xs).forEach((x) => r.push(`${P}.skill.verbs.${v}.${rk}: may name ${x}`))));
        added(S.getPath(prev, 'skill.allow_secrets'), S.getPath(st, 'skill.allow_secrets')).forEach((x) => r.push(`${P}.skill.allow_secrets: ${x}`));
      });
      const repos = (w) => { const f = w.filter; const out = []; const walk = (x) => { if (Array.isArray(x)) x.forEach(walk); else if (x && typeof x === 'object') [].concat(x.repo || []).forEach((y) => out.push(y)); }; walk(f); return out; };
      if (!isNew) { const rb = repos(B), ra = repos(A); added(rb, ra).forEach((x) => r.push(`filter.repo: also runs on ${x}`)); if (rb.length && !ra.length) r.push('filter.repo: removed (runs on every repo the connector sees)'); }
      if (A.callable === true && B.callable !== true) r.push('callable: reachable over the invoke API');
    } else if (coll === 'packs') {
      const src = String(A.use || '').split('@')[0];
      if (isNew && !/^conductor-packs\//.test(src) && !/^\.\//.test(src)) r.push(`adds third-party pack source ${src} (needs pack_trust)`);
      Object.entries(A.triggers || {}).forEach(([t, arm]) => { const was = (B.triggers || {})[t] || {}; if (arm && arm.enabled && !was.enabled) r.push(`triggers.${t}: armed${(arm.repos || []).length ? ' on ' + arm.repos.join(', ') : ''}`); added(was.repos, arm && arm.repos).forEach((x) => { if (was.enabled) r.push(`triggers.${t}.repos: adds ${x}`); }); });
      Object.entries(A.steps || {}).forEach(([k, o]) => { const was = (B.steps || {})[k] || {}; added(S.getPath(was, 'isolation.network.egress'), S.getPath(o, 'isolation.network.egress')).forEach((h) => r.push(`steps["${k}"].isolation.network.egress: adds ${h}`)); if (S.getPath(o, 'isolation.privileged') && !S.getPath(was, 'isolation.privileged')) r.push(`steps["${k}"].isolation.privileged`); });
    } else if (coll === 'identities') { added(B.allow, A.allow).forEach((h) => r.push(`browser.allow: allows site ${h}`)); added(B.logins, A.logins).forEach((h) => r.push(`browser.logins: uses your session on ${h}`)); if (A.evaluate && A.evaluate !== 'off' && B.evaluate !== A.evaluate) r.push('browser.evaluate: read-only JavaScript'); }
    else if (coll === 'credentials') { added(B.identities, A.identities).forEach((id) => r.push(`browser.credentials[${A.name || ''}]: ${id} may sign in as you on ${A.origin}`)); }
    else if (coll === 'sandbox') { added(B.egress, A.egress).forEach((h) => r.push(`sandbox.egress: adds ${h}`)); if (B.agents === 'on' && A.agents !== 'on') r.push('sandbox.agents: off (agents run unsandboxed)'); }
    else if (coll === 'runtimes') { if (B.isolation && !A.isolation) r.push('isolation: removed from the runtime'); }
    else if (coll === 'connectors') { added(B.repos, A.repos).forEach((x) => r.push(`repos: adds ${x}`)); }
    return r;
  };
  S.ORDER = ['settings', 'vaults', 'secrets', 'connectors', 'hosts', 'runtimes', 'sandbox', 'operators', 'teams', 'bots', 'botLinks', 'env', 'identities', 'credentials', 'packs', 'update', 'workflows'];

  /* ---------- config.yaml, generated from the state ---------- */
  const HEAD = { settings: 'settings: — ${settings.NAME} anywhere below', vaults: 'vaults: — where secrets live; values never appear here', connectors: 'connectors: — use: names the plugin', hosts: 'hosts: — remote machines over SSH', runtimes: 'runtimes: — where agents run', models: 'models: — fleets of acceptable models', sandbox: 'sandbox: — the agent sandbox', operators: 'operators: — who can approve', bots: 'bots: — persistent agents with their own memory', teams: 'teams: — bots inherit team defaults and can only narrow them', browser: 'browser: — the agent browser: which sites bots may use, and how they sign in', packs: 'packs: — your overrides; the pack files are vendored', update: 'update: — the auto-update policy', bot_links: 'bot_links: — operator-side links: each pair of bots may message each other', usage: 'usage: — off | subscription | api, per provider; limits only from source:', triggers: 'triggers: — on / filter / steps', workflows: 'workflows: — reusable, called with call:', secrets: 'secret references' };
  S.configText = (state) => {
    const st = state || window.D; const out = ['# conductor config — ops-box', '# managed by hand and by the dashboard; comments and unknown keys are kept', ''];
    const sec = (h, obj) => { if (obj == null || (typeof obj === 'object' && !Object.keys(obj).length)) return; out.push('# ' + HEAD[h]); out.push(Y.stringify({ [h]: obj })); out.push(''); };
    sec('settings', Object.fromEntries(st.settings.map((s) => [s.name, s.value])));
    sec('vaults', Object.fromEntries(st.vaults.map((v) => [v.name, v.cfg])));
    sec('connectors', Object.fromEntries(st.connectors.map((c) => [c.name, c.cfg])));
    sec('hosts', Object.fromEntries(st.hosts.map((h) => [h.name, h.cfg])));
    sec('runtimes', Object.fromEntries(st.runtimes.map((r) => [r.name, r.cfg])));
    sec('models', Object.fromEntries((st.fleets || []).map((f) => [f.name, f.cfg])));
    sec('sandbox', st.sandboxCfg);
    sec('operators', st.operatorsCfg);
    sec('teams', Object.fromEntries(st.teams.map((t) => [t.name, t.cfg])));
    sec('bots', Object.fromEntries(st.bots.map((m) => [m.name, m.cfg])));
    if ((st.botLinks || []).length) { out.push('# ' + HEAD.bot_links); out.push(Y.stringify({ bot_links: st.botLinks })); out.push(''); }
    const br = {};
    // the real shape: one allowlist, logins per site with the identities that may use them, evaluate per identity
    if (st.identities.length) {
      br.allow = [...new Set(st.identities.flatMap((i) => i.allow || []))];
      const sites = [...new Set(st.identities.flatMap((i) => i.logins || []))];
      if (sites.length) br.logins = sites.map((site) => ({ site, identities: st.identities.filter((i) => (i.logins || []).includes(site)).map((i) => i.id) }));
      const ev = st.identities.filter((i) => i.evaluate && i.evaluate !== 'off').map((i) => i.id);
      if (ev.length) br.evaluate = ev;
    }
    if (st.access.credentials.length) br.credentials = st.access.credentials.map((c) => Object.assign({ name: c.name }, c.cfg));
    sec('browser', br);
    sec('packs', Object.fromEntries(st.packs.map((p) => [p.name, p.cfg])));
    sec('usage', st.usageCfg);
    sec('update', st.updateCfg);
    const item = (y) => y.replace(/\n$/, '').split('\n').map((l, i) => (i === 0 ? '  - ' + l : l ? '    ' + l : l)).join('\n');
    const own = st.workflows.filter((w) => w.yaml && w.source === 'config');
    const trig = own.filter((w) => w.kind !== 'workflow'), wfs = own.filter((w) => w.kind === 'workflow');
    if (wfs.length) { out.push('# ' + HEAD.workflows); out.push('workflows:'); wfs.forEach((w) => { const y = w.yaml.replace(/^name: .*\n/, ''); out.push('  ' + w.name + ':'); out.push(y.replace(/\n$/, '').split('\n').map((l) => (l ? '    ' + l : l)).join('\n')); }); out.push(''); }
    if (trig.length) { out.push('# ' + HEAD.triggers); out.push('triggers:'); trig.forEach((w) => out.push(item(w.yaml))); out.push(''); }
    return out.join('\n');
  };
  /** Line where a section starts in configText (for "open in raw editor"). */
  S.lineOf = (text, needle) => { const i = text.split('\n').findIndex((l) => l.startsWith(needle + ':') || l.trim().startsWith(needle + ':')); return i + 1; };

  /* ---------- staging ---------- */
  let seq = 0;
  S.pending = () => window.D.pending;
  S.find = (coll, key) => window.D.pending.find((c) => c.coll === coll && c.key === key);
  /** The node as it will be once pending changes apply. */
  S.staged = (coll, key) => { const c = S.find(coll, key); return c ? clone(c.after) : C[coll].get(key); };
  S.stage = (coll, key, after, summary) => {
    const P = window.D.pending; const c = S.find(coll, key);
    const before = c ? c.before : C[coll].get(key);
    const norm = (v) => (v && typeof v === 'object' ? JSON.parse(JSON.stringify(v)) : v);
    after = norm(after);
    if (c) P.splice(P.indexOf(c), 1);
    if (eq(before, after)) { S.save(); return null; }
    const ch = { id: 'c' + Date.now().toString(36) + (seq++), coll, key, before: clone(before), after: clone(after), at: new Date().toISOString(), summary: summary || S.describe(coll, key, before, after) };
    P.push(ch); S.save();
    return ch;
  };
  S.unstage = (id) => { const P = window.D.pending; const i = P.findIndex((c) => c.id === id); if (i >= 0) P.splice(i, 1); S.save(); };
  S.discardAll = () => { window.D.pending.length = 0; S.save(); };
  S.describe = (coll, key, b, a) => {
    const p = C[coll].path(key);
    if (b == null) return `add ${p}`;
    if (a == null) return `remove ${p}`;
    if (typeof a !== 'object') return `${p}: ${String(b).split('\n')[0].slice(0, 40)} → ${String(a).split('\n')[0].slice(0, 40)}`;
    const ch = []; const walk = (x, y, pre) => { const ks = new Set([...Object.keys(x || {}), ...Object.keys(y || {})]); ks.forEach((k) => { const xa = x ? x[k] : undefined, ya = y ? y[k] : undefined; if (eq(xa, ya)) return; if (xa && ya && typeof xa === 'object' && typeof ya === 'object' && !Array.isArray(xa)) walk(xa, ya, pre + k + '.'); else ch.push(pre + k); }); };
    walk(b, a, '');
    return `${p}: ${ch.slice(0, 3).join(', ')}${ch.length > 3 ? ` +${ch.length - 3} more` : ''}`;
  };
  /** A copy of the state with the given changes applied. */
  /** What `conductor validate` on the copy prints for this state: the loader's own messages, exit status. */
  S.validateOutput = (v) => {
    const st = S.view(), tmp = '/tmp/conductor-copy-' + (window.D.daemon.config_sha || '7f3a').slice(0, 4);
    const lines = [`$ conductor validate --config ${tmp}/config.yaml`];
    v.warnings.forEach((w) => lines.push('warning: ' + w));
    if (v.errors.length) { v.errors.forEach((e) => lines.push('config: ' + e)); lines.push(`Error: ${v.errors.length} problem${v.errors.length > 1 ? 's' : ''} in config.yaml`, 'exit status 1'); }
    else lines.push(`config.yaml: ${st.connectors.length} connectors, ${st.workflows.filter((w) => w.source === 'config' && w.kind !== 'workflow').length} triggers, ${st.workflows.filter((w) => w.kind === 'workflow').length} workflows, ${st.packs.length} packs, ${st.bots.length} bots, ${st.teams.length} teams`, 'ok');
    return lines.join('\n');
  };
  S.candidate = (changes = window.D.pending) => {
    const real = window.D; const copy = clone(real);
    window.D = copy;
    try { changes.forEach((c) => C[c.coll].set(c.key, clone(c.after))); } finally { window.D = real; }
    return copy;
  };
  /** Every widening in the staged set, from the one classifier, each with its YAML path. */
  S.widening = (changes = window.D.pending) => changes.flatMap((c) => S.classify(c.coll, c.before, c.after).map((w) => ({ change: c, text: w, path: `${(C[c.coll].path || (() => c.coll))(c.key)}`, pack: c.coll === 'packs' })));
  S.validate = (changes = window.D.pending) => {
    const cand = S.candidate(changes);
    const errors = [], warnings = [];
    let text; try { text = S.configText(cand); Y.parse(text); } catch (e) { errors.push('config.yaml would not parse: ' + e.message); }
    changes.forEach((c) => {
      const coll = C[c.coll];
      if (c.after == null) { const refs = coll.refs ? coll.refs(c.key, cand) : []; if (refs.length) errors.push(`${coll.path(c.key)} is still referenced by ${refs.join(', ')}`); return; }
      if (coll.validate) errors.push(...coll.validate(c.key, c.after, cand));
      if (coll.warn) warnings.push(...coll.warn(c.key, c.after, cand));
    });
    return { errors, warnings, text };
  };

  /* ---------- apply / history ---------- */
  S.apply = (who = 'ops (dashboard)') => {
    const P = window.D.pending; if (!P.length) return null;
    const id = 'cfg-' + new Date().toISOString().slice(0, 16).replace(/[-:T]/g, '').replace(/^(\d{8})/, '$1-');
    P.forEach((c) => C[c.coll].set(c.key, clone(c.after)));
    window.D.configHistory.forEach((h) => (h.status = 'backup'));
    const summary = P.length === 1 ? P[0].summary : `${P.length} changes: ${P.map((c) => C[c.coll].path(c.key)).slice(0, 3).join(', ')}${P.length > 3 ? '…' : ''}`;
    const entry = { id, at: new Date().toISOString(), who, summary, status: 'live', changes: clone(P) };
    window.D.configHistory.unshift(entry);
    window.D.daemon.config_sha = Math.random().toString(16).slice(2, 10);
    window.D.audit.unshift({ ts: new Date().toISOString(), event: 'config_apply', who: 'operator:ops', detail: `${summary} · backup ${id} · validated on copy`, run: '' });
    P.length = 0; S.save();
    return entry;
  };
  /** Stage the inverse of entry `id` and everything applied after it. */
  S.restore = (id) => {
    const H = window.D.configHistory; const idx = H.findIndex((h) => h.id === id); if (idx < 0) return 0;
    let n = 0;
    H.slice(0, idx + 1).forEach((h) => (h.changes || []).slice().reverse().forEach((c) => { if (S.stage(c.coll, c.key, c.before, `restore ${C[c.coll].path(c.key)} from ${id}`)) n++; }));
    return n;
  };

  /* Seed history entries get real change records, so Restore works on them. */
  function seedHistory() {
    const H = window.D.configHistory; if (!H || H.every((h) => h.changes)) return;
    const ci = byName(window.D.workflows, 'ci-fixer'), ps = byName(window.D.bots, 'platform-sre'), pd = byName(window.D.connectors, 'pd'), rv = byName(window.D.packs, 'review');
    const get = (id) => byName(H, id, 'id');
    if (get('cfg-20261016-0912')) get('cfg-20261016-0912').changes = [{ coll: 'workflows', key: 'ci-fixer', before: ci.yaml.replace('max_cost_usd: 2.00 }   # raised from 1.50 on 2026-10-16', 'max_cost_usd: 1.50 }'), after: ci.yaml }];
    if (get('cfg-20261015-1412')) { const b = clone(ps.cfg); b.proactive.every = '6h'; get('cfg-20261015-1412').changes = [{ coll: 'bots', key: 'platform-sre', before: b, after: clone(ps.cfg) }]; }
    if (get('cfg-20261014-2205')) get('cfg-20261014-2205').changes = [{ coll: 'connectors', key: 'pd', before: null, after: clone(pd.cfg) }];
    if (get('cfg-20261013-1730')) { const b = clone(rv.cfg); delete b.steps; get('cfg-20261013-1730').changes = [{ coll: 'packs', key: 'review', before: b, after: clone(rv.cfg) }]; }
  }
  seedHistory();
  S.load();
})();
