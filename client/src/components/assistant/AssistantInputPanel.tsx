import { FormEvent, useLayoutEffect, useRef, useState } from 'react'
import { formatServiceError, zhCN } from '../../i18n/zh-CN'
import assistantService from '../../services/assistantService'
import { sourceAccess, type SourceTarget } from '../../contexts/SourceAccessContext'
import { useSourceAccess } from '../../hooks/useSourceAccess'
import {
  ASSISTANT_SCHEMA_VERSION,
  type AnalysisRequest,
  type Basis,
  type Candidate,
  type GroupBy,
  type Resolution,
  type ScopeMode,
  type Workflow,
} from '../../types/assistant'
import {
  ASSISTANT_METRICS,
  buildDraft,
  buildRunRequest,
  defaultAssistantInput,
  reversedDate,
  type AssistantInputValues,
} from './assistantInput'

const copy = zhCN.assistant
const fieldClass = 'neo-input w-full px-3 py-2'

export interface AssistantInputPanelProps {
  chatId: string
  isConnected: boolean
  workflow: Workflow
  onWorkflowChange: (workflow: Workflow) => void
  onRunCreated: (run: { runId: string; messageId: string; request: AnalysisRequest }) => void
}

function requestId(): string {
  const cryptoId = globalThis.crypto?.randomUUID?.()
  return cryptoId ?? `req-${Date.now()}-${Math.random().toString(16).slice(2)}`
}

function errorText(error: unknown): string {
  if (!error || typeof error !== 'object' || !('response' in error)) {
    return formatServiceError(error).summary
  }
  const response = error.response
  if (!response || typeof response !== 'object') return formatServiceError(error).summary
  const data = 'data' in response ? response.data : error
  const status = 'status' in response && typeof response.status === 'number' ? response.status : undefined
  return formatServiceError(data, status).summary
}

function fieldLabel(field: string): string {
  return copy.fields[field] ?? field
}

export default function AssistantInputPanel(props: AssistantInputPanelProps) {
  return <ChatAssistantInputPanel key={props.chatId} {...props} />
}

