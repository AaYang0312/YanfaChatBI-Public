import type { Page, Response } from '@playwright/test'
import { test, expect, ERP, ERP_B, PG, NAMES, OLD_RUN, type Barrier, type SyncApi } from './fixtures/v15-sync'
import { zhCN } from '../src/i18n/zh-CN'

// Controlled HTTP/SSE lifecycle proves the UI protocol, not formal ERP/bridge execution.
test.use({ viewport: { width: 1440, height: 1000 } })
const ownerA = 'isolated-owner-a'
const ownerB = 'isolated-owner-b'
const pendingKey = 'yanfa:data-sync:1:' + JSON.stringify([ownerA, '11111111-1111-4111-8111-111111111111'])
const posts = (api: SyncApi) => api.requests.filter(request => request.method === 'POST' && request.path.endsWith('/data-sync/jobs'))
const dialog = (page: Page) => page.getByRole('dialog', { name: zhCN.dataSync.title, exact: true })
const composer = (page: Page) => page.getByPlaceholder(zhCN.chat.placeholderConnected)
function requestID(api: SyncApi) {
  const body = posts(api)[0]?.body
  if (!body || typeof body !== 'object' || !('request_id' in body) || typeof body.request_id !== 'string') throw new Error('Missing sync request identity')
  return body.request_id
}
async function open(page: Page, id = ERP) {
  await page.goto(`/chat/${id}`)
  await expect(page.getByRole('heading', { level: 2, name: NAMES[id], exact: true })).toBeVisible()
  await expect(composer(page)).toBeEnabled()
}
async function show(page: Page, keyboard = false) {
  const options = page.getByRole('button', { name: zhCN.chat.chatOptions, exact: true }).and(page.locator(':visible'))
  if (keyboard) { await options.focus(); await options.press('ArrowDown') } else await options.click()
  const entry = page.getByRole('button', { name: new RegExp(`^(${zhCN.dataSync.menu}|${zhCN.dataSync.progress})$`) })
  if (keyboard) { await expect(entry).toBeFocused(); await entry.press('Enter') } else await entry.click()
  await expect(dialog(page)).toBeVisible()
  return dialog(page)
}
async function begin(page: Page, api: SyncApi) {
  const modal = await show(page)
  await expect(modal.getByRole('button', { name: zhCN.dataSync.start, exact: true })).toBeEnabled()
  await modal.getByRole('button', { name: zhCN.dataSync.start, exact: true }).click()
  await expect.poll(() => api.syncStarts).toBe(1)
  return Array.from(api.syncJobs.values())[0]
}
async function close(page: Page) {
  await dialog(page).getByRole('button', { name: zhCN.common.close, exact: true }).last().click()
  await expect(dialog(page)).toHaveCount(0)
}

for (const width of [1440, 390]) {
  test(`menu confirmation and keyboard focus at ${width}px start nothing; independent has no entry`, async ({ page, syncApi: api }) => {
    api.configureSync({ ready: true, operators: [ownerA] })
    await page.setViewportSize({ width, height: width === 390 ? 420 : 760 })
    await open(page)
    const modal = await show(page, true)
    await expect(modal.getByText(zhCN.dataSync.scope, { exact: true })).toBeVisible()
    await expect(modal.getByText(zhCN.dataSync.pause, { exact: true })).toBeVisible()
    await expect(modal.getByRole('button', { name: zhCN.common.close, exact: true }).first()).toBeFocused()
    const dimensions = await modal.evaluate(element => ({ width: element.getBoundingClientRect().width, screen: innerWidth, contentHeight: element.scrollHeight, visibleHeight: element.clientHeight }))
    expect(dimensions.width).toBeLessThanOrEqual(dimensions.screen)
    if (width === 390) expect(dimensions.contentHeight).toBeGreaterThan(dimensions.visibleHeight)
    await page.keyboard.press('Shift+Tab')
    await expect(modal.getByRole('button', { name: zhCN.common.close, exact: true }).last()).toBeFocused()
    await expect(modal.getByRole('button', { name: zhCN.common.close, exact: true }).last()).toBeInViewport()
    await page.keyboard.press('Tab')
    await expect(modal.getByRole('button', { name: zhCN.common.close, exact: true }).first()).toBeFocused()
    await expect(modal.getByRole('button', { name: zhCN.common.close, exact: true }).first()).toBeInViewport()
    await page.keyboard.press('Escape')
    await expect(dialog(page)).toHaveCount(0)
    await expect(page.getByRole('button', { name: zhCN.chat.chatOptions, exact: true }).and(page.locator(':visible'))).toBeFocused()
    expect(posts(api)).toEqual([])
    api.setPhase('outage')
    await open(page, PG)
    await page.getByRole('button', { name: zhCN.chat.chatOptions, exact: true }).and(page.locator(':visible')).click()
    await expect(page.getByRole('button', { name: zhCN.dataSync.menu, exact: true })).toHaveCount(0)
    await expect(page.getByRole('button', { name: zhCN.chat.refreshKnowledge, exact: true })).toBeEnabled()
    expect(posts(api)).toEqual([])
  })
}

