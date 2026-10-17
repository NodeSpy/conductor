// usage.mode: off by default (no usage or spend UI anywhere, screens still
// complete); subscription and api show figures per provider, never a total
// across providers; plan limits and tokens only from a configured usage source.
const { test, expect, go, applyStaged } = require('./fixtures');

const ROUTES = ['#/overview', '#/runs', '#/runs/r-7f2c', '#/workflows', '#/bots', '#/folders/work', '#/teams/platform', '#/bots/platform-sre', '#/bots/platform-sre/workers'];
// the display mode is a browser preference; the source is config
const setMode = (page, cfg) => page.evaluate((c) => { window.D.prefs.usageMode = c.mode; window.D.usageCfg = c.source ? { source: c.source } : {}; window.S.save(); }, cfg);

/** Every usage-looking figure inside a [data-usage] block must sit inside one provider ([data-prov]) — or be a labelled budget counter or cloud spend. */
async function crossProviderFigures(page) {
  return page.evaluate(() => {
    const RX = /\$\d|\d+(\.\d+)?[kM]? tok\b|\d+ turns|\d+%/;
    const known = Object.keys(window.D.usage.providers), bad = [];
    document.querySelectorAll('#main [data-prov]').forEach((el) => { if (!known.includes(el.dataset.prov)) bad.push(`unknown provider "${el.dataset.prov}"`); });
    document.querySelectorAll('#main [data-figure]').forEach((el) => { if (!el.closest('[data-prov]')) bad.push(`figure outside a provider: ${el.textContent}`); });
    document.querySelectorAll('#main [data-usage]').forEach((root) => {
      if (/\b(total|overall|combined)\b/i.test(root.textContent.replace(/never combined/gi, ''))) bad.push(`a total in usage UI: ${root.textContent.slice(0, 80)}`);
      const w = document.createTreeWalker(root, NodeFilter.SHOW_TEXT);
      for (let n = w.nextNode(); n; n = w.nextNode()) {
        const p = n.parentElement;
        if (!RX.test(n.textContent) || p.closest('[data-prov], [data-budget], [data-cloud-spend], .field, .hint, [data-tip]')) continue;
        bad.push(`figure outside a provider: "${n.textContent.trim()}"`);
      }
    });
    return bad;
  });
}
async function noUsageUI(page) {
  return page.evaluate(() => {
    const main = document.querySelector('#main'), bad = [];
    if (main.querySelector('[data-usage], [data-figure], [data-prov], .win-meter')) bad.push('usage element rendered');
    const clone = main.cloneNode(true); clone.querySelectorAll('[data-cloud-spend], .field, .hint, textarea, pre, .yaml, .cm').forEach((e) => e.remove());
    const t = clone.textContent;
    if (/\$\s?\d/.test(t)) bad.push('dollar figure: ' + t.match(/.{0,40}\$\s?\d.{0,20}/)[0]);
    if (/\d+(\.\d+)?[kM]? tok(ens)?\b/.test(t)) bad.push('token figure: ' + t.match(/.{0,40}\d+(\.\d+)?[kM]? tok(ens)?\b/)[0]);
    if (/of the \w+ (window|limit)/.test(t)) bad.push('plan window');
    if (/observed by conductor/.test(t)) bad.push('usage label');
    return bad;
  });
}

test('default is off: no usage or cost UI on any screen, and the screens are still complete', async ({ page }) => {
  await go(page, '#/overview', { fresh: true });
  expect(await page.evaluate(() => window.UI.usageMode())).toBe('off');
  for (const r of ROUTES) {
    await go(page, r);
    expect(await noUsageUI(page), r).toEqual([]);
    // no hole where usage would be: the overview's side column still has its panel and four stat tiles
    if (r === '#/overview') { await expect(page.locator('.stats .stat')).toHaveCount(4); await expect(page.locator('.quiet-row.wfs summary')).toContainText('Workflows'); }
  }
  // cloud spend is a real-world action, not model usage: still visible
  await go(page, '#/bots/platform-sre/cases');
  await expect(page.locator('[data-cloud-spend]').first()).toBeVisible();
  // budget forms keep run/turn/token caps, no dollar fields
  await go(page, '#/teams/platform/defaults');
  await page.locator('[data-act="team-edit"]').first().click();
  await expect(page.locator('#f-pool_turns')).toBeVisible();
  await expect(page.locator('#f-pool_tokens')).toBeVisible();
  await expect(page.locator('#f-pool_cost')).toBeHidden();
});

