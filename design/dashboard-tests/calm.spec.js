// Calm by default (from the first review: "all of it just looks so busy … I have to read everything all the time"). On the mostly
// healthy demo day (js/scenario.js: dozens of runs, one approval and one failure need you), each key screen must
// answer "is anything wrong / what needs me?" at a glance:
//   - the answer sentence is first in reading order after the title, in primary ink, with a bold lead-in; on the
//     Overview it is also the largest text in the content;
//   - loudness: in the first viewport of the content, few things carry colour (a budget that grows with the number of
//     exceptions on the day; a row counts once, however many marks identify it: kind, risk, environment, status),
//     and no row is a wall of text (facts sit in columns; no single block of prose runs past ~140 characters);
//   - on the all-quiet day the Overview says so in one line and draws no exception colour at all.
const { test, expect, go } = require('./fixtures');

// route → the one sentence a person should get at a glance, and where it lives
const ANSWERS = [
  ['#/overview', '.answer', /2 things need you/],
  ['#/attention', '.page-head .lede', /2 things need you/],
  ['#/runs', '.page-head .lede', /1 run needs attention/],
  ['#/live', '.page-head .lede', /3 runs are going now/],
  ['#/bots', '.page-head .lede', /1 approval waits for you/],
  ['#/workflows', '.page-head .lede', /workflow/],
  ['#/extensions/packs', '.page-head .lede', /Everything installed is working/],
  ['#/connectors/connectors', '.page-head .lede', /connectors are healthy/],
  ['#/bots/platform-sre/cases', '.bot-hero .lede', /approval waits for you/],
  ['#/bots/platform-sre/proposals/p12', '.prop-title', /Replace the production web server/],
];

const day = async (page, demo) => { await page.evaluate((d) => { localStorage.setItem('cd-demo', d); localStorage.removeItem('cd-proto-state-v5'); }, demo); };

/** In-page: loud units (coloured text, icons or fills) and text-heavy rows in the content's first viewport. */
const loudness = () => {
  const main = document.getElementById('main'), top = main.getBoundingClientRect().top, bottom = innerHeight;
  const rgb = (c) => { const m = String(c).match(/rgba?\(([^)]+)\)/); if (!m) return null; const [r, g, b, a = 1] = m[1].split(/[ ,/]+/).filter(Boolean).map(Number); return a < 0.3 ? null : [r, g, b]; };
  const sat = (c) => { const v = rgb(c); if (!v) return 0; const mx = Math.max(...v) / 255, mn = Math.min(...v) / 255, l = (mx + mn) / 2; return mx === mn ? 0 : (mx - mn) / (1 - Math.abs(2 * l - 1)); };
  const UNIT = '.item, tr, .mark, .lane, .answer, .grp-head, .btn, .callout, .prop-head, .stat, .quiet-row > summary, .tag, .st, .page-head .lede, .bot-hero .lede, .dg-row';
  const units = new Set(), heavy = [];
  main.querySelectorAll('*').forEach((el) => {
    const r = el.getBoundingClientRect(); if (!r.width || !r.height || r.bottom < top || r.top > bottom) return;
    const cs = getComputedStyle(el); if (cs.visibility === 'hidden' || +cs.opacity < 0.1) return;
    const loud = sat(cs.color) > 0.45 && el.textContent.trim() || sat(cs.backgroundColor) > 0.45 || (el.tagName === 'svg' && (sat(cs.color) > 0.45)) || parseFloat(cs.boxShadow.match(/inset (\d+)px/) ? 1 : 0) && sat(cs.boxShadow) > 0.45;
    if (!loud) return; const u = el.closest('.xrow, .item, tbody tr, .dg-row, .lane, .grp-head') || el.closest(UNIT) || el; units.add(u);
  });
  main.querySelectorAll('.item, .xrow, tbody tr').forEach((row) => { const r = row.getBoundingClientRect(); if (!r.height || r.bottom < top || r.top > bottom) return;
    row.querySelectorAll('*').forEach((el) => { const own = [...el.childNodes].filter((n) => n.nodeType === 3).map((n) => n.textContent).join('').replace(/\s+/g, ' ').trim(); const t = own.length > 40 ? el.innerText.replace(/\s+/g, ' ').trim() : ''; if (t.length > 140) heavy.push(t.slice(0, 60)); }); });
  return { loud: [...units].map((u) => `${u.tagName.toLowerCase()}.${String(u.getAttribute('class') || '').split(' ')[0]} "${(u.textContent || '').trim().slice(0, 30)}"`), heavy: [...new Set(heavy)], exceptions: (window.D.attention || []).length };
};