test('view permission is separate from can_start and an unready source explains the disabled action', async ({ page, syncApi: api }) => {
  api.configureSync({ ready: true, operators: [] })
  await open(page)
  const modal = await show(page)
  await expect(modal.getByRole('button', { name: zhCN.dataSync.start, exact: true })).toBeDisabled()
  await expect(modal.getByText(zhCN.dataSync.forbidden, { exact: true })).toBeVisible()
  expect(posts(api)).toEqual([])
  await close(page)
  api.configureSync({ ready: false, operators: [ownerA] })
  await show(page)
  await expect(dialog(page).getByText(zhCN.dataSync.notReady, { exact: true })).toBeVisible()
  await expect(dialog(page).getByRole('button', { name: zhCN.dataSync.start, exact: true })).toBeDisabled()
  expect(posts(api)).toEqual([])
})

for (const partial of ['none_started', 'unknown'] as const) {
  test(`R1 F1 ${partial} with zero confirmed shops never claims data already updated`, async ({ page, syncApi: api }) => {
    api.configureSync({ ready: true, operators: [ownerA] })
    await open(page)
    const accepted = await begin(page, api)
    expect(accepted).toMatchObject({ status: 'created', stage: 'preparing', processed_count: 0,
      succeeded_count: 0, failed_count: 0, partial_commits: 'none_started', release_confirmed: false })
    api.setJob({ partial_commits: partial, processed_count: 0, succeeded_count: 0, failed_count: 0,
      ...(partial === 'unknown' ? { status: 'failed', stage: 'verifying', release_confirmed: true,
        safe_summary: '提交范围暂不能确认（受控）' } : {}) }, accepted.job_id)
    await dialog(page).getByRole('button', { name: zhCN.dataSync.refresh, exact: true }).click()
    await expect(dialog(page).getByText(partial === 'unknown' ? zhCN.dataSync.failed : zhCN.dataSync.preparing, { exact: true })).toBeVisible()
    if (partial === 'unknown') await expect(dialog(page).getByText('提交范围暂不能确认（受控）', { exact: true })).toBeVisible()
    await expect(dialog(page).getByText(zhCN.dataSync.counts(0, 0, 0, null), { exact: true })).toBeVisible()
    await expect(dialog(page).getByRole('progressbar')).not.toHaveAttribute('value')
    expect(posts(api)).toHaveLength(1)
    expect(api.syncStarts).toBe(1)
    if (partial === 'unknown') await expect(dialog(page).getByText(zhCN.dataSync.commitUnknown, { exact: true })).toBeVisible()
    else await expect(dialog(page).getByText(zhCN.dataSync.commitUnknown, { exact: true })).toHaveCount(0)
    await expect(dialog(page).getByText(zhCN.dataSync.partial, { exact: true })).toHaveCount(0)
  })
}

for (const partial of ['none_started', 'unknown'] as const) {
  test(`R1 F1 ${partial} still reports confirmed shops during running sync`, async ({ page, syncApi: api }) => {
    api.configureSync({ ready: true, operators: [ownerA], expectedCount: 7 })
    await open(page)
    const accepted = await begin(page, api)
    api.setJob({ status: 'running', stage: 'syncing', partial_commits: partial,
      processed_count: 1, succeeded_count: 1, failed_count: 0, safe_summary: '已确认一家店铺完成（受控）' }, accepted.job_id)
    await dialog(page).getByRole('button', { name: zhCN.dataSync.refresh, exact: true }).click()
    await expect(dialog(page).getByText('已确认一家店铺完成（受控）', { exact: true })).toBeVisible()
    await expect(dialog(page).getByText(zhCN.dataSync.counts(1, 0, 1, 7), { exact: true })).toBeVisible()
    await expect(dialog(page).getByText(zhCN.dataSync.partial, { exact: true })).toBeVisible()
    await expect(dialog(page).getByText(zhCN.dataSync.commitUnknown, { exact: true })).toHaveCount(0)
    await expect(dialog(page).getByRole('progressbar')).not.toHaveAttribute('value')
    expect(posts(api)).toHaveLength(1)
  })
}

