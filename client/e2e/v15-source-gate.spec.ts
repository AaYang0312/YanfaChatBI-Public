import type { Page, Response } from '@playwright/test'
import { test, expect, ERP, ERP_B, PG, NAMES, OLD_RUN, type SyncApi } from './fixtures/v15-sync'
import { zhCN } from '../src/i18n/zh-CN'
import { DASHBOARD_SSE_EVENTS, type DashboardBlueprint, type DashboardGenerationCompleteEvent, type DashboardGenerationProgressEvent, type DashboardWidgetDataEvent } from '../src/types/dashboard'

declare global {
  interface Window {
    recordT41WidgetEvent: (event: string, data: DashboardWidgetDataEvent) => Promise<void>
    recordT41DashboardComplete: (data: DashboardGenerationCompleteEvent) => Promise<void>
    recordT41BatchResult: (data: DashboardGenerationProgressEvent) => Promise<void>
  }
}

// Real page/HTTP observations against the controlled T39 wire protocol only.
test.use({ viewport: { width: 1440, height: 1000 } })
const composer = (page: Page) => page.getByPlaceholder(zhCN.chat.placeholderConnected)
const creates = (api: SyncApi) => api.sourceRequests.filter(request => request.suffix === '/assistant/runs')
async function open(page: Page, id = ERP) {
  await page.goto(`/chat/${id}`)
  await expect(page.getByRole('heading', { level: 2, name: NAMES[id], exact: true })).toBeVisible()
  await expect(composer(page)).toBeEnabled()
}
async function choose(page: Page) {
  await page.getByRole('button', { name: zhCN.assistant.menu.title, exact: true }).click()
  await page.getByRole('menuitemradio', { name: zhCN.assistant.menu.productAnalysis, exact: true }).click()
  await expect(page.getByRole('form', { name: zhCN.assistant.input.title })).toBeVisible()
}
async function waitBlocked(page: Page, reason = zhCN.sourceAccess.syncing) {
  await expect(page.getByText(reason, { exact: true }).first()).toBeVisible()
  await expect(page.locator('button[type="submit"]').last()).toBeDisabled()
}
async function select(page: Page, id: string) {
  await page.getByRole('button', { name: new RegExp(NAMES[id]) }).first().click()
  await expect(page).toHaveURL(new RegExp(`/chat/${id}$`))
  await expect(page.getByRole('heading', { level: 2, name: NAMES[id], exact: true })).toBeVisible()
}

const isStatus = (response: Response) => response.request().method() === 'GET' && new URL(response.url()).pathname.endsWith(`/chats/${ERP}/data-sync/status`)
const widgetCard = (page: Page) => page.locator('div[tabindex="-1"]').filter({ has: page.getByRole('heading', { name: '快照组件', exact: true }) })
const blueprintPicker = (page: Page) => page.locator('div.fixed').filter({ has: page.getByRole('heading', { name: '选择仪表盘', exact: true }) })
const dashboardCreates = (api: SyncApi) => api.requests.filter(request => request.method === 'POST' && request.path === `/chats/${ERP}/dashboards/create-from-blueprints`)
const recoveryRequests = (api: SyncApi) => ({
  probe: api.sourceRequests.filter(request => request.chatId === ERP && request.suffix === '/connection-status').length,
  connect: api.sourceRequests.filter(request => request.chatId === ERP && request.suffix === '/connect').length,
  dashboard: api.sourceRequests.filter(request => request.chatId === ERP && request.suffix === `/dashboards/dash-${ERP}/refresh`).length,
  widget: api.sourceRequests.filter(request => request.chatId === ERP && request.suffix === `/dashboards/dash-${ERP}/widgets/widget-${ERP}/refresh`).length,
})

async function openDashboard(page: Page) {
  await page.getByRole('button', { name: zhCN.dashboard.dashboard, exact: true }).click()
  await expect(widgetCard(page).getByText('48', { exact: true })).toBeVisible()
  await expect(page.getByTitle(zhCN.dashboard.refreshWidget, { exact: true })).toBeEnabled()
}

async function nextDiscoveryCycle(page: Page) {
  const response = await page.waitForResponse(isStatus)
  await response.finished()
  // The next serial request starts only after the preceding response was applied and its 2s timer fired.
  await page.waitForRequest(request => request.method() === 'GET' && new URL(request.url()).pathname.endsWith(`/chats/${ERP}/data-sync/status`))
}

async function rebindCurrentChat(page: Page, api: SyncApi) {
  const metadata = page.waitForResponse(response => response.request().method() === 'GET' && new URL(response.url()).pathname.endsWith(`/chats/${ERP}`))
  const current = api.rebind()
  const owned = await metadata
  expect(await owned.json()).toMatchObject({ success: true, data: { id: ERP, user_id: current.user_id, source_id: current.source_id, source_binding_revision: current.source_binding_revision } })
  await owned.finished()
  await nextDiscoveryCycle(page)
  await expect(page.getByTitle(zhCN.dashboard.refreshNow, { exact: true })).toBeEnabled()
  expect(api.statusResponses).toContainEqual(expect.objectContaining({ source_id: current.source_id, binding_revision: current.source_binding_revision, ask_allowed: true }))
  await expect(page).toHaveURL(new RegExp(`/chat/${ERP}$`))
  return current
}

async function observeWidgetEvents(page: Page) {
  const events: { event: string; data: DashboardWidgetDataEvent }[] = []
  await page.exposeFunction('recordT41WidgetEvent', (event: string, data: DashboardWidgetDataEvent) => { events.push({ event, data }) })
  await page.evaluate(() => {
    for (const name of ['dashboard-widget-data', 'dashboard-widget-error']) {
      window.addEventListener(name, event => { void window.recordT41WidgetEvent(name, (event as CustomEvent<DashboardWidgetDataEvent>).detail) })
    }
  })
  return events
}

async function refreshWidgetWith(page: Page, api: SyncApi, value: number) {
  const reply = api.holdWidgetEvent('data', value)
  await page.getByTitle(zhCN.dashboard.refreshWidget, { exact: true }).click()
  await reply.arrived
  reply.release()
  await expect(widgetCard(page).getByText(String(value), { exact: true })).toBeVisible()
  await expect(page.getByTitle(zhCN.dashboard.refreshWidget, { exact: true })).toBeEnabled()
}

async function generateBlueprints(page: Page, prompt: string, binding: 'A' | 'B', names = [0, 1].map(index => `isolated ${binding} 方案 ${index}`)) {
  await page.locator('button.dashboard-selector-btn').click()
  await page.getByRole('button', { name: zhCN.dashboard.new, exact: true }).click()
  await page.getByPlaceholder(zhCN.dashboard.insightsPlaceholder).fill(prompt)
  const completed = page.waitForResponse(response => response.request().method() === 'POST' && new URL(response.url()).pathname.endsWith(`/chats/${ERP}/dashboards/suggest-templates`))
  await page.getByRole('button', { name: zhCN.dashboard.generate, exact: true }).click()
  expect((await completed).ok()).toBe(true)
  await expect(page.getByPlaceholder(zhCN.dashboard.insightsPlaceholder)).toHaveCount(0)
  for (const name of names) await expect(blueprintPicker(page).getByRole('heading', { name, exact: true })).toBeVisible()
}

