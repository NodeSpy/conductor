// Secrets & vars → Env: variables by scope with where they're used, plain-text secrets flagged and moved into
// the vault in one step, import that never writes a secret in plain text, and the effective environment of a
// step or worker (names and sources only). (Riley M6)
const { test, expect, go, headAction } = require('./fixtures');

test('the Env tab: scopes, uses, a plain-text secret moved into the vault', async ({ page }) => {
  await go(page, '#/secrets', { fresh: true });
  await page.locator('.tabs a[href="#/secrets/env"]').click();
  await expect(page).toHaveURL(/#\/secrets\/env$/);
  await expect(page.locator('tr[data-env="runtime:paseo:AWS_PROFILE"]')).toContainText('runtime paseo');
  const pw = page.locator('tr[data-env="daemon:APP_PASSWORD"]');
  await expect(pw).toContainText('plain text');
  await expect(pw).not.toContainText('svc');
  await page.locator('[data-act="env-to-vault"][data-k="daemon:APP_PASSWORD"]').click();
  await page.locator('#cf-ok').click();
  await page.locator('#su-go').click(); // the vault write is signed
  await expect(page).toHaveURL(/#\/config\/apply/);
  expect(await page.evaluate(() => D.pending.find((c) => c.key === 'daemon:APP_PASSWORD').after.value)).toBe('{{ vault "house" "env-app-password" }}');
  expect(await page.evaluate(() => D.secrets.some((s) => s.ref === 'house/env-app-password'))).toBe(true);
});

test('effective environment: the narrowest scope wins, and the sandbox passes only allowed daemon vars', async ({ page }) => {
  await go(page, '#/secrets/env', { fresh: true });
  const eff = page.locator('#env-preview');
  await expect(eff.locator('tr', { hasText: 'NODE_OPTIONS' })).toContainText('workflow ci-fixer');
  await expect(eff.locator('tr', { hasText: 'APP_USER' })).toContainText('scrubbed environment'); // a sandboxed launch never gets the daemon's own env (Isolation.md)
  await page.locator('[data-act="env-sbx"]').click();
  await expect(eff.locator('tr', { hasText: 'APP_USER' })).not.toContainText('scrubbed environment'); // a sandboxed launch never gets the daemon's own env (Isolation.md)
  await expect(eff).toContainText('values are never shown');
  // the same view on a run's agent step
  await go(page, '#/runs/r-7ef0');
  await page.locator('.dl-tree .tl[data-step="fix"]').click();
  await page.locator('#run-step details.env-tech summary').click();
  await expect(page.locator('#run-step details.env-tech')).toContainText('NODE_OPTIONS');
});

test('import: secret-looking values go to the vault, never into the file', async ({ page }) => {
  await go(page, '#/secrets/env', { fresh: true });
  await headAction(page, '[data-act="env-import"]');
  await page.locator('#f-scope').selectOption('daemon');
  await page.fill('#f-text', 'REGION=eu-west-1\nDEPLOY_TOKEN=tok-not-a-real-one-9911\n');
  await page.locator('#gen-submit').click();
  await page.locator('#su-go').click();
  await expect(page).toHaveURL(/#\/config\/apply/);
  const staged = await page.evaluate(() => D.pending.filter((c) => c.coll === 'env').map((c) => [c.key, c.after.value]));
  expect(staged).toEqual(expect.arrayContaining([['daemon:REGION', 'eu-west-1'], ['daemon:DEPLOY_TOKEN', '{{ vault "house" "env-deploy-token" }}']]));
  const hay = await page.evaluate(() => document.documentElement.outerHTML + JSON.stringify(D) + localStorage.getItem('cd-proto-state-v5'));
  expect(hay).not.toContain('tok-not-a-real-one-9911');
});
