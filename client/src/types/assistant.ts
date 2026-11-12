// Assistant v1 client contract. JSON fields match the shared contract in
// Display copy lives only in i18n/zh-CN.ts.

export const ASSISTANT_SCHEMA_VERSION = 1

export const ASSISTANT_RUN_UPDATED = 'assistant-run-updated'

export type Workflow = 'product_analysis' | 'sales_comparison'

export type Basis = 'payment' | 'outstock'

export type ScopeMode = 'all' | 'selected'

export type GroupBy = 'platform' | 'shop'

export type MetricID =
  | 'quantity'
  | 'sales'
  | 'cost'
  | 'margin'
  | 'margin_rate'
  | 'avg_price'
  | 'refund'

export type RunStatus =
  | 'created'
  | 'running'
  | 'completed'
  | 'failed'
  | 'cancelled'
  | 'interrupted'

export type RunOutcome = 'complete' | 'partial' | 'empty' | 'unavailable'

export type MetricStatus = 'available' | 'partial' | 'unavailable'

export type EntityType = 'product' | 'sku' | 'shop' | 'platform'

export type ChartKind = 'bar' | 'line'

export type ExplanationStatus = 'available' | 'rejected_evidence' | 'unavailable'

export interface DateRange {
  start_date: string
  end_date_exclusive: string
}

export interface Period extends DateRange {
  timezone: string
}

export interface Scope {
  mode: ScopeMode
  platform_ids?: string[]
  shop_ids?: string[]
}

export interface ProductSelection {
  product_id: string
  sku_ids?: string[]
}

export interface AnalysisRequest {
  schema_version: number
  request_id: string
  workflow: Workflow
  basis?: Basis
  period: Period
  trend_period?: DateRange
  scope: Scope
  product: ProductSelection
  group_by: GroupBy
  metrics: MetricID[]
  low_margin_threshold?: string
  stream_id?: string
  parent_run_id?: string
}

export interface DraftRequest {
  schema_version: number
  request_id: string
  workflow: Workflow
  basis?: Basis
  period?: Period
  trend_period?: DateRange
  scope?: Scope
  product_query: string
  product?: ProductSelection
  group_by?: GroupBy
  metrics?: MetricID[]
  low_margin_threshold?: string
  stream_id?: string
  parent_run_id?: string
}

export interface Candidate {
  entity_type: EntityType
  entity_id: string
  label: string
  platform_id?: string
  shop_id?: string
}

export interface Resolution {
  status: 'resolved' | 'needs_input'
  request?: AnalysisRequest
  missing_fields?: string[]
  candidates?: Candidate[]
}

export interface MetricValue {
  metric_id: MetricID
  status: MetricStatus
  value: string | null
  unit?: string
  currency?: string
  reason_codes?: string[]
  coverage_ref?: string
}

export interface CoverageEntity {
  entity_type: EntityType
  entity_id: string
  platform_id?: string
  shop_id?: string
  status: string
  coverage_ref?: string
  reason_codes?: string[]
  currency?: string
}

export interface EvidenceRef {
  ref_id: string
  kind: string
  metric_id?: MetricID
  coverage_ref?: string
  contract_version?: string
  snapshot_read_at?: string
}

export interface Coverage {
  requested: CoverageEntity[]
  expected: CoverageEntity[]
  covered: CoverageEntity[]
  missing: CoverageEntity[]
  stale: CoverageEntity[]
  unknown: CoverageEntity[]
  included: CoverageEntity[]
  excluded: CoverageEntity[]
  gaps?: DateRange[]
  metrics?: MetricID[]
  evidence?: EvidenceRef[]
  data_as_of: string
  coverage_ref?: string
}

export interface ChartPoint {
  dimension: string
  entity_id?: string
  business_date?: string
  metric_id: MetricID
  value: string | null
  status: MetricStatus
  currency?: string
  coverage_ref?: string
  reason_codes?: string[]
}

export interface ChartSpec {
  chart_id: string
  kind: ChartKind
  title: string
  metric_ids: MetricID[]
  dimension: string
  data_ref: string
  points: ChartPoint[]
  coverage_ref?: string
  explanation_refs?: string[]
}

export interface ReportRow {
  entity_type: EntityType
  entity_id: string
  platform_id?: string
  shop_id?: string
  currency?: string
  rank?: number
  threshold_hit?: boolean
  metrics: MetricValue[]
  coverage_ref?: string
  reason_codes?: string[]
}

export interface ReportTable {
  dimension: string
  rows: ReportRow[]
  totals?: MetricValue[]
}

export interface RefundSummary {
  label: string
  amount: string | null
  status: MetricStatus
  currency?: string
  occurrence_window: DateRange
  level?: EntityType
  entity_id?: string
  reason_codes?: string[]
  product_refund?: MetricValue
}

export interface Explanation {
  ref_id: string
  metric_id: MetricID
  value: string
  text: string
  evidence_refs: string[]
}

export interface AnalysisReport {
  report_id: string
  schema_version: number
  workflow: Workflow
  basis: Basis
  requested_scope: Scope
  actual_scope: Scope
  window: Period
  window_start: string
  window_end_exclusive: string
  trend_window?: DateRange
  contract_version: string
  outcome: RunOutcome
  coverage: Coverage
  totals: MetricValue[]
  table: ReportTable
  trend?: ChartPoint[]
  candidates?: ReportRow[]
  charts?: ChartSpec[]
  refunds: RefundSummary
  evidence?: EvidenceRef[]
  data_as_of: string
  margin_label: string
  explanation_status?: ExplanationStatus
  explanations?: Explanation[]
}

export interface RunView {
  run_id: string
  message_id: string
  request_id: string
  status: RunStatus
  stage: string
  revision: number
  report?: AnalysisReport
  normalized_input: AnalysisRequest
  error?: string
}

export interface CreateRunResponse {
  run_id: string
  message_id: string
  status: RunStatus
}

export interface AssistantRunUpdated {
  run_id: string
  message_id: string
  revision: number
  stage: string
  status: RunStatus
}

export function isTerminalRunStatus(status: RunStatus): boolean {
  return status === 'completed' || status === 'failed' || status === 'cancelled' || status === 'interrupted'
}

export function isRunStatus(value: unknown): value is RunStatus {
  return value === 'created' || value === 'running' || value === 'completed'
    || value === 'failed' || value === 'cancelled' || value === 'interrupted'
}
