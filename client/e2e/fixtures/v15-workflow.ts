import { test as base, expect, type Page } from '@playwright/test'
import { createServer, type ServerResponse } from 'node:http'
import { numericFixtureReport, numericFixtureRequest } from '../../src/components/assistant/numericFixture'
import type { AnalysisRequest, DraftRequest, RunView } from '../../src/types/assistant'

// Isolated HTTP/fixture persistence only. No ERP, model, database or business proof.
export const CHAT_A = 'isolated-chat-a'
export const CHAT_B = 'isolated-chat-b'
export const OLD_RUN = 'isolated-old-run'
export const CHAT_NAMES: Record<string, string> = { [CHAT_A]: 'isolated 工作流 A', [CHAT_B]: 'isolated 工作流 B' }
const apiURL = new URL(process.env.V15_API_URL ?? 'http://127.0.0.1:18335/api')
const origin = `http://127.0.0.1:${process.env.V15_WEB_PORT ?? '15335'}`

type Operation = 'resolve' | 'create'
type Message = {
  id: string
  chat_id: string
  type: 'assistant' | 'user'
  content: string
  created_at: string
  is_edited: boolean
  queries: never[]
  assistant_run_id?: string
}
type Gate = {
  arrived: Promise<void>
  arrive: () => void
  wait: Promise<void>
  release: () => void
}

export interface WorkflowApi {
  requests: { method: string; path: string; body: unknown }[]
  unexpected: string[]
  runs: Map<string, { chatId: string; view: RunView }>
  messages: Map<string, Message[]>
  holdNext: (operation: Operation, chatId: string) => Gate
  failNextCreate: () => void
  complete: (runId: string) => void
  disconnect: (chatId: string) => void
  close: () => Promise<void>
}

function chat(id: string) {
  return {
    id,
    display_name: CHAT_NAMES[id],
    user_id: 'isolated-owner',
    source_classification: 'independent',
    connection: { type: 'postgresql', host: 'isolated.invalid', port: '5432', username: 'isolated-reader', database: 'isolated_sales', is_example_db: false },
    settings: { auto_execute_query: false, share_data_with_ai: false, non_tech_mode: false, auto_generate_visualization: false },
    selected_collections: 'ALL',
    created_at: '2026-09-01T00:00:00Z',
    updated_at: '2026-09-08T00:00:00Z',
  }
}

