// Independent T08a-r1 review artifact. Network boundary is entirely synthetic.
import { act, cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, expect, test } from 'vitest'
import type { AxiosAdapter } from 'axios'
import axios from '../services/axiosConfig'
import chatService from '../services/chatService'
import ConnectionModal from '../components/modals/ConnectionModal'
import { StreamContext } from '../contexts/StreamContext'
import { SourceAccessContext, SourceAccessCoordinator, installSourceAccess } from '../contexts/SourceAccessContext'
import { indexStatus } from '../hooks/useObjectSelection'
import type { Chat, ObjectSelection, TableInfo, TablesResponse } from '../types/chat'
import { zhCN } from '../i18n/zh-CN'

const table: TableInfo = { name: '"public"."same"', object_id: 'table-public', schema_name: 'public', object_name: 'same', object_type: 'table', is_selected: true, row_count: 0, row_count_status: 'unknown', columns: [{ name: 'id', type: 'integer', is_nullable: false, nullable: null }] }
const view: TableInfo = { ...table, name: '"reporting"."same"', object_id: 'view-reporting', schema_name: 'reporting', object_type: 'view', is_selected: false }
const viewSelection: ObjectSelection = { selection_version: 2, mode: 'explicit', objects: [{ schema: 'reporting', name: 'same', kind: 'view' }] }
let server: Chat
let directory: TablesResponse
let coordinator: SourceAccessCoordinator
let uninstall: () => void
let oldAdapter: typeof axios.defaults.adapter
let patchWait: Promise<void> | undefined
let requests: { method?: string; url: string; body: Record<string, unknown> }[]
const adapter: AxiosAdapter = async config => {
  const body = typeof config.data === 'string' ? JSON.parse(config.data) : config.data || {}
  const url = config.url ?? ''
  requests.push({ method: config.method, url, body })
  let data: unknown
  if (config.method === 'get' && url.endsWith('/tables')) data = directory
  else if (config.method === 'get' && url.endsWith('/chats/review')) data = server
  else if (config.method === 'patch' && url.endsWith('/chats/review')) {
    if (patchWait) await patchWait
    server = { ...server, ...body, selection_revision: (server.selection_revision ?? 0) + 1, query_object_index: null }
    directory = { ...directory, selected_objects: server.selected_objects, selection_revision: server.selection_revision }
    data = server
  } else if (config.method === 'post' && (url.includes('/duplicate?') || url.endsWith('/chats'))) data = { ...server, id: 'review-created' }
  else throw new Error('Unexpected mock request: ' + config.method + ' ' + url)
  return { status: 200, statusText: 'OK', config, headers: {}, data: { success: true, data } }
}
beforeEach(() => {
  server = { id: 'review', user_id: 'owner', source_classification: 'independent', source_binding_revision: 1, selection_revision: 7,
    connection: { type: 'postgresql', host: 'fixture.invalid', port: '5432', database: 'synthetic', username: 'reader', is_example_db: false },
    selected_collections: 'ALL', settings: { auto_execute_query: false, non_tech_mode: false, auto_generate_visualization: false }, created_at: '2020-01-01', updated_at: '2020-01-01' }
  directory = { tables: [table, view], metadata_version: 2, selection_revision: 7, query_objects_enabled: true }
  requests = []; patchWait = undefined
  oldAdapter = axios.defaults.adapter; axios.defaults.adapter = adapter
  coordinator = new SourceAccessCoordinator(); coordinator.configure('owner', server, [server]); uninstall = installSourceAccess(coordinator)
})
afterEach(() => { cleanup(); uninstall(); axios.defaults.adapter = oldAdapter })
function mount() {
  return render(<SourceAccessContext.Provider value={coordinator}>
    <StreamContext.Provider value={{ streamId: 'review', setStreamId: () => {}, generateStreamId: () => 'review' }}>
      <ConnectionModal initialData={server} initialTab="schema" onClose={() => {}} onSubmit={async () => ({ success: false })} />
    </StreamContext.Provider>
  </SourceAccessContext.Provider>)
}
const loaded = (name = table.name) => screen.findByRole('checkbox', { name })
const patches = () => requests.filter(request => request.method === 'patch')
async function save() { fireEvent.click(screen.getByRole('button', { name: zhCN.connection.updateKnowledge })); await waitFor(() => expect(patches()).toHaveLength(1)) }

