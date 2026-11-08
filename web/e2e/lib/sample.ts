// A rich, reusable "populated" daemon fixture for the dashboard — used by both
// the Playwright e2e suite and the /var/tmp/dashempty conformance harness
// (shoot.js), so a side-by-side comparison against the prototype's `full` demo
// day has something real to show on every screen.
//
// Everything here goes through real daemon mechanisms: a config whose steps
// really execute (box.run), real CLI trigger dispatch, and the real dashboard
// HTTP API (ticket/comment/wallet calls) exactly as the browser would send
// them. Two exceptions write files directly because there is no API path at
// all (see the comments at each site): the audit log and the config-history
// backup. Both are also how the existing (thinner) shoot.js fixture already
// does it.
//
// This is deliberately a SEPARATE entry point from lib/daemon.ts's `start`/
// `startEmpty` — those stay exactly as they are so the existing functional
// specs that assert on their specific thin fixture keep passing. Use
// `startSample`/`seedSample` where a screen needs the fuller picture (mainly
// the conformance harness and any new visual-coverage specs).
import { spawn, execFileSync, type ChildProcess } from 'node:child_process'
import { mkdtempSync, mkdirSync, writeFileSync, appendFileSync, openSync, existsSync } from 'node:fs'
import { tmpdir } from 'node:os'
import path from 'node:path'
import http from 'node:http'
import { createHash } from 'node:crypto'
import { hermeticPath } from './nopaseo'

export interface Runtime {
  bin: string
  config: string
  stateDir: string
  home: string
  port: number
  baseURL: string
  pid: number
  logFile: string
}

function hermeticEnv(home: string): NodeJS.ProcessEnv {
  return {
    PATH: hermeticPath(),
    HOME: home,
    TMPDIR: process.env.TMPDIR ?? '/tmp',
    TERM: 'dumb',
  }
}

function resolveBin(binOverride?: string): string {
  const fromEnv = binOverride ?? process.env.CONDUCTOR_BIN
  const bin = fromEnv ? path.resolve(fromEnv) : path.resolve(__dirname, '..', '.bin', 'conductor')
  if (!existsSync(bin)) {
    throw new Error(`conductor binary not found at ${bin}. Build it first (\`make dashboard-e2e\`) or set CONDUCTOR_BIN.`)
  }
  return bin
}

