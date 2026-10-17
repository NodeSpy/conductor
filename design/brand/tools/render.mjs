// Renders the PNGs (favicons, app icons, social cards) from the SVGs build.mjs wrote.
// Needs Playwright with a Chromium; point PLAYWRIGHT_PATH at an install if it isn't resolvable here.
import fs from 'node:fs';
import path from 'node:path';
import { createRequire } from 'node:module';
import { fileURLToPath } from 'node:url';
import { DIRS, RECOMMENDED } from './directions.mjs';
import { LOGOS, kitDir, OFFICIAL_LOGO } from './logos.mjs';

const require = createRequire(import.meta.url);
const { chromium } = require(process.env.PLAYWRIGHT_PATH || 'playwright');
const here = path.dirname(fileURLToPath(import.meta.url));
const root = path.resolve(here, '..');
const only = process.argv[2];

const dataURI = (file, type) => `data:${type};base64,${fs.readFileSync(file).toString('base64')}`;
const fontCSS = () =>
  [
    ['Instrument Serif', 'italic', 400, 'InstrumentSerif-italic'],
    ['Instrument Serif', 'normal', 400, 'InstrumentSerif-normal'],
    ['Instrument Sans', 'normal', '400 700', 'InstrumentSans-normal'],
    ['Martian Mono', 'normal', '300 600', 'MartianMono-normal'],
  ]
    .map(([fam, style, w, f]) => `@font-face{font-family:'${fam}';font-style:${style};font-weight:${w};src:url(${dataURI(path.join(root, 'fonts', f + '.woff2'), 'font/woff2')})}`)
    .join('');

const svgFile = (id, name) => path.join(root, 'directions', id, name + '.svg');
const inline = (id, name) => fs.readFileSync(svgFile(id, name), 'utf8');

function socialHTML(dir) {
  const P = dir.pal.dark;
  // The mark, huge, low-contrast and cropped off the top-right corner.
  const [gs, gr, gt] = { fermata: [620, -170, -230], downbeat: [640, -300, -250] }[dir.id] || [760, -300, -230];
  const ghost = inline(dir.id, 'logo-mark-mono').replace(
    '<svg ',
    `<svg class="ghost" style="color:${P.line};width:${gs}px;height:${gs}px;right:${gr}px;top:${gt}px" `,
  );
  return `<!doctype html><meta charset="utf-8"><style>${fontCSS()}
  *{box-sizing:border-box}html,body{margin:0;width:1280px;height:640px;overflow:hidden}
  body{background:${P.bg};color:${P.ink};font-family:'Instrument Sans';position:relative;
    background-image:radial-gradient(${P.ink}10 1px,transparent 1.3px);background-size:24px 24px;background-position:12px 12px}
  .ghost{position:absolute;opacity:.85}
  .lock{position:absolute;left:96px;top:196px;height:112px}
  .lock svg{height:100%;width:auto;display:block}
  .tag{position:absolute;left:100px;top:350px;width:760px;font-size:31px;line-height:1.28;color:${P.ink2};letter-spacing:-.005em}
  .foot{position:absolute;left:100px;right:96px;bottom:64px;display:flex;justify-content:space-between;font-family:'Martian Mono';font-size:17px;color:${P.ink3};letter-spacing:-.01em}
  .foot b{color:${P.acc};font-weight:400}
  .rule{position:absolute;left:100px;right:96px;bottom:112px;border-top:1px solid ${P.line}}
  </style><body>${ghost}<div class="lock">${inline(dir.id, 'logo-horizontal-dark')}</div>
  <div class="tag">Event-driven orchestration for AI agents. Self-hosted, config-as-code, behind your approvals.</div>
  <div class="rule"></div><div class="foot"><span>triggers <b>→</b> workflows <b>·</b> bots <b>·</b> sandbox <b>·</b> packs</span><span>github.com/NodeSpy/conductor</span></div>`;
}


