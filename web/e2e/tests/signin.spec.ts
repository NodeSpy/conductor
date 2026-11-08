// Sign-in and confirmation method choice (localhost, Chromium and WebKit): the
// gate shows the stacked logo centred over the card and offers the browser
// methods (passkey, password) as a segmented choice that switches the form in
// place and is remembered; the SSH key is not one of them but stays reachable
// through a quiet fallback link. In confirmations the SSH key appears only while
// the operator has nothing else enrolled; once a password (step-up allowed for
// it here) and an authenticator exist, the sheet offers those and a password +
// code confirmation goes through.
import { test, expect, type Page, type BrowserContext, type Browser, type TestInfo, type Locator } from '@playwright/test'
import { execFileSync } from 'node:child_process'
import { createHmac } from 'node:crypto'
import { startAuthDaemon, stop, type AuthRuntime } from '../lib/authdaemon'

test.describe.configure({ mode: 'serial' })

const PASSWORD = 'correct horse battery staple'

let rt: AuthRuntime
let ctx: BrowserContext
let page: Page
let totpSecret = ''
let lastStep = -1

function base32(s: string): Buffer {
  const alpha = 'ABCDEFGHIJKLMNOPQRSTUVWXYZ234567'
  let bits = 0
  let val = 0
  const out: number[] = []
  for (const c of s.toUpperCase().replace(/[=\s]/g, '')) {
    val = (val << 5) | alpha.indexOf(c)
    bits += 5
    if (bits >= 8) {
      out.push((val >>> (bits - 8)) & 0xff)
      bits -= 8
    }
  }
  return Buffer.from(out)
}

function totpAt(secret: string, step: number): string {
  const ctr = Buffer.alloc(8)
  ctr.writeBigUInt64BE(BigInt(step))
  const mac = createHmac('sha1', base32(secret)).update(ctr).digest()
  const off = mac[mac.length - 1] & 0x0f
  const n = ((mac[off] & 0x7f) << 24) | (mac[off + 1] << 16) | (mac[off + 2] << 8) | mac[off + 3]
  return String(n % 1_000_000).padStart(6, '0')
}

// freshCode: a code from a step after the last one spent, within the ±1 window.
async function freshCode(): Promise<string> {
  for (;;) {
    const cur = Math.floor(Date.now() / 1000 / 30)
    const step = Math.max(lastStep + 1, cur)
    if (step <= cur + 1) {
      lastStep = step
      return totpAt(totpSecret, step)
    }
    await new Promise((r) => setTimeout(r, 500))
  }
}

function sign(challenge: string): string {
  const ns = challenge.split(' ')[0]
  return execFileSync('ssh-keygen', ['-Y', 'sign', '-n', ns, '-f', rt.keyFile], { input: challenge, stdio: ['pipe', 'pipe', 'ignore'] }).toString()
}

async function pasteSSH(scope: Locator | Page): Promise<void> {
  await scope.getByRole('tab', { name: 'Sign on this device' }).click()
  const challenge = ((await scope.getByTestId('ssh-challenge').textContent()) ?? '').trim()
  await scope.getByTestId('ssh-signature').fill(sign(challenge))
  await scope.getByTestId('ssh-submit').click()
}

async function newDevice(browser: Browser, info: TestInfo): Promise<BrowserContext> {
  const u = info.project.use
  return browser.newContext({
    baseURL: rt.baseURL,
    colorScheme: u.colorScheme === 'light' ? 'light' : 'dark',
    viewport: u.viewport,
    userAgent: u.userAgent,
    deviceScaleFactor: u.deviceScaleFactor,
    isMobile: u.isMobile,
    hasTouch: u.hasTouch,
  })
}

// changePassword opens Access's password form and saves, which asks for a
// confirmation; it returns the sheet.
async function changePassword(): Promise<Locator> {
  await page.goto('/#/security')
  await page.getByTestId('password-open').click()
  await page.getByTestId('password-new').fill(PASSWORD)
  await page.getByTestId('password-new2').fill(PASSWORD)
  await page.getByTestId('password-save').click()
  const dialog = page.getByTestId('stepup-dialog')
  await expect(dialog).toBeVisible()
  return dialog
}

test.beforeAll(async ({ browser, browserName }, info) => {
  const base = Number(process.env.DASH_E2E_PORT || 48766)
  rt = await startAuthDaemon(base + (browserName === 'chromium' ? 14 : 15), {
    auth: '{ password_requires_totp: false, stepup: [passkey, ssh, totp, password] }',
  })
  ctx = await newDevice(browser, info)
  page = await ctx.newPage()
})

test.afterAll(async () => {
  if (ctx) await ctx.close()
  if (rt) stop(rt)
})

test('the gate: centred stacked logo, a method choice without SSH, remembered', async () => {
  await page.goto('/signin')
  const card = page.locator('.gate-card')
  const logo = card.locator('svg.gate-logo')
  await expect(logo).toBeVisible()
  const c = (await card.boundingBox())!
  const l = (await logo.boundingBox())!
  const h = (await card.getByRole('heading', { name: 'Sign in' }).boundingBox())!
  expect(Math.abs(l.x + l.width / 2 - (c.x + c.width / 2)), 'logo centred in the card').toBeLessThanOrEqual(2)
  expect(l.y + l.height, 'logo above the heading').toBeLessThanOrEqual(h.y)
  expect(l.width, 'logo at least 160px wide').toBeGreaterThanOrEqual(160)

  // The methods are a segmented choice; the SSH key isn't one of them.
  const tabs = page.getByRole('tablist', { name: 'Sign in with' })
  await expect(tabs.getByRole('tab', { name: 'Password' })).toBeVisible()
  await expect(tabs.getByRole('tab', { name: /SSH/ })).toHaveCount(0)
  await expect(page.getByTestId('ssh-begin')).toHaveCount(0)
  await expect(page.getByTestId('signin-hint')).toContainText('conductor dashboard enroll')
  await expect(page.getByTestId('signin-hint')).not.toContainText('SSH')

  // Picking a method switches the form in place, and is remembered.
  await page.getByTestId('method-password').click()
  await expect(page.getByTestId('method-password')).toHaveAttribute('aria-selected', 'true')
  await expect(page.getByTestId('password-form')).toBeVisible()
  await page.reload()
  await expect(page.getByTestId('method-password')).toHaveAttribute('aria-selected', 'true')
  await expect(page.getByTestId('password-form')).toBeVisible()
})

