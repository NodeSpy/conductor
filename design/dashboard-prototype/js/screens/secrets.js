/* Secrets & vars: values are write-only. Stored in conductor's vault or
 * referenced from 1Password / pass / HashiCorp / file / env. Vaults, secret
 * references and settings are config (staged into the pipeline); a value
 * write goes straight to the vault after a step-up assertion. */
(function () {
  const { icon, st, tag, ago, esc } = UI;
  const VIC = { conductor: 'lock', onepassword: 'key', pass: 'key', hashicorp: 'server', file: 'file', env: 'terminal' };
  const now = () => new Date().toISOString();
  const sv = { q: '', f: 'all' };
  const vaultOf = (s) => D.vaults.find((v) => v.name === s.vault);
  const IN = { all: () => true, stored: (s) => !!s.set, referenced: (s) => !s.set, unused: (s) => !s.used.length };
  const match = (s) => IN[sv.f](s) && (!sv.q || `${s.name} ${s.ref} ${s.used.join(' ')}`.toLowerCase().includes(sv.q.toLowerCase()));
  const section = (path) => path.split(/[.[]/)[0];

  const VT = {
    conductor: [{ k: 'unlock', type: 'string', req: true, def: 'creds:house', help: 'How conductor unlocks it at boot (systemd creds, keychain…).' }],
    onepassword: [{ k: 'account', type: 'string', req: true }, { k: 'token', type: 'string', req: true, ph: 'env:OP_SERVICE_ACCOUNT_TOKEN', help: 'A service-account token reference.' }],
    pass: [{ k: 'prefix', type: 'string', def: 'conductor' }],
    file: [{ k: 'path', type: 'string', req: true, ph: '~/.config/conductor/secrets.yaml' }],
    hashicorp: [{ k: 'address', type: 'string', req: true, ph: 'https://vault.example:8200' }, { k: 'mount', type: 'string', def: 'kv' }, { k: 'token', type: 'string', req: true, ph: 'env:VAULT_TOKEN', help: 'The unlock token reference. A failing one raises a Needs-you item.' }],
  };
  function vaultForm(v) {
    const cur = v ? S.staged('vaults', v.name) : null;
    const type = cur ? cur.type : 'conductor';
    UI.form({ caps: v ? `vaults.${v.name}` : 'vaults:', title: v ? `Edit vault ${v.name}` : 'Add a vault', edit: !!v, values: cur ? Object.assign({ name: v.name }, cur) : {},
      fields: [{ k: 'name', type: 'string', req: true, readonly: !!v, pattern: /^[a-z][a-z0-9-]*$/, patternMsg: 'Lowercase letters, digits and dashes.', validate: (x) => (!v && D.vaults.find((y) => y.name === x) ? 'A vault by that name exists.' : '') },
        { k: 'type', type: 'seg', req: true, options: Object.keys(VT).map((t) => [t, t, v && t !== type ? 'Changing a vault\'s type is a new vault' : '']), def: type }]
        .concat(Object.entries(VT).flatMap(([t, fs]) => fs.map((f) => Object.assign({}, f, { k: f.k, show: (vals) => (vals.type || type) === t })))),
      note: 'A vault is config: this stages a change, validated on a copy (the unlock ref must resolve) before it applies.',
      submit: 'Review change', submitIcon: 'check',
      onSubmit: (x) => { const n = { type: x.type || type }; (VT[n.type] || []).forEach((f) => { if (x[f.k] != null) n[f.k] = x[f.k]; }); UI.staged(S.stage('vaults', x.name || v.name, n)); } });
  }
  UI.act('vault-add', () => vaultForm(null));
  UI.act('vault-edit', (b) => vaultForm(D.vaults.find((v) => v.name === b.dataset.v)));
  UI.act('vault-del', (b) => UI.confirm({ title: `Remove vault ${b.dataset.v}?`, body: 'Stages removing it from vaults:. Validation refuses it while a secret still references it.', ok: 'Stage removal', danger: true }).then((ok) => ok && UI.staged(S.stage('vaults', b.dataset.v, null))));

  function setModal(sec) {
    const writable = D.vaults.filter((v) => v.writable && v.status !== 'fail');
    UI.form({ caps: sec ? 'write-only · replace the value' : 'new secret', title: sec ? `Set ${esc(sec.name)}` : 'Add a secret', wide: true,
      values: sec ? { name: sec.name, mode: 'store', vault: sec.vault } : { mode: 'store' }, edit: !!sec,
      fields: [{ k: 'name', type: 'string', req: true, readonly: !!sec, ph: 'e.g. PD_TOKEN', validate: (x) => (!sec && D.secrets.find((s) => s.name === x) ? 'A secret by that name exists.' : '') },
        sec ? null : { k: 'mode', type: 'seg', options: [['store', 'Store a value here'], ['ref', 'Reference a vault item']], def: 'store' },
        { k: 'vault', type: 'enum', req: true, label: 'Store in', options: writable.map((v) => [v.name, `${v.name} (${v.type})`]).concat(D.vaults.filter((v) => !writable.includes(v)).map((v) => [v.name + '!', `${v.name} · ${v.status === 'fail' ? 'unreachable' : 'read-only'}`])), def: (writable[0] || {}).name, show: (v) => (v.mode || 'store') === 'store', validate: (x) => (String(x).endsWith('!') ? 'That vault can\'t be written right now.' : '') },
        { k: 'value', type: 'text', req: true, label: 'Value · write-only', ph: 'paste the value', help: 'Sent once over TLS, written straight to the vault. There is no API that returns it: not to this page, not to an agent.', show: (v) => (v.mode || 'store') === 'store' },
        { k: 'ref', type: 'string', req: true, label: 'Reference', ph: 'op://Agents/Slack/bot-token', help: '1Password (op://), pass, a file vault, HashiCorp, or env:NAME. The resolve check asks the vault whether the item exists; the value is never fetched.', show: (v) => v.mode === 'ref', validate: (x) => (/^(op:\/\/|env:|[a-z][\w-]*\/)/.test(x) ? '' : 'Start with op://, env:, or <vault>/') },
      ].filter(Boolean),
      note: sec && sec.used.length ? `<b>Used by</b> ${sec.used.map((u) => `<span class="mono">${esc(u)}</span>`).join(', ')}. Those pick up the new value on the next reload.` : '',
      submit: sec ? 'Save with passkey' : 'Save', submitIcon: 'passkey',
      onSubmit: (v, form) => {
        if (v.mode === 'ref') { UI.closeModal(); UI.staged(S.stage('secrets', v.name, v.ref)); return false; }
        const len = (v.value || '').length;
        form.querySelector('textarea').value = ''; // the value leaves the page now
        UI.closeModal();
        UI.stepup({ title: `${sec ? 'Replace' : 'Store'} ${esc(v.name)}`, purpose: 'secret write', what: `{ purpose: <b>secret_write</b>, vault: <b>${esc(v.vault)}</b>, key: <b>${esc(v.name)}</b>, value_sha256: <b>${Math.random().toString(16).slice(2, 10)}…</b>, length: <b>${len}</b> }`, ok: 'Write with passkey' }).then((ok) => {
          if (!ok) return;
          const s = D.secrets.find((x) => x.name === v.name);
          if (s) { s.set = now(); s.rotated = now(); s.vault = v.vault; }
          else D.secrets.unshift({ name: v.name, ref: `${v.vault}/${v.name.toLowerCase().replace(/_/g, '-')}`, vault: v.vault, set: now(), used: [] });
          const vv = D.vaults.find((x) => x.name === v.vault); if (vv && vv.count != null && !s) vv.count++;
          D.audit.unshift({ ts: now(), event: 'secret_write', who: 'operator:ops', detail: `${v.vault}/${v.name} · length ${len} · value not recorded`, run: '' });
          S.save(); UI.rerender(); UI.toast(sec ? `Replaced ${esc(v.name)}` : `Stored ${esc(v.name)}`, `${v.vault} · audited without its value`, 'ok');
        });
        return false;
      } });
  }
  UI.act('secret-add', () => setModal(null));
  UI.act('secret-set', (b) => setModal(D.secrets.find((s) => s.name === b.dataset.s)));
  UI.act('secret-ref', (b) => { const s = D.secrets.find((x) => x.name === b.dataset.s); UI.form({ caps: `${esc(s.used[0] || 'secrets.' + s.name)}`, title: `Change ref for ${esc(s.name)}`, edit: true, values: { ref: S.staged('secrets', s.name) },
    fields: [{ k: 'ref', type: 'string', req: true, help: 'Conductor resolves it fresh when needed. Checked in the daemon: ok or an error, never the value.', validate: (x) => (/^(op:\/\/|env:|[a-z][\w-]*\/)/.test(x) ? '' : 'Start with op://, env:, or <vault>/') }], submit: 'Review change', submitIcon: 'check', onSubmit: (v) => UI.staged(S.stage('secrets', s.name, v.ref)) }); });
  UI.act('secret-del', (b) => { const s = D.secrets.find((x) => x.name === b.dataset.s); UI.stepup({ title: `Delete ${esc(s.name)}`, purpose: 'secret delete', danger: true, ok: 'Delete with passkey', what: `{ purpose: <b>secret_delete</b>, vault: <b>${esc(s.vault)}</b>, key: <b>${esc(s.name)}</b> }` }).then((ok) => { if (!ok) return; D.secrets = D.secrets.filter((x) => x !== s); D.audit.unshift({ ts: now(), event: 'secret_delete', who: 'operator:ops', detail: `${s.vault}/${s.name}`, run: '' }); S.save(); UI.rerender(); UI.toast(`Deleted ${esc(s.name)}`, 'removed from the vault', 'ok'); }); });

  function rows() {
    const list = D.secrets.filter(match);
    return list.map((s) => { const v = vaultOf(s); const pend = S.find('secrets', s.name); const ref = pend ? pend.after : s.ref;
      return `<tr><td data-l="name"><b class="mono" style="font-size:12px">${esc(s.name)}</b>${s.warn ? `<div class="tiny" style="color:var(--wait)">${icon('warn')} ${esc(s.warn)}</div>` : ''}${pend ? ` ${tag('ref staged', 'run')}` : ''}</td>
        <td data-l="value"><span class="wo">••••••••</span></td>
        <td data-l="source"><span class="row" style="gap:6px">${icon(VIC[(v || { type: 'env' }).type])}<span class="mono tiny">${esc(ref)}</span></span></td>
        <td data-l="used in" class="uses">${s.used.map((u) => `<a href="#/config/raw?at=${section(u)}">${esc(u)}</a>`).join('<br>') || '<span class="muted tiny">unused</span>'}</td>
        <td data-l="set" class="mono tiny muted">${s.set ? ago(s.set) + ' ago' : 'external'}</td>
        <td class="num nowrap">${s.set ? `<button class="btn sm" data-act="secret-set" data-s="${esc(s.name)}">${icon('updates')}Replace</button>` : `<button class="btn sm" data-act="secret-ref" data-s="${esc(s.name)}">${icon('link')}Change ref</button>`}
          <button class="icon-btn" ${s.used.length ? UI.off(`Still used by ${s.used.join(', ')}: remove those references first`) : s.set ? `data-act="secret-del" data-s="${esc(s.name)}"` : UI.off('Lives in an external vault: delete it there')} aria-label="Delete ${esc(s.name)}">${icon('trash')}</button></td></tr>`; }).join('') || `<tr class="empty-row"><td colspan="6">No secret matches. <button class="link" data-act="sec-clear">Clear filters</button></td></tr>`;
  }
  UI.route('secrets', () => {
    const counts = Object.fromEntries(Object.keys(IN).map((k) => [k, D.secrets.filter(IN[k]).length]));
    const pendSettings = D.pending.filter((c) => c.coll === 'settings' || c.coll === 'secrets' || c.coll === 'vaults').length;
    return `<div class="page">
    ${UI.head('Secrets &amp; vars', { sub: 'values are write-only', actions: `<button class="btn" data-act="vault-add">${icon('plus')}Add a vault</button><button class="btn primary" data-act="secret-add">${icon('key')}Add a secret</button>` })}
    <div class="grid g4" style="margin-bottom:18px">${D.vaults.map((v) => `<div class="panel stat vault-card${v.status === 'fail' ? ' bad' : ''}">
      <span class="k">${icon(VIC[v.type])} ${v.name} <span class="mono" style="text-transform:none;letter-spacing:0;margin-left:auto">${v.type}</span></span>
      <span class="row">${st(v.status === 'fail' ? 'fail' : 'ok', v.status === 'unlocked' ? 'unlocked' : v.status === 'fail' ? 'unreachable' : 'reachable')}${v.writable ? tag('writable') : tag('read-only')}${S.find('vaults', v.name) ? tag('staged', 'run') : ''}</span>
      <span class="d">${esc(v.detail)}</span><div class="row" style="gap:4px"><button class="btn sm ghost" data-act="vault-edit" data-v="${v.name}">${icon('edit')}Edit</button><button class="btn sm ghost" data-act="vault-del" data-v="${v.name}" aria-label="Remove vault ${v.name}">${icon('trash')}</button></div></div>`).join('')}</div>
    ${D.vaults.some((v) => v.status === 'fail') ? `<div class="callout danger small" style="margin-bottom:14px"><b>hcv is unreachable:</b> its unlock token (<span class="mono">env:VAULT_TOKEN</span>) expired 2h ago. Nothing references hcv yet, so no connector is affected. <button class="link" data-act="vault-edit" data-v="hcv">Update the unlock ref</button></div>` : ''}
    <div class="section-title"><h2>Secrets</h2><span class="caps">${D.secrets.length} known</span></div>
    <div class="filters"><div class="input-icon">${icon('search')}<input class="input" placeholder="name, reference, or where it's used" value="${esc(sv.q)}" data-on-input="sec-q" aria-label="Search secrets"></div>${UI.seg([['all', `all · ${counts.all}`], ['stored', `stored here · ${counts.stored}`], ['referenced', `referenced · ${counts.referenced}`], ['unused', `unused · ${counts.unused}`]], sv.f, 'sec-f')}</div>
    <div class="panel"><table class="tbl responsive"><thead><tr><th>name</th><th>value</th><th>source</th><th>used in</th><th>last set</th><th></th></tr></thead><tbody id="sec-body">${rows()}</tbody></table><div class="panel-foot small muted">${icon('eyeoff')} No screen, API route or log line returns a secret's value. Values are tainted and redacted from logs and the audit trail.</div></div>
    <div class="section-title"><h2>Settings</h2><span class="caps">\${settings.NAME} anywhere in config</span></div>
    <div class="panel"><table class="tbl responsive"><thead><tr><th>name</th><th>value</th><th class="num">used</th><th></th></tr></thead><tbody>
      ${D.settings.map((s) => { const p = S.find('settings', s.name); const v = p ? p.after : s.value; return `<tr${p && p.after == null ? ' class="row-removed"' : ''}><td data-l="name" class="mono"><b>${s.name}</b>${p ? ' ' + tag(p.after == null ? 'removal staged' : 'staged', 'run') : ''}</td><td data-l="value"><input class="input mono setting-in" value="${esc(v == null ? s.value : v)}" data-on-change="setting-edit" data-k="${s.name}" aria-label="Value of ${s.name}"> ${s.chains ? tag('chains') : ''}${s.env ? tag('from env') : ''}</td><td data-l="used" class="num mono"><a class="link" href="#/config/raw?at=settings">${s.used} refs</a></td><td class="num"><button class="icon-btn" ${s.used ? UI.off(`Used ${s.used} times in config: remove those references first`) : `data-act="setting-del" data-k="${s.name}"`} aria-label="Remove ${s.name}">${icon('trash')}</button></td></tr>`; }).join('')}
      ${D.pending.filter((c) => c.coll === 'settings' && c.before == null).map((c) => `<tr><td data-l="name" class="mono"><b>${c.key}</b> ${tag('new · staged', 'run')}</td><td data-l="value"><input class="input mono setting-in" value="${esc(c.after)}" data-on-change="setting-edit" data-k="${c.key}" aria-label="Value of ${c.key}"></td><td class="num mono">0 refs</td><td class="num"><button class="icon-btn" data-act="unstage-key" data-id="${c.id}" aria-label="Discard ${c.key}">${icon('trash')}</button></td></tr>`).join('')}
    </tbody></table><div class="panel-foot"><button class="btn sm" data-act="setting-add">${icon('plus')}Add setting</button><span class="muted small grow" style="margin-left:8px">Changes go through the config pipeline (validated on a copy).</span><a class="btn sm primary" href="#/config/apply" ${pendSettings ? '' : UI.off('Nothing staged here yet: edit a value first')}>Review${pendSettings ? ` ${pendSettings}` : ''}</a></div></div>
  </div>`;
  });
  const refresh = () => { UI.$('#sec-body').innerHTML = rows(); };
  UI.act('sec-q', (el) => { sv.q = el.value; refresh(); });
  UI.act('sec-f', (b) => { sv.f = b.dataset.v; UI.$$('[data-act="sec-f"]').forEach((x) => { x.classList.toggle('on', x === b); x.setAttribute('aria-pressed', x === b); }); refresh(); });
  UI.act('sec-clear', () => { sv.q = ''; sv.f = 'all'; UI.rerender(); });
  UI.act('setting-edit', (inp) => { const v = inp.value; S.stage('settings', inp.dataset.k, v); UI.rerender(); });
  UI.act('setting-del', (b) => { S.stage('settings', b.dataset.k, null); UI.rerender(); });
  UI.act('unstage-key', (b) => { S.unstage(b.dataset.id); UI.rerender(); });
  UI.act('setting-add', () => UI.form({ caps: 'settings:', title: 'Add a setting', fields: [{ k: 'name', type: 'string', req: true, pattern: /^[a-z_][a-z0-9_]*$/, patternMsg: 'lowercase snake_case', validate: (x) => (D.settings.find((s) => s.name === x) ? 'Exists already.' : '') }, { k: 'value', type: 'string', req: true, help: 'May chain: ${settings.org}/deploys, or read ${env.NAME}.' }], submit: 'Stage', onSubmit: (v) => { S.stage('settings', v.name, v.value); UI.rerender(); UI.refreshChrome(); } }));
})();
