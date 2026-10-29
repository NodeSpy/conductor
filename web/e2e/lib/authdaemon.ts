// A dedicated, authenticated throwaway daemon for the auth suite — SEPARATE from
// the shared open daemon (lib/daemon.ts), so requiring sign-in here never affects
// the overview/config suites. It boots with an operator (an ed25519 key we
// generate), a bot with one seeded pending proposal, and the dashboard on its own
// port, then mints an enrolment ticket via the CLI (SSH-signed with our key).
import { spawn, execFileSync, type ChildProcess } from 'node:child_process'
import { mkdtempSync, writeFileSync, openSync, existsSync } from 'node:fs'
import { tmpdir, networkInterfaces } from 'node:os'
import path from 'node:path'
import http from 'node:http'
import { createHash } from 'node:crypto'

// actionDigest mirrors bot.ActionDigest: the sha256 of the canonical verb action
// (Go marshals the map with sorted keys), so an approval seeded here passes the
// gate's digest re-check when the approved verb executes.
function actionDigest(bot: string, uses: string, options: Record<string, unknown>): string {
  const canonical = JSON.stringify({ bot, kind: 'verb', options, uses, v: 1 })
  return createHash('sha256').update(canonical).digest('hex')
}

export interface AuthRuntime {
  bin: string
  config: string
  keyFile: string
  port: number
  baseURL: string // http://localhost:<port> — localhost, not 127.0.0.1, so RP "localhost" works
  pid: number
  proposalBot: string
  proposalID: string
  remoteURL?: string // http://<non-loopback ip>:<port> — the plain-HTTP bind listener, when asked for
}

// Options for a daemon that also listens on a non-loopback address.
export interface AuthDaemonOptions {
  // A dashboard.bind listener on this port over plain HTTP (allow_plain_http),
  // at the first non-loopback IPv4 address of the machine (the container's).
  bindPort?: number
}

// externalIPv4 is the first non-internal IPv4 address — inside the e2e container
// its bridge address, which a browser treats as a non-secure, non-localhost origin.
export function externalIPv4(): string {
  for (const addrs of Object.values(networkInterfaces())) {
    for (const a of addrs ?? []) {
      if (a.family === 'IPv4' && !a.internal) return a.address
    }
  }
  throw new Error('no non-loopback IPv4 address to bind the plain-HTTP listener to')
}

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
  throw new Error(`auth daemon did not come up at ${baseURL} within ${timeoutMs}ms (last: ${last})`)
}

