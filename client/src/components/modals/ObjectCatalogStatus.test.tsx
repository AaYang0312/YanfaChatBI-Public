import { act, cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, expect, test } from 'vitest'
import type { AxiosAdapter } from 'axios'
import axios from '../../services/axiosConfig'
import ConnectionModal from './ConnectionModal'
import { StreamContext } from '../../contexts/StreamContext'
import { SourceAccessContext, SourceAccessCoordinator, installSourceAccess } from '../../contexts/SourceAccessContext'
import type { Chat, TableInfo, TablesResponse } from '../../types/chat'
import { zhCN } from '../../i18n/zh-CN'

let server: Chat
let directory: TablesResponse
let coordinator: SourceAccessCoordinator
let stop: () => void
let adapterBefore: typeof axios.defaults.adapter
let changed: boolean
let manifestGate: Promise<void> | undefined
let manifestStarted: boolean
let methods: (string | undefined)[]
const object: TableInfo = { object_id: 'status-view', object_instance_version: 'v1', schema_name: 'public', object_name: 'v', object_type: 'view', name: '"public"."v"', is_selected: true, columns: [{ name: 'old', type: 'integer', is_nullable: false }] }
const adapter: AxiosAdapter = async config => {
  methods.push(config.method)
  let data: unknown
  if (config.url?.endsWith('/tables')) {
    if (changed) {
      server = { ...server, query_object_index: { ...server.query_object_index!, status: 'stale' } }
      directory.tables = [{ ...object, columns: [{ name: 'new', type: 'text', is_nullable: false }] }]
    }
    data = structuredClone(directory)
  } else {
    data = structuredClone(server)
    manifestStarted = true
    await manifestGate
  }
  return { config, status: 200, statusText: 'OK', headers: {}, data: { success: true, data } }
}
beforeEach(() => {
  server = { id: 'catalog-status', user_id: 'owner', source_classification: 'independent', selection_revision: 7,
    selected_objects: { selection_version: 2, mode: 'explicit', objects: [{ schema: 'public', name: 'v', kind: 'view' }] },
    query_object_index: { status: 'ready', format_version: 2, active_generation: 'old-generation' },
    connection: { type: 'postgresql', host: 'fixture.invalid', port: '5432', database: 'fixture', username: 'reader', is_example_db: false },
    settings: { auto_execute_query: false, non_tech_mode: false, auto_generate_visualization: false }, created_at: '', updated_at: '' }
  directory = { tables: [object], metadata_version: 2, query_objects_enabled: true, selection_revision: 7, selected_objects: server.selected_objects }
  changed = false; manifestGate = undefined; manifestStarted = false; methods = []
  adapterBefore = axios.defaults.adapter; axios.defaults.adapter = adapter
  coordinator = new SourceAccessCoordinator(); coordinator.configure('owner', server, [server]); stop = installSourceAccess(coordinator)
})
afterEach(() => { cleanup(); stop(); axios.defaults.adapter = adapterBefore })
function mount() {
  return render(<SourceAccessContext.Provider value={coordinator}><StreamContext.Provider value={{ streamId: 'fixture', setStreamId: () => {}, generateStreamId: () => 'fixture' }}>
    <ConnectionModal initialData={server} initialTab="schema" onClose={() => {}} onSubmit={async () => ({ success: false })} />
  </StreamContext.Provider></SourceAccessContext.Provider>)
}
test('A3 a catalog observation invalidating ready is reflected without a selection revision change', async () => {
  mount(); await screen.findByRole('checkbox', { name: object.name })
  expect(screen.getByRole('status')).toHaveTextContent('索引已就绪')
  changed = true
  fireEvent.click(screen.getByRole('button', { name: '重新加载对象' }))
  await screen.findByRole('checkbox', { name: object.name })
  expect(server.selection_revision).toBe(7)
  expect(screen.getByRole('status')).toHaveTextContent('索引待更新')
  fireEvent.click(screen.getByRole('button', { name: '展开 ' + object.name }))
  expect(screen.getByText('new')).toBeInTheDocument()
  expect(methods.every(method => method === 'get')).toBe(true)
})
test.each([6, 8])('A3 directory revision %s and metadata revision 7 cannot be combined into a ready writable snapshot', async revision => {
  directory.selection_revision = revision
  mount()
  await waitFor(() => expect(screen.getByRole('button', { name: '重新加载对象' })).toBeEnabled())
  expect(screen.getByRole('button', { name: zhCN.connection.updateKnowledge })).toBeDisabled()
  expect(screen.queryByText(/索引已就绪/)).not.toBeInTheDocument()
  expect(screen.getByRole('alert')).toBeInTheDocument()
  directory.selection_revision = 7
  fireEvent.click(screen.getByRole('button', { name: '重新加载对象' }))
  expect(await screen.findByRole('checkbox', { name: object.name })).toBeChecked()
  expect(methods.every(method => method === 'get')).toBe(true)
})
test.each(['owner', 'binding'] as const)('A3 a late manifest after %s changes cannot publish old ready or a writable catalog', async change => {
  mount(); await screen.findByRole('checkbox', { name: object.name })
  let release!: () => void
  manifestStarted = false
  manifestGate = new Promise<void>(resolve => { release = resolve })
  fireEvent.click(screen.getByRole('button', { name: '重新加载对象' }))
  await waitFor(() => expect(manifestStarted).toBe(true))
  const next = change === 'owner' ? { ...server, user_id: 'other-owner' } : { ...server, connection: { ...server.connection, host: 'new.invalid' } }
  act(() => coordinator.configure(next.user_id, next, [next]))
  await act(async () => { release(); await manifestGate })
  expect(screen.queryByRole('checkbox', { name: object.name })).not.toBeInTheDocument()
  expect(screen.queryByText(/索引已就绪/)).not.toBeInTheDocument()
  expect(screen.getByRole('button', { name: zhCN.connection.updateKnowledge })).toBeDisabled()
  expect(methods.every(method => method === 'get')).toBe(true)
})
