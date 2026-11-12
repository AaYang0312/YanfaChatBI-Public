import { expect, test, type Page } from '@playwright/test'
import { chatRecord, installApiMock } from './fixtures/api'
import { zhCN } from '../src/i18n/zh-CN'
import type { Chat, TableInfo } from '../src/types/chat'
import type { ObjectKnowledgeResponse, KnowledgeTextRequest, KnowledgeMappingsRequest } from '../src/types/objectKnowledge'

const view: TableInfo = { name: '"a.b"."sales,x""y"', sql_identifier: '"a.b"."sales,x""y"', object_id: 'view-kb', object_instance_version: 'v1', schema_name: 'a.b', object_name: 'sales,x"y', object_type: 'view', is_selected: true, row_count: null, row_count_status: 'unknown', columns: [{ name: 'amount', type: 'numeric', is_nullable: false, nullable: null }] }
const table: TableInfo = { ...view, object_id: 'table-kb', name: '"public"."orders"', object_type: 'table', schema_name: 'public', object_name: 'orders' }
async function setup(page: Page, options: { disabled?: boolean; locked?: boolean; legacy?: boolean; history?: 'empty' | 'object-only'; comments?: boolean } = {}) {
  const chat: Chat = { ...chatRecord(), source_classification: options.locked ? 'registered_erp' : 'independent',
    ...(options.locked ? { source_id: 'locked-source', source_binding_revision: 1 } : {}),
    selection_revision: 7, selected_objects: options.legacy ? undefined : { selection_version: 2, mode: 'all', include_kinds: ['table', 'view'] }, query_object_index: null }
  const objects = [view, table, { ...table, object_id: 'not-selected', name: '不可选目录对象', is_selected: false }]
  const kb: ObjectKnowledgeResponse = {
    chat_id: chat.id, query_objects_enabled: !options.disabled && !options.legacy, kb_revision: options.legacy ? undefined : 'kb-1', index_status: 'stale', refresh_required: true, saved: false, created_at: '', updated_at: '',
    table_descriptions: [
      { table_name: '历史甲', description: '甲的原文', provenance: 'legacy_unknown', state: 'pending', field_descriptions: [{ field_name: 'amount', description: '金额原文', provenance: 'legacy_unknown' }] },
      { table_name: '历史乙', description: '乙的原文', provenance: 'ai_generated', state: 'pending', field_descriptions: [] },
      { table_name: '不可读旧对象', description: '仍保留旧原文', object_id: 'hidden', object_instance_version: 'old', provenance: 'user_edit', state: 'mapped', field_descriptions: [] },
    ], effective_descriptions: [],
  }
  if (options.history === 'empty') kb.table_descriptions = []
  if (options.history === 'object-only') kb.table_descriptions = [{ table_name: view.name, object_id: view.object_id, object_instance_version: view.object_instance_version, state: 'mapped', provenance: 'user_edit', description: '已有对象原文', field_descriptions: [] }]
  if (options.legacy) kb.table_descriptions = [{ table_name: 'orders', description: '合法旧PG说明', field_descriptions: [{ field_name: 'id', description: '保留字段' }] }]
  // ProjectObjectKnowledge emits structural Effective rows even without persisted history.
  const project = () => options.legacy ? [] : objects.filter(object => object.is_selected).map(object => {
    const history = kb.table_descriptions.find(row => row.state === 'mapped' && row.object_id === object.object_id && row.object_instance_version === object.object_instance_version)
    return history ?? { table_name: object.name, object_id: object.object_id, object_instance_version: object.object_instance_version, state: 'mapped' as const,
      description: options.comments && object.object_id === view.object_id ? '数据库视图注释' : '', provenance: 'database_comment' as const,
      field_descriptions: options.comments && object.object_id === view.object_id ? [{ field_name: 'amount', description: '数据库列注释', provenance: 'database_comment' as const, state: 'mapped' as const, column_type: 'numeric' }] : [] }
  })
  kb.effective_descriptions = project()
  let revision = 1
  let conflict = false
  let kbGets = 0
  const writes: { method: string; path: string; body: unknown }[] = []
  await page.addInitScript(() => localStorage.setItem('token', 'fixture-token'))
  await installApiMock(page)
  await page.route('**/api/**', async route => {
    const request = route.request()
    const path = new URL(request.url()).pathname
    const method = request.method()
    if (method !== 'GET') writes.push({ method, path, body: request.postDataJSON() })
    if (path.endsWith('/chats')) return route.fulfill({ json: { success: true, data: { chats: [chat], total: 1 } } })
    if (path.endsWith('/chats/' + chat.id)) return route.fulfill({ json: { success: true, data: chat } })
    if (path.endsWith('/data-sync/status')) return route.fulfill({ json: { success: true, data: {
      schema_version: 1, source_id: 'locked-source', source_revision: 1, binding_revision: 1, display_name: '锁定源',
      readiness: { state: 'ready', reasons: [] }, can_start: false, ask_allowed: false, block_reason: 'DATA_SYNC_IN_PROGRESS', maintenance_state: 'syncing', active_job: null, latest_successful_sync_at: null,
    } } })
    if (path.endsWith('/tables')) return route.fulfill({ json: { success: true, data: { tables: objects, metadata_version: 2, selection_revision: 7, query_objects_enabled: !options.disabled } } })
    if (path.endsWith('/knowledge-base') || path.endsWith('/knowledge-base/mappings')) {
      if (method === 'GET') { kbGets++; return route.fulfill({ json: { success: true, data: { ...kb, saved: false } } }) }
      if (conflict) return route.fulfill({ status: 409, json: { success: false, error: 'KB_REVISION_CONFLICT' } })
      if (method === 'PUT') {
        const body = request.postDataJSON() as KnowledgeTextRequest
        if (options.legacy) kb.table_descriptions = body.table_descriptions.map(edit => ({ table_name: edit.table_name!, description: edit.description ?? '', field_descriptions: edit.field_descriptions ?? [] }))
        for (const edit of options.legacy ? [] : body.table_descriptions) {
          let row = edit.description_index === undefined ? kb.table_descriptions.find(row => row.object_id === edit.object_id) : kb.table_descriptions[edit.description_index]
          if (!row) {
            const object = objects.find(object => object.object_id === edit.object_id)!
            row = { table_name: object.name, object_id: object.object_id, object_instance_version: object.object_instance_version, description: '', field_descriptions: [], state: 'mapped', provenance: 'user_edit' }
            kb.table_descriptions.push(row)
          }
          if (edit.description !== undefined) row.description = edit.description
          for (const field of edit.field_descriptions ?? []) {
            const old = row.field_descriptions.find(old => old.field_name === field.field_name)
            if (old) old.description = field.description
            else row.field_descriptions.push({ ...field, provenance: 'user_edit', state: 'mapped', column_type: objects.find(object => object.object_id === row.object_id)?.columns.find(column => column.name === field.field_name)?.type })
          }
        }
      } else {
        const body = request.postDataJSON() as KnowledgeMappingsRequest
        for (const mapping of body.mappings) {
          const object = objects.find(object => object.object_id === mapping.object_id)!
          Object.assign(kb.table_descriptions[mapping.description_index], { object_id: object.object_id, object_instance_version: object.object_instance_version, state: 'mapped' })
        }
      }
      kb.effective_descriptions = project()
      kb.kb_revision = options.legacy ? undefined : 'kb-' + (++revision)
      return route.fulfill({ json: { success: true, data: { ...kb, saved: true } } })
    }
    return route.fallback()
  })
  // No API request or external provider traffic leaves the fixture.
  await page.route('**/*', route => new URL(route.request().url()).hostname === '127.0.0.1' ? route.fallback() : route.abort('blockedbyclient'))
  await page.goto('/chat/' + chat.id)
  return { kb, writes, kbGets: () => kbGets, conflict: (value: boolean) => { conflict = value } }
}
async function menu(page: Page) { await page.getByRole('button', { name: zhCN.chat.chatOptions, exact: true }).filter({ visible: true }).click() }
async function open(page: Page) {
  await menu(page)
  await page.getByRole('button', { name: '编辑知识库说明', exact: true }).click()
  await expect(page.getByRole('dialog', { name: '知识库说明' })).toBeVisible()
  await expect(page.getByRole('heading', { name: '历史说明原文', exact: true })).toBeVisible()
}
const knowledgeWrites = (writes: { path: string }[]) => writes.filter(write => write.path.includes('/knowledge-base'))