async function waitForStatus(baseURL: string, timeoutMs = 30_000): Promise<void> {
  const deadline = Date.now() + timeoutMs
  let lastErr = ''
  while (Date.now() < deadline) {
    const ok = await new Promise<boolean>((resolve) => {
      const req = http.get(baseURL + '/api/v1/status', (res) => {
        res.resume()
        resolve(res.statusCode === 200)
      })
      req.on('error', (e) => {
        lastErr = String(e)
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
  throw new Error(`dashboard did not come up at ${baseURL} within ${timeoutMs}ms (last error: ${lastErr})`)
}

// actionDigest mirrors bot.ActionDigest (internal/bot), so a proposal seeded
// straight into bots.json passes the gate's digest re-check if ever approved.
function actionDigest(bot: string, uses: string, options: Record<string, unknown>): string {
  const canonical = JSON.stringify({ bot, kind: 'verb', options, uses, v: 1 })
  return createHash('sha256').update(canonical).digest('hex')
}

function apiRequest(method: string, baseURL: string, p: string, body?: unknown): Promise<any> {
  return new Promise((resolve, reject) => {
    const data = body ? Buffer.from(JSON.stringify(body)) : undefined
    const req = http.request(
      baseURL + p,
      { method, headers: data ? { 'content-type': 'application/json', 'content-length': data.length } : {} },
      (res) => {
        const chunks: Buffer[] = []
        res.on('data', (c) => chunks.push(c))
        res.on('end', () => {
          const text = Buffer.concat(chunks).toString('utf8')
          if ((res.statusCode || 0) >= 400) return reject(new Error(`${method} ${p}: ${res.statusCode} ${text}`))
          resolve(text ? JSON.parse(text) : {})
        })
      },
    )
    req.on('error', reject)
    if (data) req.write(data)
    req.end()
  })
}
const apiPost = (baseURL: string, p: string, body?: unknown) => apiRequest('POST', baseURL, p, body)
const apiPatch = (baseURL: string, p: string, body?: unknown) => apiRequest('PATCH', baseURL, p, body)

// runTrigger dispatches a manual trigger over the CLI, retrying briefly: the
// control socket can come up a beat after the dashboard's own HTTP status
// endpoint does (waitForStatus only proves the latter), and a dispatch
// attempted in that gap fails outright rather than queuing.
function runTrigger(rt: Runtime, name: string, env: NodeJS.ProcessEnv, attempts = 20): void {
  for (let i = 0; i < attempts; i++) {
    try {
      execFileSync(rt.bin, ['run', name, '--config', rt.config], { env, stdio: 'ignore' })
      return
    } catch (e) {
      if (i === attempts - 1) throw e
      execFileSync('sleep', ['0.2'])
    }
  }
}

// waitForRunCount polls /api/v1/runs until `want` runs of the given workflow
// have left the `running` state (ok, failed, retried or interrupted) — so the
// caller can dispatch a quick trigger and know it has actually finished
// before moving on (see seedSample's comment on why this matters: the daemon
// dispatches a trigger asynchronously and processes dispatched runs one at a
// time).
async function waitForRunCount(baseURL: string, workflow: string, want: number, timeoutMs = 10_000): Promise<void> {
  const deadline = Date.now() + timeoutMs
  while (Date.now() < deadline) {
    const resp = await apiRequest('GET', baseURL, '/api/v1/runs?limit=200')
    const done = (resp.runs ?? []).filter((r: any) => r.workflow === workflow && r.status !== 'running').length
    if (done >= want) return
    await new Promise((r) => setTimeout(r, 100))
  }
  throw new Error(`timed out waiting for ${want} finished run(s) of ${workflow}`)
}

export interface SampleOptions {
  // Pre-resolved binary path (shoot.js already knows it; the e2e suite
  // resolves it from CONDUCTOR_BIN / .bin/conductor by default).
  bin?: string
  // Root directory to build the throwaway HOME/state/config under. Defaults
  // to a fresh dir under the OS tmpdir (matches lib/daemon.ts's existing
  // convention). The conformance harness passes one under /var/tmp/dashempty
  // instead, per its own hermeticity rule (never /tmp for its own scratch).
  root?: string
}

// sampleConfigYAML is the config a rich "populated" daemon boots with. It is
// intentionally close to (and was grown out of) the fixture
// /var/tmp/dashempty/fix/home/shoot.js already proved out for the phase 1-8
// conformance rounds — see that file's history for the per-screen reasoning
// behind each section. This adds:
//   - a `release` trigger whose second step fails for real (box.run `false`),
//     so a failed run has a real failed step and a real "not reached" step
//     after it (the dashboard synthesizes "idle" for unexecuted steps from
//     the workflow's own step list — internal/dashboard/runs_detail.go — no
//     seeding needed for that part).
//   - a `backfill` trigger whose one step sleeps, dispatched but not awaited,
//     so one run is still genuinely `running` when the screenshot is taken.
//   - a second team (`support`) with a bot in practice/shadow mode
//     (autonomy.shadow: true), alongside the existing `platform` team and the
//     teamless `scout` bot — "bots in several teams incl. one shadow bot".
//   - a second pending proposal (on `scout`), so Needs-you shows more than
//     one approval kind alongside the pending/subscription purchases already
//     seeded below.
//   - a fuller ticket board: one ticket in each of the six real statuses
//     (draft, ready, in_progress, waiting, done, declined).
//
// Not reproduced, with the real reason (see the report, not repeated per call
// site): chat with unread messages, and additional real sign-in/session
// records, both require a configured operator — which conflicts with the
// `allow_open` this fixture relies on for unauthenticated ticket/wallet
// seeding (internal/bot/chat.go's ChatOperator requires a non-empty
// cfg.Operators; internal/dashboard/auth's session/login endpoints exist but
// need the same). A connector "in several states" (connected/error/disabled)
// has no backing field in the data model at all — internal/dashboard/
// sections.go only ever reports a connector's configured `use:`, never a
// live health state — so "several states" here means several distinct
// connector *kinds* (command, webhook), not several health states of one.
export function sampleConfigYAML(opts: { port: number; stateDir: string }): string {
  const { port, stateDir } = opts
  return (
    [
      `store: { state_file: ${path.join(stateDir, 'state.json')}, audit_log: ${path.join(stateDir, 'audit.jsonl')} }`,
      `dashboard:`,
      `  enabled: true`,
      `  listen: [localhost]`,
      `  auth: { allow_open: true }`,
      `  port: ${port}`,
      `connectors:`,
      `  box: { use: command }`,
      `  hooks: { use: webhook }`,
      `hosts:`,
      `  build-box:`,
      `    host: build01.example.com`,
      `    user: ci`,
      `    env:`,
      `      LOG_LEVEL: debug`,
      `      DEPLOY_TOKEN: 'env:DEPLOY_TOKEN'`,
      `vaults:`,
      `  main: { type: conductor }`,
      `packs:`,
      `  review:`,
      `    use: ./packs/review`,
      `    connectors: { command: box }`,
      `settings:`,
      `  org: acme`,
      `  review_channel: "#reviews"`,
      `  deploy_repo: acme/app`,
      `  api_token: '{{ vault "main" "api-token" }}'`,
      `triggers:`,
      `  - name: deploy`,
      `    on: manual`,
      `    repo: acme/app`,
      `    steps: [{ uses: box.run, options: { command: "true" } }]`,
      `  - name: build`,
      `    on: manual`,
      `    repo: acme/widgets`,
      `    steps: [{ uses: box.run, options: { command: "true" } }]`,
      // release: a real multi-step failure. box.run (internal/connector/
      // command.go) reports a command's exit_code as plain output and never
      // fails the step itself on a nonzero exit (shell semantics, not a
      // bug) — so step 2 runs the real failing command and records its real
      // stderr, and step 3 is a real `assert` helper step that genuinely
      // fails the run on that exit code. Step 4 is never reached — the
      // dashboard's own run-detail projection synthesizes "idle" for steps
      // past the failure point by diffing the workflow's configured steps
      // against the run record (internal/dashboard/runs_detail.go), so
      // nothing extra needs seeding for the "not reached" state to show up.
      `  - name: release`,
      `    on: manual`,
      `    repo: acme/app`,
      `    steps:`,
      `      - id: build`,
      `        uses: box.run`,
      `        options: { command: "true" }`,
      `      - id: smoketest`,
      `        uses: box.run`,
      `        options: { command: "echo 'checkout 500s climbing (5xx rate 12% over 3m)' >&2; exit 1" }`,
      `      - id: checksmoketest`,
      `        assert: "{{.smoketest.exit_code}} == 0"`,
      `      - id: promote`,
      `        uses: box.run`,
      `        options: { command: "true" }`,
      // backfill: dispatched but left to sleep — the run genuinely stays
      // `running` for the life of the screenshot session (see seedSample,
      // which triggers but does not wait on this one).
      `  - name: backfill`,
      `    on: manual`,
      `    repo: acme/widgets`,
      `    steps: [{ uses: box.run, options: { command: "sleep 180" } }]`,
      `runtimes:`,
      `  agent: { use: cli, tool: claude-code }`,
      `teams:`,
      `  platform:`,
      `    description: Keeps the platform healthy`,
      `    routing: { lead: platform-sre, fallback: platform-sre }`,
      `  support:`,
      `    description: Customer-facing triage, still in practice mode`,
      `    routing: { lead: trial-bot, fallback: trial-bot }`,
      `bots:`,
      `  platform-sre:`,
      `    description: Platform reliability — keeps services healthy`,
      `    team: platform`,
      `    autonomy: { shadow: false, tiers: { reversible: [box.run] }, promote: [box.run] }`,
      `    worker: { runtime: agent }`,
      `    skill: { verbs: [box.run] }`,
      `    display_name: Platform SRE`,
      `    avatar: { shape: hexagon, eyes: round, color: teal }`,
      `  scout:`,
      `    description: Watches for drift and proposes the risky changes`,
      `    autonomy: { shadow: false }`,
      `    worker: { runtime: agent }`,
      `    skill: { verbs: [box.run] }`,
      `    avatar: { shape: blob, eyes: focused, color: violet }`,
      // trial-bot: practice/shadow mode (actions evaluated and logged, never
      // really run — internal/bot/teams.go's ShadowMode()), in its own team,
      // so Bots shows a roster that spans more than one team plus a shadow
      // badge.
      `  trial-bot:`,
      `    description: Drafts triage replies for review before going live`,
      `    team: support`,
      `    autonomy: { shadow: true }`,
      `    worker: { runtime: agent }`,
      `    skill: { verbs: [box.run] }`,
      `    display_name: Trial bot`,
      `    avatar: { shape: diamond, eyes: round, color: sand }`,
      `browser:`,
      `  allow: [shop.example.com, grafana.example.com, statuspage.example.com, app.example.com, console.example.com]`,
      `  credentials:`,
      `    - name: grafana`,
      `      origin: https://grafana.example.com`,
      `      identities: ["bot:scout"]`,
      `      username: ops`,
      `      password: '\${env.GRAFANA_PW}'`,
      `  profiles:`,
      `    home:`,
      `      identities: ["bot:scout"]`,
      `      fields: { name: '\${env.HOME_NAME}', email: '\${env.HOME_EMAIL}', phone: '\${env.HOME_PHONE}', birthday: '\${env.HOME_BIRTHDAY}', 'address.country': '\${env.HOME_COUNTRY}' }`,
      `      sites: [{ origin: "https://shop.example.com", fields: [name, email, phone] }]`,
      `      visible: { email: fact, birthday: exists }`,
      ``,
    ].join('\n') + '\n'
  )
}

function writeReviewPack(root: string): void {
  const packDir = path.join(root, 'packs', 'review')
  mkdirSync(packDir, { recursive: true })
  writeFileSync(
    path.join(packDir, 'conductor-pack.yaml'),
    [
      'pack:',
      '  name: demo-review',
      '  version: 0.1.0',
      '  description: a tiny local pack for the dashboard sample fixture',
      '  requires:',
      '    connectors: { command: "*" }',
      'workflows:',
      '  review-flow:',
      '    steps:',
      '      - id: summarize',
      '        uses: command.run',
      '        options: { command: "true" }',
      'triggers:',
      '  - name: on_demand',
      '    on: manual',
      '    steps: [{ uses: command.run, options: { command: "true" } }]',
      '',
    ].join('\n'),
  )
}

// startSample boots the rich fixture daemon (config only, not yet seeded —
// call seedSample next). Mirrors lib/daemon.ts's `start`/Runtime shape so
// existing spec helpers (and shoot.js) can use it as a drop-in.
export async function startSample(port: number, opts: SampleOptions = {}): Promise<Runtime> {
  const bin = resolveBin(opts.bin)
  const root = opts.root ?? mkdtempSync(path.join(tmpdir(), 'dash-e2e-sample-'))
  mkdirSync(root, { recursive: true })
  const stateDir = path.join(root, 'state')
  const home = path.join(root, 'home')
  const config = path.join(root, 'config.yaml')
  const logFile = path.join(root, 'daemon.log')
  const baseURL = `http://127.0.0.1:${port}`

  writeFileSync(config, sampleConfigYAML({ port, stateDir }), { mode: 0o600 })
  writeReviewPack(root)

  const env = hermeticEnv(home)
  // Fake values for ${env.*} refs in the browser: config (never real-looking,
  // never rendered — the dashboard masks secret values everywhere).
  Object.assign(env, {
    GRAFANA_PW: 'demo-not-a-real-password',
    HOME_NAME: 'Jordan Rivas',
    HOME_EMAIL: 'jordan@example.com',
    HOME_PHONE: '+1 555 0100',
    HOME_BIRTHDAY: '1990-04-12',
    HOME_COUNTRY: 'US',
  })

  // A local pack source needs one `conductor init` to vendor + lock it before
  // the daemon can boot (config.Load refuses an unresolved pack) — always
  // with an explicit --config and this throwaway HOME.
  execFileSync(bin, ['init', '--config', config], { env, stdio: 'ignore' })

  mkdirSync(stateDir, { recursive: true, mode: 0o700 })

  // A few audit entries (Logs & audit): there is no config-apply in this
  // fixture to generate one for real, so these are written straight into the
  // JSONL the daemon already reads from (newest-last on disk; the reader
  // reverses it) — same technique the existing shoot.js fixture uses, and for
  // the same reason (no API path exists to produce a believable history of
  // past events on a freshly-booted daemon).
  const auditNow = Date.now()
  const auditLines = [
    { ts: new Date(auditNow - 3 * 60 * 60 * 1000).toISOString(), event: 'config_apply', who: 'operator:ops', detail: 'applied 1 change · backup config.yaml.bak-20261105' },
    { ts: new Date(auditNow - 2 * 60 * 60 * 1000).toISOString(), event: 'signin', who: 'operator:ops', detail: 'passkey · this device' },
    { ts: new Date(auditNow - 60 * 60 * 1000).toISOString(), event: 'proposal_approved', who: 'operator:ops', detail: 'platform-sre · restart the frontend service' },
  ]
  writeFileSync(path.join(stateDir, 'audit.jsonl'), auditLines.map((l) => JSON.stringify(l)).join('\n') + '\n', { mode: 0o600 })

  // Config history (System · Config → Applied changes): one backup + its
  // matching audit entry, for the same reason as above — no config-apply ran
  // here to produce a real one. internal/dashboard/configedit reads this
  // back exactly as it would a real backup.
  const histDir = path.join(root, '.conductor-config-history')
  mkdirSync(histDir, { recursive: true })
  const liveText = sampleConfigYAML({ port, stateDir })
  const beforeText = liveText.replace('org: acme', 'org: acme-inc')
  const sha = createHash('sha256').update(beforeText).digest('hex').slice(0, 12)
  const histTS = new Date(auditNow - 4 * 60 * 60 * 1000)
  const histID = `${histTS.toISOString().replace(/[-:]/g, '').replace(/\.\d+Z$/, 'Z')}-${sha}`
  writeFileSync(path.join(histDir, `${histID}.yaml`), beforeText, { mode: 0o600 })
  const histAudit = { ts: histTS.toISOString(), event: 'dashboard.config.apply', operator: 'ops', sha: 'deadbeef0001', backup: histID, stepup: false }
  appendFileSync(path.join(stateDir, 'audit.jsonl'), JSON.stringify(histAudit) + '\n', { mode: 0o600 })

  const out = openSync(logFile, 'a')
  const child: ChildProcess = spawn(bin, ['run', '--config', config], { env, detached: true, stdio: ['ignore', out, out] })
  child.unref()
  if (!child.pid) throw new Error('failed to spawn sample daemon')
  await waitForStatus(baseURL)

  return { bin, config, stateDir, home, port, baseURL, pid: child.pid, logFile }
}

// seedSample populates a running sample daemon with run history, pending
// approvals, purchases, wallet logins/cards and tickets — all through real
// trigger dispatch and the real dashboard HTTP API, with the two
// direct-to-ledger exceptions noted inline (bots.json / purchases.json: there
// is no API to pre-create a proposal or a purchase without an agent/bot
// actually proposing one, so these are written the same way the daemon itself
// would persist them, then the daemon is restarted to pick them up).
export async function seedSample(rt: Runtime): Promise<void> {
  const env = hermeticEnv(rt.home)

  // Real run history: ok x2, and a real multi-step failure. `conductor run`
  // only dispatches (it returns as soon as the trigger is accepted on the
  // control socket, not when the run finishes), and the engine processes
  // dispatched runs one at a time — so this waits for each of these fast,
  // genuinely-finished runs to land before moving on, rather than racing the
  // engine with the restart below (an earlier version of this fixture raced
  // it: the restart's SIGTERM landed before the engine had even started most
  // of them, and every run past the first came back `interrupted`).
  const seenCount: Record<string, number> = {}
  for (const name of ['deploy', 'build', 'deploy', 'release']) {
    runTrigger(rt, name, env)
    seenCount[name] = (seenCount[name] ?? 0) + 1
    await waitForRunCount(rt.baseURL, name, seenCount[name])
  }

  // Two pending proposals, on two different bots/tiers, seeded straight into
  // the ledger (there is no API to fabricate a proposal without a bot really
  // proposing one — the daemon must be stopped while bots.json is rewritten,
  // same as the existing shoot.js/authdaemon.ts fixtures do).
  const now = new Date()
  const expires = new Date(now.getTime() + 60 * 60 * 1000)
  const restartOptions = { command: 'true' }
  const driftOptions = { command: 'echo drift-check' }
  const bots = {
    'platform-sre': {
      name: 'platform-sre',
      proposals: {
        p1: {
          id: 'p1',
          kind: 'verb',
          tier: 'reversible',
          uses: 'box.run',
          status: 'pending',
          digest: actionDigest('platform-sre', 'box.run', restartOptions),
          requester_class: 'user',
          created: now.toISOString(),
          expires: expires.toISOString(),
          options: restartOptions,
          identity: { Verbs: ['box.run'], bot: 'platform-sre', bot_role: 'home' },
          summary: 'restart the frontend service',
        },
      },
    },
    scout: {
      name: 'scout',
      proposals: {
        p1: {
          id: 'p1',
          kind: 'verb',
          tier: 'promote',
          uses: 'box.run',
          status: 'pending',
          digest: actionDigest('scout', 'box.run', driftOptions),
          requester_class: 'agent',
          created: new Date(now.getTime() - 20 * 60 * 1000).toISOString(),
          expires: new Date(now.getTime() + 40 * 60 * 1000).toISOString(),
          options: driftOptions,
          identity: { Verbs: ['box.run'], bot: 'scout', bot_role: 'home' },
          summary: 'promote the nightly drift check to run unattended',
        },
      },
    },
  }

  // Purchases on the household card: one pending, one a pending
  // subscription, one already paid above the approved amount — so the
  // approval, the subscription flag and the receipt section each have a real
  // example. Same "write the ledger, restart the daemon" technique as the
  // proposals above (no API creates a purchase without a bot requesting one).
  const day = 24 * 60 * 60 * 1000
  const paidAt = new Date(now.getTime() - 3 * day)
  const ledger = {
    next: 6,
    purchases: {
      pu1: {
        id: 'pu1',
        card: 'household',
        merchant: 'https://shop.example.com',
        items: [{ name: 'Replacement filter', qty: 2, amount: 24.5 }],
        total: 49.0,
        currency: 'USD',
        why: 'The old filter is past its service date',
        identity: 'bot:scout',
        bot: 'scout',
        requester: 'bot:scout',
        status: 'pending',
        created: now.toISOString(),
        expires: new Date(now.getTime() + 60 * 60 * 1000).toISOString(),
        digest: 'seeded-digest-1',
      },
      pu2: {
        id: 'pu2',
        card: 'household',
        merchant: 'https://shop.example.com',
        items: [{ name: 'Cloud backup storage', qty: 1, amount: 6.0 }],
        total: 6.0,
        currency: 'USD',
        recurring: true,
        interval: 'month',
        why: 'Off-site backup for the household share',
        identity: 'bot:scout',
        bot: 'scout',
        requester: 'bot:scout',
        status: 'pending',
        created: new Date(now.getTime() - 10 * 60 * 1000).toISOString(),
        expires: new Date(now.getTime() + 50 * 60 * 1000).toISOString(),
        digest: 'seeded-digest-2',
      },
      pu3: {
        id: 'pu3',
        card: 'household',
        merchant: 'https://shop.example.com',
        items: [{ name: 'Air filters (pack of 4)', qty: 1, amount: 49.0 }],
        total: 49.0,
        currency: 'USD',
        why: 'Quarterly restock',
        identity: 'bot:scout',
        bot: 'scout',
        requester: 'bot:scout',
        status: 'completed',
        created: new Date(paidAt.getTime() - 20 * 60 * 1000).toISOString(),
        decided: new Date(paidAt.getTime() - 15 * 60 * 1000).toISOString(),
        expires: new Date(paidAt.getTime() + 40 * 60 * 1000).toISOString(),
        approver: 'operator:ops',
        digest: 'seeded-digest-3',
        receipt: {
          order_number: 'ORD-48213',
          total: 54.2,
          currency: 'USD',
          at: paidAt.toISOString(),
          note: 'Charged $5.20 more than approved',
        },
      },
      // Two an agent you started asked for (not a bot's, so the operator
      // decides them on the purchase page itself; a bot's purchase is decided
      // on its proposal): a one-off and a subscription.
      pu4: {
        id: 'pu4',
        card: 'household',
        merchant: 'https://shop.example.com',
        items: [{ name: 'USB-C dock', qty: 1, amount: 39.0 }],
        total: 39.0,
        currency: 'USD',
        why: 'The desk dock stopped charging the laptop',
        identity: 'dir:/home/ops/house',
        requester: 'operator:ops',
        status: 'pending',
        created: new Date(now.getTime() - 5 * 60 * 1000).toISOString(),
        expires: new Date(now.getTime() + 55 * 60 * 1000).toISOString(),
        digest: 'seeded-digest-4',
      },
      pu5: {
        id: 'pu5',
        card: 'household',
        merchant: 'https://shop.example.com',
        items: [{ name: 'Password manager, family plan', qty: 1, amount: 4.99 }],
        total: 4.99,
        currency: 'USD',
        recurring: true,
        interval: 'month',
        why: 'Shared vault for the household',
        identity: 'dir:/home/ops/house',
        requester: 'operator:ops',
        status: 'pending',
        created: new Date(now.getTime() - 2 * 60 * 1000).toISOString(),
        expires: new Date(now.getTime() + 58 * 60 * 1000).toISOString(),
        digest: 'seeded-digest-5',
      },
    },
    spend: [{ card: 'household', identity: 'bot:scout', bot: 'scout', amount: 54.2, currency: 'USD', purchase: 'pu3', at: paidAt.toISOString() }],
  }

  process.kill(rt.pid, 'SIGTERM')
  await new Promise((r) => setTimeout(r, 400))
  writeFileSync(path.join(rt.stateDir, 'bots.json'), JSON.stringify(bots, null, 1), { mode: 0o600 })
  const browserDir = path.join(rt.stateDir, 'browser')
  mkdirSync(browserDir, { recursive: true, mode: 0o700 })
  writeFileSync(path.join(browserDir, 'purchases.json'), JSON.stringify(ledger, null, 1), { mode: 0o600 })
  const out = openSync(rt.logFile, 'a')
  const child = spawn(rt.bin, ['run', '--config', rt.config], { env, detached: true, stdio: ['ignore', out, out] })
  child.unref()
  rt.pid = child.pid!
  await waitForStatus(rt.baseURL)

  // The rest of the wallet, through the real dashboard API (unauthenticated —
  // this daemon serves open, dashboard.auth.allow_open) so cards get a real
  // brand/last4 (cardMeta only runs on this path) and every add is
  // classified/validated exactly as the dashboard's own calls are.
  const walletOp = (cmd: string, req: unknown) => apiPost(rt.baseURL, '/api/v1/browser/op', { cmd, req })
  await walletOp('browser-login-add', {
    name: 'statuspage', method: 'password', origins: ['https://statuspage.example.com'], bindings: ['bot:scout'],
    values: { username: 'ops', password: 'demo-not-a-real-password' },
  })
  await walletOp('browser-login-add', {
    name: 'app', method: 'password', origins: ['https://app.example.com'], bindings: ['bot:scout'], approval: true,
    values: { username: 'scout-bot', password: 'demo-not-a-real-password-2' },
  })
  await walletOp('browser-login-add', { name: 'console', method: 'session', site: 'console.example.com', bindings: ['bot:scout'] })
  await walletOp('browser-card-add', {
    name: 'household', kind: 'virtual', merchants: ['https://shop.example.com'], identities: ['bot:scout'],
    limits: { PerPurchase: 120, PerDay: 200, PerMonth: 500, Currency: 'USD' },
    values: { number: '4111111111111111', expiry: '12/30', cvc: '123' },
  })
  await walletOp('browser-card-add', {
    name: 'backup', kind: 'virtual', merchants: [], identities: ['bot:scout'],
    limits: { PerPurchase: 50, PerDay: 50, PerMonth: 150, Currency: 'USD' },
    values: { number: '5500000000000004', expiry: '09/29', cvc: '456' },
  })
  await walletOp('browser-card-add', {
    name: 'travel', kind: 'single-use', merchants: ['https://shop.example.com'], identities: ['bot:scout'],
    limits: { PerPurchase: 30, PerDay: 30, PerMonth: 90, Currency: 'USD' },
    values: { number: '340000000000009', expiry: '03/28', cvc: '1234' },
  })
  await walletOp('browser-card-freeze', { name: 'travel' })
  await walletOp('browser-profile-add', {
    name: 'work', identities: ['bot:scout'], sites: [{ origin: 'https://app.example.com', fields: ['name', 'email'] }], visible: {},
    values: { name: 'Jordan Rivas', email: 'jordan.rivas@workmail.example.com' },
  })

  // Tickets: one in each real status (internal/store/tickets.go:
  // draft/ready/in_progress/waiting/done/declined), through the real
  // /api/v1/tickets API — creation tolerates no operator (operatorActor falls
  // back to an empty actor) so this works on the open/allow_open fixture.
  // Chat does NOT: Service.ChatOperator (internal/bot/chat.go) refuses with
  // no operator configured, and configuring one would require turning off
  // allow_open (dashboard auth's allow_open only takes effect with none
  // configured), which this fixture's ticket/wallet seeding depends on. So
  // chat here stays at "no conversation yet" — a real, already-handled empty
  // state, not a missing feature; the authenticated suite's bots.spec.ts
  // ("a team chat message can address everyone") already covers a real
  // chat thread with its own dedicated operator-backed daemon.
  const draft = await apiPost(rt.baseURL, '/api/v1/tickets', {
    title: 'Add a synthetic check for checkout',
    description: 'A canary that exercises checkout every minute would have caught this sooner.',
    priority: 'normal',
  })
  void draft

  const ready = await apiPost(rt.baseURL, '/api/v1/tickets', {
    title: 'Investigate elevated 5xx on checkout',
    description: 'Error rate climbed after the latest deploy; roll back if it keeps climbing.',
    assignee: 'bot:platform-sre',
    priority: 'high',
  })
  await apiPatch(rt.baseURL, `/api/v1/tickets/${ready.ticket}`, { status: 'ready' })

  // The status machine (internal/bot/tickets.go ticketTransitions) allows
  // draft -> ready -> in_progress -> waiting -> done|declined (declined is
  // also reachable straight from draft/ready). A ready ticket assigned to a
  // bot is picked up automatically almost immediately (the ticket's own
  // history records it: "ready -> in_progress: picked up") — there is no API
  // call that forces in_progress directly, so this waits briefly for that
  // real pickup instead of trying to drive the transition itself.
  const inProgress = await apiPost(rt.baseURL, '/api/v1/tickets', {
    title: 'Rotate the grafana credential',
    description: 'The current grafana login is due for rotation this week.',
    assignee: 'bot:scout',
    priority: 'normal',
  })
  await apiPatch(rt.baseURL, `/api/v1/tickets/${inProgress.ticket}`, { status: 'ready' })
  await new Promise((r) => setTimeout(r, 500)) // real bot pickup: ready -> in_progress
  await apiPost(rt.baseURL, `/api/v1/tickets/${inProgress.ticket}/comments`, { text: 'Drafting the rotation steps now.' })

  const waiting = await apiPost(rt.baseURL, '/api/v1/tickets', {
    title: 'Approve the backup storage subscription',
    description: 'Needs a decision on the recurring cloud backup charge before it renews.',
    assignee: 'bot:scout',
    priority: 'normal',
  })
  await apiPatch(rt.baseURL, `/api/v1/tickets/${waiting.ticket}`, { status: 'ready' })
  await new Promise((r) => setTimeout(r, 500)) // real bot pickup: ready -> in_progress
  await apiPatch(rt.baseURL, `/api/v1/tickets/${waiting.ticket}`, { status: 'waiting' })

  const done = await apiPost(rt.baseURL, '/api/v1/tickets', {
    title: 'Document the on-call rotation',
    description: 'Write up who to page and when.',
    assignee: 'bot:platform-sre',
    priority: 'low',
  })
  await apiPatch(rt.baseURL, `/api/v1/tickets/${done.ticket}`, { status: 'ready' })
  await apiPatch(rt.baseURL, `/api/v1/tickets/${done.ticket}`, { status: 'done' })

  const declined = await apiPost(rt.baseURL, '/api/v1/tickets', {
    title: 'Give scout write access to prod DNS',
    description: 'Not worth the blast radius for how rarely this comes up.',
    assignee: 'bot:scout',
    priority: 'low',
  })
  await apiPatch(rt.baseURL, `/api/v1/tickets/${declined.ticket}`, { status: 'declined' })

  // backfill: dispatched last and NOT awaited — `conductor run` returns once
  // the trigger is accepted (it does not wait for the run to finish), so
  // this leaves a run genuinely `running` for the rest of the process's
  // life, with nothing after it in this function to race against a restart.
  runTrigger(rt, 'backfill', env)
}

export function stopSample(rt: Runtime): void {
  try {
    process.kill(-rt.pid, 'SIGTERM')
  } catch {
    try {
      process.kill(rt.pid, 'SIGTERM')
    } catch {
      /* already gone */
    }
  }
}
