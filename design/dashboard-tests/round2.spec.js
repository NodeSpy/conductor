// Round 2 of the adversarial reviews, as code: each finding that was fixed has a check here, so a later pass can't
// quietly undo it. Blockers first (truth and safety), then the majors. IDs are the reviewers' (bugs B/M, brand B/M,
// newcomer N, power N).
const { test, expect, go, isPhone } = require('./fixtures');

test('one source for the last 12 hours: Home\'s tile, its failures and Activity agree, and the parts add up (bugs B5, power N8)', async ({ page }) => {
  await go(page, '#/overview', { fresh: true });
  const tile = page.locator('.dl-tile', { hasText: 'Runs, last 12 h' });
  const n = Number(await tile.locator('b').textContent());
  const sum = (await tile.locator('.d').textContent()).match(/\d+/g).map(Number).reduce((a, b) => a + b, 0);
  expect(sum).toBe(n);
  const failedAt = (await page.locator('.dl-card', { hasText: 'Last 12 hours' }).locator('.dl-foot-note').textContent()).match(/ci-fixer at (\d\d:\d\d)/)[1];
  // the same page load (the demo's times are relative to the load, so a reload could cross a minute)
  await page.evaluate(() => { location.hash = '#/activity?since=720'; }); await page.waitForSelector('#act-strip');
  // the runs Activity lists for the same 12 hours (desktop table or phone list): shown rows, plus the rest of Finished
  const listed = await page.evaluate(() => { const vis = (e) => e.getClientRects().length > 0; const rows = [...document.querySelectorAll('#act-tbl tr[data-href], #act-phone li a[href^="#/runs/"]')].filter(vis);
    const more = [...document.querySelectorAll('[data-act="act-more"]')].find(vis), m = more && more.textContent.match(/Show (\d+) more \((\d+) in all\)/);
    const shownDone = [...document.querySelectorAll('#act-tbl tbody.g-done tr[data-href], #act-phone .dl-runs-phone:last-of-type li')].filter(vis).length;
    const folded = [...document.querySelectorAll('#act-phone .ph-done b')].find(vis), nf = folded ? Number(folded.textContent.match(/\d+/)[0]) : 0;
    return rows.length + (m ? Number(m[2]) - shownDone : 0) + nf; });
  expect(listed).toBe(n);
  await expect(page.locator('#act-strip')).toContainText(`ci-fixer at ${failedAt}`);
});

test('anything shown as needing you is in Needs you and its count, a waiting sign-in included (power N7)', async ({ page }) => {
  await go(page, '#/overview', { fresh: true });
  const items = await page.evaluate(() => D.attention.map((a) => a.href));
  expect(items).toContain('#/browser/b-02');
  await expect(page.locator('#rail-nav [data-nav="attention"] .count')).toHaveText(String(items.length));
});

test('the folder page narrows the global list: its failure and approval are there, with the same marks (bugs M4)', async ({ page }) => {
  await go(page, '#/folders/work', { fresh: true });
  await expect(page.locator('.folder-needs')).toContainText('ci-fixer');
  await expect(page.locator('.folder-stats')).not.toContainText(/Failing\s*0/);
});

test('the signing dialog opens on itself, keeps Tab inside, and Enter twice never signs (power N5, newcomer N5)', async ({ page }) => {
  test.skip(isPhone(page), 'keyboard focus: desktop');
  await go(page, '#/bots/platform-sre/proposals/p12', { fresh: true });
  await page.locator('.prop-decide [data-d="approve"]').focus();
  await page.keyboard.press('Enter');
  await expect(page.locator('#seal-go')).toBeVisible();
  expect(await page.evaluate(() => document.activeElement.matches('.modal'))).toBe(true);
  await page.keyboard.press('Enter');
  await page.waitForTimeout(200);
  expect(await page.evaluate(() => D.proposals.find((p) => p.id === 'p12').status)).toBe('pending');
  for (let i = 0; i < 12; i++) { await page.keyboard.press('Tab'); expect(await page.evaluate(() => !!document.activeElement.closest('.modal'))).toBe(true); }
  await page.keyboard.press('Escape');
  expect(await page.evaluate(() => document.activeElement.dataset.d)).toBe('approve'); // focus returns to the opener
});

test('a double-click on Review and apply stages one bot, never a nameless second (bugs B6)', async ({ page }) => {
  test.skip(isPhone(page), 'the wizard flow is covered on desktop');
  await go(page, '#/bots/new', { fresh: true });
  await page.locator('[data-act="bw-purpose"]').first().click();
  await page.fill('#bw-name', 'qa-bot');
  for (let i = 0; i < 4; i++) { const nx = page.locator('[data-act="bw-next"]'); await nx.click(); const ok = page.locator('#cf-ok'); if (await ok.count()) await ok.click(); }
  await page.locator('[data-act="bw-create"]').dblclick();
  const bots = await page.evaluate(() => D.pending.filter((c) => c.coll === 'bots').map((c) => c.key));
  expect(bots).toEqual(['qa-bot']);
});

