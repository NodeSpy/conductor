// Create / edit / delete flows, each ending in the config diff → passkey →
// apply pipeline (or, for vault writes and ledger operations, the immediate
// step-up path the design specifies). Run at desktop and phone sizes.
const { test, expect, go, applyStaged, stagedCount, headAction } = require('./fixtures');

const submit = (page) => page.locator('#gen-submit').click();
const state = (page, fn, arg) => page.evaluate(fn, arg);

test.describe('connectors, runtimes, hosts, isolation', () => {
  test('add a connector with its plugin-declared settings form, then apply', async ({ page }) => {
    await go(page, '#/connectors/connectors');
    await page.locator('[data-act="conn-add"]').click();
    await page.locator('[data-act="conn-pick"][data-u="slack"]').click();
    await expect(page.locator('.modal h2')).toHaveText('Add a Slack connector');
    // required fields come from the plugin's declaration
    await submit(page);
    await expect(page.locator('[data-field="name"].bad')).toBeVisible();
    await expect(page.locator('[data-field="bot_token"]')).toBeVisible();
    await page.fill('#f-name', 'slack-eng');
    await page.selectOption('#f-bot_token-pick', { index: 2 });
    await page.selectOption('#f-app_token-pick', { index: 3 });
    await page.fill('#f-default_channel-in', '#eng');
    await submit(page);
    await expect(page.locator('.diff')).toContainText('slack-eng:');
    await applyStaged(page);
    await go(page, '#/connectors/connectors');
    await expect(page.locator('.bot-card', { hasText: 'slack-eng' })).toContainText('use: slack');
  });
  test('edit a connector and delete one', async ({ page }) => {
    await go(page, '#/connectors/connectors');
    await page.locator('[data-act="conn-edit"][data-c="gh"]').click();
    await page.selectOption('#f-sweep', '5m');
    await submit(page);
    await expect(page.locator('.diff')).toContainText('sweep: 5m');
    await applyStaged(page);
    expect(await state(page, () => D.connectors.find((c) => c.name === 'gh').cfg.sweep)).toBe('5m');
    await go(page, '#/connectors/connectors');
    await page.locator('[data-act="conn-del"][data-c="tf-apply"]').click();
    await page.locator('#cf-ok').click();
    await applyStaged(page);
    expect(await state(page, () => D.connectors.some((c) => c.name === 'tf-apply'))).toBe(false);
  });
  test('runtime add / edit / delete, unsandboxed runtime is a widening', async ({ page }) => {
    await go(page, '#/connectors/runtimes');
    await page.locator('[data-act="rt-add"]').click();
    await page.fill('#f-name', 'gemini');
    await page.selectOption('#f-use', 'acp');
    await page.fill('[id="f-agent_acp"]', 'gemini');
    await submit(page);
    await applyStaged(page);
    await go(page, '#/connectors/runtimes');
    await expect(page.locator('tr', { hasText: 'gemini' })).toBeVisible();
    await page.locator('[data-act="rt-edit"][data-r="oc"]').click();
    await page.selectOption('[id="f-isolation_mode_opencode"]', '');
    await submit(page);
    await expect(page.locator('.tag', { hasText: 'widens: isolation: removed from the runtime' })).toBeVisible();
    await applyStaged(page, { expectWidening: true });
    await go(page, '#/connectors/runtimes');
    await page.locator('[data-act="rt-del"][data-r="deck"]').click();
    await page.locator('#cf-ok').click();
    await applyStaged(page);
    expect(await state(page, () => D.runtimes.map((r) => r.name))).not.toContain('deck');
  });
  test('hosts: add, edit, test; deleting a host in use is refused by validation', async ({ page }) => {
    await go(page, '#/connectors/hosts');
    await page.locator('[data-act="host-add"]').click();
    await page.fill('#f-name', 'lab-box'); await page.fill('#f-addr', 'lab-box.tail0000.ts.net'); await page.fill('#f-user', 'conductor');
    await submit(page);
    await applyStaged(page);
    await go(page, '#/connectors/hosts');
    await page.locator('[data-act="host-test"][data-h="lab-box"]').click();
    await expect(page.locator('tr', { hasText: 'lab-box' })).toContainText('reachable');
    await page.locator('[data-act="host-del"][data-h="gpu-box"]').click();
    await page.locator('#cf-ok').click();
    await expect(page.locator('#chk-state')).toContainText('failed');
    await expect(page.locator('.callout.danger')).toContainText('runtimes.gpu-box.host');
    await expect(page.locator('[data-act="apply-go"]')).toHaveAttribute('aria-disabled', 'true');
    await page.locator('[data-act="discard-all"]').click(); await page.locator('#cf-ok').click();
    expect(await stagedCount(page)).toBe(0);
  });
  test('isolation: egress allowlist and backend, acknowledged widening', async ({ page }) => {
    await go(page, '#/connectors/isolation');
    await page.fill('#sb-eg-new', 'api.example.com:443');
    await page.locator('[data-act="sb-eg-add"]').click();
    await page.locator('[data-act="sb-backend"][data-v="container"]').click();
    await page.locator('a', { hasText: 'Review change' }).click();
    await expect(page.locator('.diff')).toContainText('api.example.com:443');
    await applyStaged(page, { expectWidening: true });
    expect(await state(page, () => D.sandboxCfg)).toMatchObject({ backend: 'container', egress: expect.arrayContaining(['api.example.com:443']) });
  });
});

