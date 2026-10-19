/* A bot's identity everywhere it appears: its face in its colour (option A: the picture, and a thin rule in its
 * colour on its chat messages; never a tint, never a status hue), its display name with its config id beside it.
 *   UI.botAv(name, 'xs'|'sm'|'md'|'lg'|'xl')   its mascot: shape, eyes, colour (initials when it has none)
 *   UI.botName(name)                            its display name
 *   UI.botWho(name)                             the name and, quieter, its id
 *   UI.botAttrs(name)                           data-bc="<colour>" for anything that takes its rule */
(function () {
  const esc = UI.esc;
  UI.ident = (b) => (window.D.identity || {})[b] || null;
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
  UI.botWho = (b) => { const n = UI.botName(b); return `<b class="bav-name">${esc(n)}</b>${n !== b ? ` <span class="bav-id">${esc(b)}</span>` : ''}`; };
  /** A team's picture: its own icon in its colour, its uploaded image, or its initials on a tile (never a bot's face). */
  UI.teamAv = (t, size = 'sm') => { const id = (window.D.teamIdentity || {})[t] || {};
    if (id.image) return `<span class="bav tav ${size}" aria-hidden="true"><img src="${esc(id.image)}" alt=""></span>`;
    if (id.icon) return `<span class="bav tav ${size}" data-bc="${esc(id.color || 'stone')}" aria-hidden="true">${UI.icon(id.icon)}</span>`;
    return `<span class="bav tav ini ${size}" aria-hidden="true"><i>${esc(String(t).replace(/[^a-z0-9]/gi, '').slice(0, 2).toLowerCase())}</i></span>`; };
})();
