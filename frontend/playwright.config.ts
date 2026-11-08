import { defineConfig } from '@playwright/test'

export default defineConfig({
  testDir: './e2e',
  fullyParallel: false,
  workers: 1,
  timeout: 120_000,
  expect: { timeout: 15_000 },
  outputDir: 'test-results',
  reporter: [['line'], ['html', { outputFolder: 'playwright-report', open: 'never' }]],
  use: {
    baseURL: process.env.E2E_BASE_URL ?? 'http://127.0.0.1:5173',
    channel: process.env.E2E_BROWSER_CHANNEL || undefined,
    headless: true,
    launchOptions: {
      args: process.env.E2E_CERT_SPKI ? [`--ignore-certificate-errors-spki-list=${process.env.E2E_CERT_SPKI}`] : [],
    },
    trace: 'retain-on-failure',
    screenshot: 'only-on-failure',
    video: 'retain-on-failure',
  },
})
