// End-to-end tests for the Activity screen, run against a REAL throwaway daemon
// serving the REAL embedded SPA (design §4.2). The grouped list, the
// runs-per-hour strip with the hover tooltip, and the live SSE update path are
// all asserted against the daemon's own API so the UI can't drift silently.
import { test, expect } from '../fixtures'
import type { RunSummary } from '../../src/api/types'

async function apiRuns(request: { get: (u: string) => Promise<{ json: () => Promise<unknown> }> }): Promise<RunSummary[]> {
  const res = await request.get('/api/v1/runs?limit=200')
  return ((await res.json()) as { runs: RunSummary[] }).runs
}

function dataRows(page: import('@playwright/test').Page) {
  return page.locator('.tbl tbody tr:not(.grp-row)')
}

test('Activity lists every run from the daemon, grouped and filterable', async ({ page, collector }) => {
  const runs = await apiRuns(page.request)
  expect(runs.length).toBeGreaterThanOrEqual(3)

  await page.goto('/#/activity')
  await expect(page.locator('.screen-title', { hasText: 'Activity' })).toBeVisible()
  await expect(page.locator('.tbl')).toBeVisible()

  // The nav highlights Activity.
  await expect(page.locator('.nav-item.on[aria-current="page"]')).toHaveText('Activity')

  // Every run id shows in the table.
  for (const r of runs) {
    await expect(page.locator('.tbl td.id', { hasText: r.id })).toHaveCount(1)
  }

  // The runs-per-hour strip rendered with bars and a legend (passed is green).
  await expect(page.locator('.strip .chart .bar')).not.toHaveCount(0)
  await expect(page.locator('.strip .legend .legend-item', { hasText: 'Passed' })).toBeVisible()

  // Free-text search narrows the list: filter to the "build" workflow.
  const buildRuns = runs.filter((r) => r.workflow === 'build')
  await page.locator('.fb-input').fill('build')
  await page.locator('.fb-input').press('Enter')
  await expect(dataRows(page)).toHaveCount(buildRuns.length)

  // Clearing the search restores the full list.
  await page.locator('.fb-input').fill('')
  await page.locator('.fb-input').press('Enter')
  await expect(dataRows(page)).toHaveCount(runs.length)

  expect(collector.consoleErrors, collector.consoleErrors.join(' | ')).toEqual([])
})

test('the runs-per-hour bar reveals a tooltip on hover', async ({ page }) => {
  await page.goto('/#/activity')
  await expect(page.locator('.strip .chart .bar')).not.toHaveCount(0)

  // Hover the last (most recent) bar; a tooltip with per-status counts appears.
  const bars = page.locator('.strip .chart .bar')
  await bars.last().hover()
  const tip = page.locator('.bar-tip')
  await expect(tip).toBeVisible()
  await expect(tip.locator('.bar-tip-row', { hasText: 'Passed' })).toBeVisible()
})

test('a live run appears in Activity over SSE without a reload', async ({ page, daemon }) => {
  await page.goto('/#/activity')
  await expect(page.locator('.tbl')).toBeVisible()
  await expect(page.locator('.conn:visible')).toHaveAttribute('data-conn', 'live')

  const before = await dataRows(page).count()
  const tickBefore = Number(await page.locator('.col').getAttribute('data-tick'))

  daemon.trigger('deploy')

  await expect
    .poll(async () => Number(await page.locator('.col').getAttribute('data-tick')), { timeout: 10_000 })
    .toBeGreaterThan(tickBefore)
  await expect(dataRows(page)).toHaveCount(before + 1)
})

test('clicking a run row opens its run detail', async ({ page }) => {
  const runs = await apiRuns(page.request)
  const target = runs[0]

  await page.goto('/#/activity')
  await expect(page.locator('.tbl')).toBeVisible()

  await page.locator(`.tbl tbody tr[data-run="${target.id}"]`).click()
  await expect(page).toHaveURL(new RegExp(`#/runs/${target.id}$`))
  await expect(page.locator('.screen-title')).toContainText(target.workflow)
})
