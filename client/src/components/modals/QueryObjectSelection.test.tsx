import { act, cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, expect, test } from 'vitest'
import { AxiosError, type AxiosAdapter } from 'axios'
import axios from '../../services/axiosConfig'
import chatService from '../../services/chatService'
import SelectTablesModal from './SelectTablesModal'
import ConnectionModal from './ConnectionModal'
import { StreamContext } from '../../contexts/StreamContext'
import { SourceAccessContext, SourceAccessCoordinator, installSourceAccess } from '../../contexts/SourceAccessContext'
import type { Chat } from '../../types/chat'
import { zhCN } from '../../i18n/zh-CN'

const table = { name: '"public"."orders"', object_id: 'id-table', object_instance_version: 'i1', schema_name: 'public', object_name: 'orders', object_type: 'table', sql_identifier: '"public"."orders"', is_selected: true, row_count: null, row_count_status: 'unknown', partial_columns: false, columns: [{ name: 'id', type: 'integer', is_nullable: false, nullable: null, ordinal: 1, comment: '' }] }
const view = { ...table, name: '"reporting"."sales"', object_id: 'id-view', schema_name: 'reporting', object_name: 'sales', object_type: 'view', sql_identifier: '"reporting"."sales"', is_selected: false, partial_columns: true }
const viewSelection = { selection_version: 2, mode: 'explicit', objects: [{ schema: 'reporting', name: 'sales', kind: 'view' }] }
let chat: Chat
let directory: Record<string, unknown>
let requests: { method?: string; url?: string; body: Record<string, unknown> }[]
let rejectSave: boolean
let coordinator: SourceAccessCoordinator
let uninstall: () => void
let originalAdapter: typeof axios.defaults.adapter
let pendingPatch: (() => Promise<void>) | undefined
let closed: boolean
const adapter: AxiosAdapter = async config => {
  const body = typeof config.data === 'string' ? JSON.parse(config.data) : config.data || {}
  requests.push({ method: config.method, url: config.url, body })
  let data: unknown = chat
  if (config.url?.endsWith('/tables')) data = directory
  else if (config.method === 'patch') {
    if (pendingPatch) await pendingPatch()
    if (rejectSave) throw new AxiosError('Conflict', 'ERR_BAD_REQUEST', config, undefined, { status: 409, statusText: 'Conflict', data: { success: false, error: 'SELECTION_REVISION_CONFLICT' }, headers: {}, config })
    chat = { ...chat, ...body, selection_revision: 8, query_object_index: null } as Chat
    directory = { ...directory, selected_objects: body.selected_objects, selection_revision: 8 }
    data = chat
  }
  return { status: 200, statusText: 'OK', data: { success: true, data }, headers: {}, config }
}
function mount(connection = false) {
  return render(<SourceAccessContext.Provider value={coordinator}>
    <StreamContext.Provider value={{ streamId: 'fixture', setStreamId: () => {}, generateStreamId: () => 'fixture' }}>
      {connection ? <ConnectionModal initialData={chat} initialTab="schema" onClose={() => { closed = true }} onSubmit={async () => ({ success: false })}
        onUpdateSelectedCollections={async (id, value) => { await chatService.updateSelectedCollections(id, value) }} /> :
        <SelectTablesModal chat={chat} onClose={() => { closed = true }} onSave={async value => { await chatService.updateSelectedCollections(chat.id, value) }} />}
    </StreamContext.Provider>
  </SourceAccessContext.Provider>)
}
async function loaded(name = table.name) { return screen.findByRole('checkbox', { name }) }
async function save(connection = false) {
  fireEvent.click(screen.getByRole('button', { name: connection ? zhCN.connection.updateKnowledge : zhCN.schema.saveApply }))
  await waitFor(() => expect(requests.some(r => r.method === 'patch')).toBe(true))
}
const patches = () => requests.filter(r => r.method === 'patch')
beforeEach(() => {
  chat = { id: 'objects', user_id: 'owner', source_classification: 'independent', connection: { type: 'postgresql', host: 'fake.invalid', port: '5432', database: 'fixture', username: 'reader', is_example_db: false }, selected_collections: 'ALL', settings: { auto_execute_query: false, non_tech_mode: false, auto_generate_visualization: false }, created_at: '2020-01-01', updated_at: '2020-01-01' }
  directory = { tables: [table, view], metadata_version: 2, selection_revision: 7, query_objects_enabled: true }
  requests = []; rejectSave = false; pendingPatch = undefined; closed = false
  originalAdapter = axios.defaults.adapter; axios.defaults.adapter = adapter
  coordinator = new SourceAccessCoordinator(); coordinator.configure('owner', chat, [chat]); uninstall = installSourceAccess(coordinator)
})
afterEach(() => { cleanup(); uninstall(); axios.defaults.adapter = originalAdapter })