test.describe('bots', () => {
  test('edit a bot definition (proactive, team, sandbox) through the pipeline', async ({ page }) => {
    await go(page, '#/bots/platform-sre/settings');
    await page.fill('[id="f-df_proactive_every"]', '2h');
    await page.locator('[id="f-df_proactive_every"]').blur();
    await expect(page.locator('#df-yaml')).toContainText('every: 2h');
    await page.locator('[data-act="df-review"]').click();
    await expect(page.locator('.diff')).toContainText('every: 2h');
    await applyStaged(page);
    expect(await state(page, () => D.bots[0].cfg.proactive.every)).toBe('2h');
  });
  test('define a new bot, in shadow mode, with charter and scope', async ({ page }) => {
    await go(page, '#/bots/new/settings');
    await page.fill('[id="f-df__name"]', 'docs-steward');
    await page.fill('[id="f-df_description"]', 'Keeps the API docs honest');
    await page.locator('[id="f-df_description"]').blur();
    await page.fill('#df-charter', 'Review docs PRs; never merge.');
    await page.locator('#df-charter').blur();
    await page.locator('[data-act="df-review"]').click();
    await expect(page.locator('.diff')).toContainText('docs-steward:');
    await expect(page.locator('.diff')).toContainText('shadow: true');
    await applyStaged(page);
    await go(page, '#/bots');
    await expect(page.locator('#ftree')).toContainText('docs-steward');
    expect(await page.evaluate(() => D.bots.find((b) => b.name === 'docs-steward').cfg.autonomy.shadow)).toBe(true);
  });
  test('access: tier change and promotion are widening config edits; add verb; bind and unbind a credential', async ({ page }) => {
    await go(page, '#/bots/platform-sre/access');
    await page.selectOption('select[data-v="kv.set"]', 'reversible');
    await page.locator('[data-act="acc-addverb"]').click();
    await page.selectOption('#f-verb', 'gh.label'); await page.selectOption('#f-tier', 'propose');
    await submit(page);
    await page.locator('[data-act="cred-bind"]').first().click();
    await page.selectOption('#f-cred', 'app');
    await submit(page);
    // the team caps credential scope: a login outside it is refused on validation
    await expect(page.locator('.callout.danger').first()).toContainText("outside team platform's credential scope");
    await expect(page.locator('[data-act="apply-go"]')).toHaveAttribute('aria-disabled', 'true');
    await page.locator('.list .item', { hasText: 'let platform-sre use login app' }).locator('[data-act="unstage"]').click();
    await expect(page.locator('.tag', { hasText: 'loosens kv.set' })).toBeVisible();
    await applyStaged(page, { expectWidening: true });
    const m = await state(page, () => D.bots[0].cfg);
    expect(m.autonomy.tiers.reversible).toContain('kv.set');
    expect(m.skill.verbs).toContain('gh.label');
    await go(page, '#/bots/platform-sre/access');
    await page.locator('[data-act="cred-unbind"][data-c="grafana"]').click();
    await page.locator('#cf-ok').click();
    await applyStaged(page);
    expect(await state(page, () => D.access.credentials.some((c) => c.name === 'grafana'))).toBe(false);
  });
  test('ledger operations are immediate: deliver, note, close, revoke a login, memory commit, stop a worker', async ({ page }) => {
    await go(page, '#/bots/platform-sre/cases');
    await headAction(page, '[data-act="m-deliver"]');
    await page.fill('#f-title', 'Check the NAT gateway bill'); await submit(page);
    await expect(page.locator('.route-box')).toContainText('delivered to');
    await page.fill('#case-note', 'Owner is billing.'); await page.locator('[data-act="case-note"]').click();
    await expect(page.locator('.panel', { hasText: 'Notes' })).toContainText('Owner is billing.');
    await page.locator('[data-act="case-close"]').click();
    await expect(page.locator('.panel-head .st').first()).toContainText('closed');
    await go(page, '#/bots/platform-sre/access');
    await page.locator('[data-act="login-revoke"]').click(); await page.locator('#cf-ok').click();
    await expect(page.locator('.panel', { hasText: 'Browser logins' })).toContainText('No saved logins');
    await go(page, '#/bots/platform-sre/memory');
    await page.locator('#rp-ed .ced-ta').fill('# db-prod-1\n\nPgbouncer pool: 200.\n');
    await page.locator('[data-act="rp-save"]').click();
    await expect(page.locator('#rp-prev')).toContainText('Pgbouncer pool: 200.');
    await go(page, '#/bots/platform-sre/workers');
    await page.locator('[data-act="w-stop"]').first().click(); await page.locator('#cf-ok').click();
    await expect(page.locator('.wcard', { hasText: 'w41' })).toContainText('stopped');
  });
  test('approve a proposal with the passkey seal', async ({ page }) => {
    await go(page, '#/bots/platform-sre/proposals/p12');
    await page.locator('[data-act="p-approve"][data-d="approve"]').click();
    await page.locator('#seal-go').click();
    await expect(page.locator('.prop-head .st').first()).toContainText('approved');
    expect(await state(page, () => D.proposals.find((p) => p.id === 'p12').status)).toBe('approved');
  });
});

