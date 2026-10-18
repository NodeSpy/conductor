// Switching tabs keeps the page still. On every tabbed page, each tab is clicked in turn; the tab bar it sits in must
// stay exactly where it was before, during (sampled every frame while the switch renders) and after the switch, and
// the rail, the window's sideways scroll and the content column must not move either. Scrollbars are forced to the
// classic, always-visible kind (a mouse attached, or macOS set to show them), so a long tab and a short one would
// shift the content if the gutter weren't stable.
const { test, expect, go } = require('./fixtures');

const START = ['#/config', '#/logs/daemon', '#/connectors/connectors', '#/secrets', '#/extensions/packs', '#/bots/platform-sre', '#/teams/platform', '#/folders/work'];
const BARS = '#main .dl-subnav, #main .tabs, #main .dl-tabs, #main [role="tablist"]';
const CLASSIC = '::-webkit-scrollbar { width: 15px; height: 15px; } ::-webkit-scrollbar-thumb { background: #888; } ::-webkit-scrollbar-track { background: #ddd; }';

test('switching tabs moves nothing: not the tab bar, the rail, the content column or the window', async ({ page }) => {
  test.setTimeout(10 * 60 * 1000);
  await page.addInitScript((css) => { document.addEventListener('DOMContentLoaded', () => { const s = document.createElement('style'); s.textContent = css; document.head.appendChild(s); }); }, CLASSIC);
  const problems = [];
  for (const start of START) {
    await go(page, start, { fresh: true });
    // the page's own entrance (on arrival, not on a tab switch) settles before anything is measured
    await page.waitForFunction(() => document.getAnimations().every((a) => a.playState !== 'running'), null, { timeout: 3000 }).catch(() => {});
    const bars = await page.evaluate((sel) => [...document.querySelectorAll(sel)].map((t) => [...t.querySelectorAll('a[href]')].map((a) => a.getAttribute('href'))), BARS);
    for (const hrefs of bars) {
      for (const href of hrefs) {
        const sig = hrefs.map((h) => h.split('?')[0]).sort().join(' ');
        const link = page.locator(`#main a[href="${href}"]`).first();
        if (!(await link.isVisible().catch(() => false))) continue;
        const before = await page.evaluate(([sel, sig]) => {
          const find = () => [...document.querySelectorAll(sel)].find((t) => [...t.querySelectorAll('a[href]')].map((a) => a.getAttribute('href').split('?')[0]).sort().join(' ') === sig);
          const pos = () => { const b = find(), r = b && b.getBoundingClientRect(), rail = document.querySelector('.rail'), pg = document.querySelector('#main .page');
            return { bar: r ? [Math.round(r.top), Math.round(r.left)] : null, rail: rail ? Math.round(rail.getBoundingClientRect().left) : 0, x: scrollX, mainX: document.querySelector('#main').scrollLeft, col: pg ? Math.round(pg.getBoundingClientRect().left) : null }; };
          window.__samples = []; let n = 0; const tick = () => { window.__samples.push(pos()); if (++n < 50) requestAnimationFrame(tick); }; requestAnimationFrame(tick);
          return pos();
        }, [BARS, sig]);
        await link.click();
        await page.waitForTimeout(900);
        const samples = await page.evaluate(() => window.__samples);
        const what = `${start} → ${href}`;
        for (const s of samples) {
          if (s.bar && before.bar && (s.bar[0] !== before.bar[0] || s.bar[1] !== before.bar[1])) { problems.push(`${what}: the tab bar moved from ${before.bar} to ${s.bar}`); break; }
          if (s.rail !== before.rail || s.x || s.mainX) { problems.push(`${what}: the window or the rail moved sideways (rail ${s.rail}, scrollX ${s.x}, main ${s.mainX})`); break; }
          if (s.col !== null && before.col !== null && s.col !== before.col) { problems.push(`${what}: the content column moved from ${before.col} to ${s.col}`); break; }
        }
        if (samples.every((s) => !s.bar)) problems.push(`${what}: the tab bar is gone after the switch`);
      }
    }
  }
  expect(problems).toEqual([]);
});

test('the System tabs, Safety among them, put their tab bar in one place', async ({ page }) => {
  const tops = [];
  for (const r of ['#/config', '#/safety', '#/updates', '#/logs', '#/security']) {
    await go(page, r);
    await expect(page.locator('#main .dl-subnav a[aria-current="page"]')).toHaveCount(1);
    tops.push(await page.evaluate(() => { const r = document.querySelector('#main .dl-subnav').getBoundingClientRect(); return `${Math.round(r.top)},${Math.round(r.left)}`; }));
  }
  expect(new Set(tops).size, tops.join(' | ')).toBe(1);
  await expect(page.locator('#main .dl-subnav')).toContainText('Safety');
});
