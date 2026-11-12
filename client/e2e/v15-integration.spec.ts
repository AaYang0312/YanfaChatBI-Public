import { randomUUID } from 'node:crypto'
import { writeFile } from 'node:fs/promises'
import { test, expect, type APIRequestContext, type APIResponse, type Page } from '@playwright/test'
import type { CreateRunResponse, RunView } from '../src/types/assistant'
import type { DataSyncJob, DataSyncStatus } from '../src/types/dataSync'
import type { BackendMessage } from '../src/types/messages'
import { zhCN } from '../src/i18n/zh-CN'

// Real application consumer. The manager must publish a ready receipt before running.
// No import of the Node HTTP fixtures: missing infrastructure is a failure.
const API = 'http://127.0.0.1:18343/api'
const CONTROL = 'http://127.0.0.1:19343'
const POLL = { timeout: 60_000, intervals: [100, 250, 500, 1000] }
type Actor = { id: string; username: string; password: string; access_token: string }
type QueryInput = { message_id: string; query_id: string; stream_id: string }
type Fixture = {
  schema_version: number
  namespace: string
  source_id: string
  actors: { a: Actor; b: Actor }
  chats: { a: string; same_owner: string; b: string; independent: string }
  queries: { a: QueryInput; independent: QueryInput }
  model_id: string
  workflow_question: string
}
type Watermark = { source: string; entity: string; shop_id: string; watermark: string; last_error_code: string | null }
type Evidence = {
  writer_count: number
  child_operations: string[]
  verify_waiting: boolean
  reader_waiting: number
  writer_waiting: number
  source: { phase: string; state_revision: number; active_job_id: string }
  jobs: Pick<DataSyncJob, 'job_id' | 'request_id' | 'status' | 'stage' | 'release_confirmed' | 'verification_passed' | 'succeeded_count' | 'failed_count'>[]
  watermarks: Watermark[]
}
type QueryResult = {
  chat_id: string
  message_id: string
  query_id: string
  is_executed: boolean
  execution_result: { results: Record<string, unknown>[] }
  error?: { code: string; message: string }
}
let fixture: Fixture
const auth = (actor: Actor) => ({ Authorization: `Bearer ${actor.access_token}` })
const composer = (page: Page) => page.getByPlaceholder(zhCN.chat.placeholderConnected).or(page.getByPlaceholder(zhCN.chat.placeholderDisconnected))
const modal = (page: Page) => page.getByRole('dialog', { name: zhCN.dataSync.title, exact: true })

