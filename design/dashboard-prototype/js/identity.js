/* A bot's identity everywhere it appears: its face in its colour (option A: the picture, and a thin rule in its
 * colour on its chat messages; never a tint, never a status hue), its display name with its config id beside it.
 *   UI.botAv(name, 'xs'|'sm'|'md'|'lg'|'xl')   its mascot: shape, eyes, colour (initials when it has none)
 *   UI.botName(name)                            its display name
 *   UI.botWho(name)                             the name and, quieter, its id
 *   UI.botAttrs(name)                           data-bc="<colour>" for anything that takes its rule */
(function () {
  const esc = UI.esc, icon = UI.icon;
  // a bot's identity: its runtime name and tagline, and its picture from config (avatar: {shape, eyes, color}) when set,
  // staged changes included, so a picture you just picked shows everywhere at once
  UI.ident = (b) => { const base = (window.D.identity || {})[b] || null, bot = window.S && S.view && (S.view().bots || []).find((x) => x.name === b), av = bot && bot.cfg && bot.cfg.avatar;
    return av && (av.shape || av.image) ? Object.assign({}, base || {}, av.image ? { image: UI.avatarSrc(av.image), shape: null } : av) : base; };
  /* An uploaded picture is a file beside the config (avatar: { image: avatars/<name>.png }: conductor reads a PNG, JPEG
     or WebP from there, never an inline image). The prototype keeps the file's bytes in D.avatarFiles by its path. */
  UI.avatarSrc = (p) => ((window.D.avatarFiles || {})[p] || p);
  UI.avatarFile = (a, owner) => { if (!a || !a.image || !/^data:image\//.test(a.image)) return a; const ext = (a.image.match(/^data:image\/(png|jpeg|webp)/) || [, 'png'])[1].replace('jpeg', 'jpg'), path = `avatars/${owner}.${ext}`;
    (window.D.avatarFiles = window.D.avatarFiles || {})[path] = a.image; return Object.assign({}, a, { image: path }); };
  UI.botName = (b) => (UI.ident(b) || {}).name || b;
  UI.botColor = (b) => (UI.ident(b) || {}).color || 'stone';
  UI.botAttrs = (b) => `data-bc="${UI.botColor(b)}"`;
  /* A bot's mascot (66; the brand's kit, js/mascots.js): a body shape, an eye set and an identity colour, three
     choices. The kit draws with named paints (face, ink, rim) that the stylesheet maps to the bot's colour tokens, so
     every theme and every colour comes from tokens. A bot without a mascot shows its initials. */
  UI.mascotSVG = (shape, eyes) => (window.MASCOT && MASCOT.SHAPES.some((x) => x[0] === shape) && MASCOT.EYES.some((x) => x[0] === eyes) && MASCOT.allowed(shape, eyes) ? MASCOT.kitSVG(shape, eyes, { fill: 'face', ink: 'ink', rim: 'rim', title: ' ' }).replace(/ role="img" aria-label=" "/, ' aria-hidden="true"') : '');
  UI.botAv = (b, size = 'sm') => { const id = UI.ident(b); if (id && id.image) return `<span class="bav img ${size}" ${UI.botAttrs(b)} aria-hidden="true"><img src="${esc(id.image)}" alt=""></span>`;
    const svg = id ? UI.mascotSVG(id.shape, id.eyes) : '';
    const ini = esc(String(b).split(/[-_]/).map((w) => w[0]).join('').slice(0, 2));
    return `<span class="bav ${svg ? 'mascot ' : ''}${size}" ${UI.botAttrs(b)} aria-hidden="true">${svg || `<i>${ini}</i>`}</span>`; };
  /* The mascot picker (66): shape, then eyes, then colour, each drawn with your other two choices, and a live preview
     at the sizes it's seen (a page header, the chat list, a row). The single eye stays off round-topped shapes (the
     brand's rule), with the reason. */
  const COLORS = ['teal', 'lagoon', 'olive', 'lime', 'plum', 'rose', 'stone'];
  const pv = (m, size) => `<span class="bav mascot ${size}" data-bc="${m.color}" aria-hidden="true">${UI.mascotSVG(m.shape, m.eyes)}</span>`;
  let pk = null;
  const pickHTML = () => { const m = pk.m, S2 = MASCOT.SHAPES, E = MASCOT.EYES, okEyes = (sh) => (MASCOT.allowed(sh, m.eyes) ? m.eyes : 'dots');
    const opt = (act, v, on, svgM, label, off) => `<button type="button" class="mk-opt${on ? ' on' : ''}" data-act="${act}" data-v="${v}" aria-pressed="${on}" ${off ? UI.off(off) : ''} title="${esc(label)}">${pv(svgM, 'lg')}<span>${esc(label)}</span></button>`;
    return `<div class="mk-pick"><div class="mk-prev">${pv(m, 'xl')}<div class="mk-sizes">${pv(m, 'lg')}${pv(m, 'md')}${pv(m, 'sm')}</div><p class="dl-sub">${esc(S2.find((x) => x[0] === m.shape)[1])} · ${esc(E.find((x) => x[0] === m.eyes)[1].toLowerCase())} · ${esc(m.color)}</p></div>
      <div class="mk-steps"><h3>1 · Shape</h3><div class="mk-grid">${S2.map((x) => opt('mk-shape', x[0], x[0] === m.shape, { shape: x[0], eyes: okEyes(x[0]), color: m.color }, x[1])).join('')}</div>
      <h3>2 · Eyes</h3><div class="mk-grid">${E.map((x) => opt('mk-eyes', x[0], x[0] === m.eyes, { shape: m.shape, eyes: MASCOT.allowed(m.shape, x[0]) ? x[0] : 'dots', color: m.color }, x[1], MASCOT.allowed(m.shape, x[0]) ? '' : 'A single eye only goes on an angular shape: under a round top it reads as the logo')).join('')}</div>
      <h3>3 · Colour</h3><div class="mk-grid mk-cols">${COLORS.map((c) => opt('mk-color', c, c === m.color, { shape: m.shape, eyes: m.eyes, color: c }, c[0].toUpperCase() + c.slice(1))).join('')}</div></div></div>`; };
  /* Upload a picture (25b): PNG, JPEG or WebP up to 2 MB; a square crop you drag and zoom; the preview at the sizes it's
     seen. The crop is drawn to a 256px square (the API stores that: PUT /api/v1/bots/<bot>/avatar, multipart, returns
     {image}; config keeps avatar: {image: avatars/<bot>.webp}). Shared with a team's picture. */
  const TYPES = { 'image/png': 'PNG', 'image/jpeg': 'JPEG', 'image/webp': 'WebP' }, MAXB = 2 * 1024 * 1024, FR = 240;
  UI.cropHTML = (c) => `<div class="cr"><input type="file" id="cr-file" accept="image/png,image/jpeg,image/webp" hidden>
    ${c.src ? `<div class="cr-frame" id="cr-frame" aria-label="Drag to place the picture in the square"><img id="cr-img" src="${c.src}" alt="" draggable="false" style="width:${c.W}px;height:${c.H}px;transform:translate(${c.ox}px,${c.oy}px)"></div>
      <label class="cr-zoom">Zoom <input type="range" id="cr-z" min="1" max="3" step="0.01" value="${c.zoom}" data-on-input="cr-zoom"></label>
      <div class="cr-acts"><button type="button" class="btn sm secondary" data-act="cr-pick">Choose another…</button></div>`
    : `<button type="button" class="cr-drop" data-act="cr-pick">${UI.icon('upload')}<b>Choose a picture</b><span class="dl-sub">PNG, JPEG or WebP, up to 2 MB. You'll crop it to a square.</span></button>`}
    ${c.err ? `<p class="cr-err" role="alert">${esc(c.err)}</p>` : ''}</div>`;
  const cropOut = (c) => { const img = document.getElementById('cr-img'); if (!img || !img.complete) return c.out; const cv = document.createElement('canvas'); cv.width = cv.height = 256; const k = c.scale;
    cv.getContext('2d').drawImage(img, -c.ox / k, -c.oy / k, FR / k, FR / k, 0, 0, 256, 256); try { return (c.out = cv.toDataURL('image/webp', 0.9)); } catch (e) { return c.out; } };
  const fitC = (c) => { c.scale = Math.max(FR / c.w, FR / c.h) * c.zoom; c.W = c.w * c.scale; c.H = c.h * c.scale; c.ox = Math.min(0, Math.max(FR - c.W, c.ox)); c.oy = Math.min(0, Math.max(FR - c.H, c.oy)); };
  UI.cropLoad = (c, file, done) => { c.err = '';
    if (!TYPES[file.type]) { c.err = `That's ${file.type ? `a ${(file.type.split('/')[1] || '').toUpperCase()} file` : 'not a picture'}. Use a PNG, JPEG or WebP.`; return done(); }
    if (file.size > MAXB) { c.err = `That picture is ${(file.size / 1048576).toFixed(1)} MB; the limit is 2 MB.`; return done(); }
    const r = new FileReader(); r.onload = () => { const im = new Image(); im.onload = () => { Object.assign(c, { src: r.result, w: im.naturalWidth, h: im.naturalHeight, zoom: 1, ox: 0, oy: 0 }); fitC(c); c.ox = (FR - c.W) / 2; c.oy = (FR - c.H) / 2; done(); }; im.onerror = () => { c.err = 'That picture couldn’t be read.'; done(); }; im.src = r.result; }; r.readAsDataURL(file); };
  let crop = null, cropDone = null;
  const pvImg = (src, size) => `<span class="bav img ${size}" aria-hidden="true"><img src="${src}" alt=""></span>`;
  const redrawPick = () => { const b = document.getElementById('mk-body'); if (!b) return; if (pk.mode === 'upload') { b.innerHTML = uploadHTML(); requestAnimationFrame(previewCrop); } else b.innerHTML = pickHTML(); };
  const uploadHTML = () => `<div class="mk-pick"><div class="mk-prev" id="cr-prev">${crop.src ? `${pvImg(crop.out || crop.src, 'xl')}<div class="mk-sizes">${pvImg(crop.out || crop.src, 'lg')}${pvImg(crop.out || crop.src, 'md')}${pvImg(crop.out || crop.src, 'sm')}</div><p class="dl-sub">Your picture, as it will show</p>` : '<p class="dl-sub">Its preview shows here.</p>'}</div><div class="mk-steps">${UI.cropHTML(crop)}</div></div>`;
  const previewCrop = () => { if (!crop || !crop.src) return; const out = cropOut(crop); document.querySelectorAll('#cr-prev img').forEach((x) => { x.src = out; }); };
  UI.cropState = () => crop;
  UI.act('cr-pick', () => { const f = document.getElementById('cr-file'); if (f) f.click(); });
  document.addEventListener('change', (e) => { if (e.target.id !== 'cr-file' || !e.target.files[0]) return; UI.cropLoad(crop, e.target.files[0], () => (cropDone || redrawPick)()); });
  UI.act('cr-zoom', (el) => { const cx = (FR / 2 - crop.ox) / crop.scale, cy = (FR / 2 - crop.oy) / crop.scale; crop.zoom = +el.value; fitC(crop); crop.ox = FR / 2 - cx * crop.scale; crop.oy = FR / 2 - cy * crop.scale; fitC(crop); const img = document.getElementById('cr-img'); if (img) { img.style.width = crop.W + 'px'; img.style.height = crop.H + 'px'; img.style.transform = `translate(${crop.ox}px,${crop.oy}px)`; } previewCrop(); });
  document.addEventListener('pointerdown', (e) => { const fr = e.target.closest && e.target.closest('#cr-frame'); if (!fr || !crop) return; e.preventDefault(); const sx = e.clientX, sy = e.clientY, ox = crop.ox, oy = crop.oy, img = document.getElementById('cr-img');
    const mv = (ev) => { crop.ox = ox + ev.clientX - sx; crop.oy = oy + ev.clientY - sy; fitC(crop); img.style.transform = `translate(${crop.ox}px,${crop.oy}px)`; };
    const up = () => { removeEventListener('pointermove', mv); removeEventListener('pointerup', up); previewCrop(); }; addEventListener('pointermove', mv); addEventListener('pointerup', up); });
  UI.act('mk-mode', (b) => { pk.mode = b.dataset.v; b.parentElement.querySelectorAll('button').forEach((x) => { x.classList.toggle('on', x === b); x.setAttribute('aria-pressed', String(x === b)); }); if (pk.mode === 'upload' && !crop) crop = { src: '', err: '' }; redrawPick(); });
  UI.mascotPicker = (cur, onPick, title = 'Its mascot') => { const c0 = cur || {}; pk = { m: { shape: c0.shape || 'hex', eyes: c0.eyes || 'dots', color: c0.color || 'teal' }, onPick }; // only the three choices
    pk.mode = c0.image ? 'upload' : 'mascot'; crop = { src: c0.image || '', err: '', w: 256, h: 256, zoom: 1, ox: 0, oy: 0 }; if (c0.image) { crop.scale = FR / 256; crop.W = crop.H = FR; crop.out = c0.image; } cropDone = null;
    UI.modal(`<div class="modal-head"><div><div class="caps">Picture</div><h2>${esc(title)}</h2></div><button class="icon-btn" data-close aria-label="Close">${icon('x')}</button></div>
      <div class="seg mk-mode" role="group" aria-label="Kind of picture"><button type="button" class="${pk.mode === 'mascot' ? 'on' : ''}" data-act="mk-mode" data-v="mascot" aria-pressed="${pk.mode === 'mascot'}">Mascot</button><button type="button" class="${pk.mode === 'upload' ? 'on' : ''}" data-act="mk-mode" data-v="upload" aria-pressed="${pk.mode === 'upload'}">Upload a picture</button></div>
      <div class="modal-body" id="mk-body">${pk.mode === 'upload' ? uploadHTML() : pickHTML()}</div><div class="modal-foot">${c0.image ? '<button class="btn ghost" data-act="mk-remove">Remove the picture</button><span class="grow"></span>' : ''}<button class="btn ghost" data-close>Cancel</button><button class="btn primary" data-act="mk-use">Use this picture</button></div>`, { wide: true });
    if (pk.mode === 'upload') requestAnimationFrame(previewCrop); };
  UI.act('mk-remove', () => { const f = pk.onPick; UI.closeModal(); if (f) f({ shape: pk.m.shape, eyes: pk.m.eyes, color: pk.m.color }); });
  UI.act('mk-shape', (b) => { pk.m.shape = b.dataset.v; if (!MASCOT.allowed(pk.m.shape, pk.m.eyes)) pk.m.eyes = 'dots'; redrawPick(); });
  UI.act('mk-eyes', (b) => { pk.m.eyes = b.dataset.v; redrawPick(); });
  UI.act('mk-color', (b) => { pk.m.color = b.dataset.v; redrawPick(); });
  UI.act('mk-use', () => { const f = pk.onPick;
    if (pk.mode === 'upload') { if (!crop.src) { crop.err = 'Choose a picture first, or switch back to a mascot.'; return redrawPick(); } const out = cropOut(crop) || crop.src; UI.closeModal(); if (f) f({ image: out }); return; }
    const m = Object.assign({}, pk.m); UI.closeModal(); if (f) f(m); });
  /* A team's picture picker (34): its initials, an icon in one of the bot colours, or an uploaded picture; never a mascot
     (mascots are bots). The icons leave out those that already mean something (nav, objects, status, the passkey). */
  // conductor's avatar icons (RS.lists.icons), each drawn as its av-<name> glyph (kindglyphs.js)
  const TEAM_ICONS = ((window.RS && RS.lists && RS.lists.icons) || []).filter((n) => (window.ICONS || {})['av-' + n]);
  let tp = null;
  const tpv = (t, a, size) => UI.teamAvOf(t, a, size);
  const teamHTML = () => { const a = tp.a, t = tp.t;
    const body = tp.mode === 'icon' ? `<h3>Icon</h3><div class="mk-grid">${TEAM_ICONS.map((ic) => `<button type="button" class="mk-opt${a.icon === ic ? ' on' : ''}" data-act="tp-icon" data-v="${ic}" aria-pressed="${a.icon === ic}">${tpv(t, { icon: ic, color: a.color || 'teal' }, 'lg')}<span>${esc(ic.replace(/-/g, ' '))}</span></button>`).join('')}</div>
        <h3>Colour</h3><div class="mk-grid mk-cols">${COLORS.map((c) => `<button type="button" class="mk-opt${a.color === c ? ' on' : ''}" data-act="tp-color" data-v="${c}" aria-pressed="${a.color === c}">${tpv(t, { icon: a.icon || TEAM_ICONS[0], color: c }, 'lg')}<span>${c[0].toUpperCase() + c.slice(1)}</span></button>`).join('')}</div>`
      : tp.mode === 'upload' ? UI.cropHTML(crop) : `<p class="dl-sub">Its first two letters on a tile: <b>${esc(String(t).replace(/[^a-z0-9]/gi, '').slice(0, 2).toLowerCase())}</b>. The default.</p>`;
    const prevA = tp.mode === 'upload' ? (crop.src ? { image: crop.out || crop.src } : {}) : tp.mode === 'icon' ? { icon: a.icon || TEAM_ICONS[0], color: a.color || 'teal' } : {};
    return `<div class="mk-pick"><div class="mk-prev" id="cr-prev">${tpv(t, prevA, 'xl')}<div class="mk-sizes">${tpv(t, prevA, 'lg')}${tpv(t, prevA, 'md')}${tpv(t, prevA, 'sm')}</div><p class="dl-sub">team ${esc(t)}, as it will show</p></div><div class="mk-steps">${body}</div></div>`; };
  const redrawTeam = () => { const b = document.getElementById('mk-body'); if (b) b.innerHTML = teamHTML(); if (tp.mode === 'upload') requestAnimationFrame(previewCrop); };
  UI.teamPicker = (t, onPick) => { const cur = UI.teamIdent(t); tp = { t, onPick, mode: cur.image ? 'upload' : cur.icon ? 'icon' : 'initials', a: { icon: cur.icon || TEAM_ICONS[0], color: cur.color || 'teal' } };
    crop = { src: cur.image || '', err: '', w: 256, h: 256, zoom: 1, ox: 0, oy: 0, scale: FR / 256, W: FR, H: FR, out: cur.image || '' }; cropDone = redrawTeam;
    const seg = [['initials', 'Initials'], ['icon', 'Icon and colour'], ['upload', 'Upload a picture']].map(([v, l]) => `<button type="button" class="${tp.mode === v ? 'on' : ''}" data-act="tp-mode" data-v="${v}" aria-pressed="${tp.mode === v}">${l}</button>`).join('');
    UI.modal(`<div class="modal-head"><div><div class="caps">Picture</div><h2>Team ${esc(t)}'s picture</h2></div><button class="icon-btn" data-close aria-label="Close">${icon('x')}</button></div>
      <div class="seg mk-mode" role="group" aria-label="Kind of picture">${seg}</div><div class="modal-body" id="mk-body">${teamHTML()}</div>
      <div class="modal-foot"><button class="btn ghost" data-close>Cancel</button><button class="btn primary" data-act="tp-use">Use this picture</button></div>`, { wide: true }); };
  UI.act('tp-mode', (b) => { tp.mode = b.dataset.v; b.parentElement.querySelectorAll('button').forEach((x) => { x.classList.toggle('on', x === b); x.setAttribute('aria-pressed', String(x === b)); }); redrawTeam(); });
  UI.act('tp-icon', (b) => { tp.a.icon = b.dataset.v; redrawTeam(); });
  UI.act('tp-color', (b) => { tp.a.color = b.dataset.v; redrawTeam(); });
  UI.act('tp-use', () => { const f = tp.onPick; let a;
    if (tp.mode === 'upload') { if (!crop.src) { crop.err = 'Choose a picture first, or pick initials or an icon.'; return redrawTeam(); } a = { image: cropOut(crop) || crop.src }; }
    else a = tp.mode === 'icon' ? { icon: tp.a.icon, color: tp.a.color } : null; // initials: no avatar (conductor has no initials key)
    UI.closeModal(); if (f) f(a); });
  /** A bot's picture field: its mascot now, and Change… (opens the picker). */
  UI.mascotField = (m, act, attrs = '') => `<div class="mk-field">${m.image ? `${pvImg(m.image, 'lg')}<span class="dl-sub">Your picture</span>` : `${pv(m, 'lg')}<span class="dl-sub">${esc(MASCOT.SHAPES.find((x) => x[0] === m.shape)[1])} · ${esc(MASCOT.EYES.find((x) => x[0] === m.eyes)[1].toLowerCase())} · ${esc(m.color)}</span>`}<button type="button" class="btn sm secondary" data-act="${act}" ${attrs}>Change…</button></div>`;
  /** Is the bot executing right now (a worker of its running)? The spinner is for that alone (59); a live session that's
   *  idle is not "running". */
  UI.botWorking = (b) => (window.D.workers || []).some((w) => (w.bot || 'platform-sre') === b && w.status === 'run');
  UI.botWho = (b) => { const n = UI.botName(b); return `<b class="bav-name">${esc(n)}</b>${n !== b ? ` <span class="bav-id">${esc(b)}</span>` : ''}`; };
  /** A team's picture: its own icon in its colour, its uploaded image, or its initials on a tile (never a bot's face).
   *  From config (teams.<t>.avatar), staged changes included, else the demo's identity. */
  UI.teamIdent = (t) => { const tm = window.S && S.view && (S.view().teams || []).find((x) => x.name === t), av = tm && tm.cfg && tm.cfg.avatar;
    if (tm && tm.cfg) return av ? Object.assign({}, av, av.image ? { image: UI.avatarSrc(av.image) } : {}) : {}; return (window.D.teamIdentity || {})[t] || {}; };
  UI.teamAv = (t, size = 'sm') => UI.teamAvOf(t, UI.teamIdent(t), size);
  UI.teamAvOf = (t, id, size = 'sm') => { id = id || {};
    if (id.image) return `<span class="bav tav img ${size}" aria-hidden="true"><img src="${esc(id.image)}" alt=""></span>`;
    if (id.icon) return `<span class="bav tav ${size}" data-bc="${esc(id.color || 'stone')}" aria-hidden="true">${UI.icon((window.ICONS || {})['av-' + id.icon] ? 'av-' + id.icon : id.icon)}</span>`;
    return `<span class="bav tav ini ${size}" aria-hidden="true"><i>${esc(String(t).replace(/[^a-z0-9]/gi, '').slice(0, 2).toLowerCase())}</i></span>`; };
})();
