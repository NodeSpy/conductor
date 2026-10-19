/* The filter bar: one component for every list a person filters (Activity, Bots, Workflows, Secrets …).
 * GitHub's pattern. One search field holds the filters that are on, each a pill ([status: failed ✕], click it to edit,
 * ✕ or Backspace to remove), then free text; a qualifier you type (status:failed) applies when you pick a suggestion
 * or press Enter, never half-typed. Under it sit plain text buttons with a caret, one per filter, each opening a
 * searchable select menu: typeahead on top,
 * checkmarks, several at once where that makes sense, what's chosen and what you picked recently first, the keyboard
 * throughout, and never a giant list (the first matches, then "type to narrow"). A folder is a path (work ▸ platform),
 * not a tree. The field and the menus are one state: picking in a menu writes the qualifier into the field, typing a
 * qualifier sets the menu, and the field autocompletes qualifiers and their values. On a phone the buttons fold into
 * one Filter button that opens a sheet with the same menus.
 *
 *   FB.html(cfg) → markup for the bar; call it on every render with the page's own state object.
 *   cfg: { id, placeholder, label, state, onChange(state),
 *          fields: [{ k, label, multi, path, all, value(v) (button text), options() → [{ v, label, sub, n }],
 *                     more (listed under More filters until it's set), alias (a short key), group (its connector),
 *                     view (a view control such as Group by: a menu at the far right, never a qualifier or a filter) }] }
 * The menus are conductor's own filters only (status, bot, workflow, time…). What a connector's items carry (GitHub's
 * repo and PR) lives only in the search field: a qualifier with autocomplete, its keys grouped by connector under
 * "Filter by…", namespaced (github.repo:acme/web) with a short alias when unambiguous (repo:); once applied it's a pill
 * like any other.
 *   state: { q: 'free text', [k]: [values] (multi) or 'value' (single) }. A single field's '' (or its def) is unset.
 * Standalone: works on any page that loads js/icons.js before it (the options page and the brand book use it bare). */
