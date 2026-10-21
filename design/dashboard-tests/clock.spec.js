// The demo is always a normal day: state saved yesterday comes back with every timestamp moved forward, so a page
// opened a day later reads exactly as it did (Home's tiles, Activity's running times and its chart, Needs you's ages).
const { test, expect, go } = require('./fixtures');

const read = (page) => page.evaluate(() => ({ tiles: [...document.querySelectorAll('.dl-glance .dl-tile')].map((t) => t.textContent.replace(/\s+/g, ' ').trim()) }));

test('state saved a day ago reads as today: the same tiles, running times, chart and ages', async ({ page }) => {
  await go(page, '#/overview', { fresh: true });
  await page.evaluate(() => S.save());
  const fresh = await read(page);
  await go(page, '#/activity');
  const freshRunning = await page.locator('#act-tbl tbody.g-live td.num').allTextContents();
  const freshStrip = await page.locator('#act-strip .dl-foot-note').textContent();
  // as if saved yesterday: every timestamp a day earlier, and the save itself a day old
  await page.evaluate(() => { const DAY = 864e5, ISO = /^\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d(\.\d+)?Z$/, KEY = 'cd-proto-state-v7';
    const shift = (o) => { Object.keys(o).forEach((k) => { const v = o[k]; if (typeof v === 'string' && ISO.test(v)) o[k] = new Date(Date.parse(v) - DAY).toISOString(); else if (v && typeof v === 'object') shift(v); }); };
    const saved = JSON.parse(localStorage.getItem(KEY)); shift(saved); localStorage.setItem(KEY, JSON.stringify(saved)); localStorage.setItem(KEY + ':at', String(Date.now() - DAY)); });
  await go(page, '#/overview');
  expect(await read(page)).toEqual(fresh);
  await go(page, '#/activity');
  expect(await page.locator('#act-tbl tbody.g-live td.num').allTextContents()).toEqual(freshRunning);
  await expect(page.locator('#act-strip .dl-foot-note')).toHaveText(freshStrip);
  for (const t of await page.locator('#act-tbl tbody.g-live td.num').allTextContents()) expect(t).not.toMatch(/\d+ h/);
});

test('the chart and the lists cover the same hours: a failure older than the range is offered, not shown in either', async ({ page }) => {
  await go(page, '#/activity', { fresh: true });
  await page.evaluate(() => { const r = D.runs.find((x) => x.status === 'fail'); r.started = new Date(Date.now() - 30 * 3600e3).toISOString(); r.finished = r.started; S.save(); });
  await go(page, '#/activity');
  const listed = await page.locator('#act-tbl tbody.g-attn tr[data-href]').count();
  const charted = await page.evaluate(() => (document.querySelector('#act-strip .dl-foot-note').textContent.match(/(\d+) failed/) || [0, 0])[1]);
  const fails = await page.locator('#act-tbl tbody.g-attn tr[data-href] td.st-c', { hasText: 'Failed' }).count();
  expect(+charted).toBe(fails);
  expect(listed).toBeGreaterThanOrEqual(fails);
  await expect(page.locator('#act-strip [data-act="act-all-time"]')).toContainText('older failure');
});

// A new state version gives every browser a fresh demo on its next load: whatever was saved under an older key is
// dropped, not loaded.
test('saved state under an older version is dropped: the next load is the fresh demo', async ({ page }) => {
  await go(page, '#/overview', { fresh: true });
  const fresh = await page.evaluate(() => ({ bots: D.bots.length, cases: D.cases.length }));
  await page.evaluate(() => { const old = JSON.parse(JSON.stringify(D)); old.bots = []; old.cases = []; localStorage.setItem('cd-proto-state-v6', JSON.stringify(old)); localStorage.setItem('cd-proto-state-v6:at', String(Date.now() - 864e5)); });
  await go(page, '#/overview');
  expect(await page.evaluate(() => ({ bots: D.bots.length, cases: D.cases.length }))).toEqual(fresh);
  expect(await page.evaluate(() => Object.keys(localStorage).filter((k) => k.startsWith('cd-proto-state-v6')))).toEqual([]);
  expect(await page.evaluate(() => S.KEY)).toBe('cd-proto-state-v7');
});

// Reset demo data is the fresh demo of a new visit, even in a tab left open for hours: its times are now's.
test('Reset demo data puts back the fresh demo, timed from now however long the tab was open', async ({ page }) => {
  await page.clock.install();
  await go(page, '#/overview', { fresh: true });
  const newest = () => page.evaluate(() => Math.min(...D.runs.map((r) => Date.now() - Date.parse(r.started))));
  const at0 = await newest();
  await page.evaluate(() => { D.cases.push({ id: 'c-test', title: 'added by hand', status: 'open', notes: [] }); S.save(); });
  await page.clock.fastForward('03:00:00');
  await page.locator('#rail [data-act="reset-demo"], [data-act="reset-demo"]').first().evaluate((b) => b.click()).catch(() => {});
  const viaUi = await page.locator('#cf-ok').count();
  if (viaUi) await page.locator('#cf-ok').click(); else await page.evaluate(() => { S.reset(); UI.render(); });
  expect(await page.evaluate(() => D.cases.some((c) => c.id === 'c-test'))).toBe(false); // what you added is gone
  expect(Math.abs((await newest()) - at0)).toBeLessThan(5 * 60e3); // and the newest run is as recent as on a fresh load
});
