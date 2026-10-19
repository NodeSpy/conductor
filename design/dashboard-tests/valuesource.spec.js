// The value-source field: a configurable value is Typed (write-only when
// sensitive), an Env var (${env.NAME}), a Vault entry ({{ vault "v" "key" }},
// the one reference syntax) or a Setting (${settings.name}). The field shows
// the reference, never a value, with a resolve check. Browser credentials take
// only vault entries ({ vault, key } in config), so a login offers Typed (stored
// in conductor's vault) and Vault.
const { test, expect, go, applyStaged } = require('./fixtures');

const PASSWORD = 'correct-horse-battery-staple';
const vs = (page, k) => page.locator(`.vs[data-vs="${k}"]`).first();
// the source is a quiet menu inside the field: open it, then pick
const src = async (page, k, s) => { await vs(page, k).locator('.vs-srcbtn').click(); await vs(page, k).locator(`[data-act="vs-src"][data-v="${s}"]`).click(); };
const hidden = (page, k) => vs(page, k).locator('input[type=hidden]');
// a reference is picked from a lookup: type to search, then pick from the list (never typed in)
const lookup = async (page, sel, q, ref) => { const inp = page.locator(sel); await inp.click(); await inp.fill(q); await page.locator(`${sel.replace(/-pick$/, '-list')} [data-act="vs-choose"][data-ref*="${ref}"]`).first().click(); };
const lookupNew = async (page, sel) => { await page.locator(sel).click(); await page.locator(`${sel.replace(/-pick$/, '-list')} [data-act="vs-new"]`).click(); };
async function slackForm(page) {
  await go(page, '#/connectors/connectors', { fresh: true });
  await page.locator('[data-act="conn-add"]').first().click();
  await page.locator('[data-act="conn-pick"][data-u="slack"], [data-act="cat-pick"][data-name="slack"]').first().click();
  await page.locator('[data-act="conn-manual"]').click(); // fill it in yourself, rather than Connect with Slack
  await expect(vs(page, 'bot_token')).toBeVisible();
}
async function toCreds(page, method = 'password') {
  await go(page, '#/browser/add', { fresh: true });
  await page.locator(`[data-act="la-method"][data-m="${method}"]`).click();
  await expect(vs(page, 'username')).toBeVisible();
}
async function leaked(page, ...values) {
  return page.evaluate((xs) => { const hay = document.documentElement.outerHTML + JSON.stringify(window.D) + (localStorage.getItem('cd-proto-state-v6') || '') + [...document.querySelectorAll('input')].map((i) => i.value).join('\n'); return xs.filter((v) => hay.includes(v)); }, values);
}

test('each source on a connector form: typed (write-only), env var, vault entry and setting', async ({ page }) => {
  await slackForm(page);
  // a sensitive field defaults to Vault and offers no Secret layer
  await expect(vs(page, 'bot_token').locator('[data-act="vs-src"][data-v="vault"]')).toHaveAttribute('aria-pressed', 'true');
  await expect(vs(page, 'bot_token').locator('[data-act="vs-src"]')).toHaveCount(4);
  await lookup(page, '#f-bot_token-pick', 'slack bot', 'Agents/Slack/bot-token');
  await expect(hidden(page, 'bot_token')).toHaveValue('{{ vault "op" "Agents/Slack/bot-token" }}');
  await expect(vs(page, 'bot_token').locator('.vs-st.ok')).toContainText('exists');
  await expect(vs(page, 'bot_token').locator('.vs-chosen')).toContainText('Slack');
  await src(page, 'bot_token', 'env');
  await lookup(page, '#f-bot_token-pick', 'APP_U', 'env.APP_USER');
  await expect(hidden(page, 'bot_token')).toHaveValue('${env.APP_USER}');
  await src(page, 'bot_token', 'typed');
  await expect(page.locator('#f-bot_token-in')).toHaveAttribute('type', 'password');
  // the vault browser shows labels only and writes the canonical reference
  await src(page, 'bot_token', 'vault');
  await vs(page, 'bot_token').locator('[data-act="vs-vault"]').click();
  await expect(page.locator('.cdrawer')).toContainText('Pick a vault item');
  await page.locator('.cdrawer [data-vb*="Agents/Grafana/password"]').click();
  await expect(hidden(page, 'bot_token')).toHaveValue('{{ vault "op" "Agents/Grafana/password" }}');
  await expect(vs(page, 'bot_token').locator('.vs-ref')).toHaveText('{{ vault "op" "Agents/Grafana/password" }}');
  // a non-sensitive field may come from a setting
  await src(page, 'default_channel', 'setting');
  await lookup(page, '#f-default_channel-pick', 'review', 'settings.review_channel');
  await expect(hidden(page, 'default_channel')).toHaveValue('${settings.review_channel}');
  await expect(vs(page, 'default_channel').locator('.vs-err')).toHaveCount(0);
});

