/* Secrets & vars: values are write-only. A secret is an entry in a vault
 * (<vault>/<key>), referenced from config in one syntax:
 * {{ vault "<vault>" "<key>" }} (Secrets.md; op:// URIs and the named
 * secrets: block are retired). Vaults and settings are config (staged into the
 * pipeline); a value write goes straight to the vault after a step-up. */
(function () {
  const { icon, st, tag, ago, esc } = UI;
  const VIC = { conductor: 'lock', onepassword: 'key', pass: 'key', hashicorp: 'server', file: 'file', env: 'terminal' };
  const now = () => new Date().toISOString();
  const sv = { q: '', vault: [], kept: [], used: [] };
  // the Settings tab's filter (52): one live search and filter bar across settings and environment variables
  const stv = UI.settingsView = { q: '', kind: [], scope: [], used: [] };
  UI.settingKeep = (kind, name, text, used, scope) => (!stv.kind.length || stv.kind.includes(kind)) && (!stv.used.length || stv.used.includes(used ? 'used' : 'unused'))
    && (!stv.scope.length || kind !== 'env' || stv.scope.includes(String(scope || '').split(':')[0])) && (!stv.q || `${name} ${text || ''}`.toLowerCase().includes(stv.q.toLowerCase()));
  const vaultOf = (s) => D.vaults.find((v) => v.name === s.vault);
  const IN = { all: () => true, stored: (s) => !!s.set, referenced: (s) => !s.set, unused: (s) => !s.used.length };
  const match = (s) => (!sv.vault.length || sv.vault.includes(s.vault)) && (!sv.kept.length || sv.kept.some((k) => (k === 'here' ? IN.stored(s) : IN.referenced(s)))) && (!sv.used.length || sv.used.some((k) => (k === 'used' ? s.used.length : IN.unused(s))))
    && (!sv.q || `${s.alias || ''} ${s.label || ''} ${s.ref} ${s.used.join(' ')} ${s.used.map((u) => useWords(u)).join(' ')}`.toLowerCase().includes(sv.q.toLowerCase()));
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

  /* one line per secret: its name (the key) and where it's stored, what uses it in words, when it changed (or that its
     own vault manages it), and whether it's unused or stale; the copyable reference, the YAML paths and the actions are
     one click away (the row opens; the ⋯ menu acts). No value column: values never come back to the browser. */
  const VNAME = { conductor: 'conductor vault', onepassword: '1Password', pass: 'pass', hashicorp: 'HashiCorp Vault', file: 'mounted files', env: 'environment' };
  const STALE = 180 * 1440; // minutes: half a year without a change
  const keyOf = (ref) => { const [vv, ...kk] = String(ref).replace(/^op:\/\//, 'op/').split('/'); return [vv, kk.join('/')]; };
  const useWords = (u) => { let m;
    if ((m = u.match(/^connectors\.([\w-]+)\.(.+)$/))) return `${m[1]} connection · ${m[2].replace(/[._]/g, ' ')}`;
    if ((m = u.match(/^packs\.([\w-]+)\.secrets\.(.+)$/))) return `${m[1]} pack · ${m[2].replace(/_/g, ' ')}`;
    if ((m = u.match(/^browser\.credentials\.([\w-]+)\.(.+)$/))) return `${m[1]} login · ${m[2].replace(/_/g, ' ')}`;
    if ((m = u.match(/^runtimes\.([\w-]+)\.(.+)$/))) return `${m[1]} runtime · ${m[2].replace(/[._]/g, ' ')}`;
    return u; };
  const ov = { open: null };
  /* ---------- secrets and settings: one system, two tabs ----------
   * Secrets (write-only, stored in a vault, never shown) and settings (plain values, visible in config) share one row:
   * a readable name with the exact reference under it (and a Copy button), what uses it in words, its value or where it
   * lives, and one ⋯ menu (Change the value… · Copy the reference · Delete…). The page opens with the difference
   * between them in two sentences, on both tabs. Vaults matter only to secrets: they sit at the foot of Secrets,
   * always open, with their own Add a vault. */
  const ACR = { gh: 'GitHub', pd: 'PagerDuty', otp: 'one-time password', api: 'API', ui: 'UI', sa: 'service account' };
  /** A secret's name for a person: its label if it has one, else its key in words ("App · one-time password"). */
  const readable = (s) => { const parts = String(s.key || keyOf(s.ref)[1]).replace(/\?.*$/, '').split('/').filter(Boolean);
    const vault = vaultOf(s) || {}; if (parts.length > 1 && (parts[0] === 'Agents' || parts[0] === ((vault.cfg || {}).prefix) || parts[0] === 'conductor')) parts.shift();
    const words = (x) => x.split(/[-_ ]+/).map((w) => ACR[w.toLowerCase()] || w).join(' ');
    const out = parts.slice(-2).map(words).join(' · '); return out.charAt(0).toUpperCase() + out.slice(1); };
  const refOf = (s) => { const [vv, kk] = keyOf(s.ref); return `{{ vault "${vv}" "${kk}" }}`; };
  const copyBtn = (v, what) => `<button class="btn sm ghost sec-copy" data-act="sec-copy" data-v="${esc(v)}" aria-label="Copy the reference to ${esc(what)}">${icon('copy')}Copy</button>`;
  /** The shared explanation: what each is, and how a field uses it (with the exact syntax to copy). */
  // each tab says what its own kind is, in one line (50)
  const explain = (tab) => (tab === 'settings'
    ? `<p class="sec-explain">${icon('settings')}<span><b>A setting</b> is a plain value your config reads, visible to anyone who can read the config: <code class="mono">\${settings.org}</code>, or an environment variable as <code class="mono">\${env.NAME}</code>.</span></p>`
    : `<p class="sec-explain">${icon('lock')}<span><b>A secret</b> is write-only: stored in a vault and never shown again, not here and not to an agent. A field uses it as <code class="mono">{{ vault "house" "gh-app-key" }}</code>.</span></p>`);
  /** One row, the same for both: name and exact reference · used by · value or where it lives · ⋯ */
  const row = ({ id, name, ref, uses, value, note, menu, open, extra }) => `<li class="sec-row${open ? ' open' : ''}${!uses.length ? ' unused' : ''}" data-secret="${esc(id)}">
    <div class="sec-line">${extra ? `<button type="button" class="sec-main" data-act="sec-open" data-s="${esc(id)}" aria-expanded="${!!open}">` : '<div class="sec-main">'}<span class="sec-name"><b>${esc(name)}</b><span class="sec-ref mono">${esc(ref)}</span></span>
      <span class="sec-uses">${uses.length ? `<span class="dl-sub">Used by</span> ${uses.map(esc).join(', ')}` : UI.label('Unused')}</span>
      <span class="sec-when">${value}${note || ''}</span>${extra ? '</button>' : '</div>'}${copyBtn(ref, name)}
      <button class="icon-btn" data-act="${menu}" data-s="${esc(id)}" aria-label="More for ${esc(name)}">${icon('more')}</button></div>${open && extra ? `<div class="sec-detail">${extra}</div>` : ''}</li>`;
  function rows() {
    const list = D.secrets.filter(match);
    return list.map((s) => { const v = vaultOf(s) || { type: 'env', name: s.vault }, pend = S.find('secrets', s.ref), age = s.set ? (Date.now() - new Date(s.set)) / 60000 : 0, stale = s.set && age > STALE;
      const where = s.set ? `<span class="dl-sub">${icon(VIC[v.type])}${esc(VNAME[v.type] || v.name)} · changed ${UI.agoText(s.set)}</span>` : `<span class="dl-sub">${icon(VIC[v.type])}kept in ${esc(VNAME[v.type] || v.name)}</span>`;
      return row({ id: s.ref, name: s.label || readable(s), ref: refOf(pend ? { ref: pend.after } : s), uses: s.used.map(useWords), value: where, menu: 'sec-more', open: ov.open === s.ref,
        note: `${stale ? ' ' + UI.statusMark('wait', `not changed in ${Math.round(age / 43200)} months`) : ''}${pend ? ` ${UI.statusMark('staged', 'Uses repointed, not applied')}` : ''}${s.warn ? ` ${UI.statusMark('wait', esc(s.warn))}` : ''}`,
        extra: `<dl class="dl-facts"><dt>Used by</dt><dd>${s.used.map((u) => `<a class="dl-link" href="#/config/raw?at=${section(u)}">${esc(useWords(u))}</a> <span class="mono dl-sub">${esc(u)}</span>`).join('<br>') || 'Nothing uses it yet.'}</dd>
          <dt>Stored in</dt><dd>${esc(VNAME[v.type] || v.name)} <span class="dl-sub">(the vault “${esc(v.name)}”)</span>${v.writable === false ? ' · its value is set there, not here' : ''}</dd>${s.alias ? `<dt>Also known as</dt><dd><span class="mono">${esc(s.alias)}</span> <span class="dl-sub">a label for this list; config never uses it</span></dd>` : ''}</dl>` }); }).join('') || `<li class="dl-empty-row">No secret matches. <button class="link" data-act="sec-clear">Clear the filter</button></li>`;
  }
  /** How a vault is unlocked, in words, whatever shape its config has (a ref string, or { key } / { token }). */
  const unlockWords = (v) => { const u = (v.cfg || {}).unlock, ref = typeof u === 'string' ? u : u && (u.key || u.token) || '';
    const m = String(ref).match(/^(\w+):(.*)$/); if (!ref) return v.type === 'conductor' ? 'the default unlock chain' : '';
    if (!m) return `the reference ${ref}`; const [, kind, rest] = m;
    return kind === 'creds' ? `the systemd credential “${rest || v.name}”` : kind === 'keyring' ? `the OS keyring${rest ? ` (${rest})` : ''}` : kind === 'env' ? `the ${rest} environment variable` : kind === 'file' ? `the file ${rest}` : ref; };
  const vaultsHTML = () => `<section class="vaults" id="vaults"><div class="section-title"><h2>Where they’re stored</h2><span class="caps">${D.vaults.length} vaults${D.vaults.some((v) => v.status === 'fail') ? ' · one can’t be reached' : ', all reachable'}</span><button class="btn sm secondary vaults-add" data-act="vault-add">${icon('plus')}Add a vault</button></div>
    <div class="grid g4">${D.vaults.map((v) => { const how = unlockWords(v); return `<div class="panel stat vault-card${v.status === 'fail' ? ' bad' : ''}" id="vault-${esc(v.name)}">
      <span class="k">${icon(VIC[v.type])} ${esc(v.name)} <span class="dl-sub" style="text-transform:none;letter-spacing:0;margin-left:auto">${esc(VNAME[v.type] || v.type)}</span></span>
      <span class="row">${v.status === 'fail' ? UI.statusMark('fail', 'Can’t be reached') : UI.statusMark('ok', v.status === 'unlocked' ? 'Unlocked' : 'Reachable')}${S.find('vaults', v.name) ? ' ' + UI.statusMark('staged', 'Changed, not applied') : ''}</span>
      <span class="d">${v.writable ? 'conductor can store values here' : 'values are set in the vault itself'}${how ? `; unlocked with ${esc(how)}` : ''}.</span>
      <div class="card-acts"><button class="btn sm ghost" data-act="vault-edit" data-v="${esc(v.name)}">${icon('edit')}Edit</button><button class="btn sm ghost" data-act="vault-del" data-v="${esc(v.name)}" aria-label="Remove vault ${esc(v.name)}">${icon('trash')}</button></div></div>`; }).join('')}</div></section>`;
  const secBar = () => UI.filterBar({ id: 'sec', state: sv, placeholder: 'Find a secret by name, vault or what uses it', label: 'Find and filter secrets', fields: [
    { k: 'vault', label: 'Vault', multi: true, options: () => D.vaults.map((v) => ({ v: v.name, label: `${v.name} · ${VNAME[v.type] || v.type}`, n: D.secrets.filter((s) => s.vault === v.name).length })) },
    { k: 'kept', label: 'Stored', multi: true, keepOrder: true, title: 'Where its value is kept', options: () => [{ v: 'here', label: 'Stored by conductor', n: D.secrets.filter(IN.stored).length }, { v: 'external', label: 'Kept in its own vault', n: D.secrets.filter(IN.referenced).length }] },
    { k: 'used', label: 'Used', multi: true, keepOrder: true, options: () => [{ v: 'used', label: 'Used', n: D.secrets.filter((s) => s.used.length).length }, { v: 'unused', label: 'Unused', n: D.secrets.filter(IN.unused).length }] }], onChange: () => refresh() });
  const failBanner = () => { const bad = D.vaults.find((v) => v.status === 'fail'); if (!bad) return ''; const how = unlockWords(bad);
    return `<div class="callout danger small" style="margin-bottom:14px">${UI.statusMark('fail', '')}<span class="grow"><b>${esc(bad.name)} can’t be reached:</b> ${how ? `${esc(how)}, which unlocks it, ` : 'its unlock '}expired 2 h ago. Nothing uses ${esc(bad.name)} yet, so no connection is affected.</span><a class="btn sm secondary" href="#/secrets?show=vault-${esc(bad.name)}">See the vault</a></div>`; };
  const tabsHTML = (on) => UI.tabs([['secrets', 'Secrets', '#/secrets', D.secrets.length], ['settings', 'Settings', '#/secrets/settings', D.settings.length + (S.view().env || []).length]], on);
  UI.route('secrets', () => `<div class="page dl-page">
    <div class="dl-phead"><div><h1>Secrets</h1></div><div class="dl-actions tight"><button class="btn primary" data-act="secret-add">${icon('key')}Add a secret</button></div></div>
    ${tabsHTML('secrets')}
    ${explain('secrets')}
    ${failBanner()}
    ${secBar()}
    <div class="panel"><ul class="dl-list sec-list" id="sec-body">${rows()}</ul><div class="panel-foot small muted">${icon('eyeoff')} No screen, API route or log line returns a secret’s value; values are redacted from logs and the audit trail.</div></div>
    ${vaultsHTML()}
  </div>`, { nav: 'secrets' });
  /* ---------- the Settings tab: plain values, in the same rows ---------- */

  const settingRows = () => { const pend = (k) => S.find('settings', k);
    return D.settings.concat(D.pending.filter((c) => c.coll === 'settings' && c.before == null).map((c) => ({ name: c.key, value: c.after, used: 0, isNew: true }))).filter((x) => UI.settingKeep('setting', x.name, String(x.value), x.used)).map((x) => { const p = pend(x.name), v = p ? p.after : x.value;
      return row({ id: 'settings.' + x.name, name: x.name.replace(/_/g, ' ').replace(/^./, (c) => c.toUpperCase()), ref: `\${settings.${x.name}}`, uses: x.used ? [`${x.used} place${x.used === 1 ? '' : 's'} in config`] : [], menu: 'setting-more', open: false,
        value: p && p.after == null ? UI.statusMark('staged', 'Removal not applied') : `<span class="mono sec-val">${esc(v)}</span>${p || x.isNew ? ' ' + UI.statusMark('staged', 'Changed, not applied') : ''}${x.chains ? ' <span class="dl-sub">· built from another setting</span>' : ''}${x.env ? ' <span class="dl-sub">· from an environment variable</span>' : ''}` }); }).join(''); };
  const setBar = () => { const env = S.view().env || [];
    return UI.filterBar({ id: 'set', state: stv, placeholder: 'Find a setting or a variable by name or value', label: 'Find and filter settings and environment variables', fields: [
      { k: 'kind', label: 'Kind', multi: true, keepOrder: true, options: () => [{ v: 'setting', label: 'Settings', n: D.settings.length }, { v: 'env', label: 'Environment variables', n: env.length }] },
      { k: 'scope', label: 'For', multi: true, keepOrder: true, title: 'Who an environment variable is for', options: () => [['daemon', 'conductor itself'], ['runtime', 'A runtime’s agents'], ['host', 'A machine’s commands']].map(([v, label]) => ({ v, label, n: env.filter((e) => e.scope.split(':')[0] === v).length })).filter((o) => o.n) },
      { k: 'used', label: 'Used', multi: true, keepOrder: true, title: 'Whether the config uses it', options: () => [{ v: 'used', label: 'Used in config' }, { v: 'unused', label: 'Not used yet' }] }],
      onChange: () => UI.rerender({ keepScroll: true }) }); };
  UI.route('secrets/settings', () => { const pendN = D.pending.filter((c) => c.coll === 'settings' || c.coll === 'env').length, rowsS = settingRows();
    return `<div class="page dl-page">
    <div class="dl-phead"><div><h1>Secrets</h1></div></div>
    ${tabsHTML('settings')}
    ${explain('settings')}
    ${setBar()}
    ${pendN ? `<div class="set-review" role="status"><span><b>${pendN} change${pendN > 1 ? 's' : ''}</b>, not applied yet. Each is checked on a copy of the config before it applies.</span><a class="btn sm primary" href="#/config/apply">Review ${pendN}</a></div>` : ''}
    ${!stv.kind.length || stv.kind.includes('setting') ? `<section id="settings"><div class="section-title"><h2>Settings</h2><span class="caps">read in config as \${settings.NAME}</span><button class="btn sm secondary vaults-add" data-act="setting-add">${icon('plus')}Add a setting</button></div>
    <div class="panel"><ul class="dl-list sec-list">${rowsS || '<li class="dl-empty-row dl-sub">No setting matches.</li>'}</ul></div></section>` : ''}
    ${UI.envSectionHTML && (!stv.kind.length || stv.kind.includes('env')) ? UI.envSectionHTML(row) : ''}
  </div>`; }, { nav: 'secrets' });
  UI.act('sec-open', (b) => { ov.open = ov.open === b.dataset.s ? null : b.dataset.s; const el = UI.$('#sec-body'); if (el) el.innerHTML = rows(); });
  // copied: the button says so where you clicked (a menu item, which closes, says it in a toast)
  UI.act('sec-copy', (b) => { const inMenu = !!b.closest('#pop-menu'); UI.closeMenu(); try { const w = navigator.clipboard && navigator.clipboard.writeText(b.dataset.v); if (w && w.catch) w.catch(() => {}); } catch (e) {}
    if (inMenu) { UI.toast('Copied', `<span class="mono">${esc(b.dataset.v)}</span>`, 'ok'); return; } b.dataset.copied = Date.now(); if (b.dataset.was) return; b.dataset.was = b.innerHTML; b.innerHTML = `${icon('check')}Copied`; setTimeout(() => { if (b.isConnected) { b.innerHTML = b.dataset.was; delete b.dataset.was; } }, 1600); });
  // the same menu as a setting's: change the value, copy the reference, delete (a secret can also repoint its uses)
  UI.act('sec-more', (b) => { const s = D.secrets.find((x) => x.ref === b.dataset.s), v = vaultOf(s) || {};
    UI.menu(b, [['Change the value…', 'edit', 'secret-set', v.writable !== false && v.status !== 'fail' && s.vault !== 'env' ? `data-s="${esc(s.ref)}"` : UI.off(`Its value is set in ${VNAME[v.type] || v.name}`)], ['Copy the reference', 'copy', 'sec-copy', `data-v="${esc(refOf(s))}"`], ['Point its uses at another secret…', 'link', 'secret-ref', s.used.length ? `data-s="${esc(s.ref)}"` : UI.off('Nothing uses it yet')], ['-'], ['Delete…', 'trash', 'secret-del', s.used.length ? UI.off(`Still used by ${s.used.map(useWords).join(', ')}: point those elsewhere first`) : s.set ? `data-s="${esc(s.ref)}"` : UI.off(`It lives in ${VNAME[v.type] || v.name}: delete it there`)]]); });
  UI.act('setting-more', (b) => { const k = b.dataset.s.replace(/^settings\./, '');
    UI.menu(b, [['Change the value…', 'edit', 'setting-change', `data-k="${esc(k)}"`], ['Copy the reference', 'copy', 'sec-copy', `data-v="\${settings.${esc(k)}}"`], ['-'], ['Delete…', 'trash', 'setting-del', `data-k="${esc(k)}"`]]); });
  UI.act('setting-change', (b) => { const k = b.dataset.k, cur = S.find('settings', k), x = D.settings.find((y) => y.name === k) || {};
    UI.form({ caps: `\${settings.${esc(k)}}`, title: `Change ${esc(k)}`, edit: true, values: { value: cur ? cur.after : x.value }, fields: [{ k: 'value', type: 'string', req: true, help: 'May build on another: ${settings.org}/deploys, or read ${env.NAME}.' }], submit: 'Stage', onSubmit: (v) => { S.stage('settings', k, v.value); UI.rerender(); UI.refreshChrome(); } }); });
  const refresh = () => { UI.$('#sec-body').innerHTML = rows(); };
  UI.act('sec-clear', () => { Object.assign(sv, { q: '', vault: [], kept: [], used: [] }); UI.rerender(); });
  UI.act('setting-del', (b) => UI.confirm({ title: `Delete the setting ${esc(b.dataset.k)}?`, body: `Anything reading <span class="mono">\${settings.${esc(b.dataset.k)}}</span> would get nothing. This stages the change; it’s checked before it applies.`, ok: 'Stage removal', danger: true }).then((ok) => { if (ok) { S.stage('settings', b.dataset.k, null); UI.rerender(); UI.refreshChrome(); } }));
  UI.act('unstage-key', (b) => { S.unstage(b.dataset.id); UI.rerender(); });
  UI.act('setting-add', () => UI.form({ caps: 'settings:', title: 'Add a setting', fields: [{ k: 'name', type: 'string', req: true, pattern: /^[a-z_][a-z0-9_]*$/, patternMsg: 'lowercase snake_case', validate: (x) => (D.settings.find((s) => s.name === x) ? 'Exists already.' : '') }, { k: 'value', type: 'string', req: true, help: 'May chain: ${settings.org}/deploys, or read ${env.NAME}.' }], submit: 'Stage', onSubmit: (v) => { S.stage('settings', v.name, v.value); UI.rerender(); UI.refreshChrome(); } }));
})();
