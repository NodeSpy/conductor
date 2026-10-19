/* Browser logins. "Add a login" has three methods — a username and
 * password stored in conductor's vault, a reference to a vault item (1Password,
 * pass, HashiCorp, conductor), or signing in yourself (takeover) — and every
 * method ends in a binding: the exact origin and which bots may use it (teams
 * never hold access), then the config diff. Credentials are browser.credentials
 * entries naming each value as { vault, key }; a saved session is a logins:
 * opt-in per site. Used only on work the operator started (conductor's rule).
 * Values never reach the page after entry. */
(function () {
  const { icon, esc, ago, tag, st } = UI;
  const now = () => new Date().toISOString();
  const ORIGIN = /^https:\/\/[a-z0-9.-]+(:\d+)?$/;
  const la = { step: 'method', method: null, d: {} };
  const totpOk = (v) => { v = (v || '').trim(); if (!v) return null; const sec = v.startsWith('otpauth://') ? (v.match(/[?&]secret=([A-Z2-7]+)/i) || [])[1] : v.replace(/\s/g, ''); return !!sec && /^[A-Z2-7]{16,}$/i.test(sec); };
  const phone = () => innerWidth <= 720;

  UI.addLoginPage = () => {
    const q = new URLSearchParams(location.hash.split('?')[1] || '');
    if (q.get('m') && la.method !== q.get('m') && la.step === 'method') { la.method = q.get('m'); la.step = la.method === 'takeover' ? 'method' : 'creds'; if (q.get('site')) la.d.origin = 'https://' + q.get('site'); if (la.method === 'session') la.step = 'bind'; }
    const at = { method: 0, creds: 1, bind: 2 }[la.step];
    return `<div class="page">${UI.head('Add a login', { crumbs: [['Browsers', '#/browser'], ['Logins', '#/browser/logins'], ['Add a login']], sub: 'a site a bot may sign in to, and who may use it' })}
      <div class="wizard">${UI.stepper(['Method', 'Credentials', 'Binding', 'Review'], at)}
      ${la.step === 'method' ? methodHTML() : la.step === 'creds' ? credsHTML() : bindHTML()}</div></div>`;
  };
  const METHODS = {
    password: { icon: 'key', title: 'Username &amp; password', desc: 'Type them once. They go to conductor\'s vault, write-only, with an optional TOTP secret.' },
    vault: { icon: 'lock', title: 'From a vault', desc: '1Password, pass, HashiCorp or conductor\'s vault. Pick the item and map its fields; only the reference is stored.', kc: 'var(--color-icon-default)' },
    takeover: { icon: 'hand', title: 'Sign in yourself', desc: 'A live browser: you sign in, conductor keeps the session. Best for SSO, CAPTCHA, SMS or email codes.', kc: 'var(--color-icon-default)' },
    session: { icon: 'cookie', title: 'Saved session (signed in yourself)', desc: '', kc: 'var(--color-icon-default)' },
  };
  const chosen = () => UI.chosen(Object.assign({ caps: 'method', act: 'la-change' }, METHODS[la.method]));
  function methodHTML() {
    const c = (m, off) => UI.choice(Object.assign({ act: 'la-method', attrs: `data-m="${m}"`, on: la.method === m, off }, METHODS[m]));
    return `<div class="wz-card"><h3>How it signs in</h3><p class="lede">Every method ends the same way: exact origins, who may use it, then the config diff.</p>
      <div class="choice-grid">${c('password')}${c('vault')}${c('takeover', phone() ? 'Signing in yourself needs a desktop browser; on the phone, add it from a vault' : '')}</div>
      <div class="wz-notes">${[['lock', 'Values never come back', 'After you save, the page shows set, never the value.'], ['play', 'Test before you trust it', 'Test login signs in once, in a throwaway browser.'], ['shield', 'Narrow by default', 'Exact origins, and operator-started cases only unless you widen it.']].map(([i, t, d]) => `<div class="wz-note">${icon(i)}<b>${t}</b><span>${d}</span></div>`).join('')}</div></div>
      <div class="muted small">${D.access.credentials.length} sign-in credential${D.access.credentials.length === 1 ? '' : 's'} and ${D.access.logins.length} saved session${D.access.logins.length === 1 ? '' : 's'} already · <a class="link" href="#/browser/logins">manage them</a></div>`;
  }
  const CF = [
    { k: 'username', type: 'value', req: true, label: 'Username', ph: 'svc-wiki', sources: ['typed', 'vault'], vaultOnly: true },
    { k: 'password', type: 'value', req: true, sensitive: true, label: 'Password', sources: ['typed', 'vault'] },
    { k: 'otp', type: 'value', sensitive: true, otp: true, label: 'One-time code (TOTP)', sources: ['typed', 'vault'], help: 'Optional. A typed base32 seed or otpauth:// URI is checked here locally; from 1Password, pick the item\'s one-time password field.' },
  ];
  function credsHTML() {
    const vault = la.method === 'vault', src = vault ? 'vault' : 'typed';
    const items = D.opItems.filter((i) => !la.d.q || (i.title + ' ' + i.website + ' ' + i.vault).toLowerCase().includes(la.d.q.toLowerCase()));
    const browser = vault ? `<div class="field"><label for="la-q">Fill all three from one 1Password item</label><div class="input-icon">${icon('search')}<input class="input" id="la-q" placeholder="search items: title, website, vault" value="${esc(la.d.q || '')}" data-on-input="la-q" aria-label="Search 1Password items"></div>
      <div class="list op-items" id="la-items">${items.map((i) => { const ix = D.opItems.indexOf(i); return `<button type="button" class="item click${la.d.item === ix ? ' on' : ''}" data-act="la-item" data-i="${ix}"><span class="sc-ic sm">${icon('key')}</span><span class="grow"><b>${esc(i.title)}</b> <span class="mono tiny muted">${esc(i.vault)} · ${esc(i.website.replace(/^https:\/\//, ''))}</span><span class="muted tiny" style="display:block">${i.fields.map(([l]) => esc(l)).join(' · ')}</span></span></button>`; }).join('') || '<div class="item muted small">No item matches.</div>'}</div>
      <div class="hint">Titles, websites and field labels only. Or set each field's source yourself below.</div></div>` : '';
    return `${chosen()}<div class="wz-card" id="la-form" data-pre="la-"><h3>The credentials</h3><p class="lede">Each field can be typed, or come from an env var, a secret or a vault. Only references are kept; a typed password goes to the vault, write-only.</p>
      ${browser}
      ${CF.map((f) => UI.fieldHTML(Object.assign({ defaultSrc: src }, f), la.d[f.k] || '', 'la-')).join('')}
      <div class="hint" id="la-totp-ok"></div>
      <div class="wizard-foot"><button class="btn ghost" data-act="la-back">Back</button><span class="spacer"></span><button class="btn primary" data-act="la-creds">Next: binding ${icon('chev')}</button></div></div>`;
  }
  function bindHTML() {
    const name = la.d.name || (la.d.origin || '').replace(/^https:\/\//, '').split('.')[0] || '';
    const session = la.method === 'session';
    return `${chosen()}<div class="wz-card" id="la-bind"><h3>Where, and for whom</h3><p class="lede">${la.d.password ? `Password from <span class="mono">${esc(UI.VS.parse(la.d.password).label)}</span>. ` : la.pending && la.pending.password ? 'The typed values are stored in conductor\'s vault when you finish. ' : ''}${session ? 'The saved session is used only on this site, only by these bots.' : 'The login is filled only on this origin, only for these bots.'}</p>
      ${session ? '' : UI.fieldHTML({ k: 'name', type: 'string', req: true, label: 'Login name', ph: 'grafana', help: 'What a bot asks for: conductor browser login --credential <name>.' }, name)}
      ${UI.fieldHTML({ k: 'origin', type: 'string', req: true, label: session ? 'Site' : 'Origin (exact scheme + host)', ph: 'https://grafana.example.com', help: session ? 'The host the saved session belongs to.' : 'Filled only here, checked atomically with the fill: a lookalike host, a frame or a redirect elsewhere is refused.' }, la.d.origin || '')}
      ${UI.fieldHTML({ k: 'identities', type: 'select-multi', req: true, label: 'Which bots may use it', options: UI.identityOptions(), create: 'bot', createLabel: 'New bot', help: 'Access goes to bots by name. A team entry adds every bot on it now (later bots don\'t inherit it). Folders never hold access.' }, [])}
      ${session ? '' : UI.fieldHTML({ k: 'approval', type: 'bool', label: 'Phone approval', on: 'the site asks for an approval on your phone after sign-in (the bot waits; you get a Needs-you item)' }, false)}
      <div class="callout small">${icon('shield')} Used only on work <b>you</b> started: a bot's own sweep or a request from someone else can't use it. That's conductor's rule, not a setting.</div>
      <div class="form-err callout danger small hide" id="la-err"></div>
      <div class="wizard-foot"><button class="btn ghost" data-act="la-back">Back</button><span class="spacer"></span><button class="btn primary" data-act="la-save">${icon('check')}Review &amp; apply</button></div></div>`;
  }
  UI.act('la-method', (b) => { la.method = b.dataset.m; la.d = {}; if (la.method === 'takeover') { UI.go('#/browser/takeover'); return; } la.step = 'creds'; UI.rerender(); });
  UI.act('la-change', () => { la.step = 'method'; UI.rerender(); });
  UI.act('la-back', () => { la.step = la.step === 'bind' && la.method !== 'session' ? 'creds' : 'method'; UI.rerender(); });
  document.addEventListener('input', (e) => { if (e.target.id !== 'la-otp-in') return; const ok = totpOk(e.target.value); const h = UI.$('#la-totp-ok'); if (h) h.innerHTML = ok == null ? '' : ok ? '<span class="totp-ok">TOTP ••• ••• · code works ✓</span>' : '<span class="danger-text">TOTP: not a valid secret</span>'; });
  UI.act('la-q', (el) => { la.d.q = el.value; const pos = el.selectionStart; keep(); UI.rerender(); const n = UI.$('#la-q'); if (n) { n.focus(); n.setSelectionRange(pos, pos); } });
  UI.act('la-item', (b) => { const it = D.opItems[+b.dataset.i]; la.d.item = +b.dataset.i; const pick = (re) => (it.fields.find(([, t]) => re.test(t)) || [])[0];
    const u = pick(/text|email/), p = pick(/concealed/), o = pick(/otp/);
    const X = (f, otp) => `{{ vault "op" "${it.vault}/${it.title}/${f}${otp ? '?attribute=otp' : ''}" }}`; la.d.username = u ? X(u) : ''; la.d.password = p ? X(p) : ''; la.d.otp = o ? X(o, true) : ''; la.d.origin = it.website.replace(/\/$/, ''); UI.rerender(); });
  /** keep the fields' references (never typed sensitive values) across a re-render */
  const keep = () => { const f = UI.$('#la-form'); if (!f) return; CF.forEach((fd) => { const v = UI.readField(f, fd); if (v) la.d[fd.k] = v; }); };
  UI.act('la-creds', () => {
    const f = UI.$('#la-form'), v = UI.formValues(f, CF);
    if (!UI.checkFields(f, CF, v)) return;
    const t = UI.$('#la-otp-in'); if (t && t.value && !totpOk(t.value)) return UI.nudge(t, 'not a valid TOTP secret');
    // typed sensitive values stay in memory only, until the vault write at the end
    la.pending = {}; UI.VS.pending(f).forEach((el) => { const k = el.dataset.vs, inp = el.querySelector('.vs-in'); la.pending[k] = inp.value; inp.value = ''; });
    CF.forEach((fd) => { la.d[fd.k] = v[fd.k] || ''; });
    if (!la.d.origin && la.d.item != null) la.d.origin = D.opItems[la.d.item].website.replace(/\/$/, '');
    la.step = 'bind'; UI.rerender();
  });
  UI.act('la-save', () => {
    const session = la.method === 'session', f = UI.$('#la-bind');
    const fields = (session ? [] : [{ k: 'name', type: 'string', req: true, pattern: /^[a-z][\w-]*$/, patternMsg: 'letters, digits, - and _', validate: (v) => (D.access.credentials.some((c) => c.name === v) ? 'A login by that name exists.' : '') }]).concat([{ k: 'origin', type: 'string', req: true, validate: (v) => (session ? (/^(https:\/\/)?[\w.-]+$/.test(v || '') ? '' : 'a host such as app.example.com') : /^https:\/\/[\w.-]+(:\d+)?(\/[\w./-]*)?$/.test(v || '') ? '' : 'https:// plus a host (and an optional path prefix)') }, { k: 'identities', type: 'select-multi', req: true }, { k: 'approval', type: 'bool' }]);
    const v = UI.formValues(f, fields); if (!UI.checkFields(f, fields, v)) return;
    const ids = UI.expandIdentities(v.identities);
    if (session) { const site = v.origin.replace(/^https:\/\//, ''); let last = null; ids.forEach((id) => { const cur = JSON.parse(JSON.stringify(S.staged('identities', id) || { allow: [site], logins: [], evaluate: 'off' })); cur.logins = [...new Set((cur.logins || []).concat([site]))]; cur.allow = [...new Set((cur.allow || []).concat([site]))]; last = S.stage('identities', id, cur, `opt ${id} in to logged-in use of ${site}`); }); D.prefs.returnTo = { href: '#/browser/logins', label: 'Logins' }; la.step = 'method'; la.method = null; la.d = {}; S.save(); return UI.staged(last); }
    const ref = (expr) => { const p = UI.VS.parse(expr); return p.src === 'vault' && !p.retired ? { vault: p.vault, key: p.path } : null; };
    const n = { origin: v.origin, identities: ids }; if (v.approval) n.approval = true;
    ['username', 'password', 'otp'].forEach((k) => { const r = la.d[k] && ref(la.d[k]); if (r) n[k] = r; });
    const finish = () => { UI.staged(S.stage('credentials', v.name, n)); D.prefs.returnTo = { href: '#/browser/logins', label: 'Logins' }; la.step = 'method'; la.method = null; la.d = {}; la.pending = null; S.save(); };
    const typed = Object.entries(la.pending || {}).filter(([, x]) => x);
    if (!typed.length) return finish();
    UI.stepup({ title: `Store the login for ${esc(v.origin)}`, purpose: 'secret write', ok: 'Store with passkey', what: `{ purpose: <b>secret_write</b>, vault: <b>house</b>, keys: <b>${typed.map(([k, x]) => `login-${esc(v.name)}-${k} (${x.length} chars)`).join(', ')}</b> }` }).then((ok) => {
      if (!ok) return;
      typed.forEach(([k]) => { const key = `login-${v.name}-${k}`, r = `house/${key}`; D.secrets.unshift({ name: r, ref: r, vault: 'house', key, alias: '', set: now(), used: [`browser.credentials.${v.name}.${k}`] }); n[k] = { vault: 'house', key }; });
      D.audit.unshift({ ts: now(), event: 'secret_write', who: 'operator:ops', detail: `house/login-${v.name}-* · values not recorded`, run: '' });
      finish();
    });
  });

  /* ---------- the Logins tab ---------- */
  const meta = (n) => (D.loginMeta[n] = D.loginMeta[n] || { uses: [] });
  const testTag = (t) => (!t ? tag('not tested') : t.result === 'ok' ? UI.statusMark('success', `works · ${UI.agoText(t.at)}`, { sm: true }) : t.result === 'needs you' ? tag('needs you: push approval', 'wait') : tag('failed', 'fail'));
  const lv = { open: null };
  UI.act('login-more', (b) => UI.menu(b, [['Where it was used, and its vault entries', 'runs', 'login-uses', `data-n="${b.dataset.n}"`], ['Edit which bots may use it…', 'edit', 'login-bind', `data-n="${b.dataset.n}"`], b.dataset.m === 'password' ? ['Replace the password…', 'updates', 'login-replace', `data-n="${b.dataset.n}"`] : null, ['-'], ['Remove this login…', 'trash', 'lg-revoke', `data-n="${b.dataset.n}"`]].filter(Boolean)));
  UI.loginsPage = () => {
    const creds = D.access.credentials, sessions = D.access.logins;
    const rref = (r) => (r && r.vault ? `<span class="vref mono" title="{{ vault &quot;${esc(r.vault)}&quot; &quot;${esc(r.key)}&quot; }}">${esc(r.vault)} · ${esc(r.key)}</span>` : '');
    // one compact line per login (the Overview's row): its state, the site and who it's for, what it signs in with in
    // words, when it last worked; Test and a menu. The vault paths and the use history are one click away.
    const VN = { house: 'the conductor vault', op: '1Password', store: 'pass' };
    const row = (c) => { const cf = S.staged('credentials', c.name) || c.cfg || {}; const m = meta(c.name), method = cf.passkey ? 'passkey' : (cf.password && cf.password.vault === 'house') ? 'password' : 'vault';
      const t = m.test, s2 = !t ? 'idle' : t.result === 'ok' ? 'ok' : t.result === 'needs you' ? 'wait' : 'fail', parts = ['username', 'password', 'otp', 'passkey'].filter((k) => cf[k]);
      const from = [...new Set(parts.map((k) => (cf[k] || {}).vault).filter(Boolean))].map((v) => VN[v] || v).join(' and ');
      return `<li class="dl-conn login-row${s2 === 'wait' || s2 === 'fail' ? ' attn' : ''}" data-s="${s2}" data-login="${c.name}"><span class="dl-ng">${s2 === 'wait' || s2 === 'fail' ? UI.mark(s2 === 'wait' ? 'ember' : 'fail', { lg: true, label: s2 === 'wait' ? 'Needs you' : 'Failed' }) : ''}</span>
        <span class="dl-nt"><b>${esc(c.name)} <span class="tgt">${esc(String(cf.origin || '').replace(/^https?:\/\//, ''))}</span> ${S.find('credentials', c.name) ? UI.statusMark('staged', 'Not applied') : ''}</b>
          <span class="${s2 === 'wait' || s2 === 'fail' ? 'reason' : 'dl-why'}">${s2 === 'wait' ? 'Needs you: a push approval on your phone' : s2 === 'fail' ? 'The last sign-in failed' : `${s2 === 'ok' ? 'Works · ' : ''}For ${(cf.identities || []).map((x) => /^bot:/.test(x) ? UI.nm(x) : esc(x.replace(/^agent:/, ''))).join(', ')} · ${method === 'passkey' ? 'a passkey' : parts.map((k) => ({ otp: 'a one-time code', username: 'a username', password: 'a password' }[k])).filter(Boolean).join(', ')}${from ? ` from ${from}` : ''}${cf.approval ? ' · approved on your phone' : ''} · last used ${m.lastUsed ? UI.agoText(m.lastUsed) : 'never'}`}</span></span>
        <span class="dl-na"><button class="btn sm secondary" data-act="login-test" data-n="${c.name}">Test</button><button class="icon-btn" data-act="login-more" data-n="${c.name}" data-m="${method}" aria-label="More for ${esc(c.name)}">${icon('more')}</button></span>
        ${lv.open === c.name ? `<div class="login-detail"><dl class="dl-facts">${parts.map((k) => `<dt>${{ otp: 'One-time code', passkey: 'Passkey', username: 'Username', password: 'Password' }[k]}</dt><dd class="mono">{{ vault "${esc(cf[k].vault)}" "${esc(cf[k].key)}" }}</dd>`).join('')}<dt>Used by</dt><dd>${(m.uses || []).map((u) => `${esc(u.bot)} on task ${esc(u.case)}, ${UI.agoText(u.at)}`).join('<br>') || 'Not used yet.'}</dd></dl></div>` : ''}</li>`; };
    return `<div class="page">${UI.head('Logins', { crumbs: [['Browsers', '#/browser'], ['Logins']], actions: `<a class="btn primary" href="#/browser/add">${icon('key')}Add a login</a><a class="btn" href="#/browser/passkey">${icon('plus')}Register a passkey</a>` })}
      ${UI.browserTabs('logins')}
      <div class="panel"><div class="panel-head"><h3>Sign-ins</h3></div><ul class="dl-list dl-conns">${creds.map(row).join('')}</ul></div>
      <div class="panel"><div class="panel-head"><h3>Saved sessions</h3><span class="muted small">signed in yourself · state, not config</span></div><div class="list">${sessions.map((l) => `<div class="item"><span class="sc-ic sm">${icon('cookie')}</span><div class="grow small"><b>${esc(l.site)}</b> ${tag('session')}<div class="mono tiny muted">${esc(l.identity)} · ${l.cookies} cookies · seeded ${ago(l.seeded)} ago · expires in ${esc(l.expires || '30d')}</div></div><button class="btn sm secondary" data-act="login-revoke-session" data-site="${esc(l.site)}" data-id="${esc(l.identity)}">Revoke</button></div>`).join('') || '<div class="item small muted">No saved sessions.</div>'}</div></div></div>`;
  };
  UI.act('login-uses', (b) => { lv.open = lv.open === b.dataset.n ? null : b.dataset.n; UI.rerender(); });
  UI.act('login-test', (b) => {
    const c = D.access.credentials.find((x) => x.name === b.dataset.n), cf = c.cfg || {}, m = meta(c.name);
    const s = UI.modal(`<div class="modal-head"><div><div class="caps">test login · sandboxed browser</div><h2>Testing ${esc(c.name)}</h2></div><button class="icon-btn" data-close aria-label="Close">${icon('x')}</button></div><div class="modal-body small" id="lt-body">${st('run', 'signing in to ' + esc(cf.origin) + '…')}<div class="muted tiny" style="margin-top:6px">No values and no screenshots of filled fields are recorded.</div></div><div class="modal-foot"><button class="btn" data-close>Close</button></div>`);
    setTimeout(() => {
      const res = /app\.example\.com/.test(cf.origin) ? 'needs you' : /statuspage/.test(cf.origin) ? 'failed' : 'ok';
      m.test = { result: res, at: now() }; S.save();
      const body = UI.$('#lt-body', s); if (!body) return;
      body.innerHTML = res === 'ok' ? `${st('ok', 'signed in')}<div class="muted tiny">origin ✓ · frame ✓ · fields ✓ · logged out again</div>` : res === 'failed' ? `${st('fail', 'sign-in refused')}<div class="muted tiny">the site rejected the credentials · nothing was saved</div>` : `${st('wait', 'needs you: approve the push on your phone')}<div class="row" style="margin-top:8px"><button class="btn sm primary" id="lt-approved">I approved it</button></div>`;
      const ap = UI.$('#lt-approved', s); if (ap) ap.addEventListener('click', () => { m.test = { result: 'ok', at: now() }; S.save(); body.innerHTML = `${st('ok', 'signed in after your approval')}`; UI.rerender(); });
      UI.rerender();
    }, UI.fast ? 40 : 900);
  });
  UI.act('login-bind', (b) => { const n = b.dataset.n, cf = JSON.parse(JSON.stringify(S.staged('credentials', n))); UI.form({ caps: `browser.credentials[${n}]`, title: `Binding for ${n}`, edit: true, values: { origin: cf.origin, identities: cf.identities || [], approval: !!cf.approval },
    fields: [{ k: 'origin', type: 'string', req: true, label: 'Origin', validate: (v) => (/^https:\/\/[\w.-]+(:\d+)?(\/[\w./-]*)?$/.test(v || '') ? '' : 'https:// plus a host') }, { k: 'identities', type: 'select-multi', req: true, label: 'Which bots may use it', options: UI.identityOptions(), create: 'bot', createLabel: 'New bot' }, { k: 'approval', type: 'bool', label: 'Phone approval', on: 'the site asks for an approval on your phone after sign-in' }],
    submit: 'Review change', submitIcon: 'check', onSubmit: (v) => { Object.assign(cf, { origin: v.origin, identities: UI.expandIdentities(v.identities) }); if (v.approval) cf.approval = true; else delete cf.approval; UI.staged(S.stage('credentials', n, cf)); } }); });
  UI.act('login-replace', (b) => UI.form({ caps: 'write-only', title: `Replace ${b.dataset.n}'s password`, fields: [{ k: 'password', type: 'password', req: true, label: 'New password' }], submit: 'Store with passkey', submitIcon: 'passkey', onSubmit: (v, form) => { form.querySelector('input').value = ''; UI.closeModal(); UI.stepup({ title: 'Replace the password', purpose: 'secret write', what: `{ vault: <b>house</b>, key: <b>login/${esc(b.dataset.n)}/password</b> }` }).then((ok) => { if (!ok) return; const s = D.secrets.find((x) => x.ref === `house/login/${b.dataset.n}/password`); if (s) s.set = now(); S.save(); UI.rerender(); UI.toast('Password replaced', 'shown only as set', 'ok'); }); return false; } }));
  UI.act('lg-revoke', (b) => UI.confirm({ title: `Revoke ${b.dataset.n}?`, body: 'Stages removing the binding; bots can no longer use it. Stored values for a username & password login are deleted from the vault when it applies.', ok: 'Stage revoke', danger: true }).then((ok) => { if (!ok) return; D.prefs.returnTo = { href: '#/browser/logins', label: 'Logins' }; UI.staged(S.stage('credentials', b.dataset.n, null)); }));
  UI.act('login-revoke-session', (b) => UI.confirm({ title: `Log out of ${b.dataset.site}?`, body: 'Deletes the saved cookie jar now. Immediate, audited.', ok: 'Revoke', danger: true }).then((ok) => { if (!ok) return; D.access.logins = D.access.logins.filter((l) => !(l.site === b.dataset.site && l.identity === b.dataset.id)); S.save(); UI.rerender(); }));
})();