// The Daylight social card for the official logo: cream, the lockup, one plain sentence,
// and the sunrise arch rising from the bottom-right edge as the brand's illustration motif.
function socialDaylight(dir, kit) {
  const P = dir.pal.light;
  const font = (fam, f, w) => `@font-face{font-family:'${fam}';font-weight:${w};src:url(${dataURI(path.join(root, 'fonts', f + '.woff2'), 'font/woff2')})}`;
  const lock = fs.readFileSync(path.join(kit, 'logo-horizontal.svg'), 'utf8');
  const [, , bw, bh] = dir.mark.box;
  return `<!doctype html><meta charset="utf-8"><style>${font('Atkinson Hyperlegible Next', 'AtkinsonNext', '200 800')}${font('Fraunces', 'Fraunces', '100 900')}
  *{box-sizing:border-box}html,body{margin:0;width:1280px;height:640px;overflow:hidden;color-scheme:only light}
  body{background:${P.bg};color:${P.ink};font-family:'Atkinson Hyperlegible Next';position:relative}
  .sun{position:absolute;right:-60px;bottom:-150px;width:600px;height:${Math.round((600 * bh) / bw)}px}
  .lock{position:absolute;left:96px;top:176px;height:108px}
  .lock svg{height:100%;width:auto;display:block}
  .tag{position:absolute;left:100px;top:326px;width:660px;font-size:34px;line-height:1.3;color:${P.ink2}}
  .tag b{color:${P.ink};font-weight:700}
  .foot{position:absolute;left:100px;bottom:64px;font-size:20px;color:${P.ink3}}
  </style><body>
  <svg class="sun" viewBox="${dir.mark.box.join(' ')}" aria-hidden="true"><g fill="${P.line}">${dir.mark.ink}</g><g fill="${P.acc}" opacity=".35">${dir.mark.acc}</g></svg>
  <div class="lock">${lock}</div>
  <div class="tag"><b>Event-driven orchestration for AI agents.</b> Self-hosted, config-as-code, and anything risky waits for your approval.</div>
  <div class="foot">github.com/NodeSpy/conductor</div>`;
}

const imgHTML = (file, w, h) =>
  `<!doctype html><style>html,body{margin:0;background:transparent}img{display:block}</style><img src="${dataURI(file, 'image/svg+xml')}" width="${w}" height="${h}">`;

const b = await chromium.launch();
async function shoot(html, w, h, out, transparent = true) {
  const p = await b.newPage({ viewport: { width: w, height: h } });
  await p.setContent(html, { waitUntil: 'load' });
  await p.evaluate(() => document.fonts.ready);
  await p.screenshot({ path: out, omitBackground: transparent });
  await p.close();
}

for (const dir of DIRS) {
  if (only && dir.id !== only) continue;
  if (only === 'kits') continue;
  const d = path.join(root, 'directions', dir.id);
  for (const s of [16, 32, 48]) await shoot(imgHTML(svgFile(dir.id, 'favicon16-light'), s, s), s, s, path.join(d, `favicon-${s}.png`));
  await shoot(imgHTML(svgFile(dir.id, 'app-icon-dark'), 512, 512), 512, 512, path.join(d, 'app-icon-512.png'));
  await shoot(socialHTML(dir), 1280, 640, path.join(d, 'social-card.png'), false);
  console.log(dir.id, 'ok');
}

// PNGs for every logo kit.
for (const dir of LOGOS) {
  if (only && only !== 'kits' && only !== dir.id) continue;
  const kit = path.join(root, kitDir(dir.id)), f = (n) => path.join(kit, n + '.svg');
  for (const s of [16, 32, 48]) {
    await shoot(imgHTML(f('favicon16-light'), s, s), s, s, path.join(kit, `favicon-${s}.png`));
    await shoot(imgHTML(f('favicon16-dark'), s, s), s, s, path.join(kit, `favicon-${s}-dark.png`));
  }
  await shoot(imgHTML(f('apple-touch-icon'), 180, 180), 180, 180, path.join(kit, 'apple-touch-icon.png'), false);
  for (const s of [192, 512]) await shoot(imgHTML(f('app-icon'), s, s), s, s, path.join(kit, `icon-${s}.png`));
  if (dir.id === OFFICIAL_LOGO) await shoot(socialDaylight(dir, kit), 1280, 640, path.join(kit, 'social-card.png'), false);
  console.log('kit', dir.id, 'ok');
}
await b.close();