test('real chat entry saves raw history, reports stale index, and reopens saved text', async ({ page }) => {
  const fixture = await setup(page)
  await open(page)
  await expect(page.getByText('旧说明：来源未知', { exact: true })).toBeVisible()
  await expect(page.getByText('已映射，当前不可用', { exact: true })).toBeVisible()
  await page.getByRole('textbox', { name: '历史说明 1', exact: true }).fill('手工保留新原文')
  await page.getByRole('textbox', { name: '历史 1 字段 amount', exact: true }).fill('新金额说明')
  await page.getByRole('button', { name: '保存说明', exact: true }).click()
  await expect(page.getByText('说明已保存', { exact: true })).toBeVisible()
  await expect(page.getByText('索引待更新；需要刷新后才会用于检索。')).toBeVisible()
  expect(knowledgeWrites(fixture.writes)).toEqual([{ method: 'PUT', path: '/api/chats/chat-fixture/knowledge-base', body: {
    expected_kb_revision: 'kb-1', table_descriptions: [{ description_index: 0, description: '手工保留新原文', field_descriptions: [{ field_name: 'amount', description: '新金额说明' }] }],
  } }])
  await page.getByRole('button', { name: '关闭知识库', exact: true }).click()
  await open(page)
  await expect(page.getByRole('textbox', { name: '历史说明 1', exact: true })).toHaveValue('手工保留新原文')
  await expect(page.getByRole('textbox', { name: '历史 1 字段 amount', exact: true })).toHaveValue('新金额说明')
})

