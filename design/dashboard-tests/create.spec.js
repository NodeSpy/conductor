// Create in place: every picker that references another entity offers
// "+ New …". The drawer keeps the parent flow's unsaved state, the new thing
// is selected on return, and parent + child apply as ONE combined diff.
const { test, expect, go, applyStaged } = require('./fixtures');

const pickNew = async (page, sel) => { await page.locator(sel).selectOption('__new__'); await expect(page.locator('.cdrawer')).toBeVisible(); };

test('team page: "Add a bot" creates a new bot on the team, applies, and lands back on the team', async ({ page }) => {
  await go(page, '#/teams/platform');
  await expect(page.locator('#team-add-sel option').first()).toHaveText(/Create a new bot in this team/);
  await expect(page.locator('#team-add-sel')).toContainText('billing-sre (moves from finance)');
  await page.locator('#team-add-sel').selectOption('__new__');
  await expect(page).toHaveURL(/#\/bots\/new\/settings\?team=platform/);
  await expect(page.locator('.page-head .sub')).toContainText('on team platform');
  await expect(page.locator('#f-df_team')).toHaveValue('platform');
  await expect(page.locator('.inh-hint').first()).toContainText('team platform'); // inherited, narrowable
  await page.fill('[id="f-df__name"]', 'platform-cost'); await page.locator('[id="f-df__name"]').blur();
  await page.locator('[data-act="df-review"]').click();
  await expect(page.locator('.diff')).toContainText('platform-cost:');
  await expect(page.locator('.diff')).toContainText('team: platform');
  await applyStaged(page);
  await expect(page).toHaveURL(/#\/teams\/platform$/, { timeout: 8000 });
  await expect(page.locator('.panel', { hasText: 'Bots on this team' })).toContainText('platform-cost');
});

test('a connector instance created from inside a workflow step; one combined diff', async ({ page }) => {
  await go(page, '#/workflows/ci-fixer');
  await page.locator('[data-act="wf-toggle"][data-id="comment"]').click();
  await pickNew(page, '[data-card="comment"] select[data-picker="connector"]');
  await page.fill('#d-name', 'gh-bot');
  await page.fill('#d-github__app_id', '777001');
  await page.locator('#d-github__private_key-pick').selectOption({ index: 2 });
  await page.locator('#d-github__webhook_secret-pick').selectOption({ index: 2 });
  await page.locator('#dr-submit').click();
  await expect(page.locator('.cdrawer')).toHaveCount(0);
  await expect(page.locator('[data-card="comment"] select[data-picker="connector"]')).toHaveValue('gh-bot');
  await expect.poll(() => page.locator('#wf-yaml .ced-ta').inputValue()).toContain('uses: gh-bot.comment');
  await page.locator('[data-act="wf-deploy"]').click();
  await expect(page.locator('.list .item')).toHaveCount(2); // the connector and the workflow, together
  await expect(page.locator('.diff')).toContainText('gh-bot:');
  await expect(page.locator('.diff')).toContainText('uses: gh-bot.comment');
  await applyStaged(page);
  expect(await page.evaluate(() => D.connectors.some((c) => c.name === 'gh-bot'))).toBe(true);
});

test('a secret created from inside a credential binding (value to the vault, binding staged)', async ({ page }) => {
  await go(page, '#/bots/platform-sre/access');
  await page.locator('[data-act="cred-bind"]').first().click();
  await page.fill('#f-name', 'docs-admin'); await page.fill('#f-origin', 'https://docs.example.com');
  await page.selectOption('#f-identity', 'agent:docs@acme/web');
  await page.locator('[data-listk="fields"] select').selectOption('password');
  await pickNew(page, '#f-ref');
  await page.fill('#d-name', 'docs-admin-pw'); await page.fill('#d-value', 's3cret-value-xyz');
  await page.locator('#dr-submit').click();
  await page.locator('#su-go').click();
  await expect(page.locator('#f-ref')).toHaveValue('house/docs-admin-pw');
  await expect(page.locator('#f-name')).toHaveValue('docs-admin'); // the parent kept its unsaved state
  await page.locator('#gen-submit').click();
  await expect(page.locator('.diff')).toContainText('docs-admin:');
  await applyStaged(page);
  const st = await page.evaluate(() => ({ cred: D.access.credentials.find((c) => c.name === 'docs-admin'), secret: D.secrets.some((s) => s.ref === 'house/docs-admin-pw'), leaked: JSON.stringify(D).includes('s3cret-value-xyz') }));
  expect(st.cred.cfg).toMatchObject({ vault: 'house', ref: 'docs-admin-pw' });
  expect(st.secret).toBe(true); expect(st.leaked).toBe(false);
});

test('a folder created from a move dialog', async ({ page }) => {
  await go(page, page.viewportSize().width <= 720 ? '#/folders/personal%2Fhome' : '#/bots'); // on a phone, drill down to it
  await page.locator('[data-act="f-item-menu"][data-name="homelab"]').click();
  await page.locator('#pop-menu [data-act="f-move"]').click();
  await pickNew(page, '#f-to');
  await page.selectOption('#d-parent', 'personal'); await page.fill('#d-name', 'lab');
  await page.locator('#dr-submit').click();
  await expect(page.locator('#f-to')).toHaveValue('personal/lab');
  await page.locator('#gen-submit').click();
  await page.locator('.staged-moves a', { hasText: 'Review' }).click();
  await expect(page.locator('.diff')).toContainText('folder: personal/lab');
  await applyStaged(page);
  expect(await page.evaluate(() => D.bots.find((b) => b.name === 'homelab').cfg.folder)).toBe('personal/lab');
});

test('a team created from a bot\'s definition: parent and child in one diff', async ({ page }) => {
  await go(page, '#/bots/release-scribe/settings');
  await pickNew(page, '#f-df_team');
  await page.fill('#d-name', 'docs'); await page.locator('.cdrawer [data-listk="operators"] select').selectOption('ops');
  await page.locator('#dr-submit').click();
  await expect(page.locator('#f-df_team')).toHaveValue('docs');
  await page.locator('[data-act="df-review"]').click();
  await expect(page.locator('.list .item')).toHaveCount(2);
  await applyStaged(page);
  expect(await page.evaluate(() => [D.teams.some((t) => t.name === 'docs'), D.bots.find((b) => b.name === 'release-scribe').cfg.team])).toEqual([true, 'docs']);
});

test('every entity picker has a create option', async ({ page }) => {
  const seen = new Set(); const missing = [];
  const audit = async (where) => {
    const res = await page.evaluate(() => [...document.querySelectorAll('[data-picker]')].filter((el) => el.offsetParent || el.closest('.modal, .cdrawer')).map((el) => ({ kind: el.dataset.picker, ok: el.tagName === 'SELECT' ? !!el.querySelector('option[value="__new__"]') : !!el.querySelector('[data-create]') || !!el.dataset.create, id: el.id || el.getAttribute('aria-label') || el.className })));
    res.forEach((r) => { seen.add(r.kind); if (!r.ok) missing.push(`${where}: ${r.kind} picker ${r.id}`); });
  };
  const close = () => page.keyboard.press('Escape');
  await go(page, '#/teams/platform'); await audit('team page');
  await page.locator('[data-act="team-edit"]').first().click(); await audit('team form'); await close();
  await go(page, '#/members'); await go(page, '#/bots'); await page.locator('[data-act="team-new"]').click(); await audit('new team'); await close();
  await go(page, '#/teams/platform/routing'); await page.locator('[data-act="rule-edit"][data-i="-1"]').click(); await audit('routing rule'); await close();
  await go(page, '#/bots/release-scribe/settings'); await audit('bot definition');
  await go(page, '#/bots/platform-sre/access'); await page.locator('[data-act="cred-bind"]').first().click(); await audit('credential binding'); await close();
  await go(page, '#/workflows/ci-fixer');
  for (const id of ['fix', 'comment']) await page.locator(`[data-act="wf-toggle"][data-id="${id}"]`).click();
  await audit('agent and verb steps');
  await page.locator('.spine-add .plus').first().click(); await audit('step picker'); await close();
  await go(page, '#/workflows/assess-and-post');
  await go(page, '#/workflows/incident-intake'); await audit('bot step');
  await go(page, '#/connectors/connectors'); await page.locator('[data-act="conn-add"]').click(); await page.locator('[data-act="conn-pick"][data-u="slack"]').click(); await audit('connector settings'); await close();
  await go(page, '#/connectors/runtimes'); await page.locator('[data-act="rt-add"]').click(); await audit('runtime form'); await close();
  await go(page, '#/secrets'); await page.locator('[data-act="secret-add"]').click(); await audit('secret form'); await close();
  await go(page, page.viewportSize().width <= 720 ? '#/folders/personal%2Fhome' : '#/bots'); await page.locator('[data-act="f-item-menu"][data-name="homelab"]').click(); await page.locator('#pop-menu [data-act="f-move"]').click(); await audit('move dialog'); await close();
  await go(page, '#/browser/takeover'); await audit('sign-in takeover wizard');
  await go(page, '#/browser/passkey'); await audit('passkey wizard');
  expect(missing).toEqual([]);
  for (const k of ['bot', 'team', 'folder', 'connector', 'runtime', 'fleet', 'credential', 'secret', 'vault', 'host', 'identity', 'site']) expect([...seen], `a ${k} picker was audited`).toContain(k);
});