for (const phase of ['syncing', 'recovering', 'unresolved_erp'] as const) {
  test(`${phase}: all visible source/model entries refuse while persisted views remain usable`, async ({ page, syncApi: api }) => {
    api.setPhase(phase)
    await open(page)
    await waitBlocked(page, phase === 'syncing' ? zhCN.sourceAccess.syncing : phase === 'recovering' ? zhCN.sourceAccess.recovering : zhCN.sourceAccess.unresolved)
    const draft = composer(page)
    await draft.fill('门禁拒绝后仍保留的草稿')
    const before = api.sourceRequests.length
    await draft.press('Enter')
    await expect(draft).toHaveValue('门禁拒绝后仍保留的草稿')
    await expect(page.getByRole('button', { name: '重试历史问题', exact: true })).toBeDisabled()
    await expect(page.getByRole('button', { name: '修复历史错误', exact: true })).toBeDisabled()
    await expect(page.locator('[data-tooltip="' + zhCN.query.rerunQuery + '"]')).toBeDisabled()
    await expect(page.getByRole('button', { name: zhCN.assistant.card.switchBasis, exact: true })).toBeDisabled()
    await page.getByTitle(zhCN.chat.editMessage, { exact: true }).click()
    const edited = page.locator('textarea:not([placeholder])')
    await edited.fill('编辑被拒仍保留')
    await expect(page.getByRole('button', { name: zhCN.chat.sendMessage, exact: true })).toBeDisabled()
    await expect(edited).toHaveValue('编辑被拒仍保留')
    await page.getByRole('button', { name: zhCN.common.cancel, exact: true }).click()
    await choose(page)
    const form = page.getByRole('form', { name: zhCN.assistant.input.title })
    await page.getByLabel(zhCN.assistant.input.question, { exact: true }).fill('保留工作流条件')
    await form.evaluate((element: HTMLFormElement) => element.requestSubmit())
    await expect(page.getByLabel(zhCN.assistant.input.question, { exact: true })).toHaveValue('保留工作流条件')
    await page.getByRole('button', { name: zhCN.assistant.menu.close, exact: true }).click()
    await expect(page.locator(`[data-run-id="${OLD_RUN}"] tfoot td`)).toHaveText(['3', '120', '72', '48', '0.4', '40', '10'])
    await page.locator('[data-tooltip="' + zhCN.query.tableView + '"]').click()
    const next = page.getByTitle(zhCN.query.nextPage, { exact: true })
    await next.click() // Page 2 is already in the persisted first batch.
    await expect(page.getByRole('cell').filter({ hasText: 'snapshot-26' })).toBeVisible()
    await page.locator('[data-tooltip="' + zhCN.query.downloadData + '"]').click()
    const download = page.waitForEvent('download')
    await page.getByRole('button', { name: zhCN.query.exportCsv, exact: true }).click()
    expect((await download).suggestedFilename()).toContain('old-query')
    await expect(page.locator('[data-tooltip="' + zhCN.query.visualizationView + '"]')).toBeEnabled()
    await page.locator('[data-tooltip="' + zhCN.query.visualizationView + '"]').click()
    await expect(page.getByRole('button', { name: zhCN.viz.load, exact: true })).toBeDisabled()
    await page.getByRole('button', { name: zhCN.dashboard.dashboard, exact: true }).click()
    await expect(page.getByText('快照组件', { exact: true })).toBeVisible()
    await expect(page.getByTitle(zhCN.dashboard.refreshNow, { exact: true })).toHaveCount(0)
    await page.getByRole('button', { name: zhCN.dashboard.chat, exact: true }).click()
    await page.getByRole('button', { name: zhCN.chat.chatOptions, exact: true }).and(page.locator(':visible')).click()
    await expect(page.getByRole('button', { name: zhCN.chat.refreshKnowledge, exact: true })).toBeDisabled()
    await page.getByRole('button', { name: zhCN.sidebar.chatSettings, exact: true }).click()
    await page.getByRole('button', { name: zhCN.connection.tabKnowledge, exact: true }).click()
    await expect(page.getByRole('button', { name: zhCN.connection.updateKnowledge, exact: true })).toBeDisabled()
    await page.getByRole('button', { name: zhCN.common.close, exact: true }).last().click()
    expect(api.sourceRequests).toHaveLength(before)
  })
}

test('candidate and a late resolve recheck the same target before creating a run', async ({ page, syncApi: api }) => {
  await open(page)
  await expect(page.getByRole('button', { name: zhCN.assistant.card.switchBasis, exact: true })).toBeEnabled()
  await choose(page)
  const form = page.getByRole('form', { name: zhCN.assistant.input.title })
  await page.getByLabel(zhCN.assistant.input.question, { exact: true }).fill('候选商品')
  await form.getByRole('button', { name: zhCN.assistant.input.submit, exact: true }).click()
  await expect(page.getByRole('button', { name: 'isolated 候选 P1', exact: true })).toBeVisible()
  api.setPhase('syncing')
  await waitBlocked(page)
  await expect(page.getByRole('button', { name: 'isolated 候选 P1', exact: true })).toBeDisabled()
  expect(creates(api)).toHaveLength(0)
  api.setPhase('idle')
  await expect(form.getByRole('button', { name: zhCN.assistant.input.submit, exact: true })).toBeEnabled()
  expect(creates(api)).toHaveLength(0)
  await page.getByLabel(zhCN.assistant.input.question, { exact: true }).fill('确定商品')
  const late = api.hold('/assistant/resolve')
  await form.getByRole('button', { name: zhCN.assistant.input.submit, exact: true }).click()
  await late.arrived
  api.setPhase('recovering')
  await waitBlocked(page, zhCN.sourceAccess.recovering)
  late.release()
  await expect(form.getByRole('alert')).toBeVisible()
  expect(creates(api)).toHaveLength(0)
  await expect(page.getByLabel(zhCN.assistant.input.question, { exact: true })).toHaveValue('确定商品')
})

test('backend late refusal preserves draft, edited message and report with no resend on unlock', async ({ page, syncApi: api }) => {
  await open(page)
  await expect(page.getByRole('button', { name: zhCN.assistant.card.switchBasis, exact: true })).toBeEnabled()
  api.rejectNext('/messages')
  await composer(page).fill('后端竞争拒绝草稿')
  await composer(page).press('Enter')
  await expect(page.getByText(zhCN.sourceAccess.syncing, { exact: true }).first()).toBeVisible()
  await expect(composer(page)).toHaveValue('后端竞争拒绝草稿')
  api.rejectNext('/assistant/runs')
  await page.getByRole('button', { name: zhCN.assistant.card.switchBasis, exact: true }).click()
  await expect(page.locator(`[data-run-id="${OLD_RUN}"] tfoot`)).toContainText('48')
  const sent = api.sourceRequests.filter(request => request.suffix === '/messages').length
  api.setPhase('syncing')
  await waitBlocked(page)
  api.setPhase('idle')
  await expect(page.getByRole('button', { name: zhCN.assistant.card.switchBasis, exact: true })).toBeEnabled()
  expect(api.sourceRequests.filter(request => request.suffix === '/messages')).toHaveLength(sent)
  await expect(composer(page)).toHaveValue('后端竞争拒绝草稿')
})