test('explicit two-row mapping sends one batch of current readable IDs', async ({ page }) => {
  const fixture = await setup(page)
  await open(page)
  await expect(page.getByRole('combobox', { name: '历史 1 映射目标' })).toHaveValue('')
  await expect(page.getByRole('option', { name: /不可选目录对象/ })).toHaveCount(0)
  await page.getByRole('checkbox', { name: '映射历史 1', exact: true }).check()
  await page.getByRole('checkbox', { name: '映射历史 2', exact: true }).check()
  await page.getByRole('combobox', { name: '历史 1 映射目标' }).selectOption('view-kb')
  await page.getByRole('combobox', { name: '历史 2 映射目标' }).selectOption('table-kb')
  expect(knowledgeWrites(fixture.writes)).toHaveLength(0)
  await page.getByRole('button', { name: '确认批量映射', exact: true }).click()
  await expect(page.getByText('映射已保存', { exact: true })).toBeVisible()
  expect(knowledgeWrites(fixture.writes)).toEqual([{ method: 'POST', path: '/api/chats/chat-fixture/knowledge-base/mappings', body: {
    expected_kb_revision: 'kb-1', mappings: [{ description_index: 0, object_id: 'view-kb' }, { description_index: 1, object_id: 'table-kb' }],
  } }])
  await expect(page.getByText('当前有效说明 · 旧说明：来源未知', { exact: true })).toBeVisible()
  await expect(page.getByRole('textbox', { name: '历史说明 3' })).toHaveValue('仍保留旧原文')
})

test('409 keeps draft through reload until explicit adoption, and retry uses latest revision', async ({ page }) => {
  const fixture = await setup(page)
  fixture.conflict(true)
  await open(page)
  await page.getByRole('textbox', { name: '历史说明 1', exact: true }).fill('不能丢失的草稿')
  await page.getByRole('button', { name: '保存说明', exact: true }).click()
  await expect(page.getByRole('alert')).toContainText('版本已变化')
  await expect(page.getByRole('button', { name: '保存说明', exact: true })).toBeDisabled()
  await expect(page.getByText('说明已保存', { exact: true })).toHaveCount(0)
  fixture.kb.kb_revision = 'kb-latest'; fixture.kb.table_descriptions[0].description = '服务器新文本'
  await page.getByRole('button', { name: '重新加载知识库' }).click()
  await expect(page.getByRole('button', { name: '采用服务器版本并丢弃本地修改' })).toBeVisible()
  await expect(page.getByRole('textbox', { name: '历史说明 1', exact: true })).toHaveValue('不能丢失的草稿')
  await page.getByRole('button', { name: '采用服务器版本并丢弃本地修改' }).click()
  await expect(page.getByRole('textbox', { name: '历史说明 1', exact: true })).toHaveValue('服务器新文本')
  fixture.conflict(false)
  await page.getByRole('textbox', { name: '历史说明 1', exact: true }).fill('重新编辑确认')
  await page.getByRole('button', { name: '保存说明', exact: true }).click()
  await expect(page.getByText('说明已保存', { exact: true })).toBeVisible()
  expect((fixture.writes.at(-1)!.body as KnowledgeTextRequest).expected_kb_revision).toBe('kb-latest')
})

test('complex names and manual fields send only object ID and text', async ({ page }) => {
  const fixture = await setup(page)
  await open(page)
  await page.getByRole('textbox', { name: '新对象说明 ' + view.name, exact: true }).fill('新视图手工说明')
  await page.getByRole('textbox', { name: '新对象 ' + view.name + ' 字段 amount', exact: true }).fill('金额解释')
  await page.getByRole('button', { name: '保存说明', exact: true }).click()
  await expect(page.getByText('说明已保存', { exact: true })).toBeVisible()
  expect(knowledgeWrites(fixture.writes)).toEqual([{ method: 'PUT', path: '/api/chats/chat-fixture/knowledge-base', body: {
    expected_kb_revision: 'kb-1', table_descriptions: [{ object_id: 'view-kb', description: '新视图手工说明', field_descriptions: [{ field_name: 'amount', description: '金额解释' }] }],
  } }])
})

