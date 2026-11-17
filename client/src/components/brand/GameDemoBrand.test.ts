import { afterEach, expect, it, vi } from 'vitest'
afterEach(() => { vi.unstubAllEnvs(); vi.resetModules() })

it('uses neutral product copy throughout a demo build', async () => {
  vi.stubEnv('VITE_GAME_DEMO', 'true'); vi.resetModules()
  const { productName, zhCN } = await import('../../i18n/zh-CN')
  expect(productName).toBe('GameShelf')
  expect(JSON.stringify(zhCN)).not.toMatch(/元发|元發|YanfaChatBI/i)
})

it('replaces HTML title, metadata and artwork without changing attribution', async () => {
  const { transformGameDemoHtml } = await import('../../brand/gameDemoHtml')
  const html = transformGameDemoHtml('<title>元发问数平台</title><img src="/yanfachatbi-banner.png"><link href="/yanfachatbi-logo.svg"><footer>Based on NeoBase · LICENSE</footer>')
  expect(html).not.toMatch(/元发|元發|yanfachatbi/i)
  expect(html).toContain('GameShelf 游戏经营助手')
  expect(html).toContain('gameshelf-logo.svg')
  expect(html).toContain('Based on NeoBase · LICENSE')
})
