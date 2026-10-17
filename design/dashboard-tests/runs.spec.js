// Run detail tells one consistent story: every step is selectable (click or
// ↑/↓), the tabs show only what that step recorded (a command step has no
// diff), the audit is scoped to the run, the timeline has a time axis, a
// failed run says what happened and what you can do, and two runs compare
// side by side. (Riley M1 + minors, Sam M10)
const { test, expect, go } = require('./fixtures');

test('a failed run: what happened, what you can do, and an audit scoped to the run', async ({ page }) => {
  await go(page, '#/runs/r-7ef0', { fresh: true });
  await expect(page.locator('.run-story')).toContainText('The fix made it worse');
  await expect(page.locator('.run-story')).toContainText('Retry from fix');
  await expect(page.locator('[data-act="run-record"]')).toHaveText(/Download run record/);
  const audit = page.locator('.run-audit');
  await expect(audit).toContainText('workflow_failed');
  await expect(audit).toContainText('run_started');
  for (const other of ['p12', 'acme/api#480', 'pastebin', 'dashboard_session']) await expect(audit).not.toContainText(other);
  // the timeline has a time axis spanning the run
  await expect(page.locator('.gt-axis span')).toHaveCount(4);
  await expect(page.locator('.gt-axis span').last()).toHaveText('12 min');
});

test('every step is selectable; tabs follow the step (no diff on a command step); ↑/↓ moves', async ({ page }) => {
  await go(page, '#/runs/r-7ef0', { fresh: true });
  await expect(page.locator('.gt-row.on')).toContainText('test');
  const tabs = page.locator('#run-step [data-act="run-tab"]');
  await expect(tabs).toHaveText(['Outputs', 'Inputs', 'Log']);
  await page.locator('#run-step [data-act="run-tab"][data-v="log"]').click();
  await expect(page.locator('#run-step .log')).toContainText('FAIL: TestRefund');
  await expect(page.locator('#run-step .log')).not.toContainText('Read internal/billing'); // the fix step's lines stay with the fix step
  await page.locator('.gt-row[data-step="fix"]').click();
  await expect(page.locator('#run-step h3')).toContainText('fix');
  await page.locator('#run-step [data-act="run-tab"][data-v="diff"]').click();
  await expect(page.locator('#run-step .diff')).toContainText('internal/billing/refund.go');
  await page.locator('.gt-row[data-step="fix"]').focus();
  await page.keyboard.press('ArrowDown');
  await expect(page.locator('.gt-row.on')).toContainText('test');
  await expect(page.locator('#run-step [data-act="run-tab"][data-v="diff"]')).toHaveCount(0);
  // a step that never ran says so
  await page.locator('.gt-row[data-step="push"]').click();
  await expect(page.locator('#run-step')).toContainText('Not reached');
  // the story's action jumps to the fix diff
  await page.locator('.run-story [data-act="run-step"][data-step="fix"]').click();
  await expect(page.locator('#run-step .diff')).toContainText('roundHalfEven');
});

test('outputs carry no spend while usage display is off; targets never show #0', async ({ page }) => {
  await go(page, '#/runs/r-7e40', { fresh: true });
  await page.locator('.gt-row[data-step="fix"]').click();
  await expect(page.locator('#run-step')).toContainText('summary');
  await expect(page.locator('#run-step')).not.toContainText('cost_usd');
  await go(page, '#/runs');
  await expect(page.locator('tr[data-href="#/runs/r-7ec8"] [data-l="target"]')).toHaveText('acme/web');
  await expect(page.locator('#hist-body')).not.toContainText('#0');
});

