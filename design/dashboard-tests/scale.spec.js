// Scale: run history pages by cursor (50 at a time) instead of rendering hundreds of rows; Compact density
// gives one-line rows; the Running tile caps its list. (Riley M9)
const { test, expect, go, isPhone } = require('./fixtures');

const many = (page, n) => page.evaluate((k) => { const base = D.runs.filter((r) => r.status !== 'run'); for (let i = 0; i < k; i++) { const r = JSON.parse(JSON.stringify(base[i % base.length])); r.id = 'r-x' + i.toString(16).padStart(3, '0'); D.runs.push(r); } S.save(); }, n);

test('run history: the first 50, then "Load 50 more", with a cursor', async ({ page }) => {
  await go(page, '#/runs', { fresh: true });
  await many(page, 400);
  await go(page, '#/runs');
  await expect(page.locator('#hist-body tr[data-href]')).toHaveCount(50);
  await expect(page.locator('.more-row')).toContainText('next cursor');
  await page.locator('[data-act="hist-more"]').click();
  await expect(page.locator('#hist-body tr[data-href]')).toHaveCount(100);
});

test('Compact density: tighter rows (Daylight compact: 36px), local and instant', async ({ page }) => {
  test.skip(isPhone(page), 'tables become cards on the phone');
  await go(page, '#/config', { fresh: true });
  await page.locator('[data-act="pref-density"][data-v="compact"]').click();
  expect(await page.evaluate(() => D.pending.length)).toBe(0);
  await go(page, '#/runs');
  expect(await page.evaluate(() => document.documentElement.dataset.density)).toBe('compact');
  // a finished run is one line; Daylight's compact row is 36px (the same content as comfortable, only tighter)
  const h = await page.locator('#hist-body tr.quiet[data-href]').first().evaluate((el) => el.getBoundingClientRect().height);
  expect(h).toBeLessThanOrEqual(38);
});

test('the Running tile lists three names and a count, not every run', async ({ page }) => {
  await go(page, '#/overview', { fresh: true });
  await page.evaluate(() => { for (let i = 0; i < 20; i++) D.runs.unshift({ id: 'r-live' + i, workflow: 'wf-' + i, trigger: 'manual', repo: '', number: 0, status: 'run', started: new Date().toISOString(), cost: 0, steps: [] }); S.save(); });
  await go(page, '#/overview');
  await expect(page.locator('.stat', { hasText: 'Running' }).locator('.d')).toContainText('+ 20 more');
});
