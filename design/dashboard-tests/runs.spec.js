// A run, as the approved run page: what happened in a sentence, the actions for the whole run, the step tree (every
// step selectable; the tabs show only what that step recorded), labelled details, an audit scoped to the run; Back
// to Activity keeps its filters and scroll; two runs compare side by side; old Run history links redirect.
const { test, expect, go } = require('./fixtures');

test('a failed run: what happened, what you can do, and an audit scoped to the run', async ({ page }) => {
  await go(page, '#/runs/r-7ef0', { fresh: true });
  await expect(page.locator('.dl-phead .dl-lead')).toContainText('The fix made it worse');
  // the check failed on the fix step's change, so the primary redoes the fix; rerunning the test alone is secondary
  await expect(page.locator('.dl-actions .btn.primary[data-act="retry"]')).toHaveAttribute('data-from', 'fix');
  await expect(page.locator('.dl-actions .btn.secondary[data-act="retry"][data-from="test"]')).toContainText('Rerun “test” only');
  await expect(page.locator('.dl-actions [data-act="run-dismiss"]')).toBeVisible();
  await expect(page.locator('[data-act="run-record"]')).toHaveCount(0); // export is retired: no downloads
  const audit = page.locator('.run-audit');
  await expect(audit).toContainText('workflow failed');
  await expect(audit).toContainText('run started');
  for (const other of ['p12', 'acme/api#480', 'pastebin', 'dashboard_session']) await expect(audit).not.toContainText(other);
  // What happened: the step tree, the failing step called out, later steps not reached
  await expect(page.locator('.dl-tree li.failed')).toContainText('test');
  await expect(page.locator('.dl-tree li.skipped').first()).toContainText('Not reached');
});

test('every step is selectable; tabs follow the step (no diff on a command step)', async ({ page }) => {
  await go(page, '#/runs/r-7ef0', { fresh: true });
  await expect(page.locator('.dl-tree .tl.on')).toContainText('test');
  const tabs = page.locator('#run-step [data-act="run-tab"]');
  await expect(tabs).toHaveText(['Outputs', 'Inputs', 'Log']);
  await page.locator('#run-step [data-act="run-tab"][data-v="log"]').click();
  await expect(page.locator('#run-step .dl-code')).toContainText('FAIL: TestRefund');
  await expect(page.locator('#run-step .dl-code')).not.toContainText('Read internal/billing'); // the fix step's lines stay with the fix step
  await page.locator('.dl-tree .tl[data-step="fix"]').click();
  await expect(page.locator('#run-step h2')).toContainText('fix');
  await page.locator('#run-step [data-act="run-tab"][data-v="diff"]').click();
  await expect(page.locator('#run-step .diff')).toContainText('internal/billing/refund.go');
  await page.locator('.dl-tree .tl[data-step="test"]').click();
  await expect(page.locator('#run-step [data-act="run-tab"][data-v="diff"]')).toHaveCount(0);
  // a step that never ran says so
  await page.locator('.dl-tree .tl[data-step="push"]').click();
  await expect(page.locator('#run-step')).toContainText('Not reached');
});

test('outputs carry no spend while usage display is off; targets never show #0', async ({ page }) => {
  await go(page, '#/runs/r-7e40', { fresh: true });
  await page.locator('.dl-tree .tl[data-step="fix"]').click();
  await expect(page.locator('#run-step')).toContainText('summary');
  await expect(page.locator('#run-step')).not.toContainText('cost_usd');
  await go(page, '#/activity?since=');
  await expect(page.locator('tr[data-href="#/runs/r-7ec8"] td.run')).toContainText('acme/web');
  await expect(page.locator('#act-tbl')).not.toContainText('#0');
});

