/* Shell, router and shared components for the prototype. No framework: each
 * screen is a function returning an HTML string, mounted into #main. The real
 * app is Preact + TypeScript (see the design doc); this file only has to make
 * the design clickable. */
(function () {
  const UI = (window.UI = {});
  const $ = (s, el = document) => el.querySelector(s);
  const $$ = (s, el = document) => [...el.querySelectorAll(s)];
  UI.$ = $; UI.$$ = $$;
  UI.esc = (s) => String(s ?? '').replace(/[&<>"]/g, (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;' }[c]));

  /* ---------- icons (1.5 stroke, 24 grid) ---------- */
  const P = {
    overview: '<path d="M4 13h6V4H4zM14 20h6v-9h-6zM4 20h6v-3H4zM14 7h6V4h-6z"/>',
    live: '<path d="M3 12h4l3-8 4 16 3-8h4"/>',
    attention: '<path d="M12 3l9 16H3z"/><path d="M12 10v4M12 17v.5"/>',
    runs: '<path d="M4 6h16M4 12h16M4 18h10"/>',
    workflows: '<circle cx="6" cy="6" r="2.5"/><circle cx="18" cy="12" r="2.5"/><circle cx="6" cy="18" r="2.5"/><path d="M8.5 6H12a3 3 0 013 3v.5M8.5 18H12a3 3 0 003-3v-.5"/>',
    members: '<circle cx="9" cy="8" r="3.5"/><path d="M2.5 20c.8-3.6 3.4-5.5 6.5-5.5s5.7 1.9 6.5 5.5"/><path d="M16 4.6a3.5 3.5 0 010 6.8M18.5 14.8c1.6.8 2.6 2.5 3 5.2"/>',
    browser: '<rect x="3" y="4" width="18" height="16" rx="2"/><path d="M3 9h18M7 6.5h.01M10 6.5h.01"/>',
    config: '<path d="M8 4H6a2 2 0 00-2 2v3l-2 3 2 3v3a2 2 0 002 2h2M16 4h2a2 2 0 012 2v3l2 3-2 3v3a2 2 0 01-2 2h-2"/>',
    secrets: '<circle cx="8" cy="15" r="4"/><path d="M11 12l9-9M17 6l3 3M15 8l2 2"/>',
    extensions: '<path d="M10 3h4v3a2 2 0 104 0V3h3v7h-3a2 2 0 100 4h3v7h-7v-3a2 2 0 10-4 0v3H3v-7h3a2 2 0 100-4H3V3z"/>',
    connectors: '<path d="M9 7V3M15 7V3M7 7h10v4a5 5 0 01-10 0zM12 16v5"/>',
    updates: '<path d="M20 12a8 8 0 11-2.3-5.6M20 4v5h-5"/>',
    logs: '<path d="M5 4h14v16H5zM8 8h8M8 12h8M8 16h5"/>',
    shield: '<path d="M12 3l8 3v6c0 4.5-3.4 8-8 9-4.6-1-8-4.5-8-9V6z"/>',
    key: '<circle cx="8" cy="15" r="4"/><path d="M11 12l9-9M17 6l3 3"/>',
    passkey: '<circle cx="10" cy="7" r="4"/><path d="M3 21c0-4 3-6.5 7-6.5 1.4 0 2.6.3 3.7.8"/><circle cx="18" cy="15" r="2.5"/><path d="M18 17.5V22M18 20h2"/>',
    search: '<circle cx="11" cy="11" r="7"/><path d="M20 20l-4-4"/>',
    sun: '<circle cx="12" cy="12" r="4"/><path d="M12 2v2M12 20v2M2 12h2M20 12h2M4.9 4.9l1.4 1.4M17.7 17.7l1.4 1.4M4.9 19.1l1.4-1.4M17.7 6.3l1.4-1.4"/>',
    moon: '<path d="M20 14.5A8 8 0 019.5 4 8 8 0 1020 14.5z"/>',
    lock: '<rect x="5" y="11" width="14" height="10" rx="2"/><path d="M8 11V8a4 4 0 018 0v3"/>',
    check: '<path d="M5 12.5l4.5 4.5L19 7"/>',
    x: '<path d="M6 6l12 12M18 6L6 18"/>',
    spin: '<path d="M12 3a9 9 0 019 9" /><path d="M12 21a9 9 0 01-9-9" opacity=".35"/>',
    pause: '<path d="M8 5v14M16 5v14"/>',
    play: '<path d="M7 4l13 8-13 8z"/>',
    hold: '<circle cx="12" cy="12" r="9"/><path d="M9.5 9v6M14.5 9v6"/>',
    diamond: '<path d="M12 3l9 9-9 9-9-9z"/>',
    dot: '<circle cx="12" cy="12" r="4.5"/>',
    ghost: '<path d="M5 21V10a7 7 0 0114 0v11l-2.5-2-2.3 2-2.2-2-2.2 2-2.3-2z"/><path d="M9.5 10h.01M14.5 10h.01"/>',
    chev: '<path d="M9 6l6 6-6 6"/>', chevd: '<path d="M6 9l6 6 6-6"/>',
    plus: '<path d="M12 5v14M5 12h14"/>',
    send: '<path d="M4 12l16-8-6 16-2.5-6.5z"/>',
    branch: '<circle cx="6" cy="5" r="2"/><circle cx="6" cy="19" r="2"/><circle cx="18" cy="8" r="2"/><path d="M6 7v10M18 10c0 4-6 3-12 7"/>',
    file: '<path d="M6 3h8l4 4v14H6z"/><path d="M14 3v4h4"/>',
    folder: '<path d="M3 6a1 1 0 011-1h5l2 2h9a1 1 0 011 1v10a1 1 0 01-1 1H4a1 1 0 01-1-1z"/>',
    commit: '<circle cx="12" cy="12" r="3.5"/><path d="M3 12h5.5M15.5 12H21"/>',
    hand: '<path d="M8 13V5.5a1.5 1.5 0 013 0V12M11 11V4.5a1.5 1.5 0 013 0V12M14 11.5V6a1.5 1.5 0 013 0v8c0 4-2.5 7-6.5 7S5 18.5 4 16l-1.6-3.3a1.4 1.4 0 012.3-1.6L8 14"/>',
    eye: '<path d="M2 12s3.6-7 10-7 10 7 10 7-3.6 7-10 7S2 12 2 12z"/><circle cx="12" cy="12" r="3"/>',
    eyeoff: '<path d="M3 3l18 18M10.6 5.1A10 10 0 0112 5c6.4 0 10 7 10 7a17 17 0 01-3.2 4M6.6 6.6C3.8 8.4 2 12 2 12s3.6 7 10 7c1.7 0 3.2-.5 4.5-1.2"/>',
    globe: '<circle cx="12" cy="12" r="9"/><path d="M3 12h18M12 3c2.5 2.7 3.8 5.7 3.8 9S14.5 18.3 12 21c-2.5-2.7-3.8-5.7-3.8-9S9.5 5.7 12 3z"/>',
    cookie: '<path d="M12 3a9 9 0 109 9 3 3 0 01-3.5-3A3 3 0 0114 5.5 3 3 0 0112 3z"/><path d="M8.5 10.5h.01M12 15h.01M15.5 13.5h.01M9 16h.01"/>',
    terminal: '<rect x="3" y="4" width="18" height="16" rx="2"/><path d="M7 9l3 3-3 3M13 15h4"/>',
    copy: '<rect x="8" y="8" width="12" height="12" rx="2"/><path d="M16 8V5a1 1 0 00-1-1H5a1 1 0 00-1 1v10a1 1 0 001 1h3"/>',
    undo: '<path d="M9 14L4 9l5-5"/><path d="M4 9h11a5 5 0 010 10h-3"/>',
    filter: '<path d="M4 5h16l-6 8v6l-4-2v-4z"/>',
    dollar: '<path d="M12 3v18M16.5 7.5c-.6-1.5-2.3-2.5-4.5-2.5-2.5 0-4.5 1.3-4.5 3.3 0 4.7 9.5 2.3 9.5 7.4 0 2-2 3.3-5 3.3-2.3 0-4.2-1-4.8-2.6"/>',
    zap: '<path d="M13 2L4 14h7l-1 8 9-12h-7z"/>',
    clock: '<circle cx="12" cy="12" r="9"/><path d="M12 7v5l3 2"/>',
    phone: '<rect x="6" y="2" width="12" height="20" rx="2.5"/><path d="M11 18h2"/>',
    sparkle: '<path d="M12 3l1.8 5.2L19 10l-5.2 1.8L12 17l-1.8-5.2L5 10l5.2-1.8z"/>',
    code: '<path d="M9 7l-5 5 5 5M15 7l5 5-5 5"/>',
    canvas: '<rect x="3" y="3" width="7" height="6" rx="1"/><rect x="14" y="15" width="7" height="6" rx="1"/><path d="M6.5 9v3.5a2 2 0 002 2H17.5V15"/>',
    trash: '<path d="M4 7h16M10 11v6M14 11v6M6 7l1 13h10l1-13M9 7V4h6v3"/>',
    edit: '<path d="M4 20h4L19 9l-4-4L4 16z"/><path d="M13 7l4 4"/>',
    more: '<circle cx="5" cy="12" r="1.2"/><circle cx="12" cy="12" r="1.2"/><circle cx="19" cy="12" r="1.2"/>',
    link: '<path d="M10 14a4 4 0 005.7 0l3-3a4 4 0 00-5.7-5.7l-1 1M14 10a4 4 0 00-5.7 0l-3 3a4 4 0 005.7 5.7l1-1"/>',
    mouse: '<rect x="6" y="3" width="12" height="18" rx="6"/><path d="M12 7v4"/>',
    keyboard: '<rect x="2" y="6" width="20" height="12" rx="2"/><path d="M6 10h.01M10 10h.01M14 10h.01M18 10h.01M7 14h10"/>',
    grid: '<path d="M4 4h7v7H4zM13 4h7v7h-7zM4 13h7v7H4zM13 13h7v7h-7z"/>',
    warn: '<path d="M12 3l9 16H3z"/><path d="M12 10v4M12 17v.5"/>',
    box: '<path d="M3 7l9-4 9 4v10l-9 4-9-4z"/><path d="M3 7l9 4 9-4M12 11v10"/>',
    server: '<rect x="3" y="4" width="18" height="7" rx="1.5"/><rect x="3" y="13" width="18" height="7" rx="1.5"/><path d="M7 7.5h.01M7 16.5h.01"/>',
    download: '<path d="M12 4v11M7 10l5 5 5-5M5 20h14"/>',
    expand: '<path d="M4 9V4h5M20 9V4h-5M4 15v5h5M20 15v5h-5"/>',
  };
  UI.icon = (n, cls = '') => `<svg class="${cls}" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">${P[n] || P.dot}</svg>`;

  /* ---------- status ---------- */
  const SG = { ok: ['check', 'ok'], run: ['spin', 'running'], wait: ['hold', 'waiting'], fail: ['x', 'failed'], idle: ['pause', 'idle'], shadow: ['ghost', 'shadow'], ember: ['diamond', 'needs you'] };
  UI.st = (s, label, pill = false) => { const g = SG[s] || SG.idle; return `<span class="st${pill ? ' pill' : ''}" data-s="${s}"><i>${UI.icon(g[0])}</i>${label ?? g[1]}</span>`; };
  UI.tag = (t, s) => `<span class="tag"${s ? ` data-s="${s}"` : ''}>${t}</span>`;
  UI.tier = (t) => `<span class="tier ${t}">${t}</span>`;
  UI.sigil = (mbr, size = '') => `<span class="sigil ${size}" style="--sig:${mbr.color}">${mbr.name[0]}${mbr.home === 'live' ? '<span class="live"></span>' : ''}</span>`;
  UI.ago = (iso) => { const s = (Date.now() - new Date(iso)) / 1000; if (s < 60) return Math.max(1, s | 0) + 's'; if (s < 3600) return (s / 60 | 0) + 'm'; if (s < 86400) return (s / 3600 | 0) + 'h'; return (s / 86400 | 0) + 'd'; };
  UI.clock = (iso) => new Date(iso).toTimeString().slice(0, 8);
  UI.usd = (v) => v == null ? '—' : v < 0.01 && v > 0 ? '<$0.01' : '$' + v.toFixed(2);
  UI.dur = (ms) => !ms ? '—' : ms < 1000 ? ms + 'ms' : ms < 60000 ? (ms / 1000).toFixed(1) + 's' : Math.round(ms / 60000) + 'm';
  UI.md = (t) => UI.esc(t).replace(/\*\*(.+?)\*\*/g, '<b>$1</b>').replace(/`([^`]+)`/g, '<code class="mono">$1</code>');

  /* ---------- YAML highlighting (line based; good enough for a prototype) ---------- */
  UI.yamlLine = (raw) => {
    const E = UI.esc;
    // split off a trailing comment (a # at line start or after a space, outside quotes)
    let body = raw, com = '', q = null;
    for (let i = 0; i < raw.length; i++) {
      const c = raw[i];
      if (q) { if (c === q) q = null; continue; }
      if (c === '"' || c === "'") { q = c; continue; }
      if (c === '#' && (i === 0 || /\s/.test(raw[i - 1]))) { body = raw.slice(0, i); com = raw.slice(i); break; }
    }
    const val = (v) => {
      const t = v.trim(); if (!t) return E(v);
      const lead = v.slice(0, v.indexOf(t)), tail = v.slice(v.indexOf(t) + t.length);
      let cls = '';
      if (/^["'].*["']$/.test(t)) cls = 'y-s';
      else if (/^(true|false|on|off|null|yes|no)$/.test(t)) cls = 'y-b';
      else if (/^-?\d[\d._]*[a-z%]*$/i.test(t)) cls = 'y-n';
      else if (/^[&*][\w-]+/.test(t)) cls = 'y-a';
      return E(lead) + (cls ? `<span class="${cls}">${E(t)}</span>` : E(t)) + E(tail);
    };
    const m = body.match(/^(\s*(?:-\s+)?)((?!-\s)[^\s:#'"{}\[\]][^:#{}]*?|"[^"]*")(:)(\s+|$)(.*)$/);
    let out;
    if (m) out = E(m[1]) + `<span class="y-k">${E(m[2])}</span><span class="y-p">:</span>` + E(m[4]) + val(m[5]);
    else { const d = body.match(/^(\s*-\s+)(.*)$/); out = d ? `<span class="y-p">${E(d[1])}</span>` + val(d[2]) : val(body); }
    return out + (com ? `<span class="y-c">${E(com)}</span>` : '');
  };
  UI.yaml = (text, opts = {}) => `<div class="code ${opts.cls || ''}">${text.split('\n').map((l, i) => `<span class="ln${(opts.hl || []).includes(i + 1) ? ' hl' : ''}${(opts.err || []).includes(i + 1) ? ' err' : ''}" data-ln="${i + 1}">${UI.yamlLine(l) || ' '}</span>`).join('')}</div>`;

  /* ---------- router ---------- */
  const routes = [];
  UI.route = (pattern, fn, opts = {}) => {
    const keys = [];
    const src = pattern.split('/').map((seg) => {
      const mt = seg.match(/^:(\w+)(\?)?$/);
      if (!mt) return '/' + seg;
      keys.push(mt[1]);
      return mt[2] ? '(?:/([^/]+))?' : '/([^/]+)';
    }).join('');
    const re = new RegExp('^' + src.slice(1) + '/?$');
    routes.push({ re, keys, fn, opts });
  };
  let cleanup = [];
  UI.onLeave = (f) => cleanup.push(f);
  UI.every = (ms, f) => { const id = setInterval(f, ms); cleanup.push(() => clearInterval(id)); return id; };
  UI.render = () => {
    cleanup.forEach((f) => f()); cleanup = [];
    const path = (location.hash.replace(/^#\/?/, '') || 'overview').split('?')[0];
    for (const r of routes) {
      const mt = path.match(r.re);
      if (!mt) continue;
      const params = {}; r.keys.forEach((k, i) => (params[k] = mt[i + 1] ? decodeURIComponent(mt[i + 1]) : undefined));
      const main = $('#main');
      const out = r.fn(params);
      main.innerHTML = String(out);
      main.scrollTop = 0;
      UI.setNav(r.opts.nav || path.split('/')[0]);
      if (r.opts.after) r.opts.after(params, main);
      if (typeof out === 'object' && out && out.after) out.after(main);
      UI.afterRender.forEach((f) => f(main));
      return;
    }
    $('#main').innerHTML = `<div class="page"><div class="empty"><span class="serif">No such page</span><a class="btn" href="#/overview">Back to overview</a></div></div>`;
  };
  UI.afterRender = [];
  UI.go = (h) => { location.hash = h; };
  window.addEventListener('hashchange', UI.render);

  /* ---------- nav ---------- */
  const navGroups = [
    ['Now', [['overview', 'Overview', 'overview'], ['live', 'Live', 'live', () => D.runs.filter((r) => r.status === 'run').length], ['attention', 'Needs you', 'attention', () => D.attention.length, true]]],
    ['Work', [['runs', 'Run history', 'runs'], ['workflows', 'Workflows', 'workflows'], ['members', 'Members', 'members'], ['browser', 'Browsers', 'browser', () => D.browsers.length]]],
    ['System', [['config', 'Config', 'config'], ['secrets', 'Secrets & vars', 'secrets'], ['extensions', 'Packs & plugins', 'extensions'], ['connectors', 'Connectors & hosts', 'connectors'], ['updates', 'Updates', 'updates', () => 1], ['logs', 'Logs & audit', 'logs'], ['security', 'Access', 'shield']]],
  ];
  UI.navHTML = () => navGroups.map(([g, items]) => `<div class="nav-group"><span class="caps">${g}</span><nav class="nav">${items.map(([k, l, ic, c, hot]) => {
    const n = c ? c() : null;
    return `<a href="#/${k}" data-nav="${k}">${UI.icon(ic)}<span class="lbl">${l}</span>${n ? `<span class="count${hot ? ' hot' : ''}">${n}</span>` : ''}</a>`;
  }).join('')}</nav></div>`).join('');
  UI.setNav = (k) => {
    $$('[data-nav]').forEach((a) => a.classList.toggle('on', a.dataset.nav === k));
  };

  /* ---------- modal / toast / tooltip ---------- */
  UI.modal = (html, opts = {}) => {
    UI.closeModal();
    const s = document.createElement('div');
    s.className = 'scrim'; s.id = 'scrim';
    s.innerHTML = `<div class="modal ${opts.wide ? 'wide' : ''}" role="dialog" aria-modal="true">${html}</div>`;
    s.addEventListener('click', (e) => { if (e.target === s && !opts.sticky) UI.closeModal(); });
    document.body.appendChild(s);
    $$('[data-close]', s).forEach((b) => b.addEventListener('click', UI.closeModal));
    return s;
  };
  UI.closeModal = () => { const s = $('#scrim'); if (s) s.remove(); };
  UI.toast = (title, sub, s = 'ember') => {
    let box = $('.toasts'); if (!box) { box = document.createElement('div'); box.className = 'toasts'; document.body.appendChild(box); }
    const t = document.createElement('div'); t.className = 'toast'; t.dataset.s = s; t.style.setProperty('--sc', `var(--${s === 'ember' ? 'accent' : s})`);
    t.innerHTML = `<div>${title}${sub ? `<span class="mono">${sub}</span>` : ''}</div>`;
    box.appendChild(t); setTimeout(() => t.remove(), 4200);
  };
  let tip;
  document.addEventListener('mouseover', (e) => {
    const el = e.target.closest('[data-tip]');
    if (!el) { if (tip) tip.remove(), (tip = null); return; }
    if (!tip) { tip = document.createElement('div'); tip.className = 'tooltip'; document.body.appendChild(tip); }
    tip.innerHTML = el.dataset.tip;
    const r = el.getBoundingClientRect();
    const tw = tip.offsetWidth;
    tip.style.left = Math.min(window.innerWidth - tw - 8, Math.max(8, r.left + r.width / 2 - tw / 2)) + 'px';
    tip.style.top = (r.top - tip.offsetHeight - 8 < 0 ? r.bottom + 8 : r.top - tip.offsetHeight - 8) + 'px';
  });

  /* ---------- passkey approval: the "seal" ----------
   * Mirrors the real flow: GET the canonical payload → show its digest →
   * navigator.credentials.get({challenge: H(nonce ‖ digest)}) with
   * userVerification: required → POST the assertion. Here the authenticator is
   * simulated. */
  UI.approve = (p, decision = 'approve', onDone) => {
    const deny = decision === 'deny';
    const lines = p.kind === 'terraform'
      ? `<div class="kv" style="margin-bottom:12px"><dt>action</dt><dd class="mono">terraform apply</dd><dt>repo@commit</dt><dd class="mono">${p.tf.repo}@${p.tf.commit.slice(0, 12)}</dd><dt>dir · workspace</dt><dd class="mono">${p.tf.dir} · ${p.tf.workspace}</dd><dt>plan</dt><dd class="mono">+${p.tf.add} ~${p.tf.change} -${p.tf.destroy} · <span style="color:var(--wait)">replaces ${p.tf.replaces.join(', ')}</span></dd><dt>plan sha256</dt><dd class="hash">${p.tf.plan_sha}</dd></div>`
      : `<div class="kv" style="margin-bottom:12px"><dt>verb</dt><dd class="mono">${p.verb}</dd><dt>action</dt><dd>${UI.esc(p.summary)}</dd><dt>requester</dt><dd class="mono">${p.requester} <span class="muted">(${p.cls})</span></dd></div>`;
    const s = UI.modal(`
      <div class="modal-head"><div><div class="caps" style="margin-bottom:6px">${p.member} · ${p.id} · case ${p.case}</div><h2>${deny ? 'Deny' : 'Approve'} this ${p.kind === 'terraform' ? 'apply' : 'action'}</h2></div><button class="icon-btn" data-close>${UI.icon('x')}</button></div>
      <div class="modal-body">
        ${lines}
        <div class="panel inset" style="padding:10px 12px">
          <div class="caps" style="margin-bottom:4px">You are signing</div>
          <div class="digest">{ member: <b>${p.member}</b>, proposal: <b>${p.id}</b>, decision: <b>${decision}</b>, action_sha256: <b>${p.digest.replace('sha256:', '').slice(0, 32)}</b>…, nonce: <b>7Qx2…f9</b> }</div>
        </div>
        <div class="seal" id="seal">
          <div class="ring">${UI.icon('passkey', 'key')}<svg class="arc" viewBox="0 0 108 108"><circle class="track" cx="54" cy="54" r="52"/><circle class="fill" cx="54" cy="54" r="53"/></svg></div>
          <div id="seal-msg"><b>Confirm with your passkey</b><div class="muted small">User verification required · this assertion covers only this digest</div></div>
        </div>
      </div>
      <div class="modal-foot"><span class="proto-note" style="margin-right:auto">authenticator simulated</span><button class="btn ghost" data-close>Cancel</button><button class="btn ${deny ? 'danger' : 'primary'}" id="seal-go">${UI.icon('passkey')}${deny ? 'Deny with passkey' : 'Approve with passkey'}</button></div>`, { sticky: true });
    $('#seal-go', s).addEventListener('click', (e) => {
      const seal = $('#seal', s); e.currentTarget.disabled = true;
      seal.classList.add('signing'); $('#seal-msg', s).innerHTML = '<b>Touch your security key…</b><div class="muted small">navigator.credentials.get · rpId ops-box.tail0000.ts.net</div>';
      setTimeout(() => {
        seal.classList.remove('signing'); seal.classList.add(deny ? 'denied' : 'done');
        $('#seal-msg', s).innerHTML = deny ? '<b>Denied</b><div class="muted small">Signed decision recorded · the member is told</div>' : `<b>Approved · executing</b><div class="muted small">Verified against operator <span class="mono">ops</span> · credential “laptop” · sign count 212</div>`;
        p.status = deny ? 'denied' : 'approved';
        setTimeout(() => { UI.closeModal(); UI.toast(deny ? `Denied ${p.id}` : `Approved ${p.id}`, deny ? 'decision signed' : (p.kind === 'terraform' ? 'conductor re-checks the plan hash, HEAD, and a clean tree, then applies' : 'the gate re-runs exactly the signed digest'), deny ? 'fail' : 'ok'); D.attention = D.attention.filter((a) => !a.href.includes(p.id) && !(a.sub || '').includes(p.id)); onDone && onDone(); UI.refreshChrome(); }, 1300);
      }, 1600);
    });
  };

  /* ---------- command palette ---------- */
  UI.palette = () => {
    const items = [
      ...navGroups.flatMap(([, it]) => it.map(([k, l, ic]) => [l, '#/' + k, ic])),
      ...D.members.map((mb) => [`Member · ${mb.name}`, '#/members/' + mb.name, 'members']),
      ...D.runs.slice(0, 8).map((r) => [`Run ${r.id} · ${r.workflow} ${r.repo ? r.repo + '#' + r.number : ''}`, '#/runs/' + r.id, 'runs']),
      ...D.workflows.slice(0, 5).map((w) => [`Edit workflow · ${w.name}`, '#/workflows/' + w.name, 'canvas']),
      ['Pause dispatch', '#/overview', 'pause'], ['Apply update v0.72.0', '#/updates', 'updates'], ['Phone layouts', 'phone.html', 'phone'], ['Sign-in & enrollment', '#/signin', 'passkey'],
    ];
    const el = document.createElement('div');
    el.innerHTML = `<div class="scrim" id="pal-scrim" style="place-items:start center;background:rgba(8,8,6,.4)"></div><div class="palette"><input placeholder="Jump to a run, member, workflow, setting…" autofocus><div class="res"></div></div>`;
    document.body.appendChild(el);
    const inp = $('input', el), res = $('.res', el); let sel = 0, list = items;
    const draw = () => { res.innerHTML = list.slice(0, 12).map(([l, h, ic], i) => `<a href="${h}" class="${i === sel ? 'on' : ''}">${UI.icon(ic)}${UI.esc(l)}<span class="muted mono" style="margin-left:auto">${h.replace('#/', '')}</span></a>`).join(''); };
    const close = () => el.remove();
    inp.addEventListener('input', () => { const q = inp.value.toLowerCase(); list = items.filter(([l]) => l.toLowerCase().includes(q)); sel = 0; draw(); });
    inp.addEventListener('keydown', (e) => {
      if (e.key === 'ArrowDown') sel = Math.min(sel + 1, list.length - 1), draw(), e.preventDefault();
      if (e.key === 'ArrowUp') sel = Math.max(sel - 1, 0), draw(), e.preventDefault();
      if (e.key === 'Enter' && list[sel]) { location.href = list[sel][1]; close(); }
      if (e.key === 'Escape') close();
    });
    $('#pal-scrim', el).addEventListener('click', close); res.addEventListener('click', close);
    draw(); setTimeout(() => inp.focus(), 10);
  };
  document.addEventListener('keydown', (e) => { if ((e.metaKey || e.ctrlKey) && e.key === 'k') { e.preventDefault(); UI.palette(); } if (e.key === 'Escape') UI.closeModal(); });

  /* ---------- theme ---------- */
  UI.theme = (t) => { document.documentElement.dataset.theme = t; try { localStorage.setItem('cd-theme', t); } catch (e) {} const b = $('#theme-btn'); if (b) b.innerHTML = UI.icon(t === 'dark' ? 'sun' : 'moon'); };
  UI.initTheme = () => { let t = new URLSearchParams(location.search).get('theme'); try { t = t || localStorage.getItem('cd-theme'); } catch (e) {} UI.theme(t || (matchMedia('(prefers-color-scheme: light)').matches ? 'light' : 'dark')); };

  /* ---------- chrome ---------- */
  UI.refreshChrome = () => {
    const d = D.daemon, need = D.attention.length, running = D.runs.filter((r) => r.status === 'run').length;
    $('#strip-cells').innerHTML = `
      <div class="cell"><span class="pulse"></span><b>${d.paused ? 'paused' : 'up'}</b>&nbsp;${d.uptime}</div>
      <div class="cell wide">${d.version}${d.update.available ? ` <span style="color:var(--run)">→ ${d.update.available}</span>` : ''}</div>
      <div class="cell wide" data-tip="Agents are read-only; conductor writes.<br><span class='mono'>backend ${d.sandbox.backend} · ${d.sandbox.containers} containers</span>">${UI.icon('shield')}<b>sandbox</b>&nbsp;enforcing</div>
      <div class="cell wide">paseo <b>ok</b></div>
      <div class="cell"><span class="pulse run"></span><b>${running}</b>&nbsp;running</div>
      ${need ? `<a class="cell alert" href="#/attention">${UI.icon('diamond')}<b>${need}</b>&nbsp;need you</a>` : ''}
      <div class="spacer"></div>
      <div class="cell wide">${d.magicdns} · TLS</div>`;
    $('#rail-nav').innerHTML = UI.navHTML();
    $('#tabbar').innerHTML = [['overview', 'Overview', 'overview'], ['live', 'Live', 'live'], ['attention', 'Approvals', 'attention', need], ['members', 'Members', 'members'], ['more', 'More', 'grid']].map(([k, l, ic, c]) => `<a href="#/${k}" data-nav="${k}">${UI.icon(ic)}${l}${c ? `<span class="count hot">${c}</span>` : ''}</a>`).join('');
    UI.setNav((location.hash.replace(/^#\/?/, '') || 'overview').split('/')[0]);
  };

  UI.boot = () => {
    UI.initTheme();
    document.body.innerHTML = `
      <div class="shell">
        <header class="strip">
          <a class="brand" href="#/overview">
            <svg class="baton" viewBox="0 0 24 24" fill="none"><path d="M4 20L18.5 5.5" stroke="var(--ink)" stroke-width="1.8" stroke-linecap="round"/><circle cx="19.5" cy="4.5" r="2.3" fill="var(--accent)"/><path d="M3 21l2.4-.7-1.7-1.7z" fill="var(--ink)"/></svg>
            <span class="wordmark">conductor</span>
          </a>
          <div id="strip-cells" style="display:contents"></div>
          <div class="search" onclick="UI.palette()">${UI.icon('search')}<span class="hide-phone">Search</span> <span class="kbd hide-phone">⌘K</span></div>
        </header>
        <aside class="rail">
          <div class="host"><span class="dot" style="--sc:var(--ok)"></span><span class="mono">${D.daemon.host} · ${D.daemon.tailscale_ip}</span></div>
          <div id="rail-nav"></div>
          <div class="foot">
            <div class="op"><span class="avatar">op</span><div class="who"><b>${D.operator.name}</b><span>passkey · idle ${D.operator.session.idle}</span></div></div>
            <button class="icon-btn" id="theme-btn" title="Theme" onclick="UI.theme(document.documentElement.dataset.theme==='dark'?'light':'dark')"></button>
            <a class="icon-btn" href="#/signin" title="Lock (sign out)">${UI.icon('lock')}</a>
          </div>
        </aside>
        <main class="main" id="main"></main>
        <nav class="tabbar" id="tabbar"></nav>
      </div>`;
    UI.theme(document.documentElement.dataset.theme);
    UI.refreshChrome();
    UI.render();
  };

  /* small shared bits */
  UI.head = (title, opts = {}) => `<div class="page-head"><div>${opts.crumbs ? `<div class="crumbs">${opts.crumbs}</div>` : ''}<h1>${title}${opts.sub ? `<span class="sub">${opts.sub}</span>` : ''}</h1></div>${opts.actions ? `<div class="actions">${opts.actions}</div>` : ''}</div>`;
  UI.tabs = (items, on) => `<div class="tabs">${items.map(([k, l, href, c, hot]) => `<a href="${href}" class="${k === on ? 'on' : ''}">${l}${c != null ? ` <span class="count${hot ? ' hot' : ''}">${c}</span>` : ''}</a>`).join('')}</div>`;
  UI.bind = (root, sel, ev, fn) => $$(sel, root).forEach((el) => el.addEventListener(ev, (e) => fn(e, el)));
})();