test('Add a connector is the searchable catalog with a git source; the instance name is prefilled (bugs B1/B2, power N6)', async ({ page }) => {
  await go(page, '#/connectors', { fresh: true });
  await page.locator('[data-act="conn-add"]').click();
  await expect(page.locator('.modal #cat-q')).toBeVisible();
  await expect(page.locator('.modal [data-act="src-add"]')).toContainText('from git');
  await expect(page.locator('.modal [data-cat="connector:github"]')).toContainText('official plugin');
  await page.fill('.modal #cat-q', 'linear');
  await page.locator('.modal [data-act="cat-pick"][data-name="linear"]').click();
  await expect(page.locator('#f-name')).toHaveValue('linear');
});

test('one empty choice per folder picker (bugs B3)', async ({ page }) => {
  await go(page, '#/folders/work', { fresh: true });
  await page.locator('[data-act="f-new"]').first().click();
  const opts = await page.locator('.modal select[data-k="parent"] option').allTextContents();
  expect(opts.filter((o) => /none|top level/i.test(o))).toEqual(['top level']);
});

test('a bot\'s reach is said once: a verb it may never run says never in its table too (newcomer N1, power N15)', async ({ page }) => {
  await go(page, '#/bots/platform-sre/access', { fresh: true });
  const row = page.locator('.tbl tr', { hasText: 'gh.delete_*' });
  await expect(row.locator('td[data-l="what it does"]')).toContainText('never');
  await expect(page.locator('.cang[data-s="fail"]')).toContainText('Delete in GitHub');
});

test('a connection says which bots reach it, from their grants (power N4)', async ({ page }) => {
  await go(page, '#/connectors', { fresh: true });
  const row = page.locator('.conn-row', { hasText: 'tf-apply' });
  await expect(row).toContainText('used by 1 bot');
  await expect(row.locator('[title]')).toHaveAttribute('title', 'reached by platform-sre (asks first)');
});

test('the approval names who can approve, from the bot\'s operators (power N14)', async ({ page }) => {
  await go(page, '#/bots/platform-sre/proposals/p12', { fresh: true });
  await expect(page.locator('.prop-decide .hint')).toHaveText('Only ops can approve.');
});

