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

test('every bar starts on the axis; a legend names each kind with its count; no per-bar readout', async ({ page }) => {
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
    // 68: no readout line, no hint, and the legend row says only the legend
    expect(await page.locator('#main .hb-read').count()).toBe(0);
    await expect(page.locator('#main .hb-foot').first()).not.toContainText(/Point at|tap it|–now|\d\d:\d\d/);
  }
});

// 68: Home's chart is static: no focusable bar, no pointer cursor, nothing on hover or tap; the failures line stays a link
test('Home: the runs chart is static, the named failures stay links', async ({ page }) => {
  await go(page, '#/overview', { fresh: true });
  const svg = page.locator('#main .ov-runs svg.dl-hours');
  expect(await svg.locator('.hb[tabindex], .hb[data-hbtip], .hb-hit').count()).toBe(0);
  expect(await svg.locator('.hb').first().evaluate((g) => getComputedStyle(g).cursor)).not.toBe('pointer');
  const bar = svg.locator('.hb').filter({ has: page.locator('.hb-fail') }).first();
  if (await bar.count()) { await bar.hover(); await bar.dispatchEvent('click'); }
  expect(await page.locator('#main .hb-tip:not([hidden]), #main .hb.on').count()).toBe(0);
  await expect(page.locator('#main .ov-runs .dl-foot-note a.dl-link').first()).toHaveAttribute('href', /#\/runs\//);
});

// 68b: Activity's Runs per hour: a tooltip on hover, focus or tap: the time, each kind with its swatch and count, the
// failed runs named (links); it sits above its bar inside the card and never over the legend; arrow keys move it; Esc
// closes it; a quiet outline on its bar, no shaded column
test('Activity: each bar has a tooltip (hover, keyboard, tap) that is never clipped and never covers the legend', async ({ page }) => {
  // a phone's Activity has no chart (the runs list is the page there); the touch project taps it on a tablet instead
  const phone = page.viewportSize().width <= 720;
  if (phone) await page.setViewportSize({ width: 900, height: 900 });
  await go(page, '#/activity', { fresh: true });
  const card = page.locator('#main .dl-strip'), svg = card.locator('svg.dl-hours'), tip = card.locator('.hb-tip');
  const bar = svg.locator('.hb').filter({ has: page.locator('.hb-fail') }).first();
  if (phone) await bar.tap(); else await bar.hover();
  await expect(tip).toBeVisible();
  await expect(tip).toContainText(/Failed \d+/);
  await expect(tip.locator('a.dl-link[href^="#/runs/"]').first()).toBeVisible();
  await expect(tip.locator('.hb-sw-fail')).toHaveCount(1);
  const geo = async () => page.evaluate(() => { const c = document.querySelector('#main .dl-strip').getBoundingClientRect(), t = document.querySelector('#main .dl-strip .hb-tip').getBoundingClientRect(), l = document.querySelector('#main .dl-strip .hb-legend').getBoundingClientRect(), on = document.querySelector('#main .dl-strip .hb.on'), b = on && on.getBoundingClientRect();
    return { inside: t.left >= c.left - 0.5 && t.right <= c.right + 0.5 && t.top >= 0, overBars: !(t.bottom <= document.querySelector('#main .dl-strip svg.dl-hours').getBoundingClientRect().top + 0.5), overLegend: !(t.bottom <= l.top || t.top >= l.bottom || t.right <= l.left || t.left >= l.right), above: b ? t.bottom <= b.bottom : false, inView: t.left >= 0 && t.right <= innerWidth, shade: on ? getComputedStyle(on.querySelector('.hb-hit')).fill : '' }; });
  let g = await geo();
  expect(g.inside, 'inside the card and the window').toBe(true); expect(g.overBars, 'never over the bars').toBe(false); expect(g.inView, 'inside the window').toBe(true); expect(g.overLegend, 'not over the legend').toBe(false); expect(g.above, 'above its bar').toBe(true);
  expect(g.shade, 'no shaded column').toMatch(/transparent|rgba\(0, 0, 0, 0\)|none/);
  if (phone) { await bar.tap(); await expect(tip, 'a second tap closes it').toBeHidden(); await bar.tap(); await expect(tip).toBeVisible(); await page.locator('#main h1').tap(); await expect(tip, 'a tap elsewhere closes it').toBeHidden(); }
  else { await page.keyboard.press('Escape'); await expect(tip).toBeHidden(); }
  // keyboard: the last bar (now) is the tab stop; arrows move the tooltip along, Home to the first bar, which stays inside the card
  await svg.locator('.hb[tabindex="0"]').focus();
  await expect(tip).toBeVisible();
  const t0 = await tip.innerText();
  await page.keyboard.press('ArrowLeft');
  await expect(tip).not.toHaveText(t0);
  await page.keyboard.press('Home');
  g = await geo(); expect(g.inside, 'the first bar\'s tooltip is inside the card').toBe(true); expect(g.inView).toBe(true);
  await page.keyboard.press('End');
  g = await geo(); expect(g.inside, 'the last bar\'s tooltip is inside the card').toBe(true); expect(g.inView).toBe(true);
  await page.keyboard.press('Escape');
  await expect(tip).toBeHidden();
});
