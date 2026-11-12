import { defineConfig, devices } from '@playwright/test'

const port = 15348
export default defineConfig({
  testDir: './e2e',
  testMatch: 'query-objects.spec.ts',
  outputDir: './node_modules/.cache/t08a-playwright',
  reporter: [['list']],
  workers: 1,
  use: { baseURL: 'http://127.0.0.1:' + port, trace: 'retain-on-failure', screenshot: 'only-on-failure' },
  projects: [{ name: 'chromium', use: { ...devices['Desktop Chrome'] } }],
  webServer: {
    command: 'npm run dev -- --host 127.0.0.1 --port ' + port + ' --strictPort',
    url: 'http://127.0.0.1:' + port,
    env: { VITE_API_URL: 'http://127.0.0.1:18348/api' },
    reuseExistingServer: false,
  },
})

