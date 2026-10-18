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
  // calm: an open folder lets its rows speak; a collapsed one rolls up only what needs you
  await page.locator('[data-act="f-toggle"][data-path="work/billing"]').click();
  await expect(page.locator('.frow.folder', { hasText: 'billing' }).locator('.roll')).toContainText('1 needs you');
  await page.locator('[data-act="f-toggle"][data-path="work/billing"]').click();
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
  await expect(page.locator('.dl-crumbs')).toContainText('Bots › work › platform › team platform');
  await go(page, '#/teams/finance');
  await expect(page.locator('.dl-crumbs').first()).toContainText('Bots › work › billing');
});

test('phone: the tree collapses to a drill-down list', async ({ page }) => {
  test.skip(!isPhone(page), 'phone layout');
  await go(page, '#/bots');
  await page.locator('.drill a.grow[href="#/folders/work"]').click(); // by href: "work" also matches "workflow" in other rows
  await expect(page).toHaveURL(/#\/folders\/work$/);
  await page.locator('.drill a.grow', { hasText: 'platform' }).first().click();
  await expect(page.locator('.drill')).toContainText('release-scribe');
  await expect(page.locator('.drill')).toContainText('platform-sre');
  await expect(page.locator('.page-head .crumbs')).toContainText('work');
});

test('folder is one menu in the filter bar where it helps; no standalone folder button anywhere', async ({ page }) => {
  for (const r of ['#/attention', '#/overview', '#/activity', '#/browser', '#/bots', '#/workflows']) {
    await go(page, r);
    await expect(page.locator('select[data-on-change="folder-scope"], .scope-note')).toHaveCount(0);
  }
  // Activity: Folder ▾ is a searchable path list; picking one writes the qualifier into the search field
  await go(page, '#/activity');
  if (page.viewportSize().width > 720) await page.locator('.fb[data-fb="act"] [data-fb-open="folder"]').click();
  else { await page.locator('.fb[data-fb="act"] [data-fb-sheet]').click(); await page.locator('.fb-sheet [data-fb-row="folder"]').click(); }
  await page.locator('.fb-menu .fb-find').fill('platform');
  await expect(page.locator('.fb-menu .fb-opt').first()).toContainText('work ▸ platform');
  await page.locator('.fb-menu .fb-opt', { hasText: 'work ▸ platform' }).first().click();
  await page.keyboard.press('Escape');
  await expect(page.locator('.fb[data-fb="act"] .fb-in')).toHaveValue(/folder:work\/platform/);
  await expect(page.locator('#act-tbl')).toContainText('platform-sre');
  await expect(page.locator('#act-tbl')).not.toContainText('ci-fixer');
});

/* ---- one shared folder tree: bots, teams and workflows ---- */
const wlRow = (page, name) => page.locator(`#wl-body tr[data-row^="${name} "]`);
test('workflows in folders: move one through the diff; folder filters and search across folders', async ({ page }) => {
  await go(page, '#/workflows', { fresh: true });
  await expect(wlRow(page, 'ci-fixer').locator('[data-l="folder"]')).toContainText('work/platform');
  if (!isPhone(page)) {
    await page.locator('.wfn[data-f="work/platform"]').click();
    await expect(wlRow(page, 'ci-fixer')).toBeVisible();
    await expect(wlRow(page, 'dep-bump')).toBeHidden();
    await expect(page.locator('.wl-crumbs')).toContainText('platform');
    // the folder picked in the tree is a qualifier in the filter bar; source is orthogonal to folders
    const fb = page.locator('.fb[data-fb="wl"] .fb-in');
    await expect(fb).toHaveValue('folder:work/platform');
    await fb.fill('folder:work/platform source:pack');
    await expect(page.locator('#wl-empty')).toBeVisible();
    // a plain search spans every folder
    await fb.fill('dep-bump');
    await expect(wlRow(page, 'dep-bump')).toBeVisible();
    await fb.fill('');
  }
  await page.locator('[data-act="wl-more"][data-name="ci-fixer"]').click();
  await page.locator('#pop-menu [data-act="wl-move"]').click();
  await page.selectOption('#f-to', 'work/web');
  await page.locator('#gen-submit').click();
  await page.goto(page.url().replace(/#.*/, '#/config/apply'));
  await expect(page.locator('.diff')).toContainText('# folder: work/web');
  await applyStaged(page);
  await go(page, '#/workflows');
  await expect(wlRow(page, 'ci-fixer').locator('[data-l="folder"]')).toContainText('work/web');
});

test('a pack workflow sits under its pack until placed by an override; a bot workflow follows its bot', async ({ page }) => {
  await go(page, '#/workflows', { fresh: true });
  await expect(wlRow(page, 'pr-review').locator('[data-l="folder"]')).toContainText('packs/review');
  await expect(wlRow(page, 'bot:platform-sre').locator('[data-l="folder"]')).toContainText('work/platform');
  await page.locator('[data-act="wl-more"][data-name="bot:platform-sre"]').click();
  await expect(page.locator('#pop-menu button', { hasText: 'Move to a folder' })).toHaveAttribute('data-reason', /move the bot/);
  await page.keyboard.press('Escape');
  await page.locator('[data-act="wl-more"][data-name="pr-review"]').click();
  await page.locator('#pop-menu [data-act="wl-move"]').click();
  await expect(page.locator('.modal')).toContainText("A pack can't choose its own folder");
  await page.selectOption('#f-to', 'work/platform');
  await page.locator('#gen-submit').click();
  await page.goto(page.url().replace(/#.*/, '#/config/apply'));
  await expect(page.locator('.diff')).toContainText('folders:');
  await expect(page.locator('.diff')).toContainText('pr-review: work/platform');
  await applyStaged(page);
  // the bot's synthesized workflow follows when its team moves
  await go(page, '#/teams/platform');
  await page.evaluate(() => { const c = JSON.parse(JSON.stringify(S.staged('teams', 'platform'))); c.folder = 'work/sre'; S.stage('teams', 'platform', c); });
  await go(page, '#/workflows');
  await expect(wlRow(page, 'bot:platform-sre').locator('[data-l="folder"]')).toContainText('work/sre');
  await expect(wlRow(page, 'pr-review').locator('[data-l="folder"]')).toContainText('work/platform');
});

test('a folder shows everything in it: bots, teams and workflows, roll-ups and what needs you there', async ({ page }) => {
  await go(page, '#/folders/work%2Fplatform', { fresh: true });
  await expect(page.locator('.folder-stats')).toContainText('Workflows');
  await expect(page.locator('.folder-needs')).toContainText('terraform');
  await expect(page.locator('.tabs')).toContainText('Bots');
  await expect(page.locator('#ftree, .drill').first()).toContainText('platform-sre');
  await page.locator('.tabs a', { hasText: 'Workflows' }).click();
  await expect(page.locator('#fwl-body')).toContainText('ci-fixer');
  await expect(page.locator('#fwl-body')).toContainText('bot:platform-sre');
  await page.locator('.tabs a', { hasText: 'Teams' }).click();
  await expect(page.locator('.page')).toContainText('platform');
});
