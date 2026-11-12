import { act, cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, expect, test } from 'vitest'
import { AxiosError, type AxiosAdapter } from 'axios'
import axios from '../../services/axiosConfig'
import ConnectionModal from './ConnectionModal'
import { StreamContext } from '../../contexts/StreamContext'
import { SourceAccessContext, SourceAccessCoordinator, installSourceAccess } from '../../contexts/SourceAccessContext'
import type { Chat, TableInfo, TablesResponse } from '../../types/chat'
import { zhCN } from '../../i18n/zh-CN'

let chat: Chat
let directory: TablesResponse
let originalAdapter: typeof axios.defaults.adapter
let stop: () => void
let coordinator: SourceAccessCoordinator
let patchGate: Promise<void> | undefined
let patchStarted: boolean
let fail: boolean
const row: TableInfo = { name: '"public"."v"', object_id: 'view-id', schema_name: 'public', object_name: 'v', object_type: 'view', is_selected: true, row_count: 0, row_count_status: 'unknown', columns: [] }
const adapter: AxiosAdapter = async config => {
  let data: unknown = config.url?.endsWith('/tables') ? directory : chat
  if (config.method === 'patch') {
    patchStarted = true
    await patchGate
    if (fail) throw new AxiosError('Conflict', 'ERR_BAD_REQUEST', config, undefined, { status: 409, statusText: 'Conflict', config, headers: {}, data: { error: 'SELECTION_REVISION_CONFLICT' } })
    chat = { ...chat, ...JSON.parse(config.data), selection_revision: 8, query_object_index: null }
    directory = { ...directory, selection_revision: 8, selected_objects: chat.selected_objects }
    data = chat
  }
  return { config, status: 200, statusText: 'OK', headers: {}, data: { success: true, data } }
}
beforeEach(() => {
  chat = { id: 'recovery', user_id: 'owner', source_classification: 'independent', selection_revision: 7,
    selected_objects: { selection_version: 2, mode: 'explicit', objects: [{ schema: 'public', name: 'v', kind: 'view' }] },
    connection: { type: 'postgresql', host: 'fake.invalid', port: '5432', database: 'fixture', username: 'reader', is_example_db: false },
    settings: { auto_execute_query: false, non_tech_mode: false, auto_generate_visualization: false }, created_at: '', updated_at: '' }
  directory = { tables: [row], metadata_version: 2, query_objects_enabled: true, selection_revision: 7, selected_objects: chat.selected_objects }
  patchGate = undefined; patchStarted = false; fail = false
  originalAdapter = axios.defaults.adapter; axios.defaults.adapter = adapter
  coordinator = new SourceAccessCoordinator(); coordinator.configure('owner', chat, [chat]); stop = installSourceAccess(coordinator)
})
afterEach(() => { cleanup(); stop(); axios.defaults.adapter = originalAdapter })
function mount() {
  return render(<SourceAccessContext.Provider value={coordinator}><StreamContext.Provider value={{ streamId: 'fixture', setStreamId: () => {}, generateStreamId: () => 'fixture' }}>
    <ConnectionModal initialData={chat} initialTab="schema" onClose={() => {}} onSubmit={async () => ({ success: false })} />
  </StreamContext.Provider></SourceAccessContext.Provider>)
}
test.each([
  ['unknown', 0, '行数未知'], ['known', 0, '0 行'], ['estimated', 12, '约 12 行（估算）'], [undefined, 0, '0 行'],
] as const)('row status %s preserves known/estimated/legacy semantics', async (status, count, label) => {
  directory.tables = [{ ...row, row_count_status: status, row_count: count }]
  mount(); await screen.findByRole('checkbox', { name: row.name })
  fireEvent.click(screen.getByRole('button', { name: '展开 ' + row.name }))
  expect(screen.getByText(label)).toBeInTheDocument()
})
test.each([false, true])('tab switch during a save releases controls on completion (failure=%s)', async failure => {
  fail = failure
  let release!: () => void
  patchGate = new Promise<void>(resolve => { release = resolve })
  mount(); await screen.findByRole('checkbox', { name: row.name })
  fireEvent.click(screen.getByRole('button', { name: zhCN.connection.updateKnowledge }))
  await waitFor(() => expect(patchStarted).toBe(true))
  fireEvent.click(screen.getByRole('button', { name: zhCN.connection.tabSettings }))
  await act(async () => { release(); await patchGate })
  fireEvent.click(screen.getByRole('button', { name: zhCN.connection.tabKnowledge }))
  await screen.findByRole('checkbox', { name: row.name })
  await waitFor(() => expect(screen.getByRole('button', { name: '重新加载对象' })).toBeEnabled())
  expect(screen.getByRole('button', { name: zhCN.connection.updateKnowledge })).toBeEnabled()
  expect(screen.getByRole('checkbox', { name: row.name })).toBeEnabled()
  expect(screen.queryByText(/选择已保存/)).not.toBeInTheDocument()
})