test('subscription with no source: per-provider counts observed by conductor, no limits, no tokens, no totals', async ({ page }) => {
  await go(page, '#/config', { fresh: true });
  await page.locator('[data-act="usage-mode"][data-v="subscription"]').click(); // instant, no config change
  expect(await page.evaluate(() => D.pending.length)).toBe(0);
  await go(page, '#/overview');
  const cards = page.locator('.prov-card');
  await expect(cards).toHaveCount(2);
  await expect(cards.nth(0)).toContainText('Claude');
  await expect(cards.nth(1)).toContainText('Codex');
  await expect(page.locator('#main')).toContainText('observed by conductor');
  await expect(page.locator('#main')).toContainText('no usage source configured');
  await expect(page.locator('.win-meter')).toHaveCount(0);
  for (const r of ROUTES) {
    await go(page, r);
    expect(await crossProviderFigures(page), r).toEqual([]);
    const t = await page.locator('#main').textContent();
    expect(t, r).not.toMatch(/\d+(\.\d+)?[kM]? tok\b/);
    expect(t.replace(/cloud spend[^$]*\$[\d.,]+/gi, ''), r).not.toMatch(/\$\s?\d/);
  }
});

test('a usage source adds plan limits and tokens per provider, each labelled with its source', async ({ page }) => {
  await go(page, '#/overview', { fresh: true });
  await setMode(page, { mode: 'subscription' });
  await go(page, '#/config');
  await page.locator('[data-act="usage-src"]').click();
  await page.locator('#f-name').fill('llm-proxy');
  await page.locator('#f-url').fill('not a url');
  await page.locator('#gen-submit').click();
  await expect(page.locator('.modal')).toContainText('an http(s) URL');
  await page.locator('#f-url').fill('http://llm-proxy.internal:4000/usage');
  await page.locator('#gen-submit').click();
  await page.getByRole('link', { name: 'Review change' }).first().click();
  await expect(page.locator('.diff')).toContainText('llm-proxy');
  await applyStaged(page);
  await go(page, '#/overview');
  for (const p of ['claude', 'codex']) {
    const card = page.locator(`.prov-card[data-prov="${p}"]`);
    await expect(card.locator('.win-meter')).toHaveCount(2);
    await expect(card).toContainText('from llm-proxy');
    await expect(card).toContainText('tokens today');
  }
  for (const r of ROUTES) { await go(page, r); expect(await crossProviderFigures(page), r).toEqual([]); }
});

test('api mode: dollars per provider, never added together; dollar budget fields appear', async ({ page }) => {
  await go(page, '#/overview', { fresh: true });
  await setMode(page, { mode: 'api' });
  await go(page, '#/overview');
  await expect(page.locator('.prov-card[data-prov="claude"]')).toContainText('$');
  await expect(page.locator('.prov-card[data-prov="codex"]')).toContainText('$');
  const sum = await page.evaluate(() => (window.D.usage.usd.claude.today + window.D.usage.usd.codex.today).toFixed(2));
  await expect(page.locator('#main')).not.toContainText('$' + sum);
  for (const r of ROUTES) { await go(page, r); expect(await crossProviderFigures(page), r).toEqual([]); }
  await go(page, '#/teams/platform/defaults');
  await page.locator('[data-act="team-edit"]').first().click();
  await expect(page.locator('#f-pool_cost')).toBeVisible();
});

test('switching back to off removes every trace again', async ({ page }) => {
  await go(page, '#/overview', { fresh: true });
  await setMode(page, { mode: 'api', source: { name: 'llm-proxy', url: 'http://llm-proxy.internal:4000/usage' } });
  await go(page, '#/config');
  await page.locator('[data-act="usage-mode"][data-v="off"]').click();
  for (const r of ROUTES) { await go(page, r); expect(await noUsageUI(page), r).toEqual([]); }
});