test('polls never overlap, visibility events pause and resume, online resumes and both tabs converge', async ({ page, context, syncApi: api }) => {
  const first = api.hold('/data-sync/status')
  await open(page)
  await first.arrived
  const calls = api.statusCalls
  await page.clock.install()
  await page.clock.runFor(6500)
  expect(api.statusCalls).toBe(calls)
  expect(api.maxStatusInFlight).toBe(1)
  first.release()
  await expect(page.getByRole('button', { name: zhCN.assistant.card.switchBasis, exact: true })).toBeEnabled()
  const other = await context.newPage()
  await open(other, ERP_B)
  await other.bringToFront()
  // Headless Chromium keeps both pages visible; drive the browser event explicitly.
  await page.evaluate(() => {
    Object.defineProperty(document, 'visibilityState', { configurable: true, value: 'hidden' })
    document.dispatchEvent(new Event('visibilitychange'))
  })
  const countA = api.requests.filter(request => request.path === `/chats/${ERP}/data-sync/status`).length
  await page.clock.runFor(6000)
  expect(api.requests.filter(request => request.path === `/chats/${ERP}/data-sync/status`)).toHaveLength(countA)
  api.setPhase('syncing')
  await waitBlocked(other)
  await page.bringToFront()
  await page.evaluate(() => {
    Object.defineProperty(document, 'visibilityState', { configurable: true, value: 'visible' })
    document.dispatchEvent(new Event('visibilitychange'))
  })
  await waitBlocked(page)
  await context.setOffline(true)
  await expect(page.getByText(zhCN.sourceAccess.unavailable, { exact: true }).first()).toBeVisible()
  await composer(page).fill('离线草稿')
  await context.setOffline(false)
  await waitBlocked(page)
  await expect(composer(page)).toHaveValue('离线草稿')
  await other.close()
})

test('late Chat/owner metadata cannot authorize a different target; independent stays available', async ({ page, syncApi: api }) => {
  const late = api.hold('/data-sync/status')
  await open(page)
  await late.arrived
  await select(page, PG)
  late.release()
  await expect(composer(page)).toBeEnabled()
  await composer(page).fill('独立连接问题')
  await composer(page).press('Enter')
  await expect(composer(page)).toHaveValue('')
  expect(api.sourceRequests.some(request => request.chatId === PG && request.suffix === '/messages')).toBe(true)
  api.setOwner('isolated-owner-b')
  api.setPhase('recovering')
  await page.reload()
  await expect(composer(page)).toBeEnabled()
  await select(page, ERP)
  await waitBlocked(page, zhCN.sourceAccess.recovering)
  expect(api.sourceRequests.some(request => request.chatId === ERP && request.suffix === '/messages')).toBe(false)
})

for (const phase of ['syncing', 'recovering', 'outage'] as const) {
  test(`create/connect/question an unrelated PG during ERP ${phase} without extra approval`, async ({ page, syncApi: api }) => {
    api.setPhase(phase)
    await open(page)
    await waitBlocked(page, phase === 'syncing' ? zhCN.sourceAccess.syncing : phase === 'recovering' ? zhCN.sourceAccess.recovering : zhCN.sourceAccess.unavailable)
    await page.getByRole('button', { name: zhCN.sidebar.addConnection, exact: true }).first().click()
    const form = page.locator('form').filter({ has: page.getByRole('button', { name: zhCN.common.create, exact: true }) })
    for (const [name, value] of Object.entries({ host: 'new-independent.invalid', port: '5432', database: 'isolated_new_pg', username: 'isolated-reader' })) {
      await page.locator(`input[name="${name}"]`).fill(value)
    }
    await form.getByRole('button', { name: zhCN.common.create, exact: true }).click()
    await expect.poll(() => Array.from(api.chats.keys()).find(id => id.startsWith('independent-'))).toBeTruthy()
    const created = Array.from(api.chats.values()).find(chat => chat.id.startsWith('independent-'))!
    await expect(page.getByRole('button', { name: zhCN.connection.saveKnowledge, exact: true })).toBeEnabled()
    await page.getByRole('button', { name: zhCN.common.close, exact: true }).last().click()
    await page.getByRole('button', { name: new RegExp(created.display_name!) }).first().click()
    await expect(page).toHaveURL(new RegExp(`/chat/${created.id}$`))
    await expect(composer(page)).toBeEnabled()
    await composer(page).fill('新独立连接问数')
    await composer(page).press('Enter')
    await expect(composer(page)).toHaveValue('')
    expect(api.requests.filter(request => request.method === 'POST' && request.path === '/chats')).toHaveLength(1)
    expect(api.requests.some(request => request.path === `/chats/${created.id}/data-sync/status`)).toBe(false)
    expect(api.sourceRequests.some(request => request.chatId === created.id && request.suffix === '/messages')).toBe(true)
    expect(api.sourceRequests.some(request => request.chatId === created.id && request.suffix === '/connect')).toBe(true)
  })
}

test('R1 completed resolve cannot cross idle r10 to idle r13 without an explicit resubmit', async ({ page, syncApi: api }) => {
  api.advanceIdleRevision(10)
  api.setDashboardRefreshInterval(0)
  await open(page)
  await expect(page.getByRole('button', { name: zhCN.assistant.card.switchBasis, exact: true })).toBeEnabled()
  await choose(page)
  const form = page.getByRole('form', { name: zhCN.assistant.input.title })
  const question = page.getByLabel(zhCN.assistant.input.question, { exact: true })
  await question.fill('跨过完整同步但未直接观测忙态的原始输入')
  const late = api.holdResponse('/assistant/resolve')
  const delivered = page.waitForResponse(response => response.request().method() === 'POST' && new URL(response.url()).pathname.endsWith(`/chats/${ERP}/assistant/resolve`))
  await form.getByRole('button', { name: zhCN.assistant.input.submit, exact: true }).click()
  await late.arrived
  expect(api.resolveReads).toEqual([{ chatId: ERP, sourceRevision: 10, bindingRevision: 1, sourceId: api.chats.get(ERP)!.source_id }])
  expect(creates(api)).toHaveLength(0)

  const advanced = page.waitForResponse(async response => isStatus(response) && (await response.json()).data.source_revision === 13)
  api.advanceIdleRevision(13)
  await (await advanced).finished()
  await nextDiscoveryCycle(page)
  expect(api.statusResponses.map(response => response.source_revision)).toContain(13)
  expect(api.statusResponses.every(response => response.maintenance_state === 'idle' && response.ask_allowed)).toBe(true)
  late.release()
  await (await delivered).finished()
  // Observe either terminal UI outcome so a buggy accepted run fails at the request count, not a missing form.
  await expect(form.locator('button[type="submit"]:enabled').or(page.locator(`[data-run-id]:not([data-run-id="${OLD_RUN}"])`)).first()).toBeVisible()
  expect(creates(api), 'R1: a completed r10 resolve must not automatically create under r13').toHaveLength(0)
  await expect(question).toHaveValue('跨过完整同步但未直接观测忙态的原始输入')
  await expect(page.locator(`[data-run-id="${OLD_RUN}"] tfoot`)).toContainText('48')

  await form.getByRole('button', { name: zhCN.assistant.input.submit, exact: true }).click()
  await expect.poll(() => creates(api).length).toBe(1)
  expect(api.resolveReads.map(read => read.sourceRevision)).toEqual([10, 13])
  await expect(page.locator(`[data-run-id="${OLD_RUN}"] tfoot`)).toContainText('48')
})

