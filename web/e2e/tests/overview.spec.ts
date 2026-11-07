// End-to-end tests for the dashboard Overview (Home), run against a REAL
// throwaway daemon serving the REAL embedded SPA. Every assertion is checked
// against the daemon's own API, so the UI can't drift from the data silently.
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

// The Home "At a glance" tiles.
function glanceTile(page: import('@playwright/test').Page, label: string) {
  return page.locator('.dl-glance .dl-tile', { hasText: label })
}

// Waits for the SSE stream to report live: the top-bar connection cell only
// renders while the stream isn't live (Shell.tsx's ThemeCell), so "live" is
// the absence of that cell.
async function expectLive(page: import('@playwright/test').Page) {
  await expect(page.getByTestId('conn-bar')).toBeHidden()
}

test('app shell, nav and theme render with zero console errors and zero failed requests', async ({
  page,
  collector,
}) => {
  await page.goto('/')

  // Shell chrome.
  await expect(page.locator('.rail .wordmark')).toHaveText('conductor')
  await expect(page.locator('.rail nav.nav a.on[aria-current="page"]')).toHaveText('Home')
  const phone = !!test.info().project.use.isMobile
  if (phone) {
    // On a phone the rail is hidden entirely; the tab bar is the nav.
    await expect(page.locator('.rail')).toBeHidden()
    await expect(page.locator('nav.tabbar')).toBeVisible()
    await expect(page.locator('nav.tabbar a')).toHaveCount(5)
    await expect(page.locator('nav.tabbar a[data-nav=""]')).toHaveClass(/on/)
  } else {
    // Workflows, Activity, Bots and Browsers are all live nav links now.
    await expect(page.locator('.rail nav.nav a', { hasText: 'Workflows' })).toBeVisible()
    await expect(page.locator('.rail nav.nav a', { hasText: 'Activity' })).toBeVisible()
    await expect(page.locator('.rail nav.nav a', { hasText: 'Bots' })).toBeVisible()
    await expect(page.locator('.rail nav.nav a', { hasText: 'Browsers' })).toBeVisible()
  }
  // The theme toggle lives in the account menu now, not a top-bar button of
  // its own (CONFORMANCE: the account menu is the one place theme lives).
  await expect(page.locator('button.op-btn')).toBeVisible()
  await page.locator('button.op-btn').click()
  await expect(page.locator('.menu[role="menu"] button', { hasText: /Light theme|Dark theme/ })).toBeVisible()
  await page.keyboard.press('Escape')

  // The page actually rendered its content.
  await expect(page.locator('.dl-glance .dl-tile')).toHaveCount(4)
  await expect(page.locator('.dl-needs-card')).toBeVisible()

  // The SSE stream connected (the connection cell disappears once live).
  await expectLive(page)

  // Give late assets (on-demand mono font, favicon) a moment to settle.
  await page.waitForTimeout(600)

  expect(collector.consoleErrors, `console errors: ${collector.consoleErrors.join(' | ')}`).toEqual([])
  expect(collector.failed, `failed/4xx requests: ${collector.failed.join(' | ')}`).toEqual([])
})

test('Home renders the real run history from the daemon', async ({ page }) => {
  const runs = await apiRuns(page.request)
  expect(runs.length).toBeGreaterThanOrEqual(3) // seeded: deploy, build, deploy

  await page.goto('/')
  await expect(page.locator('.dl-glance')).toBeVisible()

  // The "Runs, last 12 h" tile equals the count of recent runs the API
  // returns, and its detail line breaks them down by outcome.
  const now = Date.now()
  const last12 = runs.filter((r) => new Date(r.started).getTime() >= now - 12 * 3_600_000)
  await expect(glanceTile(page, 'Runs, last 12 h').locator('b')).toHaveText(String(last12.length))

  // The Running tile mirrors the API's running count.
  const running = runs.filter((r) => r.status === 'running')
  await expect(glanceTile(page, 'Running').locator('b')).toHaveText(String(running.length))

  // Running now (if any are running) names each one, otherwise the section
  // doesn't render at all (RunningNow returns null).
  if (running.length) {
    for (const r of running.slice(0, 3)) {
      await expect(page.locator('.dl-running .dl-row-a', { hasText: r.workflow }).first()).toBeVisible()
    }
  } else {
    await expect(page.locator('.dl-running')).toHaveCount(0)
  }

  // The last-12-hours chart renders the same total the API returns.
  await expect(page.locator('.dl-hours')).toBeVisible()
  await expect(page.locator('.hb-legend')).toContainText(String(runs.filter((r) => r.status === 'ok').length))
})

test('a live run updates Home over SSE without a reload', async ({ page, daemon }) => {
  await page.goto('/')
  await expect(page.locator('.dl-glance')).toBeVisible()
  await expectLive(page)

  const before = await apiRuns(page.request)
  const tickBefore = Number(await page.locator('[data-tick]').getAttribute('data-tick'))

  // Dispatch a new run on the daemon. We never reload the page.
  daemon.trigger('deploy')

  // The SSE event bumps the live tick and the debounced refetch updates the
  // "Runs, last 12 h" tile.
  await expect
    .poll(async () => Number(await page.locator('[data-tick]').getAttribute('data-tick')), { timeout: 10_000 })
    .toBeGreaterThan(tickBefore)

  // Confirm it arrived purely via SSE: the API now has one more run than
  // before, and the tile matches the API.
  const after = await apiRuns(page.request)
  expect(after.length).toBe(before.length + 1)
  const now = Date.now()
  const last12 = after.filter((r) => new Date(r.started).getTime() >= now - 12 * 3_600_000).length
  await expect(glanceTile(page, 'Runs, last 12 h').locator('b')).toHaveText(String(last12))
})

test('/api/v1/status is reflected in the UI', async ({ page }) => {
  const res = await page.request.get('/api/v1/status')
  const st = (await res.json()) as StatusResponse

  await page.goto('/')
  await expect(page.locator('.dl-glance')).toBeVisible()

  // The machine's health and dispatch state now show on the rail's host card
  // (Home no longer carries its own "For your information" panel): paused
  // reads as a plain warning line, running reads as healthy with an uptime.
  const line = page.locator('.rail-host #host-line')
  if (st.paused) {
    await expect(line).toHaveText('Paused: nothing new starts')
    await expect(page.locator('.rail-host .host')).toHaveClass(/need/)
  } else {
    await expect(line).toHaveText(/^Healthy · up /)
    await expect(page.locator('.rail-host .host')).not.toHaveClass(/need/)
  }
})

test('dark mode renders and the Overview screenshot is captured', async ({ page }, testInfo) => {
  await page.goto('/')
  await expect(page.locator('.dl-glance')).toBeVisible()
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
  // The app shell collapses to a phone layout under 720px (the rail is
  // replaced by the bottom tab bar, the glance tiles go two-up) — so there is
  // zero horizontal overflow at iPhone-15 width (390px) as well as on
  // desktop. This used to be an expected failure on webkit-iphone15 (the
  // Phase-1 layout overflowed ~312px); it is now a hard pass on every project.
  await page.goto('/')
  await expect(page.locator('.dl-glance')).toBeVisible()
  await page.waitForTimeout(200)

  const overflow = await page.evaluate(
    () => document.documentElement.scrollWidth - document.documentElement.clientWidth,
  )
  expect(overflow, `horizontal overflow of ${overflow}px`).toBeLessThanOrEqual(1)
})