test('R1: unknown row count follows the backend status even when wire row_count is zero', async () => {
  mount(); await loaded()
  fireEvent.click(screen.getByRole('button', { name: '展开 ' + view.name }))
  expect(screen.getByText('行数未知')).toBeInTheDocument()
  expect(screen.queryByText('0 行')).not.toBeInTheDocument()
})

test('R2: switching tabs while save is pending must leave the reloaded selector usable', async () => {
  let release!: () => void
  patchWait = new Promise<void>(resolve => { release = resolve })
  mount(); await loaded(); fireEvent.click(await loaded(view.name)); await save()
  fireEvent.click(screen.getByRole('button', { name: zhCN.connection.tabSettings }))
  await act(async () => { release(); await patchWait })
  await waitFor(() => expect(coordinator.view(server.id).chat?.selection_revision).toBe(8))
  fireEvent.click(screen.getByRole('button', { name: zhCN.connection.tabKnowledge }))
  await loaded()
  expect(screen.getByRole('button', { name: '重新加载对象' })).toBeEnabled()
  expect(screen.getByRole('button', { name: zhCN.connection.updateKnowledge })).toBeEnabled()
})

test('cross-schema same names keep different object identities and save exact refs', async () => {
  mount(); expect(await loaded()).toBeChecked(); expect(await loaded(view.name)).not.toBeChecked()
  fireEvent.click(await loaded(view.name)); fireEvent.click(await loaded()); await save()
  expect(patches()[0].body).toEqual({ selected_objects: viewSelection, selection_revision: 7 })
})

test('empty explicit cannot send a patch or become ALL', async () => {
  mount(); fireEvent.click(await loaded())
  fireEvent.click(screen.getByRole('button', { name: zhCN.connection.updateKnowledge }))
  await screen.findByText('至少需要选择一个查询对象。')
  expect(patches()).toHaveLength(0)
})

test('late save after account switch cannot update the new authority', async () => {
  let release!: () => void
  patchWait = new Promise<void>(resolve => { release = resolve })
  const operation = chatService.updateSelectedObjects(server.id, viewSelection, 7)
  const rejected = expect(operation).rejects.toThrow()
  await waitFor(() => expect(patches()).toHaveLength(1))
  coordinator.configure('different-owner', undefined, [])
  release(); await rejected
  expect(coordinator.view(server.id).allowed).toBe(false)
  expect(coordinator.view(server.id).chat).toBeUndefined()
})

test('late save after binding change cannot restore the old connection', async () => {
  let release!: () => void
  patchWait = new Promise<void>(resolve => { release = resolve })
  const operation = chatService.updateSelectedObjects(server.id, viewSelection, 7)
  const rejected = expect(operation).rejects.toThrow()
  await waitFor(() => expect(patches()).toHaveLength(1))
  const rebound = { ...server, source_binding_revision: 2, selection_revision: 9, selected_objects: viewSelection, connection: { ...server.connection, database: 'other-fixture' } }
  expect(coordinator.register(rebound)).toBe(true)
  release(); await rejected
  expect(coordinator.view(server.id).chat).toEqual(rebound)
})

test('duplicate complex scope has exactly one selected_objects query and no legacy field', async () => {
  const complex: ObjectSelection = { selection_version: 2, mode: 'explicit', objects: [{ schema: 's.c', name: 'x,y"z&?+', kind: 'view' }] }
  await chatService.duplicateChat(server.id, true, false, complex)
  const params = new URL(requests[0].url, 'http://fixture.invalid').searchParams
  expect(params.getAll('selected_objects')).toHaveLength(1)
  expect(JSON.parse(params.get('selected_objects')!)).toEqual(complex)
  expect(params.has('selected_collections')).toBe(false)
})

test.each([
  [undefined, '索引未构建'],
  [{ status: 'building', format_version: 2 }, '索引构建中'],
  [{ status: 'failed', format_version: 2 }, '索引构建失败，请刷新结构后重试'],
  [{ status: 'stale', format_version: 2 }, '索引待更新'],
  [{ status: 'ready', format_version: 1, active_generation: 'old' }, '索引待更新'],
  [{ status: 'ready', format_version: 2 }, '索引待更新'],
  [{ status: 'ready', format_version: 2, active_generation: 'verified-by-server' }, '索引已就绪'],
] as const)('manifest state %j renders %s', (manifest, expected) => {
  expect(indexStatus({ ...server, query_object_index: manifest })).toBe(expected)
})