for (const invalidation of ['syncing-idle', 'binding-change'] as const) {
  for (const lateKind of ['error', 'data'] as const) {
    test(`R2 ${lateKind} after ${invalidation} cannot adopt the current widget operation`, async ({ page, syncApi: api }) => {
      api.setDashboardRefreshInterval(0)
      await open(page)
      await composer(page).fill('换代后也保留的看板用户草稿')
      await openDashboard(page)
      const observed = await observeWidgetEvents(page)
      const late = api.holdWidgetEvent(lateKind, -101)
      const accepted = page.waitForResponse(response => response.request().method() === 'POST' && new URL(response.url()).pathname.endsWith(`/dashboards/dash-${ERP}/widgets/widget-${ERP}/refresh`))
      await page.getByTitle(zhCN.dashboard.refreshWidget, { exact: true }).click()
      await late.arrived
      expect((await accepted).ok()).toBe(true)
      expect(api.widgetRequests).toHaveLength(1)
      expect(api.widgetRequests[0]).toMatchObject({ chatId: ERP, dashboardId: `dash-${ERP}`, widgetId: `widget-${ERP}`, bindingRevision: 1, event: lateKind,
        result: lateKind === 'error' ? { error: 'no connection found', data: null } : { data: [{ margin: -101 }] } })
      expect(api.widgetRequests[0].streamId).not.toBe('')
      expect(observed).toEqual([])
      await page.getByTitle(zhCN.dashboard.cancelLoading, { exact: true }).click()

      if (invalidation === 'syncing-idle') {
        api.setPhase('syncing')
        await expect(page.getByText(zhCN.sourceAccess.syncing, { exact: true }).first()).toBeVisible()
        api.setPhase('idle')
        await expect(page.getByTitle(zhCN.dashboard.refreshNow, { exact: true })).toBeEnabled()
      } else {
        await rebindCurrentChat(page, api)
      }
      await expect(widgetCard(page).getByText('48', { exact: true })).toBeVisible()
      await expect(composer(page)).toHaveValue('换代后也保留的看板用户草稿')

      await refreshWidgetWith(page, api, 84)
      expect(api.widgetRequests[1]).toMatchObject({ chatId: ERP, dashboardId: `dash-${ERP}`, widgetId: `widget-${ERP}`,
        bindingRevision: invalidation === 'binding-change' ? 2 : 1, result: { data: [{ margin: 84 }] } })
      // Keep a newer valid request pending: overwriting a per-widget target must not attribute the old SSE to it.
      const current = api.holdWidgetEvent('data', 96)
      await page.getByTitle(zhCN.dashboard.refreshWidget, { exact: true }).click()
      await current.arrived
      const before = recoveryRequests(api)
      late.release()
      const event = lateKind === 'error' ? DASHBOARD_SSE_EVENTS.WIDGET_ERROR : DASHBOARD_SSE_EVENTS.WIDGET_DATA
      await expect.poll(() => observed.filter(item => item.event === event && (lateKind === 'error' ? item.data.error === 'no connection found' : item.data.data?.[0]?.margin === -101)).length).toBeGreaterThan(0)
      await nextDiscoveryCycle(page)
      expect(recoveryRequests(api), 'R2: a delivered old SSE must not probe/connect/refresh with a new permission').toEqual(before)
      await expect(widgetCard(page).getByText('84', { exact: true })).toBeVisible()
      await expect(widgetCard(page).getByText('-101', { exact: true })).toHaveCount(0)
      await expect(page.getByTitle(zhCN.dashboard.cancelLoading, { exact: true })).toBeVisible()
      await expect(composer(page)).toHaveValue('换代后也保留的看板用户草稿')

      current.release()
      await expect(widgetCard(page).getByText('96', { exact: true })).toBeVisible()
      await expect(page.getByTitle(zhCN.dashboard.refreshWidget, { exact: true })).toBeEnabled()
      await refreshWidgetWith(page, api, 108)
      expect(api.widgetRequests).toHaveLength(4)
      expect(api.widgetRequests[3]).toMatchObject({ bindingRevision: invalidation === 'binding-change' ? 2 : 1, result: { data: [{ margin: 108 }] } })
      await expect(composer(page)).toHaveValue('换代后也保留的看板用户草稿')
    })
  }
}

test('R3 same Chat binding change cannot create from the old visible blueprints', async ({ page, syncApi: api }) => {
  api.setDashboardRefreshInterval(0)
  await open(page)
  await composer(page).fill('生成建议换绑后仍保留的经营提示草稿')
  await openDashboard(page)
  await generateBlueprints(page, '绑定 A 的收入与利润建议', 'A')
  expect(api.blueprintReads).toEqual([{ chatId: ERP, sourceId: api.chats.get(ERP)!.source_id, bindingRevision: 1, prompt: '绑定 A 的收入与利润建议' }])
  const picker = blueprintPicker(page)
  await picker.getByRole('button').filter({ has: page.getByRole('heading', { name: 'isolated A 方案 1', exact: true }) }).click()
  await expect(picker.getByRole('button', { name: zhCN.dashboard.createN, exact: true })).toBeEnabled()
  expect(dashboardCreates(api)).toHaveLength(0)

  await rebindCurrentChat(page, api)
  // Both invalidating the picker and rejecting an explicit stale click are valid; it was proven visible above.
  if (await picker.isVisible()) await picker.getByRole('button', { name: zhCN.dashboard.createN, exact: true }).click()
  await nextDiscoveryCycle(page)
  expect(dashboardCreates(api), 'R3: the visible A selection must not borrow B permission').toHaveLength(0)
  await expect(widgetCard(page).getByText('48', { exact: true })).toBeVisible()
  await expect(composer(page)).toHaveValue('生成建议换绑后仍保留的经营提示草稿')
  if (await picker.isVisible()) await picker.getByRole('button', { name: zhCN.common.cancel, exact: true }).click()

  await generateBlueprints(page, '绑定 B 的当前收入建议', 'B')
  await picker.getByRole('button').filter({ has: page.getByRole('heading', { name: 'isolated B 方案 0', exact: true }) }).click()
  await picker.getByRole('button', { name: zhCN.dashboard.createN, exact: true }).click()
  await expect.poll(() => dashboardCreates(api).length).toBe(1)
  expect(dashboardCreates(api)[0].body).toEqual({ blueprint_indices: [1], blueprint_origin: expect.any(String) })
  expect(api.blueprintCreates).toEqual([{ chatId: ERP, bindingRevision: 2, cachedBindingRevision: 2, indices: [1] }])
  expect(api.blueprintReads[1]).toMatchObject({ bindingRevision: 2, prompt: '绑定 B 的当前收入建议' })
  await expect(page.locator('button.dashboard-selector-btn')).toHaveText('isolated B 方案 1')
  await expect(composer(page)).toHaveValue('生成建议换绑后仍保留的经营提示草稿')
})

const currentPlans = (): DashboardBlueprint[] => [0, 1].map(index => ({ index, name: `isolated 当前方案 ${index}`, description: `当前批次的收入方案 ${index}`,
  template_type: 'sales', proposed_widgets: [{ title: `当前收入 ${index}`, widget_type: 'stat' }] }))

test('R11 a cancelled late blueprint event cannot replace a newly displayed batch or its selection', async ({ page, syncApi: api }) => {
  api.setDashboardRefreshInterval(0)
  await open(page)
  await composer(page).fill('旧建议晚到时也保留的用户提示')
  await openDashboard(page)
  const late = api.holdBlueprintEvent()
  await page.locator('button.dashboard-selector-btn').click()
  await page.getByRole('button', { name: zhCN.dashboard.new, exact: true }).click()
  await page.getByPlaceholder(zhCN.dashboard.insightsPlaceholder).fill('第一批已完成但事件晚到的建议')
  await page.getByRole('button', { name: zhCN.dashboard.generate, exact: true }).click()
  await late.arrived
  await expect(page.getByPlaceholder(zhCN.dashboard.insightsPlaceholder)).toHaveCount(0)
  await page.getByRole('button', { name: zhCN.common.cancel, exact: true }).click()

  const plans = currentPlans()
  api.queueBlueprints(plans)
  await generateBlueprints(page, '用户明确生成当前批次', 'A', plans.map(plan => plan.name))
  await blueprintPicker(page).getByRole('button').filter({ has: page.getByRole('heading', { name: plans[0].name, exact: true }) }).click()
  // Observe the real forwarded old event; no test dispatches a dashboard event.
  const seen = page.evaluate(() => new Promise<void>(resolve => {
    window.addEventListener('dashboard-blueprints', () => resolve(), { once: true })
  }))
  await page.evaluate(() => undefined)
  late.release()
  await seen
  await nextDiscoveryCycle(page)
  await expect(blueprintPicker(page).getByRole('heading', { name: plans[1].name, exact: true })).toBeVisible()
  await expect(blueprintPicker(page).getByRole('heading', { name: 'isolated A 方案 0', exact: true })).toHaveCount(0)
  await expect(widgetCard(page).getByText('48', { exact: true })).toBeVisible()
  await blueprintPicker(page).getByRole('button', { name: zhCN.dashboard.createN, exact: true }).click()
  await expect(page.locator('button.dashboard-selector-btn')).toHaveText(plans[1].name)
  expect(dashboardCreates(api)).toHaveLength(1)
  expect(dashboardCreates(api)[0].body).toEqual({ blueprint_indices: [1], blueprint_origin: expect.any(String) })
  await expect(composer(page)).toHaveValue('旧建议晚到时也保留的用户提示')
})

