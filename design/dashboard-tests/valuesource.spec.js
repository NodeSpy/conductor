// The value-source field: every configurable value can be Typed (write-only
// when sensitive), an Env var, a Secret, a Vault reference or a Setting. The
// field shows the reference, never a value, with a resolve check.
const { test, expect, go, applyStaged } = require('./fixtures');

const PASSWORD = 'correct-horse-battery-staple';
const vs = (page, k) => page.locator(`.vs[data-vs="${k}"]`);
const src = (page, k, s) => vs(page, k).locator(`[data-act="vs-src"][data-v="${s}"]`).click();
const hidden = (page, k) => vs(page, k).locator('input[type=hidden]');
async function toCreds(page, method = 'password') {
  await go(page, '#/browser/add', { fresh: true });
  await page.locator(`[data-act="la-method"][data-m="${method}"]`).click();
  await expect(vs(page, 'username')).toBeVisible();
}
async function leaked(page, ...values) {
  return page.evaluate((vs) => { const hay = document.documentElement.outerHTML + JSON.stringify(window.D) + (localStorage.getItem('cd-proto-state-v4') || '') + [...document.querySelectorAll('input')].map((i) => i.value).join('\n'); return vs.filter((v) => hay.includes(v)); }, values);
}

test('each source on a login form: typed, env var, secret, vault and setting', async ({ page }) => {
  await toCreds(page);
  // typed (plain) for a non-sensitive field
  await page.fill('#la-username-in', 'svc-app');
  await expect(hidden(page, 'username')).toHaveValue('svc-app');
  // env var
  await src(page, 'username', 'env');
  await page.locator('#la-username-pick').selectOption('APP_USER');
  await expect(hidden(page, 'username')).toHaveValue('${env.APP_USER}');
  await expect(vs(page, 'username').locator('.vs-st.ok')).toContainText('resolves');
  // setting (fine for a non-sensitive field)
  await src(page, 'username', 'setting');
  await page.locator('#la-username-pick').selectOption('org');
  await expect(hidden(page, 'username')).toHaveValue('${settings.org}');
  await expect(vs(page, 'username').locator('.vs-err')).toHaveCount(0);
  // secret
  await src(page, 'password', 'secret');
  await page.locator('#la-password-pick').selectOption('app_password');
  await expect(hidden(page, 'password')).toHaveValue('${secret.app_password}');
  await expect(vs(page, 'password').locator('.vs-st.ok')).toBeVisible();
  // vault: the browser shows labels only and produces an op:// reference
  await src(page, 'password', 'vault');
  await vs(page, 'password').locator('[data-act="vs-vault"]').click();
  await expect(page.locator('.cdrawer')).toContainText('Pick a vault item');
  await page.locator('.cdrawer [data-vb="op://Agents/Grafana/password"]').click();
  await expect(hidden(page, 'password')).toHaveValue('op://Agents/Grafana/password');
  await expect(vs(page, 'password').locator('.vs-ref')).toHaveText('op://Agents/Grafana/password');
  // the field shows the reference; typed sensitive input is write-only
  await src(page, 'otp', 'typed');
  await expect(page.locator('#la-otp-in')).toHaveAttribute('type', 'password');
});

test('a mixed-source login: typed username, password and TOTP from the same 1Password item', async ({ page }) => {
  await toCreds(page);
  await page.fill('#la-username-in', 'svc-app');
  await src(page, 'password', 'vault');
  await vs(page, 'password').locator('[data-act="vs-vault"]').click();
  await page.locator('.cdrawer [data-vb="op://Agents/App/password"]').click();
  await src(page, 'otp', 'vault');
  // the shortcut: another field already came from the App item
  await vs(page, 'otp').locator('[data-act="vs-same"]', { hasText: 'App · one-time password' }).click();
  await expect(hidden(page, 'otp')).toHaveValue('op://Agents/App/one-time password?attribute=otp');
  await expect(vs(page, 'otp').locator('.vs-st.ok')).toBeVisible();
  await page.locator('[data-act="la-creds"]').click();
  await page.fill('#f-name', 'app2');
  const o = page.locator('#la-bind [data-listk="origins"] input.list-add'); await o.fill('https://app.example.com'); await o.press('Enter');
  await page.locator('#la-bind [data-listk="allow"] select').selectOption('bot:platform-sre');
  await page.locator('[data-act="la-save"]').click();
  await expect(page.locator('.diff')).toContainText('op://Agents/App/password');
  await expect(page.locator('.diff')).toContainText('attribute=otp');
  await expect(page.locator('.diff')).toContainText('username: svc-app');
  await applyStaged(page);
  await go(page, '#/browser/logins');
  await expect(page.locator('[data-login="app2"]')).toContainText('op://Agents/App/password');
});

test('resolve check: resolves, does not resolve (with the reason), and the plain-text env warning', async ({ page }) => {
  await toCreds(page);
  await src(page, 'password', 'secret');
  await page.locator('#la-password-pick').selectOption('VAULT_TOKEN');
  await expect(vs(page, 'password').locator('.vs-st.fail')).toContainText("doesn't resolve");
  await expect(vs(page, 'password').locator('.vs-st.fail')).toContainText('expired');
  await page.locator('#la-password-pick').selectOption('PD_TOKEN');
  await expect(vs(page, 'password').locator('.vs-st.ok')).toContainText('resolves');
  // a vault that's down: nothing to browse, and why
  await src(page, 'otp', 'vault');
  await vs(page, 'otp').locator('[data-act="vs-vault"]').click();
  await page.locator('.cdrawer [data-act="vb-vault"][data-v="hcv"]').click();
  await expect(page.locator('.cdrawer')).toContainText('unreachable');
  await page.locator('.cdrawer [data-close]').click();
  // sensitive field from a plain-text conductor.env var: a warning that suggests a secret
  await src(page, 'password', 'env');
  await page.locator('#la-password-pick').selectOption('APP_PASSWORD');
  await expect(vs(page, 'password').locator('.vs-warn')).toContainText('plain text in conductor.env');
});

