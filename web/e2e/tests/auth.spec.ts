// Auth e2e (design §6, §9): enrol a passkey, sign in, and approve a proposal
// behind a step-up — driven through a Chromium CDP virtual authenticator against
// a dedicated authenticated daemon. WebKit has no virtual authenticator, so these
// run on Chromium only. One shared context holds the enrolled passkey and the
// session across the two tests (a second enrolment would need the first passkey,
// which a fresh authenticator wouldn't have).
import { test, expect, type Page, type BrowserContext } from '@playwright/test'
import { startAuthDaemon, enroll, stop, type AuthRuntime } from '../lib/authdaemon'

test.describe.configure({ mode: 'serial' })

let rt: AuthRuntime
let ctx: BrowserContext
let page: Page

test.beforeAll(async ({ browser, browserName }) => {
  test.skip(browserName !== 'chromium', 'the CDP virtual authenticator is Chromium-only')
  const base = Number(process.env.DASH_E2E_PORT || 48766)
  rt = await startAuthDaemon(base + 1)

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

  // Enrol a passkey via the CLI's one-time localhost link, then sign in.
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

test('the Access screen lists the passkey and the session', async () => {
  await page.goto(`${rt.baseURL}/#/security`)
  await expect(page.getByTestId('passkey-list')).toContainText('e2e laptop')
  await expect(page.getByTestId('session-list')).toContainText('this one')
})

test('approving a proposal needs a passkey step-up', async () => {
  // Without a step-up token, the decision endpoint refuses (the gate).
  const refused = await page.evaluate(
    async ([bot, id]) => {
      const state = await fetch('/api/v1/auth/state', { credentials: 'same-origin' }).then((r) => r.json())
      const res = await fetch(`/api/v1/bots/${bot}/proposals/${id}/decision`, {
        method: 'POST',
        credentials: 'same-origin',
        headers: { 'Content-Type': 'application/json', 'X-Conductor-CSRF': state.csrf },
        body: JSON.stringify({ decision: 'deny' }),
      })
      return res.status
    },
    [rt.proposalBot, rt.proposalID],
  )
  expect(refused).toBe(401)

  // The full ceremony: approvalView -> step-up (virtual authenticator) -> decide,
  // raw fetch + navigator.credentials against the built app.
  const result = await page.evaluate(
    async ([bot, id]) => {
      const b64urlToBuf = (s: string) => {
        const pad = s.length % 4 === 0 ? '' : '='.repeat(4 - (s.length % 4))
        const bin = atob(s.replace(/-/g, '+').replace(/_/g, '/') + pad)
        const u = new Uint8Array(bin.length)
        for (let i = 0; i < bin.length; i++) u[i] = bin.charCodeAt(i)
        return u
      }
      const bufToB64url = (b: ArrayBuffer) => {
        const bytes = new Uint8Array(b)
        let s = ''
        for (const x of bytes) s += String.fromCharCode(x)
        return btoa(s).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '')
      }
      const state = await fetch('/api/v1/auth/state', { credentials: 'same-origin' }).then((r) => r.json())
      const jh = { 'Content-Type': 'application/json', 'X-Conductor-CSRF': state.csrf }
      const view = await fetch(`/api/v1/bots/${bot}/proposals/${id}/approval`, { credentials: 'same-origin' }).then((r) => r.json())
      const digest = view.digests.deny
      const begin = await fetch('/api/v1/auth/stepup/begin', {
        method: 'POST', credentials: 'same-origin', headers: jh, body: JSON.stringify({ purpose: 'approve', digest }),
      }).then((r) => r.json())
      const pk = { ...begin.options.publicKey }
      pk.challenge = b64urlToBuf(pk.challenge)
      if (Array.isArray(pk.allowCredentials)) pk.allowCredentials = pk.allowCredentials.map((c: { id: string }) => ({ ...c, id: b64urlToBuf(c.id) }))
      const cred = (await navigator.credentials.get({ publicKey: pk })) as PublicKeyCredential
      const resp = cred.response as AuthenticatorAssertionResponse
      const assertion = {
        id: cred.id, rawId: bufToB64url(cred.rawId), type: cred.type,
        response: {
          authenticatorData: bufToB64url(resp.authenticatorData),
          clientDataJSON: bufToB64url(resp.clientDataJSON),
          signature: bufToB64url(resp.signature),
          userHandle: resp.userHandle ? bufToB64url(resp.userHandle) : '',
        },
        clientExtensionResults: cred.getClientExtensionResults(),
      }
      const fin = await fetch('/api/v1/auth/stepup/finish', {
        method: 'POST', credentials: 'same-origin', headers: jh,
        body: JSON.stringify({ ceremony_id: begin.ceremony_id, response: assertion }),
      }).then((r) => r.json())
      const dec = await fetch(`/api/v1/bots/${bot}/proposals/${id}/decision`, {
        method: 'POST', credentials: 'same-origin',
        headers: { ...jh, 'X-Conductor-Stepup': fin.token },
        body: JSON.stringify({ decision: 'deny' }),
      })
      return { status: dec.status, body: await dec.json() }
    },
    [rt.proposalBot, rt.proposalID],
  )
  expect(result.status).toBe(200)
  expect(result.body.decision).toBe('deny')
})
