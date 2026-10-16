// The workflow builder: a vertical list (trigger first, ordered steps,
// parallel: as a fork/join), a searchable picker, generated step forms, the
// reference picker, per-step Test against a past event, two-way YAML sync,
// undo, validation, and Save → Deploy through the config diff.
const { test, expect, go, applyStaged, isPhone } = require('./fixtures');

const yaml = (page) => page.locator('#wf-yaml .ced-ta').inputValue();
const pick = async (page, q, key) => { await page.fill('#pk-q', q); await page.locator(`.pk-item[data-key="${key}"]`).click(); };
const card = (page, id) => page.locator(`[data-card="${id}"]`);
const open = async (page, id) => { if (!(await card(page, id).getAttribute('class')).includes('open')) await page.locator(`[data-act="wf-toggle"][data-id="${id}"]`).click(); };

test('build a workflow from scratch through the picker, then deploy it', async ({ page }) => {
  await go(page, '#/workflows');
  await page.locator('[data-act="wf-new"]').click();
  await page.fill('#f-name', 'issue-labeler');
  await page.locator('#gen-submit').click();
  await expect(page).toHaveURL(/#\/workflows\/issue-labeler/);
  await expect(page.locator('#wf-state')).toContainText('unsaved draft');
  await page.locator('[data-act="wf-trig-pick"]').click();
  await pick(page, 'issue opened', 'on:gh.issue_opened');
  await expect(page.locator('#card-\\$trigger')).toContainText('gh.issue_opened');
  // + after the trigger → a connector verb, found by search
  await page.locator('.spine-add .plus[data-loc="steps"][data-i="0"]').click();
  await pick(page, 'label', 'verb:gh.label');
  await expect(card(page, 'label')).toBeVisible();
  await expect(card(page, 'label')).toContainText('options.labels is required');
  await page.locator('[data-wf-path="options.labels"] .list-add').fill('needs-triage');
  await page.locator('[data-wf-path="options.labels"] .list-add').press('Enter');
  // + after it → a built-in agent step, filled in its generated form
  await page.locator('.spine-add .plus[data-loc="steps"][data-i="1"]').click();
  await pick(page, 'agent', 'kind:agent');
  await page.locator('#wf-steps_1-prompt').fill('Summarize issue {{.title}} for the triage channel.');
  await page.locator('#wf-steps_1-runtime').selectOption('claude-cli');
  await expect.poll(() => yaml(page)).toContain('Summarize issue {{.title}}');
  const y = await yaml(page);
  expect(y).toMatch(/on: gh\.issue_opened[\s\S]*- id: label\n\s+uses: gh\.label[\s\S]*- id: agent\n\s+type: agent/);
  await expect(page.locator('[data-act="wf-deploy"]')).not.toHaveAttribute('aria-disabled', 'true');
  await page.locator('[data-act="wf-save"]').click();
  await expect(page.locator('#wf-state')).toContainText('saved · not deployed');
  await page.locator('[data-act="wf-deploy"]').click();
  await expect(page.locator('.diff')).toContainText('name: issue-labeler');
  await applyStaged(page);
  await go(page, '#/workflows');
  await expect(page.locator('tr', { hasText: 'issue-labeler' })).toContainText('gh.issue_opened');
});

test('insert a step between two others, keeping comments', async ({ page }) => {
  await go(page, '#/workflows/ci-fixer');
  await page.locator('.spine-add .plus[data-loc="steps"][data-i="1"]').click();
  await pick(page, 'log', 'kind:log');
  const y = await yaml(page);
  expect(y.indexOf('- log:')).toBeGreaterThan(y.indexOf('id: logs'));
  expect(y.indexOf('- log:')).toBeLessThan(y.indexOf('id: triage'));
  expect(y).toContain('# never touch drafts');
  expect(y).toContain('model: fixer                   # fleet: sonnet, then opus on retry');
});

test('set a field from the reference picker, and by autocomplete', async ({ page }) => {
  await go(page, '#/workflows/ci-fixer');
  await open(page, 'comment');
  await page.locator('#wf-steps_5-options_body').fill('');
  await page.locator('[data-act="wf-ref"][data-target="wf-steps_5-options_body"]').click();
  await page.locator('.dleaf.ins[data-path="push.sha"]').click();
  await expect(page.locator('#wf-steps_5-options_body')).toHaveValue('{{.push.sha}}');
  await expect.poll(() => yaml(page)).toContain('body: "{{.push.sha}}"');
  // autocomplete: typing {{.tri offers triage.*
  await page.locator('#wf-steps_5-options_body').press('End');
  await page.locator('#wf-steps_5-options_body').pressSequentially(' {{.tri');
  await expect(page.locator('#wf-ac')).toContainText('triage.kind');
  await page.locator('#wf-steps_5-options_body').press('Enter');
  await expect(page.locator('#wf-steps_5-options_body')).toHaveValue('{{.push.sha}} {{.triage}}');
});

test('test a step against a past event; its output feeds the next step\'s picker', async ({ page }) => {
  await go(page, '#/workflows/ci-fixer');
  await page.locator('[data-on-change="wf-ev"]').selectOption('ev-1288');
  await open(page, 'triage');
  await page.locator('[data-act="wf-test"][data-id="triage"]').click();
  await expect(card(page, 'triage').locator('.results .tpill.ok')).toBeVisible();
  await expect(card(page, 'triage').locator('.results')).toContainText('test_failure');
  await expect(card(page, 'logs').locator('.sc-head .tpill.ok')).toBeVisible();
  await open(page, 'fix');
  await page.locator('[data-act="wf-ref"][data-target="wf-steps_2-prompt"]').click();
  const g = page.locator('.dgroup[data-g="triage"]');
  await expect(g).toContainText('actual');
  await expect(g).toContainText('test_failure');
  await page.locator('.modal [data-close]').first().click();
  // a failing step shows its error inline
  await page.locator('[data-on-change="wf-ev"]').selectOption('ev-471');
  await open(page, 'test');
  await page.locator('[data-act="wf-test"][data-id="test"]').click();
  await expect(card(page, 'test').locator('.callout.danger', { hasText: 'Test failed' })).toContainText('exit status 1');
  await expect(card(page, 'fix').locator('.sc-head .tpill.skip')).toBeVisible();
});

test('add a parallel group and a branch', async ({ page }) => {
  await go(page, '#/workflows/ci-fixer');
  await page.locator('.spine-add .plus[data-loc="steps"][data-i="6"]').click();
  await pick(page, 'parallel', 'kind:parallel');
  const par = page.locator('.par-card');
  await expect(par).toBeVisible();
  await expect(par.locator('.lane:not(.add-lane)')).toHaveCount(2);
  await expect.poll(() => yaml(page)).toMatch(/- parallel:\n\s+- - id: parallel-a/);
  await par.locator('[data-act="wf-br-add"]').click();
  await expect(par.locator('.lane:not(.add-lane)')).toHaveCount(3);
  // add a step inside branch A
  await par.locator('.lane').first().locator('.spine-add .plus').last().click();
  await pick(page, 'post a message', 'verb:slack-ops.post');
  await expect(par.locator('.lane').first()).toContainText('slack-ops.post');
  expect(await yaml(page)).toMatch(/- - id: parallel-a[\s\S]*- id: post\n\s+uses: slack-ops\.post/);
});

test('edit the YAML and see the list update; undo a delete; keyboard reorder', async ({ page }) => {
  await go(page, '#/workflows/ci-fixer');
  if (isPhone(page)) await page.locator('[data-act="wf-pane"][data-v="yaml"]').click();
  const ta = page.locator('#wf-yaml .ced-ta');
  await ta.fill((await ta.inputValue()).replace('- id: push', '- id: ship').replace('{{.push.sha}}', '{{.ship.sha}}'));
  await expect(card(page, 'ship')).toHaveCount(1);
  await expect(card(page, 'push')).toHaveCount(0);
  // a YAML error is reported with its line; the list keeps the last valid version
  await ta.fill((await ta.inputValue()).replace('steps:', 'steps: [oops'));
  await expect(page.locator('#wf-issues')).toContainText('error');
  await expect(card(page, 'ship')).toHaveCount(1);
  await page.locator('[data-act="wf-undo"]').click();
  if (isPhone(page)) await page.locator('[data-act="wf-pane"][data-v="steps"]').click();
  await page.locator('[data-act="wf-menu"][data-id="test"]').click();
  await page.locator('#pop-menu [data-act="wf-del-step"]').click();
  await expect(card(page, 'test')).toHaveCount(0);
  await page.locator('[data-act="wf-undo"]').click();
  await expect(card(page, 'test')).toHaveCount(1);
  await page.locator('[data-act="wf-redo"]').click();
  await expect(card(page, 'test')).toHaveCount(0);
  await page.locator('[data-act="wf-undo"]').click();
  await page.locator('[data-card="logs"] .sc-head').focus();
  await page.keyboard.press('Alt+ArrowDown');
  const y = await yaml(page);
  expect(y.indexOf('id: triage')).toBeLessThan(y.indexOf('id: logs'));
});

test('drag a step to reorder it', async ({ page }) => {
  test.skip(isPhone(page), 'touch uses Move up/down and Alt+arrows; drag is a mouse affordance');
  await go(page, '#/workflows/ci-fixer');
  await page.locator('[data-act="wf-expand"][data-v="0"]').click(); // all plates in view
  await card(page, 'comment').locator('.sc-head').dragTo(card(page, 'logs'), { targetPosition: { x: 40, y: 6 } });
  const y = await yaml(page);
  expect(y.indexOf('id: comment')).toBeLessThan(y.indexOf('id: logs'));
});

test('inline validation blocks deploy; a fix deploys through the diff with comments kept', async ({ page }) => {
  await go(page, '#/workflows/ci-fixer');
  await open(page, 'fix');
  await page.locator('#wf-steps_2-model').fill('gpt-x');
  await expect(card(page, 'fix').locator('.tpill.fail')).toBeVisible();
  await expect(card(page, 'fix').locator('.ferr')).toContainText('no fleet in models:');
  await expect(page.locator('[data-act="wf-deploy"]')).toHaveAttribute('aria-disabled', 'true');
  await page.locator('#wf-steps_2-model').fill('claude-opus-5');
  await expect(card(page, 'fix').locator('.tpill.fail')).toHaveCount(0);
  await page.locator('[data-act="wf-deploy"]:not([aria-disabled="true"])').click();
  await expect(page.locator('.diff .dl.add')).toContainText('model: claude-opus-5           # fleet: sonnet, then opus on retry');
  await applyStaged(page);
  await go(page, '#/workflows/ci-fixer');
  await expect(page.locator('#wf-state')).toContainText('live');
  expect(await yaml(page)).toContain('model: claude-opus-5');
});

test('pack-owned workflow: structure is read-only, field edits become overrides', async ({ page }) => {
  await go(page, '#/workflows/pr-review');
  await expect(page.locator('.spine-add .plus').first()).toHaveAttribute('aria-disabled', 'true');
  await open(page, 'review');
  await page.locator('[data-wf-path="isolation.mode"]').first().waitFor({ state: 'attached' }).catch(() => {});
  await page.locator('[data-act="wf-seg"][data-path="isolation.mode"][data-v="container"]').click();
  await page.locator('[data-act="wf-save"]').click();
  await page.locator('[data-act="wf-deploy"]').click();
  await expect(page.locator('.diff')).toContainText('review-flow/review');
  await applyStaged(page);
  expect(await page.evaluate(() => D.packs.find((p) => p.name === 'review').cfg.steps['review-flow/review'])).toMatchObject({ isolation: { mode: 'container' } });
});