test('compare two runs: step status, durations, and the outputs diff with word highlights', async ({ page }) => {
  await go(page, '#/runs/r-7ef0', { fresh: true });
  await page.locator('.run-story a', { hasText: 'Compare with the last good' }).click();
  await expect(page).toHaveURL(/#\/runs\/r-7ef0\/compare\/r-7e40/);
  const row = page.locator('.cmp-tbl tr[data-step="test"]');
  await expect(row).toContainText('differs'); await expect(row).toContainText('failed'); await expect(row).toContainText('passed');
  await expect(page.locator('.cmp-tbl tr[data-step="push"]')).toContainText('not reached');
  await expect(page.locator('.diff mark.wd').first()).toBeVisible();
  await page.locator('.cmp-tbl tr[data-step="fix"]').click();
  await expect(page.locator('.panel', { hasText: 'r-7ef0 · fix diff' })).toContainText('refund.go');
  await expect(page.locator('.panel', { hasText: 'r-7e40 · fix diff' })).toContainText('tax.go');
  // the picker lists other runs of the same workflow
  await go(page, '#/runs/r-7ef0/compare');
  await expect(page.locator('a[href="#/runs/r-7ef0/compare/r-7ea0"]')).toBeVisible();
});

test('run detail: one run per page, a labelled metadata line, Back to Activity keeps filters and scroll', async ({ page }) => {
  await page.setViewportSize({ width: 1280, height: 600 });
  await go(page, '#/live', { fresh: true });
  await page.locator('[data-act="act-span"][data-v="24h"]').click();
  const span = '24h';
  const top = await page.evaluate(() => { const m = document.getElementById('main'); m.scrollTop = 240; return m.scrollTop; });
  expect(top).toBeGreaterThan(100);
  await page.evaluate(() => { location.hash = '#/live/r-7f3a'; });
  await expect(page).toHaveURL(/#\/live\/r-7f3a/);
  // no run tabs: the page is one run, with a way back and a pager through the running runs
  await expect(page.locator('.run-tabs, .page-head ~ .chips [href^="#/live/"]')).toHaveCount(0);
  await expect(page.locator('.run-nav a', { hasText: 'Back to Activity' })).toBeVisible();
  await expect(page.locator('.run-nav .pager')).toContainText(/1 of 3 running/);
  // the metadata line is labelled, with human durations
  const meta = page.locator('.meta-line');
  for (const [label, value] of [['Started', /\d\d:\d\d \(\d+ min ago\)/], ['Running for', /\d+ min/], ['Run', 'r-7f3a'], ['Workflow', 'ci-fixer'], ['Check on', 'acme/api#482']]) {
    await expect(meta.locator('.ml-i', { has: page.locator('dt', { hasText: new RegExp(`^${label}$`) }) }).locator('dd')).toHaveText(value);
  }
  // whole-run actions only: Stop run, marked on the risk ladder; no paseo link in the header
  await expect(page.locator('.page-head [data-act="stop-run"]')).toBeVisible();
  await expect(page.locator('.page-head .risk[data-risk="5"]')).toBeVisible();
  await expect(page.locator('.page-head', { hasText: 'Open in paseo' })).toHaveCount(0);
  // back: Activity as you left it
  await page.locator('.run-nav a', { hasText: 'Back to Activity' }).click();
  await expect(page).toHaveURL(/#\/live$/);
  await expect.poll(() => page.evaluate(() => document.getElementById('main').scrollTop)).toBe(top);
  await expect(page.locator(`[data-act="act-span"][data-v="${span}"]`)).toHaveAttribute('aria-pressed', 'true');
});

test('Open in paseo is on the step with a live paseo session, never on a run without one', async ({ page }) => {
  // ci-fixer's running step is a claude-cli agent: nothing to open in paseo anywhere on the page
  await go(page, '#/live/r-7f3a', { fresh: true });
  await expect(page.getByText('Open in paseo')).toHaveCount(0);
  // the bot run's step works in paseo: the step says what opening it is for
  await go(page, '#/live/r-7f29');
  const open = page.locator('.paseo-open');
  await expect(open).toContainText('Open in paseo');
  await expect(open).toContainText('Watch or take over this agent’s conversation');
});

test('dismiss a failed run from Needs you; the record stays in history', async ({ page }) => {
  await go(page, '#/runs/r-7ef0', { fresh: true });
  await page.locator('[data-act="run-dismiss"]').click();
  expect(await page.evaluate(() => D.attention.some((a) => a.href === '#/runs/r-7ef0'))).toBe(false);
  await go(page, '#/runs');
  await expect(page.locator('tr[data-href="#/runs/r-7ef0"]')).toBeVisible();
});

test('live: a running step offers "Restart this step", which stops the run first (Cancel focused)', async ({ page }) => {
  await go(page, '#/live/r-7f3a', { fresh: true });
  await page.locator('[data-act="live-step"][data-step="fix"]').click();
  await expect(page.locator('[data-act="retry"][data-from="fix"]')).toHaveCount(0);
  await page.locator('[data-act="restart-step"][data-from="fix"]').click();
  await expect(page.locator('.modal')).toContainText('stops r-7f3a first');
  await expect(page.locator('#cf-cancel')).toBeFocused();
  await page.locator('#cf-ok').click();
  await expect(page).toHaveURL(/#\/live\/r-/);
  expect(await page.evaluate(() => D.runs.find((r) => r.id === 'r-7f3a').status)).toBe('stopped');
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
