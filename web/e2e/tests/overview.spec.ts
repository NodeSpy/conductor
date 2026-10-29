// End-to-end tests for the dashboard Overview, run against a REAL throwaway
// daemon serving the REAL embedded SPA. Every assertion is checked against the
// daemon's own API, so the UI can't drift from the data silently.
import { test, expect } from '../fixtures'
import type { RunSummary, StatusResponse } from '../../src/api/types'
import { mkdirSync } from 'node:fs'
import path from 'node:path'

const SHOT_DIR = path.resolve(__dirname, '..', 'screenshots')

async function apiRuns(request: { get: (u: string) => Promise<{ json: () => Promise<unknown> }> }): Promise<RunSummary[]> {
  const res = await request.get('/api/v1/runs?limit=200')
  const body = (await res.json()) as { runs: RunSummary[] }
  return body.runs
}

// Data rows in the Activity table, excluding the group-head rows.
function activityRows(page: import('@playwright/test').Page) {
  return page.locator('.tbl tbody tr:not(.grp-row)')
}

test('app shell, nav and theme render with zero console errors and zero failed requests', async ({
  page,
  collector,
}) => {
  await page.goto('/')

  // Shell chrome.
  await expect(page.locator('.rail .wordmark')).toHaveText('conductor')
  await expect(page.locator('.nav-item.on[aria-current="page"]')).toHaveText('Overview')
  // On a phone the nav sits behind the menu button.
  const phone = !!test.info().project.use.isMobile
  if (phone) await page.getByTestId('menu-button').click()
  // Workflows, Activity, Bots and Browsers are all live nav links now.
  await expect(page.locator('a.nav-item', { hasText: 'Workflows' })).toBeVisible()
  await expect(page.locator('a.nav-item', { hasText: 'Activity' })).toBeVisible()
  await expect(page.locator('a.nav-item', { hasText: 'Bots' })).toBeVisible()
  await expect(page.locator('a.nav-item', { hasText: 'Browsers' })).toBeVisible()
  await expect(page.locator('.theme-btn')).toBeVisible()
  if (phone) await page.getByTestId('menu-button').click()

  // The page actually rendered its content.
  await expect(page.locator('.tbl')).toBeVisible()
  await expect(page.locator('.tiles .tile')).toHaveCount(4)

  // The SSE stream connected (the dot flips to live).
  await expect(page.locator('.conn')).toHaveAttribute('data-conn', 'live')

  // Give late assets (on-demand mono font, favicon) a moment to settle.
  await page.waitForTimeout(600)

  expect(collector.consoleErrors, `console errors: ${collector.consoleErrors.join(' | ')}`).toEqual([])
  expect(collector.failed, `failed/4xx requests: ${collector.failed.join(' | ')}`).toEqual([])
})

test('Overview renders the real run history from the daemon', async ({ page }) => {
  const runs = await apiRuns(page.request)
  expect(runs.length).toBeGreaterThanOrEqual(3) // seeded: deploy, build, deploy

  await page.goto('/')
  await expect(page.locator('.tbl')).toBeVisible()

  // Every seeded run has a durable id; assert each id shows in the table.
  for (const r of runs) {
    await expect(page.locator('.tbl td.id', { hasText: r.id })).toHaveCount(1)
  }

  // Row count matches the API exactly (all seeded runs are ok → the Finished
  // group; none exceed the 20-row initial window).
  await expect(activityRows(page)).toHaveCount(runs.length)

  // The "Runs · last 12 h" tile equals the count of recent runs the API returns.
  const now = Date.now()
  const last12 = runs.filter((r) => new Date(r.started).getTime() >= now - 12 * 3_600_000).length
  await expect(page.locator('.tiles .tile', { hasText: 'last 12' }).locator('.tile-value')).toHaveText(
    String(last12),
  )

  // The workflow names are present.
  await expect(page.locator('.tbl .cell-title', { hasText: 'deploy' }).first()).toBeVisible()
  await expect(page.locator('.tbl .cell-title', { hasText: 'build' }).first()).toBeVisible()
})

