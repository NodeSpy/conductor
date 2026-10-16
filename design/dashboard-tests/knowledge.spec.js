// Operator-authored knowledge: a team's repo and a bot's memory. Every save is
// one commit authored by the operator. Rename, delete, revert, upload, pins,
// blame, a bot-commit conflict, and proposals: approve with changes, deny
// with a reason, bulk.
const { test, expect, go } = require('./fixtures');

const ta = (page) => page.locator('#rp-ed .ced-ta');
const head = (page, id) => page.evaluate((i) => { const c = R.get(i).log[0]; return { who: c.who, kind: c.kind, msg: c.msg, n: c.changes.length, trailers: c.trailers || '' }; }, id);

test('create a file, edit it, preview it, save it as one operator commit; rename; delete; revert', async ({ page }) => {
  await go(page, '#/teams/platform/knowledge');
  await page.locator('[data-act="rp-new"]').first().click();
  await page.fill('#f-path', 'runbooks/cache.md'); await page.selectOption('#f-template', 'runbook'); await page.locator('#gen-submit').click();
  await expect(page.locator('.ed-head')).toContainText('unsaved');
  await ta(page).fill('# Cache\n\n- **TTL:** 300s on /search\n');
  await page.locator('[data-act="rp-mode"][data-v="preview"]').click();
  await expect(page.locator('#rp-prev h2')).toHaveText('Cache');
  await expect(page.locator('#rp-prev')).toContainText('TTL: 300s on /search');
  await page.fill('#rp-msg', 'runbooks: cache TTL');
  await page.locator('[data-act="rp-save"]').click();
  expect(await head(page, 'team:platform')).toMatchObject({ who: 'operator:ops', kind: 'operator', msg: 'runbooks: cache TTL', n: 1 });
  expect((await head(page, 'team:platform')).trailers).toContain('Authored-By: operator:ops');
  await expect(page.locator('.repo-hist .item').first()).toContainText('ops (operator)');
  await expect(page.locator('.readers')).toContainText('platform-sre');
  // rename
  await page.locator('[data-act="rp-file-menu"]').click(); await page.locator('#pop-menu [data-act="rp-rename"]').click();
  await page.fill('#f-path', 'runbooks/search-cache.md'); await page.locator('#gen-submit').click();
  await expect(page.locator('.repo-side .tree')).toContainText('search-cache.md');
  // delete, then revert the delete from history
  await page.locator('[data-act="rp-file-menu"]').click(); await page.locator('#pop-menu [data-act="rp-delete"]').click(); await page.locator('#cf-ok').click();
  await expect(page.locator('.repo-side .tree')).not.toContainText('search-cache.md');
  await page.locator('[data-act="rp-hist"][data-v="repo"]').click();
  await page.locator('.repo-hist .item', { hasText: 'delete runbooks/search-cache.md' }).locator('[data-act="rp-revert"]').click(); await page.locator('#cf-ok').click();
  await expect(page.locator('.repo-side .tree')).toContainText('search-cache.md');
});

test('approve with changes; deny with a reason that goes back to the bot', async ({ page }) => {
  await go(page, '#/teams/platform/knowledge');
  await page.locator('[data-act="kp-edit"][data-id="k4"]').click();
  await expect(page.locator('.ed-head')).toContainText('approving k4 with changes');
  await expect(ta(page)).toHaveValue(/deregistration_delay/);
  await ta(page).fill((await ta(page).inputValue()) + '\nChecked against modules/web/alb.tf on 2026-10-16.\n');
  await page.locator('[data-act="rp-save"]', { hasText: 'Approve with changes' }).click();
  await page.locator('#su-go').click();
  await expect(page.locator('.kprop', { hasText: 'k4' })).toContainText('approved (edited)');
  const h = await head(page, 'team:platform');
  expect(h).toMatchObject({ who: 'bot:platform-sre', kind: 'bot' });
  expect(h.trailers).toContain('Edited-By: operator:ops');
  await page.locator('[data-act="kp-deny"][data-id="k3"]').click();
  await page.fill('#f-reason', 'Covered by the Safari note in browsers.md already.'); await page.locator('#gen-submit').click();
  await expect(page.locator('.kprop', { hasText: 'k3' })).toContainText('denied: Covered by the Safari note');
  expect(await page.evaluate(() => D.chats['web-triage'].slice(-1)[0].text)).toContain('Denied knowledge proposal k3');
});