test('double click persists only safe identity and sends only request_id, then closing keeps the real source blocked', async ({ page, syncApi: api }) => {
  api.configureSync({ ready: true, operators: [ownerA] })
  await open(page)
  await composer(page).fill('同步期间保留的草稿')
  const modal = await show(page)
  const held = api.hold('/data-sync/jobs')
  await modal.getByRole('button', { name: zhCN.dataSync.start, exact: true }).dblclick()
  await held.arrived
  await expect.poll(() => modal.evaluate(element => element.contains(document.activeElement))).toBe(true)
  await page.keyboard.press('Tab')
  await expect.poll(() => modal.evaluate(element => element.contains(document.activeElement))).toBe(true)
  expect(posts(api)).toHaveLength(1)
  const requestId = requestID(api)
  expect(posts(api)[0].body).toEqual({ request_id: requestId })
  const record = await page.evaluate(key => JSON.parse(localStorage.getItem(key)!), pendingKey)
  expect(record).toEqual({ version: 1, owner: ownerA, source: api.chats.get(ERP)!.source_id, requestId, chatId: ERP, bindingRevision: 1 })
  expect(JSON.stringify(record)).not.toMatch(/host|password|ssl|ssh|connection|report|shop/)
  held.release()
  await expect.poll(() => api.syncStarts).toBe(1)
  await expect(modal.getByRole('progressbar')).not.toHaveAttribute('value')
  await expect(modal.getByText(zhCN.dataSync.unknownTotal, { exact: true })).toBeVisible()
  await page.keyboard.press('Escape')
  await expect(modal).toHaveCount(0)
  await expect(page.getByRole('button', { name: zhCN.chat.chatOptions, exact: true }).and(page.locator(':visible'))).toBeFocused()
  await expect(page.locator('button[type="submit"]').last()).toBeDisabled()
  await composer(page).press('Enter')
  await expect(composer(page)).toHaveValue('同步期间保留的草稿')
  expect(posts(api)).toHaveLength(1)
  await show(page)
  await expect(dialog(page).getByRole('button', { name: zhCN.dataSync.start, exact: true })).toHaveCount(0)
})

test('same-source two tabs with view-only operator revocation converge without a second POST', async ({ page, context, syncApi: api }) => {
  api.configureSync({ ready: true, operators: [ownerA] })
  await open(page)
  const accepted = await begin(page, api)
  api.configureSync({ ready: true, operators: [] })
  const other = await context.newPage()
  try {
    await open(other, ERP_B)
    await show(other)
    await expect(dialog(other).getByRole('progressbar')).toBeVisible()
    await expect(dialog(other).getByRole('button', { name: zhCN.dataSync.start, exact: true })).toHaveCount(0)
    await expect.poll(() => api.requestLookups.some(read => read.chatId === ERP_B && read.requestId === accepted.request_id && read.match === 'found')).toBe(true)
    expect(api.syncStarts).toBe(1)
    expect(posts(api)).toHaveLength(1)
    await close(other)
    await expect(other.locator('button[type="submit"]').last()).toBeDisabled()
  } finally { await other.close() }
})

test('held POST not_found on another tab preserves the ID through an explicit rejected resend and the original acceptance', async ({ page, context, syncApi: api }) => {
  api.configureSync({ ready: true, operators: [ownerA] })
  await open(page)
  await show(page)
  const held = api.hold('/data-sync/jobs')
  await dialog(page).getByRole('button', { name: zhCN.dataSync.start, exact: true }).click()
  await held.arrived
  const requestId = requestID(api)
  const other = await context.newPage()
  try {
    await open(other, ERP_B)
    await show(other)
    await expect(dialog(other).getByText(zhCN.dataSync.notFound, { exact: true })).toBeVisible()
    expect(posts(api)).toHaveLength(1)
    expect(api.syncStarts).toBe(0)
    api.configureSync({ ready: true, operators: [] })
    await dialog(other).getByRole('button', { name: zhCN.dataSync.resend, exact: true }).click()
    await expect.poll(() => posts(api).length).toBe(2)
    expect(posts(api)[1].body).toEqual({ request_id: requestId })
    await expect.poll(async () => other.evaluate(key => JSON.parse(localStorage.getItem(key)!).requestId, pendingKey)).toBe(requestId)
    expect(api.syncStarts).toBe(0)
    api.configureSync({ ready: true, operators: [ownerA] })
    held.release()
    await expect.poll(() => api.syncStarts).toBe(1)
    await expect(dialog(other).getByText(zhCN.dataSync.notFound, { exact: true })).toHaveCount(0)
    expect(Array.from(api.syncJobs.values())[0].request_id).toBe(requestId)
    expect(posts(api)).toHaveLength(2)
  } finally { held.release(); await other.close() }
})