test('pure view saves exact v2 scope and reopens with its selection', async () => {
  const modal = mount(); await loaded()
  fireEvent.click(await loaded(view.name)); fireEvent.click(await loaded())
  await save()
  await waitFor(() => expect(closed).toBe(true))
  expect(patches()[0].body).toEqual({ selected_objects: viewSelection, selection_revision: 7 })
  modal.unmount(); closed = false; mount()
  expect(await loaded(view.name)).toBeChecked(); expect(await loaded()).not.toBeChecked()
})
test('mixed selection never becomes legacy ALL when all current rows are checked', async () => {
  mount(); await loaded(); fireEvent.click(await loaded(view.name)); await save()
  expect(patches()[0].body).toEqual({ selected_objects: { selection_version: 2, mode: 'explicit', objects: [{ schema: 'public', name: 'orders', kind: 'table' }, { schema: 'reporting', name: 'sales', kind: 'view' }] }, selection_revision: 7 })
})
test('legacy ALL stays table-only; all kinds requires an explicit user action', async () => {
  mount(); expect(await loaded()).toBeChecked(); expect(await loaded(view.name)).not.toBeChecked()
  fireEvent.click(screen.getByRole('checkbox', { name: '全部表和视图（自动检测）' })); await save()
  expect(patches()[0].body).toEqual({ selected_objects: { selection_version: 2, mode: 'all', objects: [], include_kinds: ['table', 'view'] }, selection_revision: 7 })
})
test('complex quoted identifiers round-trip without splitting comma dot or quote', async () => {
  const complex = { ...view, name: '"a.b"."x,y""z"', schema_name: 'a.b', object_name: 'x,y"z', object_id: 'complex' }
  directory.tables = [table, complex]; mount(); await loaded(); fireEvent.click(await loaded(complex.name)); fireEvent.click(await loaded()); await save()
  expect(patches()[0].body).toEqual({ selected_objects: { selection_version: 2, mode: 'explicit', objects: [{ schema: 'a.b', name: 'x,y"z', kind: 'view' }] }, selection_revision: 7 })
})
test('409 keeps the dialog and scope, shows reload recovery and never success', async () => {
  rejectSave = true; mount(true); await loaded(); fireEvent.click(await loaded(view.name)); await save(true)
  await screen.findByText(/选择已变化/)
  expect(closed).toBe(false); expect(await loaded(view.name)).toBeChecked()
  expect(screen.queryByText(zhCN.toasts.knowledgeUpdated)).not.toBeInTheDocument()
  expect(screen.getByRole('button', { name: '重新加载对象' })).toBeEnabled()
})
test('old backend fallback retains explicit scope even if every returned row is selected', async () => {
  chat.selected_collections = 'orders'; directory = { tables: [{ ...table, name: 'orders' }] }; mount(true); await loaded('orders'); await save(true)
  expect(patches()[0].body).toEqual({ selected_collections: 'orders' })
})
test('v2 pure-view cannot fall back to an old backend or disabled feature', async () => {
  Object.assign(chat, { selected_objects: viewSelection, selection_revision: 7, selected_collections: '' }); coordinator.register(chat)
  directory = { tables: [{ ...table, name: 'orders' }], query_objects_enabled: false }
  mount(true); await screen.findByText(/保留.*选择/)
  fireEvent.click(screen.getByRole('button', { name: zhCN.connection.updateKnowledge }))
  expect(patches()).toHaveLength(0); expect(closed).toBe(false)
})
test('schema groups, view badges and unknown nullable are presented honestly', async () => {
  mount(); await loaded()
  expect(screen.getByRole('heading', { name: 'reporting' })).toBeInTheDocument()
  expect(screen.getByText('视图', { exact: true })).toBeInTheDocument()
  fireEvent.click(screen.getByRole('button', { name: '展开 ' + view.name }))
  expect(screen.getByText('可空性未知')).toBeInTheDocument()
  expect(screen.getByText('部分列')).toBeInTheDocument()
})
test('connection settings save uses v2 and nil index is not treated as ready', async () => {
  mount(true); await loaded(); fireEvent.click(await loaded(view.name)); await save(true)
  expect(patches()[0].body.selected_collections).toBeUndefined()
  await screen.findByText('选择已保存；索引未构建。')
  expect(screen.queryByText('索引已就绪')).not.toBeInTheDocument()
})
test('late save after source selection change cannot close the old modal', async () => {
  let resolve!: () => void; pendingPatch = () => new Promise<void>(yes => { resolve = yes })
  mount(); await loaded(); fireEvent.click(await loaded(view.name)); await save()
  await act(async () => { coordinator.select('another'); resolve() })
  expect(closed).toBe(false)
})
test('duplicate explicit v2 override is URL encoded and excludes legacy selection', async () => {
  await chatService.duplicateChat(chat.id, false, false, viewSelection as never)
  const url = requests.find(r => r.url?.includes('/duplicate'))!.url!
  const params = new URL(url, 'http://fixture').searchParams
  expect(JSON.parse(params.get('selected_objects')!)).toEqual(viewSelection)
  expect(params.has('selected_collections')).toBe(false)
})


