import { defineConfig, devices } from '@playwright/test'
import { mkdtempSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'

const webPort = Number(process.env.V15_WEB_PORT ?? '15335')
const apiURL = process.env.V15_API_URL ?? 'http://127.0.0.1:18335/api'
const baseURL = `http://127.0.0.1:${webPort}`
// mkdtemp creates a private directory; no reports, traces or fixtures enter the repo.
const output = mkdtempSync(join(tmpdir(), 'yanfa-v15-playwright-'))

export default defineConfig({
  testDir: './e2e',
  testMatch: /v15-.*\.spec\.ts/,
  outputDir: join(output, 'results'),
  reporter: [['list']],
  fullyParallel: false,
  workers: 1,
  use: {
    baseURL,
    trace: 'retain-on-failure',
    screenshot: 'only-on-failure',
  },
  projects: [{ name: 'chromium', use: { ...devices['Desktop Chrome'] } }],
  webServer: {
    command: `npm run dev -- --host 127.0.0.1 --port ${webPort} --strictPort`,
    url: baseURL,
    env: { VITE_API_URL: apiURL },
    reuseExistingServer: false,
  },
})
