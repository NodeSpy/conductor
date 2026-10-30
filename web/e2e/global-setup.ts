// Global setup: build nothing here (the binary is built by `make dashboard-e2e`
// before the container starts), but boot ONE throwaway daemon for the whole run
// and seed it with real run history. Teardown (global-teardown.ts) kills it.
import { start } from './lib/daemon'

export default async function globalSetup(): Promise<void> {
  const port = Number(process.env.DASH_E2E_PORT || 48766)
  const rt = await start(port)
  // eslint-disable-next-line no-console
  console.log(`[e2e] throwaway daemon up: ${rt.baseURL} (pid ${rt.pid}, state ${rt.stateDir})`)
}
