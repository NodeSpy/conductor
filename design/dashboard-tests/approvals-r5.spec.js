// r5 blocker 4: p12's tiles said 0 Adds while the plan ('Plan: 1 to add, 2 to change, 0 to destroy') and the seal
// (+1 ~2 −0) said one. The tiles now say what the plan's own summary says; the add that is the replacement says so.
const { test, expect, go } = require('./fixtures');

test('p12: the tiles agree with the plan summary and the seal; the replacement is named as one of the adds', async ({ page }) => {
  await go(page, '#/bots/platform-sre/proposals/p12', { fresh: true });
  const tiles = await page.locator('.dl-changes > div').evaluateAll((xs) => xs.map((x) => [x.querySelector('span').textContent, +x.querySelector('b').textContent, (x.querySelector('small') || {}).textContent || '']));
  expect(tiles).toEqual([['Adds', 1, 'the replacement'], ['Changes', 2, ''], ['Replaces', 1, ''], ['Destroys', 0, '']]);
  const tf = await page.evaluate(() => D.proposals.find((p) => p.id === 'p12').tf);
  expect([tf.add, tf.change, tf.destroy]).toEqual([tiles[0][1], tiles[1][1], tiles[3][1]]);
  expect(await page.evaluate(() => D.tfPlan)).toContain(`Plan: ${tiles[0][1]} to add, ${tiles[1][1]} to change, ${tiles[3][1]} to destroy`);
});

// r5 blocker 5 (the approval and config sheets; the purchase sheet is the wallet designer's): words first, the payload
// folded under For audit, and on an iPhone the confirm and deny buttons inside the sheet, above Safari's toolbar.
test('the approval sheet says what you sign in words (Atlas, the ticket by its title); the payload is under For audit', async ({ page }) => {
  await go(page, '#/bots/platform-sre/proposals/p12', { fresh: true });
  await page.evaluate(() => UI.approve(D.proposals.find((x) => x.id === 'p12')));
  const m = page.locator('.modal');
  await expect(m.locator('.caps').first()).toHaveText('Approval · from Atlas');
  await expect(m.locator('.seal-words')).toContainText('which Atlas asked for on the ticket “SEV2 api p99 latency > 2s”');
  await expect(m.locator('.seal-what')).toContainText('Adds 1 (the replacement), changes 2, replaces a server (aws_instance.web), destroys nothing');
  await expect(m.locator('details.tech')).not.toHaveAttribute('open', '');
  const visible = await m.evaluate((x) => [...x.querySelectorAll('.modal-head, .seal-words, .seal-what, .modal-foot')].map((e) => e.innerText).join(' '));
  expect(visible).not.toMatch(/case c\d+|proposed by|platform-sre|sha256|digest/);
});

test('the config apply sheet lists the changes and why the passkey, in words; hashes are under For audit', async ({ page }) => {
  await go(page, '#/connectors/connectors', { fresh: true });
  await page.locator('[data-act="conn-add"]').first().click();
  await page.locator('.pk-modal [data-act="src-add"]').first().click();
  await page.fill('#f-src', 'acme/conductor-statuspage');
  await page.getByRole('button', { name: 'Fetch and review' }).click();
  await page.locator('#trust-ack').check(); await page.locator('#trust-go').click(); await page.keyboard.press('Escape');
  await page.goto(page.url().replace(/#.*/, '#/config/apply'));
  await expect(page.locator('#chk-state')).toContainText('passed');
  const ack = page.locator('.ack input[type=checkbox]'); if (await ack.count()) await ack.check();
  await page.locator('[data-act="apply-go"]').click();
  const sum = page.locator('.modal .su-sum');
  await expect(sum).toContainText("You're applying this change to conductor's config");
  await expect(sum).toContainText('trust acme/conductor-statuspage');
  await expect(sum).toContainText('Why your passkey');
  await expect(sum).toContainText('trusts acme/conductor-statuspage: its code may run here');
  await expect(page.locator('.modal details.tech summary')).toHaveText('For audit: the exact payload your passkey signs');
  await expect(page.locator('.modal details.tech')).not.toHaveAttribute('open', '');
});

for (const decision of ['approve', 'deny']) {
  test(`on an iPhone with Safari's toolbar, the ${decision} button is inside the sheet and on screen`, async ({ page }) => {
    test.skip(page.viewportSize().width > 720, 'phone');
    await page.setViewportSize({ width: 390, height: 664 });
    await go(page, '#/bots/platform-sre/proposals/p12', { fresh: true });
    await page.goto(page.url().replace('index.html#', 'index.html?sim=ios#'));
    await page.evaluate((d) => UI.approve(D.proposals.find((x) => x.id === 'p12'), d), decision);
    await page.locator('.modal').evaluate((m) => Promise.all(m.getAnimations().map((a) => a.finished))); // the sheet slides up 8px as it opens
    const g = await page.evaluate(() => { const m = document.querySelector('.modal').getBoundingClientRect(), b = document.querySelector('#seal-go').getBoundingClientRect(); return { mb: m.bottom, bb: b.bottom, bt: b.top, ih: innerHeight }; });
    expect(g.bb, 'the button is inside the sheet').toBeLessThanOrEqual(g.mb + 0.5);
    expect(g.mb, 'the sheet is on screen').toBeLessThanOrEqual(g.ih);
    expect(g.bt).toBeGreaterThan(0);
  });
}
