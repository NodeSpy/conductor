// Passed is green (ledger item 23): the word and the mark in the success colour everywhere, and passed runs in the
// runs-per-hour bars in a softer green, with running in blue and failures in red on top. Only a pass is green: a run
// waiting on you or parked is neutral. Contrast holds in dark and light.
const { test, expect, go } = require('./fixtures');

const lum = (rgb) => { const c = rgb.match(/[\d.]+/g).slice(0, 3).map((x) => +x / 255).map((x) => (x <= 0.03928 ? x / 12.92 : ((x + 0.055) / 1.055) ** 2.4)); return 0.2126 * c[0] + 0.7152 * c[1] + 0.0722 * c[2]; };
const contrast = (a, b) => { const x = lum(a), y = lum(b); return (Math.max(x, y) + 0.05) / (Math.min(x, y) + 0.05); };
/** Resolve a CSS colour (a token, a color-mix) to rgb() through a probe element. */
const resolve = (page, css) => page.evaluate((v) => { const d = document.createElement('i'); d.style.color = v; document.body.appendChild(d); const c = getComputedStyle(d).color; d.remove();
  const k = document.createElement('canvas').getContext('2d'); k.fillStyle = c; k.fillRect(0, 0, 1, 1); const [r, g, b] = k.getImageData(0, 0, 1, 1).data; return `rgb(${r}, ${g}, ${b})`; }, css);

for (const theme of ['dark', 'light']) {
  test.describe(theme, () => {
    test.use({ protoTheme: theme });
    test('the Passed word and its mark are green, and readable', async ({ page }) => {
      await go(page, page.viewportSize().width > 720 ? '#/activity' : '#/runs/r-7ee3', { fresh: true });
      const st = page.locator('#main .st[data-s="ok"]:not(.mk-only):visible').first();
      await expect(st).toBeVisible();
      const word = await st.evaluate((el) => getComputedStyle(el).color), mark = await st.locator('i').evaluate((el) => getComputedStyle(el).color);
      expect(word).toBe(await resolve(page, 'var(--color-status-success-fg)'));
      expect(mark).toBe(await resolve(page, 'var(--color-status-success-solid)'));
      const bg = await resolve(page, 'var(--color-bg-surface)');
      expect(contrast(word, bg)).toBeGreaterThanOrEqual(4.5);
      expect(contrast(mark, bg)).toBeGreaterThanOrEqual(3);
      // parked keeps its own hue and shape
      expect(await resolve(page, 'var(--color-status-neutral-solid)')).not.toBe(mark);
    });
    test('runs per hour: passed green, running blue, failed red on top; waiting is never green', async ({ page }) => {
      for (const route of ['#/activity', '#/overview']) {
        await go(page, route, { fresh: true });
        const svg = page.locator('#main svg.dl-hours').first();
        const fill = async (cls) => resolve(page, await svg.locator(`rect.${cls}`).first().evaluate((el) => getComputedStyle(el).fill));
        expect(await fill('hb-ok')).toBe(await resolve(page, 'var(--color-status-success-chart)'));
        expect(await fill('hb-run')).toBe(await resolve(page, 'var(--color-status-running-solid)'));
        expect(await fill('hb-fail')).toBe(await resolve(page, 'var(--color-status-danger-solid)'));
        // the soft fill is calm; the bar's edge is its cap in the full success green, 3:1 or more
        expect(contrast(await fill('hb-cap'), await resolve(page, 'var(--color-bg-surface)')), `${route} passed bars' edge`).toBeGreaterThanOrEqual(3);
        expect(await fill('hb-ok')).not.toBe(await fill('hb-cap'));
        // in each bar the failure is the top segment
        const order = await svg.evaluate((el) => { const xs = {}; el.querySelectorAll('rect').forEach((r) => { (xs[r.getAttribute('x')] = xs[r.getAttribute('x')] || []).push([+r.getAttribute('y'), r.getAttribute('class')]); });
          return Object.values(xs).filter((v) => v.some(([, c]) => c === 'hb-fail')).map((v) => v.sort((a, b) => a[0] - b[0])[0][1]); });
        for (const top of order) expect(top).toBe('hb-fail');
      }
      // a run waiting on you, or parked, is counted, but not as a pass
      expect(await page.evaluate(() => ['wait', 'idle'].map((s) => UI.runBucket({ status: s })))).toEqual(['other', 'other']);
    });
  });
}

test('every bar starts on the axis; a legend names each kind with its count; a bar reads out its runs', async ({ page }) => {
  for (const route of ['#/activity', '#/overview']) {
    await go(page, route, { fresh: true });
    const svg = page.locator('#main svg.dl-hours').first();
    const r = await svg.evaluate((el) => { const ax = +el.querySelector('.hb-axis').getAttribute('y1');
      return { ax, bottoms: [...el.querySelectorAll('.hb')].map((g) => { const rs = [...g.querySelectorAll('rect:not(.hb-hit)')]; return rs.length ? Math.max(...rs.map((x) => +x.getAttribute('y') + +x.getAttribute('height'))) : null; }).filter((x) => x != null) }; });
    for (const b of r.bottoms) expect(Math.abs(b - r.ax), `${route}: a bar's bottom vs the axis`).toBeLessThan(0.01);
    const legend = page.locator('#main .hb-legend').first();
    for (const k of ['Passed', 'Failed', 'Running', 'Waiting or parked']) await expect(legend).toContainText(k);
    const sum = await legend.locator('b').allTextContents();
    expect(sum.reduce((a, x) => a + +x, 0)).toBeGreaterThan(0);
    const bar = svg.locator('.hb').filter({ has: page.locator('.hb-fail') }).first();
    await bar.dispatchEvent('click');
    await expect(page.locator('#main .hb-read').first()).toContainText(/\d+ failed/);
  }
});