async function data<T>(response: APIResponse): Promise<T> {
  expect(response.ok(), `${response.url()} returned ${response.status()}`).toBe(true)
  const body = await response.json() as { success: boolean; data: T }
  expect(body.success).toBe(true)
  return body.data
}
async function control(request: APIRequestContext, path: string, body = {}) {
  const response = await request.post(`${CONTROL}${path}`, { data: body })
  expect(response.ok(), `Control input ${path} failed: ${response.status()}`).toBe(true)
  // A control acknowledgment is never business evidence.
}
async function evidence(request: APIRequestContext): Promise<Evidence> {
  const response = await request.get(`${CONTROL}/evidence`)
  expect(response.ok()).toBe(true)
  return response.json() as Promise<Evidence>
}
async function status(request: APIRequestContext, actor = fixture.actors.a, chat = fixture.chats.a, requestID?: string) {
  const query = requestID ? `?request_id=${encodeURIComponent(requestID)}` : ''
  return data<DataSyncStatus>(await request.get(`${API}/chats/${chat}/data-sync/status${query}`, { headers: auth(actor) }))
}
async function job(request: APIRequestContext, id: string) {
  return data<DataSyncJob>(await request.get(`${API}/chats/${fixture.chats.a}/data-sync/jobs/${id}`, { headers: auth(fixture.actors.a) }))
}
async function messages(request: APIRequestContext, actor: Actor, chat: string) {
  return (await data<{ messages: BackendMessage[] }>(await request.get(`${API}/chats/${chat}/messages?page=1&page_size=100`, { headers: auth(actor) }))).messages
}
async function execute(request: APIRequestContext, actor: Actor, chat: string, query: QueryInput) {
  return request.post(`${API}/chats/${chat}/queries/execute`, { headers: auth(actor), data: query, timeout: 60_000 })
}
async function observe(name: string, value: unknown) {
  const path = test.info().outputPath(`${name}.json`)
  await writeFile(path, JSON.stringify(value, null, 2))
  await test.info().attach(name, { path, contentType: 'application/json' })
}
async function login(page: Page, actor: Actor) {
  await page.goto('/')
  await page.getByPlaceholder(zhCN.auth.login.usernamePlaceholder, { exact: true }).fill(actor.username)
  await page.getByPlaceholder(zhCN.auth.login.passwordPlaceholder, { exact: true }).fill(actor.password)
  const accepted = page.waitForResponse(response => response.request().method() === 'POST' && response.url() === `${API}/auth/login`)
  await page.getByRole('button', { name: zhCN.auth.login.submit, exact: true }).click()
  const response = await accepted
  expect(response.status()).toBe(200)
  const body = await response.json()
  expect(body.data.user.id).toBe(actor.id)
  await expect.poll(() => page.evaluate(() => localStorage.getItem('token'))).toBe(body.data.access_token)
}
async function openChat(page: Page, chat: string, connected = true) {
  await page.goto(`/chat/${chat}`)
  await expect(page.getByRole('heading', { level: 2 }).first()).toBeVisible()
  if (page.viewportSize()!.width < 768) {
    const collapse = page.getByRole('button', { name: zhCN.sidebar.collapse, exact: true })
    if (await collapse.isVisible()) await collapse.click()
    await expect(page.getByRole('button', { name: zhCN.sidebar.expand, exact: true })).toBeVisible()
  }
  if (connected) {
    await page.getByRole('button', { name: zhCN.chat.reconnect, exact: true }).or(composer(page).and(page.locator(':enabled'))).first().click()
    await expect(composer(page)).toBeEnabled()
  }
}
async function showSync(page: Page, keyboard = false) {
  const options = page.getByRole('button', { name: zhCN.chat.chatOptions, exact: true }).and(page.locator(':visible'))
  if (keyboard) { await options.focus(); await options.press('ArrowDown') } else await options.click()
  const entry = page.getByRole('button', { name: new RegExp(`^(${zhCN.dataSync.menu}|${zhCN.dataSync.progress})$`) }).and(page.locator(':visible'))
  if (keyboard) { await expect(entry).toBeFocused(); await entry.press('Enter') } else await entry.click()
  await expect(modal(page)).toBeVisible()
}
async function closeSync(page: Page) {
  await page.keyboard.press('Escape')
  await expect(modal(page)).toHaveCount(0)
  await expect(page.getByRole('button', { name: zhCN.chat.chatOptions, exact: true }).and(page.locator(':visible'))).toBeFocused()
}
async function startSync(page: Page) {
  const response = page.waitForResponse(item => item.request().method() === 'POST' && item.url() === `${API}/chats/${fixture.chats.a}/data-sync/jobs`)
  await modal(page).getByRole('button', { name: new RegExp(`^(${zhCN.dataSync.start}|${zhCN.dataSync.newOperation})$`) }).click()
  const accepted = await response
  expect(accepted.status()).toBe(202)
  const body = await accepted.json() as { success: boolean; data: DataSyncJob }
  expect(body.success).toBe(true)
  expect(body.data.source_id).toBe(fixture.source_id)
  return body.data
}
async function blocked(page: Page) {
  await expect(page.getByText(zhCN.sourceAccess.syncing, { exact: true }).first()).toBeVisible()
  await expect(page.locator('form').filter({ has: composer(page) }).locator('button[type="submit"]')).toBeDisabled()
}
async function report(request: APIRequestContext, id: string) {
  return data<RunView>(await request.get(`${API}/chats/${fixture.chats.a}/assistant/runs/${id}`, { headers: auth(fixture.actors.a) }))
}
async function createReport(page: Page, request: APIRequestContext) {
  const trigger = page.getByRole('button', { name: zhCN.assistant.menu.title, exact: true })
  await composer(page).fill('T43 普通问数草稿')
  await trigger.focus()
  await trigger.press('ArrowDown')
  await page.getByRole('menuitemradio', { name: zhCN.assistant.menu.productAnalysis, exact: true }).press('Enter')
  await page.getByLabel(zhCN.assistant.input.question, { exact: true }).fill(fixture.workflow_question)
  await page.getByLabel(zhCN.assistant.input.startDate, { exact: true }).fill('2026-09-01')
  await page.getByLabel(zhCN.assistant.input.endDate, { exact: true }).fill('2026-09-08')
  const created = page.waitForResponse(item => item.request().method() === 'POST' && item.url() === `${API}/chats/${fixture.chats.a}/assistant/runs`)
  await page.getByRole('form', { name: zhCN.assistant.input.title }).getByRole('button', { name: zhCN.assistant.input.submit, exact: true }).click()
  const response = await created
  expect(response.ok()).toBe(true)
  const accepted = (await response.json()).data as CreateRunResponse
  await expect.poll(async () => (await report(request, accepted.run_id)).status, POLL).toBe('completed')
  const view = await report(request, accepted.run_id)
  expect(view.report?.contract_version).toBe('fixture-2026-09')
  expect(view.report?.totals).toEqual(expect.arrayContaining([
    expect.objectContaining({ metric_id: 'sales', value: '120' }),
    expect.objectContaining({ metric_id: 'cost', value: '72' }),
    expect.objectContaining({ metric_id: 'margin', value: '48' }),
    expect.objectContaining({ metric_id: 'margin_rate', value: '0.4' }),
  ]))
  expect(view.report?.refunds.amount).toBe('10')
  const card = page.locator(`[data-run-id="${view.run_id}"]`)
  await expect(card.locator('[data-field="data-as-of"]')).toHaveText(view.report!.data_as_of)
  await expect(card.locator('tfoot td')).toContainText(['3', '120', '72', '48', '0.4', '40'])
  await expect(trigger).toBeFocused()
  await expect(composer(page)).toHaveValue('T43 普通问数草稿')
  expect((await messages(request, fixture.actors.a, fixture.chats.a)).filter(item => item.assistant_run_id === view.run_id).map(item => item.id)).toEqual([view.message_id])
  return view
}
function key(row: Watermark) { return `${row.source}/${row.entity}/${row.shop_id}` }
function expectWatermarks(rows: Watermark[], before: Watermark[], cutoff: string) {
  expect(rows.map(key).sort()).toEqual(before.map(key).sort())
  expect(new Set(rows.map(key)).size).toBe(14)
  for (const row of rows) {
    const initial = before.find(item => key(item) === key(row))!
    expect(Date.parse(row.watermark)).toBeGreaterThanOrEqual(Date.parse(cutoff))
    expect(Date.parse(row.watermark)).toBeGreaterThanOrEqual(Date.parse(initial.watermark))
    expect(row.last_error_code).toBeNull()
  }
}