(function () {
  const FB = (window.FB = { reg: {} });
  const esc = (s) => String(s ?? '').replace(/[&<>"]/g, (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;' }[c]));
  const P = window.ICONS || {};
  const ic = (n) => `<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.75" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">${P[n] || ''}</svg>`;
  const SHOWN = 40; // a menu never draws more than this; the rest are one keystroke away
  const phone = () => innerWidth <= 720;
  const vals = (f, st) => (f.multi ? [].concat(st[f.k] || []) : st[f.k] && st[f.k] !== (f.def || '') ? [st[f.k]] : []);
  const isSet = (f, st) => vals(f, st).length > 0;
  const opts = (f) => (typeof f.options === 'function' ? f.options() : f.options || []);
  const labelOf = (f, v) => { const o = opts(f).find((x) => x.v === v); return o ? o.label : v; };
  const pathText = (v) => String(v).split('/').join(' ▸ ');
  const qv = (v) => (/[\s,"]/.test(v) ? `"${String(v).replace(/"/g, '')}"` : v);

  /* ---------- recent picks (per filter key, shared across pages: the bot you picked on Activity is recent on Bots) ---------- */
  const RK = 'cd-filter-recent';
  const recent = (k) => { try { return (JSON.parse(localStorage.getItem(RK) || '{}')[k] || []); } catch (e) { return []; } };
  const remember = (k, v) => { try { const all = JSON.parse(localStorage.getItem(RK) || '{}'); all[k] = [v].concat((all[k] || []).filter((x) => x !== v)).slice(0, 5); localStorage.setItem(RK, JSON.stringify(all)); } catch (e) {} };

  /* ---------- the query text: qualifiers and free text, both ways ---------- */
  const qf = (cfg) => cfg.fields.filter((f) => !f.view); // the fields that filter (a view control doesn't)
  FB.serialize = (cfg) => { const st = cfg.state;
    const quals = qf(cfg).filter((f) => isSet(f, st)).map((f) => `${f.alias || f.k}:${vals(f, st).map(qv).join(',')}`);
    return quals.concat(st.q ? [st.q] : []).join(' '); };
  const TOK = /(\w[\w.-]*):("[^"]*"?|[^\s]*)|"[^"]*"?|\S+/g;
  const fieldFor = (cfg, key) => { key = String(key).toLowerCase(); return qf(cfg).find((x) => x.k.toLowerCase() === key || (x.alias && x.alias === key) || x.label.toLowerCase() === key); };
  /** Reads the field's text into state: known qualifiers set their filter (a value matches its id or its label), the
   * rest stays free text. A qualifier being typed (no value yet) changes nothing. */
  /** Applies the qualifiers typed in the field (on Enter, or a picked suggestion) on top of the filters already on, and
   * returns the free text that's left. A qualifier still missing its value stays as typed. */
  FB.apply = (cfg, text) => { const st = cfg.state, rest = [];
    for (const m of String(text).matchAll(TOK)) { const f = m[1] && fieldFor(cfg, m[1]), raw = f ? m[2].replace(/^"|"$/g, '') : '';
      if (!f || !raw) { rest.push(m[0]); continue; }
      const vs = (f.multi ? raw.split(',') : [raw]).map((x) => x.trim()).filter(Boolean).map((x) => { const o = opts(f).find((y) => y.v.toLowerCase() === x.toLowerCase() || String(y.label).toLowerCase() === x.toLowerCase()); return o ? o.v : x; });
      st[f.k] = f.multi ? [...new Set([].concat(st[f.k] || []).concat(vs))] : vs[vs.length - 1]; vs.forEach((v) => remember(f.k, v)); }
    return rest.join(' '); };
  /** The free text the field holds now: words that aren't a qualifier being typed. */
  const freeText = (text) => [...String(text).matchAll(TOK)].filter((m) => !m[1]).map((m) => m[0]).join(' ');
  FB.parse = (cfg, text) => { const st = cfg.state, free = [], seen = {};
    for (const m of String(text).matchAll(TOK)) {
      const f = m[1] && fieldFor(cfg, m[1]);
      if (!f) { free.push(m[0]); continue; }
      const raw = m[2].replace(/^"|"$/g, ''); if (!raw) continue;
      const vs = (f.multi ? raw.split(',') : [raw]).map((x) => x.trim()).filter(Boolean).map((x) => { const o = opts(f).find((y) => y.v.toLowerCase() === x.toLowerCase() || String(y.label).toLowerCase() === x.toLowerCase()); return o ? o.v : x; });
      seen[f.k] = f.multi ? [...new Set((seen[f.k] || []).concat(vs))] : vs[vs.length - 1];
    }
    qf(cfg).forEach((f) => { st[f.k] = f.k in seen ? seen[f.k] : f.multi ? [] : f.def || ''; });
    st.q = free.join(' '); };

  /* ---------- markup ---------- */
  const menus = (cfg) => qf(cfg).filter((f) => !f.group); // a connector's filters are qualifiers, never menus
  const pillText = (f, st) => { const v = vals(f, st); return `${f.alias || f.k}: ${v.map((x) => (f.value ? f.value(x) : f.path ? pathText(x) : labelOf(f, x))).join(', ')}`; };
  /** The filters that are on, as pills in the field: click one to edit it, ✕ to remove it. */
  const pillsHTML = (cfg) => { const st = cfg.state, on = qf(cfg).filter((f) => isSet(f, st) && !(f.bare && f.def));
    return on.length ? `<span class="fb-pills">${on.map((f) => `<span class="fb-pill" data-k="${esc(f.k)}"><button type="button" class="fb-pv" data-fb-edit="${esc(f.k)}" aria-label="Edit the filter ${esc(pillText(f, st))}">${esc(pillText(f, st))}</button><button type="button" class="fb-px" data-fb-unset="${esc(f.k)}" aria-label="Remove the filter ${esc(pillText(f, st))}">${ic('x')}</button></span>`).join('')}</span>` : ''; };
  const btnText = (f, st) => { const v = vals(f, st);
    // a field with a default (a time range) always names what it shows; the rest name themselves until they're set
    if (f.view) return `<span class="fb-l">${esc(f.label)}:</span> <span class="fb-v">${esc(labelOf(f, st[f.k] || f.def))}</span>`;
    if (!v.length && f.bare && f.def) return `<span class="fb-l">${esc(labelOf(f, f.def))}</span>`;
    if (!v.length) return `<span class="fb-l">${esc(f.label)}</span>`;
    const first = f.value ? f.value(v[0]) : f.path ? pathText(v[0]) : labelOf(f, v[0]);
    return f.bare ? `<span class="fb-v">${esc(first)}</span>` : `<span class="fb-l">${esc(f.label)}:</span> <span class="fb-v">${esc(first)}${v.length > 1 ? ` +${v.length - 1}` : ''}</span>`; };
  FB.html = (cfg) => { FB.reg[cfg.id] = cfg; const st = cfg.state, n = qf(cfg).filter((f) => isSet(f, st) && !f.bare).length + (st.q ? 1 : 0), quals = qf(cfg).length > 0;
    const any = qf(cfg).some((f) => isSet(f, st)) || st.q, views = cfg.fields.filter((f) => f.view);
    return `<div class="fb" data-fb="${cfg.id}">
      <div class="fb-search${any ? ' has-q' : ''}">${ic('search')}${pillsHTML(cfg)}<input class="fb-in" type="search" value="${esc(st.q || '')}" placeholder="${esc(cfg.placeholder || 'Search')}" aria-label="${esc(cfg.label || 'Search and filter')}" autocomplete="off" spellcheck="false" role="combobox" aria-expanded="false" aria-autocomplete="list" aria-controls="fb-ac-${cfg.id}">${quals ? `<button type="button" class="fb-by" data-fb-by title="Every filter you can type, conductor’s and each connection’s">Filter by…</button>` : ''}${any ? `<button type="button" class="fb-x" data-fb-clear aria-label="Clear the search and every filter">${ic('x')}</button>` : ''}
        <ul class="fb-ac" id="fb-ac-${cfg.id}" role="listbox" hidden></ul></div>
      <div class="fb-bar" role="group" aria-label="Filters">${menus(cfg).filter((f) => !f.more || isSet(f, st)).map((f) => `<button type="button" class="fb-btn${isSet(f, st) ? ' on' : ''}" data-fb-open="${f.k}" aria-haspopup="listbox" aria-expanded="false">${btnText(f, st)}${ic('chevd')}</button>`).join('')}
        <button type="button" class="fb-phone${n ? ' on' : ''}" data-fb-sheet aria-haspopup="dialog">${ic('filter')}Filter${n ? ` <span class="fb-n">${n}</span>` : ''}</button>
        ${menus(cfg).some((f) => f.more && !isSet(f, st)) ? `<button type="button" class="fb-btn fb-more-btn" data-fb-more aria-haspopup="menu" aria-expanded="false"><span class="fb-l">More filters</span>${ic('chevd')}</button>` : ''}
        ${any ? `<button type="button" class="fb-reset" data-fb-clear>${ic('x')}Clear filters</button>` : ''}
        ${views.map((f) => `<button type="button" class="fb-btn fb-view" data-fb-open="${f.k}" aria-haspopup="listbox" aria-expanded="false">${btnText(f, st)}${ic('chevd')}</button>`).join('')}${typeof cfg.end === 'function' ? cfg.end() : cfg.end || ''}</div></div>`; };

  /* ---------- after a change: the field's text, the buttons, the page's list ---------- */
  const commit = (cfg, keepText) => { const root = document.querySelector(`.fb[data-fb="${cfg.id}"]`);
    if (root) { const input = root.querySelector('.fb-in'), text = input.value, pos = input.selectionStart, focused = document.activeElement === input;
      const fresh = document.createElement('div'); fresh.innerHTML = FB.html(cfg); const nb = fresh.firstElementChild;
      root.querySelector('.fb-bar').replaceWith(nb.querySelector('.fb-bar'));
      const op = root.querySelector('.fb-pills'), np = nb.querySelector('.fb-pills'); if (op) op.remove(); if (np) input.before(np);
      const x = root.querySelector('.fb-x'), nx = nb.querySelector('.fb-x'); if (x && !nx) x.remove(); else if (!x && nx) input.after(nx);
      if (keepText) { input.value = text; if (focused) input.setSelectionRange(pos, pos); } else input.value = cfg.state.q || '';  root.querySelector('.fb-search').classList.toggle('has-q', !!FB.serialize(cfg)); }
    if (cfg.onChange) cfg.onChange(cfg.state); };
  FB.clear = (cfg) => { qf(cfg).forEach((f) => { cfg.state[f.k] = f.multi ? [] : f.def || ''; }); cfg.state.q = ''; commit(cfg); };

  /* ---------- the searchable select menu ---------- */
  let open = null; // { cfg, f, el, opener, active, sheet }
  const close = (refocus = true) => { if (!open) return; const o = open; open = null; o.el.remove(); if (o.scrim) o.scrim.remove(); if (o.opener) { o.opener.setAttribute('aria-expanded', 'false'); if (refocus && document.contains(o.opener)) o.opener.focus({ preventScroll: true }); } };
  FB.close = close;
  /** Selected first, then recent, then the rest (by count where there is one, else by name); the first SHOWN of what
   * matches the typeahead, the rest counted. */
  const ranked = (f, st, q) => { const sel = vals(f, st), rec = recent(f.k), all = opts(f);
    const hay = (o) => `${o.label} ${o.v} ${f.path ? pathText(o.v) : ''} ${o.sub || ''}`.toLowerCase();
    const words = q.toLowerCase().split(/\s+/).filter(Boolean), hit = all.filter((o) => words.every((w) => hay(o).includes(w)));
    const grp = (o) => (sel.includes(o.v) ? 0 : rec.includes(o.v) ? 1 : 2);
    hit.sort((a, b) => grp(a) - grp(b) || (grp(a) === 1 ? rec.indexOf(a.v) - rec.indexOf(b.v) : 0) || (f.keepOrder ? all.indexOf(a) - all.indexOf(b) : (b.n || 0) - (a.n || 0) || String(a.label).localeCompare(String(b.label))));
    // a typed value that isn't an option is still a filter you can set (a repo nobody's run yet)
    return { list: hit.slice(0, SHOWN), more: Math.max(0, hit.length - SHOWN), total: all.length, grp, sel }; };
  const itemsHTML = (o) => { const { cfg, f } = o, st = cfg.state, q = o.el.querySelector('.fb-find').value.trim(), r = ranked(f, st, q);
    let last = -1, out = '';
    const heads = ['Selected', 'Recent', f.all || 'All'];
    if (!f.multi && !q && f.allLabel) out += `<li role="option" class="fb-opt${!isSet(f, st) ? ' sel' : ''}" aria-selected="${!isSet(f, st)}" data-v="">${ic('check')}<span class="fb-ol"><b>${esc(f.allLabel)}</b></span></li>`;
    r.list.forEach((x) => { const g = r.grp(x); if (g !== last && (g < 2 || last !== -1) && !q) out += `<li class="fb-gh" role="presentation">${heads[g]}</li>`; last = g;
      const on = r.sel.includes(x.v);
      out += `<li role="option" class="fb-opt${on ? ' sel' : ''}" aria-selected="${on}" data-v="${esc(x.v)}">${ic('check')}<span class="fb-ol"><b>${esc(f.path ? pathText(x.label || x.v) : x.label)}</b>${x.sub ? `<small>${esc(x.sub)}</small>` : ''}</span>${x.n != null ? `<span class="fb-on">${x.n}</span>` : ''}</li>`; });
    if (!r.list.length) out += `<li class="fb-none" role="presentation">Nothing matches “${esc(q)}”.${f.free !== false && q ? ` <button type="button" class="link" data-fb-use="${esc(q)}">Filter by “${esc(q)}” anyway</button>` : ''}</li>`;
    if (r.more) out += `<li class="fb-more" role="presentation">${r.more} more. Type to narrow the ${r.total}.</li>`;
    return out; };
  const paint = (o) => { const ul = o.el.querySelector('.fb-list'); ul.innerHTML = itemsHTML(o); const xs = [...ul.querySelectorAll('.fb-opt')]; o.active = Math.min(o.active || 0, xs.length - 1); mark(o); };
  const mark = (o) => { const xs = [...o.el.querySelectorAll('.fb-opt')]; xs.forEach((x, i) => { x.classList.toggle('act', i === o.active); x.id = `fb-o-${i}`; }); const a = xs[o.active];
    o.el.querySelector('.fb-find').setAttribute('aria-activedescendant', a ? a.id : ''); if (a) a.scrollIntoView({ block: 'nearest' }); };
  const pick = (o, v) => { const { cfg, f } = o, st = cfg.state;
    if (f.multi) { const cur = vals(f, st); st[f.k] = cur.includes(v) ? cur.filter((x) => x !== v) : cur.concat([v]); if (v && !cur.includes(v)) remember(f.k, v); commit(cfg); paint(o); }
    else { st[f.k] = v; if (v && !f.view) remember(f.k, v); commit(cfg); if (o.sheet) showSheet(cfg, o); else close(); } };
  const menuBody = (cfg, f) => `<div class="fb-mh"><b>${esc(f.title || 'Filter by ' + f.label.toLowerCase())}</b>${f.multi && isSet(f, cfg.state) ? `<button type="button" class="link" data-fb-none>Clear</button>` : ''}</div>
    <div class="fb-fs">${ic('search')}<input class="fb-find" type="text" placeholder="${esc(f.find || 'Filter ' + (f.plural || f.label.toLowerCase() + 's'))}" aria-label="${esc(f.find || 'Filter ' + (f.plural || f.label.toLowerCase() + 's'))}" autocomplete="off" spellcheck="false" role="combobox" aria-expanded="true" aria-controls="fb-list"></div>
    <ul class="fb-list" id="fb-list" role="listbox"${f.multi ? ' aria-multiselectable="true"' : ''} aria-label="${esc(f.label)}"></ul>`;
  // delegated: in the phone sheet the menu (and its search field) is drawn later, inside the same element
  const wire = (o) => { const findEl = () => o.el.querySelector('.fb-find');
    o.el.addEventListener('input', (e) => { if (e.target.matches('.fb-find')) { o.active = 0; paint(o); } });
    o.el.addEventListener('keydown', (e) => { const xs = [...o.el.querySelectorAll('.fb-opt')];
      if (e.key === 'ArrowDown' || e.key === 'ArrowUp') { e.preventDefault(); o.active = (o.active + (e.key === 'ArrowDown' ? 1 : xs.length - 1)) % Math.max(1, xs.length); mark(o); }
      else if (e.key === 'Home' && !e.target.matches('.fb-find')) { e.preventDefault(); o.active = 0; mark(o); }
      else if (e.key === 'Enter' && o.f) { e.preventDefault(); const a = xs[o.active]; if (a) pick(o, a.dataset.v); }
      else if (e.key === 'Escape') { e.preventDefault(); e.stopPropagation(); if (o.sheet && o.f) showSheet(o.cfg, o); else close(); }
      else if (e.key === 'Tab' && !o.sheet) close(false); });
    o.el.addEventListener('click', (e) => { const li = e.target.closest('.fb-opt'); if (li) { o.active = [...o.el.querySelectorAll('.fb-opt')].indexOf(li); pick(o, li.dataset.v); if (findEl()) findEl().focus({ preventScroll: true }); return; }
      if (e.target.closest('[data-fb-none]')) { o.cfg.state[o.f.k] = []; commit(o.cfg); o.el.querySelector('[data-fb-none]').remove(); paint(o); }
      const use = e.target.closest('[data-fb-use]'); if (use) pick(o, use.dataset.fbUse); }); };
  FB.openMenu = (cfg, k, opener) => { close(false); const f = cfg.fields.find((x) => x.k === k); if (!f) return;
    if (phone()) { FB.openSheet(cfg, opener, k); return; }
    const el = document.createElement('div'); el.className = 'fb-menu'; el.setAttribute('role', 'dialog'); el.setAttribute('aria-label', f.label); el.innerHTML = menuBody(cfg, f); document.body.appendChild(el);
    const b = opener.getBoundingClientRect(); el.style.top = Math.min(innerHeight - 120, b.bottom + 6) + 'px'; el.style.left = Math.max(8, Math.min(innerWidth - el.offsetWidth - 8, b.left)) + 'px';
    open = { cfg, f, el, opener, active: 0 }; opener.setAttribute('aria-expanded', 'true'); wire(open); paint(open); el.querySelector('.fb-find').focus({ preventScroll: true }); };

  /** More filters ▾: the published filters that aren't menus of their own, grouped by connector; one opens its menu. */
  FB.openMore = (cfg, opener) => { close(false); const st = cfg.state, rest = menus(cfg).filter((f) => f.more && !isSet(f, st));
    const el = document.createElement('div'); el.className = 'fb-menu fb-more'; el.setAttribute('role', 'menu'); el.setAttribute('aria-label', 'More filters');
    let g = null; el.innerHTML = `<div class="fb-mh"><b>More filters</b></div><ul class="fb-list">${rest.map((f) => { const head = f.group !== g ? `<li class="fb-gh" role="presentation">${esc(f.group || 'conductor')}</li>` : ''; g = f.group; return `${head}<li><button type="button" role="menuitem" class="fb-opt fb-mi" data-k="${esc(f.k)}"><span></span><span class="fb-ol"><b>${esc(f.label)}</b><small>${esc(f.alias || f.k)}:</small></span>${ic('chev')}</button></li>`; }).join('')}</ul>`;
    document.body.appendChild(el); const b = opener.getBoundingClientRect(); el.style.top = Math.min(innerHeight - 120, b.bottom + 6) + 'px'; el.style.left = Math.max(8, Math.min(innerWidth - el.offsetWidth - 8, b.left)) + 'px';
    open = { cfg, el, opener, active: 0 }; opener.setAttribute('aria-expanded', 'true');
    const items = () => [...el.querySelectorAll('.fb-mi')];
    el.addEventListener('click', (e) => { const mi = e.target.closest('.fb-mi'); if (mi) { const k = mi.dataset.k; close(false); FB.openMenu(cfg, k, opener); } });
    el.addEventListener('keydown', (e) => { const xs = items(), i = xs.indexOf(document.activeElement); if (e.key === 'ArrowDown' || e.key === 'ArrowUp') { e.preventDefault(); xs[(i + (e.key === 'ArrowDown' ? 1 : xs.length - 1)) % xs.length].focus(); } else if (e.key === 'Escape') { e.preventDefault(); close(); } });
    if (items()[0]) items()[0].focus({ preventScroll: true }); };

  /* ---------- the phone: one Filter button, one sheet, each filter's menu inside it ---------- */
  const showSheet = (cfg, o, k) => { const st = cfg.state, f = k && cfg.fields.find((x) => x.k === k); o.f = f; o.active = 0;
    o.el.innerHTML = `<div class="fb-handle"></div>${f ? `<div class="fb-back"><button type="button" class="link" data-fb-back>${ic('chev')}All filters</button><button type="button" class="btn sm" data-fb-done>Done</button></div>${menuBody(cfg, f)}`
      : `<div class="fb-mh"><b>Filter</b><button type="button" class="btn sm" data-fb-done>Done</button></div><ul class="fb-rows">${cfg.fields.filter((x) => !x.group).map((x) => `<li><button type="button" data-fb-row="${x.k}"><span>${esc(x.label)}</span><span class="fb-rv${isSet(x, st) && !x.view ? ' on' : ''}">${x.view ? esc(labelOf(x, st[x.k] || x.def)) : isSet(x, st) ? esc(vals(x, st).map((v) => (x.path ? pathText(v) : labelOf(x, v))).join(', ')) : esc(x.allLabel || 'Any')}</span>${ic('chev')}</button></li>`).join('')}</ul>
        ${qf(cfg).some((x) => isSet(x, st)) ? `<button type="button" class="btn sm secondary fb-sheet-clear" data-fb-sheet-clear>Clear filters</button>` : ''}`}`;
    if (f) { paint(o); o.el.querySelector('.fb-find').focus({ preventScroll: true }); } else { const first = o.el.querySelector('[data-fb-row]'); if (first) first.focus({ preventScroll: true }); } };
  FB.openSheet = (cfg, opener, k) => { close(false);
    const scrim = document.createElement('div'); scrim.className = 'fb-scrim'; document.body.appendChild(scrim); scrim.addEventListener('click', () => close());
    const el = document.createElement('div'); el.className = 'fb-menu fb-sheet'; el.setAttribute('role', 'dialog'); el.setAttribute('aria-modal', 'true'); el.setAttribute('aria-label', 'Filter'); document.body.appendChild(el);
    open = { cfg, el, scrim, opener, sheet: true, active: 0 }; opener.setAttribute('aria-expanded', 'true'); wire(open);
    el.addEventListener('click', (e) => { const row = e.target.closest('[data-fb-row]'); if (row) showSheet(cfg, open, row.dataset.fbRow);
      if (e.target.closest('[data-fb-back]')) showSheet(cfg, open); if (e.target.closest('[data-fb-done]')) close();
      if (e.target.closest('[data-fb-sheet-clear]')) { FB.clear(cfg); showSheet(cfg, open); } });
    showSheet(cfg, open, k); };

  /* ---------- the search field: typing qualifiers, autocompleting them ---------- */
  const tokenAt = (input) => { const t = input.value, p = input.selectionStart; const s = t.lastIndexOf(' ', p - 1) + 1; let e = t.indexOf(' ', p); if (e < 0) e = t.length; return { s, e, w: t.slice(s, p) }; };
  const suggest = (cfg, input) => { const { w } = tokenAt(input), ul = input.parentNode.querySelector('.fb-ac'); let out = [];
    const m = w.match(/^(\w[\w.-]*):(.*)$/), keyOf = (f) => f.alias || f.k;
    const keyItem = (f) => ({ ins: `${keyOf(f)}:`, group: f.group || '', html: `<span class="fb-ak">${esc(keyOf(f))}:</span><small>${esc(f.hint || f.label)}${f.alias && f.alias !== f.k ? ` · ${esc(f.k)}` : ''}</small>` });
    if (m) { const f = fieldFor(cfg, m[1]); if (f) { const parts = m[2].replace(/^"/, '').split(','), cur = parts.pop().toLowerCase(), done = parts;
      out = opts(f).filter((o) => !done.includes(o.v) && (`${o.v} ${o.label}`).toLowerCase().includes(cur)).slice(0, 8).map((o) => ({ ins: `${keyOf(f)}:${(f.multi ? done.concat([o.v]) : [o.v]).map(qv).join(',')} `, html: `<span class="fb-ak">${esc(keyOf(f))}:</span><b>${esc(f.path ? pathText(o.v) : o.v)}</b>${o.label !== o.v && !f.path ? `<small>${esc(o.label)}</small>` : ''}` })); } }
    else if (w.length) { const lw = w.toLowerCase(); out = qf(cfg).filter((f) => f.k.toLowerCase().startsWith(lw) || (f.alias || '').startsWith(lw) || f.label.toLowerCase().startsWith(lw) || (f.group || '').toLowerCase().startsWith(lw)).map(keyItem); }
    else if (!input.value.trim() || input._by) out = qf(cfg).map(keyItem);
    input._by = false;
    input._ac = { list: out, active: 0 };
    // the qualifiers come in groups: conductor's own first, then each configured connector's under its name
    let lastG = null; ul.innerHTML = out.map((x, i) => { const head = x.group !== undefined && x.group !== lastG && (x.group || lastG !== null) ? `<li class="fb-ach" role="presentation">${esc(x.group || 'conductor')}</li>` : ''; lastG = x.group; return `${head}<li role="option" id="fb-ac-${cfg.id}-${i}" class="${i === 0 ? 'act' : ''}" data-i="${i}">${x.html}</li>`; }).join('');
    ul.hidden = !out.length; input.setAttribute('aria-expanded', String(!!out.length)); input.setAttribute('aria-activedescendant', out.length ? `fb-ac-${cfg.id}-0` : ''); };
  const accept = (cfg, input, i) => { const ac = input._ac; if (!ac || !ac.list[i]) return false; const { s, e } = tokenAt(input), t = input.value;
    input.value = t.slice(0, s) + ac.list[i].ins + t.slice(e).replace(/^ /, ''); const p = s + ac.list[i].ins.length; input.setSelectionRange(p, p);
    if (/:\S/.test(ac.list[i].ins)) { applyTyped(cfg, input); const q = input.value.length; input.setSelectionRange(q, q); }
    suggest(cfg, input); return true; };
  const hideAc = (input) => { const ul = input.parentNode.querySelector('.fb-ac'); if (ul) ul.hidden = true; input.setAttribute('aria-expanded', 'false'); input._ac = null; };
  const cfgOf = (el) => { const r = el.closest('.fb'); return r && FB.reg[r.dataset.fb]; };

  // typing searches the free text as you go; a qualifier waits for a pick or Enter
  document.addEventListener('input', (e) => { if (!e.target.matches('.fb-in')) return; const cfg = cfgOf(e.target); if (!cfg) return; const q = freeText(e.target.value); if (q !== (cfg.state.q || '')) { cfg.state.q = q; commit(cfg, true); } suggest(cfg, e.target); });
  const applyTyped = (cfg, input) => { const rest = FB.apply(cfg, input.value); cfg.state.q = freeText(rest); input.value = rest; commit(cfg, true); };
  document.addEventListener('focusin', (e) => { if (e.target.matches('.fb-in')) { const cfg = cfgOf(e.target); if (cfg) suggest(cfg, e.target); } });
  document.addEventListener('focusout', (e) => { if (e.target.matches('.fb-in')) setTimeout(() => { if (document.activeElement !== e.target) hideAc(e.target); }, 120); });
  document.addEventListener('keydown', (e) => { if (!e.target.matches('.fb-in')) return; const cfg = cfgOf(e.target), input = e.target, ac = input._ac;
    const items = [...input.parentNode.querySelectorAll('.fb-ac li[data-i]')];
    if (ac && ac.list.length && (e.key === 'ArrowDown' || e.key === 'ArrowUp')) { e.preventDefault(); ac.active = (ac.active + (e.key === 'ArrowDown' ? 1 : ac.list.length - 1)) % ac.list.length; items.forEach((x, i) => x.classList.toggle('act', i === ac.active)); input.setAttribute('aria-activedescendant', `fb-ac-${cfg.id}-${ac.active}`); }
    else if (ac && ac.list.length && (e.key === 'Tab' || e.key === 'Enter') && tokenAt(input).w) { e.preventDefault(); accept(cfg, input, ac.active); }
    else if (e.key === 'Enter') { e.preventDefault(); applyTyped(cfg, input); hideAc(input); }
    else if (e.key === 'Backspace' && input.selectionStart === 0 && input.selectionEnd === 0) { const pills = [...input.parentNode.querySelectorAll('.fb-pill')], last = pills[pills.length - 1]; if (!last) return; e.preventDefault();
      if (last.classList.contains('sel')) { const f = cfg.fields.find((x) => x.k === last.dataset.k); cfg.state[f.k] = f.multi ? [] : f.def || ''; commit(cfg, true); input.focus(); } else last.classList.add('sel'); }
    else if (e.key === 'ArrowLeft' && input.selectionStart === 0 && input.selectionEnd === 0) { const ps = input.parentNode.querySelectorAll('.fb-pv'); if (ps.length) { e.preventDefault(); ps[ps.length - 1].focus(); } }
    else if (e.key === 'Escape') { if (ac && ac.list.length) { e.preventDefault(); e.stopPropagation(); hideAc(input); } else if (input.value) { e.preventDefault(); FB.clear(cfg); } } });
  document.addEventListener('mousedown', (e) => { const li = e.target.closest('.fb-ac li[data-i]'); if (!li) return; e.preventDefault(); const input = li.closest('.fb-search').querySelector('.fb-in'); accept(cfgOf(input), input, +li.dataset.i); });
  document.addEventListener('click', (e) => {
    const b = e.target.closest('[data-fb-open]'); if (b) { e.preventDefault(); e.stopPropagation(); const cfg = cfgOf(b); if (open && open.opener === b) close(); else FB.openMenu(cfg, b.dataset.fbOpen, b); return; }
    const mo = e.target.closest('[data-fb-more]'); if (mo) { e.preventDefault(); e.stopPropagation(); if (open && open.opener === mo) close(); else FB.openMore(cfgOf(mo), mo); return; }
    const sh = e.target.closest('[data-fb-sheet]'); if (sh) { e.preventDefault(); e.stopPropagation(); FB.openSheet(cfgOf(sh), sh); return; }
    const by = e.target.closest('[data-fb-by]'); if (by) { e.preventDefault(); e.stopPropagation(); const input = by.parentNode.querySelector('.fb-in'), cfg = cfgOf(by); if (input.value && !/\s$/.test(input.value)) input.value += ' '; input.focus(); const p = input.value.length; input.setSelectionRange(p, p); input._by = true; suggest(cfg, input); return; }
    const ed = e.target.closest('[data-fb-edit]'); if (ed) { e.preventDefault(); e.stopPropagation(); const cfg = cfgOf(ed), f = cfg.fields.find((x) => x.k === ed.dataset.fbEdit), input = ed.closest('.fb-search').querySelector('.fb-in');
      // editing a pill: it comes back into the field as its qualifier, with its values to change; Enter or a pick puts it back
      const tok = `${f.alias || f.k}:${vals(f, cfg.state).map(qv).join(',')}`; cfg.state[f.k] = f.multi ? [] : f.def || ''; commit(cfg, true);
      const inp = document.querySelector(`.fb[data-fb="${cfg.id}"] .fb-in`); inp.value = tok + (inp.value ? ' ' + inp.value : ''); inp.focus(); inp.setSelectionRange(tok.length, tok.length); suggest(cfg, inp); void input; return; }
    const un = e.target.closest('[data-fb-unset]'); if (un) { e.preventDefault(); e.stopPropagation(); const cfg = cfgOf(un), f = cfg.fields.find((x) => x.k === un.dataset.fbUnset); cfg.state[f.k] = f.multi ? [] : f.def || ''; commit(cfg); return; }
    const c = e.target.closest('[data-fb-clear]'); if (c) { e.preventDefault(); e.stopPropagation(); FB.clear(cfgOf(c)); const i = document.querySelector(`.fb[data-fb="${cfgOf(c) ? cfgOf(c).id : ''}"] .fb-in`); if (i && c.classList.contains('fb-x')) i.focus(); return; }
    if (open && !open.sheet && !open.el.contains(e.target)) close(false);
  }, true);
  // a pill has the keyboard too: arrows move between pills and back to the text, Delete or Backspace removes it
  document.addEventListener('keydown', (e) => { if (!e.target.matches || !e.target.matches('.fb-pv')) return; const pv = [...e.target.closest('.fb-search').querySelectorAll('.fb-pv')], i = pv.indexOf(e.target), input = e.target.closest('.fb-search').querySelector('.fb-in');
    if (e.key === 'ArrowLeft' && i > 0) { e.preventDefault(); pv[i - 1].focus(); } else if (e.key === 'ArrowRight') { e.preventDefault(); if (pv[i + 1]) pv[i + 1].focus(); else { input.focus(); input.setSelectionRange(0, 0); } }
    else if (e.key === 'Delete' || e.key === 'Backspace') { e.preventDefault(); const cfg = cfgOf(e.target), f = cfg.fields.find((x) => x.k === e.target.dataset.fbEdit); cfg.state[f.k] = f.multi ? [] : f.def || ''; commit(cfg, true); const n = document.querySelector(`.fb[data-fb="${cfg.id}"] .fb-in`); n.focus(); n.setSelectionRange(0, 0); } });
  document.addEventListener('keydown', (e) => { if (e.target.matches && e.target.matches('.fb-in') && e.key !== 'Backspace') e.target.parentNode.querySelectorAll('.fb-pill.sel').forEach((x) => x.classList.remove('sel')); });
  document.addEventListener('keydown', (e) => { if (e.key !== 'Escape' || !open || open.el.contains(e.target)) return; e.preventDefault(); e.stopPropagation(); close(); }, true);
  addEventListener('resize', () => { if (open && !open.sheet) close(false); });
  addEventListener('hashchange', () => close(false));
  if (window.UI) window.UI.filterBar = FB.html;
})();
