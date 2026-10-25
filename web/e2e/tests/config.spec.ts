// The config-edit flow end-to-end, against a REAL conductor daemon: edit a
// setting on the Config screen, review the one-line diff, apply with the step-up
// confirm, and confirm the live config file changed on disk — comment preserved,
// nothing else touched. This uses its OWN dedicated daemon (startIsolated),
// because apply re-execs the daemon on SIGHUP and must not disturb the shared
// daemon the rest of the suite reads.
import { readFileSync } from 'node:fs'
import { test, expect } from '../fixtures'
import { startIsolated, stop, type Runtime } from '../lib/daemon'

let rt: Runtime

test.beforeAll(async () => {
  // A port distinct from the shared daemon's (DASH_E2E_PORT, default 48766).
  rt = await startIsolated(Number(process.env.DASH_E2E_PORT || 48766) + 7)
})

test.afterAll(() => {
  if (rt) stop(rt)
})

test('edit a setting → review the diff → apply → confirm', async ({ page, collector }) => {
  await page.goto(`${rt.baseURL}/#/config`)

  // The settings.greeting field is present and shows the current value.
  const field = page.locator('input[data-path="settings.greeting"]')
  await expect(field).toBeVisible()
  await expect(field).toHaveValue('hello')

  // Edit it. onChange stages the edit (blur commits).
  await field.fill('howdy')
  await field.blur()

  // It shows as staged.
  await expect(page.locator('.staged-list')).toContainText('settings.greeting')

  // Review → the one-line diff appears (one del + one add), comment preserved.
  await page.getByRole('button', { name: 'Review changes' }).click()
  const diff = page.locator('.diff')
  await expect(diff).toBeVisible()
  await expect(diff.locator('.dl.del')).toHaveCount(1)
  await expect(diff.locator('.dl.add')).toHaveCount(1)
  await expect(diff.locator('.dl.add')).toContainText('howdy')
  // The inline comment survives on the added line.
  await expect(diff.locator('.dl.add')).toContainText('inline comment')

  // Apply → confirm (step-up).
  await page.getByRole('button', { name: 'Apply…' }).click()
  await page.getByRole('button', { name: 'Confirm apply' }).click()

  // The success banner appears and names a backup.
  const ok = page.locator('.banner.ok')
  await expect(ok).toBeVisible()
  await expect(ok).toContainText('Applied')
  await expect(ok).toContainText('Backup')

  // No console errors or 4xx+ responses during the flow.
  expect(collector.consoleErrors, collector.consoleErrors.join('\n')).toEqual([])
  expect(collector.failed, collector.failed.join('\n')).toEqual([])

  // The live config file on disk now carries the change, with the comment and
  // every other line preserved byte-for-byte.
  const after = readFileSync(rt.config, 'utf8')
  expect(after).toContain('greeting: howdy   # inline comment the edit must preserve')
  expect(after).toContain('box: { use: command }') // untouched section intact
  expect(after).not.toContain('greeting: hello')
})