function ChatAssistantInputPanel({ chatId, isConnected, workflow, onWorkflowChange, onRunCreated }: AssistantInputPanelProps) {
  const access = useSourceAccess(chatId)
  const [mode, setMode] = useState<'natural' | 'explicit'>('natural')
  const [question, setQuestion] = useState('')
  const [draft, setValues] = useState<AssistantInputValues>(() => defaultAssistantInput())
  const values = { ...draft, workflow }
  const [resolution, setResolution] = useState<Resolution | null>(null)
  const [error, setError] = useState('')
  const [pending, setPending] = useState(false)
  // Each mounted Chat owns its generation and synchronous submission lock.
  const generation = useRef({ active: false, connected: isConnected })
  const submission = useRef<object | null>(null)
  const resolutionTarget = useRef<SourceTarget | null>(null)
  const sourceBinding = access.coordinator?.capture(chatId).binding
  useLayoutEffect(() => {
    const current = generation.current
    current.active = true
    return () => { current.active = false; submission.current = null }
  }, [])
  useLayoutEffect(() => { generation.current.connected = isConnected }, [isConnected])
  useLayoutEffect(() => { setResolution(null); setError(''); resolutionTarget.current = null }, [workflow, sourceBinding])

  const patch = (next: Partial<AssistantInputValues>) => {
    setValues(current => ({ ...current, ...next }))
    setResolution(null)
    setError('')
  }
  const periodFields = (
    <>
      <label className="text-sm">
        {copy.input.startDate}
        <input aria-label={copy.input.startDate} className={`${fieldClass} mt-1`} type="date" value={values.startDate} onChange={event => patch({ startDate: event.target.value })} />
      </label>
      <label className="text-sm">
        {copy.input.endDate}
        <input aria-label={copy.input.endDate} className={`${fieldClass} mt-1`} type="date" value={values.endDate} onChange={event => patch({ endDate: event.target.value })} />
      </label>
      <label className="text-sm">
        {copy.input.scope}
        <select aria-label={copy.input.scope} className={`${fieldClass} mt-1`} value={values.scopeMode} onChange={event => patch({ scopeMode: event.target.value as ScopeMode })}>
          <option value="all">{copy.input.scopeAll}</option>
          <option value="selected">{copy.input.scopeSelected}</option>
        </select>
      </label>
      <label className="text-sm">
        {copy.input.group}
        <select aria-label={copy.input.group} className={`${fieldClass} mt-1`} value={values.groupBy} onChange={event => patch({ groupBy: event.target.value as GroupBy })}>
          <option value="shop">{copy.input.shop}</option>
          <option value="platform">{copy.input.platform}</option>
        </select>
      </label>
      {values.scopeMode === 'selected' && (
        <>
          <label className="text-sm">
            {copy.input.platformIds}
            <input aria-label={copy.input.platformIds} className={`${fieldClass} mt-1`} value={values.platformIds} placeholder={copy.input.idsPlaceholder} onChange={event => patch({ platformIds: event.target.value })} />
          </label>
          <label className="text-sm">
            {copy.input.shopIds}
            <input aria-label={copy.input.shopIds} className={`${fieldClass} mt-1`} value={values.shopIds} placeholder={copy.input.idsPlaceholder} onChange={event => patch({ shopIds: event.target.value })} />
          </label>
        </>
      )}
      <label className="text-sm">
        {copy.input.threshold}
        <input aria-label={copy.input.threshold} className={`${fieldClass} mt-1`} value={values.threshold} placeholder={copy.input.thresholdPlaceholder} onChange={event => patch({ threshold: event.target.value })} />
      </label>
      <fieldset className="md:col-span-2">
        <legend className="mb-1 text-sm">{copy.input.metrics}</legend>
        <p className="text-xs text-gray-600 mb-1">{copy.input.metricsNote}</p>
        <div className="flex flex-wrap gap-3">
          {ASSISTANT_METRICS.map(metric => (
            <label key={metric} className="flex items-center gap-1 text-sm">
              <input
                type="checkbox"
                checked={values.metrics.includes(metric)}
                onChange={event => {
                  const metrics = event.target.checked
                    ? [...values.metrics, metric]
                    : values.metrics.filter(item => item !== metric)
                  patch({ metrics })
                }}
              />
              {copy.metrics[metric]}
            </label>
          ))}
        </div>
      </fieldset>
    </>
  )


  const execute = async (candidate?: Candidate) => {
    if (submission.current || !generation.current.active) return
    if (!generation.current.connected) {
      setError(copy.input.connectFirst)
      return
    }
    setError('')
    if (reversedDate(values.startDate, values.endDate)) {
      setError(copy.input.reversedDate)
      return
    }
    const token = {}
    const owner = generation.current
    submission.current = token
    const isCurrent = () => owner.active && submission.current === token
    setPending(true)
    try {
      const authority = sourceAccess()
      const target = await authority.admit(chatId)
      authority.assert(target)
      if (candidate && resolutionTarget.current) authority.assertCurrent(resolutionTarget.current)
      const id = requestId()
      let request: AnalysisRequest
      if (candidate || (mode === 'explicit' && (values.productId.trim() || values.skuIds.trim()))) {
        request = buildRunRequest(values, id, candidate)
      } else {
        const query = mode === 'natural' ? question : values.productText
        const resolved = await assistantService.resolve(chatId, buildDraft(values, id, query))
        if (!isCurrent()) return
        authority.assert(target)
        if (resolved.status === 'needs_input' || !resolved.request) {
          resolutionTarget.current = target
          setResolution({ ...resolved, status: 'needs_input', request: undefined })
          return
        }
        request = { ...resolved.request, schema_version: ASSISTANT_SCHEMA_VERSION, request_id: resolved.request.request_id || id }
      }
      if (!isCurrent()) return
      if (!owner.connected) {
        setError(copy.input.connectFirst)
        return
      }
      authority.assert(target)
      const created = await assistantService.createRun(chatId, request)
      if (!isCurrent()) return
      authority.assertCurrent(target)
      setResolution(null)
      onRunCreated({ runId: created.run_id, messageId: created.message_id, request })
    } catch (caught) {
      if (isCurrent()) setError(errorText(caught))
    } finally {
      if (isCurrent()) {
        submission.current = null
        setPending(false)
      }
    }
  }

  const submit = (event: FormEvent) => {
    event.preventDefault()
    void execute()
  }

  const candidates = resolution?.status === 'needs_input' ? resolution.candidates ?? [] : []
  const missing = resolution?.status === 'needs_input' ? resolution.missing_fields ?? [] : []

  return (
    <form aria-label={copy.input.title} className="p-3" onSubmit={submit} aria-busy={pending}>
      <fieldset disabled={pending}>
      <div className="mb-3 flex items-center justify-between gap-3">
        <h2 className="text-base font-semibold">{copy.input.title}</h2>
        <div className="flex gap-2">
          <button type="button" className={mode === 'natural' ? 'neo-button' : 'neo-button-secondary'} onClick={() => setMode('natural')}>
            {copy.input.natural}
          </button>
          <button type="button" className={mode === 'explicit' ? 'neo-button' : 'neo-button-secondary'} onClick={() => setMode('explicit')}>
            {copy.input.explicit}
          </button>
        </div>
      </div>

      {mode === 'natural' ? (
        <div className="mb-3 grid gap-3 md:grid-cols-2">
          <label className="block text-sm md:col-span-2">
            {copy.input.question}
            <textarea
              aria-label={copy.input.question}
              className={`${fieldClass} mt-1`}
              value={question}
              rows={2}
              placeholder={copy.input.questionPlaceholder}
              onChange={event => { setQuestion(event.target.value); setResolution(null); setError('') }}
            />
          </label>
          <label className="text-sm">
            {copy.input.workflow}
            <select aria-label={copy.input.workflow} className={`${fieldClass} mt-1`} value={workflow} onChange={event => onWorkflowChange(event.target.value as Workflow)}>
              <option value="product_analysis">{copy.input.productAnalysis}</option>
              <option value="sales_comparison">{copy.input.salesComparison}</option>
            </select>
          </label>
          <label className="text-sm">
            {copy.input.basis}
            <select aria-label={copy.input.basis} className={`${fieldClass} mt-1`} value={values.basis} onChange={event => patch({ basis: event.target.value as Basis })}>
              <option value="payment">{copy.basis.payment}</option>
              <option value="outstock">{copy.basis.outstock}</option>
            </select>
          </label>
          {periodFields}
        </div>
      ) : (
        <div className="mb-3 grid gap-3 md:grid-cols-2">
          <label className="text-sm">
            {copy.input.workflow}
            <select aria-label={copy.input.workflow} className={`${fieldClass} mt-1`} value={workflow} onChange={event => onWorkflowChange(event.target.value as Workflow)}>
              <option value="product_analysis">{copy.input.productAnalysis}</option>
              <option value="sales_comparison">{copy.input.salesComparison}</option>
            </select>
          </label>
          <label className="text-sm">
            {copy.input.basis}
            <select aria-label={copy.input.basis} className={`${fieldClass} mt-1`} value={values.basis} onChange={event => patch({ basis: event.target.value as Basis })}>
              <option value="payment">{copy.basis.payment}</option>
              <option value="outstock">{copy.basis.outstock}</option>
            </select>
          </label>
          {periodFields}
          <label className="text-sm">
            {copy.input.product}
            <input aria-label={copy.input.product} className={`${fieldClass} mt-1`} value={values.productText} placeholder={copy.input.productPlaceholder} onChange={event => patch({ productText: event.target.value })} />
          </label>
          <label className="text-sm">
            {copy.input.productId}
            <input aria-label={copy.input.productId} className={`${fieldClass} mt-1`} value={values.productId} onChange={event => patch({ productId: event.target.value })} />
          </label>
          <label className="text-sm">
            {copy.input.skuIds}
            <input aria-label={copy.input.skuIds} className={`${fieldClass} mt-1`} value={values.skuIds} placeholder={copy.input.idsPlaceholder} onChange={event => patch({ skuIds: event.target.value })} />
          </label>
        </div>
      )}

      </fieldset>
      {!isConnected && <p role="status" className="mb-2 text-sm">{copy.input.connectFirst}</p>}
      {!access.allowed && <p role="status" className="mb-2 text-sm">{access.reason}</p>}
      <button type="submit" className="neo-button" disabled={pending || !isConnected || !access.allowed} title={access.reason}>
        {pending ? copy.input.submitting : copy.input.submit}
      </button>

      {error && <p role="alert" className="mt-3 text-sm">{error}</p>}

      {resolution?.status === 'needs_input' && (
        <div className="mt-3" data-status="needs_input">
          {candidates.length > 0 && (
            <div role="group" aria-label={copy.input.candidates}>
              <p className="mb-2 text-sm">{copy.input.candidates}</p>
              <div className="flex flex-wrap gap-2">
                {candidates.map(candidate => (
                  <button
                    key={`${candidate.entity_type}:${candidate.entity_id}`}
                    type="button"
                    data-entity-id={candidate.entity_id}
                    data-entity-type={candidate.entity_type}
                    disabled={pending || !isConnected || !access.allowed}
                    title={access.reason}
                    onClick={() => { void execute(candidate) }}
                  >
                    {candidate.label || candidate.entity_id}
                  </button>
                ))}
              </div>
            </div>
          )}
          {missing.length > 0 && (
            <p className="mt-2 text-sm">
              {copy.input.missing}：{missing.map(fieldLabel).join('、')}
            </p>
          )}
        </div>
      )}
    </form>
  )
}
