import { ASSISTANT_SCHEMA_VERSION, type AnalysisRequest, type Basis, type Candidate, type DraftRequest, type GroupBy, type MetricID, type ProductSelection, type Scope, type ScopeMode, type Workflow } from '../../types/assistant'

export const ASSISTANT_TIMEZONE = 'Asia/Shanghai'

// The ERP quantity pilot only supports ordinary-sale quantity on the payment
// basis in Asia/Shanghai; other metrics would be rejected as unsupported.
export const ASSISTANT_METRICS: MetricID[] = ['quantity']

const DATE = /^\d{4}-\d{2}-\d{2}$/

export interface AssistantInputValues {
  workflow: Workflow
  basis: Basis
  startDate: string
  endDate: string
  scopeMode: ScopeMode
  platformIds: string
  shopIds: string
  productText: string
  productId: string
  skuIds: string
  groupBy: GroupBy
  metrics: MetricID[]
  threshold: string
}

export function defaultAssistantInput(now = new Date()): AssistantInputValues {
  const end = businessDate(now)
  const start = businessDate(addDays(now, -7))
  return {
    workflow: 'product_analysis',
    basis: 'payment',
    startDate: start,
    endDate: end,
    scopeMode: 'all',
    platformIds: '',
    shopIds: '',
    productText: '',
    productId: '',
    skuIds: '',
    groupBy: 'shop',
    metrics: [...ASSISTANT_METRICS],
    threshold: '',
  }
}

export function reversedDate(startDate: string, endDate: string): boolean {
  return DATE.test(startDate) && DATE.test(endDate) && startDate >= endDate
}

export function splitIds(value: string): string[] {
  return value.split(/[,\uff0c\s]+/).map(part => part.trim()).filter(Boolean)
}

function scopeOf(values: AssistantInputValues): Scope {
  if (values.scopeMode === 'all') return { mode: 'all' }
  const scope: Scope = { mode: 'selected' }
  const platforms = splitIds(values.platformIds)
  const shops = splitIds(values.shopIds)
  if (platforms.length > 0) scope.platform_ids = platforms
  if (shops.length > 0) scope.shop_ids = shops
  return scope
}

function productOf(values: AssistantInputValues, candidate?: Candidate): ProductSelection | undefined {
  if (candidate?.entity_type === 'sku') return { product_id: '', sku_ids: [candidate.entity_id] }
  if (candidate?.entity_type === 'product') return { product_id: candidate.entity_id }
  if (candidate) return undefined
  const productId = values.productId.trim()
  const skuIds = splitIds(values.skuIds)
  if (!productId && skuIds.length === 0) return undefined
  return {
    product_id: productId,
    ...(skuIds.length > 0 ? { sku_ids: skuIds } : {}),
  }
}

function optionalThreshold(value: string): string | undefined {
  const threshold = value.trim()
  return threshold || undefined
}

export function buildDraft(
  values: AssistantInputValues,
  requestId: string,
  productQuery: string,
  candidate?: Candidate,
): DraftRequest {
  const product = productOf(values, candidate)
  return {
    schema_version: ASSISTANT_SCHEMA_VERSION,
    request_id: requestId,
    workflow: values.workflow,
    basis: values.basis,
    period: {
      start_date: values.startDate,
      end_date_exclusive: values.endDate,
      timezone: ASSISTANT_TIMEZONE,
    },
    scope: scopeOf(values),
    product_query: productQuery.trim(),
    ...(product ? { product } : {}),
    group_by: values.groupBy,
    metrics: values.metrics,
    ...(optionalThreshold(values.threshold) ? { low_margin_threshold: values.threshold.trim() } : {}),
  }
}

export function buildRunRequest(values: AssistantInputValues, requestId: string, candidate?: Candidate): AnalysisRequest {
  const product = productOf(values, candidate) ?? { product_id: '' }
  return {
    schema_version: ASSISTANT_SCHEMA_VERSION,
    request_id: requestId,
    workflow: values.workflow,
    basis: values.basis,
    period: {
      start_date: values.startDate,
      end_date_exclusive: values.endDate,
      timezone: ASSISTANT_TIMEZONE,
    },
    scope: scopeOf(values),
    product,
    group_by: values.groupBy,
    metrics: values.metrics,
    ...(optionalThreshold(values.threshold) ? { low_margin_threshold: values.threshold.trim() } : {}),
  }
}

export function selectedIdentity(body: { product?: ProductSelection }): string[] {
  const product = body.product
  if (!product) return []
  return [product.product_id, ...(product.sku_ids ?? [])].filter(Boolean)
}

function businessDate(date: Date): string {
  const parts = new Intl.DateTimeFormat('en-CA', {
    timeZone: ASSISTANT_TIMEZONE,
    year: 'numeric',
    month: '2-digit',
    day: '2-digit',
  }).formatToParts(date)
  const pick = (type: string) => parts.find(part => part.type === type)?.value ?? ''
  return `${pick('year')}-${pick('month')}-${pick('day')}`
}

function addDays(date: Date, days: number): Date {
  return new Date(date.getTime() + days * 86400000)
}
