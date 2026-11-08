// The Secrets screen against the real daemon: vaults first, each a card with
// its entries; a value stored from the page lands in the vault and never comes
// back; a reference to an unconfigured vault gets its own card; the Settings
// tab is read-only rows with a ⋯ menu that stages edits.
import { test, expect } from '../fixtures'
import type { SecretsResponse } from '../../src/api/types'

test('Secrets: vault cards, the not-in-any-vault card, and storing a value', async ({ page, collector }, info) => {
  await page.goto('/#/secrets')
  const main = page.getByTestId('vault-main')
  await expect(main).toBeVisible()
  await expect(main).toContainText('conductor vault')
  await expect(main).toContainText('Reachable')
  await expect(main.locator('[data-secret="main/api-token"]')).toContainText('the setting api_token')
  // 1Password is read-only: Add a secret explains where its values are set.
  await expect(page.getByTestId('vault-onepass')).toContainText('1Password')
  await page.getByTestId('add-secret-onepass').click()
  await expect(page.locator('.modal-body')).toContainText('set in 1Password itself')
  await page.getByRole('button', { name: 'Close' }).last().click()
  await expect(page.getByTestId('vaults-count')).toContainText('2 vaults')
  const ghost = page.getByTestId('not-in-vault')
  await expect(ghost).toContainText('webhook-secret')
  await expect(ghost).toContainText('Add vault ghost')

  // Add a secret, preselected to this vault. allow_open: no step-up here.
  const key = 'e2e-' + info.project.name.replace(/\W+/g, '-')
  await page.getByTestId('add-secret-main').click()
  const form = page.getByTestId('secret-form')
  await expect(form.locator('select[data-k=vault]')).toHaveValue('main')
  await form.locator('[data-k=key]').fill(key)
  await form.locator('[data-k=value]').fill('e2e-value-never-echoed')
  await page.getByTestId('secret-form-submit').click()
  await expect(page.getByTestId('secrets-msg')).toContainText(`Stored main/${key}`)
  await expect(main.locator(`[data-secret="main/${key}"]`)).toBeVisible()

  const res = await page.request.get('/api/v1/secrets')
  const body = await res.text()
  expect(body).not.toContain('e2e-value-never-echoed')
  const data = JSON.parse(body) as SecretsResponse
  expect(data.secrets.some((s) => s.ref === `main/${key}` && s.stored)).toBe(true)
  expect(collector.consoleErrors).toEqual([])
})

test('Secrets: Add a vault stages the vault and opens the review', async ({ page }) => {
  await page.goto('/#/secrets')
  await page.getByTestId('add-vault').click()
  const f = page.getByTestId('vault-form')
  await f.locator('[data-k=name]').fill('spare')
  await f.getByRole('button', { name: 'file', exact: true }).click()
  await f.locator('[data-k=file__dir]').fill('/run/secrets')
  await page.getByTestId('vault-form-submit').click()
  await expect(page).toHaveURL(/#\/system\/config/)
  const cfg = (await (await page.request.get('/api/v1/config')).json()) as { staged: { id: string; path: string[] }[] }
  const st = cfg.staged.find((c) => c.path.join('.') === 'vaults.spare')
  expect(st).toBeTruthy()
  await page.request.post('/api/v1/config/unstage', { data: { id: st!.id } })
})

test('Settings: read-only rows with a ⋯ menu, env scope in words, folded preview', async ({ page }) => {
  await page.goto('/#/secrets/settings')
  await expect(page.locator('#settings input')).toHaveCount(0)
  await expect(page.locator('[data-secret="settings.greeting"]')).toContainText('hello')
  await expect(page.locator('[data-secret="env.host:build-box:LOG_LEVEL"]')).toContainText('for commands on the machine build-box')
  await expect(page.locator('#env-preview')).not.toHaveAttribute('open', '')

  await page.getByTestId('setting-more-greeting').click()
  await page.getByRole('menuitem', { name: 'Change the value…' }).click()
  const f = page.getByTestId('setting-form')
  await expect(f.locator('[data-k=value]')).toHaveValue('hello')
  await f.locator('[data-k=value]').fill('hi there')
  await page.getByTestId('setting-form-submit').click()
  await expect(page.locator('.set-review')).toContainText('not applied yet')
  const cfg = (await (await page.request.get('/api/v1/config')).json()) as { staged: { id: string; path: string[] }[] }
  const st = cfg.staged.find((c) => c.path.join('.') === 'settings.greeting')
  expect(st).toBeTruthy()
  await page.request.post('/api/v1/config/unstage', { data: { id: st!.id } })
})
