/* Knowledge you can author: one git-backed repo view, used for a team's
 * knowledge repo and for a bot's own memory.
 *
 * The operator writes directly: every save is one commit authored by the
 * operator (Authored-By: operator:ops), no approval needed, since operator
 * provenance is trusted. Bots keep committing too; a save over a newer bot
 * commit is a conflict the operator resolves explicitly (rebase or
 * overwrite). History is per file and per repo, with diffs, revert-as-a-new-
 * commit, and blame-lite. Team repos also carry bot proposals: approve,
 * approve with changes (in the editor), deny with a reason, in bulk. */
(function () {
  const { icon, esc, ago, tag } = UI;
  const now = () => new Date().toISOString();
  const minsAgo = (m) => new Date(Date.now() - m * 60000).toISOString();
  const sha = () => Math.random().toString(16).slice(2, 9);
  const R = (window.R = {});

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

  /* ---------- the repos ---------- */
  const botsOfTeam = (t) => D.bots.filter((b) => b.cfg && b.cfg.team === t).map((b) => b.name);
  R.get = (id) => {
    D.repos = D.repos || {};
    if (D.repos[id]) return D.repos[id];
    const [kind, name] = id.split(':');
    let files = {}, proposals = [], log = [];
    const imp = (who, whoKind, at, fs) => ({ sha: sha(), msg: 'import', who, kind: whoKind, at, changes: Object.entries(fs).map(([p, c]) => ({ path: p, before: null, after: c })) });
    if (kind === 'bot' && name === 'platform-sre' && D.memory) {
      files = Object.assign({}, D.memory.files);
      const db = files['knowledge/db-prod-1.md'], lat = files['runbooks/latency.md'];
      const db0 = db.split('\n').filter((l) => !/Failover drill|storm capped|No data loss/.test(l)).join('\n');
      const lat0 = lat.split('\n').filter((l) => !/deregistration/.test(l)).join('\n');
      log = [
        { sha: '8e1f0aa', msg: 'knowledge: db-prod-1 failover drill outcome', who: 'bot:platform-sre', kind: 'bot', case: 'c31', at: minsAgo(1), changes: [{ path: 'knowledge/db-prod-1.md', before: db0, after: db }] },
        { sha: 'b07e5c1', msg: 'runbooks/latency: add ALB deregistration note', who: 'operator:ops', kind: 'operator', at: minsAgo(1440), changes: [{ path: 'runbooks/latency.md', before: lat0, after: lat }] },
        imp('bot:platform-sre', 'bot', minsAgo(4320), Object.assign({}, files, { 'knowledge/db-prod-1.md': db0, 'runbooks/latency.md': lat0 })),
      ];
      log[2].msg = 'import: memory from session s1';
    } else if (kind === 'bot') {
      const old = (D.memories || {})[name];
      files = old ? Object.assign({}, old.files) : { 'knowledge/README.md': `# ${name}\n\nWhat ${name} has learned.\n` };
      log = [imp('bot:' + name, 'bot', minsAgo(2880), files)];
    } else {
      const tk = (D.teamKnowledge || {})[name];
      files = tk ? Object.assign({}, tk.files) : { 'README.md': `# ${name} team knowledge\n` };
      proposals = tk ? JSON.parse(JSON.stringify(tk.proposals || [])) : [];
      const lead = (D.teams.find((t) => t.name === name) || { cfg: {} }).cfg.lead || botsOfTeam(name)[0] || 'ops';
      log = [imp('bot:' + lead, 'bot', minsAgo(5000), files)];
    }
    const r = { id, kind, name, files, log, proposals, open: (id === 'bot:platform-sre' && D.memory && D.memory.open) || Object.keys(files).filter((p) => !p.startsWith('.') && !p.endsWith('.keep'))[0] };
    D.repos[id] = r; return r;
  };
  const visible = (p) => !p.split('/').pop().startsWith('.');
  R.version = (r, path) => { const c = r.log.find((x) => x.changes.some((ch) => ch.path === path || ch.from === path)); return c ? c.sha : null; };
  R.commit = (r, o) => {
    const c = Object.assign({ sha: sha(), at: now(), who: 'operator:ops', kind: 'operator' }, o);
    c.changes.forEach((ch) => { if (ch.from && ch.from !== ch.path) delete r.files[ch.from]; if (ch.after == null) delete r.files[ch.path]; else r.files[ch.path] = ch.after; });
    r.log.unshift(c);
    D.audit.unshift({ ts: c.at, event: r.kind === 'team' ? 'team_knowledge_commit' : 'memory_commit', who: c.who, detail: `${r.id} · ${c.sha} · ${c.msg}`, run: '' });
    S.save(); return c;
  };
  /** Blame-lite: replay the commits that touched a file and give each line to the commit that wrote it. */
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
  const whoLabel = (c) => (c.kind === 'operator' ? 'ops (operator)' : c.who.replace('bot:', ''));
  const whoChip = (c) => `<span class="who ${c.kind}">${icon(c.kind === 'operator' ? 'passkey' : 'members')}${esc(whoLabel(c))}</span>${c.case ? ` <a class="link mono tiny" href="#/bots/${c.who.replace('bot:', '')}/cases/${c.case}">${c.case}</a>` : ''}`;
  const proposedAfter = (r, p) => { const added = p.diff.split('\n').filter((l) => l[0] === '+').map((l) => l.slice(1)).join('\n'); const isNew = p.diff.startsWith('+#') || !r.files[p.to]; return (isNew ? '' : r.files[p.to].replace(/\n?$/, '\n')) + added + '\n'; };
  const readers = (r, path) => (r.kind === 'team' ? botsOfTeam(r.name) : [r.name]);

  /* ---------- editor state ---------- */
  const ev = { repo: null, path: null, text: '', base: '', baseSha: null, mode: 'split', dirty: false, conflict: null, blame: false, proposal: null, hist: 'file', q: '', sel: new Set() };
  UI.guard = () => (ev.dirty ? `Your edits to <span class="mono">${esc(ev.path)}</span> aren't saved. Leave and lose them?` : null);
  UI.guardClear = () => { ev.dirty = false; };
  function openFile(r, path, text) { r.open = path; ev.repo = r.id; ev.path = path; ev.base = r.files[path] != null ? r.files[path] : ''; ev.text = text != null ? text : ev.base; ev.baseSha = R.version(r, path); ev.dirty = text != null && text !== ev.base; ev.conflict = null; ev.proposal = null; S.save(); }
  const guardThen = (fn) => { if (!ev.dirty) return fn(); UI.confirm({ title: 'Discard unsaved changes?', body: UI.guard(), ok: 'Discard', danger: true }).then((ok) => { if (ok) { ev.dirty = false; fn(); } }); };

  /* ---------- the view ---------- */
  UI.repoView = (id, opts = {}) => {
    const r = R.get(id);
    if (ev.repo !== id || (ev.path && r.files[ev.path] == null && !ev.dirty)) { const p = r.open && r.files[r.open] != null ? r.open : Object.keys(r.files).filter(visible)[0]; if (p) openFile(r, p); else { ev.repo = id; ev.path = null; } }
    const paths = Object.keys(r.files).filter(visible).sort();
    const dirs = [...new Set(Object.keys(r.files).map((p) => p.split('/').slice(0, -1).join('/')).filter(Boolean))].sort();
    const pins = (r.files['.pins'] || '').split('\n').filter(Boolean);
    const fileRow = (p) => `<a href="#" class="f${p === ev.path ? ' on' : ''}" data-act="rp-open" data-repo="${id}" data-path="${esc(p)}" style="padding-left:${10 + (p.split('/').length - 1) * 12}px">${icon('file')}<span class="ellipsis">${esc(p.split('/').pop())}</span>${pins.includes(p) ? '<span class="pin" title="pinned">📌</span>' : ''}${p === ev.path && ev.dirty ? '<span class="dirty-dot" title="unsaved"></span>' : ''}</a>`;
    const tree = `${pins.length && r.kind === 'bot' ? `<div class="tree-dir">${icon('zap')}pinned · always in context</div>${pins.filter((p) => r.files[p] != null).map(fileRow).join('')}` : ''}
      ${paths.filter((p) => !p.includes('/')).map(fileRow).join('')}${dirs.map((d) => `<div class="tree-dir" style="padding-left:${10 + (d.split('/').length - 1) * 12}px">${icon('folder')}${esc(d.split('/').pop())}/</div>${paths.filter((p) => p.split('/').slice(0, -1).join('/') === d).map(fileRow).join('')}`).join('')}`;
    const results = ev.q ? search(r, ev.q) : null;
    const hist = r.log.filter((c) => ev.hist === 'repo' || c.changes.some((ch) => ch.path === ev.path || ch.from === ev.path));
    const pend = r.proposals.filter((p) => p.status === 'pending');
    return `<div class="repo" data-repo="${id}">
      ${r.kind === 'team' ? proposalsHTML(r, pend) : ''}
      <div class="grid repo-grid">
        <div class="panel repo-side"><div class="panel-head"><h3 class="mono small">${esc(opts.title || id)}</h3><div class="tools"><button class="icon-btn sm" data-act="rp-new" data-repo="${id}" data-tip="New file" aria-label="New file">${icon('plus')}</button><button class="icon-btn sm" data-act="rp-mkdir" data-repo="${id}" data-tip="New folder" aria-label="New folder">${icon('folder')}</button><button class="icon-btn sm" data-act="rp-upload" data-repo="${id}" data-tip="Upload or import" aria-label="Upload or import">${icon('upload')}</button></div></div>
          <div class="panel-body tight"><div class="input-icon" style="margin:8px">${icon('search')}<input class="input" placeholder="${r.kind === 'team' ? 'search team knowledge + bot memory' : 'search memory + team knowledge'}" value="${esc(ev.q)}" data-on-change="rp-q" data-repo="${id}" aria-label="Search knowledge"></div></div>
          ${results ? `<div class="list search-res">${results.map((x) => `<a href="#" class="item click small" data-act="rp-hit" data-repo="${x.repo}" data-path="${esc(x.path)}"><div class="grow"><div class="mono tiny muted">${esc(x.repo)}</div><b class="mono">${esc(x.path)}</b><div class="muted tiny ellipsis">${esc(x.snip)}</div></div></a>`).join('') || '<div class="item small muted">No match.</div>'}<button class="btn sm ghost" data-act="rp-qclear" style="margin:6px">${icon('x')}Clear search</button></div>` : `<div class="tree" style="padding:6px">${tree || '<div class="muted small" style="padding:8px">Empty. Create a file or upload one.</div>'}</div>`}</div>
        ${ev.path ? editorHTML(r) : `<div class="panel"><div class="empty"><span class="serif">No file open</span><button class="btn primary" data-act="rp-new" data-repo="${id}">${icon('plus')}New file</button></div></div>`}
        <div class="panel repo-hist"><div class="panel-head"><h3>History</h3><div class="tools">${UI.seg([['file', 'this file'], ['repo', 'repo']], ev.hist, 'rp-hist')}</div></div>
          <div class="list">${hist.slice(0, 30).map((c) => `<div class="item" style="align-items:flex-start"><span class="mono tiny" style="color:var(--accent)">${c.sha}</span><div class="grow small"><div>${esc(c.msg)}</div><div class="tiny">${whoChip(c)} <span class="muted mono">· ${ago(c.at)} ago</span></div>${ev.hist === 'repo' ? `<div class="mono tiny muted">${c.changes.map((ch) => esc(ch.from && ch.from !== ch.path ? `${ch.from} → ${ch.path}` : ch.path)).join(', ')}</div>` : ''}</div>
            <button class="icon-btn sm" data-act="rp-diff" data-repo="${id}" data-sha="${c.sha}" aria-label="Diff ${c.sha}" data-tip="Diff">${icon('eye')}</button><button class="icon-btn sm" data-act="rp-revert" data-repo="${id}" data-sha="${c.sha}" aria-label="Revert ${c.sha}" data-tip="Revert as a new commit">${icon('undo')}</button></div>`).join('') || '<div class="item small muted">No commits yet.</div>'}</div></div>
      </div></div>`;
  };
  function editorHTML(r) {
    const id = r.id, ver = R.version(r, ev.path), behind = ev.baseSha && ver && ver !== ev.baseSha;
    const t = D.bots.find((b) => b.name === r.name), team = r.kind === 'bot' && t && t.cfg && t.cfg.team, tk = team && D.teams.find((x) => x.name === team);
    const pins = (r.files['.pins'] || '').split('\n').filter(Boolean);
    const blame = ev.blame ? R.blame(r, ev.path) : null;
    let blameHTML = '';
    if (blame) { const groups = []; blame.forEach((b) => { const last = groups[groups.length - 1]; if (last && last.c === b.c) last.lines.push(b.line); else groups.push({ c: b.c, lines: [b.line] }); }); blameHTML = `<div class="blame">${groups.map((g) => `<div class="bl-group ${g.c.kind}"><div class="bl-who">${whoChip(g.c)}<span class="mono tiny muted">${g.c.sha} · ${ago(g.c.at)}</span></div><pre>${esc(g.lines.join('\n'))}</pre></div>`).join('')}</div>`; }
    return `<div class="panel repo-ed">
      <div class="panel-head ed-head"><span class="mono small grow ellipsis">${esc(ev.path)}${ev.dirty ? ' <span class="tag" data-s="wait">unsaved</span>' : ''}${ev.proposal ? ` ${tag('approving ' + ev.proposal + ' with changes', 'ember')}` : ''}</span>
        <div class="tools">${UI.seg([['edit', 'Edit'], ['split', 'Split'], ['preview', 'Preview']], ev.mode, 'rp-mode')}<button class="btn sm ghost${ev.blame ? ' on' : ''}" data-act="rp-blame" aria-pressed="${ev.blame}">${icon('members')}Who wrote this</button><button class="btn sm ghost" data-act="rp-file-menu" data-repo="${id}" aria-label="File actions">${icon('more')}</button></div></div>
      <div class="readers small muted">${icon('eye')} read by ${readers(r, ev.path).map((b) => `<a class="link mono" href="#/bots/${b}">${esc(b)}</a>`).join(', ') || 'no bot yet'}${r.kind === 'team' ? ` · team ${esc(r.name)}` : ' · its own memory'}${pins.includes(ev.path) ? ' · <b>pinned</b>: always in its context' : ''}</div>
      ${ev.conflict ? conflictHTML(r) : behind && !ev.dirty ? `<div class="callout info small">A newer commit (${ver}) changed this file. <button class="link" data-act="rp-reload" data-repo="${id}">Load it</button></div>` : ''}
      ${blame ? blameHTML : `<div class="ed-panes mode-${ev.mode}">
        <div class="ed-src">${UI.editor({ id: 'rp-ed', text: ev.text, lang: 'md', act: 'rp-input', minRows: 18, label: 'Markdown' })}</div>
        <div class="ed-prev prose" id="rp-prev">${UI.mdRender(ev.text)}</div></div>`}
      <div class="panel-foot ed-foot"><input class="input grow" id="rp-msg" placeholder="Commit message (optional)" aria-label="Commit message">
        <button class="btn sm ghost" data-act="rp-sim" data-repo="${id}" data-tip="Prototype: a bot commits to this file now, to try conflict handling">${icon('zap')}Simulate a bot commit</button>
        ${team && tk && tk.cfg.knowledge && !ev.dirty ? `<button class="btn sm" data-act="rp-promote" data-repo="${id}">${icon('upload')}Promote to team ${esc(team)}</button>` : ''}
        <button class="btn sm ghost" data-act="rp-discard" ${ev.dirty ? '' : UI.off('No unsaved changes')}>Discard</button>
        <button class="btn primary" data-act="rp-save" data-repo="${id}" ${ev.dirty || ev.proposal ? '' : UI.off('Nothing to save yet: edit the file first')}>${icon('commit')}${ev.proposal ? 'Approve with changes' : 'Save · one commit'}</button></div>
      <div class="muted tiny ed-sign">Saved as you: <span class="mono">Authored-By: operator:ops · Edited-Via: dashboard</span>. No approval: operator provenance is trusted.</div></div>`;
  }
  function conflictHTML(r) {
    const c = ev.conflict;
    return `<div class="conflict"><div class="callout warn small"><b>Conflict.</b> ${esc(whoLabel(c.commit))} committed <span class="mono">${c.commit.sha}</span> to this file after you opened it. Choose explicitly:</div>
      <div class="grid g2"><div><div class="caps">their change (vs. what you opened)</div>${UI.diffView(ev.base, c.theirs, 'unified', { file: ev.path }).html}</div><div><div class="caps">your change (vs. what you opened)</div>${UI.diffView(ev.base, ev.text, 'unified', { file: ev.path }).html}</div></div>
      <div class="row" style="margin-top:8px"><button class="btn primary" data-act="rp-rebase" data-repo="${r.id}">${icon('branch')}Rebase my edit on theirs</button><button class="btn danger" data-act="rp-overwrite" data-repo="${r.id}">Overwrite theirs with mine</button><button class="btn ghost" data-act="rp-take-theirs" data-repo="${r.id}">Discard mine, keep theirs</button></div></div>`;
  }
  function proposalsHTML(r, pend) {
    const all = r.proposals;
    return `<div class="panel" style="margin-bottom:14px"><div class="panel-head"><h3>Proposals from bots</h3><span class="muted small">bots propose; you approve, edit, or deny with a reason</span>
      <div class="tools"><button class="btn sm" ${ev.sel.size ? `data-act="kp-bulk" data-repo="${r.id}" data-d="approve"` : UI.off('Tick proposals first')}>${icon('passkey')}Approve selected${ev.sel.size ? ` (${ev.sel.size})` : ''}</button><button class="btn sm danger" ${ev.sel.size ? `data-act="kp-bulk" data-repo="${r.id}" data-d="deny"` : UI.off('Tick proposals first')}>Deny selected</button></div></div>
      <div class="list">${all.map((p) => `<div class="item kprop" style="align-items:flex-start">${p.status === 'pending' ? `<input type="checkbox" class="cbx" data-on-change="kp-sel" data-id="${p.id}" ${ev.sel.has(p.id) ? 'checked' : ''} aria-label="Select ${p.id}">` : UI.st(p.status.startsWith('approved') ? 'ok' : 'fail', '')}
        <div class="grow small"><div><b>${esc(p.summary)}</b></div><div class="mono tiny muted">${p.id} · ${esc(p.bot)}: ${esc(p.from)} → ${esc(p.to)}${p.case ? ' · case ' + p.case : ''} · ${ago(p.at)} ago${p.approver ? ' · ' + esc(p.approver) : ''}</div>${p.reason ? `<div class="tiny" style="color:var(--fail)">denied: ${esc(p.reason)}</div>` : ''}
        ${p.status === 'pending' && p.diff ? `<div class="diff" style="margin-top:6px">${p.diff.split('\n').filter(Boolean).map((l) => `<div class="dl ${l[0] === '+' ? 'add' : l[0] === '-' ? 'del' : ''}"><span></span><span></span><span>${l[0] === ' ' ? ' ' : l[0]}</span><span>${esc(l.slice(1))}</span></div>`).join('')}</div>` : ''}</div>
        ${p.status === 'pending' ? `<div class="row wrap" style="gap:4px"><button class="btn sm primary" data-act="kp-approve" data-repo="${r.id}" data-id="${p.id}">${icon('passkey')}Approve</button><button class="btn sm" data-act="kp-edit" data-repo="${r.id}" data-id="${p.id}">${icon('edit')}Approve with changes</button><button class="btn sm danger" data-act="kp-deny" data-repo="${r.id}" data-id="${p.id}">Deny</button></div>` : tag(p.status, p.status.startsWith('approved') ? 'ok' : 'fail')}</div>`).join('') || '<div class="item small muted">No proposals.</div>'}</div></div>`;
  }
  function search(r, q) {
    const ql = q.toLowerCase(), repos = [r];
    if (r.kind === 'team') botsOfTeam(r.name).forEach((b) => repos.push(R.get('bot:' + b)));
    else { const b = D.bots.find((x) => x.name === r.name); if (b && b.cfg && b.cfg.team) repos.push(R.get('team:' + b.cfg.team)); }
    const out = [];
    repos.forEach((x) => Object.entries(x.files).filter(([p]) => visible(p)).forEach(([p, c]) => { const i = (p + '\n' + c).toLowerCase().indexOf(ql); if (i >= 0) { const line = c.split('\n').find((l) => l.toLowerCase().includes(ql)) || p; out.push({ repo: x.id, path: p, snip: line.trim() }); } }));
    return out;
  }

  /* ---------- actions ---------- */
  const repoOf = (b) => R.get(b.dataset.repo || ev.repo);
  const rerender = () => UI.rerender();
  UI.act('rp-open', (a) => { const r = repoOf(a); guardThen(() => { openFile(r, a.dataset.path); rerender(); }); });
  UI.act('rp-mode', (b) => { ev.mode = b.dataset.v; ev.blame = false; rerender(); });
  UI.act('rp-blame', () => { ev.blame = !ev.blame; rerender(); });
  UI.act('rp-hist', (b) => { ev.hist = b.dataset.v; rerender(); });
  UI.act('rp-q', (el) => { ev.q = el.value.trim(); rerender(); });
  UI.act('rp-qclear', () => { ev.q = ''; rerender(); });
  UI.act('rp-hit', (a) => guardThen(() => { const r = R.get(a.dataset.repo); r.open = a.dataset.path; ev.q = ''; S.save(); const [k, n] = a.dataset.repo.split(':'); const href = k === 'team' ? `#/teams/${n}/knowledge` : `#/bots/${n}/memory`; ev.repo = null; if (location.hash.startsWith(href)) rerender(); else UI.go(href); }));
  let pv;
  UI.act('rp-input', (ta) => { ev.text = ta.value; const was = ev.dirty; ev.dirty = ev.text !== ev.base || !!ev.proposal; clearTimeout(pv); pv = setTimeout(() => { const p = UI.$('#rp-prev'); if (p) p.innerHTML = UI.mdRender(ev.text); }, 120); if (was !== ev.dirty) { const b = UI.$('[data-act="rp-save"]'); if (b && ev.dirty) { b.removeAttribute('aria-disabled'); delete b.dataset.reason; delete b.dataset.tip; } const d = UI.$('[data-act="rp-discard"]'); if (d && ev.dirty) { d.removeAttribute('aria-disabled'); delete d.dataset.reason; delete d.dataset.tip; } const h = UI.$('.ed-head .grow'); if (h && ev.dirty && !h.querySelector('.tag')) h.insertAdjacentHTML('beforeend', ' <span class="tag" data-s="wait">unsaved</span>'); } });
  UI.act('rp-discard', () => UI.confirm({ title: 'Discard your edits?', body: `Puts <span class="mono">${esc(ev.path)}</span> back to the last commit.`, ok: 'Discard', danger: true }).then((ok) => { if (!ok) return; const r = R.get(ev.repo); openFile(r, ev.path); rerender(); }));
  UI.act('rp-reload', (b) => { openFile(repoOf(b), ev.path); rerender(); });
  function save(r, opts = {}) {
    const ver = R.version(r, ev.path);
    if (!opts.force && ev.baseSha && ver && ver !== ev.baseSha) { const c = r.log.find((x) => x.sha === ver); ev.conflict = { commit: c, theirs: r.files[ev.path] }; rerender(); return; }
    const msg = (UI.$('#rp-msg') || {}).value || `${r.files[ev.path] == null ? 'add' : 'edit'} ${ev.path}`;
    const c = R.commit(r, { msg: msg + (opts.force ? ` (overwrote ${ver})` : ''), changes: [{ path: ev.path, before: r.files[ev.path] != null ? r.files[ev.path] : null, after: ev.text }], trailers: 'Authored-By: operator:ops · Edited-Via: dashboard' });
    openFile(r, ev.path); rerender(); UI.toast(`Committed ${c.sha}`, `one commit by ops · ${esc(ev.path)}`, 'ok');
  }
  UI.act('rp-save', (b) => {
    const r = repoOf(b);
    if (ev.proposal) {
      const p = r.proposals.find((x) => x.id === ev.proposal);
      UI.stepup({ title: `Approve ${p.id} with your changes`, purpose: 'team knowledge', ok: 'Approve with passkey', what: `{ team: <b>${esc(r.name)}</b>, proposal: <b>${p.id}</b>, path: <b>${esc(ev.path)}</b>, edited_by: <b>operator:ops</b> }` }).then((ok) => {
        if (!ok) return;
        R.commit(r, { msg: `promote ${p.to} from ${p.bot} (edited)`, who: 'bot:' + p.bot, kind: 'bot', case: p.case, changes: [{ path: ev.path, before: r.files[ev.path] != null ? r.files[ev.path] : null, after: ev.text }], trailers: `Promoted-By: bot:${p.bot} · Approved-By: operator:ops · Edited-By: operator:ops` });
        p.status = 'approved (edited)'; p.approver = 'ops (passkey · laptop), with changes';
        openFile(r, ev.path); S.save(); rerender(); UI.toast(`Approved ${p.id} with changes`, 'one commit, the bot as author, your edit recorded', 'ok');
      });
      return;
    }
    save(r);
  });
  UI.act('rp-overwrite', (b) => { const r = repoOf(b); ev.conflict = null; save(r, { force: true }); });
  UI.act('rp-take-theirs', (b) => { openFile(repoOf(b), ev.path); rerender(); });
  UI.act('rp-rebase', (b) => {
    // naive line rebase: theirs, plus the lines you added (after the line they followed), minus the lines you removed
    const r = repoOf(b), base = ev.base.split('\n'), theirs = ev.conflict.theirs.split('\n');
    const mine = YAML.diff(ev.base, ev.text); let out = theirs.slice(); let anchor = null;
    mine.forEach((x) => { if (x.t === ' ') anchor = x.s; else if (x.t === '-') { const i = out.indexOf(x.s); if (i >= 0) out.splice(i, 1); } else { const i = anchor == null ? -1 : out.lastIndexOf(anchor); out.splice(i + 1, 0, x.s); anchor = x.s; } });
    const merged = out.join('\n'); void base;
    openFile(r, ev.path, merged); ev.dirty = true; rerender(); UI.toast('Rebased on their commit', 'review the result, then save', 'run');
  });
  UI.act('rp-sim', (b) => {
    const r = repoOf(b), who = r.kind === 'bot' ? r.name : ((D.teams.find((t) => t.name === r.name) || { cfg: {} }).cfg.lead || botsOfTeam(r.name)[0] || 'platform-sre');
    const cur = r.files[ev.path] || '';
    R.commit(r, { msg: `note from ${who}`, who: 'bot:' + who, kind: 'bot', case: 'c29', changes: [{ path: ev.path, before: cur, after: cur.replace(/\n?$/, '\n') + `- ${who}: seen again during c29 (auto-noted)\n` }] });
    rerender(); UI.toast(`${who} committed to ${esc(ev.path)}`, ev.dirty ? 'saving now will show a conflict' : 'load it to see the change', 'run');
  });
  const pathField = (r, label, def) => ({ k: 'path', type: 'string', req: true, label, def, pattern: /^[\w][\w./ -]*\.(md|markdown|txt)$/, patternMsg: 'A relative path ending in .md', validate: (v) => (r.files[v] != null ? 'That file exists.' : '') });
  UI.act('rp-new', (b) => { const r = repoOf(b); guardThen(() => UI.form({ caps: r.id, title: 'New file', fields: [pathField(r, 'Path', r.kind === 'bot' ? 'knowledge/' : ''), { k: 'template', type: 'enum', label: 'Start from', options: [['blank', 'a blank page'], ['runbook', 'a runbook'], ['fact', 'a fact']], def: 'blank' }], submit: 'Create and edit', submitIcon: 'edit',
    onSubmit: (v) => { const title = v.path.split('/').pop().replace(/\.\w+$/, ''); const body = { blank: `# ${title}\n\n`, runbook: `# ${title}\n\n## When\n\n## Steps\n1. \n\n## Verify\n`, fact: `# ${title}\n\n- **Fact:** \n- **Source:** \n` }[v.template || 'blank']; ev.repo = r.id; ev.path = v.path; ev.base = ''; ev.text = body; ev.baseSha = null; ev.dirty = true; ev.conflict = null; ev.proposal = null; ev.mode = 'split'; r.open = v.path; S.save(); UI.rerender(); } })); });
  UI.act('rp-mkdir', (b) => { const r = repoOf(b); UI.form({ caps: r.id, title: 'New folder', fields: [{ k: 'dir', type: 'string', req: true, label: 'Folder', ph: 'runbooks/db', pattern: /^[\w][\w/ .-]*$/, patternMsg: 'letters, digits, / . _ -' }], submit: 'Create', onSubmit: (v) => { const p = v.dir.replace(/\/$/, '') + '/.keep'; R.commit(r, { msg: `mkdir ${v.dir}`, changes: [{ path: p, before: null, after: '' }], trailers: 'Authored-By: operator:ops' }); UI.rerender(); } }); });
  UI.act('rp-file-menu', (b) => { const r = repoOf(b), pins = (r.files['.pins'] || '').split('\n').filter(Boolean), isNew = r.files[ev.path] == null; UI.menu(b, [['Rename / move…', 'edit', 'rp-rename', isNew ? UI.off('Save it first') : `data-repo="${r.id}"`], [pins.includes(ev.path) ? 'Unpin' : 'Pin (always in context)', 'zap', 'rp-pin', r.kind !== 'bot' ? UI.off('Pinning is for a bot\'s own memory') : isNew ? UI.off('Save it first') : `data-repo="${r.id}"`], ['-'], ['Delete…', 'trash', 'rp-delete', isNew ? UI.off('Not saved yet: Discard instead') : `data-repo="${r.id}"`]]); });
  UI.act('rp-rename', (b) => { const r = repoOf(b), from = ev.path; guardThen(() => UI.form({ caps: r.id, title: `Rename ${esc(from)}`, edit: true, values: { path: from }, fields: [pathField(r, 'New path')], submit: 'Rename', onSubmit: (v) => { const c = r.files[from]; R.commit(r, { msg: `rename ${from} → ${v.path}`, changes: [{ path: v.path, from, before: c, after: c }], trailers: 'Authored-By: operator:ops' }); const pins = (r.files['.pins'] || '').split('\n').filter(Boolean); if (pins.includes(from)) r.files['.pins'] = pins.map((p) => (p === from ? v.path : p)).join('\n'); openFile(r, v.path); UI.rerender(); } })); });
  UI.act('rp-delete', (b) => { const r = repoOf(b), p = ev.path; UI.confirm({ title: `Delete ${esc(p)}?`, body: 'One commit removes it. Revert that commit to bring it back.', ok: 'Delete', danger: true }).then((ok) => { if (!ok) return; R.commit(r, { msg: `delete ${p}`, changes: [{ path: p, before: r.files[p], after: null }], trailers: 'Authored-By: operator:ops' }); ev.dirty = false; ev.repo = null; r.open = null; UI.rerender(); UI.toast(`Deleted ${esc(p)}`, 'revert from history to restore it', 'ok'); }); });
  UI.act('rp-pin', (b) => { const r = repoOf(b), pins = (r.files['.pins'] || '').split('\n').filter(Boolean), on = pins.includes(ev.path); const next = on ? pins.filter((x) => x !== ev.path) : pins.concat([ev.path]); R.commit(r, { msg: `${on ? 'unpin' : 'pin'} ${ev.path}`, changes: [{ path: '.pins', before: r.files['.pins'] || null, after: next.join('\n') }], trailers: 'Authored-By: operator:ops' }); openFile(r, ev.path); UI.rerender(); });
  UI.act('rp-diff', (b) => { const r = repoOf(b), c = r.log.find((x) => x.sha === b.dataset.sha); UI.modal(`<div class="modal-head"><div><div class="caps">${c.sha} · ${esc(whoLabel(c))} · ${ago(c.at)} ago</div><h2>${esc(c.msg)}</h2></div><button class="icon-btn" data-close aria-label="Close">${icon('x')}</button></div><div class="modal-body">${c.trailers ? `<div class="mono tiny muted" style="margin-bottom:8px">${esc(c.trailers)}</div>` : ''}${c.changes.filter((ch) => visible(ch.path)).map((ch) => UI.diffView(ch.before || '', ch.after || '', 'unified', { file: ch.from && ch.from !== ch.path ? `${ch.from} → ${ch.path}` : ch.path }).html).join('') || '<div class="muted small">No file content changed.</div>'}</div><div class="modal-foot"><button class="btn" data-close>Close</button></div>`, { wide: true }); });
  UI.act('rp-revert', (b) => { const r = repoOf(b), c = r.log.find((x) => x.sha === b.dataset.sha); UI.confirm({ title: `Revert ${c.sha}?`, body: `A new commit undoes “${esc(c.msg)}”. History is kept.`, ok: 'Revert' }).then((ok) => { if (!ok) return; const changes = c.changes.slice().reverse().map((ch) => (ch.from && ch.from !== ch.path ? { path: ch.from, from: ch.path, before: ch.after, after: ch.before } : { path: ch.path, before: r.files[ch.path] != null ? r.files[ch.path] : null, after: ch.before })); R.commit(r, { msg: `Revert "${c.msg}"`, changes, trailers: `Authored-By: operator:ops · Reverts: ${c.sha}` }); ev.repo = null; UI.rerender(); UI.toast(`Reverted ${c.sha}`, 'as a new commit', 'ok'); }); });
  UI.act('rp-promote', (b) => { const r = repoOf(b), team = D.bots.find((x) => x.name === r.name).cfg.team, tr = R.get('team:' + team); const id = 'k' + (10 + tr.proposals.length); tr.proposals.unshift({ id, bot: r.name, from: ev.path, to: ev.path.split('/').pop(), summary: `Promote ${ev.path.split('/').pop()} from ${r.name}`, status: 'pending', at: now(), diff: (r.files[ev.path] || '').split('\n').map((l) => '+' + l).join('\n') }); S.save(); UI.go(`#/teams/${team}/knowledge`); });
  /* upload / paste / import */
  UI.act('rp-upload', (b) => {
    const r = repoOf(b);
    const s = UI.modal(`<div class="modal-head"><div><div class="caps">${esc(r.id)}</div><h2>Upload or import</h2></div><button class="icon-btn" data-close aria-label="Close">${icon('x')}</button></div>
      <div class="modal-body stack">${UI.seg([['drop', 'Drop files'], ['paste', 'Paste'], ['repo', 'From a repo']], 'drop', 'up-mode')}
        <div class="up-pane" data-pane="drop"><label class="dropzone" id="up-drop">${icon('upload')}<span><b>Drop markdown files here</b> or choose them</span><input type="file" id="up-file" accept=".md,.markdown,.txt,text/markdown,text/plain" multiple aria-label="Choose markdown files"></label><div class="field"><label for="up-dir">Into folder</label><input class="input mono" id="up-dir" value="${r.kind === 'bot' ? 'knowledge' : ''}" placeholder="top level"></div><div id="up-list" class="small muted"></div></div>
        <div class="up-pane hide" data-pane="paste"><div class="field"><label for="up-path">Path</label><input class="input mono" id="up-path" placeholder="runbooks/new-runbook.md"></div><div class="field"><label for="up-text">Paste a runbook</label><textarea class="textarea mono" id="up-text" rows="8"></textarea></div></div>
        <div class="up-pane hide" data-pane="repo"><div class="grid g2"><div class="field"><label for="up-repo">Repo</label><input class="input mono" id="up-repo" value="acme/infra"></div><div class="field"><label for="up-src">Path in repo</label><input class="input mono" id="up-src" value="docs/runbooks/failover.md"></div></div><div class="hint">Fetched through the gh connector (read), committed with an Imported-From trailer.</div></div>
        <div class="callout small">Each import is one commit authored by you.</div></div>
      <div class="modal-foot"><button class="btn ghost" data-close>Cancel</button><button class="btn primary" id="up-go">${icon('commit')}Import</button></div>`, { wide: true });
    let mode = 'drop', picked = [];
    const showPicked = () => { UI.$('#up-list', s).innerHTML = picked.map((f) => `<div class="mono">${esc(f.name)} · ${f.text.length} chars</div>`).join(''); };
    const take = (files) => Promise.all([...files].map((f) => f.text().then((text) => ({ name: f.name, text })))).then((xs) => { picked = picked.concat(xs); showPicked(); });
    UI.$('#up-file', s).addEventListener('change', (e) => take(e.target.files));
    const dz = UI.$('#up-drop', s); dz.addEventListener('dragover', (e) => { e.preventDefault(); dz.classList.add('over'); }); dz.addEventListener('dragleave', () => dz.classList.remove('over')); dz.addEventListener('drop', (e) => { e.preventDefault(); dz.classList.remove('over'); take(e.dataTransfer.files); });
    UI.upMode = (v) => { mode = v; s.querySelectorAll('.up-pane').forEach((p) => p.classList.toggle('hide', p.dataset.pane !== v)); };
    UI.$('#up-go', s).addEventListener('click', () => {
      let changes = [], msg, trailers = 'Authored-By: operator:ops';
      if (mode === 'drop') { const dir = UI.$('#up-dir', s).value.trim().replace(/\/$/, ''); if (!picked.length) { UI.nudge(UI.$('#up-dir', s)); UI.$('#up-list', s).textContent = 'Choose or drop a file first.'; return; } changes = picked.map((f) => ({ path: (dir ? dir + '/' : '') + f.name.replace(/\.(markdown|txt)$/, '.md'), before: null, after: f.text })); msg = `import ${changes.length} file${changes.length > 1 ? 's' : ''}`; }
      else if (mode === 'paste') { const p = UI.$('#up-path', s).value.trim(), t = UI.$('#up-text', s).value; if (!/\.md$/.test(p)) return UI.nudge(UI.$('#up-path', s), 'a path ending in .md'); if (!t.trim()) return UI.nudge(UI.$('#up-text', s), 'paste the runbook first'); changes = [{ path: p, before: r.files[p] != null ? r.files[p] : null, after: t }]; msg = `add ${p} (pasted)`; }
      else { const repo = UI.$('#up-repo', s).value.trim(), src = UI.$('#up-src', s).value.trim(); const name = src.split('/').pop(); changes = [{ path: (r.kind === 'bot' ? 'runbooks/' : '') + name, before: null, after: `# ${name.replace(/\.md$/, '')}\n\nImported from ${repo}:${src}.\n\n1. Check replication lag.\n2. Promote the standby.\n3. Verify app reconnects.\n` }]; msg = `import ${src} from ${repo}`; trailers += ` · Imported-From: ${repo}@main:${src}`; }
      const c = R.commit(r, { msg, changes, trailers }); UI.closeModal(); openFile(r, changes[0].path); UI.rerender(); UI.toast(`Imported · ${c.sha}`, `${changes.length} file${changes.length > 1 ? 's' : ''}, one commit by you`, 'ok');
    });
  });
  UI.act('up-mode', (b) => { b.parentElement.querySelectorAll('button').forEach((x) => { x.classList.toggle('on', x === b); x.setAttribute('aria-pressed', x === b); }); UI.upMode(b.dataset.v); });
  /* proposals */
  UI.act('kp-sel', (cb) => { cb.checked ? ev.sel.add(cb.dataset.id) : ev.sel.delete(cb.dataset.id); rerender(); });
  const approve = (r, ps) => { ps.forEach((p) => { const after = proposedAfter(r, p); R.commit(r, { msg: `promote ${p.to} from ${p.bot}`, who: 'bot:' + p.bot, kind: 'bot', case: p.case, changes: [{ path: p.to, before: r.files[p.to] != null ? r.files[p.to] : null, after }], trailers: `Promoted-By: bot:${p.bot} · Approved-By: operator:ops` }); p.status = 'approved'; p.approver = 'ops (passkey · laptop)'; ev.sel.delete(p.id); }); r.open = ps[0].to; ev.repo = null; S.save(); rerender(); };
  const deny = (r, ps, reason) => { ps.forEach((p) => { p.status = 'denied'; p.reason = reason; ev.sel.delete(p.id); (D.chats[p.bot] = D.chats[p.bot] || []).push({ who: 'op', at: now(), text: `Denied knowledge proposal ${p.id} (${p.to}): ${reason}` }); D.audit.unshift({ ts: now(), event: 'team_knowledge_denied', who: 'operator:ops', detail: `${r.id} · ${p.id} · ${p.bot} · reason sent to the bot`, run: '' }); }); S.save(); rerender(); };
  const denyForm = (r, ps) => UI.form({ caps: `${r.id} · ${ps.map((p) => p.id).join(', ')}`, title: ps.length > 1 ? `Deny ${ps.length} proposals` : `Deny ${ps[0].id}`, fields: [{ k: 'reason', type: 'text', req: true, label: 'Reason (goes back to the proposing bot)', ph: 'e.g. Already covered in alb.md; link to it instead.' }], submit: 'Deny', onSubmit: (v) => deny(r, ps, v.reason) });
  UI.act('kp-approve', (b) => { const r = repoOf(b), p = r.proposals.find((x) => x.id === b.dataset.id); UI.stepup({ title: `Approve ${p.id}`, purpose: 'team knowledge', ok: 'Approve with passkey', what: `{ team: <b>${esc(r.name)}</b>, proposal: <b>${p.id}</b>, bot: <b>${esc(p.bot)}</b>, path: <b>${esc(p.to)}</b> }` }).then((ok) => { if (ok) { approve(r, [p]); UI.toast(`Approved ${p.id}`, `one commit to ${esc(p.to)}`, 'ok'); } }); });
  UI.act('kp-edit', (b) => { const r = repoOf(b), p = r.proposals.find((x) => x.id === b.dataset.id); guardThen(() => { openFile(r, p.to, proposedAfter(r, p)); ev.proposal = p.id; ev.dirty = true; ev.mode = 'split'; rerender(); const ed = UI.$('.repo-ed'); if (ed) ed.scrollIntoView({ block: 'start' }); }); });
  UI.act('kp-deny', (b) => { const r = repoOf(b); denyForm(r, [r.proposals.find((x) => x.id === b.dataset.id)]); });
  UI.act('kp-bulk', (b) => { const r = repoOf(b), ps = r.proposals.filter((p) => ev.sel.has(p.id) && p.status === 'pending'); if (b.dataset.d === 'deny') return denyForm(r, ps); UI.stepup({ title: `Approve ${ps.length} proposals`, purpose: 'team knowledge', ok: 'Approve with passkey', what: `{ team: <b>${esc(r.name)}</b>, proposals: <b>${ps.map((p) => p.id).join(', ')}</b> }` }).then((ok) => { if (ok) { approve(r, ps); UI.toast(`Approved ${ps.length}`, 'one commit each, the bot as author', 'ok'); } }); });
  R.pending = (id) => R.get(id).proposals.filter((p) => p.status === 'pending').length;
})();