test('simultaneous two-tab confirmations cannot allocate or POST a second unknown operation', async ({ page, context, syncApi: api }) => {
  api.configureSync({ ready: true, operators: [ownerA] })
  await open(page)
  const other = await context.newPage()
  try {
    await open(other, ERP_B)
    await show(page); await show(other)
    await expect(dialog(page).getByRole('button', { name: zhCN.dataSync.start, exact: true })).toBeEnabled()
    await expect(dialog(other).getByRole('button', { name: zhCN.dataSync.start, exact: true })).toBeEnabled()
    // Hold the browser's actual record lock so both user actions reach allocation.
    await page.evaluate(async key => {
      const hold = Promise.withResolvers<void>()
      const ready = Promise.withResolvers<void>()
      Object.defineProperty(window, 'releaseSyncRecordLock', { value: hold.resolve })
      void navigator.locks.request(key, async () => { ready.resolve(); await hold.promise })
      await ready.promise
    }, pendingKey)
    await Promise.all([
      dialog(page).getByRole('button', { name: zhCN.dataSync.start, exact: true }).click(),
      dialog(other).getByRole('button', { name: zhCN.dataSync.start, exact: true }).click(),
    ])
    await expect(dialog(page).getByRole('button', { name: zhCN.dataSync.start, exact: true })).toBeDisabled()
    await expect(dialog(other).getByRole('button', { name: zhCN.dataSync.start, exact: true })).toBeDisabled()
    expect(posts(api)).toEqual([])
    await page.evaluate(() => {
      if (!('releaseSyncRecordLock' in window) || typeof window.releaseSyncRecordLock !== 'function') throw new Error('Missing lock barrier')
      window.releaseSyncRecordLock()
    })
    await expect.poll(() => api.syncStarts).toBe(1)
    await expect(dialog(page).getByRole('button', { name: zhCN.dataSync.start, exact: true })).toHaveCount(0)
    await expect(dialog(other).getByRole('button', { name: zhCN.dataSync.start, exact: true })).toHaveCount(0)
    expect(posts(api)).toHaveLength(1)
    const requestId = requestID(api)
    const recordA = await page.evaluate(key => JSON.parse(localStorage.getItem(key)!), pendingKey)
    const recordB = await other.evaluate(key => JSON.parse(localStorage.getItem(key)!), pendingKey)
    expect(recordA.requestId).toBe(requestId)
    expect(recordB).toEqual(recordA)
  } finally { await other.close() }
})

test('an explicit same-ID resend reaches one writer; only a new operation after safe terminal gets a new ID', async ({ page, context, syncApi: api }) => {
  api.configureSync({ ready: true, operators: [ownerA] })
  await open(page)
  await show(page)
  const held = api.hold('/data-sync/jobs')
  await dialog(page).getByRole('button', { name: zhCN.dataSync.start, exact: true }).click()
  await held.arrived
  const requestId = requestID(api)
  const other = await context.newPage()
  try {
    await open(other, ERP_B)
    await show(other)
    await expect(dialog(other).getByText(zhCN.dataSync.notFound, { exact: true })).toBeVisible()
    await dialog(other).getByRole('button', { name: zhCN.dataSync.resend, exact: true }).click()
    await expect.poll(() => api.syncStarts).toBe(1)
    held.release()
    await expect(dialog(page).getByText(zhCN.dataSync.notFound, { exact: true })).toHaveCount(0)
    expect(posts(api).map(request => request.body)).toEqual([{ request_id: requestId }, { request_id: requestId }])
    expect(api.syncStarts).toBe(1)
    api.setJob({ status: 'succeeded', stage: 'verifying', verification_passed: true, release_confirmed: true,
      finished_at: '2026-11-07T02:03:04Z', expected_count: 7, processed_count: 7, succeeded_count: 7, partial_commits: 'all_windows_confirmed' })
    await expect(dialog(other).getByText(zhCN.dataSync.succeeded, { exact: true })).toBeVisible()
    await expect(dialog(other).getByText(zhCN.dataSync.partial, { exact: true })).toHaveCount(0)
    await expect(dialog(other).getByRole('progressbar')).toHaveAttribute('value', '100')
    await expect.poll(async () => other.evaluate(key => localStorage.getItem(key), pendingKey)).toBeNull()
    expect(posts(api)).toHaveLength(2)
    await expect(dialog(other).getByRole('button', { name: zhCN.dataSync.newOperation, exact: true })).toBeEnabled()
    await dialog(other).getByRole('button', { name: zhCN.dataSync.newOperation, exact: true }).click()
    await expect.poll(() => api.syncStarts).toBe(2)
    const latest = posts(api)[2].body
    expect(latest).not.toEqual({ request_id: requestId })
    expect(Array.from(api.syncJobs.values()).map(job => job.initiating_chat_id)).toEqual([ERP, ERP_B])
  } finally { held.release(); await other.close() }
})

