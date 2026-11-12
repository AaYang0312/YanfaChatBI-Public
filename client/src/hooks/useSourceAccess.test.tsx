import { act, cleanup, fireEvent, render, renderHook, screen } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, test, vi } from 'vitest'
import type { ReactNode } from 'react'
import { SourceAccessContext, SourceAccessCoordinator, SourceAccessError, installSourceAccess } from '../contexts/SourceAccessContext'
import { useSourceAccess } from './useSourceAccess'
import { dataSyncService } from '../services/dataSyncService'
import chatService from '../services/chatService'
import MessageInput from '../components/chat/MessageInput'
import type { Chat } from '../types/chat'
import type { DataSyncStatus } from '../types/dataSync'
import { zhCN } from '../i18n/zh-CN'
import AssistantInputPanel from '../components/assistant/AssistantInputPanel'
import { numericFixtureRequest } from '../components/assistant/numericFixture'
import type { AnalysisRequest } from '../types/assistant'

const post = vi.fn()
vi.mock('../services/axiosConfig', () => ({ default: { post: (...args: unknown[]) => post(...args), isCancel: () => false } }))
vi.mock('../services/analyticsService', () => ({ default: { trackMessageSubmit: vi.fn() } }))

function gateChat(id = 'erp-a', kind: Chat['source_classification'] = 'registered_erp', owner = 'owner-a'): Chat {
  return {
    id, user_id: owner, source_classification: kind,
    ...(kind === 'registered_erp' ? { source_id: 'source-a', source_binding_revision: 1 } : {}),
    connection: { type: 'postgresql', host: 'isolated.invalid', port: '5432', username: 'reader', database: 'isolated', is_example_db: false },
    settings: { auto_execute_query: false, share_data_with_ai: false, non_tech_mode: false, auto_generate_visualization: false },
    created_at: '2026-01-01T00:00:00Z', updated_at: '2026-01-01T00:00:00Z',
  }
}
function status(phase: DataSyncStatus['maintenance_state'] = 'idle', revision = 1): DataSyncStatus {
  return { schema_version: 1, source_id: 'source-a', source_revision: revision, binding_revision: 1,
    display_name: 'ERP', readiness: { state: 'ready', reasons: [] }, can_start: false,
    ask_allowed: phase === 'idle', block_reason: phase === 'idle' ? '' : phase === 'syncing' ? 'DATA_SYNC_IN_PROGRESS' : 'DATA_SYNC_RECOVERING',
    maintenance_state: phase, active_job: null, latest_successful_sync_at: null }
}
function deferred<T>() {
  // The application's ES2020 lib does not expose Promise.withResolvers.
  let resolve!: (value: T) => void
  let reject!: (reason: unknown) => void
  const promise = new Promise<T>((yes, no) => { resolve = yes; reject = no })
  return { promise, resolve, reject }
}
const stops: (() => void)[] = []
function setup(getStatus = vi.fn().mockResolvedValue(status()), chats = [gateChat()], selected = chats[0]) {
  const getChat = vi.fn(async (id: string) => chats.find(chat => chat.id === id)!)
  const coordinator = new SourceAccessCoordinator({ ...dataSyncService, getStatus, getChat })
  coordinator.configure(selected.user_id, selected, chats)
  stops.push(installSourceAccess(coordinator))
  const wrapper = ({ children }: { children: ReactNode }) => <SourceAccessContext.Provider value={coordinator}>{children}</SourceAccessContext.Provider>
  return { coordinator, getStatus, getChat, wrapper }
}

beforeEach(() => {
  vi.useFakeTimers()
  post.mockReset()
  Object.defineProperty(document, 'visibilityState', { configurable: true, value: 'visible' })
  Object.defineProperty(navigator, 'onLine', { configurable: true, value: true })
})
afterEach(() => {
  cleanup()
  stops.splice(0).reverse().forEach(stop => stop())
  vi.useRealTimers()
})

