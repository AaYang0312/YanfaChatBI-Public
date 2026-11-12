import { act, cleanup, renderHook } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, test, vi } from 'vitest'
import type { ReactNode } from 'react'
import { SourceAccessContext, SourceAccessCoordinator, installSourceAccess } from '../contexts/SourceAccessContext'
import { dataSyncService } from '../services/dataSyncService'
import { useDataSyncJob } from './useDataSyncJob'
import type { Chat } from '../types/chat'
import type { DataSyncJob, DataSyncStatus } from '../types/dataSync'
import { zhCN } from '../i18n/zh-CN'

function chat(id = 'chat-a', owner = 'owner-a', source = 'source-a'): Chat {
  return { id, user_id: owner, source_id: source, source_binding_revision: 1, source_classification: 'registered_erp',
    connection: { type: 'postgresql', host: 'private-endpoint.invalid', port: '5432', username: 'reader', database: 'isolated', is_example_db: false,
      ssh_enabled: true, ssh_host: 'private-tunnel.invalid', ssl_root_cert_url: 'https://private-ca.invalid/root' },
    settings: { auto_execute_query: false, share_data_with_ai: false, non_tech_mode: false, auto_generate_visualization: false }, created_at: '', updated_at: '' }
}
function status(source = 'source-a'): DataSyncStatus {
  return { schema_version: 1, source_id: source, source_revision: 1, binding_revision: 1, display_name: 'ERP', readiness: { state: 'ready', reasons: [] },
    can_start: true, ask_allowed: true, block_reason: '', maintenance_state: 'idle', active_job: null, latest_successful_sync_at: null }
}
function job(requestId: string, source = 'source-a', terminal = false): DataSyncJob {
  return { schema_version: 1, job_id: `job-${requestId}`, source_id: source, request_id: requestId, initiating_chat_id: 'chat-a',
    status: terminal ? 'succeeded' : 'running', stage: terminal ? 'verifying' : 'syncing', revision: terminal ? 4 : 1,
    created_at: '2026-01-01T00:00:00Z', started_at: '2026-01-01T00:00:01Z', finished_at: terminal ? '2026-01-01T00:01:00Z' : null,
    cutoff: null, expected_count: 7, processed_count: terminal ? 7 : 0, succeeded_count: terminal ? 7 : 0, failed_count: 0, shop_results: null,
    verification_passed: terminal, safe_error_code: '', safe_summary: '', partial_commits: terminal ? 'all_windows_confirmed' : 'none_started', release_confirmed: terminal }
}
function deferred<T>() {
  // The application/test tsconfig targets ES2020; Promise.withResolvers is unavailable.
  let resolve!: (value: T) => void
  let reject!: (reason: unknown) => void
  const promise = new Promise<T>((yes, no) => { resolve = yes; reject = no })
  return { promise, resolve, reject }
}
const key = (owner = 'owner-a', source = 'source-a') => 'yanfa:data-sync:1:' + JSON.stringify([owner, source])
const stops: (() => void)[] = []
function setup(chats = [chat()]) {
  let snapshot = status(chats[0].source_id)
  const jobs = new Map<string, DataSyncJob>()
  const getStatus = vi.spyOn(dataSyncService, 'getStatus').mockImplementation(async (_chat, _signal, requestId) => ({ ...snapshot,
    ...(requestId ? { request_match: jobs.has(requestId) ? 'found' as const : 'not_found' as const, matched_job: jobs.get(requestId) ?? null } : {}) }))
  const getJob = vi.spyOn(dataSyncService, 'getJob').mockImplementation(async (_chat, jobId) => Array.from(jobs.values()).find(job => job.job_id === jobId)!)
  const getChat = vi.spyOn(dataSyncService, 'getChat').mockImplementation(async id => chats.find(chat => chat.id === id)!)
  const create = vi.spyOn(dataSyncService, 'createJob').mockImplementation(async (_chat, requestId) => {
    const accepted = job(requestId, chats[0].source_id)
    jobs.set(requestId, accepted)
    snapshot = { ...snapshot, source_revision: snapshot.source_revision + 1, maintenance_state: 'syncing', can_start: false, ask_allowed: false, active_job: accepted }
    return accepted
  })
  const coordinator = new SourceAccessCoordinator()
  coordinator.configure(chats[0].user_id, chats[0], chats)
  stops.push(installSourceAccess(coordinator))
  const wrapper = ({ children }: { children: ReactNode }) => <SourceAccessContext.Provider value={coordinator}>{children}</SourceAccessContext.Provider>
  return { coordinator, wrapper, getStatus, getJob, getChat, create, jobs, setStatus(next: DataSyncStatus) { snapshot = next } }
}
beforeEach(() => {
  vi.useFakeTimers()
  localStorage.clear()
  Object.defineProperty(document, 'visibilityState', { configurable: true, value: 'visible' })
  Object.defineProperty(navigator, 'onLine', { configurable: true, value: true })
  let serial = Promise.resolve()
  Object.defineProperty(navigator, 'locks', { configurable: true, value: {
    request: vi.fn((_name: string, callback: () => unknown) => {
      const result = serial.then(callback)
      serial = result.then(() => {}, () => {})
      return result
    }),
  } })
})
afterEach(() => {
  cleanup(); stops.splice(0).reverse().forEach(stop => stop())
  vi.restoreAllMocks(); vi.useRealTimers()
})

