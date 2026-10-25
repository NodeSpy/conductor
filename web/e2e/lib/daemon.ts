// Throwaway-daemon lifecycle for the dashboard e2e suite.
//
// The suite runs against a REAL conductor daemon — the same binary a box runs —
// booted hermetically: its own temp state dir, its own HOME, a dedicated port,
// the dashboard enabled, and NO inherited PASEO_HOME / XDG / CONDUCTOR* env. It
// never touches a live daemon, socket or config. Run history is seeded by
// dispatching a trivial `command` trigger (sh -c true) a few times, so the
// Overview has real records to render and the SSE stream carries real events.
import { spawn, execFileSync, type ChildProcess } from 'node:child_process'
import { mkdtempSync, writeFileSync, openSync, readFileSync, existsSync } from 'node:fs'
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
      `  port: ${port}`,
      `connectors:`,
      `  box: { use: command }`,
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

  const env = hermeticEnv(home)
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
