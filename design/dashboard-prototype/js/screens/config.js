/* Config: files, sections and owners, apply history with one-click rollback,
 * and the change pipeline: node edit → validate on a copy → diff → apply with
 * a backup → reload → health window → commit (or automatic restore). */
(function () {
  const { icon, st, tag, ago, esc } = UI;

  const SECTIONS = [
    ['settings', 4, 'op'], ['vaults', 4, 'op'], ['connectors', 7, 'op'], ['hosts', 2, 'op'], ['runtimes', 5, 'op'], ['models', 3, 'op'],
    ['workflows', 2, 'op'], ['triggers', 6, 'op'], ['operators', 1, 'op'], ['members', 3, 'op'], ['sandbox', 1, 'op'], ['browser', 1, 'op'],
    ['packs', 4, 'op'], ['policy', 1, 'op'], ['update', 1, 'op'],
  ];

  UI.route('config', () => `<div class="page">
    ${UI.head('Config', { sub: 'the file is the source of truth', actions: `<a class="btn" href="#/config/raw">${icon('code')}Edit YAML</a><a class="btn primary" href="#/config/apply">${icon('check')}2 pending changes</a>` })}
    <div class="grid g-side">
      <div class="stack">
        <div class="panel"><div class="panel-head"><h3>Files</h3><span class="muted small">what the daemon loaded at boot</span><div class="tools">${st('ok', 'on disk = loaded')}</div></div>
          <table class="tbl compact"><thead><tr><th>file</th><th>owner</th><th>sha</th><th>loaded</th></tr></thead><tbody>
            <tr><td class="mono">~/.config/conductor/config.yaml</td><td><span class="owner op">you</span></td><td class="mono">4be1f02a</td><td class="mono muted">50m ago</td></tr>
            <tr><td class="mono">~/.config/conductor/conf.d/connectors/pd.yaml</td><td><span class="owner op">you · imports:</span></td><td class="mono">91d0c7e3</td><td class="mono muted">50m ago</td></tr>
            <tr><td class="mono">.conductor/packs/review/ (vendored)</td><td><span class="owner pack">pack review · c81e09a</span></td><td class="mono">c81e09a</td><td class="mono muted">re-vendored 6m ago</td></tr>
            <tr><td class="mono">.conductor/packs/sre-member/ (vendored)</td><td><span class="owner pack">pack sre-member · hold</span></td><td class="mono">3d70b2e</td><td class="mono muted">held</td></tr>
            <tr><td class="mono">conductor.lock.yaml</td><td><span class="owner pack">conductor</span></td><td class="mono">5e2c91aa</td><td class="mono muted">6m ago</td></tr>
          </tbody></table></div>
        <div class="panel"><div class="panel-head"><h3>Sections</h3><span class="muted small">open one in its visual editor, or as YAML</span></div><div class="panel-body"><div class="row wrap" style="gap:6px">${SECTIONS.map(([n, c]) => `<a class="chipsel" href="#/config/raw">${n} <span class="mono tiny muted">${c}</span></a>`).join('')}</div></div></div>
        <div class="callout small"><b>Packs.</b> A pack's own files are vendored and never edited here. What you change about a pack lives in <b>your</b> <span class="mono">packs.&lt;instance&gt;</span> block: bindings, <span class="mono">steps:</span> overrides, <span class="mono">triggers:</span> arms, <span class="mono">policy:</span>, <span class="mono">models:</span>. A pack can narrow, never widen: a pack that sets <span class="mono">isolation: {mode: none}</span> (or user/container, privileged, egress, fs) on its agents is refused at load. Loosening is your decision, made in your overrides, and the dashboard asks for your passkey when you do.</div>
      </div>
      <div class="stack">
        <div class="panel"><div class="panel-head"><h3>Applied changes</h3><span class="muted small">each keeps a backup</span></div><div class="list">${D.configHistory.map((h) => `
          <div class="item" style="align-items:flex-start">${h.status === 'live' ? st('ok', '') : st('idle', '')}<div class="grow"><div class="small">${esc(h.summary)}</div><div class="mono tiny muted">${h.id} · ${h.who} · ${ago(h.at)} ago</div></div>
          ${h.status === 'live' ? tag('live', 'ok') : `<button class="btn sm" data-rollback="${h.id}">${icon('undo')}Restore</button>`}</div>`).join('')}</div>
          <div class="panel-foot small muted">Restore is the same pipeline in reverse: the backup is validated on a copy first, then applied.</div></div>
        <div class="panel"><div class="panel-head"><h3>Guards</h3></div><div class="panel-body small stack tight">
          <div class="row">${st('ok', '')}Edits are node-level: comments, anchors and unknown keys are kept.</div>
          <div class="row">${st('ok', '')}Validated on a copy, with copied plugin state; <span class="mono">conductor validate</span> is not read-only.</div>
          <div class="row">${st('ok', '')}A config this binary can't load is never written.</div>
          <div class="row">${st('ok', '')}Also checked against the pending update (v0.72.0).</div>
          <div class="row">${st('ok', '')}Boot guard: a config that fails at boot is restored automatically.</div></div></div>
      </div></div></div>`, { after: (p, main) => UI.bind(main, '[data-rollback]', 'click', (e, b) => UI.go('#/config/apply?rollback=' + b.dataset.rollback)) });

  /* ---------- raw editor ---------- */
  const RAW = `# conductor config — ops-box
settings:
  org: acme
  review_channel: "#code-reviews"

sandbox:
  egress: [registry.npmjs.org, proxy.golang.org, "*.pypi.org"]

members:
  platform-sre:
    description: "SRE for the platform services"
    charter_file: members/platform-sre.md
    proactive:
      every: 4h                        # was 6h until 2026-10-15
      quiet_hours: "22:00-07:00"
      budget: { max_runs_per_day: 6, max_cost_per_day: 2.00 }
      notify_at: high

packs:
  review:
    use: conductor-packs/pr-review-team
    steps:
      review-flow/review: { model: my-fleet }   # house fleet
    triggers:
      on_review_request: { enabled: true, repos: [acme/*] }`;
  UI.route('config/raw', () => `<div class="page wide">
    ${UI.head('config.yaml', { crumbs: `<a href="#/config">Config</a>`, sub: '~/.config/conductor/config.yaml · sha 4be1f02a', actions: `<span class="mono tiny muted">node: members.platform-sre.proactive.every</span><a class="btn primary" href="#/config/apply">${icon('check')}Validate &amp; review</a>` })}
    <div class="grid" style="grid-template-columns:minmax(0,1fr) 320px">
      <div class="panel"><div class="panel-head"><span class="mono small">editing</span><div class="tools"><span class="mono tiny muted">schema from <span class="mono">conductor schema</span> · completion on</span></div></div>${UI.yaml(RAW, { hl: [14] })}</div>
      <div class="stack"><div class="panel"><div class="panel-head"><h3>Outline</h3></div><div class="tree" style="padding:6px">${['settings', 'sandbox', 'members', '  platform-sre', '    proactive', 'packs', '  review'].map((n) => `<a class="${n.includes('proactive') ? 'on' : ''}" style="padding-left:${10 + (n.length - n.trimStart().length) * 6}px">${icon(n.startsWith(' ') ? 'chev' : 'folder')}${n.trim()}</a>`).join('')}</div></div>
        <div class="callout info small">Raw edits are parsed into the same node tree as the visual editors, so a diff here is still a node-level change and the same pipeline applies.</div></div>
    </div></div>`, { nav: 'config' });

  /* ---------- apply pipeline ---------- */
  const STEPS = ['Edit', 'Validate on a copy', 'Review diff', 'Apply', 'Reload & verify'];
  const CHECKS = [
    ['Node-level edit', '2 nodes changed · 31 comments, 2 anchors, 1 unknown key (x-owner) kept byte-for-byte'],
    ['Base unchanged', 'config.yaml is still 4be1f02a: no one edited it since you started'],
    ['Loads in this binary', 'v0.71.2 loader, in-process, on the candidate bytes: 0 errors'],
    ['conductor validate on a copy', 'scratch HOME + copied plugin state, live dirs read-only: 0 errors, 1 warning'],
    ['Loads in the pending update', 'downloaded v0.72.0 validate on the same copy: 0 errors (auto-update won\'t crash-loop)'],
    ['Packs: narrow, never widen', 'no pack manifest loosens isolation; your overrides: 0 widening'],
    ['References resolve', '3 secrets, 2 settings, 1 fleet referenced by the change all resolve'],
  ];
  const DIFF = `<div class="h">config.yaml · triggers[name=ci-fixer] (line 214)</div>
    <div class="dl"><span>228</span><span>228</span><span> </span><span>        runtime: claude-cli</span></div>
    <div class="dl del"><span>229</span><span></span><span>-</span><span>        model: fixer                   # fleet: sonnet, then opus on retry</span></div>
    <div class="dl add"><span></span><span>229</span><span>+</span><span>        model: claude-opus-5           # fleet: sonnet, then opus on retry</span></div>
    <div class="dl"><span>230</span><span>230</span><span> </span><span>        prompt: |</span></div>
    <div class="h">config.yaml · triggers[name=ci-fixer].steps (line 241)</div>
    <div class="dl"><span>241</span><span>241</span><span> </span><span>        options: { body: "Fixed in {{.push.sha}}" }</span></div>
    <div class="dl add"><span></span><span>242</span><span>+</span><span>      - id: notify</span></div>
    <div class="dl add"><span></span><span>243</span><span>+</span><span>        uses: slack-ops.post</span></div>
    <div class="dl add"><span></span><span>244</span><span>+</span><span>        options: { text: "ci-fixer fixed {{.repo}}#{{.number}}" }</span></div>
    <div class="dl"><span>242</span><span>245</span><span> </span><span>    x-owner: platform                  # unknown key: kept as written</span></div>`;

  UI.route('config/apply', () => {
    const rb = (location.hash.split('rollback=')[1] || '');
    return {
      toString: () => `<div class="page">
        ${UI.head(rb ? 'Restore a backup' : 'Review and apply', { crumbs: `<a href="#/config">Config</a>`, sub: rb ? rb : '2 changes from the workflow builder' })}
        <div class="apply-steps" id="steps">${STEPS.map((s, i) => `<div class="as ${i === 0 ? 'done' : i === 1 ? 'cur' : ''}"><b>${i === 0 ? '✓' : i + 1}</b>${s}</div>`).join('')}</div>
        <div class="grid g-side">
          <div class="stack">
            <div class="panel"><div class="panel-head"><h3>Diff</h3><span class="muted small">what will be written · +4 −1</span><div class="tools"><div class="seg"><button class="on">unified</button><button>split</button><button>whole file</button></div></div></div><div class="panel-body tight"><div class="diff">${DIFF}</div></div></div>
            <div class="panel"><div class="panel-head"><h3>Effect</h3></div><div class="panel-body small stack tight">
              <div class="row">${icon('workflows')}<span><b>ci-fixer</b>: step <span class="mono">fix</span> pins <span class="mono">claude-opus-5</span> (was the <span class="mono">fixer</span> fleet) · est. +$0.40/run</span></div>
              <div class="row">${icon('zap')}<span><b>ci-fixer</b>: new step <span class="mono">notify</span> posts to Slack after a fix</span></div>
              <div class="row">${icon('updates')}<span>Reload re-execs the daemon. In-flight steps get a grace to checkpoint and resume after boot; this page reconnects by itself.</span></div></div></div>
          </div>
          <div class="stack">
            <div class="panel"><div class="panel-head"><h3>Checks</h3><div class="tools" id="chk-state">${st('run', 'running')}</div></div><div class="panel-body" id="checks">${CHECKS.map(([k, d], i) => `<div class="check-row" data-i="${i}"><span class="ci">${st('idle', '')}</span><div><b>${k}</b><div class="muted tiny">${d}</div></div></div>`).join('')}</div>
              <div class="panel-body" style="border-top:1px solid var(--line)"><div class="callout warn small">1 warning: <span class="mono">triggers.ci-fixer.steps.fix</span>: an exact model pin skips the fleet's fallback.</div></div></div>
            <div class="panel" id="apply-box"><div class="panel-body stack">
              <div class="small muted">${icon('download')} Backup <span class="mono">cfg-20261016-1032</span> is written before anything changes.</div>
              <button class="btn lg primary" id="go" disabled>${icon('passkey')}Apply with passkey</button>
              <div class="muted tiny">Every apply is a fresh passkey assertion over the diff's digest, like an approval.</div></div></div>
          </div></div></div>`,
      after(main) {
        const rows = UI.$$('.check-row', main); let i = 0;
        const tick = () => {
          if (i < rows.length) { rows[i].querySelector('.ci').innerHTML = st('ok', ''); i++; setTimeout(tick, 260); return; }
          UI.$('#chk-state').innerHTML = st('ok', '7 passed');
          UI.$$('#steps .as')[1].className = 'as done'; UI.$$('#steps .as')[1].querySelector('b').textContent = '✓'; UI.$$('#steps .as')[2].className = 'as cur';
          UI.$('#go').disabled = false;
        };
        rows.forEach((r) => (r.querySelector('.ci').innerHTML = st('run', ''))); setTimeout(tick, 400);
        UI.bind(main, '#go', 'click', () => {
          const box = UI.$('#apply-box .panel-body');
          const S = UI.$$('#steps .as');
          S[2].className = 'as done'; S[2].querySelector('b').textContent = '✓'; S[3].className = 'as cur';
          const stages = [['Passkey assertion verified', 'operator ops · credential laptop · digest 7c19…'], ['Backup written', 'cfg-20261016-1032 (0600)'], ['Apply journal written', '<state>/config-apply/pending.json'], ['config.yaml replaced', 'atomic rename · sha 7c19e2b0'], ['Daemon re-exec', 'SIGHUP path · 2 steps checkpointed'], ['Reconnected', 'SSE resumed from event 88412'], ['Booted with 7c19e2b0', 'v0.71.2 · all connectors up'], ['Health window', '60s: no crash, connectors ok → committed']];
          box.innerHTML = `<div id="stages"></div>`; let k = 0;
          const next = () => {
            if (k === 4) { S[3].className = 'as done'; S[3].querySelector('b').textContent = '✓'; S[4].className = 'as cur'; }
            if (k >= stages.length) {
              S[4].className = 'as done'; S[4].querySelector('b').textContent = '✓';
              box.insertAdjacentHTML('beforeend', `<div class="callout ember small" style="margin-top:10px"><b>Applied.</b> Live config is <span class="mono">7c19e2b0</span>.</div><div class="row" style="margin-top:10px"><button class="btn" onclick="UI.toast('Restoring cfg-20261016-1032','validated on a copy first','run')">${icon('undo')}Roll back</button><a class="btn ghost" href="#/config">Done</a></div>`);
              return;
            }
            const [a, b] = stages[k++];
            UI.$('#stages').insertAdjacentHTML('beforeend', `<div class="check-row">${k === 5 || k === 6 ? st('run', '') : st('ok', '')}<div><b>${a}</b><div class="muted tiny mono">${b}</div></div></div>`);
            setTimeout(() => { const all = UI.$$('#stages .check-row'); const last = all[all.length - 1]; if (last) last.firstElementChild.outerHTML = st('ok', ''); next(); }, k === 5 ? 1100 : 380);
          };
          next();
        });
      },
    };
  }, { nav: 'config' });
})();
