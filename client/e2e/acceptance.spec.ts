import { expect, test, type Locator, type Page } from '@playwright/test'
import { CHAT_ID, OLD_RUN_ID, PRODUCT, RUN_ID, installApiMock } from './fixtures/api'

type Box = { x: number; y: number; width: number; height: number }

const DESKTOP = { width: 1440, height: 900 }
const NARROW = { width: 390, height: 844 }

async function titleIsProduct(page: Page) {
  await expect.poll(() => page.title()).toBe(PRODUCT)
}

async function openChat(page: Page, id = CHAT_ID) {
  await page.goto(`/chat/${id}`)
  await page.getByRole('button', { name: 'sales_pg' }).click()
  await expect(page.getByRole('region', { name: '经营报告' })).toBeVisible()
}

async function visibleBox(locator: Locator): Promise<Box> {
  const box = await locator.boundingBox()
  expect(box, 'control must be visible').not.toBeNull()
  return box as Box
}

function fullyInside(inner: Box, outer: Box) {
  expect(inner.width).toBeGreaterThan(16)
  expect(inner.height).toBeGreaterThan(16)
  expect(inner.x).toBeGreaterThanOrEqual(outer.x - 1)
  expect(inner.y).toBeGreaterThanOrEqual(outer.y - 1)
  expect(inner.x + inner.width).toBeLessThanOrEqual(outer.x + outer.width + 1)
  expect(inner.y + inner.height).toBeLessThanOrEqual(outer.y + outer.height + 1)
}

async function assertConfirmCancel(page: Page, confirmName: string | RegExp) {
  const confirm = page.getByRole('button', { name: confirmName }).last()
  const cancel = page.getByRole('button', { name: '取消' }).last()
  await expect(confirm).toBeVisible()
  await expect(cancel).toBeVisible()
  await expect(confirm).toHaveText(confirmName instanceof RegExp ? confirmName : confirmName)
  const confirmBox = await visibleBox(confirm)
  const cancelBox = await visibleBox(cancel)
  const viewport = page.viewportSize()
  expect(viewport).not.toBeNull()
  const frame = { x: 0, y: 0, width: viewport!.width, height: viewport!.height }
  fullyInside(confirmBox, frame)
  fullyInside(cancelBox, frame)
  const confirmText = (await confirm.innerText()).replace(/\s/g, '')
  const cancelText = (await cancel.innerText()).replace(/\s/g, '')
  expect(confirmText).not.toBe('取消')
  expect(cancelText).toBe('取消')
  expect(confirmText).not.toBe(cancelText)
}

async function chartGeometry(page: Page, chartId: string) {
  return page.locator(`[data-chart-id="${chartId}"]`).evaluate(root => {
    const input = JSON.parse(root.getAttribute('data-chart-input') || '[]') as Array<Record<string, string | null>>
    const plotted = Array.from(root.querySelectorAll('.recharts-line-dots circle, .recharts-bar-rectangle path, .recharts-bar-rectangles path'))
      .map(node => {
        const box = (node as SVGGraphicsElement).getBBox()
        return { x: box.x, y: box.y, width: box.width, height: box.height }
      })
      .filter(box => box.width > 0 && box.height > 0)
    const labels: string[] = []
    const walker = document.createTreeWalker(root, NodeFilter.SHOW_TEXT)
    let textNode = walker.nextNode()
    while (textNode) {
      const value = (textNode.textContent || '').trim()
      if (value) labels.push(value)
      textNode = walker.nextNode()
    }
    return { input, plotted, labels }
  })
}

test.use({
  locale: 'en-US',
  extraHTTPHeaders: { 'Accept-Language': 'en-US' },
})

