/* The value-source field: one component for every configurable value.
 *
 * A field's value can be Typed (plain, or write-only when sensitive), an Env
 * var (${env.NAME}), a Secret (${secret.name}), a Vault reference
 * (op://Vault/Item/field[?attribute=otp], or {{ vault "store" "path" }}), or a
 * Setting (${settings.name}, never for a sensitive field). The field shows the
 * reference, never a value, with a resolve check the daemon performs without
 * returning the value (POST /api/v1/refs/resolve → ok | error). A typed
 * sensitive value is written to the vault behind a step-up when the form is
 * saved, and the field then holds its secret reference.
 *
 * Used by UI.fieldHTML for type 'value' (and 'secret', which is 'value' with
 * sensitive: true), so every form gets the same control. */
(function () {
  const { icon, esc } = UI;
  const VS = (UI.VS = {});
  const V = () => S.view();
  VS.SRC = { typed: 'Typed', env: 'Env var', secret: 'Secret', vault: 'Vault', setting: 'Setting' };
  const ICON = { typed: 'edit', env: 'terminal', secret: 'key', vault: 'lock', setting: 'config' };

  VS.parse = (v) => {
    v = v == null ? '' : String(v); let m;
    if (!v) return { src: null, ref: '' };
    if ((m = v.match(/^\$\{env\.([A-Za-z_]\w*)\}$/))) return { src: 'env', name: m[1], ref: v, label: v };
    if ((m = v.match(/^\$\{settings\.([\w.-]+)\}$/))) return { src: 'setting', name: m[1], ref: v, label: v };
    if ((m = v.match(/^\$\{secret\.([\w./-]+)\}$/))) return { src: 'secret', name: m[1], ref: v, label: 'secret: ' + m[1] };
    if (/^op:\/\//.test(v)) return { src: 'vault', vault: 'op', path: v.slice(5), ref: v, label: v };
    if ((m = v.match(/^\{\{\s*vault\s+"([^"]+)"\s+"([^"]+)"\s*\}\}$/))) return { src: 'vault', vault: m[1], path: m[2], ref: v, label: m[1] === 'op' ? 'op://' + m[2] : `${m[1]}: ${m[2]}` };
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
    if (p.src === 'secret') { const s = V().secrets.find((x) => x.name === p.name); if (!s) return { state: 'fail', why: `no secret "${p.name}"` }; const vt = V().vaults.find((x) => x.name === s.vault); if (vt && vt.status === 'fail') return { state: 'fail', why: `vault ${vt.name}: ${vt.detail}` }; if (s.warn) return { state: 'fail', why: s.warn }; return { state: 'ok', why: `in vault ${s.vault}` }; }
    if (p.vault === 'op') {
      const [path, q] = p.path.split('?'); const [vault, item, ...f] = path.split('/'); const field = f.join('/');
      const it = D.opItems.find((x) => x.vault === vault && x.title === item);
      if (!it) return { state: 'fail', why: `1Password: no item ${vault}/${item}` };
      const fd = it.fields.find(([l]) => l === field); if (!fd) return { state: 'fail', why: `1Password: ${item} has no field "${field}"` };
      if (/attribute=otp/.test(q || '') && fd[1] !== 'otp') return { state: 'fail', why: `"${field}" isn't a one-time-password field` };
      return { state: 'ok', why: `1Password · ${vault}/${item}` };
    }
    const vt = V().vaults.find((x) => x.name === p.vault);
    if (!vt) return { state: 'fail', why: `no vault "${p.vault}"` };
    if (vt.status === 'fail') return { state: 'fail', why: `vault ${vt.name}: ${vt.detail}` };
    return { state: 'ok', why: `vault ${vt.name}` };
  };
  /** A sensitive field may not come from plain config. */
  VS.blocked = (sensitive, v) => (sensitive && VS.parse(v).src === 'setting' ? 'Blocked: a setting is plain config, written into config.yaml and shown in every diff and backup. A sensitive value has to come from a secret, a vault reference or an env var.' : '');
  VS.warn = (sensitive, v) => { const p = VS.parse(v); if (!sensitive || p.src !== 'env') return ''; const e = envOf(p.name); return e && e.plain && e.scope === 'daemon' ? `${p.name} is plain text in conductor.env: anything that can read that file sees it. Store it as a secret and reference that instead.` : ''; };

  /* ---------- markup ---------- */
  const optsEnv = () => (V().env || D.env || []).map((e) => [e.name, `${e.name} · ${scopeLabel(e.scope)}`]);
  const optsSecret = () => V().secrets.filter((s) => /^[\w./-]+$/.test(s.name)).map((s) => [s.name, `${s.name} · ${s.vault}`]);
  const optsSetting = () => V().settings.map((s) => [s.name, `${s.name}${s.env ? ' · from env' : ''}`]);
  function body(fd, src, val, id) {
    const p = VS.parse(val), cur = p.src === src ? p : {};
    const sel = (kind, opts, label, create) => `<select class="select mono vs-pick" id="${id}-pick" ${create ? `data-picker="${create}" data-ctx='${esc(JSON.stringify({ sensitive: !!fd.sensitive }))}' data-ret="name"` : ''} data-on-change="vs-pick" aria-label="${esc(label)}">${create ? `<option value="__new__">+ ${create === 'env' ? 'Add env var' : 'New secret'}…</option>` : ''}<option value="">pick…</option>${opts.map(([v, l]) => `<option value="${esc(v)}"${cur.name === v ? ' selected' : ''}>${esc(l)}</option>`).join('')}</select>`;
    if (src === 'typed') return fd.sensitive
      ? `<input class="input mono vs-in" id="${id}-in" type="password" autocomplete="new-password" value="" placeholder="${p.src === 'typed' ? 'set · type to replace' : 'write-only: stored in the vault when you save'}" data-on-input="vs-typed" aria-label="${esc(fd.label || fd.k)} (typed, write-only)">`
      : `<input class="input mono vs-in" id="${id}-in" value="${esc(cur.ref || '')}" placeholder="${esc(fd.ph || '')}" data-on-input="vs-typed" aria-label="${esc(fd.label || fd.k)} (typed)">`;
    if (src === 'env') return sel('env', optsEnv(), `${fd.label || fd.k}: env var`, 'env');
    if (src === 'secret') return sel('secret', optsSecret(), `${fd.label || fd.k}: secret`, 'secret');
    if (src === 'setting') return sel('setting', optsSetting(), `${fd.label || fd.k}: setting`);
    return `<div class="vs-vault"><span class="vs-ref mono${cur.ref ? '' : ' muted'}">${cur.ref ? esc(cur.label) : 'nothing picked yet'}</span><button type="button" class="btn sm" data-act="vs-vault">${icon('search')}Browse vault…</button></div><div class="vs-same"></div>`;
  }
  VS.html = (fd, val, id) => {
    const p = VS.parse(val);
    const sources = fd.sources || ['typed', 'env', 'secret', 'vault', 'setting'];
    const src = p.src && sources.includes(p.src) ? p.src : fd.defaultSrc || (fd.sensitive ? 'secret' : 'typed');
    const meta = esc(JSON.stringify({ k: fd.k, label: fd.label || fd.k, sensitive: !!fd.sensitive, otp: !!fd.otp, ph: fd.ph || '', sources }));
    return `<div class="vs" data-vs="${esc(fd.k)}" data-src="${src}" data-meta="${meta}" data-id="${id}"${fd.sensitive ? ' data-sensitive="1"' : ''}>
      <input type="hidden" id="${id}" data-k="${esc(fd.k)}" value="${esc(p.src === src ? (src === 'typed' && fd.sensitive ? '' : val) : '')}"${p.src === 'typed' && fd.sensitive ? ' data-set="1"' : ''}>
      <div class="vs-bar"><div class="vs-seg" role="group" aria-label="Where ${esc(fd.label || fd.k)} comes from">${sources.map((s) => `<button type="button" class="${s === src ? 'on' : ''}" aria-pressed="${s === src}" data-act="vs-src" data-v="${s}">${icon(ICON[s])}<span>${VS.SRC[s]}</span></button>`).join('')}</div>
        <span class="vs-check" data-volatile aria-live="polite"></span></div>
      <div class="vs-body">${body(fd, src, val, id)}</div>
      <div class="vs-msg" data-volatile></div></div>`;
  };

  /* ---------- behaviour ---------- */
  const meta = (el) => JSON.parse(el.dataset.meta);
  const hid = (el) => el.querySelector('input[type=hidden]');
  let seq = 0;
  VS.refresh = (el, quiet) => {
    const m = meta(el), v = hid(el).value, chk = el.querySelector('.vs-check'), msg = el.querySelector('.vs-msg');
    const ref = el.querySelector('.vs-ref'); if (ref) { const p = VS.parse(v); ref.textContent = p.src === 'vault' ? p.label : 'nothing picked yet'; ref.classList.toggle('muted', p.src !== 'vault'); }
    const blocked = VS.blocked(m.sensitive, v), warn = VS.warn(m.sensitive, v);
    msg.innerHTML = blocked ? `<div class="vs-err">${icon('x')}${esc(blocked)}</div>` : warn ? `<div class="vs-warn">${icon('warn')}${esc(warn)} <a class="link" href="#/secrets">Secrets</a></div>` : '';
    el.classList.toggle('blocked', !!blocked);
    const pending = el.dataset.pending === '1';
    if (pending) { chk.innerHTML = `<span class="vs-st wait">${icon('lock')}write-only · stored on save</span>`; }
    else if (el.dataset.src === 'typed') chk.innerHTML = m.sensitive && hid(el).dataset.set ? `<span class="vs-st ok">${icon('check')}set</span>` : '';
    else if (!v) chk.innerHTML = '<span class="vs-st">not set</span>';
    else {
      const my = ++seq; el.dataset.seq = my; chk.innerHTML = `<span class="vs-st run">checking…</span>`;
      setTimeout(() => { if (el.dataset.seq != my) return; const r = VS.resolve(hid(el).value); chk.innerHTML = r.state === 'ok' ? `<span class="vs-st ok" title="${esc(r.why)}">${icon('check')}resolves</span>` : r.state === 'fail' ? `<span class="vs-st fail">${icon('x')}doesn't resolve: ${esc(r.why)}</span>` : ''; el.dataset.resolve = r.state; }, UI.fast ? 15 : 260);
    }
    // "use the same item" shortcuts: another field in this form came from a 1Password item
    const same = el.querySelector('.vs-same');
    if (same) {
      const form = el.closest('form, .vs-scope, .modal, .page') || document;
      const items = [...new Set([...form.querySelectorAll('.vs')].filter((x) => x !== el).map((x) => VS.parse(hid(x).value)).filter((p) => p.vault === 'op').map((p) => p.path.split('?')[0].split('/').slice(0, 2).join('/')))];
      same.innerHTML = items.map((vi) => { const it = D.opItems.find((x) => `${x.vault}/${x.title}` === vi); if (!it) return ''; return it.fields.filter(([, t]) => (m.otp ? t === 'otp' : m.sensitive ? t === 'concealed' : t === 'text' || t === 'email')).map(([l, t]) => { const r = `op://${vi}/${l}${t === 'otp' ? '?attribute=otp' : ''}`; return r === v ? '' : `<button type="button" class="vs-chip" data-act="vs-same" data-ref="${esc(r)}">${icon('link')}Use the same item: ${esc(it.title)} · ${esc(l)}</button>`; }).join(''); }).join('');
    }
    if (!quiet) hid(el).dispatchEvent(new Event('input', { bubbles: true }));
  };
  const setVal = (el, v) => { hid(el).value = v; delete hid(el).dataset.set; el.dataset.pending = ''; VS.refresh(el); };
  UI.act('vs-src', (b) => {
    const el = b.closest('.vs'), m = meta(el), src = b.dataset.v; if (el.dataset.src === src) return;
    el.dataset.src = src; el.dataset.pending = '';
    b.parentElement.querySelectorAll('button').forEach((x) => { x.classList.toggle('on', x === b); x.setAttribute('aria-pressed', x === b); });
    hid(el).value = ''; delete hid(el).dataset.set;
    el.querySelector('.vs-body').innerHTML = body(m, src, '', el.dataset.id);
    VS.refresh(el);
    if (src === 'vault') { const nb = el.querySelector('[data-act="vs-vault"]'); if (nb) nb.focus(); }
  });
  UI.act('vs-typed', (inp) => { const el = inp.closest('.vs'), m = meta(el); if (m.sensitive) { el.dataset.pending = inp.value ? '1' : ''; hid(el).value = ''; VS.refresh(el); } else setVal(el, inp.value); });
  UI.act('vs-pick', (sel) => { if (sel.value === '__new__') return; const el = sel.closest('.vs'), src = el.dataset.src; setVal(el, !sel.value ? '' : src === 'env' ? `\${env.${sel.value}}` : src === 'secret' ? `\${secret.${sel.value}}` : `\${settings.${sel.value}}`); });
  UI.act('vs-same', (b) => setVal(b.closest('.vs'), b.dataset.ref));
  // initial checks for fields as they appear
  new MutationObserver(() => document.querySelectorAll('.vs:not([data-init])').forEach((el) => { el.dataset.init = '1'; VS.refresh(el, true); })).observe(document.body, { childList: true, subtree: true });

  /* the vault browser: metadata only (titles, websites, field labels, paths) */
  UI.act('vs-vault', (b) => {
    const el = b.closest('.vs'), m = meta(el);
    const st = { vault: 'op', q: '' };
    const d = UI.openDrawer(`<div class="modal-head"><div><div class="caps">vault · ${esc(m.label)}</div><h2>Pick a vault item</h2></div><button class="icon-btn" data-close aria-label="Close">${icon('x')}</button></div><div class="modal-body stack" id="vb-body"></div>`);
    d.classList.add('layered');
    const draw = () => {
      const vt = V().vaults.find((x) => x.name === st.vault) || { type: 'onepassword', status: 'ok' };
      let list = '';
      if (vt.status === 'fail') list = `<div class="callout danger small">${icon('x')} ${esc(vt.name)} is unreachable: ${esc(vt.detail)}. Nothing to browse until it unlocks.</div>`;
      else if (st.vault === 'op') list = D.opItems.filter((i) => !st.q || `${i.title} ${i.website} ${i.vault}`.toLowerCase().includes(st.q.toLowerCase())).map((i) => `<div class="vb-item"><div class="vb-head"><span class="sc-ic sm">${icon('key')}</span><span class="grow"><b>${esc(i.title)}</b><span class="mono tiny muted">${esc(i.vault)} · ${esc(i.website.replace(/^https:\/\//, ''))}</span></span></div><div class="vb-fields">${i.fields.map(([l, t]) => `<button type="button" class="vs-chip" data-vb="op://${esc(i.vault)}/${esc(i.title)}/${esc(l)}${t === 'otp' ? '?attribute=otp' : ''}">${esc(l)} <span class="muted">${t === 'otp' ? 'one-time code' : t}</span></button>`).join('')}</div></div>`).join('') || '<div class="muted small">No item matches.</div>';
      else list = V().secrets.filter((s) => s.vault === st.vault && (!st.q || s.ref.includes(st.q))).map((s) => { const path = s.ref.split('/').slice(1).join('/'); return `<button type="button" class="vb-path" data-vb='{{ vault "${esc(st.vault)}" "${esc(path)}" }}'>${icon('file')}<span class="mono">${esc(path)}</span></button>`; }).join('') || '<div class="muted small">No entries under this vault yet.</div>';
      UI.$('#vb-body', d).innerHTML = `${UI.seg(V().vaults.filter((v) => v.type !== 'env').map((v) => [v.name, v.type === 'onepassword' ? '1Password' : v.name]), st.vault, 'vb-vault')}
        <div class="input-icon">${icon('search')}<input class="input" id="vb-q" value="${esc(st.q)}" placeholder="search titles, websites, paths" aria-label="Search the vault"></div>
        <div class="vb-list">${list}</div><div class="muted tiny">Labels and references only: the dashboard never reads a value to show it here.</div>`;
      UI.$('#vb-q', d).addEventListener('input', (e) => { st.q = e.target.value; const pos = e.target.selectionStart; draw(); const q = UI.$('#vb-q', d); q.focus(); q.setSelectionRange(pos, pos); });
      d.querySelectorAll('[data-act="vb-vault"]').forEach((x) => x.addEventListener('click', (e) => { e.stopPropagation(); st.vault = x.dataset.v; st.q = ''; draw(); }));
      d.querySelectorAll('[data-vb]').forEach((x) => x.addEventListener('click', () => { setVal(el, x.dataset.vb); d.remove(); }));
    };
    draw();
  });

  /* write-only commit: typed sensitive values go to the vault before the form saves */
  VS.pending = (root) => [...root.querySelectorAll('.vs[data-pending="1"]')];
  VS.commit = (root, prefix) => {
    const els = VS.pending(root); if (!els.length) return Promise.resolve(true);
    const slug = (s) => String(s || 'value').toLowerCase().replace(/[^a-z0-9]+/g, '_').replace(/^_|_$/g, '');
    const items = els.map((el) => { const m = meta(el), inp = el.querySelector('.vs-in'); return { el, name: `${slug(prefix)}_${slug(m.k.split('.').pop())}`, len: inp.value.length, inp }; });
    return UI.stepup({ title: `Store ${items.length} value${items.length > 1 ? 's' : ''} in the vault`, purpose: 'secret write', ok: 'Store with passkey', what: `{ purpose: <b>secret_write</b>, vault: <b>house</b>, keys: <b>${items.map((i) => esc(i.name) + ' (' + i.len + ' chars)').join(', ')}</b> }` }).then((ok) => {
      if (!ok) return false;
      items.forEach((i) => {
        if (!D.secrets.some((s) => s.name === i.name)) D.secrets.unshift({ name: i.name, ref: `house/${i.name}`, vault: 'house', set: new Date().toISOString(), used: [] });
        D.audit.unshift({ ts: new Date().toISOString(), event: 'secret_write', who: 'operator:ops', detail: `house/${i.name} · length ${i.len} · value not recorded`, run: '' });
        i.inp.value = '';
        const segBtn = i.el.querySelector('[data-act="vs-src"][data-v="secret"]');
        i.el.dataset.pending = ''; i.el.dataset.src = 'secret';
        i.el.querySelectorAll('.vs-seg button').forEach((x) => { x.classList.toggle('on', x === segBtn); x.setAttribute('aria-pressed', x === segBtn); });
        hid(i.el).value = `\${secret.${i.name}}`;
        i.el.querySelector('.vs-body').innerHTML = body(meta(i.el), 'secret', hid(i.el).value, i.el.dataset.id);
        VS.refresh(i.el, true);
      });
      S.save(); return true;
    });
  };

  /* "+ Add env var" from any env picker: staged with the parent, one diff */
  UI.CREATE.env = (ctx, done) => UI.form({ drawer: true, caps: 'new env var · conductor.env', title: 'Add an env var', submit: 'Add', submitIcon: 'plus',
    intro: 'Daemon scope: written to <span class="mono">conductor.env</span> when you apply (with a backup), and the daemon restarts through the boot guard. Other scopes live on the runtime, bot, workflow or host.',
    fields: [{ k: 'name', type: 'string', req: true, ph: 'APP_USER', pattern: /^[A-Z_][A-Z0-9_]*$/, patternMsg: 'UPPER_SNAKE_CASE', validate: (v) => ((V().env || []).some((e) => e.name === v && e.scope === 'daemon') ? 'Already set in conductor.env' : '') },
      { k: 'value', type: 'value', req: true, label: 'Value', sources: ['typed', 'secret', 'vault'], sensitive: !!ctx.sensitive, help: ctx.sensitive ? 'This feeds a sensitive field: pick a secret or a vault reference rather than plain text.' : '' }],
    onSubmit: (v) => { const p = VS.parse(v.value); S.stage('env', 'daemon:' + v.name, { scope: 'daemon', name: v.name, value: v.value, plain: p.src === 'typed' }); UI.refreshChrome(); done(v.name); } });
})();
