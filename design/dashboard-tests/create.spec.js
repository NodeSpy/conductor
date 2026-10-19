// Create in place: every picker that references another entity offers
// "+ New …". The drawer keeps the parent flow's unsaved state, the new thing
// is selected on return, and parent + child apply as ONE combined diff.
const { test, expect, go, applyStaged, headAction } = require('./fixtures');

const pickNew = async (page, sel) => { await page.locator(sel).selectOption('__new__'); await expect(page.locator('.cdrawer')).toBeVisible(); };

test('team page: "Add a bot" offers the team\'s bots and others, and opens New bot on the team', async ({ page }) => {
  await go(page, '#/teams/platform');
  // the picker starts empty (no bot is moved by default); creating one is its last choice
  await expect(page.locator('#team-add-sel option').first()).toHaveText('Choose a bot…');
  await expect(page.locator('#team-add-sel option').last()).toHaveText(/Create a new bot in this team/);
  await expect(page.locator('#team-add-sel')).toContainText('billing-sre (moves from finance)');
  await page.locator('#team-add-sel').selectOption('__new__');
  await expect(page).toHaveURL(/#\/bots\/new\?team=platform/); // the one New bot: the sheet, on this team
  await expect(page.locator('.nb-sheet')).toBeVisible();
  await page.locator('[data-act="bw-purpose"]').first().click();
  await expect(page.locator('#bw-team')).toHaveValue('platform');
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

test('a vault entry created from inside a login created from inside a binding (values to the vault, one diff)', async ({ page }) => {
  await go(page, '#/bots/platform-sre/access');
  await page.locator('[data-act="cred-bind"]').first().click();
  await pickNew(page, '#f-cred');
  await page.fill('#d-name', 'docs-admin'); await page.fill('#d-origin', 'https://docs.example.com');
  await page.fill('#d-username-in', 'svc-docs-91');
  await page.locator('#d-password-pick').selectOption('__new__');
  await page.fill('#d-key', 'docs-admin-pw'); await page.fill('#d-value', 's3cret-value-xyz');
  await page.locator('#dr-submit').last().click();
  await page.locator('#su-go').click();
  await expect(page.locator('#d-password')).toHaveValue('{{ vault "house" "docs-admin-pw" }}');
  await expect(page.locator('#d-name')).toHaveValue('docs-admin'); // the parent kept its unsaved state
  await page.locator('#dr-submit').click();
  await page.locator('#su-go').click(); // the typed username goes to the vault too
  await expect(page.locator('#f-cred')).toHaveValue('docs-admin');
  await page.locator('#gen-submit').click();
  await expect(page.locator('.diff').first()).toContainText('name: docs-admin');
  await applyStaged(page);
  const st = await page.evaluate(() => ({ cred: D.access.credentials.find((c) => c.name === 'docs-admin'), secret: D.secrets.some((s) => s.ref === 'house/docs-admin-pw'), leaked: JSON.stringify(D).includes('s3cret-value-xyz') || JSON.stringify(D).includes('svc-docs-91') }));
  expect(st.cred.cfg).toMatchObject({ password: { vault: 'house', key: 'docs-admin-pw' }, identities: ['bot:platform-sre'] });
  expect(st.secret).toBe(true); expect(st.leaked).toBe(false);
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
  await headAction(page, '[data-act="team-edit"]'); await audit('team form'); await close();
  await go(page, '#/members'); await go(page, '#/bots'); await headAction(page, '[data-act="team-new"]'); await audit('new team'); await close();
  await go(page, '#/teams/platform/routing'); await page.locator('[data-act="rule-edit"][data-i="-1"]').click(); await audit('routing rule'); await close();
  await go(page, '#/bots/release-scribe/settings'); await audit('bot definition');
  await go(page, '#/bots/platform-sre/access'); await page.locator('[data-act="cred-bind"]').first().click(); await audit('credential binding'); await close();
  await go(page, '#/workflows/ci-fixer');
  for (const id of ['fix', 'comment']) await page.locator(`[data-act="wf-toggle"][data-id="${id}"]`).click();
  await audit('agent and verb steps');
  await page.locator('.spine-add .plus').first().click(); await audit('step picker'); await close();
  await go(page, '#/workflows/assess-and-post');
  await go(page, '#/workflows/incident-intake'); await audit('bot step');
  await go(page, '#/connectors/connectors'); await page.locator('[data-act="conn-add"]').click(); await page.locator('[data-act="cat-pick"][data-kind="connector"][data-name="slack"]').click(); await audit('connector settings'); await close();
  await go(page, '#/connectors/runtimes'); await page.locator('[data-act="rt-add"]').click(); await audit('runtime form'); await close();
  await go(page, '#/secrets'); await page.locator('[data-act="secret-add"]').click(); await audit('secret form'); await close();
  await go(page, '#/bots'); await page.locator('[data-act="bv-more"][data-m="homelab"]').click(); await page.locator('#pop-menu [data-act="bv-move"]').click(); await audit('move dialog'); await close();
  await go(page, '#/browser/takeover'); await audit('sign-in takeover wizard');
  await go(page, '#/browser/passkey'); await audit('passkey wizard');
  expect(missing).toEqual([]);
  for (const k of ['bot', 'team', 'connector', 'runtime', 'fleet', 'credential', 'secret', 'vault', 'host', 'identity', 'site']) expect([...seen], `a ${k} picker was audited`).toContain(k);
});
