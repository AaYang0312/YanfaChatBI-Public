import type { Page } from '@playwright/test'
import { test, expect, CHAT_A, CHAT_B, CHAT_NAMES, OLD_RUN, type WorkflowApi } from './fixtures/v15-workflow'
import type { AnalysisRequest } from '../src/types/assistant'

const INPUT = '经营分析'
const MENU = '工作流'
const PRODUCT = '商品跨店分析'
const COMPARISON = '平台/店铺对比'

test.use({ viewport: { width: 1440, height: 900 } })

async function expectConnectedComposer(page: Page) {
  const composer = page.getByPlaceholder('问问你的数据…')
  const reconnect = page.getByRole('button', { name: '重新连接', exact: true })
  // Connection can finish while the reconnect control is disappearing.
  await reconnect.or(composer.and(page.locator(':enabled'))).first().click()
  await expect(composer).toBeEnabled()
}

async function openChat(page: Page, api: WorkflowApi) {
  await page.goto(`/chat/${CHAT_A}`)
  await expect(page.getByRole('heading', { level: 2, name: CHAT_NAMES[CHAT_A], exact: true })).toBeVisible()
  await expectConnectedComposer(page)
  await expect(page.locator(`[data-run-id="${OLD_RUN}"]`)).toBeVisible()
  await expect.poll(() => api.requests.some(item => item.path.endsWith(`/${CHAT_A}/stream`))).toBe(true)
}

async function choose(page: Page, name = PRODUCT) {
  await page.getByRole('button', { name: MENU, exact: true }).click()
  await page.getByRole('menuitemradio', { name, exact: true }).click()
  await expect(page.getByRole('form', { name: INPUT })).toBeVisible()
}

async function explicit(page: Page, id: string) {
  await page.getByRole('button', { name: '明确条件', exact: true }).click()
  await page.getByLabel('商品标识', { exact: true }).fill(id)
  await page.getByLabel('开始日期', { exact: true }).fill('2026-09-01')
  await page.getByLabel('结束日期', { exact: true }).fill('2026-09-08')
}

function creates(api: WorkflowApi) {
  return api.requests.filter(item => item.method === 'POST' && item.path.endsWith('/assistant/runs'))
}

async function switchChat(page: Page, id: string) {
  await page.getByRole('button', { name: new RegExp(CHAT_NAMES[id]) }).click()
  await expect(page).toHaveURL(new RegExp(`/chat/${id}$`))
  await expect(page.getByRole('heading', { level: 2, name: CHAT_NAMES[id], exact: true })).toBeVisible()
  await expectConnectedComposer(page)
}

