// Add a login: three methods, each ending in a binding and the diff;
// Test login; the Logins tab; inline "+ Add a login" in credential pickers.
// No secret value ever reaches the DOM or the saved state.
const { test, expect, go, isPhone, applyStaged, headAction } = require('./fixtures');

const PASSWORD = 'hunter2-Very-Secret';
const SEED = 'JBSWY3DPEHPK3PXP';
async function leaked(page, ...values) {
  return page.evaluate((vs) => { const hay = document.documentElement.outerHTML + JSON.stringify(window.D) + (localStorage.getItem('cd-proto-state-v7') || ''); const inputs = [...document.querySelectorAll('input')].map((i) => i.value).join('\n'); return vs.filter((v) => hay.includes(v) || inputs.includes(v)); }, values);
}
async function bind(page, name, origin, who) {
  await page.fill('#f-name', name);
  await page.fill('#f-origin', origin);
  await page.locator('#la-bind [data-listk="identities"] select').selectOption(who);
  // the "work you started" rule is shown, not a toggle
  await expect(page.locator('#la-bind')).toContainText('work you started');
}

test('username & password: TOTP preview, password stored write-only behind a passkey, references in the diff', async ({ page }) => {
  await go(page, '#/browser/add', { fresh: true });
  await page.locator('[data-act="la-method"][data-m="password"]').click();
  await expect(page.locator('.choice-chosen')).toContainText('Username & password'); // the chosen method carries through
  await page.fill('#la-username-in', 'svc-wiki');
  await page.fill('#la-password-in', PASSWORD);
  await page.locator('.vs[data-vs="otp"] .vs-srcbtn').click(); await page.locator('.vs[data-vs="otp"] [data-act="vs-src"][data-v="typed"]').click();
  await page.fill('#la-otp-in', 'not-base32!');
  await expect(page.locator('#la-totp-ok')).toContainText('not a valid');
  await page.fill('#la-otp-in', SEED);
  await expect(page.locator('#la-totp-ok')).toContainText('code works ✓');
  await page.locator('[data-act="la-creds"]').click();
  await bind(page, 'wiki', 'https://wiki.example.com', 'bot:platform-sre');
  await page.locator('[data-act="la-save"]').click();
  await page.locator('#su-go').click();
  await expect(page.locator('.diff')).toContainText('name: wiki');
  await expect(page.locator('.diff')).toContainText('identities: [bot:platform-sre]');
  await expect(page.locator('.diff')).toContainText('key: login-wiki-password');
  expect(await leaked(page, PASSWORD, SEED, 'svc-wiki')).toEqual([]);
  await applyStaged(page);
  await go(page, '#/browser/logins');
  await page.locator('[data-act="login-more"][data-n="wiki"]').click();
  await page.locator('#pop-menu [data-act="login-uses"]').click();
  const row = page.locator('[data-login="wiki"]');
  await expect(row).toContainText('{{ vault "house" "login-wiki-password" }}');
  await expect(row).toContainText('{{ vault "house" "login-wiki-otp" }}');
  expect(await page.evaluate(() => window.D.secrets.filter((s) => s.ref.startsWith('house/login-wiki-')).length)).toBe(3);
  expect(await leaked(page, PASSWORD, SEED)).toEqual([]);
});

test('from a vault: browse 1Password items (labels only); one item fills all three fields with vault references', async ({ page }) => {
  await go(page, '#/browser/add', { fresh: true });
  await page.locator('[data-act="la-method"][data-m="vault"]').click();
  await expect(page.locator('#la-items .item')).toHaveCount(await page.evaluate(() => D.opItems.length)); // every 1Password item, labels only
  await page.fill('#la-q', 'graf');
  await expect(page.locator('#la-items .item')).toHaveCount(1);
  await page.locator('#la-items [data-act="la-item"]').first().click();
  await expect(page.locator('#la-username')).toHaveValue('{{ vault "op" "Agents/Grafana/username" }}');
  await expect(page.locator('#la-password')).toHaveValue('{{ vault "op" "Agents/Grafana/password" }}');
  await expect(page.locator('#la-otp')).toHaveValue('{{ vault "op" "Agents/Grafana/one-time password?attribute=otp" }}');
  await page.locator('[data-act="la-creds"]').click();
  // the item's website becomes the suggested origin
  await expect(page.locator('#f-origin')).toHaveValue('https://grafana.example.com');
  await page.fill('#f-name', 'grafana-ro');
  // a team entry adds every bot on it now: access goes to bots
  await page.locator('#la-bind [data-listk="identities"] select').selectOption('team:platform');
  await page.locator('[data-act="la-save"]').click();
  await expect(page.locator('.diff')).toContainText('key: Agents/Grafana/password');
  await expect(page.locator('.diff')).toContainText('bot:platform-sre');
  await expect(page.locator('.diff')).not.toContainText('team:platform');
  await applyStaged(page);
  await go(page, '#/browser/logins');
  await page.locator('[data-act="login-more"][data-n="grafana-ro"]').click();
  await page.locator('#pop-menu [data-act="login-uses"]').click();
  await expect(page.locator('[data-login="grafana-ro"]')).toContainText('{{ vault "op" "Agents/Grafana/username" }}');
});

