import { test as base, expect, type Page } from '@playwright/test'
import { createServer, type ServerResponse } from 'node:http'
import { randomUUID } from 'node:crypto'
import { numericFixtureReport, numericFixtureRequest } from '../../src/components/assistant/numericFixture'
import type { Chat, Connection } from '../../src/types/chat'
import type { DataSyncJob, DataSyncStatus } from '../../src/types/dataSync'
import type { AnalysisRequest, RunView } from '../../src/types/assistant'
import type { Message } from '../../src/types/query'
import { DASHBOARD_SSE_EVENTS, type Dashboard, type DashboardBlueprint, type DashboardGenerationCompleteEvent, type DashboardGenerationProgressEvent, type DashboardWidgetDataEvent } from '../../src/types/dashboard'

// Controlled UI protocol over real HTTP/SSE. This is not formal ERP/T38 integration evidence.
export const ERP = 'gate-erp-a'
export const ERP_B = 'gate-erp-b'
export const PG = 'gate-independent'
export const OLD_RUN = 'gate-old-report'
export const NAMES = { [ERP]: 'isolated ERP A', [ERP_B]: 'isolated ERP B', [PG]: 'isolated independent PG' }
const apiURL = new URL(process.env.V15_API_URL ?? 'http://127.0.0.1:18341/api')
const origin = `http://127.0.0.1:${process.env.V15_WEB_PORT ?? '15341'}`
type Phase = 'idle' | 'syncing' | 'recovering' | 'unresolved_erp' | 'outage'
export interface Barrier { arrived: Promise<void>; wait: Promise<void>; arrive: () => void; release: () => void }
type WidgetEvent = 'data' | 'error'
type GenerationOutcome = 'success' | 'model-error' | 'parse-error'
export interface SyncApi {
  requests: { method: string; path: string; owner: string; body: unknown; operationId: string }[]
  sourceRequests: { chatId: string; suffix: string; method: string }[]
  unexpected: string[]
  chats: Map<string, Chat>
  setPhase: (phase: Phase) => void
  hold: (suffix: string, chatId?: string) => Barrier
  rejectNext: (suffix: string) => void
  setOwner: (owner: string) => void
  statusCalls: number
  maxStatusInFlight: number
  statusResponses: DataSyncStatus[]
  resolveReads: { chatId: string; sourceRevision: number; bindingRevision: number; sourceId: string }[]
  widgetRequests: {
    chatId: string; dashboardId: string; widgetId: string; streamId: string
    sourceId: string; bindingRevision: number; sourceRevision: number
    event: WidgetEvent; result: DashboardWidgetDataEvent
  }[]
  cursorRequests: { chatId: string; operationId: string; cursor: string | null; result: DashboardWidgetDataEvent }[]
  generationAttempts: { operationId: string; index: number; outcome: GenerationOutcome; dashboardId?: string }[]
  blueprintReads: { chatId: string; sourceId: string; bindingRevision: number; prompt: string }[]
  blueprintCreates: { chatId: string; bindingRevision: number; cachedBindingRevision: number; indices: number[] }[]
  blueprintBatches: Map<string, { chatId: string; blueprints: DashboardBlueprint[]; bindingRevision: number; sourceId: string }>
  queueBlueprints: (blueprints: DashboardBlueprint[], chatId?: string) => void
  queueGenerationOutcomes: (outcomes: GenerationOutcome[], chatId?: string) => void
  holdBlueprintEvent: (chatId?: string) => Barrier
  holdCreateCleanup: (chatId?: string) => Barrier
  holdGenerationData: (chatId?: string) => Barrier
  holdGenerationSettlement: (chatId?: string) => Barrier
  holdGenerationCompletes: (chatId?: string) => Barrier
  holdAfterFirstDashboard: (chatId?: string) => Barrier
  advanceIdleRevision: (next: number) => void
  rebind: (chatId?: string) => Chat
  holdResponse: (suffix: string, chatId?: string) => Barrier
  holdWidgetEvent: (event: WidgetEvent, value?: number, chatId?: string) => Barrier
  holdDashboardRefreshError: (error: 'sse' | 'connection', chatId?: string) => Barrier
  setCursorTable: (version: string, chatId?: string) => void
  setDashboardRefreshInterval: (seconds: number) => void
  configureSync: (options: { ready: boolean; operators: string[]; expectedCount?: number | null }) => void
  syncJobs: Map<string, DataSyncJob>
  syncStarts: number
  requestLookups: { owner: string; chatId: string; requestId: string; match: 'found' | 'not_found' }[]
  loseNext202: () => void
  setJob: (patch: Partial<DataSyncJob>, jobId?: string) => DataSyncJob
  close: () => Promise<void>
}
function chat(id: string, owner: string, independent = false): Chat {
  return {
    id, user_id: owner, display_name: NAMES[id] ?? 'isolated 新独立连接',
    source_classification: independent ? 'independent' : 'registered_erp',
    ...(!independent ? { source_id: '11111111-1111-4111-8111-111111111111', source_binding_revision: 1 } : {}),
    connection: { type: 'postgresql', host: independent ? 'independent.invalid' : 'erp.invalid', port: '5432', username: 'isolated-reader', database: independent ? 'isolated_sales' : 'bi_agent', is_example_db: false },
    settings: { auto_execute_query: false, share_data_with_ai: false, non_tech_mode: false, auto_generate_visualization: false },
    selected_collections: 'ALL', created_at: '2026-01-01T00:00:00Z', updated_at: '2026-01-01T00:00:00Z',
  }
}
async function startSyncApi(page: Page): Promise<SyncApi> {
  if (apiURL.hostname !== '127.0.0.1' || apiURL.protocol !== 'http:') throw new Error('source gate fixture requires loopback HTTP')
  let owner = 'isolated-owner-a'
  let phase: Phase = 'idle'
  let revision = 1
  let inFlight = 0
  let sequence = 0
  const chats = new Map([[ERP, chat(ERP, owner)], [ERP_B, chat(ERP_B, owner)], [PG, chat(PG, owner, true)]])
  const connected = new Map([[ERP, true], [ERP_B, true], [PG, false]])
  const streams = new Map<ServerResponse, { chatId: string; streamId: string }>()
  const barriers = new Map<string, Barrier>()
  const allBarriers = new Set<Barrier>()
  const rejected = new Set<string>()
  const responseBarriers = new Map<string, Barrier>()
  const widgetReplies = new Map<string, { event: WidgetEvent; value: number; barrier: Barrier }[]>()
  const refreshErrors = new Map<string, { error: string; barrier: Barrier }>()
  const cursorTables = new Map<string, { id: number; label: string }[]>()
  const cachedBlueprints = new Map<string, { chatId: string; blueprints: DashboardBlueprint[]; bindingRevision: number; sourceId: string }>()
  const blueprintPayloads = new Map<string, DashboardBlueprint[][]>()
  const generationOutcomes = new Map<string, GenerationOutcome[][]>()
  const blueprintEvents = new Map<string, Barrier>()
  const createCleanups = new Map<string, Barrier>()
  const generationData = new Map<string, Barrier>()
  const generationPauses = new Map<string, Barrier>()
  const generationSettlements = new Map<string, Barrier>()
  const generationCompletes = new Map<string, Barrier>()
  const createdDashboards = new Map<string, Dashboard[]>()
  let refreshInterval = 15
  let syncReady = false
  let operators = new Set<string>()
  let expectedCount: number | null = null
  let lose202 = false
  let lastSuccess: string | null = null
  const syncJobs = new Map<string, DataSyncJob>()
  const requestJobs = new Map<string, string>()
  let closing = false
  const makeBarrier = (): Barrier => {
    const arrival = Promise.withResolvers<void>()
    const release = Promise.withResolvers<void>()
    const barrier = { arrived: arrival.promise, arrive: arrival.resolve, wait: release.promise, release: release.resolve }
    allBarriers.add(barrier)
    return barrier
  }
  const emit = (chatId: string, streamId: string, event: string, data: unknown) => {
    if (closing) return
    let recipients = 0
    for (const [stream, target] of streams) {
      if (target.chatId === chatId && target.streamId === streamId && !stream.writableEnded) {
        stream.write(`data: ${JSON.stringify({ event, data })}\n\n`)
        recipients++
      }
    }
    if (!recipients) api.unexpected.push(`no SSE recipient: ${chatId}/${streamId}/${event}`)
  }
  const finishResponse = async (chatId: string, suffix: string) => {
    const barrier = responseBarriers.get(`${chatId}:${suffix}`)
    if (barrier) {
      responseBarriers.delete(`${chatId}:${suffix}`)
      barrier.arrive()
      await barrier.wait
    }
  }
  const report = numericFixtureReport()
  report.report_id = OLD_RUN
  const oldView: RunView = { run_id: OLD_RUN, message_id: 'old-report-message', request_id: 'old-request', normalized_input: numericFixtureRequest(), status: 'completed', stage: 'done', revision: 1, report }
  const runs = new Map([[OLD_RUN, oldView]])
  const rows = Array.from({ length: 50 }, (_, index) => ({ id: index + 1, label: `snapshot-${index + 1}` }))
  const history = (id: string): Message[] => id === ERP || id === ERP_B ? [
    { id: 'old-user', type: 'user', content: '历史问题', created_at: '2026-09-08T01:00:00Z' },
    { id: 'old-report-message', type: 'assistant', content: '持久经营报告快照', created_at: '2026-09-08T01:01:00Z', assistant_run_id: OLD_RUN },
    { id: 'old-query-message', type: 'assistant', user_message_id: 'old-user', content: '历史 SQL 快照', created_at: '2026-09-08T01:02:00Z',
      queries: [{ id: 'old-query', query: 'SELECT id, label FROM isolated_snapshot', description: '只读快照', is_executed: true, execution_result: rows, pagination: { total_records_count: 100, cursor_field: 'id' }, visualization: { can_visualize: true } }],
      action_buttons: [{ id: 'retry', label: '重试历史问题', action: 'try_again', isPrimary: false }, { id: 'fix', label: '修复历史错误', action: 'fix_error', isPrimary: false }],
    },
  ] : []
  const messages = new Map<string, Message[]>(Array.from(chats.keys(), id => [id, history(id)]))
  const dashboard = (id: string): Dashboard => ({ id: `dash-${id}`, chat_id: id, name: 'isolated 快照看板', is_default: true, refresh_interval: refreshInterval, time_range: '24h', created_at: '2026-01-01T00:00:00Z', updated_at: '2026-01-01T00:00:00Z', layout: [],
    widgets: cursorTables.has(id)
      ? [{ id: `widget-${id}`, dashboard_id: `dash-${id}`, title: '游标明细', widget_type: 'table', query: 'SELECT id, label FROM isolated_cursor ORDER BY id LIMIT 2',
        table_config: { columns: [{ key: 'id', label: '序号', format: 'number' }, { key: 'label', label: '明细', format: 'text' }], page_size: 2, cursor_field: 'id' }, data: [], created_at: '', updated_at: '' }]
      : [{ id: `widget-${id}`, dashboard_id: `dash-${id}`, title: '快照组件', widget_type: 'stat', query: 'SELECT 48 AS margin', stat_config: { value_query: 'SELECT 48 AS margin', format: 'number', decimal_places: 0 }, data: [{ margin: 48 }], created_at: '', updated_at: '' }],
  })
  const api: SyncApi = {
    requests: [], sourceRequests: [], unexpected: [], chats, statusCalls: 0, maxStatusInFlight: 0,
    statusResponses: [], resolveReads: [], widgetRequests: [], cursorRequests: [], generationAttempts: [], blueprintReads: [], blueprintCreates: [],
    blueprintBatches: cachedBlueprints,
    syncJobs, syncStarts: 0, requestLookups: [],
    configureSync(options) { syncReady = options.ready; operators = new Set(options.operators); expectedCount = options.expectedCount ?? null; revision++ },
    loseNext202() { lose202 = true },
    setJob(patch, jobId = Array.from(syncJobs.keys()).pop()) {
      const previous = jobId ? syncJobs.get(jobId) : undefined
      if (!previous) throw new Error('No accepted sync job')
      const next = { ...previous, ...patch, revision: previous.revision + 1 }
      syncJobs.set(next.job_id, next); revision++
      if (next.release_confirmed && ['succeeded', 'failed', 'interrupted'].includes(next.status)) {
        phase = 'idle'
        if (next.status === 'succeeded' && next.verification_passed) lastSuccess = next.finished_at
      } else phase = patch.status === 'interrupted' ? 'recovering' : 'syncing'
      return next
    },
    queueBlueprints(blueprints, chatId = ERP) {
      const queue = blueprintPayloads.get(chatId) ?? []
      queue.push(structuredClone(blueprints)); blueprintPayloads.set(chatId, queue)
    },
    queueGenerationOutcomes(outcomes, chatId = ERP) {
      const queue = generationOutcomes.get(chatId) ?? []
      queue.push([...outcomes]); generationOutcomes.set(chatId, queue)
    },
    holdBlueprintEvent(chatId = ERP) { const barrier = makeBarrier(); blueprintEvents.set(chatId, barrier); return barrier },
    holdCreateCleanup(chatId = ERP) { const barrier = makeBarrier(); createCleanups.set(chatId, barrier); return barrier },
    holdGenerationData(chatId = ERP) { const barrier = makeBarrier(); generationData.set(chatId, barrier); return barrier },
    holdGenerationSettlement(chatId = ERP) { const barrier = makeBarrier(); generationSettlements.set(chatId, barrier); return barrier },
    holdGenerationCompletes(chatId = ERP) { const barrier = makeBarrier(); generationCompletes.set(chatId, barrier); return barrier },
    holdAfterFirstDashboard(chatId = ERP) { const barrier = makeBarrier(); generationPauses.set(chatId, barrier); return barrier },
    setPhase(next) {
      phase = next; revision++
      for (const id of [ERP, ERP_B]) {
        const current = chats.get(id)!
        chats.set(id, { ...current, source_classification: next === 'unresolved_erp' ? 'unresolved_erp' : 'registered_erp', source_id: next === 'unresolved_erp' ? '' : '11111111-1111-4111-8111-111111111111' })
      }
    },
    hold(suffix, chatId = ERP) {
      const barrier = makeBarrier()
      barriers.set(`${chatId}:${suffix}`, barrier)
      return barrier
    },
    rejectNext(suffix) { rejected.add(suffix) },
    setOwner(next) { owner = next; for (const [id, current] of chats) chats.set(id, { ...current, user_id: next }) },
    advanceIdleRevision(next) {
      if (phase !== 'idle' || !Number.isSafeInteger(next) || next <= revision) throw new Error('expected a forward idle source revision')
      revision = next
    },
    rebind(chatId = ERP) {
      const current = chats.get(chatId)!
      const next: Chat = { ...current, source_id: '22222222-2222-4222-8222-222222222222', source_binding_revision: (current.source_binding_revision ?? 0) + 1,
        connection: { ...current.connection, host: 'rebound.invalid', database: 'isolated_binding_b' } }
      chats.set(chatId, next)
      revision++
      return next
    },
    holdResponse(suffix, chatId = ERP) {
      const barrier = makeBarrier()
      responseBarriers.set(`${chatId}:${suffix}`, barrier)
      return barrier
    },
    holdWidgetEvent(event, value = 48, chatId = ERP) {
      const barrier = makeBarrier()
      const replies = widgetReplies.get(chatId) ?? []
      replies.push({ event, value, barrier })
      widgetReplies.set(chatId, replies)
      return barrier
    },
    holdDashboardRefreshError(error, chatId = ERP) {
      const barrier = makeBarrier()
      refreshErrors.set(chatId, { barrier, error: error === 'sse'
        ? 'SSE stream not found - please ensure you are connected before refreshing'
        : 'database connection not found for this chat - please connect to database first' })
      return barrier
    },
    setCursorTable(version, chatId = ERP) {
      cursorTables.set(chatId, Array.from({ length: 5 }, (_, index) => ({ id: index + 1, label: `cursor-${version}-row-${index + 1}` })))
    },
    setDashboardRefreshInterval(seconds) { refreshInterval = seconds },
    async close() {
      closing = true
      for (const barrier of allBarriers) barrier.release()
      for (const stream of streams.keys()) stream.end()
      const done = Promise.withResolvers<void>()
      server.close(() => done.resolve()); server.closeAllConnections()
      await done.promise
    },
  }
  const server = createServer(async (incoming, response) => {
    response.setHeader('Access-Control-Allow-Origin', origin)
    response.setHeader('Access-Control-Allow-Credentials', 'true')
    response.setHeader('Access-Control-Allow-Headers', 'Authorization, Content-Type, Cache-Control, Last-Event-ID')
    response.setHeader('Access-Control-Allow-Methods', 'GET, POST, PATCH, DELETE, OPTIONS')
    if (incoming.method === 'OPTIONS') { response.writeHead(204).end(); return }
    const method = incoming.method ?? 'GET'
    const url = new URL(incoming.url ?? '/', apiURL)
    const route = url.pathname.slice(apiURL.pathname.replace(/\/$/, '').length)
    const send = (data: unknown, status = 200) => { response.writeHead(status, { 'Content-Type': 'application/json' }); response.end(JSON.stringify(data)) }
    try {
      let raw = ''
      for await (const chunk of incoming) raw += chunk.toString()
      const body = raw ? JSON.parse(raw) : undefined
      api.requests.push({ method, path: route, owner, body, operationId: url.searchParams.get('operation_id') ?? '' })
      const requestOwner = owner
      if (route === '/auth' || route === '/auth/') { send({ success: true, data: { id: owner, username: owner, created_at: '2026-01-01T00:00:00Z' } }); return }
      if (route === '/llm-models') { send({ success: true, data: { models: [{ id: 'isolated-model', provider: 'openai', displayName: 'isolated 模型', isEnabled: true, default: true }] } }); return }
      if (route === '/chats' && method === 'GET') { send({ success: true, data: { chats: Array.from(chats.values()), total: chats.size } }); return }
      if (route === '/chats' && method === 'POST') {
        const created = chat(`independent-${++sequence}`, owner, true)
        created.connection = body.connection as Connection
        chats.set(created.id, created); messages.set(created.id, [])
        connected.set(created.id, false)
        send({ success: true, data: created }, 201); return
      }
      const match = route.match(/^\/chats\/([^/]+)(.*)$/)
      const id = match?.[1], suffix = match?.[2] ?? ''
      if (!id || !chats.has(id)) { api.unexpected.push(`${method} ${route}`); send({ success: false }, 404); return }
      const target = structuredClone(chats.get(id)!)
      const frozenPhase = phase
      const frozenRevision = revision
      const operationId = url.searchParams.get('operation_id') ?? ''
      const sourceId = target.source_id ?? ''
      const requestKey = (requestId: string) => JSON.stringify([requestOwner, sourceId, requestId])
      const safeJob = (job: DataSyncJob): DataSyncJob => {
        const own = requestJobs.get(requestKey(job.request_id ?? '')) === job.job_id
        if (own) return structuredClone(job)
        const safe = structuredClone(job)
        delete safe.request_id
        delete safe.initiating_chat_id
        return safe
      }
      const operation = operationId ? { operation_id: operationId } : {}
      const barrier = barriers.get(`${id}:${suffix}`)
      const statusRequest = suffix === '/data-sync/status'
      if (statusRequest) { inFlight++; api.statusCalls++; api.maxStatusInFlight = Math.max(api.maxStatusInFlight, inFlight) }
      if (barrier) { barriers.delete(`${id}:${suffix}`); barrier.arrive(); await barrier.wait }
      if (statusRequest) {
        inFlight--
        if (frozenPhase === 'outage') { send({ success: false, error: '同步状态暂不可确认', data: { code: 'SYNC_STATE_UNAVAILABLE' } }, 503); return }
        const active = Array.from(syncJobs.values()).find(job => job.source_id === sourceId && !job.release_confirmed)
        const requestId = url.searchParams.get('request_id')
        const matched = requestId ? syncJobs.get(requestJobs.get(requestKey(requestId)) ?? '') : undefined
        const status: DataSyncStatus = { schema_version: 1, source_id: sourceId, source_revision: frozenRevision, binding_revision: target.source_binding_revision ?? 0,
          display_name: target.display_name || '', readiness: { state: syncReady ? 'ready' : 'not_ready', reasons: [...(!syncReady ? ['SYNC_RELEASE_UNAVAILABLE'] : []), ...(!operators.has(requestOwner) ? ['SYNC_FORBIDDEN'] : [])] }, can_start: syncReady && operators.has(requestOwner) && frozenPhase === 'idle',
          ask_allowed: frozenPhase === 'idle', block_reason: frozenPhase === 'syncing' ? 'DATA_SYNC_IN_PROGRESS' : frozenPhase === 'recovering' ? 'DATA_SYNC_RECOVERING' : '', maintenance_state: frozenPhase === 'syncing' ? 'syncing' : frozenPhase === 'recovering' ? 'recovering' : 'idle', active_job: active ? safeJob(active) : null, latest_successful_sync_at: lastSuccess,
          ...(requestId ? { request_match: matched ? 'found' as const : 'not_found' as const, matched_job: matched ? safeJob(matched) : null } : {}) }
        if (requestId) api.requestLookups.push({ owner: requestOwner, chatId: id, requestId, match: matched ? 'found' : 'not_found' })
        api.statusResponses.push(status)
        send({ success: true, data: status }); return
      }
      if (suffix === '/data-sync/jobs' && method === 'POST') {
        if (!body || Object.keys(body).length !== 1 || typeof body.request_id !== 'string') { send({ success: false }, 400); return }
        const existing = syncJobs.get(requestJobs.get(requestKey(body.request_id)) ?? '')
        if (existing) { send({ success: true, data: safeJob(existing) }, 202); return }
        if (!operators.has(requestOwner)) { send({ success: false, data: { code: 'SYNC_FORBIDDEN' } }, 403); return }
        if (!syncReady) { send({ success: false, data: { code: 'SYNC_NOT_READY' } }, 409); return }
        if (phase !== 'idle') { send({ success: false, data: { code: 'SOURCE_BUSY' } }, 409); return }
        const job: DataSyncJob = { schema_version: 1, job_id: randomUUID(), source_id: sourceId, request_id: body.request_id, initiating_chat_id: id,
          status: 'created', stage: 'preparing', revision: 1, created_at: new Date().toISOString(), started_at: null, finished_at: null, cutoff: null,
          expected_count: expectedCount, processed_count: 0, succeeded_count: 0, failed_count: 0, shop_results: null, verification_passed: false,
          safe_error_code: '', safe_summary: '', partial_commits: 'none_started', release_confirmed: false }
        syncJobs.set(job.job_id, job); requestJobs.set(requestKey(body.request_id), job.job_id)
        api.syncStarts++; phase = 'syncing'; revision++
        await finishResponse(id, suffix)
        if (lose202) { lose202 = false; response.destroy(); return }
        send({ success: true, data: safeJob(job) }, 202); return
      }
      if (suffix.startsWith('/data-sync/jobs/') && method === 'GET') {
        const job = syncJobs.get(suffix.split('/').pop()!)
        if (!job || job.source_id !== sourceId) { send({ success: false }, 404); return }
        await finishResponse(id, suffix)
        send({ success: true, data: safeJob(job) }); return
      }
      if (!suffix) {
        if (method === 'PATCH') {
          const updated = { ...target, ...(body.selected_collections ? { selected_collections: body.selected_collections } : {}), ...(body.settings ? { settings: { ...target.settings, ...body.settings } } : {}) }
          chats.set(id, updated); send({ success: true, data: updated })
        } else send({ success: true, data: target })
        return
      }
      if (suffix === '/stream') {
        response.writeHead(200, { 'Content-Type': 'text/event-stream', 'Cache-Control': 'no-cache', Connection: 'keep-alive' })
        response.write(`data: ${JSON.stringify({ event: connected.get(id) ? 'db-connected' : 'db-disconnected' })}\n\n`)
        streams.set(response, { chatId: id, streamId: url.searchParams.get('stream_id') ?? '' })
        const timer = setInterval(() => response.write(': isolated keepalive\n\n'), 15000)
        response.on('close', () => { clearInterval(timer); streams.delete(response) }); return
      }
      if (suffix === '/messages' && method === 'GET') { send({ success: true, data: { messages: messages.get(id), total: messages.get(id)!.length } }); return }
      if (suffix === '/messages/pinned') { send({ success: true, data: { messages: messages.get(id), total: messages.get(id)!.length } }); return }
      if (suffix.startsWith('/assistant/runs/') && method === 'GET') { send({ data: runs.get(suffix.split('/').pop()!) }); return }
      if (suffix === '/dashboards' && method === 'GET') { send({ success: true, data: [dashboard(id), ...(createdDashboards.get(id) ?? [])].map(item => ({ ...item, widget_count: item.widgets.length })) }); return }
      if (suffix.startsWith('/dashboards/') && method === 'GET') {
        const found = [dashboard(id), ...(createdDashboards.get(id) ?? [])].find(item => suffix === `/dashboards/${item.id}`)
        if (found) { send({ success: true, data: found }); return }
      }
      if (suffix.endsWith('/export')) { send({ dashboard: dashboard(id) }); return }
      if (suffix.includes('/cancel') || suffix === '/disconnect') { send({ success: true }); return }
      const sourceAction = suffix === '/connection-status' || suffix === '/connect' || suffix === '/tables' || suffix === '/recommendations' || method === 'POST' || suffix.startsWith('/messages/') && method === 'PATCH'
      if (sourceAction) {
        api.sourceRequests.push({ chatId: id, suffix, method })
        const deny = target.source_classification !== 'independent' && frozenPhase !== 'idle' || rejected.delete(suffix)
        if (deny) { send({ success: false, error: '数据同步中，暂时无法问数', data: { code: 'DATA_SYNC_IN_PROGRESS' } }, 423); return }
      }
      if (suffix === '/connection-status') { send({ success: true, data: { is_connected: connected.get(id) === true } }); return }
      if (suffix === '/connect') {
        connected.set(id, true)
        for (const [stream, target] of streams) if (target.chatId === id) stream.write('data: {"event":"db-connected"}\n\n')
        send({ success: true, is_connected: true }); return
      }
      if (suffix === '/tables') { send({ success: true, data: { tables: [{ name: 'isolated_snapshot', columns: [{ name: 'id', type: 'integer', is_nullable: false }], is_selected: true, row_count: 100 }] } }); return }
      if (suffix === '/recommendations') { send({ success: true, data: { recommendations: [{ text: 'isolated 推荐问题' }] } }); return }
      if (suffix === '/messages' && method === 'POST') {
        const message: Message = { id: `message-${++sequence}`, type: 'user', content: body.content, created_at: new Date().toISOString() }
        messages.get(id)!.push(message); send({ success: true, data: message }); return
      }
      if (suffix.startsWith('/messages/') && method === 'PATCH') { send({ success: true, data: { id: suffix.split('/').pop(), content: body.content } }); return }
      if (suffix === '/assistant/resolve') {
        const resolution = body.product_query?.includes('候选') ? { status: 'needs_input', candidates: [{ entity_type: 'product', entity_id: 'P1', label: 'isolated 候选 P1' }] } : { status: 'resolved', request: { ...numericFixtureRequest(), request_id: body.request_id } }
        api.resolveReads.push({ chatId: id, sourceRevision: frozenRevision, bindingRevision: target.source_binding_revision ?? 0, sourceId: target.source_id ?? '' })
        // Source work has completed. Only delivery of this immutable response is delayed.
        await finishResponse(id, suffix)
        send({ data: resolution }); return
      }
      if (suffix === '/assistant/runs') {
        const request = body as AnalysisRequest
        const run = `run-${++sequence}`
        const nextReport = { ...report, report_id: run, basis: request.basis ?? 'payment' }
        runs.set(run, { ...oldView, run_id: run, message_id: `msg-${run}`, normalized_input: request, report: nextReport })
        send({ data: { run_id: run, message_id: `msg-${run}`, status: 'completed' } }); return
      }
      if (suffix === '/queries/results' || suffix === '/queries/execute') { send({ success: true, data: { execution_result: rows, total_records_count: 100, is_executed: true, has_more: true, next_cursor: '50' } }); return }
      if (suffix === '/visualization-data' || suffix.includes('/visualizations')) { send({ success: true, data: { can_visualize: false, reason: 'isolated 无新增图表' } }); return }
      if (suffix === '/dashboards/suggest-templates') {
        const bindingRevision = target.source_binding_revision ?? 0
        const binding = bindingRevision === 1 ? 'A' : 'B'
        const blueprints: DashboardBlueprint[] = blueprintPayloads.get(id)?.shift() ?? [0, 1].map(index => ({ index, name: `isolated ${binding} 方案 ${index}`,
          description: `来自绑定 ${binding} 的经营建议 ${index}`, template_type: 'sales', proposed_widgets: [{ title: `isolated ${binding} 收入`, widget_type: 'stat' }] }))
        const reference = operationId ? randomUUID() : ''
        const cacheKey = reference ? `${id}:${reference}` : id
        cachedBlueprints.set(cacheKey, { chatId: id, blueprints, bindingRevision, sourceId: target.source_id ?? '' })
        api.blueprintReads.push({ chatId: id, sourceId: target.source_id ?? '', bindingRevision, prompt: body?.prompt ?? '' })
        const barrier = blueprintEvents.get(id)
        if (barrier) {
          blueprintEvents.delete(id)
          send({ success: true })
          barrier.arrive(); await barrier.wait
        }
        emit(id, url.searchParams.get('stream_id') ?? '', DASHBOARD_SSE_EVENTS.BLUEPRINTS, { ...operation, blueprint_origin: reference, blueprints })
        if (!barrier) send({ success: true })
        return
      }
      if (suffix === '/dashboards/create-from-blueprints') {
        const reference = body.blueprint_origin ?? ''
        const cacheKey = reference ? `${id}:${reference}` : id
        const cached = cachedBlueprints.get(cacheKey)
        if (!cached || cached.bindingRevision !== (target.source_binding_revision ?? 0) || cached.sourceId !== (target.source_id ?? '')) {
          send({ success: false, error: 'dashboard suggestions expired or their source changed' }, 400); return
        }
        const indices = body.blueprint_indices as number[]
        api.blueprintCreates.push({ chatId: id, bindingRevision: target.source_binding_revision ?? 0, cachedBindingRevision: cached.bindingRevision, indices })
        const children: DashboardWidgetDataEvent[] = []
        const completeBarrier = generationCompletes.get(id)
        generationCompletes.delete(id)
        const settlementBarrier = generationSettlements.get(id)
        generationSettlements.delete(id)
        const delayedCompletes: DashboardGenerationCompleteEvent[] = []
        let succeeded = 0, failed = 0
        const outcomes = generationOutcomes.get(id)?.shift()
        for (const [position, index] of indices.entries()) {
          const selected = cached.blueprints.find(blueprint => blueprint.index === index)
          if (!selected) throw new Error(`unknown blueprint index ${index}`)
          const outcome = outcomes?.[position] ?? 'success'
          if (outcomes) {
            const progress = 10 + Math.floor(position * 90 / indices.length)
            emit(id, url.searchParams.get('stream_id') ?? '', DASHBOARD_SSE_EVENTS.GENERATION_PROGRESS, { ...operation, status: 'generating',
              message: `Building dashboard ${position + 1}/${indices.length}: ${selected.name}...`, progress })
            if (outcome !== 'success') {
              api.generationAttempts.push({ operationId, index, outcome })
              failed++
              if (outcome === 'model-error') emit(id, url.searchParams.get('stream_id') ?? '', DASHBOARD_SSE_EVENTS.GENERATION_PROGRESS, { ...operation,
                status: 'generating', message: '部分仪表盘未能创建，正在继续处理其余选择。', progress })
              // The real producer continues on ordinary model/parse errors without a complete for this item.
              continue
            }
          }
          const created: Dashboard = { ...dashboard(id), id: `created-dashboard-${++sequence}`, name: selected.name, is_default: false }
          created.widgets = created.widgets.map((widget, offset) => ({ ...widget, id: `widget-${created.id}-${offset}`, dashboard_id: created.id, data: undefined,
            query: `SELECT ${200 + index} AS margin`, stat_config: { value_query: `SELECT ${200 + index} AS margin`, format: 'number', decimal_places: 0 } }))
          createdDashboards.set(id, [...(createdDashboards.get(id) ?? []), created])
          succeeded++
          api.generationAttempts.push({ operationId, index, outcome, dashboardId: created.id })
          const complete: DashboardGenerationCompleteEvent = { ...operation, dashboard_id: created.id, dashboard: created }
          if (completeBarrier) delayedCompletes.push(complete)
          else emit(id, url.searchParams.get('stream_id') ?? '', DASHBOARD_SSE_EVENTS.GENERATION_COMPLETE, complete)
          for (const widget of created.widgets) children.push({ ...operation, widget_id: widget.id, data: [{ margin: 200 + index }], row_count: 1, execution_time_ms: 1, has_more: false })
          const pause = generationPauses.get(id)
          if (pause) {
            generationPauses.delete(id)
            for (const data of children.splice(0)) emit(id, url.searchParams.get('stream_id') ?? '', DASHBOARD_SSE_EVENTS.WIDGET_DATA, data)
            pause.arrive(); await pause.wait
          }
        }
        if (completeBarrier) {
          completeBarrier.arrive()
          void completeBarrier.wait.then(() => {
            for (const complete of delayedCompletes) emit(id, url.searchParams.get('stream_id') ?? '', DASHBOARD_SSE_EVENTS.GENERATION_COMPLETE, complete)
          })
        }
        const settlement: DashboardGenerationProgressEvent = { ...operation, status: 'finalizing', progress: 100,
          message: '所选仪表盘已处理完成。', batch_result: { succeeded, failed } }
        if (settlementBarrier) {
          settlementBarrier.arrive()
          void settlementBarrier.wait.then(() => emit(id, url.searchParams.get('stream_id') ?? '', DASHBOARD_SSE_EVENTS.GENERATION_PROGRESS, settlement))
        } else emit(id, url.searchParams.get('stream_id') ?? '', DASHBOARD_SSE_EVENTS.GENERATION_PROGRESS, settlement)
        const descendants = generationData.get(id)
        generationData.delete(id)
        if (descendants) descendants.arrive()
        void (descendants?.wait ?? Promise.resolve()).then(() => {
          for (const data of children) emit(id, url.searchParams.get('stream_id') ?? '', DASHBOARD_SSE_EVENTS.WIDGET_DATA, data)
        })
        const cleanup = createCleanups.get(id)
        if (cleanup) { createCleanups.delete(id); cleanup.arrive(); await cleanup.wait }
        cachedBlueprints.delete(cacheKey)
        send({ success: true })
        return
      }
      if (suffix === `/dashboards/dash-${id}/widgets/widget-${id}/refresh` || suffix === `/dashboards/dash-${id}/refresh`) {
        const failure = suffix === `/dashboards/dash-${id}/refresh` ? refreshErrors.get(id) : undefined
        if (failure) {
          refreshErrors.delete(id)
          failure.barrier.arrive(); await failure.barrier.wait
          // Matches the handler's ordinary preflight error; no typed source denial or widget result.
          send({ success: false, error: failure.error }, 400)
          return
        }
        const reply = widgetReplies.get(id)?.shift()
        const event = reply?.event ?? 'data'
        const cursor = url.searchParams.get('cursor')
        const pageRows = cursorTables.get(id)?.filter(row => row.id > Number(cursor ?? 0)).slice(0, 2)
        const data = pageRows ?? [{ margin: reply?.value ?? 48 }]
        const result: DashboardWidgetDataEvent = { ...operation, widget_id: `widget-${id}`, data: event === 'error' ? null : data,
          row_count: event === 'error' ? 0 : data.length, execution_time_ms: 1, has_more: event !== 'error' && pageRows?.length === 2,
          ...(event === 'error' ? { error: 'no connection found' } : pageRows?.length === 2 ? { next_cursor: String(pageRows[1].id) } : {}) }
        const streamId = url.searchParams.get('stream_id') ?? ''
        api.widgetRequests.push({ chatId: id, dashboardId: `dash-${id}`, widgetId: result.widget_id, streamId, sourceId: target.source_id ?? '',
          bindingRevision: target.source_binding_revision ?? 0, sourceRevision: frozenRevision, event, result })
        if (pageRows) api.cursorRequests.push({ chatId: id, operationId, cursor, result })
        // A real accepted HTTP refresh produced its result before the delayed SSE delivery.
        await finishResponse(id, suffix)
        send({ success: true }, 202)
        if (reply) { reply.barrier.arrive(); await reply.barrier.wait }
        emit(id, streamId, event === 'error' ? DASHBOARD_SSE_EVENTS.WIDGET_ERROR : DASHBOARD_SSE_EVENTS.WIDGET_DATA, result)
        return
      }
      if (suffix === '/refresh-schema' || suffix.includes('/dashboards/')) { send({ success: true, data: {} }); return }
      api.unexpected.push(`${method} ${route}`); send({ success: false }, 501)
    } catch (error) { api.unexpected.push(`${method} ${route}: ${String(error)}`); if (!response.headersSent) send({ success: false }, 500); else response.end() }
  })
  const started = Promise.withResolvers<void>()
  server.once('error', started.reject); server.listen(Number(apiURL.port), '127.0.0.1', started.resolve)
  await started.promise
  await page.context().addInitScript(() => localStorage.setItem('token', 'isolated-gate-token'))
  await page.context().route('https://**/*', route => route.abort())
  return api
}
export const test = base.extend<{ syncApi: SyncApi }>({
  syncApi: async ({ page }, provide) => {
    const api = await startSyncApi(page)
    try { await provide(api); expect(api.unexpected).toEqual([]) } finally { await api.close() }
  },
})
export { expect }