export async function startAuthDaemon(port: number, opts: AuthDaemonOptions = {}): Promise<AuthRuntime> {
  const bin = resolveBin()
  const root = mkdtempSync(path.join(tmpdir(), 'dash-auth-e2e-'))
  const stateDir = path.join(root, 'state')
  const home = path.join(root, 'home')
  const config = path.join(root, 'config.yaml')
  const keyFile = path.join(root, 'id_ed25519')
  const env = hermeticEnv(home)

  // An ed25519 operator key (no passphrase); its public half is the operator's
  // ssh_key, and the CLI signs the enrolment ticket with the private half.
  execFileSync('ssh-keygen', ['-t', 'ed25519', '-N', '', '-C', 'ops@e2e', '-f', keyFile], { env, stdio: 'ignore' })
  const pub = execFileSync('cat', [keyFile + '.pub'], { env }).toString().trim()

  const ip = opts.bindPort ? externalIPv4() : ''
  const dashboard = opts.bindPort
    ? [
        `dashboard:`,
        `  enabled: true`,
        `  listen: [localhost]`,
        `  port: ${port}`,
        `  bind:`,
        `    - { address: "${ip}:${opts.bindPort}", hostnames: ["${ip}"], allow_plain_http: true }`,
      ]
    : [`dashboard: { enabled: true, listen: [localhost], port: ${port} }`]

  const now = new Date()
  const expires = new Date(now.getTime() + 60 * 60 * 1000)
  writeFileSync(
    config,
    [
      `store: { state_file: ${path.join(stateDir, 'state.json')} }`,
      `operators:`,
      `  ops: { ssh_key: "${pub}" }`,
      ...dashboard,
      `runtimes:`,
      `  claude-cli: { use: cli, tool: claude-code }`,
      `connectors:`,
      `  box: { use: command }`,
      `teams:`,
      `  platform:`,
      `    description: Platform services`,
      `    operators: [ops]`,
      `    routing: { lead: atlas, fallback: atlas }`,
      `bots:`,
      `  atlas:`,
      `    description: Platform SRE — keeps services healthy`,
      `    team: platform`,
      `    operators: [ops]`,
      `    autonomy: { shadow: false, tiers: { reversible: [box.run] }, promote: [box.run] }`,
      `    worker: { runtime: claude-cli }`,
      `    skill: { verbs: [box.run] }`,
      `    display_name: Atlas`,
      `    avatar:`,
      `      shape: hexagon`,
      `      eyes: round`,
      `      color: teal`,
      `  sre:`,
      `    description: Reliability — proposes the risky ones`,
      `    team: platform`,
      `    operators: [ops]`,
      `    autonomy: { shadow: false }`,
      `    worker: { runtime: claude-cli }`,
      `    skill: { verbs: [box.run] }`,
      ``,
    ].join('\n'),
    { mode: 0o600 },
  )

  // Seed one pending proposal straight into the ledger (bots.json), so the
  // approval endpoint has something real to decide without running an agent.
  const options = { command: 'true' }
  const bots = {
    sre: {
      name: 'sre',
      proposals: {
        p1: {
          id: 'p1',
          kind: 'verb',
          tier: 'reversible',
          uses: 'box.run',
          status: 'pending',
          // The real canonical digest, so the gate's re-check passes when the
          // approved verb executes (box.run `true` → success).
          digest: actionDigest('sre', 'box.run', options),
          requester_class: 'user',
          created: now.toISOString(),
          expires: expires.toISOString(),
          options,
          // The token-bound caller identity the approved verb runs under.
          identity: { Verbs: ['box.run'], bot: 'sre', bot_role: 'home' },
          summary: 'run a command',
        },
      },
    },
  }
  execFileSync('mkdir', ['-p', stateDir], { env })
  writeFileSync(path.join(stateDir, 'bots.json'), JSON.stringify(bots, null, 1), { mode: 0o600 })

  const baseURL = `http://localhost:${port}`
  const out = openSync(path.join(root, 'daemon.log'), 'a')
  const child: ChildProcess = spawn(bin, ['run', '--config', config], { env, detached: true, stdio: ['ignore', out, out] })
  child.unref()
  if (!child.pid) throw new Error('failed to spawn auth daemon')
  await waitForStatus(baseURL)
  const remoteURL = opts.bindPort ? `http://${ip}:${opts.bindPort}` : undefined
  if (remoteURL) await waitForStatus(remoteURL)

  return { bin, config, keyFile, port, baseURL, pid: child.pid, proposalBot: 'sre', proposalID: 'p1', remoteURL }
}

// enroll mints a one-time ticket via the CLI (SSH-signed with our key) and returns
// the URL secret and pairing code the enrolment page needs.
export function enroll(rt: AuthRuntime): { secret: string; code: string } {
  const outBuf = execFileSync(rt.bin, ['dashboard', 'enroll', '--operator', 'ops', '--key', rt.keyFile, '--config', rt.config], {
    env: hermeticEnv(path.dirname(rt.keyFile)),
  }).toString()
  const url = /http:\/\/localhost:\d+\/enroll#t=(\S+)/.exec(outBuf)
  const code = /pairing code:\s*(\S+)/.exec(outBuf)
  if (!url || !code) throw new Error('could not parse enroll output:\n' + outBuf)
  return { secret: url[1], code: code[1] }
}

export function stop(rt: AuthRuntime): void {
  try {
    process.kill(-rt.pid, 'SIGTERM')
  } catch {
    try {
      process.kill(rt.pid, 'SIGTERM')
    } catch {
      /* gone */
    }
  }
}
