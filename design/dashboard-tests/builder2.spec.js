// The builder shows what the YAML says: workflow hooks in their own lane, keys the form doesn't model as a
// "+N keys only in YAML" badge with an editor, and Test can use a pasted payload (and says when history has
// no event for the trigger). (Riley M7, M8)
const { test, expect, go } = require('./fixtures');
const yaml = (page) => page.evaluate(() => document.querySelector('#wf-yaml .ced-ta').value);

test('hooks: add an on-fail Slack post in the hooks lane; it lands in hooks:', async ({ page }) => {
  await go(page, '#/workflows/ci-fixer', { fresh: true });
  await expect(page.locator('#hooks-lane')).toContainText('Hooks');
  await page.locator('[data-act="wf-hook-add"][data-at="fail"]').click();
  await page.fill('#pk-q', 'post'); await page.locator('.pk-item[data-key="verb:slack-ops.post"]').click();
  await expect(page.locator('#hooks-lane .hook-row')).toContainText('slack-ops.post');
  await expect.poll(() => yaml(page)).toMatch(/hooks:\n\s+- at: fail\n\s+uses: slack-ops.post/);
});

test('keys only in YAML are shown and editable in place', async ({ page }) => {
  await go(page, '#/workflows/ci-fixer', { fresh: true });
  const ta = page.locator('#wf-yaml .ced-ta');
  await ta.evaluate((el) => { el.value = el.value.replace('    runtime: claude-cli\n', '    runtime: claude-cli\n    archive_when_done: false\n    output_schema: { type: object, properties: { summary: { type: string } } }\n'); el.dispatchEvent(new Event('input', { bubbles: true })); });
  await page.locator('[data-act="wf-toggle"][data-id="fix"]').first().click();
  const badge = page.locator('#card-fix .yaml-only');
  await expect(badge).toContainText('only in YAML');
  await expect(badge).toContainText('archive_when_done');
  await expect(page.locator('#card-fix')).toContainText('returns summary');
  await badge.locator('[data-act="wf-yaml-sub"]').click();
  await page.fill('#f-y', 'archive_when_done: true\n');
  await page.locator('#gen-submit').click();
  await expect.poll(() => yaml(page)).toContain('archive_when_done: true');
});

test('Test can use a pasted payload; a trigger with no past events says so', async ({ page }) => {
  await go(page, '#/workflows/ci-fixer', { fresh: true });
  const sel = page.locator('[data-on-change="wf-ev"]');
  await sel.selectOption('__paste__');
  await expect(page.locator('#f-json')).toHaveValue(/repo/);
  await page.locator('#gen-submit').click();
  await expect(sel.locator('option:checked')).toContainText('pasted gh.check_failed payload');
  await go(page, '#/workflows', { fresh: true });
  await page.locator('[data-act="wf-new"]').click();
  await page.fill('#f-name', 'pr-watch'); await page.locator('#gen-submit').click();
  await page.locator('#card-\\$trigger .sc-head, [data-id="$trigger"]').first().click().catch(() => {});
  await page.locator('#wf-yaml .ced-ta').evaluate((el) => { el.value = el.value.replace(/^on:.*$/m, '') + '\non: gh.pull_request\n'; el.dispatchEvent(new Event('input', { bubbles: true })); });
  await expect(sel.locator('option[disabled]')).toContainText('no gh.pull_request events in the last 14 days');
});