describe('sync operation consumer boundaries', () => {
  test('opening and double confirmation keep a single safely persisted operation, separate from question admission', async () => {
    const h = setup()
    h.setStatus({ ...status(), ask_allowed: false })
    const hook = renderHook(() => useDataSyncJob(chat(), true), { wrapper: h.wrapper })
    await act(async () => {})
    expect(h.create).not.toHaveBeenCalled()
    expect(hook.result.current.canStart).toBe(true)
    const held = deferred<DataSyncJob>()
    h.create.mockImplementationOnce(async (_id, requestId) => {
      const record = JSON.parse(localStorage.getItem(key())!)
      expect(record).toEqual({ version: 1, owner: 'owner-a', source: 'source-a', requestId, chatId: 'chat-a', bindingRevision: 1 })
      expect(JSON.stringify(record)).not.toMatch(/endpoint|tunnel|private-ca|binding"|connection/)
      return held.promise
    })
    let submitted: Promise<void> | undefined
    await act(async () => { submitted = hook.result.current.start(); void hook.result.current.start() })
    expect(h.create).toHaveBeenCalledTimes(1)
    const requestId = h.create.mock.calls[0][1]
    await act(async () => { held.resolve(job(requestId)); await submitted })
    expect(hook.result.current.job?.job_id).toBe(`job-${requestId}`)
    expect(JSON.parse(localStorage.getItem(key())!).requestId).toBe(requestId)
  })

  test('lost202 followed by a terminal reload uses the exact saved ID even after can_start is revoked', async () => {
    const h = setup()
    h.create.mockRejectedValueOnce(new Error('lost 202'))
    const first = renderHook(() => useDataSyncJob(chat(), true), { wrapper: h.wrapper })
    await act(async () => {})
    await act(async () => { await first.result.current.start() })
    const requestId = h.create.mock.calls[0][1]
    expect(JSON.parse(localStorage.getItem(key())!).requestId).toBe(requestId)
    first.unmount()
    h.jobs.set(requestId, job(requestId, 'source-a', true))
    h.setStatus({ ...status(), can_start: false, source_revision: 12 })
    const restored = renderHook(() => useDataSyncJob(chat(), true), { wrapper: h.wrapper })
    await act(async () => {})
    expect(restored.result.current.job?.job_id).toBe(`job-${requestId}`)
    expect(restored.result.current.terminal).toBe(true)
    expect(h.getStatus.mock.calls.some(call => call[2] === requestId)).toBe(true)
    expect(h.create).toHaveBeenCalledTimes(1)
    expect(localStorage.getItem(key())).toBeNull()
    const reads = h.getJob.mock.calls.length
    await act(async () => { vi.advanceTimersByTime(8000) })
    expect(h.getJob).toHaveBeenCalledTimes(reads)
  })

  test('held POST plus not_found never retries; another same-source Chat can explicitly resend only the same ID', async () => {
    const chats = [chat(), chat('chat-b')]
    const h = setup(chats)
    const held = deferred<DataSyncJob>()
    h.create.mockReturnValueOnce(held.promise)
    const first = renderHook(() => useDataSyncJob(chats[0], true), { wrapper: h.wrapper })
    await act(async () => {})
    let sent: Promise<void> | undefined
    await act(async () => { sent = first.result.current.start() })
    const requestId = h.create.mock.calls[0][1]
    await act(async () => { vi.advanceTimersByTime(6000) })
    expect(first.result.current.notFound).toBe(true)
    expect(h.create).toHaveBeenCalledTimes(1)
    first.unmount()
    act(() => h.coordinator.configure('owner-a', chats[1], chats))
    const second = renderHook(() => useDataSyncJob(chats[1], true), { wrapper: h.wrapper })
    await act(async () => {})
    expect(second.result.current.pending?.requestId).toBe(requestId)
    expect(second.result.current.canStart).toBe(false)
    h.create.mockRejectedValueOnce({ response: { data: { data: { code: 'SYNC_FORBIDDEN' } } } })
    await act(async () => { await second.result.current.resend() })
    expect(h.create.mock.calls.map(call => [call[0], call[1]])).toEqual([['chat-a', requestId], ['chat-a', requestId]])
    expect(JSON.parse(localStorage.getItem(key())!).requestId).toBe(requestId)
    await act(async () => { held.resolve(job(requestId)); await sent })
    expect(second.result.current.job).toBeUndefined()
    expect(second.result.current.pending?.requestId).toBe(requestId)
  })

  test.each(['read', 'write', 'roundtrip', 'locks'] as const)('storage %s denial prevents every POST', async failure => {
    const h = setup()
    const hook = renderHook(() => useDataSyncJob(chat(), true), { wrapper: h.wrapper })
    await act(async () => {})
    if (failure === 'read') vi.spyOn(Storage.prototype, 'getItem').mockImplementation(() => { throw new Error('denied') })
    if (failure === 'write') vi.spyOn(Storage.prototype, 'setItem').mockImplementation(() => { throw new Error('quota') })
    if (failure === 'roundtrip') vi.spyOn(Storage.prototype, 'setItem').mockImplementation(() => {})
    if (failure === 'locks') Object.defineProperty(navigator, 'locks', { configurable: true, value: undefined })
    await act(async () => { await hook.result.current.start() })
    expect(h.create).not.toHaveBeenCalled()
    expect(hook.result.current.error).toBe(zhCN.dataSync.storage)
  })

  test('query errors preserve identity and do not offer not_found or an automatic POST', async () => {
    const h = setup()
    h.create.mockRejectedValueOnce(new Error('lost'))
    const hook = renderHook(() => useDataSyncJob(chat(), true), { wrapper: h.wrapper })
    await act(async () => {})
    await act(async () => { await hook.result.current.start() })
    const saved = localStorage.getItem(key())
    h.getStatus.mockRejectedValue(new Error('outage'))
    await act(async () => { await hook.result.current.refresh(); vi.advanceTimersByTime(4000) })
    expect(hook.result.current.notFound).toBe(false)
    expect(hook.result.current.error).toBe(zhCN.dataSync.unavailable)
    expect(localStorage.getItem(key())).toBe(saved)
    expect(h.create).toHaveBeenCalledTimes(1)
  })

  test('source revision and question admission changes do not reject valid job metadata', async () => {
    const h = setup()
    const hook = renderHook(() => useDataSyncJob(chat(), true), { wrapper: h.wrapper })
    await act(async () => {})
    await act(async () => { await hook.result.current.start() })
    const requestId = h.create.mock.calls[0][1]
    h.jobs.set(requestId, { ...job(requestId), revision: 3, stage: 'verifying', processed_count: 7, succeeded_count: 7 })
    h.setStatus({ ...status(), source_revision: 20, ask_allowed: false, can_start: false, maintenance_state: 'recovering' })
    await act(async () => { await h.coordinator.revalidate('chat-a'); await hook.result.current.refresh() })
    expect(hook.result.current.job).toMatchObject({ job_id: `job-${requestId}`, stage: 'verifying', processed_count: 7 })
    expect(hook.result.current.terminal).toBe(false)
    expect(hook.result.current.error).toBe('')
    expect(h.coordinator.view('chat-a').allowed).toBe(false)
    expect(localStorage.getItem(key())).not.toBeNull()
  })

  test.each(['owner', 'binding', 'source'] as const)('%s switch isolates late POST and its finally from the new operation', async change => {
    const h = setup()
    const held = deferred<DataSyncJob>()
    h.create.mockReturnValueOnce(held.promise)
    const hook = renderHook(({ selected }) => useDataSyncJob(selected, true), { initialProps: { selected: chat() }, wrapper: h.wrapper })
    await act(async () => {})
    let sent: Promise<void> | undefined
    await act(async () => { sent = hook.result.current.start() })
    const requestId = h.create.mock.calls[0][1]
    const old = localStorage.getItem(key())
    const next = { ...chat('chat-b', change === 'owner' ? 'owner-b' : 'owner-a', change === 'source' ? 'source-b' : 'source-a'), source_binding_revision: change === 'binding' ? 2 : 1 }
    h.setStatus({ ...status(next.source_id), binding_revision: next.source_binding_revision, can_start: false })
    await act(async () => { h.coordinator.configure(next.user_id, next, [next]); hook.rerender({ selected: next }) })
    await act(async () => { held.resolve(job(requestId)); await sent })
    expect(hook.result.current.job).toBeUndefined()
    expect(hook.result.current.sending).toBe(false)
    expect(localStorage.getItem(key())).toBe(old)
    if (change === 'owner') expect(hook.result.current.pending).toBeUndefined()
  })

  test('two concurrent consumers allocate only one ID and terminal cleanup cannot delete a later record', async () => {
    const h = setup()
    const held = deferred<DataSyncJob>()
    h.create.mockReturnValueOnce(held.promise)
    const a = renderHook(() => useDataSyncJob(chat(), true), { wrapper: h.wrapper })
    const b = renderHook(() => useDataSyncJob(chat(), true), { wrapper: h.wrapper })
    await act(async () => {})
    let first: Promise<void> | undefined
    await act(async () => { first = a.result.current.start(); await b.result.current.start() })
    expect(h.create).toHaveBeenCalledTimes(1)
    const requestId = h.create.mock.calls[0][1]
    const nextId = '22222222-2222-4222-8222-222222222222'
    const later = JSON.stringify({ ...JSON.parse(localStorage.getItem(key())!), requestId: nextId })
    localStorage.setItem(key(), later)
    await act(async () => { window.dispatchEvent(new StorageEvent('storage', { key: key(), newValue: later })) })
    await act(async () => { held.resolve(job(requestId, 'source-a', true)); await first })
    expect(localStorage.getItem(key())).toBe(later)
    expect(a.result.current.pending?.requestId).toBe(nextId)
    expect(a.result.current.job?.request_id).not.toBe(requestId)
    expect(b.result.current.pending?.requestId).toBe(nextId)
  })

  test('R1 F2 late A 202 cannot replace B after both safe terminals clear pending at the unchanged target', async () => {
    const chats = [chat(), chat('chat-b')]
    const h = setup(chats)
    const secondAuthority = new SourceAccessCoordinator()
    secondAuthority.configure('owner-a', chats[1], chats)
    const secondWrapper = ({ children }: { children: ReactNode }) => <SourceAccessContext.Provider value={secondAuthority}>{children}</SourceAccessContext.Provider>
    const originalTarget = h.coordinator.capture(chats[0].id)
    const a = renderHook(() => useDataSyncJob(chats[0], true), { wrapper: h.wrapper })
    const b = renderHook(() => useDataSyncJob(chats[1], true), { wrapper: secondWrapper })
    await act(async () => {})
    const held202 = deferred<DataSyncJob>()
    let acceptedA!: DataSyncJob
    h.create.mockImplementationOnce(async (_chatId, requestId) => {
      acceptedA = { ...job(requestId), status: 'created', stage: 'preparing', started_at: null }
      h.jobs.set(requestId, acceptedA)
      h.setStatus({ ...status(), source_revision: 2, can_start: false, ask_allowed: false, maintenance_state: 'syncing', active_job: acceptedA })
      return held202.promise
    })
    let originalPost: Promise<void> | undefined
    await act(async () => { originalPost = a.result.current.start() })
    expect(h.create).toHaveBeenCalledTimes(1)
    const requestA = h.create.mock.calls[0][1]
    expect(h.jobs.get(requestA)?.job_id).toBe(acceptedA.job_id)
    const terminalA: DataSyncJob = { ...acceptedA, status: 'failed', stage: 'verifying', revision: 3,
      finished_at: '2026-01-01T00:01:00Z', release_confirmed: true, safe_summary: 'R1 F2 A 原结果' }
    h.jobs.set(requestA, terminalA)
    h.setStatus({ ...status(), source_revision: 3 })
    await act(async () => { await a.result.current.refresh(); await b.result.current.refresh() })
    expect(a.result.current.job?.safe_summary).toBe(terminalA.safe_summary)
    expect(a.result.current.terminal).toBe(true)
    expect(a.result.current.pending).toBeUndefined()
    expect(localStorage.getItem(key())).toBeNull()

    h.create.mockImplementationOnce(async (chatId, requestId) => {
      const accepted = { ...job(requestId), initiating_chat_id: chatId, safe_summary: 'R1 F2 B 当前运行' }
      h.jobs.set(requestId, accepted)
      h.setStatus({ ...status(), source_revision: 4, can_start: false, ask_allowed: false, maintenance_state: 'syncing', active_job: accepted })
      return accepted
    })
    await act(async () => { await b.result.current.start() })
    expect(h.create).toHaveBeenCalledTimes(2)
    const requestB = h.create.mock.calls[1][1]
    expect(requestB).not.toBe(requestA)
    const acceptedB = h.jobs.get(requestB)!
    expect(acceptedB.job_id).not.toBe(acceptedA.job_id)
    await act(async () => {
      window.dispatchEvent(new StorageEvent('storage', { key: key(), newValue: localStorage.getItem(key()) }))
      await a.result.current.refresh()
    })
    expect(a.result.current.job?.job_id).toBe(acceptedB.job_id)
    expect(a.result.current.job?.safe_summary).toBe('R1 F2 B 当前运行')
    expect(a.result.current.pending?.requestId).toBe(requestB)
    const terminalB: DataSyncJob = { ...acceptedB, status: 'failed', stage: 'verifying', revision: 3,
      started_at: '2026-01-01T00:02:00Z', finished_at: '2026-01-01T00:03:00Z', processed_count: 2, failed_count: 2,
      partial_commits: 'unknown', release_confirmed: true, safe_summary: 'R1 F2 B 当前结果' }
    h.jobs.set(requestB, terminalB)
    h.setStatus({ ...status(), source_revision: 5 })
    await act(async () => { await a.result.current.refresh(); await b.result.current.refresh() })
    expect(a.result.current.job).toMatchObject({ job_id: terminalB.job_id, safe_summary: terminalB.safe_summary, processed_count: 2, failed_count: 2 })
    expect(a.result.current.terminal).toBe(true)
    expect(a.result.current.pending).toBeUndefined()
    expect(localStorage.getItem(key())).toBeNull()
    expect(h.coordinator.isCurrent(originalTarget)).toBe(true)
    expect(document.visibilityState).toBe('visible')
    expect(a.result.current.sending).toBe(true)

    // Delivery alone is delayed: A was accepted before both metadata recoveries.
    await act(async () => { held202.resolve(acceptedA); await originalPost; await a.result.current.refresh() })
    expect(h.create.mock.calls.map(call => [call[0], call[1]])).toEqual([['chat-a', requestA], ['chat-b', requestB]])
    expect(h.jobs.size).toBe(2)
    expect(h.coordinator.isCurrent(originalTarget)).toBe(true)
    expect(localStorage.getItem(key())).toBeNull()
    expect(b.result.current.job?.job_id).toBe(terminalB.job_id)
    expect(a.result.current.job).toMatchObject({ job_id: terminalB.job_id, safe_summary: terminalB.safe_summary,
      started_at: terminalB.started_at, processed_count: 2, failed_count: 2 })
  })

  test.each(['success', 'failure'] as const)('R1 F2 late A %s and finally cannot clear a newer explicit POST after safe release', async outcome => {
    const h = setup()
    const hook = renderHook(() => useDataSyncJob(chat(), true), { wrapper: h.wrapper })
    await act(async () => {})
    const target = h.coordinator.capture('chat-a')
    const heldA = deferred<DataSyncJob>()
    let acceptedA!: DataSyncJob
    h.create.mockImplementationOnce(async (_chatId, requestId) => {
      acceptedA = { ...job(requestId), status: 'created', stage: 'preparing', started_at: null }
      h.jobs.set(requestId, acceptedA)
      h.setStatus({ ...status(), source_revision: 2, can_start: false, ask_allowed: false, maintenance_state: 'syncing', active_job: acceptedA })
      return heldA.promise
    })
    let postA: Promise<void> | undefined
    await act(async () => { postA = hook.result.current.start() })
    const requestA = h.create.mock.calls[0][1]
    h.jobs.set(requestA, { ...acceptedA, revision: 3, status: 'failed', release_confirmed: true, safe_summary: 'A 已安全结束' })
    h.setStatus({ ...status(), source_revision: 3 })
    await act(async () => { await hook.result.current.refresh(); await h.coordinator.revalidate('chat-a') })
    expect(hook.result.current.terminal).toBe(true)
    expect(localStorage.getItem(key())).toBeNull()
    expect(hook.result.current.sending).toBe(true)
    expect(hook.result.current.canStart).toBe(true)

    const heldB = deferred<DataSyncJob>()
    let acceptedB!: DataSyncJob
    h.create.mockImplementationOnce(async (_chatId, requestId) => {
      acceptedB = { ...job(requestId), safe_summary: 'B 当前同步' }
      h.jobs.set(requestId, acceptedB)
      h.setStatus({ ...status(), source_revision: 4, can_start: false, ask_allowed: false, maintenance_state: 'syncing', active_job: acceptedB })
      return heldB.promise
    })
    let postB: Promise<void> | undefined
    await act(async () => { postB = hook.result.current.start() })
    expect(h.create).toHaveBeenCalledTimes(2)
    const requestB = h.create.mock.calls[1][1]
    expect(requestB).not.toBe(requestA)
    expect(hook.result.current.pending?.requestId).toBe(requestB)
    expect(hook.result.current.sending).toBe(true)
    await act(async () => {
      if (outcome === 'success') heldA.resolve(acceptedA)
      else heldA.reject({ response: { data: { data: { code: 'SYNC_FORBIDDEN' } } } })
      await postA
    })
    expect(h.coordinator.isCurrent(target)).toBe(true)
    expect(hook.result.current.pending?.requestId).toBe(requestB)
    expect(hook.result.current.sending).toBe(true)
    expect(hook.result.current.error).toBe('')
    expect(JSON.parse(localStorage.getItem(key())!).requestId).toBe(requestB)
    await act(async () => { heldB.resolve(acceptedB); await postB })
    expect(hook.result.current.job?.job_id).toBe(acceptedB.job_id)
    expect(hook.result.current.error).toBe('')
    expect(h.create).toHaveBeenCalledTimes(2)
  })

  test.each(['success', 'failure'] as const)('R1 F2 late metadata %s still restores B after another window clears its terminal pending', async outcome => {
    const chats = [chat(), chat('chat-b')]
    const h = setup(chats)
    const secondAuthority = new SourceAccessCoordinator()
    secondAuthority.configure('owner-a', chats[1], chats)
    const secondWrapper = ({ children }: { children: ReactNode }) => <SourceAccessContext.Provider value={secondAuthority}>{children}</SourceAccessContext.Provider>
    const a = renderHook(() => useDataSyncJob(chats[0], true), { wrapper: h.wrapper })
    const b = renderHook(() => useDataSyncJob(chats[1], true), { wrapper: secondWrapper })
    await act(async () => {})
    const target = h.coordinator.capture(chats[0].id)
    await act(async () => { await a.result.current.start() })
    const requestA = h.create.mock.calls[0][1]
    h.jobs.set(requestA, { ...job(requestA, 'source-a', true), safe_summary: 'A 之前的结果' })
    h.setStatus({ ...status(), source_revision: 3 })
    await act(async () => { await a.result.current.refresh(); await h.coordinator.revalidate(chats[0].id) })
    expect(a.result.current.terminal).toBe(true)
    expect(localStorage.getItem(key())).toBeNull()
    const held = deferred<DataSyncStatus>()
    h.getStatus.mockReturnValueOnce(held.promise)
    let oldRead: Promise<void> | undefined
    await act(async () => { oldRead = a.result.current.refresh() })
    h.create.mockImplementationOnce(async (chatId, requestId) => {
      const accepted = { ...job(requestId), initiating_chat_id: chatId, safe_summary: 'B 当前同步' }
      h.jobs.set(requestId, accepted)
      h.setStatus({ ...status(), source_revision: 4, can_start: false, ask_allowed: false, maintenance_state: 'syncing', active_job: accepted })
      return accepted
    })
    await act(async () => { await b.result.current.start() })
    expect(h.create).toHaveBeenCalledTimes(2)
    const requestB = h.create.mock.calls[1][1]
    await act(async () => { window.dispatchEvent(new StorageEvent('storage', { key: key(), newValue: localStorage.getItem(key()) })) })
    const terminalB = { ...job(requestB, 'source-a', true), initiating_chat_id: chats[1].id, safe_summary: 'B 安全终态结果' }
    h.jobs.set(requestB, terminalB)
    h.setStatus({ ...status(), source_revision: 5 })
    await act(async () => { await b.result.current.refresh() })
    expect(b.result.current.job?.job_id).toBe(terminalB.job_id)
    expect(localStorage.getItem(key())).toBeNull()
    await act(async () => { window.dispatchEvent(new StorageEvent('storage', { key: key(), newValue: null })) })
    await act(async () => {
      if (outcome === 'success') held.resolve({ ...status(), source_revision: 3 })
      else held.reject(new Error('Late A metadata failure'))
      await oldRead
      await a.result.current.refresh()
    })
    expect(a.result.current.job).toMatchObject({ job_id: terminalB.job_id, safe_summary: terminalB.safe_summary })
    expect(a.result.current.error).toBe('')
    expect(a.result.current.loading).toBe(false)
    expect(a.result.current.pending).toBeUndefined()
    expect(localStorage.getItem(key())).toBeNull()
    expect(h.getStatus.mock.calls.some(call => call[0] === chats[0].id && call[2] === requestB)).toBe(true)
    expect(h.coordinator.isCurrent(target)).toBe(true)
    expect(h.create).toHaveBeenCalledTimes(2)
  })

  test.each(['success', 'failure'] as const)('R1 F2 late same-request %s preserves the confirmed terminal result without unknown recovery', async outcome => {
    const h = setup()
    const hook = renderHook(() => useDataSyncJob(chat(), true), { wrapper: h.wrapper })
    await act(async () => {})
    const held = deferred<DataSyncJob>()
    let accepted!: DataSyncJob
    h.create.mockImplementationOnce(async (_chatId, requestId) => {
      accepted = { ...job(requestId), status: 'created', stage: 'preparing', started_at: null }
      h.jobs.set(requestId, accepted)
      h.setStatus({ ...status(), source_revision: 2, can_start: false, ask_allowed: false, maintenance_state: 'syncing', active_job: accepted })
      return held.promise
    })
    let post: Promise<void> | undefined
    await act(async () => { post = hook.result.current.start() })
    const requestId = h.create.mock.calls[0][1]
    const result: DataSyncJob = { ...accepted, revision: 3, status: 'failed', release_confirmed: true, safe_summary: '原请求已安全完成核验' }
    h.jobs.set(requestId, result)
    h.setStatus({ ...status(), source_revision: 3 })
    await act(async () => { await hook.result.current.refresh(); await h.coordinator.revalidate('chat-a') })
    expect(hook.result.current.terminal).toBe(true)
    expect(hook.result.current.job?.safe_summary).toBe(result.safe_summary)
    expect(localStorage.getItem(key())).toBeNull()
    h.getStatus.mockRejectedValue(new Error('Do not resurrect unknown recovery for a confirmed result'))
    await act(async () => {
      if (outcome === 'success') held.resolve(accepted)
      else held.reject(new Error('Late transport failure'))
      await post
    })
    expect(hook.result.current.job).toMatchObject({ job_id: result.job_id, revision: 3, status: 'failed', safe_summary: result.safe_summary })
    expect(hook.result.current.terminal).toBe(true)
    expect(hook.result.current.error).toBe('')
    expect(hook.result.current.pending).toBeUndefined()
    expect(localStorage.getItem(key())).toBeNull()
    expect(h.create).toHaveBeenCalledTimes(1)
  })

  test('late terminal GET cleanup compares the exact stored operation under the record lock', async () => {
    const h = setup()
    const requestId = '11111111-1111-4111-8111-111111111111'
    const nextId = '22222222-2222-4222-8222-222222222222'
    const record = { version: 1, owner: 'owner-a', source: 'source-a', requestId, chatId: 'chat-a', bindingRevision: 1 }
    localStorage.setItem(key(), JSON.stringify(record))
    const held = deferred<DataSyncStatus>()
    h.getStatus.mockReturnValueOnce(held.promise)
    const hook = renderHook(() => useDataSyncJob(chat(), true), { wrapper: h.wrapper })
    await act(async () => {})
    const later = JSON.stringify({ ...record, requestId: nextId })
    localStorage.setItem(key(), later)
    await act(async () => {
      window.dispatchEvent(new StorageEvent('storage', { key: key(), newValue: later }))
      held.resolve({ ...status(), request_match: 'found', matched_job: job(requestId, 'source-a', true) })
    })
    expect(localStorage.getItem(key())).toBe(later)
    expect(hook.result.current.pending?.requestId).toBe(nextId)
    expect(h.create).not.toHaveBeenCalled()
  })

  test('view-only access follows another accepted job without granting a new start', async () => {
    const h = setup()
    const original = { ...job('other-owner-request'), request_id: undefined, initiating_chat_id: undefined }
    h.jobs.set('other-owner-request', original)
    h.setStatus({ ...status(), can_start: false, ask_allowed: false, maintenance_state: 'syncing', active_job: original })
    const hook = renderHook(() => useDataSyncJob(chat(), true), { wrapper: h.wrapper })
    await act(async () => {})
    expect(hook.result.current.job?.job_id).toBe(original.job_id)
    expect(hook.result.current.canStart).toBe(false)
    await act(async () => { await hook.result.current.start() })
    expect(h.create).not.toHaveBeenCalled()
    expect(localStorage.getItem(key())).toBeNull()
  })

  test.each(['owner', 'source'] as const)('late exact metadata after %s switch cannot publish or clear the old operation', async change => {
    const h = setup()
    const requestId = '11111111-1111-4111-8111-111111111111'
    const record = JSON.stringify({ version: 1, owner: 'owner-a', source: 'source-a', requestId, chatId: 'chat-a', bindingRevision: 1 })
    localStorage.setItem(key(), record)
    const held = deferred<DataSyncStatus>()
    h.getStatus.mockReturnValueOnce(held.promise)
    const hook = renderHook(({ selected }) => useDataSyncJob(selected, true), { initialProps: { selected: chat() }, wrapper: h.wrapper })
    await act(async () => {})
    const next = chat('chat-b', change === 'owner' ? 'owner-b' : 'owner-a', change === 'source' ? 'source-b' : 'source-a')
    h.setStatus({ ...status(next.source_id), source_revision: 30 })
    await act(async () => {
      h.coordinator.configure(next.user_id, next, [next]); hook.rerender({ selected: next })
      held.resolve({ ...status(), request_match: 'found', matched_job: job(requestId, 'source-a', true) })
    })
    expect(hook.result.current.job).toBeUndefined()
    expect(hook.result.current.pending).toBeUndefined()
    expect(localStorage.getItem(key())).toBe(record)
    expect(h.create).not.toHaveBeenCalled()
  })

  test('a new explicitly confirmed operation cannot display the previous operation success while its POST is held', async () => {
    const h = setup()
    const previous = '11111111-1111-4111-8111-111111111111'
    localStorage.setItem(key(), JSON.stringify({ version: 1, owner: 'owner-a', source: 'source-a', requestId: previous, chatId: 'chat-a', bindingRevision: 1 }))
    h.jobs.set(previous, job(previous, 'source-a', true))
    const hook = renderHook(() => useDataSyncJob(chat(), true), { wrapper: h.wrapper })
    await act(async () => {})
    expect(hook.result.current.terminal).toBe(true)
    const held = deferred<DataSyncJob>()
    h.create.mockReturnValueOnce(held.promise)
    let submitted: Promise<void> | undefined
    await act(async () => { submitted = hook.result.current.start() })
    const requestId = h.create.mock.calls[0][1]
    expect(requestId).not.toBe(previous)
    expect(hook.result.current.pending?.requestId).toBe(requestId)
    expect(hook.result.current.job).toBeUndefined()
    expect(hook.result.current.terminal).toBe(false)
    await act(async () => { held.resolve(job(requestId)); await submitted })
  })

  test('closing preserves the operation and terminal progress does not release the source authority', async () => {
    const h = setup()
    const hook = renderHook(({ open }) => useDataSyncJob(chat(), open), { initialProps: { open: true }, wrapper: h.wrapper })
    await act(async () => {})
    await act(async () => { await hook.result.current.start() })
    const requestId = h.create.mock.calls[0][1]
    h.setStatus({ ...status(), source_revision: 3, can_start: false, ask_allowed: false, maintenance_state: 'recovering' })
    await act(async () => { hook.rerender({ open: false }); await h.coordinator.revalidate('chat-a') })
    expect(localStorage.getItem(key())).not.toBeNull()
    h.jobs.set(requestId, { ...job(requestId, 'source-a', true), status: 'failed', partial_commits: 'known_partial_commits', verification_passed: false })
    await act(async () => { await hook.result.current.refresh() })
    expect(hook.result.current.terminal).toBe(true)
    expect(h.coordinator.view('chat-a').allowed).toBe(false)
    expect(hook.result.current.status?.latest_successful_sync_at).toBeNull()
  })
})