test('R11 multiple completes and child data survive HTTP while old cleanup cannot delete the current batch', async ({ page, syncApi: api }) => {
  api.setDashboardRefreshInterval(0)
  await open(page)
  await composer(page).fill('多选创建与新建议并存时保留的草稿')
  await openDashboard(page)
  await generateBlueprints(page, '第一批多选方案', 'A')
  const first = api.holdAfterFirstDashboard()
  const children = api.holdGenerationData()
  const cleanup = api.holdCreateCleanup()
  const completed = page.waitForResponse(response => response.request().method() === 'POST' && new URL(response.url()).pathname.endsWith(`/chats/${ERP}/dashboards/create-from-blueprints`))
  await blueprintPicker(page).getByRole('button', { name: `${zhCN.dashboard.createN} (2)`, exact: true }).click()
  await first.arrived
  await expect(page.locator('button.dashboard-selector-btn')).toHaveText('isolated A 方案 0')
  await expect(widgetCard(page).getByText('200', { exact: true })).toBeVisible()
  first.release()
  await cleanup.arrived
  await children.arrived
  await expect(page.locator('button.dashboard-selector-btn')).toHaveText('isolated A 方案 1')
  await blueprintPicker(page).getByRole('button', { name: zhCN.common.cancel, exact: true }).click()

  const plans = currentPlans()
  api.queueBlueprints(plans)
  await generateBlueprints(page, '第一批创建未返回时生成的当前建议', 'A', plans.map(plan => plan.name))
  cleanup.release()
  expect((await completed).ok()).toBe(true)
  await nextDiscoveryCycle(page)
  await expect(blueprintPicker(page).getByRole('heading', { name: plans[1].name, exact: true })).toBeVisible()
  children.release()
  await expect(widgetCard(page).getByText('201', { exact: true })).toBeVisible()
  await blueprintPicker(page).getByRole('button').filter({ has: page.getByRole('heading', { name: plans[0].name, exact: true }) }).click()
  await blueprintPicker(page).getByRole('button', { name: zhCN.dashboard.createN, exact: true }).click()
  await expect(page.locator('button.dashboard-selector-btn')).toHaveText(plans[1].name)
  expect(dashboardCreates(api).map(request => request.body)).toEqual([
    { blueprint_indices: [0, 1], blueprint_origin: expect.any(String) },
    { blueprint_indices: [1], blueprint_origin: expect.any(String) },
  ])
  await expect(composer(page)).toHaveValue('多选创建与新建议并存时保留的草稿')
  await expect(page.locator(`[data-run-id="${OLD_RUN}"] tfoot`)).toContainText('48')
})

const cursorCard = (page: Page) => page.locator('div[tabindex="-1"]').filter({ has: page.getByRole('heading', { name: '游标明细', exact: true }) })
const cursorFooter = (page: Page) => cursorCard(page).locator('div.sticky.bottom-0')
const cursorNext = (page: Page) => cursorCard(page).getByRole('button', { name: zhCN.common.next, exact: true })
const cursorPrevious = (page: Page) => cursorCard(page).getByRole('button', { name: zhCN.common.previous, exact: true })
const generationOverlay = (page: Page) => page.locator('div.fixed').filter({ has: page.getByRole('heading', { name: zhCN.dashboard.preparingSelected, exact: true }) })
const widgetRefreshPath = `/dashboards/dash-${ERP}/widgets/widget-${ERP}/refresh`

async function expectCursorRows(page: Page, version: string, ids: number[]) {
  await expect(cursorCard(page).getByRole('cell').filter({ hasText: /^cursor-/ })).toHaveText(ids.map(id => `cursor-${version}-row-${id}`))
}

async function expectCursorPage(page: Page, number: number) {
  await expect(cursorFooter(page)).toContainText(new RegExp(`${zhCN.dates.page}\\s*${number}，`))
}

async function refreshCursor(page: Page, api: SyncApi, version: string) {
  const reply = api.holdWidgetEvent('data')
  await cursorCard(page).getByTitle(zhCN.dashboard.refreshWidget, { exact: true }).click()
  await reply.arrived
  expect(api.cursorRequests.at(-1)?.cursor).toBeNull()
  reply.release()
  await expectCursorRows(page, version, [1, 2])
  await expect(cursorCard(page).getByTitle(zhCN.dashboard.refreshWidget, { exact: true })).toBeEnabled()
}

async function openCursorDashboard(page: Page, api: SyncApi) {
  api.setDashboardRefreshInterval(0)
  api.setCursorTable('before')
  await open(page)
  await page.getByRole('button', { name: zhCN.dashboard.dashboard, exact: true }).click()
  await expect(cursorCard(page)).toBeVisible()
  await expect(cursorCard(page).getByTitle(zhCN.dashboard.refreshWidget, { exact: true })).toBeEnabled()
  await refreshCursor(page, api, 'before')
  await expect(cursorPrevious(page)).toBeDisabled()
  await expect(cursorNext(page)).toBeEnabled()
}

async function cacheThirdCursorPage(page: Page, api: SyncApi) {
  await cursorNext(page).click()
  await expectCursorRows(page, 'before', [3, 4])
  await expectCursorPage(page, 2)
  await cursorNext(page).click()
  await expectCursorRows(page, 'before', [5])
  await expectCursorPage(page, 3)
  await expect(cursorNext(page)).toBeDisabled()
  expect(api.cursorRequests.map(request => request.cursor)).toEqual([null, '2', '4'])
  await cursorPrevious(page).click()
  await expectCursorRows(page, 'before', [3, 4])
  await expectCursorPage(page, 2)
}

async function observeDashboardCompletes(page: Page) {
  const events: DashboardGenerationCompleteEvent[] = []
  await page.exposeFunction('recordT41DashboardComplete', (data: DashboardGenerationCompleteEvent) => { events.push(data) })
  await page.evaluate(() => {
    window.addEventListener('dashboard-generation-complete', event => { void window.recordT41DashboardComplete((event as CustomEvent<DashboardGenerationCompleteEvent>).detail) })
  })
  return events
}