test('disabled PG object capability is recoverable and cannot save a legacy approximation', async () => {
  directory.query_objects_enabled = false
  mount(true)
  await screen.findByText(/保留.*选择/)
  fireEvent.click(screen.getByRole('button', { name: zhCN.connection.updateKnowledge }))
  expect(patches()).toHaveLength(0)
})
test('legacy update refuses to overwrite authoritative pure-view scope', async () => {
  Object.assign(chat, { selected_objects: viewSelection, selection_revision: 7 }); coordinator.register(chat)
  await expect(chatService.updateSelectedCollections(chat.id, 'ALL')).rejects.toThrow(/对象选择/)
  expect(patches()).toHaveLength(0)
})
test('an old getChat response cannot replace a newer saved selection revision', async () => {
  mount(true); await loaded(); fireEvent.click(await loaded(view.name)); await save(true)
  await screen.findByText('选择已保存；索引未构建。')
  chat = { ...chat, selection_revision: 6, selected_objects: undefined }
  await expect(chatService.getChat(chat.id)).rejects.toThrow()
  expect(coordinator.view(chat.id).chat?.selection_revision).toBe(8)
})
test('new connection enters the existing selection step and loads directory once', async () => {
  const modal = render(<SourceAccessContext.Provider value={coordinator}><StreamContext.Provider value={{ streamId: 'fixture', setStreamId: () => {}, generateStreamId: () => 'fixture' }}>
    <ConnectionModal onClose={() => { closed = true }} onSubmit={async (connection, settings) => {
      const created = await chatService.createChat(connection, settings)
      return { success: true, chatId: created.id }
    }} />
  </StreamContext.Provider></SourceAccessContext.Provider>)
  for (const [name, value] of Object.entries({ host: 'fake.invalid', port: '5432', database: 'fixture', username: 'reader', password: 'fixture-only' })) {
    fireEvent.change(modal.container.querySelector('input[name="' + name + '"]')!, { target: { value } })
  }
  fireEvent.click(screen.getByRole('button', { name: zhCN.common.create }))
  await loaded()
  expect(requests.filter(r => r.url?.endsWith('/tables'))).toHaveLength(1)
  expect(await loaded(view.name)).not.toBeChecked()
  fireEvent.click(await loaded(view.name)); fireEvent.click(await loaded())
  fireEvent.click(screen.getByRole('button', { name: zhCN.connection.saveKnowledge }))
  await screen.findByText('选择已保存；索引未构建。')
  expect(patches()[0].body.selected_objects).toEqual(viewSelection)
})


