/* The bot chat: a thread and a composer, shared by a bot's Chat tab (and later the Chat area).
 * The thread is a normal chat scroller: messages flow top-down, the newest at the bottom, the view anchored there; a
 * short conversation sits against the composer, never with a gap in the middle. A burst of tool calls is one quiet
 * line ("Ran 2 commands · started 2 read-only workers") that opens to each command and what came back. An approval the
 * bot asks for is a card in the thread. What you attach is data you gave it: images as thumbnails that open larger,
 * files as chips with their name and size, links as cards.
 * The composer mocks the attachment API (docs/design/dashboard.md §5.3, "Chat attachments"): each file is uploaded
 * first (POST …/attachments → an id), then the message refers to the ids (POST …/say {text, attachments}); a message
 * is queued until the bot is idle, then delivered. */
(function () {
  const esc = UI.esc, icon = UI.icon;
  /** What you can attach: the same limits the daemon enforces (GET /api/v1/chat/limits). */
  const LIMITS = UI.chatLimits = { files: 10, bytes: 20 * 1024 * 1024,
    types: [['image', /^image\/(png|jpeg|gif|webp)$/, 'images (PNG, JPEG, GIF, WebP)'], ['file', /^(application\/pdf|text\/.*|application\/(json|x-yaml|yaml)|application\/x-ndjson)$/, 'PDF and text (logs, CSV, JSON, YAML, Markdown)']],
    exts: /\.(png|jpe?g|gif|webp|pdf|txt|log|csv|json|ya?ml|md|ndjson)$/i };
  const ACCEPT = 'image/png,image/jpeg,image/gif,image/webp,application/pdf,text/*,.log,.csv,.json,.yaml,.yml,.md,.ndjson';
  UI.bytes = (n) => (n < 1024 ? `${n} B` : n < 1048576 ? `${Math.round(n / 1024)} KB` : `${(n / 1048576).toFixed(n < 10485760 ? 1 : 0)} MB`);
  const URL_RE = /\bhttps?:\/\/[^\s<>"']+[^\s<>"'.,;:!?)]/g;
  const linkOf = (u) => { try { const x = new URL(u); return { url: u, host: x.host, path: (x.pathname + x.search).replace(/^\/$/, '') }; } catch (e) { return null; } };
  const linkify = (html) => html.replace(/\bhttps?:\/\/[^\s<>"']+[^\s<>"'.,;:!?)]/g, (u) => `<a class="dl-link" href="${u}" target="_blank" rel="noopener noreferrer">${u}</a>`);

  /* ---------- the thread ---------- */
  const toolWords = (ts) => { const sp = ts.filter((t) => t.kind === 'spawn'), cmd = ts.length - sp.length, ro = sp.length && sp.every((t) => t.readOnly);
    return [cmd ? `Ran ${cmd} command${cmd === 1 ? '' : 's'}` : '', sp.length ? `started ${sp.length}${ro ? ' read-only' : ''} worker${sp.length === 1 ? '' : 's'}` : ''].filter(Boolean).join(' · ').replace(/^s/, 'S'); };
  const toolItem = (t) => `<li><span class="cm-tw">${t.kind === 'spawn' ? `Started a ${t.readOnly ? 'read-only ' : ''}worker: ${esc(t.brief || '')}` : 'Ran a command'}<span class="dl-sub">${UI.clock(t.at).slice(0, 5)}</span></span>
    <code class="cm-cmd">${esc(t.text)}</code>${t.out ? `<pre class="cm-out">${esc(t.out)}</pre>` : ''}</li>`;
  const tools = (ts) => `<details class="cm-row cm-tools"><summary>${icon('chev')}<span>${toolWords(ts)}</span></summary><ol>${ts.map(toolItem).join('')}</ol></details>`;
  const card = (id, bot) => { const p = (D.proposals || []).find((x) => x.id === id && (!bot || x.bot === bot)); if (!p) return '';
    const pend = p.status === 'pending', st = pend ? 'Waiting for you' : p.status[0].toUpperCase() + p.status.slice(1);
    return `<a class="cm-card${pend ? ' pend' : ''}" href="#/bots/${p.bot}/proposals/${p.id}">${UI.mark(pend ? 'ember' : p.status === 'executed' || p.status === 'approved' ? 'ok' : 'fail')}<span class="cm-ct"><b>${esc(p.title || p.summary)}</b>${UI.factsLine([UI.envFact(p.env), UI.actionFact(p.verb, p.env, p.tier)])}<span class="dl-sub">${st}${pend && p.expires ? ` · expires in ${esc(p.expires)}` : ''}</span></span>${pend ? '<span class="btn sm secondary">Review</span>' : ''}</a>`; };
  const att = (a, i, mi) => a.kind === 'obj' ? `<a class="cm-file" href="${esc(a.href)}">${icon(a.obj === 'ticket' ? 'ticket' : 'runs')}<span>${esc(a.name)}</span><span class="dl-sub">${a.obj}</span></a>`
    : a.kind === 'ref' ? `<span class="cm-file">${icon('key')}<span>${esc(a.name)}</span><span class="dl-sub">vault reference · value never shown</span></span>`
    : a.kind === 'image'
    ? `<button class="cm-img" data-act="chat-img" data-mi="${mi}" data-i="${i}" aria-label="Open ${esc(a.name)} larger"><img src="${esc(a.src || a.thumb)}" alt="${esc(a.name)}"></button>`
    : `<button class="cm-file" data-act="chat-img" data-mi="${mi}" data-i="${i}">${icon('file')}<span>${esc(a.name)}</span><span class="dl-sub">${UI.bytes(a.size)}</span></button>`;
  const linkCard = (l) => `<a class="cm-link" href="${esc(l.url)}" target="_blank" rel="noopener noreferrer">${icon('globe')}<span><b>${esc(l.host)}</b><span class="dl-sub">${esc(l.path || '/')}</span></span></a>`;
  const stateWord = (c, bot) => c.state === 'queued' ? `<span class="cm-q">${icon('clock')}Queued: delivered when ${esc(c.toWord || UI.botName(bot))} is idle</span>` : c.state === 'delivered' ? '<span>Delivered</span>' : '';
  // on a bot's own chat, any message can become a ticket for it
  const mk = (mi, o) => (o && o.ticket ? '' : `<button class="cm-mk" data-act="chat-ticket" data-mi="${mi}" aria-label="Make a ticket from this message" title="Make a ticket from this message">${icon('ticket')}</button>`);
  const msg = (c, bot0, mi, o) => { const bot = c.bot || bot0;
    if (c.who === 'sys') return `<div class="cm-row cm-sys"><span>${UI.taskWords(c.text, true)}</span><span class="dl-sub">${UI.clock(c.at).slice(0, 5)}</span></div>`;
    // an incoming event is a key moment: a solid card with its source, a plain headline, how severe it is, its facts
    // and links; the raw event folds away; the label says plainly it's data the bot reads, never orders it takes
    if (c.who === 'event') { const t = (D.cases || []).find((x) => x.id === c.case), e = c.ev || {}, src = c.source || 'an event', high = /^(high|critical)$/.test(e.level || '') || /SEV[12]/i.test(e.severity || '');
      const icon_ = e.kind === 'incident' ? 'obj-incident' : e.kind === 'issue' ? 'obj-issue' : e.kind === 'check' ? 'obj-check' : 'zap';
      const facts = [e.severity ? { icon: 'warn', value: e.severity, risky: high, pri: 1, label: 'Severity' } : null, ...(e.fields || []).map(([k, v]) => ({ icon: k === 'service' ? 'server' : k === 'region' ? 'globe' : k === 'for' ? 'clock' : 'dot', value: v, pri: 2, label: k })),
        t ? { icon: 'ticket', value: t.title, href: `#/tickets/${t.id}`, pri: 1, label: 'Ticket' } : null, e.url ? { icon: 'external', value: `Open in ${src}`, href: e.url, pri: 2, label: 'Source' } : null];
      return `<div class="cm-row cm-event"><article class="cm-evc" data-sev="${high ? 'high' : 'info'}"><header class="cm-evh">${icon(icon_)}<span>${esc(src)}${e.kind ? ` ${esc(e.kind)}` : ''}</span><span class="dl-sub">${UI.clock(c.at).slice(0, 5)}</span></header>
        <b class="cm-evt">${esc(e.title || c.text)}</b>${UI.factsLine(facts)}
        <p class="cm-evnote">${icon('shield')}Data from ${esc(src)}: ${esc(UI.botName ? UI.botName(bot) : bot)} reads it, never takes orders from it</p>
        ${e.title ? `<details class="cm-evraw"><summary>${icon('chev')}The event as it arrived</summary><code class="cm-cmd">${esc(c.text)}</code></details>` : ''}</article></div>`; }
    if (c.who === 'op') { const as = c.attachments || [], ls = c.links || [];
      return `<div class="cm-row cm-op"><div class="cm-meta">${mk(mi, o)}<span>You</span><span class="dl-sub">${UI.clock(c.at).slice(0, 5)}</span>${stateWord(c, bot)}</div>
        ${c.text ? `<div class="cm-bubble">${UI.mentionHTML(linkify(UI.md(c.text)))}</div>` : ''}
        ${as.length || ls.length ? `<div class="cm-atts"><span class="cm-given">${icon('attach')}Data you gave it</span>${as.map((a, i) => att(a, i, mi)).join('')}${ls.map(linkCard).join('')}</div>` : ''}</div>`; }
    if (c.who === 'typing') return `<div class="cm-row cm-bot" id="typing" ${UI.botAttrs(bot)}>${UI.botAv(bot, 'msg')}<div><div class="cm-meta">${UI.botWho(bot)}<span class="dl-sub">is working…</span></div><div class="cm-bubble typing"><i></i><i></i><i></i></div></div></div>`;
    return `<div class="cm-row cm-bot" ${UI.botAttrs(bot)}>${UI.botAv(bot, 'msg')}<div><div class="cm-meta">${UI.botWho(bot)}<span class="dl-sub">${UI.clock(c.at).slice(0, 5)}</span>${mk(mi, o)}</div><div class="cm-bubble">${UI.md(c.text)}</div>${c.proposal ? card(c.proposal, bot) : ''}</div></div>`;
  };
  /** The thread's rows: consecutive tool calls fold into one group line. */
  UI.chatRows = (L, bot, o) => { const out = []; for (let i = 0; i < L.length; i++) {
      if (L[i].who === 'tool') { const ts = []; while (i < L.length && L[i].who === 'tool') ts.push(L[i++]); i--; out.push(tools(ts)); }
      else out.push(msg(L[i], bot, i, o)); }
    return out.join(''); };
  /** The thread. o.ticket: a ticket's comments (its assignee answers there); o.empty: what an empty thread says. */
  UI.chatThread = (L, bot, o = {}) => `<div class="cm-thread" id="msgs" role="log" aria-label="${o.ticket ? 'Comments' : `Conversation with ${esc(bot)}`}"${o.ticket ? ` data-t="${esc(o.ticket)}"` : ''}>${L.length ? UI.chatRows(L, bot, o) : `<p class="cm-empty dl-sub">${o.empty || `No conversation yet. Ask ${esc(bot)} anything; it answers here.`}</p>`}</div>`;
  // where a thread's rows live: a bot's home conversation, or a ticket's comments (UI.ticketRows, js/screens/tickets.js)
  const ctx = () => { const k = UI.$('.cm-comp'); return { bot: k && k.dataset.bot, t: k && k.dataset.t }; };
  // a team's group chat lives under the key team:<name>
  const rowsOf = (bot, t) => (t ? UI.ticketRows(t) : /^team:/.test(bot) ? (D.teamChats[bot.slice(5)] = D.teamChats[bot.slice(5)] || []) : D.chats[bot] || []);
  const redraw = (bot, extra = '', t) => { const box = UI.$('#msgs'); if (!box) return; box.innerHTML = UI.chatRows(rowsOf(bot, t), bot, { ticket: t }) + extra; box.scrollTop = box.scrollHeight; };
  /** Anchor to the newest message whenever the thread is drawn. */
  UI.chatAnchor = () => { const box = UI.$('#msgs'); if (box) box.scrollTop = box.scrollHeight; };

  UI.act('chat-img', (b) => { const { bot, t } = ctx(), c = rowsOf(bot, t)[+b.dataset.mi], a = c && (c.attachments || [])[+b.dataset.i]; if (!a) return;
    UI.modal(`<div class="modal-head"><h2>${esc(a.name)}</h2><button class="icon-btn" data-close aria-label="Close">${icon('x')}</button></div>
      <div class="modal-body"><p class="dl-sub" style="margin:0 0 10px">${icon('attach')} Data you gave ${esc(bot)} · ${UI.bytes(a.size)} · ${esc(a.mime)} · ${UI.agoText(c.at)}</p>
      ${a.kind === 'image' ? `<img class="cm-big" src="${esc(a.src || a.thumb)}" alt="${esc(a.name)}">` : a.preview ? `<pre class="cm-out cm-prev">${esc(a.preview)}</pre>` : `<p class="dl-sub">No preview for this kind of file. ${esc(bot)} reads it from its inbox.</p>`}</div>`, { wide: true }); });

  /* ---------- the composer ---------- */
  const P = (UI._chatPend = UI._chatPend || {}); // per bot: { items: [], gone: Set of dismissed link URLs, err }
  const pend = (bot) => (P[bot] = P[bot] || { items: [], gone: [], err: '' });
  const typeOf = (f) => { const t = LIMITS.types.find(([, re]) => re.test(f.type || '')); return t ? t[0] : LIMITS.exts.test(f.name) ? (/\.(png|jpe?g|gif|webp)$/i.test(f.name) ? 'image' : 'file') : null; };
  const pendItem = (a, i) => `<span class="cm-p${a.status === 'uploading' ? ' up' : ''}" data-i="${i}" data-kind="${a.kind}">${a.kind === 'image' && a.thumb ? `<img src="${a.thumb}" alt="">` : icon(a.kind === 'link' ? 'globe' : a.kind === 'ref' ? 'key' : a.kind === 'obj' ? (a.obj === 'ticket' ? 'ticket' : 'runs') : 'file')}
    <span class="cm-pt"><b>${esc(a.kind === 'link' ? a.host : a.name)}</b><span class="dl-sub">${a.kind === 'link' ? 'link · shown as a card' : a.kind === 'ref' ? 'a vault reference · its value is never shown' : a.kind === 'obj' ? `a ${a.obj}` : a.status === 'uploading' ? 'uploading…' : UI.bytes(a.size)}</span></span>
    <button class="icon-btn sm" data-act="chat-unattach" data-i="${i}" aria-label="Remove ${esc(a.kind === 'link' ? a.url : a.name)}">${icon('x')}</button></span>`;
  const links = (bot) => { const ta = UI.$('#say'), st = pend(bot); return [...new Set(((ta && ta.value) || '').match(URL_RE) || [])].filter((u) => !st.gone.includes(u)).map(linkOf).filter(Boolean).map((l) => ({ kind: 'link', ...l })); };
  const drawPend = (bot) => { const el = UI.$('#cm-pend'); if (!el) return; const st = pend(bot), all = [...st.items, ...links(bot)];
    el.innerHTML = all.map(pendItem).join(''); el.hidden = !all.length; const er = UI.$('#cm-err'); if (er) { er.textContent = st.err; er.hidden = !st.err; } };
  // the limits are said where they matter: in the attach button's tooltip, and in words when a file is refused
  const limitWords = `Attach up to ${LIMITS.files} files, ${UI.bytes(LIMITS.bytes)} each: images, PDF and text; paste or drop them too`;
  UI.chatComposer = (bot, o = {}) => { const st = pend(bot);
    return `<div class="cm-comp" data-bot="${esc(bot)}"${o.ticket ? ` data-t="${esc(o.ticket)}"` : ''}><div class="cm-pend" id="cm-pend"${st.items.length ? '' : ' hidden'}>${st.items.map(pendItem).join('')}</div>
      <p class="cm-err" id="cm-err" role="alert"${st.err ? '' : ' hidden'}>${esc(st.err)}</p>
      <div class="cm-in"><div class="cm-field"><button type="button" class="cm-attach" data-act="chat-attach" aria-label="Attach files or images" title="${esc(limitWords)}">${icon('attach')}</button><input type="file" id="cm-file" multiple accept="${ACCEPT}" hidden>
        <span class="cm-tw"><span class="cm-mirror" aria-hidden="true"></span><textarea class="cm-ta" id="say" rows="1" role="combobox" aria-autocomplete="list" aria-expanded="false" aria-controls="mx-pop" placeholder="${o.ticket ? 'Add a comment' : `Message ${esc(o.to || UI.botName(bot))}`}" aria-label="${o.ticket ? 'Comment' : `Message ${esc(o.to || UI.botName(bot))}`}" aria-describedby="cm-hint"></textarea></span></div>
        <button class="btn primary cm-send" data-act="chat-send" data-m="${esc(bot)}"${o.ticket ? ` data-t="${esc(o.ticket)}"` : ''} aria-label="${o.ticket ? 'Post comment' : 'Send'}" title="${o.ticket ? 'Post comment' : 'Send'} (Enter; Shift+Enter for a new line)">${icon('send')}</button></div>
      <span class="sr-only" id="cm-hint">Enter sends; Shift+Enter starts a new line. ${esc(limitWords)}.</span></div>`; };
  /** Add files: check each against the limits, then upload it (the mock of POST …/attachments). */
  const addFiles = (bot, files) => { const st = pend(bot), errs = [];
    for (const f of files) {
      const k = typeOf(f), name = f.name && f.name !== 'image.png' ? f.name : `Pasted image ${UI.clock(new Date().toISOString()).slice(0, 5).replace(':', '.')}.png`;
      if (st.items.length >= LIMITS.files) { errs.push(`${name} wasn’t attached: up to ${LIMITS.files} files per message`); continue; }
      if (!k) { errs.push(`${name} wasn’t attached: only images, PDF and text files can be attached`); continue; }
      if (f.size > LIMITS.bytes) { errs.push(`${name} wasn’t attached: it’s ${UI.bytes(f.size)}, and the limit is ${UI.bytes(LIMITS.bytes)} each`); continue; }
      const a = { kind: k, name, mime: f.type || 'text/plain', size: f.size, status: 'uploading' }; st.items.push(a);
      const done = () => { a.status = 'ready'; a.id = 'att_' + Math.random().toString(16).slice(2, 6); drawPend(bot); };
      if (k === 'image' && f.size && typeof FileReader !== 'undefined') { const r = new FileReader(); r.onload = () => { thumb(r.result).then((t) => { a.thumb = t; setTimeout(done, UI.fast ? 20 : 500); }); }; r.onerror = done; r.readAsDataURL(f); }
      else if (k === 'file' && f.size && f.size < 200000 && f.text) f.text().then((t) => { a.preview = t.slice(0, 4000); setTimeout(done, UI.fast ? 20 : 500); }, done);
      else setTimeout(done, UI.fast ? 20 : 500);
    }
    st.err = errs.join('. '); drawPend(bot); };
  // a thumbnail small enough to keep in the demo's saved state
  const thumb = (src) => new Promise((res) => { const im = new Image(); im.onload = () => { const s = Math.min(1, 640 / Math.max(im.width, im.height)), c = document.createElement('canvas'); c.width = Math.max(1, im.width * s); c.height = Math.max(1, im.height * s);
    try { c.getContext('2d').drawImage(im, 0, 0, c.width, c.height); res(c.toDataURL('image/jpeg', 0.8)); } catch (e) { res(src); } }; im.onerror = () => res(src); im.src = src; });
  UI.chatAddFiles = addFiles;
  const botOf = () => (UI.$('.cm-comp') || {}).dataset?.bot;
  // the paperclip opens what you can attach (62): a file, a photo, a link, a vault item (its reference; the value is
  // never shown), a run or a ticket; on a phone the same menu is an action sheet
  let attachFor = null;
  UI.act('chat-attach', (b) => { attachFor = (b.closest('.cm-comp') || {}).dataset ? b.closest('.cm-comp').dataset.bot : null;
    UI.menu(b, [['Upload a file', 'file', 'cm-att-file'], ['Photo or image', 'image', 'cm-att-img'], ['Paste a link', 'globe', 'cm-att-link'], ['-'], ['From a vault item', 'key', 'cm-att-ref', D.secrets && D.secrets.length ? '' : UI.off('No vault items yet')], ['Attach a run or ticket', 'runs', 'cm-att-obj']]); });
  UI.act('cm-att-file', () => { const f = UI.$('#cm-file'); if (f) { f.accept = ACCEPT; f.click(); } });
  UI.act('cm-att-img', () => { const f = UI.$('#cm-file'); if (f) { f.accept = 'image/*'; f.click(); setTimeout(() => { f.accept = ACCEPT; }, 500); } });
  UI.act('cm-att-link', () => UI.form({ caps: 'Attach', title: 'Paste a link', fields: [{ k: 'url', type: 'string', req: true, label: 'Link', ph: 'https://…', pattern: /^https?:\/\/\S+$/, patternMsg: 'A web address, starting https://' }], submit: 'Attach',
    onSubmit: (v) => { const ta = UI.$('#say'); if (ta) { ta.value = `${ta.value ? ta.value.replace(/\s*$/, ' ') : ''}${v.url} `; ta.dispatchEvent(new Event('input', { bubbles: true })); } if (attachFor) drawPend(attachFor); } }));
  const keyOf = (x) => x.key || String(x.ref || '').split('/').slice(1).join('/');
  UI.act('cm-att-ref', () => UI.form({ caps: 'Attach', title: 'From a vault item', intro: '<p class="dl-sub">It attaches the reference; the value is never shown, here or to the bot.</p>', fields: [{ k: 'ref', type: 'enum', req: true, label: 'Vault item', options: (S.view().secrets || []).map((x) => [`${x.vault}:${keyOf(x)}`, `${x.vault} · ${keyOf(x)}`]) }], submit: 'Attach',
    onSubmit: (v) => { if (!attachFor) return; const [vault, ...k] = v.ref.split(':'); pend(attachFor).items.push({ kind: 'ref', name: `${vault} · ${k.join(':')}`, ref: `{{ vault "${vault}" "${k.join(':')}" }}` }); drawPend(attachFor); } }));
  UI.act('cm-att-obj', () => UI.form({ caps: 'Attach', title: 'Attach a run or ticket', fields: [{ k: 'o', type: 'enum', req: true, label: 'Run or ticket', options: [...(D.cases || []).filter((c) => c.status !== 'closed').slice(0, 12).map((c) => [`t:${c.id}`, `Ticket · ${c.title}`]), ...D.runs.slice(0, 12).map((r) => [`r:${r.id}`, `Run · ${UI.runTitle ? UI.runTitle(r) : r.workflow} (${UI.agoText(r.started)})`])] }], submit: 'Attach',
    onSubmit: (v) => { if (!attachFor) return; const [k, id] = v.o.split(':'), c = k === 't' && (D.cases || []).find((x) => x.id === id), r = k === 'r' && D.runs.find((x) => x.id === id);
      pend(attachFor).items.push({ kind: 'obj', obj: k === 't' ? 'ticket' : 'run', id, name: c ? c.title : r ? (UI.runTitle ? UI.runTitle(r) : r.workflow) : id, href: k === 't' ? `#/tickets/${id}` : `#/runs/${id}` }); drawPend(attachFor); } }));
  UI.act('chat-unattach', (b) => { const bot = botOf(), st = pend(bot), i = +b.dataset.i;
    if (i < st.items.length) st.items.splice(i, 1); else { const l = links(bot)[i - st.items.length]; if (l) st.gone.push(l.url); }
    st.err = ''; drawPend(bot); const ta = UI.$('#say'); if (ta) ta.focus(); });
  document.addEventListener('change', (e) => { if (e.target.id === 'cm-file') { addFiles(botOf(), [...e.target.files]); e.target.value = ''; } });
  document.addEventListener('input', (e) => { if (e.target.id === 'say') { const ta = e.target; ta.style.height = 'auto'; ta.style.height = Math.min(ta.scrollHeight, 180) + 'px'; drawPend(botOf()); } });
  document.addEventListener('paste', (e) => { if (e.target.id !== 'say') return; const fs = [...((e.clipboardData && e.clipboardData.files) || [])]; if (fs.length) { e.preventDefault(); addFiles(botOf(), fs); } });
  // drag files anywhere over the chat to attach them
  const dropZone = (e) => e.target.closest && e.target.closest('.chat');
  const hasFiles = (e) => e.dataTransfer && [...(e.dataTransfer.types || [])].includes('Files');
  document.addEventListener('dragover', (e) => { const z = dropZone(e); if (z && hasFiles(e)) { e.preventDefault(); z.classList.add('drop'); } });
  document.addEventListener('dragleave', (e) => { const z = dropZone(e); if (z && !z.contains(e.relatedTarget)) z.classList.remove('drop'); });
  document.addEventListener('drop', (e) => { const z = dropZone(e); if (!z) return; e.preventDefault(); z.classList.remove('drop'); addFiles(botOf(), [...((e.dataTransfer && e.dataTransfer.files) || [])]); });

  /* ---------- sending: queued until the bot is idle, then delivered, then its answer ---------- */
  UI.act('chat-send', (b) => { const bot = b.dataset.m, t = b.dataset.t, ta = UI.$('#say'), st = pend(bot), v = ta.value.trim();
    if (st.items.some((a) => a.status === 'uploading')) { st.err = 'Still uploading: it sends once every file is attached'; return drawPend(bot); }
    if (!v && !st.items.length) return UI.nudge(ta, `Type a message for ${bot} first`);
    const clear = () => { P[bot] = { items: [], gone: [], err: '' }; ta.value = ''; ta.style.height = ''; drawPend(bot); ta.dispatchEvent(new Event('input', { bubbles: true })); };
    if (t) return UI.ticketComment(t, bot, { text: v, attachments: st.items, links: links(bot) }, clear);
    if (/^team:/.test(bot)) return UI.teamSay(bot.slice(5), { text: v, attachments: st.items, links: links(bot) }, clear);
    const L = (D.chats[bot] = D.chats[bot] || []), ls = links(bot).map(({ url, host, path }) => ({ url, host, path }));
    const c = { id: 't' + Date.now().toString(36), who: 'op', at: new Date().toISOString(), text: v, state: 'queued', attachments: st.items.map(({ id, kind, name, mime, size, thumb: t, preview }) => ({ id, kind, name, mime, size, src: t, preview })), links: ls };
    if (!c.attachments.length) delete c.attachments; if (!ls.length) delete c.links;
    L.push(c); P[bot] = { items: [], gone: [], err: '' }; ta.value = ''; ta.style.height = ''; drawPend(bot); ta.dispatchEvent(new Event('input', { bubbles: true })); S.save(); redraw(bot);
    setTimeout(() => { c.state = 'delivered'; S.save(); redraw(bot, msg({ who: 'typing' }, bot)); }, UI.fast ? 60 : 1600);
    setTimeout(() => { L.push({ id: 't' + Date.now().toString(36), who: 'bot', at: new Date().toISOString(), text: c.attachments ? `Thanks, I have ${c.attachments.length === 1 ? 'the file' : `all ${c.attachments.length} files`}. I’ll treat ${c.attachments.length === 1 ? 'it' : 'them'} as data you gave me and report back here. Anything that changes something still comes to you first.` : 'Got it. I’ll work on that as your request and report back here. Anything that changes something still comes to you for approval first.' }); S.save(); redraw(bot); }, UI.fast ? 140 : 3000);
  });
  UI.chatRedraw = redraw; UI.chatRowsOf = rowsOf;
  UI.act('chat-ticket', (b) => { const { bot } = ctx(); UI.go(`#/tickets/new?bot=${encodeURIComponent(bot)}&from=${b.dataset.mi}`); });
  document.addEventListener('keydown', (e) => { if (e.target.id === 'say' && e.key === 'Enter' && !e.shiftKey && !e.isComposing) { e.preventDefault(); const b = UI.$('[data-act="chat-send"]'); if (b) b.click(); } });
})();
UI.afterRender.push((main) => { if (main.querySelector('#msgs')) UI.chatAnchor(); });

/* @mentions in the composer (item 27), like Slack: typing @ opens a list of who you can mention (in a team's chat
 * its members first, the lead marked, and @everyone; then other bots and teams), filtered as you type; arrows,
 * Enter or Tab, or a click pick one; Esc closes. A picked mention is a token in the bot's colour (drawn on a layer
 * behind the field, so the field stays plain text), Backspace takes the whole token, and in a team's chat the To row
 * follows: a bot you mention is who it goes to; @everyone is everyone on the team. */
(function () {
  const esc = UI.esc;
  const ta = () => UI.$('#say'), comp = () => UI.$('.cm-comp');
  const teamOf = () => { const b = (comp() || {}).dataset?.bot || ''; return /^team:/.test(b) ? b.slice(5) : ''; };
  const members = (t) => D.bots.filter((b) => b.cfg && b.cfg.team === t).map((b) => b.name);
  const leadOf = (t) => { const x = D.teams.find((y) => y.name === t); return (x && ((S.staged('teams', t) || x.cfg || {}).routing || {}).lead) || members(t)[0]; };
  /** Who can be mentioned here: [{ key, word, sub, kind }]. */
  const people = () => { const t = teamOf(), out = [];
    if (t) { out.push({ key: 'everyone', word: 'everyone', sub: `everyone on team ${t}`, kind: 'all' }); const lead = leadOf(t);
      members(t).forEach((b) => out.push({ key: b, word: UI.botName(b), sub: b + (b === lead ? ' · lead' : ''), kind: 'bot' })); }
    D.bots.filter((b) => !t || !members(t).includes(b.name)).forEach((b) => out.push({ key: b.name, word: UI.botName(b.name), sub: b.name + (b.cfg && b.cfg.team ? ` · team ${b.cfg.team}` : ''), kind: 'bot' }));
    D.teams.filter((x) => x.name !== t).forEach((x) => out.push({ key: 'team:' + x.name, word: x.name, sub: `team · ${members(x.name).length} bots`, kind: 'team' }));
    return out; };
  // every word a mention can be: a bot's display name or id, a team's name, everyone
  const words = () => { const m = new Map(); D.bots.forEach((b) => { m.set(UI.botName(b.name).toLowerCase(), b.name); m.set(b.name.toLowerCase(), b.name); }); D.teams.forEach((t) => m.set(t.name.toLowerCase(), 'team:' + t.name)); m.set('everyone', 'everyone'); return m; };
  const RE = /@([A-Za-z][\w-]*)/g;
  /** Mentions in a sent message, as tokens. */
  UI.mentionHTML = (html) => { const w = words(); return html.replace(RE, (all, n) => { const k = w.get(n.toLowerCase()); if (!k) return all; return `<span class="mx-tok"${k.startsWith('team:') || k === 'everyone' ? '' : ` ${UI.botAttrs(k)}`}>@${esc(n)}</span>`; }); };
  /** The mentions in what's typed: [key…]. */
  UI.mentionsIn = (text) => { const w = words(), out = []; String(text || '').replace(RE, (a, n) => { const k = w.get(n.toLowerCase()); if (k && !out.includes(k)) out.push(k); }); return out; };
  // the layer behind the field: the same text, with each mention as a token
  const mirror = () => { const t = ta(), m = UI.$('.cm-mirror'); if (!t || !m) return; const w = words();
    m.innerHTML = esc(t.value).replace(RE, (all, n) => { const k = w.get(n.toLowerCase()); return k ? `<mark class="mx-tok"${k.startsWith('team:') || k === 'everyone' ? '' : ` ${UI.botAttrs(k)}`}>${all}</mark>` : all; }) + '​'; m.scrollTop = t.scrollTop; };
  const st = { open: false, i: 0, items: [], from: 0 };
  const pop = () => UI.$('#mx-pop');
  const close = () => { st.open = false; const p = pop(); if (p) p.remove(); const t = ta(); if (t) { t.setAttribute('aria-expanded', 'false'); t.removeAttribute('aria-activedescendant'); } };
  const draw = () => { let p = pop(); const c = comp(); if (!c) return; if (!p) { p = document.createElement('div'); p.id = 'mx-pop'; p.className = 'mx-pop'; p.setAttribute('role', 'listbox'); p.setAttribute('aria-label', 'Mention'); c.appendChild(p); }
    p.innerHTML = st.items.length ? st.items.map((x, i) => `<button type="button" class="mx-o${i === st.i ? ' on' : ''}" role="option" id="mx-o${i}" aria-selected="${i === st.i}" data-act="mx-pick" data-i="${i}">${x.kind === 'bot' ? UI.botAv(x.key, 'sm') : `<span class="mx-ic">${UI.icon(x.kind === 'all' ? 'team' : 'team')}</span>`}<b>${x.kind === 'all' ? '@everyone' : esc(x.word)}</b><span class="dl-sub">${esc(x.sub)}</span></button>`).join('') : '<p class="dl-sub mx-none">Nobody by that name.</p>';
    const t = ta(); t.setAttribute('aria-expanded', 'true'); if (st.items.length) t.setAttribute('aria-activedescendant', 'mx-o' + st.i);
    const on = p.querySelector('.mx-o.on'); if (on) on.scrollIntoView({ block: 'nearest' }); };
  const update = () => { const t = ta(); if (!t) return; const before = t.value.slice(0, t.selectionStart), m = before.match(/(^|\s)@([\w-]*)$/);
    if (!m) return close(); const q = m[2].toLowerCase(); st.from = before.length - m[2].length - 1;
    st.items = people().filter((x) => !q || x.word.toLowerCase().startsWith(q) || x.key.toLowerCase().replace(/^team:/, '').startsWith(q) || (x.kind === 'all' && 'everyone'.startsWith(q))).slice(0, 8);
    st.i = Math.min(st.i, Math.max(0, st.items.length - 1)); st.open = true; draw(); };
  const syncTo = () => { const tm = teamOf(); if (tm && UI.teamToSync) UI.teamToSync(tm, UI.mentionsIn((ta() || {}).value)); };
  const pick = (i) => { const x = st.items[i], t = ta(); if (!x || !t) return; const word = '@' + (x.kind === 'all' ? 'everyone' : x.word) + ' ';
    const end = t.selectionStart; t.value = t.value.slice(0, st.from) + word + t.value.slice(end); const at = st.from + word.length; t.setSelectionRange(at, at); close(); mirror(); syncTo(); t.focus(); t.dispatchEvent(new Event('input', { bubbles: true })); };
  UI.act('mx-pick', (b) => pick(+b.dataset.i));
  document.addEventListener('input', (e) => { if (e.target.id !== 'say') return; mirror(); update(); syncTo(); });
  document.addEventListener('click', (e) => { if (st.open && !(e.target.closest && (e.target.closest('#mx-pop') || e.target.id === 'say'))) close(); });
  document.addEventListener('scroll', (e) => { if (e.target.id === 'say') mirror(); }, true);
  // keys go to the list while it's open; Backspace right after a mention takes the whole token
  document.addEventListener('keydown', (e) => { if (e.target.id !== 'say') return; const t = e.target;
    if (st.open) {
      if (e.key === 'ArrowDown' || e.key === 'ArrowUp') { e.preventDefault(); e.stopImmediatePropagation(); const n = st.items.length; if (n) { st.i = (st.i + (e.key === 'ArrowDown' ? 1 : n - 1)) % n; draw(); } return; }
      if ((e.key === 'Enter' || e.key === 'Tab') && st.items.length) { e.preventDefault(); e.stopImmediatePropagation(); pick(st.i); return; }
      if (e.key === 'Escape') { e.preventDefault(); e.stopImmediatePropagation(); close(); return; }
    }
    if (e.key === 'Backspace' && t.selectionStart === t.selectionEnd) { const before = t.value.slice(0, t.selectionStart), m = before.match(/@([A-Za-z][\w-]*)\s?$/);
      if (m && words().has(m[1].toLowerCase())) { e.preventDefault(); const at = before.length - m[0].length; t.value = t.value.slice(0, at) + t.value.slice(t.selectionStart); t.setSelectionRange(at, at); mirror(); syncTo(); close(); } }
  }, true);
  UI.afterRender.push(() => { st.open = false; mirror(); });
})();
/* the visible viewport's height (above a phone's keyboard and Safari's toolbar) for the full-height chat panel; when
   you start typing on a phone the panel comes fully into view, its composer above the keyboard */
(function () {
  const vv = window.visualViewport; if (!vv) return;
  const set = () => document.documentElement.style.setProperty('--vvh', Math.round(vv.height) + 'px');
  vv.addEventListener('resize', set); set();
  /* A chat panel that is a page's main part (a bot's Chat tab) fits what's left of the visible viewport below the
     page's head and tabs (65): only its thread scrolls, the composer is always on screen, and it refits on resize
     (a phone's keyboard, a rotated phone, a resized window). A fixed offset can't know how tall the head above is. */
  const fit = () => { const main = document.getElementById('main'); if (!main) return; const bar = document.getElementById('tabbar'), barTop = bar && bar.offsetParent ? bar.getBoundingClientRect().top : Infinity;
    main.querySelectorAll('.chat.fit, .cv-main.fit').forEach((c) => { const box = c.querySelector('#msgs'), atEnd = !box || !c.dataset.fitted || box.scrollHeight - box.scrollTop - box.clientHeight < 40; c.dataset.fitted = '1'; const bottom = Math.min(vv.height + vv.offsetTop, barTop, innerHeight) - (innerWidth <= 720 ? 12 : 24), top = c.getBoundingClientRect().top + main.scrollTop; // where it sits with the page at its top
      c.style.height = `${Math.max(innerWidth <= 720 ? 240 : 320, Math.floor(bottom - top))}px`;
      // what sits beside it (a bot's waiting approvals and open work) is no taller: it scrolls inside itself
      if (box && atEnd) box.scrollTop = box.scrollHeight; // the newest message stays in view, unless you'd scrolled up to read
      const list = c.parentElement && c.parentElement.querySelector(':scope > .cv-list'); if (list && innerWidth > 720) list.style.maxHeight = c.style.height; // the conversation list beside it is no taller
      const side = c.parentElement && c.parentElement.querySelector(':scope > .cm-side'); if (side) { side.classList.add('fit-side'); side.style.maxHeight = innerWidth > 720 ? c.style.height : ''; } }); };
  UI.fitChat = fit;
  vv.addEventListener('resize', fit); addEventListener('resize', fit);
  if (UI.afterRender) UI.afterRender.push(() => requestAnimationFrame(fit));
  document.addEventListener('focusin', (e) => { if (e.target.id !== 'say' || innerWidth > 720) return; const p = e.target.closest('.cv-main, .chat'); if (p) setTimeout(() => p.scrollIntoView({ block: 'end', behavior: 'instant' }), 60); });
})();
