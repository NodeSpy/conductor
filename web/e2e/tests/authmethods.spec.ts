// Sign-in methods e2e (dashboard-auth-methods): the passkey-less paths, on
// Chromium AND WebKit (iPhone), against a dedicated daemon that also listens on
// the machine's non-loopback address over plain HTTP. Through that origin — not
// a secure context, so no passkey — the operator signs in by pasting an SSH
// signature, sets a password and an authenticator from Access (each confirmed
// with the SSH key in the step-up dialog), signs in with password + code, signs
// in from "another device" by approving the login code with the CLI, and approves
// a bot proposal with an authenticator-code step-up. TOTP codes are computed here
// from the enrolled secret; each accepted time step is spent, so the next code
// must come from a later step.
import { test, expect, type Page, type BrowserContext, type Browser, type TestInfo, type Locator } from '@playwright/test'
import { execFileSync, spawnSync } from 'node:child_process'
import { createHmac } from 'node:crypto'
import { mkdirSync } from 'node:fs'
import path from 'node:path'
import { startAuthDaemon, stop, type AuthRuntime } from '../lib/authdaemon'

test.describe.configure({ mode: 'serial' })

const SHOT_DIR = path.resolve(__dirname, '..', 'screenshots')
const PASSWORD = 'correct horse battery staple'

let rt: AuthRuntime
let remote: string
let sshCtx: BrowserContext // signed in with the SSH key
let page: Page
let pwCtx: BrowserContext // signed in with password + code
let pwPage: Page
let totpSecret = ''
let lastStep = -1 // the last TOTP step the daemon accepted

// --- TOTP (RFC 6238: SHA-1, 30 s, 6 digits) ---