for (const error of ['sse', 'connection'] as const) {
  test(`R2 F1 cancelled whole-board ${error} HTTP error cannot reconnect or steal pending B`, async ({ page, syncApi: api }) => {
    api.setDashboardRefreshInterval(0)
    await open(page)
    await composer(page).fill('取消旧整板请求后保留的草稿')
    await openDashboard(page)
    const seen = await observeWidgetEvents(page)
    const a = api.holdDashboardRefreshError(error)
    await page.getByTitle(zhCN.dashboard.refreshNow, { exact: true }).click()
    await a.arrived
    const aRequest = api.requests.filter(request => request.method === 'POST' && request.path === `/chats/${ERP}/dashboards/dash-${ERP}/refresh`).at(-1)!
    await page.getByTitle(zhCN.dashboard.cancelRefresh, { exact: true }).click()
    await expect(page.getByTitle(zhCN.dashboard.refreshNow, { exact: true })).toBeEnabled()

    const bHTTP = api.holdResponse(`/dashboards/dash-${ERP}/refresh`)
    const bData = api.holdWidgetEvent('data', 96)
    await page.getByTitle(zhCN.dashboard.refreshNow, { exact: true }).click()
    await bHTTP.arrived
    const bRequest = api.requests.filter(request => request.method === 'POST' && request.path === `/chats/${ERP}/dashboards/dash-${ERP}/refresh`).at(-1)!
    expect(bRequest.operationId).not.toBe(aRequest.operationId)
    const cancelBoard = page.getByTitle(zhCN.dashboard.cancelRefresh, { exact: true })
    await expect(cancelBoard).toBeVisible()
    const before = recoveryRequests(api)
    const oldError = page.waitForResponse(response => response.request().method() === 'POST'
      && new URL(response.url()).pathname.endsWith(`/chats/${ERP}/dashboards/dash-${ERP}/refresh`)
      && new URL(response.url()).searchParams.get('operation_id') === aRequest.operationId)
    a.release()
    const failed = await oldError
    expect(failed.status()).toBe(400)
    expect(await failed.json()).toEqual({ success: false, error: error === 'sse'
      ? 'SSE stream not found - please ensure you are connected before refreshing'
      : 'database connection not found for this chat - please connect to database first' })
    await failed.finished()
    await nextDiscoveryCycle(page)

    // Same source epoch throughout: only cancellation/ownership can retire A's continuation.
    expect(api.statusResponses.every(status => status.source_revision === 1 && status.binding_revision === 1 && status.ask_allowed)).toBe(true)
    expect.soft(recoveryRequests(api), 'cancelled A must not start probe/connect/refresh C').toEqual(before)
    await expect.soft(cancelBoard, 'B remains pending while its HTTP and data are both held').toBeVisible()
    const bCompleted = page.waitForResponse(response => response.request().method() === 'POST'
      && new URL(response.url()).pathname.endsWith(`/chats/${ERP}/dashboards/dash-${ERP}/refresh`)
      && new URL(response.url()).searchParams.get('operation_id') === bRequest.operationId)
    bHTTP.release()
    const accepted = await bCompleted
    expect(accepted.status()).toBe(202)
    await accepted.finished()
    await bData.arrived
    bData.release()
    await expect.poll(() => seen.filter(entry => entry.data.operation_id === bRequest.operationId).map(entry => entry.data.data?.[0]?.margin)).toContain(96)
    await expect(widgetCard(page).getByText('96', { exact: true })).toBeVisible()
    await expect(composer(page)).toHaveValue('取消旧整板请求后保留的草稿')
    await expect(page.locator(`[data-run-id="${OLD_RUN}"] tfoot`)).toContainText('48')
  })
}

test('R2 F2 manual cursor refresh resets page two to page one and restarts traversal', async ({ page, syncApi: api }) => {
  await openCursorDashboard(page, api)
  await cursorNext(page).click()
  await expectCursorRows(page, 'before', [3, 4])
  await expectCursorPage(page, 2)
  expect(api.cursorRequests.map(request => request.cursor)).toEqual([null, '2'])
  api.setCursorTable('fresh')
  await refreshCursor(page, api, 'fresh')

  await expectCursorPage(page, 1)
  await expect(cursorPrevious(page)).toBeDisabled()
  await expect(cursorNext(page)).toBeEnabled()
  await cursorNext(page).click()
  await expectCursorRows(page, 'fresh', [3, 4])
  await expectCursorPage(page, 2)
  expect(api.cursorRequests.map(request => request.cursor)).toEqual([null, '2', null, '2'])
  await cursorPrevious(page).click()
  await expectCursorRows(page, 'fresh', [1, 2])
  await expectCursorPage(page, 1)
  await expect(cursorPrevious(page)).toBeDisabled()
  expect(api.cursorRequests.map(request => request.cursor)).toEqual([null, '2', null, '2'])
})

test('R2 F2 manual cursor refresh cannot reuse cached old page three as the new next page', async ({ page, syncApi: api }) => {
  await openCursorDashboard(page, api)
  await cacheThirdCursorPage(page, api)
  api.setCursorTable('fresh')
  await refreshCursor(page, api, 'fresh')
  await cursorNext(page).click()

  // The old cache hit shows before-row-5 here; the valid next page must fetch cursor=2.
  await expectCursorRows(page, 'fresh', [3, 4])
  await expectCursorPage(page, 2)
  expect(api.cursorRequests.map(request => request.cursor)).toEqual([null, '2', '4', null, '2'])
  await cursorNext(page).click()
  await expectCursorRows(page, 'fresh', [5])
  await expectCursorPage(page, 3)
  await expect(cursorNext(page)).toBeDisabled()
  await cursorPrevious(page).click()
  await expectCursorRows(page, 'fresh', [3, 4])
  await cursorPrevious(page).click()
  await expectCursorRows(page, 'fresh', [1, 2])
  await expectCursorPage(page, 1)
  await expect(cursorPrevious(page)).toBeDisabled()
  expect(api.cursorRequests.map(request => request.cursor)).toEqual([null, '2', '4', null, '2', '4'])
})

test('R2 F2 source refusal preserves the current cursor page and cached navigation without IO', async ({ page, syncApi: api }) => {
  await openCursorDashboard(page, api)
  await cacheThirdCursorPage(page, api)
  api.setCursorTable('fresh')
  api.setPhase('syncing')
  await expect(cursorCard(page).getByTitle(zhCN.sourceAccess.syncing, { exact: true })).toBeDisabled()
  const before = recoveryRequests(api)
  await expectCursorRows(page, 'before', [3, 4])
  await expectCursorPage(page, 2)
  // A local cached page is a retained snapshot and stays usable during the source refusal.
  await cursorNext(page).click()
  await expectCursorRows(page, 'before', [5])
  await expectCursorPage(page, 3)
  await cursorPrevious(page).click()
  await expectCursorRows(page, 'before', [3, 4])
  await expectCursorPage(page, 2)
  await nextDiscoveryCycle(page)
  expect(recoveryRequests(api)).toEqual(before)
  expect(api.cursorRequests.map(request => request.cursor)).toEqual([null, '2', '4'])
  api.setPhase('idle')
  await expect(cursorCard(page).getByTitle(zhCN.dashboard.refreshWidget, { exact: true })).toBeEnabled()
  await expectCursorRows(page, 'before', [3, 4])
  await expectCursorPage(page, 2)
})

test('R2 F2 backend-denied manual refresh keeps page two and its cached third page', async ({ page, syncApi: api }) => {
  await openCursorDashboard(page, api)
  await cacheThirdCursorPage(page, api)
  api.rejectNext(widgetRefreshPath)
  const before = recoveryRequests(api)
  const rejected = page.waitForResponse(response => response.request().method() === 'POST' && new URL(response.url()).pathname.endsWith(`/chats/${ERP}${widgetRefreshPath}`))
  await cursorCard(page).getByTitle(zhCN.dashboard.refreshWidget, { exact: true }).click()
  const denied = await rejected
  expect(denied.status()).toBe(423)
  await denied.finished()
  await expect(cursorCard(page).getByTitle(zhCN.dashboard.refreshWidget, { exact: true })).toBeEnabled()
  await expectCursorRows(page, 'before', [3, 4])
  await expectCursorPage(page, 2)
  await cursorNext(page).click()
  await expectCursorRows(page, 'before', [5])
  await expectCursorPage(page, 3)
  await cursorPrevious(page).click()
  await expectCursorRows(page, 'before', [3, 4])
  await expectCursorPage(page, 2)
  expect(recoveryRequests(api)).toEqual({ ...before, widget: before.widget + 1 })
  expect(api.cursorRequests.map(request => request.cursor)).toEqual([null, '2', '4'])
})

