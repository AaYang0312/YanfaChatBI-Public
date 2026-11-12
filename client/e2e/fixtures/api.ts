export interface ApiMockOptions {
  messages?: 'new' | 'old' | 'query'
  connection?: 'down' | 'up'
  auth?: 'ok' | 'network'
  execute?: 'fail' | 'hang'
}

export interface CapturedRequest {
  url: string
  body: unknown
}

import type { Page, Route } from '@playwright/test'


export const PRODUCT = '元发问数平台'
export const CHAT_ID = 'chat-fixture'
export const RUN_ID = 'run-numeric'
export const OLD_RUN_ID = 'run-old'
export const USER = {
  id: 'user-fixture',
  username: 'English Shop 杯子',
  created_at: '2026-01-15T00:00:00Z',
}

const WINDOW = {
  start_date: '2026-09-01',
  end_date_exclusive: '2026-09-08',
  timezone: 'Asia/Shanghai',
}

function metric(id: string, value: string | null, currency?: string) {
  return {
    metric_id: id,
    status: value == null ? 'unavailable' : 'available',
    value,
    currency,
    coverage_ref: 'cov:payment',
  }
}

function shop(entityId: string, quantity: string, sales: string, cost: string, margin: string, rate: string) {
  return {
    entity_type: 'shop',
    entity_id: entityId,
    currency: 'CNY',
    metrics: [
      metric('quantity', quantity),
      metric('sales', sales, 'CNY'),
      metric('cost', cost, 'CNY'),
      metric('margin', margin, 'CNY'),
      metric('margin_rate', rate),
      metric('avg_price', entityId === 'S1' ? '30' : '60', 'CNY'),
    ],
    coverage_ref: 'cov:payment',
  }
}

function point(entityId: string, metricId: string, value: string | null, businessDate?: string) {
  return {
    dimension: businessDate ?? entityId,
    entity_id: entityId,
    business_date: businessDate,
    metric_id: metricId,
    value,
    status: value == null ? 'unavailable' : 'available',
    currency: metricId === 'margin_rate' ? undefined : 'CNY',
    coverage_ref: 'cov:payment',
  }
}

export function fixtureReport(runId = RUN_ID) {
  return {
    report_id: runId,
    schema_version: 1,
    workflow: 'product_analysis',
    basis: 'payment',
    requested_scope: { mode: 'all' },
    actual_scope: { mode: 'all' },
    window: WINDOW,
    window_start: '2026-09-01T00:00:00+08:00',
    window_end_exclusive: '2026-09-08T00:00:00+08:00',
    contract_version: 'fixture-v1',
    outcome: 'complete',
    coverage: {
      requested: [],
      expected: [],
      covered: [],
      missing: [],
      stale: [],
      unknown: [],
      included: [
        { entity_type: 'shop', entity_id: 'S1', status: 'covered', coverage_ref: 'cov:payment' },
        { entity_type: 'shop', entity_id: 'S2', status: 'covered', coverage_ref: 'cov:payment' },
      ],
      excluded: [],
      data_as_of: '2026-09-08T00:30:00+08:00',
      coverage_ref: 'cov:payment',
    },
    totals: [
      metric('quantity', '3'),
      metric('sales', '120', 'CNY'),
      metric('cost', '72', 'CNY'),
      metric('margin', '48', 'CNY'),
      metric('margin_rate', '0.4'),
      metric('avg_price', '40', 'CNY'),
      metric('refund', '10', 'CNY'),
    ],
    table: {
      dimension: 'shop',
      rows: [
        shop('S1', '2', '60', '30', '30', '0.5'),
        shop('S2', '1', '60', '42', '18', '0.3'),
      ],
    },
    trend: [
      point('S1', 'sales', '20', '2026-09-01'),
      point('S1', 'sales', '40', '2026-09-02'),
      point('S1', 'sales', null, '2026-09-03'),
    ],
    charts: [
      {
        chart_id: 'shops-sales',
        kind: 'bar',
        title: 'S1 / S2',
        metric_ids: ['sales'],
        dimension: 'shop',
        data_ref: 'table.rows',
        coverage_ref: 'cov:payment',
        points: [point('S1', 'sales', '60'), point('S2', 'sales', '60')],
      },
      {
        chart_id: 'trend-sales',
        kind: 'line',
        title: 'S1',
        metric_ids: ['sales'],
        dimension: 'business_date',
        data_ref: 'trend',
        coverage_ref: 'cov:payment',
        points: [
          point('S1', 'sales', '20', '2026-09-01'),
          point('S1', 'sales', '40', '2026-09-02'),
          point('S1', 'sales', null, '2026-09-03'),
        ],
      },
    ],
    refunds: {
      label: '未扣退款',
      amount: '10',
      status: 'available',
      currency: 'CNY',
      occurrence_window: { start_date: WINDOW.start_date, end_date_exclusive: WINDOW.end_date_exclusive },
    },
    data_as_of: '2026-09-08T00:30:00+08:00',
    margin_label: '未扣退款',
  }
}

