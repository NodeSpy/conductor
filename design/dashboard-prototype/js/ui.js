/* Shell, router and shared components for the prototype. No framework: each
 * screen is a function returning an HTML string, mounted into #main. The real
 * app is Preact + TypeScript (see the design doc); this file only has to make
 * the design clickable. */
(function () {
  const UI = (window.UI = {});
  // Tests set window.__PROTO_FAST so simulated waits (authenticator, reload) take milliseconds.
  UI.fast = !!window.__PROTO_FAST;
  /** Planned features (multi-user, #185) are design previews behind Config → General; off by default. */
  UI.planned = () => !!(window.D && D.prefs && D.prefs.planned);
  UI.live = (p) => !p.planned || UI.planned();
  // Surface script errors visibly: a prototype should never fail silently.
  window.addEventListener('error', (e) => { const b = document.createElement('div'); b.style.cssText = 'position:fixed;left:0;right:0;bottom:0;z-index:99999;background:var(--color-status-danger-solid);color:var(--color-text-on-accent);font:12px monospace;padding:6px 10px'; b.textContent = 'prototype error: ' + e.message; document.body.appendChild(b); });
  const $ = (s, el = document) => el.querySelector(s);
  const $$ = (s, el = document) => [...el.querySelectorAll(s)];
  UI.$ = $; UI.$$ = $$;
  UI.esc = (s) => String(s ?? '').replace(/[&<>"]/g, (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;' }[c]));

  /* ---------- icons: Lucide at a 1.75 stroke (js/icons.js); status glyphs are Daylight's own ---------- */
  const P = window.ICONS || {};
  UI.icon = (n, cls = '') => `<svg class="${cls}" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.75" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">${P[n] || P.dot}</svg>`;
  /** A status glyph (success, warning, danger, info, running, neutral, skipped, shadow, needs-you), 16-unit grid. */
  UI.glyph = (n, cls = '') => `<svg class="glyph ${cls}" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">${(window.GLYPHS || {})[n] || ''}</svg>`;

  /* ---------- status ---------- */
  /* ---------- status marks, labels, filter chips, counts, row states (css/marks.css) ----------
   * The only way screens draw these. The brand's final status-and-label system swaps in here and in marks.css. */
  // state → Daylight glyph and default word. Passing work is ink; colour is for failed, waiting, running, needs you.
  const SG = { ok: ['success', 'ok'], success: ['success', 'done'], run: ['running', 'running'], wait: ['warning', 'waiting'], fail: ['danger', 'failed'], idle: ['neutral', 'idle'], shadow: ['shadow', 'shadow'], ember: ['needs-you', 'needs you'], skip: ['skipped', 'not reached'] };
  /** A status mark: glyph + word. opts: { cls, sm, tip, id } */
  UI.statusMark = (s, word, o = {}) => { const g = SG[s] || SG.idle; return `<span class="st${o.sm ? ' sm' : ''}${o.cls ? ' ' + o.cls : ''}" data-s="${s}"${o.id ? ` id="${o.id}"` : ''}${o.tip ? ` data-tip="${UI.esc(o.tip)}"` : ''}><i>${UI.glyph(g[0], s === 'run' ? 'spin' : '')}</i>${word ?? g[1]}</span>`; };
  UI.st = (s, label) => UI.statusMark(s, label);
  /** A label: a quiet word. opts: { s (state), mono, cls, icon, tip, attrs } */
  UI.label = (text, o = {}) => `<span class="tag${o.mono ? ' mono' : ''}${o.cls ? ' ' + o.cls : ''}"${o.s ? ` data-s="${o.s}"` : ''}${o.tip ? ` data-tip="${UI.esc(o.tip)}"` : ''}${o.attrs ? ' ' + o.attrs : ''}>${o.icon ? UI.icon(o.icon) : ''}${text}</span>`;
  UI.tag = (t, s) => UI.label(t, { s });
  /** A filter chip: { text, on, act, attrs, icon, cls, tag ('button'|'label'|'a'), href } */
  UI.filterChip = (o) => { const t = o.tag || 'button', on = !!o.on; return `<${t}${t === 'button' ? ' type="button"' : ''}${o.href ? ` href="${o.href}"` : ''} class="chip${on ? ' on' : ''}${o.cls ? ' ' + o.cls : ''}"${t === 'button' ? ` aria-pressed="${on}"` : ''}${o.act ? ` data-act="${o.act}"` : ''}${o.attrs ? ' ' + o.attrs : ''}>${on && !o.icon && !o.noCheck ? UI.icon('check') : o.icon ? UI.icon(o.icon) : ''}${o.text}</${t}>`; };
  /** Daylight's illustration motif (never part of the logo): the arch rising over a hairline horizon, rays above. */
  UI.sunrise = (cls = '') => `<svg class="sunrise ${cls}" viewBox="0 0 160 92" aria-hidden="true"><g stroke="var(--color-border-default)" stroke-width="2" stroke-linecap="round">${[-62, -38, -14, 14, 38, 62].map((a) => { const r = Math.PI * (a - 90) / 180; return `<line x1="${(80 + Math.cos(r) * 44).toFixed(1)}" y1="${(70 + Math.sin(r) * 44).toFixed(1)}" x2="${(80 + Math.cos(r) * 54).toFixed(1)}" y2="${(70 + Math.sin(r) * 54).toFixed(1)}"/>`; }).join('')}</g><path d="M46 70a34 36 0 0 1 68 0h-9a25 26 0 0 0-50 0z" fill="var(--color-logo-ink)"/><circle cx="80" cy="70" r="10" fill="var(--color-accent-default)"/><path d="M8 76h144" stroke="var(--color-logo-ink)" stroke-width="2" stroke-linecap="round"/><path d="M30 84h40M90 84h28" stroke="var(--color-border-default)" stroke-width="2" stroke-linecap="round"/></svg>`;
  /** An empty state: what appears here (a short human title), why it's empty, and the one next step. */
  UI.empty = ({ title, body = '', action = '', small = false, art = true, cls = '' }) => `<div class="empty${small ? ' small' : ''}${cls ? ' ' + cls : ''}">${art ? UI.sunrise() : ''}<span class="serif">${title}</span>${body ? `<p>${body}</p>` : ''}${action}</div>`;
  /** A filter that picks one value: a chip wrapping a native select. { icon, on, label, attrs (on the select), options (html), cls } */
  UI.selectChip = (o) => `<label class="chip chip-sel${o.on ? ' on' : ''}${o.cls ? ' ' + o.cls : ''}"${o.wrapAttrs ? ' ' + o.wrapAttrs : ''}>${o.icon ? UI.icon(o.icon) : ''}<span class="sr-only">${o.label}</span><select ${o.attrs || ''} aria-label="${o.label}">${o.options}</select></label>`;
  /** A count beside a tab or nav item; hot (ember) only where a person is needed. */
  UI.count = (n, o = {}) => `<span class="count${o.hot ? ' hot' : ''}${o.cls ? ' ' + o.cls : ''}"${o.label ? ` aria-label="${UI.esc(o.label)}"` : ''}>${n}</span>`;
  /** Attributes for a row (tr, .item) that needs someone: a thin leading edge in the state's colour, never a tint. */
  UI.rowState = (s) => (['fail', 'stopped', 'wait', 'ember', 'sel', 'moved'].includes(s) ? ` data-row="${s === 'stopped' ? 'fail' : s}"` : '');
  UI.tier = (t) => `<span class="tier ${t}">${t}</span>`;
  /** The logo, from the one reference in assets/logo/logo.js: 'mark' (the kit's inline mark) or 'lockup' (the mark
      beside the live-text wordmark, Fraunces SemiBold). The kit's mark-inline.html is fetched once and inlined, so it
      takes its colours from the tokens and needs no <use> (which WebKit won't resolve for late symbols). */
  UI.logoSvg = '';
  UI.loadLogo = () => { const L = window.LOGO || {}; if (!L.inline || UI.logoSvg) return;
    fetch(L.inline).then((r) => r.text()).then((t) => { const svg = new DOMParser().parseFromString(t, 'text/html').querySelector('svg'); if (!svg) return;
      svg.removeAttribute('role'); svg.removeAttribute('aria-label'); svg.setAttribute('aria-hidden', 'true'); svg.setAttribute('focusable', 'false');
      UI.logoSvg = svg.outerHTML; document.querySelectorAll('.logo-slot').forEach((n) => { n.innerHTML = UI.logoSvg; }); }).catch(() => {}); };
  UI.logo = (kind = 'mark', cls = '') => { const L = window.LOGO || {}, mark = `<span class="logo-slot ${cls}">${UI.logoSvg}</span>`;
    return kind === 'mark' ? mark : `<span class="lockup" role="img" aria-label="${UI.esc(L.wordmark || 'conductor')}">${mark}<span class="wordmark" aria-hidden="true">${UI.esc(L.wordmark || 'conductor')}</span></span>`; };
  UI.sigil = (mbr, size = '') => `<span class="sigil ${size}" style="--sig:${mbr.color}">${mbr.name[0]}${mbr.home === 'live' ? '<span class="live"></span>' : ''}</span>`;
  UI.ago = (iso) => { const s = (Date.now() - new Date(iso)) / 1000; if (s < 60) return Math.max(1, s | 0) + 's'; if (s < 3600) return (s / 60 | 0) + 'm'; if (s < 86400) return (s / 3600 | 0) + 'h'; return (s / 86400 | 0) + 'd'; };
  UI.clock = (iso) => new Date(iso).toTimeString().slice(0, 8);
  /* human times: "52 min ago (17:03)"; the exact local time (and UTC) on hover */
  UI.agoText = (iso) => { const s = (Date.now() - new Date(iso)) / 1000; if (s < 60) return 'just now'; if (s < 3600) return `${s / 60 | 0} min ago`; if (s < 86400) { const h = s / 3600 | 0; return `${h} h ago`; } const d = s / 86400 | 0; return d === 1 ? 'yesterday' : `${d} days ago`; };
  UI.hm = (iso) => new Date(iso).toTimeString().slice(0, 5);
  UI.when = (iso, withClock = false) => { const d = new Date(iso); return `<time datetime="${d.toISOString()}" data-tip="${d.toLocaleString()} · ${d.toISOString().slice(0, 16).replace('T', ' ')} UTC">${UI.agoText(iso)}${withClock ? ` <span class="muted">(${UI.hm(iso)})</span>` : ''}</time>`; };
  /** "7m" / "1h" / "3d" (the fake data's short ages) → "7 min ago" */
  UI.ageText = (a) => { const m = String(a).match(/^(\d+)\s*([smhd])$/); if (!m) return a; return m[2] === 's' ? 'just now' : m[2] === 'm' ? `${m[1]} min ago` : m[2] === 'h' ? `${m[1]} h ago` : `${m[1]} days ago`; };
  /** Durations in words with units: "12 min", "7 min 40 s", "26 s". */
  UI.durText = (ms) => { if (!ms) return '—'; const s = Math.round(ms / 1000); if (s < 60) return `${Math.max(1, s)} s`; const m = Math.floor(s / 60), r = s % 60; return m < 10 && r ? `${m} min ${r} s` : m < 60 ? `${m} min` : `${Math.floor(m / 60)} h ${m % 60} min`; };
  UI.runDur = (r) => (r.finished ? UI.durText(new Date(r.finished) - new Date(r.started)) : '…');
  /** A run's plain headline (what it is doing or did, in words); the workflow name and ids follow in the detail line. */
  const RUN_WHAT = { 'ci-fixer': ['Fixing a failing check', 'Fixed a failing check', 'Couldn’t fix a failing check'], 'pr-review': ['Reviewing a pull request', 'Reviewed a pull request', 'Couldn’t review a pull request'], 'issue-triage': ['Triaging a new issue', 'Triaged a new issue', 'Couldn’t triage an issue'], 'dep-bump': ['Bumping dependencies', 'Bumped dependencies', 'Couldn’t bump dependencies'], 'release-notes': ['Drafting release notes', 'Drafted release notes', 'Couldn’t draft release notes'], 'incident-intake': ['Taking in an incident', 'Took in an incident', 'Couldn’t take in an incident'], 'assess-and-post': ['Assessing and posting', 'Assessed and posted', 'Couldn’t assess and post'] };
  UI.runTitle = (r) => { const bot = String(r.workflow || '').startsWith('bot:') ? r.workflow.slice(4) : null; const i = r.status === 'run' ? 0 : ['fail', 'stopped'].includes(r.status) ? 2 : 1;
    if (r.status === 'wait') return `Waiting for your answer${bot ? ` (${bot})` : ''}`; if (r.status === 'idle') return 'Parked: on hold';
    if (bot) return [`${bot} is working`, `${bot} finished a task`, `${bot}’s task failed`][i]; const w = RUN_WHAT[r.workflow]; return w ? w[i] : [`Running ${r.workflow}`, `${r.workflow} finished`, `${r.workflow} failed`][i]; };
  /** The words for a run's state, used in tables and stories. */
  UI.runWord = (s) => ({ ok: 'Passed', run: 'Running', fail: 'Failed', wait: 'Waiting on you', idle: 'Parked', stopped: 'Stopped' }[s] || s);
  UI.runS = (s) => (s === 'stopped' ? 'fail' : s);
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
  UI.render = (opts = {}) => {
    const keep = opts.keepScroll ? $('#main').scrollTop : 0;
    cleanup.forEach((f) => f()); cleanup = [];
    UI.closeMenu && UI.closeMenu();
    const path = (location.hash.replace(/^#\/?/, '') || 'overview').split('?')[0];
    for (const r of routes) {
      const mt = path.match(r.re);
      if (!mt) continue;
      const params = {}; r.keys.forEach((k, i) => (params[k] = mt[i + 1] ? decodeURIComponent(mt[i + 1]) : undefined));
      const main = $('#main');
      const out = r.fn(params);
      main.classList.toggle("still", !!opts.keepScroll); main.innerHTML = String(out);
      main.scrollTop = keep;
      UI.params = params;
      UI.setNav(r.opts.nav || path.split('/')[0]);
      if (r.opts.after) r.opts.after(params, main);
      if (typeof out === 'object' && out && out.after) out.after(main);
      UI.afterRender.forEach((f) => f(main));
      return;
    }
    $('#main').innerHTML = `<div class="page"><div class="empty"><span class="serif">No such page</span><a class="btn" href="#/overview">Back to overview</a></div></div>`;
  };
  UI.afterRender = [];
  UI.go = (h) => { if (location.hash === h) UI.render(); else location.hash = h; };
  /** Re-render the current route in place (after a state change), keeping scroll. */
  UI.rerender = () => { UI.refreshChrome(); UI.render({ keepScroll: true }); };
  // An editor with unsaved changes can guard navigation: UI.guard() returns a message, or nothing.
  let lastHash = location.hash, ignoreHash = false;
  window.addEventListener('hashchange', () => {
    if (ignoreHash) { ignoreHash = false; return; }
    const msg = UI.guard && UI.guard();
    if (msg) {
      const target = location.hash; ignoreHash = true; location.replace(lastHash || '#/overview');
      UI.confirm({ title: 'Discard unsaved changes?', body: msg, ok: 'Discard and leave', danger: true }).then((ok) => { if (ok) { if (UI.guardClear) UI.guardClear(); location.hash = target; } });
      return;
    }
    lastHash = location.hash;
    document.querySelectorAll('.drawer-scrim').forEach((d) => d.remove()); UI.closeModal(); UI.render();
  });
  window.addEventListener('beforeunload', (e) => { if (UI.guard && UI.guard()) { e.preventDefault(); e.returnValue = ''; } });

  /* ---------- nav ---------- */
  const navGroups = [
    ['Now', [['overview', 'Overview', 'overview'], ['live', 'Live', 'live', () => D.runs.filter((r) => r.status === 'run').length], ['attention', 'Needs you', 'attention', () => D.attention.length, true]]],
    ['Work', [['runs', 'Run history', 'runs'], ['workflows', 'Workflows', 'workflows'], ['bots', 'Bots', 'bots'], ['browser', 'Browsers', 'browser', () => D.browsers.length]]],
    ['System', [['config', 'Config', 'config'], ['secrets', 'Secrets & vars', 'secrets'], ['extensions', 'Packs & plugins', 'extensions'], ['connectors', 'Connectors & hosts', 'connectors'], ['updates', 'Updates', 'updates', () => (D.daemon.update.available ? 1 : 0)], ['logs', 'Logs & audit', 'logs'], ['security', 'Access', 'shield']]],
  ];
  UI.navGroups = navGroups;
  UI.navHTML = () => navGroups.map(([g, items]) => `<div class="nav-group"><span class="caps">${g}</span><nav class="nav">${items.map(([k, l, ic, c, hot]) => {
    const n = c ? c() : null;
    return `<a href="#/${k}" data-nav="${k}">${UI.icon(ic)}<span class="lbl">${l}</span>${n ? UI.count(n, { hot }) : ''}</a>`;
  }).join('')}</nav></div>`).join('');
  // a page opened from Approvals (or another nav section) keeps that section highlighted, and its Back returns there
  document.addEventListener('click', (e) => { const a = e.target.closest && e.target.closest('a[href^="#/"]'); if (!a) return; const from = location.hash.replace(/^#\/?/, '').split(/[/?]/)[0]; const to = a.getAttribute('href'); if (['attention', 'overview'].includes(from) && /^#\/bots\/[^/]+\/proposals\//.test(to)) UI.navOrigin = { hash: to, nav: from === 'overview' ? 'overview' : 'attention', back: '#/' + from }; else if (!a.closest('.tabbar, .rail')) UI.navOrigin = UI.navOrigin && UI.navOrigin.hash === to ? UI.navOrigin : null; else UI.navOrigin = null; }, true);
  UI.setNav = (k) => {
    if (UI.navOrigin && location.hash === UI.navOrigin.hash) { k = UI.navOrigin.nav; UI.backHref = UI.navOrigin.back; } else UI.backHref = null;
    $$('[data-nav]').forEach((a) => { const on = a.dataset.nav === k; a.classList.toggle('on', on); if (on && location.hash.replace(/^#\/?/, '') === a.dataset.nav) a.setAttribute('aria-current', 'page'); else a.removeAttribute('aria-current'); });
  };

  /* ---------- modal / toast / tooltip ---------- */
  UI.modal = (html, opts = {}) => {
    UI.closeModal();
    const s = document.createElement('div');
    s.className = 'scrim'; s.id = 'scrim';
    s.innerHTML = `<div class="modal ${opts.wide ? 'wide' : ''}" role="dialog" aria-modal="true"><span class="sheet-grip only-phone" aria-hidden="true"></span>${html}</div>`;
    s.addEventListener('click', (e) => { if (e.target === s && !opts.sticky) UI.dismissModal(); });
    document.body.appendChild(s);
    $$('[data-close]', s).forEach((b) => b.addEventListener('click', () => UI.dismissModal()));
    return s;
  };
  UI.closeModal = () => { const s = $('#scrim'); if (s) s.remove(); };
  /** A way out the user asked for (close, Cancel, Esc, the scrim): asks first if there are unsaved changes. */
  const guarded = (el, rm) => { if (el._guard && el._guard()) { UI.confirm({ title: 'Discard your changes?', body: 'What you typed in this form isn\'t saved.', ok: 'Discard', cancel: 'Keep editing', danger: true }).then((ok) => { if (ok) rm(); }); return; } rm(); };
  UI.dismissModal = () => { const s = $('#scrim'); if (!s) return false; guarded(s, () => s.remove()); return true; };
  UI.dismissDrawer = () => { const all = document.querySelectorAll('.drawer-scrim'); if (!all.length) return false; const d = all[all.length - 1]; guarded(d, () => d.remove()); return true; };
  /** A create-in-place drawer: layered over the current page or modal, which keeps its unsaved state. */
  UI.openDrawer = (html) => {
    const d = document.createElement('div'); d.className = 'drawer-scrim';
    d.innerHTML = `<aside class="cdrawer" role="dialog" aria-modal="true">${html}</aside>`;
    document.body.appendChild(d);
    d.querySelectorAll('[data-close]').forEach((b) => b.addEventListener('click', (e) => { e.stopPropagation(); guarded(d, () => d.remove()); }));
    return d;
  };
  UI.closeDrawer = () => { const all = document.querySelectorAll('.drawer-scrim'); if (all.length) { all[all.length - 1].remove(); return true; } return false; };
  /** A toast: an event, past tense and specific. 6 seconds; errors stay until dismissed. s: ok | fail | wait | run | ember. */
  UI.toast = (title, sub, s = 'ember') => {
    let box = $('.toasts'); if (!box) { box = document.createElement('div'); box.className = 'toasts'; box.setAttribute('aria-live', 'polite'); document.body.appendChild(box); }
    const t = document.createElement('div'); t.className = 'toast'; t.dataset.s = s === 'ok' ? 'success' : s; t.setAttribute('role', s === 'fail' ? 'alert' : 'status');
    const g = { ok: 'success', fail: 'danger', wait: 'warning', run: 'running', ember: 'needs-you', idle: 'neutral' }[s] || 'info';
    t.innerHTML = `<span class="st" data-s="${s === 'ok' ? 'success' : s}"><i>${UI.glyph(g)}</i></span><div><b>${title}</b>${sub ? `<p>${sub}</p>` : ''}</div>${s === 'fail' ? `<button class="icon-btn toast-x" aria-label="Dismiss">${UI.icon('x')}</button>` : '<span></span>'}`;
    const x = t.querySelector('.toast-x'); if (x) x.addEventListener('click', () => t.remove());
    box.appendChild(t); if (s !== 'fail') setTimeout(() => t.remove(), 6000); else setTimeout(() => t.remove(), 20000);
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
  /** The authenticator this device would use (recorded with the decision, not assumed). */
  UI.authenticator = () => (innerWidth <= 720 ? 'phone · Face ID' : 'laptop · Touch ID');
  UI.approve = (p, decision = 'approve', onDone) => {
    const deny = decision === 'deny', dev = UI.authenticator();
    const s = UI.modal(`
      <div class="modal-head"><div><div class="caps" style="margin-bottom:6px">${p.bot} · ${p.id} · case ${p.case}</div><h2>${deny ? 'Deny' : 'Approve'}: ${UI.esc(p.title || p.summary)}</h2></div><button class="icon-btn" data-close aria-label="Close">${UI.icon('x')}</button></div>
      <div class="modal-body stack">
        <div class="seal-words">${deny ? 'You\'re denying' : 'You\'re approving'} <b>${UI.esc(p.title || p.summary)}</b>, proposed by <b>${p.bot}</b>.${p.impact ? ` <span class="dim">${UI.esc(p.impact)}</span>` : ''}</div>
        ${UI.requesterBanner ? UI.requesterBanner(p) : ''}
        ${p.kind === 'terraform' ? `<div class="kv small"><dt>action</dt><dd class="mono">terraform apply · ${p.tf.repo} · ${p.tf.dir} · ${p.tf.workspace}</dd><dt>plan</dt><dd class="mono">+${p.tf.add} ~${p.tf.change} -${p.tf.destroy} · <span style="color:var(--color-status-warning-fg)">replaces ${p.tf.replaces.join(', ')}</span></dd></div>` : UI.payloadHTML ? UI.payloadHTML(p) : ''}
        ${deny ? `<div class="field"><label for="seal-reason">Tell the bot why <span class="opt">optional</span></label><textarea class="textarea" id="seal-reason" rows="2" placeholder="e.g. Not in #incidents; post it to #finance instead."></textarea></div>` : ''}
        <details class="tech"><summary>The exact payload the passkey signs</summary><div class="digest">{ bot: <b>${p.bot}</b>, proposal: <b>${p.id}</b>, decision: <b>${decision}</b>, action_sha256: <b>${p.digest.replace('sha256:', '')}</b>, nonce: <b>7Qx2…f9</b> }</div></details>
        <div class="seal" id="seal">
          <div class="ring">${UI.icon('passkey', 'key')}<svg class="arc" viewBox="0 0 108 108"><circle class="track" cx="54" cy="54" r="52"/><circle class="fill" cx="54" cy="54" r="53"/></svg></div>
          <div id="seal-msg"><b>Confirm with your passkey</b><div class="muted small">User verification required · this assertion covers only this action</div></div>
        </div>
      </div>
      <div class="modal-foot"><span class="proto-note" style="margin-right:auto">authenticator simulated</span><button class="btn ghost" data-close>Cancel</button><button class="btn ${deny ? 'danger' : 'primary'}" id="seal-go">${UI.icon('passkey')}${deny ? 'Deny with passkey' : 'Approve with passkey'}</button></div>`, { sticky: true });
    $('#seal-go', s).addEventListener('click', (e) => {
      const seal = $('#seal', s); e.currentTarget.disabled = true; const reason = deny ? ($('#seal-reason', s) || {}).value || '' : '';
      seal.classList.add('signing'); $('#seal-msg', s).innerHTML = `<b>Confirm on your ${dev.split(' · ')[0]}…</b><div class="muted small">navigator.credentials.get · rpId ops-box.tail0000.ts.net</div>`;
      setTimeout(() => {
        seal.classList.remove('signing'); seal.classList.add(deny ? 'denied' : 'done');
        $('#seal-msg', s).innerHTML = deny ? '<b>Denied</b><div class="muted small">Signed decision recorded · the bot is told</div>' : `<b>Approved · executing</b><div class="muted small">Verified against operator <span class="mono">ops</span> · credential “${dev}”</div>`;
        p.status = deny ? 'denied' : 'approved'; p.approver = `ops (passkey · ${dev.split(' · ')[0]})`; if (reason.trim()) p.reason = reason.trim();
        if (deny && reason.trim()) (D.chats[p.bot] = D.chats[p.bot] || []).push({ who: 'op', at: new Date().toISOString(), text: `Denied ${p.id}: ${reason.trim()}` });
        setTimeout(() => { UI.closeModal(); UI.toast(deny ? `Denied ${p.id}` : `Approved ${p.id}`, deny ? (reason.trim() ? 'your reason went to the bot' : 'decision signed') : (p.kind === 'terraform' ? 'conductor re-checks the plan hash, HEAD, and a clean tree, then applies' : 'the gate re-runs exactly the signed digest'), deny ? 'fail' : 'ok'); D.attention = D.attention.filter((a) => !a.href.includes(p.id) && !(a.sub || '').includes(p.id)); D.audit.unshift({ ts: new Date().toISOString(), event: 'bot_approval', who: 'operator:ops', detail: `${p.id} · ${decision} · ${dev}${reason.trim() ? ' · reason given' : ''}`, run: '' }); if (onDone) onDone(); }, UI.fast ? 40 : 900);
      }, UI.fast ? 40 : 1600);
    });
  };

  /* ---------- command palette ---------- */
  // UI.palette (⌘K) and the single-key shortcuts live in js/palette.js
  document.addEventListener('keydown', (e) => { if ((e.metaKey || e.ctrlKey) && e.key === 'k') { e.preventDefault(); UI.palette(); } if (e.key === 'Escape' && !e.defaultPrevented) { const pop = document.querySelector('#pop-menu'); if (pop) { pop.remove(); return; } if (!UI.dismissDrawer()) UI.dismissModal(); } });

  /* ---------- theme ---------- */
  /* The head script applied the theme before paint; these keep it. UI.theme(t) is an explicit choice (saved);
     UI.theme('auto') goes back to following the OS. */
  UI.themeIcon = () => { const b = $('#theme-btn'); if (b) b.innerHTML = UI.icon(document.documentElement.dataset.theme === 'dark' ? 'sun' : 'moon'); };
  UI.theme = (t) => { const h = document.documentElement, m = document.querySelector('meta[name="theme-color"]');
    if (t === 'auto') { try { localStorage.removeItem('cd-theme'); } catch (e) {} t = matchMedia('(prefers-color-scheme: dark)').matches ? 'dark' : 'light'; h.dataset.themeSource = 'os'; }
    else { try { localStorage.setItem('cd-theme', t); } catch (e) {} h.dataset.themeSource = 'explicit'; }
    h.dataset.theme = t; if (m) m.content = getComputedStyle(h).getPropertyValue('--color-bg-rail').trim(); UI.themeIcon(); };
  /** Density from the per-browser display preference; phones are always comfortable (44px targets). */
  UI.applyDensity = () => { const want = (window.D && D.prefs && D.prefs.density) || 'comfortable'; document.documentElement.dataset.density = innerWidth <= 720 ? 'comfortable' : want; };
  window.addEventListener('resize', () => UI.applyDensity());
  UI.initTheme = () => { UI.applyDensity(); { const m = document.querySelector('meta[name="theme-color"]'); if (m) m.content = getComputedStyle(document.documentElement).getPropertyValue('--color-bg-rail').trim(); } if (!document.documentElement.dataset.theme) document.documentElement.dataset.theme = 'dark'; UI.themeIcon(); if (document.documentElement.classList.contains('sim-ios') && !document.getElementById('sim-toolbar')) { const o = document.createElement('div'); o.id = 'sim-toolbar'; o.setAttribute('aria-hidden', 'true'); o.innerHTML = '<span></span>'; document.body.appendChild(o); } };

  /* ---------- chrome ---------- */
  UI.refreshChrome = () => {
    const d = D.daemon, need = D.attention.length, running = D.runs.filter((r) => r.status === 'run').length;
    const warn = d.paused || D.connectors.some((c) => c.status !== 'ok');
    // the top bar says how things are in words; detail (uptime, version, agents, the address) is one step down
    const health = d.paused ? ['wait', 'Dispatch paused'] : warn ? ['wait', 'Something needs a look'] : ['ok', 'All systems normal'];
    $('#strip-cells').innerHTML = `
      <div class="spacer"></div>
      <button type="button" class="cell status-btn" data-act="strip-status" aria-label="System status: ${health[1]}" data-tip="${d.paused ? 'Dispatch is paused: no new runs start' : `conductor ${d.version}, up ${d.uptime}`}">${UI.statusMark(health[0] === 'ok' ? 'success' : health[0], `<span class="lbl-long">${health[1]}</span>`)}</button>
      <a class="cell" href="#/safety" data-tip="The agent sandbox is on: bots can't touch files or the network beyond what you allowed. See what each bot may do on its own.<br><span class='mono'>backend ${d.sandbox.backend} · ${d.sandbox.containers} containers</span>">${UI.icon('shield')}<span class="lbl-long">Sandbox on</span></a>
      <a class="cell${d.update.available ? '' : ' quiet'}" href="#/updates" data-tip="conductor ${d.version}${d.update.available ? `; ${d.update.available} is available` : ', up to date'}">${UI.icon('updates')}<span class="lbl-long">${d.update.available ? `Update ready` : d.version}</span></a>
      ${running ? `<a class="cell" href="#/live" data-tip="Runs in progress">${UI.statusMark('run', `<span><b>${running}</b> running</span>`)}</a>` : ''}
      ${D.pending.length ? `<a class="cell staged" href="#/config/apply" data-tip="Config changes staged but not applied">${UI.icon('edit')}<span><b>${D.pending.length}</b><span class="lbl-long"> to review</span></span></a>` : ''}
      ${need ? `<a class="cell alert" href="#/attention" aria-label="${need} need you">${UI.count(need, { hot: true })}<span class="lbl-long">need you</span></a>` : ''}`;
    $('#rail-nav').innerHTML = UI.navHTML();
    $('#tabbar').innerHTML = [['overview', 'Overview', 'overview'], ['live', 'Live', 'live'], ['attention', 'Approvals', 'attention', need], ['bots', 'Bots', 'bots'], ['more', 'More', 'grid']].map(([k, l, ic, c]) => `<a href="#/${k}" data-nav="${k}">${UI.icon(ic)}${l}${c ? UI.count(c, { hot: true }) : ''}</a>`).join('');
    UI.setNav((location.hash.replace(/^#\/?/, '') || 'overview').split('/')[0]);
  };

  UI.boot = () => {
    window.D = D;
    document.body.innerHTML = `
      <div class="shell">
        <header class="strip">
          <a class="brand" href="#/overview" aria-label="conductor: overview">${UI.logo('lockup')}</a>
          <div class="search" role="button" tabindex="0" data-act="palette" aria-label="Search runs, bots and settings">${UI.icon('search')}<span class="lbl">Search runs, bots and settings</span><span class="kbd">⌘K</span></div>
          <div id="strip-cells" style="display:contents"></div>
        </header>
        <aside class="rail">
          <div id="rail-nav"></div>
          <div class="foot">
            <a class="host" href="#/connectors/hosts" data-tip="This box, over Tailscale with TLS<br><span class='mono'>${D.daemon.magicdns}</span>"><span class="dot" data-s="success"></span><span class="who"><b>${D.daemon.host}</b><span id="host-line">Healthy · up ${D.daemon.uptime}</span></span></a>
            <div class="tools">
              <span class="op" data-tip="Signed in as ${D.operator.name}, with a passkey"><span class="avatar">${D.operator.name.slice(0, 2)}</span></span>
              <a class="icon-btn" href="#/glossary" data-tip="Glossary: the words conductor uses" aria-label="Glossary">${UI.icon('info')}</a>
              <button class="icon-btn" id="theme-btn" data-tip="Light or dark" aria-label="Light or dark" data-act="theme"></button>
              <button class="icon-btn" data-act="reset-demo" data-tip="Reset demo data: put the prototype's fake state back" aria-label="Reset demo data">${UI.icon('undo')}</button>
              <a class="icon-btn" href="#/signin" data-tip="Lock (sign out)" aria-label="Lock">${UI.icon('lock')}</a>
            </div>
          </div>
        </aside>
        <main class="main" id="main"></main>
        <nav class="tabbar" id="tabbar"></nav>
      </div>`;
    UI.initTheme();
    UI.loadLogo();
    UI.refreshChrome();
    UI.render();
  };

  /* small shared bits */
  UI.head = (title, opts = {}) => `<div class="page-head"><div>${opts.crumbs ? `<div class="crumbs">${opts.crumbs}</div>` : ''}<h1>${title}${opts.sub ? `<span class="sub">${opts.sub}</span>` : ''}</h1>${opts.lede ? `<p class="lede">${opts.lede}</p>` : ''}</div>${opts.actions ? `<div class="actions">${opts.actions}</div>` : ''}</div>`;
  UI.tabs = (items, on) => `<div class="tabs" role="tablist">${items.map(([k, l, href, c, hot]) => `<a href="${href}" role="tab" class="${k === on ? 'on' : ''}"${k === on ? ' aria-current="page" aria-selected="true"' : ''}>${l}${c != null ? ' ' + UI.count(c, { hot }) : ''}</a>`).join('')}</div>`;
  UI.bind = (root, sel, ev, fn) => $$(sel, root).forEach((el) => el.addEventListener(ev, (e) => fn(e, el)));

  /* =================================================================
   * Interaction helpers. Controls declare data-act="name"; one delegated
   * listener dispatches to UI.acts[name](el, event). Re-rendered fragments
   * and modals need no re-binding.
   * ================================================================= */
  UI.acts = {};
  // one handler per action name: a second registration is a bug (two screens fighting over one name)
  UI.act = (name, fn) => { if (UI.acts[name]) console.error(`duplicate action handler: ${name}`); UI.acts[name] = fn; };
  const disabledEl = (el) => el.disabled || el.getAttribute('aria-disabled') === 'true';
  document.addEventListener('click', (e) => {
    const el = e.target.closest('[data-act]');
    if (!el) return;
    if (disabledEl(el)) { e.preventDefault(); e.stopPropagation(); UI.flashReason(el); return; }
    const fn = UI.acts[el.dataset.act];
    if (fn) { e.preventDefault(); e.stopPropagation(); fn(el, e); }
  });
  document.addEventListener('click', (e) => { const a = e.target.closest('a[aria-disabled="true"]'); if (a && !a.dataset.act) { e.preventDefault(); UI.flashReason(a); } }, true);
  document.addEventListener('keydown', (e) => { if ((e.key === 'Enter' || e.key === ' ') && e.target.matches('[data-act][role="button"]')) { e.preventDefault(); e.target.click(); } });
  ['change', 'input'].forEach((type) => document.addEventListener(type, (e) => {
    const el = e.target.closest(`[data-on-${type}]`);
    if (el && UI.acts[el.dataset['on' + type[0].toUpperCase() + type.slice(1)]]) UI.acts[el.dataset['on' + type[0].toUpperCase() + type.slice(1)]](el, e);
  }));
  document.addEventListener('toggle', (e) => { const el = e.target; if (el.dataset && el.dataset.onToggle && UI.acts[el.dataset.onToggle]) UI.acts[el.dataset.onToggle](el, e); }, true); // toggle doesn't bubble
  /** On touch there's no hover: tapping a disabled control shows its reason. */
  UI.flashReason = (el) => {
    const r = el.dataset.reason || el.dataset.tip; if (!r) return;
    const t = document.createElement('div'); t.className = 'tooltip reason-flash'; t.innerHTML = r; document.body.appendChild(t);
    const b = el.getBoundingClientRect();
    t.style.left = Math.min(innerWidth - t.offsetWidth - 8, Math.max(8, b.left + b.width / 2 - t.offsetWidth / 2)) + 'px';
    t.style.top = (b.top - t.offsetHeight - 8 < 0 ? b.bottom + 8 : b.top - t.offsetHeight - 8) + 'px';
    setTimeout(() => t.remove(), 2600);
  };
  /** Attributes for a control that can't be used right now, and why. */
  UI.off = (reason) => `aria-disabled="true" data-reason="${UI.esc(reason)}" data-tip="${UI.esc(reason)}"`;
  /* ---- shared components (css/components.css) ---- */
  /** A selectable choice card: icon left, title over description. o: { act, attrs, icon, title, desc, on, off (reason), kc } */
  UI.choice = (o) => `<button type="button" class="choice${o.on ? ' on' : ''}" aria-pressed="${!!o.on}" ${o.off ? UI.off(o.off) : `data-act="${o.act}" ${o.attrs || ''}`}${o.kc ? ` style="--kc:${o.kc}"` : ''}><span class="choice-ic">${UI.icon(o.icon)}</span><span class="choice-body"><span class="choice-title">${o.title}</span><span class="choice-desc">${o.desc}</span>${o.off ? `<span class="choice-note">${UI.esc(o.off)}</span>` : ''}</span><span class="choice-check" aria-hidden="true">${UI.icon('check')}</span></button>`;
  /** The choice made in an earlier step, carried forward, with a way back. */
  UI.chosen = (o) => `<div class="choice-chosen"${o.kc ? ` style="--kc:${o.kc}"` : ''}><span class="choice-ic">${UI.icon(o.icon)}</span><div class="grow"><span class="caps">${o.caps || 'method'}</span><b>${o.title}</b></div>${o.act ? `<button class="btn sm ghost" data-act="${o.act}">Change</button>` : ''}</div>`;
  /** A wizard stepper: steps = labels, at = current index (done before it). */
  UI.stepper = (steps, at, bad) => `<div class="stepper apply-steps" role="list">${steps.map((x, i) => `<div class="as ${i < at ? 'done' : i === at ? (bad ? 'cur bad' : 'cur') : ''}" role="listitem"${i === at ? ' aria-current="step"' : ''}><b>${i < at ? '✓' : i + 1}</b>${x}</div>`).join('')}</div>`;
  /** Segmented control: items [[value, label]]; act receives el.dataset.v. */
  UI.seg = (items, value, act, extra = '') => `<div class="seg" role="group" ${extra}>${items.map(([v, l, dis]) => `<button type="button" data-v="${UI.esc(v)}" ${dis ? UI.off(dis) : `data-act="${act}"`} aria-pressed="${String(v) === String(value)}" class="${String(v) === String(value) ? 'on' : ''}">${l}</button>`).join('')}</div>`;
  UI.switchBtn = (on, act, attrs = '') => `<button type="button" role="switch" class="switch${on ? ' on' : ''}" aria-checked="${!!on}" data-act="${act}" ${attrs}></button>`;
  UI.act('strip-status', (b) => { const d = D.daemon, running = D.runs.filter((r) => r.status === 'run').length;
    UI.menu(b, [[`conductor ${d.version}, ${d.paused ? 'paused' : 'up'} ${d.uptime}${d.update.available ? ` · ${d.update.available} ready` : ''}`, 'server', 'strip-go', 'data-href="#/updates"'], [`Sandbox enforcing: what bots can do`, 'shield', 'strip-go', 'data-href="#/safety"'], [`Agents healthy (${d.paseo.agents})`, 'zap', 'strip-go', 'data-href="#/connectors/runtimes"'], [`${running} running now`, 'live', 'strip-go', 'data-href="#/live"'], ...(D.pending.length ? [[`${D.pending.length} change${D.pending.length > 1 ? 's' : ''} to review`, 'edit', 'strip-go', 'data-href="#/config/apply"']] : []), ['-'], [`${d.magicdns} · TLS`, 'lock', 'strip-go', 'data-href="#/security"']]); });
  UI.act('strip-go', (b) => { UI.closeMenu(); UI.go(b.dataset.href); });
  UI.act('theme', () => UI.theme(document.documentElement.dataset.theme === 'dark' ? 'light' : 'dark'));
  UI.act('palette', () => UI.palette());
  UI.act('close-modal', () => UI.closeModal());
  UI.act('reset-demo', () => UI.confirm({ title: 'Reset demo data?', body: 'Puts the prototype\'s fake state back: staged and applied changes, chat, cases, workflows, everything you added or removed. Your theme stays.', ok: 'Reset', danger: true }).then((ok) => { if (!ok) return; S.reset(); UI.refreshChrome(); UI.render(); UI.toast('Demo data reset', 'fresh fake state', 'ok'); }));

  /* ---------- confirm / step-up ---------- */
  // over an open modal or drawer, dialogs layer instead of replacing it (its unsaved state stays)
  const layer = (html, opts) => { if (document.querySelector('#scrim, .drawer-scrim')) { const d = UI.openDrawer(html); d.classList.add('layered'); return [d, () => d.remove()]; } return [UI.modal(html, opts), UI.closeModal]; };
  UI.confirm = ({ title, body = '', ok = 'Confirm', cancel = 'Cancel', danger = false, caps = '' }) => new Promise((res) => {
    const [s, close] = layer(`<div class="modal-head"><div>${caps ? `<div class="caps">${caps}</div>` : ''}<h2>${title}</h2></div><button class="icon-btn" data-close aria-label="Close">${UI.icon('x')}</button></div>
      <div class="modal-body small dim">${body}</div>
      <div class="modal-foot"><button class="btn ghost" data-close id="cf-cancel">${cancel}</button><button class="btn ${danger ? 'danger' : 'primary'}" id="cf-ok">${ok}</button></div>`, {});
    let done = false;
    if (danger) setTimeout(() => { const c = $('#cf-cancel', s); if (c) c.focus(); }, 20); // destructive: Cancel has the focus
    $('#cf-ok', s).addEventListener('click', () => { done = true; close(); res(true); });
    const obs = new MutationObserver(() => { if (!document.body.contains(s)) { obs.disconnect(); if (!done) res(false); } });
    obs.observe(document.body, { childList: true });
  });
  /** A step-up passkey assertion over `what` (simulated authenticator). */
  UI.stepup = ({ title, what, purpose = 'step-up', ok = 'Confirm with passkey', danger = false }) => new Promise((res) => {
    const [s, close] = layer(`<div class="modal-head"><div><div class="caps">${purpose} · fresh passkey assertion</div><h2>${title}</h2></div><button class="icon-btn" data-close aria-label="Close">${UI.icon('x')}</button></div>
      <div class="modal-body"><div class="panel inset" style="padding:10px 12px"><div class="caps" style="margin-bottom:4px">You are signing</div><div class="digest">${what}</div></div>
        <div class="seal" id="su-seal"><div class="ring">${UI.icon('passkey', 'key')}<svg class="arc" viewBox="0 0 108 108"><circle class="track" cx="54" cy="54" r="52"/><circle class="fill" cx="54" cy="54" r="53"/></svg></div>
        <div id="su-msg"><b>Confirm with your passkey</b><div class="muted small">User verification required · bound to this request's digest · single use, 2 min</div></div></div></div>
      <div class="modal-foot"><span class="proto-note" style="margin-right:auto">authenticator simulated</span><button class="btn ghost" data-close>Cancel</button><button class="btn ${danger ? 'danger' : 'primary'}" id="su-go">${UI.icon('passkey')}${ok}</button></div>`, { sticky: true });
    let done = false;
    $('#su-go', s).addEventListener('click', (e) => {
      e.currentTarget.disabled = true; const seal = $('#su-seal', s); seal.classList.add('signing');
      $('#su-msg', s).innerHTML = '<b>Touch your security key…</b><div class="muted small">navigator.credentials.get · rpId ops-box.tail0000.ts.net</div>';
      setTimeout(() => { seal.classList.remove('signing'); seal.classList.add('done'); $('#su-msg', s).innerHTML = '<b>Verified</b><div class="muted small">operator ops · credential laptop</div>'; setTimeout(() => { done = true; close(); res(true); }, UI.fast ? 50 : 450); }, UI.fast ? 80 : 900);
    });
    const obs = new MutationObserver(() => { if (!document.body.contains(s)) { obs.disconnect(); if (!done) res(false); } });
    obs.observe(document.body, { childList: true });
  });

  /* ---------- popover menu ---------- */
  UI.closeMenu = () => { const m = $('#pop-menu'); if (m) m.remove(); };
  /** items: [[label, icon, act, attrs?]] — attrs may carry data-* for the act, or UI.off(reason). */
  UI.menu = (anchor, items) => {
    UI.closeMenu();
    const m = document.createElement('div'); m.id = 'pop-menu'; m.className = 'menu'; m.setAttribute('role', 'menu');
    m.innerHTML = items.map(([l, ic, act, attrs = '']) => (l === '-' ? '<hr>' : `<button type="button" role="menuitem" ${attrs.includes('aria-disabled') ? '' : `data-act="${act}"`} ${attrs}>${UI.icon(ic)}${l}</button>`)).join('');
    document.body.appendChild(m);
    const b = anchor.getBoundingClientRect();
    const phone = innerWidth <= 720;
    if (phone) m.classList.add('as-sheet');
    else { m.style.top = Math.min(innerHeight - m.offsetHeight - 8, b.bottom + 4) + 'px'; m.style.left = Math.max(8, Math.min(innerWidth - m.offsetWidth - 8, b.right - m.offsetWidth)) + 'px'; }
    setTimeout(() => document.addEventListener('click', function off(e) { if (!m.contains(e.target)) { UI.closeMenu(); document.removeEventListener('click', off, true); } }, true));
    m.addEventListener('click', (e) => { if (e.target.closest('[data-act]')) setTimeout(UI.closeMenu); });
  };

  /* ---------- generated forms ----------
   * UI.form({ caps, title, intro, fields, values, submit, onSubmit, wide, side })
   * fields: { k, label, type, req, options, help, ph, def, show(values), validate(v, values) }
   * types: string text number bool enum seg list secret duration map code host select-multi
   * onSubmit(values) returns false (keep open) or a string error to show. */
  const fieldId = (k, pre = 'f-') => pre + k.replace(/[^\w-]/g, '_');
  /** Create-in-place: an entity picker's first option opens a drawer that stages the new thing. */
  UI.createOpt = (fd) => (fd.create ? `<option value="__new__">+ ${UI.esc(fd.createLabel || 'New ' + fd.create)}…</option>` : '');
  UI.pickerAttrs = (fd) => (fd.create ? `data-picker="${fd.create}" data-ctx='${UI.esc(JSON.stringify(fd.createCtx || {}))}' data-ret="${fd.ret || 'name'}"` : '');
  UI.fieldHTML = (fd, v, pre = 'f-') => {
    const id = fieldId(fd.k, pre), lbl = fd.label || fd.k.split('.').pop().replace(/_/g, ' ');
    const req = fd.req ? '<span class="req" aria-label="required">*</span>' : '<span class="opt">optional</span>';
    const help = fd.help ? `<div class="hint">${fd.help}</div>` : '';
    const val = v == null ? '' : v;
    let input;
    switch (fd.type) {
      case 'text': case 'code': input = `<textarea class="textarea${fd.type === 'code' ? ' mono' : ''}" id="${id}" data-k="${fd.k}" rows="${fd.rows || 3}" placeholder="${UI.esc(fd.ph || '')}">${UI.esc(val)}</textarea>`; break;
      case 'number': input = `<input class="input mono" id="${id}" data-k="${fd.k}" inputmode="decimal" value="${UI.esc(val)}" placeholder="${UI.esc(fd.ph || '')}">`; break;
      case 'bool': input = `<label class="row small" style="min-height:30px"><input type="checkbox" class="cbx" id="${id}" data-k="${fd.k}" ${val ? 'checked' : ''}> ${fd.on || 'on'}</label>`; break;
      case 'enum': case 'host': {
        if (fd.type === 'host') fd = Object.assign({ create: 'host', createLabel: 'New host' }, fd);
        const opts = fd.type === 'host' ? [''].concat(S.view().hosts.map((h) => h.name)) : (fd.req ? [] : ['']).concat(fd.options || []);
        input = `<select class="select mono" id="${id}" data-k="${fd.k}" ${UI.pickerAttrs(fd)}>${UI.createOpt(fd)}${opts.map((o) => { const [ov, ol] = Array.isArray(o) ? o : [o, o === '' ? '— none —' : o]; return `<option value="${UI.esc(ov)}"${String(ov) === String(val === '' && fd.def != null && fd.type !== 'host' ? fd.def : val) ? ' selected' : ''}>${UI.esc(ol)}</option>`; }).join('')}</select>`; break;
      }
      case 'seg': input = `<div class="seg" role="group" data-segk="${fd.k}">${fd.options.map((o) => { const [ov, ol, dis] = Array.isArray(o) ? o : [o, o]; const on = String(ov) === String(val === '' ? fd.def : val); return `<button type="button" data-v="${UI.esc(ov)}" ${dis ? UI.off(dis) : 'data-act="form-seg"'} aria-pressed="${on}" class="${on ? 'on' : ''}">${ol}</button>`; }).join('')}</div><input type="hidden" id="${id}" data-k="${fd.k}" value="${UI.esc(val === '' ? fd.def ?? '' : val)}">`; break;
      case 'list': case 'select-multi': {
        const items = Array.isArray(val) ? val : val ? String(val).split(/,\s*/) : [];
        input = `<div class="listed" data-listk="${fd.k}" id="${id}">${items.map((x) => `<span class="chip-x"><span class="mono">${UI.esc(x)}</span><button type="button" data-act="list-del" aria-label="Remove ${UI.esc(x)}">${UI.icon('x')}</button></span>`).join('')}
          ${fd.type === 'select-multi' ? `<select class="select mono list-add-sel" data-on-change="list-add-sel" ${UI.pickerAttrs(fd)} aria-label="Add ${UI.esc(lbl)}"><option value="">+ add…</option>${UI.createOpt(fd)}${(fd.options || []).map((o) => (Array.isArray(o) ? o : [o, o])).filter(([o]) => !items.includes(o)).map(([o, l]) => `<option value="${UI.esc(o)}">${UI.esc(l)}</option>`).join('')}</select>` : `<input class="input mono list-add" placeholder="${UI.esc(fd.ph || 'add, then Enter')}" data-k-list="${fd.k}"><button type="button" class="btn sm" data-act="list-add">${UI.icon('plus')}Add</button>`}</div>`; break;
      }
      case 'map': {
        const ents = Object.entries(val || {});
        input = `<div class="kvedit" data-mapk="${fd.k}" id="${id}">${ents.concat([['', '']]).map(([a, b], i) => `<div class="kvrow"><input class="input mono" placeholder="key" value="${UI.esc(a)}" data-mk="k"><input class="input mono" placeholder="value" value="${UI.esc(typeof b === 'object' ? JSON.stringify(b) : b)}" data-mk="v">${i < ents.length ? `<button type="button" class="icon-btn" data-act="map-del" aria-label="Remove row">${UI.icon('trash')}</button>` : '<span style="width:30px"></span>'}</div>`).join('')}</div>`; break;
      }
      case 'value': case 'secret': input = UI.VS.html(fd.type === 'secret' ? Object.assign({ sensitive: true }, fd) : fd, val, id); break;
      case 'password': input = `<input class="input mono" type="password" autocomplete="new-password" id="${id}" data-k="${fd.k}" value="" placeholder="${UI.esc(fd.ph || '')}">`; break;
      default: input = `<input class="input${fd.mono === false ? '' : ' mono'}" id="${id}" data-k="${fd.k}" value="${UI.esc(val)}" placeholder="${UI.esc(fd.ph || (fd.type === 'duration' ? 'e.g. 30m' : ''))}" ${fd.readonly ? 'readonly' : ''}>`;
    }
    return `<div class="field${fd.type === 'bool' ? ' boolf' : ''}" data-field="${fd.k}" ${fd.show ? 'data-cond="1"' : ''}><label for="${id}">${UI.esc(lbl)} ${req}</label>${input}${help}<div class="ferr" role="alert"></div></div>`;
  };
  UI.readField = (root, fd) => {
    const id = fieldId(fd.k, (root.closest && root.closest('[data-pre]') ? root.closest('[data-pre]').dataset.pre : '') || 'f-');
    if (fd.type === 'list' || fd.type === 'select-multi') return $$(`[data-listk="${fd.k}"] .chip-x .mono`, root).map((x) => x.textContent);
    if (fd.type === 'map') { const o = {}; $$(`[data-mapk="${fd.k}"] .kvrow`, root).forEach((r) => { const k = $('[data-mk=k]', r).value.trim(), v = $('[data-mk=v]', r).value.trim(); if (k) o[k] = /^-?\d+(\.\d+)?$/.test(v) ? Number(v) : v === 'true' ? true : v === 'false' ? false : v; }); return o; }
    const el = root.querySelector('#' + id); if (!el) return undefined;
    if (fd.type === 'bool') return el.checked;
    const v = el.value.trim();
    if (fd.type === 'number') return v === '' ? undefined : /^-?\d+(\.\d+)?$/.test(v) ? Number(v) : v;
    return v;
  };
  UI.formValues = (root, fields) => { const o = {}; fields.forEach((fd) => { const v = UI.readField(root, fd); if (v !== undefined && v !== '' && !(Array.isArray(v) && !v.length && !fd.keepEmpty) && !(typeof v === 'object' && !Array.isArray(v) && !Object.keys(v).length)) o[fd.k] = v; }); return o; };
  UI.checkFields = (root, fields, vals) => {
    let bad = 0;
    fields.forEach((fd) => {
      const box = root.querySelector(`[data-field="${fd.k}"]`); if (!box || box.classList.contains('hide')) return;
      const v = vals[fd.k]; let err = '';
      const vsEl = (fd.type === 'value' || fd.type === 'secret') && box.querySelector('.vs');
      if (vsEl && UI.VS.blocked(fd.type === 'secret' || fd.sensitive, v)) err = 'A sensitive value can\'t come from a setting.';
      else if (vsEl && fd.req && !v && (vsEl.dataset.pending === '1' || box.querySelector('input[type=hidden][data-set]'))) err = '';
      else if (fd.req && (v === undefined || v === '' || (Array.isArray(v) && !v.length))) err = 'Required.';
      else if (v !== undefined && fd.type === 'number' && typeof v !== 'number' && !/^\{\{/.test(v)) err = 'A number, or a {{ reference }}.';
      else if (v && fd.type === 'duration' && !/^\d+(ms|s|m|h|d)$/.test(v)) err = 'A duration such as 30s, 10m or 2h.';
      else if (fd.pattern && v && !fd.pattern.test(v)) err = fd.patternMsg || 'Invalid value.';
      else if (fd.validate) err = fd.validate(v, vals) || '';
      box.classList.toggle('bad', !!err); box.querySelector('.ferr').textContent = err; if (err) bad++;
    });
    return bad === 0;
  };
  UI.applyConds = (root, fields) => { const vals = UI.formValues(root, fields); fields.forEach((fd) => { if (!fd.show) return; const box = root.querySelector(`[data-field="${fd.k}"]`); if (box) box.classList.toggle('hide', !fd.show(vals)); }); };
  UI.form = (spec) => {
    const vals = spec.values || {};
    const dr = !!spec.drawer, pre = dr ? 'd-' : 'f-', ids = dr ? { form: 'dr-form', submit: 'dr-submit' } : { form: 'gen-form', submit: 'gen-submit' };
    const shell = (h) => (dr ? UI.openDrawer(h) : UI.modal(h, { wide: spec.wide, sticky: true }));
    const s = shell(`<div class="modal-head"><div>${spec.caps ? `<div class="caps">${spec.caps}</div>` : ''}<h2>${spec.title}</h2></div><button class="icon-btn" data-close aria-label="Close">${UI.icon('x')}</button></div>
      <form class="modal-body stack" novalidate id="${ids.form}" data-pre="${pre}">${spec.intro ? `<div class="small dim">${spec.intro}</div>` : ''}
        ${spec.fields.map((fd) => (fd.section ? `<div class="caps form-sec">${fd.section}</div>` : '') + UI.fieldHTML(fd, vals[fd.k] !== undefined ? vals[fd.k] : fd.def != null && !spec.edit ? fd.def : '', pre)).join('')}
        <div class="form-err callout danger small hide" role="alert"></div>${spec.note ? `<div class="callout small">${spec.note}</div>` : ''}
      </form>
      <div class="modal-foot">${spec.extra || ''}<button type="button" class="btn ghost" data-close>Cancel</button><button type="button" class="btn primary" id="${ids.submit}">${spec.submitIcon ? UI.icon(spec.submitIcon) : ''}${spec.submit || 'Save'}</button></div>`);
    const close = dr ? () => s.remove() : UI.closeModal;
    const form = $('#' + ids.form, s);
    setTimeout(() => { const init = JSON.stringify(UI.formValues(form, spec.fields)); s._guard = () => !!UI.VS.pending(form).length || JSON.stringify(UI.formValues(form, spec.fields)) !== init; }, 60);
    UI.applyConds(form, spec.fields);
    form.addEventListener('input', () => UI.applyConds(form, spec.fields));
    form.addEventListener('change', () => UI.applyConds(form, spec.fields));
    form.addEventListener('submit', (e) => { e.preventDefault(); $('#' + ids.submit, s).click(); });
    $('#' + ids.submit, s).addEventListener('click', () => {
      const v = UI.formValues(form, spec.fields.filter((fd) => !form.querySelector(`[data-field="${fd.k}"]`).classList.contains('hide')));
      if (!UI.checkFields(form, spec.fields, v)) { const first = form.querySelector('.field.bad input, .field.bad select, .field.bad textarea'); if (first) first.focus(); return; }
      if (UI.VS.pending(form).length) { UI.VS.commit(form, typeof spec.secretPrefix === 'function' ? spec.secretPrefix(form) : spec.secretPrefix || ((form.querySelector('[data-k="name"]') || {}).value || vals.name || spec.title || 'value')).then((ok) => { if (ok) $('#' + ids.submit, s).click(); }); return; }
      const r = spec.onSubmit(v, form);
      const box = $('.form-err', s);
      if (typeof r === 'string') { box.textContent = r; box.classList.remove('hide'); return; }
      if (r !== false) close();
    });
    // autofocus the first field, unless the user is already in one (never steal keystrokes)
    setTimeout(() => { if (form.contains(document.activeElement)) return; const f = form.querySelector('input:not([type=hidden]):not([readonly]),select,textarea'); if (f && innerWidth > 720) f.focus(); }, 30);
    return s;
  };
  // form control behaviours (shared by modal forms and inline editors)
  UI.act('form-seg', (b) => { const seg = b.closest('[data-segk]'); $$('button', seg).forEach((x) => { x.classList.toggle('on', x === b); x.setAttribute('aria-pressed', x === b); }); const h = seg.parentElement.querySelector(`input[data-k="${seg.dataset.segk}"]`); h.value = b.dataset.v; h.dispatchEvent(new Event('input', { bubbles: true })); });
  const listAdd = (box, v) => { v = v.trim(); if (!v) return; if ($$('.chip-x .mono', box).some((x) => x.textContent === v)) return; const sp = document.createElement('span'); sp.className = 'chip-x'; sp.innerHTML = `<span class="mono">${UI.esc(v)}</span><button type="button" data-act="list-del" aria-label="Remove ${UI.esc(v)}">${UI.icon('x')}</button>`; const anchor = box.querySelector('.list-add, .list-add-sel'); box.insertBefore(sp, anchor); box.dispatchEvent(new Event('input', { bubbles: true })); };
  UI.nudge = (inp, msg) => { inp.classList.remove('shake'); void inp.offsetWidth; inp.classList.add('shake'); if (msg) inp.placeholder = msg; inp.focus(); };
  UI.act('list-add', (b) => { const box = b.closest('.listed'), inp = $('.list-add', box); if (!inp.value.trim()) return UI.nudge(inp, 'type a value first'); listAdd(box, inp.value); inp.value = ''; inp.focus(); });
  UI.act('list-add-sel', (sel) => { const box = sel.closest('.listed'); listAdd(box, sel.value); const o = sel.querySelector(`option[value="${CSS.escape(sel.value)}"]`) || [...sel.options].find((x) => x.value === sel.value); if (o && o.value) o.remove(); sel.value = ''; });
  UI.act('list-del', (b) => { const box = b.closest('.listed'); b.closest('.chip-x').remove(); box.dispatchEvent(new Event('input', { bubbles: true })); });
  UI.act('map-del', (b) => { const box = b.closest('.kvedit'); b.closest('.kvrow').remove(); box.dispatchEvent(new Event('input', { bubbles: true })); });
  document.addEventListener('keydown', (e) => { if (e.key === 'Enter' && e.target.matches('.list-add') && !e.target.dataset.wfListadd) { e.preventDefault(); const box = e.target.closest('.listed'); listAdd(box, e.target.value); e.target.value = ''; } });
  document.addEventListener('input', (e) => { const r = e.target.closest('.kvedit .kvrow'); if (!r) return; const box = r.parentElement; const rows = $$('.kvrow', box); const lastRow = rows[rows.length - 1]; if (r === lastRow && $$('input', r).some((i) => i.value)) { const nr = document.createElement('div'); nr.className = 'kvrow'; nr.innerHTML = `<input class="input mono" placeholder="key" data-mk="k"><input class="input mono" placeholder="value" data-mk="v"><span style="width:30px"></span>`; box.appendChild(nr); const sp = r.querySelector('span'); if (sp) sp.outerHTML = `<button type="button" class="icon-btn" data-act="map-del" aria-label="Remove row">${UI.icon('trash')}</button>`; } });

  /** Stage a config change and offer the pipeline. */
  UI.staged = (ch, opts = {}) => {
    UI.refreshChrome();
    if (!ch) { UI.toast('No change', 'the node is the same as the live config', 'idle'); return; }
    if (opts.go !== false) UI.go('#/config/apply');
    else UI.toast(`Staged: ${UI.esc(ch.summary)}`, `${D.pending.length} change${D.pending.length > 1 ? 's' : ''} waiting · review in Config → apply`, 'run');
  };
  /* phone polish after every render: tab rows that scroll show a fade and keep the current tab in view;
     page-head actions beyond the first two fold into a ⋯ menu (the originals stay, hidden, and get the click) */
  UI.afterRender.push((main) => {
    main.querySelectorAll('.tabs').forEach((t) => { const upd = () => { t.classList.toggle('scrolls', t.scrollWidth > t.clientWidth + 2); t.classList.toggle('at-end', t.scrollLeft + t.clientWidth >= t.scrollWidth - 2); }; const on = t.querySelector('.on'); if (on && t.scrollWidth > t.clientWidth) t.scrollLeft = Math.max(0, on.offsetLeft - t.clientWidth / 2 + on.offsetWidth / 2); upd(); t.addEventListener('scroll', upd, { passive: true }); });
    if (innerWidth > 720) return;
    main.querySelectorAll('.page-head .actions, .bot-hero > .row.wrap').forEach((box) => { const btns = [...box.children].filter((x) => x.matches('.btn, a.btn') && !x.classList.contains('st')); if (btns.length <= 2) return;
      const keep = btns.filter((x) => x.classList.contains('primary')).slice(0, 1); btns.forEach((x) => { if (keep.length < 2 && !keep.includes(x)) keep.push(x); }); const extra = btns.filter((x) => !keep.includes(x));
      extra.forEach((x, i) => { x.classList.add('ph-folded'); x.dataset.fold = i; });
      const more = document.createElement('button'); more.type = 'button'; more.className = 'btn ph-more'; more.setAttribute('aria-label', 'More actions'); more.innerHTML = UI.icon('more'); more.dataset.act = 'ph-more'; box.appendChild(more); });
  });
  UI.act('ph-more', (b) => { const box = b.closest('.actions, .bot-hero > .row.wrap'), extra = [...box.querySelectorAll('.ph-folded')]; UI.menu(b, extra.map((x) => [x.textContent.trim() || x.getAttribute('aria-label') || 'Action', 'chev', 'ph-fold-go', `data-i="${x.dataset.fold}"${x.getAttribute('aria-disabled') === 'true' ? ` aria-disabled="true" data-reason="${UI.esc(x.dataset.reason || '')}"` : ''}`])); UI.phFoldBox = box; });
  UI.act('ph-fold-go', (b) => { UI.closeMenu(); const x = UI.phFoldBox && UI.phFoldBox.querySelector(`.ph-folded[data-fold="${b.dataset.i}"]`); if (x) x.click(); });
  /** A five-field cron in words where it's simple ("0 6 * * 1" → "Mondays at 06:00"); otherwise the expression. */
  UI.humanCron = (c) => { const f = String(c).trim().split(/\s+/); if (f.length !== 5 || !/^\d+$/.test(f[0]) || !/^\d+$/.test(f[1])) return c; const t = `${f[1].padStart(2, '0')}:${f[0].padStart(2, '0')}`, D7 = ['Sundays', 'Mondays', 'Tuesdays', 'Wednesdays', 'Thursdays', 'Fridays', 'Saturdays'];
    if (f[2] === '*' && f[3] === '*' && f[4] === '*') return `Every day at ${t}`; if (f[2] === '*' && f[3] === '*' && /^[0-6]$/.test(f[4])) return `${D7[+f[4]]} at ${t}`; if (f[2] === '*' && f[3] === '*' && f[4] === '1-5') return `Weekdays at ${t}`; if (/^\d+$/.test(f[2]) && f[3] === '*' && f[4] === '*') return `On day ${f[2]} of each month at ${t}`; return c; };
  UI.humanOn = (on) => String(on || '').replace(/schedule ((?:\S+\s+){4}\S+)/, (m, c) => { const h = UI.humanCron(c); return h === c ? m : `${h} (schedule)`; });
  /** The sticky bulk bar: n selected, actions [[label, icon, act, attrs?, offReason?]], and a clear action. */
  UI.bulkBar = (n, noun, actions, clearAct) => `<div class="bulk-bar" role="region" aria-label="Bulk actions"><b>${n} ${noun}${n > 1 ? 's' : ''} selected</b>${actions.map(([l, ic, act, attrs = '', off]) => `<button class="btn sm" ${off ? UI.off(off) : `data-act="${act}" ${attrs}`}>${UI.icon(ic)}${l}</button>`).join('')}<button class="btn sm ghost" data-act="${clearAct}" style="margin-left:auto">Clear</button></div>`;
  /** Download a text file (exports). */
  UI.download = (name, text, type = 'application/json') => { const a = document.createElement('a'); a.href = URL.createObjectURL(new Blob([text], { type })); a.download = name; document.body.appendChild(a); a.click(); setTimeout(() => { URL.revokeObjectURL(a.href); a.remove(); }, 100); UI.lastDownload = name; };

  /* ---------- editable code: a highlighted <pre> under a transparent <textarea> ---------- */
  const KW = { js: /\b(const|let|var|return|if|else|for|of|in|function|new|true|false|null|undefined|await|async|throw|try|catch)\b/g, python: /\b(import|from|def|return|if|elif|else|for|in|not|and|or|None|True|False|print|with|as|try|except|raise|lambda)\b/g, sh: /\b(if|then|fi|for|do|done|echo|exit|set|export|in)\b/g };
  UI.codeLine = (raw, lang) => {
    if (lang === 'yaml') return UI.yamlLine(raw);
    if (lang === 'md' && UI.mdLine) return UI.mdLine(raw);
    const E = UI.esc; let out = '', i = 0;
    const com = lang === 'js' ? '//' : '#';
    const ci = (() => { let q = null; for (let k = 0; k < raw.length; k++) { const c = raw[k]; if (q) { if (c === '\\') k++; else if (c === q) q = null; continue; } if (c === '"' || c === "'" || c === '`') q = c; else if (raw.startsWith(com, k)) return k; } return -1; })();
    const body = ci >= 0 ? raw.slice(0, ci) : raw, tail = ci >= 0 ? raw.slice(ci) : '';
    out = E(body).replace(/(&quot;|"|'|`)(?:(?!\1).)*\1/g, (m) => `<span class="y-s">${m}</span>`).replace(/\b(\d+(\.\d+)?)\b/g, '<span class="y-n">$1</span>');
    if (KW[lang]) out = out.replace(KW[lang], '<span class="y-k">$1</span>');
    out = out.replace(/\{\{[^}]*\}\}/g, (m) => `<span class="y-a">${m}</span>`).replace(/\bctx\.[\w.]+/g, (m) => `<span class="y-a">${m}</span>`);
    return out + (tail ? `<span class="y-c">${E(tail)}</span>` : '');
  };
  const edLines = (text, lang, o = {}) => text.split('\n').map((l, i) => `<span class="ln${(o.hl || []).includes(i + 1) ? ' hl' : ''}${(o.err || []).includes(i + 1) ? ' err' : ''}" data-ln="${i + 1}">${UI.codeLine(l, lang) || ' '}</span>`).join('');
  /** UI.editor({ id, text, lang, err, hl, minRows, act, label }) — act fires on input (debounce in the handler). */
  UI.editor = (o) => `<div class="ced" data-lang="${o.lang || 'yaml'}" id="${o.id}"><pre class="code ced-hl" aria-hidden="true">${edLines(o.text, o.lang || 'yaml', o)}</pre><textarea class="ced-ta" spellcheck="false" autocapitalize="off" autocomplete="off" aria-label="${UI.esc(o.label || 'YAML')}" ${o.act ? `data-on-input="${o.act}"` : ''} ${o.readonly ? 'readonly' : ''} style="min-height:${(o.minRows || 8) * 19.5 + 16}px">${UI.esc(o.text)}</textarea></div>`;
  UI.editorMark = (root, o) => { const pre = root.querySelector('.ced-hl'), ta = root.querySelector('.ced-ta'); pre.innerHTML = edLines(ta.value, root.dataset.lang, o); pre.scrollTop = ta.scrollTop; pre.scrollLeft = ta.scrollLeft; };
  UI.editorSet = (root, text, o = {}) => { const ta = root.querySelector('.ced-ta'); if (ta.value !== text) { const pos = ta.selectionStart; ta.value = text; if (document.activeElement === ta) ta.setSelectionRange(Math.min(pos, text.length), Math.min(pos, text.length)); } UI.editorMark(root, o); };
  UI.editorScrollTo = (root, line) => { const ta = root.querySelector('.ced-ta'); const y = (line - 1) * 19.55; if (ta.scrollHeight > ta.clientHeight) ta.scrollTop = Math.max(0, y - 60); else { const sc = root.closest('.ced-scroll') || $('#main'); const r = root.getBoundingClientRect(); sc.scrollTop += r.top + y - 140; } };
  document.addEventListener('input', (e) => { if (e.target.classList.contains('ced-ta')) { const root = e.target.closest('.ced'); const pre = root.querySelector('.ced-hl'); pre.innerHTML = edLines(e.target.value, root.dataset.lang, {}); } });
  document.addEventListener('scroll', (e) => { if (e.target.classList && e.target.classList.contains('ced-ta')) { const pre = e.target.previousElementSibling; pre.scrollTop = e.target.scrollTop; pre.scrollLeft = e.target.scrollLeft; } }, true);
  document.addEventListener('keydown', (e) => { if (e.key === 'Tab' && e.target.classList.contains('ced-ta') && !e.shiftKey) { e.preventDefault(); const ta = e.target, a = ta.selectionStart; ta.setRangeText('  ', a, ta.selectionEnd, 'end'); ta.dispatchEvent(new Event('input', { bubbles: true })); } });

  /* ---------- diffs ---------- */
  UI.diffView = (A, B, mode = 'unified', o = {}) => {
    const d = YAML.diff(A, B), E = UI.esc;
    /* word-level highlight: pair each run of removed lines with the added lines after it; mark what changed between them */
    for (let i = 0; i < d.length;) { if (d[i].t !== '-') { i++; continue; } let j = i; while (j < d.length && d[j].t === '-') j++; let k = j; while (k < d.length && d[k].t === '+') k++;
      for (let n = 0; n < Math.min(j - i, k - j); n++) { const x = d[i + n].s, y = d[j + n].s; let p = 0; while (p < x.length && p < y.length && x[p] === y[p]) p++; let q = 0; while (q < x.length - p && q < y.length - p && x[x.length - 1 - q] === y[y.length - 1 - q]) q++;
        if ((p + q) / Math.max(x.length, y.length, 1) >= 0.4) { d[i + n].w = [x.slice(0, p), x.slice(p, x.length - q), x.slice(x.length - q)]; d[j + n].w = [y.slice(0, p), y.slice(p, y.length - q), y.slice(y.length - q)]; } }
      i = k; }
    const lineHTML = (r) => (r.w ? `${E(r.w[0])}${r.w[1] ? `<mark class="wd">${E(r.w[1])}</mark>` : ''}${E(r.w[2])}` : UI.yamlLine(r.s) || ' ');
    const adds = d.filter((r) => r.t === '+').length, dels = d.filter((r) => r.t === '-').length;
    if (!adds && !dels) return { html: `<div class="empty small"><span class="serif">No difference</span>The candidate is byte-identical to the live file.</div>`, adds, dels };
    /* The hunk's location: the key path of its first changed line (teams.platform.collections.vendors) and that line's number. */
    const LA = A.split('\n'), LB = B.split('\n');
    const keyAt = (lines, i) => { const path = []; let ind = Infinity; for (let k = i; k >= 0; k--) { const m = (lines[k] || '').match(/^(\s*)(- )?("[^"]+"|[\w.@/${}-]+):(\s|$)/); if (!m) continue; const n = m[1].length + (m[2] ? 2 : 0); if (n < ind) { path.unshift(m[3].replace(/^"|"$/g, '')); ind = m[2] ? m[1].length : n; if (!ind) break; } } return path.join('.'); };
    const loc = (h) => { const r = h.find((x) => x.t !== ' ') || h[0]; const inB = r.t !== '-'; return { path: o.sections ? keyAt(inB ? LB : LA, (inB ? r.b : r.a) - 1) : '', line: inB ? r.b : r.a }; };
    const headOf = (h) => { const l = loc(h); return `<div class="h">${E(o.file || 'config.yaml')}${l.path ? ' · <span class="mono">' + E(l.path) + '</span>' : ''} · line ${l.line}</div>`; };
    const row = (r) => `<div class="dl ${r.t === '+' ? 'add' : r.t === '-' ? 'del' : ''}"><span>${r.a || ''}</span><span>${r.b || ''}</span><span>${r.t === ' ' ? ' ' : r.t}</span><span>${lineHTML(r)}</span></div>`;
    let html;
    if (mode === 'whole') html = d.map(row).join('');
    else if (mode === 'split') {
      html = YAML.hunks(d, 2).map((h) => { const L = [], R = []; let dl = [], al = []; const flush = () => { const n = Math.max(dl.length, al.length); for (let k = 0; k < n; k++) { L.push(dl[k] ? `<div class="sl del"><span>${dl[k].a}</span><span>${lineHTML(dl[k])}</span></div>` : '<div class="sl gap"><span></span><span> </span></div>'); R.push(al[k] ? `<div class="sl add"><span>${al[k].b}</span><span>${lineHTML(al[k])}</span></div>` : '<div class="sl gap"><span></span><span> </span></div>'); } dl = []; al = []; };
        h.forEach((r) => { if (r.t === '-') dl.push(r); else if (r.t === '+') al.push(r); else { flush(); L.push(`<div class="sl"><span>${r.a}</span><span>${UI.yamlLine(r.s) || ' '}</span></div>`); R.push(`<div class="sl"><span>${r.b}</span><span>${UI.yamlLine(r.s) || ' '}</span></div>`); } }); flush();
        return `${headOf(h)}<div class="split"><div>${L.join('')}</div><div>${R.join('')}</div></div>`; }).join('');
    } else html = YAML.hunks(d, 3).map((h) => `${headOf(h)}${h.map(row).join('')}`).join('');
    return { html: `<div class="diff ${mode}">${html}</div>`, adds, dels };
  };
})();