test('compare two runs: step status, durations, and the outputs diff with word highlights', async ({ page }) => {
  await go(page, '#/runs/r-7ef0', { fresh: true });
  await page.locator('.dl-actions a', { hasText: 'Compare' }).click();
  await expect(page).toHaveURL(/#\/runs\/r-7ef0\/compare$/);
  await expect(page.locator('a[href="#/runs/r-7ef0/compare/r-7ea0"]')).toBeVisible(); // the other runs of the workflow
  await page.locator('a[href="#/runs/r-7ef0/compare/r-7e40"]').click();
  const row = page.locator('.cmp-tbl tr[data-step="test"]');
  await expect(row).toContainText('differs'); await expect(row).toContainText(/failed/i); await expect(row).toContainText(/passed/i);
  await expect(page.locator('.cmp-tbl tr[data-step="push"]')).toContainText(/not reached/i);
  await expect(page.locator('.diff mark.wd').first()).toBeVisible();
  await page.locator('.cmp-tbl tr[data-step="fix"]').click();
  await expect(page.locator('.dl-card', { hasText: 'r-7ef0 · fix diff' })).toContainText('refund.go');
  await expect(page.locator('.dl-card', { hasText: 'r-7e40 · fix diff' })).toContainText('tax.go');
});

test('a run page: Back to Activity keeps the filters and scroll; labelled details; whole-run actions only', async ({ page }) => {
  await page.setViewportSize({ width: 1280, height: 600 });
  await go(page, '#/activity', { fresh: true });
  await page.locator('select[data-k="since"]').selectOption('1440');
  const top = await page.evaluate(() => { const m = document.getElementById('main'); m.style.scrollBehavior = 'auto'; m.scrollTop = 240; return m.scrollTop; });
  expect(top).toBeGreaterThan(100);
  await page.evaluate(() => { location.hash = '#/runs/r-7f3a'; });
  // one run per page: a way back, no tabs and no previous/next
  await expect(page.locator('.run-nav a', { hasText: 'Back to Activity' })).toBeVisible();
  await expect(page.locator('.pager, .run-tabs')).toHaveCount(0);
  // the details are labelled, with human durations
  const facts = page.locator('.dl-facts').first();
  for (const [label, value] of [['Started', /\d+ min ago \(\d\d:\d\d\)/], ['Running for', /\d+ min/], ['Run', 'r-7f3a'], ['Workflow', 'ci-fixer'], ['Triggered by', 'A check failed on acme/api#482']]) {
    await expect(facts.locator(`dt:text-is("${label}") + dd`)).toHaveText(value);
  }
  // whole-run actions only: Stop run (confirmed), no risk meter, no paseo link in the head
  await expect(page.locator('.dl-actions [data-act="stop-run"]')).toBeVisible();
  await expect(page.locator('.risk, .risk-ladder')).toHaveCount(0);
  await expect(page.locator('.dl-actions', { hasText: 'Open in paseo' })).toHaveCount(0);
  await page.locator('.run-nav a', { hasText: 'Back to Activity' }).click();
  await expect(page).toHaveURL(/#\/activity$/);
  await expect.poll(() => page.evaluate(() => document.getElementById('main').scrollTop)).toBe(top);
  await expect(page.locator('select[data-k="since"]')).toHaveValue('1440');
});

test('Open in paseo is on the step with a live paseo session, never on a run without one', async ({ page }) => {
  // ci-fixer's running step is a claude-cli agent: nothing to open in paseo anywhere on the page
  await go(page, '#/runs/r-7f3a', { fresh: true });
  await expect(page.getByText('Open in paseo')).toHaveCount(0);
  // the bot's run works in paseo: the step says what opening it is for
  await go(page, '#/runs/r-7f29');
  const open = page.locator('#run-step .dl-step-open').first();
  await expect(open).toContainText('Open in paseo');
  await expect(open).toContainText('Watch or take over this agent’s conversation');
});

test('dismiss a failed run from Needs you; the record stays in Activity', async ({ page }) => {
  await go(page, '#/runs/r-7ef0', { fresh: true });
  await page.locator('[data-act="run-dismiss"]').click();
  expect(await page.evaluate(() => D.attention.some((a) => a.href === '#/runs/r-7ef0'))).toBe(false);
  await go(page, '#/activity');
  await expect(page.locator('tr[data-href="#/runs/r-7ef0"]')).toBeVisible();
});

test('a running step offers "Restart this step", which stops the run first (Cancel focused)', async ({ page }) => {
  await go(page, '#/runs/r-7f3a', { fresh: true });
  await page.locator('.dl-tree .tl[data-step="fix"]').click();
  await expect(page.locator('[data-act="retry"][data-from="fix"]')).toHaveCount(0);
  await page.locator('[data-act="restart-step"][data-from="fix"]').click();
  await expect(page.locator('.modal')).toContainText('stops r-7f3a first');
  await expect(page.locator('#cf-cancel')).toBeFocused();
  await page.locator('#cf-ok').click();
  await expect(page).toHaveURL(/#\/runs\/r-/);
  expect(await page.evaluate(() => D.runs.find((r) => r.id === 'r-7f3a').status)).toBe('stopped');
});

test('old links: Run history and the live pages land on Activity and the run page', async ({ page }) => {
  await go(page, '#/runs?wf=ci-fixer', { fresh: true });
  await expect(page).toHaveURL(/#\/activity$/);
  await expect(page.locator('select[data-k="wf"]')).toHaveValue('ci-fixer');
  await go(page, '#/live/r-7f3a');
  await expect(page).toHaveURL(/#\/runs\/r-7f3a$/);
  await expect(page.locator('.nav a[data-nav="runs"]')).toHaveCount(0); // no Run history in the rail
});

test('diff lines: no phantom space before a colon; the hunk header names the key path', async ({ page }) => {
  await go(page, '#/runs/r-7ef0/compare/r-7e40', { fresh: true });
  const pad = await page.locator('.diff .dl .y-k').first().evaluate((el) => getComputedStyle(el).paddingRight);
  expect(pad).toBe('0px');
  await go(page, '#/bots/platform-sre/settings');
  await page.evaluate(() => { const c = S.staged('bots', 'platform-sre'); c.description = 'Platform SRE, edited'; S.stage('bots', 'platform-sre', c); });
  await go(page, '#/config/apply');
  await expect(page.locator('.diff .h').first()).toContainText('bots.platform-sre.description');
});