test('R1 F2 delivered A 202 cannot replace visible B after both safe terminals clear pending', async ({ page, context, syncApi: api }) => {
  api.configureSync({ ready: true, operators: [ownerA], expectedCount: 7 })
  await open(page)
  const other = await context.newPage()
  let held: Barrier | undefined
  let secondaryMetadata: Barrier | undefined
  const responsesA: { response: Response; receivedAt: number }[] = []
  const observeA = (response: Response) => {
    if (response.request().method() === 'POST' && new URL(response.url()).pathname.endsWith(`/chats/${ERP}/data-sync/jobs`)) responsesA.push({ response, receivedAt: Date.now() })
  }
  page.on('response', observeA)
  try {
    // Both windows are ready before the ten-second production POST clock starts.
    await open(other, ERP_B)
    await show(other); await show(page)
    await expect(dialog(page).getByRole('button', { name: zhCN.dataSync.start, exact: true })).toBeEnabled()
    await expect(dialog(other).getByRole('button', { name: zhCN.dataSync.start, exact: true })).toBeEnabled()
    const original = api.chats.get(ERP)!
    const identity = { owner: original.user_id, chatId: original.id, source: original.source_id, binding: original.source_binding_revision }
    const originalURL = page.url()
    const visibility: string[] = []
    await page.exposeFunction('observeT42F2Visibility', (state: string) => { visibility.push(state) })
    await page.evaluate(() => {
      const record = Reflect.get(window, 'observeT42F2Visibility')
      if (typeof record !== 'function') throw new Error('Missing visibility observer')
      void record(document.visibilityState)
      document.addEventListener('visibilitychange', () => { void record(document.visibilityState) })
    })
    expect(await page.evaluate(() => document.visibilityState)).toBe('visible')
    const refresh = async (target: Page) => {
      const button = dialog(target).getByRole('button', { name: zhCN.dataSync.refresh, exact: true })
      await expect(button).toBeEnabled()
      const metadata = target.waitForResponse(response => response.request().method() === 'GET'
        && new URL(response.url()).pathname.endsWith('/data-sync/status'))
      await button.click()
      await (await metadata).finished()
      await expect(button).toBeEnabled()
    }
    // Let the original window accept A's terminal before the other window can
    // clear its shared record. Neither A's admission nor the original target is held.
    secondaryMetadata = api.hold('/data-sync/status', ERP_B)
    held = api.holdResponse('/data-sync/jobs', ERP)
    const sentAt = Date.now()
    await dialog(page).getByRole('button', { name: zhCN.dataSync.start, exact: true }).click()
    await held.arrived
    expect(posts(api)).toHaveLength(1)
    expect(api.syncStarts).toBe(1)
    const acceptedA = Array.from(api.syncJobs.values())[0]
    expect(acceptedA.request_id).toBe(requestID(api))
    api.setJob({ status: 'failed', stage: 'verifying', expected_count: 7, processed_count: 1, succeeded_count: 0, failed_count: 1,
      release_confirmed: true, partial_commits: 'unknown', safe_summary: 'R1 F2 A 原结果' }, acceptedA.job_id)
    await refresh(page)
    await expect(dialog(page).getByText('R1 F2 A 原结果', { exact: true })).toBeVisible()
    expect(await page.evaluate(key => localStorage.getItem(key), pendingKey)).toBeNull()
    expect(api.requestLookups.some(read => read.chatId === ERP && read.requestId === acceptedA.request_id && read.match === 'found')).toBe(true)
    secondaryMetadata.release()
    await refresh(other)
    await expect(dialog(other).getByRole('button', { name: new RegExp(`^(${zhCN.dataSync.start}|${zhCN.dataSync.newOperation})$`) })).toBeEnabled()
    await dialog(other).getByRole('button', { name: new RegExp(`^(${zhCN.dataSync.start}|${zhCN.dataSync.newOperation})$`) }).click()
    await expect.poll(() => api.syncStarts).toBe(2)
    const acceptedB = Array.from(api.syncJobs.values()).find(job => job.job_id !== acceptedA.job_id)!
    expect(acceptedB.request_id).not.toBe(acceptedA.request_id)
    expect(posts(api).map(request => request.body)).toEqual([{ request_id: acceptedA.request_id }, { request_id: acceptedB.request_id }])
    api.setJob({ status: 'running', stage: 'syncing', safe_summary: 'R1 F2 B 当前运行' }, acceptedB.job_id)
    await refresh(page)
    await expect(dialog(page).getByText('R1 F2 B 当前运行', { exact: true })).toBeVisible()
    const pendingB = await page.evaluate(key => JSON.parse(localStorage.getItem(key)!), pendingKey)
    expect(pendingB.requestId).toBe(acceptedB.request_id)
    api.setJob({ status: 'failed', stage: 'verifying', expected_count: 7, processed_count: 2, succeeded_count: 0, failed_count: 2,
      release_confirmed: true, partial_commits: 'unknown', safe_summary: 'R1 F2 B 当前结果' }, acceptedB.job_id)
    await refresh(page)
    await expect(dialog(page).getByText('R1 F2 B 当前结果', { exact: true })).toBeVisible()
    await expect(dialog(page).getByText(zhCN.dataSync.counts(0, 2, 2, 7), { exact: true })).toBeVisible()
    expect(await page.evaluate(key => localStorage.getItem(key), pendingKey)).toBeNull()
    const current = api.chats.get(ERP)!
    expect({ owner: current.user_id, chatId: current.id, source: current.source_id, binding: current.source_binding_revision }).toEqual(identity)
    expect(page.url()).toBe(originalURL)
    expect(await page.evaluate(() => document.visibilityState)).toBe('visible')
    expect(visibility).not.toContain('hidden')
    expect(responsesA).toEqual([])
    expect(Date.now() - sentAt, 'F2 requires the original successful response before the unchanged 10s POST timeout').toBeLessThan(10000)
    held.release()
    await expect.poll(() => responsesA[0]?.response.status()).toBe(202)
    const deliveredA = responsesA[0]
    expect(await deliveredA.response.finished()).toBeNull()
    expect(await deliveredA.response.json()).toMatchObject({ success: true, data: { job_id: acceptedA.job_id,
      request_id: acceptedA.request_id, status: 'created', stage: 'preparing', partial_commits: 'none_started' } })
    expect(deliveredA.receivedAt - sentAt, 'A HTTP202 must actually arrive before timeout; a cancelled request is not this regression input').toBeLessThan(10000)
    await refresh(page)
    expect(posts(api)).toHaveLength(2)
    expect(api.syncStarts).toBe(2)
    expect(api.syncJobs.size).toBe(2)
    expect(await page.evaluate(key => localStorage.getItem(key), pendingKey)).toBeNull()
    expect(page.url()).toBe(originalURL)
    expect(await page.evaluate(() => document.visibilityState)).toBe('visible')
    expect(visibility).not.toContain('hidden')
    await expect(dialog(page).getByText('R1 F2 B 当前结果', { exact: true })).toBeVisible()
    await expect(dialog(page).getByText(zhCN.dataSync.counts(0, 2, 2, 7), { exact: true })).toBeVisible()
    await expect(dialog(page).getByText('R1 F2 A 原结果', { exact: true })).toHaveCount(0)
  } finally { secondaryMetadata?.release(); held?.release(); page.off('response', observeA); await other.close() }
})

