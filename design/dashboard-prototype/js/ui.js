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
  /** The text of a bit of markup (a glossary term's tooltip and attributes never leak into a cell). */
  UI.plainText = (html) => { const d = document.createElement('div'); d.innerHTML = String(html); d.querySelectorAll('[data-tip]').forEach((x) => x.removeAttribute('data-tip')); return d.textContent; };
  UI.esc = (s) => String(s ?? '').replace(/[&<>"]/g, (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;' }[c]));

  /* ---------- icons: Lucide at a 1.75 stroke (js/icons.js); status glyphs are Daylight's own ---------- */
  const P = window.ICONS || {};
  UI.icon = (n, cls = '') => `<svg class="${cls}" data-ico="${n}" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.75" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">${P[n] || P.dot}</svg>`;
  /** A status glyph (success, warning, danger, info, running, neutral, skipped, shadow, needs-you), 16-unit grid. */
  UI.glyph = (n, cls = '') => `<svg class="glyph ${cls}" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">${(window.GLYPHS || {})[n] || ''}</svg>`;

  /* ---------- status ---------- */
  /* ---------- status marks, labels, filter chips, counts, row states (css/marks.css) ----------
   * The only way screens draw these. The brand's final status-and-label system swaps in here and in marks.css. */
  // state → Daylight mark and default word (design/brand/status/marks.js STATES)
  const SG = { ok: ['success', 'Passed'], success: ['success', 'Passed'], run: ['running', 'Running'], wait: ['warning', 'Waiting'], fail: ['danger', 'Failed'], idle: ['neutral', 'Parked'], shadow: ['shadow', 'Shadow'], ember: ['needs-you', 'Needs you'], skip: ['skipped', 'Not reached'], staged: ['staged', 'Not applied'], ready: ['ready', 'Ready'] }; // ready: set to start, not started (the brand's hollow dot: Passed not yet filled; ‖ is parked)
  /** The mark alone (a 12px state shape in its state colour), for margins, trees and lanes. */
  UI.markGlyph = (s) => (SG[s] || SG.idle)[0]; // the glyph a state's mark draws
  UI.mark = (s, o = {}) => { const g = SG[s] || SG.idle; return `<span class="st mk-only${o.lg ? ' lg' : ''}" data-s="${s}"${o.label ? ` role="img" aria-label="${UI.esc(o.label === true ? g[1] : o.label)}"` : ''}><i>${UI.glyph(g[0])}</i></span>`; };
  /** A status mark: mark + word (marks.js status()). opts: { cls, sm, lg, tip, id } */
  UI.statusMark = (s, word, o = {}) => { const g = SG[s] || SG.idle; return `<span class="st${o.sm ? ' sm' : ''}${o.lg ? ' lg' : ''}${o.cls ? ' ' + o.cls : ''}" data-s="${s}"${o.id ? ` id="${o.id}"` : ''}${o.tip ? ` data-tip="${UI.esc(o.tip)}"` : ''}><i>${UI.glyph(g[0])}</i>${word ?? g[1]}</span>`; };
  UI.st = (s, label) => UI.statusMark(s, label);
  /** A label: a quiet word. opts: { s (state), mono, cls, icon, tip, attrs } */
  UI.label = (text, o = {}) => `<span class="tag${o.mono ? ' mono' : ''}${o.cls ? ' ' + o.cls : ''}"${o.s ? ` data-s="${o.s}"` : ''}${o.tip ? ` data-tip="${UI.esc(o.tip)}"` : ''}${o.attrs ? ' ' + o.attrs : ''}>${o.icon ? UI.icon(o.icon) : ''}${text}</span>`;
  UI.tag = (t, s) => UI.label(t, { s });
  /** A filter chip: { text, on, act, attrs, icon, cls, tag ('button'|'label'|'a'), href } */
  UI.filterChip = (o) => { const t = o.tag || 'button', on = !!o.on; return `<${t}${t === 'button' ? ' type="button"' : ''}${o.href ? ` href="${o.href}"` : ''} class="chip${on ? ' on' : ''}${o.cls ? ' ' + o.cls : ''}"${t === 'button' ? ` aria-pressed="${on}"` : ''}${o.act ? ` data-act="${o.act}"` : ''}${o.attrs ? ' ' + o.attrs : ''}>${on && !o.icon && !o.noCheck ? UI.icon('check') : o.icon ? UI.icon(o.icon) : ''}${o.text}</${t}>`; };
  /** An empty state: what appears here (a short human title), why it's empty, and the one next step. No illustration:
      the logo is never turned into a motif, and a plain useful sentence does the job. */
  UI.empty = ({ title, body = '', action = '', small = false, cls = '' }) => `<div class="empty${small ? ' small' : ''}${cls ? ' ' + cls : ''}"><span class="serif">${title}</span>${body ? `<p>${body}</p>` : ''}${action}</div>`;
  /** A filter that picks one value: a chip wrapping a native select. { icon, on, label, attrs (on the select), options (html), cls } */
  UI.selectChip = (o) => `<label class="chip chip-sel${o.on ? ' on' : ''}${o.cls ? ' ' + o.cls : ''}"${o.wrapAttrs ? ' ' + o.wrapAttrs : ''}>${o.icon ? UI.icon(o.icon) : ''}<span class="sr-only">${o.label}</span><select ${o.attrs || ''} aria-label="${o.label}">${o.options}</select></label>`;
  /** A group heading in a grouped list or table: "Needs attention · 3", then one plain sentence and at most one action.
      tone: attn (needs someone) | run (running now) | done (finished, quiet ink). */
  UI.groupHead = ({ title, n, say = '', action = '', tone = 'done' }) => `<div class="grp-head" data-tone="${tone}"><span class="grp-t">${title}${n != null ? ` <span class="grp-n">· ${n}</span>` : ''}</span>${say ? `<span class="grp-say">${say}</span>` : ''}${action ? `<span class="grp-act">${action}</span>` : ''}</div>`;
  /** The same heading as a table row spanning every column. */
  UI.groupRow = (o) => `<tr class="grp-row" data-tone="${o.tone || 'done'}"><td colspan="20">${UI.groupHead(o)}</td></tr>`;
  /* what a verb does, and how much a decision risks, in plain words: used only inside approval content ("Touches
   * production", "Spends money"); there is no visual risk scale (the brand's proposal is pending review) */
  UI.actionOf = (verb = '', hint = '') => { const v = String(verb).toLowerCase() + ' ' + String(hint).toLowerCase();
    if (/purchase|buy|savings_plan|reserve|spend|budget\.raise/.test(v)) return 'money';
    if (/destroy|delete|drop|\.rm\b|remove|purge|revoke/.test(v)) return 'destroy';
    if (/grant|permission|operators|access|widen|allow_secrets|autonomy|sandbox|egress/.test(v)) return 'permission';
    if (/tf-apply|terraform apply|\.apply\b|deploy|rollout|migrate/.test(v)) return 'infra';
    if (/push|commit|merge|git\./.test(v)) return 'push';
    if (/comment|post|reply|review|message|send|notify|page\b/.test(v)) return 'post';
    if (/\.get\b|get_|list|read|search|logs|diff|plan\b|tf-plan|fetch|query|status|watch/.test(v)) return 'read';
    return 'change'; };
  UI.RISK = [['read', 'Reads only'], ['reversible', 'Reversible'], ['change', 'Changes things'], ['prod', 'Touches production'], ['money', 'Spends money'], ['destroy', 'Destructive']];
  /** How risky: from conductor's tier (observe / reversible / propose / commit_money / never), the verb's action and the
      environment it lands in. Returns { level 0..5, key, word }. */
  UI.riskOf = ({ tier = '', verb = '', env = '', destroys = 0, action = '' } = {}) => { const a = action || UI.actionOf(verb); let lv;
    if (a === 'permission') lv = 5; else if (a === 'destroy' || destroys > 0 || tier === 'never') lv = 5; else if (a === 'money' || tier === 'commit_money') lv = 4;
    else if (env === 'production' && a !== 'read') lv = 3; else if (a === 'read' || tier === 'observe') lv = 0; else if (tier === 'reversible' || a === 'post') lv = 1; else lv = 2;
    const word = lv === 5 && a === 'permission' ? 'Changes permissions' : UI.RISK[lv][1]; return { level: lv, key: UI.RISK[lv][0], word }; };
  /** '23h 52m' → '23 h 52 min': spans people read, not unit codes. */
  /** '3d 4h 12m' → '3 days': the rail's uptime, to the largest unit. */
  UI.upFor = (t) => { const m = String(t).match(/(\d+)\s*([dhm])/); if (!m) return t; const n = +m[1]; return `${n} ${{ d: 'day', h: 'hour', m: 'minute' }[m[2]]}${n === 1 ? '' : 's'}`; };
  UI.humanSpan = (t) => String(t).replace(/(\d+)\s*d\b/g, '$1 d').replace(/(\d+)\s*h\b/g, '$1 h').replace(/(\d+)\s*m\b/g, '$1 min').replace(/(\d+)\s*s\b/g, '$1 s');
  /** Where an action lands, as a quiet word (no colour, no badge): production / staging / local. */
  UI.envTag = (env) => (env ? UI.label(env[0].toUpperCase() + env.slice(1)) : '');
  /** Labelled metadata (the labelled facts below, from items { label, value, mono }). */
  UI.metaLine = (items) => UI.facts(items.filter(Boolean).map((x) => [x.label, x.mono ? `<span class="mono">${x.value}</span>` : x.value]));
  /** Prose that may carry markup (<b>, <span class="mono">…) and plain placeholders (<vault>, <name>): the
      placeholders are escaped so the browser never swallows them as unknown tags (a dropped interpolation). */
  UI.ph = (html) => String(html).replace(/<(?!\/?(?:a|b|i|em|strong|span|code|br|div|p|ul|ol|li|kbd|small|mark|time|svg|path|button|input|label|select|option|details|summary|dl|dt|dd|table|tr|td|th|thead|tbody|pre|h[1-6])\b)([a-z][\w-]*)>/gi, '&lt;$1&gt;');
  /** The labelled facts of a thing (the run page's Details card): label, then value; ids and times never bare. */
  UI.facts = (items) => `<dl class="dl-facts">${items.filter(Boolean).map(([k, v]) => `<dt>${k}</dt><dd>${v}</dd>`).join('')}</dl>`;
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
    // sign-in and enrolment use the kit's horizontal lockup file (BRAND.md Logo › Files), one per theme
    if (kind === 'horizontal') return `<span class="logo-h ${cls}"><img class="lh-light" src="assets/logo/logo-horizontal.svg" alt="${UI.esc(L.wordmark || 'conductor')}"><img class="lh-dark" src="assets/logo/logo-horizontal-dark.svg" alt="${UI.esc(L.wordmark || 'conductor')}"></span>`;
    return kind === 'mark' ? mark : `<span class="lockup" role="img" aria-label="${UI.esc(L.wordmark || 'conductor')}">${mark}<span class="wordmark" aria-hidden="true">${UI.esc(L.wordmark || 'conductor')}</span></span>`; };
  UI.sigil = (mbr, size = '') => `<span class="sigil ${size}" style="--sig:${mbr.color}">${mbr.name[0]}${mbr.home === 'live' ? '<span class="live"></span>' : ''}</span>`;
  UI.ago = (iso) => { const s = (Date.now() - new Date(iso)) / 1000; if (s < 60) return Math.max(1, s | 0) + 's'; if (s < 3600) return (s / 60 | 0) + 'm'; if (s < 86400) return (s / 3600 | 0) + 'h'; return (s / 86400 | 0) + 'd'; };
  UI.clock = (iso) => new Date(iso).toTimeString().slice(0, 8);
  /* human times: "52 min ago (17:03)"; the exact local time (and UTC) on hover */
  UI.agoText = (iso) => { const s = (Date.now() - new Date(iso)) / 1000; if (s < 60) return 'just now'; if (s < 3600) return `${s / 60 | 0} min ago`; if (s < 86400) { const h = s / 3600 | 0; return `${h} h ago`; } const d = s / 86400 | 0; return d === 1 ? 'yesterday' : `${d} days ago`; };
  /** A date and time in the one format the dashboard uses: "Oct 17, 21:07" (24-hour, local). */
  UI.dateTime = (iso) => { const d = new Date(iso); return `${d.toLocaleString('en', { month: 'short' })} ${d.getDate()}, ${d.toTimeString().slice(0, 5)}`; };
  UI.hm = (iso) => new Date(iso).toTimeString().slice(0, 5);
  UI.when = (iso, withClock = false) => { const d = new Date(iso); return `<time datetime="${d.toISOString()}">${UI.agoText(iso)}${withClock ? ` <span class="muted">(${UI.hm(iso)})</span>` : ''}</time>`; };
  /** "7m" / "1h" / "3d" (the fake data's short ages) → "7 min ago" */
  UI.ageText = (a) => { const m = String(a).match(/^(\d+)\s*([smhd])$/); if (!m) return a; return m[2] === 's' ? 'just now' : m[2] === 'm' ? `${m[1]} min ago` : m[2] === 'h' ? `${m[1]} h ago` : m[1] === '1' ? 'yesterday' : `${m[1]} days ago`; };
  /** Durations in words with units: "12 min", "7 min 40 s", "26 s". */
  /** How long something has been running, in whole minutes (a list of live runs never mixes "6 min" and "6 min 2 s"). */
  UI.runningFor = (iso) => { const m = Math.floor((Date.now() - new Date(iso)) / 60000); return m < 1 ? 'under a minute' : m < 60 ? `${m} min` : `${Math.floor(m / 60)} h ${m % 60} min`; };
  UI.durText = (ms) => { if (!ms) return '—'; const s = Math.round(ms / 1000); if (s < 60) return `${Math.max(1, s)} s`; const m = Math.floor(s / 60), r = s % 60; return m < 10 && r ? `${m} min ${r} s` : m < 60 ? `${m} min` : `${Math.floor(m / 60)} h ${m % 60} min`; };
  UI.runDur = (r) => (r.finished ? UI.durText(new Date(r.finished) - new Date(r.started)) : '…');
  /** A run's plain headline (what it is doing or did, in words); the workflow name and ids follow in the detail line. */
  const RUN_WHAT = { 'ci-fixer': ['Fixing a failing check', 'Fixed a failing check', 'Couldn’t fix a failing check'], 'pr-review': ['Reviewing a pull request', 'Reviewed a pull request', 'Couldn’t review a pull request'], 'issue-triage': ['Triaging a new issue', 'Triaged a new issue', 'Couldn’t triage an issue'], 'dep-bump': ['Bumping dependencies', 'Bumped dependencies', 'Couldn’t bump dependencies'], 'release-notes': ['Drafting release notes', 'Drafted release notes', 'Couldn’t draft release notes'], 'incident-intake': ['Taking in an incident', 'Took in an incident', 'Couldn’t take in an incident'], 'assess-and-post': ['Assessing and posting', 'Assessed and posted', 'Couldn’t assess and post'] };
  UI.runTitle = (r) => { const bot = String(r.workflow || '').startsWith('bot:') ? r.workflow.slice(4) : null; const i = r.status === 'run' ? 0 : ['fail', 'stopped'].includes(r.status) ? 2 : 1;
    if (r.status === 'wait') return `Waiting for your answer${bot ? ` (${bot})` : ''}`; if (r.status === 'idle') return `${String(r.workflow).replace(/^bot:/, '')} is parked`;
    if (bot) return [`${bot} is working`, `${bot} finished a task`, `${bot}’s task failed`][i]; const w = RUN_WHAT[r.workflow]; return w ? w[i] : [`Running ${r.workflow}`, `${r.workflow} finished`, `${r.workflow} failed`][i]; };
  /** A run's error in plain words; the raw text stays on the run's record and in the step log. */
  UI.plainError = (r) => { const e = String(r.error || ''); let m;
    if ((m = e.match(/(\d+) failures? \(([^)]*)\)/))) return `${m[1]} test${m[1] === '1' ? '' : 's'} failed (${m[2]})`;
    if (/max_cost_usd [\d.]+ exceeded/.test(e)) return 'It hit its budget cap, so conductor stopped it';
    if (/stopped by operator/.test(e)) return 'You stopped it';
    return e; };
  /** The words for a run's state, used in tables and stories. */
  UI.runWord = (s) => ({ ok: 'Passed', run: 'Running', fail: 'Failed', wait: 'Needs you', idle: 'Parked', stopped: 'Stopped' }[s] || s);
  // a run's state → the status vocabulary (a run waiting on a person's answer needs you; a stopped run is a dash)
  UI.runS = (s) => ({ stopped: 'skip', wait: 'ember' }[s] || s);
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
  /** Workflow figures come from the run records (the same ones Activity lists), never from hand-written numbers:
      runs24 = runs started in the last 24 hours, fail24 = those that failed, ok = the share that didn't. */
  UI.syncRunStats = () => { if (!window.D || !D.workflows) return; const since = Date.now() - 864e5;
    D.workflows.forEach((w) => { const rs = D.runs.filter((r) => r.workflow === w.name && new Date(r.started) >= since), f = rs.filter((r) => r.status === 'fail' || r.status === 'stopped').length;
      w.runs24 = rs.length; w.fail24 = f; w.ok = rs.length ? (rs.length - f) / rs.length : 1; }); };
  /** Needs you is derived, on every render, from the things that need a person: a pending approval, a failed run (until
      dismissed), a run waiting on an answer, a browser or a login waiting on a push approval. Anything shown as "Needs
      you" anywhere is in this list and in the counts; an item whose cause is resolved leaves it. Fixture items keep
      their hand-written words; new ones are written from their source. */
  UI.syncAttention = () => { if (!window.D || !D.attention || !D.proposals) return;
    const dismissed = new Set(D.dismissed || []), want = [], ago = (iso) => (iso ? UI.ago(iso) : '');
    D.proposals.filter((p) => p.status === 'pending' && UI.live(p)).forEach((p) => want.push({ kind: p.kind === 'purchase' ? 'purchase' : 'approval', href: `#/bots/${p.bot}/proposals/${p.id}`, title: `${p.verb} · ${p.id}`, sub: `${p.bot} · case ${p.case}`, plain: p.title || p.summary || p.verb, why: p.kind === 'purchase' ? p.why || `${p.bot} asks before it buys` : `${p.bot} asks before it does this`, verb: 'Review', age: ago(p.at) }));
    // a purchase that isn't a bot's (a bot's is its proposal, above) waits for your signed approval too
    (D.purchases || []).filter((p) => !p.bot && UI.puStatus && UI.puStatus(p) === 'pending').forEach((p) => want.push({ kind: 'purchase', href: `#/browser/purchases/${p.id}`, title: `purchase ${p.id}`, sub: p.merchant, plain: UI.purchaseTitle(p), why: p.why || 'An agent asks before it buys', verb: 'Review', age: ago(p.created) }));
    D.runs.filter((r) => r.status === 'fail' && Date.now() - new Date(r.finished || r.started) < 864e5).forEach((r) => want.push({ kind: 'failed', href: `#/runs/${r.id}`, title: `${r.workflow} failed${UI.runTarget(r) ? ' on ' + UI.runTarget(r) : ''}`, sub: r.error || '', plain: `${r.workflow.replace(/^bot:/, '')} failed${UI.runTarget(r) ? ' on ' + UI.runTarget(r) : ''}`, why: r.error || '', verb: 'Open', age: ago(r.finished || r.started) }));
    D.runs.filter((r) => r.status === 'wait').forEach((r) => want.push({ kind: 'needs_input', href: `#/runs/${r.id}`, title: `${r.workflow} is waiting on you`, sub: r.detail || '', plain: `${r.workflow.replace(/^bot:/, '')} needs your answer`, why: r.detail || '', verb: 'Answer', age: ago(r.started) }));
    const waitingOrigins = new Set();
    (D.browsers || []).filter((b) => b.status === 'wait').forEach((b) => { try { waitingOrigins.add(new URL(b.url).origin); } catch (e) {} want.push({ kind: 'browser', href: `#/browser/${b.id}`, title: `${b.title || 'a sign-in'} · ${b.identity}`, sub: b.url || '', plain: `Approve a sign-in for ${b.identity}`, why: 'Its browser is waiting on you', verb: 'Open', age: '' }); });
    Object.entries(D.loginMeta || {}).forEach(([n, m]) => { const c = (D.access && D.access.credentials || []).find((x) => x.name === n), origin = c && c.origin; if (m.test && m.test.result === 'needs you' && !waitingOrigins.has(origin)) want.push({ kind: 'browser', href: '#/browser/logins', title: `login ${n} · push approval`, sub: origin || '', plain: `Approve a sign-in on ${origin ? origin.replace(/^https?:\/\//, '') : n}`, why: 'A push approval is waiting on your phone', verb: 'Open', age: ago(m.test.at) }); });
    if (D.daemon && D.daemon.paused) want.push({ kind: 'paused', s: 'ember', href: '#/safety?show=pause', title: 'dispatch paused', sub: '', plain: 'All automation is paused', why: 'Nothing new starts until you resume it. Events that arrive meanwhile may not be kept.', verb: 'Resume', age: '' });
    const us = UI.updateState(); if (us.need) want.push({ kind: 'update', s: 'ember', href: '#/updates', title: `update ${us.v}`, sub: us.need, plain: `Update ${us.v} needs you`, why: us.need, verb: 'Review', age: '' });
    (D.connectors || []).filter((c) => c.status === 'fail').forEach((c) => want.push({ kind: 'connection', s: 'fail', href: `#/connectors/connectors?show=conn-${c.name}`, title: `${c.name} · ${c.use}`, sub: c.detail || '', plain: `conductor can’t reach ${(window.SCHEMA && SCHEMA.connectors[c.use] && SCHEMA.connectors[c.use].label) || c.use} (${c.name})`, why: c.detail ? `${c.detail}. Workflows that use it will fail until it’s fixed.` : 'Workflows that use it will fail until it’s fixed.', verb: 'Fix it', age: '' }));
    const keep = D.attention.filter((a) => want.some((w) => w.href === a.href) && !dismissed.has(a.href));
    D.attention = keep.concat(want.filter((w) => !dismissed.has(w.href) && !keep.some((a) => a.href === w.href)));
  };
  /** The update, in words: quiet when it installs itself, `need` when a person has to act (it failed, it's held, or it
      waits for an approval). The machine card in the rail says it; Needs you lists it only when it needs someone. */
  UI.updateState = () => { const d = (window.D && D.daemon) || {}, u = d.update || {}, v = u.available; if (!v) return {};
    if (u.blocked) return { v, need: u.blocked };
    if (u.installing) return { v, quiet: `Installing ${v}…` };
    const pol = (window.S && S.staged ? S.staged('update', 'update') : null) || u;
    if (pol.apply === 'workflow') return { v, quiet: `${v} is ready; a workflow decides when` };
    return { v, quiet: pol.auto === false ? `${v} is ready to install` : `${v} installs within ${UI.humanSpan ? UI.humanSpan(pol.interval || '10m') : '10 min'}` }; };
  /** The machine card: the rail's foot on a desktop, the top of More on a phone. It links to System → Updates. */
  UI.hostCard = () => { const d = D.daemon, us = UI.updateState();
    const [s, line] = d.paused ? ['wait', 'Paused: nothing new starts'] : us.need ? ['ember', `Update ${us.v} needs you`] : ['ok', `Healthy · up ${UI.upFor(d.uptime)}`];
    return `<a class="host${us.need || d.paused ? ' need' : ''}" href="#/updates" aria-label="${UI.esc(d.host)}: ${UI.esc(line)}${us.quiet ? '. ' + UI.esc(us.quiet) : ''}">${UI.statusMark(s, '<span class="sr-only">' + (s === 'ok' ? 'healthy' : 'needs a look') + '</span>')}<span class="who"><b>${UI.esc(d.host)}</b><span id="host-line">${UI.esc(line)}</span>${us.quiet && !us.need ? `<span class="host-upd">${UI.esc(us.quiet)}</span>` : ''}</span></a>`; };
  /** The one stat tile (Home's At a glance, Needs you's kinds): label on top, the number, a detail
      line; a link, or a button that filters (with its selected state); the row of them is UI.glance, Home's grid. */
  UI.statTile = (o) => { const inner = `<span class="dl-lbl">${o.label}</span><b>${o.value}</b>${o.detail != null ? `<span class="d">${o.detail}</span>` : ''}`, cls = `dl-tile${o.hot ? ' hot' : ''}${o.on ? ' on' : ''}`;
    return o.href ? `<a class="${cls}" href="${o.href}">${inner}</a>` : `<button type="button" class="${cls}" data-act="${o.act}" ${o.attrs || ''} aria-pressed="${!!o.on}">${inner}</button>`; };
  UI.glance = (tiles, label = 'At a glance') => `<section class="dl-glance" aria-label="${label}">${tiles.join('')}</section>`;
  /* ---------- the facts line: what a person scans a row for, one icon and one value per fact ----------
   * Which bot or workflow, where (a pull request, a repository, a channel, a site), the environment, what kind of
   * action and how risky, what it touches. One object icon per kind of fact, the value in quiet ink; a fact that leads
   * somewhere is underlined and opens it (a row's own link stays the row's); a risky action is in bold ink; production
   * carries the warning mark and colour. IDs and hashes never: they're on the detail page. On a phone the low-value
   * facts (pri 3) drop first and the rest wrap under.
   *   UI.factsLine([{ icon, value, href, pri: 1|2|3, risky, prod, label }])  (UI.facts is the details list) */
  UI.factsLine = (xs) => { xs = (xs || []).filter((f) => f && f.value); if (!xs.length) return '';
    return `<span class="fx">${xs.map((f) => { const ext = /^https?:/.test(f.href || ''), inner = `${f.prod ? `<i class="fx-g fx-prod">${PROD_SQ}</i>` : f.glyph ? f.glyph : f.tone === 'danger' ? `<span class="fx-tile">${UI.icon(f.icon)}</span>` : UI.icon(f.icon || 'dot')}${UI.esc(f.value)}`;
      return `<span class="fx-i${f.href ? ' ln' : ''}${f.risky ? ' hi' : ''}${f.prod ? ' prod t-warning' : ''}${f.tone ? ' t-' + f.tone : ''}" data-pri="${f.pri || 2}"${f.label ? ` title="${UI.esc(f.label)}"` : ''}${f.href ? ` role="link" tabindex="0" data-fx-href="${UI.esc(f.href)}"${ext ? ' data-fx-ext="1"' : ''}` : ''}>${inner}</span>`; }).join('')}</span>`; };
  /* One time range (61): the same choices everywhere a time range appears (Activity, Logs & audit, a workflow's runs,
     Home's chart); the filter bar draws it as its own control at the far right, a clock and "Last 12 hours ▾". */
  UI.TIME_RANGES = [['1h', 'Last hour', 60], ['12h', 'Last 12 hours', 720], ['24h', 'Last 24 hours', 1440], ['7d', 'Last 7 days', 10080], ['30d', 'Last 30 days', 43200], ['all', 'All time', 0]];
  UI.timeField = (o = {}) => ({ k: o.k || 'since', label: 'Time', time: true, bare: true, keepOrder: true, def: o.def || '12h', title: o.title || 'Show from', find: 'Find a range', hint: '1h, 12h, 24h, 7d, 30d, all', options: () => UI.TIME_RANGES.filter(([v]) => !o.only || o.only.includes(v)).map(([v, label]) => ({ v, label })) });
  UI.sinceMin = (v) => (UI.TIME_RANGES.find(([k]) => k === v) || [0, 0, 0])[2];
  UI.withinRange = (iso, v) => { const m = UI.sinceMin(v); return !m || (Date.now() - new Date(iso)) / 60000 <= m; };
  /** The facts a plugin or a connection carries, each its own kind (45; the brand's fact types, design/brand/status/
   *  facts.js): the service (its own glyph; the plug when it has none), where it comes from (built in: the conductor
   *  mark; official; a custom source, toned), how it updates (stays current, held: full ink, within a range), the
   *  version (always quiet), an update (the most noticeable), and production (the solid square, amber). In a row the
   *  signal comes first (update, risk), then what and where, then origin and how it updates, then the version. */
  const MARK16 = '<svg viewBox="0 0 16 16" width="16" height="16" aria-hidden="true" class="fx-mark"><path fill="currentColor" d="M1 11a7 8 0 0 1 14 0h-3a4 4.8 0 0 0-8 0z"/><circle cx="8" cy="11" r="2.6" fill="currentColor"/></svg>';
  const svcGlyph = (u) => { const g = (window.BRAND_SERVICES || {})[u]; return g ? `<svg viewBox="0 0 24 24" class="fx-svc" aria-hidden="true">${g.replace(/<path /g, '<path fill="currentColor" ')}</svg>` : null; };
  UI.pluginFact = {
    service: (u) => { const d = (window.SCHEMA && SCHEMA.connectors[u]) || {}, g = svcGlyph(u); return Object.assign({ icon: 'plug', value: d.label || (UI.serviceName ? UI.serviceName(u) : u), label: 'Service' }, g ? { glyph: g } : {}); },
    origin: (o, src) => (o === 'builtin' ? { glyph: MARK16, value: 'Built in', label: 'Ships with conductor' } : o === 'official' ? { icon: 'badge-check', value: 'Official', label: 'Published and verified by conductor’s maintainers' } : { icon: 'git-fork', value: src ? `Custom · ${src}` : 'Custom source', tone: 'custom', label: 'From a source you added: its trust differs' }),
    policy: (v) => (!v || !v.ver ? null : v.kind === 'pinned' ? { icon: 'pin', value: `Held at ${v.ver}`, tone: 'held', label: 'Held at this version until you change it' } : v.kind === 'range' ? { icon: 'brackets', value: `Updates within ${v.range}`, label: `Takes new releases within ${v.range}` } : { icon: 'refresh-cw', value: 'Stays current', label: 'Takes every new release' }),
    version: (ver) => (ver ? { icon: 'tag', value: ver, label: 'The installed version' } : null),
    latest: (ver) => (ver ? { icon: 'tag', value: ver, label: 'The newest release' } : null),
    update: (ver) => (ver ? { icon: 'circle-arrow-up', value: `${ver} available`, tone: 'update', label: 'A newer version can be installed' } : null),
    env: (env) => (!env ? null : /^prod/i.test(env) ? { prod: true, value: 'Production', label: 'Production' } : { icon: 'server', value: String(env).toLowerCase(), label: 'Environment' }),
  };
  // a code language's or an agent runtime's own glyph, where the brand has one (67); null for the generic icon
  // (a brand mark where the language has one, else a Lucide icon: a terminal for a command, braces for jq/yq, a function for CEL)
  const KIND_GLYPH = { engine: { 'go-embed': 'go', go: 'go', python: 'python', ts: 'typescript', typescript: 'typescript', js: 'javascript', javascript: 'javascript', lua: 'lua', wasm: 'webassembly' }, runtime: { claude: 'claude', 'claude-code': 'claude', container: 'docker' } };
  const KIND_ICON = { engine: { cli: 'terminal', command: 'terminal', cel: 'square-function', jq: 'braces', yq: 'braces', starlark: 'star', risor: 'binary' }, runtime: { cli: 'terminal' } };
  UI.kindGlyph = (kind, name) => { const g = (KIND_GLYPH[kind] || {})[name], i = (KIND_ICON[kind] || {})[name]; return g ? svcGlyph(g) : i ? UI.icon(i, 'fx-ico') : null; };
  // the production square (the env glyph): production is never plain text, and never the waiting triangle
  const PROD_SQ = '<svg viewBox="0 0 10 10" width="10" height="10" aria-hidden="true"><rect x=".5" y=".5" width="9" height="9" rx="1.8" fill="currentColor"/></svg>';
  // a fact's link inside a row that is itself a link: it opens the fact's own page, never the row's
  const fxGo = (el, e) => { e.preventDefault(); e.stopPropagation(); const h = el.dataset.fxHref; if (el.dataset.fxExt) UI.toast('Opens outside the dashboard', UI.esc(h), 'idle'); else UI.go(h); };
  document.addEventListener('click', (e) => { const el = e.target.closest && e.target.closest('[data-fx-href]'); if (el) fxGo(el, e); }, true);
  document.addEventListener('keydown', (e) => { if ((e.key === 'Enter' || e.key === ' ') && e.target.matches && e.target.matches('[data-fx-href]')) fxGo(e.target, e); }, true);
  /** What kind of action, in words, with its icon and whether it's risky: from the verb and where it lands. */
  const ACT_FACT = { read: ['act-read', 'Reads'], post: ['act-post', 'Posts a message'], push: ['act-push', 'Pushes code'], change: ['act-change', 'Changes a setting'], infra: ['act-infra', 'Changes infrastructure'], money: ['act-money', 'Spends money'], destroy: ['act-destroy', 'Deletes things'], permission: ['act-permission', 'Changes permissions'] };
  // graded prominence (45b): what can't be taken back or costs money is the strongest (danger); changing infrastructure is
  // amber, like production; reads and reversible changes stay quiet
  // graded (45b, the brand's risk grades): 0 reads and reversible, quiet; 1 changes something, full ink; 2 infrastructure,
  // amber with the hammer; 3 money, deleting, widening access, danger with the icon cut out of a solid tile
  const ACT_GRADE = { read: [0, 'eye'], post: [1, 'message-square'], push: [1, 'git-commit-horizontal'], change: [1, 'pencil-line'], infra: [2, 'hammer'], money: [3, 'banknote'], destroy: [3, 'trash-2'], permission: [3, 'lock-open'] };
  const GRADE_TONE = [null, 'change', 'warning', 'danger'];
  UI.actionFact = (verb, env, tier) => { const a = UI.actionOf(verb), [, value] = ACT_FACT[a] || ACT_FACT.change, [g, icon] = ACT_GRADE[a] || ACT_GRADE.change; return { icon, value, pri: 2, tone: GRADE_TONE[g], label: 'What it does' }; };
  UI.envFact = (env) => (env ? { icon: 'server', value: env, prod: env === 'production', pri: 1, label: 'Environment' } : null);
  /** A ticket id in prose ("(task c29)", the ledger's word) said in words: the task's own name; as a link where the page can carry one. */
  UI.taskWords = (text, link = false) => UI.esc(text).replace(/\(task (c\d+)\)/g, (m, id) => { const c = (D.cases || []).find((x) => x.id === id); if (!c) return ''; const name = `the ticket “${UI.esc(c.title)}”`;
    return link ? `(<a class="dl-link" href="#/tickets/${id}">${name}</a>)` : `(${name})`; });
  /** A set of runs in words whose parts add up to the whole: "19 went fine · 2 failed · 3 running". */
  UI.runParts = (rs) => [[rs.filter((r) => r.status === 'ok').length, 'went fine'], [rs.filter((r) => r.status === 'fail' || r.status === 'stopped').length, 'failed'], [rs.filter((r) => r.status === 'run').length, 'running'], [rs.filter((r) => r.status === 'wait').length, 'waiting on you'], [rs.filter((r) => r.status === 'idle').length, 'parked']].filter(([n]) => n).map(([n, t]) => `${n} ${t}`).join(' · ');
  /** The tab bars on the page, each as the set of places its tabs go: two renders that share one are tabs of one page. */
  const tabSigs = (root) => [...root.querySelectorAll('.dl-subnav, .tabs, .dl-tabs, [role="tablist"]')].map((t) => [...t.querySelectorAll('a[href]')].map((a) => a.getAttribute('href').split('?')[0]).sort().join(' ')).filter(Boolean);
  UI.render = (opts = {}) => {
    UI.syncRunStats(); UI.syncAttention();
    const prev = tabSigs($('#main')), prevTop = $('#main').scrollTop;
    let keep = opts.keepScroll ? prevTop : 0;
    cleanup.forEach((f) => f()); cleanup = [];
    UI.closeMenu && UI.closeMenu();
    const path = (location.hash.replace(/^#\/?/, '') || 'overview').split('?')[0];
    for (const r of routes) {
      const mt = path.match(r.re);
      if (!mt) continue;
      const params = {}; r.keys.forEach((k, i) => (params[k] = mt[i + 1] ? decodeURIComponent(mt[i + 1]) : undefined));
      const main = $('#main');
      const out = r.fn(params);
      main.innerHTML = String(out);
      // switching tabs keeps the page still: no entrance, the frame and the tab bar where they were (the scroll kept,
      // up to the tab bar, so a tab opens at its top when you'd scrolled past it); only the content below changes
      const tabSwitch = !opts.keepScroll && tabSigs(main).some((x) => prev.includes(x));
      main.classList.toggle('still', !!opts.keepScroll || tabSwitch);
      if (tabSwitch) {
        // a shorter tab keeps the page's height for this view (set before anything is measured, so the scroll is never
        // clamped first), and the scroll comes back instantly, not smoothly: the tab bar doesn't move at all
        if (main.firstElementChild) main.firstElementChild.style.minHeight = prevTop + main.clientHeight + 'px';
        const bar = main.querySelector('.dl-subnav, .tabs, .dl-tabs, [role="tablist"]'), barTop = bar ? bar.getBoundingClientRect().top - main.getBoundingClientRect().top + prevTop : 0;
        keep = Math.min(prevTop, Math.max(0, barTop - 8));
      }
      main.scrollTo({ top: keep, behavior: 'instant' });
      UI.params = params;
      // the review page keeps the section you came from highlighted (it belongs to whatever you were changing)
      UI.setNav(path === 'config/apply' && D.prefs && D.prefs.returnTo ? (D.prefs.returnTo.href.replace(/^#\/?/, '').split(/[/?]/)[0] || 'overview') : (typeof r.opts.nav === 'function' ? r.opts.nav(params) : r.opts.nav) || path.split('/')[0]);
      document.body.classList.toggle('bare', !!r.opts.bare); // signed out: no rail, top bar or tab bar
      if (r.opts.after) r.opts.after(params, main);
      if (typeof out === 'object' && out && out.after) out.after(main);
      UI.afterRender.forEach((f) => f(main));
      UI.fitSteppers(main);
      return;
    }
    { const pt = $('#ph-title'); if (pt) pt.textContent = 'Not found'; $$('[data-nav]').forEach((a) => a.classList.remove('on')); } // the header never keeps the last page's name
    $('#main').innerHTML = `<div class="page"><div class="empty"><span class="serif">No such page</span><a class="btn" href="#/overview">Back to overview</a></div></div>`;
  };
  UI.afterRender = [];
  // belt and braces: overflow:hidden can still be scrolled by script, focus or find-in-page; the window and the main
  // column snap back to their left edge if anything ever moves them sideways
  addEventListener('scroll', () => { if (scrollX) scrollTo(0, scrollY); }, { passive: true });
  document.addEventListener('scroll', (e) => { const t = e.target; if (t && t.classList && (t.classList.contains('main') || t.classList.contains('shell')) && t.scrollLeft) t.scrollLeft = 0; }, { capture: true, passive: true });
  // a link can ask for a section of a page (?show=<id>): it scrolls into view and takes the focus outline briefly
  UI.afterRender.push((main) => { const m = location.hash.match(/[?&]show=([\w-]+)/); const el = m && main.querySelector('#' + m[1]); if (el) setTimeout(() => { el.scrollIntoView({ block: 'start' }); el.classList.add('flash-target'); setTimeout(() => el.classList.remove('flash-target'), 1600); }, 30); });
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
    UI.prevHash = lastHash; lastHash = location.hash;
    document.querySelectorAll('.drawer-scrim').forEach((d) => d.remove()); UI.closeModal(); UI.render();
  });
  window.addEventListener('beforeunload', (e) => { if (UI.guard && UI.guard()) { e.preventDefault(); e.returnValue = ''; } });

  /* ---------- nav ---------- */
  // the rail, as in Daylight's mocks (design/brand/v2): the work, then Settings; only "Needs you" is counted hot
  const navGroups = [
    ['', [['overview', 'Home', 'overview'], ['chat', 'Chat', 'message', () => (UI.chatUnreadCount ? UI.chatUnreadCount() : 0), true], ['attention', 'Needs you', 'attention', () => D.attention.length, true], ['activity', 'Activity', 'live'], ['tickets', 'Tickets', 'ticket'], ['workflows', 'Workflows', 'workflows'], ['bots', 'Bots', 'bots'], ['browser', 'Browsers', 'globe']]],
    ['Settings', [['connectors', 'Plugins', 'connectors'], ['extensions', 'Packs', 'extensions'], ['secrets', 'Secrets', 'secrets'], ['system', 'System', 'settings']]],
  ];
  UI.navGroups = navGroups;
  UI.pageTitle = (nav) => { const k = nav || (location.hash.replace(/^#\/?/, '') || 'overview').split(/[/?]/)[0]; const map = { live: 'activity', runs: 'activity', teams: 'bots', folders: 'bots', config: 'system', updates: 'system', logs: 'system', security: 'system', safety: 'system', more: 'more', preferences: 'preferences' }; const key = map[k] || k; const hit = navGroups.flatMap(([, x]) => x).find(([n]) => n === key); return hit ? hit[1] : key === 'more' ? 'More' : key === 'preferences' ? 'Preferences' : key === 'glossary' ? 'Glossary' : 'conductor'; };
  UI.navHTML = () => navGroups.map(([g, items]) => `<div class="nav-group">${g ? `<span class="grp">${g}</span>` : ''}<nav class="nav">${items.map(([k, l, ic, c, hot]) => {
    const n = c ? c() : null;
    return `<a href="#/${k}" data-nav="${k}">${UI.icon(ic)}<span class="lbl">${l}</span>${n ? UI.count(n, { hot }) : ''}</a>`;
  }).join('')}</nav></div>`).join('');
  // a page opened from Approvals (or another nav section) keeps that section highlighted, and its Back returns there
  document.addEventListener('click', (e) => { const a = e.target.closest && e.target.closest('a[href^="#/"]'); if (!a) return; const from = location.hash.replace(/^#\/?/, '').split(/[/?]/)[0]; const to = a.getAttribute('href'); if (['attention', 'overview'].includes(from) && /^#\/bots\/[^/]+\/proposals\//.test(to)) UI.navOrigin = { hash: to, nav: from === 'overview' ? 'overview' : 'attention', back: '#/' + from }; else if (!a.closest('.tabbar, .rail')) UI.navOrigin = UI.navOrigin && UI.navOrigin.hash === to ? UI.navOrigin : null; else UI.navOrigin = null; }, true);
  // pages that live inside another nav destination
  const NAV_ALIAS = { live: 'activity', runs: 'activity', teams: 'bots', folders: 'bots', config: 'system', updates: 'system', logs: 'system', security: 'system', safety: 'system' };
  UI.setNav = (k) => {
    k = NAV_ALIAS[k] || k;
    if (UI.navOrigin && location.hash === UI.navOrigin.hash) { k = UI.navOrigin.nav; UI.backHref = UI.navOrigin.back; } else UI.backHref = null;
    // the phone header names the section on every render (it was set once, at boot, and went stale)
    const pt = $('#ph-title'); if (pt) pt.textContent = UI.pageTitle(k);
    $$('[data-nav]').forEach((a) => { const on = a.dataset.nav === k; a.classList.toggle('on', on); if (on && location.hash.replace(/^#\/?/, '') === a.dataset.nav) a.setAttribute('aria-current', 'page'); else a.removeAttribute('aria-current'); });
  };

  /* ---------- modal / toast / tooltip ---------- */
  /* Focus, for every dialog: it opens on the dialog itself (not a button, so Enter can never act by accident; a form
     then focuses its first field and a destructive confirm its Cancel), Tab stays inside the topmost dialog, and
     closing returns focus to whatever opened it. */
  const focusInto = (box) => { if (!box) return; const lab = box.querySelector('h2, h1'); if (lab) { lab.id = lab.id || 'dlg-' + Math.random().toString(36).slice(2, 8); box.setAttribute('aria-labelledby', lab.id); }
    setTimeout(() => { if (box.isConnected && !box.contains(document.activeElement)) box.focus({ preventScroll: true }); }); };
  const FOCUSABLE = 'a[href], button:not([disabled]), input:not([type=hidden]):not([disabled]), select:not([disabled]), textarea:not([disabled]), summary, [tabindex]:not([tabindex="-1"])';
  UI.topDialog = () => { const all = [...document.querySelectorAll('.palette, #scrim .modal, .drawer-scrim .cdrawer')]; return all.sort((a, b) => (a.compareDocumentPosition(b) & Node.DOCUMENT_POSITION_FOLLOWING ? -1 : 1)).pop() || null; };
  document.addEventListener('keydown', (e) => {
    if (e.key !== 'Tab') return; const top = UI.topDialog(); if (!top) return;
    const els = [...top.querySelectorAll(FOCUSABLE)].filter((x) => x.getClientRects().length && !x.closest('details:not([open]) > :not(summary)')); if (!els.length) { e.preventDefault(); top.focus(); return; }
    const first = els[0], last = els[els.length - 1], at = document.activeElement;
    if (!top.contains(at)) { e.preventDefault(); (e.shiftKey ? last : first).focus(); }
    else if (e.shiftKey && (at === first || at === top)) { e.preventDefault(); last.focus(); }
    else if (!e.shiftKey && at === last) { e.preventDefault(); first.focus(); }
  });
  // closing however it closes (Esc, Cancel, the scrim, a handler removing it) hands focus back to the opener
  new MutationObserver((ms) => ms.forEach((m) => m.removedNodes.forEach((n) => { const o = n._opener;
    if (o && o.isConnected && (!document.activeElement || document.activeElement === document.body || !document.activeElement.isConnected)) o.focus({ preventScroll: true }); }))).observe(document.body, { childList: true });
  UI.modal = (html, opts = {}) => {
    UI.closeModal();
    const s = document.createElement('div');
    s.className = 'scrim'; s.id = 'scrim';
    s.innerHTML = `<div class="modal ${opts.wide ? 'wide' : ''} ${opts.cls || ''}" role="dialog" aria-modal="true" tabindex="-1"><span class="sheet-grip only-phone" aria-hidden="true"></span>${html}</div>`;
    s.addEventListener('click', (e) => { if (e.target === s && !opts.sticky) UI.dismissModal(); });
    s._opener = document.activeElement;
    document.body.appendChild(s);
    focusInto($('.modal', s));
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
    d.innerHTML = `<aside class="cdrawer" role="dialog" aria-modal="true" tabindex="-1">${html}</aside>`;
    d._opener = document.activeElement;
    document.body.appendChild(d);
    focusInto($('.cdrawer', d));
    d.querySelectorAll('[data-close]').forEach((b) => b.addEventListener('click', (e) => { e.stopPropagation(); guarded(d, () => d.remove()); }));
    return d;
  };
  UI.closeDrawer = () => { const all = document.querySelectorAll('.drawer-scrim'); if (all.length) { all[all.length - 1].remove(); return true; } return false; };
  /** A toast: an event, past tense and specific. 6 seconds; errors stay until dismissed. s: ok | fail | wait | run | ember. */
  UI.toast = (title, sub, s = 'ember', action = null) => {
    let box = $('.toasts'); if (!box) { box = document.createElement('div'); box.className = 'toasts'; box.setAttribute('aria-live', 'polite'); document.body.appendChild(box); }
    const t = document.createElement('div'); t.className = 'toast'; t.dataset.s = s === 'ok' ? 'success' : s; t.setAttribute('role', s === 'fail' ? 'alert' : 'status');
    const g = { ok: 'success', fail: 'danger', wait: 'warning', run: 'running', ember: 'needs-you', idle: 'neutral' }[s] || 'info';
    t.innerHTML = `<span class="st" data-s="${s === 'ok' ? 'success' : s}"><i>${UI.glyph(g)}</i></span><div><b>${title}</b>${sub ? `<p>${sub}</p>` : ''}</div>${s === 'fail' ? `<button class="icon-btn toast-x" aria-label="Dismiss">${UI.icon('x')}</button>` : '<span></span>'}`;
    const x = t.querySelector('.toast-x'); if (x) x.addEventListener('click', () => t.remove());
    if (action) { const ab = document.createElement('button'); ab.type = 'button'; ab.className = 'btn sm ghost toast-act'; ab.textContent = action.label; ab.addEventListener('click', () => { t.remove(); action.fn(); }); t.lastElementChild.replaceWith(ab); }
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
        ${p.kind === 'terraform' ? `<div class="kv small"><dt>action</dt><dd class="mono">terraform apply · ${p.tf.repo} · ${p.tf.dir} · ${p.tf.workspace}</dd><dt>plan</dt><dd class="mono">+${p.tf.add} ~${p.tf.change} -${p.tf.destroy} · ${UI.statusMark('wait', `replaces ${p.tf.replaces.join(', ')}`, { sm: true })}</dd></div>` : UI.payloadHTML ? UI.payloadHTML(p) : ''}
        ${deny ? `<div class="field"><label for="seal-reason">Tell the bot why <span class="opt">optional</span></label><textarea class="textarea" id="seal-reason" rows="2" maxlength="500" placeholder="e.g. Not in #incidents; post it to #finance instead."></textarea></div>` : ''}
        <details class="tech"><summary>The exact payload the passkey signs</summary><div class="digest">{ bot: <b>${p.bot}</b>, proposal: <b>${p.id}</b>, decision: <b>${decision}</b>, action_sha256: <b>${p.digest.replace('sha256:', '')}</b>, nonce: <b>7Qx2…f9</b> }</div></details>
        <div class="seal" id="seal">
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
    UI.syncAttention();
    const d = D.daemon, need = D.attention.length, running = D.runs.filter((r) => r.status === 'run').length;
    // the top bar is search and your account; whatever needs a person is in Needs you (the one attention signal), and
    // the machine and its updates are the card at the foot of the rail
    void d; void running;
    $('#strip-cells').innerHTML = `
      <div class="spacer"></div>
      ${D.pending.length ? `<a class="cell staged" href="#/config/apply">${UI.statusMark('staged', `${D.pending.length} not applied`)}</a>` : ''}
      <button type="button" class="cell op-btn" data-act="op-menu" aria-label="${D.operator.name}: account and display"><span class="avatar">${D.operator.name.slice(0, 2)}</span></button>`;
    $('#rail-nav').innerHTML = UI.navHTML();
    $('#rail-host').innerHTML = UI.hostCard();
    $('#tabbar').innerHTML = [['overview', 'Home', 'overview'], ['attention', 'Needs you', 'attention', need, true], ['chat', 'Chat', 'message', UI.chatUnreadCount ? UI.chatUnreadCount() : 0, true], ['activity', 'Activity', 'live'], ['more', 'More', 'more']].map(([k, l, ic, c, hot]) => `<a href="#/${k}" data-nav="${k}">${UI.icon(ic)}<span>${l}</span>${c ? UI.count(c, { hot: !!hot }) : ''}</a>`).join('');
    UI.setNav((location.hash.replace(/^#\/?/, '') || 'overview').split('/')[0]);
  };

  UI.boot = () => {
    window.D = D;
    document.body.innerHTML = `
      <a class="skip" href="#main" data-act="skip-main">Skip to the page</a>
      <div class="shell">
        <aside class="rail">
          <a class="brand" href="#/overview" aria-label="conductor: home">${UI.logo('lockup')}</a>
          <div id="rail-nav"></div>
          <div id="rail-host" class="rail-host"></div>
        </aside>
        <header class="strip">
          <a class="brand ph-brand" href="#/overview" aria-label="conductor: home">${UI.logo('mark')}<b id="ph-title"></b></a>
          <div class="search" role="button" tabindex="0" data-act="palette" aria-label="Search runs, bots and settings">${UI.icon('search')}<span class="lbl">Search runs, bots and settings</span><span class="kbd">⌘K</span></div>
          <div id="strip-cells" style="display:contents"></div>
        </header>
        <main class="main" id="main" tabindex="-1"></main>
        <nav class="tabbar" id="tabbar"></nav>
      </div>`;
    UI.initTheme();
    UI.loadLogo();
    UI.refreshChrome();
    UI.render();
  };

  /* small shared bits */
  /** The summary header: crumbs, the title (the serif for a short human title; gen: the UI face for generated ones),
      one sentence that answers the page's question (lede), an optional detail line (machine detail last), actions. */
  /** The one way back from a page you drilled into: the page's whole path, each step a link, the page itself last
      (Bots › platform › platform-sre › Approvals › Replace the production web server). No "‹ Back" links anywhere. */
  UI.crumbs = (items) => `<nav class="dl-crumbs" aria-label="Where you are">${items.filter(Boolean).map((it, i, xs) => `${i ? '<span class="sep" aria-hidden="true">›</span>' : ''}${i === xs.length - 1 && !it[1] ? `<span class="cur" aria-current="page">${UI.esc(it[0])}</span>` : `<a href="${it[1]}">${UI.esc(it[0])}</a>`}`).join('')}</nav>`;
  UI.head = (title, opts = {}) => `<div class="page-head"><div>${Array.isArray(opts.crumbs) ? UI.crumbs(opts.crumbs) : opts.crumbs ? `<div class="crumbs">${opts.crumbs}</div>` : ''}<h1${opts.gen ? ' class="gen dl-h1-long"' : ''}>${title}</h1>${opts.sub ? `<p class="sub">${String(opts.sub).replace(/^([a-z])/, (c) => c.toUpperCase())}</p>` : ''}${opts.lede ? `<p class="lede">${opts.lede}</p>` : ''}${opts.lede2 ? `<p class="lede-2">${opts.lede2}</p>` : ''}${opts.detail ? `<div class="head-detail">${opts.detail}</div>` : ''}</div>${opts.actions ? `<div class="actions">${opts.actions}</div>` : ''}</div>`;
  /** The System area's own navigation: the rarely visited pages, one sidebar item (Settings → System). */
  UI.systemTabs = (on) => `<nav class="dl-tabs dl-subnav" aria-label="System">${[['config', 'Config', '#/config'], ['safety', 'Safety', '#/safety', D.daemon.paused ? '!' : ''], ['updates', 'Updates', '#/updates', D.daemon.update && D.daemon.update.blocked ? '!' : ''], ['logs', 'Logs & audit', '#/logs'], ['security', 'Access', '#/security']].map(([k, l, h, n]) => `<a href="${h}" class="${k === on ? 'on' : ''}"${k === on ? ' aria-current="page"' : ''}>${l}${n ? ` ${UI.count(n, { hot: true })}` : ''}</a>`).join('')}</nav>`;
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
  // the skip link lands on the page's decision when there is one (an approval's Approve), else on the page itself
  UI.act('skip-main', (b, e) => { if (e) e.preventDefault(); const t = $('#main .prop-decide .btn'), m = $('#main'); if (t) t.focus(); else m.focus({ preventScroll: true }); });

  const disabledEl = (el) => el.disabled || el.getAttribute('aria-disabled') === 'true';
  document.addEventListener('click', (e) => {
    const el = e.target.closest('[data-act]');
    if (!el) return;
    if (disabledEl(el)) { e.preventDefault(); e.stopPropagation(); UI.flashReason(el); return; }
    // a double-click is one action: its second click (detail 2) never stages, signs or applies twice
    if (e.detail > 1 && !el.matches('input, select, textarea, label')) { e.preventDefault(); e.stopPropagation(); return; }
    const fn = UI.acts[el.dataset.act];
    // a checkbox or radio keeps its own toggle (preventing it made a tick look ignored until the re-render)
    if (fn) { if (!el.matches('input[type="checkbox"], input[type="radio"]')) e.preventDefault(); e.stopPropagation(); fn(el, e); }
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
  /** A wizard stepper, the one implementation: steps = labels, at = current index (done before it). opts.states overrides
      each step's state ('done' | 'cur' | 'bad' | ''); opts.id names it. The full row of labelled steps shows when it fits;
      when it doesn't (a phone, a narrow column), UI.fitSteppers switches it to "Step 2 of 5 · Watches" and a thin bar.
      It never wraps, clips or overlaps. */
  UI.stepper = (steps, at, bad, o = {}) => { const st = o.states || steps.map((x, i) => (i < at ? 'done' : i === at ? (bad ? 'cur bad' : 'cur') : '')), cur = Math.max(0, st.findIndex((x) => /cur|bad/.test(x))), done = st.filter((x) => x === 'done').length, n = steps.length, k = st.every((x) => x === 'done') ? n : cur + 1;
    return `<div class="stepper apply-steps" role="list" aria-label="Step ${Math.min(k, n)} of ${n}: ${UI.esc(String(steps[Math.min(k, n) - 1]).replace(/<[^>]+>/g, ''))}"${o.id ? ` id="${o.id}"` : ''} data-n="${n}">${steps.map((x, i) => `<div class="as ${st[i]}" role="listitem"${/cur|bad/.test(st[i]) ? ' aria-current="step"' : ''}><b>${st[i] === 'done' ? '✓' : i + 1}</b><span class="as-l">${x}</span></div>`).join('')}<div class="step-compact" aria-hidden="true"><span class="sc-t">Step ${Math.min(k, n)} of ${n} · <b>${steps[Math.min(k, n) - 1]}</b></span><span class="step-bar"><i style="width:${(Math.max(done, k - 1 + (st[cur] && /cur/.test(st[cur]) ? 0.5 : 0)) / n) * 100}%"></i></span></div></div>`; };
  /** Show each stepper in full only when its labelled steps fit; otherwise the compact form. Run after every render. */
  UI.fitSteppers = (root = document) => { root.querySelectorAll('.stepper').forEach((el) => { el.classList.remove('compact'); if (el.scrollWidth > el.clientWidth + 1) el.classList.add('compact'); }); };
  window.addEventListener('resize', () => UI.fitSteppers());
  /** Segmented control: items [[value, label]]; act receives el.dataset.v. */
  UI.seg = (items, value, act, extra = '') => `<div class="seg" role="group" ${extra}>${items.map(([v, l, dis]) => `<button type="button" data-v="${UI.esc(v)}" ${dis ? UI.off(dis) : `data-act="${act}"`} aria-pressed="${String(v) === String(value)}" class="${String(v) === String(value) ? 'on' : ''}">${l}</button>`).join('')}</div>`;
  UI.switchBtn = (on, act, attrs = '') => `<button type="button" role="switch" class="switch${on ? ' on' : ''}" aria-checked="${!!on}" data-act="${act}" ${attrs}></button>`;
  /** Demo data (prototype only): which fake day to show, chosen per browser (js/scenario.js), or reset the current one. */
  UI.DEMOS = [['calm', 'A mostly healthy day', 'Dozens of runs; one approval and one failure need you'], ['quiet', 'All quiet', 'Nothing needs you; every system healthy'], ['full', 'Everything at once', 'Every kind of exception, to try each flow']];
  UI.setDemo = (k) => { try { localStorage.setItem('cd-demo', k); localStorage.removeItem('cd-proto-state-v6'); } catch (e) {} location.reload(); };
  UI.act('demo-menu', (b) => UI.menu(b, UI.DEMOS.map(([k, t]) => [`${D.demo === k ? '● ' : ''}${t}`, k === 'quiet' ? 'check' : k === 'full' ? 'grid' : 'sun', 'demo-set', `data-k="${k}"`]).concat([['-'], ['Reset this day’s demo data', 'undo', 'reset-demo']])));
  UI.act('demo-set', (b) => { UI.closeMenu(); UI.setDemo(b.dataset.k); });
  /** Pause or resume dispatch: weighty, so it says what it does and asks first. Lives in System → Safety (and ⌘K). */
  UI.setPaused = (on) => { D.daemon.paused = on; D.audit.unshift({ ts: new Date().toISOString(), event: on ? 'daemon_pause' : 'daemon_resume', who: 'operator:ops', detail: 'dispatch ' + (on ? 'paused' : 'resumed') + ' from the dashboard', run: '' }); S.save(); UI.refreshChrome(); UI.rerender(); UI.toast(on ? 'All automation paused' : 'Automation resumed', on ? 'Runs already going finish; nothing new starts until you resume.' : 'New events start their runs again.', on ? 'wait' : 'ok'); };
  UI.act('pause', () => { UI.closeMenu(); const on = !D.daemon.paused;
    UI.confirm(on ? { title: 'Pause all automation?', body: 'Nothing new starts until you resume: no workflow runs and no bot picks up new work. Runs already going finish. <span class="dl-sub">Events that arrive while paused may not be kept. Resume from System → Safety or from Needs you.</span>', ok: 'Pause all automation', danger: true } : { title: 'Resume automation?', body: 'conductor starts dispatching again; new events start their runs.', ok: 'Resume' }).then((ok) => { if (ok) UI.setPaused(on); }); });
  UI.act('strip-go', (b) => { UI.closeMenu(); UI.go(b.dataset.href); });
  /** The avatar menu: who you are, the display preferences, the glossary, the prototype's demo data, and Lock. */
  UI.act('op-menu', (b) => UI.menu(b, [[`Signed in as ${D.operator.name}`, 'shield', 'strip-go', 'data-href="#/security"'], ['-'], [document.documentElement.dataset.theme === 'dark' ? 'Light theme' : 'Dark theme', document.documentElement.dataset.theme === 'dark' ? 'sun' : 'moon', 'theme-menu'], ['Preferences', 'settings', 'strip-go', 'data-href="#/preferences"'], ...(D.prefs.gsHidden ? [['Show Get started', 'check', 'gs-show']] : []), ['Glossary', 'info', 'strip-go', 'data-href="#/glossary"'], ['Demo data…', 'sparkle', 'demo-menu'], ['-'], ['Lock', 'lock', 'strip-go', 'data-href="#/signin"']]));
  UI.act('theme-menu', () => { UI.closeMenu(); UI.theme(document.documentElement.dataset.theme === 'dark' ? 'light' : 'dark'); });
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
        <div class="seal" id="su-seal">
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
  UI.closeMenu = () => { const m = $('#pop-menu'); if (!m) return; const back = m.contains(document.activeElement) && m._opener; m.remove(); if (back && back.isConnected) back.focus({ preventScroll: true }); };
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
    // a menu is a menu to the keyboard too: it takes focus, ↑/↓ (and Home/End) move, Esc or Tab closes it
    m._opener = anchor; const menuItems = () => [...m.querySelectorAll('[role="menuitem"]')];
    m.addEventListener('keydown', (e) => { const xs = menuItems(), i = xs.indexOf(document.activeElement);
      if (e.key === 'ArrowDown' || e.key === 'ArrowUp') { e.preventDefault(); xs[(i + (e.key === 'ArrowDown' ? 1 : xs.length - 1)) % xs.length].focus(); }
      else if (e.key === 'Home' || e.key === 'End') { e.preventDefault(); (e.key === 'Home' ? xs[0] : xs[xs.length - 1]).focus(); }
      else if (e.key === 'Escape') { e.preventDefault(); e.stopPropagation(); UI.closeMenu(); }
      else if (e.key === 'Tab') UI.closeMenu(); });
    const firstItem = menuItems()[0]; if (firstItem) firstItem.focus({ preventScroll: true });
  };

  /** A field's label: its own, else its key in sentence case with the usual acronyms (app_id → "App ID"). */
  UI.fieldLabel = (fd) => fd.label || fd.k.split('.').pop().replace(/^set__/, '').replace(/_/g, ' ').replace(/\b(id|url|api|ssh|otp|tls|sha|uri|dns|ip)\b/gi, (x) => x.toUpperCase()).replace(/^./, (c) => c.toUpperCase());
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
    const id = fieldId(fd.k, pre), lbl = UI.fieldLabel(fd);
    const req = fd.req ? '<span class="req" aria-label="required">*</span>' : '<span class="opt">optional</span>';
    const help = fd.help ? `<div class="hint">${UI.ph(fd.help)}</div>` : '';
    const val = v == null ? '' : v;
    let input;
    switch (fd.type) {
      case 'text': case 'code': input = `<textarea class="textarea${fd.type === 'code' ? ' mono' : ''}" id="${id}" data-k="${fd.k}" rows="${fd.rows || 3}" placeholder="${UI.esc(fd.ph || '')}">${UI.esc(val)}</textarea>`; break;
      case 'number': input = `<input class="input mono" id="${id}" data-k="${fd.k}" inputmode="decimal" value="${UI.esc(val)}" placeholder="${UI.esc(fd.ph || '')}">`; break;
      case 'bool': input = `<label class="row small" style="min-height:30px"><input type="checkbox" class="cbx" id="${id}" data-k="${fd.k}" ${val ? 'checked' : ''}> ${fd.on || 'on'}</label>`; break;
      case 'enum': case 'host': {
        if (fd.type === 'host') fd = Object.assign({ create: 'host', createLabel: 'New host' }, fd);
        // one empty choice at most: an optional field adds "— none —" only when its options don't already name one ("top level")
        const hasEmpty = (fd.options || []).some((o) => (Array.isArray(o) ? o[0] : o) === '');
        const opts = fd.type === 'host' ? [''].concat(S.view().hosts.map((h) => h.name)) : (fd.req || hasEmpty ? [] : ['']).concat(fd.options || []);
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
      <form class="modal-body stack" novalidate id="${ids.form}" data-pre="${pre}">${spec.intro ? `<div class="small dim">${UI.ph(spec.intro)}</div>` : ''}
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
    // the review remembers where you came from: its Back and its Done return there, and the rail keeps that section
    if (opts.go !== false) { if (!D.prefs.returnTo && !location.hash.startsWith('#/config')) { D.prefs.returnTo = { href: location.hash, label: (document.querySelector('#main h1') || {}).textContent || UI.pageTitle() }; S.save(); } UI.go('#/config/apply'); }
    else UI.toast(`Staged: ${UI.esc(ch.summary)}`, `${D.pending.length} change${D.pending.length > 1 ? 's' : ''} waiting · review in Config → apply`, 'run');
  };
  /** A tab bar that doesn't fit never cuts a label: the tabs that don't fit go into a "More" menu at its end, and the
      current tab always stays in view (it takes the place of the last one that fits). Hidden tabs stay in the DOM, so
      the bar's set of places is the same on every tab (UI.render keeps tab switches still). */
  UI.fitTabs = (t) => { const tabs = [...t.querySelectorAll(':scope > a')]; t.querySelector(':scope > .tab-more')?.remove(); tabs.forEach((a) => a.classList.remove('tab-over'));
    if (t.scrollWidth <= t.clientWidth + 1 || tabs.length < 3) return;
    const gap = parseFloat(getComputedStyle(t).columnGap) || 16, room = t.clientWidth - 76, w = tabs.map((a) => a.getBoundingClientRect().width), on = tabs.findIndex((a) => a.classList.contains('on'));
    let used = 0, keep = []; tabs.forEach((a, i) => { if (used + w[i] <= room) { keep.push(i); used += w[i] + gap; } });
    if (on >= 0 && !keep.includes(on)) { while (keep.length && keep.reduce((x, i) => x + w[i] + gap, 0) + w[on] > room) keep.pop(); keep.push(on); keep.sort((x, y) => x - y); }
    const over = tabs.filter((a, i) => !keep.includes(i)); if (!over.length) return;
    over.forEach((a) => a.classList.add('tab-over'));
    const hot = over.some((a) => a.querySelector('.hot, em.hot'));
    const b = document.createElement('button'); b.type = 'button'; b.className = 'tab-more' + (hot ? ' hot' : ''); b.dataset.act = 'tab-more'; b.setAttribute('aria-haspopup', 'menu');
    b.innerHTML = `More${UI.icon('chevd')}`; b.setAttribute('aria-label', `More tabs: ${over.map((a) => a.textContent.trim()).join(', ')}`); t.appendChild(b); };
  let fitT = 0; addEventListener('resize', () => { clearTimeout(fitT); fitT = setTimeout(() => document.querySelectorAll('#main .tabs, #main .dl-tabs').forEach(UI.fitTabs), 120); });
  UI.act('tab-more', (b) => UI.menu(b, [...b.parentElement.querySelectorAll(':scope > a.tab-over')].map((a) => [a.textContent.trim().replace(/\s+/g, ' '), 'chev', 'strip-go', `data-href="${a.getAttribute('href')}"`])));
  /* phone polish after every render: tab rows fit (UI.fitTabs);
     page-head actions beyond the first two fold into a ⋯ menu (the originals stay, hidden, and get the click) */
  UI.afterRender.push((main) => {
    main.querySelectorAll('.tabs, .dl-tabs').forEach(UI.fitTabs);
    // a responsive table's cells carry their column's name, so a phone can show it beside the value
    main.querySelectorAll('table.responsive').forEach((tb) => { const hs = [...tb.querySelectorAll('thead th')].map((th) => th.textContent.trim()); tb.querySelectorAll('tbody tr').forEach((tr) => [...tr.children].forEach((td, i) => { if (!td.dataset.l && hs[i] && !td.classList.contains('sel')) td.dataset.l = hs[i]; })); });
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
  /** The selection bar, one pattern for every list with bulk select: while anything is checked it replaces the list's
      header (or filter) row in place, pinned at the top while you scroll: "3 selected", the actions that apply to the
      selection [[label, icon, act, attrs?, offReason?]], "Select all N matching" and Clear. o: { all: n, allAct } */
  UI.bulkBar = (n, noun, actions, clearAct, o = {}) => `<div class="dl-selbar" role="region" aria-label="Selection"><b>${n} selected</b>${actions.map(([l, ic, act, attrs = '', off]) => `<button class="btn sm secondary" ${off ? UI.off(off) : `data-act="${act}" ${attrs}`}>${ic ? UI.icon(ic) : ''}${l}</button>`).join('')}<span class="dl-sp"></span>${o.all && o.all > n ? `<button class="btn sm ghost" data-act="${o.allAct}">Select all ${o.all} matching</button>` : ''}<button class="btn sm ghost" data-act="${clearAct}">Clear</button></div>`;
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
