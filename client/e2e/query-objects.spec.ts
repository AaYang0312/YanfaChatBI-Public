import { expect, test, type Page } from '@playwright/test'
import { chatRecord, fixtureMessages, installApiMock } from './fixtures/api'
import { zhCN } from '../src/i18n/zh-CN'
import type { Chat, ObjectSelection, TableInfo, TablesResponse } from '../src/types/chat'

const table: TableInfo = { name: '"public"."orders"', object_id: 'table-1', object_instance_version: 'i1', schema_name: 'public', object_name: 'orders', object_type: 'table', sql_identifier: '"public"."orders"', is_selected: true, row_count: null, row_count_status: 'unknown', columns: [{ name: 'id', type: 'integer', is_nullable: false, nullable: null, ordinal: 1 }] }
const view: TableInfo = { ...table, name: '"reporting"."sales"', object_id: 'view-1', schema_name: 'reporting', object_name: 'sales', object_type: 'view', is_selected: false, partial_columns: true }
const pureView: ObjectSelection = { selection_version: 2, mode: 'explicit', objects: [{ schema: 'reporting', name: 'sales', kind: 'view' }] }
async function setup(page: Page, options: { legacy?: boolean; selected?: ObjectSelection; complex?: boolean; pure?: boolean; ready?: boolean } = {}) {
  const chat = { ...chatRecord(), source_classification: 'independent', selection_revision: 7, selected_objects: options.selected, query_object_index: options.ready ? { status: 'ready', format_version: 2, active_generation: 'old-generation' } : null } as Chat
  const object = options.complex ? { ...view, name: '"a.b"."x,y""z"', object_id: 'complex', schema_name: 'a.b', object_name: 'x,y"z' } : view
  const directory: TablesResponse = options.legacy
    ? { tables: [{ ...table, name: 'orders' }] }
    : { tables: options.pure ? [object] : [table, object], metadata_version: 2, query_objects_enabled: true, selection_revision: 7, selected_objects: options.selected }
  if (options.legacy) chat.selected_collections = 'orders'
  const patches: Record<string, unknown>[] = []
  let conflict = false
  let executions = 0
  let catalogInvalidates = false
  await page.addInitScript(() => localStorage.setItem('token', 'fixture-token'))
  await installApiMock(page, { chats: [chat] })
  await page.route('**/api/chats/**', async route => {
    const request = route.request()
    const path = new URL(request.url()).pathname
    if (path.endsWith('/tables')) {
      if (catalogInvalidates) chat.query_object_index = { ...chat.query_object_index!, status: 'stale' }
      return route.fulfill({ json: { success: true, data: directory } })
    }
    if (path.endsWith('/messages')) {
      const messages = fixtureMessages('query').slice(0, 1)
      messages[0].queries![0] = { ...messages[0].queries![0], query: 'SELECT id FROM "reporting"."sales"', is_executed: false, execution_result: undefined, execution_time: undefined, error: undefined, example_result: [], example_execution_time: undefined }
      return route.fulfill({ json: { success: true, data: { messages, total: 1 } } })
    }
    if (path.endsWith('/queries/execute')) { executions++; return route.fulfill({ json: { success: true, data: { is_executed: true, execution_result: [{ id: 1 }] } } }) }
    if (path.endsWith('/chats/' + chat.id)) {
      if (request.method() === 'PATCH') {
        const body = request.postDataJSON()
        patches.push(body)
        if (conflict) return route.fulfill({ status: 409, json: { success: false, error: 'SELECTION_REVISION_CONFLICT' } })
        Object.assign(chat, body, { selection_revision: (chat.selection_revision ?? 0) + 1 })
        directory.selected_objects = chat.selected_objects
        directory.selection_revision = chat.selection_revision
      }
      return route.fulfill({ json: { success: true, data: chat } })
    }
    return route.fallback()
  })
  // No real backend, model, telemetry or third-party traffic leaves this fixture.
  await page.route('**/*', route => {
    const url = new URL(route.request().url())
    return url.hostname === '127.0.0.1' ? route.fallback() : route.abort('blockedbyclient')
  })
  await page.goto('/chat/' + chat.id)
  return { chat, directory, object, patches, conflict: () => { conflict = true }, executions: () => executions, invalidateCatalog: () => { catalogInvalidates = true } }
}
async function open(page: Page) {
  await page.getByTitle(zhCN.sidebar.connectionMenu, { exact: true }).click()
  await page.getByRole('button', { name: zhCN.sidebar.chatSettings, exact: true }).click()
  await page.getByRole('button', { name: zhCN.connection.tabKnowledge, exact: true }).click()
}
async function save(page: Page) { await page.getByRole('button', { name: zhCN.connection.updateKnowledge, exact: true }).click() }

