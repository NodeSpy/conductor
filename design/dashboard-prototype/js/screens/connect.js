/* "Connect with Slack / GitHub": the app-manifest flows, mocked. The dashboard generates the app manifest
 * (Slack app manifest, GitHub App manifest), sends you to the service to create and install it, and on return
 * stores the tokens it hands back in conductor's vault (a signed secret write) and stages the connector with
 * references to them. The manual form stays for "I already have tokens". After connecting it says, in words,
 * what bots will be able to do with the connection. */
(function () {
  const { icon, esc } = UI;
  const RISK = /merge|resolve|apply|push|delete|close/;
  const verbsLine = (use, inst) => { const d = SCHEMA.connectors[use]; if (!d) return ''; const vs = Object.entries(d.verbs || {});
    const read = vs.filter(([, v]) => v.read), write = vs.filter(([, v]) => !v.read);
    return `<div class="conn-can"><div class="caps">What bots will be able to do with this</div><p class="small" style="margin:4px 0">Nothing yet on its own: each bot gets only the actions you grant it. ${read.length ? `Reading (${read.map(([k]) => `<span class="mono">${inst}.${k}</span>`).join(', ')}) is safe to grant.` : 'This service has no read-only actions.'} ${write.length ? `${write.map(([k]) => `<span class="mono">${inst}.${k}</span>${RISK.test(k) ? ' (high impact)' : ''}`).join(', ')} change${write.length === 1 ? 's' : ''} things and ask${write.length === 1 ? 's' : ''} you first unless you let ${write.length === 1 ? 'it' : 'them'} run on ${write.length === 1 ? 'its' : 'their'} own.` : 'Nothing it does changes anything.'}</p></div>`; };
  const MANIFEST = {
    slack: (inst) => `display_information:\n  name: conductor (${inst})\nfeatures:\n  bot_user: { display_name: conductor, always_online: true }\noauth_config:\n  scopes:\n    bot: [chat:write, app_mentions:read, channels:history, reactions:write]\nsettings:\n  socket_mode_enabled: true\n  event_subscriptions:\n    bot_events: [app_mention, message.channels]\n`,
    github: (inst) => `{\n  "name": "conductor-${inst}",\n  "url": "https://ops-box.tail0000.ts.net",\n  "hook_attributes": { "url": "https://smee.io/conductor-${inst}" },\n  "default_permissions": { "pull_requests": "write", "checks": "read", "issues": "write", "contents": "read", "metadata": "read" },\n  "default_events": ["pull_request", "check_run", "issues", "issue_comment"]\n}\n`,
  };
  const SERVICE = { slack: { label: 'Slack', btn: 'Create the app in Slack', back: 'Slack installed the app to the acme workspace and returned a bot token and an app-level token.', keys: ['bot_token', 'app_token'] }, github: { label: 'GitHub', btn: 'Create the GitHub App', back: 'GitHub created the app "conductor-gh" (app id 417209) and returned its private key and webhook secret.', keys: ['private_key', 'webhook_secret'] } };
  UI.connectManifest = (use) => { const sv = SERVICE[use], base = use === 'github' ? 'gh' : use, taken = (n) => S.view().connectors.some((c) => c.name === n); let inst = base, i = 2; while (taken(inst)) inst = `${base}-${i++}`; const st = { inst, phase: 0 };
    const draw = () => { const m = document.querySelector('.modal'); if (m) m.innerHTML = body(); };
    const body = () => `<div class="modal-head"><div><div class="caps">connect with ${sv.label}</div><h2>${st.phase ? `${sv.label} is connected` : `Create a ${sv.label} app for conductor`}</h2></div><button class="icon-btn" data-close aria-label="Close">${icon('x')}</button></div><div class="modal-body stack">
      ${st.phase === 0 ? `<div class="field"><label for="cm-inst">Name</label><input class="input mono" id="cm-inst" value="${esc(st.inst)}" data-on-input="cm-inst"></div>
        <p class="small" style="margin:0">conductor made this ${use === 'slack' ? 'app manifest' : 'GitHub App manifest'}. ${sv.label} opens with it filled in; you review the permissions there and install the app. Nothing is stored until you come back.</p>
        <details><summary class="small">The manifest</summary>${UI.yaml(MANIFEST[use](st.inst))}</details>` :
        `<div class="callout ok small">${icon('check')} ${esc(sv.back)}</div><p class="small" style="margin:0">The ${sv.keys.map((k) => k.replace('_', ' ')).join(' and ')} go to conductor's vault as <span class="mono">house/${esc(st.inst)}-…</span> (you sign the write), and <span class="mono">connectors.${esc(st.inst)}</span> is staged with references to them.</p>${verbsLine(use, st.inst)}`}</div>
      <div class="modal-foot"><button class="btn ghost" data-close>Cancel</button>${st.phase === 0 ? `<button class="btn primary" data-act="cm-go">${icon('link')}${sv.btn}</button>` : `<button class="btn primary" data-act="cm-store">Store the tokens…</button>`}</div>`;
    UI.modal(body());
    UI.cm = { st, draw, use };
  };
  UI.act('cm-inst', (el) => { UI.cm.st.inst = el.value.trim(); });
  UI.act('cm-go', () => { if (!/^[a-z][a-z0-9-]*$/.test(UI.cm.st.inst) || S.view().connectors.some((c) => c.name === UI.cm.st.inst)) { UI.toast('Pick another name for the connection', 'lowercase, digits, dashes; not one in use', 'fail'); return; } UI.cm.st.phase = 1; UI.cm.draw(); });
  UI.act('cm-store', () => { const { st, use } = UI.cm, sv = SERVICE[use], keys = sv.keys.map((k) => `${st.inst}-${k.replace('_', '-')}`);
    UI.closeModal();
    UI.stepup({ title: `Store ${keys.length} tokens in the vault`, purpose: 'secret write', ok: 'Store with passkey', what: `{ purpose: <b>secret_write</b>, vault: <b>house</b>, keys: <b>${keys.join(', ')}</b> }` }).then((ok) => { if (!ok) return;
      keys.forEach((key) => { const ref = 'house/' + key; if (!D.secrets.some((x) => x.ref === ref)) D.secrets.unshift({ name: ref, ref, vault: 'house', key, alias: '', set: new Date().toISOString(), used: [] }); D.audit.unshift({ ts: new Date().toISOString(), event: 'secret_write', who: 'operator:ops', detail: `${ref} · from the ${sv.label} app install · value not recorded`, run: '' }); });
      const n = { use }; sv.keys.forEach((k, i) => { n[k] = `{{ vault "house" "${keys[i]}" }}`; });
      if (use === 'slack') { n.mode = 'socket'; n.default_channel = '#ops'; } else { n.app_id = 417209; n.webhook = 'smee'; n.sweep = 'adaptive 30s'; }
      S.save(); UI.staged(S.stage('connectors', st.inst, n, `connect ${sv.label} as connectors.${st.inst} (app manifest)`)); }); });
})();