for (const failure of ['model-error', 'parse-error'] as const) {
  test(`R2 F3 partial ${failure} exits generation while successful HTTP-late widget data remains valid`, async ({ page, syncApi: api }) => {
    api.setDashboardRefreshInterval(0)
    await open(page)
    await composer(page).fill('部分创建失败后也保留的草稿')
    await openDashboard(page)
    await generateBlueprints(page, '允许部分失败的双蓝图创建', 'A')
    const completes = await observeDashboardCompletes(page)
    const seen = await observeWidgetEvents(page)
    api.queueGenerationOutcomes([failure, 'success'])
    const children = api.holdGenerationData()
    const completed = page.waitForResponse(response => response.request().method() === 'POST' && new URL(response.url()).pathname.endsWith(`/chats/${ERP}/dashboards/create-from-blueprints`))
    await blueprintPicker(page).getByRole('button', { name: `${zhCN.dashboard.createN} (2)`, exact: true }).click()
    await children.arrived
    const response = await completed
    expect(response.status()).toBe(200)
    await response.finished()
    const create = dashboardCreates(api).at(-1)!
    expect(create.body).toEqual({ blueprint_indices: [0, 1], blueprint_origin: expect.any(String) })
    await expect.poll(() => completes.filter(event => event.operation_id === create.operationId).map(event => event.dashboard.name)).toEqual(['isolated A 方案 1'])
    expect(api.generationAttempts.filter(attempt => attempt.operationId === create.operationId).map(attempt => ({ index: attempt.index, outcome: attempt.outcome }))).toEqual([
      { index: 0, outcome: failure }, { index: 1, outcome: 'success' },
    ])
    await expect(page.locator('button.dashboard-selector-btn')).toHaveText('isolated A 方案 1')
    await expect(blueprintPicker(page)).toHaveCount(0)
    children.release()
    await expect.poll(() => seen.filter(entry => entry.data.operation_id === create.operationId).map(entry => entry.data.data?.[0]?.margin)).toContain(201)
    await expect(widgetCard(page).getByText('201', { exact: true })).toBeVisible()
    await expect(composer(page)).toHaveValue('部分创建失败后也保留的草稿')
    await expect(page.locator(`[data-run-id="${OLD_RUN}"] tfoot`)).toContainText('48')
    // HTTP is finished and the only successful dashboard and its child are present; no complete exists for index 0.
    await expect(generationOverlay(page)).toHaveCount(0)
    await page.locator('button.dashboard-selector-btn').click()
    await page.getByRole('button', { name: zhCN.dashboard.new, exact: true }).click()
    await expect(page.getByPlaceholder(zhCN.dashboard.insightsPlaceholder)).toBeVisible()
  })
}

test('R2 F3 all ordinary generation failures exit the overlay and leave the old dashboard usable', async ({ page, syncApi: api }) => {
  api.setDashboardRefreshInterval(0)
  await open(page)
  await composer(page).fill('全部创建失败后保留的草稿')
  await openDashboard(page)
  await generateBlueprints(page, '全部普通模型失败的双蓝图创建', 'A')
  const completes = await observeDashboardCompletes(page)
  api.queueGenerationOutcomes(['model-error', 'model-error'])
  const completed = page.waitForResponse(response => response.request().method() === 'POST' && new URL(response.url()).pathname.endsWith(`/chats/${ERP}/dashboards/create-from-blueprints`))
  await blueprintPicker(page).getByRole('button', { name: `${zhCN.dashboard.createN} (2)`, exact: true }).click()
  const response = await completed
  expect(response.status()).toBe(200)
  await response.finished()
  await expect(blueprintPicker(page)).toHaveCount(0)
  const create = dashboardCreates(api).at(-1)!
  expect(create.body).toEqual({ blueprint_indices: [0, 1], blueprint_origin: expect.any(String) })
  expect(api.generationAttempts.filter(attempt => attempt.operationId === create.operationId).map(attempt => ({ index: attempt.index, outcome: attempt.outcome }))).toEqual([
    { index: 0, outcome: 'model-error' }, { index: 1, outcome: 'model-error' },
  ])
  expect(completes.filter(event => event.operation_id === create.operationId)).toEqual([])
  await expect(page.locator('button.dashboard-selector-btn')).toHaveText('isolated 快照看板')
  await expect(widgetCard(page).getByText('48', { exact: true })).toBeVisible()
  await expect(composer(page)).toHaveValue('全部创建失败后保留的草稿')
  await expect(page.locator(`[data-run-id="${OLD_RUN}"] tfoot`)).toContainText('48')
  await expect(generationOverlay(page)).toHaveCount(0)
  await refreshWidgetWith(page, api, 108)
  await expect(page.locator('button.dashboard-selector-btn')).toHaveText('isolated 快照看板')
})

async function observeBatchResults(page: Page) {
  const events: DashboardGenerationProgressEvent[] = []
  await page.exposeFunction('recordT41BatchResult', (data: DashboardGenerationProgressEvent) => { events.push(data) })
  await page.evaluate(() => {
    window.addEventListener('dashboard-generation-progress', event => {
      const data = (event as CustomEvent<DashboardGenerationProgressEvent>).detail
      if (data.batch_result) void window.recordT41BatchResult(data)
    })
  })
  return events
}

test('R2 F1 current whole-board retry preserves valid reconnect and data', async ({ page, syncApi: api }) => {
  api.setDashboardRefreshInterval(0)
  await open(page)
  await openDashboard(page)
  const before = recoveryRequests(api)
  const error = api.holdDashboardRefreshError('sse')
  const data = api.holdWidgetEvent('data', 108)
  await page.getByTitle(zhCN.dashboard.refreshNow, { exact: true }).click()
  await error.arrived
  error.release()
  await data.arrived
  expect(recoveryRequests(api)).toEqual({ ...before, probe: before.probe + 1, dashboard: before.dashboard + 2 })
  data.release()
  await expect(widgetCard(page).getByText('108', { exact: true })).toBeVisible()
  await expect(page.getByTitle(zhCN.dashboard.refreshNow, { exact: true })).toBeEnabled()
})

test('R2 F1 cancellation during reconnect suppresses the pending retry', async ({ page, syncApi: api }) => {
  api.setDashboardRefreshInterval(0)
  await open(page)
  await openDashboard(page)
  const error = api.holdDashboardRefreshError('connection')
  const probe = api.hold('/connection-status')
  await page.getByTitle(zhCN.dashboard.refreshNow, { exact: true }).click()
  await error.arrived
  error.release()
  await probe.arrived
  const before = recoveryRequests(api)
  await page.getByTitle(zhCN.dashboard.cancelRefresh, { exact: true }).click()
  await expect(page.getByTitle(zhCN.dashboard.refreshNow, { exact: true })).toBeEnabled()
  probe.release()
  await nextDiscoveryCycle(page)
  // This probe was already received before cancellation; no subsequent source request is allowed.
  expect(recoveryRequests(api)).toEqual({ ...before, probe: before.probe + 1 })
  await expect(widgetCard(page).getByText('48', { exact: true })).toBeVisible()
  await expect(page.getByTitle(zhCN.dashboard.refreshNow, { exact: true })).toBeEnabled()
})