for (const [route, sel, rx] of ANSWERS) {
  test(`calm day, ${route}: the answer comes first and the screen stays quiet`, async ({ page }) => {
    await day(page, 'calm'); await go(page, route);
    const ans = page.locator(sel).first();
    await expect(ans).toBeVisible();
    await expect(ans).toContainText(rx);
    // first in reading order after the title, in primary ink with a bold lead-in (or itself bold, as a title)
    const a = await ans.evaluate((el) => { const main = document.getElementById('main'), all = [...main.querySelectorAll('h1, h2, p, .item-t, td, .lede, .answer, .prop-title')].filter((x) => x.getBoundingClientRect().height && x.innerText.trim()); const i = all.indexOf(el.closest('h1, h2, p, .item-t, td, .lede, .answer, .prop-title') || el); const b = el.querySelector('b') || el; return { i, weight: +getComputedStyle(b).fontWeight, color: getComputedStyle(b).color, ink: getComputedStyle(document.documentElement).getPropertyValue('--color-text-primary').trim() }; });
    expect(a.i, 'the answer is near the top of the reading order').toBeLessThanOrEqual(2);
    expect(a.weight, 'the answer leads in bold').toBeGreaterThanOrEqual(600);
    const L = await page.evaluate(loudness);
    const budget = Math.max(4, 4 * Math.max(1, L.exceptions));
    expect(L.loud.length, `coloured things in the first viewport of ${route}: ${L.loud.join(' | ')}`).toBeLessThanOrEqual(budget);
    expect(L.heavy, `text-heavy rows on ${route}`).toEqual([]);
  });
}

test('calm day, Overview: the answer is the largest text in the content', async ({ page }) => {
  await day(page, 'calm'); await go(page, '#/overview');
  const r = await page.evaluate(() => { const main = document.getElementById('main'); let max = 0, who = ''; main.querySelectorAll('*').forEach((el) => { if (!el.childNodes.length || ![...el.childNodes].some((n) => n.nodeType === 3 && n.textContent.trim())) return; const b = el.getBoundingClientRect(); if (!b.height || b.top > innerHeight) return; const px = parseFloat(getComputedStyle(el).fontSize); if (px > max) { max = px; who = el.closest('.answer') ? 'answer' : `${el.tagName}.${el.className}`; } }); return { max, who }; });
  expect(r.who).toBe('answer');
});

test('all-quiet day, Overview: one line says nothing needs you, and nothing is coloured as an exception', async ({ page }) => {
  await day(page, 'quiet'); await go(page, '#/overview');
  await expect(page.locator('.answer')).toHaveText(/All quiet\. Nothing needs you\./);
  await expect(page.locator('.panel.attn')).toHaveCount(0);
  await expect(page.locator('#strip-cells .cell.alert')).toHaveCount(0);
  await expect(page.locator('[data-row="fail"], [data-row="ember"], [data-row="wait"], .mark[data-m="fail"], .mark[data-m="wait"]')).toHaveCount(0);
  const L = await page.evaluate(loudness);
  // only running work (arcs) and the "now" cursor may carry colour on a quiet day
  expect(L.loud.filter((x) => !/mark\.|lane|nowline|now/.test(x)).length, L.loud.join(' | ')).toBeLessThanOrEqual(1);
});
