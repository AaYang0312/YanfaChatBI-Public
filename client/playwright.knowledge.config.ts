import { defineConfig } from '@playwright/test'
import objects from './playwright.objects.config'

// Reuse browser settings without changing the reviewed object-selection suite.
const port = 15349
export default defineConfig({
  ...objects,
  testMatch: 'knowledge-base.spec.ts',
  outputDir: './node_modules/.cache/t08b-playwright',
  use: { ...objects.use, baseURL: 'http://127.0.0.1:' + port },
  webServer: {
    command: 'npm run dev -- --host 127.0.0.1 --port ' + port + ' --strictPort',
    url: 'http://127.0.0.1:' + port,
    env: { VITE_API_URL: 'http://127.0.0.1:18349/api' },
    reuseExistingServer: false,
  },
})
