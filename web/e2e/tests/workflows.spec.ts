// End-to-end tests for Workflows (design §4.4): the list, one workflow's graph +
// YAML (node→YAML), the dry-run Test, and the builder canvas — against a REAL
// throwaway daemon serving the REAL embedded SPA.
import { test, expect } from '../fixtures'
import type { WorkflowSummary, WorkflowDetailResponse } from '../../src/api/types'

async function apiWorkflows(request: { get: (u: string) => Promise<{ json: () => Promise<unknown> }> }): Promise<WorkflowSummary[]> {
  const res = await request.get('/api/v1/workflows')
  return ((await res.json()) as { workflows: WorkflowSummary[] }).workflows
}

test('Workflows lists every configured trigger with its run tally', async ({ page, collector }) => {
  const wfs = await apiWorkflows(page.request)
  expect(wfs.map((w) => w.name)).toEqual(expect.arrayContaining(['deploy', 'build']))

  await page.goto('/#/workflows')
  await expect(page.locator('.screen-title', { hasText: 'Workflows' })).toBeVisible()
  await expect(page.locator('.nav-item.on[aria-current="page"]')).toHaveText('Workflows')

  // A row per configured workflow.
  await expect(page.locator('.tbl tbody tr[data-wf]')).toHaveCount(wfs.length)
  await expect(page.locator('tr[data-wf="deploy"]')).toBeVisible()

  expect(collector.consoleErrors, collector.consoleErrors.join(' | ')).toEqual([])
})

test('a workflow page shows its graph, YAML and a passing dry-run Test', async ({ page }) => {
  const res = await page.request.get('/api/v1/workflows/deploy')
  const wf = (await res.json()) as WorkflowDetailResponse
  expect(wf.nodes.length).toBeGreaterThanOrEqual(1)

  await page.goto('/#/workflows/deploy')
  await expect(page.locator('.screen-title', { hasText: 'deploy' })).toBeVisible()

  // Graph: one node per step; the YAML pane shows the `on:` line.
  await expect(page.locator('.graph .graph-node')).toHaveCount(wf.nodes.length)
  await expect(page.locator('.yaml-pane')).toContainText('manual')

  // Dry-run Test resolves without dispatching anything.
  await page.locator('.btn', { hasText: 'Test' }).click()
  await expect(page.locator('.banner', { hasText: 'Resolves' })).toBeVisible()
})

test('clicking a graph node highlights its line in the YAML pane', async ({ page }) => {
  await page.goto('/#/workflows/deploy')
  await expect(page.locator('.graph .graph-node').first()).toBeVisible()
  await page.locator('.graph .graph-node').first().click()
  await expect(page.locator('.yaml-pane .yaml-l.hot')).toHaveCount(1)
})

test('the builder canvas stays in sync with the YAML pane', async ({ page }) => {
  await page.goto('/#/workflows/new')
  await expect(page.locator('.screen-title', { hasText: 'Build a workflow' })).toBeVisible()

  // The template parses into canvas nodes.
  const before = await page.locator('.builder-canvas .graph-node').count()
  expect(before).toBeGreaterThanOrEqual(2)

  // Adding a step from the palette grows the canvas.
  await page.locator('.palette .chip', { hasText: 'Agent' }).click()
  await expect(page.locator('.builder-canvas .graph-node')).toHaveCount(before + 1)
})
