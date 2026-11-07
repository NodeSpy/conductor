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

  // Recipe: one step card per node, in the default tab.
  await expect(page.locator('.step-card')).toHaveCount(wf.nodes.length)

  // The Config file tab shows the same workflow's real YAML (the `on:` line).
  await page.locator('.dl-tabs a', { hasText: 'Config file' }).click()
  await expect(page.locator('#wf-yaml')).toContainText('manual')

  // Dry-run Test resolves without dispatching anything.
  await page.locator('.btn', { hasText: 'Test' }).click()
  await expect(page.locator('.banner', { hasText: 'Resolves' })).toBeVisible()
})

test('showing a step in the config file switches tabs and highlights its line', async ({ page }) => {
  const res = await page.request.get('/api/v1/workflows/deploy')
  const wf = (await res.json()) as WorkflowDetailResponse
  const first = wf.nodes[0]
  test.skip(!first?.line, 'this workflow\'s first step carries no source line')

  await page.goto('/#/workflows/deploy')
  await expect(page.locator('.step-card').first()).toBeVisible()

  // Opening a step card, then "Show in the config file", switches to the
  // Config file tab and highlights that step's real YAML line.
  await page.locator('.step-card .sc-head').first().click()
  await page.locator('.step-card .sc-body .btn', { hasText: 'Show in the config file' }).first().click()
  await expect(page.locator('.dl-tabs a.on')).toHaveText('Config file')
  await expect(page.locator(`#wf-yaml [data-ln="${first.line}"]`)).toHaveClass(/hl/)
})

test('creating a workflow validates its name and stages it, not applies it', async ({ page }) => {
  // This runs against the suite's SHARED daemon (not an isolated one), and
  // every project in the run shares it too — so the staged-but-unapplied
  // change this test makes must not outlive it, or the next project's run of
  // this same test (or any other test reading staged state) inherits it.
  try {
    await page.goto('/#/workflows/new')
    await expect(page.locator('h1', { hasText: 'Build a workflow' })).toBeVisible()

    // An existing name is refused before anything is staged.
    await page.locator('#wf-new-name').fill('deploy')
    await page.locator('#wf-new-name').blur()
    await expect(page.locator('.ferr', { hasText: 'exists' })).toBeVisible()
    await page.locator('.btn.primary', { hasText: 'Create' }).click()
    await expect(page.locator('.banner')).toHaveCount(0)

    // A fresh, valid name stages a new manual trigger — review it, don't apply.
    const name = 'e2e-new-wf-' + Date.now().toString(36)
    await page.locator('#wf-new-name').fill(name)
    await page.locator('.btn.primary', { hasText: 'Create' }).click()
    await expect(page.locator('.banner', { hasText: name })).toBeVisible()
    await expect(page.locator('.banner', { hasText: 'staged, not applied' })).toBeVisible()
  } finally {
    await page.request.post('/api/v1/config/clear')
  }
})
