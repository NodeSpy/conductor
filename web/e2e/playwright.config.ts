import { defineConfig, devices } from '@playwright/test'

// One throwaway daemon serves the whole run, so tests share state and must not
// race each other to mutate it — single worker, serial. The daemon binds a
// fixed high port (override with DASH_E2E_PORT); the base URL is derived from it.
const PORT = Number(process.env.DASH_E2E_PORT || 48766)
const BASE = `http://127.0.0.1:${PORT}`
process.env.DASH_E2E_PORT = String(PORT)

// The project rule: verify on Chromium desktop AND WebKit iPhone, OS dark. The
// iPhone 15 descriptor carries the WebKit engine, viewport and DPR; we force the
// OS colour scheme to dark on both so dark mode is exercised on each engine.
const iphone = devices['iPhone 15'] ?? devices['iPhone 14'] ?? devices['iPhone 13']

export default defineConfig({
  testDir: './tests',
  fullyParallel: false,
  workers: 1,
  forbidOnly: !!process.env.CI,
  retries: 0,
  reporter: [['list'], ['html', { open: 'never', outputFolder: 'playwright-report' }]],
  globalSetup: './global-setup.ts',
  globalTeardown: './global-teardown.ts',
  timeout: 30_000,
  expect: { timeout: 10_000 },
  use: {
    baseURL: BASE,
    colorScheme: 'dark',
    screenshot: 'only-on-failure',
    trace: 'retain-on-failure',
  },
  projects: [
    {
      name: 'chromium-desktop',
      use: { ...devices['Desktop Chrome'], colorScheme: 'dark' },
    },
    {
      name: 'webkit-iphone15',
      use: { ...iphone, colorScheme: 'dark' },
    },
  ],
})