export function fixtureRequest() {
  return {
    schema_version: 1,
    request_id: 'req-numeric',
    workflow: 'product_analysis',
    basis: 'payment',
    period: WINDOW,
    scope: { mode: 'all' },
    product: { product_id: 'P1' },
    group_by: 'shop',
    metrics: ['quantity', 'sales', 'cost', 'margin', 'margin_rate', 'avg_price', 'refund'],
  }
}

export function chatRecord(id = CHAT_ID, database = 'sales_pg') {
  return {
    id,
    user_id: USER.id,
    connection: {
      type: 'postgresql',
      host: 'db.internal',
      port: '5432',
      username: 'reader',
      database,
      is_example_db: false,
    },
    selected_collections: 'ALL',
    settings: {
      auto_execute_query: false,
      share_data_with_ai: false,
      non_tech_mode: false,
      auto_generate_visualization: false,
    },
    created_at: '2026-09-01T00:00:00Z',
    updated_at: '2026-09-08T00:00:00Z',
  }
}

function queryRow(id: string, overrides: Record<string, unknown> = {}) {
  return {
    id,
    query: 'SELECT sku, shop_name FROM sales WHERE product_id = \'P1\'',
    is_edited: false,
    description: 'SKU shop_name',
    execution_time: 12,
    example_execution_time: 8,
    can_rollback: false,
    is_critical: false,
    is_executed: true,
    is_rolled_back: false,
    example_result: [{ sku: 'sku-2', shop_name: '杯子店' }],
    execution_result: [{ sku: 'sku-2', shop_name: '杯子店' }],
    query_type: 'SELECT',
    pagination: { total_records_count: 51, page_size: 50 },
    tables: 'sales',
    ...overrides,
  }
}

export function fixtureMessages(kind: 'new' | 'old' | 'query') {
  if (kind === 'query') {
    return [
      {
        id: 'msg-query',
        chat_id: CHAT_ID,
        type: 'assistant',
        content: 'SELECT sku FROM sales',
        is_edited: false,
        queries: [queryRow('q-fail', {
          error: { code: 'SAFETY_VIOLATION', message: 'query rejected', details: 'rule-7' },
          execution_result: [],
          is_executed: false,
          pagination: { total_records_count: 51, page_size: 50 },
        })],
        created_at: '2026-09-08T01:00:00Z',
      },
      {
        id: 'msg-page',
        chat_id: CHAT_ID,
        type: 'assistant',
        content: 'page fixture',
        is_edited: false,
        queries: [queryRow('q-page', {
          execution_result: Array.from({ length: 51 }, (_, index) => ({ sku: `sku-${index}`, shop_name: '杯子店' })),
          example_result: [],
        })],
        created_at: '2026-09-08T01:01:00Z',
      },
      {
        id: 'msg-empty-viz',
        chat_id: CHAT_ID,
        type: 'assistant',
        content: 'viz fixture',
        is_edited: false,
        queries: [queryRow('q-viz', {
          visualization: {
            can_visualize: false,
            reason: 'No data rows returned by the query',
          },
          execution_result: [],
        })],
        created_at: '2026-09-08T01:02:00Z',
      },
    ]
  }
  const runId = kind === 'old' ? OLD_RUN_ID : RUN_ID
  return [
    {
      id: `msg-${kind}`,
      chat_id: CHAT_ID,
      type: 'assistant',
      content: 'English Shop 杯子 / sku-2 / gpt-4o',
      is_edited: false,
      llm_model: 'gpt-4o',
      llm_model_name: 'GPT-4 Omni',
      assistant_run_id: runId,
      queries: [],
      created_at: '2026-09-08T01:00:00Z',
    },
  ]
}

function json(route: Route, body: unknown, status = 200) {
  return route.fulfill({
    status,
    contentType: 'application/json',
    body: JSON.stringify(body),
  })
}