function base32(s: string): Buffer {
  const alpha = 'ABCDEFGHIJKLMNOPQRSTUVWXYZ234567'
  let bits = 0
  let val = 0
  const out: number[] = []
  for (const c of s.toUpperCase().replace(/[=\s]/g, '')) {
    const i = alpha.indexOf(c)
    if (i < 0) throw new Error(`bad base32 character ${c}`)
    val = (val << 5) | i
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

// freshCode returns a code the daemon will accept now: from a step after the
// last spent one and within its ±1 step window, waiting for the clock when the
// next unspent step is still two ahead. It records the step as spent.
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

// --- SSH ---

// sign signs a challenge the way the page's command does; the namespace is the
// challenge's first word (conductor-dashboard-login / -stepup).
function sign(challenge: string): string {
  const ns = challenge.split(' ')[0]
  return execFileSync('ssh-keygen', ['-Y', 'sign', '-n', ns, '-f', rt.keyFile], { input: challenge, stdio: ['pipe', 'pipe', 'ignore'] }).toString()
}

// pasteSSH completes an SSH panel (on the sign-in page or in the step-up dialog)
// by the paste route. A touch device opens the panel on the approve tab.
async function pasteSSH(scope: Locator | Page): Promise<void> {
  await scope.getByRole('tab', { name: 'Sign on this device' }).click()
  const challenge = ((await scope.getByTestId('ssh-challenge').textContent()) ?? '').trim()
  expect(challenge).toMatch(/^conductor-dashboard-(login|stepup) v1 /)
  await scope.getByTestId('ssh-signature').fill(sign(challenge))
  await scope.getByTestId('ssh-submit').click()
}

// --- pages ---

async function newDeviceContext(browser: Browser, info: TestInfo, colorScheme: 'dark' | 'light' = 'dark'): Promise<BrowserContext> {
  const u = info.project.use
  return browser.newContext({
    baseURL: remote,
    colorScheme,
    viewport: u.viewport,
    userAgent: u.userAgent,
    deviceScaleFactor: u.deviceScaleFactor,
    isMobile: u.isMobile,
    hasTouch: u.hasTouch,
  })
}

async function authState(ctx: BrowserContext): Promise<{ signed_in: boolean; method?: string; transport?: string }> {
  const r = await ctx.request.get(remote + '/api/v1/auth/state')
  expect(r.ok()).toBeTruthy()
  return r.json()
}

async function noHorizontalOverflow(p: Page): Promise<void> {
  const over = await p.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth)
  expect(over, 'page scrolls sideways').toBeLessThanOrEqual(1)
}

// fitsInside checks an element sits within its container's box (a full-width
// button whose padding is added to 100% pokes out of its card on WebKit).
async function fitsInside(inner: Locator, outer: Locator): Promise<void> {
  const a = (await inner.boundingBox())!
  const b = (await outer.boundingBox())!
  expect(a.x, 'starts inside its container').toBeGreaterThanOrEqual(b.x - 0.5)
  expect(a.x + a.width, 'ends inside its container').toBeLessThanOrEqual(b.x + b.width + 0.5)
}

async function shot(p: Page, info: TestInfo, name: string): Promise<void> {
  mkdirSync(SHOT_DIR, { recursive: true })
  await p.screenshot({ path: path.join(SHOT_DIR, `authmethods-${name}-${info.project.name}.png`), fullPage: true })
}

test.beforeAll(async ({ browser, browserName }, info) => {
  const base = Number(process.env.DASH_E2E_PORT || 48766)
  // Distinct ports per project (the other suites use base+1…+3 and base+7).
  const off = browserName === 'chromium' ? 4 : 8
  rt = await startAuthDaemon(base + off, { bindPort: base + off + 1 })
  remote = rt.remoteURL!
  sshCtx = await newDeviceContext(browser, info)
  page = await sshCtx.newPage()
})

test.afterAll(async () => {
  if (pwCtx) await pwCtx.close()
  if (sshCtx) await sshCtx.close()
  if (rt) stop(rt)
})

test('a plain-HTTP origin says it is unencrypted and offers no passkey', async ({ browser }, info) => {
  await page.goto('/signin')
  await expect(page.getByRole('heading', { name: 'Sign in' })).toBeVisible()
  await expect(page.getByTestId('plain-http-notice')).toBeVisible()
  await expect(page.getByTestId('transport')).toContainText('Plain HTTP')
  // The footer names the address this browser used, never an internal listener name.
  await expect(page.getByTestId('transport')).toContainText(new URL(remote).host)
  await expect(page.getByTestId('transport')).not.toContainText('bind:')
  await expect(page.getByTestId('signin-operator')).toHaveAttribute('placeholder', /operator/)
  // The SSH key is the primary method; no passkey button, no passkey chip.
  await expect(page.getByTestId('ssh-begin')).toBeVisible()
  await expect(page.getByTestId('signin-button')).toHaveCount(0)
  await expect(page.getByTestId('method-passkey')).toHaveCount(0)
  await expect(page.getByTestId('method-password')).toBeVisible()
  await fitsInside(page.getByTestId('ssh-begin'), page.locator('.gate-card'))
  await noHorizontalOverflow(page)
  await shot(page, info, 'signin-dark')

  // The same page in light mode.
  const light = await newDeviceContext(browser, info, 'light')
  const lp = await light.newPage()
  await lp.goto('/signin')
  await expect(lp.getByTestId('plain-http-notice')).toBeVisible()
  await noHorizontalOverflow(lp)
  await shot(lp, info, 'signin-light')
  await light.close()
})

test('sign in by pasting an SSH signature', async ({}, info) => {
  await page.goto('/signin')
  await page.getByTestId('signin-operator').fill('ops')
  await page.getByTestId('ssh-begin').click()
  await expect(page.getByTestId('ssh-panel')).toBeVisible()
  await expect(page.getByTestId('login-code').or(page.getByTestId('ssh-command'))).toBeVisible()
  await noHorizontalOverflow(page)
  await shot(page, info, 'ssh-panel')
  await pasteSSH(page)
  await page.waitForURL(/#\/overview/, { timeout: 15_000 })
  const st = await authState(sshCtx)
  expect(st).toMatchObject({ signed_in: true, method: 'ssh', transport: 'http' })
  // Signed in over plain HTTP, the app keeps saying so.
  await expect(page.getByTestId('plain-http-notice')).toBeVisible()
})

test('set a password and an authenticator from Access, confirming with the SSH key', async ({}, info) => {
  test.setTimeout(60_000)
  await page.goto('/#/security')
  await page.getByTestId('password-open').click()
  await page.getByTestId('password-new').fill(PASSWORD)
  await page.getByTestId('password-new2').fill(PASSWORD)
  await page.getByTestId('password-save').click()

  // Changing a credential asks for a confirmation; on this connection that is
  // the SSH key (no passkey here, no authenticator yet).
  const dialog = page.getByTestId('stepup-dialog')
  await expect(dialog).toBeVisible()
  await expect(dialog).toContainText('Change how you sign in')
  await expect(dialog.getByTestId('ssh-panel')).toBeVisible()
  await noHorizontalOverflow(page)
  await shot(page, info, 'stepup-ssh')
  await pasteSSH(dialog)
  await expect(dialog).toHaveCount(0)
  await expect(page.getByTestId('access-msg')).toContainText('Password set')
  await expect(page.getByTestId('password-state')).not.toContainText('Not set')

  // The authenticator: read the key, compute the current code, confirm.
  await page.getByTestId('totp-start').click()
  totpSecret = ((await page.getByTestId('totp-secret').textContent()) ?? '').replace(/\s+/g, '')
  expect(totpSecret).toMatch(/^[A-Z2-7]{16,}$/)
  await expect(page.locator('img.totp-qr')).toBeVisible()
  await noHorizontalOverflow(page)
  await shot(page, info, 'totp-setup')
  await page.getByTestId('totp-code').fill(await freshCode())
  await page.getByTestId('totp-confirm').click()
  await expect(dialog).toBeVisible()
  await pasteSSH(dialog)
  await expect(dialog).toHaveCount(0)
  await expect(page.getByTestId('access-msg')).toContainText('Authenticator app set up')
  await expect(page.getByTestId('totp-state')).toContainText(/Set up (just now|\d+ min ago)/)
  await expect(page.getByTestId('totp-state')).not.toContainText('ago ago')
  await expect(page.locator('.acc-note', { hasText: 'A session ends after' })).not.toContainText(/\d+m0s|\dh0m/)
  await shot(page, info, 'access')
})

test('replace the authenticator app: the old one works until the new one is confirmed', async ({}, info) => {
  test.setTimeout(60_000)
  await page.goto('/#/security')
  await page.getByTestId('totp-replace').click()
  await expect(page.getByTestId('totp-replace-note')).toBeVisible()
  // The manual key reads in 4-character blocks.
  const shown = ((await page.getByTestId('totp-secret').textContent()) ?? '').trim()
  expect(shown).toMatch(/^([A-Z2-7]{4} )+[A-Z2-7]{1,4}$/)
  const next = shown.replace(/\s+/g, '')
  await shot(page, info, 'totp-replace')

  // Until confirmed, the old app's code still confirms a step-up — here, for
  // the replacement itself.
  const code = await freshCode()
  totpSecret = next
  lastStep = -1
  await page.getByTestId('totp-code').fill(await freshCode())
  await page.getByTestId('totp-confirm').click()
  const dialog = page.getByTestId('stepup-dialog')
  await expect(dialog).toBeVisible()
  await expect(dialog.getByTestId('stepup-what')).toContainText('Replace your authenticator app')
  await dialog.getByTestId('stepup-method-totp').click()
  await dialog.getByTestId('stepup-totp-code').fill(code)
  await dialog.getByTestId('stepup-totp-submit').click()
  await expect(dialog).toHaveCount(0)
  await expect(page.getByTestId('access-msg')).toContainText('Authenticator app replaced')
})

test('sign in with a password and an authenticator code', async ({ browser }, info) => {
  test.setTimeout(60_000)
  pwCtx = await newDeviceContext(browser, info)
  pwPage = await pwCtx.newPage()
  await pwPage.goto('/signin')
  await pwPage.getByTestId('method-password').click()
  await pwPage.getByTestId('signin-operator').fill('ops')
  await pwPage.getByTestId('signin-password').fill(PASSWORD)

  // A spent code is refused: the step the authenticator was confirmed with.
  await pwPage.getByTestId('signin-code').fill(totpAt(totpSecret, lastStep))
  await pwPage.getByTestId('signin-password-submit').click()
  await expect(pwPage.getByTestId('signin-error')).toBeVisible()
  expect((await authState(pwCtx)).signed_in).toBe(false)

  await pwPage.getByTestId('signin-code').fill(await freshCode())
  await pwPage.getByTestId('signin-password-submit').click()
  await pwPage.waitForURL(/#\/overview/, { timeout: 15_000 })
  expect(await authState(pwCtx)).toMatchObject({ signed_in: true, method: 'password', transport: 'http' })
})

test('sign in from another device: approve the login code with the CLI', async ({ browser }, info) => {
  const ctx = await newDeviceContext(browser, info)
  try {
    const p = await ctx.newPage()
    await p.goto('/signin')
    await p.getByTestId('signin-operator').fill('ops')
    await p.getByTestId('ssh-begin').click()
    await p.getByRole('tab', { name: 'Approve from another device' }).click()
    const code = ((await p.getByTestId('login-code').textContent()) ?? '').trim()
    expect(code).toMatch(/^[A-Z0-9]{4}-[A-Z0-9]{4}$/)
    await expect(p.getByTestId('ssh-approve-command')).toContainText(`approve-login ${code} --url ${remote} --operator ops`)

    // The CLI fetches the pending challenge, checks it names this origin and
    // operator, signs it with the key and approves; the waiting page polls in.
    const approve = () =>
      spawnSync(rt.bin, ['dashboard', 'approve-login', code, '--url', remote, '--operator', 'ops', '--key', rt.keyFile, '--yes'], {
        env: { PATH: process.env.PATH ?? '/usr/bin:/bin', HOME: path.dirname(rt.keyFile), TERM: 'dumb' },
        encoding: 'utf8',
      })
    const first = approve()
    expect(first.status, first.stderr).toBe(0)
    expect(first.stderr).toContain('approved')
    await p.waitForURL(/#\/overview/, { timeout: 15_000 })
    expect(await authState(ctx)).toMatchObject({ signed_in: true, method: 'ssh' })

    // The code is single use.
    const again = approve()
    expect(again.status).not.toBe(0)
  } finally {
    await ctx.close()
  }
})

test('approve a proposal with an authenticator-code step-up', async ({}, info) => {
  test.setTimeout(90_000)
  await pwPage.goto('/#/bots/sre/approvals')
  const review = pwPage.getByTestId('review-proposal').first()
  await expect(review).toBeVisible({ timeout: 10_000 })
  await review.click()
  await pwPage.getByTestId('approve').click()

  const dialog = pwPage.getByTestId('stepup-dialog')
  await expect(dialog).toContainText('Approve or deny')
  // The sheet carries the action it confirms, pretty-printed and readable in
  // place: it sits over the page that showed it.
  const action = dialog.getByTestId('stepup-action')
  await expect(action).toBeVisible()
  await expect(action).toContainText('box.run')
  expect(((await action.textContent()) ?? '').split('\n').length, 'pretty-printed over several lines').toBeGreaterThan(2)
  await fitsInside(action, dialog)
  const cancel = (await dialog.getByTestId('stepup-cancel').boundingBox())!
  expect(cancel.height, 'Cancel is a 44px tap target').toBeGreaterThanOrEqual(44)
  // No passkey on plain HTTP; the SSH key and the authenticator are offered.
  await expect(dialog.getByTestId('stepup-method-passkey')).toHaveCount(0)
  await expect(dialog.getByTestId('stepup-method-ssh')).toBeVisible()
  await dialog.getByTestId('stepup-method-totp').click()
  await dialog.getByTestId('stepup-totp-code').fill(await freshCode())
  await fitsInside(dialog.getByTestId('stepup-totp-submit'), dialog)
  await noHorizontalOverflow(pwPage)
  await shot(pwPage, info, 'stepup-totp')
  await dialog.getByTestId('stepup-totp-submit').click()
  await expect(pwPage.getByTestId('approval-done')).toContainText('Approved', { timeout: 15_000 })
})