describe('one App source authority at consumer boundaries', () => {
  test('serial polling waits for completion and continues after a terminal/idle source', async () => {
    const pending = deferred<DataSyncStatus>()
    const getStatus = vi.fn().mockReturnValueOnce(pending.promise).mockResolvedValue(status('idle', 2))
    const { coordinator, wrapper } = setup(getStatus)
    const hook = renderHook(() => useSourceAccess('erp-a'), { wrapper })
    stops.push(coordinator.start())
    await act(async () => { await Promise.resolve(); vi.advanceTimersByTime(8000) })
    expect(getStatus).toHaveBeenCalledTimes(1)
    expect(hook.result.current.allowed).toBe(false)
    await act(async () => { pending.resolve(status()); await pending.promise })
    expect(hook.result.current.allowed).toBe(true)
    await act(async () => { vi.advanceTimersByTime(2000) })
    expect(getStatus).toHaveBeenCalledTimes(2)
    expect(hook.result.current.allowed).toBe(true)
  })

  test('a late response from a previous Chat/account cannot grant permission', async () => {
    const late = deferred<DataSyncStatus>()
    const getStatus = vi.fn().mockReturnValueOnce(late.promise).mockResolvedValue(status('recovering', 3))
    const other = { ...gateChat('erp-b', 'registered_erp', 'owner-b'), source_id: 'source-b' }
    const { coordinator } = setup(getStatus)
    stops.push(coordinator.start())
    await act(async () => {})
    coordinator.configure('owner-b', other, [other])
    await act(async () => { late.resolve(status('idle', 99)); await late.promise })
    expect(coordinator.view('erp-a').allowed).toBe(false)
    expect(coordinator.view('erp-b').allowed).toBe(false)
    await expect(coordinator.admit('erp-a')).rejects.toBeInstanceOf(SourceAccessError)
  })

  test('binding and source revisions reject older responses and pending continuations', async () => {
    const getStatus = vi.fn().mockResolvedValueOnce(status('idle', 10)).mockResolvedValueOnce(status('idle', 9))
    const { coordinator } = setup(getStatus)
    await coordinator.revalidate('erp-a')
    const target = await coordinator.admit('erp-a')
    await coordinator.revalidate('erp-a')
    expect(coordinator.view('erp-a').allowed).toBe(false)
    expect(() => coordinator.assert(target)).toThrow(SourceAccessError)
    coordinator.register({ ...gateChat(), source_binding_revision: 2 })
    expect(() => coordinator.assert(target)).toThrow(SourceAccessError)
    expect(coordinator.view('erp-a').allowed).toBe(false)
  })

  test('R1 idle revision advance revokes the old target and permits a new explicit operation', async () => {
    const getStatus = vi.fn().mockResolvedValue(status('idle', 10))
    const { coordinator } = setup(getStatus)
    await coordinator.revalidate('erp-a')
    const oldTarget = await coordinator.admit('erp-a')
    coordinator.assert(oldTarget)

    getStatus.mockResolvedValue(status('idle', 13))
    await coordinator.revalidate('erp-a')
    expect(coordinator.view('erp-a').allowed).toBe(true)
    expect.soft(() => coordinator.assert(oldTarget), 'idle r10 permission must not continue under idle r13').toThrow(SourceAccessError)

    const currentTarget = await coordinator.admit('erp-a')
    coordinator.assert(currentTarget)
    post.mockResolvedValueOnce({ data: { success: true, data: { id: 'after-r13', content: '用户重新明确提交' } } })
    const accepted = await chatService.sendMessage('erp-a', 'temp-r13', 'stream', '用户重新明确提交')
    expect(accepted.data).toEqual({ id: 'after-r13', content: '用户重新明确提交' })
    expect(post).toHaveBeenCalledTimes(1)
  })

  test('a known lock blocks the service itself, with no SQL/model request', async () => {
    const { coordinator } = setup(vi.fn().mockResolvedValue(status('syncing')))
    await coordinator.revalidate('erp-a')
    await expect(chatService.sendMessage('erp-a', 'temp', 'stream', '草稿')).rejects.toThrow(zhCN.sourceAccess.syncing)
    await expect(chatService.executeQuery('erp-a', 'message', 'query', 'stream', new AbortController())).rejects.toThrow(SourceAccessError)
    expect(post).not.toHaveBeenCalled()
  })

  test.each(['syncing', 'recovering', 'outage'] as const)('independent create/connect/question ignores ERP %s', async phase => {
    const independent = gateChat('pg', 'independent')
    const getStatus = phase === 'outage' ? vi.fn().mockRejectedValue(new Error('offline')) : vi.fn().mockResolvedValue(status(phase))
    const { coordinator } = setup(getStatus)
    await coordinator.revalidate('erp-a')
    post.mockResolvedValueOnce({ data: { success: true, data: independent } })
    const created = await chatService.createChat(independent.connection, independent.settings)
    expect(created.id).toBe('pg')
    post.mockResolvedValueOnce({ data: { success: true } })
    await chatService.connectToConnection('pg', 'stream')
    post.mockResolvedValueOnce({ data: { success: true, data: { id: 'accepted', content: '独立问数' } } })
    const result = await chatService.sendMessage('pg', 'temp', 'stream', '独立问数')
    expect(result.data.content).toBe('独立问数')
    expect(getStatus).toHaveBeenCalledTimes(1)
    expect(coordinator.view('erp-a').allowed).toBe(false)
  })

  test.each([undefined, 'unresolved_erp'] as const)('missing/sticky metadata %s never becomes independent', async kind => {
    const unknown = gateChat('legacy', kind)
    delete unknown.source_id
    delete unknown.source_binding_revision
    if (kind === undefined) delete unknown.source_classification
    const { coordinator, getStatus } = setup(vi.fn(), [unknown])
    await expect(coordinator.admit('legacy')).rejects.toBeInstanceOf(SourceAccessError)
    expect(coordinator.view('legacy').allowed).toBe(false)
    expect(getStatus).not.toHaveBeenCalled()
    expect(post).not.toHaveBeenCalled()
  })

  test('hide/offline pauses discovery; visible/online revalidation fences old await continuations', async () => {
    const { coordinator, getStatus } = setup()
    stops.push(coordinator.start())
    await act(async () => {})
    const target = await coordinator.admit('erp-a')
    Object.defineProperty(document, 'visibilityState', { configurable: true, value: 'hidden' })
    act(() => document.dispatchEvent(new Event('visibilitychange')))
    await act(async () => { vi.advanceTimersByTime(10000) })
    expect(getStatus).toHaveBeenCalledTimes(1)
    expect(coordinator.view().allowed).toBe(false)
    Object.defineProperty(document, 'visibilityState', { configurable: true, value: 'visible' })
    act(() => document.dispatchEvent(new Event('visibilitychange')))
    expect(coordinator.view().allowed).toBe(false)
    await act(async () => {})
    expect(coordinator.view().allowed).toBe(true)
    expect(() => coordinator.assert(target)).toThrow(SourceAccessError)
    Object.defineProperty(navigator, 'onLine', { configurable: true, value: false })
    act(() => window.dispatchEvent(new Event('offline')))
    await act(async () => { vi.advanceTimersByTime(6000) })
    expect(getStatus).toHaveBeenCalledTimes(2)
    Object.defineProperty(navigator, 'onLine', { configurable: true, value: true })
    act(() => window.dispatchEvent(new Event('online')))
    await act(async () => {})
    expect(getStatus).toHaveBeenCalledTimes(3)
  })

  test('Enter cannot bypass the gate; backend refusal and user edits during await preserve the draft', async () => {
    const getStatus = vi.fn().mockResolvedValue(status('syncing'))
    const { coordinator, wrapper } = setup(getStatus)
    await coordinator.revalidate('erp-a')
    const accepted = deferred<boolean>()
    const send = vi.fn(() => accepted.promise)
    render(<MessageInput chatId="erp-a" isConnected isExpanded={false} onSendMessage={send} workflowControl={null} />, { wrapper })
    const input = screen.getByRole('textbox')
    fireEvent.change(input, { target: { value: '保留草稿' } })
    fireEvent.keyDown(input, { key: 'Enter' })
    expect(send).not.toHaveBeenCalled()
    expect(input).toHaveValue('保留草稿')
    getStatus.mockResolvedValue(status('idle', 2))
    await act(async () => { await coordinator.revalidate('erp-a') })
    expect(send).not.toHaveBeenCalled()
    fireEvent.keyDown(input, { key: 'Enter' })
    await act(async () => { accepted.resolve(false); await accepted.promise })
    expect(input).toHaveValue('保留草稿')
    send.mockResolvedValueOnce(true)
    fireEvent.keyDown(input, { key: 'Enter' })
    fireEvent.change(input, { target: { value: '下一条草稿' } })
    await act(async () => {})
    expect(input).toHaveValue('下一条草稿')
  })
  test('a resolve accepted before sync cannot continue into create after the status changes', async () => {
    const getStatus = vi.fn().mockResolvedValue(status())
    const { coordinator, wrapper } = setup(getStatus)
    await coordinator.revalidate('erp-a')
    const resolved = deferred<{ data: { data: { status: string; request: AnalysisRequest } } }>()
    post.mockReturnValueOnce(resolved.promise)
    render(<AssistantInputPanel chatId="erp-a" isConnected workflow="product_analysis" onWorkflowChange={() => {}} onRunCreated={vi.fn()} />, { wrapper })
    fireEvent.change(screen.getByLabelText(zhCN.assistant.input.question), { target: { value: '晚回解析草稿' } })
    fireEvent.submit(screen.getByRole('form', { name: zhCN.assistant.input.title }))
    await act(async () => {})
    expect(post).toHaveBeenCalledTimes(1)
    getStatus.mockResolvedValue(status('syncing', 2))
    await act(async () => { await coordinator.revalidate('erp-a') })
    await act(async () => { resolved.resolve({ data: { data: { status: 'resolved', request: numericFixtureRequest() } } }); await resolved.promise })
    expect(post).toHaveBeenCalledTimes(1)
    expect(screen.getByLabelText(zhCN.assistant.input.question)).toHaveValue('晚回解析草稿')
    expect(screen.getByRole('alert')).toHaveTextContent(zhCN.sourceAccess.syncing)
  })

  test('a new App with the same owner and Chat rejects the old coordinator continuation', async () => {
    const independent = gateChat('pg', 'independent')
    const first = new SourceAccessCoordinator()
    first.configure('owner-a', independent, [independent])
    const target = await first.admit('pg')
    const second = new SourceAccessCoordinator()
    second.configure('owner-a', independent, [independent])
    expect(() => second.assert(target)).toThrow(SourceAccessError)
  })
})
