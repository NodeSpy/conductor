// Interaction tests for the dashboard prototype. See README.md.
// BASE_URL points at a running prototype server; without it, the suite
// serves design/dashboard-prototype itself on 127.0.0.1:$PORT (default 8799).
const path = require('path');
const os = require('os');
const { defineConfig } = require('@playwright/test');

const PORT = process.env.PORT || 8799;
const BASE = process.env.BASE_URL || `http://127.0.0.1:${PORT}`;

module.exports = defineConfig({
  testDir: __dirname,
  testMatch: /.*\.spec\.js/,
  outputDir: process.env.OUT_DIR || path.join(os.tmpdir(), 'cd-proto-tests'),
  timeout: 15 * 60 * 1000,
  expect: { timeout: 5000 },
  fullyParallel: false,
  workers: Number(process.env.WORKERS || 2),
  reporter: [['list'], ['json', { outputFile: process.env.REPORT || path.join(os.tmpdir(), 'cd-proto-tests', 'results.json') }]],
  use: { baseURL: BASE, actionTimeout: 4000, trace: 'off', screenshot: 'only-on-failure' },
  projects: [
    { name: 'desktop', use: { viewport: { width: 1440, height: 900 } } },
    { name: 'phone', use: { viewport: { width: 390, height: 844 }, isMobile: true, hasTouch: true } },
  ],
  webServer: process.env.BASE_URL ? undefined : {
    command: `python3 -m http.server ${PORT} --bind 127.0.0.1 --directory ${JSON.stringify(path.join(__dirname, '..', 'dashboard-prototype'))}`,
    url: `${BASE}/index.html`, reuseExistingServer: true, timeout: 20000,
  },
});
