// Personal details and payment cards (ledger item 40), mocked on the conductor side's semantics (feat/agent-browser,
// docs/wiki/Browser.md): values are never shown, a config-defined profile or card changes in the config, only a
// widening asks for the passkey (a new profile or card, a bot or team, a site or a detail, a looser "may learn", a
// shop, a higher limit, an unfreeze) and so does every purchase decision; freezing is one click. Purchases wait in
// Needs you and in a bot's Approvals, with the shop, items, total, a subscription flagged, the card and the limits;
// receipts land on the ticket; a bot's What it can do says what it can buy, and buying without asking is an opt-in
// widening under its spend cap.
const { test, expect, go, applyStaged } = require('./fixtures');

const passkey = (page) => page.locator('#su-go');

test('Browsers has four tabs on every page: Sessions, Logins, Personal details, Payment cards', async ({ page }) => {
  for (const [route, on] of [['#/browser', 'Sessions'], ['#/browser/logins', 'Logins'], ['#/browser/details', 'Personal details'], ['#/browser/cards', 'Payment cards']]) {
    await go(page, route);
    const tabs = page.locator('#main .tabs [role="tab"]');
    await expect(tabs).toHaveCount(4);
    expect((await tabs.allTextContents()).map((t) => t.replace(/\s*\d+$/, '').trim())).toEqual(['Sessions', 'Logins', 'Personal details', 'Payment cards']);
    await expect(page.locator('#main .tabs [aria-selected="true"]')).toContainText(on);
  }
});

test('personal details never show a value: masked, its source in words, and a config profile changes in the config', async ({ page }) => {
  await go(page, '#/browser/details/home', { fresh: true });
  const rows = page.locator('.wl-fields tbody tr');
  await expect(rows).toHaveCount(8);
  await expect(rows.first()).toContainText('••••');
  await expect(rows.first()).toContainText('typed in, stored write-only');
  await expect(page.locator('.wl-fields')).toContainText('1Password · Me/Home/email');
  await expect(page.locator('.wl-fields')).toContainText('environment variable HOME_PHONE');
  await expect(page.locator('#main')).toContainText('Only on work you asked for');
  // sites with the details allowed there, and who
  await expect(page.locator('#main')).toContainText('Fills name, email, phone and the address');
  await expect(page.locator('#main')).toContainText('Hearth');
  await go(page, '#/browser/details/work');
  await expect(page.locator('.wl-cfg')).toContainText('Defined in the config');
  await expect(page.locator('[data-act="w-site-add"], button:has-text("Add a site")').first()).toHaveAttribute('aria-disabled', 'true');
  await expect(page.locator('.wl-vis')).toHaveCount(0);
});

test('what a bot may learn: looser asks for the passkey, stricter does not', async ({ page }) => {
  await go(page, '#/browser/details/home', { fresh: true });
  const phone = page.locator('.wl-vis[data-f="phone"]');
  await phone.selectOption('fact');
  await expect(passkey(page)).toBeVisible();
  await passkey(page).click();
  await expect.poll(() => page.evaluate(() => D.wallet.profiles[0].visible.phone)).toBe('fact');
  await page.locator('.wl-vis[data-f="email"]').selectOption('hidden');
  await expect(passkey(page)).toHaveCount(0);
  await expect.poll(() => page.evaluate(() => D.wallet.profiles[0].visible.email)).toBeUndefined();
});

test('add a site: the exact address only, and it widens', async ({ page }) => {
  await go(page, '#/browser/details/home', { fresh: true });
  await page.locator('[data-act="w-site-add"]').click();
  await page.locator('#f-origin').fill('https://store.example.org/checkout');
  await page.locator('#gen-submit').click();
  await expect(page.locator('[data-field="origin"] .ferr')).toContainText('exact address');
  await page.locator('#f-origin').fill('https://store.example.org');
  await page.locator('#gen-submit').click();
  await expect(page.locator('.form-err')).toContainText('at least one detail');
  await page.locator('#f-f_email').check();
  await page.locator('#gen-submit').click();
  await passkey(page).click();
  await expect(page.locator('#main')).toContainText('store.example.org');
  expect(await page.evaluate(() => D.wallet.profiles[0].sites.find((s) => s.origin === 'https://store.example.org').fields)).toEqual(['email']);
});

