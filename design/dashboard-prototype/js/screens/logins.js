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
    const steps = ['Method', 'Credentials', 'Binding', 'Review'];
    const at = { method: 0, creds: 1, bind: 2 }[la.step];
    return `<div class="page">${UI.head('Add a login', { crumbs: '<a href="#/browser">Browsers</a> › <a href="#/browser/logins">Logins</a>', sub: 'a site a bot may sign in to, and who may use it' })}
      <div class="apply-steps">${steps.map((x, i) => `<div class="as ${i < at ? 'done' : i === at ? 'cur' : ''}"><b>${i < at ? '✓' : i + 1}</b>${x}</div>`).join('')}</div>
      ${la.step === 'method' ? methodHTML() : la.step === 'creds' ? credsHTML() : bindHTML()}</div>`;
  };
  function methodHTML() {
    const card = (m, ic, title, body, off) => `<button class="panel login-method${la.method === m ? ' on' : ''}" ${off ? UI.off(off) : `data-act="la-method" data-m="${m}"`}><span class="sc-ic">${icon(ic)}</span><b>${title}</b><span class="small muted">${body}</span></button>`;
    return `<div class="grid g3">${card('password', 'key', 'Username &amp; password', 'Type them once; they go to conductor\'s vault, write-only. Optional TOTP.')}
      ${card('vault', 'lock', 'From a vault', '1Password, pass, HashiCorp or conductor\'s vault: pick the item and map its fields. Only the reference is stored.')}
      ${card('takeover', 'hand', 'Sign in yourself', 'A live browser: you sign in, conductor saves the session. Best for SSO, CAPTCHA, SMS or email codes.', phone() ? 'Signing in yourself needs a desktop browser; add by vault reference on the phone' : '')}</div>`;
  }
  function credsHTML() {
    if (la.method === 'password') {
      const ok = totpOk(la.d.totp);
      return `<div class="panel"><div class="panel-body stack" id="la-form">
        <div class="grid g2"><div class="field"><label for="la-user">Username</label><input class="input mono" id="la-user" autocomplete="off" value="${esc(la.d.user || '')}"></div><div class="field"><label for="la-pass">Password <span class="opt">write-only</span></label><input class="input mono" id="la-pass" type="password" autocomplete="new-password"></div></div>
        <div class="field"><label for="la-totp">TOTP secret or otpauth:// URI <span class="opt">optional · write-only</span></label><input class="input mono" id="la-totp" type="password" autocomplete="off" data-on-input="la-totp"><div class="hint" id="la-totp-ok">${ok == null ? 'Conductor generates the codes; you never see the seed again.' : ok ? '<span class="totp-ok">••• ••• · code works ✓</span>' : '<span class="danger-text">not a valid TOTP secret</span>'}</div></div>
        <div class="callout small">${icon('lock')} Stored in conductor's vault (<span class="mono">house</span>) when you apply, behind your passkey. Afterwards the page shows only <b>set</b>, with Replace and Remove.</div>
        <div class="row"><button class="btn ghost" data-act="la-back">Back</button><button class="btn primary" style="margin-left:auto" data-act="la-creds">Next: binding</button></div></div></div>`;
    }
    if (la.method === 'vault') {
      const v = la.d.vault || 'op', items = D.opItems.filter((i) => !la.d.q || (i.title + ' ' + i.website + ' ' + i.vault).toLowerCase().includes(la.d.q.toLowerCase()));
      const it = la.d.item != null ? D.opItems[la.d.item] : null;
      return `<div class="panel"><div class="panel-body stack">
        <div class="field"><label>Vault</label>${UI.seg([['op', '1Password'], ['store', 'pass'], ['hcv', 'HashiCorp Vault'], ['house', 'conductor']], v, 'la-vault')}</div>
        ${v === 'op' ? `<div class="input-icon">${icon('search')}<input class="input" id="la-q" placeholder="search items: title, website, vault" value="${esc(la.d.q || '')}" data-on-input="la-q" aria-label="Search 1Password items"></div>
          <div class="list op-items" id="la-items">${items.map((i) => { const ix = D.opItems.indexOf(i); return `<button class="item click${la.d.item === ix ? ' on' : ''}" data-act="la-item" data-i="${ix}"><span class="sc-ic sm">${icon('key')}</span><div class="grow"><b>${esc(i.title)}</b> <span class="mono tiny muted">${esc(i.vault)}</span><div class="tiny muted">${esc(i.website)} · fields: ${i.fields.map(([l]) => esc(l)).join(', ')}</div></div></button>`; }).join('') || '<div class="item small muted">No item matches.</div>'}</div>
          <div class="muted tiny">Titles, websites and field labels only: conductor never reads a value to show it here.</div>`
          : `<div class="field"><label for="la-path">Item path</label><input class="input mono" id="la-path" value="${esc(la.d.path || '')}" placeholder="${v === 'hcv' ? 'kv/data/app' : 'conductor/app'}"></div>`}
        ${it || v !== 'op' ? `<div class="caps">Map fields</div>${['username', 'password', 'otp'].map((role) => { const opts = it ? it.fields.map(([l]) => l) : ['username', 'password', 'otp']; const def = la.d.map && la.d.map[role] != null ? la.d.map[role] : it ? (it.fields.find(([, t]) => (role === 'username' ? /text|email/ : role === 'password' ? /concealed/ : /otp/).test(t)) || [''])[0] : role === 'otp' ? '' : role; return `<div class="flt-row"><span class="mono small" style="min-width:140px">${role === 'otp' ? 'one-time password' : role}</span><select class="select mono" id="la-map-${role}" aria-label="Map ${role}"><option value="">— none —</option>${opts.map((o) => `<option${o === def ? ' selected' : ''}>${esc(o)}</option>`).join('')}</select><span class="mono tiny muted">${def ? esc(ref(v, it, def)) : ''}</span></div>`; }).join('')}` : ''}
        <div class="row"><button class="btn ghost" data-act="la-back">Back</button><button class="btn primary" style="margin-left:auto" ${it || v !== 'op' ? 'data-act="la-creds"' : UI.off('Pick an item first')}>Next: binding</button></div></div></div>`;
    }
    return '';
  }
  const ref = (v, it, field) => (v === 'op' ? `op://${it.vault}/${it.title}/${field}` : `${v}/${la.d.path || 'item'}/${field}`);
  function bindHTML() {
    const name = la.d.name || (la.d.origin || '').replace(/^https:\/\//, '').split('.')[0] || '';
    return `<div class="panel"><div class="panel-body stack" id="la-bind">
      <div class="callout small">Method: <b>${{ password: 'username & password (conductor vault)', vault: 'vault reference', session: 'saved session (signed in yourself)' }[la.method]}</b>${la.method === 'vault' ? ` · <span class="mono">${esc(Object.values(la.d.refs || {}).join(', '))}</span>` : ''}</div>
      ${UI.fieldHTML({ k: 'name', type: 'string', req: true, label: 'Login name', ph: 'grafana' }, name)}
      ${UI.fieldHTML({ k: 'origins', type: 'list', req: true, label: 'Origins (exact scheme + host)', ph: 'https://grafana.example.com', help: 'The login is filled only on these origins, checked atomically with the fill.' }, la.d.origin ? [la.d.origin] : [])}
      ${UI.fieldHTML({ k: 'allow', type: 'select-multi', req: true, label: 'Who may use it', options: allowOpts(), create: 'bot', createLabel: 'New bot', help: 'Bots, teams or folders. A team or folder covers the bots in it.' }, [])}
      ${UI.fieldHTML({ k: 'operator_only', type: 'bool', label: 'Provenance', on: 'only for cases an operator started (recommended, as in #182)' }, true)}
      <div class="form-err callout danger small hide" id="la-err"></div>
      <div class="row"><button class="btn ghost" data-act="la-back">Back</button><button class="btn primary" style="margin-left:auto" data-act="la-save">${icon('check')}Review &amp; apply</button></div></div></div>`;
  }
  UI.act('la-method', (b) => { la.method = b.dataset.m; la.d = {}; if (la.method === 'takeover') { UI.go('#/browser/takeover'); return; } la.step = 'creds'; UI.rerender(); });
  UI.act('la-back', () => { la.step = la.step === 'bind' && la.method !== 'session' ? 'creds' : 'method'; UI.rerender(); });
  UI.act('la-totp', (el) => { const ok = totpOk(el.value); UI.$('#la-totp-ok').innerHTML = ok == null ? 'Conductor generates the codes; you never see the seed again.' : ok ? '<span class="totp-ok">••• ••• · code works ✓</span>' : '<span class="danger-text">not a valid TOTP secret</span>'; });
  UI.act('la-vault', (b) => { la.d.vault = b.dataset.v; la.d.item = null; UI.rerender(); });
  UI.act('la-q', (el) => { la.d.q = el.value; const pos = el.selectionStart; UI.rerender(); const n = UI.$('#la-q'); if (n) { n.focus(); n.setSelectionRange(pos, pos); } });
  UI.act('la-item', (b) => { la.d.item = +b.dataset.i; la.d.map = null; UI.rerender(); });
  UI.act('la-creds', () => {
    if (la.method === 'password') {
      const u = UI.$('#la-user').value.trim(), p = UI.$('#la-pass').value, t = UI.$('#la-totp').value.trim();
      if (!u) return UI.nudge(UI.$('#la-user'), 'a username'); if (!p) return UI.nudge(UI.$('#la-pass'), 'a password');
      if (t && !totpOk(t)) return UI.nudge(UI.$('#la-totp'));
      la.d.user = u; la.secret = { password: p, totp: t }; // held in memory only until the vault write
      UI.$('#la-pass').value = ''; UI.$('#la-totp').value = '';
    } else {
      const v = la.d.vault || 'op', it = la.d.item != null ? D.opItems[la.d.item] : null;
      la.d.refs = {}; ['username', 'password', 'otp'].forEach((r) => { const f = UI.$('#la-map-' + r).value; if (f) la.d.refs[r] = ref(v, it, f); });
      if (!la.d.refs.password && !la.d.refs.username) return UI.toast('Map at least one field', '', 'fail');
      if (it) la.d.origin = it.website.replace(/\/$/, '');
    }
    la.step = 'bind'; UI.rerender();
  });
  UI.act('la-save', () => {
    const f = UI.$('#la-bind'), fields = [{ k: 'name', type: 'string', req: true, pattern: /^[a-z][\w-]*$/, patternMsg: 'letters, digits, - and _', validate: (v) => (D.access.credentials.some((c) => c.name === v) ? 'A login by that name exists.' : '') }, { k: 'origins', type: 'list', req: true, validate: (v) => ((v || []).find((o) => !ORIGIN.test(o)) ? 'Each origin is https:// plus a host, no path.' : '') }, { k: 'allow', type: 'select-multi', req: true }, { k: 'operator_only', type: 'bool' }];
    const v = UI.formValues(f, fields); if (!UI.checkFields(f, fields, v)) return;
    const base = { method: la.method, origin: v.origins[0], origins: v.origins, allow: v.allow, operator_only: !!v.operator_only };
    const finish = (n) => { UI.staged(S.stage('credentials', v.name, Object.assign(base, n))); D.prefs.returnTo = { href: '#/browser/logins', label: 'Logins' }; la.step = 'method'; la.method = null; la.d = {}; S.save(); };
    if (la.method === 'password') {
      const sec = la.secret || {};
      UI.stepup({ title: `Store the login for ${esc(v.origins[0])}`, purpose: 'secret write', ok: 'Store with passkey', what: `{ purpose: <b>secret_write</b>, vault: <b>house</b>, keys: <b>login/${esc(v.name)}/{username,password${sec.totp ? ',otp' : ''}}</b> }` }).then((ok) => {
        if (!ok) return;
        ['username', 'password'].concat(sec.totp ? ['otp'] : []).forEach((k) => D.secrets.unshift({ name: `login ${v.name} ${k}`, ref: `house/login/${v.name}/${k}`, vault: 'house', set: now(), used: [`browser.credentials.${v.name}.${k}`] }));
        la.secret = null;
        finish({ vault: 'house', ref: `login/${v.name}/*`, fields: ['username', 'password'].concat(sec.totp ? ['otp'] : []), username: la.d.user });
      });
    } else if (la.method === 'vault') finish({ vault: (la.d.vault || 'op'), ref: Object.values(la.d.refs)[0], refs: la.d.refs, fields: Object.keys(la.d.refs) });
    else finish({ fields: [] });
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
          <div class="tiny">${method === 'password' ? `<span class="mono">username ${esc(cf.username || '')}</span> · password <b>set</b>${(cf.fields || []).includes('otp') ? ' · TOTP <b>set</b>' : ''}` : method === 'vault' ? `<span class="mono">${esc(Object.values(cf.refs || { r: `${cf.vault}:${cf.ref}` }).join(' · '))}</span>` : method === 'passkey' ? `private key in <span class="mono">${esc(cf.vault)}</span>` : 'session cookies'} · last used ${m.lastUsed ? ago(m.lastUsed) + ' ago' : 'never'} · ${testTag(m.test)}</div>
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
      const res = /app\.example\.com/.test(cf.origin) ? 'needs you' : cf.username === 'wrong' ? 'failed' : 'ok';
      m.test = { result: res, at: now() }; S.save();
      const body = UI.$('#lt-body', s); if (!body) return;
      body.innerHTML = res === 'ok' ? `${st('ok', 'signed in')}<div class="muted tiny">origin ✓ · frame ✓ · fields ✓ · logged out again</div>` : res === 'failed' ? `${st('fail', 'sign-in refused')}<div class="muted tiny">the site rejected the credentials · nothing was saved</div>` : `${st('wait', 'needs you: approve the push on your phone')}<div class="row" style="margin-top:8px"><button class="btn sm primary" id="lt-approved">I approved it</button></div>`;
      const ap = UI.$('#lt-approved', s); if (ap) ap.addEventListener('click', () => { m.test = { result: 'ok', at: now() }; S.save(); body.innerHTML = `${st('ok', 'signed in after your approval')}`; });
      UI.rerender();
    }, UI.fast ? 40 : 900);
  });
  UI.act('login-bind', (b) => { const n = b.dataset.n, cf = JSON.parse(JSON.stringify(S.staged('credentials', n))); UI.form({ caps: `browser.credentials.${n}`, title: `Binding for ${n}`, edit: true, values: { origins: cf.origins || [cf.origin], allow: cf.allow || (cf.identity ? [cf.identity] : []), operator_only: cf.operator_only !== false },
    fields: [{ k: 'origins', type: 'list', req: true, label: 'Origins', validate: (v) => ((v || []).find((o) => !ORIGIN.test(o)) ? 'https:// plus a host, no path' : '') }, { k: 'allow', type: 'select-multi', req: true, label: 'Who may use it', options: allowOpts(), create: 'bot', createLabel: 'New bot' }, { k: 'operator_only', type: 'bool', label: 'Provenance', on: 'only for operator-started cases' }],
    submit: 'Review change', submitIcon: 'check', onSubmit: (v) => { Object.assign(cf, { origins: v.origins, origin: v.origins[0], allow: v.allow, operator_only: !!v.operator_only }); UI.staged(S.stage('credentials', n, cf)); } }); });
  UI.act('login-replace', (b) => UI.form({ caps: 'write-only', title: `Replace ${b.dataset.n}'s password`, fields: [{ k: 'password', type: 'string', req: true, label: 'New password', mono: false }], submit: 'Store with passkey', submitIcon: 'passkey', onSubmit: (v, form) => { form.querySelector('input').value = ''; UI.closeModal(); UI.stepup({ title: 'Replace the password', purpose: 'secret write', what: `{ vault: <b>house</b>, key: <b>login/${esc(b.dataset.n)}/password</b> }` }).then((ok) => { if (!ok) return; const s = D.secrets.find((x) => x.ref === `house/login/${b.dataset.n}/password`); if (s) s.set = now(); S.save(); UI.rerender(); UI.toast('Password replaced', 'shown only as set', 'ok'); }); return false; } }));
  UI.act('login-revoke', (b) => UI.confirm({ title: `Revoke ${b.dataset.n}?`, body: 'Stages removing the binding; bots can no longer use it. Stored values for a username & password login are deleted from the vault when it applies.', ok: 'Stage revoke', danger: true }).then((ok) => { if (!ok) return; D.prefs.returnTo = { href: '#/browser/logins', label: 'Logins' }; UI.staged(S.stage('credentials', b.dataset.n, null)); }));
  UI.act('login-revoke-session', (b) => UI.confirm({ title: `Log out of ${b.dataset.site}?`, body: 'Deletes the saved cookie jar now. Immediate, audited.', ok: 'Revoke', danger: true }).then((ok) => { if (!ok) return; D.access.logins = D.access.logins.filter((l) => !(l.site === b.dataset.site && l.identity === b.dataset.id)); S.save(); UI.rerender(); }));
})();