// These two scenarios share the owned runtime namespace; run the whole file once.
test.describe.serial('T43 actual isolated application, SQL process and persistence', () => {
  test.use({ viewport: { width: 1440, height: 1000 } })
  test.beforeAll(async ({ request }) => {
    expect(process.env.V15_API_URL).toBe(API)
    expect(process.env.V15_WEB_PORT).toBe('15343')
    const response = await request.get(`${CONTROL}/fixture`)
    expect(response.ok(), 'Manager-owned real runtime must be ready').toBe(true)
    fixture = await response.json() as Fixture
    expect(fixture.schema_version).toBe(1)
    expect(fixture.actors.a.id).not.toBe(fixture.actors.b.id)
    expect(new Set(Object.values(fixture.chats)).size).toBe(4)
    await observe('owned-runtime-identity', { namespace: fixture.namespace, source_id: fixture.source_id, chats: fixture.chats })
  })
  test.afterEach(async ({ request }) => { await control(request, '/release-all') })

  test('real SH read excludes sync; real EX writer gates accounts until PG verification, preserving report snapshots', async ({ page, browser, request }) => {
    test.setTimeout(180_000)
    await login(page, fixture.actors.a)
    await openChat(page, fixture.chats.a)
    const old = await createReport(page, request)
    const card = page.locator(`[data-run-id="${old.run_id}"]`)
    const oldTable = await card.locator('table').innerText()
    await page.reload()
    await expect(card.locator('table')).toHaveText(oldTable, { useInnerText: true })
    expect((await report(request, old.run_id)).report).toEqual(old.report)

    await control(request, '/prepare-sync', { scenario: 'ok', hold_writer: true, hold_verify: true })
    const before = await evidence(request)
    expect(new Set(before.watermarks.map(key)).size).toBe(14)
    await control(request, '/hold-reader')
    let readingFinished = false
    const reading = execute(request, fixture.actors.a, fixture.chats.a, fixture.queries.a).then(response => { readingFinished = true; return response })
    try {
      await expect.poll(async () => (await evidence(request)).reader_waiting, POLL).toBeGreaterThan(0)
      const refused = await request.post(`${API}/chats/${fixture.chats.a}/data-sync/jobs`, { headers: auth(fixture.actors.a), data: { request_id: randomUUID() } })
      expect(refused.status()).toBe(409)
      expect(await refused.json()).toMatchObject({ success: false, data: { code: 'SOURCE_BUSY' } })
      const held = await evidence(request)
      expect(held.writer_count).toBe(before.writer_count)
      expect(held.child_operations).toEqual(before.child_operations)
      expect(held.reader_waiting).toBeGreaterThan(0)
      expect(readingFinished).toBe(false)
      await observe('reader-held-sync-409', held)
    } finally { await control(request, '/release-reader') }
    const read = await data<QueryResult>(await reading)
    expect(read).toMatchObject({ chat_id: fixture.chats.a, message_id: fixture.queries.a.message_id, query_id: fixture.queries.a.query_id, is_executed: true })
    expect(read.error).toBeUndefined()
    await expect.poll(async () => (await status(request)).can_start, POLL).toBe(true)
    await showSync(page)
    expect((await evidence(request)).writer_count).toBe(before.writer_count)
    const accepted = await startSync(page)
    await expect.poll(async () => (await evidence(request)).writer_waiting, POLL).toBeGreaterThan(0)
    const writing = await evidence(request)
    expect(writing.writer_count).toBe(before.writer_count + 1)
    expect(writing.child_operations).toHaveLength(before.child_operations.length + 1)
    expect(writing.source).toMatchObject({ phase: 'syncing', active_job_id: accepted.job_id })
    expect(writing.watermarks).toEqual(before.watermarks)
    await closeSync(page)
    await blocked(page)
    await composer(page).fill('T43 同步拒绝后保留草稿')
    await composer(page).press('Enter')
    await expect(composer(page)).toHaveValue('T43 同步拒绝后保留草稿')
    await expect(card.locator('table')).toHaveText(oldTable, { useInnerText: true })

    const other = await browser.newContext({ baseURL: 'http://127.0.0.1:15343', viewport: { width: 1440, height: 1000 } })
    const sameOwner = await page.context().newPage()
    try {
      const b = await other.newPage()
      await login(b, fixture.actors.b)
      await openChat(b, fixture.chats.b, false)
      await openChat(sameOwner, fixture.chats.same_owner, false)
      await blocked(b)
      await blocked(sameOwner)
      await showSync(b)
      expect((await status(request, fixture.actors.b, fixture.chats.b)).active_job?.job_id).toBe(accepted.job_id)
      const bJob = await data<DataSyncJob>(await request.get(`${API}/chats/${fixture.chats.b}/data-sync/jobs/${accepted.job_id}`, { headers: auth(fixture.actors.b) }))
      expect(bJob.job_id).toBe(accepted.job_id)
      expect(bJob.request_id).toBeUndefined()
      await closeSync(b)
      for (const [actor, chat] of [[fixture.actors.a, fixture.chats.a], [fixture.actors.a, fixture.chats.same_owner], [fixture.actors.b, fixture.chats.b]] as const) {
        const saved = await messages(request, actor, chat)
        const denied = await request.post(`${API}/chats/${chat}/messages`, { headers: auth(actor), data: { content: 'T43 denied actual model question', stream_id: `t43-denied-${chat}`, llm_model: fixture.model_id } })
        expect(denied.status()).toBe(423)
        await observe(`source-denial-${chat}`, { at: new Date().toISOString(), chat_id: chat, status: denied.status(), response: await denied.json() })
        expect(await messages(request, actor, chat)).toEqual(saved)
      }
      expect((await execute(request, fixture.actors.a, fixture.chats.a, fixture.queries.a)).status()).toBe(423)
      const independent = await other.newPage()
      await openChat(independent, fixture.chats.independent)
      const independentRead = await data<QueryResult>(await execute(request, fixture.actors.b, fixture.chats.independent, fixture.queries.independent))
      expect(independentRead).toMatchObject({ chat_id: fixture.chats.independent, query_id: fixture.queries.independent.query_id, is_executed: true })
      expect(independentRead.error).toBeUndefined()
      expect((await evidence(request)).writer_waiting).toBeGreaterThan(0)
      expect((await evidence(request)).writer_count).toBe(before.writer_count + 1)
      await observe('writer-held-accounts-and-independent', { evidence: await evidence(request), independent: independentRead })
      await control(request, '/release-writer')
      await expect.poll(async () => (await evidence(request)).verify_waiting, POLL).toBe(true)
      await expect.poll(async () => { const current = await job(request, accepted.job_id); return [current.stage, current.processed_count, current.succeeded_count, current.failed_count] }, POLL).toEqual(['verifying', 7, 7, 0])
      await showSync(page)
      await expect(modal(page).getByText(zhCN.dataSync.verifying, { exact: true })).toBeVisible()
      await expect(modal(page).getByText(zhCN.dataSync.counts(7, 0, 7, 7), { exact: true })).toBeVisible()
      await expect(modal(page).getByRole('progressbar')).not.toHaveAttribute('value')
      await expect(modal(page).getByText(zhCN.dataSync.succeeded, { exact: true })).toHaveCount(0)
      expect((await status(request)).ask_allowed).toBe(false)
      const verifying = await evidence(request)
      expectWatermarks(verifying.watermarks, before.watermarks, (await job(request, accepted.job_id)).cutoff!)
      expect(verifying.jobs.find(item => item.job_id === accepted.job_id)?.release_confirmed).toBe(false)
      await observe('seven-of-seven-before-original-verifier', verifying)
      await control(request, '/release-verify')
      await expect.poll(async () => { const current = await job(request, accepted.job_id); return [current.status, current.verification_passed, current.release_confirmed] }, POLL).toEqual(['succeeded', true, true])
      await expect.poll(async () => (await status(request)).ask_allowed, POLL).toBe(true)
      await expect(modal(page).getByText(zhCN.dataSync.succeeded, { exact: true })).toBeVisible()
      await expect(modal(page).getByRole('progressbar')).toHaveAttribute('value', '100')
      const finished = await job(request, accepted.job_id)
      expect(finished.shop_results?.map(item => item.shop_id).sort()).toEqual(['1', '2', '3', '4', '5', '6', '7'])
      expect(finished.shop_results?.every(item => item.status === 'ok' && item.related_orders_refetch_completed)).toBe(true)
      expect((await status(request)).latest_successful_sync_at).toBe(finished.finished_at)
      await closeSync(page)
      await expect(page.locator('form').filter({ has: composer(page) }).locator('button[type="submit"]')).toBeEnabled()
      await expect(composer(page)).toHaveValue('T43 同步拒绝后保留草稿')
      expect((await report(request, old.run_id)).report).toEqual(old.report)
      await page.reload()
      await expect(card.locator('table')).toHaveText(oldTable, { useInnerText: true })
      await expect(card.locator('[data-field="data-as-of"]')).toHaveText(old.report!.data_as_of)
      const explicitRead = await data<QueryResult>(await execute(request, fixture.actors.a, fixture.chats.a, fixture.queries.a))
      expect(explicitRead.is_executed).toBe(true)
      expect(explicitRead.error).toBeUndefined()
      expect(explicitRead.execution_result).not.toEqual(read.execution_result)
      await observe('verified-release-and-explicit-read', { job: finished, evidence: await evidence(request), read: explicitRead, report: (await report(request, old.run_id)).report })
    } finally { await sameOwner.close(); await other.close() }
  })

  test('lost actual 202 recovers the same terminal partial-failure job after reload without another SQL child', async ({ page, request }) => {
    test.setTimeout(120_000)
    await page.setViewportSize({ width: 390, height: 844 })
    await login(page, fixture.actors.a)
    await openChat(page, fixture.chats.a)
    const reference = (await messages(request, fixture.actors.a, fixture.chats.a)).find(item => item.assistant_run_id)
    expect(reference?.assistant_run_id).toBeDefined()
    const old = await report(request, reference!.assistant_run_id!)
    const card = page.locator(`[data-run-id="${old.run_id}"]`)
    await expect(card.locator('[data-field="data-as-of"]')).toHaveText(old.report!.data_as_of)
    const oldTable = await card.locator('table').innerText()
    const previousSuccess = (await status(request)).latest_successful_sync_at
    expect(previousSuccess).not.toBeNull()
    await control(request, '/prepare-sync', { scenario: 'shop_error_exit0', hold_writer: true, hold_verify: false })
    const before = await evidence(request)
    await showSync(page, true)
    await expect(modal(page).getByRole('button', { name: zhCN.common.close, exact: true }).first()).toBeFocused()
    let accepted: DataSyncJob | undefined
    let requestID: string | undefined
    let deliveryLost = false
    let outage = false
    const jobsURL = `${API}/chats/${fixture.chats.a}/data-sync/jobs`
    const statusURL = `${API}/chats/${fixture.chats.a}/data-sync/status*`
    await page.route(statusURL, async route => { if (outage) await route.abort('connectionfailed'); else await route.continue() })
    await page.route(jobsURL, async route => {
      if (route.request().method() !== 'POST') { await route.continue(); return }
      const response = await route.fetch()
      expect(response.status()).toBe(202)
      const body = await response.json()
      expect(body.success).toBe(true)
      accepted = body.data as DataSyncJob
      const submitted: unknown = route.request().postDataJSON()
      if (!submitted || typeof submitted !== 'object' || !('request_id' in submitted) || typeof submitted.request_id !== 'string') {
        throw new Error('Actual sync POST must carry its persisted request_id')
      }
      requestID = submitted.request_id
      expect(accepted.request_id).toBe(requestID)
      outage = true
      await route.abort('connectionfailed')
      deliveryLost = true
    })
    await modal(page).getByRole('button', { name: new RegExp(`^(${zhCN.dataSync.start}|${zhCN.dataSync.newOperation})$`) }).click()
    await expect.poll(() => deliveryLost, POLL).toBe(true)
    await expect.poll(async () => (await evidence(request)).writer_waiting, POLL).toBeGreaterThan(0)
    const pendingKey = `yanfa:data-sync:1:${JSON.stringify([fixture.actors.a.id, fixture.source_id])}`
    const pending = await page.evaluate(key => localStorage.getItem(key), pendingKey)
    expect(JSON.parse(pending!).requestId).toBe(requestID)
    await expect(modal(page).getByRole('alert')).toBeVisible()
    await closeSync(page)
    await composer(page).fill('T43 响应丢失保留的草稿')
    await composer(page).press('Enter')
    await expect(composer(page)).toHaveValue('T43 响应丢失保留的草稿')
    await expect(card.locator('table')).toHaveText(oldTable, { useInnerText: true })
    // Status delivery stays aborted until the next document loads, so no poll
    // can consume the original terminal result before the actual reload.
    await control(request, '/release-writer')
    await expect.poll(async () => { const current = await job(request, accepted!.job_id); return [current.status, current.release_confirmed] }, POLL).toEqual(['failed', true])
    const failed = await job(request, accepted!.job_id)
    expect(failed).toMatchObject({ request_id: requestID, expected_count: 7, processed_count: 7, succeeded_count: 6, failed_count: 1, verification_passed: true, partial_commits: 'known_partial_commits' })
    expect(failed.shop_results?.find(item => item.shop_id === '1')).toMatchObject({ status: 'failed', safe_error_code: 'SHOP_FAILED' })
    const actual = await evidence(request)
    expect(actual.writer_count).toBe(before.writer_count + 1)
    expect(actual.jobs.find(item => item.job_id === failed.job_id)).toMatchObject({ status: 'failed', succeeded_count: 6, failed_count: 1, release_confirmed: true })
    // This existing child commits its PG windows before reporting shop 1 failed.
    // Preserve those actual commits; successful watermarks alone cannot make a job succeed.
    expectWatermarks(actual.watermarks, before.watermarks, failed.cutoff!)
    expect((await status(request)).latest_successful_sync_at).toBe(previousSuccess)
    await page.unroute(jobsURL)
    expect(await page.evaluate(key => localStorage.getItem(key), pendingKey)).toBe(pending)
    page.once('framenavigated', () => { outage = false })
    const recovered = page.waitForResponse(response => response.request().method() === 'GET' && response.url().startsWith(`${API}/chats/${fixture.chats.a}/data-sync/status?`) && new URL(response.url()).searchParams.get('request_id') === requestID)
    await page.reload()
    const restored = await recovered
    expect((await restored.json()).data).toMatchObject({ request_match: 'found', matched_job: { job_id: failed.job_id, request_id: requestID, status: 'failed', release_confirmed: true } })
    await page.unroute(statusURL)
    await expect(page.getByRole('heading', { level: 2 }).first()).toBeVisible()
    const collapse = page.getByRole('button', { name: zhCN.sidebar.collapse, exact: true })
    if (await collapse.isVisible()) await collapse.click()
    await expect(page.getByRole('button', { name: zhCN.sidebar.expand, exact: true })).toBeVisible()
    await showSync(page, true)
    await expect(modal(page).getByText(zhCN.dataSync.failed, { exact: true })).toBeVisible()
    await expect(modal(page).getByText(zhCN.dataSync.counts(6, 1, 7, 7), { exact: true })).toBeVisible()
    await expect(modal(page).getByText(zhCN.dataSync.partial, { exact: true })).toBeVisible()
    await expect(modal(page).getByRole('progressbar')).not.toHaveAttribute('value')
    await expect.poll(() => page.evaluate(key => localStorage.getItem(key), pendingKey)).toBeNull()
    const replay = await data<DataSyncJob>(await request.post(jobsURL, { headers: auth(fixture.actors.a), data: { request_id: requestID } }))
    expect(replay).toMatchObject({ job_id: failed.job_id, request_id: requestID, status: 'failed', release_confirmed: true })
    expect((await evidence(request)).writer_count).toBe(before.writer_count + 1)
    expect((await evidence(request)).child_operations).toEqual(actual.child_operations)
    await closeSync(page)
    await page.reload()
    await expect(card.locator('table')).toHaveText(oldTable, { useInnerText: true })
    await expect(card.locator('[data-field="data-as-of"]')).toHaveText(old.report!.data_as_of)
    expect((await report(request, old.run_id)).report).toEqual(old.report)
    await observe('lost-202-terminal-partial-recovery', { request_id: requestID, job: failed, replay, evidence: actual, latest_successful_sync_at: previousSuccess })
  })
})