test('bulk approve proposals', async ({ page }) => {
  await go(page, '#/teams/platform/knowledge');
  for (const id of ['k4', 'k3']) await page.locator(`.kprop input[data-id="${id}"]`).check();
  await page.locator('[data-act="kp-bulk"][data-d="approve"]').click(); await page.locator('#su-go').click();
  await expect(page.locator('.kprop', { hasText: 'k4' })).toContainText('approved');
  await expect(page.locator('.kprop', { hasText: 'k3' })).toContainText('approved');
  await expect(page.locator('.repo-side .tree')).toContainText('browsers.md');
});

test('upload a markdown file as one commit', async ({ page }) => {
  await go(page, '#/teams/platform/knowledge');
  await page.locator('[data-act="rp-upload"]').click();
  await page.setInputFiles('#up-file', { name: 'oncall-v2.md', mimeType: 'text/markdown', buffer: Buffer.from('# On-call v2\n\nPrimary rotates weekly.\n') });
  await expect(page.locator('#up-list')).toContainText('oncall-v2.md');
  await page.locator('#up-go').click();
  await expect(page.locator('.repo-side .tree')).toContainText('oncall-v2.md');
  expect(await head(page, 'team:platform')).toMatchObject({ who: 'operator:ops', msg: 'import 1 file' });
  await expect(page.locator('#rp-prev')).toContainText('Primary rotates weekly.');
});

test('edit a bot\'s memory, pin a fact, see who wrote each section', async ({ page }) => {
  await go(page, '#/bots/platform-sre/memory');
  await page.locator('[data-act="rp-open"][data-path="knowledge/db-prod-1.md"]').first().click();
  await ta(page).fill((await ta(page).inputValue()).replace('No data loss.', 'No data loss. Pool size 200.'));
  await page.locator('[data-act="rp-save"]').click();
  expect(await head(page, 'bot:platform-sre')).toMatchObject({ who: 'operator:ops' });
  await page.locator('[data-act="rp-file-menu"]').click(); await page.locator('#pop-menu [data-act="rp-pin"]').click();
  await expect(page.locator('.repo-side .tree')).toContainText('pinned');
  await page.locator('[data-act="rp-blame"]').click();
  await expect(page.locator('.bl-group.bot').first()).toContainText('platform-sre');
  await expect(page.locator('.bl-group.operator').first()).toContainText('ops (operator)');
});

test('a bot commits while you edit: the conflict is shown and you rebase explicitly', async ({ page }) => {
  await go(page, '#/bots/platform-sre/memory');
  await page.locator('[data-act="rp-open"][data-path="runbooks/latency.md"]').first().click();
  await ta(page).fill((await ta(page).inputValue()) + '4. Check the CDN cache hit rate.\n');
  await page.locator('[data-act="rp-sim"]').click();
  await page.locator('[data-act="rp-save"]').click();
  await expect(page.locator('.conflict')).toContainText('Conflict');
  await page.locator('[data-act="rp-rebase"]').click();
  await expect(ta(page)).toHaveValue(/auto-noted[\s\S]*CDN cache hit rate|CDN cache hit rate[\s\S]*auto-noted/);
  await page.locator('[data-act="rp-save"]').click();
  const f = await page.evaluate(() => R.get('bot:platform-sre').files['runbooks/latency.md']);
  expect(f).toContain('auto-noted'); expect(f).toContain('CDN cache hit rate');
});

test('unsaved changes are guarded; search spans team knowledge and bot memory', async ({ page }) => {
  await go(page, '#/teams/platform/knowledge');
  await ta(page).fill('# changed\n');
  await page.locator('nav [data-nav="overview"]').first().click().catch(() => page.evaluate(() => { location.hash = '#/overview'; }));
  await expect(page.locator('.modal, .cdrawer').first()).toContainText('Discard unsaved changes?');
  await page.locator('[data-close]').last().click();
  await expect(page).toHaveURL(/knowledge/);
  await page.locator('[data-act="rp-discard"]').click(); await page.locator('#cf-ok').click();
  await page.fill('[data-on-change="rp-q"]', 'deregistration'); await page.locator('[data-on-change="rp-q"]').press('Enter');
  await page.locator('.search-res [data-act="rp-hit"][data-repo="bot:platform-sre"]').first().click();
  await expect(page).toHaveURL(/#\/bots\/platform-sre\/memory/);
  await expect(ta(page)).toHaveValue(/deregistration/);
});
