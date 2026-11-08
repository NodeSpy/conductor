// Plugins (CONFORMANCE "Plugins"): the four tabs list what the daemon has,
// and every add/edit/remove ends in a STAGED change — nothing is written or
// installed until Review & apply. Runs against the shared daemon (box and
// hooks connectors, a local runtime, the build-box host) and clears what it
// staged after each test.
import { test, expect } from '../fixtures'

test.afterEach(async ({ page, daemon }) => {
  await page.request.post(`${daemon.runtime.baseURL}/api/v1/config/clear`, { data: {} })
})

test('the four tabs list what the daemon has, with counts', async ({ page }) => {
  await page.goto('/#/connections')
  await expect(page.locator('.page-head h1')).toHaveText('Plugins')
  const tabs = page.getByRole('navigation', { name: 'Plugins' }).or(page.locator('[aria-label="Plugins"]')).first()
  await expect(tabs).toContainText('Connections')
  await expect(page.locator('li.cn-row[data-c="box"]')).toContainText('Built in')
  await expect(page.locator('li.cn-row[data-c="hooks"]')).toBeVisible()
  await expect(page.locator('[data-act="conn-add"]')).toHaveText(/Add a connection/)

  await page.goto('/#/connections/runtimes')
  await expect(page.locator('li.pl-row[data-k="local"]')).toBeVisible()
  await expect(page.locator('[data-act="rt-add"]')).toBeVisible()

  await page.goto('/#/connections/languages')
  await expect(page.locator('li.pl-row[data-k="cli"]')).toBeVisible()

  await page.goto('/#/connections/machines')
  await expect(page.locator('li.pl-row[data-k="build-box"]')).toContainText('Not checked yet')
})

test('adding a connection only stages it', async ({ page, daemon }) => {
  const before = await (await page.request.get(`${daemon.runtime.baseURL}/api/v1/config`)).json()
  await page.goto('/#/connections')
  await page.locator('[data-act="conn-add"]').click()
  const pick = page.locator('.pk-modal [data-act="cat-pick"][data-name="command"]')
  await expect(pick).toBeVisible()
  await pick.click()
  const form = page.locator('#gen-form')
  await expect(form).toBeVisible()
  await form.getByRole('textbox', { name: 'Name' }).fill('box-two')
  await page.locator('#gen-submit').click()
  await expect(page).toHaveURL(/#\/system\/config/)
  await expect(page.locator('.staged-list')).toContainText('connectors.box-two')

  const after = await (await page.request.get(`${daemon.runtime.baseURL}/api/v1/config`)).json()
  expect(after.text).toBe(before.text)
  expect(after.sha).toBe(before.sha)
})

test('a connection’s menu configures and removes it through staging', async ({ page }) => {
  await page.goto('/#/connections')
  await page.locator('[data-act="conn-more"][data-c="hooks"]').click()
  const menu = page.getByRole('menu')
  await expect(menu).toContainText('Configure…')
  await menu.getByText('Remove this connection…').click()
  await page.locator('#cf-ok').click()
  await expect(page).toHaveURL(/#\/system\/config/)
  await expect(page.locator('.staged-list')).toContainText('connectors.hooks')
})

test('a runtime can be added, and a machine edited, by staging', async ({ page }) => {
  await page.goto('/#/connections/runtimes')
  await page.locator('[data-act="rt-add"]').click()
  await page.locator('.pk-modal [data-act="cat-pick"][data-name="cli"]').click()
  await page.locator('#gen-form').getByRole('textbox', { name: 'Name' }).fill('second')
  await page.locator('#gen-submit').click()
  await expect(page.locator('.staged-list')).toContainText('runtimes.second')

  await page.goto('/#/connections/machines')
  await page.locator('[data-act="host-more"][data-h="build-box"]').click()
  const menu = page.getByRole('menu')
  await expect(menu.getByText('Test the connection')).toBeVisible()
  await menu.getByText('Edit…').click()
  await page.locator('#gen-form').getByRole('textbox', { name: 'User' }).fill('deploy')
  await page.locator('#gen-submit').click()
  await expect(page.locator('.staged-list')).toContainText('hosts.build-box')
})
