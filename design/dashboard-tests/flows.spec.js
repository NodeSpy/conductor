// Create / edit / delete flows, each ending in the config diff → passkey →
// apply pipeline (or, for vault writes and ledger operations, the immediate
// step-up path the design specifies). Run at desktop and phone sizes.
const { test, expect, go, applyStaged, stagedCount, headAction, isPhone } = require('./fixtures');

const submit = (page) => page.locator('#gen-submit').click();
const state = (page, fn, arg) => page.evaluate(fn, arg);

test.describe('connectors, runtimes, hosts, isolation', () => {
  test('add a connector with its plugin-declared settings form, then apply', async ({ page }) => {
    await go(page, '#/connectors/connectors');
    await page.locator('[data-act="conn-add"]').click();
    await page.locator('[data-act="cat-pick"][data-kind="connector"][data-name="slack"]').click();
    await expect(page.locator('.modal h2')).toHaveText('Add a Slack connector');
    // required fields come from the plugin's declaration (the instance name is prefilled; empty it to see it's required)
    await page.fill('#f-name', '');
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
    await expect(page.locator('.conn-row', { hasText: 'slack-eng' })).toContainText('Slack');
  });
  test('edit a connector and delete one', async ({ page }) => {
    await go(page, '#/connectors/connectors');
    await page.locator('[data-act="conn-more"][data-c="gh"]').click(); await page.locator('#pop-menu [data-act="conn-edit"]').click();
    await page.selectOption('#f-sweep', '5m');
    await submit(page);
    await expect(page.locator('.diff')).toContainText('sweep: 5m');
    await applyStaged(page);
    expect(await state(page, () => D.connectors.find((c) => c.name === 'gh').cfg.sweep)).toBe('5m');
    await go(page, '#/connectors/connectors');
    await page.locator('[data-act="conn-more"][data-c="tf-apply"]').click();
    await page.locator('.menu [data-act="conn-del"][data-c="tf-apply"]').click();
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
  test('Safety: allowed sites and the kind of sandbox, an acknowledged widening', async ({ page }) => {
    await go(page, '#/connectors/isolation'); // the old link lands on Safety
    await expect(page).toHaveURL(/#\/safety$/);
    await page.fill('#sb-eg-new', 'api.example.com:443');
    await page.locator('[data-act="sb-eg-add"]').click();
    await page.locator('input[name="sb-kind"][value="container"]').check();
    await page.locator('.sb-card a', { hasText: 'Review the change' }).click();
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
    // the plain list is the editor: move kv.set to "on its own" from its menu
    await page.locator('[data-act="can-more"][data-v="kv.set"]').click();
    await page.locator('#pop-menu [data-act="can-move"][data-mode="own"]').click();
    await page.locator('[data-act="acc-addverb"]').click();
    await page.selectOption('#f-verb', 'gh.label'); // asks first, the default
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
    await page.locator('[data-act="cred-more"][data-c="grafana"]').click();
    await page.locator('#pop-menu [data-act="cred-unbind"][data-c="grafana"]').click();
    await page.locator('#cf-ok').click();
    await applyStaged(page);
    expect(await state(page, () => D.access.credentials.some((c) => c.name === 'grafana'))).toBe(false);
  });
  test('ledger operations are immediate: deliver, note, close, revoke a login, memory commit, stop a worker', async ({ page }) => {
    await go(page, '#/bots/platform-sre/cases');
    await headAction(page, '[data-act="m-deliver"]');
    await page.fill('#f-title', 'Check the NAT gateway bill'); await submit(page);
    await expect(page.locator('.case-head')).toContainText(/delivered to/i);
    await page.fill('#case-note', 'Owner is billing.'); await page.locator('[data-act="case-note"]').click();
    await expect(page.locator('.panel', { hasText: 'Notes' })).toContainText('Owner is billing.');
    await page.locator('[data-act="case-close"]').click();
    await expect(page.locator('.case-state').first()).toContainText('Closed');
    await go(page, '#/bots/platform-sre/access');
    await page.locator('[data-act="login-revoke"]').click(); await page.locator('#cf-ok').click();
    await expect(page.locator('.panel', { hasText: 'Saved browser sessions' })).toContainText('No saved logins');
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
    await expect(page.locator('.dl-decided .st').first()).toContainText('Approved');
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
    await page.locator('[data-act="sec-open"][data-s="house/sentry-dsn"]').click(); // the reference is in the row's detail
    await expect(page.locator('#sec-body li[data-secret="house/sentry-dsn"]')).toContainText('{{ vault "house" "sentry-dsn" }}');
    await expect(page.locator('#sec-body li[data-secret="house/sentry-dsn"]')).toContainText('a label for this list');
    expect(await state(page, () => JSON.stringify(D))).not.toContain('abc@o0.ingest');
    await page.locator('[data-act="sec-more"][data-s="house/sentry-dsn"]').click();
    await page.locator('#pop-menu [data-act="secret-set"]').click();
    await page.fill('#f-value', 'rotated'); await submit(page); await page.locator('#su-go').click();
    // no op:// anywhere: one reference syntax
    expect(await page.locator('#sec-body').textContent()).not.toContain('op://');
    // filters
    await page.fill('.fb[data-fb="sec"] .fb-in', 'used:unused');
    await expect(page.locator('#sec-body li[data-secret="house/sentry-dsn"]')).toBeVisible();
    await page.fill('.fb[data-fb="sec"] .fb-in', 'slack');
    await expect(page.locator('#sec-body li[data-secret]')).toHaveCount(2);
    await page.locator('[data-act="sec-more"][data-s="op/Agents/Slack/bot-token"]').click();
    await page.locator('#pop-menu [data-act="secret-ref"]').click();
    await page.fill('#f-ref', 'op/Agents/Slack/bot-token-2'); await submit(page);
    await expect(page.locator('.diff').first()).toContainText('{{ vault \\"op\\" \\"Agents/Slack/bot-token-2\\" }}');
    await page.locator('[data-act="discard-all"]').click(); await page.locator('#cf-ok').click();
    await go(page, '#/secrets');
    await page.locator('.callout.danger a', { hasText: 'See the vault' }).click(); // the banner leads to the vault's card
    await page.locator('#vault-hcv [data-act="vault-edit"]').click();
    await page.fill('#f-hashicorp__unlock_token', 'env:VAULT_TOKEN_2026');
    await submit(page);
    await applyStaged(page);
    expect(await state(page, () => D.vaults.find((v) => v.name === 'hcv').status)).toBe('ok');
    await go(page, '#/secrets/settings');
    await page.locator('[data-act="setting-more"][data-s="settings.review_channel"]').click();
    await page.locator('#pop-menu [data-act="setting-change"]').click();
    await page.fill('#f-value', '#reviews'); await submit(page);
    await page.locator('[data-act="setting-add"]').first().click();
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
    await page.locator('.ident-d summary').click(); // per-identity settings are folded
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
    await page.locator('[data-act="pack-more"][data-p="kit"]').click();
    await page.locator('#pop-menu [data-act="pack-hold"]').click();
    await page.locator('[data-act="pack-plan"][data-p="review"][data-update="1"]').click();
    await page.locator('#pp-go').click();
    await expect(page.locator('.diff').first()).toContainText('pr-review-team@1.5.0');
    await applyStaged(page);
    expect(await state(page, () => D.packs.find((p) => p.name === 'review').ver)).toBe('1.5.0');
    await go(page, '#/extensions/packs');
    await page.locator('a[href="#/extensions/pack/sre-bot"]').first().click();
    // the guided overrides: the step override is listed, with a way back to the pack's default
    await expect(page.locator('.dl-card', { hasText: 'Your overrides' })).toContainText('sweep/check');
    // the same block as YAML: a step the pack doesn't have is refused with the names it does have
    await page.locator('.po-yaml summary').click();
    const y = page.locator('#po-y');
    await expect(y).toHaveValue(/sweep\/check/);
    await y.fill('steps:\n  sweep/check: { model: light }\n  sweep/nope: { model: summarizer }\n'); await y.blur();
    await expect(page.locator('#po-y-err')).toContainText('the pack has no step by that name');
    await page.locator('#po-y').fill('steps:\n  sweep/check: { model: light }\n  sweep/report: { model: summarizer }\n'); await page.locator('#po-y').blur();
    await page.locator('.dl-actions a', { hasText: 'Review the change' }).click();
    await expect(page.locator('.diff')).toContainText('sweep/report');
    await go(page, '#/extensions/packs');
    await page.locator('[data-act="pack-more"][data-p="house-style"]').click();
    await page.locator('#pop-menu [data-act="pack-del"]').click(); await page.locator('#cf-ok').click();
    // Add a pack opens the catalog; a source that isn't in it is the secondary path, read before it's added
    await page.goto('/index.html#/extensions/packs');
    await page.locator('.page-head a', { hasText: 'Add a pack' }).click();
    await expect(page).toHaveURL(/#\/extensions\/browse/);
    await page.locator('[data-act="pack-source"]').click();
    await page.fill('#ps-use', 'conductor-packs/dep-hygiene@^2'); await page.locator('#ps-use').blur();
    await page.locator('[data-act="ps-read"]').click();
    await expect(page.locator('#ps-inst')).toHaveValue('dep-hygiene'); // named from the pack
    await page.fill('#ps-inst', 'hygiene'); await page.locator('#ps-inst').blur();
    await page.locator('[data-act="ps-add"]').click();
    await expect(page.locator('.list .item')).toHaveCount(3);
    await applyStaged(page);
    const names = await state(page, () => D.packs.map((p) => p.name));
    expect(names).toContain('hygiene'); expect(names).not.toContain('house-style');
  });
  test('services: update one with step-up on Connections, install a new one into a new connection', async ({ page }) => {
    await go(page, '#/extensions/plugins'); // the old Plugins tab lands on Connections
    await expect(page).toHaveURL(/#\/connectors\/connectors/);
    await page.locator('.svc-head[data-svc="slack"] [data-act="plugin-update"]').click(); await page.locator('#su-go').click();
    await expect(page.locator('.svc-head[data-svc="slack"]')).toContainText('v1.7.0');
    await go(page, '#/extensions/install/connector/aws-cost');
    await expect(page.locator('.nav a[data-nav="connectors"].on')).toHaveCount(1); // a service's pages sit under Connections (the rail, hidden on a phone, still marks it)
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
    await page.locator('a', { hasText: 'Review the change' }).click();
    await expect(page.locator('.diff')).toContainText('interval: 30m');
    await applyStaged(page);
    await go(page, '#/updates');
    await page.locator('[data-act="upd-apply"]').click(); await page.locator('#cf-ok').click();
    await expect(page.locator('#main')).toContainText('You’re on v0.72.0, the newest release.');
  });
  test('revert one change keeps the later ones', async ({ page }) => {
    await go(page, '#/config');
    await page.locator('[data-act="cfg-h-more"][data-id="cfg-20261015-1412"]').click();
    await page.locator('#pop-menu [data-act="revert-one"]').click();
    await expect(page).toHaveURL(/#\/config\/apply/);
    expect(await page.evaluate(() => D.pending.map((c) => c.key))).toEqual(['platform-sre']);
    await applyStaged(page);
    expect(await state(page, () => [D.bots[0].cfg.proactive.every, /max_cost_usd: 2\.00/.test(D.workflows.find((w) => w.name === 'ci-fixer').yaml)])).toEqual(['6h', true]);
  });
  test('restore a backup runs the same pipeline; raw YAML edits become node changes', async ({ page }) => {
    await go(page, '#/config');
    await page.locator('[data-act="cfg-h-more"][data-id="cfg-20261015-1412"]').click();
    await page.locator('#pop-menu [data-act="restore"]').click();
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
    await page.locator('a', { hasText: 'Review the change' }).click();
    await applyStaged(page);
    await go(page, '#/security');
    await page.locator('[data-act="pk-revoke"]').last().click(); await page.locator('#cf-ok').click(); // removing access doesn't ask for a passkey
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
    // Pause all automation lives in System → Safety: it explains itself and asks first; while paused, Needs you says so
    await go(page, '#/safety');
    await page.locator('#pause [data-act="pause"]').click();
    await expect(page.locator('.modal')).toContainText('Nothing new starts until you resume');
    await page.locator('#cf-ok').click();
    await page.reload();
    await go(page, '#/attention');
    await expect(page.locator('.dl-needs')).toContainText('All automation is paused');

    await go(page, '#/more');
    await page.locator('#main [data-act="reset-demo"]').click(); await page.locator('#cf-ok').click();
    await go(page, '#/overview');
    await expect(page.locator('.dl-needs')).not.toContainText('All automation is paused');
    await go(page, '#/safety');
    await expect(page.locator('#pause [data-act="pause"]')).toContainText('Pause all automation');
  });
});
