import { useCallback, useEffect, useRef, useState } from 'react'
import assistantService from '../services/assistantService'
import { sourceAccess } from '../contexts/SourceAccessContext'
import {
  ASSISTANT_RUN_UPDATED,
  isRunStatus,
  isTerminalRunStatus,
  type AnalysisRequest,
  type AssistantRunUpdated,
  type Basis,
  type RunStatus,
  type RunView,
} from '../types/assistant'

export interface UseAssistantRunParams {
  chatId: string
  runId: string | undefined
}

/**
 * Local marker for a run created by POST that has not yet produced an
 * authoritative GET RunView. It only carries real POST fields and the submitted
 * basis; no backend field (normalized_input/report) is fabricated here.
 */
export interface PendingRun {
  run_id: string
  status: RunStatus
  basis?: Basis
}

export interface UseAssistantRunResult {
  displayed: RunView | null
  pending: PendingRun | null
  error: unknown
  switchBasis: (request: AnalysisRequest) => Promise<void>
}

/**
 * Status-only SSE notice. A notice that carries report/financial payload is
 * deliberately rejected here: financial values must only ever come from the
 * authoritative GET response, never from the SSE stream.
 */
function isRunNotice(value: unknown): value is AssistantRunUpdated {
  if (!value || typeof value !== 'object') return false
  const notice = value as Partial<AssistantRunUpdated>
  return typeof notice.run_id === 'string'
    && typeof notice.message_id === 'string'
    && typeof notice.revision === 'number'
    && typeof notice.stage === 'string'
    && isRunStatus(notice.status)
    && !('report' in notice)
    && !('totals' in notice)
    && !('charts' in notice)
}

interface TrackedRun {
  revision: number
  noticeRevision: number
  loaded: boolean
  status?: RunView['status']
}

interface RecoveryController {
  chatId: string
  runId: string | undefined
  switchBasis: (request: AnalysisRequest) => Promise<void>
}

/** Serial authoritative refresh for non-terminal runs; not a business timeout. */
const RECOVERY_INTERVAL_MS = 2000

export function useAssistantRun({ chatId, runId }: UseAssistantRunParams): UseAssistantRunResult {
  const [displayed, setDisplayed] = useState<RunView | null>(null)
  const [pending, setPending] = useState<PendingRun | null>(null)
  const [error, setError] = useState<unknown>(null)
  const controller = useRef<RecoveryController | null>(null)

  useEffect(() => {
    let disposed = false
    let activeId = runId
    let pendingId: string | undefined
    let creating = 0
    let loading = false
    let timer: number | undefined
    const tracked = new Map<string, TrackedRun>()
    const queued = new Set<string>()
    if (runId) tracked.set(runId, { revision: -1, noticeRevision: -1, loaded: false })

    const clearTimer = () => {
      window.clearTimeout(timer)
      timer = undefined
    }
    const schedule = () => {
      clearTimer()
      if (disposed || loading) return
      for (const run of tracked.values()) {
        if (!run.loaded || !run.status || !isTerminalRunStatus(run.status)) {
          timer = window.setTimeout(refreshRecoverable, RECOVERY_INTERVAL_MS)
          return
        }
      }
    }
    const apply = (id: string, run: TrackedRun, view: RunView) => {
      if (view.run_id !== id || view.revision < run.revision) return
      run.revision = view.revision
      run.loaded = true
      run.status = view.status
      if (id === activeId) {
        setDisplayed(current => {
          if (!current?.report || view.report) return view
          if (view.status === 'failed' || view.status === 'cancelled' || view.status === 'interrupted') {
            return { ...view, report: current.report }
          }
          return current
        })
      } else if (id === pendingId) {
        if (view.status === 'completed' && view.report) {
          if (activeId) tracked.delete(activeId)
          activeId = id
          pendingId = undefined
          setDisplayed(view)
          setPending(null)
        } else {
          setPending({ run_id: view.run_id, status: view.status, basis: view.normalized_input.basis })
        }
      }
    }
    async function drain() {
      if (disposed || loading) return
      loading = true
      clearTimer()
      try {
        while (queued.size > 0) {
          const id = queued.values().next().value
          if (!id) break
          queued.delete(id)
          const run = tracked.get(id)
          if (!run) continue
          try {
            const view = await assistantService.getRun(chatId, id)
            if (disposed) return
            if (tracked.get(id) !== run) continue
            setError(null)
            apply(id, run, view)
          } catch (cause) {
            if (disposed) return
            if (tracked.get(id) === run) setError(cause)
          }
        }
      } finally {
        loading = false
        schedule()
      }
    }
    function refreshRecoverable() {
      if (disposed) return
      for (const [id, run] of tracked) {
        if (!run.loaded || !run.status || !isTerminalRunStatus(run.status)) queued.add(id)
      }
      void drain()
    }
    const onUpdate = (event: Event) => {
      const notice = (event as CustomEvent<unknown>).detail
      if (!isRunNotice(notice)) return
      const run = tracked.get(notice.run_id)
      if (!run || notice.revision <= Math.max(run.revision, run.noticeRevision)) return
      run.noticeRevision = notice.revision
      queued.add(notice.run_id)
      void drain()
    }
    const onVisible = () => {
      if (document.visibilityState === 'visible') refreshRecoverable()
    }

    controller.current = {
      chatId,
      runId,
      async switchBasis(request) {
        const seq = ++creating
        try {
          const access = sourceAccess()
          const target = await access.admit(chatId)
          access.assert(target)
          const created = await assistantService.createRun(chatId, request)
          if (disposed || seq !== creating) return
          access.assertCurrent(target)
          setError(null)
          if (created.run_id !== activeId) {
            if (pendingId) {
              tracked.delete(pendingId)
              queued.delete(pendingId)
            }
            pendingId = created.run_id
            const run: TrackedRun = { revision: -1, noticeRevision: -1, loaded: false }
            run.status = created.status
            tracked.set(created.run_id, run)
            setPending({ run_id: created.run_id, status: created.status, basis: request.basis })
          }
          queued.add(created.run_id)
          void drain()
        } catch (cause) {
          if (!disposed && seq === creating) setError(cause)
        }
      },
    }
    setDisplayed(null)
    setPending(null)
    setError(null)
    globalThis.addEventListener(ASSISTANT_RUN_UPDATED, onUpdate)
    globalThis.addEventListener('online', refreshRecoverable)
    document.addEventListener('visibilitychange', onVisible)
    refreshRecoverable()
    return () => {
      disposed = true
      controller.current = null
      clearTimer()
      queued.clear()
      globalThis.removeEventListener(ASSISTANT_RUN_UPDATED, onUpdate)
      globalThis.removeEventListener('online', refreshRecoverable)
      document.removeEventListener('visibilitychange', onVisible)
    }
  }, [chatId, runId])

  const switchBasis = useCallback(async (request: AnalysisRequest) => {
    const current = controller.current
    if (current?.chatId === chatId && current.runId === runId) await current.switchBasis(request)
  }, [chatId, runId])

  return { displayed, pending, error, switchBasis }
}

export function pendingBasisOf(displayed: RunView | null, pending: PendingRun | null): Basis | null {
  if (!pending || pending.run_id === displayed?.run_id) return null
  if (isTerminalRunStatus(pending.status)) return null
  return pending.basis ?? displayed?.report?.basis ?? null
}
