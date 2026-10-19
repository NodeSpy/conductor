// The four Plugins tabs are one list (ledger 67): Connections, Agent runtimes, Code languages and Machines each draw the
// flat connection row (its glyph on the name's line, how it is in plain words, a facts line, one ⋯ on every row, built
// in or not), with no inner footer; a language says where it's used ("1 workflow step, in …", linked), never "run: js";
// the add pickers are the connection picker: tight rows (one line of name and description, then facts), "Installed"
// for what is already there, no shadowed button, and only the list scrolls, with a fade while more is below.
const { test, expect, go } = require('./fixtures');

test('every Plugins tab draws the connection row with one ⋯ per row and a glyph on the name line', async ({ page }) => {
  for (const tab of ['connectors', 'runtimes', 'languages', 'hosts']) {
    await go(page, `#/connectors/${tab}`, { fresh: true });
    const r = await page.evaluate(() => [...document.querySelectorAll('#main .dl-conns > li.dl-conn')].map((li) => ({ more: li.querySelectorAll('.dl-na .icon-btn[aria-label^="More for"]').length, glyph: !!li.querySelector(':scope > .dl-ng > *, :scope > .cn-sel > input'), facts: !!li.querySelector('.fx'), text: li.innerText })));
    expect(r.length, `${tab} has rows`).toBeGreaterThan(0);
    for (const x of r) { expect(x.more, `${tab}: one ⋯ on "${x.text.split('\n')[0]}"`).toBe(1); expect(x.glyph, `${tab}: a glyph or mark in the gutter`).toBe(true); expect(x.facts, `${tab}: a facts line`).toBe(true); }
    expect(await page.locator('#main .panel-foot').count(), `${tab}: no inner footer`).toBe(0);
  }
  await go(page, '#/connectors/languages', { fresh: true });
  const t = await page.locator('#main .dl-conns').innerText();
  expect(t).not.toMatch(/run: js|Used by run/);
  expect(t).toMatch(/Used by 1 workflow step, in pr-review/);
  await expect(page.locator('#main .pl-row[data-k="js"] .dl-why a[href="#/workflows/pr-review"]')).toHaveCount(1);
  // the language glyphs are their own: a terminal for cli, the JavaScript mark for js
  expect(await page.locator('.pl-row[data-k="cli"] .dl-ng svg[data-ico="terminal"]').count()).toBe(1);
  expect(await page.locator('.pl-row[data-k="js"] .dl-ng svg.fx-svc').count()).toBe(1);
  // a built-in's ⋯ says what's possible: where it's used, which opens its workflow
  await page.locator('[data-act="lang-more"][data-p="cli"]').click();
  await page.getByText('Show where it’s used').click();
  await expect(page).toHaveURL(/#\/workflows\/ci-fixer$/);
});

test('the add-a-language picker: tight rows, Installed for what is there, no shadowed button, only the list scrolls', async ({ page }) => {
  await go(page, '#/connectors/languages', { fresh: true });
  await page.locator('[data-act="lang-add"]').first().click();
  await expect(page.locator('.pk-modal .pk-row').first()).toBeVisible();
  await expect(page.locator('.pk-modal input[type="search"], .pk-modal .fb input').first()).toHaveAttribute('placeholder', 'Find a code language');
  const m = await page.evaluate(() => { const rows = [...document.querySelectorAll('.pk-modal .pk-row')], l = document.querySelector('.pk-modal .pk-list'), modal = document.querySelector('.pk-modal');
    return { hs: rows.map((r) => r.getBoundingClientRect().height), inst: rows.filter((r) => /Installed/.test(r.querySelector('.dl-na').innerText)).map((r) => r.dataset.cat), choose: rows.filter((r) => /Installed/.test(r.querySelector('.dl-na').innerText) && r.querySelector('.dl-na .btn')).length,
      shadow: [...document.querySelectorAll('.pk-modal .pk-row .btn')].map((b) => getComputedStyle(b).boxShadow).filter((s) => s !== 'none'),
      icTop: rows.map((r) => Math.abs(r.querySelector('.sc-ic').getBoundingClientRect().top + 13 - (r.querySelector('.pk-l1 > b').getBoundingClientRect().top + r.querySelector('.pk-l1 > b').getBoundingClientRect().height / 2))),
      listScrolls: l.scrollHeight > l.clientHeight, modalScrolls: modal.scrollHeight > modal.clientHeight + 1, fade: l.classList.contains('more') }; });
  const phone = page.viewportSize().width <= 720; // a phone: two lines of description at most, then the facts beside a 44px touch button
  for (const h of m.hs) expect(h, 'a picker row is tight').toBeLessThanOrEqual(phone ? 140 : 72);
  expect(m.inst).toEqual(expect.arrayContaining(['engine:cli', 'engine:js']));
  expect(m.choose, 'an installed one has no Choose button').toBe(0);
  expect(m.shadow, 'no picker button has a drop shadow').toEqual([]);
  for (const d of m.icTop) expect(d, 'the glyph sits on the name line').toBeLessThanOrEqual(3);
  expect(m.modalScrolls, 'the frame does not scroll').toBe(false);
  if (m.listScrolls) {
    expect(m.fade, 'a fade while more rows are below').toBe(true);
    await page.locator('.pk-modal .pk-list').evaluate((l) => { l.scrollTop = l.scrollHeight; l.dispatchEvent(new Event('scroll')); });
    await expect(page.locator('.pk-modal .pk-list')).not.toHaveClass(/more/);
  }
});

test('the add-a-runtime picker opens first, and the runtime and machine ⋯ menus offer the version policy and tests', async ({ page }) => {
  await go(page, '#/connectors/runtimes', { fresh: true });
  await page.locator('[data-act="rt-add"]').first().click();
  await expect(page.locator('.pk-modal h2')).toHaveText('Add a runtime');
  await page.keyboard.press('Escape');
  await page.locator('[data-act="rt-more"][data-r="paseo"]').click();
  for (const l of ['Edit…', 'Hold at this version', 'Show the plugin’s permissions', 'Remove…']) await expect(page.locator('.menu, [role="menu"]').getByText(l, { exact: true })).toBeVisible();
  await page.keyboard.press('Escape');
  await go(page, '#/connectors/hosts', { fresh: true });
  await page.locator('[data-act="host-more"]').first().click();
  for (const l of ['Test the connection', 'Edit…', 'Remove…']) await expect(page.locator('.menu, [role="menu"]').getByText(l, { exact: true })).toBeVisible();
});