async function startWorkflowApi(page: Page): Promise<WorkflowApi> {
  if (apiURL.hostname !== '127.0.0.1' || apiURL.protocol !== 'http:') {
    throw new Error('isolated workflow fixture requires a local http V15_API_URL')
  }
  const messages = new Map<string, Message[]>([[CHAT_A, []], [CHAT_B, []]])
  const runs = new Map<string, { chatId: string; view: RunView }>()
  const gates = new Map<string, Gate>()
  const pendingGates = new Set<Gate>()
  const streams = new Map<string, Set<ServerResponse>>()
  const connected = new Map([[CHAT_A, true], [CHAT_B, true]])
  const requests: { method: string; path: string; body: unknown }[] = []
  const unexpected: string[] = []
  let nextCreateError = false
  let sequence = 0

  const persist = (chatId: string, input: AnalysisRequest, runId: string, completed: boolean) => {
    const messageId = `msg-${runId}`
    const report = numericFixtureReport()
    report.report_id = runId
    report.workflow = input.workflow
    report.basis = input.basis ?? 'payment'
    report.contract_version = 'isolated/v15-workflow'
    const view: RunView = {
      run_id: runId,
      message_id: messageId,
      request_id: input.request_id,
      normalized_input: structuredClone(input),
      status: completed ? 'completed' : 'running',
      stage: completed ? 'done' : 'running',
      revision: completed ? 2 : 1,
      ...(completed ? { report } : {}),
    }
    runs.set(runId, { chatId, view })
    messages.get(chatId)!.push({
      id: messageId,
      chat_id: chatId,
      type: 'assistant',
      content: 'isolated 经营报告快照',
      assistant_run_id: runId,
      is_edited: false,
      queries: [],
      created_at: runId === OLD_RUN ? '2026-09-08T01:00:00Z' : new Date().toISOString(),
    })
    return view
  }
  persist(CHAT_A, numericFixtureRequest(), OLD_RUN, true)

  const server = createServer(async (incoming, response) => {
    response.setHeader('Access-Control-Allow-Origin', origin)
    response.setHeader('Access-Control-Allow-Credentials', 'true')
    response.setHeader('Access-Control-Allow-Headers', 'Authorization, Content-Type, Cache-Control, Last-Event-ID')
    response.setHeader('Access-Control-Allow-Methods', 'GET, POST, PATCH, DELETE, OPTIONS')
    if (incoming.method === 'OPTIONS') { response.writeHead(204).end(); return }
    const url = new URL(incoming.url ?? '/', apiURL)
    const path = url.pathname
    const method = incoming.method ?? 'GET'
    const send = (body: unknown, status = 200) => {
      response.writeHead(status, { 'Content-Type': 'application/json' })
      response.end(JSON.stringify(body))
    }
    try {
      let raw = ''
      for await (const chunk of incoming) raw += chunk.toString()
      const body = raw ? JSON.parse(raw) : undefined
      requests.push({ method, path, body })
      const route = path.slice(apiURL.pathname.replace(/\/$/, '').length)
      if (route === '/auth' || route === '/auth/') {
        send({ success: true, data: { id: 'isolated-owner', username: 'isolated 操作员', created_at: '2026-01-01T00:00:00Z' } })
        return
      }
      if (route === '/chats' && method === 'GET') {
        send({ success: true, data: { chats: [chat(CHAT_A), chat(CHAT_B)], total: 2 } })
        return
      }
      if (route === '/llm-models') {
        send({ success: true, data: { models: [{ id: 'isolated-model', displayName: 'isolated 模型', provider: 'openai', isEnabled: true, default: true }] } })
        return
      }
      const match = route.match(/^\/chats\/([^/]+)(.*)$/)
      const chatId = match?.[1]
      const suffix = match?.[2]
      if (!chatId || !messages.has(chatId)) {
        unexpected.push(`${method} ${path}`)
        send({ code: 'isolated_unknown_route' }, 404)
        return
      }
      if (!suffix) {
        send({ success: true, data: chat(chatId) })
        return
      }
      if (suffix === '/connection-status') {
        send({ success: connected.get(chatId), is_connected: connected.get(chatId) })
        return
      }
      if (suffix === '/connect') {
        send({ success: connected.get(chatId) }, connected.get(chatId) ? 200 : 503)
        return
      }
      if (suffix === '/stream') {
        response.writeHead(200, { 'Content-Type': 'text/event-stream', 'Cache-Control': 'no-cache', Connection: 'keep-alive' })
        response.write(`data: ${JSON.stringify({ event: connected.get(chatId) ? 'db-connected' : 'db-disconnected' })}\n\n`)
        const group = streams.get(chatId) ?? new Set<ServerResponse>()
        streams.set(chatId, group)
        group.add(response)
        const heartbeat = setInterval(() => response.write(': isolated keepalive\n\n'), 15000)
        response.on('close', () => { clearInterval(heartbeat); group.delete(response) })
        return
      }
      if (suffix === '/messages' && method === 'GET') {
        send({ success: true, data: { messages: messages.get(chatId), total: messages.get(chatId)!.length } })
        return
      }
      if (suffix === '/messages/pinned') {
        send({ success: true, data: { messages: [], total: 0 } })
        return
      }
      if (suffix === '/recommendations') {
        send({ success: true, data: { recommendations: [] } })
        return
      }
      if (suffix === '/dashboards') {
        send({ success: true, data: [] })
        return
      }
      if (suffix === '/assistant/resolve' && method === 'POST') {
        const gate = gates.get(`resolve:${chatId}`)
        if (gate) { gates.delete(`resolve:${chatId}`); gate.arrive(); await gate.wait }
        const draft = body as DraftRequest
        if (draft.product_query === 'invalid') { send({ code: 'invalid_enum' }, 422); return }
        if (!draft.product_query?.trim()) {
          send({ data: { status: 'needs_input', missing_fields: ['product'] } })
        } else if (draft.product_query.includes('杯子')) {
          send({ data: { status: 'needs_input', missing_fields: ['product.product_id'], candidates: [
            { entity_type: 'product', entity_id: 'cup-1', label: 'isolated 杯子 A' },
            { entity_type: 'sku', entity_id: 'sku-2', label: 'isolated SKU sku-2' },
          ] } })
        } else {
          send({ data: { status: 'resolved', request: {
            ...numericFixtureRequest(),
            request_id: draft.request_id,
            workflow: draft.workflow,
            basis: draft.basis ?? 'payment',
            ...(draft.period ? { period: draft.period } : {}),
            ...(draft.scope ? { scope: draft.scope } : {}),
            ...(draft.group_by ? { group_by: draft.group_by } : {}),
            ...(draft.metrics ? { metrics: draft.metrics } : {}),
            ...(draft.low_margin_threshold ? { low_margin_threshold: draft.low_margin_threshold } : {}),
            product: { product_id: 'P1' },
          } } })
        }
        return
      }
      if (suffix === '/assistant/runs' && method === 'POST') {
        if (nextCreateError) { nextCreateError = false; send({ code: 'internal_error' }, 500); return }
        const input = body as AnalysisRequest
        const existing = Array.from(runs.values()).find(run => run.chatId === chatId && run.view.request_id === input.request_id)
        const view = existing?.view ?? persist(chatId, input, `isolated-run-${++sequence}`, false)
        const gate = gates.get(`create:${chatId}`)
        if (gate) { gates.delete(`create:${chatId}`); gate.arrive(); await gate.wait }
        send({ data: { run_id: view.run_id, message_id: view.message_id, status: 'created' } })
        return
      }
      const runMatch = suffix?.match(/^\/assistant\/runs\/([^/]+)$/)
      if (runMatch && method === 'GET') {
        const run = runs.get(runMatch[1])
        if (run?.chatId === chatId) send({ data: run.view })
        else send({ code: 'not_found' }, 404)
        return
      }
      unexpected.push(`${method} ${path}`)
      send({ code: 'isolated_unhandled_route' }, 501)
    } catch (error) {
      unexpected.push(`${method} ${path}: ${String(error)}`)
      if (!response.headersSent) send({ code: 'isolated_fixture_error' }, 500)
      else response.end()
    }
  })
  const listening = Promise.withResolvers<void>()
  server.once('error', listening.reject)
  server.listen(Number(apiURL.port), '127.0.0.1', listening.resolve)
  await listening.promise
  await page.addInitScript(() => localStorage.setItem('token', 'isolated-workflow-token'))
  await page.route('https://**/*', route => route.abort())

  return {
    requests,
    unexpected,
    runs,
    messages,
    holdNext(operation: Operation, chatId: string) {
      const arrival = Promise.withResolvers<void>()
      const gate = Promise.withResolvers<void>()
      const barrier = { arrived: arrival.promise, arrive: arrival.resolve, wait: gate.promise, release: gate.resolve }
      gates.set(`${operation}:${chatId}`, barrier)
      pendingGates.add(barrier)
      return barrier
    },
    failNextCreate() { nextCreateError = true },
    complete(runId: string) {
      const run = runs.get(runId)
      if (!run) throw new Error(`unknown isolated run ${runId}`)
      const report = numericFixtureReport()
      report.report_id = runId
      report.workflow = run.view.normalized_input.workflow
      report.basis = run.view.normalized_input.basis ?? 'payment'
      report.contract_version = 'isolated/v15-workflow'
      run.view = { ...run.view, status: 'completed', stage: 'done', revision: 2, report }
      for (const stream of streams.get(run.chatId) ?? []) {
        stream.write(`data: ${JSON.stringify({ event: 'assistant-run-updated', data: {
          run_id: runId, message_id: run.view.message_id, revision: 2, status: 'completed', stage: 'done',
        } })}\n\n`)
      }
    },
    disconnect(chatId: string) {
      connected.set(chatId, false)
      for (const stream of streams.get(chatId) ?? []) stream.write('data: {"event":"db-disconnected"}\n\n')
    },
    async close() {
      for (const gate of pendingGates) gate.release()
      const closing = Promise.withResolvers<void>()
      server.close(() => closing.resolve())
      server.closeAllConnections()
      await closing.promise
    },
  }
}

export const test = base.extend<{ workflowApi: WorkflowApi }>({
  workflowApi: async ({ page }, provide) => {
    const api = await startWorkflowApi(page)
    try { await provide(api); expect(api.unexpected).toEqual([]) }
    finally { await api.close() }
  },
})
export { expect }
