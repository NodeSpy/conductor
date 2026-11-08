// Throwaway-daemon lifecycle for the dashboard e2e suite.
//
// The suite runs against a REAL conductor daemon — the same binary a box runs —
// booted hermetically: its own temp state dir, its own HOME, a dedicated port,
// the dashboard enabled, and NO inherited PASEO_HOME / XDG / CONDUCTOR* env. It
// never touches a live daemon, socket or config. Run history is seeded by
// dispatching a trivial `command` trigger (sh -c true) a few times, so the
// Overview has real records to render and the SSE stream carries real events.
import { spawn, execFileSync, type ChildProcess } from 'node:child_process'
import { mkdtempSync, mkdirSync, writeFileSync, openSync, readFileSync, existsSync } from 'node:fs'
import { tmpdir } from 'node:os'
import path from 'node:path'
import http from 'node:http'

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

const RUNTIME_FILE = path.resolve(__dirname, '..', '.runtime.json')

// A minimal, hermetic environment for every daemon/CLI invocation: PATH (for sh
// + the binary), HOME pointed at the throwaway dir, and TMPDIR. Crucially it
// does NOT forward PASEO_HOME, XDG_* or any CONDUCTOR* var — the daemon resolves
// every path from the config's state_file, so this stays fully isolated.
function hermeticEnv(home: string): NodeJS.ProcessEnv {
  return {
    PATH: process.env.PATH ?? '/usr/local/bin:/usr/bin:/bin',
    HOME: home,
    TMPDIR: process.env.TMPDIR ?? '/tmp',
    TERM: 'dumb',
  }
}