test('add personal details: required fields, a typed value is stored write-only and cleared, then the passkey', async ({ page }) => {
  await go(page, '#/browser/details/new', { fresh: true });
  await page.locator('[data-act="w-np-save"]').click();
  await expect(page.locator('[data-field="name"].bad')).toBeVisible();
  await expect(page.locator('[data-field="who"].bad')).toBeVisible();
  await page.locator('#w-np-name').fill('travel');
  await page.locator('#wv-email').fill('someone@example.com');
  await page.locator('[data-wv="name"] [data-act="w-src"][data-v="ref"]').click();
  await page.locator('#wv-name').fill('not a reference');
  await page.locator('#w-np-origin').fill('https://trips.example.com');
  await page.locator('#w-np-who').selectOption('bot:homelab');
  await page.locator('[data-act="w-np-save"]').click();
  await expect(page.locator('[data-wv="name"].bad')).toContainText('A reference such as');
  await page.locator('#wv-name').fill('{{ vault "op" "Me/Travel/name" }}');
  await page.locator('[data-act="w-np-save"]').click();
  await passkey(page).click();
  await expect(page).toHaveURL(/#\/browser\/details\/travel$/);
  const p = await page.evaluate(() => D.wallet.profiles.find((x) => x.name === 'travel'));
  expect(p.fields.email).toEqual({ kind: 'stored', source: '' });
  expect(p.fields.name.kind).toBe('vault');
  expect(JSON.stringify(await page.evaluate(() => D))).not.toContain('someone@example.com');
  await expect(page.locator('#main')).not.toContainText('someone@example.com');
});

test('cards: spend against the limits, warnings from validate, and the card never shown beyond its last four', async ({ page }) => {
  await go(page, '#/browser/cards', { fresh: true });
  const household = page.locator('.wl-card-row[data-card="household"]');
  await expect(household).toContainText('Visa •••• 4242');
  await expect(household).toContainText('$12.50 of $200.00 in 24 hours');
  await expect(page.locator('.wl-card-row[data-card="spare"]')).toContainText('No shop is allowed yet');
  await go(page, '#/browser/cards/spare');
  await expect(page.locator('.wl-note')).toHaveCount(2);
  await expect(page.locator('.wl-note').first()).toContainText('prefer a virtual or single-use card');
  await expect(page.locator('.wl-cfg')).toContainText('Defined in the config');
  await go(page, '#/browser/cards/household');
  await expect(page.locator('.wl-limits').first()).toContainText('$12.50 of $200.00');
  await expect(page.locator('.wl-meter').first()).toBeVisible();
  await expect(page.locator('.wl-idl')).toContainText('Hearth');
  await expect(page.locator('#main')).not.toContainText('4242 4242');
});

test('freeze is one click; unfreezing asks for the passkey', async ({ page }) => {
  await go(page, '#/browser/cards/household', { fresh: true });
  await page.locator('#main [data-act="w-freeze"]').first().click();
  await expect(passkey(page)).toHaveCount(0);
  await expect(page.locator('.wl-frozen')).toContainText('Frozen');
  expect(await page.evaluate(() => D.wallet.cards[0].frozen)).toBe(true);
  // a purchase with a frozen card can't be approved
  await go(page, '#/browser/purchases/pu8');
  await expect(page.locator('.dl-decide')).toContainText('The card is frozen');
  await go(page, '#/browser/cards/household');
  await page.locator('#main [data-act="w-unfreeze"]').first().click();
  await passkey(page).click();
  await expect(page.locator('.wl-frozen')).toHaveCount(0);
  expect(await page.evaluate(() => D.wallet.cards[0].frozen)).toBe(false);
});

test('limits: lowering applies at once, raising asks for the passkey; a shop must be an exact address', async ({ page }) => {
  await go(page, '#/browser/cards/household', { fresh: true });
  await page.locator('[data-act="w-limits"]').click();
  await page.locator('#f-per_purchase').fill('100');
  await page.locator('#gen-submit').click();
  await expect(passkey(page)).toHaveCount(0);
  await expect.poll(() => page.evaluate(() => D.wallet.cards[0].limits.per_purchase)).toBe(100);
  await page.locator('[data-act="w-limits"]').click();
  await page.locator('#f-per_month').fill('500');
  await page.locator('#gen-submit').click();
  await passkey(page).click();
  await expect.poll(() => page.evaluate(() => D.wallet.cards[0].limits.per_month)).toBe(500);
  await page.locator('[data-act="w-merch-add"]').click();
  await page.locator('#f-origin').fill('shop.example.org');
  await page.locator('#gen-submit').click();
  await expect(page.locator('[data-field="origin"] .ferr')).toContainText('exact address');
  await page.locator('#f-origin').fill('https://shop.example.org');
  await page.locator('#gen-submit').click();
  await passkey(page).click();
  await expect(page.locator('#main')).toContainText('shop.example.org');
});

test('a bot’s purchase waits in Needs you and its Approvals: shop, items, total, card, a higher price flagged, the limits', async ({ page }) => {
  await go(page, '#/attention', { fresh: true });
  const pending = await page.evaluate(() => D.purchases.filter((p) => UI.puStatus(p) === 'pending').length);
  await expect(page.locator('.dl-tile', { hasText: 'Purchase' }).locator('b')).toHaveText(String(pending));
  const row = page.locator('.dl-need', { hasText: 'Buy a 4 TB NAS hard drive for $89.99' });
  await expect(row.locator('.fx')).toContainText('shop.example.com');
  await expect(row.locator('.fx')).toContainText('$89.99');
  await expect(row.locator('.dl-na')).toHaveText('Review');
  await row.locator('a').first().click();
  await expect(page).toHaveURL(/#\/bots\/homelab\/proposals\/p14/);
  const card = page.locator('.dl-proposal');
  await expect(card.locator('.wl-items')).toContainText('4 TB NAS hard drive');
  await expect(card).toContainText('Asks again for a higher price');
  await expect(card).toContainText('Hearth’s limits:');
  await expect(card).toContainText('never sees the card');
  await expect(card.locator('[data-act="p-approve"][data-d="approve"]')).toContainText('Approve with passkey');
  // the purchase page of a waiting bot purchase is its proposal
  await go(page, '#/browser/purchases/pu7');
  await expect(page).toHaveURL(/#\/bots\/homelab\/proposals\/p14/);
  await page.locator('[data-act="p-approve"][data-d="approve"]').first().click();
  await page.locator('#seal-go').click();
  await expect(page.locator('.dl-decided')).toContainText('Approved');
  expect(await page.evaluate(() => UI.puStatus(D.purchases.find((p) => p.id === 'pu7')))).toBe('approved');
  await go(page, '#/attention');
  await expect(page.locator('.dl-need', { hasText: 'NAS hard drive' })).toHaveCount(0);
});

test('a subscription is flagged; approving and denying are signed with the passkey', async ({ page }) => {
  await go(page, '#/browser/purchases/pu8', { fresh: true });
  await expect(page.locator('.wl-flag')).toContainText('A subscription: it charges $6.00 every month');
  await page.locator('[data-act="w-pu-decide"][data-d="deny"]').click();
  await page.locator('#f-reason').fill('Not this month.');
  await page.locator('#gen-submit').click();
  await passkey(page).click();
  await expect(page.locator('.dl-decided')).toContainText('Denied by ops: “Not this month.”');
  await go(page, '#/attention');
  await expect(page.locator('.dl-need', { hasText: 'Off-site backup' })).toHaveCount(0);
});

test('receipts: on the ticket, and a charge above the approval is flagged', async ({ page }) => {
  await go(page, '#/tickets/c39', { fresh: true });
  const panel = page.locator('.wl-tk');
  await expect(panel).toContainText('SATA cable, 2-pack');
  await expect(panel).toContainText('PX-20418');
  await expect(panel).toContainText('Waiting for you');
  await go(page, '#/browser/purchases/pu1');
  await expect(page.locator('.wl-receipt.flag')).toContainText('more than the approved 24.00 USD');
});

test('What it can do says what a bot can buy; buying without asking is an opt-in widening under its spend cap', async ({ page }) => {
  await go(page, '#/bots/homelab/access', { fresh: true });
  const box = page.locator('#wl-bot');
  await expect(box).toContainText('Can buy: asks you first, up to $120.00 a purchase, at shop.example.com, parts.example.net and backup.example.com');
  await expect(box).toContainText('Without asking: never');
  await expect(box).toContainText('Fills your details');
  await expect(page.locator('.cang[data-s="ember"]')).toContainText('Buy things with a card you allow');
  await box.locator('[data-act="w-un-on"]').click();
  await page.locator('#f-per_action').fill('151');
  await page.locator('#f-per_period').fill('30');
  await page.locator('#gen-submit').click();
  await expect(page.locator('[data-field="per_action"] .ferr')).toContainText('spend cap');
  await page.locator('#f-per_action').fill('10');
  await page.locator('#gen-submit').click();
  await expect(page.locator('.form-err')).toContainText('at least one shop');
  await page.locator('#f-shop0').check();
  await page.locator('#gen-submit').click();
  await expect(page.locator('#main')).toContainText('autonomy.unattended');
  await applyStaged(page, { expectWidening: true });
  await go(page, '#/bots/homelab/access');
  await expect(page.locator('#wl-bot')).toContainText('Without asking: up to $10.00 a purchase');
});
