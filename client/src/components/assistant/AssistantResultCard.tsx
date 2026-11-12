import { useMemo } from 'react'
import ChartRenderer from '../ChartRenderer'
import { zhCN } from '../../i18n/zh-CN'
import { reportChartModels } from './assistantChartModel'
import type {
  AnalysisReport,
  AnalysisRequest,
  Basis,
  MetricID,
  MetricValue,
  ReportRow,
} from '../../types/assistant'

const copy = zhCN.assistant
const ERP_QUANTITY_PILOT_CONTRACT = 'erp-payment-sale-quantity-pilot-v1'

const METRIC_ORDER: MetricID[] = [
  'quantity',
  'sales',
  'cost',
  'margin',
  'margin_rate',
  'avg_price',
  'refund',
]



function findMetric(metrics: MetricValue[] | undefined, id: MetricID): MetricValue | undefined {
  return metrics?.find(metric => metric.metric_id === id)
}


function currenciesOf(report: AnalysisReport): string {
  const seen: string[] = []
  const push = (currency: string | undefined) => {
    if (currency && !seen.includes(currency)) seen.push(currency)
  }
  push(report.refunds.currency)
  for (const row of report.table.rows) push(row.currency)
  for (const total of report.totals) push(total.currency)
  return seen.join(' / ')
}

function coverageText(report: AnalysisReport): string {
  const parts: string[] = []
  const buckets: Array<[keyof typeof copy.coverage, { entity_id: string }[]]> = [
    ['included', report.coverage.included],
    ['excluded', report.coverage.excluded],
    ['missing', report.coverage.missing],
    ['stale', report.coverage.stale],
    ['unknown', report.coverage.unknown],
  ]
  for (const [key, entities] of buckets) {
    if (entities.length === 0) continue
    parts.push(`${copy.coverage[key]} ${entities.map(entity => entity.entity_id).join(', ')}`)
  }
  if (report.coverage.coverage_ref) parts.push(report.coverage.coverage_ref)
  return parts.join('；')
}

function windowText(report: AnalysisReport): string {
  const start = report.window.start_date || report.window_start.slice(0, 10)
  const end = report.window.end_date_exclusive || report.window_end_exclusive.slice(0, 10)
  return `${start} – ${end} (${report.window.timezone})`
}


function visibleMetrics(report: AnalysisReport): MetricID[] {
  const present = new Set<MetricID>()
  for (const metric of report.totals) present.add(metric.metric_id)
  for (const row of report.table.rows) {
    for (const metric of row.metrics) present.add(metric.metric_id)
  }
  return METRIC_ORDER.filter(id => present.has(id))
}

function cell(row: ReportRow | undefined, id: MetricID, totals?: MetricValue[]): string {
  const metric = row ? findMetric(row.metrics, id) : findMetric(totals, id)
  if (!metric) return ''
  if (metric.value == null || metric.status === 'unavailable') return copy.card.marginUnavailable
  return metric.value
}

export interface AssistantResultCardProps {
  report: AnalysisReport
  pendingBasis?: Basis | null
  onSwitchBasis?: (basis: Basis, request: AnalysisRequest) => void
  sourceRequest?: AnalysisRequest
}

export default function AssistantResultCard({
  report,
  pendingBasis = null,
  onSwitchBasis,
  sourceRequest,
}: AssistantResultCardProps) {
  const metrics = visibleMetrics(report)
  const isQuantityPilot = report.contract_version === ERP_QUANTITY_PILOT_CONTRACT
  const charts = useMemo(() => reportChartModels(report), [report])
  const otherBasis: Basis = report.basis === 'payment' ? 'outstock' : 'payment'
  const loading = pendingBasis != null

  const switchBasis = () => {
    if (!onSwitchBasis || !sourceRequest || loading) return
    onSwitchBasis(otherBasis, {
      ...sourceRequest,
      basis: otherBasis,
      request_id: globalThis.crypto?.randomUUID?.() ?? `req-${Date.now()}-${Math.random().toString(16).slice(2)}`,
      parent_run_id: report.report_id,
    })
  }

  return (
    <section className="assistant-result-card" data-run-id={report.report_id} aria-label={copy.card.title}>
      <header>
        <h3>{copy.card.title}</h3>
        <p>{copy.outcome[report.outcome]}</p>
      </header>
      <dl>
        <div>
          <dt>{copy.card.basis}</dt>
          <dd data-field="basis">{copy.basis[report.basis]}</dd>
        </div>
        <div>
          <dt>{copy.card.window}</dt>
          <dd data-field="window">{windowText(report)}</dd>
        </div>
        <div>
          <dt>{copy.card.currency}</dt>
          <dd data-field="currency">{currenciesOf(report)}</dd>
        </div>
        <div>
          <dt>{copy.card.dataAsOf}</dt>
          <dd data-field="data-as-of">{report.data_as_of}</dd>
        </div>
        <div>
          <dt>{copy.card.coverage}</dt>
          <dd data-field="coverage">{coverageText(report)}</dd>
        </div>
      </dl>
      {isQuantityPilot && <p>{copy.card.erpPilotCoverage}</p>}
      {loading && (
        <p role="status" data-field="pending-basis">{copy.card.loadingNewBasis}</p>
      )}
      <table>
        <thead>
          <tr>
            <th>{copy.card.entity}</th>
            {metrics.map(id => <th key={id}>{copy.metrics[id]}</th>)}
          </tr>
        </thead>
        <tbody>
          {report.table.rows.map(row => (
            <tr key={row.entity_id} data-entity-id={row.entity_id}>
              <th>{row.entity_id}</th>
              {metrics.map(id => <td key={id}>{cell(row, id)}</td>)}
            </tr>
          ))}
        </tbody>
        <tfoot>
          <tr data-entity-id="totals">
            <th>{copy.card.title}</th>
            {metrics.map(id => <td key={id}>{cell(undefined, id, report.totals)}</td>)}
          </tr>
        </tfoot>
      </table>
      {!isQuantityPilot && (
        <>
          <p data-field="margin-label">{report.margin_label}</p>
          <p data-field="refund">
            {copy.card.refund} {report.refunds.amount ?? copy.card.marginUnavailable}
          </p>
        </>
      )}
      {charts.map(chart => (
        <div key={chart.chartId} data-chart-id={chart.chartId} data-chart-input={JSON.stringify(chart.data)}>
          <ChartRenderer config={chart.config} data={chart.data} />
        </div>
      ))}
      {(report.trend ?? []).filter(point => point.value == null).map(point => (
        <p key={`${point.business_date ?? point.dimension}:${point.metric_id}`} data-missing-trend={point.business_date ?? point.dimension}>
          {copy.card.missingTrend} {point.business_date ?? point.dimension}
        </p>
      ))}
      {!isQuantityPilot && onSwitchBasis && sourceRequest && (
        <button type="button" onClick={switchBasis} disabled={loading}>
          {copy.card.switchBasis}
        </button>
      )}
    </section>
  )
}
