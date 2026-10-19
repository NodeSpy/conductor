/* The value-source field: one component for every configurable value.
 *
 * A field's value is Typed (plain, or write-only when sensitive), an Env var
 * (${env.NAME}), a Vault entry ({{ vault "<vault>" "<key>" }}, the one
 * reference syntax; op:// URIs and the named secrets: block are retired), or
 * a Setting (${settings.name}, never for a sensitive field). The field shows
 * the reference, never a value, with a resolve check the daemon performs
 * without returning the value (POST /api/v1/refs/resolve → ok | error). A
 * typed sensitive value is written to conductor's vault (house) behind a
 * step-up when the form is saved, and the field then holds its reference.
 *
 * Used by UI.fieldHTML for type 'value' (and 'secret', which is 'value' with
 * sensitive: true), so every form gets the same control. */
(function () {
  const { icon, esc } = UI;
  const VS = (UI.VS = {});
  const V = () => S.view();
  VS.SRC = { typed: 'Typed', env: 'Env var', vault: 'Vault', setting: 'Setting' };
  const ICON = { typed: 'edit', env: 'terminal', vault: 'lock', setting: 'config' };
  /* Daylight: the source is a quiet menu inside the field, not a segmented control in front of it. A typed sensitive
     value is a Secret (conductor stores it encrypted); a vault reference is a Vault item; typed plain values and
     settings sit under "Other sources". */
  const SRCW = (s, sensitive) => ({ typed: sensitive ? ['Secret', 'Type it once; conductor stores it encrypted in its vault. It never comes back to the browser.', 'key'] : ['Typed value', 'Written into the config as it is. Not for anything secret.', 'edit'], vault: ['Vault item', 'A conductor vault entry, or an item in 1Password or Bitwarden, read when needed.', 'lock'], env: ['Environment variable', 'Read from conductor’s environment when the value is used.', 'terminal'], setting: ['Setting', 'A shared value from Config → Settings. Plain config, so never for a secret.', 'config'] }[s]);
  const srcBtn = (s, sensitive) => { const [t, , ic] = SRCW(s, sensitive); return `${icon(ic)}<span>${t}</span>${icon('chevd', 'chev')}`; };
  const srcMenu = (sources, src, sensitive) => { const main = sources.filter((x) => (sensitive ? x !== 'setting' : x !== 'typed' && x !== 'setting')), other = sources.filter((x) => !main.includes(x));
    const opt = (x) => { const [t, d, ic] = SRCW(x, sensitive); return `<button type="button" role="menuitemradio" class="vs-opt${x === src ? ' on' : ''}" aria-pressed="${x === src}" aria-checked="${x === src}" data-act="vs-src" data-v="${x}">${icon(ic)}<b>${t}</b><span>${d}</span></button>`; };
    return `${main.map(opt).join('')}${other.length ? `<div class="vs-more">Other sources</div>${other.map(opt).join('')}` : ''}`; };

  VS.parse = (v) => {
    v = v == null ? '' : String(v); let m;
    if (!v) return { src: null, ref: '' };
    if ((m = v.match(/^\$\{env\.([A-Za-z_]\w*)\}$/))) return { src: 'env', name: m[1], ref: v, label: v };
    if ((m = v.match(/^\$\{settings\.([\w.-]+)\}$/))) return { src: 'setting', name: m[1], ref: v, label: v };
    if (/^op:\/\//.test(v)) return { src: 'vault', vault: 'op', path: v.slice(5), ref: v, label: v, retired: true };
    if ((m = v.match(/^\{\{\s*vault\s+"([^"]+)"\s+"([^"]+)"\s*\}\}$/))) return { src: 'vault', vault: m[1], path: m[2], ref: v, label: `${m[1]} · ${m[2]}` };
    return { src: 'typed', ref: v, label: v };
  };
  const envOf = (n) => (V().env || D.env || []).find((e) => e.name === n);
  const scopeLabel = (s) => (s === 'daemon' ? 'conductor.env' : s.replace(':', ' '));
  /** What the daemon would answer: ok, or an error reason. Never the value. */
  VS.resolve = (v) => {
    const p = VS.parse(v);
    if (!p.src) return { state: 'none' };
    if (p.src === 'typed') return { state: 'typed' };
    if (p.src === 'env') { const e = envOf(p.name); return e ? { state: 'ok', why: `set in ${scopeLabel(e.scope)}` } : { state: 'fail', why: `${p.name} isn't set in conductor.env or any scope this reaches` }; }
    if (p.src === 'setting') return V().settings.some((x) => x.name === p.name) ? { state: 'ok', why: 'setting exists' } : { state: 'fail', why: `no setting "${p.name}"` };
    if (p.retired) return { state: 'fail', why: 'op:// references were replaced by {{ vault "op" "…" }}; an unmigrated one fails loudly' };
    if (p.vault === 'op' && V().secrets.some((x) => x.ref === `op/${p.path}`)) return { state: 'ok', why: `1Password · ${p.path}` };
    if (p.vault === 'op') {
      const [path, q] = p.path.split('?'); const [vault, item, ...f] = path.split('/'); const field = f.join('/');
      const it = D.opItems.find((x) => x.vault === vault && x.title === item);
      if (!it) return { state: 'fail', why: `1Password: no item ${vault}/${item}` };
      const fd = it.fields.find(([l]) => l === field); if (!fd) return { state: 'fail', why: `1Password: ${item} has no field "${field}"` };
      if (/attribute=otp/.test(q || '') && fd[1] !== 'otp') return { state: 'fail', why: `"${field}" isn't a one-time-password field` };
      return { state: 'ok', why: `1Password · ${vault}/${item}` };
    }
    const vt = V().vaults.find((x) => x.name === p.vault);
    if (!vt) return { state: 'fail', why: `no vault "${p.vault}" (a reference to an unknown vault fails conductor validate)` };
    if (vt.status === 'fail') return { state: 'fail', why: `vault ${vt.name}: ${vt.detail}` };
    if (['conductor', 'file'].includes(vt.type) && !V().secrets.some((x) => x.ref === `${p.vault}/${p.path}`)) return { state: 'fail', why: `${vt.name} has no entry "${p.path}"` };
    return { state: 'ok', why: `vault ${vt.name}` };
  };
  /** A sensitive field may not come from plain config. */
  VS.blocked = (sensitive, v) => (sensitive && VS.parse(v).src === 'setting' ? 'Blocked: a setting is plain config, written into config.yaml and shown in every diff and backup. A sensitive value has to come from a secret, a vault reference or an env var.' : '');
  VS.warn = (sensitive, v) => { const p = VS.parse(v); if (!sensitive || p.src !== 'env') return ''; const e = envOf(p.name); return e && e.plain && e.scope === 'daemon' ? `${p.name} is plain text in conductor.env: anything that can read that file sees it. Store it as a secret and reference that instead.` : ''; };

  /* ---------- markup ---------- */
  /* ---------- the lookup: a reference is picked from what exists, never typed ----------
     One list for the field's source: vault items across the vaults (1Password items by vault/item, each field its own
     choice; the conductor vault, pass and HashiCorp by path), environment variables and settings, by name. Each says
     whether it exists and when it last changed, never its value; one that can't be found or reached is flagged. */
  const vaultWord = (v) => { const vt = V().vaults.find((x) => x.name === v); return vt && vt.type === 'onepassword' ? '1Password' : v === 'house' ? 'conductor vault' : vt && vt.type === 'pass' ? 'pass' : vt && /hashi|vault/.test(vt.type || '') ? 'HashiCorp' : v; };
  const unreachable = (v) => { const vt = V().vaults.find((x) => x.name === v); return vt && vt.status === 'fail' ? `can’t be reached: ${vt.name} ${vt.detail || 'is unreachable'}` : ''; };
  const changed = (at) => (at ? `exists · changed ${UI.agoText(at)}` : 'exists');
  VS.options = (src) => {
    if (src === 'env') return (V().env || D.env || []).map((e) => ({ ref: `\${env.${e.name}}`, icon: 'terminal', title: e.name, sub: `environment variable · ${scopeLabel(e.scope)}`, meta: changed(e.set), flag: '' }));
    if (src === 'setting') return (V().settings || []).map((x) => ({ ref: `\${settings.${x.name}}`, icon: 'config', title: x.name, sub: `setting${x.used ? ` · used in ${x.used} place${x.used === 1 ? '' : 's'}` : ''}`, meta: changed(x.set), flag: '' }));
    const out = [], seen = new Set(), add = (o) => { if (!seen.has(o.ref)) { seen.add(o.ref); out.push(o); } };
    (D.opItems || []).forEach((it) => it.fields.forEach(([f, t]) => add({ ref: `{{ vault "op" "${it.vault}/${it.title}/${f}${t === 'otp' ? '?attribute=otp' : ''}" }}`, icon: 'lock', vault: 'op', title: it.title, field: f, sub: `1Password · ${it.vault}/${it.title}`, meta: changed(it.changed), flag: unreachable('op'), otp: t === 'otp' })));
    (V().secrets || []).forEach((x) => { const path = String(x.key || x.ref.split('/').slice(1).join('/')); add({ ref: S.vaultExpr(x.ref), icon: 'lock', vault: x.vault, title: path, sub: vaultWord(x.vault), meta: changed(x.set), flag: unreachable(x.vault) }); });
    return out; };
  /** What's chosen, as the lookup knows it (or, for a reference it can't find, a flagged stand-in). */
  VS.option = (src, ref) => VS.options(src).find((o) => o.ref === ref) || (() => { const p = VS.parse(ref); const r = VS.resolve(ref);
    return { ref, icon: src === 'env' ? 'terminal' : src === 'setting' ? 'config' : 'lock', title: p.src === 'vault' ? p.path.split('/').slice(-2, -1)[0] || p.path : p.name || ref, field: p.src === 'vault' && p.path.includes('/') ? p.path.split('/').pop().replace(/\?.*$/, '') : '', sub: p.src === 'vault' ? `${vaultWord(p.vault)} · ${p.path.split('/').slice(0, -1).join('/')}` : '', meta: '', flag: r.state === 'fail' ? r.why : '' }; })();
  // every word must match somewhere; a match in the item's name or field ranks above one only in where it lives
  VS.find = (src, q, m = {}) => { const ts = String(q || '').toLowerCase().split(/\s+/).filter(Boolean);
    const score = (o) => { const own = `${o.title} ${o.field || ''}`.toLowerCase(), where = `${o.vault || ''} ${o.sub}`.toLowerCase(); let n = 0; for (const t of ts) { if (own.includes(t)) n += 2; else if (where.includes(t)) n += 1; else return -1; } return n + (m.otp && o.otp ? 1 : 0); };
    return VS.options(src).map((o, i) => [o, score(o), i]).filter(([, n]) => n >= 0).sort((a, b) => b[1] - a[1] || a[2] - b[2]).map(([o]) => o); };
  const PH = { vault: 'Search vault items: a name, a path or a field', env: 'Search environment variables', setting: 'Search settings' };
  const NEW = { vault: ['secret', 'New vault entry…'], env: ['env', 'Add an environment variable…'] };
  const optHTML = (o, id, i) => `<li role="option" id="${id}-o${i}" class="vs-o${o.flag ? ' bad' : ''}" data-act="vs-choose" data-ref="${esc(o.ref)}" data-vb="${esc(o.ref)}" aria-selected="false">${icon(o.icon)}<span class="vs-ot"><b>${esc(o.title)}${o.field ? `<span class="vs-of"> · ${esc(o.field)}</span>` : ''}</b><span>${esc(o.sub)}</span></span><span class="vs-om${o.flag ? ' flag' : ''}">${o.flag ? UI.mark('fail') : ''}${esc(o.flag || o.meta)}</span></li>`;
  /** The list under the search (and, larger, in Browse…): the matches, then a way to add one that isn't there. */
  VS.listHTML = (src, q, m, id, cap = 8) => { const xs = VS.find(src, q, m), nw = NEW[src], ql = String(q || '').toLowerCase().trim();
    const down = src === 'vault' && ql ? V().vaults.filter((v) => v.status === 'fail' && (v.name.toLowerCase().includes(ql) || vaultWord(v.name).toLowerCase().includes(ql))) : [];
    return `${down.map((v) => `<li class="vs-none-l vs-down" role="presentation">${UI.mark('fail')} ${esc(v.name)} can’t be reached: ${esc(v.detail || 'it is unreachable')}. Its items can’t be listed until it unlocks.</li>`).join('')}${xs.slice(0, cap).map((o, i) => optHTML(o, id, i)).join('') || (down.length ? '' : `<li class="vs-none-l" role="presentation">Nothing matches “${esc(q)}”.</li>`)}${xs.length > cap ? `<li class="vs-more-l" role="presentation">${xs.length - cap} more: keep typing${src === 'vault' ? ', or Browse…' : ''}</li>` : ''}${nw ? `<li role="option" id="${id}-onew" class="vs-o vs-onew" data-act="vs-new" data-kind="${nw[0]}" aria-selected="false">${icon('plus')}<span class="vs-ot"><b>${nw[1]}</b></span></li>` : ''}`; };
  /** A chosen reference: a chip (its icon, name and field) with ✕; the search comes back when it's removed. */
  const chipHTML = (src, val, id, label) => { const o = VS.option(src, val);
    return `<span class="vs-chosen${o.flag ? ' bad' : ''}" id="${id}-pick" tabindex="0" aria-label="${esc(label)}: ${esc(o.title)}${o.field ? ', ' + esc(o.field) : ''}">${icon(o.icon)}<b>${esc(o.title)}</b>${o.field ? `<span class="vs-of">${esc(o.field)}</span>` : ''}${o.sub ? `<span class="vs-cs">${esc(o.sub)}</span>` : ''}<button type="button" class="vs-x" data-act="vs-clear" aria-label="Remove ${esc(o.title)}">${icon('x')}</button></span>`; };
  const lookHTML = (fd, src, val, id) => { const label = fd.label || fd.k, browse = src === 'vault' ? `<button type="button" class="btn sm vs-browse" data-act="vs-vault">${icon('search')}Browse…</button>` : '';
    return `<div class="vs-look">${val ? chipHTML(src, val, id, label) : `<input class="input vs-q" id="${id}-pick" role="combobox" aria-expanded="false" aria-autocomplete="list" aria-controls="${id}-list" autocomplete="off" spellcheck="false" placeholder="${PH[src]}" data-on-input="vs-q" aria-label="${esc(label)}: ${src === 'vault' ? 'vault item' : src === 'env' ? 'environment variable' : 'setting'}">`}<ul class="vs-list" id="${id}-list" role="listbox" hidden></ul>${browse}</div>`; };
  function body(fd, src, val, id) {
    const p = VS.parse(val), cur = p.src === src ? p : {};
    if (src === 'typed') return fd.sensitive || fd.vaultOnly
      ? `<input class="input mono vs-in" id="${id}-in" type="${fd.sensitive ? 'password' : 'text'}" autocomplete="${fd.sensitive ? 'new-password' : 'off'}" value="" placeholder="${p.src === 'typed' ? 'set · type to replace' : esc(fd.typedPh || 'stored in the vault when you save; config keeps the reference')}" data-on-input="vs-typed" aria-label="${esc(fd.label || fd.k)} (typed, stored in the vault)">`
      : `<input class="input mono vs-in" id="${id}-in" value="${esc(cur.ref || '')}" placeholder="${esc(fd.ph || '')}" data-on-input="vs-typed" aria-label="${esc(fd.label || fd.k)} (typed)">`;
    return `${lookHTML(fd, src, cur.ref || '', id)}${src === 'vault' ? `<div class="vs-ref mono tiny"${cur.ref ? '' : ' hidden'}>${cur.ref ? esc(cur.ref) : ''}</div><div class="vs-same"></div>` : ''}`;
  }
  VS.html = (fd, val, id) => {
    const p = VS.parse(val);
    const sources = fd.sources || ['typed', 'env', 'vault', 'setting'];
    const src = p.src && sources.includes(p.src) ? p.src : fd.defaultSrc || (fd.sensitive ? 'vault' : 'typed');
    const meta = esc(JSON.stringify({ k: fd.k, label: fd.label || fd.k, sensitive: !!fd.sensitive, otp: !!fd.otp, ph: fd.ph || '', typedPh: fd.typedPh || '', sources, vaultOnly: !!fd.vaultOnly }));
    return `<div class="vs" data-vs="${esc(fd.k)}" data-src="${src}" data-meta="${meta}" data-id="${id}"${fd.sensitive ? ' data-sensitive="1"' : ''}>
      <input type="hidden" id="${id}" data-k="${esc(fd.k)}" value="${esc(p.src === src ? (src === 'typed' && fd.sensitive ? '' : val) : '')}"${p.src === 'typed' && fd.sensitive ? ' data-set="1"' : ''}>
      <div class="vs-row"><details class="vs-menu"><summary class="vs-srcbtn" aria-label="Where ${esc(fd.label || fd.k)} comes from: ${SRCW(src, !!fd.sensitive)[0]}">${srcBtn(src, !!fd.sensitive)}</summary><div class="vs-pop vs-seg" role="menu">${srcMenu(sources, src, !!fd.sensitive)}</div></details>
        <div class="vs-body">${body(fd, src, val, id)}</div></div>
      <span class="vs-check" data-volatile aria-live="polite"></span>
      <div class="vs-msg" data-volatile></div></div>`;
  };

  /* ---------- behaviour ---------- */
  const meta = (el) => JSON.parse(el.dataset.meta);
  const hid = (el) => el.querySelector('input[type=hidden]');
  let seq = 0;
  VS.refresh = (el, quiet) => {
    const m = meta(el), v = hid(el).value, chk = el.querySelector('.vs-check'), msg = el.querySelector('.vs-msg');
    const ref = el.querySelector('.vs-ref'); if (ref) { const p = VS.parse(v); ref.textContent = p.src === 'vault' ? p.ref : ''; ref.hidden = p.src !== 'vault'; } // the reference it writes, quietly, once there is one
    const blocked = VS.blocked(m.sensitive, v), warn = VS.warn(m.sensitive, v) || (VS.parse(v).retired ? 'op:// references are retired: pick the entry again to write {{ vault "op" "…" }}' : '');
    msg.innerHTML = blocked ? `<div class="vs-err">${icon('x')}${esc(blocked)}</div>` : warn ? `<div class="vs-warn">${icon('warn')}${esc(warn)} <a class="link" href="#/secrets">Secrets</a></div>` : '';
    el.classList.toggle('blocked', !!blocked);
    const pending = el.dataset.pending === '1';
    if (pending) { chk.innerHTML = UI.statusMark('idle', 'write-only · stored on save', { cls: 'vs-st wait', sm: true }); }
    else if (el.dataset.src === 'typed') chk.innerHTML = m.sensitive && hid(el).dataset.set ? UI.statusMark('ok', 'set', { cls: 'vs-st ok', sm: true }) : '';
    else if (!v) chk.innerHTML = '<span class="vs-st vs-none">Not set yet</span>'; // plain words: a status mark is for a run's state
    else {
      const my = ++seq; el.dataset.seq = my; chk.innerHTML = UI.statusMark('run', 'checking…', { cls: 'vs-st run', sm: true });
      setTimeout(() => { if (el.dataset.seq != my) return; const v2 = hid(el).value, r = VS.resolve(v2), o = VS.option(el.dataset.src, v2);
        // whether it exists and when it last changed, never the value; one that can't be found or reached is flagged
        chk.innerHTML = r.state === 'ok' ? UI.statusMark('success', esc(o.meta || 'exists'), { cls: 'vs-st ok', tip: r.why, sm: true }) : r.state === 'fail' ? UI.statusMark('fail', /reach|unreachable|locked/i.test(r.why) ? `can’t be reached: ${esc(r.why)}` : `can’t be found: ${esc(r.why)}`, { cls: 'vs-st fail', sm: true }) : ''; el.dataset.resolve = r.state; }, UI.fast ? 15 : 260);
    }
    // "use the same item" shortcuts: another field in this form came from a 1Password item
    const same = el.querySelector('.vs-same');
    if (same) {
      const form = el.closest('form, .vs-scope, .modal, .page') || document;
      const items = [...new Set([...form.querySelectorAll('.vs')].filter((x) => x !== el).map((x) => VS.parse(hid(x).value)).filter((p) => p.vault === 'op' && !p.retired).map((p) => p.path.split('?')[0].split('/').slice(0, 2).join('/')))];
      same.innerHTML = items.map((vi) => { const it = D.opItems.find((x) => `${x.vault}/${x.title}` === vi); if (!it) return ''; const used = new Set([...form.querySelectorAll('.vs input[type=hidden]')].map((x) => x.value)); const named = it.fields.filter(([l]) => [m.k, m.label].some((x) => { const w = String(x || '').toLowerCase().split(/[^a-z]+/)[0]; return w && l.toLowerCase().split(/[^a-z]+/).includes(w); })); // a field named like this one (expiry, number, name…) first
        return (named.length ? named : it.fields.filter(([, t]) => (m.otp ? t === 'otp' : m.sensitive ? t === 'concealed' : t === 'text' || t === 'email'))).map(([l, t]) => { const r = `{{ vault "op" "${vi}/${l}${t === 'otp' ? '?attribute=otp' : ''}" }}`; return r === v || used.has(r) ? '' : `<button type="button" class="vs-chip" data-act="vs-same" data-ref="${esc(r)}">${icon('link')}Use the same item: ${esc(it.title)} · ${esc(l)}</button>`; }).join(''); }).join('');
    }
    if (!quiet) hid(el).dispatchEvent(new Event('input', { bubbles: true }));
  };
  const setVal = (el, v) => { hid(el).value = v; delete hid(el).dataset.set; el.dataset.pending = '';
    if (el.dataset.src !== 'typed') { el.querySelector('.vs-body').innerHTML = body(meta(el), el.dataset.src, v, el.dataset.id); }
    VS.refresh(el); };
  VS.setVal = setVal;
  /* the lookup's behaviour: type to search, ↑/↓ and Enter or a click to pick, Esc to close; ✕ takes the choice away */
  const listOf = (inp) => inp.closest('.vs-look').querySelector('.vs-list');
  const openList = (inp) => { const el = inp.closest('.vs'), m = meta(el), ul = listOf(inp); ul.innerHTML = VS.listHTML(el.dataset.src, inp.value, m, el.dataset.id); ul.hidden = false; inp.setAttribute('aria-expanded', 'true'); inp.removeAttribute('aria-activedescendant'); };
  const closeList = (inp) => { const ul = listOf(inp); ul.hidden = true; inp.setAttribute('aria-expanded', 'false'); inp.removeAttribute('aria-activedescendant'); };
  UI.act('vs-q', (inp) => openList(inp));
  // the list opens on a click into the field, on typing or on ↓, never on focus alone (switching the source shouldn't cover what's under the field)
  document.addEventListener('click', (e) => { if (e.target.matches && e.target.matches('.vs-q')) openList(e.target); });
  document.addEventListener('focusout', (e) => { const inp = e.target; if (!inp.matches || !inp.matches('.vs-q')) return; setTimeout(() => { if (document.body.contains(inp) && !inp.closest('.vs-look').contains(document.activeElement)) closeList(inp); }, 120); });
  document.addEventListener('mousedown', (e) => { if (e.target.closest && e.target.closest('.vs-list [role=option]')) { e.preventDefault(); return; }
    document.querySelectorAll('.vs-list:not([hidden]):not(.big)').forEach((ul) => { if (!ul.closest('.vs-look').contains(e.target)) { const q = ul.closest('.vs-look').querySelector('.vs-q'); if (q) closeList(q); else ul.hidden = true; } }); }, true); // keep the search focused while picking
  document.addEventListener('keydown', (e) => { const inp = e.target; if (!inp.matches || !inp.matches('.vs-q')) return; const ul = listOf(inp), xs = [...ul.querySelectorAll('[role=option]')];
    if (e.key === 'Escape' && !ul.hidden) { e.preventDefault(); e.stopPropagation(); closeList(inp); return; }
    if (e.key === 'ArrowDown' || e.key === 'ArrowUp') { e.preventDefault(); if (ul.hidden) openList(inp); const ys = [...ul.querySelectorAll('[role=option]')]; if (!ys.length) return; let i = ys.findIndex((x) => x.getAttribute('aria-selected') === 'true'); i = e.key === 'ArrowDown' ? (i + 1) % ys.length : (i <= 0 ? ys.length : i) - 1;
      ys.forEach((x, j) => x.setAttribute('aria-selected', j === i)); inp.setAttribute('aria-activedescendant', ys[i].id); ys[i].scrollIntoView({ block: 'nearest' }); return; }
    if (e.key === 'Enter') { const on = xs.find((x) => x.getAttribute('aria-selected') === 'true') || (xs.length && inp.value ? xs[0] : null); if (on) { e.preventDefault(); e.stopPropagation(); on.click(); } } }, true);
  let browseFor = null; // the field Browse… was opened from
  UI.act('vs-choose', (li) => { const el = li.closest('.vs') || browseFor; if (!el) return; const scrim = li.closest('.vs') ? null : li.closest('.drawer-scrim'); if (scrim) scrim.remove(); setVal(el, li.dataset.ref); const c = el.querySelector('.vs-chosen'); if (c) c.focus(); });
  UI.act('vs-clear', (b) => { const el = b.closest('.vs'); setVal(el, ''); const q = el.querySelector('.vs-q'); if (q) q.focus(); });
  UI.act('vs-new', (li) => { const el = li.closest('.vs') || browseFor, m = meta(el), kind = li.dataset.kind, fn = UI.CREATE && UI.CREATE[kind]; if (!fn) return; const scrim = li.closest('.vs') ? null : li.closest('.drawer-scrim'); if (scrim) scrim.remove();
    fn({ sensitive: !!m.sensitive }, (res) => { if (!document.body.contains(el)) return; const name = typeof res === 'object' ? res.name : res; setVal(el, kind === 'env' ? `\${env.${name}}` : S.vaultExpr(typeof res === 'object' ? res.ref || res.name : res)); UI.toast(`Created ${esc(name)}`, kind === 'env' ? 'staged · picked · applies with this change' : 'written to the vault · picked', 'ok'); }); });
  /** Show the chosen source in the field's menu button and close the menu. */
  VS.markSrc = (el, src, m) => { el.querySelectorAll('[data-act="vs-src"]').forEach((x) => { const on = x.dataset.v === src; x.classList.toggle('on', on); x.setAttribute('aria-pressed', on); x.setAttribute('aria-checked', on); });
    const sum = el.querySelector('.vs-srcbtn'); if (sum) { sum.innerHTML = srcBtn(src, !!m.sensitive); sum.setAttribute('aria-label', `Where ${m.label} comes from: ${SRCW(src, !!m.sensitive)[0]}`); } const d = el.querySelector('.vs-menu'); if (d) d.open = false; };
  // the source menu is a popover: one open at a time; it closes on a click outside it and on Esc
  document.addEventListener('toggle', (e) => { const d = e.target; if (!d.classList || !d.classList.contains('vs-menu') || !d.open) return; document.querySelectorAll('.vs-menu[open]').forEach((x) => { if (x !== d) x.open = false; }); }, true);
  document.addEventListener('click', (e) => { document.querySelectorAll('.vs-menu[open]').forEach((d) => { if (!d.contains(e.target)) d.open = false; }); }, true);
  document.addEventListener('keydown', (e) => { if (e.key !== 'Escape') return; const d = document.querySelector('.vs-menu[open]'); if (d) { d.open = false; e.preventDefault(); const s = d.querySelector('summary'); if (s) s.focus(); } }, true);
  UI.act('vs-src', (b) => {
    const el = b.closest('.vs'), m = meta(el), src = b.dataset.v; if (el.dataset.src === src) { VS.markSrc(el, src, m); return; }
    // each source keeps what you gave it: switching away and back brings it back (r5 major 14: Vault item → Secret →
    // Vault item read 'Not set yet' and the configured key was gone)
    const mem = (el._vsMem = el._vsMem || {}), h = hid(el); mem[el.dataset.src] = { value: h.value, set: h.dataset.set, pending: el.dataset.pending };
    const back = mem[src] || { value: '' };
    el.dataset.src = src; el.dataset.pending = back.pending || '';
    VS.markSrc(el, src, m);
    h.value = back.value || ''; if (back.set != null) h.dataset.set = back.set; else delete h.dataset.set;
    el.querySelector('.vs-body').innerHTML = body(m, src, back.value || '', el.dataset.id);
    VS.refresh(el);
    if (src !== 'typed') { const q = el.querySelector('.vs-q'); if (q) q.focus(); }
  });
  UI.act('vs-typed', (inp) => { const el = inp.closest('.vs'), m = meta(el); if (m.sensitive || m.vaultOnly) { el.dataset.pending = inp.value ? '1' : ''; hid(el).value = ''; VS.refresh(el); } else setVal(el, inp.value); });
  UI.act('vs-pick', (sel) => { if (sel.value === '__new__') return; const el = sel.closest('.vs'), src = el.dataset.src; setVal(el, !sel.value ? '' : src === 'env' ? `\${env.${sel.value}}` : src === 'vault' ? S.vaultExpr(sel.value) : `\${settings.${sel.value}}`); });
  UI.act('vs-same', (b) => setVal(b.closest('.vs'), b.dataset.ref));
  // initial checks for fields as they appear
  new MutationObserver(() => document.querySelectorAll('.vs:not([data-init])').forEach((el) => { el.dataset.init = '1'; VS.refresh(el, true); })).observe(document.body, { childList: true, subtree: true });

  /* Browse…: the same lookup, larger: every vault, its items and their fields, labels and references only */
  UI.act('vs-vault', (b) => {
    const el = b.closest('.vs'), m = meta(el), st = { vault: '', q: '' }, lid = `${el.dataset.id}-browse`;
    const d = UI.openDrawer(`<div class="modal-head"><div><div class="caps">vault · ${esc(m.label)}</div><h2>Pick a vault item</h2></div><button class="icon-btn" data-close aria-label="Close">${icon('x')}</button></div><div class="modal-body stack" id="vb-body"></div>`);
    d.classList.add('layered'); browseFor = el;
    const draw = () => {
      const vt = V().vaults.find((x) => x.name === st.vault), down = vt && vt.status === 'fail';
      const xs = VS.find('vault', st.q, m).filter((o) => !st.vault || o.vault === st.vault);
      UI.$('#vb-body', d).innerHTML = `${UI.seg([['', 'All vaults']].concat(V().vaults.map((v) => [v.name, v.type === 'onepassword' ? `${v.name} · 1Password` : v.name])), st.vault, 'vb-vault')}
        <div class="input-icon">${icon('search')}<input class="input" id="vb-q" value="${esc(st.q)}" placeholder="Search vault items: a name, a path or a field" aria-label="Search the vaults" aria-controls="${lid}-list"></div>
        ${down ? `<div class="callout danger small">${icon('x')} ${esc(vt.name)} is unreachable: ${esc(vt.detail)}. Nothing to browse until it unlocks.</div>` : `<ul class="vs-list big" id="${lid}-list" role="listbox">${xs.map((o, i) => optHTML(o, lid, i)).join('') || '<li class="vs-none-l" role="presentation">No item matches.</li>'}<li role="option" id="${lid}-onew" class="vs-o vs-onew" data-act="vs-new" data-kind="secret" aria-selected="false">${icon('plus')}<span class="vs-ot"><b>New vault entry…</b></span></li></ul>`}
        <div class="muted tiny">Labels, references and when each changed: the dashboard never reads a value to show it here.</div>`;
      UI.$('#vb-q', d).addEventListener('input', (e) => { st.q = e.target.value; const pos = e.target.selectionStart; draw(); const q = UI.$('#vb-q', d); q.focus(); q.setSelectionRange(pos, pos); });
      d.querySelectorAll('[data-act="vb-vault"]').forEach((x) => x.addEventListener('click', (e) => { e.stopPropagation(); st.vault = x.dataset.v; st.q = ''; draw(); }));
    };
    draw(); setTimeout(() => { const q = UI.$('#vb-q', d); if (q) q.focus(); }, 30);
  });

  /* write-only commit: typed sensitive values go to the vault before the form saves */
  VS.pending = (root) => [...root.querySelectorAll('.vs[data-pending="1"]')];
  VS.commit = (root, prefix) => {
    const els = VS.pending(root); if (!els.length) return Promise.resolve(true);
    const slug = (s) => String(s || 'value').toLowerCase().replace(/[^a-z0-9]+/g, '_').replace(/^_|_$/g, '');
    const items = els.map((el) => { const m = meta(el), inp = el.querySelector('.vs-in'); return { el, name: `${slug(prefix).replace(/_/g, '-')}-${slug(m.k.split('.').pop()).replace(/_/g, '-')}`, len: inp.value.length, inp }; });
    return UI.stepup({ title: `Store ${items.length} value${items.length > 1 ? 's' : ''} in the vault`, purpose: 'secret write', ok: 'Store with passkey', what: `{ purpose: <b>secret_write</b>, vault: <b>house</b>, keys: <b>${items.map((i) => esc(i.name) + ' (' + i.len + ' chars)').join(', ')}</b> }` }).then((ok) => {
      if (!ok) return false;
      items.forEach((i) => {
        const ref = `house/${i.name}`;
        if (!D.secrets.some((s) => s.ref === ref)) D.secrets.unshift({ name: ref, ref, vault: 'house', key: i.name, alias: '', set: new Date().toISOString(), used: [] });
        D.audit.unshift({ ts: new Date().toISOString(), event: 'secret_write', who: 'operator:ops', detail: `${ref} · length ${i.len} · value not recorded`, run: '' });
        i.inp.value = '';
        i.el.dataset.pending = ''; i.el.dataset.src = 'vault';
        VS.markSrc(i.el, 'vault', meta(i.el));
        hid(i.el).value = S.vaultExpr(ref);
        i.el.querySelector('.vs-body').innerHTML = body(meta(i.el), 'vault', hid(i.el).value, i.el.dataset.id);
        VS.refresh(i.el, true);
      });
      S.save(); return true;
    });
  };

  /* "+ Add env var" from any env picker: staged with the parent, one diff */
  UI.CREATE.env = (ctx, done) => UI.form({ drawer: true, caps: 'new env var · conductor.env', title: 'Add an env var', submit: 'Add', submitIcon: 'plus',
    intro: 'Daemon scope: written to <span class="mono">conductor.env</span> when you apply (with a backup), and the daemon restarts through the boot guard. Other scopes live on the runtime, bot, workflow or host.',
    fields: [{ k: 'name', type: 'string', req: true, ph: 'APP_USER', pattern: /^[A-Z_][A-Z0-9_]*$/, patternMsg: 'UPPER_SNAKE_CASE', validate: (v) => ((V().env || []).some((e) => e.name === v && e.scope === 'daemon') ? 'Already set in conductor.env' : '') },
      { k: 'value', type: 'value', req: true, label: 'Value', sources: ['typed', 'vault'], sensitive: !!ctx.sensitive, help: ctx.sensitive ? 'This feeds a sensitive field: reference a vault entry rather than plain text.' : '' }],
    onSubmit: (v) => { const p = VS.parse(v.value); S.stage('env', 'daemon:' + v.name, { scope: 'daemon', name: v.name, value: v.value, plain: p.src === 'typed' }); UI.refreshChrome(); done(v.name); } });
})();
