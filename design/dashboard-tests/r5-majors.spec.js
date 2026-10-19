// Round 5 majors 11–15: the reviewers' repros.
const { test, expect, go, applyStaged } = require('./fixtures');

// r5-11: "Asks before anything else" with Slack chosen showed "Does on its own: Post a message". The channel it tells
// you in is the one thing it does on its own besides reading, and both the preset and the list say so.
test('New bot, Asks before anything else, telling you in Slack: the preset and the list agree', async ({ page }) => {
  await go(page, '#/bots/new', { fresh: true });
  await page.locator('[data-act="bw-purpose"][data-p="incidents"]').click();
  await page.locator('[data-act="bw-next"]').click(); await page.locator('[data-act="bw-next"]').click();
  await page.locator('[data-act="bw-tell"][data-v="slack"]').click(); await page.fill('#bw-channel', '#incidents');
  await page.locator('[data-act="bw-next"]').click();
  await page.locator('[data-act="bw-preset"][data-v="ask"]').click();
  await expect(page.locator('[data-act="bw-preset"][data-v="ask"]')).toContainText('tells you where you chose, on its own');
  const own = page.locator('.cang[data-s="ok"]');
  await expect(own).toContainText('Tell you in #incidents');
  await expect(own).not.toContainText('Post a message');
  await expect(page.locator('.cang[data-s="ember"]')).toContainText(/Add a note|Comment|note/i);
  await expect(page.locator('.bw-body')).not.toContainText(/shadow|observe\b/);
});

// r5-12: the regressions.
test('memory: what you typed survives a search in its memory', async ({ page }) => {
  await go(page, '#/bots/platform-sre/memory', { fresh: true });
  await page.locator('[data-act="rp-new"]').first().click();
  await page.locator('.modal [data-act="rp-new-pick"][data-k="knowledge"]').click();
  await page.fill('#ke-title', 'Cache TTLs are per route');
  await page.locator('.rp-find .fb-in').click(); await page.keyboard.type('runbook', { delay: 30 }); // as a person types
  await expect(page.locator('.rp-find .fb-in')).toHaveValue('runbook'); // every letter lands in the search
  await expect(page.locator('#ke-title')).toHaveValue('Cache TTLs are per route'); // and the entry keeps what you typed
});

test('Safety: turning the sandbox off asks first; cancelling leaves it on', async ({ page }) => {
  await go(page, '#/safety', { fresh: true });
  await page.locator('[data-act="sb-toggle"]').click();
  await expect(page.locator('.modal h2')).toHaveText('Turn the sandbox off?');
  await page.locator('#cf-cancel').click();
  expect(await page.evaluate(() => D.pending.length)).toBe(0);
  await page.locator('[data-act="sb-toggle"]').click(); await page.locator('#cf-ok').click();
  expect(await page.evaluate(() => (S.staged('sandbox', 'sandbox') || {}).agents)).toBe('off');
});

test('retry: one at a time, labelled plainly, with a note; the first run links to it; the retry\'s log is its own', async ({ page }) => {
  await go(page, '#/runs/r-7ef0', { fresh: true });
  await page.locator('.dl-actions .btn.primary[data-act="retry"]').click();
  const opts = await page.locator('.modal select#f-from option, .modal [data-k="from"] option').allTextContents();
  expect(opts.join(' | ')).toContain('(where it failed)'); expect(opts.join(' | ')).not.toContain('(selected)');
  await page.fill('.modal [data-k="note"], .modal #f-note', 'the test expects half-even rounding');
  await page.locator('.modal').getByRole('button', { name: 'Retry', exact: true }).click();
  await expect(page).toHaveURL(/#\/runs\/r-[0-9a-f]{4}$/);
  const id = page.url().split('/').pop();
  await expect(page.locator('#main')).toContainText('You retried it from “fix”');
  await expect(page.locator('#main')).toContainText('telling the agent: “the test expects half-even rounding”');
  await expect(page.locator('#main')).not.toContainText(/A check failed on .* just now/);
  // its log: times from when it started, the identity on this run's repository
  const g = await page.evaluate((rid) => { const r = D.runs.find((x) => x.id === rid), t0 = new Date(r.started); const ts = [...document.querySelectorAll('#log .ln i')].map((i) => i.textContent).filter(Boolean); const ln = [...document.querySelectorAll('#log .ln')].map((x) => x.textContent).find((t) => /identity agent:/.test(t)), id = ln ? ln.match(/identity agent:[\w-]+@([\w./-]+)\s*$/) : null; return { first: ts[0], start: t0.toTimeString().slice(0, 8), repo: r.repo, logRepo: id && id[1] }; }, id);
  if (g.first) expect(g.first >= g.start, `the log starts at ${g.first}, the step at ${g.start}`).toBe(true);
  if (g.logRepo) expect(g.logRepo).toBe(g.repo);
  // the first run points to it; a second Retry opens it instead of starting another
  await go(page, '#/runs/r-7ef0');
  await expect(page.locator('.dl-actions .btn.primary')).toHaveText('Open the retry');
  await page.evaluate(() => { const b = document.createElement('button'); b.dataset.act = 'retry'; b.dataset.run = 'r-7ef0'; b.dataset.from = 'fix'; document.body.appendChild(b); b.click(); });
  await expect(page).toHaveURL(new RegExp(`#/runs/${id}$`));
  expect(await page.evaluate(() => D.runs.filter((x) => x.retry_of === 'r-7ef0' && x.status === 'run').length)).toBe(1);
});

// r5-13: the pack-install consent sentence was clipped (the panel borrowed the plan's code style, white-space: pre);
// now it wraps inside its callout on a desktop and an iPhone.
test('installing a pack: the consent sentence wraps, every action and the rest of it visible', async ({ page }) => {
  await go(page, '#/extensions/install/pack/pr-review-team', { fresh: true });
  const g = page.locator('.grants-loud');
  await expect(g).toBeVisible();
  const m = await g.evaluate((e) => ({ ws: getComputedStyle(e).whiteSpace, over: e.scrollWidth - e.clientWidth }));
  expect(m.ws).not.toBe('pre');
  expect(m.over, 'nothing overflows the callout').toBeLessThanOrEqual(1);
  await expect(g).toContainText('It asks for these actions:');
  await expect(g).toContainText('gh.review');
});

// r5-15: after New team, the review's crumb and its Back button read the Bots page's heading with its count ("Bots 6 bots
// · 2 teams · 2 solo") and the button ran off the screen. They say "Bots".
test('after New team: the review says Bots in its crumb and its Back button, which fits', async ({ page }) => {
  await go(page, '#/bots', { fresh: true });
  await page.locator('[data-act="team-new"]').first().click();
  await page.fill('.modal #f-name, .cdrawer #f-name', 'data');
  await page.locator('.modal [data-listk="bots"] select, .cdrawer [data-listk="bots"] select').first().selectOption('platform-data');
  await page.locator('.modal .modal-foot .btn.primary, .cdrawer .btn.primary').last().click();
  await expect(page).toHaveURL(/#\/config\/apply/);
  await expect(page.locator('#main .dl-crumbs').first()).toContainText(/^\s*Bots\s*›?\s*Review and apply/);
  await expect(page.locator('#main .dl-crumbs').first()).not.toContainText('bots ·');
  await applyStaged(page);
  const back = page.locator('[data-act="return-to"]');
  await expect(back).toHaveText(/^Back to Bots$/);
  expect(await back.evaluate((b) => b.getBoundingClientRect().right <= innerWidth)).toBe(true);
});
