import { afterEach, expect, it, vi } from 'vitest'

afterEach(() => { vi.unstubAllEnvs(); vi.resetModules() })

it('keeps normal deployment quantity-only and unfrozen', async () => {
  vi.stubEnv('VITE_GAME_DEMO', 'false'); vi.resetModules()
  const { defaultAssistantInput } = await import('./assistantInput')
  const input = defaultAssistantInput(new Date('2026-12-01T04:00:00Z'))
  expect(input.metrics).toEqual(['quantity'])
  expect(input.productId).toBe('')
  expect(input.endDate).toBe('2026-12-01')
})

it('makes the demo week and main product repeatable with money metrics', async () => {
  vi.stubEnv('VITE_GAME_DEMO', 'true'); vi.resetModules()
  const { defaultAssistantInput, buildRunRequest } = await import('./assistantInput')
  const input = defaultAssistantInput(new Date('2030-01-01T00:00:00Z'))
  expect([input.startDate, input.endDate, input.productId]).toEqual(['2026-11-02', '2026-11-09', 'RE9-PS5-STD'])
  expect(input.metrics).toEqual(['quantity', 'sales', 'cost', 'margin', 'margin_rate', 'avg_price'])
  const request = buildRunRequest(input, 'demo-request')
  expect(request.period.timezone).toBe('Asia/Shanghai')
  expect(request.basis).toBe('payment')
  expect(request.product.product_id).toBe('RE9-PS5-STD')
})

it('a natural game search never retains a different explicit product or SKU', async () => {
  vi.stubEnv('VITE_GAME_DEMO', 'true'); vi.resetModules()
  const { defaultAssistantInput, buildNaturalDraft } = await import('./assistantInput')
  const input = { ...defaultAssistantInput(), skuIds: 'SKU-RE9-PS5-STD' }
  const draft = buildNaturalDraft(input, 'search-another-title', 'NIGHTREIGN')
  expect(draft.product).toBeUndefined()
  expect(draft.product_query).toBe('NIGHTREIGN')
})
