// Packs, applied for real: Browse → Add → review → apply installs the pack
// (fetched by the apply itself, no `init`), a release tagged afterwards shows
// as an update that applies, and Remove applies too. Its own daemon (an
// apply restarts it) with a throwaway git registry of a synthetic pack.
import { existsSync, readFileSync } from 'node:fs'
import path from 'node:path'
import type { Page } from '@playwright/test'
import { test, expect } from '../fixtures'
import { startWithGitCatalog, stop, type Runtime } from '../lib/daemon'

let rt: Runtime & { registry: string; release: (name: string, version: string) => void }

test.beforeAll(async () => {
  rt = await startWithGitCatalog(Number(process.env.DASH_E2E_PORT || 48766) + 24)
})

test.afterAll(() => {
  if (rt) stop(rt)
})

test.describe.configure({ mode: 'serial' })

const vendored = () => path.join(path.dirname(rt.config), '.conductor', 'packs', 'greeter', 'conductor-pack.yaml')

// the daemon's started_at; an apply restarts it, so a new one means the
// restarted daemon (with the applied config) is the one answering
const startedAt = async (page: Page) => {
  const r = await page.request.get(`${rt.baseURL}/api/v1/status`).catch(() => null)
  return r && r.ok() ? String((await r.json()).started_at) : ''
}

// review → apply → confirm on the config page, then wait for the restart
async function applyStaged(page: Page) {
  await expect(page).toHaveURL(/#\/system\/config/)
  const before = await startedAt(page)
  await page.getByRole('button', { name: 'Review changes' }).click()
  await page.getByRole('button', { name: 'Apply…' }).click()
  await page.getByRole('button', { name: 'Confirm apply' }).click()
  await expect(page.locator('.banner.ok')).toContainText('Applied', { timeout: 60_000 })
  await expect.poll(async () => { const s = await startedAt(page); return s !== '' && s !== before }, { timeout: 60_000 }).toBe(true)
}

test('Add → apply installs the pack: fetched, vendored, its workflow live', async ({ page }) => {
  await page.goto(`${rt.baseURL}/#/packs/browse`)
  await page.locator('.cat-card', { hasText: 'greeter' }).getByRole('link', { name: 'Add' }).click()
  await expect(page.getByRole('heading', { level: 1, name: 'Install greeter' })).toBeVisible()
  // armed, so the restarted daemon runs its trigger: the proof it loaded
  await page.locator('#iw-arm-on_tick').check()
  await page.getByRole('button', { name: 'Review everything it adds' }).click()
  await applyStaged(page)

  expect(readFileSync(rt.config, 'utf8')).toContain('greeter:')
  expect(readFileSync(vendored(), 'utf8')).toContain('version: 1.0.0')
  await page.goto(`${rt.baseURL}/#/packs`)
  const row = page.locator('.pack-row[data-pack="greeter"]')
  await expect(row).toContainText('says hello on a schedule')
  await expect(row).toContainText('1.0.0')
  await expect(row).toContainText('Automation on')
  const wf = await page.request.get(`${rt.baseURL}/api/v1/workflows`)
  expect(await wf.text()).toContain('on_tick')
})

test('a new release shows as an update; Update → apply moves to it', async ({ page }) => {
  rt.release('greeter', '1.1.0')
  await page.goto(`${rt.baseURL}/#/packs`)
  const row = page.locator('.pack-row[data-pack="greeter"]')
  await expect(row).toContainText('1.1.0 available', { timeout: 30_000 })
  await row.getByRole('button', { name: 'Update to 1.1.0…' }).click()
  await page.getByRole('dialog').getByRole('button', { name: 'Stage update' }).click()
  await applyStaged(page)

  expect(readFileSync(vendored(), 'utf8')).toContain('version: 1.1.0')
  await page.goto(`${rt.baseURL}/#/packs`)
  await expect(row).toContainText('1.1.0')
  await expect(row).not.toContainText('available')
})

test('Remove → apply takes the pack and its vendored copy away', async ({ page }) => {
  await page.goto(`${rt.baseURL}/#/packs`)
  const row = page.locator('.pack-row[data-pack="greeter"]')
  await row.getByRole('button', { name: 'More for greeter' }).click()
  await page.getByRole('menuitem', { name: 'Remove this pack…' }).click()
  await page.getByRole('dialog', { name: 'Remove pack greeter?' }).getByRole('button', { name: 'Stage removal' }).click()
  await applyStaged(page)

  expect(readFileSync(rt.config, 'utf8')).not.toContain('greeter')
  expect(existsSync(vendored())).toBe(false)
  await page.goto(`${rt.baseURL}/#/packs`)
  await expect(page.getByTestId('packs-empty')).toBeVisible()
  const wf = await page.request.get(`${rt.baseURL}/api/v1/workflows`)
  expect(await wf.text()).not.toContain('on_tick')
})
