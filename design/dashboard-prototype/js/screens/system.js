/* System: packs & plugins, updates, connectors/runtimes/hosts/isolation, logs & audit, phone "more". */
(function () {
  const { icon, st, tag, ago, esc, usd } = UI;

  /* ---------- packs & plugins ---------- */
  UI.route('extensions/:tab?', ({ tab }) => {
    tab = tab || 'packs';
    const packs = `<div class="panel">${D.packs.map((p) => `<div class="list"><div class="item" style="padding:14px 16px;align-items:flex-start">
        <span class="sigil" style="--sig:${p.official ? 'var(--accent)' : 'var(--ink-3)'}">${p.name[0]}</span>
        <div class="grow"><div class="row wrap" style="gap:6px"><b style="font-size:15px">${p.name}</b><span class="mono tiny muted">${p.use}</span>${p.official ? tag('official registry', 'ember') : tag('third party')}${p.hold ? tag('hold', 'wait') : ''}</div>
          <div class="row wrap small muted" style="gap:14px;margin-top:5px"><span class="mono">${p.ver} · ${p.sha}</span><span>${icon('live')} ${p.triggers}</span><span>${icon('edit')} ${p.overrides} override${p.overrides === 1 ? '' : 's'} in your config</span></div>
          ${p.status === 'wait' ? `<div class="callout warn small" style="margin-top:8px">Triggers are dormant: the pack needs a <span class="mono">pagerduty</span> connector and none is bound. The rest of the pack runs.</div>` : ''}</div>
        <div class="row wrap">${p.update ? `<button class="btn sm primary" data-plan="${p.name}">${icon('updates')}Update to ${p.update}</button>` : ''}<button class="btn sm" data-plan="${p.name}">${icon('eye')}Plan</button><button class="btn sm ghost">${p.hold ? 'Release hold' : 'Hold'}</button><button class="btn sm danger">${icon('trash')}</button></div></div></div>`).join('')}</div>
      <div class="callout small" style="margin-top:12px">Adding, updating or removing a pack is a change to your <span class="mono">packs:</span> block, then <span class="mono">conductor init</span>'s fetch and lock (<span class="mono">conductor.lock.yaml</span> records the sha), validated on a copy before anything is applied. <span class="mono">pack_trust:</span> limits which remote sources are allowed.</div>`;
    const plugins = `<div class="panel"><table class="tbl responsive"><thead><tr><th>plugin</th><th>kind</th><th>version</th><th>source</th><th>instances</th><th>on update</th><th></th></tr></thead><tbody>
      ${D.plugins.map((p) => `<tr><td data-l="plugin"><b>${p.name}</b></td><td data-l="kind">${tag(p.kind)}</td><td data-l="version" class="mono">${p.ver}${p.update ? ` <span style="color:var(--run)">→ ${p.update}</span>` : ''}</td><td data-l="source" class="mono tiny muted">${p.src}</td><td data-l="instances" class="mono small">${p.instances.join(', ')}</td><td data-l="reload">${p.reload === 'in place' ? tag('hot reload', 'ok') : tag('restart')}</td>
        <td class="num">${p.update ? `<button class="btn sm primary">${icon('updates')}Update</button>` : `<button class="btn sm ghost">${icon('more')}</button>`}</td></tr>`).join('')}
      </tbody></table><div class="panel-foot small muted">A moved plugin whose interface is unchanged reloads in place; a changed declaration or widened permissions restarts the daemon. Pin with <span class="mono">use: &lt;ref&gt;@v1.2.3</span>.</div></div>`;
    const browse = `<div class="grid g3">${[['conductor-packs/sre-member', 'SRE runbooks, knowledge and a member charter', '0.9.0', true], ['conductor-packs/dep-hygiene', 'Weekly dependency bumps with a test gate', '2.1.0', false], ['conductor-packs/incident-scribe', 'Timelines and RCAs from incident channels', '1.0.3', false], ['conductor-plugins/linear', 'Linear connector: issues, comments, webhooks', 'v1.1.0', false], ['conductor-plugins/opsgenie', 'Opsgenie connector', 'v0.8.2', false], ['conductor-plugins/lua', 'Code-step engine: run: lua', 'v0.2.0', false]].map(([n, d, v, inst]) => `<div class="panel member-card"><div class="row"><b class="mono" style="font-size:12px">${n}</b><span class="mono tiny muted" style="margin-left:auto">${v}</span></div><div class="small dim">${d}</div><div class="row">${inst ? tag('installed', 'ok') : `<button class="btn sm primary">${icon('plus')}Add</button>`}<span class="muted tiny" style="margin-left:auto">official registry</span></div></div>`).join('')}</div>`;
    return `<div class="page">
      ${UI.head('Packs &amp; plugins', { actions: `<button class="btn" id="add-src">${icon('link')}Add from a source…</button>` })}
      ${UI.tabs([['packs', 'Packs', '#/extensions/packs', D.packs.length], ['plugins', 'Plugins', '#/extensions/plugins', D.plugins.length], ['browse', 'Browse', '#/extensions/browse']], tab)}
      ${tab === 'plugins' ? plugins : tab === 'browse' ? browse : packs}
      <div class="row small muted" style="margin-top:14px">${icon('updates')} Dependencies follow <span class="mono">update.deps</span> (on): a moved pack or plugin that still validates is applied on the 10-minute cycle, logged with the sha it moved from.</div>
    </div>`;
  }, { nav: 'extensions' });
  UI.afterRender.push((main) => UI.bind(main, '[data-plan]', 'click', (e, b) => UI.modal(`<div class="modal-head"><div><div class="caps">conductor pack plan</div><h2>Update ${b.dataset.plan} to 1.5.0</h2></div><button class="icon-btn" data-close>${icon('x')}</button></div>
    <div class="modal-body stack small">
      <div class="kv"><dt>from</dt><dd class="mono">1.4.2 · c81e09a</dd><dt>to</dt><dd class="mono">1.5.0 · 0e47d12</dd><dt>source</dt><dd class="mono">conductor-packs/pr-review-team (trusted)</dd></div>
      <div class="caps">What changes</div>
      <div class="check-row">${st('ok', '')}<div>workflow <span class="mono">review-flow</span>: step <span class="mono">summarize</span> added (narrows nothing, widens nothing)</div></div>
      <div class="check-row">${st('ok', '')}<div>your 4 overrides still apply (<span class="mono">review-flow/review</span> still exists)</div></div>
      <div class="check-row">${st('ok', '')}<div>pack lint: no isolation loosening, no runtime/host, no trust: change</div></div>
      <div class="check-row">${st('wait', '')}<div>new <b>disarmed</b> trigger <span class="mono">on_push</span>: nothing runs until you arm it</div></div>
      <div class="check-row">${st('ok', '')}<div>validated on a copy with v0.71.2 and v0.72.0</div></div>
    </div><div class="modal-foot"><button class="btn ghost" data-close>Cancel</button><button class="btn primary" data-close onclick="UI.toast('Updating review → 1.5.0','lock written · reload after validate','run')">${icon('check')}Update</button></div>`, { wide: true })));

  /* ---------- updates ---------- */
  UI.route('updates', () => {
    const d = D.daemon;
    return `<div class="page">
      ${UI.head('Updates', { sub: `running ${d.version}`, actions: `<button class="btn" onclick="UI.toast('Checked','git ls-remote refs/dist · v0.72.0 is newest','ok')">${icon('updates')}Check now</button>` })}
      <div class="grid g-side">
        <div class="stack">
          <div class="panel" style="border-color:color-mix(in srgb,var(--run) 45%,var(--line))"><div class="panel-body"><div class="row wrap" style="gap:16px;align-items:flex-start">
            <div class="grow"><div class="caps">available</div><div class="serif" style="font-size:52px;line-height:1">v0.72.0</div><div class="muted small">released 2026-10-16 · found ${ago(d.update.last_check)} ago</div></div>
            <div class="stack tight" style="min-width:260px">
              <div class="check-row">${st('ok', '')}<div>Downloaded and verified</div></div>
              <div class="check-row">${st('ok', '')}<div>Preflight: <span class="mono">v0.72.0 validate</span> loads this config (on a copy)</div></div>
              <div class="check-row">${st('ok', '')}<div>Its plugins are installed or fetchable</div></div>
              <div class="check-row">${st('run', '')}<div>Auto-update applies it within ${d.update.interval}</div></div></div></div>
            <div class="row" style="margin-top:14px"><button class="btn primary lg" id="upd">${icon('download')}Apply now</button><span class="muted small">Same path as auto-update: preflight, keep <span class="mono">conductor.prev</span>, atomic swap, restart.</span></div></div></div>
          <div class="panel"><div class="panel-head"><h3>Releases</h3></div><div class="list">${D.releases.map((r) => `<div class="item" style="align-items:flex-start"><span class="mono" style="width:70px;font-weight:600">${r.v}</span><div class="grow small"><div class="mono tiny muted">${r.date}${r.current ? ' · running' : ''}</div><ul style="margin:4px 0 0;padding-left:16px">${r.notes.map((n) => `<li>${esc(n)}</li>`).join('')}</ul></div>${r.current ? tag('current', 'ok') : r.preflight ? tag('preflight ok', 'ok') : `<button class="btn sm ghost">${icon('undo')}Roll back to</button>`}</div>`).join('')}</div></div>
        </div>
        <div class="stack">
          <div class="panel"><div class="panel-head"><h3>Policy</h3><span class="muted small">update: in config</span></div><div class="panel-body stack tight small">
            <label class="row"><button class="switch on" onclick="this.classList.toggle('on')"></button><b>Auto-update</b> <span class="muted">on (the default)</span></label>
            <label class="row"><button class="switch on" onclick="this.classList.toggle('on')"></button>Keep packs &amp; plugins current (deps)</label>
            <label class="row"><button class="switch on" onclick="this.classList.toggle('on')"></button>Hot-reload unchanged plugins</label>
            <div class="field" style="margin-top:6px"><label>Apply</label><div class="seg"><button class="on">automatically</button><button>through a workflow</button></div><div class="hint"><span class="mono">apply: workflow</span> emits <span class="mono">conductor.update_available</span> for a gated update.</div></div>
            <div class="field"><label>Check every</label><input class="input mono" value="10m" style="width:90px"></div></div></div>
          <div class="callout small">A release that can't load this box's config is never swapped in. The previous binary is kept as <span class="mono">conductor.prev</span>.</div>
        </div></div></div>`;
  }, { after: (p, main) => UI.bind(main, '#upd', 'click', () => UI.modal(`<div class="modal-head"><h2>Apply v0.72.0 now?</h2><button class="icon-btn" data-close>${icon('x')}</button></div><div class="modal-body small dim">The daemon restarts into the new binary. Runs in flight checkpoint and resume; this page reconnects by itself. Auto-migration runs on first boot.</div><div class="modal-foot"><button class="btn ghost" data-close>Cancel</button><button class="btn primary" data-close onclick="UI.toast('Updating to v0.72.0','preflight ✓ · swap · restart','run')">${icon('passkey')}Apply with passkey</button></div>`)) });

  /* ---------- connectors, runtimes, hosts, isolation ---------- */
  UI.route('connectors/:tab?', ({ tab }) => {
    tab = tab || 'connectors';
    const d = D.daemon;
    const conns = `<div class="grid g3">${D.connectors.map((c) => `<div class="panel member-card"><div class="row"><span class="icon-btn" style="pointer-events:none">${icon('connectors')}</span><div class="grow"><b style="font-size:15px">${c.name}</b> <span class="mono tiny muted">use: ${c.use}</span></div>${st(c.status, '')}</div>
      <div class="small dim">${esc(c.detail)}</div><div class="row small muted mono tiny"><span>${c.events} events</span><span>${c.verbs} verb calls</span>${c.rl ? `<span>rate ${c.rl}</span>` : ''}<button class="btn sm" style="margin-left:auto">${icon('edit')}Configure</button></div></div>`).join('')}
      <div class="panel inset" style="display:grid;place-items:center;border-style:dashed;min-height:150px"><button class="btn">${icon('plus')}Add a connector</button></div></div>`;
    const runtimes = `<div class="panel"><table class="tbl responsive"><thead><tr><th>runtime</th><th>use</th><th>status</th><th>isolation</th><th></th></tr></thead><tbody>${D.runtimes.map((r) => `<tr><td data-l="runtime"><b>${r.name}</b></td><td data-l="use" class="mono">${r.use}</td><td data-l="status">${st(r.status, r.detail)}</td><td data-l="isolation">${tag(r.isolation, 'ok')}</td><td class="num"><button class="btn sm">${icon('edit')}Edit</button></td></tr>`).join('')}</tbody></table></div>`;
    const hosts = `<div class="panel"><table class="tbl"><thead><tr><th>host</th><th>address</th><th>user</th><th>status</th><th class="num">rtt</th></tr></thead><tbody>${D.hosts.map((h) => `<tr><td><b>${h.name}</b></td><td class="mono">${h.addr}</td><td class="mono">${h.user}</td><td>${st(h.status, 'reachable')}</td><td class="num mono">${h.rtt}</td></tr>`).join('')}</tbody></table><div class="panel-foot"><button class="btn sm">${icon('plus')}Add host</button></div></div>`;
    const iso = `<div class="grid g2"><div class="panel"><div class="panel-head"><h3>Agent sandbox</h3>${st('ok', 'enforcing', true)}</div><div class="panel-body stack">
        <dl class="kv small"><dt>agents</dt><dd class="mono">on</dd><dt>backend</dt><dd class="mono">${d.sandbox.backend} (Linux namespaces + pivot_root)</dd><dt>identities</dt><dd class="mono">${d.sandbox.identities} · HOMEs in &lt;state&gt;/agent-state</dd><dt>containers</dt><dd class="mono">${d.sandbox.containers} live (billing-sre, gpu-box)</dd><dt>egress</dt><dd>${d.sandbox.egress.map((e) => tag(e)).join(' ')}</dd><dt>push</dt><dd class="mono small">fast-forward only, re-signed with your SSH key</dd></dl>
        <div class="field"><label>Default backend</label><div class="seg"><button class="on">namespace / Seatbelt</button><button>container</button></div></div>
        <div class="callout small">The dashboard's own addresses (127.0.0.1:7788, ${d.tailscale_ip}:7788, ${d.magicdns}) are refused by the egress proxy even if an allowlist names them.</div></div></div>
      <div class="panel"><div class="panel-head"><h3>Opt-outs in effect</h3><span class="muted small">isolation: {mode: none}</span></div><div class="list">
        <div class="item">${st('wait', '')}<div class="grow small"><b>trigger gh.review_requested (hand-off)</b><div class="mono tiny muted">your config · runs as you through paseo's plain provider · titled [UNSANDBOXED]</div></div></div>
        <div class="item">${st('ok', '')}<div class="grow small muted">No pack sets an opt-out (a pack can't).</div></div></div>
        <div class="panel-foot small muted">An unsandboxed agent runs as your uid: it could reach localhost. The dashboard still requires a passkey for everything, and an approval needs your authenticator.</div></div></div>`;
    return `<div class="page">${UI.head('Connectors &amp; hosts')}
      ${UI.tabs([['connectors', 'Connectors', '#/connectors/connectors', D.connectors.length], ['runtimes', 'Runtimes', '#/connectors/runtimes', D.runtimes.length], ['hosts', 'Hosts', '#/connectors/hosts', D.hosts.length], ['isolation', 'Isolation', '#/connectors/isolation']], tab)}
      ${tab === 'runtimes' ? runtimes : tab === 'hosts' ? hosts : tab === 'isolation' ? iso : conns}
      <p class="muted small" style="margin-top:12px">Every edit here is a node-level change to your config and goes through validate-on-a-copy → diff → apply.</p></div>`;
  }, { nav: 'connectors' });

  /* ---------- logs & audit ---------- */
  const LOGS = [['info', 'conductor', 'gh: sweep 14 repos · 0 missed · next in 30s'], ['info', 'paseo', 'provider entries unchanged=2 (claude-sandboxed, codex-sandboxed)'], ['info', 'members', 'platform-sre: delivered 1 message to home s7 (idle)'], ['warn', 'sandbox', 'egress denied: storage.googleapis.com from agent:fixer@acme/api'], ['info', 'engine', 'r-7f3a ci-fixer step fix: model claude-sonnet-5 (fleet fixer rung 1)'], ['info', 'dashboard', 'session ops: passkey assertion ok (laptop) from 127.0.0.1'], ['err', 'vaults', 'hcv: 403 permission denied (token expired)'], ['info', 'update', 'v0.72.0 found · preflight validate on a copy: ok']];
  UI.route('logs/:tab?', ({ tab }) => {
    tab = tab || 'daemon';
    const daemon = `<div class="panel"><div class="panel-head"><span class="st" data-s="run"><i><span class="rec" style="background:var(--run)"></span></i>following</span><div class="tools"><div class="seg"><button class="on">all</button><button>warn+</button><button>error</button></div><div class="input-icon" style="width:220px">${icon('search')}<input class="input" placeholder="filter"></div></div></div>
      <div class="log" style="min-height:420px">${LOGS.map(([k, s, m], i) => `<div class="l ${k === 'err' ? 'err' : k === 'warn' ? 'warn' : ''}"><span class="t">${new Date(Date.now() - (LOGS.length - i) * 21000).toTimeString().slice(0, 8)}</span><span class="src">${s}</span><span class="m">${esc(m)}</span></div>`).join('')}</div>
      <div class="panel-foot small muted">From the daemon's own log ring (journald / launchd log is unchanged). Secret values are redacted at the source.</div></div>`;
    const audit = `<div class="filters"><div class="input-icon">${icon('search')}<input class="input" placeholder="event, actor, run, case…"></div>${['dashboard_*', 'member_*', 'browser_*', 'config_apply', 'egress_denied', 'verb'].map((e, i) => `<button class="chipsel${i === 1 ? ' on' : ''}">${e}</button>`).join('')}</div>
      <div class="panel"><table class="tbl responsive"><thead><tr><th>when</th><th>event</th><th>actor</th><th>detail</th></tr></thead><tbody>${D.audit.map((a) => `<tr><td data-l="when" class="mono tiny muted">${ago(a.ts)} ago</td><td data-l="event" class="mono" style="color:var(--run)">${a.event}</td><td data-l="actor" class="mono small">${a.who}</td><td data-l="detail" class="small">${esc(a.detail)}</td></tr>`).join('')}</tbody></table>
      <div class="panel-foot small muted">audit.jsonl · append-only · every dashboard action is audited with the operator, the passkey credential and the client address.</div></div>`;
    return `<div class="page">${UI.head('Logs &amp; audit', { actions: `<button class="btn">${icon('download')}Download</button>` })}${UI.tabs([['daemon', 'Daemon log', '#/logs/daemon'], ['audit', 'Audit', '#/logs/audit']], tab)}${tab === 'audit' ? audit : daemon}</div>`;
  }, { nav: 'logs' });

  /* ---------- phone: more ---------- */
  UI.route('more', () => `<div class="page">${UI.head('More')}<div class="panel"><div class="list">${[['runs', 'Run history', 'runs'], ['workflows', 'Workflows', 'workflows'], ['browser', 'Browsers', 'browser'], ['config', 'Config', 'config'], ['secrets', 'Secrets & vars', 'secrets'], ['extensions', 'Packs & plugins', 'extensions'], ['connectors', 'Connectors & hosts', 'connectors'], ['updates', 'Updates', 'updates'], ['logs', 'Logs & audit', 'logs'], ['security', 'Access', 'shield']].map(([h, l, ic]) => `<a class="item click" href="#/${h}">${icon(ic)}<span class="grow" style="font-weight:600">${l}</span><span class="chev">${icon('chev')}</span></a>`).join('')}
    <a class="item click" onclick="UI.theme(document.documentElement.dataset.theme==='dark'?'light':'dark')">${icon('moon')}<span class="grow" style="font-weight:600">Toggle theme</span></a></div></div></div>`);
})();
