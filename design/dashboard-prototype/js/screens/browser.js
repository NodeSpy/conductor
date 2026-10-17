/* Agent browsers (#182): live view, takeover, seeding a login, passkey registration, per-identity settings. */
(function () {
  const { icon, st, tag, ago, esc } = UI;

  const spark = (pts, color) => `<svg viewBox="0 0 200 80" preserveAspectRatio="none"><polyline fill="none" stroke="${color}" stroke-width="1.6" points="${pts.map((v, i) => `${(i / (pts.length - 1)) * 200},${80 - v}`).join(' ')}"/></svg>`;
  // fake third-party pages inside the screencast: drawn by the .site classes (screens.css) with tokens, never raw colours
  const SITES = {
    'b-01': () => `<div class="site" data-theme="light"><div class="top dark"><b>◢ Grafana</b><span>Dashboards › API latency</span><span class="dim-on" style="margin-left:auto">Last 1 hour</span></div><div class="grafana" data-theme="dark">
      <div class="gp"><div>p99 latency by route</div>${spark([20, 22, 21, 25, 24, 52, 61, 66, 63, 64, 58, 22, 20], 'var(--color-dataviz-categorical-5)')}</div>
      <div class="gp"><div>requests / s</div>${spark([40, 42, 44, 41, 45, 47, 46, 48, 45, 44, 46, 47, 45], 'var(--color-dataviz-categorical-7)')}</div>
      <div class="gp"><div>web instances by AMI</div>${spark([30, 30, 30, 30, 50, 50, 50, 50, 50, 50, 50, 30, 30], 'var(--color-dataviz-categorical-4)')}</div>
      <div class="gp"><div>5xx rate</div>${spark([5, 5, 6, 5, 9, 14, 12, 13, 11, 10, 9, 6, 5], 'var(--color-dataviz-categorical-3)')}</div></div></div>`,
    'b-02': () => `<div class="site" data-theme="light"><div class="top"><b>App</b><span class="dim-on">Sign in</span></div><div style="flex:1;display:grid"><div class="card"><div class="phone-ill">📱</div><h4>Approve the sign-in on your phone</h4><p>We sent a push notification to the device registered to <b>agent-triage@acme.example</b>. This page continues once it's approved.</p><div class="site-prog"><i></i></div><p class="site-small" style="margin:14px 0 0">Password <span class="masked">••••••••••••</span> accepted</p></div></div></div>`,
    'b-03': () => `<div class="site plain" data-theme="light"><div class="top light"><b>Docs</b><span>API</span><span>Guides</span></div><div class="site-doc"><div class="site-small">API › Refunds</div><h2>Create a refund</h2><p>Refund fees are rounded <b>half-even</b> to the nearest cent. A partial refund keeps the original fee schedule.</p><pre>POST /v1/refunds { "charge": "ch_123", "amount": 2000 }</pre></div></div>`,
    seed: () => `<div class="site" data-theme="light"><div class="top"><b>Console</b></div><div style="flex:1;display:grid"><div class="card" style="text-align:left"><h4>Sign in to Console</h4><p>Use your work account.</p><div class="site-field">ops-agent@acme.example</div><div class="site-field focus">Password<span class="cursor-blink"></span></div><span class="sbtn">Continue</span><p class="site-small" style="margin:12px 0 0">or <u>Sign in with a passkey</u></p></div></div></div>`,
  };

  /* ---------- list ---------- */
  UI.route('browser', () => `<div class="page">
    ${UI.head('Browsers', { lede: (() => { const w = D.browsers.filter((b) => b.status === 'wait').length, r = D.browsers.filter((b) => b.status === 'run').length; return `${w ? `<b>${w} ${w === 1 ? 'browser is' : 'browsers are'} waiting on you.</b> ` : '<b>Nothing needs you.</b> '}${D.browsers.length} open, ${r} in use by an agent right now.`; })(), lede2: 'conductor’s Chromium: one per agent session, sandboxed.', actions: `${UI.folderChip()}<a class="btn" href="#/browser/add">${icon('key')}Add a login</a><a class="btn" href="#/browser/passkey">${icon('plus')}Register a passkey</a>` })}
    ${UI.tabs([['sessions', 'Sessions', '#/browser', D.browsers.length], ['logins', 'Logins', '#/browser/logins', D.access.credentials.length + D.access.logins.length]], 'sessions')}
    ${UI.scopeNote(D.browsers.filter((b) => !UI.scopeKeeps(b.identity.startsWith('bot:') ? b.identity.slice(4) : null)).length)}
    <div class="grid g3">${D.browsers.filter((b) => UI.scopeKeeps(b.identity.startsWith('bot:') ? b.identity.slice(4) : null)).map((b) => `
      <a class="panel btile" href="#/browser/${b.id}">
        <div class="thumb"><div class="mini" style="height:238%">${SITES[b.id]()}</div>${b.status === 'wait' ? `<span class="ctrl-banner wait">${icon('hold')}Waiting on you</span>` : ''}</div>
        <div class="panel-body stack tight">
          <div class="row">${st(b.status, b.status === 'run' ? 'agent acting' : b.status === 'wait' ? 'needs you' : 'idle')}<span class="mono tiny muted" style="margin-left:auto">${b.id} · ${b.actions} actions</span></div>
          <div class="ellipsis" style="font-weight:600">${esc(b.title)}</div>
          <div class="mono tiny muted ellipsis">${b.url}</div>
          <div class="row wrap" style="gap:5px">${tag(b.identity)}${b.loggedIn ? tag(icon('cookie') + ' logged in', 'wait') : tag('anonymous', 'ok')}</div>
        </div></a>`).join('')}
    </div>
    <div class="section-title"><h2>Per identity</h2><span class="caps">browser.* in config</span></div>
    <div class="panel"><table class="tbl responsive"><thead><tr><th>identity</th><th>allowlist</th><th>logged-in sites</th><th>credentials</th><th>evaluate</th><th></th></tr></thead><tbody>
      ${D.identities.filter((i) => UI.scopeKeeps(i.id.startsWith('bot:') ? i.id.slice(4) : null)).map((i) => { const c = S.staged('identities', i.id) || {}; const creds = D.access.credentials.filter((x) => x.identity === i.id).map((x) => `${x.name} (${x.fields})`).join(' · ') || '—'; return `<tr><td data-l="identity" class="mono">${esc(i.id)} ${S.find('identities', i.id) ? tag('staged', 'run') : ''}</td><td data-l="allow" class="mono small">${(c.allow || []).join(', ') || '—'}</td><td data-l="logins" class="mono small">${(c.logins || []).join(', ') || '—'}</td><td data-l="credentials" class="small">${esc(creds)}</td><td data-l="evaluate">${tag(c.evaluate || 'off', (c.evaluate || 'off') === 'off' ? 'ok' : 'wait')}</td><td class="num"><button class="btn sm" data-act="ident-edit" data-i="${esc(i.id)}">${icon('edit')}Edit</button></td></tr>`; }).join('')}
    </tbody></table><div class="panel-foot small muted"><button class="btn sm" data-act="ident-edit" data-i="">${icon('plus')}Add identity</button><span style="margin-left:8px">A <span class="mono">"*"</span> allowlist is refused. Edits go through the config pipeline; anything that widens (a site, a login, a credential, evaluate) also asks for your passkey.</span></div></div>
  </div>`);

  /* ---------- live view ---------- */
  function live(b) {
    return `<div class="page wide">
      ${UI.head(esc(b.title), { gen: true, crumbs: `<a href="#/browser">Browsers</a> ${icon('chev')} ${esc(b.title)}`,
        actions: `${st(b.status, b.status === 'run' ? 'agent acting' : b.status === 'wait' ? 'waiting on you' : 'idle', true)}<button class="btn" data-act="b-peek" data-b="${b.id}">${icon('download')}Screenshot</button><button class="btn${bv.lease === b.id ? '' : ' primary'}" data-act="b-take" data-b="${b.id}" id="take">${icon(bv.lease === b.id ? 'undo' : 'hand')}${bv.lease === b.id ? 'Release' : 'Take over'}</button><button class="btn secondary" data-act="b-close" data-b="${b.id}">${icon('x')}Close browser</button>` })}
      ${UI.metaLine([{ obj: 'browser', label: 'Browser', value: b.id, mono: true }, { obj: 'run', label: 'Used by run', value: `<a class="mono" href="#/runs/${b.agent.startsWith('r-') ? b.agent : 'r-7f29'}">${b.agent}</a>` }, { obj: 'login', label: 'Signs in as', value: esc(b.identity), mono: true }, { label: 'Asked for by', value: esc(b.requester), mono: true }])}
      <div class="grid" style="grid-template-columns:minmax(0,1fr) 340px;align-items:start">
        <div class="panel">
          <div class="bchrome">${['back', 'fwd', 'reload'].map((k) => `<button class="icon-btn${k === 'back' ? ' flip' : ''}" ${bv.lease === b.id ? (k === 'fwd' && !(bv.fwd || []).length ? UI.off('Nothing to go forward to') : k === 'back' && !(bv.hist || []).length ? UI.off('Nothing to go back to') : `data-act="b-nav" data-k="${k}" data-b="${b.id}"`) : UI.off('The agent is driving: take over to use the browser yourself')} aria-label="${k}">${icon(k === 'reload' ? 'updates' : 'chev')}</button>`).join('')}
            <div class="url">${icon('lock', 'lockic')}<span class="ellipsis">${esc(b.url)}</span></div>
            ${UI.label(`${b.tabs} tab${b.tabs > 1 ? 's' : ''}`)}<span class="row tiny mono muted"><span class="rec"></span>CDP screencast · 12 fps</span></div>
          <div class="bscreen${bv.lease === b.id ? ' takeover' : ''}" id="screen" data-volatile>${bv.lease === b.id ? `<div class="ctrl-banner" id="banner">${icon('hand')}You have control · agent paused<span class="kbd on-accent">Esc</span> to release</div>` : ''}${(SITES[b.id] || SITES['b-03'])()}
            <svg class="fake-cursor" id="cur" style="left:62%;top:44%;${bv.lease === b.id ? 'display:none' : ''}" viewBox="0 0 24 24"><path d="M4 2l16 9-7 2-3 7z" fill="var(--color-text-primary)" stroke="var(--color-bg-surface)" stroke-width="1.5"/></svg>
          </div>
          <div class="panel-foot small"><span class="muted">${icon('shield')} jailed Chromium · CDP over a pipe (no debugging port) · egress via the allowlist proxy only</span><span style="margin-left:auto" class="mono tiny muted">${b.profile}</span></div>
        </div>
        <div class="stack">
          <div class="panel"><div class="panel-head"><h3>Session</h3></div><div class="panel-body"><dl class="kv small">
            <dt>profile</dt><dd>${b.loggedIn ? tag('logged-in browser · narrowed to its sites', 'wait') : tag('anonymous · ephemeral', 'ok')}</dd>
            <dt>evaluate</dt><dd>${tag(b.loggedIn ? 'off (a credential was filled)' : 'off', 'ok')}</dd></dl>
            ${b.note ? `<div class="callout ${b.status === 'wait' ? 'warn' : 'info'} small" style="margin-top:10px">${esc(b.note)}</div>` : ''}</div></div>
          <div class="panel"><div class="panel-head"><h3>Actions</h3><span class="muted small">audited · values never recorded</span></div><div class="list">${(D.browserLogs = D.browserLogs || {}, D.browserLogs[b.id] = D.browserLogs[b.id] || (b.id === 'b-02' ? D.browserLog.slice() : D.browserLog.slice(0, 3))).map(([a, d, s]) => `<div class="item small" style="align-items:flex-start">${st(s, '')}<div class="grow"><span class="mono" style="font-weight:500">${a}</span><div class="muted tiny mono">${esc(d)}</div></div></div>`).join('')}</div></div>
        </div>
      </div></div>`;
  }
  const bv = { lease: null, hist: [], fwd: [] };
  function bindLive(main) {
    const cur = UI.$('#cur', main);
    if (cur && bv.lease == null) UI.every(1800, () => { cur.style.left = 30 + Math.random() * 50 + '%'; cur.style.top = 25 + Math.random() * 50 + '%'; });
  }
  const blog = (id, row) => { D.browserLogs = D.browserLogs || {}; (D.browserLogs[id] = D.browserLogs[id] || []).push(row); S.save(); };
  UI.act('b-peek', (b) => { blog(b.dataset.b, ['peek (operator)', 'masked screenshot · credential fields hidden', 'ok']); D.audit.unshift({ ts: new Date().toISOString(), event: 'browser_peek', who: 'operator:ops', detail: b.dataset.b, run: '' }); UI.rerender(); UI.toast('Screenshot saved', 'credential fields masked · audited as a peek', 'ok'); });
  UI.act('b-take', (btn) => {
    const id = btn.dataset.b;
    if (bv.lease === id) { bv.lease = null; blog(id, ['release', 'lease returned · agent resumes with a fresh snapshot', 'ok']); UI.rerender(); UI.toast('Released to the agent', 'it resumes with a fresh snapshot', 'ok'); return; }
    UI.stepup({ title: 'Drive this browser yourself', purpose: 'browser takeover', ok: 'Take over with passkey', what: `{ purpose: <b>browser_takeover</b>, browser: <b>${id}</b>, lease: <b>until you release</b> } · keys audited as a length only; cookies kept only for this browser's logged-in sites` }).then((ok) => { if (!ok) return; bv.lease = id; bv.hist = []; bv.fwd = []; blog(id, ['takeover', 'operator lease taken · agent actions wait', 'wait']); D.audit.unshift({ ts: new Date().toISOString(), event: 'browser_takeover', who: 'operator:ops', detail: id, run: '' }); S.save(); UI.rerender(); UI.toast('You have control', 'agent actions queue until you release', 'ember'); });
  });
  UI.act('b-nav', (btn) => { const b = D.browsers.find((x) => x.id === btn.dataset.b); const k = btn.dataset.k; if (k === 'reload') blog(b.id, ['reload (operator)', b.url, 'ok']); else if (k === 'back') { bv.fwd.push(b.url); b.url = bv.hist.pop(); blog(b.id, ['back (operator)', b.url, 'ok']); } else { bv.hist.push(b.url); b.url = bv.fwd.pop(); blog(b.id, ['forward (operator)', b.url, 'ok']); } S.save(); UI.rerender(); });
  document.addEventListener('click', (e) => { if (bv.lease && e.target.closest('#screen.takeover') && !e.target.closest('#banner')) { const b = D.browsers.find((x) => x.id === bv.lease); if (!b) return; const r = e.target.closest('#screen').getBoundingClientRect(); bv.hist.push(b.url); bv.fwd = []; const nu = b.url.split('?')[0] + '?op=' + Math.round(((e.clientX - r.left) / r.width) * 100); b.url = nu; blog(b.id, ['click (operator)', `at ${Math.round(e.clientX - r.left)},${Math.round(e.clientY - r.top)} · Input.dispatchMouseEvent`, 'ok']); UI.rerender(); } });
  UI.act('b-close', (btn) => UI.confirm({ title: `Close browser ${btn.dataset.b}?`, body: 'Ends the browser session; the agent\'s next browser call opens a fresh one. Its ephemeral profile is discarded.', ok: 'Close browser', danger: true }).then((ok) => { if (!ok) return; D.browsers = D.browsers.filter((x) => x.id !== btn.dataset.b); D.attention = D.attention.filter((a) => !a.href.includes(btn.dataset.b)); if (bv.lease === btn.dataset.b) bv.lease = null; D.audit.unshift({ ts: new Date().toISOString(), event: 'browser_close', who: 'operator:ops', detail: btn.dataset.b, run: '' }); S.save(); UI.refreshChrome(); UI.go('#/browser'); }));
  UI.act('ident-edit', (b) => {
    const id = b.dataset.i, cur = id ? S.staged('identities', id) : { allow: [], logins: [], evaluate: 'off' };
    UI.form({ caps: id ? `browser.identities["${id}"]` : 'browser.identities', title: id ? `Browser settings · ${id}` : 'Add a browser identity', edit: !!id, values: Object.assign({ id }, cur),
      fields: [{ k: 'id', type: 'string', req: true, readonly: !!id, label: 'Identity', ph: 'agent:docs@acme/api', pattern: /^(bot:|agent:)[\w@./-]+$/, patternMsg: 'bot:<name> or agent:<name>@<repo>' }, { k: 'allow', type: 'list', label: 'Allowlist (sites it may reach)', ph: 'docs.example.com', help: 'A "*" allowlist is refused. Adding a site widens.' }, { k: 'logins', type: 'list', label: 'Logged-in sites', ph: 'console.example.com', help: 'Must also be on the allowlist. Seed the login after applying.' }, { k: 'evaluate', type: 'seg', options: [['off', 'off'], ['read-only', 'read-only'], ['on', 'on (widens)']], def: 'off', help: 'Page JavaScript the agent may run. Never on once a credential is filled.' }],
      submit: 'Review change', submitIcon: 'check', onSubmit: (v) => UI.staged(S.stage('identities', v.id || id, { allow: v.allow || [], logins: v.logins || [], evaluate: v.evaluate || 'off' })) });
  });
  /* ---------- seed a login / register a passkey ---------- */
  const sd = { step: 0, kind: null, identity: '', site: '', cred: '' };
  function seed(passkey) {
    const kind = passkey ? 'passkey' : 'login';
    if (sd.kind !== kind) Object.assign(sd, { step: 0, kind, identity: '', site: '', cred: '' });
    const steps = passkey ? ['Choose identity & credential', 'Arm a virtual authenticator', 'Add a passkey on the site', 'Saved to vault'] : ['Choose identity & site', 'Sign in yourself', 'Keep only this site\'s cookies', 'Saved'];
    const ids = S.view().identities.map((x) => x.id);
    const ident = sd.identity || ids[0];
    const sites = (S.staged('identities', ident) || {}).logins || [];
    const creds = S.view().access.credentials.filter((c) => c.identity === ident && /passkey/.test(c.fields || (c.cfg && c.cfg.fields || []).join(',')));
    const site = passkey ? ((creds.find((c) => c.name === sd.cred) || creds[0] || {}).origin || '').replace('https://', '') : sd.site || sites[0] || '';
    const k = sd.step;
    const left = k === 0 ? `<div class="empty"><span class="serif">${passkey ? 'Pick who gets the passkey' : 'Pick the identity and site'}</span>A headless, jailed browser opens in takeover mode, so this works from your phone over the tailnet.</div>`
      : k >= 3 ? `<div class="empty"><span class="serif">${passkey ? 'Passkey stored' : 'Login saved'}</span>${passkey ? `Private key written to house/passkeys/${esc(site.split('.')[1] || site)} · never shown here` : `${esc(site)}: 6 cookies kept, 41 discarded`}<a class="btn" href="#/browser">Back to browsers</a></div>`
      : `<div class="bchrome"><div class="url">${icon('lock', 'lockic')}https://${esc(site)}/${passkey ? 'settings/security' : 'login'}</div><span class="ctrl-banner" style="position:static;transform:none;box-shadow:none;font-size:11px">${icon('hand')}You are driving</span></div><div class="bscreen takeover">${SITES.seed()}</div>`;
    const right = k === 0 ? `<div class="field"><label for="sd-id">Identity</label><select class="select mono" id="sd-id" data-on-change="sd-id" data-picker="identity" data-ret="name"><option value="__new__">+ New browser identity…</option>${ids.map((x) => `<option${x === ident ? ' selected' : ''}>${esc(x)}</option>`).join('')}</select></div>
        ${passkey ? `<div class="field"><label for="sd-cred">Credential</label><select class="select mono" id="sd-cred" data-on-change="sd-cred" data-picker="credential" data-ctx='${esc(JSON.stringify({ identity: ident }))}' data-ret="name"><option value="__new__">+ New credential binding…</option>${creds.map((c) => `<option value="${c.name}"${c.name === sd.cred ? ' selected' : ''}>${c.name} · ${c.origin}</option>`).join('') || '<option value="">no passkey credential bound to this identity</option>'}</select><div class="hint">Bind one with a <span class="mono">passkey</span> field first (Access → Credential bindings).</div></div>`
          : `<div class="field"><label for="sd-site">Site</label><select class="select mono" id="sd-site" data-on-change="sd-site" data-picker="site" data-ctx='${esc(JSON.stringify({ identity: ident }))}' data-ret="name"><option value="__new__">+ Add a site to ${esc(ident)}…</option>${sites.map((x) => `<option${x === site ? ' selected' : ''}>${esc(x)}</option>`).join('') || '<option value="">no logged-in sites for this identity</option>'}</select><div class="hint">Only sites in this identity's <span class="mono">browser.logins</span> can be seeded. <button class="link" data-act="ident-edit" data-i="${esc(ident)}">Add a site</button></div></div>`}
        <div class="row"><a class="btn ghost" href="#/browser">Cancel</a><button class="btn primary" style="margin-left:auto" ${site ? 'data-act="sd-next"' : UI.off(passkey ? 'Bind a passkey credential to this identity first' : 'Add a logged-in site to this identity first')}>${icon('hand')}Open the browser</button></div>`
      : k === 1 ? `<div class="callout ember small">${passkey ? `An empty virtual authenticator is armed on this tab only. Add a passkey on the site's security page.` : `Sign in to <b>${esc(site)}</b> yourself. Keys you type are audited as a length only.`}</div><div class="row"><button class="btn ghost" data-act="sd-cancel">Cancel</button><button class="btn primary" style="margin-left:auto" data-act="sd-next">${icon('check')}${passkey ? 'I\'ve added the passkey' : 'I\'ve signed in'}</button></div>`
      : k === 2 ? `<div class="callout small">${passkey ? `Conductor takes the one credential whose RP covers <span class="mono">${esc(site)}</span> and writes it to the vault.` : `Conductor keeps only <b>${esc(site)}</b>'s cookies, in <span class="mono">&lt;state&gt;/browser/logins/</span> (0600), and only for <b>your own</b> requests to ${esc(ident)}.`}</div><div class="row"><button class="btn ghost" data-act="sd-cancel">Discard</button><button class="btn primary" style="margin-left:auto" data-act="sd-save">${icon('check')}${passkey ? 'Save passkey' : 'Save session'}</button></div>`
      : `<div class="row"><button class="btn" data-act="sd-again">${icon('plus')}Seed another</button><a class="btn ghost" href="#/bots/${ident.replace('bot:', '')}/access">See it in Access</a></div>`;
    return `<div class="page wide">${UI.head(passkey ? 'Register a passkey' : 'Sign in yourself', { crumbs: `<a href="#/browser">Browsers</a> › <a href="#/browser/add">Add a login</a>`, sub: passkey ? 'a dedicated, separately revocable passkey per identity and site' : 'a headless takeover, from any device on your tailnet' })}
      ${UI.stepper(steps, k, false, { states: steps.map((x, i) => (i < k || k >= 3 ? 'done' : i === k ? 'cur' : '')) })}
      <div class="grid seed-grid"><div class="panel">${left}</div><div class="stack"><div class="panel"><div class="panel-body stack">${right}</div></div>
        <div class="panel"><div class="panel-body small muted">Or import one domain's cookies: <span class="mono">conductor browser cookies import</span>. A whole browser profile is never imported.</div></div></div></div></div>`;
  }
  UI.act('sd-id', (el) => { sd.identity = el.value; sd.site = ''; sd.cred = ''; UI.rerender(); });
  UI.act('sd-site', (el) => { sd.site = el.value; UI.rerender(); });
  UI.act('sd-cred', (el) => { sd.cred = el.value; UI.rerender(); });
  UI.act('sd-next', () => { if (!sd.identity) sd.identity = D.identities[0].id; if (!sd.site) sd.site = ((S.staged('identities', sd.identity) || {}).logins || [])[0] || ''; sd.step++; UI.rerender(); });
  UI.act('sd-cancel', () => { sd.step = 0; UI.rerender(); });
  UI.act('sd-again', () => { sd.step = 0; UI.rerender(); });
  UI.act('sd-save', () => {
    const passkey = sd.kind === 'passkey', ident = sd.identity || D.identities[0].id;
    UI.stepup({ title: passkey ? 'Store the passkey' : `Save the ${sd.site} login`, purpose: passkey ? 'passkey register' : 'login seed', ok: 'Save with passkey', what: passkey ? `{ purpose: <b>browser_passkey_save</b>, identity: <b>${esc(ident)}</b>, credential: <b>${esc(sd.cred || 'statuspage')}</b> }` : `{ purpose: <b>browser_seed_save</b>, identity: <b>${esc(ident)}</b>, site: <b>${esc(sd.site)}</b>, cookies: <b>6</b> }` }).then((ok) => {
      if (!ok) return;
      if (passkey) { const name = (sd.cred || 'statuspage') + ' passkey'; const s = D.secrets.find((x) => x.name === name); if (s) s.set = new Date().toISOString(); else D.secrets.push({ name, ref: `house/passkeys/${sd.cred || 'statuspage'}`, vault: 'house', set: new Date().toISOString(), used: [`browser.credentials.${sd.cred || 'statuspage'}.passkey`] }); }
      else { D.access.logins = D.access.logins.filter((l) => !(l.site === sd.site && l.identity === ident)); D.access.logins.push({ site: sd.site, identity: ident, seeded: new Date().toISOString(), cookies: 6 }); }
      D.audit.unshift({ ts: new Date().toISOString(), event: passkey ? 'browser_passkey_register' : 'browser_seed', who: 'operator:ops', detail: `${ident} · ${passkey ? sd.cred : sd.site}`, run: '' });
      sd.step = 3; S.save(); UI.rerender();
    });
  });

  UI.route('browser/:id', ({ id }) => {
    if (id === 'seed') { setTimeout(() => UI.go('#/browser/takeover')); return ''; }
    if (id === 'add') return UI.addLoginPage();
    if (id === 'logins') return UI.loginsPage();
    if (id === 'takeover') return seed(false);
    if (id === 'passkey') return seed(true);
    const b = D.browsers.find((x) => x.id === id);
    if (!b) return `<div class="page"><div class="empty"><span class="serif">That browser has closed</span><a class="btn" href="#/browser">Browsers</a></div></div>`;
    return { toString: () => live(b), after: bindLive };
  }, { nav: 'browser' });
  document.addEventListener('keydown', (e) => { if (e.key === 'Escape' && UI.$('#banner') && !UI.$('#scrim')) UI.$('#take').click(); });
})();