test('reloading objects refreshes building/ready status from the chat manifest', async () => {
  chat = { ...chat, selected_objects: viewSelection as Chat['selected_objects'], selection_revision: 7,
    query_object_index: { status: 'building', format_version: 2 } }
  coordinator.register(chat); directory.selected_objects = viewSelection
  mount(true); await loaded()
  expect(screen.getByRole('status')).toHaveTextContent('索引构建中')
  chat = { ...chat, query_object_index: { status: 'ready', format_version: 2, active_generation: 'fixture-generation' } }
  fireEvent.click(screen.getByRole('button', { name: '重新加载对象' }))
  await waitFor(() => expect(screen.getByRole('status')).toHaveTextContent('索引已就绪'))
})


test('upgrading an explicit legacy subset refuses a catalog that silently omits selected objects', async () => {
  chat.selected_collections = 'orders,missing'
  mount(true)
  await screen.findByText(/保留.*选择/)
  fireEvent.click(screen.getByRole('button', { name: zhCN.connection.updateKnowledge }))
  expect(patches()).toHaveLength(0)
})
test('explicit all-tables save never enables views', async () => {
  mount(true); await loaded(); await save(true)
  expect(patches()[0].body).toEqual({ selected_objects: { selection_version: 2, mode: 'all', objects: [], include_kinds: ['table'] }, selection_revision: 7 })
})
test('a source admission denial prevents all object metadata network requests', async () => {
  coordinator.configure('', undefined, [])
  mount()
  await waitFor(() => expect(screen.getByRole('button', { name: zhCN.schema.saveApply })).toBeDisabled())
  expect(requests).toHaveLength(0)
})
test('duplicate without override leaves scope to the backend and legacy override stays separate', async () => {
  await chatService.duplicateChat(chat.id)
  await chatService.duplicateChat(chat.id, true, false, 'orders,items')
  const urls = requests.map(request => new URL(request.url!, 'http://fixture').searchParams)
  expect(urls[0].has('selected_objects')).toBe(false)
  expect(urls[0].has('selected_collections')).toBe(false)
  expect(urls[1].get('selected_collections')).toBe('orders,items')
  expect(urls[1].has('selected_objects')).toBe(false)
})

test('a stale settings response cannot return success over a newer v2 selection', async () => {
  coordinator.register({ ...chat, selected_objects: viewSelection as Chat['selected_objects'], selection_revision: 9 })
  await expect(chatService.editChat(chat.id, undefined, chat.settings)).rejects.toThrow()
  expect(coordinator.view(chat.id).chat?.selection_revision).toBe(9)
})
test('a legacy save already in flight cannot report success after v2 selection takes authority', async () => {
  let resolve!: () => void
  pendingPatch = () => new Promise<void>(yes => { resolve = yes })
  const operation = chatService.updateSelectedCollections(chat.id, 'orders')
  const rejected = expect(operation).rejects.toThrow(/对象选择/)
  await waitFor(() => expect(patches()).toHaveLength(1))
  coordinator.register({ ...chat, selected_objects: viewSelection as Chat['selected_objects'], selection_revision: 9 })
  resolve()
  await rejected
})
