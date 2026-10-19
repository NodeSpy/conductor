// One page container on every route (ledger items 53, 63): the title and the content start on Activity's left edge (the
// page gutter, the top bar's own inset; the search sits at the right, next to the avatar) and end on its right edge, at 1440, 1280 and 1024; no screen has its own wrapper.
const { test, expect, go } = require('./fixtures');
const { ROUTES } = require('./routes');

const edges = (page) => page.evaluate(() => {
  const p = document.querySelector('#main > .page'); if (!p) return null;
  const r = p.getBoundingClientRect(), cs = getComputedStyle(p), h = p.querySelector('h1'), hd = h && (['.tk-head', '.dl-bothead', '.dl-phead', '.page-head'].map((x) => h.closest(x)).find(Boolean) || h), hr = h && h.offsetParent ? hd.getBoundingClientRect() : null; // a header with a picture or mark before the name starts on the edge
  return { left: Math.round(r.left + parseFloat(cs.paddingLeft)), right: Math.round(r.right - parseFloat(cs.paddingRight)), h1: hr ? Math.round(hr.left) : null, flush: p.classList.contains('flush') };
});

for (const w of [1440, 1280, 1024]) {
  test(`one page container at ${w}px: every route's title and content edges match Activity's`, async ({ page }) => {
    test.skip(page.viewportSize().width <= 720, 'desktop widths');
    test.setTimeout(6 * 60 * 1000);
    await page.setViewportSize({ width: w, height: 900 });
    await go(page, '#/activity', { fresh: true });
    const ref = await edges(page);
    // the reference itself sits on the top bar's inset (63: the search moved right; the gutter is the anchor), and the
    // search is at the top bar's right, next to the avatar
    const bar = await page.evaluate(() => { const st = document.querySelector('.strip'), r = st.getBoundingClientRect(), cs = getComputedStyle(st), q = st.querySelector('.search').getBoundingClientRect(), av = st.querySelector('.op-btn').getBoundingClientRect(); return { inset: Math.round(r.left + parseFloat(cs.paddingLeft)), searchRight: Math.round(q.right), avLeft: Math.round(av.left) }; });
    expect(Math.abs(ref.left - bar.inset), 'the page gutter is the top bar\'s inset').toBeLessThanOrEqual(1);
    expect(bar.avLeft - bar.searchRight, 'the search sits just before the avatar').toBeGreaterThanOrEqual(0);
    expect(bar.avLeft - bar.searchRight, 'the search sits just before the avatar').toBeLessThanOrEqual(40);
    const bad = [];
    for (const r of ROUTES) {
      await go(page, r, { fresh: true });
      const e = await edges(page);
      if (!e || e.flush) continue; // a full-bleed screen (the New bot sheet over Bots) has no page edges of its own
      if (Math.abs(e.left - ref.left) > 1) bad.push(`${r}: content starts at ${e.left}, Activity's at ${ref.left}`);
      if (Math.abs(e.right - ref.right) > 1) bad.push(`${r}: content ends at ${e.right}, Activity's at ${ref.right}`);
      if (e.h1 != null && Math.abs(e.h1 - ref.left) > 1) bad.push(`${r}: the title's header starts at ${e.h1}, not ${ref.left}`);
    }
    expect(bad).toEqual([]);
  });
}
