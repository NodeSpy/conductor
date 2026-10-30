// Global teardown: kill the throwaway daemon started in global-setup.
import { readRuntime, stop } from './lib/daemon'

export default async function globalTeardown(): Promise<void> {
  try {
    const rt = readRuntime()
    stop(rt)
    // eslint-disable-next-line no-console
    console.log(`[e2e] throwaway daemon stopped (pid ${rt.pid})`)
  } catch {
    /* nothing to stop */
  }
}