test('with the password as the one browser method, it opens on the password', async ({ browser }, info) => {
  // The set a plain-HTTP origin offers (no passkey there), SSH listed first.
  const c = await newDevice(browser, info)
  try {
    const p = await c.newPage()
    await p.route('**/api/v1/auth/state', async (route) => {
      const res = await route.fetch()
      await route.fulfill({ response: res, json: { ...(await res.json()), methods: ['ssh', 'password'] } })
    })
    await p.goto('/signin')
    await expect(p.getByTestId('password-form')).toBeVisible()
    await expect(p.getByRole('tablist', { name: 'Sign in with' })).toHaveCount(0)
    await expect(p.getByTestId('method-ssh')).toBeVisible()
  } finally {
    await c.close()
  }
})

test('the SSH key is a quiet fallback that still signs in', async () => {
  await page.goto('/signin')
  const link = page.getByTestId('method-ssh')
  await expect(link).toHaveText('Use your SSH key instead')
  await link.click()
  await expect(page.getByRole('tablist', { name: 'Sign in with' })).toHaveCount(0)
  await page.getByTestId('ssh-back').click()
  await expect(page.getByRole('tablist', { name: 'Sign in with' })).toBeVisible()
  await page.getByTestId('method-ssh').click()
  await page.getByTestId('signin-operator').fill('ops')
  await page.getByTestId('ssh-begin').click()
  await pasteSSH(page)
  await page.waitForURL(/#\/overview/, { timeout: 15_000 })
})

test('with only an SSH key, a confirmation offers it and says how to stop needing it', async () => {
  await page.goto('/#/security')
  await expect(page.getByTestId('stepup-methods')).toContainText('For now you confirm with your SSH key')
  const dialog = await changePassword()
  await expect(dialog.getByRole('tablist', { name: 'Confirm with' })).toHaveCount(0)
  await expect(dialog.getByTestId('ssh-panel')).toBeVisible()
  await expect(dialog.getByTestId('stepup-ssh-fallback')).toContainText('System → Access')
  await pasteSSH(dialog)
  await expect(dialog).toHaveCount(0)
  await expect(page.getByTestId('access-msg')).toContainText('Password set')
})

test('once something else is enrolled, the SSH key is no longer offered', async () => {
  await page.getByTestId('totp-start').click()
  totpSecret = ((await page.getByTestId('totp-secret').textContent()) ?? '').replace(/\s+/g, '')
  await page.getByTestId('totp-code').fill(await freshCode())
  await page.getByTestId('totp-confirm').click()
  const dialog = page.getByTestId('stepup-dialog')
  await expect(dialog).toBeVisible()
  await expect(dialog.getByTestId('ssh-panel')).toHaveCount(0)
  await expect(dialog.getByTestId('stepup-ssh-fallback')).toHaveCount(0)
  await dialog.getByTestId('stepup-password').fill(PASSWORD)
  await dialog.getByTestId('stepup-password-submit').click()
  await expect(dialog).toHaveCount(0)
  await expect(page.getByTestId('access-msg')).toContainText('Authenticator app set up')
  await expect(page.getByTestId('stepup-methods')).toContainText('authenticator code or your password')
  await expect(page.getByTestId('stepup-methods')).not.toContainText('SSH')
})

test('confirm with a password and an authenticator code', async () => {
  const dialog = await changePassword()
  const tabs = dialog.getByRole('tablist', { name: 'Confirm with' })
  await expect(tabs.getByRole('tab')).toHaveText(['Authenticator code', 'Password'])
  await dialog.getByTestId('stepup-method-password').click()
  await dialog.getByTestId('stepup-password').fill(PASSWORD)
  await dialog.getByTestId('stepup-password-code').fill(await freshCode())
  await dialog.getByTestId('stepup-password-submit').click()
  await expect(dialog).toHaveCount(0)
  await expect(page.getByTestId('access-msg')).toContainText('Password changed')

  // The next confirmation opens on the method used last.
  const again = await changePassword()
  await expect(again.getByTestId('stepup-method-password')).toHaveAttribute('aria-selected', 'true')
  await again.getByTestId('stepup-cancel').click()
  await expect(again).toHaveCount(0)
})

test('a gate taller than the screen scrolls to its last button', async ({ browser }, info) => {
  const c = await browser.newContext({ baseURL: rt.baseURL, viewport: { width: 393, height: 600 } })
  try {
    const p = await c.newPage()
    await p.goto('/enroll')
    const logo = p.locator('svg.gate-logo')
    await expect(logo).toBeVisible()
    expect((await logo.boundingBox())!.y, 'the top of the card is not clipped').toBeGreaterThanOrEqual(0)
    const last = p.getByTestId('enroll-button').or(p.getByTestId('enroll-password-start'))
    await last.scrollIntoViewIfNeeded()
    const box = (await last.boundingBox())!
    expect(box.y + box.height, 'the last button scrolls into view').toBeLessThanOrEqual(600)
  } finally {
    await c.close()
  }
})