test('inline create from the env-var and secret pickers', async ({ page }) => {
  await toCreds(page);
  await page.fill('#la-username-in', 'svc-app');
  // + Add env var (sensitive context: offers secret/vault rather than plain text)
  await src(page, 'password', 'env');
  await page.locator('#la-password-pick').selectOption('__new__');
  await expect(page.locator('.cdrawer')).toContainText('Add an env var');
  await page.fill('#d-name', 'APP_TOKEN');
  await page.locator('.cdrawer .vs [data-act="vs-src"][data-v="secret"]').click();
  await page.locator('#d-value-pick').selectOption('app_password');
  await page.locator('#dr-submit').click();
  await expect(hidden(page, 'password')).toHaveValue('${env.APP_TOKEN}');
  await expect(vs(page, 'password').locator('.vs-st.ok')).toBeVisible();
  // + New secret: the value goes to the vault behind a passkey, the field gets the reference
  await src(page, 'otp', 'secret');
  await page.locator('#la-otp-pick').selectOption('__new__');
  await page.fill('#d-name', 'app_otp_seed'); await page.fill('#d-value', 'JBSWY3DPEHPK3PXP');
  await page.locator('#dr-submit').click(); await page.locator('#su-go').click();
  await expect(hidden(page, 'otp')).toHaveValue('${secret.app_otp_seed}');
  await page.locator('[data-act="la-creds"]').click();
  await page.fill('#f-name', 'tok');
  const o = page.locator('#la-bind [data-listk="origins"] input.list-add'); await o.fill('https://tok.example.com'); await o.press('Enter');
  await page.locator('#la-bind [data-listk="allow"] select').selectOption('bot:platform-sre');
  await page.locator('[data-act="la-save"]').click();
  // the env var is part of the same review, in its own file
  await expect(page.locator('.panel', { hasText: 'conductor.env' }).first()).toContainText('APP_TOKEN');
  expect(await leaked(page, 'JBSWY3DPEHPK3PXP')).toEqual([]);
});

test('a sensitive field can not come from a setting: blocked with an explanation', async ({ page }) => {
  await toCreds(page);
  await page.fill('#la-username-in', 'svc-app');
  await src(page, 'password', 'setting');
  await page.locator('#la-password-pick').selectOption('org');
  await expect(vs(page, 'password').locator('.vs-err')).toContainText('Blocked');
  await page.locator('[data-act="la-creds"]').click();
  await expect(page.locator('[data-field="password"].bad')).toBeVisible();
  await expect(page.locator('#la-bind')).toHaveCount(0);
});

test('the same component in a connector form and in a workflow step input', async ({ page }) => {
  await go(page, '#/connectors/connectors', { fresh: true });
  await page.locator('[data-act="conn-add"]').first().click();
  await page.locator('[data-act="conn-pick"][data-u="slack"], [data-act="cat-pick"][data-name="slack"]').first().click();
  await expect(page.locator('[data-field="bot_token"] .vs-seg')).toBeVisible();
  await page.locator('[data-field="bot_token"] [data-act="vs-src"][data-v="typed"]').click();
  await expect(page.locator('#f-bot_token-in')).toHaveAttribute('type', 'password');
  await page.locator('[data-field="bot_token"] [data-act="vs-src"][data-v="setting"]').click();
  await page.locator('#f-bot_token-pick').selectOption('org');
  await expect(page.locator('[data-field="bot_token"] .vs-err')).toContainText('Blocked');
  await page.locator('.modal [data-close]').first().click();
  // a step input
  await go(page, '#/workflows');
  await page.locator('[data-act="wf-new"]').click();
  await page.fill('#f-name', 'notify'); await page.locator('#gen-submit').click();
  await page.locator('.spine-add .plus[data-loc="steps"][data-i="0"]').click();
  await page.fill('#pk-q', 'post'); await page.locator('.pk-item[data-key="verb:slack-ops.post"]').click();
  const ch = page.locator('[data-wf-path="options.channel"] .vs');
  await expect(ch.locator('.vs-seg')).toBeVisible();
  await ch.locator('[data-act="vs-src"][data-v="setting"]').click();
  await ch.locator('select').selectOption('review_channel');
  await expect.poll(() => page.locator('#wf-yaml .ced-ta').inputValue()).toContain('channel: "${settings.review_channel}"');
});

test('a typed password never renders after save: not in the page, the state, the diff or the config', async ({ page }) => {
  await go(page, '#/connectors/connectors', { fresh: true });
  await page.locator('[data-act="conn-add"]').first().click();
  await page.locator('[data-act="conn-pick"][data-u="slack"], [data-act="cat-pick"][data-name="slack"]').first().click();
  await page.fill('#f-name', 'slack-two');
  await page.locator('[data-field="bot_token"] [data-act="vs-src"][data-v="typed"]').click();
  await page.fill('#f-bot_token-in', PASSWORD);
  await page.locator('#f-app_token-pick').selectOption('SLACK_APP_TOKEN');
  await page.locator('#gen-submit').click();
  await page.locator('#su-go').click(); // the typed value goes to the vault, write-only
  await expect(page.locator('.diff')).toContainText('slack-two:');
  await expect(page.locator('.diff')).toContainText('${secret.slack_two_bot_token}');
  expect(await leaked(page, PASSWORD)).toEqual([]);
  await applyStaged(page);
  await go(page, '#/config/raw');
  expect(await leaked(page, PASSWORD)).toEqual([]);
});