test('pure view save and reopen preserves v2 scope without pretending the index is ready', async ({ page }) => {
  const fixture = await setup(page, { pure: true })
  await open(page)
  await page.getByRole('checkbox', { name: view.name, exact: true }).check()
  await save(page)
  await expect(page.getByText('选择已保存；索引未构建。')).toBeVisible()
  expect(fixture.patches).toEqual([{ selected_objects: pureView, selection_revision: 7 }])
  await page.getByRole('button', { name: zhCN.common.close, exact: true }).last().click()
  await open(page)
  await expect(page.getByRole('checkbox', { name: view.name, exact: true })).toBeChecked()
  await expect(page.getByRole('heading', { name: 'reporting', exact: true })).toBeVisible()
})
test('mixed objects remain explicit even when every current object is selected', async ({ page }) => {
  const fixture = await setup(page)
  await open(page)
  await expect(page.getByRole('checkbox', { name: table.name, exact: true })).toBeChecked()
  await expect(page.getByRole('checkbox', { name: view.name, exact: true })).not.toBeChecked()
  await page.getByRole('checkbox', { name: view.name, exact: true }).check()
  await save(page)
  await expect.poll(() => fixture.patches.length).toBe(1)
  expect(fixture.patches[0]).toEqual({ selected_objects: { selection_version: 2, mode: 'explicit', objects: [{ schema: 'public', name: 'orders', kind: 'table' }, { schema: 'reporting', name: 'sales', kind: 'view' }] }, selection_revision: 7 })
})
test('all kinds is an explicit opt-in while old ALL remains table-only', async ({ page }) => {
  const fixture = await setup(page)
  await open(page)
  await expect(page.getByRole('checkbox', { name: '全部表（自动检测）', exact: true })).toBeChecked()
  await page.getByRole('checkbox', { name: '全部表和视图（自动检测）', exact: true }).check()
  await save(page)
  await expect.poll(() => fixture.patches.length).toBe(1)
  expect(fixture.patches[0]).toEqual({ selected_objects: { selection_version: 2, mode: 'all', objects: [], include_kinds: ['table', 'view'] }, selection_revision: 7 })
})
test('complex identifiers are saved as exact refs and searchable', async ({ page }) => {
  const fixture = await setup(page, { complex: true })
  await open(page)
  await page.getByRole('checkbox', { name: fixture.object.name, exact: true }).check()
  await page.getByRole('checkbox', { name: table.name, exact: true }).uncheck()
  await page.getByRole('textbox', { name: '搜索查询对象' }).fill('x,y')
  await expect(page.getByRole('checkbox', { name: fixture.object.name, exact: true })).toBeVisible()
  await save(page)
  await expect.poll(() => fixture.patches.length).toBe(1)
  expect(fixture.patches[0].selected_objects).toEqual({ selection_version: 2, mode: 'explicit', objects: [{ schema: 'a.b', name: 'x,y"z', kind: 'view' }] })
})
test('409 preserves the draft and offers reload instead of success', async ({ page }) => {
  const fixture = await setup(page)
  fixture.conflict()
  await open(page)
  await page.getByRole('checkbox', { name: view.name, exact: true }).check()
  await save(page)
  await expect(page.getByRole('alert')).toContainText('选择已变化')
  await expect(page.getByRole('checkbox', { name: view.name, exact: true })).toBeChecked()
  await expect(page.getByRole('button', { name: '重新加载对象' })).toBeEnabled()
  await expect(page.getByText('选择已保存；索引未构建。')).toHaveCount(0)
})
test('old backend keeps explicit scope rather than promoting it to ALL', async ({ page }) => {
  const fixture = await setup(page, { legacy: true })
  await open(page)
  await expect(page.getByRole('checkbox', { name: 'orders', exact: true })).toBeChecked()
  await save(page)
  await expect.poll(() => fixture.patches.length).toBe(1)
  expect(fixture.patches[0]).toEqual({ selected_collections: 'orders' })
})
test('generate-only still shows SQL and only executes on the explicit action', async ({ page }) => {
  const fixture = await setup(page, { selected: pureView })
  await expect(page.locator('pre').filter({ hasText: 'SELECT id FROM' }).first()).toBeVisible()
  const execute = page.getByTitle(zhCN.query.executeQuery, { exact: true })
  await expect(execute).toBeEnabled()
  expect(fixture.executions()).toBe(0)
  await execute.click()
  await expect.poll(fixture.executions).toBe(1)
})


test('A3 a catalog read invalidating the index is reflected after reload at the same selection revision', async ({ page }) => {
  const fixture = await setup(page, { ready: true, selected: pureView })
  await open(page)
  await expect(page.getByText('结构可用，执行未验证 · 索引已就绪', { exact: true })).toBeVisible()
  fixture.invalidateCatalog()
  await page.getByRole('button', { name: '重新加载对象' }).click()
  await expect(page.getByText('结构可用，执行未验证 · 索引待更新', { exact: true })).toBeVisible()
  await expect(page.getByText('结构可用，执行未验证 · 索引已就绪', { exact: true })).toHaveCount(0)
  await expect(page.getByRole('checkbox', { name: view.name, exact: true })).toBeChecked()
  expect(fixture.chat.selection_revision).toBe(7)
  expect(fixture.patches).toHaveLength(0)
})
