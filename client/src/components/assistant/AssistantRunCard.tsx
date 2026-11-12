import AssistantResultCard from './AssistantResultCard'
import { pendingBasisOf, useAssistantRun } from '../../hooks/useAssistantRun'
import { formatServiceError, zhCN } from '../../i18n/zh-CN'
import { isTerminalRunStatus, type AnalysisRequest } from '../../types/assistant'
import { useSourceAccess } from '../../hooks/useSourceAccess'

export default function AssistantRunCard({
  chatId,
  runId,
  request,
}: {
  chatId: string
  runId: string
  request?: AnalysisRequest
}) {
  const run = useAssistantRun({ chatId, runId })
  const access = useSourceAccess(chatId)
  const report = run.displayed?.report
  const pendingBasis = pendingBasisOf(run.displayed, run.pending)
  if (!report) {
    if (run.error) return <p role="alert">{formatServiceError(run.error).summary}</p>
    const status = run.displayed?.status
    if (status && isTerminalRunStatus(status)) {
      return <p role={status === 'failed' ? 'alert' : 'status'}>{zhCN.assistant.status[status]}</p>
    }
    return <p role="status">{pendingBasis ? zhCN.assistant.card.loadingNewBasis : zhCN.common.loading}</p>
  }
  const displayedStatus = run.displayed?.status
  const interrupted = displayedStatus && displayedStatus !== 'completed' && isTerminalRunStatus(displayedStatus)
    ? displayedStatus
    : null
  return (
    <>
      {!access.allowed && <p role="status">{access.reason}</p>}
      {Boolean(run.error) && <p role="alert">{formatServiceError(run.error).summary}</p>}
      {run.pending && isTerminalRunStatus(run.pending.status) && (
        <p role={run.pending.status === 'failed' ? 'alert' : 'status'}>
          {run.pending.status === 'completed' ? zhCN.assistant.card.unavailable : zhCN.assistant.status[run.pending.status]}
        </p>
      )}
      {interrupted && (
        <p role={interrupted === 'failed' ? 'alert' : 'status'}>{zhCN.assistant.status[interrupted]}</p>
      )}
      <fieldset disabled={!access.allowed} title={access.reason}>
      <AssistantResultCard
        report={report}
        sourceRequest={run.displayed?.normalized_input ?? request}
        pendingBasis={pendingBasis}
        onSwitchBasis={run.displayed?.normalized_input || request ? (_basis, next) => { void run.switchBasis(next) } : undefined}
      />
      </fieldset>
    </>
  )
}
