/* Knowledge you can author, by kind: a team's knowledge repo and a bot's own
 * memory. Every entry has a kind, and its kind decides its home and its
 * fields, as conductor's kinds declare them: knowledge/ (title, applies_to,
 * evidence, confidence), runbooks/ (title, when_to_use, prerequisites, steps,
 * verification, rollback, owner), watch/ (what, signal, threshold, why),
 * log/ (bots only; the operator appends a note or flags it wrong), handoff/
 * (system), and collections declared on the team or bot (or by a pack). Paths are derived from kind and
 * title; free paths only where a collection allows them. Git underneath:
 * every save is one commit authored by the operator, with history, diff,
 * revert, blame and conflict handling. Bot proposals and imports are checked
 * against the same schema. */
(function () {
  const { icon, esc, ago, tag } = UI;
  const now = () => new Date().toISOString();
  const R = (window.R = {});
  const minsAgo = (m) => new Date(Date.now() - m * 60000).toISOString();
  const sha = () => Math.random().toString(16).slice(2, 9);

  /* ---------- markdown: highlighting and a safe preview ---------- */
  UI.mdLine = (raw) => {
    const E = esc(raw);
    if (/^#{1,6}\s/.test(raw)) return `<span class="y-k">${E}</span>`;
    if (/^```/.test(raw)) return `<span class="y-c">${E}</span>`;
    return E.replace(/^(\s*[-*+]|\s*\d+\.)\s/, '<span class="y-a">$&</span>').replace(/\*\*([^*]+)\*\*/g, '<b>**$1**</b>').replace(/`([^`]+)`/g, '<span class="y-s">`$1`</span>').replace(/\[\[([^\]]+)\]\]/g, '<span class="y-n">[[$1]]</span>').replace(/\[([^\]]+)\]\(([^)]+)\)/g, '<span class="y-n">[$1]($2)</span>');
  };
  const inline = (t) => esc(t).replace(/\*\*([^*]+)\*\*/g, '<b>$1</b>').replace(/(^|[^*])\*([^*]+)\*/g, '$1<i>$2</i>').replace(/`([^`]+)`/g, '<code>$1</code>')
    .replace(/\[\[([^\]]+)\]\]/g, '<span class="wikilink">$1</span>').replace(/\[([^\]]+)\]\((https?:\/\/[^)\s]+)\)/g, '<a href="$2" target="_blank" rel="noopener noreferrer">$1</a>');
  UI.mdRender = (src) => {
    const out = []; let list = null, code = null, para = [];
    const flushP = () => { if (para.length) { out.push(`<p>${inline(para.join(' '))}</p>`); para = []; } };
    const flushL = () => { if (list) { out.push(`<${list.t}>${list.items.map((x) => `<li>${inline(x)}</li>`).join('')}</${list.t}>`); list = null; } };
    String(src || '').split('\n').forEach((l) => {
      if (code) { if (/^```/.test(l)) { out.push(`<pre><code>${esc(code.join('\n'))}</code></pre>`); code = null; } else code.push(l); return; }
      if (/^```/.test(l)) { flushP(); flushL(); code = []; return; }
      const h = l.match(/^(#{1,6})\s+(.*)$/); if (h) { flushP(); flushL(); out.push(`<h${Math.min(4, h[1].length + 1)}>${inline(h[2])}</h${Math.min(4, h[1].length + 1)}>`); return; }
      const li = l.match(/^\s*([-*+]|\d+\.)\s+(.*)$/); if (li) { flushP(); const t = /\d/.test(li[1]) ? 'ol' : 'ul'; if (!list || list.t !== t) { flushL(); list = { t, items: [] }; } list.items.push(li[2]); return; }
      if (!l.trim()) { flushP(); flushL(); return; }
      if (list && /^\s{2,}/.test(l)) { list.items[list.items.length - 1] += ' ' + l.trim(); return; }
      flushL(); para.push(l.trim());
    });
    if (code) out.push(`<pre><code>${esc(code.join('\n'))}</code></pre>`);
    flushP(); flushL();
    return out.join('');
  };


  /* ---------- kinds: what goes where ---------- */
  const slug = (t) => String(t || '').toLowerCase().replace(/[^a-z0-9]+/g, '-').replace(/^-+|-+$/g, '') || 'untitled';
  const F = (k, type, o = {}) => Object.assign({ k, type }, o);
  const KINDS = (UI.KINDS = {
    knowledge: { id: 'knowledge', label: 'Knowledge', one: 'knowledge entry', icon: 'sparkle', dir: 'knowledge', repos: ['team', 'bot'], body: 'What is true, and why you believe it', template: '',
      fields: [F('title', 'string', { req: true }), F('applies_to', 'list', { label: 'Applies to', ph: 'service or resource', help: 'Services and resources this is about. Used to filter and to find it.' }), F('evidence', 'text', { req: true, rows: 2, label: 'Evidence', help: 'Why you believe it: a case, a file, a dashboard, a test.' }), F('confidence', 'enum', { req: true, options: ['low', 'medium', 'high'], def: 'medium' })],
      who: { team: 'You write directly; bots promote from their memory, with your approval.', bot: 'You and the bot write directly.' } },
    runbook: { id: 'runbook', label: 'Runbooks', one: 'runbook', icon: 'workflows', dir: 'runbooks', repos: ['team', 'bot'], body: 'Notes on the procedure',
      fields: [F('title', 'string', { req: true }), F('when_to_use', 'text', { req: true, rows: 2, label: 'When to use', help: 'The trigger or the symptoms.' }), F('prerequisites', 'list', { ph: 'access, a change window, a tool' }), F('steps', 'steps', { req: true }), F('verification', 'text', { req: true, rows: 2, help: 'How you know it worked.' }), F('rollback', 'text', { req: true, rows: 2, help: 'How to undo it, or "nothing to roll back".' }), F('owner', 'string', { req: true, ph: 'a bot, a team or a person' })],
      who: { team: 'You write directly; bots propose.', bot: 'You and the bot write directly.' } },
    watch: { id: 'watch', label: 'Watch', one: 'watch item', icon: 'eye', dir: 'watch', titleKey: 'what', repos: ['team', 'bot'],
      fields: [F('what', 'string', { req: true, label: 'What to watch' }), F('signal', 'text', { req: true, rows: 2, label: 'Signal / query' }), F('threshold', 'text', { req: true, rows: 1 }), F('why', 'text', { req: true, rows: 2 })],
      who: { team: 'You write directly; bots propose.', bot: 'You and the bot write directly.' } },
    log: { id: 'log', label: 'Log', one: 'log entry', icon: 'clock', dir: 'log', repos: ['bot'], readonly: 'The bot writes its log, one entry per case or sweep. You can add a note or flag it as wrong (both appended, with your name), but history isn\'t rewritten.', who: { bot: 'The bot only. You annotate or flag.' } },
    handoff: { id: 'handoff', label: 'Handoff', one: 'handoff note', icon: 'send', dir: 'handoff', repos: ['bot'], readonly: 'Written by conductor when a home session rotates. Read-only.', who: { bot: 'System-generated, read-only.' } },
  });
  const titleOf = (kind, fm) => fm[kind.titleKey || 'title'];
  const fieldFromCfg = ([name, f]) => F(name, f.type === 'enum' ? 'enum' : ['string', 'text', 'list', 'steps', 'number', 'bool'].includes(f.type) ? f.type : 'string', Object.assign({ req: !!f.required }, f.type === 'enum' ? { options: f.values || [] } : {}, f.type === 'text' ? { rows: 2 } : {}));
  /** A repo's collections: teams.<t>.collections or bots.<b>.collections; a pack's definition via from:. */
  const collections = (r) => { const owner = r.kind === 'team' ? S.view().teams.find((t) => t.name === r.name) : S.view().bots.find((b) => b.name === r.name); const cols = (owner && owner.cfg && owner.cfg.collections) || {};
    return Object.entries(cols).map(([name, c0]) => { const c = c0.from ? Object.assign({}, (D.packCollectionFiles || {})[c0.from] || {}, { from: c0.from }) : c0; const fields = Object.entries(c.fields || {}).map(fieldFromCfg); if (!fields.some((f) => f.k === 'title')) fields.unshift(F('title', 'string', { req: true })); const writers = c.writers || ['operator'];
      return { id: 'c:' + name, coll: name, label: name, one: name.replace(/s$/, '') + ' entry', icon: 'folder', dir: String(c.dir || name).replace(/\/$/, ''), pack: c.from ? (c.from.match(/packs\/([^/]+)/) || [])[1] : null, from: c.from, free: !!c.free_paths, template: c.template || '', write: writers, body: 'Body', fields, who: { team: `Writers: ${writers.join(', ')}.`, bot: `Writers: ${writers.join(', ')}.` } }; }); };
  const kindsFor = (r) => Object.values(KINDS).filter((k) => k.repos.includes(r.kind)).concat(collections(r));
  const kindById = (r, id) => kindsFor(r).find((k) => k.id === id);
  const visible = (p) => !p.split('/').some((x) => x.startsWith('.'));
  const kindOfPath = (r, p) => kindsFor(r).filter((k) => k.dir).sort((a, b) => b.dir.length - a.dir.length).find((k) => p.startsWith(k.dir + '/')) || null;
  const writable = (r, k) => (k.readonly ? k.readonly : k.coll && !k.write.includes('operator') ? `Only bots write ${k.label}, as the collection declares` : '');

  /* ---------- the file format: markdown with front matter ---------- */
  const FM = (R.FM = {
    parse(text) { text = text || ''; if (!text.startsWith('---\n')) return { fm: {}, body: text, raw: true }; const end = text.indexOf('\n---\n', 3); if (end < 0) return { fm: {}, body: text, err: 'front matter is not closed with ---' }; try { return { fm: YAML.parse(text.slice(4, end + 1)).value || {}, body: text.slice(end + 5) }; } catch (e) { return { fm: {}, body: text.slice(end + 5), err: 'front matter: ' + e.message }; } },
    emit(kind, fm, body) { const o = { kind: kind.coll || kind.id }; kind.fields.forEach((f) => { const v = fm[f.k]; if (v == null || v === '' || (Array.isArray(v) && !v.length)) return; o[f.k] = v; }); return '---\n' + YAML.stringify(o).replace(/\n?$/, '\n') + '---\n' + (body && body.trim() ? body.replace(/\s*$/, '\n') : ''); },
  });
  /** Schema check for one kind; the same check runs on operator saves, bot proposals and imports. */
  /** The kind's schema check, worded like conductor's (missing required field, want a list…). */
  const validate = (R.validate = (kind, fm, declared) => {
    const e = [];
    if (declared && declared !== (kind.coll || kind.id)) e.push(`kind: says "${declared}", but ${kind.dir}/ holds ${kind.label.toLowerCase()}`);
    kind.fields.forEach((f) => {
      const v = fm[f.k], empty = v == null || v === '' || (Array.isArray(v) && !v.length);
      if (f.req && empty) { e.push(`${f.k}: missing required field (${f.type === 'steps' ? 'at least one step' : f.type})`); return; }
      if (empty) return;
      if (f.type === 'string' && (typeof v !== 'string' || /\n/.test(v))) e.push(`${f.k}: want one line`);
      if (f.type === 'enum' && !f.options.includes(v)) e.push(`${f.k}: ${v} is not one of ${f.options.join(', ')}`);
      if ((f.type === 'list' || f.type === 'steps') && !Array.isArray(v)) e.push(`${f.k}: want a list`);
      else if (f.type === 'list' || f.type === 'steps') v.forEach((x, i) => { if (typeof x !== 'string' || !x.trim()) e.push(`${f.k}: item ${i + 1}: want a non-empty string`); });
      if (f.type === 'number' && typeof v !== 'number') e.push(`${f.k}: want a number`);
      if (f.type === 'bool' && typeof v !== 'boolean') e.push(`${f.k}: want true or false`);
    });
    return e;
  });
  const entriesOf = (r, kind) => Object.keys(r.files).filter((p) => visible(p) && kindOfPath(r, p) === kind).sort().map((p) => { const x = FM.parse(r.files[p]); return { kind, path: p, key: p, title: titleOf(kind, x.fm) || p.split('/').pop().replace(/\.md$/, ''), fm: x.fm, body: x.body, err: x.err }; });
  const derive = (kind, title, free) => `${kind.dir}/${kind.free && free ? free.replace(/^\/+/, '').replace(/\.md$/, '') : slug(title)}.md`;
  const botsOfTeam = (t) => D.bots.filter((b) => b.cfg && b.cfg.team === t).map((b) => b.name);
  const related = (r) => { const repos = [r]; if (r.kind === 'team') botsOfTeam(r.name).forEach((b) => repos.push(R.get('bot:' + b))); else { const b = D.bots.find((x) => x.name === r.name); if (b && b.cfg && b.cfg.team) repos.push(R.get('team:' + b.cfg.team)); } return repos; };
  const caseHref = (id) => { const c = D.cases.find((x) => x.id === id); return c ? `#/tickets/${id}` : null; };
  /** Who cites an entry: runbooks and log entries that link it, and the cases behind them. */
  const cites = (r, path) => {
    const stem = path.replace(/\.md$/, ''), out = { runbooks: [], logs: [], cases: new Set() };
    related(r).forEach((x) => Object.entries(x.files).forEach(([p, c]) => { if (p === path || !visible(p) || !(c.includes('[[' + stem + ']]') || c.includes(path))) return; const k = kindOfPath(x, p), fm = FM.parse(c).fm; if (k && k.id === 'runbook') out.runbooks.push({ repo: x.id, path: p, title: fm.title || p }); if (k && k.id === 'log') { out.logs.push({ repo: x.id, path: p, title: fm.title || p }); if (fm.case) out.cases.add(fm.case); } }));
    D.cases.forEach((c) => (c.notes || []).forEach((n) => { if ((n.text || '').includes(path) || (n.text || '').includes(stem + '.md')) out.cases.add(c.id); }));
    out.cases = [...out.cases]; return out;
  };

  /* ---------- the repos (git underneath: files, commits, history) ---------- */
  R.get = (id) => {
    D.repos = D.repos || {};
    if (D.repos[id]) return D.repos[id];
    const [kind, name] = id.split(':');
    let files = {}, proposals = [], log = [];
    const imp = (who, whoKind, at, fs) => ({ sha: sha(), msg: 'import', who, kind: whoKind, at, changes: Object.entries(fs).map(([p, c]) => ({ path: p, before: null, after: c })) });
    if (kind === 'bot' && name === 'platform-sre' && D.memory) {
      files = Object.assign({}, D.memory.files);
      const db = files['knowledge/db-prod-1.md'], lat = files['runbooks/latency.md'];
      const db0 = db.split('\n').filter((l) => !/Failover drill|storm capped/.test(l)).join('\n');
      const lat0 = lat.split('\n').filter((l) => !/deregistration/.test(l)).join('\n');
      log = [
        { sha: '8e1f0aa', msg: 'knowledge: db-prod-1 failover drill outcome', who: 'bot:platform-sre', kind: 'bot', case: 'c31', at: minsAgo(1), changes: [{ path: 'knowledge/db-prod-1.md', before: db0, after: db }] },
        { sha: 'b07e5c1', msg: 'runbooks/latency: add ALB deregistration step', who: 'operator:ops', kind: 'operator', at: minsAgo(1440), changes: [{ path: 'runbooks/latency.md', before: lat0, after: lat }] },
        imp('bot:platform-sre', 'bot', minsAgo(4320), Object.assign({}, files, { 'knowledge/db-prod-1.md': db0, 'runbooks/latency.md': lat0 })),
      ];
      log[2].msg = 'import: memory from session s1';
    } else if (kind === 'bot') {
      files = Object.assign({}, (D.botMemory || {})[name] || {});
      log = Object.keys(files).length ? [imp('bot:' + name, 'bot', minsAgo(2880), files)] : [];
    } else {
      const tk = (D.teamKnowledge || {})[name];
      files = tk ? Object.assign({}, tk.files) : {};
      proposals = tk ? JSON.parse(JSON.stringify(tk.proposals || [])) : [];
      const lead = (D.teams.find((t) => t.name === name) || { cfg: {} }).cfg.lead || botsOfTeam(name)[0] || 'ops';
      log = Object.keys(files).length ? [imp('bot:' + lead, 'bot', minsAgo(5000), files)] : [];
    }
    const r = { id, kind, name, files, log, proposals, open: (id === 'bot:platform-sre' && D.memory && D.memory.open) || null };
    D.repos[id] = r; return r;
  };
  R.version = (r, path) => { const c = r.log.find((x) => x.changes.some((ch) => ch.path === path || ch.from === path)); return c ? c.sha : null; };
  R.commit = (r, o) => {
    const c = Object.assign({ sha: sha(), at: now(), who: 'operator:ops', kind: 'operator' }, o);
    c.changes.forEach((ch) => { if (ch.from && ch.from !== ch.path) delete r.files[ch.from]; if (ch.after == null) delete r.files[ch.path]; else r.files[ch.path] = ch.after; });
    r.log.unshift(c);
    D.audit.unshift({ ts: c.at, event: r.kind === 'team' ? 'team_knowledge_commit' : 'memory_commit', who: c.who, detail: `${r.id} · ${c.sha} · ${c.msg}`, run: '' });
    S.save(); return c;
  };
  R.blame = (r, path) => {
    const chain = []; let p = path;
    for (const c of r.log) { const ch = c.changes.find((x) => x.path === p); if (ch) { chain.unshift({ c, ch }); if (ch.from) p = ch.from; if (ch.before == null && !ch.from) break; } }
    let lines = [], owners = [];
    chain.forEach(({ c, ch }) => {
      const next = (ch.after || '').split('\n');
      if (ch.before == null && !ch.from) { lines = next; owners = next.map(() => c); return; }
      const d = YAML.diff(lines.join('\n'), next.join('\n')); const no = []; let ai = 0;
      d.forEach((x) => { if (x.t === ' ') { no.push(owners[ai] || c); ai++; } else if (x.t === '-') ai++; else no.push(c); });
      lines = next; owners = no;
    });
    return lines.map((l, i) => ({ line: l, c: owners[i] }));
  };
  R.pending = (id) => R.get(id).proposals.filter((p) => p.status === 'pending').length;
  const whoLabel = (c) => (c.kind === 'operator' ? 'ops (operator)' : c.who.replace('bot:', ''));
  const whoChip = (c) => `${UI.label(esc(whoLabel(c)), { cls: 'who ' + c.kind, icon: c.kind === 'operator' ? 'users' : 'bots' })}${c.case && caseHref(c.case) ? ` <a class="link mono tiny" href="${caseHref(c.case)}">${c.case}</a>` : ''}`;
  const readers = (r) => (r.kind === 'team' ? botsOfTeam(r.name) : [r.name]);

  /* ---------- editor state ---------- */
  const ev = { repo: null, kind: null, key: null, isNew: false, base: '', baseSha: null, steps: [], mode: 'split', blame: false, proposal: null, conflict: null, q: '', tags: [], view: 'kinds', file: null, hist: 'file', sel: new Set(), draft: null };
  const pathOf = () => (ev.key ? ev.key.split('#')[0] : null);
  // a step is one line of text (conductor's runbook steps are strings); a command or verb rides along as "(run: `cmd`)" / "(verb: x)"
  const stepsFrom = (fm) => (Array.isArray(fm.steps) ? fm.steps : []).map((st) => { if (st && typeof st === 'object') return { do: st.do || '', type: st.verb ? 'verb' : st.run ? 'run' : '', arg: st.verb || st.run || '' }; const m = String(st || '').match(/^(.*?)\s*\((run|verb): `?([^`)]*)`?\)\s*$/); return m ? { do: m[1], type: m[2], arg: m[3] } : { do: String(st || ''), type: '', arg: '' }; });
  const stepsTo = (st) => st.filter((x) => x.do || x.arg).map((x) => `${x.do}${x.type === 'run' && x.arg ? ` (run: \`${x.arg}\`)` : x.type === 'verb' && x.arg ? ` (verb: ${x.arg})` : ''}`);
  const serialize = (kind, d) => (!kind.fields ? '' : FM.emit(kind, d.fm, d.body) + '|' + (d.free || ''));
  function openEntry(r, kind, entry, draft) {
    ev.repo = r.id; ev.kind = kind.id; ev.key = entry ? entry.key : null; ev.isNew = !entry; ev.conflict = null; ev.proposal = null; ev.blame = false; ev.view = 'kinds';
    const d = draft || (entry ? { fm: JSON.parse(JSON.stringify(entry.fm)), body: entry.body || '', free: entry && kind.free ? entry.path.slice(kind.dir.length + 1).replace(/\.md$/, '') : '' } : { fm: {}, body: kind.template || '', free: '' });
    ev.draft = d; ev.steps = stepsFrom(d.fm);
    ev.base = draft && !entry ? serialize(kind, { fm: {}, body: kind.template || '', free: '' }) : entry ? serialize(kind, { fm: entry.fm, body: entry.body || '', free: d.free }) : serialize(kind, d);
    ev.baseSha = entry ? R.version(r, entry.path) : null;
    r.open = ev.key; r.openKind = kind.id; S.save();
  }
  function readDraft(kind) {
    const f = UI.$('#ke-form'); if (!f) return ev.draft;
    syncSteps();
    const fm = UI.formValues(f, kind.fields.filter((x) => x.type !== 'steps'));
    if (kind.fields.some((x) => x.type === 'steps')) fm.steps = stepsTo(ev.steps);
    const ta = UI.$('#rp-ed .ced-ta'), fr = UI.$('#ke-free');
    return { fm, body: ta ? ta.value : (ev.draft || {}).body || '', free: fr ? fr.value.trim() : '' };
  }
  const isDirty = () => { const r = ev.repo && R.get(ev.repo), kind = r && ev.kind && kindById(r, ev.kind); if (!kind || kind.readonly || !UI.$('#ke-form')) return false; return serialize(kind, readDraft(kind)) !== ev.base; };
  UI.guard = () => (isDirty() ? `Your edits to <span class="mono">${esc(pathOf() || 'the new entry')}</span> aren't saved. Leave and lose them?` : null);
  UI.guardClear = () => { ev.base = ''; ev.draft = null; };
  const guardThen = (fn) => { if (!isDirty() && !ev.proposal) return fn(); UI.confirm({ title: 'Discard unsaved changes?', body: UI.guard() || 'You are approving a proposal with changes.', ok: 'Discard', cancel: 'Keep editing', danger: true }).then((ok) => { if (ok) { ev.base = ''; ev.proposal = null; fn(); } }); };
  const enable = (el, on, reason) => { if (!el) return; if (on) { el.removeAttribute('aria-disabled'); delete el.dataset.reason; delete el.dataset.tip; } else { el.setAttribute('aria-disabled', 'true'); el.dataset.reason = el.dataset.tip = reason; } };
  function refreshDirty() {
    const r = ev.repo && R.get(ev.repo), kind = r && kindById(r, ev.kind); if (!kind || !UI.$('#ke-form')) return;
    const d = readDraft(kind), dirty = serialize(kind, d) !== ev.base || !!ev.proposal;
    enable(UI.$('[data-act="rp-save"]'), dirty, ev.isNew ? 'Fill in the fields first' : 'Nothing to save yet: change a field first');
    enable(UI.$('[data-act="rp-discard"]'), (dirty || ev.isNew) && !ev.proposal, 'No unsaved changes');
    const t = UI.$('#ke-dirty'); if (t) t.classList.toggle('hide', !dirty || !!ev.proposal);
    const p = UI.$('#ke-path'); if (p) p.textContent = derive(kind, titleOf(kind, d.fm) || '', d.free);
    const pv = UI.$('#rp-prev'); if (pv) pv.innerHTML = UI.mdRender(d.body);
  }
  let rdT; const later = () => { clearTimeout(rdT); rdT = setTimeout(refreshDirty, 60); };
  ['input', 'change'].forEach((t) => document.addEventListener(t, (e) => { if (e.target.closest && e.target.closest('#ke-form')) refreshDirty(); }));
  document.addEventListener('click', (e) => { if (e.target.closest && e.target.closest('#ke-form')) later(); });
  document.addEventListener('keyup', (e) => { if (e.target.closest && e.target.closest('#ke-form')) later(); });

  /* ---------- runbook steps ---------- */
  const verbList = () => S.view().connectors.flatMap((c) => { const d = SCHEMA.connectors[(c.cfg && c.cfg.use || c.use || '').split('@')[0]]; return d ? Object.keys(d.verbs).map((v) => `${c.name}.${v}`) : []; });
  function syncSteps() { const box = UI.$('#rb-steps'); if (!box) return; ev.steps = UI.$$('.rb-step', box).map((row) => ({ do: row.querySelector('[data-sk="do"]').value, type: row.querySelector('[data-sk="type"]').value, arg: (row.querySelector('[data-sk="arg"]') || { value: '' }).value })); }
  const stepsHTML = () => `<div class="rb-steps" id="rb-steps">${ev.steps.map((s, i) => `<div class="rb-step" data-i="${i}"><span class="rb-n">${i + 1}</span>
      <div class="rb-main"><input class="input" data-sk="do" value="${esc(s.do)}" placeholder="What to do" aria-label="Step ${i + 1}: what to do">
        <div class="rb-act"><select class="select sm" data-sk="type" data-act-change="rb-type" aria-label="Step ${i + 1}: command or verb"><option value=""${!s.type ? ' selected' : ''}>no command</option><option value="run"${s.type === 'run' ? ' selected' : ''}>run a command</option><option value="verb"${s.type === 'verb' ? ' selected' : ''}>call a verb</option></select>${s.type ? `<input class="input mono" data-sk="arg" value="${esc(s.arg)}" ${s.type === 'verb' ? 'list="rb-verbs" placeholder="connector.verb"' : 'placeholder="the command"'} aria-label="Step ${i + 1}: ${s.type === 'verb' ? 'verb' : 'command'}">` : ''}</div></div>
      <div class="rb-ctl"><button type="button" class="icon-btn sm" ${i ? `data-act="rb-up" data-i="${i}"` : UI.off('Already the first step')} aria-label="Move step ${i + 1} up">↑</button><button type="button" class="icon-btn sm" ${i < ev.steps.length - 1 ? `data-act="rb-down" data-i="${i}"` : UI.off('Already the last step')} aria-label="Move step ${i + 1} down">↓</button><button type="button" class="icon-btn sm" data-act="rb-del" data-i="${i}" aria-label="Remove step ${i + 1}">${icon('trash')}</button></div></div>`).join('') || '<div class="muted small">No steps yet.</div>'}</div>
    <button type="button" class="btn sm" data-act="rb-add">${icon('plus')}Add a step</button><datalist id="rb-verbs">${verbList().map((v) => `<option value="${esc(v)}">`).join('')}</datalist>`;
  const redrawSteps = () => { const f = UI.$('[data-field="steps"] .rb-wrap'); if (f) f.innerHTML = stepsHTML(); later(); };
  UI.act('rb-add', () => { syncSteps(); ev.steps.push({ do: '', type: '', arg: '' }); redrawSteps(); const rows = UI.$$('#rb-steps [data-sk="do"]'); if (rows.length) rows[rows.length - 1].focus(); });
  UI.act('rb-del', (b) => { syncSteps(); ev.steps.splice(+b.dataset.i, 1); redrawSteps(); });
  UI.act('rb-up', (b) => { syncSteps(); const i = +b.dataset.i; [ev.steps[i - 1], ev.steps[i]] = [ev.steps[i], ev.steps[i - 1]]; redrawSteps(); });
  UI.act('rb-down', (b) => { syncSteps(); const i = +b.dataset.i; [ev.steps[i + 1], ev.steps[i]] = [ev.steps[i], ev.steps[i + 1]]; redrawSteps(); });
  document.addEventListener('change', (e) => { if (e.target.matches && e.target.matches('[data-act-change="rb-type"]')) { syncSteps(); redrawSteps(); } });

  /* ---------- the view ---------- */
  UI.repoView = (id, opts = {}) => {
    const r = R.get(id), kinds = kindsFor(r);
    if (ev.repo !== id) { ev.repo = id; ev.q = ''; ev.tags = []; ev.view = 'kinds'; ev.key = null; ev.kind = null; const k = kindById(r, r.openKind) || (r.open && kindOfPath(r, r.open.split('#')[0])) || kinds[0]; const es = entriesOf(r, k), e = es.find((x) => x.key === r.open) || es[0]; if (e) openEntry(r, k, e); else ev.kind = k.id; }
    let kind = kindById(r, ev.kind) || kinds[0];
    if (ev.key && !ev.isNew && !entriesOf(r, kind).some((x) => x.key === ev.key)) { const e = entriesOf(r, kind)[0]; if (e) openEntry(r, kind, e); else { ev.key = null; } }
    const pins = (r.files['.pins'] || '').split('\n').filter(Boolean);
    const tags = [...new Set(kinds.flatMap((k) => (k.dir || k.file ? entriesOf(r, k) : [])).flatMap((e) => (Array.isArray(e.fm.applies_to) ? e.fm.applies_to : [])))].sort();
    const match = (e) => (!ev.tags.length || (Array.isArray(e.fm.applies_to) && e.fm.applies_to.some((t) => ev.tags.includes(t)))) && (!ev.q || JSON.stringify(e.fm).toLowerCase().includes(ev.q.toLowerCase()) || (e.body || '').toLowerCase().includes(ev.q.toLowerCase()));
    const kindRow = (k) => { const es = entriesOf(r, k), shown = es.filter(match), on = k.id === kind.id;
      return `<div class="kind-grp${on ? ' on' : ''}"><a href="#" class="kind-row" data-act="rp-kind" data-repo="${id}" data-k="${k.id}" aria-expanded="${on}">${icon(k.icon)}<span class="grow">${esc(k.label)}${k.pack ? ` ${UI.label(`pack ${esc(k.pack)}`, { cls: 'origin custom' })}` : ''}</span>${UI.count(`${ev.q || ev.tags.length ? `${shown.length}/` : ''}${es.length}`)}</a>
        ${on ? `<div class="kind-entries">${shown.map((e) => `<a href="#" class="f entry${e.key === ev.key ? ' on' : ''}" data-act="rp-open" data-repo="${id}" data-key="${esc(e.key)}" data-k="${k.id}"><span class="ellipsis grow" title="${esc(e.title)}">${esc(e.title)}</span>${pins.includes(e.path) ? `<span class="pin" title="pinned: always in context">${icon('pin')}</span>` : ''}${e.err ? UI.label(`invalid`, { s: 'fail' }) : ''}</a>`).join('') || `<div class="muted tiny" style="padding:6px 12px">${es.length ? 'Nothing matches.' : 'None yet.'}</div>`}</div>` : ''}</div>`; };
    const files = Object.keys(r.files).filter(visible).sort();
    const side = `<div class="panel repo-side"><div class="panel-head"><h3 class="ellipsis" title="${esc(opts.title || id)}">${esc(r.kind === 'team' ? 'Team knowledge' : 'What it knows')}</h3><div class="tools"><button class="btn sm primary" data-act="rp-new" data-repo="${id}">${icon('plus')}New…</button><button class="icon-btn sm" data-act="rp-upload" data-repo="${id}" data-tip="Import markdown into a kind" aria-label="Import">${icon('upload')}</button></div></div>
      <div class="panel-body tight stack tight rp-find" style="padding:8px">${UI.filterBar({ id: 'rp', state: ev, placeholder: r.kind === 'team' ? 'Find in team knowledge' : 'Find in its memory', label: 'Find and filter knowledge', fields: tags.length ? [{ k: 'tags', label: 'About', alias: 'about', multi: true, title: 'What it applies to', options: () => tags.map((t) => ({ v: t, label: t, n: kinds.flatMap((k) => (k.dir || k.file ? entriesOf(r, k) : [])).filter((e) => (e.fm.applies_to || []).includes(t)).length })) }] : [], onChange: () => UI.rerender({ keepScroll: true }) })}
        ${UI.seg([['kinds', 'By kind'], ['files', 'Files']], ev.view, 'rp-view')}</div>
      ${ev.q && ev.view === 'kinds' ? searchHTML(r) : ''}
      ${ev.view === 'files' ? `<div class="tree" style="padding:6px">${files.map((p) => `<a href="#" class="f${p === ev.file ? ' on' : ''}" data-act="rp-file" data-repo="${id}" data-path="${esc(p)}">${icon('file')}<span class="mono small ellipsis" title="${esc(p)}">${esc(p)}</span>${kindOfPath(r, p) ? '' : UI.label('untyped')}</a>`).join('') || '<div class="muted small" style="padding:8px">Empty.</div>'}<div class="muted tiny" style="padding:8px">Read-mostly: edit entries by kind.</div></div>`
        : `<div class="kinds">${kinds.map(kindRow).join('')}</div>
          <div class="dl-sub rp-coll"><a class="dl-link" href="#/config/collections">Your own kinds of entry…</a></div>`}</div>`;
    const hist = r.log.filter((c) => ev.hist === 'repo' || c.changes.some((ch) => ch.path === pathOf() || ch.from === pathOf()));
    const main = ev.view === 'files' && ev.file ? rawHTML(r) : ev.key || ev.isNew ? (kind.readonly ? readHTML(r, kind) : editorHTML(r, kind)) : `<div class="panel"><div class="empty"><span class="serif">No ${esc(kind.label.toLowerCase())} yet</span><span>${esc(kind.who[r.kind] || '')}</span>${writable(r, kind) ? '' : `<button class="btn primary" data-act="rp-new-kind" data-repo="${id}" data-k="${kind.id}">${icon('plus')}New ${esc(kind.one)}</button>`}</div></div>`;
    return `<div class="repo" data-repo="${id}">${r.kind === 'team' ? proposalsHTML(r) : ''}
      <div class="grid repo-grid">${side}${main}
        <div class="panel repo-hist"><div class="panel-head"><h3>History</h3><div class="tools">${UI.seg([['file', 'this entry'], ['repo', 'repo']], ev.hist, 'rp-hist')}</div></div>
          <div class="list">${hist.slice(0, 30).map((c) => `<div class="item" style="align-items:flex-start"><span class="mono tiny" style="color:var(--color-text-accent)">${c.sha}</span><div class="grow small"><div>${esc(c.msg)}</div><div class="tiny">${whoChip(c)} <span class="muted mono">· ${ago(c.at)} ago</span></div>${ev.hist === 'repo' ? `<div class="mono tiny muted">${c.changes.filter((ch) => visible(ch.path)).map((ch) => esc(ch.from && ch.from !== ch.path ? `${ch.from} → ${ch.path}` : ch.path)).join(', ')}</div>` : ''}</div>
            <button class="icon-btn sm" data-act="rp-diff" data-repo="${id}" data-sha="${c.sha}" aria-label="Diff ${c.sha}" data-tip="Diff">${icon('eye')}</button><button class="icon-btn sm" data-act="rp-revert" data-repo="${id}" data-sha="${c.sha}" aria-label="Revert ${c.sha}" data-tip="Revert as a new commit">${icon('undo')}</button></div>`).join('') || '<div class="item small muted">No commits yet.</div>'}</div></div>
      </div></div>`;
  };
  function editorHTML(r, kind) {
    const id = r.id, d = ev.draft || { fm: {}, body: '' }, path = ev.isNew ? derive(kind, titleOf(kind, d.fm) || '', d.free) : pathOf();
    const ver = !ev.isNew && R.version(r, path), behind = ev.baseSha && ver && ver !== ev.baseSha;
    const b = D.bots.find((x) => x.name === r.name), team = r.kind === 'bot' && b && b.cfg && b.cfg.team, tk = team && D.teams.find((x) => x.name === team);
    const pins = (r.files['.pins'] || '').split('\n').filter(Boolean);
    const errs = ev.isNew ? [] : validate(kind, d.fm);
    const c = kind.id === 'knowledge' || kind.id === 'runbook' ? cites(r, path) : null;
    const blame = ev.blame && !ev.isNew ? R.blame(r, path) : null;
    let blameHTML = '';
    if (blame) { const groups = []; blame.forEach((x) => { const last = groups[groups.length - 1]; if (last && last.c === x.c) last.lines.push(x.line); else groups.push({ c: x.c, lines: [x.line] }); }); blameHTML = `<div class="blame">${groups.map((g) => `<div class="bl-group ${g.c.kind}"><div class="bl-who">${whoChip(g.c)}<span class="mono tiny muted">${g.c.sha} · ${ago(g.c.at)}</span></div><pre>${esc(g.lines.join('\n'))}</pre></div>`).join('')}</div>`; }
    const fieldsHTML = kind.fields.map((f) => (f.type === 'steps' ? `<div class="field" data-field="steps"><label>Steps <span class="req">*</span></label><div class="rb-wrap">${stepsHTML()}</div><div class="ferr" role="alert"></div></div>` : UI.fieldHTML(f, d.fm[f.k] != null ? d.fm[f.k] : f.def != null && ev.isNew ? f.def : '', 'ke-'))).join('');
    return `<div class="panel repo-ed ke" data-kind="${kind.id}">
      <div class="panel-head ed-head">${UI.label(esc(kind.label), { cls: 'kind-tag', icon: kind.icon })}<b class="grow ellipsis" title="${esc(d.fm.title || d.fm.what || '')}">${ev.isNew ? `New ${esc(kind.one)}` : esc(d.fm.title || d.fm.what || path)}</b>${UI.label(`unsaved`, { s: 'wait', cls: 'hide', attrs: 'id="ke-dirty"' })}${ev.proposal ? tag('approving ' + ev.proposal + ' with changes', 'wait') : ''}
        <div class="tools">${ev.isNew ? '' : `<button class="btn sm ghost${ev.blame ? ' on' : ''}" data-act="rp-blame" aria-pressed="${ev.blame}">${icon('bots')}Who wrote this</button><button class="btn sm ghost" data-act="rp-file-menu" data-repo="${id}" aria-label="Entry actions">${icon('more')}</button>`}</div></div>
      <div class="ke-path"><span class="caps">path</span><span class="mono" id="ke-path">${esc(path)}</span><span class="muted tiny">${kind.free ? `you choose the path inside ${esc(kind.dir)}/` : `derived from the kind and the ${kind.titleKey === 'what' ? '“what to watch”' : 'title'}; rename by changing it`}</span></div>
      <div class="readers small muted">${icon('eye')} read by ${readers(r).map((x) => `<a class="link mono" href="#/bots/${x}">${esc(x)}</a>`).join(', ') || 'no bot yet'}${r.kind === 'team' ? ` · team ${esc(r.name)}` : ' · its own memory'}${pins.includes(path) ? ' · <b>pinned</b>: always in its context' : ''} · ${esc(kind.who[r.kind] || '')}</div>
      ${ev.conflict ? conflictHTML(r) : behind && !isDirty() ? `<div class="callout info small">A newer commit (${ver}) changed this entry. <button class="link" data-act="rp-reload" data-repo="${id}">Load it</button></div>` : ''}
      ${errs.length ? `<div class="callout danger small">This entry fails the ${esc(kind.one)} schema: ${errs.map(esc).join('; ')}. Fix it and save.</div>` : ''}
      ${blame ? blameHTML : `<form class="ke-form stack" id="ke-form" data-pre="ke-" novalidate onsubmit="return false">
        ${kind.free ? `<div class="field" data-field="free"><label for="ke-free">Path in ${esc(kind.dir)}/ <span class="opt">this collection allows free paths</span></label><div class="ke-free"><span class="mono muted">${esc(kind.dir)}/</span><input class="input mono" id="ke-free" value="${esc(d.free || '')}" placeholder="${esc(slug(d.fm.title || 'my-entry'))}" aria-label="Path inside ${esc(kind.dir)}"><span class="mono muted">.md</span></div><div class="ferr" role="alert"></div></div>` : ''}
        ${fieldsHTML}
        ${kind.body ? `<div class="field"><div class="row"><label>${esc(kind.body)} <span class="opt">markdown</span></label><span style="margin-left:auto">${UI.seg([['edit', 'Edit'], ['split', 'Split'], ['preview', 'Preview']], ev.mode, 'rp-mode')}</span></div>
          <div class="ed-panes mode-${ev.mode}"><div class="ed-src">${UI.editor({ id: 'rp-ed', text: d.body || '', lang: 'md', act: 'rp-input', minRows: 8, label: kind.body + ' (markdown)' })}</div><div class="ed-prev prose" id="rp-prev">${UI.mdRender(d.body || '')}</div></div></div>` : ''}
      </form>`}
      ${c && !ev.isNew ? `<div class="cites small"><div class="caps">${kind.id === 'runbook' ? 'used in' : 'cited by'}</div>${c.runbooks.map((x) => `<a class="cite-link" href="#" data-act="rp-hit" data-repo="${x.repo}" data-key="${esc(x.path)}">${icon('workflows')}${esc(x.title)}</a>`).join('')}${c.cases.map((x) => caseHref(x) ? `<a class="cite-link" href="${caseHref(x)}">${icon('runs')}task ${esc(x)}</a>` : `<span class="cite-link">${icon('runs')}${esc(x)}</span>`).join('')}${c.logs.map((x) => `<a class="cite-link" href="#" data-act="rp-hit" data-repo="${x.repo}" data-key="${esc(x.path)}">${icon('clock')}${esc(x.title)}</a>`).join('')}${!c.runbooks.length && !c.cases.length && !c.logs.length ? '<span class="muted">nothing yet</span>' : ''}</div>` : ''}
      <div class="form-err callout danger small hide" id="ke-err" role="alert"></div>
      <div class="panel-foot ed-foot"><input class="input grow" id="rp-msg" placeholder="Commit message (optional)" aria-label="Commit message">
        ${ev.isNew ? '' : `<button class="btn sm ghost" data-act="rp-sim" data-repo="${id}">${icon('commit')}Simulate a bot commit</button>`}
        ${team && tk && tk.cfg.knowledge && !ev.isNew && (kind.id === 'knowledge' || kind.id === 'runbook') ? `<button class="btn sm" data-act="rp-promote" data-repo="${id}">${icon('upload')}Promote to team ${esc(team)}</button>` : ''}
        <button class="btn sm ghost" data-act="rp-discard" ${ev.isNew ? '' : UI.off('No unsaved changes')}>${ev.isNew ? 'Cancel' : 'Discard'}</button>
        <button class="btn primary" data-act="rp-save" data-repo="${id}" ${ev.proposal ? '' : UI.off(ev.isNew ? 'Fill in the fields first' : 'Nothing to save yet: change a field first')}>${icon('commit')}${ev.proposal ? 'Approve with changes' : 'Save · one commit'}</button></div>
      <div class="muted tiny ed-sign">Saved as you: <span class="mono">Authored-By: operator:ops · Edited-Via: dashboard</span>. No approval: operator provenance is trusted.</div></div>`;
  }
  function readHTML(r, kind) {
    const e = entriesOf(r, kind).find((x) => x.key === ev.key); if (!e) return '';
    return `<div class="panel repo-ed ke" data-kind="${kind.id}"><div class="panel-head ed-head">${UI.label(esc(kind.label), { cls: 'kind-tag', icon: kind.icon })}<b class="grow ellipsis">${esc(e.title)}</b>${e.fm.flagged_wrong ? tag('flagged as wrong', 'fail') : ''}${e.fm.case && caseHref(e.fm.case) ? `<a class="btn sm ghost" href="${caseHref(e.fm.case)}">${icon('runs')}case ${esc(e.fm.case)}</a>` : ''}</div>
      <div class="ke-path"><span class="caps">path</span><span class="mono">${esc(e.path)}</span><span class="muted tiny">${kind.id === 'log' ? `written by the bot, ${e.fm.case ? 'task ' + esc(e.fm.case) : e.fm.sweep ? 'sweep ' + esc(e.fm.sweep) : 'per case'}` : 'system-generated'}</span></div>
      <div class="callout small">${icon('lock')} ${esc(kind.readonly)}</div>
      <div class="panel-body prose">${UI.mdRender(e.body)}</div>
      ${kind.id === 'log' ? `${e.fm.flagged_wrong ? `<div class="callout danger small" style="margin:0 16px">${icon('warn')} Flagged wrong: ${esc(e.fm.flagged_wrong)}</div>` : ''}<div class="panel-body"><div class="row"><button class="btn sm" data-act="rp-annotate" data-repo="${r.id}">${icon('edit')}Add a note</button><button class="btn sm secondary" data-act="rp-flag" data-repo="${r.id}">${icon('warn')}Flag as wrong</button><span class="muted tiny">appended below the bot's text with your name; nothing is rewritten</span></div></div>` : ''}</div>`;
  }
  function rawHTML(r) {
    const p = ev.file, k = kindOfPath(r, p), txt = r.files[p] || '';
    return `<div class="panel repo-ed"><div class="panel-head ed-head"><span class="mono small grow ellipsis" title="${esc(p)}">${esc(p)}</span>${k ? tag(k.label) : tag('untyped', 'wait')}
      <div class="tools">${k ? `<button class="btn sm" data-act="rp-as-kind" data-repo="${r.id}" data-path="${esc(p)}">Open as ${esc(k.one)}</button>` : `<button class="btn sm primary" data-act="rp-map-file" data-repo="${r.id}" data-path="${esc(p)}">Map into a kind…</button>`}</div></div>
      <div class="muted small" style="padding:8px 16px 0">The files view is read-mostly: entries are edited through their kind, so they keep their fields and their place.</div>
      <pre class="raw mono">${esc(txt)}</pre></div>`;
  }
  function conflictHTML(r) {
    const c = ev.conflict;
    return `<div class="conflict"><div class="callout warn small"><b>Conflict.</b> ${esc(whoLabel(c.commit))} committed <span class="mono">${c.commit.sha}</span> to this entry after you opened it. Choose explicitly:</div>
      <div class="grid g2"><div><div class="caps">their change</div>${UI.diffView(c.base, c.theirs, 'unified', { file: pathOf() }).html}</div><div><div class="caps">your change</div>${UI.diffView(c.base, c.mine, 'unified', { file: pathOf() }).html}</div></div>
      <div class="row wrap" style="margin-top:8px"><button class="btn primary" data-act="rp-rebase" data-repo="${r.id}">${icon('branch')}Rebase my edit on theirs</button><button class="btn secondary" data-act="rp-overwrite" data-repo="${r.id}">Overwrite theirs with mine</button><button class="btn ghost" data-act="rp-take-theirs" data-repo="${r.id}">Discard mine, keep theirs</button></div></div>`;
  }
  function proposalsHTML(r) {
    const all = r.proposals;
    return `<div class="panel" style="margin-bottom:14px"><div class="panel-head"><h3>Proposals from bots</h3><span class="muted small">each is checked against its kind's schema</span>
      <div class="tools"><button class="btn sm" ${ev.sel.size ? `data-act="kp-bulk" data-repo="${r.id}" data-d="approve"` : UI.off('Tick proposals first')}>${icon('passkey')}Approve selected${ev.sel.size ? ` (${ev.sel.size})` : ''}</button><button class="btn sm secondary" ${ev.sel.size ? `data-act="kp-bulk" data-repo="${r.id}" data-d="deny"` : UI.off('Tick proposals first')}>Deny selected</button></div></div>
      <div class="list">${all.map((p) => { const k = kindById(r, p.kind === 'knowledge' || p.kind === 'runbook' || p.kind === 'watch' ? p.kind : 'c:' + p.kind) || KINDS.knowledge; const x = FM.parse(p.content || ''); const errs = p.status === 'pending' ? (x.err ? [x.err] : validate(k, x.fm, x.fm.kind)) : [];
        return `<div class="item kprop" style="align-items:flex-start" data-prop="${p.id}">${p.status === 'pending' ? `<input type="checkbox" class="cbx" data-on-change="kp-sel" data-id="${p.id}" ${ev.sel.has(p.id) ? 'checked' : ''} aria-label="Select ${p.id}">` : UI.st(p.status.startsWith('approved') ? 'ok' : 'fail', '')}
        <div class="grow small"><div class="row wrap" style="gap:6px">${UI.label(esc(k.label), { cls: 'kind-tag', icon: k.icon })}<b>${esc(p.summary)}</b></div>${UI.factsLine([{ icon: 'obj-bot', value: p.bot, href: `#/bots/${p.bot}`, pri: 1, label: 'Proposed by' }, { icon: 'file', value: p.to, pri: 2, label: 'Goes to' }, (() => { const c = p.case && (D.cases || []).find((x) => x.id === p.case); return c ? { icon: 'ticket', value: c.title, href: `#/tickets/${c.id}`, pri: 3, label: 'For the ticket' } : null; })(), { icon: 'clock', value: `${ago(p.at)} ago`, pri: 2, label: 'Proposed' }, p.approver ? { icon: 'passkey', value: p.approver, pri: 3, label: 'Decided by' } : null])}${p.reason ? `<div class="tiny">${UI.statusMark('fail', `denied: ${esc(p.reason)}`, { sm: true })}</div>` : ''}
          ${errs.length ? `<div class="schema-errs"><b>Fails the ${esc(k.one)} schema</b><ul>${errs.map((e) => `<li class="mono">${esc(e)}</li>`).join('')}</ul></div>` : ''}
          ${p.status === 'pending' && !errs.length ? (r.files[p.to] != null ? UI.diffView(r.files[p.to], p.content, 'unified', { file: p.to }).html : `<div class="prop-fields">${k.fields.filter((f) => x.fm[f.k] != null && f.type !== 'steps').map((f) => `<div><span class="caps">${esc(f.label || f.k.replace(/_/g, ' '))}</span> ${esc(Array.isArray(x.fm[f.k]) ? x.fm[f.k].join(', ') : x.fm[f.k])}</div>`).join('')}</div>`) : ''}</div>
        ${p.status === 'pending' ? `<div class="row wrap kp-acts" style="gap:4px"><button class="btn sm primary" ${errs.length ? UI.off(`Fails the ${k.one} schema: ${errs[0]}${errs.length > 1 ? ` (+${errs.length - 1} more)` : ''}. Approve it with changes, or deny it.`) : `data-act="kp-approve" data-repo="${r.id}" data-id="${p.id}"`}>${icon('passkey')}Approve</button><button class="btn sm" data-act="kp-edit" data-repo="${r.id}" data-id="${p.id}">${icon('edit')}Approve with changes</button><button class="btn sm secondary" data-act="kp-deny" data-repo="${r.id}" data-id="${p.id}">Deny</button></div>` : tag(p.status, p.status.startsWith('approved') ? 'ok' : 'fail')}</div>`; }).join('') || '<div class="item small muted">No proposals.</div>'}</div></div>`;
  }
  function searchHTML(r) {
    const ql = ev.q.toLowerCase(), out = [];
    related(r).forEach((x) => kindsFor(x).forEach((k) => entriesOf(x, k).forEach((e) => { const hay = JSON.stringify(e.fm) + '\n' + (e.body || ''); if (hay.toLowerCase().includes(ql)) { const line = (e.body || '').split('\n').find((l) => l.toLowerCase().includes(ql)) || Object.values(e.fm).map(String).find((v) => v.toLowerCase().includes(ql)) || ''; out.push({ repo: x.id, kind: k, e, snip: line.trim() }); } })));
    return `<div class="list search-res">${out.map((x) => `<a href="#" class="item click small" data-act="rp-hit" data-repo="${x.repo}" data-key="${esc(x.e.key)}"><div class="grow"><div class="mono tiny muted">${esc(x.repo)} · ${esc(x.kind.label)}</div><b>${esc(x.e.title)}</b><div class="muted tiny ellipsis" title="${esc(x.snip)}">${esc(x.snip)}</div></div></a>`).join('') || '<div class="item small muted">No match.</div>'}<button class="btn sm ghost" data-act="rp-qclear" style="margin:6px">${icon('x')}Clear search</button></div>`;
  }

  /* ---------- actions ---------- */
  const repoOf = (b) => R.get(b.dataset.repo || ev.repo);
  const rerender = () => UI.rerender();
  UI.act('rp-kind', (a) => { const r = repoOf(a); guardThen(() => { const k = kindById(r, a.dataset.k); ev.kind = k.id; ev.view = 'kinds'; const e = entriesOf(r, k)[0]; if (e) openEntry(r, k, e); else { ev.key = null; ev.isNew = false; r.openKind = k.id; } rerender(); }); });
  UI.act('rp-open', (a) => { const r = repoOf(a); guardThen(() => { const k = kindById(r, a.dataset.k), e = entriesOf(r, k).find((x) => x.key === a.dataset.key); if (e) openEntry(r, k, e); rerender(); }); });
  UI.act('rp-view', (b) => guardThen(() => { ev.view = b.dataset.v; rerender(); }));
  UI.act('rp-file', (a) => { ev.file = a.dataset.path; rerender(); });
  UI.act('rp-as-kind', (b) => { const r = repoOf(b), k = kindOfPath(r, b.dataset.path), e = entriesOf(r, k).find((x) => x.path === b.dataset.path); if (e) openEntry(r, k, e); rerender(); });
  UI.act('rp-mode', (b) => { const ta = UI.$('#rp-ed .ced-ta'); if (ta && ev.draft) ev.draft.body = ta.value; const r = R.get(ev.repo), k = kindById(r, ev.kind); if (k && UI.$('#ke-form')) { const d = readDraft(k); ev.draft = d; } ev.mode = b.dataset.v; ev.blame = false; rerender(); });
  UI.act('rp-blame', () => { const r = R.get(ev.repo), k = kindById(r, ev.kind); if (UI.$('#ke-form')) ev.draft = readDraft(k); ev.blame = !ev.blame; rerender(); });
  UI.act('rp-hist', (b) => { const r = R.get(ev.repo), k = r && kindById(r, ev.kind); if (k && UI.$('#ke-form')) ev.draft = readDraft(k); ev.hist = b.dataset.v; rerender(); });
  UI.act('rp-q', (el) => { ev.q = el.value.trim(); rerender(); });
  UI.act('rp-qclear', () => { ev.q = ''; rerender(); });
  UI.act('rp-hit', (a) => guardThen(() => { const x = R.get(a.dataset.repo), p = a.dataset.key.split('#')[0], k = kindOfPath(x, p); x.open = a.dataset.key; x.openKind = k && k.id; ev.q = ''; S.save(); const [kk, n] = a.dataset.repo.split(':'); const href = kk === 'team' ? `#/teams/${n}/knowledge` : `#/bots/${n}/memory`; ev.repo = null; if (location.hash.startsWith(href)) rerender(); else UI.go(href); }));
  UI.act('rp-input', () => later());
  UI.act('rp-discard', () => { if (ev.isNew && !isDirty()) { ev.isNew = false; ev.repo = null; return rerender(); } UI.confirm({ title: ev.isNew ? 'Discard this new entry?' : 'Discard your edits?', body: ev.isNew ? 'Nothing was saved.' : `Puts <span class="mono">${esc(pathOf())}</span> back to the last commit.`, ok: 'Discard', cancel: 'Keep editing', danger: true }).then((ok) => { if (!ok) return; const r = R.get(ev.repo), k = kindById(r, ev.kind); if (ev.isNew) { ev.isNew = false; ev.repo = null; } else openEntry(r, k, entriesOf(r, k).find((x) => x.key === ev.key)); rerender(); }); });
  UI.act('rp-reload', (b) => { const r = repoOf(b), k = kindById(r, ev.kind); openEntry(r, k, entriesOf(r, k).find((x) => x.key === ev.key)); rerender(); });
  function showErrs(kind, errs) {
    const f = UI.$('#ke-form'); if (!f) return;
    f.querySelectorAll('.field').forEach((x) => { x.classList.remove('bad'); const fe = x.querySelector('.ferr'); if (fe) fe.textContent = ''; });
    errs.forEach((e) => { const k = e.split(/[.:[]/)[0]; const box = f.querySelector(`[data-field="${k}"]`); if (box) { box.classList.add('bad'); const fe = box.querySelector('.ferr'); if (fe) fe.textContent = (fe.textContent ? fe.textContent + ' · ' : '') + e.replace(/^[\w]+:\s*/, ''); } });
    const box = UI.$('#ke-err'); box.textContent = `Fails the ${kind.one} schema: ${errs.join('; ')}`; box.classList.remove('hide');
  }
  function save(r, opts = {}) {
    const kind = kindById(r, ev.kind), d = readDraft(kind);
    const errs = validate(kind, d.fm);
    if (kind.free && d.free && !/^[a-z0-9][\w./-]*$/i.test(d.free)) errs.push('free: a relative path such as vendors/acme (no .. or leading /)');
    if (kind.free && /\.\./.test(d.free)) errs.push('free: no .. in paths');
    if (errs.length) { showErrs(kind, errs); return; }
    const msgIn = (UI.$('#rp-msg') || {}).value;
    const prop = ev.proposal && r.proposals.find((x) => x.id === ev.proposal);
    const author = prop ? { who: 'bot:' + prop.bot, kind: 'bot', case: prop.case, trailers: `Promoted-By: bot:${prop.bot} · Approved-By: operator:ops · Edited-By: operator:ops` } : { trailers: 'Authored-By: operator:ops · Edited-Via: dashboard' };
    const path = derive(kind, titleOf(kind, d.fm), d.free), from = ev.isNew ? null : pathOf();
    if (path !== from && r.files[path] != null) return showErrs(kind, [`title: an entry with this title already exists (${path})`]);
    const ver = from && R.version(r, from); if (!opts.force && from && ev.baseSha && ver && ver !== ev.baseSha) return conflict(r, kind, d, from);
    const text = FM.emit(kind, d.fm, d.body);
    const c = R.commit(r, Object.assign({ msg: msgIn || `${ev.isNew ? 'add' : from !== path ? 'rename' : 'edit'} ${kind.one} ${path}${opts.force ? ` (overwrote ${ver})` : ''}`, changes: [{ path, from: from && from !== path ? from : undefined, before: from ? r.files[from] : null, after: text }] }, author));
    if (from && from !== path) { const pins = (r.files['.pins'] || '').split('\n').filter(Boolean); if (pins.includes(from)) r.files['.pins'] = pins.map((p) => (p === from ? path : p)).join('\n'); }
    finishSave(r, kind, entriesOf(r, kind).find((x) => x.path === path), c, prop);
  }
  function finishSave(r, kind, entry, c, prop) {
    if (prop) { prop.status = 'approved (edited)'; prop.approver = 'ops (passkey · laptop), with changes'; }
    openEntry(r, kind, entry); S.save(); rerender();
    UI.toast(prop ? `Approved ${prop.id} with changes` : `Committed ${c.sha}`, prop ? 'one commit, the bot as author, your edit recorded' : `one commit by ops · ${esc(entry ? entry.path : '')}`, 'ok');
  }
  function conflict(r, kind, d, path) { const ver = R.version(r, path), c = r.log.find((x) => x.sha === ver); const base = r.log.find((x) => x.sha === ev.baseSha); const baseText = base ? (base.changes.find((ch) => ch.path === path) || {}).after || '' : ''; ev.draft = d; ev.conflict = { commit: c, theirs: r.files[path], base: baseText, mine: FM.emit(kind, d.fm, d.body) }; rerender(); }
  UI.act('rp-save', (b) => {
    const r = repoOf(b);
    if (ev.proposal) { const p = r.proposals.find((x) => x.id === ev.proposal); const kind = kindById(r, ev.kind), errs = validate(kind, readDraft(kind).fm); if (errs.length) return showErrs(kind, errs); UI.stepup({ title: `Approve ${p.id} with your changes`, purpose: 'team knowledge', ok: 'Approve with passkey', what: `{ team: <b>${esc(r.name)}</b>, proposal: <b>${p.id}</b>, kind: <b>${esc(kind.id)}</b>, edited_by: <b>operator:ops</b> }` }).then((ok) => { if (ok) save(r); }); return; }
    save(r);
  });
  UI.act('rp-overwrite', (b) => { const r = repoOf(b); ev.conflict = null; rerender(); setTimeout(() => save(r, { force: true }), 0); });
  UI.act('rp-take-theirs', (b) => { const r = repoOf(b), k = kindById(r, ev.kind); openEntry(r, k, entriesOf(r, k).find((x) => x.key === ev.key)); rerender(); });
  UI.act('rp-rebase', (b) => {
    // field-wise: every field you changed (vs. what you opened) wins; the rest is theirs. The body merges line by line.
    const r = repoOf(b), k = kindById(r, ev.kind), c = ev.conflict;
    const pb = FM.parse(c.base);
    const pt = FM.parse(c.theirs);
    const mine = ev.draft, fm = Object.assign({}, pt.fm);
    k.fields.forEach((f) => { if (JSON.stringify(mine.fm[f.k]) !== JSON.stringify(pb.fm[f.k])) fm[f.k] = mine.fm[f.k]; });
    let out = (pt.body || '').split('\n'), anchor = null;
    YAML.diff(pb.body || '', mine.body || '').forEach((x) => { if (x.t === ' ') anchor = x.s; else if (x.t === '-') { const i = out.indexOf(x.s); if (i >= 0) out.splice(i, 1); } else { const i = anchor == null ? -1 : out.lastIndexOf(anchor); out.splice(i + 1, 0, x.s); anchor = x.s; } });
    const entry = entriesOf(r, k).find((x) => x.key === ev.key);
    openEntry(r, k, entry, { fm, body: out.join('\n'), free: mine.free });
    ev.base = serialize(k, { fm: entry.fm, body: entry.body, free: mine.free }); rerender(); later();
    UI.toast('Rebased on their commit', 'review the result, then save', 'run');
  });
  UI.act('rp-sim', (b) => {
    const r = repoOf(b), k = kindById(r, ev.kind), who = r.kind === 'bot' ? r.name : ((D.teams.find((t) => t.name === r.name) || { cfg: {} }).cfg.lead || botsOfTeam(r.name)[0] || 'platform-sre');
    const p = pathOf(), cur = r.files[p] || '';
    const after = cur.replace(/\n?$/, '\n') + `- ${who}: seen again during c29 (auto-noted)\n`;
    R.commit(r, { msg: `note from ${who}`, who: 'bot:' + who, kind: 'bot', case: 'c29', changes: [{ path: p, before: cur, after }] });
    const dirty = isDirty(); if (UI.$('#ke-form')) ev.draft = readDraft(k); const base = ev.base; rerender(); ev.base = base; later();
    UI.toast(`${who} committed to ${esc(p)}`, dirty ? 'saving now will show a conflict' : 'load it to see the change', 'run');
  });
  /* New… : choose a kind; the path follows from it */
  const newEntry = (r, k) => { openEntry(r, k, null); ev.mode = 'split'; rerender(); setTimeout(() => { const t = UI.$('#ke-title, #ke-what'), a = document.activeElement; if (t && !(a && a.closest && a.closest('#ke-form'))) t.focus(); }, 30); }; // never steal focus from a field already being typed in
  UI.act('rp-new-kind', (b) => { const r = repoOf(b); guardThen(() => newEntry(r, kindById(r, b.dataset.k))); });
  UI.act('rp-new', (b) => {
    const r = repoOf(b);
    guardThen(() => {
      UI.modal(`<div class="modal-head"><div><div class="caps">${esc(r.id)}</div><h2>New…</h2></div><button class="icon-btn" data-close aria-label="Close">${icon('x')}</button></div>
        <div class="modal-body stack"><p class="small dim" style="margin:0">Pick what you're writing. Its kind decides where it lives and what it needs: knowledge goes in knowledge/, runbooks in runbooks/.</p>
          <div class="choice-grid">${kindsFor(r).map((k) => UI.choice({ act: 'rp-new-pick', attrs: `data-repo="${r.id}" data-k="${k.id}"`, icon: k.icon, title: esc(k.coll ? `${k.label}` : k.one.replace(/^./, (c) => c.toUpperCase())), desc: esc(`${k.dir}/ · ${(k.fields || []).map((f) => f.k.replace(/_/g, ' ')).join(', ')}${k.free ? ' · free paths' : ''}${k.pack ? ` · from pack ${k.pack}` : ''}`), off: writable(r, k) })).join('')}</div></div>
        <div class="modal-foot"><a class="btn ghost" href="#/config/collections">Declare a collection…</a><button class="btn ghost" data-close>Cancel</button></div>`, { wide: true });
    });
  });
  UI.act('rp-new-pick', (b) => { const r = repoOf(b); UI.closeModal(); newEntry(r, kindById(r, b.dataset.k)); });
  UI.act('rp-file-menu', (b) => { const r = repoOf(b), k = kindById(r, ev.kind), pins = (r.files['.pins'] || '').split('\n').filter(Boolean); UI.menu(b, [[pins.includes(pathOf()) ? 'Unpin' : 'Pin (always in context)', 'pin', 'rp-pin', r.kind !== 'bot' ? UI.off('Pinning is for a bot\'s own memory') : `data-repo="${r.id}"`], ['-'], ['Delete…', 'trash', 'rp-delete', `data-repo="${r.id}"`]]); });
  UI.act('rp-delete', (b) => { const r = repoOf(b), k = kindById(r, ev.kind), p = pathOf(); UI.confirm({ title: `Delete this ${esc(k.one)}?`, body: `One commit removes <span class="mono">${esc(ev.key)}</span>. Revert that commit to bring it back.`, ok: 'Delete', cancel: 'Keep it', danger: true }).then((ok) => { if (!ok) return;
    R.commit(r, { msg: `delete ${k.one} ${p}`, changes: [{ path: p, before: r.files[p], after: null }], trailers: 'Authored-By: operator:ops' });
    ev.base = ''; ev.repo = null; r.open = null; rerender(); UI.toast(`Deleted ${esc(ev.key || p)}`, 'revert from history to restore it', 'ok'); }); });
  UI.act('rp-pin', (b) => { const r = repoOf(b), p = pathOf(), pins = (r.files['.pins'] || '').split('\n').filter(Boolean), on = pins.includes(p); const next = on ? pins.filter((x) => x !== p) : pins.concat([p]); R.commit(r, { msg: `${on ? 'unpin' : 'pin'} ${p}`, changes: [{ path: '.pins', before: r.files['.pins'] || null, after: next.join('\n') }], trailers: 'Authored-By: operator:ops' }); rerender(); });
  UI.act('rp-diff', (b) => { const r = repoOf(b), c = r.log.find((x) => x.sha === b.dataset.sha); UI.modal(`<div class="modal-head"><div><div class="caps">${c.sha} · ${esc(whoLabel(c))} · ${ago(c.at)} ago</div><h2>${esc(c.msg)}</h2></div><button class="icon-btn" data-close aria-label="Close">${icon('x')}</button></div><div class="modal-body">${c.trailers ? `<div class="mono tiny muted" style="margin-bottom:8px">${esc(c.trailers)}</div>` : ''}${c.changes.filter((ch) => visible(ch.path)).map((ch) => UI.diffView(ch.before || '', ch.after || '', 'unified', { file: ch.from && ch.from !== ch.path ? `${ch.from} → ${ch.path}` : ch.path }).html).join('')}</div><div class="modal-foot"><button class="btn" data-close>Close</button></div>`, { wide: true }); });
  UI.act('rp-revert', (b) => { const r = repoOf(b), c = r.log.find((x) => x.sha === b.dataset.sha); UI.confirm({ title: `Revert ${c.sha}?`, body: `A new commit undoes “${esc(c.msg)}”. History is kept.`, ok: 'Revert' }).then((ok) => { if (!ok) return; const changes = c.changes.slice().reverse().map((ch) => (ch.from && ch.from !== ch.path ? { path: ch.from, from: ch.path, before: ch.after, after: ch.before } : { path: ch.path, before: r.files[ch.path] != null ? r.files[ch.path] : null, after: ch.before })); R.commit(r, { msg: `Revert "${c.msg}"`, changes, trailers: `Authored-By: operator:ops · Reverts: ${c.sha}` }); ev.repo = null; rerender(); }); });
  UI.act('rp-promote', (b) => { const r = repoOf(b), team = D.bots.find((x) => x.name === r.name).cfg.team, tr = R.get('team:' + team), k = kindById(r, ev.kind), p = pathOf(), x = FM.parse(r.files[p]); const id = 'k' + (10 + tr.proposals.length); tr.proposals.unshift({ id, bot: r.name, kind: k.id, to: derive(k, x.fm.title), summary: `Promote ${k.one} “${x.fm.title || p}” from ${r.name}`, status: 'pending', at: now(), content: r.files[p] }); S.save(); UI.go(`#/teams/${team}/knowledge`); });
  /* log: annotate or flag, never rewrite */
  // conductor's operator edit for the log: a "## Operator note" (or "## Flagged WRONG") section appended; flagging also sets flagged_wrong
  const annotate = (r, flag, text) => { const p = pathOf(), cur = r.files[p], x = FM.parse(cur), at = now(); const fm = Object.assign({}, x.fm); if (flag) fm.flagged_wrong = text.replace(/\s+/g, ' ').slice(0, 500); const entry = `\n## ${flag ? 'Flagged WRONG by the operator' : 'Operator note'} — ops, ${at}\n\n${text}\n`; const next = '---\n' + YAML.stringify(fm).replace(/\n?$/, '\n') + '---\n' + String(x.body || '').replace(/\n*$/, '\n') + entry; R.commit(r, { msg: `${flag ? 'flag wrong: ' : 'annotate '}${p}`, changes: [{ path: p, before: cur, after: next }], trailers: 'Authored-By: operator:ops' }); rerender(); };
  UI.act('rp-annotate', (b) => { const r = repoOf(b); UI.form({ caps: pathOf(), title: 'Annotate this log entry', fields: [{ k: 'text', type: 'text', req: true, label: 'Note' }], submit: 'Add note', onSubmit: (v) => annotate(r, false, v.text) }); });
  UI.act('rp-flag', (b) => { const r = repoOf(b); UI.form({ caps: pathOf(), title: 'Flag as wrong', intro: 'The entry stays as the bot wrote it; your flag and reason sit beside it, and the bot reads them.', fields: [{ k: 'text', type: 'text', req: true, label: 'What is wrong' }], submit: 'Flag', onSubmit: (v) => annotate(r, true, v.text) }); });

  /* ---------- import: markdown mapped into a kind ---------- */
  const sections = (md) => { let title = ''; const secs = { _intro: [] }; let cur = '_intro'; String(md).split('\n').forEach((l) => { const h1 = l.match(/^#\s+(.*)/), h = l.match(/^#{2,3}\s+(.*)/); if (h1 && !title) { title = h1[1].trim(); return; } if (h) { cur = h[1].trim().toLowerCase(); secs[cur] = []; return; } secs[cur].push(l); }); return { title, secs }; };
  const items = (ls) => (ls || []).map((l) => l.match(/^\s*(?:\d+[.)]|[-*+])\s+(.*)$/)).filter(Boolean).map((m) => m[1].trim());
  const sec = (secs, re) => { const k = Object.keys(secs).find((x) => x !== '_intro' && re.test(x)); return k ? secs[k] : null; };
  const txt = (ls) => (ls || []).join('\n').trim();
  R.detect = (md) => { const { secs } = sections(md); const numbered = String(md).split('\n').filter((l) => /^\s*\d+[.)]\s/.test(l)).length; return numbered >= 2 && (sec(secs, /step|procedure/) || sec(secs, /verif|rollback|when|symptom/)) ? 'runbook' : 'knowledge'; };
  R.convert = (kind, md, name) => {
    const { title, secs } = sections(md), t = title || String(name || 'imported').replace(/\.\w+$/, '').replace(/[-_]/g, ' ');
    if (kind.id === 'runbook') {
      const stepLines = sec(secs, /step|procedure/) || String(md).split('\n').filter((l) => /^\s*\d+[.)]\s/.test(l));
      const steps = items(stepLines).map((x) => { const m = x.match(/`([^`]+)`/); return m ? `${x.replace(/`[^`]+`/, '').replace(/[\s:—-]+$/, '').trim() || 'Run it'} (run: \`${m[1]}\`)` : x; });
      return { fm: { title: t, when_to_use: txt(sec(secs, /when|symptom|trigger/)) || txt(secs._intro), prerequisites: items(sec(secs, /prereq|before you/)), steps, verification: txt(sec(secs, /verif|check it worked|done when/)), rollback: txt(sec(secs, /rollback|undo|back out/)), owner: 'ops' }, body: '' };
    }
    if (kind.id === 'knowledge') { const paras = txt(secs._intro).split(/\n\s*\n/); return { fm: { title: t, evidence: `imported from ${name || 'a paste'}`, confidence: 'medium' }, body: paras.join('\n\n') + Object.entries(secs).filter(([k]) => k !== '_intro').map(([k, v]) => `\n\n## ${k}\n${txt(v)}`).join('') }; }
    return { fm: { title: t }, body: String(md).replace(/^#\s+.*\n/, '').trim() + '\n' };
  };
  const summary = (kind, d) => { const errs = validate(kind, d.fm); const bits = kind.id === 'runbook' ? [`${(d.fm.steps || []).length} steps`, `when to use ${d.fm.when_to_use ? '✓' : '✗'}`, `verification ${d.fm.verification ? '✓' : '✗'}`, `rollback ${d.fm.rollback ? '✓' : '✗ (you add it)'}`] : kind.id === 'knowledge' ? [`evidence ✓`, `applies to ${(d.fm.applies_to || []).length ? '✓' : '– (optional)'}`] : ['body ✓']; return `${bits.join(' · ')}${errs.length ? ` · <span class="danger-text">${errs.length} field${errs.length > 1 ? 's' : ''} to fill</span>` : ' · <span class="ok-text">ready</span>'}`; };
  function importMap(r, docs) {
    const choices = kindsFor(r).filter((k) => !writable(r, k) && k.id !== 'watch');
    const st = docs.map((d) => ({ name: d.name, text: d.text, kind: R.detect(d.text) }));
    const s = UI.modal(`<div class="modal-head"><div><div class="caps">${esc(r.id)} · import</div><h2>Map into a kind</h2></div><button class="icon-btn" data-close aria-label="Close">${icon('x')}</button></div><div class="modal-body stack" id="im-body"></div>
      <div class="modal-foot"><button class="btn ghost" data-close>Cancel</button><button class="btn primary" id="im-go">${icon('commit')}Import</button></div>`, { wide: true });
    const draw = () => { UI.$('#im-body', s).innerHTML = `<p class="small dim" style="margin:0">Each file becomes one entry of the kind you confirm; its path follows from the kind and its title.</p>${st.map((x, i) => { const k = kindById(r, x.kind) || KINDS.knowledge, d = R.convert(k, x.text, x.name); return `<div class="im-row"><div class="row wrap"><b class="mono small">${esc(x.name)}</b><span class="muted tiny">detected: ${esc(KINDS[R.detect(x.text)].one)}</span><label class="row small" style="margin-left:auto">as <select class="select sm" data-im="${i}" aria-label="Kind for ${esc(x.name)}">${choices.map((c) => `<option value="${c.id}"${c.id === x.kind ? ' selected' : ''}>${esc(c.coll ? c.label : c.one)}</option>`).join('')}</select></label></div><div class="mono tiny">→ ${esc(derive(k, titleOf(k, d.fm)))}</div><div class="tiny muted">${summary(k, d)}</div></div>`; }).join('')}`;
      s.querySelectorAll('[data-im]').forEach((sel) => sel.addEventListener('change', () => { st[+sel.dataset.im].kind = sel.value; draw(); })); };
    draw();
    UI.$('#im-go', s).addEventListener('click', () => {
      const ok = [], todo = [];
      st.forEach((x) => { const k = kindById(r, x.kind), d = R.convert(k, x.text, x.name), path = derive(k, d.fm.title); if (!validate(k, d.fm).length && r.files[path] == null) ok.push({ path, after: FM.emit(k, d.fm, d.body) }); else todo.push({ k, d }); });
      if (ok.length) R.commit(r, { msg: `import ${ok.length} entr${ok.length > 1 ? 'ies' : 'y'}`, changes: ok.map((x) => ({ path: x.path, before: null, after: x.after })), trailers: 'Authored-By: operator:ops · Imported-Via: dashboard' });
      UI.closeModal();
      if (todo.length) { openEntry(r, todo[0].k, null, todo[0].d); ev.base = serialize(todo[0].k, { fm: {}, body: '', free: '' }); rerender(); setTimeout(() => { const f = UI.$('#ke-form'); if (f) showErrs(todo[0].k, validate(todo[0].k, todo[0].d.fm)); }, 40); UI.toast(ok.length ? `Imported ${ok.length}; ${todo.length} need${todo.length > 1 ? '' : 's'} fields` : 'Fill in what the import couldn\'t find', 'the first one is open: complete it and save', 'run'); }
      else { const k = kindById(r, st[0].kind); const e = entriesOf(r, k).find((x) => x.path === ok[0].path); if (e) openEntry(r, k, e); rerender(); UI.toast(`Imported ${ok.length}`, 'one commit by you', 'ok'); }
    });
  }
  UI.act('rp-map-file', (b) => { const r = repoOf(b); importMap(r, [{ name: b.dataset.path, text: r.files[b.dataset.path] }]); });
  UI.act('rp-upload', (b) => {
    const r = repoOf(b);
    const s = UI.modal(`<div class="modal-head"><div><div class="caps">${esc(r.id)}</div><h2>Import markdown</h2></div><button class="icon-btn" data-close aria-label="Close">${icon('x')}</button></div>
      <div class="modal-body stack">${UI.seg([['drop', 'Drop files'], ['paste', 'Paste'], ['repo', 'From a repo']], 'drop', 'up-mode')}
        <div class="up-pane" data-pane="drop"><label class="dropzone" id="up-drop">${icon('upload')}<span><b>Drop markdown files here</b> or choose them</span><input type="file" id="up-file" accept=".md,.markdown,.txt,text/markdown,text/plain" multiple aria-label="Choose markdown files"></label><div id="up-list" class="small muted"></div></div>
        <div class="up-pane hide" data-pane="paste"><div class="field"><label for="up-text">Paste markdown</label><textarea class="textarea mono" id="up-text" rows="9" placeholder="# Failover&#10;&#10;## Steps&#10;1. …"></textarea></div></div>
        <div class="up-pane hide" data-pane="repo"><div class="grid g2"><div class="field"><label for="up-repo">Repo</label><input class="input mono" id="up-repo" value="acme/infra"></div><div class="field"><label for="up-src">Path in repo</label><input class="input mono" id="up-src" value="docs/runbooks/failover.md"></div></div><div class="hint">Fetched through the gh connector (read).</div></div>
        <div class="callout small">Next you confirm which kind each file becomes. Nothing is placed by path.</div></div>
      <div class="modal-foot"><button class="btn ghost" data-close>Cancel</button><button class="btn primary" id="up-go">Next: map into kinds ${icon('chev')}</button></div>`, { wide: true });
    let mode = 'drop', picked = [];
    const showPicked = () => { UI.$('#up-list', s).innerHTML = picked.map((f) => `<div class="mono">${esc(f.name)} · ${f.text.length} chars</div>`).join(''); };
    const take = (fs) => Promise.all([...fs].map((f) => f.text().then((text) => ({ name: f.name, text })))).then((xs) => { picked = picked.concat(xs); showPicked(); });
    UI.$('#up-file', s).addEventListener('change', (e) => take(e.target.files));
    const dz = UI.$('#up-drop', s); dz.addEventListener('dragover', (e) => { e.preventDefault(); dz.classList.add('over'); }); dz.addEventListener('dragleave', () => dz.classList.remove('over')); dz.addEventListener('drop', (e) => { e.preventDefault(); dz.classList.remove('over'); take(e.dataTransfer.files); });
    UI.upMode = (v) => { mode = v; s.querySelectorAll('.up-pane').forEach((p) => p.classList.toggle('hide', p.dataset.pane !== v)); };
    UI.$('#up-go', s).addEventListener('click', () => {
      let docs;
      if (mode === 'drop') { if (!picked.length) { UI.$('#up-list', s).textContent = 'Choose or drop a file first.'; return; } docs = picked; }
      else if (mode === 'paste') { const t = UI.$('#up-text', s).value; if (!t.trim()) return UI.nudge(UI.$('#up-text', s), 'paste some markdown first'); docs = [{ name: 'pasted.md', text: t }]; }
      else { const src = UI.$('#up-src', s).value.trim(); docs = [{ name: src.split('/').pop(), text: `# Failover\n\nImported from ${UI.$('#up-repo', s).value.trim()}:${src}.\n\n## When\nThe primary is unhealthy for 2 minutes.\n\n## Steps\n1. Check replication lag\n2. Promote the standby: \`aws rds failover-db-cluster\`\n3. Verify the app reconnects\n\n## Verify\nWrites succeed on the new primary.\n\n## Rollback\nFail back after recovery.\n` }]; }
      importMap(r, docs);
    });
  });
  UI.act('up-mode', (b) => { b.parentElement.querySelectorAll('button').forEach((x) => { x.classList.toggle('on', x === b); x.setAttribute('aria-pressed', x === b); }); UI.upMode(b.dataset.v); });

  /* ---------- proposals ---------- */
  UI.act('kp-sel', (cb) => { cb.checked ? ev.sel.add(cb.dataset.id) : ev.sel.delete(cb.dataset.id); rerender(); });
  const pkind = (r, p) => kindById(r, ['knowledge', 'runbook', 'watch'].includes(p.kind) ? p.kind : 'c:' + p.kind) || KINDS.knowledge;
  const perrs = (r, p) => { const x = FM.parse(p.content || ''); return x.err ? [x.err] : validate(pkind(r, p), x.fm, x.fm.kind); };
  const approve = (r, ps) => { ps.forEach((p) => { R.commit(r, { msg: `promote ${p.to} from ${p.bot}`, who: 'bot:' + p.bot, kind: 'bot', case: p.case, changes: [{ path: p.to, before: r.files[p.to] != null ? r.files[p.to] : null, after: p.content }], trailers: `Promoted-By: bot:${p.bot} · Approved-By: operator:ops` }); p.status = 'approved'; p.approver = 'ops (passkey · laptop)'; ev.sel.delete(p.id); }); r.open = ps[0].to; r.openKind = pkind(r, ps[0]).id; ev.repo = null; S.save(); rerender(); };
  const deny = (r, ps, reason) => { ps.forEach((p) => { p.status = 'denied'; p.reason = reason; ev.sel.delete(p.id); (D.chats[p.bot] = D.chats[p.bot] || []).push({ who: 'op', at: now(), text: `Denied knowledge proposal ${p.id} (${p.to}): ${reason}` }); D.audit.unshift({ ts: now(), event: 'team_knowledge_denied', who: 'operator:ops', detail: `${r.id} · ${p.id} · ${p.bot} · reason sent to the bot`, run: '' }); }); S.save(); rerender(); };
  const denyForm = (r, ps) => UI.form({ caps: `${r.id} · ${ps.map((p) => p.id).join(', ')}`, title: ps.length > 1 ? `Deny ${ps.length} proposals` : `Deny ${ps[0].id}`, fields: [{ k: 'reason', type: 'text', req: true, label: 'Reason (goes back to the proposing bot)', ph: 'e.g. Already covered in knowledge/alb.md; link to it instead.' }], submit: 'Deny', onSubmit: (v) => deny(r, ps, v.reason) });
  UI.act('kp-approve', (b) => { const r = repoOf(b), p = r.proposals.find((x) => x.id === b.dataset.id); if (perrs(r, p).length) return; UI.stepup({ title: `Approve ${p.id}`, purpose: 'team knowledge', ok: 'Approve with passkey', what: `{ team: <b>${esc(r.name)}</b>, proposal: <b>${p.id}</b>, kind: <b>${esc(p.kind)}</b>, bot: <b>${esc(p.bot)}</b>, path: <b>${esc(p.to)}</b> }` }).then((ok) => { if (ok) { approve(r, [p]); UI.toast(`Approved ${p.id}`, `one commit to ${esc(p.to)}`, 'ok'); } }); });
  UI.act('kp-edit', (b) => { const r = repoOf(b), p = r.proposals.find((x) => x.id === b.dataset.id); guardThen(() => { const k = pkind(r, p), x = FM.parse(p.content || ''), existing = entriesOf(r, k).find((e) => e.path === p.to); openEntry(r, k, existing || null, { fm: x.fm, body: x.body, free: '' }); ev.proposal = p.id; ev.mode = 'split'; rerender(); later(); const ed = UI.$('.repo-ed'); if (ed) ed.scrollIntoView({ block: 'start' }); const errs = validate(k, x.fm, x.fm.kind); if (errs.length) setTimeout(() => showErrs(k, errs), 30); }); });
  UI.act('kp-deny', (b) => { const r = repoOf(b); denyForm(r, [r.proposals.find((x) => x.id === b.dataset.id)]); });
  UI.act('kp-bulk', (b) => { const r = repoOf(b), ps = r.proposals.filter((p) => ev.sel.has(p.id) && p.status === 'pending'); if (b.dataset.d === 'deny') return denyForm(r, ps); const bad = ps.filter((p) => perrs(r, p).length); if (bad.length) { UI.toast(`${bad.map((p) => p.id).join(', ')} fail${bad.length > 1 ? '' : 's'} the schema`, 'approve with changes or deny; untick to approve the rest', 'fail'); return; } UI.stepup({ title: `Approve ${ps.length} proposals`, purpose: 'team knowledge', ok: 'Approve with passkey', what: `{ team: <b>${esc(r.name)}</b>, proposals: <b>${ps.map((p) => p.id).join(', ')}</b> }` }).then((ok) => { if (ok) { approve(r, ps); UI.toast(`Approved ${ps.length}`, 'one commit each, the bot as author', 'ok'); } }); });

  /* ---------- Config → Collections: custom kinds, declared per team or bot ---------- */
  const FIELD_LINE = /^([a-z][a-z0-9_]*)(!)?:(string|text|list|steps|number|bool|enum\(([\w-]+(\|[\w-]+)*)\))$/;
  const fieldLine = ([f, d]) => `${f}${d.required ? '!' : ''}:${d.type === 'enum' ? `enum(${(d.values || []).join('|')})` : d.type || 'string'}`;
  const fieldsFromLines = (ls) => Object.fromEntries((ls || []).map((l) => { const m = l.match(FIELD_LINE); const d = { type: m[3].startsWith('enum') ? 'enum' : m[3] }; if (m[2]) d.required = true; if (d.type === 'enum') d.values = m[4].split('|'); return [m[1], d]; }));
  const ownerColl = (o) => (o.startsWith('team:') ? 'teams' : 'bots');
  const allColls = () => { const v = S.view(); return [...v.teams.map((t) => ['team:' + t.name, t]), ...v.bots.map((b) => ['bot:' + b.name, b])].flatMap(([o, x]) => Object.entries((x.cfg && x.cfg.collections) || {}).map(([name, c]) => ({ owner: o, name, c }))); };
  UI.route('config/collections', () => {
    const cs = allColls();
    const row = ({ owner, name, c }) => { const kind = owner.split(':')[0], who = owner.slice(kind.length + 1), d = c.from ? Object.assign({}, (D.packCollectionFiles || {})[c.from] || {}) : c, pack = c.from && (c.from.match(/packs\/([^/]+)/) || [])[1], staged = S.find(ownerColl(owner), who);
      return `<div class="item coll-row" style="align-items:flex-start" data-coll="${esc(owner + '/' + name)}"><span class="sc-ic sm">${icon('folder')}</span><div class="grow small"><div class="row wrap" style="gap:6px"><b>${esc(name)}</b><span class="mono tiny muted">${esc(String(d.dir || name).replace(/\/$/, ''))}/</span>${tag(kind === 'team' ? 'team knowledge · ' + who : 'bot memory · ' + who)}${d.free_paths ? tag('free paths', 'wait') : ''}${pack ? `${UI.label(`from pack ${esc(pack)}`, { cls: 'origin custom' })}` : staged ? tag('staged', 'run') : ''}</div>
      <div class="mono tiny muted">fields: ${Object.entries(d.fields || {}).map(fieldLine).map(esc).join(', ') || 'title only'} · writers: ${esc((d.writers || ['operator']).join(', '))}</div>${pack ? `<div class="mono tiny muted">from: ${esc(c.from)}</div>` : ''}</div>
      ${pack ? `<span class="muted tiny coll-note">defined by the pack's file; ${kind}s.${esc(who)}.collections.${esc(name)} just points at it</span>` : `<div class="row" style="gap:4px"><button class="btn sm" data-act="coll-edit" data-o="${esc(owner)}" data-c="${esc(name)}">${icon('edit')}Edit</button><button class="btn sm secondary" data-act="coll-del" data-o="${esc(owner)}" data-c="${esc(name)}" aria-label="Remove collection ${esc(name)}">${icon('trash')}</button></div>`}</div>`; };
    return `<div class="page">${UI.head('Collections', { crumbs: '<a href="#/config">Config</a>', sub: 'custom kinds of entry for a team\'s knowledge or a bot\'s memory', actions: `<button class="btn primary" data-act="coll-new">${icon('plus')}New collection</button>` })}
      <div class="callout small" style="margin-bottom:14px">Built-in kinds keep their homes: knowledge in <span class="mono">knowledge/</span>, runbooks in <span class="mono">runbooks/</span>, watch items in <span class="mono">watch/</span>, the log in <span class="mono">log/</span>, handoffs in <span class="mono">handoff/</span>. A collection adds a kind with its own directory, fields and template, declared on the team (<span class="mono">teams.&lt;team&gt;.collections</span>) or the bot (<span class="mono">bots.&lt;bot&gt;.collections</span>) whose repo it lives in. It's config, so it goes through the diff.</div>
      <div class="panel"><div class="list">${cs.map(row).join('') || '<div class="empty small">No collections yet. <button class="link" data-act="coll-new">Declare one</button></div>'}</div></div></div>`;
  }, { nav: 'config' });
  const collForm = (owner, name) => { const v0 = S.view(); const cur = owner ? (S.staged(ownerColl(owner), owner.split(':').slice(1).join(':')).collections || {})[name] : null;
    const owners = [...v0.teams.map((t) => ['team:' + t.name, `team ${t.name} (team knowledge)`]), ...v0.bots.map((b) => ['bot:' + b.name, `bot ${b.name} (its memory)`])];
    UI.form({ caps: owner ? `${ownerColl(owner)}.${owner.split(':')[1]}.collections.${name}` : 'collections:', title: owner ? `Edit ${name}` : 'New collection', edit: !!owner, wide: true,
      values: cur ? { owner, name, dir: cur.dir || '', fields: Object.entries(cur.fields || {}).map(fieldLine), template: cur.template || '', free_paths: !!cur.free_paths, writers: cur.writers || ['operator'] } : { owner: owners[0] && owners[0][0], writers: ['operator'], free_paths: false },
      fields: [{ k: 'owner', type: 'enum', req: true, readonly: !!owner, label: 'Belongs to', options: owners, help: 'A team\'s collection lives in its knowledge repo; a bot\'s, in its memory.' },
        { k: 'name', type: 'string', req: true, readonly: !!owner, pattern: /^[a-z][a-z0-9_-]{0,39}$/, patternMsg: 'lowercase letters, digits, _ and -, up to 40', validate: (v, all) => (!owner && allColls().some((c) => c.owner === all.owner && c.name === v) ? 'That team or bot already has a collection by that name.' : '') },
        { k: 'dir', type: 'string', label: 'Directory', ph: 'defaults to the name', validate: (v) => { const d = String(v || '').replace(/\/$/, ''); return d && !/^[a-z][a-z0-9_-]*(\/[a-z0-9_-]+)*$/.test(d) ? 'A relative folder such as notes/vendors' : d && S.RESERVED_DIRS.includes(d.split('/')[0]) ? `${d.split('/')[0]}/ belongs to a built-in kind` : ''; } },
        { k: 'fields', type: 'list', label: 'Fields (besides title)', ph: 'severity!:enum(sev1|sev2|sev3)', help: 'name:type, one per line; type is string, text, list, steps, number, bool or enum(a|b|c). Add ! after the name for a required field.', validate: (v) => { const bad = (v || []).find((f) => !FIELD_LINE.test(f)); return bad ? `"${bad}" is not name:type (or name!:type)` : ''; } },
        { k: 'template', type: 'text', label: 'Body template (markdown)', ph: '## Timeline\n\n## Root cause\n' },
        { k: 'free_paths', type: 'bool', label: 'Free paths', on: 'let writers choose a path inside the directory (otherwise the path follows the title)' },
        { k: 'writers', type: 'select-multi', req: true, label: 'Who writes', options: [['operator', 'operator (you, from the dashboard)'], ['proposal', 'proposal (team bots propose, you approve)'], ['bots', 'bots (the bot writes its own memory)']], help: 'A team takes operator and proposal; a bot takes operator and bots.', validate: (v, all) => { const W = String(all.owner || owner || '').startsWith('team:') ? ['operator', 'proposal'] : ['operator', 'bots']; const bad = (v || []).find((w) => !W.includes(w)); return bad ? `${bad} isn't a writer for a ${String(all.owner || owner).split(':')[0]}; use ${W.join(' or ')}` : ''; } }],
      submit: 'Review change', submitIcon: 'check',
      onSubmit: (v) => { const o = owner || v.owner, coll = ownerColl(o), who = o.split(':').slice(1).join(':'), n = {}; const dir = String(v.dir || '').replace(/\/$/, ''); if (dir && dir !== (v.name || name)) n.dir = dir; const f = fieldsFromLines(v.fields); if (Object.keys(f).length) n.fields = f; if (v.template) n.template = v.template; if (v.free_paths) n.free_paths = true; if (JSON.stringify(v.writers) !== '["operator"]') n.writers = v.writers;
        const cfg = S.staged(coll, who); cfg.collections = Object.assign({}, cfg.collections, { [v.name || name]: n }); D.prefs.returnTo = { href: '#/config/collections', label: 'Collections' }; UI.staged(S.stage(coll, who, cfg)); } }); };
  UI.act('coll-new', () => collForm(null));
  UI.act('coll-edit', (b) => collForm(b.dataset.o, b.dataset.c));
  UI.act('coll-del', (b) => UI.confirm({ title: `Remove collection ${esc(b.dataset.c)}?`, body: 'Stages removing it from the config. Its files stay in the repo (visible in the files view) until you map or delete them.', ok: 'Stage removal', cancel: 'Keep it', danger: true }).then((ok) => { if (!ok) return; const o = b.dataset.o, coll = ownerColl(o), who = o.split(':').slice(1).join(':'), cfg = S.staged(coll, who); const cs = Object.assign({}, cfg.collections); delete cs[b.dataset.c]; if (Object.keys(cs).length) cfg.collections = cs; else delete cfg.collections; UI.staged(S.stage(coll, who, cfg)); }));
})();