test.describe('secrets and vars', () => {
  test('store a secret (write-only, step-up), replace it, repoint its uses, fix a vault, edit settings', async ({ page }) => {
    await go(page, '#/secrets');
    await page.locator('[data-act="secret-add"]').click();
    await page.fill('#f-key', 'sentry-dsn'); await page.fill('#f-alias', 'SENTRY_DSN'); await page.fill('#f-value', 'https://abc@o0.ingest.example/1');
    await submit(page);
    await page.locator('#su-go').click();
    await expect(page.locator('#sec-body tr[data-secret="house/sentry-dsn"]')).toContainText('{{ vault "house" "sentry-dsn" }}');
    await expect(page.locator('#sec-body tr[data-secret="house/sentry-dsn"]')).toContainText('dashboard only');
    expect(await state(page, () => JSON.stringify(D))).not.toContain('abc@o0.ingest');
    await page.locator('[data-act="secret-set"][data-s="house/sentry-dsn"]').click();
    await page.fill('#f-value', 'rotated'); await submit(page); await page.locator('#su-go').click();
    // no op:// anywhere: one reference syntax
    expect(await page.locator('#sec-body').textContent()).not.toContain('op://');
    // filters
    await page.locator('[data-act="sec-f"][data-v="unused"]').click();
    await expect(page.locator('#sec-body tr[data-secret="house/sentry-dsn"]')).toBeVisible();
    await page.locator('[data-act="sec-f"][data-v="all"]').click();
    await page.fill('[data-on-input="sec-q"]', 'slack');
    await expect(page.locator('#sec-body tr')).toHaveCount(2);
    await page.locator('[data-act="secret-ref"][data-s="op/Agents/Slack/bot-token"]').click();
    await page.fill('#f-ref', 'op/Agents/Slack/bot-token-2'); await submit(page);
    await expect(page.locator('.diff').first()).toContainText('{{ vault \\"op\\" \\"Agents/Slack/bot-token-2\\" }}');
    await page.locator('[data-act="discard-all"]').click(); await page.locator('#cf-ok').click();
    await go(page, '#/secrets');
    await page.locator('.callout.danger [data-act="vault-edit"]').click();
    await page.fill('#f-hashicorp__unlock_token', 'env:VAULT_TOKEN_2026');
    await submit(page);
    await applyStaged(page);
    expect(await state(page, () => D.vaults.find((v) => v.name === 'hcv').status)).toBe('ok');
    await go(page, '#/secrets');
    await page.fill('input[data-k="review_channel"]', '#reviews'); await page.locator('input[data-k="review_channel"]').press('Enter');
    await page.locator('[data-act="setting-add"]').click();
    await page.fill('#f-name', 'oncall_channel'); await page.fill('#f-value', '#oncall'); await submit(page);
    await page.locator('a', { hasText: /^Review/ }).click();
    await expect(page.locator('.diff')).toContainText('oncall_channel');
    await applyStaged(page);
    expect(await state(page, () => D.settings.find((s) => s.name === 'review_channel').value)).toBe('#reviews');
  });
});

