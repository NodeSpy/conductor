/* Access: passkey sign-in, operator enrollment (CLI one-time localhost link), passkeys, sessions, listeners. */
(function () {
  const { icon, st, tag, ago, esc } = UI;
  const baton = `<svg width="34" height="34" viewBox="0 0 24 24" fill="none"><path d="M4 20L18.5 5.5" stroke="var(--ink)" stroke-width="1.6" stroke-linecap="round"/><circle cx="19.5" cy="4.5" r="2.3" fill="var(--accent)"/><path d="M3 21l2.4-.7-1.7-1.7z" fill="var(--ink)"/></svg>`;
  const gate = (inner) => `<div class="gate" style="position:fixed;inset:0;z-index:60;background:var(--bg);background-image:radial-gradient(var(--dot) 1px,transparent 1.2px);background-size:22px 22px">${inner}</div>`;

  UI.route('signin', () => ({
    toString: () => gate(`<div class="panel card"><div class="row">${baton}<span class="mono tiny muted" style="margin-left:auto">${D.daemon.magicdns}</span></div>
      <h1>conductor</h1><p class="dim" style="margin:0 0 20px">Sign in to <b>ops-box</b> with your passkey.</p>
      <div class="seal" id="seal" style="padding:6px 0 16px"><div class="ring">${icon('passkey', 'key')}<svg class="arc" viewBox="0 0 108 108"><circle class="track" cx="54" cy="54" r="52"/><circle class="fill" cx="54" cy="54" r="53"/></svg></div></div>
      <button class="btn lg primary" style="width:100%;justify-content:center" data-act="signin">${icon('passkey')}Sign in with a passkey</button>
      <div class="muted tiny" style="margin-top:14px;line-height:1.6">Reachable only on 127.0.0.1 and this box's tailnet address. No passkey on this device? Run <span class="mono">conductor dashboard enroll</span> on the box for a one-time link.</div>
      <div class="row tiny muted" style="margin-top:16px;border-top:1px dashed var(--line);padding-top:12px"><span>${icon('shield')} TLS · Tailscale cert</span><a style="margin-left:auto;color:var(--accent)" href="#/enroll?from=signin">Enrollment demo →</a></div></div>`),
  }), { nav: 'security' });

  const ORD = ['first', 'second', 'third', 'fourth', 'fifth', 'sixth', 'seventh', 'eighth', 'ninth', 'tenth'];
  const ordinal = (n) => ORD[n - 1] || n + 'th';
  const enFrom = () => (new URLSearchParams(location.hash.split('?')[1] || '').get('from') === 'signin' ? '#/signin' : '#/security');
  UI.route('enroll', () => {
    const n = D.passkeys.length, back = enFrom();
    if (en.void) return { toString: () => gate(`<div class="panel card" style="width:min(560px,100%)"><div class="row">${baton}<a class="icon-btn" style="margin-left:auto" href="${back}" aria-label="Close">${icon('x')}</a></div><h1>This link is void</h1><p class="dim">The one-time enrollment link was cancelled and can't be used again. Run <span class="mono">conductor dashboard enroll</span> on the box for a new one.</p><a class="btn" href="${back}" data-act="en-reset">${icon('chev')}Back to ${back === '#/signin' ? 'sign-in' : 'Access'}</a></div>`) };
    return { toString: () => gate(`<div class="panel card" style="width:min(580px,100%)"><div class="gate-head">${baton}<span class="tag" data-s="wait">one-time link · 9:41 left</span><a class="icon-btn" style="margin-left:auto" href="${back}" aria-label="Close and go back" data-tip="Back without using the link">${icon('x')}</a></div>
      <h1>Enroll a passkey</h1><p class="dim" style="margin:0 0 16px">You opened the link <span class="mono">conductor dashboard enroll</span> printed. It works once, on <b>localhost</b> only, for 10 minutes. Then registration continues on the tailnet origin, because a passkey belongs to one name.</p>
      <div class="code wrap" style="margin-bottom:16px"><span class="ln"><span class="y-c">$ conductor dashboard enroll --operator ops</span></span><span class="ln">operator ops: open this on THIS machine within 10m (single use):</span><span class="ln"><span class="y-s">http://localhost:7788/enroll#t=Hk3v…q9</span></span><span class="ln">pairing code: <span class="y-n">417-209</span></span></div>
      <div class="stack">
        <div class="grid g2"><div class="field"><label for="en-op">Operator</label><input class="input mono" id="en-op" value="ops" readonly></div><div class="field" id="en-code-f"><label for="en-code">Pairing code <span class="opt">demo: 417-209</span></label><input class="input mono" id="en-code" placeholder="000-000" inputmode="numeric"><div class="ferr"></div></div></div>
        <div class="field"><label for="en-name">Name this passkey</label><input class="input" id="en-name" value="${['phone · Face ID', 'desktop · Windows Hello', 'yubikey'][n % 3]}"></div>
        <div class="field"><label>Register on</label>${UI.seg([['tailnet', D.daemon.magicdns], ['localhost', 'localhost only']], en.rp, 'en-rp')}<div class="hint">The tailnet name (the default) works from this box and your phone; this page hands a one-time ticket to <span class="mono">https://${D.daemon.magicdns}:7788/enroll</span>, here or by QR on another device. A localhost-only passkey works only on the box.</div></div>
        <div class="callout warn small" data-count="passkeys:${n}">${n ? `You already have ${n} passkey${n > 1 ? 's' : ''}, so adding a ${ordinal(n + 1)} also needs one of them: you confirm with an existing passkey before the new one registers.` : 'This is your first passkey: the pairing code is the proof.'} Every enrollment is audited and fires <span class="mono">conductor.dashboard_enrolled</span>.</div>
        <div class="wizard-foot"><button class="btn ghost" data-act="en-cancel">Cancel</button><span class="spacer"></span><button class="btn lg primary" data-act="enroll">${icon('passkey')}Create passkey</button></div>
      </div></div>`) };
  }, { nav: 'security' });

  const en = { rp: 'tailnet' };
  UI.act('signin', () => { const seal = UI.$('#seal'); seal.classList.add('signing'); setTimeout(() => { seal.classList.remove('signing'); seal.classList.add('done'); if (!D.sessions.find((x) => x.current)) D.sessions.unshift({ id: 's-' + Date.now().toString(36), device: 'Firefox · laptop', addr: '127.0.0.1', started: new Date().toISOString(), idle: '0m', current: true }); S.save(); setTimeout(() => UI.go('#/overview'), UI.fast ? 20 : 500); }, UI.fast ? 30 : 1100); });
  UI.act('en-rp', (b) => { en.rp = b.dataset.v; UI.$$('[data-act="en-rp"]').forEach((x) => { x.classList.toggle('on', x === b); x.setAttribute('aria-pressed', x === b); }); });
  const register = (name, by) => { D.passkeys.push({ id: 'pk' + Date.now().toString(36), name, rp: en.rp === 'tailnet' ? D.daemon.magicdns : 'localhost', kind: 'platform authenticator', used: new Date().toISOString(), count: 0, bound: true }); D.audit.unshift({ ts: new Date().toISOString(), event: 'dashboard_enrolled', who: 'operator:ops', detail: `passkey “${name}” · rp ${en.rp === 'tailnet' ? D.daemon.magicdns : 'localhost'}${by ? ` · confirmed with “${by}”` : ''}`, run: '' }); S.save(); UI.toast(`Passkey “${UI.esc(name)}” enrolled`, by ? `confirmed with ${UI.esc(by)} · user verification required` : 'operator ops · user verification required', 'ok'); UI.go(enFrom() === '#/signin' ? '#/signin' : '#/security'); };
  UI.act('enroll', () => {
    const code = UI.$('#en-code').value.trim(), f = UI.$('#en-code-f');
    if (code !== '417-209') { f.classList.add('bad'); f.querySelector('.ferr').textContent = code ? 'That code doesn\'t match the one the CLI printed. A wrong code burns the ticket after 3 tries.' : 'Type the pairing code the CLI printed.'; UI.$('#en-code').focus(); return; }
    const name = UI.$('#en-name').value.trim() || 'new passkey';
    if (!D.passkeys.length) return register(name);
    // adding to an existing set needs one of them: a WebAuthn assertion (simulated) before the new credential registers
    let pick = D.passkeys[0].id;
    const s = UI.modal(`<div class="modal-head"><div><div class="caps">step 2 of 2 · webauthn assertion</div><h2>Confirm with an existing passkey</h2></div><button class="icon-btn" data-close aria-label="Close">${icon('x')}</button></div>
      <div class="modal-body stack"><p class="small dim" style="margin:0">“${esc(name)}” registers only after one of your ${D.passkeys.length} passkeys says yes.</p>
        <div class="choice-grid" id="pk-pick">${D.passkeys.map((k, i) => UI.choice({ act: 'pk-pick', attrs: `data-id="${k.id}"`, icon: 'passkey', title: esc(k.name), desc: `${esc(k.kind)} · used ${ago(k.used)} ago`, on: i === 0 })).join('')}</div></div>
      <div class="modal-foot"><button class="btn ghost" data-close>Cancel</button><button class="btn primary" id="pk-go">${icon('passkey')}Use this passkey</button></div>`);
    UI.act('pk-pick', (b) => { pick = b.dataset.id; s.querySelectorAll('.choice').forEach((x) => { x.classList.toggle('on', x === b); x.setAttribute('aria-pressed', x === b); }); });
    UI.$('#pk-go', s).addEventListener('click', () => { const k = D.passkeys.find((x) => x.id === pick); k.used = new Date().toISOString(); k.count++; UI.closeModal(); register(name, k.name); });
  });
  UI.act('en-cancel', () => UI.confirm({ title: 'Cancel this enrollment?', body: 'The one-time link becomes void on the box and can\'t be used again. Run <span class="mono">conductor dashboard enroll</span> for a new one.', ok: 'Void the link', cancel: 'Keep enrolling', danger: true }).then((ok) => { if (!ok) return; en.void = true; D.audit.unshift({ ts: new Date().toISOString(), event: 'dashboard_enroll_cancelled', who: 'operator:ops', detail: 'one-time link voided', run: '' }); S.save(); UI.rerender(); UI.toast('This link is now void', 'run conductor dashboard enroll for a new one', 'ok'); }));
  UI.act('en-reset', () => { en.void = false; });
  UI.act('pk-revoke', (b) => { const k = D.passkeys.find((x) => x.id === b.dataset.id); UI.stepup({ title: `Revoke “${UI.esc(k.name)}”`, purpose: 'passkey revoke', danger: true, ok: 'Revoke with passkey', what: `{ purpose: <b>passkey_revoke</b>, credential: <b>${UI.esc(k.name)}</b> } · its sessions end` }).then((ok) => { if (!ok) return; D.passkeys = D.passkeys.filter((x) => x !== k); D.audit.unshift({ ts: new Date().toISOString(), event: 'dashboard_passkey_revoked', who: 'operator:ops', detail: k.name, run: '' }); S.save(); UI.rerender(); }); });
  UI.act('sess-end', (b) => { D.sessions = D.sessions.filter((x) => x.id !== b.dataset.id); D.audit.unshift({ ts: new Date().toISOString(), event: 'dashboard_session_end', who: 'operator:ops', detail: b.dataset.id, run: '' }); S.save(); UI.rerender(); });
  UI.act('sess-all', () => UI.confirm({ title: 'Sign out everywhere?', body: 'Ends every session, this one too. You sign in again with a passkey.', ok: 'Sign out everywhere', danger: true }).then((ok) => { if (!ok) return; D.sessions = []; D.audit.unshift({ ts: new Date().toISOString(), event: 'dashboard_logout_all', who: 'operator:ops', detail: 'all sessions', run: '' }); S.save(); UI.go('#/signin'); }));
  UI.act('op-bound', () => { const c = JSON.parse(JSON.stringify(S.staged('operators', 'ops'))); c.require_device_bound = !c.require_device_bound; S.stage('operators', 'ops', c); UI.refreshChrome(); UI.rerender(); });
  UI.route('security', () => {
    const d = D.daemon;
    return `<div class="page">
      ${UI.head('Access', { sub: 'operators, passkeys, sessions and where the dashboard listens' })}
      <div class="grid g2">
        <div class="stack">
          <div class="panel"><div class="panel-head"><h3>Passkeys · ops</h3><div class="tools"><a class="btn sm" href="#/enroll?from=security">${icon('plus')}Add (CLI link)</a></div></div><div class="list">
            ${D.passkeys.map((k) => `<div class="item"><span class="icon-btn" aria-hidden="true">${icon('passkey')}</span><div class="grow"><b>${esc(k.name)}</b> ${k.bound ? tag('device-bound') : tag('synced')}<div class="mono tiny muted">${k.rp} · ${k.kind} · sign count ${k.count}</div></div><span class="mono tiny muted hide-phone">used ${ago(k.used)} ago</span><button class="btn sm danger" ${D.passkeys.length > 1 ? `data-act="pk-revoke" data-id="${k.id}"` : UI.off('Your last passkey: enroll another first, or reset with conductor dashboard reset')}>Revoke</button></div>`).join('')}</div>
            <div class="panel-foot small muted">Revoking a passkey ends its sessions. The last passkey can be replaced only through a fresh CLI enrollment link.</div></div>
          <div class="panel"><div class="panel-head"><h3>Approval policy</h3>${S.find('operators', 'ops') ? tag('staged', 'run') : ''}</div><div class="panel-body small stack tight">
            <label class="row">${UI.switchBtn(!!(S.staged('operators', 'ops') || {}).require_device_bound, 'op-bound', 'aria-label="Require a device-bound passkey for approvals"')}<span><b>Require a device-bound passkey for approvals</b><div class="muted tiny">Synced passkeys (iCloud, Google) still sign in and apply config; approvals then need a key that never leaves its device.</div></span></label>
            <a class="btn sm primary" href="#/config/apply" ${S.find('operators', 'ops') ? '' : UI.off('Change the policy first')} style="align-self:flex-start">Review change</a></div></div>
          <div class="panel"><div class="panel-head"><h3>Sessions</h3><div class="tools"><button class="btn sm danger" data-act="sess-all">Sign out everywhere</button></div></div><div class="list">
            ${D.sessions.map((x) => `<div class="item">${st('ok', '')}<div class="grow small"><b>${esc(x.device)}</b> ${x.current ? tag('this session', 'ember') : ''}<div class="mono tiny muted">${esc(x.addr)} · started ${ago(x.started)} ago · idle ${x.idle}</div></div>${x.current ? '' : `<button class="btn sm ghost" data-act="sess-end" data-id="${x.id}">End</button>`}</div>`).join('')}</div>
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
