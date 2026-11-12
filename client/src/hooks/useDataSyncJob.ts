import { useEffect, useRef, useState } from 'react'
import { useSourceAccess } from './useSourceAccess'
import { dataSyncService } from '../services/dataSyncService'
import type { Chat } from '../types/chat'
import type { DataSyncJob, DataSyncStatus } from '../types/dataSync'
import { zhCN } from '../i18n/zh-CN'
import type { SourceTarget } from '../contexts/SourceAccessContext'

// This record identifies an operation, never an execution permission or a busy lock.
interface PendingSync {
  version: 1
  owner: string
  source: string
  requestId: string
  chatId: string
  bindingRevision: number
}
interface FollowedSync {
  requestId?: string
  jobId?: string
}
interface SyncView {
  status?: DataSyncStatus
  job?: DataSyncJob
  pending?: PendingSync
  notFound: boolean
  loading: boolean
  sending: boolean
  error: string
}
export interface DataSyncController extends SyncView {
  terminal: boolean
  canStart: boolean
  refresh: () => Promise<void> | undefined
  start: () => Promise<void> | undefined
  resend: () => Promise<void> | undefined
}
const initial: SyncView = { notFound: false, loading: true, sending: false, error: '' }
const prefix = 'yanfa:data-sync:1:'
function readPending(key: string, owner: string, source: string): PendingSync | undefined {
  let raw: string | null
  let record: PendingSync
  try {
    raw = window.localStorage.getItem(key)
    if (raw === null) return undefined
    record = JSON.parse(raw)
  } catch { throw new Error(zhCN.dataSync.storage) }
  if (!record || typeof record !== 'object' || record.version !== 1 || record.owner !== owner || record.source !== source
    || typeof record.requestId !== 'string' || !/^[0-9a-f]{8}(?:-[0-9a-f]{4}){3}-[0-9a-f]{12}$/i.test(record.requestId)
    || typeof record.chatId !== 'string' || !record.chatId || !Number.isSafeInteger(record.bindingRevision) || record.bindingRevision < 1) {
    throw new Error(zhCN.dataSync.storage)
  }
  return record
}
async function withRecordLock<T>(key: string, action: () => T): Promise<T> {
  // A storage read/write pair is not a cross-tab CAS. Web Locks serialize only
  // record allocation/removal; the backend remains the sole execution authority.
  if (!navigator.locks) throw new Error(zhCN.dataSync.storage)
  return navigator.locks.request(key, action)
}
function terminal(job?: DataSyncJob): boolean {
  return !!job && job.release_confirmed === true
    && (job.status === 'failed' || job.status === 'interrupted' || job.status === 'succeeded' && job.verification_passed === true)
}
function safeFailure(error: unknown): string {
  let data: unknown = error
  for (const key of ['response', 'data', 'data']) {
    if (!data || typeof data !== 'object' || !(key in data)) return zhCN.dataSync.unavailable
    data = Reflect.get(data, key)
  }
  if (data && typeof data === 'object' && 'code' in data && typeof data.code === 'string') return zhCN.dataSync.reason(data.code)
  return zhCN.dataSync.unavailable
}

