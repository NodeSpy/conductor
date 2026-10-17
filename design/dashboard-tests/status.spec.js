// One status, one look. Every status a screen shows (running, passed, failed, needs you …) is drawn by the shared
// status mark (UI.statusMark / UI.mark, exactly the brand's status/marks.js vocabulary), so "Running" reads the same
// on the run page, in Activity, on Home and in the rail. Static: no screen hand-writes a status mark or dot, and no
// screen stylesheet picks a status hue. Rendered, on every route: each mark carries its glyph and its state, and each
// state has one colour across the whole app.
const fs = require('fs');
const path = require('path');
const { test, expect, go } = require('./fixtures');
const { ROUTES } = require('./routes');

const PROTO = path.join(__dirname, '..', 'dashboard-prototype');
const files = (dir, ext) => fs.readdirSync(dir, { withFileTypes: true }).flatMap((e) => (e.isDirectory() ? files(path.join(dir, e.name), ext) : ext.test(e.name) ? [path.join(dir, e.name)] : []));

test('statuses are built only by the shared status mark', () => {
  const bad = [];
  for (const f of files(path.join(PROTO, 'js'), /\.js$/)) {
    const rel = path.relative(PROTO, f), text = fs.readFileSync(f, 'utf8');
    if (rel !== path.join('js', 'ui.js')) {
      for (const m of text.matchAll(/class=\\?["'](st|dot|pill|badge|status)( [^"'\\]*)?\\?["']/g)) bad.push(`${rel}: hand-written ${m[0]}`);
    }
    for (const m of text.matchAll(/--color-status-running/g)) bad.push(`${rel}: ${m[0]}`);
  }
  // state colours live in the vocabulary (marks.css) and the tokens; screens' stylesheets never pick a status hue
  for (const f of files(path.join(PROTO, 'css'), /\.css$/)) {
    if (/tokens\.css$|marks\.css$/.test(f)) continue;
    for (const m of fs.readFileSync(f, 'utf8').matchAll(/--color-status-running/g)) bad.push(`${path.relative(PROTO, f)}: ${m[0]}`);
  }
  expect(bad).toEqual([]);
});

for (const theme of ['light', 'dark']) {
  test(`every rendered status mark is the shared one, and each state has one colour (${theme})`, async ({ page }) => {
    test.setTimeout(10 * 60 * 1000);
    const problems = [], colours = {};
    for (const r of ROUTES) {
      await go(page, r, { fresh: true });
      await page.evaluate((t) => { document.documentElement.dataset.theme = t; }, theme);
      const marks = await page.evaluate(() => [...document.querySelectorAll('.st')].filter((el) => el.getBoundingClientRect().width).map((el) => {
        const i = el.querySelector(':scope > i'), word = [...el.childNodes].filter((n) => n !== i).map((n) => n.textContent).join('').trim();
        const visibleWord = el.querySelector(':scope > .sr-only') ? '' : word;
        return { s: el.dataset.s, glyph: !!(i && i.querySelector('svg')), color: visibleWord ? getComputedStyle(el).color : '', icon: i ? getComputedStyle(i).color : '', word: visibleWord.slice(0, 20), cls: el.className };
      }));
      for (const m of marks) {
        if (!m.s) problems.push(`${r}: a status mark without a state (${m.cls})`);
        if (!m.glyph) problems.push(`${r}: a status mark without its glyph (${m.s} "${m.word}")`);
        // a toast's or a pill's mark may be tinted for its container; the plain mark must agree everywhere
        if (m.cls !== 'st' && m.cls !== 'st sm') continue;
        const k = m.s;
        colours[k] = colours[k] || { word: {}, icon: {} };
        if (m.color) (colours[k].word[m.color] = colours[k].word[m.color] || []).push(`${r} "${m.word}"`);
        (colours[k].icon[m.icon] = colours[k].icon[m.icon] || []).push(r);
      }
    }
    for (const [s, c] of Object.entries(colours)) {
      for (const part of ['word', 'icon']) {
        const seen = Object.keys(c[part]);
        if (seen.length > 1) problems.push(`state "${s}" ${part} has ${seen.length} colours: ${seen.map((x) => `${x} (${c[part][x].slice(0, 2).join(', ')})`).join(' · ')}`);
      }
    }
    expect(colours.run, 'running work appears somewhere').toBeTruthy();
    expect(problems).toEqual([]);
  });
}

test('running looks the same on the run page as in Activity: the vocabulary’s running mark and word', async ({ page }) => {
  const look = (sel) => page.evaluate((q) => { const st = document.querySelector(q); if (!st) return null; const probe = document.createElement('span'); probe.style.color = 'var(--color-status-running-fg)'; document.body.appendChild(probe); const fg = getComputedStyle(probe).color; probe.style.color = 'var(--color-status-running-solid)'; const solid = getComputedStyle(probe).color; probe.remove(); return { word: st.textContent.trim(), color: getComputedStyle(st).color, mark: getComputedStyle(st.querySelector('i')).color, fg, solid }; }, sel);
  await go(page, '#/runs/r-7f3a', { fresh: true });
  const a = await look('.dl-phead .st[data-s="run"]');
  expect(a.word).toMatch(/running/i);
  expect(a.color).toBe(a.fg); expect(a.mark).toBe(a.solid);
  await go(page, '#/activity');
  const b = await look('#act-tbl .g-live .st[data-s="run"]');
  expect([b.color, b.mark]).toEqual([a.color, a.mark]);
});
