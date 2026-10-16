// Bots in folders: organization only. Create, rename, move (menu and
// drag-and-drop), delete — moves go through the config diff — roll-ups,
// search, breadcrumbs, the phone drill-down, and folder filters elsewhere.
const { test, expect, go, applyStaged, isPhone } = require('./fixtures');

const row = (page, text) => page.locator('.ftree .frow', { hasText: text }).first();
const review = async (page) => { await page.locator('.staged-moves a', { hasText: 'Review' }).click(); };

test('the Bots page: a folder tree with teams, solo bots and roll-ups; New bot / team / folder', async ({ page }) => {
  await go(page, '#/bots');
  await expect(page.locator('.page-head h1')).toHaveText(/Bots/);
  for (const b of ['New bot', 'New team', 'New folder']) await expect(page.locator('.page-head .actions', { hasText: b })).toBeVisible();
  if (isPhone(page)) { await expect(page.locator('.drill')).toContainText('work'); return; }
  await expect(row(page, 'platform-sre')).toBeVisible();
  await expect(page.locator('.frow.folder', { hasText: 'billing' }).locator('.roll')).toContainText('1');
  await expect(page.locator('.frow.bot', { hasText: 'homelab' })).toContainText('solo');
});

test('create a folder and move a solo bot into it with "Move to…", through the diff', async ({ page }) => {
  await go(page, '#/bots');
  await page.locator('[data-act="f-new"]').first().click();
  await page.selectOption('#f-parent', 'personal'); await page.fill('#f-name', 'side'); await page.locator('#gen-submit').click();
  if (!isPhone(page)) await expect(page.locator('.frow.folder', { hasText: 'side' })).toContainText('empty');
  else { await go(page, '#/folders/personal'); await expect(page.locator('.drill')).toContainText('side'); await go(page, '#/folders/personal%2Fhome'); }
  await page.locator('[data-act="f-item-menu"][data-name="homelab"]').click();
  await page.locator('#pop-menu [data-act="f-move"]').click();
  await page.selectOption('#f-to', 'personal/side'); await page.locator('#gen-submit').click();
  await review(page);
  await expect(page.locator('.diff')).toContainText('folder: personal/side');
  await applyStaged(page);
  expect(await page.evaluate(() => D.bots.find((b) => b.name === 'homelab').cfg.folder)).toBe('personal/side');
});

test('drag a team onto a folder to move it', async ({ page }) => {
  test.skip(isPhone(page), 'touch moves with Move to…');
  await go(page, '#/bots');
  await page.locator('.frow.team', { hasText: 'finance' }).dragTo(page.locator('.frow.folder', { hasText: 'personal' }).first());
  await expect(page.locator('.frow.team', { hasText: 'finance' })).toContainText('move staged');
  await review(page);
  await applyStaged(page);
  expect(await page.evaluate(() => D.teams.find((t) => t.name === 'finance').cfg.folder)).toBe('personal');
});

test('rename a folder rewrites what is inside it; delete a folder moves its contents up', async ({ page }) => {
  await go(page, isPhone(page) ? '#/folders/work' : '#/bots');
  await page.locator('[data-act="f-menu"][data-path="work/billing"]').click();
  await page.locator('#pop-menu [data-act="f-rename"]').click();
  await page.fill('#f-name', 'money'); await page.locator('#gen-submit').click();
  await expect(page.locator('.diff')).toContainText('folder: work/money');
  await applyStaged(page);
  expect(await page.evaluate(() => D.teams.find((t) => t.name === 'finance').cfg.folder)).toBe('work/money');
  await go(page, isPhone(page) ? '#/folders/personal' : '#/bots');
  await page.locator('[data-act="f-menu"][data-path="personal/home"]').click();
  await page.locator('#pop-menu [data-act="f-delete"]').click();
  await page.locator('#cf-ok').click();
  await applyStaged(page);
  expect(await page.evaluate(() => D.bots.find((b) => b.name === 'homelab').cfg.folder)).toBe('personal');
});

test('search across folders; breadcrumbs on bot and team pages', async ({ page }) => {
  await go(page, '#/bots');
  await page.fill('[data-on-input="f-q"]', 'warehouse');
  await expect(page.locator('#ftree')).toContainText('platform-data');
  await expect(page.locator('#ftree')).not.toContainText('homelab');
  await go(page, '#/bots/platform-data');
  await expect(page.locator('.bot-hero .crumbs')).toContainText('Bots › work › platform › team platform › platform-data');
  await go(page, '#/teams/finance');
  await expect(page.locator('.crumbs').first()).toContainText('Bots › work › billing');
});

test('phone: the tree collapses to a drill-down list', async ({ page }) => {
  test.skip(!isPhone(page), 'phone layout');
  await go(page, '#/bots');
  await page.locator('.drill a.grow', { hasText: 'work' }).click();
  await expect(page).toHaveURL(/#\/folders\/work$/);
  await page.locator('.drill a.grow', { hasText: 'platform' }).first().click();
  await expect(page.locator('.drill')).toContainText('release-scribe');
  await expect(page.locator('.drill')).toContainText('platform-sre');
  await expect(page.locator('.page-head .crumbs')).toContainText('work');
});

test('folder filters: Needs you, approvals, run history, browsers', async ({ page }) => {
  await go(page, '#/overview');
  await page.locator('.attn select[data-on-change="folder-scope"]').selectOption('work/billing');
  await expect(page.locator('.attn .list')).toContainText('slack-ops.post to #incidents');
  await expect(page.locator('.attn .list')).not.toContainText('terraform apply');
  await expect(page.locator('.scope-note').first()).toContainText('work/billing');
  await go(page, '#/attention');
  await expect(page.locator('select[data-on-change="folder-scope"]').first()).toHaveValue('work/billing');
  await page.locator('select[data-on-change="folder-scope"]').first().selectOption('work/platform');
  await expect(page.locator('.attn')).toContainText('terraform apply');
  await go(page, '#/runs');
  await expect(page.locator('#hist-body')).toContainText('bot:platform-sre');
  await expect(page.locator('#hist-body')).not.toContainText('ci-fixer');
  await go(page, '#/browser');
  await expect(page.locator('.btile')).toHaveCount(1);
  await page.locator('[data-act="folder-clear"]').first().click();
  await expect(page.locator('.btile')).toHaveCount(3);
});