export function useDataSyncJob(inputChat: Chat, open: boolean): DataSyncController {
  const access = useSourceAccess(inputChat.id)
  const chat = access.chat ?? inputChat
  const coordinator = access.coordinator
  const captured = coordinator?.capture(chat.id)
  // Source admission/revision deliberately does not participate in metadata identity.
  const identity = useRef<SourceTarget>()
  const previous = identity.current
  if (!captured || !previous || captured.instance !== previous.instance || captured.owner !== previous.owner
    || captured.session !== previous.session || captured.selection !== previous.selection
    || captured.chatId !== previous.chatId || captured.binding !== previous.binding) identity.current = captured
  const target = identity.current
  const [snapshot, setSnapshot] = useState<{ target?: SourceTarget; view: SyncView }>({ view: initial })
  const actions = useRef<{ refresh: () => Promise<void>; start: (resend: boolean) => Promise<void> }>()

  useEffect(() => {
    setSnapshot({ target, view: initial })
    if (!coordinator || !target?.binding || target.owner !== chat.user_id || chat.source_classification !== 'registered_erp' || !chat.source_id || !chat.source_binding_revision) {
      actions.current = undefined
      return
    }
    const source = chat.source_id
    const owner = target.owner
    const key = prefix + JSON.stringify([owner, source])
    let live = true
    let state: SyncView = { ...initial }
    let timer: number | undefined
    let reading: Promise<void> | undefined
    let storageChanged = false
    let following: FollowedSync = {}
    let submission: { operation: FollowedSync } | undefined
    let revision = -1
    const controller = new AbortController()
    const current = () => live && coordinator.isCurrent(target)
    const available = () => current() && navigator.onLine !== false && document.visibilityState !== 'hidden'
    const owns = (operation: FollowedSync) => current() && following === operation
    const update = (patch: Partial<SyncView>) => {
      if (!current()) return
      state = { ...state, ...patch }; setSnapshot({ target, view: state })
    }
    // This identity survives terminal pending cleanup. Only current discovery or
    // an explicit persisted request may select a different displayed operation.
    const followOperation = (requestId?: string, jobId?: string): FollowedSync => {
      if (requestId && following.requestId === requestId || jobId && following.jobId === jobId) {
        following.requestId ??= requestId
        following.jobId ??= jobId
        return following
      }
      following = { requestId, jobId }
      update({ job: undefined, pending: undefined, notFound: false, error: '' })
      return following
    }
    const validateJob = (job: DataSyncJob, request?: string) => {
      if (job.schema_version !== 1 || job.source_id !== source || !job.job_id
        || request && job.request_id !== request) throw new Error('Invalid job metadata')
    }
    const acceptJob = (job: DataSyncJob, operation: FollowedSync, request?: string): boolean => {
      if (!owns(operation)) return false
      validateJob(job, request)
      if (operation.jobId && operation.jobId !== job.job_id
        || operation.requestId && job.request_id && operation.requestId !== job.request_id) throw new Error('Conflicting job metadata')
      if (state.job?.job_id === job.job_id && job.revision < state.job.revision) return false
      operation.requestId ??= job.request_id
      operation.jobId = job.job_id
      update({ job })
      return true
    }
    const clearTerminal = async (record: PendingSync, job: DataSyncJob, operation: FollowedSync) => {
      if (!terminal(job) || !owns(operation)) return
      await withRecordLock(key, () => {
        if (!owns(operation)) return
        const stored = readPending(key, owner, source)
        if (stored?.requestId === record.requestId) window.localStorage.removeItem(key)
      })
      if (owns(operation) && state.pending?.requestId === record.requestId) update({ pending: undefined, notFound: false })
    }
    const schedule = () => {
      clearTimeout(timer)
      if (available() && (state.pending || state.job && !terminal(state.job) || !state.job && following.requestId)) timer = window.setTimeout(() => { void refresh() }, 2000)
    }
    const refresh = (): Promise<void> => {
      if (reading) return reading
      if (!available()) { update({ loading: false, error: zhCN.dataSync.unavailable }); return Promise.resolve() }
      clearTimeout(timer)
      let readOwner = following
      const operation = (async () => {
        update({ loading: true })
        let record: PendingSync | undefined
        try { record = readPending(key, owner, source) }
        catch { update({ error: zhCN.dataSync.storage, loading: false }); return }
        if (record) readOwner = followOperation(record.requestId)
        const requestId = record?.requestId ?? (!state.job ? readOwner.requestId : undefined)
        update({ pending: record, notFound: false })
        try {
          const status = await coordinator.readSyncStatus(target, controller.signal, requestId)
          if (!owns(readOwner) || controller.signal.aborted) return
          coordinator.assertCurrent(target)
          if (status.schema_version !== 1 || status.source_id !== source || status.binding_revision !== chat.source_binding_revision
            || !Number.isSafeInteger(status.source_revision)) throw new Error('Invalid status metadata')
          if (status.source_revision < revision) return
          revision = status.source_revision
          if (readPending(key, owner, source)?.requestId !== record?.requestId) { storageChanged = true; return }
          update({ status, error: '' })
          let progress: DataSyncJob | undefined
          if (requestId) {
            if (status.request_match === 'found' && status.matched_job) {
              if (!acceptJob(status.matched_job, readOwner, requestId)) return
              if (record) await clearTerminal(record, status.matched_job, readOwner)
              if (!owns(readOwner)) return
              void coordinator.revalidate(chat.id)
              if (terminal(status.matched_job) && status.active_job?.job_id !== status.matched_job.job_id) progress = status.active_job ?? undefined
            } else if (status.request_match === 'not_found') {
              // Even a later rejection cannot settle an earlier outstanding POST.
              update({ notFound: true })
            } else throw new Error('Missing exact request match')
          } else progress = status.active_job ?? (state.job && !terminal(state.job) ? state.job : undefined)
          if (progress) {
            validateJob(progress)
            readOwner = followOperation(progress.request_id, progress.job_id)
            update({ loading: true })
            if (terminal(progress)) acceptJob(progress, readOwner)
            else {
              const job = await dataSyncService.getJob(chat.id, progress.job_id, controller.signal)
              if (!owns(readOwner) || controller.signal.aborted) return
              coordinator.assertCurrent(target)
              if (readPending(key, owner, source)) { storageChanged = true; return }
              if (acceptJob(job, readOwner) && terminal(job)) void coordinator.revalidate(chat.id)
            }
          }
        } catch (error) {
          if (owns(readOwner) && !controller.signal.aborted) update({ error: safeFailure(error) })
        } finally {
          if (owns(readOwner)) { update({ loading: false }); schedule() }
        }
      })()
      reading = operation
      void operation.finally(() => {
        if (reading === operation) reading = undefined
        if (storageChanged && current()) { storageChanged = false; void refresh() }
      })
      return operation
    }
    const start = async (resend: boolean) => {
      if (submission?.operation === following && !terminal(state.job) || !available()) return
      const attempt = { operation: following }
      submission = attempt
      update({ sending: true, error: '' })
      let record: PendingSync | undefined
      try {
        // Refresh before allocating. Viewing or recovering an old request does not
        // require can_start and never borrows the question admission permission.
        await refresh()
        if (!available() || !owns(attempt.operation)) return
        coordinator.assertCurrent(target)
        if (resend) {
          record = readPending(key, owner, source)
          if (!record || !state.notFound || record.requestId !== state.pending?.requestId) return
          const original = await dataSyncService.getChat(record.chatId, controller.signal)
          if (!available() || !owns(attempt.operation)) return
          coordinator.assertCurrent(target)
          if (original.user_id !== owner || original.id !== record.chatId || original.source_classification !== 'registered_erp'
            || original.source_id !== source || original.source_binding_revision !== record.bindingRevision) {
            update({ error: zhCN.sourceAccess.changed }); return
          }
        } else {
          if (state.pending) return
          if (state.error || state.status?.can_start !== true || state.status.readiness.state !== 'ready') return
          const claim = await withRecordLock(key, () => {
            coordinator.assertCurrent(target)
            if (!owns(attempt.operation)) return
            const stored = readPending(key, owner, source)
            if (stored) {
              attempt.operation = followOperation(stored.requestId)
              update({ pending: stored, sending: true })
              return { record: stored, created: false }
            }
            const next: PendingSync = { version: 1, owner, source, requestId: crypto.randomUUID(), chatId: chat.id, bindingRevision: chat.source_binding_revision! }
            const raw = JSON.stringify(next)
            try {
              window.localStorage.setItem(key, raw)
              if (window.localStorage.getItem(key) !== raw) throw new Error('Storage did not retain the request')
            } catch { throw new Error(zhCN.dataSync.storage) }
            attempt.operation = followOperation(next.requestId)
            update({ pending: next, sending: true })
            return { record: next, created: true }
          })
          if (!claim || !available() || !owns(attempt.operation)) return
          record = claim.record
          update({ pending: record })
          if (!claim.created) { await refresh(); return }
        }
        if (!available() || !owns(attempt.operation)) return
        // Verify storage again immediately before send; never send an unrecorded ID.
        if (readPending(key, owner, source)?.requestId !== record.requestId) throw new Error(zhCN.dataSync.storage)
        coordinator.assertCurrent(target)
        update({ pending: record, notFound: false })
        schedule()
        // Closing/unmounting does not cancel or resolve this operation. A late
        // response may only update its original live view, never another target.
        const accepted = await dataSyncService.createJob(record.chatId, record.requestId)
        if (!owns(attempt.operation)) return
        coordinator.assertCurrent(target)
        const stored = readPending(key, owner, source)
        if (stored && stored.requestId !== record.requestId) return
        if (state.pending && state.pending.requestId !== record.requestId) return
        if (!acceptJob(accepted, attempt.operation, record.requestId)) return
        await clearTerminal(record, accepted, attempt.operation)
        if (!owns(attempt.operation)) return
        void coordinator.revalidate(chat.id)
      } catch (error) {
        // Confirmed terminal metadata already settled this request; a delayed
        // transport failure cannot turn that result back into an unknown request.
        if (owns(attempt.operation) && (!record || !terminal(state.job))) update({ error: error instanceof Error && error.message === zhCN.dataSync.storage ? zhCN.dataSync.storage : safeFailure(error) })
      } finally {
        if (submission === attempt) {
          submission = undefined
          if (current()) update({ sending: false })
        }
        if (owns(attempt.operation) && record && !terminal(state.job)) void refresh()
      }
    }
    const installed = { refresh, start }
    actions.current = installed
    const onStorage = (event: StorageEvent) => {
      if (event.key !== key && event.key !== null) return
      if (!current()) return
      try {
        const record = readPending(key, owner, source)
        if (record) followOperation(record.requestId)
        update({ pending: record, notFound: false, loading: true })
      } catch { update({ error: zhCN.dataSync.storage }); return }
      if (reading) storageChanged = true
      else void refresh()
    }
    window.addEventListener('storage', onStorage)
    void refresh()
    return () => {
      live = false; controller.abort(); clearTimeout(timer)
      window.removeEventListener('storage', onStorage)
      if (actions.current === installed) actions.current = undefined
    }
  }, [coordinator, target, chat.id, chat.user_id, chat.source_classification, chat.source_id, chat.source_binding_revision])

  // T41 continues source discovery after this hook stops terminal progress IO.
  useEffect(() => { void actions.current?.refresh() }, [open, access.status?.active_job?.job_id, access.status?.source_revision])
  const valid = !!target && coordinator?.isCurrent(target) === true
  const state = valid && snapshot.target === target ? snapshot.view : initial
  return {
    ...state,
    terminal: terminal(state.job),
    canStart: valid && !state.loading && (!state.sending || terminal(state.job)) && !state.error && !state.pending
      && (!state.job || terminal(state.job)) && state.status?.can_start === true && state.status.readiness.state === 'ready',
    refresh: () => actions.current?.refresh(),
    start: () => actions.current?.start(false),
    resend: () => actions.current?.start(true),
  }
}
