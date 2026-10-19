// A hand edit of config.yaml changes only what you changed (r5 blocker 3). The reviewer appended a newline and the review
// staged the deletion of browser.allow, logins and evaluate, re-added three credentials as widenings and rewrote a
// trigger as itself; a one-value edit rewrote 14 lines and turned an outdented comment into YAML. Now: spacing alone
// changes nothing; one value is one line; comments, blank lines and the sections the dashboard doesn't model are kept
// as written; an edit the dashboard can't map is refused with the reason, never dropped or guessed.
const { test, expect, go, applyStaged } = require('./fixtures');

const edit = async (page, fn) => { await go(page, '#/config/raw', { fresh: true });
  await page.locator('#raw-ed textarea').evaluate((ta, src) => { const f = new Function('t', src); ta.value = f(ta.value); ta.dispatchEvent(new Event('input', { bubbles: true })); }, `return (${fn.toString()})(t)`);
  await page.locator('[data-act="raw-review"]').first().click(); };
const changedLines = (page) => page.evaluate(() => YAML.diff(S.configText(), S.configText(S.candidate())).filter((r) => r.t !== ' ').length);

test('appending a newline changes nothing: no staged change, no diff', async ({ page }) => {
  await edit(page, (t) => t + '\n');
  await expect(page.locator('.toast').last()).toContainText('Nothing changed');
  expect(await page.evaluate(() => D.pending.length)).toBe(0);
});

test('one value is one line: max_cost_usd 2.00 → 3.00 stages that node and the diff is −1 +1, its comment kept', async ({ page }) => {
  await edit(page, (t) => t.replace('max_cost_usd: 2.00 }   # raised from 1.50 on 2026-10-16', 'max_cost_usd: 3.00 }   # raised from 1.50 on 2026-10-16'));
  await expect(page).toHaveURL(/#\/config\/apply/);
  expect(await changedLines(page)).toBe(2);
  const nodes = await page.evaluate(() => D.pending.filter((c) => c.coll !== 'file').map((c) => `${c.coll}:${c.key}`));
  expect(nodes).toEqual(['workflows:ci-fixer']);
  await expect(page.locator('#main')).toContainText('policy.budget.max_cost_usd: 2 → 3');
  expect(await page.evaluate(() => S.configText(S.candidate()))).toContain('max_cost_usd: 3.00 }   # raised from 1.50 on 2026-10-16');
  expect(await page.evaluate(() => S.widening().length)).toBe(0); // no invented widenings
  await expect(page.locator('#main')).not.toContainText('browser.allow');
  await applyStaged(page);
  expect(await page.evaluate(() => S.configText())).toContain('max_cost_usd: 3.00 }   # raised from 1.50 on 2026-10-16');
});

test('a comment you add is kept where you put it (an outdented one stays a comment), and nothing else moves', async ({ page }) => {
  await edit(page, (t) => t.replace(/(\n\s*budget: \{ max_cost_usd: 2\.00 \}[^\n]*\n)/, '$1  # TODO: lower after Friday   # raised from 1.50\n'));
  await expect(page).toHaveURL(/#\/config\/apply/);
  const after = await page.evaluate(() => S.configText(S.candidate()));
  expect(after).toContain('\n  # TODO: lower after Friday   # raised from 1.50\n');
  expect(after).not.toMatch(/\n\s*TODO: lower after Friday/);
  expect(await changedLines(page)).toBe(1);
  expect(await page.evaluate(() => D.pending.filter((c) => c.coll !== 'file').length), 'a comment changes no node').toBe(0);
});

test('a section the dashboard doesn\'t model is kept as written; a key conductor doesn\'t have is refused by the checks', async ({ page }) => {
  await edit(page, (t) => t + '\n# dry runs only, until Friday\ndry_run: true\n');
  await expect(page).toHaveURL(/#\/config\/apply/);
  expect(await page.evaluate(() => S.configText(S.candidate()))).toMatch(/# dry runs only, until Friday\ndry_run: true\n/);
  expect(await page.evaluate(() => S.validate().errors)).toEqual([]);
  await edit(page, (t) => t + '\nlisten: ":8080"\n');
  expect(await page.evaluate(() => S.validate().errors.join())).toMatch(/listen: field listen not found in type config\.Config/);
});

test('an edit the dashboard can\'t map (browser.allow) is refused with the reason, and nothing is staged', async ({ page }) => {
  await edit(page, (t) => t.replace('    - docs.example.com\n', '    - docs.example.com\n    - evil.example.com\n'));
  await expect(page.locator('#raw-err')).toBeVisible();
  await expect(page.locator('#raw-err')).toContainText("can't stage");
  await expect(page.locator('#raw-err')).toContainText('browser.allow');
  expect(await page.evaluate(() => D.pending.length)).toBe(0);
});

test('a form edit of one value is one line too, also after a hand edit kept the file\'s comments', async ({ page }) => {
  await go(page, '#/overview', { fresh: true });
  const one = () => page.evaluate(() => { const c = JSON.parse(JSON.stringify(S.staged('bots', 'platform-sre'))); c.description = 'SRE for the platform services, nights too'; S.stage('bots', 'platform-sre', c); const n = YAML.diff(S.configText(), S.configText(S.candidate())).filter((r) => r.t !== ' ').length; D.pending = []; return n; });
  expect(await one()).toBe(2);
  // a hand edit adds a comment and applies; then the same form edit still changes one line, and the comment stays
  await page.evaluate(() => { const t = S.configText().replace('\nbots:\n', '\n# reviewed weekly\nbots:\n'); S.stage('file', 'config.yaml', t); });
  await page.goto(page.url().replace(/#.*/, '#/config/apply')); await applyStaged(page);
  expect(await page.evaluate(() => S.configText())).toContain('\n# reviewed weekly\nbots:\n');
  expect(await one()).toBe(2);
});