test('disabled v2 keeps history and offers reload without changing selection', async ({ page }) => {
  const fixture = await setup(page, { disabled: true })
  await open(page)
  await expect(page.getByRole('alert')).toContainText('未启用')
  await expect(page.getByRole('button', { name: '保存说明', exact: true })).toBeDisabled()
  await expect(page.getByRole('button', { name: '确认批量映射', exact: true })).toBeDisabled()
  await expect(page.getByRole('button', { name: '重新加载知识库' })).toBeEnabled()
  expect(fixture.writes.filter(write => !write.path.endsWith('/connect'))).toEqual([])
})

test('locked source disables the real chat entry without a KB request', async ({ page }) => {
  const fixture = await setup(page, { locked: true })
  await menu(page)
  await expect(page.getByRole('button', { name: '编辑知识库说明' })).toBeDisabled()
  expect(fixture.kbGets()).toBe(0)
  expect(knowledgeWrites(fixture.writes)).toEqual([])
})

for (const comments of [false, true]) {
  test('B1 structural Effective without history allows object and field text (comments=' + comments + ')', async ({ page }) => {
    const fixture = await setup(page, { history: 'empty', comments })
    await open(page)
    await expect(page.getByText('暂无历史说明。')).toBeVisible()
    if (comments) {
      await expect(page.getByText('数据库视图注释', { exact: true })).toBeVisible()
      await expect(page.getByText('amount：数据库列注释', { exact: true })).toBeVisible()
    }
    await page.getByRole('textbox', { name: '新对象说明 ' + view.name, exact: true }).fill('新的人工说明')
    await page.getByRole('textbox', { name: '新对象 ' + view.name + ' 字段 amount', exact: true }).fill('新的列说明')
    await page.getByRole('button', { name: '保存说明', exact: true }).click()
    await expect(page.getByText('说明已保存', { exact: true })).toBeVisible()
    expect(knowledgeWrites(fixture.writes)).toEqual([{ method: 'PUT', path: '/api/chats/chat-fixture/knowledge-base', body: {
      expected_kb_revision: 'kb-1', table_descriptions: [{ object_id: view.object_id, description: '新的人工说明', field_descriptions: [{ field_name: 'amount', description: '新的列说明' }] }],
    } }])
    await page.getByRole('button', { name: '关闭知识库' }).click()
    await open(page)
    await expect(page.getByRole('textbox', { name: '历史说明 1', exact: true })).toHaveValue('新的人工说明')
    await expect(page.getByRole('textbox', { name: '历史 1 字段 amount', exact: true })).toHaveValue('新的列说明')
  })
}
test('B1 current object history still allows adding a missing visible column without replacing object text', async ({ page }) => {
  const fixture = await setup(page, { history: 'object-only' })
  await open(page)
  await expect(page.getByRole('textbox', { name: '新对象说明 ' + view.name, exact: true })).toHaveCount(0)
  await page.getByRole('textbox', { name: '新对象 ' + view.name + ' 字段 amount', exact: true }).fill('后补列说明')
  await page.getByRole('button', { name: '保存说明', exact: true }).click()
  await expect(page.getByText('说明已保存', { exact: true })).toBeVisible()
  expect(knowledgeWrites(fixture.writes)).toEqual([{ method: 'PUT', path: '/api/chats/chat-fixture/knowledge-base', body: {
    expected_kb_revision: 'kb-1', table_descriptions: [{ object_id: view.object_id, field_descriptions: [{ field_name: 'amount', description: '后补列说明' }] }],
  } }])
  await expect(page.getByRole('textbox', { name: '历史说明 1', exact: true })).toHaveValue('已有对象原文')
  await expect(page.getByRole('textbox', { name: '历史 1 字段 amount', exact: true })).toHaveValue('后补列说明')
})
test('B2 legacy PostgreSQL false capability remains editable from the real chat menu', async ({ page }) => {
  const fixture = await setup(page, { legacy: true })
  await open(page)
  await page.getByRole('textbox', { name: '历史说明 1', exact: true }).fill('旧PG新原文')
  await page.getByRole('button', { name: '保存说明', exact: true }).click()
  await expect(page.getByText('说明已保存', { exact: true })).toBeVisible()
  expect(knowledgeWrites(fixture.writes)).toEqual([{ method: 'PUT', path: '/api/chats/chat-fixture/knowledge-base', body: {
    table_descriptions: [{ table_name: 'orders', description: '旧PG新原文', field_descriptions: [{ field_name: 'id', description: '保留字段' }] }],
  } }])
  await expect(page.getByRole('button', { name: '确认批量映射' })).toBeDisabled()
})