test('lost response on an open page only queries the original request without automatic POST', async ({ page, syncApi: api }) => {
  api.configureSync({ ready: true, operators: [ownerA] })
  await open(page)
  await show(page)
  await page.route('**/data-sync/jobs', async route => {
    const accepted = await route.fetch()
    expect(accepted.status()).toBe(202)
    await route.abort('failed')
  })
  await dialog(page).getByRole('button', { name: zhCN.dataSync.start, exact: true }).click()
  await expect.poll(() => api.syncStarts).toBe(1)
  const requestId = requestID(api)
  await expect.poll(() => api.requestLookups.some(read => read.requestId === requestId && read.match === 'found')).toBe(true)
  api.configureSync({ ready: true, operators: [] })
  api.setJob({ status: 'failed', stage: 'verifying', expected_count: 7, processed_count: 7, succeeded_count: 6, failed_count: 1,
    release_confirmed: true, partial_commits: 'known_partial_commits', safe_summary: '原请求在响应丢失后完成（受控）' })
  await expect(dialog(page).getByText('原请求在响应丢失后完成（受控）', { exact: true })).toBeVisible()
  await expect.poll(async () => page.evaluate(key => localStorage.getItem(key), pendingKey)).toBeNull()
  expect(posts(api)).toHaveLength(1)
  expect(api.syncStarts).toBe(1)
})

