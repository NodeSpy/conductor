// Packs: Installed, Browse, a pack's Details, Add (the install review) and
// Add from a source, against a REAL conductor daemon with
// CONDUCTOR_PACK_REGISTRY pointed at a throwaway local registry of synthetic
// packs (lib/daemon.ts startWithCatalog). Its own dedicated daemon: these
// stage config edits, which must not disturb the shared daemon the rest of
// the suite reads. Every edit here only STAGES: the config file on disk is
// checked unchanged after each.
import { readFileSync } from 'node:fs'
import path from 'node:path'
import { test, expect } from '../fixtures'
import { startWithCatalog, stop, type Runtime } from '../lib/daemon'

let rt: Runtime & { registry: string }
let cfgBefore = ''

test.beforeAll(async () => {
  rt = await startWithCatalog(Number(process.env.DASH_E2E_PORT || 48766) + 23)
  cfgBefore = readFileSync(rt.config, 'utf8')
})

test.afterAll(() => {
  if (rt) stop(rt)
})

test.beforeEach(async ({ request }) => {
  await request.post(`${rt.baseURL}/api/v1/config/clear`, { data: {} })
})

const unchanged = () => expect(readFileSync(rt.config, 'utf8')).toBe(cfgBefore)

test('Installed lists the pack with its facts; hold stages, remove asks then stages', async ({ page }) => {
  await page.goto(`${rt.baseURL}/#/packs`)
  const row = page.locator('.pack-row[data-pack="house"]')
  await expect(row).toContainText('our own house checks')
  await expect(row).toContainText('Automation off')
  await expect(row).toContainText('Your own')
  await expect(row.getByRole('link', { name: 'Customize' })).toHaveAttribute('href', '#/packs/pack/house')
  await expect(page.getByText('Adding, updating or removing a pack is reviewed like any other change.')).toBeVisible()

  await row.getByRole('button', { name: 'More for house' }).click()
  await page.getByRole('menuitem', { name: 'Hold it at this version' }).click()
  await expect(page.locator('.toast')).toContainText('Staged: hold house at this version')
  await expect(row).toContainText('edit staged')

  await row.getByRole('button', { name: 'More for house' }).click()
  await page.getByRole('menuitem', { name: 'What an update would change…' }).click()
  const plan = page.getByRole('dialog', { name: 'Plan for house' })
  await expect(plan).toContainText('a pack of your own')
  await plan.getByRole('button', { name: 'Close' }).first().click()

  await row.getByRole('button', { name: 'More for house' }).click()
  await page.getByRole('menuitem', { name: 'Remove this pack…' }).click()
  await page.getByRole('dialog', { name: 'Remove pack house?' }).getByRole('button', { name: 'Stage removal' }).click()
  await expect(page).toHaveURL(/#\/system\/config/)
  await expect(page.locator('.staged-list')).toContainText('packs.house')
  unchanged()
})

test('Browse lists the registry as cards, searches and filters', async ({ page }) => {
  await page.goto(`${rt.baseURL}/#/packs/browse`)
  const grid = page.locator('#cat-grid')
  // the template-only pack belongs to New bot, not here
  await expect(grid.locator('.cat-card')).toHaveCount(3)
  await expect(grid).toContainText('greeter')
  await expect(grid).toContainText('tidy-kit')
  await expect(grid).not.toContainText('helper-kit')
  const house = grid.locator('.cat-card', { hasText: 'house' })
  await expect(house).toContainText('installed 0.1.0')
  await expect(house.getByRole('link', { name: 'Add' })).toHaveCount(0)

  const search = page.getByPlaceholder('Find by name, what it does or a tag')
  await search.fill('schedule')
  await search.press('Enter')
  await expect(grid.locator('.cat-card')).toHaveCount(1)
  await expect(grid).toContainText('greeter')
  await search.fill('nonesuch')
  await search.press('Enter')
  await expect(grid).toContainText('Nothing matches.')
})

test('Details, then Add: the review lists what it brings and stages packs.<name> only', async ({ page }) => {
  await page.goto(`${rt.baseURL}/#/packs/browse`)
  await page.locator('.cat-card', { hasText: 'greeter' }).getByRole('link', { name: 'Details' }).click()
  await expect(page.getByRole('heading', { level: 1, name: 'greeter' })).toBeVisible()
  await expect(page.locator('.pk-page')).toContainText('on_tick')
  await expect(page.locator('.pk-page')).toContainText('say-hi')
  await page.getByRole('link', { name: 'Install…' }).click()

  await expect(page.getByRole('heading', { level: 1, name: 'Install greeter' })).toBeVisible()
  await expect(page.locator('#iw-form')).toContainText('Bound automatically to box')
  await expect(page.locator('#iw-form')).toContainText('What installing adds')
  await page.locator('#iw-arm-on_tick').check()
  await expect(page.locator('#iw-form')).toContainText('and you are arming on_tick')
  await page.getByRole('button', { name: 'Review everything it adds' }).click()

  await expect(page).toHaveURL(/#\/system\/config/)
  const staged = page.locator('.staged-list')
  await expect(staged).toContainText('packs.greeter')
  await expect(staged).toContainText(path.join(rt.registry, 'greeter'))
  await expect(staged).toContainText('on_tick')
  unchanged()
})

test('Add from a source reads its manifest, then stages it; a bad source says so plainly', async ({ page }) => {
  await page.goto(`${rt.baseURL}/#/packs/browse`)
  await page.getByRole('link', { name: 'Add a pack from a source or git URL' }).click()
  await expect(page.getByRole('heading', { level: 1, name: 'A pack from a source' })).toBeVisible()

  await page.locator('#ps-use').fill('/nonexistent/nowhere')
  await page.getByRole('button', { name: 'Read its manifest' }).click()
  await expect(page.locator('.form-err')).toContainText(/couldn.t read a pack there/i)

  await page.locator('#ps-use').fill(path.join(rt.registry, 'tidy-kit'))
  await page.getByRole('button', { name: 'Read its manifest' }).click()
  await expect(page.getByRole('heading', { level: 2, name: 'tidy-kit' })).toBeVisible()
  await expect(page.locator('#ps-inst')).toHaveValue('tidy-kit')
  await page.getByRole('button', { name: 'Review the change' }).click()
  await expect(page).toHaveURL(/#\/system\/config/)
  await expect(page.locator('.staged-list')).toContainText('packs.tidy-kit')
  unchanged()
})

test("A pack's own page stages its overrides", async ({ page }) => {
  await page.goto(`${rt.baseURL}/#/packs/pack/house`)
  await expect(page.getByRole('heading', { level: 1, name: 'Customize house' })).toBeVisible()
  await expect(page.locator('.dl-col-main')).toContainText('bound automatically to box')
  await page.getByRole('switch', { name: 'Arm on_save' }).click()
  await expect(page.getByRole('link', { name: 'Review the change' })).toBeVisible()
  await expect(page.locator('.dl-col-side')).toContainText('on_save armed')
  await page.getByRole('button', { name: 'Reset to the pack’s' }).click()
  await expect(page.locator('.dl-col-side')).toContainText('None: it runs exactly as the pack ships.')
  unchanged()
})