test.describe('isolated workflow page/API transitions', () => {
  test('default entry preserves history, supports both keyboard choices, returns focus and retains drafts', async ({ page, workflowApi: api }) => {
    await openChat(page, api)
    const trigger = page.getByRole('button', { name: MENU, exact: true })
    const ordinary = page.getByPlaceholder('问问你的数据…')
    await expect(trigger).toHaveAttribute('aria-expanded', 'false')
    await expect(page.getByRole('form', { name: INPUT })).toHaveCount(0)
    await ordinary.fill('普通消息草稿')
    await trigger.focus()
    await trigger.press('ArrowDown')
    await expect(page.getByRole('menuitemradio', { name: PRODUCT })).toBeFocused()
    await page.keyboard.press('Enter')
    await expect(page.getByLabel('分析类型')).toHaveValue('product_analysis')
    await page.getByLabel('问题', { exact: true }).fill('未提交草稿')
    await page.keyboard.press('Escape')
    await expect(trigger).toBeFocused()
    await trigger.press('ArrowUp')
    await expect(page.getByRole('menuitemradio', { name: COMPARISON })).toBeFocused()
    await page.keyboard.press('Enter')
    await expect(page.getByLabel('分析类型')).toHaveValue('sales_comparison')
    await expect(page.getByLabel('问题', { exact: true })).toHaveValue('未提交草稿')
    await page.getByRole('button', { name: '关闭工作流', exact: true }).click()
    await expect(trigger).toBeFocused()
    await trigger.click()
    await expect(page.getByLabel('问题', { exact: true })).toHaveValue('未提交草稿')
    await ordinary.click()
    await expect(trigger).toHaveAttribute('aria-expanded', 'false')
    await expect(ordinary).toBeFocused()
    await expect(ordinary).toHaveValue('普通消息草稿')
    expect(creates(api)).toHaveLength(0)
    expect(api.requests.filter(item => item.path.endsWith('/assistant/resolve'))).toHaveLength(0)
    await expect(page.locator(`[data-run-id="${OLD_RUN}"] tfoot`)).toContainText('120')
  })

  test('ambiguity, date and server errors retain input; confirmation creates one intended run and message', async ({ page, workflowApi: api }) => {
    await openChat(page, api)
    await choose(page)
    const form = page.getByRole('form', { name: INPUT })
    const question = page.getByLabel('问题', { exact: true })
    await question.fill('invalid')
    await form.getByRole('button', { name: '开始分析', exact: true }).click()
    await expect(form.getByRole('alert')).toBeVisible()
    await expect(question).toHaveValue('invalid')
    expect(creates(api)).toHaveLength(0)
    await question.fill('杯子')
    await page.getByLabel('开始日期', { exact: true }).fill('2026-09-08')
    await page.getByLabel('结束日期', { exact: true }).fill('2026-09-01')
    const resolvesBeforeDate = api.requests.filter(item => item.path.endsWith('/assistant/resolve')).length
    await form.getByRole('button', { name: '开始分析', exact: true }).click()
    await expect(form.getByRole('alert')).toBeVisible()
    expect(api.requests.filter(item => item.path.endsWith('/assistant/resolve'))).toHaveLength(resolvesBeforeDate)
    await page.getByLabel('开始日期', { exact: true }).fill('2026-09-01')
    await page.getByLabel('结束日期', { exact: true }).fill('2026-09-08')
    await form.getByRole('button', { name: '开始分析', exact: true }).click()
    await expect(form.getByRole('button', { name: 'isolated 杯子 A', exact: true })).toBeVisible()
    await expect(form.getByRole('button', { name: 'isolated SKU sku-2', exact: true })).toBeVisible()
    expect(creates(api)).toHaveLength(0)
    await page.getByRole('button', { name: '关闭工作流' }).click()
    await page.getByRole('button', { name: MENU, exact: true }).click()
    await expect(question).toHaveValue('杯子')
    api.failNextCreate()
    await form.getByRole('button', { name: 'isolated SKU sku-2' }).click()
    await expect(form.getByRole('alert')).toBeVisible()
    await expect(form.getByRole('button', { name: 'isolated 杯子 A' })).toBeEnabled()
    await expect(question).toHaveValue('杯子')
    expect(creates(api)).toHaveLength(1)
    const accepted = api.holdNext('create', CHAT_A)
    await form.getByRole('button', { name: 'isolated SKU sku-2' }).click()
    await accepted.arrived
    const run = Array.from(api.runs.values()).find(item => item.view.run_id !== OLD_RUN)!
    expect(run.view.normalized_input.product).toEqual({ product_id: '', sku_ids: ['sku-2'] })
    const response = page.waitForResponse(item => item.url().endsWith(`/${CHAT_A}/assistant/runs`))
    api.complete(run.view.run_id)
    accepted.release()
    await response
    await expect(page.locator(`[data-run-id="${run.view.run_id}"]`)).toBeVisible()
    await expect(page.getByRole('button', { name: MENU, exact: true })).toHaveAttribute('aria-expanded', 'false')
    expect(creates(api)).toHaveLength(2)
    expect(api.messages.get(CHAT_A)!.filter(message => message.assistant_run_id === run.view.run_id)).toHaveLength(1)
    await expect(page.locator(`[data-run-id="${run.view.run_id}"]`)).toHaveCount(1)
  })

  test('natural resolved input and explicit comparison both use the accepted report chain', async ({ page, workflowApi: api }) => {
    await openChat(page, api)
    await choose(page)
    await page.getByLabel('问题', { exact: true }).fill('确定商品 P1')
    await page.getByLabel('开始日期', { exact: true }).fill('2026-09-01')
    await page.getByLabel('结束日期', { exact: true }).fill('2026-09-08')
    const natural = api.holdNext('create', CHAT_A)
    await page.getByRole('button', { name: '开始分析', exact: true }).click()
    await natural.arrived
    const first = Array.from(api.runs.keys()).find(id => id !== OLD_RUN)!
    api.complete(first)
    natural.release()
    await expect(page.locator(`[data-run-id="${first}"]`)).toBeVisible()
    await choose(page, COMPARISON)
    await explicit(page, 'P1')
    const comparison = api.holdNext('create', CHAT_A)
    await page.getByRole('form', { name: INPUT }).evaluate((element: HTMLFormElement) => {
      element.requestSubmit()
      element.requestSubmit()
    })
    await comparison.arrived
    expect(creates(api)).toHaveLength(2)
    const second = Array.from(api.runs.keys()).find(id => id !== OLD_RUN && id !== first)!
    expect(api.runs.get(second)!.view.normalized_input.workflow).toBe('sales_comparison')
    expect(api.requests.filter(item => item.path.endsWith('/assistant/resolve'))).toHaveLength(1)
    api.complete(second)
    comparison.release()
    await expect(page.locator(`[data-run-id="${second}"] tfoot td`)).toHaveText(['3', '120', '72', '48', '0.4', '40', '10'])
    expect(api.messages.get(CHAT_A)!.filter(message => message.assistant_run_id === second)).toHaveLength(1)
  })

  test('late A resolve is discarded before create while B keeps its own draft', async ({ page, workflowApi: api }) => {
    await openChat(page, api)
    await choose(page)
    await page.getByLabel('问题', { exact: true }).fill('确定 A 商品')
    const late = api.holdNext('resolve', CHAT_A)
    await page.getByRole('button', { name: '开始分析', exact: true }).click()
    await late.arrived
    await switchChat(page, CHAT_B)
    await choose(page, COMPARISON)
    await expect(page.getByLabel('问题', { exact: true })).toHaveValue('')
    await page.getByLabel('问题', { exact: true }).fill('B 草稿')
    const response = page.waitForResponse(item => item.url().endsWith(`/${CHAT_A}/assistant/resolve`))
    late.release()
    await response
    await expect(page.getByLabel('问题', { exact: true })).toHaveValue('B 草稿')
    await expect(page.getByRole('button', { name: '开始分析', exact: true })).toBeEnabled()
    expect(creates(api)).toHaveLength(0)
    expect(api.messages.get(CHAT_B)).toEqual([])
    await expect(page.getByRole('region', { name: '经营报告' })).toHaveCount(0)
  })

  test('late A create stays in A and its finally cannot unlock B for duplicate submission', async ({ page, workflowApi: api }) => {
    await openChat(page, api)
    await choose(page)
    await explicit(page, 'A-product')
    const a = api.holdNext('create', CHAT_A)
    await page.getByRole('button', { name: '开始分析', exact: true }).click()
    await a.arrived
    const aRun = Array.from(api.runs.keys()).find(id => id !== OLD_RUN)!
    await switchChat(page, CHAT_B)
    await choose(page, COMPARISON)
    await explicit(page, 'B-product')
    const b = api.holdNext('create', CHAT_B)
    await page.getByRole('button', { name: '开始分析', exact: true }).click()
    await b.arrived
    const bRun = Array.from(api.runs.values()).find(item => item.chatId === CHAT_B)!.view.run_id
    const aResponse = page.waitForResponse(item => item.url().endsWith(`/${CHAT_A}/assistant/runs`))
    api.complete(aRun)
    a.release()
    await aResponse
    await expect(page.getByRole('button', { name: '正在解析…', exact: true })).toBeDisabled()
    await expect(page.locator(`[data-run-id="${aRun}"]`)).toHaveCount(0)
    await page.getByRole('form', { name: INPUT }).evaluate((element: HTMLFormElement) => element.requestSubmit())
    expect(creates(api)).toHaveLength(2)
    const bResponse = page.waitForResponse(item => item.url().endsWith(`/${CHAT_B}/assistant/runs`))
    api.complete(bRun)
    b.release()
    await bResponse
    await expect(page.locator(`[data-run-id="${bRun}"]`)).toHaveCount(1)
    await expect(page.locator(`[data-run-id="${aRun}"]`)).toHaveCount(0)
    expect(creates(api).map(item => (item.body as AnalysisRequest).product.product_id)).toEqual(['A-product', 'B-product'])
    await switchChat(page, CHAT_A)
    await expect(page.locator(`[data-run-id="${aRun}"]`)).toBeVisible()
    await expect(page.locator(`[data-run-id="${bRun}"]`)).toHaveCount(0)
  })

  test('closing accepted work preserves old/new snapshots through refresh and a basis change creates a distinct run', async ({ page, workflowApi: api }) => {
    await openChat(page, api)
    const old = page.locator(`[data-run-id="${OLD_RUN}"]`)
    const oldTable = await old.locator('table').innerText()
    const oldCoverage = await old.locator('[data-field="coverage"]').innerText()
    await choose(page)
    await explicit(page, 'P1')
    const pending = api.holdNext('create', CHAT_A)
    await page.getByRole('button', { name: '开始分析', exact: true }).click()
    await pending.arrived
    const newRun = Array.from(api.runs.keys()).find(id => id !== OLD_RUN)!
    await page.getByRole('button', { name: '关闭工作流', exact: true }).click()
    const runningResponse = page.waitForResponse(item => item.request().method() === 'GET' && item.url().endsWith(`/assistant/runs/${newRun}`))
    pending.release()
    const running = await runningResponse
    expect((await running.json()).data.status).toBe('running')
    await expect(page.locator(`[data-run-id="${newRun}"]`)).toHaveCount(0)
    await expect(page.getByRole('button', { name: MENU, exact: true })).toHaveAttribute('aria-expanded', 'false')
    api.complete(newRun)
    const current = page.locator(`[data-run-id="${newRun}"]`)
    await expect(current).toBeVisible()
    const newTable = await current.locator('table').innerText()
    const asOf = await current.locator('[data-field="data-as-of"]').innerText()
    await page.reload()
    await expect(page.locator(`[data-run-id="${OLD_RUN}"] table`)).toHaveText(oldTable, { useInnerText: true })
    await expect(page.locator(`[data-run-id="${newRun}"] table`)).toHaveText(newTable, { useInnerText: true })
    await expect(page.locator(`[data-run-id="${newRun}"] [data-field="data-as-of"]`)).toHaveText(asOf)
    await expect(page.locator(`[data-run-id="${OLD_RUN}"] [data-field="coverage"]`)).toHaveText(oldCoverage)
    expect(creates(api)).toHaveLength(1)
    const basis = api.holdNext('create', CHAT_A)
    await current.getByRole('button', { name: '切换口径', exact: true }).click()
    await basis.arrived
    const changed = Array.from(api.runs.values()).find(item => item.view.normalized_input.parent_run_id === newRun)!
    expect(changed.view.run_id).not.toBe(newRun)
    expect(changed.view.normalized_input.basis).toBe('outstock')
    await expect(current.locator('table')).toHaveText(newTable, { useInnerText: true })
    api.complete(changed.view.run_id)
    basis.release()
    await expect(page.locator(`[data-run-id="${changed.view.run_id}"] [data-field="basis"]`)).toHaveText('出库口径')
    expect(api.runs.get(newRun)!.view.report!.basis).toBe('payment')
    await page.reload()
    await expect(page.locator(`[data-run-id="${newRun}"] [data-field="basis"]`)).toHaveText('支付口径')
    await expect(page.locator(`[data-run-id="${changed.view.run_id}"] table`)).toHaveText(newTable, { useInnerText: true })
    expect(creates(api)).toHaveLength(2)
    expect(api.requests.filter(item => item.path.endsWith('/cancel'))).toHaveLength(0)
  })

  test('disconnection keeps the entry visible but blocks resolve, explicit create and candidate execution', async ({ page, workflowApi: api }) => {
    await openChat(page, api)
    await choose(page)
    await page.getByLabel('问题', { exact: true }).fill('杯子')
    await page.getByRole('button', { name: '开始分析', exact: true }).click()
    await expect(page.getByRole('button', { name: 'isolated SKU sku-2' })).toBeEnabled()
    api.disconnect(CHAT_A)
    await expect(page.getByRole('button', { name: 'isolated SKU sku-2' })).toBeDisabled()
    await expect(page.getByText('请先连接数据源', { exact: true })).toBeVisible()
    await page.getByRole('form', { name: INPUT }).evaluate((element: HTMLFormElement) => element.requestSubmit())
    await page.getByRole('button', { name: '关闭工作流' }).click()
    await page.getByRole('button', { name: MENU, exact: true }).click()
    await expect(page.getByLabel('问题', { exact: true })).toHaveValue('杯子')
    await page.getByRole('button', { name: '明确条件', exact: true }).click()
    await page.getByLabel('商品标识', { exact: true }).fill('P1')
    await page.getByRole('form', { name: INPUT }).evaluate((element: HTMLFormElement) => element.requestSubmit())
    expect(creates(api)).toHaveLength(0)
    expect(api.requests.filter(item => item.path.endsWith('/assistant/resolve'))).toHaveLength(1)
  })

  for (const viewport of [{ width: 1440, height: 900 }, { width: 390, height: 844 }]) {
    test(`workflow scroll leaves model and send usable at ${viewport.width}px`, async ({ page, workflowApi: api }, testInfo) => {
      await page.setViewportSize(viewport)
      await openChat(page, api)
      await choose(page, COMPARISON)
      await explicit(page, 'P1')
      await page.getByLabel('范围', { exact: true }).selectOption('selected')
      const dialog = page.getByRole('dialog', { name: MENU, exact: true })
      const send = page.getByRole('button', { name: '发送消息', exact: true })
      const model = page.getByRole('button', { name: /已选： isolated 模型/ })
      const popupBox = await dialog.boundingBox()
      const sendBox = await send.boundingBox()
      const modelBox = await model.boundingBox()
      expect(popupBox).not.toBeNull()
      expect(sendBox).not.toBeNull()
      expect(modelBox).not.toBeNull()
      expect(popupBox!.x).toBeGreaterThanOrEqual(0)
      expect(popupBox!.x + popupBox!.width).toBeLessThanOrEqual(viewport.width)
      expect(popupBox!.y).toBeGreaterThanOrEqual(0)
      expect(popupBox!.y + popupBox!.height).toBeLessThanOrEqual(Math.min(sendBox!.y, modelBox!.y))
      await page.getByRole('button', { name: '开始分析', exact: true }).scrollIntoViewIfNeeded()
      await expect(page.getByRole('button', { name: '开始分析', exact: true })).toBeVisible()
      if (viewport.width === 390) {
        expect(await dialog.evaluate(element => element.scrollHeight > element.clientHeight && element.scrollTop > 0)).toBe(true)
      }
      await expect(send).toBeEnabled()
      await expect(model).toBeEnabled()
      await page.screenshot({ path: testInfo.outputPath(`isolated-workflow-${viewport.width}.png`), fullPage: true })
      await model.click()
      await expect(page.getByRole('heading', { name: '选择 AI 模型', exact: true })).toBeVisible()
      await expect(page.getByRole('button', { name: MENU, exact: true })).toHaveAttribute('aria-expanded', 'false')
      expect(creates(api)).toHaveLength(0)
    })
  }

  test('pinned and dashboard switches preserve reports and introduce no second workflow form', async ({ page, workflowApi: api }) => {
    await openChat(page, api)
    await choose(page)
    await page.getByLabel('问题', { exact: true }).fill('视图切换草稿')
    await page.getByRole('button', { name: '对话选项', exact: true }).click()
    await page.getByRole('button', { name: '显示置顶', exact: true }).click()
    await expect(page.getByText('暂无置顶消息', { exact: true })).toBeVisible()
    await expect(page.getByRole('form', { name: INPUT })).toHaveCount(0)
    await page.getByRole('button', { name: '对话选项', exact: true }).click()
    await page.getByRole('button', { name: '显示消息', exact: true }).click()
    await page.getByRole('button', { name: MENU, exact: true }).click()
    await expect(page.getByLabel('问题', { exact: true })).toHaveValue('视图切换草稿')
    await page.getByRole('button', { name: '仪表盘', exact: true }).click()
    await expect(page.getByRole('heading', { name: '让数据动起来', exact: true })).toBeVisible()
    await expect(page.getByRole('form', { name: INPUT })).toHaveCount(0)
    await page.getByRole('button', { name: '对话', exact: true }).click()
    await expect(page.locator(`[data-run-id="${OLD_RUN}"]`)).toBeVisible()
    await page.getByRole('button', { name: MENU, exact: true }).click()
    await expect(page.getByRole('form', { name: INPUT })).toHaveCount(1)
    await expect(page.getByLabel('问题', { exact: true })).toHaveValue('视图切换草稿')
    expect(creates(api)).toHaveLength(0)
  })
})
