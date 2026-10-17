/* Secrets & vars: values are write-only. A secret is an entry in a vault
 * (<vault>/<key>), referenced from config in one syntax:
 * {{ vault "<vault>" "<key>" }} (Secrets.md; op:// URIs and the named
 * secrets: block are retired). Vaults and settings are config (staged into the
 * pipeline); a value write goes straight to the vault after a step-up. */
(function () {
  const { icon, st, tag, ago, esc } = UI;
  const VIC = { conductor: 'lock', onepassword: 'key', pass: 'key', hashicorp: 'server', file: 'file', env: 'terminal' };
  const now = () => new Date().toISOString();
  const sv = { q: '', f: 'all' };
  const vaultOf = (s) => D.vaults.find((v) => v.name === s.vault);
  const IN = { all: () => true, stored: (s) => !!s.set, referenced: (s) => !s.set, unused: (s) => !s.used.length };
  const match = (s) => IN[sv.f](s) && (!sv.q || `${s.alias || ''} ${s.ref} ${s.used.join(' ')}`.toLowerCase().includes(sv.q.toLowerCase()));
  const section = (path) => path.split(/[.[]/)[0];

  // real connection keys per type (Secrets.md)
  const VT = {
    conductor: [{ k: 'path', type: 'string', ph: '~/.config/conductor/vault.json (default)' }, { k: 'unlock.key', type: 'string', label: 'unlock · key', ph: 'creds:house', help: 'creds:, keyring:, env:VAR or file:/path. Empty: the default chain ($CONDUCTOR_VAULT_KEY, systemd creds, keyring, vault.key).' }],
    onepassword: [{ k: 'account', type: 'string' }, { k: 'service_account', type: 'string', label: 'service account', ph: 'env:OP_SA', help: 'A bootstrap ref → OP_SERVICE_ACCOUNT_TOKEN.' }],
    pass: [{ k: 'prefix', type: 'string', def: 'conductor' }],
    file: [{ k: 'dir', type: 'string', req: true, ph: '/run/secrets', help: 'A mounted secrets directory (systemd LoadCredential, docker, k8s).' }],
    hashicorp: [{ k: 'addr', type: 'string', req: true, ph: 'https://vault.internal:8200' }, { k: 'mount', type: 'string', def: 'secret' }, { k: 'namespace', type: 'string' }, { k: 'unlock.token', type: 'string', req: true, label: 'unlock · token', ph: 'env:VAULT_TOKEN', help: 'The unlock token reference. A failing one raises a Needs-you item.' }],
  };
  function vaultForm(v) {
    const cur = v ? S.staged('vaults', v.name) : null;
    const type = cur ? cur.type : 'conductor';
    UI.form({ caps: v ? `vaults.${v.name}` : 'vaults:', title: v ? `Edit vault ${v.name}` : 'Add a vault', edit: !!v, values: cur ? Object.assign({ name: v.name, type: cur.type }, Object.fromEntries((VT[cur.type] || []).map((f) => [`${cur.type}__${f.k.replace('.', '_')}`, S.getPath(cur, f.k)]))) : {},
      fields: [{ k: 'name', type: 'string', req: true, readonly: !!v, pattern: /^[a-z][a-z0-9-]*$/, patternMsg: 'Lowercase letters, digits and dashes.', validate: (x) => (!v && D.vaults.find((y) => y.name === x) ? 'A vault by that name exists.' : '') },
        { k: 'type', type: 'seg', req: true, options: Object.keys(VT).map((t) => [t, t, v && t !== type ? 'Changing a vault\'s type is a new vault' : '']), def: type }]
        .concat(Object.entries(VT).flatMap(([t, fs]) => fs.map((f) => Object.assign({}, f, { k: `${t}__${f.k.replace('.', '_')}`, label: f.label || f.k, show: (vals) => (vals.type || type) === t })))),
      note: 'A vault is config: this stages a change, validated on a copy (the unlock ref must resolve) before it applies.',
      submit: 'Review change', submitIcon: 'check',
      onSubmit: (x) => { const n = { type: x.type || type }; (VT[n.type] || []).forEach((f) => { const val = x[`${n.type}__${f.k.replace('.', '_')}`]; if (val != null && val !== '') S.setPath(n, f.k, val); }); UI.staged(S.stage('vaults', x.name || v.name, n)); } });
  }
  UI.act('vault-add', () => vaultForm(null));
  UI.act('vault-edit', (b) => vaultForm(D.vaults.find((v) => v.name === b.dataset.v)));
  UI.act('vault-del', (b) => UI.confirm({ title: `Remove vault ${b.dataset.v}?`, body: 'Stages removing it from vaults:. Validation refuses it while a secret still references it.', ok: 'Stage removal', danger: true }).then((ok) => ok && UI.staged(S.stage('vaults', b.dataset.v, null))));

  function setModal(sec) {
    const writable = D.vaults.filter((v) => v.writable && v.status !== 'fail');
    UI.form({ caps: sec ? 'write-only · replace the value' : 'new vault entry', title: sec ? `Replace ${esc(sec.ref)}` : 'Add a secret', wide: true,
      values: sec ? { vault: sec.vault, key: sec.key, alias: sec.alias } : {}, edit: !!sec,
      intro: sec ? '' : 'A secret is a vault entry. Store a value in a writable vault here; entries in 1Password and other external vaults are referenced where they\'re used, with the Vault source of any field.',
      fields: [
        { k: 'vault', type: 'enum', req: true, label: 'Store in', readonly: !!sec, create: 'vault', createLabel: 'New vault', options: writable.map((v) => [v.name, `${v.name} (${v.type})`]), def: (writable[0] || {}).name },
        { k: 'key', type: 'string', req: true, readonly: !!sec, label: 'Key', ph: 'e.g. pagerduty-ui', pattern: /^[\w./-]+$/, patternMsg: 'letters, digits, . _ / -', validate: (x, vals) => (!sec && D.secrets.find((s) => s.ref === `${vals.vault}/${x}`) ? 'That entry exists: replace it instead.' : '') },
        { k: 'value', type: 'text', req: true, label: 'Value · write-only', ph: 'paste the value', help: 'Sent once over TLS, written straight to the vault. There is no API that returns it: not to this page, not to an agent.' },
        { k: 'alias', type: 'string', label: 'Alias (dashboard only)', ph: 'e.g. PD_TOKEN', help: 'A label for this list. Config never sees it: config writes {{ vault "&lt;vault&gt;" "&lt;key&gt;" }}.' },
      ],
      note: sec && sec.used.length ? `<b>Used by</b> ${sec.used.map((u) => `<span class="mono">${esc(u)}</span>`).join(', ')}. Those pick up the new value on the next reload.` : '',
      submit: sec ? 'Replace with passkey' : 'Store with passkey', submitIcon: 'passkey',
      onSubmit: (v, form) => {
        const len = (v.value || '').length, vault = v.vault || sec.vault, key = v.key || sec.key, ref = `${vault}/${key}`;
        form.querySelector('textarea').value = ''; // the value leaves the page now
        UI.closeModal();
        UI.stepup({ title: `${sec ? 'Replace' : 'Store'} ${esc(ref)}`, purpose: 'secret write', what: `{ purpose: <b>secret_write</b>, vault: <b>${esc(vault)}</b>, key: <b>${esc(key)}</b>, length: <b>${len}</b> }`, ok: 'Write with passkey' }).then((ok) => {
          if (!ok) return;
          const s = D.secrets.find((x) => x.ref === ref);
          if (s) { s.set = now(); s.rotated = now(); if (v.alias != null) s.alias = v.alias; }
          else D.secrets.unshift({ name: ref, ref, vault, key, alias: v.alias || '', set: now(), used: [] });
          const vv = D.vaults.find((x) => x.name === vault); if (vv && vv.count != null && !s) vv.count++;
          D.audit.unshift({ ts: now(), event: 'secret_write', who: 'operator:ops', detail: `${ref} · length ${len} · value not recorded`, run: '' });
          S.save(); UI.rerender(); UI.toast(sec ? `Replaced ${esc(ref)}` : `Stored ${esc(ref)}`, 'audited without its value', 'ok');
        });
        return false;
      } });
  }
  UI.act('secret-add', () => setModal(null));
  UI.act('secret-set', (b) => setModal(D.secrets.find((s) => s.ref === b.dataset.s)));
  UI.act('secret-ref', (b) => { const s = D.secrets.find((x) => x.ref === b.dataset.s); UI.form({ caps: s.used.join(', ') || s.ref, title: `Point uses of ${esc(s.ref)} elsewhere`, edit: true, values: { ref: S.staged('secrets', s.ref) },
    intro: `Rewrites every place that uses this entry (${s.used.length || 'none'}) to another one, as {{ vault "&lt;vault&gt;" "&lt;key&gt;" }}.`,
    fields: [{ k: 'ref', type: 'string', req: true, label: 'New entry (<vault>/<key>)', ph: 'op/Agents/Slack/bot-token-2', help: 'Checked by the daemon: ok or an error, never the value.', validate: (x) => (/^[a-z][\w-]*\/.+/.test(x) ? (D.vaults.some((v) => v.name === x.split('/')[0]) ? '' : `no vault "${x.split('/')[0]}"`) : '<vault>/<key>') }], submit: 'Review change', submitIcon: 'check', onSubmit: (v) => UI.staged(S.stage('secrets', s.ref, v.ref)) }); });
  UI.act('secret-del', (b) => { const s = D.secrets.find((x) => x.ref === b.dataset.s); UI.stepup({ title: `Delete ${esc(s.ref)}`, purpose: 'secret delete', danger: true, ok: 'Delete with passkey', what: `{ purpose: <b>secret_delete</b>, vault: <b>${esc(s.vault)}</b>, key: <b>${esc(s.key)}</b> }` }).then((ok) => { if (!ok) return; D.secrets = D.secrets.filter((x) => x !== s); D.audit.unshift({ ts: now(), event: 'secret_delete', who: 'operator:ops', detail: s.ref, run: '' }); S.save(); UI.rerender(); UI.toast(`Deleted ${esc(s.ref)}`, 'removed from the vault', 'ok'); }); });

  function rows() {
    const list = D.secrets.filter(match);
    return list.map((s) => { const v = vaultOf(s); const pend = S.find('secrets', s.ref); const ref = pend ? pend.after : s.ref; const [vv, ...kk] = ref.split('/');
      return `<tr data-secret="${esc(s.ref)}"><td data-l="entry"><span class="row" style="gap:6px">${icon(VIC[(v || { type: 'env' }).type])}<b class="mono" style="font-size:12px">${esc(vv)} · ${esc(kk.join('/'))}</b></span><div class="mono tiny muted vexpr">{{ vault "${esc(vv)}" "${esc(kk.join('/'))}" }}</div>${s.warn ? `<div class="tiny" style="color:var(--wait)">${icon('warn')} ${esc(s.warn)}</div>` : ''}${pend ? ` ${tag('uses repointed · staged', 'run')}` : ''}</td>
        <td data-l="alias">${s.alias ? `<span class="mono tiny">${esc(s.alias)}</span> <span class="muted tiny">dashboard only</span>` : '<span class="muted tiny">—</span>'}</td>
        <td data-l="value"><span class="wo">••••••••</span></td>
        <td data-l="used in" class="uses">${s.used.map((u) => `<a href="#/config/raw?at=${section(u)}">${esc(u)}</a>`).join('<br>') || '<span class="muted tiny">unused</span>'}</td>
        <td data-l="set" class="mono tiny muted">${s.set ? ago(s.set) + ' ago' : v && v.writable === false ? 'external' : '—'}</td>
        <td class="num nowrap">${v && v.writable !== false && v.status !== 'fail' ? `<button class="btn sm" data-act="secret-set" data-s="${esc(s.ref)}">${icon('updates')}Replace</button>` : ''} <button class="btn sm ghost" data-act="secret-ref" data-s="${esc(s.ref)}" ${s.used.length ? '' : UI.off('Nothing uses it yet')}>${icon('link')}Repoint uses</button>
          <button class="icon-btn" ${s.used.length ? UI.off(`Still used by ${s.used.join(', ')}: repoint those first`) : s.set ? `data-act="secret-del" data-s="${esc(s.ref)}"` : UI.off('Lives in an external vault: delete it there')} aria-label="Delete ${esc(s.ref)}">${icon('trash')}</button></td></tr>`; }).join('') || `<tr class="empty-row"><td colspan="6">No secret matches. <button class="link" data-act="sec-clear">Clear the filter</button></td></tr>`;
  }
  UI.route('secrets', () => {
    const counts = Object.fromEntries(Object.keys(IN).map((k) => [k, D.secrets.filter(IN[k]).length]));
    const pendSettings = D.pending.filter((c) => c.coll === 'settings' || c.coll === 'secrets' || c.coll === 'vaults').length;
    return `<div class="page">
    ${UI.head('Secrets &amp; vars', { sub: 'values are write-only', actions: `<button class="btn" data-act="vault-add">${icon('plus')}Add a vault</button><button class="btn primary" data-act="secret-add">${icon('key')}Add a secret</button>` })}
    ${UI.tabs([['main', 'Secrets, vaults &amp; settings', '#/secrets'], ['env', 'Env', '#/secrets/env', (S.view().env || []).length]], 'main')}
    <div class="grid g4" style="margin-bottom:18px">${D.vaults.map((v) => `<div class="panel stat vault-card${v.status === 'fail' ? ' bad' : ''}">
      <span class="k">${icon(VIC[v.type])} ${v.name} <span class="mono" style="text-transform:none;letter-spacing:0;margin-left:auto">${v.type}</span></span>
      <span class="row">${st(v.status === 'fail' ? 'fail' : 'ok', v.status === 'unlocked' ? 'unlocked' : v.status === 'fail' ? 'unreachable' : 'reachable')}${v.writable ? tag('writable') : tag('read-only')}${S.find('vaults', v.name) ? tag('staged', 'run') : ''}</span>
      <span class="d">${esc(v.detail)}</span><div class="row" style="gap:4px"><button class="btn sm ghost" data-act="vault-edit" data-v="${v.name}">${icon('edit')}Edit</button><button class="btn sm ghost" data-act="vault-del" data-v="${v.name}" aria-label="Remove vault ${v.name}">${icon('trash')}</button></div></div>`).join('')}</div>
    ${D.vaults.some((v) => v.status === 'fail') ? `<div class="callout danger small" style="margin-bottom:14px"><b>hcv is unreachable:</b> its unlock token (<span class="mono">unlock.token: \${env.VAULT_TOKEN}</span>) expired 2h ago. Nothing references hcv yet, so no connector is affected. <button class="link" data-act="vault-edit" data-v="hcv">Update the unlock ref</button></div>` : ''}
    <div class="section-title"><h2>Secrets</h2><span class="caps">${D.secrets.length} known</span></div>
    <div class="filters"><div class="input-icon">${icon('search')}<input class="input" placeholder="vault, key, alias, or where it's used" value="${esc(sv.q)}" data-on-input="sec-q" aria-label="Search secrets"></div>${UI.seg([['all', `all · ${counts.all}`], ['stored', `stored here · ${counts.stored}`], ['referenced', `referenced · ${counts.referenced}`], ['unused', `unused · ${counts.unused}`]], sv.f, 'sec-f')}</div>
    <div class="panel"><table class="tbl responsive"><thead><tr><th>entry · reference</th><th>alias</th><th>value</th><th>used in</th><th>last set</th><th></th></tr></thead><tbody id="sec-body">${rows()}</tbody></table><div class="panel-foot small muted">${icon('eyeoff')} No screen, API route or log line returns a secret's value. Values are tainted and redacted from logs and the audit trail.</div></div>
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
