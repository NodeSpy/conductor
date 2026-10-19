// Needs you: one tile per kind (Home's tile), counts that match Home's Needs you tile and the rail's badge, a tile
// that filters the list to its kind (selected state, "Showing N of M · Clear"), and counts kept with their nouns.
const { test, expect, go } = require('./fixtures');

test('Needs you: kind tiles match Home and the badge, filter on click, and say what they hide', async ({ page }) => {
  await go(page, '#/overview', { fresh: true });
  const homeN = +(await page.locator('.dl-glance .dl-tile').first().locator('b').textContent());
  const badge = await page.evaluate(() => D.attention.length);
  expect(homeN).toBe(badge);
  // the breakdown keeps each count with its noun (a no-break space)
  expect(await page.locator('.dl-glance .dl-tile').first().locator('.d').textContent()).toMatch(/\d [a-z]/);
  await go(page, '#/attention');
  const tiles = page.locator('#need-tiles .dl-tile');
  const sum = (await tiles.locator('b').allTextContents()).reduce((a, x) => a + +x, 0);
  expect(sum).toBe(badge);
  // the same tile as Home's: the same padding, number and label type, and width in the same grid
  const style = (t) => { const cs = getComputedStyle(t); return [cs.padding, getComputedStyle(t.querySelector('b')).fontSize, getComputedStyle(t.querySelector('.dl-lbl')).fontSize, Math.round(t.getBoundingClientRect().width)]; };
  if (page.viewportSize().width > 720) { const h = await tiles.first().evaluate(style);
    await go(page, '#/overview');
    const hh = await page.locator('.dl-glance .dl-tile').first().evaluate(style);
    expect(h).toEqual(hh);
  } else { // a phone: the kinds are one compact row, so the first item is on the first screen (r5)
    expect(await page.locator('#need-body li, #need-body .dl-need').first().evaluate((e) => e.getBoundingClientRect().top < innerHeight - 120)).toBe(true); }
  await go(page, '#/attention');
  const first = tiles.first(), n = +(await first.locator('b').textContent());
  await first.click();
  await expect(page.locator('#need-tiles .dl-tile').first()).toHaveAttribute('aria-pressed', 'true');
  await expect(page.locator('#need-body .dl-need')).toHaveCount(n);
  await expect(page.locator('.need-shown')).toHaveText(`Showing ${n} of ${badge} · Clear`);
  await page.locator('.need-shown [data-act="need-clear"]').click();
  await expect(page.locator('.need-shown')).toHaveCount(0);
  await expect(page.locator('#need-tiles .dl-tile[aria-pressed="true"]')).toHaveCount(0);
});
