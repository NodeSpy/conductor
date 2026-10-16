/* Secrets & vars: values are write-only. Stored in conductor's vault or referenced
 * from 1Password / pass / HashiCorp / file / env. Settings are editable. Both show where they're used. */
(function () {
  const { icon, st, tag, ago, esc } = UI;
  const VIC = { conductor: 'lock', onepassword: 'key', pass: 'key', hashicorp: 'server', file: 'file', env: 'terminal' };

  function setModal(sec) {
    const name = sec ? sec.name : '';
    UI.modal(`<div class="modal-head"><div><div class="caps">${sec ? 'rotate' : 'new secret'}</div><h2>${sec ? esc(name) : 'Add a secret'}</h2></div><button class="icon-btn" data-close>${icon('x')}</button></div>
      <div class="modal-body stack">
        <div class="seg" id="mode"><button class="on" data-v="store">Store a value</button><button data-v="ref">Reference a vault item</button></div>
        <div id="m-store" class="stack">
          <div class="grid g2"><div class="field"><label>Name</label><input class="input mono" value="${esc(name)}" placeholder="e.g. PD_TOKEN"></div><div class="field"><label>Store in</label><select class="select mono"><option>house (conductor vault)</option><option>store (pass)</option><option>hcv (HashiCorp KV v2) · token expired</option></select></div></div>
          <div class="field"><label>Value <span class="muted">· write-only</span></label><textarea class="textarea mono" rows="3" placeholder="paste the value" autocomplete="off" spellcheck="false" style="-webkit-text-security:disc"></textarea>
            <div class="hint">Sent once over TLS, written straight to the vault. There is no API that returns it: not to this page, not to an agent.</div></div>
        </div>
        <div id="m-ref" class="stack hide">
          <div class="field"><label>Reference</label><input class="input mono" value="op://Agents/Slack/bot-token"><div class="hint">1Password through <span class="mono">op</span>, <span class="mono">pass</span>, a file vault or HashiCorp. Conductor resolves it fresh when needed.</div></div>
          <div class="row small">${st('ok', 'resolves')}<span class="muted mono tiny">checked by asking the vault whether the item exists · value not fetched</span></div>
        </div>
        ${sec ? `<div class="callout small"><b>Used by</b> ${sec.used.map((u) => `<span class="mono">${u}</span>`).join(', ')}. Those connectors pick up the new value on the next reload (validated on a copy first).</div>` : ''}
      </div>
      <div class="modal-foot"><button class="btn ghost" data-close>Cancel</button><button class="btn primary" id="save-secret">${icon('passkey')}Save with passkey</button></div>`);
    UI.bind(document, '#mode button', 'click', (e, b) => { UI.$$('#mode button').forEach((x) => x.classList.toggle('on', x === b)); UI.$('#m-store').classList.toggle('hide', b.dataset.v !== 'store'); UI.$('#m-ref').classList.toggle('hide', b.dataset.v !== 'ref'); });
    UI.bind(document, '#save-secret', 'click', () => { UI.closeModal(); UI.toast(sec ? `Rotated ${name}` : 'Secret stored', 'house · audited without its value', 'ok'); });
  }

  UI.route('secrets', () => `<div class="page">
    ${UI.head('Secrets &amp; vars', { sub: 'values are write-only', actions: `<button class="btn" id="add-vault">${icon('plus')}Add a vault</button><button class="btn primary" id="add-secret">${icon('key')}Add a secret</button>` })}
    <div class="grid g4" style="margin-bottom:18px">${D.vaults.map((v) => `<div class="panel stat" ${v.status === 'fail' ? 'style="border-color:color-mix(in srgb,var(--fail) 45%,var(--line))"' : ''}>
      <span class="k">${icon(VIC[v.type])} ${v.name} <span class="mono" style="text-transform:none;letter-spacing:0;margin-left:auto">${v.type}</span></span>
      <span class="row">${st(v.status === 'fail' ? 'fail' : 'ok', v.status === 'unlocked' ? 'unlocked' : v.status === 'fail' ? 'unreachable' : 'reachable')}${v.writable ? tag('writable') : tag('read-only')}</span>
      <span class="d">${esc(v.detail)}</span></div>`).join('')}</div>
    ${D.vaults.some((v) => v.status === 'fail') ? `<div class="callout danger small" style="margin-bottom:14px"><b>hcv is unreachable:</b> its unlock token (<span class="mono">env:VAULT_TOKEN</span>) expired 2h ago. Nothing references hcv yet, so no connector is affected. <a style="color:var(--accent)" href="#" onclick="return false">Update the unlock ref</a></div>` : ''}
    <div class="section-title"><h2>Secrets</h2><span class="caps">${D.secrets.length} referenced by config</span></div>
    <div class="filters"><div class="input-icon">${icon('search')}<input class="input" placeholder="name, reference, or where it's used"></div><div class="seg"><button class="on">all</button><button>stored here</button><button>referenced</button><button>unused</button></div></div>
    <div class="panel"><table class="tbl responsive"><thead><tr><th>name</th><th>value</th><th>source</th><th>used in</th><th>last set</th><th></th></tr></thead><tbody>
      ${D.secrets.map((s) => `<tr><td data-l="name"><b class="mono" style="font-size:12px">${esc(s.name)}</b>${s.warn ? `<div class="tiny" style="color:var(--wait)">${icon('warn')} ${esc(s.warn)}</div>` : ''}</td>
        <td data-l="value"><span class="wo">••••••••</span></td>
        <td data-l="source"><span class="row" style="gap:6px">${icon(VIC[(D.vaults.find((v) => v.name === s.vault) || { type: 'env' }).type])}<span class="mono tiny">${esc(s.ref)}</span></span></td>
        <td data-l="used in" class="uses">${s.used.map((u) => `<a href="#/config/raw">${u}</a>`).join('<br>')}</td>
        <td data-l="set" class="mono tiny muted">${s.set ? ago(s.set) + ' ago' : 'external'}</td>
        <td class="num"><button class="btn sm" data-rotate="${esc(s.name)}">${s.set ? icon('updates') + 'Rotate' : icon('link') + 'Change ref'}</button></td></tr>`).join('')}
    </tbody></table><div class="panel-foot small muted">${icon('eyeoff')} No screen, API route or log line returns a secret's value. Values are tainted and redacted from logs and the audit trail.</div></div>
    <div class="section-title"><h2>Settings</h2><span class="caps">\${settings.NAME} anywhere in config</span></div>
    <div class="panel"><table class="tbl"><thead><tr><th>name</th><th>value</th><th class="num">used</th><th></th></tr></thead><tbody>
      ${D.settings.map((s) => `<tr><td class="mono"><b>${s.name}</b></td><td><input class="input mono" style="height:26px;max-width:340px" value="${esc(s.value)}"> ${s.chains ? tag('chains') : ''}${s.env ? tag('from env') : ''}</td><td class="num mono"><a style="border-bottom:1px dotted var(--line-3)" href="#/config/raw">${s.used} refs</a></td><td class="num"><button class="icon-btn">${icon('trash')}</button></td></tr>`).join('')}
    </tbody></table><div class="panel-foot"><button class="btn sm">${icon('plus')}Add setting</button><span class="muted small" style="margin-left:auto">Changes go through the config pipeline (validated on a copy).</span><a class="btn sm primary" href="#/config/apply">Review</a></div></div>
  </div>`, { after: (p, main) => {
    UI.bind(main, '#add-secret', 'click', () => setModal(null));
    UI.bind(main, '[data-rotate]', 'click', (e, b) => setModal(D.secrets.find((s) => s.name === b.dataset.rotate)));
    UI.bind(main, '#add-vault', 'click', () => UI.toast('Vaults are config', 'adding one opens the vaults: editor → config pipeline', 'run'));
  } });
})();