test('a mixed-source login: typed username, password and TOTP from the same 1Password item', async ({ page }) => {
  await toCreds(page);
  await expect(vs(page, 'username').locator('[data-act="vs-src"]')).toHaveCount(2); // typed (stored in the vault) or vault
  await page.fill('#la-username-in', 'svc-mixed-77');
  await src(page, 'password', 'vault');
  await vs(page, 'password').locator('[data-act="vs-vault"]').click();
  await page.locator('.cdrawer [data-vb*="Agents/App/password"]').click();
  await src(page, 'otp', 'vault');
  await vs(page, 'otp').locator('[data-act="vs-same"]', { hasText: 'App · one-time password' }).click();
  await expect(hidden(page, 'otp')).toHaveValue('{{ vault "op" "Agents/App/one-time password?attribute=otp" }}');
  await expect(vs(page, 'otp').locator('.vs-st.ok')).toBeVisible();
  await page.locator('[data-act="la-creds"]').click();
  await page.fill('#f-name', 'app2');
  await page.fill('#f-origin', 'https://app.example.com');
  await page.locator('#la-bind [data-listk="identities"] select').selectOption('bot:platform-sre');
  await page.locator('[data-act="la-save"]').click();
  await page.locator('#su-go').click(); // the typed username goes to conductor's vault
  await expect(page.locator('.diff')).toContainText('name: app2');
  await expect(page.locator('.diff')).toContainText('key: Agents/App/password');
  await expect(page.locator('.diff')).toContainText('attribute=otp');
  await expect(page.locator('.diff')).toContainText('key: login-app2-username');
  await applyStaged(page);
  await go(page, '#/browser/logins');
  await page.locator('[data-act="login-more"][data-n="app2"]').click();
  await page.locator('#pop-menu [data-act="login-uses"]').click();
  await expect(page.locator('[data-login="app2"]')).toContainText('{{ vault "op" "Agents/App/password" }}');
  expect(await leaked(page, 'svc-mixed-77')).toEqual([]);
});

