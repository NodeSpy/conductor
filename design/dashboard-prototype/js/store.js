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
  const KEY = 'cd-proto-state-v2';
  const Y = window.YAML;
  const clone = Y.clone, eq = Y.eq;
  const SEED = clone(window.D);

  /* ---------- persistence ---------- */
  S.save = () => { try { localStorage.setItem(KEY, JSON.stringify(window.D)); } catch (e) {} };
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
  listColl('hosts', () => D().hosts, {
    label: 'host', href: () => '#/connectors/hosts',
    set(k, node) { const L = D().hosts; if (node == null) return drop(L, k); const it = byName(L, k); if (it) Object.assign(it, { cfg: node, addr: node.addr, user: node.user }); else L.push({ name: k, addr: node.addr, user: node.user, status: 'wait', rtt: '—', cfg: node }); },
    validate(k, n, all) { const e = []; if (!n.addr) e.push(`hosts.${k}.addr is required`); if (!n.user) e.push(`hosts.${k}.user is required`); return e; },
    refs(k, all) { return all.runtimes.filter((r) => r.cfg && r.cfg.host === k).map((r) => `runtimes.${r.name}.host`); },
  });
  listColl('vaults', () => D().vaults, {
    label: 'vault', href: () => '#/secrets',
    set(k, node) { const L = D().vaults; if (node == null) return drop(L, k); const it = byName(L, k); if (it) { it.cfg = node; it.type = node.type; if (it.name === 'hcv' && node.token && node.token !== 'env:VAULT_TOKEN') { it.status = 'ok'; it.detail = 'KV v2 · token from ' + node.token; } } else L.push({ name: k, type: node.type, status: 'ok', detail: `${node.type} · added from the dashboard`, writable: node.type !== 'onepassword', count: 0, cfg: node }); },
    validate(k, n) { return /^[a-z][a-z0-9-]*$/.test(k) ? [] : [`vault name "${k}": lowercase letters, digits and dashes`]; },
    refs(k, all) { return all.secrets.filter((s) => s.vault === k).map((s) => s.used[0] || s.name).filter(Boolean); },
  });
  C.settings = { section: 'settings', file: 'config.yaml', label: 'setting', href: () => '#/secrets',
    keys: () => D().settings.map((s) => s.name), get: (k) => { const s = byName(D().settings, k); return s ? s.value : null; },
    set: (k, v) => { if (v == null) drop(D().settings, k); else { const s = byName(D().settings, k); if (s) s.value = v, (s.chains = String(v).includes('${settings.')), (s.env = String(v).includes('${env.')); else D().settings.push({ name: k, value: v, used: 0, chains: String(v).includes('${settings.'), env: String(v).includes('${env.') }); } },
    path: (k) => `settings.${k}`,
    validate(k, v, all) { const e = []; if (!/^[a-z_][a-z0-9_]*$/.test(k)) e.push(`settings.${k}: names are lowercase snake_case`); const m = String(v).match(/\$\{settings\.(\w+)\}/g) || []; m.forEach((x) => { const n = x.slice(11, -1); if (!byName(all.settings, n)) e.push(`settings.${k}: \${settings.${n}} does not exist`); if (n === k) e.push(`settings.${k} refers to itself`); }); return e; },
    refs(k, all) { const s = byName(all.settings, k); return s && s.used ? [`${s.used} reference${s.used > 1 ? 's' : ''} in config`] : []; },
  };
  C.secrets = { section: 'secrets', file: 'config.yaml', label: 'secret reference', href: () => '#/secrets',
    keys: () => D().secrets.map((s) => s.name), get: (k) => { const s = byName(D().secrets, k); return s ? s.ref : null; },
    set: (k, ref) => { if (ref == null) return drop(D().secrets, k); const v = ref.startsWith('op://') ? 'op' : ref.startsWith('env:') ? 'env' : ref.split('/')[0]; const s = byName(D().secrets, k); if (s) { s.ref = ref; s.vault = v; } else D().secrets.push({ name: k, ref, vault: v, set: null, used: [] }); },
    path: (k) => { const s = byName(D().secrets, k); return (s && s.used[0]) || `secrets.${k}`; },
    validate(k, ref, all) { const v = ref.startsWith('op://') ? 'op' : ref.startsWith('env:') ? 'env' : ref.split('/')[0]; if (v === 'env') return []; const vv = byName(all.vaults, v); return !vv ? [`${k}: ref "${ref}" names no vault`] : vv.status === 'fail' ? [`${k}: vault ${v} is unreachable, so ${ref} can't be checked`] : []; },
    refs(k, all) { const s = byName(all.secrets, k); return s ? s.used : []; },
  };
  C.members = { section: 'members', file: 'config.yaml', label: 'member', href: (k) => `#/members/${k}/settings`,
    keys: () => D().members.map((m) => m.name), get: (k) => { const m = byName(D().members, k); return m ? clone(m.cfg) : null; },
    set(k, n) {
      const L = D().members; if (n == null) return drop(L, k);
      let m = byName(L, k);
      if (!m) { m = { name: k, color: ['#ff6a2b', '#4fb8e8', '#3fbf7f', '#e6b33c', '#a59fc4'][L.length % 5], home: 'idle', session: 's1', turns: 0, open: 0, pending: 0, workers: 0, spend: 0 }; L.push(m); D().chats[k] = D().chats[k] || []; }
      Object.assign(m, { cfg: n, desc: n.description || '', shadow: !!(n.autonomy && n.autonomy.shadow), mode: (n.isolation && n.isolation.mode) || 'namespace', operators: n.operators || [], peers: n.peers || [], maxTurns: S.getPath(n, 'home.rotate.max_turns') || 100, maxAge: S.getPath(n, 'home.rotate.max_age') || '24h', age: m.age || '0m', sandboxed: true,
        proactive: n.proactive ? { every: n.proactive.every, next: (m.proactive && m.proactive.next) || n.proactive.every, quiet: n.proactive.quiet_hours, budget: `0/${S.getPath(n, 'proactive.budget.max_runs_per_day') || '∞'} runs` } : null });
    },
    path: (k) => `members.${k}`,
    validate(k, n, all) {
      const e = [];
      if (!/^[a-z][a-z0-9-]*$/.test(k)) e.push(`member name "${k}": lowercase letters, digits and dashes`);
      if (!n.operators || !n.operators.length) e.push(`members.${k}.operators: at least one operator must be able to approve`);
      (n.operators || []).forEach((o) => { if (!all.operatorsCfg[o]) e.push(`members.${k}.operators: "${o}" is not in operators:`); });
      (n.peers || []).forEach((p) => { if (p === k) e.push(`members.${k}.peers: a member can't peer with itself`); else if (!byName(all.members, p)) e.push(`members.${k}.peers: no member named "${p}"`); });
      const tiers = S.getPath(n, 'autonomy.tiers') || {}; const seen = {};
      Object.entries(tiers).forEach(([t, vs]) => (vs || []).forEach((v) => { if (seen[v]) e.push(`members.${k}.autonomy.tiers: ${v} is in both ${seen[v]} and ${t}`); seen[v] = t; }));
      (S.getPath(n, 'autonomy.promote') || []).forEach((v) => { if (seen[v] !== 'reversible') e.push(`members.${k}.autonomy.promote: ${v} must be a reversible-tier verb to be promoted`); });
      if (n.isolation && n.isolation.mode === 'none') e.push(`members.${k}.isolation: mode none is set on a runtime, workflow or trigger, never on a member`);
      if (n.worker && n.worker.runtime && !byName(all.runtimes, n.worker.runtime)) e.push(`members.${k}.worker.runtime: no runtime "${n.worker.runtime}"`);
      return e;
    },
    warn(k, n, all) { const w = []; (n.peers || []).forEach((p) => { const o = byName(all.members, p); if (o && !((o.cfg && o.cfg.peers) || []).includes(k)) w.push(`members.${k}.peers: ${p} doesn't list ${k} back, so the link stays closed until it does`); }); if (!S.getPath(n, 'autonomy.shadow') && !all.members.find((m) => m.name === k)) w.push(`members.${k}: a new member normally starts in shadow mode`); return w; },
    widens(b, a) {
      const r = [], B = b || {}, A = a || {};
      const bv = new Set(S.getPath(B, 'skill.verbs') || []); (S.getPath(A, 'skill.verbs') || []).forEach((v) => { if (!bv.has(v)) r.push(`adds verb ${v}`); });
      const order = ['never', 'propose', 'commit_money', 'reversible', 'observe'];
      const tierOf = (n, v) => Object.entries(S.getPath(n, 'autonomy.tiers') || {}).find(([, vs]) => (vs || []).includes(v));
      (S.getPath(A, 'skill.verbs') || []).forEach((v) => { const tb = tierOf(B, v), ta = tierOf(A, v); const ib = order.indexOf(tb ? tb[0] : 'propose'), ia = order.indexOf(ta ? ta[0] : 'propose'); if (bv.has(v) && ia > ib) r.push(`loosens ${v}: ${tb ? tb[0] : 'propose'} → ${ta[0]}`); });
      const bp = new Set(S.getPath(B, 'autonomy.promote') || []); (S.getPath(A, 'autonomy.promote') || []).forEach((v) => { if (!bp.has(v)) r.push(`promotes ${v}`); });
      if (b && S.getPath(B, 'autonomy.shadow') && !S.getPath(A, 'autonomy.shadow')) r.push('leaves shadow mode: proposals become approvable');
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
  C.credentials = { section: 'browser', file: 'config.yaml', label: 'credential binding', href: () => '#/members/platform-sre/access',
    keys: () => D().access.credentials.map((c) => c.name), get: (k) => { const c = byName(D().access.credentials, k); return c ? clone(c.cfg) : null; },
    set: (k, n) => { const L = D().access.credentials; if (n == null) return drop(L, k); const c = byName(L, k); const patch = { cfg: n, origin: n.origin, identity: n.identity, fields: (n.fields || []).join(', '), vault: n.vault, ref: n.ref }; if (c) Object.assign(c, patch); else L.push(Object.assign({ name: k }, patch)); },
    path: (k) => `browser.credentials.${k}`,
    validate(k, n, all) { const e = []; if (!/^https:\/\//.test(n.origin || '')) e.push(`browser.credentials.${k}.origin must be an https:// origin`); const v = byName(all.vaults, n.vault); if (!v) e.push(`browser.credentials.${k}.vault: no vault "${n.vault}"`); else if (v.status === 'fail') e.push(`browser.credentials.${k}: vault ${n.vault} is unreachable`); if (!(n.fields || []).length) e.push(`browser.credentials.${k}.fields: pick at least one field`); return e; },
    widens: (b, a) => (a && !b ? [`binds credential to ${a.identity}`] : a && b && a.identity !== b.identity ? [`rebinds credential to ${a.identity}`] : []),
  };
  C.packs = { section: 'packs', file: 'config.yaml', label: 'pack', href: () => '#/extensions/packs',
    keys: () => D().packs.map((p) => p.name), get: (k) => { const p = byName(D().packs, k); return p ? clone(p.cfg) : null; },
    set(k, n) { const L = D().packs; if (n == null) return drop(L, k); const p = byName(L, k); const ver = (n.use.split('@')[1] || '').replace('^', ''); const ov = Object.keys(n.steps || {}).length + Object.keys(n.triggers || {}).length; if (p) Object.assign(p, { cfg: n, use: n.use.split('@')[0], hold: !!n.hold, overrides: ov, ver: n.x_version || p.ver, update: n.x_version && n.x_version === p.update ? null : p.update, sha: n.x_version && n.x_version !== p.ver ? '0e47d12' : p.sha }); else L.push({ name: k, use: n.use.split('@')[0], ver: ver || '1.0.0', sha: 'b51d0a2', status: 'ok', official: n.use.startsWith('conductor-packs/'), hold: !!n.hold, triggers: '0 armed / 1 · arm one to start', overrides: ov, cfg: n }); },
    path: (k) => `packs.${k}`,
    validate(k, n, all) { const e = []; if (!n.use) e.push(`packs.${k}.use is required`); else if (!/^(conductor-packs\/|\.\/|acme\/)/.test(n.use)) e.push(`packs.${k}.use: ${n.use.split('@')[0]} is not allowed by pack_trust (add a pack_trust source first)`); Object.values(n.steps || {}).forEach((o) => { if (o && o.isolation && o.isolation.mode === 'none') {} }); Object.entries(n.connectors || {}).forEach(([role, inst]) => { if (!byName(all.connectors, inst)) e.push(`packs.${k}.connectors.${role}: no connector "${inst}"`); }); return e; },
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
  single('update', () => D().updateCfg, (n) => { D().updateCfg = n; D().daemon.update.auto = n.auto; D().daemon.update.interval = n.interval; }, { label: 'update policy', href: () => '#/updates',
    validate: (k, n) => (/^\d+[smh]$/.test(n.interval || '') ? [] : [`update.interval: "${n.interval}" is not a duration (e.g. 10m)`]) });
  C.operators = { section: 'operators', file: 'config.yaml', label: 'operator', href: () => '#/security',
    keys: () => Object.keys(D().operatorsCfg), get: (k) => clone(D().operatorsCfg[k] || null), set: (k, n) => { if (n == null) delete D().operatorsCfg[k]; else D().operatorsCfg[k] = n; }, path: (k) => `operators.${k}` };

  S.ORDER = ['settings', 'vaults', 'secrets', 'connectors', 'hosts', 'runtimes', 'sandbox', 'operators', 'members', 'identities', 'credentials', 'packs', 'update', 'workflows'];

  /* ---------- config.yaml, generated from the state ---------- */
  const HEAD = { settings: 'settings: — ${settings.NAME} anywhere below', vaults: 'vaults: — where secrets live; values never appear here', connectors: 'connectors: — use: names the plugin', hosts: 'hosts: — remote machines over SSH', runtimes: 'runtimes: — where agents run', sandbox: 'sandbox: — the agent sandbox', operators: 'operators: — who can approve', members: 'members: — persistent agents (#178)', browser: 'browser: — the agent browser (#182)', packs: 'packs: — your overrides; the pack files are vendored', update: 'update: — the auto-update policy', triggers: 'triggers: — on / filter / steps', workflows: 'workflows: — reusable, called with call:', secrets: 'secret references' };
  S.configText = (state) => {
    const st = state || window.D; const out = ['# conductor config — ops-box', '# managed by hand and by the dashboard; comments and unknown keys are kept', ''];
    const sec = (h, obj) => { if (obj == null || (typeof obj === 'object' && !Object.keys(obj).length)) return; out.push('# ' + HEAD[h]); out.push(Y.stringify({ [h]: obj })); out.push(''); };
    sec('settings', Object.fromEntries(st.settings.map((s) => [s.name, s.value])));
    sec('vaults', Object.fromEntries(st.vaults.map((v) => [v.name, v.cfg])));
    sec('connectors', Object.fromEntries(st.connectors.map((c) => [c.name, c.cfg])));
    sec('hosts', Object.fromEntries(st.hosts.map((h) => [h.name, h.cfg])));
    sec('runtimes', Object.fromEntries(st.runtimes.map((r) => [r.name, r.cfg])));
    sec('sandbox', st.sandboxCfg);
    sec('operators', st.operatorsCfg);
    sec('members', Object.fromEntries(st.members.map((m) => [m.name, m.cfg])));
    const br = {};
    if (st.identities.length) br.identities = Object.fromEntries(st.identities.map((i) => [i.id, { allow: i.allow, logins: i.logins, evaluate: i.evaluate }]));
    if (st.access.credentials.length) br.credentials = Object.fromEntries(st.access.credentials.map((c) => [c.name, c.cfg]));
    sec('browser', br);
    sec('packs', Object.fromEntries(st.packs.map((p) => [p.name, p.cfg])));
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
  S.candidate = (changes = window.D.pending) => {
    const real = window.D; const copy = clone(real);
    window.D = copy;
    try { changes.forEach((c) => C[c.coll].set(c.key, clone(c.after))); } finally { window.D = real; }
    return copy;
  };
  S.widening = (changes = window.D.pending) => changes.flatMap((c) => (C[c.coll].widens ? C[c.coll].widens(c.before, c.after) : []).map((w) => ({ change: c, text: w })));
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
    const ci = byName(window.D.workflows, 'ci-fixer'), ps = byName(window.D.members, 'platform-sre'), pd = byName(window.D.connectors, 'pd'), rv = byName(window.D.packs, 'review');
    const get = (id) => byName(H, id, 'id');
    if (get('cfg-20261016-0912')) get('cfg-20261016-0912').changes = [{ coll: 'workflows', key: 'ci-fixer', before: ci.yaml.replace('max_cost_usd: 2.00 }   # raised from 1.50 on 2026-10-16', 'max_cost_usd: 1.50 }'), after: ci.yaml }];
    if (get('cfg-20261015-1412')) { const b = clone(ps.cfg); b.proactive.every = '6h'; get('cfg-20261015-1412').changes = [{ coll: 'members', key: 'platform-sre', before: b, after: clone(ps.cfg) }]; }
    if (get('cfg-20261014-2205')) get('cfg-20261014-2205').changes = [{ coll: 'connectors', key: 'pd', before: null, after: clone(pd.cfg) }];
    if (get('cfg-20261013-1730')) { const b = clone(rv.cfg); delete b.steps; get('cfg-20261013-1730').changes = [{ coll: 'packs', key: 'review', before: b, after: clone(rv.cfg) }]; }
  }
  seedHistory();
  S.load();
})();
