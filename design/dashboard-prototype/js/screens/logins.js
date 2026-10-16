/* Browser logins (#182). "Add a login" has three methods — a username and
 * password stored in conductor's vault, a reference to a vault item (1Password,
 * pass, HashiCorp, conductor), or signing in yourself (takeover) — and every
 * method ends in a binding: exact origins, which bots/teams/folders may use
 * it, operator-started cases only (on by default), then the config diff.
 * Values never reach the page after entry: they're shown as "set". */
(function () {
  const { icon, esc, ago, tag, st } = UI;
  const now = () => new Date().toISOString();
  const ORIGIN = /^https:\/\/[a-z0-9.-]+(:\d+)?$/;
  const la = { step: 'method', method: null, d: {} };
  const totpOk = (v) => { v = (v || '').trim(); if (!v) return null; const sec = v.startsWith('otpauth://') ? (v.match(/[?&]secret=([A-Z2-7]+)/i) || [])[1] : v.replace(/\s/g, ''); return !!sec && /^[A-Z2-7]{16,}$/i.test(sec); };
  const allowOpts = () => S.view().bots.map((b) => 'bot:' + b.name).concat(S.view().teams.map((t) => 'team:' + t.name), UI.allFolders(false).map((f) => 'folder:' + f));
  const phone = () => innerWidth <= 720;

  UI.addLoginPage = () => {
    const q = new URLSearchParams(location.hash.split('?')[1] || '');
    if (q.get('m') && la.method !== q.get('m') && la.step === 'method') { la.method = q.get('m'); la.step = la.method === 'takeover' ? 'method' : 'creds'; if (q.get('site')) la.d.origin = 'https://' + q.get('site'); if (la.method === 'session') la.step = 'bind'; }
    const at = { method: 0, creds: 1, bind: 2 }[la.step];
    return `<div class="page">${UI.head('Add a login', { crumbs: '<a href="#/browser">Browsers</a> › <a href="#/browser/logins">Logins</a>', sub: 'a site a bot may sign in to, and who may use it' })}
      <div class="wizard">${UI.stepper(['Method', 'Credentials', 'Binding', 'Review'], at)}
      ${la.step === 'method' ? methodHTML() : la.step === 'creds' ? credsHTML() : bindHTML()}</div></div>`;
  };
  const METHODS = {
    password: { icon: 'key', title: 'Username &amp; password', desc: 'Type them once. They go to conductor\'s vault, write-only, with an optional TOTP secret.' },
    vault: { icon: 'lock', title: 'From a vault', desc: '1Password, pass, HashiCorp or conductor\'s vault. Pick the item and map its fields; only the reference is stored.', kc: 'var(--run)' },
    takeover: { icon: 'hand', title: 'Sign in yourself', desc: 'A live browser: you sign in, conductor keeps the session. Best for SSO, CAPTCHA, SMS or email codes.', kc: 'var(--ok)' },
    session: { icon: 'cookie', title: 'Saved session (signed in yourself)', desc: '', kc: 'var(--ok)' },
  };
  const chosen = () => UI.chosen(Object.assign({ caps: 'method', act: 'la-change' }, METHODS[la.method]));
  function methodHTML() {
    const c = (m, off) => UI.choice(Object.assign({ act: 'la-method', attrs: `data-m="${m}"`, on: la.method === m, off }, METHODS[m]));
    return `<div class="wz-card"><h3>How will the bot sign in?</h3><p class="lede">Every method ends the same way: exact origins, who may use it, then the config diff.</p>
      <div class="choice-grid">${c('password')}${c('vault')}${c('takeover', phone() ? 'Signing in yourself needs a desktop browser; on the phone, add it from a vault' : '')}</div>
      <div class="wz-notes">${[['lock', 'Values never come back', 'After you save, the page shows set, never the value.'], ['play', 'Test before you trust it', 'Test login signs in once, in a throwaway browser.'], ['shield', 'Narrow by default', 'Exact origins, and operator-started cases only unless you widen it.']].map(([i, t, d]) => `<div class="wz-note">${icon(i)}<b>${t}</b><span>${d}</span></div>`).join('')}</div></div>
      <div class="muted small">${D.access.credentials.length} logins already · <a class="link" href="#/browser/logins">manage them</a></div>`;
  }
  const CF = [
    { k: 'username', type: 'value', req: true, label: 'Username', ph: 'svc-wiki' },
    { k: 'password', type: 'value', req: true, sensitive: true, label: 'Password' },
    { k: 'otp', type: 'value', sensitive: true, otp: true, label: 'TOTP', help: 'Optional. A typed secret or otpauth:// URI is checked here locally; a 1Password one-time-password field works as op://…?attribute=otp.' },
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
    return `${chosen()}<div class="wz-card" id="la-bind"><h3>Where, and for whom</h3><p class="lede">${la.d.password ? `Password from <span class="mono">${esc(UI.VS.parse(la.d.password).label)}</span>. ` : la.pending && la.pending.password ? 'The typed password is stored in the vault when you finish. ' : ''}The login is filled only on these origins, only for these bots.</p>
      ${UI.fieldHTML({ k: 'name', type: 'string', req: true, label: 'Login name', ph: 'grafana' }, name)}
      ${UI.fieldHTML({ k: 'origins', type: 'list', req: true, label: 'Origins (exact scheme + host)', ph: 'https://grafana.example.com', help: 'The login is filled only on these origins, checked atomically with the fill.' }, la.d.origin ? [la.d.origin] : [])}
      ${UI.fieldHTML({ k: 'allow', type: 'select-multi', req: true, label: 'Who may use it', options: allowOpts(), create: 'bot', createLabel: 'New bot', help: 'Bots, teams or folders. A team or folder covers the bots in it.' }, [])}
      ${UI.fieldHTML({ k: 'operator_only', type: 'bool', label: 'Provenance', on: 'only for cases an operator started (recommended, as in #182)' }, true)}
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
    la.d.username = u ? `op://${it.vault}/${it.title}/${u}` : ''; la.d.password = p ? `op://${it.vault}/${it.title}/${p}` : ''; la.d.otp = o ? `op://${it.vault}/${it.title}/${o}?attribute=otp` : ''; la.d.origin = it.website.replace(/\/$/, ''); UI.rerender(); });
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
    const f = UI.$('#la-bind'), fields = [{ k: 'name', type: 'string', req: true, pattern: /^[a-z][\w-]*$/, patternMsg: 'letters, digits, - and _', validate: (v) => (D.access.credentials.some((c) => c.name === v) ? 'A login by that name exists.' : '') }, { k: 'origins', type: 'list', req: true, validate: (v) => ((v || []).find((o) => !ORIGIN.test(o)) ? 'Each origin is https:// plus a host, no path.' : '') }, { k: 'allow', type: 'select-multi', req: true }, { k: 'operator_only', type: 'bool' }];
    const v = UI.formValues(f, fields); if (!UI.checkFields(f, fields, v)) return;
    const base = { method: la.method, origin: v.origins[0], origins: v.origins, allow: v.allow, operator_only: !!v.operator_only };
    const finish = (n) => { UI.staged(S.stage('credentials', v.name, Object.assign(base, n))); D.prefs.returnTo = { href: '#/browser/logins', label: 'Logins' }; la.step = 'method'; la.method = null; la.d = {}; S.save(); };
    if (la.method === 'session') return finish({ fields: [] });
    const refs = { username: la.d.username, password: la.d.password }; if (la.d.otp) refs.otp = la.d.otp;
    const typed = Object.entries(la.pending || {}).filter(([, x]) => x);
    const done = () => { la.pending = null; finish(Object.assign({ fields: Object.keys(refs).filter((k) => refs[k]) }, refs)); };
    if (!typed.length) return done();
    UI.stepup({ title: `Store the login for ${esc(v.origins[0])}`, purpose: 'secret write', ok: 'Store with passkey', what: `{ purpose: <b>secret_write</b>, vault: <b>house</b>, keys: <b>${typed.map(([k, x]) => `login_${esc(v.name)}_${k} (${x.length} chars)`).join(', ')}</b> }` }).then((ok) => {
      if (!ok) return;
      typed.forEach(([k]) => { const name = `login_${v.name}_${k}`; D.secrets.unshift({ name, ref: `house/login/${v.name}/${k}`, vault: 'house', set: now(), used: [`browser.credentials.${v.name}.${k}`] }); refs[k] = `\${secret.${name}}`; });
      D.audit.unshift({ ts: now(), event: 'secret_write', who: 'operator:ops', detail: `house/login/${v.name}/* · values not recorded`, run: '' });
      done();
    });
  });

  /* ---------- the Logins tab ---------- */
  const meta = (n) => (D.loginMeta[n] = D.loginMeta[n] || { uses: [] });
  const testTag = (t) => (!t ? tag('not tested') : t.result === 'ok' ? `<span data-s="ok" class="tag">${icon('check')} works · ${ago(t.at)} ago</span>` : t.result === 'needs you' ? tag('needs you: push approval', 'wait') : tag('failed', 'fail'));
  const lv = { open: null };
  UI.loginsPage = () => {
    const creds = D.access.credentials, sessions = D.access.logins;
    const row = (c) => { const cf = S.staged('credentials', c.name) || c.cfg || {}; const m = meta(c.name), method = cf.method || 'vault';
      return `<div class="item login-row" style="align-items:flex-start" data-login="${c.name}"><span class="sc-ic sm">${icon(method === 'passkey' ? 'passkey' : method === 'password' ? 'key' : method === 'session' ? 'cookie' : 'lock')}</span>
        <div class="grow small"><div><b>${esc(c.name)}</b> ${tag(method === 'password' ? 'username & password' : method === 'vault' ? 'vault reference' : method)} ${S.find('credentials', c.name) ? tag('staged', 'run') : ''}</div>
          <div class="mono tiny muted">${esc((cf.origins || [cf.origin]).join(', '))} · ${esc((cf.allow || [cf.identity]).filter(Boolean).join(', '))}${cf.operator_only ? ' · operator-started only' : ''}</div>
          <div class="tiny">${cf.password ? ['username', 'password', 'otp'].filter((k) => cf[k]).map((k) => `${k === 'otp' ? 'TOTP' : k} <span class="mono">${esc(UI.VS.parse(cf[k]).label)}</span>`).join(' · ') : method === 'password' ? 'username <b>set</b> · password <b>set</b>' : method === 'vault' ? `<span class="mono">${esc(Object.values(cf.refs || { r: `${cf.vault}:${cf.ref}` }).join(' · '))}</span>` : method === 'passkey' ? `private key in <span class="mono">${esc(cf.vault)}</span>` : 'session cookies'} · last used ${m.lastUsed ? ago(m.lastUsed) + ' ago' : 'never'} · ${testTag(m.test)}</div>
          ${lv.open === c.name ? `<div class="list uses">${(m.uses || []).map((u) => `<div class="item tiny"><span class="mono">${esc(u.bot)}</span><span class="grow">case ${esc(u.case)}</span><span class="muted mono">${ago(u.at)} ago</span></div>`).join('') || '<div class="item tiny muted">Not used yet.</div>'}<div class="tiny muted">which bot, which case, when — never a value</div></div>` : ''}</div>
        <div class="row wrap" style="gap:4px"><button class="btn sm" data-act="login-test" data-n="${c.name}">${icon('play')}Test</button><button class="btn sm ghost" data-act="login-uses" data-n="${c.name}" aria-expanded="${lv.open === c.name}">${icon('runs')}Usage</button><button class="btn sm ghost" data-act="login-bind" data-n="${c.name}">${icon('edit')}Edit binding</button>${method === 'password' ? `<button class="btn sm ghost" data-act="login-replace" data-n="${c.name}">${icon('updates')}Replace</button>` : ''}<button class="btn sm danger" data-act="login-revoke" data-n="${c.name}">Revoke</button></div></div>`; };
    return `<div class="page">${UI.head('Logins', { crumbs: '<a href="#/browser">Browsers</a>', sub: 'credentials, saved sessions and passkeys bots may use', actions: `<a class="btn primary" href="#/browser/add">${icon('key')}Add a login</a><a class="btn" href="#/browser/passkey">${icon('passkey')}Register a passkey</a>` })}
      ${UI.tabs([['sessions', 'Sessions', '#/browser', D.browsers.length], ['logins', 'Logins', '#/browser/logins', creds.length + sessions.length]], 'logins')}
      <div class="panel"><div class="panel-head"><h3>Credentials &amp; passkeys</h3><span class="muted small">browser.credentials</span></div><div class="list">${creds.map(row).join('')}</div></div>
      <div class="panel" style="margin-top:14px"><div class="panel-head"><h3>Saved sessions</h3><span class="muted small">signed in yourself · state, not config</span></div><div class="list">${sessions.map((l) => `<div class="item"><span class="sc-ic sm">${icon('cookie')}</span><div class="grow small"><b>${esc(l.site)}</b> ${tag('session')}<div class="mono tiny muted">${esc(l.identity)} · ${l.cookies} cookies · seeded ${ago(l.seeded)} ago · expires in ${esc(l.expires || '30d')}</div></div><button class="btn sm danger" data-act="login-revoke-session" data-site="${esc(l.site)}" data-id="${esc(l.identity)}">Revoke</button></div>`).join('') || '<div class="item small muted">No saved sessions.</div>'}</div></div></div>`;
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
  UI.act('login-bind', (b) => { const n = b.dataset.n, cf = JSON.parse(JSON.stringify(S.staged('credentials', n))); UI.form({ caps: `browser.credentials.${n}`, title: `Binding for ${n}`, edit: true, values: { origins: cf.origins || [cf.origin], allow: cf.allow || (cf.identity ? [cf.identity] : []), operator_only: cf.operator_only !== false },
    fields: [{ k: 'origins', type: 'list', req: true, label: 'Origins', validate: (v) => ((v || []).find((o) => !ORIGIN.test(o)) ? 'https:// plus a host, no path' : '') }, { k: 'allow', type: 'select-multi', req: true, label: 'Who may use it', options: allowOpts(), create: 'bot', createLabel: 'New bot' }, { k: 'operator_only', type: 'bool', label: 'Provenance', on: 'only for operator-started cases' }],
    submit: 'Review change', submitIcon: 'check', onSubmit: (v) => { Object.assign(cf, { origins: v.origins, origin: v.origins[0], allow: v.allow, operator_only: !!v.operator_only }); UI.staged(S.stage('credentials', n, cf)); } }); });
  UI.act('login-replace', (b) => UI.form({ caps: 'write-only', title: `Replace ${b.dataset.n}'s password`, fields: [{ k: 'password', type: 'password', req: true, label: 'New password' }], submit: 'Store with passkey', submitIcon: 'passkey', onSubmit: (v, form) => { form.querySelector('input').value = ''; UI.closeModal(); UI.stepup({ title: 'Replace the password', purpose: 'secret write', what: `{ vault: <b>house</b>, key: <b>login/${esc(b.dataset.n)}/password</b> }` }).then((ok) => { if (!ok) return; const s = D.secrets.find((x) => x.ref === `house/login/${b.dataset.n}/password`); if (s) s.set = now(); S.save(); UI.rerender(); UI.toast('Password replaced', 'shown only as set', 'ok'); }); return false; } }));
  UI.act('login-revoke', (b) => UI.confirm({ title: `Revoke ${b.dataset.n}?`, body: 'Stages removing the binding; bots can no longer use it. Stored values for a username & password login are deleted from the vault when it applies.', ok: 'Stage revoke', danger: true }).then((ok) => { if (!ok) return; D.prefs.returnTo = { href: '#/browser/logins', label: 'Logins' }; UI.staged(S.stage('credentials', b.dataset.n, null)); }));
  UI.act('login-revoke-session', (b) => UI.confirm({ title: `Log out of ${b.dataset.site}?`, body: 'Deletes the saved cookie jar now. Immediate, audited.', ok: 'Revoke', danger: true }).then((ok) => { if (!ok) return; D.access.logins = D.access.logins.filter((l) => !(l.site === b.dataset.site && l.identity === b.dataset.id)); S.save(); UI.rerender(); }));
})();
