// Test fixtures: hand each test a `daemon` handle so it can dispatch a run on the
// live throwaway daemon mid-test (the SSE path), plus a `collector` that records
// console errors and failed/4xx+ network requests seen on the page.
import { test as base, expect, type Page } from '@playwright/test'
import { readRuntime, trigger, type Runtime } from './lib/daemon'

export interface Daemon {
  runtime: Runtime
  trigger: (name: string) => void
}

export interface Collector {
  consoleErrors: string[]
  failed: string[]
}

export const test = base.extend<{ daemon: Daemon; collector: Collector }>({
  daemon: async ({}, use) => {
    const runtime = readRuntime()
    await use({ runtime, trigger: (name: string) => trigger(runtime, name) })
  },
  collector: async ({ page }: { page: Page }, use) => {
    const consoleErrors: string[] = []
    const failed: string[] = []
    page.on('console', (m) => {
      if (m.type() === 'error') consoleErrors.push(m.text())
    })
    page.on('pageerror', (e) => consoleErrors.push(`pageerror: ${e.message}`))
    page.on('requestfailed', (r) => {
      // EventSource is long-lived; a cancellation at context close is not a load
      // failure. We assert the collector during the test, before teardown.
      failed.push(`requestfailed ${r.url()} (${r.failure()?.errorText ?? 'unknown'})`)
    })
    page.on('response', (r) => {
      if (r.status() >= 400) failed.push(`${r.status()} ${r.url()}`)
    })
    await use({ consoleErrors, failed })
  },
})

export { expect }
