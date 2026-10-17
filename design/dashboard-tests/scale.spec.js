// Scale: Activity pages Finished by 20 and keeps one runs-per-hour strip however many runs or workflows there are;
// Compact density gives one-line rows; Home's Running now shows three. (Riley M9)
const { test, expect, go, isPhone } = require('./fixtures');

const many = (page, n) => page.evaluate((k) => { const base = D.runs.filter((r) => r.status !== 'run'); for (let i = 0; i < k; i++) { const r = JSON.parse(JSON.stringify(base[i % base.length])); r.id = 'r-x' + i.toString(16).padStart(3, '0'); D.runs.push(r); } S.save(); }, n);

test('Activity: Finished pages by 20 ("Show 20 more of N"), however many runs there are', async ({ page }) => {
  await go(page, '#/activity', { fresh: true });
  await many(page, 400);
  await go(page, '#/activity?since=');
  const done = page.locator('#act-tbl tbody.g-done tr[data-href]');
  await expect(done).toHaveCount(20);
  await expect(page.locator('#act-more')).toContainText(/Show 20 more of \d+/);
  await page.locator('#act-more').click();
  await expect(done).toHaveCount(40);
  // the runs-per-hour strip stays one strip, whatever the count
  await expect(page.locator('#act-strip svg.dl-hours')).toHaveCount(1);
});

test('Compact density: tighter rows (Daylight compact: 36px), local and instant', async ({ page }) => {
  test.skip(isPhone(page), 'the phone shows lists, always comfortable');
  await go(page, '#/config', { fresh: true });
  await page.locator('[data-act="pref-density"][data-v="compact"]').click();
  expect(await page.evaluate(() => D.pending.length)).toBe(0);
  await go(page, '#/activity');
  expect(await page.evaluate(() => document.documentElement.dataset.density)).toBe('compact');
  // a finished run is one line; Daylight's compact row is 36px (the same content as comfortable, only tighter)
  const h = await page.locator('#act-tbl tbody.g-done tr[data-href]').first().evaluate((el) => el.getBoundingClientRect().height);
  expect(h).toBeLessThanOrEqual(38);
});

test('Home’s Running now shows three, however many run, and a quiet link to the rest', async ({ page }) => {
  await go(page, '#/overview', { fresh: true });
  await page.evaluate(() => { for (let i = 0; i < 20; i++) D.runs.unshift({ id: 'r-live' + i, workflow: 'wf-' + i, trigger: 'manual', repo: '', number: 0, status: 'run', started: new Date().toISOString(), cost: 0, steps: [] }); S.save(); });
  await go(page, '#/overview');
  await expect(page.locator('.dl-running li')).toHaveCount(3);
  await expect(page.locator('.dl-running a.dl-more')).toContainText(/\+\d+ more running/);
  await expect(page.locator('.dl-tile', { hasText: 'Running' }).first()).not.toContainText('wf-');
});
