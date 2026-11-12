import { expect, test } from '@playwright/test'
import { chatRecord, installApiMock } from './fixtures/api'
import { zhCN } from '../src/i18n/zh-CN'

test('connection settings omit legacy AI result sharing on edit and create', async ({ page }) => {
  const chat = { ...chatRecord(), source_classification: 'independent' }
  chat.settings.share_data_with_ai = true
  await page.addInitScript(() => localStorage.setItem('token', 'fixture-token'))
  await installApiMock(page, { chats: [chat] })

  await page.route(/\/chats(?:\/[^/?]+)?$/, async route => {
    const method = route.request().method()
    const path = new URL(route.request().url()).pathname
    if (method === 'PATCH' && path.endsWith('/chats/' + chat.id)) {
      await route.fulfill({ json: { success: true, data: chat } })
    } else if (method === 'POST' && path.endsWith('/chats')) {
      await route.fulfill({ status: 201, json: { success: true, data: { ...chatRecord('new-fixture'), source_classification: 'independent' } } })
    } else {
      await route.fallback()
    }
  })

  await page.goto('/chat/' + chat.id)
  await page.getByTitle(zhCN.sidebar.connectionMenu, { exact: true }).click()
  await page.getByRole('button', { name: zhCN.sidebar.chatSettings, exact: true }).click()
  const settings = page.getByRole('button', { name: zhCN.connection.tabSettings, exact: true })
  await settings.click()
  await expect(page.getByText(zhCN.connection.autoFetch, { exact: true })).toBeVisible()
  await expect(page.getByText('与 AI 共享结果', { exact: true })).toHaveCount(0)
  const edit = page.waitForRequest(request => request.method() === 'PATCH' && request.url().endsWith('/chats/' + chat.id))
  await page.getByRole('button', { name: zhCN.connection.updateSettings, exact: true }).click()
  expect((await edit).postDataJSON().settings).toEqual({
    auto_execute_query: false, non_tech_mode: false, auto_generate_visualization: false,
  })
  await expect(page.getByText(zhCN.toasts.settingsUpdated, { exact: true })).toBeVisible()
  await page.getByRole('button', { name: zhCN.common.close, exact: true }).last().click()

  await page.getByRole('button', { name: zhCN.sidebar.addConnection, exact: true }).click()
  await settings.click()
  await expect(page.getByText(zhCN.connection.autoFetch, { exact: true })).toBeVisible()
  await expect(page.getByText('与 AI 共享结果', { exact: true })).toHaveCount(0)
  await page.getByRole('button', { name: zhCN.connection.tabConnection, exact: true }).click()
  await page.locator('select').filter({ has: page.locator('option[value="postgresql"]') }).selectOption('postgresql')
  await page.locator('input[name=host]').fill('db.example.test')
  await page.locator('input[name=port]').fill('5432')
  await page.locator('input[name=database]').fill('fixture')
  await page.locator('input[name=username]').fill('reader')
  await page.locator('input[name=password]').fill('fixture-password')
  const create = page.waitForRequest(request => request.method() === 'POST' && request.url().endsWith('/chats'))
  await page.getByRole('button', { name: zhCN.common.create, exact: true }).click()
  expect((await create).postDataJSON().settings).toEqual({
    auto_execute_query: true, non_tech_mode: false,
  })
  await expect(page.getByRole('button', { name: zhCN.connection.tabKnowledge, exact: true })).toBeVisible()
})
