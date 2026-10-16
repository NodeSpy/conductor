/* Agent browsers (#182): live view, takeover, seeding a login, passkey registration, per-identity settings. */
(function () {
  const { icon, st, tag, ago, esc } = UI;

  const spark = (pts, color) => `<svg viewBox="0 0 200 80" preserveAspectRatio="none"><polyline fill="none" stroke="${color}" stroke-width="1.6" points="${pts.map((v, i) => `${(i / (pts.length - 1)) * 200},${80 - v}`).join(' ')}"/></svg>`;
  const SITES = {
    'b-01': () => `<div class="site"><div class="top" style="background:#181b1f"><b style="color:#ff8833">◢ Grafana</b><span>Dashboards › API latency</span><span style="margin-left:auto;opacity:.6">Last 1 hour</span></div><div class="grafana">
      <div class="gp"><div>p99 latency by route</div>${spark([20, 22, 21, 25, 24, 52, 61, 66, 63, 64, 58, 22, 20], '#f2495c')}</div>
      <div class="gp"><div>requests / s</div>${spark([40, 42, 44, 41, 45, 47, 46, 48, 45, 44, 46, 47, 45], '#73bf69')}</div>
      <div class="gp"><div>web instances by AMI</div>${spark([30, 30, 30, 30, 50, 50, 50, 50, 50, 50, 50, 30, 30], '#5794f2')}</div>
      <div class="gp"><div>5xx rate</div>${spark([5, 5, 6, 5, 9, 14, 12, 13, 11, 10, 9, 6, 5], '#fade2a')}</div></div></div>`,
    'b-02': () => `<div class="site"><div class="top"><b>App</b><span style="opacity:.7">Sign in</span></div><div style="flex:1;display:grid"><div class="card"><div class="phone-ill">📱</div><h4>Approve the sign-in on your phone</h4><p>We sent a push notification to the device registered to <b>agent-triage@acme.example</b>. This page continues once it's approved.</p><div style="height:4px;background:#e9edf8;border-radius:2px;overflow:hidden"><div style="width:40%;height:100%;background:#3451d1;animation:indet 1.4s infinite"></div></div><p style="margin:14px 0 0;font-size:12px">Password <span class="masked">••••••••••••</span> accepted</p></div></div></div>`,
    'b-03': () => `<div class="site" style="background:#fff"><div class="top" style="background:#fff;color:#1d2433;border-bottom:1px solid #e6e8ef"><b>Docs</b><span>API</span><span>Guides</span></div><div style="padding:22px 30px;font-family:Arial,'Liberation Sans',sans-serif"><div style="font-size:12px;color:#7a8396">API › Refunds</div><h2 style="font-family:Georgia,serif;font-weight:400;font-size:28px;margin:6px 0 10px">Create a refund</h2><p style="color:#4a5366;font-size:13.5px;max-width:520px">Refund fees are rounded <b>half-even</b> to the nearest cent. A partial refund keeps the original fee schedule.</p><pre style="background:#f4f5f8;padding:12px;border-radius:6px;font-size:12px">POST /v1/refunds { "charge": "ch_123", "amount": 2000 }</pre></div></div>`,
    seed: () => `<div class="site"><div class="top"><b>Console</b></div><div style="flex:1;display:grid"><div class="card" style="text-align:left"><h4>Sign in to Console</h4><p>Use your work account.</p><div style="border:1px solid #d5d9e3;border-radius:6px;padding:9px 11px;font-size:13px;margin-bottom:8px;color:#1d2433">ops-agent@acme.example</div><div style="border:2px solid #3451d1;border-radius:6px;padding:9px 11px;font-size:13px;margin-bottom:14px;color:#9aa1b2">Password<span class="cursor-blink" style="background:#3451d1;margin-left:2px"></span></div><span class="sbtn">Continue</span><p style="margin:12px 0 0;font-size:12px">or <u>Sign in with a passkey</u></p></div></div></div>`,
  };

  /* ---------- list ---------- */
  UI.route('browser', () => `<div class="page">
    ${UI.head('Browsers', { sub: 'conductor\'s Chromium, one per agent session, sandboxed', actions: `<a class="btn" href="#/browser/seed">${icon('hand')}Seed a login</a><a class="btn" href="#/browser/passkey">${icon('passkey')}Register a passkey</a>` })}
    <div class="grid g3">${D.browsers.map((b) => `
      <a class="panel btile" href="#/browser/${b.id}">
        <div class="thumb"><div class="mini" style="height:238%">${SITES[b.id]()}</div>${b.status === 'wait' ? `<span class="ctrl-banner" style="background:var(--wait);top:8px;font-size:11px">${icon('hold')}waiting on you</span>` : ''}</div>
        <div class="panel-body stack tight">
          <div class="row">${st(b.status, b.status === 'run' ? 'agent acting' : b.status === 'wait' ? 'needs you' : 'idle')}<span class="mono tiny muted" style="margin-left:auto">${b.id} · ${b.actions} actions</span></div>
          <div class="ellipsis" style="font-weight:600">${esc(b.title)}</div>
          <div class="mono tiny muted ellipsis">${b.url}</div>
          <div class="row wrap" style="gap:5px">${tag(b.identity)}${b.loggedIn ? tag(icon('cookie') + ' logged in', 'wait') : tag('anonymous', 'ok')}</div>
        </div></a>`).join('')}
    </div>
    <div class="section-title"><h2>Per identity</h2><span class="caps">browser.* in config</span></div>
    <div class="panel"><table class="tbl responsive"><thead><tr><th>identity</th><th>allowlist</th><th>logged-in sites</th><th>credentials</th><th>evaluate</th><th></th></tr></thead><tbody>
      ${[['member:platform-sre', 'grafana.example.com, console.example.com', 'console.example.com', 'grafana (password+otp) · statuspage (passkey)', 'off'], ['agent:triage@acme/app', 'app.example.com, docs.example.com', 'app.example.com', 'app (password+otp+push)', 'off'], ['agent:docs@acme/web', 'docs.example.com', '—', '—', 'read-only']].map(([i, a, l, c, e]) => `<tr><td data-l="identity" class="mono">${i}</td><td data-l="allow" class="mono small">${a}</td><td data-l="logins" class="mono small">${l}</td><td data-l="credentials" class="small">${c}</td><td data-l="evaluate">${tag(e, e === 'off' ? 'ok' : 'wait')}</td><td class="num"><button class="btn sm">${icon('edit')}Edit</button></td></tr>`).join('')}
    </tbody></table><div class="panel-foot small muted">A <span class="mono">"*"</span> allowlist is refused. Edits go through the config pipeline; anything that widens (a site, a login, a credential, evaluate) also asks for your passkey.</div></div>
  </div>`);

  /* ---------- live view ---------- */
  function live(b) {
    return `<div class="page wide">
      ${UI.head(esc(b.title), { crumbs: `<a href="#/browser">Browsers</a> ${icon('chev')} ${b.id}`, sub: b.identity,
        actions: `${st(b.status, b.status === 'run' ? 'agent acting' : b.status === 'wait' ? 'waiting on you' : 'idle', true)}<button class="btn" id="peek">${icon('download')}Screenshot</button><button class="btn primary" id="take">${icon('hand')}Take over</button><button class="btn danger">${icon('x')}Close browser</button>` })}
      <div class="grid" style="grid-template-columns:minmax(0,1fr) 340px;align-items:start">
        <div class="panel">
          <div class="bchrome"><button class="icon-btn" style="transform:rotate(180deg)">${icon('chev')}</button><button class="icon-btn">${icon('chev')}</button><button class="icon-btn">${icon('updates')}</button>
            <div class="url">${icon('lock', 'lockic')}<span class="ellipsis">${b.url}</span></div>
            <span class="tag">${b.tabs} tab${b.tabs > 1 ? 's' : ''}</span><span class="row tiny mono muted"><span class="rec"></span>CDP screencast · 12 fps</span></div>
          <div class="bscreen" id="screen">${SITES[b.id]()}
            <svg class="fake-cursor" id="cur" style="left:62%;top:44%" viewBox="0 0 24 24"><path d="M4 2l16 9-7 2-3 7z" fill="#111" stroke="#fff" stroke-width="1.5"/></svg>
          </div>
          <div class="panel-foot small"><span class="muted">${icon('shield')} jailed Chromium · CDP over a pipe (no debugging port) · egress via the allowlist proxy only</span><span style="margin-left:auto" class="mono tiny muted">${b.profile}</span></div>
        </div>
        <div class="stack">
          <div class="panel"><div class="panel-head"><h3>Session</h3></div><div class="panel-body"><dl class="kv small">
            <dt>identity</dt><dd class="mono">${b.identity}</dd><dt>agent</dt><dd class="mono"><a href="#/live/${b.agent.startsWith('r-') ? b.agent : 'r-7f29'}" style="color:var(--accent)">${b.agent}</a></dd>
            <dt>requester</dt><dd class="mono">${b.requester}</dd><dt>profile</dt><dd>${b.loggedIn ? tag('logged-in browser · narrowed to its sites', 'wait') : tag('anonymous · ephemeral', 'ok')}</dd>
            <dt>evaluate</dt><dd>${tag(b.loggedIn ? 'off (a credential was filled)' : 'off', 'ok')}</dd></dl>
            ${b.note ? `<div class="callout ${b.status === 'wait' ? 'warn' : 'info'} small" style="margin-top:10px">${esc(b.note)}</div>` : ''}</div></div>
          <div class="panel"><div class="panel-head"><h3>Actions</h3><span class="muted small">audited · values never recorded</span></div><div class="list">${D.browserLog.map(([a, d, s]) => `<div class="item small" style="align-items:flex-start">${st(s, '')}<div class="grow"><span class="mono" style="font-weight:500">${a}</span><div class="muted tiny mono">${esc(d)}</div></div></div>`).join('')}</div></div>
        </div>
      </div></div>`;
  }
  function bindLive(main) {
    const cur = UI.$('#cur', main);
    if (cur) UI.every(1800, () => { cur.style.left = 30 + Math.random() * 50 + '%'; cur.style.top = 25 + Math.random() * 50 + '%'; });
    UI.bind(main, '#peek', 'click', () => UI.toast('Screenshot saved', 'credential fields masked · audited as a peek', 'ok'));
    UI.bind(main, '#take', 'click', (e, btn) => {
      const scr = UI.$('#screen', main), on = !scr.classList.contains('takeover');
      if (on) {
        UI.modal(`<div class="modal-head"><div><div class="caps">takeover</div><h2>Drive this browser yourself</h2></div><button class="icon-btn" data-close>${icon('x')}</button></div>
          <div class="modal-body small dim stack"><p style="margin:0">The agent's next browser action waits until you release control. Your clicks and keys go to the page through CDP input events.</p>
          <ul style="margin:0;padding-left:18px"><li>What you type is never shown to the agent and is audited as a length only.</li><li>Cookies set while you drive are kept only for this browser's logged-in sites, and only those it already had.</li><li>Release hands the page back to the agent with a fresh snapshot.</li></ul></div>
          <div class="modal-foot"><button class="btn ghost" data-close>Cancel</button><button class="btn primary" id="take-go">${icon('passkey')}Take over with passkey</button></div>`);
        UI.$('#take-go').addEventListener('click', () => {
          UI.closeModal(); scr.classList.add('takeover'); cur.style.display = 'none';
          scr.insertAdjacentHTML('afterbegin', `<div class="ctrl-banner" id="banner">${icon('hand')}You have control · agent paused<span class="kbd" style="background:rgba(0,0,0,.15);border-color:rgba(0,0,0,.2);color:inherit">Esc</span> to release</div>`);
          btn.innerHTML = icon('undo') + 'Release'; btn.classList.remove('primary');
          UI.toast('You have control', 'agent actions queue until you release', 'ember');
        });
      } else {
        scr.classList.remove('takeover'); cur.style.display = ''; UI.$('#banner').remove(); btn.innerHTML = icon('hand') + 'Take over'; btn.classList.add('primary');
        UI.toast('Released to the agent', 'it resumes with a fresh snapshot', 'ok');
      }
    });
  }

  /* ---------- seed a login / register a passkey ---------- */
  function seed(passkey) {
    const steps = passkey ? ['Choose identity & credential', 'Arm a virtual authenticator', 'Add a passkey on the site', 'Saved to vault'] : ['Choose identity & site', 'Sign in yourself', 'Keep only this site\'s cookies', 'Saved'];
    return `<div class="page wide">${UI.head(passkey ? 'Register a passkey' : 'Seed a login', { crumbs: `<a href="#/browser">Browsers</a>`, sub: passkey ? 'a dedicated, separately revocable passkey per identity and site' : 'a headless takeover, from any device on your tailnet' })}
      <div class="apply-steps">${steps.map((s, i) => `<div class="as ${i === 0 ? 'done' : i === 1 ? 'cur' : ''}"><b>${i === 0 ? '✓' : i + 1}</b>${s}</div>`).join('')}</div>
      <div class="grid" style="grid-template-columns:minmax(0,1fr) 340px;align-items:start">
        <div class="panel"><div class="bchrome"><div class="url">${icon('lock', 'lockic')}https://console.example.com/login</div><span class="ctrl-banner" style="position:static;transform:none;box-shadow:none;font-size:11px">${icon('hand')}You are driving</span></div>
          <div class="bscreen takeover">${SITES.seed()}</div></div>
        <div class="stack">
          <div class="panel"><div class="panel-body stack">
            <div class="field"><label>Identity</label><select class="select mono"><option>member:platform-sre</option><option>agent:triage@acme/app</option></select></div>
            ${passkey ? `<div class="field"><label>Credential</label><select class="select mono"><option>statuspage · https://manage.statuspage.example</option></select></div><div class="field"><label>Store the private key in</label><select class="select mono"><option>house (conductor vault) · passkeys/statuspage</option></select></div>
              <div class="callout ember small">An empty virtual authenticator is armed on this tab only. Add a passkey on the site's security page; conductor takes the one credential whose RP covers <span class="mono">manage.statuspage.example</span> and writes it to the vault. It is never shown here.</div>`
              : `<div class="field"><label>Site</label><select class="select mono"><option>console.example.com (browser.logins)</option></select></div>
              <div class="callout ember small">When you finish, conductor keeps only <b>console.example.com</b>'s cookies, in <span class="mono">&lt;state&gt;/browser/logins/</span> (0600). The session is used only for <b>your own</b> requests to this identity, in a separate browser that reaches only the logged-in sites.</div>`}
            <div class="row"><button class="btn ghost">Cancel</button><button class="btn primary" style="margin-left:auto" onclick="UI.toast('${passkey ? 'Passkey saved to house/passkeys/statuspage' : 'Login saved for console.example.com'}','${passkey ? 'sign count 0 · bound to member:platform-sre' : '6 cookies kept · 41 discarded'}','ok')">${icon('check')}${passkey ? 'Save passkey' : 'Save session'}</button></div>
          </div></div>
          <div class="panel"><div class="panel-body small muted">Or import one domain's cookies: <span class="mono">conductor browser cookies import</span>. A whole browser profile is never imported.</div></div>
        </div></div></div>`;
  }

  UI.route('browser/:id', ({ id }) => {
    if (id === 'seed') return seed(false);
    if (id === 'passkey') return seed(true);
    const b = D.browsers.find((x) => x.id === id) || D.browsers[0];
    return { toString: () => live(b), after: bindLive };
  }, { nav: 'browser' });
  document.addEventListener('keydown', (e) => { if (e.key === 'Escape' && UI.$('#banner')) UI.$('#take').click(); });
})();
