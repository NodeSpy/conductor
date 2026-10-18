// The shared list row (mark | content | time | action): in every list with actions, at desktop and phone widths, a
// row's time and its button never touch (a gap of at least the spacing token), the button sits centred on the row,
// and down a list the times' right edges and the buttons' left edges are identical (on a phone they sit under the text,
// still apart). A long time label ("yesterday", "3 days ago") is part of the check.
const { test, expect, go } = require('./fixtures');

const LISTS = [['#/attention', '.dl-needs'], ['#/overview', '.dl-needs'], ['#/bots', '.bot-list .dl-list'], ['#/bots/platform-sre/proposals', '.dl-list'], ['#/folders/work', '.dl-needs']];

test('list rows: time and action never touch, and line up down the list', async ({ page }) => {
  // one item a day old, so the longest time label is in the list
  await go(page, '#/attention', { fresh: true });
  await page.evaluate(() => { const a = D.attention.find((x) => x.kind === 'approval'); if (a) a.age = '3d'; S.save(); });
  const problems = [];
  for (const [route, list] of LISTS) {
    await go(page, route);
    const out = await page.evaluate((sel) => { const gap = parseFloat(getComputedStyle(document.documentElement).getPropertyValue('--space-2')) || 8, bad = [];
      document.querySelectorAll(`#main ${sel}`).forEach((ul) => { const rows = [...ul.querySelectorAll(':scope > li > .dl-need-a')].filter((r) => r.getClientRects().length);
        const timeR = [], btnL = [];
        rows.forEach((r) => { const t = r.querySelector(':scope > .dl-age'), b = r.querySelector(':scope > .dl-na > .btn, :scope > .dl-na > .st'); if (!t || !t.textContent.trim() || !b) return;
          const tr = t.getBoundingClientRect(), br = b.getBoundingClientRect(), cs = getComputedStyle(r), r0 = r.getBoundingClientRect(), rr = { top: r0.top + parseFloat(cs.paddingTop), bottom: r0.bottom - parseFloat(cs.paddingBottom) }, name = (r.querySelector('b') || r).textContent.trim().slice(0, 30);
          const sameLine = Math.abs((tr.top + tr.bottom) / 2 - (br.top + br.bottom) / 2) < 12;
          if (sameLine && br.left - tr.right < gap) bad.push(`"${name}": time and action ${Math.round(br.left - tr.right)}px apart`);
          if (!sameLine && (tr.bottom > br.top && tr.top < br.bottom && tr.right > br.left)) bad.push(`"${name}": time and action overlap`);
          if (innerWidth > 720 && Math.abs((br.top + br.bottom) / 2 - (rr.top + rr.bottom) / 2) > 2) bad.push(`"${name}": the action is off the row's centre by ${Math.round((br.top + br.bottom) / 2 - (rr.top + rr.bottom) / 2)}px`);
          if (b.matches('.btn')) { timeR.push(Math.round(tr.right)); btnL.push(Math.round(br.left)); } });
        if (innerWidth > 720 && new Set(timeR).size > 1) bad.push(`times end at different x: ${[...new Set(timeR)].join(', ')}`);
        if (new Set(btnL).size > 1 && innerWidth > 720) bad.push(`buttons start at different x: ${[...new Set(btnL)].join(', ')}`); });
      return bad; }, list);
    out.forEach((x) => problems.push(`${route}: ${x}`));
  }
  expect(problems).toEqual([]);
});
