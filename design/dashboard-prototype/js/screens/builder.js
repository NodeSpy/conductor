/* Workflows list + the builder.
 *
 * The builder is a vertical list, top to bottom, the way conductor runs a
 * workflow: the trigger card (`on:` + filters) first, then the ordered steps,
 * with a `parallel:` step drawn as a fork that joins before the next step.
 * A "+" between every step opens a searchable picker (built-in step kinds and
 * every connector's verbs, from plugin declarations). Each step expands in
 * place into a form generated from its declared fields. Any field can insert a
 * reference to the trigger or an earlier step's output ({{.triage.kind}}),
 * from a data tree or by autocomplete. Test runs up to a step against a real
 * past event and feeds the actual outputs to later steps' pickers.
 *
 * YAML is canonical: every edit is a node-level edit through YAML.emit, so
 * comments and unknown keys survive, and editing the YAML pane updates the
 * list. Save stages the change; Deploy is the config diff → passkey → apply. */
(function () {
  const { icon, st, tag, usd, esc } = UI;
  const Y = YAML;
  const isObj = (v) => v && typeof v === 'object' && !Array.isArray(v);
  let B = null;

  /* ================= workflows list ================= */
  const wl = { q: '', owner: 'all', folder: '' };
  const enc = encodeURIComponent;
  const within = (x, f) => !f || x === f || x.startsWith(f + '/');
  const wfRows = () => {
    const staged = D.pending.filter((c) => c.coll === 'workflows'), drafts = Object.keys(D.drafts || {});
    const newOnes = [...new Set([...staged.filter((c) => c.before == null).map((c) => c.key), ...drafts.filter((n) => !D.workflows.find((w) => w.name === n))])];
    return D.workflows.map((w) => ({ w, owner: UI.wfOwner(w), folder: UI.folderOfWf(w) })).concat(newOnes.map((n) => ({ w: { name: n, on: 'draft', runs24: 0, ok: 1, cost24: 0, source: 'config', fresh: true }, owner: 'config', folder: '' })));
  };
  const OWNER = { pack: (w) => `pack ${UI.packOf(w)}`, bot: () => `bot · ${UI.term('synthesized')}`, config: () => 'your config' };
  const moveOff = (r) => (r.owner === 'bot' ? `Follows ${r.w.name.slice(4)}'s folder: move the bot (or its team) to move it` : r.w.fresh ? 'Save it first' : '');
  /** The workflows table, used by the Workflows page and by a folder's Workflows tab. */
  UI.wfTable = (ws, opts = {}) => {
    const staged = D.pending.filter((c) => c.coll === 'workflows');
    const rows = Array.isArray(ws) && ws.length && ws[0].w ? ws : (ws || []).map((w) => ({ w, owner: UI.wfOwner(w), folder: UI.folderOfWf(w) }));
    const vis = (r) => opts.folder != null || ((wl.owner === 'all' || wl.owner === r.owner) && (wl.q ? (r.w.name + ' ' + r.w.on + ' ' + r.folder).toLowerCase().includes(wl.q.toLowerCase()) : !wl.folder || (wl.folder === '__top__' ? !r.folder : within(r.folder, wl.folder))));
    const bulk = opts.folder == null;
    return `<div class="panel"><table class="tbl responsive wl-table"><thead><tr>${bulk ? `<th class="sel"><input type="checkbox" class="cbx" data-on-change="wl-sel-all" aria-label="Select all shown"${UI.wlSel.size && rows.filter(vis).every((r) => UI.wlSel.has(r.w.name)) ? ' checked' : ''}></th>` : ''}<th>Workflow</th><th>Starts when</th><th>Folder</th><th>Owner</th><th class="num">Last 24 hours</th>${UI.usageOn() ? `<th>Usage by provider</th>` : ''}<th></th></tr></thead><tbody id="${opts.folder != null ? 'fwl-body' : 'wl-body'}">
    ${rows.map((r) => { const w = r.w, d = (D.drafts || {})[w.name] != null, st2 = staged.find((c) => c.key === w.name);
      return `<tr class="click${vis(r) ? '' : ' hidden'}" data-row="${esc(w.name + ' ' + w.on + ' ' + r.folder)}" data-owner="${r.owner}" data-folder="${esc(r.folder)}" ${moveOff(r) ? '' : `draggable="true" data-wf-drag="${esc(w.name)}"`}>
      ${bulk ? `<td class="sel" data-l=""><input type="checkbox" class="cbx" data-act="wl-sel" data-name="${esc(w.name)}" aria-label="Select ${esc(w.name)}"${UI.wlSel.has(w.name) ? ' checked' : ''}></td>` : ''}<td data-l="workflow"><a class="link-plain" href="#/workflows/${w.name}"><b>${w.name}</b></a> ${w.kind === 'workflow' ? tag('callable') : ''}${d ? tag('draft', 'wait') : ''}${st2 ? tag(st2.before == null ? 'new · staged' : 'edit staged', 'run') : ''}</td>
      <td data-l="on" class="small dim${UI.humanOn(w.on) === (w.on || '') ? ' mono' : ''}" data-tip="<span class='mono'>${esc(w.on || '')}</span>">${esc(UI.humanOn(w.on))}</td>
      <td data-l="folder">${r.folder ? `<a class="small link-plain folder-link muted" href="#/folders/${enc(r.folder)}/workflows">${esc(r.folder)}</a>` : '<span class="muted small">top level</span>'}</td>
      <td data-l="owner">${UI.label(esc(String(OWNER[r.owner](w)).replace(/<[^>]+>/g, '')), { cls: 'owner ' + (r.owner === 'config' ? 'op' : 'pack') })}</td>
      <td data-l="last 24 hours" class="num">${w.runs24 && w.ok < 0.9 ? UI.statusMark('wait', `${Math.round(w.ok * w.runs24)} of ${w.runs24} fine`) : `<span class="muted">${w.runs24 ? `${w.runs24} run${w.runs24 === 1 ? '' : 's'}` : 'no runs'}</span>`}</td>${UI.usageOn() ? `<td data-l="usage" class="small">${UI.provSet(UI.wfProv(w.name), { nosrc: true })}</td>` : ''}
      <td class="num nowrap wl-acts"><a class="btn sm ghost" href="#/workflows/${w.name}" aria-label="Open ${w.name} in the builder" data-tip="Open in the builder">${icon('canvas')}</a><button class="btn sm ghost" ${moveOff(r) ? UI.off(moveOff(r)) : `data-act="wl-move" data-name="${esc(w.name)}"`} aria-label="Move ${w.name} to a folder" data-tip="${moveOff(r) ? esc(moveOff(r)) : 'Move to…'}">${icon('folder')}</button> ${w.fresh ? `<button class="btn sm ghost" ${UI.off('Deploy it first; a draft has never run')}>${icon('play')}</button>` : `<button class="btn sm ghost" data-act="wf-run" data-name="${w.name}" aria-label="Run ${w.name}" data-tip="Run now">${icon('play')}</button>`}
        ${r.owner === 'config' && !w.fresh ? `<button class="btn sm ghost" data-act="wf-del" data-name="${w.name}" aria-label="Delete ${w.name}" data-tip="Delete">${icon('trash')}</button>` : r.owner === 'config' ? `<button class="btn sm ghost" data-act="wf-discard" data-name="${w.name}" aria-label="Discard draft ${w.name}" data-tip="Discard draft">${icon('trash')}</button>` : ''}</td></tr>`; }).join('')}
    <tr class="empty-row${rows.some(vis) ? ' hidden' : ''}" id="${opts.folder != null ? 'fwl-empty' : 'wl-empty'}"><td colspan="9">No workflow matches.</td></tr></tbody></table></div>`;
  };
  function folderNav(rows) {
    const fs = UI.allFolders().filter((f) => rows.some((r) => within(r.folder, f)));
    const item = (key, label, depth, rs) => { const ws = rs.map((r) => r.w), wr = UI.wfRoll(ws); return `<a href="#" class="wfn${wl.folder === key ? ' on' : ''}" data-act="wl-folder" data-f="${esc(key)}" ${key && key !== '__top__' ? `data-wf-drop="${esc(key)}"` : key === '__top__' ? 'data-wf-drop=""' : ''} style="--d:${depth}" aria-current="${wl.folder === key}">${icon(key ? 'folder' : 'workflows')}<span class="wfn-name"><span class="ellipsis" title="${esc(key && key !== '__top__' ? key : label)}">${esc(label)}</span>${wr.failing ? `<span class="wfn-roll"><b class="danger-text">${wr.failing} failing</b></span>` : ''}</span>${UI.count(wr.n)}</a>`; };
    return `<div class="panel-head"><h3>Folders</h3><a class="link tiny" href="#/bots">shared with bots</a></div><div class="wfn-list">${item('', 'All workflows', 0, rows)}${fs.map((f) => item(f, f.split('/').pop(), f.split('/').length, rows.filter((r) => within(r.folder, f)))).join('')}${rows.some((r) => !r.folder) ? item('__top__', 'Top level', 1, rows.filter((r) => !r.folder)) : ''}</div><div class="panel-foot tiny muted">Drag a workflow onto a folder, or use Move to…. Pack workflows sit under their pack until you place them.</div>`;
  }
  UI.route('workflows', () => {
    const rows = wfRows();
    if (wl.folder && wl.folder !== '__top__' && !UI.allFolders().includes(wl.folder)) wl.folder = '';
    const crumbs = wl.folder && wl.folder !== '__top__' ? `<div class="crumbs wl-crumbs"><a href="#" data-act="wl-folder" data-f="">All workflows</a>${wl.folder.split('/').map((seg, i, a) => ` › <a href="#" data-act="wl-folder" data-f="${esc(a.slice(0, i + 1).join('/'))}">${esc(seg)}</a>`).join('')} <a class="link tiny" href="#/folders/${enc(wl.folder)}/workflows" style="margin-left:8px">open the folder</a></div>` : '';
    return `<div class="page">
    ${UI.head('Workflows', { lede: (() => { const ws = rows.map((r) => r.w), bad = ws.filter((w) => w.runs24 && w.ok < 0.9); return `${bad.length ? `<b>${bad.length} ${bad.length === 1 ? 'workflow is' : 'workflows are'} failing some runs:</b> ${bad.map((w) => esc(w.name)).join(', ')}. ` : '<b>Every workflow is running fine.</b> '}${ws.length} workflows; ${ws.reduce((a, w) => a + (w.runs24 || 0), 0)} runs in the last 24 hours.`; })(), lede2: `A ${UI.term('workflow')} is a fixed recipe: when something happens, run these steps. Want an AI to decide what to do? Hand the event to a <a class="link" href="#/bots">bot</a>.`, actions: `<button class="btn primary" data-act="wf-new">${icon('plus')}New workflow</button>` })}
    <div class="wl-layout"><aside class="panel wl-folders">${folderNav(rows)}</aside><div class="wl-main">
    ${crumbs}
    <div class="filters"><div class="input-icon">${icon('search')}<input class="input" placeholder="name, event or folder" value="${esc(wl.q)}" data-on-input="wl-q" aria-label="Filter workflows"></div>${UI.seg([['all', 'all'], ['config', 'yours'], ['pack', 'packs'], ['bot', 'bots']], wl.owner, 'wl-owner')}
      ${UI.selectChip({ icon: 'folder', on: !!wl.folder, label: 'Folder', cls: 'wl-fsel', attrs: 'data-on-change="wl-folder-sel"', options: `<option value="">all folders</option>${UI.allFolders().filter((f) => rows.some((r) => within(r.folder, f))).map((f) => `<option value="${esc(f)}"${f === wl.folder ? ' selected' : ''}>${esc(f)}</option>`).join('')}` })}
      <span class="count-note small muted" id="wl-note">${wl.q && wl.folder ? 'searching every folder' : ''}</span></div>
    ${UI.wfTable(rows)}${wlBulkBar()}</div></div></div>`;
  });
  /* bulk: tick rows (shift-click for a range), then one action stages one change per workflow, reviewed as one diff */
  UI.wlSel = UI.wlSel || new Set(); let wlLast = null;
  const wlBulkBar = () => { const n = UI.wlSel.size; if (!n) return ''; const ws = [...UI.wlSel].map((x) => S.view().workflows.find((w) => w.name === x)).filter(Boolean), cfg = ws.filter((w) => UI.wfOwner(w) === 'config').length;
    return UI.bulkBar(n, 'workflow', [['Move to folder…', 'folder', 'wl-bulk-move'], ['Disable', 'pause', 'wl-bulk-enable', 'data-v="false"', cfg ? '' : 'Only workflows in your config can be switched off here'], ['Enable', 'play', 'wl-bulk-enable', 'data-v="true"', cfg ? '' : 'Only workflows in your config can be switched on here']], 'wl-bulk-clear'); };
  UI.act('wl-sel', (cb, e) => { const boxes = UI.$$('#wl-body input[data-act="wl-sel"]').filter((x) => !x.closest('tr').classList.contains('hidden')), i = boxes.indexOf(cb);
    if (e && e.shiftKey && wlLast != null) { const [a, b] = [Math.min(wlLast, i), Math.max(wlLast, i)]; boxes.slice(a, b + 1).forEach((x) => { x.checked = cb.checked; cb.checked ? UI.wlSel.add(x.dataset.name) : UI.wlSel.delete(x.dataset.name); }); } else cb.checked ? UI.wlSel.add(cb.dataset.name) : UI.wlSel.delete(cb.dataset.name);
    wlLast = i; UI.rerender({ keepScroll: true }); });
  UI.act('wl-sel-all', (cb) => { UI.$$('#wl-body tr[data-row]:not(.hidden) input[data-act="wl-sel"]').forEach((x) => (cb.checked ? UI.wlSel.add(x.dataset.name) : UI.wlSel.delete(x.dataset.name))); UI.rerender({ keepScroll: true }); });
  UI.act('wl-bulk-clear', () => { UI.wlSel.clear(); UI.rerender({ keepScroll: true }); });
  UI.act('wl-bulk-move', () => { const names = [...UI.wlSel], bots = names.filter((n) => UI.wfOwner(S.view().workflows.find((w) => w.name === n)) === 'bot');
    UI.form({ caps: 'move', title: `Move ${names.length} workflow${names.length > 1 ? 's' : ''}`, values: { to: '' }, fields: [{ k: 'to', type: 'enum', req: true, label: 'Folder', options: [['', 'top level']].concat(UI.allFolders().map((f) => [f, f])), create: 'folder', createLabel: 'New folder' }],
      note: `One staged change per workflow, reviewed together as one diff.${bots.length ? ` ${bots.length} bot workflow${bots.length > 1 ? 's follow their bot' : ' follows its bot'} and stay${bots.length > 1 ? '' : 's'} put.` : ''}`, submit: 'Stage moves', submitIcon: 'folder',
      onSubmit: (v) => { let last = null; names.forEach((n) => { const ch = UI.stageWorkflowMove(n, v.to || ''); if (ch) last = ch; }); UI.wlSel.clear(); UI.staged(last); } }); });
  UI.act('wl-bulk-enable', (b) => { const on = b.dataset.v === 'true'; let last = null, skipped = 0;
    [...UI.wlSel].forEach((n) => { const w = S.view().workflows.find((x) => x.name === n); if (UI.wfOwner(w) !== 'config') { skipped++; return; } const y = S.staged('workflows', n) || w.yaml || ''; const ny = /^enabled:.*$/m.test(y) ? (on ? y.replace(/^enabled:.*\n/m, '') : y.replace(/^enabled:.*$/m, 'enabled: false')) : on ? y : y.replace(/^(name: .*\n)/m, '$1enabled: false\n'); const ch = S.stage('workflows', n, ny, `${on ? 'enable' : 'disable'} workflow ${n}`); if (ch) last = ch; });
    if (skipped) UI.toast(`${skipped} skipped`, 'pack and bot workflows are switched on or off in their pack or bot', 'idle'); UI.wlSel.clear(); UI.staged(last); });
  const wlFilter = () => { let n = 0; UI.$$('#wl-body tr[data-row]').forEach((tr) => { const f = tr.dataset.folder; const ok = (wl.owner === 'all' || tr.dataset.owner === wl.owner) && (wl.q ? tr.dataset.row.toLowerCase().includes(wl.q.toLowerCase()) : !wl.folder || (wl.folder === '__top__' ? !f : within(f, wl.folder))); tr.classList.toggle('hidden', !ok); if (ok) n++; }); const e = UI.$('#wl-empty'); if (e) e.classList.toggle('hidden', n > 0); const note = UI.$('#wl-note'); if (note) note.textContent = wl.q && wl.folder ? 'searching every folder' : ''; };
  UI.act('wl-q', (el) => { wl.q = el.value; wlFilter(); });
  UI.act('wl-owner', (b) => { wl.owner = b.dataset.v; UI.$$('[data-act="wl-owner"]').forEach((x) => { x.classList.toggle('on', x === b); x.setAttribute('aria-pressed', x === b); }); wlFilter(); });
  UI.act('wl-folder', (a) => { wl.folder = a.dataset.f; UI.rerender(); });
  UI.act('wl-folder-sel', (s2) => { wl.folder = s2.value; UI.rerender(); });
  /** Move a workflow: your config gets folder:; a pack workflow is placed by your pack override; a bot's follows the bot. */
  UI.stageWorkflowMove = (name, folder) => {
    const w = S.view().workflows.find((x) => x.name === name), o = UI.wfOwner(w); let ch = null;
    if (o === 'bot') return null;
    if (o === 'pack') { const inst = UI.packOf(w), c = JSON.parse(JSON.stringify(S.staged('packs', inst))); c.folders = Object.assign({}, c.folders); if (folder === `packs/${inst}`) delete c.folders[name]; else c.folders[name] = folder; if (!Object.keys(c.folders).length) delete c.folders; ch = S.stage('packs', inst, c, `move pack workflow ${name} → ${folder || 'top level'} (packs.${inst}.folders)`); }
    else { const y = S.staged('workflows', name) || w.yaml || ''; const line = `folder: "${folder}"`; const ny = /^folder:.*$/m.test(y) ? (folder ? y.replace(/^folder:.*$/m, line) : y.replace(/^folder:.*\n/m, '')) : folder ? y.replace(/^(name: .*\n)/m, `$1${line}\n`) : y; ch = S.stage('workflows', name, ny, `move workflow ${name} → ${folder || 'top level'}`); }
    D.folderDrafts = (D.folderDrafts || []).filter((f) => !within(folder || '\u0000', f));
    return ch;
  };
  UI.moveWorkflow = (name, folder) => {
    if (UI.wfOwner(S.view().workflows.find((x) => x.name === name)) === 'bot') return;
    const ch = UI.stageWorkflowMove(name, folder);
    S.save(); UI.refreshChrome(); UI.rerender();
    UI.toast(ch ? `Move staged: ${esc(name)} → ${esc(folder || 'top level')}` : 'Already there', ch ? 'folders live in config: review and apply it with the other staged changes' : '', ch ? 'run' : 'idle');
  };
  UI.act('wl-move', (b) => {
    const name = b.dataset.name, w = S.view().workflows.find((x) => x.name === name), cur = UI.folderOfWf(w), o = UI.wfOwner(w);
    UI.form({ caps: o === 'pack' ? `packs.${UI.packOf(w)}.folders.${name}` : `triggers[name=${name}].folder`, title: `Move ${name}`, values: { to: cur },
      fields: [{ k: 'to', type: 'enum', req: true, label: 'Folder', options: [['', 'top level']].concat([...new Set(UI.allFolders().concat(o === 'pack' ? [`packs/${UI.packOf(w)}`] : []))].sort().map((f) => [f, f])), create: 'folder', createLabel: 'New folder', createCtx: { parent: cur } }],
      note: o === 'pack' ? `A pack can't choose its own folder: its workflows sit under <span class="mono">packs/${esc(UI.packOf(w))}</span> until you place them. This is your pack override, so it goes through the diff.` : 'Folders live in config, so the move is staged for the diff. It changes nothing else: folders carry no behavior.',
      submit: 'Stage move', submitIcon: 'folder', onSubmit: (v) => UI.moveWorkflow(name, v.to || '') });
  });
  // drag a row onto a folder in the tree (mouse; touch uses Move to…)
  document.addEventListener('dragstart', (e) => { const tr = e.target.closest && e.target.closest('[data-wf-drag]'); if (!tr) return; e.dataTransfer.setData('text/wf', tr.dataset.wfDrag); e.dataTransfer.effectAllowed = 'move'; tr.classList.add('dragging'); });
  document.addEventListener('dragend', (e) => { const tr = e.target.closest && e.target.closest('[data-wf-drag]'); if (tr) tr.classList.remove('dragging'); UI.$$('.wfn.drop-on').forEach((x) => x.classList.remove('drop-on')); });
  document.addEventListener('dragover', (e) => { const t = e.target.closest && e.target.closest('[data-wf-drop]'); if (!t || ![...(e.dataTransfer.types || [])].includes('text/wf')) return; e.preventDefault(); UI.$$('.wfn.drop-on').forEach((x) => x !== t && x.classList.remove('drop-on')); t.classList.add('drop-on'); });
  document.addEventListener('drop', (e) => { const t = e.target.closest && e.target.closest('[data-wf-drop]'); const name = e.dataTransfer && e.dataTransfer.getData('text/wf'); if (!t || !name) return; e.preventDefault(); UI.moveWorkflow(name, t.dataset.wfDrop); });
  UI.act('wf-new', () => UI.form({ caps: 'new workflow', title: 'Name your workflow', fields: [{ k: 'name', label: 'Name', type: 'string', req: true, ph: 'e.g. release-notify', pattern: /^[a-z][a-z0-9-]*$/, patternMsg: 'Lowercase letters, digits and dashes.', validate: (v) => (D.workflows.find((w) => w.name === v) || (D.drafts || {})[v] != null ? 'A workflow by that name exists.' : '') }, { k: 'kind', label: 'Kind', type: 'seg', options: [['trigger', 'Triggered (on: an event)'], ['workflow', 'Reusable (call: it)']], def: 'trigger' }], submit: 'Create', onSubmit: (v) => { D.drafts = D.drafts || {}; D.drafts[v.name] = v.kind === 'workflow' ? `name: ${v.name}\ninputs:\n  repo: { type: string, required: true }\nsteps: []\n` : `name: ${v.name}\nsteps: []\n`; S.save(); UI.go('#/workflows/' + v.name); } }));
  UI.act('wf-run', (b) => {
    const w = D.workflows.find((x) => x.name === b.dataset.name); let ins = {};
    try { ins = (Y.parse(w.yaml || '').value || {}).inputs || {}; } catch (e) {}
    const evs = SCHEMA.events.filter((e) => w.on && w.on.includes(e.on));
    UI.form({ caps: 'manual run · control run', title: `Run ${w.name}`, intro: Object.keys(ins).length ? 'Inputs from the workflow\'s inputs:' : 'Replays a past event through the workflow, or runs with no event.',
      fields: Object.keys(ins).length ? Object.entries(ins).map(([k, d]) => ({ k, type: d.type === 'integer' ? 'number' : 'string', req: !!d.required, def: d.default })) : [{ k: 'event', label: 'Event', type: 'enum', options: [['', 'no event (manual)']].concat(evs.map((e) => [e.id, e.label])) }],
      submit: 'Run now', submitIcon: 'play',
      onSubmit: (v) => {
        const id = 'r-' + Math.random().toString(16).slice(2, 6), ev = SCHEMA.events.find((e) => e.id === v.event);
        const graph = (w.steps && w.steps.length ? w.steps : S.deriveGraph(w.yaml || ''));
        D.runs.unshift({ id, workflow: w.name, trigger: ev ? ev.on + ' (replay)' : 'manual', repo: ev && ev.payload.repo || '', number: ev && ev.payload.number || 0, status: 'run', started: new Date().toISOString(), cost: 0, steps: graph.map((s, i) => ({ id: s.id, status: i === 0 ? 'run' : 'idle', duration_ms: 0, cost_usd: 0 })) });
        if (!w.steps || !w.steps.length) w.steps = graph;
        D.audit.unshift({ ts: new Date().toISOString(), event: 'workflow_run', who: 'operator:ops', detail: `${w.name} · manual · ${id}`, run: id });
        S.save(); UI.refreshChrome(); UI.go('#/runs/' + id);
      } });
  });
  UI.act('wf-del', (b) => UI.confirm({ title: `Delete ${b.dataset.name}?`, body: 'Stages removing the trigger from config.yaml. Review the diff and apply with your passkey; nothing changes until then.', ok: 'Stage removal', danger: true }).then((ok) => { if (ok) UI.staged(S.stage('workflows', b.dataset.name, null)); }));
  UI.act('wf-discard', (b) => { if (D.drafts) delete D.drafts[b.dataset.name]; const c = S.find('workflows', b.dataset.name); if (c) S.unstage(c.id); S.save(); UI.rerender(); });

  /* ================= builder state ================= */
  function synthBot(name) { const m = D.bots.find((x) => 'bot:' + x.name === name) || D.bots[0]; return `# synthesized from bots.${m.name} — edit the bot, not this\nname: bot:${m.name}\non: [pd.incident, manual]\nsteps:\n  - id: deliver\n    bot: ${m.name}\n    title: "{{.title}}"\n`; }
  function load(name) {
    const w = D.workflows.find((x) => x.name === name);
    const drafts = (D.drafts = D.drafts || {});
    const owner = !w ? (drafts[name] != null || S.find('workflows', name) ? 'config' : 'missing') : w.source === 'config' ? 'config' : w.pack ? 'pack' : 'bot';
    const live = owner === 'config' ? S.cfg.workflows.get(name) : w ? w.yaml || synthBot(name) : null;
    const staged = owner === 'config' ? S.staged('workflows', name) : live;
    const text = drafts[name] != null ? drafts[name] : staged != null ? staged : live || '';
    B = { name, owner, w, live, text, hist: [text], hi: 0, open: new Set(), sel: null, tests: {}, ev: '', side: 'yaml', pane: 'steps', lastPush: 0, pack: w && w.pack, ov: null };
    if (owner === 'pack') { const p = D.packs.find((x) => x.name === w.pack); B.ov = Y.clone(S.staged('packs', w.pack) || p.cfg); B.ovBase = Y.clone(B.ov); }
    parse();
    const first = WF.walk(B.value || {})[0];
    if (B.value && !B.value.on && !B.value.inputs) B.open.add('$trigger');
    else if (first && first.step && first.step.id) B.open.add(first.step.id);
    B.sel = first ? first.id : null;
    const evs = SCHEMA.events.filter((e) => WF.sources(B.value || {}).some((s) => s.on === e.on));
    B.ev = evs.length ? evs[0].id : '';
  }
  function parse() {
    try { const p = Y.parse(B.text); B.value = p.value || {}; B.meta = p.meta; B.err = null; } catch (e) { B.err = e; }
    B.issues = B.err ? [{ level: 'error', line: B.err.line, msg: B.err.msg || B.err.message }] : WF.validate(B.text, { reusable: !!(B.value && B.value.inputs) });
  }
  const readOnly = () => B.owner === 'bot' ? 'Synthesized from the bot definition: edit the bot instead' : B.owner === 'pack' ? 'Pack-owned: the structure is the pack\'s. Edit step fields to make overrides, or call this workflow from your own trigger' : '';
  const errs = () => B.issues.filter((i) => i.level === 'error');
  const status = () => {
    if (B.owner === 'pack') return YAML.eq(B.ov, B.ovBase) ? (S.find('packs', B.pack) ? 'saved' : 'live') : 'draft';
    if (B.owner !== 'config') return 'live';
    const staged = S.staged('workflows', B.name);
    if (B.text !== (staged == null ? '' : staged)) return 'draft';
    if (staged !== B.live) return 'saved';
    return 'live';
  };
  function saveDraft() {
    if (B.owner !== 'config') return;
    const staged = S.staged('workflows', B.name);
    if (B.text === (staged == null ? '' : staged)) delete D.drafts[B.name]; else D.drafts[B.name] = B.text;
    S.save();
  }
  /** Commit a new YAML text. soft: keep the list DOM (typing in a field). */
  function commit(text, opts = {}) {
    if (text === B.text && !opts.force) return;
    B.text = text;
    const now = Date.now();
    if (opts.coalesce && now - B.lastPush < 1200 && B.hi > 0) B.hist[B.hi] = text;
    else { B.hist = B.hist.slice(0, B.hi + 1); B.hist.push(text); B.hi = B.hist.length - 1; }
    B.lastPush = now;
    parse(); saveDraft();
    if (opts.fromYaml) { drawList(); drawBar(); drawIssues(); return; }
    if (opts.soft) { softUpdate(); return; }
    redraw();
  }
  function mutate(fn, opts) {
    flushPending();
    if (B.err) { UI.toast('Fix the YAML first', `line ${B.err.line}: ${esc(B.err.msg || B.err.message)}`, 'fail'); return false; }
    const v = Y.clone(B.value), had = new Set(Object.keys(B.value || {}));
    const r = fn(v);
    if (r === false) return false;
    commit(Y.emit(canonical(v, had), B.meta), opts);
    return true;
  }
  /* A newly added trigger key goes where you'd write it (name, folder, on, enabled, filter, group, policy … before steps), not after steps. Existing keys keep their order. */
  const CANON = ['name', 'extends', 'abstract', 'folder', 'description', 'on', 'enabled', 'repo', 'filter', 'group', 'policy', 'gate', 'shadow', 'callable', 'options', 'inputs', 'outputs', 'steps', 'hooks'];
  function canonical(v, had) { if (!v || typeof v !== 'object' || Array.isArray(v)) return v; const keys = Object.keys(v), fresh = keys.filter((k) => !had.has(k) && CANON.includes(k)); if (!fresh.length) return v;
    const out = {}, rank = (k) => CANON.indexOf(k); keys.filter((k) => !fresh.includes(k)).forEach((k) => { fresh.filter((f) => !(f in out) && rank(f) < rank(k) && rank(k) >= 0).sort((a, b) => rank(a) - rank(b)).forEach((f) => { out[f] = v[f]; }); out[k] = v[k]; }); fresh.forEach((f) => { if (!(f in out)) out[f] = v[f]; }); return out; }
  const allIds = (v) => WF.walk(v).map((e) => e.step && e.step.id).filter(Boolean);
  const uniqueId = (v, base) => { base = String(base || 'step').replace(/[^\w-]/g, '-').replace(/^[^a-z]+/i, '') || 'step'; const ids = new Set(allIds(v)); if (!ids.has(base)) return base; let n = 2; while (ids.has(`${base}-${n}`)) n++; return `${base}-${n}`; };
  const locOf = (s) => s.split('.').map((x) => (/^\d+$/.test(x) ? +x : x));
  const entryById = (id) => WF.walk(B.value || {}).find((e) => e.id === id);
  const evPayload = () => { const e = SCHEMA.events.find((x) => x.id === B.ev); return e ? e.payload : null; };
  /** Rename a step id in meta too, so its comments follow it. */
  function rekey(oldId, newId) { Object.keys(B.meta).forEach((k) => { const n = k.split(`[id=${oldId}]`).join(`[id=${newId}]`); if (n !== k) { B.meta[n] = B.meta[k]; delete B.meta[k]; } }); }

  /* ================= rendering ================= */
  const KC = (k) => WF.KINDS[k] || WF.KINDS.unknown;
  const issuesFor = (id) => B.issues.filter((i) => i.step === id);
  const pill = (r) => !r ? '' : r.status === 'ok' ? UI.statusMark('ok', `ok · ${r.ms}ms`, { cls: 'tpill ok', sm: true }) : r.status === 'error' ? UI.statusMark('fail', `error`, { cls: 'tpill fail', sm: true }) : r.status === 'skipped' ? UI.statusMark('skip', `skipped`, { cls: 'tpill skip', sm: true }) : UI.statusMark('idle', `not run`, { cls: 'tpill idle', sm: true });

  /* the head, as in the approved mock (builderPage): crumbs, the name, what it does, its state, and three actions */
  function bar() {
    const s = status(), ro = readOnly(), e = errs();
    const applyBlock = B.err ? 'Fix the YAML first' : e.length ? `${e.length} error${e.length > 1 ? 's' : ''} to fix first` : B.owner === 'bot' ? ro : s === 'live' ? 'Nothing to apply: this is what’s live' : '';
    const v = B.value || {}, desc = esc(v.description || (WF.sources(v).length ? `${whenWords(WF.sources(v))}${isObj(v.filter) && filterWords(v.filter) ? ' ' + filterWords(v.filter) : ''}: this runs.` : ''));
    const folder = (() => { try { const w = D.workflows.find((x) => x.name === B.name); return w && UI.folderOfWf ? UI.folderOfWf(w, false) : (B.value || {}).folder || ""; } catch (e) { return (B.value || {}).folder || ""; } })();
    return `<div class="dl-crumbs"><a href="#/workflows">Workflows</a>${folder ? `${icon('chev')}<span>${esc(folder)}</span>` : ''}</div>
      <div class="dl-phead"><div><h1 class="wfb-title">${esc(B.name)}</h1><p class="dl-lead">${desc}</p>
        <p class="wfb-owner">${B.owner === 'pack' ? `From the pack <b>${esc(B.pack)}</b>: your changes are saved as overrides.` : B.owner === 'bot' ? `Made by conductor for the bot (${UI.term('synthesized')}); read-only here.` : ''} ${UI.statusMark(s === 'draft' ? 'wait' : s === 'saved' ? 'staged' : 'ok', s === 'draft' ? 'Unsaved changes' : s === 'saved' ? 'Saved, not applied' : 'Live', { id: 'wf-state', cls: 'wf-state ' + s })}</p></div>
        <div class="dl-actions tight"><button class="btn ghost" data-act="wf-undo" ${B.hi > 0 ? '' : UI.off('Nothing to undo')}>${icon('undo')}Undo</button><button class="btn secondary" data-act="wf-test-all" ${!B.ev ? UI.off('Pick a test event under Try it first') : B.err ? UI.off('Fix the YAML first') : ''}>${icon('play')}Test</button><button class="btn primary" data-act="wf-deploy" ${applyBlock ? UI.off(applyBlock) : ''}>Save and apply</button></div></div>
      <nav class="dl-tabs" role="tablist">${[['recipe', 'Recipe'], ['config', 'Config file'], ['runs', 'Runs', D.runs.filter((r) => r.workflow === B.name).length], ['settings', 'Settings']].map(([k, l, n]) => `<a href="#" role="tab" data-act="wf-tab" data-v="${k}" class="${(B.tab || 'recipe') === k ? 'on' : ''}"${(B.tab || 'recipe') === k ? ' aria-selected="true"' : ''}>${l}${n ? ` <em>${n}</em>` : ''}</a>`).join('')}</nav>`;
  }
  function addSlot(loc, index, label) {
    const ro = readOnly();
    return `<div class="spine-add"><button type="button" class="plus" ${ro ? UI.off(ro) : `data-act="wf-add" data-loc="${loc}" data-i="${index}"`} aria-label="${label || 'Add a step here'}">${icon('plus')}</button></div>`;
  }

  // the trigger in words: "A CI check fails", then where and unless (the mock's When box)
  const EV_WORDS = { 'gh.check_failed': 'A CI check fails', 'gh.review_requested': 'Someone asks for a review', 'gh.issue_opened': 'An issue is opened', 'gh.pr_opened': 'A pull request is opened', 'pd.incident': 'A PagerDuty incident opens', manual: 'You start it' };
  const whenWords = (srcs) => (srcs.length ? srcs.map((x) => EV_WORDS[x.on] || (/^timer\./.test(x.on) ? 'On its schedule' : x.on)).join(' or ') : 'Nothing starts it yet');
  const filterWords = (f) => { if (!isObj(f)) return ''; const out = []; if (f.repo) out.push(`on ${[].concat(f.repo).join(' or ')}`); if (f.not_label_any) out.push(`unless it’s labelled “${[].concat(f.not_label_any).join('” or “')}”`); if (f.label_any) out.push(`only when labelled “${[].concat(f.label_any).join('” or “')}”`); Object.keys(f).filter((k) => !['repo', 'not_label_any', 'label_any'].includes(k)).forEach((k) => out.push(`${k}: ${[].concat(f[k]).join(', ')}`)); return out.join(', '); };
  function triggerCard() {
    const v = B.value || {}, open = B.open.has('$trigger'), srcs = WF.sources(v), iss = B.issues.filter((i) => i.trigger), reusable = !!v.inputs;
    const ro = readOnly();
    const sum = reusable ? `called with ${Object.keys(v.inputs).join(', ') || 'no inputs'}` : srcs.length ? srcs.map((s) => s.on).join(' · ') : 'no trigger yet';
    const filters = isObj(v.filter) ? Object.entries(v.filter).map(([k, x]) => tag(`${k}: ${[].concat(x).join(', ')}`)).join(' ') : '';
    const evs = B.ev && SCHEMA.events.find((e) => e.id === B.ev);
    return `<div class="trig-card${open ? ' open' : ''}${iss.some((i) => !i.todo) ? ' has-err' : ''}" id="card-$trigger">
      <div class="sc-head" role="button" tabindex="0" data-act="wf-toggle" data-id="$trigger" aria-expanded="${open}">
        <span class="wl">${icon(reusable ? 'link' : 'zap')}${reusable ? 'Called' : 'When'}</span>
        <div class="grow"><b>${reusable ? esc(sum) : esc(whenWords(srcs))}</b> <span class="dl-sub">${esc(filterWords(v.filter))}</span></div>
        ${iss.some((i) => !i.todo) ? UI.statusMark('fail', `${iss.filter((i) => !i.todo).length}`, { cls: 'tpill fail', sm: true }) : iss.length ? UI.statusMark('idle', `to do`, { cls: 'tpill todo', sm: true }) : evs ? UI.statusMark('ok', `${esc(evs.label.split(' · ')[0])}`, { cls: 'tpill ok', tip: 'Test event', sm: true }) : ''}
        <span class="chev">${icon(open ? 'chevd' : 'chev')}</span></div>
      ${open ? `<div class="sc-body">${iss.map((i) => i.todo ? `<div class="callout small todo">${icon('dot')} ${esc(i.todo)}</div>` : `<div class="callout danger small">${esc(i.msg)}</div>`).join('')}
        ${!srcs.length && !reusable ? `<div class="empty small"><span class="serif">What starts this workflow?</span>An event from a connector, a schedule, or a manual run.<button class="btn primary" style="margin-top:8px" ${ro ? UI.off(ro) : 'data-act="wf-trig-pick"'}>${icon('plus')}Choose a trigger</button></div>` : ''}
        ${reusable ? inputsEditor(v, ro) : srcs.length ? sourcesEditor(v, srcs, ro) : ''}
      </div>` : ''}
    </div>`;
  }
  const fltValInput = (k, ro) => (RS.boolFilterKeys.has(RS.filterBase(k)) ? `<select class="select mono" id="flt-val" aria-label="New filter value" ${ro ? 'disabled' : ''}><option value="true">true</option><option value="false">false</option></select>` : `<input class="input mono" id="flt-val" placeholder="${/expr$/.test(k) ? "!is_draft && !contains(title, 'Release')" : 'value, value'}" aria-label="New filter value" ${ro ? 'readonly' : ''}>`);
  function sourcesEditor(v, srcs, ro) {
    const allowed = new Set(); srcs.forEach((s) => { const e = SCHEMA.event(s.on); (e && e.filters || []).forEach((k) => allowed.add(k)); });
    const dis = ro ? UI.off(ro) : '';
    return `<div class="caps">Sources</div>
      ${srcs.map((s, i) => { const e = SCHEMA.event(s.on); return `<div class="src-row"><span class="sc-ic sm">${icon(e && e.conn ? e.conn.icon : e ? e.icon : 'live')}</span><div class="grow"><b class="mono small">${esc(s.on)}</b><div class="muted tiny">${e ? esc(e.label) : 'conductor lifecycle event'}</div></div><button class="btn sm ghost" ${ro ? dis : `data-act="wf-src-del" data-i="${i}"`} aria-label="Remove source ${esc(s.on)}">${icon('trash')}</button></div>`; }).join('')}
      <button class="btn sm" ${ro ? dis : 'data-act="wf-trig-pick" data-add="1"'}>${icon('plus')}Add a source</button>
      <div class="caps" style="margin-top:6px">Filters <span class="muted" style="text-transform:none;letter-spacing:0">· a map is AND; any key takes not_; expr: adds a condition</span></div>${typeof v.filter === 'string' || Array.isArray(v.filter) ? `<div class="callout info small">This filter is ${typeof v.filter === 'string' ? 'an expression' : 'a list (OR of alternatives)'}: edit it in the YAML pane or below.</div>` : ''}
      ${Object.entries(isObj(v.filter) ? v.filter : {}).map(([k, x]) => `<div class="flt-row"><span class="mono small">${esc(k)}</span><input class="input mono" value="${esc([].concat(x).join(', '))}" data-wf-filter="${esc(k)}" ${ro ? 'readonly' : ''} aria-label="Filter ${esc(k)}"><button class="btn sm ghost" ${ro ? dis : `data-act="wf-flt-del" data-k="${esc(k)}"`} aria-label="Remove filter ${esc(k)}">${icon('trash')}</button></div>`).join('')}
      <div class="flt-row"><select class="select mono" id="flt-new" aria-label="New filter key" ${ro ? 'disabled' : ''} data-on-change="wf-flt-key">${RS.filterKeysFor([...allowed]).filter((k) => !(isObj(v.filter) && k in v.filter)).map((k) => `<option>${k}</option>`).join('')}</select><span id="flt-val-box">${fltValInput(RS.filterKeysFor([...allowed]).find((k) => !(isObj(v.filter) && k in v.filter)) || '', ro)}</span><button class="btn sm" ${ro ? dis : allowed.size ? 'data-act="wf-flt-add"' : UI.off('This event declares no filters')}>${icon('plus')}Add</button></div>
      <details class="adv"${B.advOpen && B.advOpen.has('$trigger') ? ' open' : ''} data-adv="$trigger"><summary>More: name, grouping, budget</summary><div class="stack tight" style="margin-top:8px">
        ${fieldRow('', 'name', { k: 'name', type: 'string', help: 'A trigger reachable by conductor run needs a unique name.' }, v.name, ro)}
        ${fieldRow('', 'group.key', { k: 'group.key', type: 'string', ref: true, help: 'Batch a burst of events into one run, e.g. {{.repo}}#{{.pr}}.' }, S.getPath(v, 'group.key'), ro)}
        ${fieldRow('', 'group.window', { k: 'group.window', type: 'duration' }, S.getPath(v, 'group.window'), ro)}
        ${fieldRow('', 'policy.budget.window', { k: 'policy.budget.window', type: 'duration', label: 'budget · window', ph: '24h (default)', ref: false }, S.getPath(v, 'policy.budget.window'), ro)}
        ${fieldRow('', 'policy.budget.max_tokens', { k: 'policy.budget.max_tokens', type: 'string', label: 'budget · max tokens in the window', ph: '500k or 2m', ref: false, help: 'A hard cap: over-cap runs shed and retry when the window frees.' }, S.getPath(v, 'policy.budget.max_tokens'), ro)}
        ${UI.isApi() || S.getPath(v, 'policy.budget.max_cost_usd') != null ? fieldRow('', 'policy.budget.max_cost_usd', { k: 'policy.budget.max_cost_usd', type: 'number', label: 'budget · max cost (USD) in the window', ref: false }, S.getPath(v, 'policy.budget.max_cost_usd'), ro) : ''}
        ${fieldRow('', 'callable', { k: 'callable', type: 'bool', on: 'callable over the invoke API (widening)' }, v.callable, ro)}
      </div></details>`;
  }
  function inputsEditor(v, ro) {
    const dis = ro ? UI.off(ro) : '';
    return `<div class="caps">Inputs</div>${Object.entries(v.inputs).map(([k, d]) => `<div class="flt-row"><span class="mono small">${esc(k)}</span><span class="muted tiny">${esc((d && d.type) || 'string')}${d && d.required ? ' · required' : ''}${d && d.default != null ? ' · default ' + esc(d.default) : ''}</span><button class="btn sm ghost" ${ro ? dis : `data-act="wf-in-del" data-k="${esc(k)}"`} aria-label="Remove input ${esc(k)}">${icon('trash')}</button></div>`).join('')}
      <div class="flt-row"><input class="input mono" id="in-name" placeholder="name" aria-label="Input name"><select class="select mono" id="in-type" aria-label="Input type"><option>string</option><option>integer</option><option>boolean</option></select><label class="row tiny"><input type="checkbox" class="cbx" id="in-req">required</label><button class="btn sm" ${ro ? dis : 'data-act="wf-in-add"'}>${icon('plus')}Add input</button></div>`;
  }

  /* ---------- generated step forms ---------- */
  const TEMPLATED = new Set(['text', 'string', 'number']);
  function fieldRow(loc, path, fd, val, ro, extra = '') {
    const id = 'wf-' + (loc || 'trig').replace(/\./g, '_') + '-' + path.replace(/[^\w]/g, '_');
    const label = fd.label || path.split('.').pop().replace(/_/g, ' ');
    const isu = loc ? B.issues.filter((i) => i.step === (entryAt(loc) || {}).id && i.field && (i.field === path || i.field.startsWith(path + '.'))) : B.issues.filter((i) => i.trigger && i.field === path);
    const dis = ro ? 'readonly aria-readonly="true"' : '';
    const data = `data-wf-loc="${loc}" data-wf-path="${path}" data-wf-type="${fd.type}" id="${id}"`;
    const ph = fd.def != null && fd.type !== 'enum' && fd.type !== 'bool' ? `default ${fd.def}` : fd.ph || '';
    const refBtn = fd.ref !== false && (TEMPLATED.has(fd.type) || fd.type === 'code') && !ro && fd.type !== 'enum' ? `<button type="button" class="ref-btn" data-act="wf-ref" data-target="${id}">${icon('code')}<span>Insert from the event</span></button>` : '';
    let input;
    const v = val == null ? '' : val;
    switch (fd.type) {
      case 'text': input = `<textarea class="textarea mono" rows="${fd.rows || 3}" ${data} ${dis} placeholder="${esc(ph)}">${esc(typeof v === 'object' ? JSON.stringify(v) : v)}</textarea>`; break;
      case 'code': input = `<div class="code-wrap" ${data}>${UI.editor({ id: id + '-ed', text: String(v), lang: { js: 'js', node: 'js', python3: 'python', sh: 'sh' }[fd.lang] || 'js', minRows: 7, label: 'code', readonly: !!ro })}</div>`; break;
      case 'bool': input = `<label class="row small" style="min-height:30px"><input type="checkbox" class="cbx" ${data} ${v ? 'checked' : ''} ${ro ? 'disabled' : ''}> ${fd.on || 'on'}</label>`; break;
      case 'enum': input = `<select class="select mono" ${data} ${ro ? 'disabled' : ''} ${ro ? '' : UI.pickerAttrs(fd)}>${ro ? '' : UI.createOpt(fd)}${(fd.req ? [] : [['', fd.def != null ? `default (${fd.def})` : '— none —']]).concat((fd.options || []).map((o) => (Array.isArray(o) ? o : [o, o]))).map(([ov, ol]) => `<option value="${esc(ov)}"${String(ov) === String(v) ? ' selected' : ''}>${esc(ol)}</option>`).join('')}${v !== '' && !(fd.options || []).some((o) => String(Array.isArray(o) ? o[0] : o) === String(v)) ? `<option selected value="${esc(v)}">${esc(v)}</option>` : ''}</select>`; break;
      case 'list': case 'multi': { const items = Array.isArray(v) ? v : v === '' ? [] : [v]; input = `<div class="listed" ${data}>${items.map((x) => `<span class="chip-x"><span class="mono">${esc(x)}</span>${ro ? '' : `<button type="button" data-act="wf-list-del" aria-label="Remove ${esc(x)}">${icon('x')}</button>`}</span>`).join('')}${ro ? '' : fd.type === 'multi' ? `<select class="select mono list-add-sel" data-wf-listadd="1" aria-label="Add ${esc(label)}" ${UI.pickerAttrs(fd)}><option value="">+ add…</option>${UI.createOpt(fd)}${(fd.options || []).filter((o) => !items.includes(o)).map((o) => `<option>${esc(o)}</option>`).join('')}</select>` : `<input class="input mono list-add" data-wf-listadd="1" placeholder="add, then Enter" aria-label="Add ${esc(label)}">`}</div>`; break; }
      case 'map': { const ents = Object.entries(isObj(v) ? v : {}); input = `<div class="kvedit wf-map" ${data}>${ents.concat(ro ? [] : [['', '']]).map(([a, b]) => `<div class="kvrow"><input class="input mono" placeholder="key" value="${esc(a)}" data-mk="k" ${dis}><input class="input mono" placeholder="value" value="${esc(typeof b === 'object' ? JSON.stringify(b) : b)}" data-mk="v" ${dis}><span></span></div>`).join('')}</div>`; break; }
      case 'value': input = `<div class="wf-vs" data-wf-loc="${loc}" data-wf-path="${path}" data-wf-type="value" id="${id}">${UI.VS.html(fd, typeof v === 'object' ? '' : v, id + '-v')}</div>`; break;
      case 'seg': input = `<div class="seg" role="group">${fd.options.map(([ov, ol]) => `<button type="button" ${ro ? UI.off(ro) : `data-act="wf-seg" data-loc="${loc}" data-path="${path}" data-v="${esc(ov)}"`} aria-pressed="${String(ov) === String(v || fd.def || '')}" class="${String(ov) === String(v || fd.def || '') ? 'on' : ''}">${ol}</button>`).join('')}</div>`; break;
      default: input = `<input class="input mono" value="${esc(typeof v === 'object' ? JSON.stringify(v) : v)}" ${data} ${dis} placeholder="${esc(ph)}" ${fd.list ? `list="${fd.list}"` : ''} autocomplete="off">`;
    }
    const hard = isu.filter((i) => !i.todo);
    return `<div class="field wf-field${hard.length ? ' bad' : isu.length ? ' todo' : ''}" data-field="${path}"><label for="${id}">${esc(label)} ${fd.req ? '<span class="req">*</span>' : ''}${refBtn}</label>${input}${fd.help ? `<div class="hint">${fd.help}</div>` : ''}${isu.map((i) => i.todo ? `<div class="ftodo">${icon('dot')}${esc(i.todo)}</div>` : `<div class="ferr">${esc(i.msg)}</div>`).join('')}${extra}</div>`;
  }
  const entryAt = (loc) => WF.walk(B.value || {}).find((e) => e.list.concat([e.index]).join('.') === loc);
  function formFor(e, loc, ro) {
    const s = e.step, k = e.kind, rows = [];
    const seen = new Set(['id', 'type', 'uses', 'if', 'for_each', 'timeout', 'retry', 'continue_on_error', 'hooks', 'output_schema', 'decide', 'parallel', 'bot', 'call', 'with', 'use', 'command', 'run', 'code', 'sleep', 'wait_for', 'log', 'set', 'assert', 'fail', 'skill', 'isolation', '<<']);
    const F = (path, fd) => { seen.add(path.split('.')[0]); rows.push(fieldRow(loc, path, fd, S.getPath(s, path), ro)); };
    const packRo = B.owner === 'pack';
    const ovPath = packRo ? `${B.w.yaml.match(/^name: (.*)$/m)[1]}/${s.id}` : '';
    const ovRo = (field) => (packRo && !['model', 'runtime'].includes(field) && !field.startsWith('isolation.') && !field.startsWith('options.') ? 'Structure and prompts are the pack\'s; model, runtime, isolation and options can be overridden' : '');
    const G = (path, fd) => { seen.add(path.split('.')[0]); const r = packRo ? ovRo(path) : ro; const ovv = packRo ? S.getPath((B.ov.steps || {})[ovPath] || {}, path) : undefined; rows.push(fieldRow(loc, path, Object.assign({}, fd, packRo && !r ? { help: (fd.help ? fd.help + ' ' : '') + (ovv !== undefined ? `<b>Overridden</b> in packs.${B.pack}.steps["${ovPath}"]` : `An edit becomes an override in packs.${B.pack}.steps["${ovPath}"]`) } : {}), ovv !== undefined ? ovv : S.getPath(s, path), r)); };
    if (k === 'verb' || k === 'ask') {
      const decl = SCHEMA.verb(s.uses);
      const [inst, verb] = String(s.uses).split('.');
      const c = S.view().connectors.find((x) => x.name === inst);
      const accts = c ? S.view().connectors.filter((x) => x.use === c.use) : [];
      rows.push(`<div class="acct-row"><div class="field"><label>Account <span class="opt">connector instance</span></label><select class="select mono" ${ro ? 'disabled' : `data-wf-acct="${loc}" data-picker="connector" data-ctx='${esc(JSON.stringify({ use: c ? c.use : inst }))}' data-ret="name"`} aria-label="Connected account">${ro ? '' : `<option value="__new__">+ New ${esc((SCHEMA.connectors[c ? c.use : inst] || { label: inst }).label)} connector…</option>`}${accts.length ? accts.map((a) => `<option${a.name === inst ? ' selected' : ''} value="${a.name}">${a.name} · ${a.use}${a.status !== 'ok' ? ' (' + a.status + ')' : ''}</option>`).join('') : `<option>${esc(inst)} · not connected</option>`}</select>${accts.length > 1 ? `<div class="hint">${accts.length} ${c.use} connectors: pick which account acts.</div>` : !c && !['git', 'kv'].includes(inst) ? `<div class="hint"><a class="link" href="#/connectors/connectors">Add a ${esc(inst)} connector</a> to run this.</div>` : ''}</div>
        <div class="field"><label>Action</label><select class="select mono" ${ro ? 'disabled' : `data-wf-verb="${loc}"`} aria-label="Action">${decl ? Object.entries(decl.conn.verbs).map(([vn, vd]) => `<option value="${vn}"${vn === verb ? ' selected' : ''}>${vn} · ${esc(vd.label)}</option>`).join('') : `<option>${esc(verb)}</option>`}</select></div></div>`);
      if (decl) decl.fields.forEach((f) => G('options.' + f.k, Object.assign({}, f, { type: f.type === 'duration' ? 'string' : f.type === 'secret' ? 'string' : f.type })));
    } else if (k === 'agent') {
      // the agent card in three named groups (Riley polish, waited for Daylight): what it does, where it runs, isolation
      rows.push('<div class="sc-sec">What it does</div>');
      if (packRo) rows.push(`<div class="field"><label>prompt</label><div class="prompt-ro mono small">${esc(s.prompt || s.prompt_file || '')}</div><div class="hint">The prompt is the pack's.</div></div>`);
      else F('prompt', { type: 'text', rows: 5, help: 'Optional: omit it and the agent gets the event. Insert data with “Insert from the event” or by typing {{.' });
      rows.push('<div class="sc-sec">Where it runs</div>');
      G('runtime', { type: 'enum', options: S.view().runtimes.map((r) => r.name), def: (D.runtimes.find((r) => r.cfg && r.cfg.default) || {}).name, create: 'runtime', createLabel: 'New runtime' });
      G('model', { type: 'string', list: 'dl-models', help: `A fleet (fixer, reviewer…), a model id, or a wildcard. Omit for a bare launch. <button type="button" class="link" data-act="create-inline" data-create="fleet" data-picker="fleet" data-target="wf-${loc.replace(/\./g, '_')}-model">+ New fleet</button>` });
      { const ovS = packRo ? (B.ov.steps || {})[ovPath] || {} : {}; const rtName = ovS.runtime || s.runtime || (D.runtimes.find((r) => r.cfg && r.cfg.default) || D.runtimes[0] || {}).name, rt = S.view().runtimes.find((r) => r.name === rtName), paseo = rt && rt.use === 'paseo', iso = isObj(ovS.isolation) ? Object.assign({}, s.isolation, ovS.isolation) : isObj(s.isolation) ? s.isolation : null;
        if (paseo && !iso) rows.push(`<div class="field"><label>isolation</label><div class="hint">${icon('shield')} ${esc(rtName)} is a paseo runtime: its agents are children of the paseo daemon, which conductor can't wrap, so a step isolation block isn't available. The daemon's agent sandbox (Connectors → Isolation) still applies to paseo provider entries. Pick a cli, acp, opencode or agent-deck runtime to isolate this step.</div></div>`);
        else {
          rows.push('<div class="sc-sec">Isolation <span>how the agent is fenced in</span></div>');
          G('isolation.mode', { type: 'seg', label: 'mode', options: [['', 'none set'], ['user', 'user'], ['namespace', 'namespace'], ['container', 'container']], help: 'user: a low-privilege account · namespace: a mount/net namespace jail · container: an image. No block: the runtime\'s own isolation applies.' });
          if (iso && iso.mode === 'user') G('isolation.user', { type: 'string', label: 'user', req: true, ph: 'conductor-agent' });
          if (iso && iso.mode === 'container') { G('isolation.container.image', { type: 'string', label: 'container image', req: true, ph: 'ghcr.io/acme/agents:latest' }); G('isolation.container.engine', { type: 'enum', label: 'engine', options: ['docker', 'podman'] }); }
          if (iso && iso.mode) {
            G('isolation.network.egress', { type: 'list', label: 'egress allowlist', ph: 'api.github.com:443', help: 'Through conductor\'s egress proxy; an empty network block denies all. Adding a host widens.' });
            G('isolation.network.deny', { type: 'bool', label: 'no network', on: 'cut the network structurally (exclusive with egress)' });
            G('isolation.fs', { type: 'list', label: 'extra paths (fs allowlist)', ph: '/srv/media' });
            G('isolation.limits.memory', { type: 'string', label: 'memory limit', ph: '2g' }); G('isolation.limits.cpu', { type: 'string', label: 'cpu limit', ph: '200%' }); G('isolation.limits.pids', { type: 'number', label: 'pids limit' });
            if (iso.mode === 'namespace') G('isolation.privileged', { type: 'bool', label: 'privileged', on: 'see the daemon\'s own files (widening)' });
          }
        } }
      if (!packRo) {
        F('workspace', { type: 'enum', options: ['local', 'worktree'] });
        F('checkout', { type: 'enum', options: ['none', 'checkout-pr', 'branch-off'] });
        rows.push(grantsHTML(loc, s, ro));
        F('handoff', { type: 'enum', options: S.view().connectors.filter((c) => SCHEMA.connectors[c.use] && Object.values(SCHEMA.connectors[c.use].verbs).some((x) => x.ask)).map((c) => c.name), create: 'connector', createCtx: { uses: ['web', 'slack'] }, createLabel: 'New ask-capable connector', help: 'Present a review loop on an ask-capable connector.' });
        F('browser.credentials', { type: 'multi', label: 'browser credentials', options: S.view().access.credentials.map((x) => x.name), create: 'credential', createLabel: 'Add a login', help: 'Logins this step\'s browser may fill (values never reach the agent).' });
        F('skill.secrets_via', { type: 'enum', label: 'secrets via', options: [['none', 'none (the default)'], ['broker', 'broker: single-use grants, audited'], ['env', 'env: the agent\'s environment']], help: 'How this agent may obtain secrets. allow_secrets needs broker.' });
        F('skill.allow_secrets', { type: 'multi', label: 'secrets (broker grants)', options: S.view().secrets.map((x) => x.ref), create: 'secret', ret: 'ref', createLabel: 'New vault entry', help: 'Exact <vault>/<key> entries the agent may request through the broker: single use, 60s, audited. Needs secrets via: broker.' });
        F('gate.run', { type: 'multi', label: 'quality gate', options: SCHEMA.checks });
      }
    } else if (k === 'code') {
      const eng = s.use || s.run || 'cli';
      rows.push(`<div class="field"><label>Language</label><select class="select mono" ${ro ? 'disabled' : `data-wf-engine="${loc}"`} aria-label="Language">${SCHEMA.engines.map(([ek, el]) => `<option value="${ek}"${ek === eng ? ' selected' : ''}>${esc(el)}</option>`).join('')}</select><div class="hint">ctx carries the trigger's fields and every earlier step's outputs (ctx.logs.text). What you return or print becomes this step's outputs.</div></div>`);
      if (eng === 'cli' || s.command) F('command', { type: 'list', req: true, help: 'argv, one item per argument.' });
      else rows.push(fieldRow(loc, 'code', { type: 'code', lang: eng, req: true }, s.code, ro));
      F('env', { type: 'map', help: 'Pass a secret explicitly: {{ vault "house" "key" }}.' });
    } else if (k === 'decide') {
      F('model', { type: 'string', list: 'dl-models', help: 'The tier that answers.' });
      F('decide.state', { type: 'text', label: 'state', rows: 3, help: 'What the decision looks at.' });
      rows.push(questionsEditor(s, loc, ro));
    } else if (k === 'call') {
      const wfs = S.view().workflows.filter((w) => w.kind === 'workflow');
      rows.push(`<div class="field"><label>Workflow</label><select class="select mono" ${ro ? 'disabled' : `data-wf-call="${loc}" data-picker="workflow" data-ret="name"`} aria-label="Workflow to call">${ro ? '' : '<option value="__new__">+ New reusable workflow…</option>'}${wfs.map((w) => `<option${w.name === s.call ? ' selected' : ''}>${w.name}</option>`).join('')}</select></div>`);
      const w = wfs.find((x) => x.name === s.call); let ins = {};
      try { ins = (w && Y.parse(w.yaml).value.inputs) || {}; } catch (x) {}
      Object.entries(ins).forEach(([ik, d]) => F('with.' + ik, { type: d.type === 'integer' ? 'number' : 'string', req: !!d.required, def: d.default, label: ik + ' (input)' }));
    } else if (k === 'wait') {
      const mode = s.wait_for ? 'wait_for' : 'sleep';
      rows.push(`<div class="field"><label>Wait for</label>${UI.seg([['sleep', 'a duration'], ['wait_for', 'a condition']], mode, 'wf-waitmode', `data-loc="${loc}"`).replace(/data-act="wf-waitmode"/g, ro ? UI.off(ro) : `data-act="wf-waitmode" data-loc="${loc}"`)}</div>`);
      if (mode === 'sleep') F('sleep', { type: 'string', req: true, help: 'e.g. 30s, 5m.' });
      else { F('wait_for.uses', { type: 'enum', label: 'read verb', options: allVerbs(true) }); F('wait_for.until', { type: 'string', label: 'until', help: 'Over the verb\'s outputs: status == \'completed\'.' }); F('wait_for.every', { type: 'string', label: 'every' }); F('wait_for.timeout', { type: 'string', label: 'timeout' }); }
    } else if (k === 'bot') {
      const toTeam = isObj(s.bot) && !!s.bot.team;
      rows.push(`<div class="field"><label>Hand to</label>${UI.seg([['bot', 'a bot'], ['team', 'a team']], toTeam ? 'team' : 'bot', 'wf-bottarget').replace(/data-act="wf-bottarget"/g, ro ? UI.off(ro) : `data-act="wf-bottarget" data-loc="${loc}"`)}<div class="hint">${toTeam ? 'The team\'s routing rules pick the bot; with no match its lead triages. The requester is this trigger\'s event.' : 'Delivered to the bot directly, as conductor bot deliver.'}</div></div>`);
      if (toTeam) F('bot.team', { type: 'enum', label: 'team', req: true, options: S.view().teams.map((x) => x.name), create: 'team', createLabel: 'New team' });
      else F('bot.name', { type: 'enum', label: 'bot', req: true, options: S.view().bots.map((x) => x.name), create: 'bot', createLabel: 'New bot' });
      F('bot.title', { type: 'string', label: 'title', help: 'Becomes the case title.' });
      F('bot.text', { type: 'text', label: 'text', rows: 3, help: 'The payload. It reaches the bot fenced as untrusted data, never as instructions.' });
      F('bot.requester', { type: 'string', label: 'requester', help: 'Who asked; leave empty for an alert with no human behind it.' });
      F('bot.urgent', { type: 'bool', label: 'urgent', on: 'start a worker now, don\'t wait on the conversation' });
    } else if (k === 'log') F('log', { type: 'text', rows: 2 });
    else if (k === 'set') F('set', { type: 'map', label: 'values', help: 'Read later as {{.' + (s.id || 'this') + '.<key>}}.' });
    else if (k === 'assert') F('assert', { type: 'string', help: 'Same grammar as if:. The run fails unless it holds.' });
    else if (k === 'fail') F('fail', { type: 'text', rows: 2 });
    // advanced
    const adv = [];
    const A = (path, fd) => adv.push(fieldRow(loc, path, fd, S.getPath(s, path), packRo ? 'The pack sets these' : ro));
    A('id', { type: 'string', ref: false, help: 'How later steps address its outputs. Renaming updates nothing else: fix references the validator flags.' });
    A('if', { type: 'string', help: 'Skip unless it holds: {{.triage.kind}} == test_failure' });
    A('for_each', { type: 'string', help: 'Run once per element: {{.item}} and {{.index}} in scope.' });
    A('timeout', { type: 'string' });
    A('retry.max', { type: 'number', label: 'retry · max', ref: false });
    A('continue_on_error', { type: 'bool', on: 'record the error and keep going' });
    if (k === 'agent' && s.output_schema) { const props = Object.entries((s.output_schema && s.output_schema.properties) || {}); rows.push(`<div class="field"><label>output_schema <span class="opt">what the agent must answer with</span></label><div class="small">${props.length ? 'returns ' + props.map(([n, d]) => `<span class="mono">${esc(n)}</span> <span class="muted">(${esc((d && d.type) || 'any')})</span>`).join(', ') : '<span class="mono">' + esc(JSON.stringify(s.output_schema).slice(0, 80)) + '</span>'}</div><div class="hint">Edit it with the keys below or in the YAML pane.</div></div>`); }
    const extra = Object.keys(s || {}).filter((x) => !seen.has(x));
    const yamlOnly = (extra.length || (k === 'agent' && s.output_schema)) ? `<div class="yaml-only small">${UI.label(`+${extra.length + (k === 'agent' && s.output_schema ? 1 : 0)} key${extra.length + (k === 'agent' && s.output_schema ? 1 : 0) === 1 ? '' : 's'} only in YAML`)} <span class="mono muted">${extra.concat(k === 'agent' && s.output_schema ? ['output_schema'] : []).map(esc).join(', ')}</span>${ro ? '' : ` <button type="button" class="link" data-act="wf-yaml-sub" data-loc="${loc}">Edit them here</button>`}</div>` : '';
    return rows.join('') + yamlOnly + `<details class="adv"${B.advOpen && B.advOpen.has(loc) ? ' open' : ''} data-adv="${loc}"><summary>More options: id, if, for_each, timeout, retry</summary><div class="adv-grid">${adv.join('')}</div></details>`;
  }
  function questionsEditor(s, loc, ro) {
    const q = (s.decide && s.decide.questions) || {};
    const dis = ro ? UI.off(ro) : '';
    return `<div class="field"><label>Questions</label><div class="q-list">${Object.entries(q).map(([n, d]) => `<div class="q-row"><input class="input mono" value="${esc(n)}" data-q-name="${esc(n)}" data-loc="${loc}" aria-label="Question name" ${ro ? 'readonly' : ''}><select class="select mono" data-q-type="${esc(n)}" data-loc="${loc}" aria-label="Answer type" ${ro ? 'disabled' : ''}>${['noul', 'choice', 'score'].map((t) => `<option${t === ((d && d.type) || 'noul') ? ' selected' : ''}>${t}</option>`).join('')}</select><input class="input" value="${esc((d && d.instructions) || '')}" placeholder="instructions" data-q-ins="${esc(n)}" data-loc="${loc}" aria-label="Instructions" ${ro ? 'readonly' : ''}>${d && d.type === 'choice' ? `<input class="input mono q-ch" value="${esc((d.choices || []).join(', '))}" placeholder="choice, choice" data-q-ch="${esc(n)}" data-loc="${loc}" aria-label="Choices" ${ro ? 'readonly' : ''}>` : ''}<button type="button" class="icon-btn" ${ro ? dis : `data-act="wf-q-del" data-loc="${loc}" data-n="${esc(n)}"`} aria-label="Remove question">${icon('trash')}</button></div>`).join('')}</div><button type="button" class="btn sm" ${ro ? dis : `data-act="wf-q-add" data-loc="${loc}"`}>${icon('plus')}Add a question</button><div class="hint">noul = yes/no with a probability · choice = one of a list · score = 0..1. Answers are outputs by question name.</div></div>`;
  }
  /* skill.verbs: the list form (patterns) or the map form (per verb, the resources each call may name) */
  const RESOURCE_KEYS = ['channel', 'repo', 'store', 'namespace', 'project', 'service'];
  const resourcesOf = (pattern) => { const d = SCHEMA.verb(pattern); return d ? d.fields.map((f) => f.k).filter((k) => RESOURCE_KEYS.includes(k)) : []; };
  function grantsHTML(loc, s, ro) {
    const v = S.getPath(s, 'skill.verbs'), isMap = isObj(v), entries = isMap ? Object.entries(v) : (Array.isArray(v) ? v : v ? [v] : []).map((x) => [x, null]);
    const id = 'wf-' + loc.replace(/\./g, '_') + '-grants';
    return `<div class="field wf-field" data-field="skill.verbs"><label>skill verbs (grant) <span class="opt">${isMap ? 'map form: scoped per verb' : 'list form'}</span></label>
      <div class="grants" id="${id}">${entries.map(([pat, scope]) => { const res = resourcesOf(pat), sc = isObj(scope) ? scope : {}; return `<div class="grant"><div class="row"><span class="mono small grow">${esc(pat)}</span>${ro ? '' : `<button type="button" class="btn sm ghost" data-act="wf-grant-del" data-loc="${loc}" data-p="${esc(pat)}" aria-label="Remove grant ${esc(pat)}">${icon('trash')}</button>`}</div>
        ${isMap ? `<div class="grant-scopes">${Object.entries(sc).map(([k, xs]) => UI.label(`<span class="mono">${esc(k)}: ${esc([].concat(xs).join(', '))}</span>${ro ? '' : `<button type="button" class="chip-del" data-act="wf-scope-del" data-loc="${loc}" data-p="${esc(pat)}" data-k="${esc(k)}" aria-label="Remove ${esc(k)} scope">×</button>`}`, { cls: 'removable' })).join('') || '<span class="muted tiny">no listed resources: limited to the dispatch\'s own context (its repo, the channel its event came from)</span>'}
          ${ro || !res.length ? '' : `<span class="scope-add"><select class="select sm" data-scope-key="${esc(pat)}" aria-label="Resource for ${esc(pat)}">${res.map((k) => `<option>${k}</option>`).join('')}</select><input class="input mono sm" data-scope-val="${esc(pat)}" placeholder="#code-reviews, acme/api" aria-label="Allowed values for ${esc(pat)}"><button type="button" class="btn sm" data-act="wf-scope-add" data-loc="${loc}" data-p="${esc(pat)}">${icon('plus')}Scope</button></span>`}</div>` : ''}</div>`; }).join('') || '<div class="muted tiny">None: deny by default, the agent can call no verbs.</div>'}</div>
      ${ro ? '' : `<div class="row" style="gap:6px;margin-top:6px"><select class="select mono sm" id="${id}-new" aria-label="Add a verb grant"><option value="">+ grant a verb…</option>${allVerbs().concat([...new Set(D.connectors.map((c) => c.name + '.*'))]).filter((x) => !entries.some(([p]) => p === x)).map((x) => `<option>${esc(x)}</option>`).join('')}</select><button type="button" class="btn sm" data-act="wf-grant-add" data-loc="${loc}" data-sel="${id}-new">${icon('plus')}Grant</button>${isMap ? `<button type="button" class="btn sm ghost" data-act="wf-grant-form" data-loc="${loc}" data-v="list" ${Object.values(v).some((x) => isObj(x) && Object.keys(x).length) ? UI.off('Some grants list resources: remove their scopes first') : ''}>Use the list form</button>` : `<button type="button" class="btn sm ghost" data-act="wf-grant-form" data-loc="${loc}" data-v="map">Scope per verb (map form)</button>`}</div>`}
      <div class="hint">Deny by default: the verbs this agent may call back into conductor. In the map form, a resource you list widens what a call may name; one you don't list is limited to the dispatch's own context.</div></div>`;
  }
  const grantsOf = (s) => { const v = S.getPath(s, 'skill.verbs'); return isObj(v) ? Object.assign({}, v) : v; };
  UI.act('wf-grant-add', (b) => { const sel = UI.$('#' + b.dataset.sel); if (!sel || !sel.value) return UI.nudge(sel); const pat = sel.value; mutate((v) => { const s = WF.at(v, locOf(b.dataset.loc)); const cur = grantsOf(s); if (isObj(cur)) { cur[pat] = {}; S.setPath(s, 'skill.verbs', cur); } else S.setPath(s, 'skill.verbs', [].concat(cur || []).concat([pat])); }); });
  UI.act('wf-grant-del', (b) => mutate((v) => { const s = WF.at(v, locOf(b.dataset.loc)); const cur = grantsOf(s); if (isObj(cur)) { delete cur[b.dataset.p]; S.setPath(s, 'skill.verbs', Object.keys(cur).length ? cur : undefined); } else S.setPath(s, 'skill.verbs', [].concat(cur || []).filter((x) => x !== b.dataset.p)); }));
  UI.act('wf-grant-form', (b) => mutate((v) => { const s = WF.at(v, locOf(b.dataset.loc)); const cur = grantsOf(s); if (b.dataset.v === 'map') S.setPath(s, 'skill.verbs', Object.fromEntries([].concat(cur || []).map((p) => [p, {}]))); else S.setPath(s, 'skill.verbs', Object.keys(cur || {})); }));
  UI.act('wf-scope-add', (b) => { const p = b.dataset.p, key = UI.$(`[data-scope-key="${CSS.escape(p)}"]`).value, inp = UI.$(`[data-scope-val="${CSS.escape(p)}"]`); const vals = inp.value.split(',').map((x) => x.trim()).filter(Boolean); if (!vals.length) return UI.nudge(inp, 'one or more values'); mutate((v) => { const s = WF.at(v, locOf(b.dataset.loc)); const cur = grantsOf(s); const e = Object.assign({}, cur[p]); e[key] = [...new Set([].concat(e[key] || []).concat(vals))]; cur[p] = e; S.setPath(s, 'skill.verbs', cur); }); });
  UI.act('wf-scope-del', (b) => mutate((v) => { const s = WF.at(v, locOf(b.dataset.loc)); const cur = grantsOf(s); const e = Object.assign({}, cur[b.dataset.p]); delete e[b.dataset.k]; cur[b.dataset.p] = e; S.setPath(s, 'skill.verbs', cur); }));
  const allVerbs = (readOnlyOnly) => { const out = []; D.connectors.forEach((c) => { const d = SCHEMA.connectors[c.use]; if (d) Object.entries(d.verbs).forEach(([vn, vd]) => { if (!readOnlyOnly || vd.read) out.push(`${c.name}.${vn}`); }); }); out.push('git.push'); return out; };

  function resultPanel(e) {
    const r = e.step && e.step.id && B.tests[e.step.id];
    if (!r) return '';
    const tab = (B.rtab && B.rtab[e.step.id]) || 'output';
    if (r.status === 'error') return `<div class="results err"><div class="row">${pill(r)}<span class="mono tiny muted">${r.ms}ms · against ${esc((SCHEMA.events.find((x) => x.id === B.ev) || {}).label || 'no event')}</span></div><div class="callout danger small mono">${esc(r.error)}</div>${r.output ? `<div class="caps">partial output</div>${tree(r.output, e.step.id, 0)}` : ''}</div>`;
    if (r.status !== 'ok') return `<div class="results"><div class="row">${pill(r)}<span class="muted small">${esc(r.note || 'an earlier step failed, so this one did not run')}</span></div></div>`;
    return `<div class="results"><div class="row">${pill(r)}<span class="mono tiny muted">${esc(r.note || '')}</span><div class="seg sm" style="margin-left:auto">${['output', 'inputs'].map((t) => `<button type="button" data-act="wf-rtab" data-id="${e.step.id}" data-v="${t}" aria-pressed="${t === tab}" class="${t === tab ? 'on' : ''}">${t}</button>`).join('')}</div></div>${tree(tab === 'output' ? r.output : r.inputs, tab === 'output' ? e.step.id : '', 0)}</div>`;
  }
  function tree(o, base, depth, insertTarget) {
    if (!isObj(o) && !Array.isArray(o)) return `<div class="dtree"><div class="dleaf"><span class="dv mono">${esc(JSON.stringify(o))}</span></div></div>`;
    const ent = Array.isArray(o) ? o.map((v, i) => [String(i), v]) : Object.entries(o || {});
    if (!ent.length) return `<div class="muted tiny" style="padding:4px 0">${Array.isArray(o) ? '[ ]' : '{ } · no outputs'}</div>`;
    return `<div class="dtree">${ent.map(([k, v]) => { const p = base ? `${base}.${k}` : k; const kids = (isObj(v) || Array.isArray(v)) && depth < 3; const prev = typeof v === 'string' ? `"${v.length > 70 ? v.slice(0, 69) + '…' : v}"` : isObj(v) ? `{${Object.keys(v).length}}` : Array.isArray(v) ? `[${v.length}]` : String(v);
      const leaf = insertTarget ? `<button type="button" class="dleaf ins" data-act="wf-ins" data-path="${esc(p)}" data-target="${insertTarget}" title="Insert {{.${esc(p)}}}"><span class="dk mono">${esc(k)}</span><span class="dv mono">${esc(prev)}</span><span class="dins">${icon('plus')}</span></button>` : `<div class="dleaf"><span class="dk mono">${esc(k)}</span><span class="dv mono">${esc(prev)}</span></div>`;
      return kids ? `<div class="dnode">${leaf}<div class="dkids">${tree(v, p, depth + 1, insertTarget)}</div></div>` : leaf; }).join('')}</div>`;
  }

  function badges(id) { const all = issuesFor(id), iss = all.filter((i) => !i.todo), td = all.filter((i) => i.todo), er = iss.filter((i) => i.level === 'error').length, wr = iss.filter((i) => i.level === 'warn').length; if (!er && !wr && td.length) return UI.statusMark('idle', `to do ${td.length}`, { cls: 'tpill todo', tip: td.map((i) => i.todo).join(' · '), sm: true }); return er ? UI.statusMark('fail', `${er}`, { cls: 'tpill fail', tip: iss.find((i) => i.level === 'error').msg, sm: true }) : wr ? UI.statusMark('wait', `${wr}`, { cls: 'tpill warn', tip: iss[0].msg, sm: true }) : ''; }
  function stepCard(e, loc, num) {
    const s = e.step, k = e.kind, kc = KC(k), id = e.id, open = B.open.has(id), ro = readOnly();
    const iss = issuesFor(id), r = s && s.id && B.tests[s.id];
    const er = iss.filter((i) => i.level === 'error' && !i.todo).length, wr = iss.filter((i) => i.level === 'warn').length;
    const first = e.index === 0, last = e.index === WF.at(B.value, e.list).length - 1;
    if (k === 'parallel') return parallelCard(e, loc, num);
    const general = iss.filter((i) => !i.field || !open);
    return `<div class="step-card${open ? ' open' : ''}${B.sel === id ? ' sel' : ''}${er ? ' has-err' : ''}${B.owner === 'pack' ? ' pack-owned' : ''}" id="card-${esc(id)}" data-card="${esc(id)}" data-loc="${loc}" style="--kc:${kc.color}" ${ro ? '' : 'draggable="true"'}>
      <div class="sc-head" role="button" tabindex="0" data-act="wf-toggle" data-id="${esc(id)}" aria-expanded="${open}" aria-label="Step ${num}: ${esc(id)}">
        <span class="grip" ${ro ? 'aria-hidden="true"' : 'data-tip="Drag to reorder · Alt+↑/↓"'}>${icon('more')}</span>
        <span class="sc-num">${num}</span>
        <div class="grow sc-text"><div class="sc-title"><b>${esc(s && s.id ? s.id : '(no id)')}</b>${s && s.if ? UI.label(`if`) : ''}${s && s.for_each ? UI.label('for each') : ''}</div><div class="sc-sum" id="sum-${esc(id)}">${esc(WF.summary(s))}</div></div>
        <span class="sc-ty">${icon(kc.icon)}${kc.label}</span>
        <span class="sc-badges" id="badge-${esc(id)}">${badges(id)}</span>${pill(r)}
        <button type="button" class="icon-btn sc-more" data-act="wf-menu" data-id="${esc(id)}" aria-label="Step actions">${icon('more')}</button>
        <span class="chev">${icon(open ? 'chevd' : 'chev')}</span>
      </div>
      ${open ? `<div class="sc-body">
        ${general.filter((i) => !i.field).map((i) => i.todo ? `<div class="callout small todo">${esc(i.todo)}</div>` : `<div class="callout ${i.level === 'error' ? 'danger' : i.level === 'info' ? 'info' : 'warn'} small">${esc(i.msg)}</div>`).join('')}
        ${r && r.status === 'error' ? `<div class="callout danger small"><b>Test failed:</b> <span class="mono">${esc(r.error)}</span></div>` : ''}
        <div class="sc-form">${formFor(e, loc, ro && B.owner !== 'pack' ? ro : '')}</div>
        <div class="sc-test"><button class="btn sm" ${!s.id ? UI.off('Give this step an id to test it') : !B.ev ? UI.off('Pick a test event in the toolbar first') : B.err ? UI.off('Fix the YAML first') : `data-act="wf-test" data-id="${esc(s.id)}"`}>${icon('play')}Test${e.order ? ' up to here' : ''}</button><span class="muted tiny">runs earlier steps too, against the chosen event; writes are dry-runs</span></div>
        ${resultPanel(e)}
        <div class="sc-foot row"><button class="btn sm ghost" ${ro ? UI.off(ro) : first ? UI.off('Already first in its list') : `data-act="wf-move" data-id="${esc(id)}" data-d="-1"`}>${icon('chev', 'up')}Move up</button><button class="btn sm ghost" ${ro ? UI.off(ro) : last ? UI.off('Already last in its list') : `data-act="wf-move" data-id="${esc(id)}" data-d="1"`}>${icon('chev', 'down')}Move down</button><button class="btn sm ghost" ${ro ? UI.off(ro) : `data-act="wf-dup" data-id="${esc(id)}"`}>${icon('copy')}Duplicate</button><button class="btn sm ghost danger-text" ${ro ? UI.off(ro) : `data-act="wf-del-step" data-id="${esc(id)}"`}>${icon('trash')}Delete</button></div>
      </div>` : ''}
    </div>`;
  }
  function parallelCard(e, loc, num) {
    const s = e.step, ro = readOnly(), id = e.id, iss = issuesFor(id);
    const L = 'ABCDEFGH';
    return `<div class="par-card${B.sel === id ? ' sel' : ''}" id="card-${esc(id)}" data-card="${esc(id)}" data-loc="${loc}" ${ro ? '' : 'draggable="true"'}>
      <div class="par-head"><span class="grip">${icon('more')}</span><span class="sc-num">${num}</span><span class="sc-ic par">${icon('workflows')}</span><div class="grow"><b>Parallel</b> <span class="muted small">${s.parallel.length} branches · joined before the next step</span></div>${iss.map((i) => UI.statusMark((i.level === 'error' ? 'fail' : 'warn') === 'fail' ? 'fail' : 'wait', ``, { cls: 'tpill ' + (i.level === 'error' ? 'fail' : 'warn'), tip: i.msg, sm: true })).join('')}<button type="button" class="icon-btn" data-act="wf-menu" data-id="${esc(id)}" aria-label="Parallel group actions">${icon('more')}</button></div>
      <div class="par-fork" aria-hidden="true"></div>
      <div class="par-lanes" style="--n:${s.parallel.length + (ro ? 0 : 1)}">
        ${s.parallel.map((br, b) => { const bloc = `${loc}.parallel.${b}`; const kids = WF.walk(B.value).filter((x) => x.branch && x.branch.parent.id === id && x.branch.b === b); return `<div class="lane" data-lane="${bloc}"><div class="lane-head"><span class="caps">branch ${L[b] || b + 1}</span><button type="button" class="icon-btn sm" ${ro ? UI.off(ro) : s.parallel.length <= 2 ? UI.off('A parallel group keeps at least two branches; delete the group instead') : `data-act="wf-br-del" data-loc="${loc}" data-b="${b}"`} aria-label="Remove branch ${L[b]}">${icon('trash')}</button></div>
          ${addSlot(bloc, 0, `Add a step at the top of branch ${L[b]}`)}${kids.map((x, j) => stepCard(x, `${bloc}.${j}`, `${num}${L[b].toLowerCase()}${j + 1}`) + addSlot(bloc, j + 1, `Add a step in branch ${L[b]} after ${x.id}`)).join('')}</div>`; }).join('')}
        ${ro ? '' : `<div class="lane add-lane"><button type="button" class="btn sm" data-act="wf-br-add" data-loc="${loc}">${icon('plus')}Add a branch</button></div>`}
      </div>
      <div class="par-join" aria-hidden="true"><span class="caps">join</span></div>
    </div>`;
  }

  /** The list never diverges from the YAML: if rendering the parsed value fails, show the last good list, locked, under the error. */
  function listHTML() {
    try { const h = listInner(); B.lastGood = h; return h; }
    catch (e) { console.warn('builder render', e); return `<div class="callout danger small sticky-err">${icon('warn')} The list can't show this YAML (${esc(e.message)}). The YAML pane is canonical; the list below is the last version it could show, locked until the YAML is something it can draw.</div>${B.lastGood ? `<div class="wfb-locked" inert>${B.lastGood}</div>` : ''}`; }
  }
  function listInner() {
    const v = B.value || {};
    if (B.err && !B.value) return `<div class="callout danger">${esc(B.err.message)}</div>`;
    const steps = Array.isArray(v.steps) ? v.steps : [];
    const top = WF.walk(v).filter((e) => !e.branch);
    const ro = readOnly();
    return `${B.err ? `<div class="callout danger small sticky-err">${icon('warn')} YAML error at line ${B.err.line}: ${esc(B.err.msg || B.err.message)}. The list shows the last valid version.</div>` : ''}
      ${ro ? `<div class="callout ${B.owner === 'pack' ? 'info' : 'warn'} small">${B.owner === 'pack' ? `<b>Pack-owned.</b> Field edits become overrides in <span class="mono">packs.${B.pack}.steps</span>; the pack's file is never touched and an update keeps them. To add steps, call <span class="mono">${B.pack}/${esc((v.name || ''))}</span> from your own trigger.` : `<b>Synthesized.</b> This workflow comes from <a class="link" href="#/bots/${B.name.replace('bot:', '')}/settings">the bot definition</a>.`}</div>` : ''}
      <div class="staff">
        ${triggerCard()}
        ${addSlot('steps', 0, 'Add a step after the trigger')}
        ${top.map((e, i) => stepCard(e, `steps.${e.index}`, i + 1) + addSlot('steps', e.index + 1, `Add a step after ${e.id}`)).join('')}
        ${hooksLane(v, ro)}
        ${!steps.length ? `<div class="staff-end muted small">${ro ? '' : 'Add the first step with +'}</div>` : '<div class="staff-end caps">end</div>'}
      </div>
      <datalist id="dl-models">${SCHEMA.fleets.concat(SCHEMA.models).map((m) => `<option value="${m}">`).join('')}</datalist>`;
  }

  /* hooks: the workflow's own (start / done / fail / stop), each a verb call */
  function hooksLane(v, ro) { const hs = Array.isArray(v.hooks) ? v.hooks : [];
    return `<div class="hooks-lane" id="hooks-lane"><div class="row" style="gap:8px"><span class="caps">Hooks</span><span class="muted tiny">verb calls when the workflow starts, finishes, fails or is stopped</span></div>
      ${hs.map((h, i) => `<div class="hook-row">${UI.label(`on ${esc(h.at || '?')}`, { s: h.at === 'fail' ? 'fail' : h.at === 'done' ? 'ok' : 'idle' })}<span class="mono small">${esc(h.uses || '')}</span>${h.if ? UI.label(`if`, { tip: h.if }) : ''}<span class="muted tiny grow ellipsis">${esc(Object.entries(h.options || {}).map(([k2, x]) => `${k2}: ${typeof x === 'object' ? JSON.stringify(x) : x}`).join(' · '))}</span>${ro ? '' : `<button type="button" class="icon-btn" data-act="wf-hook-del" data-i="${i}" aria-label="Remove the ${esc(h.at)} hook">${icon('trash')}</button>`}</div>`).join('') || '<div class="muted tiny">none</div>'}
      ${ro ? '' : `<div class="row wrap" style="gap:6px">${['start', 'done', 'fail', 'stop'].map((at) => `<button type="button" class="btn sm ghost" data-act="wf-hook-add" data-at="${at}">${icon('plus')}on ${at}</button>`).join('')}</div>`}</div>`; }
  UI.act('wf-hook-add', (b) => { const at = b.dataset.at; openPicker((item) => mutate((v) => { const n = makeStep(v, item); if (!n.uses) return; v.hooks = Array.isArray(v.hooks) ? v.hooks : []; v.hooks.push(Object.assign({ at, uses: n.uses }, n.options ? { options: n.options } : {})); }), { items: pickerItems().filter((x) => x.key.startsWith('verb:')), title: `Which call on ${at}?`, caps: 'add a hook' }); });
  UI.act('wf-hook-del', (b) => mutate((v) => { v.hooks.splice(+b.dataset.i, 1); if (!v.hooks.length) delete v.hooks; }));
  /* the keys the form doesn't model, edited as YAML in place */
  UI.act('wf-yaml-sub', (b) => { const e = entryAt(b.dataset.loc), s = e.step, known = new Set(['id', 'type', 'uses', 'if', 'for_each', 'timeout', 'retry', 'continue_on_error', 'prompt', 'runtime', 'model', 'isolation', 'skill', 'options', 'hooks', 'decide', 'parallel', 'bot', 'call', 'with', 'use', 'command', 'run', 'code', 'sleep', 'wait_for', 'log', 'set', 'assert', 'fail']);
    const sub = Object.fromEntries(Object.entries(s).filter(([k2]) => !known.has(k2) || k2 === 'output_schema'));
    UI.form({ caps: `${s.id || 'step'} · keys only in YAML`, title: `More keys on ${esc(s.id || 'this step')}`, values: { y: YAML.stringify(sub) }, intro: 'Keys the form doesn\'t model yet, kept exactly as written. The validator checks them like any other.',
      fields: [{ k: 'y', type: 'code', rows: 10, label: 'YAML', validate: (t) => { try { const v = YAML.parse(t || '').value; return v == null || (typeof v === 'object' && !Array.isArray(v)) ? '' : 'A map of keys'; } catch (err) { return 'Not valid YAML: ' + err.message; } } }],
      submit: 'Apply to the step', submitIcon: 'check', onSubmit: (v) => { const nv = YAML.parse(v.y || '').value || {}; mutate((root) => { const st = WF.at(root, e.list)[e.index]; Object.keys(sub).forEach((k2) => delete st[k2]); Object.assign(st, nv); }); } }); });

  /* ---------- side: YAML + data ---------- */
  function hlLines() { const e = B.sel && entryById(B.sel); if (!e || !B.meta) return []; const m = B.meta[e.path]; if (!m) return []; const out = []; for (let n = m.line; n <= m.end; n++) out.push(n); return out; }
  function sideHTML() {
    const tabs = UI.seg([['yaml', `${icon('code')}YAML`], ['data', `${icon('grid')}Data`]], B.side, 'wf-side');
    let body;
    if (B.side === 'data') {
      const e = B.sel && entryById(B.sel);
      const groups = WF.scope(B.value || {}, e || null, B.tests, evPayload());
      body = `<div class="data-pane"><div class="small muted" style="margin-bottom:8px">Data <b>${e ? esc(e.id) : 'the first step'}</b> can read. ${B.ev ? 'Tested steps show their actual output.' : 'Pick a test event and Test a step to see real values.'}</div>
        ${groups.map((g) => `<div class="dgroup"><div class="dg-head">${icon(g.icon)}<b class="mono">${esc(g.label)}</b><span class="mono tiny muted ellipsis">${esc(g.sub)}</span>${g.tested ? UI.statusMark('ok', 'tested', { cls: 'tpill ok', sm: true }) : g.unknown ? UI.statusMark('idle', 'test to see', { cls: 'tpill idle', sm: true }) : UI.statusMark('idle', 'declared', { cls: 'tpill idle', sm: true })}</div>${tree(g.data, g.key, 0)}</div>`).join('')}</div>`;
    } else {
      const ro = B.owner === 'bot' || B.owner === 'pack';
      body = `${B.owner === 'pack' ? `<div class="seg ytabs" role="group">${[['source', 'Pack source (read-only)'], ['ov', 'Your overrides']].map(([v, l]) => `<button type="button" data-act="wf-ytab" data-v="${v}" aria-pressed="${(B.ytab || 'source') === v}" class="${(B.ytab || 'source') === v ? 'on' : ''}">${l}</button>`).join('')}</div>` : ''}
        <div class="yaml-wrap">${UI.editor({ id: 'wf-yaml', text: B.owner === 'pack' && B.ytab === 'ov' ? Y.stringify({ packs: { [B.pack]: B.ov } }) : B.text, lang: 'yaml', act: ro ? null : 'wf-yaml-input', minRows: 24, err: B.issues.filter((i) => i.level === 'error').map((i) => i.line), hl: hlLines(), readonly: ro, label: 'Workflow YAML' })}</div>`;
    }
    return `<div class="side-head">${tabs}<span class="muted tiny" style="margin-left:auto">${icon('code')} YAML is canonical · ${B.owner === 'config' ? 'config.yaml · ' + esc(S.cfg.workflows.path(B.name)) : B.owner === 'pack' ? '.conductor/packs/' + B.pack : 'synthesized'}</span></div>
      <div class="side-body">${body}</div><div class="side-foot" id="wf-issues">${issuesHTML()}</div>`;
  }
  function issuesHTML() {
    const td = B.issues.filter((i) => i.todo), e = B.issues.filter((i) => i.level === 'error' && !i.todo), w = B.issues.filter((i) => i.level === 'warn');
    if (!B.issues.length) return `${UI.statusMark('success', 'valid', { cls: 'val-pill', sm: true })}<span class="mono tiny muted">checked on a copy · ${status() === 'draft' ? 'unsaved edits' : 'no unsaved edits'}</span>`;
    return `<div class="issues">${td.length ? `<span class="val-pill todo">${td.length} to do</span>` : ''}${e.length ? `${UI.statusMark('fail', `${e.length} error${e.length > 1 ? 's' : ''}`, { cls: 'val-pill', sm: true })}` : ''}${w.length ? `${UI.statusMark('wait', `${w.length} warning${w.length > 1 ? 's' : ''}`, { cls: 'val-pill', sm: true })}` : ''}
      ${B.issues.slice(0, 6).map((i, n) => `<button type="button" class="issue ${i.todo ? 'todo' : i.level}" data-act="wf-issue" data-n="${n}">${i.todo ? `${icon('dot')}${esc(i.todo)}` : `<span class="mono tiny">L${i.line}</span>${esc(i.msg)}`}</button>`).join('')}</div>`;
  }

  /* the side of the recipe: Try it (a recent event; anything that would change something is a dry run), At a glance,
     and the config file, read-only, one click from its own tab */
  function trySide() {
    const v = B.value || {}, srcs = WF.sources(v), evs = SCHEMA.events.filter((x) => srcs.some((so) => so.on === x.on) || x.on === 'manual');
    const runs = D.runs.filter((r) => r.workflow === B.name), ok = runs.filter((r) => r.status === 'ok').length, fail = runs.filter((r) => r.status === 'fail' || r.status === 'stopped').length, parked = runs.filter((r) => r.status === 'idle').length;
    const budget = S.getPath(v, 'policy.budget.max_cost_usd');
    return `<section class="dl-card"><header class="dl-ch"><h2>Try it</h2></header><p class="dl-sub" style="margin:0 0 12px">Run every step against a recent event. Anything that would change something is a dry run.</p>
        <label class="dl-search dl-select">${icon('pr')}<select data-on-change="wf-ev" aria-label="Test event"><option value="">Choose an event…</option>${evs.map((x) => `<option value="${x.id}"${x.id === B.ev ? ' selected' : ''}>${esc(x.label)}${x.at ? ` · ${x.at < 60 ? x.at + ' min' : Math.round(x.at / 60) + ' h'} ago` : ''}</option>`).join('')}</select></label>
        <button class="btn secondary wide" data-act="wf-test-all" ${B.ev ? '' : UI.off('Choose an event first')}>${icon('play')}Run a test</button></section>
      <section class="dl-card"><header class="dl-ch"><h2>At a glance</h2></header>${UI.facts([budget != null ? ['Budget', `Up to $${Number(budget).toFixed(2)} per run`] : null, ['Recent runs', runs.length ? `${runs.length} · ${ok} fine${fail ? ` · ${fail} failed` : ''}${parked ? ` · ${parked} parked` : ''}` : 'none yet'], ['Owner', B.owner === 'pack' ? `the pack ${esc(B.pack)}` : B.owner === 'bot' ? 'conductor (for the bot)' : 'your config']])}</section>
      <section class="dl-card"><header class="dl-ch"><h2>Config file</h2><span class="dl-sp"></span><a class="dl-link" href="#" data-act="wf-tab" data-v="config">Open</a></header><pre class="dl-code small">${String(B.text || '').split('\n').slice(0, 9).map((l, i) => `<span class="ln"><i>${i + 1}</i>${esc(l)}</span>`).join('')}</pre></section>`;
  }
  function tabBody() {
    const t = B.tab || 'recipe';
    if (t === 'config') return `<section class="dl-card wfb-config"><aside class="wfb-side" id="wfb-side">${sideHTML()}</aside></section>`;
    if (t === 'runs') { const rs = D.runs.filter((r) => r.workflow === B.name).slice(0, 12);
      return `<section class="dl-card">${rs.length ? `<ul class="dl-list">${rs.map((r) => `<li><a class="dl-row-a" href="#/runs/${r.id}"><span class="dl-ng">${UI.mark(UI.runS(r.status), { lg: true, label: UI.runWord(r.status) })}</span><span class="dl-nt"><b>${esc(UI.runTitle(r))} <span class="tgt">${UI.runTarget(r)}</span></b><span class="dl-why">${UI.runWord(r.status)} · <span class="mono">${r.id}</span></span></span><span class="dl-age">${UI.agoText(r.started)}</span></a></li>`).join('')}</ul>` : '<p class="dl-sub">No runs yet.</p>'}
        <p class="dl-foot-note"><a class="dl-link" href="#/activity?wf=${encodeURIComponent(B.name)}&since=">Every run of ${esc(B.name)} in Activity →</a></p></section>`; }
    if (t === 'settings') return `<section class="dl-card"><header class="dl-ch"><h2>Settings</h2></header><div id="wfb-list" class="wfb-settings">${triggerCard()}</div><p class="dl-sub">Budgets, isolation and the rest of the workflow’s keys are in the <a class="dl-link" href="#" data-act="wf-tab" data-v="config">config file</a>.</p></section>`;
    return `<div class="dl-cols"><div class="dl-col-main"><section class="dl-card dl-recipe"><div class="wfb-list" id="wfb-list">${listHTML()}</div></section></div><div class="dl-col-side">${trySide()}</div></div>`;
  }
  function page() {
    return `<div class="page dl-page wfb-page"><div class="wfb pane-steps" id="wfb">
      <div class="wfb-bar" id="wfb-bar">${bar()}</div>
      <div class="wfb-tab" id="wfb-tab">${tabBody()}</div>
    </div></div>`;
  }
  function drawBar() { const el = UI.$('#wfb-bar'); if (el) el.innerHTML = bar(); }
  function drawList() { flushPending(); const el = UI.$('#wfb-list'); if (el) { const y = el.scrollTop; el.innerHTML = listHTML(); el.scrollTop = y; } }
  function drawSide() { const el = UI.$('#wfb-side'); if (el) el.innerHTML = sideHTML(); }
  function drawIssues() { const el = UI.$('#wf-issues'); if (el) el.innerHTML = issuesHTML(); const ed = UI.$('#wf-yaml'); if (ed && B.side === 'yaml') UI.editorMark(ed, { err: B.issues.filter((i) => i.level === 'error').map((i) => i.line), hl: hlLines() }); }
  function redraw() { drawBar(); drawList(); drawSide(); }
  /** After typing in a field: refresh YAML, issues, summary and badges, keep focus. */
  function softUpdate() {
    drawBar();
    const ed = UI.$('#wf-yaml'); if (ed && B.side === 'yaml' && B.owner === 'config') UI.editorSet(ed, B.text, { err: B.issues.filter((i) => i.level === 'error').map((i) => i.line), hl: hlLines() });
    else if (B.side === 'yaml' && B.owner === 'pack') drawSide();
    drawIssues();
    WF.walk(B.value || {}).forEach((e) => { const el = document.getElementById('sum-' + e.id); if (el) el.textContent = WF.summary(e.step); const card = document.getElementById('card-' + e.id); if (card) card.classList.toggle('has-err', issuesFor(e.id).some((i) => i.level === 'error')); const bd = document.getElementById('badge-' + e.id); if (bd) bd.innerHTML = badges(e.id); });
    UI.$$('.wf-field').forEach((f) => { const inp = f.querySelector('[data-wf-loc]'); if (!inp) return; const loc = inp.dataset.wfLoc, path = inp.dataset.wfPath; const e = loc ? entryAt(loc) : null; const isu = loc ? B.issues.filter((i) => e && i.step === e.id && i.field && (i.field === path || i.field.startsWith(path + '.'))) : B.issues.filter((i) => i.trigger && i.field === path); f.classList.toggle('bad', isu.some((i) => !i.todo)); f.classList.toggle('todo', isu.length > 0 && isu.every((i) => i.todo)); f.querySelectorAll('.ferr, .ftodo').forEach((x) => x.remove()); isu.forEach((i) => { const d = document.createElement('div'); d.className = i.todo ? 'ftodo' : 'ferr'; d.textContent = i.todo || i.msg; f.appendChild(d); }); });
  }

  UI.route('workflows/:name', ({ name }) => {
    if (!B || B.name !== name || (B.owner === 'config' && S.cfg.workflows.get(name) !== B.live && (D.drafts || {})[name] == null && !S.find('workflows', name))) load(name);
    if (B.owner === 'missing') return `<div class="page"><div class="empty"><span class="serif">No workflow named ${esc(name)}</span><a class="btn" href="#/workflows">All workflows</a></div></div>`;
    return { toString: page, after() {
      const onKey = (e) => {
        if (!location.hash.startsWith('#/workflows/')) return;
        const inField = e.target.closest('input, select, textarea:not(.ced-ta)') || (e.target.classList.contains('ced-ta') && !e.target.closest('#wf-yaml'));
        if ((e.ctrlKey || e.metaKey) && e.key.toLowerCase() === 'z' && !inField) { e.preventDefault(); e.shiftKey ? redo() : undo(); }
        else if ((e.ctrlKey || e.metaKey) && e.key.toLowerCase() === 'y' && !inField) { e.preventDefault(); redo(); }
        else if ((e.ctrlKey || e.metaKey) && e.key.toLowerCase() === 's') { e.preventDefault(); UI.acts['wf-save'](); }
        const head = e.target.closest('.sc-head');
        if (head && !inField) {
          const card = head.closest('[data-card]'); const id = card && card.dataset.card;
          if (e.altKey && (e.key === 'ArrowUp' || e.key === 'ArrowDown')) { e.preventDefault(); moveStep(id, e.key === 'ArrowUp' ? -1 : 1, true); }
          else if (!e.altKey && (e.key === 'ArrowUp' || e.key === 'ArrowDown')) { e.preventDefault(); const hs = UI.$$('.wfb-list .sc-head'); const i = hs.indexOf(head); const n = hs[i + (e.key === 'ArrowUp' ? -1 : 1)]; if (n) n.focus(); }
          else if ((e.key === 'Delete' || e.key === 'Backspace') && id && id !== '$trigger' && !readOnly()) { e.preventDefault(); deleteStep(id); }
          else if ((e.ctrlKey || e.metaKey) && e.key.toLowerCase() === 'd' && id && id !== '$trigger' && !readOnly()) { e.preventDefault(); dupStep(id); }
        }
      };
      document.addEventListener('keydown', onKey); UI.onLeave(() => document.removeEventListener('keydown', onKey));
    } };
  }, { nav: 'workflows' });

  /* ================= operations ================= */
  function undo() { if (B.hi <= 0) return; B.hi--; B.text = B.hist[B.hi]; parse(); saveDraft(); redraw(); UI.toast('Undone', '', 'idle'); }
  function redo() { if (B.hi >= B.hist.length - 1) return; B.hi++; B.text = B.hist[B.hi]; parse(); saveDraft(); redraw(); }
  UI.act('wf-undo', undo); UI.act('wf-redo', redo);
  document.addEventListener('toggle', (e) => { const d = e.target; if (!B || !d.matches || !d.matches('details[data-adv]')) return; B.advOpen = B.advOpen || new Set(); if (d.open) B.advOpen.add(d.dataset.adv); else B.advOpen.delete(d.dataset.adv); }, true);
  UI.act('wf-toggle', (el) => { const id = el.dataset.id; if (B.open.has(id)) B.open.delete(id); else B.open.add(id); B.sel = id === '$trigger' ? B.sel : id; drawList(); if (B.side === 'yaml') drawIssues(); else drawSide(); const h = UI.$(`#card-${CSS.escape(id)} .sc-head`); if (h) h.focus({ preventScroll: true }); });
  UI.act('wf-expand', (b) => { const all = WF.walk(B.value || {}).filter((e) => e.kind !== 'parallel').map((e) => e.id); if (b.dataset.v === '1') { all.forEach((x) => B.open.add(x)); B.open.add('$trigger'); } else B.open.clear(); drawList(); });
  UI.act('wf-jump', (b) => { B.sel = b.dataset.id; B.open.add(b.dataset.id); drawList(); drawSide(); const c = document.getElementById('card-' + b.dataset.id); if (c) c.scrollIntoView({ block: 'center', behavior: 'smooth' }); });
  UI.act('wf-side', (b) => { B.side = b.dataset.v; drawSide(); });
  UI.act('wf-tab', (b) => { flushPending(); B.tab = b.dataset.v; if (B.tab === 'config') B.side = B.side || 'yaml'; const el = UI.$('#wfb-tab'); if (el) el.innerHTML = tabBody(); drawBar(); });
  UI.act('wf-pane', (b) => { B.pane = b.dataset.v; if (b.dataset.v !== 'steps') B.side = b.dataset.v; UI.$('#wfb').className = `wfb pane-${B.pane}`; UI.$$('[data-act="wf-pane"]').forEach((x) => { x.classList.toggle('on', x === b); x.setAttribute('aria-pressed', x === b); }); drawSide(); });
  UI.act('wf-ytab', (b) => { B.ytab = b.dataset.v; drawSide(); });
  UI.act('wf-rtab', (b) => { B.rtab = B.rtab || {}; B.rtab[b.dataset.id] = b.dataset.v; drawList(); });
  UI.act('wf-ev', (sel) => { if (sel.value !== '__paste__') { B.ev = sel.value; B.tests = {}; redraw(); return; }
    const on = (WF.sources(B.value || {})[0] || {}).on || 'manual', ev = SCHEMA.event(on), sample = ev && ev.fields ? JSON.stringify(ev.fields, null, 2) : '{\n  \n}';
    sel.value = B.ev || '';
    UI.form({ caps: 'test event', title: `Paste a ${on} payload`, intro: 'Test runs the steps against it (writes are dry runs). It stays in this browser session; nothing is sent.', values: { json: sample },
      fields: [{ k: 'json', type: 'code', rows: 12, req: true, label: 'Payload (JSON)', validate: (t) => { try { const v = JSON.parse(t); return v && typeof v === 'object' ? '' : 'An object'; } catch (e) { return 'Not valid JSON: ' + e.message; } } }],
      submit: 'Use this payload', submitIcon: 'play',
      onSubmit: (v) => { const id = 'paste-' + Date.now().toString(36); SCHEMA.events.push({ id, on, at: 0, label: `pasted ${on} payload`, payload: JSON.parse(v.json) }); B.ev = id; B.tests = {}; redraw(); UI.toast('Test event set', 'Test now runs against the pasted payload', 'ok'); } }); });
  UI.act('wf-issue', (b) => { const i = B.issues[+b.dataset.n]; if (!i) return; if (i.step) { B.sel = i.step; B.open.add(i.step); drawList(); } else if (i.trigger) { B.open.add('$trigger'); drawList(); } if (B.side !== 'yaml') { B.side = 'yaml'; drawSide(); } const ed = UI.$('#wf-yaml'); if (ed) { UI.editorMark(ed, { err: [i.line], hl: [i.line] }); UI.editorScrollTo(ed, i.line); } const c = i.step && document.getElementById('card-' + i.step); if (c) c.scrollIntoView({ block: 'center' }); });

  /* add a step: the picker */
  function pickerItems(inBranch) {
    const items = [];
    SCHEMA.kinds.forEach((k) => items.push({ key: 'kind:' + k.k, group: 'Built-in', icon: k.icon, label: k.label, sub: k.desc, dis: inBranch && k.k === 'parallel' ? 'A parallel group can\'t nest inside a branch' : '' }));
    items.push({ key: 'tpl:command', group: 'Built-in', icon: 'code', label: 'Run a command', sub: 'use: cli with command: [ … ], in the sandbox', risk: 'change' },
      { key: 'tpl:wait_for', group: 'Built-in', icon: 'clock', label: 'Wait for a condition', sub: 'wait_for: check a read verb until a condition holds' },
      { key: 'tpl:team', group: 'Built-in', icon: 'members', label: 'Hand to a team', sub: 'bot: { team }: the team\'s rules or its lead pick the bot' },
      { key: 'tpl:fail', group: 'Built-in', icon: 'x', label: 'Fail the run', sub: 'fail: stop here with a message (pair it with if:)' });
    const RISKY = /apply|merge|delete|resolve|push|deploy|destroy|close|revert|rollback/;
    const seen = new Set();
    S.view().connectors.forEach((c) => { const d = SCHEMA.connectors[c.use]; if (!d) return; seen.add(c.use); Object.entries(d.verbs).forEach(([vn, vd]) => items.push({ key: `verb:${c.name}.${vn}`, group: `${d.label} · ${c.name}`, icon: d.icon, label: vd.label, sub: `${c.name}.${vn}`, risk: vd.read ? 'read' : RISKY.test(vn) ? 'high' : 'change' })); });
    Object.entries(SCHEMA.connectors).forEach(([use, d]) => { if (seen.has(use) || d.registry) return; if (use === 'git') Object.entries(d.verbs).forEach(([vn, vd]) => items.push({ key: `verb:git.${vn}`, group: 'git · builtin', icon: d.icon, label: vd.label, sub: `git.${vn}`, risk: vd.read ? 'read' : RISKY.test(vn) ? 'high' : 'change' })); });
    Object.entries(SCHEMA.connectors).forEach(([use, d]) => { if (!d.registry) return; Object.entries(d.verbs).forEach(([vn, vd]) => items.push({ key: `verb:${use}.${vn}`, group: `${d.label} · not installed`, icon: d.icon, label: vd.label, sub: `${use}.${vn} · needs ${d.registry}`, notInstalled: true })); });
    const R = { read: 0, change: 1, high: 2 };
    return items.map((x, i) => [x, i]).sort((a, b) => (a[0].group === b[0].group ? (R[a[0].risk] || 0) - (R[b[0].risk] || 0) || a[1] - b[1] : a[1] - b[1])).map(([x]) => x);
  }
  const RISK_TAG = { read: ['reads only', 'ok'], change: ['changes things', 'wait'], high: ['high impact', 'fail'] };
  function openPicker(onPick, opts = {}) {
    const items = opts.items || pickerItems(opts.inBranch);
    const recent = ((D.recentSteps || []).map((k) => items.find((x) => x.key === k)).filter(Boolean)).slice(0, 5);
    const s = UI.modal(`<div class="modal-head"><div><div class="caps">${opts.caps || 'add a step'}</div><h2>${opts.title || 'What should happen?'}</h2></div><button class="icon-btn" data-close aria-label="Close">${icon('x')}</button></div>
      <div class="modal-body picker" data-picker="connector"><div class="row" style="gap:8px;margin-bottom:12px"><div class="input-icon grow" style="margin:0">${icon('search')}<input class="input" id="pk-q" placeholder="${opts.ph || 'Search: comment, slack, agent, code, approval…'}" autocomplete="off" aria-label="Search steps"></div><button type="button" class="btn sm" data-act="create-inline" data-create="connector">${icon('plus')}New connector</button></div><div id="pk-list" class="pk-list" role="listbox"></div></div>`, { wide: true });
    UI.onCreated = (kind) => { if (kind === 'connector') { const cb = onPick; openPicker(cb, Object.assign({}, opts, { items: opts.items ? opts.rebuild && opts.rebuild() : undefined })); UI.toast('Connector staged', 'its verbs and events are in the list now; it applies with this workflow', 'ok'); } };
    const draw = (q) => {
      q = (q || '').toLowerCase().trim();
      const match = items.filter((x) => !q || (x.label + ' ' + x.sub + ' ' + x.group + ' ' + x.key).toLowerCase().includes(q));
      const groups = {}; match.forEach((x) => (groups[x.group] = groups[x.group] || []).push(x));
      const sect = (title, list) => `<div class="pk-group"><div class="caps">${esc(title)}</div><div class="pk-grid">${list.map((x) => `<button type="button" class="pk-item${x.notInstalled ? ' ni' : ''}" role="option" ${x.dis ? UI.off(x.dis) : `data-act="pk-pick" data-key="${esc(x.key)}"`}><span class="sc-ic sm">${icon(x.icon)}</span><span class="grow"><b>${esc(x.label)}</b><span class="mono tiny muted">${esc(x.sub)}</span></span></button>`).join('')}</div></div>`;
      UI.$('#pk-list', s).innerHTML = (!q && recent.length ? sect('Recently used', recent) : '') + (Object.keys(groups).length ? Object.entries(groups).map(([g, l]) => sect(g, l)).join('') : `<div class="empty small">Nothing matches “${esc(q)}”.</div>`);
    };
    draw('');
    const q = UI.$('#pk-q', s); q.addEventListener('input', () => draw(q.value));
    q.addEventListener('keydown', (e) => { if (e.key === 'Enter') { const f = UI.$('.pk-item[data-act]', s); if (f) f.click(); } });
    setTimeout(() => { if (!s.contains(document.activeElement) || document.activeElement === document.body) q.focus(); }, 30);
    B.onPick = (key) => { UI.closeModal(); D.recentSteps = [key].concat((D.recentSteps || []).filter((k) => k !== key)).slice(0, 8); S.save(); onPick(items.find((x) => x.key === key)); };
  }
  UI.act('pk-pick', (b) => B.onPick && B.onPick(b.dataset.key));
  function makeStep(v, item) {
    const [t, k] = item.key.split(/:(.*)/s);
    if (t === 'kind') { const kd = SCHEMA.kinds.find((x) => x.k === k); const base = { agent: 'agent', code: 'code', decide: 'decide', ask: 'approval', wait: 'wait', parallel: 'parallel', call: 'call', log: 'log', set: 'vars', assert: 'check', bot: 'handoff' }[k]; const id = uniqueId(v, base); const node = kd.make(id); if (k === 'parallel') { node.parallel[0][0].id = uniqueId(v, id + '-a'); node.parallel[1][0].id = uniqueId(Object.assign({}, v, { steps: (v.steps || []).concat([node]) }), id + '-b'); } if (k === 'call') { const w = D.workflows.find((x) => x.kind === 'workflow'); node.call = w ? w.name : 'assess-and-post'; } if (k === 'ask') { const c = D.connectors.find((x) => x.use === 'web') || D.connectors.find((x) => x.use === 'slack'); node.uses = (c ? c.name : 'handoff') + '.ask'; } return node; }
    if (t === 'tpl') { const team = (S.view().teams[0] || {}).name || 'platform', read = Object.entries(SCHEMA.connectors).flatMap(([u, d]) => { const c = S.view().connectors.find((x) => x.use === u); return c ? Object.entries(d.verbs).filter(([, vd]) => vd.read).map(([vn]) => `${c.name}.${vn}`) : []; })[0] || 'gh.get_pr';
      return { command: { id: uniqueId(v, 'run'), use: 'cli', command: ['make', 'test'] }, wait_for: { id: uniqueId(v, 'wait'), wait_for: { uses: read, until: '' } }, team: { id: uniqueId(v, 'route'), bot: { team, title: '{{.title}}' } }, fail: { id: uniqueId(v, 'stop'), fail: 'Explain why the run stops here' } }[k]; }
    const uses = k, verb = uses.split('.')[1];
    const decl = SCHEMA.verb(uses);
    const node = { id: uniqueId(v, verb), uses };
    if (decl) { const req = decl.fields.filter((f) => f.req && f.def == null); if (req.length) node.options = Object.fromEntries(req.map((f) => [f.k, f.type === 'list' ? [] : f.type === 'enum' ? f.options[0] : ''])); }
    return node;
  }
  UI.act('wf-add', (b) => {
    const loc = locOf(b.dataset.loc), i = +b.dataset.i;
    openPicker((item) => {
      let newId;
      mutate((v) => { const list = WF.at(v, loc); if (!Array.isArray(list)) { if (loc.length === 1) v.steps = []; } const L = WF.at(v, loc) || (v.steps = []); const node = makeStep(v, item); newId = node.id || null; L.splice(i, 0, node); });
      if (newId) { B.open.add(newId); B.sel = newId; drawList(); const c = document.getElementById('card-' + newId); if (c) { c.scrollIntoView({ block: 'center' }); c.classList.add('just-added'); } }
      if (item.notInstalled) UI.toast(`${esc(item.sub.split(' · ')[0])} needs a plugin`, 'add the connector in Connectors & hosts; the validator flags it until then', 'wait');
    }, { inBranch: loc.length > 1 });
  });
  UI.act('wf-br-add', (b) => { const loc = locOf(b.dataset.loc); let nid; mutate((v) => { const g = WF.at(v, loc); nid = uniqueId(v, 'branch-' + 'abcdefgh'[g.parallel.length]); g.parallel.push([{ id: nid, log: 'new branch' }]); }); if (nid) { B.open.add(nid); drawList(); } });
  UI.act('wf-br-del', (b) => { const loc = locOf(b.dataset.loc); mutate((v) => { WF.at(v, loc).parallel.splice(+b.dataset.b, 1); }); });
  function findLoc(v, id) { const e = WF.walk(v).find((x) => x.id === id); return e ? { list: WF.at(v, e.list), index: e.index, e } : null; }
  function deleteStep(id) {
    let gone; mutate((v) => { const f = findLoc(v, id); if (!f) return false; gone = f.list.splice(f.index, 1)[0];
      // a branch left empty disappears; a group left with one branch keeps it (the validator warns)
      WF.walk(v).filter((x) => x.kind === 'parallel').forEach((p) => { p.step.parallel = p.step.parallel.filter((br) => br.length); }); });
    if (gone) { B.open.delete(id); UI.toast(`Deleted ${esc(id)}`, 'Ctrl+Z to undo', 'idle'); }
  }
  function dupStep(id) { let nid; mutate((v) => { const f = findLoc(v, id); if (!f) return false; const c = Y.clone(f.list[f.index]); const re = (n) => { if (isObj(n) && n.id) n.id = uniqueId(v, n.id + '-copy'); if (n.parallel) n.parallel.forEach((br) => br.forEach(re)); }; re(c); nid = c.id; f.list.splice(f.index + 1, 0, c); }); if (nid) { B.open.add(nid); B.sel = nid; drawList(); } }
  function moveStep(id, d, keepFocus) {
    const ok = mutate((v) => { const f = findLoc(v, id); if (!f) return false; const j = f.index + d; if (j < 0 || j >= f.list.length) return false; const [x] = f.list.splice(f.index, 1); f.list.splice(j, 0, x); });
    if (ok && keepFocus) { const h = UI.$(`#card-${CSS.escape(id)} .sc-head`); if (h) h.focus(); }
  }
  UI.act('wf-del-step', (b) => deleteStep(b.dataset.id));
  UI.act('wf-dup', (b) => dupStep(b.dataset.id));
  UI.act('wf-move', (b) => moveStep(b.dataset.id, +b.dataset.d));
  UI.act('wf-menu', (b) => {
    const id = b.dataset.id, ro = readOnly(), f = findLoc(B.value, id);
    const first = f && f.index === 0, last = f && f.index === f.list.length - 1;
    const off = (r) => UI.off(r);
    UI.menu(b, [
      ['Insert a step above', 'plus', 'wf-ins-rel', ro ? off(ro) : `data-id="${esc(id)}" data-d="0"`],
      ['Insert a step below', 'plus', 'wf-ins-rel', ro ? off(ro) : `data-id="${esc(id)}" data-d="1"`],
      ['-'],
      ['Move up', 'chev', 'wf-move', ro ? off(ro) : first ? off('Already first') : `data-id="${esc(id)}" data-d="-1"`],
      ['Move down', 'chev', 'wf-move', ro ? off(ro) : last ? off('Already last') : `data-id="${esc(id)}" data-d="1"`],
      ['Duplicate', 'copy', 'wf-dup', ro ? off(ro) : `data-id="${esc(id)}"`],
      ['Show in YAML', 'code', 'wf-show-yaml', `data-id="${esc(id)}"`],
      ['-'],
      ['Delete', 'trash', 'wf-del-step', ro ? off(ro) : `data-id="${esc(id)}"`],
    ]);
  });
  UI.act('wf-ins-rel', (b) => { const f = findLoc(B.value, b.dataset.id); if (!f) return; const fake = document.createElement('button'); fake.dataset.loc = f.e.list.join('.'); fake.dataset.i = f.index + +b.dataset.d; UI.acts['wf-add'](fake); });
  UI.act('wf-show-yaml', (b) => { B.sel = b.dataset.id; B.side = 'yaml'; B.tab = 'config'; const tb = UI.$('#wfb-tab'); if (tb) tb.innerHTML = tabBody(); drawBar(); const l = hlLines()[0]; const ed = UI.$('#wf-yaml'); if (ed && l) UI.editorScrollTo(ed, l); });

  /* drag to reorder (mouse); touch uses the menu / Move buttons */
  let dragId = null;
  document.addEventListener('dragstart', (e) => { const c = e.target.closest && e.target.closest('.wfb [data-card]'); if (!c || !B) return; dragId = c.dataset.card; e.dataTransfer.effectAllowed = 'move'; e.dataTransfer.setData('text/plain', dragId); c.classList.add('dragging'); });
  document.addEventListener('dragend', () => { dragId = null; UI.$$('.dragging, .drop-before, .drop-after').forEach((x) => x.classList.remove('dragging', 'drop-before', 'drop-after')); });
  document.addEventListener('dragover', (e) => { if (!dragId) return; const c = e.target.closest('.wfb [data-card]'); UI.$$('.drop-before, .drop-after').forEach((x) => x.classList.remove('drop-before', 'drop-after')); if (!c || c.dataset.card === dragId) return; e.preventDefault(); const r = c.getBoundingClientRect(); c.classList.add(e.clientY < r.top + r.height / 2 ? 'drop-before' : 'drop-after'); });
  document.addEventListener('drop', (e) => {
    if (!dragId) return; const c = e.target.closest('.wfb [data-card]'); if (!c) return; e.preventDefault();
    const after = c.classList.contains('drop-after'), target = c.dataset.card, src = dragId; dragId = null;
    mutate((v) => { const a = findLoc(v, src); if (!a) return false; const node = a.list[a.index]; const tb = findLoc(v, target); if (!tb) return false; if (node.parallel && tb.e.branch) { UI.toast('A parallel group can\'t go inside a branch', '', 'wait'); return false; } a.list.splice(a.index, 1); const t2 = findLoc(v, target); t2.list.splice(t2.index + (after ? 1 : 0), 0, node); WF.walk(v).filter((x) => x.kind === 'parallel').forEach((p) => { p.step.parallel = p.step.parallel.filter((br) => br.length); }); });
  });

  /* field edits → node-level YAML edits */
  function coerce(type, raw) {
    if (type === 'number') return raw === '' ? undefined : /^-?\d+(\.\d+)?$/.test(raw) ? Number(raw) : raw;
    if (type === 'bool') return raw ? true : undefined;
    if (type === 'code') return raw === '' ? undefined : raw.endsWith('\n') ? raw : raw + '\n';
    return raw === '' ? undefined : raw;
  }
  function setField(loc, path, val, opts) {
    if (B.owner === 'pack') return setOverride(loc, path, val);
    if (!loc) return mutate((v) => { S.setPath(v, path, val); if (val === undefined) prune(v, path); }, opts);
    if (path === 'id') {
      const e = entryAt(loc); const old = e && e.step.id;
      if (!val || !/^[a-z][\w-]*$/i.test(val)) { return mutate((v) => { S.setPath(WF.at(v, locOf(loc)), 'id', val); }, opts); }
      if (old && old !== val) { rekey(old, val); if (B.open.has(old)) { B.open.delete(old); B.open.add(val); } if (B.sel === old) B.sel = val; if (B.tests[old]) { B.tests[val] = B.tests[old]; delete B.tests[old]; } }
      return mutate((v) => { WF.at(v, locOf(loc)).id = val; }, Object.assign({}, opts, { soft: false }));
    }
    return mutate((v) => { const s = WF.at(v, locOf(loc)); if (!s) return false; if (path === 'isolation.mode' && val === undefined) { delete s.isolation; return; } S.setPath(s, path, val); if (val === undefined) prune(s, path); }, opts);
  }
  /** Drop maps a removal left empty, so the YAML never says isolation: {}. */
  function prune(obj, path) { const parts = path.split('.'); for (let i = parts.length - 1; i > 0; i--) { const par = S.getPath(obj, parts.slice(0, i).join('.')); if (isObj(par) && !Object.keys(par).length) S.setPath(obj, parts.slice(0, i).join('.'), undefined); else break; } }
  function setOverride(loc, path, val) {
    const e = entryAt(loc); const wfName = B.w.yaml.match(/^name: (.*)$/m)[1]; const key = `${wfName}/${e.step.id}`;
    B.ov.steps = B.ov.steps || {}; const o = B.ov.steps[key] || {};
    const src = S.getPath(e.step, path);
    if (val === undefined || YAML.eq(val, src)) S.setPath(o, path, undefined); else S.setPath(o, path, val);
    if (Object.keys(o).length) B.ov.steps[key] = o; else delete B.ov.steps[key];
    if (!Object.keys(B.ov.steps).length) delete B.ov.steps;
    B.issues = WF.validate(B.text).concat(path === 'isolation.mode' && val === 'none' ? [{ level: 'warn', line: 1, msg: `packs.${B.pack}.steps["${key}"].isolation: none runs the pack's agent unsandboxed — your decision, made in your override (widening: asks for your passkey)`, step: e.id, field: 'isolation.mode' }] : []).concat(path === 'model' && val && !(SCHEMA.fleets.includes(val) || SCHEMA.models.includes(val) || val.includes('*')) ? [{ level: 'error', line: 1, msg: `model "${val}": no fleet in models: and no provider offers it`, step: e.id, field: 'model' }] : []);
    drawBar(); drawList(); drawSide();
  }
  const fieldEl = (t) => t.closest && t.closest('[data-wf-loc]');
  // Typing is debounced; any structural edit first flushes what's pending, so
  // a keystroke is never lost to a re-render.
  const debounced = {}, pendingFns = {};
  let flushing = false;
  function later(key, ms, fn) { clearTimeout(debounced[key]); pendingFns[key] = fn; debounced[key] = setTimeout(() => { delete pendingFns[key]; fn(); }, ms); }
  function flushPending() { if (flushing) return; flushing = true; try { Object.keys(pendingFns).forEach((k) => { clearTimeout(debounced[k]); const f = pendingFns[k]; delete pendingFns[k]; f(); }); } finally { flushing = false; } }
  document.addEventListener('input', (e) => {
    if (!B || !e.target.closest('#wfb')) return;
    const el = fieldEl(e.target);
    if (el && el.classList.contains('wf-vs')) { const h = el.querySelector('input[type=hidden]'); later(el.id, 250, () => setField(el.dataset.wfLoc, el.dataset.wfPath, h.value || undefined, { soft: true, coalesce: true })); return; }
    if (el && el.classList.contains('code-wrap')) { later(el.id, 300, () => setField(el.dataset.wfLoc, el.dataset.wfPath, coerce('code', e.target.value), { soft: true, coalesce: true })); return; }
    if (el && (el.tagName === 'INPUT' || el.tagName === 'TEXTAREA') && el.type !== 'checkbox' && !el.closest('.listed') && !el.closest('.kvedit')) { autocomplete(el); if (B.owner === 'pack') return; later(el.id, 350, () => setField(el.dataset.wfLoc, el.dataset.wfPath, coerce(el.dataset.wfType, el.value), { soft: el.dataset.wfPath !== 'id', coalesce: true })); return; }
    const flt = e.target.closest('[data-wf-filter]');
    if (flt) { later('flt', 400, () => mutate((v) => { const vals = flt.value.split(',').map((x) => x.trim()).filter(Boolean); v.filter[flt.dataset.wfFilter] = vals.length === 1 && !Array.isArray(v.filter[flt.dataset.wfFilter]) ? vals[0] : vals; }, { soft: true, coalesce: true })); return; }
    const map = e.target.closest('.wf-map');
    if (map) { later('map', 400, () => { const o = {}; UI.$$('.kvrow', map).forEach((r) => { const k = UI.$('[data-mk=k]', r).value.trim(), val = UI.$('[data-mk=v]', r).value; if (k) o[k] = /^-?\d+(\.\d+)?$/.test(val) ? Number(val) : val; }); setField(map.dataset.wfLoc, map.dataset.wfPath, Object.keys(o).length ? o : undefined, { soft: true, coalesce: true }); }); return; }
    const q = e.target.closest('[data-q-name],[data-q-ins],[data-q-ch]');
    if (q) later('q', 400, () => writeQuestions(q.dataset.loc));
  });
  document.addEventListener('change', (e) => {
    if (!B || !e.target.closest('#wfb')) return;
    const t = e.target, el = fieldEl(t);
    if (el && el.tagName === 'SELECT') return setField(el.dataset.wfLoc, el.dataset.wfPath, el.value === '' ? undefined : el.value);
    if (el && t.type === 'checkbox') return setField(el.dataset.wfLoc, el.dataset.wfPath, t.checked ? true : undefined);
    if (el && (el.tagName === 'INPUT' || el.tagName === 'TEXTAREA') && el.dataset.wfPath === 'id') { clearTimeout(debounced[el.id]); delete pendingFns[el.id]; return setField(el.dataset.wfLoc, 'id', el.value.trim() || undefined); }
    if (el && (el.tagName === 'INPUT' || el.tagName === 'TEXTAREA') && B.owner === 'pack') return setField(el.dataset.wfLoc, el.dataset.wfPath, coerce(el.dataset.wfType, el.value));
    if (t.dataset.wfAcct) return mutate((v) => { const s = WF.at(v, locOf(t.dataset.wfAcct)); s.uses = t.value + '.' + s.uses.split('.')[1]; });
    if (t.dataset.wfVerb) return mutate((v) => { const s = WF.at(v, locOf(t.dataset.wfVerb)); s.uses = s.uses.split('.')[0] + '.' + t.value; const d = SCHEMA.verb(s.uses); if (s.options && d) Object.keys(s.options).forEach((k) => { if (!d.fields.find((f) => f.k === k)) delete s.options[k]; }); });
    if (t.dataset.wfEngine) return mutate((v) => { const s = WF.at(v, locOf(t.dataset.wfEngine)); const was = s.use || s.run; delete s.run; s.use = t.value; if (t.value === 'cli') { delete s.code; s.command = s.command || ['echo', 'hello']; } else { delete s.command; if (!s.code || was === 'cli') s.code = { js: 'return { ok: true };\n', python3: 'import json, sys\nctx = json.load(sys.stdin)\nprint(json.dumps({"ok": True}))\n', sh: 'echo "{\\"ok\\": true}"\n' }[t.value] || '-- code\n'; } });
    if (t.dataset.wfCall) return mutate((v) => { const s = WF.at(v, locOf(t.dataset.wfCall)); s.call = t.value; s.with = {}; });
    if (t.dataset.wfListadd && t.tagName === 'SELECT' && t.value) { const box = t.closest('[data-wf-loc]'); const cur = S.getPath(WF.at(B.value, locOf(box.dataset.wfLoc)), box.dataset.wfPath) || []; return setField(box.dataset.wfLoc, box.dataset.wfPath, [].concat(cur, [t.value])); }
    if (t.dataset.qType) return writeQuestions(t.dataset.loc);
  });
  document.addEventListener('keydown', (e) => {
    if (!B || !e.target.dataset || !e.target.dataset.wfListadd || e.target.tagName !== 'INPUT') return;
    if (e.key !== 'Enter') return; e.preventDefault();
    const box = e.target.closest('[data-wf-loc]'); const val = e.target.value.trim(); if (!val) return;
    const cur = S.getPath(WF.at(B.value, locOf(box.dataset.wfLoc)), box.dataset.wfPath) || [];
    setField(box.dataset.wfLoc, box.dataset.wfPath, [].concat(cur, [val]));
    setTimeout(() => { const n = UI.$(`[data-wf-loc="${box.dataset.wfLoc}"][data-wf-path="${box.dataset.wfPath}"] .list-add`); if (n) n.focus(); });
  });
  UI.act('wf-list-del', (b) => { const box = b.closest('[data-wf-loc]'); const txt = b.parentElement.querySelector('.mono').textContent; const cur = [].concat(S.getPath(WF.at(B.value, locOf(box.dataset.wfLoc)), box.dataset.wfPath) || []); setField(box.dataset.wfLoc, box.dataset.wfPath, cur.filter((x) => String(x) !== txt)); });
  UI.act('wf-seg', (b) => setField(b.dataset.loc, b.dataset.path, b.dataset.v === '' ? undefined : b.dataset.v));
  UI.act('wf-bottarget', (b) => mutate((v) => { const s = WF.at(v, locOf(b.dataset.loc)); const cur = isObj(s.bot) ? s.bot : {}; const nb = Object.assign({}, cur); delete nb.name; delete nb.team; if (b.dataset.v === 'team') nb.team = (D.teams[0] || {}).name; else nb.name = (D.bots[0] || {}).name; s.bot = nb; }));
  UI.act('wf-waitmode', (b) => mutate((v) => { const s = WF.at(v, locOf(b.dataset.loc)); if (b.dataset.v === 'sleep') { delete s.wait_for; s.sleep = s.sleep || '30s'; } else { delete s.sleep; s.wait_for = { uses: 'gh.get_run', options: { repo: '{{.repo}}', run_id: '{{.run_id}}' }, until: "status == 'completed'", every: '5s', timeout: '2m' }; } }));
  function writeQuestions(loc) {
    const rows = UI.$$(`.q-row`).filter((r) => r.querySelector(`[data-loc="${loc}"]`));
    const q = {};
    rows.forEach((r) => { const n = r.querySelector('[data-q-name]').value.trim(); if (!n) return; const t = r.querySelector('[data-q-type]').value; const ins = r.querySelector('[data-q-ins]').value; const ch = r.querySelector('[data-q-ch]'); q[n] = Object.assign({ type: t }, t === 'choice' ? { choices: ch ? ch.value.split(',').map((x) => x.trim()).filter(Boolean) : [] } : {}, ins ? { instructions: ins } : {}); });
    const typeChanged = rows.some((r) => (r.querySelector('[data-q-type]').value === 'choice') !== !!r.querySelector('[data-q-ch]'));
    mutate((v) => { const s = WF.at(v, locOf(loc)); s.decide = s.decide || {}; s.decide.questions = q; }, { soft: !typeChanged, coalesce: true });
  }
  UI.act('wf-q-add', (b) => mutate((v) => { const s = WF.at(v, locOf(b.dataset.loc)); s.decide = s.decide || {}; s.decide.questions = s.decide.questions || {}; let n = 'question', i = 2; while (s.decide.questions[n]) n = 'question' + i++; s.decide.questions[n] = { type: 'noul', instructions: '' }; }));
  UI.act('wf-q-del', (b) => mutate((v) => { delete WF.at(v, locOf(b.dataset.loc)).decide.questions[b.dataset.n]; }));
  UI.act('wf-flt-key', (sel) => { const box = UI.$('#flt-val-box'); if (box) box.innerHTML = fltValInput(sel.value, false); });
  UI.act('wf-flt-add', () => { const k = UI.$('#flt-new').value, val = UI.$('#flt-val').value.trim(); if (!k || !val) { UI.$('#flt-val').focus(); UI.$('#flt-val').classList.add('shake'); return; } mutate((v) => { v.filter = isObj(v.filter) ? v.filter : {}; const base = RS.filterBase(k); if (RS.boolFilterKeys.has(base)) { v.filter[k] = val === 'true'; return; } if (/expr$/.test(k)) { v.filter[k] = val; return; } const vals = val.split(',').map((x) => x.trim()).filter(Boolean); v.filter[k] = vals.length === 1 && !/any|all|repo|branch|author/.test(base) ? vals[0] : vals; }); });
  UI.act('wf-flt-del', (b) => mutate((v) => { delete v.filter[b.dataset.k]; if (!Object.keys(v.filter).length) delete v.filter; }));
  UI.act('wf-src-del', (b) => mutate((v) => { const s = WF.sources(v); s.splice(+b.dataset.i, 1); const raw = s.map((x) => (x.block ? { [x.on]: x.block } : x.on)); if (!raw.length) delete v.on; else v.on = raw.length === 1 && typeof raw[0] === 'string' ? raw[0] : raw; }));
  UI.act('wf-in-add', () => { const n = UI.$('#in-name').value.trim(); if (!/^[a-z_][\w]*$/.test(n)) { UI.$('#in-name').focus(); UI.$('#in-name').classList.add('shake'); return; } mutate((v) => { v.inputs = v.inputs || {}; v.inputs[n] = Object.assign({ type: UI.$('#in-type').value }, UI.$('#in-req').checked ? { required: true } : {}); }); });
  UI.act('wf-in-del', (b) => mutate((v) => { delete v.inputs[b.dataset.k]; }));
  UI.act('wf-trig-pick', (b) => {
    const trigItems = () => { const items = [{ key: 'on:manual', group: 'conductor', icon: 'play', label: 'Manual run', sub: 'manual · conductor run, or the Run button' }, { key: 'on:timer.schedule', group: 'conductor', icon: 'clock', label: 'Schedule', sub: 'timer.<name> · a cron in timers:' }, { key: 'on:conductor.failed', group: 'conductor', icon: 'warn', label: 'A run failed', sub: 'conductor.failed · lifecycle' }];
    S.view().connectors.forEach((c) => { const d = SCHEMA.connectors[c.use]; if (d) Object.entries(d.events).forEach(([en, ed]) => items.push({ key: `on:${c.name}.${en}`, group: `${d.label} · ${c.name}`, icon: d.icon, label: ed.label, sub: `${c.name}.${en}` })); }); return items; };
    const items = trigItems();
    openPicker((item) => mutate((v) => {
      const on = item.key.slice(3);
      const cur = WF.sources(v).map((x) => (x.block ? { [x.on]: x.block } : x.on));
      if (b.dataset.add) { if (cur.includes(on)) return false; cur.push(on); } else cur.splice(0, cur.length, on);
      const val = cur.length === 1 ? cur[0] : cur;
      if ('on' in v) v.on = val;
      else { const rest = Object.assign({}, v); Object.keys(v).forEach((k) => delete v[k]); if ('name' in rest) { v.name = rest.name; delete rest.name; } v.on = val; Object.assign(v, rest); }
      if (!B.ev) { const e = SCHEMA.events.find((x) => x.on === on); if (e) B.ev = e.id; }
    }), { items, rebuild: trigItems, caps: 'trigger', title: 'What starts this workflow?', ph: 'Search events: check failed, review, incident, schedule…' });
  });

  /* references: the data picker and autocomplete */
  function scopeFor(el) { const loc = el.dataset.wfLoc; const e = loc ? entryAt(loc) : null; return WF.scope(B.value || {}, e, B.tests, evPayload()); }
  function insertRef(target, path) {
    const el = document.getElementById(target); if (!el) return;
    const isCode = el.classList.contains('code-wrap');
    const ta = isCode ? el.querySelector('.ced-ta') : el;
    const txt = isCode ? `ctx.${path}` : `{{.${path}}}`;
    ta.focus();
    const a = ta.selectionStart ?? ta.value.length, z = ta.selectionEnd ?? a;
    const before = ta.value.slice(0, a); const m = before.match(/\{\{\s*\.?[\w.]*$/);
    if (m && !isCode) ta.setRangeText(txt, a - m[0].length, z, 'end'); else ta.setRangeText(txt, a, z, 'end');
    ta.dispatchEvent(new Event('input', { bubbles: true }));
    if (ta.tagName === 'SELECT') return;
    closeAc();
  }
  UI.act('wf-ref', (b) => {
    const el = document.getElementById(b.dataset.target); if (!el) return;
    const groups = scopeFor(el);
    const s = UI.modal(`<div class="modal-head"><div><div class="caps">insert a reference · ${el.classList.contains('code-wrap') ? 'ctx.<path>' : '{{.path}}'}</div><h2>Data from earlier</h2></div><button class="icon-btn" data-close aria-label="Close">${icon('x')}</button></div>
      <div class="modal-body"><div class="input-icon" style="margin-bottom:10px">${icon('search')}<input class="input" id="rf-q" placeholder="filter: repo, logs.text…" aria-label="Filter data"></div><div id="rf-tree">${groups.map((g) => `<div class="dgroup" data-g="${esc(g.label)}"><div class="dg-head">${icon(g.icon)}<b class="mono">${esc(g.label)}</b><span class="mono tiny muted ellipsis">${esc(g.sub)}</span>${g.tested ? UI.statusMark('ok', 'actual', { cls: 'tpill ok', sm: true }) : g.unknown ? UI.statusMark('idle', 'Test it to see outputs', { cls: 'tpill idle', sm: true }) : UI.statusMark('idle', 'declared', { cls: 'tpill idle', sm: true })}</div>${g.key ? `<button type="button" class="dleaf ins whole" data-act="wf-ins" data-path="${esc(g.key)}" data-target="${b.dataset.target}"><span class="dk mono">${esc(g.key)}</span><span class="dv mono">the whole output</span><span class="dins">${icon('plus')}</span></button>` : ''}${tree(g.data, g.key, 0, b.dataset.target)}</div>`).join('')}</div></div>`, { wide: true });
    const q = UI.$('#rf-q', s); setTimeout(() => { if (innerWidth > 720 && !s.contains(document.activeElement)) q.focus(); }, 30);
    q.addEventListener('input', () => { const v = q.value.toLowerCase(); UI.$$('.dleaf.ins', s).forEach((x) => x.classList.toggle('hidden', !!v && !x.dataset.path.toLowerCase().includes(v))); });
  });
  UI.act('wf-ins', (b) => { UI.closeModal(); insertRef(b.dataset.target, b.dataset.path); });
  let ac = null;
  function closeAc() { const m = document.getElementById('wf-ac'); if (m) m.remove(); ac = null; }
  function autocomplete(el) {
    if (el.tagName !== 'INPUT' && el.tagName !== 'TEXTAREA') return;
    const before = el.value.slice(0, el.selectionStart); const m = before.match(/\{\{\s*\.([\w.-]*)$/);
    if (!m) return closeAc();
    const paths = WF.paths(scopeFor(el)).filter((p) => p.path.toLowerCase().startsWith(m[1].toLowerCase())).slice(0, 8);
    if (!paths.length) return closeAc();
    closeAc();
    const box = document.createElement('div'); box.id = 'wf-ac'; box.className = 'ac'; box.setAttribute('role', 'listbox');
    box.innerHTML = paths.map((p, i) => `<button type="button" role="option" class="${i === 0 ? 'on' : ''}" data-act="wf-ac-pick" data-path="${esc(p.path)}" data-target="${el.id}"><span class="mono">${esc(p.path)}</span><span class="muted mono tiny ellipsis">${esc(typeof p.value === 'object' ? JSON.stringify(p.value).slice(0, 40) : String(p.value).slice(0, 40))}</span></button>`).join('');
    document.body.appendChild(box);
    const r = el.getBoundingClientRect(); box.style.left = Math.min(r.left, innerWidth - box.offsetWidth - 8) + 'px'; box.style.top = (r.bottom + 4 + box.offsetHeight > innerHeight ? r.top - box.offsetHeight - 4 : r.bottom + 4) + 'px'; box.style.minWidth = Math.min(r.width, 360) + 'px';
    ac = { el, sel: 0, n: paths.length };
  }
  UI.act('wf-ac-pick', (b) => insertRef(b.dataset.target, b.dataset.path));
  document.addEventListener('keydown', (e) => {
    if (!ac || e.target !== ac.el) return;
    const box = document.getElementById('wf-ac'); if (!box) return;
    if (e.key === 'ArrowDown' || e.key === 'ArrowUp') { e.preventDefault(); ac.sel = (ac.sel + (e.key === 'ArrowDown' ? 1 : -1) + ac.n) % ac.n; UI.$$('button', box).forEach((x, i) => x.classList.toggle('on', i === ac.sel)); }
    else if (e.key === 'Enter' || e.key === 'Tab') { e.preventDefault(); UI.$$('button', box)[ac.sel].click(); }
    else if (e.key === 'Escape') { e.stopPropagation(); closeAc(); }
  }, true);
  document.addEventListener('focusout', (e) => { if (ac && e.target === ac.el) setTimeout(() => { if (ac && document.activeElement !== ac.el) closeAc(); }, 150); });

  /* YAML pane → list */
  let yT;
  UI.act('wf-yaml-input', (ta) => { clearTimeout(yT); yT = setTimeout(() => { commit(ta.value, { fromYaml: true, coalesce: true }); UI.editorMark(UI.$('#wf-yaml'), { err: B.issues.filter((i) => i.level === 'error').map((i) => i.line), hl: hlLines() }); }, 300); });

  /* test */
  function runTest(target) {
    const ev = evPayload(); if (!ev) return;
    const r = WF.test(B.value, target, ev);
    B.tests = r;
    const t = r[target];
    redraw();
    if (t) UI.toast(t.status === 'ok' ? `Tested ${esc(target)}` : t.status === 'error' ? `${esc(target)} failed` : `${esc(target)} ${t.status}`, t.status === 'error' ? esc(t.error) : `${Object.values(r).filter((x) => x.status === 'ok').length} steps ran · outputs now in later steps' data`, t.status === 'ok' ? 'ok' : t.status === 'error' ? 'fail' : 'idle');
  }
  UI.act('wf-test', (b) => { flushPending(); runTest(b.dataset.id); });
  UI.act('wf-test-all', () => { flushPending(); const all = WF.walk(B.value || {}).filter((e) => !e.branch && e.step && (e.step.id || e.kind === 'parallel')); const last = all[all.length - 1]; if (!last) return; const id = last.kind === 'parallel' ? (last.step.parallel.flat().slice(-1)[0] || {}).id : last.step.id; if (id) runTest(id); WF.walk(B.value).forEach((e) => { if (e.step && e.step.id && B.tests[e.step.id]) B.open.add(e.step.id); }); drawList(); });

  /* save and deploy */
  UI.act('wf-save', () => { flushPending();
    if (B.owner === 'pack') { const ch = S.stage('packs', B.pack, B.ov); B.ovBase = Y.clone(B.ov); UI.toast(ch ? 'Saved as a pack override' : 'Override matches the live config', ch ? `packs.${B.pack}.steps · staged, deploy to apply` : '', ch ? 'run' : 'idle'); UI.refreshChrome(); drawBar(); return; }
    if (status() !== 'draft') return;
    S.stage('workflows', B.name, B.text); delete D.drafts[B.name]; S.save();
    UI.refreshChrome(); drawBar(); drawIssues();
    UI.toast('Saved', 'staged in config · Deploy reviews the diff and applies it', 'run');
  });
  UI.act('wf-deploy', () => { flushPending();
    if (status() === 'draft') UI.acts['wf-save']();
    UI.go('#/config/apply');
  });
})();
