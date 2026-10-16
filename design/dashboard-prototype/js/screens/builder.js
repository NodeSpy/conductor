/* Workflows list + the visual builder. YAML stays canonical: the canvas is a
 * view of the YAML node tree, every canvas edit is a node-level YAML edit
 * (comments and unknown keys kept), and validation runs on a copy. */
(function () {
  const { icon, st, tag, usd, esc } = UI;

  UI.route('workflows', () => `<div class="page">
    ${UI.head('Workflows', { sub: 'triggers and the steps they run', actions: `<a class="btn primary" href="#/workflows/new">${icon('plus')}New workflow</a>` })}
    <div class="panel"><table class="tbl responsive"><thead><tr><th>workflow</th><th>on</th><th>owner</th><th class="num">runs 24h</th><th class="num">ok</th><th class="num">spend 24h</th><th></th></tr></thead><tbody>
    ${D.workflows.map((w) => `<tr class="click" onclick="location.hash='#/workflows/${w.name}'"><td data-l="workflow"><b>${w.name}</b></td><td data-l="on" class="mono small">${w.on}</td>
      <td data-l="owner">${w.source.startsWith('pack') ? `<span class="owner pack">${w.source}</span>` : w.source === 'member' ? '<span class="owner pack">member (synthesized)</span>' : '<span class="owner op">your config</span>'}</td>
      <td data-l="runs" class="num mono">${w.runs24}</td><td data-l="ok" class="num mono">${Math.round(w.ok * 100)}%</td><td data-l="spend" class="num mono">${usd(w.cost24)}</td>
      <td class="num"><span class="btn sm">${icon('canvas')}Open</span> <span class="btn sm ghost" onclick="event.stopPropagation();UI.toast('Run queued: ${w.name}','manual · inputs from its on: manual','run')">${icon('play')}</span></td></tr>`).join('')}
    </tbody></table></div></div>`);

  /* ---------- models ---------- */
  const CI = {
    owner: 'config', file: '~/.config/conductor/config.yaml', node: 'triggers[name=ci-fixer]',
    trigger: { on: 'gh.check_failed', detail: 'repos: acme/api, acme/web · not_label_any: wip' },
    rows: [
      { id: 'logs', kind: 'uses', what: 'gh.check_logs' },
      { id: 'triage', kind: 'decide', what: 'kind: test_failure | flaky | infra' },
      { id: 'fix', kind: 'agent', what: 'claude-cli · fixer', model: 'fixer', runtime: 'claude-cli', iff: '{{.triage.kind}} == test_failure' },
      { id: 'test', kind: 'code', what: 'run: command' },
      { id: 'push', kind: 'uses', what: 'git.push' },
      { id: 'comment', kind: 'uses', what: 'gh.comment' },
    ],
    yaml: `triggers:
  - name: ci-fixer
    on: gh.check_failed
    repos: [acme/api, acme/web]
    filter: { not_label_any: [wip] }   # never touch drafts
    policy:
      budget: { max_cost_usd: 2.00 }   # raised from 1.50 on 2026-10-16
    steps:
      - id: logs
        uses: gh.check_logs
      - id: triage
        decide: { kind: [test_failure, flaky, infra] }
      - id: fix
        if: "{{.triage.kind}} == test_failure"
        type: agent
        runtime: claude-cli
        model: fixer                   # fleet: sonnet, then opus on retry
        prompt: |
          Fix the failing check on {{.repo}}#{{.number}}.
          {{.logs.text}}
      - id: test
        run: command
        command: [go, test, ./...]
      - id: push
        uses: git.push
      - id: comment
        uses: gh.comment
        options: { body: "Fixed in {{.push.sha}}" }
    x-owner: platform                  # unknown key: kept as written`,
  };
  const PR = {
    owner: 'pack', pack: 'review', file: '.conductor/packs/review/workflows/review-flow.yaml', node: 'workflows.review-flow',
    trigger: { on: 'gh.review_requested', detail: 'armed by you: repos acme/*' },
    rows: [
      { id: 'fetch', kind: 'uses', what: 'gh.pr_diff' },
      { parallel: [[{ id: 'lint', kind: 'code', what: 'run: js' }], [{ id: 'review', kind: 'agent', what: 'claude-cli · reviewer', model: 'reviewer', runtime: 'claude-cli', gate: 'review/no-todos' }]] },
      { id: 'post', kind: 'uses', what: 'gh.review' },
    ],
    yaml: `# review-flow — shipped by conductor-packs/pr-review-team@1.4.2 (c81e09a)
workflows:
  review-flow:
    gate: { run: [review/no-todos] }
    steps:
      - id: fetch
        uses: github.pr_diff
      - parallel:
          - - id: lint
              run: js
              script: lint.js
          - - id: review
              type: agent
              model: reviewer
              prompt_file: prompts/review.md
      - id: post
        uses: github.review`,
    overrides: `packs:
  review:
    use: conductor-packs/pr-review-team
    connectors: { github: gh }
    steps:
      review-flow/review: { model: my-fleet }   # house fleet, not the pack's
    triggers:
      on_review_request: { enabled: true, repos: [acme/*] }`,
  };

  const KIND_IC = { uses: 'zap', agent: 'sparkle', code: 'code', decide: 'diamond' };
  const nodeHTML = (s, x, y, sel, pack) => `<div class="node${sel === s.id ? ' sel' : ''}${pack ? ' pack-owned' : ''}" data-step="${s.id}" style="left:${x}px;top:${y}px;width:186px">
      <span class="port in"></span><div class="nk"><span>${icon(KIND_IC[s.kind])} ${s.kind}</span><span>${s.iff ? 'if' : ''}${s.gate ? 'gate' : ''}</span></div>
      <div class="nn">${s.id}</div><div class="mono muted ellipsis" style="font-size:10px">${esc(s.what)}</div><span class="port out"></span></div>`;

  function canvas(model, sel) {
    const W = 186, H = 74, GAP = 46, BOXW = model.rows.some((r) => r.parallel) ? 500 : 400, CX = BOXW / 2;
    let y = 30, html = '', paths = '', prev = [{ x: CX, y: y + 52 }];
    html += `<div class="node trigger" style="left:${CX - W / 2}px;top:${y}px;width:${W}px"><div class="nk"><span>${icon('live')} trigger</span></div><div class="nn mono" style="font-size:12px">${model.trigger.on}</div><div class="muted" style="font-size:10px">${esc(model.trigger.detail)}</div><span class="port out"></span></div>`;
    y += 52 + GAP;
    const link = (fromPts, x, yy) => fromPts.forEach((p) => (paths += `<path class="done" d="M${p.x} ${p.y} C${p.x} ${p.y + GAP / 2} ${x} ${yy - GAP / 2} ${x} ${yy}"/>`));
    model.rows.forEach((r) => {
      if (r.parallel) {
        const n = r.parallel.length, span = (W + 40) * n - 40, x0 = CX - span / 2;
        html += `<div class="par-box" style="left:${x0 - 16}px;top:${y - 18}px;width:${span + 32}px;height:${H + 36}px"><span class="caps">parallel · joined before the next step</span></div>`;
        const outs = [];
        r.parallel.forEach((branch, i) => { const s = branch[0], x = x0 + i * (W + 40); link(prev, x + W / 2, y); html += nodeHTML(s, x, y, sel, model.owner === 'pack'); outs.push({ x: x + W / 2, y: y + H }); });
        prev = outs; y += H + GAP + 10;
      } else {
        link(prev, CX, y); html += nodeHTML(r, CX - W / 2, y, sel, model.owner === 'pack'); prev = [{ x: CX, y: y + H }]; y += H + GAP;
      }
    });
    html += `<button class="add-step" style="left:${CX - 15}px;top:${y - GAP + 12}px" title="Add a step">${icon('plus')}</button>`;
    link(prev, CX, y - GAP + 12);
    return `<div style="position:relative;width:${BOXW}px;height:${y + 20}px;margin:0 auto"><svg class="edges" width="${BOXW}" height="${y + 20}" style="position:absolute;inset:0">${paths}</svg>${html}</div>`;
  }

  function stepLines(yaml, id) {
    const L = yaml.split('\n'); const i = L.findIndex((l) => new RegExp(`- id: ${id}$`).test(l.trim()) || l.trim() === `- - id: ${id}` || l.trim() === `- id: ${id}`);
    if (i < 0) return [];
    const ind = L[i].search(/\S/); const out = [i + 1];
    for (let j = i + 1; j < L.length; j++) { const k = L[j].search(/\S/); if (k <= ind && L[j].trim()) break; out.push(j + 1); }
    return out;
  }

  function inspector(model, id) {
    const s = model.rows.flatMap((r) => (r.parallel ? r.parallel.flat() : [r])).find((x) => x.id === id);
    if (!s) return '';
    const pack = model.owner === 'pack';
    return `<div class="panel inspector"><div class="panel-head"><span class="icon-btn" style="pointer-events:none">${icon(KIND_IC[s.kind])}</span><h3 class="mono">${s.id}</h3>${pack ? '<span class="owner pack">pack-owned</span>' : '<span class="owner op">yours</span>'}<div class="tools"><button class="icon-btn" data-close-insp>${icon('x')}</button></div></div>
      <div class="panel-body stack tight">
        ${pack ? `<div class="callout info small">Edits become an override in <span class="mono">packs.${model.pack}.steps["review-flow/${s.id}"]</span>. The pack's file is never touched; a pack update keeps your override.</div>` : ''}
        <div class="field"><label>id</label><input class="input mono" value="${s.id}" ${pack ? 'disabled' : ''}></div>
        ${s.kind === 'agent' ? `<div class="grid g2"><div class="field"><label>runtime</label><select class="select mono"><option>${s.runtime}</option><option>paseo</option><option>oc</option></select></div>
          <div class="field"><label>model</label><select class="select mono" id="f-model">${['fixer', 'reviewer', 'my-fleet', 'claude-opus-5', 'gpt-x'].map((m) => `<option${m === (pack ? 'my-fleet' : s.model) ? ' selected' : ''}>${m}</option>`).join('')}</select></div></div>
          <div class="field"><label>isolation</label><div class="seg" id="f-iso"><button class="on" data-v="">sandboxed</button><button data-v="container">container</button><button data-v="none">none</button></div><div class="hint" id="iso-hint">${pack ? 'A pack can narrow its sandbox, never loosen it; an override here is your decision.' : 'Default: the agent sandbox.'}</div></div>` : ''}
        ${s.iff ? `<div class="field"><label>if</label><input class="input mono" value="${esc(s.iff)}"></div>` : ''}
        ${s.kind === 'uses' ? `<div class="field"><label>uses</label><input class="input mono" value="${s.what}"><div class="hint">verb options and outputs come from <span class="mono">conductor schema</span>.</div></div>` : ''}
        <div class="row" style="margin-top:4px"><button class="btn sm ghost">${icon('updates')}Retry policy</button><button class="btn sm ghost">${icon('clock')}Timeout</button>${pack ? '' : `<button class="btn sm danger" style="margin-left:auto">${icon('trash')}Remove</button>`}</div>
      </div></div>`;
  }

  UI.route('workflows/:name', ({ name }) => {
    const model = name === 'pr-review' ? PR : CI;
    let yaml = model.owner === 'pack' ? model.overrides : model.yaml, sel = model.owner === 'pack' ? 'review' : 'fix', errs = [], changes = 0, view = 'yours';
    const yamlPane = () => {
      const text = view === 'pack' ? model.yaml : yaml;
      const hl = view === 'pack' ? stepLines(text, sel) : model.owner === 'pack' ? (text.split('\n').map((l, i) => (l.includes(`review-flow/${sel}`) ? i + 1 : 0)).filter(Boolean)) : stepLines(text, sel);
      return UI.yaml(text, { hl, err: errs.map((e) => e.line) });
    };
    const valPane = () => errs.length ? `<span class="val-pill" style="color:var(--fail)">${icon('x')}${errs.length} error</span><span class="mono tiny" style="color:var(--fail)">${esc(errs[0].msg)}</span>`
      : `<span class="val-pill" style="color:var(--ok)">${icon('check')}valid</span><span class="mono tiny muted">checked on a copy · ${changes ? changes + ' unsaved change' + (changes > 1 ? 's' : '') : 'no changes'}</span>`;
    const page = () => `<div class="page flush"><div class="builder">
      <div class="palette-col"><div class="caps" style="margin-bottom:8px">Add a step</div>
        ${[['uses', 'Verb (uses:)', 'zap'], ['agent', 'Agent', 'sparkle'], ['code', 'Code (run:)', 'code'], ['decide', 'Decide', 'diamond'], ['parallel', 'Parallel branches', 'workflows'], ['call', 'Call a workflow', 'link'], ['foreach', 'For each', 'runs']].map(([k, l, ic]) => `<div class="pal-item" data-add="${k}">${icon(ic)}${l}</div>`).join('')}
        <div class="caps" style="margin:16px 0 8px">Verbs</div>${['gh.comment', 'gh.review', 'gh.label', 'slack-ops.post', 'slack-ops.ask', 'kv.set', 'git.push', 'hoff.ask', 'member'].map((v) => `<div class="pal-item mono" style="font-size:11px;font-weight:400" data-add="uses">${icon('zap')}${v}</div>`).join('')}
      </div>
      <div class="canvas-col">
        <div class="btoolbar"><a class="btn sm ghost back" href="#/workflows">${icon('chev')}</a><span class="serif" style="font-size:24px">${name}</span>
          ${model.owner === 'pack' ? `<span class="owner pack">pack ${model.pack} · conductor-packs/pr-review-team@1.4.2</span>` : '<span class="owner op">your config</span>'}
          <span class="mono tiny muted">${model.node}</span><div style="margin-left:auto" class="row"><button class="btn sm">${icon('play')}Dry run</button><a class="btn sm primary" id="apply" href="#/config/apply">${icon('check')}Review &amp; apply</a></div></div>
        <div class="canvas" id="canvas">${canvas(model, sel)}${inspector(model, sel)}</div>
      </div>
      <div class="yaml-col">
        <div class="btoolbar">${model.owner === 'pack' ? `<div class="seg" id="yview"><button class="${view === 'yours' ? 'on' : ''}" data-v="yours">Your overrides</button><button class="${view === 'pack' ? 'on' : ''}" data-v="pack">Pack source (read-only)</button></div>` : `<span class="mono small">${model.file}</span>`}<span class="muted tiny" style="margin-left:auto">${icon('code')} YAML is canonical</span></div>
        <div id="yaml" style="flex:1;overflow:auto">${yamlPane()}</div>
        <div class="panel-foot" id="val" style="background:var(--bg-2)">${valPane()}</div>
      </div></div></div>`;
    const redraw = (main) => { UI.$('#yaml', main).innerHTML = yamlPane(); UI.$('#val', main).innerHTML = valPane(); UI.$('#apply', main).classList.toggle('disabled', errs.length > 0); if (errs.length) UI.$('#apply', main).setAttribute('disabled', ''); else UI.$('#apply', main).removeAttribute('disabled'); };
    const bindAll = (main) => {
      UI.bind(main, '.node[data-step]', 'click', (e, n) => { sel = n.dataset.step; UI.$('#canvas', main).innerHTML = canvas(model, sel) + inspector(model, sel); bindAll(main); redraw(main); });
      UI.bind(main, '[data-close-insp]', 'click', () => UI.$('.inspector', main).remove());
      UI.bind(main, '#f-model', 'change', (e, s) => {
        const v = s.value; changes++;
        if (model.owner === 'pack') yaml = yaml.replace(/(review-flow\/review: \{ model: )[\w-]+( \})/, `$1${v}$2`);
        else { const L = yaml.split('\n'); const i = L.findIndex((l) => /^\s+model: /.test(l) && l.includes('#')); const ci = L[i].indexOf('#'); const pre = L[i].slice(0, L[i].indexOf('model: ') + 7) + v; L[i] = (pre.length < ci ? pre.padEnd(ci) : pre + ' ') + L[i].slice(ci); yaml = L.join('\n'); }
        const lines = yaml.split('\n'); const ln = lines.findIndex((l) => l.includes('model: ' + v)) + 1;
        errs = v === 'gpt-x' ? [{ line: ln, msg: `line ${ln}: model "gpt-x" — no fleet in models: and no provider offers it` }] : [];
        redraw(main);
      });
      UI.bind(main, '#f-iso button', 'click', (e, b) => {
        UI.$$('#f-iso button', main).forEach((x) => x.classList.toggle('on', x === b)); changes++;
        const hint = UI.$('#iso-hint', main);
        if (b.dataset.v === 'none') { hint.innerHTML = `<span style="color:var(--wait)">${icon('warn')} Widens: this step's agent runs unsandboxed, as you. Applying asks for your passkey.</span>`; if (model.owner === 'pack') yaml = yaml.replace('{ model: my-fleet }', '{ model: my-fleet, isolation: { mode: none } }'); }
        else hint.textContent = b.dataset.v === 'container' ? 'Narrows: the agent runs in its identity\'s container.' : 'Default: the agent sandbox.';
        redraw(main);
      });
      UI.bind(main, '#yview button', 'click', (e, b) => { view = b.dataset.v; UI.$$('#yview button', main).forEach((x) => x.classList.toggle('on', x === b)); redraw(main); });
      UI.bind(main, '.add-step, [data-add]', 'click', () => {
        if (model.owner === 'pack') { UI.toast('Pack workflows take overrides, not new steps', 'call review/review-flow from your own trigger to extend it', 'wait'); return; }
        if (model.rows.find((r) => r.id === 'notify')) return;
        model.rows.push({ id: 'notify', kind: 'uses', what: 'slack-ops.post' }); changes++;
        yaml = yaml.replace('    x-owner:', '      - id: notify\n        uses: slack-ops.post\n        options: { text: "ci-fixer fixed {{.repo}}#{{.number}}" }\n    x-owner:');
        sel = 'notify'; UI.$('#canvas', main).innerHTML = canvas(model, sel) + inspector(model, sel); bindAll(main); redraw(main);
      });
    };
    return { toString: page, after: bindAll };
  }, { nav: 'workflows' });
})();
