// 71: a knowledge entry's Evidence is a list of things, not one box of text: a ticket, a run, a pull request or issue,
// a file or image (the chat's attach rules), a link, or a note, added from "Add evidence", each a chip that opens what
// it names and comes off with its ✕. Conductor writes evidence as one string today: a string loads as one note and a
// single note saves back as that string, so existing files are unchanged.
const { test, expect, go } = require('./fixtures');

const files = (page, id) => page.evaluate((i) => R.get(i).files, id);
const field = (page) => page.locator('[data-field="evidence"]');
const chips = (page) => field(page).locator('.ev-chip');
const add = async (page, k) => { await field(page).locator('.ev-add').click(); await page.locator(`.menu [data-act="ev-new"][data-k="${k}"]`).click(); };
const open = async (page, title) => { const back = page.locator('[data-act="rp-list"]'); if (await back.isVisible()) await back.click(); await page.locator('.kind-entries [data-act="rp-open"]', { hasText: title }).click(); };
const PNG = Buffer.from('iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg==', 'base64');

test('evidence is chips that open what they name, compact on a short entry', async ({ page }) => {
  await go(page, '#/bots/platform-sre/memory', { fresh: true });
  await expect(chips(page)).toHaveCount(3);
  await expect(chips(page).nth(0)).toHaveClass(/ev-note/);
  await expect(chips(page).nth(1).locator('a')).toHaveAttribute('href', '#/tickets/c31');
  await expect(chips(page).nth(1)).toContainText('Record db-prod-1 failover drill outcome'); // the ticket by its title, never its id
  await expect(chips(page).nth(2).locator('a')).toHaveAttribute('href', '#/runs/r-7f3a');
  // compact: three chips and the Add button take at most two rows on a desktop, not a tall box
  if (page.viewportSize().width > 720) expect((await field(page).locator('.ev-list').boundingBox()).height).toBeLessThan(90);
  // an old entry's plain string is one note chip, and opening it changes nothing
  await open(page, 'ALB');
  await expect(chips(page)).toHaveCount(1);
  await expect(chips(page).first()).toHaveClass(/ev-note/);
  await expect(chips(page).first()).toContainText('modules/web/alb.tf');
  await expect(page.locator('[data-act="rp-save"]')).toHaveAttribute('aria-disabled', 'true');
});

test('add a ticket, a run, a pull request, a link, a note and an image; remove one; save a typed list', async ({ page }) => {
  await go(page, '#/bots/platform-sre/memory', { fresh: true });
  await open(page, 'ALB');
  await add(page, 'ticket');
  await page.locator('.menu [data-act="ev-put"][data-k="ticket"]').first().click();
  await add(page, 'run');
  await page.locator('.menu [data-act="ev-put"][data-k="run"]').first().click();
  await add(page, 'ref');
  await page.fill('#f-ref', 'acme web 1288'); await page.locator('#gen-submit').click();
  await expect(page.locator('.modal .form-err')).toContainText('owner/repo#number'); // said what it wants, nothing added
  await page.fill('#f-ref', 'acme/web#1288'); await page.locator('#gen-submit').click();
  await expect(field(page).locator('.ev-ref a')).toHaveAttribute('href', 'https://github.com/acme/web/issues/1288');
  await add(page, 'url');
  await page.fill('#f-url', 'grafana/d/db'); await page.locator('#gen-submit').click();
  await expect(page.locator('.modal .form-err')).toContainText('https://');
  await page.fill('#f-url', 'https://grafana.example.com/d/db-prod'); await page.locator('#gen-submit').click();
  await add(page, 'note');
  await page.fill('#f-note', 'Checked by hand in the console'); await page.locator('#gen-submit').click();
  // a file goes through the chat's attach rules: an image gets a thumbnail; a type it can't take is refused, in words
  await page.setInputFiles('#ev-file', [{ name: 'alb-targets.png', mimeType: 'image/png', buffer: PNG }, { name: 'tool.exe', mimeType: 'application/x-msdownload', buffer: Buffer.from('MZ') }]);
  await expect(field(page).locator('.ev-file img')).toBeVisible();
  await expect(field(page).locator('.ferr')).toContainText('tool.exe wasn’t attached: only images, PDF and text files can be attached');
  await expect(chips(page)).toHaveCount(7);
  // remove the run
  await field(page).locator('.ev-run .ev-x').click();
  await expect(chips(page)).toHaveCount(6);
  await page.locator('[data-act="rp-save"]').click();
  const f = (await files(page, 'bot:platform-sre'))['knowledge/alb.md'];
  expect(f).toMatch(/evidence:\n {2}- note: modules\/web\/alb.tf\n {2}- ticket: c\d+\n {2}- ref: acme\/web#1288\n {2}- url: https:\/\/grafana.example.com\/d\/db-prod\n {2}- note: Checked by hand in the console\n {2}- file: alb-targets.png\n {4}attachment: att_[0-9a-f]+\n/);
  expect(f).not.toContain('run:');
});

test('evidence is required: with every chip removed the save says so', async ({ page }) => {
  await go(page, '#/bots/platform-sre/memory', { fresh: true });
  await open(page, 'ALB');
  await field(page).locator('.ev-x').click();
  await expect(chips(page)).toHaveCount(0);
  await page.locator('[data-act="rp-save"]').click();
  await expect(page.locator('#ke-err')).toContainText('evidence: missing required field (at least one piece of evidence)');
  await expect(page.locator('[data-field="evidence"].bad')).toBeVisible();
});
