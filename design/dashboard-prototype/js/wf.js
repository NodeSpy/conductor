/* Workflow model for the builder: how conductor workflows really work.
 * A trigger has `on:` (one or several sources), optional `filter:`, and an
 * ordered `steps:` list. A `parallel: [[…],[…]]` step runs its branches at
 * once and joins before the next step; there is no `needs:` graph. Steps see
 * the trigger's fields at the top level (`{{.repo}}`) and every earlier step's
 * outputs by id (`{{.triage.kind}}`), Go-template style.
 *
 * WF.walk / WF.validate / WF.render / WF.test / WF.scope are pure functions
 * over the parsed YAML value; the builder owns the UI. */
(function () {
  const WF = (window.WF = {});
  const Y = window.YAML;
  const isObj = (v) => v && typeof v === 'object' && !Array.isArray(v);
  const fleetNames = () => ((V().fleets || []).map((f) => f.name)).concat(window.SCHEMA.fleets);
  const V = () => (window.S && window.S.view ? window.S.view() : window.D); // staged view: a just-created connector resolves

  /* ---------- kinds ---------- */
  WF.KINDS = {
    agent: { icon: 'sparkle', label: 'Agent', color: 'var(--accent)' }, code: { icon: 'code', label: 'Code', color: 'var(--ok)' },
    decide: { icon: 'diamond', label: 'Decide', color: 'var(--shadow)' }, ask: { icon: 'hand', label: 'Hand-off', color: 'var(--wait)' },
    verb: { icon: 'zap', label: 'Action', color: 'var(--run)' }, call: { icon: 'link', label: 'Call', color: 'var(--ink-2)' },
    wait: { icon: 'clock', label: 'Wait', color: 'var(--ink-3)' }, log: { icon: 'logs', label: 'Log', color: 'var(--ink-3)' },
    set: { icon: 'edit', label: 'Set', color: 'var(--ink-3)' }, assert: { icon: 'check', label: 'Assert', color: 'var(--ink-3)' },
    fail: { icon: 'x', label: 'Fail', color: 'var(--fail)' }, bot: { icon: 'members', label: 'Bot', color: 'var(--accent-2)' }, parallel: { icon: 'workflows', label: 'Parallel', color: 'var(--run)' }, unknown: { icon: 'warn', label: 'Unknown', color: 'var(--fail)' },
  };
  WF.kind = (s) => {
    if (!isObj(s)) return 'unknown';
    if (Array.isArray(s.parallel)) return 'parallel';
    if (s.bot) return 'bot';
    if (s.uses) { const v = window.SCHEMA.verb(s.uses); return v && v.ask ? 'ask' : 'verb'; }
    if (s.decide) return 'decide';
    if (s.call) return 'call';
    if ('sleep' in s || s.wait_for) return 'wait';
    if ('log' in s) return 'log';
    if (s.set) return 'set';
    if ('assert' in s) return 'assert';
    if ('fail' in s) return 'fail';
    if (s.code != null || s.command || s.use || s.run || s.type === 'command') return 'code';
    if (s.type === 'agent' || 'prompt' in s || s.agent || s['<<']) return 'agent';
    return 'unknown';
  };
  WF.summary = (s) => {
    const k = WF.kind(s);
    const t = (x, n = 60) => { x = String(x == null ? '' : x).replace(/\s+/g, ' ').trim(); return x.length > n ? x.slice(0, n - 1) + '…' : x; };
    switch (k) {
      case 'verb': case 'ask': { const v = window.SCHEMA.verb(s.uses); return `${s.uses}${v ? ' · ' + v.label : ''}`; }
      case 'agent': return `${s.runtime || 'default runtime'} · ${s.model || (s['<<'] ? 'from ' + s['<<'] : 'bare launch')}${s.prompt ? ' · “' + t(s.prompt, 42) + '”' : ''}`;
      case 'code': return s.command ? `${s.use || s.run || 'cli'} · ${[].concat(s.command).join(' ')}` : `${s.use || s.run} · ${t((s.code || '').split('\n')[0], 50)}`;
      case 'decide': return `${s.model || 'light'} · ${Object.keys((s.decide && s.decide.questions) || {}).join(', ') || 'no questions'}`;
      case 'call': return `call ${s.call}${s.with ? ' · ' + Object.keys(s.with).length + ' inputs' : ''}`;
      case 'wait': return s.wait_for ? `wait for ${s.wait_for.uses || '…'} until ${s.wait_for.until || '…'}` : `sleep ${s.sleep}`;
      case 'log': return `log “${t(s.log, 50)}”`;
      case 'set': return `set ${Object.keys(s.set || {}).join(', ')}`;
      case 'assert': return `assert ${t(s.assert, 50)}`;
      case 'fail': return `fail “${t(s.fail, 50)}”`;
      case 'parallel': return `${s.parallel.length} branches, joined before the next step`;
      case 'bot': return typeof s.bot === 'string' ? `to bot ${s.bot}${s.title ? ' · “' + t(s.title, 40) + '”' : ''}` : `to team ${s.bot.team} · its rules, else the lead`;
      default: return 'no step type: add uses:, type: agent, use:, decide:, call: …';
    }
  };
  WF.idOf = (s, i) => (isObj(s) && s.id) || `${WF.kind(s)}#${i + 1}`;

  /** Walk every step with its YAML meta path and location. */
  WF.walk = (value) => {
    const out = [];
    const rec = (list, base, loc, branch) => (list || []).forEach((s, i) => {
      const seg = isObj(s) && typeof s.id === 'string' && s.id ? `[id=${s.id}]` : i;
      const path = `${base}.${seg}`;
      const e = { step: s, path, list: loc, index: i, branch, kind: WF.kind(s), id: WF.idOf(s, out.length), order: out.length };
      out.push(e);
      if (e.kind === 'parallel') s.parallel.forEach((br, b) => rec(br, `${path}.parallel.${b}`, loc.concat([i, 'parallel', b]), { parent: e, b }));
    });
    rec(value && value.steps, 'steps', ['steps'], null);
    return out;
  };
  WF.at = (value, loc) => loc.reduce((a, k) => (a == null ? a : a[k]), value);

  /** Steps whose outputs are visible to `entry`: every earlier top-level step
   * (a parallel group contributes all its branches), and earlier steps in the
   * same branch. */
  WF.before = (value, entry) => {
    const all = WF.walk(value); const vis = [];
    const top = (e) => { let x = e; while (x.branch) x = x.branch.parent; return x; };
    const me = entry; const myTop = me ? top(me) : null;
    all.forEach((e) => {
      if (e === me || e.kind === 'parallel') return;
      const t = top(e);
      if (myTop && t.order < myTop.order) vis.push(e);
      else if (me && me.branch && e.branch && e.branch.parent === me.branch.parent && e.branch.b === me.branch.b && e.order < me.order) vis.push(e);
      else if (!me) vis.push(e);
    });
    return vis;
  };

  /* ---------- trigger sources ---------- */
  WF.sources = (v) => { const on = v && v.on; if (!on) return []; return (Array.isArray(on) ? on : [on]).map((x) => (typeof x === 'string' ? { on: x } : { on: Object.keys(x)[0], block: x[Object.keys(x)[0]] })); };
  WF.eventFields = (v) => {
    const f = {};
    WF.sources(v).forEach((s) => { const e = window.SCHEMA.event(s.on); if (e) Object.assign(f, Y.clone(e.fields)); else if (/^conductor\./.test(s.on)) Object.assign(f, { kind: 'failed', workflow: 'ci-fixer', url: 'https://ops-box.tail0000.ts.net/runs/r-7ef0', error: 'step test failed', failed_step: 'test' }); });
    if (v && v.inputs) f.inputs = Object.fromEntries(Object.entries(v.inputs).map(([k, d]) => [k, d && d.default != null ? d.default : d && d.type === 'integer' ? 0 : '']));
    if (v && v.group) f.group = { count: 2, events: [] };
    return f;
  };

  /* ---------- declared outputs ---------- */
  WF.outputsOf = (s) => {
    const k = WF.kind(s);
    if (k === 'verb' || k === 'ask') { const v = window.SCHEMA.verb(s.uses); return v ? Y.clone(v.outputs) : null; }
    if (k === 'agent') { const o = { text: '(the agent\'s final message)', diff: '(its proposed change)', cost_usd: 0.4 }; const props = s.output_schema && s.output_schema.properties; if (props) Object.keys(props).forEach((p) => (o[p] = props[p].enum ? props[p].enum[0] : '…')); return o; }
    if (k === 'decide') { const o = { _by: 'light' }; Object.entries((s.decide && s.decide.questions) || {}).forEach(([q, d]) => (o[q] = (d && d.type) === 'choice' ? { choice: (d.choices || [])[0] || '' } : (d && d.type) === 'score' ? { score: 0.5 } : { noul: 0.5 })); Object.entries((s.decide && s.decide.questions) || {}).forEach(([q, d]) => { if ((d && d.type) === 'choice') o[q] = (d.choices || [])[0] || ''; }); return o; }
    if (k === 'set') return Y.clone(s.set);
    if (k === 'call') { const w = (V().workflows || []).find((x) => x.name === s.call); if (!w || !w.yaml) return null; try { return Y.clone(Y.parse(w.yaml).value.outputs || {}); } catch (e) { return null; } }
    if (k === 'wait' && s.wait_for) { const v = window.SCHEMA.verb(s.wait_for.uses); return v ? Y.clone(v.outputs) : null; }
    if (k === 'code') return null; // known only after a Test
    if (k === 'bot') return { case: 'c41', routed_to: typeof s.bot === 'string' ? s.bot : 'lead', by: typeof s.bot === 'string' ? 'direct' : 'rule' };
    return {};
  };

  /* ---------- references ---------- */
  const REF = /\{\{-?\s*([^}]*?)\s*-?\}\}/g;
  WF.refsIn = (str) => {
    const out = []; if (typeof str !== 'string') return out;
    let m; REF.lastIndex = 0;
    while ((m = REF.exec(str))) { const expr = m[1]; const rs = expr.match(/(^|[\s(|])\.([A-Za-z_][\w-]*(?:\.[A-Za-z_][\w-]*)*)/g) || []; rs.forEach((r) => out.push(r.trim().replace(/^[(|]\s*/, '').slice(1))); }
    return out;
  };
  const bareRefs = (expr) => (String(expr).replace(REF, ' ').match(/\b([a-z_][\w-]*)\.([\w.]+)/gi) || []).filter((x) => !/^\d/.test(x));
  const strings = (o, pre = '', acc = []) => { if (typeof o === 'string') acc.push([pre, o]); else if (Array.isArray(o)) o.forEach((x, i) => strings(x, pre + '.' + i, acc)); else if (isObj(o)) Object.entries(o).forEach(([k, v]) => { if (k !== 'parallel') strings(v, pre ? pre + '.' + k : k, acc); }); return acc; };

  /* ---------- validation ---------- */
  WF.validate = (yaml, opts = {}) => {
    const out = [];
    let p; try { p = Y.parse(yaml); } catch (e) { return [{ level: 'error', line: e.line || 1, msg: e.msg || e.message }]; }
    const v = p.value, meta = p.meta;
    const lineOf = (path) => { let q = path; while (q) { if (meta[q] && meta[q].line) return meta[q].line; q = q.includes('.') ? q.slice(0, q.lastIndexOf('.')) : ''; } return 1; };
    const add = (level, path, msg, extra = {}) => out.push(Object.assign({ level, line: lineOf(path), msg, path }, extra));
    if (!isObj(v)) { add('error', '', 'a workflow is a map with on: and steps:'); return out; }
    const reusable = !!v.inputs || opts.reusable;
    if (!v.on && !reusable) add('error', 'name', 'on: is missing: choose what starts this workflow', { field: 'on', trigger: true });
    WF.sources(v).forEach((s, i) => {
      const on = s.on, path = Array.isArray(v.on) ? `on.${i}` : 'on';
      if (on === 'manual' || /^timer\.[\w-]+$/.test(on) || /^conductor\.[\w_]+$/.test(on)) return;
      const [inst, ev] = String(on).split('.');
      const c = V().connectors.find((x) => x.name === inst);
      if (!c) { add('error', path, `on: ${on}: no connector named "${inst}"`, { trigger: true }); return; }
      const decl = window.SCHEMA.connectors[c.use];
      if (!decl || !decl.events[ev]) add('error', path, `on: ${on}: the ${c.use} plugin declares no "${ev}" event`, { trigger: true });
    });
    if (v.filter && isObj(v.filter)) {
      const allowed = new Set(); WF.sources(v).forEach((s) => { const e = window.SCHEMA.event(s.on); (e && e.filters || []).forEach((k) => allowed.add(k)); });
      Object.keys(v.filter).forEach((k) => { const base = k.replace(/^not_/, ''); if (allowed.size && !allowed.has(k) && !allowed.has(base)) add('error', `filter.${k}`, `filter: "${k}" isn't in the event's schema (${[...allowed].join(', ')})`, { trigger: true, field: 'filter' }); });
    }
    if (!Array.isArray(v.steps)) { if (v.steps != null) add('error', 'steps', 'steps: must be a list'); else add('warn', 'name', 'no steps yet: add one with +'); return out; }
    if (!v.steps.length) add('warn', 'steps', 'no steps yet: add one with +');
    const all = WF.walk(v), ids = {};
    const fields = WF.eventFields(v);
    all.forEach((e) => {
      const s = e.step, P = e.path;
      if (!isObj(s)) { add('error', P, 'a step is a map'); return; }
      if (s.id != null) {
        if (typeof s.id !== 'string' || !/^[a-z][\w-]*$/i.test(s.id)) add('error', P + '.id', `id "${s.id}": letters, digits, _ and -`, { step: e.id, field: 'id' });
        else if (ids[s.id]) add('error', P + '.id', `duplicate id "${s.id}" (also step ${ids[s.id]})`, { step: e.id, field: 'id' });
        else ids[s.id] = e.order + 1;
      }
      const err = (msg, field, level = 'error') => add(level, field ? `${P}.${field}` : P, msg, { step: e.id, field });
      switch (e.kind) {
        case 'unknown': err('this step has no type: add uses:, type: agent, use:, decide:, call:, log:, sleep: …'); break;
        case 'verb': case 'ask': {
          const [inst, verb] = String(s.uses).split('.');
          const c = V().connectors.find((x) => x.name === inst);
          const decl = window.SCHEMA.verb(s.uses);
          if (!c && !['git', 'kv'].includes(inst)) { const reg = window.SCHEMA.connectors[inst]; err(reg ? `uses: ${s.uses}: no "${inst}" connector yet — add one (${reg.registry ? 'installs ' + reg.registry : 'builtin'}) in Connectors` : `uses: ${s.uses}: no connector named "${inst}"`, 'uses'); break; }
          if (!decl) { err(`uses: ${s.uses}: ${c ? c.use : inst} declares no verb "${verb}"`, 'uses'); break; }
          const opts = s.options || {};
          decl.fields.forEach((f) => { if (f.req && f.def == null && (opts[f.k] == null || opts[f.k] === '' || (Array.isArray(opts[f.k]) && !opts[f.k].length))) err(`options.${f.k} is required by ${s.uses}`, `options.${f.k}`); if (f.type === 'enum' && opts[f.k] != null && !String(opts[f.k]).includes('{{') && !f.options.includes(opts[f.k])) err(`options.${f.k}: "${opts[f.k]}" is not one of ${f.options.join(', ')}`, `options.${f.k}`); });
          Object.keys(opts).forEach((k) => { if (!decl.fields.find((f) => f.k === k)) err(`options.${k}: ${s.uses} has no option "${k}" (typo?)`, `options.${k}`); });
          break;
        }
        case 'agent': {
          if (!s.prompt && !s.prompt_file && !s['<<'] && !s.agent) err('prompt: is required: say what the agent should do', 'prompt');
          if (s.model != null && typeof s.model === 'string') { const m = s.model; if (!(fleetNames().includes(m) || window.SCHEMA.models.includes(m) || /[*]/.test(m))) err(`model "${m}": no fleet in models: and no provider offers it`, 'model'); }
          if (s.runtime && !V().runtimes.find((r) => r.name === s.runtime)) err(`runtime "${s.runtime}": no runtime by that name`, 'runtime');
          if (s.isolation && s.isolation.mode === 'none') err('isolation: mode none runs this agent unsandboxed, as you (widening: applying asks for your passkey)', 'isolation.mode', 'warn');
          (S.getPath(s, 'skill.verbs') || []).forEach((vb) => { if (!/\*$/.test(vb) && !window.SCHEMA.verb(vb)) err(`skill.verbs: ${vb} is not a known verb`, 'skill.verbs'); });
          if (s.handoff && !V().connectors.find((c) => c.name === s.handoff)) err(`handoff: no connector "${s.handoff}"`, 'handoff');
          break;
        }
        case 'code': {
          const eng = s.use || s.run || (s.type === 'command' ? 'cli' : '');
          if (s.use && s.run) err('set use: or run:, never both', 'use');
          if (eng && !window.SCHEMA.engines.find(([k]) => k === eng)) err(`use: "${eng}" is not an engine (${window.SCHEMA.engines.map(([k]) => k).join(', ')})`, 'use');
          if (eng === 'cli' || s.type === 'command') { if (!s.command || ![].concat(s.command).length) err('command: is required for use: cli', 'command'); }
          else if (!s.code || !String(s.code).trim()) err('code: is empty', 'code');
          break;
        }
        case 'decide': { const q = (s.decide && s.decide.questions) || {}; if (!Object.keys(q).length) err('decide.questions: ask at least one question', 'decide.questions'); Object.entries(q).forEach(([n, d]) => { if (d && d.type === 'choice' && !(d.choices || []).length) err(`decide.questions.${n}: a choice question needs choices`, 'decide.questions'); }); break; }
        case 'call': {
          const w = V().workflows.find((x) => x.name === s.call && x.kind === 'workflow');
          if (!w) { err(`call: no reusable workflow "${s.call}"`, 'call'); break; }
          let ins = {}; try { ins = Y.parse(w.yaml).value.inputs || {}; } catch (x) {}
          Object.entries(ins).forEach(([k, d]) => { if (d && d.required && !(s.with && s.with[k] != null && s.with[k] !== '')) err(`with.${k} is required by ${s.call}`, `with.${k}`); });
          Object.keys(s.with || {}).forEach((k) => { if (!(k in ins)) err(`with.${k}: ${s.call} has no input "${k}"`, `with.${k}`); });
          break;
        }
        case 'wait': if ('sleep' in s && !/^\d+(ms|s|m|h)$/.test(String(s.sleep))) err(`sleep: "${s.sleep}" is not a positive duration`, 'sleep'); break;
        case 'bot': if (typeof s.bot === 'string') { if (!V().bots.find((b) => b.name === s.bot)) err(`bot: no bot named "${s.bot}"`, 'bot'); } else if (!s.bot.team || !V().teams.find((x) => x.name === s.bot.team)) err(`bot.team: no team named "${s.bot.team || ''}"`, 'bot.team'); if (!s.title) err('title: is required: it becomes the case title', 'title'); break;
        case 'parallel': if (s.parallel.length < 2) err('parallel: needs at least two branches', 'parallel', 'warn'); s.parallel.forEach((br, b) => { if (!Array.isArray(br) || !br.length) err(`parallel branch ${b + 1} is empty`, 'parallel'); }); break;
        default:
      }
      // references
      const vis = WF.before(v, e); const visIds = new Set(vis.map((x) => x.step && x.step.id).filter(Boolean));
      const later = new Set(all.filter((x) => x.order > e.order && x.step && x.step.id).map((x) => x.step.id));
      const sibling = new Set(all.filter((x) => x.branch && e.branch && x.branch.parent === e.branch.parent && x.branch.b !== e.branch.b && x.step && x.step.id).map((x) => x.step.id));
      const scopeOk = (head) => head in fields || ['inputs', 'item', 'index', 'group', 'error', 'failed_step', 'gate', 'vars', 'options'].includes(head) || visIds.has(head);
      const check = (ref, field) => {
        const [head, sub] = ref.split('.');
        if (scopeOk(head)) {
          const src = vis.find((x) => x.step && x.step.id === head);
          if (src && sub) { const o = WF.outputsOf(src.step); if (o && Object.keys(o).length && !(sub in o)) err(`{{.${ref}}}: ${head} has no output "${sub}" (it has ${Object.keys(o).join(', ')})`, field, 'warn'); }
          return;
        }
        if (s.id === head) err(`{{.${ref}}}: a step can't read its own outputs`, field);
        else if (sibling.has(head)) err(`{{.${ref}}}: ${head} runs in a parallel branch beside this one; its outputs exist after the join`, field);
        else if (later.has(head)) err(`{{.${ref}}}: ${head} runs later, so it has no outputs yet`, field);
        else err(`{{.${ref}}}: no earlier step or event field named "${head}"`, field);
      };
      strings(Object.fromEntries(Object.entries(s).filter(([k]) => k !== 'parallel'))).forEach(([field, str]) => WF.refsIn(str).forEach((r) => check(r, field)));
      if (typeof s.if === 'string') bareRefs(s.if).forEach((r) => { if (!/^(true|false)$/.test(r)) check(r, 'if'); });
    });
    return out;
  };

  /* ---------- rendering ---------- */
  const get = (o, path) => path.split('.').reduce((a, k) => (a == null ? undefined : a[k]), o);
  WF.render = (str, scope, miss = []) => {
    if (typeof str !== 'string') return str;
    const sole = str.match(/^\{\{\s*\.([\w.-]+)\s*\}\}$/);
    if (sole) { const v = get(scope, sole[1]); if (v === undefined) miss.push(sole[1]); return v === undefined ? '<no value>' : v; }
    return str.replace(REF, (m, expr) => {
      if (/^vault\s/.test(expr)) return '••••••••';
      const dm = expr.match(/^\.([\w.-]+)\s*\|\s*default\s+"([^"]*)"$/);
      if (dm) { const v = get(scope, dm[1]); return v == null || v === '' ? dm[2] : typeof v === 'object' ? JSON.stringify(v) : String(v); }
      const pm = expr.match(/^\.([\w.-]+)(\s*\|\s*json)?$/);
      if (pm) { const v = get(scope, pm[1]); if (v === undefined) { miss.push(pm[1]); return '<no value>'; } return typeof v === 'object' ? JSON.stringify(v) : String(v); }
      return m;
    });
  };
  const renderDeep = (o, scope, miss) => (typeof o === 'string' ? WF.render(o, scope, miss) : Array.isArray(o) ? o.map((x) => renderDeep(x, scope, miss)) : isObj(o) ? Object.fromEntries(Object.entries(o).map(([k, v]) => [k, renderDeep(v, scope, miss)])) : o);
  /** if:/assert: — templates render first, then a tiny comparison grammar. */
  WF.cond = (expr, scope) => {
    let e = String(WF.render(String(expr), scope));
    e = e.replace(/\b([a-z_][\w-]*(?:\.[\w-]+)+)\b/gi, (m) => { const v = get(scope, m); return v === undefined ? m : JSON.stringify(v); });
    const val = (t) => { t = t.trim(); if (/^-?\d+(\.\d+)?$/.test(t)) return +t; if (/^(true|false)$/.test(t)) return t === 'true'; if (/^".*"$|^'.*'$/.test(t)) { try { return JSON.parse(t.replace(/^'|'$/g, '"')); } catch (x) { return t.slice(1, -1); } } return t; };
    const one = (c) => { c = c.trim(); let neg = false; while (c.startsWith('!')) { neg = !neg; c = c.slice(1).trim(); } const m = c.match(/^(.*?)\s*(==|!=|>=|<=|>|<)\s*(.*)$/); let r; if (m) { const a = val(m[1]), b = val(m[3]); r = { '==': a == b, '!=': a != b, '>=': a >= b, '<=': a <= b, '>': a > b, '<': a < b }[m[2]]; } else { const x = val(c); r = !!x && x !== 'false' && x !== '<no value>'; } return neg ? !r : r; };
    return e.split('||').some((orp) => orp.split('&&').every(one));
  };

  /* ---------- test: run up to a step against a real past event ---------- */
  const hash = (s) => { let h = 0; for (const c of String(s)) h = (h * 31 + c.charCodeAt(0)) | 0; return Math.abs(h); };
  function simulate(s, kind, scope, ev) {
    const miss = [];
    switch (kind) {
      case 'verb': case 'ask': {
        const decl = window.SCHEMA.verb(s.uses); if (!decl) throw new Error(`${s.uses} is not a known verb`);
        const opts = {}; decl.fields.forEach((f) => { const raw = s.options && s.options[f.k] != null ? s.options[f.k] : f.def; if (raw != null) opts[f.k] = renderDeep(raw, scope, miss); });
        if (miss.length) throw new Error(`{{.${miss[0]}}} has no value in this event or an earlier step's output`);
        decl.fields.forEach((f) => { if (f.req && (opts[f.k] == null || opts[f.k] === '')) throw new Error(`options.${f.k} is required`); });
        const out = Y.clone(decl.outputs);
        if (out.url && opts.repo && opts.number) out.url = `https://github.com/${opts.repo}/pull/${opts.number}#issuecomment-${1844120391 + hash(JSON.stringify(opts)) % 1000}`;
        if (kind === 'ask') out.text = opts.draft ? String(opts.draft).slice(0, 80) : out.text;
        return { output: out, inputs: opts, note: kind === 'ask' ? 'simulated reply: approve' : decl.read ? 'read verb · called for real in a test' : 'write verb · dry-run: not sent' };
      }
      case 'agent': {
        if (s.model && typeof s.model === 'string' && !(fleetNames().includes(s.model) || window.SCHEMA.models.includes(s.model) || s.model.includes('*'))) throw new Error(`model "${s.model}": no fleet in models: and no provider offers it`);
        const prompt = WF.render(s.prompt || '', scope, miss);
        if (miss.length) throw new Error(`prompt: {{.${miss[0]}}} has no value`);
        let text = `Done. ${String(prompt).split('\n')[0].slice(0, 90)}`;
        if (/label from|one label/i.test(prompt)) text = /bug|fail|error|retr/i.test(JSON.stringify(ev)) ? 'bug' : 'question';
        else if (/decide auto vs manual/i.test(prompt)) text = 'auto';
        else if (/^fix/i.test(prompt)) text = 'Fixed the rounding in applyFee (half-even); go test ./internal/billing passes.';
        else if (/^review/i.test(prompt)) text = 'Two N+1 queries in the orders handler; add an index on (tenant_id, created_at).';
        const out = { text, diff: /^(fix|bump)/i.test(prompt) ? 'internal/billing/refund.go | 6 +++--' : '', cost_usd: +(0.18 + (hash(prompt) % 40) / 100).toFixed(2) };
        const props = s.output_schema && s.output_schema.properties; if (props) Object.keys(props).forEach((p) => (out[p] = props[p].enum ? props[p].enum[0] : text));
        return { output: out, inputs: { prompt, model: s.model || '(bare launch)', runtime: s.runtime || 'paseo (default)' }, note: 'dry-run agent: no checkout, tools stubbed' };
      }
      case 'code': {
        const eng = s.use || s.run || 'cli';
        if (eng === 'js') {
          const ctx = Y.clone(scope);
          ctx.store = () => ({ get: () => null, set: () => true }); ctx.memory = { recall: () => [] };
          let r;
          try { r = new Function('ctx', '"use strict";\n' + (s.code || ''))(ctx); } catch (x) { throw new Error(`${x.name}: ${x.message}`); }
          if (r === undefined) throw new Error('the code returned nothing: return an object, its keys become outputs');
          return { output: isObj(r) ? r : { value: r }, inputs: { engine: 'js (QuickJS in the daemon; ran in this browser for the test)' }, note: 'ran the code against this event' };
        }
        if (eng === 'cli' || s.command) { const cmd = [].concat(s.command || []).map((x) => WF.render(String(x), scope)).join(' '); const fail = /test/.test(cmd) && ev && ev.check_name && !Object.values(scope).some((o) => o && typeof o === 'object' && o.diff); return { output: { exit_code: fail ? 1 : 0, stdout: fail ? '--- FAIL: TestRefund (0.02s)\nFAIL' : 'ok', stderr: '' }, inputs: { command: cmd }, note: 'host commands are simulated in the prototype', fail: fail ? `exit status 1: ${cmd}` : null }; }
        const m = String(s.code || '').match(/json\.dumps\((\{[\s\S]*?\})\)/);
        return { output: m ? { sev: 'low', name: ev && (ev.title || ev.check_name) } : { stdout: '' }, inputs: { engine: eng }, note: `${eng} runs on the daemon; simulated here` };
      }
      case 'decide': {
        const o = { _by: `${s.model || 'light'} (claude-haiku-4-5)` };
        const st = WF.render(S.getPath(s, 'decide.state') || '', scope, miss);
        Object.entries((s.decide && s.decide.questions) || {}).forEach(([q, d]) => { const t = (d && d.type) || 'noul'; if (t === 'choice') { const ch = d.choices || []; const flaky = ev && (ev.check_name === 'e2e' || /hold|flaky/.test(JSON.stringify(ev.labels || ev.title || ''))); o[q] = (flaky && ch.find((c) => /flak/.test(c))) || (/FAIL|failed/i.test(st) && ch.find((c) => /fail/.test(c))) || ch.find((c) => new RegExp(c.replace(/_/g, '.?'), 'i').test(st + JSON.stringify(ev))) || ch[hash(st) % Math.max(1, ch.length)] || ''; } else if (t === 'score') o[q] = { score: +((hash(q + st) % 100) / 100).toFixed(2) }; else o[q] = { noul: +((hash(q + st) % 100) / 100).toFixed(2) }; });
        return { output: o, inputs: { state: String(st).slice(0, 200) }, note: 'answered by the decision runtime (simulated)' };
      }
      case 'call': {
        const w = V().workflows.find((x) => x.name === s.call); if (!w) throw new Error(`no workflow ${s.call}`);
        const withV = renderDeep(s.with || {}, scope, miss); if (miss.length) throw new Error(`with: {{.${miss[0]}}} has no value`);
        let outs = {}; try { outs = Y.parse(w.yaml).value.outputs || {}; } catch (x) {}
        return { output: Object.fromEntries(Object.keys(outs).map((k) => [k, k === 'decision' ? 'auto' : '…'])), inputs: withV, note: `ran ${s.call} as a dry-run` };
      }
      case 'wait': return { output: s.wait_for ? Y.clone((window.SCHEMA.verb(s.wait_for.uses) || {}).outputs || {}) : {}, inputs: { sleep: s.sleep }, note: 'skipped the wait in the test' };
      case 'log': return { output: {}, inputs: { log: WF.render(s.log, scope) }, note: 'logged' };
      case 'set': return { output: renderDeep(s.set, scope, miss), inputs: {}, note: '' };
      case 'assert': if (!WF.cond(s.assert, scope)) throw new Error(`assert failed: ${s.assert}`); return { output: {}, inputs: {}, note: 'held' };
      case 'fail': throw new Error(WF.render(s.fail, scope));
      case 'bot': {
        const title = WF.render(s.title || '', scope, miss); if (miss.length) throw new Error(`title: {{.${miss[0]}}} has no value`);
        if (typeof s.bot === 'string') return { output: { case: 'c41', routed_to: s.bot, by: 'direct' }, inputs: { title }, note: 'dry run: the case is not opened' };
        const t = V().teams.find((x) => x.name === s.bot.team); if (!t) throw new Error(`no team ${s.bot.team}`);
        const on = ev && (ev.incident ? 'pd.incident' : ev.check_name ? 'gh.check_failed' : ev.labels ? 'gh.issue_opened' : '');
        const r = (t.cfg.routing || []).find((x) => x.when && x.when.on === on && Object.entries(x.when).every(([k, v]) => k === 'on' || [].concat(v).includes(String(ev[k] != null ? ev[k] : (ev.incident || {})[k]))));
        const to = r ? (r.to === 'lead' ? t.cfg.lead : r.to) : t.cfg.lead;
        return { output: { case: 'c41', routed_to: to || '(no lead)', by: r ? `rule ${r.name}` : 'lead' }, inputs: { title, team: t.name }, note: 'dry run: routed by the team\'s rules; no case opened' };
      }
      default: throw new Error('this step has no type');
    }
  }
  /** Run steps in order up to (and including) `targetId`; returns { results, order }. */
  WF.test = (value, targetId, ev) => {
    const results = {}; const scope = Object.assign({}, Y.clone(ev || {}));
    let stop = false, halted = false;
    const runList = (list, inBranch) => {
      for (const s of list || []) {
        if (stop) return;
        const kind = WF.kind(s), id = (s && s.id) || null, key = id || `${kind}@${Math.random().toString(36).slice(2, 6)}`;
        if (halted) { if (id) results[id] = { status: 'not run' }; continue; }
        if (kind === 'parallel') { s.parallel.forEach((br) => runList(br, true)); if (!inBranch && s.parallel.flat().some((x) => x && x.id === targetId)) stop = true; continue; }
        const t0 = 20 + (hash(key) % 900);
        if (s.if != null && !WF.cond(s.if, scope)) { if (id) results[id] = { status: 'skipped', note: `if: ${s.if} → false`, ms: 1 }; }
        else {
          try { const r = simulate(s, kind, scope, ev); if (r.fail) throw Object.assign(new Error(r.fail), { output: r.output }); if (id) { results[id] = { status: 'ok', output: r.output, inputs: r.inputs, note: r.note, ms: t0 }; scope[id] = r.output; } }
          catch (x) { if (id) results[id] = { status: 'error', error: x.message, output: x.output, ms: t0 }; if (s.continue_on_error) { if (id) scope[id] = { error: x.message, failed: true }; } else halted = true; }
        }
        if (id === targetId && !inBranch) stop = true;
      }
    };
    runList(value.steps, false);
    return results;
  };

  /** Data visible to a step, for the reference picker: trigger + earlier steps. */
  WF.scope = (value, entry, results, ev) => {
    const groups = [];
    const srcs = WF.sources(value).map((s) => s.on).join(' · ') || (value.inputs ? 'call inputs' : 'no trigger');
    groups.push({ key: '', label: 'Trigger', sub: srcs, icon: 'live', data: ev ? Y.clone(ev) : WF.eventFields(value), tested: !!ev });
    WF.before(value, entry).forEach((e) => {
      if (!e.step || !e.step.id) return;
      const r = results && results[e.step.id];
      const data = r && r.status === 'ok' ? r.output : WF.outputsOf(e.step);
      groups.push({ key: e.step.id, label: e.step.id, sub: WF.summary(e.step), icon: WF.KINDS[e.kind].icon, data: data || {}, tested: !!(r && r.status === 'ok'), unknown: !data });
    });
    return groups;
  };
  /** Flat list of reference paths for autocomplete. */
  WF.paths = (groups) => {
    const out = [];
    groups.forEach((g) => { const rec = (o, pre, depth) => { if (depth > 4) return; if (isObj(o)) Object.entries(o).forEach(([k, v]) => { const p = pre ? pre + '.' + k : k; out.push({ path: p, value: v, group: g.label }); rec(v, p, depth + 1); }); }; rec(g.data, g.key, 0); if (g.key) out.unshift({ path: g.key, value: g.data, group: g.label }); });
    return out;
  };
})();
