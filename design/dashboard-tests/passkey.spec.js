// A passkey only for changes that widen what bots or agents can do (from the one widening classifier) and
// for approvals; everything else applies after the diff review with a plain Apply, still validated on a
// copy, backed up and rollback-able. The review says why a passkey is or isn't needed. (Sam M7, Daniel)
const { test, expect, go } = require('./fixtures');

async function toReview(page) {
  await go(page, '#/config/apply');
  await expect(page.locator('#chk-state')).toContainText('passed');
}

test('a non-widening change (a description) applies with a plain Apply, no passkey', async ({ page }) => {
  await go(page, '#/bots', { fresh: true });
  await page.evaluate(() => { const c = S.staged('bots', 'platform-sre'); c.description = 'SRE for platform, reworded'; S.stage('bots', 'platform-sre', c); });
  await toReview(page);
  await expect(page.locator('#pk-why')).toContainText('No passkey needed');
  await expect(page.locator('[data-act="apply-go"]')).toHaveText(/^\s*Apply\s*$/);
  await page.locator('[data-act="apply-go"]').click();
  await expect(page.locator('#su-go')).toHaveCount(0);
  await expect(page.locator('.callout.ember')).toContainText('Applied.', { timeout: 8000 });
  await expect(page.locator('main')).toContainText('nothing widens, so no passkey');
  expect(await page.evaluate(() => D.audit[0].detail)).toContain('no passkey');
  await expect(page.locator('[data-act="rollback-now"]')).toBeVisible();
});

test('a widening change (a promotion) needs the passkey and says why', async ({ page }) => {
  await go(page, '#/bots', { fresh: true });
  await page.evaluate(() => { const c = S.staged('bots', 'platform-sre'); c.autonomy.promote = (c.autonomy.promote || []).concat('gh.comment'); c.autonomy.tiers.reversible = (c.autonomy.tiers.reversible || []).concat('gh.comment'); c.autonomy.tiers.propose = c.autonomy.tiers.propose.filter((v) => v !== 'gh.comment'); S.stage('bots', 'platform-sre', c); });
  await toReview(page);
  await expect(page.locator('#pk-why')).toContainText('Needs your passkey');
  await expect(page.locator('[data-act="apply-go"]')).toContainText('Apply with passkey');
  await page.locator('.ack input').check();
  await page.locator('[data-act="apply-go"]').click();
  await expect(page.locator('#su-go')).toBeVisible();
  await page.locator('#su-go').click();
  await expect(page.locator('.callout.ember')).toContainText('Applied.', { timeout: 8000 });
});

test('mixing a widening and a non-widening change in one set needs the passkey', async ({ page }) => {
  await go(page, '#/bots', { fresh: true });
  await page.evaluate(() => {
    const a = S.staged('bots', 'release-scribe'); a.folder = 'personal'; S.stage('bots', 'release-scribe', a);
    const b = S.staged('bots', 'platform-sre'); b.autonomy.promote = (b.autonomy.promote || []).concat('kv.set'); b.autonomy.tiers.reversible = (b.autonomy.tiers.reversible || []).concat('kv.set'); b.autonomy.tiers.propose = b.autonomy.tiers.propose.filter((v) => v !== 'kv.set'); S.stage('bots', 'platform-sre', b);
  });
  await toReview(page);
  await expect(page.locator('#pk-why')).toContainText('Needs your passkey');
  await page.locator('.ack input').check();
  await page.locator('[data-act="apply-go"]').click();
  await expect(page.locator('#su-go')).toBeVisible();
});

// The passkey glyph means "this signs": it appears only on a control that starts a WebAuthn signing (approve, deny,
// apply a widening change, sign in, enrol, the signing sheet's own button) and on the passkeys themselves. Opening,
// reviewing and navigating never carry it (Daniel: "Review only opens the approval").
const { ROUTES } = require('./routes');
const SIGNS = new Set(['p-approve', 'kp-approve', 'kp-bulk', 'apply-go', 'signin', 'enroll', 'cm-store', 'cm-go', 'upd-apply']);
test('the passkey glyph sits only on controls that sign', async ({ page }) => {
  test.setTimeout(6 * 60 * 1000);
  const bad = [];
  for (const r of ROUTES) {
    await go(page, r, { fresh: true });
    const found = await page.evaluate((signs) => [...document.querySelectorAll('svg[data-ico="passkey"]')].filter((s) => s.getBoundingClientRect().width).map((s) => {
      const ctl = s.closest('a, button, [role="button"], summary');
      // the passkeys themselves (Access, a login that is a passkey) and the signing surfaces may show it as decoration
      if (!ctl) return s.closest('.ring, #pk-why, .pk-list, .pk-obj, .seal') ? null : `decoration outside a signing surface: ${s.parentElement.className} "${s.parentElement.textContent.trim().slice(0, 30)}"`;
      if (ctl.tagName === 'A') return `a link carries it: ${ctl.getAttribute('href')} "${ctl.textContent.trim().slice(0, 30)}"`;
      if (signs.includes(ctl.dataset.act) || /^(seal-go|su-go|pk-go)$/.test(ctl.id) || /passkey/i.test(ctl.textContent)) return null;
      // a signing action that can't be used yet (disabled with its reason) keeps its glyph
      if (ctl.getAttribute('aria-disabled') === 'true' && /^(Approve|Deny)/.test(ctl.textContent.trim())) return null;
      return `${ctl.tagName.toLowerCase()}[data-act="${ctl.dataset.act || ''}"]#${ctl.id} "${ctl.textContent.trim().slice(0, 30)}"`;
    }).filter(Boolean), [...SIGNS]);
    found.forEach((f) => bad.push(`${r}: ${f}`));
  }
  expect(bad).toEqual([]);
});