test('resolve check: resolves, does not resolve (with the reason), retired op://, plain-text env warning', async ({ page }) => {
  await slackForm(page);
  await lookup(page, '#f-bot_token-pick', 'pd', 'conductor/pd');
  await expect(vs(page, 'bot_token').locator('.vs-st.ok')).toContainText('exists');
  await vs(page, 'bot_token').locator('[data-act="vs-vault"]').click();
  await page.locator('.cdrawer [data-act="vb-vault"][data-v="hcv"]').click();
  await expect(page.locator('.cdrawer')).toContainText('unreachable');
  await page.locator('.cdrawer [data-close]').click();
  // an unmigrated op:// reference fails loudly
  const st = await page.evaluate(() => UI.VS.resolve('op://Agents/Slack/bot-token'));
  expect(st.state).toBe('fail'); expect(st.why).toMatch(/replaced by \{\{ vault "op"/);
  // a conductor vault entry that doesn't exist
  expect((await page.evaluate(() => UI.VS.resolve('{{ vault "house" "nope" }}'))).why).toMatch(/no entry "nope"/);
  // sensitive field from a plain-text conductor.env var: a warning
  await src(page, 'bot_token', 'env');
  await lookup(page, '#f-bot_token-pick', 'APP_PASS', 'env.APP_PASSWORD');
  await expect(vs(page, 'bot_token').locator('.vs-warn')).toContainText('plain text in conductor.env');
});

test('inline create from the env-var and vault pickers', async ({ page }) => {
  await slackForm(page);
  await src(page, 'bot_token', 'env');
  await lookupNew(page, '#f-bot_token-pick');
  await expect(page.locator('.cdrawer')).toContainText('Add an env var');
  await page.fill('#d-name', 'SLACK_TOKEN');
  await lookup(page, '#d-value-pick', 'slack bot', 'Agents/Slack/bot-token');
  await page.locator('#dr-submit').click();
  await expect(hidden(page, 'bot_token')).toHaveValue('${env.SLACK_TOKEN}');
  await expect(vs(page, 'bot_token').locator('.vs-st.ok')).toBeVisible();
  await lookupNew(page, '#f-app_token-pick');
  await page.fill('#d-key', 'slack-app-token'); await page.fill('#d-value', 'xapp-not-a-real-token');
  await page.locator('#dr-submit').click(); await page.locator('#su-go').click();
  await expect(hidden(page, 'app_token')).toHaveValue('{{ vault "house" "slack-app-token" }}');
  await page.fill('#f-name', 'slack-two');
  await page.locator('#gen-submit').click();
  await expect(page.locator('.panel', { hasText: 'conductor.env' }).first()).toContainText('SLACK_TOKEN');
  await expect(page.locator('.diff').first()).toContainText('slack-app-token');
  expect(await leaked(page, 'xapp-not-a-real-token')).toEqual([]);
});

test('a sensitive field can not come from a setting: blocked with an explanation', async ({ page }) => {
  await slackForm(page);
  await src(page, 'bot_token', 'setting');
  await lookup(page, '#f-bot_token-pick', 'org', 'settings.org');
  await expect(vs(page, 'bot_token').locator('.vs-err')).toContainText('Blocked');
  await page.fill('#f-name', 'slack-x');
  await page.locator('#gen-submit').click();
  await expect(page.locator('[data-field="bot_token"].bad')).toBeVisible();
});

test('the same component in a workflow step input', async ({ page }) => {
  await go(page, '#/workflows', { fresh: true });
  await page.locator('[data-act="wf-new"]').click();
  await page.fill('#f-name', 'notify'); await page.locator('#gen-submit').click();
  await page.locator('.spine-add .plus[data-loc="steps"][data-i="0"]').click();
  await page.fill('#pk-q', 'post'); await page.locator('.pk-item[data-key="verb:slack-ops.post"]').click();
  const ch = page.locator('[data-wf-path="options.channel"] .vs');
  await expect(ch.locator('.vs-srcbtn')).toBeVisible();
  await ch.locator('.vs-srcbtn').click();
  await ch.locator('[data-act="vs-src"][data-v="setting"]').click();
  await ch.locator('.vs-q').fill('review'); await ch.locator('[data-act="vs-choose"][data-ref*="settings.review_channel"]').click();
  await expect.poll(() => page.evaluate(() => document.querySelector('#wf-yaml .ced-ta').value)).toContain('channel: "${settings.review_channel}"');
});

test('a typed token never renders after save: not in the page, the state, the diff or the config', async ({ page }) => {
  await slackForm(page);
  await page.fill('#f-name', 'slack-two');
  await src(page, 'bot_token', 'typed');
  await page.fill('#f-bot_token-in', PASSWORD);
  await lookup(page, '#f-app_token-pick', 'slack app', 'Agents/Slack/app-token');
  await page.locator('#gen-submit').click();
  await page.locator('#su-go').click(); // the typed value goes to the vault, write-only
  await expect(page.locator('.diff')).toContainText('slack-two:');
  await expect(page.locator('.diff')).toContainText('slack-two-bot-token');
  expect(await leaked(page, PASSWORD)).toEqual([]);
  await applyStaged(page);
  await go(page, '#/config/raw');
  expect(await leaked(page, PASSWORD)).toEqual([]);
});

test('a reference is a lookup: type to search, pick with the keyboard, a chip with ✕, whether it exists and when it changed, never the value', async ({ page }) => {
  await slackForm(page);
  const inp = page.locator('#f-bot_token-pick'), list = page.locator('#f-bot_token-list');
  await inp.click(); await inp.fill('grafana pass');
  const first = list.locator('[data-act="vs-choose"]').first();
  await expect(first).toContainText('Grafana');
  await expect(first).toContainText('password');
  await expect(first).toContainText('exists');
  await page.keyboard.press('ArrowDown'); await page.keyboard.press('Enter');
  await expect(hidden(page, 'bot_token')).toHaveValue('{{ vault "op" "Agents/Grafana/password" }}');
  const chip = vs(page, 'bot_token').locator('.vs-chosen');
  await expect(chip).toContainText('Grafana'); await expect(chip).toContainText('password');
  await chip.locator('[data-act="vs-clear"]').click();
  await expect(hidden(page, 'bot_token')).toHaveValue('');
  await expect(page.locator('#f-bot_token-pick')).toBeFocused();
  // a vault that can't be reached is flagged in the list; Esc closes it
  await page.locator('#f-bot_token-pick').fill('hcv');
  await expect(list).toContainText('can’t be reached');
  await page.keyboard.press('Escape');
  await expect(list).toBeHidden();
  // a reference to something that isn't there is flagged inline
  await page.evaluate(() => UI.VS.setVal(document.querySelector('.vs[data-vs="bot_token"]'), '{{ vault "house" "gone-away" }}'));
  await expect(vs(page, 'bot_token').locator('.vs-st.fail')).toContainText('can’t be found');
  await expect(vs(page, 'bot_token').locator('.vs-chosen.bad')).toBeVisible();
  // Browse… is the same lookup, larger, and never shows a value
  await vs(page, 'bot_token').locator('[data-act="vs-clear"]').click();
  await vs(page, 'bot_token').locator('[data-act="vs-vault"]').click();
  await page.fill('#vb-q', 'household cvc');
  await expect(page.locator('.cdrawer [data-act="vs-choose"]')).toHaveCount(1);
  await page.locator('.cdrawer [data-act="vs-choose"]').click();
  await expect(hidden(page, 'bot_token')).toHaveValue('{{ vault "op" "Cards/Household/cvc" }}');
});
