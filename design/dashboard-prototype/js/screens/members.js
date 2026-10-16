/* Members (#178): list, and per member: chat, cases, proposals, memory, workers, access, definition. */
(function () {
  const { icon, st, tag, tier, usd, ago, esc, md } = UI;
  const M = (n) => D.members.find((m) => m.name === n) || D.members[0];
  const props = (n) => D.proposals.filter((p) => p.member === n);
  const pending = (n) => props(n).filter((p) => p.status === 'pending');

  /* ---------- list ---------- */
  UI.route('members', () => `<div class="page">
    ${UI.head('Members', { sub: 'persistent agents with a charter, a memory and a ledger', actions: `<a class="btn primary" href="#/members/new/settings">${icon('plus')}Define a member</a>` })}
    <div class="grid g3">${D.members.map((m) => `
      <a class="panel member-card" href="#/members/${m.name}">
        <div class="row">${UI.sigil(m, 'lg')}<div class="grow"><h3>${m.name}</h3><div class="muted small">${esc(m.desc)}</div></div></div>
        <div class="row wrap">${m.home === 'live' ? st('run', 'home live') : st('idle', 'home idle')}${m.shadow ? st('shadow', 'shadow mode') : ''}${tag(m.mode === 'container' ? 'container' : 'sandboxed', 'ok')}</div>
        <div class="grid g3" style="gap:8px">
          <div><div class="caps">cases</div><div class="serif" style="font-size:26px">${m.open}</div></div>
          <div><div class="caps">pending</div><div class="serif" style="font-size:26px;color:${m.pending ? 'var(--accent)' : 'inherit'}">${m.pending}</div></div>
          <div><div class="caps">workers</div><div class="serif" style="font-size:26px">${m.workers}</div></div>
        </div>
        <div class="row small muted"><span class="mono tiny">session ${m.session} · ${m.turns}/${m.maxTurns} turns · ${m.age}/${m.maxAge}</span><span style="margin-left:auto">${m.proactive ? icon('clock') + ' sweep in ' + m.proactive.next : ''}</span></div>
      </a>`).join('')}
      <div class="panel inset" style="display:grid;place-items:center;border-style:dashed;min-height:220px"><div class="empty"><span class="serif">Peers</span><svg width="220" height="80" viewBox="0 0 220 80"><path d="M40 40 C 90 0, 130 0, 180 40" stroke="var(--line-3)" fill="none" stroke-dasharray="4 4"/><circle cx="40" cy="40" r="14" fill="#ff6a2b"/><circle cx="180" cy="40" r="14" fill="#4fb8e8"/><text x="40" y="70" text-anchor="middle" font-size="10" fill="var(--ink-3)" font-family="Martian Mono">platform-sre</text><text x="180" y="70" text-anchor="middle" font-size="10" fill="var(--ink-3)" font-family="Martian Mono">billing-sre</text><text x="110" y="18" text-anchor="middle" font-size="9" fill="var(--ink-3)" font-family="Martian Mono">data only · 20/h</text></svg><div class="small">web-triage is linked to no one.</div></div></div>
    </div></div>`);

  /* ---------- member shell ---------- */
  function hero(m) {
    return `<div class="member-hero">${UI.sigil(m, 'lg')}<div class="grow"><div class="crumbs mono tiny muted"><a href="#/members">Members</a> › ${m.name}</div><h1>${m.name}</h1>
      <div class="facts">${m.home === 'live' ? st('run', `home ${m.session} live`, true) : st('idle', `home ${m.session} idle`, true)}
        ${tag(`${m.turns}/${m.maxTurns} turns`)}${tag(`age ${m.age}/${m.maxAge}`)}${m.shadow ? tag('shadow mode', 'shadow') : tag('approved execution', 'ok')}
        ${tag(`${icon('shield')} ${m.mode}`, 'ok')}${tag(`operators: ${m.operators.join(', ')}`)}${m.peers.length ? tag(`peers: ${m.peers.join(', ')}`) : ''}</div></div>
      <div class="row wrap"><button class="btn" data-act="rotate">${icon('updates')}Rotate session</button>${m.proactive ? `<button class="btn" data-act="sweep">${icon('eye')}Sweep now</button>` : ''}<button class="btn" data-act="deliver">${icon('send')}Deliver event</button></div></div>`;
  }
  function shell(m, tab, body) {
    const pc = pending(m.name).length;
    return `<div class="page">${hero(m)}
      ${UI.tabs([['chat', 'Chat', `#/members/${m.name}/chat`], ['cases', 'Cases', `#/members/${m.name}/cases`, D.cases.filter((c) => c.status === 'open').length], ['proposals', 'Proposals', `#/members/${m.name}/proposals`, pc, pc > 0], ['memory', 'Memory', `#/members/${m.name}/memory`], ['workers', 'Workers', `#/members/${m.name}/workers`, D.workers.filter((w) => w.status === 'run').length], ['access', 'Access', `#/members/${m.name}/access`], ['settings', 'Definition', `#/members/${m.name}/settings`]], tab)}
      ${body}</div>`;
  }
  UI.afterRender.push((main) => {
    UI.bind(main, '[data-act="rotate"]', 'click', () => UI.modal(`<div class="modal-head"><h2>Rotate the home session?</h2><button class="icon-btn" data-close>${icon('x')}</button></div><div class="modal-body small dim stack"><p style="margin:0">Conductor asks the current session for a hand-off note, waits for that turn to settle, launches a fresh session in a new worktree, and only then archives the old one. Its token is revoked at once.</p><div class="kv"><dt>current</dt><dd class="mono">s7 · 41 turns · 9h</dd><dt>next</dt><dd class="mono">s8 · resumes from handoff/s7.md + ledger</dd></div></div><div class="modal-foot"><button class="btn ghost" data-close>Cancel</button><button class="btn primary" data-close onclick="UI.toast('Rotation started','asking s7 for a hand-off note','run')">Rotate</button></div>`));
    UI.bind(main, '[data-act="sweep"]', 'click', () => UI.toast('Sweep x5 started', 'read-only · budget 3/6 runs · $0.31/$2.00 today', 'run'));
    UI.bind(main, '[data-act="deliver"]', 'click', () => UI.modal(`<div class="modal-head"><div><div class="caps">conductor member deliver</div><h2>Deliver an event</h2></div><button class="icon-btn" data-close>${icon('x')}</button></div><div class="modal-body stack">
      <div class="field"><label>Title</label><input class="input" value="Investigate elevated 5xx on /refund"></div>
      <div class="field"><label>Text <span class="muted">(reaches the member fenced as untrusted data)</span></label><textarea class="textarea" rows="4">Error budget burn 4x over the last 30m.</textarea></div>
      <div class="grid g2"><div class="field"><label>Requester</label><select class="select"><option>operator:ops (you)</option><option>event</option></select></div><div class="field"><label>Urgency</label><label class="row small" style="height:30px"><button class="switch" onclick="this.classList.toggle('on')"></button>urgent: start a worker now</label></div></div></div>
      <div class="modal-foot"><button class="btn ghost" data-close>Cancel</button><button class="btn primary" data-close onclick="UI.toast('Delivered as case c32','requester operator:ops','ok')">Deliver</button></div>`));
  });

  /* ---------- chat ---------- */
  let chatName = 'platform-sre';
  const bubble = (c) => {
    const who = c.who === 'op' ? 'you · ops' : c.who === 'member' ? chatName : c.who === 'event' ? `event → case ${c.case}` : 'tool';
    return `<div class="msg ${c.who}"><div class="meta"><span>${who}</span><span>${UI.clock(c.at)}</span></div><div class="bubble">${c.who === 'tool' ? '$ ' + esc(c.text) : md(c.text)}</div></div>`;
  };
  function chat(m) {
    chatName = m.name;
    const pp = pending(m.name);
    return shell(m, 'chat', `<div class="grid g-side">
      <div class="panel chat"><div class="panel-head"><h3>Home session <span class="mono muted">${m.session}</span></h3><span class="muted small">via paseo · messages arrive when the agent is idle, never mid-turn</span><div class="tools"><button class="btn sm ghost">${icon('terminal')}Open in paseo</button></div></div>
        <div class="msgs" id="msgs">${D.chat.map(bubble).join('')}</div>
        <div class="composer"><textarea class="textarea" id="say" rows="1" placeholder="Message ${m.name}…  (you are the requester: operator:ops)"></textarea><button class="btn primary" id="send">${icon('send')}Send</button></div></div>
      <div class="stack">
        ${pp.map((p) => `<div class="panel" style="border-color:color-mix(in srgb,var(--accent) 40%,var(--line))"><div class="panel-head">${st('ember', 'awaiting you')}<span class="mono tiny muted" style="margin-left:auto">${p.id} · ${ago(p.at)}</span></div><div class="panel-body small"><div style="font-weight:600;margin-bottom:6px">${esc(p.summary)}</div><div class="row"><a class="btn sm primary" href="#/members/${m.name}/proposals/${p.id}">${icon('passkey')}Review &amp; sign</a><span class="muted tiny">expires in ${p.expires}</span></div></div></div>`).join('')}
        <div class="panel"><div class="panel-head"><h3>Open cases</h3><a class="btn sm ghost" style="margin-left:auto" href="#/members/${m.name}/cases">All</a></div><div class="list">${D.cases.filter((c) => c.status === 'open').map((c) => `<div class="item small"><span class="mono tiny muted">${c.id}</span><span class="grow ellipsis">${esc(c.title)}</span><span class="taint ${c.cls}">${c.cls}</span></div>`).join('')}</div></div>
        <div class="callout info small">While a third party's request is open (case <b>c24</b>, chat:U-THIRD), nothing the home session does runs on more trust than that requester's.</div>
      </div></div>`);
  }
  function bindChat(main) {
    const box = UI.$('#msgs', main); if (!box) return;
    box.scrollTop = box.scrollHeight;
    const send = () => {
      const ta = UI.$('#say', main), v = ta.value.trim(); if (!v) return;
      box.insertAdjacentHTML('beforeend', bubble({ who: 'op', at: new Date().toISOString(), text: v })); ta.value = '';
      box.insertAdjacentHTML('beforeend', `<div class="msg member" id="typing"><div class="meta"><span>${chatName}</span><span>queued → delivered (idle)</span></div><div class="bubble typing"><i></i><i></i><i></i></div></div>`);
      box.scrollTop = box.scrollHeight;
      setTimeout(() => { const t = UI.$('#typing'); if (t) t.outerHTML = bubble({ who: 'member', at: new Date().toISOString(), text: 'Noted. I’ll keep **c29** open until p99 stays under 400ms for 30 minutes after the apply, then write the episode and close it.' }); box.scrollTop = box.scrollHeight; }, 1800);
    };
    UI.bind(main, '#send', 'click', send);
    UI.bind(main, '#say', 'keydown', (e) => { if (e.key === 'Enter' && !e.shiftKey) { e.preventDefault(); send(); } });
  }

  /* ---------- cases ---------- */
  function cases(m) {
    return shell(m, 'cases', `<div class="filters"><div class="seg"><button class="on">open</button><button>closed</button><button>all</button></div><span class="muted small">A case's requester class decides how much trust its work runs on: <span class="taint operator">operator</span> <span class="taint event">event</span> <span class="taint user">user</span> <span class="taint peer">peer</span> <span class="taint proactive">proactive</span></span></div>
      <div class="panel"><table class="tbl responsive"><thead><tr><th>case</th><th>title</th><th>requester</th><th>class</th><th>opened</th><th class="num">workers</th><th class="num">proposals</th><th>status</th></tr></thead><tbody>
      ${D.cases.map((c) => `<tr class="click"><td data-l="case" class="mono">${c.id}${c.urgent ? ' ' + tag('urgent', 'fail') : ''}</td><td data-l="title"><b>${esc(c.title)}</b></td><td data-l="requester" class="mono muted">${c.requester}</td><td data-l="class"><span class="taint ${c.cls}">${c.cls}</span></td><td data-l="opened" class="mono muted">${ago(c.opened)} ago</td><td data-l="workers" class="num mono">${c.workers}</td><td data-l="proposals" class="num mono">${c.proposals}</td><td data-l="status">${c.status === 'open' ? st('run', 'open') : st('ok', 'closed')}</td></tr>`).join('')}
      </tbody></table></div>
      <div class="row small muted" style="margin-top:10px">${icon('dollar')} spend this period: <span class="mono">${usd(m.spend)}</span> of the commit_money cap · ledger <span class="mono">members.json</span> healthy (not degraded)</div>`);
  }

  /* ---------- proposals ---------- */
  const PS = { pending: 'ember', shadow: 'shadow', executed: 'ok', approved: 'ok', refused: 'fail', denied: 'fail', failed: 'fail' };
  function proposals(m, sub) {
    const list = props(m.name);
    const p = sub && list.find((x) => x.id === sub);
    if (p) return shell(m, 'proposals', proposalDetail(m, p));
    return shell(m, 'proposals', `<div class="panel">${list.map((p) => `
      <a class="proposal ${p.status}" href="#/members/${m.name}/proposals/${p.id}">
        <div>${st(PS[p.status] || 'idle', p.status)}</div>
        <div><div class="row wrap" style="gap:6px;margin-bottom:4px"><span class="mono" style="font-weight:600">${p.id}</span>${tier(p.tier)}<span class="mono small">${p.verb}</span><span class="muted tiny mono">case ${p.case}</span>${p.requester ? `<span class="taint ${p.cls}">${p.requester}</span>` : ''}${p.worker ? tag('worker ' + p.worker) : ''}</div><div>${esc(p.summary)}</div>${p.approver ? `<div class="muted tiny mono" style="margin-top:4px">approved by ${p.approver}</div>` : ''}</div>
        <div class="row">${p.status === 'pending' ? `<span class="muted tiny mono">expires ${p.expires}</span><span class="btn sm primary">${icon('passkey')}Review</span>` : `<span class="muted tiny mono">${ago(p.at)} ago</span>`}</div>
      </a>`).join('')}</div>
      <p class="muted small" style="margin-top:10px">Shadow proposals are the calibration record of a member in shadow mode (or a proactive sweep's write): they can't be approved. Approval is also available from the CLI: <span class="mono">conductor member approve ${m.name} &lt;id&gt;</span> (SSH-signed).</p>`);
  }
  function planHTML(t) {
    return esc(t).split('\n').map((l) => /must be replaced|forces replacement/.test(l) ? `<span class="rep">${l}</span>` : /^\s*\+\/-|^\s*~|will be updated/.test(l) ? `<span class="chg">${l}</span>` : /^\s*\+ /.test(l) ? `<span class="add">${l}</span>` : /#|unchanged/.test(l) ? `<span class="c">${l}</span>` : l).join('\n');
  }
  function proposalDetail(m, p) {
    const tf = p.tf;
    return `<div class="row" style="margin-bottom:12px"><a class="btn sm ghost back" href="#/members/${m.name}/proposals">${icon('chev', '')}All proposals</a></div>
      <div class="grid g-side">
        <div class="stack">
          <div class="panel"><div class="panel-head">${st(PS[p.status], p.status, true)}<h3 class="mono">${p.id}</h3>${tier(p.tier)}<span class="mono small muted">${p.verb}</span><div class="tools"><span class="mono tiny muted">proposed ${ago(p.at)} ago${p.expires ? ' · expires in ' + p.expires : ''}</span></div></div>
            <div class="panel-body"><div class="serif" style="font-size:24px;line-height:1.2;margin-bottom:12px">${esc(p.summary)}</div>
            ${tf ? `<div class="grid g4" style="margin-bottom:14px">
                <div class="panel inset stat"><span class="k">add</span><span class="v" style="color:var(--ok)">+${tf.add}</span></div>
                <div class="panel inset stat"><span class="k">change</span><span class="v" style="color:var(--wait)">~${tf.change}</span></div>
                <div class="panel inset stat"><span class="k">destroy</span><span class="v">-${tf.destroy}</span></div>
                <div class="panel inset stat"><span class="k">replaces</span><span class="v" style="color:var(--fail)">${tf.replaces.length}</span><span class="d mono">${tf.replaces.join(', ')}</span></div></div>
              <div class="callout warn small" style="margin-bottom:12px"><b>Replaces aws_instance.web.</b> Not a stateful type, so it isn't refused; <span class="mono">aws_db_instance</span>, buckets, disks and keys would be unless <span class="mono">allow_destroy</span> names them.</div>
              <div class="caps" style="margin-bottom:6px">Plan (conductor ran it with the tf-plan connector's READ role)</div><div class="plan">${planHTML(D.tfPlan)}</div>`
              : `<div class="caps" style="margin-bottom:6px">Exact action</div>${UI.yaml(`verb: ${p.verb}\noptions:\n${Object.entries(p.options || {}).map(([k, v]) => `  ${k}: "${v}"`).join('\n')}`)}`}
            </div></div>
        </div>
        <div class="stack">
          <div class="panel"><div class="panel-head"><h3>What you sign</h3></div><div class="panel-body"><dl class="kv">
            ${tf ? `<dt>repo</dt><dd class="mono">${tf.repo}</dd><dt>commit</dt><dd class="hash">${tf.commit}</dd><dt>dir</dt><dd class="mono">${tf.dir}</dd><dt>workspace</dt><dd class="mono">${tf.workspace}</dd><dt>var files</dt><dd class="mono">${tf.var_files.join(', ')}</dd><dt>plan sha256</dt><dd class="hash">${tf.plan_sha}</dd>` : ''}
            <dt>case</dt><dd class="mono">${p.case}</dd><dt>requester</dt><dd>${p.requester ? `<span class="taint ${p.cls}">${p.requester}</span>` : '—'}</dd><dt>digest</dt><dd class="hash">${p.digest}</dd>
          </dl>
          ${p.status === 'pending' ? `<div class="row" style="margin-top:16px"><button class="btn lg primary grow" id="ap">${icon('passkey')}Approve</button><button class="btn lg danger" id="dn">Deny</button></div><div class="muted tiny" style="margin-top:8px">Needs one of: <span class="mono">${m.operators.join(', ')}</span>, with user verification. Then conductor checks the plan file still hashes to the signed value, HEAD is the commit and the tree is clean, and applies with the tf-apply connector's WRITE role.</div>` : ''}
          </div></div>
          <div class="panel"><div class="panel-head"><h3>Timeline</h3></div><div class="list small">
            <div class="item">${st('ok', '')}<span class="grow">proposed by home ${m.session}</span><span class="mono tiny muted">${ago(p.at)}</span></div>
            ${tf ? `<div class="item">${st('ok', '')}<span class="grow">planned in <span class="mono tiny">&lt;state&gt;/members/${m.name}/tf/${p.id}/</span> (0700)</span><span class="mono tiny muted">${ago(p.at)}</span></div>` : ''}
            <div class="item">${st(p.status === 'pending' ? 'ember' : PS[p.status], '')}<span class="grow">${p.status === 'pending' ? 'awaiting operator signature' : p.status}</span></div></div></div>
        </div>
      </div>`;
  }

  /* ---------- memory ---------- */
  function memory(m) {
    const tree = D.memory.tree.map((f) => f.dir ? `<a>${icon('folder')}${f.p}</a>` : `<a class="f${f.p === 'knowledge/db-prod-1.md' ? ' on' : ''}">${icon('file')}${f.p.split('/').pop()}${f.recent ? '<span class="new"></span>' : ''}</a>`).join('');
    const prose = D.memory.file.replace(/^# (.*)$/m, '<h1>$1</h1>').replace(/^## (.*)$/gm, '<h2>$1</h2>').replace(/\*\*(.+?)\*\*/g, '<b>$1</b>').replace(/\*(.+?)\*/g, '<i>$1</i>').replace(/`([^`]+)`/g, '<code>$1</code>').replace(/^- (.*)$/gm, '<li>$1</li>').replace(/(<li>[\s\S]*?<\/li>\n?)+/g, (x) => `<ul>${x}</ul>`).replace(/\n\n/g, '<br>');
    return shell(m, 'memory', `<div class="grid" style="grid-template-columns:240px minmax(0,1fr) 300px">
      <div class="panel"><div class="panel-head"><h3>memory repo</h3><button class="icon-btn" style="margin-left:auto" title="New file">${icon('plus')}</button></div><div class="tree" style="padding:6px">${tree}</div></div>
      <div class="panel"><div class="panel-head"><span class="mono small">knowledge/db-prod-1.md</span><span class="muted tiny mono">last: 8e1f0aa · platform-sre · 1m</span><div class="tools"><div class="seg" id="memv"><button class="on" data-v="read">Read</button><button data-v="edit">${icon('edit')}Edit</button></div></div></div>
        <div class="panel-body"><div id="mem-read" class="prose">${prose}</div>
        <div id="mem-edit" class="hide stack"><textarea class="textarea mono" rows="16">${esc(D.memory.file)}</textarea>
          <div class="field"><label>Commit message</label><input class="input" value="knowledge/db-prod-1: note pgbouncer pool size"></div>
          <div class="row"><span class="muted tiny">One commit, authored <span class="mono">ops (dashboard)</span>, trailers <span class="mono">Member-Role: operator · Edited-Via: dashboard</span>. The member sees it on its next read.</span><button class="btn primary" style="margin-left:auto" id="mem-commit">${icon('commit')}Commit</button></div></div></div></div>
      <div class="panel"><div class="panel-head"><h3>History</h3></div><div class="list">${D.memory.log.map((c) => `<div class="item" style="align-items:flex-start"><span class="mono tiny" style="color:var(--accent)">${c.sha}</span><div class="grow small"><div>${esc(c.msg)}</div><div class="mono tiny muted">${c.author} · ${ago(c.at)}</div><div class="mono tiny muted" style="opacity:.8">${c.trailers}</div></div></div>`).join('')}</div>
        <div class="panel-foot"><button class="btn sm">${icon('undo')}Revert…</button><button class="btn sm ghost">Consolidate now</button></div></div>
      </div>`);
  }

  /* ---------- workers ---------- */
  function workers(m) {
    const card = (w) => `<div class="wnode"><div class="wcard" data-s="${w.status}"><div class="row wrap" style="gap:6px">${st(w.status, '')}<b class="mono">${w.id}</b>${w.group ? tag('group ' + w.group) : ''}<span class="taint ${w.cls}">taint: ${w.cls}</span>${w.readOnly ? tag('read-only', 'ok') : ''}<span class="mono tiny muted" style="margin-left:auto">case ${w.case} · ${usd(w.cost)} · ${ago(w.started)}</span></div>
      <div class="small" style="margin:5px 0 3px">${esc(w.brief)}</div><div class="mono tiny muted">verbs: ${w.verbs}${w.note ? ` · <span style="color:var(--wait)">${w.note}</span>` : ''}</div>
      <div class="row" style="margin-top:6px;gap:6px"><a class="btn sm" href="#/${w.status === 'run' ? 'live' : 'runs'}/${w.status === 'run' ? 'r-7f29' : 'r-7e52'}">${icon('runs')}Run</a>${w.status === 'run' ? `<button class="btn sm danger">Stop</button>` : ''}</div></div></div>`;
    return shell(m, 'workers', `<div class="grid g-side"><div class="panel"><div class="panel-head"><h3>Worker tree</h3><span class="muted small">instructions flow down, authority never grows</span></div><div class="wtree">
      <div class="wnode"><span class="sigil sm" style="--sig:${m.color}">${m.name[0]}</span><div><b>home ${m.session}</b> <span class="muted small">live · paseo · turn ${m.turns}</span></div></div>
      <div class="wkids">${D.workers.filter((w) => w.parent === 'home s7').map(card).join('')}</div>
      <div class="wnode" style="opacity:.75"><span class="sigil sm" style="--sig:var(--ink-4)">${m.name[0]}</span><div><b>home s6</b> <span class="muted small">retired · token revoked</span></div></div>
      <div class="wkids">${D.workers.filter((w) => w.parent === 'home s6').map(card).join('')}</div>
      <div class="wnode"><span class="sigil sm" style="--sig:var(--shadow)">${icon('eye')}</span><div><b>proactive sweeps</b> <span class="muted small">every ${m.proactive ? m.proactive.every : '—'} · ${m.proactive ? m.proactive.budget : ''}</span></div></div>
      <div class="wkids">${D.workers.filter((w) => w.parent === 'sweep').map(card).join('')}</div>
    </div></div>
    <div class="stack"><div class="callout small">A child carries its member's verbs or fewer, and the <b>less trusted</b> of its case's requester and its parent's trust. Children can't spawn, join, tell or message peers.</div>
      <div class="callout info small">Only the parent (the home session) tells a worker. From here you can open a worker's run, or stop it.</div>
      <div class="panel"><div class="panel-head"><h3>Limits</h3></div><div class="panel-body"><dl class="kv small"><dt>max_concurrent</dt><dd class="mono">4 (2 live)</dd><dt>max_per_case</dt><dd class="mono">3</dd><dt>runtime</dt><dd class="mono">claude-cli · sandboxed</dd><dt>timeout</dt><dd class="mono">2h</dd></dl></div></div></div></div>`);
  }

  /* ---------- access ---------- */
  function access(m) {
    const a = D.access;
    return shell(m, 'access', `<div class="callout small" style="margin-bottom:14px">Grants and verbs live in config, so each change here goes through the config pipeline: a node-level YAML edit, validated on a copy, shown as a diff, applied with a backup. <b>Widening</b> (a new verb, a looser tier, a promotion, a credential) also asks for your passkey. Revoking a saved browser login is immediate.</div>
      <div class="grid g2">
        <div class="panel"><div class="panel-head"><h3>Verbs &amp; autonomy</h3><span class="muted small">skill.verbs × autonomy.tiers</span><div class="tools"><button class="btn sm">${icon('plus')}Add verb</button></div></div>
          <table class="tbl compact"><thead><tr><th>verb</th><th>tier</th><th>promoted</th><th></th></tr></thead><tbody>${a.verbs.map((v) => `<tr><td class="mono">${v.v}</td><td><select class="select" style="height:24px;width:130px;font-family:var(--font-mono);font-size:10.5px">${['observe', 'reversible', 'propose', 'commit_money', 'never'].map((t) => `<option${t === v.tier ? ' selected' : ''}>${t}</option>`).join('')}</select></td><td>${v.tier === 'reversible' ? `<button class="switch${v.promoted ? ' on' : ''}" onclick="this.classList.toggle('on')"></button>` : '<span class="muted tiny">—</span>'}</td><td class="num"><button class="icon-btn" title="Remove">${icon('trash')}</button></td></tr>`).join('')}</tbody></table>
          <div class="panel-foot small muted">An unclassified verb is <span class="tier propose">propose</span>. Promotion takes effect only while sessions are sandboxed (they are).</div></div>
        <div class="stack">
          <div class="panel"><div class="panel-head"><h3>Credential bindings</h3><div class="tools"><button class="btn sm">${icon('plus')}Bind</button></div></div><div class="list">${a.credentials.map((c) => `<div class="item"><span class="icon-btn">${icon('key')}</span><div class="grow"><b>${c.name}</b> <span class="mono tiny muted">${c.origin}</span><div class="mono tiny muted">${c.fields} · ${c.vault}:${c.ref}</div></div><a class="btn sm ghost" href="#/browser/b-01">Use</a><button class="btn sm danger">Unbind</button></div>`).join('')}</div></div>
          <div class="panel"><div class="panel-head"><h3>Browser logins</h3><div class="tools"><a class="btn sm" href="#/browser/seed">${icon('hand')}Seed a login</a></div></div><div class="list">${a.logins.map((l) => `<div class="item"><span class="icon-btn">${icon('cookie')}</span><div class="grow"><b>${l.site}</b><div class="mono tiny muted">${l.cookies} cookies · seeded ${ago(l.seeded)} ago · operator requests only</div></div><button class="btn sm danger" onclick="UI.toast('Logged out of console.example.com','saved jar deleted · audited','ok')">Revoke</button></div>`).join('')}</div></div>
          <div class="panel"><div class="panel-head"><h3>Operators who can approve</h3></div><div class="list"><div class="item"><span class="avatar">op</span><b>ops</b><span class="muted small grow">ssh-ed25519 …Example · 2 passkeys</span>${tag('you', 'ember')}</div></div></div>
        </div></div>`);
  }

  /* ---------- definition editor ---------- */
  function settings(m, isNew) {
    const name = isNew ? 'new-member' : m.name;
    const y = `members:
  ${name}:
    description: "${isNew ? '' : m.desc}"
    charter_file: members/${name}.md
    escalation: "Page the operator if a sev1 is not mitigated in 15 minutes."
    operators: [ops]
    home:
      rotate: { max_age: 24h, max_turns: 100 }
    worker:
      runtime: claude-cli
      timeout: 2h
      max_concurrent: 4
    skill:
      verbs: [kv.get, kv.set, gh.get_*, gh.comment, slack-ops.post]
    autonomy:
      shadow: ${isNew ? 'true ' : 'false'}             # ${isNew ? 'the default for a new member' : 'approved execution'}
      tiers:
        observe: [gh.get_*, kv.get]
        reversible: [slack-ops.post]
        propose: [gh.comment, kv.set]
        never: [gh.delete_*]
      promote: [slack-ops.post]
      approval_ttl: 24h
    proactive:
      every: 4h
      quiet_hours: "22:00-07:00"
      budget: { max_runs_per_day: 6, max_cost_per_day: 2.00 }
      checklist: { section: "Proactive checks" }
      notify_at: high
    peers: [billing-sre]
    isolation: { mode: ${m.mode === 'container' ? 'container' : 'namespace'} }`;
    const sec = (t, body) => `<div class="panel"><div class="panel-head"><h3>${t}</h3></div><div class="panel-body stack">${body}</div></div>`;
    return (isNew ? (b) => `<div class="page">${UI.head('Define a member', { crumbs: '<a href="#/members">Members</a>' })}${b}</div>` : (b) => shell(m, 'settings', b))(`
      <div class="grid g2" style="align-items:start">
        <div class="stack">
          ${sec('Identity &amp; charter', `<div class="grid g2"><div class="field"><label>Name</label><input class="input mono" value="${name}" ${isNew ? '' : 'disabled'}></div><div class="field"><label>Operators</label><input class="input mono" value="ops"></div></div>
            <div class="field"><label>Description</label><input class="input" value="${isNew ? '' : esc(m.desc)}"></div>
            <div class="field"><label>Charter <span class="muted">· members/${name}.md</span></label><textarea class="textarea" rows="6">You are the platform services' first-line SRE. Triage pages, investigate with read-only tools, propose fixes, and escalate per the policy below.

## Proactive checks
- error budgets for api and web
- NAT and egress cost vs last week</textarea><div class="hint">Config only: nothing on the agent-facing surface writes it.</div></div>
            <div class="field"><label>Escalation</label><input class="input" value="Page the operator if a sev1 is not mitigated in 15 minutes."></div>`)}
          ${sec('Autonomy', `<label class="row small"><button class="switch${isNew ? ' on' : ''}" onclick="this.classList.toggle('on')"></button><b>Shadow mode</b> <span class="muted">nothing above observe executes; proposals are a calibration record</span></label>
            <div class="grid g2"><div class="field"><label>Approval TTL</label><input class="input mono" value="24h"></div><div class="field"><label>Spend cap (commit_money)</label><input class="input mono" placeholder="per_action 2000 · per_period 5000 / 720h"></div></div>
            <div class="hint">Tiers per verb are on the Access tab. Promotion needs the sandbox; validation warns if it can't take effect.</div>`)}
          ${sec('Proactive sweeps', `<label class="row small"><button class="switch on" onclick="this.classList.toggle('on')"></button>Enabled (read-only, enforced by the gate)</label>
            <div class="grid g3"><div class="field"><label>Every</label><input class="input mono" value="4h"></div><div class="field"><label>Quiet hours</label><input class="input mono" value="22:00-07:00"></div><div class="field"><label>Notify at</label><select class="select"><option>high</option><option>medium</option><option>low</option></select></div></div>
            <div class="grid g2"><div class="field"><label>Max runs / day</label><input class="input mono" value="6"></div><div class="field"><label>Max cost / day</label><input class="input mono" value="2.00"></div></div>`)}
          ${sec('Sessions, workers, isolation, peers', `<div class="grid g3"><div class="field"><label>Rotate after</label><input class="input mono" value="24h"></div><div class="field"><label>…or turns</label><input class="input mono" value="100"></div><div class="field"><label>Worker runtime</label><select class="select"><option>claude-cli</option><option>oc</option></select></div></div>
            <div class="field"><label>Sandbox</label><div class="seg"><button class="${m.mode !== 'container' ? 'on' : ''}">namespace</button><button class="${m.mode === 'container' ? 'on' : ''}">container</button><button disabled title="isolation: {mode: none} is set on a runtime, workflow or trigger — never here">none</button></div><div class="hint">The container backend runs each identity in its own container (<span class="mono">--network none</span>, no capabilities).</div></div>
            <div class="field"><label>Peers</label><input class="input mono" value="billing-sre"><div class="hint">Both sides must list each other (or use member_links:). Linking is a channel for data to cross.</div></div>`)}
        </div>
        <div class="stack" style="position:sticky;top:12px">
          <div class="panel"><div class="panel-head"><h3>As YAML</h3><span class="muted small">config.yaml · node <span class="mono">members.${name}</span></span><div class="tools">${st('ok', 'validates')}</div></div><div class="panel-body tight">${UI.yaml(y, { hl: isNew ? [] : [18, 19, 28] })}</div>
            <div class="panel-foot"><span class="muted small">2 warnings: none · 0 errors</span><a class="btn primary" style="margin-left:auto" href="#/config/apply">${icon('check')}Review changes</a></div></div>
        </div></div>`);
  }

  UI.route('members/:name/:tab?/:sub?', ({ name, tab, sub }) => {
    if (name === 'new') return settings(D.members[2], true);
    const m = M(name);
    switch (tab || 'chat') {
      case 'cases': return cases(m);
      case 'proposals': return { toString: () => proposals(m, sub), after: (main) => {
        const p = sub && D.proposals.find((x) => x.id === sub);
        UI.bind(main, '#ap', 'click', () => UI.approve(p, 'approve', () => UI.render()));
        UI.bind(main, '#dn', 'click', () => UI.approve(p, 'deny', () => UI.render()));
      } };
      case 'memory': return { toString: () => memory(m), after: (main) => {
        UI.bind(main, '#memv button', 'click', (e, b) => { UI.$$('#memv button').forEach((x) => x.classList.toggle('on', x === b)); UI.$('#mem-read').classList.toggle('hide', b.dataset.v !== 'read'); UI.$('#mem-edit').classList.toggle('hide', b.dataset.v !== 'edit'); });
        UI.bind(main, '#mem-commit', 'click', () => UI.toast('Committed c2f7a19 to platform-sre memory', 'Member-Role: operator · Edited-Via: dashboard', 'ok'));
      } };
      case 'workers': return workers(m);
      case 'access': return access(m);
      case 'settings': return settings(m);
      default: return { toString: () => chat(m), after: bindChat };
    }
  }, { nav: 'members' });
})();
