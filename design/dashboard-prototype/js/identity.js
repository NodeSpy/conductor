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
    return av && av.shape ? Object.assign({}, base || {}, av) : base; };
  UI.botName = (b) => (UI.ident(b) || {}).name || b;
  UI.botColor = (b) => (UI.ident(b) || {}).color || 'stone';
  UI.botAttrs = (b) => `data-bc="${UI.botColor(b)}"`;
  /* A bot's mascot (66; the brand's kit, js/mascots.js): a body shape, an eye set and an identity colour, three
     choices. The kit draws with named paints (face, ink, rim) that the stylesheet maps to the bot's colour tokens, so
     every theme and every colour comes from tokens. A bot without a mascot shows its initials. */
  UI.mascotSVG = (shape, eyes) => (window.MASCOT && MASCOT.SHAPES.some((x) => x[0] === shape) && MASCOT.EYES.some((x) => x[0] === eyes) && MASCOT.allowed(shape, eyes) ? MASCOT.kitSVG(shape, eyes, { fill: 'face', ink: 'ink', rim: 'rim', title: ' ' }).replace(/ role="img" aria-label=" "/, ' aria-hidden="true"') : '');
  UI.botAv = (b, size = 'sm') => { const id = UI.ident(b), svg = id ? UI.mascotSVG(id.shape, id.eyes) : '';
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
    return `<div class="mk-pick"><div class="mk-prev">${pv(m, 'xl')}<div class="mk-sizes">${pv(m, 'lg')}${pv(m, 'list40')}${pv(m, 'sm')}</div><p class="dl-sub">${esc(S2.find((x) => x[0] === m.shape)[1])} · ${esc(E.find((x) => x[0] === m.eyes)[1].toLowerCase())} · ${esc(m.color)}</p></div>
      <div class="mk-steps"><h3>1 · Shape</h3><div class="mk-grid">${S2.map((x) => opt('mk-shape', x[0], x[0] === m.shape, { shape: x[0], eyes: okEyes(x[0]), color: m.color }, x[1])).join('')}</div>
      <h3>2 · Eyes</h3><div class="mk-grid">${E.map((x) => opt('mk-eyes', x[0], x[0] === m.eyes, { shape: m.shape, eyes: MASCOT.allowed(m.shape, x[0]) ? x[0] : 'dots', color: m.color }, x[1], MASCOT.allowed(m.shape, x[0]) ? '' : 'A single eye only goes on an angular shape: under a round top it reads as the logo')).join('')}</div>
      <h3>3 · Colour</h3><div class="mk-grid mk-cols">${COLORS.map((c) => opt('mk-color', c, c === m.color, { shape: m.shape, eyes: m.eyes, color: c }, c[0].toUpperCase() + c.slice(1))).join('')}</div></div></div>`; };
  const redrawPick = () => { const b = document.getElementById('mk-body'); if (b) b.innerHTML = pickHTML(); };
  UI.mascotPicker = (cur, onPick, title = 'Its mascot') => { const c0 = cur || {}; pk = { m: { shape: c0.shape || 'hex', eyes: c0.eyes || 'dots', color: c0.color || 'teal' }, onPick }; // only the three choices
    UI.modal(`<div class="modal-head"><div><div class="caps">Picture</div><h2>${esc(title)}</h2></div><button class="icon-btn" data-close aria-label="Close">${icon('x')}</button></div>
      <div class="modal-body" id="mk-body">${pickHTML()}</div><div class="modal-foot"><button class="btn ghost" data-close>Cancel</button><button class="btn primary" data-act="mk-use">Use this mascot</button></div>`, { wide: true }); };
  UI.act('mk-shape', (b) => { pk.m.shape = b.dataset.v; if (!MASCOT.allowed(pk.m.shape, pk.m.eyes)) pk.m.eyes = 'dots'; redrawPick(); });
  UI.act('mk-eyes', (b) => { pk.m.eyes = b.dataset.v; redrawPick(); });
  UI.act('mk-color', (b) => { pk.m.color = b.dataset.v; redrawPick(); });
  UI.act('mk-use', () => { const m = Object.assign({}, pk.m), f = pk.onPick; UI.closeModal(); if (f) f(m); });
  /** A bot's picture field: its mascot now, and Change… (opens the picker). */
  UI.mascotField = (m, act, attrs = '') => `<div class="mk-field">${pv(m, 'lg')}<span class="dl-sub">${esc(MASCOT.SHAPES.find((x) => x[0] === m.shape)[1])} · ${esc(MASCOT.EYES.find((x) => x[0] === m.eyes)[1].toLowerCase())} · ${esc(m.color)}</span><button type="button" class="btn sm secondary" data-act="${act}" ${attrs}>Change…</button></div>`;
  UI.botWho = (b) => { const n = UI.botName(b); return `<b class="bav-name">${esc(n)}</b>${n !== b ? ` <span class="bav-id">${esc(b)}</span>` : ''}`; };
  /** A team's picture: its own icon in its colour, its uploaded image, or its initials on a tile (never a bot's face). */
  UI.teamAv = (t, size = 'sm') => { const id = (window.D.teamIdentity || {})[t] || {};
    if (id.image) return `<span class="bav tav ${size}" aria-hidden="true"><img src="${esc(id.image)}" alt=""></span>`;
    if (id.icon) return `<span class="bav tav ${size}" data-bc="${esc(id.color || 'stone')}" aria-hidden="true">${UI.icon(id.icon)}</span>`;
    return `<span class="bav tav ini ${size}" aria-hidden="true"><i>${esc(String(t).replace(/[^a-z0-9]/gi, '').slice(0, 2).toLowerCase())}</i></span>`; };
})();