test('lost202 restores the exact terminal job after reload without another writer or ID', async ({ page, syncApi: api }) => {
  api.configureSync({ ready: true, operators: [ownerA] })
  await open(page)
  await show(page)
  const response = api.holdResponse('/data-sync/jobs')
  api.loseNext202()
  await dialog(page).getByRole('button', { name: zhCN.dataSync.start, exact: true }).click()
  await response.arrived
  const accepted = Array.from(api.syncJobs.values())[0]
  // Stop page IO before publishing terminal, so only reload can recover it.
  await page.goto('about:blank')
  api.setJob({ status: 'succeeded', stage: 'verifying', processed_count: 7, succeeded_count: 7, expected_count: 7,
    verification_passed: true, release_confirmed: true, finished_at: '2026-11-07T01:02:03Z', safe_summary: '已核验并安全释放' })
  api.configureSync({ ready: true, operators: [] })
  response.release()
  await open(page)
  await show(page)
  await expect(dialog(page).getByText(zhCN.dataSync.succeeded, { exact: true })).toBeVisible()
  await expect(dialog(page).getByRole('progressbar')).toHaveAttribute('value', '100')
  await expect.poll(() => api.requestLookups.some(read => read.requestId === accepted.request_id && read.match === 'found')).toBe(true)
  await expect.poll(async () => page.evaluate(key => localStorage.getItem(key), pendingKey)).toBeNull()
  expect(posts(api)).toHaveLength(1)
  expect(api.syncStarts).toBe(1)
})

test('7 of 7 verifying is indeterminate; partial failure preserves old report and last success', async ({ page, syncApi: api }) => {
  api.configureSync({ ready: true, operators: [ownerA], expectedCount: 7 })
  await open(page)
  const report = page.locator(`[data-run-id="${OLD_RUN}"]`)
  const oldAsOf = await report.locator('[data-field="data-as-of"]').innerText()
  await begin(page, api)
  api.setJob({ status: 'running', stage: 'verifying', processed_count: 7, succeeded_count: 7, started_at: '2026-11-07T00:00:00Z' })
  await expect(dialog(page).getByText(zhCN.dataSync.verifying, { exact: true })).toBeVisible()
  await expect(dialog(page).getByText(zhCN.dataSync.counts(7, 0, 7, 7), { exact: true })).toBeVisible()
  await expect(dialog(page).getByRole('progressbar')).not.toHaveAttribute('value')
  await expect(dialog(page).getByText(zhCN.dataSync.succeeded, { exact: true })).toHaveCount(0)
  api.setJob({ status: 'failed', stage: 'verifying', processed_count: 7, succeeded_count: 6, failed_count: 1,
    verification_passed: false, release_confirmed: true, partial_commits: 'known_partial_commits', safe_summary: '一店结果核验失败，其他已提交数据保留' })
  await expect(dialog(page).getByText(zhCN.dataSync.failed, { exact: true })).toBeVisible()
  await expect(dialog(page).getByText(zhCN.dataSync.partial, { exact: true })).toBeVisible()
  await expect(dialog(page).getByText(zhCN.dataSync.counts(6, 1, 7, 7), { exact: true })).toBeVisible()
  await expect(dialog(page).getByText(zhCN.dataSync.noSuccess, { exact: true })).toBeVisible()
  await expect(dialog(page).getByRole('progressbar')).not.toHaveAttribute('value')
  await close(page)
  await expect(report.locator('[data-field="data-as-of"]')).toHaveText(oldAsOf)
  await expect(report.locator('tfoot td')).toHaveText(['3', '120', '72', '48', '0.4', '40', '10'])
  await expect(page.locator('button[type="submit"]').last()).toBeEnabled()
  expect(api.syncStarts).toBe(1)
})

