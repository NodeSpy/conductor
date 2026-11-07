// End-to-end tests for the Activity screen, run against a REAL throwaway daemon
// serving the REAL embedded SPA (design §4.2). The grouped list, the
// runs-per-hour strip with the hover tooltip, and the live SSE update path are
// all asserted against the daemon's own API so the UI can't drift silently.
//
// Two parallel layouts render the same data (Activity.tsx: a desktop grouped
// table, `.dl-tbl`, and a phone list, `.dl-phone-groups`/`.dl-runs-phone` —
// CSS shows one and hides the other at the 720px breakpoint, chosen by
// `usePhoneLayout`). The phone list also hides its Finished group behind a
// disclosure (`.ph-done`) by default, and never prints a run's id as text (the
// desktop table's `td.id` has no phone equivalent) — so a run row there is
// found by its `data-run` attribute (PhoneRow), not by visible id text.
// These helpers pick the right layout's markup per project instead of
// assuming the desktop one everywhere.
import { test, expect } from '../fixtures'
import type { Page } from '@playwright/test'
import type { RunSummary } from '../../src/api/types'

async function apiRuns(request: { get: (u: string) => Promise<{ json: () => Promise<unknown> }> }): Promise<RunSummary[]> {
  const res = await request.get('/api/v1/runs?limit=200')
  return ((await res.json()) as { runs: RunSummary[] }).runs
}

function isPhone(): boolean {
  return !!test.info().project.use.isMobile
}

// dataRows is every run row actually on screen for this project's layout. On
// phone it first opens the Finished disclosure, if there is one and it's
// closed, so a run that only lives in that group is counted too.
async function dataRows(page: Page) {
  if (!isPhone()) return page.locator('.dl-tbl tbody tr:not(.gh):not(.more)')
  const more = page.locator('.ph-done[aria-expanded="false"]')
  if (await more.count()) await more.click()
  return page.locator('.dl-runs-phone li')
}

async function rowFor(page: Page, id: string) {
  if (!isPhone()) return page.locator(`.dl-tbl tbody tr[data-run="${id}"]`)
  const row = page.locator(`.dl-runs-phone li[data-run="${id}"]`)
  if (await row.count()) return row
  // Not in Needs attention or Running now: it's in Finished, behind the
  // disclosure (PhoneGroups only renders that list once opened).
  const more = page.locator('.ph-done[aria-expanded="false"]')
  if (await more.count()) await more.click()
  return row
}

async function expectRunsVisible(page: Page) {
  if (isPhone()) {
    await expect(page.locator('.dl-phone-groups')).toBeVisible()
    await expect(page.locator('.dl-tbl')).toBeHidden()
  } else {
    await expect(page.locator('.dl-tbl')).toBeVisible()
  }
}

async function expectNavOn(page: Page) {
  if (isPhone()) {
    await expect(page.locator('nav.tabbar a[data-nav="activity"]')).toHaveClass(/on/)
  } else {
    await expect(page.locator('.rail nav.nav a.on[aria-current="page"]')).toHaveText('Activity')
  }
}

test('Activity lists every run from the daemon, grouped and filterable', async ({ page, collector }) => {
  const runs = await apiRuns(page.request)
  expect(runs.length).toBeGreaterThanOrEqual(3)

  await page.goto('/#/activity')
  await expect(page.locator('.dl-phead h1', { hasText: 'Activity' })).toBeVisible()
  await expectRunsVisible(page)
  await expectNavOn(page)

  // Every run shows, found the right way for this layout: by its id text in
  // the desktop table's id column, or by the phone row's data-run hook (the
  // phone list never prints the id as text).
  for (const r of runs) {
    await expect(await rowFor(page, r.id)).toHaveCount(1)
  }

  // The runs-per-hour strip rendered with bars and a legend (passed is green)
  // — shared markup above both layouts.
  await expect(page.locator('.dl-strip .dl-hours .hb')).not.toHaveCount(0)
  await expect(page.locator('.dl-strip .hb-legend li', { hasText: 'Passed' })).toBeVisible()

  // Free-text search narrows the list: filter to the "build" workflow.
  const buildRuns = runs.filter((r) => r.workflow === 'build')
  await page.locator('.fb-in').fill('build')
  await page.locator('.fb-in').press('Enter')
  await expect(await dataRows(page)).toHaveCount(buildRuns.length)

  // Clearing the search restores the full list.
  await page.locator('.fb-in').fill('')
  await page.locator('.fb-in').press('Enter')
  await expect(await dataRows(page)).toHaveCount(runs.length)

  expect(collector.consoleErrors, collector.consoleErrors.join(' | ')).toEqual([])
})

test('the runs-per-hour bar reveals a tooltip on hover', async ({ page }) => {
  await page.goto('/#/activity')
  await expect(page.locator('.dl-strip .dl-hours .hb')).not.toHaveCount(0)

  // Hover the last (most recent) bar; a tooltip with per-status counts appears.
  const bars = page.locator('.dl-strip .dl-hours .hb')
  await bars.last().hover()
  const tip = page.locator('.hb-tip')
  await expect(tip).toBeVisible()
  await expect(tip.locator('.hb-tip-l li', { hasText: 'Passed' })).toBeVisible()
})

test('a live run appears in Activity over SSE without a reload', async ({ page, daemon }) => {
  await page.goto('/#/activity')
  await expectRunsVisible(page)
  // The SSE stream connected: the top-bar connection cell only renders while
  // it isn't live (Shell.tsx's ThemeCell), so "live" is its absence.
  await expect(page.getByTestId('conn-bar')).toBeHidden()

  const before = await (await dataRows(page)).count()
  const tickBefore = Number(await page.locator('[data-tick]').first().getAttribute('data-tick'))

  daemon.trigger('deploy')

  await expect
    .poll(async () => Number(await page.locator('[data-tick]').first().getAttribute('data-tick')), { timeout: 10_000 })
    .toBeGreaterThan(tickBefore)
  await expect(await dataRows(page)).toHaveCount(before + 1)
})

test('clicking a run row opens its run detail', async ({ page }) => {
  const runs = await apiRuns(page.request)
  const target = runs[0]

  await page.goto('/#/activity')
  await expectRunsVisible(page)

  const row = await rowFor(page, target.id)
  if (isPhone()) await row.locator('a').click()
  else await row.click()
  await expect(page).toHaveURL(new RegExp(`#/runs/${target.id}$`))
  await expect(page.locator('.dl-phead h1')).toContainText(target.workflow)
})