test('the review check counts what changed: a one-field team edit is one node, and nothing else moves (power N2/N3)', async ({ page }) => {
  test.skip(isPhone(page), 'desktop form');
  await go(page, '#/teams/platform', { fresh: true });
  await page.locator('[data-act="team-edit"]').first().click();
  await page.locator('.modal button[data-v="observe"]').first().click();
  await page.locator('.modal .modal-foot .btn.primary').click();
  await page.waitForURL(/#\/config\/apply/);
  await expect(page.locator('#chk-state')).not.toContainText('running');
  await expect(page.locator('.check-row, li', { hasText: 'Node-level edit' }).first()).toContainText('1 node changed (teams.platform.autonomy.ceiling)');
  await expect(page.locator('.diff').first()).not.toContainText('promotion');
});

test('the enforced egress allowlist (deny: true with egress) validates; advisory egress is flagged (power N1)', async ({ page }) => {
  await go(page, '#/overview', { fresh: true });
  const msgs = await page.evaluate(() => { const y = (iso) => `name: t\non: manual\nsteps:\n  - id: a\n    prompt: hi\n    isolation: ${iso}\n`;
    const v = (iso) => WF.validate(y(iso)).map((i) => `${i.level}: ${i.msg}`);
    return { enforced: v('{ mode: namespace, network: { egress: ["api.github.com:443"], deny: true } }'), advisory: v('{ mode: namespace, network: { egress: ["api.github.com:443"] } }') }; });
  expect(msgs.enforced.filter((m) => /egress/.test(m))).toEqual([]);
  expect(msgs.advisory.join(' ')).toContain('advisory only');
});

test('the phone header names the section on every page, followed in the app (bugs B7, newcomer N4)', async ({ page }) => {
  test.skip(!isPhone(page), 'phone only');
  await go(page, '#/overview', { fresh: true });
  for (const [tab, title] of [['activity', 'Activity'], ['bots', 'Bots'], ['attention', 'Needs you'], ['overview', 'Home']]) {
    await page.locator(`#tabbar [data-nav="${tab}"]`).click();
    await expect(page.locator('#ph-title')).toHaveText(title);
  }
});

test('on a phone an approval stands alone: no bot header, its title and why above a one-row bar (bugs M5, newcomer N7)', async ({ page }) => {
  test.skip(!isPhone(page), 'phone only');
  await go(page, '#/attention', { fresh: true });
  await page.locator('a[href="#/bots/platform-sre/proposals/p12"]').first().click();
  await expect(page.locator('.dl-bothead')).toBeHidden();
  const title = await page.locator('.dl-ptitle').boundingBox(), bar = await page.locator('.prop-decide').boundingBox();
  expect(title.y + title.height).toBeLessThan(bar.y);
  const btns = await page.locator('.prop-decide .btn').evaluateAll((bs) => bs.map((b) => Math.round(b.getBoundingClientRect().top)));
  expect(new Set(btns).size).toBe(1); // Approve and Deny share one row
});

test('page content is never hidden behind an entrance animation (brand B3, bugs M6)', async ({ page }) => {
  await go(page, '#/activity', { fresh: true });
  const ops = await page.evaluate(() => [...document.querySelectorAll('.page > *')].map((x) => getComputedStyle(x).opacity));
  expect(ops.every((o) => o === '1')).toBe(true);
  expect(await page.evaluate(() => [...document.styleSheets].flatMap((s) => { try { return [...s.cssRules]; } catch (e) { return []; } }).filter((r) => r.name === 'rise').map((r) => r.cssText).join(''))).not.toContain('opacity');
});

test('no downloads anywhere: export is retired (bugs M15)', async ({ page }) => {
  for (const r of ['#/runs/r-7ef0', '#/logs', '#/logs/audit', '#/secrets/env', '#/activity']) {
    await go(page, r, { fresh: true });
    await expect(page.locator('#main').getByRole('button', { name: /download|export/i })).toHaveCount(0);
  }
});

test('a failed run offers the step that made the change first, the failing check second (newcomer N10, power N17)', async ({ page }) => {
  await go(page, '#/runs/r-7ef0', { fresh: true });
  await expect(page.locator('.dl-actions .btn.primary[data-act="retry"]')).toContainText('Retry from “fix”');
});

test('a running run streams its own workflow\'s log, and its time column never wraps (bugs M2/M3)', async ({ page }) => {
  await go(page, '#/runs/r-7f29', { fresh: true });
  await expect(page.locator('#log')).not.toContainText('TestRefund');
  await expect(page.locator('#log')).toContainText('platform-sre');
  const wraps = await page.locator('#log .ln i').evaluateAll((is) => is.filter((i) => i.getClientRects().length > 1 || i.getBoundingClientRect().height > 24).length);
  expect(wraps).toBe(0);
});

test('the step picker says what each action does, reads first, and lists each built-in once (newcomer N2)', async ({ page }) => {
  await go(page, '#/workflows/ci-fixer', { fresh: true });
  await page.locator('[data-act="wf-add"]').first().click();
  const gh = page.locator('.pk-group', { hasText: 'GitHub' }).first();
  const risks = await gh.locator('.pk-risk').allTextContents();
  expect(risks.length).toBeGreaterThan(2);
  const order = { 'reads only': 0, 'changes things': 1, 'high impact': 2 };
  expect(risks.map((r) => order[r])).toEqual([...risks.map((r) => order[r])].sort());
  const builtins = await page.locator('.pk-group', { hasText: 'Built-in' }).first().locator('.pk-item b').allTextContents();
  expect(builtins).not.toContain('Wait for a condition');
});

test('a glossary term never leaks markup into a cell (power N21)', async ({ page }) => {
  await go(page, '#/workflows', { fresh: true });
  await expect(page.locator('#main')).not.toContainText('data-term=');
});

test('disabled buttons use the disabled tokens at full opacity (brand M5)', async ({ page }) => {
  await go(page, '#/workflows/ci-fixer', { fresh: true });
  const b = page.locator('[data-act="wf-deploy"][aria-disabled="true"], .btn[aria-disabled="true"]').first();
  expect(await b.evaluate((el) => getComputedStyle(el).opacity)).toBe('1');
});

test('page titles match the nav (brand M11)', async ({ page }) => {
  for (const [r, t] of [['#/connectors', 'Connections'], ['#/extensions', 'Packs'], ['#/secrets', 'Secrets']]) {
    await go(page, r, { fresh: true });
    await expect(page.locator('#main h1').first()).toHaveText(t);
  }
});

test('no risk ladder or escalating tier capsules anywhere: Safety, team ceilings, a bot\'s access (bugs B4, brand B2)', async ({ page }) => {
  for (const r of ['#/safety', '#/teams/platform/defaults', '#/bots/platform-sre/access']) {
    await go(page, r, { fresh: true });
    await expect(page.locator('.tier-ladder')).toHaveCount(0);
    const styled = await page.locator('.tier').evaluateAll((ts) => ts.filter((t) => { const cs = getComputedStyle(t); return parseFloat(cs.borderTopWidth) > 0 || /line-through/.test(cs.textDecorationLine); }).length);
    expect(styled).toBe(0);
  }
});

test('navigating in the app, a page is visible at once: never left at opacity 0 (brand B3, on WebKit too)', async ({ page }) => {
  await go(page, '#/overview', { fresh: true });
  for (const r of ['#/activity', '#/bots/platform-sre/proposals/p12', '#/attention', '#/workflows']) {
    await page.evaluate((h) => { location.hash = h; }, r);
    await page.waitForTimeout(300);
    const hidden = await page.evaluate(() => [...document.querySelectorAll('#main .page > *')].filter((x) => getComputedStyle(x).opacity !== '1').length);
    expect(hidden, r).toBe(0);
    await expect(page.locator('#main .page > *:visible').first()).toBeVisible();
  }
});
