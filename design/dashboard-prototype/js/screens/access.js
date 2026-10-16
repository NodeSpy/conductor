/* Access: passkey sign-in, operator enrollment (CLI one-time localhost link), passkeys, sessions, listeners. */
(function () {
  const { icon, st, tag, ago } = UI;
  const baton = `<svg width="34" height="34" viewBox="0 0 24 24" fill="none"><path d="M4 20L18.5 5.5" stroke="var(--ink)" stroke-width="1.6" stroke-linecap="round"/><circle cx="19.5" cy="4.5" r="2.3" fill="var(--accent)"/><path d="M3 21l2.4-.7-1.7-1.7z" fill="var(--ink)"/></svg>`;
  const gate = (inner) => `<div class="gate" style="position:fixed;inset:0;z-index:60;background:var(--bg);background-image:radial-gradient(var(--dot) 1px,transparent 1.2px);background-size:22px 22px">${inner}</div>`;

  UI.route('signin', () => ({
    toString: () => gate(`<div class="panel card"><div class="row">${baton}<span class="mono tiny muted" style="margin-left:auto">${D.daemon.magicdns}</span></div>
      <h1>conductor</h1><p class="dim" style="margin:0 0 20px">Sign in to <b>ops-box</b> with your passkey.</p>
      <div class="seal" id="seal" style="padding:6px 0 16px"><div class="ring">${icon('passkey', 'key')}<svg class="arc" viewBox="0 0 108 108"><circle class="track" cx="54" cy="54" r="52"/><circle class="fill" cx="54" cy="54" r="53"/></svg></div></div>
      <button class="btn lg primary" style="width:100%;justify-content:center" id="go">${icon('passkey')}Sign in with a passkey</button>
      <div class="muted tiny" style="margin-top:14px;line-height:1.6">Reachable only on 127.0.0.1 and this box's tailnet address. No passkey on this device? Run <span class="mono">conductor dashboard enroll</span> on the box for a one-time link.</div>
      <div class="row tiny muted" style="margin-top:16px;border-top:1px dashed var(--line);padding-top:12px"><span>${icon('shield')} TLS · Tailscale cert</span><a style="margin-left:auto;color:var(--accent)" href="#/enroll">Enrollment demo →</a></div></div>`),
    after(main) {
      UI.$('#go').addEventListener('click', () => {
        UI.$('#seal').classList.add('signing');
        setTimeout(() => { UI.$('#seal').classList.remove('signing'); UI.$('#seal').classList.add('done'); setTimeout(() => UI.go('#/overview'), 600); }, 1300);
      });
    },
  }), { nav: 'security' });

  UI.route('enroll', () => ({
    toString: () => gate(`<div class="panel card" style="width:min(560px,100%)"><div class="row">${baton}<span class="tag" data-s="wait">one-time link · expires in 9:41</span></div>
      <h1>Enroll a passkey</h1><p class="dim" style="margin:0 0 16px">You opened the link <span class="mono">conductor dashboard enroll</span> printed. It works once, on <b>localhost</b> only, for 10 minutes. Then registration continues on the tailnet origin, because a passkey belongs to one name.</p>
      <div class="code" style="margin-bottom:16px"><span class="ln"><span class="y-c">$ conductor dashboard enroll --operator ops</span></span><span class="ln">operator ops: open this on THIS machine within 10m (single use):</span><span class="ln"><span class="y-s">  http://localhost:7788/enroll#t=Hk3v…q9</span></span><span class="ln">pairing code: <span class="y-n">417-209</span></span></div>
      <div class="stack">
        <div class="grid g2"><div class="field"><label>Operator</label><input class="input mono" value="ops" disabled></div><div class="field"><label>Pairing code</label><input class="input mono" value="417-209"></div></div>
        <div class="field"><label>Name this passkey</label><input class="input" value="laptop · Touch ID"></div>
        <div class="field"><label>Register on</label><div class="seg"><button class="on">${D.daemon.magicdns}</button><button>localhost only</button></div><div class="hint">The tailnet name (the default) works from this box and your phone; this page hands a one-time ticket to <span class="mono">https://${D.daemon.magicdns}:7788/enroll</span>, here or by QR on another device. A localhost-only passkey works only on the box.</div></div>
        <div class="callout warn small">You already have 2 passkeys, so adding a third also needs one of them. Every enrollment is audited and fires <span class="mono">conductor.dashboard_enrolled</span>.</div>
        <button class="btn lg primary" style="justify-content:center" onclick="UI.toast('Passkey “laptop · Touch ID” enrolled','operator ops · user verification required','ok');UI.go('#/security')">${icon('passkey')}Create passkey</button>
      </div></div>`),
  }), { nav: 'security' });

  UI.route('security', () => {
    const d = D.daemon;
    return `<div class="page">
      ${UI.head('Access', { sub: 'operators, passkeys, sessions and where the dashboard listens' })}
      <div class="grid g2">
        <div class="stack">
          <div class="panel"><div class="panel-head"><h3>Passkeys · ops</h3><div class="tools"><a class="btn sm" href="#/enroll">${icon('plus')}Add (CLI link)</a></div></div><div class="list">
            ${[['laptop · Touch ID', 'ops-box.tail0000.ts.net', 'platform authenticator', '4m ago', 212], ['phone · Face ID', 'ops-box.tail0000.ts.net', 'synced passkey', '2d ago', 31], ['security key', 'ops-box.tail0000.ts.net', 'roaming · FIDO2', '19d ago', 4]].map(([n, rp, k, used, c]) => `<div class="item"><span class="icon-btn">${icon('passkey')}</span><div class="grow"><b>${n}</b><div class="mono tiny muted">${rp} · ${k} · sign count ${c}</div></div><span class="mono tiny muted">used ${used}</span><button class="btn sm danger">Revoke</button></div>`).join('')}</div>
            <div class="panel-foot small muted">Revoking a passkey ends its sessions. The last passkey can be replaced only through a fresh CLI enrollment link.</div></div>
          <div class="panel"><div class="panel-head"><h3>Sessions</h3><div class="tools"><button class="btn sm danger">Sign out everywhere</button></div></div><div class="list">
            <div class="item">${st('ok', '')}<div class="grow small"><b>Firefox · laptop</b> ${tag('this session', 'ember')}<div class="mono tiny muted">127.0.0.1 · started ${ago(D.operator.session.created)} ago · idle ${D.operator.session.idle}</div></div></div>
            <div class="item">${st('ok', '')}<div class="grow small"><b>Safari · phone</b><div class="mono tiny muted">100.101.7.3 (tailnet) · started 2d ago · idle 6h</div></div><button class="btn sm ghost">End</button></div></div>
            <div class="panel-foot small muted">Idle 30m, absolute 12h. Cookie: <span class="mono">__Host-</span>, Secure, HttpOnly, SameSite=Strict.</div></div>
        </div>
        <div class="stack">
          <div class="panel"><div class="panel-head"><h3>Listeners</h3>${st('ok', 'private', true)}</div><div class="panel-body"><dl class="kv small">
            <dt>localhost</dt><dd class="mono">http://localhost:7788 (a secure context by spec) · bootstrap + enrollment</dd>
            <dt>tailnet</dt><dd class="mono">https://${d.magicdns}:7788 · ${d.tailscale_ip}</dd><dt>certificate</dt><dd class="mono">${d.tls}</dd>
            <dt>refused</dt><dd class="small">0.0.0.0, LAN and public addresses; exposure connectors (tunnels) never serve the dashboard</dd></dl></div></div>
          <div class="panel"><div class="panel-head"><h3>Step-up</h3></div><div class="panel-body small stack tight">
            <div>A fresh passkey assertion is required for:</div>
            <div class="row wrap" style="gap:6px">${['approve / deny', 'config apply & restore', 'secret write', 'grant / widen', 'browser takeover', 'update now', 'pack add/remove'].map((t) => tag(t)).join('')}</div>
            <div class="muted">Everything else uses the session.</div></div></div>
          <div class="panel"><div class="panel-head"><h3>Lockout</h3></div><div class="panel-body small"><dl class="kv"><dt>failed assertions</dt><dd>5 per 15m per client → 15m lock, doubling</dd><dt>enrollment links</dt><dd>3 per hour; one live at a time</dd><dt>right now</dt><dd>${st('ok', 'no locks')}</dd></dl></div></div>
        </div></div></div>`;
  });
})();
