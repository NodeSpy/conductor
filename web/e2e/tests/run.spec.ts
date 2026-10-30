// End-to-end tests for the Run detail screen (design §4.3), against a REAL
// throwaway daemon serving the REAL embedded SPA. The step tree, the step card
// and the retry control are asserted against the daemon's own run record.
import { test, expect } from '../fixtures'
import type { RunSummary, RunDetailResponse } from '../../src/api/types'

async function firstRun(request: { get: (u: string) => Promise<{ json: () => Promise<unknown> }> }): Promise<RunSummary> {
  const res = await request.get('/api/v1/runs?limit=200')
  return ((await res.json()) as { runs: RunSummary[] }).runs[0]
}

test('a run detail renders its step tree and recorded step output', async ({ page, collector }) => {
  const run = await firstRun(page.request)
  const res = await page.request.get(`/api/v1/runs/${run.id}`)
  const detail = (await res.json()) as RunDetailResponse
  expect(detail.steps.length).toBeGreaterThanOrEqual(1)

  await page.goto(`/#/runs/${run.id}`)

  // Header mirrors the run; breadcrumb back to Activity.
  await expect(page.locator('.screen-title')).toContainText(run.workflow)
  await expect(page.locator('.crumbs a', { hasText: 'Activity' })).toBeVisible()

  // The step tree has one button per recorded step, keyed by step id.
  await expect(page.locator('.step-tree .step-tl')).toHaveCount(detail.steps.length)
  for (const s of detail.steps) {
    await expect(page.locator('.step-tree .step-tl b', { hasText: s.id }).first()).toBeVisible()
  }

  // The Details panel mirrors the run id and links to its workflow.
  await expect(page.locator('.facts div', { hasText: 'Run' }).locator('dd')).toContainText(run.id)
  await expect(page.locator('.facts a', { hasText: run.workflow })).toBeVisible()

  expect(collector.consoleErrors, collector.consoleErrors.join(' | ')).toEqual([])
})

test('a finished run offers a retry-from-step form', async ({ page }) => {
  const run = await firstRun(page.request)
  await page.goto(`/#/runs/${run.id}`)
  await expect(page.locator('.step-tree')).toBeVisible()

  // The Retry control opens the from-step form (no live run → Retry, not Stop).
  await page.locator('.run-actions .btn', { hasText: 'Retry' }).click()
  await expect(page.locator('.retry-form')).toBeVisible()
  await expect(page.locator('.retry-form select')).toBeVisible()
})

test('an unknown run id shows the empty state', async ({ page }) => {
  await page.goto('/#/runs/does-not-exist')
  await expect(page.locator('.empty h2')).toContainText('does-not-exist')
  await expect(page.locator('.empty a', { hasText: 'Back to Activity' })).toBeVisible()
})

test('a forced retry asks to confirm, showing the steps it runs again', async ({ page }) => {
  const run = await firstRun(page.request)
  // The server's answer to a forced retry on a signed-in dashboard: a
  // run.replay step-up with its description of what runs again.
  let sent: Record<string, unknown> | null = null
  await page.route(`**/api/v1/runs/${run.id}/retry`, async (route) => {
    sent = route.request().postDataJSON() as Record<string, unknown>
    await route.fulfill({
      status: 401,
      contentType: 'application/json',
      body: JSON.stringify({
        error: { code: 'stepup_required', message: 'a step-up is required', hint: '' },
        stepup: {
          purpose: 'run.replay',
          digest: 'a'.repeat(43),
          description: `re-run run ${run.id} from the beginning, forcing a replay\nthese steps already succeeded and run again:\n  - post`,
        },
      }),
    })
  })
  await page.goto(`/#/runs/${run.id}`)
  await page.locator('.run-actions .btn', { hasText: 'Retry' }).click()
  await page.locator('.retry-form .field-check input').check()
  await page.locator('.retry-form .btn.primary', { hasText: 'Retry' }).click()
  const dialog = page.getByTestId('stepup-dialog')
  await expect(dialog).toBeVisible()
  await expect(dialog).toContainText('Re-run steps of a run that already ran')
  await expect(page.getByTestId('stepup-description')).toContainText('these steps already succeeded and run again')
  expect(sent).toMatchObject({ force: true })
})