test.describe('browser acceptance', () => {
  test('login welcome chat dashboard titles stay 元发问数平台', async ({ page }) => {
    await page.setViewportSize(DESKTOP)
    await installApiMock(page, { chats: [] })
    await page.goto('/')
    await expect(page.getByRole('button', { name: '登录' })).toBeVisible()
    await titleIsProduct(page)
    await expect(page.getByRole('heading', { name: PRODUCT, exact: true })).toBeVisible()
    await expect(page.locator('body')).not.toContainText('YanfaChatBI')

    await page.getByPlaceholder('用户名或邮箱').fill('fixture-user')
    await page.getByPlaceholder('密码').fill('secret1')
    await page.getByRole('button', { name: '登录', exact: true }).click()
    await expect(page.getByRole('heading', { name: /\u4f60\u95ee/ })).toBeVisible()
    await titleIsProduct(page)
    await page.reload()
    await expect(page.getByRole('heading', { name: /\u4f60\u95ee/ })).toBeVisible()
    await titleIsProduct(page)

    await page.evaluate(() => localStorage.setItem('token', 'fixture-token'))
    await installApiMock(page, { messages: 'new' })
    await page.goto(`/chat/${CHAT_ID}`)
    await expect(page.getByRole('button', { name: 'sales_pg' })).toBeVisible()
    await titleIsProduct(page)
    await page.getByRole('button', { name: 'sales_pg' }).click()
    await expect(page.getByRole('region', { name: '经营报告' })).toBeVisible()
    await titleIsProduct(page)
    await page.reload()
    await page.getByRole('button', { name: 'sales_pg' }).click()
    await expect(page.getByRole('region', { name: '经营报告' })).toBeVisible()
    await titleIsProduct(page)

    await page.getByRole('button', { name: '仪表盘', exact: true }).click()
    await expect(page.getByRole('heading', { name: '让数据动起来' })).toBeVisible()
    await titleIsProduct(page)
    await page.reload()
    await page.getByRole('button', { name: 'sales_pg' }).click()
    await page.getByRole('button', { name: '仪表盘', exact: true }).click()
    await expect(page.getByRole('heading', { name: '让数据动起来' })).toBeVisible()
    await titleIsProduct(page)
  })

  test('bar and line charts match the fixture table and skip the missing date', async ({ page }) => {
    await page.setViewportSize(DESKTOP)
    await page.addInitScript(() => localStorage.setItem('token', 'fixture-token'))
    await installApiMock(page)
    await openChat(page)

    const card = page.getByRole('region', { name: '经营报告' })
    const table = card.locator('table')
    await expect(table).toContainText('S1')
    await expect(table).toContainText('S2')
    await expect(table.getByRole('row').filter({ hasText: 'S1' })).toContainText('60')
    await expect(table.getByRole('row').filter({ hasText: 'S2' })).toContainText('60')
    await expect(card).toContainText('未扣退款')
    await expect(card).toContainText('cov:payment')
    await expect(card.getByText('缺失趋势点 2026-09-03')).toBeVisible()

    const bar = page.locator('[data-chart-id="shops-sales"]')
    const line = page.locator('[data-chart-id="trend-sales"]')
    await expect(bar).toBeVisible()
    await expect(line).toBeVisible()
    await expect(bar).toContainText('S1')
    await expect(bar).toContainText('S2')
    await expect(bar).toContainText('销售额')
    await expect(line).toContainText('2026-09-01')
    await expect(line).toContainText('2026-09-03')

    const barGeometry = await chartGeometry(page, 'shops-sales')
    const lineGeometry = await chartGeometry(page, 'trend-sales')
    expect(barGeometry.input).toEqual([
      { dimension: 'S1', sales: '60' },
      { dimension: 'S2', sales: '60' },
    ])
    expect(lineGeometry.input).toEqual([
      { dimension: '2026-09-01', sales: '20' },
      { dimension: '2026-09-02', sales: '40' },
      { dimension: '2026-09-03', sales: null },
    ])
    expect(barGeometry.plotted).toHaveLength(2)
    expect(lineGeometry.plotted).toHaveLength(2)
    expect(lineGeometry.labels).toEqual(expect.arrayContaining(['2026-09-01', '2026-09-03']))
    const plottedCenters = lineGeometry.plotted.map(dot => dot.x + dot.width / 2).sort((a, b) => a - b)
    const ticks = await line.evaluate(root => {
      const found: Array<{ label: string, x: number }> = []
      const walker = document.createTreeWalker(root, NodeFilter.SHOW_TEXT)
      let node = walker.nextNode()
      while (node) {
        const label = (node.textContent || '').trim()
        if (label.includes('2026') && node.parentElement) {
          const box = (node.parentElement as SVGGraphicsElement).getBBox()
          found.push({ label, x: box.x + box.width / 2 })
        }
        node = walker.nextNode()
      }
      return found
    })
    const missingTick = ticks.find(tick => tick.label === '2026-09-03')
    expect(missingTick).toBeTruthy()
    for (const center of plottedCenters) {
      expect(Math.abs(center - missingTick!.x)).toBeGreaterThan(8)
    }
    expect(JSON.stringify(lineGeometry.plotted)).not.toContain('"0"')

    const firstBar = bar.locator('.recharts-bar-rectangle path').first()
    await firstBar.hover({ force: true })
    const tooltip = page.locator('.recharts-tooltip-wrapper').filter({ hasText: 'S1' })
    await expect(tooltip).toBeVisible()
    await expect(tooltip).toContainText('销售额')
    await expect(tooltip).toContainText('60')
    await expect(table.getByRole('row').filter({ hasText: 'S1' })).toContainText('60')

    const firstDot = line.locator('.recharts-line-dots circle').first()
    await firstDot.hover({ force: true })
    const lineTip = page.locator('.recharts-tooltip-wrapper').last()
    await expect(lineTip).toContainText('2026-09-01')
    await expect(lineTip).toContainText('20')
    await expect(lineTip).not.toContainText('2026-09-03')
  })

  test('chinese surfaces survive Accept-Language en-US and reload', async ({ page }) => {
    await page.setViewportSize(DESKTOP)
    await installApiMock(page, { auth: 'network' })
    await page.addInitScript(() => localStorage.setItem('token', 'expired-token'))
    await page.goto('/')
    await expect(page.getByText('服务暂时不可用，请稍后重试。')).toBeVisible()
    await expect(page.getByRole('button', { name: '登录' })).toBeVisible()

    await page.getByPlaceholder('用户名或邮箱').fill('ab')
    await page.getByPlaceholder('密码').fill('123')
    await page.getByPlaceholder('用户名或邮箱').blur()
    await page.getByPlaceholder('密码').blur()
    await expect(page.getByText('用户名或邮箱至少 3 个字符')).toBeVisible()
    await expect(page.getByText('密码至少 6 个字符')).toBeVisible()

    await page.getByRole('button', { name: '新用户 — 注册' }).click()
    await page.getByPlaceholder('用户名', { exact: true }).fill('ab')
    await page.getByPlaceholder('邮箱').fill('not-an-email')
    await page.getByPlaceholder('密码', { exact: true }).fill('123')
    await page.getByPlaceholder('确认密码').fill('456')
    await page.getByRole('button', { name: '注册', exact: true }).click()
    await expect(page.getByText('用户名至少 3 个字符')).toBeVisible()
    await expect(page.getByText('请输入有效邮箱')).toBeVisible()
    await expect(page.getByText('密码至少 6 个字符')).toBeVisible()
    await expect(page.getByText('两次密码不一致')).toBeVisible()

    await page.getByRole('button', { name: '已有账号 — 登录' }).click()
    await page.getByRole('button', { name: '忘记密码？' }).click()
    await page.getByPlaceholder('邮箱').fill('bad')
    await page.getByRole('button', { name: '发送重置验证码' }).click()
    await expect(page.getByText('请输入有效邮箱')).toBeVisible()
    await page.getByPlaceholder('邮箱').fill('user@example.com')
    await page.getByRole('button', { name: '发送重置验证码' }).click()
    await expect(page.getByText('如果账号存在，将收到重置邮件。')).toBeVisible()

    await page.getByPlaceholder('6 位验证码').fill('12345')

    await page.getByPlaceholder('新密码', { exact: true }).fill('123')
    await page.getByPlaceholder('确认新密码').fill('456')
    await page.getByRole('button', { name: '重置密码' }).click()
    await expect(page.getByText('验证码必须为 6 位数字')).toBeVisible()
    await expect(page.getByText('密码至少 6 个字符')).toBeVisible()
    await expect(page.getByText('两次密码不一致')).toBeVisible()

    await page.addInitScript(() => localStorage.setItem('token', 'fixture-token'))
    const mock = await installApiMock(page, { messages: 'query', connection: 'down', execute: 'hang' })
    await page.goto(`/chat/${CHAT_ID}`)
    await page.getByRole('button', { name: 'sales_pg' }).click()
    await expect(page.getByText('已断开')).toBeVisible()
    await page.getByRole('button', { name: '重新连接' }).first().click()
    await expect(page.getByText(/重新连接数据源失败/)).toBeVisible()
    await expect(page.getByText('服务暂时不可用，请稍后重试。')).toBeVisible()

    await expect(page.getByText('查询被安全校验拦截。')).toBeVisible()
    await page.getByText('技术详情').first().click()
    await expect(page.getByText('SAFETY_VIOLATION')).toBeVisible()
    await page.getByRole('button', { name: '编辑查询' }).first().click()
    await assertConfirmCancel(page, '保存修改')
    await page.getByRole('button', { name: '取消' }).last().click()

    const cancelable = page.locator('div').filter({ has: page.getByText('SELECT sku FROM sales') }).first()
    await cancelable.getByTitle('执行查询').click()
    const cancelQuery = cancelable.getByTitle('取消查询')
    await expect(cancelQuery).toBeVisible()
    await expect.poll(() => mock.seen().some(item => item.includes('/queries/execute'))).toBe(true)
    await cancelQuery.click()
    await expect(page.getByText('查询已取消')).toBeVisible()
    await expect(cancelQuery).toHaveCount(0)

    await installApiMock(page, { messages: 'query', connection: 'down', execute: 'fail' })
    await page.getByTitle('执行查询').first().click()
    const failed = page.locator('div').filter({ has: page.getByText('SELECT sku FROM sales') }).first()
    await failed.getByText('技术详情').click()
    await expect(failed.getByText('QUERY_FAILED')).toBeVisible()

    const paged = page.locator('div').filter({ has: page.getByText('page fixture') }).first()
    const pager = paged.locator('div.gap-4').filter({ has: page.getByTitle('下一页') }).first()
    const table = paged.locator('table').first()
    await expect(pager).toContainText('1')
    await expect(pager).toContainText('3')
    await expect(table).toContainText('sku-0')
    await pager.getByTitle('下一页').click()
    await expect(pager).toContainText('2')
    await expect(table).toContainText('sku-25')
    await expect(table).not.toContainText('sku-0')

    await expect(page.getByText('暂无可展示数据')).toBeVisible()

    await page.getByRole('button', { name: '添加连接' }).first().click()
    await expect(page.getByText('数据源类型')).toBeVisible()
    await expect(page.getByText('PostgreSQL').first()).toBeVisible()
    await page.getByRole('button', { name: '关闭' }).last().click()

    await page.getByRole('button', { name: '仪表盘', exact: true }).click()
    await expect(page.getByRole('heading', { name: '让数据动起来' })).toBeVisible()
    await expect(page.getByRole('button', { name: '尝试推荐' })).toBeVisible()

    const posts = mock.posts()
    expect(posts.some(item => item.url.includes('/queries/execute'))).toBe(true)
    expect(JSON.stringify(posts)).not.toContain('en-US')
  })

  test('confirm and cancel stay readable on desktop and narrow screens', async ({ page }) => {
    for (const viewport of [DESKTOP, NARROW]) {
      await page.setViewportSize(viewport)
      await page.addInitScript(() => localStorage.setItem('token', 'fixture-token'))
      await installApiMock(page)
      await page.goto('/')
      const logoutName = viewport.width < 768 ? '退出' : '退出登录'
      await page.getByRole('button', { name: logoutName, exact: true }).click()
      await assertConfirmCancel(page, '确认')
      await page.getByRole('button', { name: '取消' }).click()
      await expect(page.getByRole('button', { name: logoutName, exact: true })).toBeVisible()
    }
  })


  test('old and new reports reload in Chinese without changing identifiers', async ({ page }) => {
    await page.setViewportSize(NARROW)
    await page.addInitScript(() => localStorage.setItem('token', 'fixture-token'))
    await installApiMock(page, { messages: 'old' })
    await page.goto(`/chat/${CHAT_ID}`)
    await page.getByRole('button', { name: '展开侧栏' }).click()
    await page.getByRole('button', { name: 'sales_pg' }).click()
    await expect(page.getByRole('region', { name: '经营报告' })).toBeVisible()
    await expect(page.getByText('English Shop 杯子 / sku-2 / gpt-4o')).toBeVisible()
    await expect(page.getByText('GPT-4 Omni')).toBeVisible()
    await page.reload()
    await page.getByRole('button', { name: '展开侧栏' }).click()
    await page.getByRole('button', { name: 'sales_pg' }).click()
    await expect(page.getByRole('region', { name: '经营报告' })).toBeVisible()
    await titleIsProduct(page)
    await expect(page.locator(`[data-run-id="${OLD_RUN_ID}"]`)).toBeVisible()

    await installApiMock(page, { messages: 'new' })
    await page.goto(`/chat/${CHAT_ID}`)
    await page.getByRole('button', { name: '展开侧栏' }).click()
    await page.getByRole('button', { name: 'sales_pg' }).click()
    await expect(page.locator(`[data-run-id="${RUN_ID}"]`)).toBeVisible()
    await page.reload()
    await page.getByRole('button', { name: '展开侧栏' }).click()
    await page.getByRole('button', { name: 'sales_pg' }).click()
    await expect(page.getByText('未扣退款').first()).toBeVisible()
    await titleIsProduct(page)

  })
})