export async function installApiMock(page: Page, options: ApiMockOptions = {}) {
  const messages = fixtureMessages(options.messages ?? 'new')
  const chats = options.chats ?? [chatRecord()]
  const posted: CapturedRequest[] = []
  const seen: string[] = []

  await page.route('**/*', async route => {
    const request = route.request()
    const url = new URL(request.url())
    const raw = url.pathname
    const marker = raw.includes('/api/') ? raw.indexOf('/api/') : raw.lastIndexOf('/undefined/')
    const path = marker >= 0 ? raw.slice(marker) : raw
    const api = path.startsWith('/api/') || path.startsWith('/undefined/')
    if (!api || path.includes('.') || path.startsWith('/src/') || path.startsWith('/@')) {
      await route.continue()
      return
    }
    const method = request.method()
    seen.push(`${method} ${path}`)
    if (method !== 'GET' && method !== 'HEAD') {
      try {
        posted.push({ url: request.url(), body: request.postDataJSON() })
      } catch {
        posted.push({ url: request.url(), body: request.postData() })
      }
    }

    if (path.endsWith('/auth/') || path.endsWith('/auth')) {
      if (options.auth === 'network') {
        await route.abort('failed')
        return
      }
      const token = request.headers()['authorization'] ?? ''
      if (!token.includes('fixture-token')) {
        await json(route, { success: false, error: 'unauthorized' }, 401)
        return
      }
      await json(route, { success: true, data: USER })
      return
    }
    if (path.endsWith('/auth/login')) {
      await json(route, {
        success: true,
        data: { access_token: 'fixture-token', refresh_token: 'fixture-refresh', user: USER },
      })
      return
    }
    if (path.endsWith('/auth/signup')) {
      await json(route, { success: false, error: 'email already exists' }, 409)
      return
    }
    if (path.endsWith('/auth/forgot-password')) {
      await json(route, { success: true, data: { message: 'sent' } })
      return
    }
    if (path.endsWith('/auth/reset-password')) {
      await json(route, { success: false, error: 'invalid or expired otp' }, 400)
      return
    }
    if (path.endsWith('/llm-models')) {
      await json(route, {
        success: true,
        data: { models: [{ id: 'gpt-4o', provider: 'OpenAI', displayName: 'GPT-4 Omni', isEnabled: true }] },
      })
      return
    }
    if (path.endsWith('/github/stats')) {
      await json(route, { success: true, data: { star_count: 1 } })
      return
    }
    if (/\/chats\/[^/]+\/assistant\/runs\/[^/]+$/.test(path)) {
      const runId = path.split('/').pop() ?? RUN_ID
      await json(route, {
        data: {
          run_id: runId,
          message_id: `msg-${runId}`,
          request_id: 'req-numeric',
          status: 'completed',
          stage: 'done',
          revision: 1,
          report: fixtureReport(runId),
          normalized_input: fixtureRequest(),
        },
      })
      return
    }
    if (path.endsWith('/assistant/runs') && method === 'POST') {
      await json(route, { data: { run_id: RUN_ID, message_id: 'msg-created', status: 'completed' } })
      return
    }
    if (path.endsWith('/assistant/resolve')) {
      await json(route, { data: { status: 'needs_input', candidates: [{ entity_type: 'sku', entity_id: 'sku-2', label: '杯子 sku-2' }] } })
      return
    }
    if (path.endsWith('/messages') && method === 'GET') {
      await json(route, { success: true, data: { messages, total: messages.length } })
      return
    }
    if (path.endsWith('/recommendations')) {
      await json(route, { success: true, data: { recommendations: [{ text: '这个店近期销售额是多少' }] } })
      return
    }
    if (path.endsWith('/connection-status')) {
      await json(route, { success: true, is_connected: options.connection !== 'down' })
      return
    }
    if (path.endsWith('/connect')) {
      if (options.connection === 'down') {
        await json(route, { success: false, error: 'connection refused to db.internal' }, 500)
        return
      }
      await json(route, { success: true })
      return
    }
    if (path.endsWith('/dashboards') && method === 'GET') {
      await json(route, { success: true, data: [] })
      return
    }
    if (path.endsWith('/queries/execute')) {
      if (options.execute === 'hang') {
        seen.push(`HANG ${path}`)
        return new Promise<void>(() => {})
      }
      await json(route, {
        success: true,
        data: {
          is_executed: true,
          is_rolled_back: false,
          execution_result: null,
          total_records_count: 0,
          error: { code: 'QUERY_FAILED', message: 'syntax error at sku' },
        },
      })
      return
    }
    if (path.endsWith('/queries/results')) {
      await json(route, {
        success: true,
        data: { rows: [{ sku: 'sku-page-2', shop_name: '杯子店' }], total_records_count: 51, has_more: false },
      })
      return
    }
    if (path.endsWith('/stream/cancel')) {
      await json(route, { success: true })
      return
    }
    if (path.includes('/stream')) {
      const stream = new ReadableStream({
        async start(controller) {
          const encoder = new TextEncoder()
          const { promise, resolve } = Promise.withResolvers<void>()
          controller.enqueue(encoder.encode('data: {"event":"ping"}\n\n'))
          setTimeout(resolve, 50)
          await promise
          controller.enqueue(encoder.encode(': keep-alive\n\n'))
        },
      })
      await route.fulfill({ status: 200, contentType: 'text/event-stream', body: stream as never })
      return
    }
    if ((path === '/api/chats' || path === '/undefined/chats') && method === 'GET') {
      await json(route, { success: true, data: { chats, total: chats.length } })
      return
    }
    if (/\/chats\/[^/]+$/.test(path) && method === 'GET') {
      await json(route, { success: true, data: chats[0] })
      return
    }
    if (method === 'DELETE' && /\/chats\//.test(path)) {
      await json(route, { success: true })
      return
    }
    await json(route, { success: true, data: {} })
  })

  return {
    posts: () => posted,
    seen: () => seen,
  }
}