test('a live run updates the Overview over SSE without a reload', async ({ page, daemon }) => {
  await page.goto('/')
  await expect(page.locator('.tbl')).toBeVisible()
  await expect(page.locator('.conn')).toHaveAttribute('data-conn', 'live')

  const before = await activityRows(page).count()
  const tickBefore = Number(await page.locator('.col').getAttribute('data-tick'))

  // Dispatch a new run on the daemon. We never reload the page.
  daemon.trigger('deploy')

  // The SSE event bumps the live tick and the debounced refetch adds the row.
  await expect
    .poll(async () => Number(await page.locator('.col').getAttribute('data-tick')), { timeout: 10_000 })
    .toBeGreaterThan(tickBefore)
  await expect(activityRows(page)).toHaveCount(before + 1)

  // Confirm it arrived purely via SSE: the API now has one more run than the
  // row count we captured before, and the UI matches the API.
  const after = await apiRuns(page.request)
  expect(after.length).toBe(before + 1)
  await expect(activityRows(page)).toHaveCount(after.length)
})

test('/api/v1/status is reflected in the UI', async ({ page }) => {
  const res = await page.request.get('/api/v1/status')
  const st = (await res.json()) as StatusResponse

  await page.goto('/')
  await expect(page.locator('.tbl')).toBeVisible()

  // Version tile mirrors status.version.
  await expect(page.locator('.tiles .tile', { hasText: 'Version' }).locator('.tile-value')).toHaveText(st.version)

  // The Machine panel mirrors pid, config sha and dispatch state.
  await expect(page.locator('.facts div', { hasText: 'PID' }).locator('dd')).toHaveText(String(st.pid))
  await expect(page.locator('.facts div', { hasText: 'Config' }).locator('dd')).toHaveText(st.config_sha || '—')
  await expect(page.locator('.facts div', { hasText: 'Dispatch' }).locator('dd')).toHaveText(
    st.paused ? 'Paused' : 'Running',
  )
})

test('dark mode renders and the Overview screenshot is captured', async ({ page }, testInfo) => {
  await page.goto('/')
  await expect(page.locator('.tbl')).toBeVisible()
  await page.waitForTimeout(300)

  // OS is dark (both projects set colorScheme: dark) → the page paints dark.
  const dark = await page.evaluate(() => matchMedia('(prefers-color-scheme: dark)').matches)
  expect(dark).toBe(true)
  const bg = await page.evaluate(() => getComputedStyle(document.body).backgroundColor)
  const rgb = (bg.match(/\d+(\.\d+)?/g) ?? []).map(Number)
  const lum = (0.2126 * rgb[0] + 0.7152 * rgb[1] + 0.0722 * rgb[2]) / 255
  expect(lum, `body background ${bg} should be dark`).toBeLessThan(0.5)

  // Durable evidence: a full-page Overview screenshot per project (desktop +
  // iPhone), captured whether or not the responsive guard below holds.
  mkdirSync(SHOT_DIR, { recursive: true })
  await page.screenshot({ path: path.join(SHOT_DIR, `overview-${testInfo.project.name}.png`), fullPage: true })
})

test('no horizontal overflow at the viewport width', async ({ page }) => {
  // The app shell collapses to a phone layout under 860px (the rail becomes a
  // top bar, the tiles go two-up) and the Activity table scrolls inside its own
  // wrapper rather than dragging the page wide — so there is zero horizontal
  // overflow at iPhone-15 width (390px) as well as on desktop. This used to be
  // an expected failure on webkit-iphone15 (the Phase-1 layout overflowed
  // ~312px); it is now a hard pass on every project.
  await page.goto('/')
  await expect(page.locator('.tbl')).toBeVisible()
  await page.waitForTimeout(200)

  const overflow = await page.evaluate(
    () => document.documentElement.scrollWidth - document.documentElement.clientWidth,
  )
  expect(overflow, `horizontal overflow of ${overflow}px`).toBeLessThanOrEqual(1)
})