function resolveBin(): string {
  const fromEnv = process.env.CONDUCTOR_BIN
  const bin = fromEnv ? path.resolve(fromEnv) : path.resolve(__dirname, '..', '.bin', 'conductor')
  if (!existsSync(bin)) {
    throw new Error(
      `conductor binary not found at ${bin}. Build it first ` +
        `(\`make dashboard-e2e\` does this) or set CONDUCTOR_BIN.`,
    )
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

// start boots a throwaway daemon with the dashboard enabled and seeds a few real
// runs, then records the runtime for the tests and the teardown.
export async function start(port: number): Promise<Runtime> {
  const bin = resolveBin()
  const root = mkdtempSync(path.join(tmpdir(), 'dash-e2e-'))
  const stateDir = path.join(root, 'state')
  const home = path.join(root, 'home')
  const config = path.join(root, 'config.yaml')
  const logFile = path.join(root, 'daemon.log')
  const baseURL = `http://127.0.0.1:${port}`

  writeFileSync(
    config,
    [
      `store: { state_file: ${path.join(stateDir, 'state.json')} }`,
      `dashboard:`,
      `  enabled: true`,
      `  listen: [localhost]`,
      `  auth: { allow_open: true }`,
      `  port: ${port}`,
      `settings:`,
      `  greeting: hello`,
      // A whole-field vault reference, the documented way a real config wires
      // a credential (internal/secrets.ParseVaultRef) — not the legacy
      // top-level secrets: map, which config.Load hard-rejects now. This is
      // what /api/v1/secrets actually has to find on a real box (phase 8).
      `  api_token: '{{ vault "main" "api-token" }}'`,
      `connectors:`,
      `  box: { use: command }`,
      // webhook/cron are true built-ins (compiled in, never fetched) — unlike
      // github, which the dashboard's PluginInfo.Builtin list still claims is
      // a built-in (sections.go) but which the plugin host actually fetches
      // and runs as a subprocess over the network. Seeding a second
      // connector here must stay offline, so it uses one of the real
      // built-ins instead (see the phase 7 report's data-gap list for the
      // stale Builtin claim).
      `  hooks: { use: webhook }`,
      `runtimes:`,
      `  local: { use: cli }`,
      // hosts.*.env is the one place config has a named, scoped env map
      // outside a step (config.HostConfig.Env) — a runtimes: entry has none
      // (config.RuntimeConfig); one literal value and one reference, so the
      // Settings tab's env list (phase 8) has both shapes to show.
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
      `triggers:`,
      `  - name: deploy`,
      `    on: manual`,
      `    repo: acme/app`,
      `    steps: [{ uses: box.run, options: { command: "true" } }]`,
      `  - name: build`,
      `    on: manual`,
      `    repo: acme/widgets`,
      `    steps: [{ uses: box.run, options: { command: "true" } }]`,
      ``,
    ].join('\n'),
    { mode: 0o600 },
  )

  // A minimal local pack (packs: review: { use: ./packs/review }), so Packs
  // has an installed instance to show. No network: a local source still needs
  // one `conductor init` to vendor + lock it (config.Load refuses an
  // unresolved pack) — run hermetically, with an explicit --config and HOME
  // pointed at this throwaway root, same as every other invocation here. It
  // must NEVER run without --config: that falls back to the operator's real
  // config/state dirs.
  const packDir = path.join(root, 'packs', 'review')
  mkdirSync(packDir, { recursive: true })
  writeFileSync(
    path.join(packDir, 'conductor-pack.yaml'),
    [
      'pack:',
      '  name: demo-review',
      '  version: 0.1.0',
      '  description: a tiny local pack for the dashboard e2e suite',
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

  const env = hermeticEnv(home)
  execFileSync(bin, ['init', '--config', config], { env, stdio: 'ignore' })

  const out = openSync(logFile, 'a')
  // detached so we can signal the whole process group on teardown.
  const child: ChildProcess = spawn(bin, ['run', '--config', config], {
    env,
    detached: true,
    stdio: ['ignore', out, out],
  })
  child.unref()
  if (!child.pid) throw new Error('failed to spawn daemon')

  await waitForStatus(baseURL)

  // Seed real run history: dispatch the manual triggers through the running
  // daemon. Each produces a durable record and a live SSE event.
  for (const name of ['deploy', 'build', 'deploy']) {
    execFileSync(bin, ['run', name, '--config', config], { env, stdio: 'ignore' })
  }

  const rt: Runtime = { bin, config, stateDir, home, port, baseURL, pid: child.pid, logFile }
  writeFileSync(RUNTIME_FILE, JSON.stringify(rt, null, 2))
  return rt
}

export function readRuntime(): Runtime {
  return JSON.parse(readFileSync(RUNTIME_FILE, 'utf8')) as Runtime
}

// startIsolated boots a DEDICATED throwaway daemon (its own temp dir, config and
// port) that is NOT recorded in RUNTIME_FILE — so a test that mutates and
// reloads the config (the config-edit apply flow re-execs the daemon on SIGHUP)
// does not disturb the shared daemon the rest of the suite reads. The caller
// stops it in afterAll. The config carries an editable `settings.greeting`.
export async function startIsolated(port: number): Promise<Runtime> {
  const bin = resolveBin()
  const root = mkdtempSync(path.join(tmpdir(), 'dash-e2e-iso-'))
  const stateDir = path.join(root, 'state')
  const home = path.join(root, 'home')
  const config = path.join(root, 'config.yaml')
  const logFile = path.join(root, 'daemon.log')
  const baseURL = `http://127.0.0.1:${port}`

  writeFileSync(
    config,
    [
      `store: { state_file: ${path.join(stateDir, 'state.json')} }`,
      `dashboard:`,
      `  enabled: true`,
      `  listen: [localhost]`,
      `  auth: { allow_open: true }`,
      `  port: ${port}`,
      `settings:`,
      `  greeting: hello   # inline comment the edit must preserve`,
      `connectors:`,
      `  box: { use: command }`,
      ``,
    ].join('\n'),
    { mode: 0o600 },
  )

  const env = hermeticEnv(home)
  const out = openSync(logFile, 'a')
  const child: ChildProcess = spawn(bin, ['run', '--config', config], {
    env,
    detached: true,
    stdio: ['ignore', out, out],
  })
  child.unref()
  if (!child.pid) throw new Error('failed to spawn isolated daemon')
  await waitForStatus(baseURL)
  return { bin, config, stateDir, home, port, baseURL, pid: child.pid, logFile }
}

// startEmpty boots a DEDICATED daemon with nothing configured beyond the
// dashboard and the one connector a config needs to load: no triggers, bots, teams, tickets, packs or
// secrets — a first visit on a fresh box. Every screen must read as an empty
// state there, never an error.
//
// With runtime (the default) it has one agent command-line runtime, which a
// bot's workers need, as a box ready for its first bot would; without, the
// Bots welcome says that comes first.
export async function startEmpty(port: number, opts: { runtime?: boolean } = {}): Promise<Runtime> {
  const bin = resolveBin()
  const root = mkdtempSync(path.join(tmpdir(), 'dash-e2e-empty-'))
  const stateDir = path.join(root, 'state')
  const home = path.join(root, 'home')
  const config = path.join(root, 'config.yaml')
  const logFile = path.join(root, 'daemon.log')
  const baseURL = `http://127.0.0.1:${port}`
  writeFileSync(
    config,
    [
      `store: { state_file: ${path.join(stateDir, 'state.json')} }`,
      `dashboard:`,
      `  enabled: true`,
      `  listen: [localhost]`,
      `  auth: { allow_open: true }`,
      `  port: ${port}`,
      `connectors:`,
      `  box: { use: command }`,
      ...(opts.runtime === false ? [] : [`runtimes:`, `  agent: { use: cli, tool: claude-code }`]),
      ``,
    ].join('\n'),
    { mode: 0o600 },
  )
  const env = hermeticEnv(home)
  const out = openSync(logFile, 'a')
  const child: ChildProcess = spawn(bin, ['run', '--config', config], { env, detached: true, stdio: ['ignore', out, out] })
  child.unref()
  if (!child.pid) throw new Error('failed to spawn empty daemon')
  await waitForStatus(baseURL)
  return { bin, config, stateDir, home, port, baseURL, pid: child.pid, logFile }
}

// startWithCatalog boots a DEDICATED daemon with CONDUCTOR_PACK_REGISTRY
// pointed at a throwaway local directory of synthetic packs (neutral names,
// no copy of a real pack), so Packs → Browse has something to list offline,
// the way an operator's configured registry would. One pack of its own is
// installed (vendored by a hermetic `init` on this throwaway config, like the
// populated starter), so Installed has a row to act on. No network.
export async function startWithCatalog(port: number): Promise<Runtime & { registry: string }> {
  const bin = resolveBin()
  const root = mkdtempSync(path.join(tmpdir(), 'dash-e2e-cat-'))
  const stateDir = path.join(root, 'state')
  const home = path.join(root, 'home')
  const config = path.join(root, 'config.yaml')
  const logFile = path.join(root, 'daemon.log')
  const baseURL = `http://127.0.0.1:${port}`
  const registry = path.join(root, 'registry')

  const pack = (dir: string, yaml: string[]) => {
    mkdirSync(dir, { recursive: true })
    writeFileSync(path.join(dir, 'conductor-pack.yaml'), [...yaml, ''].join('\n'))
  }
  const needsCommand = ['  requires:', '    connectors: { command: "*" }']
  pack(path.join(registry, 'greeter'), [
    'pack:',
    '  name: greeter',
    '  version: 1.0.0',
    '  description: says hello on a schedule',
    ...needsCommand,
    'workflows:',
    '  say-hi: { steps: [ { id: hello, uses: command.run, options: { command: "true" } } ] }',
    'triggers:',
    '  - { name: on_tick, on: manual, steps: [ { uses: command.run, options: { command: "true" } } ] }',
  ])
  pack(path.join(registry, 'tidy-kit'), [
    'pack:',
    '  name: tidy-kit',
    '  version: 2.1.0',
    '  description: tidies up stale branches on request',
    ...needsCommand,
    'workflows:',
    '  tidy: { steps: [ { id: sweep, uses: command.run, options: { command: "true" } } ] }',
  ])
  pack(path.join(registry, 'helper-kit'), [
    'pack:',
    '  name: helper-kit',
    '  version: 0.5.0',
    '  description: a bot template only',
    'bots:',
    '  buddy:',
    '    description: A helper',
    '    charter: "Help out."',
  ])
  pack(path.join(root, 'packs', 'house'), [
    'pack:',
    '  name: house',
    '  version: 0.1.0',
    '  description: our own house checks',
    ...needsCommand,
    'workflows:',
    '  check: { steps: [ { id: lint, uses: command.run, options: { command: "true" } } ] }',
    'triggers:',
    '  - { name: on_save, on: manual, steps: [ { uses: command.run, options: { command: "true" } } ] }',
  ])

  writeFileSync(
    config,
    [
      `store: { state_file: ${path.join(stateDir, 'state.json')} }`,
      `dashboard:`,
      `  enabled: true`,
      `  listen: [localhost]`,
      `  auth: { allow_open: true }`,
      `  port: ${port}`,
      `connectors:`,
      `  box: { use: command }`,
      `packs:`,
      `  house: { use: ./packs/house }`,
      ``,
    ].join('\n'),
    { mode: 0o600 },
  )

  const env = { ...hermeticEnv(home), CONDUCTOR_PACK_REGISTRY: registry }
  execFileSync(bin, ['init', '--config', config], { env, stdio: 'ignore' })
  const out = openSync(logFile, 'a')
  const child: ChildProcess = spawn(bin, ['run', '--config', config], { env, detached: true, stdio: ['ignore', out, out] })
  child.unref()
  if (!child.pid) throw new Error('failed to spawn catalog daemon')
  await waitForStatus(baseURL)
  return { bin, config, stateDir, home, port, baseURL, pid: child.pid, logFile, registry }
}

// startWithGitCatalog boots a DEDICATED daemon whose registry is a throwaway
// git repo of synthetic packs (CONDUCTOR_PACK_REGISTRY=file://…), tagged the
// way a pack monorepo is (<pack>/vX.Y.Z), so an install is a real git source
// with releases: Add → apply fetches it, and a release tagged mid-test is an
// update to apply. Nothing installed and no `init`: every pack arrives
// through the dashboard's own apply. No network.
export async function startWithGitCatalog(port: number): Promise<Runtime & { registry: string; release: (name: string, version: string) => void }> {
  const bin = resolveBin()
  const root = mkdtempSync(path.join(tmpdir(), 'dash-e2e-gitcat-'))
  const stateDir = path.join(root, 'state')
  const home = path.join(root, 'home')
  const config = path.join(root, 'config.yaml')
  const logFile = path.join(root, 'daemon.log')
  const baseURL = `http://127.0.0.1:${port}`
  const registry = path.join(root, 'registry')
  mkdirSync(home, { recursive: true })
  const gitEnv = { ...hermeticEnv(home), GIT_AUTHOR_NAME: 't', GIT_AUTHOR_EMAIL: 't@e', GIT_COMMITTER_NAME: 't', GIT_COMMITTER_EMAIL: 't@e' }
  const git = (...args: string[]) => execFileSync('git', args, { cwd: registry, env: gitEnv, stdio: 'ignore' })
  const manifest = (name: string, version: string) =>
    [
      'pack:',
      `  name: ${name}`,
      `  version: ${version}`,
      '  description: says hello on a schedule',
      '  requires:',
      '    connectors: { command: "*" }',
      'workflows:',
      '  say-hi: { steps: [ { id: hello, uses: command.run, options: { command: "true" } } ] }',
      'triggers:',
      '  - { name: on_tick, on: manual, steps: [ { uses: command.run, options: { command: "true" } } ] }',
      '',
    ].join('\n')
  const release = (name: string, version: string) => {
    mkdirSync(path.join(registry, name), { recursive: true })
    writeFileSync(path.join(registry, name, 'conductor-pack.yaml'), manifest(name, version))
    git('add', '-A')
    git('commit', '-q', '-m', `${name} ${version}`)
    git('tag', `${name}/v${version}`)
  }
  mkdirSync(registry, { recursive: true })
  git('init', '-q', '-b', 'main')
  release('greeter', '1.0.0')

  writeFileSync(
    config,
    [
      `store: { state_file: ${path.join(stateDir, 'state.json')} }`,
      `dashboard:`,
      `  enabled: true`,
      `  listen: [localhost]`,
      `  auth: { allow_open: true }`,
      `  port: ${port}`,
      `connectors:`,
      `  box: { use: command }`,
      ``,
    ].join('\n'),
    { mode: 0o600 },
  )
  const env = { ...hermeticEnv(home), CONDUCTOR_PACK_REGISTRY: 'file://' + registry }
  const out = openSync(logFile, 'a')
  const child: ChildProcess = spawn(bin, ['run', '--config', config], { env, detached: true, stdio: ['ignore', out, out] })
  child.unref()
  if (!child.pid) throw new Error('failed to spawn git catalog daemon')
  await waitForStatus(baseURL)
  return { bin, config, stateDir, home, port, baseURL, pid: child.pid, logFile, registry, release }
}

// trigger dispatches a manual trigger on the running daemon (used mid-test to
// prove the live SSE update path). It returns once the daemon has accepted it.
export function trigger(rt: Runtime, name: string): void {
  execFileSync(rt.bin, ['run', name, '--config', rt.config], {
    env: hermeticEnv(rt.home),
    stdio: 'ignore',
  })
}

// stop kills the daemon process group.
export function stop(rt: Runtime): void {
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