test('sign in yourself goes to the takeover on desktop; on the phone it is disabled with a reason', async ({ page }) => {
  await go(page, '#/browser/add', { fresh: true });
  const card = page.locator('.choice', { hasText: 'Sign in yourself' });
  if (isPhone(page)) { await expect(card).toHaveAttribute('aria-disabled', 'true'); await expect(card).toHaveAttribute('data-reason', /desktop/); return; }
  await card.click();
  await expect(page).toHaveURL(/#\/browser\/takeover/);
});

test('Test login: success, failure and a push that needs you', async ({ page }) => {
  await go(page, '#/browser/logins', { fresh: true });
  await page.locator('[data-login="grafana"] [data-act="login-test"]').click();
  await expect(page.locator('#lt-body')).toContainText('signed in');
  await page.locator('.modal [data-close]').first().click();
  await page.locator('[data-login="statuspage"] [data-act="login-test"]').click();
  await expect(page.locator('#lt-body')).toContainText('sign-in refused');
  await page.locator('.modal [data-close]').first().click();
  await page.locator('[data-login="app"] [data-act="login-test"]').click();
  await expect(page.locator('#lt-body')).toContainText('needs you');
  await page.locator('#lt-approved').click();
  await expect(page.locator('#lt-body')).toContainText('signed in after your approval');
  await page.locator('.modal [data-close]').first().click();
  await expect(page.locator('[data-login="app"]')).toContainText('Works');
});

test('Logins tab: usage history, edit binding and revoke, all through the diff', async ({ page }) => {
  await go(page, '#/browser/logins', { fresh: true });
  await page.locator('[data-act="login-more"][data-n="grafana"]').click();
  await page.locator('#pop-menu [data-act="login-uses"]').click();
  await expect(page.locator('[data-login="grafana"] .login-detail')).toContainText('platform-sre');
  await page.locator('[data-act="login-more"][data-n="grafana"]').click();
  await page.locator('#pop-menu [data-act="lg-revoke"]').click();
  await page.locator('#cf-ok').click();
  await expect(page.locator('.diff')).toContainText('grafana');
  await applyStaged(page);
  expect(await page.evaluate(() => window.D.access.credentials.some((c) => c.name === 'grafana'))).toBe(false);
});

test('"+ Add a login" inside a credential picker: typed password stored, login staged with the parent', async ({ page }) => {
  await go(page, '#/teams/platform/defaults', { fresh: true });
  await headAction(page, '[data-act="team-edit"]');
  await page.locator('[data-listk="credentials"] select').selectOption('__new__');
  await expect(page.locator('.cdrawer')).toContainText('Add a login');
  await page.fill('#d-name', 'runbooks');
  await page.fill('#d-origin', 'https://runbooks.example.com');
  await page.locator('.cdrawer .vs[data-vs="username"] .vs-srcbtn').click(); await page.locator('.cdrawer .vs[data-vs="username"] [data-act="vs-src"][data-v="typed"]').click();
  await page.fill('#d-username-in', 'svc-runbooks');
  await page.locator('.cdrawer .vs[data-vs="password"] .vs-srcbtn').click(); await page.locator('.cdrawer .vs[data-vs="password"] [data-act="vs-src"][data-v="typed"]').click();
  await page.fill('#d-password-in', PASSWORD);
  await page.locator('.cdrawer [data-listk="identities"] select').selectOption('team:platform');
  await page.locator('#dr-submit').click();
  await page.locator('#su-go').click();
  await expect(page.locator('.cdrawer')).toHaveCount(0);
  await expect(page.locator('[data-listk="credentials"]')).toContainText('runbooks');
  await page.locator('#gen-submit').click();
  await expect(page.locator('.diff')).toContainText('name: runbooks');
  await expect(page.locator('.diff')).toContainText('key: login-runbooks-password');
  expect(await leaked(page, PASSWORD)).toEqual([]);
  await applyStaged(page);
});
