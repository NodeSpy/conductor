// A throwaway authenticated daemon for the Browsers + wallet suite (design §4.5):
// an operator (an ed25519 key), the dashboard, a browser config with one payment
// card and one profile (sources only), the operator as a payment approver, and
// one pending non-bot purchase seeded straight into the ledger so the approval
// endpoint has something real to decide. Hermetic — its own temp state, HOME and
// port, no live daemon/config/secrets. Reuses the auth suite's enrol + stop.
import { spawn, execFileSync, type ChildProcess } from 'node:child_process'
import { mkdtempSync, writeFileSync, mkdirSync, openSync, existsSync } from 'node:fs'
import { tmpdir } from 'node:os'
import path from 'node:path'
import http from 'node:http'
import { enroll, stop, type AuthRuntime } from './authdaemon'

export { enroll, stop } from './authdaemon'

function hermeticEnv(home: string): NodeJS.ProcessEnv {
  return { PATH: process.env.PATH ?? '/usr/bin:/bin', HOME: home, TMPDIR: process.env.TMPDIR ?? '/tmp', TERM: 'dumb' }
}

function resolveBin(): string {
  const fromEnv = process.env.CONDUCTOR_BIN
  const bin = fromEnv ? path.resolve(fromEnv) : path.resolve(__dirname, '..', '.bin', 'conductor')
  if (!existsSync(bin)) throw new Error(`conductor binary not found at ${bin} (run make dashboard-e2e or set CONDUCTOR_BIN)`)
  return bin
}

async function waitForStatus(baseURL: string, timeoutMs = 30_000): Promise<void> {
  const deadline = Date.now() + timeoutMs
  let last = ''
  while (Date.now() < deadline) {
    const ok = await new Promise<boolean>((resolve) => {
      const req = http.get(baseURL + '/api/v1/auth/state', (res) => {
        res.resume()
        resolve(res.statusCode === 200)
      })
      req.on('error', (e) => {
        last = String(e)
        resolve(false)
      })
      req.setTimeout(1000, () => {
        req.destroy()
        resolve(false)
      })
    })
    if (ok) return
    await new Promise((r) => setTimeout(r, 150))
  }
  throw new Error(`browser daemon did not come up at ${baseURL} within ${timeoutMs}ms (last: ${last})`)
}

export async function startBrowserDaemon(port: number): Promise<AuthRuntime> {
  const bin = resolveBin()
  const root = mkdtempSync(path.join(tmpdir(), 'dash-browser-e2e-'))
  const stateDir = path.join(root, 'state')
  const home = path.join(root, 'home')
  const config = path.join(root, 'config.yaml')
  const keyFile = path.join(root, 'id_ed25519')
  const env = hermeticEnv(home)

  execFileSync('ssh-keygen', ['-t', 'ed25519', '-N', '', '-C', 'ops@e2e', '-f', keyFile], { env, stdio: 'ignore' })
  const pub = execFileSync('cat', [keyFile + '.pub'], { env }).toString().trim()

  writeFileSync(
    config,
    [
      `store: { state_file: ${path.join(stateDir, 'state.json')} }`,
      `operators:`,
      `  ops: { ssh_key: "${pub}" }`,
      `dashboard: { enabled: true, listen: [localhost], port: ${port} }`,
      `connectors:`,
      `  box: { use: command }`,
      `browser:`,
      `  allow: [shop.example.com]`,
      `  cards:`,
      `    household:`,
      `      kind: virtual`,
      `      number: '\${env.CARD_NUMBER}'`,
      `      expiry: '\${env.CARD_EXPIRY}'`,
      `      cvc: '\${env.CARD_CVC}'`,
      `      merchants: ["https://shop.example.com"]`,
      `      identities: ["bot:atlas"]`,
      `      limits: { per_purchase: 120, per_day: 200, per_month: 500, currency: USD }`,
      // A second card the pending purchase uses, so freezing household (a
      // separate test) can't make the purchase unapprovable.
      `    shopping:`,
      `      kind: virtual`,
      `      number: '\${env.CARD2_NUMBER}'`,
      `      expiry: '\${env.CARD2_EXPIRY}'`,
      `      cvc: '\${env.CARD2_CVC}'`,
      `      merchants: ["https://shop.example.com"]`,
      `      identities: ["bot:atlas"]`,
      `      limits: { per_purchase: 120, per_day: 200, per_month: 500, currency: USD }`,
      `  profiles:`,
      `    home:`,
      `      identities: ["bot:atlas"]`,
      `      fields: { name: '\${env.HOME_NAME}', email: '\${env.HOME_EMAIL}', phone: '\${env.HOME_PHONE}' }`,
      `      sites: [{ origin: "https://shop.example.com", fields: [name, email, phone] }]`,
      `      visible: { email: fact }`,
      `  payments: { approvers: [ops] }`,
      ``,
    ].join('\n'),
    { mode: 0o600 },
  )

  // Seed one pending, non-bot purchase into the ledger (approvable directly, not
  // through a bot proposal). The digest is opaque: the dashboard reads it back
  // and binds the step-up to it, and ApproveWithin only checks it matches.
  const now = new Date()
  const expires = new Date(now.getTime() + 60 * 60 * 1000)
  const browserDir = path.join(stateDir, 'browser')
  mkdirSync(browserDir, { recursive: true, mode: 0o700 })
  const ledger = {
    next: 2,
    purchases: {
      pu1: {
        id: 'pu1',
        card: 'shopping',
        merchant: 'https://shop.example.com',
        items: [{ name: 'Widget', qty: 2, amount: 9.99 }],
        total: 19.98,
        currency: 'USD',
        why: 'Restock the widget shelf',
        identity: 'user',
        requester: 'user',
        status: 'pending',
        created: now.toISOString(),
        expires: expires.toISOString(),
        digest: 'seeded-digest',
      },
    },
    spend: [],
  }
  writeFileSync(path.join(browserDir, 'purchases.json'), JSON.stringify(ledger, null, 1), { mode: 0o600 })

  const baseURL = `http://localhost:${port}`
  const out = openSync(path.join(root, 'daemon.log'), 'a')
  const child: ChildProcess = spawn(bin, ['run', '--config', config], { env, detached: true, stdio: ['ignore', out, out] })
  child.unref()
  if (!child.pid) throw new Error('failed to spawn browser daemon')
  await waitForStatus(baseURL)

  return { bin, config, keyFile, port, baseURL, pid: child.pid, proposalBot: '', proposalID: '' }
}

// re-export for symmetry (tests import both from here)
export const _enroll = enroll
export const _stop = stop
