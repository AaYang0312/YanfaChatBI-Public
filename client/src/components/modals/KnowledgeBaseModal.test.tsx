import { act, cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { afterEach, beforeEach, expect, test } from 'vitest'
import { AxiosError, type AxiosAdapter } from 'axios'
import axios from '../../services/axiosConfig'
import KnowledgeBaseModal from './KnowledgeBaseModal'
import ChatHeader from '../chat/ChatHeader'
import { UserProvider } from '../../contexts/UserProvider'
import { zhCN } from '../../i18n/zh-CN'
import { SourceAccessContext, SourceAccessCoordinator, installSourceAccess } from '../../contexts/SourceAccessContext'
import type { Chat, TableInfo } from '../../types/chat'
import type { TableDescription } from '../../types/knowledgeBase'
import type { ObjectKnowledgeResponse } from '../../types/objectKnowledge'

const chat: Chat = { id: 'kb-a', user_id: 'owner', source_classification: 'independent',
  connection: { type: 'postgresql', host: 'fake.invalid', port: '5432', username: 'reader', database: 'fixture', is_example_db: false },
  selected_objects: { selection_version: 2, mode: 'all', include_kinds: ['table', 'view'] }, selection_revision: 3,
  settings: { auto_execute_query: false, non_tech_mode: false, auto_generate_visualization: false }, created_at: '', updated_at: '' }
const secondChat = { ...chat, id: 'kb-b' }
const view: TableInfo = { object_id: 'view-id', object_instance_version: 'v1', schema_name: 'a.b', object_name: 'sales,x', object_type: 'view', name: '"a.b"."sales,x"', sql_identifier: '"a.b"."sales,x"', is_selected: true, row_count: null, row_count_status: 'unknown', columns: [{ name: 'amount', type: 'numeric', is_nullable: false, nullable: null }] }
const table: TableInfo = { ...view, object_id: 'table-id', object_type: 'table', schema_name: 'public', object_name: 'orders', name: '"public"."orders"' }
const rows = [
  { table_name: '历史甲', description: '原始说明甲', field_descriptions: [{ field_name: 'amount', description: '原始金额', provenance: 'legacy_unknown' }], provenance: 'legacy_unknown', state: 'pending' },
  { table_name: '历史乙', description: '原始说明乙', field_descriptions: [], provenance: 'ai_generated', state: 'pending' },
  { table_name: '不可读旧对象', description: '仍需保留的原文', field_descriptions: [], object_id: 'hidden-id', object_instance_version: 'old', provenance: 'user_edit', state: 'mapped', object_scope: { source_key: 'never-send' } },
] satisfies (TableDescription & { object_scope?: { source_key: string } })[]
const base: ObjectKnowledgeResponse = { chat_id: chat.id, table_descriptions: rows, effective_descriptions: [], query_objects_enabled: true, kb_revision: 'kb-1', index_status: 'stale', saved: false, refresh_required: true, created_at: '', updated_at: '' }
let body: typeof base
let catalog: TableInfo[]
let requests: { method?: string; url: string; body: Record<string, unknown> }[]
let coordinator: SourceAccessCoordinator
let stop: () => void
let originalAdapter: typeof axios.defaults.adapter
let conflict: boolean
let delayedGet: (() => Promise<void>) | undefined
let delayedSave: (() => Promise<void>) | undefined
let saved: boolean
let directoryRevision: number
let partialReceipt: boolean
let getConflict: boolean
const adapter: AxiosAdapter = async config => {
  const requestBody = typeof config.data === 'string' ? JSON.parse(config.data) : config.data || {}
  const url = config.url!
  requests.push({ method: config.method, url, body: requestBody })
  if (getConflict && config.method === 'get' && url.endsWith('/knowledge-base')) throw new AxiosError('v2 document disabled', 'ERR_BAD_REQUEST', config, undefined, { status: 409, statusText: 'Conflict', headers: {}, config, data: { success: false, error: 'QUERY_OBJECTS_DISABLED' } })
  const other = url.includes('/kb-b/')
  let data: unknown = structuredClone(other ? { ...base, chat_id: 'kb-b', kb_revision: 'other-1', table_descriptions: [{ ...rows[0], table_name: 'B 历史', description: 'B 原文' }] } : body)
  if (url.endsWith('/tables')) data = { metadata_version: 2, query_objects_enabled: true, selection_revision: directoryRevision, tables: catalog }
  else if (config.method === 'get' && delayedGet && !other) await delayedGet()
  else if (config.method !== 'get') {
    if (delayedSave) await delayedSave()
    if (conflict) throw new AxiosError('conflict', 'ERR_BAD_REQUEST', config, undefined, { status: 409, statusText: 'Conflict', headers: {}, config, data: { success: false, error: 'KB_REVISION_CONFLICT' } })
    data = partialReceipt ? { chat_id: chat.id, query_objects_enabled: true, saved: true, refresh_required: true, index_status: 'invalidation_failed' } : { ...body, kb_revision: 'kb-2', saved, index_status: 'stale', refresh_required: true }
  }
  return { status: 200, statusText: 'OK', headers: {}, config, data: { success: true, data } }
}
function mount(id = chat.id) {
  return render(<SourceAccessContext.Provider value={coordinator}><KnowledgeBaseModal chatId={id} onClose={() => {}} /></SourceAccessContext.Provider>)
}
const mutations = () => requests.filter(request => request.method !== 'get')
async function textField(name = '历史说明 1') { return screen.findByRole('textbox', { name }) }
beforeEach(() => {
  body = structuredClone(base); catalog = [view, table, { ...view, object_id: 'unselected', name: '未选择对象', is_selected: false }]
  getConflict = false; partialReceipt = false; directoryRevision = 3; requests = []; conflict = false; delayedGet = undefined; delayedSave = undefined; saved = true
  originalAdapter = axios.defaults.adapter; axios.defaults.adapter = adapter
  coordinator = new SourceAccessCoordinator(); coordinator.configure('owner', chat, [chat, secondChat]); stop = installSourceAccess(coordinator)
})
afterEach(() => { cleanup(); stop(); axios.defaults.adapter = originalAdapter })

test('history stays verbatim and unmapped with provenance; unreadable mapped history remains editable', async () => {
  mount()
  expect(await textField()).toHaveValue('原始说明甲')
  expect(await textField('历史说明 3')).toHaveValue('仍需保留的原文')
  expect(screen.getByText('旧说明：来源未知')).toBeInTheDocument()
  expect(screen.getByText('AI 生成说明')).toBeInTheDocument()
  expect(screen.getByText('已映射，当前不可用')).toBeInTheDocument()
  expect(mutations()).toHaveLength(0)
})
test('history and field text save sends only explicit patch identities and edited text', async () => {
  mount(); fireEvent.change(await textField(), { target: { value: '用户新说明' } })
  fireEvent.change(await textField('历史 1 字段 amount'), { target: { value: '用户新金额' } })
  fireEvent.click(screen.getByRole('button', { name: '保存说明' }))
  await screen.findByText('说明已保存')
  expect(mutations()[0].body).toEqual({ expected_kb_revision: 'kb-1', table_descriptions: [{ description_index: 0, description: '用户新说明', field_descriptions: [{ field_name: 'amount', description: '用户新金额' }] }] })
  expect(screen.getByText('索引待更新；需要刷新后才会用于检索。')).toBeInTheDocument()
})
test('multi-row mapping uses selected readable object IDs and sends one explicit batch confirmation', async () => {
  mount(); await textField()
  const first = screen.getByRole('combobox', { name: '历史 1 映射目标' })
  expect(within(first).queryByRole('option', { name: /未选择对象/ })).not.toBeInTheDocument()
  expect(first).toHaveValue('')
  fireEvent.click(screen.getByRole('checkbox', { name: '映射历史 1' }))
  fireEvent.click(screen.getByRole('checkbox', { name: '映射历史 2' }))
  fireEvent.change(first, { target: { value: 'view-id' } })
  fireEvent.change(screen.getByRole('combobox', { name: '历史 2 映射目标' }), { target: { value: 'table-id' } })
  expect(mutations()).toHaveLength(0)
  fireEvent.click(screen.getByRole('button', { name: '确认批量映射' }))
  await screen.findByText('映射已保存')
  expect(mutations()).toEqual([{ method: 'post', url: expect.stringMatching(/\/chats\/kb-a\/knowledge-base\/mappings$/), body: { expected_kb_revision: 'kb-1', mappings: [{ description_index: 0, object_id: 'view-id' }, { description_index: 1, object_id: 'table-id' }] } }])
})
test('new manual object text uses object_id without copying identity evidence', async () => {
  mount()
  fireEvent.change(await textField('新对象说明 ' + view.name), { target: { value: '视图手工说明' } })
  fireEvent.click(screen.getByRole('button', { name: '保存说明' }))
  await screen.findByText('说明已保存')
  expect(mutations()[0].body).toEqual({ expected_kb_revision: 'kb-1', table_descriptions: [{ object_id: 'view-id', description: '视图手工说明' }] })
})
test('409 blocks retry, reload preserves local draft until explicitly adopting the server version', async () => {
  conflict = true; mount()
  fireEvent.change(await textField(), { target: { value: '未保存草稿' } })
  fireEvent.click(screen.getByRole('button', { name: '保存说明' }))
  await screen.findByText(/版本已变化/)
  expect(await textField()).toHaveValue('未保存草稿')
  expect(screen.getByRole('button', { name: '保存说明' })).toBeDisabled()
  expect(screen.queryByText('说明已保存')).not.toBeInTheDocument()
  body = { ...body, kb_revision: 'kb-latest', table_descriptions: [{ ...rows[0], description: '服务器新版本' }, ...rows.slice(1)] }
  fireEvent.click(screen.getByRole('button', { name: '重新加载知识库' }))
  await screen.findByRole('button', { name: '采用服务器版本并丢弃本地修改' })
  expect(await textField()).toHaveValue('未保存草稿')
  fireEvent.click(screen.getByRole('button', { name: '采用服务器版本并丢弃本地修改' }))
  expect(await textField()).toHaveValue('服务器新版本')
  conflict = false
  fireEvent.change(await textField(), { target: { value: '明确重新编辑' } })
  fireEvent.click(screen.getByRole('button', { name: '保存说明' }))
  await screen.findByText('说明已保存')
  expect(mutations()[1].body.expected_kb_revision).toBe('kb-latest')
})
test('disabled v2 is recoverable and keeps history without any selection write', async () => {
  body.query_objects_enabled = false
  mount(); expect(await textField()).toHaveValue('原始说明甲')
  expect(screen.getByRole('alert')).toHaveTextContent(/未启用/)
  expect(screen.getByRole('button', { name: '保存说明' })).toBeDisabled()
  expect(screen.getByRole('button', { name: '确认批量映射' })).toBeDisabled()
  expect(mutations()).toHaveLength(0)
})
test('late metadata from another chat never replaces the current editor', async () => {
  let resolve!: () => void; delayedGet = () => new Promise<void>(yes => { resolve = yes })
  const modal = mount()
  await waitFor(() => expect(requests.some(request => request.url.endsWith('/kb-a/knowledge-base'))).toBe(true))
  act(() => coordinator.select('kb-b'))
  modal.rerender(<SourceAccessContext.Provider value={coordinator}><KnowledgeBaseModal chatId="kb-b" onClose={() => {}} /></SourceAccessContext.Provider>)
  expect(await textField()).toHaveValue('B 原文')
  await act(async () => { resolve() })
  expect(await textField()).toHaveValue('B 原文')
  expect(screen.queryByDisplayValue('原始说明甲')).not.toBeInTheDocument()
})
test('successful HTTP envelope without saved confirmation cannot report a saved edit', async () => {
  saved = false; mount()
  fireEvent.change(await textField(), { target: { value: '未确认草稿' } })
  fireEvent.click(screen.getByRole('button', { name: '保存说明' }))
  await screen.findByText(/未确认保存/)
  expect(screen.queryByText('说明已保存')).not.toBeInTheDocument()
  expect(await textField()).toHaveValue('未确认草稿')
})


test('unreadable mapped history can save text by history index without mapping evidence', async () => {
  mount(); fireEvent.change(await textField('历史说明 3'), { target: { value: '仍保留且可编辑' } })
  fireEvent.click(screen.getByRole('button', { name: '保存说明' }))
  await screen.findByText('说明已保存')
  expect(mutations()[0].body).toEqual({ expected_kb_revision: 'kb-1', table_descriptions: [{ description_index: 2, description: '仍保留且可编辑' }] })
})
test('selection changes remove stale mapping targets and require reload before mutation', async () => {
  mount(); await textField()
  act(() => { coordinator.register({ ...chat, selection_revision: 4 }); coordinator.configure('owner', { ...chat, selection_revision: 4 }, [{ ...chat, selection_revision: 4 }]) })
  const targets = screen.getByRole('combobox', { name: '历史 1 映射目标' })
  expect(within(targets).queryByRole('option', { name: /sales,x/ })).not.toBeInTheDocument()
  expect(screen.getByRole('button', { name: '确认批量映射' })).toBeDisabled()
  expect(mutations()).toHaveLength(0)
})
test('legacy save preserves unedited rows and fields while dropping response-only evidence', async () => {
  body = { chat_id: chat.id, table_descriptions: structuredClone(rows), created_at: '', updated_at: '' } as typeof base
  const legacy = { ...chat, user_id: 'legacy-owner', selected_objects: undefined }
  coordinator.configure('legacy-owner', legacy, [legacy])
  mount(); fireEvent.change(await textField(), { target: { value: '旧接口修改' } })
  fireEvent.click(screen.getByRole('button', { name: '保存说明' }))
  await screen.findByText('说明已保存')
  expect(mutations()[0].body).toEqual({ table_descriptions: [
    { table_name: '历史甲', description: '旧接口修改', field_descriptions: [{ field_name: 'amount', description: '原始金额' }] },
    { table_name: '历史乙', description: '原始说明乙', field_descriptions: [] },
    { table_name: '不可读旧对象', description: '仍需保留的原文', field_descriptions: [] },
  ] })
})
test('mapping conflict does not report success or silently adopt a newer revision', async () => {
  conflict = true; mount(); await textField()
  fireEvent.click(screen.getByRole('checkbox', { name: '映射历史 1' }))
  fireEvent.change(screen.getByRole('combobox', { name: '历史 1 映射目标' }), { target: { value: 'view-id' } })
  fireEvent.click(screen.getByRole('button', { name: '确认批量映射' }))
  await screen.findByText(/版本已变化/)
  expect(screen.queryByText('映射已保存')).not.toBeInTheDocument()
  expect(screen.getByRole('combobox', { name: '历史 1 映射目标' })).toHaveValue('view-id')
  expect(screen.getByRole('button', { name: '确认批量映射' })).toBeDisabled()
})
test('a late save cannot show success or old text in a different chat', async () => {
  let release!: () => void
  delayedSave = () => new Promise<void>(resolve => { release = resolve })
  const modal = mount()
  fireEvent.change(await textField(), { target: { value: 'A 草稿' } })
  fireEvent.click(screen.getByRole('button', { name: '保存说明' }))
  await waitFor(() => expect(mutations()).toHaveLength(1))
  act(() => coordinator.select('kb-b'))
  modal.rerender(<SourceAccessContext.Provider value={coordinator}><KnowledgeBaseModal chatId="kb-b" onClose={() => {}} /></SourceAccessContext.Provider>)
  expect(await textField()).toHaveValue('B 原文')
  await act(async () => release())
  expect(screen.queryByText('说明已保存')).not.toBeInTheDocument()
  expect(screen.queryByDisplayValue('A 草稿')).not.toBeInTheDocument()
})


function mountHeader(value = chat) {
  return render(<UserProvider><SourceAccessContext.Provider value={coordinator}><ChatHeader chat={value} isConnected isConnecting={false}
    onClearChat={() => {}} onEditConnection={() => {}} onShowCloseConfirm={() => {}} onReconnect={() => {}}
    setShowRefreshSchema={() => {}} onToggleSearch={() => {}} onOpenDataSync={() => {}} syncPending={false} /></SourceAccessContext.Provider></UserProvider>)
}
test('real chat header menu opens the knowledge editor and closes it without a schema refresh', async () => {
  mountHeader()
  fireEvent.click(screen.getAllByRole('button', { name: zhCN.chat.chatOptions })[0])
  fireEvent.click(screen.getByRole('button', { name: '编辑知识库说明' }))
  expect(await textField()).toHaveValue('原始说明甲')
  expect(screen.getByRole('dialog', { name: '知识库说明' })).toBeInTheDocument()
  fireEvent.click(screen.getByRole('button', { name: '关闭知识库' }))
  expect(screen.queryByRole('dialog', { name: '知识库说明' })).not.toBeInTheDocument()
  expect(mutations()).toHaveLength(0)
})
test('real chat header disables knowledge entry when source authority is locked', () => {
  const locked: Chat = { ...chat, source_classification: 'registered_erp', source_id: 'locked', source_binding_revision: 1 }
  coordinator.configure('owner', locked, [locked])
  mountHeader(locked)
  fireEvent.click(screen.getAllByRole('button', { name: zhCN.chat.chatOptions })[0])
  const entry = screen.getByRole('button', { name: '编辑知识库说明' })
  expect(entry).toBeDisabled()
  expect(entry).toHaveAttribute('title', coordinator.view(chat.id).reason)
  fireEvent.click(entry)
  expect(screen.queryByRole('dialog', { name: '知识库说明' })).not.toBeInTheDocument()
  expect(requests).toHaveLength(0)
})

test('a stale object directory cannot offer mapping targets while history remains editable', async () => {
  directoryRevision = 2
  mount(); expect(await textField()).toHaveValue('原始说明甲')
  expect(screen.getByRole('combobox', { name: '历史 1 映射目标' })).toBeDisabled()
  expect(screen.getByRole('button', { name: '确认批量映射' })).toBeDisabled()
  expect(screen.queryByRole('option', { name: /sales,x/ })).not.toBeInTheDocument()
  fireEvent.change(await textField(), { target: { value: '保留原文编辑' } })
  expect(screen.getByRole('button', { name: '保存说明' })).toBeEnabled()
})
test('wire null field lists still allow historical text to be displayed and saved', async () => {
  body.table_descriptions[0].field_descriptions = null as unknown as typeof rows[0]['field_descriptions']
  mount(); fireEvent.change(await textField(), { target: { value: '无字段说明仍可编辑' } })
  fireEvent.click(screen.getByRole('button', { name: '保存说明' }))
  await screen.findByText('说明已保存')
  expect(mutations()[0].body).toEqual({ expected_kb_revision: 'kb-1', table_descriptions: [{ description_index: 0, description: '无字段说明仍可编辑' }] })
})

test('non-PostgreSQL legacy text save remains available when object capability is false', async () => {
  body.query_objects_enabled = false
  const legacy: Chat = { ...chat, user_id: 'legacy-owner', selected_objects: undefined, connection: { ...chat.connection, type: 'mysql' } }
  coordinator.configure('legacy-owner', legacy, [legacy])
  mount(); fireEvent.change(await textField(), { target: { value: '旧数据库说明' } })
  fireEvent.click(screen.getByRole('button', { name: '保存说明' }))
  await screen.findByText('说明已保存')
  expect(mutations()[0].body).not.toHaveProperty('expected_kb_revision')
  expect(mutations()[0].body.table_descriptions).toHaveLength(3)
  expect(screen.getByRole('button', { name: '确认批量映射' })).toBeDisabled()
})
test('saved receipt with invalidation failure distinguishes persistence from readiness and retains the draft', async () => {
  partialReceipt = true
  mount(); fireEvent.change(await textField(), { target: { value: '已写入但尚待确认的草稿' } })
  fireEvent.click(screen.getByRole('button', { name: '保存说明' }))
  await screen.findByText('更改已写入；索引尚未确认，需要重新加载。')
  expect(await textField()).toHaveValue('已写入但尚待确认的草稿')
  expect(screen.getByRole('button', { name: '保存说明' })).toBeDisabled()
  expect(screen.getByRole('button', { name: '重新加载知识库' })).toBeEnabled()
  expect(screen.queryByText('索引已就绪。')).not.toBeInTheDocument()
})

const projected = (comment = false): TableDescription => ({
  table_name: view.name, object_id: view.object_id, object_instance_version: view.object_instance_version,
  state: 'mapped', description: comment ? '数据库视图注释' : '', provenance: comment ? 'database_comment' : undefined,
  field_descriptions: comment ? [{ field_name: 'amount', description: '数据库列注释', provenance: 'database_comment' }] : [],
})
function setLegacyAuthority() {
  const legacy: Chat = { ...chat, selected_objects: undefined, selected_collections: 'orders' }
  stop()
  coordinator = new SourceAccessCoordinator()
  coordinator.configure('owner', legacy, [legacy])
  stop = installSourceAccess(coordinator)
}
test.each([false, true])('B1 structural projection keeps manual object and column inputs (DB comments=%s)', async comment => {
  body.table_descriptions = []; body.effective_descriptions = [projected(comment)]
  mount()
  const editor = await textField('新对象说明 ' + view.name)
  expect(editor).toHaveValue('')
  if (comment) {
    expect(screen.getByText('数据库视图注释')).toBeInTheDocument()
    expect(screen.getByText('amount：数据库列注释')).toBeInTheDocument()
  }
  fireEvent.change(editor, { target: { value: '人工对象说明' } })
  fireEvent.change(await textField('新对象 ' + view.name + ' 字段 amount'), { target: { value: '人工金额' } })
  fireEvent.click(screen.getByRole('button', { name: '保存说明' }))
  await screen.findByText('说明已保存')
  expect(mutations()[0].body).toEqual({ expected_kb_revision: 'kb-1', table_descriptions: [{ object_id: 'view-id', description: '人工对象说明', field_descriptions: [{ field_name: 'amount', description: '人工金额' }] }] })
})
test('B1 current object history without a newly visible column permits a field-only manual patch', async () => {
  const history: TableDescription = { ...projected(), description: '已有对象说明', provenance: 'user_edit', field_descriptions: [] }
  body.table_descriptions = [history]; body.effective_descriptions = [history]
  mount(); await textField()
  expect(screen.queryByRole('textbox', { name: '新对象说明 ' + view.name })).not.toBeInTheDocument()
  fireEvent.change(await textField('新对象 ' + view.name + ' 字段 amount'), { target: { value: '新增列说明' } })
  fireEvent.click(screen.getByRole('button', { name: '保存说明' }))
  await screen.findByText('说明已保存')
  expect(mutations()[0].body).toEqual({ expected_kb_revision: 'kb-1', table_descriptions: [{ object_id: 'view-id', field_descriptions: [{ field_name: 'amount', description: '新增列说明' }] }] })
})
test.each(['pending', 'replaced'] as const)('B1 %s history cannot hide current object inputs or be automatically rebound', async kind => {
  body.table_descriptions = [{ ...projected(), description: '历史原文', state: kind === 'pending' ? 'pending' : 'mapped', object_instance_version: kind === 'replaced' ? 'old-instance' : view.object_instance_version }]
  body.effective_descriptions = [projected()]
  mount()
  fireEvent.change(await textField('新对象说明 ' + view.name), { target: { value: '当前人工说明' } })
  expect(await textField()).toHaveValue('历史原文')
  fireEvent.click(screen.getByRole('button', { name: '保存说明' }))
  await screen.findByText('说明已保存')
  expect(mutations()[0].body).toEqual({ expected_kb_revision: 'kb-1', table_descriptions: [{ object_id: 'view-id', description: '当前人工说明' }] })
})
test('B2 a genuinely legacy PG authority accepts explicit false and preserves other history fields', async () => {
  setLegacyAuthority()
  body = { chat_id: chat.id, query_objects_enabled: false, saved: false, index_status: 'not_checked', created_at: '', updated_at: '', table_descriptions: [{ table_name: 'orders', description: '旧PG说明', field_descriptions: [{ field_name: 'id', description: '保留字段' }] }] }
  expect(coordinator.view(chat.id).chat?.selected_objects).toBeUndefined()
  mount(); fireEvent.change(await textField(), { target: { value: '修改旧PG说明' } })
  fireEvent.click(screen.getByRole('button', { name: '保存说明' }))
  await screen.findByText('说明已保存')
  expect(mutations()[0].body).toEqual({ table_descriptions: [{ table_name: 'orders', description: '修改旧PG说明', field_descriptions: [{ field_name: 'id', description: '保留字段' }] }] })
})
test('B2 an observed v2 KB cannot downgrade after a false response without a v2 chat selection', async () => {
  setLegacyAuthority()
  mount(); await textField()
  body = { chat_id: chat.id, query_objects_enabled: false, saved: false, index_status: 'not_checked', created_at: '', updated_at: '', table_descriptions: [rows[0]] }
  fireEvent.click(screen.getByRole('button', { name: '重新加载知识库' }))
  await waitFor(() => expect(screen.getByRole('button', { name: '重新加载知识库' })).toBeEnabled())
  fireEvent.change(await textField(), { target: { value: '仍应禁止降级' } })
  expect(screen.getByRole('button', { name: '保存说明' })).toBeDisabled()
  expect(mutations()).toHaveLength(0)
})
test('B2 disabled v2 document GET 409 never becomes an editable legacy snapshot', async () => {
  setLegacyAuthority(); getConflict = true
  mount()
  await screen.findByText(/版本已变化/)
  expect(screen.getByRole('button', { name: '保存说明' })).toBeDisabled()
  expect(screen.queryByRole('textbox', { name: '历史说明 1' })).not.toBeInTheDocument()
  expect(mutations()).toHaveLength(0)
})
test('B2 stale legacy props cannot erase v2 source authority', async () => {
  body.query_objects_enabled = false
  const legacy = { ...chat, selected_objects: undefined }
  expect(coordinator.register(legacy)).toBe(false)
  expect(coordinator.view(chat.id).chat?.selected_objects).toEqual(chat.selected_objects)
  mountHeader(legacy); fireEvent.click(screen.getAllByRole('button', { name: zhCN.chat.chatOptions })[0])
  fireEvent.click(screen.getByRole('button', { name: '编辑知识库说明' }))
  fireEvent.change(await textField(), { target: { value: '不可降级原文' } })
  expect(screen.getByRole('button', { name: '保存说明' })).toBeDisabled()
  expect(mutations()).toHaveLength(0)
})

test('B2 a legacy snapshot becomes fail-closed when a reload reports a migrated v2 document', async () => {
  setLegacyAuthority()
  body = { chat_id: chat.id, table_descriptions: [rows[0]], created_at: '', updated_at: '' }
  mount(); fireEvent.change(await textField(), { target: { value: '未保存原文' } })
  getConflict = true
  fireEvent.click(screen.getByRole('button', { name: '重新加载知识库' }))
  await screen.findByText(/版本已变化/)
  expect(await textField()).toHaveValue('未保存原文')
  expect(screen.getByRole('button', { name: '保存说明' })).toBeDisabled()
  expect(mutations()).toHaveLength(0)
})
