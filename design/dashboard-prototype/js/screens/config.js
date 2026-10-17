/* Config: files, sections and owners, apply history with one-click restore,
 * the raw editor, and the change pipeline every editor ends in:
 * staged node edits → validate on a copy → diff → passkey → apply with a
 * backup → reload → health window → committed (or restored). */
(function () {
  const { icon, st, tag, ago, esc } = UI;
  const C = () => S.cfg;

  const SECTIONS = [
    ['settings', 'secrets', () => D.settings.length], ['vaults', 'secrets', () => D.vaults.length], ['connectors', 'connectors/connectors', () => D.connectors.length],
    ['hosts', 'connectors/hosts', () => D.hosts.length], ['runtimes', 'connectors/runtimes', () => D.runtimes.length], ['sandbox', 'connectors/isolation', () => 1],
    ['operators', 'security', () => Object.keys(D.operatorsCfg).length], ['bots', 'bots', () => D.bots.length], ['browser', 'browser', () => D.identities.length + D.access.credentials.length],
    ['packs', 'extensions/packs', () => D.packs.length], ['update', 'updates', () => 1], ['workflows', 'workflows', () => D.workflows.filter((w) => w.kind === 'workflow' && w.source === 'config').length], ['triggers', 'workflows', () => D.workflows.filter((w) => w.kind === 'trigger' && w.source === 'config').length],
  ];
  const view = { open: null };

  UI.route('config', () => {
    const n = D.pending.length;
    return `<div class="page">
    ${UI.head('Config', { sub: 'the file is the source of truth', actions: `<a class="btn" href="#/config/raw">${icon('code')}Edit YAML</a>${n ? `<a class="btn primary" href="#/config/apply">${icon('check')}${n} staged change${n > 1 ? 's' : ''}</a>` : `<button class="btn" ${UI.off('Nothing staged. Edit a section, a bot or a workflow, and its change waits here for review.')}>${icon('check')}No staged changes</button>`}` })}
    <div class="grid g-side">
      <div class="stack">
        <div class="panel" id="display"><div class="panel-head"><h3>Display preferences</h3><span class="muted small">this browser only · instant · not config, no passkey</span></div><div class="panel-body stack tight small">
          <div class="field"><label>Theme</label>${UI.seg([['auto', 'Match my device'], ['light', 'Light'], ['dark', 'Dark']], document.documentElement.dataset.themeSource === 'os' ? 'auto' : document.documentElement.dataset.theme, 'pref-theme')}</div>
          <div class="field"><label>Usage display</label>${UI.seg([['off', 'off'], ['subscription', 'subscription'], ['api', 'api ($)']], UI.usageMode(), 'usage-mode')}
            <div class="hint"><b>off</b> (the default): no usage or spend anywhere; your real usage lives in the proxy or load balancer in front of your model accounts. <b>subscription</b>: runs and turns conductor observes, per provider; plan limits and tokens only from a usage source. <b>api</b>: dollars, per provider. Providers are never added together. Cloud spend (commit_money, aws-cost) shows in every mode.</div></div>
          <label class="row">${UI.switchBtn(!!D.prefs.planned, 'planned-toggle', 'aria-label="Show planned features"')}<span><b>Show planned features</b> <span class="muted">previews of multi-user sign-in, designed but not built yet; off by default</span></span></label></div></div>
        ${UI.usageMode() === 'off' ? '' : (() => { const src = (S.staged('usage', 'usage') || {}).source; return `<div class="panel" id="general"><div class="panel-head"><h3>Usage source</h3><span class="muted small">config: usage.source</span>${S.find('usage', 'usage') ? tag('staged', 'run') : ''}</div><div class="panel-body stack tight small"><div class="field">${src ? `<div class="row"><span class="mono grow">${esc(src.name)} · ${esc(src.url)}</span><button class="btn sm" data-act="usage-src">Edit</button><button class="btn sm ghost" data-act="usage-src-del">Remove</button></div>` : `<div class="row"><span class="muted grow">none: only counts conductor observes (runs, turns), per provider</span><button class="btn sm" data-act="usage-src">${icon('plus')}Add a usage source</button></div>`}<div class="hint">The proxy or load balancer in front of your model accounts, which reports plan limits and tokens. Config, so it goes through the diff.</div></div>${S.find('usage', 'usage') ? '<a class="btn sm primary" href="#/config/apply" style="align-self:flex-start">Review change</a>' : ''}</div></div>`; })()}
        <div class="panel"><div class="panel-head"><h3>Files</h3><span class="muted small">what the daemon loaded at boot</span><div class="tools">${st('ok', 'on disk = loaded')}</div></div>
          <table class="tbl compact responsive"><thead><tr><th>file</th><th>owner</th><th>sha</th><th>loaded</th></tr></thead><tbody>
            <tr><td data-l="file" class="mono"><a class="link" href="#/config/raw">~/.config/conductor/config.yaml</a></td><td data-l="owner"><span class="owner op">you</span></td><td data-l="sha" class="mono">${D.daemon.config_sha}</td><td data-l="loaded" class="mono muted">${ago(D.configHistory[0].at)} ago</td></tr>
            <tr><td data-l="file" class="mono">~/.config/conductor/conf.d/connectors/pd.yaml</td><td data-l="owner"><span class="owner op">you · imports:</span></td><td data-l="sha" class="mono">91d0c7e3</td><td data-l="loaded" class="mono muted">50m ago</td></tr>
            <tr><td data-l="file" class="mono">.conductor/packs/review/ (vendored)</td><td data-l="owner"><span class="owner pack">pack review · c81e09a</span></td><td data-l="sha" class="mono">c81e09a</td><td data-l="loaded" class="mono muted">re-vendored 6m ago</td></tr>
            <tr><td data-l="file" class="mono">.conductor/packs/sre-bot/ (vendored)</td><td data-l="owner"><span class="owner pack">pack sre-bot · hold</span></td><td data-l="sha" class="mono">3d70b2e</td><td data-l="loaded" class="mono muted">held</td></tr>
            <tr><td data-l="file" class="mono">conductor.lock.yaml</td><td data-l="owner"><span class="owner pack">conductor</span></td><td data-l="sha" class="mono">5e2c91aa</td><td data-l="loaded" class="mono muted">6m ago</td></tr>
          </tbody></table></div>
        <div class="panel"><div class="panel-head"><h3>Sections</h3><span class="muted small">open one in its visual editor, or jump to it in the YAML</span></div><div class="panel-body"><div class="sec-grid">${SECTIONS.map(([s, href, c]) => `<div class="sec-chip"><a href="#/${href}" class="grow"><b>${s}</b> <span class="mono tiny muted">${c()}</span></a><a class="icon-btn" href="#/config/raw?at=${s}" data-tip="${s}: in the YAML" aria-label="${s} in the YAML">${icon('code')}</a></div>`).join('')}</div></div></div>
        <div class="callout small"><b>Packs.</b> A pack's own files are vendored and never edited here. What you change about a pack lives in <b>your</b> <span class="mono">packs.&lt;instance&gt;</span> block: bindings, <span class="mono">steps:</span> overrides, <span class="mono">triggers:</span> arms, <span class="mono">policy:</span>, <span class="mono">models:</span>. A pack can narrow, never widen. Loosening is your decision, made in your overrides, and the dashboard asks for your passkey when you do.</div>
      </div>
      <div class="stack">
        <div class="panel"><div class="panel-head"><h3>Applied changes</h3><span class="muted small">each keeps a backup</span></div><div class="list">${D.configHistory.map((h) => `
          <div class="item hist" style="align-items:flex-start">${h.status === 'live' ? st('ok', '') : st('idle', '')}<div class="grow"><div class="small">${esc(h.summary)}</div><div class="mono tiny muted">${h.id} · ${esc(h.who)} · ${ago(h.at)} ago</div>
            ${view.open === h.id ? `<div class="hist-diff">${(h.changes || []).map((c) => UI.diffView(c.before == null ? '' : typeof c.before === 'string' ? c.before : YAML.stringify({ [C()[c.coll].path(c.key)]: c.before }), c.after == null ? '' : typeof c.after === 'string' ? c.after : YAML.stringify({ [C()[c.coll].path(c.key)]: c.after }), 'unified', { file: C()[c.coll].path(c.key) }).html).join('')}</div>` : ''}</div>
            <div class="row" style="gap:4px"><button class="btn sm ghost" data-act="hist-view" data-id="${h.id}" aria-expanded="${view.open === h.id}">${icon(view.open === h.id ? 'chevd' : 'eye')}${view.open === h.id ? 'Hide' : 'Diff'}</button>
            ${h.status === 'live' ? tag('live', 'ok') : `<button class="btn sm" data-act="restore" data-id="${h.id}">${icon('undo')}Restore</button>`}</div></div>`).join('')}</div>
          <div class="panel-foot small muted">Restore is the same pipeline in reverse: the backup is validated on a copy first, then applied.</div></div>
        <div class="panel"><div class="panel-head"><h3>Guards</h3></div><div class="panel-body small stack tight">
          <div class="row">${st('ok', '')}Edits are node-level: comments, anchors and unknown keys are kept.</div>
          <div class="row">${st('ok', '')}Validated on a copy, with copied plugin state; <span class="mono">conductor validate</span> is not read-only.</div>
          <div class="row">${st('ok', '')}A config this binary can't load is never written.</div>
          <div class="row">${st('ok', '')}Also checked against the pending update (v0.72.0).</div>
          <div class="row">${st('ok', '')}Boot guard: a config that fails at boot is restored automatically.</div></div></div>
        <div class="panel"><div class="panel-body row small"><span class="muted grow">Prototype: everything here is fake state in this browser.</span><button class="btn sm" data-act="reset-demo">${icon('undo')}Reset demo data</button></div></div>
      </div></div></div>`;
  });
  UI.act('usage-mode', (b) => { D.prefs.usageMode = b.dataset.v; S.save(); UI.refreshChrome(); UI.rerender(); UI.toast(`Usage display: ${b.dataset.v}`, 'a display preference for this browser; config is unchanged', 'ok'); });
  UI.act('pref-theme', (b) => { UI.theme(b.dataset.v); UI.rerender(); });
  UI.act('usage-src', () => { const c = JSON.parse(JSON.stringify(S.staged('usage', 'usage'))); UI.form({ caps: 'usage.source', title: c.source ? 'Edit the usage source' : 'Add a usage source', intro: 'The proxy or load balancer in front of your model accounts. Conductor reads per-provider usage from it; it never adds providers together.', values: c.source || {}, fields: [{ k: 'name', type: 'string', req: true, ph: 'llm-proxy', help: 'Shown next to every figure it supplies.' }, { k: 'url', type: 'string', req: true, label: 'Usage URL', ph: 'http://llm-proxy.internal:4000/usage', validate: (v) => (/^https?:\/\/[^\s/]+/.test(v) ? '' : 'an http(s) URL') }, { k: 'token', type: 'secret', label: 'Token (optional)' }], submit: 'Stage', submitIcon: 'check', onSubmit: (v) => { c.source = { name: v.name, url: v.url }; if (v.token) c.source.token = v.token; S.stage('usage', 'usage', c, c.source ? 'usage.source' : ''); UI.closeModal(); UI.rerender(); } }); });
  UI.act('usage-src-del', () => { const c = JSON.parse(JSON.stringify(S.staged('usage', 'usage'))); delete c.source; S.stage('usage', 'usage', c); UI.rerender(); });
  UI.act('planned-toggle', () => { D.prefs.planned = !D.prefs.planned; if (!D.prefs.planned) D.prefs.instance = 'personal'; S.save(); UI.rerender(); UI.toast(D.prefs.planned ? 'Planned features shown' : 'Planned features hidden', D.prefs.planned ? 'each is marked Planned; nothing there changes your setup' : '', 'run'); });
  UI.act('hist-view', (b) => { view.open = view.open === b.dataset.id ? null : b.dataset.id; UI.rerender(); });
  UI.act('restore', (b) => {
    const n = S.restore(b.dataset.id);
    if (!n) { UI.toast('Nothing to restore', 'the live config already matches that backup', 'idle'); return; }
    UI.refreshChrome(); UI.go('#/config/apply');
  });

  /* ---------- raw editor ---------- */
  const raw = { text: null, base: null, err: null };
  function rawToChanges(text) {
    const p = YAML.parse(text), v = p.value || {}, meta = p.meta, lines = text.split('\n');
    const out = [];
    const cmp = (coll, key, node) => { const cur = S.staged(coll, key); if (!YAML.eq(cur, node)) out.push([coll, key, node]); };
    const mapSec = (coll, obj) => { const keys = new Set([...C()[coll].keys(), ...Object.keys(obj || {})]); keys.forEach((k) => cmp(coll, k, obj && k in obj ? obj[k] : null)); };
    mapSec('settings', v.settings); mapSec('vaults', v.vaults); mapSec('connectors', v.connectors); mapSec('hosts', v.hosts); mapSec('runtimes', v.runtimes);
    mapSec('operators', v.operators); mapSec('bots', v.bots); mapSec('packs', v.packs);
    mapSec('identities', v.browser && v.browser.identities); mapSec('credentials', v.browser && v.browser.credentials);
    if (v.sandbox) cmp('sandbox', 'sandbox', v.sandbox);
    if (v.update) cmp('update', 'update', v.update);
    const slice = (m, dedent) => lines.slice(m.line - 1, m.end).map((l) => l.slice(dedent)).join('\n') + '\n';
    const seen = new Set();
    (v.triggers || []).forEach((t, i) => { const m = meta[`triggers.${i}`]; if (!m || !t || !t.name) return; seen.add(t.name); let y = slice(m, 4); y = y.replace(/^\s*-\s*/, ''); cmp('workflows', t.name, y); });
    Object.keys(v.workflows || {}).forEach((name) => { const m = meta[`workflows.${name}`]; seen.add(name); const y = 'name: ' + name + '\n' + lines.slice(m.line, m.end).map((l) => l.slice(4)).join('\n') + '\n'; cmp('workflows', name, y); });
    C().workflows.keys().forEach((k) => { if (!seen.has(k)) out.push(['workflows', k, null]); });
    return out;
  }
  UI.route('config/raw', () => {
    const at = (location.hash.split('at=')[1] || '').split('&')[0];
    const live = S.configText(S.candidate());
    if (raw.base !== live || raw.text == null) { raw.base = live; raw.text = live; raw.err = null; }
    const outline = raw.text.split('\n').map((l, i) => [l, i + 1]).filter(([l]) => /^[a-z][\w-]*:/.test(l) || /^ {2}[\w"@:./-]+:/.test(l) || /^ {2}- name: /.test(l));
    const changed = YAML.diff(raw.base, raw.text).filter((r) => r.t !== ' ').length;
    return {
      toString: () => `<div class="page wide">
      ${UI.head('config.yaml', { crumbs: `<a href="#/config">Config</a>`, sub: `~/.config/conductor/config.yaml · sha ${D.daemon.config_sha}${D.pending.length ? ' · shown with your staged changes' : ''}`,
        actions: `<span class="mono tiny muted" id="raw-state">${changed ? changed + ' lines edited' : 'no edits'}</span><button class="btn" data-act="raw-revert" ${changed ? '' : UI.off('No edits to throw away')}>${icon('undo')}Revert edits</button><button class="btn primary" data-act="raw-review" ${changed ? '' : UI.off('Edit the YAML first; the review shows the node-level diff')}>${icon('check')}Validate &amp; review</button>` })}
      <div class="grid raw-grid">
        <div class="panel"><div class="panel-head"><span class="mono small">editing</span><div class="tools"><span class="mono tiny muted">schema from <span class="mono">conductor schema</span></span></div></div>
          <div id="raw-err" class="callout danger small ${raw.err ? '' : 'hide'}" style="margin:10px">${raw.err ? esc(raw.err.message) : ''}</div>
          <div class="ced-scroll">${UI.editor({ id: 'raw-ed', text: raw.text, lang: 'yaml', act: 'raw-input', minRows: 30, label: 'config.yaml', err: raw.err ? [raw.err.line] : [] })}</div></div>
        <div class="stack raw-side"><div class="panel"><div class="panel-head"><h3>Outline</h3></div><div class="tree" style="padding:6px;max-height:60vh;overflow:auto">${outline.map(([l, n]) => { const d = l.search(/\S/); const name = l.trim().replace(/^- name: /, '').replace(/:.*$/, ''); return `<a href="#/config/raw?at=${encodeURIComponent(d ? name : name)}" data-act="raw-goto" data-line="${n}" class="${at === name ? 'on' : ''}" style="padding-left:${10 + d * 6}px">${icon(d ? 'chev' : 'folder')}${esc(name)}</a>`; }).join('')}</div></div>
          <div class="callout info small">Raw edits are parsed into the same node tree as the visual editors, so a change here is still a node-level change and the same pipeline applies.</div></div>
      </div></div>`,
      after(main) {
        if (at) { const n = S.lineOf(raw.text, decodeURIComponent(at)); if (n > 0) setTimeout(() => { const ed = UI.$('#raw-ed', main); UI.editorMark(ed, { hl: [n] }); UI.editorScrollTo(ed, n); }); }
      },
    };
  }, { nav: 'config' });
  let rawT;
  UI.act('raw-input', (ta) => {
    raw.text = ta.value;
    clearTimeout(rawT);
    rawT = setTimeout(() => {
      try { YAML.parse(raw.text); raw.err = null; } catch (e) { raw.err = e; }
      const box = UI.$('#raw-err'); if (box) { box.classList.toggle('hide', !raw.err); box.textContent = raw.err ? raw.err.message : ''; }
      UI.editorMark(UI.$('#raw-ed'), { err: raw.err ? [raw.err.line] : [] });
      const changed = YAML.diff(raw.base, raw.text).filter((r) => r.t !== ' ').length;
      UI.$('#raw-state').textContent = changed ? changed + ' lines edited' : 'no edits';
      UI.$$('[data-act="raw-review"],[data-act="raw-revert"]').forEach((b) => { if (changed) { b.removeAttribute('aria-disabled'); b.removeAttribute('data-tip'); b.removeAttribute('data-reason'); } else b.setAttribute('aria-disabled', 'true'); });
    }, 250);
  });
  UI.act('raw-goto', (a) => { const ed = UI.$('#raw-ed'); const n = +a.dataset.line; UI.$$('.tree a').forEach((x) => x.classList.toggle('on', x === a)); UI.editorMark(ed, { hl: [n] }); UI.editorScrollTo(ed, n); });
  UI.act('raw-revert', () => { raw.text = raw.base; raw.err = null; UI.rerender(); });
  UI.act('raw-review', () => {
    let ch;
    try { ch = rawToChanges(raw.text); } catch (e) { raw.err = e; UI.rerender(); return; }
    if (!ch.length) { UI.toast('No node changed', 'whitespace or comment-only edits outside a workflow don\'t change config', 'idle'); return; }
    ch.forEach(([coll, key, node]) => S.stage(coll, key, node, null));
    raw.text = null; UI.refreshChrome(); UI.go('#/config/apply');
  });

  /* ---------- apply pipeline ---------- */
  const STEPS = ['Edit', 'Validate on a copy', 'Review diff', 'Apply', 'Reload & verify'];
  const ap = { mode: 'unified', ack: false, phase: 'check', done: null, checksFor: null };
  UI.route('config/apply', () => {
    const P = D.pending;
    if (ap.done && !P.length) return doneView();
    ap.done = null;
    if (!P.length) return `<div class="page">${UI.head('Review and apply', { crumbs: `<a href="#/config">Config</a>` })}
      <div class="panel"><div class="empty"><span class="serif">Nothing staged</span>Edit a connector, a bot, a secret reference or a workflow and its change waits here: validated on a copy, shown as a diff, applied with your passkey.<div class="row" style="justify-content:center;margin-top:10px"><a class="btn" href="#/config">Config</a><a class="btn" href="#/connectors">Connectors</a><a class="btn" href="#/workflows">Workflows</a></div></div></div></div>`;
    const key = JSON.stringify(P.map((c) => c.id));
    if (ap.checksFor !== key) { ap.checksFor = key; ap.phase = 'check'; ap.ack = false; }
    const before = S.configText(D), cand = S.candidate(), after = S.configText(cand), envB = S.envText(D), envA = S.envText(cand);
    // conductor.lock.yaml: what `conductor init` would pin for added or changed packs
    const lockOf = (st) => 'packs:\n' + (st.packs || []).map((p) => { const u = String((p.cfg || {}).use || ''); const src = u.split('@')[0], ver = u.split('@')[1] || p.ver || 'latest'; return `  ${p.name}:\n    source: ${src.replace(/^conductor-packs\//, 'NodeSpy/conductor-packs/')}\n    version: ${ver.replace(/^\^/, '')}${/^\.\//.test(src) ? '\n    local: true' : `\n    sha: ${(p.name.length * 2654435761 >>> 0).toString(16).slice(0, 8)}`}`; }).join('\n') + '\n';
    const lockB = lockOf(D), lockA = lockOf(cand);
    const v = S.validate(), wid = S.widening();
    const dv = UI.diffView(before, after, ap.mode, { sections: true });
    const restoring = P.every((c) => /^restore /.test(c.summary));
    const blocked = v.errors.length ? `${v.errors.length} error${v.errors.length > 1 ? 's' : ''} block the apply — fix ${v.errors.length > 1 ? 'them' : 'it'} or discard the change` : ap.phase === 'check' ? 'Checks are still running on the copy' : wid.length && !ap.ack ? 'This change widens what agents can do: tick the acknowledgement first' : '';
    const CHECKS = [
      ['Node-level edit', `${P.length} node${P.length > 1 ? 's' : ''} changed · ${(before.match(/^\s*#/gm) || []).length} comment lines kept byte-for-byte`, true],
      ['Base unchanged', `config.yaml is still ${D.daemon.config_sha}: no one edited it since you started`, true],
      ['Loads in this binary', v.errors.length ? `${v.errors.length} error${v.errors.length > 1 ? 's' : ''}` : `${D.daemon.version} loader, in-process, on the candidate bytes: 0 errors`, !v.errors.length],
      ['conductor validate on a copy', `scratch HOME + copied plugin state, live dirs read-only: ${v.errors.length} errors, ${v.warnings.length} warning${v.warnings.length === 1 ? '' : 's'}`, !v.errors.length],
      ['Loads in the pending update', v.errors.length ? 'skipped: fix the errors first' : `downloaded ${D.daemon.update.available || 'release'} validate on the same copy: 0 errors`, !v.errors.length],
      ['Team ceilings', (() => { const te = v.errors.filter((e) => /Team ceilings|team \S+ (classifies|holds|requires|allows)|outside team|subset|proactive pool|team \S+'s cap/.test(e)); return te.length ? `${te.length} bot setting${te.length > 1 ? 's' : ''} above a team ceiling: refused` : 'every bot stays within its team\'s ceiling, tiers and scopes'; })(), !v.errors.some((e) => /Team ceilings|outside team|subset|proactive pool|team \S+'s cap/.test(e))],
      ['Packs: narrow, never widen', (() => { const pw = wid.filter((w) => w.pack); return pw.length ? `${pw.length} pack change${pw.length > 1 ? 's' : ''} widen${pw.length > 1 ? '' : 's'}: needs acknowledgement` : 'no pack manifest or override loosens anything'; })(), true, wid.some((w) => w.pack) ? 'wait' : 'ok'],
      ['Widening', wid.length ? `${wid.length} change${wid.length > 1 ? 's' : ''} widen what agents can do: acknowledge them below` : 'your change widens nothing', true, wid.length ? 'wait' : 'ok'],
      ['References resolve', v.errors.some((e) => /vault|no |unreachable|does not exist|names no/.test(e)) ? 'a reference does not resolve (see errors)' : 'every secret, setting, connector and runtime the change references resolves', !v.errors.some((e) => /vault|no |unreachable|does not exist|names no/.test(e))],
    ];
    const cur = ap.phase === 'check' ? 1 : 2;
    return {
      toString: () => `<div class="page">
        ${UI.head(restoring ? 'Restore a backup' : 'Review and apply', { crumbs: `<a href="#/config">Config</a>`, sub: `${P.length} staged change${P.length > 1 ? 's' : ''} · +${dv.adds} −${dv.dels}`, actions: `<button class="btn ghost" data-act="discard-all">${icon('trash')}Discard all</button>` })}
        <div class="apply-steps" id="steps">${STEPS.map((s, i) => `<div class="as ${i < cur ? 'done' : i === cur ? (v.errors.length && i === 1 ? 'bad' : 'cur') : ''}"><b>${i < cur ? '✓' : i + 1}</b>${s}</div>`).join('')}</div>
        <div class="grid g-side">
          <div class="stack">
            <div class="panel"><div class="panel-head"><h3>Staged</h3><span class="muted small">each one is a node edit in config.yaml</span></div><div class="list">${P.map((c) => {
              const w = wid.filter((x) => x.change === c), href = C()[c.coll].href ? C()[c.coll].href(c.key) : '#/config';
              return `<div class="item" style="align-items:flex-start">${st(c.after == null ? 'fail' : c.before == null ? 'ok' : 'run', '')}<div class="grow"><div class="small"><b>${esc(c.summary)}</b></div><div class="mono tiny muted">${esc(C()[c.coll].path(c.key))} · ${c.after == null ? 'remove' : c.before == null ? 'add' : 'edit'} · staged ${ago(c.at)} ago</div>${w.map((x) => `<span class="tag" data-s="wait">${icon('warn')} widens: ${esc(x.text)}</span>`).join(' ')}</div>
                <a class="btn sm ghost" href="${href}">${icon('edit')}Edit</a><button class="btn sm ghost" data-act="unstage" data-id="${c.id}" aria-label="Discard ${esc(c.summary)}">${icon('x')}Discard</button></div>`; }).join('')}</div></div>
            <div class="panel"><div class="panel-head"><h3>Diff</h3><span class="muted small">what will be written · +${dv.adds} −${dv.dels}</span><div class="tools">${UI.seg([['unified', 'unified'], ['split', 'split'], ['whole', 'whole file']], ap.mode, 'diff-mode')}</div></div><div class="panel-body tight">${dv.html}</div></div>
            ${lockB !== lockA ? `<div class="panel lock-diff"><div class="panel-head"><h3 class="mono">conductor.lock.yaml</h3><span class="muted small">what conductor init fetches and pins, checked on the copy</span></div><div class="panel-body tight">${UI.diffView(lockB, lockA, ap.mode === 'split' ? 'split' : 'unified', { file: 'conductor.lock.yaml' }).html}</div></div>` : ''}
            ${envB !== envA ? `<div class="panel"><div class="panel-head"><h3 class="mono">conductor.env</h3><span class="muted small">daemon environment · backed up before writing · restart through the boot guard</span></div><div class="panel-body tight">${UI.diffView(envB, envA, ap.mode === 'split' ? 'split' : 'unified', { file: 'conductor.env' }).html}</div></div>` : ''}
          </div>
          <div class="stack">
            <div class="panel"><div class="panel-head"><h3>Checks</h3><div class="tools" id="chk-state">${ap.phase === 'check' ? st('run', 'running') : v.errors.length ? st('fail', `${v.errors.length} failed`) : st('ok', `7 passed${v.warnings.length ? ` · ${v.warnings.length} warning${v.warnings.length > 1 ? 's' : ''}` : ''}`)}</div></div>
              <div class="panel-body" id="checks">${CHECKS.map(([k, d, ok, s], i) => `<div class="check-row" data-i="${i}"><span class="ci">${ap.phase === 'check' ? st('run', '') : st(ok ? s || 'ok' : 'fail', '')}</span><div><b>${k}</b><div class="muted tiny">${esc(d)}</div></div></div>`).join('')}</div>
              ${ap.phase !== 'check' ? `<details class="validate-out"${v.errors.length || v.warnings.length ? ' open' : ''}><summary class="small">conductor validate output <span class="muted">(stdout and stderr, on the copy)</span></summary><pre class="code-out mono" id="validate-out">${esc(S.validateOutput(v))}</pre></details>` : ''}
              ${ap.phase !== 'check' && (v.errors.length || v.warnings.length) ? `<div class="panel-body" style="border-top:1px solid var(--line)">${v.errors.map((e) => `<div class="callout danger small" style="margin-bottom:6px"><span class="mono">${esc(e)}</span></div>`).join('')}${v.warnings.map((e) => `<div class="callout warn small" style="margin-bottom:6px">${esc(e)}</div>`).join('')}</div>` : ''}</div>
            <div class="panel" id="apply-box"><div class="panel-body stack">
              ${wid.length ? `<label class="row small ack"><input type="checkbox" class="cbx" data-on-change="ack" ${ap.ack ? 'checked' : ''}> <span>I understand this <b>widens</b> what agents can do:</span></label><ul class="widen-list">${wid.map((x) => `<li><span class="mono tiny">${esc(x.path)}</span> ${esc(x.text)}</li>`).join('')}</ul><label class="row small ack hide"></label>` : ''}
              <div class="pk-why ${wid.length ? 'needs' : 'none'}" id="pk-why">${wid.length ? `${icon('passkey')}<span><b>Needs your passkey:</b> ${esc(wid[0].text)}${wid.length > 1 ? ` (and ${wid.length - 1} more widening${wid.length > 2 ? 's' : ''} above)` : ''}. Anything that lets bots or agents do more is signed, like an approval.</span>` : `${icon('check')}<span><b>No passkey needed:</b> nothing here lets bots or agents do more (moves, renames, descriptions, tighter limits, removals). It's still checked on a copy, backed up first, and can be rolled back.</span>`}</div>
              <div class="small muted">${icon('download')} A backup is written before anything changes. Applying restarts conductor's config; in-flight runs are checkpointed.</div>
              <button class="btn lg primary" data-act="apply-go" ${blocked ? UI.off(blocked) : ''}>${wid.length ? `${icon('passkey')}Apply with passkey` : `${icon('check')}Apply`}</button></div></div>
          </div></div></div>`,
      after(main) {
        if (ap.phase !== 'check') return;
        const rows = UI.$$('.check-row', main); let i = 0;
        const tick = () => {
          if (!document.body.contains(main) || !UI.$('#checks')) return;
          if (i < rows.length) { rows[i].querySelector('.ci').innerHTML = st(CHECKS[i][2] ? CHECKS[i][3] || 'ok' : 'fail', ''); i++; setTimeout(tick, UI.fast ? 10 : 160); return; }
          ap.phase = 'review'; UI.render({ keepScroll: true });
        };
        setTimeout(tick, UI.fast ? 10 : 250);
      },
    };
  }, { nav: 'config' });
  UI.act('diff-mode', (b) => { ap.mode = b.dataset.v; UI.rerender(); });
  UI.act('ack', (cb) => { ap.ack = cb.checked; UI.rerender(); });
  UI.act('unstage', (b) => { S.unstage(b.dataset.id); UI.rerender(); });
  UI.act('discard-all', () => UI.confirm({ title: 'Discard all staged changes?', body: `${D.pending.length} change${D.pending.length > 1 ? 's' : ''} will be thrown away. The live config is untouched.`, ok: 'Discard', danger: true }).then((ok) => { if (ok) { S.discardAll(); UI.rerender(); } }));
  UI.act('apply-go', () => {
    const P = D.pending, digest = Math.random().toString(16).slice(2, 10), wid = S.widening();
    if (!wid.length) { const entry = S.apply(); D.audit.unshift({ ts: new Date().toISOString(), event: 'config_apply', who: 'operator:ops', detail: `${P.length || 1} change(s) · no widening, no passkey · session · backup ${entry && entry.id}` }); ap.done = { entry, stage: 0, pk: false }; UI.refreshChrome(); UI.render(); return; }
    UI.stepup({ title: `Apply ${P.length} change${P.length > 1 ? 's' : ''}`, purpose: 'config apply', what: `{ purpose: <b>config_apply</b>, base_sha: <b>${D.daemon.config_sha}</b>, diff_sha256: <b>${digest}…</b>, changes: <b>${P.length}</b> }`, ok: 'Apply with passkey' }).then((ok) => {
      if (!ok) return;
      const entry = S.apply();
      ap.done = { entry, stage: 0, pk: true };
      UI.refreshChrome(); UI.render();
    });
  });
  function doneView() {
    const e = ap.done.entry;
    const stages = [ap.done.pk ? ['Passkey assertion verified', 'operator ops · credential laptop · it widens what bots can do'] : ['Signed-in session checked', 'operator ops · nothing widens, so no passkey'], ['Backup written', `${e.id} (0600)`], ['Apply journal written', '<state>/config-apply/pending.json'], ['config.yaml replaced', `atomic rename · sha ${D.daemon.config_sha}`], ['Daemon re-exec', 'SIGHUP path · in-flight steps checkpointed'], ['Reconnected', 'SSE resumed from event 88412'], [`Booted with ${D.daemon.config_sha}`, `${D.daemon.version} · all connectors up`], ['Health window', '60s: no crash, connectors ok → committed']];
    const k = ap.done.stage, fin = k >= stages.length;
    return {
      toString: () => `<div class="page">${UI.head('Applied', { crumbs: `<a href="#/config">Config</a>`, sub: esc(e.summary) })}
        <div class="apply-steps">${STEPS.map((s, i) => `<div class="as ${i < (fin ? 5 : k >= 4 ? 4 : 3) ? 'done' : 'cur'}"><b>${i < (fin ? 5 : k >= 4 ? 4 : 3) ? '✓' : i + 1}</b>${s}</div>`).join('')}</div>
        <div class="grid g-side"><div class="panel"><div class="panel-head"><h3>Reload &amp; verify</h3>${fin ? st('ok', 'committed') : st('run', 'in progress')}</div><div class="panel-body" id="stages">${stages.slice(0, Math.min(k + 1, stages.length)).map(([a, b], i) => `<div class="check-row">${i < k || fin ? st('ok', '') : st('run', '')}<div><b>${a}</b><div class="muted tiny mono">${esc(b)}</div></div></div>`).join('')}</div></div>
        <div class="stack">${fin ? `<div class="callout ember small"><b>Applied.</b> Live config is <span class="mono">${D.daemon.config_sha}</span>. Backup <span class="mono">${e.id}</span> holds what it replaced.</div>
          <div class="row"><button class="btn" data-act="rollback-now" data-id="${e.id}">${icon('undo')}Roll back</button>${D.prefs.returnTo ? `<a class="btn primary" href="${D.prefs.returnTo.href}" data-act="return-to">${icon('chev')}Back to ${esc(D.prefs.returnTo.label)}</a>` : `<a class="btn ghost" href="#/config">Done</a>`}</div>${D.prefs.returnTo ? `<div class="muted tiny">Taking you back to ${esc(D.prefs.returnTo.label)}…</div>` : ''}` : `<div class="callout small">The daemon re-execs on reload. This page reconnects by itself and resumes the stream.</div>`}</div></div></div>`,
      after() { if (fin) { const rt = D.prefs.returnTo; if (rt) setTimeout(() => { if (location.hash.startsWith('#/config/apply') && D.prefs.returnTo) { D.prefs.returnTo = null; S.save(); ap.done = null; UI.go(rt.href); } }, UI.fast ? 700 : 1200); return; } setTimeout(() => { if (!ap.done) return; ap.done.stage++; if (location.hash.startsWith('#/config/apply')) UI.render({ keepScroll: true }); }, UI.fast ? 15 : k === 4 ? 900 : 260); },
    };
  }
  UI.act('return-to', (a) => { const rt = D.prefs.returnTo; D.prefs.returnTo = null; S.save(); ap.done = null; UI.go(rt ? rt.href : '#/config'); });
  UI.act('rollback-now', (b) => { const n = S.restore(b.dataset.id); ap.done = null; UI.refreshChrome(); if (n) UI.render(); });
})();