test.describe('browser', () => {
  test('identity settings widen through the pipeline; seed a login; take over and release', async ({ page }) => {
    await go(page, '#/browser');
    await page.locator('[data-act="ident-edit"][data-i="agent:docs@acme/web"]').click();
    await page.locator('[data-listk="allow"] .list-add').fill('status.example.com');
    await page.locator('[data-listk="allow"] .list-add').press('Enter');
    await submit(page);
    await expect(page.locator('.tag', { hasText: 'allows site status.example.com' })).toBeVisible();
    await applyStaged(page, { expectWidening: true });
    await go(page, '#/browser/takeover');
    await page.locator('[data-act="sd-next"]').click();
    await page.locator('[data-act="sd-next"]').click();
    await page.locator('[data-act="sd-save"]').click(); await page.locator('#su-go').click();
    await expect(page.locator('.empty')).toContainText('Login saved');
    await go(page, '#/browser/b-02');
    await page.locator('[data-act="b-take"]').click(); await page.locator('#su-go').click();
    await expect(page.locator('#banner')).toBeVisible();
    await page.locator('[data-act="b-nav"][data-k="reload"]').click();
    await page.locator('[data-act="b-take"]').click();
    await expect(page.locator('#banner')).toHaveCount(0);
  });
});

test.describe('packs and plugins', () => {
  test('hold, update, overrides, remove, add from a source', async ({ page }) => {
    await go(page, '#/extensions/packs');
    await page.locator('[data-act="pack-hold"][data-p="kit"]').click();
    await page.locator('[data-act="pack-plan"][data-p="review"][data-update="1"]').click();
    await page.locator('#pp-go').click();
    await expect(page.locator('.diff').first()).toContainText('pr-review-team@1.5.0');
    await applyStaged(page);
    expect(await state(page, () => D.packs.find((p) => p.name === 'review').ver)).toBe('1.5.0');
    await go(page, '#/extensions/packs');
    await page.locator('[data-act="pack-ov"][data-p="sre-bot"]').click();
    // overrides are YAML; a step the pack doesn't have is refused with the names it does have
    const y = page.locator('#f-yaml');
    await expect(y).toHaveValue(/sweep\/check/);
    await y.fill('steps:\n  sweep/check: { model: light }\n  sweep/nope: { model: summarizer }\n');
    await submit(page);
    await expect(page.locator('[data-field="yaml"] .ferr')).toContainText('the pack has no step by that name');
    await y.fill('steps:\n  sweep/check: { model: light }\n  sweep/report: { model: summarizer }\n');
    await submit(page);
    await expect(page.locator('.diff')).toContainText('sweep/report');
    await go(page, '#/extensions/packs');
    await page.locator('[data-act="pack-del"][data-p="house-style"]').click(); await page.locator('#cf-ok').click();
    await page.goto('/index.html#/extensions/packs');
    await page.locator('[data-act="pack-add"]').click();
    await page.fill('#f-name', 'hygiene'); await page.fill('#f-use', 'conductor-packs/dep-hygiene@^2'); await submit(page);
    await expect(page.locator('.list .item')).toHaveCount(3);
    await applyStaged(page);
    const names = await state(page, () => D.packs.map((p) => p.name));
    expect(names).toContain('hygiene'); expect(names).not.toContain('house-style');
  });
  test('plugins: update with step-up, install from browse into a new connector', async ({ page }) => {
    await go(page, '#/extensions/plugins');
    await page.locator('[data-act="plugin-update"][data-p="slack"]').click(); await page.locator('#su-go').click();
    await expect(page.locator('tr', { hasText: 'slack' })).toContainText('v1.7.0');
    await go(page, '#/extensions/browse');
    const card = page.locator(`[data-cat="connector:aws-cost"][data-origin="official"]`);
    await card.locator("a.btn.primary").click();
    await expect(page).toHaveURL(/#\/extensions\/install\/connector\/aws-cost$/);
    await page.fill('#f-inst', 'aws'); await page.fill('#f-set__role_arn', 'arn:aws:iam::000000000000:role/cost-read');
    await page.locator('[data-act="iw-review"]').click();
    await applyStaged(page);
    expect(await state(page, () => D.connectors.some((c) => c.name === 'aws' && c.use === 'aws-cost'))).toBe(true);
  });
});

test.describe('updates, config history, raw editor, access', () => {
  test('update policy edits stage; apply an update now', async ({ page }) => {
    await go(page, '#/updates');
    await page.locator('[data-act="upd-pol"][data-k="deps"]').click();
    await page.fill('#upd-int', '30m'); await page.locator('#upd-int').press('Enter');
    await page.locator('a', { hasText: 'Review policy change' }).click();
    await expect(page.locator('.diff')).toContainText('interval: 30m');
    await applyStaged(page);
    await go(page, '#/updates');
    await page.locator('[data-act="upd-apply"]').click(); await page.locator('#su-go').click();
    await expect(page.locator('.page-head')).toContainText('up to date (v0.72.0)');
  });
  test('revert one change keeps the later ones', async ({ page }) => {
    await go(page, '#/config');
    await page.locator('[data-act="revert-one"][data-id="cfg-20261015-1412"]').click();
    await expect(page).toHaveURL(/#\/config\/apply/);
    expect(await page.evaluate(() => D.pending.map((c) => c.key))).toEqual(['platform-sre']);
    await applyStaged(page);
    expect(await state(page, () => [D.bots[0].cfg.proactive.every, /max_cost_usd: 2\.00/.test(D.workflows.find((w) => w.name === 'ci-fixer').yaml)])).toEqual(['6h', true]);
  });
  test('restore a backup runs the same pipeline; raw YAML edits become node changes', async ({ page }) => {
    await go(page, '#/config');
    await page.locator('[data-act="restore"][data-id="cfg-20261015-1412"]').click();
    await expect(page.locator('.modal')).toContainText('also undoes 1 later change'); // and names it
    await expect(page.locator('.modal')).toContainText('cfg-20261016-0912');
    await page.locator('#cf-ok').click();
    await expect(page.locator('.page-head h1')).toContainText('Restore a backup');
    await applyStaged(page);
    expect(await state(page, () => D.bots[0].cfg.proactive.every)).toBe('6h');
    await go(page, '#/config/raw');
    const ta = page.locator('#raw-ed .ced-ta');
    const text = await ta.inputValue();
    await ta.evaluate((el, v) => { el.value = v; el.dispatchEvent(new Event('input', { bubbles: true })); }, text.replace('org: acme', 'org: acme-corp'));
    await page.locator('[data-act="raw-review"]:not([aria-disabled="true"])').click();
    await expect(page.locator('.diff')).toContainText('org: acme-corp');
    await applyStaged(page);
    expect(await state(page, () => D.settings.find((s) => s.name === 'org').value)).toBe('acme-corp');
  });
  test('access: device-bound policy, revoke a passkey, end a session, enroll', async ({ page }) => {
    await go(page, '#/security');
    await page.locator('[data-act="op-bound"]').click();
    await page.locator('a', { hasText: 'Review change' }).click();
    await applyStaged(page);
    await go(page, '#/security');
    await page.locator('[data-act="pk-revoke"]').last().click(); await page.locator('#su-go').click();
    await page.locator('[data-act="sess-end"]').click();
    expect(await state(page, () => [D.passkeys.length, D.sessions.length])).toEqual([2, 1]);
    await go(page, '#/enroll');
    await page.locator('[data-act="enroll"]').click();
    await expect(page.locator('#en-code-f .ferr')).toContainText('pairing code');
    await page.fill('#en-code', '417-209'); await page.locator('[data-act="enroll"]').click();
    await expect(page.locator('.modal')).toContainText('Confirm with an existing passkey');
    await page.locator('#pk-go').click();
    await expect(page).toHaveURL(/#\/security/);
    expect(await state(page, () => D.passkeys.length)).toBe(3);
  });
  test('state persists across navigation and reloads; reset demo data restores the seed', async ({ page }) => {
    await go(page, '#/overview');
    await page.locator('[data-act="pause"]').click();
    await page.reload();
    await expect(page.locator('[data-act="pause"]')).toContainText('Resume dispatch');
    await go(page, '#/more');
    await page.locator('#main [data-act="reset-demo"]').click(); await page.locator('#cf-ok').click();
    await go(page, '#/overview');
    await expect(page.locator('[data-act="pause"]')).toContainText('Pause dispatch');
  });
});
