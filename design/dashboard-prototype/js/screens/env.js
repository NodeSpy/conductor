/* Secrets & vars → Env: environment variables by scope, where each one is used, and the effective environment
 * a step or a bot worker would get. Scopes, from widest to narrowest (the narrowest wins):
 *   daemon      conductor.env, a plain-text file read at boot (applying re-execs through the boot guard)
 *   runtime:<n> runtimes.<n>.env, for every agent on that runtime
 *   host:<n>    hosts.<n>.env, for steps that run over SSH there
 *   workflow:<n> the trigger's env, for its steps
 * Values are never shown when they look secret; a secret-looking plain value is flagged with a one-click move
 * into conductor's vault. The sandbox passes agents the runtime/host/workflow env, and from the daemon only
 * the names on sandbox.env_allow (TZ, LANG and anything you list). */
(function () {
  const { icon, esc, tag } = UI;
  const ORDER = ['daemon', 'runtime', 'host', 'workflow'];
  const scopeKind = (s) => s.split(':')[0];
  const scopeLabel = (s) => (s === 'daemon' ? 'conductor.env' : s.replace(':', ' '));
  const SECRETISH = /TOKEN|SECRET|PASSWORD|PASSWD|KEY|CREDENTIAL/;
  const looksSecret = (e) => e.looksSecret || (e.plain && SECRETISH.test(e.name));
  const shown = (e) => (looksSecret(e) && e.plain ? `<span class="masked-v">${'•'.repeat(Math.min(e.len || 10, 12))}</span>` : esc(e.value));
  const usesOf = (name) => { const t = S.configText(), out = []; t.split('\n').forEach((l, i) => { if (l.includes('${env.' + name + '}')) out.push(i + 1); }); return out; };
  const allow = () => ((S.view().sandboxCfg || {}).env_allow || ['TZ', 'LANG']);

  /** The effective env for a context: [{ name, scope, shadowed: [scopes], passes: bool, ref }] — names and sources only. */
  UI.effectiveEnv = (ctx = {}) => { const env = S.view().env || [], want = { daemon: true, runtime: ctx.runtime, host: ctx.host, workflow: ctx.workflow }, by = {};
    env.filter((e) => { const k = scopeKind(e.scope), n = e.scope.split(':')[1]; return k === 'daemon' || (want[k] && want[k] === n); })
      .sort((a, b) => ORDER.indexOf(scopeKind(a.scope)) - ORDER.indexOf(scopeKind(b.scope)))
      .forEach((e) => { const prev = by[e.name]; by[e.name] = { name: e.name, scope: e.scope, shadowed: prev ? prev.shadowed.concat(prev.scope) : [], ref: /^\{\{|\$\{/.test(e.value || '') ? e.value : null, secretish: looksSecret(e) }; });
    return Object.values(by).map((x) => Object.assign(x, { passes: !ctx.sandboxed || scopeKind(x.scope) !== 'daemon' || allow().includes(x.name) })).sort((a, b) => a.name.localeCompare(b.name)); };
  UI.effectiveEnvHTML = (ctx) => { const rows = UI.effectiveEnv(ctx);
    return `<table class="tbl compact responsive env-eff"><thead><tr><th>name</th><th>comes from</th><th>overrides</th><th>${ctx.sandboxed ? 'reaches the agent' : 'reaches the step'}</th></tr></thead><tbody>${rows.map((r) => `<tr><td data-l="name" class="mono">${esc(r.name)}</td><td data-l="comes from" class="mono">${esc(scopeLabel(r.scope))}${r.ref ? ` <span class="muted tiny">(${esc(UI.VS ? UI.VS.parse(r.ref).label || r.ref : r.ref)})</span>` : ''}</td><td data-l="overrides" class="mono muted">${r.shadowed.map(scopeLabel).map(esc).join(', ') || '—'}</td><td data-l="reaches">${r.passes ? UI.st('ok', 'yes') : UI.st('idle', 'no: not on sandbox.env_allow')}</td></tr>`).join('') || '<tr class="empty-row"><td colspan="4">No env vars reach this context.</td></tr>'}</tbody></table><div class="muted tiny" style="padding:6px 2px">Names and where they come from only; values are never shown here.</div>`; };

  const ev = { scope: 'all', ctx: { runtime: 'claude-cli', host: '', workflow: 'ci-fixer', sandboxed: true } };
  UI.route('secrets/:tab', ({ tab }) => {
    if (tab !== 'env') { setTimeout(() => UI.go('#/secrets')); return ''; }
    const v = S.view(), env = (v.env || []).filter((e) => ev.scope === 'all' || scopeKind(e.scope) === ev.scope);
    const plainSecrets = (v.env || []).filter((e) => looksSecret(e) && e.plain);
    return `<div class="page">${UI.head('Secrets &amp; vars', { sub: 'environment variables', actions: `<button class="btn" data-act="env-export">${icon('download')}Export</button><button class="btn" data-act="env-import">${icon('upload')}Import</button><button class="btn primary" data-act="env-add">${icon('plus')}Add an env var</button>` })}
      ${UI.tabs([['main', 'Secrets, vaults &amp; settings', '#/secrets'], ['env', 'Env', '#/secrets/env', (v.env || []).length]], 'env')}
      <p class="lede" style="margin:0 0 12px">Variables reach steps and agents by scope; the narrowest wins (a workflow's beats a host's, beats a runtime's, beats <span class="mono">conductor.env</span>). Changes to <span class="mono">conductor.env</span> apply by restarting conductor through the boot guard, with a backup.</p>
      ${plainSecrets.length ? `<div class="callout warn small" style="margin-bottom:12px">${icon('warn')} <span class="grow"><b>${plainSecrets.map((e) => e.name).join(', ')}</b> look${plainSecrets.length > 1 ? '' : 's'} secret but sit${plainSecrets.length > 1 ? '' : 's'} in plain text in ${plainSecrets.map((e) => scopeLabel(e.scope)).join(', ')}.</span>${plainSecrets.map((e) => `<button class="btn sm" data-act="env-to-vault" data-k="${esc(e.scope + ':' + e.name)}">${icon('lock')}Move ${esc(e.name)} to the vault</button>`).join('')}</div>` : ''}
      <div class="filters">${UI.seg([['all', 'all scopes'], ['daemon', 'conductor.env'], ['runtime', 'runtimes'], ['host', 'hosts'], ['workflow', 'workflows']], ev.scope, 'env-scope')}</div>
      <div class="panel"><table class="tbl responsive"><thead><tr><th>name</th><th>scope</th><th>value</th><th>used in config</th><th></th></tr></thead><tbody>
        ${env.map((e) => { const k = e.scope + ':' + e.name, u = usesOf(e.name), st = S.find('env', k); return `<tr data-env="${esc(k)}"><td data-l="name" class="mono"><b>${esc(e.name)}</b></td><td data-l="scope" class="mono">${esc(scopeLabel(e.scope))}</td><td data-l="value" class="mono">${shown(e)}${looksSecret(e) && e.plain ? ' ' + tag('plain text', 'wait') : ''}${st ? ' ' + tag('staged', 'run') : ''}</td><td data-l="used in">${u.length ? u.map((n) => `<a class="link mono" href="#/config/raw?line=${n}">line ${n}</a>`).join(', ') : '<span class="muted tiny">' + (scopeKind(e.scope) === 'daemon' ? 'read by conductor or its plugins' : 'given to every step in scope') + '</span>'}</td><td class="num nowrap"><button class="btn sm ghost" data-act="env-edit" data-k="${esc(k)}">${icon('edit')}Edit</button><button class="icon-btn" data-act="env-del" data-k="${esc(k)}" aria-label="Remove ${esc(e.name)}">${icon('trash')}</button></td></tr>`; }).join('') || '<tr class="empty-row"><td colspan="5">No env vars in this scope.</td></tr>'}
      </tbody></table></div>
      <div class="panel" style="margin-top:14px" id="env-preview"><div class="panel-head"><h3>Effective environment</h3><span class="muted small">what a step would get</span></div><div class="panel-body stack tight">
        <div class="row wrap" style="gap:10px">
          <label class="small">Runtime <select class="select sm" data-on-change="env-ctx" data-k="runtime">${['', ...v.runtimes.map((r) => r.name)].map((n) => `<option value="${esc(n)}"${ev.ctx.runtime === n ? ' selected' : ''}>${n || 'none'}</option>`).join('')}</select></label>
          <label class="small">Host <select class="select sm" data-on-change="env-ctx" data-k="host">${['', ...v.hosts.map((h) => h.name)].map((n) => `<option value="${esc(n)}"${ev.ctx.host === n ? ' selected' : ''}>${n || 'this machine'}</option>`).join('')}</select></label>
          <label class="small">Workflow <select class="select sm" data-on-change="env-ctx" data-k="workflow">${['', ...v.workflows.filter((w) => w.kind !== 'workflow').map((w) => w.name)].map((n) => `<option value="${esc(n)}"${ev.ctx.workflow === n ? ' selected' : ''}>${n || 'none'}</option>`).join('')}</select></label>
          <label class="row small">${UI.switchBtn(ev.ctx.sandboxed, 'env-sbx', 'aria-label="Agent step in the sandbox"')}<span>an agent step in the sandbox</span></label></div>
        ${UI.effectiveEnvHTML(ev.ctx)}</div></div></div>`;
  }, { nav: 'secrets' });
  UI.act('env-scope', (b) => { ev.scope = b.dataset.v; UI.rerender(); });
  UI.act('env-ctx', (el) => { ev.ctx[el.dataset.k] = el.value; UI.rerender(); });
  UI.act('env-sbx', () => { ev.ctx.sandboxed = !ev.ctx.sandboxed; UI.rerender(); });
  const scopes = () => { const v = S.view(); return [['daemon', 'conductor.env (the daemon)'], ...v.runtimes.map((r) => ['runtime:' + r.name, 'runtime ' + r.name]), ...v.hosts.map((h) => ['host:' + h.name, 'host ' + h.name]), ...v.workflows.filter((w) => w.source === 'config' && w.kind !== 'workflow').map((w) => ['workflow:' + w.name, 'workflow ' + w.name])]; };
  const envForm = (k) => { const cur = k ? S.staged('env', k) : null;
    UI.form({ caps: cur ? S.cfg.env.path(k) : 'env', title: cur ? `Edit ${cur.name}` : 'Add an env var', edit: !!cur, values: cur ? { scope: cur.scope, name: cur.name, value: cur.value } : { scope: 'daemon' },
      intro: 'A value that looks secret goes to the vault, and the variable holds a reference to it.',
      fields: [{ k: 'scope', type: 'enum', req: true, readonly: !!cur, options: scopes() }, { k: 'name', type: 'string', req: true, readonly: !!cur, ph: 'APP_USER', pattern: /^[A-Z_][A-Z0-9_]*$/, patternMsg: 'UPPER_SNAKE_CASE', validate: (v, all) => (!cur && (S.view().env || []).some((e) => e.name === v && e.scope === all.scope) ? 'Already set in that scope' : '') },
        { k: 'value', type: 'value', req: true, label: 'Value', sources: ['typed', 'vault'], sensitive: false }],
      secretPrefix: (v) => 'env-' + String(v.name || '').toLowerCase(),
      submit: 'Review change', submitIcon: 'check',
      onSubmit: (v) => { const scope = cur ? cur.scope : v.scope, name = cur ? cur.name : v.name, p = UI.VS.parse(v.value); D.prefs.returnTo = { href: '#/secrets/env', label: 'Env' }; UI.staged(S.stage('env', scope + ':' + name, { scope, name, value: v.value, plain: p.src === 'typed' })); } }); };
  UI.act('env-add', () => envForm(null));
  UI.act('env-edit', (b) => envForm(b.dataset.k));
  UI.act('env-del', (b) => UI.confirm({ title: `Remove ${esc(b.dataset.k.split(':').pop())}?`, body: `Stages removing it from ${esc(scopeLabel(b.dataset.k.split(':').slice(0, -1).join(':')))}. Anything reading <span class="mono">\${env.${esc(b.dataset.k.split(':').pop())}}</span> would get nothing.`, ok: 'Stage removal', cancel: 'Keep it', danger: true }).then((ok) => ok && UI.staged(S.stage('env', b.dataset.k, null))));
  /** Store values in conductor's vault (house) now: a secret write, signed with the passkey, value never recorded. */
  const vaultWrite = (keys) => UI.stepup({ title: `Store ${keys.length} value${keys.length > 1 ? 's' : ''} in the vault`, purpose: 'secret write', ok: 'Store with passkey', what: `{ purpose: <b>secret_write</b>, vault: <b>house</b>, keys: <b>${keys.map(esc).join(', ')}</b> }` }).then((ok) => { if (!ok) return false;
    keys.forEach((key) => { const ref = 'house/' + key; if (!D.secrets.some((x) => x.ref === ref)) D.secrets.unshift({ name: ref, ref, vault: 'house', key, alias: '', set: new Date().toISOString(), used: [] }); D.audit.unshift({ ts: new Date().toISOString(), event: 'secret_write', who: 'operator:ops', detail: `${ref} · value not recorded`, run: '' }); }); S.save(); return true; });
  UI.act('env-to-vault', (b) => { const k = b.dataset.k, cur = S.staged('env', k), key = 'env-' + cur.name.toLowerCase().replace(/_/g, '-');
    UI.confirm({ title: `Move ${esc(cur.name)} into the vault?`, body: `Stores the current value as <span class="mono">house/${esc(key)}</span> (write-only, with your passkey), and stages ${esc(cur.name)} as <span class="mono">{{ vault "house" "${esc(key)}" }}</span>, so the plain-text line leaves ${esc(scopeLabel(cur.scope))}.`, ok: 'Continue' })
      .then((ok) => ok && vaultWrite([key])).then((ok) => { if (!ok) return; D.prefs.returnTo = { href: '#/secrets/env', label: 'Env' }; UI.staged(S.stage('env', k, Object.assign({}, cur, { value: `{{ vault "house" "${key}" }}`, plain: false, looksSecret: false, len: undefined }))); }); });
  UI.act('env-export', () => { const v = S.view(), lines = (v.env || []).map((e) => `# ${scopeLabel(e.scope)}\n${e.name}=${looksSecret(e) && e.plain ? '<redacted: plain-text secret, move it to the vault>' : e.value}`); UI.download('conductor-env.txt', lines.join('\n') + '\n', 'text/plain'); UI.toast('Exported env', 'names, scopes and references; plain-text secrets are redacted', 'ok'); });
  UI.act('env-import', () => UI.form({ caps: 'env import', title: 'Import env vars', intro: 'Paste KEY=value lines (a .env file). They go to the scope you pick. Values that look secret are stored in conductor\'s vault and referenced, never written in plain text.',
    fields: [{ k: 'scope', type: 'enum', req: true, options: scopes() }, { k: 'text', type: 'text', req: true, rows: 6, label: 'Lines', ph: 'APP_USER=svc-app\nAPI_TOKEN=…', validate: (t) => { const bad = String(t || '').split('\n').filter((l) => l.trim() && !l.trim().startsWith('#') && !/^[A-Z_][A-Z0-9_]*=/.test(l.trim())); return bad.length ? `Not KEY=value: ${bad[0].slice(0, 40)}` : ''; } }],
    submit: 'Review change', submitIcon: 'check',
    onSubmit: (v) => { const lines = String(v.text).split('\n').map((l) => l.trim()).filter((l) => l && !l.startsWith('#')).map((l) => { const i = l.indexOf('='), name = l.slice(0, i), val = l.slice(i + 1).replace(/^["']|["']$/g, ''); const secret = SECRETISH.test(name) && !/^\{\{|\$\{/.test(val); return { name, val, secret, key: 'env-' + name.toLowerCase().replace(/_/g, '-') }; });
      const keys = lines.filter((x) => x.secret).map((x) => x.key);
      (keys.length ? vaultWrite(keys) : Promise.resolve(true)).then((ok) => { if (!ok) return; let last = null;
        lines.forEach((x) => { last = S.stage('env', v.scope + ':' + x.name, x.secret ? { scope: v.scope, name: x.name, value: `{{ vault "house" "${x.key}" }}`, plain: false } : { scope: v.scope, name: x.name, value: x.val, plain: !/^\{\{|\$\{/.test(x.val) }); });
        D.prefs.returnTo = { href: '#/secrets/env', label: 'Env' }; if (keys.length) UI.toast(`${keys.length} secret value${keys.length > 1 ? 's' : ''} went to the vault`, 'the variables reference them; values never sit in plain text', 'ok'); UI.staged(last); }); } }));
})();
