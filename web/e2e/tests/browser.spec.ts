// Browsers + wallet e2e (design §4.5, §9). Driven through the built SPA against a
// real authenticated daemon with a payment card, a profile and one pending
// purchase, and a Chromium CDP virtual authenticator for sign-in and the purchase
// step-up. WebKit has no virtual authenticator, so these are Chromium-only, like
// the auth and bots suites. One shared context holds the enrolled passkey and the
// session across the serial tests.
import { test, expect, type Page, type BrowserContext } from '@playwright/test'
import { startBrowserDaemon, enroll, stop } from '../lib/browserdaemon'
import type { AuthRuntime } from '../lib/authdaemon'

test.describe.configure({ mode: 'serial' })

let rt: AuthRuntime
let ctx: BrowserContext
let page: Page

test.beforeAll(async ({ browser, browserName }) => {
  test.skip(browserName !== 'chromium', 'the CDP virtual authenticator is Chromium-only')
  const base = Number(process.env.DASH_E2E_PORT || 48766)
  rt = await startBrowserDaemon(base + 3)

  ctx = await browser.newContext({ baseURL: rt.baseURL, colorScheme: 'dark' })
  page = await ctx.newPage()
  const cdp = await ctx.newCDPSession(page)
  await cdp.send('WebAuthn.enable')
  await cdp.send('WebAuthn.addVirtualAuthenticator', {
    options: {
      protocol: 'ctap2',
      ctap2Version: 'ctap2_1',
      transport: 'internal',
      hasResidentKey: true,
      hasUserVerification: true,
      isUserVerified: true,
      automaticPresenceSimulation: true,
    },
  })

  const { secret, code } = enroll(rt)
  await page.goto(`${rt.baseURL}/enroll#t=${secret}`)
  await expect(page.getByRole('heading', { name: 'Enrol a passkey' })).toBeVisible()
  await page.getByTestId('enroll-code').fill(code)
  await page.getByTestId('enroll-name').fill('e2e laptop')
  await page.getByRole('button', { name: 'Localhost only', exact: true }).click()
  await page.getByTestId('enroll-button').click()
  await expect(page.getByRole('heading', { name: 'Passkey enrolled' })).toBeVisible({ timeout: 15_000 })

  await page.goto(`${rt.baseURL}/signin`)
  await page.getByTestId('signin-button').click()
  await expect(page.getByRole('link', { name: 'Access' })).toBeVisible({ timeout: 15_000 })
})

test.afterAll(async () => {
  if (ctx) await ctx.close()
  if (rt) stop(rt)
})

test('a profile shows every detail masked, with its source in words — never a value', async () => {
  await page.goto(`${rt.baseURL}/#/browser/details/home`)
  await expect(page.getByRole('heading', { name: 'home' })).toBeVisible({ timeout: 10_000 })
  // Every detail is •••••••• set, never a value.
  const masked = page.getByTestId('masked-value')
  await expect(masked.first()).toBeVisible()
  await expect(masked.first()).toHaveText('••••••••')
  // The email's source reads in words (an env var), and no value leaks onto the page.
  await expect(page.locator('tr[data-field="email"] .wl-src')).toContainText('environment variable HOME_EMAIL')
  await expect(page.locator('body')).not.toContainText('@')
})

test('freezing a card narrows it at once — no passkey needed', async () => {
  await page.goto(`${rt.baseURL}/#/browser/cards`)
  const row = page.locator('[data-card="household"]')
  await expect(row).toBeVisible({ timeout: 10_000 })
  // Brand + last four, never more of the number.
  await expect(row).toContainText('••••')
  await row.getByTestId('freeze').click()
  // Freeze is narrowing: it applies without a step-up, and the row says so.
  await expect(row.getByTestId('card-status')).toContainText('Frozen', { timeout: 10_000 })
  await expect(row.getByTestId('unfreeze')).toBeVisible()
})

test('approving a purchase needs a passkey step-up and records the decision', async () => {
  await page.goto(`${rt.baseURL}/#/browser/purchases/pu1`)
  await expect(page.getByRole('heading', { name: /Buy Widget/ })).toBeVisible({ timeout: 10_000 })
  // The terms: items, the card (last four only) and the stacked limits.
  await expect(page.getByTestId('purchase-kicker')).toContainText('Needs you')
  await expect(page.locator('.wl-items')).toContainText('Widget')
  await expect(page.getByTestId('approve')).toBeVisible()

  await page.getByTestId('approve').click()
  // The virtual authenticator satisfies the step-up automatically; the decision
  // lands and the screen confirms it.
  await expect(page.getByTestId('purchase-done')).toContainText('Approved', { timeout: 15_000 })
})
