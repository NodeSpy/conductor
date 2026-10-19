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

const src = async (page, k, s) => { const el = page.locator(`.vs[data-vs="${k}"]`).first(); await el.locator('.vs-srcbtn').click(); await el.locator(`[data-act="vs-src"][data-v="${s}"]`).click(); };
const lookup = async (page, sel, q, ref) => { const inp = page.locator(sel); await inp.click(); await inp.fill(q); await page.locator(`${sel.replace(/-pick$/, '-list')} [data-act="vs-choose"][data-ref*="${ref}"]`).first().click(); };

test('add personal details: the shared value field (typed write-only, or a vault item from the lookup; never a setting), several bots or teams, then the passkey', async ({ page }) => {
  await go(page, '#/browser/details/new', { fresh: true });
  await page.locator('[data-act="w-np-save"]').click();
  await expect(page.locator('[data-field="name"].bad')).toBeVisible();
  await expect(page.locator('[data-field="who"].bad')).toBeVisible();
  await expect(page.locator('#main')).not.toContainText('--profile');
  await page.locator('#w-np-name').fill('travel');
  await page.locator('#wv-email-in').fill('someone@example.com');
  await src(page, 'name', 'vault');
  await lookup(page, '#wv-name-pick', 'home name', 'Me/Home/name');
  await expect(page.locator('.vs[data-vs="name"] .vs-chosen')).toContainText('Home');
  await src(page, 'phone', 'setting');
  await lookup(page, '#wv-phone-pick', 'org', 'settings.org');
  await page.locator('#w-np-origin').fill('https://trips.example.com');
  await page.locator('#w-np [data-listk="who"] select').selectOption('bot:homelab');
  await page.locator('#w-np [data-listk="who"] select').selectOption('team:finance');
  await page.locator('[data-act="w-np-save"]').click();
  await expect(page.locator('[data-field="phone"].bad')).toContainText('Not from a setting');
  await page.locator('.vs[data-vs="phone"] [data-act="vs-clear"]').click();
  await page.locator('[data-act="w-np-save"]').click();
  await passkey(page).click();
  await expect(page).toHaveURL(/#\/browser\/details\/travel$/);
  const p = await page.evaluate(() => D.wallet.profiles.find((x) => x.name === 'travel'));
  expect(p.fields.email).toEqual({ kind: 'stored', source: '' });
  expect(p.fields.name).toEqual({ kind: 'vault', source: '{{ vault "op" "Me/Home/name" }}' });
  expect(p.fields.phone).toBeUndefined();
  expect(p.identities).toEqual(['bot:homelab', 'team:finance']);
  expect(JSON.stringify(await page.evaluate(() => D))).not.toContain('someone@example.com');
  await expect(page.locator('#main')).not.toContainText('someone@example.com');
});

test('add a card: the shared value fields, $ amounts, shops as rows each checked, several bots or teams, inside the page container', async ({ page }) => {
  await go(page, '#/browser/cards/new', { fresh: true });
  const [h1, ref] = await page.evaluate(() => [document.querySelector('#main h1').getBoundingClientRect().left, document.querySelector('#w-nc').getBoundingClientRect().left]);
  await go(page, '#/activity');
  expect(await page.evaluate(() => document.querySelector('#main h1').getBoundingClientRect().left)).toBeCloseTo(h1, 0);
  expect(ref).toBeCloseTo(h1, 0);
  await go(page, '#/browser/cards/new');
  await expect(page.locator('#main')).not.toContainText('--card');
  await expect(page.locator('.wl-money')).toHaveCount(3);
  await expect(page.locator('[data-money="per_purchase"]')).toHaveAttribute('placeholder', 'Amount');
  await page.locator('#w-nc-name').fill('travel');
  await page.locator('#wv-number-in').fill('4242424242424242');
  await src(page, 'expiry', 'vault');
  await lookup(page, '#wv-expiry-pick', 'household expiry', 'Cards/Household/expiry');
  await page.locator('[data-money="per_purchase"]').fill('$40');
  await page.locator('[data-money="per_day"]').fill('80');
  await page.locator('[data-money="per_month"]').fill('200.50');
  await page.locator('[data-act="w-row-add"]').click(); await page.locator('[data-row="merchants"]').last().fill('shop.example.org/cart');
  await page.locator('[data-act="w-row-add"]').click(); await page.locator('[data-row="merchants"]').last().fill('https://trips.example.com');
  await page.locator('#w-nc [data-listk="who"] select').selectOption('bot:homelab');
  await page.locator('#w-nc [data-listk="who"] select').selectOption('team:finance');
  await page.locator('[data-act="w-nc-save"]').click();
  // only the wrong row is marked, with the reason
  await expect(page.locator('[data-row="merchants"].wl-bad')).toHaveCount(1);
  await expect(page.locator('[data-field="merchants"] .ferr')).toContainText('shop.example.org/cart');
  await page.locator('.wl-row').first().locator('[data-act="w-row-rm"]').click();
  await page.locator('[data-act="w-nc-save"]').click();
  await passkey(page).click();
  await expect(page).toHaveURL(/#\/browser\/cards\/travel$/);
  const c = await page.evaluate(() => D.wallet.cards.find((x) => x.name === 'travel'));
  expect(c.limits).toMatchObject({ per_purchase: 40, per_day: 80, per_month: 200.5 });
  expect(c.merchants).toEqual(['https://trips.example.com']);
  expect(c.identities).toEqual(['bot:homelab', 'team:finance']);
  expect(c.fields.number.kind).toBe('stored');
  expect(c.fields.expiry.source).toBe('{{ vault "op" "Cards/Household/expiry" }}');
  expect(JSON.stringify(await page.evaluate(() => D))).not.toContain('4242424242424242');
});

test('cards: spend against the limits, warnings from validate, and the card never shown beyond its last four', async ({ page }) => {
  await go(page, '#/browser/cards', { fresh: true });
  const household = page.locator('.wl-crow[data-card="household"]');
  await expect(household).toContainText('Visa •••• 4242');
  await expect(household).toContainText('$12.50 of $200.00 in 24 hours');
  await expect(page.locator('.wl-crow[data-card="spare"]')).toContainText('No shop is allowed yet');
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
  await expect(card).toContainText('Hearth’s own caps');
  await expect(card).toContainText('never sees the card');
  await expect(card.locator('[data-act="p-approve"][data-d="approve"]')).toContainText('Approve with passkey');
  // the purchase page of a waiting bot purchase is its proposal
  await go(page, '#/browser/purchases/pu7');
  await expect(page).toHaveURL(/#\/bots\/homelab\/proposals\/p14/);
  await page.locator('[data-act="p-approve"][data-d="approve"]').first().click();
  await page.locator('#seal-go').click();
  await expect(page.locator('.wl-done')).toContainText('Approved by you');
  expect(await page.evaluate(() => UI.puStatus(D.purchases.find((p) => p.id === 'pu7')))).toBe('approved');
  await go(page, '#/attention');
  await expect(page.locator('.dl-need', { hasText: 'NAS hard drive' })).toHaveCount(0);
});

test('a subscription is flagged; approving and denying are signed with the passkey', async ({ page }) => {
  await go(page, '#/browser/purchases/pu8', { fresh: true });
  await expect(page.locator('.wl-flag')).toContainText('A subscription: it charges $6.00 every month');
  await page.locator('[data-act="w-pu-decide"][data-d="deny"]').click();
  await page.locator('#seal-reason').fill('Not this month.');
  await page.locator('#seal-go').click();
  await expect(page.locator('.wl-done')).toContainText('Denied by you');
  await expect(page.locator('.wl-done')).toContainText('“Not this month.”');
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
  await expect(box).toContainText('Can buy: asks you first, up to $100.00 a purchase, $150.00 a day and $300.00 in 30 days, at shop.example.com, parts.example.net and backup.example.com');
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

// Ledger 56: spend limits stack (card → the card for the caller → the bot's purchases: under its team's budget.purchases →
// the ticket's spend_limit), and the lowest wins; refusals name the layer and what's left.
test('the approval lists what each limit leaves after the purchase, the tightest marked; over one, it says conductor would refuse', async ({ page }) => {
  await go(page, '#/bots/homelab/proposals/p14', { fresh: true });
  const ls = page.locator('.wl-layers li');
  await expect(ls).toHaveCount(4);
  await expect(ls.filter({ hasText: 'This ticket’s budget' })).toContainText('$17.51 left of $120.00');
  await expect(page.locator('.wl-layers li.bind')).toContainText('Hearth’s own caps');
  await expect(page.locator('.wl-layers li.bind')).toContainText('the tightest');
  await page.evaluate(() => { D.cases.find((c) => c.id === 'c39').spend_limit.amount = '100.00'; S.save(); });
  await go(page, '#/bots/homelab/proposals/p14');
  await expect(page.locator('.wl-layers li.over')).toContainText('This ticket’s budget');
  await expect(page.locator('.dl-proposal')).toContainText('Conductor would refuse this now: over this ticket’s $100.00 limit: $87.50 left');
});

test('a ticket’s spending limit: what is left, paid and waiting; lowering needs no passkey, raising does, and above a 30-day cap it warns', async ({ page }) => {
  await go(page, '#/tickets/c39', { fresh: true });
  const panel = page.locator('.wl-tkb');
  await expect(panel).toContainText('$17.51 left of $120.00');
  await expect(panel).toContainText('$12.50 paid · $89.99 waiting or approved');
  await expect(panel).toContainText('Only with household, only at shop.example.com and parts.example.net');
  await expect(page.locator('.tk-chat')).toContainText('over this ticket’s $120.00 limit, $17.51 left');
  await panel.locator('[data-act="w-tkb"]').click();
  await page.locator('[data-money="budget"]').fill('110');
  await page.locator('#w-tkb-go').click();
  await expect(passkey(page)).toHaveCount(0);
  await expect(page.locator('.wl-tkb')).toContainText('$7.51 left of $110.00');
  await page.locator('.wl-tkb [data-act="w-tkb"]').click();
  await page.locator('[data-money="budget"]').fill('350');
  await page.locator('#w-tkb-go').click();
  await passkey(page).click();
  await expect(page.locator('.wl-tkb')).toContainText('left of $350.00');
  await expect(page.locator('.wl-tkb .wl-flag')).toContainText('Above Hearth’s 30-day cap ($300.00)');
});

test('a new ticket can carry a spending limit (a widening: the passkey), held to one card and some shops', async ({ page }) => {
  await go(page, '#/tickets/new?bot=homelab', { fresh: true });
  await page.locator('#tk-title').fill('Buy a spare fan for nas-01');
  await page.locator('#w-nt-b > summary').click();
  await page.locator('[data-money="budget"]').fill('abc');
  await page.locator('[data-act="tk-create"][data-stage="ready"]').click();
  await expect(page.locator('[data-field="budget"] .ferr')).toContainText('An amount in dollars');
  await page.locator('[data-money="budget"]').fill('$30');
  await page.locator('#w-bcard').selectOption('household');
  await page.locator('#w-nt-b [data-act="w-row-add"]').click(); await page.locator('[data-row="bmerch"]').fill('https://parts.example.net');
  await page.locator('[data-act="tk-create"][data-stage="ready"]').click();
  await passkey(page).click();
  await expect(page).toHaveURL(/#\/tickets\/c\d+$/);
  await expect(page.locator('.wl-tkb')).toContainText('$30.00 left of $30.00');
  const sl = await page.evaluate(() => D.cases[0].spend_limit);
  expect(sl).toEqual({ amount: '30.00', currency: 'USD', card: 'household', merchants: ['https://parts.example.net'] });
});

test('Can buy on a bot comes from the whole stack; its own caps can only be lower than its team’s; a raised cap is a widening in review', async ({ page }) => {
  await go(page, '#/bots/homelab/access', { fresh: true });
  await expect(page.locator('#wl-bot')).toContainText('up to $100.00 a purchase, $150.00 a day and $300.00 in 30 days');
  await expect(page.locator('#wl-bot .wl-by')).toContainText('a purchase is set by its own cap');
  await page.locator('[data-act="w-caps"][data-n="homelab"]').click();
  await page.locator('[data-money="per_purchase"]').fill('120');
  await page.locator('#w-caps-go').click();
  await expect(page.locator('#main')).toContainText('purchases.per_purchase: raised from 100 to 120');
  await applyStaged(page, { expectWidening: true });
  // a team's caps are the ceiling: Ledger (billing-sre, team finance) can't go above $50.00 a purchase
  await go(page, '#/teams/finance/defaults');
  await expect(page.locator('.wl-team')).toContainText('each bot up to $50.00 a purchase');
  await page.evaluate(() => { const b = D.bots.find((x) => x.name === 'billing-sre'); b.cfg.skill.verbs.push('browser.purchase'); D.wallet.cards[1].identities.push('bot:billing-sre'); S.save(); });
  await go(page, '#/bots/billing-sre/access');
  await expect(page.locator('#wl-bot')).toContainText('Its own caps: none');
  await page.locator('[data-act="w-caps"][data-n="billing-sre"]').click();
  await page.locator('[data-money="per_purchase"]').fill('60'); await page.locator('[data-money="per_day"]').fill('90'); await page.locator('[data-money="per_month"]').fill('200');
  await page.locator('#w-caps-go').click();
  await expect(page.locator('#w-caps-err')).toContainText('Above team finance’s caps');
});

test('a refusal names the layer that stopped it and what is left', async ({ page }) => {
  await go(page, '#/tickets/c39', { fresh: true });
  const words = await page.evaluate(() => { const ls = UI.limitLayers({ cardN: 'household', bot: 'homelab', caseId: 'c39', total: 24 }); return ls.filter((l) => l.over).map((l) => UI.refusalWords(l, ls.cur)); });
  expect(words).toContain('over this ticket’s $120.00 limit: $17.51 left');
});


// Round 5 (items 5, 6, 7): the purchase signing sheet in words, a decided state at the top, a ticket's limit on every ticket.
test('the purchase signing sheet says what you agree to in words; ids and the digest only under For audit; its confirm row on screen', async ({ page }) => {
  for (const [route, act] of [['#/bots/homelab/proposals/p14', '[data-act="p-approve"][data-d="approve"]'], ['#/browser/purchases/pu8', '[data-act="w-pu-decide"][data-d="approve"]']]) {
    await go(page, route, { fresh: true });
    await page.locator(act).first().click();
    const sheet = page.locator('.modal.wl-seal');
    await expect(sheet.locator('.seal-words')).toContainText('You’re letting');
    const text = await sheet.evaluate((m) => [...m.querySelectorAll(':scope > .modal-head, .seal-words, .wl-seal-where')].map((e) => e.innerText).join('\n'));
    for (const bad of ['{"name"', 'recurring false', 'browser.purchase', 'case c', 'proposed by', 'sha256', 'p14', 'pu8']) expect(text, `no “${bad}” before For audit`).not.toContain(bad);
    await expect(sheet.locator('.wl-seal-audit')).not.toHaveAttribute('open', '');
    await expect(sheet.locator('.wl-seal-audit')).toContainText('sha256');
    const fits = await page.locator('#seal-go').evaluate((b) => { const r = b.getBoundingClientRect(), vh = (window.visualViewport ? visualViewport.height : innerHeight); return r.bottom <= vh && r.top >= 0; });
    expect(fits, 'the confirm button is on screen').toBe(true);
    await page.locator('.modal.wl-seal [data-close]').last().click();
  }
});

test('after you decide a purchase, its top says what happened, by whom, when and what next; the needs-you edge goes', async ({ page }) => {
  await go(page, '#/bots/homelab/proposals/p14', { fresh: true });
  await page.locator('[data-act="p-approve"][data-d="approve"]').first().click();
  await page.locator('#seal-go').click();
  const done = page.locator('.dl-proposal > .wl-done');
  await expect(done).toContainText('Approved by you');
  await expect(done).toContainText('may pay once');
  await expect(page.locator('.dl-proposal')).toHaveClass(/decided/);
  await expect(page.locator('.dl-proposal .dl-kick')).toHaveCount(0);
  expect(await done.evaluate((e) => e.getBoundingClientRect().top < innerHeight / 2)).toBe(true);
  await go(page, '#/logs/audit');
  await expect(page.locator('#main')).toContainText('approved a purchase');
});

test('a ticket’s spending limit shows on every ticket that has one (a team’s, a draft); the draft sheet says Save draft; no case words', async ({ page }) => {
  await go(page, '#/tickets/new?team=platform', { fresh: true });
  await page.locator('#tk-title').fill('Order a spare SSD for the build box');
  await page.locator('#w-nt-b > summary').click();
  await page.locator('[data-money="budget"]').fill('50');
  await page.locator('[data-act="tk-create"][data-stage="draft"]').click();
  await expect(page.locator('#su-go')).toContainText('Save draft with passkey');
  const sheet = await page.locator('.modal').innerText();
  expect(sheet).not.toMatch(/new_case|case:/);
  expect(sheet).toContain('saved as a draft');
  await page.locator('#su-go').click();
  await expect(page).toHaveURL(/#\/tickets\/c\d+$/);
  await expect(page.locator('.wl-tkb')).toContainText('$50.00 left of $50.00');
  await expect(page.locator('.wl-tkb')).toContainText('whichever bot on team platform takes it');
});
