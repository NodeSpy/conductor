// Bots/Teams/Chat/Tickets/Approvals e2e (design §4.4, §9). Driven through the
// built SPA against a real authenticated daemon with a team (platform) and two
// bots (atlas, sre), one seeded pending proposal, and a Chromium CDP virtual
// authenticator for sign-in and the approval step-up. WebKit has no virtual
// authenticator, so these are Chromium-only, like the auth suite. One shared
// context holds the enrolled passkey and the session across the serial tests.
import { test, expect, type Page, type BrowserContext } from '@playwright/test'
import { startAuthDaemon, enroll, stop, type AuthRuntime } from '../lib/authdaemon'

test.describe.configure({ mode: 'serial' })

let rt: AuthRuntime
let ctx: BrowserContext
let page: Page

test.beforeAll(async ({ browser, browserName }) => {
  test.skip(browserName !== 'chromium', 'the CDP virtual authenticator is Chromium-only')
  const base = Number(process.env.DASH_E2E_PORT || 48766)
  rt = await startAuthDaemon(base + 2)

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
  await expect(page.getByRole('heading', { name: 'Enrol', exact: true })).toBeVisible()
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

test('the Bots screen shows teams as sections with their bots', async () => {
  await page.goto(`${rt.baseURL}/#/bots`)
  await expect(page.getByRole('heading', { name: /Bots/ })).toBeVisible()
  await expect(page.locator('[data-team="platform"]')).toBeVisible()
  await expect(page.locator('[data-bot="atlas"]').first()).toBeVisible()
  // Atlas has a mascot face (an SVG), not initials.
  await expect(page.locator('[data-bot="atlas"] .avatar svg').first()).toBeVisible()
})

test('approving a proposal from its page needs a passkey step-up', async () => {
  await page.goto(`${rt.baseURL}/#/bots/sre/approvals`)
  const review = page.getByTestId('review-proposal').first()
  await expect(review).toBeVisible({ timeout: 10_000 })
  await review.click()

  // The approval screen renders the canonical digest and the signing buttons.
  await expect(page.getByTestId('approval-digest')).toBeVisible()
  await expect(page.getByTestId('approve')).toBeVisible()

  await page.getByTestId('approve').click()
  // The step-up dialog offers the passkey first; the virtual authenticator
  // satisfies it automatically, the decision lands and the screen confirms it.
  await page.getByTestId('stepup-passkey').click()
  await expect(page.getByTestId('approval-done')).toContainText('Approved', { timeout: 15_000 })
})

test('create a ticket and mark it ready', async () => {
  await page.goto(`${rt.baseURL}/#/tickets`)
  await page.getByTestId('new-ticket').click()
  await page.getByTestId('ticket-title').fill('Investigate elevated 5xx')
  await page.getByTestId('ticket-assignee').selectOption('bot:atlas')
  await page.getByTestId('create-ready').click()

  // The sheet closes and the new ticket shows on the board, past Draft (a ready
  // ticket with an assignee; atlas may already have picked it up).
  const card = page.locator('.ticket-card', { hasText: 'Investigate elevated 5xx' }).first()
  await expect(card).toBeVisible({ timeout: 10_000 })
  await card.click()
  await expect(page.getByTestId('ticket-status')).toContainText(/Ready|In progress/, { timeout: 10_000 })
})

test('a team chat message can address everyone', async () => {
  await page.goto(`${rt.baseURL}/#/chat/team:platform`)
  await expect(page.getByTestId('chat-input')).toBeVisible({ timeout: 10_000 })
  await page.getByTestId('to-everyone').click()
  await page.getByTestId('chat-input').fill('status check please, everyone')
  await page.getByTestId('chat-send').click()

  const scroll = page.getByTestId('chat-scroll')
  await expect(scroll).toContainText('status check please, everyone', { timeout: 10_000 })
  await expect(scroll.getByText('to everyone').first()).toBeVisible()
})

test('give a bot a face (stage an avatar change)', async () => {
  await page.goto(`${rt.baseURL}/#/bots/atlas/settings`)
  await page.reload() // boot the SPA cleanly at this hash, free of prior in-SPA nav
  await expect(page.getByRole('heading', { name: /Atlas/ })).toBeVisible({ timeout: 10_000 })
  await expect(page.getByTestId('identity-picker')).toBeVisible({ timeout: 10_000 })
  // Pick a different colour, then use it — it stages a config change.
  await page.locator('[data-testid="idp-colors"] [data-color="plum"]').click()
  await page.getByTestId('stage-face').click()

  // The review panel appears with the staged change; reviewing shows the diff.
  await expect(page.getByText(/Review \d change/)).toBeVisible({ timeout: 10_000 })
  await page.getByRole('button', { name: 'Review changes' }).click()
  await expect(page.locator('.diff')).toContainText('plum', { timeout: 10_000 })
})
