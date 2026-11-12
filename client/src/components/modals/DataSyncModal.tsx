import { useLayoutEffect, useRef } from 'react'
import { createPortal } from 'react-dom'
import { X } from 'lucide-react'
import type { Chat } from '../../types/chat'
import type { DataSyncController } from '../../hooks/useDataSyncJob'
import { zhCN } from '../../i18n/zh-CN'
import { chatDisplayName } from '../../utils/chatDisplayName'
import { syncProgressValue } from '../../utils/dataSyncProgress'

interface Props {
  chat: Chat
  sync: DataSyncController
  onClose: () => void
  returnFocus: HTMLElement | null
}
export default function DataSyncModal({ chat, sync, onClose, returnFocus }: Props) {
  const panel = useRef<HTMLDivElement>(null)
  const close = useRef<HTMLButtonElement>(null)
  useLayoutEffect(() => {
    close.current?.focus()
    return () => {
      if (!returnFocus?.isConnected) return
      if (returnFocus.getClientRects().length) { returnFocus.focus(); return }
      for (const trigger of returnFocus.ownerDocument.querySelectorAll<HTMLButtonElement>('.chat-header-menu-button')) {
        if (trigger.getClientRects().length) { trigger.focus(); break }
      }
    }
  }, [returnFocus])
  // A pending or terminal transition can remove the focused action.
  useLayoutEffect(() => {
    if (panel.current && !panel.current.contains(document.activeElement)) close.current?.focus()
  })
  const maintenance = sync.status?.operation_kind === 'external_maintenance'
  const job = maintenance ? null : sync.job
  const succeeded = !maintenance && sync.terminal && job?.status === 'succeeded'
  const stage = maintenance ? zhCN.dataSync.maintenance : job ? sync.terminal ? job.status === 'succeeded' ? zhCN.dataSync.succeeded : job.status === 'failed' ? zhCN.dataSync.failed : zhCN.dataSync.interrupted : zhCN.dataSync[job.stage] : zhCN.dataSync.preparing
  const total = job?.expected_count != null && Number.isSafeInteger(job.expected_count) && job.expected_count > 0 ? job.expected_count : null
  const progressValue = job ? syncProgressValue(job.processed_count, total, succeeded) : undefined
  const confirmedCommits = job && (job.succeeded_count > 0 || job.partial_commits === 'known_partial_commits' || job.partial_commits === 'all_windows_confirmed')
  const commitNotice = !job || succeeded ? undefined : confirmedCommits ? zhCN.dataSync.partial : job.partial_commits === 'unknown' ? zhCN.dataSync.commitUnknown : undefined
  const reason = !sync.status ? zhCN.sourceAccess.unavailable
    : sync.status.maintenance_state === 'recovering' ? zhCN.sourceAccess.recovering
    : sync.status.maintenance_state === 'syncing' ? zhCN.sourceAccess.syncing
    : sync.status.readiness.state !== 'ready' ? zhCN.dataSync.notReady
    : sync.status.block_reason === 'SOURCE_BUSY' ? zhCN.dataSync.busy
    : !sync.status.can_start ? zhCN.dataSync.forbidden : ''
  return createPortal(
    <div className="fixed inset-0 z-[120] bg-black/50 flex items-center justify-center p-3" onMouseDown={event => { if (event.target === event.currentTarget) onClose() }}>
      <div ref={panel} role="dialog" aria-modal="true" aria-labelledby="data-sync-title" aria-describedby="data-sync-scope" tabIndex={-1}
        className="bg-white neo-border rounded-lg w-full max-w-lg max-h-[calc(100dvh-1.5rem)] overflow-y-auto overscroll-contain p-4 md:p-6"
        onKeyDown={event => {
          if (event.key === 'Escape') { event.preventDefault(); event.stopPropagation(); onClose() }
          if (event.key === 'Tab') {
            const elements = Array.from(panel.current?.querySelectorAll<HTMLElement>('button:not(:disabled), [href], [tabindex="0"]') ?? [])
            const first = elements[0], last = elements[elements.length - 1]
            if (event.shiftKey && document.activeElement === first) { event.preventDefault(); last?.focus() }
            else if (!event.shiftKey && document.activeElement === last) { event.preventDefault(); first?.focus() }
          }
        }}>
        <div className="flex items-center justify-between gap-3 mb-4">
          <h2 id="data-sync-title" className="text-xl font-bold">{zhCN.dataSync.title}</h2>
          <button ref={close} type="button" aria-label={zhCN.common.close} onClick={onClose} className="p-2 hover:bg-neo-gray rounded focus-visible:outline-black"><X className="w-5 h-5" /></button>
        </div>
        <p className="font-semibold break-words">{chatDisplayName(chat)}</p>
        <p id="data-sync-scope" className="my-2">{zhCN.dataSync.scope}</p>
        <p className="text-sm bg-yellow-50 border-2 border-black p-3 mb-4">{zhCN.dataSync.pause}</p>
        <dl className="text-sm space-y-2 mb-4">
          <div><dt className="font-semibold">{zhCN.dataSync.lastSuccess}</dt><dd>{sync.status?.latest_successful_sync_at ? new Date(sync.status.latest_successful_sync_at).toLocaleString('zh-CN') : zhCN.dataSync.noSuccess}</dd></div>
          {job && <div><dt className="font-semibold">{zhCN.dataSync.started}</dt><dd>{job.started_at ? new Date(job.started_at).toLocaleString('zh-CN') : zhCN.common.pleaseWait}</dd></div>}
        </dl>
        <div className="my-4" aria-live="polite">
          <p className={`font-bold mb-2 ${succeeded ? 'text-green-700' : ''}`}>{maintenance ? stage : job ? stage : sync.pending ? zhCN.dataSync.pending : zhCN.dataSync.preparing}</p>
          <progress aria-label={zhCN.dataSync.progress} className="w-full h-4 accent-black" max={100} {...(progressValue !== undefined ? { value: progressValue } : {})} />
          {progressValue !== undefined && <p className="text-sm mt-1 text-right">{progressValue}%</p>}
          {maintenance && <p className="text-sm mt-2 break-all">{zhCN.dataSync.maintenanceRef}: {sync.status?.maintenance_operation_ref}</p>}
          {job && <>
            <p className="text-sm mt-2">{zhCN.dataSync.counts(job.succeeded_count, job.failed_count, job.processed_count, total)}</p>
            {total === null && <p className="text-sm">{zhCN.dataSync.unknownTotal}</p>}
            {!sync.terminal && (job.stage === 'verifying' || job.status === 'succeeded' || job.status === 'failed' || job.status === 'interrupted') && <p className="text-sm mt-2">{zhCN.dataSync.releasing}</p>}
            {job.safe_summary && <p className="mt-2 break-words">{job.safe_summary}</p>}
            {commitNotice && <p className="text-sm mt-2 text-amber-800">{commitNotice}</p>}
          </>}
          {sync.pending && <p className="text-sm mt-2">{sync.notFound ? zhCN.dataSync.notFound : zhCN.dataSync.pending}</p>}
        </div>
        {reason && <p className="text-sm mb-2">{reason}</p>}
        {sync.status?.readiness.reasons?.map(code => <p key={code} className="text-sm mb-2">{zhCN.dataSync.reason(code)}</p>)}
        {sync.error && <p role="alert" className="border-2 border-red-700 p-2 my-3 text-sm">{sync.error}</p>}
        <div className="flex flex-wrap gap-3 mt-5">
          {!sync.pending && (!job || sync.terminal) && <button type="button" className="neo-button px-3 py-2 disabled:opacity-50" disabled={!sync.canStart} onClick={() => { void sync.start() }}>{job ? zhCN.dataSync.newOperation : zhCN.dataSync.start}</button>}
          {sync.pending && sync.notFound && <button type="button" className="neo-button px-3 py-2 disabled:opacity-50" disabled={sync.sending || sync.loading || !!sync.error} onClick={() => { void sync.resend() }}>{zhCN.dataSync.resend}</button>}
          <button type="button" className="neo-button-secondary px-3 py-2" disabled={sync.loading} onClick={() => { void sync.refresh() }}>{zhCN.dataSync.refresh}</button>
          <button type="button" className="neo-button-secondary px-3 py-2" onClick={onClose}>{zhCN.common.close}</button>
        </div>
      </div>
    </div>, document.body,
  )
}
