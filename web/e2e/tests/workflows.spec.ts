// End-to-end tests for Workflows (design §4.4): the list, one workflow's
// recipe + config-file YAML, the dry-run Test, and creating a new workflow —
// against a REAL throwaway daemon serving the REAL embedded SPA.
import { test, expect } from '../fixtures'
import type { WorkflowSummary, WorkflowDetailResponse } from '../../src/api/types'

async function apiWorkflows(request: { get: (u: string) => Promise<{ json: () => Promise<unknown> }> }): Promise<WorkflowSummary[]> {
  const res = await request.get('/api/v1/workflows')
  return ((await res.json()) as { workflows: WorkflowSummary[] }).workflows
}

function isPhone(): boolean {
  return !!test.info().project.use.isMobile
}

async function expectNavOn(page: import('@playwright/test').Page, label: string) {
  // Workflows isn't one of the phone tab bar's five items (Shell.tsx's
  // TAB_ITEMS) — it's reached through More, which carries no "on" state of
  // its own for a sub-page. The rail (desktop) names every page, so only it
  // has something to assert here.
  if (isPhone()) return
  await expect(page.locator('.rail nav.nav a.on[aria-current="page"]')).toHaveText(label)
}

test('Workflows lists every configured trigger with its run tally', async ({ page, collector }) => {
  const wfs = await apiWorkflows(page.request)
  expect(wfs.map((w) => w.name)).toEqual(expect.arrayContaining(['deploy', 'build']))

  await page.goto('/#/workflows')
  await expect(page.locator('.dl-phead h1', { hasText: 'Workflows' })).toBeVisible()
  await expectNavOn(page, 'Workflows')

  // A row per configured workflow (grouped by source by default; still one
  // .m-row[data-wf] per workflow regardless of which group it's under).
  await expect(page.locator('.m-row[data-wf]')).toHaveCount(wfs.length)
  await expect(page.locator('.m-row[data-wf="deploy"]')).toBeVisible()

  expect(collector.consoleErrors, collector.consoleErrors.join(' | ')).toEqual([])
})

test('a workflow page shows its recipe, config file and a passing dry-run Test', async ({ page }) => {
  const res = await page.request.get('/api/v1/workflows/deploy')
  const wf = (await res.json()) as WorkflowDetailResponse
  expect(wf.nodes.length).toBeGreaterThanOrEqual(1)

  await page.goto('/#/workflows/deploy')
  await expect(page.locator('.wfb-title', { hasText: 'deploy' })).toBeVisible()

  // Recipe: the trigger card, then one step card per top-level step.
  await expect(page.locator('.trig-card')).toBeVisible()
  await expect(page.locator('.wfb-list .step-card').first()).toBeVisible()

  // The Config file tab shows the same workflow's real YAML (the `on:` line).
  await page.locator('.dl-tabs a', { hasText: 'Config file' }).click()
  await expect(page.locator('#wf-yaml')).toContainText('manual')

  // Dry-run Test resolves without dispatching anything.
  await page.locator('.wfb-bar .btn', { hasText: 'Test' }).click()
  await expect(page.locator('.toast', { hasText: 'Tested deploy' })).toBeVisible()
})

test('Show in YAML on a step switches to the config file and highlights the step', async ({ page }) => {
  await page.goto('/#/workflows/deploy')
  const card = page.locator('.wfb-list .step-card').first()
  await expect(card).toBeVisible()
  await card.locator('.sc-more').click()
  await page.locator('.menu button', { hasText: 'Show in YAML' }).click()
  await expect(page.locator('.dl-tabs a.on')).toHaveText('Config file')
  await expect(page.locator('#wf-yaml .ln.hl').first()).toBeVisible()
})

test('New workflow names a draft, the builder opens on it, and Save stages it (not applies it)', async ({ page }) => {
  // This runs against the suite's SHARED daemon: the staged change must not
  // outlive the test.
  const name = 'e2e-new-wf-' + Date.now().toString(36)
  try {
    // the old page address opens the dialog on the list
    await page.goto('/#/workflows/new')
    await expect(page.locator('.modal h2', { hasText: 'Name your workflow' })).toBeVisible()

    // An existing name is refused.
    await page.locator('#f-name').fill('deploy')
    await page.getByTestId('wf-new-submit').click()
    await expect(page.locator('.modal .ferr', { hasText: 'exists' })).toBeVisible()

    // A fresh name opens the builder on a draft; nothing is staged yet.
    await page.locator('#f-name').fill(name)
    await page.getByTestId('wf-new-submit').click()
    await expect(page.locator('.wfb-title', { hasText: name })).toBeVisible()
    await expect(page.locator('#wf-state')).toHaveText('Unsaved changes')

    // Save (Ctrl+S) stages it through config staging.
    await page.keyboard.press('Control+s')
    await expect(page.locator('.toast', { hasText: 'Saved' })).toBeVisible()
    const cfg = (await (await page.request.get('/api/v1/config')).json()) as { staged: { summary?: string }[] }
    expect(cfg.staged.some((c) => (c.summary || '').includes(name))).toBe(true)
  } finally {
    await page.request.post('/api/v1/config/clear')
    await page.evaluate((n) => localStorage.removeItem('cd-wf-draft:' + n), name).catch(() => {})
  }
})