test('interrupted without release stays recovering; safe terminal stops job polls while source discovery continues', async ({ page, syncApi: api }) => {
  api.configureSync({ ready: true, operators: [ownerA] })
  await open(page)
  await begin(page, api)
  api.setJob({ status: 'interrupted', stage: 'verifying', partial_commits: 'unknown', release_confirmed: false })
  await expect(dialog(page).getByText(zhCN.dataSync.releasing, { exact: true })).toBeVisible()
  await expect(dialog(page).getByText(zhCN.dataSync.interrupted, { exact: true })).toHaveCount(0)
  await close(page)
  await expect(page.locator('button[type="submit"]').last()).toBeDisabled()
  api.setJob({ release_confirmed: true })
  await show(page)
  await expect(dialog(page).getByText(zhCN.dataSync.interrupted, { exact: true })).toBeVisible()
  const jobReads = api.requests.filter(request => /\/data-sync\/jobs\//.test(request.path)).length
  const sourceReads = api.statusCalls
  await expect.poll(() => api.statusCalls).toBeGreaterThan(sourceReads + 1)
  expect(api.requests.filter(request => /\/data-sync\/jobs\//.test(request.path))).toHaveLength(jobReads)
  expect(api.syncStarts).toBe(1)
})

test('storage denial sends zero POST and the visible error allows a safe manual recovery', async ({ page, syncApi: api }) => {
  api.configureSync({ ready: true, operators: [ownerA] })
  await open(page)
  await show(page)
  await page.evaluate(() => {
    const original = Storage.prototype.setItem
    Object.defineProperty(window, 'restoreSyncStorage', { value: () => { Storage.prototype.setItem = original } })
    Storage.prototype.setItem = function(key, value) { if (key.startsWith('yanfa:data-sync:')) throw new DOMException('denied', 'QuotaExceededError'); original.call(this, key, value) }
  })
  await dialog(page).getByRole('button', { name: zhCN.dataSync.start, exact: true }).click()
  await expect(dialog(page).getByRole('alert')).toHaveText(zhCN.dataSync.storage)
  expect(posts(api)).toEqual([])
  await page.evaluate(() => {
    if (!('restoreSyncStorage' in window) || typeof window.restoreSyncStorage !== 'function') throw new Error('Missing storage restoration')
    window.restoreSyncStorage()
  })
  await dialog(page).getByRole('button', { name: zhCN.dataSync.refresh, exact: true }).click()
  await expect(dialog(page).getByRole('button', { name: zhCN.dataSync.start, exact: true })).toBeEnabled()
  await dialog(page).getByRole('button', { name: zhCN.dataSync.start, exact: true }).click()
  await expect.poll(() => api.syncStarts).toBe(1)
  expect(posts(api)).toHaveLength(1)
})

test('closing and offline recovery retain operation, draft and report without automatic POST', async ({ page, context, syncApi: api }) => {
  api.configureSync({ ready: true, operators: [ownerA] })
  await open(page)
  await composer(page).fill('离线后保留的同步草稿')
  await begin(page, api)
  await close(page)
  const record = await page.evaluate(key => localStorage.getItem(key), pendingKey)
  await context.setOffline(true)
  await show(page)
  await expect(dialog(page).getByRole('alert')).toBeVisible()
  await close(page)
  await expect(composer(page)).toHaveValue('离线后保留的同步草稿')
  await expect(page.locator(`[data-run-id="${OLD_RUN}"] tfoot td`)).toHaveText(['3', '120', '72', '48', '0.4', '40', '10'])
  api.setPhase('recovering')
  await context.setOffline(false)
  await show(page)
  await expect(dialog(page).getByText(zhCN.sourceAccess.recovering, { exact: true })).toBeVisible()
  expect(await page.evaluate(key => localStorage.getItem(key), pendingKey)).toBe(record)
  expect(posts(api)).toHaveLength(1)
})

test('switching target and owner while POST response is late never reuses or clears another owner record', async ({ page, syncApi: api }) => {
  api.configureSync({ ready: true, operators: [ownerA] })
  await open(page)
  await show(page)
  const held = api.holdResponse('/data-sync/jobs')
  await dialog(page).getByRole('button', { name: zhCN.dataSync.start, exact: true }).click()
  await held.arrived
  const requestId = requestID(api)
  await close(page)
  await page.getByRole('button', { name: new RegExp(NAMES[PG]) }).first().click()
  await expect(page).toHaveURL(new RegExp(`/chat/${PG}$`))
  held.release()
  await expect(composer(page)).toBeEnabled()
  const saved = await page.evaluate(key => localStorage.getItem(key), pendingKey)
  api.setOwner(ownerB)
  await page.reload()
  await expect(composer(page)).toBeEnabled()
  await page.getByRole('button', { name: new RegExp(NAMES[ERP_B]) }).first().click()
  await show(page)
  await expect(dialog(page).getByRole('progressbar')).toBeVisible()
  expect(api.requestLookups.filter(read => read.owner === ownerB && read.requestId === requestId)).toEqual([])
  expect(await page.evaluate(key => localStorage.getItem(key), pendingKey)).toBe(saved)
  expect(posts(api)).toHaveLength(1)
  expect(api.syncStarts).toBe(1)
})

test('open confirmation returns focus to the visible options action after responsive width changes', async ({ page, syncApi: api }) => {
  api.configureSync({ ready: true, operators: [ownerA] })
  await open(page)
  await show(page, true)
  for (const width of [390, 1440]) {
    await page.setViewportSize({ width, height: width === 390 ? 420 : 1000 })
    await page.keyboard.press('Escape')
    await expect(dialog(page)).toHaveCount(0)
    await expect(page.getByRole('button', { name: zhCN.chat.chatOptions, exact: true }).and(page.locator(':visible'))).toBeFocused()
    if (width === 390) await show(page, true)
  }
  expect(posts(api)).toEqual([])
})