test('R2 F2 cancelling a first-page refresh preserves page two and its cached third page', async ({ page, syncApi: api }) => {
  await openCursorDashboard(page, api)
  await cacheThirdCursorPage(page, api)
  const seen = await observeWidgetEvents(page)
  api.setCursorTable('fresh')
  const late = api.holdWidgetEvent('data')
  const completed = page.waitForResponse(response => response.request().method() === 'POST' && new URL(response.url()).pathname.endsWith(`/chats/${ERP}${widgetRefreshPath}`))
  await cursorCard(page).getByTitle(zhCN.dashboard.refreshWidget, { exact: true }).click()
  await late.arrived
  const response = await completed
  await response.finished()
  const operation = api.cursorRequests.at(-1)!.operationId
  await expectCursorRows(page, 'before', [3, 4])
  await expectCursorPage(page, 2)
  await cursorCard(page).getByTitle(zhCN.dashboard.cancelLoading, { exact: true }).click()
  late.release()
  await expect.poll(() => seen.filter(entry => entry.data.operation_id === operation).map(entry => entry.data.data?.map(row => row.label))).toEqual([
    ['cursor-fresh-row-1', 'cursor-fresh-row-2'],
  ])
  await expectCursorRows(page, 'before', [3, 4])
  await expectCursorPage(page, 2)
  await cursorNext(page).click()
  await expectCursorRows(page, 'before', [5])
  await expectCursorPage(page, 3)
  await cursorPrevious(page).click()
  await expectCursorRows(page, 'before', [3, 4])
  await expectCursorPage(page, 2)
  expect(api.cursorRequests.map(request => request.cursor)).toEqual([null, '2', '4', null])
})

test('R2 F3 old batch settlement cannot clear a newer generation or drop its valid late child', async ({ page, syncApi: api }) => {
  api.setDashboardRefreshInterval(0)
  await open(page)
  await composer(page).fill('旧批次结算与新建议并存时保留的草稿')
  await openDashboard(page)
  const results = await observeBatchResults(page)
  await generateBlueprints(page, '将被延迟结算的旧创建', 'A')
  const settlement = api.holdGenerationSettlement()
  const children = api.holdGenerationData()
  const completed = page.waitForResponse(response => response.request().method() === 'POST' && new URL(response.url()).pathname.endsWith(`/chats/${ERP}/dashboards/create-from-blueprints`))
  await blueprintPicker(page).getByRole('button', { name: `${zhCN.dashboard.createN} (2)`, exact: true }).click()
  await settlement.arrived
  await children.arrived
  const response = await completed
  expect(response.status()).toBe(200)
  await response.finished()
  const old = dashboardCreates(api).at(-1)!
  await expect(generationOverlay(page)).toHaveCount(0)
  await expect(blueprintPicker(page)).toHaveCount(0)
  await expect(page.locator('button.dashboard-selector-btn')).toHaveText('isolated A 方案 1')

  const plans = currentPlans()
  api.queueBlueprints(plans)
  const suggestions = api.holdBlueprintEvent()
  await page.locator('button.dashboard-selector-btn').click()
  await page.getByRole('button', { name: zhCN.dashboard.new, exact: true }).click()
  await page.getByPlaceholder(zhCN.dashboard.insightsPlaceholder).fill('旧结算到来时仍在生成的当前建议')
  await page.getByRole('button', { name: zhCN.dashboard.generate, exact: true }).click()
  await suggestions.arrived
  const currentProgress = page.locator('div.fixed').filter({ has: page.getByRole('heading', { name: zhCN.dashboard.preparingCustom, exact: true }) })
  await expect(currentProgress).toBeVisible()
  settlement.release()
  await expect.poll(() => results.filter(event => event.operation_id === old.operationId).map(event => event.batch_result)).toEqual([{ succeeded: 2, failed: 0 }])
  await expect(currentProgress).toBeVisible()
  children.release()
  await expect(widgetCard(page).getByText('201', { exact: true })).toBeVisible()
  await expect(currentProgress).toBeVisible()
  suggestions.release()
  await expect(blueprintPicker(page).getByRole('heading', { name: plans[1].name, exact: true })).toBeVisible()
  await expect(currentProgress).toHaveCount(0)
  await blueprintPicker(page).getByRole('button').filter({ has: page.getByRole('heading', { name: plans[0].name, exact: true }) }).click()
  await blueprintPicker(page).getByRole('button', { name: zhCN.dashboard.createN, exact: true }).click()
  await expect(page.locator('button.dashboard-selector-btn')).toHaveText(plans[1].name)
  await expect(widgetCard(page).getByText('201', { exact: true })).toBeVisible()
  await expect(composer(page)).toHaveValue('旧批次结算与新建议并存时保留的草稿')
})

test('R2 F3 settled HTTP retains late completes without replacing a newer picker', async ({ page, syncApi: api }) => {
  api.setDashboardRefreshInterval(0)
  await open(page)
  await openDashboard(page)
  const results = await observeBatchResults(page)
  const completes = await observeDashboardCompletes(page)
  const seen = await observeWidgetEvents(page)
  await generateBlueprints(page, '完成事件晚于结算的旧创建', 'A')
  const complete = api.holdGenerationCompletes()
  const children = api.holdGenerationData()
  const completed = page.waitForResponse(response => response.request().method() === 'POST' && new URL(response.url()).pathname.endsWith(`/chats/${ERP}/dashboards/create-from-blueprints`))
  await blueprintPicker(page).getByRole('button', { name: `${zhCN.dashboard.createN} (2)`, exact: true }).click()
  await complete.arrived
  await children.arrived
  const response = await completed
  expect(response.status()).toBe(200)
  await response.finished()
  const old = dashboardCreates(api).at(-1)!
  await expect.poll(() => results.filter(event => event.operation_id === old.operationId).map(event => event.batch_result)).toEqual([{ succeeded: 2, failed: 0 }])
  await expect(generationOverlay(page)).toHaveCount(0)
  await expect(blueprintPicker(page)).toHaveCount(0)
  await expect(page.locator('button.dashboard-selector-btn')).toHaveText('isolated 快照看板')

  const plans = currentPlans()
  api.queueBlueprints(plans)
  await generateBlueprints(page, '旧 complete 到来前显示的新建议', 'A', plans.map(plan => plan.name))
  complete.release()
  await expect.poll(() => completes.filter(event => event.operation_id === old.operationId).map(event => event.dashboard.name)).toEqual(['isolated A 方案 0', 'isolated A 方案 1'])
  await expect(blueprintPicker(page).getByRole('heading', { name: plans[1].name, exact: true })).toBeVisible()
  await expect(page.locator('button.dashboard-selector-btn')).toHaveText('isolated 快照看板')
  children.release()
  await expect.poll(() => seen.filter(entry => entry.data.operation_id === old.operationId).map(entry => entry.data.data?.[0]?.margin)).toEqual([200, 201])
  await expect(widgetCard(page).getByText('48', { exact: true })).toBeVisible()
  await blueprintPicker(page).getByRole('button').filter({ has: page.getByRole('heading', { name: plans[0].name, exact: true }) }).click()
  await blueprintPicker(page).getByRole('button', { name: zhCN.dashboard.createN, exact: true }).click()
  await expect(page.locator('button.dashboard-selector-btn')).toHaveText(plans[1].name)
  await expect(widgetCard(page).getByText('201', { exact: true })).toBeVisible()
  await page.locator('button.dashboard-selector-btn').click()
  // Both old dashboards remain in the real selector list even though their completes were background events.
  await expect(page.getByText('isolated A 方案 0', { exact: true })).toBeVisible()
  await expect(page.getByText('isolated A 方案 1', { exact: true })).toBeVisible()
})
