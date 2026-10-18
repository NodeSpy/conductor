/* Access: passkey sign-in, operator enrollment (CLI one-time localhost link), passkeys, sessions, listeners. */
(function () {
  const { icon, st, tag, ago, esc } = UI;
  const baton = () => UI.logo('horizontal', 'gate-mark'); // rendered per visit: the kit's mark loads after this file
  const gate = (inner) => `<div class="gate">${inner}</div>`;

  UI.route('signin', () => ({
    toString: () => gate(`<div class="panel card"><div class="row">${baton()}</div>
      <h1>Sign in</h1><p class="dim" style="margin:0 0 20px">Sign in to <b>ops-box</b> with your passkey. <span class="item-id" style="display:block;margin-top:4px">${D.daemon.magicdns}</span></p>
      <button class="btn lg primary" style="width:100%;justify-content:center" data-act="signin">${icon('passkey')}Sign in with a passkey</button>
      <div class="muted tiny" style="margin-top:14px;line-height:1.6">Reachable only on 127.0.0.1 and this box's tailnet address. No passkey on this device? Run <span class="mono">conductor dashboard enroll</span> on the box for a one-time link.</div>
      <div class="row tiny muted gate-foot"><span>${icon('shield')} TLS · Tailscale cert</span><a class="link" style="margin-left:auto" href="#/enroll?from=signin">Enrollment demo →</a></div></div>`),
  }), { nav: 'security', bare: true });

  const ORD = ['first', 'second', 'third', 'fourth', 'fifth', 'sixth', 'seventh', 'eighth', 'ninth', 'tenth'];
  const ordinal = (n) => ORD[n - 1] || n + 'th';
  const enFrom = () => (new URLSearchParams(location.hash.split('?')[1] || '').get('from') === 'signin' ? '#/signin' : '#/security');
  UI.route('enroll', () => {
    const n = D.passkeys.length, back = enFrom();
    if (en.void) return { toString: () => gate(`<div class="panel card" style="width:min(560px,100%)"><div class="row">${baton()}<a class="icon-btn" style="margin-left:auto" href="${back}" aria-label="Close">${icon('x')}</a></div><h1>This link is void</h1><p class="dim">The one-time enrollment link was cancelled and can't be used again. Run <span class="mono">conductor dashboard enroll</span> on the box for a new one.</p><a class="btn" href="${back}" data-act="en-reset">${icon('chev')}Back to ${back === '#/signin' ? 'sign-in' : 'Access'}</a></div>`) };
    return { toString: () => gate(`<div class="panel card" style="width:min(580px,100%)"><div class="gate-head">${baton()}${UI.label(`one-time link · 9:41 left`, { s: 'wait' })}<a class="icon-btn" style="margin-left:auto" href="${back}" aria-label="Close and go back" data-tip="Back without using the link">${icon('x')}</a></div>
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
  UI.act('signin', (b) => { b.setAttribute('aria-disabled', 'true'); b.lastChild.textContent = 'Confirm on your device…'; setTimeout(() => { if (!D.sessions.find((x) => x.current)) D.sessions.unshift({ id: 's-' + Date.now().toString(36), device: 'Firefox · laptop', addr: '127.0.0.1', started: new Date().toISOString(), idle: '0m', current: true }); S.save(); setTimeout(() => UI.go('#/overview'), UI.fast ? 20 : 500); }, UI.fast ? 30 : 1100); });
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
        <div class="choice-grid" id="en-pk-pick">${D.passkeys.map((k, i) => UI.choice({ act: 'en-pk-pick', attrs: `data-id="${k.id}"`, icon: 'passkey', title: esc(k.name), desc: `${esc(k.kind)} · used ${ago(k.used)} ago`, on: i === 0 })).join('')}</div></div>
      <div class="modal-foot"><button class="btn ghost" data-close>Cancel</button><button class="btn primary" id="pk-go">${icon('passkey')}Use this passkey</button></div>`);
    en.pick = (id) => { pick = id; };
    UI.$('#pk-go', s).addEventListener('click', () => { const k = D.passkeys.find((x) => x.id === pick); k.used = new Date().toISOString(); k.count++; UI.closeModal(); register(name, k.name); });
  });
  UI.act('en-pk-pick', (b) => { if (en.pick) en.pick(b.dataset.id); b.closest('.choice-grid').querySelectorAll('.choice').forEach((x) => { x.classList.toggle('on', x === b); x.setAttribute('aria-pressed', x === b); }); });
  UI.act('en-cancel', () => UI.confirm({ title: 'Cancel this enrollment?', body: 'The one-time link becomes void on the box and can\'t be used again. Run <span class="mono">conductor dashboard enroll</span> for a new one.', ok: 'Void the link', cancel: 'Keep enrolling', danger: true }).then((ok) => { if (!ok) return; en.void = true; D.audit.unshift({ ts: new Date().toISOString(), event: 'dashboard_enroll_cancelled', who: 'operator:ops', detail: 'one-time link voided', run: '' }); S.save(); UI.rerender(); UI.toast('This link is now void', 'run conductor dashboard enroll for a new one', 'ok'); }));
  UI.act('en-reset', () => { en.void = false; });
  UI.act('pk-revoke', (b) => { const k = D.passkeys.find((x) => x.id === b.dataset.id); UI.confirm({ title: `Remove “${UI.esc(k.name)}”?`, body: 'It can no longer sign in or approve, and any session it started ends now. Your other passkeys keep working.', ok: 'Remove passkey', danger: true }).then((ok) => { if (!ok) return; D.passkeys = D.passkeys.filter((x) => x !== k); D.audit.unshift({ ts: new Date().toISOString(), event: 'dashboard_passkey_revoked', who: 'operator:ops', detail: k.name, run: '' }); S.save(); UI.rerender(); UI.toast(`Removed ${UI.esc(k.name)}`, '', 'ok'); }); });
  UI.act('sess-end', (b) => { D.sessions = D.sessions.filter((x) => x.id !== b.dataset.id); D.audit.unshift({ ts: new Date().toISOString(), event: 'dashboard_session_end', who: 'operator:ops', detail: b.dataset.id, run: '' }); S.save(); UI.rerender(); });
  UI.act('sess-all', () => UI.confirm({ title: 'Sign out everywhere?', body: 'Every device is signed out, this one too. You sign in again with a passkey.', ok: 'Sign out everywhere', danger: true }).then((ok) => { if (!ok) return; D.sessions = []; D.audit.unshift({ ts: new Date().toISOString(), event: 'dashboard_logout_all', who: 'operator:ops', detail: 'all sessions', run: '' }); S.save(); UI.go('#/signin'); }));
  UI.act('op-bound', () => { const c = JSON.parse(JSON.stringify(S.staged('operators', 'ops'))); c.require_device_bound = !c.require_device_bound; S.stage('operators', 'ops', c); UI.refreshChrome(); UI.rerender(); });
  /* Access, in plain words: who can get in and from where, which devices can approve, what's signed in now, and when
     conductor asks for your passkey. Listeners, the certificate, lockout numbers and cookie flags are folded. */
  const devWord = (k) => (k.bound ? 'Stays on this device' : 'Synced to your other devices');
  UI.route('security', () => {
    const d = D.daemon, pol = S.staged('operators', 'ops') || {};
    return `<div class="page dl-page">${UI.systemTabs('security')}
      <div class="dl-phead"><div><h1>Access</h1><p class="dl-lead"><b>Only you can sign in</b>, with a passkey, from this computer and your tailnet.</p></div></div>
      <div class="dl-cols"><div class="dl-col-main">
        <section class="dl-card"><header class="dl-ch"><h2>Your passkeys</h2><span class="dl-sp"></span><a class="btn sm secondary" href="#/enroll?from=security">${icon('plus')}Add a passkey</a></header>
          <ul class="dl-list">${D.passkeys.map((k) => `<li class="acc-row pk-list"><div class="dl-nt"><b>${esc(k.name)}</b><span class="dl-why">${devWord(k)} · last used ${UI.agoText(k.used)}</span></div><span class="dl-na"><button class="btn sm ghost" ${D.passkeys.length > 1 ? `data-act="pk-revoke" data-id="${k.id}"` : UI.off('Your only passkey: add another first, so you can still sign in')}>Remove…</button></span></li>`).join('')}</ul>
          <p class="dl-sub acc-note">Adding a passkey needs a one-time link from the command line on this computer (<span class="mono">conductor dashboard enroll</span>), so only someone with your SSH access can add one.</p></section>
        <section class="dl-card"><header class="dl-ch"><h2>Signed in now</h2><span class="dl-sp"></span><button class="btn sm secondary" data-act="sess-all">Sign out everywhere…</button></header>
          <ul class="dl-list">${D.sessions.map((x) => `<li class="acc-row"><div class="dl-nt"><b>${esc(x.device)}${x.current ? ' <span class="dl-sub">(this one)</span>' : ''}</b><span class="dl-why">${/tailnet/.test(x.addr) ? 'over your tailnet' : 'on this computer'} · signed in ${UI.agoText(x.started)} · last active ${UI.humanSpan(x.idle)} ago</span></div><span class="dl-na">${x.current ? '' : `<button class="btn sm ghost" data-act="sess-end" data-id="${x.id}">Sign out</button>`}</span></li>`).join('')}</ul>
          <p class="dl-sub acc-note">A session ends after 30 minutes idle, or 12 hours at most.</p></section>
      </div><div class="dl-col-side">
        <section class="dl-card"><header class="dl-ch"><h2>When it needs your passkey</h2></header>
          <ul class="acc-asks"><li>Approving or denying what a bot asks to do</li><li>Changes that give bots more access or loosen safety: a new permission, a new allowed site, the sandbox off</li><li>Storing or replacing a secret value in a vault</li><li>Taking over a browser a bot is signed in to</li></ul>
          <p class="dl-sub">Everything else, including routine config changes, uses your signed-in session.</p></section>
        <section class="dl-card"><header class="dl-ch"><h2>Approvals</h2>${S.find('operators', 'ops') ? UI.statusMark('staged', 'Not applied') : ''}</header>
          <label class="row">${UI.switchBtn(!!pol.require_device_bound, 'op-bound', 'aria-label="Approve only with a passkey that stays on its device"')}<span><b>Approve only with a passkey that stays on its device</b></span></label>
          <p class="dl-sub">${pol.require_device_bound ? 'A synced passkey can still sign you in, but approvals need one that never leaves its device.' : 'Any of your passkeys can approve.'}</p>
          ${S.find('operators', 'ops') ? '<a class="btn sm primary" href="#/config/apply">Review the change</a>' : ''}</section>
        <details class="dl-card dl-tech-d acc-tech"><summary>${icon('chev')}Technical details</summary><dl class="dl-facts">
          <dt>Addresses</dt><dd><span class="mono">http://localhost:7788</span> (sign-in and enrolment only) and <span class="mono">https://${d.magicdns}:7788</span> (${d.tailscale_ip}); never 0.0.0.0, your LAN or the internet, and tunnels never serve it</dd>
          <dt>Certificate</dt><dd class="mono">${d.tls}</dd>
          <dt>Wrong passkey</dt><dd>5 failed tries in 15 minutes locks that client for 15 minutes, doubling each time</dd>
          <dt>Enrolment links</dt><dd>3 an hour, one live at a time</dd>
          <dt>Session cookie</dt><dd class="mono">__Host-, Secure, HttpOnly, SameSite=Strict</dd>
          <dt>Passkeys</dt><dd>${D.passkeys.map((k) => `${esc(k.name)}: ${esc(k.kind)}, used ${k.count} times`).join('; ')}</dd></dl></details>
      </div></div></div>`;
  });
})();
